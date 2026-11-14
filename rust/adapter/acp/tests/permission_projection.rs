use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{mpsc, Arc, Mutex};
use std::time::{Duration, SystemTime, UNIX_EPOCH};

use futures::{executor::block_on, StreamExt};
use oxap_adapter::jsonrpc::Inbound;
use oxap_adapter::InteractionResolution;
use oxap_adapter_acp::{
    native::PromptParams,
    process::{Process, ProcessConfig},
    PromptBridge, TextRun, TextRunContext,
};
use oxap_protocol::{PermissionRequestedPayload, PermissionResolveRequest};
use serde_json::json;

static NEXT: AtomicU64 = AtomicU64::new(0);
struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}

fn permission_case(mode: &str) {
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-permission-engine-{}-{stamp}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    let script = r#"read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native"}}'; read -r request; printf '%s\n' '{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"native","toolCall":{"toolCallId":"native-tool","title":"Read"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}'; if [ "$2" = parent ]; then printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; fi; read -r reply; printf '%s\n' "$reply" > "$1"; if [ "$2" = grant ]; then printf '%s\n' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; fi; read -r eof; exit 0"#;
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                fixture.0.join("reply").to_string_lossy().into(),
                mode.into(),
            ],
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
    block_on(bridge.call(
        "session/new".into(),
        Some(serde_json::value::to_raw_value(&json!({"cwd":"/","mcpServers":[]})).unwrap()),
        None,
    ))
    .unwrap();
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
    let mut call = bridge.prompt(
        PromptParams {
            session_id: "native".into(),
            ..PromptParams::default()
        },
        None,
    );
    block_on(call.admitted()).unwrap();
    let mut stream = run.lock().unwrap().start(10).unwrap();
    let request = receive.recv_timeout(Duration::from_secs(3)).unwrap();
    let params =
        oxap_adapter_acp::native::decode(request.params.as_ref().unwrap().get().as_bytes())
            .unwrap();
    run.lock()
        .unwrap()
        .permission_request(params, "user".into(), 20)
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
    if mode == "grant" {
        let ticket = run
            .lock()
            .unwrap()
            .permission_reply(&InteractionResolution {
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
            })
            .unwrap();
        block_on(bridge.respond(
            request.clone(),
            serde_json::value::to_raw_value(ticket.response()).unwrap(),
        ))
        .unwrap();
        run.lock().unwrap().commit_permission(ticket, 30).unwrap();
    }
    run.lock()
        .unwrap()
        .complete(block_on(call).unwrap(), 40)
        .unwrap();
    let tickets = run.lock().unwrap().permission_settlements();
    for ticket in tickets {
        block_on(bridge.respond(
            request.clone(),
            serde_json::value::to_raw_value(ticket.response()).unwrap(),
        ))
        .unwrap();
        run.lock().unwrap().commit_permission(ticket, 50).unwrap();
    }
    events.extend(
        block_on(stream.collect::<Vec<_>>())
            .into_iter()
            .collect::<Result<Vec<_>, _>>()
            .unwrap(),
    );
    block_on(bridge.clone().close()).unwrap();
    let values = events
        .iter()
        .map(|event| serde_json::to_value(event).unwrap())
        .collect::<Vec<_>>();
    let resolved = values
        .iter()
        .find(|event| event["type"] == "action.permission.resolved")
        .unwrap();
    assert_eq!(resolved["in_reply_to"], values[2]["id"]);
    assert_eq!(
        resolved["payload"]["outcome"],
        if mode == "grant" {
            "resolved"
        } else {
            "cancelled"
        }
    );
    assert_eq!(values.last().unwrap()["type"], "run.completed");
    assert!(
        values
            .iter()
            .position(|event| event["type"] == "action.permission.resolved")
            .unwrap()
            < values
                .iter()
                .position(|event| event["type"] == "action.call.failed")
                .unwrap()
    );
    let reply: serde_json::Value =
        serde_json::from_str(&std::fs::read_to_string(fixture.0.join("reply")).unwrap()).unwrap();
    assert_eq!(
        reply["result"]["outcome"]["outcome"],
        if mode == "grant" {
            "selected"
        } else {
            "cancelled"
        }
    );
}

#[test]
fn native_permission_resolves_through_the_public_engine() {
    permission_case("grant");
}
#[test]
fn parent_completion_settles_permission_before_tool_and_run() {
    permission_case("parent");
}

