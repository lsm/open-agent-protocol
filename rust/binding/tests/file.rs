use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::time::{Duration, Instant};

use oap_binding::{frame, Action, Entry, Error, FileStore, Record, Store};

struct Scratch(PathBuf);

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn scratch(name: &str) -> (Scratch, PathBuf) {
    let dir = std::env::temp_dir().join(format!("oap-binding-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    let path = dir.join("nested").join("bindings.jsonl");
    (Scratch(dir), path)
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

fn extend(path: &PathBuf, tail: &str) {
    let mut held = std::fs::read(path).unwrap();
    held.extend_from_slice(tail.as_bytes());
    std::fs::write(path, held).unwrap();
}

fn torn<T: std::fmt::Debug>(result: Result<T, Error>) -> bool {
    matches!(result, Err(Error::Torn(_)))
}

#[test]
fn a_store_appends_and_reads_back_every_record_and_a_reopen_is_appended() {
    let (_scratch, path) = scratch("append");
    let store = FileStore::open(&path).unwrap();
    for at in 0..4 {
        store.append(Entry::opened(sample(), at)).unwrap();
    }
    store
        .append(Entry::reopened(sample(), "native-7", 4))
        .unwrap();
    let history = store.history("session-1").unwrap();
    assert_eq!(history.len(), 5);
    assert_eq!(history[0].record.native_session_id, "");
    let latest = store.latest("session-1").unwrap().unwrap();
    assert_eq!(
        (
            latest.action,
            latest.time_ms,
            latest.record.native_session_id.as_str()
        ),
        (Action::Reopened, 4, "native-7")
    );
    let mut other = sample();
    other.session_id = "session-2".into();
    store.append(Entry::opened(other, 5)).unwrap();
    assert_eq!(store.history("session-1").unwrap().len(), 5);
    assert!(store.latest("session-9").unwrap().is_none());
    assert_eq!(store.sessions().unwrap().len(), 2);
}

#[test]
fn the_line_is_the_one_the_zig_and_go_stores_write() {
    let (_scratch, path) = scratch("line");
    let store = FileStore::open(&path).unwrap();
    let zig = "c66c4573 {\"action\":\"opened\",\"time_ms\":7,\"record\":{\"session_id\":\"s\",\"adapter\":\"codex\",\"native_session_id\":\"thread-1\"}}\n";
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
        frame(serde_json::to_string(&entry).unwrap().as_bytes()),
        zig.as_bytes()
    );
    store.append(entry).unwrap();
    assert_eq!(std::fs::read_to_string(&path).unwrap(), zig);
    let settings = "b3827233 {\"action\":\"opened\",\"time_ms\":7,\"record\":{\"session_id\":\"s\",\"adapter\":\"pi\",\"model\":\"m\",\"reasoning_level\":\"high\",\"compaction_policy\":{\"kind\":\"share\",\"share_percent\":80}}}\n";
    std::fs::write(&path, settings).unwrap();
    let reread = FileStore::open(&path).unwrap();
    assert_eq!(
        reread.history("s").unwrap()[0].record.reasoning_level,
        "high"
    );
}

#[test]
fn a_torn_append_is_detected_and_never_read() {
    let (_scratch, path) = scratch("torn");
    let store = FileStore::open(&path).unwrap();
    store.append(Entry::opened(sample(), 1)).unwrap();
    let line = serde_json::to_string(&Entry::reopened(sample(), "native-9", 3)).unwrap();
    let whole = String::from_utf8(frame(line.as_bytes())).unwrap();
    extend(&path, whole.trim_end_matches('\n'));
    assert!(torn(store.latest("session-1")));
    assert!(torn(store.history("session-1")));
    assert!(torn(store.sessions()));
}

#[test]
fn a_torn_or_corrupt_tail_is_truncated_on_append_and_on_open() {
    for tail in [
        "3f1a9b2c {\"action\":\"opene",
        "3f1a9b2c {\"action\":\"reopened\",\"record\":{\"session_id\":\"session-1\"}}\n",
    ] {
        let (_scratch, path) = scratch("tail");
        let store = FileStore::open(&path).unwrap();
        store.append(Entry::opened(sample(), 1)).unwrap();
        extend(&path, tail);
        assert!(torn(store.latest("session-1")), "{tail}");
        let reopened = FileStore::open(&path).unwrap();
        let history = reopened.history("session-1").unwrap();
        assert_eq!(history.len(), 1, "{tail}");
        extend(&path, tail);
        store
            .append(Entry::reopened(sample(), "native-7", 2))
            .unwrap();
        let history = store.history("session-1").unwrap();
        assert_eq!(history.len(), 2, "{tail}");
        assert_eq!(history[1].action, Action::Reopened);
    }
}

#[test]
fn a_record_whose_bytes_do_not_match_its_checksum_is_refused_then_repaired() {
    let (_scratch, path) = scratch("checksum");
    let store = FileStore::open(&path).unwrap();
    store.append(Entry::opened(sample(), 1)).unwrap();
    store.append(Entry::opened(sample(), 2)).unwrap();
    let held = std::fs::read_to_string(&path).unwrap();
    let edited = held.replacen("\"session-1\"", "\"session-2\"", 1);
    assert_eq!(edited.len(), held.len());
    std::fs::write(&path, edited).unwrap();
    assert!(torn(store.history("session-1")));
    store.append(Entry::opened(sample(), 3)).unwrap();
    let history = store.history("session-1").unwrap();
    assert_eq!(history.len(), 1);
    assert_eq!(history[0].time_ms, 3);
}

#[test]
fn a_host_that_empties_the_store_does_not_leave_the_cache_pointing_into_nothing() {
    let (_scratch, path) = scratch("emptied");
    let store = FileStore::open(&path).unwrap();
    store.append(Entry::opened(sample(), 1)).unwrap();
    store.append(Entry::opened(sample(), 2)).unwrap();
    std::fs::write(&path, b"").unwrap();
    let mut long = sample();
    long.session_id = "session-longer-than-the-cached-offset".into();
    long.model = "m".repeat(200);
    store.append(Entry::opened(long.clone(), 9)).unwrap();
    store.append(Entry::opened(long.clone(), 9)).unwrap();
    assert_eq!(store.history(&long.session_id).unwrap().len(), 2);
}

#[test]
fn four_hundred_appends_read_back_whole() {
    let (_scratch, path) = scratch("linear");
    let store = FileStore::open(&path).unwrap();
    let started = Instant::now();
    for at in 0..400 {
        store.append(Entry::opened(sample(), at)).unwrap();
    }
    assert!(started.elapsed() < Duration::from_secs(5));
    assert_eq!(store.history("session-1").unwrap().len(), 400);
}

#[test]
fn the_store_file_is_not_world_readable() {
    let (_scratch, path) = scratch("mode");
    let store = FileStore::open(&path).unwrap();
    store.append(Entry::opened(sample(), 1)).unwrap();
    let mode = std::fs::metadata(&path).unwrap().permissions().mode();
    assert_eq!(mode & 0o077, 0);
    assert!(FileStore::open(std::path::Path::new("")).is_err());
}

#[test]
fn a_record_longer_than_the_tail_window_is_kept_across_appends() {
    let (_scratch, path) = scratch("long");
    let store = FileStore::open(&path).unwrap();
    let mut long = sample();
    long.model = "m".repeat(5000);
    store.append(Entry::opened(sample(), 1)).unwrap();
    store.append(Entry::opened(long, 2)).unwrap();
    store.append(Entry::opened(sample(), 3)).unwrap();
    let history = store.history("session-1").unwrap();
    assert_eq!(
        history
            .iter()
            .map(|entry| entry.time_ms)
            .collect::<Vec<_>>(),
        [1, 2, 3]
    );
    assert_eq!(history[1].record.model.len(), 5000);
}

#[test]
fn a_store_whose_file_was_deleted_reads_as_empty() {
    let (_scratch, path) = scratch("deleted");
    let store = FileStore::open(&path).unwrap();
    store.append(Entry::opened(sample(), 1)).unwrap();
    std::fs::remove_file(&path).unwrap();
    assert!(store.history("session-1").unwrap().is_empty());
    assert!(store.latest("session-1").unwrap().is_none());
    assert!(store.sessions().unwrap().is_empty());
}

#[test]
fn a_fresh_store_reads_as_empty() {
    let (_scratch, path) = scratch("fresh");
    let store = FileStore::open(&path).unwrap();
    assert!(store.history("session-1").unwrap().is_empty());
    assert!(store.latest("session-1").unwrap().is_none());
    assert!(store.sessions().unwrap().is_empty());
}
