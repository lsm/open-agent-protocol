use std::collections::HashSet;

use oap_protocol::{
    CapabilityDescriptor, CompactionPolicy, FeatureSupport, MessageSubmitRequest, ReasoningLevel,
    RequestedDeliveryMode, SessionSettingsUpdateRequest, SupportLevel, FEATURE_COMPACTION_POLICY,
    FEATURE_DELIVERY_BTW, FEATURE_DELIVERY_QUEUE, FEATURE_DELIVERY_STEER, FEATURE_INSTRUCTIONS,
    FEATURE_MODEL_SELECTION, FEATURE_SESSION_REASONING, FEATURE_STRUCTURED_OUTPUT,
    FEATURE_TOOLS_PROVIDE, FEATURE_TOOL_SELECTION, FEATURE_TOOL_SOURCES_ATTACH, MODE_SESSION_LIVE,
    MODE_SESSION_OPEN,
};

use crate::{Error, OpenRequest, UnsupportedControl};

fn unadvertised(feature: &str) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl::unadvertised(feature)))
}

fn delivery_feature(mode: &RequestedDeliveryMode) -> Option<&'static str> {
    match mode.as_str() {
        "queue" => Some(FEATURE_DELIVERY_QUEUE),
        "steer" => Some(FEATURE_DELIVERY_STEER),
        "btw" => Some(FEATURE_DELIVERY_BTW),
        _ => None,
    }
}

pub fn refuse_unadvertised_controls(
    request: &MessageSubmitRequest,
    advertised: &[&str],
) -> Result<(), Error> {
    let controls = [
        (FEATURE_INSTRUCTIONS, request.instructions.is_some()),
        (FEATURE_MODEL_SELECTION, request.model_id.is_some()),
        (FEATURE_STRUCTURED_OUTPUT, request.output_schema.is_some()),
        (FEATURE_TOOL_SELECTION, request.tool_choice.is_some()),
    ];
    let delivery = delivery_feature(&request.delivery).map(|key| (key, true));
    for (key, present) in controls.into_iter().chain(delivery) {
        if present && !advertised.contains(&key) {
            return Err(unadvertised(key));
        }
    }
    Ok(())
}

fn offered(support: &FeatureSupport) -> bool {
    !support.level.is_empty() && support.level != SupportLevel::UNAVAILABLE
}

pub fn refuse_unadvertised_tool_sources(
    request: &OpenRequest,
    disclosed: &[FeatureSupport],
) -> Result<(), Error> {
    if request.tool_sources.is_empty()
        || disclosed
            .iter()
            .any(|support| offered(support) && support.discloses_mode(MODE_SESSION_OPEN))
    {
        return Ok(());
    }
    Err(unadvertised(FEATURE_TOOL_SOURCES_ATTACH))
}

pub fn refuse_unadvertised_tools(
    request: &OpenRequest,
    disclosed: &[FeatureSupport],
) -> Result<(), Error> {
    if request.tools.is_empty() || disclosed.iter().any(offered) {
        return Ok(());
    }
    Err(unadvertised(FEATURE_TOOLS_PROVIDE))
}

pub fn open_setting_keys(
    level: &ReasoningLevel,
    policy: Option<&CompactionPolicy>,
) -> Vec<&'static str> {
    let mut keys = Vec::new();
    if !level.is_empty() {
        keys.push(FEATURE_SESSION_REASONING);
    }
    if policy.is_some() {
        keys.push(FEATURE_COMPACTION_POLICY);
    }
    keys
}

pub fn open_setting_field(key: &str) -> &'static str {
    match key {
        FEATURE_SESSION_REASONING => "reasoning_level",
        FEATURE_COMPACTION_POLICY => "compaction_policy",
        _ => "",
    }
}

fn refuse_settings(
    keys: Vec<&'static str>,
    descriptor: &CapabilityDescriptor,
    mode: &str,
    opted: &[String],
) -> Result<(), Error> {
    for key in keys {
        let support = descriptor.effective_support(key);
        if !support.is_some_and(|support| offered(support) && support.discloses_mode(mode)) {
            let control = UnsupportedControl {
                field: open_setting_field(key).to_owned(),
                ..UnsupportedControl::unadvertised(key)
            };
            return Err(Error::UnsupportedControl(Box::new(control)));
        }
        if support.is_some_and(|support| support.level == SupportLevel::DEGRADED)
            && !opted.iter().any(|o| o == key)
        {
            return Err(Error::DegradedControl(key.to_owned()));
        }
    }
    Ok(())
}

pub fn refuse_unadvertised_settings(
    request: &OpenRequest,
    descriptor: &CapabilityDescriptor,
) -> Result<(), Error> {
    let keys = open_setting_keys(&request.reasoning_level, request.compaction_policy.as_ref());
    refuse_settings(
        keys,
        descriptor,
        MODE_SESSION_OPEN,
        &request.allow_degraded_features,
    )
}

pub fn refuse_unadvertised_live_settings(
    request: &SessionSettingsUpdateRequest,
    descriptor: &CapabilityDescriptor,
) -> Result<(), Error> {
    let keys = open_setting_keys(&request.reasoning_level, request.compaction_policy.as_ref());
    refuse_settings(
        keys,
        descriptor,
        MODE_SESSION_LIVE,
        &request.allow_degraded_features,
    )
}

pub fn duplicate_environment_name(entries: &[String]) -> Option<&str> {
    let mut seen = HashSet::new();
    entries
        .iter()
        .map(|entry| {
            entry
                .split_once('=')
                .map_or(entry.as_str(), |(name, _)| name)
        })
        .find(|name| !seen.insert(*name))
}
