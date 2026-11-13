mod support;

use std::time::Duration;

use oap_adapter::testkit::{drain, RunTrace};
use oap_adapter::{Error, EventStream};
use oap_adapter_hermes::rpc::RequestId;
use oap_protocol::{
    ContentPart, ContentPartType, MessageContent, MessageRole, MessageSubmitRequest,
    MessageSubmitResponse, RequestedDeliveryMode,
};
use support::{go_adapter, opened, submission, wait_unusable, Fake};

const ADMITTED: &str = r#"{"session_id":"session","accepted":true,"submission_id":"message-a","requested_delivery":"auto","effective_delivery":"start","delivery_resolution":"session_idle","admission":"started","run_id":"run-b","status":"running","model_id":"hermes-test","message_ids":["message-a"]}"#;
const STARTED: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#;
const OPENED_TWICE: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_grammar","message":"turn opened twice"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#;
const SEQUENCE_GAP: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_external_activity","message":"non-contiguous seq 5 after 1"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#;
const FOREIGN_SESSION: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_external_activity","message":"event for foreign session \"other123\""}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#;
const PROCESS_EXIT: &str = r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_process_exit","message":"EOF"},"settled_by":"inferred"},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#;
const RUNNING: &str = r#"{"session_id":"session","status":"running","active_run_id":"run-b","current_model_id":"hermes-test","transcript_cursor":"1","updated_at_ms":3}"#;
const STREAMING: &str = r#"{"status":"streaming"}"#;

fn json(value: &impl serde::Serialize) -> String {
    serde_json::to_string(value).unwrap()
}

fn events(stream: &mut EventStream) -> Vec<String> {
    drain(stream, Duration::from_secs(5))
        .iter()
        .map(json)
        .collect()
}

fn harness(text: &str) -> Error {
    Error::Harness(text.into())
}

fn admit(
    fake: &Fake,
    session: &oap_adapter_hermes::session::Session,
    response_first: bool,
) -> (MessageSubmitResponse, EventStream) {
    if !response_first {
        fake.event_ahead_of_next_reply("message.start", 1, "");
    }
    fake.reply(Ok(STREAMING));
    std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        if response_first {
            fake.event("message.start", 1, "");
        }
        submitted.join().unwrap().unwrap()
    })
}

#[test]
fn an_admitted_run_starts_once_hermes_opens_the_turn_and_a_second_open_fails_it() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.raw(r#"{"type":"message.start"}"#);
    let (admission, mut stream) = admit(&fake, &session, true);
    assert_eq!(json(&admission), ADMITTED);
    assert_eq!(
        fake.calls(),
        [(
            "prompt.submit".into(),
            r#"{"session_id":"sess0001","text":"hello"}"#.into()
        )]
    );
    assert_eq!(json(&session.state().unwrap()), RUNNING);
    assert_eq!(session.submit(submission()).err(), Some(Error::RunActive));
    assert_eq!(fake.count("prompt.submit"), 1);
    assert_eq!(session.close(), Err(Error::RunActive));
    fake.event("message.start", 2, "");
    assert_eq!(events(&mut stream), [STARTED, OPENED_TWICE]);
    assert_eq!(
        session.submit(submission()).err(),
        Some(harness("fake client: no scripted reply for prompt.submit"))
    );
}

#[test]
fn a_turn_opened_before_the_reply_starts_the_same_run_and_a_sequence_gap_fails_it() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    let (admission, mut stream) = admit(&fake, &session, false);
    assert_eq!(json(&admission), ADMITTED);
    fake.event("message.delta", 5, r#"{"text":"skipped"}"#);
    assert_eq!(events(&mut stream), [STARTED, SEQUENCE_GAP]);
    assert_eq!(
        session.submit(submission()).err(),
        Some(Error::SessionClosed)
    );
}

#[test]
fn an_event_for_another_session_fails_the_running_turn() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    let (_, mut stream) = admit(&fake, &session, true);
    fake.raw(r#"{"type":"message.delta","session_id":"other123","seq":2,"payload":{"text":"x"}}"#);
    assert_eq!(events(&mut stream), [STARTED, FOREIGN_SESSION]);
}

fn failed(message: &str) -> String {
    OPENED_TWICE.replace(
        r#""code":"hermes_invalid_grammar","message":"turn opened twice""#,
        &format!(
            r#""code":"hermes_external_activity","message":{}"#,
            json(&message)
        ),
    )
}

#[test]
fn a_server_request_the_running_turn_cannot_own_fails_it_unanswered() {
    for (id, params, message) in [
        (
            RequestId::String("r1".into()),
            r#"{"session_id":"other"}"#,
            r#"approval request for foreign session "other""#,
        ),
        (
            RequestId::Integer(7),
            r#"{"session_id":"sess0001"}"#,
            "approval request with a non-string id",
        ),
    ] {
        let fake = Fake::new([]);
        let session = opened(&fake);
        let (_, mut stream) = admit(&fake, &session, true);
        let answers = fake.request(id, "approval", params);
        assert_eq!(events(&mut stream), [STARTED.to_owned(), failed(message)]);
        assert!(answers.codes().is_empty(), "{message}");
    }
}

#[test]
fn an_unmapped_server_request_during_a_run_is_answered_and_the_run_continues() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    let (_, _stream) = admit(&fake, &session, true);
    let answers = fake.request(
        RequestId::String("r2".into()),
        "session.weird",
        r#"{"session_id":"sess0001"}"#,
    );
    assert_eq!(answers.codes(), [-32601]);
    assert_eq!(json(&session.state().unwrap()), RUNNING);
}

