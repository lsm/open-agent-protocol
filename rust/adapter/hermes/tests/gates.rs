mod support;

use std::sync::Arc;
use std::time::Duration;

use oap_adapter::gojson::go_marshal;
use oap_adapter::testkit::drain;
use oap_adapter::{EventStream, OpenRequest};
use oap_adapter_hermes::rpc::RequestId;
use oap_protocol::Participant;
use support::{go_adapter, opened, submission, Answers, Fake};

const STREAMING: &str = r#"{"status":"streaming"}"#;

struct Gates {
    fake: Arc<Fake>,
    count: usize,
    answers: Vec<Arc<Answers>>,
}

impl Gates {
    fn gate(&mut self, method: &str, payload: &str) {
        self.count += 1;
        let mut params: serde_json::Map<String, serde_json::Value> =
            serde_json::from_str(payload).unwrap();
        params.insert("session_id".into(), "sess0001".into());
        if method == "approval" && !params.contains_key("request_id") {
            params.insert(
                "request_id".into(),
                "0123456789abcdef0123456789abcdef".into(),
            );
        }
        let id = RequestId::String(format!("srq-{}", self.count));
        let params = serde_json::to_string(&params).unwrap();
        self.answers.push(self.fake.request(id, method, &params));
    }

    fn answered(&self) -> usize {
        self.answers
            .iter()
            .map(|answers| answers.codes().len())
            .sum()
    }
}

fn events(stream: &mut EventStream) -> Vec<String> {
    drain(stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&serde_json::to_string(envelope).unwrap()))
        .collect()
}

fn traced(who: &str, steps: &[(&str, &str, &str)], hang_up: bool) -> (Vec<String>, usize) {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = go_adapter(&fake)
        .open(OpenRequest {
            session_id: "session".into(),
            participant: Participant {
                id: who.into(),
                ..Participant::default()
            },
            ..OpenRequest::default()
        })
        .unwrap();
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    let mut gates = Gates {
        fake: fake.clone(),
        count: 0,
        answers: Vec::new(),
    };
    let mut seq = 2;
    for (method, kind, payload) in steps {
        if method.is_empty() {
            fake.event(kind, seq, payload);
            seq += 1;
        } else {
            gates.gate(method, payload);
        }
    }
    if hang_up {
        fake.hang_up();
    }
    (events(&mut stream), gates.answered())
}

