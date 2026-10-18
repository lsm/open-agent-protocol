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
    assert_eq!(
        listed.models[0].source, None,
        "an omitted source must read as unknown, not as a default"
    );

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
        assert_eq!(
            listed.models[0].source,
            Some(want),
            "a stated source must survive the reader unchanged"
        );
    }
}

#[tokio::test]
async fn a_present_null_or_non_string_source_is_rejected_on_the_wire() {
    for shape in ["null-source", "number-source", "invented-source"] {
        let client = client_with(shape).await;
        let listed = client.models().list(ListModelsRequest::default()).await;
        assert!(
            listed.is_err(),
            "{shape}: a present source that is not a known literal must be rejected, not read as unknown"
        );

        let resolved = client.models().resolve("fixture", None, "mock").await;
        assert!(resolved.is_err(), "{shape}: resolve must reject it too");
    }
}

#[tokio::test]
async fn an_absent_key_and_a_stated_literal_still_succeed() {
    let client = client_with("absent-source").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("an absent key is legal and reads as unknown");
    assert_eq!(listed.models[0].source, None);

    let client = client_with("stated").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("a stated literal still lists");
    assert_eq!(listed.models[0].source, Some(ModelSource::Dynamic));
}
