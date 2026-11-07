use std::collections::{BTreeMap, BTreeSet};
use std::sync::{Arc, Mutex};

use oap_adapter::gojson::replace_lone_surrogates;
use oap_protocol::{ActionCallPayload, EnvelopeType, ProtocolError, ToolCallId};
use serde_json::value::RawValue;

use crate::native::{decode, ToolCall, ToolCallUpdate};

type Event = (EnvelopeType, ActionCallPayload);
type Fault = (&'static str, &'static str);

#[derive(Default)]
pub(crate) struct Tools {
    calls: Vec<Tool>,
    pub(crate) seen: Arc<Mutex<BTreeSet<String>>>,
}

struct Tool {
    native: ToolCall,
    id: ToolCallId,
    started: bool,
    terminal: bool,
    status: String,
}

impl Tools {
    pub(crate) fn payload(&self, native_id: &str) -> Option<ActionCallPayload> {
        self.calls
            .iter()
            .find(|tool| tool.native.tool_call_id == native_id)
            .map(|tool| tool.event(EnvelopeType::ACTION_CALL_STARTED).1)
    }

    pub(crate) fn observe(
        &mut self,
        raw: &[u8],
        patch: bool,
        ids: &mut dyn FnMut(&str) -> String,
    ) -> (Vec<Event>, Option<Fault>) {
        let mut events = Vec::new();
        let fault = self.apply(raw, patch, ids, &mut events).err();
        (events, fault)
    }

    fn apply(
        &mut self,
        raw: &[u8],
        patch: bool,
        ids: &mut dyn FnMut(&str) -> String,
        events: &mut Vec<Event>,
    ) -> Result<(), Fault> {
        let (index, status) = if patch {
            let update = decode::<ToolCallUpdate>(raw)
                .map_err(|_| ("acp_invalid_tool_update", "malformed tool update"))?;
            let index = self
                .calls
                .iter()
                .position(|t| t.native.tool_call_id == update.tool_call_id)
                .ok_or(("acp_tool_patch_without_call", "tool patch before creation"))?;
            let tool = &mut self.calls[index];
            if tool.terminal {
                return Err(("acp_tool_after_terminal", "tool updated after terminal"));
            }
            if let Some(title) = update.title.filter(|title| !title.is_empty()) {
                tool.native.title = title;
            }
            if !name(&update.name).is_empty() {
                tool.native.name = update.name;
            }
            for (target, value) in [
                (&mut tool.native.raw_input, update.raw_input),
                (&mut tool.native.raw_output, update.raw_output),
                (&mut tool.native.content, update.content),
                (&mut tool.native.locations, update.locations),
            ] {
                if value.is_some() {
                    *target = value;
                }
            }
            (index, update.status.unwrap_or_default())
        } else {
            let call = decode::<ToolCall>(raw)
                .map_err(|_| ("acp_invalid_tool_call", "malformed tool call"))?;
            if call.tool_call_id.is_empty() || call.title.is_empty() {
                return Err(("acp_invalid_tool_call", "tool id and title are required"));
            }
            let index = self
                .calls
                .iter()
                .position(|t| t.native.tool_call_id == call.tool_call_id);
            let first = index.is_none();
            let index = match index {
                Some(index) => index,
                None => {
                    if !self
                        .seen
                        .lock()
                        .unwrap_or_else(|p| p.into_inner())
                        .insert(call.tool_call_id.clone())
                    {
                        return Err(("acp_tool_id_reuse", "tool id reused across prompts"));
                    }
                    self.calls.push(Tool {
                        native: ToolCall::default(),
                        id: ids("tool-call").into(),
                        started: false,
                        terminal: false,
                        status: String::new(),
                    });
                    self.calls.len() - 1
                }
            };
            let tool = &mut self.calls[index];
            if tool.terminal {
                return Err(("acp_tool_after_terminal", "tool updated after terminal"));
            }
            tool.native = call;
            if first {
                events.push(tool.event(EnvelopeType::ACTION_CALL_REQUESTED));
            }
            (index, tool.native.status.clone())
        };
        let tool = &mut self.calls[index];
        match status.as_str() {
            "" if patch && tool.started => {
                let (kind, mut payload) = tool.event(EnvelopeType::ACTION_CALL_PROGRESS);
                payload.arguments_json = None;
                payload.result = None;
                events.push((kind, payload));
            }
            "" | "pending" => (),
            "in_progress" | "completed" | "failed" => {
                tool.status = status.clone();
                if !tool.started {
                    tool.started = true;
                    events.push(tool.event(EnvelopeType::ACTION_CALL_STARTED));
                } else if status == "in_progress" {
                    events.push(tool.event(EnvelopeType::ACTION_CALL_PROGRESS));
                }
                let terminal = match status.as_str() {
                    "completed" => Some(EnvelopeType::ACTION_CALL_COMPLETED),
                    "failed" => Some(EnvelopeType::ACTION_CALL_FAILED),
                    _ => None,
                };
                if let Some(kind) = terminal {
                    tool.terminal = true;
                    events.push(tool.event(kind));
                }
            }
            _ => return Err(("acp_invalid_tool_status", "unknown tool status")),
        }
        Ok(())
    }

