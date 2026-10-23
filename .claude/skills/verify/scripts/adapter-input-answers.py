#!/usr/bin/env python3
"""Exercise Decision 0056 through public helpers and emitted OAP wire."""

import argparse
import copy
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
CARGO = ["cargo", "build", "--manifest-path", "rust/Cargo.toml"]


def run(command, output, label, source=None, expected=0):
    result = subprocess.run(command, cwd=ROOT, input=source, capture_output=True,
                            text=True, timeout=600)
    (output / f"{label}.out").write_text(result.stdout)
    (output / f"{label}.err").write_text(result.stderr)
    assert result.returncode == expected, (label, result.returncode,
                                           result.stdout, result.stderr)
    return result


def question(name="q", kind="text", required=False):
    value = {"id": name, "kind": kind, "prompt": "Answer?", "required": required}
    if kind != "text":
        value["options"] = [{"id": "yes", "label": "Yes"},
                            {"id": "no", "label": "No"}]
    return value


def cases():
    text = question()
    required = question(required=True)
    empty = {"question_id": "q", "text": ""}
    filled = {"question_id": "q", "text": "é漢😀"}
    choice = {"question_id": "q", "selected_option_ids": ["yes"]}
    yield "optional-empty", [text], [empty], True, [True]
    yield "optional-filled", [text], [filled], True, [True]
    yield "optional-omitted", [text], [], True, []
    yield "required-empty", [required], [empty], False, [False]
    yield "required-filled", [required], [filled], True, [True]
    yield "required-missing", [required], [], False, []
    yield "no-questions", [], [], True, []
    yield "foreign", [text], [{"question_id": "other", "text": "x"}], False, [False]
    yield "duplicate", [text], [filled, filled], False, [True, True]
    yield "text-choices", [text], [choice], False, [False]
    yield "mixed", [text], [dict(choice, text="mixed")], False, [False]
    for kind in ["single_choice", "multi_choice"]:
        q = question(kind=kind)
        yield f"{kind}-valid", [q], [choice], True, [True]
        yield f"{kind}-blank", [q], [empty], False, [False]
        yield f"{kind}-foreign", [q], [dict(choice, selected_option_ids=["other"])], False, [False]
        yield f"{kind}-duplicate", [q], [dict(choice, selected_option_ids=["yes", "yes"])], False, [False]
    yield "single-choice-two", [question(kind="single_choice")], [dict(choice, selected_option_ids=["yes", "no"])], False, [False]
    yield "multi-choice-two", [question(kind="multi_choice")], [dict(choice, selected_option_ids=["yes", "no"])], True, [True]
    for kind in ["text", "single_choice", "multi_choice"]:
        questions = [question(kind=kind, required=True), question("note")]
        note = {"question_id": "note", "text": "note"}
        answer = filled if kind == "text" else choice
        yield f"{kind}-required-missing-beside-optional", questions, [note], False, [True]
        yield f"{kind}-required-repaired", questions, [answer], True, [True]
        yield f"{kind}-required-and-optional-empty", questions, [answer, {"question_id": "note", "text": ""}], True, [True, True]


def trace(questions, answers):
    value = json.loads((ROOT / "fixtures/valid/user-input-completed.json").read_text())
    for envelope in value:
        if envelope["type"] == "user.input.requested":
            envelope["payload"]["questions"] = questions
            envelope["payload"]["draft_answers"] = answers
        if envelope["type"] == "user.input.resolved":
            envelope["payload"]["answers"] = answers
    resolved = next(e for e in value if e["type"] == "user.input.resolved")
    request = copy.deepcopy(resolved)
    request.update(id="answer-request", type="user.input.resolve.request")
    request.pop("sequence")
    request["payload"].pop("status")
    response = copy.deepcopy(request)
    response.update(id="answer-response", type="user.input.resolve.response",
                    in_reply_to="answer-request")
    response["payload"] = {key: resolved["payload"][key]
                           for key in ["interaction_id", "session_id", "run_id"]}
    response["payload"]["accepted"] = True
    index = value.index(resolved)
    value[index:index] = [request, response]
    return value


def consumers(output):
    run(CARGO + ["-p", "oap-adapter", "--example", "input_answers"], output, "build-rust")
    go = output / "go-answers"
    run(["go", "build", "-o", str(go), "./rust/adapter/tests/testdata/input_answers_reference.go"], output, "build-go")
    return {"rust": [str(ROOT / "rust/target/debug/examples/input_answers")], "go": [str(go)]}


def optional_empty(questions, answers):
    return any(q["kind"] == "text" and not q.get("required", False)
               and a["question_id"] == q["id"] and a.get("text") == ""
               and not a.get("selected_option_ids")
               for q in questions for a in answers)


