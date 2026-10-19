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

pub fn approval(run: &RunId, permission: &PermissionRequestedPayload) -> InteractionResolution {
    InteractionResolution {
        run_id: run.clone(),
        responded_by: permission.responded_by.clone(),
        permission: Some(PermissionResolveRequest {
            interaction_id: permission.interaction_id.clone(),
            session_id: "session-1".into(),
            run_id: run.clone(),
            requested_by: permission.requested_by.clone(),
            responded_by: permission.responded_by.clone(),
            choice_id: "approve".into(),
            granted: true,
            ..PermissionResolveRequest::default()
        }),
        input: None,
    }
}

pub fn yes() -> Vec<InputAnswer> {
    vec![InputAnswer {
        question_id: "choice".into(),
        selected_option_ids: vec!["yes".into()],
        ..InputAnswer::default()
    }]
}

pub fn answering(
    run: &RunId,
    input: &UserInputRequestedPayload,
    answers: Vec<InputAnswer>,
) -> InteractionResolution {
    InteractionResolution {
        run_id: run.clone(),
        responded_by: input.responded_by.clone(),
        permission: None,
        input: Some(UserInputResolveRequest {
            interaction_id: input.interaction_id.clone(),
            session_id: "session-1".into(),
            run_id: run.clone(),
            requested_by: input.requested_by.clone(),
            responded_by: input.responded_by.clone(),
            answers,
        }),
    }
}

pub fn resolve(session: &MemorySession, resolution: InteractionResolution) -> Result<(), Error> {
    session.resolve(resolution)
}

pub fn approve(session: &MemorySession, run: &RunId, events: &[Envelope]) {
    let permission: PermissionRequestedPayload =
        of_type(events, EnvelopeType::ACTION_PERMISSION_REQUESTED)
            .decode_payload()
            .unwrap();
    resolve(session, approval(run, &permission)).unwrap();
}

pub fn answer(session: &MemorySession, run: &RunId, events: &[Envelope]) {
    let input: UserInputRequestedPayload = of_type(events, EnvelopeType::USER_INPUT_REQUESTED)
        .decode_payload()
        .unwrap();
    resolve(session, answering(run, &input, yes())).unwrap();
}

pub fn resolve_gates(
    session: &MemorySession,
    run: &RunId,
    stream: &mut EventStream,
    drained: &[Envelope],
) -> Vec<Envelope> {
    approve(session, run, drained);
    let mut rest = drain(stream);
    answer(session, run, &rest);
    rest.extend(drain(stream));
    rest
}

pub fn cancel(session: &MemorySession, run: &RunId) -> Result<RunCancelResponse, Error> {
    session.cancel(run)
}

pub fn state(session: &MemorySession) -> SessionState {
    session.snapshot()
}

pub fn idle(session: &MemorySession) -> bool {
    let state = state(session);
    state.status == SessionStatus::IDLE && state.active_run_id.is_none()
}

pub fn resume(
    session: &MemorySession,
    run: &RunId,
    after: u64,
) -> Result<(Recovery, EventStream), Error> {
    session.resume(ResumeRequest {
        run_id: run.clone(),
        after_sequence: after,
    })
}

pub struct Submission {
    pub request: MessageSubmitRequest,
    pub admission: MessageSubmitResponse,
    pub cancelled: bool,
}

pub fn submission(request: MessageSubmitRequest, admission: &MessageSubmitResponse) -> Submission {
    Submission {
        request,
        admission: admission.clone(),
        cancelled: false,
    }
}

pub fn envelope(kind: EnvelopeType, id: &str, payload: &impl serde::Serialize) -> Envelope {
    Envelope::new(kind, id, payload).unwrap()
}

pub fn is_terminal(kind: &EnvelopeType) -> bool {
    [
        EnvelopeType::RUN_COMPLETED,
        EnvelopeType::RUN_FAILED,
        EnvelopeType::RUN_CANCELLED,
    ]
    .contains(kind)
}

pub fn is_exchange(kind: &EnvelopeType) -> bool {
    kind.as_str().ends_with(".request") || kind.as_str().ends_with(".response")
}

