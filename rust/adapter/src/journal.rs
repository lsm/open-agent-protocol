use std::collections::{HashMap, HashSet, VecDeque};
use std::pin::Pin;
use std::sync::{Arc, Mutex, MutexGuard};
use std::task::{Context, Poll, Waker};

use futures_core::Stream;
use oap_protocol::{Envelope, SessionState};

use crate::{Error, EventStream, Recovery, ReplayGap};

pub const FOLLOWER_BUFFER: usize = 64;

type Item = Result<Envelope, Error>;

#[derive(Default)]
struct Follower {
    run: Option<String>,
    cursor: u64,
    position: usize,
    buffer: VecDeque<Item>,
    closed: bool,
    waker: Option<Waker>,
}

#[derive(Default)]
struct Inner {
    capacity: usize,
    entries: VecDeque<Envelope>,
    evicted: usize,
    latest: HashMap<String, u64>,
    ended: HashSet<String>,
    stopped: bool,
    followers: HashMap<u64, Follower>,
    next_follower: u64,
}

fn run_of(envelope: &Envelope) -> &str {
    envelope.run_id.as_ref().map_or("", |id| id.as_str())
}

impl Inner {
    fn find(&self, run: &str, sequence: u64, position: usize) -> Option<(Envelope, usize)> {
        let start = position.saturating_sub(self.evicted);
        self.entries
            .iter()
            .enumerate()
            .skip(start)
            .find_map(|(index, envelope)| {
                (run_of(envelope) == run && envelope.sequence == Some(sequence))
                    .then(|| (envelope.clone(), self.evicted + index + 1))
            })
    }

    fn finished(&self, run: &str, cursor: u64) -> bool {
        self.ended.contains(run) && self.latest.get(run).is_some_and(|latest| *latest <= cursor)
    }

    fn pump(&mut self, id: u64) {
        loop {
            let Some(follower) = self.followers.get(&id) else {
                return;
            };
            let Some(run) = follower.run.clone() else {
                return;
            };
            if follower.closed || follower.buffer.len() >= FOLLOWER_BUFFER {
                return;
            }
            let Some((envelope, next)) = self.find(&run, follower.cursor + 1, follower.position)
            else {
                return;
            };
            let finished = self.finished(&run, follower.cursor + 1);
            let Some(follower) = self.followers.get_mut(&id) else {
                return;
            };
            follower.buffer.push_back(Ok(envelope));
            follower.cursor += 1;
            follower.position = next;
            follower.closed = finished;
        }
    }

    fn wake_all(&mut self) {
        for follower in self.followers.values_mut() {
            if let Some(waker) = follower.waker.take() {
                waker.wake();
            }
        }
    }
}

#[derive(Clone)]
pub struct Journal {
    inner: Arc<Mutex<Inner>>,
}

impl Journal {
    pub fn new(capacity: usize) -> Self {
        Self {
            inner: Arc::new(Mutex::new(Inner {
                capacity,
                ..Inner::default()
            })),
        }
    }

    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }

    pub fn append(&self, envelope: Envelope, terminal: bool) {
        let mut inner = self.lock();
        if let Some(sequence) = envelope.sequence {
            let run = run_of(&envelope).to_owned();
            inner.latest.insert(run.clone(), sequence);
            if terminal {
                inner.ended.insert(run);
            }
        }
        inner.entries.push_back(envelope);
        if inner.entries.len() > inner.capacity {
            inner.entries.pop_front();
            inner.evicted += 1;
        }
        let ids: Vec<u64> = inner.followers.keys().copied().collect();
        for id in ids {
            inner.pump(id);
        }
        inner.wake_all();
    }

    pub fn ended(&self, run: &str) -> Option<u64> {
        let inner = self.lock();
        inner
            .ended
            .contains(run)
            .then(|| inner.latest.get(run).copied().unwrap_or(0))
    }

    pub fn resume(
        &self,
        state: SessionState,
        run: &str,
        after: u64,
        latest: u64,
    ) -> (Recovery, EventStream) {
        let (mut oldest, mut first, mut last) = (0, 0, 0);
        for envelope in self
            .lock()
            .entries
            .iter()
            .filter(|envelope| run_of(envelope) == run)
        {
            let Some(sequence) = envelope.sequence else {
                continue;
            };
            if oldest == 0 {
                oldest = sequence;
            }
            if sequence > after {
                if first == 0 {
                    first = sequence;
                }
                last = sequence;
            }
        }
        let mut recovery = Recovery {
            state,
            run_id: run.into(),
            requested_after: after,
            replayed_from: after,
            replayed_through: after,
            replay_gap: None,
        };
        if after < latest && (oldest == 0 || after + 1 < oldest) {
            recovery.replay_gap = Some(ReplayGap {
                requested_after: after,
                oldest_available: oldest,
                latest_available: latest,
            });
            recovery.replayed_from = 0;
            recovery.replayed_through = 0;
            return (recovery, closed());
        }
        if first != 0 {
            recovery.replayed_from = first;
            recovery.replayed_through = last;
        }
        (recovery, self.follow(run, after))
    }

    fn add_follower(&self, follower: Follower) -> Following {
        let mut inner = self.lock();
        let id = inner.next_follower;
        inner.next_follower += 1;
        inner.followers.insert(id, follower);
        inner.pump(id);
        Following {
            journal: self.clone(),
            id,
        }
    }

    pub fn follow(&self, run: &str, after: u64) -> EventStream {
        Box::pin(self.add_follower(Follower {
            run: Some(run.to_owned()),
            cursor: after,
            ..Follower::default()
        }))
    }

    pub fn reserve(&self) -> Reservation {
        Reservation {
            following: Some(self.add_follower(Follower::default())),
        }
    }

    pub fn close(&self) {
        let mut inner = self.lock();
        inner.stopped = true;
        inner.wake_all();
    }
}

