use oap_provider_catalog::{
    base_url_with_overrides, normalize_kimi_region, normalize_versioned_base_url, Catalog,
    Endpoint, EnvironmentValue, Provider, GLOBAL_BASE_URL_ENV,
};

fn held() -> &'static Catalog {
    Catalog::embedded()
}

fn env(pairs: &[(&str, &str)]) -> Vec<EnvironmentValue> {
    pairs
        .iter()
        .map(|(name, value)| EnvironmentValue::new(*name, *value))
        .collect()
}

fn kimi(region: &str) -> String {
    held()
        .base_url("kimi", "openai-completions", region)
        .unwrap()
        .to_owned()
}

#[test]
fn the_global_override_redirects_any_provider_to_the_loopback() {
    let overrides =
        held().base_overrides_from_env(&env(&[(GLOBAL_BASE_URL_ENV, "http://127.0.0.1:9/v1")]));
    for (id, wire) in [
        ("openai", "openai-completions"),
        ("anthropic", "anthropic-messages"),
        ("deepseek", "openai-completions"),
        ("kimi", "openai-completions"),
    ] {
        assert_eq!(
            base_url_with_overrides(&overrides, id, wire),
            "http://127.0.0.1:9",
            "{id}"
        );
    }
}

#[test]
fn the_global_override_keeps_its_path_but_loses_its_slashes_for_an_unversioned_route() {
    let overrides =
        held().base_overrides_from_env(&env(&[(GLOBAL_BASE_URL_ENV, "http://127.0.0.1:9/proxy/")]));
    assert_eq!(
        base_url_with_overrides(&overrides, "github-copilot", "openai-completions"),
        "http://127.0.0.1:9/proxy"
    );
    let overrides =
        held().base_overrides_from_env(&env(&[(GLOBAL_BASE_URL_ENV, "http://127.0.0.1:9///")]));
    assert_eq!(
        base_url_with_overrides(&overrides, "github-copilot", "openai-completions"),
        "http://127.0.0.1:9"
    );
}

#[test]
fn the_global_override_wins_over_the_per_provider_one() {
    let openai = held().first_base_url_env("openai");
    let overrides = held().base_overrides_from_env(&env(&[
        (GLOBAL_BASE_URL_ENV, "http://global.test"),
        (openai, "http://openai.test"),
    ]));
    assert_eq!(
        base_url_with_overrides(&overrides, "openai", "openai-completions"),
        "http://global.test"
    );
}

#[test]
fn a_per_provider_override_only_reaches_its_own_provider_and_wire() {
    let anthropic = held().first_base_url_env("anthropic");
    let overrides = held().base_overrides_from_env(&env(&[(anthropic, "http://anthropic.test")]));
    assert_eq!(
        base_url_with_overrides(&overrides, "anthropic", "anthropic-messages"),
        "http://anthropic.test"
    );
    for (id, wire) in [
        ("openai", "openai-completions"),
        ("anthropic", "openai-completions"),
        ("openai", "anthropic-messages"),
        ("kimi", "anthropic-messages"),
    ] {
        assert_eq!(
            base_url_with_overrides(&overrides, id, wire),
            "",
            "{id} {wire}"
        );
    }
}

#[test]
fn the_version_suffix_is_stripped_and_trailing_slashes_trimmed() {
    for (input, want) in [
        ("http://a.test/v1", "http://a.test"),
        ("http://a.test/", "http://a.test"),
        ("http://a.test///", "http://a.test"),
        ("http://a.test", "http://a.test"),
        ("http://a.test/v1///", "http://a.test"),
        ("", ""),
    ] {
        assert_eq!(normalize_versioned_base_url(input), want, "{input}");
    }
}

#[test]
fn an_empty_override_is_no_override() {
    let openai = held().first_base_url_env("openai");
    let overrides =
        held().base_overrides_from_env(&env(&[(GLOBAL_BASE_URL_ENV, ""), (openai, "")]));
    assert_eq!(
        base_url_with_overrides(&overrides, "openai", "openai-completions"),
        ""
    );
}

#[test]
fn the_openai_override_reaches_the_responses_wire_as_well_as_completions() {
    let openai = held().first_base_url_env("openai");
    let overrides = held().base_overrides_from_env(&env(&[(openai, "http://both.test")]));
    for wire in ["openai-completions", "openai-responses"] {
        assert_eq!(
            base_url_with_overrides(&overrides, "openai", wire),
            "http://both.test"
        );
    }
    assert_eq!(
        base_url_with_overrides(&overrides, "openai", "openai-codex-responses"),
        ""
    );
}

