use std::path::Path;

use oap_adapter_copilot::native::{decode_event, parse, Kind};
use oap_adapter_copilot::CORPUS_DIRECTORY;
use serde::Deserialize;
use serde_json::value::RawValue;

#[derive(Deserialize)]
struct Item {
    action: String,
    raw: Box<RawValue>,
}

#[test]
fn every_message_in_the_pinned_corpus_parses_to_the_kind_its_action_names() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../..")
        .join(&*CORPUS_DIRECTORY);
    #[derive(Deserialize)]
    struct Case {
        path: String,
    }
    #[derive(Deserialize)]
    struct Manifest {
        cases: Vec<Case>,
    }
    let manifest: Manifest =
        serde_json::from_str(&std::fs::read_to_string(root.join("manifest.json")).unwrap())
            .unwrap();
    assert!(!manifest.cases.is_empty(), "the manifest lists no cases");
    for case in &manifest.cases {
        let native = root.join(&case.path).join("native.jsonl");
        let lines = std::fs::read_to_string(&native)
            .unwrap_or_else(|failure| panic!("{}: {failure}", native.display()));
        for line in lines.lines().filter(|line| !line.trim().is_empty()) {
            let item: Item = serde_json::from_str(line).unwrap();
            let frame = item.raw.get().as_bytes();
            let parsed = parse(frame);
            if item.action == "reject-wire" {
                assert!(parsed.is_err(), "accepted {}", item.raw.get());
                continue;
            }
            let message = parsed.unwrap_or_else(|_| panic!("refused {}", item.raw.get()));
            let want = match item.action.as_str() {
                "admit" => Kind::Request,
                "bind" => Kind::Response,
                _ => Kind::Notification,
            };
            assert_eq!(message.kind, want, "{}", item.raw.get());
            let event = decode_event(&message);
            match item.action.as_str() {
                "reject-event" => assert!(event.is_err(), "accepted {}", item.raw.get()),
                "observe" => {
                    let event = event.unwrap().expect("an observed frame is an event");
                    assert!(!event.kind.is_empty() && !event.session_id.is_empty());
                }
                _ => assert!(event.is_ok(), "{}", item.raw.get()),
            }
        }
    }
}

#[test]
fn an_event_keeps_native_names_and_exact_numbers_and_other_frames_are_not_events() {
    let message = parse(br#"{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"native-session","event":{"type":"session.future","ephemeral":true,"data":{"count":18446744073709551615}}}}"#).unwrap();
    let event = decode_event(&message).unwrap().unwrap();
    assert_eq!(event.kind, "session.future");
    assert!(event.ephemeral);
    assert_eq!(event.data["count"].get(), "18446744073709551615");
    for frame in [
        &br#"{"jsonrpc":"2.0","method":"session.lifecycle","params":{}}"#[..],
        br#"{"jsonrpc":"2.0","id":1,"result":{}}"#,
    ] {
        assert!(decode_event(&parse(frame).unwrap()).unwrap().is_none());
    }
}

#[test]
fn a_frame_must_carry_a_valid_id_one_outcome_and_a_nonempty_method_or_error() {
    for frame in [
        "[]",
        "null",
        r#"{"jsonrpc":"1.0","id":1,"result":{}}"#,
        r#"{"jsonrpc":"2.0","result":{}}"#,
        r#"{"jsonrpc":"2.0","id":null,"result":{}}"#,
        r#"{"jsonrpc":"2.0","id":1.5,"result":{}}"#,
        r#"{"jsonrpc":"2.0","id":true,"result":{}}"#,
        r#"{"jsonrpc":"2.0","method":""}"#,
        r#"{"jsonrpc":"2.0","method":"m","result":{}}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":1}}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":1,"message":""}}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":"1","message":"no"}}"#,
        r#"{"jsonrpc":"2.0","id":1,"id":2,"result":{}}"#,
        r#"{"jsonrpc":"2.0","id":1,"result":{"nested":1,"nested":2}}"#,
        r#"{"jsonrpc":"2.0","id":1,"result":{}} {}"#,
        r#"{"jsonrpc":"2.0","id":1,"error":{"code":null,"message":"no"}}"#,
    ] {
        assert!(parse(frame.as_bytes()).is_err(), "accepted {frame}");
    }
    for (frame, kind) in [
        (r#"{"jsonrpc":"2.0","id":1,"result":null}"#, Kind::Response),
        (
            r#"{"jsonrpc":"2.0","id":"q1","method":"gitHubToken.getToken"}"#,
            Kind::Request,
        ),
        (
            r#"{"jsonrpc":"2.0","id":-1,"error":{"code":-32601,"message":"no such method"}}"#,
            Kind::Failure,
        ),
    ] {
        let message = parse(frame.as_bytes()).unwrap_or_else(|_| panic!("refused {frame}"));
        assert_eq!(message.kind, kind, "{frame}");
    }
    let failure =
        parse(br#"{"jsonrpc":"2.0","id":-1,"error":{"code":-32601,"message":"no such method"}}"#)
            .unwrap();
    assert_eq!(
        (failure.code, failure.error.as_str()),
        (-32601, "no such method")
    );
}

#[test]
fn a_lone_surrogate_escape_reads_as_the_replacement_character_as_go_s_decoder_reads_it() {
    let message = parse(br#"{"jsonrpc":"2.0","method":"session.event","params":{"sessionId":"s","event":{"type":"t","data":{}}},"note":"\ud800"}"#).unwrap();
    assert_eq!(decode_event(&message).unwrap().unwrap().kind, "t");
}
