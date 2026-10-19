#![allow(dead_code, unused_imports)]

use std::collections::HashMap;
use std::sync::atomic::{AtomicI64, AtomicU64, Ordering};
use std::sync::Arc;

pub use futures::executor::block_on;
pub use futures::{FutureExt, StreamExt};
pub use oap_adapter::memory::{
    Clock, Config, IdGenerator, Memory, MemorySession, CAPABILITY_REVISION, MODEL_SECONDARY,
};
pub use oap_adapter::Error;
pub use oap_adapter::*;
pub use oap_protocol::*;
use serde_json::value::RawValue;

#[derive(Default)]
pub struct FixedClock(AtomicI64);

impl Clock for FixedClock {
    fn now_ms(&self) -> i64 {
        self.0.fetch_add(1, Ordering::SeqCst) + 1
    }
}

#[derive(Default)]
pub struct FixedIds(AtomicU64);

impl IdGenerator for FixedIds {
    fn new_id(&self, kind: &str) -> String {
        format!("{kind}-{:02}", self.0.fetch_add(1, Ordering::SeqCst) + 1)
    }
}

pub fn memory(capacity: usize) -> Memory {
    Memory::new(Config {
        clock: Some(Arc::new(FixedClock::default())),
        ids: Some(Arc::new(FixedIds::default())),
        journal_capacity: capacity,
    })
}

pub fn user_open() -> OpenRequest {
    OpenRequest {
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

pub fn open_with(memory: &Memory, participant: &str) -> MemorySession {
    let request = OpenRequest {
        session_id: "session-1".into(),
        participant: Participant {
            id: participant.into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    };
    memory.open_session(&request).unwrap()
}

pub fn session(capacity: usize) -> MemorySession {
    open_with(&memory(capacity), "user")
}

pub fn descriptor() -> Descriptor {
    memory(64).describe()
}

pub fn go() -> Vec<Message> {
    vec![Message {
        role: MessageRole::USER,
        content: MessageContent::from_text("go"),
        ..Message::default()
    }]
}

pub fn plain() -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: "session-1".into(),
        delivery: RequestedDeliveryMode::AUTO,
        messages: go(),
        ..MessageSubmitRequest::default()
    }
}

pub fn raw(text: &str) -> Option<Box<RawValue>> {
    Some(RawValue::from_string(text.to_owned()).unwrap())
}

pub fn try_submit(
    session: &MemorySession,
    request: MessageSubmitRequest,
) -> Result<(MessageSubmitResponse, EventStream), Error> {
    session.submit(SubmitRequest {
        request,
        ..SubmitRequest::default()
    })
}

pub fn submit(
    session: &MemorySession,
    request: MessageSubmitRequest,
) -> (MessageSubmitResponse, EventStream) {
    try_submit(session, request).unwrap()
}

pub fn started(session: &MemorySession) -> (MessageSubmitResponse, EventStream) {
    let (admission, stream) = submit(session, plain());
    assert!(
        admission.accepted && admission.admission == Admission::STARTED,
        "{admission:?}"
    );
    (admission, stream)
}

pub fn drain(stream: &mut EventStream) -> Vec<Envelope> {
    let mut events = Vec::new();
    while let Some(Some(item)) = stream.next().now_or_never() {
        events.push(item.unwrap());
    }
    events
}

pub fn of_type(events: &[Envelope], wanted: EnvelopeType) -> &Envelope {
    events
        .iter()
        .find(|event| event.envelope_type == wanted)
        .unwrap_or_else(|| panic!("no {} in {} envelopes", wanted.as_str(), events.len()))
}

pub fn types_and_sequence(events: &[Envelope], want: &[EnvelopeType], first: u64) {
    let got: Vec<&str> = events
        .iter()
        .map(|event| event.envelope_type.as_str())
        .collect();
    let wanted: Vec<&str> = want.iter().map(EnvelopeType::as_str).collect();
    assert_eq!(got, wanted);
    for (index, event) in events.iter().enumerate() {
        assert_eq!(event.sequence, Some(first + index as u64), "event {index}");
        let bound = event.envelope_type.as_str().starts_with("action.call.")
            || event.envelope_type == EnvelopeType::ACTION_PERMISSION_REQUESTED;
        if bound {
            assert!(
                event.tool_call_id.is_some(),
                "event {index} lacks tool_call_id correlation"
            );
        }
    }
}

pub fn state(session: &MemorySession) -> SessionState {
    session.snapshot()
}

pub fn idle(session: &MemorySession) -> bool {
    let state = state(session);
    state.status == SessionStatus::IDLE && state.active_run_id.is_none()
}

pub fn refusal(err: Error) -> Box<UnsupportedControl> {
    match err {
        Error::UnsupportedControl(control) => control,
        other => panic!("got {other:?}, want a typed unsupported-control refusal"),
    }
}

pub const INITIAL: [EnvelopeType; 4] = [
    EnvelopeType::RUN_STARTED,
    EnvelopeType::CONTENT_DELTA,
    EnvelopeType::ACTION_CALL_REQUESTED,
    EnvelopeType::ACTION_PERMISSION_REQUESTED,
];
