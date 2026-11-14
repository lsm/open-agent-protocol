"""Locating the ``oapx`` runtime binary.

Resolution order mirrors ``typescript/src/binary_resolver.ts``:

1. ``OXAP_SDK_BINARY_PATH``, then the legacy ``OAP_SDK_BINARY_PATH``, then
   ``BinaryResolverOptions.binary_path`` (the environment wins, as in the
   TypeScript SDK).
2. ``OXAP_SDK_BINARY_URL`` / ``OAP_SDK_BINARY_URL`` (first one set), or
   ``binary_url``, which **requires** a SHA-256 checksum. Each environment URL
   takes the checksum written beside it (``OXAP_SDK_BINARY_SHA256`` for the
   OXAP URL, ``OAP_SDK_BINARY_SHA256`` for the legacy one), falling back to
   ``checksum_sha256``; a checksum is never borrowed across the two pairs. The
   download is cached and re-verified on every resolve.
3. ``./zig-out/bin/oxap``, then ``./zig/zig-out/bin/oxap``, then the same two
   paths with the legacy ``oapx`` name.
4. ``oxap`` on ``PATH``, then ``oapx``.

On Windows each name carries an ``.exe`` suffix.

The TypeScript SDK has one extra step between 2 and 3: an optional
``@oxap/cli-<platform>-<arch>`` npm package. **That step is deliberately
omitted here.** npm's optional-dependency mechanism installs a per-platform
package automatically; Python's closest equivalent would be publishing
platform-specific wheels, and no such distribution exists for oapx today.
Inventing a ``oap-sdk-cli-<platform>`` import probe would be dead code that also
silently outranks a local ``zig build`` -- the exact footgun CLAUDE.md warns
about for the TypeScript resolver. If platform wheels are published later, this
is the place to add the step; until then, set ``OXAP_SDK_BINARY_PATH`` when you
need to pin a specific binary.
"""

from __future__ import annotations

import hashlib
import os
import shutil
import stat
import urllib.parse
import urllib.request
from dataclasses import dataclass
from pathlib import Path
from typing import Optional

__all__ = ["BinaryResolverOptions", "resolve_makai_binary"]

ENV_BINARY_PATH = "OAP_SDK_BINARY_PATH"
ENV_BINARY_URL = "OAP_SDK_BINARY_URL"
ENV_BINARY_SHA256 = "OAP_SDK_BINARY_SHA256"
ENV_BINARY_PATH_OXAP = "OXAP_SDK_BINARY_PATH"
ENV_BINARY_URL_OXAP = "OXAP_SDK_BINARY_URL"
ENV_BINARY_SHA256_OXAP = "OXAP_SDK_BINARY_SHA256"

_DOWNLOAD_TIMEOUT_S = 120.0


@dataclass(frozen=True)
class BinaryResolverOptions:
    """Options controlling how the ``oapx`` binary is located.

    Attributes:
        binary_path: Explicit path to an ``oapx`` executable.
        binary_url: URL to download the binary from. Requires
            ``checksum_sha256``.
        checksum_sha256: Hex-encoded SHA-256 of the downloaded binary.
        cache_dir: Where downloads are cached. Defaults to
            ``~/.cache/makai/bin``.
    """

    binary_path: Optional[str] = None
    binary_url: Optional[str] = None
    checksum_sha256: Optional[str] = None
    cache_dir: Optional[str] = None


def _is_executable_file(candidate: Path) -> bool:
    return candidate.is_file() and os.access(candidate, os.X_OK)


def _binary_names() -> tuple[str, ...]:
    if os.name == "nt":
        return ("oxap.exe", "oapx.exe")
    return ("oxap", "oapx")


def _binary_name() -> str:
    return _binary_names()[0]


def _first_non_empty(*values: Optional[str]) -> Optional[str]:
    for value in values:
        if value:
            return value
    return None


def _url_and_checksum(options: "BinaryResolverOptions") -> tuple[Optional[str], Optional[str]]:
    option_checksum = options.checksum_sha256
    oxap_url = _first_non_empty(os.environ.get(ENV_BINARY_URL_OXAP))
    if oxap_url:
        return oxap_url, _first_non_empty(os.environ.get(ENV_BINARY_SHA256_OXAP), option_checksum)
    legacy_url = _first_non_empty(os.environ.get(ENV_BINARY_URL))
    if legacy_url:
        return legacy_url, _first_non_empty(os.environ.get(ENV_BINARY_SHA256), option_checksum)
    if options.binary_url:
        return options.binary_url, _first_non_empty(
            os.environ.get(ENV_BINARY_SHA256_OXAP),
            os.environ.get(ENV_BINARY_SHA256),
            option_checksum,
        )
    return None, None


def _sha256(content: bytes) -> str:
    return hashlib.sha256(content).hexdigest()


def resolve_makai_binary(options: Optional[BinaryResolverOptions] = None) -> str:
    """Return the path (or bare command name) of the ``oapx`` binary.

    Raises:
        FileNotFoundError: An explicit path was given but does not exist.
        ValueError: A URL was given without a checksum, or the checksum did
            not match the downloaded bytes.
    """
    options = options or BinaryResolverOptions()

    explicit = _first_non_empty(
        os.environ.get(ENV_BINARY_PATH_OXAP), os.environ.get(ENV_BINARY_PATH), options.binary_path
    )
    if explicit:
        resolved = Path(explicit).expanduser().resolve()
        if not resolved.exists():
            raise FileNotFoundError(f"oapx binary not found at {resolved}")
        return str(resolved)

    binary_url, checksum = _url_and_checksum(options)
    if binary_url:
        return _resolve_from_url(binary_url, checksum, options.cache_dir)

    names = _binary_names()
    for name in names:
        for candidate in (
            Path.cwd() / "zig-out" / "bin" / name,
            Path.cwd() / "zig" / "zig-out" / "bin" / name,
        ):
            if _is_executable_file(candidate):
                return str(candidate)

    for name in names:
        found = shutil.which(name)
        if found:
            return found
    return names[0]


def _resolve_from_url(binary_url: str, checksum: Optional[str], cache_dir: Optional[str]) -> str:
    if not checksum:
        raise ValueError(
            f"SHA256 checksum is required when downloading oapx binary from URL: {binary_url}"
        )
    expected = checksum.lower()

    directory = Path(cache_dir) if cache_dir else Path.home() / ".cache" / "makai" / "bin"
    file_name = Path(urllib.parse.urlparse(binary_url).path).name or _binary_name()
    cache_path = directory / file_name

    if cache_path.exists():
        actual = _sha256(cache_path.read_bytes())
        if actual == expected:
            return str(cache_path)
        cache_path.unlink()

    content = _download(binary_url)
    actual = _sha256(content)
    if actual != expected:
        raise ValueError(f"binary checksum mismatch: expected {expected}, got {actual}")

    directory.mkdir(parents=True, exist_ok=True)
    temp_path = cache_path.with_suffix(cache_path.suffix + ".tmp")
    temp_path.write_bytes(content)
    if os.name != "nt":
        temp_path.chmod(temp_path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)
    temp_path.replace(cache_path)
    return str(cache_path)


def _download(binary_url: str) -> bytes:
    parsed = urllib.parse.urlparse(binary_url)
    if parsed.scheme not in ("http", "https"):
        raise ValueError(f"unsupported binary_url scheme: {parsed.scheme!r}")
    with urllib.request.urlopen(binary_url, timeout=_DOWNLOAD_TIMEOUT_S) as response:  # noqa: S310
        status = getattr(response, "status", 200)
        if status != 200:
            raise ValueError(f"failed to download binary: {status}")
        data: bytes = response.read()
    return data
