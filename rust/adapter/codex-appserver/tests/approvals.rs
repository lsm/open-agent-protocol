mod support;

use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};

use oap_adapter::gojson::go_marshal;
use oap_adapter::runtime::Runtime;
use oap_adapter::testkit::{drain, next, RunTrace};
use oap_adapter::OpenRequest;
use oap_protocol::{
    EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest, Participant,
    RequestedDeliveryMode,
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
struct Step {
    notify: Option<Notify>,
    request: Option<Request>,
    #[serde(default)]
    r#await: usize,
}

#[derive(Deserialize)]
struct Scenario {
    name: String,
    steps: Vec<Step>,
    events: Box<RawValue>,
    answers: Value,
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

#[test]
fn each_approval_request_is_scoped_and_settled_as_go_does() {
    let scenarios: Vec<Scenario> = serde_json::from_str(include_str!("go_approvals.json")).unwrap();
    assert_eq!(scenarios.len(), 5);
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
        for step in scenario.steps {
            if let Some(notify) = step.notify {
                fake.notify(&notify.method, notify.params.get());
            } else if let Some(request) = step.request {
                let answers = fake.request_with(request.id, &request.method, request.params.get());
                requests.push((request.id, answers));
            } else {
                for _ in 0..step.r#await {
                    events.push(next(&mut stream, Duration::from_secs(5)));
                }
            }
        }
        events.extend(drain(&mut stream, Duration::from_secs(5)));
        assert_eq!(
            go_marshal(&serde_json::to_string(&events).unwrap()),
            go_marshal(scenario.events.get()),
            "{}",
            scenario.name
        );
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
