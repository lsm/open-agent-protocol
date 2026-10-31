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

fn traced(trace: &str) -> Vec<(String, String, String)> {
    let path = format!(
        "{}/tests/data/provider_attach/{trace}.json",
        env!("CARGO_MANIFEST_DIR")
    );
    let found: std::collections::BTreeSet<_> = Validator::new()
        .unwrap()
        .validate(&std::fs::read(&path).unwrap(), &path)
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.envelope_id, d.pointer))
        .collect();
    found.into_iter().collect()
}

#[test]
fn every_attach_path_draws_what_go_draws() {
    let cases: [(&str, &[(&str, &str, &str)]); 13] = [
        (
            "degraded-accepted-other-session",
            &[("scope_mismatch", "attach-response", "/payload/session_id")][..],
        ),
        (
            "alias-named-by-a-model",
            &[(
                "duplicate_provider",
                "attach-response-a",
                "/payload/provider_id",
            )][..],
        ),
        (
            "alias-served-by-catalog",
            &[(
                "duplicate_provider",
                "attach-response-a",
                "/payload/provider_id",
            )][..],
        ),
        ("attach-drops-catalog", &[][..]),
        (
            "catalog-omits-attached",
            &[("unmatched_provider", "models-resp", "/payload/providers")][..],
        ),
        (
            "degraded-accepted",
            &[("degraded_without_optin", "attach-response", "/type")][..],
        ),
        ("degraded-refused-with-optin", &[][..]),
        (
            "no-descriptor-accepted",
            &[("unavailable_capability", "attach-request-one", "/type")][..],
        ),
        (
            "no-descriptor-refused",
            &[("unavailable_capability", "attach-request", "/type")][..],
        ),
        (
            "response-other-alias",
            &[(
                "scope_mismatch",
                "attach-response-one",
                "/payload/provider_id",
            )][..],
        ),
        (
            "response-other-session",
            &[(
                "scope_mismatch",
                "attach-response-one",
                "/payload/session_id",
            )][..],
        ),
        (
            "unadvertised-refused-other-code",
            &[("unavailable_capability", "attach-error", "/payload/error")][..],
        ),
        ("unavailable-descriptor", &[][..]),
    ];
    for (trace, want) in cases {
        assert_eq!(traced(trace), owned(want), "{trace}");
    }
}
