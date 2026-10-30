use std::fmt;
use std::io::{BufRead, Write};

use serde::de::{self, MapAccess, Visitor};
use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use serde_json::Value;

use crate::live::block_on;
use crate::Hub;

pub const DEFAULT_FRAME_LIMIT: usize = (16 << 20) + (2 << 20);

pub const MIN_FRAME_LIMIT: usize = 256;

const MESSAGE_LIMIT: usize = 300;

const PARAMS: [&str; 8] = [
    "adapter",
    "session_id",
    "run_id",
    "after",
    "request",
    "cursor",
    "limit",
    "allow_degraded_features",
];

#[derive(Clone, Debug)]
pub struct StdioOptions {
    pub frame_limit: usize,
}

impl Default for StdioOptions {
    fn default() -> Self {
        Self {
            frame_limit: DEFAULT_FRAME_LIMIT,
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum StdioError {
    #[error(
        "servestdio: frame limit {0} is below the 256-byte minimum a correlated refusal needs"
    )]
    FrameLimit(usize),
    #[error("line {line} is not a valid request: {detail}")]
    MalformedLine { line: usize, detail: String },
    #[error("servestdio: encoded line exceeds the frame limit")]
    LineTooLarge,
    #[error(transparent)]
    Io(#[from] std::io::Error),
}

#[derive(Debug, Default)]
struct Request {
    id: i64,
    op: String,
    session_id: String,
    request: Option<Box<RawValue>>,
    present: Vec<String>,
}

#[derive(Serialize)]
struct WireError {
    code: &'static str,
    message: String,
}

#[derive(Serialize)]
struct Response<'a> {
    id: i64,
    ok: bool,
    result: &'a RawValue,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<WireError>,
}

pub fn serve(
    hub: &Hub,
    mut input: impl BufRead,
    mut output: impl Write,
    options: &StdioOptions,
) -> Result<(), StdioError> {
    if options.frame_limit < MIN_FRAME_LIMIT {
        return Err(StdioError::FrameLimit(options.frame_limit));
    }
    let mut number = 0;
    let mut ids = 0;
    loop {
        number += 1;
        let malformed = |detail: String| StdioError::MalformedLine {
            line: number,
            detail,
        };
        let Some(frame) = read_frame(&mut input, options.frame_limit).map_err(|err| match err {
            FrameError::Defect(detail) => malformed(detail),
            FrameError::Io(err) => StdioError::Io(err),
        })?
        else {
            return Ok(());
        };
        let request = decode_request(&frame).map_err(malformed)?;
        let answer = dispatch(hub, &request, &mut ids);
        let Some(line) = fitted(request.id, answer, options.frame_limit)? else {
            continue;
        };
        output.write_all(&line)?;
        output.write_all(b"\n")?;
        output.flush()?;
    }
}

enum FrameError {
    Defect(String),
    Io(std::io::Error),
}

fn read_frame(input: &mut impl BufRead, limit: usize) -> Result<Option<Vec<u8>>, FrameError> {
    let mut frame = Vec::new();
    loop {
        let available = input.fill_buf().map_err(FrameError::Io)?;
        if available.is_empty() {
            if frame.is_empty() {
                return Ok(None);
            }
            return Err(FrameError::Defect("unterminated final line".into()));
        }
        let (taken, ended) = match available.iter().position(|byte| *byte == b'\n') {
            Some(at) => (at + 1, true),
            None => (available.len(), false),
        };
        if frame.len() + taken > limit + 1 {
            return Err(FrameError::Defect(format!(
                "line exceeds the {limit}-byte frame limit"
            )));
        }
        frame.extend_from_slice(&available[..taken]);
        input.consume(taken);
        if !ended {
            continue;
        }
        frame.pop();
        if frame.contains(&b'\r') {
            return Err(FrameError::Defect(
                "carriage return is not valid framing".into(),
            ));
        }
        if frame.is_empty() {
            return Err(FrameError::Defect("empty line".into()));
        }
        if std::str::from_utf8(&frame).is_err() {
            return Err(FrameError::Defect("line is not UTF-8".into()));
        }
        return Ok(Some(frame));
    }
}

struct Members(Vec<(String, Box<RawValue>)>);

impl<'de> Deserialize<'de> for Members {
    fn deserialize<D: de::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct Collect;

