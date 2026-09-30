"""Native/shared catalogue absence: the parser models.py owns, through the public client."""

import json
from typing import Any, Dict, List, Optional

import pytest

from conftest import FakeServerFactory
from oap_sdk.errors import MakaiProtocolError
from test_models import models_config

BASE: Dict[str, Any] = {
    "model_ref": "anthropic/anthropic-messages@claude-sonnet-4-5",
    "model_id": "claude-sonnet-4-5",
    "display_name": "Claude Sonnet 4.5",
    "provider_id": "anthropic",
    "api": "anthropic-messages",
    "auth_status": "authenticated",
    "capabilities": ["chat"],
}


def model(**overrides: Any) -> Dict[str, Any]:
    return {**BASE, **overrides}


async def _list(fake: FakeServerFactory, entry: Dict[str, Any]) -> Any:
    client = await fake.client(models_config([entry]))
    return await client.models.list()


async def _resolve(fake: FakeServerFactory, entry: Dict[str, Any]) -> Any:
    client = await fake.client(models_config([entry]))
    return await client.models.resolve(provider_id="anthropic", model_id=entry["model_id"])


@pytest.mark.asyncio
async def test_missing_members_read_as_unknown(fake: FakeServerFactory) -> None:
    listed = await _list(fake, model())
    assert listed.models[0].lifecycle is None
    assert listed.models[0].source is None


@pytest.mark.asyncio
async def test_resolve_reads_missing_members_as_unknown(fake: FakeServerFactory) -> None:
    resolved = await _resolve(fake, model())
    assert resolved.lifecycle is None
    assert resolved.source is None


@pytest.mark.asyncio
async def test_stated_native_members_keep_their_values(fake: FakeServerFactory) -> None:
    for lifecycle, source in (
        ("stable", "dynamic"),
        ("preview", "static_fallback"),
        ("deprecated", "dynamic"),
    ):
        listed = await _list(fake, model(lifecycle=lifecycle, source=source))
        assert listed.models[0].lifecycle == lifecycle
        assert listed.models[0].source == source


@pytest.mark.asyncio
async def test_a_present_null_is_refused(fake: FakeServerFactory) -> None:
    with pytest.raises(MakaiProtocolError) as caught:
        await _list(fake, model(lifecycle=None))
    assert caught.value.code == "malformed_response"
    assert "lifecycle" in str(caught.value).lower()

    with pytest.raises(MakaiProtocolError) as caught:
        await _list(fake, model(source=None))
    assert caught.value.code == "malformed_response"
    assert "source" in str(caught.value).lower()


@pytest.mark.asyncio
async def test_a_wrong_type_or_unknown_literal_is_refused(fake: FakeServerFactory) -> None:
    for entry, member in (
        (model(lifecycle=7), "lifecycle"),
        (model(lifecycle="retired"), "lifecycle"),
        (model(source=7), "source"),
        (model(source="invented-source"), "source"),
    ):
        with pytest.raises(MakaiProtocolError) as caught:
            await _list(fake, entry)
        assert caught.value.code == "malformed_response"
        assert member in str(caught.value).lower()


@pytest.mark.asyncio
async def test_the_rest_of_the_native_envelope_is_still_validated(fake: FakeServerFactory) -> None:
    for field, bad in (
        ("model_ref", 7),
        ("model_id", None),
        ("api", 7),
        ("auth_status", "invented"),
        ("capabilities", "chat"),
    ):
        with pytest.raises(MakaiProtocolError) as caught:
            await _list(fake, model(**{field: bad}))
        assert caught.value.code == "malformed_response", f"{field} must still be validated"


@pytest.mark.asyncio
async def test_an_unknown_lifecycle_is_not_filtered_out(fake: FakeServerFactory) -> None:
    kept = await _list(fake, model())
    assert len(kept.models) == 1, "an unknown lifecycle must not be dropped from the listing"


@pytest.mark.asyncio
async def test_the_shared_reader_does_not_filter_deprecated_itself(
    fake: FakeServerFactory,
) -> None:
    for flag in (None, False, True):
        client = await fake.client(models_config([model(lifecycle="deprecated")]))
        listed = await client.models.list(include_deprecated=flag)
        assert len(listed.models) == 1, (
            f"the shared reader must not drop an entry locally (include_deprecated={flag})"
        )


@pytest.mark.asyncio
async def test_a_concurrent_pair_of_cases_does_not_share_state(fake: FakeServerFactory) -> None:
    first = await _list(fake, model(lifecycle="stable", source="dynamic"))
    second = await _list(fake, model())
    assert first.models[0].lifecycle == "stable"
    assert second.models[0].lifecycle is None
