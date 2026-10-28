use std::future::Future;
use std::pin::Pin;
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::future::{select, Either};
use futures::StreamExt;
use oap_protocol::{
    CapabilitiesRequest, Envelope, EnvelopeType, InitializeRequest, InitializeResponse, Message,
    MessageContent, MessageRole, MessageSubmitRequest, MessageSubmitResponse, RecoveryMetadata,
    RunCancelRequest, RunCancelResponse, RunStatus, RunStatusUpdatedPayload, SessionOpenRequest,
    SessionState, SessionStatus, SupportLevel, ToolsListRequest, FEATURE_INSTRUCTIONS,
    FEATURE_MODEL_SELECTION, FEATURE_STRUCTURED_OUTPUT, FEATURE_TOOL_SELECTION, PROFILE, VERSION,
};
use oap_validation::Validator;

use crate::{Descriptor, EventStream, OpenRequest, ToolCatalog};

pub const DEFAULT_TIMEOUT: Duration = Duration::from_secs(1);

struct Deadline {
    at: Instant,
    armed: bool,
}

impl Future for Deadline {
    type Output = ();

    fn poll(mut self: Pin<&mut Self>, cx: &mut Context<'_>) -> Poll<()> {
        let now = Instant::now();
        if now >= self.at {
            return Poll::Ready(());
        }
        if !self.armed {
            self.armed = true;
            let (waker, wait) = (cx.waker().clone(), self.at - now);
            std::thread::spawn(move || {
                std::thread::sleep(wait);
                waker.wake();
            });
        }
        Poll::Pending
    }
}

fn deadline(timeout: Duration) -> Deadline {
    Deadline {
        at: Instant::now() + timeout,
        armed: false,
    }
}

pub fn next(stream: &mut EventStream, timeout: Duration) -> Envelope {
    let waited = futures::executor::block_on(select(stream.next(), deadline(timeout)));
    match waited {
        Either::Left((Some(Ok(envelope)), _)) => envelope,
        Either::Left((Some(Err(error)), _)) => panic!("adapter event stream: {error}"),
        Either::Left((None, _)) => panic!("adapter event stream closed"),
        Either::Right(_) => panic!("timed out waiting for adapter event"),
    }
}

pub fn drain(stream: &mut EventStream, timeout: Duration) -> Vec<Envelope> {
    let mut limit = deadline(timeout);
    let mut envelopes = Vec::new();
    loop {
        match futures::executor::block_on(select(stream.next(), &mut limit)) {
            Either::Left((Some(Ok(envelope)), _)) => envelopes.push(envelope),
            Either::Left((Some(Err(error)), _)) => panic!("adapter event stream: {error}"),
            Either::Left((None, _)) => return envelopes,
            Either::Right(_) => panic!("timed out waiting for adapter stream closure"),
        }
    }
}

impl Unpin for Deadline {}

pub fn is_control_exchange(kind: &EnvelopeType) -> bool {
    [
        EnvelopeType::CAPABILITIES_REQUEST,
        EnvelopeType::CAPABILITIES_RESPONSE,
        EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
        EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE,
        EnvelopeType::RUN_CANCEL_REQUEST,
        EnvelopeType::RUN_CANCEL_RESPONSE,
        EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST,
        EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE,
        EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
        EnvelopeType::ACTION_CALL_RESOLVE_RESPONSE,
        EnvelopeType::USER_INPUT_RESOLVE_REQUEST,
        EnvelopeType::USER_INPUT_RESOLVE_RESPONSE,
        EnvelopeType::USER_INPUT_CANCEL_REQUEST,
        EnvelopeType::USER_INPUT_CANCEL_RESPONSE,
        EnvelopeType::SESSION_STATE_REQUEST,
        EnvelopeType::SESSION_STATE_RESPONSE,
    ]
    .contains(kind)
}

pub fn is_terminal(kind: &EnvelopeType) -> bool {
    [
        EnvelopeType::RUN_COMPLETED,
        EnvelopeType::RUN_FAILED,
        EnvelopeType::RUN_CANCELLED,
    ]
    .contains(kind)
}

pub fn assert_run_events(admission: &MessageSubmitResponse, revision: &str, events: &[Envelope]) {
    let run = admission.run_id.as_ref();
    assert!(
        admission.accepted && run.is_some_and(|run| !run.as_str().is_empty()),
        "invalid admission: {admission:?}"
    );
    let last = events
        .iter()
        .rposition(|event| !is_control_exchange(&event.envelope_type));
    let (mut terminals, mut emitted) = (0, 0);
    for (index, event) in events.iter().enumerate() {
        if is_control_exchange(&event.envelope_type) {
            continue;
        }
        emitted += 1;
        assert!(
            event.session_id.as_ref() == Some(&admission.session_id)
                && event.run_id.as_ref() == run,
            "event {index} scope: {event:?}"
        );
        assert_eq!(event.sequence, Some(emitted), "event {index} sequence");
        if !revision.is_empty() {
            assert_eq!(
                event.capability_revision.as_deref(),
                Some(revision),
                "event {index} capability revision"
            );
        }
        if is_terminal(&event.envelope_type) {
            terminals += 1;
            assert_eq!(
                Some(index),
                last,
                "terminal event at index {index} of {}",
                events.len()
            );
        }
    }
    assert_eq!(terminals, 1, "got {terminals} terminal events");
}

