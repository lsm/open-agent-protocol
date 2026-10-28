use oap_binding::{
    list, sessions, state, Action, Entry, Error, MemoryStore, Record, Store, DEFAULT_LIMIT,
    MAX_LIMIT,
};

fn entry(action: Action, time_ms: i64, session_id: &str) -> Entry {
    Entry {
        action,
        time_ms,
        record: Record {
            session_id: session_id.into(),
            adapter: "memory".into(),
            ..Record::default()
        },
    }
}

fn ids(entries: &[Entry]) -> Vec<&str> {
    entries
        .iter()
        .map(|entry| entry.record.session_id.as_str())
        .collect()
}

fn sample() -> Record {
    Record::from_open(
        "session-1",
        "memory",
        "memory-oap-v1",
        "a-model",
        "/home/op",
        "/work/repo",
        &["fs".into()],
    )
}

#[test]
fn live_reads_the_entry_that_claims_to_open_the_session() {
    assert!(Entry::opened(sample(), 1).live());
    assert!(Entry::reopened(sample(), "native-7", 2).live());
    assert!(!Entry::closed(sample(), 3).live());
    assert!(!Entry {
        action: Action::Refused,
        time_ms: 4,
        record: sample()
    }
    .live());
}

#[test]
fn a_reopen_keeps_the_previous_native_id_unless_it_learns_a_new_one() {
    let mut record = sample();
    record.native_session_id = "native-1".into();
    assert_eq!(
        Entry::reopened(record.clone(), "", 2)
            .record
            .native_session_id,
        "native-1"
    );
    let reopened = Entry::reopened(record, "native-2", 3);
    assert_eq!(reopened.action, Action::Reopened);
    assert_eq!(reopened.time_ms, 3);
    assert_eq!(reopened.record.native_session_id, "native-2");
}

#[test]
fn state_ignores_a_refusal_for_a_duplicate_that_never_ran() {
    let second = Record::from_open(
        "session-1",
        "memory",
        "memory-oap-v1",
        "another-model",
        "/home/op",
        "/work",
        &["git".into()],
    );
    let history = [
        Entry::opened(sample(), 1),
        Entry {
            action: Action::Refused,
            time_ms: 2,
            record: second,
        },
    ];
    let running = state(&history).unwrap();
    assert_eq!(running.action, Action::Opened);
    assert_eq!(running.record.model, "a-model");
    assert_eq!(running.record.tool_source_ids, ["fs"]);
    assert!(state(&[Entry {
        action: Action::Refused,
        time_ms: 1,
        record: sample()
    }])
    .is_none());
}

#[test]
fn sessions_keeps_each_sessions_latest_entry_that_is_not_refused() {
    let sessions = sessions(&[
        entry(Action::Opened, 1, "a"),
        entry(Action::Refused, 2, "only-refused"),
        entry(Action::Opened, 3, "b"),
        entry(Action::Closed, 4, "a"),
        entry(Action::Refused, 5, "a"),
    ]);
    assert_eq!(ids(&sessions), ["a", "b"]);
    assert_eq!(sessions[0].action, Action::Closed);
    assert_eq!(sessions[0].time_ms, 4);
}

#[test]
fn a_list_is_newest_first_with_ties_broken_by_session_id() {
    let page = list(
        &[
            entry(Action::Opened, 1, "old"),
            entry(Action::Opened, 5, "tie-b"),
            entry(Action::Opened, 9, "new"),
            entry(Action::Opened, 5, "tie-a"),
        ],
        "",
        0,
    )
    .unwrap();
    assert_eq!(ids(&page.entries), ["new", "tie-a", "tie-b", "old"]);
    assert_eq!(page.next_cursor, "");
}

