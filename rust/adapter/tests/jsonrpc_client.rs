use std::io::{BufRead, BufReader, PipeReader, PipeWriter, Write};
use std::time::{Duration, Instant};

use oap_adapter::jsonrpc::{
    ClientOptions, Dialect, Error, Fault, Inbound, Kind, Message, RequestId, RpcClient,
};
use serde_json::value::RawValue;

const D: Dialect = Dialect::lenient("acp");

const WAIT: Duration = Duration::from_secs(2);

struct Peer {
    client: RpcClient,
    from_client: BufReader<PipeReader>,
    to_client: Option<PipeWriter>,
}

fn peer(strict: bool) -> Peer {
    let (from_server, to_client) = std::io::pipe().unwrap();
    let (from_client, to_server) = std::io::pipe().unwrap();
    let client = RpcClient::new(
        D,
        from_server,
        to_server,
        ClientOptions {
            queue_capacity: 8,
            strict_response_ids: strict,
            ..ClientOptions::default()
        },
    );
    Peer {
        client,
        from_client: BufReader::new(from_client),
        to_client: Some(to_client),
    }
}

impl Peer {
    fn read(&mut self) -> Message {
        let mut line = String::new();
        self.from_client.read_line(&mut line).unwrap();
        Message::parse(&D, line.trim_end_matches('\n').as_bytes()).unwrap()
    }

    fn send(&mut self, line: &str) {
        let writer = self.to_client.as_mut().unwrap();
        writer.write_all(line.as_bytes()).unwrap();
        writer.write_all(b"\n").unwrap();
    }

    fn hang_up(&mut self) {
        self.to_client.take();
    }
}

fn raw(text: &str) -> Option<Box<RawValue>> {
    Some(RawValue::from_string(text.into()).unwrap())
}

fn call_in_thread(
    client: &RpcClient,
    id: RequestId,
    method: &'static str,
) -> std::thread::JoinHandle<Result<String, Error>> {
    let client = client.clone();
    std::thread::spawn(move || {
        client
            .call_id(id, method, None, None, None)
            .map(|raw| raw.get().to_owned())
    })
}

