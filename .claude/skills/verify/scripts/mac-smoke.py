#!/usr/bin/env python3
"""Credential-free macOS PTY smoke; reuse the maintained terminal decoder."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location("pty_driver", ROOT / "scripts/tui-pty-driver.py")
driver = importlib.util.module_from_spec(spec)
spec.loader.exec_module(driver)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    if sys.platform != "darwin":
        parser.error("macOS only; use scripts/tui-pty-driver.py on Linux")
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    real_home = str(Path.home())
    session = None
    with tempfile.TemporaryDirectory(prefix="oap-verify-") as temporary:
        root = Path(temporary).resolve()
        binary = root / "oapx"
        shutil.copy2(ROOT / "zig/zig-out/bin/oapx", binary)
        home = root / "home"
        home.mkdir()
        profile = root / "smoke.sb"
        profile.write_text(
            '(version 1) (allow default)\n'
            '(deny network*)\n'
            '(deny mach-lookup (global-name-regex #".*[Ss]ecurity.*"))\n'
            f'(deny file-read* (subpath {json.dumps(real_home)}))\n'
        )
        environment = {
            "PATH": "/usr/bin:/bin",
            "HOME": str(home),
            "TMPDIR": str(root),
            "TERM": "xterm-256color",
            "OAPX_KEYCHAIN_SERVICE": "oap-verify-" + str(uuid.uuid4()),
        }
        command = ["/usr/bin/sandbox-exec", "-f", str(profile), str(binary)]
        result = subprocess.run(command + ["--help"], env=environment, cwd=root,
                                capture_output=True, timeout=15)
        (output / "help.txt").write_bytes(result.stdout + result.stderr)
        if result.returncode != 0 or b"validate" not in result.stdout + result.stderr:
            raise RuntimeError("sandboxed oapx --help failed; see help.txt")
        os.environ.clear()
        os.environ.update(environment)
        options = argparse.Namespace(binary=command[0], width=100, height=30,
                                     fixture_text="oap-verify-fixture-reply")
        try:
            session = driver.PtySession(options, home=str(home), cwd=str(root),
                                        argv=command[1:])
            session.wait_for(driver.WELCOME_MARKER, 15, "welcome banner", since=0)
            session.quiesce(0.4)
            session.type_text("verify local reply")
            start = len(session.plain)
            session.send(b"\r", "submit")
            session.wait_for(options.fixture_text.encode(), 15, "fixture reply", since=start)
            session.quiesce(0.4)
            session.type_text("/quit")
            session.send(b"\r", "quit")
            code = session.wait_exit(5)
            if code != 0:
                raise RuntimeError(f"TUI exit {code}")
            print("PASS: sandboxed oapx --help; PTY welcome -> prompt -> fixture reply -> /quit (exit 0)")
        finally:
            if session is not None:
                (output / "transcript.bin").write_bytes(b"".join(chunk for _, chunk in session.chunks))
                (output / "screen.txt").write_bytes(session.screen_text())
                session.close()
    print(f"Artifacts: {output}; temporary HOME/binary removed; child stopped")


if __name__ == "__main__":
    main()
