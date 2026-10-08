//! End-to-end coverage against a real `oapx serve agent,provider --stdio` build.
//!
//! These need a runtime binary and skip when `OAP_SDK_BINARY_PATH` is unset, which
//! mirrors `typescript/test/makai_binary_smoke.test.ts`. Build one with:
//!
//! ```text
//! zig build install --prefix /tmp/oapx-rs
//! OAP_SDK_BINARY_PATH=/tmp/oapx-rs/bin/oapx cargo test --test real_binary
//! ```
//!
//! Nothing here needs provider credentials. The paths that would are exercised
//! through their unauthenticated failure, which is itself the behaviour worth
//! pinning: an `auth_required` rejection must reach the typed auth error. The
//! runtime's built-in `test-fixture` auth provider gives a real, deterministic
//! interactive login with no network.

#![allow(
    clippy::unwrap_used,
    clippy::expect_used,
    clippy::panic,
    clippy::indexing_slicing
)]

mod common;

use std::time::Duration;

use futures::StreamExt;
use oap_sdk::{Error, ExecutionRequest, ListModelsRequest};

#[tokio::test]
async fn connects_to_the_real_runtime() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("the runtime connects");
    assert!(!client.is_closed());
    client.close().await;
    assert!(client.is_closed());
}

#[tokio::test]
async fn a_version_mismatch_against_the_real_runtime_fails_fast() {
    let binary = require_real_binary!();
    let error = common::real_builder(&binary)
        .expected_protocol_version("2")
        .connect()
        .await
        .unwrap_err();
    assert_eq!(error.code(), Some("unsupported_feature"));
}

#[tokio::test]
async fn the_real_runtime_serves_the_model_catalog() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("connects");

    let response = client
        .models()
        .list(ListModelsRequest {
            include_login_required: Some(true),
            ..Default::default()
        })
        .await
        .expect("lists models");
    assert!(
        !response.models.is_empty(),
        "the static catalog is non-empty"
    );
    assert!(response.fetched_at_ms > 0);

    for model in &response.models {
        assert!(!model.model_ref.is_empty());
        assert!(!model.model_id.is_empty());
        assert!(!model.provider_id.is_empty());
    }
    client.close().await;
}

#[tokio::test]
async fn the_real_runtime_resolves_one_model() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("connects");

    let listed = client
        .models()
        .list(ListModelsRequest {
            include_login_required: Some(true),
            ..Default::default()
        })
        .await
        .expect("lists models");
    let first = listed.models.first().expect("at least one model").clone();

    let resolved = client
        .models()
        .resolve(&first.provider_id, Some(&first.api), &first.model_id)
        .await
        .expect("resolves");
    assert_eq!(resolved.model_ref, first.model_ref);
    client.close().await;
}

#[tokio::test]
async fn resolving_a_model_that_does_not_exist_is_an_invalid_request() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("connects");
    let error = client
        .models()
        .resolve("anthropic", Some("anthropic-messages"), "no-such-model")
        .await
        .unwrap_err();
    assert!(matches!(error, Error::Protocol { .. }), "{error:?}");
    assert_eq!(error.code(), Some("invalid_request"));
    client.close().await;
}

#[tokio::test]
async fn the_real_runtime_lists_auth_providers() {
    let binary = require_real_binary!();
    let client = common::real_builder_with_fixture(&binary)
        .connect()
        .await
        .expect("connects");

    let providers = client
        .auth()
        .list_providers()
        .await
        .expect("lists providers");
    assert!(!providers.is_empty());
    assert!(
        providers.iter().any(|provider| provider.id == "anthropic"),
        "{providers:?}"
    );
    // The runtime serves the fixture only when `OAPX_TEST_FIXTURE_PROVIDER=1`,
    // which this client set. So a runtime that does not offer it now is a
    // failure, not a reason to skip: the test asked for it by name, and the
    // skip is what hid this coverage being lost in the first place.
    assert!(
        providers
            .iter()
            .any(|provider| provider.id == "test-fixture"),
        "the CI fixture provider was asked for by name but is not served: {providers:?}"
    );
    client.close().await;
}