        impl<'de> Visitor<'de> for Collect {
            type Value = Members;

            fn expecting(&self, formatter: &mut fmt::Formatter) -> fmt::Result {
                formatter.write_str("request is not a JSON object")
            }

            fn visit_map<A: MapAccess<'de>>(self, mut map: A) -> Result<Members, A::Error> {
                let mut members = Vec::new();
                while let Some(key) = map.next_key::<String>()? {
                    members.push((key, map.next_value::<Box<RawValue>>()?));
                }
                Ok(Members(members))
            }
        }

        deserializer.deserialize_map(Collect)
    }
}

fn invalid(detail: impl fmt::Display) -> String {
    trim(&format!("invalid JSON request: {detail}"))
}

fn decode_request(frame: &[u8]) -> Result<Request, String> {
    let mut deserializer = serde_json::Deserializer::from_slice(frame);
    let Members(members) = Members::deserialize(&mut deserializer).map_err(invalid)?;
    deserializer
        .end()
        .map_err(|_| invalid("trailing data after the request object"))?;
    let mut request = Request::default();
    let mut id = None;
    for (key, value) in members {
        if request.present.contains(&key) {
            return Err(invalid(format!("repeated key {key:?}")));
        }
        let typed = |ok: bool| {
            if ok {
                Ok(())
            } else {
                Err(invalid(format!("field {key:?} has the wrong type")))
            }
        };
        let raw = matches!(key.as_str(), "after" | "request");
        let parsed: Value = if raw {
            Value::Null
        } else {
            serde_json::from_str(value.get()).map_err(invalid)?
        };
        match key.as_str() {
            "id" => {
                id = parsed.as_i64();
                typed(id.is_some())?;
            }
            "op" => {
                typed(parsed.is_string())?;
                request.op = parsed.as_str().unwrap_or_default().to_owned();
            }
            "adapter" | "session_id" | "run_id" | "cursor" => {
                typed(parsed.is_string() || parsed.is_null())?;
                if key == "session_id" {
                    request.session_id = parsed.as_str().unwrap_or_default().to_owned();
                }
            }
            "limit" => typed(parsed.is_i64() || parsed.is_null())?,
            "allow_degraded_features" => typed(match &parsed {
                Value::Array(items) => items.iter().all(Value::is_string),
                Value::Null => true,
                _ => false,
            })?,
            "request" => request.request = Some(value),
            "after" => {}
            _ => {
                return Err(invalid(format!(
                    "field {key:?} must use its exact protocol spelling"
                )))
            }
        }
        request.present.push(key);
    }
    request.id = id.ok_or_else(|| invalid("id is required"))?;
    if request.op.is_empty() {
        return Err(invalid("op is required"));
    }
    Ok(request)
}

fn trim(message: &str) -> String {
    match message.char_indices().nth(MESSAGE_LIMIT) {
        Some((at, _)) => format!("{}…", &message[..at]),
        None => message.to_owned(),
    }
}

type Answer = Result<Value, WireError>;

impl Request {
    fn only(&self, allowed: &[&str]) -> Result<(), WireError> {
        let extra: Vec<&str> = PARAMS
            .into_iter()
            .filter(|param| !allowed.contains(param) && self.present.iter().any(|key| key == param))
            .collect();
        if extra.is_empty() {
            return Ok(());
        }
        Err(WireError {
            code: "invalid_request",
            message: format!(
                "op {:?} accepts no {} parameter",
                trim(&self.op),
                extra.join(", ")
            ),
        })
    }
}

fn next_id(ids: &mut u64, kind: &str) -> String {
    *ids += 1;
    format!("oap-{kind}-{ids}")
}

fn session(hub: &Hub, id: &str) -> Result<std::sync::Arc<crate::HubSession>, WireError> {
    hub.session(&id.into()).map_err(|err| WireError {
        code: "unknown_session",
        message: trim(&err.to_string()),
    })
}

fn wire(code: &'static str, message: impl fmt::Display) -> WireError {
    WireError {
        code,
        message: trim(&message.to_string()),
    }
}

const MAX_ENVELOPE_BYTES: usize = 16 << 20;

