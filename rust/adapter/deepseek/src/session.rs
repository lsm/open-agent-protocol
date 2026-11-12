use std::sync::{Arc, Mutex, MutexGuard};

use crossbeam_channel::{select, Receiver, Sender};
use oap_adapter::journal::{Journal, Reservation};
use oap_adapter::{Error, EventStream, InteractionResolution, Recovery, ResumeRequest};
use oap_protocol::{
    ActionCallPayload, Admission as SubmitAdmission, ContentDeltaPayload, EffectiveDeliveryMode,
    Envelope, EnvelopeId, EnvelopeType, Message, MessageId, MessageRole, MessageSubmitRequest,
    MessageSubmitResponse, ProtocolError, RequestedDeliveryMode, RunCancelResponse,
    RunCompletedPayload, RunFailedPayload, RunId, RunStartedPayload, RunStatus, SessionState,
    SessionStatus, SubmissionId, ToolCallId, Usage, SETTLED_BY_INFERRED,
};
use serde_json::value::RawValue;

use crate::adapter::ENDPOINT_ID;
use crate::admission::{Admission, Candidate};
use crate::content::{final_content, stream_parts};
use crate::native::{
    decode_notification, observed_only, AssistantAttempt, AssistantMessageEvent, Event,
    Notification, SessionPromptParams, SessionPromptResult, StepBoundary,
    SubagentFinishedNotification, ToolCall, ToolResult, TurnEnd, TurnStart, METHOD_SESSION_PROMPT,
};
use crate::process::Process;
use crate::prompt::native_prompt;
use crate::rpc::{Inbound, NotificationMessage};
use crate::runtime::Runtime;

pub struct Session {
    shared: Arc<Shared>,
}

struct Shared {
    prompting: Mutex<()>,
    inner: Mutex<Inner>,
    journal: Journal,
    native_id: String,
    runtime: Runtime,
}

struct Inner {
    process: Option<Process>,
    state: SessionState,
    unusable: bool,
    run: Option<Run>,
    last_seq: Option<i64>,
    children: Vec<Child>,
}

struct Child {
    id: String,
    run: RunId,
    terminal: bool,
    failed: bool,
}

struct Run {
    id: Option<RunId>,
    receipt: String,
    started: bool,
    next: u64,
    buffered: Vec<(Notification, Option<i64>)>,
    candidate: Candidate,
    message_id: MessageId,
    turn: i64,
    step: i64,
    turn_ended: bool,
    idle_after_end: bool,
    end_kind: String,
    reply: Option<AssistantMessageEvent>,
    tools: Vec<Tool>,
    stream: Arc<Mutex<Reservation>>,
    start: Sender<Result<RunId, Error>>,
}

type Failure<'a> = (&'a str, &'a str);

#[derive(Default, serde::Deserialize)]
struct EndReason {
    #[serde(default)]
    kind: String,
}

const MISMATCH: &str = "deepseek_session_mismatch";
const EXTERNAL: &str = "deepseek_external_activity";
const GRAMMAR: &str = "deepseek_invalid_grammar";
const LIFECYCLE: &str = "deepseek_tool_lifecycle";
const CHILD: &str = "deepseek_child_lifecycle";

struct Tool {
    native: String,
    id: ToolCallId,
    name: String,
    started: Option<EnvelopeId>,
    terminal: bool,
}

fn native_protocol(detail: &str) -> Error {
    Error::Harness(format!(
        "deepseek adapter: invalid native protocol observation: {detail}"
    ))
}

fn hold<T>(mutex: &Mutex<T>) -> MutexGuard<'_, T> {
    mutex.lock().unwrap_or_else(|poison| poison.into_inner())
}

