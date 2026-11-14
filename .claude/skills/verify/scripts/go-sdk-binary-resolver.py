#!/usr/bin/env python3
"""Observe the public Go SDK's runtime binary precedence end to end."""
import argparse
import hashlib
import http.server
import os
from pathlib import Path
import subprocess
import threading

ROOT = Path(__file__).resolve().parents[4]
SOURCE = r'''package main

import (
    "context"
    "fmt"
    "os"

    "github.com/lsm/oxap/go/sdk"
)

func main() {
    resolved, err := sdk.ResolveBinary(context.Background(), &sdk.Options{CacheDir: os.Getenv("PROBE_CACHE")})
    if err != nil {
        fmt.Printf("error: %v\n", err)
        os.Exit(3)
    }
    fmt.Println(resolved)
}
'''
ENV_NAMES = ["OXAP_SDK_BINARY_PATH", "OAP_SDK_BINARY_PATH", "OXAP_SDK_BINARY_URL",
             "OAP_SDK_BINARY_URL", "OXAP_SDK_BINARY_SHA256", "OAP_SDK_BINARY_SHA256"]


def run(command, cwd=ROOT):
    result = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=300)
    assert result.returncode == 0, (command, result.stdout, result.stderr)
    return result.stdout


def script(path, body="#!/bin/sh\n"):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(body)
    path.chmod(0o755)
    return path


class Payloads:
    def __init__(self):
        self.requests = []

    def handler(self):
        outer = self

        class Handler(http.server.BaseHTTPRequestHandler):
            def do_GET(self):
                outer.requests.append(self.path)
                body = b"#!/bin/sh\necho preferred\n" if self.path.endswith("oxap") else b"#!/bin/sh\necho legacy\n"
                self.send_response(200)
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        return Handler


def resolve(consumer, workdir, env, extra):
    environment = {name: value for name, value in os.environ.items() if name not in ENV_NAMES}
    environment.update(env, PATH=extra.get("PATH", environment.get("PATH", "")),
                       PROBE_CACHE=str(extra["cache"]))
    result = subprocess.run([str(consumer)], cwd=workdir, env=environment,
                            capture_output=True, text=True, timeout=120)
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    module = output / "module"
    module.mkdir()
    (module / "go.mod").write_text(
        "module probe\n\ngo 1.26\n\nrequire github.com/lsm/oxap v0.0.0\n\n"
        f"replace github.com/lsm/oxap => {ROOT}\n")
    (module / "main.go").write_text(SOURCE)
    consumer = module / "consumer"
    run(["go", "build", "-o", str(consumer), "."], cwd=module)
    print(f"consumer built: {consumer}")

    (output / "empty").mkdir()

    cases = []

    work = output / "local"
    legacy = script(work / "zig-out/bin/oapx")
    cases.append(("local build with only oapx", work, {}, {}, str(legacy)))

    work = output / "both-local"
    script(work / "zig-out/bin/oapx")
    preferred = script(work / "zig/zig-out/bin/oxap")
    cases.append(("local builds with both names", work, {}, {}, str(preferred)))

    path_dir = output / "path"
    script(path_dir / "oapx")
    preferred = script(path_dir / "oxap")
    cases.append(("PATH with both names", output / "empty", {}, {"PATH": str(path_dir)}, str(preferred)))

    path_dir = output / "path-legacy"
    preferred = script(path_dir / "oapx")
    cases.append(("PATH with only oapx", output / "empty", {}, {"PATH": str(path_dir)}, str(preferred)))

    prefer_dir = output / "prefer-env"
    preferred = script(prefer_dir / "from-oxap")
    legacy = script(prefer_dir / "from-oap")
    cases.append(("OXAP_SDK_BINARY_PATH over OAP_SDK_BINARY_PATH", prefer_dir,
                  {"OXAP_SDK_BINARY_PATH": str(preferred), "OAP_SDK_BINARY_PATH": str(legacy)}, {},
                  str(preferred)))
    cases.append(("legacy OAP_SDK_BINARY_PATH alone", prefer_dir,
                  {"OAP_SDK_BINARY_PATH": str(legacy)}, {}, str(legacy)))

    failures = 0
    for index, (label, workdir, env, extra, want) in enumerate(cases):
        result = resolve(consumer, workdir, env, dict(extra, cache=output / f"cache-{index}"))
        got = result.stdout.strip()
        (output / f"case-{index}.out").write_text(result.stdout)
        (output / f"case-{index}.err").write_text(result.stderr)
        if result.returncode != 0 or got != want:
            failures += 1
            print(f"FAIL: {label}: exit={result.returncode} resolved={got!r} want={want!r}")
        else:
            print(f"PASS: {label}: {got}")

    payloads = Payloads()
    server = http.server.HTTPServer(("127.0.0.1", 0), payloads.handler())
    thread = threading.Thread(target=server.serve_forever, daemon=True)
    thread.start()
    base = f"http://127.0.0.1:{server.server_port}"
    digests = {
        "preferred": hashlib.sha256(b"#!/bin/sh\necho preferred\n").hexdigest(),
        "legacy": hashlib.sha256(b"#!/bin/sh\necho legacy\n").hexdigest(),
    }
    try:
        cases = [
            ("OXAP_SDK_BINARY_URL over OAP_SDK_BINARY_URL", {
                "OXAP_SDK_BINARY_URL": f"{base}/preferred/oxap",
                "OXAP_SDK_BINARY_SHA256": digests["preferred"],
                "OAP_SDK_BINARY_URL": f"{base}/legacy/oapx",
                "OAP_SDK_BINARY_SHA256": digests["legacy"],
            }, "/preferred/oxap", "oxap"),
            ("legacy OAP_SDK_BINARY_URL alone", {
                "OAP_SDK_BINARY_URL": f"{base}/legacy/oapx",
                "OAP_SDK_BINARY_SHA256": digests["legacy"],
            }, "/legacy/oapx", "oapx"),
        ]
        for index, (label, env, want_request, want_name) in enumerate(cases):
            payloads.requests.clear()
            result = resolve(consumer, output / "empty", env, {"cache": output / f"url-cache-{index}"})
            got = result.stdout.strip()
            (output / f"url-{index}.out").write_text(result.stdout)
            (output / f"url-{index}.err").write_text(result.stderr)
            if (result.returncode != 0 or Path(got).name != want_name
                    or payloads.requests != [want_request]):
                failures += 1
                print(f"FAIL: {label}: exit={result.returncode} resolved={got!r} "
                      f"requests={payloads.requests}")
            else:
                print(f"PASS: {label}: {Path(got).name} from {payloads.requests}")
    finally:
        server.shutdown()

    if failures:
        raise SystemExit(f"{failures} resolver case(s) failed; artifacts: {output}")
    print(f"PASS: OXAP names outrank OAP names at every step; legacy names still resolve. Artifacts: {output}")


if __name__ == "__main__":
    main()
