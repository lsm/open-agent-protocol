mod support;

use std::sync::Arc;
use std::time::Duration;

use oap_binding::MemoryStore;
use oap_serve::endpoint::{Endpoint, EndpointOptions};
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::{json, Value};

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

#[test]
fn the_model_and_tool_catalogs_answer_under_the_revision_they_bind_to() {
    let hub = Hub::new(Registry::default_registry());
    let answered = run(
        &hub,
        &[
            open(),
            request("models.request", "m", json!({"session_id": "s"})),
            request("action.tools.list.request", "t", json!({"session_id": "s"})),
        ],
    );
    for (id, kind) in [
        ("m", "models.response"),
        ("t", "action.tools.list.response"),
    ] {
        let answered = answer(&answered, id);
        assert_eq!(answered["type"], kind, "{answered}");
        assert_eq!(answered["session_id"], "s");
        assert_eq!(
            answered["capability_revision"],
            oap_adapter::memory::CAPABILITY_REVISION
        );
    }
}

#[test]
fn a_session_list_needs_a_history_and_reads_the_sessions_it_records() {
    let plain = run(
        &Hub::new(Registry::default_registry()),
        &[request("session.list.request", "l", json!({}))],
    );
    let refused = &answer(&plain, "l")["payload"]["error"];
    assert_eq!(refused["code"], "unsupported_feature", "{refused}");
    assert_eq!(refused["details"]["feature"], "session.list");
    assert_eq!(refused["details"]["reason"], "unadvertised");
    let recorded = Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    );
    let answered = run(
        &recorded,
        &[
            open(),
            request("session.list.request", "l", json!({})),
            request("session.list.request", "bad", json!({"limit": 100000})),
        ],
    );
    let listed = answer(&answered, "l");
    assert_eq!(listed["type"], "session.list.response", "{listed}");
    assert!(listed["payload"].to_string().contains("\"s\""), "{listed}");
    assert_eq!(
        answer(&answered, "bad")["payload"]["error"]["code"],
        "invalid_request"
    );
}

#[test]
fn a_compaction_answers_its_admission_and_then_streams_the_run_it_admitted() {
    let hub = Hub::new(Registry::default_registry());
    let answered = run(
        &hub,
        &[
            open(),
            request(
                "session.compact.request",
                "c",
                json!({"session_id": "s", "delivery": "auto"}),
            ),
        ],
    );
    let admitted = answer(&answered, "c");
    assert_eq!(admitted["type"], "session.compact.response", "{admitted}");
    let run = admitted["run_id"].as_str().unwrap();
    let at = answered
        .iter()
        .position(|line| line["in_reply_to"] == "c")
        .unwrap();
    let streamed: Vec<&Value> = answered[at + 1..].iter().collect();
    assert_eq!(streamed[0]["type"], "run.started", "{answered:?}");
    assert!(streamed.iter().all(|event| event["run_id"] == run));
}
