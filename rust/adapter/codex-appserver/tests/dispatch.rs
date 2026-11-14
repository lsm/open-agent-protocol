mod support;

use std::sync::Arc;
use std::time::{Duration, Instant};

use oxap_adapter::testkit::drain;
use oxap_adapter_codex_appserver::session::Session;
use oxap_protocol::{Message, MessageContent, MessageRole, MessageSubmitRequest};
use support::{adapter, request, Fake};

fn open() -> (Arc<Fake>, Session) {
    let fake = Fake::new([
        Ok(r#"{"thread":{"id":"t-1"}}"#),
        Ok(r#"{"turn":{"id":"turn-1","status":"inProgress"}}"#),
    ]);
    let session = adapter(&fake, |_| {}).open(request()).unwrap();
    (fake, session)
}

#[test]
fn a_reverse_request_is_answered_as_unsupported() {
    let (fake, _session) = open();
    let answers = fake.request("item/tool/call", "{}");
    let deadline = Instant::now() + Duration::from_secs(5);
    while answers.0.lock().unwrap().is_empty() {
        assert!(Instant::now() < deadline, "the request was never answered");
        std::thread::sleep(Duration::from_millis(10));
    }
    let answer = &answers.0.lock().unwrap()[0];
    let error = answer.error.as_ref().unwrap();
    assert_eq!(
        (error.code, error.message.as_str()),
        (-32601, "unsupported Codex server request")
    );
    assert_eq!(
        error.data.as_ref().map(|data| data.get()),
        Some(r#"{"method":"item/tool/call"}"#)
    );
}

#[test]
fn a_submit_returns_when_codex_holds_its_answer_until_the_reader_catches_up() {
    let (fake, session) = open();
    fake.hold_replies_behind_barriers();
    let deadline = Instant::now() + Duration::from_secs(5);
    while !fake.activated() {
        assert!(
            Instant::now() < deadline,
            "the session never read Codex's queue"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
    let submitted = session.submit(MessageSubmitRequest {
        session_id: "s".into(),
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("hi"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    });
    assert!(submitted.is_ok(), "{:?}", submitted.err());
}

#[test]
fn a_submit_returns_when_codex_asks_something_ahead_of_its_answer() {
    let (fake, session) = open();
    fake.hold_replies_behind_barriers();
    let deadline = Instant::now() + Duration::from_secs(5);
    while !fake.activated() {
        assert!(
            Instant::now() < deadline,
            "the session never read Codex's queue"
        );
        std::thread::sleep(Duration::from_millis(10));
    }
    let answers = fake.request_ahead_of_next_reply("item/tool/call", "{}");
    let submitted = session.submit(MessageSubmitRequest {
        session_id: "s".into(),
        messages: vec![Message {
            role: MessageRole::USER,
            content: MessageContent::from_text("hi"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    });
    assert!(submitted.is_ok(), "{:?}", submitted.err());
    assert_eq!(answers.0.lock().unwrap().len(), 1);
}

#[test]
fn a_transport_closed_during_turn_start_fails_the_run_it_admits_as_go_does() {
    let (fake, session) = open();
    let session = Arc::new(session);
    let (arrivals, release) = fake.hold_calls("turn/start");
    let submitting = {
        let session = session.clone();
        std::thread::spawn(move || {
            session.submit(MessageSubmitRequest {
                session_id: "s".into(),
                messages: vec![Message {
                    role: MessageRole::USER,
                    content: MessageContent::from_text("hi"),
                    ..Message::default()
                }],
                ..MessageSubmitRequest::default()
            })
        })
    };
    arrivals.recv_timeout(Duration::from_secs(5)).unwrap();
    fake.hang_up();
    std::thread::sleep(Duration::from_millis(200));
    release.send(()).unwrap();
    let (_, mut stream) = submitting.join().unwrap().unwrap();
    let events = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        drain(&mut stream, Duration::from_secs(2))
    }))
    .unwrap_or_default();
    let shape: Vec<_> = events
        .iter()
        .map(|event| {
            (
                event.envelope_type.to_string(),
                serde_json::from_str::<serde_json::Value>(event.payload.as_ref().unwrap().get())
                    .unwrap(),
            )
        })
        .collect();
    assert_eq!(
        shape,
        [(
            "run.failed".to_owned(),
            serde_json::json!({"session_id": "s", "run_id": "run-minted", "error": {"code": "native_transport_closed", "message": "Codex app-server transport closed before terminal settlement"}, "settled_by": "inferred"})
        )]
    );
}

#[test]
fn a_client_that_panics_during_turn_start_leaves_close_free_to_return() {
    let (fake, session) = open();
    let session = Arc::new(session);
    fake.panic_on("turn/start");
    let submitted = std::panic::catch_unwind(std::panic::AssertUnwindSafe(|| {
        session.submit(MessageSubmitRequest {
            session_id: "s".into(),
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("hi"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        })
    }));
    assert!(submitted.is_err());
    let (closed, closing) = crossbeam_channel::bounded(1);
    {
        let session = session.clone();
        std::thread::spawn(move || closed.send(session.close().map_err(|error| error.to_string())));
    }
    assert_eq!(closing.recv_timeout(Duration::from_secs(5)), Ok(Ok(())));
    assert_eq!(
        session.state().map(drop).map_err(|error| error.to_string()),
        Err("adapter: session closed".to_owned())
    );
}

#[test]
fn a_cancel_during_turn_start_waits_for_the_turn_as_go_does() {
    let fake = Fake::new([
        Ok(r#"{"thread":{"id":"t-1"}}"#),
        Ok(r#"{"turn":{"id":"turn-1","status":"inProgress"}}"#),
    ]);
    fake.always_reply("turn/interrupt", Ok("{}"));
    let next = Arc::new(std::sync::atomic::AtomicU64::new(0));
    let session = Arc::new(
        adapter(&fake, |config| {
            config.runtime = oxap_adapter::runtime::Runtime::new(
                || 1,
                move |kind| {
                    format!(
                        "{kind}-{}",
                        next.fetch_add(1, std::sync::atomic::Ordering::SeqCst) + 1
                    )
                },
            );
        })
        .open(request())
        .unwrap(),
    );
    let (arrivals, release) = fake.hold_calls("turn/start");
    let submitting = {
        let session = session.clone();
        std::thread::spawn(move || {
            session
                .submit(MessageSubmitRequest {
                    session_id: "s".into(),
                    messages: vec![Message {
                        role: MessageRole::USER,
                        content: MessageContent::from_text("hi"),
                        ..Message::default()
                    }],
                    ..MessageSubmitRequest::default()
                })
                .map(|(response, _)| response.run_id)
        })
    };
    arrivals.recv_timeout(Duration::from_secs(5)).unwrap();
    let (entered, started) = crossbeam_channel::bounded(0);
    let (answered, settled) = crossbeam_channel::bounded(1);
    let cancelling = {
        let session = session.clone();
        std::thread::spawn(move || {
            let _ = entered.send(());
            let _ = answered.send(session.cancel(&"run-2".into()));
        })
    };
    started.recv_timeout(Duration::from_secs(5)).unwrap();
    let early = settled.recv_timeout(Duration::from_millis(500));
    assert!(
        matches!(early, Err(crossbeam_channel::RecvTimeoutError::Timeout)),
        "the cancel answered while turn/start was still in flight: {early:?}"
    );
    release.send(()).unwrap();
    let run = submitting.join().unwrap().unwrap();
    let answer = settled.recv_timeout(Duration::from_secs(5)).unwrap();
    cancelling.join().unwrap();
    assert_eq!(run.as_ref().map(|run| run.as_str()), Some("run-2"));
    assert_eq!(
        answer
            .map(|answer| serde_json::to_value(&answer).unwrap())
            .ok(),
        Some(
            serde_json::json!({"session_id": "s", "run_id": "run-2", "accepted": true, "status": "cancelling"})
        )
    );
    let interrupts: Vec<_> = fake
        .calls()
        .into_iter()
        .filter(|(method, _)| method == "turn/interrupt")
        .map(|(_, params)| params)
        .collect();
    assert_eq!(interrupts, [r#"{"threadId":"t-1","turnId":"turn-1"}"#]);
}
