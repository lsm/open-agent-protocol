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
    let mut authenticated = false;
    let mut selected_model = "fixture/openai-responses@mock".to_owned();
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
        let data = &request["payload"];
        match (profile, kind) {
            (AGENT, "protocol.initialize.request") => emit(
                profile,
                "protocol.initialize.response",
                Some(id),
                json!({
                    "protocol_version": "0.1", "profile": AGENT, "endpoint": { "id": "fixture" }
                }),
                json!({}),
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
                emit(
                    profile,
                    "auth.login.event",
                    None,
                    json!({ "flow_id": "flow-1", "provider_id": data["provider_id"], "kind": "prompt", "prompt_id": "prompt-1", "message": "Enter code", "allow_empty": false }),
                    json!({ "sequence": 2 }),
                );
            }
            (AGENT, "auth.login.reply.request") => {
                emit(
                    profile,
                    "auth.login.reply.response",
                    Some(id),
                    json!({ "flow_id": data["flow_id"], "prompt_id": data["prompt_id"], "accepted": true }),
                    json!({}),
                );
                authenticated = data["answer"] == "code";
                let completed = if authenticated {
                    json!({ "flow_id": data["flow_id"], "provider_id": "fixture", "status": "success" })
                } else {
                    json!({ "flow_id": data["flow_id"], "provider_id": "fixture", "status": "failed", "error": { "code": "bad_code", "message": "wrong code" } })
                };
                emit(
                    profile,
                    "auth.login.completed",
                    None,
                    completed,
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
            (PROVIDER, "provider.models.list.request") => emit(
                profile,
                "provider.models.list.response",
                Some(id),
                json!({
                    "models": [{ "model_ref": "fixture/openai-responses@mock", "model_id": "mock", "provider_id": "fixture", "wire": "openai-responses", "auth_status": "authenticated", "source": "discovered", "lifecycle": "stable", "capabilities": ["chat", "streaming"] }]
                }),
                json!({}),
            ),
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
            (AGENT, "session.open.request") => emit(
                profile,
                "session.open.response",
                Some(id),
                json!({ "session_id": data["session_id"], "status": "idle" }),
                json!({ "session_id": data["session_id"] }),
            ),
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
