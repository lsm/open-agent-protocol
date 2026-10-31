use oap_validation::Validator;

fn drawn(fixture: &str) -> Vec<(String, String, String)> {
    let path = format!(
        "{}/../../fixtures/semantic-invalid/{fixture}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    let mut found: Vec<_> = Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer))
        .collect();
    found.sort();
    found
}

fn owned(want: &[(&str, &str, &str)]) -> Vec<(String, String, String)> {
    let mut want: Vec<_> = want
        .iter()
        .map(|(a, b, c)| (a.to_string(), b.to_string(), c.to_string()))
        .collect();
    want.sort();
    want
}

#[test]
fn a_provider_attachment_is_judged_by_the_live_attach_advertisement() {
    for (fixture, want) in [
        (
            "provider-attach-accepted-without-session-live",
            &[
                (
                    "undisclosed_attach_modes",
                    "caps-response",
                    "/payload/features/action.providers.attach/modes",
                ),
                ("unavailable_capability", "attach-response", "/type"),
            ][..],
        ),
        (
            "provider-attach-conflicting-alias",
            &[(
                "duplicate_provider",
                "attach-response-two",
                "/payload/provider_id",
            )][..],
        ),
        (
            "provider-attach-degraded-refusal-wrong-code",
            &[("degraded_without_optin", "attach-error", "/payload/error")][..],
        ),
        (
            "provider-attach-unadvertised-refusal-without-reason",
            &[("unavailable_capability", "attach-error", "/payload/error")][..],
        ),
    ] {
        assert_eq!(drawn(fixture), owned(want), "{fixture}");
    }
}
