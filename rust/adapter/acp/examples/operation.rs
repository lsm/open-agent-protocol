use std::future::Future;
use std::io::{self, Write};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker};
use oap_adapter_acp::process::{Process, ProcessConfig};

fn cancel<T>(operation: impl Future<Output = T>) {
    let mut operation = Box::pin(operation);
    let waker = noop_waker();
    let mut context = Context::from_waker(&waker);
    let began = Instant::now();
    let pending = matches!(operation.as_mut().poll(&mut context), Poll::Pending);
    let poll_us = began.elapsed().as_micros();
    println!("pending");
    io::stdout().flush().unwrap();
    let mut command = String::new();
    io::stdin().read_line(&mut command).unwrap();
    let began = Instant::now();
    drop(operation);
    println!(
        "{}",
        serde_json::json!({
            "pending": pending, "poll_us": poll_us, "cancel_ms": began.elapsed().as_millis(),
        })
    );
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let config = ProcessConfig {
        path: args.next().unwrap(),
        args: args.collect(),
        env: Some(Vec::new()),
        shutdown_timeout: Some(Duration::from_millis(300)),
        ..ProcessConfig::default()
    };
    match mode.as_str() {
        "start-cancel" => cancel(Process::start_async(config, None)),
        "early-cancel" => {
            drop(Process::start_async(config, None));
            println!("cancelled");
        }
        "deadline" => {
            let error = block_on(Process::start_async(
                config,
                Some(Instant::now() + Duration::from_millis(200)),
            ))
            .err()
            .unwrap();
            println!("{error}");
        }
        "call-cancel" | "complete" => {
            let process = block_on(Process::start_async(
                config,
                Some(Instant::now() + Duration::from_secs(3)),
            ))
            .unwrap();
            let operation = process.call_async(
                "session/new".into(),
                Some(
                    serde_json::value::to_raw_value(&oap_adapter_acp::native::SessionNewParams {
                        cwd: "/".into(),
                        mcp_servers: Vec::new(),
                    })
                    .unwrap(),
                ),
                None,
            );
            if mode == "call-cancel" {
                cancel(operation);
            } else {
                let (process, result) = block_on(operation).unwrap();
                let closed = process.close();
                println!(
                    "{}",
                    serde_json::json!({"result": result, "close_error": closed.err()})
                );
            }
        }
        _ => panic!("unknown mode"),
    }
}
