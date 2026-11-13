use oap_adapter::gojson::{go_quote, zero};
use serde::de::DeserializeOwned;
use serde::Deserialize;

use super::requests::refused;
use super::*;

#[derive(Clone, Debug, Default, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct MessageStartPayload {
    #[serde(default, deserialize_with = "zero")]
    pub display_kind: String,
    #[serde(default, deserialize_with = "zero")]
    pub display_text: String,
    #[serde(default, deserialize_with = "zero")]
    pub notification_category: String,
    #[serde(default, deserialize_with = "zero")]
    pub text: String,
    #[serde(default, deserialize_with = "zero")]
    pub turn_id: String,
}

pub const OBSERVED_EVENTS: &[&str] = &[
    "agent.print",
    "agent.terminal.output",
    "approval.cancelled",
    "billing.step_up.verification",
    "bot_relay.outbox.pending",
    "browser.controller.cancel",
    "browser.controller.command",
    "browser.progress",
    "connection.request",
    "connection.update",
    "cron.changed",
    "display.install.done",
    "display.install.log",
    "display.lease",
    "display.status",
    "free_tier.challenge",
    "layout.apply",
    "message.reaction",
    "moa.aggregating",
    "moa.phase",
    "moa.progress",
    "moa.reference",
    "notice",
    "notification.clear",
    "notification.show",
    "onboarding.ended",
    "pairing.changed",
    "pane.reveal",
    "pet.changed",
    "pet.generate.progress",
    "pet.hatch.progress",
    "platforms.changed",
    "preview.close",
    "preview.open",
    "preview.restart.complete",
    "preview.restart.progress",
    "projects.changed",
    "reaction",
    "reasoning.available",
    "review.summary",
    "session.control.update",
    "session.reclaimed",
    "session.resume_progress",
    "session.title",
    "sessions.changed",
    "setup.ready",
    "skills.changed",
    "skin.changed",
    "status.update",
    "subagent.progress",
    "subagent.spawn_requested",
    "subagent.start",
    "subagent.thinking",
    "subagent.tool",
    "terminal.close",
    "tip.show",
    "todo.updated",
    "tool.generating",
    "tool.output_risk",
    "tool.progress",
    "voice.interrupted",
    "voice.partial",
    "voice.status",
    "voice.transcript",
    "wake.detected",
];

const TYPED_EVENTS: &[&str] = &[
    EVENT_GATEWAY_READY,
    EVENT_MESSAGE_START,
    EVENT_MESSAGE_DELTA,
    EVENT_REASONING_DELTA,
    EVENT_THINKING_DELTA,
    EVENT_MESSAGE_INTERIM,
    EVENT_MESSAGE_COMPLETE,
    EVENT_ERROR,
    EVENT_TOOL_START,
    EVENT_TOOL_COMPLETE,
    EVENT_SESSION_USAGE,
    EVENT_SESSION_INFO,
    EVENT_REQUEST_CANCEL,
    EVENT_SUBAGENT_COMPLETE,
    EVENT_BTW_COMPLETE,
    EVENT_BACKGROUND_COMPLETE,
];

pub fn is_run_scoped(kind: &str) -> bool {
    matches!(
        kind,
        EVENT_MESSAGE_START
            | EVENT_MESSAGE_DELTA
            | EVENT_REASONING_DELTA
            | EVENT_THINKING_DELTA
            | EVENT_MESSAGE_COMPLETE
            | EVENT_TOOL_START
            | EVENT_TOOL_COMPLETE
    )
}

pub fn decode_notification(method: &str, params: &[u8]) -> Result<Event, String> {
    if method != NOTIFY_EVENT {
        return Err(refused(format!(
            "unknown notification method {}",
            go_quote(method)
        )));
    }
    let event: Event = decode_strict(params)
        .map_err(|error| refused(format!("invalid event envelope: {error}")))?;
    event.validate()?;
    Ok(event)
}

fn hex_epoch(epoch: &str) -> bool {
    epoch.len() == 32
        && epoch
            .bytes()
            .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte))
}

