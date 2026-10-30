use oap_validation::Validator;

fn drawn(path: &str) -> Vec<(String, String, String)> {
    let path = format!(
        "{}/../../fixtures/semantic-invalid/{path}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    let data = std::fs::read(&path).unwrap();
    let mut found: Vec<_> = Validator::new()
        .unwrap()
        .validate(&data, &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer))
        .collect();
    found.sort();
    found
}

fn unadvertised(envelopes: &[&str]) -> Vec<(String, String, String)> {
    envelopes
        .iter()
        .map(|id| {
            (
                "unavailable_capability".to_owned(),
                id.to_string(),
                "/type".to_owned(),
            )
        })
        .collect()
}

#[test]
fn an_optional_unit_used_without_an_advertisement_is_unavailable() {
    for (fixture, envelopes) in [
        ("compaction-request-unadvertised", &["compreq"][..]),
        ("compaction-event-unadvertised", &["ev2", "ev3"][..]),
        ("models-unadvertised", &["models-resp"][..]),
        ("session-list-unadvertised", &["l1r"][..]),
        ("transcript-load-unadvertised", &["q1r"][..]),
    ] {
        assert_eq!(drawn(fixture), unadvertised(envelopes), "{fixture}");
    }
}
