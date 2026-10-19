use oap_protocol::{MessageSubmitRequest, ToolChoice, ToolChoiceError};
use serde_json::json;

fn policy(tool_choice: serde_json::Value) -> Result<Option<ToolChoice>, ToolChoiceError> {
    serde_json::from_value::<MessageSubmitRequest>(
        json!({"session_id": "s", "messages": [], "delivery": "auto", "tool_choice": tool_choice}),
    )
    .unwrap()
    .tool_choice_policy()
}

fn names(list: &[&str]) -> Vec<String> {
    list.iter().map(|name| name.to_string()).collect()
}

#[test]
fn an_absent_tool_choice_is_no_policy() {
    let request: MessageSubmitRequest =
        serde_json::from_value(json!({"session_id": "s", "messages": [], "delivery": "auto"}))
            .unwrap();
    assert_eq!(request.tool_choice_policy(), Ok(None));
}

#[test]
fn a_tool_choice_that_is_not_exactly_one_list_is_refused_for_the_reason_it_fails() {
    assert_eq!(policy(json!(null)), Err(ToolChoiceError::Null));
    assert_eq!(policy(json!({})), Err(ToolChoiceError::Neither));
    assert_eq!(
        policy(json!({"allowed": [], "disallowed": []})),
        Err(ToolChoiceError::Both)
    );
    assert_eq!(
        policy(json!({"allowed": null})),
        Err(ToolChoiceError::NullList("allowed"))
    );
    assert!(matches!(
        policy(json!({"allowed": [], "mode": "x"})),
        Err(ToolChoiceError::NotThePolicy(_))
    ));
    assert!(matches!(
        policy(json!({"allowed": "a"})),
        Err(ToolChoiceError::NotThePolicy(_))
    ));
    assert!(matches!(
        policy(json!("auto")),
        Err(ToolChoiceError::NotThePolicy(_))
    ));
}

#[test]
fn an_allowed_list_filters_to_its_names_and_a_disallowed_list_filters_them_out() {
    let catalog = names(&["read", "write", "run"]);
    let allowed = policy(json!({"allowed": ["read", "run"]}))
        .unwrap()
        .unwrap();
    assert_eq!(allowed.filter(&catalog), names(&["read", "run"]));
    let disallowed = policy(json!({"disallowed": ["write"]})).unwrap().unwrap();
    assert_eq!(disallowed.filter(&catalog), names(&["read", "run"]));
    let nothing = policy(json!({"allowed": []})).unwrap().unwrap();
    assert!(nothing.filter(&catalog).is_empty());
}

#[test]
fn a_name_outside_a_known_catalog_is_neither_permitted_nor_satisfiable() {
    let catalog = names(&["read"]);
    let choice = policy(json!({"allowed": ["read", "ghost"]}))
        .unwrap()
        .unwrap();
    let defect = choice.unsatisfiable(&catalog, true).unwrap();
    assert_eq!(defect.pointer, "/payload/tool_choice/allowed/1");
    assert_eq!(defect.tool, "ghost");
    assert!(choice.unsatisfiable(&catalog, false).is_none());
    assert!(!choice.permits("ghost", &catalog, true));
    assert!(choice.permits("ghost", &catalog, false));
    assert!(choice.permits("read", &catalog, true));
}
