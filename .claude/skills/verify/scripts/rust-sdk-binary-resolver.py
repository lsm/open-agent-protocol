#!/usr/bin/env python3
"""Observe the public Rust SDK's runtime binary precedence end to end."""
import argparse
import hashlib
import http.server
import os
from pathlib import Path
import shutil
import subprocess
import threading

ROOT = Path(__file__).resolve().parents[4]
SDK_MANIFEST = ROOT / "rust" / "sdk" / "Cargo.toml"
ENV_NAMES = ["OXAP_SDK_BINARY_PATH", "OAP_SDK_BINARY_PATH", "OXAP_SDK_BINARY_URL",
             "OAP_SDK_BINARY_URL", "OXAP_SDK_BINARY_SHA256", "OAP_SDK_BINARY_SHA256"]
CONSUMER_MANIFEST = r'''[package]
name = "resolver-probe"
version = "0.0.0"
edition = "2021"

[dependencies]
oxap-sdk = { path = "SDK_PATH", features = ["download"] }
tokio = { version = "1", features = ["macros", "rt"] }
'''
SOURCE = r'''use oxap_sdk::BinaryResolver;

#[tokio::main(flavor = "current_thread")]
async fn main() {
    let mut resolver = BinaryResolver::default();
    resolver.base_dir = Some(std::path::PathBuf::from(
        std::env::var("PROBE_CWD").expect("PROBE_CWD"),
    ));
    resolver.cache_dir = Some(std::path::PathBuf::from(
        std::env::var("PROBE_CACHE").expect("PROBE_CACHE"),
    ));
    if let Ok(url) = std::env::var("PROBE_OPTION_URL") {
        resolver.binary_url = Some(url);
        resolver.checksum_sha256 = std::env::var("PROBE_OPTION_CHECKSUM").ok();
    }
    match resolver.resolve().await {
        Ok(path) => println!("{}", path.display()),
        Err(error) => {
            println!("error: {error}");
            std::process::exit(3);
        }
    }
}
'''


def run(command, cwd=ROOT, timeout=3600):
    result = subprocess.run(command, cwd=cwd, capture_output=True, text=True, timeout=timeout)
    assert result.returncode == 0, (command, result.stdout, result.stderr)
    return result.stdout


def stub(path):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("#!/bin/sh\n")
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
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def log_message(self, *args):
                pass

        return Handler


