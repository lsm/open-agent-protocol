use std::collections::{HashMap, VecDeque};
use std::sync::Arc;

use oap_protocol::{
    ContentPartType, Envelope, EnvelopeType, InteractionId, Message, MessageRole, RunId, RunStatus,
    RunStatusUpdatedPayload, SessionId, SessionStatus,
};
use serde_json::Value;

use crate::{Hub, HubError, HubSession};

pub const TURNS_KEPT: usize = 512;
pub const TURN_TEXT_LIMIT: usize = 64 * 1024;
pub const LAST_REPLY_LIMIT: usize = 4096;

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum WorkStatus {
    Queued,
    Running,
    NeedsYou,
    Done,
    Failed,
    Stopped,
}

impl WorkStatus {
    pub fn as_str(self) -> &'static str {
        match self {
            WorkStatus::Queued => "queued",
            WorkStatus::Running => "running",
            WorkStatus::NeedsYou => "needs_you",
            WorkStatus::Done => "done",
            WorkStatus::Failed => "failed",
            WorkStatus::Stopped => "stopped",
        }
    }
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Work {
    pub session_id: SessionId,
    pub adapter: String,
    pub directory: String,
    pub status: WorkStatus,
    pub run_id: Option<RunId>,
    pub title: String,
    pub link: String,
    pub last_reply: String,
    pub updated_at_ms: i64,
    pub pending_interaction: Option<InteractionId>,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Turn {
    pub role: &'static str,
    pub text: String,
    pub run_id: Option<RunId>,
    pub outcome: &'static str,
    pub at_ms: i64,
}

#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Turns {
    pub first_index: u64,
    pub turns: Vec<Turn>,
}

#[derive(Clone, Debug)]
struct Outcome {
    status: WorkStatus,
    run_id: Option<RunId>,
    reply: String,
}

#[derive(Default)]
pub(crate) struct WorkLog {
    title: String,
    turns: VecDeque<Turn>,
    dropped: u64,
    outcome: Option<Outcome>,
    waits: HashMap<RunId, (bool, Option<InteractionId>)>,
}

pub(crate) fn cut_at(text: &str, limit: usize) -> &str {
    if text.len() <= limit {
        return text;
    }
    let mut at = limit;
    while !text.is_char_boundary(at) {
        at -= 1;
    }
    &text[..at]
}

fn user_text(messages: &[Message]) -> String {
    let mut texts = Vec::new();
    for message in messages
        .iter()
        .filter(|message| message.role == MessageRole::USER)
    {
        if let Some(text) = message.content.text() {
            texts.push(text);
            continue;
        }
        for part in message.content.parts().unwrap_or_default() {
            if part.part_type == ContentPartType::TEXT {
                texts.push(part.text);
            }
        }
    }
    let mut joined = String::new();
    for text in texts {
        if !joined.is_empty() {
            joined.push('\n');
        }
        joined.push_str(&text);
    }
    joined
}

fn reply_text(envelope: &Envelope) -> String {
    let Ok(body) = envelope.decode_payload::<Value>() else {
        return String::new();
    };
    match &body["final_response"]["content"] {
        Value::String(text) => text.clone(),
        Value::Array(parts) => parts
            .iter()
            .filter_map(|part| part["text"].as_str())
            .collect(),
        _ => String::new(),
    }
}

impl WorkLog {
    fn record(&mut self, turn: Turn) {
        if self.turns.len() == TURNS_KEPT {
            self.turns.pop_front();
            self.dropped += 1;
        }
        self.turns.push_back(Turn {
            text: cut_at(&turn.text, TURN_TEXT_LIMIT).to_owned(),
            ..turn
        });
    }

    pub(crate) fn submitted(&mut self, messages: &[Message], run: Option<RunId>, at_ms: i64) {
        self.record(Turn {
            role: "user",
            text: user_text(messages),
            run_id: run,
            outcome: "",
            at_ms,
        });
    }

