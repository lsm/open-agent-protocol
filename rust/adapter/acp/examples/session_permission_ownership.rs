use std::io::{self, Write};
use std::task::Context;
use std::time::{Duration, Instant};

use futures::{executor::block_on, task::noop_waker, StreamExt};
use oap_adapter::{Adapter as _, Error, InteractionResolution, OpenRequest, SubmitRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{
    Message, MessageContent, MessageSubmitRequest, Participant, PermissionRequestedPayload,
    PermissionResolveRequest,
};
use serde_json::json;

fn command() {
    let mut line = String::new();
    io::stdin().read_line(&mut line).unwrap();
}

fn report(value: serde_json::Value) {
    println!("{value}");
    io::stdout().flush().unwrap();
}

fn main() {
    let mut args = std::env::args().skip(1);
    let mode = args.next().unwrap();
    let path = args.next().unwrap();
    report(json!({"before":true}));
    command();
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path,
            args: args.collect(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            frame_limit: 8 * 1024 * 1024,
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        },
        abort_active_on_close: true,
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
    report(json!({"open":true}));
    command();
    let (admission, mut stream) = block_on(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
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
    let mut events = Vec::new();
    for _ in 0..3 {
        events.push(block_on(stream.next()).unwrap().unwrap());
    }
    let permission = events
        .last()
        .unwrap()
        .decode_payload::<PermissionRequestedPayload>()
        .unwrap();
    let mut resolution = if mode.starts_with("resolve")
        || (mode.starts_with("cancel") && !mode.starts_with("cancel-gate"))
    {
        let mut future = session.resolve(InteractionResolution {
            run_id: permission.run_id.clone(),
            responded_by: permission.responded_by.clone(),
            permission: Some(PermissionResolveRequest {
                interaction_id: permission.interaction_id,
                requested_by: permission.requested_by,
                responded_by: permission.responded_by,
                session_id: permission.session_id,
                run_id: permission.run_id,
                choice_id: "allow".into(),
                granted: true,
                ..PermissionResolveRequest::default()
            }),
            ..InteractionResolution::default()
        });
        let waker = noop_waker();
        let began = Instant::now();
        assert!(future
            .as_mut()
            .poll(&mut Context::from_waker(&waker))
            .is_pending());
        let poll_us = began.elapsed().as_micros();
        assert!(poll_us < 50000);
        Some((future, poll_us))
    } else {
        None
    };
    report(json!({"gate":true,"writing":resolution.is_some()}));
    command();
    if mode.starts_with("cancel") {
        let mut cancel = session.cancel(admission.run_id.clone().unwrap());
        let waker = noop_waker();
        let began = Instant::now();
        assert!(cancel
            .as_mut()
            .poll(&mut Context::from_waker(&waker))
            .is_pending());
        let poll_us = began.elapsed().as_micros();
        report(json!({"cancelling":true,"poll_us":poll_us}));
        command();
        let began = Instant::now();
        if mode.ends_with("-drop") {
            drop(cancel);
        } else {
            let _ = block_on(session.close());
            assert_eq!(block_on(cancel), Err(Error::SessionClosed));
        }
        if let Some((future, _)) = resolution.take() {
            assert!(block_on(future).is_err());
        }
        let _ = block_on(session.close());
        drop(resolution);
        drop(session);
        events.extend(
            block_on(stream.collect::<Vec<_>>())
                .into_iter()
                .collect::<Result<Vec<_>, _>>()
                .unwrap(),
        );
        assert!(!events
            .iter()
            .any(|event| event.envelope_type.as_str() == "run.cancelled"));
        if !mode.starts_with("cancel-gate") {
            assert!(!events
                .iter()
                .any(|event| event.envelope_type.as_str() == "run.status.updated"));
        }
        if mode.ends_with("-close") {
            assert_eq!(
                events.len(),
                if mode.starts_with("cancel-gate") {
                    4
                } else {
                    3
                }
            );
        }
        report(
            json!({"cleanup_ms":began.elapsed().as_millis(),"poll_us":poll_us,"events":events,"admission":admission,"descriptor":adapter.describe().capabilities}),
        );
        command();
        return;
    }
    let began = Instant::now();
    let mut poll_us = 0;
    if mode == "resolve-cancel" {
        let (future, elapsed) = resolution.take().unwrap();
        poll_us = elapsed;
        drop(future);
    }
    if mode == "drop" {
        drop(resolution);
        drop(session);
    } else {
        let _ = block_on(session.close());
        assert!(block_on(session.state()).is_err());
        if let Some((future, elapsed)) = resolution.take() {
            poll_us = elapsed;
            assert!(block_on(future).is_err());
        }
        drop(resolution);
        drop(session);
    }
    events.extend(
        block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap(),
    );
    if mode != "resolve-cancel" {
        assert_eq!(events.len(), 3);
    }
    report(
        json!({"cleanup_ms":began.elapsed().as_millis(),"poll_us":poll_us,"events":events,"admission":admission,"descriptor":adapter.describe().capabilities}),
    );
    command();
}
