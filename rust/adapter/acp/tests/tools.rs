use futures::{executor::block_on, StreamExt};
use oxap_adapter::jsonrpc::NotificationMessage;
use oxap_adapter_acp::{native::PromptResult, TextRun, TextRunContext};
use serde_json::{json, Value};

fn observe(run: &mut TextRun, value: Value) {
    run.observe(
        NotificationMessage {
            method: "session/update".into(),
            params: Some(
                serde_json::value::to_raw_value(&json!({"sessionId":"native","update":value}))
                    .unwrap(),
            ),
        },
        20,
    )
    .unwrap();
}

fn project(updates: Vec<Value>, stop: &str) -> Vec<Value> {
    let mut next = 0;
    let mut run = TextRun::new(
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
    );
    let stream = run.start(10).unwrap();
    for update in updates {
        observe(&mut run, update);
    }
    run.complete(
        PromptResult {
            stop_reason: stop.into(),
            ..PromptResult::default()
        },
        30,
    )
    .unwrap();
    block_on(stream.collect::<Vec<_>>())
        .into_iter()
        .map(|event| serde_json::to_value(event.unwrap()).unwrap())
        .collect()
}

fn call(id: &str, status: &str) -> Value {
    json!({"sessionUpdate":"tool_call","toolCallId":id,"title":"Read","status":status})
}

fn patch(id: &str, status: &str) -> Value {
    json!({"sessionUpdate":"tool_call_update","toolCallId":id,"status":status})
}

#[test]
fn native_tool_progress_and_terminal_preserve_scoped_payloads() {
    let events = project(
        vec![
            json!({"sessionUpdate":"tool_call","toolCallId":"native-tool","title":"Read","name":"read_file","status":"pending","rawInput":{"path":"x"},"rawOutput":{"early":true}}),
            patch("native-tool", "in_progress"),
            json!({"sessionUpdate":"tool_call_update","toolCallId":"native-tool","title":"Read v2","name":7,"content":[{"type":"text","text":"working"}]}),
            json!({"sessionUpdate":"tool_call_update","toolCallId":"native-tool","name":"grep","status":"completed","rawOutput":{"ok":true}}),
        ],
        "end_turn",
    );
    assert_eq!(
        events
            .iter()
            .map(|e| e["type"].as_str().unwrap())
            .collect::<Vec<_>>(),
        [
            "run.started",
            "action.call.requested",
            "action.call.started",
            "action.call.progress",
            "action.call.completed",
            "run.completed"
        ]
    );
    let id = &events[1]["tool_call_id"];
    assert_ne!(id, "native-tool");
    for (index, event) in events.iter().enumerate() {
        assert_eq!(event["sequence"], index + 1);
        assert_eq!(event["session_id"], "session");
        assert_eq!(event["run_id"], "run");
    }
    for event in &events[1..5] {
        assert_eq!(event["tool_call_id"], *id);
        assert_eq!(event["payload"]["tool_call_id"], *id);
        assert_eq!(event["payload"]["requested_by"], "acp.v1");
        assert_eq!(event["payload"]["execution_owner"], "acp-agent");
    }
    for event in &events[1..3] {
        assert_eq!(event["payload"]["arguments_json"], json!({"path":"x"}));
        assert!(event["payload"].get("result").is_none());
        assert!(event["payload"].get("progress").is_none());
    }
    let progress = &events[3]["payload"];
    assert_eq!(progress["name"], "read_file");
    assert!(progress.get("arguments_json").is_none());
    assert!(progress.get("result").is_none());
    assert_eq!(
        progress["progress"],
        json!({"content":[{"type":"text","text":"working"}],"locations":null})
    );
    let terminal = &events[4]["payload"];
    assert_eq!(terminal["name"], "grep");
    assert_eq!(terminal["result"], json!({"ok":true}));
    assert!(terminal.get("arguments_json").is_none());
}

#[test]
fn direct_tool_terminals_synthesize_start_and_keep_null_members() {
    for status in ["completed", "failed"] {
        let events = project(vec![call("tool", status)], "end_turn");
        assert_eq!(events.len(), 5);
        assert_eq!(events[1]["type"], "action.call.requested");
        assert_eq!(
            events[1]["payload"].get("arguments_json"),
            Some(&Value::Null)
        );
        assert_eq!(events[2]["type"], "action.call.started");
        assert!(events[2]["payload"].get("arguments_json").is_none());
        assert_eq!(events[3]["type"], format!("action.call.{status}"));
        if status == "completed" {
            assert_eq!(events[3]["payload"].get("result"), Some(&Value::Null));
        } else {
            assert_eq!(events[3]["payload"]["error"]["code"], "tool_failed");
            assert!(events[3]["payload"].get("result").is_none());
        }
    }
}

#[test]
fn unfinished_tools_settle_in_creation_order_before_the_run() {
    for stop in ["end_turn", "refusal", "unknown"] {
        let events = project(vec![call("z", "pending"), call("a", "pending")], stop);
        let requested = events
            .iter()
            .filter(|e| e["type"] == "action.call.requested")
            .map(|e| e["tool_call_id"].clone())
            .collect::<Vec<_>>();
        let kind = if stop == "end_turn" {
            "action.call.failed"
        } else {
            "action.call.cancelled"
        };
        let settled = events
            .iter()
            .filter(|e| e["type"] == kind)
            .map(|e| e["tool_call_id"].clone())
            .collect::<Vec<_>>();
        assert_eq!(requested.len(), 2);
        assert_eq!(settled, requested);
        assert_eq!(
            events.last().unwrap()["type"],
            if stop == "end_turn" {
                "run.completed"
            } else {
                "run.failed"
            }
        );
        if stop == "end_turn" {
            assert_eq!(events[3]["type"], "action.call.started");
            assert_eq!(events[4]["payload"]["error"]["code"], "incomplete_tool");
        } else {
            assert!(events.iter().all(|e| e["type"] != "action.call.started"));
        }
    }
}

#[test]
fn invalid_tool_observations_fail_once_and_settle_existing_children() {
    for (updates, code) in [
        (
            vec![json!({"sessionUpdate":"tool_call","toolCallId":7})],
            "acp_invalid_tool_call",
        ),
        (
            vec![json!({"sessionUpdate":"tool_call","toolCallId":"t"})],
            "acp_invalid_tool_call",
        ),
        (
            vec![json!({"sessionUpdate":"tool_call_update","toolCallId":7})],
            "acp_invalid_tool_update",
        ),
        (vec![patch("t", "completed")], "acp_tool_patch_without_call"),
        (vec![call("t", "mystery")], "acp_invalid_tool_status"),
        (
            vec![call("t", "completed"), patch("t", "in_progress")],
            "acp_tool_after_terminal",
        ),
        (
            vec![call("t", "completed"), call("t", "pending")],
            "acp_tool_after_terminal",
        ),
    ] {
        let events = project(updates, "end_turn");
        assert_eq!(events.last().unwrap()["payload"]["error"]["code"], code);
        assert_eq!(
            events.iter().filter(|e| e["type"] == "run.failed").count(),
            1
        );
        assert!(events.iter().all(|e| e["type"] != "run.completed"));
        if code == "acp_invalid_tool_status" {
            assert_eq!(events[events.len() - 2]["type"], "action.call.cancelled");
        }
    }
}