pub fn invariants(submissions: &[Submission], events: &[Envelope]) {
    let mut next: HashMap<RunId, u64> = HashMap::new();
    let mut terminals: HashMap<RunId, usize> = HashMap::new();
    for submission in submissions {
        let run = submission.admission.run_id.clone().unwrap();
        next.insert(run, 1);
    }
    for (index, event) in events.iter().enumerate() {
        if is_exchange(&event.envelope_type) {
            continue;
        }
        let run = event.run_id.clone().unwrap();
        let expected = next
            .get_mut(&run)
            .unwrap_or_else(|| panic!("event {index} names an unadmitted run"));
        assert_eq!(
            event.session_id.as_ref().map(SessionId::as_str),
            Some("session-1"),
            "event {index}"
        );
        assert_eq!(event.sequence, Some(*expected), "event {index} sequence");
        *expected += 1;
        assert_eq!(
            event.capability_revision.as_deref(),
            Some(CAPABILITY_REVISION),
            "event {index}"
        );
        let count = terminals.entry(run).or_default();
        assert_eq!(*count, 0, "event {index} follows its run's terminal");
        if is_terminal(&event.envelope_type) {
            *count += 1;
        }
    }
    for run in next.keys() {
        assert_eq!(
            terminals.get(run),
            Some(&1),
            "run {} terminals",
            run.as_str()
        );
    }
}

pub fn cancel_cut(events: &[Envelope], run: &RunId) -> usize {
    let ours = |event: &&Envelope| event.run_id.as_ref() == Some(run);
    let cancelling = events.iter().position(|event| {
        ours(&event)
            && event.envelope_type == EnvelopeType::RUN_STATUS_UPDATED
            && event
                .decode_payload::<RunStatusUpdatedPayload>()
                .is_ok_and(|payload| payload.status == RunStatus::CANCELLING)
    });
    cancelling
        .or_else(|| {
            events
                .iter()
                .rposition(|event| ours(&event) && is_terminal(&event.envelope_type))
        })
        .unwrap_or(events.len())
}

pub fn trace(
    open: &OpenRequest,
    submissions: &[Submission],
    descriptor: &Descriptor,
    events: &[Envelope],
) -> Vec<u8> {
    let first = &submissions[0].admission;
    let session = first.session_id.clone();
    let revision = Some(descriptor.capability_revision.clone());
    let initialize = envelope(
        EnvelopeType::PROTOCOL_INITIALIZE_REQUEST,
        "initialize-request",
        &InitializeRequest {
            protocol_versions: vec![VERSION.into()],
            profiles: vec![PROFILE.into()],
            participant: Some(open.participant.clone()),
        },
    );
    let mut initialized = envelope(
        EnvelopeType::PROTOCOL_INITIALIZE_RESPONSE,
        "initialize-response",
        &InitializeResponse {
            protocol_version: VERSION.into(),
            profile: PROFILE.into(),
            endpoint: descriptor.capabilities.endpoint.clone(),
        },
    );
    initialized.in_reply_to = Some(initialize.id.clone());
    let asked = envelope(
        EnvelopeType::CAPABILITIES_REQUEST,
        "capabilities-request",
        &CapabilitiesRequest {},
    );
    let mut described = envelope(
        EnvelopeType::CAPABILITIES_RESPONSE,
        "capabilities-response",
        &descriptor.capabilities,
    );
    described.in_reply_to = Some(asked.id.clone());
    described.capability_revision = revision.clone();
    let mut opening = envelope(
        EnvelopeType::SESSION_OPEN_REQUEST,
        "open-request",
        &SessionOpenRequest {
            session_id: Some(session.clone()),
            ..SessionOpenRequest::default()
        },
    );
    let mut opened = envelope(
        EnvelopeType::SESSION_OPEN_RESPONSE,
        "open-response",
        &SessionState {
            session_id: session.clone(),
            status: SessionStatus::IDLE,
            ..SessionState::default()
        },
    );
    opened.session_id = Some(session.clone());
    opened.in_reply_to = Some(opening.id.clone());
    opening.capability_revision = revision.clone();
    opened.capability_revision = revision.clone();
    let mut trace = vec![initialize, initialized, asked, described, opening, opened];
    for (index, submission) in submissions.iter().enumerate() {
        let mut request = envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
            &format!("submit-request-{}", index + 1),
            &submission.request,
        );
        request.session_id = Some(session.clone());
        request.capability_revision = revision.clone();
        let mut response = envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE,
            &format!("submit-response-{}", index + 1),
            &submission.admission,
        );
        response.session_id = Some(session.clone());
        response.in_reply_to = Some(request.id.clone());
        response.capability_revision = revision.clone();
        trace.extend([request, response]);
    }
    let mut events = events.to_vec();
    for (index, submission) in submissions.iter().enumerate().filter(|(_, s)| s.cancelled) {
        let run = submission.admission.run_id.clone().unwrap();
        let mut request = envelope(
            EnvelopeType::RUN_CANCEL_REQUEST,
            &format!("cancel-request-{}", index + 1),
            &RunCancelRequest {
                session_id: session.clone(),
                run_id: run.clone(),
                ..RunCancelRequest::default()
            },
        );
        request.session_id = Some(session.clone());
        request.run_id = Some(run.clone());
        let mut ack = envelope(
            EnvelopeType::RUN_CANCEL_RESPONSE,
            &format!("cancel-response-{}", index + 1),
            &RunCancelResponse {
                session_id: session.clone(),
                run_id: run.clone(),
                accepted: true,
                status: RunStatus::CANCELLING,
            },
        );
        ack.session_id = Some(session.clone());
        ack.run_id = Some(run.clone());
        ack.in_reply_to = Some(request.id.clone());
        let cut = cancel_cut(&events, &run);
        events.splice(cut..cut, [request, ack]);
    }
    trace.extend(events);
    serde_json::to_vec(&trace).unwrap()
}

