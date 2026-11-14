mod support;

use std::time::Duration;

use oxap_adapter::{Error, OpenRequest, CONTROL_UNSATISFIABLE};
use oxap_adapter_codex_appserver::adapter::{Adapter, Config};
use oxap_adapter_codex_appserver::process::ProcessConfig;
use oxap_protocol::{
    CompactionKind, CompactionPolicy, Participant, ReasoningLevel, RecoveryMetadata, SessionStatus,
    FEATURE_OPEN_REOPEN,
};
use support::{adapter, request, Fake};

fn reopen(thread: &str) -> OpenRequest {
    OpenRequest {
        reopen: true,
        native_session_id: thread.into(),
        ..request()
    }
}

fn control(error: Error) -> (String, &'static str, String) {
    match error {
        Error::UnsupportedControl(control) => (control.feature, control.reason, control.detail),
        other => panic!("not a control refusal: {other:?}"),
    }
}

fn harness(error: Error) -> String {
    match error {
        Error::Harness(text) => text,
        other => panic!("not a harness error: {other:?}"),
    }
}

const RESUMED: &str = r#"{"thread":{"id":"t-1"},"model":"gpt-resumed","cwd":"/work/./","approvalPolicy":"never","sandbox":{"type":"readOnly"},"reasoningEffort":"none"}"#;

