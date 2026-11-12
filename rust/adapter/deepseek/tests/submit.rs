#![cfg(unix)]

use std::path::PathBuf;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::time::Duration;

use oap_adapter::{testkit, Error, EventStream, OpenRequest};
use oap_adapter_deepseek::adapter::{pin, Adapter, Config};
use oap_adapter_deepseek::native::{SERVER_NAME, SERVER_VERSION};
use oap_adapter_deepseek::process::ProcessConfig;
use oap_adapter_deepseek::runtime::Runtime;
use oap_adapter_deepseek::session::Session;
use oap_protocol::{
    Admission, ContentDeltaPayload, EffectiveDeliveryMode, EnvelopeType, Message, MessageContent,
    MessageRole, MessageSubmitRequest, MessageSubmitResponse, Participant, RequestedDeliveryMode,
    RunCompletedPayload, RunFailedPayload, RunStartedPayload, RunStatus, SessionStatus,
    SETTLED_BY_INFERRED,
};
use serde_json::Value;

static SPAWNING: Mutex<()> = Mutex::new(());
static NEXT: AtomicU64 = AtomicU64::new(0);

struct Fixture {
    _serial: MutexGuard<'static, ()>,
    dir: PathBuf,
}

impl Drop for Fixture {
    fn drop(&mut self) {
        let _ = std::fs::write(self.dir.join("release"), "");
        let _ = std::fs::remove_dir_all(&self.dir);
    }
}

fn notify(method: &str, params: &str) -> String {
    format!(
        "printf '%s\\n' '{{\"jsonrpc\":\"2.0\",\"method\":\"{method}\",\"params\":{params}}}'\n"
    )
}

fn event(seq: i64, kind: &str, data: &str) -> String {
    notify(
        "session.event",
        &format!(
            r#"{{"sessionId":"s","event":{{"type":"{kind}","seq":{seq},"time":1,"data":{data}}}}}"#
        ),
    )
}

