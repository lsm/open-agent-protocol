#![allow(
    clippy::unwrap_used,
    clippy::expect_used,
    clippy::panic,
    clippy::indexing_slicing
)]

mod common;

use std::time::Duration;

use futures::StreamExt;
use oap_sdk::{Client, ClientBuilder, Error, ExecutionRequest, ListModelsRequest, StreamErrorKind};

#[tokio::test]
async fn connects_and_completes_the_handshake() {
    let client = common::fake_client("ok").await;
    assert!(!client.is_closed());
    client.close().await;
    assert!(client.is_closed());
}

#[tokio::test]
async fn a_missing_handshake_times_out_rather_than_hanging() {
    let started = tokio::time::Instant::now();
    let error = common::fake_builder("no_handshake")
        .handshake_timeout(Duration::from_millis(300))
        .connect()
        .await
        .unwrap_err();
    assert!(
        matches!(
            error,
            Error::Stream {
                kind: StreamErrorKind::TransportError,
                ..
            }
        ),
        "{error:?}"
    );
    assert!(
        error
            .to_string()
            .contains("timed out waiting for protocol.initialize.request"),
        "{error}"
    );
    assert!(started.elapsed() < Duration::from_secs(3));
}

#[tokio::test]
async fn a_version_mismatch_fails_fast_with_a_typed_code() {
    let error = common::fake_builder("bad_version")
        .connect()
        .await
        .unwrap_err();
    assert_eq!(error.code(), Some("protocol_mismatch"));
}

#[tokio::test]
async fn an_expected_version_can_be_overridden() {
    let client = common::fake_builder("bad_version")
        .expected_protocol_version("99")
        .connect()
        .await
        .expect("the fake advertises 99");
    client.close().await;
}

#[tokio::test]
async fn an_error_frame_in_place_of_the_handshake_is_a_protocol_error() {
    let error = common::fake_builder("handshake_error")
        .connect()
        .await
        .unwrap_err();
    assert!(matches!(error, Error::Protocol { .. }), "{error:?}");
    assert_eq!(error.code(), Some("startup_failed"));
}

#[tokio::test]
async fn garbage_lines_are_discarded_without_breaking_the_stream() {
    let client = common::fake_client("garbage_then_ready").await;
    let models = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .expect("models still work after a garbage line");
    assert_eq!(models.models.len(), 1);
    client.close().await;
}

#[tokio::test]
async fn a_missing_binary_is_a_clear_transport_error() {
    let error = ClientBuilder::new()
        .command("/definitely/not/here/makai")
        .connect()
        .await
        .unwrap_err();
    assert!(matches!(error, Error::Transport { .. }), "{error:?}");
    assert!(
        error.to_string().contains("/definitely/not/here/makai"),
        "{error}"
    );
}

#[tokio::test]
async fn a_child_that_dies_mid_request_fails_the_call_rather_than_hanging() {
    let client = common::fake_client("die_on_request").await;
    let started = tokio::time::Instant::now();
    let error = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .unwrap_err();
    assert!(
        matches!(
            error,
            Error::Stream {
                kind: StreamErrorKind::TransportError,
                ..
            }
        ),
        "{error:?}"
    );
    assert!(
        error.to_string().contains("exit") || error.to_string().contains("stdio stream ended"),
        "{error}"
    );
    assert!(started.elapsed() < Duration::from_secs(2));
    assert!(client.is_closed());
    client.close().await;
}

#[tokio::test]
async fn a_dead_child_fails_later_calls_immediately() {
    let client = common::fake_client("die_on_request").await;
    let _ = client.models().list(ListModelsRequest::default()).await;

    tokio::time::sleep(Duration::from_millis(100)).await;
    let started = tokio::time::Instant::now();
    let error = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .unwrap_err();
    assert!(started.elapsed() < Duration::from_millis(500), "{error}");
    client.close().await;
}

#[tokio::test]
async fn a_silent_runtime_times_out_per_call() {
    let client = common::fake_builder("silent")
        .response_timeout(Duration::from_millis(200))
        .connect()
        .await
        .expect("connects");
    let error = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .unwrap_err();
    assert!(error.to_string().contains("timed out"), "{error}");

    assert!(!client.is_closed());
    client.close().await;
}

#[tokio::test]
async fn concurrent_calls_multiplex_over_one_transport() {
    let client = common::fake_client("ok").await;
    let models = client.models();

    let calls = (0..8).map(|_| {
        let models = models.clone();
        async move { models.list(ListModelsRequest::default()).await }
    });
    let results = futures::future::join_all(calls).await;

    for result in results {
        assert_eq!(result.expect("each call settles").models.len(), 1);
    }
    client.close().await;
}

#[cfg(unix)]
#[tokio::test]
async fn closing_the_client_reaps_the_child() {
    let pid_file = tempfile::NamedTempFile::new().expect("temp file");
    let client = common::fake_builder("ok")
        .env(
            "OAP_SDK_FAKE_PID_FILE",
            pid_file.path().display().to_string(),
        )
        .connect()
        .await
        .expect("connects");
    let pid = wait_for_pid(pid_file.path()).await;
    assert!(process_is_alive(pid), "the fake should be running");

    client.close().await;
    assert!(client.is_closed());

    assert!(!process_is_alive(pid), "the child should be reaped");
}