fn gate_request(
    raw: Option<&RawValue>,
    want: &[oap_protocol::EnvelopeType],
) -> Result<oap_protocol::Envelope, WireError> {
    let raw = raw
        .map(RawValue::get)
        .filter(|text| *text != "null")
        .ok_or_else(|| wire("invalid_request", "the op requires a \"request\" envelope"))?;
    if raw.len() > MAX_ENVELOPE_BYTES {
        return Err(wire(
            "request_too_large",
            "request envelope exceeds the daemon limit",
        ));
    }
    let value: Value = serde_json::from_str(raw).map_err(|err| wire("malformed_json", err))?;
    if !value.is_object() {
        return Err(wire(
            "malformed_json",
            "the request envelope is not an object",
        ));
    }
    let validator = crate::schema::envelope_validator().map_err(|err| wire("internal", err))?;
    validator
        .validate(&value)
        .map_err(|err| wire("schema_invalid", err))?;
    let envelope =
        oap_protocol::Envelope::parse(raw.as_bytes()).map_err(|err| wire("malformed_json", err))?;
    if !want.contains(&envelope.envelope_type) {
        let names: Vec<&str> = want.iter().map(|kind| kind.as_str()).collect();
        return Err(wire(
            "type_mismatch",
            format!(
                "op expects {}, got {}",
                names.join(" or "),
                envelope.envelope_type.as_str()
            ),
        ));
    }
    Ok(envelope)
}

fn payload<T: serde::de::DeserializeOwned>(
    envelope: &oap_protocol::Envelope,
) -> Result<T, WireError> {
    envelope
        .decode_payload()
        .map_err(|err| wire("invalid_payload", err))
}

fn reply(
    kind: oap_protocol::EnvelopeType,
    request: &oap_protocol::Envelope,
    session_id: oap_protocol::SessionId,
    run_id: oap_protocol::RunId,
    body: &impl Serialize,
    ids: &mut u64,
) -> Answer {
    let mut response = oap_protocol::Envelope::new(kind, next_id(ids, "response"), body)
        .map_err(|err| wire("internal", err))?;
    response.in_reply_to = Some(request.id.clone());
    response.session_id = Some(session_id);
    response.run_id = Some(run_id);
    response.capability_revision = request.capability_revision.clone();
    serde_json::to_value(&response).map_err(|err| wire("internal", err))
}

fn adapter_code(err: &crate::HubError) -> &'static str {
    use oap_adapter::Error;
    match err {
        crate::HubError::ScopeMismatch { .. } => "scope_mismatch",
        crate::HubError::Adapter(Error::SessionClosed) => "session_closed",
        crate::HubError::Adapter(Error::RunNotFound) => "run_not_found",
        crate::HubError::Adapter(Error::RunAlreadyTerminal { .. }) => "run_terminal",
        crate::HubError::Adapter(
            Error::InteractionNotFound
            | Error::InteractionResolved
            | Error::WrongResponder
            | Error::InvalidResolution,
        ) => "resolution_rejected",
        _ => "internal",
    }
}

fn cancel(hub: &Hub, request: &Request, ids: &mut u64) -> Answer {
    let envelope = gate_request(
        request.request.as_deref(),
        &[oap_protocol::EnvelopeType::RUN_CANCEL_REQUEST],
    )?;
    let asked: oap_protocol::RunCancelRequest = payload(&envelope)?;
    let entry = session(hub, &request.session_id)?;
    if &asked.session_id != entry.id() {
        return Err(wire(
            "scope_mismatch",
            format!(
                "payload session_id \"{}\" does not match the addressed session \"{}\"",
                asked.session_id,
                entry.id()
            ),
        ));
    }
    let ack = block_on(entry.cancel(asked.run_id)).map_err(|err| wire(adapter_code(&err), err))?;
    reply(
        oap_protocol::EnvelopeType::RUN_CANCEL_RESPONSE,
        &envelope,
        ack.session_id.clone(),
        ack.run_id.clone(),
        &ack,
        ids,
    )
}

