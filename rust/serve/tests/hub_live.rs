use std::sync::{Arc, Mutex};
use std::thread;
use std::time::Duration;

use futures::channel::mpsc::{unbounded, UnboundedSender};
use futures::executor::block_on;
use futures::StreamExt;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, Error, EventStream, InteractionResolution, OpenRequest,
    Recovery, ResumeRequest, Session, SubmitRequest,
};
use oap_protocol::{
    Admission, Envelope, EnvelopeType, MessageSubmitRequest, MessageSubmitResponse,
    RunCancelResponse, RunId, SessionState, SessionStatus,
};
use oap_serve::{Hub, HubError, HubOptions, HubSession, Registry, Subscription};

type Sender = UnboundedSender<Result<Envelope, Error>>;

type Hold = Arc<Mutex<Option<futures::channel::oneshot::Receiver<()>>>>;

#[derive(Default)]
struct Scripted {
    senders: Arc<Mutex<Vec<(RunId, Sender)>>>,
    queue_next: Arc<Mutex<bool>>,
    hold: Hold,
    anonymous: bool,
}

struct ScriptedSession {
    id: String,
    senders: Arc<Mutex<Vec<(RunId, Sender)>>>,
    queue_next: Arc<Mutex<bool>>,
    hold: Hold,
    anonymous: bool,
}

impl Adapter for Scripted {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        Box::pin(async { Ok(Descriptor::default()) })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        let session: Box<dyn Session> = Box::new(ScriptedSession {
            id: request.session_id.to_string(),
            senders: self.senders.clone(),
            queue_next: self.queue_next.clone(),
            hold: self.hold.clone(),
            anonymous: self.anonymous,
        });
        Box::pin(async move { Ok(session) })
    }
}

impl Session for ScriptedSession {
    fn submit(&self, _: SubmitRequest) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        let mut senders = self.senders.lock().unwrap();
        let run: RunId = format!("run-{}", senders.len() + 1).into();
        let (sender, receiver) = unbounded();
        senders.push((run.clone(), sender));
        let admission = MessageSubmitResponse {
            session_id: self.id.as_str().into(),
            accepted: true,
            admission: if std::mem::take(&mut *self.queue_next.lock().unwrap()) {
                Admission::QUEUED
            } else {
                Admission::STARTED
            },
            run_id: (!self.anonymous).then_some(run),
            ..MessageSubmitResponse::default()
        };
        let stream: EventStream = Box::pin(receiver);
        let hold = self.hold.lock().unwrap().take();
        Box::pin(async move {
            if let Some(hold) = hold {
                let _ = hold.await;
            }
            Ok((admission, stream))
        })
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        let state = SessionState {
            session_id: self.id.as_str().into(),
            status: SessionStatus::IDLE,
            ..SessionState::default()
        };
        Box::pin(async move { Ok(state) })
    }

    fn resolve(&self, _: InteractionResolution) -> BoxFuture<'_, ()> {
        Box::pin(async { Ok(()) })
    }

    fn cancel(&self, _: RunId) -> BoxFuture<'_, RunCancelResponse> {
        Box::pin(async { Err(Error::RunNotFound) })
    }

    fn resume(&self, _: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        Box::pin(async { Err(Error::RunNotFound) })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        Box::pin(async { Ok(()) })
    }
}

struct Script {
    hub: Hub,
    session: Arc<HubSession>,
    senders: Arc<Mutex<Vec<(RunId, Sender)>>>,
    queue_next: Arc<Mutex<bool>>,
    hold: Hold,
}

fn script(queue: usize) -> Script {
    scripted_with(queue, false)
}

