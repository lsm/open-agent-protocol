#!/usr/bin/env python3
"""Credential-free macOS PTY smoke; reuse the maintained terminal decoder."""
import argparse
import fcntl
import struct
import termios
import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
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
            for text in ("abc", "中文", "中" * 50):
                session.send(b"\x15", "clear draft")
                start_frame = len(session.chunks)
                session.type_text(text)
                session.quiesce(0.4)
                rows = session.screen.visible_rows()
                (output / ("caret-" + str(len(text)) + ".txt")).write_text(
                    "\n".join(rows) + f"\nreal cursor: {session.screen.row},{session.screen.col}\n")
                border = next(r for r, line in enumerate(rows) if line.startswith("╰"))
                cells = sum(driver.cell_width(c) for c in text)
                expected = (border - 1, 4 + cells % (session.width - 6))
                actual = (session.screen.row, session.screen.col)
                rendered = b"".join(chunk for _, chunk in session.chunks[start_frame:])
                position = f"\x1b[{expected[1] + 1}G".encode()
                if actual != expected:
                    raise AssertionError(f"caret for {text!r}: expected {expected}, got {actual}")
                if position not in rendered:
                    raise AssertionError(f"no caret column escape {position!r} for {text!r}")
            session.send(b"\x1b[D", "move caret left over CJK")
            session.quiesce(0.4)
            if session.screen.col != 8:
                raise AssertionError(f"CJK left caret column: {session.screen.col}")
            session.width = 120
            session.screen.cols = 120
            for line in session.screen.lines:
                line.extend([" "] * 20)
            fcntl.ioctl(session.master, termios.TIOCSWINSZ, struct.pack("HHHH", session.height, session.width, 0, 0))
            os.kill(session.proc.pid, signal.SIGWINCH)
            session.quiesce(1.0)
            rows = session.screen.visible_rows()
            (output / "resized.txt").write_text("\n".join(rows) + f"\nreal cursor: {session.screen.row},{session.screen.col}\n")
            border = next(r for r, line in enumerate(rows) if line.startswith("╰"))
            if (session.screen.row, session.screen.col) != (border - 1, 102):
                raise AssertionError(f"resized CJK caret: {session.screen.row},{session.screen.col}")
            session.send(b"\x15", "clear draft")
            session.type_text("verify local reply")
            start = len(session.plain)
            session.send(b"\r", "submit")
            session.wait_for(options.fixture_text.encode(), 15, "fixture reply", since=start)
            session.quiesce(0.4)
            session.type_text("/zen")
            session.send(b"\r", "enter zen mode")
            session.quiesce(0.4)
            session.type_text("中文")
            session.quiesce(0.4)
            rows = session.screen.visible_rows()
            row, line = next((r, line) for r, line in enumerate(rows) if "中文" in line)
            col = sum(driver.cell_width(c) for c in line[:line.index("中文")]) + 4
            if (session.screen.row, session.screen.col) != (row, col):
                raise AssertionError(f"zen caret: expected {(row, col)}, got {(session.screen.row, session.screen.col)}")
            (output / "zen.txt").write_text("\n".join(rows) + f"\nreal cursor: {session.screen.row},{session.screen.col}\n")
            session.send(b"\x15", "clear zen draft")
            session.type_text("/quit")
            session.send(b"\r", "quit")
            code = session.wait_exit(5)
            if code != 0:
                raise RuntimeError(f"TUI exit {code}")
            transcript = b"".join(chunk for _, chunk in session.chunks)
            if b"\x1b[?25h" not in transcript:
                raise AssertionError("terminal cursor not restored on exit")
            if session.screen.col != 0:
                raise AssertionError(f"exit left cursor in composer column {session.screen.col}")
            print("PASS: caret escapes, ASCII/CJK/wrap/edit/resize, exit cursor restoration;")
            print("PASS: sandboxed oapx --help; PTY welcome -> prompt -> fixture reply -> /quit (exit 0)")
        finally:
            if session is not None:
                (output / "transcript.bin").write_bytes(b"".join(chunk for _, chunk in session.chunks))
                (output / "screen.txt").write_bytes(session.screen_text())
                session.close()
    print(f"Artifacts: {output}; temporary HOME/binary removed; child stopped")


if __name__ == "__main__":
    main()
