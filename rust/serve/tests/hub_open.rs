use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};

use futures::executor::block_on;
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, Error, EventStream, InteractionResolution, OpenRequest,
    Recovery, ResumeRequest, Session, SubmitRequest,
};
use oap_protocol::{MessageSubmitResponse, RunCancelResponse, RunId, SessionState, SessionStatus};
use oap_serve::{Hub, HubError, Registry, DEFAULT_PARTICIPANT};

#[derive(Clone, Copy)]
enum Answer {
    Live,
    Fails,
    Closed,
    ClosedState,
    ClosesAfterOpen,
    StateClosesAfterOpen,
}

#[derive(Default)]
struct Seen {
    participants: Vec<String>,
    closed: Vec<String>,
}

struct Manual {
    answer: Answer,
    seen: Arc<Mutex<Seen>>,
}

struct ManualSession {
    id: String,
    answer: Answer,
    seen: Arc<Mutex<Seen>>,
    closed: AtomicBool,
    asked: AtomicBool,
}

impl Adapter for Manual {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        Box::pin(async { Ok(Descriptor::default()) })
    }

    fn open(&self, request: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        self.seen
            .lock()
            .unwrap()
            .participants
            .push(request.participant.id.to_string());
        let session: Box<dyn Session> = Box::new(ManualSession {
            id: request.session_id.to_string(),
            answer: self.answer,
            seen: self.seen.clone(),
            closed: AtomicBool::new(false),
            asked: AtomicBool::new(false),
        });
        Box::pin(async move { Ok(session) })
    }
}

impl Session for ManualSession {
    fn submit(&self, _: SubmitRequest) -> BoxFuture<'_, (MessageSubmitResponse, EventStream)> {
        Box::pin(async { Err(Error::SessionClosed) })
    }

    fn state(&self) -> BoxFuture<'_, SessionState> {
        let state = SessionState {
            session_id: self.id.as_str().into(),
            status: SessionStatus::IDLE,
            ..SessionState::default()
        };
        let asked_before = self.asked.swap(true, Ordering::SeqCst);
        let answer = match self.answer {
            _ if self.closed.load(Ordering::SeqCst) => Answer::Closed,
            Answer::ClosesAfterOpen if asked_before => Answer::Closed,
            Answer::ClosesAfterOpen => Answer::Live,
            Answer::StateClosesAfterOpen if asked_before => Answer::ClosedState,
            Answer::StateClosesAfterOpen => Answer::Live,
            other => other,
        };
        Box::pin(async move {
            match answer {
                Answer::Live | Answer::ClosesAfterOpen | Answer::StateClosesAfterOpen => Ok(state),
                Answer::ClosedState => Ok(SessionState {
                    status: SessionStatus::CLOSED,
                    ..state
                }),
                Answer::Fails => Err(Error::harness("state probe failed")),
                Answer::Closed => Err(Error::SessionClosed),
            }
        })
    }

    fn resolve(&self, _: InteractionResolution) -> BoxFuture<'_, ()> {
        Box::pin(async { Err(Error::SessionClosed) })
    }

    fn cancel(&self, _: RunId) -> BoxFuture<'_, RunCancelResponse> {
        Box::pin(async { Err(Error::SessionClosed) })
    }

    fn resume(&self, _: ResumeRequest) -> BoxFuture<'_, (Recovery, EventStream)> {
        Box::pin(async { Err(Error::SessionClosed) })
    }

    fn close(&self) -> BoxFuture<'_, ()> {
        self.closed.store(true, Ordering::SeqCst);
        self.seen.lock().unwrap().closed.push(self.id.clone());
        Box::pin(async { Ok(()) })
    }
}

fn hub_with(answer: Answer) -> (Hub, Arc<Mutex<Seen>>) {
    let seen = Arc::new(Mutex::new(Seen::default()));
    let mut registry = Registry::default_registry();
    registry
        .register(
            "manual",
            Arc::new(Manual {
                answer,
                seen: seen.clone(),
            }),
        )
        .unwrap();
    registry
        .register(
            "live",
            Arc::new(Manual {
                answer: Answer::Live,
                seen: seen.clone(),
            }),
        )
        .unwrap();
    (Hub::new(registry), seen)
}

fn request(id: &str) -> OpenRequest {
    OpenRequest {
        session_id: id.into(),
        ..OpenRequest::default()
    }
}

#[test]
fn an_open_names_its_adapter_and_its_id_once() {
    let (hub, _) = hub_with(Answer::Live);
    assert!(matches!(
        block_on(hub.open("ghost", request("s"))),
        Err(HubError::UnknownAdapter(name)) if name == "ghost"
    ));
    assert!(matches!(
        hub.session(&"ghost".into()),
        Err(HubError::UnknownSession(id)) if id.as_str() == "ghost"
    ));
    let opened = block_on(hub.open("memory", request("dup"))).unwrap();
    assert_eq!(opened.session.id().as_str(), "dup");
    assert_eq!(opened.session.adapter(), "memory");
    assert_eq!(opened.state.session_id.as_str(), "dup");
    assert!(matches!(
        block_on(hub.open("live", request("dup"))),
        Err(HubError::SessionExists(id)) if id.as_str() == "dup"
    ));
    assert_eq!(hub.session(&"dup".into()).unwrap().adapter(), "memory");
}

