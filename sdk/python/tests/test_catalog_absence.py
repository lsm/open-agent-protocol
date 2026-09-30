"""OAP catalogue absence: one fake process per case, driven through the public client."""

import json
import os
import sys
import unittest
from typing import List, Optional, Sequence, Tuple, Union

from oap_sdk import MakaiProtocolError, connect
from oap_sdk.types import ListModelsResponse

HOST = r'''
import json, os, sys

A = "open-agent-protocol.agent-control-core"
P = "open-agent-protocol.model-provider-core"
model = {"model_ref": "fixture/other:abs@ok", "model_id": "ok", "provider_id": "fixture",
         "wire": "other", "auth_status": "authenticated",
         "capabilities": ["chat", "streaming"]}
shape = os.environ.get("OAP_PY_FIXTURE_SHAPE", "")
stamped = os.environ.get("OAP_PY_FIXTURE_SET", "")
for member in ("lifecycle", "source"):
    if f"{member}_{shape}" in stamped:
        model[member] = json.loads(os.environ[f"{member}_{shape}"])
    elif f"absent-{member}" == shape:
        pass
    elif f"null-{member}" == shape:
        model[member] = None


def emit(profile, kind, rid, payload, scope=None):
    message = {"protocol": "open-agent-protocol", "version": "0.1", "profile": profile,
               "type": kind, "id": "host-1", "in_reply_to": rid, "payload": payload}
    if scope:
        message.update(scope)
    print(json.dumps(message), flush=True)


for line in sys.stdin:
    request = json.loads(line)
    assert request["protocol"] == "open-agent-protocol"
    assert request["version"] == "0.1"
    kind, rid = request["type"], request["id"]
    if kind == "protocol.initialize.request":
        emit(A, "protocol.initialize.response", rid,
             {"protocol_version": "0.1", "profile": A, "endpoint": {"id": "fixture"}})
    elif kind == "capabilities.request":
        print(json.dumps({"protocol": "open-agent-protocol", "version": "0.1", "profile": A,
                          "type": "capabilities.response", "id": "host-capabilities",
                          "in_reply_to": rid, "capability_revision": "fixture-rev-1",
                          "payload": {"features": {}}}), flush=True)
    elif kind == "provider.models.list.request":
        emit(P, "provider.models.list.response", rid,
             {"models": [model], "catalog": {"observed_at_ms": 1, "complete": True}})
    else:
        emit(A, kind.replace(".request", ".response"), rid, {})
'''

class _Absent:
    pass


_ABSENT = _Absent()
_Member = Union[str, None, _Absent]


class CatalogAbsence(unittest.IsolatedAsyncioTestCase):
    async def _list(
        self,
        key: str,
        lifecycle: _Member,
        source: _Member,
        include_deprecated: Optional[bool] = None,
    ) -> ListModelsResponse:
        env = dict(os.environ)
        env["OAP_PY_FIXTURE_SHAPE"] = key
        stamped = []
        members: Sequence[Tuple[str, _Member]] = (("lifecycle", lifecycle), ("source", source))
        for member, raw in members:
            if raw is not _ABSENT:
                env[f"{member}_{key}"] = str(raw)
                stamped.append(f"{member}_{key}")
        env["OAP_PY_FIXTURE_SET"] = ",".join(stamped)
        async with connect(command=sys.executable, args=["-u", "-c", HOST],
                           legacy_wire=False, env=env) as client:
            return await client.models.list(include_deprecated=include_deprecated)

    async def test_absent_members_read_as_unknown(self) -> None:
        listed = await self._list("absent", _ABSENT, _ABSENT)
        self.assertIsNone(listed.models[0].lifecycle)
        self.assertIsNone(listed.models[0].source)

    async def test_a_stated_member_keeps_its_mapping(self) -> None:
        listed = await self._list("stated", '"deprecated"', '"fallback"')
        model = listed.models[0]
        self.assertEqual(model.lifecycle, "deprecated")
        self.assertEqual(model.source, "static_fallback")

        listed = await self._list("preview", '"preview"', '"discovered"')
        model = listed.models[0]
        self.assertEqual(model.lifecycle, "preview")
        self.assertEqual(model.source, "dynamic")

    async def test_a_present_null_is_refused(self) -> None:
        for key, member in (("null-lifecycle", "lifecycle"), ("null-source", "source")):
            with self.assertRaises(MakaiProtocolError) as caught:
                await self._list(key, "null" if member == "lifecycle" else _ABSENT,
                                 "null" if member == "source" else _ABSENT)
            self.assertEqual(caught.exception.code, "malformed_response")
            self.assertIn(member, str(caught.exception).lower())

    async def test_an_unrecognised_member_is_refused(self) -> None:
        for key, raw in (("invented-lifecycle", '"retired"'), ("number-lifecycle", "7"),
                         ("invented-source", '"invented-source"'), ("number-source", "7")):
            member = key.split("-")[1]
            with self.assertRaises(MakaiProtocolError) as caught:
                await self._list(key, raw if member == "lifecycle" else _ABSENT,
                                 raw if member == "source" else _ABSENT)
            self.assertEqual(caught.exception.code, "malformed_response")
            self.assertIn(member, str(caught.exception).lower())

    async def test_the_shared_aliases_are_not_wire_values(self) -> None:
        for key, raw in (("alias-dynamic", '"dynamic"'),
                         ("alias-static-fallback", '"static_fallback"')):
            with self.assertRaises(MakaiProtocolError) as caught:
                await self._list(key, _ABSENT, raw)
            self.assertEqual(caught.exception.code, "malformed_response")
            self.assertIn("source", str(caught.exception).lower())

    async def test_deprecated_is_filtered_but_an_unknown_one_is_kept(self) -> None:
        dropped = await self._list("dep", '"deprecated"', '"fallback"', include_deprecated=False)
        self.assertEqual(len(dropped.models), 0, "a stated deprecated must still be filtered")
        included = await self._list("dep", '"deprecated"', '"fallback"', include_deprecated=True)
        self.assertEqual(len(included.models), 1, "and returned when asked for")

        kept = await self._list("keep", _ABSENT, _ABSENT, include_deprecated=False)
        self.assertEqual(len(kept.models), 1, "an unknown lifecycle must not be dropped")

    async def test_resolve_sees_the_same_absence(self) -> None:
        env = dict(os.environ)
        env["OAP_PY_FIXTURE_SHAPE"] = "resolve-absent"
        env["OAP_PY_FIXTURE_SET"] = ""
        async with connect(command=sys.executable, args=["-u", "-c", HOST],
                           legacy_wire=False, env=env) as client:
            resolved = await client.models.resolve(provider_id="fixture", model_id="ok")
            self.assertIsNone(resolved.source)
            self.assertIsNone(resolved.lifecycle)


if __name__ == "__main__":
    unittest.main()
