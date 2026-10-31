use oap_adapter::{Error, NativeTurn};
use oap_binding::{Action, Entry};
use oap_protocol::SessionId;
use serde::Serialize;
use serde_json::Value;

use crate::live::block_on;
use crate::work::{cut_at, TURN_TEXT_LIMIT};
use crate::{Hub, HubError, NativeRef, Work, WorkStatus};

pub(crate) const READ_DEFAULT: i64 = 100;
pub(crate) const READ_MAX: i64 = 500;

pub(crate) struct Refusal {
    pub(crate) code: &'static str,
    pub(crate) message: String,
}

fn refused(code: &'static str, message: impl Into<String>) -> Refusal {
    Refusal {
        code,
        message: message.into(),
    }
}

fn history_unreadable() -> Refusal {
    refused("history_failed", "the session history could not be read")
}

#[derive(Serialize)]
struct WorkRef {
    adapter: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    session_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    native_id: String,
}

#[derive(Serialize)]
struct Pending {
    interaction_id: String,
}

#[derive(Serialize)]
struct WorkJson {
    #[serde(rename = "ref")]
    work_ref: WorkRef,
    #[serde(skip_serializing_if = "Option::is_none")]
    held: Option<bool>,
    #[serde(skip_serializing_if = "String::is_empty")]
    status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    state: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    directory: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    link: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    run_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    last_reply: String,
    updated_at_ms: i64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pending: Option<Pending>,
}

fn held_work(found: Work) -> WorkJson {
    WorkJson {
        work_ref: WorkRef {
            adapter: found.adapter,
            session_id: found.session_id.as_str().to_owned(),
            native_id: String::new(),
        },
        held: None,
        status: found.status.as_str().to_owned(),
        state: String::new(),
        directory: found.directory,
        title: found.title,
        link: found.link,
        run_id: found
            .run_id
            .map(|run| run.as_str().to_owned())
            .unwrap_or_default(),
        last_reply: found.last_reply,
        updated_at_ms: found.updated_at_ms,
        pending: found.pending_interaction.map(|pending| Pending {
            interaction_id: pending.as_str().to_owned(),
        }),
    }
}

fn unheld_work(entry: Entry) -> WorkJson {
    WorkJson {
        work_ref: WorkRef {
            adapter: entry.record.adapter,
            session_id: entry.record.session_id,
            native_id: String::new(),
        },
        held: Some(false),
        status: String::new(),
        state: if entry.action == Action::Closed {
            "closed"
        } else {
            "live"
        }
        .to_owned(),
        directory: entry.record.directory,
        title: String::new(),
        link: String::new(),
        run_id: String::new(),
        last_reply: String::new(),
        updated_at_ms: entry.time_ms,
        pending: None,
    }
}

#[derive(Serialize)]
struct TurnJson {
    index: u64,
    role: String,
    text: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    run_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    outcome: String,
    at_ms: i64,
}

fn encoded(value: &impl Serialize) -> Result<Value, Refusal> {
    serde_json::to_value(value).map_err(|err| refused("internal", err.to_string()))
}

fn unheld(hub: &Hub, id: &SessionId) -> Result<Option<Entry>, Refusal> {
    let Some(bindings) = hub.bindings() else {
        return Ok(None);
    };
    let entries = bindings.sessions().map_err(|_| history_unreadable())?;
    Ok(oap_binding::sessions(&entries)
        .into_iter()
        .find(|entry| entry.record.session_id == id.as_str()))
}

fn state_refusal(err: &HubError, id: &SessionId) -> Refusal {
    match err {
        HubError::SessionClosed(_) | HubError::Adapter(Error::SessionClosed) => refused(
            "session_closed",
            format!("the session {:?} is closed", id.as_str()),
        ),
        _ => refused("unknown_session", format!("no session {:?}", id.as_str())),
    }
}

pub(crate) fn status(hub: &Hub, id: &SessionId) -> Result<Value, Refusal> {
    if hub.session(id).is_err() {
        if let Some(entry) = unheld(hub, id)? {
            return encoded(&unheld_work(entry));
        }
    }
    let found = block_on(hub.work(id)).map_err(|err| state_refusal(&err, id))?;
    encoded(&held_work(found))
}

pub(crate) fn stop(hub: &Hub, id: &SessionId) -> Result<Value, Refusal> {
    if hub.session(id).is_err() {
        if let Some(entry) = unheld(hub, id)? {
            return encoded(&unheld_work(entry));
        }
    }
    let current = block_on(hub.work(id)).map_err(|err| state_refusal(&err, id))?;
    let live = matches!(
        current.status,
        WorkStatus::Running | WorkStatus::Queued | WorkStatus::NeedsYou
    );
    let Some(run) = current.run_id.clone().filter(|_| live) else {
        return encoded(&held_work(current));
    };
    let entry = hub.session(id).map_err(|err| state_refusal(&err, id))?;
    block_on(entry.cancel(run)).map_err(|err| {
        let code = match &err {
            HubError::Adapter(Error::RunAlreadyTerminal { .. }) => "run_terminal",
            HubError::Adapter(Error::RunNotFound) => "run_not_found",
            HubError::Adapter(Error::SessionClosed) => "session_closed",
            _ => "internal",
        };
        refused(code, err.to_string())
    })?;
    status(hub, id)
}