#[test]
fn an_open_without_a_participant_speaks_for_the_default_one() {
    let (hub, seen) = hub_with(Answer::Live);
    block_on(hub.open("manual", request("s"))).unwrap();
    assert_eq!(seen.lock().unwrap().participants, [DEFAULT_PARTICIPANT]);
}

#[test]
fn an_open_whose_state_fails_closes_the_adapter_session_and_lists_nothing() {
    let (hub, seen) = hub_with(Answer::Fails);
    let refused = block_on(hub.open("manual", request("state-fail")));
    assert!(matches!(refused, Err(HubError::Adapter(Error::Harness(_)))));
    assert_eq!(seen.lock().unwrap().closed, ["state-fail"]);
    assert!(hub.sessions().is_empty());
    block_on(hub.open("live", request("state-fail"))).unwrap();
}

#[test]
fn an_open_of_a_session_already_closed_is_refused_and_leaves_its_id_free() {
    let (hub, seen) = hub_with(Answer::Closed);
    match block_on(hub.open("manual", request("already-closed"))) {
        Err(HubError::ClosedAtOpen(state)) => {
            assert_eq!(state.session_id.as_str(), "already-closed");
            assert_eq!(state.status, SessionStatus::CLOSED);
        }
        other => panic!("open = {:?}", other.map(|opened| opened.state)),
    }
    assert!(hub.sessions().is_empty());
    assert!(hub.session(&"already-closed".into()).is_err());
    assert_eq!(seen.lock().unwrap().closed, ["already-closed"]);
    block_on(hub.open("live", request("already-closed"))).unwrap();
    assert_eq!(hub.sessions().len(), 1);
}

#[test]
fn a_closed_snapshot_at_open_closes_the_adapter_and_leaves_its_id_free() {
    let (hub, seen) = hub_with(Answer::ClosedState);
    assert!(matches!(
        block_on(hub.open("manual", request("s"))),
        Err(HubError::ClosedAtOpen(state)) if state.status == SessionStatus::CLOSED
    ));
    assert_eq!(seen.lock().unwrap().closed, ["s"]);
    assert!(hub.sessions().is_empty());
    assert!(hub.session(&"s".into()).is_err());
    block_on(hub.open("live", request("s"))).unwrap();
}

#[test]
fn a_closed_session_leaves_the_listing_and_frees_its_id() {
    let (hub, seen) = hub_with(Answer::Live);
    let first = block_on(hub.open("live", request("b"))).unwrap().session;
    block_on(hub.open("memory", request("a"))).unwrap();
    let listed: Vec<String> = hub
        .sessions()
        .iter()
        .map(|session| session.id().to_string())
        .collect();
    assert_eq!(listed, ["a", "b"]);
    block_on(first.close()).unwrap();
    assert!(first.is_closed());
    assert_eq!(seen.lock().unwrap().closed, ["b"]);
    assert!(hub.session(&"b".into()).is_err());
    assert_eq!(hub.sessions().len(), 1);
    block_on(hub.open("live", request("b"))).unwrap();
    assert!(matches!(
        block_on(first.state()),
        Err(HubError::Adapter(Error::SessionClosed))
    ));
}

#[test]
fn a_state_the_adapter_reports_closed_releases_the_session() {
    let (hub, seen) = hub_with(Answer::ClosesAfterOpen);
    let session = block_on(hub.open("manual", request("s"))).unwrap().session;
    assert_eq!(hub.sessions().len(), 1);
    assert!(matches!(
        block_on(session.state()),
        Err(HubError::Adapter(Error::SessionClosed))
    ));
    assert!(session.is_closed());
    assert!(hub.sessions().is_empty());
    assert!(seen.lock().unwrap().closed.is_empty());
    block_on(hub.open("live", request("s"))).unwrap();
}

#[test]
fn a_closed_snapshot_releases_the_session_and_allows_the_id_to_reopen() {
    let (hub, seen) = hub_with(Answer::StateClosesAfterOpen);
    let session = block_on(hub.open("manual", request("s"))).unwrap().session;
    assert_eq!(
        block_on(session.state()).unwrap().status,
        SessionStatus::CLOSED
    );
    assert!(hub.sessions().is_empty());
    assert!(hub.session(&"s".into()).is_err());
    assert!(seen.lock().unwrap().closed.is_empty());
    block_on(hub.open("live", request("s"))).unwrap();
}

#[test]
fn a_memory_session_reads_its_own_state() {
    let mut registry = Registry::new();
    registry
        .register("memory", Arc::new(Memory::new(Config::default())))
        .unwrap();
    let hub = Hub::new(registry);
    let session = block_on(hub.open("memory", request("s"))).unwrap().session;
    let state = block_on(session.state()).unwrap();
    assert_eq!(state.session_id.as_str(), "s");
    assert_eq!(state.status, SessionStatus::IDLE);
}
