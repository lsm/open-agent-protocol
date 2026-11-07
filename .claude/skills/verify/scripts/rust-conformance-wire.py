import argparse
import json
import os
from pathlib import Path
import signal
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[4]
SOURCE = r'''
use std::io::Cursor;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};
use oap_conformance::{framing_contract, read_frame, Client, ClientError, ControlFrame, Frame, Process, ProcessError, Runner, Transcript};
use oap_protocol::{Envelope, EnvelopeType};
use serde_json::json;

fn main() {
    let mut endpoint = Command::new(std::env::args().nth(1).unwrap());
    endpoint.args(["serve", "agent"]).stderr(Stdio::null());
    let mut process = Process::spawn(&mut endpoint).unwrap();
        let mut transcript = Transcript::default();
        for (kind, id, payload, expected) in [
            ("protocol.initialize.request", "init", json!({"protocol_versions":["0.1"],"profiles":["open-agent-protocol.agent-control-core"]}), "protocol.initialize.response"),
            ("capabilities.request", "caps", json!({}), "capabilities.response"),
        ] {
            let request = Envelope::new(EnvelopeType::from(kind), id, &payload).unwrap();
            process.send(&Frame::Envelope(Box::new(request.clone()))).unwrap();
            transcript.push(request);
            let Some(Frame::Envelope(answer)) = process.next(Duration::from_secs(2)).unwrap() else { panic!("no envelope"); };
            assert_eq!(answer.in_reply_to.as_ref().unwrap().as_str(), id);
            assert_eq!(answer.envelope_type.as_str(), expected);
            transcript.push(*answer);
            println!("endpoint answered {expected}");
        }
        assert_eq!(transcript.envelopes().len(), 4);
        process.close_input();
        assert!(process.next(Duration::from_secs(2)).unwrap().is_none());
        assert!(process.wait_exit(Duration::from_secs(2)).unwrap().success());
        println!("endpoint exited 0 after EOF");
        let mut trace = Transcript::default();
        let mut event = Envelope::new(EnvelopeType::RUN_STARTED, "event", &json!({})).unwrap();
        event.run_id = Some("r".into()); event.sequence = Some(1);
        trace.push(event.clone()); trace.push(event);
        trace.push(Envelope::new(EnvelopeType::SESSION_OPEN_RESPONSE, "open", &json!({"active_run_id":"r"})).unwrap());
        trace.probe("probe".into());
        let mut refusal = Envelope::new(EnvelopeType::ERROR_RESPONSE, "refusal", &json!({})).unwrap();
        refusal.in_reply_to = Some("probe".into()); trace.push(refusal);
        assert_eq!(trace.envelopes().iter().map(|e| e.id.as_str()).collect::<Vec<_>>(), ["open", "event"]);
        let mut cursor = Cursor::new("{\"control\":\"replay\",\"after\":0,\"latest_available\":18446744073709551615}");
        let Some(Frame::Control(control)) = read_frame(&mut cursor).unwrap() else { panic!("no control"); };
        assert_eq!(control.after, Some(0)); assert_eq!(control.latest_available, u64::MAX);
        assert!(read_frame(&mut Cursor::new("{")).is_err());
        for (script, closes_stdout) in [
            ("import time; time.sleep(30)", false),
            ("import os,time; os.close(1); time.sleep(30)", true),
        ] {
            let mut command = Command::new("python3");
            command.args(["-u", "-c", script]);
            let mut peer = Process::spawn(&mut command).unwrap();
            let start = Instant::now();
            if closes_stdout {
                assert!(peer.next(Duration::from_secs(2)).unwrap().is_none());
                assert!(matches!(peer.wait_exit(Duration::from_millis(100)), Err(ProcessError::ExitTimeout(_))));
            } else {
                assert!(matches!(peer.next(Duration::from_millis(100)), Err(ProcessError::Timeout)));
                peer.close().unwrap();
            }
            assert!(start.elapsed() < Duration::from_secs(3));
            println!("bounded cleanup of synthetic peer, stdout closed = {closes_stdout}");
        }
        let mut endpoint = Command::new(std::env::args().nth(1).unwrap());
        endpoint.args(["serve", "agent"]).stderr(Stdio::null());
        let mut client = Client::spawn(&mut endpoint, Duration::from_secs(2)).unwrap();
        client.send(Envelope::new(EnvelopeType::CAPABILITIES_REQUEST, "client-caps", &json!({})).unwrap()).unwrap();
        assert_eq!(client.response(&"client-caps".into()).unwrap().envelope_type, EnvelopeType::CAPABILITIES_RESPONSE);
        assert_eq!(client.transcript().len(), 2);
        client.close_input();
        assert!(client.wait().unwrap().success());
        println!("public Client matched capabilities response, retained trace and observed EOF exit 0");
        let mut command = Command::new("python3");
        command.args(["-u", "-c", r#"import json,sys,time
sys.stdin.readline()
end=time.monotonic()+3
i=0
while time.monotonic()<end:
 print(json.dumps({'protocol':'open-agent-protocol','type':'run.started','id':str(i),'payload':{}}),flush=True)
 i+=1
 time.sleep(.005)
"#]);
        let mut client = Client::spawn(&mut command, Duration::from_millis(500)).unwrap();
        client.send(Envelope::new(EnvelopeType::CAPABILITIES_REQUEST, "missing", &json!({})).unwrap()).unwrap();
        client.event().unwrap();
        let start = Instant::now();
        assert!(matches!(client.response(&"missing".into()), Err(ClientError::Timeout { .. })));
        assert!(start.elapsed() >= Duration::from_millis(500) && start.elapsed() < Duration::from_secs(1));
        assert!(client.transcript().len() > 3);
        println!("chatty response wait stopped after {:?}: {}", start.elapsed(), client.closed_by_runner().unwrap());
        let mut command = Command::new("python3");
        command.args(["-u", "-c", r#"import json,sys,time
sys.stdin.readline()
end=time.monotonic()+.7
while time.monotonic()<end:
 print(json.dumps({'control':'replay.accepted','id':'unrelated'}),flush=True)
 time.sleep(.005)
sys.stdin.readline()
print(json.dumps({'protocol':'open-agent-protocol','type':'capabilities.response','id':'answer','in_reply_to':'resume','payload':{}}),flush=True)
"#]);
        let mut client = Client::spawn(&mut command, Duration::from_millis(500)).unwrap();
        client.send_control(ControlFrame { control: "replay".into(), id: "missing".into(), ..ControlFrame::default() }).unwrap();
        let start = Instant::now();
        assert!(matches!(client.control("missing"), Err(ClientError::ControlUnanswered)));
        assert!(start.elapsed() >= Duration::from_millis(500) && start.elapsed() < Duration::from_secs(1));
        assert!(client.closed_by_runner().is_none());
        client.send(Envelope::new(EnvelopeType::CAPABILITIES_REQUEST, "resume", &json!({})).unwrap()).unwrap();
        assert_eq!(client.response(&"resume".into()).unwrap().id.as_str(), "answer");
        println!("unanswered control used one budget; endpoint then answered resume");
        let mut endpoint = Command::new(std::env::args().nth(1).unwrap());
        endpoint.args(["serve", "agent"]).stderr(Stdio::null());
        let mut client = Client::spawn(&mut endpoint, Duration::from_secs(2)).unwrap();
        let mut runner = Runner::new(&mut client, "bootstrap");
        runner.initialize();
        runner.capabilities().unwrap();
        assert_eq!(runner.open().unwrap().session_id.as_str(), "bootstrap");
        assert_eq!(runner.checks().len(), 6);
        assert!(runner.checks().iter().all(|check| check.passed));
        println!("bootstrap checks {}", serde_json::to_string(&runner.into_checks()).unwrap());
        assert_eq!(client.transcript().len(), 6);
        client.close_input();
        assert!(client.wait().unwrap().success());
        let mut command = Command::new("python3");
        command.args(["-u", "-c", r#"import json,sys
for line in sys.stdin:
 request=json.loads(line)
 print(json.dumps({'protocol':'open-agent-protocol','type':'protocol.initialize.response','id':'bad','in_reply_to':request['id'],'payload':['synthetic-private-sentinel']}),flush=True)
"#]);
        let mut client = Client::spawn(&mut command, Duration::from_secs(2)).unwrap();
        let mut runner = Runner::new(&mut client, "bootstrap");
        runner.initialize();
        assert_eq!(runner.checks().len(), 1);
        let check = &runner.checks()[0];
        assert!(!check.passed && check.name == "protocol.initialize.response decodes");
        assert!(!check.detail.contains("synthetic-private-sentinel"));
        println!("malformed bootstrap {}: {}", check.name, check.detail);
        runner.into_checks();
        client.close_input();
        assert!(client.wait().unwrap().success());
        let mut endpoint = Command::new(std::env::args().nth(1).unwrap());
        endpoint.args(["serve", "agent"]).stderr(Stdio::null());
        let mut client = Client::spawn(&mut endpoint, Duration::from_secs(2)).unwrap();
        let mut runner = Runner::new(&mut client, "models");
        runner.initialize();
        let descriptor = runner.capabilities().unwrap();
        let opened = runner.open().unwrap();
        runner.list_sessions(&descriptor);
        let selected = runner.select_model(&descriptor, &opened.current_model_id, "");
        assert_ne!(selected, opened.current_model_id);
        assert!(runner.switch_model(&selected, &opened.current_model_id));
        assert_eq!(runner.checks().len(), 10);
        let alternate = runner.select_model(&descriptor, &selected, "");
        assert_ne!(alternate, selected);
        assert!(runner.switch_model(&alternate, &selected));
        assert_eq!(runner.checks().len(), 13);
        assert!(runner.checks().iter().all(|check| check.passed));
        println!("model changed from {} to {}; checks {}", selected, alternate, serde_json::to_string(runner.checks()).unwrap());
        let run = runner.submit_run(&alternate, true).unwrap();
        assert!(runner.consume_run(&run));
        assert_eq!(runner.checks().len(), 20);
        assert!(runner.checks().iter().all(|check| check.passed));
        println!("admitted run {run} resolved permission and user input, reached its terminal; checks {}", serde_json::to_string(runner.checks()).unwrap());
        runner.replay_run(&run);
        assert!(runner.checks().last().unwrap().passed && !runner.checks().last().unwrap().skipped);
        println!("cursor replay re-delivered the exact ordered run identities, types and sequences");
        runner.check_state();
        runner.refuse_stale_revision();
        runner.answer_cancel(&descriptor, Some(&run));
        runner.refuse_addressable_envelope();
        runner.refuse_unknown_reopen(&descriptor);
        assert_eq!(runner.checks().len(), 26);
        assert!(runner.checks().iter().all(|check| check.passed));
        println!("state and refusal checks {}", serde_json::to_string(&runner.into_checks()).unwrap());
        let trace = client.transcript();
        assert!(trace.iter().all(|envelope| envelope.envelope_type != EnvelopeType::RUN_CANCEL_REQUEST && envelope.envelope_type.as_str() != "conformance.not.a.real.request" && !envelope.capability_revision.as_deref().unwrap_or("").ends_with("-stale")));
        for kind in [EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST, EnvelopeType::USER_INPUT_RESOLVE_REQUEST, EnvelopeType::RUN_COMPLETED] {
            assert_eq!(trace.iter().filter(|envelope| envelope.envelope_type == kind).count(), 1);
        }
        assert!(client.transcript().iter().all(|envelope| !envelope.envelope_type.as_str().starts_with("session.list.")));
        client.close_input();
        assert!(client.wait().unwrap().success());
        let mut endpoint = Command::new(std::env::args().nth(1).unwrap());
        endpoint.args(["serve", "agent"]).stderr(Stdio::null());
        let framing = framing_contract(&mut endpoint, Duration::from_secs(2));
        assert!(framing.iter().all(|check| check.passed));
        println!("real endpoint fatal framing checks {}", serde_json::to_string(&framing).unwrap());
        let report = oap_conformance::run(&mut endpoint, oap_conformance::Options {
            line_deadline: Duration::from_secs(2),
            ..oap_conformance::Options::default()
        }).unwrap();
        assert!(report.passed, "checks {:?}. Diagnostics {:?}", report.checks, report.diagnostics);
        assert_eq!(report.checks.len(), 27);
        assert!(report.diagnostics.is_empty());
        assert!(!report.trace.is_empty());
        let serialized = serde_json::to_value(&report).unwrap();
        assert!(serialized.get("trace").is_none());
        println!("complete native report passed all 27 checks, including the Rust validator, EOF and fatal inputs");
        println!("PASS: real endpoint/process/Client/Runner exchange and EOF; shared budgets; bootstrap/model/list/run/refusal/replay checks; fatal framing; streamed gates and terminal; malformed-object rejection; trace ordering; exact cursors");
}
'''