#[derive(Clone, Debug, Default)]
pub struct RunTrace {
    pub admission: MessageSubmitResponse,
    pub descriptor: Descriptor,
    pub events: Vec<Envelope>,
    pub submitted: Option<MessageSubmitRequest>,
    pub opened: Option<OpenRequest>,
    pub catalog: Option<(ToolsListRequest, ToolCatalog)>,
    pub cancelled: bool,
}

fn envelope(kind: EnvelopeType, id: &str, payload: &impl serde::Serialize) -> Envelope {
    Envelope::new(kind, id, payload).expect("adaptertest payload encodes")
}

fn revision_of(text: &str) -> Option<String> {
    (!text.is_empty()).then(|| text.to_owned())
}

impl RunTrace {
    pub fn new(
        admission: MessageSubmitResponse,
        descriptor: Descriptor,
        events: Vec<Envelope>,
    ) -> Self {
        Self {
            admission,
            descriptor,
            events,
            ..Self::default()
        }
    }

    fn submit_controls(&self) -> Vec<&'static str> {
        [
            FEATURE_INSTRUCTIONS,
            FEATURE_MODEL_SELECTION,
            FEATURE_STRUCTURED_OUTPUT,
            FEATURE_TOOL_SELECTION,
        ]
        .into_iter()
        .filter(|key| {
            self.descriptor
                .capabilities
                .effective_support(key)
                .is_some_and(|support| support.level != SupportLevel::UNAVAILABLE)
        })
        .collect()
    }

    fn opening(&self, open: &OpenRequest) -> [Envelope; 4] {
        let admission = &self.admission;
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
                endpoint: self.descriptor.capabilities.endpoint.clone(),
            },
        );
        initialized.in_reply_to = Some(initialize.id.clone());
        let session = if open.session_id.as_str().is_empty() {
            admission.session_id.clone()
        } else {
            open.session_id.clone()
        };
        let mut request = envelope(
            EnvelopeType::SESSION_OPEN_REQUEST,
            "open-request",
            &SessionOpenRequest {
                session_id: Some(session),
                tool_sources: open.tool_sources.clone(),
                tools: open.tools.clone(),
                allow_degraded_features: open.allow_degraded_features.clone(),
                reopen: open.reopen,
                reasoning_level: open.reasoning_level.clone(),
                compaction_policy: open.compaction_policy.clone(),
                ..SessionOpenRequest::default()
            },
        );
        let mut response = envelope(
            EnvelopeType::SESSION_OPEN_RESPONSE,
            "open-response",
            &SessionState {
                session_id: admission.session_id.clone(),
                status: SessionStatus::IDLE,
                recovery: open.reopen.then(|| RecoveryMetadata {
                    recovered: true,
                    ..RecoveryMetadata::default()
                }),
                ..SessionState::default()
            },
        );
        response.session_id = Some(admission.session_id.clone());
        response.in_reply_to = Some(request.id.clone());
        request.capability_revision = revision_of(&self.descriptor.capability_revision);
        response.capability_revision = request.capability_revision.clone();
        [initialize, initialized, request, response]
    }

    pub fn encode(&self) -> Result<Vec<u8>, String> {
        let offered = self.submit_controls();
        if self.submitted.is_none() && !offered.is_empty() {
            return Err(format!("adaptertest: the descriptor offers submit controls {offered:?}, so a synthesized submit could drop the one the run was admitted with; pass the submit request the adapter received"));
        }
        let admission = &self.admission;
        let revision = revision_of(&self.descriptor.capability_revision);
        let mut trace = Vec::new();
        let mut opening = Vec::new();
        if let Some(open) = &self.opened {
            let [initialize, initialized, request, response] = self.opening(open);
            trace.extend([initialize, initialized]);
            opening.extend([request, response]);
        }
        if revision.is_some() {
            let asked = envelope(
                EnvelopeType::CAPABILITIES_REQUEST,
                "capabilities-request",
                &CapabilitiesRequest {},
            );
            let mut described = envelope(
                EnvelopeType::CAPABILITIES_RESPONSE,
                "capabilities-response",
                &self.descriptor.capabilities,
            );
            described.in_reply_to = Some(asked.id.clone());
            described.capability_revision = revision.clone();
            trace.extend([asked, described]);
        }
        trace.extend(opening);
        if let Some((request, catalog)) = &self.catalog {
            let mut listed = envelope(
                EnvelopeType::ACTION_TOOLS_LIST_REQUEST,
                "tools-request",
                request,
            );
            listed.session_id = Some(admission.session_id.clone());
            listed.capability_revision = revision_of(&catalog.revision);
            let mut tools = envelope(
                EnvelopeType::ACTION_TOOLS_LIST_RESPONSE,
                "tools-response",
                &catalog.tools,
            );
            tools.session_id = Some(admission.session_id.clone());
            tools.in_reply_to = Some(listed.id.clone());
            tools.capability_revision = listed.capability_revision.clone();
            trace.extend([listed, tools]);
        }
        let request = self
            .submitted
            .clone()
            .unwrap_or_else(|| MessageSubmitRequest {
                session_id: admission.session_id.clone(),
                delivery: admission.requested_delivery.clone(),
                messages: vec![Message {
                    role: MessageRole::USER,
                    content: MessageContent::from_text("adaptertest"),
                    ..Message::default()
                }],
                ..MessageSubmitRequest::default()
            });
        let mut submit = envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_REQUEST,
            "submit-request",
            &request,
        );
        submit.session_id = Some(admission.session_id.clone());
        let mut response = envelope(
            EnvelopeType::SESSION_MESSAGE_SUBMIT_RESPONSE,
            "submit-response",
            admission,
        );
        response.session_id = Some(admission.session_id.clone());
        response.in_reply_to = Some(submit.id.clone());
        submit.capability_revision = revision.clone();
        response.capability_revision = revision;
        trace.extend([submit, response]);
        let cut = if self.cancelled {
            cancel_cut(&self.events)
        } else {
            self.events.len()
        };
        trace.extend_from_slice(&self.events[..cut]);
        if self.cancelled {
            trace.extend(self.cancel_exchange());
        }
        trace.extend_from_slice(&self.events[cut..]);
        serde_json::to_vec(&trace).map_err(|error| error.to_string())
    }

    fn cancel_exchange(&self) -> [Envelope; 2] {
        let session = self.admission.session_id.clone();
        let run = self.admission.run_id.clone().unwrap_or_default();
        let mut request = envelope(
            EnvelopeType::RUN_CANCEL_REQUEST,
            "cancel-request",
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
            "cancel-response",
            &RunCancelResponse {
                session_id: session.clone(),
                run_id: run.clone(),
                accepted: true,
                status: RunStatus::CANCELLING,
            },
        );
        ack.session_id = Some(session);
        ack.run_id = Some(run);
        ack.in_reply_to = Some(request.id.clone());
        [request, ack]
    }

    pub fn assert_valid(&self) {
        assert_run_events(
            &self.admission,
            &self.descriptor.capability_revision,
            &self.events,
        );
        let trace = self.encode().unwrap_or_else(|error| panic!("{error}"));
        let result = Validator::new()
            .expect("validator loads")
            .validate(&trace, "adaptertest");
        assert!(
            result.valid(),
            "adapter trace failed OAP validation: {:?}\ntrace: {}",
            result.diagnostics,
            String::from_utf8_lossy(&trace)
        );
    }

    pub fn assert_invalid(&self, code: &str) {
        assert_run_events(
            &self.admission,
            &self.descriptor.capability_revision,
            &self.events,
        );
        let trace = self.encode().unwrap_or_else(|error| panic!("{error}"));
        let result = Validator::new()
            .expect("validator loads")
            .validate(&trace, "adaptertest");
        assert!(
            !result.valid(),
            "adapter trace was expected to fail OAP validation with {code}, but it passed\ntrace: {}",
            String::from_utf8_lossy(&trace)
        );
        assert!(
            result
                .diagnostics
                .iter()
                .any(|diagnostic| diagnostic.code == code),
            "adapter trace failed OAP validation, but not with {code}: {:?}",
            result.diagnostics
        );
    }
}

pub fn cancel_cut(events: &[Envelope]) -> usize {
    events
        .iter()
        .position(|event| {
            event.envelope_type == EnvelopeType::RUN_STATUS_UPDATED
                && event
                    .decode_payload::<RunStatusUpdatedPayload>()
                    .is_ok_and(|payload| payload.status == RunStatus::CANCELLING)
        })
        .or_else(|| {
            events
                .iter()
                .rposition(|event| is_terminal(&event.envelope_type))
        })
        .unwrap_or(events.len())
}

pub fn assert_types(events: &[Envelope], want: &[EnvelopeType]) {
    let got: Vec<&EnvelopeType> = events.iter().map(|event| &event.envelope_type).collect();
    assert_eq!(got, want.iter().collect::<Vec<_>>(), "event types");
}
