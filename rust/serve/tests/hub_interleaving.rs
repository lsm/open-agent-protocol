use std::collections::{HashMap, HashSet};
use std::sync::Arc;
use std::thread::{self, JoinHandle};
use std::time::{Duration, Instant};

use futures::executor::block_on;
use futures::StreamExt;
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{Error, OpenRequest, SubmitRequest};
use oap_protocol::{
    Admission, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    RequestedDeliveryMode, RunId, SessionId,
};
use oap_serve::{Hub, HubError, HubSession, Registry, Subscription};

const SETTLE: Duration = Duration::from_secs(10);

struct Seeded(u64);

impl Seeded {
    fn below(&mut self, bound: u64) -> u64 {
        self.0 ^= self.0 << 13;
        self.0 ^= self.0 >> 7;
        self.0 ^= self.0 << 17;
        self.0 % bound
    }
}

struct Delivered {
    run: RunId,
    sequence: u64,
    id: String,
    terminal: bool,
}

struct Reader {
    session: SessionId,
    cursor: Option<RunId>,
    joined: Option<(RunId, u64)>,
    earlier: HashSet<RunId>,
    thread: JoinHandle<Vec<Delivered>>,
}

fn read(mut subscription: Subscription) -> JoinHandle<Vec<Delivered>> {
    thread::spawn(move || {
        let mut events = Vec::new();
        while let Some(item) = block_on(subscription.next()) {
            let envelope = match item {
                Ok(envelope) => envelope,
                Err(HubError::SessionClosed(_) | HubError::Overflow { .. }) => break,
                Err(err) => panic!("a reader ended with {err}"),
            };
            let (Some(run), Some(sequence)) = (envelope.run_id.clone(), envelope.sequence) else {
                continue;
            };
            events.push(Delivered {
                run,
                sequence,
                id: envelope.id.to_string(),
                terminal: [
                    EnvelopeType::RUN_COMPLETED,
                    EnvelopeType::RUN_FAILED,
                    EnvelopeType::RUN_CANCELLED,
                ]
                .contains(&envelope.envelope_type),
            });
        }
        events
    })
}

struct Driver {
    hub: Hub,
    slots: Vec<Arc<HubSession>>,
    opened: usize,
    readers: Vec<Reader>,
    admitted: HashMap<SessionId, HashSet<RunId>>,
}

impl Driver {
    fn new() -> Self {
        let mut registry = Registry::new();
        registry
            .register(
                "memory",
                Arc::new(Memory::new(Config {
                    journal_capacity: 4096,
                    ..Config::default()
                })),
            )
            .unwrap();
        let mut driver = Self {
            hub: Hub::new(registry),
            slots: Vec::new(),
            opened: 0,
            readers: Vec::new(),
            admitted: HashMap::new(),
        };
        for _ in 0..3 {
            let session = driver.fresh();
            driver.slots.push(session);
        }
        driver
    }

    fn fresh(&mut self) -> Arc<HubSession> {
        let id = format!("seeded-{}", self.opened);
        self.opened += 1;
        block_on(self.hub.open(
            "memory",
            OpenRequest {
                session_id: id.as_str().into(),
                ..OpenRequest::default()
            },
        ))
        .unwrap()
        .session
    }

    fn active(&self, slot: usize) -> Option<RunId> {
        let state = block_on(self.slots[slot].state()).unwrap();
        state.active_runs.first().map(|run| run.run_id.clone())
    }

