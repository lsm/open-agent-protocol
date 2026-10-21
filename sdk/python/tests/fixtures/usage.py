"""Combined-profile stdio endpoint with a caller-selected terminal usage value."""

import json
import sys
from typing import Any

AGENT = "open-agent-protocol.agent-control-core"
PROVIDER = "open-agent-protocol.model-provider-core"
raw = sys.argv[1]
site = sys.argv[2] if len(sys.argv) > 2 else "completed"
usage = {} if raw in ("absent", "ordinary-negative-zero") else {"usage": "RAW_USAGE_COUNT_OBJECT"}
serial = 0


def emit(profile: str, kind: str, reply: str | None = None, **fields: Any) -> None:
    global serial
    serial += 1
    frame = {"protocol": "open-agent-protocol", "version": "0.1", "profile": profile,
             "type": kind, "id": f"fixture-{serial}", **fields}
    if reply is not None:
        frame["in_reply_to"] = reply
    print(json.dumps(frame).replace('"RAW_USAGE_COUNT_OBJECT"', raw)
          .replace('"RAW_ORDINARY_NUMBERS"', '{"input_tokens":-0,"nested":{"usage":{"input_tokens":-0}},"string":"-0"}'), flush=True)


for line in sys.stdin:
    request = json.loads(line)
    kind, rid = request["type"], request["id"]
    if kind == "protocol.initialize.request":
        emit(AGENT, "protocol.initialize.response", rid, payload={
            "protocol_version": "0.1", "profile": AGENT, "endpoint": {"id": "fixture"}})
    elif kind == "capabilities.request":
        emit(AGENT, "capabilities.response", rid, capability_revision="usage-1", payload={
            "endpoint": {"id": "fixture"}, "features": {}})
    elif kind == "inference.create.request":
        scope = {"inference_id": f"inference-{serial}"}
        emit(PROVIDER, "inference.create.response", rid, **scope, payload={"accepted": True})
        emit(PROVIDER, "inference.started", **scope, payload={"model_ref": request["payload"]["model_ref"]})
        if site == "inference.failed":
            emit(PROVIDER, site, **scope, payload={
                "error": {"code": "fixture_error", "message": "fixture failure"}, **usage})
        else:
            emit(PROVIDER, "inference.completed", **scope, payload={
                "message": {"role": "assistant", "content": "usage"}, "stop_reason": "stop",
                **(usage if site == "completed" else {})})
    elif kind == "session.open.request":
        sid = request["payload"]["session_id"]
        emit(AGENT, "session.open.response", rid, session_id=sid,
             payload={"session_id": sid, "status": "idle"})
    elif kind == "session.message.submit.request":
        scope = {"session_id": request["payload"]["session_id"], "run_id": f"run-{serial}"}
        emit(AGENT, "session.message.submit.response", rid, session_id=scope["session_id"],
             payload={"accepted": True, "run_id": scope["run_id"]})
        emit(AGENT, "run.started", **scope, payload={**scope, "model_id": "fixture/other@usage"})
        if raw == "ordinary-negative-zero":
            emit(AGENT, "action.call.completed", **scope, payload={**scope,
                "tool_call_id": "fixture-call", "result": "RAW_ORDINARY_NUMBERS"})
        if site == "content.completed":
            emit(AGENT, site, **scope, payload={**scope, "message_id": "fixture-message", **usage})
        if site in ("run.failed", "run.cancelled"):
            emit(AGENT, site, **scope, payload={**scope,
                "error": {"code": "fixture_error", "message": "fixture failure"}, **usage})
        else:
            emit(AGENT, "run.completed", **scope, payload={**scope,
                "final_response": {"role": "assistant", "content": "usage"}, "stop_reason": "stop",
                **(usage if site == "completed" else {})})
    elif kind not in ("inference.cancel.request", "run.cancel.request"):
        raise AssertionError(f"unexpected request: {kind}")
