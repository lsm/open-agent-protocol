mod support;

use oap_adapter::memory::{model_catalog, MODEL_PRIMARY};
use support::*;

fn switch(
    session: &MemorySession,
    model: &str,
) -> Result<(SessionModelSwitchResponse, SessionState), Error> {
    session.switch_model(&SessionModelSwitchRequest {
        session_id: "session-1".into(),
        model_id: model.into(),
        ..SessionModelSwitchRequest::default()
    })
}

#[test]
fn a_switch_moves_the_session_default_without_rewriting_an_admitted_run() {
    let session = session(64);
    let (first, state) = switch(&session, MODEL_PRIMARY).unwrap();
    assert_eq!(
        (
            first.previous_model_id.as_str(),
            state.current_model_id.as_str()
        ),
        ("", MODEL_PRIMARY)
    );
    assert_eq!(
        switch(&session, MODEL_PRIMARY).unwrap().0.previous_model_id,
        MODEL_PRIMARY
    );
    assert_eq!(
        switch(&session, "missing").err(),
        Some(Error::ModelNotFound("missing".into()))
    );
    let (admission, mut stream) = started(&session);
    assert_eq!(admission.model_id, MODEL_PRIMARY);
    let initial = drain(&mut stream);
    let (second, state) = switch(&session, MODEL_SECONDARY).unwrap();
    assert_eq!(
        (
            second.previous_model_id.as_str(),
            state.current_model_id.as_str()
        ),
        (MODEL_PRIMARY, MODEL_SECONDARY)
    );
    let rest = resolve_gates(
        &session,
        admission.run_id.as_ref().unwrap(),
        &mut stream,
        &initial,
    );
    let completed: RunCompletedPayload = of_type(&rest, EnvelopeType::RUN_COMPLETED)
        .decode_payload()
        .unwrap();
    assert_eq!(completed.model_id, MODEL_PRIMARY);
    let foreign = SessionModelSwitchRequest {
        session_id: "elsewhere".into(),
        model_id: MODEL_PRIMARY.into(),
        ..SessionModelSwitchRequest::default()
    };
    assert_eq!(
        session.switch_model(&foreign).err(),
        Some(Error::InvalidSubmission)
    );
}

#[test]
fn a_queued_run_starts_on_the_model_switched_to_while_it_waited() {
    let session = session(64);
    let (first, mut first_stream) = started(&session);
    let events = drain(&mut first_stream);
    let queued = MessageSubmitRequest {
        delivery: RequestedDeliveryMode::QUEUE,
        ..plain()
    };
    let (reservation, mut reserved) = submit(&session, queued);
    assert_eq!(reservation.admission, Admission::QUEUED);
    switch(&session, MODEL_SECONDARY).unwrap();
    resolve_gates(
        &session,
        first.run_id.as_ref().unwrap(),
        &mut first_stream,
        &events,
    );
    let promoted = drain(&mut reserved);
    let started: RunStartedPayload = of_type(&promoted, EnvelopeType::RUN_STARTED)
        .decode_payload()
        .unwrap();
    assert_eq!(started.model_id, MODEL_SECONDARY);
}

#[test]
fn a_closed_session_reopens_on_the_model_it_was_switched_to() {
    let memory = memory(64);
    let session = open_with(&memory, "user");
    switch(&session, MODEL_SECONDARY).unwrap();
    session.close().unwrap();
    let reopen = OpenRequest {
        session_id: "session-1".into(),
        reopen: true,
        ..user_open()
    };
    assert_eq!(
        memory
            .open_session(&reopen)
            .unwrap()
            .snapshot()
            .current_model_id,
        MODEL_SECONDARY
    );
}

#[test]
fn the_model_catalog_is_the_fixed_pair_the_model_gate_admits() {
    let session = session(64);
    switch(&session, MODEL_SECONDARY).unwrap();
    let catalog = session.models(&ModelsRequest::default()).unwrap();
    assert_eq!(catalog.revision, CAPABILITY_REVISION);
    assert_eq!(catalog.models.current_model_id, MODEL_SECONDARY);
    let ids: Vec<&str> = catalog
        .models
        .models
        .iter()
        .map(|model| model.id.as_str())
        .collect();
    assert_eq!(ids, [MODEL_PRIMARY, MODEL_SECONDARY]);
    let defaults: Vec<bool> = catalog
        .models
        .models
        .iter()
        .map(|model| model.default)
        .collect();
    assert_eq!(defaults, [true, false]);
    assert_eq!(catalog.models.models.len(), model_catalog().len());
    assert_eq!(catalog.models.providers[0].id, "reference");
    let foreign = ModelsRequest {
        session_id: "elsewhere".into(),
        ..ModelsRequest::default()
    };
    assert_eq!(
        session.models(&foreign).err(),
        Some(Error::InvalidSubmission)
    );
    session.close().unwrap();
    assert_eq!(
        session.models(&ModelsRequest::default()).err(),
        Some(Error::SessionClosed)
    );
}

