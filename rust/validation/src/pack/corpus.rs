use serde::{Deserialize, Deserializer};
use serde_json::Value;

use super::resources::base;
use super::{failure, text, CompiledPacks, PackLoadError};
use crate::codes;

#[derive(Default, Deserialize)]
#[serde(default)]
pub(super) struct Manifest {
    version: i64,
    #[serde(deserialize_with = "null_default")]
    pub(super) fixtures: Vec<Entry>,
}

#[derive(Default, Deserialize)]
#[serde(default)]
pub(super) struct Entry {
    #[serde(deserialize_with = "null_default")]
    id: String,
    #[serde(deserialize_with = "null_default")]
    pub(super) path: String,
    #[serde(deserialize_with = "null_default")]
    pub(super) kind: String,
    #[serde(deserialize_with = "null_default")]
    valid: bool,
    #[serde(deserialize_with = "null_default")]
    phase: String,
    #[serde(deserialize_with = "strings_default")]
    codes: Vec<String>,
    #[serde(deserialize_with = "strings_default")]
    pub(super) units: Vec<String>,
    #[serde(rename = "provenance", deserialize_with = "strings_default")]
    pub(super) _provenance: Vec<String>,
    #[serde(deserialize_with = "null_default")]
    pub(super) covers: Vec<Coverage>,
    #[serde(deserialize_with = "null_default")]
    pub(super) mode: String,
    #[serde(deserialize_with = "null_default")]
    pub(super) profile: String,
    #[serde(deserialize_with = "null_default")]
    scope: String,
    #[serde(deserialize_with = "strings_default")]
    pub(super) packs: Vec<String>,
    #[serde(rename = "rules", deserialize_with = "strings_default")]
    _rules: Vec<String>,
}

#[derive(Default, Deserialize)]
#[serde(default)]
pub(super) struct Coverage {
    #[serde(deserialize_with = "null_default")]
    pub(super) capability: String,
    #[serde(deserialize_with = "null_default")]
    pub(super) aspect: String,
}

fn null_default<'de, D: Deserializer<'de>, T: Deserialize<'de> + Default>(
    deserializer: D,
) -> Result<T, D::Error> {
    Ok(Option::<T>::deserialize(deserializer)?.unwrap_or_default())
}

fn strings_default<'de, D: Deserializer<'de>>(deserializer: D) -> Result<Vec<String>, D::Error> {
    Ok(Option::<Vec<Option<String>>>::deserialize(deserializer)?
        .unwrap_or_default()
        .into_iter()
        .map(Option::unwrap_or_default)
        .collect())
}

impl CompiledPacks {
    pub fn check_corpus_entries(&self) -> Result<(), PackLoadError> {
        read(self).map(|_| ())
    }
}

pub(super) fn read(compiled: &CompiledPacks) -> Result<Vec<(&Value, Manifest)>, PackLoadError> {
    let mut manifests = Vec::new();
    let mut refusals = Vec::new();
    for (_, descriptor) in compiled.resources().descriptors() {
        let Some(path) = compiled.resources().manifests().get(&base(descriptor)) else {
            continue;
        };
        let id = text(descriptor, "id");
        let parsed = (|| {
            let data = std::fs::read(path).map_err(|e| failure(id, e.to_string()))?;
            let manifest: Manifest =
                serde_json::from_slice(&data).map_err(|e| failure(id, e.to_string()))?;
            if manifest.version != 1 {
                return Err(failure(id, "manifest version must be 1".to_owned()));
            }
            for entry in &manifest.fixtures {
                check_entry(entry, id)?;
            }
            Ok(manifest)
        })();
        match parsed {
            Ok(manifest) => manifests.push((descriptor, manifest)),
            Err(error) => refusals.extend(error.refusals),
        }
    }
    if refusals.is_empty() {
        Ok(manifests)
    } else {
        Err(PackLoadError { refusals })
    }
}

