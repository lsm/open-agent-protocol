use oap_validation::Validator;

fn drawn(fixture: &str) -> Vec<(String, String, String, String)> {
    let path = format!(
        "{}/../../fixtures/semantic-invalid/{fixture}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer, d.actual))
        .collect()
}

#[test]
fn a_catalog_declares_each_provider_once_and_every_provider_its_models_name() {
    let one = |code: &str, pointer: &str, actual: &str| {
        vec![(
            code.to_owned(),
            "models-resp".to_owned(),
            pointer.to_owned(),
            actual.to_owned(),
        )]
    };
    for (fixture, want) in [
        (
            "models-duplicate-provider",
            one("duplicate_provider", "/payload/providers", "p"),
        ),
        (
            "models-unmatched-provider",
            one("unmatched_provider", "/payload/models", "gateway"),
        ),
        (
            "models-provider-id-differs-in-case",
            one("unmatched_provider", "/payload/models", "P"),
        ),
    ] {
        assert_eq!(drawn(fixture), want, "{fixture}");
    }
}

fn derived(trace: &str) -> Vec<(String, String, Vec<String>)> {
    let path = format!(
        "{}/tests/data/catalog_providers/{trace}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.actual, d.related_ids))
        .collect()
}

#[test]
fn a_model_without_a_provider_is_fine_and_an_undeclared_one_is_reported_once() {
    assert_eq!(derived("model-without-provider"), []);
    let once = (
        "unmatched_provider".to_owned(),
        "gateway".to_owned(),
        vec!["m2".to_owned()],
    );
    assert_eq!(derived("two-models-one-undeclared-provider"), [once]);
}
