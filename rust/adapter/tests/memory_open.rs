use futures::executor::block_on;
use oap_adapter::memory::{history_tokens, Config, Memory, CAPABILITY_REVISION, MODEL_SECONDARY};
use oap_adapter::{Admitter, Error, OpenRequest, CONTROL_UNSATISFIABLE};
use oap_protocol::*;

fn opener(session: &str) -> OpenRequest {
    OpenRequest {
        session_id: session.into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

fn refusal(result: Result<(), Error>) -> Option<(String, String, String, String)> {
    match result {
        Err(Error::UnsupportedControl(control)) => Some((
            control.feature,
            control.reason.to_owned(),
            control.source,
            control.tool,
        )),
        _ => None,
    }
}

#[test]
fn the_memory_admits_what_it_would_open_and_refuses_what_open_would_refuse() {
    let memory = Memory::new(Config::default());
    let share = CompactionPolicy {
        kind: CompactionKind::SHARE,
        ..CompactionPolicy::default()
    };
    let cases = [
        (
            "a plain open",
            OpenRequest {
                session_id: "s".into(),
                ..OpenRequest::default()
            },
            None,
        ),
        (
            "a share of zero",
            OpenRequest {
                compaction_policy: Some(share),
                ..OpenRequest::default()
            },
            Some(FEATURE_COMPACTION_POLICY),
        ),
        (
            "an attachment with no kind",
            OpenRequest {
                tool_sources: vec![ToolSourceAttachment {
                    id: "x".into(),
                    ..ToolSourceAttachment::default()
                }],
                ..OpenRequest::default()
            },
            Some(FEATURE_TOOL_SOURCES_ATTACH),
        ),
        (
            "a provided tool with no name",
            OpenRequest {
                tools: vec![ToolDefinition::default()],
                ..OpenRequest::default()
            },
            Some(FEATURE_TOOLS_PROVIDE),
        ),
    ];
    for (name, request, feature) in cases {
        let admitted = block_on(memory.admit(&request));
        let opened = memory
            .open_session(&OpenRequest {
                participant: Participant {
                    id: "p".into(),
                    ..Participant::default()
                },
                ..request
            })
            .map(|_| ());
        match feature {
            None => assert!(
                admitted.is_ok() && opened.is_ok(),
                "{name}: {admitted:?} {opened:?}"
            ),
            Some(feature) => {
                assert_eq!(
                    refusal(admitted.clone()).map(|r| r.0).as_deref(),
                    Some(feature),
                    "{name}"
                );
                assert_eq!(
                    opened.err().map(|e| e.to_string()),
                    admitted.err().map(|e| e.to_string()),
                    "{name}"
                );
            }
        }
    }
}

#[test]
fn an_attachment_refusal_names_the_source_it_refuses() {
    let attachment = |id: &str, kind: &str| ToolSourceAttachment {
        id: id.into(),
        kind: kind.into(),
        ..ToolSourceAttachment::default()
    };
    let cases = [
        (
            "an id the descriptor already declares",
            vec![attachment("reference-mcp", TOOL_SOURCE_PROCESS)],
            "reference-mcp",
        ),
        (
            "two attachments sharing one id",
            vec![
                attachment("twice", TOOL_SOURCE_PROCESS),
                attachment("twice", TOOL_SOURCE_LOCAL),
            ],
            "twice",
        ),
        (
            "a transport the descriptor does not disclose",
            vec![attachment("far-away", TOOL_SOURCE_REMOTE)],
            "far-away",
        ),
        (
            "more sources than the disclosed ceiling",
            vec![
                attachment("one", TOOL_SOURCE_PROCESS),
                attachment("two", TOOL_SOURCE_PROCESS),
                attachment("three", TOOL_SOURCE_PROCESS),
            ],
            "three",
        ),
        (
            "one variable named twice",
            vec![ToolSourceAttachment {
                environment: vec!["A=1".into(), "A=2".into()],
                ..attachment("env", TOOL_SOURCE_PROCESS)
            }],
            "env",
        ),
    ];
    let memory = Memory::new(Config::default());
    for (name, sources, source) in cases {
        let result = memory
            .open_session(&OpenRequest {
                tool_sources: sources,
                ..opener("refused")
            })
            .map(|_| ());
        let (feature, reason, named, _) =
            refusal(result).unwrap_or_else(|| panic!("{name}: admitted"));
        assert_eq!(
            (feature.as_str(), reason.as_str(), named.as_str()),
            (FEATURE_TOOL_SOURCES_ATTACH, CONTROL_UNSATISFIABLE, source),
            "{name}"
        );
    }
}

#[test]
fn a_provided_tool_is_refused_for_each_disclosed_limit_and_admitted_within_them() {
    let tool = |name: &str, source: &str| ToolDefinition {
        name: name.into(),
        execution_owner: "user".into(),
        source: source.into(),
        ..ToolDefinition::default()
    };
    let with_schema = |schema: &str| ToolDefinition {
        input_schema: Some(serde_json::value::RawValue::from_string(schema.into()).unwrap()),
        ..tool("schemed", "reference-native")
    };
    let memory = Memory::new(Config::default());
    let cases = [
        (
            "someone else's",
            ToolDefinition {
                execution_owner: "other".into(),
                ..tool("mine", "reference-native")
            },
            Some("mine"),
        ),
        ("no source", tool("nosource", ""), Some("nosource")),
        ("an unknown source", tool("lost", "nowhere"), Some("lost")),
        (
            "the scripted name",
            tool("scripted_tool", "reference-native"),
            Some("scripted_tool"),
        ),
        (
            "outside the pattern",
            tool("Bad-Name", "reference-native"),
            Some("Bad-Name"),
        ),
        (
            "another dialect",
            with_schema(r#"{"$schema":"http://json-schema.org/draft-07/schema#"}"#),
            Some("schemed"),
        ),
        (
            "the disclosed dialect",
            with_schema(r#"{"$schema":"https://json-schema.org/draft/2020-12/schema"}"#),
            None,
        ),
        ("no dialect", with_schema(r#"{"type":"object"}"#), None),
        (
            "a null dialect, which Go reads as none",
            with_schema(r#"{"$schema":null}"#),
            None,
        ),
        (
            "a dialect that is not a string",
            with_schema(r#"{"$schema":7}"#),
            Some("schemed"),
        ),
    ];
    for (name, provided, refused) in cases {
        let result = memory
            .open_session(&OpenRequest {
                tools: vec![provided],
                ..opener("p")
            })
            .map(|session| session.provided_tools().len());
        match refused {
            None => assert_eq!(result.ok(), Some(1), "{name}"),
            Some(tool) => assert_eq!(
                refusal(result.map(|_| ())).map(|r| r.3).as_deref(),
                Some(tool),
                "{name}"
            ),
        }
    }
    let three = vec![
        tool("a", "reference-native"),
        tool("b", "reference-native"),
        tool("c", "reference-native"),
    ];
    let result = memory
        .open_session(&OpenRequest {
            tools: three,
            ..opener("p")
        })
        .map(|_| ());
    assert_eq!(refusal(result).map(|r| r.3).as_deref(), Some("c"));
}

#[test]
fn a_compaction_policy_sets_the_threshold_and_the_state_reports_its_effective_form() {
    let memory = Memory::new(Config::default());
    let policy = |kind: CompactionKind, share: i64, tokens: i64| CompactionPolicy {
        kind,
        share_percent: share,
        tokens,
    };
    for (given, threshold) in [
        (None, 6553),
        (Some(policy(CompactionKind::AUTO, 0, 0)), 6553),
        (Some(policy(CompactionKind::SHARE, 50, 0)), 4096),
        (Some(policy(CompactionKind::TOKENS, 0, 1000)), 1000),
        (Some(policy(CompactionKind::OFF, 0, 0)), u64::MAX),
    ] {
        let session = memory
            .open_session(&OpenRequest {
                compaction_policy: given.clone(),
                ..opener("c")
            })
            .unwrap();
        assert_eq!(session.compaction_threshold(), threshold, "{given:?}");
    }
    let session = memory.open_session(&opener("default")).unwrap();
    assert_eq!(
        session.snapshot().compaction_policy.unwrap().kind,
        CompactionKind::AUTO
    );
    let stray = memory
        .open_session(&OpenRequest {
            compaction_policy: Some(policy(CompactionKind::SHARE, 30, 999)),
            ..opener("s")
        })
        .unwrap();
    assert_eq!(
        stray.snapshot().compaction_policy,
        Some(policy(CompactionKind::SHARE, 30, 0))
    );
    assert_eq!(
        history_tokens(&[Message {
            content: MessageContent::from_text("12345"),
            ..Message::default()
        }]),
        2
    );
}

#[test]
fn a_closed_session_reopens_on_its_own_model_and_policy_and_only_once() {
    let memory = Memory::new(Config::default());
    let tokens = CompactionPolicy {
        kind: CompactionKind::TOKENS,
        tokens: 500,
        ..CompactionPolicy::default()
    };
    memory.keep("kept", MODEL_SECONDARY, Some(tokens.clone()));
    let reopened = memory
        .open_session(&OpenRequest {
            reopen: true,
            ..opener("kept")
        })
        .unwrap();
    let state = reopened.snapshot();
    assert!(state
        .recovery
        .as_ref()
        .is_some_and(|recovery| recovery.recovered));
    assert_eq!(state.current_model_id, MODEL_SECONDARY);
    assert_eq!(state.compaction_policy, Some(tokens));
    assert_eq!(reopened.compaction_threshold(), 500);
    for session in ["kept", "never"] {
        let again = memory.open_session(&OpenRequest {
            reopen: true,
            ..opener(session)
        });
        assert!(matches!(again, Err(Error::UnknownSession(_))), "{session}");
    }
    assert!(matches!(
        memory.open_session(&OpenRequest::default()),
        Err(Error::InvalidParticipant)
    ));
}

#[test]
fn the_descriptor_advertises_the_reference_revision_limits_and_catalog() {
    let descriptor = Memory::new(Config {
        journal_capacity: 8,
        ..Config::default()
    })
    .describe();
    assert_eq!(descriptor.capability_revision, CAPABILITY_REVISION);
    assert_eq!(descriptor.journal.capacity, 8);
    let capabilities = descriptor.capabilities;
    assert_eq!(
        capabilities.limits.unwrap().max_queued_runs_per_session,
        Some(1)
    );
    assert_eq!(capabilities.tools[0].name, "scripted_tool");
    assert_eq!(capabilities.sources.len(), 2);
    let attach = &capabilities.features[FEATURE_TOOL_SOURCES_ATTACH];
    assert_eq!(attach.max_sources(), Some(2));
    assert_eq!(
        attach.transports(),
        Some(vec![
            TOOL_SOURCE_PROCESS.to_owned(),
            TOOL_SOURCE_LOCAL.to_owned()
        ])
    );
    assert_eq!(
        capabilities.features[FEATURE_TOOLS_PROVIDE].max_tools(),
        Some(2)
    );
    assert_eq!(capabilities.features.len(), 32);
}
