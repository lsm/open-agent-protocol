use std::io::Write;
use std::time::{Duration, Instant};

use oxap_adapter::jsonrpc::{ClientOptions, Dialect, Inbound, RpcClient};

const WAIT: Duration = Duration::from_secs(3);

fn client() -> (RpcClient, std::io::PipeWriter) {
    let (read, write) = std::io::pipe().unwrap();
    let client = RpcClient::new(
        Dialect::lenient("acp"),
        read,
        std::io::sink(),
        ClientOptions {
            queue_capacity: 1,
            queue_stall: Some(Duration::from_millis(200)),
            ..ClientOptions::default()
        },
    );
    (client, write)
}

fn send(writer: &mut impl Write) {
    writeln!(writer, r#"{{"jsonrpc":"2.0","method":"one"}}"#).unwrap();
    writeln!(writer, r#"{{"jsonrpc":"2.0","method":"two"}}"#).unwrap();
}

#[test]
fn a_bounded_inbound_queue_recovers_without_losing_or_reordering_frames() {
    let (client, mut writer) = client();
    let inbound = client.inbound();
    send(&mut writer);
    let deadline = Instant::now() + WAIT;
    while inbound.is_empty() && Instant::now() < deadline {
        std::thread::yield_now();
    }
    assert_eq!(inbound.len(), 1);
    for method in ["one", "two"] {
        let Inbound::Notification(message) = inbound.recv_timeout(WAIT).unwrap() else {
            panic!("not a notification")
        };
        assert_eq!(message.method, method);
    }
    assert!(!client.is_closed());
    client.close();
    drop(writer);
    assert_eq!(
        client.read_done().recv_timeout(WAIT),
        Err(crossbeam_channel::RecvTimeoutError::Disconnected)
    );
}

#[test]
fn a_stalled_inbound_queue_closes_with_an_explicit_overflow() {
    let (client, mut writer) = client();
    let inbound = client.inbound();
    send(&mut writer);
    assert_eq!(
        client.done().recv_timeout(WAIT),
        Err(crossbeam_channel::RecvTimeoutError::Disconnected)
    );
    assert_eq!(
        client.err().as_deref(),
        Some("acp rpc: notification queue is full")
    );
    assert_eq!(inbound.len(), 1);
    drop(writer);
    assert_eq!(
        client.read_done().recv_timeout(WAIT),
        Err(crossbeam_channel::RecvTimeoutError::Disconnected)
    );
}
