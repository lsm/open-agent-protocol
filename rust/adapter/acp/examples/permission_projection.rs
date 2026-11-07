use std::collections::BTreeMap;
use std::io::Read;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, OnceLock, Weak};
use std::time::Duration;

use futures::{executor::block_on, StreamExt};
use oap_adapter::jsonrpc::{Inbound, IncomingRequest};
use oap_adapter::{Error, InteractionResolution};
use oap_adapter_acp::{native::*, pin, process::*, PromptBridge, TextRun, TextRunContext};
use oap_protocol::{InteractionId, PermissionResolveRequest};
use serde_json::{json, Value};

type Requests = Arc<Mutex<BTreeMap<InteractionId, Arc<IncomingRequest>>>>;

fn settle(run: &Mutex<TextRun>, bridge: &PromptBridge, requests: &Requests) {
    let replies = run.lock().unwrap().permission_settlements();
    for reply in replies {
        let request = requests.lock().unwrap()[reply.interaction_id()].clone();
        let _ = block_on(bridge.respond(
            request,
            serde_json::value::to_raw_value(reply.response()).unwrap(),
        ));
        run.lock().unwrap().commit_permission(reply, 1000).unwrap();
    }
}

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
                version: oap_protocol::VERSION.into(),
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
    let owner: Arc<OnceLock<Weak<PromptBridge>>> = Arc::default();
    let active_owner = owner.clone();
    let requests: Requests = Arc::default();
    let pending = requests.clone();
    let granted = input["granted"].as_bool().unwrap_or(true);
    let resolve = input["resolve"].as_bool().unwrap_or(false);
    let bridge = Arc::new(PromptBridge::new(process, move |message| {
        crossbeam_channel::select! {recv(done) -> _ => return,recv(admitted) -> _ => ()}
        let bridge = active_owner.get().unwrap().upgrade().unwrap();
        match message {
            Inbound::Notification(notification) => {
                receiving
                    .lock()
                    .unwrap()
                    .observe(notification, 1000)
                    .unwrap();
                settle(&receiving, &bridge, &pending);
            }
            Inbound::Request(request) if request.method == METHOD_SESSION_REQUEST_PERMISSION => {
                let params = decode::<PermissionRequest>(
                    request
                        .params
                        .as_ref()
                        .map_or(b"null".as_slice(), |raw| raw.get().as_bytes()),
                );
                let result = match params {
                    Ok(params) => {
                        receiving
                            .lock()
                            .unwrap()
                            .permission_request(params, "user".into(), 1000)
                    }
                    Err(_) => {
                        receiving
                            .lock()
                            .unwrap()
                            .fail(
                                "acp_invalid_permission",
                                "malformed permission request",
                                false,
                                1000,
                            )
                            .unwrap();
                        Err(Error::Harness("invalid permission request".into()))
                    }
                };
                match result {
                    Ok(id) => {
                        pending.lock().unwrap().insert(id.clone(), request.clone());
                        if resolve {
                            let reply = receiving
                                .lock()
                                .unwrap()
                                .permission_reply(&InteractionResolution {
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
                                })
                                .unwrap();
                            block_on(bridge.respond(
                                request,
                                serde_json::value::to_raw_value(reply.response()).unwrap(),
                            ))
                            .unwrap();
                            receiving
                                .lock()
                                .unwrap()
                                .commit_permission(reply, 1000)
                                .unwrap();
                        }
                    }
                    Err(error) => {
                        settle(&receiving, &bridge, &pending);
                        let message = match error {
                            Error::Harness(message) => message,
                            other => other.to_string(),
                        };
                        block_on(bridge.respond_error(request, -32602, message, None)).unwrap();
                    }
                }
            }
            Inbound::Request(request) => {
                block_on(bridge.respond_error(
                    request,
                    -32601,
                    "method not supported".into(),
                    None,
                ))
                .unwrap();
            }
            Inbound::Barrier(_) => unreachable!(),
        }
    }));
    owner.set(Arc::downgrade(&bridge)).unwrap();
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
        Ok(result) => run.lock().unwrap().complete(result, 1000).unwrap(),
        Err(error) => run
            .lock()
            .unwrap()
            .prompt_error(&error.to_string(), error.remote_code().is_none(), 1000)
            .unwrap(),
    }
    settle(&run, &bridge, &requests);
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
