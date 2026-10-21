use std::collections::BTreeSet;
use std::path::PathBuf;

use oap_validation::{codes, Phase, Validator};
use serde_json::{json, Value};

fn fixture(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(name);
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn catalog(trace: &mut Value) -> &mut Value {
    &mut trace
        .as_array_mut()
        .unwrap()
        .iter_mut()
        .find(|e| e["type"] == "models.response")
        .unwrap()["payload"]
}

fn judge(trace: &Value) -> BTreeSet<String> {
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "catalog-shape");
    assert!(result
        .diagnostics
        .iter()
        .all(|d| d.phase == Phase::Semantic));
    result.diagnostics.into_iter().map(|d| d.code).collect()
}

#[test]
fn duplicate_ids_are_rejected_even_when_separated() {
    for name in ["models-duplicate-id", "models-duplicate-id-apart"] {
        let mut trace = fixture(&format!("semantic-invalid/{name}.json"));
        assert_eq!(
            judge(&trace),
            BTreeSet::from([codes::DUPLICATE_MODEL_ID.to_owned()]),
            "{name}"
        );
        catalog(&mut trace)["models"]
            .as_array_mut()
            .unwrap()
            .last_mut()
            .unwrap()["id"] = "m3".into();
        assert!(judge(&trace).is_empty());
    }
}

#[test]
fn multiple_defaults_are_rejected_even_around_a_nondefault_current_model() {
    for name in [
        "models-two-defaults",
        "models-two-defaults-around-the-current-model",
    ] {
        let mut trace = fixture(&format!("semantic-invalid/{name}.json"));
        assert_eq!(
            judge(&trace),
            BTreeSet::from([codes::AMBIGUOUS_DEFAULT_MODEL.to_owned()]),
            "{name}"
        );
        for model in catalog(&mut trace)["models"].as_array_mut().unwrap() {
            model.as_object_mut().unwrap().remove("default");
        }
        assert!(judge(&trace).is_empty());
    }
}

#[test]
fn current_model_must_be_present_in_the_response_itself() {
    let mut trace = fixture("semantic-invalid/models-current-not-listed.json");
    assert_eq!(
        judge(&trace),
        BTreeSet::from([codes::MODEL_NOT_IN_CATALOG.to_owned()])
    );
    catalog(&mut trace)["models"]
        .as_array_mut()
        .unwrap()
        .push(json!({"id":"m1"}));
    assert!(judge(&trace).is_empty());
}

#[test]
fn empty_catalogs_and_no_default_or_nondefault_current_models_are_valid() {
    let mut trace = fixture("valid/models-empty-catalog.json");
    assert!(judge(&trace).is_empty());
    catalog(&mut trace)["models"] = json!([{"id":"m1"},{"id":"M1"}]);
    catalog(&mut trace)["current_model_id"] = "m1".into();
    assert!(judge(&trace).is_empty());
    catalog(&mut trace)["models"][1]["default"] = true.into();
    assert!(judge(&trace).is_empty());
}

#[test]
fn all_shape_faults_are_reported_even_when_the_revision_is_stale() {
    let mut trace = fixture("valid/models-empty-catalog.json");
    catalog(&mut trace)["models"] = json!([{"id":"m1","default":true},{"id":"m1","default":true}]);
    catalog(&mut trace)["current_model_id"] = "absent".into();
    let mut expected = BTreeSet::from([
        codes::DUPLICATE_MODEL_ID.to_owned(),
        codes::AMBIGUOUS_DEFAULT_MODEL.to_owned(),
        codes::MODEL_NOT_IN_CATALOG.to_owned(),
    ]);
    assert_eq!(judge(&trace), expected);
    trace[3]["capability_revision"] = "stale".into();
    expected.insert(codes::STALE_CAPABILITY_REVISION.to_owned());
    assert_eq!(judge(&trace), expected);
}

#[test]
fn an_identical_model_id_in_another_response_is_not_a_duplicate_descriptor() {
    let mut trace = fixture("valid/models-empty-catalog.json");
    catalog(&mut trace)["models"] = json!([{"id":"m1","default":true}]);
    let mut request = trace[2].clone();
    request["id"] = "query2".into();
    let mut response = trace[3].clone();
    response["id"] = "response2".into();
    response["in_reply_to"] = "query2".into();
    trace.as_array_mut().unwrap().extend([request, response]);
    assert!(judge(&trace).is_empty());
}
