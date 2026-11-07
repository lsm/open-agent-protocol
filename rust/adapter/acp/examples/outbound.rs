use std::future::Future;
use std::io::{self, Write};
use std::sync::{mpsc, Arc};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker};
use oap_adapter::jsonrpc::{Inbound, IncomingRequest};
use oap_adapter_acp::process::{Process, ProcessConfig};
use oap_adapter_acp::{Operation, PromptBridge};
use serde_json::json;

fn command() {
    let mut line = String::new();
    io::stdin().read_line(&mut line).unwrap();
}

fn report(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
}

fn bridge(path: &str, args: &[String]) -> (Arc<PromptBridge>, Arc<IncomingRequest>) {
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: path.into(),
            args: args.into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let (send, receive) = mpsc::channel();
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        if let Inbound::Request(request) = message {
            send.send(request).unwrap();
        }
    }));
    let request = receive.recv_timeout(Duration::from_secs(5)).unwrap();
    (bridge, request)
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let path = args.next().unwrap();
    let args: Vec<String> = args.collect();
    report(json!({"before":true}));
    command();
    let (bridge, request) = bridge(&path, &args);
    report(json!({"open":true}));
    command();
    if mode == "complete" || mode == "error" {
        assert!(block_on(bridge.notify(String::new(), None, None)).is_err());
        assert!(
            block_on(bridge.notify("fixture/expired".into(), None, Some(Instant::now()))).is_err()
        );
        if mode == "complete" {
            block_on(bridge.respond(
                request.clone(),
                serde_json::value::to_raw_value(&json!({"accepted":true})).unwrap(),
            ))
            .unwrap();
        } else {
            block_on(bridge.respond_error(
                request.clone(),
                -32601,
                "synthetic refusal".into(),
                Some(serde_json::value::to_raw_value(&json!({"reason":"fixture"})).unwrap()),
            ))
            .unwrap();
        }
        assert!(block_on(bridge.respond(
            request,
            serde_json::value::to_raw_value(&json!({})).unwrap()
        ))
        .is_err());
        block_on(bridge.notify("fixture/done".into(), None, None)).unwrap();
        block_on(bridge.clone().close()).unwrap();
        drop(bridge);
        report(json!({"complete":true}));
    } else {
        let payload = serde_json::value::to_raw_value(&json!({
            "padding": "x".repeat(4 * 1024 * 1024)
        }))
        .unwrap();
        let operation = if mode.starts_with("reply") {
            bridge.respond(request, payload)
        } else if mode.starts_with("error") {
            bridge.respond_error(request, -32601, "synthetic refusal".into(), Some(payload))
        } else {
            let deadline =
                (mode == "notify-deadline").then(|| Instant::now() + Duration::from_secs(1));
            bridge.notify("fixture/large".into(), Some(payload), deadline)
        };
        let mut operation = Box::pin(operation);
        report(json!({"writing":true}));
        command();
        let waker = noop_waker();
        let began = Instant::now();
        assert!(operation
            .as_mut()
            .poll(&mut Context::from_waker(&waker))
            .is_pending());
        let poll_us = began.elapsed().as_micros();
        let mut full = false;
        if mode == "queue" {
            let mut waiting: Vec<Operation<()>> = vec![
                bridge.notify("fixture/queued-one".into(), None, None),
                bridge.notify("fixture/queued-two".into(), None, None),
            ];
            let deadline = Instant::now() + Duration::from_secs(3);
            while !full && Instant::now() < deadline {
                for index in 0..waiting.len() {
                    if let Poll::Ready(result) = std::pin::Pin::new(&mut waiting[index])
                        .poll(&mut Context::from_waker(&waker))
                    {
                        assert!(result
                            .unwrap_err()
                            .to_string()
                            .contains("write queue is full"));
                        drop(waiting.swap_remove(index));
                        full = true;
                        break;
                    }
                }
                std::thread::sleep(Duration::from_millis(1));
            }
            assert!(full);
            assert_eq!(waiting.len(), 1);
            drop(waiting);
        }
        let began = Instant::now();
        if mode == "notify-deadline" {
            assert!(block_on(operation)
                .unwrap_err()
                .to_string()
                .contains("deadline"));
            let _ = block_on(bridge.clone().close());
        } else if mode.ends_with("close") {
            assert!(block_on(bridge.clone().close()).is_err());
            assert!(block_on(operation).is_err());
        } else if mode.ends_with("drop") {
            drop(bridge);
            assert!(block_on(operation).is_err());
            report(
                json!({"poll_us":poll_us,"cleanup_ms":began.elapsed().as_millis(),"queue_full":full}),
            );
            command();
            return;
        } else {
            drop(operation);
            let _ = block_on(bridge.clone().close());
        }
        drop(bridge);
        report(
            json!({"poll_us":poll_us,"cleanup_ms":began.elapsed().as_millis(),"queue_full":full}),
        );
    }
    command();
}