fn check_entry(entry: &Entry, owner: &str) -> Result<(), PackLoadError> {
    let invalid = |reason| failure(owner, format!("fixture {:?}: {reason}", entry.id));
    if !matches!(
        entry.kind.as_str(),
        "positive" | "schema-invalid" | "semantic-invalid" | "load-invalid"
    ) {
        return Err(invalid("unknown fixture kind"));
    }
    if !matches!(entry.profile.as_str(), "" | "model-provider-core")
        || (!entry.scope.is_empty() && entry.profile.is_empty())
    {
        return Err(invalid(
            "unknown profile or scope outside the provider profile",
        ));
    }
    let provider = !entry.profile.is_empty();
    if provider {
        let trace = !entry.valid
            && !entry.codes.is_empty()
            && entry.codes.iter().all(|c| {
                matches!(
                    c.as_str(),
                    codes::SEQUENCE_GAP
                        | codes::SEQUENCE_REGRESSION
                        | codes::EVENT_AFTER_TERMINAL
                        | codes::TERMINAL_NOT_ASSEMBLY
                )
            });
        if entry.scope != if trace { "trace" } else { "frame" }
            || entry.kind == "load-invalid"
            || !entry.mode.is_empty()
            || !entry.packs.is_empty()
            || !entry.covers.is_empty()
            || entry
                .units
                .iter()
                .any(|u| !matches!(u.as_str(), "provider-core" | "credentials" | "carry"))
        {
            return Err(invalid(
                "invalid provider scope, units or agent-only fields",
            ));
        }
    }
    if entry.valid {
        if entry.kind != "positive" || !entry.phase.is_empty() || !entry.codes.is_empty() {
            return Err(invalid("inconsistent positive expectation"));
        }
    } else {
        let load = entry.kind == "load-invalid";
        if entry.kind == "positive"
            || entry.codes.is_empty()
            || (load && entry.phase != "load")
            || (!load && !matches!(entry.phase.as_str(), "decode" | "schema" | "semantic"))
        {
            return Err(invalid("invalid phase or missing diagnostic codes"));
        }
        for code in &entry.codes {
            let known = if load {
                LOAD_CODES.contains(&code.as_str())
            } else if provider {
                PROVIDER_CODES.contains(&code.as_str())
            } else {
                AGENT_CODES.contains(&code.as_str())
            };
            if !known {
                return Err(invalid("unknown diagnostic or load-error code"));
            }
        }
    }
    if provider && entry.units.is_empty() {
        return Err(invalid("missing provider conformance units"));
    }

    Ok(())
}

