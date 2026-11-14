#!/usr/bin/env python3
"""Observe declared and future vocabulary values through a separate Rust consumer."""
import argparse
import json
from pathlib import Path
import subprocess

ROOT = Path(__file__).resolve().parents[4]
SOURCE = '''use oxap_protocol::{Admission, EnvelopeType, RequestedDeliveryMode, RunStatus};
use serde_json::{json, Value};
fn main() {
    let mut rows: Vec<Value> = Vec::new();
    for name in ["run.started", "session.task.updated", "com.example.future.event", ""] {
        let value: EnvelopeType = serde_json::from_value(json!(name)).unwrap();
        rows.push(json!({"vocabulary":"envelope", "value":value, "known":value.is_known()}));
    }
    for name in ["running", "paused", ""] {
        let value: RunStatus = serde_json::from_value(json!(name)).unwrap();
        rows.push(json!({"vocabulary":"status", "value":value, "known":value.is_known()}));
    }
    for name in ["started", "merged", ""] {
        let value: Admission = serde_json::from_value(json!(name)).unwrap();
        rows.push(json!({"vocabulary":"admission", "value":value, "known":value.is_known()}));
    }
    for name in ["auto", "later", ""] {
        let value: RequestedDeliveryMode = serde_json::from_value(json!(name)).unwrap();
        rows.push(json!({"vocabulary":"delivery", "value":value, "known":value.is_known()}));
    }
    println!("{}", serde_json::to_string(&rows).unwrap());
}
'''


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", required=True)
    output = Path(parser.parse_args().output_dir).resolve()
    output.mkdir(parents=True, exist_ok=False)
    project = output / "consumer"
    (project / "src").mkdir(parents=True)
    (project / "src/main.rs").write_text(SOURCE)
    (project / "Cargo.toml").write_text(
        '[package]\nname="vocabulary-consumer"\nversion="0.0.0"\nedition="2021"\n'
        f'[dependencies]\noxap-protocol={{path={json.dumps(str(ROOT / "rust/protocol"))}}}\nserde_json="1"\n')
    result = subprocess.run(["cargo", "run", "--quiet", "--offline", "--manifest-path",
                             str(project / "Cargo.toml"), "--target-dir", str(ROOT / "rust/target")],
                            cwd=ROOT, capture_output=True, text=True, timeout=600)
    (output / "consumer.stdout").write_text(result.stdout)
    (output / "consumer.stderr").write_text(result.stderr)
    assert result.returncode == 0, result.stderr
    rows = json.loads(result.stdout)
    values = {
        "envelope":["run.started", "session.task.updated", "com.example.future.event", ""],
        "status":["running", "paused", ""],
        "admission":["started", "merged", ""],
        "delivery":["auto", "later", ""],
    }
    expected = [{"vocabulary":name, "value":value, "known":index == 0}
                for name, terms in values.items() for index, value in enumerate(terms)]
    assert rows == expected, rows
    (output / "results.json").write_text(json.dumps(rows, indent=2) + "\n")
    print(f"PASS: {len(rows)} public vocabulary queries preserve known, future, staged and empty wire values")


if __name__ == "__main__":
    main()
