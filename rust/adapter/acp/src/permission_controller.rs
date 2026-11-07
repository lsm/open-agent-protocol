use std::sync::{Arc, Mutex, Weak};

use oap_adapter::jsonrpc::{IncomingRequest, NotificationMessage};
use oap_adapter::{BoxFuture, Error, InteractionResolution};
use oap_protocol::{InteractionId, ParticipantId};

use crate::native::{decode, PermissionRequest, PromptResult, METHOD_SESSION_REQUEST_PERMISSION};
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
    bindings: Mutex<Vec<Binding>>,
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
            bindings: Mutex::new(Vec::new()),
        }))
    }

    fn operation<T: Send + 'static>(
        self: Arc<Self>,
        work: impl FnOnce(&Self, &mut Vec<Binding>) -> Result<T, Error> + Send + 'static,
    ) -> BoxFuture<'static, T> {
        let operation = Operation::spawn(move |control| {
            self.bridge.arm(control);
            let mut bindings = self.bindings.lock().unwrap_or_else(|p| p.into_inner());
            Ok(work(&self, &mut bindings))
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
            if owner.bridge.is_closed() || run.lock().unwrap().state().closed {
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
                        text.fail(
                            "acp_invalid_permission",
                            "malformed permission request",
                            false,
                            (owner.clock)(),
                        )?;
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
            if owner.bridge.is_closed() || run.lock().unwrap().state().closed {
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
            run.lock()
                .unwrap()
                .commit_permission(reply, (owner.clock)())?;
            binding.request = None;
            Ok(())
        })
    }

    pub fn observe(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        notification: NotificationMessage,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, move |text, now| text.observe(notification, now))
    }

    pub fn complete(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        result: PromptResult,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, move |text, now| text.complete(result, now))
    }

    pub fn prompt_error(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        message: String,
        inferred: bool,
    ) -> BoxFuture<'static, ()> {
        self.transition(run, move |text, now| {
            text.prompt_error(&message, inferred, now)
        })
    }

    fn transition(
        self: Arc<Self>,
        run: Arc<Mutex<TextRun>>,
        update: impl FnOnce(&mut TextRun, i64) -> Result<(), Error> + Send + 'static,
    ) -> BoxFuture<'static, ()> {
        self.operation(move |owner, bindings| {
            update(&mut run.lock().unwrap(), (owner.clock)())?;
            owner.settle(&run, bindings)
        })
    }

    fn settle(&self, run: &Arc<Mutex<TextRun>>, bindings: &mut [Binding]) -> Result<(), Error> {
        let replies = run.lock().unwrap().permission_settlements();
        for reply in replies {
            let binding = bound(bindings, run, reply.interaction_id())?;
            if let Some(request) = &binding.request {
                let _ = request.respond(reply.response());
            }
            run.lock()
                .unwrap()
                .commit_permission(reply, (self.clock)())?;
            binding.request = None;
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
        .find(|binding| &binding.id == id)
        .ok_or(Error::InteractionNotFound)?;
    if !Weak::ptr_eq(&binding.run, &Arc::downgrade(run)) {
        return Err(Error::InvalidResolution);
    }
    Ok(binding)
}