fn proof() -> String {
    [
        event(
            1,
            "agent/inbox/spliced",
            r#"{"target":"next-turn","start":0,"inserted":[{"id":"receipt","role":"user","content":[],"source":{"kind":"user"}}]}"#,
        ),
        event(2, "turn/start", r#"{"turn":1}"#),
        event(3, "step/start", r#"{"turn":1,"step":1}"#),
        event(
            4,
            "user/message",
            r#"{"id":"receipt","role":"user","content":[],"source":{"kind":"user"}}"#,
        ),
    ]
    .concat()
}

fn answer(result: &str) -> String {
    format!("printf '{{\"jsonrpc\":\"2.0\",\"id\":%s,{result}}}\\n' \"$id\"\n")
}

fn receipt() -> String {
    answer(r#""result":{"messageId":"receipt"}"#)
}

impl Fixture {
    fn new() -> Self {
        let serial = SPAWNING.lock().unwrap_or_else(|poison| poison.into_inner());
        let dir = std::env::temp_dir().join(format!(
            "oap-deepseek-submit-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::SeqCst)
        ));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Self {
            _serial: serial,
            dir,
        }
    }

    fn open(&self, on_prompt: &str) -> Arc<Session> {
        let dir = self.dir.to_string_lossy().into_owned();
        let info = format!(
            r#"{{"serverInfo":{{"name":"{SERVER_NAME}","version":"{}"}}}}"#,
            *SERVER_VERSION
        );
        let script = format!(
            r#"while read line; do
printf '%s\n' "$line" >> "{dir}/wire"
id=$(printf '%s' "$line" | sed -n 's/.*"id":\([0-9]*\).*/\1/p')
case "$line" in
*'"method":"initialize"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{info}}}\n' "$id" ;;
*'"method":"session/prompt"'*)
{on_prompt};;
*'"method":"shutdown"'*) printf '{{"jsonrpc":"2.0","id":%s,"result":{{}}}}\n' "$id"; exit 0 ;;
esac
done"#
        );
        Adapter::new(Config {
            process: ProcessConfig {
                path: "/bin/sh".into(),
                args: vec!["-c".into(), script, "sh".into()],
                dir,
                env: Some(vec!["PATH=/usr/bin:/bin".into()]),
                shutdown_timeout: Some(Duration::from_secs(2)),
                ..ProcessConfig::default()
            },
            provider: "deepseek".into(),
            model: "m".into(),
            runtime: Runtime::new(|| 42, |kind| format!("{kind}-minted")),
            ..Config::default()
        })
        .unwrap()
        .open(OpenRequest {
            session_id: "s".into(),
            participant: Participant {
                id: "user".into(),
                ..Participant::default()
            },
            ..OpenRequest::default()
        })
        .map(Arc::new)
        .unwrap()
    }

    fn until_released(&self) -> String {
        let dir = self.dir.display();
        format!(
            "n=0; until [ -f \"{dir}/release\" ] || [ ! -d \"{dir}\" ] || [ $n -ge 200 ]; do sleep 0.05; n=$((n+1)); done\n"
        )
    }

    fn held_until_released(&self) -> String {
        self.until_released() + "exit 0\n"
    }

    fn wait_for(&self, name: &str) {
        let deadline = std::time::Instant::now() + Duration::from_secs(5);
        while !self.dir.join(name).exists() && std::time::Instant::now() < deadline {
            std::thread::sleep(Duration::from_millis(10));
        }
        assert!(self.dir.join(name).exists(), "{name} never appeared");
    }

    fn release(&self) {
        std::fs::write(self.dir.join("release"), "").unwrap();
    }

    fn prompts(&self) -> Vec<Value> {
        std::fs::read_to_string(self.dir.join("wire"))
            .unwrap_or_default()
            .lines()
            .map(|line| serde_json::from_str::<Value>(line).unwrap())
            .filter(|frame| frame["method"] == "session/prompt")
            .collect()
    }
}

fn hello(session: &str) -> MessageSubmitRequest {
    MessageSubmitRequest {
        session_id: session.into(),
        messages: vec![Message {
            id: Some("m1".into()),
            role: MessageRole::USER,
            content: MessageContent::from_text("hello"),
            ..Message::default()
        }],
        ..MessageSubmitRequest::default()
    }
}

fn submit(
    session: &Arc<Session>,
    request: MessageSubmitRequest,
) -> Result<(MessageSubmitResponse, EventStream), Error> {
    let (send, outcome) = std::sync::mpsc::channel();
    let session = session.clone();
    std::thread::spawn(move || {
        let _ = send.send(session.submit(request));
    });
    outcome
        .recv_timeout(Duration::from_secs(5))
        .expect("submit neither admitted nor refused the run")
}

fn harness(error: Error) -> String {
    match error {
        Error::Harness(text) => text,
        other => panic!("not a harness error: {other:?}"),
    }
}

#[test]
fn submit_refuses_another_sessions_id_and_a_closed_session() {
    let fixture = Fixture::new();
    let session = fixture.open(&receipt());
    assert_eq!(submit(&session, hello("t")).err(), Some(Error::RunNotFound));
    session.close().unwrap();
    assert_eq!(
        submit(&session, hello("s")).err(),
        Some(Error::SessionClosed)
    );
    assert!(fixture.prompts().is_empty());
}

#[test]
fn a_prompt_the_runtime_refuses_fails_submit_and_leaves_the_session_idle() {
    let fixture = Fixture::new();
    let session = fixture.open(&answer(r#""error":{"code":-32000,"message":"busy"}"#));
    let refused = harness(submit(&session, hello("s")).err().unwrap());
    assert!(refused.contains("busy"), "{refused}");
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
}

#[test]
fn a_prompt_response_without_a_message_id_retires_the_session() {
    let fixture = Fixture::new();
    let session = fixture.open(&answer(r#""result":{}"#));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: prompt response omitted messageId"
    );
    assert_eq!(session.state().err(), Some(Error::SessionClosed));
}

fn settles_closed(session: &Session) -> bool {
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while std::time::Instant::now() < deadline {
        if session.state().err() == Some(Error::SessionClosed) {
            return true;
        }
        std::thread::sleep(Duration::from_millis(10));
    }
    false
}

#[test]
fn a_runtime_that_exits_while_the_prompt_is_pending_fails_submit() {
    let fixture = Fixture::new();
    let session = fixture.open("exit 0\n");
    assert!(submit(&session, hello("s")).is_err());
    assert!(settles_closed(&session), "{:?}", session.state());
}

#[test]
fn a_notification_while_the_prompt_is_pending_does_not_retire_the_session() {
    let fixture = Fixture::new();
    let reply = event(1, "turn/start", r#"{"turn":1}"#)
        + &answer(r#""error":{"code":-32000,"message":"busy"}"#);
    let session = fixture.open(&reply);
    let refused = harness(submit(&session, hello("s")).err().unwrap());
    assert!(refused.contains("busy"), "{refused}");
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
}

type Submitted = Result<(MessageSubmitResponse, EventStream), Error>;

fn pending(fixture: &Fixture, session: &Arc<Session>) -> std::sync::mpsc::Receiver<Submitted> {
    let (send, first) = std::sync::mpsc::channel();
    let session = session.clone();
    std::thread::spawn(move || {
        let _ = send.send(session.submit(hello("s")));
    });
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while fixture.prompts().is_empty() && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert_eq!(fixture.prompts().len(), 1);
    first
}

#[test]
fn a_close_is_refused_and_a_second_submit_waits_while_the_prompt_is_pending() {
    let fixture = Fixture::new();
    let session = fixture.open(&fixture.held_until_released());
    let first = pending(&fixture, &session);
    assert_eq!(session.close().err(), Some(Error::RunActive));
    let (send, second) = std::sync::mpsc::channel();
    {
        let session = session.clone();
        std::thread::spawn(move || {
            let _ = send.send(session.submit(hello("s")));
        });
    }
    assert!(second.recv_timeout(Duration::from_millis(200)).is_err());
    fixture.release();
    assert!(first
        .recv_timeout(Duration::from_secs(5))
        .expect("the released submit did not return")
        .is_err());
    assert!(second
        .recv_timeout(Duration::from_secs(5))
        .expect("the second submit did not return")
        .is_err());
}

#[test]
fn close_is_refused_after_the_runtime_answers_the_prompt_with_its_receipt() {
    let fixture = Fixture::new();
    let answered = format!("touch \"{}/answered\"\n", fixture.dir.display());
    let session = fixture.open(&(receipt() + &answered + &fixture.held_until_released()));
    let first = pending(&fixture, &session);
    let deadline = std::time::Instant::now() + Duration::from_secs(5);
    while !fixture.dir.join("answered").exists() && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert!(fixture.dir.join("answered").exists());
    assert_eq!(session.close().err(), Some(Error::RunActive));
    fixture.release();
    assert!(first
        .recv_timeout(Duration::from_secs(5))
        .expect("the released submit did not return")
        .is_err());
}

#[test]
fn a_status_for_another_session_before_the_receipt_fails_submit() {
    let fixture = Fixture::new();
    let foreign = notify(
        "session.status",
        r#"{"sessionId":"other","status":"running"}"#,
    );
    let session = fixture.open(&(foreign + &receipt()));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: session.status for foreign session"
    );
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    session.close().unwrap();
}

#[test]
fn an_event_for_another_session_after_the_receipt_fails_submit() {
    let fixture = Fixture::new();
    let foreign = notify(
        "session.event",
        r#"{"sessionId":"other","event":{"type":"turn/start","seq":1,"time":1,"data":{"turn":1}}}"#,
    );
    let session = fixture.open(&(receipt() + &foreign));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: session.event for foreign session"
    );
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
    session.close().unwrap();
}

#[test]
fn a_foreign_or_recursive_subagent_before_the_receipt_fails_submit() {
    let fixture = Fixture::new();
    let recursive = notify(
        "subagent.started",
        r#"{"parentSessionId":"s","childSessionId":"s"}"#,
    );
    let session = fixture.open(&(recursive + &receipt()));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: foreign or recursive subagent"
    );
}

#[test]
fn a_second_submit_waits_for_the_first_prompt_and_keeps_its_own_run() {
    let fixture = Fixture::new();
    let dir = fixture.dir.display().to_string();
    let first = format!("touch \"{dir}/first\"\n")
        + &notify(
            "session.status",
            r#"{"sessionId":"other","status":"running"}"#,
        )
        + &format!("touch \"{dir}/foreign\"\n")
        + &fixture.until_released()
        + &answer(r#""error":{"code":-32000,"message":"first refused"}"#);
    let second = receipt() + "exit 0\n";
    let session = fixture.open(&format!(
        "if [ -f \"{dir}/first\" ]; then\n{second}else\n{first}fi\n"
    ));
    let first = pending(&fixture, &session);
    fixture.wait_for("foreign");
    std::thread::sleep(Duration::from_millis(200));
    let (send, second) = std::sync::mpsc::channel();
    {
        let session = session.clone();
        std::thread::spawn(move || {
            let _ = send.send(session.submit(hello("s")));
        });
    }
    std::thread::sleep(Duration::from_millis(200));
    fixture.release();
    let first = harness(
        first
            .recv_timeout(Duration::from_secs(5))
            .expect("the first submit did not return")
            .err()
            .unwrap(),
    );
    assert!(first.contains("first refused"), "{first}");
    let second = harness(
        second
            .recv_timeout(Duration::from_secs(5))
            .expect("the second submit did not return")
            .err()
            .unwrap(),
    );
    assert!(!second.contains("first refused"), "{second}");
    assert!(
        second.starts_with("deepseek adapter: invalid native protocol observation: "),
        "{second}"
    );
}

#[test]
fn submit_admits_the_run_once_the_runtime_proves_the_turn() {
    let fixture = Fixture::new();
    let session = fixture.open(&(receipt() + &proof()));
    let (response, mut stream) = submit(&session, hello("s")).unwrap();
    assert!(response.accepted);
    assert_eq!(response.session_id.as_str(), "s");
    assert_eq!(response.submission_id.as_str(), "receipt");
    assert_eq!(response.requested_delivery, RequestedDeliveryMode::AUTO);
    assert_eq!(response.effective_delivery, EffectiveDeliveryMode::START);
    assert_eq!(response.delivery_resolution, "session_idle");
    assert_eq!(response.admission, Admission::STARTED);
    assert_eq!(
        response.run_id.as_ref().map(|id| id.as_str()),
        Some("run-minted")
    );
    assert_eq!(response.status, RunStatus::RUNNING);
    assert_eq!(response.model_id, "m");
    assert_eq!(response.message_ids, ["m1".into()]);
    let prompts = fixture.prompts();
    assert_eq!(
        prompts[0]["params"],
        serde_json::json!({"sessionId":"s","contentBlocks":[{"type":"text","text":"hello"}]})
    );
    let started = testkit::next(&mut stream, Duration::from_secs(5));
    assert_eq!(started.envelope_type, EnvelopeType::RUN_STARTED);
    assert_eq!(started.sequence, Some(1));
    assert_eq!(started.timestamp_ms, Some(42));
    assert_eq!(started.session_id.as_ref().map(|id| id.as_str()), Some("s"));
    assert_eq!(
        started.run_id.as_ref().map(|id| id.as_str()),
        Some("run-minted")
    );
    assert_eq!(
        started.capability_revision.as_deref(),
        Some(pin().capability_revision.as_str())
    );
    let payload: RunStartedPayload = started.decode_payload().unwrap();
    assert_eq!(payload.status, RunStatus::RUNNING);
    assert_eq!(payload.model_id, "m");
    assert_eq!(payload.started_at_ms, 42);
    let state = session.state().unwrap();
    assert_eq!(state.status, SessionStatus::RUNNING);
    assert_eq!(
        state.active_run_id.as_ref().map(|id| id.as_str()),
        Some("run-minted")
    );
    assert_eq!(state.transcript_cursor, "1");
    assert_eq!(submit(&session, hello("s")).err(), Some(Error::RunActive));
}

#[test]
fn notifications_ahead_of_the_prompt_response_still_admit_the_run() {
    let fixture = Fixture::new();
    let session = fixture.open(&(proof() + &receipt()));
    let (response, _) = submit(&session, hello("s")).unwrap();
    assert_eq!(response.admission, Admission::STARTED);
}

#[test]
fn a_turn_that_ends_without_ownership_proof_fails_submit() {
    let fixture = Fixture::new();
    let turn = [
        event(1, "turn/start", r#"{"turn":1}"#),
        event(2, "step/start", r#"{"turn":1,"step":1}"#),
        event(
            3,
            "user/message",
            r#"{"id":"receipt","role":"user","content":[],"source":{"kind":"user"}}"#,
        ),
        event(4, "turn/end", r#"{"turn":1,"reason":{"kind":"completed"}}"#),
    ]
    .concat();
    let session = fixture.open(&(receipt() + &turn));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: submission ownership proof failed"
    );
    assert_eq!(session.state().unwrap().status, SessionStatus::IDLE);
}

#[test]
fn a_native_sequence_that_goes_backwards_before_admission_fails_submit() {
    let fixture = Fixture::new();
    let replayed = [
        event(2, "turn/start", r#"{"turn":1}"#),
        event(2, "step/start", r#"{"turn":1,"step":1}"#),
    ]
    .concat();
    let session = fixture.open(&(receipt() + &replayed));
    assert_eq!(
        harness(submit(&session, hello("s")).err().unwrap()),
        "deepseek adapter: invalid native protocol observation: non-monotonic native event sequence"
    );
}

#[test]
fn close_is_refused_while_a_run_is_running_and_dropping_the_session_shuts_it_down() {
    let fixture = Fixture::new();
    let pid = format!("printf '%s' $$ > \"{}/pid\"\n", fixture.dir.display());
    let session = fixture.open(&(pid + &receipt() + &proof()));
    let (_, _stream) = submit(&session, hello("s")).unwrap();
    assert_eq!(session.close().err(), Some(Error::RunActive));
    drop(session);
    let pid = std::fs::read_to_string(fixture.dir.join("pid")).unwrap();
    let alive = std::process::Command::new("/bin/kill")
        .args(["-0", &pid])
        .output()
        .unwrap()
        .status
        .success();
    assert!(!alive, "runtime {pid} outlived its dropped session");
}

fn fails_after_admission(fixture: &Fixture, violation: &str) -> (Arc<Session>, RunFailedPayload) {
    let session = fixture.open(&(receipt() + &proof() + &fixture.until_released() + violation));
    let (response, mut stream) = submit(&session, hello("s")).unwrap();
    let started = testkit::next(&mut stream, Duration::from_secs(5));
    assert_eq!(started.envelope_type, EnvelopeType::RUN_STARTED);
    fixture.release();
    let failed = testkit::next(&mut stream, Duration::from_secs(5));
    assert_eq!(failed.envelope_type, EnvelopeType::RUN_FAILED);
    assert_eq!(failed.sequence, Some(2));
    assert_eq!(failed.run_id, response.run_id);
    let payload: RunFailedPayload = failed.decode_payload().unwrap();
    assert_eq!(payload.session_id.as_str(), "s");
    assert_eq!(payload.run_id.as_str(), "run-minted");
    (session, payload)
}

#[test]
fn a_violation_after_admission_fails_the_started_run_with_gos_code_and_frees_the_session() {
    let cases = [
        (
            event(5, "turn/end", r#"{"turn":1,"reason":{"kind":"completed"}}"#)
                .replace(r#""sessionId":"s""#, r#""sessionId":"other""#),
            "deepseek_session_mismatch",
            "session.event for foreign session",
        ),
        (
            notify("session.status", r#"{"sessionId":"other","status":"idle"}"#),
            "deepseek_session_mismatch",
            "session.status for foreign session",
        ),
        (
            event(4, "turn/end", r#"{"turn":1,"reason":{"kind":"completed"}}"#),
            "deepseek_invalid_sequence",
            "non-monotonic native event sequence",
        ),
        (
            notify(
                "subagent.started",
                r#"{"parentSessionId":"other","childSessionId":"c"}"#,
            ),
            "deepseek_external_activity",
            "foreign or recursive subagent",
        ),
    ];
    for (violation, code, message) in cases {
        let fixture = Fixture::new();
        let (session, payload) = fails_after_admission(&fixture, &violation);
        assert_eq!(payload.error.code, code, "{violation}");
        assert_eq!(payload.error.message, message, "{violation}");
        assert_eq!(payload.settled_by, "", "{violation}");
        let state = session.state().unwrap();
        assert_eq!(state.status, SessionStatus::IDLE, "{violation}");
        assert_eq!(state.active_run_id, None, "{violation}");
        assert_eq!(state.transcript_cursor, "2", "{violation}");
        session.close().unwrap();
    }
}

#[test]
fn a_reverse_request_after_admission_fails_the_started_run_and_retires_the_session() {
    let fixture = Fixture::new();
    let request = r#"printf '%s\n' '{"jsonrpc":"2.0","id":"r1","method":"session/permission","params":{}}'
"#;
    let (session, payload) = fails_after_admission(&fixture, request);
    assert_eq!(payload.error.code, "deepseek_external_activity");
    assert_eq!(payload.error.message, "reverse request");
    assert!(settles_closed(&session));
    session.close().unwrap();
}

#[test]
fn a_runtime_that_exits_after_admission_fails_the_started_run_as_inferred() {
    let fixture = Fixture::new();
    let (session, payload) = fails_after_admission(&fixture, "exit 0\n");
    assert_eq!(payload.error.code, "deepseek_process_exit");
    assert_eq!(payload.settled_by, SETTLED_BY_INFERRED);
    assert!(settles_closed(&session));
    session.close().unwrap();
}

fn reply(seq: i64, step: i64) -> String {
    event(
        seq,
        "assistant/message",
        &format!(
            r#"{{"turn":1,"step":{step},"message":{{"id":"a","role":"assistant","content":[{{"type":"text","text":"hi"}}],"source":{{"kind":"model","provider":"p","model":"m"}}}},"stream":[{{"type":"text-chunks","time0":0,"index":0,"dt":[5],"texts":["h","i"]}}],"usage":{{"inputTokens":3,"outputTokens":2}}}}"#
        ),
    )
}

fn turn_end(seq: i64, turn: i64, kind: &str) -> String {
    event(
        seq,
        "turn/end",
        &format!(r#"{{"turn":{turn},"reason":{{"kind":"{kind}"}}}}"#),
    )
}

fn idle() -> String {
    notify("session.status", r#"{"sessionId":"s","status":"idle"}"#)
}

fn completes(held: bool) {
    let fixture = Fixture::new();
    let hold = if held {
        fixture.until_released()
    } else {
        String::new()
    };
    let tail = reply(5, 1)
        + &event(6, "step/end", r#"{"turn":1,"step":1}"#)
        + &turn_end(7, 1, "completed")
        + &idle();
    let session = fixture.open(&(receipt() + &proof() + &hold + &tail));
    let (response, mut stream) = submit(&session, hello("s")).unwrap();
    fixture.release();
    let envelopes = testkit::drain(&mut stream, Duration::from_secs(5));
    let kinds: Vec<_> = envelopes.iter().map(|e| e.envelope_type.clone()).collect();
    assert_eq!(
        kinds,
        [
            EnvelopeType::RUN_STARTED,
            EnvelopeType::CONTENT_DELTA,
            EnvelopeType::CONTENT_DELTA,
            EnvelopeType::RUN_COMPLETED
        ],
        "held {held}"
    );
    for (envelope, text) in envelopes[1..3].iter().zip(["h", "i"]) {
        let delta: ContentDeltaPayload = envelope.decode_payload().unwrap();
        assert_eq!(delta.part.text, text, "held {held}");
        assert_eq!(
            delta.message_id,
            Some("message-minted".into()),
            "held {held}"
        );
    }
    let completed = &envelopes[3];
    assert_eq!(completed.sequence, Some(4), "held {held}");
    assert_eq!(completed.run_id, response.run_id, "held {held}");
    let payload: RunCompletedPayload = completed.decode_payload().unwrap();
    assert_eq!(payload.stop_reason, "completed", "held {held}");
    assert_eq!(payload.final_response.id, Some("message-minted".into()));
    assert_eq!(payload.final_response.role, MessageRole::ASSISTANT);
    assert_eq!(payload.final_response.content.text().as_deref(), Some("hi"));
    let usage = payload.usage.unwrap();
    assert_eq!(
        (usage.input_tokens, usage.output_tokens, usage.total_tokens),
        (Some(3), Some(2), Some(5)),
        "held {held}"
    );
    let state = session.state().unwrap();
    assert_eq!(state.status, SessionStatus::IDLE, "held {held}");
    assert_eq!(state.active_run_id, None, "held {held}");
    assert_eq!(state.transcript_cursor, "4", "held {held}");
    session.close().unwrap();
}

#[test]
fn a_completed_turn_that_goes_idle_after_submit_returns_completes_the_run() {
    completes(true);
}

#[test]
fn a_completed_turn_stays_running_until_the_runtime_reports_idle() {
    let fixture = Fixture::new();
    let tail = reply(5, 1) + &turn_end(6, 1, "completed");
    let session = fixture.open(&(receipt() + &proof() + &fixture.until_released() + &tail));
    let (_, mut stream) = submit(&session, hello("s")).unwrap();
    fixture.release();
    let kinds: Vec<_> = (0..3)
        .map(|_| testkit::next(&mut stream, Duration::from_secs(5)).envelope_type)
        .collect();
    assert_eq!(
        kinds,
        [
            EnvelopeType::RUN_STARTED,
            EnvelopeType::CONTENT_DELTA,
            EnvelopeType::CONTENT_DELTA
        ]
    );
    std::thread::sleep(Duration::from_millis(300));
    assert_eq!(session.state().unwrap().status, SessionStatus::RUNNING);
    assert_eq!(session.close().err(), Some(Error::RunActive));
}

#[test]
fn a_turn_that_does_not_complete_fails_the_run_with_its_kind() {
    for kind in ["blocked", "max-tokens", "interrupted"] {
        let fixture = Fixture::new();
        let (session, payload) = fails_after_admission(&fixture, &(turn_end(5, 1, kind) + &idle()));
        assert_eq!(payload.error.code, format!("deepseek_{kind}"));
        assert_eq!(payload.error.message, format!("native turn ended: {kind}"));
        session.close().unwrap();
    }
}

#[test]
fn a_completed_turn_without_an_assistant_message_fails_the_run() {
    let fixture = Fixture::new();
    let (session, payload) =
        fails_after_admission(&fixture, &(turn_end(5, 1, "completed") + &idle()));
    assert_eq!(payload.error.code, "deepseek_missing_final_message");
    assert_eq!(
        payload.error.message,
        "completed turn omitted assistant message"
    );
    session.close().unwrap();
}

#[test]
fn an_event_that_breaks_the_turn_grammar_fails_the_run() {
    let cases = [
        (reply(5, 2), "event outside open owned step"),
        (
            event(5, "step/end", r#"{"turn":1,"step":2}"#),
            "invalid step end",
        ),
        (
            event(5, "step/start", r#"{"turn":1,"step":1}"#),
            "invalid step start",
        ),
        (
            event(5, "turn/start", r#"{"turn":2}"#),
            "overlapping foreign turn",
        ),
        (turn_end(5, 2, "completed"), "invalid turn end"),
    ];
    for (violation, message) in cases {
        let fixture = Fixture::new();
        let (session, payload) = fails_after_admission(&fixture, &violation);
        assert_eq!(
            payload.error.code, "deepseek_invalid_grammar",
            "{violation}"
        );
        assert_eq!(payload.error.message, message, "{violation}");
        session.close().unwrap();
    }
}