REFUSAL_ENDPOINT = r'''
import json,sys
for line in sys.stdin:
 try: request=json.loads(line)
 except ValueError: sys.exit(7)
 if 'id' not in request: sys.exit(7)
 print(json.dumps({'protocol':'open-agent-protocol','type':'error.response','id':'refusal-'+request['id'],'in_reply_to':request['id'],'payload':{'error':{'code':'fixture_refusal','message':'synthetic refusal'}}}),flush=True)
'''


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output-dir", required=True)
    out = Path(parser.parse_args().output_dir).resolve()
    out.mkdir(parents=True, exist_ok=False)
    source = out / "consumer/src"
    source.mkdir(parents=True)
    (source / "main.rs").write_text(SOURCE)
    manifest = source.parent / "Cargo.toml"
    manifest.write_text(
        '[package]\nname="oap-conformance-wire-proof"\nversion="0.0.0"\nedition="2021"\n'
        '[dependencies]\nserde_json="1"\n'
        f'oap-conformance={{path="{ROOT / "rust/conformance"}"}}\n'
        f'oap-protocol={{path="{ROOT / "rust/protocol"}"}}\n'
    )
    target = ROOT / "rust/target"
    for name, command in [
        ("endpoint", ["cargo", "build", "--manifest-path", str(ROOT / "rust/Cargo.toml"), "-p", "oap-serve", "--bin", "oap-rust-hub"]),
        ("consumer", ["cargo", "build", "--offline", "--manifest-path", str(manifest), "--target-dir", str(target)]),
        ("runner", ["cargo", "build", "--manifest-path", str(ROOT / "rust/Cargo.toml"), "-p", "oap-conformance", "--example", "endpoint_conformance"]),
        ("go", ["go", "build", "-o", str(out / "goap"), "./go/cmd/goap"]),
    ]:
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
        (out / f"{name}-build.txt").write_text(result.stdout + result.stderr)
        assert result.returncode == 0, f"{name} build failed; inspect retained build log"
    with tempfile.TemporaryDirectory(prefix="oap-conformance-home-") as home:
        env = {"PATH": os.environ["PATH"], "HOME": home, "XDG_DATA_HOME": home + "/data", "XDG_CONFIG_HOME": home + "/config"}
        example = str(target / "debug/examples/endpoint_conformance")
        for name, command in [
            ("consumer", [str(target / "debug/oap-conformance-wire-proof"), str(target / "debug/oap-rust-hub")]),
            ("rust-report", [example, str(target / "debug/oap-rust-hub"), "serve", "agent"]),
            ("go-report", [example, str(out / "goap"), "serve", "agent"]),
            ("refusal-report", [example, "python3", "-u", "-c", REFUSAL_ENDPOINT]),
        ]:
            with tempfile.TemporaryFile() as stdout, tempfile.TemporaryFile() as stderr:
                with subprocess.Popen(command, env=env, stdout=stdout, stderr=stderr, start_new_session=True) as process:
                    try:
                        process.wait(timeout=30)
                    finally:
                        try:
                            os.killpg(process.pid, signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    stdout.seek(0)
                    stderr.seek(0)
                    observed, diagnostic = stdout.read().decode(), stderr.read().decode()
                    (out / f"{name}.stderr.txt").write_text(diagnostic)
                    if name == "consumer":
                        (out / "consumer.txt").write_text(observed + diagnostic)
                        print(observed + diagnostic, end="")
                        assert process.returncode == 0 and "PASS:" in observed, "public consumer failed"
                    else:
                        (out / f"{name}.json").write_text(observed)
                        expected = name != "refusal-report"
                        assert process.returncode == (0 if expected else 1), f"{name} failed; inspect retained output"
                        report = json.loads(observed)
                        assert report["passed"] == expected and "trace" not in report
                        if expected:
                            assert len(report["checks"]) == 27 and not report.get("diagnostics")
                            print(f"{name}: native CLI passed 27 checks, Rust validation, EOF and fatal input contracts")
                        else:
                            assert any(not check["passed"] for check in report["checks"])
                            print("refusal-report: native CLI reported failed checks and exited 1")


if __name__ == "__main__":
    main()
