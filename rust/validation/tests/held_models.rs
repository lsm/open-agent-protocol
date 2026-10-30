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

#[test]
fn a_selection_made_before_a_catalog_is_judged_when_one_arrives() {
    for (fixture, envelope, pointer) in [
        (
            "compound-open-anonymous-model-reconciled",
            "ocr",
            "/payload/model_id",
        ),
        (
            "models-select-in-gap-unlisted",
            "submit-resp",
            "/payload/model_id",
        ),
        (
            "models-listed-selection-false-miss",
            "submit-err",
            "/payload/error",
        ),
        (
            "models-refused-before-catalog-missing-detail",
            "submit-err",
            "/payload/error",
        ),
        (
            "model-switch-unlisted",
            "switch-response",
            "/payload/model_id",
        ),
    ] {
        let want = vec![(
            "model_not_in_catalog".to_owned(),
            envelope.to_owned(),
            pointer.to_owned(),
        )];
        assert_eq!(drawn(fixture), want, "{fixture}");
    }
}
