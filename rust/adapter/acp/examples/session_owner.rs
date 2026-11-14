use std::future::Future;
use std::io::{self, Write};
use std::task::Context;
use std::time::Duration;

use futures::{executor::block_on, task::noop_waker};
use oxap_adapter::{Adapter as _, OpenRequest};
use oxap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oxap_protocol::{Participant, SessionModelSwitchRequest, FEATURE_SESSION_MODEL_SWITCH};

fn phase(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
    let mut command = String::new();
    io::stdin().read_line(&mut command).unwrap();
}

fn cancel<T>(operation: impl Future<Output = T>) {
    let mut operation = Box::pin(operation);
    let waker = noop_waker();
    let pending = operation
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending();
    phase(serde_json::json!({"phase":"pending", "pending":pending}));
    drop(operation);
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
        ..Config::default()
    })
    .unwrap();
    phase(serde_json::json!({"phase":"booted"}));
    let session = block_on(adapter.open(OpenRequest {
        session_id: "oap-session".into(),
        participant: Participant {
            id: "fixture-user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    phase(serde_json::json!({"phase":"opened", "state":block_on(session.state()).unwrap()}));
    let mut closed = None;
    let mut switch_error = None;
    let state_error;
    match mode.as_str() {
        "drop" => {
            drop(session);
            state_error = None;
        }
        "cancel-switch" | "close-with-switch" => {
            let mut switching = Box::pin(session.model_switcher().unwrap().switch_model(
                SessionModelSwitchRequest {
                    session_id: "oap-session".into(),
                    model_id: "luna".into(),
                    allow_degraded_features: vec![FEATURE_SESSION_MODEL_SWITCH.into()],
                },
            ));
            if mode == "cancel-switch" {
                cancel(switching);
                closed = block_on(session.close())
                    .err()
                    .map(|error| error.to_string());
            } else {
                let waker = noop_waker();
                let pending = switching
                    .as_mut()
                    .poll(&mut Context::from_waker(&waker))
                    .is_pending();
                phase(serde_json::json!({"phase":"pending", "pending":pending}));
                closed = block_on(session.close())
                    .err()
                    .map(|error| error.to_string());
                switch_error = block_on(switching).err().map(|error| error.to_string());
            }
            state_error = block_on(session.state())
                .err()
                .map(|error| error.to_string());
            block_on(session.close()).unwrap();
        }
        "cancel-close" => {
            cancel(session.close());
            state_error = block_on(session.state())
                .err()
                .map(|error| error.to_string());
            block_on(session.close()).unwrap();
        }
        "close" => {
            closed = block_on(session.close())
                .err()
                .map(|error| error.to_string());
            state_error = block_on(session.state())
                .err()
                .map(|error| error.to_string());
            block_on(session.close()).unwrap();
        }
        _ => panic!("unknown lifecycle mode"),
    }
    phase(serde_json::json!({
        "phase":"cleaned", "stateError":state_error, "closeError":closed,
        "switchError":switch_error,
    }));
}