#[cfg(unix)]
#[tokio::test]
async fn dropping_the_client_terminates_the_child() {
    let pid_file = tempfile::NamedTempFile::new().expect("temp file");
    let pid = {
        let _client = common::fake_builder("ok")
            .env(
                "OAP_SDK_FAKE_PID_FILE",
                pid_file.path().display().to_string(),
            )
            .connect()
            .await
            .expect("connects");
        let pid = wait_for_pid(pid_file.path()).await;
        assert!(process_is_alive(pid));
        pid
    };

    for _ in 0..40 {
        if !process_is_alive(pid) {
            return;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("dropping the client left the child {pid} running");
}

#[cfg(unix)]
#[tokio::test]
async fn dropping_a_client_mid_stream_terminates_the_child() {
    let pid_file = tempfile::NamedTempFile::new().expect("temp file");
    let pid = {
        let client = common::fake_builder("provider_slow")
            .env(
                "OAP_SDK_FAKE_PID_FILE",
                pid_file.path().display().to_string(),
            )
            .connect()
            .await
            .expect("connects");
        let pid = wait_for_pid(pid_file.path()).await;

        let mut events = Box::pin(client.provider().stream(ExecutionRequest::prompt(
            "anthropic/anthropic-messages@claude-sonnet-4-5",
            "hi",
        )));
        events.next().await.expect("an event").expect("no failure");
        pid
    };

    for _ in 0..40 {
        if !process_is_alive(pid) {
            return;
        }
        tokio::time::sleep(Duration::from_millis(50)).await;
    }
    panic!("dropping a client mid-stream left the child {pid} running");
}

#[cfg(unix)]
async fn wait_for_pid(path: &std::path::Path) -> u32 {
    for _ in 0..100 {
        if let Ok(text) = std::fs::read_to_string(path) {
            if let Ok(pid) = text.trim().parse() {
                return pid;
            }
        }
        tokio::time::sleep(Duration::from_millis(20)).await;
    }
    panic!("the fake never reported its pid");
}

#[cfg(unix)]
fn process_is_alive(pid: u32) -> bool {
    std::process::Command::new("ps")
        .args(["-o", "stat=", "-p", &pid.to_string()])
        .output()
        .map(|output| {
            let stat = String::from_utf8_lossy(&output.stdout);
            let stat = stat.trim();
            !stat.is_empty() && !stat.starts_with('Z')
        })
        .unwrap_or(false)
}

#[tokio::test]
async fn calls_after_close_fail_instead_of_hanging() {
    let client = common::fake_client("ok").await;
    client.close().await;

    let error = client
        .models()
        .list(ListModelsRequest::default())
        .await
        .unwrap_err();
    assert!(error.to_string().contains("closed"), "{error}");

    let error = client
        .provider()
        .complete(ExecutionRequest::prompt("p/a@m", "hi"))
        .await
        .unwrap_err();
    assert!(error.to_string().contains("closed"), "{error}");
}

#[tokio::test]
async fn close_is_idempotent() {
    let client = common::fake_client("ok").await;
    client.close().await;
    client.close().await;
    assert!(client.is_closed());
}

#[tokio::test]
async fn namespace_handles_outlive_the_client_value() {
    let provider = {
        let client = common::fake_client("ok").await;
        client.provider()
    };
    let response = provider
        .complete(ExecutionRequest::prompt(
            "anthropic/anthropic-messages@claude-sonnet-4-5",
            "hi",
        ))
        .await
        .expect("the transport outlives the client value");
    assert_eq!(response.text(), "provider works");
}

#[tokio::test]
async fn the_builder_controls_the_child_environment() {
    let error = ClientBuilder::new()
        .command(common::fake_binary())
        .args(Vec::<String>::new())
        .env_clear()
        .env("OAP_SDK_FAKE_SCENARIO", "handshake_error")
        .handshake_timeout(Duration::from_millis(2_000))
        .connect()
        .await
        .unwrap_err();
    assert_eq!(error.code(), Some("startup_failed"));
}

#[tokio::test]
async fn the_client_is_debuggable_without_leaking_internals() {
    let client: Client = common::fake_client("ok").await;
    let rendered = format!("{client:?}");
    assert!(rendered.contains("Client"), "{rendered}");
    client.close().await;
}

#[tokio::test]
async fn an_endpoint_refusing_provider_describe_runs_agents_and_refuses_provider_calls() {
    for scenario in ["agent_only", "agent_only_unsupported"] {
        let client = common::fake_client(scenario).await;
        let request = ExecutionRequest::prompt("fixture/openai-responses@mock", "hello");
        let agent = client
            .agent()
            .run(request.clone())
            .await
            .expect("agent run");
        assert_eq!(agent.text(), "agent works", "{scenario}");
        let models = client
            .models()
            .list(ListModelsRequest::default())
            .await
            .unwrap_err();
        assert_eq!(models.code(), Some("unsupported_feature"), "{scenario}");
        let completed = client
            .provider()
            .complete(request.clone())
            .await
            .unwrap_err();
        assert_eq!(completed.code(), Some("unsupported_feature"), "{scenario}");
        let streamed = Box::pin(client.provider().stream(request))
            .next()
            .await
            .expect("one item");
        assert_eq!(
            streamed.unwrap_err().code(),
            Some("unsupported_feature"),
            "{scenario}"
        );
        client.close().await;
    }
}

#[tokio::test]
async fn a_provider_describe_failing_for_any_reason_but_a_refusal_fails_the_connection() {
    let error = common::fake_builder("describe_fails")
        .connect()
        .await
        .unwrap_err();
    assert_eq!(error.code(), Some("internal"));
    assert_eq!(error.to_string(), "provider registry unavailable");
}

#[tokio::test]
async fn a_malformed_provider_describe_fails_the_connection_rather_than_reading_as_agent_only() {
    let error = common::fake_builder("describe_garbage")
        .handshake_timeout(Duration::from_millis(300))
        .connect()
        .await
        .unwrap_err();
    assert!(
        error
            .to_string()
            .contains("timed out waiting for provider.describe.request"),
        "{error}"
    );
}
