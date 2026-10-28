use std::collections::HashSet;

use oap_provider_catalog::{is_responses_only_model, joins_wire, Catalog, Endpoint, Provider};

fn held() -> &'static Catalog {
    Catalog::embedded()
}

#[test]
fn status_answers_supported_for_a_row_that_records_none_and_for_an_unknown_id() {
    for provider in &held().providers {
        assert!(!held().status(&provider.id).is_empty(), "{}", provider.id);
    }
    assert_eq!(held().status("no-such-provider"), "supported");
}

#[test]
fn an_unknown_id_resolves_no_wire_and_no_accessors() {
    let unknown = "no-such-provider";
    assert_eq!(held().wire_for_model(unknown, "gpt-5-pro"), "");
    assert_eq!(held().offering(unknown), "");
    assert!(held().credential_env(unknown).is_empty());
    assert!(held().base_url_env(unknown).is_empty());
    assert_eq!(held().region_env(unknown), "");
    assert_eq!(held().models_endpoint(unknown), "");
    assert!(held().oauth_origin(unknown).is_none());
}

#[test]
fn a_base_is_the_endpoint_s_own_for_its_wire_and_its_region() {
    let kimi = held().provider("kimi").expect("a kimi row");
    let regions: HashSet<_> = kimi.endpoints.iter().map(|e| e.region.as_str()).collect();
    assert_eq!(regions.len(), 2, "the region rule is untested without two");
    let china = held()
        .base_url("kimi", "openai-completions", "china")
        .unwrap();
    let global = held()
        .base_url("kimi", "openai-completions", "global")
        .unwrap();
    assert_ne!(china, global);
    assert_eq!(held().base_url("kimi", "openai-completions", "mars"), None);
    assert_eq!(held().base_url("kimi", "openai-completions", ""), None);
}

#[test]
fn no_region_answers_only_an_endpoint_that_declares_none() {
    for provider in &held().providers {
        for endpoint in provider.endpoints.iter().filter(|e| e.region.is_empty()) {
            assert_eq!(
                held().base_url(&provider.id, &endpoint.wire, ""),
                Some(endpoint.base_url.as_str()),
                "{} {}",
                provider.id,
                endpoint.wire
            );
        }
    }
}

#[test]
fn the_default_base_skips_the_wire() {
    let openai = held().provider("openai").unwrap();
    assert!(openai.endpoints.len() >= 2);
    assert_eq!(
        held().default_base_url("openai", ""),
        Some(openai.endpoints[0].base_url.as_str())
    );
}

#[test]
fn the_default_base_is_the_row_s_own_only_when_it_records_where_it_comes_from() {
    let row = Provider {
        id: "row".into(),
        base_url_source: "static".into(),
        endpoints: vec![Endpoint {
            wire: "openai-completions".into(),
            base_url: "https://api.example.com".into(),
            ..Endpoint::default()
        }],
        ..Provider::default()
    };
    let recorded = Catalog {
        providers: vec![row.clone()],
    };
    assert_eq!(
        recorded.default_base_url("row", ""),
        Some("https://api.example.com")
    );
    let unrecorded = Catalog {
        providers: vec![Provider {
            base_url_source: String::new(),
            ..row
        }],
    };
    assert_eq!(unrecorded.default_base_url("row", ""), None);
    assert_eq!(
        unrecorded.base_url("row", "openai-completions", ""),
        Some("https://api.example.com")
    );
}

#[test]
fn every_row_with_an_endpoint_records_where_its_base_comes_from() {
    for provider in &held().providers {
        if !provider.endpoints.is_empty() {
            assert!(!provider.base_url_source.is_empty(), "{}", provider.id);
        }
    }
}

#[test]
fn a_row_with_no_endpoints_resolves_no_base() {
    let empty: Vec<_> = held()
        .providers
        .iter()
        .filter(|p| p.endpoints.is_empty())
        .collect();
    assert!(!empty.is_empty(), "the no-endpoint case is untested");
    for provider in empty {
        assert_eq!(
            held().base_url(&provider.id, "openai-completions", ""),
            None
        );
        assert_eq!(held().default_base_url(&provider.id, ""), None);
    }
}

#[test]
fn a_model_only_the_responses_wire_serves_picks_that_wire() {
    let openai = held().provider("openai").unwrap();
    assert_eq!(openai.first_implemented_wire(), "openai-completions");
    for model in [
        "o1-pro",
        "o3-pro",
        "gpt-5-pro",
        "gpt-5-codex",
        "gpt-5.1-codex-max",
        "computer-use-preview",
        "gpt-5-deep-research",
    ] {
        assert!(is_responses_only_model(model), "{model}");
        assert_eq!(held().wire_for_model("openai", model), "openai-responses");
    }
    assert_eq!(
        held().wire_for_model("openai", "gpt-4o"),
        "openai-completions"
    );
    assert_eq!(held().wire_for_model("no-such-provider", "o1-pro"), "");
}

#[test]
fn every_wire_the_rows_name_is_one_the_catalog_joins() {
    for provider in &held().providers {
        for wire in &provider.wires {
            assert!(joins_wire(wire), "{} names {wire}", provider.id);
        }
        assert!(
            !provider.first_implemented_wire().is_empty(),
            "{}",
            provider.id
        );
        assert_eq!(
            held().wire_for_model(&provider.id, "some-model"),
            provider.first_implemented_wire()
        );
    }
}
