use std::future::Future;
use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};
use std::task::{Context, Poll};
use std::time::{Duration, Instant};

use futures::{task::noop_waker, StreamExt};
use oxap_adapter::{
    Adapter as _, Error, EventStream, OpenRequest, ResumeRequest, Session, SubmitRequest,
};
use oxap_adapter_acp::{pin, process::ProcessConfig, Adapter, Config};
use oxap_protocol::{
    Envelope, Message, MessageContent, MessageRole, MessageSubmitRequest, Participant, RunId,
    SessionStatus,
};
use serde_json::{json, Value};

static NEXT: AtomicU64 = AtomicU64::new(0);

fn wait<F: Future>(future: F) -> F::Output {
    let mut future = Box::pin(future);
    let deadline = Instant::now() + Duration::from_secs(10);
    loop {
        match future
            .as_mut()
            .poll(&mut Context::from_waker(&noop_waker()))
        {
            Poll::Ready(value) => return value,
            Poll::Pending => {
                assert!(
                    Instant::now() < deadline,
                    "public corpus operation timed out"
                );
                std::thread::sleep(Duration::from_millis(1));
            }
        }
    }
}

fn corpus_case(id: &str) -> (PathBuf, Value) {
    let root = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&pin().corpus);
    let manifest: Value =
        serde_json::from_slice(&std::fs::read(root.join("manifest.json")).unwrap()).unwrap();
    let entry = manifest["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|entry| entry["id"] == id)
        .unwrap();
    let directory = root.join(entry["path"].as_str().unwrap());
    let definition =
        serde_json::from_slice(&std::fs::read(directory.join("case.json")).unwrap()).unwrap();
    (directory, definition)
}

struct Fixture {
    directory: PathBuf,
    retained: bool,
    environment: Vec<String>,
}