#[tokio::test]
async fn a_runtime_that_was_not_asked_does_not_serve_the_fixture() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("connects");

    let providers = client
        .auth()
        .list_providers()
        .await
        .expect("lists providers");
    assert!(
        !providers
            .iter()
            .any(|provider| provider.id == "test-fixture"),
        "a user who did not set the opt-in was offered the CI fixture: {providers:?}"
    );
    assert!(!providers.is_empty());
    client.close().await;
}

#[tokio::test]
async fn a_manual_login_fails_closed_against_the_real_runtime() {
    let binary = require_real_binary!();
    let home = tempfile::tempdir().expect("tempdir");
    let client = common::real_builder_with_fixture(&binary)
        .env("HOME", home.path().display().to_string())
        .frame_timeout(Duration::from_millis(3_000))
        .connect()
        .await
        .expect("connects");

    let error = client.auth().login("test-fixture", None).await.unwrap_err();
    assert_eq!(error.code(), Some("auth_input_unavailable"), "{error:?}");
    client.close().await;
}

#[tokio::test]
async fn an_unauthenticated_provider_call_reaches_the_typed_auth_error() {
    let binary = require_real_binary!();
    let home = tempfile::tempdir().expect("tempdir");
    let client = common::real_builder(&binary)
        .env("HOME", home.path().display().to_string())
        .connect()
        .await
        .expect("connects");

    let error = client
        .provider()
        .complete(ExecutionRequest::prompt(
            "anthropic/anthropic-messages@claude-sonnet-4-5",
            "hi",
        ))
        .await
        .unwrap_err();
    assert!(matches!(error, Error::AuthRequired { .. }), "{error:?}");
    assert_eq!(error.provider_id(), Some("anthropic"));
    client.close().await;
}

#[tokio::test]
async fn an_unauthenticated_provider_stream_reaches_the_typed_auth_error() {
    let binary = require_real_binary!();
    let home = tempfile::tempdir().expect("tempdir");
    let client = common::real_builder(&binary)
        .env("HOME", home.path().display().to_string())
        .connect()
        .await
        .expect("connects");

    let mut events = Box::pin(client.provider().stream(ExecutionRequest::prompt(
        "anthropic/anthropic-messages@claude-sonnet-4-5",
        "hi",
    )));
    let error = events
        .next()
        .await
        .expect("one item")
        .expect_err("auth is required");
    assert!(matches!(error, Error::AuthRequired { .. }), "{error:?}");
    drop(events);
    client.close().await;
}

#[tokio::test]
async fn an_agent_run_on_a_model_outside_the_catalog_is_refused_by_name() {
    let binary = require_real_binary!();
    let home = tempfile::tempdir().expect("tempdir");
    let client = common::real_builder(&binary)
        .env("HOME", home.path().display().to_string())
        .connect()
        .await
        .expect("connects");

    let error = client
        .agent()
        .run(ExecutionRequest::prompt(
            "anthropic/anthropic-messages@claude-sonnet-4-5",
            "hi",
        ))
        .await
        .unwrap_err();
    assert_eq!(error.code(), Some("model_not_found"), "{error:?}");
    client.close().await;
}

#[tokio::test]
async fn several_calls_multiplex_over_one_real_runtime() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("connects");

    let models = client.models();
    let auth = client.auth();
    let (listed, providers) = tokio::join!(
        models.list(ListModelsRequest::default()),
        auth.list_providers()
    );
    assert!(!listed.expect("lists").models.is_empty());
    assert!(!providers.expect("lists").is_empty());
    client.close().await;
}

#[tokio::test]
async fn closing_the_client_reaps_the_real_runtime() {
    let binary = require_real_binary!();
    let client = common::real_builder(&binary)
        .connect()
        .await
        .expect("connects");
    client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("lists");

    let started = tokio::time::Instant::now();
    client.close().await;
    assert!(
        started.elapsed() < Duration::from_secs(3),
        "close should not wait out a timeout"
    );
    assert!(client.is_closed());
}