fn scripted_with(queue: usize, anonymous: bool) -> Script {
    let scripted = Scripted {
        anonymous,
        ..Scripted::default()
    };
    let senders = scripted.senders.clone();
    let queue_next = scripted.queue_next.clone();
    let hold = scripted.hold.clone();
    let mut registry = Registry::new();
    registry.register("scripted", Arc::new(scripted)).unwrap();
    let hub = Hub::with_options(
        registry,
        HubOptions {
            stream_queue: queue,
            ..HubOptions::default()
        },
    );
    let session = block_on(hub.open(
        "scripted",
        OpenRequest {
            session_id: "s".into(),
            ..OpenRequest::default()
        },
    ))
    .unwrap()
    .session;
    Script {
        hub,
        session,
        senders,
        queue_next,
        hold,
    }
}

impl Script {
    fn submit(&self) -> RunId {
        self.submit_anyway();
        self.senders.lock().unwrap().last().unwrap().0.clone()
    }

    fn submit_anyway(&self) -> Option<RunId> {
        block_on(self.session.submit(SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "s".into(),
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        }))
        .unwrap()
        .run_id
    }

    fn submit_queued(&self) -> RunId {
        *self.queue_next.lock().unwrap() = true;
        self.submit()
    }

    fn overflow(&self, run: &RunId) {
        self.sender(run)
            .unbounded_send(Err(Error::EventStreamOverflow))
            .unwrap();
    }

    fn sender(&self, run: &RunId) -> Sender {
        let senders = self.senders.lock().unwrap();
        senders.iter().find(|(id, _)| id == run).unwrap().1.clone()
    }

    fn emit(&self, run: &RunId, sequence: u64) {
        let mut envelope = Envelope::new(
            EnvelopeType::RUN_STATUS_UPDATED,
            format!("e{sequence}"),
            &serde_json::json!({}),
        )
        .unwrap();
        envelope.session_id = Some("s".into());
        envelope.run_id = Some(run.clone());
        envelope.sequence = Some(sequence);
        self.sender(run).unbounded_send(Ok(envelope)).unwrap();
    }

    fn fail(&self, run: &RunId) {
        self.sender(run)
            .unbounded_send(Err(Error::harness("the harness died")))
            .unwrap();
    }

    fn end(&self, run: &RunId) {
        let mut senders = self.senders.lock().unwrap();
        senders.retain(|(id, _)| id != run);
    }

    fn subscribe(&self) -> Subscription {
        self.hub.subscribe(&"s".into()).unwrap()
    }
}

fn next(subscription: &mut Subscription) -> Option<Result<Envelope, HubError>> {
    block_on(subscription.next())
}

fn settle() {
    thread::sleep(Duration::from_millis(50));
}

#[test]
fn every_subscriber_sees_the_run_in_order_and_then_its_end() {
    let script = script(64);
    let mut subscriptions: Vec<Subscription> = (0..4).map(|_| script.subscribe()).collect();
    let run = script.submit();
    for sequence in 1..=3 {
        script.emit(&run, sequence);
    }
    script.end(&run);
    for subscription in &mut subscriptions {
        for sequence in 1..=3 {
            let envelope = next(subscription).unwrap().unwrap();
            assert_eq!(envelope.run_id.as_ref(), Some(&run));
            assert_eq!(envelope.sequence, Some(sequence));
        }
        assert!(next(subscription).is_none());
    }
}

#[test]
fn a_subscriber_that_falls_behind_is_ended_with_an_overflow_naming_its_run_and_position() {
    let script = script(2);
    let mut slow = script.subscribe();
    let run = script.submit();
    let mut fast = script.subscribe();
    for sequence in 1..=2 {
        script.emit(&run, sequence);
    }
    settle();
    assert_eq!(next(&mut fast).unwrap().unwrap().sequence, Some(1));
    assert_eq!(next(&mut fast).unwrap().unwrap().sequence, Some(2));
    script.emit(&run, 3);
    settle();
    assert_eq!(next(&mut slow).unwrap().unwrap().sequence, Some(1));
    assert_eq!(next(&mut slow).unwrap().unwrap().sequence, Some(2));
    match next(&mut slow) {
        Some(Err(HubError::Overflow {
            run_id,
            last_sequence,
        })) => {
            assert_eq!((run_id, last_sequence), (run.clone(), 2));
        }
        other => panic!(
            "after the overflow: {:?}",
            other.map(|item| item.map(|e| e.sequence))
        ),
    }
    assert!(next(&mut slow).is_none());
    assert_eq!(next(&mut fast).unwrap().unwrap().sequence, Some(3));
    script.end(&run);
    assert!(next(&mut fast).is_none());
}