pub fn validate(
    open: &OpenRequest,
    submissions: &[Submission],
    events: &[Envelope],
) -> oap_validation::ValidationResult {
    let data = trace(open, submissions, &descriptor(), events);
    oap_validation::Validator::new()
        .unwrap()
        .validate(&data, "memory")
}

pub fn assert_valid(submissions: &[Submission], events: &[Envelope]) {
    invariants(submissions, events);
    let result = validate(&user_open(), submissions, events);
    assert!(result.valid(), "{:#?}", result.diagnostics);
}

pub fn state_exchange(snapshot: &SessionState) -> [Envelope; 2] {
    let mut request = envelope(
        EnvelopeType::SESSION_STATE_REQUEST,
        "state-request",
        &SessionStateRequest {
            session_id: snapshot.session_id.clone(),
        },
    );
    request.session_id = Some(snapshot.session_id.clone());
    let mut response = envelope(
        EnvelopeType::SESSION_STATE_RESPONSE,
        "state-response",
        snapshot,
    );
    response.session_id = Some(snapshot.session_id.clone());
    response.in_reply_to = Some(request.id.clone());
    [request, response]
}

pub fn splice_after(
    events: &[Envelope],
    after: &EnvelopeId,
    exchange: [Envelope; 2],
) -> Vec<Envelope> {
    let index = events.iter().position(|event| &event.id == after).unwrap() + 1;
    let mut spliced = events.to_vec();
    spliced.splice(index..index, exchange);
    spliced
}

pub fn refusal(err: Error) -> Box<UnsupportedControl> {
    match err {
        Error::UnsupportedControl(control) => control,
        other => panic!("got {other:?}, want a typed unsupported-control refusal"),
    }
}

pub type Mutation = (&'static str, fn(&mut PermissionResolveRequest));

pub const INITIAL: [EnvelopeType; 4] = [
    EnvelopeType::RUN_STARTED,
    EnvelopeType::CONTENT_DELTA,
    EnvelopeType::ACTION_CALL_REQUESTED,
    EnvelopeType::ACTION_PERMISSION_REQUESTED,
];