#[test]
fn each_gate_surfaces_and_settles_as_go_s_does() {
    for (name, who, steps, want) in [
        (
            "approval",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "approvaldesc",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"","description":"only desc","choices":["deny"]}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"only desc","questions":[{"id":"choice","prompt":"only desc","kind":"single_choice","required":true,"options":[{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "clarify",
            "user",
            vec![
                (
                    r#"clarify"#,
                    r#""#,
                    r#"{"questions":[{"qid":"q1","question":"Which?","choices":["a","b"]},{"qid":"q2","question":"Many?","choices":["x","y"],"multi_select":true},{"qid":"q3","question":"Free?"},{"qid":"q4","question":"Free multi?","multi_select":true}]}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Clarification","questions":[{"id":"q1","prompt":"Which?","kind":"single_choice","required":true,"options":[{"id":"a","label":"a"},{"id":"b","label":"b"}]},{"id":"q2","prompt":"Many?","kind":"multi_choice","required":true,"options":[{"id":"x","label":"x"},{"id":"y","label":"y"}]},{"id":"q3","prompt":"Free?","kind":"text","required":true},{"id":"q4","prompt":"Free multi?","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "sudo",
            "user",
            vec![
                (r#"sudo"#, r#""#, r#"{"command":"apt install"}"#),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Password required","description":"apt install","questions":[{"id":"password","prompt":"Enter the sudo password","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "sudobare",
            "user",
            vec![
                (r#"sudo"#, r#""#, r#"{}"#),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Password required","questions":[{"id":"password","prompt":"Enter the sudo password","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "secret",
            "user",
            vec![
                (
                    r#"secret"#,
                    r#""#,
                    r#"{"env_var":"API_KEY","prompt":"Paste it","metadata":{"a":1}}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Secret required","description":"API_KEY","questions":[{"id":"value","prompt":"Paste it","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "participant",
            "alice",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"alice","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"alice","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "noparticipant",
            "",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "expire",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"request.cancel"#,
                    r#"{"id":"srq-1","method":"approval","reason":"timeout"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "expireother",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"request.cancel"#,
                    r#"{"id":"srq-9","method":"approval","reason":"timeout"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "expiretwice",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"request.cancel"#,
                    r#"{"id":"srq-1","method":"approval"}"#,
                ),
                (
                    r#""#,
                    r#"request.cancel"#,
                    r#"{"id":"srq-1","method":"approval"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "twogates",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (r#"sudo"#, r#""#, r#"{"command":"c"}"#),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"","status":"error","error":"boom"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-h","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Password required","description":"c","questions":[{"id":"password","prompt":"Enter the sudo password","kind":"text","required":true}],"allow_cancel":true},"sequence":4,"timestamp_ms":7,"session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-i","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-g","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-j","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":6,"timestamp_ms":10,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-k","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":7,"timestamp_ms":11,"in_reply_to":"event-h","session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-l","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_error","message":"boom"}},"sequence":8,"timestamp_ms":12,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "gatethentool",
            "user",
            vec![
                (
                    r#""#,
                    r#"tool.start"#,
                    r#"{"tool_id":"t1","name":"terminal","context":"c"}"#,
                ),
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (
                    r#""#,
                    r#"message.complete"#,
                    r#"{"text":"ok","status":"complete"}"#,
                ),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.requested","id":"event-e","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","arguments_json":null},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.started","id":"event-f","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal"},"sequence":3,"timestamp_ms":5,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-h","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":4,"timestamp_ms":6,"session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-i","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-g","updated_at_ms":7},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-j","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":6,"timestamp_ms":9,"in_reply_to":"event-h","session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.call.failed","id":"event-k","payload":{"session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","requested_by":"hermes.gateway","execution_owner":"hermes","name":"terminal","error":{"code":"incomplete_tool","message":"turn settled with an unfinished hermes tool"}},"sequence":7,"timestamp_ms":10,"in_reply_to":"event-f","session_id":"session","run_id":"run-b","tool_call_id":"tool-call-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-l","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":8,"timestamp_ms":11,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "invalidchoice",
            "user",
            vec![(
                r#"approval"#,
                r#""#,
                r#"{"command":"c","choices":["maybe"]}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_event","message":"invalid approval gate"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "invalidclarify",
            "user",
            vec![(r#"clarify"#, r#""#, r#"{"questions":[]}"#)],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_event","message":"invalid clarify gate"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "invalidsecret",
            "user",
            vec![(r#"secret"#, r#""#, r#"{"env_var":"","prompt":"p"}"#)],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_event","message":"invalid secret gate"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "emptychoice",
            "user",
            vec![(
                r#"clarify"#,
                r#""#,
                r#"{"questions":[{"qid":"q1","question":"Which?","choices":["a",""]}]}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-e","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_event","message":"gate with a question nobody can answer"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "childmirror",
            "user",
            vec![
                (
                    r#"approval"#,
                    r#""#,
                    r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
                ),
                (r#""#, r#"message.complete"#, r#"{"text":"sub"}"#),
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_invalid_settlement","message":"child-mirror settlement on the parent stream"}},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
        (
            "hangup",
            "user",
            vec![(
                r#"approval"#,
                r#""#,
                r#"{"command":"rm -rf <x>","description":"d","choices":["once","session","always","deny"],"tool_name":"terminal"}"#,
            )],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm -rf \u003cx\u003e","questions":[{"id":"choice","prompt":"rm -rf \u003cx\u003e","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"session","label":"session"},{"id":"always","label":"always"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_process_exit","message":"EOF"},"settled_by":"inferred"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
        ),
    ] {
        let (events, answered) = traced(who, &steps, name == "hangup");
        assert_eq!(events, want, "{name}");
        assert_eq!(answered, 0, "{name}");
    }
}

#[test]
fn a_gate_seen_before_the_turn_opened_surfaces_after_the_run_starts() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let mut gates = Gates {
        fake: fake.clone(),
        count: 0,
        answers: Vec::new(),
    };
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        gates.gate("approval", r#"{"command":"early","choices":["once"]}"#);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    fake.event(
        "message.complete",
        2,
        r#"{"text":"ok","status":"complete"}"#,
    );
    assert_eq!(
        events(&mut stream),
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"early","questions":[{"id":"choice","prompt":"early","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
        ]
    );
    assert_eq!(gates.answered(), 0);
}
