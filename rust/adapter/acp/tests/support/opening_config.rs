use std::future::Future;
use std::path::Path;
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::task::noop_waker;
use oap_adapter::{Adapter as _, Error, OpenRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{ModelsRequest, Participant};
use serde_json::{json, Value};

pub fn wait<T>(future: impl Future<Output = T>) -> T {
    let mut future = std::pin::pin!(future);
    let deadline = Instant::now() + Duration::from_secs(5);
    loop {
        if let Poll::Ready(value) = future
            .as_mut()
            .poll(&mut Context::from_waker(&noop_waker()))
        {
            return value;
        }
        assert!(Instant::now() < deadline, "opening settings did not settle");
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn failure(error: Error) -> Value {
    match error {
        Error::UnsupportedControl(control) => {
            json!({"feature":control.feature,"reason":control.reason,"field":control.field,"detail":control.detail})
        }
        error => json!({"error":error.to_string()}),
    }
}

fn reaped(directory: &Path) {
    if let Ok(pid) = std::fs::read_to_string(directory.join("pid")) {
        assert!(
            !std::process::Command::new("/bin/kill")
                .args(["-0", &pid])
                .output()
                .unwrap()
                .status
                .success(),
            "native {pid} survived"
        );
    }
}

pub fn run(input: &Value) -> Value {
    let directory = Path::new(input["case"].as_str().unwrap());
    let adapter = Adapter::new(Config {
        process: ProcessConfig {
            path: input["python"].as_str().unwrap().into(),
            args: vec![
                "-u".into(),
                input["agent"].as_str().unwrap().into(),
                directory.to_string_lossy().into(),
            ],
            dir: "/".into(),
            env: Some(Vec::new()),
            queue_capacity: 1,
            shutdown_timeout: Some(Duration::from_secs(1)),
            ..ProcessConfig::default()
        },
        ..Config::default()
    })
    .unwrap();
    let request = OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        reopen: input["reopen"].as_bool().unwrap_or(false),
        native_session_id: "native".into(),
        reasoning_level: input["reasoning"].as_str().unwrap_or_default().into(),
        ..OpenRequest::default()
    };
    let mut opening = adapter.open(request);
    if input["cancel"].as_bool().unwrap_or(false) {
        let limit = Instant::now() + Duration::from_secs(5);
        while !directory.join("reverse-answered").exists() {
            let began = Instant::now();
            if let Poll::Ready(result) = opening
                .as_mut()
                .poll(&mut Context::from_waker(&noop_waker()))
            {
                return match result {
                    Err(error) => json!({"failure":failure(error)}),
                    Ok(session) => {
                        wait(session.close()).unwrap();
                        json!({"unexpected_open":true})
                    }
                };
            }
            assert!(began.elapsed() < Duration::from_millis(50));
            assert!(Instant::now() < limit, "native setting was not observed");
            std::thread::sleep(Duration::from_millis(1));
        }
        let began = Instant::now();
        drop(opening);
        assert!(began.elapsed() < Duration::from_secs(3));
        reaped(directory);
        return json!({"cancelled":true});
    }
    let session = match wait(opening) {
        Ok(session) => session,
        Err(error) => {
            reaped(directory);
            return json!({"failure":failure(error)});
        }
    };
    let state = wait(session.state()).unwrap();
    let models = wait(session.model_lister().unwrap().models(ModelsRequest {
        session_id: "session".into(),
        allow_degraded_features: vec!["models.list".into()],
    }))
    .map(|catalog| json!({"ok":catalog.models}))
    .unwrap_or_else(|error| json!({"failure":failure(error)}));
    let native_id = session.native_session_id();
    wait(session.close()).unwrap();
    reaped(directory);
    json!({"state":state,"models":models,"native_id":native_id,"descriptor":adapter.describe().capabilities,"revision":adapter.describe().capability_revision})
}
