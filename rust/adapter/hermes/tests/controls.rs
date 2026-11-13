mod support;

use std::time::Duration;

use oap_adapter::gojson::go_marshal;
use oap_adapter::testkit::drain;
use oap_adapter::{
    Error, EventStream, ReplayGap, ResumeRequest, CONTROL_UNADVERTISED, CONTROL_UNSATISFIABLE,
};
use oap_adapter_hermes::session::Session;
use oap_protocol::{
    CompactionKind, CompactionPolicy, ReasoningLevel, RunId, SessionSettingsUpdateRequest,
    FEATURE_COMPACTION_POLICY, FEATURE_SESSION_REASONING,
};
use support::{opened, opened_with_journal, submission, wait_unusable, Fake};

const STREAMING: &str = r#"{"status":"streaming"}"#;

fn events(stream: &mut EventStream) -> Vec<String> {
    drain(stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&serde_json::to_string(envelope).unwrap()))
        .collect()
}

fn json(value: &impl serde::Serialize) -> String {
    serde_json::to_string(value).unwrap()
}

fn running(fake: &std::sync::Arc<Fake>, session: &Session) -> EventStream {
    let (_, stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    stream
}

fn calls(fake: &Fake) -> Vec<String> {
    fake.calls()
        .into_iter()
        .map(|(method, params)| format!("{method} {params}"))
        .collect()
}

fn settings(session: &str, level: ReasoningLevel) -> SessionSettingsUpdateRequest {
    SessionSettingsUpdateRequest {
        session_id: session.into(),
        reasoning_level: level,
        ..SessionSettingsUpdateRequest::default()
    }
}

fn control(error: Error) -> (String, &'static str, String, String) {
    match error {
        Error::UnsupportedControl(control) => (
            control.feature,
            control.reason,
            control.field,
            control.detail,
        ),
        other => panic!("not a control refusal: {other:?}"),
    }
}

#[test]
fn a_cancel_interrupts_hermes_and_the_interruption_fails_the_run_as_go_s_does() {
    let fake = Fake::new([
        Ok(STREAMING),
        Ok(r#"{"status":"interrupted"}"#),
        Err("hermes rpc error 4009 for request 3: busy"),
    ]);
    let session = opened(&fake);
    let mut stream = running(&fake, &session);
    assert_eq!(
        json(&session.cancel(&RunId::from("run-b")).unwrap()),
        r#"{"session_id":"session","run_id":"run-b","accepted":true,"status":"cancelling"}"#
    );
    assert_eq!(
        session.cancel(&RunId::from("run-x")),
        Err(Error::RunNotFound)
    );
    assert_eq!(
        session.cancel(&RunId::from("run-b")),
        Err(Error::Harness(
            "hermes rpc error 4009 for request 3: busy".into()
        ))
    );
    fake.event(
        "message.complete",
        2,
        r#"{"text":"half","status":"interrupted"}"#,
    );
    assert_eq!(
        events(&mut stream),
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_interrupted","message":"half"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
        ]
    );
    assert_eq!(
        session.cancel(&RunId::from("run-b")),
        Err(Error::RunNotFound)
    );
    assert_eq!(
        calls(&fake),
        [
            r#"prompt.submit {"session_id":"sess0001","text":"hello"}"#,
            r#"session.interrupt {"session_id":"sess0001"}"#,
            r#"session.interrupt {"session_id":"sess0001"}"#,
        ]
    );
}

#[test]
fn a_cancel_before_the_turn_opened_names_no_run_as_go_s_does() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        assert_eq!(session.cancel(&RunId::default()), Err(Error::RunNotFound));
        assert_eq!(
            session.cancel(&RunId::from("run-b")),
            Err(Error::RunNotFound)
        );
        assert_eq!(
            session.resume(resume("run-b", 0)).err(),
            Some(Error::RunNotFound)
        );
        assert_eq!(
            session.resume(resume("", 0)).err(),
            Some(Error::RunNotFound)
        );
        assert_eq!(
            session
                .update_settings(settings("session", ReasoningLevel::LOW))
                .err(),
            Some(Error::RunActive)
        );
        fake.event("message.start", 1, "");
        let _ = submitted.join().unwrap().unwrap();
    });
    assert_eq!(
        calls(&fake),
        [r#"prompt.submit {"session_id":"sess0001","text":"hello"}"#]
    );
}

