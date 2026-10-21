use oap_validation::ProviderValidator;
use serde_json::{json, Value};

fn fixture(path: &str) -> Vec<Value> {
    let path = format!(
        "{}/../../fixtures/provider/{path}",
        env!("CARGO_MANIFEST_DIR")
    );
    serde_json::from_str(&std::fs::read_to_string(path).unwrap()).unwrap()
}

fn find<'a>(trace: &'a mut [Value], id: &str) -> &'a mut Value {
    trace.iter_mut().find(|e| e["id"] == id).unwrap()
}

fn drawn(trace: &[Value]) -> Vec<(String, String, String)> {
    ProviderValidator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "provider-rules")
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer))
        .collect()
}

fn one(code: &str, envelope: &str, pointer: &str) -> Vec<(String, String, String)> {
    vec![(code.to_owned(), envelope.to_owned(), pointer.to_owned())]
}

#[test]
fn a_terminal_tool_call_must_keep_the_ended_identity_and_name() {
    for (member, value) in [("tool_call_id", "tc9"), ("name", "other")] {
        let mut trace = fixture("valid/signed-reasoning-and-tool-call.json");
        find(&mut trace, "t1")["payload"]["message"]["content"][1][member] = json!(value);
        assert_eq!(
            drawn(&trace),
            one("terminal_not_assembly", "t1", "/payload/message/content/1"),
            "{member}"
        );
    }
}

#[test]
fn a_bearer_value_is_a_credential_whatever_its_case_or_leading_blanks() {
    for value in [" \tBearer synthetic", "bearer synthetic"] {
        let mut trace = fixture("semantic-invalid/bearer-shaped-header-under-another-name.json");
        trace[0]["payload"]["headers"] = json!({"X-Gateway-Token": value});
        assert_eq!(
            drawn(&trace),
            one(
                "credential_in_headers",
                "c0",
                "/payload/headers/X-Gateway-Token"
            ),
            "{value:?}"
        );
    }
}

#[test]
fn a_grant_carrying_a_value_is_judged_no_further() {
    let mut trace = fixture("semantic-invalid/grant-carrying-a-credential-value.json");
    trace[0]["payload"]["unknown_member"] = json!(true);
    assert_eq!(
        drawn(&trace),
        one("credential_in_trace", "g1", "/payload/value")
    );
}

#[test]
fn an_inference_opens_its_sequence_at_one_and_resumes_after_a_repeat() {
    let mut opening = fixture("valid/text-inference.json");
    for event in &mut opening {
        if let Some(sequence) = event["sequence"].as_u64() {
            event["sequence"] = json!(sequence + 1);
        }
    }
    assert_eq!(drawn(&opening), one("sequence_gap", "e1", "/sequence"));

    let mut repeated = fixture("valid/text-inference.json");
    let at = repeated.iter().position(|e| e["id"] == "d0-5").unwrap() + 1;
    let mut again = repeated[at - 1].clone();
    again["id"] = json!("d0-again");
    again["sequence"] = json!(2);
    again["payload"]["delta"] = json!("");
    repeated.insert(at, again);
    assert_eq!(
        drawn(&repeated),
        one("sequence_regression", "d0-again", "/sequence")
    );
}
