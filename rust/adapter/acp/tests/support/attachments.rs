use std::future::Future;
use std::path::Path;
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::task::noop_waker;
use oxap_adapter::{Adapter as _, Error, OpenRequest};
use oxap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oxap_protocol::Participant;
use serde_json::{json, Value};

pub fn wait<T>(future: impl Future<Output = T>) -> T {
    let mut future = Box::pin(future);
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        if let Poll::Ready(value) = future
            .as_mut()
            .poll(&mut Context::from_waker(&noop_waker()))
        {
            return value;
        }
        assert!(
            Instant::now() < deadline,
            "native attachment operation did not settle"
        );
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn failure(error: Error) -> Value {
    let raw = error.to_string();
    match error {
        Error::UnsupportedControl(control) => {
            json!({"feature":control.feature,"reason":control.reason,"tool":control.tool,"field":control.field,"source":control.source,"detail":control.detail,"error_raw":raw})
        }
        Error::InvalidParticipant => json!({"kind":"invalid_participant","error_raw":raw}),
        Error::InvalidResolution => json!({"kind":"invalid_resolution","error_raw":raw}),
        _ => json!({"kind":"other","error_raw":raw}),
    }
}

fn reaped(directory: &Path) {
    if let Ok(pids) = std::fs::read_to_string(directory.join("pids")) {
        for pid in pids.lines() {
            assert!(
                !std::process::Command::new("/bin/kill")
                    .args(["-0", pid])
                    .output()
                    .unwrap()
                    .status
                    .success(),
                "native {pid} survived cleanup"
            );
        }
    }
}

pub fn run(input: &Value) -> Value {
    let directory = Path::new(input["case"].as_str().unwrap());
    let adapter = Adapter::new(Config {
        mcp_servers: serde_json::from_value(input.get("configured").cloned().unwrap_or(json!([])))
            .unwrap(),
        process: ProcessConfig {
            path: input["python"].as_str().unwrap().into(),
            args: vec![
                "-u".into(),
                input["agent"].as_str().unwrap().into(),
                directory.to_string_lossy().into(),
            ],
            dir: "/".into(),
            env: Some(
                serde_json::from_value(input.get("environment").cloned().unwrap_or(json!([])))
                    .unwrap(),
            ),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let descriptor = wait(adapter.probe()).unwrap();
    let requests = input
        .get("requests")
        .and_then(Value::as_array)
        .cloned()
        .unwrap_or_else(|| vec![input.clone()]);
    let mut results = Vec::new();
    for item in requests {
        let request = OpenRequest {
            session_id: item["session_id"].as_str().unwrap_or("session").into(),
            participant: Participant {
                id: (if item["no_participant"] == true {
                    ""
                } else {
                    "user"
                })
                .into(),
                ..Participant::default()
            },
            reopen: item["reopen"].as_bool().unwrap_or(false),
            native_session_id: item["native_id"].as_str().unwrap_or("native").into(),
            tool_sources: serde_json::from_value(
                item.get("attachments").cloned().unwrap_or(json!([])),
            )
            .unwrap(),
            tools: serde_json::from_value(item.get("tools").cloned().unwrap_or(json!([]))).unwrap(),
            reasoning_level: item["reasoning"].as_str().unwrap_or_default().into(),
            ..OpenRequest::default()
        };
        let admitted = wait(adapter.admitter().unwrap().admit(&request))
            .err()
            .map(failure);
        let mut opening = adapter.open(request);
        if item["cancel"] == true {
            let deadline = Instant::now() + Duration::from_secs(10);
            while !directory.join("opening").exists() {
                assert!(opening
                    .as_mut()
                    .poll(&mut Context::from_waker(&noop_waker()))
                    .is_pending());
                assert!(Instant::now() < deadline);
                std::thread::sleep(Duration::from_millis(1));
            }
            let began = Instant::now();
            drop(opening);
            assert!(began.elapsed() < Duration::from_secs(3));
            reaped(directory);
            results.push(json!({"admit":admitted,"open":{"cancelled":true}}));
            continue;
        }
        let opened = match wait(opening) {
            Ok(session) => {
                let state = wait(session.state()).unwrap();
                let native_id = session.native_session_id();
                for source in &state.sources {
                    let value = serde_json::to_value(source).unwrap();
                    for key in ["command", "args", "environment"] {
                        assert!(value.get(key).is_none(), "attachment-only field leaked");
                    }
                }
                wait(session.close()).unwrap();
                wait(session.close()).unwrap();
                json!({"state":state,"native_id":native_id})
            }
            Err(error) => json!({"failure":failure(error)}),
        };
        reaped(directory);
        results.push(json!({"admit":admitted,"open":opened}));
    }
    let after = wait(adapter.probe()).unwrap();
    assert_eq!(
        serde_json::to_value(&descriptor.capabilities).unwrap(),
        serde_json::to_value(&after.capabilities).unwrap()
    );
    json!({"descriptor":descriptor.capabilities,"revision":descriptor.capability_revision,"results":results,"spawned":directory.join("pids").exists()})
}
