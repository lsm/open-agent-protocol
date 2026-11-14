use std::future::Future;
use std::io::{self, Write};
use std::path::Path;
use std::pin::Pin;
use std::task::Context;
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker};
use oxap_adapter::{Adapter as _, OpenRequest};
use oxap_adapter_acp::{
    native::SessionReopenParams,
    process::{Process, ProcessConfig},
    Adapter, Config,
};
use oxap_protocol::Participant;
use serde_json::json;

fn phase(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
    let mut command = String::new();
    assert!(io::stdin().read_line(&mut command).unwrap() > 0);
}

fn pending<F: Future>(future: F, marker: &Path) -> Pin<Box<F>> {
    let mut future = Box::pin(future);
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        let began = Instant::now();
        assert!(
            future
                .as_mut()
                .poll(&mut Context::from_waker(&noop_waker()))
                .is_pending(),
            "operation settled before native ownership proof"
        );
        assert!(began.elapsed() < Duration::from_millis(50));
        if marker.exists() {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "agent never reached native reload"
        );
        std::thread::sleep(Duration::from_millis(1));
    }
    phase(json!({"phase":"pending","pending":true}));
    future
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let python = args.next().unwrap();
    let agent = args.next().unwrap();
    let directory = args.next().unwrap();
    phase(json!({"phase":"booted"}));
    let config = ProcessConfig {
        path: python,
        args: vec!["-u".into(), agent, directory.clone()],
        dir: "/".into(),
        env: Some(Vec::new()),
        queue_capacity: 1,
        shutdown_timeout: Some(Duration::from_millis(250)),
        ..ProcessConfig::default()
    };
    let marker = Path::new(&directory).join(if mode == "cancel" {
        "reverse-answered"
    } else {
        "partial.bin"
    });
    let error;
    let elapsed;
    if mode == "deadline" {
        let process = block_on(Process::start_async(config, None)).unwrap();
        let params = serde_json::value::to_raw_value(&SessionReopenParams {
            session_id: "bound-native".into(),
            cwd: "/".into(),
            ..SessionReopenParams::default()
        })
        .unwrap();
        let operation = pending(
            process.call_draining_async(
                "session/load".into(),
                Some(params),
                Some(Instant::now() + Duration::from_secs(2)),
            ),
            &marker,
        );
        let began = Instant::now();
        error = Some(
            block_on(operation)
                .err()
                .expect("quiet draining call exceeded no deadline")
                .to_string(),
        );
        elapsed = began.elapsed();
        assert!(
            error.as_ref().unwrap().contains("deadline exceeded"),
            "{error:?}"
        );
    } else {
        let adapter = Adapter::new(Config {
            process: config,
            ..Config::default()
        })
        .unwrap();
        let operation = pending(
            adapter.open(OpenRequest {
                session_id: "session".into(),
                participant: Participant {
                    id: "user".into(),
                    ..Participant::default()
                },
                reopen: true,
                native_session_id: "bound-native".into(),
                ..OpenRequest::default()
            }),
            &marker,
        );
        let began = Instant::now();
        drop(operation);
        elapsed = began.elapsed();
        error = None;
    }
    assert!(elapsed < Duration::from_secs(3));
    let pid = std::fs::read_to_string(Path::new(&directory).join("pid")).unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", &pid])
        .output()
        .unwrap()
        .status
        .success());
    phase(json!({"phase":"cleaned","error":error,"elapsed_ms":elapsed.as_millis()}));
}
