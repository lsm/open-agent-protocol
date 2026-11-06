import argparse
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
use oap_conformance::{read_frame, Client, ClientError, ControlFrame, Frame, Process, ProcessError, Transcript};
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
        println!("PASS: real endpoint/process/Client exchange and EOF; shared wait budgets; soft control timeout; trace ordering; exact cursors; malformed JSON refused");
}
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
    ]:
        result = subprocess.run(command, cwd=ROOT, capture_output=True, text=True, timeout=300)
        (out / f"{name}-build.txt").write_text(result.stdout + result.stderr)
        assert result.returncode == 0, f"{name} build failed; inspect retained build log"
    with tempfile.TemporaryDirectory(prefix="oap-conformance-home-") as home:
        env = {"PATH": os.environ["PATH"], "HOME": home, "XDG_DATA_HOME": home + "/data", "XDG_CONFIG_HOME": home + "/config"}
        command = [str(target / "debug/oap-conformance-wire-proof"), str(target / "debug/oap-rust-hub")]
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
                text = stdout.read().decode() + stderr.read().decode()
                (out / "consumer.txt").write_text(text)
                print(text, end="")
                assert process.returncode == 0 and "PASS:" in text, "public consumer failed"


if __name__ == "__main__":
    main()
