use std::io::{BufRead, BufReader, PipeReader, PipeWriter, Write};
use std::time::Duration;

use crossbeam_channel::Receiver;
use oap_adapter_pi::client::Client;
use oap_adapter_pi::native::{Command, ExtensionUiResponse};
use oap_adapter_pi::rpc::{Error, Inbound};
use oap_adapter_pi::rpc_client::{ClientOptions, RpcClient};

const WAIT: Duration = Duration::from_secs(5);

struct Peer {
    client: RpcClient,
    from_client: BufReader<PipeReader>,
    to_client: Option<PipeWriter>,
}

fn peer(options: ClientOptions) -> Peer {
    let (from_server, to_client) = std::io::pipe().unwrap();
    let (from_client, to_server) = std::io::pipe().unwrap();
    Peer {
        client: RpcClient::new(from_server, to_server, options),
        from_client: BufReader::new(from_client),
        to_client: Some(to_client),
    }
}

fn capacity(capacity: usize) -> ClientOptions {
    ClientOptions {
        queue_capacity: capacity,
        write_queue_capacity: capacity,
        ..ClientOptions::default()
    }
}

impl Peer {
    fn read(&mut self) -> serde_json::Value {
        let mut line = String::new();
        self.from_client.read_line(&mut line).unwrap();
        serde_json::from_str(&line).unwrap()
    }

    fn send(&mut self, line: &str) {
        let writer = self.to_client.as_mut().unwrap();
        writer.write_all(line.as_bytes()).unwrap();
        writer.write_all(b"\n").unwrap();
    }
}

fn acknowledge(inbound: Receiver<Inbound>) {
    std::thread::spawn(move || {
        while let Ok(message) = inbound.recv() {
            drop(message);
        }
    });
}

fn call(
    client: &RpcClient,
    command: Command,
) -> std::thread::JoinHandle<Result<Option<String>, Error>> {
    let client = client.clone();
    std::thread::spawn(move || {
        client
            .call(&command, true)
            .map(|raw| raw.map(|raw| raw.get().to_owned()))
    })
}

fn prompt() -> Command {
    Command {
        kind: "prompt".into(),
        message: Some("hello".into()),
        ..Command::default()
    }
}

