use std::path::PathBuf;

use oap_validation::{Phase, Validator};
use serde_json::Value;

fn fixture(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(name);
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn judge(trace: &Value) -> Vec<(String, usize, String)> {
    let result = Validator::new()
        .unwrap()
        .validate(&serde_json::to_vec(trace).unwrap(), "tool-catalog");
    assert!(result
        .diagnostics
        .iter()
        .all(|d| d.phase == Phase::Semantic));
    result
        .diagnostics
        .into_iter()
        .map(|d| (d.code, d.index, d.pointer))
        .collect()
}

#[test]
fn descriptor_sources_are_unique_across_top_level_and_layers() {
    for name in [
        "descriptor-duplicate-source-id",
        "descriptor-source-id-repeated-across-layers",
    ] {
        let trace = fixture(&format!("semantic-invalid/{name}.json"));
        assert_eq!(
            judge(&trace),
            [("duplicate_tool_source".into(), 1, "/payload/sources".into())],
            "{name}"
        );
    }
}

#[test]
fn effective_tool_names_are_unique_before_a_catalog_is_requested() {
    let mut trace = fixture("semantic-invalid/descriptor-duplicate-tool-name.json");
    trace.as_array_mut().unwrap().truncate(2);
    assert_eq!(
        judge(&trace),
        [("duplicate_tool_name".into(), 1, "/payload/tools".into())]
    );
}

#[test]
fn descriptor_tools_resolve_only_sources_in_the_same_descriptor() {
    for name in [
        "tools-descriptor-dangling-source",
        "descriptor-layer-tool-dangling-source",
    ] {
        let trace = fixture(&format!("semantic-invalid/{name}.json"));
        assert_eq!(
            judge(&trace),
            [("unmatched_tool_source".into(), 1, "/payload/tools".into())],
            "{name}"
        );
    }
    assert!(judge(&fixture("valid/descriptor-tool-without-source.json")).is_empty());
}
