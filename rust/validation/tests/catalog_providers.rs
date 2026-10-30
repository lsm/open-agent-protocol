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
