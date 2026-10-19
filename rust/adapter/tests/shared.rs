use oap_adapter::Error;
use oap_adapter::*;
use oap_protocol::*;

fn question(id: &str, kind: InputQuestionKind, options: &[&str]) -> InputQuestion {
    InputQuestion {
        id: id.into(),
        kind,
        options: options
            .iter()
            .map(|id| InputOption {
                id: id.to_string(),
                ..InputOption::default()
            })
            .collect(),
        ..InputQuestion::default()
    }
}

fn answer(question: &str, text: &str, selected: &[&str]) -> InputAnswer {
    InputAnswer {
        question_id: question.into(),
        text: text.into(),
        selected_option_ids: selected.iter().map(|id| id.to_string()).collect(),
    }
}

#[test]
fn an_answer_must_fit_its_question_kind_and_options() {
    let text = question("note", InputQuestionKind::TEXT, &[]);
    let single = question("pick", InputQuestionKind::SINGLE_CHOICE, &["a", "b"]);
    let multi = question("many", InputQuestionKind::MULTI_CHOICE, &["x", "y"]);
    let bogus = question("q", InputQuestionKind::from("bogus"), &[]);
    let cases = [
        ("text ok", &text, answer("note", "hello", &[]), true),
        ("text empty", &text, answer("note", "", &[]), false),
        (
            "text with selections",
            &text,
            answer("note", "hello", &["a"]),
            false,
        ),
        (
            "text foreign question",
            &text,
            answer("other", "hello", &[]),
            false,
        ),
        ("single ok", &single, answer("pick", "", &["a"]), true),
        ("single none", &single, answer("pick", "", &[]), false),
        (
            "single two",
            &single,
            answer("pick", "", &["a", "b"]),
            false,
        ),
        (
            "single unoffered",
            &single,
            answer("pick", "", &["c"]),
            false,
        ),
        ("single empty id", &single, answer("pick", "", &[""]), false),
        (
            "single with text",
            &single,
            answer("pick", "a", &["a"]),
            false,
        ),
        ("single text only", &single, answer("pick", "a", &[]), false),
        ("multi ok one", &multi, answer("many", "", &["x"]), true),
        (
            "multi ok two",
            &multi,
            answer("many", "", &["x", "y"]),
            true,
        ),
        ("multi none", &multi, answer("many", "", &[]), false),
        (
            "multi duplicate",
            &multi,
            answer("many", "", &["x", "x"]),
            false,
        ),
        (
            "multi unoffered",
            &multi,
            answer("many", "", &["x", "z"]),
            false,
        ),
        (
            "multi with text",
            &multi,
            answer("many", "x", &["x"]),
            false,
        ),
        ("unknown kind", &bogus, answer("q", "x", &[]), false),
    ];
    for (name, question, answer, valid) in cases {
        let result = validate_input_answer(question, &answer);
        assert_eq!(result.is_ok(), valid, "{name}");
        if !valid {
            assert_eq!(result, Err(Error::InvalidResolution), "{name}");
        }
    }
}

#[test]
fn an_answer_set_is_indexed_only_when_every_answer_fits_one_offered_question_once() {
    let questions = [
        question("pick", InputQuestionKind::SINGLE_CHOICE, &["a"]),
        question("note", InputQuestionKind::TEXT, &[]),
    ];
    let answers = [answer("pick", "", &["a"]), answer("note", "hi", &[])];
    let indexed = index_input_answers(&questions, &answers).unwrap();
    assert_eq!(indexed.len(), 2);
    for (name, answers) in [
        (
            "duplicate answer",
            vec![answer("note", "a", &[]), answer("note", "b", &[])],
        ),
        ("foreign question", vec![answer("other", "a", &[])]),
        ("malformed answer", vec![answer("pick", "a", &["a"])]),
        ("unoffered option", vec![answer("pick", "", &["z"])]),
    ] {
        assert_eq!(
            index_input_answers(&questions, &answers).err(),
            Some(Error::InvalidResolution),
            "{name}"
        );
    }
}

fn submit(delivery: RequestedDeliveryMode) -> MessageSubmitRequest {
    MessageSubmitRequest {
        delivery,
        ..MessageSubmitRequest::default()
    }
}

fn refused_feature(result: Result<(), Error>) -> Option<String> {
    match result {
        Err(Error::UnsupportedControl(control)) if control.reason == CONTROL_UNADVERTISED => {
            Some(control.feature)
        }
        _ => None,
    }
}

