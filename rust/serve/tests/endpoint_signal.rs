use std::io::{BufRead, BufReader, Write};
use std::process::{Child, ChildStdin, Command, Stdio};
use std::sync::mpsc::Receiver;
use std::time::Duration;

use serde_json::{json, Value};

struct Agent {
    child: Child,
    stdin: ChildStdin,
    out: Receiver<Value>,
    err: Receiver<String>,
}

impl Agent {
    fn start() -> Self {
        let mut child = Command::new(env!("CARGO_BIN_EXE_oap-rust-hub"))
            .args(["serve", "agent"])
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

    fn send(&mut self, line: Value) {
        writeln!(self.stdin, "{line}").unwrap();
    }

    fn answer(&self, id: &str) -> Value {
        loop {
            let line = self.out.recv_timeout(Duration::from_secs(5)).unwrap();
            if line["in_reply_to"] == id {
                return line;
            }
        }
    }

    fn logged(&self, wanted: &str) -> String {
        loop {
            let line = self.err.recv_timeout(Duration::from_secs(5)).unwrap();
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
                "the agent went on serving"
            );
            std::thread::sleep(Duration::from_millis(20));
        }
    }
}

fn request(kind: &str, id: &str, payload: Value) -> Value {
    json!({
        "protocol": "oap",
        "version": "0.1",
        "profile": "open-agent-protocol.agent-control-core",
        "type": kind,
        "id": id,
        "session_id": "s",
        "payload": payload,
    })
}

fn running(agent: &mut Agent) -> String {
    agent.send(request(
        "session.open.request",
        "o",
        json!({"session_id": "s"}),
    ));
    agent.send(request(
        "session.message.submit.request",
        "m",
        json!({"session_id": "s", "delivery": "auto", "messages": [{"role": "user", "content": "run"}]}),
    ));
    agent.answer("m")["run_id"].as_str().unwrap().to_owned()
}

#[test]
fn a_first_signal_drains_naming_the_live_run_and_a_second_forces_the_agent_out() {
    let mut agent = Agent::start();
    let run = running(&mut agent);
    agent.signal("INT");
    let line = agent.logged("shutting down gracefully");
    assert_eq!(
        line,
        format!("oap-rust-hub: shutting down gracefully; still active: s/{run}; 0 queued run(s) lost; signal again to force")
    );
    agent.send(request(
        "session.open.request",
        "late",
        json!({"session_id": "t"}),
    ));
    assert_eq!(
        agent.answer("late")["payload"]["error"]["code"],
        "shutting_down"
    );
    agent.signal("TERM");
    assert_eq!(
        agent.logged("forced"),
        format!("oap-rust-hub: forced shutdown; cut off: s/{run}")
    );
    assert!(agent.exits_with_input_open().success());
}

#[test]
fn a_signal_with_nothing_running_stops_the_agent_at_once() {
    let mut agent = Agent::start();
    agent.send(request(
        "session.open.request",
        "o",
        json!({"session_id": "s"}),
    ));
    agent.answer("o");
    agent.signal("TERM");
    assert_eq!(
        agent.logged("shutting down"),
        "oap-rust-hub: shutting down gracefully; still active: none; 0 queued run(s) lost; signal again to force"
    );
    assert!(agent.exits_with_input_open().success());
}
