use std::sync::{Arc, Mutex, Weak};

use futures_util::lock::Mutex as AsyncMutex;
use oap_adapter::jsonrpc::{IncomingRequest, NotificationMessage};
use oap_adapter::{BoxFuture, Error, InteractionResolution};
use oap_protocol::{InteractionId, ParticipantId, RunCancelResponse, RunStatus};

use crate::native::{
    decode, CancelParams, PermissionOutcome, PermissionRequest, PermissionResponse, PromptResult,
    METHOD_SESSION_CANCEL, METHOD_SESSION_REQUEST_PERMISSION,
};
use crate::{Operation, PromptBridge, TextRun};

struct Binding {
    id: InteractionId,
    run: Weak<Mutex<TextRun>>,
    request: Option<Arc<IncomingRequest>>,
    source: Weak<IncomingRequest>,
}

pub struct Permissions {
    bridge: Arc<PromptBridge>,
    participant: ParticipantId,
    clock: Box<dyn Fn() -> i64 + Send + Sync>,
    bindings: AsyncMutex<Vec<Binding>>,
}

impl Permissions {
    pub fn new(
        bridge: Arc<PromptBridge>,
        participant: ParticipantId,
        clock: impl Fn() -> i64 + Send + Sync + 'static,
    ) -> Result<Arc<Self>, Error> {
        if participant.as_str().is_empty() {
            return Err(Error::InvalidParticipant);
        }
        Ok(Arc::new(Self {
            bridge,
            participant,
            clock: Box::new(clock),
            bindings: AsyncMutex::new(Vec::new()),
        }))
    }

