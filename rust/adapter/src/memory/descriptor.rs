use std::collections::BTreeMap;

use oap_protocol::*;

use super::{
    attach_support, declared_sources, provide_support, raw, scripted_catalog, support, Memory,
    CAPABILITY_REVISION, ENDPOINT_ID, FIXED_RESULT,
};
use crate::{Descriptor, JournalDescriptor};

pub(super) fn features() -> BTreeMap<String, FeatureSupport> {
    let native = |reason: &str| support(SupportLevel::NATIVE, reason);
    let emulated = |reason: &str| support(SupportLevel::EMULATED, reason);
    let degraded = |reason: &str| support(SupportLevel::DEGRADED, reason);
    let run_scoped = |reason: &str| FeatureSupport {
        scope: SCOPE_RUN.to_owned(),
        ..emulated(reason)
    };
    let gate = "the reference adapter exposes an interactive scripted gate";
    [
        ("protocol.initialize", native("")),
        ("capabilities", native("")),
        ("session.open", native("")),
        (FEATURE_OPEN_SUBSCRIBE, native("the journal exists from the open, so a subscription registered there misses nothing")),
        (FEATURE_OPEN_REOPEN, emulated("a closed session's model and compaction policy are kept in process memory, so a reopen in the same process restores them and one after a restart is refused")),
        ("session.state", native("")),
        ("session.message.submit", native("")),
        ("session.message.delivery.auto", native("")),
        (FEATURE_DELIVERY_QUEUE, emulated("a busy session reserves one second run and promotes it when the started run settles")),
        (FEATURE_DELIVERY_STEER, emulated("guidance waits on the target run and is applied at its input gate, the scripted turn boundary")),
        (FEATURE_SESSION_COMPACT, emulated("a compaction run replaces the scripted history with a fixed summary that names the focus, and has no model to write it")),
        (FEATURE_RUN_COMPACTION, emulated("the reference adapter publishes the compactions it is asked for, and compacts on its own at the start of a run once its estimate of the history, a token per four bytes of text, reaches the session's threshold, or for overflow once the history no longer fits the reference model's 8192-token window, whatever the policy")),
        (FEATURE_COMPACTION_POLICY, FeatureSupport {
            modes: vec![MODE_SESSION_OPEN.to_owned(), MODE_SESSION_LIVE.to_owned()],
            ..emulated("auto compacts at 80% of the reference model's window, share and tokens set the threshold, off never compacts on its own except for overflow past the reference window, and an update takes effect at the next run's start")
        }),
        ("run.streaming", native("")),
        ("run.status", native("")),
        ("run.cancel", emulated("run-target API is implemented over a one-active-run session")),
        ("run.resume", degraded("reattachment and replay use a bounded process-memory journal")),
        ("run.reconciliation", native("")),
        ("run.replay", degraded("older cursors can expire and no cross-process replay is claimed")),
        ("action.tools", emulated("the reference adapter projects the scripted tool lifecycle")),
        ("action.tools.execute", emulated("the reference adapter executes a fixed deterministic script")),
        (FEATURE_TOOLS_LIST, emulated("the reference catalog is the scripted tool plus the session's attached sources")),
        (FEATURE_TOOL_SOURCES_ATTACH, attach_support()),
        (FEATURE_TOOLS_PROVIDE, provide_support()),
        ("action.permissions", emulated(gate)),
        ("user_input", emulated(gate)),
        (FEATURE_MODEL_SELECTION, run_scoped("the reference adapter runs no model; it echoes a selection from a fixed catalog for one run")),
        (FEATURE_SESSION_MODEL_SWITCH, emulated("the reference adapter changes the session default within its fixed catalog")),
        (FEATURE_MODELS_LIST, native("the reference adapter serves its fixed catalog, which is exactly the set its model gate admits")),
        (FEATURE_INSTRUCTIONS, emulated("instructions are prepended to the scripted text so their effect is observable")),
        (FEATURE_TOOL_SELECTION, run_scoped("the policy filters the scripted tool and is not retained past the run")),
        (FEATURE_STRUCTURED_OUTPUT, FeatureSupport {
            constraints: BTreeMap::from([(CONSTRAINT_FIXED_RESULT.to_owned(), raw(FIXED_RESULT))]),
            ..emulated("the scripted result is fixed, so only a schema that object satisfies is admitted")
        }),
    ]
    .into_iter()
    .map(|(key, support)| (key.to_owned(), support))
    .collect()
}

impl Memory {
    pub fn describe(&self) -> Descriptor {
        let endpoint = EndpointDescriptor {
            id: EndpointId::from(ENDPOINT_ID),
            name: "Deterministic In-Memory Reference Adapter".to_owned(),
            version: VERSION.to_owned(),
            adapter: "process-memory-script".to_owned(),
        };
        Descriptor {
            capabilities: CapabilityDescriptor {
                endpoint,
                protocol_versions: vec![VERSION.to_owned()],
                profiles: vec![PROFILE.to_owned()],
                features: features(),
                tools: scripted_catalog(),
                sources: declared_sources(),
                limits: Some(CapabilityLimits {
                    max_active_runs_per_session: Some(2),
                    max_queued_runs_per_session: Some(1),
                }),
                ..CapabilityDescriptor::default()
            },
            capability_revision: CAPABILITY_REVISION.to_owned(),
            journal: JournalDescriptor {
                scope: "session".to_owned(),
                persistence: "process_memory".to_owned(),
                replay: SupportLevel::DEGRADED,
                capacity: self.capacity,
            },
            max_active_runs_per_session: 2,
            interactive_gates: true,
            cancellation_target: "run".to_owned(),
            cancellation_implementation: "session_emulated".to_owned(),
        }
    }
}