#[test]
fn a_stream_error_reaches_the_subscriber_after_what_came_before_it() {
    let script = script(64);
    let mut watcher = script.subscribe();
    let run = script.submit();
    script.emit(&run, 1);
    script.fail(&run);
    assert_eq!(next(&mut watcher).unwrap().unwrap().sequence, Some(1));
    assert!(matches!(
        next(&mut watcher),
        Some(Err(HubError::Adapter(Error::Harness(_))))
    ));
    assert!(next(&mut watcher).is_none());
}

#[test]
fn a_subscription_reports_where_it_joined_a_run_and_nothing_between_runs() {
    let script = script(64);
    let mut watcher = script.subscribe();
    assert_eq!(watcher.joined_at(), None);
    let run = script.submit();
    script.emit(&run, 1);
    script.emit(&run, 2);
    next(&mut watcher).unwrap().unwrap();
    next(&mut watcher).unwrap().unwrap();
    let mid = script.subscribe();
    assert_eq!(mid.joined_at(), Some((run.clone(), 2)));
    drop(mid);
    script.end(&run);
    assert!(next(&mut watcher).is_none());
    let second = script.submit();
    let late = script.subscribe();
    assert_eq!(late.joined_at(), None);
    script.end(&second);
}

#[test]
fn a_subscription_ends_when_its_owner_closes_it_or_the_session_closes() {
    let script = script(64);
    let mut owned = script.subscribe();
    owned.close();
    assert!(next(&mut owned).is_none());
    let mut watcher = script.subscribe();
    block_on(script.session.close()).unwrap();
    assert!(next(&mut watcher).is_none());
    assert!(matches!(
        script.hub.subscribe(&"s".into()),
        Err(HubError::UnknownSession(_))
    ));
}

#[test]
fn an_adapter_overflow_cuts_only_the_subscribers_still_exposed_to_its_run() {
    let script = script(64);
    let mut moved_on = script.subscribe();
    let lagging = script.subscribe();
    let old = script.submit();
    script.emit(&old, 1);
    assert_eq!(next(&mut moved_on).unwrap().unwrap().sequence, Some(1));
    let newer = script.submit();
    script.emit(&newer, 1);
    assert_eq!(
        next(&mut moved_on).unwrap().unwrap().run_id.as_ref(),
        Some(&newer)
    );
    let mut lagging = lagging;
    script.overflow(&old);
    settle();
    script.emit(&newer, 2);
    assert_eq!(next(&mut moved_on).unwrap().unwrap().sequence, Some(2));
    assert_eq!(next(&mut lagging).unwrap().unwrap().sequence, Some(1));
    assert_eq!(
        next(&mut lagging).unwrap().unwrap().run_id.as_ref(),
        Some(&newer)
    );
    match next(&mut lagging).unwrap().unwrap_err() {
        HubError::Overflow {
            run_id,
            last_sequence,
        } => {
            assert_eq!((run_id, last_sequence), (old.clone(), 1));
        }
        other => panic!("{other:?}"),
    }
    script.end(&old);
    script.end(&newer);
    assert!(next(&mut moved_on).is_none());
}

#[test]
fn a_run_admitted_without_its_id_is_bound_from_its_first_envelope() {
    let script = scripted_with(64, true);
    let mut watcher = script.subscribe();
    assert_eq!(script.submit_anyway(), None);
    let run = script.senders.lock().unwrap().last().unwrap().0.clone();
    script.emit(&run, 1);
    assert_eq!(
        next(&mut watcher).unwrap().unwrap().run_id.as_ref(),
        Some(&run)
    );
    assert_eq!(script.session.current_run(), Some(run.clone()));
    script.end(&run);
    assert!(next(&mut watcher).is_none());
}