fn resolve(hub: &Hub, request: &Request, ids: &mut u64) -> Answer {
    use oap_protocol::EnvelopeType;
    let envelope = gate_request(
        request.request.as_deref(),
        &[
            EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST,
            EnvelopeType::USER_INPUT_RESOLVE_REQUEST,
            EnvelopeType::ACTION_CALL_RESOLVE_REQUEST,
        ],
    )?;
    let entry = session(hub, &request.session_id)?;
    let mut resolution = oap_adapter::InteractionResolution::default();
    let (kind, interaction, session_id, _) =
        if envelope.envelope_type == EnvelopeType::ACTION_PERMISSION_RESOLVE_REQUEST {
            let asked: oap_protocol::PermissionResolveRequest = payload(&envelope)?;
            let named = (
                EnvelopeType::ACTION_PERMISSION_RESOLVE_RESPONSE,
                asked.interaction_id.clone(),
                asked.session_id.clone(),
                asked.run_id.clone(),
            );
            resolution.run_id = asked.run_id.clone();
            resolution.responded_by = asked.responded_by.clone();
            resolution.permission = Some(asked);
            named
        } else if envelope.envelope_type == EnvelopeType::USER_INPUT_RESOLVE_REQUEST {
            let asked: oap_protocol::UserInputResolveRequest = payload(&envelope)?;
            let named = (
                EnvelopeType::USER_INPUT_RESOLVE_RESPONSE,
                asked.interaction_id.clone(),
                asked.session_id.clone(),
                asked.run_id.clone(),
            );
            resolution.run_id = asked.run_id.clone();
            resolution.responded_by = asked.responded_by.clone();
            resolution.input = Some(asked);
            named
        } else {
            return Err(wire(
                "unsupported_feature",
                "the hub does not yet resolve provided-tool calls over stdio",
            ));
        };
    let run = resolution.run_id.clone();
    block_on(entry.resolve(resolution)).map_err(|err| wire(adapter_code(&err), err))?;
    let body = oap_protocol::UserInputResolveResponse {
        interaction_id: interaction,
        session_id,
        run_id: run.clone(),
        accepted: true,
    };
    reply(kind, &envelope, entry.id().clone(), run, &body, ids)
}

fn rfc3339(ms: i64) -> String {
    let seconds = ms.div_euclid(1000);
    let (days, rest) = (seconds.div_euclid(86_400), seconds.rem_euclid(86_400));
    let shifted = days + 719_468;
    let era = shifted.div_euclid(146_097);
    let of_era = shifted.rem_euclid(146_097);
    let year_of_era = (of_era - of_era / 1460 + of_era / 36_524 - of_era / 146_096) / 365;
    let day_of_year = of_era - (365 * year_of_era + year_of_era / 4 - year_of_era / 100);
    let month_index = (5 * day_of_year + 2) / 153;
    let day = day_of_year - (153 * month_index + 2) / 5 + 1;
    let month = if month_index < 10 {
        month_index + 3
    } else {
        month_index - 9
    };
    let year = year_of_era + era * 400 + i64::from(month <= 2);
    format!(
        "{year:04}-{month:02}-{day:02}T{:02}:{:02}:{:02}Z",
        rest / 3600,
        rest / 60 % 60,
        rest % 60
    )
}

