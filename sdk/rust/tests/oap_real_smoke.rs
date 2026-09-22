//! Safe live OAP smoke: profile handshake, auth status, and model catalog only.
//! Set OAP_SDK_OAP_BINARY_PATH to a locally built oapx; no login or inference runs.

use oap_sdk::{ClientBuilder, ListModelsRequest};

#[tokio::test]
async fn combined_profiles_and_auth_status_on_real_binary() {
    let Ok(binary) = std::env::var("OAP_SDK_OAP_BINARY_PATH") else {
        return;
    };
    let client = ClientBuilder::new()
        .command(binary)
        .connect()
        .await
        .expect("combined OAP connection");
    let providers = client
        .auth()
        .list_providers()
        .await
        .expect("agent-profile auth providers");
    assert!(
        !providers.is_empty(),
        "auth provider status should be discoverable"
    );
    let models = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("provider-profile model catalog");
    assert!(
        !models.models.is_empty(),
        "provider model catalog should be discoverable"
    );
    client.close().await;
}
