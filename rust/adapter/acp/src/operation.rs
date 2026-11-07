use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::thread::JoinHandle;
use std::time::Instant;

use futures_channel::oneshot;
use oap_adapter::jsonrpc::{Error, Fault, RpcClient};
use oap_adapter::process::Child;
use serde_json::value::RawValue;

use crate::process::{Process, ProcessConfig, DIALECT};

#[cfg(test)]
pub(crate) static SPAWNS: std::sync::atomic::AtomicUsize = std::sync::atomic::AtomicUsize::new(0);

#[derive(Default)]
struct CancelState {
    cancelled: bool,
    target: Option<(Child, RpcClient)>,
}

#[derive(Default)]
pub(crate) struct Cancellation(Mutex<CancelState>);

impl Cancellation {
    pub(crate) fn arm(&self, child: &Child, client: &RpcClient) {
        let cancelled = {
            let mut state = self.0.lock().unwrap_or_else(|poison| poison.into_inner());
            state.target = Some((child.clone(), client.clone()));
            state.cancelled
        };
        if cancelled {
            self.stop();
        }
    }

    fn stop(&self) {
        let target = {
            let mut state = self.0.lock().unwrap_or_else(|poison| poison.into_inner());
            state.cancelled = true;
            state.target.clone()
        };
        if let Some((child, client)) = target {
            child.kill();
            client.close();
            let _ = child.exited().recv();
        }
    }
}

pub struct Operation<T> {
    response: oneshot::Receiver<Result<T, Error>>,
    worker: Option<JoinHandle<()>>,
    cancellation: Arc<Cancellation>,
}

fn worker_error() -> Error {
    DIALECT.error(Fault::Transport("acp rpc: operation worker stopped".into()))
}

impl<T: Send + 'static> Operation<T> {
    pub(crate) fn spawn(
        work: impl FnOnce(&Cancellation) -> Result<T, Error> + Send + 'static,
    ) -> Self {
        #[cfg(test)]
        SPAWNS.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
        let (reply, response) = oneshot::channel();
        let cancellation = Arc::new(Cancellation::default());
        let control = cancellation.clone();
        let worker = std::thread::spawn(move || {
            let outcome = work(&control);
            let _ = reply.send(outcome);
        });
        Self {
            response,
            worker: Some(worker),
            cancellation,
        }
    }
}

impl<T> Future for Operation<T> {
    type Output = Result<T, Error>;

    fn poll(mut self: Pin<&mut Self>, context: &mut Context<'_>) -> Poll<Self::Output> {
        match Pin::new(&mut self.response).poll(context) {
            Poll::Pending => Poll::Pending,
            Poll::Ready(outcome) => {
                let joined = self
                    .worker
                    .take()
                    .expect("operation already completed")
                    .join();
                Poll::Ready(match joined {
                    Ok(()) => outcome.unwrap_or_else(|_| Err(worker_error())),
                    Err(_) => Err(worker_error()),
                })
            }
        }
    }
}

impl<T> Drop for Operation<T> {
    fn drop(&mut self) {
        if let Some(worker) = self.worker.take() {
            self.cancellation.stop();
            let _ = worker.join();
        }
    }
}

impl Process {
    pub fn call_draining_async(
        self,
        method: String,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> Operation<(Self, Box<RawValue>)> {
        Operation::spawn(move |control| {
            control.arm(&self.child, &self.client);
            let inbound = self.client.inbound();
            let result = std::thread::scope(|scope| {
                let (send, response) = crossbeam_channel::bounded(1);
                let client = &self.client;
                let child = &self.child;
                scope.spawn(move || {
                    let result = client.call(&method, params, deadline);
                    if result.is_err() {
                        child.kill();
                        client.close();
                    }
                    let _ = send.send(result);
                });
                let result = loop {
                    crossbeam_channel::select! {
                        recv(response) -> result => break result.unwrap_or_else(|_| Err(worker_error())),
                        recv(inbound) -> message => match message {
                            Ok(oap_adapter::jsonrpc::Inbound::Request(request)) => {
                                if let Err(error) = request.respond_error(
                                    -32601,
                                    "method not supported during session reload",
                                ) {
                                    break Err(error);
                                }
                            }
                            Ok(oap_adapter::jsonrpc::Inbound::Barrier(ack)) => drop(ack),
                            Ok(oap_adapter::jsonrpc::Inbound::Notification(_)) => (),
                            Err(_) => break Err(DIALECT.error(Fault::Closed)),
                        }
                    }
                };
                if result.is_err() {
                    self.child.kill();
                    self.client.close();
                    if let Ok(Err(error)) = response.recv() {
                        if matches!(&error.fault, Fault::DeadlineExceeded) {
                            return Err(error);
                        }
                    }
                }
                result
            })?;
            Ok((self, result))
        })
    }

    pub fn call_shared_async(
        self: Arc<Self>,
        method: String,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> Operation<Box<RawValue>> {
        Operation::spawn(move |control| {
            control.arm(&self.child, &self.client);
            self.client.call(&method, params, deadline)
        })
    }

    pub fn close_shared_async(self: Arc<Self>) -> Operation<()> {
        Operation::spawn(move |control| {
            control.arm(&self.child, &self.client);
            self.close()
                .map_err(|reason| DIALECT.error(Fault::Transport(reason)))
        })
    }

    pub fn close_async(self) -> Operation<()> {
        Operation::spawn(move |control| {
            control.arm(&self.child, &self.client);
            self.close()
                .map_err(|reason| DIALECT.error(Fault::Transport(reason)))
        })
    }

    pub fn start_async(config: ProcessConfig, deadline: Option<Instant>) -> Operation<Self> {
        Operation::spawn(move |control| Self::start_controlled(&config, deadline, Some(control)))
    }

    pub fn call_async(
        self,
        method: String,
        params: Option<Box<RawValue>>,
        deadline: Option<Instant>,
    ) -> Operation<(Self, Box<RawValue>)> {
        Operation::spawn(move |control| {
            control.arm(&self.child, &self.client);
            let result = self.client.call(&method, params, deadline)?;
            Ok((self, result))
        })
    }
}
