use oap_adapter::gojson::go_marshal;
use oap_adapter_hermes::native::{
    decode_strict, ConfigSetParams, ConfigSetResult, InterruptParams, InterruptResult,
    PromptSubmitParams, PromptSubmitResult, SessionCreateParams, SessionCreateResult,
    SessionResumeParams, SessionResumeResult,
};

fn marshal(value: &impl serde::Serialize) -> String {
    go_marshal(&serde_json::to_string(value).unwrap())
}

#[test]
fn params_marshal_as_go_marshals_them() {
    assert_eq!(marshal(&SessionCreateParams::default()), "{}");
    assert_eq!(
        marshal(&SessionCreateParams {
            title: "t".into(),
            cwd: "/w".into(),
            model: "m".into(),
            reasoning_effort: "high".into(),
        }),
        r#"{"title":"t","cwd":"/w","model":"m","reasoning_effort":"high"}"#
    );
    assert_eq!(
        marshal(&ConfigSetParams::default()),
        r#"{"session_id":"","key":"","value":""}"#
    );
    assert_eq!(
        marshal(&ConfigSetParams {
            session_id: "s".into(),
            key: "reasoning".into(),
            value: "none".into(),
        }),
        r#"{"session_id":"s","key":"reasoning","value":"none"}"#
    );
    assert_eq!(
        marshal(&SessionResumeParams {
            session_id: "k<1>".into()
        }),
        r#"{"session_id":"k\u003c1\u003e"}"#
    );
    assert_eq!(
        marshal(&PromptSubmitParams {
            session_id: "s".into(),
            text: "a&b".into()
        }),
        r#"{"session_id":"s","text":"a\u0026b"}"#
    );
    assert_eq!(
        marshal(&InterruptParams {
            session_id: "s".into()
        }),
        r#"{"session_id":"s"}"#
    );
}

#[test]
fn results_decode_leniently_as_go_s_unmarshal_does() {
    let created: SessionCreateResult = decode_strict(
        br#"{"session_id":"s","stored_session_id":"k","message_count":2,"messages":[{"a":1}],"info":{"model":"m"},"extra":true}"#,
    )
    .unwrap();
    assert_eq!(
        (
            created.session_id.as_str(),
            created.stored_session_id.as_str(),
            created.message_count
        ),
        ("s", "k", 2)
    );
    assert_eq!(created.messages.unwrap().get(), r#"[{"a":1}]"#);
    assert_eq!(created.info.unwrap().get(), r#"{"model":"m"}"#);

    let resumed: SessionResumeResult = decode_strict(
        br#"{"session_id":"s","resumed":"k","message_count":3,"info":null,"running":true,"status":"idle","auto_continue":{"x":1}}"#,
    )
    .unwrap();
    assert_eq!(
        (
            resumed.session_id.as_str(),
            resumed.resumed.as_str(),
            resumed.message_count,
            resumed.running,
            resumed.status.as_str()
        ),
        ("s", "k", 3, true, "idle")
    );
    assert_eq!(resumed.info.unwrap().get(), "null");
    assert_eq!(resumed.auto_continue.unwrap().get(), r#"{"x":1}"#);

    let submitted: PromptSubmitResult = decode_strict(
        br#"{"status":"streaming","survivor_user_row_ids":[3,null,5],"survivor_row_id_map":{"1":2},"voice_stopped":true,"turn_isolation":true}"#,
    )
    .unwrap();
    assert_eq!(submitted.status, "streaming");
    assert_eq!(submitted.survivor_user_row_ids, [Some(3), None, Some(5)]);
    assert_eq!(submitted.survivor_row_id_map.unwrap().get(), r#"{"1":2}"#);
    assert!(submitted.voice_stopped && submitted.turn_isolation);

    let interrupted: InterruptResult =
        decode_strict(br#"{"status":"ok","interrupted":true}"#).unwrap();
    assert_eq!(
        (
            interrupted.status.as_str(),
            interrupted.interrupted,
            interrupted.turn_isolation
        ),
        ("ok", true, false)
    );
    assert!(decode_strict::<InterruptResult>(br#"{"status":5}"#).is_err());

    let set: ConfigSetResult =
        decode_strict(br#"{"key":"reasoning","value":"none","extra":1}"#).unwrap();
    assert_eq!(
        (set.key.as_str(), set.value.as_str()),
        ("reasoning", "none")
    );
}