    pub(crate) fn observe(&mut self, envelope: &Envelope, at_ms: i64) {
        let kind = &envelope.envelope_type;
        let (status, outcome) = if *kind == EnvelopeType::RUN_COMPLETED {
            (WorkStatus::Done, "completed")
        } else if *kind == EnvelopeType::RUN_FAILED {
            (WorkStatus::Failed, "failed")
        } else if *kind == EnvelopeType::RUN_CANCELLED {
            (WorkStatus::Stopped, "cancelled")
        } else {
            if *kind == EnvelopeType::RUN_STATUS_UPDATED {
                if let Ok(update) = envelope.decode_payload::<RunStatusUpdatedPayload>() {
                    self.waits.insert(
                        envelope.run_id.clone().unwrap_or_default(),
                        (
                            update.status == RunStatus::WAITING_FOR_INPUT,
                            update.pending_user_input_id,
                        ),
                    );
                }
            }
            return;
        };
        if let Some(run) = &envelope.run_id {
            self.waits.remove(run);
        }
        let reply = if status == WorkStatus::Done {
            reply_text(envelope)
        } else {
            String::new()
        };
        self.outcome = Some(Outcome {
            status,
            run_id: envelope.run_id.clone(),
            reply: reply.clone(),
        });
        self.record(Turn {
            role: "assistant",
            text: reply,
            run_id: envelope.run_id.clone(),
            outcome,
            at_ms,
        });
    }

    pub(crate) fn set_title(&mut self, title: String) {
        self.title = title;
    }

    pub(crate) fn turns(&self, after: Option<u64>, limit: usize) -> Turns {
        let held = self.turns.len() as u64;
        let start = match after {
            Some(u64::MAX) => {
                return Turns {
                    first_index: self.dropped + held,
                    turns: Vec::new(),
                }
            }
            Some(after) => after + 1,
            None => self.dropped,
        };
        let from = start.saturating_sub(self.dropped).min(held) as usize;
        let to = self.turns.len().min(from.saturating_add(limit));
        Turns {
            first_index: self.dropped + from as u64,
            turns: self.turns.range(from..to).cloned().collect(),
        }
    }
}

impl HubSession {
    pub fn set_title(&self, title: &str) {
        self.with_live(|live| live.work.set_title(title.to_owned()));
    }

    pub fn turns(&self, after: Option<u64>, limit: usize) -> Turns {
        self.with_live(|live| live.work.turns(after, limit))
    }

    async fn work_of(&self) -> Option<Work> {
        let state = match self.state().await {
            Ok(state) => state,
            Err(HubError::Adapter(oap_adapter::Error::SessionClosed)) => return None,
            Err(_) => oap_protocol::SessionState {
                session_id: self.id().clone(),
                status: SessionStatus::ERROR,
                ..oap_protocol::SessionState::default()
            },
        };
        if state.status == SessionStatus::CLOSED {
            return None;
        }
        let (title, outcome, waits) = self.with_live(|live| {
            (
                live.work.title.clone(),
                live.work.outcome.clone(),
                live.work.waits.clone(),
            )
        });
        let mut found = Work {
            session_id: self.id().clone(),
            adapter: self.adapter().to_owned(),
            directory: self.record().directory.clone(),
            status: WorkStatus::Done,
            run_id: state.active_run_id.clone(),
            title,
            link: String::new(),
            last_reply: String::new(),
            updated_at_ms: match state.updated_at_ms {
                0 => self.created_ms(),
                at => at,
            },
            pending_interaction: state
                .active_runs
                .iter()
                .rev()
                .find_map(|run| run.pending_interactions.first().cloned()),
        };
        let mut waiting = false;
        if state.status == SessionStatus::RUNNING && found.pending_interaction.is_none() {
            if let Some((true, pending)) = found.run_id.as_ref().and_then(|run| waits.get(run)) {
                waiting = true;
                found.pending_interaction = pending.clone();
            }
        }
        found.status = if state.status == SessionStatus::QUEUED {
            WorkStatus::Queued
        } else if state.status == SessionStatus::WAITING_FOR_INPUT {
            WorkStatus::NeedsYou
        } else if state.status == SessionStatus::RUNNING {
            if waiting || found.pending_interaction.is_some() {
                WorkStatus::NeedsYou
            } else {
                WorkStatus::Running
            }
        } else if state.status == SessionStatus::ERROR {
            WorkStatus::Failed
        } else if let (true, Some(outcome)) = (state.status == SessionStatus::IDLE, outcome) {
            found.run_id = outcome.run_id;
            if outcome.status == WorkStatus::Done {
                found.last_reply = cut_at(&outcome.reply, LAST_REPLY_LIMIT).to_owned();
            }
            outcome.status
        } else {
            WorkStatus::Done
        };
        Some(found)
    }
}

