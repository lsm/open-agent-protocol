mod support;

use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, InteractionResolution, OpenRequest, Recovery, ResumeRequest,
    Session, SubmitRequest,
};
use oap_protocol::{MessageSubmitResponse, RunCancelResponse, RunId, SessionState};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

struct Bare(Box<dyn Session>);

impl Session for Bare {
    fn submit(
        &self,
        request: SubmitRequest,
    ) -> BoxFuture<'_, (MessageSubmitResponse, oap_adapter::EventStream)> {
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
    fn resume(
        &self,
        request: ResumeRequest,
    ) -> BoxFuture<'_, (Recovery, oap_adapter::EventStream)> {
        self.0.resume(request)
    }
    fn close(&self) -> BoxFuture<'_, ()> {
        self.0.close()
    }
}

struct Unlisting(Arc<dyn Adapter>);

impl Adapter for Unlisting {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.0.probe()
    }
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            let inner = self.0.open(request).await?;
            Ok(Box::new(Bare(inner)) as Box<dyn Session>)
        })
    }
}

fn opened(registry: Registry, adapter: &str) -> Hub {
    let hub = Hub::new(registry);
    block_on(hub.open(
        adapter,
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    hub
}

fn unlisting() -> Hub {
    let mut registry = Registry::new();
    let memory = Registry::default_registry().lookup("memory").unwrap();
    registry
        .register("bare", Arc::new(Unlisting(memory)))
        .unwrap();
    opened(registry, "bare")
}

fn lines(hub: &Hub, input: &[&str]) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        input.as_bytes(),
        output.clone(),
        &StdioOptions::default(),
    )
    .unwrap();
    String::from_utf8(output.bytes())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

#[test]
fn models_answers_the_sessions_catalog_under_its_revision() {
    let hub = opened(Registry::default_registry(), "memory");
    let answered = lines(&hub, &[r#"{"id":1,"op":"models","session_id":"s"}"#]);
    let response = &answered[0]["result"];
    assert_eq!(response["type"], "models.response", "{}", answered[0]);
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["payload"]["session_id"], "s");
    assert!(response["payload"]["models"].is_array());
    assert_eq!(
        response["capability_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
}

#[test]
fn tools_answers_the_sessions_catalog_under_its_revision() {
    let hub = opened(Registry::default_registry(), "memory");
    let answered = lines(&hub, &[r#"{"id":1,"op":"tools","session_id":"s"}"#]);
    let response = &answered[0]["result"];
    assert_eq!(
        response["type"], "action.tools.list.response",
        "{}",
        answered[0]
    );
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["payload"]["session_id"], "s");
    assert!(response["payload"]["tools"].is_array());
    assert_eq!(
        response["capability_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
}

#[test]
fn a_catalog_op_needs_a_live_session_and_no_other_parameter() {
    let hub = opened(Registry::default_registry(), "memory");
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"models","session_id":"ghost"}"#,
            r#"{"id":2,"op":"tools","session_id":"ghost"}"#,
            r#"{"id":3,"op":"models","session_id":"s","adapter":"memory"}"#,
            r#"{"id":4,"op":"tools","session_id":"s","allow_degraded_features":["x"]}"#,
        ],
    );
    assert_eq!(answered[0]["error"]["code"], "unknown_session");
    assert_eq!(answered[1]["error"]["code"], "unknown_session");
    assert_eq!(answered[2]["error"]["code"], "invalid_request");
    assert_eq!(answered[3]["ok"], true, "{}", answered[3]);
}

#[test]
fn a_session_with_no_catalog_answers_its_list_feature_unadvertised() {
    let hub = unlisting();
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"models","session_id":"s"}"#,
            r#"{"id":2,"op":"tools","session_id":"s"}"#,
        ],
    );
    for (line, feature) in answered.iter().zip(["models.list", "action.tools.list"]) {
        let error = &line["error"];
        assert_eq!(error["code"], "unsupported_feature", "{line}");
        assert_eq!(error["details"]["feature"], feature);
        assert_eq!(error["details"]["reason"], "unadvertised");
    }
}
