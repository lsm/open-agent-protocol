use std::sync::{Arc, Mutex};

use futures::executor::block_on;
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, Error, EventStream, InteractionResolution, OpenRequest,
    Recovery, ResumeRequest, Session, SubmitRequest, CONTROL_UNSATISFIABLE,
};
use oap_binding::{Action, MemoryStore, Store};
use oap_protocol::{
    MessageSubmitResponse, RunCancelResponse, RunId, SessionState, FEATURE_OPEN_REOPEN,
};
use oap_serve::{Hub, HubError, HubOptions, Registry};

struct Native {
    inner: Memory,
    asked: Mutex<Vec<OpenRequest>>,
}

struct NativeSession(Box<dyn Session>);

impl Adapter for Native {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.inner.probe()
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        self.asked.lock().unwrap().push(request.clone());
        Box::pin(async move {
            let inner = self.inner.open(request).await?;
            let wrapped: Box<dyn Session> = Box::new(NativeSession(inner));
            Ok(wrapped)
        })
    }
}

impl Session for NativeSession {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        self.0.submit(request)
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        self.0.state()
    }

    fn resolve(&self, resolution: InteractionResolution) -> BoxFuture<'_, ()> {
        self.0.resolve(resolution)
    }

    fn cancel(&self, run: RunId) -> BoxFuture<'_, RunCancelResponse> {
        self.0.cancel(run)
    }

    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        self.0.resume(request)
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        self.0.close()
    }

    fn native_session_id(&self) -> Option<String> {
        Some("native-thread-7".into())
    }
}

fn bound(store: &Arc<MemoryStore>, registry: Registry) -> Hub {
    Hub::with_options(
        registry,
        HubOptions {
            bindings: Some(store.clone()),
            ..HubOptions::default()
        },
    )
}

fn memory_registry() -> Registry {
    let mut registry = Registry::new();
    registry
        .register("memory", Arc::new(Memory::new(Config::default())))
        .unwrap();
    registry
}

fn request(id: &str, reopen: bool) -> OpenRequest {
    OpenRequest {
        session_id: id.into(),
        reopen,
        ..OpenRequest::default()
    }
}

#[test]
fn a_reopen_is_recorded_as_reopened_after_the_close() {
    let store = Arc::new(MemoryStore::default());
    let hub = bound(&store, memory_registry());
    let session = block_on(hub.open("memory", request("s", false)))
        .unwrap()
        .session;
    block_on(session.close()).unwrap();
    let reopened = block_on(hub.open("memory", request("s", true))).unwrap();
    assert!(reopened
        .state
        .recovery
        .as_ref()
        .is_some_and(|recovery| recovery.recovered));
    let actions: Vec<Action> = store
        .history("s")
        .unwrap()
        .into_iter()
        .map(|entry| entry.action)
        .collect();
    assert_eq!(actions, [Action::Opened, Action::Closed, Action::Reopened]);
}

#[test]
fn a_reopen_of_a_live_session_is_session_exists_before_the_adapter_is_asked() {
    let hub = Hub::new(memory_registry());
    block_on(hub.open("memory", request("live", false))).unwrap();
    assert!(matches!(
        block_on(hub.open("memory", request("live", true))),
        Err(HubError::SessionExists(_))
    ));
}

#[test]
fn a_reopen_hands_the_adapter_the_native_id_its_binding_recorded() {
    let store = Arc::new(MemoryStore::default());
    let native = Arc::new(Native {
        inner: Memory::new(Config::default()),
        asked: Mutex::new(Vec::new()),
    });
    let mut registry = Registry::new();
    registry.register("native", native.clone()).unwrap();
    registry
        .register("memory", Arc::new(Memory::new(Config::default())))
        .unwrap();
    let hub = bound(&store, registry);
    let session = block_on(hub.open("native", request("bound", false)))
        .unwrap()
        .session;
    assert_eq!(
        store
            .latest("bound")
            .unwrap()
            .unwrap()
            .record
            .native_session_id,
        "native-thread-7"
    );
    block_on(session.close()).unwrap();
    block_on(hub.open("native", request("bound", true))).unwrap();
    let last = native.asked.lock().unwrap().last().cloned().unwrap();
    assert!(last.reopen);
    assert_eq!(last.native_session_id, "native-thread-7");

    let before = native.asked.lock().unwrap().len();
    assert!(matches!(
        block_on(hub.open("native", request("unbound", true))),
        Err(HubError::UnknownSession(id)) if id.as_str() == "unbound"
    ));
    block_on(hub.open("memory", request("elsewhere", false))).unwrap();
    assert!(matches!(
        block_on(hub.open("native", request("elsewhere", true))),
        Err(HubError::SessionExists(_))
    ));
    assert_eq!(native.asked.lock().unwrap().len(), before);
}

#[test]
fn a_reopen_bound_to_another_adapter_is_unknown_session() {
    let store = Arc::new(MemoryStore::default());
    let mut registry = memory_registry();
    registry
        .register("other", Arc::new(Memory::new(Config::default())))
        .unwrap();
    let hub = bound(&store, registry);
    let session = block_on(hub.open("memory", request("s", false)))
        .unwrap()
        .session;
    block_on(session.close()).unwrap();
    assert!(matches!(
        block_on(hub.open("other", request("s", true))),
        Err(HubError::UnknownSession(_))
    ));
}

#[test]
fn a_reopen_after_a_restart_the_adapter_cannot_load_is_unsupported_feature() {
    let store = Arc::new(MemoryStore::default());
    let session = block_on(bound(&store, memory_registry()).open("memory", request("kept", false)))
        .unwrap()
        .session;
    block_on(session.close()).unwrap();
    match block_on(bound(&store, memory_registry()).open("memory", request("kept", true))) {
        Err(HubError::Adapter(Error::UnsupportedControl(control))) => {
            assert_eq!(control.feature, FEATURE_OPEN_REOPEN);
            assert_eq!(control.reason, CONTROL_UNSATISFIABLE);
        }
        other => panic!("reopen = {:?}", other.map(|opened| opened.state)),
    }
}