#[test]
fn a_settings_update_sets_the_level_on_an_idle_session_as_go_s_does() {
    let fake = Fake::new([
        Ok(r#"{"key":"reasoning","value":"high"}"#),
        Err("unknown reasoning value: max"),
        Ok(STREAMING),
    ]);
    let session = opened(&fake);
    let (response, state) = session
        .update_settings(settings("session", ReasoningLevel::HIGH))
        .unwrap();
    assert_eq!(
        json(&response),
        r#"{"session_id":"session","reasoning_level":"high"}"#
    );
    assert_eq!(
        json(&state),
        r#"{"session_id":"session","status":"idle","current_model_id":"hermes-test","updated_at_ms":2,"reasoning_level":"high"}"#
    );
    let (response, state) = session
        .update_settings(settings("session", ReasoningLevel::default()))
        .unwrap();
    assert_eq!(json(&response), r#"{"session_id":"session"}"#);
    assert_eq!(
        json(&state),
        r#"{"session_id":"session","status":"idle","current_model_id":"hermes-test","updated_at_ms":3,"reasoning_level":"high"}"#
    );
    assert_eq!(
        control(
            session
                .update_settings(settings("session", ReasoningLevel::MAX))
                .unwrap_err()
        ),
        (
            FEATURE_SESSION_REASONING.into(),
            CONTROL_UNSATISFIABLE,
            "reasoning_level".into(),
            "Hermes refused the reasoning level: unknown reasoning value: max".into()
        )
    );
    assert_eq!(
        session
            .update_settings(settings("other", ReasoningLevel::LOW))
            .err(),
        Some(Error::RunNotFound)
    );
    let policy = SessionSettingsUpdateRequest {
        compaction_policy: Some(CompactionPolicy {
            kind: CompactionKind::OFF,
            ..CompactionPolicy::default()
        }),
        ..settings("session", ReasoningLevel::default())
    };
    assert_eq!(
        control(session.update_settings(policy).unwrap_err()),
        (
            FEATURE_COMPACTION_POLICY.into(),
            CONTROL_UNADVERTISED,
            "compaction_policy".into(),
            String::new()
        )
    );
    let mut stream = running(&fake, &session);
    assert_eq!(
        session
            .update_settings(settings("session", ReasoningLevel::LOW))
            .err(),
        Some(Error::RunActive)
    );
    fake.event(
        "message.complete",
        2,
        r#"{"text":"ok","status":"complete"}"#,
    );
    events(&mut stream);
    assert_eq!(
        json(&session.state().unwrap()),
        r#"{"session_id":"session","status":"idle","current_model_id":"hermes-test","transcript_cursor":"2","updated_at_ms":6,"reasoning_level":"high"}"#
    );
    session.close().unwrap();
    assert_eq!(
        session
            .update_settings(settings("session", ReasoningLevel::LOW))
            .err(),
        Some(Error::SessionClosed)
    );
    assert_eq!(
        calls(&fake),
        [
            r#"config.set {"session_id":"sess0001","key":"reasoning","value":"high"}"#,
            r#"config.set {"session_id":"sess0001","key":"reasoning","value":"max"}"#,
            r#"prompt.submit {"session_id":"sess0001","text":"hello"}"#,
        ]
    );
}

fn resume(run: &str, after: u64) -> ResumeRequest {
    ResumeRequest {
        run_id: run.into(),
        after_sequence: after,
    }
}

#[test]
fn a_resume_replays_a_live_or_ended_run_from_its_cursor_as_go_s_does() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let mut stream = running(&fake, &session);
    fake.event("message.delta", 2, r#"{"text":"a"}"#);
    let (recovery, mut replay) = session.resume(resume("run-b", 1)).unwrap();
    assert_eq!(
        json(&recovery.state),
        r#"{"session_id":"session","status":"running","active_run_id":"run-b","current_model_id":"hermes-test","transcript_cursor":"2","updated_at_ms":4}"#
    );
    assert_eq!(
        (
            recovery.run_id.as_str(),
            recovery.requested_after,
            recovery.replayed_from,
            recovery.replayed_through,
            recovery.replay_gap
        ),
        ("run-b", 1, 2, 2, None)
    );
    assert_eq!(
        session.resume(resume("run-b", 3)).err(),
        Some(Error::ReplayCursorFuture)
    );
    assert_eq!(
        session.resume(resume("run-x", 0)).err(),
        Some(Error::RunNotFound)
    );
    fake.event("message.complete", 3, r#"{"text":"a","status":"complete"}"#);
    assert_eq!(
        events(&mut replay),
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"event-d","payload":{"session_id":"session","run_id":"run-b","message_id":"message-a","part":{"type":"text","text":"a"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-e","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"a"},"stop_reason":"completed"},"sequence":3,"timestamp_ms":5,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
        ]
    );
    events(&mut stream);
    let (recovery, mut replay) = session.resume(resume("run-b", 2)).unwrap();
    assert_eq!(
        json(&recovery.state),
        r#"{"session_id":"session","status":"idle","current_model_id":"hermes-test","transcript_cursor":"3","updated_at_ms":5}"#
    );
    assert_eq!(
        (
            recovery.requested_after,
            recovery.replayed_from,
            recovery.replayed_through
        ),
        (2, 3, 3)
    );
    assert_eq!(
        events(&mut replay),
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-e","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"a"},"stop_reason":"completed"},"sequence":3,"timestamp_ms":5,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
        ]
    );
    let (recovery, mut replay) = session.resume(resume("run-b", 3)).unwrap();
    assert_eq!(
        (
            recovery.requested_after,
            recovery.replayed_from,
            recovery.replayed_through
        ),
        (3, 3, 3)
    );
    assert!(events(&mut replay).is_empty());
    assert_eq!(
        session.resume(resume("run-b", 4)).err(),
        Some(Error::ReplayCursorFuture)
    );
    session.close().unwrap();
    assert_eq!(
        session.resume(resume("run-b", 0)).err(),
        Some(Error::SessionClosed)
    );
}

#[test]
fn a_resume_behind_the_journal_reports_the_gap_as_go_s_does() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened_with_journal(&fake, 2);
    let mut stream = running(&fake, &session);
    fake.event("message.delta", 2, r#"{"text":"a"}"#);
    fake.event("message.delta", 3, r#"{"text":"b"}"#);
    fake.event(
        "message.complete",
        4,
        r#"{"text":"ab","status":"complete"}"#,
    );
    events(&mut stream);
    assert_eq!(
        session.resume(resume("run-b", 0)).err(),
        Some(Error::ReplayGap(ReplayGap {
            requested_after: 0,
            oldest_available: 3,
            latest_available: 4,
        }))
    );
}

#[test]
fn a_session_whose_transport_closed_refuses_settings_as_closed_as_go_s_does() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.hang_up();
    wait_unusable(&session);
    assert_eq!(
        session
            .update_settings(settings("session", ReasoningLevel::LOW))
            .err(),
        Some(Error::SessionClosed)
    );
    assert_eq!(
        session.resume(resume("run-x", 0)).err(),
        Some(Error::RunNotFound)
    );
    assert!(fake.calls().is_empty());
}
