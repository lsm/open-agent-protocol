#!/usr/bin/env python3
"""Generate Decision 0065 fixtures without parsing their numeric tokens."""
import argparse
import copy
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
FIELDS = ["input_tokens", "output_tokens", "total_tokens", "cache_read_input_tokens", "cache_write_input_tokens", "reasoning_tokens"]
SITES = ["content.completed", "run.completed", "run.failed", "run.cancelled", "inference.completed", "inference.failed"]
TOKENS = [("zero", "0", ""), ("maximum", "18446744073709551615", ""),
          ("overflow", "18446744073709551616", "schema_invalid"),
          ("negative-zero", "-0", "payload_decode"), ("decimal", "1.0", "payload_decode"),
          ("exponent", "1e3", "payload_decode")]
EXTRA = [("negative", "-1", "schema_invalid"), ("fraction", "1.5", "schema_invalid"),
         ("null", "null", "schema_invalid"), ("boolean", "true", "schema_invalid"),
         ("string", '"3"', "schema_invalid"), ("integral-fraction-exponent", "1.5e1", "payload_decode"),
         ("large-exponent", "1e30", "schema_invalid"),
         ("exact-maximum-decimal", "18446744073709551615.0", "payload_decode"),
         ("exact-overflow-decimal", "18446744073709551616.0", "schema_invalid"),
         ("tiny-fraction", "1.0000000000000000000000000000000000000001", "schema_invalid")]
RANGE = "0065#usage-counts-are-canonical-unsigned-64-bit-numbers/1"
SPELLING = "0065#diagnostics-follow-value-before-spelling/1"
DECISION = "decisions/0065-usage-counts-have-an-exact-wire-spelling.md"


def load(path):
    return json.loads((ROOT / path).read_text())


def template(site):
    if site.startswith("inference."):
        trace = load("fixtures/provider/valid/unary-completion.json")
        if site == "inference.failed":
            trace[-1]["type"] = site
            trace[-1]["payload"] = {"error": {"code": "endpoint_error", "message": "fixture failure"}}
    elif site == "run.cancelled":
        trace = load("fixtures/valid/core-cancelled.json")
    else:
        trace = load("fixtures/valid/core-completed.json")
        if site == "run.failed":
            trace[-1]["type"] = site
            trace[-1]["payload"] = {"session_id": "s1", "run_id": "r1", "error": {"code": "fixture_failure", "message": "fixture failure"}}
        if site == "content.completed":
            frame = copy.deepcopy(trace[-1])
            frame.update(type=site, id="usage-message", sequence=2)
            frame["payload"] = {"session_id": "s1", "run_id": "r1", "message_id": "m1"}
            trace[-1]["sequence"] = 3
            trace.insert(-1, frame)
    for frame in trace:
        frame["payload"].pop("usage", None)
    return trace


def write_text(path, text, check):
    if check:
        if not path.is_file() or path.read_text() != text:
            raise RuntimeError(f"fixture is not current: {path}")
    else:
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)


def entry(identifier, code, provider):
    value = {"id": identifier, "path": "usage-counts/" + identifier + ".json",
             "kind": "positive" if not code else "schema-invalid" if code == "schema_invalid" else "semantic-invalid",
             "valid": not code, "phase": "" if not code else "schema" if code == "schema_invalid" else "decode",
             "codes": [code] if code else [], "units": ["provider-core" if provider else "core"],
             "provenance": [DECISION], "rules": [SPELLING if code == "payload_decode" else RANGE]}
    if provider:
        value.update(profile="model-provider-core", scope="frame")
    return value


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--check", action="store_true")
    check = parser.parse_args().check
    generated = []
    for site in SITES:
        for field in FIELDS:
            cases = TOKENS + (EXTRA if field == "input_tokens" else [])
            for label, token, code in cases:
                trace = template(site)
                target = next(frame for frame in trace if frame["type"] == site)
                target["payload"]["usage"] = {field: "RAW_USAGE_COUNT"}
                identifier = "usage-count-" + site.replace(".", "-") + "-" + field.replace("_", "-") + "-" + label
                rendered = "[\n" + ",\n".join("  " + json.dumps(frame, separators=(",", ":")) for frame in trace) + "\n]\n"
                rendered = rendered.replace('"RAW_USAGE_COUNT"', token)
                item = entry(identifier, code, site.startswith("inference."))
                write_text(ROOT / "fixtures" / item["path"], rendered, check)
                generated.append(item)
        trace = template(site)
        identifier = "usage-count-" + site.replace(".", "-") + "-absent"
        rendered = "[\n" + ",\n".join("  " + json.dumps(frame, separators=(",", ":")) for frame in trace) + "\n]\n"
        item = entry(identifier, "", site.startswith("inference."))
        write_text(ROOT / "fixtures" / item["path"], rendered, check)
        generated.append(item)
    trace = template("run.completed")
    trace[-2]["sequence"] = "RAW_SEQUENCE_ONE"
    trace[-1]["sequence"] = "RAW_SEQUENCE_TWO"
    trace[-1]["timestamp_ms"] = "RAW_TIMESTAMP"
    controls = [("envelope-numbers", trace)]
    trace = load("fixtures/valid/tools-permission-completed.json")
    for frame in trace:
        if frame["type"] == "action.call.completed":
            frame["payload"]["result"] = {"input_tokens": "RAW_TOOL_NUMBER", "total_tokens": "RAW_TOOL_LARGE"}
    controls.append(("tool-numbers", trace))
    for label, trace in controls:
        identifier = "usage-count-control-" + label
        rendered = "[\n" + ",\n".join("  " + json.dumps(frame, separators=(",", ":")) for frame in trace) + "\n]\n"
        for marker, token in {"RAW_SEQUENCE_ONE": "1.0", "RAW_SEQUENCE_TWO": "2e0", "RAW_TIMESTAMP": "1e2", "RAW_TOOL_NUMBER": "1.0", "RAW_TOOL_LARGE": "18446744073709551616"}.items():
            rendered = rendered.replace(json.dumps(marker), token)
        item = entry(identifier, "", False)
        item["rules"] = [RANGE]
        write_text(ROOT / "fixtures" / item["path"], rendered, check)
        generated.append(item)
    manifest_path = ROOT / "fixtures/manifest.json"
    source = manifest_path.read_text()
    manifest = json.loads(source)
    existing = {item["id"]: item for item in manifest["fixtures"]}
    historic = existing["payload-decode-usage-past-the-integer-range"]
    historic.update(kind="schema-invalid", phase="schema", codes=["schema_invalid"], rules=[RANGE])
    historic["provenance"] = list(dict.fromkeys(historic["provenance"] + [DECISION]))
    pattern = r'    {\n      "id": "payload-decode-usage-past-the-integer-range",.*?\n    }'
    source = re.sub(pattern, lambda _: "\n".join("    " + line for line in json.dumps(historic, indent=2).splitlines()), source, count=1, flags=re.S)
    additions = []
    for item in generated:
        if item["id"] in existing:
            if existing[item["id"]] != item:
                raise RuntimeError(f"manifest entry differs: {item['id']}")
        else:
            additions.append("    " + json.dumps(item, separators=(",", ":")))
    if additions:
        head, tail = source.rsplit("\n  ]", 1)
        source = head + ",\n" + ",\n".join(additions) + "\n  ]" + tail
    if check and manifest_path.read_text() != source:
        raise RuntimeError("manifest needs the generated entries")
    if not check:
        manifest_path.write_text(source)
    print(f"{'Checked' if check else 'Generated'} {len(generated)} raw-preserving usage fixtures")


if __name__ == "__main__":
    main()
