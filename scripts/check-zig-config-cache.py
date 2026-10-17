#!/usr/bin/env python3
"""Verify content-based invalidation of the runtime's configuration inputs."""
import os
from pathlib import Path
import subprocess
import tempfile


def main():
    root = Path(__file__).resolve().parent.parent
    inputs = [root / "providers/catalog.json", root / "providers/resolved_urls.json"]
    inputs += [root / "harnesses" / (name + ".json") for name in (
        "acp", "claude-code", "codex-app-server", "deepseek-harness",
        "hermes", "opencode", "pi",
    )]
    with tempfile.TemporaryDirectory(prefix="oap-zig-config-") as cache:
        command = [os.environ.get("ZIG", "zig"), "build", "--build-file", "zig/build.zig",
                   "--cache-dir", cache, "-Dtarget=x86_64-linux",
                   "--cache-poison=disallowed", "--print-configuration"]

        def configure():
            return subprocess.run(command, cwd=root, stdout=subprocess.DEVNULL,
                                  stderr=subprocess.PIPE, timeout=120)

        def require_success():
            result = configure()
            if result.returncode:
                raise RuntimeError(result.stderr.decode(errors="replace"))

        require_success()
        require_success()
        for path in inputs:
            original = path.read_bytes()
            try:
                path.write_bytes(original + b"\ninvalid-zig-config-cache-probe\n")
                result = configure()
                if result.returncode == 0 or b"parsing " not in result.stderr:
                    raise RuntimeError("configuration did not reparse " + str(path)
                                       + "\n" + result.stderr.decode(errors="replace"))
            finally:
                path.write_bytes(original)
            require_success()
            print("configuration cache invalidated: " + str(path.relative_to(root)), flush=True)


if __name__ == "__main__":
    main()
