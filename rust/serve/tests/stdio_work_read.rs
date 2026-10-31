mod support;

use std::sync::Arc;
use std::time::{Duration, Instant};

use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_binding::MemoryStore;
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode, RunId,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::Value;

fn recorded() -> Hub {
    Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    )
}

fn running(hub: &Hub) -> RunId {
    let opened = block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    block_on(opened.session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "s".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("do the thing"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap()
    .run_id
    .unwrap()
}

fn lines(hub: &Hub, input: &[&str]) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        std::io::Cursor::new(input.into_bytes()),
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
fn the_work_verbs_refuse_a_session_they_cannot_find_and_a_bad_read_window() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"work.status","session_id":"absent"}"#,
            r#"{"id":2,"op":"work.read","session_id":"absent"}"#,
            r#"{"id":3,"op":"work.stop","session_id":"absent"}"#,
            r#"{"id":4,"op":"work.read","session_id":"absent","limit":0}"#,
            r#"{"id":5,"op":"work.read","session_id":"absent","after":-1}"#,
            r#"{"id":6,"op":"work.status","session_id":"absent","adapter":"memory"}"#,
        ],
    );
    for line in &answered[..3] {
        assert_eq!(line["error"]["code"], "unknown_session", "{line}");
        assert_eq!(line["error"]["message"], "no session \"absent\"");
    }
    assert_eq!(
        answered[3]["error"]["message"],
        "work.read: limit must be from 1 to 500"
    );
    assert_eq!(answered[4]["error"]["message"], "after is a whole number");
    assert_eq!(answered[5]["error"]["code"], "invalid_request");
}

#[test]
fn a_held_session_answers_its_work_and_the_turns_serve_recorded() {
    let hub = Hub::new(Registry::default_registry());
    let run = running(&hub);
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"work.status","session_id":"s"}"#,
            r#"{"id":2,"op":"work.read","session_id":"s"}"#,
        ],
    );
    let work = &answered[0]["result"];
    assert_eq!(
        work["ref"],
        serde_json::json!({"adapter":"memory","session_id":"s"})
    );
    assert_eq!(work["status"], "needs_you", "{work}");
    assert_eq!(work["run_id"], run.as_str());
    assert!(work["pending"]["interaction_id"].is_string());
    assert!(work.get("held").is_none());
    let turn = &answered[1]["result"]["turns"][0];
    assert_eq!(turn["index"], 0);
    assert_eq!(turn["role"], "user");
    assert_eq!(turn["text"], "do the thing");
    assert_eq!(turn["run_id"], run.as_str());
}

#[test]
fn work_stop_cancels_the_active_run() {
    let hub = Hub::new(Registry::default_registry());
    running(&hub);
    let answered = lines(&hub, &[r#"{"id":1,"op":"work.stop","session_id":"s"}"#]);
    assert_eq!(answered[0]["ok"], true, "{}", answered[0]);
    let deadline = Instant::now() + Duration::from_secs(5);
    let entry = hub.session(&"s".into()).unwrap();
    while block_on(entry.state()).unwrap().active_run_id.is_some() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert_eq!(block_on(entry.state()).unwrap().active_run_id, None);
}

#[test]
fn a_session_only_the_history_names_answers_unheld_without_reopening() {
    let hub = recorded();
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    block_on(hub.session(&"s".into()).unwrap().close()).unwrap();
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"work.status","session_id":"s"}"#,
            r#"{"id":2,"op":"work.read","session_id":"s"}"#,
            r#"{"id":3,"op":"work.stop","session_id":"s"}"#,
        ],
    );
    let work = &answered[0]["result"];
    assert_eq!(work["held"], false, "{work}");
    assert_eq!(work["state"], "closed");
    assert_eq!(work["ref"]["session_id"], "s");
    assert_eq!(answered[1]["result"]["turns"], serde_json::json!([]));
    assert_eq!(answered[2]["result"], *work);
    assert!(hub.session(&"s".into()).is_err());
}
