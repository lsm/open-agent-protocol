use std::io::{self, Write};
use std::sync::{mpsc, Arc, Mutex};
use std::task::Context;
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker, StreamExt};
use oxap_adapter::jsonrpc::Inbound;
use oxap_adapter::InteractionResolution;
use oxap_adapter_acp::{native::*, process::*, Permissions, PromptBridge, TextRun, TextRunContext};
use oxap_protocol::{InteractionId, PermissionResolveRequest};
use serde_json::json;

fn command() {
    let mut line = String::new();
    io::stdin().read_line(&mut line).unwrap();
}

fn report(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
}

fn answer(id: InteractionId) -> InteractionResolution {
    InteractionResolution {
        run_id: "run".into(),
        responded_by: "user".into(),
        permission: Some(PermissionResolveRequest {
            interaction_id: id,
            requested_by: "acp.v1".into(),
            responded_by: "user".into(),
            session_id: "session".into(),
            run_id: "run".into(),
            choice_id: "allow".into(),
            granted: true,
            ..PermissionResolveRequest::default()
        }),
        ..InteractionResolution::default()
    }
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let path = args.next().unwrap();
    report(json!({"before":true}));
    command();
    let process = block_on(Process::start_async(
        ProcessConfig {
            path,
            args: args.collect(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            frame_limit: 8 * 1024 * 1024,
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let (send, receive) = mpsc::channel();
    let bridge = Arc::new(PromptBridge::new(process, move |inbound| {
        if let Inbound::Request(request) = inbound {
            send.send(request).unwrap();
        }
    }));
    let native = receive.recv_timeout(Duration::from_secs(5)).unwrap();
    let permissions = Permissions::new(bridge.clone(), "user".into(), || 1000).unwrap();
    let mut next = 0;
    let run = Arc::new(Mutex::new(TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native".into(),
            run_id: "run".into(),
            message_id: "session/message".into(),
            capability_revision: None,
        },
        move |kind| {
            next += 1;
            format!("{kind}-{next}")
        },
    )));
    let stream = run.lock().unwrap().start(1000).unwrap();
    let id = block_on(permissions.clone().request(run.clone(), native.clone())).unwrap();
    assert_eq!(
        block_on(permissions.clone().request(run.clone(), native)).unwrap_err(),
        oxap_adapter::Error::InvalidResolution
    );
    report(json!({"open":true}));
    command();
    let resolution = answer(id);
    let mut operation = if mode.starts_with("settle") {
        permissions.clone().complete(
            run.clone(),
            PromptResult {
                stop_reason: "end_turn".into(),
                ..PromptResult::default()
            },
        )
    } else {
        permissions.clone().resolve(run.clone(), resolution.clone())
    };
    let waker = noop_waker();
    let began = Instant::now();
    assert!(operation
        .as_mut()
        .poll(&mut Context::from_waker(&waker))
        .is_pending());
    let poll_us = began.elapsed().as_micros();
    report(json!({"writing":true}));
    command();
    let began = Instant::now();
    if mode == "queued-cancel" {
        let queued = permissions.clone().resolve(run.clone(), resolution);
        drop(queued);
        assert!(block_on(operation).is_err());
        let _ = block_on(bridge.clone().close());
    } else if mode.ends_with("close") {
        assert!(block_on(bridge.clone().close()).is_err());
        let result = block_on(operation);
        assert_eq!(result.is_ok(), mode.starts_with("settle"));
    } else {
        drop(operation);
        let _ = block_on(bridge.clone().close());
    }
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let types: Vec<_> = events
        .iter()
        .map(|event| event.envelope_type.as_str())
        .collect();
    assert_eq!(
        types
            .iter()
            .filter(|kind| **kind == "action.permission.resolved")
            .count(),
        1
    );
    assert_eq!(
        types.last().copied(),
        Some(if mode.starts_with("settle") {
            "run.completed"
        } else {
            "run.failed"
        })
    );
    drop(permissions);
    drop(bridge);
    report(json!({"poll_us":poll_us,"cleanup_ms":began.elapsed().as_millis(),"events":events}));
    command();
}
