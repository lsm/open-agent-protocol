"""A configurable fake ``oapx serve agent,provider --stdio`` host.

One script driven by a JSON config so the matrix of scenarios does not need a
file each. It answers ``protocol.initialize.request`` and
``capabilities.request`` itself, then replies to every other envelope from the
config.

The config path comes from ``OAP_SDK_FAKE_CONFIG``. Recognised keys:

``handshake``
    ``"ready"`` (default), ``"error"``, ``"bad_version"``, ``"silent"``, or
    ``"garbage"`` (a non-JSON line instead of the initialize response).
``log``
    Path to append every received envelope to, one JSON object per line.
``handlers``
    ``{envelope_type: [reply, ...]}``. Replies are emitted in order.
``exit_after``
    Exit the process after this many envelopes past the handshake,
    simulating a crash.

A reply is a dict with:

``type``
    Envelope type to emit.
``profile``
    Profile to emit it under; defaults to the request's.
``payload``
    Payload object. ``"$session_id"`` / ``"$request_id"`` anywhere inside it
    is substituted from the request.
``scope``
    Envelope members to set, such as ``inference_id``, ``session_id`` or
    ``sequence``, substituted the same way.
``correlate``
    ``true`` (default) to set ``in_reply_to`` to the request's ``id``.
``delay_ms``
    Sleep before emitting.
``raw``
    Write this string verbatim instead of building an envelope, for
    malformed-line coverage.
"""

from __future__ import annotations

import itertools
import json
import os
import sys
import time
from typing import Any, Dict, List, Optional

AGENT = "open-agent-protocol.agent-control-core"
_ids = itertools.count(1)


def emit(frame: Dict[str, Any]) -> None:
    sys.stdout.write(json.dumps(frame) + "\n")
    sys.stdout.flush()


def envelope(profile: str, kind: str, payload: Dict[str, Any], **members: Any) -> Dict[str, Any]:
    return {"protocol": "open-agent-protocol", "version": "0.1", "profile": profile,
            "type": kind, "id": f"host-{next(_ids)}", "payload": payload, **members}


def load_config() -> Dict[str, Any]:
    path = os.environ.get("OAP_SDK_FAKE_CONFIG")
    if not path:
        return {}
    with open(path, encoding="utf-8") as handle:
        data: Dict[str, Any] = json.load(handle)
    return data


def append_log(path: Optional[str], frame: Dict[str, Any]) -> None:
    if not path:
        return
    with open(path, "a", encoding="utf-8") as handle:
        handle.write(json.dumps(frame) + "\n")


def substitute(value: Any, request: Dict[str, Any]) -> Any:
    if isinstance(value, str):
        if value == "$session_id":
            payload = request.get("payload") or {}
            return request.get("session_id") or payload.get("session_id")
        if value == "$request_id":
            return request.get("id")
        return value
    if isinstance(value, dict):
        return {key: substitute(item, request) for key, item in value.items()}
    if isinstance(value, list):
        return [substitute(item, request) for item in value]
    return value


def emit_replies(request: Dict[str, Any], specs: List[Dict[str, Any]]) -> None:
    for spec in specs:
        delay_ms = spec.get("delay_ms", 0)
        if delay_ms:
            time.sleep(delay_ms / 1000.0)
        if "raw" in spec:
            sys.stdout.write(spec["raw"] + "\n")
            sys.stdout.flush()
            continue
        members = substitute(spec.get("scope", {}), request)
        if spec.get("correlate", True):
            members["in_reply_to"] = request.get("id")
        emit(envelope(spec.get("profile") or request.get("profile") or AGENT, spec["type"],
                      substitute(spec.get("payload", {}), request), **members))


def handshake(request: Dict[str, Any], mode: str) -> None:
    kind = request.get("type")
    if kind == "protocol.initialize.request":
        if mode == "silent":
            return
        if mode == "garbage":
            sys.stdout.write("{not json\n")
            sys.stdout.flush()
            return
        if mode == "error":
            emit(envelope(AGENT, "error.response", {"error": {"code": "version_mismatch", "message": "unsupported protocol"}},
                          in_reply_to=request.get("id")))
            return
        version = "99" if mode == "bad_version" else "0.1"
        emit(envelope(AGENT, "protocol.initialize.response", {"protocol_version": version, "profile": AGENT,
                                                              "endpoint": {"id": "fixture"}}, in_reply_to=request.get("id")))
        return
    emit(envelope(AGENT, "capabilities.response", {"endpoint": {"id": "oapx.agent"}, "features": {}},
                  in_reply_to=request.get("id"), capability_revision="fixture-rev-1"))


def main() -> None:
    config = load_config()
    log_path = config.get("log")
    mode = config.get("handshake", "ready")
    handlers: Dict[str, List[Dict[str, Any]]] = config.get("handlers", {})
    exit_after = config.get("exit_after")
    received = 0

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            request = json.loads(line)
        except json.JSONDecodeError:
            continue
        if not isinstance(request, dict):
            continue
        append_log(log_path, request)

        kind = request.get("type")
        if kind in ("protocol.initialize.request", "capabilities.request"):
            handshake(request, mode)
            continue

        received += 1
        if exit_after is not None and received >= exit_after:
            sys.stdout.flush()
            os._exit(1)
        if kind in handlers:
            emit_replies(request, handlers[kind])


if __name__ == "__main__":
    main()