impl Hub {
    fn link_of(&self, entry: &HubSession) -> String {
        let native = &entry.record().native_session_id;
        if native.is_empty() {
            return String::new();
        }
        self.registry()
            .lookup(entry.adapter())
            .and_then(|adapter| {
                adapter
                    .native_linker()
                    .map(|linker| linker.native_link(native))
            })
            .unwrap_or_default()
    }

    async fn work_in(&self, entry: &Arc<HubSession>) -> Option<Work> {
        let mut found = entry.work_of().await?;
        found.link = self.link_of(entry);
        Some(found)
    }

    pub async fn work(&self, id: &SessionId) -> Result<Work, HubError> {
        let entry = self.session(id)?;
        match self.work_in(&entry).await {
            Some(found) => Ok(found),
            None => {
                entry.mark_closed();
                Err(HubError::SessionClosed(id.clone()))
            }
        }
    }

    pub async fn works(&self) -> Vec<Work> {
        let mut listed = Vec::new();
        for entry in self.sessions() {
            match self.work_in(&entry).await {
                Some(found) => listed.push(found),
                None => entry.mark_closed(),
            }
        }
        listed
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn envelope(kind: EnvelopeType, payload: Value) -> Envelope {
        let mut envelope = Envelope::new(kind, "e", &payload).unwrap();
        envelope.run_id = Some("run-1".into());
        envelope
    }

    #[test]
    fn a_reply_is_read_from_plain_or_part_content_and_cut_on_a_char_boundary() {
        let plain = envelope(
            EnvelopeType::RUN_COMPLETED,
            serde_json::json!({"final_response": {"content": "done"}}),
        );
        assert_eq!(reply_text(&plain), "done");
        let parts = envelope(
            EnvelopeType::RUN_COMPLETED,
            serde_json::json!({"final_response": {"content": [{"type":"text","text":"a"},{"type":"text","text":"b"}]}}),
        );
        assert_eq!(reply_text(&parts), "ab");
        assert_eq!(cut_at("aé", 2), "a");
    }

    #[test]
    fn user_text_separates_only_after_something_was_written_as_go_does() {
        let said = |texts: &[&str]| -> Vec<Message> {
            texts
                .iter()
                .map(|text| Message {
                    role: MessageRole::USER,
                    content: oap_protocol::MessageContent::from_text(text),
                    ..Message::default()
                })
                .collect()
        };
        assert_eq!(user_text(&said(&["", "next"])), "next");
        assert_eq!(user_text(&said(&["a", "", "b"])), "a\n\nb");
    }

    #[test]
    fn the_log_keeps_its_last_turns_and_pages_them_by_index() {
        let mut log = WorkLog::default();
        for _ in 0..TURNS_KEPT + 2 {
            log.submitted(&[], None, 1);
        }
        let page = log.turns(None, 3);
        assert_eq!(page.first_index, 2);
        assert_eq!(page.turns.len(), 3);
        assert_eq!(log.turns(Some(5), 1).first_index, 6);
        assert_eq!(
            log.turns(Some(u64::MAX), 1).first_index,
            (TURNS_KEPT + 2) as u64
        );
        assert_eq!(log.turns(Some(5), usize::MAX).turns.len(), TURNS_KEPT - 4);
    }

    #[test]
    fn a_terminal_records_the_outcome_and_a_status_update_records_the_wait() {
        let mut log = WorkLog::default();
        log.observe(
            &envelope(
                EnvelopeType::RUN_STATUS_UPDATED,
                serde_json::json!({"session_id":"s","run_id":"run-1","status":"waiting_for_input","pending_user_input_id":"ask-1"}),
            ),
            1,
        );
        assert_eq!(
            log.waits.get(&RunId::from("run-1")),
            Some(&(true, Some(InteractionId::from("ask-1"))))
        );
        log.observe(
            &envelope(EnvelopeType::RUN_CANCELLED, serde_json::json!({})),
            2,
        );
        let outcome = log.outcome.clone().unwrap();
        assert_eq!(outcome.status, WorkStatus::Stopped);
        assert_eq!(log.turns(None, 10).turns[0].outcome, "cancelled");
        assert!(log.waits.is_empty());
    }
}
