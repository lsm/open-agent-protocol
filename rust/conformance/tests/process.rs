#![cfg(unix)]

use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use oap_conformance::{Frame, Process, ProcessError};
use oap_protocol::{Envelope, EnvelopeType};
use serde_json::json;

fn python(script: &str) -> Command {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", script]);
    command
}

#[test]
fn a_process_exchanges_frames_and_exits_after_input_eof() {
    let mut process = Process::spawn(&mut python(
        "import sys\nfor line in sys.stdin: print(line, end='', flush=True)",
    ))
    .unwrap();
    let request = Envelope::new(EnvelopeType::CAPABILITIES_REQUEST, "c", &json!({})).unwrap();
    process.send(&Frame::Envelope(Box::new(request))).unwrap();
    let Some(Frame::Envelope(answer)) = process.next(Duration::from_secs(2)).unwrap() else {
        panic!("no answer");
    };
    assert_eq!(answer.id.as_str(), "c");
    process.close_input();
    assert!(process.next(Duration::from_secs(2)).unwrap().is_none());
    assert!(process.wait_exit(Duration::from_secs(2)).unwrap().success());
    process.close().unwrap();
    process.close().unwrap();
}

#[test]
fn a_receive_timeout_is_soft_and_close_stops_a_silent_peer() {
    let mut process = Process::spawn(&mut python("import time\ntime.sleep(30)")).unwrap();
    let start = Instant::now();
    assert!(matches!(
        process.next(Duration::from_millis(80)),
        Err(ProcessError::Timeout)
    ));
    assert!(start.elapsed() >= Duration::from_millis(80));
    process.close().unwrap();
    assert!(start.elapsed() < Duration::from_secs(3));
}

#[test]
fn an_endpoint_that_closes_stdout_and_stays_alive_is_killed_after_one_exit_budget() {
    let mut process =
        Process::spawn(&mut python("import os,time\nos.close(1)\ntime.sleep(30)")).unwrap();
    assert!(process.next(Duration::from_secs(2)).unwrap().is_none());
    let start = Instant::now();
    assert!(matches!(
        process.wait_exit(Duration::from_millis(80)),
        Err(ProcessError::ExitTimeout(_))
    ));
    assert!(start.elapsed() >= Duration::from_millis(80));
    assert!(start.elapsed() < Duration::from_secs(3));
}

#[test]
fn closing_a_peer_cannot_wait_for_the_host_to_drain_its_full_queue() {
    let mut process = Process::spawn(&mut python("import json,time\nfor i in range(200): print(json.dumps({'control':'replay','id':str(i)}),flush=True)\ntime.sleep(30)")).unwrap();
    assert!(matches!(
        process.next(Duration::from_secs(2)).unwrap(),
        Some(Frame::Control(_))
    ));
    std::thread::sleep(Duration::from_millis(50));
    let start = Instant::now();
    process.close().unwrap();
    assert!(start.elapsed() < Duration::from_secs(3));
}

#[test]
fn bad_output_is_reported_without_hiding_the_following_frame() {
    let process = Process::spawn(&mut python(
        "print('{',flush=True)\nprint('{\"control\":\"replay\",\"id\":\"ok\"}',flush=True)",
    ))
    .unwrap();
    assert!(matches!(
        process.next(Duration::from_secs(2)),
        Err(ProcessError::Wire(_))
    ));
    let Some(Frame::Control(answer)) = process.next(Duration::from_secs(2)).unwrap() else {
        panic!("no answer after malformed output");
    };
    assert_eq!(answer.id, "ok");
}

struct Descendant(String);

impl Drop for Descendant {
    fn drop(&mut self) {
        let _ = Command::new("kill")
            .args(["-KILL", &self.0])
            .stderr(Stdio::null())
            .status();
    }
}

#[test]
fn close_releases_the_reader_even_when_a_descendant_keeps_stdout_open() {
    let script = "import json,subprocess,sys\np=subprocess.Popen([sys.executable,'-c','import time; time.sleep(6)'],stderr=subprocess.DEVNULL)\nprint(json.dumps({'control':'held','id':str(p.pid)}),flush=True)";
    let mut process = Process::spawn(&mut python(script)).unwrap();
    let Some(Frame::Control(frame)) = process.next(Duration::from_secs(2)).unwrap() else {
        panic!("the endpoint did not identify its stdout holder");
    };
    let _descendant = Descendant(frame.id);
    assert!(process.wait_exit(Duration::from_secs(2)).unwrap().success());
    assert!(matches!(
        process.next(Duration::from_millis(20)),
        Err(ProcessError::Timeout)
    ));
    let start = Instant::now();
    process.close().unwrap();
    assert!(start.elapsed() < Duration::from_secs(2));
}
