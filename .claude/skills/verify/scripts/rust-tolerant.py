#!/usr/bin/env python3
"""Observe tolerant and strict validation through separate public Rust and Go consumers."""
import argparse
import collections
from copy import deepcopy
import json
from pathlib import Path
import runpy
import shutil

ROOT = Path(__file__).resolve().parents[4]
NATIVE = runpy.run_path(str(Path(__file__).with_name("rust-packed-traces.py")))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    rust = NATIVE["build_native"](output)
    goap = output / "goap"
    result = NATIVE["run"](["go", "build", "-o", goap, "./go/cmd/goap"], output, "go-build")
    assert result.returncode == 0, result.stderr
    results = []
    failures = []

    def judge(label, trace, directories, strict, tolerant):
        path = output / (label + ".json")
        path.write_text(json.dumps(trace))
        flags = [v for directory in directories for v in ["--pack", directory]]
        for mode, valid in [("strict", strict), ("tolerant", tolerant)]:
            go = NATIVE["run"]([goap, "validate", "--format", "json", "--mode", mode, *flags, path], output, label + "." + mode + ".go")
            rs = NATIVE["run"]([rust, *(["--tolerant"] if mode == "tolerant" else []), *flags, path], output, label + "." + mode + ".rust")
            go_report, rust_report = json.loads(go.stdout)[0], json.loads(rs.stdout)
            pointers = {"ext-packed-type-malformed":"/payload", "started-const":"/payload/status"}
            if label in pointers:
                diagnostic = rust_report["diagnostics"][0]
                assert diagnostic.get("pointer") == pointers[label], (mode, diagnostic)
            go_shape = NATIVE["shape"](go_report.get("diagnostics"))
            rust_shape = NATIVE["shape"](rust_report.get("diagnostics"))
            assert go_report["valid"] == valid and go.returncode == int(not valid), (label, mode, go_report)
            item = {"id":label, "mode":mode, "valid":valid, "go":go_shape, "rust":rust_shape}
            results.append(item)
            if go_shape != rust_shape or rs.returncode != go.returncode:
                failures.append(item)

    base = json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())
    for key, value in [("admission", "parked"), ("effective_delivery", "later"), ("status", "paused")]:
        trace = deepcopy(base)
        trace[1]["payload"][key] = value
        judge("foreign-" + key, trace, [], False, True)
    trace = deepcopy(base)
    trace[0]["payload"]["delivery"] = trace[1]["payload"]["requested_delivery"] = "later"
    judge("foreign-requested-delivery", trace, [], False, True)
    for label, node, key, value in [
        ("additive-member", 2, "com.example.note", "future"),
        ("started-const", 2, "status", "failed"),
        ("accepted-type", 1, "accepted", "true"),
    ]:
        trace = deepcopy(base)
        trace[node]["payload"][key] = value
        judge(label, trace, [], False, label == "additive-member")
    trace = deepcopy(base)
    trace[2]["payload"].pop("status")
    judge("missing-status", trace, [], False, False)
    trace = deepcopy(base)
    trace[1]["payload"]["effective_delivery"], trace[1]["payload"]["status"] = "later", "queued"
    judge("known-admission-contradiction", trace, [], False, False)
    for statuses in [("paused",), ("paused", "queued"), ("paused", "queued", "waiting_for_input")]:
        trace = deepcopy(base)
        updates = []
        for index, status in enumerate(statuses, 2):
            update = deepcopy(base[2])
            update.update(type="run.status.updated", id="status-" + str(index), sequence=index)
            update["payload"]["status"] = status
            updates.append(update)
        trace[-1]["sequence"] = len(updates) + 2
        trace[3:3] = updates
        judge("statuses-" + "-".join(statuses), trace, [], False, len(statuses) < 3)
    trace = deepcopy(base[:2])
    trace[1]["payload"]["admission"] = "merged"
    judge("opaque-admission-still-owes-terminal", trace, [], False, False)
    for status, valid in [("waiting_for_input", True), ("queued", False)]:
        trace = deepcopy(base)
        trace[1]["payload"]["admission"] = "merged"
        update = dict(deepcopy(base[2]), type="run.status.updated", id="before-start", sequence=1)
        update["payload"]["status"] = status
        trace[2]["sequence"], trace[3]["sequence"] = 2, 3
        trace.insert(2, update)
        judge("opaque-prestart-" + status, trace, [], False, valid)
    for session, valid in [("s1", True), ("s-other", False)]:
        trace = deepcopy(base)
        request = dict(deepcopy(base[0]), id="submit-again", session_id=session)
        request["payload"]["session_id"] = session
        response = dict(deepcopy(base[1]), id="response-again", in_reply_to="submit-again", session_id=session)
        response["payload"].update(session_id=session, admission="merged", effective_delivery="merge", submission_id="sub-again")
        trace[3:3] = [request, response]
        judge("opaque-tracked-run-" + session, trace, [], False, valid)
    for status, valid in [("draining", True), ("cancelling", False)]:
        trace = json.loads((ROOT / "fixtures/valid/core-cancelled.json").read_text())
        next(e for e in trace if e["type"] == "run.cancel.response")["payload"]["status"] = status
        next(e for e in trace if e["type"] == "run.status.updated")["payload"]["status"] = "running"
        judge("cancel-status-" + status, trace, [], False, valid)
    for position in [2, 3, 4]:
        trace = deepcopy(base)
        unknown = deepcopy(base[2])
        unknown.update(type="com.example.future.event", id="future", payload={})
        trace.insert(position, unknown)
        for index, envelope in enumerate(trace[2:], 1):
            envelope["sequence"] = index
        judge("unknown-event-position-" + str(position), trace, [], False, position != 4)
    trace = deepcopy(base)
    event = dict(deepcopy(base[2]), type="com.example.future.event", id="mixed-scope", sequence=2,
                 payload={"session_id":42, "run_id":"r-other"})
    trace[-1]["sequence"] = 3
    trace.insert(3, event)
    judge("unknown-scope-partial-decode", trace, [], False, False)
    trace[3]["type"] = "com.example.replyroles.notice"
    judge("declared-scope-partial-decode", trace, [ROOT / "fixtures/packs/reply-roles"], False, False)
    assert all(r["go"] == [("semantic", "scope_mismatch", 3)] for r in results[-2:]), results[-2:]
    for conflict in [False, True]:
        request = dict(deepcopy(base[0]), type="com.example.pause.request", payload={}, run_id="rA")
        response = dict(deepcopy(base[1]), type="com.example.pause.response", payload={}, run_id="rB" if conflict else "rA")
        judge("unknown-pair-" + str(conflict).lower(), [request, response], [], False, not conflict)
    for label, request, correlated, sequenced in [
        ("event", False, False, True), ("sequenced-request", True, False, True),
        ("response", True, True, False), ("orphan-response", False, True, False),
    ]:
        trace = deepcopy(base)
        additions = []
        if request:
            envelope = dict(deepcopy(base[2]), type="com.example.pause.request", id="pause-request",
                            payload={"session_id":"s1", "run_id":"rB" if sequenced else "r1"})
            if not sequenced:
                envelope.pop("sequence")
            additions.append(envelope)
        if correlated:
            envelope = dict(deepcopy(base[2]), type="com.example.pause.response", id="pause-response",
                            in_reply_to="pause-request", run_id="rB", payload={"session_id":"s1", "run_id":"r1"})
            envelope.pop("sequence")
            additions.append(envelope)
        if not request and not correlated:
            additions.append(dict(deepcopy(base[2]), type="com.example.pause.event", id="pause-event",
                                  payload={"session_id":"s1", "run_id":"rB"}))
        trace[3:3] = additions
        if sequenced:
            trace[3]["sequence"], trace[-1]["sequence"] = 2, 3
        judge("unknown-scope-" + label, trace, [], False, False)
    for kind in ["com.example.future", "action.call.future", "action.permission.future", "user.input.future"]:
        event = dict(deepcopy(base[0]), type=kind, id="future", payload={})
        judge("unknown-root-" + kind, [event], [], False, True)
    for key, value in [("protocol", "future-protocol"), ("version", "0.2"), ("profile", "future-profile"), ("payload", [])]:
        event = dict(deepcopy(base[0]), type="com.example.future", payload={})
        event[key] = value
        judge("unknown-common-" + key, [event], [], False, False)
    for content, valid in [
        ([{"type":"audio", "audio":{"url":"https://example.invalid/a.wav"}}], True),
        ([{"type":"text", "text":"hello", "annotations":[]}], True),
        ([{"type":"text"}], False),
    ]:
        trace = deepcopy(base)
        trace[0]["payload"]["messages"][0]["content"] = content
        judge("content-" + str(len(results)), trace, [], False, valid)
    for kind, valid in [("date", True), ("single_choice", False)]:
        trace = json.loads((ROOT / "fixtures/valid/user-input-completed.json").read_text())
        event = next(e for e in trace if e["type"] == "user.input.requested")
        event["payload"]["questions"][0]["kind"] = kind
        event["payload"]["questions"][0].pop("options", None)
        judge("question-" + kind, trace, [], False, valid)
    trace = json.loads((ROOT / "fixtures/valid/tools-permission-completed.json").read_text())
    next(e for e in trace if e["type"] == "capabilities.response")["payload"]["features"]["tools"]["level"] = "partial"
    judge("unknown-support-is-unavailable", trace, [], False, False)

    task_payloads = {
        "started":{"task":{"task_id":"task", "kind":"command", "status":"running", "cancellable":True}},
        "updated":{"task_id":"task", "status":"running"},
        "ended":{"task_id":"task", "status":"completed", "cause":"finished"},
    }
    for suffix, task_payload in task_payloads.items():
        event = dict(deepcopy(base[2]), type="session.task." + suffix, id="task-event", payload=task_payload)
        judge("task-unadmitted-" + suffix, [event], [], True, False)
        trace = deepcopy(base)
        trace[2]["sequence"], trace[3]["sequence"] = 2, 3
        trace.insert(2, event)
        judge("task-prestart-" + suffix, trace, [], False, True)
        event.pop("run_id")
        judge("task-session-scoped-" + suffix, [event], [], True, True)
    for operation, response_payload in [
        ("cancel", {"outcome":"requested"}),
        ("output", {"output":"", "next":0, "truncated":False, "ended":False}),
    ]:
        request = dict(deepcopy(base[0]), type="session.task." + operation + ".request", id="task-request",
                       payload={"session_id":"s-other", "task_id":"task"})
        response = dict(deepcopy(base[1]), type="session.task." + operation + ".response", id="task-response",
                        in_reply_to="task-request", payload=response_payload)
        request.pop("run_id", None)
        response.pop("run_id", None)
        judge("task-request-scope-" + operation, [request, response], [], True, False)
        request["payload"]["session_id"] = "s1"
        response["payload"]["session_id"] = "s-other"
        judge("task-response-scope-" + operation, [request, response], [], False, False)

    storage = ROOT / "fixtures/packs/storage"
    for path, valid in [("valid/ext-advertised-key-gated.json", True), ("schema-invalid/ext-packed-type-malformed.json", False)]:
        judge(Path(path).stem, json.loads((ROOT / "fixtures" / path).read_text()), [storage], valid, valid)
    schemas = {
        "union":{"oneOf":[
            {"type":"object", "required":["kind", "text"], "properties":{"kind":{"const":"text"}, "text":{"type":"string"}, "tag":{"enum":["a", "b"]}, "n":{"enum":[1, 2]}}, "additionalProperties":False},
            {"type":"object", "required":["kind", "url"], "properties":{"kind":{"const":"image"}, "url":{"type":"string"}}, "additionalProperties":False},
        ]},
        "conditional":{"type":"object", "required":["kind"], "properties":{"kind":{"enum":["choice", "text"]}, "options":{"type":"array"}},
                       "additionalProperties":False, "if":{"required":["kind"], "properties":{"kind":{"enum":["choice"]}}}, "then":{"required":["options"]}},
    }
    values = {
        "union":[({"kind":"text", "text":"ok"}, True, True), ({"kind":"text"}, False, False), ({"kind":"future"}, False, True),
                 ({"kind":"text", "text":9}, False, False), ({"kind":"text", "text":"ok", "tag":"future"}, False, True),
                 ({"kind":"text", "text":"ok", "n":3}, False, False)],
        "conditional":[({"kind":"choice"}, False, False), ({"kind":"text"}, True, True), ({"kind":"date"}, False, True)],
    }
    for label, schema in schemas.items():
        pack = output / (label + "-pack")
        shutil.copytree(ROOT / "fixtures/packs/reply-roles", pack)
        descriptor = json.loads((pack / "pack.json").read_text())
        member = "com.example.replyroles.value"
        descriptor["payload_members"] = [{"payload_type":"capabilities.response", "member":member, "schema":schema}]
        descriptor["gates"].append({"payload_type":"capabilities.response", "member":member, "ungated":True})
        (pack / "pack.json").write_text(json.dumps(descriptor))
        for index, (value, strict, tolerant) in enumerate(values[label]):
            trace = json.loads((ROOT / "fixtures/valid/ext-descriptor-member-advertised.json").read_text())
            trace[1]["payload"] = {"endpoint":{"id":"agent"}, member:value}
            judge(label + "-" + str(index), trace, [pack], strict, tolerant)

    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    gaps = set(json.loads((ROOT / "fixtures/known-gaps.json").read_text())["rust"]) - {"ext-unpacked-type-tolerated"}
    groups = collections.defaultdict(list)
    for fixture in manifest:
        if fixture["kind"] == "load-invalid":
            codes = NATIVE["load_refusal"](goap, rust, fixture, output, tolerant=True)
            results.append({"id":fixture["id"], "mode":"tolerant", "load_codes":codes})
        elif fixture["kind"] == "semantic-invalid" and fixture.get("packs"):
            judge(fixture["id"], json.loads((ROOT / "fixtures" / fixture["path"]).read_text()),
                  [ROOT / "fixtures" / pack for pack in fixture["packs"]], False, False)
        elif fixture["valid"] and fixture.get("profile") != "model-provider-core" and fixture["id"] not in gaps:
            groups[tuple(fixture.get("packs", []))].append(fixture)
    for index, (packs, fixtures) in enumerate(sorted(groups.items())):
        flags = [v for pack in packs for v in ["--pack", ROOT / "fixtures" / pack]]
        paths = [ROOT / "fixtures" / f["path"] for f in fixtures]
        go = NATIVE["run"]([goap, "validate", "--format", "json", "--mode", "tolerant", *flags, *paths], output, f"positives-{index}.go")
        rs = NATIVE["run"]([rust, "--tolerant", *flags, *paths], output, f"positives-{index}.rust")
        go_reports = json.loads(go.stdout)
        rust_reports = [json.loads(line) for line in rs.stdout.splitlines()]
        assert len(go_reports) == len(rust_reports) == len(fixtures)
        assert go.returncode == 0 and all(r["valid"] for r in go_reports), go.stdout
        for fixture, report in zip(fixtures, rust_reports):
            item = {"id":fixture["id"], "mode":"tolerant", "valid":True, "rust":NATIVE["shape"](report.get("diagnostics"))}
            results.append(item)
            if report.get("diagnostics"):
                failures.append(item)
        assert rs.returncode == int(any(r.get("diagnostics") for r in rust_reports)), rs.stderr
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    (output / "disagreements.json").write_text(json.dumps(failures, indent=2) + "\n")
    print(f"{len(results)} public strict/tolerant judgments, {len(failures)} disagreements", flush=True)
    for failure in failures:
        print(json.dumps(failure), flush=True)
    assert not failures


if __name__ == "__main__":
    main()