impl Session {
    pub(crate) fn new(
        process: Process,
        state: SessionState,
        journal_capacity: usize,
        runtime: Runtime,
    ) -> Self {
        let inbound = process.client.inbound();
        let done = process.client.done();
        let shared = Arc::new(Shared {
            native_id: state.session_id.as_str().into(),
            inner: Mutex::new(Inner {
                process: Some(process),
                state,
                unusable: false,
                run: None,
                last_seq: None,
                children: Vec::new(),
            }),
            journal: Journal::new(journal_capacity),
            runtime,
            prompting: Mutex::new(()),
        });
        let reader = shared.clone();
        std::thread::spawn(move || reader.dispatch(&inbound, &done));
        Self { shared }
    }

    pub fn state(&self) -> Result<SessionState, Error> {
        let inner = self.shared.lock();
        if inner.process.is_none() || inner.unusable {
            return Err(Error::SessionClosed);
        }
        Ok(inner.state.clone())
    }

    pub fn submit(
        &self,
        request: MessageSubmitRequest,
    ) -> Result<(MessageSubmitResponse, EventStream), Error> {
        let runtime = &self.shared.runtime;
        let (blocks, message_ids) = native_prompt(&request, &|kind| runtime.new_id(kind))?;
        let prompting = hold(&self.shared.prompting);
        let (client, stream, started) = {
            let mut inner = self.shared.lock();
            let client = match &inner.process {
                Some(process) if !inner.unusable => process.client.clone(),
                _ => return Err(Error::SessionClosed),
            };
            if request.session_id != inner.state.session_id {
                return Err(Error::RunNotFound);
            }
            if inner.run.is_some() || inner.state.status != SessionStatus::IDLE {
                return Err(Error::RunActive);
            }
            let (start, started) = crossbeam_channel::bounded(1);
            let stream = Arc::new(Mutex::new(self.shared.journal.reserve()));
            inner.run = Some(Run {
                id: None,
                receipt: String::new(),
                started: false,
                next: 1,
                buffered: Vec::new(),
                candidate: Candidate::default(),
                message_id: MessageId::from(self.shared.runtime.new_id("message")),
                turn: 0,
                step: 0,
                turn_ended: false,
                idle_after_end: false,
                end_kind: String::new(),
                reply: None,
                tools: Vec::new(),
                stream: stream.clone(),
                start,
            });
            (client, stream, started)
        };
        let receipt = self.prompt(client, blocks).inspect_err(|error| {
            self.shared
                .abort_own(&mut self.shared.lock(), &stream, error.clone());
        })?;
        {
            let mut inner = self.shared.lock();
            if receipt.is_empty() {
                inner.unusable = true;
                let error = native_protocol("prompt response omitted messageId");
                self.shared.abort_own(&mut inner, &stream, error.clone());
                return Err(error);
            }
            if let Some(run) = inner.run.as_mut().filter(|run| run.owns(&stream)) {
                run.receipt.clone_from(&receipt);
                self.shared.admit(&mut inner);
            }
        }
        drop(prompting);
        let run_id = started.recv().unwrap_or(Err(Error::SessionClosed))?;
        let stream = hold(&stream).stream();
        Ok((
            MessageSubmitResponse {
                session_id: request.session_id,
                accepted: true,
                submission_id: SubmissionId::from(receipt),
                requested_delivery: RequestedDeliveryMode::AUTO,
                effective_delivery: EffectiveDeliveryMode::START,
                delivery_resolution: "session_idle".into(),
                admission: SubmitAdmission::STARTED,
                run_id: Some(run_id),
                status: RunStatus::RUNNING,
                model_id: self.shared.lock().state.current_model_id.clone(),
                message_ids,
                ..MessageSubmitResponse::default()
            },
            stream,
        ))
    }

