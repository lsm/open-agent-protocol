use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, Error, EventStream, InteractionResolution, OpenRequest,
    Recovery, ResumeRequest, Session, SubmitRequest,
};
use oap_binding::{state, Action, MemoryStore, Store};
use oap_protocol::{
    CompactionKind, CompactionPolicy, MessageSubmitResponse, RunCancelResponse, RunId,
    SessionState, SessionStatus, ToolSourceAttachment,
};
use oap_serve::{Hub, HubError, HubOptions, Registry};

fn hub(store: &Arc<MemoryStore>, home: &str) -> Hub {
    hub_in(store, home, "")
}

fn hub_in(store: &Arc<MemoryStore>, home: &str, directory: &str) -> Hub {
    let mut registry = Registry::new();
    registry.set_working_directory("memory", directory);
    registry
        .register("memory", Arc::new(Memory::new(Config::default())))
        .unwrap();
    registry
        .register(
            "settling",
            Arc::new(Settling(Memory::new(Config::default()))),
        )
        .unwrap();
    Hub::with_options(
        registry,
        HubOptions {
            bindings: Some(store.clone()),
            home: home.into(),
            stream_queue: 0,
            report: None,
        },
    )
}

fn request(id: &str) -> OpenRequest {
    OpenRequest {
        session_id: id.into(),
        ..OpenRequest::default()
    }
}

fn actions(store: &MemoryStore, id: &str) -> Vec<Action> {
    store
        .history(id)
        .unwrap()
        .into_iter()
        .map(|entry| entry.action)
        .collect()
}

#[test]
fn an_open_is_recorded_with_the_adapter_home_directory_and_settings_it_asked_for() {
    let store = Arc::new(MemoryStore::default());
    let hub = hub_in(&store, "/home/op", "/work/repo");
    let policy = CompactionPolicy {
        kind: CompactionKind::SHARE,
        share_percent: 70,
        ..CompactionPolicy::default()
    };
    let opened = block_on(hub.open(
        "memory",
        OpenRequest {
            reasoning_level: "high".to_owned().into(),
            compaction_policy: Some(policy.clone()),
            tool_sources: Vec::<ToolSourceAttachment>::new(),
            ..request("session-bound")
        },
    ))
    .unwrap();
    let entry = store.latest("session-bound").unwrap().unwrap();
    assert_eq!(entry.action, Action::Opened);
    let record = entry.record;
    assert_eq!(record.session_id, "session-bound");
    assert_eq!(record.adapter, "memory");
    assert_eq!(record.home, "/home/op");
    assert_eq!(record.directory, "/work/repo");
    assert_eq!(record.reasoning_level, "high");
    assert_eq!(record.compaction_policy, Some(policy));
    assert_eq!(record.model, opened.state.current_model_id);
    assert!(!record.harness_version.is_empty());
}

#[test]
fn a_directory_the_adapter_was_not_given_is_omitted() {
    let store = Arc::new(MemoryStore::default());
    let hub = hub(&store, "");
    block_on(hub.open("memory", request("s"))).unwrap();
    let record = store.latest("s").unwrap().unwrap().record;
    assert_eq!((record.directory.as_str(), record.home.as_str()), ("", ""));
}

#[test]
fn a_closed_session_is_recorded_as_closed_after_its_open() {
    let store = Arc::new(MemoryStore::default());
    let hub = hub(&store, "");
    let session = block_on(hub.open("memory", request("s"))).unwrap().session;
    block_on(session.close()).unwrap();
    assert_eq!(actions(&store, "s"), [Action::Opened, Action::Closed]);
    let history = store.history("s").unwrap();
    assert!(history[1].time_ms >= history[0].time_ms);
    assert!(history[1].time_ms > 0);
}

