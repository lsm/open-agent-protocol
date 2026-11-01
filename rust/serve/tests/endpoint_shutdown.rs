mod support;

use std::io::{PipeWriter, Write};
use std::sync::mpsc::{Receiver, RecvTimeoutError};
use std::time::{Duration, Instant};

use oap_serve::endpoint::{Endpoint, EndpointError, EndpointOptions};
use oap_serve::{Hub, Registry};
use serde_json::{json, Value};

struct Serving {
    writer: PipeWriter,
    out: support::Captured,
    ended: Receiver<Result<(), EndpointError>>,
}

impl Serving {
    fn start() -> Self {
        let hub = Hub::new(Registry::default_registry());
        let endpoint = Endpoint::new(
            &hub,
            EndpointOptions {
                adapter: "memory".into(),
                shutdown: Some(Duration::from_millis(500)),
                ..EndpointOptions::default()
            },
        )
        .unwrap();
        let (reader, writer) = std::io::pipe().unwrap();
        let out = support::Captured::default();
        let served = out.clone();
        let (finished, ended) = std::sync::mpsc::channel();
        std::thread::spawn(move || {
            let _ = finished.send(endpoint.run(std::io::BufReader::new(reader), served));
        });
        Self { writer, out, ended }
    }

    fn send(&mut self, line: Value) {
        writeln!(self.writer, "{line}").unwrap();
    }

    fn answer(&self, id: &str) -> Value {
        self.wait_for(|line| line["in_reply_to"] == id)
    }

    fn wait_for(&self, found: impl Fn(&Value) -> bool) -> Value {
        let deadline = Instant::now() + Duration::from_secs(5);
        loop {
            if let Some(line) = self.out.values().into_iter().find(|line| found(line)) {
                return line;
            }
            assert!(Instant::now() < deadline, "{:?}", self.out.values());
            std::thread::sleep(Duration::from_millis(10));
        }
    }

    fn ends_with_input_open(&self) {
        self.ended
            .recv_timeout(Duration::from_secs(5))
            .expect("serving went on with its input still open")
            .unwrap();
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

fn shutdown(id: &str, payload: Value) -> Value {
    request("endpoint.shutdown.request", id, payload)
}

fn running(serving: &mut Serving) -> String {
    serving.send(request(
        "session.open.request",
        "o",
        json!({"session_id": "s"}),
    ));
    serving.send(request(
        "session.message.submit.request",
        "m",
        json!({"session_id": "s", "delivery": "auto", "messages": [{"role": "user", "content": "run"}]}),
    ));
    serving.answer("m")["run_id"].as_str().unwrap().to_owned()
}

#[test]
fn a_shutdown_with_nothing_running_answers_stopping_and_ends_serving() {
    let mut serving = Serving::start();
    serving.send(shutdown("x", json!({})));
    let answer = serving.answer("x");
    assert_eq!(answer["type"], "endpoint.shutdown.response", "{answer}");
    assert_eq!(answer["payload"]["outcome"], "stopping");
    serving.ends_with_input_open();
}

#[test]
fn a_graceful_shutdown_asks_then_drains_refusing_new_work_until_its_run_ends() {
    let mut serving = Serving::start();
    let run = running(&mut serving);
    serving.send(shutdown("ask", json!({})));
    let chosen = serving.answer("ask");
    assert_eq!(chosen["payload"]["outcome"], "choose", "{chosen}");
    assert_eq!(chosen["payload"]["active_runs"][0]["run_id"], run.as_str());
    serving.send(shutdown("g", json!({"mode": "graceful"})));
    assert_eq!(serving.answer("g")["payload"]["outcome"], "draining");
    serving.send(request(
        "session.open.request",
        "late",
        json!({"session_id": "t"}),
    ));
    assert_eq!(
        serving.answer("late")["payload"]["error"]["code"],
        "shutting_down"
    );
    assert_eq!(
        serving.ended.recv_timeout(Duration::from_millis(200)).err(),
        Some(RecvTimeoutError::Timeout)
    );
    serving.send(request(
        "run.cancel.request",
        "c",
        json!({"session_id": "s", "run_id": run}),
    ));
    serving.wait_for(|line| line["type"] == "run.cancelled");
    serving.ends_with_input_open();
}

#[test]
fn a_forced_shutdown_stops_at_once_over_a_live_run() {
    let mut serving = Serving::start();
    running(&mut serving);
    serving.send(shutdown("f", json!({"mode": "force"})));
    assert_eq!(serving.answer("f")["payload"]["outcome"], "stopping");
    serving.ends_with_input_open();
}

#[test]
fn a_shutdown_naming_a_mode_it_does_not_know_is_refused_and_changes_nothing() {
    let mut serving = Serving::start();
    serving.send(shutdown("bad", json!({"mode": "later"})));
    assert_eq!(
        serving.answer("bad")["payload"]["error"]["code"],
        "invalid_payload"
    );
    serving.send(request(
        "session.open.request",
        "o",
        json!({"session_id": "s"}),
    ));
    assert_eq!(serving.answer("o")["type"], "session.open.response");
}
