mod support;

use std::sync::atomic::Ordering;
use std::sync::Arc;

use oap_adapter::{Error, OpenRequest, CONTROL_UNADVERTISED, CONTROL_UNSATISFIABLE};
use oap_adapter_hermes::client::NATIVE_PROTOCOL;
use oap_protocol::{
    CompactionKind, CompactionPolicy, ReasoningLevel, SessionState, FEATURE_COMPACTION_POLICY,
    FEATURE_OPEN_REOPEN, FEATURE_SESSION_REASONING,
};
use support::{adapter, request, Factory, Fake, Plain};

const RECOVERED: &str = r#""recovery":{"recovered":true,"reason":"Hermes restored the bound conversation and reports the model it last ran under; OAP runs and cursors remain process-local"}"#;

fn json(state: &SessionState) -> String {
    serde_json::to_string(state).unwrap()
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

fn reopen(stored: &str) -> OpenRequest {
    OpenRequest {
        reopen: true,
        native_session_id: stored.into(),
        ..request()
    }
}

#[test]
fn an_open_reports_an_idle_session_bound_to_the_stored_session() {
    let fake = Fake::stored([], "st1");
    let factory = Factory::new(&fake, "sess0001");
    let session = adapter(factory.clone()).open(request()).unwrap();
    assert_eq!(
        json(&session.state().unwrap()),
        r#"{"session_id":"s","status":"idle","current_model_id":"hermes-test","updated_at_ms":42}"#
    );
    assert_eq!(session.native_session_id(), "st1");
    assert!(fake.calls().is_empty());
    assert!(factory.efforts.lock().unwrap().is_empty());
    assert_eq!(factory.starts.load(Ordering::SeqCst), 1);
}

#[test]
fn an_open_sets_the_reasoning_level_through_config_set_and_mints_a_session_id() {
    let fake = Fake::new([Ok(r#"{"key":"reasoning","value":"none"}"#)]);
    let factory = Factory::new(&fake, "sess0001");
    let session = adapter(factory.clone())
        .open(OpenRequest {
            session_id: "".into(),
            reasoning_level: ReasoningLevel::OFF,
            ..request()
        })
        .unwrap();
    assert_eq!(
        fake.calls(),
        [(
            "config.set".into(),
            r#"{"session_id":"sess0001","key":"reasoning","value":"none"}"#.into()
        )]
    );
    assert_eq!(*factory.efforts.lock().unwrap(), ["none"]);
    assert_eq!(
        json(&session.state().unwrap()),
        r#"{"session_id":"session-minted","status":"idle","current_model_id":"hermes-test","updated_at_ms":42,"reasoning_level":"off"}"#
    );
}

#[test]
fn a_level_hermes_refuses_is_unsatisfiable_and_closes_the_client() {
    let fake = Fake::new([Err("unknown reasoning value: max")]);
    let refused = adapter(Factory::new(&fake, "sess0001"))
        .open(OpenRequest {
            reasoning_level: ReasoningLevel::MAX,
            ..request()
        })
        .err()
        .unwrap();
    assert_eq!(
        control(refused),
        (
            FEATURE_SESSION_REASONING.into(),
            CONTROL_UNSATISFIABLE,
            "reasoning_level".into(),
            "Hermes refused the reasoning level: unknown reasoning value: max".into()
        )
    );
    assert_eq!(
        fake.calls(),
        [(
            "config.set".into(),
            r#"{"session_id":"sess0001","key":"reasoning","value":"max"}"#.into()
        )]
    );
    assert_eq!(fake.closes(), 1);
}

#[test]
fn a_factory_that_names_no_session_is_refused_and_its_client_closed() {
    let fake = Fake::new([]);
    let refused = adapter(Factory::new(&fake, ""))
        .open(request())
        .err()
        .unwrap();
    assert_eq!(refused, Error::Harness(NATIVE_PROTOCOL.into()));
    assert_eq!(fake.closes(), 1);
}

#[test]
fn a_compaction_policy_is_refused_before_the_gateway_starts() {
    let fake = Fake::new([]);
    let factory = Factory::new(&fake, "sess0001");
    let refused = adapter(factory.clone())
        .open(OpenRequest {
            compaction_policy: Some(CompactionPolicy {
                kind: CompactionKind::OFF,
                ..CompactionPolicy::default()
            }),
            ..request()
        })
        .err()
        .unwrap();
    assert_eq!(
        control(refused),
        (
            FEATURE_COMPACTION_POLICY.into(),
            CONTROL_UNADVERTISED,
            "compaction_policy".into(),
            String::new()
        )
    );
    assert_eq!(factory.starts.load(Ordering::SeqCst), 0);
}

#[test]
fn a_reopen_naming_no_stored_session_or_through_a_plain_factory_is_refused() {
    let fake = Fake::new([]);
    for (open, detail) in [
        (reopen("  "), "the binding names no stored session"),
        (
            reopen("st"),
            "this client factory cannot reload a stored session",
        ),
    ] {
        let refused = adapter(Arc::new(Plain(fake.clone())))
            .open(open)
            .err()
            .unwrap();
        assert_eq!(
            control(refused),
            (
                FEATURE_OPEN_REOPEN.into(),
                CONTROL_UNSATISFIABLE,
                String::new(),
                format!("Hermes could not reload its binding: {detail}")
            )
        );
    }
    let factory = Factory::reopening(&fake, Err("Hermes has no stored session \"st\""));
    let refused = adapter(factory.clone()).open(reopen("st")).err().unwrap();
    assert_eq!(
        control(refused).3,
        "Hermes could not reload its binding: Hermes has no stored session \"st\""
    );
    assert_eq!(fake.closes(), 0);
}

#[test]
fn a_reopen_reports_the_recovery_and_the_model_hermes_last_ran_under() {
    for (info, model) in [
        (r#"{"model":"m2"}"#, "m2"),
        (r#"{"model":""}"#, "hermes-test"),
        (r#"{"model":7}"#, "hermes-test"),
        ("null", "hermes-test"),
    ] {
        let fake = Fake::stored([], " st ");
        let resumed: &'static str =
            Box::leak(format!(r#"{{"session_id":"sess0002","info":{info}}}"#).into_boxed_str());
        let factory = Factory::reopening(&fake, Ok(resumed));
        let session = adapter(factory.clone()).open(reopen(" st ")).unwrap();
        assert_eq!(*factory.reopened.lock().unwrap(), [" st "], "{info}");
        assert_eq!(factory.starts.load(Ordering::SeqCst), 0, "{info}");
        assert_eq!(
            json(&session.state().unwrap()),
            format!(
                r#"{{"session_id":"s","status":"idle","current_model_id":"{model}","updated_at_ms":42,{RECOVERED}}}"#
            ),
            "{info}"
        );
    }
}

#[test]
fn a_reopen_resumed_under_no_session_is_refused_and_its_client_closed() {
    let fake = Fake::new([]);
    let factory = Factory::reopening(&fake, Ok(r#"{"session_id":""}"#));
    let refused = adapter(factory).open(reopen("st")).err().unwrap();
    assert_eq!(refused, Error::Harness(NATIVE_PROTOCOL.into()));
    assert_eq!(fake.closes(), 1);
}

#[test]
fn a_closed_session_reports_closed_and_closes_its_client_once() {
    let fake = Fake::new([]);
    *fake.close_error.lock().unwrap() = Some("exit status 3".into());
    let session = adapter(Factory::new(&fake, "sess0001"))
        .open(request())
        .unwrap();
    assert_eq!(session.close(), Err(Error::Harness("exit status 3".into())));
    assert_eq!(session.close(), Ok(()));
    assert_eq!(fake.closes(), 1);
    assert_eq!(session.state().err(), Some(Error::SessionClosed));
}
