use oap_adapter_hermes::native::{
    decode_server_request, decode_strict, request_session_id, ApprovalResult, ServerRequest,
    SessionInfoPayload, SubagentPayload,
};

fn refused(detail: &str) -> String {
    format!("hermes native: invalid pinned message: {detail}")
}

fn error(method: &str, params: &str) -> String {
    outcome(method, params).unwrap_err()
}

fn outcome(method: &str, params: &str) -> Result<Option<ServerRequest>, String> {
    decode_server_request(method, params.as_bytes())
}

#[test]
fn an_approval_decodes_leniently_and_needs_known_choices() {
    let Ok(Some(ServerRequest::Approval(request))) = outcome(
        "approval",
        r#"{"session_id":"s","request_id":"r","command":"rm","choices":["once","deny"],"future":1}"#,
    ) else {
        panic!("approval refused");
    };
    assert_eq!(request.choices, ["once", "deny"]);
    assert!(matches!(
        outcome(
            "approval",
            r#"{"session_id":"s","request_id":"r","description":"d","choices":["once"]}"#
        ),
        Ok(Some(ServerRequest::Approval(_)))
    ));
    assert_eq!(
        outcome(
            "approval",
            r#"{"session_id":"s","request_id":"r","command":"rm","choices":["once",null]}"#
        )
        .unwrap_err(),
        "hermes native: invalid pinned message: approval choice \"\""
    );
    assert_eq!(
        outcome("approval", r#"{"session_id":"s","request_id":"r","choices":["once"]}"#).unwrap_err(),
        "hermes native: invalid pinned message: approval request requires session_id, request_id, a command and choices"
    );
    assert_eq!(
        outcome("approval", "[]").unwrap_err(),
        "hermes native: invalid pinned message: invalid approval request: json: cannot unmarshal array into Go value of type native.ApprovalRequestParams"
    );
}

#[test]
fn a_clarify_batch_decodes_strictly_and_needs_identified_questions() {
    let Ok(Some(ServerRequest::Clarify(request))) = outcome(
        "clarify",
        r#"{"session_id":"s","questions":[{"qid":"q","question":"why?","choices":null,"multi_select":null}],"answers":{"q":null}}"#,
    ) else {
        panic!("clarify refused");
    };
    assert_eq!(request.questions[0].qid, "q");
    assert_eq!(request.answers.unwrap().get("q"), Some(&None));
    assert!(outcome("clarify", r#"{"session_id":"s","questions":[],"extra":1}"#)
        .unwrap_err()
        .starts_with("hermes native: invalid pinned message: invalid clarify request: unknown field `extra`"));
    assert_eq!(
        error("clarify", r#"{"session_id":"s","questions":null}"#),
        refused("clarify request needs a session and at least one question")
    );
    assert_eq!(
        error("clarify", r#"{"session_id":"s","questions":[null]}"#),
        refused("invalid clarify batch question")
    );
}

#[test]
fn sudo_and_secret_requests_need_their_identity() {
    assert!(matches!(
        outcome("sudo", r#"{"session_id":"s"}"#),
        Ok(Some(ServerRequest::Sudo(_)))
    ));
    assert_eq!(
        error("sudo", r#"{"session_id":"s","x":1}"#),
        refused("invalid sudo request")
    );
    assert!(matches!(
        outcome(
            "secret",
            r#"{"session_id":"s","env_var":"E","prompt":"p","metadata":{"m":1}}"#
        ),
        Ok(Some(ServerRequest::Secret(_)))
    ));
    assert_eq!(
        error("secret", r#"{"session_id":"s","env_var":"E"}"#),
        refused("invalid secret request")
    );
    assert!(matches!(outcome("unknown", "{}"), Ok(None)));
}

#[test]
fn the_request_session_is_read_leniently_and_absent_on_any_failure() {
    assert_eq!(
        request_session_id(br#"{"session_id":"a","session_id":"b","x":1}"#),
        "b"
    );
    assert_eq!(request_session_id(br#"{"session_id":7}"#), "");
    assert_eq!(request_session_id(b"null"), "");
    assert_eq!(request_session_id(b"{"), "");
}

#[test]
fn payloads_follow_their_pinned_strictness() {
    let info: SessionInfoPayload = decode_strict(
        br#"{"model":"m","provider":"p","running":true,"turn_started_at":1.5,"future":1}"#,
    )
    .unwrap();
    assert_eq!(info.turn_started_at, Some(1.5));
    let subagent: SubagentPayload = decode_strict(
        br#"{"goal":"g","task_count":2,"toolsets":["a",null],"duration_seconds":0.5}"#,
    )
    .unwrap();
    assert_eq!(subagent.toolsets, ["a", ""]);
    assert!(decode_strict::<SubagentPayload>(br#"{"goal":"g","future":1}"#).is_err());
    assert_eq!(
        serde_json::to_string(&ApprovalResult {
            choice: "once".into()
        })
        .unwrap(),
        r#"{"choice":"once"}"#
    );
}