    fn submit(&mut self, slot: usize) {
        let session = &self.slots[slot];
        let request = SubmitRequest {
            request: MessageSubmitRequest {
                session_id: session.id().clone(),
                delivery: RequestedDeliveryMode::AUTO,
                messages: vec![Message {
                    role: MessageRole::USER,
                    content: MessageContent::from_text("run"),
                    ..Message::default()
                }],
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        };
        match block_on(session.submit(request)) {
            Ok(admitted) => {
                assert!(
                    admitted.admission == Admission::STARTED
                        || admitted.admission == Admission::QUEUED
                );
                self.admitted
                    .entry(session.id().clone())
                    .or_default()
                    .extend(admitted.run_id);
            }
            Err(HubError::Adapter(Error::RunActive)) => {}
            Err(err) => panic!("submit: {err}"),
        }
    }

    fn cancel(&self, slot: usize) {
        let Some(run) = self.active(slot) else {
            return;
        };
        match block_on(self.slots[slot].cancel(run)) {
            Ok(_)
            | Err(HubError::Adapter(Error::RunNotFound))
            | Err(HubError::Adapter(Error::RunAlreadyTerminal { .. })) => {}
            Err(err) => panic!("cancel: {err}"),
        }
    }

    fn close(&mut self, slot: usize) {
        match block_on(self.slots[slot].close()) {
            Ok(()) => self.slots[slot] = self.fresh(),
            Err(HubError::Adapter(Error::RunActive)) => {}
            Err(err) => panic!("close: {err}"),
        }
    }

    fn subscribe(&mut self, slot: usize, from_start: bool) {
        let session = self.slots[slot].id().clone();
        let subscription = if from_start {
            block_on(self.hub.subscribe_after(&session, None, 0))
        } else {
            self.hub.subscribe(&session)
        };
        let subscription = match subscription {
            Ok(subscription) => subscription,
            Err(HubError::NoRunToResume) => return,
            Err(err) => panic!("subscribe: {err}"),
        };
        let cursor = from_start.then(|| subscription.run_id().clone());
        let joined = subscription.joined_at();
        let earlier = self.admitted.get(&session).cloned().unwrap_or_default();
        self.readers.push(Reader {
            session,
            cursor,
            joined,
            earlier,
            thread: read(subscription),
        });
    }

    fn step(&mut self, random: &mut Seeded) {
        let slot = random.below(self.slots.len() as u64) as usize;
        match random.below(5) {
            0 | 1 => self.submit(slot),
            2 => self.cancel(slot),
            3 => self.close(slot),
            _ => {
                let from_start = random.below(2) == 0;
                self.subscribe(slot, from_start);
            }
        }
    }

    fn settle(&self) {
        let deadline = Instant::now() + SETTLE;
        for slot in 0..self.slots.len() {
            while self.active(slot).is_some() {
                assert!(Instant::now() < deadline, "a run never settled");
                self.cancel(slot);
            }
            self.replay_every_run(slot, deadline);
            block_on(self.slots[slot].close()).unwrap();
        }
    }

    fn replay_every_run(&self, slot: usize, deadline: Instant) {
        let session = self.slots[slot].id();
        for run in self.admitted.get(session).into_iter().flatten() {
            let subscription =
                block_on(self.hub.subscribe_after(session, Some(run.clone()), 0)).unwrap();
            let thread = read(subscription);
            while !thread.is_finished() {
                assert!(
                    Instant::now() < deadline,
                    "{run:?} never replayed to an end"
                );
                thread::sleep(Duration::from_millis(1));
            }
            let events = thread.join().unwrap();
            assert!(events.last().is_some_and(|event| event.terminal), "{run:?}");
            for (index, event) in events.iter().enumerate() {
                assert_eq!(event.sequence, index as u64 + 1, "{run:?}");
                assert_eq!(event.terminal, index + 1 == events.len(), "{run:?}");
            }
        }
    }

    fn check(self) {
        let deadline = Instant::now() + SETTLE;
        let mut seen: HashMap<(SessionId, RunId, u64), String> = HashMap::new();
        let mut ends: HashMap<(SessionId, RunId), u64> = HashMap::new();
        for reader in self.readers {
            while !reader.thread.is_finished() {
                assert!(Instant::now() < deadline, "a reader never ended");
                thread::sleep(Duration::from_millis(1));
            }
            let mut last: HashMap<RunId, u64> = HashMap::new();
            let mut finished: HashMap<RunId, bool> = HashMap::new();
            for event in reader.thread.join().unwrap() {
                let run = event.run.clone();
                assert!(!finished.get(&run).copied().unwrap_or(false));
                match last.get(&run) {
                    Some(previous) => assert_eq!(event.sequence, previous + 1),
                    None => match &reader.joined {
                        Some((joined, after)) if *joined == run => {
                            assert_eq!(event.sequence, after + 1)
                        }
                        _ if reader.cursor.as_ref() == Some(&run)
                            || !reader.earlier.contains(&run) =>
                        {
                            assert_eq!(event.sequence, 1)
                        }
                        _ => {}
                    },
                }
                last.insert(run.clone(), event.sequence);
                finished.insert(run.clone(), event.terminal);
                let at = (reader.session.clone(), run.clone(), event.sequence);
                let id = seen.entry(at).or_insert_with(|| event.id.clone());
                assert_eq!(*id, event.id);
                if event.terminal {
                    ends.insert((reader.session.clone(), run), event.sequence);
                }
            }
        }
        assert!(!ends.is_empty());
        for (session, run, sequence) in seen.keys() {
            if let Some(end) = ends.get(&(session.clone(), run.clone())) {
                assert!(sequence <= end);
            }
        }
    }
}

#[test]
fn a_seeded_interleaving_keeps_every_run_contiguous_and_its_terminal_last() {
    for seed in 1..=32 {
        let mut random = Seeded(0x9E37_79B9_7F4A_7C15_u64.wrapping_mul(seed));
        let mut driver = Driver::new();
        for _ in 0..200 {
            driver.step(&mut random);
        }
        driver.settle();
        driver.check();
    }
}
