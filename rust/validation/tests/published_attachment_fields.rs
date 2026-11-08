use std::path::Path;

use oap_validation::Validator;
use serde_json::{json, Value};

#[test]
fn every_session_source_publication_rejects_attachment_fields() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/semantic-invalid/tools-catalog-leaks-attachment-env.json");
    let original: Vec<Value> = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let validator = Validator::tolerant().unwrap();
    for kind in [
        "action.tools.list.response",
        "session.open.response",
        "session.state.response",
        "session.state.updated",
    ] {
        let mut clean = original.clone();
        clean[5]["payload"]["sources"][1]
            .as_object_mut()
            .unwrap()
            .remove("environment");
        let index = if kind == "session.open.response" {
            clean.truncate(4);
            3
        } else if kind.starts_with("session.state.") {
            clean[4]["type"] = json!("session.state.request");
            clean[5]["type"] = json!(kind);
            clean[5]["payload"].as_object_mut().unwrap().remove("tools");
            clean[5]["payload"]["status"] = json!("idle");
            if kind == "session.state.updated" {
                clean[5]["sequence"] = json!(1);
                clean[5].as_object_mut().unwrap().remove("in_reply_to");
                clean.remove(4);
                4
            } else {
                5
            }
        } else {
            5
        };
        let valid = validator.validate(&serde_json::to_vec(&clean).unwrap(), kind);
        assert!(
            valid.diagnostics.is_empty(),
            "{kind}: {:?}",
            valid.diagnostics
        );
        for (member, value) in [
            ("command", json!("synthetic-command")),
            ("args", json!([])),
            ("environment", json!(["SYNTHETIC_TOKEN=do-not-echo"])),
        ] {
            let mut trace = clean.clone();
            trace[index]["payload"]["sources"][1][member] = value;
            let result = validator.validate(&serde_json::to_vec(&trace).unwrap(), kind);
            assert_eq!(
                result.diagnostics.len(),
                1,
                "{kind}: {:?}",
                result.diagnostics
            );
            assert_eq!(result.diagnostics[0].code, "attachment_field_in_catalog");
            assert_eq!(
                result.diagnostics[0].pointer,
                format!("/payload/sources/1/{member}")
            );
            assert!(!serde_json::to_string(&result)
                .unwrap()
                .contains("do-not-echo"));
        }
    }
}