    fn prompt(
        &self,
        client: crate::rpc::RpcClient,
        blocks: Vec<crate::native::ContentBlock>,
    ) -> Result<String, Error> {
        let params = serde_json::value::to_raw_value(&SessionPromptParams {
            session_id: self.shared.native_id.clone(),
            content_blocks: Some(blocks),
        })
        .map_err(Error::harness)?;
        let (report, reported) = crossbeam_channel::bounded(1);
        let (answer, answered) = crossbeam_channel::bounded(1);
        std::thread::spawn(move || {
            let _ = answer.send(client.call_started(METHOD_SESSION_PROMPT, Some(params), report));
        });
        if let Ok(Some(refused)) = reported.recv() {
            return Err(Error::Harness(refused));
        }
        let raw = answered
            .recv()
            .map_err(|_| Error::SessionClosed)?
            .map_err(Error::harness)?;
        if raw.get() == "null" {
            return Ok(String::new());
        }
        serde_json::from_str::<SessionPromptResult>(raw.get())
            .map(|result| result.message_id)
            .map_err(Error::harness)
    }

    pub fn resolve(&self, _: InteractionResolution) -> Result<(), Error> {
        Err(Error::InteractionNotFound)
    }

    pub fn cancel(&self, _: &RunId) -> Result<RunCancelResponse, Error> {
        Err(Error::Harness(
            "deepseek adapter: operation unavailable".into(),
        ))
    }

    pub fn resume(&self, request: ResumeRequest) -> Result<(Recovery, EventStream), Error> {
        let inner = self.shared.lock();
        if inner.process.is_none() {
            return Err(Error::SessionClosed);
        }
        let journal = &self.shared.journal;
        let run = request.run_id.as_str();
        let latest = journal.ended(run).ok_or(Error::RunNotFound)?;
        if request.after_sequence > latest {
            return Err(Error::ReplayCursorFuture);
        }
        let (recovery, replay) =
            journal.resume(inner.state.clone(), run, request.after_sequence, latest);
        if let Some(gap) = recovery.replay_gap {
            return Err(Error::ReplayGap(gap));
        }
        Ok((recovery, replay))
    }

    pub fn close(&self) -> Result<(), Error> {
        let inner = self.shared.lock();
        if inner.process.is_some() && inner.run.is_some() {
            return Err(Error::RunActive);
        }
        self.shut_down(inner)
    }

    fn shut_down(&self, mut inner: MutexGuard<'_, Inner>) -> Result<(), Error> {
        let Some(process) = inner.process.take() else {
            return Ok(());
        };
        inner.state.status = SessionStatus::CLOSED;
        drop(inner);
        let closed = process.close();
        self.shared.journal.close();
        closed.map_err(Error::Harness)
    }

    pub fn native_session_id(&self) -> String {
        self.shared.native_id.clone()
    }
}

impl Drop for Session {
    fn drop(&mut self) {
        let _ = self.shut_down(self.shared.lock());
    }
}

