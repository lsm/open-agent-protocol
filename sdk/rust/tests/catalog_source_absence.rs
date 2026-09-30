mod common;

use oap_sdk::{ListModelsRequest, ModelSource};

async fn client_with(shape: &str) -> oap_sdk::Client {
    oap_sdk::ClientBuilder::new()
        .command(env!("CARGO_BIN_EXE_oap-protocol-fake"))
        .args([format!("--catalog-shape={shape}")])
        .connect()
        .await
        .expect("connects to the fake OAP server")
}

#[tokio::test]
async fn an_omitted_source_reads_as_unknown_through_the_oap_path() {
    let client = client_with("absent-source").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("an omitted source must not fail the listing");
    assert_eq!(listed.models.len(), 1);
    assert_eq!(listed.models[0].source, None, "an omitted source must read as unknown, not as a default");

    let resolved = client
        .models()
        .resolve("fixture", None, "mock")
        .await
        .expect("an omitted source must not fail resolve either");
    assert_eq!(resolved.source, None);
}

#[tokio::test]
async fn a_stated_source_is_read_unchanged_on_the_oap_path() {
    for (shape, want) in [
        ("stated", ModelSource::Dynamic),
        ("fallback", ModelSource::StaticFallback),
    ] {
        let client = client_with(shape).await;
        let listed = client
            .models()
            .list(ListModelsRequest::default())
            .await
            .expect("a stated source still lists");
        assert_eq!(listed.models[0].source, Some(want), "a stated source must survive the reader unchanged");
    }
}

#[tokio::test]
async fn the_native_legacy_path_keeps_its_stated_mapping() {
    let client = common::fake_client("models").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("the native path still lists");
    assert!(!listed.models.is_empty());
    for model in &listed.models {
        assert!(
            model.source.is_some(),
            "the native path states a source and must keep reading it"
        );
    }
}

#[test]
fn the_shared_result_sees_a_missing_source_as_unknown_and_still_rejects_a_bad_one() {
    let stated = serde_json::json!({
        "model_ref": "p/wire@m", "model_id": "m", "display_name": "M", "provider_id": "p",
        "api": "wire", "auth_status": "authenticated", "lifecycle": "stable",
        "capabilities": ["chat"], "source": "static_fallback"
    });
    let decoded: oap_sdk::ModelDescriptor =
        serde_json::from_value(stated.clone()).expect("a stated source decodes");
    assert_eq!(decoded.source, Some(ModelSource::StaticFallback));

    let mut omitted = stated.clone();
    omitted.as_object_mut().unwrap().remove("source");
    let decoded: oap_sdk::ModelDescriptor =
        serde_json::from_value(omitted).expect("an omitted source decodes as unknown");
    assert_eq!(decoded.source, None);

    let mut bogus = stated.clone();
    bogus["source"] = serde_json::json!("discovered-magic");
    let rejected = serde_json::from_value::<oap_sdk::ModelDescriptor>(bogus);
    assert!(rejected.is_err(), "an invalid stated source must still be rejected, not defaulted");
}
