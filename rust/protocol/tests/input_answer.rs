use oap_protocol::{
    InputAnswer, UserInputRequestedPayload, UserInputResolveRequest, UserInputResolvedPayload,
};
use serde_json::{json, Value};

#[test]
fn text_answers_keep_empty_text_and_choice_answers_have_no_text_member() {
    for value in [
        json!({"question_id":"q", "text":""}),
        json!({"question_id":"q", "text":"é漢😀"}),
        json!({"question_id":"q", "selected_option_ids":["yes"]}),
        json!({"question_id":"q", "text":"mixed", "selected_option_ids":["yes"]}),
    ] {
        let answer: InputAnswer = serde_json::from_value(value.clone()).unwrap();
        assert_eq!(serde_json::to_value(&answer).unwrap(), value);
        let again: InputAnswer = serde_json::from_value(value).unwrap();
        assert_eq!(answer, again);
    }
}

#[test]
fn empty_text_survives_drafts_resolve_requests_and_resolved_events() {
    let answer = InputAnswer {
        question_id: "q".into(),
        ..InputAnswer::default()
    };
    let requested = serde_json::to_value(UserInputRequestedPayload {
        draft_answers: vec![answer.clone()],
        ..UserInputRequestedPayload::default()
    })
    .unwrap();
    let request = serde_json::to_value(UserInputResolveRequest {
        answers: vec![answer.clone()],
        ..UserInputResolveRequest::default()
    })
    .unwrap();
    let resolved = serde_json::to_value(UserInputResolvedPayload {
        answers: vec![answer],
        ..UserInputResolvedPayload::default()
    })
    .unwrap();
    for (payload, key) in [
        (requested, "draft_answers"),
        (request, "answers"),
        (resolved, "answers"),
    ] {
        assert_eq!(
            payload[key][0].get("text"),
            Some(&Value::String(String::new()))
        );
        assert!(payload[key][0].get("selected_option_ids").is_none());
    }
}
