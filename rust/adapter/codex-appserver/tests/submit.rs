mod support;

use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;

use oap_adapter::runtime::Runtime;
use oap_adapter::{Error, CONTROL_UNADVERTISED, CONTROL_UNSATISFIABLE};
use oap_adapter_codex_appserver::session::Session;
use oap_protocol::{
    Admission, ContentPart, EffectiveDeliveryMode, Message, MessageContent, MessageRole,
    MessageSubmitRequest, RequestedDeliveryMode, RunStatus, SessionStatus, FEATURE_INSTRUCTIONS,
    FEATURE_STRUCTURED_OUTPUT,
};
use serde_json::value::RawValue;
use support::{adapter, request, Fake, Reply};

type Refusal = fn(&Error) -> bool;

const STARTED: &str = r#"{"thread":{"id":"t-1"}}"#;
const TURN: &str = r#"{"turn":{"id":"turn-1","status":"inProgress"}}"#;

fn open(replies: impl IntoIterator<Item = Reply>) -> (Arc<Fake>, Session) {
    let fake = Fake::new([Ok(STARTED)].into_iter().chain(replies));
    let next = Arc::new(AtomicU64::new(0));
    let session = adapter(&fake, |config| {
        config.runtime = Runtime::new(
            || 7,
            move |kind| format!("{kind}-{}", next.fetch_add(1, Ordering::SeqCst) + 1),
        );
    })
    .open(request())
    .unwrap();
    (fake, session)
}

fn user(id: &str, text: &str) -> Message {
    Message {
        id: (!id.is_empty()).then(|| id.into()),
        role: MessageRole::USER,
        content: MessageContent::from_text(text),
        ..Message::default()
    }
}

fn submission(messages: Vec<Message>) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "s".into(),
        messages,
        ..MessageSubmitRequest::default()
    }
}

fn last_call(fake: &Fake) -> (String, String) {
    fake.calls().last().cloned().unwrap()
}

#[test]
fn a_submit_starts_a_turn_and_answers_with_the_run_it_admitted() {
    let (fake, session) = open([Ok(TURN)]);
    let (response, _stream) = session
        .submit(submission(vec![user("m-given", "hi"), user("", "there")]))
        .unwrap();
    assert_eq!(
        last_call(&fake),
        (
            "turn/start".into(),
            r#"{"threadId":"t-1","input":[{"type":"text","text":"hi"},{"type":"text","text":"there"}],"model":"gpt-test"}"#.into()
        )
    );
    assert_eq!(response.session_id.as_str(), "s");
    assert!(response.accepted);
    assert_eq!(response.submission_id.as_str(), "submission-4");
    assert_eq!(response.requested_delivery, RequestedDeliveryMode::AUTO);
    assert_eq!(response.effective_delivery, EffectiveDeliveryMode::START);
    assert_eq!(response.delivery_resolution, "session_idle");
    assert_eq!(response.admission, Admission::STARTED);
    assert_eq!(
        response.run_id.as_ref().map(|id| id.as_str()),
        Some("run-2")
    );
    assert_eq!(response.status, RunStatus::RUNNING);
    assert_eq!(response.model_id, "gpt-test");
    let ids: Vec<_> = response.message_ids.iter().map(|id| id.as_str()).collect();
    assert_eq!(ids, ["m-given", "message-1"]);
    let state = session.state().unwrap();
    assert_eq!(state.status, SessionStatus::QUEUED);
    assert_eq!(
        state.active_run_id.as_ref().map(|id| id.as_str()),
        Some("run-2")
    );
    assert_eq!(state.updated_at_ms, 7);
}

