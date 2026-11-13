#!/usr/bin/env python3
"""Observe the public TypeScript SDK's runtime binary precedence end to end."""
import argparse
import hashlib
import http.server
import json
import os
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[4]
NODE = shutil.which("node")
assert NODE, "node is not on PATH"
ENV_NAMES = ["OXAP_SDK_BINARY_PATH", "OAP_SDK_BINARY_PATH", "OXAP_SDK_BINARY_URL",
             "OAP_SDK_BINARY_URL", "OXAP_SDK_BINARY_SHA256", "OAP_SDK_BINARY_SHA256"]
SOURCE = r'''const sdk = require(process.argv[2]);
const path = require("node:path");
const fs = require("node:fs");

const bundledDir = process.env.PROBE_BUNDLED_DIR;
const options = { cwd: process.env.PROBE_CWD, cacheDir: process.env.PROBE_CACHE };
options.resolveModule = (specifier) => {
  if (bundledDir) {
    const candidate = path.join(bundledDir, path.basename(specifier));
    if (fs.existsSync(candidate)) return candidate;
  }
  throw new Error(`Cannot find module '${specifier}'`);
};
sdk.resolveMakaiBinary(options).then(
  (resolved) => console.log(resolved),
  (error) => { console.log(`error: ${error.message}`); process.exit(3); },
);
'''


def run(command, cwd=ROOT, timeout=300):
    result = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    assert result.returncode == 0, (command, result.stdout, result.stderr)
    return result.stdout


def stub(path, name="#!/bin/sh\n"):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(name)
    path.chmod(0o755)
    return path


def binary(name):
    return f"{name}.exe" if os.name == "nt" else name


class Payloads:
    def __init__(self):
        self.requests = []

    def handler(self):
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                outer.requests.append(self.path)
                body = b"#!/bin/sh\necho oxap\n" if "oxap" in self.path else b"#!/bin/sh\necho oapx\n"
                self.send_response(200)
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        return Handler