#[test]
fn a_list_pages_on_its_cursor_until_none_remain() {
    let all: Vec<Entry> = ["e", "d", "c", "b", "a"]
        .iter()
        .enumerate()
        .map(|(at, id)| entry(Action::Opened, at as i64 + 1, id))
        .collect();
    let mut seen = Vec::new();
    let mut cursor = String::new();
    for _ in 0..4 {
        let page = list(&all, &cursor, 2).unwrap();
        assert!(page.entries.len() <= 2);
        seen.extend(ids(&page.entries).into_iter().map(str::to_owned));
        if page.next_cursor.is_empty() {
            break;
        }
        cursor = page.next_cursor;
    }
    assert_eq!(seen, ["a", "b", "c", "d", "e"]);
}

#[test]
fn a_list_defaults_its_limit_and_refuses_one_out_of_range() {
    let all: Vec<Entry> = (0..=DEFAULT_LIMIT)
        .map(|at| {
            let id: String = [
                (b'a' + (at % 26) as u8) as char,
                (b'a' + (at / 26) as u8) as char,
            ]
            .iter()
            .collect();
            entry(Action::Opened, at as i64, &id)
        })
        .collect();
    let page = list(&all, "", 0).unwrap();
    assert_eq!(page.entries.len(), DEFAULT_LIMIT);
    assert!(!page.next_cursor.is_empty());
    for limit in [-1, MAX_LIMIT as i64 + 1] {
        assert!(matches!(list(&all, "", limit), Err(Error::InvalidLimit)));
    }
    assert!(list(&all, "", MAX_LIMIT as i64).is_ok());
}

#[test]
fn a_cursor_the_list_did_not_issue_is_refused() {
    for cursor in ["not base64!", "bm8tY29sb24", "eDpzZXNzaW9u", "MTI6"] {
        assert!(
            matches!(list(&[], cursor, 0), Err(Error::InvalidCursor)),
            "{cursor}"
        );
    }
}

#[test]
fn a_cursor_is_the_bytes_the_zig_and_go_hubs_issue_for_the_same_position() {
    let page = list(
        &[
            entry(Action::Opened, 1, "old"),
            entry(Action::Opened, 5, "tie-b"),
            entry(Action::Opened, 9, "new"),
            entry(Action::Opened, 5, "tie-a"),
        ],
        "",
        2,
    )
    .unwrap();
    assert_eq!(page.next_cursor, "NTp0aWUtYQ");
}

#[test]
fn the_memory_store_lists_what_was_appended() {
    let store = MemoryStore::default();
    for appended in [
        entry(Action::Opened, 1, "a"),
        entry(Action::Opened, 2, "b"),
        entry(Action::Closed, 3, "a"),
        entry(Action::Refused, 4, "a"),
    ] {
        store.append(appended).unwrap();
    }
    let listed = store.sessions().unwrap();
    assert_eq!(ids(&listed), ["a", "b"]);
    assert_eq!(listed[0].action, Action::Closed);
    assert_eq!(store.latest("a").unwrap().unwrap().action, Action::Refused);
    assert_eq!(store.history("a").unwrap().len(), 3);
    assert!(store.latest("z").unwrap().is_none());
}

#[test]
fn an_entry_serializes_as_the_zig_and_go_stores_write_it() {
    let entry = Entry::opened(
        Record {
            session_id: "s".into(),
            adapter: "codex".into(),
            native_session_id: "thread-1".into(),
            ..Record::default()
        },
        7,
    );
    assert_eq!(
        serde_json::to_string(&entry).unwrap(),
        r#"{"action":"opened","time_ms":7,"record":{"session_id":"s","adapter":"codex","native_session_id":"thread-1"}}"#
    );
    let settings = r#"{"action":"opened","time_ms":7,"record":{"session_id":"s","adapter":"pi","model":"m","reasoning_level":"high","compaction_policy":{"kind":"share","share_percent":80}}}"#;
    let decoded: Entry = serde_json::from_str(settings).unwrap();
    assert_eq!(decoded.record.reasoning_level, "high");
    assert_eq!(serde_json::to_string(&decoded).unwrap(), settings);
}
