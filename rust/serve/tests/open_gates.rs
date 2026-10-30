use std::collections::BTreeMap;
use std::sync::Arc;

use futures::executor::block_on;
use oap_adapter::{
    Adapter, BoxFuture, Descriptor, Error, OpenRequest, Session, CONTROL_UNADVERTISED,
};
use oap_protocol::{
    CapabilityDescriptor, CompactionPolicy, FeatureSupport, SessionOpenRequest, SupportLevel,
    ToolSourceAttachment, FEATURE_COMPACTION_POLICY, FEATURE_OPEN_REOPEN, FEATURE_OPEN_SUBSCRIBE,
    FEATURE_TOOL_SOURCES_ATTACH, MODE_SESSION_OPEN, TOOL_SOURCE_PROCESS, TOOL_SOURCE_REMOTE,
};
use oap_serve::{attachment_gate, election_gate, resolve_attachments, Hub, HubError, Registry};

struct Advertising(BTreeMap<String, FeatureSupport>, &'static str);

impl Adapter for Advertising {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        let descriptor = Descriptor {
            capability_revision: self.1.into(),
            capabilities: CapabilityDescriptor {
                features: self.0.clone(),
                ..CapabilityDescriptor::default()
            },
            ..Descriptor::default()
        };
        Box::pin(async move { Ok(descriptor) })
    }

    fn open(&self, _: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async { Err(Error::SessionClosed) })
    }
}

fn support(level: SupportLevel, modes: &[&str]) -> FeatureSupport {
    FeatureSupport {
        level,
        modes: modes.iter().map(|mode| (*mode).to_owned()).collect(),
        ..FeatureSupport::default()
    }
}

fn hub(features: &[(&str, FeatureSupport)]) -> Hub {
    hub_at(features, "rev-1")
}

fn hub_at(features: &[(&str, FeatureSupport)], revision: &'static str) -> Hub {
    let mut registry = Registry::new();
    registry
        .register(
            "adv",
            Arc::new(Advertising(
                features
                    .iter()
                    .map(|(key, support)| ((*key).to_owned(), support.clone()))
                    .collect(),
                revision,
            )),
        )
        .unwrap();
    registry
        .register_tool_source(
            "fs",
            ToolSourceAttachment {
                kind: TOOL_SOURCE_PROCESS.into(),
                command: "/bin/fs".into(),
                environment: vec!["OPERATOR".into()],
                ..ToolSourceAttachment::default()
            },
        )
        .unwrap();
    Hub::new(registry)
}

