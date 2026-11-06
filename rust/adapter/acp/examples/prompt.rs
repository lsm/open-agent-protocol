use std::future::Future;
use std::io::{self, Write};
use std::sync::{Arc, Mutex};
use std::task::Context;
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker};
use oap_adapter::jsonrpc::Inbound;
use oap_adapter_acp::native::{ContentBlock, PromptParams, SessionNewParams};
use oap_adapter_acp::process::{Process, ProcessConfig};
use oap_adapter_acp::PromptBridge;

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: args.next().unwrap(),
            args: args.collect(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(250)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let observed = Arc::new(Mutex::new(Vec::new()));
    let events = observed.clone();
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        let event = match message {
            Inbound::Notification(message) => serde_json::json!({
                "method":message.method, "params":message.params,
            }),
            Inbound::Request(request) => {
                request
                    .respond(&serde_json::json!({"accepted":true}))
                    .unwrap();
                let id: serde_json::Value = serde_json::from_str(&request.id.to_string()).unwrap();
                serde_json::json!({"id":id, "method":request.method, "params":request.params})
            }
            Inbound::Barrier(_) => unreachable!(),
        };
        events.lock().unwrap().push(event);
    }));
    let opened = block_on(
        bridge.call(
            "session/new".into(),
            Some(
                serde_json::value::to_raw_value(&SessionNewParams {
                    cwd: "/".into(),
                    mcp_servers: Vec::new(),
                })
                .unwrap(),
            ),
            None,
        ),
    )
    .unwrap();
    let opened: serde_json::Value = serde_json::from_str(opened.get()).unwrap();
    let mut call = Box::pin(bridge.prompt(
        PromptParams {
            session_id: opened["sessionId"].as_str().unwrap().into(),
            prompt: vec![ContentBlock {
                kind: "text".into(),
                text: "synthetic prompt".into(),
                ..ContentBlock::default()
            }],
        },
        None,
    ));
    let admitted = block_on(call.admitted());
    println!("{}", serde_json::json!({"admitted":admitted.is_ok()}));
    io::stdout().flush().unwrap();
    let mut pending = None;
    let mut poll_us = None;
    let mut cancel_ms = None;
    let mut result = None;
    let mut error = None;
    let mut closed = None;
    if matches!(mode.as_str(), "cancel" | "close") {
        let waker = noop_waker();
        let began = Instant::now();
        pending = Some(
            call.as_mut()
                .poll(&mut Context::from_waker(&waker))
                .is_pending(),
        );
        poll_us = Some(began.elapsed().as_micros());
        let mut command = String::new();
        io::stdin().read_line(&mut command).unwrap();
        if mode == "close" {
            closed = Some(block_on(bridge.clone().close()));
            error = block_on(call).err().map(|error| error.to_string());
        } else {
            let began = Instant::now();
            drop(call);
            cancel_ms = Some(began.elapsed().as_millis());
        }
    } else {
        match block_on(call) {
            Ok(value) => result = Some(value),
            Err(value) => error = Some(value.to_string()),
        }
    }
    let before_close = observed.lock().unwrap().clone();
    let closed = closed.unwrap_or_else(|| block_on(bridge.clone().close()));
    println!(
        "{}",
        serde_json::json!({
            "result":result, "error":error, "events":before_close,
            "pending":pending, "poll_us":poll_us, "cancel_ms":cancel_ms,
            "close_error":closed.err().map(|error| error.to_string()),
            "dispatcher_released":Arc::strong_count(&observed)==1,
        })
    );
}