impl Event {
    fn payload<T: DeserializeOwned + Default>(&self) -> Result<T, String> {
        decode_strict(self.payload_bytes())
            .map_err(|error| refused(format!("invalid {} payload: {error}", self.kind)))
    }

    pub fn validate(&self) -> Result<(), String> {
        let kind = self.kind.as_str();
        if kind.is_empty() {
            return Err(refused("event type is required"));
        }
        if !self.session_id.is_empty() {
            if self.seq < 1 {
                return Err(refused(format!(
                    "session event {} requires a positive seq",
                    go_quote(kind)
                )));
            }
        } else if self.seq != 0 {
            return Err(refused(format!(
                "session-less event {} must not carry seq",
                go_quote(kind)
            )));
        }
        if !TYPED_EVENTS.contains(&kind) && !OBSERVED_EVENTS.contains(&kind) {
            return Err(refused(format!("unknown event type {}", go_quote(kind))));
        }
        match kind {
            EVENT_MESSAGE_START if self.payload_bytes().is_empty() => Ok(()),
            EVENT_MESSAGE_START => self.payload::<MessageStartPayload>().map(drop),
            EVENT_SESSION_INFO if self.payload_bytes().is_empty() => Err(refused(
                "invalid session.info payload: unexpected end of JSON input",
            )),
            EVENT_SESSION_INFO => self.payload::<SessionInfoPayload>().map(drop),
            EVENT_GATEWAY_READY => {
                let ready: ReadyPayload = self.payload()?;
                if !ready.change_events || !hex_epoch(&ready.replay_epoch) || ready.skin.is_none() {
                    return Err(refused("invalid gateway.ready payload"));
                }
                if !self.session_id.is_empty() {
                    return Err(refused("gateway.ready must be session-less"));
                }
                Ok(())
            }
            EVENT_MESSAGE_COMPLETE => {
                let complete: MessageCompletePayload = self.payload()?;
                if !matches!(
                    complete.status.as_str(),
                    "interrupted" | "error" | "complete" | ""
                ) {
                    return Err(refused(format!(
                        "message.complete status {}",
                        go_quote(&complete.status)
                    )));
                }
                if complete.status == "error" && complete.error.is_empty() {
                    return Err(refused("error settlement requires an error message"));
                }
                Ok(())
            }
            EVENT_MESSAGE_DELTA | EVENT_REASONING_DELTA | EVENT_THINKING_DELTA => {
                let delta: DeltaPayload = self.payload()?;
                if !delta.rendered.is_empty() && kind != EVENT_MESSAGE_DELTA {
                    return Err(refused("rendered is message.delta-only"));
                }
                if delta.verbose && kind != EVENT_REASONING_DELTA {
                    return Err(refused("verbose is reasoning.delta-only"));
                }
                Ok(())
            }
            EVENT_MESSAGE_INTERIM => self.payload::<InterimPayload>().map(drop),
            EVENT_TOOL_START => self.payload::<ToolStartPayload>().map(drop),
            EVENT_TOOL_COMPLETE => self.payload::<ToolCompletePayload>().map(drop),
            EVENT_SESSION_USAGE => self.payload::<UsageTickPayload>().map(drop),
            EVENT_SUBAGENT_COMPLETE => self.payload::<SubagentPayload>().map(drop),
            EVENT_REQUEST_CANCEL => {
                let cancel: RequestCancelPayload = self.payload()?;
                if cancel.id.is_empty() || cancel.method.is_empty() {
                    return Err(refused("request.cancel requires id and method"));
                }
                Ok(())
            }
            EVENT_ERROR => {
                let failure: ErrorPayload = self.payload()?;
                if failure.message.is_empty() {
                    return Err(refused("error event requires a message"));
                }
                Ok(())
            }
            EVENT_BTW_COMPLETE | EVENT_BACKGROUND_COMPLETE => {
                let task: TaskCompletePayload = self.payload()?;
                if task.task_id.is_empty() {
                    return Err(refused("task completion requires task_id"));
                }
                Ok(())
            }
            _ => Ok(()),
        }
    }
}
