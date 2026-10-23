use std::future::poll_fn;

use oap_protocol::{
    ActionCallPayload, ContentPart, Message, MessageSubmitRequest, ProtocolError,
    RunCancelledPayload, RunCompletedPayload, RunFailedPayload, RunId, Usage,
};

use crate::{Error, Session, SubmitRequest};

#[derive(Clone, Debug, Default)]
pub struct RunOutcome {
    pub run_id: Option<RunId>,
    pub response: Option<Message>,
    pub text: String,
    pub usage: Option<Usage>,
    pub tool_calls: usize,
}

#[derive(Debug, thiserror::Error)]
#[error("{kind}")]
pub struct RunError {
    pub outcome: Box<RunOutcome>,
    #[source]
    pub kind: RunErrorKind,
}

#[derive(Debug, thiserror::Error)]
pub enum RunErrorKind {
    #[error("adapter: run failed: {}: {}", .0.code, .0.message)]
    Failed(Box<ProtocolError>),
    #[error("adapter: the run was cancelled")]
    Cancelled,
    #[error("adapter: the stream ended without a terminal run event")]
    NoTerminal,
    #[error("adapter: a gate was not answered")]
    GateUnanswered,
    #[error(transparent)]
    Adapter(#[from] Error),
    #[error(transparent)]
    Decode(#[from] oap_protocol::Error),
    #[error("adapter: message content is neither text nor parts: {0}")]
    Content(#[source] serde_json::Error),
}

pub fn message_text(message: &Message) -> Result<String, serde_json::Error> {
    if let Some(text) = message.content.text() {
        return Ok(text);
    }
    let parts: Option<Vec<ContentPart>> = serde_json::from_str(message.content.raw().get())?;
    Ok(parts
        .unwrap_or_default()
        .into_iter()
        .map(|p| p.text)
        .collect())
}

pub async fn run_to_terminal(
    session: &dyn Session,
    request: MessageSubmitRequest,
) -> Result<RunOutcome, RunError> {
    let mut outcome = RunOutcome::default();
    match collect(session, request, &mut outcome).await {
        Ok(()) => Ok(outcome),
        Err(kind) => Err(RunError {
            outcome: Box::new(outcome),
            kind,
        }),
    }
}

async fn collect(
    session: &dyn Session,
    request: MessageSubmitRequest,
    outcome: &mut RunOutcome,
) -> Result<(), RunErrorKind> {
    let (admission, mut stream) = session
        .submit(SubmitRequest {
            request,
            ..SubmitRequest::default()
        })
        .await?;
    outcome.run_id = admission.run_id;
    let mut last_sequence = 0;
    while let Some(item) = poll_fn(|cx| stream.as_mut().poll_next(cx)).await {
        let event = item?;
        if event.run_id.is_some() {
            outcome.run_id = event.run_id.clone();
        }
        if let Some(sequence) = event.sequence {
            if sequence <= last_sequence {
                continue;
            }
            last_sequence = sequence;
        }
        match event.envelope_type.as_str() {
            "action.call.requested" => {
                let call: ActionCallPayload = event.decode_payload()?;
                if call.interaction_id.is_some() {
                    return Err(RunErrorKind::GateUnanswered);
                }
            }
            "user.input.requested" | "action.permission.requested" => {
                return Err(RunErrorKind::GateUnanswered);
            }
            "action.call.started" => outcome.tool_calls += 1,
            "run.completed" => {
                let completed: RunCompletedPayload = event.decode_payload()?;
                let text =
                    message_text(&completed.final_response).map_err(RunErrorKind::Content)?;
                outcome.run_id = Some(completed.run_id);
                outcome.text = text;
                outcome.response = Some(completed.final_response);
                outcome.usage = completed.usage;
                return Ok(());
            }
            "run.failed" => {
                let failed: RunFailedPayload = event.decode_payload()?;
                outcome.run_id = Some(failed.run_id);
                outcome.usage = failed.usage;
                return Err(RunErrorKind::Failed(Box::new(failed.error)));
            }
            "run.cancelled" => {
                let cancelled: RunCancelledPayload = event.decode_payload()?;
                outcome.usage = cancelled.usage;
                return Err(RunErrorKind::Cancelled);
            }
            _ => {}
        }
    }
    Err(RunErrorKind::NoTerminal)
}
