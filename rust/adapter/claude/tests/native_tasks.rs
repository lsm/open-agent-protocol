use oxap_adapter_claude::native::{
    decode_observation, Observation, TaskNotificationFrame, TaskUpdatedFrame,
    SYSTEM_TASK_NOTIFICATION, SYSTEM_TASK_PROGRESS, SYSTEM_TASK_STARTED, SYSTEM_TASK_UPDATED,
    TYPE_SYSTEM,
};

const SESSION_ID: &str = "3b926aac-d113-4b86-9dc1-0c36b2013f93";
const TOOL_USE_ID: &str = "toolu_probe01";

fn system(subtype: &str, frame: &str) -> Observation {
    decode_observation(TYPE_SYSTEM, subtype, frame.as_bytes()).unwrap()
}

fn refusal(subtype: &str, frame: &str) -> String {
    decode_observation(TYPE_SYSTEM, subtype, frame.as_bytes())
        .unwrap_err()
        .to_string()
}

fn notification(frame: &str) -> TaskNotificationFrame {
    match system(SYSTEM_TASK_NOTIFICATION, frame) {
        Observation::TaskNotification(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

fn updated(frame: &str) -> TaskUpdatedFrame {
    match system(SYSTEM_TASK_UPDATED, frame) {
        Observation::TaskUpdated(frame) => *frame,
        other => panic!("{other:?}"),
    }
}

#[test]
fn a_task_started_frame_carries_its_task_and_its_tool() {
    match system(
        SYSTEM_TASK_STARTED,
        &format!(
            r#"{{"type":"system","subtype":"task_started","task_id":"task1","description":"research","uuid":"t1","session_id":"{SESSION_ID}","tool_use_id":"{TOOL_USE_ID}","task_type":"local_agent","subagent_type":"Explore","is_backgrounded":true}}"#
        ),
    ) {
        Observation::TaskStarted(frame) => {
            assert_eq!(frame.task_id, "task1");
            assert_eq!(frame.description, "research");
            assert_eq!(frame.tool_use_id, TOOL_USE_ID);
            assert_eq!(frame.task_type, "local_agent");
            assert_eq!(frame.subagent_type, "Explore");
            assert_eq!(frame.is_backgrounded, Some(true));
        }
        other => panic!("{other:?}"),
    }
    for frame in [
        r#"{"type":"system","subtype":"task_started","description":"d","uuid":"t1","session_id":"s"}"#,
        r#"{"type":"system","subtype":"task_started","task_id":"t","uuid":"t1","session_id":"s"}"#,
        r#"{"type":"system","subtype":"task_started","task_id":"t","description":"d","session_id":"s"}"#,
        r#"{"type":"system","subtype":"task_started","task_id":"t","description":"d","uuid":"t1"}"#,
    ] {
        assert_eq!(
            refusal(SYSTEM_TASK_STARTED, frame),
            "claude native: invalid frame for a known type: task_started requires task_id, description, uuid, and session_id",
            "{frame}"
        );
    }
}

#[test]
fn a_task_progress_frame_carries_its_usage() {
    match system(
        SYSTEM_TASK_PROGRESS,
        &format!(
            r#"{{"type":"system","subtype":"task_progress","task_id":"task1","description":"research","usage":{{"total_tokens":10,"tool_uses":2,"duration_ms":100}},"uuid":"t2","session_id":"{SESSION_ID}","tool_use_id":"{TOOL_USE_ID}"}}"#
        ),
    ) {
        Observation::TaskProgress(frame) => {
            assert_eq!(
                (
                    frame.usage.total_tokens,
                    frame.usage.tool_uses,
                    frame.usage.duration_ms
                ),
                (10, 2, 100)
            );
            assert_eq!(frame.tool_use_id, TOOL_USE_ID);
        }
        other => panic!("{other:?}"),
    }
    match system(
        SYSTEM_TASK_PROGRESS,
        r#"{"type":"system","subtype":"task_progress","task_id":"t","description":"d","usage":null,"uuid":"u","session_id":"s"}"#,
    ) {
        Observation::TaskProgress(frame) => assert_eq!(frame.usage.total_tokens, 0),
        other => panic!("{other:?}"),
    }
    assert_eq!(
        refusal(
            SYSTEM_TASK_PROGRESS,
            r#"{"type":"system","subtype":"task_progress","task_id":"t","uuid":"u","session_id":"s"}"#
        ),
        "claude native: invalid frame for a known type: task_progress requires task_id, description, uuid, and session_id"
    );
}

#[test]
fn a_task_notification_settles_a_task_with_a_terminal_status() {
    let done = notification(&format!(
        r#"{{"type":"system","subtype":"task_notification","task_id":"task1","status":"completed","output_file":"/out","summary":"done","uuid":"t2","session_id":"{SESSION_ID}","usage":{{"total_tokens":10,"tool_uses":2,"duration_ms":100}}}}"#
    ));
    assert_eq!(done.status, "completed");
    assert_eq!(done.output_file, "/out");
    assert_eq!(done.usage.map(|usage| usage.tool_uses), Some(2));
    for status in ["failed", "stopped", "killed"] {
        let frame = notification(&format!(
            r#"{{"type":"system","subtype":"task_notification","task_id":"t","status":"{status}","output_file":"/o","summary":"s","uuid":"u","session_id":"s","usage":null}}"#
        ));
        assert_eq!(frame.status, status);
        assert!(frame.usage.is_none());
    }
    assert_eq!(
        refusal(
            SYSTEM_TASK_NOTIFICATION,
            r#"{"type":"system","subtype":"task_notification","task_id":"task1","status":"running","output_file":"/o","summary":"s","uuid":"t3","session_id":"s"}"#
        ),
        "claude native: invalid frame for a known type: task_notification status \"running\" is not completed, failed, or stopped"
    );
}

#[test]
fn a_task_notification_needs_its_output_file_present_but_not_filled() {
    let foreground = notification(&format!(
        r#"{{"type":"system","subtype":"task_notification","task_id":"b1","tool_use_id":"toolu_01","status":"completed","output_file":"","summary":"Wait, then list files","uuid":"t2","session_id":"{SESSION_ID}"}}"#
    ));
    assert_eq!(foreground.output_file, "");
    assert_eq!(foreground.tool_use_id, "toolu_01");
    let null = notification(
        r#"{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed","output_file":null,"summary":"s","uuid":"t2","session_id":"s"}"#,
    );
    assert_eq!(null.output_file, "");
    for frame in [
        r#"{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed","summary":"s","uuid":"t2","session_id":"s"}"#,
        r#"{"type":"system","subtype":"task_notification","task_id":"b1","output_file":"","summary":"s","uuid":"t2","session_id":"s"}"#,
        r#"{"type":"system","subtype":"task_notification","task_id":"b1","status":"completed","output_file":"","uuid":"t2","session_id":"s"}"#,
    ] {
        assert_eq!(
            refusal(SYSTEM_TASK_NOTIFICATION, frame),
            "claude native: invalid frame for a known type: task_notification requires task_id, status, output_file, summary, uuid, and session_id",
            "{frame}"
        );
    }
}

#[test]
fn a_task_updated_frame_is_terminal_when_its_patch_says_so() {
    let killed = updated(&format!(
        r#"{{"type":"system","subtype":"task_updated","task_id":"task1","session_id":"{SESSION_ID}","patch":{{"status":"killed","end_time":123,"error":"boom","is_backgrounded":false}}}}"#
    ));
    assert!(killed.terminal());
    assert_eq!(killed.patch.status, "killed");
    assert_eq!(killed.patch.end_time, Some(123));
    assert_eq!(killed.patch.error, "boom");
    assert_eq!(killed.patch.is_backgrounded, Some(false));
    let running = updated(
        r#"{"type":"system","subtype":"task_updated","task_id":"t","patch":{"status":"running","description":"d"}}"#,
    );
    assert!(!running.terminal());
    assert_eq!(running.patch.description, "d");
    assert!(!updated(r#"{"type":"system","subtype":"task_updated","task_id":"t","patch":null}"#).terminal());
    assert_eq!(
        refusal(
            SYSTEM_TASK_UPDATED,
            r#"{"type":"system","subtype":"task_updated","patch":{"status":"killed"}}"#
        ),
        "claude native: invalid frame for a known type: task_updated requires task_id"
    );
}
