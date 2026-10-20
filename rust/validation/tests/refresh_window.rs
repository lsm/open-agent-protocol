use oap_validation::{codes, Phase, ValidationResult, Validator};
use serde_json::{json, Value};

fn envelope(mut value: Value) -> Value {
    value["protocol"] = json!("open-agent-protocol");
    value["version"] = json!("0.1");
    value["profile"] = json!("open-agent-protocol.agent-control-core");
    value
}

fn stale_in_window(request: Value) -> bool {
    let events = [
        envelope(json!({"type": "capabilities.request", "id": "c1", "payload": {}})),
        envelope(
            json!({"type": "capabilities.response", "id": "c2", "in_reply_to": "c1", "capability_revision": "v1",
            "payload": {"endpoint": {"id": "agent"}, "features": {}}}),
        ),
        envelope(
            json!({"type": "capabilities.updated", "id": "u1", "sequence": 1, "capability_revision": "v2",
            "payload": {"previous_revision": "v1"}}),
        ),
        envelope(request),
    ];
    let trace = events
        .iter()
        .map(Value::to_string)
        .collect::<Vec<_>>()
        .join("\n");
    let result = Validator::new()
        .unwrap()
        .validate(trace.as_bytes(), "refresh-window");
    assert!(
        result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic),
        "{result:?}"
    );
    result
        .diagnostics
        .iter()
        .any(|d| d.code == codes::STALE_CAPABILITY_REVISION && d.envelope_id == "r1")
}

fn request(kind: &str, payload: Value) -> Value {
    let mut request = json!({"type": kind, "id": "r1", "session_id": "s", "payload": payload});
    if let Some(run) = request["payload"].get("run_id").cloned() {
        request["run_id"] = run;
    }
    request
}

#[test]
fn every_request_before_the_refresh_is_stale() {
    assert!(stale_in_window(request(
        "models.request",
        json!({"session_id": "s"})
    )));
    assert!(stale_in_window(request(
        "session.state.request",
        json!({"session_id": "s"})
    )));
}

#[test]
fn discovery_and_the_answers_to_work_in_flight_are_exempt() {
    let run = json!({"session_id": "s", "run_id": "run"});
    let owners = json!({"session_id": "s", "run_id": "run", "interaction_id": "i",
        "requested_by": "agent", "responded_by": "user"});
    let with = |extra: Value| {
        let mut payload = owners.clone();
        payload
            .as_object_mut()
            .unwrap()
            .extend(extra.as_object().unwrap().clone());
        payload
    };
    let exempt = [
        request("capabilities.request", json!({})),
        request("run.cancel.request", run),
        request(
            "action.permission.resolve.request",
            with(json!({"granted": true, "choice_id": "approve"})),
        ),
        request(
            "user.input.resolve.request",
            with(json!({"answers": [{"question_id": "q", "text": "a"}]})),
        ),
        request("user.input.cancel.request", owners.clone()),
        request(
            "action.call.resolve.request",
            with(json!({"tool_call_id": "t", "result": {"ok": true}})),
        ),
    ];
    for request in exempt {
        let kind = request["type"].clone();
        assert!(!stale_in_window(request), "{kind}");
    }
}

#[test]
fn pending_interactions_settle_under_the_previous_descriptor_in_the_refresh_window() {
    let validator = Validator::new().unwrap();
    for (name, trace) in [
        (
            "permission",
            include_str!("../../../fixtures/valid/permission-resolved-in-the-refresh-window.json"),
        ),
        (
            "user-input",
            include_str!("../../../fixtures/valid/user-input-resolved-in-the-refresh-window.json"),
        ),
    ] {
        let result = validator.validate(trace.as_bytes(), name);
        assert!(
            result.valid(),
            "{name}: {}",
            serde_json::to_string_pretty(&result).unwrap()
        );
    }
}

