use std::sync::Arc;

use oap_binding::MemoryStore;
use oap_serve::endpoint::{Endpoint, EndpointError, EndpointOptions};
use oap_serve::{Hub, HubOptions, Registry};
use serde_json::Value;

fn endpoint(hub: &Hub, frame_limit: usize) -> Endpoint {
    Endpoint::new(
        hub,
        EndpointOptions {
            adapter: "memory".into(),
            frame_limit,
        },
    )
    .unwrap()
}

fn run(hub: &Hub, input: &str) -> (Vec<Value>, Result<(), EndpointError>) {
    run_within(hub, input, 0)
}

fn run_within(hub: &Hub, input: &str, limit: usize) -> (Vec<Value>, Result<(), EndpointError>) {
    let mut out = Vec::new();
    let result = endpoint(hub, limit).run(input.as_bytes(), &mut out);
    let lines = String::from_utf8(out)
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str(line).unwrap())
        .collect();
    (lines, result)
}

fn request(kind: &str, id: &str, revision: Option<&str>) -> String {
    let mut envelope = serde_json::json!({
        "protocol": "oap",
        "version": "0.1",
        "profile": "open-agent-protocol.agent-control-core",
        "type": kind,
        "id": id,
        "payload": {},
    });
    if let Some(revision) = revision {
        envelope["capability_revision"] = revision.into();
    }
    envelope.to_string()
}

#[test]
fn an_endpoint_needs_an_adapter_its_registry_holds() {
    let hub = Hub::new(Registry::default_registry());
    let made = |adapter: &str| {
        Endpoint::new(
            &hub,
            EndpointOptions {
                adapter: adapter.into(),
                ..EndpointOptions::default()
            },
        )
        .err()
        .map(|err| err.to_string())
    };
    assert_eq!(
        made("").as_deref(),
        Some("serveendpoint: an adapter name is required")
    );
    assert_eq!(
        made("ghost").as_deref(),
        Some("serveendpoint: no adapter \"ghost\"")
    );
    assert_eq!(made("memory"), None);
}

#[test]
fn initialize_answers_the_version_profile_and_revision_it_serves() {
    let hub = Hub::new(Registry::default_registry());
    let (answered, result) = run(
        &hub,
        &request("protocol.initialize.request", "init-1", Some("old")),
    );
    result.unwrap();
    let answer = &answered[0];
    assert_eq!(answer["type"], "protocol.initialize.response", "{answer}");
    assert_eq!(answer["in_reply_to"], "init-1");
    assert_eq!(answer["payload"]["protocol_version"], "0.1");
    assert_eq!(
        answer["payload"]["profile"],
        "open-agent-protocol.agent-control-core"
    );
    assert_eq!(
        answer["capability_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
}

#[test]
fn capabilities_add_stdio_and_list_sessions_only_with_a_history() {
    let plain = Hub::new(Registry::default_registry());
    let (answered, _) = run(&plain, &request("capabilities.request", "c", None));
    let payload = &answered[0]["payload"];
    assert_eq!(answered[0]["type"], "capabilities.response");
    assert_eq!(
        payload["bindings"],
        serde_json::json!([{"kind": "stdio", "serialization": "jsonl"}])
    );
    assert!(
        payload["features"].get("session.list").is_none(),
        "{payload}"
    );
    let recorded = Hub::with_options(
        Registry::default_registry(),
        HubOptions {
            bindings: Some(Arc::new(MemoryStore::default())),
            ..HubOptions::default()
        },
    );
    let (answered, _) = run(&recorded, &request("capabilities.request", "c", None));
    assert_eq!(
        answered[0]["payload"]["features"]["session.list"]["level"],
        "emulated"
    );
}

#[test]
fn a_stale_revision_is_refused_on_every_request_but_discovery() {
    let hub = Hub::new(Registry::default_registry());
    let (answered, _) = run(
        &hub,
        &[
            request("capabilities.request", "c", Some("old")),
            request("session.state.request", "s", Some("old")),
        ]
        .join("\n"),
    );
    assert_eq!(answered[0]["type"], "capabilities.response");
    let error = &answered[1]["payload"]["error"];
    assert_eq!(answered[1]["in_reply_to"], "s");
    assert_eq!(error["code"], "stale_capabilities", "{error}");
    assert_eq!(error["details"]["expected_revision"], "old");
    assert_eq!(
        error["details"]["current_revision"],
        oap_adapter::memory::CAPABILITY_REVISION
    );
}

#[test]
fn a_request_it_does_not_serve_or_one_missing_members_is_refused_and_serving_goes_on() {
    let hub = Hub::new(Registry::default_registry());
    let (answered, result) = run(
        &hub,
        &[
            request("endpoint.ping.request", "p", None),
            "".into(),
            r#"{"protocol":"oap","id":"m","session_id":"s"}"#.into(),
            r#"{"control":"later","id":"k"}"#.into(),
            request("capabilities.request", "c", None),
        ]
        .join("\n"),
    );
    result.unwrap();
    assert_eq!(
        answered[0]["payload"]["error"]["code"],
        "unsupported_request"
    );
    assert_eq!(
        answered[0]["payload"]["error"]["message"],
        "this endpoint serves no endpoint.ping.request"
    );
    assert_eq!(answered[1]["payload"]["error"]["code"], "invalid_request");
    assert_eq!(
        answered[1]["payload"]["error"]["message"],
        "the envelope omits required member(s): version, profile, type, payload"
    );
    assert_eq!(answered[1]["in_reply_to"], "m");
    assert_eq!(answered[1]["session_id"], "s");
    assert_eq!(answered[2]["control"], "replay.error");
    assert_eq!(answered[2]["code"], "unsupported_control");
    assert_eq!(answered[2]["id"], "k");
    assert_eq!(answered[3]["type"], "capabilities.response");
}

#[test]
fn a_line_that_cannot_be_answered_ends_serving() {
    let hub = Hub::new(Registry::default_registry());
    for line in [
        "not json",
        r#"{"type":"capabilities.request","id":"x"}"#,
        r#"{"protocol":"oap","type":"capabilities.request"}"#,
    ] {
        let (answered, result) = run(&hub, line);
        assert!(answered.is_empty(), "{line}");
        assert!(
            matches!(result, Err(EndpointError::MalformedLine(_))),
            "{line}: {result:?}"
        );
    }
    let (_, result) = run_within(&hub, &request("capabilities.request", "c", None), 64);
    assert!(
        matches!(result, Err(EndpointError::FrameTooLarge(_))),
        "{result:?}"
    );
}
