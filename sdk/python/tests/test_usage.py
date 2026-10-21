"""Public Python consumers must retain measurements and refuse malformed usage."""

import json
import os
from pathlib import Path
import subprocess
import signal
import sys
import unittest
from typing import Any

SDK = Path(__file__).resolve().parents[1]
FIELDS = {"input_tokens": "input", "output_tokens": "output", "total_tokens": "total",
          "cache_read_input_tokens": "cache_read", "cache_write_input_tokens": "cache_write",
          "reasoning_tokens": "reasoning"}
MAX = 2**64 - 1


def observe(raw: str, site: str = "completed") -> list[dict[str, Any]]:
    child = subprocess.Popen(
        [sys.executable, str(SDK / "examples/usage.py"), sys.executable, "-u",
         str(SDK / "tests/fixtures/usage.py"), raw, site],
        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
    try:
        stdout, stderr = child.communicate(timeout=20)
    except subprocess.TimeoutExpired:
        os.killpg(child.pid, signal.SIGKILL)
        child.communicate()
        raise
    assert child.returncode == 0, stderr
    values: list[dict[str, Any]] = json.loads(stdout)
    assert [(value["agent"], value["stream"]) for value in values] == [
        (False, False), (False, True), (True, False), (True, True)]
    return values


class UsageTests(unittest.TestCase):
    def test_absent_zero_partial_total_and_uint64_maximum(self) -> None:
        cases: list[tuple[str, Any]] = [("absent", None), ("{}", {})]
        cases += [(json.dumps({wire: count}), {name: count})
                  for wire, name in FIELDS.items() for count in (0, MAX)]
        cases.append((json.dumps({"input_tokens": MAX, "output_tokens": 0, "total_tokens": 17}),
                      {"input": MAX, "output": 0, "total": 17}))
        for raw, expected in cases:
            with self.subTest(usage=raw):
                if expected is not None:
                    expected = {name: expected.get(name) for name in FIELDS.values()}
                for value in observe(raw):
                    self.assertEqual(value.get("usage", "missing"), expected, value)

    def test_malformed_usage_is_a_typed_protocol_error(self) -> None:
        counts: list[Any] = [None, -1, 1.5, 1.0, True, "3", {}, [], MAX + 1]
        cases = [json.dumps({field: count}) for field in FIELDS
                 for count in counts]
        cases += ["null", "true", "[]", "3", '"usage"']
        for raw in cases:
            with self.subTest(usage=raw):
                for value in observe(raw):
                    self.assertNotIn("usage", value)
                    self.assertEqual(value.get("error"), "malformed_response", value)
                    self.assertEqual(value.get("error_type"), "MakaiProtocolError", value)

    def test_negative_zero_tokens_are_typed_protocol_errors(self) -> None:
        cases = ['{"' + field + '":-0}' for field in FIELDS]
        cases.append('{"input_tokens":7,"output_tokens":-0}')
        for raw in cases:
            with self.subTest(usage=raw):
                for value in observe(raw):
                    self.assertNotIn("usage", value)
                    self.assertEqual(value.get("error"), "malformed_response", value)
                    self.assertEqual(value.get("error_type"), "MakaiProtocolError", value)

    def test_negative_zero_in_tool_results_keeps_ordinary_json_decoding(self) -> None:
        values = observe("ordinary-negative-zero")
        self.assertTrue(all(value.get("usage", "missing") is None for value in values), values)
        self.assertEqual(values[-1]["tool_results"], [{"input_tokens": 0,
                         "nested": {"usage": {"input_tokens": 0}}, "string": "-0"}])

    def test_negative_zero_on_content_and_failure_frames_is_refused(self) -> None:
        for site in ("content.completed", "run.failed", "run.cancelled", "inference.failed"):
            for field in FIELDS:
                with self.subTest(site=site, field=field):
                    values = observe('{"' + field + '":-0}', site)
                    selected = [value for value in values if value["agent"] == (site != "inference.failed")]
                    for value in selected:
                        self.assertNotIn("usage", value)
                        self.assertEqual(value.get("error"), "malformed_response", value)
                        self.assertEqual(value.get("error_type"), "MakaiProtocolError", value)
