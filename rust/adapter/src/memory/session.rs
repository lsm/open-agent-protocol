use std::collections::{HashMap, VecDeque};
use std::sync::{Arc, Mutex};

use oap_protocol::*;
use serde::Serialize;

use super::admit::history_tokens;
use super::controls::{self, Controls};
use super::pipe::Pipe;
use super::{raw, CAPABILITY_REVISION, SCRIPTED_SOURCE, SCRIPTED_TOOL, SCRIPTED_TOOL_OWNER};
use super::{Clock, IdGenerator, KeptSession, ENDPOINT_ID};
use crate::{Error, EventStream, SubmitRequest, UnsupportedControl, CONTROL_UNADVERTISED};

#[derive(Clone, Copy, PartialEq, Eq)]
pub(super) enum Stage {
    Permission,
    Call,
    Input,
}

pub(super) struct Run {
    pub(super) id: String,
    pub(super) status: RunStatus,
    pub(super) started: bool,
    pub(super) queued_admission: bool,
    pub(super) answered: bool,
    pub(super) pending_interaction: String,
    pub(super) stage: Stage,
    pub(super) controls: Controls,
    pub(super) next_sequence: u64,
    pub(super) terminal: bool,
    pub(super) permission_id: String,
    pub(super) input_id: String,
    pub(super) tool_call_id: String,
    pub(super) call_id: String,
    pub(super) provided_tool: ToolDefinition,
    pub(super) acknowledged: bool,
    pub(super) requested_by: String,
    pub(super) responded_by: String,
    pub(super) subscribers: Vec<Pipe>,
    pub(super) input: u64,
}

pub(super) enum Emit {
    TerminalWon,
    Failed(Error),
}

impl From<Error> for Emit {
    fn from(err: Error) -> Self {
        Emit::Failed(err)
    }
}

pub(super) fn surfaced(result: Result<(), Emit>) -> Result<(), Error> {
    match result {
        Ok(()) | Err(Emit::TerminalWon) => Ok(()),
        Err(Emit::Failed(err)) => Err(err),
    }
}

pub(super) struct Inner {
    pub(super) clock: Arc<dyn Clock>,
    pub(super) ids: Arc<dyn IdGenerator>,
    pub(super) kept: Arc<Mutex<HashMap<String, KeptSession>>>,
    pub(super) capacity: usize,
    pub(super) participant: ParticipantId,
    pub(super) state: SessionState,
    pub(super) attached: Vec<ToolSourceAttachment>,
    pub(super) provided: Vec<ToolDefinition>,
    pub(super) closed: bool,
    pub(super) active: Option<String>,
    pub(super) reserved: Option<String>,
    pub(super) runs: HashMap<String, Run>,
    pub(super) journal: VecDeque<Envelope>,
    pub(super) settled: Vec<SettledRun>,
    pub(super) threshold: u64,
    pub(super) history: u64,
}

pub(super) fn golden_input_questions() -> Vec<InputQuestion> {
    vec![InputQuestion {
        id: "choice".into(),
        prompt: "Continue?".into(),
        kind: InputQuestionKind::SINGLE_CHOICE,
        required: true,
        options: vec![InputOption {
            id: "yes".into(),
            label: "Yes".into(),
            ..InputOption::default()
        }],
    }]
}

