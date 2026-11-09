use oap_adapter::{BoxFuture, Error, ModelSwitcher, UnsupportedControl, CONTROL_UNSATISFIABLE};
use oap_protocol::{
    ReasoningLevel, SessionModelSwitchRequest, SessionModelSwitchResponse, SessionState,
    FEATURE_SESSION_MODEL_SWITCH,
};

use crate::native::{
    decode, ConfigOption, ConfigOptionUpdate, SessionUpdateParams, SetConfigOptionParams,
    SetConfigOptionResult, CATEGORY_THOUGHT_LEVEL, METHOD_SESSION_SET_CONFIG_OPTION,
    METHOD_SESSION_UPDATE,
};
use crate::session::{Inner, Session};

fn refusal(detail: impl Into<String>) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl {
        feature: FEATURE_SESSION_MODEL_SWITCH.into(),
        reason: CONTROL_UNSATISFIABLE,
        field: "model_id".into(),
        detail: detail.into(),
        ..UnsupportedControl::default()
    }))
}

pub(crate) fn missing_model(feature: &str) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl {
        feature: feature.into(),
        reason: CONTROL_UNSATISFIABLE,
        detail: "the ACP agent reports no configOption whose category is model".into(),
        ..UnsupportedControl::default()
    }))
}

fn level(value: &str) -> ReasoningLevel {
    let lower = value.replace('\u{0130}', "i").to_lowercase();
    match lower.as_str() {
        "off" | "low" | "medium" | "high" | "xhigh" | "max" => lower.into(),
        _ => ReasoningLevel::default(),
    }
}

fn reasoning(options: &[ConfigOption]) -> ReasoningLevel {
    let mut result = ReasoningLevel::default();
    for option in options
        .iter()
        .filter(|option| option.category == CATEGORY_THOUGHT_LEVEL)
    {
        let parsed = level(&option.current_value);
        if !parsed.is_empty() {
            result = parsed;
            continue;
        }
        if let Some(value) = option
            .values()
            .into_iter()
            .find(|value| value.value == option.current_value)
        {
            let parsed = level(&value.name);
            if !parsed.is_empty() {
                result = parsed;
            }
        }
    }
    result
}

impl Inner {
    pub(crate) fn observe_config(
        &mut self,
        notification: &oap_adapter::jsonrpc::NotificationMessage,
        native_id: &str,
    ) -> bool {
        if self.process.is_none() || notification.method != METHOD_SESSION_UPDATE {
            return false;
        }
        let Some(params) = notification.params.as_deref() else {
            return false;
        };
        let Ok(params) = decode::<SessionUpdateParams>(params.get().as_bytes()) else {
            return false;
        };
        let Some(update) = params.update.as_deref() else {
            return false;
        };
        let Ok(update) = decode::<ConfigOptionUpdate>(update.get().as_bytes()) else {
            return false;
        };
        if params.session_id != native_id || update.session_update != "config_option_update" {
            return false;
        }
        self.refresh();
        self.apply_config(&update.config_options);
        true
    }

    pub(crate) fn apply_config(&mut self, options: &[ConfigOption]) {
        let previous = (
            self.state.current_model_id.clone(),
            self.state.reasoning_level.clone(),
        );
        self.model_option = options
            .iter()
            .find(|option| option.category == "model")
            .cloned();
        if let Some(option) = options.iter().rev().find(|option| {
            option.category == "model"
                && !matches!(option.current_value.as_str(), "" | "default" | "current")
        }) {
            self.state
                .current_model_id
                .clone_from(&option.current_value);
        }
        self.state.reasoning_level = reasoning(options);
        if previous
            != (
                self.state.current_model_id.clone(),
                self.state.reasoning_level.clone(),
            )
        {
            self.state.updated_at_ms = self.runtime.now_ms();
        }
    }
}

impl ModelSwitcher for Session {
    fn switch_model(
        &self,
        request: SessionModelSwitchRequest,
    ) -> BoxFuture<'_, (SessionModelSwitchResponse, SessionState)> {
        Box::pin(async move {
            if !request.allows_degraded(FEATURE_SESSION_MODEL_SWITCH) {
                return Err(Error::DegradedControl(FEATURE_SESSION_MODEL_SWITCH.into()));
            }
            let _switching = self.switching.lock().await;
            let (process, option) = {
                let inner = self.lock();
                let process = inner.process.clone().ok_or(Error::SessionClosed)?;
                if request.session_id != inner.state.session_id {
                    return Err(Error::InvalidSubmission);
                }
                let option = inner
                    .model_option
                    .as_ref()
                    .ok_or_else(|| missing_model(FEATURE_SESSION_MODEL_SWITCH))?;
                if !option
                    .values()
                    .iter()
                    .any(|value| value.value == request.model_id)
                {
                    return Err(Error::ModelNotFound(request.model_id.clone()));
                }
                (process, option.id.clone())
            };
            let params = serde_json::value::to_raw_value(&SetConfigOptionParams {
                session_id: self.native_id.clone(),
                config_id: option.clone(),
                value: request.model_id.clone(),
            })
            .map_err(|error| Error::Harness(error.to_string()))?;
            let raw = process
                .call(METHOD_SESSION_SET_CONFIG_OPTION.into(), Some(params), None)
                .await
                .map_err(|error| match error.fault {
                    oap_adapter::jsonrpc::Fault::Remote { object, .. } => refusal(format!(
                        "the agent refused its model option: {}",
                        object.message
                    )),
                    _ => Error::Harness(error.to_string()),
                })?;
            let set = decode::<SetConfigOptionResult>(raw.get().as_bytes()).map_err(|error| {
                Error::Harness(format!(
                    "decode session/set_config_option response: {error}"
                ))
            })?;
            let mut confirmations = set.config_options.iter().filter(|value| value.id == option);
            let confirmed = confirmations.next().is_some_and(|value| {
                matches!(value.kind.as_str(), "" | "select")
                    && value.current_value == request.model_id
            });
            if !confirmed || confirmations.next().is_some() {
                return Err(refusal("the agent did not confirm its model option"));
            }
            let mut inner = self.lock();
            if inner.process.is_none() {
                return Err(Error::SessionClosed);
            }
            inner.refresh();
            let previous =
                std::mem::replace(&mut inner.state.current_model_id, request.model_id.clone());
            inner.state.reasoning_level = reasoning(&set.config_options);
            inner.state.updated_at_ms = inner.runtime.now_ms();
            Ok((
                SessionModelSwitchResponse {
                    session_id: inner.state.session_id.clone(),
                    model_id: request.model_id,
                    previous_model_id: previous,
                },
                inner.state.clone(),
            ))
        })
    }
}
