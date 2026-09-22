"""Combined-profile OAP wire tests; the fake host never speaks Makai V1."""

import sys
import unittest

from oap_sdk import AuthFlowHandlers, AuthOptions, MakaiProtocolError, MakaiStreamError, RunOptions, ToolDefinition, connect
from oap_sdk._oap import _messages


HOST = r'''
import json, sys
A = "open-agent-protocol.agent-control-core"
P = "open-agent-protocol.model-provider-core"
def emit(profile, kind, reply=None, scope=None, payload=None):
    message = {"protocol":"open-agent-protocol", "version":"0.1", "profile":profile,
               "type":kind, "id":"host-"+kind, "payload":payload or {}}
    if reply: message["in_reply_to"] = reply
    if scope: message.update(scope)
    print(json.dumps(message), flush=True)
auth_ready = False
for line in sys.stdin:
    request = json.loads(line)
    assert request["protocol"] == "open-agent-protocol"
    assert request["version"] == "0.1"
    kind, rid = request["type"], request["id"]
    if kind == "protocol.initialize.request":
        emit(A, "protocol.initialize.response", rid, payload={"protocol_version":"0.1", "profile":A,
             "endpoint":{"id":"fixture"}})
    elif kind == "provider.models.list.request":
        emit(P, "provider.models.list.response", rid, payload={"models":[{
             "model_ref":"fixture/other:test@ok", "model_id":"ok", "provider_id":"fixture",
             "wire":"other", "auth_status":"authenticated", "lifecycle":"stable",
             "source":"fallback", "capabilities":["chat", "streaming"]}]})
    elif kind == "inference.create.request":
        scope={"inference_id":"inf-1"}
        emit(P, "inference.create.response", rid, scope, {"accepted":True})
        if request["payload"]["model_ref"].endswith("@parts"):
            emit(P, "inference.completed", scope=scope, payload={
                "message":{"role":"assistant","content":[
                    {"type":"text","text":"before "},
                    {"type":"reasoning","reasoning":"thought","carry":"sig"},
                    {"type":"image","image":{"url":"https://example.invalid/image.png"}},
                    {"type":"tool_call","tool_call_id":"call-1","name":"weather",
                     "arguments_json":{"city":"SF"},"carry":"opaque"}]},
                "stop_reason":"tool_use"})
            continue
        if request["payload"]["model_ref"].endswith("@unknown-part"):
            emit(P, "inference.completed", scope=scope, payload={
                "message":{"role":"assistant","content":[{"type":"future_part","value":1}]},
                "stop_reason":"stop"})
            continue
        emit(P, "inference.started", scope=scope, payload={"model_ref":"fixture/other:test@ok"})
        if request["payload"]["model_ref"].endswith("@auth-once") and not auth_ready:
            emit(P, "inference.failed", scope=scope, payload={"error":{
                 "code":"auth_required", "message":"login required"}})
            continue
        emit(P, "inference.part.started", scope=scope, payload={"part_index":0,"part_kind":"text"})
        emit(P, "inference.part.delta", scope=scope, payload={"part_index":0,"delta":"hello"})
        emit(P, "inference.completed", scope=scope, payload={"message":{"role":"assistant","content":"hello"},
             "stop_reason":"stop","usage":{"input_tokens":1,"output_tokens":2}})
    elif kind == "session.open.request":
        sid=request["payload"].get("session_id") or "session-1"
        emit(A, "session.open.response", rid, {"session_id":sid}, {"session_id":sid,"status":"idle"})
    elif kind == "session.model.switch.request":
        sid=request["payload"]["session_id"]
        emit(A, "session.model.switch.response", rid, {"session_id":sid},
             {"session_id":sid,"model_id":request["payload"]["model_id"]})
    elif kind == "session.message.submit.request":
        sid=request["payload"]["session_id"]
        scope={"session_id":sid,"run_id":"run-1"}
        emit(A, "session.message.submit.response", rid, {"session_id":sid},
             {"accepted":True,"run_id":"run-1", "model_id":request["payload"].get("model_id", "fixture/other:test@ok")})
        emit(A, "run.started", scope=scope, payload={"session_id":sid,"run_id":"run-1",
             "model_id":request["payload"].get("model_id", "fixture/other:test@ok")})
        if request["payload"].get("model_id", "").endswith("@auth-once") and not auth_ready:
            emit(A, "run.failed", scope=scope, payload={"session_id":sid,"run_id":"run-1",
                 "error":{"code":"credential_missing","message":"login required"}})
            continue
        emit(A, "content.delta", scope=scope, payload={"session_id":sid,"run_id":"run-1",
             "part":{"type":"text","text":"agent"}})
        emit(A, "run.completed", scope=scope, payload={"session_id":sid,"run_id":"run-1",
             "final_response":{"role":"assistant","content":"agent"},
             "model_id":"fixture/other:test@ok", "stop_reason":"stop"})
    elif kind == "auth.providers.request":
        emit(A, "auth.providers.response", rid, payload={"providers":[{
             "id":"fixture","name":"Fixture","auth_status":"login_required"}]})
    elif kind == "auth.login.start.request":
        emit(A, "auth.login.start.response", rid, payload={"flow_id":"flow-1"})
        emit(A, "auth.login.event", scope={"sequence":1}, payload={
             "flow_id":"flow-1","provider_id":"fixture","kind":"url","url":"https://example.invalid/auth"})
        emit(A, "auth.login.event", scope={"sequence":2}, payload={
             "flow_id":"flow-1","provider_id":"fixture","kind":"prompt",
             "prompt_id":"prompt-1","message":"Enter code","allow_empty":False})
    elif kind == "auth.login.reply.request":
        assert request["payload"]["answer"] == "test-code"
        auth_ready = True
        emit(A, "auth.login.reply.response", rid, payload={
             "flow_id":"flow-1","prompt_id":"prompt-1","accepted":True})
        emit(A, "auth.login.completed", scope={"sequence":3}, payload={
             "flow_id":"flow-1","provider_id":"fixture","status":"success"})
    elif kind == "auth.login.cancel.request":
        emit(A, "auth.login.cancel.response", rid, payload={"flow_id":"flow-1","accepted":True})
    elif kind == "session.provider.attach.request":
        emit(A, "error.response", rid, payload={"error":{
             "code":"unsupported_feature", "message":"attachment not supported"}})
    elif kind in ("inference.cancel.request", "run.cancel.request"):
        pass
    else:
        raise AssertionError("unexpected OAP request: " + kind)
'''


