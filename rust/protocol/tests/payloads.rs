use std::path::PathBuf;

use oap_protocol::*;
use serde::de::DeserializeOwned;
use serde::Serialize;
use serde_json::Value;

fn fixtures() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../../fixtures")
}

fn round_trip<T: DeserializeOwned + Serialize>(payload: &Value) -> Value {
    let typed: T = serde_json::from_value(payload.clone()).unwrap();
    serde_json::to_value(typed).unwrap()
}

fn as_go_reencodes(payload: &Value) -> Value {
    let Value::Object(members) = payload else {
        return payload.clone();
    };
    Value::Object(
        members
            .iter()
            .filter(|(key, value)| {
                !key.contains('.')
                    && !(*key == "features" && value.as_object().is_some_and(|map| map.is_empty()))
            })
            .map(|(key, value)| (key.clone(), value.clone()))
            .collect(),
    )
}

fn reencoded(envelope_type: &str, payload: &Value) -> Option<Value> {
    Some(match envelope_type {
        "run.cancel.request" => round_trip::<RunCancelRequest>(payload),
        "run.cancel.response" => round_trip::<RunCancelResponse>(payload),
        "run.started" => round_trip::<RunStartedPayload>(payload),
        "run.status.updated" => round_trip::<RunStatusUpdatedPayload>(payload),
        "content.delta" => round_trip::<ContentDeltaPayload>(payload),
        "content.completed" => round_trip::<ContentCompletedPayload>(payload),
        "run.completed" => round_trip::<RunCompletedPayload>(payload),
        "run.failed" => round_trip::<RunFailedPayload>(payload),
        "run.cancelled" => round_trip::<RunCancelledPayload>(payload),
        "action.tools.list.request" => round_trip::<ToolsListRequest>(payload),
        "action.tools.list.response" => round_trip::<ToolsListResponse>(payload),
        "action.call.requested"
        | "action.call.started"
        | "action.call.progress"
        | "action.call.completed"
        | "action.call.failed"
        | "action.call.cancelled" => round_trip::<ActionCallPayload>(payload),
        "action.call.resolve.request" => round_trip::<ActionCallResolveRequest>(payload),
        "action.call.resolve.response" => round_trip::<ActionCallResolveResponse>(payload),
        "action.permission.requested" => round_trip::<PermissionRequestedPayload>(payload),
        "action.permission.resolve.request" => round_trip::<PermissionResolveRequest>(payload),
        "action.permission.resolve.response" => round_trip::<PermissionResolveResponse>(payload),
        "action.permission.resolved" => round_trip::<PermissionResolvedPayload>(payload),
        "user.input.requested" => round_trip::<UserInputRequestedPayload>(payload),
        "user.input.resolve.request" => round_trip::<UserInputResolveRequest>(payload),
        "user.input.resolve.response" | "user.input.cancel.response" => {
            round_trip::<UserInputResolveResponse>(payload)
        }
        "user.input.resolved" => round_trip::<UserInputResolvedPayload>(payload),
        "user.input.cancel.request" => round_trip::<UserInputCancelRequest>(payload),
        "auth.providers.request" => round_trip::<AuthProvidersRequest>(payload),
        "auth.providers.response" => round_trip::<AuthProvidersResponse>(payload),
        "auth.login.start.request" => round_trip::<AuthLoginStartRequest>(payload),
        "auth.login.start.response" => round_trip::<AuthLoginStartResponse>(payload),
        "auth.login.event" => round_trip::<AuthLoginEvent>(payload),
        "auth.login.cancel.request" => round_trip::<AuthLoginCancelRequest>(payload),
        "auth.login.cancel.response" => round_trip::<AuthLoginCancelResponse>(payload),
        "auth.login.completed" => round_trip::<AuthLoginCompleted>(payload),
        "run.steer.applied" => round_trip::<RunSteerAppliedPayload>(payload),
        "run.steer.dropped" => round_trip::<RunSteerDroppedPayload>(payload),
        "run.compaction.started" => round_trip::<RunCompactionStartedPayload>(payload),
        "run.compaction.ended" => round_trip::<RunCompactionEndedPayload>(payload),
        "protocol.initialize.request" => round_trip::<InitializeRequest>(payload),
        "protocol.initialize.response" => round_trip::<InitializeResponse>(payload),
        "capabilities.request" => round_trip::<CapabilitiesRequest>(payload),
        "capabilities.response" => round_trip::<CapabilitiesResponse>(payload),
        "capabilities.updated" => round_trip::<CapabilitiesUpdated>(payload),
        "models.request" => round_trip::<ModelsRequest>(payload),
        "models.response" => round_trip::<ModelsResponse>(payload),
        "session.message.submit.request" => round_trip::<MessageSubmitRequest>(payload),
        "session.message.submit.response" => round_trip::<MessageSubmitResponse>(payload),
        "session.compact.request" => round_trip::<SessionCompactRequest>(payload),
        "session.compact.response" => round_trip::<SessionCompactResponse>(payload),
        "transcript.load.request" => round_trip::<TranscriptLoadRequest>(payload),
        "transcript.load.response" => round_trip::<TranscriptLoadResponse>(payload),
        "error.response" => round_trip::<ErrorResponse>(payload),
        _ => return None,
    })
}

