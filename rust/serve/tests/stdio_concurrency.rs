mod support;

use std::sync::{Arc, Condvar, Mutex};
use std::time::Duration;

use oap_adapter::{Adapter, BoxFuture, Descriptor, Error, OpenRequest, Session};
use oap_serve::stdio::{serve, StdioOptions};
use oap_serve::{Hub, Registry};
use serde_json::Value;

#[derive(Default)]
struct Meeting {
    arrived: Mutex<usize>,
    met: Condvar,
}

struct Waiting(Arc<Meeting>, usize, Duration);

impl Adapter for Waiting {
    fn probe(&self) -> BoxFuture<'_, Descriptor> {
        let meeting = self.0.clone();
        let wanted = self.1;
        let patience = self.2;
        Box::pin(async move {
            let mut arrived = meeting.arrived.lock().unwrap();
            *arrived += 1;
            meeting.met.notify_all();
            let (arrived, waited) = meeting
                .met
                .wait_timeout_while(arrived, patience, |arrived| *arrived < wanted)
                .unwrap();
            if waited.timed_out() {
                return Err(Error::Harness(format!(
                    "only {} probes were in flight",
                    *arrived
                )));
            }
            Ok(Descriptor {
                capability_revision: "rev-1".into(),
                ..Descriptor::default()
            })
        })
    }

    fn open(&self, _: OpenRequest) -> BoxFuture<'_, Box<dyn Session>> {
        Box::pin(async { Err(Error::SessionClosed) })
    }
}

fn hub(wanted: usize, patience: Duration) -> Hub {
    let mut registry = Registry::new();
    registry
        .register(
            "waiting",
            Arc::new(Waiting(Arc::default(), wanted, patience)),
        )
        .unwrap();
    Hub::new(registry)
}

fn served(hub: &Hub, lines: &[&str], options: &StdioOptions) -> Vec<Value> {
    let input: String = lines.iter().map(|line| format!("{line}\n")).collect();
    let output = support::Captured::default();
    serve(
        hub,
        std::io::Cursor::new(input.into_bytes()),
        output.clone(),
        options,
    )
    .unwrap();
    output.values()
}

fn answer(lines: &[Value], id: i64) -> &Value {
    lines.iter().find(|line| line["id"] == id).unwrap()
}

#[test]
fn pipelined_requests_are_served_at_once_rather_than_one_after_another() {
    let hub = hub(2, Duration::from_secs(5));
    let answered = served(
        &hub,
        &[
            r#"{"id":1,"op":"capabilities","adapter":"waiting"}"#,
            r#"{"id":2,"op":"capabilities","adapter":"waiting"}"#,
        ],
        &StdioOptions::default(),
    );
    for id in [1, 2] {
        let line = answer(&answered, id);
        assert_eq!(line["ok"], true, "{line}");
    }
}

#[test]
fn a_request_past_the_in_flight_bound_is_refused_busy_naming_the_bound() {
    let hub = hub(2, Duration::from_secs(1));
    let answered = served(
        &hub,
        &[
            r#"{"id":1,"op":"capabilities","adapter":"waiting"}"#,
            r#"{"id":2,"op":"adapters"}"#,
        ],
        &StdioOptions {
            max_ops: 1,
            ..StdioOptions::default()
        },
    );
    let refused = &answer(&answered, 2)["error"];
    assert_eq!(refused["code"], "busy", "{refused}");
    assert_eq!(
        refused["message"],
        "the frontend is already running 1 operations; send this request again"
    );
    assert_eq!(answer(&answered, 1)["error"]["code"], "probe_failed");
}