pub(super) fn golden_arguments() -> Option<Box<serde_json::value::RawValue>> {
    Some(raw(r#"{"operation":"golden"}"#))
}

const TOOL_BOUND: [EnvelopeType; 8] = [
    EnvelopeType::ACTION_CALL_REQUESTED,
    EnvelopeType::ACTION_CALL_STARTED,
    EnvelopeType::ACTION_CALL_PROGRESS,
    EnvelopeType::ACTION_CALL_COMPLETED,
    EnvelopeType::ACTION_CALL_FAILED,
    EnvelopeType::ACTION_CALL_CANCELLED,
    EnvelopeType::ACTION_PERMISSION_REQUESTED,
    EnvelopeType::USER_INPUT_REQUESTED,
];

impl Inner {
    pub(super) fn session_id(&self) -> SessionId {
        self.state.session_id.clone()
    }

    pub(super) fn run(&self, id: &str) -> &Run {
        self.runs
            .get(id)
            .unwrap_or_else(|| unreachable!("run {id} is tracked from admission"))
    }

    pub(super) fn run_mut(&mut self, id: &str) -> &mut Run {
        self.runs
            .get_mut(id)
            .unwrap_or_else(|| unreachable!("run {id} is tracked from admission"))
    }

    pub(super) fn live(&self, id: &Option<String>) -> bool {
        id.as_ref()
            .and_then(|id| self.runs.get(id))
            .is_some_and(|run| !run.terminal)
    }

    pub(super) fn refresh_state(&mut self) {
        let mut entries = Vec::new();
        let mut position = 0;
        let mut started: Option<&Run> = None;
        for id in [&self.active, &self.reserved].into_iter().flatten() {
            let Some(run) = self.runs.get(id) else {
                continue;
            };
            if run.terminal || !run.answered {
                continue;
            }
            let reservation = run.queued_admission && !run.started;
            if !reservation {
                entries.push(entry(run, 0));
                started = Some(run);
                continue;
            }
            position += 1;
            entries.push(entry(run, position));
        }
        let (status, active) = match started {
            Some(run) => {
                let waiting = run.status == RunStatus::WAITING_FOR_INPUT
                    || !run.pending_interaction.is_empty();
                (
                    if waiting {
                        SessionStatus::WAITING_FOR_INPUT
                    } else {
                        SessionStatus::RUNNING
                    },
                    Some(RunId::from(run.id.as_str())),
                )
            }
            None if !entries.is_empty() => (SessionStatus::QUEUED, None),
            None => (SessionStatus::IDLE, None),
        };
        self.state.active_runs = entries;
        if !self.settled.is_empty() {
            self.state.as_of = Some(SessionCapture {
                settled: self.settled.clone(),
                ..SessionCapture::default()
            });
        }
        self.state.status = status;
        self.state.active_run_id = active;
    }

    fn publish(
        &mut self,
        run_id: &str,
        envelope_type: EnvelopeType,
        payload: impl Serialize,
        terminal: bool,
    ) -> Result<Option<String>, Emit> {
        if self.run(run_id).terminal {
            return Err(Emit::TerminalWon);
        }
        let now = self.clock.now_ms();
        let event_id = self.ids.new_id("event");
        let mut envelope = Envelope::new(envelope_type.clone(), event_id.as_str(), &payload)
            .map_err(|_| Emit::Failed(Error::InvalidSubmission))?;
        let session = self.session_id();
        let run = self.run_mut(run_id);
        let sequence = run.next_sequence;
        run.next_sequence += 1;
        envelope.sequence = Some(sequence);
        envelope.timestamp_ms = Some(now);
        envelope.session_id = Some(session);
        envelope.run_id = Some(RunId::from(run_id));
        envelope.capability_revision = Some(CAPABILITY_REVISION.to_owned());
        if TOOL_BOUND.contains(&envelope_type) && run.controls.calls_tool {
            envelope.tool_call_id = Some(ToolCallId::from(run.tool_call_id.as_str()));
        }
        match envelope_type.as_str() {
            "action.permission.requested" => run.pending_interaction = run.permission_id.clone(),
            "user.input.requested" => run.pending_interaction = run.input_id.clone(),
            "action.permission.resolved" | "user.input.resolved" => run.pending_interaction.clear(),
            "action.call.requested" if !run.call_id.is_empty() => {
                run.pending_interaction = run.call_id.clone()
            }
            "action.call.completed" | "action.call.failed" | "action.call.cancelled"
                if !run.call_id.is_empty() && run.pending_interaction == run.call_id =>
            {
                run.pending_interaction.clear()
            }
            _ => {}
        }
        let mut promoted = None;
        if terminal {
            run.terminal = true;
            run.pending_interaction.clear();
            run.status = match envelope_type.as_str() {
                "run.completed" => RunStatus::COMPLETED,
                "run.failed" => RunStatus::FAILED,
                _ => RunStatus::CANCELLED,
            };
        }
        let subscribers = if terminal {
            std::mem::take(&mut run.subscribers)
        } else {
            run.subscribers.clone()
        };
        self.journal.push_back(envelope.clone());
        while self.journal.len() > self.capacity {
            self.journal.pop_front();
        }
        self.state.transcript_cursor = sequence.to_string();
        self.state.updated_at_ms = now;
        if terminal {
            if self.active.as_deref() == Some(run_id) {
                self.active = None;
            }
            if self.reserved.as_deref() == Some(run_id) {
                self.reserved = None;
            }
            if self.active.is_none() && self.live(&self.reserved) {
                promoted = self.reserved.take();
                self.active = promoted.clone();
            }
            self.settled.push(SettledRun {
                run_id: RunId::from(run_id),
                sequence,
            });
        }
        self.refresh_state();
        for subscriber in &subscribers {
            subscriber.send(Ok(envelope.clone()));
            if terminal {
                subscriber.close();
            }
        }
        Ok(promoted)
    }

    pub(super) fn emit(
        &mut self,
        run_id: &str,
        envelope_type: EnvelopeType,
        payload: impl Serialize,
        terminal: bool,
    ) -> Result<(), Emit> {
        if let Some(promoted) = self.publish(run_id, envelope_type, payload, terminal)? {
            self.emit_initial(&promoted)?;
        }
        Ok(())
    }

    fn emit_initial(&mut self, run_id: &str) -> Result<(), Emit> {
        let current = self.state.current_model_id.clone();
        let session = self.session_id();
        let run = self.run_mut(run_id);
        if run.controls.model.is_empty() {
            run.controls.model = current;
        }
        run.started = true;
        run.status = RunStatus::RUNNING;
        let (model, calls_tool, instructions) = (
            run.controls.model.clone(),
            run.controls.calls_tool,
            run.controls.instructions.clone(),
        );
        let input = run.input;
        let started = RunStartedPayload {
            session_id: session.clone(),
            run_id: RunId::from(run_id),
            status: RunStatus::RUNNING,
            model_id: model,
            started_at_ms: self.clock.now_ms(),
        };
        self.emit(run_id, EnvelopeType::RUN_STARTED, started, false)?;
        self.history += input;
        let mut text = if calls_tool {
            "I will use the scripted tool."
        } else {
            "I will answer without the scripted tool."
        }
        .to_owned();
        if !instructions.is_empty() {
            text = format!("{instructions} {text}");
        }
        let delta = ContentDeltaPayload {
            session_id: session.clone(),
            run_id: RunId::from(run_id),
            message_id: Some(MessageId::from(self.ids.new_id("message"))),
            part: ContentPart {
                part_type: ContentPartType::TEXT,
                text,
                ..ContentPart::default()
            },
        };
        self.emit(run_id, EnvelopeType::CONTENT_DELTA, delta, false)?;
        if !calls_tool {
            return self.request_input(run_id);
        }
        let run = self.run(run_id);
        let (requested_by, responded_by, tool_call_id) = (
            run.requested_by.clone(),
            run.responded_by.clone(),
            run.tool_call_id.clone(),
        );
        if !run.call_id.is_empty() {
            let provided = ActionCallPayload {
                interaction_id: Some(InteractionId::from(run.call_id.as_str())),
                session_id: session,
                run_id: RunId::from(run_id),
                tool_call_id: ToolCallId::from(tool_call_id.as_str()),
                name: run.provided_tool.name.clone(),
                arguments_json: golden_arguments(),
                requested_by: Some(ParticipantId::from(requested_by.as_str())),
                responded_by: Some(ParticipantId::from(responded_by.as_str())),
                execution_owner: run.provided_tool.execution_owner.clone(),
                source: run.provided_tool.source.clone(),
                ..ActionCallPayload::default()
            };
            return self.emit(run_id, EnvelopeType::ACTION_CALL_REQUESTED, provided, false);
        }
        let call = ActionCallPayload {
            session_id: session.clone(),
            run_id: RunId::from(run_id),
            tool_call_id: ToolCallId::from(tool_call_id.as_str()),
            name: SCRIPTED_TOOL.to_owned(),
            arguments_json: golden_arguments(),
            requested_by: Some(ParticipantId::from(requested_by.as_str())),
            execution_owner: ParticipantId::from(SCRIPTED_TOOL_OWNER),
            source: SCRIPTED_SOURCE.to_owned(),
            ..ActionCallPayload::default()
        };
        self.emit(run_id, EnvelopeType::ACTION_CALL_REQUESTED, call, false)?;
        let choice = |id: &str, label: &str| PermissionChoice {
            id: id.into(),
            label: label.into(),
            ..PermissionChoice::default()
        };
        let permission = PermissionRequestedPayload {
            interaction_id: InteractionId::from(self.run(run_id).permission_id.as_str()),
            requested_by: ParticipantId::from(requested_by.as_str()),
            responded_by: ParticipantId::from(responded_by.as_str()),
            session_id: session,
            run_id: RunId::from(run_id),
            tool_call_id: Some(ToolCallId::from(tool_call_id.as_str())),
            title: "Allow scripted tool".into(),
            description: "The golden script requires approval.".into(),
            choices: vec![choice("approve", "Approve"), choice("deny", "Deny")],
            arguments_json: golden_arguments(),
        };
        self.emit(
            run_id,
            EnvelopeType::ACTION_PERMISSION_REQUESTED,
            permission,
            false,
        )
    }

    pub(super) fn request_input(&mut self, run_id: &str) -> Result<(), Emit> {
        let run = self.run(run_id);
        let input = UserInputRequestedPayload {
            interaction_id: InteractionId::from(run.input_id.as_str()),
            requested_by: ParticipantId::from(run.requested_by.as_str()),
            responded_by: ParticipantId::from(run.responded_by.as_str()),
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            tool_call_id: run
                .controls
                .calls_tool
                .then(|| ToolCallId::from(run.tool_call_id.as_str())),
            title: "Golden input".into(),
            description: "Choose the deterministic answer.".into(),
            questions: golden_input_questions(),
            ..UserInputRequestedPayload::default()
        };
        let input_id = run.input_id.clone();
        self.emit(run_id, EnvelopeType::USER_INPUT_REQUESTED, input, false)?;
        let now = self.clock.now_ms();
        let status = RunStatusUpdatedPayload {
            session_id: self.session_id(),
            run_id: RunId::from(run_id),
            status: RunStatus::WAITING_FOR_INPUT,
            pending_user_input_id: Some(InteractionId::from(input_id.as_str())),
            updated_at_ms: now,
        };
        if !self.run(run_id).terminal {
            self.run_mut(run_id).status = RunStatus::WAITING_FOR_INPUT;
            self.state.updated_at_ms = now;
        }
        self.emit(run_id, EnvelopeType::RUN_STATUS_UPDATED, status, false)
    }

    pub(super) fn submit(
        &mut self,
        submit: SubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let request = submit.request;
        if request.delivery == RequestedDeliveryMode::STEER {
            let control = UnsupportedControl {
                feature: FEATURE_DELIVERY_STEER.to_owned(),
                reason: CONTROL_UNADVERTISED,
                ..UnsupportedControl::default()
            };
            return Err(Error::UnsupportedControl(Box::new(control)));
        }
        let mut controls = controls::admit(&self.provided, &request)?;
        if request.session_id.as_str().is_empty() || request.messages.is_empty() {
            return Err(Error::InvalidSubmission);
        }
        if !request.delivery.is_empty()
            && request.delivery != RequestedDeliveryMode::AUTO
            && request.delivery != RequestedDeliveryMode::QUEUE
        {
            if request.delivery == RequestedDeliveryMode::BTW {
                let control = UnsupportedControl {
                    feature: FEATURE_DELIVERY_BTW.to_owned(),
                    reason: CONTROL_UNADVERTISED,
                    ..UnsupportedControl::default()
                };
                return Err(Error::UnsupportedControl(Box::new(control)));
            }
            return Err(Error::InvalidSubmission);
        }
        if self.closed {
            return Err(Error::SessionClosed);
        }
        if request.session_id != self.state.session_id {
            return Err(Error::RunNotFound);
        }
        let busy = self.live(&self.active);
        if busy && self.live(&self.reserved) {
            return Err(Error::RunActive);
        }
        if !busy && request.delivery != RequestedDeliveryMode::QUEUE && controls.model.is_empty() {
            controls.model = self.state.current_model_id.clone();
        }
        let id = self.ids.new_id("run");
        let permission_id = self.ids.new_id("permission");
        let input_id = self.ids.new_id("input");
        let tool_call_id = self.ids.new_id("tool-call");
        let mut run = Run {
            id: id.clone(),
            status: RunStatus::RUNNING,
            started: false,
            queued_admission: false,
            answered: false,
            pending_interaction: String::new(),
            stage: Stage::Permission,
            controls,
            next_sequence: 1,
            terminal: false,
            permission_id,
            input_id,
            tool_call_id,
            call_id: String::new(),
            provided_tool: ToolDefinition::default(),
            acknowledged: false,
            requested_by: ENDPOINT_ID.to_owned(),
            responded_by: self.participant.as_str().to_owned(),
            subscribers: Vec::new(),
            input: history_tokens(&request.messages),
        };
        if !run.controls.calls_tool {
            run.stage = Stage::Input;
        } else if !self.provided.is_empty() {
            run.stage = Stage::Call;
            run.call_id = self.ids.new_id("call");
            run.provided_tool = run.controls.elected.clone();
        }
        let pipe = Pipe::default();
        run.subscribers.push(pipe.clone());
        let reservation = busy || request.delivery == RequestedDeliveryMode::QUEUE;
        run.queued_admission = reservation;
        if reservation {
            run.status = RunStatus::QUEUED;
        }
        let model = run.controls.model.clone();
        self.runs.insert(id.clone(), run);
        if busy {
            self.reserved = Some(id.clone());
        } else {
            self.active = Some(id.clone());
        }
        self.refresh_state();
        self.state.updated_at_ms = self.clock.now_ms();
        let message_ids: Vec<MessageId> = request
            .messages
            .iter()
            .map(|message| {
                message
                    .id
                    .clone()
                    .unwrap_or_else(|| MessageId::from(self.ids.new_id("message")))
            })
            .collect();
        let requested = if request.delivery.is_empty() {
            RequestedDeliveryMode::AUTO
        } else {
            request.delivery
        };
        let mut admission = MessageSubmitResponse {
            session_id: self.session_id(),
            accepted: true,
            submission_id: SubmissionId::from(self.ids.new_id("submission")),
            requested_delivery: requested,
            effective_delivery: EffectiveDeliveryMode::START,
            delivery_resolution: "session_idle".into(),
            admission: Admission::STARTED,
            run_id: Some(RunId::from(id.as_str())),
            status: RunStatus::RUNNING,
            model_id: model,
            message_ids,
            ..MessageSubmitResponse::default()
        };
        if reservation {
            admission.effective_delivery = EffectiveDeliveryMode::QUEUE;
            admission.admission = Admission::QUEUED;
            admission.status = RunStatus::QUEUED;
            if busy {
                admission.delivery_resolution = "session_busy".into();
            }
        }
        let started = if busy {
            Ok(())
        } else {
            surfaced(self.emit_initial(&id))
        };
        self.run_mut(&id).answered = true;
        self.refresh_state();
        started.map(|()| (admission, pipe.stream()))
    }

    pub(super) fn close(&mut self) -> Result<(), Error> {
        if self.closed {
            return Ok(());
        }
        if self.live(&self.active) || self.live(&self.reserved) {
            return Err(Error::RunActive);
        }
        self.closed = true;
        self.state.status = SessionStatus::CLOSED;
        self.state.active_run_id = None;
        self.state.updated_at_ms = self.clock.now_ms();
        let kept = KeptSession {
            model: self.state.current_model_id.clone(),
            policy: self.state.compaction_policy.clone(),
        };
        self.kept
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
            .insert(self.state.session_id.as_str().to_owned(), kept);
        for run in self.runs.values_mut() {
            for subscriber in run.subscribers.drain(..) {
                subscriber.close();
            }
        }
        Ok(())
    }
}

fn entry(run: &Run, position: usize) -> ActiveRun {
    let reservation = run.queued_admission && !run.started;
    let pending = if run.pending_interaction.is_empty() {
        Vec::new()
    } else {
        vec![InteractionId::from(run.pending_interaction.as_str())]
    };
    let acknowledged = if run.acknowledged
        && !run.pending_interaction.is_empty()
        && run.pending_interaction == run.call_id
    {
        vec![InteractionId::from(run.call_id.as_str())]
    } else {
        Vec::new()
    };
    ActiveRun {
        run_id: RunId::from(run.id.as_str()),
        status: if reservation {
            RunStatus::QUEUED
        } else {
            run.status.clone()
        },
        relationship: RELATIONSHIP_PRIMARY.to_owned(),
        queue_position: (position > 0).then_some(position as i64),
        as_of_sequence: Some(run.next_sequence - 1),
        pending_interactions: pending,
        acknowledged_interactions: acknowledged,
        ..ActiveRun::default()
    }
}
