use std::path::Path;

use oxap_validation::pack::PackLoader;
use serde_json::Value;

#[test]
fn compiled_packs_validate_complete_envelopes_and_member_values() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    for (pack, trace, valid) in [
        ("storage", "valid/ext-advertised-key-gated.json", true),
        (
            "storage",
            "schema-invalid/ext-packed-type-malformed.json",
            false,
        ),
        (
            "descriptor-member",
            "valid/ext-descriptor-member-advertised.json",
            true,
        ),
        (
            "storage",
            "schema-invalid/ext-core-invalid-stays-invalid.json",
            false,
        ),
    ] {
        let compiled = PackLoader::load(&[root.join("packs").join(pack)]).unwrap();
        let trace: Value =
            serde_json::from_slice(&std::fs::read(root.join(trace)).unwrap()).unwrap();
        let results: Vec<_> = trace
            .as_array()
            .unwrap()
            .iter()
            .map(|e| compiled.validate_schema(e, "public-schema-contract"))
            .collect();
        assert_eq!(results.iter().all(Result::is_ok), valid, "{results:?}");
    }
}

#[test]
fn member_errors_point_into_the_original_payload() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let compiled = PackLoader::load(&[root.join("packs/descriptor-member")]).unwrap();
    let mut trace: Value = serde_json::from_slice(
        &std::fs::read(root.join("valid/ext-descriptor-member-advertised.json")).unwrap(),
    )
    .unwrap();
    trace[1]["payload"]["com.example.descriptor.region"] = serde_json::json!(17);
    let diagnostic = compiled
        .validate_schema(&trace[1], "member-pointer")
        .unwrap_err();
    assert_eq!(diagnostic.code, "schema_invalid");
    assert_eq!(diagnostic.pointer, "/payload/com.example.descriptor.region");
}

#[test]
fn schema_checks_leave_the_callers_value_unchanged_and_redact_auth_answers() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/packs/storage");
    let compiled = PackLoader::load(&[root]).unwrap();
    let value = serde_json::json!({
        "protocol":"SENSITIVE_TEST_CODE", "version":"0.1",
        "profile":"open-agent-protocol.agent-control-core",
        "type":"auth.login.reply.request", "id":"reply",
        "payload":{"flow_id":"flow", "prompt_id":"prompt", "answer":"SENSITIVE_TEST_CODE"}
    });
    let original = value.clone();
    let diagnostic = compiled.validate_schema(&value, "redaction").unwrap_err();
    assert_eq!(value, original);
    assert_eq!(
        diagnostic.message,
        "invalid auth reply (sensitive answer redacted)"
    );
    assert!(!serde_json::to_string(&diagnostic)
        .unwrap()
        .contains("SENSITIVE_TEST_CODE"));
}

#[test]
fn primary_member_errors_follow_escaped_pointer_order() {
    struct Temporary(std::path::PathBuf);
    impl Drop for Temporary {
        fn drop(&mut self) {
            std::fs::remove_dir_all(&self.0).unwrap();
        }
    }
    let directory = Temporary(std::env::temp_dir().join(format!(
        "oap-schema-pointers-{}-{}",
        std::process::id(),
        std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    )));
    std::fs::create_dir(&directory.0).unwrap();
    let names = ["com.example.pointer.a/b~c", "com.example.pointer.ab"];
    let members: Vec<_> = names
        .iter()
        .map(|name| {
            serde_json::json!({
                "payload_type":"run.completed", "member":name,
                "schema":{"type":"object", "properties":{"ok":{"const":true}}}
            })
        })
        .collect();
    let gates: Vec<_> = names
        .iter()
        .map(|name| {
            serde_json::json!({
                "payload_type":"run.completed", "member":name, "ungated":true
            })
        })
        .collect();
    std::fs::write(directory.0.join("types.json"), "{}").unwrap();
    std::fs::write(
        directory.0.join("pack.json"),
        serde_json::to_vec(&serde_json::json!({
            "id":"com.example.pointer", "version":"1", "schemas":["types.json"],
            "payload_members":members, "gates":gates
        }))
        .unwrap(),
    )
    .unwrap();
    let compiled = PackLoader::load(std::slice::from_ref(&directory.0)).unwrap();
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures");
    let mut trace: Value =
        serde_json::from_slice(&std::fs::read(root.join("valid/core-completed.json")).unwrap())
            .unwrap();
    let terminal = trace
        .as_array_mut()
        .unwrap()
        .iter_mut()
        .find(|envelope| envelope["type"] == "run.completed")
        .unwrap();
    for name in names {
        terminal["payload"][name] = serde_json::json!({"ok":false});
    }
    let diagnostic = compiled
        .validate_schema(terminal, "ordered-members")
        .unwrap_err();
    assert_eq!(diagnostic.pointer, "/payload/com.example.pointer.ab/ok");
}