#[test]
fn a_status_other_than_streaming_or_a_failed_call_refuses_the_submission() {
    for (reply, want) in [
        (
            Ok(r#"{"status":"steered"}"#),
            r#"hermes adapter: invalid native protocol observation: prompt.submit returned status "steered""#,
        ),
        (
            Ok(r#"{}"#),
            r#"hermes adapter: invalid native protocol observation: prompt.submit returned status """#,
        ),
        (
            Ok(r#"{"status":"weird"}"#),
            r#"hermes adapter: invalid native protocol observation: prompt.submit returned status "weird""#,
        ),
        (Err("boom"), "boom"),
    ] {
        let fake = Fake::new([reply, Err("again")]);
        let session = opened(&fake);
        assert_eq!(session.submit(submission()).err(), Some(harness(want)));
        assert_eq!(
            json(&session.state().unwrap().status),
            r#""idle""#,
            "{want}"
        );
        assert_eq!(
            session.submit(submission()).err(),
            Some(harness("again")),
            "{want}"
        );
    }
}

#[test]
fn an_unsolicited_turn_or_a_closed_transport_refuses_the_next_submission() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.event("message.start", 1, "");
    assert_eq!(
        session.submit(submission()).err(),
        Some(Error::SessionClosed)
    );
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.hang_up();
    wait_unusable(&session);
    assert_eq!(
        session.submit(submission()).err(),
        Some(Error::SessionClosed)
    );
    assert_eq!(fake.count("prompt.submit"), 0);
}

#[test]
fn a_transport_that_dies_after_the_turn_opened_fails_the_run_as_a_process_exit() {
    for close_before_reply in [true, false] {
        let fake = Fake::new([Ok(STREAMING)]);
        let adapter = go_adapter(&fake);
        let session = adapter
            .open(oap_adapter::OpenRequest {
                session_id: "session".into(),
                ..support::request()
            })
            .unwrap();
        let release = fake.hold_next_reply();
        let (admission, mut stream) = std::thread::scope(|scope| {
            let submitted = scope.spawn(|| session.submit(submission()));
            fake.await_call("prompt.submit", 1);
            fake.event("message.start", 1, "");
            if close_before_reply {
                fake.hang_up();
                wait_unusable(&session);
            }
            release.send(()).unwrap();
            submitted.join().unwrap().unwrap()
        });
        assert_eq!(json(&admission), ADMITTED);
        if !close_before_reply {
            fake.hang_up();
        }
        let traced = drain(&mut stream, Duration::from_secs(5));
        assert_eq!(
            traced.iter().map(json).collect::<Vec<_>>(),
            [STARTED, PROCESS_EXIT],
            "{close_before_reply}"
        );
        RunTrace {
            submitted: Some(submission()),
            ..RunTrace::new(admission, adapter.describe(), traced)
        }
        .assert_valid();
        assert_eq!(
            session.submit(submission()).err(),
            Some(Error::SessionClosed)
        );
    }
}

#[test]
fn a_transport_that_dies_before_the_turn_opened_refuses_the_submission() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let release = fake.hold_next_reply();
    let refused = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.hang_up();
        wait_unusable(&session);
        release.send(()).unwrap();
        submitted.join().unwrap().err()
    });
    assert_eq!(
        refused,
        Some(harness(
            "hermes adapter: invalid native protocol observation: EOF"
        ))
    );
}

#[test]
fn a_transport_that_dies_after_the_reply_but_before_the_turn_opened_refuses_the_submission() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let refused = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        std::thread::sleep(Duration::from_millis(50));
        fake.hang_up();
        submitted.join().unwrap().err()
    });
    assert_eq!(
        refused,
        Some(harness(
            "hermes adapter: invalid native protocol observation: EOF"
        ))
    );
}

#[test]
fn a_submission_hermes_cannot_take_is_refused_before_the_gateway_sees_it() {
    let fake = Fake::new([Err("stop"), Err("stop")]);
    let session = opened(&fake);
    let text = |text: &str| ContentPart {
        part_type: ContentPartType::TEXT,
        text: text.into(),
        ..ContentPart::default()
    };
    let mut parts = submission();
    parts.messages[0].content = MessageContent::from_parts(&[text("a"), text("b")]);
    assert_eq!(session.submit(parts).err(), Some(harness("stop")));
    assert_eq!(
        fake.calls()[0].1,
        r#"{"session_id":"sess0001","text":"a\nb"}"#
    );
    let refused = |change: fn(&mut MessageSubmitRequest)| {
        let mut request = submission();
        change(&mut request);
        session.submit(request).err().map(|error| error.to_string())
    };
    let invalid = Some("adapter: invalid submission".to_owned());
    assert_eq!(
        refused(|request| request.messages.push(request.messages[0].clone())),
        invalid
    );
    assert_eq!(
        refused(|request| request.messages[0].role = MessageRole::ASSISTANT),
        invalid
    );
    assert_eq!(refused(|request| request.session_id = "".into()), invalid);
    assert_eq!(
        refused(|request| request.delivery = RequestedDeliveryMode::QUEUE),
        Some("adapter: unsupported input: session.message.delivery.queue (unadvertised)".into())
    );
    assert_eq!(
        refused(|request| request.session_id = "other".into()),
        Some("adapter: run not found".into())
    );
    assert_eq!(
        refused(|request| request.delivery = RequestedDeliveryMode::default()),
        Some("stop".into())
    );
    assert_eq!(fake.count("prompt.submit"), 2);
}