fn dispatch(hub: &Hub, request: &Request, ids: &mut u64) -> Answer {
    match request.op.as_str() {
        "sessions" => {
            request.only(&[])?;
            let listed: Vec<Value> = block_on(hub.session_statuses())
                .into_iter()
                .map(|status| {
                    let mut info = serde_json::json!({
                        "session_id": status.session_id,
                        "adapter": status.adapter,
                        "status": status.status,
                        "created_at": rfc3339(status.created_ms),
                    });
                    if let Some(run) = status.active_run_id {
                        info["active_run_id"] = serde_json::json!(run);
                    }
                    if !status.active_runs.is_empty() {
                        info["active_runs"] = serde_json::json!(status.active_runs);
                    }
                    info
                })
                .collect();
            Ok(serde_json::json!({ "sessions": listed }))
        }
        "state" => {
            request.only(&["session_id"])?;
            let entry = session(hub, &request.session_id)?;
            let state = match block_on(entry.state()) {
                Ok(state) => state,
                Err(crate::HubError::Adapter(oap_adapter::Error::SessionClosed)) => {
                    oap_protocol::SessionState {
                        session_id: entry.id().clone(),
                        status: oap_protocol::SessionStatus::CLOSED,
                        ..oap_protocol::SessionState::default()
                    }
                }
                Err(err) => {
                    return Err(WireError {
                        code: "state_failed",
                        message: trim(&err.to_string()),
                    })
                }
            };
            let mut envelope = oap_protocol::Envelope::new(
                oap_protocol::EnvelopeType::SESSION_STATE_RESPONSE,
                next_id(ids, "response"),
                &state,
            )
            .map_err(|err| WireError {
                code: "internal",
                message: trim(&err.to_string()),
            })?;
            envelope.in_reply_to = Some(next_id(ids, "request").into());
            envelope.session_id = Some(state.session_id.clone());
            Ok(serde_json::to_value(&envelope).unwrap_or(Value::Null))
        }
        "cancel" => {
            request.only(&["session_id", "request"])?;
            cancel(hub, request, ids)
        }
        "resolve" => {
            request.only(&["session_id", "request"])?;
            resolve(hub, request, ids)
        }
        "close" => {
            request.only(&["session_id"])?;
            let entry = session(hub, &request.session_id)?;
            block_on(entry.close()).map_err(|err| WireError {
                code: match &err {
                    crate::HubError::Adapter(oap_adapter::Error::RunActive) => "run_active",
                    crate::HubError::Adapter(oap_adapter::Error::SessionClosed) => "session_closed",
                    _ => "internal",
                },
                message: trim(&err.to_string()),
            })?;
            Ok(Value::Null)
        }
        "adapters" => {
            request.only(&[])?;
            Ok(adapters(hub))
        }
        _ => Err(WireError {
            code: "unknown_op",
            message: format!("no op {:?}", trim(&request.op)),
        }),
    }
}

#[derive(Serialize)]
struct AdapterInfo {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    capability_revision: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    capabilities: Option<oap_protocol::CapabilityDescriptor>,
    #[serde(skip_serializing_if = "String::is_empty")]
    error: String,
}

fn adapters(hub: &Hub) -> Value {
    let listed: Vec<AdapterInfo> = block_on(hub.adapters())
        .into_iter()
        .map(|(name, probed)| match probed {
            Ok(descriptor) => AdapterInfo {
                name,
                capability_revision: descriptor.capability_revision,
                capabilities: Some(descriptor.capabilities),
                error: String::new(),
            },
            Err(err) => AdapterInfo {
                name,
                capability_revision: String::new(),
                capabilities: None,
                error: trim(&err.to_string()),
            },
        })
        .collect();
    serde_json::json!({ "adapters": listed })
}

fn fitted(id: i64, answer: Answer, limit: usize) -> Result<Option<Vec<u8>>, StdioError> {
    let reduced = match &answer {
        Err(error) => Some(WireError {
            code: error.code,
            message: trim(&error.message),
        }),
        Ok(_) => None,
    };
    let fallback = WireError {
        code: "response_too_large",
        message: "the encoded response exceeds the frame limit".into(),
    };
    for candidate in [Some(answer), reduced.map(Err), Some(Err(fallback))]
        .into_iter()
        .flatten()
    {
        let line = response_line(id, candidate)?;
        if line.len() <= limit {
            return Ok(Some(line));
        }
    }
    Ok(None)
}

fn response_line(id: i64, answer: Answer) -> Result<Vec<u8>, StdioError> {
    let (result, error) = match answer {
        Ok(value) => (value, None),
        Err(error) => (Value::Null, Some(error)),
    };
    let result = serde_json::value::to_raw_value(&result).map_err(|_| StdioError::LineTooLarge)?;
    serde_json::to_vec(&Response {
        id,
        ok: error.is_none(),
        result: &result,
        error,
    })
    .map_err(|_| StdioError::LineTooLarge)
}

#[cfg(test)]
mod tests {
    #[test]
    fn rfc3339_spells_utc_seconds_across_leap_days_and_centuries() {
        for (ms, want) in [
            (0, "1970-01-01T00:00:00Z"),
            (951_782_400_000, "2000-02-29T00:00:00Z"),
            (1_761_782_400_123, "2025-10-30T00:00:00Z"),
            (4_102_444_799_000, "2099-12-31T23:59:59Z"),
        ] {
            assert_eq!(super::rfc3339(ms), want);
        }
    }
}
