use std::collections::{HashMap, VecDeque};
use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::task::{Context, Poll, Wake, Waker};
use std::thread::{self, Thread};
use std::time::{Duration, Instant};

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
pub(crate) enum Terminal {
    Overflow(RunId),
    Failed(Error),
}

#[derive(Default)]
struct Mailbox {
    queue: VecDeque<Envelope>,
    attached: Option<RunId>,
    last_read: Option<RunId>,
    first_run: u64,
    stopped: bool,
    terminal: Option<Terminal>,
    waker: Option<Waker>,
}

#[derive(Default)]
pub(crate) struct Subscriber {
    mailbox: Mutex<Mailbox>,
}

impl Subscriber {
    fn offer(&self, envelope: &Envelope, serial: u64, capacity: usize) -> bool {
        let mut mailbox = lock(&self.mailbox);
        if serial < mailbox.first_run {
            return true;
        }
        if mailbox.queue.len() >= capacity {
            return false;
        }
        mailbox.queue.push_back(envelope.clone());
        if let Some(waker) = mailbox.waker.take() {
            waker.wake();
        }
        true
    }

    fn exposed_to(&self, run: &RunId, serial: impl Fn(&RunId) -> u64) -> bool {
        let mailbox = lock(&self.mailbox);
        let unread = mailbox
            .queue
            .iter()
            .any(|envelope| envelope.run_id.as_ref() == Some(run));
        match (&mailbox.last_read, &mailbox.attached) {
            (Some(read), _) => read == run || (unread && serial(run) > serial(read)),
            (None, Some(attached)) => {
                attached == run || (unread && serial(run) >= serial(attached))
            }
            (None, None) => unread,
        }
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
    serials: HashMap<RunId, u64>,
    ended: std::collections::HashSet<RunId>,
    next_serial: u64,
    readers: usize,
    closed: bool,
    pending_end: Option<(Vec<Arc<Subscriber>>, Terminal)>,
    reservations: usize,
    finish_due: bool,
    pub(crate) work: crate::work::WorkLog,
}

impl Live {
    pub(crate) fn current(&self) -> Option<RunId> {
        self.current.clone()
    }

    pub(crate) fn reserve(&mut self) {
        self.reservations += 1;
    }

    pub(crate) fn release(&mut self) -> Vec<(Arc<Subscriber>, Option<Terminal>)> {
        self.reservations -= 1;
        if self.finish_due && self.readers == 0 && self.reservations == 0 {
            return self.finish(None);
        }
        Vec::new()
    }

    pub(crate) fn start(&mut self, run: Option<&RunId>, current: bool) -> u64 {
        self.reservations -= 1;
        self.readers += 1;
        self.finish_due = false;
        self.next_serial += 1;
        let serial = self.next_serial;
        if let Some(run) = run {
            self.bind(run, serial, current);
        }
        serial
    }

    fn bind(&mut self, run: &RunId, serial: u64, current: bool) {
        let newer = self
            .current
            .as_ref()
            .is_none_or(|held| serial > self.serials.get(held).copied().unwrap_or(0));
        self.serials.insert(run.clone(), serial);
        self.ended.remove(run);
        if current && newer {
            self.supersede(run);
            self.sequences.remove(run);
        }
    }

    fn supersede(&mut self, run: &RunId) {
        self.current = Some(run.clone());
        if let Some((cohort, failure)) = self.pending_end.take() {
            self.subs
                .retain(|sub| !cohort.iter().any(|held| Arc::ptr_eq(held, sub)));
            for sub in cohort {
                sub.stop(Some(failure.clone()));
            }
        }
    }

    fn cut_exposed(&mut self, run: &RunId) -> Vec<Arc<Subscriber>> {
        let (cut, kept) = std::mem::take(&mut self.subs).into_iter().partition(|sub| {
            sub.exposed_to(run, |held| self.serials.get(held).copied().unwrap_or(0))
        });
        self.subs = kept;
        cut
    }

