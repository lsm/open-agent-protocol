mod support;

use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode, RunId,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubSession, Registry};
use serde_json::Value;

fn opened(capacity: usize) -> (Hub, Arc<HubSession>) {
    let mut registry = Registry::new();
    registry
        .register(
            "memory",
            Arc::new(Memory::new(Config {
                journal_capacity: capacity,
                ..Config::default()
            })),
        )
        .unwrap();
    let hub = Hub::new(registry);
    let session = block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap()
    .session;
    (hub, session)
}

fn started(session: &Arc<HubSession>) -> RunId {
    block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "s".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("go"),
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
    lines_with(hub, input, StdioOptions::default())
}

fn lines_with(hub: &Hub, input: &[&str], options: StdioOptions) -> Vec<Value> {
    support::in_turn(hub, input, &options)
}

fn signals<'a>(lines: &'a [Value], event: &str) -> Vec<&'a Value> {
    lines.iter().filter(|line| line["event"] == event).collect()
}

#[test]
fn events_refuses_a_cursor_it_cannot_read_and_a_session_it_does_not_hold() {
    let (hub, _) = opened(64);
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"events","session_id":"ghost"}"#,
            r#"{"id":2,"op":"events","session_id":"s","run_id":"r"}"#,
            r#"{"id":3,"op":"events","session_id":"s","after":"5"}"#,
            r#"{"id":4,"op":"events","session_id":"s","after":0}"#,
            r#"{"id":5,"op":"events","session_id":"s","adapter":"memory"}"#,
        ],
    );
    assert_eq!(answered[0]["error"]["code"], "unknown_session");
    assert_eq!(answered[1]["error"]["code"], "invalid_cursor");
    assert_eq!(
        answered[1]["error"]["message"],
        "run_id names the run a cursor belongs to; it has no meaning without after"
    );
    assert_eq!(answered[2]["error"]["code"], "invalid_cursor");
    assert_eq!(answered[3]["error"]["code"], "no_run_to_resume");
    assert_eq!(answered[4]["error"]["code"], "invalid_request");
}

#[test]
fn a_cursor_replays_its_run_as_envelope_lines_after_a_null_response() {
    let (hub, session) = opened(64);
    let run = started(&session);
    let answered = lines(
        &hub,
        &[&format!(
            r#"{{"id":7,"op":"events","session_id":"s","run_id":"{run}","after":2}}"#
        )],
    );
    assert_eq!(answered[0]["id"], 7);
    assert_eq!(answered[0]["ok"], true);
    assert!(answered[0]["result"].is_null());
    let envelopes = signals(&answered, "envelope");
    assert!(!envelopes.is_empty());
    for (offset, line) in envelopes.iter().enumerate() {
        assert_eq!(line["id"], 7);
        assert_eq!(line["session_id"], "s");
        assert_eq!(line["sequence"], 3 + offset as u64);
        assert_eq!(line["envelope"]["sequence"], line["sequence"]);
        assert_eq!(line["envelope"]["run_id"], run.as_str());
    }
}

#[test]
fn a_cursor_older_than_the_journal_answers_null_and_then_a_replay_gap() {
    let (hub, session) = opened(2);
    let run = started(&session);
    block_on(session.cancel(run.clone())).unwrap();
    let answered = lines(
        &hub,
        &[&format!(
            r#"{{"id":3,"op":"events","session_id":"s","run_id":"{run}","after":0}}"#
        )],
    );
    assert!(answered[0]["result"].is_null());
    let gaps = signals(&answered, "oap-replay-gap");
    assert_eq!(gaps.len(), 1);
    assert_eq!(gaps[0]["id"], 3);
    assert_eq!(gaps[0]["session_id"], "s");
    assert_eq!(gaps[0]["requested_after"], 0);
    assert!(gaps[0]["oldest_available"].as_u64().unwrap() > 1);
}

