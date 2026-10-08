"""OAP catalogue absence: one fake process per case, driven through the public client."""

import os
import sys
import unittest
from typing import Any, Dict, Optional, Sequence, Tuple, Union

from oap_sdk import MakaiProtocolError, connect
from oap_sdk.types import ListModelsResponse

HOST = r'''
import json, os, sys

A = "open-agent-protocol.agent-control-core"
P = "open-agent-protocol.model-provider-core"
model = {"model_ref": "fixture/other:abs@ok", "model_id": "ok", "provider_id": "fixture",
         "wire": "other",
         "auth_status": json.loads(os.environ.get("OAP_PY_AUTH_STATUS", '"authenticated"')),
         "capabilities": ["chat", "streaming"]}
shape = os.environ.get("OAP_PY_FIXTURE_SHAPE", "")
stamped = os.environ.get("OAP_PY_FIXTURE_SET", "")
for member in ("lifecycle", "source"):
    if f"{member}_{shape}" in stamped:
        model[member] = json.loads(os.environ[f"{member}_{shape}"])


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
        emit(A, "capabilities.response", rid, {"features": {}},
             {"capability_revision": "fixture-rev-1"})
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
        api: Optional[str] = None,
        model_id: Optional[str] = None,
        include_login_required: Optional[bool] = None,
        auth_status: Optional[str] = None,
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
        if auth_status is not None:
            env["OAP_PY_AUTH_STATUS"] = auth_status
        async with connect(command=sys.executable, args=["-u", "-c", HOST],
                           env=env) as client:
            return await client.models.list(include_deprecated=include_deprecated, api=api,
                                            model_id=model_id,
                                            include_login_required=include_login_required)

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

    async def test_a_present_null_wrong_type_unknown_or_alias_is_refused(self) -> None:
        for key, lifecycle, source, member in (
            ("null-lifecycle", "null", _ABSENT, "lifecycle"),
            ("null-source", _ABSENT, "null", "source"),
            ("invented-lifecycle", '"retired"', _ABSENT, "lifecycle"),
            ("number-lifecycle", "7", _ABSENT, "lifecycle"),
            ("invented-source", _ABSENT, '"invented-source"', "source"),
            ("number-source", _ABSENT, "7", "source"),
            ("alias-dynamic", _ABSENT, '"dynamic"', "source"),
            ("alias-static-fallback", _ABSENT, '"static_fallback"', "source"),
        ):
            with self.assertRaises(MakaiProtocolError) as caught:
                await self._list(key, lifecycle, source)
            self.assertEqual(caught.exception.code, "malformed_response", key)
            self.assertIn(member, str(caught.exception).lower(), key)

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
                           env=env) as client:
            resolved = await client.models.resolve(provider_id="fixture", model_id="ok")
            self.assertIsNone(resolved.source)
            self.assertIsNone(resolved.lifecycle)
    async def test_an_invalid_source_is_refused_even_when_the_row_is_filtered_out(self) -> None:
        with self.assertRaises(MakaiProtocolError) as caught:
            await self._list("byp-dep", '"deprecated"', "null", include_deprecated=False)
        self.assertEqual(caught.exception.code, "malformed_response")
        self.assertIn("source", str(caught.exception).lower())

    async def test_an_invalid_member_is_refused_even_when_another_filter_skips_it(self) -> None:
        cases: Sequence[Tuple[str, Dict[str, Any], Dict[str, Any]]] = (
            ("api", {"api": "not-other"}, {}),
            ("model", {"model_id": "nope"}, {}),
            ("auth", {"include_login_required": False}, {"auth_status": '"login_required"'}),
        )
        for key, kwargs, extra in cases:
            for member, lifecycle, source in (
                ("lifecycle", "null", _ABSENT), ("source", _ABSENT, "null")):
                with self.subTest(filter=key, member=member):
                    survivor = await self._list(f"keep-{key}", '"stable"', '"fallback"',
                                                **kwargs, **extra)
                    self.assertEqual(survivor.models, [], "filter must actually skip the row")
                    with self.assertRaises(MakaiProtocolError) as caught:
                        await self._list(f"byp-{key}-{member}", lifecycle, source,
                                         **kwargs, **extra)
                    self.assertEqual(caught.exception.code, "malformed_response")
                    self.assertIn(member, str(caught.exception).lower())

    async def test_a_valid_row_is_still_filtered_as_before(self) -> None:
        dropped = await self._list("filt-dep", '"deprecated"', '"fallback"',
                                   include_deprecated=False)
        self.assertEqual(dropped.models, [])
        kept = await self._list("filt-dep-keep", '"deprecated"', '"fallback"',
                                include_deprecated=True)
        self.assertEqual(len(kept.models), 1)
        by_api = await self._list("filt-api", '"stable"', '"fallback"', api="nothing")
        self.assertEqual(by_api.models, [], "a non-matching api still filters the row out")

        unknown = await self._list("filt-absent", _ABSENT, _ABSENT, include_deprecated=False)
        self.assertEqual(len(unknown.models), 1)
        self.assertIsNone(unknown.models[0].lifecycle)
        self.assertIsNone(unknown.models[0].source)


if __name__ == "__main__":
    unittest.main()
