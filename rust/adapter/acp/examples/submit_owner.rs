use std::io::{self, Write};
use std::task::Context;
use std::time::Duration;

use futures::{executor::block_on, task::noop_waker, StreamExt};
use oxap_adapter::{Adapter as _, OpenRequest, SubmitRequest};
use oxap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oxap_protocol::{Message, MessageContent, MessageSubmitRequest, Participant};
use serde_json::json;

fn phase(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
    let mut line = String::new();
    io::stdin().read_line(&mut line).unwrap();
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: args.next().unwrap(),
            args: args.collect(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(250)),
            ..ProcessConfig::default()
        },
        abort_active_on_close: mode != "default-close-admission",
        ..Config::default()
    })
    .unwrap();
    phase(json!({"phase":"booted"}));
    let session = block_on(adapter.open(OpenRequest {
        session_id: "oap-session".into(),
        participant: Participant {
            id: "fixture-user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    phase(
        json!({"phase":"opened", "state":block_on(session.state()).unwrap(), "descriptor":adapter.describe().capabilities, "revision":adapter.describe().capability_revision}),
    );
    let request = SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "oap-session".into(),
            messages: vec![Message {
                role: "user".into(),
                content: MessageContent::from_text(&if mode.contains("admission") {
                    "x".repeat(4 * 1024 * 1024)
                } else {
                    "hi".into()
                }),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    };
    let mut events = Vec::new();
    let mut errors = Vec::new();
    let mut close_error = None;
    let mut submit_error = None;
    if mode.contains("admission") {
        let mut submit = session.submit(request);
        let waker = noop_waker();
        let pending = submit
            .as_mut()
            .poll(&mut Context::from_waker(&waker))
            .is_pending();
        phase(json!({"phase":"pending", "pending":pending}));
        if mode == "default-close-admission" {
            let error = block_on(session.close()).unwrap_err();
            assert!(matches!(error, oxap_adapter::Error::RunActive));
            close_error = Some(error.to_string());
        }
        if mode == "close-admission" {
            close_error = block_on(session.close())
                .err()
                .map(|error| error.to_string());
        }
        if mode == "close-admission" {
            let error = block_on(submit)
                .err()
                .expect("closed admission was accepted");
            assert!(matches!(error, oxap_adapter::Error::SessionClosed));
            submit_error = Some(error.to_string());
        } else {
            drop(submit);
        }
        drop(session);
    } else {
        let (response, mut stream) = block_on(session.submit(request)).unwrap();
        events.push(block_on(stream.next()).unwrap().unwrap());
        phase(
            json!({"phase":"admitted", "response":response, "state":block_on(session.state()).unwrap()}),
        );
        match mode.as_str() {
            "complete" | "overflow" => {
                let deadline = std::time::Instant::now() + Duration::from_secs(10);
                while block_on(session.state()).unwrap().status.as_str() != "idle" {
                    assert!(std::time::Instant::now() < deadline);
                    std::thread::sleep(Duration::from_millis(5));
                }
                for event in block_on(stream.by_ref().collect::<Vec<_>>()) {
                    match event {
                        Ok(event) => events.push(event),
                        Err(error) => errors.push(error.to_string()),
                    }
                }
                phase(json!({"phase":"settled", "state":block_on(session.state()).unwrap()}));
                block_on(session.close()).unwrap();
                drop(session);
            }
            "drop" => drop(session),
            "abort-close" => {
                close_error = block_on(session.close())
                    .err()
                    .map(|error| error.to_string());
                drop(session);
            }
            "cancel-close" => {
                let mut close = session.close();
                let waker = noop_waker();
                let pending = close
                    .as_mut()
                    .poll(&mut Context::from_waker(&waker))
                    .is_pending();
                phase(json!({"phase":"pending", "pending":pending}));
                drop(close);
                block_on(session.close()).unwrap();
                drop(session);
            }
            _ => panic!("unknown mode"),
        }
        assert!(block_on(stream.next()).is_none());
    }
    phase(
        json!({"phase":"cleaned", "events":events, "errors":errors, "closeError":close_error, "submitError":submit_error}),
    );
}