const AGENT_CODES: &[&str] = &[
    codes::UNMATCHED_STEER,
    codes::DUPLICATE_STEER,
    codes::COMPACTION_UNPAIRED,
    codes::COMPACTION_ENDED_WITHOUT_START,
    codes::COMPACTION_OPEN_AT_TERMINAL,
    codes::COMPACTION_RUN_MISMATCH,
    codes::COMPACTION_STOP_REASON_MISMATCH,
    codes::COMPACTION_FAILED_WITHOUT_ERROR,
    codes::PENDING_STEER_AT_TERMINAL,
    codes::MALFORMED_JSON,
    codes::DUPLICATE_KEY,
    codes::SCHEMA_INVALID,
    codes::PAYLOAD_DECODE,
    codes::DUPLICATE_ENVELOPE_ID,
    codes::SCOPE_MISMATCH,
    codes::UNMATCHED_RESPONSE,
    codes::MISSING_RESPONSE,
    codes::DUPLICATE_RESPONSE,
    codes::SEQUENCE_GAP,
    codes::SEQUENCE_REGRESSION,
    codes::ILLEGAL_RUN_TRANSITION,
    codes::MISSING_RUN_STARTED,
    codes::MISSING_RUN_TERMINAL,
    codes::DUPLICATE_RUN_TERMINAL,
    codes::EVENT_AFTER_TERMINAL,
    codes::DUPLICATE_MESSAGE_COMPLETION,
    codes::CONTENT_AFTER_COMPLETION,
    codes::PENDING_TOOL_AT_TERMINAL,
    codes::PENDING_INTERACTION_AT_TERMINAL,
    codes::UNMATCHED_TOOL,
    codes::ILLEGAL_TOOL_TRANSITION,
    codes::DUPLICATE_INTERACTION,
    codes::UNMATCHED_INTERACTION,
    codes::WRONG_INTERACTION_RESPONDER,
    codes::UNAVAILABLE_CAPABILITY,
    codes::UNHONOURED_CAPABILITY,
    codes::STALE_CAPABILITY_REVISION,
    codes::CANCEL_NOT_SETTLED,
    codes::UNDECLARED_REPLAY_GAP,
    codes::UNKNOWN_PARTICIPANT,
    codes::SESSION_STATE_MISMATCH,
    codes::UNAPPLIED_CONTROL,
    codes::UNSATISFIABLE_CONTROL,
    codes::DEGRADED_WITHOUT_OPTIN,
    codes::DUPLICATE_TOOL_NAME,
    codes::UNDISCLOSED_SELECTION_SCOPE,
    codes::QUEUE_ORDER_VIOLATION,
    codes::QUEUE_LIMIT_EXCEEDED,
    codes::PREMATURE_SESSION_MUTATION,
    codes::UNDISCLOSED_QUEUE_LIMIT,
    codes::UNMATCHED_PROVIDER,
    codes::DUPLICATE_PROVIDER,
    codes::UNMATCHED_TOOL_SOURCE,
    codes::DUPLICATE_TOOL_SOURCE,
    codes::CATALOG_MISMATCH,
    codes::ATTACHMENT_FIELD_IN_CATALOG,
    codes::UNDISCLOSED_ATTACH_MODES,
    codes::UNDISCLOSED_ATTACH_LIMIT,
    codes::UNATTRIBUTED_CALL,
    codes::WRONG_TOOL_OWNER,
    codes::UNDISCLOSED_PROVIDE_LIMIT,
    codes::RESOLUTION_PAYLOAD_MISMATCH,
    codes::MODEL_NOT_IN_CATALOG,
    codes::AMBIGUOUS_DEFAULT_MODEL,
    codes::DUPLICATE_MODEL_ID,
    codes::UNANNOUNCED_CATALOG_CHANGE,
    codes::AUTH_FLOW_ORDER,
    codes::MISSING_AUTH_TERMINAL,
    codes::SESSION_LIST_OVER_LIMIT,
    codes::SESSION_LIST_ORDER,
    codes::DUPLICATE_SESSION_ENTRY,
    codes::TRANSCRIPT_OVER_LIMIT,
    codes::DUPLICATE_TRANSCRIPT_ENTRY,
    codes::TRANSCRIPT_GAP_MISMATCH,
];

const PROVIDER_CODES: &[&str] = &[
    codes::MALFORMED_JSON,
    codes::DUPLICATE_KEY,
    codes::PAYLOAD_DECODE,
    codes::SCHEMA_INVALID,
    codes::CREDENTIAL_IN_TRACE,
    codes::CREDENTIAL_IN_HEADERS,
    codes::TERMINAL_NOT_ASSEMBLY,
    codes::EVENT_AFTER_TERMINAL,
    codes::SEQUENCE_GAP,
    codes::SEQUENCE_REGRESSION,
];

const LOAD_CODES: &[&str] = &[
    "pack_unprefixed_name",
    "pack_foreign_prefix",
    "pack_id_collision",
    "pack_type_duplicate",
    "pack_branch_undeclared_type",
    "pack_branch_unpinned",
    "pack_ungated_type",
    "pack_restates_core_member",
    "pack_member_target_unknown",
    "pack_member_duplicate",
    "pack_role_undeclared",
    "pack_response_gated",
    "pack_reply_target_unknown",
    "pack_reply_target_ambiguous",
    "pack_refusal_undeclared",
    "pack_schema_path_escape",
    "pack_external_ref",
    "pack_dependency_missing",
    "pack_fixture_claims_core_unit",
    "ext_claim_without_pack",
];
