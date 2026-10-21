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
    "anchor-foreign-admission-before-snapshot",
    "anchor-foreign-entry-refused",
    "anchor-foreign-entry-wrong-run",
]
POSITIVE = [
    "queue-state-entry-leads-its-admission",
    "queue-state-anchor-request-reflected",
    "compound-open-admits-its-message",
    "anchor-message-open-assigns-session",
    "compound-open-queued-message",
    "steer-state-pending",
    "steer-state-capture-straddles-admission",
]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", type=Path, required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = args.output_dir.resolve()
    output.mkdir(parents=True, exist_ok=False)

    def run(command, label):
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=600)
        (output / f"{label}.stdout").write_text(result.stdout)
        (output / f"{label}.stderr").write_text(result.stderr)
        (output / f"{label}.exit").write_text(str(result.returncode) + "\n")
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
            response = copy.deepcopy(next(e for e in trace if e.get("in_reply_to") == "sub2"))
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

    trace = fixture("steer-state-pending")
    p = state(trace)
    p.pop("active_runs")
    p["as_of"] = {"admitted_submit_requests": ["sub2"]}
    response = next(e for e in trace if e.get("in_reply_to") == "sub2")
    trace.remove(response)
    trace.insert(next(i for i, e in enumerate(trace) if e["type"] == "session.state.response") + 1, response)
    cases.append(("scalar-active-run-accounts-for-steer", trace, []))
    trace = fixture("compound-open-admits-its-message")
    template = fixture("anchor-session-names-state-request")
    query = copy.deepcopy(next(e for e in template if e["id"] == "read"))
    response = copy.deepcopy(next(e for e in template if e["id"] == "snapshot"))
    p = response["payload"]
    p["active_runs"][0].update(as_of_sequence=0, admitted_submit_requests=["oc"])
    p["as_of"] = {"admitted_submit_requests": ["oc"]}
    trace[3:3] = [query, response]
    cases.append(("message-open-response-after-snapshot", trace, []))

    for name in ["anchor-foreign-admission", "anchor-foreign-admission-before-snapshot"]:
        trace = fixture(name)
        request = next(e for e in trace if e["id"] == "foreign-submit")
        request["session_id"] = request["payload"]["session_id"] = "s2"
        next(e for e in trace if e["id"] == "foreign-response")["session_id"] = "s2"
        state(trace)["as_of"]["admitted_submit_requests"] = ["req1"]
        cases.append((name + "-repair-request-and-anchor", trace, []))

    trace = fixture("compound-open-admits-its-message")[:4]
    trace[1]["payload"]["features"]["session.open.reopen"] = {"level": "native"}
    trace[2]["payload"]["reopen"] = True
    trace[3]["payload"]["recovery"] = {"recovered": True}
    cases.append(("reopen-message-without-a-new-admission", trace, ["missing_run_terminal", "session_state_mismatch"]))

    for name in ["anchor-foreign-entry-refused", "anchor-foreign-entry-wrong-run"]:
        trace = fixture(name)
        state(trace)["active_runs"] = [entry for entry in state(trace)["active_runs"] if entry["run_id"] != "r5"]
        cases.append((name + "-remove-foreign-entry", trace, []))

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
            if language == "rust" and label == "reopen-message-without-a-new-admission":
                okay = okay and all(d["message"] == "the answered request did not establish the claimed admission" for d in diagnostics if d["code"] == "session_state_mismatch")
            if not okay:
                failures.append(record)
            if language == "rust":
                snapshots = {e["id"] for e in trace if e["type"] in ("session.open.response", "session.state.response", "session.state.updated")}
                assert all(d.get("envelope_id") in snapshots and d.get("pointer", "").startswith("/payload/") for d in diagnostics if d["code"] == "session_state_mismatch"), diagnostics
            print(("PASS" if okay else "FAIL") + ": " + json.dumps(record), flush=True)
    (output / "observations.json").write_text(json.dumps(observations, indent=2) + "\n")
    assert not failures, failures
    print(f"PASS: {len(cases)} admission-anchor traces agree across Go, Rust and Zig; 0 disagreements", flush=True)
    if args.mutation_check:
        source = ROOT / "rust/validation/src/state/admission_anchors.rs"
        mutations = [
            ('Some("an anchor must name a submit or message-carrying open request on this session")', 'return;', "anchor-session-names-state-request"),
            ('Some("an anchor must name a submit or message-carrying open request on this session")', 'return;', "anchor-entry-names-state-request"),
            ('Some("an anchor must name a submit or message-carrying open request on this session")', 'return;', "compound-open-anchor-names-an-open-without-a-message"),
            ('Some("an anchor must name a submit or message-carrying open request on this session")', 'return;', "anchor-foreign-request"),
            ('Some("the anchor was admitted on another session")', 'None', "anchor-foreign-admission"),
            ('Some("the anchor was admitted to a different run")', 'None', "queue-state-entry-anchor-admitted-elsewhere"),
            ('Some("the snapshot accounts for the anchor\'s admitted run nowhere")', 'None', "queue-state-anchor-request-unaccounted"),
            ('Some("the answered request did not establish the claimed admission")', 'None', "queue-state-anchor-request-refused"),
            ('Some("the claimed admission received no response")', 'None', "queue-state-anchor-request-unanswered"),
            ('run.steers.values().any(|steer| steer.request == request)', 'false', "multiple-steer-anchors-delayed-True-reversed-True"),
            ('request.envelope_type == EnvelopeType::SESSION_OPEN_REQUEST', 'false', "message-open-response-after-snapshot"),
        ]
        mutations.extend([
            ('let run = self\n                .runs\n                .get(id)\n                .filter(|run| run.session == p.session_id.as_str());', 'if self.runs.get(id).is_some_and(|run| run.session != p.session_id.as_str()) { continue; } let run = self.runs.get(id);', "anchor-foreign-entry-refused"),
            ('let run = self\n                .runs\n                .get(id)\n                .filter(|run| run.session == p.session_id.as_str());', 'if self.runs.get(id).is_some_and(|run| run.session != p.session_id.as_str()) { continue; } let run = self.runs.get(id);', "anchor-foreign-entry-wrong-run"),
            ('the answered request did not establish the claimed admission', 'the claimed admission was refused', "reopen-message-without-a-new-admission"),
        ])
        mutations = [("rust", source, old, new, target) for old, new, target in mutations]
        mutations.extend([
            ("rust", ROOT / "rust/validation/src/state/admissions.rs", 'request.session = s(&p.session_id);', 'request.session = String::new();', "anchor-message-open-assigns-session"),
            ("go", ROOT / "go/validation/queuestate.go", 'candidate.session == session && (run == "" || candidate.id == run)', '(run == "" || candidate.id == run)', "anchor-foreign-admission-before-snapshot"),
            ("zig", ROOT / "zig/src/validation/semantic.zig", 'std.mem.eql(u8, run.session, session_id) and (run_id.len == 0 or std.mem.eql(u8, run.id, run_id))', '(run_id.len == 0 or std.mem.eql(u8, run.id, run_id))', "anchor-foreign-admission-before-snapshot"),
        ])
        builds = {
            "rust": ["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-validation", "--example", "validate"],
            "go": ["go", "build", "-o", str(output / "goap"), "./go/cmd/goap"],
            "zig": ["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"],
        }
        try:
            for index, (language, source, old, new, target) in enumerate(mutations):
                original = source.read_text()
                assert original.count(old) == (2 if language == "go" else 1), old
                try:
                    source.write_text(original.replace(old, new))
                    build = run(builds[language], f"mutation-{index}-build")
                    assert build.returncode == 0, build.stderr
                    result = run([*commands[language], str(output / f"{target}.json")], f"mutation-{index}-result")
                    reports = json.loads(result.stdout)
                    if isinstance(reports, dict):
                        reports = [reports]
                    diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
                    codes = sorted({d["code"] for d in diagnostics})
                    baseline = next(r for r in observations if r["case"] == target and r["language"] == language)
                    changed = (result.returncode, codes) != (baseline["exit"], baseline["codes"])
                    if target == "reopen-message-without-a-new-admission":
                        changed = changed or any(d["code"] == "session_state_mismatch" and d["message"] != "the answered request did not establish the claimed admission" for d in diagnostics)
                    assert changed, (target, reports)
                    assert result.returncode in (0, 1) and all(d["phase"] == "semantic" for d in diagnostics), reports
                    if language in ("go", "zig"):
                        assert codes == ["scope_mismatch"], reports
                    print(f"PASS: {language} mutation {index} caught by {target}", flush=True)
                finally:
                    source.write_text(original)
        finally:
            for language, command in builds.items():
                build = run(command, f"restored-{language}-build")
                assert build.returncode == 0, build.stderr
        for label, _, expected in cases:
            for language, command in commands.items():
                result = run([*command, str(output / f"{label}.json")], f"restored-{label}-{language}")
                reports = json.loads(result.stdout)
                if isinstance(reports, dict):
                    reports = [reports]
                codes = sorted({d["code"] for report in reports for d in report.get("diagnostics") or []})
                assert result.returncode == int(bool(expected)) and codes == expected, (label, reports)
        print(f"PASS: {len(mutations)} source mutations caught; sources restored and all three CLIs replayed", flush=True)


if __name__ == "__main__":
    main()
