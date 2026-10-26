#!/usr/bin/env python3
"""Check that every runner invocation reaches and logs the first command."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

SOURCE = Path(__file__).with_name("check.sh")
with tempfile.TemporaryDirectory(prefix="oap-check-invocations-") as temporary:
    root = Path(temporary)
    scripts = root / ".claude/skills/verify/scripts"
    scripts.mkdir(parents=True)
    shutil.copy2(SOURCE, scripts / "check.sh")
    binary = root / "bin"
    binary.mkdir()
    go = binary / "go"
    go.write_text("#!/bin/bash\necho runner-reached-go\nexit 73\n")
    go.chmod(0o755)
    for index, (cwd, invocation) in enumerate([
        (root, ["bash", ".claude/skills/verify/scripts/check.sh"]),
        (scripts, ["bash", "check.sh"]),
        (scripts, ["./check.sh"]),
    ]):
        temporary_root = root / str(index)
        temporary_root.mkdir()
        environment = dict(os.environ, PATH=str(binary) + os.pathsep + os.environ["PATH"], TMPDIR=str(temporary_root))
        environment.pop("OAP_VERIFY_ARTIFACTS", None)
        result = subprocess.run(invocation, cwd=cwd, env=environment, capture_output=True, text=True, timeout=15)
        assert result.returncode == 73, (invocation, result.returncode, result.stdout, result.stderr)
        logs = list(temporary_root.glob("oap-verify.*/check.log"))
        assert len(logs) == 1 and "runner-reached-go" in logs[0].read_text(), (invocation, logs)
        assert "EXIT: 73" in logs[0].read_text(), invocation
        print(f"PASS: {invocation}: first check reached, exit 73 preserved and logged")