struct Empty;

impl Stream for Empty {
    type Item = Item;

    fn poll_next(self: Pin<&mut Self>, _: &mut Context<'_>) -> Poll<Option<Item>> {
        Poll::Ready(None)
    }
}

pub fn closed() -> EventStream {
    Box::pin(Empty)
}

pub struct Reservation {
    following: Option<Following>,
}

impl Reservation {
    fn id(&self) -> Option<(Journal, u64)> {
        self.following
            .as_ref()
            .map(|following| (following.journal.clone(), following.id))
    }

    pub fn bind(&self, run: &str) {
        let Some((journal, id)) = self.id() else {
            return;
        };
        let mut inner = journal.lock();
        let Some(follower) = inner.followers.get_mut(&id) else {
            return;
        };
        if follower.closed || follower.run.is_some() {
            return;
        }
        follower.run = Some(run.to_owned());
        inner.pump(id);
        inner.wake_all();
    }

    pub fn release(&self) {
        let Some((journal, id)) = self.id() else {
            return;
        };
        let mut inner = journal.lock();
        if let Some(follower) = inner
            .followers
            .get_mut(&id)
            .filter(|follower| follower.run.is_none())
        {
            follower.closed = true;
        }
        inner.wake_all();
    }

    pub fn stream(&mut self) -> EventStream {
        match self.following.take() {
            Some(following) => Box::pin(following),
            None => closed(),
        }
    }
}

struct Following {
    journal: Journal,
    id: u64,
}

impl Drop for Following {
    fn drop(&mut self) {
        self.journal.lock().followers.remove(&self.id);
    }
}

impl Stream for Following {
    type Item = Item;

    fn poll_next(self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<Option<Item>> {
        let mut inner = self.journal.lock();
        let stopped = inner.stopped;
        if !stopped {
            inner.pump(self.id);
        }
        let finished_or_behind = {
            let Some(follower) = inner.followers.get(&self.id) else {
                return Poll::Ready(None);
            };
            follower.run.as_ref().map(|run| {
                let behind = inner
                    .latest
                    .get(run)
                    .is_some_and(|latest| *latest > follower.cursor);
                (inner.finished(run, follower.cursor), behind)
            })
        };
        let Some(follower) = inner.followers.get_mut(&self.id) else {
            return Poll::Ready(None);
        };
        if let Some(item) = follower.buffer.pop_front() {
            return Poll::Ready(Some(item));
        }
        if follower.closed || stopped {
            follower.closed = true;
            return Poll::Ready(None);
        }
        match finished_or_behind {
            Some((true, _)) => {
                follower.closed = true;
                Poll::Ready(None)
            }
            Some((false, true)) => {
                follower.closed = true;
                Poll::Ready(Some(Err(Error::EventStreamOverflow)))
            }
            _ => {
                follower.waker = Some(cx.waker().clone());
                Poll::Pending
            }
        }
    }
}