#[test]
fn a_failure_of_one_of_two_runs_reaches_the_subscribers_reading_it() {
    let script = script(64);
    let mut watcher = script.subscribe();
    let first = script.submit();
    script.emit(&first, 1);
    assert_eq!(next(&mut watcher).unwrap().unwrap().sequence, Some(1));
    let second = script.submit();
    let mut on_second = script.subscribe();
    script.emit(&second, 1);
    assert_eq!(
        next(&mut on_second).unwrap().unwrap().run_id.as_ref(),
        Some(&second)
    );
    assert_eq!(
        next(&mut watcher).unwrap().unwrap().run_id.as_ref(),
        Some(&second)
    );
    script.fail(&second);
    settle();
    script.end(&first);
    assert!(matches!(
        next(&mut watcher),
        Some(Err(HubError::Adapter(Error::Harness(_))))
    ));
    assert!(matches!(
        next(&mut on_second),
        Some(Err(HubError::Adapter(Error::Harness(_))))
    ));
}

#[test]
fn a_failure_of_an_older_run_ends_only_the_subscribers_still_on_it() {
    let script = script(64);
    let lagging = script.subscribe();
    let first = script.submit();
    script.emit(&first, 1);
    settle();
    let second = script.submit();
    let mut on_second = script.subscribe();
    script.emit(&second, 1);
    assert_eq!(
        next(&mut on_second).unwrap().unwrap().run_id.as_ref(),
        Some(&second)
    );
    let mut lagging = lagging;
    script.fail(&first);
    settle();
    script.emit(&second, 2);
    assert_eq!(next(&mut on_second).unwrap().unwrap().sequence, Some(2));
    assert_eq!(next(&mut lagging).unwrap().unwrap().sequence, Some(1));
    assert_eq!(
        next(&mut lagging).unwrap().unwrap().run_id.as_ref(),
        Some(&second)
    );
    assert!(matches!(
        next(&mut lagging),
        Some(Err(HubError::Adapter(Error::Harness(_))))
    ));
    script.end(&second);
    assert!(next(&mut on_second).is_none());
}

#[test]
fn closing_a_session_with_a_run_still_being_read_ends_its_subscribers_once_the_run_drains() {
    let script = script(64);
    let mut watcher = script.subscribe();
    let run = script.submit();
    script.emit(&run, 1);
    block_on(script.session.close()).unwrap();
    assert_eq!(next(&mut watcher).unwrap().unwrap().sequence, Some(1));
    script.emit(&run, 2);
    assert_eq!(next(&mut watcher).unwrap().unwrap().sequence, Some(2));
    script.end(&run);
    assert!(next(&mut watcher).is_none());
}

#[test]
fn a_held_failure_does_not_carry_into_a_run_promoted_after_it() {
    let script = script(64);
    let mut on_first = script.subscribe();
    let first = script.submit();
    script.emit(&first, 1);
    assert_eq!(next(&mut on_first).unwrap().unwrap().sequence, Some(1));
    let queued = script.submit_queued();
    script.fail(&first);
    settle();
    let mut on_queued = script.subscribe();
    script.emit(&queued, 1);
    assert!(matches!(
        next(&mut on_first),
        Some(Err(HubError::Adapter(Error::Harness(_))))
    ));
    assert_eq!(
        next(&mut on_queued).unwrap().unwrap().run_id.as_ref(),
        Some(&queued)
    );
    script.end(&queued);
    assert!(next(&mut on_queued).is_none());
}