def resolve(consumer, sdk, output, label, workdir, env, bundled=None, cache=None):
    environment = {name: value for name, value in os.environ.items() if name not in ENV_NAMES}
    environment.update(env)
    environment["PROBE_CWD"] = str(workdir)
    environment["PROBE_CACHE"] = str(cache or output / f"cache-{label}")
    if bundled is not None:
        environment["PROBE_BUNDLED_DIR"] = str(bundled)
    result = subprocess.run([NODE, str(consumer), str(sdk)], cwd=workdir, env=environment,
                            capture_output=True, text=True, timeout=120)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)

    run(["npm", "run", "build:sdk"], timeout=600)
    sdk = ROOT / "dist" / "src" / "index.js"
    assert sdk.is_file(), f"SDK entry point missing: {sdk}"
    consumer = output / "consumer.js"
    consumer.write_text(SOURCE)
    print(f"consumer built: {consumer}")

    empty = output / "empty"
    empty.mkdir()
    cases = []

    bundled = output / "bundled-both" / "bin"
    stub(bundled / binary("oapx"))
    preferred = stub(bundled / binary("oxap"))
    cases.append(("bundled package with both names", empty, {}, bundled, str(preferred)))

    bundled = output / "bundled-legacy" / "bin"
    legacy = stub(bundled / binary("oapx"))
    cases.append(("bundled package with only oapx", empty, {}, bundled, str(legacy)))

    work = output / "local"
    stub(work / "zig-out" / "bin" / binary("oapx"))
    preferred = stub(work / "zig" / "zig-out" / "bin" / binary("oxap"))
    cases.append(("local builds with both names", work, {}, None, str(preferred)))

    work = output / "local-legacy"
    legacy = stub(work / "zig-out" / "bin" / binary("oapx"))
    cases.append(("local build with only oapx", work, {}, None, str(legacy)))

    path_dir = output / "path"
    stub(path_dir / binary("oapx"))
    preferred = stub(path_dir / binary("oxap"))
    cases.append(("PATH with both names", empty, {"PATH": str(path_dir)}, None, str(preferred)))

    path_dir = output / "path-legacy"
    legacy = stub(path_dir / binary("oapx"))
    cases.append(("PATH with only oapx", empty, {"PATH": str(path_dir)}, None, str(legacy)))

    env_dir = output / "env"
    preferred = stub(env_dir / "from-oxap")
    legacy = stub(env_dir / "from-oap")
    cases.append(("OXAP_SDK_BINARY_PATH over OAP_SDK_BINARY_PATH", empty,
                  {"OXAP_SDK_BINARY_PATH": str(preferred), "OAP_SDK_BINARY_PATH": str(legacy)}, None,
                  str(preferred)))
    cases.append(("legacy OAP_SDK_BINARY_PATH alone", empty,
                  {"OAP_SDK_BINARY_PATH": str(legacy)}, None, str(legacy)))

    failures = 0
    for label, workdir, env, bundled_dir, want in cases:
        result = resolve(consumer, sdk, output, label, workdir, env, bundled_dir)
        got = result.stdout.strip()
        slug = label.replace(" ", "-").replace("/", "-")
        (output / f"{slug}.out").write_text(result.stdout)
        (output / f"{slug}.err").write_text(result.stderr)
        if result.returncode != 0 or got != want:
            failures += 1
            print(f"FAIL: {label}: exit={result.returncode} resolved={got!r} want={want!r}")
        else:
            print(f"PASS: {label}: {got}")

    payloads = Payloads()
    server = http.server.HTTPServer(("127.0.0.1", 0), payloads.handler())
    import threading

    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{server.server_port}"
    digests = {
        "oxap": hashlib.sha256(b"#!/bin/sh\necho oxap\n").hexdigest(),
        "oapx": hashlib.sha256(b"#!/bin/sh\necho oapx\n").hexdigest(),
    }
    try:
        url_cases = [
            ("OXAP_SDK_BINARY_URL over OAP_SDK_BINARY_URL", {
                "OXAP_SDK_BINARY_URL": f"{base}/preferred/oxap",
                "OXAP_SDK_BINARY_SHA256": digests["oxap"],
                "OAP_SDK_BINARY_URL": f"{base}/legacy/oapx",
                "OAP_SDK_BINARY_SHA256": digests["oapx"],
            }, ["/preferred/oxap"], binary("oxap")),
            ("legacy OAP_SDK_BINARY_URL alone", {
                "OAP_SDK_BINARY_URL": f"{base}/legacy/oapx",
                "OAP_SDK_BINARY_SHA256": digests["oapx"],
            }, ["/legacy/oapx"], binary("oapx")),
            ("an OXAP URL does not take the legacy checksum", {
                "OXAP_SDK_BINARY_URL": f"{base}/preferred/oxap",
                "OAP_SDK_BINARY_SHA256": digests["oapx"],
            }, [], None),
            ("a legacy URL does not take the OXAP checksum", {
                "OAP_SDK_BINARY_URL": f"{base}/legacy/oapx",
                "OXAP_SDK_BINARY_SHA256": digests["oxap"],
            }, [], None),
        ]
        for index, (label, env, want_requests, want_name) in enumerate(url_cases):
            payloads.requests.clear()
            result = resolve(consumer, sdk, output, f"url-{index}", empty, env)
            got = result.stdout.strip()
            (output / f"url-{index}.out").write_text(result.stdout)
            (output / f"url-{index}.err").write_text(result.stderr)
            if want_name is None:
                ok = result.returncode != 0 and "SHA256 checksum is required" in got and not payloads.requests
            else:
                ok = (result.returncode == 0 and Path(got).name == want_name
                      and payloads.requests == want_requests)
            if not ok:
                failures += 1
                print(f"FAIL: {label}: exit={result.returncode} resolved={got!r} "
                      f"requests={payloads.requests}")
            else:
                print(f"PASS: {label}: exit={result.returncode} resolved={got} "
                      f"requests={payloads.requests}")
    finally:
        server.shutdown()

    if failures:
        raise SystemExit(f"{failures} resolver case(s) failed; artifacts: {output}")
    print(f"PASS: OXAP names outrank OAP names at every step; legacy names still resolve. "
          f"Artifacts: {output}")


if __name__ == "__main__":
    main()
