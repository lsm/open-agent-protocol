use oap_validation::Validator;

fn drawn(fixture: &str) -> Vec<(String, String, String)> {
    let path = format!(
        "{}/../../fixtures/semantic-invalid/{fixture}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer))
        .collect()
}

fn missing(envelope: &str, pointer: &str) -> Vec<(String, String, String)> {
    vec![(
        "model_not_in_catalog".to_owned(),
        envelope.to_owned(),
        pointer.to_owned(),
    )]
}

#[test]
fn a_selection_the_bound_catalog_does_not_list_must_be_refused_by_name() {
    for (fixture, envelope, pointer) in [
        (
            "models-select-unlisted",
            "submit-resp",
            "/payload/admission",
        ),
        (
            "models-select-removed-after-refresh",
            "submit-resp",
            "/payload/admission",
        ),
        (
            "models-unlisted-selection-missing-detail",
            "submit-err",
            "/payload/error",
        ),
        (
            "models-unlisted-selection-wrong-refusal",
            "submit-err",
            "/payload/error",
        ),
    ] {
        assert_eq!(drawn(fixture), missing(envelope, pointer), "{fixture}");
    }
}
