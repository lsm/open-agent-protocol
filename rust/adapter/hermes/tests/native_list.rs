mod support;

use std::sync::{Arc, Mutex};
use std::time::Instant;

use oap_adapter::{Error, NativeListRequest, NativeListing};
use oap_adapter_hermes::adapter::{Adapter, Config};
use oap_adapter_hermes::client::{Client, ClientFactory};
use serde_json::value::RawValue;
use support::go_runtime;

type Listed = (&'static str, &'static str, i64);

struct Lister {
    answer: Result<&'static str, &'static str>,
    limits: Mutex<Vec<usize>>,
}

impl ClientFactory for Lister {
    fn start(&self, _deadline: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        Err(Error::Harness("unused".into()))
    }

    fn list(&self, limit: usize) -> Option<Result<Box<RawValue>, Error>> {
        self.limits.lock().unwrap().push(limit);
        Some(match self.answer {
            Ok(answer) => Ok(RawValue::from_string(answer.into()).unwrap()),
            Err(failure) => Err(Error::Harness(failure.into())),
        })
    }
}

struct Plain;

impl ClientFactory for Plain {
    fn start(&self, _deadline: Option<Instant>) -> Result<(Arc<dyn Client>, String), Error> {
        Err(Error::Harness("unused".into()))
    }
}

fn adapter(factory: Arc<dyn ClientFactory>) -> Adapter {
    Adapter::new(Config {
        factory: Some(factory),
        model: "hermes-test".into(),
        journal_capacity: 64,
        runtime: go_runtime(),
        ..Config::default()
    })
    .unwrap()
}

fn listed(
    answer: Result<&'static str, &'static str>,
    limit: usize,
) -> (Result<Vec<NativeListing>, Error>, Vec<usize>) {
    let lister = Arc::new(Lister {
        answer,
        limits: Mutex::new(Vec::new()),
    });
    let listed = adapter(lister.clone()).native_list(NativeListRequest {
        limit,
        ..NativeListRequest::default()
    });
    let limits = lister.limits.lock().unwrap().clone();
    (listed, limits)
}

