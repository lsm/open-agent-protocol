#![cfg(unix)]

use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::time::{Duration, Instant};

use oap_conformance::{Client, ClientError, ControlFrame};
use oap_protocol::{Envelope, EnvelopeType};
use serde_json::json;

static NEXT: AtomicU64 = AtomicU64::new(0);

struct Readiness(std::path::PathBuf);

impl Drop for Readiness {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn client(script: &str, within: Duration) -> Client {
    client_starting(script, within, Duration::ZERO)
}

fn client_starting(script: &str, within: Duration, startup: Duration) -> Client {
    let fixture = Readiness(std::env::temp_dir().join(format!(
        "oap-conformance-ready-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    let ready = fixture.0.join("ready");
    let body = script.strip_prefix(PRELUDE).unwrap();
    let mut command = Command::new("python3");
    command.args([
        "-u",
        "-c",
        &format!(
            "import time\ntime.sleep({})\n{PRELUDE}from pathlib import Path\nPath({}).write_text('ready')\n{body}",
            startup.as_secs_f64(),
            serde_json::to_string(&ready).unwrap()
        ),
    ]);
    let start = Instant::now();
    let client = Client::spawn(&mut command, within).unwrap();
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match std::fs::read_to_string(&ready) {
            Ok(token) if token == "ready" => break,
            Err(err) if err.kind() != std::io::ErrorKind::NotFound => panic!("{err}"),
            _ => {}
        }
        assert!(
            Instant::now() < deadline,
            "synthetic peer never became ready"
        );
        std::thread::sleep(Duration::from_millis(5));
    }
    assert!(start.elapsed() >= startup);
    println!(
        "fixture ready: injected_startup_ms={} readiness_ms={}",
        startup.as_millis(),
        start.elapsed().as_millis()
    );
    client
}

fn request(id: &str) -> Envelope {
    Envelope::new(EnvelopeType::CAPABILITIES_REQUEST, id, &json!({})).unwrap()
}

const PRELUDE: &str = "import json,sys,time\ndef emit(id,reply=''):\n print(json.dumps({'protocol':'open-agent-protocol','type':'capabilities.response','id':id,'in_reply_to':reply,'payload':{}}),flush=True)\n";

#[test]
fn replies_events_and_controls_are_kept_until_their_caller_asks_for_them() {
    let script = format!("{PRELUDE}sys.stdin.readline()\nsys.stdin.readline()\nemit('event')\nprint('{{\"control\":\"replay.accepted\",\"id\":\"ctl\"}}',flush=True)\nemit('a-response','a')\nemit('b-response','b')\n");
    let mut client = client(&script, Duration::from_secs(2));
    client.send(request("a")).unwrap();
    client.send(request("b")).unwrap();
    assert_eq!(
        client.response(&"b".into()).unwrap().id.as_str(),
        "b-response"
    );
    assert_eq!(
        client.response(&"a".into()).unwrap().id.as_str(),
        "a-response"
    );
    assert_eq!(client.event().unwrap().id.as_str(), "event");
    assert_eq!(client.control("ctl").unwrap().control, "replay.accepted");
    let ids: Vec<String> = client
        .transcript()
        .iter()
        .map(|e| e.id.to_string())
        .collect();
    assert_eq!(ids, ["a", "b", "event", "a-response", "b-response"]);
    client.close_input();
    assert!(client.wait().unwrap().success());
}

#[test]
fn chatty_events_cannot_renew_a_response_budget() {
    let script = format!("{PRELUDE}sys.stdin.readline()\nend=time.monotonic()+3\ni=0\nwhile time.monotonic()<end:\n emit(str(i))\n i+=1\n time.sleep(.005)\n");
    let mut client = client(&script, Duration::from_secs(1));
    client.send(request("missing")).unwrap();
    client.event().unwrap();
    let start = Instant::now();
    assert!(matches!(
        client.response(&"missing".into()),
        Err(ClientError::Timeout { .. })
    ));
    assert!(start.elapsed() >= Duration::from_secs(1));
    assert!(start.elapsed() < Duration::from_secs(2));
    assert!(client.transcript().len() > 3);
    assert!(client.closed_by_runner().unwrap().contains("1s"));
}

#[test]
fn unrelated_answers_cannot_renew_an_event_budget() {
    let script = format!("{PRELUDE}sys.stdin.readline()\nemit('ready','start')\nend=time.monotonic()+3\ni=0\nwhile time.monotonic()<end:\n emit(str(i),'nobody')\n i+=1\n time.sleep(.005)\n");
    let mut client = client(&script, Duration::from_secs(1));
    client.send(request("start")).unwrap();
    client.response(&"start".into()).unwrap();
    let start = Instant::now();
    assert!(matches!(client.event(), Err(ClientError::Timeout { .. })));
    assert!(start.elapsed() >= Duration::from_secs(1));
    assert!(start.elapsed() < Duration::from_secs(2));
    assert!(client.transcript().len() > 3);
}

#[test]
fn an_unanswered_control_uses_one_budget_and_leaves_the_endpoint_usable() {
    let script = format!("{PRELUDE}sys.stdin.readline()\nend=time.monotonic()+1.2\ni=0\nwhile time.monotonic()<end:\n print(json.dumps({{'control':'replay.accepted','id':str(i)}}),flush=True)\n i+=1\n time.sleep(.005)\nsys.stdin.readline()\nemit('resumed','resume')\n");
    let mut client = client(&script, Duration::from_secs(1));
    client
        .send_control(ControlFrame {
            control: "replay".into(),
            id: "missing".into(),
            ..ControlFrame::default()
        })
        .unwrap();
    let start = Instant::now();
    assert!(matches!(
        client.control("missing"),
        Err(ClientError::ControlUnanswered)
    ));
    assert!(start.elapsed() >= Duration::from_secs(1));
    assert!(start.elapsed() < Duration::from_secs(2));
    assert!(client.closed_by_runner().is_none());
    client.send(request("resume")).unwrap();
    assert_eq!(
        client.response(&"resume".into()).unwrap().id.as_str(),
        "resumed"
    );
}

#[test]
fn a_probe_and_its_answer_are_omitted_from_the_trace() {
    let script = format!("{PRELUDE}sys.stdin.readline()\nemit('refusal','probe')\n");
    let mut client = client(&script, Duration::from_secs(2));
    client.probe(request("probe")).unwrap();
    assert_eq!(
        client.response(&"probe".into()).unwrap().id.as_str(),
        "refusal"
    );
    assert!(client.transcript().is_empty());
}

#[test]
fn a_chatty_endpoint_that_ignores_eof_gets_one_drain_budget() {
    chatty_eof(Duration::ZERO);
}

#[test]
fn a_slow_starting_chatty_endpoint_still_gets_one_drain_budget() {
    chatty_eof(Duration::from_secs(2));
}

fn chatty_eof(startup: Duration) {
    let script = format!("{PRELUDE}sys.stdin.read()\nend=time.monotonic()+3\ni=0\nwhile time.monotonic()<end:\n emit(str(i))\n i+=1\n time.sleep(.005)\n");
    let mut client = client_starting(&script, Duration::from_secs(1), startup);
    client.close_input();
    let start = Instant::now();
    assert!(
        matches!(client.wait(), Err(ClientError::Timeout { within, .. }) if within == Duration::from_secs(1))
    );
    assert!(start.elapsed() >= Duration::from_secs(1));
    assert!(start.elapsed() < Duration::from_secs(2));
    let frames = client.transcript().len();
    println!(
        "chatty EOF: injected_startup_ms={} drain_ms={} frames={frames}",
        startup.as_millis(),
        start.elapsed().as_millis()
    );
    assert!(frames > 2);
}

#[test]
fn a_zero_deadline_selects_the_default_and_can_wait_for_a_delayed_answer() {
    let script = format!("{PRELUDE}sys.stdin.readline()\ntime.sleep(.05)\nemit('answer','a')\n");
    let mut client = client(&script, Duration::ZERO);
    client.send(request("a")).unwrap();
    assert_eq!(client.response(&"a".into()).unwrap().id.as_str(), "answer");
}

#[test]
fn a_control_wait_is_capped_at_ten_seconds_even_with_a_longer_line_deadline() {
    let script = format!("{PRELUDE}emit('ready')\ntime.sleep(30)\n");
    let mut client = client(&script, Duration::from_secs(20));
    client.event().unwrap();
    let start = Instant::now();
    assert!(matches!(
        client.control("missing"),
        Err(ClientError::ControlUnanswered)
    ));
    assert!(start.elapsed() >= Duration::from_secs(10));
    assert!(start.elapsed() < Duration::from_secs(12));
    assert!(client.closed_by_runner().is_none());
}
