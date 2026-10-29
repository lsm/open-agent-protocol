use std::collections::HashMap;
use std::io::{BufReader, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicI64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::{Duration, Instant};

use crossbeam_channel::{after, never, select, Receiver, Sender, TrySendError};
use serde_json::value::RawValue;

use super::{
    Decoder, Dialect, Encoder, Error, Fault, Inbound, IncomingRequest, Kind, Message,
    NotificationMessage, RequestId, Responder,
};

pub const DEFAULT_QUEUE_CAPACITY: usize = 64;
pub const DEFAULT_STALL: Duration = Duration::from_secs(30);

pub type Closer = Box<dyn FnOnce() + Send>;

pub type Started = Sender<Option<String>>;

pub type NotificationCheck = fn(&str, Option<&RawValue>) -> Result<(), String>;

#[derive(Default)]
pub struct ClientOptions {
    pub frame_limit: usize,
    pub queue_capacity: usize,
    pub queue_stall: Option<Duration>,
    pub write_queue_capacity: usize,
    pub first_request_id: i64,
    pub strict_response_ids: bool,
    pub closer: Option<Closer>,
    pub check_notification: Option<NotificationCheck>,
}

type CallResult = Result<Box<RawValue>, Error>;

struct WriteRequest {
    message: Message,
    deadline: Option<Instant>,
    started: Sender<()>,
    encoded: Sender<()>,
    result: Sender<Result<(), Error>>,
}

struct State {
    pending: HashMap<RequestId, Sender<CallResult>>,
    incoming: HashMap<RequestId, ()>,
    closed: bool,
    err: Option<String>,
    activated: bool,
    decoding: bool,
    routing: bool,
}

struct Inner {
    dialect: Dialect,
    check: Option<NotificationCheck>,
    state: Mutex<State>,
    next_id: AtomicI64,
    strict: bool,
    stall: Duration,
    writes: Sender<WriteRequest>,
    inbound_tx: Sender<Inbound>,
    inbound_rx: Receiver<Inbound>,
    done_rx: Receiver<()>,
    done_tx: Mutex<Option<Sender<()>>>,
    closer: Mutex<Option<Closer>>,
    closing: AtomicBool,
    unblocks_writes: bool,
}

#[derive(Clone)]
pub struct RpcClient(Arc<Inner>);

fn lock<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

fn deadline_channel(deadline: Option<Instant>) -> Receiver<Instant> {
    match deadline {
        Some(at) => after(at.saturating_duration_since(Instant::now())),
        None => never(),
    }
}

struct ClientResponder(RpcClient);

impl Responder for ClientResponder {
    fn send(&self, message: Message) -> Result<(), Error> {
        let id = message.id.clone();
        self.0.write(message, None)?;
        if let Some(id) = id {
            lock(&self.0 .0.state).incoming.remove(&id);
        }
        Ok(())
    }
}

impl RpcClient {
    pub fn new(
        dialect: Dialect,
        reader: impl Read + Send + 'static,
        writer: impl Write + Send + 'static,
        mut options: ClientOptions,
    ) -> Self {
        let capacity = if options.queue_capacity == 0 {
            DEFAULT_QUEUE_CAPACITY
        } else {
            options.queue_capacity
        };
        let write_capacity = if options.write_queue_capacity == 0 {
            capacity
        } else {
            options.write_queue_capacity
        };
        let unblocks_writes = options.closer.is_some();
        let (writes, write_rx) = crossbeam_channel::bounded(write_capacity);
        let (inbound_tx, inbound_rx) = crossbeam_channel::bounded(capacity);
        let (done_tx, done_rx) = crossbeam_channel::bounded(0);
        let client = Self(Arc::new(Inner {
            dialect,
            check: options.check_notification,
            state: Mutex::new(State {
                pending: HashMap::new(),
                incoming: HashMap::new(),
                closed: false,
                err: None,
                activated: false,
                decoding: false,
                routing: false,
            }),
            next_id: AtomicI64::new(options.first_request_id),
            strict: options.strict_response_ids,
            stall: options
                .queue_stall
                .filter(|stall| !stall.is_zero())
                .unwrap_or(DEFAULT_STALL),
            writes,
            inbound_tx,
            inbound_rx,
            done_rx,
            done_tx: Mutex::new(Some(done_tx)),
            closer: Mutex::new(options.closer.take()),
            closing: AtomicBool::new(false),
            unblocks_writes,
        }));
        let limit = options.frame_limit;
        let writer_client = client.clone();
        std::thread::spawn(move || {
            writer_client.write_loop(Encoder::new(dialect, writer, limit), write_rx)
        });
        let reader_client = client.clone();
        std::thread::spawn(move || {
            reader_client.read_loop(Decoder::new(dialect, BufReader::new(reader), limit))
        });
        client
    }

    pub fn inbound(&self) -> Receiver<Inbound> {
        lock(&self.0.state).activated = true;
        self.0.inbound_rx.clone()
    }

    pub fn done(&self) -> Receiver<()> {
        self.0.done_rx.clone()
    }

    pub fn err(&self) -> Option<String> {
        lock(&self.0.state).err.clone()
    }

    pub fn is_closed(&self) -> bool {
        lock(&self.0.state).closed
    }

    fn fail(&self, fault: Fault) -> Error {
        self.0.dialect.error(fault)
    }

    fn text(&self, detail: &str) -> String {
        format!("{} rpc: {detail}", self.0.dialect.name)
    }

    fn close_error(&self) -> Error {
        let closed = self.fail(Fault::Closed).to_string();
        match self.err() {
            Some(reason) if reason != closed => self.fail(Fault::Transport(reason)),
            _ => self.fail(Fault::Closed),
        }
    }

    pub fn call(
        &self,
        method: &str,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> CallResult {
        let id = RequestId::Integer(
            self.0
                .next_id
                .fetch_add(1, Ordering::SeqCst)
                .wrapping_add(1),
        );
        self.call_id(id, method, params, deadline, None)
    }

    pub fn call_id(
        &self,
        id: RequestId,
        method: &str,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
        started: Option<Started>,
    ) -> CallResult {
        let mut started = started;
        let mut report = |error: Option<String>| {
            if let Some(started) = started.take() {
                let _ = started.send(error);
            }
        };
        if method.is_empty() {
            let error = self.fail(Fault::InvalidMessage(String::new()));
            report(Some(error.to_string()));
            return Err(error);
        }
        let (respond, response) = crossbeam_channel::bounded(1);
        {
            let mut state = lock(&self.0.state);
            if state.closed {
                drop(state);
                let error = self.close_error();
                report(Some(error.to_string()));
                return Err(error);
            }
            if state.pending.contains_key(&id) {
                drop(state);
                let error = self.fail(Fault::Transport(
                    self.text(&format!("request id is already active: {id}")),
                ));
                report(Some(error.to_string()));
                return Err(error);
            }
            state.pending.insert(id.clone(), respond);
        }
        if let Err(error) = self.write(Message::request(id.clone(), method, params), deadline) {
            if self.is_closed() {
                let parked = select! {
                    recv(response) -> outcome => outcome.ok(),
                    recv(deadline_channel(deadline)) -> _ => None,
                };
                if let Some(outcome) = parked {
                    if outcome
                        .as_ref()
                        .map_or_else(|e| e.remote_code().is_some(), |_| true)
                    {
                        report(None);
                        return outcome;
                    }
                }
            }
            lock(&self.0.state).pending.remove(&id);
            report(Some(error.to_string()));
            return Err(error);
        }
        report(None);
        select! {
            recv(response) -> outcome => outcome.unwrap_or_else(|_| Err(self.close_error())),
            recv(deadline_channel(deadline)) -> _ => {
                lock(&self.0.state).pending.remove(&id);
                let error = self.fail(Fault::DeadlineExceeded);
                self.close_with(error.to_string());
                Err(error)
            }
        }
    }

    pub fn call_started(
        &self,
        method: &str,
        params: Option<Box<RawValue>>,
        started: Started,
    ) -> CallResult {
        let id = RequestId::Integer(
            self.0
                .next_id
                .fetch_add(1, Ordering::SeqCst)
                .wrapping_add(1),
        );
        self.call_id(id, method, params, None, Some(started))
    }

    pub fn notify(
        &self,
        method: &str,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> Result<(), Error> {
        if method.is_empty() {
            return Err(self.fail(Fault::InvalidMessage(String::new())));
        }
        self.write(Message::notification(method, params), deadline)
    }

    pub fn close(&self) {
        self.close_with(self.fail(Fault::Closed).to_string());
    }

    fn close_with(&self, reason: String) {
        if self.0.closing.swap(true, Ordering::SeqCst) {
            return;
        }
        if let Some(closer) = lock(&self.0.closer).take() {
            closer();
        }
        let pending = {
            let mut state = lock(&self.0.state);
            state.closed = true;
            state.err.get_or_insert(reason.clone());
            let defer = state.routing || (state.decoding && self.0.unblocks_writes);
            if defer {
                HashMap::new()
            } else {
                std::mem::take(&mut state.pending)
            }
        };
        lock(&self.0.done_tx).take();
        self.fail_pending(pending);
    }

    fn fail_pending(&self, pending: HashMap<RequestId, Sender<CallResult>>) {
        let error = self.close_error();
        for (_, respond) in pending {
            let _ = respond.send(Err(clone_error(&error)));
        }
    }

    fn settle_reader(&self) {
        let pending = {
            let mut state = lock(&self.0.state);
            state.routing = false;
            if state.closed {
                std::mem::take(&mut state.pending)
            } else {
                HashMap::new()
            }
        };
        self.fail_pending(pending);
    }

    fn write(&self, message: Message, deadline: Option<Instant>) -> Result<(), Error> {
        if deadline.is_some_and(|at| Instant::now() >= at) {
            return Err(self.fail(Fault::DeadlineExceeded));
        }
        let (started, began) = crossbeam_channel::bounded(1);
        let (encoded, settled) = crossbeam_channel::bounded(1);
        let (result, written) = crossbeam_channel::bounded(1);
        if self.is_closed() {
            return Err(self.close_error());
        }
        match self.0.writes.try_send(WriteRequest {
            message,
            deadline,
            started,
            encoded,
            result,
        }) {
            Ok(()) => {}
            Err(TrySendError::Full(_)) => {
                return Err(self.fail(Fault::Transport(self.text("write queue is full"))))
            }
            Err(TrySendError::Disconnected(_)) => return Err(self.close_error()),
        }
        select! {
            recv(written) -> outcome => outcome.unwrap_or_else(|_| Err(self.close_error())),
            recv(deadline_channel(deadline)) -> _ => {
                let error = self.fail(Fault::DeadlineExceeded);
                self.close_with(error.to_string());
                Err(error)
            }
            recv(self.0.done_rx) -> _ => {
                let settles = settled.try_recv().is_ok() || self.0.unblocks_writes;
                if began.try_recv().is_ok() && settles {
                    return written.recv().unwrap_or_else(|_| Err(self.close_error()));
                }
                Err(self.close_error())
            }
        }
    }

    fn write_loop<W: Write>(&self, mut encoder: Encoder<W>, requests: Receiver<WriteRequest>) {
        loop {
            select! {
                recv(requests) -> request => {
                    let Ok(request) = request else { return };
                    if self.is_closed() {
                        let _ = request.result.send(Err(self.close_error()));
                        return;
                    }
                    if request.deadline.is_some_and(|at| Instant::now() >= at) {
                        let _ = request.result.send(Err(self.fail(Fault::DeadlineExceeded)));
                        continue;
                    }
                    let _ = request.started.send(());
                    let outcome = encoder.encode(&request.message);
                    let _ = request.encoded.send(());
                    let failed = outcome.is_err();
                    if let Err(reason) = &outcome {
                        self.close_with(reason.to_string());
                    }
                    let _ = request.result.send(outcome);
                    if failed {
                        return;
                    }
                }
                recv(self.0.done_rx) -> _ => return,
            }
        }
    }

    fn read_loop<R: std::io::BufRead>(&self, mut decoder: Decoder<R>) {
        loop {
            lock(&self.0.state).decoding = true;
            let decoded = decoder.decode();
            {
                let mut state = lock(&self.0.state);
                state.decoding = false;
                state.routing = decoded.is_ok();
            }
            match decoded {
                Ok(message) => {
                    let stop = self.route(message);
                    self.settle_reader();
                    if stop {
                        return;
                    }
                }
                Err(Error {
                    fault: Fault::Io(error),
                    ..
                }) if error.kind() == std::io::ErrorKind::UnexpectedEof => {
                    self.close_with("EOF".into());
                    self.settle_reader();
                    return;
                }
                Err(error) => {
                    self.close_with(error.to_string());
                    self.settle_reader();
                    return;
                }
            }
        }
    }

    fn route(&self, message: Message) -> bool {
        match message.kind {
            Kind::Response | Kind::Error => {
                if lock(&self.0.state).activated && !self.barrier() {
                    return true;
                }
                let id = message.id.clone().unwrap_or(RequestId::Integer(0));
                let outcome = match message.error {
                    Some(object) => Err(self.fail(Fault::Remote {
                        id: id.clone(),
                        object,
                    })),
                    None => Ok(message
                        .result
                        .unwrap_or_else(|| RawValue::from_string("null".into()).expect("null"))),
                };
                let respond = lock(&self.0.state).pending.remove(&id);
                match respond {
                    Some(respond) => {
                        let _ = respond.send(outcome);
                        false
                    }
                    None => self.unmatched(&id),
                }
            }
            Kind::Request => {
                let id = message.id.clone().unwrap_or(RequestId::Integer(0));
                let duplicate = lock(&self.0.state)
                    .incoming
                    .insert(id.clone(), ())
                    .is_some();
                if duplicate {
                    self.close_with(self.text(&format!("request id is already active: {id}")));
                    return true;
                }
                let request = IncomingRequest::new(
                    self.0.dialect,
                    id,
                    message.method,
                    message.params,
                    Arc::new(ClientResponder(self.clone())),
                );
                !self.enqueue(
                    Inbound::Request(Arc::new(request)),
                    &self.text("reverse request queue is full"),
                )
            }
            Kind::Notification => {
                if let Some(check) = self.0.check {
                    if let Err(reason) = check(&message.method, message.params.as_deref()) {
                        self.close_with(reason);
                        return true;
                    }
                }
                !self.enqueue(
                    Inbound::Notification(NotificationMessage {
                        method: message.method,
                        params: message.params,
                    }),
                    &self.text("notification queue is full"),
                )
            }
        }
    }

    fn enqueue(&self, message: Inbound, overflow: &str) -> bool {
        let activated = lock(&self.0.state).activated;
        let message = match self.0.inbound_tx.try_send(message) {
            Ok(()) => return true,
            Err(TrySendError::Full(message)) if activated => message,
            Err(_) => {
                self.close_with(overflow.into());
                return false;
            }
        };
        select! {
            send(self.0.inbound_tx, message) -> sent => sent.is_ok(),
            recv(self.0.done_rx) -> _ => false,
            recv(after(self.0.stall)) -> _ => {
                self.close_with(overflow.into());
                false
            }
        }
    }

    fn barrier(&self) -> bool {
        let (ack, acked) = crossbeam_channel::bounded::<()>(0);
        if !self.enqueue(
            Inbound::Barrier(ack),
            &self.text("notification queue is full"),
        ) {
            return false;
        }
        select! {
            recv(acked) -> _ => {}
            recv(self.0.done_rx) -> _ => {}
        }
        true
    }

    fn unmatched(&self, id: &RequestId) -> bool {
        if self.is_closed() {
            return true;
        }
        if self.0.strict {
            self.close_with(self.text(&format!("response id is not pending: {id}")));
            return true;
        }
        false
    }
}

fn clone_error(error: &Error) -> Error {
    let fault = match &error.fault {
        Fault::Closed => Fault::Closed,
        _ => Fault::Transport(error.to_string()),
    };
    Error {
        dialect: error.dialect,
        fault,
    }
}