fn refused_feature(result: Result<String, HubError>) -> (String, &'static str, String) {
    match result {
        Err(HubError::Adapter(Error::UnsupportedControl(control))) => {
            (control.feature, control.reason, control.field)
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn an_open_electing_nothing_passes_both_gates_without_a_revision() {
    let hub = hub(&[]);
    let plain = SessionOpenRequest::default();
    assert_eq!(
        block_on(election_gate(&hub, "adv", "", &plain)).unwrap(),
        ""
    );
    assert_eq!(
        block_on(attachment_gate(&hub, "adv", "", &plain)).unwrap(),
        ""
    );
}

#[test]
fn the_election_gate_refuses_an_unadvertised_or_unopted_degraded_feature() {
    let reopen = SessionOpenRequest {
        reopen: true,
        ..SessionOpenRequest::default()
    };
    let (feature, reason, _) =
        refused_feature(block_on(election_gate(&hub(&[]), "adv", "", &reopen)));
    assert_eq!(
        (feature.as_str(), reason),
        (FEATURE_OPEN_REOPEN, CONTROL_UNADVERTISED)
    );

    let degraded = hub(&[(FEATURE_OPEN_SUBSCRIBE, support(SupportLevel::DEGRADED, &[]))]);
    let subscribe = SessionOpenRequest {
        subscribe: true,
        ..SessionOpenRequest::default()
    };
    assert!(matches!(
        block_on(election_gate(&degraded, "adv", "", &subscribe)),
        Err(HubError::Adapter(Error::DegradedControl(feature))) if feature == FEATURE_OPEN_SUBSCRIBE
    ));
    let opted = SessionOpenRequest {
        allow_degraded_features: vec![FEATURE_OPEN_SUBSCRIBE.into()],
        ..subscribe
    };
    assert_eq!(
        block_on(election_gate(&degraded, "adv", "", &opted)).unwrap(),
        "rev-1"
    );
}

#[test]
fn an_open_setting_needs_the_session_open_mode_and_names_its_field() {
    let policy = SessionOpenRequest {
        compaction_policy: Some(CompactionPolicy::default()),
        ..SessionOpenRequest::default()
    };
    let modeless = hub(&[(
        FEATURE_COMPACTION_POLICY,
        support(SupportLevel::NATIVE, &[]),
    )]);
    let (feature, _, field) =
        refused_feature(block_on(election_gate(&modeless, "adv", "", &policy)));
    assert_eq!(
        (feature.as_str(), field.as_str()),
        (FEATURE_COMPACTION_POLICY, "compaction_policy")
    );
    let moded = hub(&[(
        FEATURE_COMPACTION_POLICY,
        support(SupportLevel::NATIVE, &[MODE_SESSION_OPEN]),
    )]);
    assert_eq!(
        block_on(election_gate(&moded, "adv", "", &policy)).unwrap(),
        "rev-1"
    );
}

#[test]
fn a_gate_refuses_a_stale_revision_and_an_unknown_adapter() {
    let reopen = SessionOpenRequest {
        reopen: true,
        ..SessionOpenRequest::default()
    };
    let hub = hub(&[(FEATURE_OPEN_REOPEN, support(SupportLevel::NATIVE, &[]))]);
    assert!(matches!(
        block_on(election_gate(&hub, "adv", "rev-0", &reopen)),
        Err(HubError::StaleRevision { expected, current }) if expected == "rev-1" && current == "rev-0"
    ));
    assert!(matches!(
        block_on(election_gate(&hub, "ghost", "", &reopen)),
        Err(HubError::UnknownAdapter(_))
    ));
}

#[test]
fn the_attachment_gate_needs_the_feature_disclosed_for_session_open() {
    let attaching = SessionOpenRequest {
        tool_sources: vec![ToolSourceAttachment {
            id: "fs".into(),
            ..ToolSourceAttachment::default()
        }],
        ..SessionOpenRequest::default()
    };
    let (feature, _, _) =
        refused_feature(block_on(attachment_gate(&hub(&[]), "adv", "", &attaching)));
    assert_eq!(feature, FEATURE_TOOL_SOURCES_ATTACH);
    let open = hub(&[(
        FEATURE_TOOL_SOURCES_ATTACH,
        support(SupportLevel::NATIVE, &[MODE_SESSION_OPEN]),
    )]);
    assert_eq!(
        block_on(attachment_gate(&open, "adv", "", &attaching)).unwrap(),
        "rev-1"
    );
}

#[test]
fn attachments_resolve_to_the_operators_sources_and_refuse_what_the_wire_may_not_say() {
    let hub = hub(&[]);
    let by_id = |id: &str| ToolSourceAttachment {
        id: id.into(),
        ..ToolSourceAttachment::default()
    };
    let resolved = resolve_attachments(
        hub.registry(),
        &[
            ToolSourceAttachment {
                environment: vec!["OPERATOR".into(), "CALLER".into()],
                ..by_id("fs")
            },
            ToolSourceAttachment {
                kind: TOOL_SOURCE_REMOTE.into(),
                ..by_id("remote")
            },
        ],
    )
    .unwrap();
    assert_eq!(resolved[0].command, "/bin/fs");
    assert_eq!(resolved[0].environment, ["OPERATOR", "CALLER"]);
    assert_eq!(resolved[1].id, "remote");

    for (attachment, reason) in [
        (
            ToolSourceAttachment {
                command: "/bin/evil".into(),
                ..by_id("fs")
            },
            "does not accept a command or arguments from the wire",
        ),
        (
            ToolSourceAttachment {
                environment: vec!["TOKEN=x".into()],
                ..by_id("fs")
            },
            "only the bare NAME allowlist form",
        ),
        (
            ToolSourceAttachment {
                kind: TOOL_SOURCE_PROCESS.into(),
                ..by_id("unknown")
            },
            "no tool source of that id is configured",
        ),
        (
            ToolSourceAttachment {
                endpoint: "elsewhere".into(),
                ..by_id("fs")
            },
            "does not accept endpoint from the wire",
        ),
    ] {
        let refusal = resolve_attachments(hub.registry(), &[attachment]).unwrap_err();
        assert!(refusal.reason.contains(reason), "{}", refusal.reason);
        assert!(refusal.to_string().starts_with("tool source \""));
    }
}

#[test]
fn a_gate_refuses_an_adapter_whose_descriptor_carries_no_revision() {
    let reopen = SessionOpenRequest {
        reopen: true,
        ..SessionOpenRequest::default()
    };
    let bare = hub_at(
        &[(FEATURE_OPEN_REOPEN, support(SupportLevel::NATIVE, &[]))],
        "",
    );
    match block_on(election_gate(&bare, "adv", "", &reopen)) {
        Err(HubError::Adapter(Error::Harness(message))) => {
            assert_eq!(message, "adapter descriptor carries no capability revision");
        }
        other => panic!("{other:?}"),
    }
}

#[test]
fn the_attachment_gate_refuses_a_degraded_attach_without_the_opt_in() {
    let attaching = SessionOpenRequest {
        tool_sources: vec![ToolSourceAttachment {
            id: "fs".into(),
            ..ToolSourceAttachment::default()
        }],
        ..SessionOpenRequest::default()
    };
    let degraded = hub(&[(
        FEATURE_TOOL_SOURCES_ATTACH,
        support(SupportLevel::DEGRADED, &[MODE_SESSION_OPEN]),
    )]);
    assert!(matches!(
        block_on(attachment_gate(&degraded, "adv", "", &attaching)),
        Err(HubError::Adapter(Error::DegradedControl(feature))) if feature == FEATURE_TOOL_SOURCES_ATTACH
    ));
    let opted = SessionOpenRequest {
        allow_degraded_features: vec![FEATURE_TOOL_SOURCES_ATTACH.into()],
        ..attaching
    };
    assert_eq!(
        block_on(attachment_gate(&degraded, "adv", "", &opted)).unwrap(),
        "rev-1"
    );
}
