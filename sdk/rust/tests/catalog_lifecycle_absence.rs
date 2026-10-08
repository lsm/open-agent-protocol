mod common;

use oap_sdk::{ListModelsRequest, ModelLifecycle};

async fn oap_list(shape: &str, include_deprecated: Option<bool>) -> Vec<oap_sdk::ModelDescriptor> {
    let client = oap_client(shape).await;
    client
        .models()
        .list(ListModelsRequest {
            include_deprecated,
            ..ListModelsRequest::default()
        })
        .await
        .expect("the OAP path lists")
        .models
}

#[tokio::test]
async fn the_oap_filter_drops_only_a_stated_deprecated_lifecycle() {
    for retained in [
        oap_list("absent-lifecycle", None).await,
        oap_list("absent-lifecycle", Some(false)).await,
        oap_list("stable", None).await,
        oap_list("preview-lifecycle", None).await,
    ] {
        assert!(
            !retained.is_empty(),
            "a model that stated stable, preview or nothing must stay in the listing"
        );
    }

    assert!(
        oap_list("deprecated-lifecycle", None).await.is_empty(),
        "a stated deprecated lifecycle is dropped by default"
    );
    assert!(
        oap_list("deprecated-lifecycle", Some(false))
            .await
            .is_empty(),
        "and dropped again when the request says not to include them"
    );
    assert!(
        !oap_list("deprecated-lifecycle", Some(true))
            .await
            .is_empty(),
        "and kept when the request asks for deprecated models"
    );
}

async fn oap_client(shape: &str) -> oap_sdk::Client {
    let binary = std::path::PathBuf::from(env!("CARGO_BIN_EXE_oap-protocol-fake"));
    oap_sdk::ClientBuilder::new()
        .command(binary)
        .args([format!("--catalog-shape={shape}")])
        .env_clear()
        .handshake_timeout(std::time::Duration::from_millis(2_000))
        .response_timeout(std::time::Duration::from_millis(2_000))
        .frame_timeout(std::time::Duration::from_millis(2_000))
        .connect()
        .await
        .expect("the OAP fake connects")
}

#[tokio::test]
async fn the_oap_path_keeps_a_stated_lifecycle_and_refuses_a_null_one() {
    let client = oap_client("stable").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("the OAP path lists");
    assert_eq!(listed.models[0].lifecycle, Some(ModelLifecycle::Stable));

    let absent = oap_client("absent-lifecycle").await;
    let listed = absent
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("an omitted lifecycle decodes on the OAP path as unknown");
    assert_eq!(listed.models[0].lifecycle, None);

    for (shape, what) in [
        ("null-lifecycle", "a present null"),
        ("invented-lifecycle", "an invented literal"),
    ] {
        let client = oap_client(shape).await;
        let refused = client.models().list(ListModelsRequest::default()).await;
        assert!(
            refused.is_err(),
            "{what} lifecycle must be refused on the OAP path, not defaulted"
        );
    }
}
