mod support;

use std::sync::Arc;

use futures::executor::block_on;
use oap_binding::MemoryStore;
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::Value;

fn lines(hub: &Hub, input: &[&str]) -> Vec<Value> {
    let input: String = input.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        std::io::Cursor::new(input.to_owned().into_bytes()),
        output.clone(),
        &StdioOptions::default(),
    )
    .unwrap();
    String::from_utf8(output.bytes())
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect()
}

fn recorded(ids: &[&str]) -> Hub {
    let hub = Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    );
    for id in ids {
        block_on(hub.open(
            "memory",
            oap_adapter::OpenRequest {
                session_id: (*id).into(),
                ..oap_adapter::OpenRequest::default()
            },
        ))
        .unwrap();
    }
    hub
}

#[test]
fn capabilities_answers_the_adapters_descriptor_under_its_revision() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[r#"{"id":1,"op":"capabilities","adapter":"memory"}"#],
    );
    let response = &answered[0]["result"];
    assert_eq!(response["type"], "capabilities.response", "{}", answered[0]);
    assert_eq!(
        response["capability_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
    assert!(response["in_reply_to"]
        .as_str()
        .unwrap()
        .starts_with("oap-request-"));
    assert!(response["payload"]["features"].is_object());
}

#[test]
fn capabilities_needs_an_adapter_it_knows_and_no_other_parameter() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"capabilities"}"#,
            r#"{"id":2,"op":"capabilities","adapter":"ghost"}"#,
            r#"{"id":3,"op":"capabilities","adapter":"memory","session_id":"s"}"#,
        ],
    );
    assert_eq!(answered[0]["error"]["code"], "invalid_request");
    assert_eq!(answered[0]["error"]["message"], "adapter is required");
    assert_eq!(answered[1]["error"]["code"], "unknown_adapter");
    assert_eq!(answered[2]["error"]["code"], "invalid_request");
}

#[test]
fn history_on_a_hub_keeping_none_is_an_unadvertised_session_list() {
    let hub = Hub::new(Registry::default_registry());
    let answered = lines(&hub, &[r#"{"id":1,"op":"history"}"#]);
    let error = &answered[0]["error"];
    assert_eq!(error["code"], "unsupported_feature", "{error}");
    assert_eq!(error["details"]["feature"], "session.list");
    assert_eq!(error["details"]["reason"], "unadvertised");
}

#[test]
fn history_pages_the_recorded_sessions_by_cursor() {
    let hub = recorded(&["a", "b", "c"]);
    let first = lines(&hub, &[r#"{"id":1,"op":"history","limit":2}"#]);
    let page = &first[0]["result"];
    assert_eq!(
        page["sessions"].as_array().unwrap().len(),
        2,
        "{}",
        first[0]
    );
    assert_eq!(page["sessions"][0]["state"], "live");
    assert_eq!(page["sessions"][0]["adapter"], "memory");
    let cursor = page["next_cursor"].as_str().unwrap().to_owned();
    let next = format!(r#"{{"id":2,"op":"history","limit":2,"cursor":"{cursor}"}}"#);
    let second = lines(&hub, &[next.as_str()]);
    let rest = &second[0]["result"];
    assert_eq!(
        rest["sessions"].as_array().unwrap().len(),
        1,
        "{}",
        second[0]
    );
    assert!(rest.get("next_cursor").is_none());
    let mut seen: Vec<Value> = page["sessions"]
        .as_array()
        .unwrap()
        .iter()
        .chain(rest["sessions"].as_array().unwrap())
        .map(|entry| entry["session_id"].clone())
        .collect();
    seen.sort_by_key(|id| id.to_string());
    assert_eq!(seen, ["a", "b", "c"]);
}

#[test]
fn history_refuses_a_cursor_it_did_not_mint_and_a_limit_out_of_range() {
    let hub = recorded(&["a"]);
    let answered = lines(
        &hub,
        &[
            r#"{"id":1,"op":"history","cursor":"not-a-cursor"}"#,
            r#"{"id":2,"op":"history","limit":0}"#,
            r#"{"id":3,"op":"history","limit":101}"#,
            r#"{"id":4,"op":"history","session_id":"a"}"#,
        ],
    );
    assert_eq!(
        answered[0]["error"]["code"], "invalid_cursor",
        "{}",
        answered[0]
    );
    assert_eq!(answered[1]["error"]["code"], "invalid_request");
    assert_eq!(answered[2]["error"]["code"], "invalid_request");
    assert_eq!(answered[3]["error"]["code"], "invalid_request");
}
