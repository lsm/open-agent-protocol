mod support;

use std::sync::{Arc, Mutex};

use futures::executor::block_on;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, EventStream, InteractionResolution, OpenRequest, Recovery,
    ResumeRequest, Session, SubmitRequest, TranscriptLoader, TranscriptRequest,
};
use oap_binding::MemoryStore;
use oap_protocol::{
    MessageSubmitResponse, RunCancelResponse, RunId, SessionState, TranscriptLoadResponse,
};
use oap_serve::stdio::StdioOptions;
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::Value;

struct Native(Box<dyn Session>);

impl Session for Native {
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
    fn cancel(&self, run_id: RunId) -> BoxFuture<'_, RunCancelResponse> {
        self.0.cancel(run_id)
    }
    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        self.0.resume(request)
    }
    fn close(&self) -> BoxFuture<'_, ()> {
        self.0.close()
    }
    fn native_session_id(&self) -> Option<String> {
        Some("native-1".into())
    }
}

struct Reading {
    inner: Arc<dyn Adapter>,
    asked: Mutex<Vec<TranscriptRequest>>,
}

impl Adapter for Reading {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.inner.probe()
    }
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            let inner = self.inner.open(request).await?;
            Ok(Box::new(Native(inner)) as Box<dyn Session>)
        })
    }
    fn transcript_loader(&self) -> Option<&dyn TranscriptLoader> {
        Some(self)
    }
}

impl TranscriptLoader for Reading {
    fn transcript_load(&self, request: TranscriptRequest) -> BoxFuture<'_, TranscriptLoadResponse> {
        self.asked.lock().unwrap().push(request);
        Box::pin(async {
            Ok(TranscriptLoadResponse {
                session_id: "native-1".into(),
                next_cursor: "next".into(),
                ..TranscriptLoadResponse::default()
            })
        })
    }
}

fn reading() -> (Arc<Reading>, Registry) {
    let adapter = Arc::new(Reading {
        inner: Registry::default_registry().lookup("memory").unwrap(),
        asked: Mutex::default(),
    });
    let mut registry = Registry::new();
    registry.register("reading", adapter.clone()).unwrap();
    (adapter, registry)
}

fn open(hub: &Hub, adapter: &str) {
    block_on(hub.open(
        adapter,
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
}

fn lines(hub: &Hub, input: &[&str]) -> Vec<Value> {
    support::in_turn(hub, input, &StdioOptions::default())
}

#[test]
fn transcript_refuses_an_unknown_session_a_limit_out_of_range_and_a_cursor_as_after() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"transcript","session_id":"absent"}"#,
            r#"{"id":2,"op":"transcript","session_id":"absent","limit":0}"#,
            r#"{"id":3,"op":"transcript","session_id":"absent","limit":501}"#,
            r#"{"id":4,"op":"transcript","session_id":"absent","after":1}"#,
        ],
    );
    assert_eq!(
        answered[0]["error"]["code"], "unknown_session",
        "{}",
        answered[0]
    );
    assert_eq!(answered[0]["error"]["message"], "no session \"absent\"");
    assert_eq!(answered[1]["error"]["code"], "invalid_request");
    assert_eq!(
        answered[1]["error"]["message"],
        "transcript: limit must be from 1 to 500"
    );
    assert_eq!(answered[2]["error"]["code"], "invalid_request");
    assert_eq!(answered[3]["error"]["code"], "invalid_request");
}

#[test]
fn a_held_session_whose_adapter_keeps_no_transcript_answers_transcript_load_unadvertised() {
    let hub = Hub::new(Registry::default_registry());
    open(&hub, "memory");
    let answered = lines(&hub, &[r#"{"id":1,"op":"transcript","session_id":"s"}"#]);
    let error = &answered[0]["error"];
    assert_eq!(error["code"], "unsupported_feature", "{error}");
    assert_eq!(error["details"]["feature"], "transcript.load");
    assert_eq!(error["details"]["reason"], "unadvertised");
}

#[test]
fn a_held_session_reads_its_native_transcript_under_its_own_id() {
    let (adapter, registry) = reading();
    let hub = Hub::new(registry);
    open(&hub, "reading");
    let answered = lines(
        &hub,
        &[r#"{"id":1,"op":"transcript","session_id":"s","cursor":"c1","limit":7}"#],
    );
    let page = &answered[0]["result"];
    assert_eq!(page["session_id"], "s", "{}", answered[0]);
    assert_eq!(page["entries"], serde_json::json!([]));
    assert_eq!(page["next_cursor"], "next");
    let asked = adapter.asked.lock().unwrap();
    assert_eq!(asked[0].native_id, "native-1");
    assert_eq!(asked[0].cursor, "c1");
    assert_eq!(asked[0].limit, 7);
}

#[test]
fn a_closed_session_the_history_records_reads_the_native_it_was_bound_to() {
    let (adapter, registry) = reading();
    let hub = Hub::with_options(
        registry,
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    );
    open(&hub, "reading");
    block_on(hub.session(&"s".into()).unwrap().close()).unwrap();
    let answered = lines(&hub, &[r#"{"id":1,"op":"transcript","session_id":"s"}"#]);
    assert_eq!(answered[0]["result"]["session_id"], "s", "{}", answered[0]);
    let asked = adapter.asked.lock().unwrap();
    assert_eq!(asked[0].native_id, "native-1");
    assert_eq!(asked[0].limit, 100);
}
