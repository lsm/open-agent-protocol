#!/usr/bin/env python3
"""Exercise Copilot context tiers in the real TUI without real credentials."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location("driver", ROOT / "scripts/tui-pty-driver.py")
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--binary", default=str(ROOT / "zig/zig-out/bin/oapx"))
    options = parser.parse_args()
    output = Path(options.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    with tempfile.TemporaryDirectory(prefix="oap-copilot-context-") as temporary:
        root = Path(temporary).resolve()
        home = root / "home"
        data = home / ".oapx"
        cache = data / "model_catalog"
        cache.mkdir(parents=True)
        binary = root / "oapx"
        shutil.copy2(options.binary, binary)
        (data / "auth.json").write_text(json.dumps({"github-copilot": {
            "api_key": "synthetic-test-value-not-a-real-credential"}}))
        config = data / "config.json"
        config.write_text(json.dumps({"model": "claude-sonnet-4.6", "provider": "github-copilot",
                                     "api": "openai-completions", "workspace": str(root)}))
        metadata = {"data": [{"id": "claude-sonnet-4.6", "name": "Synthetic Copilot Claude",
            "model_picker_enabled": True, "capabilities": {"type": "chat", "limits": {
                "max_prompt_tokens": 200000, "max_context_window_tokens": 1000000,
                "max_output_tokens": 128000}}, "billing": {"token_prices": {
                    "default": {"context_max": 200000}, "long_context": {"context_max": 872000}}}}]}
        (cache / "github-copilot.json").write_text(json.dumps(metadata))
        (output / "synthetic-models.json").write_text(json.dumps(metadata, indent=2))
        profile = root / "isolation.sb"
        profile.write_text('(version 1) (allow default)\n(deny network*)\n'
            '(deny mach-lookup (global-name-regex #".*[Ss]ecurity.*"))\n'
            f'(deny file-read* (subpath {json.dumps(str(Path.home()))}))\n')
        environment = {"PATH": "/usr/bin:/bin", "HOME": str(home), "TMPDIR": str(root),
                       "TERM": "xterm-256color", "OAPX_KEYCHAIN_SERVICE": "oap-verify-" + str(uuid.uuid4())}
        os.environ.clear()
        os.environ.update(environment)
        session = None
        try:
            args = argparse.Namespace(binary="/usr/bin/sandbox-exec", width=150, height=35, fixture_text="")
            session = driver.PtySession(args, home=str(home), use_fixture=False, cwd=str(root),
                                       argv=["-f", str(profile), str(binary)])
            session.wait_for(driver.WELCOME_MARKER, 15, "welcome", since=0)
            session.quiesce(0.3)
            cases = [
                ("/context", "context window: 200000", None),
                ("/context 872k", "context window: 872000", 872000),
                ("/context 900k", "takes at most 872000 context tokens", 872000),
                ("/context default", "context window: 200000", None),
                ("/context 200k", "context window: 200000", 200000),
            ]
            for index, (command, marker, persisted) in enumerate(cases):
                since = len(session.plain)
                session.type_text(command)
                session.send(b"\r", command)
                session.wait_for(marker.encode(), 10, command, since=since)
                session.quiesce(0.3)
                actual = json.loads(config.read_text()).get("mode", {}).get("context_window")
                assert actual == persisted, (command, actual, persisted)
                (output / f"screen-{index}.txt").write_bytes(session.screen_text())
            session.type_text("/quit")
            session.send(b"\r", "quit")
            assert session.wait_exit(10) == 0
            print("PASS: real Copilot TUI selected 200K and metadata-provided 872K, refused 900K, "
                  "restored default 200K and persisted choices; exit 0; synthetic catalog, no live provider")
        finally:
            if session is not None:
                (output / "transcript.bin").write_bytes(b"".join(chunk for _, chunk in session.chunks))
                (output / "screen.txt").write_bytes(session.screen_text())
                session.close()


if __name__ == "__main__":
    main()
