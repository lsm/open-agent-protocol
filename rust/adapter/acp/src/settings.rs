use std::collections::BTreeMap;
use std::time::{Duration, Instant};

use oap_adapter::{Error, OpenRequest, UnsupportedControl, CONTROL_UNSATISFIABLE};
use oap_protocol::FEATURE_SESSION_REASONING;

use crate::native::{
    decode, ConfigOption, SessionNewResult, SetConfigOptionParams, SetConfigOptionResult,
    CATEGORY_THOUGHT_LEVEL, METHOD_SESSION_SET_CONFIG_OPTION,
};
use crate::{process::Process, Config};

fn option<'a>(options: &'a [ConfigOption], id: &str) -> Option<&'a ConfigOption> {
    let mut matches = options.iter().filter(|option| option.id == id);
    let option = matches.next()?;
    (matches.next().is_none() && matches!(option.kind.as_str(), "" | "select")).then_some(option)
}

fn config_error(id: &str, detail: impl std::fmt::Display) -> Error {
    Error::Harness(format!(
        "ACP session config {}: {detail}",
        oap_adapter::gojson::go_quote(id)
    ))
}

fn refusal(detail: impl Into<String>) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl {
        feature: FEATURE_SESSION_REASONING.into(),
        reason: CONTROL_UNSATISFIABLE,
        field: "reasoning_level".into(),
        detail: detail.into(),
        ..UnsupportedControl::default()
    }))
}

fn confirm(options: &[ConfigOption], wanted: &BTreeMap<String, String>) -> Result<(), Error> {
    for (id, value) in wanted {
        if !option(options, id).is_some_and(|option| option.current_value == *value) {
            return Err(config_error(
                id,
                "later setting changed the requested value",
            ));
        }
    }
    Ok(())
}

async fn set(
    owner: &mut Option<Process>,
    opened: &mut SessionNewResult,
    id: &str,
    value: &str,
) -> Result<(), Error> {
    let params = serde_json::value::to_raw_value(&SetConfigOptionParams {
        session_id: opened.session_id.clone(),
        config_id: id.into(),
        value: value.into(),
    })
    .map_err(Error::harness)?;
    let (process, raw) = owner
        .take()
        .expect("opening process")
        .call_draining_async(
            METHOD_SESSION_SET_CONFIG_OPTION.into(),
            Some(params),
            Some(Instant::now() + Duration::from_secs(60)),
        )
        .await
        .map_err(Error::harness)?;
    *owner = Some(process);
    opened.config_options = decode::<SetConfigOptionResult>(raw.get().as_bytes())
        .map_err(Error::harness)?
        .config_options;
    Ok(())
}

pub(crate) async fn configure(
    process: Process,
    mut opened: SessionNewResult,
    config: &Config,
    request: &OpenRequest,
) -> Result<(Process, SessionNewResult), Error> {
    let mut owner = Some(process);
    let result = async {
        for (id, value) in &config.session_config {
            if id.is_empty()
                || value.is_empty()
                || !option(&opened.config_options, id).is_some_and(|option| {
                    option
                        .values()
                        .iter()
                        .any(|offered| offered.value == *value)
                })
            {
                return Err(config_error(id, "requested value is not advertised"));
            }
            set(&mut owner, &mut opened, id, value)
                .await
                .map_err(|error| config_error(id, format!("set rejected: {error}")))?;
            if !option(&opened.config_options, id)
                .is_some_and(|option| option.current_value == *value)
            {
                return Err(config_error(id, "value not confirmed"));
            }
        }
        confirm(&opened.config_options, &config.session_config)?;
        if !request.reasoning_level.is_empty() {
            let option = opened
                .config_options
                .iter()
                .find(|option| option.category == CATEGORY_THOUGHT_LEVEL)
                .ok_or_else(|| refusal("the agent offers no thought_level option"))?;
            let offered = option
                .values()
                .into_iter()
                .find(|value| {
                    value
                        .value
                        .eq_ignore_ascii_case(request.reasoning_level.as_str())
                        || value
                            .name
                            .eq_ignore_ascii_case(request.reasoning_level.as_str())
                })
                .ok_or_else(|| {
                    refusal(format!(
                        "the agent's thought_level option offers no value named {}",
                        request.reasoning_level
                    ))
                })?;
            let id = option.id.clone();
            set(&mut owner, &mut opened, &id, &offered.value)
                .await
                .map_err(|error| {
                    refusal(format!(
                        "the agent refused its thought_level option: {error}"
                    ))
                })?;
            if !opened
                .config_options
                .iter()
                .any(|option| option.id == id && option.current_value == offered.value)
            {
                return Err(refusal(
                    "the agent did not confirm its thought_level option",
                ));
            }
        }
        confirm(&opened.config_options, &config.session_config)
    }
    .await;
    if let Err(error) = result {
        if let Some(process) = owner {
            let _ = process.close_async().await;
        }
        return Err(error);
    }
    Ok((owner.expect("opening process"), opened))
}
