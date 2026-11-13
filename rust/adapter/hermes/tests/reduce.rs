mod support;

use oap_adapter::Error;
use oap_adapter_hermes::rpc::RequestId;
use support::{opened, wait_unusable, Fake};

fn unusable(fake: &Fake, session: &oap_adapter_hermes::session::Session) -> bool {
    fake.barrier();
    session.state().err() == Some(Error::SessionClosed)
}

#[test]
fn an_event_for_another_session_or_out_of_sequence_makes_the_session_unusable() {
    for params in [
        r#"{"type":"message.delta","session_id":"other123","seq":1,"payload":{"text":"x"}}"#,
        r#"{"type":"skin.changed","session_id":"sess0001","seq":2,"payload":{}}"#,
    ] {
        let fake = Fake::new([]);
        let session = opened(&fake);
        fake.raw(params);
        assert!(unusable(&fake, &session), "{params}");
    }
}

#[test]
fn session_less_and_session_wide_events_leave_an_idle_session_usable() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.raw(r#"{"type":"message.start"}"#);
    fake.event("skin.changed", 1, "{}");
    fake.event("skin.changed", 2, "{}");
    assert!(!unusable(&fake, &session));
    fake.event("message.delta", 3, r#"{"text":"x"}"#);
    assert!(unusable(&fake, &session));
}

#[test]
fn a_run_event_no_run_reserved_makes_the_session_unusable() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.event("message.start", 1, "");
    assert!(unusable(&fake, &session));
}

#[test]
fn an_unmapped_server_request_is_answered_method_not_found() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    let answers = fake.request(
        RequestId::String("r1".into()),
        "session.weird",
        r#"{"session_id":"sess0001"}"#,
    );
    assert_eq!(answers.codes(), [-32601]);
    assert!(!unusable(&fake, &session));
}

#[test]
fn a_server_request_the_session_cannot_own_makes_it_unusable_unanswered() {
    for (id, method, params) in [
        (
            RequestId::String("r2".into()),
            "approval",
            r#"{"session_id":"other"}"#,
        ),
        (RequestId::Integer(7), "session.weird", "{}"),
        (
            RequestId::String("r3".into()),
            "sudo",
            r#"{"session_id":"sess0001"}"#,
        ),
        (RequestId::String("r4".into()), "clarify", "{}"),
    ] {
        let fake = Fake::new([]);
        let session = opened(&fake);
        let answers = fake.request(id, method, params);
        assert!(unusable(&fake, &session), "{method} {params}");
        assert!(answers.codes().is_empty(), "{method} {params}");
    }
}

#[test]
fn a_closed_transport_makes_an_idle_session_unusable_and_close_still_succeeds() {
    let fake = Fake::new([]);
    let session = opened(&fake);
    fake.hang_up();
    wait_unusable(&session);
    assert_eq!(session.close(), Ok(()));
    assert_eq!(fake.closes(), 1);
}
