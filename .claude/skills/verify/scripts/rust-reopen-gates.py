#!/usr/bin/env python3
"""Compare reopen gates and compound refusal ownership through public consumers."""
import argparse
from copy import deepcopy
import json
from pathlib import Path
import runpy

ROOT = Path(__file__).resolve().parents[4]
NATIVE = runpy.run_path(str(Path(__file__).with_name("rust-packed-traces.py")))
REOPEN, SUBSCRIBE = "session.open.reopen", "session.open.subscribe"


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    rust = NATIVE["build_native"](output)
    goap = output / "goap"
    built = NATIVE["run"](["go", "build", "-o", goap, "./go/cmd/goap"], output, "go-build")
    assert built.returncode == 0, built.stderr
    results, disagreements = [], []

    def judge(label, trace, codes, owner=None, modes=("strict", "tolerant")):
        path = output / (label + ".json")
        path.write_text(json.dumps(trace))
        for mode in modes:
            go = NATIVE["run"]([goap, "validate", "--format", "json", "--mode", mode, path], output, label + "." + mode + ".go")
            rs = NATIVE["run"]([rust, *(["--tolerant"] if mode == "tolerant" else []), path], output, label + "." + mode + ".rust")
            g, r = json.loads(go.stdout)[0], json.loads(rs.stdout)
            gd, rd = g.get("diagnostics") or [], r.get("diagnostics") or []
            gs, rss = NATIVE["shape"](gd), NATIVE["shape"](rd)
            assert sorted({d[1] for d in gs}) == sorted(codes), (label, mode, g)
            assert go.returncode == int(bool(codes)), (label, mode, go.stderr)
            if owner:
                assert any(owner in d.get("expected", "") for d in gd), (label, gd)
            item = {"id": label, "mode": mode, "go": gs, "rust": rss, "owner": owner}
            results.append(item)
            if gs != rss or go.returncode != rs.returncode or (owner and not any(owner in d.get("expected", "") for d in rd)):
                disagreements.append(item)

    def refused(trace, code, feature=None, reason=None):
        trace = deepcopy(trace)
        error = {"code": code, "message": "synthetic refusal"}
        if feature:
            error["details"] = {"feature": feature}
        if reason:
            error["details"]["reason"] = reason
        trace[-1].update(type="error.response", payload={"error": error})
        return trace

    def fixture(name):
        return json.loads((ROOT / "fixtures/semantic-invalid" / (name + ".json")).read_text())

    base = fixture("session-reopen-unadvertised")
    degraded = fixture("session-reopen-degraded-admitted-without-optin")
    for name, code in [("session-reopen-unadvertised", "unavailable_capability"),
                       ("session-reopen-unadvertised-wrong-refusal", "unavailable_capability"),
                       ("session-reopen-degraded-admitted-without-optin", "degraded_without_optin")]:
        judge(name, fixture(name), [code])
    native = deepcopy(base)
    native[1]["payload"]["features"][REOPEN] = {"level": "native"}
    judge("native-reopen", native, [])
    opted = deepcopy(degraded)
    opted[-2]["payload"]["allow_degraded_features"] = [REOPEN]
    judge("degraded-opted-in", opted, [])
    other = deepcopy(degraded)
    other[-2]["payload"]["allow_degraded_features"] = [SUBSCRIBE]
    judge("degraded-other-opt-in", other, ["degraded_without_optin"])
    for label, trace, code in [("unadvertised", base, "unavailable_capability"), ("degraded", degraded, "degraded_without_optin")]:
        invalid = deepcopy(trace)
        invalid[-1]["payload"].pop("recovery")
        judge(label + "-gate-before-loading", invalid, [code])
    invalid = deepcopy(native)
    invalid[-1]["payload"].pop("recovery")
    judge("native-still-requires-recovery", invalid, ["session_state_mismatch"])
    judge("typed-unadvertised-refusal", refused(base, "unsupported_feature", REOPEN, "unadvertised"), [])
    judge("typed-degraded-refusal", refused(degraded, "capability_degraded", REOPEN), [])
    judge("wrong-degraded-feature", refused(degraded, "capability_degraded", SUBSCRIBE), ["degraded_without_optin"])
    judge("native-store-gone", refused(native, "unsupported_feature", REOPEN), [])
    judge("native-unknown-binding", refused(native, "unknown_session"), [])
    ordinary = deepcopy(base)
    ordinary[-2]["payload"]["reopen"] = False
    judge("ordinary-open-needs-no-reopen-key", ordinary, [])
    compound = deepcopy(base)
    compound[-2]["payload"]["subscribe"] = True
    judge("compound-admission", compound, ["unavailable_capability"])
    judge("compound-reopen-refusal", refused(compound, "unsupported_feature", REOPEN, "unadvertised"), [])
    judge("compound-subscribe-refusal", refused(compound, "unsupported_feature", SUBSCRIBE, "unadvertised"), [])
    judge("compound-unknown-session", refused(compound, "unknown_session"), [])
    judge("compound-wrong-refusal", refused(compound, "unsupported_feature", "com.example.other", "unadvertised"), ["unavailable_capability"], REOPEN)
    mixed = deepcopy(compound)
    mixed[1]["payload"]["features"][SUBSCRIBE] = {"level": "degraded", "reason": "lossy"}
    judge("compound-other-elected-defect", refused(mixed, "capability_degraded", SUBSCRIBE), [])
    missing = deepcopy(compound[2:])
    judge("compound-missing-descriptor", missing, ["unavailable_capability"])
    future = deepcopy(base)
    future[1]["payload"]["features"][REOPEN] = {"level": "partial"}
    judge("future-support-is-unavailable", future, ["unavailable_capability"], modes=("tolerant",))
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    (output / "disagreements.json").write_text(json.dumps(disagreements, indent=2) + "\n")
    print(f"{len(results)} public reopen judgments, {len(disagreements)} disagreements")
    assert not disagreements, disagreements


if __name__ == "__main__":
    main()