#[test]
fn every_run_interaction_auth_and_error_payload_in_the_positive_fixtures_round_trips() {
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(fixtures().join("manifest.json")).unwrap()).unwrap();
    let mut judged = 0;
    let mut differing = Vec::new();
    for entry in manifest["fixtures"].as_array().unwrap() {
        if entry["kind"] != "positive"
            || entry
                .get("profile")
                .is_some_and(|p| p != "agent-control-core")
        {
            continue;
        }
        let path = entry["path"].as_str().unwrap();
        let trace: Value =
            serde_json::from_slice(&std::fs::read(fixtures().join(path)).unwrap()).unwrap();
        let Value::Array(envelopes) = trace else {
            continue;
        };
        for envelope in envelopes {
            let envelope_type = envelope["type"].as_str().unwrap_or_default();
            let Some(back) = reencoded(envelope_type, &envelope["payload"]) else {
                continue;
            };
            judged += 1;
            if back != as_go_reencodes(&envelope["payload"]) {
                differing.push(format!(
                    "{path} {envelope_type}\n  in  {}\n  out {back}",
                    envelope["payload"]
                ));
            }
        }
    }
    assert!(
        differing.is_empty(),
        "{} differ:\n{}",
        differing.len(),
        differing.join("\n")
    );
    assert!(judged >= 1500, "judged only {judged} payloads");
}

#[test]
fn a_part_spells_text_only_for_a_text_part_and_reasoning_only_for_a_reasoning_part() {
    let text = ContentPart {
        part_type: ContentPartType::TEXT,
        ..ContentPart::default()
    };
    assert_eq!(
        serde_json::to_value(&text).unwrap(),
        serde_json::json!({"type": "text", "text": ""})
    );
    let reasoning = ContentPart {
        part_type: ContentPartType::REASONING,
        text: "stray".into(),
        ..ContentPart::default()
    };
    assert_eq!(
        serde_json::to_value(&reasoning).unwrap(),
        serde_json::json!({"type": "reasoning", "reasoning": ""})
    );
}

#[test]
fn a_carry_survives_a_round_trip_and_a_part_without_one_spells_none() {
    for raw in [
        r#"{"type":"reasoning","reasoning":"thinking","carry":"sig-1"}"#,
        r#"{"type":"tool_call","tool_call_id":"t1","name":"search","arguments_json":{"q":"zig"},"carry":"sig-2"}"#,
    ] {
        let part: ContentPart = serde_json::from_str(raw).unwrap();
        assert!(!part.carry.is_empty());
        assert_eq!(
            serde_json::to_value(&part).unwrap(),
            serde_json::from_str::<Value>(raw).unwrap()
        );
    }
    let plain = ContentPart {
        part_type: ContentPartType::TEXT,
        text: "hello".into(),
        ..ContentPart::default()
    };
    assert!(!serde_json::to_string(&plain).unwrap().contains("carry"));
}

#[test]
fn zero_values_go_omits_are_omitted() {
    let recovery = RecoveryMetadata {
        recovered: false,
        ..RecoveryMetadata::default()
    };
    assert_eq!(
        serde_json::to_value(&recovery).unwrap(),
        serde_json::json!({})
    );
    assert_eq!(
        serde_json::to_value(Usage::default()).unwrap(),
        serde_json::json!({})
    );
}

#[test]
fn message_content_reads_as_text_or_parts() {
    assert_eq!(
        MessageContent::from_text("hi").text().as_deref(),
        Some("hi")
    );
    let parts = MessageContent::from_parts(&[ContentPart {
        part_type: ContentPartType::TEXT,
        text: "a".into(),
        ..ContentPart::default()
    }]);
    assert!(parts.text().is_none());
    assert_eq!(parts.parts().unwrap()[0].text, "a");
    assert_eq!(MessageContent::default().raw().get(), "null");
}

