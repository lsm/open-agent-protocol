#!/usr/bin/env python3
"""Replay synthetic adapter consumers through public Rust and Go APIs."""
import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = ROOT / "rust/adapter/src/prompt.rs"


def run(command, output, name, timeout=300):
    result = subprocess.run(command, cwd=ROOT, text=True, capture_output=True, timeout=timeout)
    (output / f"{name}.stdout").write_text(result.stdout)
    (output / f"{name}.stderr").write_text(result.stderr)
    assert result.returncode == 0, (name, result.returncode, result.stderr[-3000:])
    return result.stdout


def mutation_check(output):
    original = SOURCE.read_text()
    mutations = [
        ("duplicate", "sequence <= last_sequence", "sequence < last_sequence", "completed_returns_authoritative_parts_usage_and_drops_open_stream"),
        ("usage", "outcome.usage = completed.usage;", "outcome.usage = None;", "completed_returns_authoritative_parts_usage_and_drops_open_stream"),
        ("failure", "RunErrorKind::Failed(Box::new(failed.error))", "RunErrorKind::Cancelled", "failed_run_preserves_failure_and_measured_zero"),
        ("terminal", "Err(RunErrorKind::NoTerminal)", "Ok(())", "cancelled_and_exhausted_stream_are_distinct_and_keep_admission_id"),
        ("stream-drop", "outcome.usage = completed.usage;", "outcome.usage = completed.usage;\n                std::mem::forget(stream);", "completed_returns_authoritative_parts_usage_and_drops_open_stream"),
    ]
    try:
        for name, old, new, test in mutations:
            assert original.count(old) == 1, name
            SOURCE.write_text(original.replace(old, new))
            result = subprocess.run(["cargo", "test", "--manifest-path", "rust/Cargo.toml", "-p", "oap-adapter", "--test", "prompt", test], cwd=ROOT, text=True, capture_output=True, timeout=300)
            log = result.stdout + result.stderr
            (output / f"mutation-{name}.log").write_text(log)
            assert result.returncode != 0 and "test result: FAILED" in log, (name, log[-3000:])
            print(f"KILLED: {name} by public outcome assertion", flush=True)
    finally:
        SOURCE.write_text(original)
    run(["cargo", "test", "--manifest-path", "rust/Cargo.toml", "-p", "oap-adapter", "--test", "prompt"], output, "restored-tests")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    if args.mutation_check:
        mutation_check(output)
        return
    run(["cargo", "build", "--manifest-path", "rust/Cargo.toml", "-p", "oap-adapter", "--example", "prompt"], output, "rust-build")
    metadata = json.loads(run(["cargo", "metadata", "--manifest-path", "rust/Cargo.toml", "--no-deps", "--format-version=1"], output, "metadata"))
    rust = [str(Path(metadata["target_directory"]) / "debug/examples/prompt")]
    go = [str(output / "go-reference")]
    run(["go", "build", "-o", go[0], "rust/adapter/tests/testdata/prompt_reference.go"], output, "go-build")
    cases = []
    for name, kind in [("core-completed", "completed"), ("admission-queued-prestart-failed", "failed"), ("admission-queued-prestart-cancelled", "cancelled")]:
        trace = json.loads((ROOT / f"fixtures/valid/{name}.json").read_text())
        admission = next(e["payload"] for e in trace if e["type"] == "session.message.submit.response")
        events = [e for e in trace if "sequence" in e]
        cases.append((name, {"admission": admission, "events": events}, kind))
    base = copy.deepcopy(cases[0][1])
    for usage in [None, {}, {"input_tokens": 0}, {"input_tokens": 2**64-1, "output_tokens": 0, "cache_read_input_tokens": 0, "cache_write_input_tokens": 7, "reasoning_tokens": 0, "total_tokens": 2**64-1}]:
        case = copy.deepcopy(base)
        terminal = case["events"][-1]["payload"]
        if usage is None:
            terminal.pop("usage", None)
        else:
            terminal["usage"] = usage
        cases.append((f"usage-{len(cases)}", case, "completed"))
    for content in ["é漢😀", [{"type": "text", "text": "one"}, {"type": "image", "image": {"url": "synthetic:image"}}, {"type": "reasoning", "reasoning": "hidden"}, {"type": "text", "text": "two"}], [], None, 42, ["bad"]]:
        cases.append((f"text-{len(cases)}", {"message": {"role": "assistant", "content": content}}, "content" if content == 42 or content == ["bad"] else "completed"))
    for label, change, kind in [
        ("no-terminal", lambda c: c["events"].pop(), "no_terminal"),
        ("empty-stream", lambda c: c.update(events=[]), "no_terminal"),
        ("stream-error", lambda c: c.update(events=[], stream_error=True), "adapter"),
        ("submission-error", lambda c: c.update(submit_error=True), "adapter"),
        ("terminal-open-stream", lambda c: c.update(hold=True), "completed"),
    ]:
        case = copy.deepcopy(base)
        change(case)
        cases.append((label, case, kind))
    call = {"protocol": "open-agent-protocol", "version": "0.1", "profile": "open-agent-protocol.agent-control-core", "type": "action.call.started", "id": "call-start", "tool_call_id": "call-1", "run_id": "r1", "session_id": "s1", "sequence": 2, "payload": {"session_id": "s1", "run_id": "r1", "tool_call_id": "call-1", "execution_owner": "agent"}}
    case = copy.deepcopy(base)
    terminal = case["events"][-1]
    terminal["sequence"] = 3
    case["events"] = [case["events"][0], call, copy.deepcopy(call), terminal]
    cases.append(("duplicate-call-start", case, "completed"))
    gate_trace = json.loads((ROOT / "fixtures/valid/control-tool-roundtrip.json").read_text())
    gate_admission = next(e["payload"] for e in gate_trace if e["type"] == "session.message.submit.response")
    gate_events = [e for e in gate_trace if "sequence" in e]
    cases.append(("provided-call-gate", {"admission": gate_admission, "events": gate_events, "hold": True}, "gate_unanswered"))
    for typ, fixture in [
        ("user.input.requested", "user-input-resolved-in-the-refresh-window"),
        ("action.permission.requested", "permission-resolved-in-the-refresh-window"),
    ]:
        trace = json.loads((ROOT / f"fixtures/valid/{fixture}.json").read_text())
        gate = next(e for e in trace if e["type"] == typ)
        case = copy.deepcopy(base)
        case["events"] = [gate]
        cases.append((typ, case, "gate_unanswered"))
    for typ, fixture in [
        ("user.input.requested", "user-input-resolved-in-the-refresh-window"),
        ("action.permission.requested", "permission-resolved-in-the-refresh-window"),
        ("action.call.requested", "control-tool-roundtrip"),
    ]:
        trace = json.loads((ROOT / f"fixtures/valid/{fixture}.json").read_text())
        gate = next(e for e in trace if e["type"] == typ)
        for defect in ["missing", "scalar", "member"]:
            broken = copy.deepcopy(gate)
            if defect == "missing":
                broken.pop("payload")
            elif defect == "scalar":
                broken["payload"] = 42
            else:
                broken["payload"]["run_id"] = 42
            case = copy.deepcopy(base)
            case["events"] = [broken]
            case["hold"] = True
            cases.append((f"{typ}-{defect}-payload", case, "decode"))
    for name, case, expected in cases:
        path = output / f"{name}.json"
        path.write_text(json.dumps(case, ensure_ascii=False))
        actual = json.loads(run(rust + [str(path)], output, f"{name}-rust"))
        reference = json.loads(run(go + [str(path)], output, f"{name}-go"))
        if "stream_dropped" in actual:
            assert actual.pop("stream_dropped") == (not case.get("submit_error", False)), name
        assert actual == reference, (name, actual, reference)
        assert actual["kind"] == expected, (name, actual)
        if name == "duplicate-call-start":
            assert actual["tool_calls"] == 1, actual
        print(f"AGREE: {name}: {expected}", flush=True)
    cancelled = copy.deepcopy(cases[2][1])
    cancelled["events"][-1]["payload"]["usage"] = {"output_tokens": 0}
    path = output / "cancelled-usage.json"
    path.write_text(json.dumps(cancelled))
    actual = json.loads(run(rust + [str(path)], output, "cancelled-usage-rust"))
    reference = json.loads(run(go + [str(path)], output, "cancelled-usage-go"))
    assert actual["kind"] == reference["kind"] == "cancelled"
    assert actual["usage"] == {"output_tokens": 0} and reference["usage"] is None
    print("DECLARED DIFFERENCE: Rust preserves cancelled usage; Go helper currently omits it", flush=True)
    pending = output / "drop-pending.json"
    pending.write_text(json.dumps({"events": [], "hold": True, "drop_pending": True}))
    assert json.loads(run(rust + [str(pending)], output, "drop-pending-rust")) == {"kind": "dropped_pending", "stream_dropped": True}
    memory = json.loads(run(rust + ["--memory"], output, "memory-rust"))
    assert memory == {"kind": "gate_unanswered", "run_present": True, "cancel_accepted": True, "closed": True}
    print(f"PASS: {len(cases)} Rust/Go cases agree; cancelled usage retained, pending stream dropped, memory run explicitly cancelled and session closed", flush=True)


if __name__ == "__main__":
    main()
