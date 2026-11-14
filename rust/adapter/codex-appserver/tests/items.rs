mod support;

use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;
use std::time::Duration;

use oxap_adapter::gojson::go_marshal;
use oxap_adapter::runtime::Runtime;
use oxap_adapter::testkit::{drain, RunTrace};
use oxap_adapter::OpenRequest;
use oxap_protocol::{
    EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest, Participant,
    RequestedDeliveryMode,
};
use serde::Deserialize;
use serde_json::value::RawValue;
use support::{adapter, Fake};

#[derive(Deserialize)]
struct Notification {
    method: String,
    params: Box<RawValue>,
}

#[derive(Deserialize)]
struct Scenario {
    name: String,
    notifications: Vec<Notification>,
    events: Box<RawValue>,
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

#[test]
fn each_item_scenario_emits_the_actions_go_emits() {
    let scenarios: Vec<Scenario> = serde_json::from_str(include_str!("go_items.json")).unwrap();
    assert_eq!(scenarios.len(), 8);
    for scenario in scenarios {
        let fake = Fake::new([
            Ok(r#"{"thread":{"id":"native-thread"}}"#),
            Ok(r#"{"turn":{"id":"native-turn","status":"inProgress"}}"#),
        ]);
        let clock = Arc::new(AtomicI64::new(0));
        let next = Arc::new(AtomicU64::new(0));
        let adapter = adapter(&fake, |config| {
            config.model = "glm-test".into();
            config.journal_capacity = 32;
            config.runtime = Runtime::new(
                move || clock.fetch_add(1, Ordering::SeqCst) + 1,
                move |kind| format!("{kind}-{:02}", next.fetch_add(1, Ordering::SeqCst) + 1),
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
        for notification in &scenario.notifications {
            fake.notify(&notification.method, notification.params.get());
        }
        let events = drain(&mut stream, Duration::from_secs(5));
        assert_eq!(
            go_marshal(&serde_json::to_string(&events).unwrap()),
            go_marshal(scenario.events.get()),
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