impl Fixture {
    fn new(id: &str) -> Self {
        let retained = std::env::var_os("OAP_ACP_CORPUS_EVIDENCE").map(PathBuf::from);
        let base = retained.clone().unwrap_or_else(std::env::temp_dir);
        let directory = base.join(format!(
            "{id}-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&directory).unwrap();
        let (source, definition) = corpus_case(id);
        std::fs::copy(
            source.join(definition["native"].as_str().unwrap()),
            directory.join("native.jsonl"),
        )
        .unwrap();
        std::fs::write(
            directory.join("agent.py"),
            include_str!("../../../../.claude/skills/verify/scripts/fixtures/acp-journal-agent.py"),
        )
        .unwrap();
        let environment = definition
            .get("environment")
            .cloned()
            .map(serde_json::from_value)
            .transpose()
            .unwrap()
            .unwrap_or_default();
        Self {
            directory,
            retained: retained.is_some(),
            environment,
        }
    }

    fn adapter(&self) -> Adapter {
        self.adapter_with_capacity(0)
    }

    fn adapter_with_capacity(&self, capacity: usize) -> Adapter {
        let selected = Command::new("python3")
            .args(["-c", "import sys; print(sys.executable)"])
            .output()
            .unwrap();
        assert!(selected.status.success());
        Adapter::new(Config {
            journal_capacity: capacity,
            abort_active_on_close: true,
            process: ProcessConfig {
                path: String::from_utf8(selected.stdout).unwrap().trim().into(),
                args: vec![
                    "-u".into(),
                    self.directory.join("agent.py").to_string_lossy().into(),
                    self.directory.to_string_lossy().into(),
                ],
                dir: "/".into(),
                env: Some(self.environment.clone()),
                queue_capacity: 1,
                shutdown_timeout: Some(Duration::from_secs(1)),
                ..ProcessConfig::default()
            },
            ..Config::default()
        })
        .unwrap()
    }

    fn reaped(&self) {
        let path = self.directory.join("pid");
        if path.exists() {
            let pid = std::fs::read_to_string(path).unwrap();
            assert!(
                !Command::new("/bin/kill")
                    .args(["-0", pid.trim()])
                    .output()
                    .unwrap()
                    .status
                    .success(),
                "native corpus child survived close"
            );
        }
    }
}

impl Drop for Fixture {
    fn drop(&mut self) {
        if !self.retained {
            std::fs::remove_dir_all(&self.directory).unwrap();
        }
    }
}

fn request() -> OpenRequest {
    OpenRequest {
        session_id: "session".into(),
        participant: Participant {
            id: "user".into(),
            ..Participant::default()
        },
        ..OpenRequest::default()
    }
}

#[test]
fn the_captured_replay_case_resumes_the_actual_completed_public_run() {
    let fixture = Fixture::new("replay-degradation");
    let session = wait(fixture.adapter().open(request())).unwrap();
    let (admission, mut stream) = wait(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("hello"),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap();
    let mut events = Vec::new();
    while let Some(event) = wait(stream.next()) {
        events.push(event.unwrap());
    }
    assert_eq!(events.len(), 3);
    let (_, definition) = corpus_case("replay-degradation");
    let after = definition["replay_after"].as_u64().unwrap();
    let result = wait(session.resume(ResumeRequest {
        run_id: admission.run_id.unwrap(),
        after_sequence: after,
    }));
    let observed = match result {
        Ok((recovery, mut replay)) => {
            let mut suffix = Vec::new();
            while let Some(event) = wait(replay.next()) {
                suffix.push(event.unwrap());
            }
            assert_eq!(recovery.replayed_from, after + 1);
            assert_eq!(recovery.replayed_through, events.len() as u64);
            assert_eq!(
                serde_json::to_value(suffix).unwrap(),
                serde_json::to_value(&events[after as usize..]).unwrap()
            );
            None
        }
        Err(error) => Some(format!("{error:?}")),
    };
    wait(session.close()).unwrap();
    fixture.reaped();
    std::fs::write(
        fixture.directory.join("events.json"),
        serde_json::to_vec_pretty(&events).unwrap(),
    )
    .unwrap();
    assert_eq!(observed, None, "captured public replay was refused");
}

fn submit(session: &dyn Session) -> (oxap_protocol::MessageSubmitResponse, EventStream) {
    wait(session.submit(SubmitRequest {
        request: MessageSubmitRequest {
            session_id: "session".into(),
            messages: vec![Message {
                role: MessageRole::USER,
                content: MessageContent::from_text("hello"),
                ..Message::default()
            }],
            allow_degraded_features: vec!["session.message.submit".into(), "run.streaming".into()],
            ..MessageSubmitRequest::default()
        },
        ..SubmitRequest::default()
    }))
    .unwrap()
}

fn collect(stream: &mut EventStream) -> (Vec<Envelope>, Option<Error>) {
    let mut events = Vec::new();
    while let Some(event) = wait(stream.next()) {
        match event {
            Ok(event) => events.push(event),
            Err(error) => return (events, Some(error)),
        }
    }
    (events, None)
}

fn replay(session: &dyn Session, run_id: &RunId, after: u64) -> (Value, EventStream) {
    let (recovery, stream) = wait(session.resume(ResumeRequest {
        run_id: run_id.clone(),
        after_sequence: after,
    }))
    .unwrap();
    (
        json!({"state":recovery.state,"run_id":recovery.run_id,"requested_after":recovery.requested_after,"replayed_from":recovery.replayed_from,"replayed_through":recovery.replayed_through}),
        stream,
    )
}

pub fn exercise(mode: &str) -> Value {
    let fixture = Fixture::new("replay-degradation");
    let held = matches!(mode, "active" | "close" | "overflow");
    let large = matches!(mode, "large-close" | "overflow");
    std::fs::write(
        fixture.directory.join("options.json"),
        json!({"hold":held,"chunks":if large {100}else{1},"reload":mode=="reopen"}).to_string(),
    )
    .unwrap();
    let capacity = if matches!(mode, "evicted" | "cross-run" | "overflow") {
        2
    } else {
        256
    };
    let adapter = if mode == "default" {
        fixture.adapter()
    } else {
        fixture.adapter_with_capacity(capacity)
    };
    let descriptor = wait(adapter.probe()).unwrap();
    assert_eq!(
        descriptor.journal.capacity,
        if mode == "default" { 256 } else { capacity }
    );
    assert_eq!(descriptor.journal.scope, "session");
    assert_eq!(descriptor.journal.persistence, "process_memory");
    assert_eq!(descriptor.journal.replay.as_str(), "degraded");
    let session = wait(adapter.open(request())).unwrap();
    let opened = wait(session.state()).unwrap();
    let (admission, mut primary) = submit(session.as_ref());
    let id = admission.run_id.clone().unwrap();
    let mut out = json!({"descriptor":descriptor.capabilities,"revision":descriptor.capability_revision,"journal":{"scope":descriptor.journal.scope,"persistence":descriptor.journal.persistence,"replay":descriptor.journal.replay,"capacity":descriptor.journal.capacity},"opened":opened,"admission":admission});
    if held {
        let prefix = vec![
            wait(primary.next()).unwrap().unwrap(),
            wait(primary.next()).unwrap().unwrap(),
        ];
        let after = if mode == "active" { 1 } else { 0 };
        let (recovery, mut following) = replay(session.as_ref(), &id, after);
        out["recovery"] = recovery;
        if mode == "active" {
            let first = wait(following.next()).unwrap().unwrap();
            assert_eq!(
                serde_json::to_value(&first).unwrap(),
                serde_json::to_value(&prefix[1]).unwrap()
            );
            assert!(matches!(
                following
                    .as_mut()
                    .poll_next(&mut Context::from_waker(&noop_waker())),
                Poll::Pending
            ));
            std::fs::write(fixture.directory.join("release-1"), "go").unwrap();
            let (tail, error) = collect(&mut primary);
            assert_eq!(error, None);
            let (replayed, error) = collect(&mut following);
            assert_eq!(error, None);
            assert_eq!(
                serde_json::to_value(&replayed).unwrap(),
                serde_json::to_value(&tail).unwrap()
            );
            out["events"] = serde_json::to_value([prefix, tail].concat()).unwrap();
            out["replayed"] = serde_json::to_value([vec![first], replayed].concat()).unwrap();
        } else if mode == "close" {
            let began = Instant::now();
            let closed = wait(session.close());
            assert!(
                matches!(&closed, Err(Error::Harness(message)) if message.contains("shutdown timed out"))
            );
            assert!(began.elapsed() < Duration::from_secs(3));
            out["close_error"] = json!(closed.unwrap_err().to_string());
            let (replayed, error) = collect(&mut following);
            assert_eq!(error, None);
            assert_eq!(
                serde_json::to_value(&replayed).unwrap(),
                serde_json::to_value(&prefix).unwrap()
            );
            assert!(collect(&mut primary).0.is_empty());
            assert_eq!(
                wait(session.resume(ResumeRequest {
                    run_id: id.clone(),
                    after_sequence: 0
                }))
                .err(),
                Some(Error::SessionClosed)
            );
            out["events"] = serde_json::to_value(prefix).unwrap();
            out["replayed"] = serde_json::to_value(replayed).unwrap();
        } else {
            std::fs::write(fixture.directory.join("release-1"), "go").unwrap();
            let deadline = Instant::now() + Duration::from_secs(10);
            while wait(session.state()).unwrap().status != SessionStatus::IDLE {
                assert!(Instant::now() < deadline);
                std::thread::sleep(Duration::from_millis(1));
            }
            let (replayed, error) = collect(&mut following);
            assert_eq!(error, Some(Error::EventStreamOverflow));
            assert_eq!(replayed.len(), 66);
            for (index, event) in replayed.iter().enumerate() {
                assert_eq!(event.sequence, Some(index as u64 + 1));
            }
            out["events"] = serde_json::to_value(prefix).unwrap();
            out["replayed"] = serde_json::to_value(replayed).unwrap();
            out["overflow"] = json!(true);
        }
    } else {
        if mode == "large-close" {
            let deadline = Instant::now() + Duration::from_secs(10);
            while wait(session.state()).unwrap().status != SessionStatus::IDLE {
                assert!(Instant::now() < deadline);
                std::thread::sleep(Duration::from_millis(1));
            }
        }
        let (events, error) = collect(&mut primary);
        if mode == "large-close" {
            assert_eq!(error, Some(Error::EventStreamOverflow));
            let deadline = Instant::now() + Duration::from_secs(10);
            while wait(session.state()).unwrap().status != SessionStatus::IDLE {
                assert!(Instant::now() < deadline);
                std::thread::sleep(Duration::from_millis(1));
            }
            let (_, mut reference) = replay(session.as_ref(), &id, 0);
            let (expected, error) = collect(&mut reference);
            assert_eq!(error, None);
            assert_eq!(expected.len(), 102);
            let (recovery, mut following) = replay(session.as_ref(), &id, 0);
            wait(session.close()).unwrap();
            let (replayed, error) = collect(&mut following);
            assert_eq!(error, None);
            assert_eq!(
                serde_json::to_value(&replayed).unwrap(),
                serde_json::to_value(&expected).unwrap()
            );
            out["events"] = serde_json::to_value(expected).unwrap();
            out["replayed"] = serde_json::to_value(replayed).unwrap();
            out["recovery"] = recovery;
        } else {
            assert_eq!(error, None);
            assert_eq!(
                wait(session.resume(ResumeRequest {
                    run_id: "missing".into(),
                    after_sequence: 0
                }))
                .err(),
                Some(Error::RunNotFound)
            );
            assert_eq!(
                wait(session.resume(ResumeRequest {
                    run_id: id.clone(),
                    after_sequence: 4
                }))
                .err(),
                Some(Error::ReplayCursorFuture)
            );
            let after = if mode == "cross-run" { 3 } else { 1 };
            if mode == "cross-run" {
                let (_, mut second) = submit(session.as_ref());
                assert_eq!(collect(&mut second).1, None);
            }
            if matches!(mode, "evicted" | "cross-run") {
                let gap = wait(session.resume(ResumeRequest {
                    run_id: id.clone(),
                    after_sequence: 0,
                }))
                .err()
                .unwrap();
                let Error::ReplayGap(gap) = gap else {
                    panic!("expected an explicit replay gap")
                };
                assert_eq!(gap.latest_available, 3);
                assert_eq!(gap.oldest_available, if mode == "evicted" { 2 } else { 0 });
                out["gap"] = json!({"requested_after":gap.requested_after,"oldest_available":gap.oldest_available,"latest_available":gap.latest_available});
            }
            let (recovery, mut following) = replay(session.as_ref(), &id, after);
            let (replayed, error) = collect(&mut following);
            assert_eq!(error, None);
            assert_eq!(
                serde_json::to_value(&replayed).unwrap(),
                serde_json::to_value(&events[after as usize..]).unwrap()
            );
            let (_, mut ended) = replay(session.as_ref(), &id, 3);
            assert!(collect(&mut ended).0.is_empty());
            out["events"] = serde_json::to_value(events).unwrap();
            out["replayed"] = serde_json::to_value(replayed).unwrap();
            out["recovery"] = recovery;
        }
    }
    wait(session.close()).unwrap();
    fixture.reaped();
    if mode == "reopen" {
        let mut open = request();
        open.reopen = true;
        open.native_session_id = "native-session".into();
        let reopened = wait(adapter.open(open)).unwrap();
        assert_eq!(
            wait(reopened.resume(ResumeRequest {
                run_id: id,
                after_sequence: 0
            }))
            .err(),
            Some(Error::RunNotFound)
        );
        out["reopened"] = serde_json::to_value(wait(reopened.state()).unwrap()).unwrap();
        wait(reopened.close()).unwrap();
        fixture.reaped();
    }
    out
}

#[test]
fn public_journal_preserves_replay_and_live_follow_across_its_boundaries() {
    for mode in [
        "default",
        "complete",
        "evicted",
        "cross-run",
        "active",
        "close",
        "overflow",
        "large-close",
        "reopen",
    ] {
        exercise(mode);
    }
}