#[test]
fn inbound_frames_keep_their_order_and_a_response_waits_behind_its_barrier() {
    let mut peer = peer(capacity(8));
    let inbound = peer.client.inbound();
    let result = call(&peer.client, prompt());
    let id = peer.read()["id"].as_str().unwrap().to_owned();
    peer.send(r#"{"type":"agent_start"}"#);
    peer.send(
        r#"{"type":"extension_ui_request","id":"ui","method":"confirm","title":"T","message":"M"}"#,
    );
    peer.send(&format!(
        r#"{{"id":"{id}","type":"response","command":"prompt","success":true}}"#
    ));
    assert!(
        matches!(inbound.recv_timeout(WAIT).unwrap(), Inbound::Event(event) if event.kind == "agent_start")
    );
    assert!(matches!(
        inbound.recv_timeout(WAIT).unwrap(),
        Inbound::ExtensionRequest(_)
    ));
    let Inbound::Barrier(ack) = inbound.recv_timeout(WAIT).unwrap() else {
        panic!("no barrier");
    };
    std::thread::sleep(Duration::from_millis(50));
    assert!(!result.is_finished(), "the response overtook its barrier");
    drop(ack);
    assert_eq!(result.join().unwrap().unwrap(), None);
}

#[test]
fn out_of_order_responses_reach_their_own_calls() {
    let mut peer = peer(capacity(8));
    acknowledge(peer.client.inbound());
    let text = |id: &str| Command {
        id: id.into(),
        kind: "get_last_assistant_text".into(),
        ..Command::default()
    };
    let a = call(&peer.client, text("a"));
    let b = call(&peer.client, text("b"));
    let first = peer.read()["id"].as_str().unwrap().to_owned();
    let second = peer.read()["id"].as_str().unwrap().to_owned();
    for id in [&second, &first] {
        peer.send(&format!(
            r#"{{"id":"{id}","type":"response","command":"get_last_assistant_text","success":true,"data":{{"text":"{id}"}}}}"#
        ));
    }
    assert_eq!(a.join().unwrap().unwrap().unwrap(), r#"{"text":"a"}"#);
    assert_eq!(b.join().unwrap().unwrap().unwrap(), r#"{"text":"b"}"#);
}

#[test]
fn an_ambiguous_response_closes_the_client_and_fails_the_call() {
    for (response, want) in [
        (
            r#"{"id":"other","type":"response","command":"get_state","success":true}"#,
            "pi rpc: response id is not pending: other",
        ),
        (
            r#"{"type":"response","command":"get_state","success":true}"#,
            "pi rpc: response id is not pending: empty",
        ),
        (
            r#"{"id":"x","type":"response","command":"abort","success":true}"#,
            "pi rpc: response command does not match request: got abort want get_state",
        ),
    ] {
        let mut peer = peer(capacity(4));
        let result = call(
            &peer.client,
            Command {
                id: "x".into(),
                ..Command::of("get_state")
            },
        );
        let _ = peer.read();
        peer.send(response);
        let _ = peer.client.done().recv_timeout(WAIT);
        assert_eq!(peer.client.err().as_deref(), Some(want));
        assert!(result.join().unwrap().is_err());
    }
}

#[test]
fn a_completed_id_is_never_reused_and_a_refusal_is_a_remote_error() {
    let mut peer = peer(capacity(4));
    acknowledge(peer.client.inbound());
    let fixed = || Command {
        id: "fixed".into(),
        ..Command::of("get_state")
    };
    let first = call(&peer.client, fixed());
    let _ = peer.read();
    peer.send(r#"{"id":"fixed","type":"response","command":"get_state","success":true}"#);
    first.join().unwrap().unwrap();
    assert_eq!(
        peer.client.call(&fixed(), false).unwrap_err().to_string(),
        "pi rpc: request id is already active: fixed"
    );
    let refused = call(&peer.client, prompt());
    let id = peer.read()["id"].as_str().unwrap().to_owned();
    peer.send(&format!(
        r#"{{"id":"{id}","type":"response","command":"prompt","success":false,"error":"rejected"}}"#
    ));
    let error = refused.join().unwrap().unwrap_err();
    assert!(matches!(&error, Error::Remote { message, .. } if message == "rejected"));
    assert_eq!(
        error.to_string(),
        format!("pi rpc error for {id} (prompt): rejected")
    );
}

#[test]
fn generated_ids_are_distinct() {
    let mut peer = peer(capacity(4));
    acknowledge(peer.client.inbound());
    let first = call(&peer.client, Command::of("get_state"));
    let one = peer.read()["id"].as_str().unwrap().to_owned();
    peer.send(&format!(
        r#"{{"id":"{one}","type":"response","command":"get_state","success":true}}"#
    ));
    first.join().unwrap().unwrap();
    let second = call(&peer.client, Command::of("get_state"));
    let two = peer.read()["id"].as_str().unwrap().to_owned();
    peer.send(&format!(
        r#"{{"id":"{two}","type":"response","command":"get_state","success":true}}"#
    ));
    second.join().unwrap().unwrap();
    assert_ne!(one, two);
    assert_eq!((one.as_str(), two.as_str()), ("req_1", "req_2"));
}

#[test]
fn malformed_output_and_a_stalled_queue_close_the_client() {
    let mut garbage = peer(capacity(1));
    garbage.send("garbage");
    let _ = garbage.client.done().recv_timeout(WAIT);
    assert!(garbage
        .client
        .err()
        .unwrap()
        .starts_with("pi rpc: invalid JSONL frame: "));
    let mut stalled = peer(ClientOptions {
        queue_stall: Some(Duration::from_millis(50)),
        ..capacity(1)
    });
    let _inbound = stalled.client.inbound();
    stalled.send(r#"{"type":"agent_start"}"#);
    stalled.send(r#"{"type":"agent_settled"}"#);
    let _ = stalled.client.done().recv_timeout(WAIT);
    assert_eq!(
        stalled.client.err().as_deref(),
        Some("pi rpc: inbound queue is full")
    );
}

#[test]
fn end_of_stream_fails_a_pending_call_with_eof() {
    let mut peer = peer(capacity(2));
    let result = call(&peer.client, Command::of("get_state"));
    let _ = peer.read();
    peer.to_client.take();
    assert_eq!(result.join().unwrap().unwrap_err().to_string(), "EOF");
}

#[test]
fn a_response_parked_behind_its_barrier_is_delivered_when_the_client_closes() {
    let mut peer = peer(capacity(8));
    let inbound = peer.client.inbound();
    let result = call(&peer.client, Command::of("get_state"));
    let id = peer.read()["id"].as_str().unwrap().to_owned();
    peer.client
        .respond(&ExtensionUiResponse {
            kind: "extension_ui_response".into(),
            id: "sync".into(),
            cancelled: true,
            ..ExtensionUiResponse::default()
        })
        .unwrap();
    let _ = peer.read();
    peer.send(&format!(
        r#"{{"id":"{id}","type":"response","command":"get_state","success":true}}"#
    ));
    let Inbound::Barrier(_ack) = inbound.recv_timeout(WAIT).unwrap() else {
        panic!("no barrier");
    };
    peer.client.close().unwrap();
    assert_eq!(result.join().unwrap().unwrap(), None);
    assert!(!peer
        .client
        .err()
        .unwrap_or_default()
        .contains("not pending"));
}

struct Stuck(Receiver<()>, crossbeam_channel::Sender<()>);

impl Write for Stuck {
    fn write(&mut self, _: &[u8]) -> std::io::Result<usize> {
        let _ = self.1.try_send(());
        let _ = self.0.recv();
        Err(std::io::ErrorKind::BrokenPipe.into())
    }

    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

#[test]
fn a_write_blocked_with_no_closer_returns_once_the_reader_retires() {
    let (from_server, mut to_client) = std::io::pipe().unwrap();
    let (release, stuck) = crossbeam_channel::bounded::<()>(0);
    let (entered, entering) = crossbeam_channel::bounded(1);
    let client = RpcClient::new(from_server, Stuck(stuck, entered), capacity(8));
    let result = call(&client, Command::of("get_state"));
    entering.recv_timeout(WAIT).unwrap();
    to_client.write_all(b"{not json\n").unwrap();
    let deadline = std::time::Instant::now() + WAIT;
    while !result.is_finished() && std::time::Instant::now() < deadline {
        std::thread::sleep(Duration::from_millis(10));
    }
    assert!(result.is_finished(), "the call hung inside a stuck write");
    assert!(result.join().unwrap().is_err());
    drop(release);
}