    fn publish(&mut self, envelope: &Envelope, capacity: usize) {
        self.work.observe(envelope, crate::hub::now_ms());
        let run = envelope.run_id.clone().unwrap_or_default();
        let terminal = [
            EnvelopeType::RUN_COMPLETED,
            EnvelopeType::RUN_FAILED,
            EnvelopeType::RUN_CANCELLED,
        ]
        .contains(&envelope.envelope_type);
        let serial = |run: &RunId| self.serials.get(run).copied().unwrap_or(0);
        let newer = match &self.current {
            Some(current) => current != &run && serial(&run) > serial(current),
            None => !run.as_str().is_empty(),
        };
        if !terminal && newer {
            self.supersede(&run);
        }
        if let Some(sequence) = envelope.sequence {
            let seen = self.sequences.entry(run.clone()).or_default();
            *seen = (*seen).max(sequence);
        }
        let serial = self.serials.get(&run).copied().unwrap_or(0);
        let mut dropped = Vec::new();
        self.subs.retain(|sub| {
            let kept = sub.offer(envelope, serial, capacity);
            if !kept {
                dropped.push(sub.clone());
            }
            kept
        });
        for sub in dropped {
            let resume = self.loss_run(&sub, &run);
            sub.stop(Some(Terminal::Overflow(resume)));
        }
    }

    fn loss_run(&self, sub: &Subscriber, dropped: &RunId) -> RunId {
        let serial = |run: &RunId| self.serials.get(run).copied().unwrap_or(0);
        let spent = |run: &RunId| self.current.as_ref() != Some(run) && self.ended.contains(run);
        let mailbox = lock(&sub.mailbox);
        let mut candidates: Vec<RunId> = mailbox
            .queue
            .iter()
            .filter_map(|envelope| envelope.run_id.clone())
            .collect();
        match &mailbox.last_read {
            Some(read) => candidates.push(read.clone()),
            None => candidates.extend(mailbox.attached.clone()),
        }
        candidates.extend(self.current.clone());
        let mut newest = dropped.clone();
        let mut live = !spent(dropped);
        for run in candidates {
            match (live, spent(&run)) {
                (true, true) => {}
                (false, false) => {
                    newest = run;
                    live = true;
                }
                _ if serial(&run) > serial(&newest) => newest = run,
                _ => {}
            }
        }
        newest
    }

    fn detach(&mut self) -> Vec<Arc<Subscriber>> {
        std::mem::take(&mut self.subs)
    }

    fn exit(
        &mut self,
        run: &RunId,
        end: Option<Terminal>,
    ) -> Vec<(Arc<Subscriber>, Option<Terminal>)> {
        self.readers -= 1;
        self.ended.insert(run.clone());
        let current = self.current.as_ref() == Some(run);
        let mut stopped: Vec<(Arc<Subscriber>, Option<Terminal>)> = match &end {
            Some(failure) if !current => self
                .cut_exposed(run)
                .into_iter()
                .map(|sub| (sub, Some(failure.clone())))
                .collect(),
            _ => Vec::new(),
        };
        let end = end.filter(|_| current);
        if end.is_none() && !self.closed && self.pending_end.is_none() && !self.finish_due {
            return stopped;
        }
        if self.readers > 0 || self.reservations > 0 {
            if let Some(failure) = end {
                self.pending_end = Some((self.subs.clone(), failure));
            }
            if self.readers == 0 {
                self.finish_due = true;
            }
            return stopped;
        }
        stopped.extend(self.finish(end));
        stopped
    }

    fn finish(&mut self, end: Option<Terminal>) -> Vec<(Arc<Subscriber>, Option<Terminal>)> {
        self.finish_due = false;
        let (cohort, held) = match self.pending_end.take() {
            Some((cohort, failure)) => (cohort, Some(failure)),
            None => (Vec::new(), None),
        };
        self.detach()
            .into_iter()
            .map(|sub| {
                let failed = cohort.iter().any(|member| Arc::ptr_eq(member, &sub));
                let terminal = if failed { held.clone() } else { end.clone() };
                (sub, terminal)
            })
            .collect()
    }

    pub(crate) fn close(&mut self) -> Vec<(Arc<Subscriber>, Option<Terminal>)> {
        self.closed = true;
        if self.readers > 0 {
            return Vec::new();
        }
        if self.reservations > 0 {
            self.finish_due = true;
            return Vec::new();
        }
        self.finish(None)
    }

    pub(crate) fn subscribe(&mut self) -> Option<(Arc<Subscriber>, Joined)> {
        if self.closed {
            return None;
        }
        let sub = Arc::new(Subscriber::default());
        lock(&sub.mailbox).attached = self.current.clone();
        self.subs.push(sub.clone());
        let joined = self
            .current
            .as_ref()
            .filter(|run| !self.ended.contains(*run))
            .and_then(|run| {
                let sequence = self.sequences.get(run).copied().unwrap_or(0);
                (sequence > 0).then(|| (run.clone(), sequence))
            });
        Some((sub, joined))
    }

