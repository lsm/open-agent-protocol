use oap_sdk::{AuthStatus, ListModelsRequest};

async fn client_with(shape: &str) -> oap_sdk::Client {
    oap_sdk::ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([format!("--catalog-shape={shape}")])
        .connect()
        .await
        .expect("connects to the fake OAP server")
}

#[tokio::test]
async fn an_omitted_auth_status_reads_as_unknown_through_the_oap_path() {
    let client = client_with("absent-auth").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("an omitted auth_status must not fail the listing");
    assert_eq!(listed.models.len(), 1);
    assert_eq!(
        listed.models[0].auth_status,
        AuthStatus::Unknown,
        "an omitted auth_status must read as the existing unknown value"
    );

    let resolved = client
        .models()
        .resolve("fixture", None, "mock")
        .await
        .expect("an omitted auth_status must not fail resolve either");
    assert_eq!(resolved.auth_status, AuthStatus::Unknown);
}

#[tokio::test]
async fn every_stated_auth_status_survives_the_oap_reader() {
    for (literal, want) in [
        ("authenticated", AuthStatus::Authenticated),
        ("login_required", AuthStatus::LoginRequired),
        ("expired", AuthStatus::Expired),
        ("refreshing", AuthStatus::Refreshing),
        ("login_in_progress", AuthStatus::LoginInProgress),
        ("failed", AuthStatus::Failed),
        ("unknown", AuthStatus::Unknown),
    ] {
        let client = client_with(&format!("stated-auth-{literal}")).await;
        let listed = client
            .models()
            .list(ListModelsRequest {
                include_login_required: Some(true),
                ..ListModelsRequest::default()
            })
            .await
            .unwrap_or_else(|err| panic!("{literal} must list, got {err}"));
        assert_eq!(
            listed.models.len(),
            1,
            "{literal} must survive the reader's own login_required filter"
        );
        assert_eq!(
            listed.models[0].auth_status, want,
            "{literal} must reach the reader as itself"
        );
    }
}

#[tokio::test]
async fn a_malformed_auth_status_is_refused_even_when_a_local_filter_would_skip_the_row() {
    for (shape, api) in [
        ("null-auth", "openai-responses"),
        ("number-auth", "openai-responses"),
        ("invented-auth", "openai-responses"),
        ("null-auth", "no-such-api"),
        ("invented-auth", "no-such-model"),
    ] {
        let client = client_with(shape).await;
        let request = ListModelsRequest {
            api: Some(api.to_owned()),
            ..ListModelsRequest::default()
        };
        let result = client.models().list(request).await;
        assert!(
            result.is_err(),
            "{shape} must be refused as malformed_response, got {:?}",
            result.map(|listed| listed.models.len())
        );
        let message = result.err().map(|err| err.to_string()).unwrap_or_default();
        assert!(
            message.contains("malformed") || message.contains("OAP model entry is malformed"),
            "{shape} must be refused as a protocol error, got {message}"
        );
    }
}

#[tokio::test]
async fn the_api_filter_really_skips_a_valid_row_before_any_refusal_is_claimed() {
    let client = client_with("stated-auth-authenticated").await;
    let listed = client
        .models()
        .list(ListModelsRequest {
            api: Some("no-such-api".to_owned()),
            ..ListModelsRequest::default()
        })
        .await
        .expect("a valid row with a stated auth_status lists whatever the api filter does");
    assert_eq!(
        listed.models.len(),
        0,
        "the local api filter must skip the valid row"
    );
}
