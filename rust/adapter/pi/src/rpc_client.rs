use std::collections::{HashMap, HashSet};
use std::io::{BufRead, BufReader, Read, Write};
use std::sync::atomic::{AtomicBool, AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use crossbeam_channel::{after, select, Receiver, Sender, TrySendError};
use serde_json::value::RawValue;

use crate::client::Client;
use crate::native::{Command, ExtensionUiResponse, Response};
use crate::rpc::{
    decode_frame, encode, read_frame, Error, Frame, Inbound, Outbound, DEFAULT_FRAME_LIMIT,
};

const DEFAULT_QUEUE_CAPACITY: usize = 64;
const DEFAULT_STALL: Duration = Duration::from_secs(30);

pub type Closer = Box<dyn FnOnce() + Send>;

#[derive(Default)]
pub struct ClientOptions {
    pub frame_limit: usize,
    pub queue_capacity: usize,
    pub queue_stall: Option<Duration>,
    pub write_queue_capacity: usize,
    pub first_request_id: u64,
    pub closer: Option<Closer>,
}

struct WriteRequest {
    value: Outbound,
    started: Sender<()>,
    encoded: Sender<()>,
    result: Sender<Result<(), Error>>,
}

struct Pending {
    command: String,
    result: Sender<Result<Response, Error>>,
}

struct State {
    pending: HashMap<String, Pending>,
    sent: HashSet<String>,
    closed: bool,
    err: Option<Error>,
    decoding: bool,
    routing: bool,
}

struct Inner {
    state: Mutex<State>,
    next_id: AtomicU64,
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

fn other(text: impl Into<String>) -> Error {
    Error::Other(text.into())
}

fn or_default(value: usize, default: usize) -> usize {
    if value == 0 {
        default
    } else {
        value
    }
}

impl RpcClient {
    pub fn new(
        reader: impl Read + Send + 'static,
        writer: impl Write + Send + 'static,
        mut options: ClientOptions,
    ) -> Self {
        let capacity = or_default(options.queue_capacity, DEFAULT_QUEUE_CAPACITY);
        let (writes, write_rx) =
            crossbeam_channel::bounded(or_default(options.write_queue_capacity, capacity));
        let (inbound_tx, inbound_rx) = crossbeam_channel::bounded(capacity);
        let (done_tx, done_rx) = crossbeam_channel::bounded(0);
        let unblocks_writes = options.closer.is_some();
        let client = Self(Arc::new(Inner {
            state: Mutex::new(State {
                pending: HashMap::new(),
                sent: HashSet::new(),
                closed: false,
                err: None,
                decoding: false,
                routing: false,
            }),
            next_id: AtomicU64::new(options.first_request_id),
            stall: options.queue_stall.unwrap_or(DEFAULT_STALL),
            writes,
            inbound_tx,
            inbound_rx,
            done_rx,
            done_tx: Mutex::new(Some(done_tx)),
            closer: Mutex::new(options.closer.take()),
            closing: AtomicBool::new(false),
            unblocks_writes,
        }));
        let limit = or_default(options.frame_limit, DEFAULT_FRAME_LIMIT);
        let writer_client = client.clone();
        std::thread::spawn(move || writer_client.write_loop(writer, write_rx, limit));
        let reader_client = client.clone();
        std::thread::spawn(move || reader_client.read_loop(BufReader::new(reader), limit));
        client
    }

    fn is_closed(&self) -> bool {
        lock(&self.0.state).closed
    }

    fn close_error(&self) -> Error {
        lock(&self.0.state).err.clone().unwrap_or(Error::Closed)
    }

    pub(crate) fn close_with(&self, reason: Error) {
        if self.0.closing.swap(true, Ordering::SeqCst) {
            return;
        }
        if let Some(closer) = lock(&self.0.closer).take() {
            closer();
        }
        let pending = {
            let mut state = lock(&self.0.state);
            state.closed = true;
            state.err.get_or_insert(reason);
            if state.routing || (state.decoding && self.0.unblocks_writes) {
                HashMap::new()
            } else {
                std::mem::take(&mut state.pending)
            }
        };
        lock(&self.0.done_tx).take();
        self.fail_pending(pending);
    }

    fn fail_pending(&self, pending: HashMap<String, Pending>) {
        let error = self.close_error();
        for (_, call) in pending {
            let _ = call.result.send(Err(error.clone()));
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

    fn write(&self, value: Outbound) -> Result<(), Error> {
        if self.is_closed() {
            return Err(self.close_error());
        }
        let (started, began) = crossbeam_channel::bounded(1);
        let (encoded, settled) = crossbeam_channel::bounded(1);
        let (result, written) = crossbeam_channel::bounded(1);
        match self.0.writes.try_send(WriteRequest {
            value,
            started,
            encoded,
            result,
        }) {
            Ok(()) => {}
            Err(TrySendError::Full(_)) => return Err(other("pi rpc: write queue is full")),
            Err(TrySendError::Disconnected(_)) => return Err(self.close_error()),
        }
        select! {
            recv(written) -> outcome => outcome.unwrap_or_else(|_| Err(self.close_error())),
            recv(self.0.done_rx) -> _ => {
                let settles = settled.try_recv().is_ok() || self.0.unblocks_writes;
                if began.try_recv().is_ok() && settles {
                    return written.recv().unwrap_or_else(|_| Err(self.close_error()));
                }
                Err(self.close_error())
            }
        }
    }

    fn write_loop(&self, mut writer: impl Write, requests: Receiver<WriteRequest>, limit: usize) {
        loop {
            select! {
                recv(requests) -> request => {
                    let Ok(request) = request else { return };
                    let _ = request.started.send(());
                    let outcome = encode(&request.value, limit).and_then(|data| {
                        writer
                            .write_all(&data)
                            .and_then(|()| writer.flush())
                            .map_err(|failure| other(failure.to_string()))
                    });
                    let _ = request.encoded.send(());
                    let failed = outcome.is_err();
                    if let Err(reason) = &outcome {
                        self.close_with(reason.clone());
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

    fn read_loop(&self, mut reader: impl BufRead, limit: usize) {
        loop {
            lock(&self.0.state).decoding = true;
            let decoded = read_frame(&mut reader, limit).and_then(|data| decode_frame(&data));
            {
                let mut state = lock(&self.0.state);
                state.decoding = false;
                state.routing = decoded.is_ok();
            }
            let routed = decoded.and_then(|frame| self.route(frame));
            if let Err(reason) = &routed {
                self.close_with(reason.clone());
            }
            self.settle_reader();
            if routed.is_err() {
                return;
            }
        }
    }

    fn route(&self, frame: Frame) -> Result<(), Error> {
        let response = match frame {
            Frame::Event(event) => return self.enqueue(Inbound::Event(event)),
            Frame::ExtensionRequest(request) => {
                return self.enqueue(Inbound::ExtensionRequest(request))
            }
            Frame::Response(response) => *response,
        };
        if response.id.is_empty() {
            return Err(other("pi rpc: response id is not pending: empty"));
        }
        let pending = lock(&self.0.state).pending.remove(&response.id);
        let Some(pending) = pending else {
            if self.is_closed() {
                return Err(self.close_error());
            }
            return Err(other(format!(
                "pi rpc: response id is not pending: {}",
                response.id
            )));
        };
        if pending.command != response.command {
            let error = other(format!(
                "pi rpc: response command does not match request: got {} want {}",
                response.command, pending.command
            ));
            let _ = pending.result.send(Err(error.clone()));
            return Err(error);
        }
        let (ack, acked) = crossbeam_channel::bounded::<()>(0);
        if let Err(error) = self.enqueue(Inbound::Barrier(ack)) {
            let _ = pending.result.send(Err(error.clone()));
            return Err(error);
        }
        let closing = select! {
            recv(acked) -> _ => false,
            recv(self.0.done_rx) -> _ => true,
        };
        let _ = pending.result.send(Ok(response));
        if closing {
            return Err(self.close_error());
        }
        Ok(())
    }

    fn enqueue(&self, message: Inbound) -> Result<(), Error> {
        let message = match self.0.inbound_tx.try_send(message) {
            Ok(()) => return Ok(()),
            Err(TrySendError::Full(message)) => message,
            Err(TrySendError::Disconnected(_)) => return Err(self.close_error()),
        };
        select! {
            send(self.0.inbound_tx, message) -> sent => sent.map_err(|_| self.close_error()),
            recv(self.0.done_rx) -> _ => Err(self.close_error()),
            recv(after(self.0.stall)) -> _ => Err(other("pi rpc: inbound queue is full")),
        }
    }
}

impl Client for RpcClient {
    fn call(&self, command: &Command, want: bool) -> Result<Option<Box<RawValue>>, Error> {
        let mut command = command.clone();
        if command.id.is_empty() {
            command.id = format!("req_{}", self.0.next_id.fetch_add(1, Ordering::SeqCst) + 1);
        }
        command
            .validate()
            .map_err(|invalid| other(invalid.to_string()))?;
        let (result, response) = crossbeam_channel::bounded(1);
        {
            let mut state = lock(&self.0.state);
            if state.closed {
                drop(state);
                return Err(self.close_error());
            }
            if !state.sent.insert(command.id.clone()) {
                return Err(other(format!(
                    "pi rpc: request id is already active: {}",
                    command.id
                )));
            }
            state.pending.insert(
                command.id.clone(),
                Pending {
                    command: command.kind.clone(),
                    result,
                },
            );
        }
        let id = command.id.clone();
        if let Err(error) = self.write(Outbound::Command(Box::new(command))) {
            if self.is_closed() {
                if let Ok(outcome) = response.recv() {
                    return settle(outcome, want);
                }
            }
            lock(&self.0.state).pending.remove(&id);
            return Err(error);
        }
        let outcome = response.recv().unwrap_or_else(|_| Err(self.close_error()));
        settle(outcome, want)
    }

    fn respond(&self, response: &ExtensionUiResponse) -> Result<(), Error> {
        self.write(Outbound::Response(response.clone()))
    }

    fn inbound(&self) -> Receiver<Inbound> {
        self.0.inbound_rx.clone()
    }

    fn done(&self) -> Receiver<()> {
        self.0.done_rx.clone()
    }

    fn err(&self) -> Option<String> {
        lock(&self.0.state).err.as_ref().map(ToString::to_string)
    }

    fn close(&self) -> Result<(), Error> {
        self.close_with(Error::Closed);
        Ok(())
    }
}

fn settle(outcome: Result<Response, Error>, want: bool) -> Result<Option<Box<RawValue>>, Error> {
    let response = outcome?;
    if !response.success {
        return Err(Error::Remote {
            id: response.id,
            command: response.command,
            message: response.error,
        });
    }
    Ok(response.data.filter(|_| want))
}