pub(crate) fn read(
    hub: &Hub,
    id: &SessionId,
    after: Option<u64>,
    limit: Option<i64>,
) -> Result<Value, Refusal> {
    let bound = match limit {
        None => READ_DEFAULT,
        Some(limit) if (1..=READ_MAX).contains(&limit) => limit,
        Some(_) => {
            return Err(refused(
                "invalid_request",
                format!("work.read: limit must be from 1 to {READ_MAX}"),
            ))
        }
    } as usize;
    let mut held = true;
    let mut native = NativeRef::default();
    match hub.held_native(id) {
        Some(found) => native = found,
        None => {
            if let Some(entry) = unheld(hub, id)? {
                held = false;
                native = NativeRef {
                    adapter: entry.record.adapter,
                    native_id: entry.record.native_session_id,
                    directory: entry.record.directory,
                };
            }
        }
    }
    let wanted = bound + after.map_or(0, |after| after.min(1 << 31) as usize + 1);
    if let Some(Ok(turns)) = block_on(hub.native_transcript(&native, wanted)) {
        if !turns.is_empty() {
            return encoded(&serde_json::json!({ "turns": native_turns(&turns, after, bound) }));
        }
    }
    if !held {
        return encoded(&serde_json::json!({ "turns": [] }));
    }
    let entry = hub.session(id).map_err(|err| state_refusal(&err, id))?;
    let page = entry.turns(after, bound);
    let turns: Vec<TurnJson> = page
        .turns
        .into_iter()
        .zip(page.first_index..)
        .map(|(turn, index)| TurnJson {
            index,
            role: turn.role.to_owned(),
            text: turn.text,
            run_id: turn
                .run_id
                .map(|run| run.as_str().to_owned())
                .unwrap_or_default(),
            outcome: turn.outcome.to_owned(),
            at_ms: turn.at_ms,
        })
        .collect();
    encoded(&serde_json::json!({ "turns": turns }))
}

fn native_turns(turns: &[NativeTurn], after: Option<u64>, bound: usize) -> Vec<TurnJson> {
    let from = match after {
        None => 0,
        Some(u64::MAX) => turns.len(),
        Some(after) => (after.saturating_add(1)).min(turns.len() as u64) as usize,
    };
    let to = turns.len().min(from.saturating_add(bound));
    turns[from..to]
        .iter()
        .zip(from as u64..)
        .map(|(turn, index)| TurnJson {
            index,
            role: turn.role.clone(),
            text: cut_at(&turn.text, TURN_TEXT_LIMIT).to_owned(),
            run_id: String::new(),
            outcome: String::new(),
            at_ms: turn.at_ms,
        })
        .collect()
}

const VERBS: [(&str, &str); 3] = [
    ("work.status", ""),
    ("work.stop", "run.cancel"),
    ("work.read", ""),
];

pub(crate) fn capabilities(hub: &Hub) -> Result<Value, Refusal> {
    let mut names = hub.registry().names();
    names.sort();
    let mut adapters = Vec::new();
    let mut unavailable = Vec::new();
    for name in names {
        let Some(implementation) = hub.registry().lookup(&name) else {
            continue;
        };
        let descriptor = match block_on(hub.probe(&name)) {
            Ok(descriptor) => descriptor,
            Err(err) => {
                unavailable
                    .push(serde_json::json!({ "adapter": name, "message": err.to_string() }));
                continue;
            }
        };
        let withdrawn = |key: &str| {
            descriptor
                .capabilities
                .features
                .get(key)
                .is_none_or(|feature| feature.level == oap_protocol::SupportLevel::UNAVAILABLE)
        };
        let verbs: Vec<&str> = VERBS
            .iter()
            .filter(|(_, rests_on)| rests_on.is_empty() || !withdrawn(rests_on))
            .map(|(verb, _)| *verb)
            .collect();
        let mut reach = serde_json::json!({
            "adapter": name,
            "any_directory": false,
            "verbs": verbs,
            "native": {
                "list": implementation.native_lister().is_some(),
                "read": implementation.native_reader().is_some(),
                "search": implementation.native_searcher().is_some(),
            },
        });
        let directory = hub.registry().working_directory(&name);
        if !directory.is_empty() {
            reach["directory"] = Value::String(directory.to_owned());
        }
        adapters.push(reach);
    }
    let mut answer = serde_json::json!({ "adapters": adapters });
    if !unavailable.is_empty() {
        answer["unavailable"] = Value::Array(unavailable);
    }
    Ok(answer)
}