#[test]
fn an_open_starts_a_thread_with_the_session_settings_and_reports_an_idle_session() {
    let fake = Fake::new([Ok(r#"{"thread":{"id":"t-1"},"model":"ignored"}"#)]);
    let session = adapter(&fake, |_| {})
        .open(OpenRequest {
            reasoning_level: ReasoningLevel::HIGH,
            compaction_policy: Some(CompactionPolicy {
                kind: CompactionKind::TOKENS,
                tokens: 9000,
                ..CompactionPolicy::default()
            }),
            ..request()
        })
        .unwrap();
    assert_eq!(
        fake.calls(),
        [(
            "thread/start".into(),
            r#"{"model":"gpt-test","cwd":"/work","approvalPolicy":"never","sandbox":"read-only","config":{"model_auto_compact_token_limit":9000,"model_reasoning_effort":"high"}}"#.into()
        )]
    );
    assert_eq!(session.native_session_id(), "t-1");
    let state = session.state().unwrap();
    assert_eq!(state.session_id.as_str(), "s");
    assert_eq!(state.status, SessionStatus::IDLE);
    assert_eq!(state.current_model_id, "gpt-test");
    assert_eq!(state.updated_at_ms, 42);
    assert_eq!(state.reasoning_level, ReasoningLevel::HIGH);
    assert_eq!(state.compaction_policy.unwrap().tokens, 9000);
    assert_eq!(state.recovery, None);
    assert_eq!(fake.closes(), 0);
}

#[test]
fn an_open_without_settings_sends_no_config_and_mints_a_session_id() {
    let fake = Fake::new([Ok(r#"{"thread":{"id":"t-1"}}"#)]);
    let session = adapter(&fake, |config| {
        config.approval_policy.clear();
        config.sandbox.clear();
    })
    .open(OpenRequest {
        session_id: "".into(),
        ..request()
    })
    .unwrap();
    assert_eq!(
        fake.calls(),
        [(
            "thread/start".into(),
            r#"{"model":"gpt-test","cwd":"/work"}"#.into()
        )]
    );
    assert_eq!(
        session.state().unwrap().session_id.as_str(),
        "session-minted"
    );
}

#[test]
fn an_open_without_a_participant_starts_no_client() {
    let fake = Fake::new([]);
    let refused = adapter(&fake, |_| {})
        .open(OpenRequest {
            participant: Participant::default(),
            ..request()
        })
        .err()
        .unwrap();
    assert!(matches!(refused, Error::InvalidParticipant), "{refused:?}");
    assert!(fake.calls().is_empty());
}

#[test]
fn a_failed_or_empty_thread_start_closes_the_client() {
    let fake = Fake::new([Err("no model")]);
    let text = harness(adapter(&fake, |_| {}).open(request()).err().unwrap());
    assert_eq!(
        text,
        "start Codex thread: codex app-server rpc error -32600 for request 1: no model"
    );
    assert_eq!(fake.closes(), 1);
    let fake = Fake::new([Ok(r#"{"thread":{}}"#)]);
    let text = harness(adapter(&fake, |_| {}).open(request()).err().unwrap());
    assert_eq!(
        text,
        "codex app-server adapter: invalid native protocol observation: thread/start returned no thread id"
    );
    assert_eq!(fake.closes(), 1);
}

#[test]
fn a_configured_thread_is_resumed_under_its_model_and_effort() {
    let fake = Fake::new([Ok(RESUMED)]);
    let session = adapter(&fake, |config| config.resume_thread_id = "t-1".into())
        .open(request())
        .unwrap();
    assert_eq!(
        fake.calls(),
        [(
            "thread/resume".into(),
            r#"{"threadId":"t-1","cwd":"/work","approvalPolicy":"never","sandbox":"read-only"}"#
                .into()
        )]
    );
    let state = session.state().unwrap();
    assert_eq!(state.current_model_id, "gpt-resumed");
    assert_eq!(state.reasoning_level, ReasoningLevel::OFF);
    assert_eq!(state.recovery, None);
}

#[test]
fn a_reopen_resumes_the_bound_thread_and_reports_recovery() {
    let fake = Fake::new([Ok(RESUMED)]);
    let session = adapter(&fake, |config| config.resume_thread_id = "other".into())
        .open(OpenRequest {
            reasoning_level: ReasoningLevel::LOW,
            ..reopen("t-1")
        })
        .unwrap();
    assert_eq!(
        fake.calls(),
        [(
            "thread/resume".into(),
            r#"{"threadId":"t-1","cwd":"/work","approvalPolicy":"never","sandbox":"read-only","config":{"model_reasoning_effort":"low"}}"#.into()
        )]
    );
    let state = session.state().unwrap();
    assert_eq!(state.reasoning_level, ReasoningLevel::LOW);
    assert_eq!(
        state.recovery,
        Some(RecoveryMetadata {
            recovered: true,
            ..RecoveryMetadata::default()
        })
    );
}

#[test]
fn a_reopen_without_a_bound_thread_or_that_codex_cannot_load_is_unsatisfiable() {
    let fake = Fake::new([]);
    let (feature, reason, detail) = control(adapter(&fake, |_| {}).open(reopen("")).err().unwrap());
    assert_eq!(
        (feature.as_str(), reason, detail.as_str()),
        (
            FEATURE_OPEN_REOPEN,
            CONTROL_UNSATISFIABLE,
            "no Codex thread is bound to this session"
        )
    );
    assert_eq!(fake.closes(), 1);
    let fake = Fake::new([Err("thread not found")]);
    let (feature, _, detail) = control(adapter(&fake, |_| {}).open(reopen("t-1")).err().unwrap());
    assert_eq!(feature, FEATURE_OPEN_REOPEN);
    assert_eq!(
        detail,
        "Codex could not load the bound thread: codex app-server rpc error -32600 for request 1: thread not found"
    );
    assert_eq!(fake.closes(), 1);
}

#[test]
fn a_resume_that_answers_another_thread_is_a_native_protocol_violation() {
    let fake = Fake::new([Ok(r#"{"thread":{"id":"t-2"}}"#)]);
    let text = harness(
        adapter(&fake, |config| config.resume_thread_id = "t-1".into())
            .open(request())
            .err()
            .unwrap(),
    );
    assert_eq!(
        text,
        r#"codex app-server adapter: invalid native protocol observation: thread/resume returned unexpected thread id "t-2""#
    );
    assert_eq!(fake.closes(), 1);
}

#[test]
fn a_resume_under_other_host_permissions_is_refused() {
    for (configure, answer, detail) in [
        (
            (|config: &mut Config| config.sandbox = "custom".into()) as fn(&mut Config),
            RESUMED,
            "the configured sandbox custom has no thread/resume policy the adapter can confirm",
        ),
        (
            |_| {},
            r#"{"thread":{"id":"t-1"},"cwd":"/work","approvalPolicy":"never","sandbox":{"type":"workspaceWrite"}}"#,
            r#"Codex resumed the thread under sandbox {"type":"workspaceWrite"}, not the configured read-only"#,
        ),
        (
            |_| {},
            r#"{"thread":{"id":"t-1"},"cwd":"/work","sandbox":{"type":"readOnly"}}"#,
            "Codex resumed the thread under approval policy , not the configured never",
        ),
        (
            |_| {},
            r#"{"thread":{"id":"t-1"},"cwd":"/work/../elsewhere","approvalPolicy":"never","sandbox":{"type":"readOnly"}}"#,
            r#"Codex resumed the thread in "/work/../elsewhere", not the configured "/work""#,
        ),
    ] {
        let fake = Fake::new([Ok(answer)]);
        let (feature, reason, refused) =
            control(adapter(&fake, configure).open(reopen("t-1")).err().unwrap());
        assert_eq!(
            (feature.as_str(), reason),
            (FEATURE_OPEN_REOPEN, CONTROL_UNSATISFIABLE)
        );
        assert_eq!(refused, detail);
        assert_eq!(fake.closes(), 1);
        let fake = Fake::new([Ok(answer)]);
        let text = harness(
            adapter(&fake, |config| {
                configure(config);
                config.resume_thread_id = "t-1".into();
            })
            .open(request())
            .err()
            .unwrap(),
        );
        assert_eq!(text, format!("resume Codex thread: {detail}"));
    }
}

#[test]
fn closing_a_session_closes_its_client_once() {
    let fake = Fake::new([Ok(r#"{"thread":{"id":"t-1"}}"#)]);
    let session = adapter(&fake, |_| {}).open(request()).unwrap();
    session.close().unwrap();
    session.close().unwrap();
    assert_eq!(fake.closes(), 1);
    assert!(matches!(session.state(), Err(Error::SessionClosed)));
}

#[cfg(unix)]
#[test]
fn the_default_factory_launches_codex_with_the_host_client_info() {
    let dir = std::env::temp_dir().join(format!("oap-codex-open-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    let path = dir.to_string_lossy().into_owned();
    let script = r#"read request; printf '%s\n' "$request" > initialize; printf '%s\n' '{"id":0,"result":{}}'; read notice; read start; printf '%s\n' "$start" > start; printf '%s\n' '{"id":1,"result":{"thread":{"id":"t-1"}}}'; cat >/dev/null"#;
    let session = Adapter::new(Config {
        process: ProcessConfig {
            path: "/bin/sh".into(),
            args: vec!["-c".into(), script.into(), "codex".into()],
            dir: path.clone(),
            env: Some(vec!["PATH=/usr/bin:/bin".into()]),
            shutdown_timeout: Some(Duration::from_secs(2)),
            ..ProcessConfig::default()
        },
        model: "gpt-test".into(),
        ..Config::default()
    })
    .unwrap()
    .open(request())
    .unwrap();
    assert_eq!(session.native_session_id(), "t-1");
    session.close().unwrap();
    assert_eq!(
        std::fs::read_to_string(dir.join("initialize")).unwrap(),
        "{\"id\":0,\"method\":\"initialize\",\"params\":{\"clientInfo\":{\"name\":\"open-agent-protocol\",\"version\":\"0.1\"}}}\n"
    );
    assert_eq!(
        std::fs::read_to_string(dir.join("start")).unwrap(),
        format!("{{\"id\":1,\"method\":\"thread/start\",\"params\":{{\"model\":\"gpt-test\",\"cwd\":{}}}}}\n", serde_json::to_string(&path).unwrap())
    );
    let _ = std::fs::remove_dir_all(&dir);
}
