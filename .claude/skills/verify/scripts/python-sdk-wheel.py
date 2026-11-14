#!/usr/bin/env python3
"""Build the Python SDK wheel, install it into a clean venv and import it."""
import argparse
import json
from pathlib import Path
import subprocess
import sys
import tempfile
import zipfile

ROOT = Path(__file__).resolve().parents[4]
VERSION = "0.1.0a33"
PROBE = r'''import importlib.metadata, importlib.util, json
import oxap_sdk
print(json.dumps({
    "version": importlib.metadata.version("oxap-sdk"),
    "module_version": oxap_sdk.__version__,
    "module_file": oxap_sdk.__file__,
    "connect": callable(oxap_sdk.connect),
    "old_import": importlib.util.find_spec("oap_sdk") is not None,
}))
'''


def run(command, **kwargs):
    print("+", " ".join(map(str, command)), flush=True)
    result = subprocess.run(command, capture_output=True, text=True, timeout=600, **kwargs)
    print(f"exit={result.returncode}", flush=True)
    if result.returncode != 0:
        sys.stderr.write(result.stdout + result.stderr)
        raise SystemExit(result.returncode)
    return result.stdout


def check(work, interpreter):
    dist = work / "dist"
    run([interpreter, "-m", "pip", "wheel", "--no-deps", "-w", str(dist), str(ROOT / "sdk/python")])
    wheels = sorted(dist.glob("*.whl"))
    names = [wheel.name for wheel in wheels]
    assert names == [f"oxap_sdk-{VERSION}-py3-none-any.whl"], names
    with zipfile.ZipFile(wheels[0]) as archive:
        members = archive.namelist()
    (work / "wheel-contents.json").write_text(json.dumps(members, indent=2) + "\n")
    roots = sorted({member.split("/")[0] for member in members})
    print(f"wheel roots={roots} members={len(members)}")
    assert roots == ["oxap_sdk", f"oxap_sdk-{VERSION}.dist-info"], roots
    assert "oxap_sdk/py.typed" in members, "py.typed missing"
    venv = work / "venv"
    run([interpreter, "-m", "venv", str(venv)])
    python = venv / "bin" / "python"
    run([str(python), "-m", "pip", "install", "--no-deps", "--no-index", str(wheels[0])])
    observed = json.loads(run([str(python), "-I", "-c", PROBE], cwd=work))
    (work / "probe.json").write_text(json.dumps(observed, indent=2) + "\n")
    print(json.dumps(observed, indent=2))
    assert observed["version"] == observed["module_version"] == VERSION, observed
    assert str(venv.resolve()) in str(Path(observed["module_file"]).resolve()), observed
    assert observed["connect"] and not observed["old_import"], observed


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--python", default=sys.executable)
    parser.add_argument("--output-dir", type=Path)
    args = parser.parse_args()
    if args.output_dir:
        args.output_dir.mkdir(parents=True, exist_ok=True)
        check(args.output_dir, args.python)
    else:
        with tempfile.TemporaryDirectory(prefix="oxap-python-wheel-") as temporary:
            check(Path(temporary), args.python)
    print("python-sdk-wheel: PASS")


if __name__ == "__main__":
    main()