#[test]
fn the_native_list_reads_each_page_hermes_answers_as_go_s_does() {
    let cases: [(&str, usize, Vec<Listed>); 23] = [
        (
            "{ \"sessions\" :\n [ { \"id\" : \"w\" , \"started_at\" :\t12 \r\n} , {\"id\":\"v\",\"started_at\": 1.5 }] }",
            5,
            vec![("w", "", 12000), ("v", "", 1500)],
        ),
        (
            r#"{"sessions":[{"id":"stored-a","title":"Named","preview":"ignored","started_at":1774717612.4789,"message_count":3,"source":"cli"},7,{"title":"no id"},{"id":"stored-b","title":"","preview":"first words","started_at":1774717000},{"id":"stored-c","preview":"past the limit"}]}"#,
            2,
            vec![
                ("stored-a", "Named", 1774717612478),
                ("stored-b", "first words", 1774717000000),
            ],
        ),
        (r#"{"sessions":[{"id":"a"}]}"#, 0, vec![]),
        (
            r#"{"sessions":[null,"x",[1],{"id":7},{"id":null},{"id":""},{"id":"a","title":7,"preview":"p"},{"id":"b","title":null,"preview":8},{"id":"c","started_at":"12"},{"id":"d","started_at":null},{"id":"e","started_at":true}]}"#,
            10,
            vec![
                ("a", "p", 0),
                ("b", "", 0),
                ("c", "", 0),
                ("d", "", 0),
                ("e", "", 0),
            ],
        ),
        (
            r#"{"sessions":[{"id":"a","id":"b","title":"t1","title":"t2"}]}"#,
            5,
            vec![("b", "t2", 0)],
        ),
        (
            r#"{"sessions":[{"id":"a"}],"sessions":[{"id":"b"}]}"#,
            5,
            vec![("b", "", 0)],
        ),
        (r#"{"SESSIONS":[{"id":"a"}]}"#, 5, vec![("a", "", 0)]),
        (
            r#"{"Sessions":[{"id":"a"}],"sessions":[{"id":"b"}]}"#,
            5,
            vec![("b", "", 0)],
        ),
        (r#"{"sessions":[{"id":"a"}],"Sessions":7}"#, 5, vec![]),
        (
            r#"{"ſessions":[{"id":"long-s"}]}"#,
            5,
            vec![("long-s", "", 0)],
        ),
        (r#"{"sessİons":[{"id":"dotted-i"}]}"#, 5, vec![]),
        (r#"{"sessıons":[{"id":"dotless-i"}]}"#, 5, vec![]),
        (r#"{"session":[{"id":"a"}]}"#, 5, vec![]),
        (r#"{"sessions":null}"#, 5, vec![]),
        (r#"{"sessions":{}}"#, 5, vec![]),
        (r#"{"sessions":"x"}"#, 5, vec![]),
        (r#"{}"#, 5, vec![]),
        (r#"null"#, 5, vec![]),
        (r#"[]"#, 5, vec![]),
        (r#""x""#, 5, vec![]),
        (
            r#"{"sessions":[{"id":"\ud800x","title":"aé\n"}]}"#,
            5,
            vec![("�x", "aé\n", 0)],
        ),
        (r#"{"sessions":[{"ID":"upper"},{"Id":"mixed"}]}"#, 5, vec![]),
        (
            r#"{"sessions":[{"id":"s1","started_at":12},{"id":"s2","started_at":1.5},{"id":"s3","started_at":-1.5},{"id":"s4","started_at":9223372036854775807},{"id":"s5","started_at":-9223372036854775808},{"id":"s6","started_at":1e15},{"id":"s7","started_at":1e3},{"id":"s8","started_at":9223372036854775},{"id":"s9","started_at":9223372036854776},{"id":"s10","started_at":-9223372036854775},{"id":"s11","started_at":-9223372036854776},{"id":"s12","started_at":1.9999},{"id":"s13","started_at":-0.0005},{"id":"s14","started_at":999999999999999.9},{"id":"s15","started_at":-1e15},{"id":"s16","started_at":1e309},{"id":"s17","started_at":1.5e2},{"id":"s18","started_at":9223372036854775808},{"id":"s19","started_at":-0},{"id":"s20","started_at":0.0001},{"id":"s21","started_at":1E2},{"id":"s22","started_at":123456789012345678901234567890}]}"#,
            30,
            vec![
                ("s1", "", 12000),
                ("s2", "", 1500),
                ("s3", "", -1500),
                ("s4", "", 9223372036854775807),
                ("s5", "", -9223372036854775808),
                ("s6", "", 0),
                ("s7", "", 1000000),
                ("s8", "", 9223372036854775000),
                ("s9", "", 9223372036854775807),
                ("s10", "", -9223372036854775000),
                ("s11", "", -9223372036854775808),
                ("s12", "", 1999),
                ("s13", "", 0),
                ("s14", "", 999999999999999872),
                ("s15", "", 0),
                ("s16", "", 0),
                ("s17", "", 150000),
                ("s18", "", 0),
                ("s19", "", 0),
                ("s20", "", 0),
                ("s21", "", 100000),
                ("s22", "", 0),
            ],
        ),
    ];
    for (answer, limit, want) in cases {
        let (listed, limits) = listed(Ok(answer), limit);
        let want: Vec<NativeListing> = want
            .into_iter()
            .map(|(id, title, updated_at_ms)| NativeListing {
                native_id: id.into(),
                title: title.into(),
                updated_at_ms,
                ..NativeListing::default()
            })
            .collect();
        assert_eq!(listed, Ok(want), "{answer}");
        assert_eq!(limits, [limit], "{answer}");
    }
}

#[test]
fn a_failed_listing_fails_and_a_factory_that_cannot_list_lists_nothing() {
    let (listed, limits) = listed(Err("gateway gone"), 5);
    assert_eq!(listed, Err(Error::Harness("gateway gone".into())));
    assert_eq!(limits, [5]);
    assert_eq!(
        adapter(Arc::new(Plain)).native_list(NativeListRequest {
            limit: 5,
            ..NativeListRequest::default()
        }),
        Ok(Vec::new())
    );
}