#[test]
fn a_late_envelope_of_an_older_run_does_not_expose_a_subscriber_reading_a_newer_one() {
    let script = script(64);
    let mut reader = script.subscribe();
    let old = script.submit();
    script.emit(&old, 1);
    assert_eq!(next(&mut reader).unwrap().unwrap().sequence, Some(1));
    let newer = script.submit();
    script.emit(&newer, 1);
    assert_eq!(
        next(&mut reader).unwrap().unwrap().run_id.as_ref(),
        Some(&newer)
    );
    script.emit(&old, 2);
    settle();
    script.overflow(&old);
    settle();
    script.emit(&newer, 2);
    assert_eq!(next(&mut reader).unwrap().unwrap().sequence, Some(2));
    assert_eq!(
        next(&mut reader).unwrap().unwrap().run_id.as_ref(),
        Some(&newer)
    );
    script.end(&old);
    script.end(&newer);
    assert!(next(&mut reader).is_none());
}

#[test]
fn a_subscription_after_a_run_ended_reports_no_join_point() {
    let script = script(64);
    let mut watcher = script.subscribe();
    let run = script.submit();
    script.emit(&run, 1);
    script.emit(&run, 2);
    next(&mut watcher).unwrap().unwrap();
    next(&mut watcher).unwrap().unwrap();
    script.end(&run);
    assert!(next(&mut watcher).is_none());
    assert_eq!(script.subscribe().joined_at(), None);
}

#[test]
fn an_overflow_caused_by_a_late_envelope_of_an_older_run_names_the_current_run() {
    let script = script(2);
    let mut full = script.subscribe();
    let old = script.submit();
    script.emit(&old, 1);
    assert_eq!(next(&mut full).unwrap().unwrap().sequence, Some(1));
    let current = script.submit();
    script.emit(&current, 1);
    script.emit(&current, 2);
    settle();
    script.emit(&old, 2);
    settle();
    assert_eq!(next(&mut full).unwrap().unwrap().sequence, Some(1));
    assert_eq!(next(&mut full).unwrap().unwrap().sequence, Some(2));
    match next(&mut full) {
        Some(Err(HubError::Overflow {
            run_id,
            last_sequence,
        })) => {
            assert_eq!((run_id, last_sequence), (current.clone(), 2));
        }
        other => panic!("{:?}", other.map(|item| item.map(|e| e.sequence))),
    }
    script.end(&old);
    script.end(&current);
}

#[test]
fn a_submission_still_in_flight_keeps_the_subscribers_open_for_its_run() {
    let script = Arc::new(script(64));
    let mut watcher = script.subscribe();
    let first = script.submit();
    script.emit(&first, 1);
    assert_eq!(next(&mut watcher).unwrap().unwrap().sequence, Some(1));
    let (release, hold) = futures::channel::oneshot::channel();
    *script.hold.lock().unwrap() = Some(hold);
    let submitting = script.clone();
    let pending = thread::spawn(move || submitting.submit_queued());
    settle();
    script.end(&first);
    settle();
    release.send(()).unwrap();
    let queued = pending.join().unwrap();
    script.emit(&queued, 1);
    let delivered = next(&mut watcher).unwrap().unwrap();
    assert_eq!(delivered.run_id.as_ref(), Some(&queued));
    script.end(&queued);
    assert!(next(&mut watcher).is_none());
}

#[test]
fn a_submission_dropped_while_in_flight_releases_its_reservation() {
    let script = script(64);
    let mut watcher = script.subscribe();
    let first = script.submit();
    script.emit(&first, 1);
    assert_eq!(next(&mut watcher).unwrap().unwrap().sequence, Some(1));
    let (_release, hold) = futures::channel::oneshot::channel::<()>();
    *script.hold.lock().unwrap() = Some(hold);
    {
        let mut pending = Box::pin(script.session.submit(SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "s".into(),
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        }));
        assert!(futures::FutureExt::now_or_never(pending.as_mut()).is_none());
    }
    script.end(&first);
    assert!(next(&mut watcher).is_none());
}