#[test]
fn a_submit_runs_under_a_requested_model_and_passes_its_output_schema() {
    let (fake, session) = open([Ok(TURN)]);
    let (response, _stream) = session
        .submit(MessageSubmitRequest {
            model_id: Some("gpt-other".into()),
            output_schema: Some(RawValue::from_string(r#"{"type":"object"}"#.into()).unwrap()),
            ..submission(vec![user("", "hi")])
        })
        .unwrap();
    assert_eq!(
        last_call(&fake).1,
        r#"{"threadId":"t-1","input":[{"type":"text","text":"hi"}],"model":"gpt-other","outputSchema":{"type":"object"}}"#
    );
    assert_eq!(response.model_id, "gpt-other");
}

#[test]
fn a_submit_go_refuses_starts_no_turn() {
    let schema = |text: &str| Some(RawValue::from_string(text.into()).unwrap());
    let cases: Vec<(MessageSubmitRequest, Refusal)> = vec![
        (
            MessageSubmitRequest {
                model_id: Some(String::new()),
                ..submission(vec![user("", "hi")])
            },
            |error| matches!(error, Error::ModelNotFound(model) if model.is_empty()),
        ),
        (
            MessageSubmitRequest {
                instructions: Some("be brief".into()),
                ..submission(vec![user("", "hi")])
            },
            |error| matches!(error, Error::UnsupportedControl(control) if control.feature == FEATURE_INSTRUCTIONS && control.reason == CONTROL_UNADVERTISED),
        ),
        (
            MessageSubmitRequest {
                output_schema: schema(r#"{"type":7}"#),
                ..submission(vec![user("", "hi")])
            },
            |error| matches!(error, Error::UnsupportedControl(control) if control.feature == FEATURE_STRUCTURED_OUTPUT && control.reason == CONTROL_UNSATISFIABLE && control.field == "output_schema" && !control.detail.is_empty()),
        ),
        (
            MessageSubmitRequest {
                session_id: "other".into(),
                ..submission(vec![user("", "hi")])
            },
            |error| matches!(error, Error::InvalidSubmission),
        ),
        (submission(vec![]), |error| {
            matches!(error, Error::InvalidSubmission)
        }),
        (
            MessageSubmitRequest {
                allow_degraded_features: vec!["user_input".into()],
                ..submission(vec![user("", "hi")])
            },
            |error| matches!(error, Error::InvalidSubmission),
        ),
        (
            MessageSubmitRequest {
                metadata: [("k".to_owned(), RawValue::from_string("1".into()).unwrap())].into(),
                ..submission(vec![user("", "hi")])
            },
            |error| matches!(error, Error::InvalidSubmission),
        ),
        (
            submission(vec![Message {
                role: MessageRole::ASSISTANT,
                ..user("", "hi")
            }]),
            |error| matches!(error, Error::UnsupportedInput),
        ),
        (
            submission(vec![Message {
                content: MessageContent::from_parts(&[ContentPart {
                    part_type: "image".into(),
                    ..ContentPart::default()
                }]),
                ..user("", "")
            }]),
            |error| matches!(error, Error::UnsupportedInput),
        ),
    ];
    for (submitted, refused) in cases {
        let (fake, session) = open([]);
        let error = session
            .submit(submitted)
            .err()
            .expect("a refused submit was admitted");
        assert!(refused(&error), "{error:?}");
        assert_eq!(fake.calls().len(), 1, "a refused submit reached Codex");
        assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    }
}

#[test]
fn a_second_submit_while_a_run_is_active_or_after_close_is_refused() {
    let (fake, session) = open([Ok(TURN)]);
    let _first = session.submit(submission(vec![user("", "hi")])).unwrap();
    assert!(matches!(
        session.submit(submission(vec![user("", "again")])),
        Err(Error::RunActive)
    ));
    assert_eq!(fake.calls().len(), 2);
    let (_, closed) = open([]);
    closed.close().unwrap();
    assert!(matches!(
        closed.submit(submission(vec![user("", "hi")])),
        Err(Error::SessionClosed)
    ));
}

#[test]
fn a_refused_turn_start_leaves_the_session_idle_unless_the_transport_failed() {
    let (_fake, session) = open([Err("busy"), Ok(TURN)]);
    let error = session
        .submit(submission(vec![user("", "hi")]))
        .err()
        .unwrap();
    assert_eq!(
        error.to_string(),
        "start Codex turn: codex app-server rpc error -32600 for request 1: busy"
    );
    let state = session.state().unwrap();
    assert_eq!(state.status, SessionStatus::IDLE);
    assert_eq!(state.active_run_id, None);
    assert!(session.submit(submission(vec![user("", "again")])).is_ok());
    let (fake, session) = open([Err("gone")]);
    *fake.failed.lock().unwrap() = Some("closed".into());
    assert!(session.submit(submission(vec![user("", "hi")])).is_err());
    assert!(matches!(session.state(), Err(Error::SessionClosed)));
}

#[test]
fn a_turn_start_without_a_turn_id_closes_the_session() {
    let (_fake, session) = open([Ok(r#"{"turn":{}}"#)]);
    let error = session
        .submit(submission(vec![user("", "hi")]))
        .err()
        .unwrap();
    assert_eq!(
        error.to_string(),
        "codex app-server adapter: invalid native protocol observation: turn/start returned no turn id"
    );
    assert!(matches!(session.state(), Err(Error::SessionClosed)));
}

#[test]
fn a_null_content_is_empty_text_as_go_reads_it() {
    let (fake, session) = open([Ok(TURN)]);
    let null: Message = serde_json::from_str(r#"{"role":"user","content":null}"#).unwrap();
    let (_response, _stream) = session.submit(submission(vec![null])).unwrap();
    assert_eq!(
        last_call(&fake).1,
        r#"{"threadId":"t-1","input":[{"type":"text"}],"model":"gpt-test"}"#
    );
}
