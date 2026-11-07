use std::future::Future;
use std::path::Path;
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::{task::noop_waker, StreamExt};
use oap_adapter::{Adapter as _, Error, OpenRequest, SubmitRequest};
use oap_adapter_acp::{process::ProcessConfig, Adapter, Config};
use oap_protocol::{
    Message, MessageContent, MessageSubmitRequest, ModelsRequest, Participant,
    SessionModelSwitchRequest,
};
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
        assert!(Instant::now() < deadline, "public operation did not settle");
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn observed(directory: &Path, token: &str) {
    let deadline = Instant::now() + Duration::from_secs(5);
    while !directory.join(format!("observed-{token}")).exists() {
        assert!(
            Instant::now() < deadline,
            "native marker {token} was not answered"
        );
        std::thread::sleep(Duration::from_millis(1));
    }
}

fn error(error: Error) -> Value {
    match error {
        Error::UnsupportedControl(control) => {
            json!({"feature":control.feature,"reason":control.reason})
        }
        Error::ModelNotFound(id) => json!({"model_not_found":id}),
        error => json!({"other":error.to_string()}),
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
        abort_active_on_close: true,
        ..Config::default()
    })
    .unwrap();
    let session = wait(adapter.open(OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }))
    .unwrap();
    let initial = wait(session.state()).unwrap();
    let mut stream = input["active"].as_bool().unwrap_or(false).then(|| {
        let (_, stream) = wait(session.submit(SubmitRequest {
            request: MessageSubmitRequest {
                session_id: "session".into(),
                messages: vec![Message {
                    id: Some("input".into()),
                    role: "user".into(),
                    content: MessageContent::from_text("synthetic"),
                    ..Message::default()
                }],
                allow_degraded_features: vec![
                    "session.message.submit".into(),
                    "run.streaming".into(),
                ],
                ..MessageSubmitRequest::default()
            },
            ..SubmitRequest::default()
        }))
        .unwrap();
        observed(directory, "prompt");
        stream
    });
    let mut snapshots = Vec::new();
    for (index, step) in input["steps"].as_array().unwrap().iter().enumerate() {
        std::thread::sleep(Duration::from_millis(3));
        let mut command = step.clone();
        command["token"] = json!(index.to_string());
        std::fs::write(directory.join("next.json"), command.to_string()).unwrap();
        std::fs::rename(directory.join("next.json"), directory.join("command.json")).unwrap();
        observed(directory, &index.to_string());
        let state = wait(session.state()).unwrap();
        let catalog = match wait(session.model_lister().unwrap().models(ModelsRequest {
            session_id: "session".into(),
            allow_degraded_features: vec!["models.list".into()],
        })) {
            Ok(catalog) => json!({"ok":catalog.models}),
            Err(failure) => json!({"error":error(failure)}),
        };
        let switched =
            step["switch"].as_str().map(|model| {
                match wait(session.model_switcher().unwrap().switch_model(
                    SessionModelSwitchRequest {
                        session_id: "session".into(),
                        model_id: model.into(),
                        allow_degraded_features: vec!["session.model.switch".into()],
                    },
                )) {
                    Ok((response, state)) => json!({"ok":response,"state":state}),
                    Err(failure) => json!({"error":error(failure)}),
                }
            });
        snapshots.push(json!({"state":state,"catalog":catalog,"switch":switched}));
    }
    let mut events = Vec::new();
    if let Some(stream) = &mut stream {
        std::fs::write(
            directory.join("next.json"),
            json!({"finish":true,"token":"finish"}).to_string(),
        )
        .unwrap();
        std::fs::rename(directory.join("next.json"), directory.join("command.json")).unwrap();
        while let Some(event) = wait(stream.next()) {
            events.push(event.unwrap());
        }
        observed(directory, "finish");
    }
    let final_state = wait(session.state()).unwrap();
    wait(session.close()).unwrap();
    let pid: i32 = std::fs::read_to_string(directory.join("pid"))
        .unwrap()
        .parse()
        .unwrap();
    assert!(!std::process::Command::new("/bin/kill")
        .args(["-0", &pid.to_string()])
        .output()
        .unwrap()
        .status
        .success());
    json!({"initial":initial,"snapshots":snapshots,"events":events,"final":final_state,"descriptor":adapter.describe().capabilities,"revision":adapter.describe().capability_revision})
}