#[test]
fn a_refused_duplicate_open_is_recorded_as_a_refusal_and_nothing_else() {
    let store = Arc::new(MemoryStore::default());
    let hub = hub(&store, "");
    block_on(hub.open("memory", request("twice"))).unwrap();
    assert!(matches!(
        block_on(hub.open("memory", request("twice"))),
        Err(HubError::SessionExists(_))
    ));
    assert_eq!(actions(&store, "twice"), [Action::Opened, Action::Refused]);
    let history = store.history("twice").unwrap();
    assert!(!history[1].live());
    let running = state(&history).unwrap();
    assert_eq!(running.action, Action::Opened);
}

#[test]
fn a_session_that_settles_at_open_records_both_ends_in_one_go() {
    let store = Arc::new(MemoryStore::default());
    let hub = hub(&store, "");
    assert!(matches!(
        block_on(hub.open("settling", request("settled"))),
        Err(HubError::ClosedAtOpen(_))
    ));
    assert_eq!(actions(&store, "settled"), [Action::Opened, Action::Closed]);
}

#[test]
fn two_opens_of_different_sessions_are_two_records_and_a_hub_without_a_store_records_nothing() {
    let store = Arc::new(MemoryStore::default());
    let hub = hub(&store, "");
    block_on(hub.open("memory", request("a"))).unwrap();
    block_on(hub.open("memory", request("b"))).unwrap();
    assert_eq!(actions(&store, "a"), [Action::Opened]);
    assert_eq!(store.sessions().unwrap().len(), 2);
    let bare = Hub::new(Registry::default_registry());
    assert!(bare.bindings().is_none());
    block_on(bare.open("memory", request("c"))).unwrap();
    assert!(hub.bindings().is_some());
}

struct Settling(Memory);

struct SettlingSession(Box<dyn Session>);

impl Adapter for Settling {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.0.probe()
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            let inner = self.0.open(request).await?;
            let wrapped: Box<dyn Session> = Box::new(SettlingSession(inner));
            Ok(wrapped)
        })
    }
}

impl Session for SettlingSession {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        self.0.submit(request)
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        Box::pin(async {
            let mut state = self.0.state().await?;
            state.status = SessionStatus::CLOSED;
            Ok(state)
        })
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
        Box::pin(async { Err(Error::SessionClosed) })
    }
}

#[test]
fn a_session_closed_after_its_hub_is_gone_is_still_recorded_as_closed() {
    let store = Arc::new(MemoryStore::default());
    let session = {
        let hub = hub(&store, "");
        block_on(hub.open("memory", request("outlived")))
            .unwrap()
            .session
    };
    block_on(session.close()).unwrap();
    assert_eq!(
        actions(&store, "outlived"),
        [Action::Opened, Action::Closed]
    );
}

struct Refusing;

impl Store for Refusing {
    fn append(&self, _: oap_binding::Entry) -> Result<(), oap_binding::Error> {
        Err(oap_binding::Error::Torn("disk full".into()))
    }

    fn history(&self, _: &str) -> Result<Vec<oap_binding::Entry>, oap_binding::Error> {
        Ok(Vec::new())
    }

    fn sessions(&self) -> Result<Vec<oap_binding::Entry>, oap_binding::Error> {
        Ok(Vec::new())
    }
}

#[test]
fn a_store_that_refuses_a_record_is_reported_and_the_open_still_stands() {
    let reports = Arc::new(std::sync::Mutex::new(Vec::<String>::new()));
    let heard = reports.clone();
    let hub = Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(Refusing)),
            home: String::new(),
            stream_queue: 0,
            report: Some(Arc::new(move |line| heard.lock().unwrap().push(line))),
        },
    );
    let session = block_on(hub.open("memory", request("unrecorded")))
        .unwrap()
        .session;
    block_on(session.close()).unwrap();
    let reports = reports.lock().unwrap();
    assert_eq!(reports.len(), 2, "{reports:?}");
    assert!(reports[0].starts_with("binding: opened for unrecorded: "));
    assert!(reports[0].contains("disk full"));
    assert!(reports[1].starts_with("binding: closed for unrecorded: "));
}
