use std::process::Command;
use std::time::{Duration, Instant};

use super::{Client, ClientError};

fn exited_client() -> Client {
    let mut command = Command::new("python3");
    command.args(["-u", "-c", "import os; os._exit(0)"]);
    let mut client = Client::spawn(&mut command, Duration::from_secs(2)).unwrap();
    client.close_input();
    assert!(client
        .process
        .wait_exit(Duration::from_secs(2))
        .unwrap()
        .success());
    assert!(client
        .process
        .next(Duration::from_secs(2))
        .unwrap()
        .is_none());
    client
}

#[test]
fn delivered_eof_survives_a_caller_pause_past_the_original_drain_budget() {
    let mut client = exited_client();
    let mut paused = false;
    let result = client.wait_for(Duration::from_secs(2), true, "endpoint EOF", |client| {
        if !paused {
            std::thread::sleep(Duration::from_secs(2));
            paused = true;
        }
        client.eof.then_some(())
    });
    client.close().unwrap();
    assert!(result.is_ok(), "already delivered EOF became {result:?}");
    assert!(client.closed_by_runner().is_none());
    assert!(client.wait().unwrap().success());
}

#[test]
fn an_empty_closed_endpoint_is_gone_at_the_expired_response_boundary() {
    let mut client = exited_client();
    let result = client.wait_for(Duration::ZERO, true, "answer to missing", |_| None::<()>);
    client.close().unwrap();
    assert!(
        matches!(result, Err(ClientError::EndpointGone)),
        "{result:?}"
    );
    assert!(client.closed_by_runner().is_none());
}

#[test]
fn a_buffered_reply_precedes_a_later_bad_line_at_expiry() {
    let mut command = Command::new("python3");
    command.args([
        "-u",
        "-c",
        "print('{\"protocol\":\"open-agent-protocol\",\"type\":\"capabilities.response\",\"id\":\"answer\",\"in_reply_to\":\"request\",\"payload\":{}}',flush=True); print('{',flush=True)",
    ]);
    let mut client = Client::spawn(&mut command, Duration::from_secs(2)).unwrap();
    client.close_input();
    assert!(client
        .process
        .wait_exit(Duration::from_secs(2))
        .unwrap()
        .success());
    let start = Instant::now();
    while client.process.buffered() != 2 {
        assert!(start.elapsed() < Duration::from_secs(2));
        std::thread::yield_now();
    }
    let reply = client.wait_for(Duration::ZERO, true, "answer to request", |client| {
        client.responses.pop_front()
    });
    let following = client.event();
    client.close().unwrap();
    assert_eq!(reply.unwrap().id.as_str(), "answer");
    assert!(matches!(
        following,
        Err(ClientError::Process(crate::ProcessError::Wire(_)))
    ));
    assert!(client.closed_by_runner().is_none());
}
