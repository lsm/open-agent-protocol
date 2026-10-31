mod support;

use futures::executor::block_on;
use oap_protocol::{
    CompactionKind, CompactionPolicy, Envelope, EnvelopeType, SessionSettingsUpdateRequest,
};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

fn opened() -> Hub {
    let hub = Hub::new(Registry::default_registry());
    block_on(hub.open(
        "memory",
        oap_adapter::OpenRequest {
            session_id: "s".into(),
            ..oap_adapter::OpenRequest::default()
        },
    ))
    .unwrap();
    hub
}

fn settings_line(id: i64, request: &SessionSettingsUpdateRequest) -> String {
    let mut envelope = Envelope::new(
        EnvelopeType::SESSION_SETTINGS_UPDATE_REQUEST,
        format!("settings-{id}"),
        request,
    )
    .unwrap();
    envelope.session_id = Some("s".into());
    format!(
        r#"{{"id":{id},"op":"settings","session_id":"s","request":{}}}"#,
        serde_json::to_string(&envelope).unwrap()
    )
}

fn tokens(session: &str) -> SessionSettingsUpdateRequest {
    SessionSettingsUpdateRequest {
        session_id: session.into(),
        compaction_policy: Some(CompactionPolicy {
            kind: CompactionKind::TOKENS,
            tokens: 1,
            ..CompactionPolicy::default()
        }),
        ..SessionSettingsUpdateRequest::default()
    }
}

fn lines(hub: &Hub, input: &[String]) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        std::io::Cursor::new(input.to_owned().into_bytes()),
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
fn settings_answers_the_update_in_reply_to_its_request() {
    let hub = opened();
    let answered = lines(&hub, &[settings_line(1, &tokens("s"))]);
    let response = &answered[0]["result"];
    assert_eq!(
        response["type"], "session.settings.update.response",
        "{}",
        answered[0]
    );
    assert_eq!(response["in_reply_to"], "settings-1");
    assert_eq!(response["session_id"], "s");
    assert_eq!(response["payload"]["compaction_policy"]["kind"], "tokens");
}

#[test]
fn a_settings_update_naming_no_setting_or_another_session_is_refused() {
    let hub = opened();
    let answered = lines(
        &hub,
        &[
            settings_line(
                1,
                &SessionSettingsUpdateRequest {
                    session_id: "s".into(),
                    ..SessionSettingsUpdateRequest::default()
                },
            ),
            settings_line(2, &tokens("elsewhere")),
            r#"{"id":3,"op":"settings","session_id":"s","adapter":"memory"}"#.to_owned(),
        ],
    );
    assert_eq!(
        answered[0]["error"]["code"], "schema_invalid",
        "{}",
        answered[0]
    );
    let empty = SessionSettingsUpdateRequest {
        session_id: "s".into(),
        ..SessionSettingsUpdateRequest::default()
    };
    assert!(matches!(
        block_on(hub.session(&"s".into()).unwrap().update_settings(empty)),
        Err(oap_serve::HubError::Adapter(
            oap_adapter::Error::InvalidSubmission
        ))
    ));
    assert_eq!(
        answered[1]["error"]["code"], "scope_mismatch",
        "{}",
        answered[1]
    );
    assert_eq!(answered[2]["error"]["code"], "invalid_request");
}
