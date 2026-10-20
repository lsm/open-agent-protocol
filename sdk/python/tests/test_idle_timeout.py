"""A run may go quiet for longer than a request's reply timeout; only an opt-in idle timeout ends it."""

import os
import sys
import tempfile
import unittest

from oap_sdk import connect


SLOW_HOST = r'''
import json, os, sys, time
A = "open-agent-protocol.agent-control-core"
P = "open-agent-protocol.model-provider-core"
PAUSE = float(os.environ["SLOW_HOST_PAUSE"])
LOG = os.environ["SLOW_HOST_LOG"]
def emit(profile, kind, reply=None, scope=None, payload=None):
    message = {"protocol":"open-agent-protocol", "version":"0.1", "profile":profile,
               "type":kind, "id":"host-"+kind, "payload":payload or {}}
    if reply: message["in_reply_to"] = reply
    if scope: message.update(scope)
    print(json.dumps(message), flush=True)
for line in sys.stdin:
    request = json.loads(line)
    kind, rid = request["type"], request["id"]
    with open(LOG, "a") as log:
        log.write(kind + "\n")
    if kind == "protocol.initialize.request":
        emit(A, "protocol.initialize.response", rid, payload={"protocol_version":"0.1", "profile":A, "endpoint":{"id":"fixture"}})
    elif kind == "capabilities.request":
        message = {"protocol":"open-agent-protocol", "version":"0.1", "profile":A, "type":"capabilities.response",
                   "id":"host-capabilities", "in_reply_to":rid, "capability_revision":"fixture-rev-1",
                   "payload":{"endpoint":{"id":"oapx.agent"}, "features":{}}}
        print(json.dumps(message), flush=True)
    elif kind == "inference.create.request":
        scope = {"inference_id":"inf-1"}
        emit(P, "inference.create.response", rid, scope, {"accepted":True})
        emit(P, "inference.started", scope=scope, payload={"model_ref":"fixture/other:test@ok"})
        time.sleep(PAUSE)
        emit(P, "inference.completed", scope=scope, payload={"message":{"role":"assistant","content":"thought long"}, "stop_reason":"stop"})
    elif kind == "session.open.request":
        sid = request["payload"].get("session_id") or "session-1"
        emit(A, "session.open.response", rid, {"session_id":sid}, {"session_id":sid, "status":"idle"})
    elif kind == "session.message.submit.request":
        sid = request["payload"]["session_id"]
        scope = {"session_id":sid, "run_id":"run-1"}
        emit(A, "session.message.submit.response", rid, {"session_id":sid}, {"accepted":True, "run_id":"run-1"})
        emit(A, "run.started", scope=scope, payload={"session_id":sid, "run_id":"run-1"})
        time.sleep(PAUSE)
        emit(A, "run.completed", scope=scope, payload={"session_id":sid, "run_id":"run-1",
             "final_response":{"role":"assistant","content":"thought long"}, "stop_reason":"stop"})
'''


class QuietRunTests(unittest.IsolatedAsyncioTestCase):
    def host(self, pause: float) -> dict:
        log = tempfile.NamedTemporaryFile(delete=False)
        log.close()
        self.addCleanup(os.unlink, log.name)
        self.log = log.name
        return {**os.environ, "SLOW_HOST_PAUSE": str(pause), "SLOW_HOST_LOG": log.name}

    async def test_an_agent_run_quiet_for_longer_than_the_response_timeout_still_completes(self) -> None:
        async with connect(command=sys.executable, args=["-u", "-c", SLOW_HOST], env=self.host(0.8),
                           response_timeout=0.3) as client:
            response = await client.agent.run(model_ref="fixture/other:test@ok",
                                              messages=[{"role": "user", "content": "think"}])
            self.assertEqual(response.text, "thought long")

    async def test_an_inference_quiet_for_longer_than_the_response_timeout_still_completes(self) -> None:
        async with connect(command=sys.executable, args=["-u", "-c", SLOW_HOST], env=self.host(0.8),
                           response_timeout=0.3) as client:
            response = await client.provider.complete(model_ref="fixture/other:test@ok",
                                                      messages=[{"role": "user", "content": "think"}])
            self.assertEqual(response.text, "thought long")
