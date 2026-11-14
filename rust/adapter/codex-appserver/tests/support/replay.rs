use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use super::{adapter, Fake, Reply};
use oap_adapter::gojson::go_marshal;
use oap_adapter::runtime::Runtime;
use oap_adapter::testkit::{drain, next, RunTrace};
use oap_adapter::{Error, EventStream, OpenRequest};
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    Participant, RequestedDeliveryMode, RunCancelResponse, RunId,
};
use serde::Deserialize;
use serde_json::value::RawValue;

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
    #[serde(default)]
    state: bool,
    cancel: Option<String>,
    cancel_async: Option<String>,
    #[serde(default)]
    release: bool,
    #[serde(default)]
    submit: bool,
}

#[derive(Deserialize)]
struct Scenario {
    name: String,
    #[serde(default)]
    interrupt_error: String,
    #[serde(default)]
    gate: bool,
    schema: Option<Box<RawValue>>,
    steps: Vec<Step>,
    events: Box<RawValue>,
    cancels: Vec<String>,
    states: Vec<String>,
    interrupts: usize,
    interrupt_params: Vec<String>,
}

fn submission(schema: Option<Box<RawValue>>) -> MessageSubmitRequest {
    MessageSubmitRequest {
        output_schema: schema,
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

fn named(result: Result<RunCancelResponse, Error>) -> String {
    match result {
        Ok(response) => format!(
            "{} {} {} {}",
            if response.accepted {
                "accepted"
            } else {
                "refused"
            },
            response.status.as_str(),
            response.session_id.as_str(),
            response.run_id.as_str()
        ),
        Err(Error::RunNotFound) => "run_not_found".into(),
        Err(Error::RunAlreadyTerminal { run_id, status }) => format!(
            "run_already_terminal {} {}",
            run_id.as_ref().map_or("", |id| id.as_str()),
            status.as_ref().map_or("", |status| status.as_str())
        ),
        Err(other) => format!("error: {other}"),
    }
}

fn take(stream: &mut EventStream, events: &mut Vec<Envelope>, count: usize) {
    for _ in 0..count {
        match catch_unwind(AssertUnwindSafe(|| next(stream, Duration::from_secs(2)))) {
            Ok(event) => events.push(event),
            Err(_) => return,
        }
    }
}

pub fn replay(fixture: &str, count: usize) {
    let scenarios: Vec<Scenario> = serde_json::from_str(fixture).unwrap();
    assert_eq!(scenarios.len(), count);
    for scenario in scenarios {
        let interrupt: Reply = if scenario.interrupt_error.is_empty() {
            Ok("{}")
        } else {
            Err(Box::leak(scenario.interrupt_error.clone().into_boxed_str()))
        };
        let turn: Reply = Ok(r#"{"turn":{"id":"native-turn","status":"inProgress"}}"#);
        let fake = Fake::new([Ok(r#"{"thread":{"id":"native-thread"}}"#), turn, turn]);
        fake.always_reply("turn/interrupt", interrupt);
        let gate = scenario.gate.then(|| fake.hold_interrupts());
        let (arrivals, mut release) = match gate {
            Some((arrivals, release)) => (Some(arrivals), Some(release)),
            None => (None, None),
        };
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
        let schema = scenario.schema;
        let (admission, mut stream) = session.submit(submission(schema.clone())).unwrap();
        let mut events = Vec::new();
        let mut cancels = Vec::new();
        let mut states = Vec::new();
        std::thread::scope(|scope| {
            let mut pending = Vec::new();
            for step in scenario.steps {
                if let Some(notify) = step.notify {
                    fake.notify(&notify.method, notify.params.get());
                } else if let Some(request) = step.request {
                    fake.request_with(request.id, &request.method, request.params.get());
                } else if step.state {
                    let state = session.state().unwrap();
                    let active = state.active_run_id.map(|id| id.as_str().to_owned());
                    states.push(format!(
                        "{} {}",
                        state.status.as_str(),
                        active.unwrap_or_default()
                    ));
                } else if let Some(run) = step.cancel {
                    cancels.push(named(session.cancel(&RunId::from(run))));
                } else if let Some(run) = step.cancel_async {
                    let session = &session;
                    pending.push(scope.spawn(move || named(session.cancel(&RunId::from(run)))));
                    if pending.len() == 1 {
                        let arrived = arrivals.as_ref().unwrap();
                        let _ = arrived.recv_timeout(Duration::from_secs(5));
                    } else {
                        std::thread::sleep(Duration::from_millis(200));
                    }
                } else if step.submit {
                    events.extend(drain(&mut stream, Duration::from_secs(5)));
                    stream = session.submit(submission(schema.clone())).unwrap().1;
                } else if step.release {
                    release.take();
                    for cancel in pending.drain(..) {
                        cancels.push(cancel.join().unwrap());
                    }
                } else {
                    take(&mut stream, &mut events, step.r#await);
                }
            }
        });
        events.extend(drain(&mut stream, Duration::from_secs(5)));
        assert_eq!(
            go_marshal(&serde_json::to_string(&events).unwrap()),
            go_marshal(scenario.events.get()),
            "{}",
            scenario.name
        );
        assert_eq!(cancels, scenario.cancels, "{}", scenario.name);
        assert_eq!(states, scenario.states, "{}", scenario.name);
        let interrupts: Vec<String> = fake
            .calls()
            .into_iter()
            .filter(|(method, _)| method == "turn/interrupt")
            .map(|(_, params)| params)
            .collect();
        assert_eq!(interrupts.len(), scenario.interrupts, "{}", scenario.name);
        assert_eq!(interrupts, scenario.interrupt_params, "{}", scenario.name);
        let admitted: Vec<Envelope> = events
            .into_iter()
            .filter(|event| event.run_id == admission.run_id)
            .collect();
        let started = admitted
            .iter()
            .any(|event| event.envelope_type == EnvelopeType::RUN_STARTED);
        let cancelled = admitted
            .iter()
            .any(|event| event.envelope_type == EnvelopeType::RUN_CANCELLED);
        if started {
            RunTrace {
                submitted: Some(submission(schema)),
                cancelled,
                ..RunTrace::new(admission, adapter.describe(), admitted)
            }
            .assert_valid();
        }
    }
}