def resolve(consumer, output, label, workdir, env, cache=None, option_url=None,
            option_checksum=None):
    environment = {name: value for name, value in os.environ.items() if name not in ENV_NAMES}
    environment.update(env)
    environment["PROBE_CWD"] = str(workdir)
    environment["PROBE_CACHE"] = str(cache or output / f"cache-{label}")
    if option_url is not None:
        environment["PROBE_OPTION_URL"] = option_url
        environment["PROBE_OPTION_CHECKSUM"] = option_checksum or ""
    return subprocess.run([str(consumer)], cwd=workdir, env=environment,
                          capture_output=True, text=True, timeout=180)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)

    crate = output / "consumer"
    (crate / "src").mkdir(parents=True)
    (crate / "Cargo.toml").write_text(
        CONSUMER_MANIFEST.replace("SDK_PATH", str(SDK_MANIFEST.parent)))
    (crate / "src" / "main.rs").write_text(SOURCE)
    environment = dict(os.environ, CARGO_TARGET_DIR=str(ROOT / "rust" / "target"))
    build = subprocess.run(["cargo", "build", "--manifest-path", str(crate / "Cargo.toml")],
                           cwd=crate, env=environment, capture_output=True, text=True,
                           timeout=3600)
    assert build.returncode == 0, (build.stdout, build.stderr)
    consumer = ROOT / "rust" / "target" / "debug" / binary("resolver-probe")
    assert consumer.is_file(), f"consumer missing: {consumer}"
    print(f"consumer built: {consumer}")

    empty = output / "empty"
    empty.mkdir()
    cases = []

    env_dir = output / "env"
    preferred = stub(env_dir / "from-oxap")
    legacy = stub(env_dir / "from-oap")
    cases.append(("OXAP_SDK_BINARY_PATH over OAP_SDK_BINARY_PATH",
                  {"OXAP_SDK_BINARY_PATH": str(preferred), "OAP_SDK_BINARY_PATH": str(legacy)},
                  empty, str(preferred)))
    cases.append(("legacy OAP_SDK_BINARY_PATH alone",
                  {"OAP_SDK_BINARY_PATH": str(legacy)}, empty, str(legacy)))

    work = output / "local"
    stub(work / "zig-out" / "bin" / binary("oapx"))
    preferred = stub(work / "zig" / "zig-out" / "bin" / binary("oxap"))
    cases.append(("local builds with both names", {}, work, str(preferred)))

    work = output / "local-legacy"
    legacy = stub(work / "zig-out" / "bin" / binary("oapx"))
    cases.append(("local build with only oapx", {}, work, str(legacy)))

    path_dir = output / "path"
    stub(path_dir / binary("oapx"))
    preferred = stub(path_dir / binary("oxap"))
    cases.append(("PATH with both names", {"PATH": str(path_dir)}, empty, str(preferred)))

    path_dir = output / "path-legacy"
    legacy = stub(path_dir / binary("oapx"))
    cases.append(("PATH with only oapx", {"PATH": str(path_dir)}, empty, str(legacy)))

    empty_path = output / "empty-path"
    empty_path.mkdir()
    cases.append(("bare-name fallback", {"PATH": str(empty_path)}, empty, binary("oxap")))

    failures = 0
    for label, env, workdir, want in cases:
        result = resolve(consumer, output, label, workdir, env)
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
            result = resolve(consumer, output, f"url-{index}", empty, env)
            got = result.stdout.strip()
            (output / f"url-{index}.out").write_text(result.stdout)
            (output / f"url-{index}.err").write_text(result.stderr)
            if want_name is None:
                ok = (result.returncode != 0 and "SHA-256 checksum is required" in got
                      and not payloads.requests)
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

        payloads.requests.clear()
        result = resolve(consumer, output, "option-url-oxap-checksum", empty, {
            "OXAP_SDK_BINARY_SHA256": digests["oxap"],
        }, cache=output / "cache-option-oxap", option_url=f"{base}/preferred/oxap",
            option_checksum="0" * 64)
        got = result.stdout.strip()
        (output / "option-url-oxap-checksum.out").write_text(result.stdout)
        if result.returncode != 0 or Path(got).name != binary("oxap"):
            failures += 1
            print(f"FAIL: an option URL takes the OXAP environment checksum: "
                  f"exit={result.returncode} resolved={got!r}")
        else:
            print(f"PASS: an option URL takes the OXAP environment checksum: {got}")

        payloads.requests.clear()
        result = resolve(consumer, output, "option-url-legacy-checksum", empty, {
            "OAP_SDK_BINARY_SHA256": digests["oapx"],
        }, cache=output / "cache-option-oap", option_url=f"{base}/legacy/oapx",
            option_checksum="0" * 64)
        got = result.stdout.strip()
        (output / "option-url-legacy-checksum.out").write_text(result.stdout)
        if result.returncode != 0 or Path(got).name != binary("oapx"):
            failures += 1
            print(f"FAIL: an option URL takes the legacy environment checksum: "
                  f"exit={result.returncode} resolved={got!r}")
        else:
            print(f"PASS: an option URL takes the legacy environment checksum: {got}")
    finally:
        server.shutdown()

    if failures:
        raise SystemExit(f"{failures} resolver case(s) failed; artifacts: {output}")
    print(f"PASS: OXAP names outrank OAP names at every step; legacy names still resolve. "
          f"Artifacts: {output}")


if __name__ == "__main__":
    main()