fn fixture(name: &str) -> Vec<Value> {
    let path = std::path::Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures/valid")
        .join(format!("{name}.json"));
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn check(validator: &Validator, trace: &[Value]) -> ValidationResult {
    let result = validator.validate(&serde_json::to_vec(trace).unwrap(), "refresh-window");
    assert!(
        result
            .diagnostics
            .iter()
            .all(|d| d.phase == Phase::Semantic),
        "{result:?}"
    );
    result
}

fn settlement_cases() -> Vec<Vec<Value>> {
    let permission = fixture("permission-resolved-in-the-refresh-window");
    let input = fixture("user-input-resolved-in-the-refresh-window");
    let mut cancel_input = input.clone();
    for e in &mut cancel_input {
        match e["type"].as_str().unwrap() {
            "user.input.requested" => e["payload"]["allow_cancel"] = json!(true),
            "user.input.resolve.request" => {
                e["type"] = json!("user.input.cancel.request");
                e["payload"].as_object_mut().unwrap().remove("answers");
            }
            "user.input.resolve.response" => e["type"] = json!("user.input.cancel.response"),
            "user.input.resolved" => {
                e["payload"]["status"] = json!("cancelled");
                e["payload"].as_object_mut().unwrap().remove("answers");
            }
            _ => {}
        }
    }
    let mut cancel_run = fixture("core-cancelled");
    cancel_run.splice(0..0, permission[..2].iter().cloned());
    let update = permission.iter().find(|e| e["id"] == "capu").unwrap();
    cancel_run.insert(5, update.clone());
    for e in &mut cancel_run {
        if e["type"] == "run.cancel.request" || e["type"] == "run.cancel.response" {
            e["capability_revision"] = json!("v1");
        }
    }
    let mut call = fixture("control-call-late-acknowledgement");
    let mut update = update.clone();
    update["payload"]["previous_revision"] = json!("control-tools-v1");
    call.insert(10, update);
    let mut refresh = call[2..4].to_vec();
    refresh[0]["id"] = json!("refresh-req");
    refresh[1]["id"] = json!("refresh-resp");
    refresh[1]["in_reply_to"] = json!("refresh-req");
    refresh[1]["capability_revision"] = json!("v2");
    call.splice(15..15, refresh);
    for e in &mut call[17..] {
        e["capability_revision"] = json!("v2");
    }
    vec![permission, input, cancel_input, cancel_run, call]
}

#[test]
fn all_five_exempt_exchanges_accept_the_previous_revision_but_reject_an_unrelated_one() {
    let validator = Validator::new().unwrap();
    for trace in settlement_cases() {
        let result = check(&validator, &trace);
        assert!(result.valid(), "{result:?}");
        let mut wrong = trace;
        let update = wrong
            .iter()
            .position(|e| e["type"] == "capabilities.updated")
            .unwrap();
        let mut ids = Vec::new();
        for e in &mut wrong[update + 1..] {
            let kind = e["type"].as_str().unwrap();
            if kind.contains(".resolve.")
                || kind.starts_with("run.cancel.")
                || kind.starts_with("user.input.cancel.")
            {
                e["capability_revision"] = json!("unrelated");
                ids.push(e["id"].as_str().unwrap().to_owned());
            }
        }
        assert!(!ids.is_empty());
        let result = check(&validator, &wrong);
        for id in ids {
            assert!(
                result
                    .diagnostics
                    .iter()
                    .any(|d| d.envelope_id == id && d.code == codes::STALE_CAPABILITY_REVISION),
                "{id}: {result:?}"
            );
        }
    }
}

#[test]
fn settlement_does_not_bypass_the_previous_features_or_the_request_revision_echo() {
    let validator = Validator::new().unwrap();
    for (name, feature, request, response, event) in [
        ("permission", "permissions", "pq", "pr", "e4"),
        ("user-input", "user_input", "iq", "ir", "e3"),
    ] {
        let trace = fixture(&format!("{name}-resolved-in-the-refresh-window"));
        for revision in [None, Some("v2")] {
            let mut changed = trace.clone();
            for e in &mut changed {
                if e["id"] == request || e["id"] == response || e["id"] == event {
                    if let Some(revision) = revision {
                        e["capability_revision"] = json!(revision);
                    } else {
                        e.as_object_mut().unwrap().remove("capability_revision");
                    }
                }
            }
            let result = check(&validator, &changed);
            for id in [request, response, event] {
                assert!(
                    result
                        .diagnostics
                        .iter()
                        .any(|d| d.envelope_id == id && d.code == codes::UNAVAILABLE_CAPABILITY),
                    "{id}: {result:?}"
                );
            }
        }
        let mut unavailable = trace.clone();
        unavailable[1]["payload"]["features"][feature]["level"] = json!("unavailable");
        let result = check(&validator, &unavailable);
        for id in [request, response, event] {
            assert!(
                result
                    .diagnostics
                    .iter()
                    .any(|d| d.envelope_id == id && d.code == codes::UNAVAILABLE_CAPABILITY),
                "{id}: {result:?}"
            );
        }
        let mut wrong_echo = trace;
        let e = wrong_echo.iter_mut().find(|e| e["id"] == response).unwrap();
        e["capability_revision"] = json!("v2");
        let result = check(&validator, &wrong_echo);
        assert!(
            result.diagnostics.iter().any(|d| d.envelope_id == response
                && d.code == codes::STALE_CAPABILITY_REVISION
                && d.expected == "v1"
                && d.actual == "v2"),
            "{result:?}"
        );
    }
}

#[test]
fn a_settling_response_uses_refreshed_features_but_echoes_the_admitted_revision() {
    let validator = Validator::new().unwrap();
    for (name, feature, request, response) in [
        ("permission", "permissions", "pq", "pr"),
        ("user-input", "user_input", "iq", "ir"),
    ] {
        let mut trace = fixture(&format!("{name}-resolved-in-the-refresh-window"));
        let response_at = trace.iter().position(|e| e["id"] == response).unwrap();
        let pending_response = trace.remove(response_at);
        let refreshed = trace.iter().position(|e| e["id"] == "capr2").unwrap();
        trace[refreshed]["payload"]["features"][feature]["level"] = json!("unavailable");
        trace.insert(refreshed + 1, pending_response);
        let result = check(&validator, &trace);
        assert!(
            result
                .diagnostics
                .iter()
                .any(|d| d.envelope_id == response && d.code == codes::UNAVAILABLE_CAPABILITY),
            "{result:?}"
        );
        assert!(
            !result
                .diagnostics
                .iter()
                .any(|d| d.envelope_id == response && d.code == codes::STALE_CAPABILITY_REVISION),
            "{result:?}"
        );
        let request_at = trace.iter().position(|e| e["id"] == request).unwrap();
        let late_request = trace.remove(request_at);
        let response_at = trace.iter().position(|e| e["id"] == response).unwrap();
        trace.insert(response_at, late_request);
        let result = check(&validator, &trace);
        assert!(
            result
                .diagnostics
                .iter()
                .any(|d| d.envelope_id == request && d.code == codes::STALE_CAPABILITY_REVISION),
            "{result:?}"
        );
    }
}
