const { createInterface } = require("node:readline");

let id = 0;
let authenticated = false;
let selectedModel = "fixture/openai-responses@mock";
let opened = {};
let participant;
let submittedModel = "";
const agent = "open-agent-protocol.agent-control-core";
const provider = "open-agent-protocol.model-provider-core";
function send(request, type, payload, scope = {}) {
  process.stdout.write(JSON.stringify({
    protocol: "open-agent-protocol", version: "0.1", profile: request.profile,
    type, id: `server-${++id}`, payload,
    ...(request.id ? { in_reply_to: request.id } : {}), ...scope,
  }) + "\n");
}
function event(profile, type, payload, scope = {}) {
  send({ profile }, type, payload, scope);
}

const SUPPORTED_AUTH = new Set([
  "absent", "null", "number", "empty", "invented",
  "authenticated", "login_required", "expired",
  "refreshing", "login_in_progress", "failed", "unknown",
]);

function fixtureModel() {
  const model = { model_ref: "fixture/openai-responses@mock", model_id: "mock", provider_id: "fixture", wire: "openai-responses", capabilities: ["chat", "streaming"], lifecycle: "stable", source: "discovered", auth_status: "authenticated", cost: { input: 3, output: 15, cache_read: 0.3, cache_write: 3.75 }, input_modalities: ["text", "image"], reasoning_levels: ["off", "medium", "high"], release_date: "2025-09-29", family: "mock-family" };
  switch (process.env.OAP_FIXTURE_SOURCE) {
    case "absent": delete model.source; break;
    case "null": model.source = null; break;
    case "number": model.source = 7; break;
    case "invented": model.source = "invented-source"; break;
    case "fallback": model.source = "fallback"; break;
    case "alias-dynamic": model.source = "dynamic"; break;
    case "alias-static-fallback": model.source = "static_fallback"; break;
    default: break;
  }
  switch (process.env.OAP_FIXTURE_LIFECYCLE) {
    case "absent": delete model.lifecycle; break;
    case "null": model.lifecycle = null; break;
    case "number": model.lifecycle = 7; break;
    case "invented": model.lifecycle = "retired"; break;
    case "preview": model.lifecycle = "preview"; break;
    case "deprecated": model.lifecycle = "deprecated"; break;
    default: break;
  }
  if (process.env.OAP_FIXTURE_ENTRY === "nonobject") return "not-an-object";
  if (process.env.OAP_FIXTURE_ENTRY === "trailing-nonobject") return [model, 7];
  switch (process.env.OAP_FIXTURE_AUTH) {
    case "absent": delete model.auth_status; break;
    case "null": model.auth_status = null; break;
    case "number": model.auth_status = 7; break;
    case "invented": model.auth_status = "retired"; break;
    case "empty": model.auth_status = ""; break;
    case "authenticated": model.auth_status = "authenticated"; break;
    case "login_required": model.auth_status = "login_required"; break;
    case "expired": model.auth_status = "expired"; break;
    case "refreshing": model.auth_status = "refreshing"; break;
    case "login_in_progress": model.auth_status = "login_in_progress"; break;
    case "failed": model.auth_status = "failed"; break;
    case "unknown": model.auth_status = "unknown"; break;
    default: break;
  }
  const selector = process.env.OAP_FIXTURE_AUTH;
  if (selector && !SUPPORTED_AUTH.has(selector)) {
    throw new Error(`unsupported OAP_FIXTURE_AUTH selector: ${selector}`);
  }
  return model;
}
createInterface({ input: process.stdin }).on("line", (line) => {
  const request = JSON.parse(line);
  if (request.protocol !== "open-agent-protocol" || request.version !== "0.1" || !request.id) {
    process.exitCode = 2;
    return;
  }
  switch (`${request.profile}:${request.type}`) {
    case `${agent}:protocol.initialize.request`:
      participant = request.payload.participant?.id;
      send(request, "protocol.initialize.response", { protocol_version: "0.1", profile: agent, endpoint: { id: "fixture" } });
      break;
    case `${agent}:capabilities.request`:
      send(request, "capabilities.response", process.env.OAP_FIXTURE_BARE
        ? { endpoint: { id: "oapx.agent-control" }, features: { "auth.providers": true, "auth.login": true } }
        : { endpoint: { id: "oapx.agent" }, features: { "auth.providers": true, "auth.login": true, "action.tools.provide": { level: "native" } } }, { capability_revision: "r1" });
      break;
    case `${agent}:auth.providers.request`:
      send(request, "auth.providers.response", { providers: [{ id: "fixture", name: "Fixture", auth_kinds: ["api_key"], auth_status: authenticated ? "authenticated" : "login_required" }] });
      break;
    case `${agent}:auth.login.start.request`:
      send(request, "auth.login.start.response", { flow_id: "flow-1" });
      event(agent, "auth.login.event", { flow_id: "flow-1", provider_id: request.payload.provider_id, kind: "url", url: "https://example.invalid/login" }, { sequence: 1 });
      if (request.payload.provider_id === "manual") {
        event(agent, "auth.login.event", { flow_id: "flow-1", provider_id: "manual", kind: "prompt", prompt_id: "prompt-1", message: "Enter code", allow_empty: false }, { sequence: 2 });
        break;
      }
      event(agent, "auth.login.event", { flow_id: "flow-1", provider_id: request.payload.provider_id, kind: "progress", message: "Login completed in browser" }, { sequence: 2 });
      authenticated = true;
      event(agent, "auth.login.completed", { flow_id: "flow-1", provider_id: "fixture", status: "success" }, { sequence: 3 });
      break;
    case `${agent}:auth.login.cancel.request`:
      send(request, "auth.login.cancel.response", { flow_id: request.payload.flow_id, accepted: true });
      event(agent, "auth.login.completed", { flow_id: request.payload.flow_id, provider_id: "fixture", status: "cancelled" }, { sequence: 3 });
      break;
    case `${provider}:provider.describe.request`:
      send(request, "provider.describe.response", { providers: [], protocol_versions: ["0.1"] });
      break;
    case `${provider}:provider.models.list.request`:
      send(request, "provider.models.list.response", {
        models: Array.isArray(fixtureModel()) ? fixtureModel() : [fixtureModel()],
        catalog: { observed_at_ms: 1_759_100_000_000, complete: true },
      });
      break;
    case `${provider}:inference.create.request`:
      if (request.payload.model_ref.endsWith("@malformed-frame")) {
        process.stdout.write('{"answer":"secret-code",\n');
        break;
      }
      if ((request.payload.model_ref.endsWith("@needs-login") || request.payload.model_ref.endsWith("@credential-rejected")) && !authenticated) {
        send(request, "inference.create.response", { accepted: false, error: { code: request.payload.model_ref.endsWith("@credential-rejected") ? "credential_rejected" : "credential_missing", message: "login required" } });
        break;
      }
      send(request, "inference.create.response", { accepted: true }, { inference_id: "inf-1" });
      event(provider, "inference.started", { model_ref: request.payload.model_ref, started_at_ms: 0 }, { inference_id: "inf-1", sequence: 1 });
      event(provider, "inference.part.started", { part_index: 0, part_kind: "text" }, { inference_id: "inf-1", sequence: 2 });
      event(provider, "inference.part.delta", { part_index: 0, delta: "provider works" }, { inference_id: "inf-1", sequence: 3 });
      event(provider, "inference.part.ended", { part_index: 0, part_kind: "text", text: "provider works" }, { inference_id: "inf-1", sequence: 4 });
      if (request.payload.model_ref.endsWith("@structured")) {
        event(provider, "inference.part.started", { part_index: 1, part_kind: "tool_call", tool_call_id: "call-1", name: "lookup" }, { inference_id: "inf-1", sequence: 5 });
        event(provider, "inference.part.delta", { part_index: 1, delta: '{"city":' }, { inference_id: "inf-1", sequence: 6 });
        event(provider, "inference.part.ended", { part_index: 1, part_kind: "tool_call", tool_call: { tool_call_id: "call-1", name: "lookup", arguments_json: { city: "Paris" } } }, { inference_id: "inf-1", sequence: 7 });
      }
      event(provider, "inference.completed", { message: { role: "assistant", content: request.payload.model_ref.endsWith("@echo")
        ? JSON.stringify(request.payload)
        : request.payload.model_ref.endsWith("@structured")
          ? [
            { type: "reasoning", reasoning: "thinking", carry: "reasoning-carry" },
            { type: "tool_call", tool_call_id: "call-1", name: "lookup", arguments_json: { city: "Paris" }, carry: "tool-carry" },
            { type: "tool_result", tool_call_id: "call-0", result: "prior result", is_error: false },
            { type: "image", image: { data: "aGVsbG8=", media_type: "image/png" } },
            { type: "text", text: "answer" },
          ]
          : "provider works" }, stop_reason: "end_turn", usage: { input_tokens: 1, output_tokens: 2 } }, { inference_id: "inf-1", sequence: request.payload.model_ref.endsWith("@structured") ? 8 : 5 });
      break;
    case `${agent}:session.open.request`:
      opened = request.payload;
      send(request, "session.open.response", { session_id: request.payload.session_id, status: "idle" }, { session_id: request.payload.session_id });
      break;
    case `${agent}:session.message.submit.request`:
      if (request.payload.session_id === "existing-session" && request.payload.model_id !== undefined) {
        send(request, "error.response", { error: { code: "invalid_request", message: "selected run must omit model_id" } });
        break;
      }
      if (request.payload.model_id?.endsWith("@needs-login") && !authenticated) {
        send(request, "error.response", { error: { code: "auth_required", message: "login required" } });
        break;
      }
      send(request, "session.message.submit.response", { session_id: request.payload.session_id, accepted: true, run_id: "run-1", submission_id: "sub-1", requested_delivery: "auto", effective_delivery: "start", admission: "started" }, { session_id: request.payload.session_id });
      event(agent, "run.started", { session_id: request.payload.session_id, run_id: "run-1", model_id: request.payload.model_id || selectedModel }, { session_id: request.payload.session_id, run_id: "run-1", sequence: 1 });
      submittedModel = request.payload.model_id ?? "";
      if (submittedModel.endsWith("@tool") || submittedModel.endsWith("@tool-then-login")) {
        event(agent, "action.call.requested", { session_id: request.payload.session_id, run_id: "run-1", tool_call_id: "call-1", name: "lookup", execution_owner: participant, interaction_id: "interaction-1", requested_by: "fixture", responded_by: participant, arguments_json: { word: "oap" } }, { session_id: request.payload.session_id, run_id: "run-1", tool_call_id: "call-1", sequence: 2 });
        break;
      }
      if (request.payload.model_id?.endsWith("@settings")) {
        const said = `reasoning=${opened.reasoning_level} output=${opened.metadata?.oapx?.output} user_input=${opened.metadata?.oapx?.user_input} participant=${participant}`;
        event(agent, "run.completed", { session_id: request.payload.session_id, run_id: "run-1", final_response: { role: "assistant", content: said }, stop_reason: "end_turn" }, { session_id: request.payload.session_id, run_id: "run-1", sequence: 2 });
        break;
      }
      event(agent, "content.delta", { session_id: request.payload.session_id, run_id: "run-1", part: { type: "text", text: "agent works" } }, { session_id: request.payload.session_id, run_id: "run-1", sequence: 2 });
      event(agent, "run.completed", { session_id: request.payload.session_id, run_id: "run-1", final_response: { role: "assistant", content: "agent works" }, model_id: request.payload.model_id || selectedModel, stop_reason: "end_turn", usage: { input_tokens: 2, output_tokens: 3 } }, { session_id: request.payload.session_id, run_id: "run-1", sequence: 3 });
      break;
    case `${agent}:action.call.resolve.request`: {
      const answer = request.payload;
      const scope = { session_id: answer.session_id, run_id: "run-1", tool_call_id: "call-1" };
      send(request, "action.call.resolve.response", { interaction_id: answer.interaction_id, session_id: answer.session_id, run_id: "run-1", tool_call_id: answer.tool_call_id, accepted: true }, { session_id: answer.session_id, run_id: "run-1" });
      event(agent, "action.call.started", { session_id: answer.session_id, run_id: "run-1", tool_call_id: "call-1", name: "lookup" }, { ...scope, sequence: 3 });
      event(agent, "action.call.completed", { session_id: answer.session_id, run_id: "run-1", tool_call_id: "call-1", name: "lookup", result: answer.result }, { ...scope, sequence: 4 });
      if (submittedModel.endsWith("@tool-then-login")) {
        event(agent, "run.failed", { session_id: answer.session_id, run_id: "run-1", error: { code: "credential_missing", message: "login required" } }, { session_id: answer.session_id, run_id: "run-1", sequence: 5 });
        break;
      }
      const said = `${opened.tools?.[0]?.name} owned by ${opened.tools?.[0]?.execution_owner} said ${answer.result} (error ${answer.error?.message}) as ${answer.responded_by}`;
      event(agent, "run.completed", { session_id: answer.session_id, run_id: "run-1", final_response: { role: "assistant", content: said }, stop_reason: "end_turn" }, { session_id: answer.session_id, run_id: "run-1", sequence: 5 });
      break;
    }
    case `${agent}:session.model.switch.request`:
      selectedModel = request.payload.model_id;
      send(request, "session.model.switch.response", { session_id: request.payload.session_id, model_id: request.payload.model_id }, { session_id: request.payload.session_id });
      break;
    default:
      send(request, "error.response", { error: { code: "unsupported_feature", message: request.type } });
  }
});
