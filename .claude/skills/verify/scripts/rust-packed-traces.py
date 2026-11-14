#!/usr/bin/env python3
"""Replay native strict packed traces through external Rust and Go consumers."""
import argparse
from copy import deepcopy
import json
from pathlib import Path
import re
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = '''use std::error::Error;
use std::path::PathBuf;
use std::process::ExitCode;
use oxap_validation::{ProviderValidator, ValidationResult, Validator};
type Judge = Box<dyn Fn(&[u8], &str) -> ValidationResult>;
fn main() -> Result<ExitCode, Box<dyn Error>> {
    let mut arguments = std::env::args_os().skip(1);
    let mut paths = Vec::new();
    let mut packs = Vec::new();
    let mut provider = false;
    let mut tolerant = false;
    while let Some(argument) = arguments.next() {
        match argument.to_str() {
            Some("--provider") => provider = true,
            Some("--tolerant") => tolerant = true,
            Some("--pack") => packs.push(PathBuf::from(arguments.next().ok_or("missing pack path")?)),
            _ => paths.push(PathBuf::from(argument)),
        }
    }
    let judge: Judge = if provider {
        let validator = ProviderValidator::new()?;
        Box::new(move |data, fixture| validator.validate(data, fixture))
    } else if packs.is_empty() {
        let validator = if tolerant { Validator::tolerant()? } else { Validator::new()? };
        Box::new(move |data, fixture| validator.validate(data, fixture))
    } else {
        let loaded = if tolerant { Validator::tolerant_with_packs(&packs) } else { Validator::with_packs(&packs) };
        let validator = match loaded {
            Ok(validator) => validator,
            Err(error) => {
                println!("{}", serde_json::json!({"load_codes":error.codes(), "refusals":error.refusals}));
                return Ok(ExitCode::FAILURE);
            }
        };
        Box::new(move |data, fixture| validator.validate(data, fixture))
    };
    let mut valid = true;
    for path in paths {
        let result = judge(&std::fs::read(&path)?, &path.to_string_lossy());
        valid &= result.valid();
        println!("{}", serde_json::to_string(&result)?);
    }
    Ok(if valid { ExitCode::SUCCESS } else { ExitCode::FAILURE })
}
'''


def build_native(output):
    project = output / "native-consumer"
    (project / "src").mkdir(parents=True)
    (project / "Cargo.toml").write_text(
        '[package]\nname="native-packed-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noxap-validation={{path={json.dumps(str(ROOT / "rust/validation"))}}}\nserde_json="1"\n')
    (project / "src/main.rs").write_text(SOURCE)
    result = subprocess.run(["cargo", "build", "--offline", "--manifest-path", str(project / "Cargo.toml"),
                             "--target-dir", str(ROOT / "rust/target")], cwd=ROOT, capture_output=True,
                            text=True, timeout=900)
    (output / "native-build.stdout").write_text(result.stdout)
    (output / "native-build.stderr").write_text(result.stderr)
    assert result.returncode == 0, result.stderr
    return ROOT / "rust/target/debug/native-packed-consumer"


def run(command, output, label):
    result = subprocess.run(list(map(str, command)), cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / (label + ".stdout")).write_text(result.stdout)
    (output / (label + ".stderr")).write_text(result.stderr)
    return result


def load_refusal(goap, rust, fixture, output, tolerant=False):
    directories = [ROOT / "fixtures" / p for p in [fixture["path"], *fixture.get("packs", [])]]
    flags = [argument for directory in directories for argument in ["--pack", directory]]
    core = ROOT / "fixtures/valid/core-completed.json"
    go = run([goap, "validate", "--format", "json", "--mode", "tolerant" if tolerant else "strict", *flags, core], output, fixture["id"] + ".go")
    rs = run([rust, *(["--tolerant"] if tolerant else []), *flags, core], output, fixture["id"] + ".rust")
    go_codes = sorted(set(re.findall(r"\b(?:pack_[a-z_]+|ext_claim_without_pack)\b", go.stderr)))
    rust_codes = json.loads(rs.stdout)["load_codes"]
    assert go.returncode == rs.returncode == 1, (fixture["id"], go.stderr, rs.stdout)
    assert go_codes == rust_codes == sorted(set(fixture["codes"])), (fixture["id"], go.stderr, rs.stdout)
    return rust_codes


def shape(diagnostics):
    return sorted((d["phase"], d["code"], d.get("index", 0)) for d in diagnostics or [])


