"""End-to-end coverage against a real ``oapx serve agent,provider --stdio`` host.

Every test here needs ``OAP_SDK_BINARY_PATH`` and skips without it, so a green
``pytest`` with the variable unset means **zero** real-runtime coverage. Build
one first::

    zig build install --prefix /tmp/oapx-py
    OAP_SDK_BINARY_PATH=/tmp/oapx-py/bin/oapx pytest

No provider credentials are needed: the model catalogue, envelope validation,
frame routing, and process lifetime are all reachable without them.

The ``auth`` protocol is **not** exercised here by default. On macOS the login
Keychain is the primary credential store, and an unsigned local build blocks on
a Keychain access prompt that never surfaces from a non-interactive shell, so
``auth.providers.request`` never answers and the request hangs. Set
``OAP_SDK_TEST_REAL_AUTH=1`` to opt in where the runtime can reach its store
without prompting (Linux CI, or a signed build with a granted ACL).
"""

from __future__ import annotations

import asyncio
import os

import pytest

from conftest import process_alive
from oap_sdk.client import MakaiClient
from oap_sdk.errors import MakaiProtocolError, MakaiStreamError
from oap_sdk.transport import StdioTransport

pytestmark = pytest.mark.real_binary


async def open_transport(binary: str, **kwargs: object) -> StdioTransport:
    transport = StdioTransport(command=binary, **kwargs)  # type: ignore[arg-type]
    await transport.connect()
    return transport


async def test_handshake_against_the_real_host(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    try:
        assert transport.connected
        assert transport.pid is not None
    finally:
        await transport.close()


async def test_models_list_returns_the_static_catalogue(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, response_timeout=10.0)
    try:
        result = await client.models.list()
        assert result.models, "the runtime always serves at least the static fallback catalogue"
        assert result.fetched_at_ms > 0
        for model in result.models:
            assert model.model_ref
            assert model.provider_id
            assert model.api
    finally:
        await client.close()


async def test_model_refs_round_trip_opaquely(real_binary: str) -> None:
    """A ref taken from list must resolve without the caller touching it."""
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, response_timeout=10.0)
    try:
        listed = (await client.models.list()).models[0]
        resolved = await client.models.resolve(
            provider_id=listed.provider_id, api=listed.api, model_id=listed.model_id
        )
        assert resolved.model_ref == listed.model_ref
    finally:
        await client.close()


async def test_resolve_unknown_model_is_a_protocol_error(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, response_timeout=10.0)
    try:
        provider_id = (await client.models.list()).models[0].provider_id
        with pytest.raises(MakaiProtocolError) as excinfo:
            await client.models.resolve(provider_id=provider_id, model_id="definitely-not-a-model")
        assert excinfo.value.code == "model_not_found"
    finally:
        await client.close()


async def test_filtered_list_narrows_the_catalogue(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, response_timeout=10.0)
    try:
        everything = await client.models.list()
        provider_id = everything.models[0].provider_id
        filtered = await client.models.list(provider_id=provider_id)
        assert filtered.models
        assert {model.provider_id for model in filtered.models} == {provider_id}
        assert len(filtered.models) <= len(everything.models)
    finally:
        await client.close()


async def test_concurrent_requests_do_not_cross_talk(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, response_timeout=15.0)
    try:
        results = await asyncio.gather(*(client.models.list() for _ in range(8)))
        counts = {len(result.models) for result in results}
        assert len(counts) == 1, "every concurrent list must see the same catalogue"
    finally:
        await client.close()


async def test_an_unknown_envelope_type_is_refused_by_name(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    try:
        async with transport.route(request_id="01ARZ3NDEKTSV4RRFFQ69G5FAV") as route:
            await transport.send({
                "protocol": "open-agent-protocol", "version": "0.1",
                "profile": "open-agent-protocol.agent-control-core",
                "type": "definitely_not_a_real_envelope", "id": "01ARZ3NDEKTSV4RRFFQ69G5FAV", "payload": {},
            })
            reply = await route.next_frame(5.0)
        assert reply["type"] == "error.response"
        assert reply["payload"]["error"]["code"] == "unsupported_request"
    finally:
        await transport.close()



async def test_close_terminates_the_real_process(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    pid = transport.pid
    assert pid is not None
    await transport.close()
    for _ in range(60):
        if not process_alive(pid):
            break
        await asyncio.sleep(0.05)
    assert not process_alive(pid), "closing the client must not leak the runtime process"


async def test_context_manager_closes_the_real_process(real_binary: str) -> None:
    async with StdioTransport(command=real_binary) as transport:
        client = MakaiClient(transport, response_timeout=10.0)
        assert (await client.models.list()).models
        pid = transport.pid
    assert pid is not None
    for _ in range(60):
        if not process_alive(pid):
            break
        await asyncio.sleep(0.05)
    assert not process_alive(pid)


async def test_requests_after_close_fail_cleanly(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, response_timeout=5.0)
    await client.close()
    with pytest.raises((MakaiProtocolError, MakaiStreamError)):
        await client.models.list()


async def test_handshake_timeout_is_respected(real_binary: str) -> None:
    """An absurdly short handshake budget fails fast instead of hanging."""
    transport = StdioTransport(
        command=real_binary, handshake_timeout=0.000_001
    )
    with pytest.raises(MakaiStreamError, match="handshake timed out"):
        await transport.connect()
    assert transport.pid is None


async def test_resolver_finds_the_binary_from_the_environment(real_binary: str) -> None:
    from oap_sdk.binary import resolve_makai_binary

    assert os.path.samefile(resolve_makai_binary(), real_binary)


@pytest.mark.skipif(
    not os.environ.get("OAP_SDK_TEST_REAL_AUTH"),
    reason="set OAP_SDK_TEST_REAL_AUTH=1; on macOS an unsigned build blocks on a Keychain prompt",
)
async def test_auth_list_providers_against_the_real_host(real_binary: str) -> None:
    transport = await open_transport(real_binary)
    client = MakaiClient(transport, frame_timeout=15.0)
    try:
        providers = await client.auth.list_providers()
        for provider in providers:
            assert provider.id
            assert provider.name
            assert provider.auth_status in {
                "authenticated",
                "login_required",
                "expired",
                "refreshing",
                "login_in_progress",
                "failed",
                "unknown",
            }
    finally:
        await client.close()
