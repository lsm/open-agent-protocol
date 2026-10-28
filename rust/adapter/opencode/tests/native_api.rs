use std::collections::BTreeMap;

use oap_adapter_opencode::native::{
    decode_api_error, valid_delivery, valid_event_id, valid_message_id, valid_session_id, ApiError,
    Error, SessionInfo,
};

#[test]
fn identifiers_need_their_prefix_and_a_nonempty_word_suffix() {
    assert!(valid_session_id("ses_1"));
    assert!(valid_session_id("sesAbc-9"));
    assert!(!valid_session_id("ses"));
    assert!(!valid_session_id("ses_1/2"));
    assert!(valid_message_id("msg_a"));
    assert!(!valid_message_id("msga"));
    assert!(valid_event_id("evt_1"));
    assert!(!valid_event_id("evt_"));
    assert!(valid_delivery("steer") && valid_delivery("queue"));
    assert!(!valid_delivery("start"));
}

#[test]
fn an_api_error_keeps_its_tag_and_every_other_member_and_names_its_status() {
    let error = decode_api_error(
        404,
        br#"{"_tag":"SessionNotFoundError","sessionID":"ses_1","detail":{"a":1}}"#,
    );
    assert_eq!(error.tag, "SessionNotFoundError");
    assert_eq!(
        error.fields,
        BTreeMap::from([
            ("detail".to_owned(), r#"{"a":1}"#.to_owned()),
            ("sessionID".to_owned(), r#""ses_1""#.to_owned()),
        ])
    );
    assert!(error.is_session_not_found() && !error.is_conflict());
    assert_eq!(
        error.to_string(),
        "opencode native: HTTP 404 SessionNotFoundError"
    );
    assert_eq!(
        Error::Api(error).to_string(),
        "opencode native: HTTP 404 SessionNotFoundError"
    );
}

#[test]
fn a_body_that_is_not_a_json_object_is_an_untagged_api_error() {
    for body in [&b"gateway timeout"[..], b"[1]", b"{\"_tag\":5}", b""] {
        let error = decode_api_error(502, body);
        assert_eq!(error.tag, "", "{body:?}");
        assert_eq!(error.to_string(), "opencode native: HTTP 502");
    }
    let conflicts = ["ConflictError", "PromptConflictError"].map(|tag| ApiError {
        status: 409,
        tag: tag.into(),
        ..ApiError::default()
    });
    assert!(conflicts.iter().all(ApiError::is_conflict));
}

#[test]
fn session_info_needs_an_id_a_project_and_nonnegative_times_and_refuses_an_unknown_member() {
    let info: SessionInfo = serde_json::from_str(
        r#"{"id":"ses_1","projectID":"p","title":"t","time":{"created":1,"updated":2},"location":{"directory":"/w"}}"#,
    )
    .unwrap();
    assert_eq!(info.validate(), Ok(()));
    assert_eq!(info.directory(), "/w");
    for invalid in [
        r#"{"id":"bad","projectID":"p"}"#,
        r#"{"id":"ses_1"}"#,
        r#"{"id":"ses_1","projectID":"p","time":{"created":-1}}"#,
    ] {
        let info: SessionInfo = serde_json::from_str(invalid).unwrap();
        assert_eq!(
            info.validate().unwrap_err().to_string(),
            "opencode native: invalid wire payload: invalid session info",
            "{invalid}"
        );
    }
    assert!(serde_json::from_str::<SessionInfo>(r#"{"id":"ses_1","extra":1}"#).is_err());
    let unplaced: SessionInfo = serde_json::from_str(r#"{"id":"ses_1","location":null}"#).unwrap();
    assert_eq!(unplaced.directory(), "");
}