#[test]
fn an_unlisted_vocabulary_word_is_kept_rather_than_refused() {
    let resolved: UserInputResolvedPayload =
        serde_json::from_value(serde_json::json!({"status": "deferred"})).unwrap();
    assert_eq!(resolved.status, "deferred");
}

#[test]
fn a_present_null_raw_member_is_kept() {
    let part = serde_json::json!({"type": "tool_result", "tool_call_id": "t", "result": null});
    assert_eq!(
        serde_json::to_value(serde_json::from_value::<ContentPart>(part.clone()).unwrap()).unwrap(),
        part
    );
}

#[test]
fn a_present_null_result_is_kept_and_counts_as_the_result_arm() {
    let with_null = serde_json::json!({"session_id": "s", "run_id": "r", "tool_call_id": "t", "execution_owner": "agent", "result": null});
    assert_eq!(round_trip::<ActionCallPayload>(&with_null), with_null);
    let call: ActionCallResolveRequest =
        serde_json::from_value(serde_json::json!({"result": null})).unwrap();
    assert_eq!(call.arm(), Some(RESOLVE_ARM_RESULT));
    let none: ActionCallResolveRequest = serde_json::from_value(serde_json::json!({})).unwrap();
    assert_eq!(none.arm(), None);
    let both: ActionCallResolveRequest = serde_json::from_value(
        serde_json::json!({"started": {}, "error": {"code": "x", "message": "y"}}),
    )
    .unwrap();
    assert_eq!(both.arm(), None);
}

#[test]
fn the_highest_resolve_reason_is_the_earliest_on_the_ladder() {
    let reasons = [
        ResolveReason::LATE_ACKNOWLEDGEMENT,
        ResolveReason::from("not_on_the_ladder"),
        ResolveReason::WRONG_RESPONDER,
    ];
    assert_eq!(
        ResolveReason::highest(&reasons),
        Some(ResolveReason::WRONG_RESPONDER)
    );
    assert_eq!(ResolveReason::highest(&[ResolveReason::from("x")]), None);
    assert_eq!(ResolveReason::UNKNOWN_INTERACTION.rank(), Some(0));
}

#[test]
fn a_limit_reads_only_when_it_has_the_type_and_range_it_means() {
    let support: FeatureSupport = serde_json::from_value(serde_json::json!({
        "level": "native",
        "limits": {"max_tools": 0, "max_sources": 3, "name_pattern": "", "schema_dialect": "d", "transports": ["local", "carrier-pigeon"]}
    }))
    .unwrap();
    assert_eq!(support.max_tools(), None);
    assert_eq!(support.max_sources(), Some(3));
    assert_eq!(support.name_pattern(), None);
    assert_eq!(support.schema_dialect().as_deref(), Some("d"));
    assert_eq!(support.transports(), Some(vec!["local".to_owned()]));
    let unlisted: FeatureSupport = serde_json::from_value(
        serde_json::json!({"level": "native", "limits": {"transports": ["x"], "max_tools": 2.5}}),
    )
    .unwrap();
    assert_eq!(unlisted.transports(), None);
    assert_eq!(unlisted.max_tools(), None);
}

#[test]
fn effective_support_prefers_the_descriptor_then_layers_in_name_order() {
    let descriptor: CapabilityDescriptor = serde_json::from_value(serde_json::json!({
        "endpoint": {"id": "agent"},
        "features": {"a": {"level": "native"}},
        "layers": {
            "zeta": {"features": {"a": {"level": "degraded"}, "b": {"level": "degraded"}}, "sources": [{"id": "z", "kind": "local"}]},
            "alpha": {"features": {"b": {"level": "emulated"}}, "sources": [{"id": "a", "kind": "local"}]}
        },
        "sources": [{"id": "top", "kind": "remote"}]
    }))
    .unwrap();
    assert_eq!(
        descriptor.effective_support("a").unwrap().level,
        SupportLevel::NATIVE
    );
    assert_eq!(
        descriptor.effective_support("b").unwrap().level,
        SupportLevel::EMULATED
    );
    assert!(descriptor.effective_support("c").is_none());
    let ids: Vec<_> = descriptor
        .effective_sources()
        .into_iter()
        .map(|source| source.id)
        .collect();
    assert_eq!(ids, ["top", "a", "z"]);
}

#[test]
fn a_delivery_mode_names_its_feature_key() {
    assert_eq!(
        RequestedDeliveryMode::QUEUE.feature_key(),
        FEATURE_DELIVERY_QUEUE
    );
}
