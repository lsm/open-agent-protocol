import argparse
import copy
import json
import subprocess
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
NEGATIVE = [
    "queue-state-anchor-request-refused",
    "queue-state-anchor-request-unaccounted",
    "queue-state-anchor-request-unanswered",
    "queue-state-entry-anchor-admitted-elsewhere",
    "queue-state-entry-leads-a-different-run",
    "compound-open-anchor-names-an-open-without-a-message",
    "anchor-session-names-state-request",
    "anchor-entry-names-state-request",
    "anchor-foreign-request",
    "anchor-foreign-admission",
]
POSITIVE = [
    "queue-state-entry-leads-its-admission",
    "queue-state-anchor-request-reflected",
    "compound-open-admits-its-message",
    "compound-open-queued-message",
    "steer-state-pending",
    "steer-state-capture-straddles-admission",
]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", type=Path, required=True)
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)

    def run(command, label):
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
        (output / f"{label}.stdout").write_text(result.stdout)
        (output / f"{label}.stderr").write_text(result.stderr)
        return result

    for name, command in [
        ("go", ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"]),
        ("rust", ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"]),
        ("zig", ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"]),
    ]:
        result = run(command, f"{name}-build")
        assert result.returncode == 0, (name, result.stderr)
    commands = {
        "go": [str(output / "goap"), "validate", "--format=json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format=json"],
    }
    manifest = {f["id"]: f for f in json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]}

    def fixture(name):
        return json.loads((ROOT / "fixtures" / manifest[name]["path"]).read_text())

    def state(trace):
        return next(e["payload"] for e in trace if e["type"] == "session.state.response")

    cases = [(name, fixture(name), sorted(set(manifest[name].get("codes", [])))) for name in NEGATIVE + POSITIVE]
    for name in ["queue-state-anchor-request-refused", "anchor-foreign-request"]:
        trace = fixture(name)
        state(trace).pop("as_of")
        cases.append((name + "-remove-claim", trace, []))
    for name in ["queue-state-anchor-request-unaccounted", "queue-state-entry-anchor-admitted-elsewhere"]:
        trace = fixture(name)
        p = state(trace)
        p["active_runs"][0].pop("admitted_submit_requests", None)
        p["active_runs"].append(copy.deepcopy(state(fixture("queue-state-entry-leads-its-admission"))["active_runs"][1]))
        cases.append((name + "-account-for-run", trace, []))
    for name in ["anchor-session-names-state-request", "anchor-entry-names-state-request"]:
        trace = fixture(name)
        p = state(trace)
        target = p["as_of"] if "as_of" in p else p["active_runs"][0]
        target["admitted_submit_requests"] = ["req1"]
        cases.append((name + "-correct-kind", trace, []))
    trace = fixture("queue-state-entry-leads-its-admission")
    response = next(e for e in trace if e["id"] == "adm2")
    trace.remove(response)
    trace.insert(next(i for i, e in enumerate(trace) if e["type"] == "session.state.response"), response)
    cases.append(("admission-before-snapshot-repair", trace, []))
    for delayed in [False, True]:
        for reverse in [False, True]:
            trace = fixture("steer-state-pending")
            request = copy.deepcopy(next(e for e in trace if e["id"] == "sub2"))
            request["id"] = "sub3"
            response = copy.deepcopy(next(e for e in trace if e["in_reply_to"] == "sub2"))
            response.update(id="steer3-response", in_reply_to="sub3")
            response["payload"].update(submission_id="steer3", message_ids=["guidance3"])
            trace.insert(next(i for i, e in enumerate(trace) if e.get("in_reply_to") == "sub2"), request)
            p = state(trace)
            p["active_runs"][0]["pending_steers"].append({"submission_id": "steer3", "request_id": "sub3"})
            p["active_runs"][0]["admitted_submit_requests"] = ["sub1", "sub2", "sub3"]
            p["as_of"] = {"admitted_submit_requests": ["sub1", "sub2", "sub3"]}
            first = next(e for e in trace if e.get("in_reply_to") == "sub2")
            trace.remove(first)
            responses = [response, first] if reverse else [first, response]
            index = next(i for i, e in enumerate(trace) if e["type"] == "session.state.response")
            if delayed:
                index += 1
            trace[index:index] = responses
            applied = copy.deepcopy(next(e for e in trace if e["type"] == "run.steer.applied"))
            applied.update(id="steer3-applied", sequence=3)
            applied["payload"].update(submission_id="steer3", request_id="sub3", message_ids=["guidance3"])
            trace.insert(-1, applied)
            trace[-1]["sequence"] = 4
            cases.append((f"multiple-steer-anchors-delayed-{delayed}-reversed-{reverse}", trace, []))

    observations = []
    failures = []
    for label, trace, expected in cases:
        path = output / f"{label}.json"
        path.write_text(json.dumps(trace) + "\n")
        for language, command in commands.items():
            result = run([*command, str(path)], f"{label}-{language}")
            reports = json.loads(result.stdout)
            if isinstance(reports, dict):
                reports = [reports]
            diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
            codes = sorted({d["code"] for d in diagnostics})
            record = {"case": label, "language": language, "exit": result.returncode, "codes": codes}
            observations.append(record)
            okay = codes == expected and result.returncode == int(bool(expected)) and all(d["phase"] == "semantic" for d in diagnostics)
            if not okay:
                failures.append(record)
            if language == "rust":
                snapshots = {e["id"] for e in trace if e["type"] in ("session.open.response", "session.state.response", "session.state.updated")}
                assert all(d.get("envelope_id") in snapshots and d.get("pointer", "").startswith("/payload/") for d in diagnostics if d["code"] == "session_state_mismatch"), diagnostics
            print(("PASS" if okay else "FAIL") + ": " + json.dumps(record), flush=True)
    (output / "observations.json").write_text(json.dumps(observations, indent=2) + "\n")
    assert not failures, failures
    print(f"PASS: {len(cases)} admission-anchor traces agree across Go, Rust and Zig; 0 disagreements", flush=True)


if __name__ == "__main__":
    main()
