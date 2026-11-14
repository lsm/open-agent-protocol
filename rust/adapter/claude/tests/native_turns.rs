use oxap_adapter_claude::native::{new_user_turn, validate_turn_uuid};

const TURN_UUID: &str = "aaaa1111-0000-4000-8000-000000000001";

#[test]
fn a_composed_user_turn_is_the_frame_go_marshals() {
    assert_eq!(
        new_user_turn(TURN_UUID, "hello one", true).unwrap(),
        r#"{"client_composed":true,"message":{"content":"hello one","role":"user"},"origin":{"kind":"human"},"parent_tool_use_id":null,"session_id":"default","type":"user","uuid":"aaaa1111-0000-4000-8000-000000000001"}"#
    );
}

#[test]
fn a_turn_the_cli_may_expand_carries_no_client_composed_member() {
    assert_eq!(
        new_user_turn(TURN_UUID, "hello one", false).unwrap(),
        r#"{"message":{"content":"hello one","role":"user"},"origin":{"kind":"human"},"parent_tool_use_id":null,"session_id":"default","type":"user","uuid":"aaaa1111-0000-4000-8000-000000000001"}"#
    );
}

#[test]
fn a_user_turn_escapes_its_text_as_go_does() {
    assert_eq!(
        new_user_turn("u", "a <b> & \"c\"\n\u{2028}", false).unwrap(),
        r#"{"message":{"content":"a <b> & \"c\"\n ","role":"user"},"origin":{"kind":"human"},"parent_tool_use_id":null,"session_id":"default","type":"user","uuid":"u"}"#
    );
}

#[test]
fn a_turn_uuid_is_one_to_one_hundred_twenty_eight_alphanumerics_or_hyphens() {
    for good in [TURN_UUID, "ABC-123", "a", &"a".repeat(128)] {
        assert_eq!(validate_turn_uuid(good), Ok(()), "{good}");
    }
    for long in ["", &"a".repeat(129)] {
        assert_eq!(
            validate_turn_uuid(long).unwrap_err().to_string(),
            "claude native: invalid frame for a known type: turn uuid must be 1-128 bytes",
            "{long}"
        );
    }
    for bad in [
        "sp ace-uuid",
        "uuid/with/slashes",
        "uuid_with_underscore",
        "caf\u{e9}",
    ] {
        assert_eq!(
            validate_turn_uuid(bad).unwrap_err().to_string(),
            "claude native: invalid frame for a known type: turn uuid must be alphanumeric with hyphens",
            "{bad}"
        );
    }
    assert_eq!(
        validate_turn_uuid(&"\u{e9}".repeat(65))
            .unwrap_err()
            .to_string(),
        "claude native: invalid frame for a known type: turn uuid must be 1-128 bytes"
    );
}
