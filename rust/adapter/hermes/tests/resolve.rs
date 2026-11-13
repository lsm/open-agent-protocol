mod support;

use std::sync::Arc;
use std::time::Duration;

use oap_adapter::gojson::go_marshal;
use oap_adapter::testkit::drain;
use oap_adapter::{Error, InteractionResolution};
use oap_adapter_hermes::rpc::RequestId;
use oap_protocol::{InputAnswer, UserInputResolveRequest};
use support::{opened, submission, Answers, Fake};

const STREAMING: &str = r#"{"status":"streaming"}"#;

enum Step {
    Gate(&'static str, &'static str),
    Complete(&'static str),
    Cancel(&'static str),
    Resolve(
        Box<InteractionResolution>,
        Option<(&'static str, &'static str)>,
        bool,
    ),
}

fn input(id: &str, answers: Vec<InputAnswer>) -> InteractionResolution {
    InteractionResolution {
        input: Some(UserInputResolveRequest {
            interaction_id: id.into(),
            session_id: "session".into(),
            answers,
            ..UserInputResolveRequest::default()
        }),
        ..InteractionResolution::default()
    }
}

fn pick(question: &str, ids: &[&str]) -> InputAnswer {
    InputAnswer {
        question_id: question.into(),
        selected_option_ids: ids.iter().map(|id| id.to_string()).collect(),
        ..InputAnswer::default()
    }
}

fn text(question: &str, value: &str) -> InputAnswer {
    InputAnswer {
        question_id: question.into(),
        text: value.into(),
        ..InputAnswer::default()
    }
}

fn resolve(
    resolution: InteractionResolution,
    before: Option<(&'static str, &'static str)>,
    fail: bool,
) -> Step {
    Step::Resolve(Box::new(resolution), before, fail)
}

fn once() -> InteractionResolution {
    input("interaction-d", vec![pick("choice", &["once"])])
}

fn with(
    mut resolution: InteractionResolution,
    edit: impl FnOnce(&mut InteractionResolution),
) -> InteractionResolution {
    edit(&mut resolution);
    resolution
}

fn label(outcome: Result<(), Error>) -> String {
    match outcome {
        Ok(()) => "ok".into(),
        Err(Error::InteractionNotFound) => "notfound".into(),
        Err(Error::InvalidResolution) => "invalid".into(),
        Err(other) => format!("other:{other}"),
    }
}

type Traced = (Vec<String>, Vec<String>, Vec<(String, String)>);

fn traced(steps: Vec<Step>) -> Traced {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = opened(&fake);
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    let mut gates: Vec<Arc<Answers>> = Vec::new();
    let mut outcomes = Vec::new();
    let mut seq = 2;
    for step in steps {
        match step {
            Step::Gate(method, payload) => {
                let mut params: serde_json::Map<String, serde_json::Value> =
                    serde_json::from_str(payload).unwrap();
                params.insert("session_id".into(), "sess0001".into());
                if method == "approval" {
                    params.insert(
                        "request_id".into(),
                        "0123456789abcdef0123456789abcdef".into(),
                    );
                }
                let id = RequestId::String(format!("srq-{}", gates.len() + 1));
                let params = serde_json::to_string(&params).unwrap();
                gates.push(fake.request(id, method, &params));
            }
            Step::Complete(payload) => {
                fake.event("message.complete", seq, payload);
                seq += 1;
            }
            Step::Cancel(payload) => {
                fake.event("request.cancel", seq, payload);
                seq += 1;
            }
            Step::Resolve(resolution, before, fail) => {
                let target = match resolution.input.as_ref() {
                    Some(input) if input.interaction_id.as_str() == "interaction-g" => 1,
                    _ => 0,
                };
                let answers = &gates[target];
                *answers.failure.lock().unwrap() =
                    fail.then(|| "hermes rpc: client closed".to_owned());
                if let Some((kind, payload)) = before {
                    let (fake, at) = (fake.clone(), seq);
                    seq += 1;
                    *answers.before.lock().unwrap() =
                        Some(Box::new(move || fake.event(kind, at, payload)));
                }
                outcomes.push(label(session.resolve(*resolution)));
            }
        }
    }
    let events = drain(&mut stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&serde_json::to_string(envelope).unwrap()))
        .collect();
    let answered = gates
        .iter()
        .enumerate()
        .flat_map(|(index, answers)| {
            answers
                .results()
                .into_iter()
                .map(move |result| (format!("srq-{}", index + 1), go_marshal(&result)))
        })
        .collect();
    (outcomes, events, answered)
}

#[test]
fn each_resolution_answers_hermes_and_settles_the_gate_as_go_s_does() {
    for (name, steps, outcomes, events, answers) in [
        (
            "approve",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(once(), None, false),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["once"]}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"choice":"once"}"#)],
        ),
        (
            "approveby",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(
                    with(once(), |r| {
                        r.run_id = "run-b".into();
                        r.responded_by = "user".into();
                        let i = r.input.as_mut().unwrap();
                        i.run_id = "run-b".into();
                        i.responded_by = "user".into();
                        i.requested_by = "hermes.gateway".into();
                    }),
                    None,
                    false,
                ),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["once"]}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"choice":"once"}"#)],
        ),
        (
            "refusals",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(InteractionResolution::default(), None, false),
                resolve(
                    input("interaction-z", vec![pick("choice", &["once"])]),
                    None,
                    false,
                ),
                resolve(with(once(), |r| r.run_id = "run-x".into()), None, false),
                resolve(
                    with(once(), |r| {
                        r.input.as_mut().unwrap().session_id = "other".into()
                    }),
                    None,
                    false,
                ),
                resolve(
                    with(once(), |r| {
                        r.input.as_mut().unwrap().run_id = "run-x".into()
                    }),
                    None,
                    false,
                ),
                resolve(
                    with(once(), |r| r.responded_by = "alice".into()),
                    None,
                    false,
                ),
                resolve(
                    with(once(), |r| {
                        r.input.as_mut().unwrap().responded_by = "alice".into()
                    }),
                    None,
                    false,
                ),
                resolve(
                    with(once(), |r| {
                        r.input.as_mut().unwrap().requested_by = "someone".into()
                    }),
                    None,
                    false,
                ),
                resolve(
                    input(
                        "interaction-d",
                        vec![pick("choice", &["once"]), pick("choice", &["deny"])],
                    ),
                    None,
                    false,
                ),
                resolve(
                    input("interaction-d", vec![pick("choice", &["maybe"])]),
                    None,
                    false,
                ),
                resolve(
                    input("interaction-d", vec![pick("other", &["once"])]),
                    None,
                    false,
                ),
                resolve(input("interaction-d", vec![]), None, false),
                resolve(
                    with(
                        input("interaction-d", vec![pick("choice", &["deny"])]),
                        |r| r.input.as_mut().unwrap().session_id = "".into(),
                    ),
                    None,
                    false,
                ),
                resolve(once(), None, false),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec![
                "notfound", "notfound", "invalid", "invalid", "invalid", "invalid", "invalid",
                "invalid", "invalid", "invalid", "invalid", "invalid", "ok", "notfound",
            ],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["deny"]}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"choice":"deny"}"#)],
        ),
        (
            "clarify",
            vec![
                Step::Gate(
                    "clarify",
                    r#"{"questions":[{"qid":"q1","question":"One?","choices":["a","b"]},{"qid":"q2","question":"Many?","choices":["<x>","y"],"multi_select":true},{"qid":"q3","question":"Free?"}]}"#,
                ),
                resolve(
                    input("interaction-d", vec![pick("q1", &["a"])]),
                    None,
                    false,
                ),
                resolve(
                    input(
                        "interaction-d",
                        vec![
                            text("q3", "free <&>"),
                            pick("q2", &["y", "<x>"]),
                            pick("q2", &["y"]),
                        ],
                    ),
                    None,
                    false,
                ),
                resolve(
                    input(
                        "interaction-d",
                        vec![
                            text("q3", "free <&>"),
                            pick("q2", &["y", "<x>"]),
                            pick("q1", &["b"]),
                        ],
                    ),
                    None,
                    false,
                ),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["invalid", "invalid", "ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Clarification","questions":[{"id":"q1","prompt":"One?","kind":"single_choice","required":true,"options":[{"id":"a","label":"a"},{"id":"b","label":"b"}]},{"id":"q2","prompt":"Many?","kind":"multi_choice","required":true,"options":[{"id":"\u003cx\u003e","label":"\u003cx\u003e"},{"id":"y","label":"y"}]},{"id":"q3","prompt":"Free?","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"q3","text":"free \u003c\u0026\u003e"},{"question_id":"q2","selected_option_ids":["y","\u003cx\u003e"]},{"question_id":"q1","selected_option_ids":["b"]}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(
                r#"srq-1"#,
                r#"{"answers":{"q1":"b","q2":"[\"y\",\"\\u003cx\\u003e\"]","q3":"free \u003c\u0026\u003e"}}"#,
            )],
        ),
        (
            "sudo",
            vec![
                Step::Gate("sudo", r#"{"command":"c"}"#),
                resolve(
                    input("interaction-d", vec![pick("password", &["x"])]),
                    None,
                    false,
                ),
                resolve(
                    input("interaction-d", vec![text("value", "pw")]),
                    None,
                    false,
                ),
                resolve(
                    input("interaction-d", vec![text("password", "pw <&>")]),
                    None,
                    false,
                ),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["invalid", "invalid", "ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Password required","description":"c","questions":[{"id":"password","prompt":"Enter the sudo password","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"password","text":"pw \u003c\u0026\u003e"}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"value":"pw \u003c\u0026\u003e"}"#)],
        ),
        (
            "secret",
            vec![
                Step::Gate("secret", r#"{"env_var":"K","prompt":"Paste"}"#),
                resolve(
                    input("interaction-d", vec![text("value", "s3cret")]),
                    None,
                    false,
                ),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Secret required","description":"K","questions":[{"id":"value","prompt":"Paste","kind":"text","required":true}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"value","text":"s3cret"}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"value":"s3cret"}"#)],
        ),
        (
            "expired",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                Step::Cancel(r#"{"id":"srq-1","method":"approval"}"#),
                resolve(once(), None, false),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["notfound"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![],
        ),
        (
            "parked",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(
                    once(),
                    Some(("message.complete", r#"{"text":"ok","status":"complete"}"#)),
                    false,
                ),
            ],
            vec!["ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["once"]}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"choice":"once"}"#)],
        ),
        (
            "withdrawn",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(
                    once(),
                    Some(("request.cancel", r#"{"id":"srq-1","method":"approval"}"#)),
                    false,
                ),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["notfound"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"choice":"once"}"#)],
        ),
        (
            "failed",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(once(), None, true),
                resolve(once(), None, false),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["other:hermes rpc: client closed", "ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["once"]}]},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-h","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-i","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":6,"timestamp_ms":10,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-1"#, r#"{"choice":"once"}"#)],
        ),
        (
            "failedparked",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                resolve(
                    once(),
                    Some(("message.complete", r#"{"text":"ok","status":"complete"}"#)),
                    true,
                ),
            ],
            vec!["other:hermes rpc: client closed"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![],
        ),
        (
            "afterterminal",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
                resolve(once(), None, false),
            ],
            vec!["notfound"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-g","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":4,"timestamp_ms":7,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-h","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":5,"timestamp_ms":8,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![],
        ),
        (
            "twogates",
            vec![
                Step::Gate("approval", r#"{"command":"rm","choices":["once","deny"]}"#),
                Step::Gate("approval", r#"{"command":"second","choices":["deny"]}"#),
                resolve(
                    input("interaction-g", vec![pick("choice", &["deny"])]),
                    None,
                    false,
                ),
                Step::Complete(r#"{"text":"ok","status":"complete"}"#),
            ],
            vec!["ok"],
            vec![
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"rm","questions":[{"id":"choice","prompt":"rm","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"},{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-h","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"second","questions":[{"id":"choice","prompt":"second","kind":"single_choice","required":true,"options":[{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":4,"timestamp_ms":7,"session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-i","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-g","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-j","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["deny"]}]},"sequence":6,"timestamp_ms":10,"in_reply_to":"event-h","session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-k","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":11},"sequence":7,"timestamp_ms":12,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-l","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"cancelled"},"sequence":8,"timestamp_ms":13,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
                r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-m","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":9,"timestamp_ms":14,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            ],
            vec![(r#"srq-2"#, r#"{"choice":"deny"}"#)],
        ),
    ] {
        let (got_outcomes, got_events, got_answers) = traced(steps);
        assert_eq!(got_outcomes, outcomes, "{name}");
        assert_eq!(got_events, events, "{name}");
        let answers: Vec<(String, String)> = answers
            .into_iter()
            .map(|(id, result): (&str, &str)| (id.to_owned(), result.to_owned()))
            .collect();
        assert_eq!(got_answers, answers, "{name}");
    }
}

#[test]
fn a_settlement_waits_for_every_resolution_in_flight_as_go_s_does() {
    let fake = Fake::new([Ok(STREAMING)]);
    let session = Arc::new(opened(&fake));
    let (_, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| session.submit(submission()));
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    let gate = |count: usize, payload: &str| {
        let params = format!(
            r#"{{"session_id":"sess0001","request_id":"0123456789abcdef0123456789abcdef",{payload}}}"#
        );
        fake.request(
            RequestId::String(format!("srq-{count}")),
            "approval",
            &params,
        )
    };
    let first = gate(1, r#""command":"first","choices":["once"]"#);
    let second = gate(2, r#""command":"second","choices":["deny"]"#);
    let inner = Arc::new(std::sync::Mutex::new(String::new()));
    let (nested, settle, outcome) = (session.clone(), fake.clone(), inner.clone());
    *second.before.lock().unwrap() = Some(Box::new(move || {
        settle.event(
            "message.complete",
            2,
            r#"{"text":"ok","status":"complete"}"#,
        )
    }));
    *first.before.lock().unwrap() = Some(Box::new(move || {
        let resolved = nested.resolve(input("interaction-g", vec![pick("choice", &["deny"])]));
        *outcome.lock().unwrap() = label(resolved);
    }));
    let outer = label(session.resolve(input("interaction-d", vec![pick("choice", &["once"])])));
    assert_eq!(
        (inner.lock().unwrap().as_str(), outer.as_str()),
        ("ok", "ok")
    );
    let events: Vec<String> = drain(&mut stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&serde_json::to_string(envelope).unwrap()))
        .collect();
    assert_eq!(
        events,
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-e","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"first","questions":[{"id":"choice","prompt":"first","kind":"single_choice","required":true,"options":[{"id":"once","label":"once"}]}],"allow_cancel":true},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-f","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-d","updated_at_ms":5},"sequence":3,"timestamp_ms":6,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.requested","id":"event-h","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","title":"Command approval","description":"second","questions":[{"id":"choice","prompt":"second","kind":"single_choice","required":true,"options":[{"id":"deny","label":"deny"}]}],"allow_cancel":true},"sequence":4,"timestamp_ms":7,"session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-i","payload":{"session_id":"session","run_id":"run-b","status":"waiting_for_input","pending_user_input_id":"interaction-g","updated_at_ms":8},"sequence":5,"timestamp_ms":9,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-j","payload":{"interaction_id":"interaction-g","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["deny"]}]},"sequence":6,"timestamp_ms":10,"in_reply_to":"event-h","session_id":"session","run_id":"run-b","turn_id":"interaction-g","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-k","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":11},"sequence":7,"timestamp_ms":12,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"user.input.resolved","id":"event-l","payload":{"interaction_id":"interaction-d","requested_by":"hermes.gateway","responded_by":"user","session_id":"session","run_id":"run-b","status":"submitted","answers":[{"question_id":"choice","selected_option_ids":["once"]}]},"sequence":8,"timestamp_ms":13,"in_reply_to":"event-e","session_id":"session","run_id":"run-b","turn_id":"interaction-d","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.status.updated","id":"event-m","payload":{"session_id":"session","run_id":"run-b","status":"running","updated_at_ms":14},"sequence":9,"timestamp_ms":15,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"event-n","payload":{"session_id":"session","run_id":"run-b","final_response":{"id":"message-a","role":"assistant","content":"ok"},"stop_reason":"completed"},"sequence":10,"timestamp_ms":16,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
        ]
    );
    assert_eq!(first.results(), [r#"{"choice":"once"}"#]);
    assert_eq!(second.results(), [r#"{"choice":"deny"}"#]);
}
