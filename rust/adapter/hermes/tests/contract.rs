mod support;

use std::future::Future;
use std::pin::Pin;
use std::sync::{Arc, Mutex};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::task::noop_waker;
use oap_adapter::gojson::go_marshal;
use oap_adapter::testkit::drain;
use oap_adapter::{
    Adapter as _, Error, InteractionResolution, NativeListRequest, OpenRequest, ResumeRequest,
    SubmitRequest,
};
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::client::{Client, ClientFactory};
use oap_protocol::{
    InputAnswer, ReasoningLevel, RunId, SessionSettingsUpdateRequest, UserInputResolveRequest,
};
use serde_json::value::RawValue;
use support::{go_adapter, go_runtime, request, submission, Fake};

fn wait<F: Future + Unpin>(mut future: F) -> F::Output {
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if let Poll::Ready(value) =
            Pin::new(&mut future).poll(&mut Context::from_waker(&noop_waker()))
        {
            return value;
        }
        assert!(Instant::now() < deadline, "the operation never finished");
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn json(value: &impl serde::Serialize) -> String {
    serde_json::to_string(value).unwrap()
}

struct Lister(Mutex<Vec<usize>>);

impl ClientFactory for Lister {
    fn start(&self, _: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        Err(Error::Harness("unused".into()))
    }

    fn list(&self, limit: usize) -> Option<Result<Box<RawValue>, Error>> {
        self.0.lock().unwrap().push(limit);
        Some(Ok(RawValue::from_string(
            r#"{"sessions":[{"id":"stored-a","title":"Named","started_at":1774717612.4789},{"id":"stored-b","preview":"first words"}]}"#.into(),
        )
        .unwrap()))
    }
}

#[test]
fn the_contract_serves_the_session_go_serves() {
    let fake = Fake::stored(
        [
            Ok(r#"{"status":"streaming"}"#),
            Ok(r#"{"status":"interrupted"}"#),
        ],
        "stored-1",
    );
    let adapter = go_adapter(&fake);
    let descriptor = wait(adapter.probe()).unwrap();
    assert_eq!(
        format!("{descriptor:?}"),
        format!("{:?}", adapter.describe())
    );
    let session = wait(oap_adapter::Adapter::open(
        &adapter,
        OpenRequest {
            session_id: "session".into(),
            ..request()
        },
    ))
    .unwrap();
    assert_eq!(session.native_session_id().as_deref(), Some("stored-1"));
    let (response, mut stream) = std::thread::scope(|scope| {
        let submitted = scope.spawn(|| {
            wait(session.submit(SubmitRequest {
                request: submission(),
                ..SubmitRequest::default()
            }))
        });
        fake.await_call("prompt.submit", 1);
        fake.event("message.start", 1, "");
        submitted.join().unwrap().unwrap()
    });
    assert_eq!(response.run_id, Some(RunId::from("run-b")));
    assert_eq!(
        json(&wait(session.cancel(RunId::from("run-b"))).unwrap()),
        r#"{"session_id":"session","run_id":"run-b","accepted":true,"status":"cancelling"}"#
    );
    let updater = session.settings_updater().unwrap();
    assert_eq!(
        wait(updater.update_settings(SessionSettingsUpdateRequest {
            session_id: "session".into(),
            reasoning_level: ReasoningLevel::LOW,
            ..SessionSettingsUpdateRequest::default()
        }))
        .err(),
        Some(Error::RunActive)
    );
    fake.event(
        "message.complete",
        2,
        r#"{"text":"half","status":"interrupted"}"#,
    );
    let events: Vec<String> = drain(&mut stream, Duration::from_secs(5))
        .iter()
        .map(|envelope| go_marshal(&json(envelope)))
        .collect();
    assert_eq!(
        events,
        [
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"event-c","payload":{"session_id":"session","run_id":"run-b","status":"running","model_id":"hermes-test","started_at_ms":2},"sequence":1,"timestamp_ms":3,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
            r#"{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.failed","id":"event-d","payload":{"session_id":"session","run_id":"run-b","error":{"code":"hermes_interrupted","message":"half"}},"sequence":2,"timestamp_ms":4,"session_id":"session","run_id":"run-b","capability_revision":"hermes-v0.23.1-oap-v1"}"#,
        ]
    );
    assert_eq!(json(&wait(session.state()).unwrap().status), r#""idle""#);
    let (recovery, mut replay) = wait(session.resume(ResumeRequest {
        run_id: "run-b".into(),
        after_sequence: 1,
    }))
    .unwrap();
    assert_eq!(
        (
            recovery.requested_after,
            recovery.replayed_from,
            recovery.replayed_through
        ),
        (1, 2, 2)
    );
    assert_eq!(
        drain(&mut replay, Duration::from_secs(5))
            .iter()
            .map(|envelope| go_marshal(&json(envelope)))
            .collect::<Vec<_>>(),
        events[1..]
    );
    assert_eq!(
        wait(session.resolve(InteractionResolution {
            input: Some(UserInputResolveRequest {
                interaction_id: "interaction-d".into(),
                session_id: "session".into(),
                answers: vec![InputAnswer {
                    question_id: "choice".into(),
                    selected_option_ids: vec!["once".into()],
                    ..InputAnswer::default()
                }],
                ..UserInputResolveRequest::default()
            }),
            ..InteractionResolution::default()
        })),
        Err(Error::InteractionNotFound)
    );
    assert_eq!(wait(session.close()), Ok(()));
    assert_eq!(wait(session.state()).err(), Some(Error::SessionClosed));
    assert_eq!(fake.closes(), 1);
}

#[test]
fn the_contract_lists_the_stored_sessions_go_lists() {
    let lister = Arc::new(Lister(Mutex::new(Vec::new())));
    let adapter = Adapter::new(Config {
        factory: Some(lister.clone()),
        model: "hermes-test".into(),
        runtime: go_runtime(),
        ..Config::default()
    })
    .unwrap();
    let listed = wait(
        adapter
            .native_lister()
            .unwrap()
            .native_list(NativeListRequest {
                limit: 2,
                ..NativeListRequest::default()
            }),
    )
    .unwrap();
    assert_eq!(
        listed
            .iter()
            .map(|row| (
                row.native_id.as_str(),
                row.title.as_str(),
                row.updated_at_ms
            ))
            .collect::<Vec<_>>(),
        [
            ("stored-a", "Named", 1774717612478),
            ("stored-b", "first words", 0)
        ]
    );
    assert_eq!(*lister.0.lock().unwrap(), [2]);
}
