use std::future::Future;
use std::io::{self, Write};
use std::path::Path;
use std::task::Context;
use std::time::{Duration, Instant};

use futures::task::noop_waker;
use oap_adapter::{Adapter as _, OpenRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::Participant;
use serde_json::json;

fn phase(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
    let mut command = String::new();
    assert!(io::stdin().read_line(&mut command).unwrap() > 0);
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let python = args.next().unwrap();
    let agent = args.next().unwrap();
    let directory = args.next().unwrap();
    phase(json!({"phase":"booted"}));
    let mut config = Config {
        process: ProcessConfig {
            path: python,
            args: vec!["-u".into(), agent, directory.clone()],
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(250)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    };
    if mode == "request-partial" {
        config
            .session_config
            .insert("blob".into(), "x".repeat(2 * 1024 * 1024));
    }
    let adapter = Adapter::new(config).unwrap();
    let request = OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        reopen: mode == "reload-quiet",
        native_session_id: "native".into(),
        reasoning_level: if mode == "request-partial" {
            ""
        } else {
            "high"
        }
        .into(),
        ..OpenRequest::default()
    };
    let mut opening = adapter.open(request);
    let marker = Path::new(&directory).join(if mode.ends_with("partial") {
        "partial.bin"
    } else {
        "reverse-answered"
    });
    let deadline = Instant::now() + Duration::from_secs(10);
    let mut preparation_poll_ms = 0;
    loop {
        let began = Instant::now();
        assert!(opening
            .as_mut()
            .poll(&mut Context::from_waker(&noop_waker()))
            .is_pending());
        preparation_poll_ms = preparation_poll_ms.max(began.elapsed().as_millis());
        if marker.exists() {
            break;
        }
        assert!(
            Instant::now() < deadline,
            "native opening setting was never observed"
        );
        std::thread::sleep(Duration::from_millis(1));
    }
    let began = Instant::now();
    assert!(opening
        .as_mut()
        .poll(&mut Context::from_waker(&noop_waker()))
        .is_pending());
    let waiting_poll_ms = began.elapsed().as_millis();
    assert!(waiting_poll_ms < 50);
    phase(
        json!({"phase":"pending","pending":true,"preparation_poll_ms":preparation_poll_ms,"waiting_poll_ms":waiting_poll_ms}),
    );
    let began = Instant::now();
    drop(opening);
    let elapsed = began.elapsed();
    assert!(elapsed < Duration::from_secs(3));
    let pid = std::fs::read_to_string(Path::new(&directory).join("pid")).unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", &pid])
        .output()
        .unwrap()
        .status
        .success());
    phase(json!({"phase":"cleaned","error":null,"elapsed_ms":elapsed.as_millis()}));
}