class OAPWireTests(unittest.IsolatedAsyncioTestCase):
    async def test_combined_profile_models_provider_and_agent(self) -> None:
        async with connect(command=sys.executable, args=["-u", "-c", HOST], legacy_wire=False) as client:
            models = await client.models.list()
            self.assertEqual(models.models[0].model_ref, "fixture/other:test@ok")
            self.assertEqual(models.models[0].source, "static_fallback")
            response = await client.provider.complete(
                model_ref="fixture/other:test@ok", messages=[{"role": "user", "content": "hi"}])
            self.assertEqual(response.text, "hello")
            structured = await client.provider.complete(
                model_ref="fixture/other:test@parts", messages=[{"role": "user", "content": "hi"}])
            self.assertEqual(structured.text, "before ")
            self.assertEqual(structured.stop_reason, "tool_use")
            self.assertIsInstance(structured.message.content, list)
            parts = structured.message.content
            assert isinstance(parts, list)
            self.assertEqual([part["type"] for part in parts],
                             ["text", "thinking", "image", "tool_call"])
            self.assertEqual(parts[1]["thinking_signature"], "sig")  # type: ignore[typeddict-item]
            self.assertEqual(parts[2]["url"], "https://example.invalid/image.png")  # type: ignore[typeddict-item]
            self.assertEqual(parts[3]["arguments_json"], '{"city": "SF"}')  # type: ignore[typeddict-item]
            self.assertEqual(parts[3]["carry"], "opaque")  # type: ignore[typeddict-item]
            replay = _messages([{"role": "assistant", "content": parts}])
            replay_parts = replay[0]["content"]
            self.assertEqual(replay_parts[1]["type"], "reasoning")
            self.assertEqual(replay_parts[1]["carry"], "sig")
            self.assertEqual(replay_parts[2]["image"]["url"], "https://example.invalid/image.png")
            self.assertEqual(replay_parts[3]["arguments_json"], {"city": "SF"})
            self.assertEqual(replay_parts[3]["carry"], "opaque")
            with self.assertRaises(MakaiProtocolError) as unsupported_part:
                await client.provider.complete(model_ref="fixture/other:test@unknown-part",
                    messages=[{"role": "user", "content": "hi"}])
            self.assertEqual(unsupported_part.exception.code, "unsupported_feature")
            events = [event async for event in client.provider.stream(
                model_ref="fixture/other:test@ok", messages=[{"role": "user", "content": "hi"}])]
            self.assertEqual([event.type for event in events],
                             ["message_start", "text_delta", "message_end"])
            opened = await client.agent.open_session("session-1")
            self.assertEqual(opened["session_id"], "session-1")
            switched = await client.agent.switch_model("session-1", "fixture/other:test@ok")
            self.assertEqual(switched["model_id"], "fixture/other:test@ok")
            agent_response = await client.agent.run(
                messages=[{"role": "user", "content": "hi"}],
                options=RunOptions(session_id="session-1"))
            self.assertEqual(agent_response.text, "agent")
            with self.assertRaises(MakaiProtocolError) as unsupported_tools:
                await client.agent.run(model_ref="fixture/other:test@ok",
                    messages=[{"role": "user", "content": "hi"}],
                    tools=[ToolDefinition(name="tool", description="", parameters_schema_json="{}")])
            self.assertEqual(unsupported_tools.exception.code, "unsupported_feature")
            with self.assertRaises(MakaiProtocolError) as unsupported_options:
                await client.agent.run(model_ref="fixture/other:test@ok",
                    messages=[{"role": "user", "content": "hi"}],
                    options=RunOptions(max_tokens=10))
            self.assertEqual(unsupported_options.exception.code, "unsupported_feature")
            with self.assertRaises(MakaiStreamError) as unsupported_attachment:
                await client.agent.attach_provider("session-1", {"id": "alias", "provider_id": "fixture"})
            self.assertEqual(unsupported_attachment.exception.code, "unsupported_feature")
            providers = await client.auth.list_providers()
            self.assertEqual(providers[0].auth_status, "login_required")
            received = []
            await client.auth.login("fixture", AuthFlowHandlers(
                on_event=lambda event: received.append(event.type),
                on_prompt=lambda _: "test-code"))
            self.assertEqual(received, ["auth_url", "prompt", "success"])

    async def test_auto_once_retries_only_typed_auth_failure(self) -> None:
        handlers = AuthFlowHandlers(on_prompt=lambda _: "test-code")
        async with connect(command=sys.executable, args=["-u", "-c", HOST], legacy_wire=False,
                           auth=AuthOptions(auth_retry_policy="auto_once", handlers=handlers)) as client:
            response = await client.provider.complete(
                model_ref="fixture/other:test@auth-once", messages=[{"role": "user", "content": "hi"}])
            self.assertEqual(response.text, "hello")

    async def test_agent_auto_once_retries_typed_run_failure(self) -> None:
        handlers = AuthFlowHandlers(on_prompt=lambda _: "test-code")
        async with connect(command=sys.executable, args=["-u", "-c", HOST], legacy_wire=False,
                           auth=AuthOptions(auth_retry_policy="auto_once", handlers=handlers)) as client:
            response = await client.agent.run(
                model_ref="fixture/other:test@auth-once", messages=[{"role": "user", "content": "hi"}])
            self.assertEqual(response.text, "agent")


if __name__ == "__main__":
    unittest.main()