    pub(crate) fn subscribe_from(&mut self, run: &RunId) -> Option<Arc<Subscriber>> {
        let (sub, _) = self.subscribe()?;
        lock(&sub.mailbox).first_run = self.serials.get(run).copied().unwrap_or(0);
        Some(sub)
    }

    pub(crate) fn later_runs(&self, run: &RunId) -> Vec<RunId> {
        let Some(first) = self.serials.get(run) else {
            return Vec::new();
        };
        let mut later: Vec<(u64, RunId)> = self
            .serials
            .iter()
            .filter(|(_, serial)| *serial > first)
            .map(|(run, serial)| (*serial, run.clone()))
            .collect();
        later.sort_by_key(|(serial, _)| *serial);
        later.into_iter().map(|(_, run)| run).collect()
    }

    pub(crate) fn end_subscriptions(&mut self) -> Vec<(Arc<Subscriber>, Option<Terminal>)> {
        self.detach().into_iter().map(|sub| (sub, None)).collect()
    }

    fn unsubscribe(&mut self, sub: &Arc<Subscriber>) {
        self.subs.retain(|held| !Arc::ptr_eq(held, sub));
    }
}

pub(crate) fn read_run(
    entry: Arc<HubSession>,
    mut run: Option<RunId>,
    serial: u64,
    current: bool,
    mut stream: EventStream,
) {
    thread::spawn(move || {
        let mut end = None;
        while let Some(item) = block_on(std::future::poll_fn(|context| {
            stream.as_mut().poll_next(context)
        })) {
            match item {
                Ok(envelope) => {
                    if let (None, Some(bound)) = (&run, &envelope.run_id) {
                        run = Some(bound.clone());
                        entry.with_live(|live| live.bind(bound, serial, current));
                    }
                    let capacity = entry.stream_queue();
                    entry.with_live(|live| live.publish(&envelope, capacity));
                }
                Err(Error::EventStreamOverflow) => {
                    let overflowed = run.clone().unwrap_or_default();
                    let subs = entry.with_live(|live| live.cut_exposed(&overflowed));
                    for sub in subs {
                        sub.stop(Some(Terminal::Overflow(overflowed.clone())));
                    }
                }
                Err(err) => {
                    end = Some(Terminal::Failed(err));
                    break;
                }
            }
        }
        let run = run.unwrap_or_default();
        for (sub, terminal) in entry.with_live(|live| live.exit(&run, end)) {
            sub.stop(terminal);
        }
    });
}

pub(crate) fn finish(subs: Vec<(Arc<Subscriber>, Option<Terminal>)>) {
    for (sub, terminal) in subs {
        sub.stop(terminal);
    }
}

struct Unpark(Thread);

impl Wake for Unpark {
    fn wake(self: Arc<Self>) {
        self.0.unpark();
    }
}

pub(crate) fn block_on<F: Future>(future: F) -> F::Output {
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

pub struct Subscription {
    run: RunId,
    joined: Option<(RunId, u64)>,
    positions: HashMap<RunId, u64>,
    replays: VecDeque<(RunId, EventStream)>,
    one_run: bool,
    sub: Arc<Subscriber>,
    session: Arc<HubSession>,
    done: bool,
    cancel: Arc<Cancel>,
}

#[derive(Default)]
struct Cancel {
    cancelled: std::sync::atomic::AtomicBool,
    waker: Mutex<Option<Waker>>,
}

impl Cancel {
    fn cancelled(&self, context: &Context<'_>) -> bool {
        *lock(&self.waker) = Some(context.waker().clone());
        self.cancelled.load(std::sync::atomic::Ordering::SeqCst)
    }
}

#[derive(Clone)]
pub struct Canceller(Arc<Cancel>);

impl Canceller {
    pub fn cancel(&self) {
        self.0
            .cancelled
            .store(true, std::sync::atomic::Ordering::SeqCst);
        if let Some(waker) = lock(&self.0.waker).take() {
            waker.wake();
        }
    }
}

impl Subscription {
    pub(crate) fn live(
        session: Arc<HubSession>,
        sub: Arc<Subscriber>,
        joined: Option<(RunId, u64)>,
    ) -> Self {
        Self {
            run: RunId::default(),
            joined,
            positions: HashMap::new(),
            replays: VecDeque::new(),
            one_run: false,
            sub,
            session,
            done: false,
            cancel: Arc::default(),
        }
    }

    pub(crate) fn after(
        session: Arc<HubSession>,
        sub: Arc<Subscriber>,
        run: RunId,
        after: u64,
    ) -> Self {
        let mut subscription = Self::live(session, sub, None);
        subscription.positions.insert(run.clone(), after);
        subscription.run = run;
        subscription
    }

    pub(crate) fn one_run(session: Arc<HubSession>, run: RunId, after: u64) -> Self {
        let mut subscription = Self::after(session, Arc::default(), run, after);
        subscription.one_run = true;
        subscription
    }

    pub(crate) fn replaying(&mut self, run: RunId, stream: EventStream) {
        self.replays.push_back((run, stream));
    }

    pub fn canceller(&self) -> Canceller {
        Canceller(self.cancel.clone())
    }

    pub fn run_id(&self) -> &RunId {
        &self.run
    }

    pub fn joined_at(&self) -> Option<(RunId, u64)> {
        self.joined.clone()
    }

    pub fn close(&mut self) {
        self.session.with_live(|live| live.unsubscribe(&self.sub));
        self.sub.stop(None);
        self.replays.clear();
        self.done = true;
    }

    fn delivered(&self, envelope: &Envelope) -> bool {
        let seen = envelope
            .run_id
            .as_ref()
            .and_then(|run| self.positions.get(run));
        matches!((envelope.sequence, seen), (Some(sequence), Some(seen)) if sequence <= *seen)
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

pub(crate) fn block_on_within<F: Future>(future: F, limit: Duration) -> Option<F::Output> {
    let deadline = Instant::now() + limit;
    let mut future = std::pin::pin!(future);
    let waker = Waker::from(Arc::new(Unpark(thread::current())));
    let mut context = Context::from_waker(&waker);
    loop {
        if let Poll::Ready(output) = future.as_mut().poll(&mut context) {
            return Some(output);
        }
        let left = deadline.saturating_duration_since(Instant::now());
        if left.is_zero() {
            return None;
        }
        thread::park_timeout(left);
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
        let cancel = self.cancel.clone();
        loop {
            let polled = if let Some((run, stream)) = self.replays.front_mut() {
                match stream.as_mut().poll_next(context) {
                    Poll::Pending if cancel.cancelled(context) => Polled::End,
                    Poll::Pending => {
                        let mut mailbox = lock(&self.sub.mailbox);
                        if !mailbox.stopped {
                            mailbox.waker = Some(context.waker().clone());
                            return Poll::Pending;
                        }
                        drop(mailbox);
                        self.replays.clear();
                        continue;
                    }
                    Poll::Ready(None) => {
                        self.replays.pop_front();
                        continue;
                    }
                    Poll::Ready(Some(Ok(envelope))) => Polled::Item(Box::new(envelope)),
                    Poll::Ready(Some(Err(Error::EventStreamOverflow))) => {
                        Polled::Overflow(run.clone())
                    }
                    Poll::Ready(Some(Err(err))) => Polled::Failed(err),
                }
            } else if self.one_run {
                Polled::End
            } else {
                let mut mailbox = lock(&self.sub.mailbox);
                if let Some(envelope) = mailbox.queue.pop_front() {
                    mailbox.last_read = envelope.run_id.clone();
                    Polled::Item(Box::new(envelope))
                } else if mailbox.stopped {
                    match mailbox.terminal.take() {
                        Some(Terminal::Overflow(run)) => Polled::Overflow(run),
                        Some(Terminal::Failed(err)) => Polled::Failed(err),
                        None => Polled::End,
                    }
                } else if cancel.cancelled(context) {
                    Polled::End
                } else {
                    mailbox.waker = Some(context.waker().clone());
                    return Poll::Pending;
                }
            };
            let item = match polled {
                Polled::Item(envelope) if self.delivered(&envelope) => continue,
                Polled::Item(envelope) => {
                    self.observe(&envelope);
                    return Poll::Ready(Some(Ok(*envelope)));
                }
                Polled::End => None,
                Polled::Overflow(run) => Some(Err(self.overflowed(run))),
                Polled::Failed(err) => Some(Err(HubError::Adapter(err))),
            };
            self.close();
            return Poll::Ready(item);
        }
    }
}
