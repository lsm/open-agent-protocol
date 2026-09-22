"""Every name ``types`` publishes is importable from the package itself."""

from __future__ import annotations

import oap_sdk
from oap_sdk import types


def test_package_reexports_every_public_type() -> None:
    assert not set(types.__all__) - set(oap_sdk.__all__)


def test_every_exported_name_resolves() -> None:
    assert [name for name in oap_sdk.__all__ if not hasattr(oap_sdk, name)] == []


def test_the_callback_types_a_typed_consumer_annotates_with_are_exported() -> None:
    for name in ("ToolExecutor", "AuthEventHandler", "AuthPromptHandler", "Role"):
        assert getattr(oap_sdk, name) is getattr(types, name)
