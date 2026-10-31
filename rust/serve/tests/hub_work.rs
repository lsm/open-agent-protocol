use std::time::{Duration, Instant};

use futures::executor::block_on;
use oap_adapter::{OpenRequest, SubmitRequest};
use oap_protocol::{
    Message, MessageContent, MessageRole, MessageSubmitRequest, RequestedDeliveryMode, RunId,
};
use oap_serve::{Hub, HubError, Registry, WorkStatus};

fn opened(hub: &Hub, id: &str) {
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: id.into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap();
}

fn submitted(hub: &Hub, id: &str, text: &str) -> RunId {
    block_on(hub.session(&id.into()).unwrap().submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: id.into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text(text),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap()
    .run_id
    .unwrap()
}

#[test]
fn a_fresh_session_is_done_with_no_run_under_its_adapter() {
    let hub = Hub::new(Registry::default_registry());
    opened(&hub, "s");
    let work = block_on(hub.work(&"s".into())).unwrap();
    assert_eq!(work.status, WorkStatus::Done);
    assert_eq!(work.adapter, "memory");
    assert_eq!(work.run_id, None);
    assert!(work.updated_at_ms > 0);
}

#[test]
fn a_run_waiting_on_a_permission_needs_you_and_its_message_is_the_first_turn() {
    let hub = Hub::new(Registry::default_registry());
    opened(&hub, "s");
    let run = submitted(&hub, "s", "do the thing");
    let work = block_on(hub.work(&"s".into())).unwrap();
    assert_eq!(work.status, WorkStatus::NeedsYou, "{work:?}");
    assert_eq!(work.run_id, Some(run.clone()));
    assert!(work.pending_interaction.is_some());
    let turns = hub.session(&"s".into()).unwrap().turns(None, 10);
    assert_eq!(turns.first_index, 0);
    assert_eq!(turns.turns[0].role, "user");
    assert_eq!(turns.turns[0].text, "do the thing");
    assert_eq!(turns.turns[0].run_id, Some(run));
}

#[test]
fn a_cancelled_run_leaves_its_session_stopped_with_the_outcome_logged() {
    let hub = Hub::new(Registry::default_registry());
    opened(&hub, "s");
    let run = submitted(&hub, "s", "go");
    let entry = hub.session(&"s".into()).unwrap();
    block_on(entry.cancel(run.clone())).unwrap();
    let deadline = Instant::now() + Duration::from_secs(5);
    let work = loop {
        let work = block_on(hub.work(&"s".into())).unwrap();
        if work.status == WorkStatus::Stopped || Instant::now() > deadline {
            break work;
        }
        std::thread::sleep(Duration::from_millis(10));
    };
    assert_eq!(work.status, WorkStatus::Stopped, "{work:?}");
    assert_eq!(work.run_id, Some(run));
    let turns = entry.turns(None, 10);
    assert_eq!(turns.turns[1].role, "assistant");
    assert_eq!(turns.turns[1].outcome, "cancelled");
}

#[test]
fn works_lists_each_live_session_and_a_closed_one_is_gone() {
    let hub = Hub::new(Registry::default_registry());
    opened(&hub, "a");
    opened(&hub, "b");
    hub.session(&"a".into()).unwrap().set_title("first");
    block_on(hub.session(&"b".into()).unwrap().close()).unwrap();
    let works = block_on(hub.works());
    assert_eq!(works.len(), 1);
    assert_eq!(works[0].session_id.as_str(), "a");
    assert_eq!(works[0].title, "first");
    assert!(matches!(
        block_on(hub.work(&"b".into())),
        Err(HubError::UnknownSession(_))
    ));
}
