#!/usr/bin/env python3
"""Exercise Rust's public validator on control-tool traces and semantic mutations."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]


def command(args):
    return subprocess.run(args, cwd=ROOT, capture_output=True, text=True, timeout=180)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    cargo = ["cargo", "--manifest-path", "rust/Cargo.toml"]
    build = command([cargo[0], "build", *cargo[1:], "-p", "oap-validation", "--example", "validate"])
    (output / "build.log").write_text(build.stdout + build.stderr)
    assert build.returncode == 0, build.stderr
    metadata = command([cargo[0], "metadata", *cargo[1:], "--no-deps", "--format-version", "1"])
    assert metadata.returncode == 0, metadata.stderr
    binary = Path(json.loads(metadata.stdout)["target_directory"]) / "debug/examples/validate"

    compared = []

    def replay(label, path, expected, phase="semantic"):
        result = command([str(binary), str(path)])
        (output / f"{label}.result.json").write_text(result.stdout)
        (output / f"{label}.stderr").write_text(result.stderr)
        assert result.returncode == int(bool(expected)), (label, result.stderr, result.stdout)
        diagnostics = json.loads(result.stdout)["diagnostics"]
        actual = {d["code"] for d in diagnostics}
        compared.append((str(path), set(expected), actual))
        assert actual == set(expected), (label, actual, expected, diagnostics)
        assert all(d["phase"] == phase for d in diagnostics), (label, diagnostics)
        print(f"{label}: exit {result.returncode}, codes {sorted(actual)}", flush=True)
        return diagnostics

    fixtures = ROOT / "fixtures"
    gaps = set(json.loads((fixtures / "known-gaps.json").read_text())["rust"])
    manifest = json.loads((fixtures / "manifest.json").read_text())["fixtures"]
    count = 0
    for entry in manifest:
        if not entry["id"].startswith(("control-call-", "control-tool-", "open-provide-", "provide-name-pattern-")):
            continue
        if entry["id"] in gaps:
            print(f"Known unported fixture: {entry['id']}", flush=True)
            continue
        replay(entry["id"], fixtures / entry["path"], entry.get("codes", []), entry.get("phase", "semantic"))
        count += 1

    base = json.loads((fixtures / "valid/control-tool-roundtrip.json").read_text())
    for label, identifier, key, replacement, expected in [
        ("mutated-binding", "res-req", "tool_call_id", "other-call", "scope_mismatch"),
        ("mutated-request-id", "ev-completed", "request_id", "unobserved-request", "unmatched_interaction"),
        ("mutated-result", "ev-completed", "result", {"output": "changed"}, "resolution_payload_mismatch"),
        ("mutated-responder", "res-req", "responded_by", "agent", "wrong_interaction_responder"),
    ]:
        trace = copy.deepcopy(base)
        event = next(e for e in trace if e["id"] == identifier)
        event["payload"][key] = replacement
        if key == "tool_call_id":
            event["tool_call_id"] = replacement
        path = output / f"{label}.trace.json"
        path.write_text(json.dumps(trace))
        replay(label, path, [expected])

    trace = json.loads((fixtures / "semantic-invalid/provide-name-pattern-not-re2.json").read_text())
    descriptor = next(e for e in trace if e["type"] == "capabilities.response")
    descriptor["payload"]["features"]["action.tools.provide"]["limits"]["name_pattern"] = "[a-z]+"
    path = output / "repaired-name-pattern.trace.json"
    path.write_text(json.dumps(trace))
    replay("repaired-name-pattern", path, [])
    trace = copy.deepcopy(base)
    next(e for e in trace if e["id"] == "res-req")["payload"]["result"] = {"count": 1, "ok": True}
    completion = next(e for e in trace if e["id"] == "ev-completed")
    completion["payload"]["result"] = {"ok": True, "count": 1.0}
    path = output / "canonical-json.trace.json"
    path.write_text(json.dumps(trace))
    replay("canonical-json", path, [])
    completion["payload"]["result"]["count"] = 2
    path = output / "canonical-json-changed.trace.json"
    path.write_text(json.dumps(trace))
    replay("canonical-json-changed", path, ["resolution_payload_mismatch"])
    trace = json.loads((fixtures / "valid/open-provide-unadvertised-rejected.json").read_text())
    next(e for e in trace if e["type"] == "session.open.request")["payload"]["subscribe"] = True
    error = next(e for e in trace if e["type"] == "error.response")
    error["payload"]["error"]["details"]["feature"] = "session.open.subscribe"
    path = output / "compound-subscribe-refusal.trace.json"
    path.write_text(json.dumps(trace))
    replay("compound-subscribe-refusal", path, [])
    error["payload"]["error"]["details"]["feature"] = "unrelated.feature"
    path = output / "compound-unrelated-refusal.trace.json"
    path.write_text(json.dumps(trace))
    replay("compound-unrelated-refusal", path, ["unavailable_capability"])
    unicode17 = json.loads((fixtures / "semantic-invalid/open-provide-refused-within-a-unicode17-pattern.json").read_text())
    path = output / "unicode17-descriptor.trace.json"
    path.write_text(json.dumps(unicode17[:4]))
    replay("unicode17-descriptor", path, [])
    valid_patterns = [r"\pL+", r"\p{Greek}", r"\p{^Greek}", "[[:alpha:]]", r"\bword",
                      "^*a", "café", r"\x{100}", r"\Qa.b\E", "a{01}", "a{,3}",
                      "(?)a", "(?P<a>x)(?P<a>y)", r"\p{Letter}", "(?P<1>a)", "(?ii)a", r"\000", r"\123", r"\12", "[^^]", r"[\d-a]", "[--a]"]
    invalid_patterns = ["[a-z", "(ab", "ab)", "*a", "a**", "[z-a]", "[a--b]", r"\q",
                        "(?P<>x)", "a{2,1}", "x{1001}", "(?i)[a-", "(?=a)", "(?<!a)b",
                        "[[:foo:]]", r"\1", r"\8", r"\C", r"[\b]", "(?i-)a",
                        "(?P<é>a)", "{2}", "a|{2}", "({2})", "a*{2}",
                        "a{2}{3}", "a{2}*", "(?:[a-z]{1000}){1000}", "(?x)a",
                        r"\p{Script=Greek}", "(?u)a", r"\u0061", r"[\x{+41}]", r"[\x+4]"]
    descriptor_trace = json.loads((fixtures / "semantic-invalid/provide-name-pattern-not-re2.json").read_text())
    for index, (pattern, expected) in enumerate([(p, []) for p in valid_patterns]
                                               + [(p, ["undisclosed_provide_limit"]) for p in invalid_patterns]):
        trace = copy.deepcopy(descriptor_trace)
        next(e for e in trace if e["type"] == "capabilities.response")["payload"]["features"]["action.tools.provide"]["limits"]["name_pattern"] = pattern
        path = output / f"re2-syntax-{index}.trace.json"
        path.write_text(json.dumps(trace))
        replay(f"re2-syntax-{index}", path, expected)
    matching = [("[a-z]+", "echo-1", False), ("ab|cd", "cd", True), ("(?i)[a-z]+", "ECHO", True),
                ("[a-z]+", "é", False), (r"\w+", "é", False), (r"\d+", "١", False),
                (r"\s", "\t", True), (r"\s", "t", False), (r"\s", "\u000b", False),
                (r"\Qecho.+\E", "echo.+", True), ("[a&&b]+", "a&b", True),
                ("[^^]+", "abc", True), ("[^^]+", "a^", False), (r"[\d-a]+", "-a1", True),
                ("[--a]+", "0-a", True), (r"\p{Greek}+", "α", True), (r"\b.\b", "é", False)]
    for index, (pattern, name, matches) in enumerate(matching):
        trace = json.loads((fixtures / "valid/open-provide-refused-name-partly-matching-pattern.json").read_text())
        next(e for e in trace if e["type"] == "capabilities.response")["payload"]["features"]["action.tools.provide"]["limits"]["name_pattern"] = pattern
        next(e for e in trace if e["type"] == "session.open.request")["payload"]["tools"][0]["name"] = name
        next(e for e in trace if e["type"] == "error.response")["payload"]["error"]["details"]["tool"] = name
        path = output / f"re2-match-{index}.trace.json"
        path.write_text(json.dumps(trace))
        replay(f"re2-match-{index}", path, ["undisclosed_provide_limit"] if matches else [])
    go_binary = output / "goap"
    built = command(["go", "build", "-o", str(go_binary), "./go/cmd/goap"])
    (output / "go-build.log").write_text(built.stdout + built.stderr)
    assert built.returncode == 0, built.stderr
    result = command([str(go_binary), "validate", "--format=json", *[p for p, _, _ in compared]])
    (output / "go-comparison.json").write_text(result.stdout)
    (output / "go-comparison.stderr").write_text(result.stderr)
    assert result.returncode == int(any(expected for _, expected, _ in compared)), result.stderr
    reports = json.loads(result.stdout)
    assert len(reports) == len(compared)
    for report, (path, expected, rust) in zip(reports, compared):
        diagnostics = report.get("diagnostics") or []
        go = {d["code"] for d in diagnostics}
        assert go == rust == expected, (path, go, rust, expected, diagnostics)
        assert all(d["phase"] == "semantic" for d in diagnostics), (path, diagnostics)
    gap = fixtures / "semantic-invalid/open-provide-refused-within-a-unicode17-pattern.json"
    rust_gap = command([str(binary), str(gap)])
    go_gap = command([str(go_binary), "validate", "--format=json", str(gap)])
    zig_gap = command([str(ROOT / "zig/zig-out/bin/oapx"), "validate", str(gap)])
    for name, result in [("rust", rust_gap), ("go", go_gap), ("zig", zig_gap)]:
        (output / f"known-unicode17-{name}.stdout").write_text(result.stdout)
        (output / f"known-unicode17-{name}.stderr").write_text(result.stderr)
    assert rust_gap.returncode == 0 and json.loads(rust_gap.stdout)["diagnostics"] == []
    assert zig_gap.returncode == 0 and "PASS" in zig_gap.stdout
    assert go_gap.returncode == 1, go_gap.stderr
    diagnostics = json.loads(go_gap.stdout)[0]["diagnostics"]
    assert {d["code"] for d in diagnostics} == {"undisclosed_provide_limit"}, diagnostics
    assert all(d["phase"] == "semantic" and d["pointer"] == "/payload/error" for d in diagnostics)
    print("KNOWN GAP observed: Go rejects the Unicode 17 refusal; Rust and Zig omit that judgment", flush=True)
    print(f"PASS: {count} shared traces, nine consumer variations, a valid Unicode 17 descriptor, and {len(valid_patterns) + len(invalid_patterns) + len(matching)} RE2 probes; {len(compared)} Go/Rust comparisons, zero disagreements", flush=True)


if __name__ == "__main__":
    main()
