mod support;

use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use oap_adapter::gojson::go_marshal;
use oap_adapter::runtime::Runtime;
use oap_adapter::testkit::{drain, next, RunTrace};
use oap_adapter::{Error, EventStream, InteractionResolution, OpenRequest};
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    Participant, PermissionResolveRequest, RequestedDeliveryMode, UserInputResolveRequest,
};
use serde::Deserialize;
use serde_json::value::RawValue;
use serde_json::{json, Value};
use support::{adapter, Answers, Fake};

#[derive(Deserialize)]
struct Notify {
    method: String,
    params: Box<RawValue>,
}

#[derive(Deserialize)]
struct Request {
    id: i64,
    method: String,
    params: Box<RawValue>,
}

#[derive(Deserialize)]
struct Resolve {
    run_id: String,
    responded_by: String,
    permission: Option<PermissionResolveRequest>,
    input: Option<UserInputResolveRequest>,
}

#[derive(Deserialize)]
struct Step {
    notify: Option<Notify>,
    request: Option<Request>,
    #[serde(default)]
    r#await: usize,
    resolve: Option<Resolve>,
    #[serde(default)]
    state: bool,
}

#[derive(Deserialize)]
struct Scenario {
    name: String,
    steps: Vec<Step>,
    events: Box<RawValue>,
    answers: Value,
    resolutions: Vec<String>,
    states: Vec<String>,
}

fn submission() -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "session-1".into(),
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("hello"),
            ..Message::default()
        }],
        delivery: RequestedDeliveryMode::AUTO,
        ..MessageSubmitRequest::default()
    }
}

fn outcome(result: Result<(), Error>) -> String {
    match result {
        Ok(()) => "ok".into(),
        Err(Error::InvalidResolution) => "invalid_resolution".into(),
        Err(Error::InteractionNotFound) => "interaction_not_found".into(),
        Err(Error::InteractionResolved) => "interaction_resolved".into(),
        Err(Error::WrongResponder) => "wrong_responder".into(),
        Err(other) => format!("error: {other}"),
    }
}

fn latest_interaction(events: &[Envelope]) -> String {
    events
        .iter()
        .rev()
        .filter(|event| {
            event.envelope_type == EnvelopeType::ACTION_PERMISSION_REQUESTED
                || event.envelope_type == EnvelopeType::USER_INPUT_REQUESTED
        })
        .find_map(|event| {
            let payload: Value = serde_json::from_str(event.payload.as_ref()?.get()).ok()?;
            Some(payload["interaction_id"].as_str()?.to_owned())
        })
        .unwrap_or_default()
}

fn answer(id: i64, answers: &Answers) -> Value {
    let deadline = Instant::now() + Duration::from_secs(2);
    while answers.0.lock().unwrap().is_empty() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(5));
    }
    let answers = answers.0.lock().unwrap();
    let Some(message) = answers.first() else {
        return json!({"id": id, "result": null, "error": null});
    };
    let result = message
        .result
        .as_ref()
        .map_or(Value::Null, |raw| serde_json::from_str(raw.get()).unwrap());
    let error = message.error.as_ref().map_or(Value::Null, |error| {
        let mut object = json!({"code": error.code, "message": error.message});
        if let Some(data) = &error.data {
            object["data"] = serde_json::from_str(data.get()).unwrap();
        }
        object
    });
    json!({"id": id, "result": result, "error": error})
}

fn take(stream: &mut EventStream, events: &mut Vec<Envelope>, count: usize) {
    for _ in 0..count {
        match catch_unwind(AssertUnwindSafe(|| next(stream, Duration::from_secs(2)))) {
            Ok(event) => events.push(event),
            Err(_) => return,
        }
    }
}

#[test]
fn each_user_input_request_is_scoped_and_settled_as_go_does() {
    let scenarios: Vec<Scenario> = serde_json::from_str(include_str!("go_inputs.json")).unwrap();
    assert_eq!(scenarios.len(), 3);
    for scenario in scenarios {
        let fake = Fake::new([
            Ok(r#"{"thread":{"id":"native-thread"}}"#),
            Ok(r#"{"turn":{"id":"native-turn","status":"inProgress"}}"#),
        ]);
        let clock = Arc::new(AtomicI64::new(0));
        let ids = Arc::new(AtomicU64::new(0));
        let adapter = adapter(&fake, |config| {
            config.model = "glm-test".into();
            config.journal_capacity = 32;
            config.runtime = Runtime::new(
                move || clock.fetch_add(1, Ordering::SeqCst) + 1,
                move |kind| format!("{kind}-{:02}", ids.fetch_add(1, Ordering::SeqCst) + 1),
            );
        });
        let session = adapter
            .open(OpenRequest {
                session_id: "session-1".into(),
                participant: Participant {
                    id: "user".into(),
                    ..Participant::default()
                },
                ..OpenRequest::default()
            })
            .unwrap();
        let (admission, mut stream) = session.submit(submission()).unwrap();
        let mut events = Vec::new();
        let mut requests = Vec::new();
        let mut resolutions = Vec::new();
        let mut states = Vec::new();
        for step in scenario.steps {
            if let Some(notify) = step.notify {
                fake.notify(&notify.method, notify.params.get());
            } else if let Some(request) = step.request {
                let answers = fake.request_with(request.id, &request.method, request.params.get());
                requests.push((request.id, answers));
            } else if let Some(resolve) = step.resolve {
                let latest = latest_interaction(&events);
                let mut permission = resolve.permission;
                if let Some(permission) = permission.as_mut() {
                    if permission.interaction_id.as_str().is_empty() {
                        permission.interaction_id = latest.as_str().into();
                    }
                }
                let mut input = resolve.input;
                if let Some(input) = input.as_mut() {
                    if input.interaction_id.as_str().is_empty() {
                        input.interaction_id = latest.as_str().into();
                    }
                }
                resolutions.push(outcome(session.resolve(InteractionResolution {
                    run_id: resolve.run_id.into(),
                    responded_by: resolve.responded_by.into(),
                    permission,
                    input,
                })));
            } else if step.state {
                let state = session.state().unwrap();
                let active = state.active_run_id.map(|id| id.as_str().to_owned());
                states.push(format!(
                    "{} {}",
                    state.status.as_str(),
                    active.unwrap_or_default()
                ));
            } else {
                take(&mut stream, &mut events, step.r#await);
            }
        }
        events.extend(drain(&mut stream, Duration::from_secs(5)));
        assert_eq!(
            go_marshal(&serde_json::to_string(&events).unwrap()),
            go_marshal(scenario.events.get()),
            "{}",
            scenario.name
        );
        assert_eq!(resolutions, scenario.resolutions, "{}", scenario.name);
        assert_eq!(states, scenario.states, "{}", scenario.name);
        let answered: Vec<Value> = requests
            .iter()
            .map(|(id, answers)| answer(*id, answers))
            .collect();
        assert_eq!(
            Value::Array(answered),
            scenario.answers,
            "{}",
            scenario.name
        );
        if events
            .iter()
            .all(|event| event.envelope_type != EnvelopeType::RUN_CANCELLED)
        {
            RunTrace {
                submitted: Some(submission()),
                ..RunTrace::new(admission, adapter.describe(), events)
            }
            .assert_valid();
        }
    }
}
