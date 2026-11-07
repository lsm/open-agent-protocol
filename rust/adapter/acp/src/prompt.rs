use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex, Weak};
use std::task::{Context, Poll};
use std::thread::JoinHandle;
use std::time::Instant;

use futures_channel::oneshot;
use oap_adapter::jsonrpc::{Error, Fault, Inbound, IncomingRequest};
use serde_json::value::RawValue;

use crate::native::{decode, PromptParams, PromptResult, METHOD_SESSION_PROMPT};
use crate::process::{Process, DIALECT};
use crate::Operation;

pub struct PromptBridge {
    process: Arc<Process>,
    dispatcher: Mutex<Option<JoinHandle<bool>>>,
    requests: Arc<Mutex<Vec<Weak<IncomingRequest>>>>,
}

pub struct PromptCall {
    admission: oneshot::Receiver<Option<String>>,
    completion: Operation<PromptResult>,
}

fn stopped() -> Error {
    DIALECT.error(Fault::Transport("acp rpc: prompt worker stopped".into()))
}

impl PromptBridge {
    pub fn new(process: Process, mut receive: impl FnMut(Inbound) + Send + 'static) -> Self {
        let inbound = process.client.inbound();
        let done = process.client.done();
        let client = process.client.clone();
        let requests = Arc::new(Mutex::new(Vec::<Weak<IncomingRequest>>::new()));
        let received = requests.clone();
        let dispatcher = std::thread::spawn(move || {
            let result = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| loop {
                crossbeam_channel::select! {
                    recv(done) -> _ => break,
                    recv(inbound) -> message => match message {
                        Ok(Inbound::Barrier(ack)) => drop(ack),
                        Ok(message) => {
                            if let Inbound::Request(request) = &message {
                                let mut known = received.lock().unwrap_or_else(|p| p.into_inner());
                                known.retain(|request| request.strong_count() > 0);
                                known.push(Arc::downgrade(request));
                            }
                            receive(message);
                        }
                        Err(_) => break,
                    }
                }
            }));
            client.close();
            result.is_ok()
        });
        Self {
            process: Arc::new(process),
            dispatcher: Mutex::new(Some(dispatcher)),
            requests,
        }
    }

    pub(crate) fn arm(&self, control: &crate::operation::Cancellation) {
        control.arm(&self.process.child, &self.process.client);
    }

    pub(crate) fn is_closed(&self) -> bool {
        self.process.client.is_closed()
    }

    pub(crate) fn owns(&self, request: &Arc<IncomingRequest>) -> bool {
        owns(&self.requests, request)
    }

    pub fn notify(
        &self,
        method: String,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> Operation<()> {
        let process = self.process.clone();
        Operation::spawn(move |control| {
            control.arm(&process.child, &process.client);
            process.client.notify(&method, params, deadline)
        })
    }

    pub fn respond(&self, request: Arc<IncomingRequest>, result: Box<RawValue>) -> Operation<()> {
        self.reverse(request, move |request| request.respond(&result))
    }

    pub fn respond_error(
        &self,
        request: Arc<IncomingRequest>,
        code: i64,
        message: String,
        data: Option<Box<RawValue>>,
    ) -> Operation<()> {
        self.reverse(request, move |request| {
            request.respond_error_with(code, &message, data)
        })
    }

    fn reverse(
        &self,
        request: Arc<IncomingRequest>,
        send: impl FnOnce(&IncomingRequest) -> Result<(), Error> + Send + 'static,
    ) -> Operation<()> {
        let process = self.process.clone();
        let requests = self.requests.clone();
        Operation::spawn(move |control| {
            if !owns(&requests, &request) {
                return Err(DIALECT.error(Fault::InvalidMessage(
                    "reverse request belongs to another bridge".into(),
                )));
            }
            control.arm(&process.child, &process.client);
            send(&request)
        })
    }

    pub fn call(
        &self,
        method: String,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> Operation<Box<RawValue>> {
        self.process
            .clone()
            .call_shared_async(method, params, deadline)
    }

    pub fn prompt(&self, params: PromptParams, deadline: Option<Instant>) -> PromptCall {
        let (report, admission) = oneshot::channel();
        let process = self.process.clone();
        let completion = Operation::spawn(move |control| {
            control.arm(&process.child, &process.client);
            let params = serde_json::value::to_raw_value(&params)
                .map_err(|error| DIALECT.error(Fault::Encode(error.to_string())))?;
            let result = process.client.call_reported(
                METHOD_SESSION_PROMPT,
                Some(params),
                deadline,
                move |error| {
                    let _ = report.send(error);
                },
            )?;
            decode::<PromptResult>(result.get().as_bytes()).map_err(|error| {
                DIALECT.error(Fault::Decode {
                    method: METHOD_SESSION_PROMPT.into(),
                    detail: error.to_string(),
                })
            })
        });
        PromptCall {
            admission,
            completion,
        }
    }

    fn shutdown(&self) -> Result<(), Error> {
        let mut dispatcher = self
            .dispatcher
            .lock()
            .unwrap_or_else(|poison| poison.into_inner());
        let result = self
            .process
            .close()
            .map_err(|error| DIALECT.error(Fault::Transport(error)));
        let joined = match dispatcher.take().map(JoinHandle::join) {
            None | Some(Ok(true)) => Ok(()),
            _ => Err(stopped()),
        };
        result.and(joined)
    }

    pub fn close(self: Arc<Self>) -> Operation<()> {
        self.close_joining(Vec::new())
    }

    pub(crate) fn close_workers(&self, workers: Vec<JoinHandle<()>>) -> Result<(), Error> {
        let mut outcome = self.shutdown();
        for worker in workers {
            if worker.join().is_err() {
                outcome = Err(stopped());
            }
        }
        outcome
    }

    pub(crate) fn close_joining(self: Arc<Self>, workers: Vec<JoinHandle<()>>) -> Operation<()> {
        Operation::spawn(move |control| {
            control.arm(&self.process.child, &self.process.client);
            self.close_workers(workers)
        })
    }
}

impl Drop for PromptBridge {
    fn drop(&mut self) {
        let _ = self.shutdown();
    }
}

impl PromptCall {
    pub async fn admitted(&mut self) -> Result<(), Error> {
        match (&mut self.admission).await.map_err(|_| stopped())? {
            None => Ok(()),
            Some(error) => Err(DIALECT.error(Fault::Transport(error))),
        }
    }
}

impl Future for PromptCall {
    type Output = Result<PromptResult, Error>;

    fn poll(mut self: Pin<&mut Self>, context: &mut Context<'_>) -> Poll<Self::Output> {
        Pin::new(&mut self.completion).poll(context)
    }
}

fn owns(requests: &Mutex<Vec<Weak<IncomingRequest>>>, request: &Arc<IncomingRequest>) -> bool {
    requests
        .lock()
        .unwrap_or_else(|p| p.into_inner())
        .iter()
        .filter_map(Weak::upgrade)
        .any(|known| Arc::ptr_eq(&known, request))
}