#[test]
fn a_live_subscription_joining_mid_run_says_where_it_joined() {
    let (hub, session) = opened(64);
    let run = started(&session);
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    while hub.subscribe(&"s".into()).unwrap().joined_at().is_none() {
        assert!(std::time::Instant::now() < deadline);
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    let answered = lines(&hub, &[r#"{"id":4,"op":"events","session_id":"s"}"#]);
    assert!(answered[0]["result"].is_null());
    let joined = signals(&answered, "oap-subscribed");
    assert_eq!(joined.len(), 1);
    assert_eq!(joined[0]["run_id"], run.as_str());
    assert!(joined[0]["joined_after"].as_u64().unwrap() > 0);
}

#[test]
fn closing_the_session_ends_its_subscription_with_a_session_closed_line() {
    let (hub, _) = opened(64);
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"events","session_id":"s"}"#,
            r#"{"id":2,"op":"close","session_id":"s"}"#,
        ],
    );
    let closed = signals(&answered, "oap-session-closed");
    assert_eq!(closed.len(), 1);
    assert_eq!(closed[0]["id"], 1);
    assert_eq!(closed[0]["session_id"], "s");
    assert_eq!(closed[0]["message"], "the session is closed");
}

#[test]
fn a_subscription_past_the_cap_is_refused_busy() {
    let (hub, _) = opened(64);
    let answered = lines_with(
        &hub,
        &[
            r#"{"id":1,"op":"events","session_id":"s"}"#,
            r#"{"id":2,"op":"events","session_id":"s"}"#,
        ],
        StdioOptions {
            max_subscriptions: 1,
            ..StdioOptions::default()
        },
    );
    assert_eq!(answered[0]["ok"], true);
    let refused = answered.iter().find(|line| line["id"] == 2).unwrap();
    assert_eq!(refused["error"]["code"], "busy");
    assert!(refused["error"]["message"]
        .as_str()
        .unwrap()
        .starts_with("the frontend is already serving 1 subscriptions"));
}

#[test]
fn a_replay_gap_falls_back_to_its_minimal_line_under_a_tight_frame_limit() {
    let id = "s".repeat(120);
    let mut registry = Registry::new();
    registry
        .register(
            "memory",
            Arc::new(Memory::new(Config {
                journal_capacity: 2,
                ..Config::default()
            })),
        )
        .unwrap();
    let hub = Hub::new(registry);
    let session = block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: id.as_str().into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap()
    .session;
    let run = block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: id.as_str().into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("go"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap()
    .run_id
    .unwrap();
    block_on(session.cancel(run.clone())).unwrap();
    let answered = lines_with(
        &hub,
        &[&format!(
            r#"{{"id":3,"op":"events","session_id":"{id}","run_id":"{run}","after":0}}"#
        )],
        StdioOptions {
            frame_limit: 256,
            ..StdioOptions::default()
        },
    );
    let gaps = signals(&answered, "oap-replay-gap");
    assert_eq!(gaps.len(), 1, "{answered:?}");
    assert!(gaps[0].get("session_id").is_none());
    assert!(gaps[0].get("message").is_none());
    assert_eq!(gaps[0]["requested_after"], 0);
}

struct Stalled;

impl std::io::Write for Stalled {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        loop {
            std::thread::park();
        }
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn a_host_that_stops_reading_cannot_hold_serve_past_its_shutdown_window() {
    let (hub, session) = opened(64);
    started(&session);
    let input = "{\"id\":1,\"op\":\"events\",\"session_id\":\"s\"}\n";
    let began = std::time::Instant::now();
    let served = serve(
        &hub,
        std::io::Cursor::new(input.to_owned().into_bytes()),
        Stalled,
        &StdioOptions::default(),
    );
    let took = began.elapsed();
    assert!(
        matches!(served, Err(oap_serve::stdio::StdioError::ShutdownStalled)),
        "{served:?}"
    );
    assert!(took >= oap_serve::stdio::SHUTDOWN_WINDOW / 2, "{took:?}");
    assert!(took < oap_serve::stdio::SHUTDOWN_WINDOW * 2, "{took:?}");
}

#[test]
fn refusals_a_stalled_host_cannot_take_are_dropped_and_reported_rather_than_stalling_the_reader() {
    let hub = Hub::new(Registry::default_registry());
    let input: String = (1..=1200)
        .map(|id| format!("{{\"id\":{id},\"op\":\"adapters\"}}\n"))
        .collect();
    let (done, finished) = std::sync::mpsc::channel();
    std::thread::spawn(move || {
        let served = serve(
            &hub,
            std::io::Cursor::new(input.into_bytes()),
            Stalled,
            &StdioOptions::default(),
        );
        let _ = done.send(served);
    });
    let served = finished
        .recv_timeout(oap_serve::stdio::SHUTDOWN_WINDOW * 3)
        .expect("serve stalled on a full output queue");
    assert!(
        matches!(served, Err(oap_serve::stdio::StdioError::RequestsDropped)),
        "{served:?}"
    );
}
