use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode, RunStatus,
    ShutdownMode, ShutdownRun,
};
use oap_serve::{asked_shutdown, Hub, Registry};
use serde_json::value::RawValue;

fn raw(text: &str) -> Box<RawValue> {
    RawValue::from_string(text.to_owned()).unwrap()
}

fn running(hub: &Hub, id: &str) -> oap_protocol::RunId {
    let opened = block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: id.into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    block_on(opened.session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: id.into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
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

#[test]
fn a_shutdown_request_names_graceful_force_or_no_mode_and_nothing_else() {
    assert_eq!(asked_shutdown(None), Ok(None));
    assert_eq!(asked_shutdown(Some(&raw("null"))), Ok(None));
    assert_eq!(asked_shutdown(Some(&raw("{}"))), Ok(None));
    assert_eq!(asked_shutdown(Some(&raw(r#"{"mode":null}"#))), Ok(None));
    assert_eq!(
        asked_shutdown(Some(&raw(r#"{"mode":"graceful"}"#))),
        Ok(Some(ShutdownMode::GRACEFUL))
    );
    assert_eq!(
        asked_shutdown(Some(&raw(r#"{"mode":"force"}"#))),
        Ok(Some(ShutdownMode::FORCE))
    );
    for refused in [r#"{"mode":"later"}"#, r#"{"other":1}"#, "[]", "1"] {
        assert!(asked_shutdown(Some(&raw(refused))).is_err(), "{refused}");
    }
}

#[test]
fn nonterminal_runs_names_every_sessions_unfinished_run_and_no_idle_one() {
    let hub = Hub::new(Registry::default_registry());
    let run = running(&hub, "busy");
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: "idle".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
    let pending = block_on(hub.session(&"busy".into()).unwrap().state())
        .unwrap()
        .active_runs[0]
        .pending_interactions
        .clone();
    let runs = block_on(hub.nonterminal_runs());
    assert_eq!(
        runs,
        [ShutdownRun {
            session_id: "busy".into(),
            run_id: run,
            status: RunStatus::RUNNING,
            pending_interactions: pending,
        }]
    );
}

#[test]
fn cancel_queued_reports_whether_the_cancel_was_accepted() {
    let hub = Hub::new(Registry::default_registry());
    let run = running(&hub, "busy");
    let named = ShutdownRun {
        session_id: "busy".into(),
        run_id: run,
        ..ShutdownRun::default()
    };
    assert!(block_on(hub.cancel_queued(&named)));
    let ghost = ShutdownRun {
        session_id: "ghost".into(),
        ..named
    };
    assert!(!block_on(hub.cancel_queued(&ghost)));
}