    fn operation<T: Send + 'static>(
        self: Arc<Self>,
        work: impl FnOnce(&Self, &mut Vec<Binding>) -> Result<T, Error> + Send + 'static,
    ) -> BoxFuture<'static, T> {
        let operation = Operation::spawn(move |control| {
            self.bridge.arm(control);
            let mut bindings = futures::executor::block_on(self.bindings.lock());
            let outcome = work(&self, &mut bindings);
            bindings.retain(|binding| binding.source.strong_count() > 0);
            Ok(outcome)
        });
        Box::pin(async move { operation.await.map_err(Error::harness)? })
    }

    pub fn request(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        request: Arc<IncomingRequest>,
    ) -> BoxFuture<'static, InteractionId> {
        self.operation(move |owner, bindings| {
            if !owner.bridge.owns(&request) {
                return Err(Error::InvalidResolution);
            }
            if owner.bridge.is_closed() {
                return Err(Error::SessionClosed);
            }
            if bindings
                .iter()
                .any(|binding| Weak::ptr_eq(&binding.source, &Arc::downgrade(&request)))
            {
                return Err(Error::InvalidResolution);
            }
            if request.method != METHOD_SESSION_REQUEST_PERMISSION {
                request
                    .respond_error(-32601, "method not supported")
                    .map_err(Error::harness)?;
                return Err(Error::InvalidResolution);
            }
            let params = decode::<PermissionRequest>(
                request
                    .params
                    .as_ref()
                    .map_or(b"null".as_slice(), |raw| raw.get().as_bytes()),
            );
            let projected = {
                let mut text = run.lock().unwrap();
                match params {
                    Ok(params) => {
                        text.permission_request(params, owner.participant.clone(), (owner.clock)())
                    }
                    Err(_) => {
                        let _ = text.fail(
                            "acp_invalid_permission",
                            "malformed permission request",
                            false,
                            (owner.clock)(),
                        );
                        Err(Error::Harness("invalid permission request".into()))
                    }
                }
            };
            match projected {
                Ok(id) => {
                    bindings.push(Binding {
                        id: id.clone(),
                        run: Arc::downgrade(&run),
                        source: Arc::downgrade(&request),
                        request: Some(request),
                    });
                    Ok(id)
                }
                Err(Error::InteractionResolved) => {
                    request
                        .respond(&PermissionResponse {
                            outcome: PermissionOutcome {
                                outcome: "cancelled".into(),
                                ..PermissionOutcome::default()
                            },
                        })
                        .map_err(Error::harness)?;
                    Err(Error::InteractionResolved)
                }
                Err(error) => {
                    let _ = request.respond_error(-32602, &error.to_string());
                    owner.settle(&run, bindings)?;
                    Err(error)
                }
            }
        })
    }

    pub fn resolve(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        resolution: InteractionResolution,
    ) -> BoxFuture<'static, ()> {
        self.operation(move |owner, bindings| {
            if owner.bridge.is_closed() {
                return Err(Error::SessionClosed);
            }
            let reply = run.lock().unwrap().permission_reply(&resolution)?;
            let binding = bound(bindings, &run, reply.interaction_id())?;
            let request = binding.request.as_ref().ok_or(Error::InteractionResolved)?;
            if let Err(error) = request.respond(reply.response()) {
                run.lock().unwrap().fail(
                    "acp_permission_response_failed",
                    &error.to_string(),
                    true,
                    (owner.clock)(),
                )?;
                owner.settle(&run, bindings)?;
                return Err(Error::harness(error));
            }
            binding.request = None;
            run.lock()
                .unwrap()
                .commit_permission(reply, (owner.clock)())?;
            Ok(())
        })
    }

    pub fn observe(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        notification: NotificationMessage,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, false, move |text, now| text.observe(notification, now))
    }

    pub fn complete(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        result: PromptResult,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, true, move |text, now| text.complete(result, now))
    }

    pub fn prompt_error(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        message: String,
        inferred: bool,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, true, move |text, now| {
            text.prompt_error(&message, inferred, now)
        })
    }

    pub(crate) fn prompt_failure(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        error: oap_adapter::jsonrpc::Error,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, true, move |text, now| {
            if error.remote_code() == Some(-32800) && text.state().status == RunStatus::CANCELLING {
                text.settle_usage(None);
                text.cancelled("ACP prompt cancellation confirmed", now)
            } else {
                text.prompt_error(&error.to_string(), error.remote_code().is_none(), now)
            }
        })
    }

    pub fn cancel(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
    ) -> BoxFuture<'static, RunCancelResponse> {
        let state = run.lock().unwrap().cancel_state();
        if !matches!(&state, Ok(response) if response.status == RunStatus::RUNNING) {
            return Box::pin(async move {
                let _owner = self;
                state
            });
        }
        self.operation(move |owner, bindings| {
            let response = run.lock().unwrap().cancel_state()?;
            if response.status != RunStatus::RUNNING {
                return Ok(response);
            }
            if owner.bridge.is_closed() {
                return Err(Error::SessionClosed);
            }
            let params = serde_json::value::to_raw_value(&CancelParams {
                session_id: run.lock().unwrap().context.native_id.clone(),
            })
            .map_err(Error::harness)?;
            futures::executor::block_on(owner.bridge.notify(
                METHOD_SESSION_CANCEL.into(),
                Some(params),
                None,
            ))
            .map_err(Error::harness)?;
            let now = (owner.clock)();
            run.lock().unwrap().request_cancel(now)?;
            owner.settle(&run, bindings)?;
            Ok(RunCancelResponse {
                status: RunStatus::CANCELLING,
                ..response
            })
        })
    }

    fn transition(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        parent: bool,
        update: impl FnOnce(&mut TextRun, i64) -> Result<(), Error> + Send + 'static,
    ) -> BoxFuture<'static, ()> {
        Box::pin(async move {
            let held = if parent {
                Some(self.bindings.lock().await)
            } else {
                None
            };
            {
                let mut text = run.lock().unwrap();
                update(&mut text, (self.clock)())?;
                if text.permission_settlements().is_empty() {
                    return Ok(());
                }
            }
            if let Some(mut bindings) = held.or_else(|| self.bindings.try_lock()) {
                bindings.retain(|binding| binding.source.strong_count() > 0);
                let native_reply = bindings.iter().any(|binding| {
                    Weak::ptr_eq(&binding.run, &Arc::downgrade(&run)) && binding.request.is_some()
                });
                if !native_reply {
                    self.settle(&run, &mut bindings)?;
                    return Ok(());
                }
            }
            self.operation(move |owner, bindings| owner.settle(&run, bindings))
                .await?;
            Ok(())
        })
    }

    fn settle(&self, run: &Arc<Mutex<TextRun>>, bindings: &mut [Binding]) -> Result<(), Error> {
        let replies = run.lock().unwrap().permission_settlements();
        for reply in replies {
            let binding = bound(bindings, run, reply.interaction_id())?;
            if let Some(request) = &binding.request {
                let _ = request.respond(reply.response());
            }
            binding.request = None;
            run.lock()
                .unwrap()
                .commit_permission(reply, (self.clock)())?;
        }
        Ok(())
    }
}

fn bound<'a>(
    bindings: &'a mut [Binding],
    run: &Arc<Mutex<TextRun>>,
    id: &InteractionId,
) -> Result<&'a mut Binding, Error> {
    let binding = bindings
        .iter_mut()
        .find(|binding| &binding.id == id && Weak::ptr_eq(&binding.run, &Arc::downgrade(run)))
        .ok_or(Error::InteractionNotFound)?;
    Ok(binding)
}