impl Shared {
    fn lock(&self) -> MutexGuard<'_, Inner> {
        hold(&self.inner)
    }

    fn dispatch(&self, inbound: &Receiver<Inbound>, done: &Receiver<()>) {
        loop {
            select! {
                recv(inbound) -> message => match message {
                    Ok(message) => self.reduce(message),
                    Err(_) => break,
                },
                recv(done) -> _ => break,
            }
        }
        while let Ok(message) = inbound.try_recv() {
            self.reduce(message);
        }
        let mut inner = self.lock();
        if let Some(process) = &inner.process {
            let reason = process.client.err().unwrap_or_else(|| "<nil>".into());
            inner.unusable = true;
            self.fail_settled(
                &mut inner,
                ("deepseek_process_exit", &reason),
                SETTLED_BY_INFERRED,
            );
        }
    }

    fn reduce(&self, message: Inbound) {
        match message {
            Inbound::Barrier(ack) => drop(ack),
            Inbound::Request(_) => {
                let mut inner = self.lock();
                inner.unusable = true;
                self.fail(&mut inner, (EXTERNAL, "reverse request"));
            }
            Inbound::Notification(notification) => self.apply_notification(&notification),
        }
    }

    fn apply_notification(&self, notification: &NotificationMessage) {
        let params = notification.params.as_ref().map_or("", |raw| raw.get());
        let decoded = decode_notification(&notification.method, params.as_bytes()).ok();
        let mut inner = self.lock();
        let after = inner.last_seq;
        if let Some(run) = inner.run.as_mut() {
            let Some(decoded) = decoded else { return };
            if !run.started {
                run.buffered.push((decoded.clone(), after));
            }
            return self.apply_native(&mut inner, &decoded);
        }
        if let (false, Some(Notification::SessionStatus(status))) = (inner.unusable, &decoded) {
            if status.session_id == self.native_id {
                return;
            }
        }
        inner.unusable = true;
    }

    fn apply_native(&self, inner: &mut Inner, notification: &Notification) {
        if let Some(failure) = self.foreign(inner, notification) {
            return self.fail(inner, failure);
        }
        let started = inner.run.as_ref().is_some_and(|run| run.started);
        match notification {
            Notification::SessionEvent(event) => {
                inner.last_seq = Some(event.event.seq);
                if started {
                    self.apply_owned(inner, &event.event);
                } else {
                    self.admit(inner);
                }
            }
            Notification::SessionStatus(status) if started && status.status == "idle" => {
                if let Some(run) = inner.run.as_mut().filter(|run| run.turn_ended) {
                    run.idle_after_end = true;
                }
                self.try_settle(inner);
            }
            Notification::SubagentStarted(child) if started => {
                self.child_started(inner, &child.child_session_id, "duplicate child start");
            }
            Notification::SubagentFinished(child) if started => {
                self.child_finished(inner, child, "unmatched child finish");
                self.try_settle(inner);
            }
            _ => {}
        }
    }

    fn child_started(&self, inner: &mut Inner, id: &str, duplicate: &str) {
        let Some(run) = inner.run.as_ref().and_then(|run| run.id.clone()) else {
            return;
        };
        if inner.children.iter().any(|child| child.id == id) {
            return self.fail(inner, (CHILD, duplicate));
        }
        inner.children.push(Child {
            id: id.into(),
            run,
            terminal: false,
            failed: false,
        });
    }

    fn child_finished(
        &self,
        inner: &mut Inner,
        finished: &SubagentFinishedNotification,
        unmatched: &str,
    ) {
        let run = inner.run.as_ref().and_then(|run| run.id.clone());
        let child = inner.children.iter_mut().find(|child| {
            child.id == finished.child_session_id && Some(&child.run) == run.as_ref()
        });
        match child {
            Some(child) if !child.terminal => {
                child.terminal = true;
                child.failed = finished.status != "ok" || finished.stop_reason == "max-tokens";
            }
            _ => self.fail(inner, (CHILD, unmatched)),
        }
    }

    fn replay(&self, inner: &mut Inner) {
        let Some(run) = inner.run.as_mut() else {
            return;
        };
        let candidate = std::mem::take(&mut run.candidate);
        let buffered = std::mem::take(&mut run.buffered);
        run.turn = candidate.turn;
        run.step = candidate.step;
        let mut turn_end = 0;
        for event in &candidate.events {
            if event.kind == "turn/end" {
                turn_end = event.seq;
            }
            let admitted = match event.kind.as_str() {
                "turn/start" | "user/message" => true,
                "step/start" => event
                    .data_as::<StepBoundary>()
                    .is_ok_and(|boundary| boundary.step <= candidate.step),
                _ => false,
            };
            if !admitted {
                self.apply_owned(inner, event);
            }
        }
        for (notification, after) in buffered {
            match notification {
                Notification::SessionStatus(status) => {
                    if let Some(run) = inner.run.as_mut().filter(|run| run.turn_ended) {
                        if after.unwrap_or(0) >= turn_end {
                            run.idle_after_end = status.status == "idle";
                        }
                    }
                }
                Notification::SubagentStarted(child) => {
                    let invalid = "invalid buffered child start";
                    self.child_started(inner, &child.child_session_id, invalid);
                }
                Notification::SubagentFinished(child) => {
                    self.child_finished(inner, &child, "invalid buffered child finish");
                }
                Notification::SessionEvent(_) => {}
            }
        }
        self.try_settle(inner);
    }

    fn apply_owned(&self, inner: &mut Inner, event: &Event) {
        if let Err((code, detail)) = self.owned(inner, event) {
            self.fail(inner, (code, &detail));
        }
    }

    fn owned(&self, inner: &mut Inner, event: &Event) -> Result<(), (&'static str, String)> {
        let grammar = |detail: &str| Err((GRAMMAR, detail.to_owned()));
        let Some(run) = inner.run.as_mut() else {
            return Ok(());
        };
        match event.kind.as_str() {
            "turn/start" => {
                if event.data_as::<TurnStart>().unwrap_or_default().turn != run.turn {
                    return grammar("overlapping foreign turn");
                }
            }
            "step/start" => {
                let boundary: StepBoundary = event.data_as().unwrap_or_default();
                if boundary.turn != run.turn || boundary.step <= run.step {
                    return grammar("invalid step start");
                }
                run.step = boundary.step;
            }
            "step/end" => {
                let boundary: StepBoundary = event.data_as().unwrap_or_default();
                if boundary.turn != run.turn || boundary.step != run.step {
                    return grammar("invalid step end");
                }
            }
            "assistant/attempt" => {
                let attempt: AssistantAttempt = event.data_as().unwrap_or_default();
                if !run.in_step(attempt.turn, attempt.step) {
                    return grammar("event outside open owned step");
                }
            }
            "assistant/message" => {
                let reply: AssistantMessageEvent = event.data_as().unwrap_or_default();
                if !run.in_step(reply.turn, reply.step) {
                    return grammar("event outside open owned step");
                }
                let parts = stream_parts(&reply.stream);
                let message_id = Some(run.message_id.clone());
                let run_id = run.id.clone().unwrap_or_default();
                run.reply = Some(reply);
                for part in parts {
                    let payload = ContentDeltaPayload {
                        session_id: inner.state.session_id.clone(),
                        run_id: run_id.clone(),
                        message_id: message_id.clone(),
                        part,
                    };
                    let _ = self.emit(inner, EnvelopeType::CONTENT_DELTA, &payload, false);
                }
            }
            "tool/call" => {
                let call: ToolCall = event.data_as().unwrap_or_default();
                if !run.in_step(call.turn, call.step) {
                    return grammar("event outside open owned step");
                }
                return self.start_tool(inner, call);
            }
            "tool/result" => {
                let result: ToolResult = event.data_as().unwrap_or_default();
                if !run.in_step(result.turn, result.step) {
                    return grammar("event outside open owned step");
                }
                return self.end_tool(inner, result);
            }
            "turn/end" => {
                let end: TurnEnd = event.data_as().unwrap_or_default();
                if end.turn != run.turn || run.turn_ended {
                    return grammar("invalid turn end");
                }
                let reason = end.reason.as_ref().map_or("", |raw| raw.get());
                run.end_kind = serde_json::from_str::<EndReason>(reason)
                    .unwrap_or_default()
                    .kind;
                run.turn_ended = true;
                self.try_settle(inner);
            }
            "user/message"
            | "agent/inbox/spliced"
            | "todo/write"
            | "request/header"
            | "request/context"
            | "session/end-seed" => {}
            kind if observed_only(kind) || event.ignorable == Some(true) => {}
            kind => {
                return Err((
                    "deepseek_unknown_event",
                    format!(
                        "unknown required event {}",
                        oap_adapter::gojson::go_quote(kind)
                    ),
                ))
            }
        }
        Ok(())
    }

    fn try_settle(&self, inner: &mut Inner) {
        if !inner
            .run
            .as_ref()
            .is_some_and(|run| run.started && run.turn_ended && run.idle_after_end)
        {
            return;
        }
        let run_id = inner.run.as_ref().and_then(|run| run.id.clone());
        let mut children = inner
            .children
            .iter()
            .filter(|child| Some(&child.run) == run_id.as_ref());
        if children.clone().any(|child| !child.terminal) {
            return;
        }
        let failed_child = children.any(|child| child.failed);
        self.settle_open_tools(inner);
        if failed_child {
            return self.fail(inner, ("deepseek_child_failed", "subagent failed"));
        }
        let Some(run) = inner.run.as_ref() else {
            return;
        };
        let kind = run.end_kind.clone();
        if kind != "completed" {
            let code = format!("deepseek_{kind}");
            return self.fail(inner, (&code, &format!("native turn ended: {kind}")));
        }
        let Some(reply) = &run.reply else {
            let detail = "completed turn omitted assistant message";
            return self.fail(inner, ("deepseek_missing_final_message", detail));
        };
        let content = match final_content(&reply.message.content, &|native| {
            let tool = run.tools.iter().find(|tool| tool.native == native)?;
            Some((tool.id.clone(), tool.name.clone()))
        }) {
            Ok(content) => content,
            Err(detail) => return self.fail(inner, ("deepseek_invalid_final_message", &detail)),
        };
        let usage = reply.usage.as_ref().map(|usage| Usage {
            input_tokens: Some(usage.input_tokens as u64),
            output_tokens: Some(usage.output_tokens as u64),
            total_tokens: Some(usage.input_tokens as u64 + usage.output_tokens as u64),
            ..Usage::default()
        });
        let payload = RunCompletedPayload {
            session_id: inner.state.session_id.clone(),
            run_id: run.id.clone().unwrap_or_default(),
            final_response: Message {
                id: Some(run.message_id.clone()),
                role: MessageRole::ASSISTANT,
                content,
                ..Message::default()
            },
            stop_reason: "completed".into(),
            usage,
            ..RunCompletedPayload::default()
        };
        let _ = self.emit(inner, EnvelopeType::RUN_COMPLETED, &payload, true);
    }

    fn foreign(&self, inner: &Inner, notification: &Notification) -> Option<Failure<'static>> {
        let native = &self.native_id;
        let (code, detail) = match notification {
            Notification::SessionEvent(event) if &event.session_id != native => {
                (MISMATCH, "session.event for foreign session")
            }
            Notification::SessionEvent(event)
                if inner.last_seq.is_some_and(|last| event.event.seq <= last) =>
            {
                (
                    "deepseek_invalid_sequence",
                    "non-monotonic native event sequence",
                )
            }
            Notification::SessionStatus(status) if &status.session_id != native => {
                (MISMATCH, "session.status for foreign session")
            }
            Notification::SubagentStarted(child)
                if &child.parent_session_id != native || &child.child_session_id == native =>
            {
                (EXTERNAL, "foreign or recursive subagent")
            }
            Notification::SubagentFinished(child) if &child.parent_session_id != native => {
                (EXTERNAL, "foreign child finish")
            }
            _ => return None,
        };
        Some((code, detail))
    }

    fn admit(&self, inner: &mut Inner) {
        let Some(run) = inner
            .run
            .as_mut()
            .filter(|run| !run.started && !run.receipt.is_empty())
        else {
            return;
        };
        let events = run
            .buffered
            .iter()
            .filter_map(|(notification, _)| match notification {
                Notification::SessionEvent(event) => Some(&event.event),
                _ => None,
            });
        match run.candidate.evaluate(&run.receipt, events) {
            Admission::Pending => {}
            Admission::Refused => {
                self.abort(inner, native_protocol("submission ownership proof failed"));
            }
            Admission::Admitted => {
                let id = RunId::from(self.runtime.new_id("run"));
                run.id = Some(id.clone());
                run.started = true;
                hold(&run.stream).bind(id.as_str());
                inner.state.status = SessionStatus::RUNNING;
                inner.state.active_run_id = Some(id.clone());
                let payload = RunStartedPayload {
                    session_id: inner.state.session_id.clone(),
                    run_id: id.clone(),
                    status: RunStatus::RUNNING,
                    model_id: inner.state.current_model_id.clone(),
                    started_at_ms: self.runtime.now_ms(),
                    ..RunStartedPayload::default()
                };
                let emitted = self.emit(inner, EnvelopeType::RUN_STARTED, &payload, false);
                if let Some(run) = &inner.run {
                    let _ = run.start.send(emitted.map(|()| id));
                }
                self.replay(inner);
            }
        }
    }

    fn emit(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl serde::Serialize,
        terminal: bool,
    ) -> Result<(), Error> {
        self.append(inner, kind, payload, terminal, |_| {})
            .map(drop)
    }

    fn append(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &impl serde::Serialize,
        terminal: bool,
        address: impl FnOnce(&mut Envelope),
    ) -> Result<EnvelopeId, Error> {
        let run = inner.run.as_mut().ok_or(Error::RunNotFound)?;
        let mut envelope =
            Envelope::new(kind, self.runtime.new_id("event"), payload).map_err(Error::harness)?;
        address(&mut envelope);
        let id = envelope.id.clone();
        let now = self.runtime.now_ms();
        envelope.sequence = Some(run.next);
        envelope.timestamp_ms = Some(now);
        envelope.session_id = Some(inner.state.session_id.clone());
        envelope.run_id.clone_from(&run.id);
        envelope.capability_revision = Some(crate::adapter::pin().capability_revision.clone());
        inner.state.transcript_cursor = run.next.to_string();
        inner.state.updated_at_ms = now;
        run.next += 1;
        self.journal.append(envelope, terminal);
        if terminal {
            inner.run = None;
            inner.state.status = SessionStatus::IDLE;
            inner.state.active_run_id = None;
        }
        Ok(id)
    }

    fn start_tool(&self, inner: &mut Inner, call: ToolCall) -> Result<(), (&'static str, String)> {
        let Some(run) = inner.run.as_mut() else {
            return Ok(());
        };
        if run.tools.iter().any(|tool| tool.native == call.call_id) {
            return Err((LIFECYCLE, "duplicate tool call".into()));
        }
        let id = ToolCallId::from(self.runtime.new_id("tool-call"));
        let mut payload = self.tool_payload(inner, &id, &call.name);
        payload.arguments_json = RawValue::from_string(call.arguments).ok();
        let requested = self.emit_reply(inner, EnvelopeType::ACTION_CALL_REQUESTED, &payload, None);
        payload.arguments_json = None;
        let kind = EnvelopeType::ACTION_CALL_STARTED;
        let started = self.emit_reply(inner, kind, &payload, requested);
        if let Some(run) = inner.run.as_mut() {
            run.tools.push(Tool {
                native: call.call_id,
                id,
                name: call.name,
                started,
                terminal: false,
            });
        }
        Ok(())
    }

    fn end_tool(
        &self,
        inner: &mut Inner,
        result: ToolResult,
    ) -> Result<(), (&'static str, String)> {
        let native = &result.message.source.call_id;
        let Some(tool) = inner.run.as_mut().and_then(|run| {
            let tool = run.tools.iter_mut().find(|tool| &tool.native == native)?;
            (!tool.terminal).then_some(tool)
        }) else {
            return Err((LIFECYCLE, "unmatched tool result".into()));
        };
        tool.terminal = true;
        let (id, name, started) = (tool.id.clone(), tool.name.clone(), tool.started.clone());
        let mut payload = self.tool_payload(inner, &id, &name);
        let kind = match result.error {
            Some(error) => {
                let mut failure = ProtocolError {
                    code: error.code.clone(),
                    message: error.name.clone(),
                    ..ProtocolError::default()
                };
                if let Some(reason) = error.reason_text().filter(|reason| !reason.is_empty()) {
                    failure.message = reason;
                    failure.details.insert("name".into(), error.name.into());
                }
                payload.error = Some(failure);
                EnvelopeType::ACTION_CALL_FAILED
            }
            None => {
                let content = serde_json::to_string(&result.message.content).unwrap_or_default();
                payload.result = RawValue::from_string(content).ok();
                EnvelopeType::ACTION_CALL_COMPLETED
            }
        };
        self.emit_reply(inner, kind, &payload, started);
        Ok(())
    }

    fn settle_open_tools(&self, inner: &mut Inner) {
        let open: Vec<(ToolCallId, String, Option<EnvelopeId>)> = inner
            .run
            .iter_mut()
            .flat_map(|run| run.tools.iter_mut())
            .filter(|tool| !tool.terminal)
            .map(|tool| {
                tool.terminal = true;
                (tool.id.clone(), tool.name.clone(), tool.started.clone())
            })
            .collect();
        for (id, name, started) in open {
            let mut payload = self.tool_payload(inner, &id, &name);
            payload.error = Some(ProtocolError {
                code: "incomplete_tool".into(),
                message: "turn settled with an unfinished deepseek tool".into(),
                ..ProtocolError::default()
            });
            self.emit_reply(inner, EnvelopeType::ACTION_CALL_FAILED, &payload, started);
        }
    }

    fn tool_payload(&self, inner: &Inner, id: &ToolCallId, name: &str) -> ActionCallPayload {
        ActionCallPayload {
            session_id: inner.state.session_id.clone(),
            run_id: inner
                .run
                .as_ref()
                .and_then(|run| run.id.clone())
                .unwrap_or_default(),
            tool_call_id: id.clone(),
            requested_by: Some(ENDPOINT_ID.into()),
            execution_owner: "deepseek-harness".into(),
            name: name.into(),
            ..ActionCallPayload::default()
        }
    }

    fn emit_reply(
        &self,
        inner: &mut Inner,
        kind: EnvelopeType,
        payload: &ActionCallPayload,
        reply: Option<EnvelopeId>,
    ) -> Option<EnvelopeId> {
        self.append(inner, kind, payload, false, |envelope| {
            envelope.in_reply_to = reply;
            envelope.tool_call_id = Some(payload.tool_call_id.clone());
        })
        .ok()
    }

    fn fail(&self, inner: &mut Inner, failure: Failure) {
        self.fail_settled(inner, failure, "");
    }

    fn fail_settled(&self, inner: &mut Inner, (code, detail): Failure, settled_by: &str) {
        let Some(run_id) = inner.run.as_ref().and_then(|run| run.id.clone()) else {
            return self.abort(inner, native_protocol(detail));
        };
        self.settle_open_tools(inner);
        let payload = RunFailedPayload {
            session_id: inner.state.session_id.clone(),
            run_id,
            error: ProtocolError {
                code: code.into(),
                message: detail.into(),
                ..ProtocolError::default()
            },
            settled_by: settled_by.into(),
            ..RunFailedPayload::default()
        };
        let _ = self.emit(inner, EnvelopeType::RUN_FAILED, &payload, true);
    }

    fn abort(&self, inner: &mut Inner, error: Error) {
        if let Some(run) = inner.run.take_if(|run| !run.started) {
            run.end(error);
        }
    }

    fn abort_own(&self, inner: &mut Inner, stream: &Arc<Mutex<Reservation>>, error: Error) {
        if let Some(run) = inner.run.take_if(|run| run.owns(stream)) {
            run.end(error);
        }
    }
}

impl Run {
    fn in_step(&self, turn: i64, step: i64) -> bool {
        turn == self.turn && step == self.step && !self.turn_ended
    }

    fn owns(&self, stream: &Arc<Mutex<Reservation>>) -> bool {
        !self.started && Arc::ptr_eq(&self.stream, stream)
    }

    fn end(self, error: Error) {
        hold(&self.stream).release();
        let _ = self.start.send(Err(error));
    }
}

#[cfg(test)]
#[path = "../tests/support/session_journal.rs"]
mod tests;
