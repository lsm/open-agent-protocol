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
        trace = copy.deepcopy(missing)
        trace[5]["payload"]["questions"][0].update(kind=kind, options=[{"id":"x","label":"X"}])
        result[kind + "-required-unanswered"] = (trace, expected([6, 8]))
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


def mutation_check(output, corpus):
    paths = {
        "go": ROOT / "go/validation/state.go",
        "rust": ROOT / "rust/validation/src/state/interactions.rs",
        "zig": ROOT / "zig/src/validation/semantic.zig",
    }
    changes = [
        ("go", "optional-empty", 'question.Required && answer.Text == ""', 'answer.Text == ""', "optional-empty"),
        ("go", "required-empty", 'question.Required && answer.Text == ""', 'false', "required-empty"),
        ("go", "text-arm", 'len(answer.SelectedOptionIDs) != 0 || ', '', "optional-text-choice-arm"),
        ("go", "required-present", 'if question.Required && !answered[question.ID] {', 'if false && question.Required && !answered[question.ID] {', "required-missing"),
        ("go", "diagnostic-location", 'CodeUnmatchedInteraction, i, line, e, "/payload/answers", "answer does not satisfy its text question"', 'CodeUnmatchedInteraction, i + 1, line, e, "/payload/answers", "answer does not satisfy its text question"', "required-empty"),
        ("rust", "optional-empty", 'question.required && answer.text.is_empty()', 'answer.text.is_empty()', "optional-empty"),
        ("rust", "required-empty", 'question.required && answer.text.is_empty()', 'false', "required-empty"),
        ("rust", "text-arm", '!answer.selected_option_ids.is_empty()', 'false', "optional-text-choice-arm"),
        ("rust", "required-present", '.filter(|q| q.required && !answered.contains(q.id.as_str()))', '.filter(|q| false && q.required && !answered.contains(q.id.as_str()))', "required-missing"),
        ("rust", "diagnostic-location", 'self.add_expected(code, at, e, pointer,', 'self.add_expected(code, (at.0 + 1, at.1), e, pointer,', "required-empty"),
        ("zig", "optional-empty", 'memberBool(question, "required") and memberString(answer, "text").len == 0', 'memberString(answer, "text").len == 0', "optional-empty"),
        ("zig", "required-empty", 'memberBool(question, "required") and memberString(answer, "text").len == 0', 'false', "required-empty"),
        ("zig", "text-arm", 'member(answer, "text") == null or ', '', "optional-text-choice-arm"),
        ("zig", "required-present", 'if (memberBool(question, "required") and covered.get(memberString(question, "id")) == null)', 'if (false and memberBool(question, "required") and covered.get(memberString(question, "id")) == null)', "required-missing"),
        ("zig", "diagnostic-location", 'memberString(answer, "text").len == 0)) try self.add(code_unmatched_interaction, index);', 'memberString(answer, "text").len == 0)) try self.add(code_unmatched_interaction, index + 1);', "required-empty"),
    ]
    originals = {language: path.read_bytes() for language, path in paths.items()}
    caught = []
    try:
        for language, name, before, after, target in changes:
            text = originals[language].decode()
            if text.count(before) != 1:
                raise RuntimeError(language + "." + name + ": source target not unique")
            paths[language].write_text(text.replace(before, after, 1))
            label = "mutation-" + language + "-" + name
            common.build(language, output, label + "-build")
            got, want = replay(language, output, label, corpus[target])
            if (got == want or got["exit"] not in [0, 1]
                    or any(d[0] != "semantic" or d[1] != "unmatched_interaction" for d in got["diagnostics"])):
                raise RuntimeError(label + ": survived or inconclusive: " + str(got))
            caught.append({"mutation": label, "target": target, "got": got, "want": want})
            paths[language].write_bytes(originals[language])
            print("CAUGHT " + label, flush=True)
    finally:
        for language, path in paths.items():
            path.write_bytes(originals[language])
        for language in paths:
            common.build(language, output, "restored-" + language + "-build")
    (output / "mutations.json").write_text(json.dumps(caught, indent=2) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    for language in ["go", "rust", "zig"]:
        common.build(language, output, language + "-build")
    corpus = cases()
    if args.mutation_check:
        mutation_check(output, corpus)
    results, wrong = [], []
    for name, case in corpus.items():
        for language in ["go", "rust", "zig"]:
            got, want = replay(language, output, language + "." + name, case)
            results.append({"case": name, "language": language, "got": got, "want": want})
            if got != want:
                wrong.append(language + "." + name + ": " + str(got) + " != " + str(want))
    (output / "results.json").write_text(json.dumps(results, indent=2) + "\n")
    if wrong:
        raise RuntimeError("\n".join(wrong))
    print(f"PASS {len(corpus)} traces x 3; zero disagreements; {output}")


if __name__ == "__main__":
    main()
