mod support;

use futures::executor::block_on;
use oxap_protocol::{ActionCallResolveRequest, Envelope, EnvelopeType};
use oxap_serve::stdio::StdioOptions;
use oxap_serve::{Hub, Registry};
use serde_json::Value;

fn opened() -> Hub {
    let hub = Hub::new(Registry::default_registry());
    block_on(hub.open(
        "memory",
        oxap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oxap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    hub
}

fn call(session: &str) -> ActionCallResolveRequest {
    ActionCallResolveRequest {
        interaction_id: "call-1".into(),
        session_id: session.into(),
        run_id: "run-1".into(),
        tool_call_id: "tool-1".into(),
        requested_by: "agent".into(),
        responded_by: "user".into(),
        result: Some(serde_json::value::to_raw_value(&serde_json::json!({"ok": true})).unwrap()),
        ..ActionCallResolveRequest::default()
    }
}

fn resolve_line(id: i64, request: &ActionCallResolveRequest) -> String {
    let mut envelope = Envelope::new(
        EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
        format!("resolve-{id}"),
        request,
    )
    .unwrap();
    envelope.session_id = Some("s".into());
    envelope.run_id = Some("run-1".into());
    format!(
        r#"{{"id":{id},"op":"resolve","session_id":"s","request":{}}}"#,
        serde_json::to_string(&envelope).unwrap()
    )
}

fn lines(hub: &Hub, input: &[String]) -> Vec<Value> {
    support::in_turn(hub, input, &StdioOptions::default())
}

#[test]
fn a_call_resolution_answers_the_adapters_verdict_in_reply_to_its_request() {
    let hub = opened();
    let answered = lines(&hub, &[resolve_line(1, &call("s"))]);
    let response = &answered[0]["result"];
    assert_eq!(
        response["type"], "action.call.resolve.response",
        "{}",
        answered[0]
    );
    assert_eq!(response["in_reply_to"], "resolve-1");
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["run_id"], "run-1");
    assert_eq!(response["payload"]["tool_call_id"], "tool-1");
    assert_eq!(response["payload"]["accepted"], false);
    assert!(response["payload"]["reason"].is_string());
}

#[test]
fn a_call_resolution_for_another_session_is_a_scope_mismatch() {
    let hub = opened();
    let answered = lines(&hub, &[resolve_line(1, &call("elsewhere"))]);
    assert_eq!(
        answered[0]["error"]["code"], "scope_mismatch",
        "{}",
        answered[0]
    );
}

struct Closing(Box<dyn oxap_adapter::Session>);

impl oxap_adapter::CallResolver for Closing {
    fn resolve_call(
        &self,
        _: oxap_adapter::CallResolution,
    ) -> oxap_adapter::BoxFuture<'_, oxap_protocol::ActionCallResolveResponse> {
        Box::pin(async { Err(oxap_adapter::Error::SessionClosed) })
    }
}

impl oxap_adapter::Session for Closing {
    fn submit(
        &self,
        request: oxap_adapter::SubmitRequest,
    ) -> oxap_adapter::BoxFuture<
        '_,
        (
            oxap_protocol::MessageSubmitResponse,
            oxap_adapter::EventStream,
        ),
    > {
        self.0.submit(request)
    }
    fn state(&self) -> oxap_adapter::BoxFuture<'_, oxap_protocol::SessionState> {
        self.0.state()
    }
    fn resolve(
        &self,
        resolution: oxap_adapter::InteractionResolution,
    ) -> oxap_adapter::BoxFuture<'_, ()> {
        self.0.resolve(resolution)
    }
    fn cancel(
        &self,
        run_id: oxap_protocol::RunId,
    ) -> oxap_adapter::BoxFuture<'_, oxap_protocol::RunCancelResponse> {
        self.0.cancel(run_id)
    }
    fn resume(
        &self,
        request: oxap_adapter::ResumeRequest,
    ) -> oxap_adapter::BoxFuture<'_, (oxap_adapter::Recovery, oxap_adapter::EventStream)> {
        self.0.resume(request)
    }
    fn close(&self) -> oxap_adapter::BoxFuture<'_, ()> {
        self.0.close()
    }
    fn call_resolver(&self) -> Option<&dyn oxap_adapter::CallResolver> {
        Some(self)
    }
}

struct ClosingAdapter(std::sync::Arc<dyn oxap_adapter::Adapter>);

impl oxap_adapter::Adapter for ClosingAdapter {
    fn probe(&self) -> oxap_adapter::BoxFuture<'_, oxap_adapter::Descriptor> {
        self.0.probe()
    }
    fn open(
        &self,
        request: oxap_adapter::OpenRequest,
    ) -> oxap_adapter::BoxFuture<'_, Box<dyn oxap_adapter::Session>> {
        Box::pin(async move {
            let inner = self.0.open(request).await?;
            Ok(Box::new(Closing(inner)) as Box<dyn oxap_adapter::Session>)
        })
    }
}

#[test]
fn a_call_resolution_finding_its_session_closed_closes_the_hub_session() {
    let mut registry = Registry::new();
    let memory = Registry::default_registry().lookup("memory").unwrap();
    registry
        .register("memory", std::sync::Arc::new(ClosingAdapter(memory)))
        .unwrap();
    let hub = Hub::new(registry);
    let opened = block_on(hub.open(
        "memory",
        oxap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oxap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    let answered = lines(&hub, &[resolve_line(1, &call("s"))]);
    assert_eq!(
        answered[0]["error"]["code"], "session_closed",
        "{}",
        answered[0]
    );
    assert!(opened.session.is_closed());
    assert!(hub.session(&"s".into()).is_err());
}
