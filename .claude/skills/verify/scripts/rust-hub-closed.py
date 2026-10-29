import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", required=True)
    args = parser.parse_args()
    out = Path(args.output_dir).resolve()
    source = out / "consumer" / "src"
    source.mkdir(parents=True, exist_ok=True)
    fixture = (ROOT / "rust/serve/tests/hub_open.rs").read_text().split("\n#[test]\n", 1)[0]
    (source / "main.rs").write_text(fixture + '''
fn main() {
    for answer in [Answer::Closed, Answer::ClosedState] {
        let (hub, seen) = hub_with(answer);
        assert!(matches!(block_on(hub.open("manual", request("reused"))), Err(HubError::ClosedAtOpen(_))));
        assert_eq!(seen.lock().unwrap().closed, ["reused"]);
        assert!(hub.sessions().is_empty());
        block_on(hub.open("live", request("reused"))).unwrap();
        println!("closed at open: adapter cleaned and ID reused");
    }
    let (hub, _) = hub_with(Answer::StateClosesAfterOpen);
    let opened = block_on(hub.open("manual", request("reused"))).unwrap();
    assert_eq!(block_on(opened.session.state()).unwrap().status, SessionStatus::CLOSED);
    assert!(hub.sessions().is_empty());
    assert!(hub.session(&"reused".into()).is_err());
    block_on(hub.open("live", request("reused"))).unwrap();
    println!("closed snapshot: listing cleared and ID reused");
}
''')
    dependencies = "\n".join(name + " = { path = " + json.dumps(str(ROOT / path)) + " }" for name, path in [("oap-adapter", "rust/adapter"), ("oap-protocol", "rust/protocol"), ("oap-serve", "rust/serve")])
    manifest = source.parent / "Cargo.toml"
    manifest.write_text('[package]\nname = "oap-hub-closed-proof"\nversion = "0.0.0"\nedition = "2021"\n[dependencies]\nfutures = "0.3"\n' + dependencies + "\n")
    built = subprocess.run(["cargo", "build", "--offline", "--manifest-path", str(manifest), "--target-dir", str(out / "target")], capture_output=True, text=True, timeout=300)
    (out / "build.txt").write_text(built.stdout + built.stderr)
    assert built.returncode == 0, "standalone consumer did not compile; inspect build.txt"
    with tempfile.TemporaryDirectory(prefix="oap-hub-closed-home-") as home:
        env = {"PATH": os.environ["PATH"], "HOME": home, "XDG_DATA_HOME": home + "/data", "XDG_CONFIG_HOME": home + "/config"}
        result = subprocess.run([str(out / "target/debug/oap-hub-closed-proof")], env=env, capture_output=True, text=True, timeout=30)
        (out / "consumer.txt").write_text(result.stdout + result.stderr)
        assert result.returncode == 0, "public hub lifecycle failed; inspect consumer.txt"
    print("PASS: standalone public Rust Hub consumer cleaned closed opens, removed closed snapshots and reused IDs; synthetic adapters; temporary HOME removed")


if __name__ == "__main__":
    main()
