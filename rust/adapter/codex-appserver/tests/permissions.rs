use oap_adapter_codex_appserver::native::{ThreadResumeParams, ThreadResumeResponse};
use oap_adapter_codex_appserver::permissions::confirm;

fn sent(cwd: &str, approval_policy: &str, sandbox: &str) -> ThreadResumeParams {
    ThreadResumeParams {
        thread_id: "t-1".into(),
        cwd: cwd.into(),
        approval_policy: approval_policy.into(),
        sandbox: sandbox.into(),
        ..ThreadResumeParams::default()
    }
}

fn answered(json: &str) -> ThreadResumeResponse {
    serde_json::from_str(json).unwrap()
}

#[test]
fn a_resume_under_the_configured_permissions_is_confirmed() {
    for (cwd, answer) in [
        (
            "/work",
            r#"{"cwd":"/work/./","approvalPolicy":"never","sandbox":{"type":"readOnly","writableRoots":[]}}"#,
        ),
        (
            "/a/b/../c",
            r#"{"cwd":"/a/c","approvalPolicy":"never","sandbox":{"type":"readOnly"}}"#,
        ),
        (
            "/work/",
            r#"{"cwd":"/x/../work","approvalPolicy":"never","sandbox":{"type":"readOnly"}}"#,
        ),
    ] {
        assert_eq!(
            confirm(&sent(cwd, "never", "read-only"), &answered(answer)),
            Ok(()),
            "{answer}"
        );
    }
    for (sandbox, policy) in [
        ("workspace-write", "workspaceWrite"),
        ("danger-full-access", "dangerFullAccess"),
    ] {
        let answer = format!(r#"{{"sandbox":{{"type":"{policy}"}}}}"#);
        assert_eq!(confirm(&sent("", "", sandbox), &answered(&answer)), Ok(()));
    }
    assert_eq!(confirm(&sent("", "", ""), &answered("{}")), Ok(()));
}

#[test]
fn a_sandbox_the_adapter_cannot_map_or_that_codex_changed_is_refused() {
    assert_eq!(
        confirm(
            &sent("", "", "custom"),
            &answered(r#"{"sandbox":{"type":"custom"}}"#)
        ),
        Err(
            "the configured sandbox custom has no thread/resume policy the adapter can confirm"
                .into()
        )
    );
    for (answer, shown) in [
        (
            r#"{"sandbox":{"type":"workspaceWrite"}}"#,
            r#"{"type":"workspaceWrite"}"#,
        ),
        (r#"{"sandbox":"readOnly"}"#, r#""readOnly""#),
        (r#"{"sandbox":{"type":7}}"#, r#"{"type":7}"#),
        (r#"{"sandbox":null}"#, "null"),
        ("{}", ""),
    ] {
        assert_eq!(
            confirm(&sent("", "", "read-only"), &answered(answer)),
            Err(format!(
                "Codex resumed the thread under sandbox {shown}, not the configured read-only"
            )),
            "{answer}"
        );
    }
}

#[test]
fn an_approval_policy_codex_changed_is_refused() {
    for (answer, shown) in [
        (r#"{"approvalPolicy":"on-request"}"#, r#""on-request""#),
        (
            r#"{"approvalPolicy":{"granular":{}}}"#,
            r#"{"granular":{}}"#,
        ),
        (r#"{"approvalPolicy":null}"#, "null"),
        ("{}", ""),
    ] {
        assert_eq!(
            confirm(&sent("", "never", ""), &answered(answer)),
            Err(format!(
                "Codex resumed the thread under approval policy {shown}, not the configured never"
            )),
            "{answer}"
        );
    }
}

#[test]
fn a_working_directory_codex_changed_is_refused_with_both_quoted() {
    assert_eq!(
        confirm(
            &sent("/work", "", ""),
            &answered(r#"{"cwd":"/work/../elsewhere"}"#)
        ),
        Err(
            r#"Codex resumed the thread in "/work/../elsewhere", not the configured "/work""#
                .into()
        )
    );
    assert_eq!(
        confirm(&sent("/work", "", ""), &answered("{}")),
        Err(r#"Codex resumed the thread in "", not the configured "/work""#.into())
    );
}
