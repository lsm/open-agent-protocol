#!/usr/bin/env python3
"""Replay optional query outcomes through public Rust and Go consumers."""
import argparse
from copy import deepcopy
import json
from pathlib import Path
import runpy

ROOT = Path(__file__).resolve().parents[4]
NATIVE = runpy.run_path(str(Path(__file__).with_name("rust-packed-traces.py")))
GAPS = {
    "models-list-refused-advertised": "models.list",
    "models-list-degraded-without-optin": "models.list",
    "models-unadvertised-wrong-reason": "models.list",
    "session-list-unadvertised-wrong-refusal": "session.list",
    "session-list-degraded-admitted-without-optin": "session.list",
    "transcript-load-degraded-admitted-without-optin": "transcript.load",
}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    rust = NATIVE["build_native"](output)
    goap = output / "goap"
    built = NATIVE["run"](["go", "build", "-o", goap, "./go/cmd/goap"], output, "go-build")
    assert built.returncode == 0, built.stderr
    manifest = json.loads((ROOT / "fixtures/manifest.json").read_text())["fixtures"]
    results, disagreements = [], []

    def judge(label, trace, codes, modes=("strict", "tolerant")):
        path = output / (label + ".json")
        path.write_text(json.dumps(trace))
        for mode in modes:
            go = NATIVE["run"]([goap, "validate", "--format", "json", "--mode", mode, path], output, label + "." + mode + ".go")
            rs = NATIVE["run"]([rust, *(["--tolerant"] if mode == "tolerant" else []), path], output, label + "." + mode + ".rust")
            g, r = json.loads(go.stdout)[0], json.loads(rs.stdout)
            go_shape, rust_shape = NATIVE["shape"](g.get("diagnostics")), NATIVE["shape"](r.get("diagnostics"))
            assert sorted({d[1] for d in go_shape}) == sorted(codes), (label, mode, g)
            assert go.returncode == int(bool(codes)), (label, mode, go.stderr)
            item = {"id": label, "mode": mode, "go": go_shape, "rust": rust_shape}
            results.append(item)
            if go_shape != rust_shape or go.returncode != rs.returncode:
                disagreements.append(item)

    for entry in manifest:
        label = entry["id"]
        if label not in GAPS:
            continue
        key = GAPS[label]
        trace = json.loads((ROOT / "fixtures" / entry["path"]).read_text())
        judge(label, trace, entry["codes"])
        repaired = deepcopy(trace)
        if "degraded" in label:
            repaired[-2]["payload"]["allow_degraded_features"] = [key]
        elif label == "models-list-refused-advertised":
            repaired[-1]["type"] = "models.response"
            repaired[-1]["capability_revision"] = repaired[1]["capability_revision"]
            repaired[-1]["payload"] = {"models": []}
            if "session_id" in repaired[-2]["payload"]:
                repaired[-1]["payload"]["session_id"] = repaired[-2]["payload"]["session_id"]
        else:
            repaired[-1]["payload"]["error"] = {"code": "unsupported_feature", "message": "unavailable", "details": {"feature": key, "reason": "unadvertised"}}
        judge(label + "-repaired", repaired, [])
        if "degraded" in label:
            wrong_opt_in = deepcopy(repaired)
            wrong_opt_in[-2]["payload"]["allow_degraded_features"] = ["com.example.other"]
            judge(label + "-other-opt-in", wrong_opt_in, entry["codes"])
            first, second = deepcopy(trace[-2]), deepcopy(repaired[-2])
            first["id"], second["id"] = "query-bad", "query-good"
            bad, good = deepcopy(trace[-1]), deepcopy(repaired[-1])
            bad.update(id="reply-bad", in_reply_to="query-bad")
            good.update(id="reply-good", in_reply_to="query-good")
            judge(label + "-reversed-replies", [*trace[:-2], first, second, good, bad], entry["codes"])
            refused = deepcopy(repaired)
            refused[-1]["type"] = "error.response"
            refused[-1]["payload"] = {"error": {"code": "internal_error", "message": "read failed"}}
            judge(label + "-opted-in-refusal", refused, ["unhonoured_capability"] if key == "models.list" else [])
            if key == "models.list":
                wrong_kind = deepcopy(trace)
                wrong_kind[1]["payload"]["features"]["session.list"] = {"level": "native"}
                rogue = deepcopy(wrong_kind[-1])
                rogue.update(type="session.list.response", id="wrong-kind", payload={"sessions": []})
                rogue.pop("session_id", None)
                judge(label + "-wrong-response-kind", [*wrong_kind[:-1], rogue, wrong_kind[-1]], ["unmatched_response", "degraded_without_optin"])
        if "wrong" in label:
            future = deepcopy(repaired)
            future[1]["payload"]["features"][key] = {"level": "partial"}
            judge(label + "-future-level-refused", future, [], ("tolerant",))
            future[-1]["payload"]["error"]["details"]["reason"] = "unsatisfiable"
            judge(label + "-future-level-wrong-refusal", future, entry["codes"], ("tolerant",))
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    (output / "disagreements.json").write_text(json.dumps(disagreements, indent=2) + "\n")
    print(f"{len(results)} public query judgments, {len(disagreements)} disagreements")
    assert not disagreements, disagreements


if __name__ == "__main__":
    main()