#[test]
fn out_of_order_answers_reach_their_own_calls_across_id_domains() {
    let mut peer = peer(true);
    let text = call_in_thread(&peer.client, RequestId::String("same".into()), "string");
    let first = peer.read();
    let number = call_in_thread(&peer.client, RequestId::Integer(7), "integer");
    let second = peer.read();
    assert_eq!(first.method, "string");
    assert_eq!(second.id, Some(RequestId::Integer(7)));
    peer.send(r#"{"jsonrpc":"2.0","id":7,"result":"seven"}"#);
    peer.send(r#"{"jsonrpc":"2.0","id":"same","result":"text"}"#);
    assert_eq!(number.join().unwrap().unwrap(), r#""seven""#);
    assert_eq!(text.join().unwrap().unwrap(), r#""text""#);
}

#[test]
fn a_remote_error_names_its_code_and_request() {
    let mut peer = peer(true);
    let call = call_in_thread(&peer.client, RequestId::Integer(3), "session/prompt");
    peer.read();
    peer.send(
        r#"{"jsonrpc":"2.0","id":3,"error":{"code":-32603,"message":"fixture prompt failure"}}"#,
    );
    let error = call.join().unwrap().unwrap_err();
    assert_eq!(error.remote_code(), Some(-32603));
    assert_eq!(
        error.to_string(),
        "acp rpc error -32603 for request 3: fixture prompt failure"
    );
}

#[test]
fn calls_number_their_requests_from_one() {
    let mut peer = peer(true);
    let client = peer.client.clone();
    let call = std::thread::spawn(move || client.call("initialize", raw("{}"), None));
    let request = peer.read();
    assert_eq!(request.id, Some(RequestId::Integer(1)));
    assert_eq!(request.kind, Kind::Request);
    peer.send(r#"{"jsonrpc":"2.0","id":1,"result":{}}"#);
    assert_eq!(call.join().unwrap().unwrap().get(), "{}");
}

#[test]
fn notifications_and_reverse_requests_arrive_inbound_and_a_response_is_written() {
    let mut peer = peer(true);
    let inbound = peer.client.inbound();
    peer.send(r#"{"jsonrpc":"2.0","method":"session/update","params":{"a":1}}"#);
    peer.send(r#"{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{}}"#);
    let Inbound::Notification(notification) = inbound.recv_timeout(WAIT).unwrap() else {
        panic!("expected a notification");
    };
    assert_eq!(notification.method, "session/update");
    let Inbound::Request(request) = inbound.recv_timeout(WAIT).unwrap() else {
        panic!("expected a request");
    };
    request
        .respond(&serde_json::json!({"outcome":{"outcome":"cancelled"}}))
        .unwrap();
    assert!(matches!(
        request.respond_error(-1, "again"),
        Err(Error {
            fault: Fault::ReverseRequestResolved,
            ..
        })
    ));
    let response = peer.read();
    assert_eq!(response.kind, Kind::Response);
    assert_eq!(response.id, Some(RequestId::Integer(9)));
}

#[test]
fn a_response_waits_until_every_earlier_inbound_message_is_drained() {
    let mut peer = peer(true);
    let inbound = peer.client.inbound();
    let call = call_in_thread(&peer.client, RequestId::Integer(1), "session/prompt");
    peer.read();
    peer.send(r#"{"jsonrpc":"2.0","method":"session/update","params":{}}"#);
    peer.send(r#"{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}"#);
    assert!(matches!(
        inbound.recv_timeout(WAIT).unwrap(),
        Inbound::Notification(_)
    ));
    std::thread::sleep(Duration::from_millis(50));
    assert!(!call.is_finished(), "the response overtook the barrier");
    let Inbound::Barrier(ack) = inbound.recv_timeout(WAIT).unwrap() else {
        panic!("expected the barrier");
    };
    drop(ack);
    assert!(call.join().unwrap().is_ok());
}

#[test]
fn end_of_stream_fails_pending_calls_and_signals_done() {
    let mut peer = peer(true);
    let call = call_in_thread(&peer.client, RequestId::Integer(1), "session/prompt");
    peer.read();
    peer.hang_up();
    assert!(call.join().unwrap().is_err());
    assert!(peer.client.done().recv_timeout(WAIT).is_err());
    assert_eq!(peer.client.err().as_deref(), Some("EOF"));
    assert!(peer.client.call("x", None, None).is_err());
}

#[test]
fn a_strict_client_closes_on_an_unmatched_response() {
    let mut peer = peer(true);
    peer.send(r#"{"jsonrpc":"2.0","id":42,"result":null}"#);
    assert!(peer.client.done().recv_timeout(WAIT).is_err());
    assert_eq!(
        peer.client.err().as_deref(),
        Some("acp rpc: response id is not pending: 42")
    );
}

#[test]
fn a_lenient_client_ignores_an_unmatched_response() {
    let mut peer = peer(false);
    peer.send(r#"{"jsonrpc":"2.0","id":42,"result":null}"#);
    let call = call_in_thread(&peer.client, RequestId::Integer(1), "ping");
    peer.read();
    peer.send(r#"{"jsonrpc":"2.0","id":1,"result":true}"#);
    assert_eq!(call.join().unwrap().unwrap(), "true");
}

#[test]
fn a_reverse_request_id_reused_while_active_closes_the_client() {
    let mut peer = peer(true);
    let _inbound = peer.client.inbound();
    peer.send(r#"{"jsonrpc":"2.0","id":5,"method":"a","params":{}}"#);
    peer.send(r#"{"jsonrpc":"2.0","id":5,"method":"b","params":{}}"#);
    assert!(peer.client.done().recv_timeout(WAIT).is_err());
    assert_eq!(
        peer.client.err().as_deref(),
        Some("acp rpc: request id is already active: 5")
    );
}

#[test]
fn a_malformed_frame_closes_the_client_with_its_reason() {
    let mut peer = peer(true);
    peer.send(r#"{"jsonrpc":"2.0","id":1,"id":2,"result":null}"#);
    assert!(peer.client.done().recv_timeout(WAIT).is_err());
    assert!(peer
        .client
        .err()
        .unwrap()
        .starts_with("acp rpc: invalid JSON-RPC message: duplicate object key"));
}

#[test]
fn a_call_past_its_deadline_closes_the_client() {
    let mut peer = peer(true);
    let started = Instant::now();
    let deadline = Some(started + Duration::from_millis(100));
    let client = peer.client.clone();
    let call = std::thread::spawn(move || client.call("slow", None, deadline));
    peer.read();
    assert!(matches!(
        call.join().unwrap(),
        Err(Error {
            fault: Fault::DeadlineExceeded,
            ..
        })
    ));
    assert!(started.elapsed() < WAIT);
    assert!(peer.client.is_closed());
}

#[test]
fn close_is_idempotent_and_keeps_the_first_reason() {
    let peer = peer(true);
    peer.client.close();
    peer.client.close();
    assert_eq!(peer.client.err().as_deref(), Some("acp rpc: client closed"));
    assert!(matches!(
        peer.client.call("x", None, None),
        Err(Error {
            fault: Fault::Closed,
            ..
        })
    ));
}

struct Stuck(crossbeam_channel::Receiver<()>);

impl Write for Stuck {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        let _ = self.0.recv();
        Err(std::io::ErrorKind::BrokenPipe.into())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn a_close_returns_a_call_whose_frame_is_stuck_in_a_write_with_no_closer_to_unblock_it() {
    let (from_server, to_client) = std::io::pipe().unwrap();
    let (release, stuck) = crossbeam_channel::bounded::<()>(0);
    let client = RpcClient::new(D, from_server, Stuck(stuck), ClientOptions::default());
    let caller = client.clone();
    let (answered, answer) = crossbeam_channel::bounded(1);
    std::thread::spawn(move || {
        let _ = answered.send(caller.call("session/prompt", None, None));
    });
    std::thread::sleep(Duration::from_millis(100));
    client.close();
    let outcome = answer
        .recv_timeout(WAIT)
        .expect("the call returned after close");
    assert!(matches!(outcome.unwrap_err().fault, Fault::Closed));
    drop(release);
    drop(to_client);
}

#[test]
fn a_frame_the_writer_cannot_encode_fails_its_call_with_the_encoder_s_own_fault() {
    let strict = Dialect::strict("deepseek");
    let (from_server, _to_client) = std::io::pipe().unwrap();
    let (_from_client, to_server) = std::io::pipe().unwrap();
    let client = RpcClient::new(
        strict,
        from_server,
        to_server,
        ClientOptions {
            frame_limit: 8,
            ..ClientOptions::default()
        },
    );
    let refused = client.call("session.status", None, None).unwrap_err();
    assert!(matches!(refused.fault, Fault::FrameTooLarge), "{refused}");
}

#[test]
fn a_call_reports_its_admission_through_an_unbuffered_channel_once_and_then_closes_it() {
    let mut peer = peer(false);
    let (started, admitted) = crossbeam_channel::bounded(0);
    let caller = peer.client.clone();
    let call = std::thread::spawn(move || caller.call_started("session/prompt", None, started));
    std::thread::sleep(Duration::from_millis(100));
    assert_eq!(admitted.recv_timeout(WAIT).unwrap(), None);
    let request = peer.read();
    peer.send(&format!(
        r#"{{"jsonrpc":"2.0","id":{},"result":{{}}}}"#,
        request.id.unwrap()
    ));
    assert_eq!(call.join().unwrap().unwrap().get(), "{}");
    assert!(admitted.recv_timeout(WAIT).is_err());
}

#[test]
fn a_response_being_routed_when_the_client_closes_still_reaches_its_call() {
    let mut peer = peer(false);
    let inbound = peer.client.inbound();
    let call = call_in_thread(&peer.client, RequestId::Integer(1), "session/prompt");
    let request = peer.read();
    assert_eq!(request.id, Some(RequestId::Integer(1)));
    peer.send(r#"{"jsonrpc":"2.0","id":1,"result":{"stopReason":"end_turn"}}"#);
    let Inbound::Barrier(_ack) = inbound.recv_timeout(WAIT).unwrap() else {
        panic!("the response did not park behind a barrier");
    };
    peer.client.close();
    assert_eq!(
        call.join().unwrap().unwrap(),
        r#"{"stopReason":"end_turn"}"#
    );
}

#[test]
fn a_call_closed_while_the_reader_decodes_is_failed_when_the_reader_then_errors() {
    let (from_server, mut to_client) = std::io::pipe().unwrap();
    let (from_client, to_server) = std::io::pipe().unwrap();
    let client = RpcClient::new(
        D,
        from_server,
        to_server,
        ClientOptions {
            closer: Some(Box::new(|| {})),
            ..ClientOptions::default()
        },
    );
    let mut from_client = BufReader::new(from_client);
    let call = call_in_thread(&client, RequestId::Integer(1), "session/prompt");
    let mut line = String::new();
    from_client.read_line(&mut line).unwrap();
    client.close();
    to_client.write_all(b"{not json\n").unwrap();
    let deadline = Instant::now() + WAIT;
    while !call.is_finished() && Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert!(call.is_finished(), "the deferred call was never failed");
    assert!(matches!(
        call.join().unwrap().unwrap_err().fault,
        Fault::Closed
    ));
}

#[test]
fn a_duplicate_id_reported_to_a_slow_reader_does_not_hold_the_client() {
    let mut peer = peer(false);
    let first = call_in_thread(&peer.client, RequestId::Integer(9), "session/prompt");
    let _ = peer.read();
    let (started, admitted) = crossbeam_channel::bounded(0);
    let duplicate = peer.client.clone();
    let refused = std::thread::spawn(move || {
        duplicate.call_id(
            RequestId::Integer(9),
            "session/prompt",
            None,
            None,
            Some(started),
        )
    });
    std::thread::sleep(Duration::from_millis(100));
    peer.send(r#"{"jsonrpc":"2.0","id":9,"result":{}}"#);
    assert_eq!(first.join().unwrap().unwrap(), "{}");
    assert!(admitted.recv_timeout(WAIT).unwrap().is_some());
    assert!(refused.join().unwrap().is_err());
}
