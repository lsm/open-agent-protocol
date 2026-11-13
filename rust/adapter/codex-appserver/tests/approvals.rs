mod support;

use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use oap_adapter::gojson::go_marshal;
use oap_adapter::runtime::Runtime;
use oap_adapter::testkit::{drain, next, RunTrace};
use oap_adapter::{Error, InteractionResolution, OpenRequest};
use oap_adapter_codex_appserver::adapter::Adapter;
use oap_adapter_codex_appserver::rpc::{
    self, Inbound, IncomingRequest, Message as Answer, RequestId, Responder, DIALECT,
};
use oap_adapter_codex_appserver::session::Session;
use oap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    Participant, PermissionResolveRequest, RequestedDeliveryMode,
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
}

#[derive(Deserialize)]
struct Step {
    notify: Option<Notify>,
    request: Option<Request>,
    #[serde(default)]
    r#await: usize,
    resolve: Option<Resolve>,
}

#[derive(Deserialize)]
struct Scenario {
    name: String,
    steps: Vec<Step>,
    events: Box<RawValue>,
    answers: Value,
    resolutions: Vec<String>,
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
        .filter(|event| event.envelope_type == EnvelopeType::ACTION_PERMISSION_REQUESTED)
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

fn opened(fake: &Arc<Fake>) -> (Adapter, Session) {
    let clock = Arc::new(AtomicI64::new(0));
    let ids = Arc::new(AtomicU64::new(0));
    let adapter = adapter(fake, |config| {
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
    (adapter, session)
}

fn fake() -> Arc<Fake> {
    Fake::new([
        Ok(r#"{"thread":{"id":"native-thread"}}"#),
        Ok(r#"{"turn":{"id":"native-turn","status":"inProgress"}}"#),
    ])
}

#[test]
fn each_approval_scenario_round_trips_as_go_does() {
    let scenarios: Vec<Scenario> = serde_json::from_str(include_str!("go_approvals.json")).unwrap();
    assert_eq!(scenarios.len(), 8);
    for scenario in scenarios {
        let fake = fake();
        let (adapter, session) = opened(&fake);
        let (admission, mut stream) = session.submit(submission()).unwrap();
        let mut events = Vec::new();
        let mut requests = Vec::new();
        let mut resolutions = Vec::new();
        for step in scenario.steps {
            if let Some(notify) = step.notify {
                fake.notify(&notify.method, notify.params.get());
            } else if let Some(request) = step.request {
                let answers = fake.request_with(request.id, &request.method, request.params.get());
                requests.push((request.id, answers));
            } else if let Some(resolve) = step.resolve {
                let mut permission = resolve.permission;
                if let Some(permission) = permission.as_mut() {
                    if permission.interaction_id.as_str().is_empty() {
                        permission.interaction_id = latest_interaction(&events).into();
                    }
                }
                resolutions.push(outcome(session.resolve(InteractionResolution {
                    run_id: resolve.run_id.into(),
                    responded_by: resolve.responded_by.into(),
                    permission,
                    input: None,
                })));
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
        assert_eq!(resolutions, scenario.resolutions, "{}", scenario.name);
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
        let cancelled = events
            .iter()
            .any(|event| event.envelope_type == EnvelopeType::RUN_CANCELLED);
        RunTrace {
            submitted: Some(submission()),
            cancelled,
            ..RunTrace::new(admission, adapter.describe(), events)
        }
        .assert_valid();
    }
}

struct Held {
    entered: crossbeam_channel::Sender<()>,
    released: crossbeam_channel::Receiver<()>,
    sent: Mutex<Vec<Answer>>,
}

impl Responder for Held {
    fn send(&self, message: Answer) -> Result<(), rpc::Error> {
        self.entered.send(()).unwrap();
        self.released.recv().unwrap();
        self.sent.lock().unwrap().push(message);
        Ok(())
    }
}

#[test]
fn a_turn_that_completes_while_an_answer_is_written_waits_for_the_resolution_as_go_s_does() {
    let fake = fake();
    let (_adapter, session) = opened(&fake);
    let (_admission, mut stream) = session.submit(submission()).unwrap();
    fake.notify(
        "turn/started",
        r#"{"threadId":"native-thread","turn":{"id":"native-turn","status":"inProgress"}}"#,
    );
    fake.notify(
        "item/started",
        r#"{"threadId":"native-thread","turnId":"native-turn","item":{"type":"commandExecution","id":"c1","command":"ls"}}"#,
    );
    let (entered, arrivals) = crossbeam_channel::unbounded();
    let (release, released) = crossbeam_channel::bounded(0);
    let held = Arc::new(Held {
        entered,
        released,
        sent: Mutex::new(Vec::new()),
    });
    fake.deliver(Inbound::Request(Arc::new(IncomingRequest::new(
        DIALECT,
        RequestId::Integer(1),
        "item/commandExecution/requestApproval",
        Some(
            RawValue::from_string(
                r#"{"threadId":"native-thread","turnId":"native-turn","itemId":"c1"}"#.into(),
            )
            .unwrap(),
        ),
        held.clone(),
    ))))
    .unwrap();
    let mut events: Vec<Envelope> = (0..4)
        .map(|_| next(&mut stream, Duration::from_secs(5)))
        .collect();
    let interaction = latest_interaction(&events);
    std::thread::scope(|scope| {
        let resolving = scope.spawn(|| {
            session.resolve(InteractionResolution {
                run_id: "run-02".into(),
                responded_by: "user".into(),
                permission: Some(PermissionResolveRequest {
                    interaction_id: interaction.as_str().into(),
                    session_id: "session-1".into(),
                    run_id: "run-02".into(),
                    requested_by: "codex.app-server".into(),
                    responded_by: "user".into(),
                    choice_id: "accept".into(),
                    granted: true,
                    ..PermissionResolveRequest::default()
                }),
                input: None,
            })
        });
        arrivals.recv_timeout(Duration::from_secs(5)).unwrap();
        fake.notify(
            "turn/completed",
            r#"{"threadId":"native-thread","turn":{"id":"native-turn","status":"completed"}}"#,
        );
        let (ack, acked) = crossbeam_channel::bounded::<()>(0);
        fake.deliver(Inbound::Barrier(ack)).unwrap();
        assert_eq!(
            acked.recv_timeout(Duration::from_millis(200)),
            Err(crossbeam_channel::RecvTimeoutError::Timeout)
        );
        release.send(()).unwrap();
        assert_eq!(resolving.join().unwrap(), Ok(()));
    });
    while events.last().map(|event| &event.envelope_type) != Some(&EnvelopeType::RUN_COMPLETED) {
        events.push(next(&mut stream, Duration::from_secs(5)));
    }
    assert_eq!(
        events[4..]
            .iter()
            .map(|event| format!(
                "{} {}",
                event.envelope_type.as_str(),
                go_marshal(event.payload.as_ref().unwrap().get())
            ))
            .collect::<Vec<_>>(),
        [
            r#"action.permission.resolved {"interaction_id":"interaction-09","requested_by":"codex.app-server","responded_by":"user","session_id":"session-1","run_id":"run-02","tool_call_id":"tool-call-06","outcome":"resolved","choice_id":"accept","granted":true}"#,
            r#"action.call.failed {"session_id":"session-1","run_id":"run-02","tool_call_id":"tool-call-06","requested_by":"codex.app-server","execution_owner":"codex.app-server","name":"codex.command","error":{"code":"native_action_incomplete","message":"native turn ended before the action completed"}}"#,
            r#"run.completed {"session_id":"session-1","run_id":"run-02","final_response":{"id":"message-03","role":"assistant","content":""},"stop_reason":"end_turn"}"#,
        ]
    );
    assert_eq!(held.sent.lock().unwrap().len(), 1);
}
