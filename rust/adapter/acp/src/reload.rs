use std::time::{Duration, Instant};

use oap_adapter::{Error, OpenRequest, UnsupportedControl, CONTROL_UNSATISFIABLE};
use oap_protocol::FEATURE_OPEN_REOPEN;
use serde_json::value::RawValue;

use crate::native::{
    SessionNewParams, SessionReopenParams, METHOD_SESSION_LOAD, METHOD_SESSION_NEW,
    METHOD_SESSION_RESUME,
};
use crate::process::Process;
use crate::Config;

pub(crate) const REASON: &str = "ACP restored the bound conversation; configOptions reports the agent's current settings, and omitted settings remain the loader's configuration";

pub(crate) fn refusal(detail: impl Into<String>) -> Error {
    Error::UnsupportedControl(Box::new(UnsupportedControl {
        feature: FEATURE_OPEN_REOPEN.into(),
        reason: CONTROL_UNSATISFIABLE,
        detail: detail.into(),
        ..UnsupportedControl::default()
    }))
}

pub(crate) async fn call(
    process: Process,
    request: &OpenRequest,
    config: &Config,
) -> Result<(Process, Box<RawValue>), Error> {
    if !request.reopen {
        let params = serde_json::value::to_raw_value(&SessionNewParams {
            cwd: config.process.dir.clone(),
            mcp_servers: config.mcp_servers.clone(),
        })
        .map_err(|error| Error::Harness(error.to_string()))?;
        return process
            .call_async(METHOD_SESSION_NEW.into(), Some(params), None)
            .await
            .map_err(|error| Error::Harness(format!("create ACP session: {error}")));
    }
    let caps = process
        .initialize
        .agent_capabilities
        .as_ref()
        .expect("initialize validated capabilities");
    let method = if caps
        .get("loadSession")
        .is_some_and(|raw| serde_json::from_str::<bool>(raw.get()).unwrap_or(false))
    {
        Some(METHOD_SESSION_LOAD)
    } else if caps
        .get("sessionCapabilities")
        .and_then(|raw| serde_json::from_str::<serde_json::Value>(raw.get()).ok())
        .is_some_and(|value| {
            value
                .get("resume")
                .is_some_and(serde_json::Value::is_object)
        })
    {
        Some(METHOD_SESSION_RESUME)
    } else {
        None
    };
    let Some(method) = method else {
        let _ = process.close_async().await;
        return Err(refusal(
            "the ACP agent advertises neither loadSession nor sessionCapabilities.resume",
        ));
    };
    let params = serde_json::value::to_raw_value(&SessionReopenParams {
        session_id: request.native_session_id.clone(),
        cwd: config.process.dir.clone(),
        mcp_servers: config.mcp_servers.clone(),
    })
    .map_err(|error| Error::Harness(error.to_string()))?;
    process
        .call_draining_async(
            method.into(),
            Some(params),
            Some(Instant::now() + Duration::from_secs(60)),
        )
        .await
        .map_err(|error| {
            refusal(format!(
                "the ACP agent could not load the bound session: {error}"
            ))
        })
}