def verify(output):
    helpers = consumers(output)
    run(CARGO + ["-p", "oap-validation", "--example", "validate"], output, "build-rust-validator")
    goap = output / "goap"
    run(["go", "build", "-o", str(goap), "./go/cmd/goap"], output, "build-go-validator")
    run(["zig", "build", "--build-file", "zig/build.zig", "-Doptimize=ReleaseSafe"], output, "build-zig")
    validators = {
        "rust": [str(ROOT / "rust/target/debug/examples/validate")],
        "go": [str(goap), "validate"],
        "zig": [str(ROOT / "zig/zig-out/bin/oapx"), "validate"],
    }
    comparisons = 0
    for name, questions, answers, indexed, valid in cases():
        source = json.dumps({"questions": questions, "answers": answers}, ensure_ascii=False)
        (output / f"{name}.input.json").write_text(source)
        observed = []
        for language, command in helpers.items():
            result = json.loads(run(command, output, f"{name}-{language}", source).stdout)
            assert result["indexed"] == indexed, (name, language, result)
            assert result["valid"] == valid, (name, language, result)
            observed.append(result)
        assert observed[0] == observed[1], (name, observed)
        comparisons += 1
        if not answers:
            continue
        path = output / f"{name}.trace.json"
        path.write_text(json.dumps(trace(questions, observed[0]["answers"]), ensure_ascii=False))
        known_gap = indexed and optional_empty(questions, answers)
        for language, command in validators.items():
            result = run(command + [str(path)], output, f"{name}-{language}-wire",
                         expected=0 if indexed and not known_gap else 1)
            diagnostic = result.stdout + result.stderr
            if known_gap:
                assert "unmatched_interaction" in diagnostic and "schema_invalid" not in diagnostic, (name, language, result)
            elif not indexed:
                assert "schema_invalid" in diagnostic or "unmatched_interaction" in diagnostic, (name, language, result)
        print(f"PASS {name}: helpers; " + ("KNOWN GAP: optional empty text is schema-valid but rejected semantically by all three validators" if known_gap else "emitted wire"), flush=True)
    repair = trace([question()], [{"question_id": "q", "text": ""}])
    broken = copy.deepcopy(repair)
    for envelope in broken:
        for key in ["answers", "draft_answers"]:
            for answer in envelope["payload"].get(key, []):
                answer.pop("text", None)
    for label, value in [("missing-text", broken), ("restored-empty-text", repair)]:
        path = output / f"{label}.trace.json"
        path.write_text(json.dumps(value))
        for language, command in validators.items():
            result = run(command + [str(path)], output, f"{label}-{language}", expected=1)
            diagnostic = result.stdout + result.stderr
            if label == "missing-text":
                assert "schema_invalid" in diagnostic
            else:
                assert "schema_invalid" not in diagnostic and "unmatched_interaction" in diagnostic
    print(f"PASS {comparisons} public Rust/Go helper cases; emitted traces have no unexpected Rust/Go/Zig disagreements; missing-text schema failure repaired; optional-empty semantic validator gap remains explicit", flush=True)


def mutations(output):
    rust_answer = "rust/adapter/src/answer.rs"
    go_answer = "go/adapter/answer.go"
    variants = [
        ("rust-optional", rust_answer, 'has_text || !question.required', 'has_text', "rust-helpers"),
        ("rust-required", rust_answer, '.any(|question| question.required && !indexed.contains_key(question.id.as_str()))', '.any(|_| false)', "rust-helpers"),
        ("go-optional", go_answer, '!hasText && question.Required', '!hasText', "go"),
        ("go-required", go_answer, 'question.Required && !answered', 'false && !answered', "go"),
        ("rust-wire", "rust/protocol/src/interaction.rs", 'self.selected_option_ids.is_empty() || !self.text.is_empty()', '!self.text.is_empty()', "rust-wire"),
        ("go-wire", "go/protocol/interaction.go", 'len(a.SelectedOptionIDs) == 0 || a.Text != ""', 'a.Text != ""', "go"),
    ]
    commands = {
        "rust-helpers": ["cargo", "test", "--manifest-path", "rust/Cargo.toml", "-p", "oap-adapter", "--test", "answer_contract"],
        "rust-wire": ["cargo", "test", "--manifest-path", "rust/Cargo.toml", "-p", "oap-protocol", "--test", "input_answer"],
        "go": ["go", "test", "./go/adapter", "./go/protocol", "-count=1"],
    }
    for name, relative, before, after, suite in variants:
        path = ROOT / relative
        original = path.read_text()
        assert original.count(before) == 1, name
        try:
            path.write_text(original.replace(before, after))
            result = run(commands[suite], output, name, expected=101 if suite.startswith("rust") else 1)
            log = result.stdout + result.stderr
            assert "test result: FAILED" in log if suite.startswith("rust") else "--- FAIL:" in log, log
            assert "could not compile" not in log and "build failed" not in log, log
        finally:
            path.write_text(original)
        print(f"PASS runtime mutation killed: {name}", flush=True)
    for name, command in commands.items():
        run(command, output, f"restored-{name}")
    print("PASS six source mutations killed and original suites restored", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--mutation-check", action="store_true")
    args = parser.parse_args()
    output = Path(args.output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    (mutations if args.mutation_check else verify)(output)


if __name__ == "__main__":
    main()
