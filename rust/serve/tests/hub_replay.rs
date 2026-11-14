use std::sync::Arc;

use futures::executor::block_on;
use futures::{FutureExt, StreamExt};
use oxap_adapter::memory::{Config, Memory};
use oxap_adapter::{Error, InteractionResolution, OpenRequest, SubmitRequest};
use oxap_protocol::{
    Envelope, EnvelopeType, Message, MessageContent, MessageRole, MessageSubmitRequest,
    PermissionResolveRequest, RequestedDeliveryMode, RunId, UserInputResolveRequest,
};
use oxap_serve::{Hub, HubError, HubSession, Registry, Subscription};

fn hub(capacity: usize) -> Hub {
    let mut registry = Registry::new();
    registry
        .register(
            "memory",
            Arc::new(Memory::new(Config {
                journal_capacity: capacity,
                ..Config::default()
            })),
        )
        .unwrap();
    Hub::new(registry)
}

fn open(hub: &Hub, id: &str) -> Arc<HubSession> {
    block_on(hub.open(
        "memory",
        OpenRequest {
            session_id: id.into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap()
    .session
}

fn prompt(session: &str) -> SubmitRequest {
    SubmitRequest {
        request: MessageSubmitRequest {
            session_id: session.into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("go"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }
}

fn cancelled_run(session: &Arc<HubSession>) -> RunId {
    let admission = block_on(session.submit(prompt(session.id().as_str()))).unwrap();
    let run = admission.run_id.unwrap();
    assert!(block_on(session.cancel(run.clone())).unwrap().accepted);
    run
}

fn drain(subscription: &mut Subscription) -> Vec<Envelope> {
    let mut seen = Vec::new();
    while let Some(Some(item)) = subscription.next().now_or_never() {
        seen.push(item.unwrap());
    }
    seen
}

fn until(subscription: &mut Subscription, stop: impl Fn(&Envelope) -> bool) -> Vec<Envelope> {
    let mut seen = Vec::new();
    loop {
        let envelope = block_on(subscription.next()).unwrap().unwrap();
        let stopping = stop(&envelope);
        seen.push(envelope);
        if stopping {
            return seen;
        }
    }
}

fn of_type(kind: EnvelopeType) -> impl Fn(&Envelope) -> bool {
    move |envelope| envelope.envelope_type == kind
}

#[test]
fn a_resumed_stream_carries_on_into_the_sessions_next_run() {
    let hub = hub(64);
    let session = open(&hub, "next-run");
    let mut watcher = hub.subscribe(session.id()).unwrap();
    let first = cancelled_run(&session);
    until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let mut resumed = block_on(hub.subscribe_after(session.id(), Some(first.clone()), 0)).unwrap();
    let replayed = until(&mut resumed, of_type(EnvelopeType::RUN_CANCELLED));
    assert_eq!(replayed[0].run_id.as_ref(), Some(&first));
    assert_eq!(replayed[0].sequence, Some(1));
    let second = cancelled_run(&session);
    let carried = until(&mut resumed, of_type(EnvelopeType::RUN_CANCELLED));
    assert_eq!(carried[0].run_id.as_ref(), Some(&second));
    assert_eq!(carried[0].sequence, Some(1));
    assert_eq!(
        carried.last().and_then(|last| last.run_id.as_ref()),
        Some(&second)
    );
}

#[test]
fn a_cursor_replays_a_later_run_that_settled_before_it_subscribed() {
    let hub = hub(64);
    let session = open(&hub, "later-run");
    let mut watcher = hub.subscribe(session.id()).unwrap();
    let first = cancelled_run(&session);
    until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let second = cancelled_run(&session);
    until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let mut resumed = block_on(hub.subscribe_after(session.id(), Some(first), 0)).unwrap();
    until(&mut resumed, of_type(EnvelopeType::RUN_CANCELLED));
    let replayed = until(&mut resumed, of_type(EnvelopeType::RUN_CANCELLED));
    for (index, envelope) in replayed.iter().enumerate() {
        assert_eq!(envelope.run_id.as_ref(), Some(&second));
        assert_eq!(envelope.sequence, Some(index as u64 + 1));
    }
    let third = cancelled_run(&session);
    let carried = until(&mut resumed, of_type(EnvelopeType::RUN_CANCELLED));
    assert_eq!(carried[0].run_id.as_ref(), Some(&third));
    assert_eq!(carried[0].sequence, Some(1));
}

#[test]
fn a_cursor_is_a_replay_gap_when_a_later_run_has_left_the_journal() {
    let hub = hub(2);
    let session = open(&hub, "lost-run");
    let mut watcher = hub.subscribe(session.id()).unwrap();
    let first = cancelled_run(&session);
    let settled = until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let reached = settled.last().unwrap().sequence.unwrap();
    cancelled_run(&session);
    until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    match block_on(hub.subscribe_after(session.id(), Some(first), reached)) {
        Err(HubError::Adapter(Error::ReplayGap(gap))) => assert_eq!(gap.requested_after, 0),
        Err(err) => panic!("{err}"),
        Ok(_) => panic!("a cursor over a lost run was accepted"),
    }
}

#[test]
fn a_one_run_cursor_ends_with_its_run_and_ignores_a_later_runs_gap() {
    let hub = hub(2);
    let session = open(&hub, "one-run");
    let mut watcher = hub.subscribe(session.id()).unwrap();
    let first = cancelled_run(&session);
    let settled = until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let reached = settled.last().unwrap().sequence.unwrap();
    cancelled_run(&session);
    until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let mut replay = block_on(hub.subscribe_one_run(session.id(), Some(first), reached)).unwrap();
    assert!(block_on(replay.next()).is_none());
}

#[test]
fn ending_subscriptions_stops_a_replay_still_following_a_run() {
    let hub = hub(64);
    let session = open(&hub, "following");
    let run = block_on(session.submit(prompt("following")))
        .unwrap()
        .run_id
        .unwrap();
    let mut cursor = block_on(hub.subscribe_after(session.id(), Some(run), 0)).unwrap();
    until(
        &mut cursor,
        of_type(EnvelopeType::ACTION_PERMISSION_REQUESTED),
    );
    hub.end_subscriptions();
    assert!(block_on(cursor.next()).is_none());
}

#[test]
fn a_cursor_skips_a_run_admitted_before_its_own() {
    let hub = hub(64);
    let session = open(&hub, "earlier-run");
    let mut watcher = hub.subscribe(session.id()).unwrap();
    let earlier = block_on(session.submit(prompt("earlier-run")))
        .unwrap()
        .run_id
        .unwrap();
    until(
        &mut watcher,
        of_type(EnvelopeType::ACTION_PERMISSION_REQUESTED),
    );
    let mut queued = prompt("earlier-run");
    queued.request.delivery = RequestedDeliveryMode::QUEUE;
    let queued = block_on(session.submit(queued)).unwrap().run_id.unwrap();
    block_on(session.cancel(queued.clone())).unwrap();
    until(&mut watcher, |envelope| {
        envelope.run_id.as_ref() == Some(&queued)
    });
    let mut cursor = block_on(hub.subscribe_after(session.id(), Some(queued.clone()), 0)).unwrap();
    until(&mut cursor, |envelope| {
        envelope.run_id.as_ref() == Some(&queued)
    });
    block_on(session.cancel(earlier)).unwrap();
    until(&mut watcher, of_type(EnvelopeType::RUN_CANCELLED));
    let later = cancelled_run(&session);
    for envelope in until(&mut cursor, of_type(EnvelopeType::RUN_CANCELLED)) {
        assert_eq!(envelope.run_id.as_ref(), Some(&later));
    }
}

#[test]
fn a_cursor_replays_the_run_after_it_through_the_terminal() {
    let hub = hub(64);
    let session = open(&hub, "resume");
    let run = cancelled_run(&session);
    let mut replay = block_on(hub.subscribe_after(&"resume".into(), Some(run.clone()), 2)).unwrap();
    assert_eq!(replay.run_id(), &run);
    let seen = drain(&mut replay);
    assert!(!seen.is_empty());
    for (offset, envelope) in seen.iter().enumerate() {
        assert_eq!(envelope.run_id.as_ref(), Some(&run));
        assert_eq!(envelope.sequence, Some(3 + offset as u64));
    }
    assert_eq!(
        seen.last().unwrap().envelope_type,
        EnvelopeType::RUN_CANCELLED
    );
    let mut current = block_on(hub.subscribe_after(&"resume".into(), None, 2)).unwrap();
    assert_eq!(current.run_id(), &run);
    let positions = |events: &[Envelope]| -> Vec<(String, Option<u64>)> {
        events
            .iter()
            .map(|envelope| (envelope.id.to_string(), envelope.sequence))
            .collect()
    };
    assert_eq!(positions(&drain(&mut current)), positions(&seen));
}

#[test]
fn a_cursor_older_than_the_journal_keeps_is_a_replay_gap() {
    let hub = hub(2);
    let session = open(&hub, "gap");
    let run = cancelled_run(&session);
    match block_on(hub.subscribe_after(&"gap".into(), Some(run), 0)) {
        Err(HubError::Adapter(Error::ReplayGap(gap))) => {
            assert_eq!(gap.requested_after, 0);
            assert!(gap.oldest_available > 1);
        }
        other => panic!("subscribe = {:?}", other.err()),
    }
}

#[test]
fn a_cursor_needs_a_live_session_with_a_run() {
    let hub = hub(64);
    assert!(matches!(
        block_on(hub.subscribe_after(&"ghost".into(), None, 0)),
        Err(HubError::UnknownSession(_))
    ));
    let session = open(&hub, "idle");
    assert!(matches!(
        block_on(hub.subscribe_after(&"idle".into(), None, 0)),
        Err(HubError::NoRunToResume)
    ));
    cancelled_run(&session);
    let held = hub.session(&"idle".into()).unwrap();
    block_on(session.close()).unwrap();
    assert!(matches!(
        block_on(hub.subscribe_after(&"idle".into(), None, 0)),
        Err(HubError::UnknownSession(_))
    ));
    assert!(matches!(
        block_on(held.submit(prompt("idle"))),
        Err(HubError::Adapter(Error::SessionClosed))
    ));
}

#[test]
fn a_request_scoped_to_another_session_is_refused_before_the_adapter_sees_it() {
    let hub = hub(64);
    let session = open(&hub, "dup");
    match block_on(session.submit(prompt("other"))) {
        Err(HubError::ScopeMismatch { payload, addressed }) => {
            assert_eq!((payload.as_str(), addressed.as_str()), ("other", "dup"));
        }
        other => panic!("submit = {:?}", other.map(|admission| admission.run_id)),
    }
    assert!(matches!(
        block_on(hub.subscribe_after(&"dup".into(), None, 0)),
        Err(HubError::NoRunToResume)
    ));
}

#[test]
fn a_resolution_scoped_to_another_session_is_refused_before_the_adapter_sees_it() {
    let hub = hub(64);
    let session = open(&hub, "mine");
    let permission = InteractionResolution {
        permission: Some(PermissionResolveRequest {
            session_id: "theirs".into(),
            ..PermissionResolveRequest::default()
        }),
        ..InteractionResolution::default()
    };
    let input = InteractionResolution {
        input: Some(UserInputResolveRequest {
            session_id: "theirs".into(),
            ..UserInputResolveRequest::default()
        }),
        ..InteractionResolution::default()
    };
    for resolution in [permission, input] {
        match block_on(session.resolve(resolution)) {
            Err(HubError::ScopeMismatch { payload, addressed }) => {
                assert_eq!((payload.as_str(), addressed.as_str()), ("theirs", "mine"));
            }
            other => panic!("resolve = {other:?}"),
        }
    }
}

#[test]
fn an_unqualified_cursor_follows_a_queued_run_the_adapter_has_since_started() {
    let hub = hub(64);
    let session = open(&hub, "promoted");
    let first = block_on(session.submit(prompt("promoted")))
        .unwrap()
        .run_id
        .unwrap();
    let mut queued = prompt("promoted");
    queued.request.delivery = RequestedDeliveryMode::QUEUE;
    let second = block_on(session.submit(queued)).unwrap().run_id.unwrap();
    assert_ne!(first, second);
    block_on(session.cancel(first)).unwrap();
    let replay = block_on(hub.subscribe_after(&"promoted".into(), None, 0)).unwrap();
    assert_eq!(replay.run_id(), &second);
}

#[test]
fn a_live_subscriber_joins_a_queued_run_once_the_adapter_starts_it() {
    let hub = hub(64);
    let session = open(&hub, "handover");
    let first = block_on(session.submit(prompt("handover")))
        .unwrap()
        .run_id
        .unwrap();
    let mut queued = prompt("handover");
    queued.request.delivery = RequestedDeliveryMode::QUEUE;
    let second = block_on(session.submit(queued)).unwrap().run_id.unwrap();
    block_on(session.cancel(first)).unwrap();
    let deadline = std::time::Instant::now() + std::time::Duration::from_secs(5);
    while session.current_run().as_ref() != Some(&second) {
        assert!(
            std::time::Instant::now() < deadline,
            "{:?}",
            session.current_run()
        );
        std::thread::sleep(std::time::Duration::from_millis(10));
    }
    let joined = hub.subscribe(&"handover".into()).unwrap().joined_at();
    assert_eq!(joined.map(|(run, _)| run), Some(second));
}
