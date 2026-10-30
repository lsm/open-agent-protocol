use oap_provider_catalog::{
    api_key_from_names, Catalog, EnvironmentValue, Source, StoredCredential,
};

fn held() -> &'static Catalog {
    Catalog::embedded()
}

fn env(names: &[&str]) -> Vec<EnvironmentValue> {
    names
        .iter()
        .map(|name| EnvironmentValue::new(*name, "held"))
        .collect()
}

fn key(value: &str) -> StoredCredential {
    StoredCredential {
        api_key: Some(value.into()),
        ..StoredCredential::default()
    }
}

fn access(value: &str) -> StoredCredential {
    StoredCredential {
        oauth_access: Some(value.into()),
        ..StoredCredential::default()
    }
}

#[test]
fn the_environment_answers_before_a_stored_key() {
    let name = held().credential_env("openai")[0].clone();
    let credential = held()
        .lookup_credential(&env(&[&name]), Some(&key("sk-stored")), "openai")
        .unwrap();
    assert_eq!(credential.source, Source::Environment);
    assert_eq!(credential.name, name);
    assert_eq!(credential.key, "held");
}

#[test]
fn the_row_s_own_variable_order_decides_which_key_answers() {
    let names = held().credential_env("anthropic");
    assert_eq!(names, ["ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"]);
    let credential = held()
        .api_key_for_provider(
            &env(&["ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"]),
            "anthropic",
        )
        .unwrap();
    assert_eq!(credential.name, "ANTHROPIC_AUTH_TOKEN");
}

#[test]
fn an_empty_environment_variable_counts_as_unset() {
    let held_env = vec![
        EnvironmentValue::new("ANTHROPIC_AUTH_TOKEN", ""),
        EnvironmentValue::new("ANTHROPIC_API_KEY", "sk-second"),
    ];
    let credential = held().api_key_for_provider(&held_env, "anthropic").unwrap();
    assert_eq!(
        (credential.name.as_str(), credential.key.as_str()),
        ("ANTHROPIC_API_KEY", "sk-second")
    );
    let names = ["A".to_owned(), "B".to_owned()];
    assert!(api_key_from_names(&[EnvironmentValue::new("A", "")], &names).is_none());
    let repeated = [
        EnvironmentValue::new("A", ""),
        EnvironmentValue::new("A", "later"),
    ];
    assert!(api_key_from_names(&repeated, &names[..1]).is_none());
}

#[test]
fn a_stored_key_answers_a_row_with_nothing_in_the_environment() {
    let credential = held()
        .lookup_credential(&[], Some(&key("sk-stored")), "openai")
        .unwrap();
    assert_eq!(credential.source, Source::Stored);
    assert_eq!(credential.name, "openai");
    assert_eq!(credential.key, "sk-stored");
}

#[test]
fn a_stored_key_is_refused_by_a_row_that_takes_no_api_key() {
    assert!(held().accepts_auth("openai-codex", "oauth"));
    assert!(!held().accepts_auth("openai-codex", "api_key"));
    assert!(held().credential_env("openai-codex").is_empty());
    assert!(held()
        .lookup_credential(&[], Some(&key("sk")), "openai-codex")
        .is_none());
    let credential = held()
        .lookup_credential(&[], Some(&access("at-oauth")), "openai-codex")
        .unwrap();
    assert_eq!(credential.source, Source::OAuth);
    assert_eq!(credential.source.as_str(), "oauth");
}

#[test]
fn an_oauth_access_is_refused_by_an_api_key_only_row() {
    assert!(!held().accepts_auth("openai", "oauth"));
    assert!(held()
        .lookup_credential(&[], Some(&access("at-oauth")), "openai")
        .is_none());
}

#[test]
fn a_stored_credential_that_is_set_and_empty_resolves_nothing() {
    assert!(held()
        .lookup_credential(&[], Some(&key("")), "openai")
        .is_none());
    assert!(held()
        .lookup_credential(&[], Some(&access("")), "openai-codex")
        .is_none());
    assert!(held()
        .lookup_credential(&[], Some(&StoredCredential::default()), "openai")
        .is_none());
    assert!(held().lookup_credential(&[], None, "openai").is_none());
}

#[test]
fn a_row_that_needs_no_credential_says_so() {
    let found = held()
        .providers
        .iter()
        .filter(|provider| held().needs_no_credential(&provider.id))
        .count();
    assert!(
        found > 0,
        "no row needs no credential: the case is untested"
    );
    assert!(!held().needs_no_credential("openai"));
    assert!(!held().needs_no_credential("no-such-provider"));
}

#[test]
fn an_unknown_id_resolves_no_credential() {
    let name = held().credential_env("openai")[0].clone();
    assert!(held()
        .lookup_credential(
            &env(&[&name, "ANYTHING"]),
            Some(&key("sk")),
            "no-such-provider"
        )
        .is_none());
    assert!(held()
        .api_key_for_provider(&env(&[&name]), "no-such-provider")
        .is_none());
    assert!(!held().accepts_auth("no-such-provider", "api_key"));
}

#[test]
fn every_row_that_names_a_variable_resolves_its_first() {
    for provider in &held().providers {
        let names = held().credential_env(&provider.id);
        if names.is_empty() {
            continue;
        }
        let values: Vec<_> = names
            .iter()
            .map(|name| EnvironmentValue::new(name.clone(), format!("key-for-{name}")))
            .collect();
        let credential = held().api_key_for_provider(&values, &provider.id).unwrap();
        assert_eq!(credential.name, names[0], "{}", provider.id);
    }
}

#[test]
fn a_credential_group_names_the_shared_key() {
    for provider in &held().providers {
        let want = if provider.credential_group.is_empty() {
            provider.id.as_str()
        } else {
            provider.credential_group.as_str()
        };
        assert_eq!(held().credential_key(&provider.id), want);
    }
    assert_eq!(
        held().credential_key("no-such-provider"),
        "no-such-provider"
    );
}
