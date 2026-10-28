use oap_provider_catalog::{Catalog, Error};

const SCHEMA: &str = r##"{"$schema":"https://json-schema.org/draft/2020-12/schema","$id":"catalog.schema.json","type":"object","required":["providers"],"properties":{"providers":{"type":"array","minItems":1,"items":{"$ref":"#/$defs/provider"}}},"additionalProperties":false,"$defs":{"provider":{"type":"object","required":["id"],"properties":{"id":{"type":"string","minLength":1},"endpoints":{"type":"array","items":{"type":"object","required":["wire","base_url"],"properties":{"wire":{"type":"string","minLength":1},"base_url":{"type":"string","minLength":1},"region":{"type":"string","minLength":1}},"additionalProperties":false}}},"additionalProperties":false}}}"##;

#[test]
fn load_reads_a_row_and_its_endpoints() {
    let catalog = Catalog::load(
        SCHEMA,
        r#"{"providers":[{"id":"kimi","endpoints":[{"wire":"openai-completions","base_url":"https://api.kimi.com/coding","region":"china"}]}]}"#,
    )
    .unwrap();
    assert_eq!(catalog.providers.len(), 1);
    let endpoint = &catalog.providers[0].endpoints[0];
    assert_eq!(endpoint.base_url, "https://api.kimi.com/coding");
    assert_eq!(endpoint.region, "china");
    assert!(!endpoint.carries_version);
}

#[test]
fn load_refuses_an_undeclared_member_and_an_empty_catalog() {
    assert!(matches!(
        Catalog::load(SCHEMA, r#"{"providers":[{"id":"kimi","surprise":1}]}"#),
        Err(Error::Invalid(_))
    ));
    assert!(Catalog::load(SCHEMA, r#"{"providers":[]}"#).is_err());
    let twice = Catalog::load(SCHEMA, r#"{"providers":[{"id":"a"},{"id":"a"}]}"#).unwrap();
    assert_eq!(twice.providers.len(), 2);
}

#[test]
fn a_member_written_twice_in_one_object_is_refused() {
    let error = Catalog::load(SCHEMA, r#"{"providers":[{"id":"a","id":"b"}]}"#).unwrap_err();
    assert!(matches!(error, Error::Decode(_)), "{error}");
}

#[test]
fn data_after_the_catalog_object_is_refused() {
    assert!(Catalog::load(SCHEMA, r#"{"providers":[{"id":"a"}]} {}"#).is_err());
}

#[test]
fn the_checked_in_catalog_loads_with_its_context_window_ceilings() {
    let catalog = Catalog::embedded();
    assert!(!catalog.providers.is_empty());
    let ceiling = |id: &str| catalog.provider(id).unwrap().max_context_window;
    assert_eq!(ceiling("openai"), 1_000_000);
    assert_eq!(ceiling("openai-codex"), 1_000_000);
    assert_eq!(ceiling("anthropic"), 0);
    let ids: std::collections::HashSet<_> = catalog.providers.iter().map(|p| &p.id).collect();
    assert_eq!(
        ids.len(),
        catalog.providers.len(),
        "one row per provider id"
    );
}

#[test]
fn the_checked_in_catalog_reencodes_to_its_own_members() {
    let text = include_str!("../../../providers/catalog.json");
    let original: serde_json::Value = serde_json::from_str(text).unwrap();
    let reencoded = serde_json::to_value(Catalog::embedded()).unwrap();
    assert_eq!(reencoded, original);
}
