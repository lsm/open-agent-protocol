"""The installed distribution is ``oxap-sdk`` and ships only ``oxap_sdk``."""

from __future__ import annotations

import importlib.metadata
import importlib.util

import oxap_sdk


def test_distribution_is_oxap_sdk_at_the_package_version() -> None:
    assert importlib.metadata.version("oxap-sdk") == oxap_sdk.__version__ == "0.1.0a33"


def test_the_old_import_name_is_not_provided() -> None:
    assert importlib.util.find_spec("oap_sdk") is None
