use std::collections::{HashMap, HashSet, VecDeque};
use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::task::{Context, Poll, Wake, Waker};
use std::thread::{self, Thread};

use futures_core::Stream;
use oap_adapter::{Error, EventStream};
use oap_protocol::{Envelope, EnvelopeType, RunId};

use crate::{HubError, HubSession};

pub(crate) const DEFAULT_STREAM_QUEUE: usize = 64;

pub(crate) type Joined = Option<(RunId, u64)>;

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(PoisonError::into_inner)
}

#[derive(Clone)]
enum Terminal {
    Overflow(RunId),
    Failed(Error),
}

#[derive(Default)]
struct Mailbox {
    queue: VecDeque<Envelope>,
    stopped: bool,
    terminal: Option<Terminal>,
    waker: Option<Waker>,
}

#[derive(Default)]
pub(crate) struct Subscriber {
    mailbox: Mutex<Mailbox>,
}

impl Subscriber {
    fn offer(&self, envelope: &Envelope, capacity: usize) -> bool {
        let mut mailbox = lock(&self.mailbox);
        if mailbox.queue.len() >= capacity {
            return false;
        }
        mailbox.queue.push_back(envelope.clone());
        if let Some(waker) = mailbox.waker.take() {
            waker.wake();
        }
        true
    }

    fn stop(&self, terminal: Option<Terminal>) {
        let mut mailbox = lock(&self.mailbox);
        if mailbox.terminal.is_none() {
            mailbox.terminal = terminal;
        }
        mailbox.stopped = true;
        if let Some(waker) = mailbox.waker.take() {
            waker.wake();
        }
    }
}

#[derive(Default)]
pub(crate) struct Live {
    current: Option<RunId>,
    sequences: HashMap<RunId, u64>,
    subs: Vec<Arc<Subscriber>>,
    finished: HashSet<RunId>,
    readers: usize,
    closed: bool,
}

impl Live {
    pub(crate) fn current(&self) -> Option<RunId> {
        self.current.clone()
    }

    pub(crate) fn start(&mut self, run: &RunId, current: bool) {
        self.readers += 1;
        self.finished.remove(run);
        if current {
            self.current = Some(run.clone());
            self.sequences.remove(run);
        }
    }

    fn publish(&mut self, envelope: &Envelope, capacity: usize) {
        let run = envelope.run_id.clone().unwrap_or_default();
        let terminal = [
            EnvelopeType::RUN_COMPLETED,
            EnvelopeType::RUN_FAILED,
            EnvelopeType::RUN_CANCELLED,
        ]
        .contains(&envelope.envelope_type);
        let current_done = self
            .current
            .as_ref()
            .is_none_or(|current| self.finished.contains(current));
        if !terminal && current_done && !run.as_str().is_empty() && !self.finished.contains(&run) {
            self.current = Some(run.clone());
        }
        if let Some(sequence) = envelope.sequence {
            let seen = self.sequences.entry(run.clone()).or_default();
            *seen = (*seen).max(sequence);
        }
        self.subs.retain(|sub| {
            let kept = sub.offer(envelope, capacity);
            if !kept {
                sub.stop(Some(Terminal::Overflow(run.clone())));
            }
            kept
        });
    }

    fn detach(&mut self) -> Vec<Arc<Subscriber>> {
        std::mem::take(&mut self.subs)
    }

    fn exit(
        &mut self,
        run: &RunId,
        end: Option<Terminal>,
    ) -> (Vec<Arc<Subscriber>>, Option<Terminal>) {
        self.readers -= 1;
        self.finished.insert(run.clone());
        if self.readers > 0 {
            return (Vec::new(), None);
        }
        let end = end.filter(|_| self.current.as_ref() == Some(run));
        (self.detach(), end)
    }

    pub(crate) fn close(&mut self) -> Vec<Arc<Subscriber>> {
        self.closed = true;
        if self.readers > 0 {
            return Vec::new();
        }
        self.detach()
    }

    pub(crate) fn subscribe(&mut self) -> Option<(Arc<Subscriber>, Joined)> {
        if self.closed {
            return None;
        }
        let sub = Arc::new(Subscriber::default());
        self.subs.push(sub.clone());
        let joined = self.current.as_ref().and_then(|run| {
            let sequence = self.sequences.get(run).copied().unwrap_or(0);
            (sequence > 0).then(|| (run.clone(), sequence))
        });
        Some((sub, joined))
    }

    fn unsubscribe(&mut self, sub: &Arc<Subscriber>) {
        self.subs.retain(|held| !Arc::ptr_eq(held, sub));
    }
}

