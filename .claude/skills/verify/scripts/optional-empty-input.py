#!/usr/bin/env python3
"""Judge Decision 0056 optional empty text on the three public validator CLIs."""
import argparse
import copy
import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[4]
spec = importlib.util.spec_from_file_location("validator_builds", Path(__file__).with_name("rust-tool-catalog.py"))
common = importlib.util.module_from_spec(spec)
spec.loader.exec_module(common)


def load(kind, name):
    return json.loads((ROOT / "fixtures" / kind / (name + ".json")).read_text())


def expected(indices=(), phase="semantic"):
    code = "schema_invalid" if phase == "schema" else "unmatched_interaction"
    return {"exit": int(bool(indices)), "diagnostics": [[phase, code, i] for i in indices]}


def cases():
    optional = load("valid", "user-input-optional-empty-text")
    required = load("semantic-invalid", "user-input-required-empty-text")
    missing = load("semantic-invalid", "user-input-required-answer-missing")
    result = {
        "optional-empty": (optional, expected()),
        "required-empty": (required, expected([6, 8])),
        "required-missing": (missing, expected([6, 8])),
    }
    trace = copy.deepcopy(optional)
    trace[5]["payload"]["questions"][0].pop("required")
    result["optional-by-default"] = (trace, expected())
    for name, base in [("required-empty", required), ("required-missing", missing)]:
        for index, arm in [(6, "request"), (8, "event")]:
            trace = copy.deepcopy(base)
            other = 8 if index == 6 else 6
            trace[other]["payload"]["answers"] = [{"question_id": "q", "text": "repaired"}]
            result[name + "-" + arm] = (trace, expected([index]))
        trace = copy.deepcopy(base)
        for index in [6, 8]:
            trace[index]["payload"]["answers"] = [{"question_id": "q", "text": "repaired"}]
        result["repair-" + name] = (trace, expected())
    for text in [" ", "中文", "answer"]:
        trace = copy.deepcopy(required)
        for index in [6, 8]:
            trace[index]["payload"]["answers"][0]["text"] = text
        result["required-text-" + text] = (trace, expected())
    trace = copy.deepcopy(missing)
    trace[5]["payload"]["questions"][0]["required"] = False
    result["optional-unanswered"] = (trace, expected())
    trace = copy.deepcopy(optional)
    for index in [6, 8]:
        trace[index]["payload"]["answers"] = [{"question_id":"q", "selected_option_ids":["x"]}]
    result["optional-text-choice-arm"] = (trace, expected([6, 8]))
    for index in [6, 8]:
        trace = copy.deepcopy(optional)
        trace[index]["payload"]["answers"][0].pop("text")
        result["absent-answer-arm-" + str(index)] = (trace, expected([index], "schema"))
    for kind in ["single_choice", "multi_choice"]:
        trace = copy.deepcopy(optional)
        trace[5]["payload"]["questions"][0].update(kind=kind, options=[{"id":"x","label":"X"},{"id":"y","label":"Y"}])
        for index in [6, 8]:
            trace[index]["payload"]["answers"] = [{"question_id":"q", "selected_option_ids":["x"]}]
        result[kind + "-selected"] = (copy.deepcopy(trace), expected())
        for index in [6, 8]:
            trace[index]["payload"]["answers"] = [{"question_id":"q", "text":""}]
        result[kind + "-text-arm"] = (copy.deepcopy(trace), expected([6, 8]))
        for index in [6, 8]:
            trace[index]["payload"]["answers"] = [{"question_id":"q", "selected_option_ids":["foreign"]}]
        result[kind + "-foreign-option"] = (trace, expected([6, 8]))
    return result


def replay(language, output, name, case):
    trace, want = case
    path = output / (name + ".input.json")
    path.write_text(json.dumps(trace, ensure_ascii=False, indent=2) + "\n")
    command = {
        "go": [str(output / "goap"), "validate", "--format", "json"],
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate", "--format", "json"],
    }[language]
    result = common.run(command + [str(path)], output, name)
    data = json.loads(result.stdout)
    reports = data if isinstance(data, list) else data.get("reports", [data])
    diagnostics = [d for report in reports for d in report.get("diagnostics") or []]
    got = {"exit": result.returncode, "diagnostics": sorted([d["phase"], d["code"], d.get("index", 0)] for d in diagnostics)}
    return got, want


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for language in ["go", "rust", "zig"]:
        common.build(language, output, language + "-build")
    results, wrong = [], []
    for name, case in cases().items():
        for language in ["go", "rust", "zig"]:
            got, want = replay(language, output, language + "." + name, case)
            results.append({"case": name, "language": language, "got": got, "want": want})
            if got != want:
                wrong.append(language + "." + name + ": " + str(got) + " != " + str(want))
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    if wrong:
        raise RuntimeError("\n".join(wrong))
    print(f"PASS {len(cases())} traces x 3; zero disagreements; {output}")


if __name__ == "__main__":
    main()
