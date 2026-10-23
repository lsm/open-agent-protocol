#[path = "../tests/prompt_support/mod.rs"]
mod prompt_support;

use std::error::Error as StdError;
use std::sync::atomic::Ordering;

use futures::{executor::block_on, FutureExt};
use oap_adapter::memory::{Config, Memory};
use oap_adapter::{run_to_terminal, Adapter, Error, OpenRequest, RunErrorKind};
use oap_protocol::*;
use prompt_support::ScriptSession;
use serde::Deserialize;
use serde_json::{json, Value};

#[derive(Deserialize)]
#[serde(default)]
struct Input {
    admission: MessageSubmitResponse,
    events: Vec<Envelope>,
    hold: bool,
    drop_pending: bool,
    stream_error: bool,
    submit_error: bool,
    message: Option<Message>,
}

impl Default for Input {
    fn default() -> Self {
        Self {
            admission: MessageSubmitResponse {
                run_id: Some("run-1".into()),
                ..MessageSubmitResponse::default()
            },
            events: Vec::new(),
            hold: false,
            drop_pending: false,
            stream_error: false,
            submit_error: false,
            message: None,
        }
    }
}

fn memory() -> Result<Value, Box<dyn StdError>> {
    let memory = Memory::new(Config::default());
    let session = block_on(memory.open(OpenRequest {
        session_id: "session-1".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))?;
    let error = block_on(run_to_terminal(
        session.as_ref(),
        MessageSubmitRequest {
            session_id: "session-1".into(),
            delivery: RequestedDeliveryMode::AUTO,
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("synthetic prompt"),
                ..Message::default()
            }],
            ..MessageSubmitRequest::default()
        },
    ))
    .unwrap_err();
    assert!(matches!(error.kind, RunErrorKind::GateUnanswered));
    let run = error.outcome.run_id.clone().ok_or("missing admitted run")?;
    let cancelled = block_on(session.cancel(run))?;
    assert!(cancelled.accepted);
    block_on(session.close())?;
    Ok(json!({"kind":"gate_unanswered","run_present":true,"cancel_accepted":true,"closed":true}))
}

fn replay(input: Input) -> Result<Value, Box<dyn StdError>> {
    if let Some(message) = input.message {
        return Ok(match oap_adapter::message_text(&message) {
            Ok(text) => json!({"kind":"completed", "text":text}),
            Err(_) => json!({"kind":"content", "text":""}),
        });
    }
    let mut events: Vec<_> = input.events.into_iter().map(Ok).collect();
    if input.stream_error {
        events.push(Err(Error::SessionClosed));
    }
    let mut session = ScriptSession::new(events, input.hold);
    session.admission = input.admission;
    session.submit_error = input.submit_error.then_some(Error::SessionClosed);
    if input.drop_pending {
        assert!(run_to_terminal(&session, MessageSubmitRequest::default())
            .now_or_never()
            .is_none());
        return Ok(
            json!({"kind":"dropped_pending","stream_dropped":session.dropped.load(Ordering::SeqCst)}),
        );
    }
    let (outcome, kind, failure) =
        match block_on(run_to_terminal(&session, MessageSubmitRequest::default())) {
            Ok(outcome) => (outcome, "completed", None),
            Err(error) => {
                let (kind, failure) = match error.kind {
                    RunErrorKind::Failed(failure) => ("failed", Some(*failure)),
                    RunErrorKind::Cancelled => ("cancelled", None),
                    RunErrorKind::NoTerminal => ("no_terminal", None),
                    RunErrorKind::GateUnanswered => ("gate_unanswered", None),
                    RunErrorKind::Adapter(_) => ("adapter", None),
                    RunErrorKind::Decode(_) => ("decode", None),
                    RunErrorKind::Content(_) => ("content", None),
                };
                (*error.outcome, kind, failure)
            }
        };
    Ok(
        json!({"kind":kind,"run_id":outcome.run_id,"text":outcome.text,
        "response":outcome.response,"usage":outcome.usage,"tool_calls":outcome.tool_calls,
        "failure":failure,"stream_dropped":session.dropped.load(Ordering::SeqCst)}),
    )
}

fn main() -> Result<(), Box<dyn StdError>> {
    let path = std::env::args()
        .nth(1)
        .ok_or("expected input JSON path or --memory")?;
    let result = if path == "--memory" {
        memory()?
    } else {
        replay(serde_json::from_slice(&std::fs::read(path)?)?)?
    };
    println!("{}", serde_json::to_string(&result)?);
    Ok(())
}