fn update(
    session: &MemorySession,
    request: SessionSettingsUpdateRequest,
) -> Result<(SessionSettingsUpdateResponse, SessionState), Error> {
    session.update_settings(&SessionSettingsUpdateRequest {
        session_id: "session-1".into(),
        ..request
    })
}

#[test]
fn a_settings_update_moves_the_policy_and_threshold_or_is_refused_whole() {
    let session = session(64);
    let before = session.compaction_threshold();
    let tokens = CompactionPolicy {
        kind: CompactionKind::TOKENS,
        tokens: 1,
        ..CompactionPolicy::default()
    };
    let asked = SessionSettingsUpdateRequest {
        compaction_policy: Some(tokens.clone()),
        ..SessionSettingsUpdateRequest::default()
    };
    let (response, state) = update(&session, asked).unwrap();
    assert_eq!(response.compaction_policy.as_ref(), Some(&tokens));
    assert_eq!(
        response
            .previous_compaction_policy
            .map(|policy| policy.kind),
        Some(CompactionKind::AUTO)
    );
    assert_eq!(state.compaction_policy.as_ref(), Some(&tokens));
    assert_ne!(session.compaction_threshold(), before);
    assert_eq!(session.compaction_threshold(), 1);
    let reasoning = SessionSettingsUpdateRequest {
        reasoning_level: ReasoningLevel::HIGH,
        compaction_policy: Some(CompactionPolicy {
            kind: CompactionKind::OFF,
            ..CompactionPolicy::default()
        }),
        ..SessionSettingsUpdateRequest::default()
    };
    let control = refusal(update(&session, reasoning).err().unwrap());
    assert_eq!(
        (control.feature.as_str(), control.field.as_str()),
        (FEATURE_SESSION_REASONING, "reasoning_level")
    );
    let zero = SessionSettingsUpdateRequest {
        compaction_policy: Some(CompactionPolicy {
            kind: CompactionKind::TOKENS,
            ..CompactionPolicy::default()
        }),
        ..SessionSettingsUpdateRequest::default()
    };
    assert_eq!(
        refusal(update(&session, zero).err().unwrap()).reason,
        CONTROL_UNSATISFIABLE
    );
    assert_eq!(session.snapshot().compaction_policy, Some(tokens));
    assert_eq!(session.compaction_threshold(), 1);
}

#[test]
fn the_tool_catalog_adds_the_session_s_sources_and_provided_tools_only_when_scoped() {
    let memory = memory(64);
    let provided = ToolDefinition {
        name: "lookup".into(),
        execution_owner: "user".into(),
        source: "reference-native".into(),
        input_schema: raw(
            r#"{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"}"#,
        ),
        ..ToolDefinition::default()
    };
    let open = OpenRequest {
        session_id: "session-1".into(),
        tools: vec![provided],
        ..user_open()
    };
    let session = memory.open_session(&open).unwrap();
    let names = |catalog: ToolCatalog| {
        catalog
            .tools
            .tools
            .into_iter()
            .map(|tool| tool.name)
            .collect::<Vec<_>>()
    };
    let unscoped = session.tools(&ToolsListRequest::default()).unwrap();
    assert_eq!(unscoped.revision, CAPABILITY_REVISION);
    assert_eq!(
        unscoped.tools.sources,
        oap_adapter::memory::declared_sources()
    );
    assert_eq!(names(unscoped), ["scripted_tool"]);
    let scoped = ToolsListRequest {
        session_id: Some("session-1".into()),
        ..ToolsListRequest::default()
    };
    assert_eq!(
        names(session.tools(&scoped).unwrap()),
        ["scripted_tool", "lookup"]
    );
    let foreign = ToolsListRequest {
        session_id: Some("elsewhere".into()),
        ..ToolsListRequest::default()
    };
    assert_eq!(session.tools(&foreign).err(), Some(Error::RunNotFound));
}

#[test]
fn the_session_trait_offers_its_catalogs_and_controls() {
    let session = session(64);
    let dynamic: &dyn Session = &session;
    assert!(dynamic.model_lister().is_some());
    assert!(dynamic.model_switcher().is_some());
    assert!(dynamic.settings_updater().is_some());
    assert!(dynamic.tool_lister().is_some());
    let catalog = block_on(
        dynamic
            .model_lister()
            .unwrap()
            .models(ModelsRequest::default()),
    )
    .unwrap();
    assert_eq!(catalog.models.models.len(), 2);
}
