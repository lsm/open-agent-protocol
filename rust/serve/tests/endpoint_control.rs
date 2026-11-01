mod support;

use std::sync::Arc;
use std::time::Duration;

use oap_adapter::{
    Adapter, BoxFuture, Descriptor, EventStream, InteractionResolution, OpenRequest, Recovery,
    ResumeRequest, Session, SubmitRequest,
};
use oap_protocol::{MessageSubmitResponse, RunCancelResponse, RunId, SessionState};
use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

struct SlowAck(Box<dyn Session>);

impl Session for SlowAck {
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
        Box::pin(async move {
            let ack = self.0.cancel(run_id).await;
            std::thread::sleep(Duration::from_millis(200));
            ack
        })
    }
    fn resume(&self, request: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        self.0.resume(request)
    }
    fn close(&self) -> BoxFuture<'_, ()> {
        self.0.close()
    }
}

struct Slow(Arc<dyn Adapter>);

impl Adapter for Slow {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        self.0.probe()
    }
    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async move {
            let inner = self.0.open(request).await?;
            Ok(Box::new(SlowAck(inner)) as Box<dyn Session>)
        })
    }
}

fn run(hub: &Hub, lines: &[Value]) -> Vec<Value> {
    let endpoint = Endpoint::new(
        hub,
        EndpointOptions {
            adapter: "memory".into(),
            shutdown: Some(Duration::from_millis(200)),
            ..EndpointOptions::default()
        },
    )
    .unwrap();
    let input: Vec<String> = lines.iter().map(Value::to_string).collect();
    let out = support::Captured::default();
    endpoint
        .run(std::io::Cursor::new(input.join("\n")), out.clone())
        .unwrap();
    out.values()
}

fn request(kind: &str, id: &str, payload: Value) -> Value {
    json!({
        "protocol": "oap",
        "version": "0.1",
        "profile": "open-agent-protocol.agent-control-core",
        "type": kind,
        "id": id,
        "session_id": "s",
        "payload": payload,
    })
}

fn open() -> Value {
    request("session.open.request", "o", json!({"session_id": "s"}))
}

fn answer<'a>(answered: &'a [Value], id: &str) -> &'a Value {
    answered
        .iter()
        .find(|line| line["in_reply_to"] == id)
        .unwrap_or_else(|| panic!("nothing answered {id}: {answered:?}"))
}

fn wait_for(out: &support::Captured, found: impl Fn(&Value) -> bool) -> Value {
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    loop {
        if let Some(line) = out.values().into_iter().find(|line| found(line)) {
            return line;
        }
        assert!(std::time::Instant::now() < deadline, "{:?}", out.values());
        std::thread::sleep(Duration::from_millis(10));
    }
}

#[test]
fn a_cancel_answers_its_intent_before_the_run_stream_carries_the_cancellation() {
    use std::io::Write;
    let mut registry = Registry::new();
    registry
        .register(
            "slow",
            Arc::new(Slow(Registry::default_registry().lookup("memory").unwrap())),
        )
        .unwrap();
    let hub = Hub::new(registry);
    let endpoint = Endpoint::new(
        &hub,
        EndpointOptions {
            adapter: "slow".into(),
            ..EndpointOptions::default()
        },
    )
    .unwrap();
    let (reader, mut writer) = std::io::pipe().unwrap();
    let out = support::Captured::default();
    let served_out = out.clone();
    let served =
        std::thread::spawn(move || endpoint.run(std::io::BufReader::new(reader), served_out));
    let mut send = |line: Value| writeln!(writer, "{line}").unwrap();
    send(open());
    send(request(
        "session.message.submit.request",
        "m",
        json!({"session_id": "s", "delivery": "auto", "messages": [{"role": "user", "content": "run"}]}),
    ));
    let run_id = wait_for(&out, |line| line["in_reply_to"] == "m")["run_id"]
        .as_str()
        .unwrap()
        .to_owned();
    send(request(
        "run.cancel.request",
        "x",
        json!({"session_id": "t", "run_id": run_id}),
    ));
    send(request(
        "run.cancel.request",
        "c",
        json!({"session_id": "s", "run_id": run_id}),
    ));
    let cancelled = wait_for(&out, |line| line["type"] == "run.cancelled");
    assert_eq!(cancelled["run_id"], run_id.as_str());
    drop(writer);
    served.join().unwrap().unwrap();
    let answered = out.values();
    let acknowledged = answer(&answered, "c");
    assert_eq!(
        acknowledged["type"], "run.cancel.response",
        "{acknowledged}"
    );
    assert_eq!(acknowledged["run_id"], run_id.as_str());
    assert_eq!(acknowledged["session_id"], "s");
    let at = |found: &dyn Fn(&Value) -> bool| answered.iter().position(found);
    assert!(
        at(&|line| line["in_reply_to"] == "c") < at(&|line| line["type"] == "run.cancelled"),
        "{answered:?}"
    );
    assert_eq!(
        answer(&answered, "x")["payload"]["error"]["code"],
        "scope_mismatch"
    );
}

#[test]
fn a_resolution_naming_a_run_the_session_never_ran_is_refused_run_not_found() {
    let hub = Hub::new(Registry::default_registry());
    let answered = run(
        &hub,
        &[
            open(),
            request(
                "action.permission.resolve.request",
                "p",
                json!({"session_id": "s", "run_id": "r", "interaction_id": "i", "responded_by": "oap-hub", "decision": "allow"}),
            ),
            request(
                "user.input.resolve.request",
                "u",
                json!({"session_id": "s", "run_id": "r", "interaction_id": "i", "responded_by": "oap-hub"}),
            ),
        ],
    );
    for id in ["p", "u"] {
        let refused = &answer(&answered, id)["payload"]["error"];
        assert_eq!(refused["code"], "run_not_found", "{id}: {refused}");
    }
}

#[test]
fn a_settings_update_answers_and_then_publishes_the_state_in_its_own_sequence() {
    let hub = Hub::new(Registry::default_registry());
    let settings = |id: &str| {
        request(
            "session.settings.update.request",
            id,
            json!({"session_id": "s", "compaction_policy": {"kind": "tokens", "tokens": 1}}),
        )
    };
    let answered = run(&hub, &[open(), settings("a"), settings("b")]);
    let types: Vec<&str> = answered[1..]
        .iter()
        .map(|line| line["type"].as_str().unwrap())
        .collect();
    assert_eq!(
        types,
        [
            "session.settings.update.response",
            "session.state.updated",
            "session.settings.update.response",
            "session.state.updated",
        ],
        "{answered:?}"
    );
    assert_eq!(answered[1]["in_reply_to"], "a");
    assert_eq!(answered[2]["sequence"], 1);
    assert_eq!(answered[4]["sequence"], 2);
    assert_eq!(answered[2]["session_id"], "s");
}
