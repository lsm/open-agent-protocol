use std::path::Path;

use oap_validation::Validator;
use serde_json::{json, Value};

#[test]
fn descriptors_reject_attachment_members_even_when_empty() {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/semantic-invalid/descriptor-leaks-attachment-fields.json");
    let original: Vec<Value> = serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap();
    let validator = Validator::new().unwrap();
    for pointer in ["/payload/sources/0", "/payload/layers/action/sources/0"] {
        for (member, value) in [
            ("command", json!("")),
            ("args", json!([])),
            ("environment", json!([])),
            ("environment", json!(["SYNTHETIC_TOKEN=do-not-echo"])),
        ] {
            let mut trace = original.clone();
            trace[1]["payload"]["sources"][0]
                .as_object_mut()
                .unwrap()
                .remove("environment");
            trace[1]["payload"]["layers"]["action"]["sources"][0]
                .as_object_mut()
                .unwrap()
                .remove("command");
            trace[1].pointer_mut(pointer).unwrap()[member] = value;
            let result =
                validator.validate(&serde_json::to_vec(&trace).unwrap(), "descriptor-fields");
            assert_eq!(result.diagnostics.len(), 1, "{:?}", result.diagnostics);
            let diagnostic = &result.diagnostics[0];
            assert_eq!(diagnostic.code, "attachment_field_in_catalog");
            assert_eq!(diagnostic.pointer, format!("{pointer}/{member}"));
            assert!(!serde_json::to_string(&result)
                .unwrap()
                .contains("do-not-echo"));
        }
    }
}