def compare(goap, rust, trace, directories, expected, output, label):
    flags = [argument for directory in directories for argument in ["--pack", directory]]
    go = run([goap, "validate", "--format", "json", *flags, trace], output, label + ".go")
    rs = run([rust, *flags, trace], output, label + ".rust")
    go_report = json.loads(go.stdout)[0]
    rust_report = json.loads(rs.stdout)
    assert "load_codes" not in rust_report, (label, rust_report)
    observed = shape(rust_report.get("diagnostics"))
    assert observed == shape(go_report.get("diagnostics")), (label, rust_report, go_report)
    assert sorted({code for _, code, _ in observed}) == sorted(expected), (label, rust_report, expected)
    assert go.returncode == rs.returncode == int(bool(observed)), (label, go.stderr, rs.stderr)
    assert go_report["valid"] == (not observed), (label, go_report)
    return {"id":label, "valid":not observed, "diagnostics":observed}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--zig", type=Path, help="Rebuilt oapx binary for the shared reply-role controls")
    arguments = parser.parse_args()
    output = Path(arguments.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    rust = build_native(output)
    goap = output / "goap"
    result = run(["go", "build", "-o", goap, "./go/cmd/goap"], output, "go-build")
    assert result.returncode == 0, result.stderr
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    results = []
    for fixture in manifest:
        if fixture["kind"] == "load-invalid":
            results.append({"id":fixture["id"], "load_codes":load_refusal(goap, rust, fixture, output)})
        elif fixture.get("packs") and fixture.get("mode", "strict") == "strict":
            results.append(compare(goap, rust, ROOT / "fixtures" / fixture["path"],
                                   [ROOT / "fixtures" / p for p in fixture["packs"]],
                                   fixture.get("codes", []), output, fixture["id"]))
    descriptor = ROOT / "fixtures/packs/descriptor-member"
    results.append(compare(goap, rust, descriptor / "fixtures/region-on-stale-descriptor.json",
                           [descriptor], ["stale_capability_revision", "unavailable_capability"],
                           output, "stale-descriptor-member-gate"))
    storage = ROOT / "fixtures/packs/storage"
    base = json.loads((ROOT / "fixtures/valid/ext-advertised-key-gated.json").read_text())
    mutations = []
    for alias in ["session.message.", "agent_control.", "action."]:
        trace = deepcopy(base[:4])
        trace[1]["payload"]["features"] = {alias + "com.example.storage.objects":{"level":"native"}}
        mutations.append(("exact-key-" + alias.rstrip("."), trace, ["unavailable_capability"]))
    trace = deepcopy(base[:4])
    trace[2]["payload"]["session_id"] = "other"
    mutations.append(("request-session-conflict", trace, ["scope_mismatch"]))
    trace = deepcopy(base[:4])
    trace[2]["run_id"] = "original"
    trace[3]["run_id"] = "other"
    mutations.append(("response-run-conflict", trace, ["scope_mismatch"]))
    trace = deepcopy(base[:4])
    trace.append(dict(trace[3], id="duplicate-response"))
    mutations.append(("duplicate-packed-response", trace, ["duplicate_response"]))
    mutations.append(("missing-packed-response", deepcopy(base[:3]), ["missing_response"]))
    for advertised, later in [(False, True), (True, False)]:
        trace = deepcopy([base[i] for i in [0, 1, 2, 4, 5, 6, 8]])
        trace[1]["payload"]["features"] = {"com.example.storage.objects":{"level":"native"}} if advertised else {}
        trace[5]["payload"]["features"] = {"com.example.storage.objects":{"level":"native"}} if later else {}
        trace[6]["in_reply_to"] = "read1"
        trace[6]["payload"]["error"]["code"] = "com.example.storage.object_not_found"
        mutations.append(("captured-gate-" + str(advertised).lower(), trace, [] if advertised else ["unavailable_capability"]))
    header = {"protocol":"open-agent-protocol", "version":"0.1", "profile":"open-agent-protocol.agent-control-core"}
    def envelope(kind, identity, payload, **fields):
        return dict(header, type=kind, id=identity, payload=payload, **fields)
    run_trace = deepcopy(base[:2]) + [
        envelope("session.message.submit.request", "submit", {"delivery":"auto", "session_id":"s1", "messages":[{"role":"user", "content":"go"}]}, session_id="s1"),
        envelope("session.message.submit.response", "admission", {"accepted":True, "admission":"started", "effective_delivery":"start", "requested_delivery":"auto", "run_id":"r1", "session_id":"s1", "status":"running", "submission_id":"sub1"}, session_id="s1", in_reply_to="submit"),
        envelope("run.started", "start", {"run_id":"r1", "session_id":"s1", "status":"running"}, session_id="s1", run_id="r1", sequence=1),
        envelope("com.example.storage.objects.changed", "packed-event", {"session_id":"s1", "key":"object"}, session_id="s1", run_id="r1", sequence=2, capability_revision="v1"),
        envelope("run.completed", "terminal", {"final_response":{"role":"assistant", "content":"ok"}, "run_id":"r1", "session_id":"s1", "stop_reason":"end_turn"}, session_id="s1", run_id="r1", sequence=3),
    ]
    mutations.append(("packed-event-cursor", run_trace, []))
    trace = deepcopy(run_trace)
    trace[4], trace[5] = trace[5], trace[4]
    trace[4]["sequence"], trace[5]["sequence"] = 1, 2
    mutations.append(("packed-event-before-start", trace, []))
    for alias in ["session.message.", "agent_control.", "action."]:
        trace = deepcopy(run_trace)
        trace[1]["payload"]["features"] = {alias + "com.example.storage.objects":{"level":"native"}}
        mutations.append(("event-exact-key-" + alias.rstrip("."), trace, ["unavailable_capability"]))
    for label, sequence, terminal, code in [("gap", 3, 4, "sequence_gap"), ("repeated", 1, 2, "sequence_regression")]:
        trace = deepcopy(run_trace)
        trace[5]["sequence"] = sequence
        trace[6]["sequence"] = terminal
        mutations.append(("packed-event-" + label, trace, [code]))
    trace = deepcopy(run_trace)
    trace[5], trace[6] = trace[6], trace[5]
    trace[5]["sequence"], trace[6]["sequence"] = 2, 3
    mutations.append(("packed-event-after-terminal", trace, ["event_after_terminal"]))
    trace = deepcopy(run_trace[:5] + run_trace[6:])
    trace[-1]["sequence"] = 2
    trace[1]["payload"]["features"] = {}
    trace[2]["payload"]["com.example.storage.workspace"] = {"bucket":"reports"}
    mutations.append(("member-request-unadvertised", trace, ["unavailable_capability"]))
    trace = json.loads((ROOT / "fixtures/valid/core-completed.json").read_text())
    event = dict(deepcopy(trace[2]), type="com.example.replyroles.notice", id="mixed-scope",
                 sequence=2, payload={"session_id":42, "run_id":"r-other"})
    trace[-1]["sequence"] = 3
    trace.insert(3, event)
    path = output / "packed-scope-partial-decode.json"
    path.write_text(json.dumps(trace))
    results.append(compare(goap, rust, path, [ROOT / "fixtures/packs/reply-roles"],
                           ["scope_mismatch"], output, "packed-scope-partial-decode"))
    for label, trace, expected in mutations:
        path = output / (label + ".json")
        path.write_text(json.dumps(trace))
        results.append(compare(goap, rust, path, [storage], expected, output, label))
    roles = output / "roles"
    roles.mkdir()
    request, response, event = ["com.example.roles." + name for name in ["operation.response", "answer.request", "event.request"]]
    (roles / "types.json").write_text(json.dumps({"$defs":{name:{"properties":{"type":{"const":kind}}} for name, kind in [("request",request), ("response",response), ("event",event)]}}))
    (roles / "pack.json").write_text(json.dumps({
        "id":"com.example.roles", "version":"1", "schemas":["types.json"],
        "envelope_types":[{"type":request, "role":"request", "schema":"types.json#/$defs/request"}, {"type":response, "role":"response", "schema":"types.json#/$defs/response", "replies_to":request}, {"type":event, "role":"event", "schema":"types.json#/$defs/event"}],
        "gates":[{"type":request, "ungated":True}, {"type":event, "ungated":True}],
    }))
    image = {"nested":{"type":"tool_result", "result":[{"type":"image", "image":{"url":"https://example.invalid/image.png"}}]}}
    image_pack = output / "descriptor-image"
    shutil.copytree(ROOT / "fixtures/packs/descriptor-member", image_pack)
    descriptor = json.loads((image_pack / "pack.json").read_text())
    descriptor["payload_members"][0]["schema"] = {
        "anyOf":[descriptor["payload_members"][0]["schema"], {"type":"object"}],
    }
    (image_pack / "pack.json").write_text(json.dumps(descriptor))
    trace = json.loads((image_pack / "fixtures/region-on-stale-descriptor.json").read_text())
    trace[4]["payload"]["com.example.descriptor.region"] = image
    path = output / "stale-descriptor-member-image.json"
    path.write_text(json.dumps(trace))
    judgment = compare(goap, rust, path, [image_pack], ["stale_capability_revision", "unavailable_capability"],
                       output, "stale-descriptor-member-image")
    assert judgment["diagnostics"] == [
        ("semantic", "stale_capability_revision", 4),
        ("semantic", "stale_capability_revision", 5),
        ("semantic", "unavailable_capability", 4),
        ("semantic", "unavailable_capability", 4),
    ], judgment
    results.append(judgment)
    for label, trace, expected in [
        ("declared-roles-override-suffixes", [envelope(request,"request",{}), envelope(response,"response",{},in_reply_to="request")], []),
        ("packed-request-image", [envelope(request,"request",image), envelope(response,"response",{},in_reply_to="request")], []),
        ("packed-event-image", [envelope(event,"event",image)], ["unavailable_capability"]),
    ]:
        path = output / (label + ".json")
        path.write_text(json.dumps(trace))
        results.append(compare(goap, rust, path, [roles], expected, output, label))
    misleading = output / "event-reply-field"
    misleading.mkdir()
    descriptor = json.loads((roles / "pack.json").read_text())
    event_entry = descriptor["envelope_types"].pop()
    event_entry["replies_to"] = request
    descriptor["envelope_types"].insert(0, event_entry)
    (misleading / "pack.json").write_text(json.dumps(descriptor))
    (misleading / "types.json").write_text((roles / "types.json").read_text())
    path = output / "nonresponse-replies-to-is-ignored.json"
    path.write_text(json.dumps([envelope(request,"request",{}), envelope(response,"response",{},in_reply_to="request")]))
    results.append(compare(goap, rust, path, [misleading], [], output, "nonresponse-replies-to-is-ignored"))
    core_target = output / "core-reply-field"
    core_target.mkdir()
    descriptor = json.loads((misleading / "pack.json").read_text())
    descriptor["envelope_types"][0]["replies_to"] = "capabilities.request"
    (core_target / "pack.json").write_text(json.dumps(descriptor))
    (core_target / "types.json").write_text((roles / "types.json").read_text())
    results.append(compare(goap, rust, ROOT / "fixtures/valid/core-completed.json", [core_target], [], output, "core-reply-target-is-preserved"))
    no_result = output / "no-result"
    no_result.mkdir()
    request_a = "com.example.noresult.read"
    request_b = "com.example.noresult.other"
    reply = request_a + ".response"
    types = [("read", request_a, "request"), ("other", request_b, "request"), ("response", reply, "response")]
    (no_result / "types.json").write_text(json.dumps({"$defs":{name:{"properties":{"type":{"const":kind}}} for name, kind, _ in types}}))
    declared = [{"type":kind, "role":role, "schema":"types.json#/$defs/" + name} for name, kind, role in types]
    declared[2]["replies_to"] = request_b
    (no_result / "pack.json").write_text(json.dumps({
        "id":"com.example.noresult", "version":"1", "schemas":["types.json"], "envelope_types":declared,
        "gates":[{"type":request_a, "ungated":True}, {"type":request_b, "ungated":True}],
    }))
    for label, response_envelope, expected in [
        ("request-without-success-refusal", envelope("error.response", "refusal", {"error":{"code":"unsupported_feature", "message":"no result"}}, in_reply_to="read"), []),
        ("undeclared-success-reply", envelope(reply, "reply", {}, in_reply_to="read"), ["unmatched_response", "missing_response"]),
    ]:
        path = output / (label + ".json")
        path.write_text(json.dumps([envelope(request_a, "read", {}), response_envelope]))
        results.append(compare(goap, rust, path, [no_result], expected, output, label))
    if arguments.zig:
        zig_results = []
        for fixture in manifest:
            if "packs/reply-roles" not in fixture.get("packs", []):
                continue
            label = fixture["id"]
            result = run([arguments.zig.resolve(), "validate", "--pack", ROOT / "fixtures/packs/reply-roles",
                          ROOT / "fixtures" / fixture["path"]], output, label + ".zig")
            codes = sorted(set(re.findall(r"semantic ([a-z_]+) at", result.stdout)))
            expected = sorted(fixture.get("codes", []))
            assert result.returncode == int(not fixture["valid"]), (label, result.stdout, result.stderr)
            assert codes == expected, (label, codes, expected)
            assert result.stdout.startswith("PASS " if fixture["valid"] else "FAIL "), (label, result.stdout)
            zig_results.append({"id":label, "exit":result.returncode, "codes":codes})
        assert len(zig_results) == 3, zig_results
        (output / "zig-results.json").write_text(json.dumps(zig_results, indent=2) + "\n")
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    print(f"PASS: {len(results)} native strict packed trace/load cases agree on verdict, phase, code and index")


if __name__ == "__main__":
    main()