fn projector(session: &str) -> (TextRun, oxap_adapter::EventStream, InteractionResolution) {
    let mut next = 0;
    let mut run = TextRun::new(
        TextRunContext {
            session_id: session.into(),
            native_id: "native".into(),
            run_id: "run".into(),
            message_id: "message".into(),
            capability_revision: None,
        },
        move |kind| {
            next += 1;
            format!("{kind}-{next}")
        },
    );
    let mut stream = run.start(10).unwrap();
    let params=oxap_adapter_acp::native::decode(br#"{"sessionId":"native","toolCall":{"toolCallId":"tool","title":"Read"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}"#).unwrap();
    run.permission_request(params, "user".into(), 20).unwrap();
    let mut events = Vec::new();
    for _ in 0..3 {
        events.push(block_on(stream.next()).unwrap().unwrap());
    }
    let request = events
        .last()
        .unwrap()
        .decode_payload::<PermissionRequestedPayload>()
        .unwrap();
    let resolution = InteractionResolution {
        run_id: request.run_id.clone(),
        responded_by: request.responded_by.clone(),
        permission: Some(PermissionResolveRequest {
            interaction_id: request.interaction_id,
            requested_by: request.requested_by,
            responded_by: request.responded_by,
            session_id: request.session_id,
            run_id: request.run_id,
            choice_id: "allow".into(),
            granted: true,
            ..PermissionResolveRequest::default()
        }),
        ..InteractionResolution::default()
    };
    (run, stream, resolution)
}

#[test]
fn a_reply_from_another_projection_is_refused_even_when_generated_ids_match() {
    let (first, _first_stream, resolution) = projector("first");
    let (mut second, _second_stream, _) = projector("second");
    let reply = first.permission_reply(&resolution).unwrap();
    let result = second.commit_permission(reply, 30);
    assert!(
        matches!(result, Err(oxap_adapter::Error::InvalidResolution)),
        "{result:?}"
    );
}

#[test]
fn repeated_completion_preserves_the_pending_terminal_usage_snapshot() {
    let (mut run, stream, _) = projector("session");
    for count in [10, 20] {
        run.complete(
            oxap_adapter_acp::native::PromptResult {
                stop_reason: "end_turn".into(),
                usage: Some(
                    serde_json::value::to_raw_value(
                        &json!({"totalTokens":count,"inputTokens":count,"outputTokens":0}),
                    )
                    .unwrap(),
                ),
            },
            30,
        )
        .unwrap();
    }
    for reply in run.permission_settlements() {
        run.commit_permission(reply, 40).unwrap();
    }
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let terminal = serde_json::to_value(events.last().unwrap()).unwrap();
    assert_eq!(
        terminal["payload"]["usage"]["input_tokens"], 10,
        "{terminal}"
    );
    assert_eq!(
        terminal["extensions"]["com.agentclientprotocol.turn_usage"]["input_tokens"],
        10
    );
}

#[test]
fn control_answers_validate_scope_responder_choice_and_grant_before_preparation() {
    let (mut run, _stream, resolution) = projector("session");
    for case in [
        "outer-run",
        "inner-run",
        "session",
        "outer-responder",
        "inner-responder",
        "requester",
        "choice",
        "grant",
        "unknown",
        "missing",
        "input",
    ] {
        let mut candidate = resolution.clone();
        let request = candidate.permission.as_mut().unwrap();
        match case {
            "outer-run" => candidate.run_id = "other".into(),
            "inner-run" => request.run_id = "other".into(),
            "session" => request.session_id = "other".into(),
            "outer-responder" => candidate.responded_by = "other".into(),
            "inner-responder" => request.responded_by = "other".into(),
            "requester" => request.requested_by = "other".into(),
            "choice" => request.choice_id = "unknown".into(),
            "grant" => request.granted = false,
            "unknown" => request.interaction_id = "unknown".into(),
            "missing" => candidate.permission = None,
            "input" => candidate.input = Some(Default::default()),
            _ => unreachable!(),
        }
        let error = run.permission_reply(&candidate).err().expect(case);
        match case {
            "outer-responder" | "inner-responder" => assert!(
                matches!(error, oxap_adapter::Error::WrongResponder),
                "{case}"
            ),
            "unknown" => assert!(
                matches!(error, oxap_adapter::Error::InteractionNotFound),
                "{case}"
            ),
            _ => assert!(
                matches!(error, oxap_adapter::Error::InvalidResolution),
                "{case}"
            ),
        }
    }
    let reply = run.permission_reply(&resolution).unwrap();
    assert_eq!(reply.response().outcome.option_id, "allow");
    run.commit_permission(reply, 30).unwrap();
    assert!(matches!(
        run.permission_reply(&resolution),
        Err(oxap_adapter::Error::InteractionResolved)
    ));
}

#[test]
fn pending_failure_settles_the_gate_before_children_and_keeps_its_metadata() {
    let (mut run, stream, _) = projector("session");
    run.fail("observed_failure", "first", false, 30).unwrap();
    run.complete(
        oxap_adapter_acp::native::PromptResult {
            stop_reason: "end_turn".into(),
            usage: Some(
                serde_json::value::to_raw_value(
                    &json!({"totalTokens":10,"inputTokens":8,"outputTokens":2}),
                )
                .unwrap(),
            ),
        },
        40,
    )
    .unwrap();
    for reply in run.permission_settlements() {
        run.commit_permission(reply, 50).unwrap();
    }
    let events = block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let values = events
        .iter()
        .map(|e| serde_json::to_value(e).unwrap())
        .collect::<Vec<_>>();
    assert_eq!(
        values
            .iter()
            .map(|e| e["type"].as_str().unwrap())
            .collect::<Vec<_>>(),
        [
            "action.permission.resolved",
            "action.call.cancelled",
            "run.failed"
        ]
    );
    assert_eq!(
        values.last().unwrap()["payload"]["error"]["code"],
        "observed_failure"
    );
    assert!(values.last().unwrap()["payload"].get("usage").is_none());
}

#[test]
fn closing_a_projection_refuses_a_prepared_commit_without_reviving_its_stream() {
    let (mut run, stream, resolution) = projector("session");
    let reply = run.permission_reply(&resolution).unwrap();
    run.close();
    assert!(matches!(
        run.commit_permission(reply, 30),
        Err(oxap_adapter::Error::SessionClosed)
    ));
    assert!(block_on(stream.collect::<Vec<_>>()).is_empty());
    assert!(run.state().closed);
}

#[test]
fn native_usage_in_the_pending_window_reaches_the_next_run() {
    let stamp = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .unwrap()
        .as_nanos();
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-pending-usage-{}-{stamp}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    let script = r#"printf '%s' $$ > "$2"; read -r request; printf '%s\n' "$request" >> "$1"; printf '%s\n' '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"agentCapabilities":{}}}'; read -r request; printf '%s\n' "$request" >> "$1"; printf '%s\n' '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"native"}}'; read -r request; printf '%s\n' "$request" >> "$1"; printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native","update":{"sessionUpdate":"usage_update","used":100,"size":1000}}}' '{"jsonrpc":"2.0","id":"permission","method":"session/request_permission","params":{"sessionId":"native","toolCall":{"toolCallId":"tool","title":"Act"},"options":[{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}' '{"jsonrpc":"2.0","id":3,"result":{"stopReason":"end_turn"}}'; read -r request; printf '%s\n' "$request" >> "$1"; printf '%s\n' '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native","update":{"sessionUpdate":"usage_update","used":200,"size":1000}}}'; read -r request; printf '%s\n' "$request" >> "$1"; printf '%s\n' '{"jsonrpc":"2.0","id":4,"result":{}}'; read -r reply; printf '%s\n' "$reply" >> "$1"; read -r request; printf '%s\n' "$request" >> "$1"; printf '%s\n' '{"jsonrpc":"2.0","id":5,"result":{"stopReason":"end_turn"}}'; read -r eof; exit 0"#;
    let process = block_on(Process::start_async(
        ProcessConfig {
            path: "/bin/sh".into(),
            args: vec![
                "-c".into(),
                script.into(),
                "fixture".into(),
                fixture.0.join("wire").to_string_lossy().into(),
                fixture.0.join("pid").to_string_lossy().into(),
            ],
            env: Some(Vec::new()),
            queue_capacity: 1,
            write_queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_millis(200)),
            ..ProcessConfig::default()
        },
        None,
    ))
    .unwrap();
    let ids = Arc::new(AtomicU64::new(0));
    let scope = Arc::default();
    let engine = |id: &str| {
        let ids = ids.clone();
        Arc::new(Mutex::new(TextRun::new_in_scope(
            TextRunContext {
                session_id: "session".into(),
                native_id: "native".into(),
                run_id: id.into(),
                message_id: format!("message-{id}").into(),
                capability_revision: Some(oxap_adapter_acp::pin().capability_revision.clone()),
            },
            move |kind| format!("{kind}-{}", ids.fetch_add(1, Ordering::Relaxed) + 1),
            Arc::clone(&scope),
        )))
    };
    let first = engine("first");
    let active = Arc::new(Mutex::new(first.clone()));
    let receiving = active.clone();
    let (send, receive) = mpsc::channel();
    let bridge = Arc::new(PromptBridge::new(process, move |message| match message {
        Inbound::Notification(notification) => receiving
            .lock()
            .unwrap()
            .lock()
            .unwrap()
            .observe(notification, 20)
            .unwrap(),
        Inbound::Request(request) => send.send(request).unwrap(),
        Inbound::Barrier(_) => unreachable!(),
    }));
    block_on(bridge.call(
        "session/new".into(),
        Some(serde_json::value::to_raw_value(&json!({"cwd":"/","mcpServers":[]})).unwrap()),
        None,
    ))
    .unwrap();
    let first_stream = first.lock().unwrap().start(10).unwrap();
    let mut call = bridge.prompt(
        PromptParams {
            session_id: "native".into(),
            prompt: vec![oxap_adapter_acp::native::ContentBlock {
                kind: "text".into(),
                text: "synthetic".into(),
                ..Default::default()
            }],
        },
        None,
    );
    block_on(call.admitted()).unwrap();
    let request = receive.recv_timeout(Duration::from_secs(3)).unwrap();
    let params =
        oxap_adapter_acp::native::decode(request.params.as_ref().unwrap().get().as_bytes())
            .unwrap();
    first
        .lock()
        .unwrap()
        .permission_request(params, "user".into(), 20)
        .unwrap();
    let result = block_on(call).unwrap();
    first.lock().unwrap().complete(result, 30).unwrap();
    block_on(bridge.notify("fixture/pending".into(), None, None)).unwrap();
    block_on(bridge.call("fixture/barrier".into(), None, None)).unwrap();
    let replies = first.lock().unwrap().permission_settlements();
    for reply in replies {
        block_on(bridge.respond(
            request.clone(),
            serde_json::value::to_raw_value(reply.response()).unwrap(),
        ))
        .unwrap();
        first.lock().unwrap().commit_permission(reply, 40).unwrap();
    }
    let first_events = block_on(first_stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    let second = engine("second");
    *active.lock().unwrap() = second.clone();
    let second_stream = second.lock().unwrap().start(50).unwrap();
    let mut call = bridge.prompt(
        PromptParams {
            session_id: "native".into(),
            prompt: vec![oxap_adapter_acp::native::ContentBlock {
                kind: "text".into(),
                text: "synthetic".into(),
                ..Default::default()
            }],
        },
        None,
    );
    block_on(call.admitted()).unwrap();
    let result = block_on(call).unwrap();
    second.lock().unwrap().complete(result, 60).unwrap();
    let second_events = block_on(second_stream.collect::<Vec<_>>())
        .into_iter()
        .collect::<Result<Vec<_>, _>>()
        .unwrap();
    block_on(bridge.clone().close()).unwrap();
    let first_terminal = serde_json::to_value(first_events.last().unwrap()).unwrap();
    let second_terminal = serde_json::to_value(second_events.last().unwrap()).unwrap();
    let wire = std::fs::read_to_string(fixture.0.join("wire"))
        .unwrap()
        .lines()
        .map(|line| serde_json::from_str::<serde_json::Value>(line).unwrap())
        .collect::<Vec<_>>();
    let native_pid: u32 = std::fs::read_to_string(fixture.0.join("pid"))
        .unwrap()
        .parse()
        .unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", &native_pid.to_string()])
        .output()
        .unwrap()
        .status
        .success());
    println!(
        "pending-usage-proof {}",
        json!({"runs":[first_events,second_events],"wire":wire,"native_pid":native_pid})
    );
    assert_eq!(
        first_terminal["extensions"]["com.agentclientprotocol.context"]["used_tokens"],
        100
    );
    assert_eq!(
        second_terminal["extensions"]["com.agentclientprotocol.context"]["used_tokens"], 200,
        "{second_terminal}"
    );
}
