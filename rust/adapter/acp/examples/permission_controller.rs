use std::io::Read;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, OnceLock, Weak};
use std::time::Duration;

use futures::{executor::block_on, StreamExt};
use oxap_adapter::jsonrpc::Inbound;
use oxap_adapter::InteractionResolution;
use oxap_adapter_acp::{
    native::*, pin, process::*, Permissions, PromptBridge, TextRun, TextRunContext,
};
use oxap_protocol::PermissionResolveRequest;
use serde_json::{json, Value};

fn main() {
    let mut input = String::new();
    std::io::stdin().read_to_string(&mut input).unwrap();
    let input: Value = serde_json::from_str(&input).unwrap();
    let ids = Arc::new(AtomicU64::new(2));
    let next = ids.clone();
    let run = Arc::new(Mutex::new(TextRun::new(
        TextRunContext {
            session_id: "session".into(),
            native_id: "native-session".into(),
            run_id: "run-1".into(),
            message_id: "session/message-2".into(),
            capability_revision: Some(pin().capability_revision.clone()),
        },
        move |kind| format!("{kind}-{}", next.fetch_add(1, Ordering::SeqCst) + 1),
    )));
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: input["path"].as_str().unwrap().into(),
            args: serde_json::from_value(input["args"].clone()).unwrap(),
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(300)),
            client_info: Some(Implementation {
                name: "open-agent-protocol".into(),
                version: oxap_protocol::VERSION.into(),
                ..Implementation::default()
            }),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let (release, admitted) = crossbeam_channel::bounded::<()>(0);
    let done = process.client.done();
    let receiving = run.clone();
    let owner: Arc<OnceLock<Weak<Permissions>>> = Arc::default();
    let active_owner = owner.clone();
    let granted = input["granted"].as_bool().unwrap_or(true);
    let resolve = input["resolve"].as_bool().unwrap_or(false);
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        crossbeam_channel::select! {recv(done) -> _ => return,recv(admitted) -> _ => ()}
        let permissions = active_owner.get().unwrap().upgrade().unwrap();
        match message {
            Inbound::Notification(notification) => {
                block_on(permissions.observe(receiving.clone(), notification)).unwrap();
            }
            Inbound::Request(request) => {
                if let Ok(id) = block_on(permissions.clone().request(receiving.clone(), request)) {
                    if resolve {
                        block_on(permissions.resolve(
                            receiving.clone(),
                            InteractionResolution {
                                run_id: "run-1".into(),
                                responded_by: "user".into(),
                                permission: Some(PermissionResolveRequest {
                                    interaction_id: id,
                                    requested_by: "acp.v1".into(),
                                    responded_by: "user".into(),
                                    session_id: "session".into(),
                                    run_id: "run-1".into(),
                                    choice_id: "pick".into(),
                                    granted,
                                    ..PermissionResolveRequest::default()
                                }),
                                ..InteractionResolution::default()
                            },
                        ))
                        .unwrap();
                    }
                }
            }
            Inbound::Barrier(_) => unreachable!(),
        }
    }));
    let permissions = Permissions::new(bridge.clone(), "user".into(), || 1000).unwrap();
    owner.set(Arc::downgrade(&permissions)).unwrap();
    block_on(
        bridge.call(
            METHOD_SESSION_NEW.into(),
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
    let mut call = bridge.prompt(
        PromptParams {
            session_id: "native-session".into(),
            prompt: vec![ContentBlock {
                kind: "text".into(),
                text: "synthetic".into(),
                ..ContentBlock::default()
            }],
        },
        None,
    );
    block_on(call.admitted()).unwrap();
    let stream = run.lock().unwrap().start(1000).unwrap();
    ids.fetch_add(1, Ordering::SeqCst);
    drop(release);
    match block_on(call) {
        Ok(result) => block_on(permissions.clone().complete(run.clone(), result)).unwrap(),
        Err(error) => block_on(permissions.clone().prompt_error(
            run.clone(),
            error.to_string(),
            error.remote_code().is_none(),
        ))
        .unwrap(),
    }
    let closed = block_on(bridge.clone().close());
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    println!(
        "{}",
        json!({"events":events,"closeError":closed.err().map(|error|error.to_string())})
    );
}
