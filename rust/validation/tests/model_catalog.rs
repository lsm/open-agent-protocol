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

fn catalog_misses(trace: &str) -> Vec<(String, String)> {
    let path = format!(
        "{}/tests/data/model_catalog/{trace}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .filter(|d| d.code == "model_not_in_catalog")
        .map(|d| (d.envelope_id, d.pointer))
        .collect()
}

#[test]
fn only_a_catalog_served_under_the_current_revision_and_binding_judges_a_selection() {
    let admitted = ("submit-resp".to_owned(), "/payload/admission".to_owned());
    assert_eq!(catalog_misses("older-revision"), [admitted]);
    assert_eq!(catalog_misses("degraded-catalog"), []);
}

#[test]
fn a_catalog_served_in_the_announce_window_binds_nothing() {
    assert_eq!(catalog_misses("announce-window"), []);
}