#[test]
fn the_kimi_region_takes_its_aliases_ascii_case_insensitively() {
    for (input, want) in [
        ("global", "global"),
        ("GLOBAL", "global"),
        (" moonshot ", "global"),
        ("Moonshot", "global"),
        ("china", "china"),
        ("CN", "china"),
        ("coding", "china"),
        (" Coding\n", "china"),
        ("elsewhere", ""),
        ("", ""),
        ("globalish", ""),
        ("moon\u{17f}hot", ""),
        ("CH\u{130}NA", ""),
        ("glob\u{131}al", ""),
    ] {
        assert_eq!(normalize_kimi_region(input), want, "{input:?}");
    }
}

#[test]
fn the_kimi_region_from_the_environment_picks_the_catalog_endpoint() {
    let region = held().region_env("kimi");
    for value in ["moonshot", "GLOBAL", "global"] {
        assert_eq!(
            held().resolve_base_url(
                &env(&[(region, value)]),
                "kimi",
                "openai-completions",
                "china"
            ),
            Some(kimi("global"))
        );
    }
    assert_eq!(
        held()
            .base_overrides_from_env(&env(&[(region, "elsewhere")]))
            .kimi_region,
        ""
    );
    for stored in ["", "elsewhere", "   "] {
        assert_eq!(
            held().resolve_base_url(&[], "kimi", "openai-completions", stored),
            Some(kimi("china"))
        );
    }
    assert_eq!(
        held().resolve_base_url(&[], "kimi", "openai-completions", "moonshot"),
        Some(kimi("global"))
    );
}

#[test]
fn the_catalog_fallback_for_a_known_pair_ignores_the_region() {
    for (id, wire) in [
        ("openai", "openai-completions"),
        ("anthropic", "anthropic-messages"),
        ("deepseek", "openai-completions"),
        ("openai-codex", "openai-codex-responses"),
        ("openrouter", "openai-completions"),
    ] {
        for region in ["", "a-region-nobody-declares"] {
            assert!(
                held().resolve_base_url(&[], id, wire, region).is_some(),
                "{id} {region}"
            );
        }
    }
}

fn renamed(apply: impl Fn(&mut Provider)) -> Catalog {
    let mut catalog = held().clone();
    catalog.providers.iter_mut().for_each(apply);
    catalog
}

#[test]
fn the_override_names_come_from_the_catalog_row() {
    let stale = held().first_base_url_env("openai").to_owned();
    let stale_region = held().region_env("kimi").to_owned();
    let catalog = renamed(|row| match row.id.as_str() {
        "openai" => row.base_url_env = vec!["A_DIFFERENT_NAME".into()],
        "kimi" => row.region_env = "A_DIFFERENT_REGION_NAME".into(),
        _ => {}
    });
    let overrides = catalog.base_overrides_from_env(&env(&[
        ("A_DIFFERENT_NAME", "http://renamed.test"),
        (&stale, "http://stale.test"),
    ]));
    assert_eq!(
        base_url_with_overrides(&overrides, "openai", "openai-completions"),
        "http://renamed.test"
    );
    let regions = catalog.base_overrides_from_env(&env(&[
        ("A_DIFFERENT_REGION_NAME", "moonshot"),
        (&stale_region, "china"),
    ]));
    assert_eq!(regions.kimi_region, "global");
    assert_eq!(held().first_base_url_env("a-row-that-has-none"), "");
}

#[test]
fn the_kimi_fallback_is_the_row_s_own_default_region() {
    assert_eq!(held().default_region("kimi"), "china");
    assert_eq!(held().default_kimi_region(), "china");
    let catalog = renamed(|row| {
        if row.id == "kimi" {
            row.default_region = "global".into();
        }
    });
    assert_eq!(catalog.default_kimi_region(), "global");
    assert_eq!(
        catalog.resolve_base_url(&[], "kimi", "openai-completions", ""),
        Some(kimi("global"))
    );
}

#[test]
fn a_row_naming_no_default_region_falls_back_to_china() {
    let endpoint = |region: &str, base: &str| Endpoint {
        wire: "openai-completions".into(),
        region: region.into(),
        base_url: base.into(),
        ..Endpoint::default()
    };
    let catalog = Catalog {
        providers: vec![Provider {
            id: "kimi".into(),
            endpoints: vec![
                endpoint("china", "http://china.test"),
                endpoint("global", "http://global.test"),
            ],
            ..Provider::default()
        }],
    };
    assert_eq!(catalog.default_region("kimi"), "");
    assert_eq!(catalog.default_kimi_region(), "china");
    assert_eq!(
        catalog.resolve_base_url(&[], "kimi", "openai-completions", ""),
        Some("http://china.test".into())
    );
    assert_eq!(
        catalog.resolve_base_url(&[], "kimi", "openai-completions", "global"),
        Some("http://global.test".into())
    );
}