pub(crate) fn read_run(entry: Arc<HubSession>, run: RunId, mut stream: EventStream) {
    thread::spawn(move || {
        let mut end = None;
        while let Some(item) = block_on(std::future::poll_fn(|context| {
            stream.as_mut().poll_next(context)
        })) {
            match item {
                Ok(envelope) => {
                    let capacity = entry.stream_queue();
                    entry.with_live(|live| live.publish(&envelope, capacity));
                }
                Err(Error::EventStreamOverflow) => {
                    let subs = entry.with_live(Live::detach);
                    for sub in subs {
                        sub.stop(Some(Terminal::Overflow(run.clone())));
                    }
                }
                Err(err) => {
                    end = Some(Terminal::Failed(err));
                    break;
                }
            }
        }
        let (subs, end) = entry.with_live(|live| live.exit(&run, end));
        for sub in subs {
            sub.stop(end.clone());
        }
    });
}

pub(crate) fn finish(subs: Vec<Arc<Subscriber>>) {
    for sub in subs {
        sub.stop(None);
    }
}

struct Unpark(Thread);

impl Wake for Unpark {
    fn wake(self: Arc<Self>) {
        self.0.unpark();
    }
}

fn block_on<F: Future>(future: F) -> F::Output {
    let mut future = std::pin::pin!(future);
    let waker = Waker::from(Arc::new(Unpark(thread::current())));
    let mut context = Context::from_waker(&waker);
    loop {
        if let Poll::Ready(output) = future.as_mut().poll(&mut context) {
            return output;
        }
        thread::park();
    }
}

enum Source {
    Replay(EventStream),
    Live {
        sub: Arc<Subscriber>,
        session: Arc<HubSession>,
    },
}

pub struct Subscription {
    run: RunId,
    joined: Option<(RunId, u64)>,
    positions: HashMap<RunId, u64>,
    source: Source,
    done: bool,
}

impl Subscription {
    pub(crate) fn replay(run: RunId, stream: EventStream) -> Self {
        Self {
            run,
            joined: None,
            positions: HashMap::new(),
            source: Source::Replay(stream),
            done: false,
        }
    }

    pub(crate) fn live(
        session: Arc<HubSession>,
        sub: Arc<Subscriber>,
        joined: Option<(RunId, u64)>,
    ) -> Self {
        Self {
            run: RunId::default(),
            joined,
            positions: HashMap::new(),
            source: Source::Live { sub, session },
            done: false,
        }
    }

    pub fn run_id(&self) -> &RunId {
        &self.run
    }

    pub fn joined_at(&self) -> Option<(RunId, u64)> {
        self.joined.clone()
    }

    pub fn close(&mut self) {
        if let Source::Live { sub, session } = &self.source {
            session.with_live(|live| live.unsubscribe(sub));
            sub.stop(None);
        }
        self.done = true;
    }

    fn observe(&mut self, envelope: &Envelope) {
        let run = envelope.run_id.clone().unwrap_or_else(|| self.run.clone());
        if let Some(sequence) = envelope.sequence {
            let seen = self.positions.entry(run).or_default();
            *seen = (*seen).max(sequence);
        }
    }

    fn overflowed(&self, run: RunId) -> HubError {
        let last_sequence = self.positions.get(&run).copied().unwrap_or(0);
        HubError::Overflow {
            run_id: run,
            last_sequence,
        }
    }
}

impl Drop for Subscription {
    fn drop(&mut self) {
        self.close();
    }
}

enum Polled {
    Item(Box<Envelope>),
    End,
    Overflow(RunId),
    Failed(Error),
}

impl Stream for Subscription {
    type Item = Result<Envelope, HubError>;

    fn poll_next(mut self: Pin<&mut Self>, context: &mut Context<'_>) -> Poll<Option<Self::Item>> {
        if self.done {
            return Poll::Ready(None);
        }
        let replayed = self.run.clone();
        let polled = match &mut self.source {
            Source::Replay(stream) => match stream.as_mut().poll_next(context) {
                Poll::Pending => return Poll::Pending,
                Poll::Ready(None) => Polled::End,
                Poll::Ready(Some(Ok(envelope))) => Polled::Item(Box::new(envelope)),
                Poll::Ready(Some(Err(Error::EventStreamOverflow))) => Polled::Overflow(replayed),
                Poll::Ready(Some(Err(err))) => Polled::Failed(err),
            },
            Source::Live { sub, .. } => {
                let mut mailbox = lock(&sub.mailbox);
                if let Some(envelope) = mailbox.queue.pop_front() {
                    Polled::Item(Box::new(envelope))
                } else if mailbox.stopped {
                    match mailbox.terminal.take() {
                        Some(Terminal::Overflow(run)) => Polled::Overflow(run),
                        Some(Terminal::Failed(err)) => Polled::Failed(err),
                        None => Polled::End,
                    }
                } else {
                    mailbox.waker = Some(context.waker().clone());
                    return Poll::Pending;
                }
            }
        };
        let item = match polled {
            Polled::Item(envelope) => {
                self.observe(&envelope);
                return Poll::Ready(Some(Ok(*envelope)));
            }
            Polled::End => None,
            Polled::Overflow(run) => Some(Err(self.overflowed(run))),
            Polled::Failed(err) => Some(Err(HubError::Adapter(err))),
        };
        self.done = true;
        Poll::Ready(item)
    }
}