    pub(crate) fn settle(&mut self, cancel: bool) -> Vec<Event> {
        let mut events = Vec::new();
        for tool in self.calls.iter_mut().filter(|tool| !tool.terminal) {
            if !cancel && !tool.started {
                tool.started = true;
                events.push(tool.event(EnvelopeType::ACTION_CALL_STARTED));
            }
            tool.terminal = true;
            let kind = if cancel {
                EnvelopeType::ACTION_CALL_CANCELLED
            } else {
                EnvelopeType::ACTION_CALL_FAILED
            };
            let (kind, mut payload) = tool.event(kind);
            payload.progress = None;
            payload.error = if cancel {
                None
            } else {
                Some(ProtocolError {
                    code: "incomplete_tool".into(),
                    message: "prompt completed with unfinished ACP tool".into(),
                    ..ProtocolError::default()
                })
            };
            events.push((kind, payload));
        }
        events
    }
}

impl Tool {
    fn event(&self, kind: EnvelopeType) -> Event {
        let name = name(&self.native.name);
        let mut payload = ActionCallPayload {
            tool_call_id: self.id.clone(),
            requested_by: Some("acp.v1".into()),
            execution_owner: "acp-agent".into(),
            name: if name.is_empty() {
                self.native.title.clone()
            } else {
                name
            },
            ..ActionCallPayload::default()
        };
        match kind.as_str() {
            "action.call.requested" => {
                payload.arguments_json = Some(self.native.raw_input.clone().unwrap_or_else(null))
            }
            "action.call.started" => payload.arguments_json = self.native.raw_input.clone(),
            "action.call.progress" => {
                payload.arguments_json = self.native.raw_input.clone();
                payload.result = self.native.raw_output.clone();
                if self.status == "in_progress" {
                    payload.progress = Some(
                        serde_json::value::to_raw_value(&BTreeMap::from([
                            ("content", &self.native.content),
                            ("locations", &self.native.locations),
                        ]))
                        .expect("native fields contain valid JSON"),
                    );
                }
            }
            "action.call.completed" => {
                payload.result = Some(self.native.raw_output.clone().unwrap_or_else(null))
            }
            "action.call.failed" => {
                payload.error = Some(ProtocolError {
                    code: "tool_failed".into(),
                    message: "ACP tool call failed".into(),
                    ..ProtocolError::default()
                })
            }
            _ => (),
        }
        (kind, payload)
    }
}

fn name(raw: &Option<Box<RawValue>>) -> String {
    raw.as_deref()
        .and_then(|raw| serde_json::from_str(&replace_lone_surrogates(raw.get())).ok())
        .unwrap_or_default()
}

fn null() -> Box<RawValue> {
    RawValue::from_string("null".into()).expect("null is valid JSON")
}
