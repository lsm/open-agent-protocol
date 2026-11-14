use std::collections::BTreeMap;

use oxap_protocol::{ActionCallPayload, EnvelopeType, ProtocolError, RunId, ToolCallId};
use serde::Serialize;
use serde_json::value::{to_raw_value, RawValue};

use crate::adapter::ENDPOINT_ID;
use crate::native::{FileChange, Item, METHOD_ITEM_STARTED};
use crate::session::{Binding, Inner, Shared};

fn name(item: &Item) -> String {
    match item.kind.as_str() {
        "commandExecution" => "codex.command".into(),
        "fileChange" => "codex.file_change".into(),
        "mcpToolCall" if item.server.is_empty() && item.tool.is_empty() => "mcp.tool".into(),
        "mcpToolCall" => format!("mcp.{}.{}", item.server, item.tool),
        _ => String::new(),
    }
}

#[derive(Serialize)]
struct Changes<'a> {
    changes: &'a Option<Vec<FileChange>>,
}

fn raw(value: &impl Serialize) -> Box<RawValue> {
    to_raw_value(value).unwrap_or_else(|_| RawValue::NULL.to_owned())
}

fn arguments(item: &Item) -> Box<RawValue> {
    if let Some(arguments) = &item.arguments {
        return arguments.clone();
    }
    match item.kind.as_str() {
        "commandExecution" => raw(&BTreeMap::from([
            ("command", &item.command),
            ("cwd", &item.cwd),
        ])),
        "fileChange" => raw(&Changes {
            changes: &item.changes,
        }),
        _ => raw(&BTreeMap::from([
            ("server", &item.server),
            ("tool", &item.tool),
        ])),
    }
}

fn result(item: &Item) -> Box<RawValue> {
    let output = raw(&item.output.clone().unwrap_or_default());
    if item.kind == "commandExecution" {
        let mut result = BTreeMap::from([
            ("command", raw(&item.command)),
            ("cwd", raw(&item.cwd)),
            ("exitCode", raw(&item.exit_code)),
            ("output", output),
        ]);
        if let Some(native) = &item.result {
            result.insert("nativeResult", native.clone());
        }
        return raw(&result);
    }
    match &item.result {
        Some(native) => native.clone(),
        None => raw(&BTreeMap::from([("output", output)])),
    }
}

fn payload(inner: &Inner, binding: &Binding, with_arguments: bool) -> ActionCallPayload {
    ActionCallPayload {
        session_id: inner.state.session_id.clone(),
        run_id: binding.run.clone(),
        tool_call_id: binding.tool_call.clone(),
        requested_by: Some(ENDPOINT_ID.into()),
        execution_owner: ENDPOINT_ID.into(),
        name: binding.name.clone(),
        arguments_json: with_arguments.then(|| binding.arguments.clone()),
        ..ActionCallPayload::default()
    }
}

impl Shared {
    pub(crate) fn action(&self, inner: &mut Inner, id: &RunId, method: &str, item: Item) {
        let name = name(&item);
        if name.is_empty() || item.id.is_empty() {
            return;
        }
        if method == METHOD_ITEM_STARTED {
            if inner.items.contains_key(&item.id) {
                return;
            }
            let binding = Binding {
                run: id.clone(),
                tool_call: ToolCallId::from(self.runtime.new_id("tool-call")),
                name,
                arguments: arguments(&item),
                terminal: false,
                order: inner.next_order,
            };
            inner.next_order += 1;
            let started = payload(inner, &binding, true);
            inner.items.insert(item.id, binding);
            self.emit_action(inner, id, EnvelopeType::ACTION_CALL_REQUESTED, &started);
            self.emit_action(inner, id, EnvelopeType::ACTION_CALL_STARTED, &started);
            return;
        }
        let Some(binding) = inner
            .items
            .get_mut(&item.id)
            .filter(|binding| &binding.run == id && !binding.terminal)
        else {
            self.fail_run(
                inner,
                id,
                "invalid_native_action",
                "item/completed arrived without one active item/started",
                "",
            );
            return;
        };
        binding.terminal = true;
        let binding = binding.clone();
        let mut ended = payload(inner, &binding, false);
        if item.error.is_some() || item.status == "failed" {
            let message = item
                .error
                .map(|error| error.message)
                .filter(|message| !message.is_empty())
                .unwrap_or_else(|| "native action failed".into());
            ended.error = Some(ProtocolError {
                code: "native_action_failed".into(),
                message,
                ..ProtocolError::default()
            });
            self.emit_action(inner, id, EnvelopeType::ACTION_CALL_FAILED, &ended);
            return;
        }
        ended.result = Some(result(&item));
        self.emit_action(inner, id, EnvelopeType::ACTION_CALL_COMPLETED, &ended);
    }

    pub(crate) fn close_pending(&self, inner: &mut Inner, id: &RunId, interrupted: bool) {
        let mut pending: Vec<Binding> = inner
            .items
            .values_mut()
            .filter(|binding| &binding.run == id && !binding.terminal)
            .map(|binding| {
                binding.terminal = true;
                binding.clone()
            })
            .collect();
        pending.sort_by_key(|binding| binding.order);
        for binding in pending {
            let mut ended = payload(inner, &binding, false);
            if interrupted {
                self.emit_action(inner, id, EnvelopeType::ACTION_CALL_CANCELLED, &ended);
                continue;
            }
            ended.error = Some(ProtocolError {
                code: "native_action_incomplete".into(),
                message: "native turn ended before the action completed".into(),
                ..ProtocolError::default()
            });
            self.emit_action(inner, id, EnvelopeType::ACTION_CALL_FAILED, &ended);
        }
    }
}
