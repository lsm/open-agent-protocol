use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc::Receiver;
use std::time::Duration;

use oxap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    RequestedDeliveryMode, SessionOpenRequest,
};
use serde::Serialize;
use serde_json::Value;

struct Daemon {
    child: Child,
    stdin: ChildStdin,
    out: Receiver<Value>,
    err: Receiver<String>,
}

impl Daemon {
    fn start() -> Self {
        let mut child = Command::new(env!("CARGO_BIN_EXE_oap-rust-hub"))
            .args(["serve", "--stdio"])
            .stdin(Stdio::piped())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .spawn()
            .unwrap();
        let stdin = child.stdin.take().unwrap();
        let (sent, out) = std::sync::mpsc::channel();
        let stdout = BufReader::new(child.stdout.take().unwrap());
        std::thread::spawn(move || {
            for line in stdout.lines().map_while(Result::ok) {
                if sent.send(serde_json::from_str(&line).unwrap()).is_err() {
                    return;
                }
            }
        });
        let (logged, err) = std::sync::mpsc::channel();
        let stderr = BufReader::new(child.stderr.take().unwrap());
        std::thread::spawn(move || {
            for line in stderr.lines().map_while(Result::ok) {
                if logged.send(line).is_err() {
                    return;
                }
            }
        });
        Self {
            child,
            stdin,
            out,
            err,
        }
    }

    fn send(&mut self, line: &str) {
        writeln!(self.stdin, "{line}").unwrap();
    }

    fn answer(&self, id: i64) -> Value {
        loop {
            let line = self.out.recv_timeout(Duration::from_secs(10)).unwrap();
            if line["id"] == id {
                return line;
            }
        }
    }

    fn logged(&self, wanted: &str) -> String {
        loop {
            let line = self.err.recv_timeout(Duration::from_secs(10)).unwrap();
            if line.contains(wanted) {
                return line;
            }
        }
    }

    fn signal(&self, name: &str) {
        let sent = Command::new("kill")
            .args([format!("-{name}"), self.child.id().to_string()])
            .status()
            .unwrap();
        assert!(sent.success());
    }

    fn exits_with_input_open(&mut self) -> std::process::ExitStatus {
        let deadline = std::time::Instant::now() + Duration::from_secs(10);
        loop {
            if let Some(status) = self.child.try_wait().unwrap() {
                return status;
            }
            assert!(
                std::time::Instant::now() < deadline,
                "the hub went on serving"
            );
            std::thread::sleep(Duration::from_millis(20));
        }
    }
}

fn envelope(kind: EnvelopeType, payload: &impl Serialize) -> String {
    serde_json::to_string(&Envelope::new(kind, "req", payload).unwrap()).unwrap()
}

fn open(id: i64, session: &str) -> String {
    let request = envelope(
        EnvelopeType::SESSION_OPEN_REQUEST,
        &SessionOpenRequest {
            session_id: Some(session.into()),
            ..SessionOpenRequest::default()
        },
    );
    format!(r#"{{"id":{id},"op":"open","adapter":"memory","request":{request}}}"#)
}

fn running(daemon: &mut Daemon) -> String {
    daemon.send(&open(1, "s"));
    assert_eq!(daemon.answer(1)["ok"], true);
    let mut request = Envelope::new(
        EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
        "req",
        &MessageSubmitRequest {
            session_id: "s".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("run"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
    )
    .unwrap();
    request.session_id = Some("s".into());
    let request = serde_json::to_string(&request).unwrap();
    daemon.send(&format!(
        r#"{{"id":2,"op":"submit","session_id":"s","request":{request}}}"#
    ));
    daemon.answer(2)["result"]["run_id"]
        .as_str()
        .unwrap()
        .to_owned()
}

#[test]
fn a_first_signal_drains_naming_the_live_run_and_a_second_forces_the_hub_out() {
    let mut daemon = Daemon::start();
    let run = running(&mut daemon);
    daemon.signal("INT");
    assert_eq!(
        daemon.logged("shutting down gracefully"),
        format!("oap-rust-hub: shutting down gracefully; still active: s/{run}; 0 queued run(s) lost; signal again to force")
    );
    daemon.send(&open(3, "t"));
    assert_eq!(daemon.answer(3)["error"]["code"], "shutting_down");
    daemon.signal("TERM");
    assert_eq!(
        daemon.logged("forced"),
        format!("oap-rust-hub: forced shutdown; cut off: s/{run}")
    );
    assert!(daemon.exits_with_input_open().success());
}

#[test]
fn a_signal_with_nothing_running_stops_the_hub_at_once() {
    let mut daemon = Daemon::start();
    daemon.send(&open(1, "s"));
    assert_eq!(daemon.answer(1)["ok"], true);
    daemon.signal("TERM");
    assert_eq!(
        daemon.logged("shutting down"),
        "oap-rust-hub: shutting down gracefully; still active: none; 0 queued run(s) lost; signal again to force"
    );
    assert!(daemon.exits_with_input_open().success());
}
