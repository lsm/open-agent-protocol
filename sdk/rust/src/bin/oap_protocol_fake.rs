use std::io::{self, BufRead, Write};

use serde_json::{json, Value};

const AGENT: &str = "open-agent-protocol.agent-control-core";
const PROVIDER: &str = "open-agent-protocol.model-provider-core";

fn emit(profile: &str, kind: &str, reply: Option<&str>, payload: Value, scope: Value) {
    let mut frame = json!({
        "protocol": "open-agent-protocol", "version": "0.1", "profile": profile,
        "type": kind, "id": format!("fixture-{kind}"), "payload": payload,
    });
    if let Some(reply) = reply {
        frame["in_reply_to"] = json!(reply);
    }
    if let Some(obj) = scope.as_object() {
        for (key, value) in obj {
            frame[key] = value.clone();
        }
    }
    println!("{frame}");
    let _ = io::stdout().flush();
}

fn main() {
    let shape = std::env::args()
        .nth(1)
        .and_then(|arg| arg.strip_prefix("--catalog-shape=").map(str::to_owned))
        .unwrap_or_else(|| "stated".to_owned());
    let mut authenticated = false;
    let mut selected_model = "fixture/openai-responses@mock".to_owned();
    let mut opened = Value::Null;
    let mut participant = Value::Null;
    for line in io::stdin().lock().lines() {
        let Ok(line) = line else { break };
        let Ok(request) = serde_json::from_str::<Value>(&line) else {
            break;
        };
        let Some(profile) = request.get("profile").and_then(Value::as_str) else {
            break;
        };
        let Some(kind) = request.get("type").and_then(Value::as_str) else {
            break;
        };
        let Some(id) = request.get("id").and_then(Value::as_str) else {
            break;
        };
        if request.get("protocol").and_then(Value::as_str) != Some("open-agent-protocol") {
            break;
        }
        if profile == AGENT
            && kind != "protocol.initialize.request"
            && kind != "capabilities.request"
            && request.get("capability_revision").and_then(Value::as_str) != Some("fixture-rev-1")
        {
            break;
        }
        let data = &request["payload"];
        match (profile, kind) {
            (AGENT, "protocol.initialize.request") => {
                participant = data["participant"]["id"].clone();
                emit(
                    profile,
                    "protocol.initialize.response",
                    Some(id),
                    json!({
                        "protocol_version": "0.1", "profile": AGENT, "endpoint": { "id": "fixture" }
                    }),
                    json!({}),
                );
            }
            (AGENT, "capabilities.request") => emit(
                profile,
                "capabilities.response",
                Some(id),
                json!({ "features": {} }),
                json!({ "capability_revision": "fixture-rev-1" }),
            ),
            (AGENT, "auth.providers.request") => emit(
                profile,
                "auth.providers.response",
                Some(id),
                json!({ "providers": [{ "id": "fixture", "name": "Fixture", "auth_status": if authenticated { "authenticated" } else { "login_required" } }] }),
                json!({}),
            ),
            (AGENT, "auth.login.start.request") => {
                emit(
                    profile,
                    "auth.login.start.response",
                    Some(id),
                    json!({ "flow_id": "flow-1" }),
                    json!({}),
                );
                emit(
                    profile,
                    "auth.login.event",
                    None,
                    json!({ "flow_id": "flow-1", "provider_id": data["provider_id"], "kind": "url", "url": "https://example.invalid/login" }),
                    json!({ "sequence": 1 }),
                );
                if data["provider_id"] == "manual" {
                    emit(
                        profile,
                        "auth.login.event",
                        None,
                        json!({ "flow_id": "flow-1", "provider_id": "manual", "kind": "prompt", "prompt_id": "prompt-1", "message": "Enter code", "allow_empty": false }),
                        json!({ "sequence": 2 }),
                    );
                    continue;
                }
                emit(
                    profile,
                    "auth.login.event",
                    None,
                    json!({ "flow_id": "flow-1", "provider_id": data["provider_id"], "kind": "progress", "message": "Login completed in browser" }),
                    json!({ "sequence": 2 }),
                );
                authenticated = true;
                emit(
                    profile,
                    "auth.login.completed",
                    None,
                    json!({ "flow_id": "flow-1", "provider_id": "fixture", "status": "success" }),
                    json!({ "sequence": 3 }),
                );
            }
            (AGENT, "auth.login.cancel.request") => {
                emit(
                    profile,
                    "auth.login.cancel.response",
                    Some(id),
                    json!({ "flow_id": data["flow_id"], "accepted": true }),
                    json!({}),
                );
                emit(
                    profile,
                    "auth.login.completed",
                    None,
                    json!({ "flow_id": data["flow_id"], "provider_id": "fixture", "status": "cancelled" }),
                    json!({ "sequence": 3 }),
                );
            }
            (PROVIDER, "provider.describe.request") => emit(
                profile,
                "provider.describe.response",
                Some(id),
                json!({
                    "providers": [], "protocol_versions": ["0.1"]
                }),
                json!({}),
            ),
            (PROVIDER, "provider.models.list.request") => {
                let mut entry = json!({
                    "model_ref": "fixture/openai-responses@mock", "model_id": "mock", "provider_id": "fixture",
                    "wire": "openai-responses", "auth_status": "authenticated",
                    "capabilities": ["chat", "streaming"]
                });
                match shape.as_str() {
                    "absent-source" => {}
                    "null-source" => entry["source"] = Value::Null,
                    "number-source" => entry["source"] = json!(7),
                    "invented-source" => entry["source"] = json!("invented-source"),
                    "fallback" => entry["source"] = json!("fallback"),
                    _ => entry["source"] = json!("discovered"),
                }
                match shape.as_str() {
                    "absent-lifecycle" => {
                        let _ = entry.as_object_mut().map(|o| o.remove("lifecycle"));
                    }
                    "null-lifecycle" => entry["lifecycle"] = Value::Null,
                    "invented-lifecycle" => entry["lifecycle"] = json!("retired"),
                    "preview-lifecycle" => entry["lifecycle"] = json!("preview"),
                    "deprecated-lifecycle" => entry["lifecycle"] = json!("deprecated"),
                    _ => entry["lifecycle"] = json!("stable"),
                }
                match shape.as_str() {
                    "absent-auth" => {
                        let _ = entry.as_object_mut().map(|o| o.remove("auth_status"));
                    }
                    "null-auth" => entry["auth_status"] = Value::Null,
                    "number-auth" => entry["auth_status"] = json!(7),
                    "invented-auth" => entry["auth_status"] = json!("retired"),
                    other => {
                        if let Some(literal) = other.strip_prefix("stated-auth-") {
                            entry["auth_status"] = json!(literal);
                        }
                    }
                }
                emit(
                    profile,
                    "provider.models.list.response",
                    Some(id),
                    json!({ "models": [entry] }),
                    json!({}),
                )
            }
            (PROVIDER, "inference.create.request") => {
                let auth_rejection =
                    data.get("model_ref")
                        .and_then(Value::as_str)
                        .and_then(|model| {
                            if model.ends_with("@needs-login") {
                                Some("credential_missing")
                            } else if model.ends_with("@credential-rejected") {
                                Some("credential_rejected")
                            } else {
                                None
                            }
                        });
                if let Some(code) = auth_rejection.filter(|_| !authenticated) {
                    emit(
                        profile,
                        "inference.create.response",
                        Some(id),
                        json!({ "accepted": false, "error": { "code": code, "message": "login required" } }),
                        json!({}),
                    );
                    continue;
                }
                emit(
                    profile,
                    "inference.create.response",
                    Some(id),
                    json!({ "accepted": true }),
                    json!({ "inference_id": "inf-1" }),
                );
                emit(
                    profile,
                    "inference.started",
                    None,
                    json!({ "model_ref": data["model_ref"], "started_at_ms": 0 }),
                    json!({ "inference_id": "inf-1", "sequence": 1 }),
                );
                emit(
                    profile,
                    "inference.part.started",
                    None,
                    json!({ "part_index": 0, "part_kind": "text" }),
                    json!({ "inference_id": "inf-1", "sequence": 2 }),
                );
                emit(
                    profile,
                    "inference.part.delta",
                    None,
                    json!({ "part_index": 0, "delta": "provider works" }),
                    json!({ "inference_id": "inf-1", "sequence": 3 }),
                );
                let structured = data
                    .get("model_ref")
                    .and_then(Value::as_str)
                    .is_some_and(|model| model.ends_with("@structured"));
                if structured {
                    emit(
                        profile,
                        "inference.part.started",
                        None,
                        json!({ "part_index": 1, "part_kind": "tool_call", "tool_call_id": "call-1", "name": "lookup" }),
                        json!({ "inference_id": "inf-1", "sequence": 4 }),
                    );
                    emit(
                        profile,
                        "inference.part.delta",
                        None,
                        json!({ "part_index": 1, "delta": "{\"city\":" }),
                        json!({ "inference_id": "inf-1", "sequence": 5 }),
                    );
                    emit(
                        profile,
                        "inference.part.ended",
                        None,
                        json!({ "part_index": 1, "part_kind": "tool_call", "tool_call": { "tool_call_id": "call-1", "name": "lookup", "arguments_json": { "city": "Paris" } } }),
                        json!({ "inference_id": "inf-1", "sequence": 6 }),
                    );
                }
                let content = match data.get("model_ref").and_then(Value::as_str) {
                    Some(model) if model.ends_with("@echo") => json!(data.to_string()),
                    Some(model) if model.ends_with("@structured") => json!([
                        { "type": "reasoning", "reasoning": "thinking", "carry": "reasoning-carry" },
                        { "type": "tool_call", "tool_call_id": "call-1", "name": "lookup", "arguments_json": { "city": "Paris" }, "carry": "tool-carry" },
                        { "type": "tool_result", "tool_call_id": "call-0", "result": "prior result", "is_error": false },
                        { "type": "image", "image": { "data": "aGVsbG8=", "media_type": "image/png" } },
                        { "type": "text", "text": "answer" }
                    ]),
                    _ => json!("provider works"),
                };
                emit(
                    profile,
                    "inference.completed",
                    None,
                    json!({ "message": { "role": "assistant", "content": content }, "stop_reason": "stop", "usage": { "input_tokens": 1, "output_tokens": 2 } }),
                    json!({ "inference_id": "inf-1", "sequence": if structured { 7 } else { 4 } }),
                );
            }
            (AGENT, "session.open.request") => {
                opened = data.clone();
                emit(
                    profile,
                    "session.open.response",
                    Some(id),
                    json!({ "session_id": data["session_id"], "status": "idle" }),
                    json!({ "session_id": data["session_id"] }),
                );
            }
            (AGENT, "action.call.resolve.request") => {
                let scope = json!({ "session_id": data["session_id"], "run_id": "run-1" });
                emit(
                    profile,
                    "action.call.resolve.response",
                    Some(id),
                    json!({ "interaction_id": data["interaction_id"], "session_id": data["session_id"], "run_id": "run-1", "tool_call_id": data["tool_call_id"], "accepted": true }),
                    json!({ "session_id": data["session_id"] }),
                );
                emit(
                    profile,
                    "action.call.started",
                    None,
                    json!({ "session_id": data["session_id"], "run_id": "run-1", "tool_call_id": "call-1", "name": "lookup" }),
                    scope.clone(),
                );
                emit(
                    profile,
                    "action.call.completed",
                    None,
                    json!({ "session_id": data["session_id"], "run_id": "run-1", "tool_call_id": "call-1", "name": "lookup", "result": data["result"] }),
                    scope.clone(),
                );
                let said = format!(
                    "{} owned by {} said {} (error {}) as {}",
                    opened["tools"][0]["name"],
                    opened["tools"][0]["execution_owner"],
                    data["result"],
                    data["error"]["message"],
                    data["responded_by"]
                );
                emit(
                    profile,
                    "run.completed",
                    None,
                    json!({ "session_id": data["session_id"], "run_id": "run-1", "final_response": { "role": "assistant", "content": said }, "stop_reason": "end_turn" }),
                    scope,
                );
            }
            (AGENT, "session.message.submit.request") => {
                if data.get("session_id").and_then(Value::as_str) == Some("existing-session")
                    && data.get("model_id").is_some()
                {
                    emit(
                        profile,
                        "error.response",
                        Some(id),
                        json!({ "error": { "code": "invalid_request", "message": "selected run must omit model_id" } }),
                        json!({}),
                    );
                    continue;
                }
                let effective_model = data
                    .get("model_id")
                    .and_then(Value::as_str)
                    .unwrap_or(&selected_model);
                if effective_model.ends_with("@needs-login") && !authenticated {
                    emit(
                        profile,
                        "error.response",
                        Some(id),
                        json!({ "error": { "code": "auth_required", "message": "login required" } }),
                        json!({}),
                    );
                    continue;
                }
                let scope = json!({ "session_id": data["session_id"], "run_id": "run-1" });
                emit(
                    profile,
                    "session.message.submit.response",
                    Some(id),
                    json!({ "session_id": data["session_id"], "accepted": true, "run_id": "run-1", "submission_id": "sub-1", "requested_delivery": "auto", "effective_delivery": "start", "admission": "started" }),
                    json!({ "session_id": data["session_id"] }),
                );
                if effective_model.ends_with("@tool") {
                    emit(
                        profile,
                        "run.started",
                        None,
                        json!({ "session_id": data["session_id"], "run_id": "run-1", "model_id": effective_model }),
                        scope.clone(),
                    );
                    emit(
                        profile,
                        "action.call.requested",
                        None,
                        json!({ "session_id": data["session_id"], "run_id": "run-1", "tool_call_id": "call-1", "name": "lookup", "execution_owner": participant, "interaction_id": "interaction-1", "requested_by": "fixture", "responded_by": participant, "arguments_json": { "word": "oap" } }),
                        scope,
                    );
                    continue;
                }
                if effective_model.ends_with("@settings") {
                    let said = format!(
                        "reasoning={} output={} user_input={} participant={}",
                        opened["reasoning_level"],
                        opened["metadata"]["oapx"]["output"],
                        opened["metadata"]["oapx"]["user_input"],
                        participant
                    );
                    emit(
                        profile,
                        "run.completed",
                        None,
                        json!({ "session_id": data["session_id"], "run_id": "run-1", "final_response": { "role": "assistant", "content": said }, "stop_reason": "end_turn" }),
                        scope,
                    );
                    continue;
                }
                emit(
                    profile,
                    "run.started",
                    None,
                    json!({ "session_id": data["session_id"], "run_id": "run-1", "model_id": effective_model }),
                    scope.clone(),
                );
                emit(
                    profile,
                    "content.delta",
                    None,
                    json!({ "session_id": data["session_id"], "run_id": "run-1", "part": { "type": "text", "text": "agent works" } }),
                    scope.clone(),
                );
                emit(
                    profile,
                    "run.completed",
                    None,
                    json!({ "session_id": data["session_id"], "run_id": "run-1", "final_response": { "role": "assistant", "content": "agent works" }, "model_id": effective_model, "stop_reason": "end_turn", "usage": { "input_tokens": 2, "output_tokens": 3 } }),
                    scope,
                );
            }
            (AGENT, "session.model.switch.request") => {
                selected_model = data
                    .get("model_id")
                    .and_then(Value::as_str)
                    .unwrap_or_default()
                    .to_owned();
                emit(
                    profile,
                    "session.model.switch.response",
                    Some(id),
                    json!({ "session_id": data["session_id"], "model_id": data["model_id"] }),
                    json!({ "session_id": data["session_id"] }),
                );
            }
            _ => emit(
                profile,
                "error.response",
                Some(id),
                json!({ "error": { "code": "unsupported_feature", "message": kind } }),
                json!({}),
            ),
        }
    }
}
