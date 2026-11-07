use std::io::Read;
use std::path::Path;
use std::time::{Duration, Instant};

use futures::{executor::block_on, FutureExt, StreamExt};
use oap_adapter::{Adapter as _, Error, OpenRequest, SubmitRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{Message, MessageContent, MessageSubmitRequest, Participant, RunCancelResponse};
use serde_json::{json, Value};

fn wait(path: &Path) {
    let deadline = Instant::now() + Duration::from_secs(10);
    while !path.exists() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(1));
    }
    assert!(path.exists(), "missing {}", path.display());
}

fn control(result: Result<RunCancelResponse, Error>) -> Value {
    match result {
        Ok(response) => serde_json::to_value(response).unwrap(),
        Err(Error::RunAlreadyTerminal { run_id, status }) => {
            json!({"error":"run_terminal","run_id":run_id,"status":status})
        }
        Err(error) => panic!("cancel: {error}"),
    }
}

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let input: Value = serde_json::from_str(&input).unwrap();
    let directory = Path::new(input["case"].as_str().unwrap());
    let mode = input["mode"].as_str().unwrap();
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: input["path"].as_str().unwrap().into(),
            args: serde_json::from_value(input["args"].clone()).unwrap(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let session = block_on(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    let (admission, mut stream) = block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                id: Some("input-message".into()),
                role: "user".into(),
                content: MessageContent::from_text("synthetic"),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap();
    let run = admission.run_id.clone().unwrap();
    let mut events = Vec::new();
    for _ in 0..if mode == "gates" { 4 } else { 2 } {
        events.push(block_on(stream.next()).unwrap().unwrap());
    }
    let cancel = block_on(session.cancel(run.clone())).unwrap();
    let cancelling = block_on(session.state()).unwrap();
    if mode == "gates" {
        std::fs::write(directory.join("late"), b"go").unwrap();
        wait(&directory.join("late-answer.json"));
    }
    std::fs::write(directory.join("stream"), b"go").unwrap();
    loop {
        let event = block_on(stream.next()).unwrap().unwrap();
        let after = event.envelope_type.as_str() == "content.delta"
            && event
                .decode_payload::<oap_protocol::ContentDeltaPayload>()
                .unwrap()
                .part
                .text
                == "after";
        events.push(event);
        if after {
            break;
        }
    }
    let duplicate = block_on(session.cancel(run.clone())).unwrap();
    let still_running = block_on(session.state()).unwrap();
    let pending = stream.next().now_or_never().is_none();
    assert!(pending);
    std::fs::write(directory.join("finish"), b"go").unwrap();
    events.extend(
        block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap(),
    );
    if mode == "exit" {
        std::fs::write(directory.join("exit"), b"go").unwrap();
        let pid = std::fs::read_to_string(directory.join("pid")).unwrap();
        let deadline = Instant::now() + Duration::from_secs(3);
        while std::process::Command::new("/bin/kill")
            .args(["-0", pid.trim()])
            .output()
            .unwrap()
            .status
            .success()
            && Instant::now() < deadline
        {
            std::thread::sleep(Duration::from_millis(1));
        }
        assert!(!std::process::Command::new("/bin/kill")
            .args(["-0", pid.trim()])
            .output()
            .unwrap()
            .status
            .success());
    }
    let retry = control(block_on(session.cancel(run)));
    let state = block_on(session.state()).unwrap();
    block_on(session.close()).unwrap();
    println!(
        "{}",
        json!({
            "admission":admission,"events":events,"cancel":cancel,"cancelling":cancelling,
            "duplicate":duplicate,"still_running":still_running,"pending":pending,"retry":retry,"state":state,
            "descriptor":adapter.describe().capabilities,"revision":adapter.describe().capability_revision
        })
    );
}