#[test]
fn a_non_auto_delivery_is_judged_under_its_own_key_after_the_run_controls_and_auto_is_never_gated()
{
    for (delivery, key) in [
        (RequestedDeliveryMode::QUEUE, FEATURE_DELIVERY_QUEUE),
        (RequestedDeliveryMode::STEER, FEATURE_DELIVERY_STEER),
        (RequestedDeliveryMode::BTW, FEATURE_DELIVERY_BTW),
    ] {
        assert_eq!(
            refused_feature(refuse_unadvertised_controls(&submit(delivery.clone()), &[]))
                .as_deref(),
            Some(key)
        );
        assert!(refuse_unadvertised_controls(&submit(delivery), &[key]).is_ok());
    }
    let instructed = MessageSubmitRequest {
        instructions: Some("i".into()),
        ..submit(RequestedDeliveryMode::QUEUE)
    };
    assert_eq!(
        refused_feature(refuse_unadvertised_controls(&instructed, &[])).as_deref(),
        Some(FEATURE_INSTRUCTIONS)
    );
    for delivery in [
        RequestedDeliveryMode::default(),
        RequestedDeliveryMode::AUTO,
    ] {
        assert!(refuse_unadvertised_controls(&submit(delivery), &[]).is_ok());
    }
}

fn support(level: SupportLevel, modes: &[&str]) -> FeatureSupport {
    FeatureSupport {
        level,
        modes: modes.iter().map(|m| m.to_string()).collect(),
        ..FeatureSupport::default()
    }
}

#[test]
fn tool_sources_need_a_session_open_disclosure_and_provided_tools_need_any_offer() {
    let with_sources = OpenRequest {
        tool_sources: vec![ToolSourceAttachment::default()],
        ..OpenRequest::default()
    };
    assert!(refuse_unadvertised_tool_sources(&OpenRequest::default(), &[]).is_ok());
    assert_eq!(
        refused_feature(refuse_unadvertised_tool_sources(
            &with_sources,
            &[support(SupportLevel::NATIVE, &[])]
        ))
        .as_deref(),
        Some(FEATURE_TOOL_SOURCES_ATTACH)
    );
    assert!(refuse_unadvertised_tool_sources(
        &with_sources,
        &[support(SupportLevel::NATIVE, &[MODE_SESSION_OPEN])]
    )
    .is_ok());
    assert!(refused_feature(refuse_unadvertised_tool_sources(
        &with_sources,
        &[support(SupportLevel::UNAVAILABLE, &[MODE_SESSION_OPEN])]
    ))
    .is_some());

    let with_tools = OpenRequest {
        tools: vec![ToolDefinition::default()],
        ..OpenRequest::default()
    };
    assert_eq!(
        refused_feature(refuse_unadvertised_tools(
            &with_tools,
            &[support(SupportLevel::UNAVAILABLE, &[])]
        ))
        .as_deref(),
        Some(FEATURE_TOOLS_PROVIDE)
    );
    assert!(
        refuse_unadvertised_tools(&with_tools, &[support(SupportLevel::EMULATED, &[])]).is_ok()
    );
}

#[test]
fn a_setting_needs_its_mode_disclosed_and_a_degraded_one_needs_an_opt_in() {
    let descriptor = |level: SupportLevel, mode: &str| CapabilityDescriptor {
        features: [(
            FEATURE_SESSION_REASONING.to_owned(),
            support(level, &[mode]),
        )]
        .into(),
        ..CapabilityDescriptor::default()
    };
    let open = OpenRequest {
        reasoning_level: ReasoningLevel::HIGH,
        ..OpenRequest::default()
    };
    assert!(refuse_unadvertised_settings(
        &open,
        &descriptor(SupportLevel::NATIVE, MODE_SESSION_OPEN)
    )
    .is_ok());
    match refuse_unadvertised_settings(&open, &descriptor(SupportLevel::NATIVE, MODE_SESSION_LIVE))
    {
        Err(Error::UnsupportedControl(control)) => assert_eq!(control.field, "reasoning_level"),
        other => panic!("{other:?}"),
    }
    let degraded = descriptor(SupportLevel::DEGRADED, MODE_SESSION_OPEN);
    assert_eq!(
        refuse_unadvertised_settings(&open, &degraded),
        Err(Error::DegradedControl(FEATURE_SESSION_REASONING.into()))
    );
    let opted = OpenRequest {
        allow_degraded_features: vec![FEATURE_SESSION_REASONING.into()],
        ..open
    };
    assert!(refuse_unadvertised_settings(&opted, &degraded).is_ok());
    let live = SessionSettingsUpdateRequest {
        reasoning_level: ReasoningLevel::LOW,
        ..SessionSettingsUpdateRequest::default()
    };
    assert!(refuse_unadvertised_live_settings(
        &live,
        &descriptor(SupportLevel::NATIVE, MODE_SESSION_LIVE)
    )
    .is_ok());
    assert!(refuse_unadvertised_live_settings(
        &live,
        &descriptor(SupportLevel::NATIVE, MODE_SESSION_OPEN)
    )
    .is_err());
    assert!(Error::DegradedControl(String::new()).is_unsupported_input());
}

#[test]
fn a_repeated_environment_name_is_named_whatever_its_values() {
    let entries = |list: &[&str]| list.iter().map(|s| s.to_string()).collect::<Vec<_>>();
    assert_eq!(
        duplicate_environment_name(&entries(&["A=1", "B=2", "A=3"])),
        Some("A")
    );
    assert_eq!(duplicate_environment_name(&entries(&["A=1", "B"])), None);
    assert_eq!(
        duplicate_environment_name(&entries(&["A", "A=1"])),
        Some("A")
    );
}
