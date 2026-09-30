mod common;

use oap_sdk::{ListModelsRequest, ModelLifecycle};

fn descriptor(overrides: serde_json::Value) -> serde_json::Value {
    let mut base = serde_json::json!({
        "model_ref": "p/wire@m", "model_id": "m", "display_name": "M", "provider_id": "p",
        "api": "wire", "auth_status": "authenticated", "capabilities": ["chat"],
        "lifecycle": "stable", "source": "static_fallback"
    });
    let base_map = base.as_object_mut().expect("an object");
    let extra = overrides.as_object().expect("an object");
    for (key, value) in extra {
        if value.is_null() && key.as_str() == "lifecycle" {
            base_map.insert(key.clone(), serde_json::Value::Null);
            continue;
        }
        base_map.insert(key.clone(), value.clone());
    }
    base
}

#[test]
fn the_shared_result_tells_an_absent_lifecycle_from_a_present_null() {
    let mut omitted = descriptor(serde_json::json!({}));
    omitted
        .as_object_mut()
        .expect("an object")
        .remove("lifecycle");
    let decoded: oap_sdk::ModelDescriptor =
        serde_json::from_value(omitted).expect("an omitted lifecycle decodes as unknown");
    assert_eq!(decoded.lifecycle, None, "an omitted lifecycle is unknown");

    let stated: oap_sdk::ModelDescriptor =
        serde_json::from_value(descriptor(serde_json::json!({ "lifecycle": "deprecated" })))
            .expect("a stated lifecycle decodes");
    assert_eq!(
        stated.lifecycle,
        Some(ModelLifecycle::Deprecated),
        "a stated lifecycle keeps its mapping"
    );

    for (value, what) in [
        (serde_json::Value::Null, "a present null"),
        (serde_json::json!(7), "a number"),
        (serde_json::json!("retired"), "an invented literal"),
    ] {
        let rejected = serde_json::from_value::<oap_sdk::ModelDescriptor>(descriptor(
            serde_json::json!({ "lifecycle": value }),
        ));
        assert!(
            rejected.is_err(),
            "{what} lifecycle must be rejected on the shared path, not read as unknown"
        );
    }
}

async fn legacy_client(stating: &str) -> oap_sdk::Client {
    common::fake_builder("models")
        .env("OAP_SDK_FAKE_LIFECYCLE", stating)
        .connect()
        .await
        .expect("the native fake connects")
}

#[tokio::test]
async fn the_legacy_list_and_resolve_read_an_absent_lifecycle_as_unknown() {
    let client = legacy_client("absent").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("the legacy list still answers when lifecycle is omitted");
    assert!(!listed.models.is_empty());
    for model in &listed.models {
        assert_eq!(
            model.lifecycle, None,
            "an omitted lifecycle is unknown, not a fabricated value"
        );
    }
    let resolved = client
        .models()
        .resolve("anthropic", Some("anthropic-messages"), "claude-sonnet-4-5")
        .await
        .expect("the legacy resolve still answers when lifecycle is omitted");
    assert_eq!(resolved.lifecycle, None, "and unknown on resolve too");
}

#[tokio::test]
async fn the_legacy_resolve_keeps_a_stated_lifecycle() {
    let client = legacy_client("deprecated").await;
    let resolved = client
        .models()
        .resolve("anthropic", Some("anthropic-messages"), "claude-sonnet-4-5")
        .await
        .expect("the legacy resolve answers a stated lifecycle");
    assert_eq!(
        resolved.lifecycle,
        Some(ModelLifecycle::Deprecated),
        "a stated native lifecycle keeps its mapping"
    );
}

#[tokio::test]
async fn the_legacy_list_refuses_a_present_null_lifecycle() {
    let client = legacy_client("null").await;
    let refused = client.models().list(ListModelsRequest::default()).await;
    assert!(
        refused.is_err(),
        "a present null lifecycle must fail the shared list, not read as unknown"
    );
}

#[tokio::test]
async fn the_legacy_resolve_refuses_a_present_null_lifecycle() {
    let client = legacy_client("null").await;
    let refused = client
        .models()
        .resolve("anthropic", Some("anthropic-messages"), "claude-sonnet-4-5")
        .await;
    assert!(
        refused.is_err(),
        "a present null lifecycle must fail the shared resolve, not read as unknown"
    );
}

#[tokio::test]
async fn the_legacy_list_refuses_an_invalid_stated_lifecycle() {
    for (stating, what) in [("invented", "an invented literal"), ("number", "a number")] {
        let client = legacy_client(stating).await;
        let refused = client.models().list(ListModelsRequest::default()).await;
        assert!(
            refused.is_err(),
            "{what} must be rejected on the shared list, not defaulted"
        );
    }
}

#[tokio::test]
async fn a_model_that_did_not_state_a_lifecycle_is_not_filtered_as_deprecated() {
    let client = legacy_client("absent").await;
    let listed = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("the list answers");
    assert!(
        !listed.models.is_empty(),
        "an unknown lifecycle must not silently drop the model from the listing"
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
