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
    #[serde(skip_serializing_if = "Option::is_none")]
    native: Option<bool>,
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
        native: None,
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
        native: None,
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

const VERBS: [(&str, &str); 4] = [
    ("work.list", ""),
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

const LIST_LIMIT_DEFAULT: i64 = 50;
const LIST_LIMIT_MAX: i64 = 100;
const NATIVE_DEPTH_MAX: usize = 500;

fn native_work(adapter: &str, listing: &oap_adapter::NativeListing) -> WorkJson {
    WorkJson {
        work_ref: WorkRef {
            adapter: adapter.to_owned(),
            session_id: String::new(),
            native_id: listing.native_id.clone(),
        },
        held: Some(false),
        native: Some(true),
        status: String::new(),
        state: if listing.running { "running" } else { "idle" }.to_owned(),
        directory: listing.directory.clone(),
        title: listing.title.clone(),
        link: listing.link.clone(),
        run_id: String::new(),
        last_reply: String::new(),
        updated_at_ms: listing.updated_at_ms,
        pending: None,
    }
}

#[derive(Default, serde::Deserialize)]
pub(crate) struct ListRequest {
    directory: Option<String>,
    adapters: Option<Vec<String>>,
    include_closed: Option<bool>,
    include_native: Option<bool>,
    limit: Option<i64>,
    cursor: Option<String>,
    search: Option<String>,
}

pub(crate) fn parse_list(
    raw: Option<&serde_json::value::RawValue>,
) -> Result<ListRequest, Refusal> {
    let Some(raw) = raw.filter(|raw| raw.get().trim() != "null") else {
        return Ok(ListRequest::default());
    };
    let malformed = || {
        refused(
            "invalid_request",
            "request is an object of directory, adapters, include_closed, include_native, limit, cursor and search",
        )
    };
    match serde_json::from_str::<Value>(raw.get()) {
        Ok(Value::Object(_)) => serde_json::from_str(raw.get()).map_err(|_| malformed()),
        _ => Err(malformed()),
    }
}

#[derive(Serialize, serde::Deserialize)]
struct ListCursor {
    at: i64,
    key: String,
    depth: usize,
}

struct Piece {
    directory: String,
    at: i64,
    key: String,
    value: WorkJson,
}

fn session_for_native(hub: &Hub, adapter: &str, native: &str) -> Option<SessionId> {
    hub.sessions()
        .into_iter()
        .find(|entry| {
            !native.is_empty()
                && entry.adapter() == adapter
                && entry.record().native_session_id == native
        })
        .map(|entry| entry.id().clone())
}

type Natives = (Vec<(String, oap_adapter::NativeListing)>, Vec<Value>);

fn natives_for(
    hub: &Hub,
    known: &std::collections::HashSet<String>,
    request: &ListRequest,
    limit: usize,
) -> Natives {
    let search = request.search.clone().unwrap_or_default();
    let mut found = Vec::new();
    let mut failures = Vec::new();
    for name in hub.registry().names() {
        if request
            .adapters
            .as_ref()
            .is_some_and(|adapters| !adapters.is_empty() && !adapters.contains(&name))
        {
            continue;
        }
        let Some(implementation) = hub.registry().lookup(&name) else {
            continue;
        };
        let directory = hub.registry().working_directory(&name).to_owned();
        let listed = if search.is_empty() {
            let Some(lister) = implementation.native_lister() else {
                continue;
            };
            block_on(lister.native_list(oap_adapter::NativeListRequest { directory, limit }))
        } else {
            let Some(searcher) = implementation.native_searcher() else {
                continue;
            };
            block_on(searcher.native_search(oap_adapter::NativeSearchRequest {
                directory,
                limit,
                term: search.clone(),
            }))
        };
        match listed {
            Err(err) => failures.push(serde_json::json!({
                "adapter": name,
                "message": err.to_string().trim(),
            })),
            Ok(listed) => {
                for listing in listed {
                    let bound = known.contains(&listing.native_id)
                        || session_for_native(hub, &name, &listing.native_id).is_some();
                    if search.is_empty() && bound {
                        continue;
                    }
                    found.push((name.clone(), listing));
                }
            }
        }
    }
    (found, failures)
}

pub(crate) fn list(hub: &Hub, request: ListRequest) -> Result<Value, Refusal> {
    use base64::engine::general_purpose::URL_SAFE_NO_PAD;
    use base64::Engine;
    let limit = match request.limit {
        None => LIST_LIMIT_DEFAULT,
        Some(limit) if (1..=LIST_LIMIT_MAX).contains(&limit) => limit,
        Some(_) => return Err(refused("invalid_request", "limit is 1 to 100")),
    } as usize;
    let cursor = request.cursor.clone().unwrap_or_default();
    let after = if cursor.is_empty() {
        None
    } else {
        let decoded = URL_SAFE_NO_PAD
            .decode(&cursor)
            .ok()
            .and_then(|raw| serde_json::from_slice::<ListCursor>(&raw).ok())
            .filter(|decoded| !decoded.key.is_empty() && decoded.depth >= 1);
        Some(
            decoded
                .ok_or_else(|| refused("invalid_request", "cursor is not one this list issued"))?,
        )
    };
    let directory = request.directory.clone().unwrap_or_default();
    let adapters = request.adapters.clone().unwrap_or_default();
    let wanted = |adapter: &str, place: &str| {
        (adapters.is_empty() || adapters.iter().any(|named| named == adapter))
            && (directory.is_empty() || place == directory)
    };
    let searching = request
        .search
        .as_deref()
        .is_some_and(|search| !search.is_empty());
    let mut pieces = Vec::new();
    let mut held = std::collections::HashMap::new();
    for found in block_on(hub.works()) {
        if !searching && wanted(&found.adapter, &found.directory) {
            pieces.push(Piece {
                directory: found.directory.clone(),
                at: found.updated_at_ms,
                key: format!("s:{}", found.session_id.as_str()),
                value: held_work(found.clone()),
            });
        }
        held.insert(found.session_id.clone(), found);
    }
    let mut known = std::collections::HashSet::new();
    let mut unheld = std::collections::HashMap::new();
    let entries = match hub.bindings() {
        None => Vec::new(),
        Some(bindings) => {
            oap_binding::sessions(&bindings.sessions().map_err(|_| history_unreadable())?)
        }
    };
    for entry in entries {
        let native = entry.record.native_session_id.clone();
        if !native.is_empty() {
            known.insert(native.clone());
        }
        if hub
            .session(&entry.record.session_id.as_str().into())
            .is_ok()
            || (entry.action == Action::Closed && request.include_closed != Some(true))
        {
            continue;
        }
        if !native.is_empty() {
            unheld.insert(format!("{}/{native}", entry.record.adapter), entry.clone());
        }
        if !searching && wanted(&entry.record.adapter, &entry.record.directory) {
            pieces.push(Piece {
                directory: entry.record.directory.clone(),
                at: entry.time_ms,
                key: format!("s:{}", entry.record.session_id),
                value: unheld_work(entry),
            });
        }
    }
    let depth = after.as_ref().map_or(0, |after| after.depth);
    let mut missing = Vec::new();
    if request.include_native == Some(true) || searching {
        let (found, failures) = natives_for(
            hub,
            &known,
            &request,
            (depth + limit + 1).min(NATIVE_DEPTH_MAX),
        );
        for (adapter, listing) in found {
            if searching {
                let bound = session_for_native(hub, &adapter, &listing.native_id)
                    .and_then(|id| held.get(&id).cloned());
                if let Some(session) = bound {
                    if wanted(&session.adapter, &session.directory) {
                        pieces.push(Piece {
                            directory: session.directory.clone(),
                            at: session.updated_at_ms,
                            key: format!("s:{}", session.session_id.as_str()),
                            value: held_work(session),
                        });
                    }
                    continue;
                }
                if let Some(entry) = unheld.get(&format!("{adapter}/{}", listing.native_id)) {
                    if wanted(&entry.record.adapter, &entry.record.directory) {
                        pieces.push(Piece {
                            directory: entry.record.directory.clone(),
                            at: entry.time_ms,
                            key: format!("s:{}", entry.record.session_id),
                            value: unheld_work(entry.clone()),
                        });
                    }
                    continue;
                }
                if known.contains(&listing.native_id) {
                    continue;
                }
            }
            if wanted(&adapter, &listing.directory) {
                pieces.push(Piece {
                    directory: listing.directory.clone(),
                    at: listing.updated_at_ms,
                    key: format!("n:{adapter}/{}", listing.native_id),
                    value: native_work(&adapter, &listing),
                });
            }
        }
        missing = failures;
    }
    pieces.sort_by(|a, b| b.at.cmp(&a.at).then_with(|| a.key.cmp(&b.key)));
    if let Some(after) = &after {
        pieces
            .retain(|piece| piece.at < after.at || (piece.at == after.at && piece.key > after.key));
    }
    let mut next_cursor = String::new();
    if pieces.len() > limit {
        let last = &pieces[limit - 1];
        let encoded = serde_json::to_vec(&ListCursor {
            at: last.at,
            key: last.key.clone(),
            depth: depth + limit,
        })
        .map_err(|err| refused("internal", err.to_string()))?;
        next_cursor = URL_SAFE_NO_PAD.encode(encoded);
        pieces.truncate(limit);
    }
    let mut groups: Vec<Value> = Vec::new();
    let mut at = std::collections::HashMap::new();
    for piece in pieces {
        let index = *at.entry(piece.directory.clone()).or_insert_with(|| {
            groups.push(serde_json::json!({
                "directory": piece.directory,
                "last_activity_ms": piece.at,
                "work": [],
            }));
            groups.len() - 1
        });
        let value = encoded(&piece.value)?;
        if let Some(work) = groups[index]["work"].as_array_mut() {
            work.push(value);
        }
    }
    if !directory.is_empty() && after.is_none() && groups.is_empty() {
        groups
            .push(serde_json::json!({ "directory": directory, "last_activity_ms": 0, "work": [] }));
    }
    let mut answer = serde_json::json!({ "groups": groups });
    if !next_cursor.is_empty() {
        answer["next_cursor"] = Value::String(next_cursor);
    }
    if !missing.is_empty() {
        answer["unavailable"] = Value::Array(missing);
    }
    Ok(answer)
}
