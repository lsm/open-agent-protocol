mod support;

use std::time::Duration;

use oap_adapter::gojson::go_marshal;
use oap_adapter::testkit::drain;
use support::{opened, submission, Fake};

const STREAMING: &str = r#"{"status":"streaming"}"#;

fn traced(steps: &[(&str, &str)], hang_up: bool) -> Vec<String> {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    for (index, (kind, payload)) in steps.iter().enumerate() {
        fake.event(kind, index as i64 + 2, payload);
    }
    if hang_up {
        fake.hang_up();
    }
    drain(&mut stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&serde_json::to_string(envelope).unwrap()))
        .collect()
}

#[test]
fn each_tool_lifecycle_maps_to_the_envelopes_go_emits() {
    for (name, steps, want) in [
        (
            "tool",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx","args":{"b":1e2,"a":"<x>","n":{"z":1.50,"y":null,"big":12345678901234567890},"l":[1,true,"s"]},"args_text":"x","preview":"p","labels":["k"]}"#,
                ),
                (
                    r#"tool.complete"#,
                    r#"{"tool_id":"t1","name":"terminal","args":{},"duration_s":0.5,"result":{"ok":true,"v":1.0, "s":"<&>"},"summary":"s","result_text":"r"}"#,
                ),
                (
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":{"a":"\u003cx\u003e","b":100,"l":[1,true,"s"],"n":{"big":12345678901234567000,"y":null,"z":1.5}}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":{"ok":true,"v":1.0,"s":"\u003c\u0026\u003e"}},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "noargs",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (r#"tool.complete"#, r#"{"tool_id":"t1","name":"terminal"}"#),
                (
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "nullargs",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx","args":null}"#,
                ),
                (
                    r#"tool.complete"#,
                    r#"{"tool_id":"t1","name":"terminal","args":null,"result":null}"#,
                ),
                (
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "scalarresult",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx","args":{}}"#,
                ),
                (
                    r#"tool.complete"#,
                    r#"{"tool_id":"t1","name":"terminal","result":"text"}"#,
                ),
                (
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":{}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":"text"},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "samename",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (r#"tool.complete"#, r#"{"tool_id":"t1","name":"terminal"}"#),
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t2","name":"terminal","context":"ctx"}"#,
                ),
                (r#"tool.complete"#, r#"{"tool_id":"t2","name":"terminal"}"#),
                (
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-i","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-h","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-h","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-j","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-h","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":6,"timestamp_ms":8,"in_reply_to":"event-i","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-h","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-k","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-h","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":7,"timestamp_ms":9,"in_reply_to":"event-j","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-h","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-l","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":8,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "duplicate",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_tool_lifecycle","message":"duplicate tool call"}},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "reusedafter",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (r#"tool.complete"#, r#"{"tool_id":"t1","name":"terminal"}"#),
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_tool_lifecycle","message":"duplicate tool call"}},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "unmatched",
            vec![(r#"tool.complete"#, r#"{"tool_id":"t9","name":"terminal"}"#)],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_tool_lifecycle","message":"unmatched tool completion"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "twice",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (r#"tool.complete"#, r#"{"tool_id":"t1","name":"terminal"}"#),
                (r#"tool.complete"#, r#"{"tool_id":"t1","name":"terminal"}"#),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_tool_lifecycle","message":"unmatched tool completion"}},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "unfinished",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t2","name":"terminal","context":"ctx"}"#,
                ),
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t3","name":"terminal","context":"ctx"}"#,
                ),
                (r#"tool.complete"#, r#"{"tool_id":"t2","name":"terminal"}"#),
                (
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-h","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":4,"timestamp_ms":6,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-i","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":5,"timestamp_ms":7,"in_reply_to":"event-h","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-k","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-j","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":6,"timestamp_ms":8,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-j","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-l","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-j","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":7,"timestamp_ms":9,"in_reply_to":"event-k","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-j","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.completed","id":"event-m","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","result":null},"sequence":8,"timestamp_ms":10,"in_reply_to":"event-i","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-n","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":9,"timestamp_ms":11,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-o","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-j","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":10,"timestamp_ms":12,"in_reply_to":"event-l","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-j","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-p","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":11,"timestamp_ms":13,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "unfinishedfail",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t2","name":"terminal","context":"ctx"}"#,
                ),
                (
                    r#"message.complete"#,
                    r#"{"text":"","status":"error","error":"boom"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-h","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":4,"timestamp_ms":6,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-i","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":5,"timestamp_ms":7,"in_reply_to":"event-h","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-j","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":6,"timestamp_ms":8,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-k","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":7,"timestamp_ms":9,"in_reply_to":"event-i","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-l","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_error","message":"boom"}},"sequence":8,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "unfinishedchild",
            vec![
                (
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
                ),
                (r#"message.complete"#, r#"{"text":"sub"}"#),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_settlement","message":"child-mirror settlement on the parent stream"}},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "hangup",
            vec![(
                r#"tool.start"#,
                r#"{"tool_id":"t1","name":"terminal","context":"ctx"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-g","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":4,"timestamp_ms":6,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_process_exit","message":"EOF"},"settled_by":"inferred"},"sequence":5,"timestamp_ms":7,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
    ] {
        assert_eq!(traced(&steps, name == "hangup"), want, "{name}");
    }
}
