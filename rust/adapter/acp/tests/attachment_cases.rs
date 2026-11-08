use std::path::PathBuf;
use std::process::Command;
use std::sync::atomic::{AtomicU64, Ordering};

use serde_json::{json, Value};

#[path = "support/attachments.rs"]
mod support;

static NEXT: AtomicU64 = AtomicU64::new(0);
struct Fixture(PathBuf);
impl Drop for Fixture {
    fn drop(&mut self) {
        std::fs::remove_dir_all(&self.0).unwrap();
    }
}
fn source() -> Value {
    json!({"id":"filesystem","kind":"process","protocol":"mcp","display_name":"Files","endpoint":"stdio:fixture","command":"/synthetic/never-executed-mcp","args":["--root","/workspace"],"environment":["TOKEN","MISSING","MODE=readonly"]})
}
fn run(mut input: Value, native: Value) -> Value {
    let fixture = Fixture(std::env::temp_dir().join(format!(
        "oap-mcp-{}-{}",
        std::process::id(),
        NEXT.fetch_add(1, Ordering::Relaxed)
    )));
    std::fs::create_dir(&fixture.0).unwrap();
    std::fs::write(fixture.0.join("native.json"), native.to_string()).unwrap();
    let python = Command::new("python3")
        .args(["-c", "import sys; print(sys.executable)"])
        .output()
        .unwrap();
    assert!(python.status.success());
    input["python"] = json!(String::from_utf8(python.stdout).unwrap().trim());
    input["agent"] = json!(PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("../../../.claude/skills/verify/scripts/fixtures/acp-attachments-agent.py"));
    input["case"] = json!(fixture.0);
    support::run(&input)
}

#[test]
fn stdio_attachments_use_only_the_explicit_environment_and_publish_descriptors() {
    for (reopen, caps, method) in [
        (false, json!({}), "session/new"),
        (true, json!({"loadSession":true}), "session/load"),
        (
            true,
            json!({"sessionCapabilities":{"resume":{}}}),
            "session/resume",
        ),
    ] {
        let observed = run(
            json!({"reopen":reopen,"attachments":[source()],"environment":["TOKEN=operator","UNREQUESTED=private"]}),
            json!({"caps":caps}),
        );
        assert!(observed["results"][0]["admit"].is_null());
        let state = &observed["results"][0]["open"]["state"];
        assert_eq!(
            state["sources"],
            json!([{"id":"filesystem","kind":"process","protocol":"mcp","display_name":"Files","endpoint":"stdio:fixture"}]),
            "{method}"
        );
        assert_eq!(state["status"], "idle");
        assert_eq!(observed["spawned"], true);
    }
}

#[test]
fn invalid_or_conflicting_sources_refuse_before_any_native_process_starts() {
    let cases = [
        (json!({"id":"","kind":"process","command":"x"}), json!([])),
        (
            json!({"id":"same","kind":"process","command":"x"}),
            json!([{"name":"same","command":"fixed"}]),
        ),
        (
            json!({"id":"remote","kind":"http","command":"x"}),
            json!([]),
        ),
        (json!({"id":"missing-kind","command":"x"}), json!([])),
        (
            json!({"id":"dup-env","kind":"process","command":"x","environment":["TOKEN","TOKEN=value"]}),
            json!([]),
        ),
    ];
    for (attachment, configured) in cases {
        let observed = run(
            json!({"attachments":[attachment],"configured":configured}),
            json!({}),
        );
        assert_eq!(observed["spawned"], false);
        let failure = &observed["results"][0]["open"]["failure"];
        assert_eq!(failure["feature"], "action.tool_sources.attach");
        assert_eq!(failure["reason"], "unsatisfiable");
        assert_eq!(&observed["results"][0]["admit"], failure);
    }
    let observed = run(
        json!({"attachments":[{"id":"no-command","kind":"process"}]}),
        json!({}),
    );
    assert_eq!(
        observed["results"][0]["open"]["failure"]["kind"],
        "invalid_resolution"
    );
    assert_eq!(observed["spawned"], false);
    let observed = run(json!({"attachments":[source(),source()]}), json!({}));
    assert_eq!(
        observed["results"][0]["open"]["failure"]["source"],
        "filesystem"
    );
    assert_eq!(observed["spawned"], false);
}

#[test]
fn per_open_attachments_do_not_mutate_configured_sources_or_later_sessions() {
    let mut second = source();
    second["id"] = json!("second");
    let observed = run(
        json!({"configured":[{"name":"fixed","command":"operator-mcp"}],"requests":[{"session_id":"a","attachments":[source()]},{"session_id":"b","attachments":[second]}]}),
        json!({}),
    );
    assert_eq!(
        observed["descriptor"]["sources"],
        json!([{"id":"fixed","kind":"process","protocol":"mcp"}])
    );
    assert_eq!(
        observed["results"][0]["open"]["state"]["sources"][1]["id"],
        "filesystem"
    );
    assert_eq!(
        observed["results"][1]["open"]["state"]["sources"][1]["id"],
        "second"
    );
}

#[test]
fn dropping_an_open_after_the_native_mcp_request_reaps_new_and_reload_peers() {
    for reopen in [false, true] {
        assert_eq!(
            run(
                json!({"reopen":reopen,"cancel":true,"attachments":[source()]}),
                json!({"quiet":true})
            )["results"][0]["open"],
            json!({"cancelled":true})
        );
    }
}
