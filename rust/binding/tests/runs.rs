use std::path::PathBuf;

use oap_binding::{frame, open_runs, MemoryRuns, RunAction, RunRecord, Runs, RunsFile};

struct Scratch(PathBuf);

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn scratch(name: &str) -> (Scratch, PathBuf) {
    let dir = std::env::temp_dir().join(format!("oap-runs-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    let path = dir.join("state").join("runs.jsonl");
    (Scratch(dir), path)
}

fn run(action: RunAction, time_ms: i64, session_id: &str, run_id: &str) -> RunRecord {
    RunRecord {
        action,
        time_ms,
        session_id: session_id.into(),
        run_id: run_id.into(),
        ..RunRecord::default()
    }
}

fn replacing(time_ms: i64, run_id: &str, replaces: &str) -> RunRecord {
    RunRecord {
        replaces: replaces.into(),
        ..run(RunAction::Admitted, time_ms, "s", run_id)
    }
}

fn ids(store: &dyn Runs, session_id: &str) -> Vec<String> {
    store
        .open_runs(session_id)
        .unwrap()
        .into_iter()
        .map(|open| open.run_id)
        .collect()
}

fn write_log(path: &PathBuf, records: &[RunRecord]) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    let mut log = Vec::new();
    for record in records {
        log.extend(frame(serde_json::to_string(record).unwrap().as_bytes()));
    }
    std::fs::write(path, log).unwrap();
}

#[test]
fn run_stores_keep_open_runs_in_admission_order_and_forget_settled_ones() {
    let (_scratch, path) = scratch("order");
    let memory = MemoryRuns::default();
    let file = RunsFile::open(&path).unwrap();
    for store in [&memory as &dyn Runs, &file] {
        for record in [
            run(RunAction::Admitted, 1, "s", "r1"),
            run(RunAction::Started, 2, "s", "r1"),
            RunRecord {
                request: r#"{"message":"next"}"#.into(),
                ..run(RunAction::Admitted, 3, "s", "r2")
            },
            run(RunAction::Admitted, 4, "other", "r3"),
            run(RunAction::Settled, 5, "other", "r3"),
        ] {
            store.append_run(record).unwrap();
        }
    }
    let reread = RunsFile::open(&path).unwrap();
    for store in [&memory as &dyn Runs, &reread] {
        let runs = store.open_runs("s").unwrap();
        assert_eq!(runs.len(), 2);
        assert!(runs[0].run_id == "r1" && runs[0].started);
        assert!(runs[1].run_id == "r2" && !runs[1].started);
        assert_eq!(runs[1].request, r#"{"message":"next"}"#);
        assert!(store.open_runs("other").unwrap().is_empty());
    }
}

#[test]
fn a_run_record_line_is_the_checksum_of_its_json_in_fixed_member_order() {
    let (_scratch, path) = scratch("line");
    let store = RunsFile::open(&path).unwrap();
    for request in [r#"{"message":"next"}"#, r#"{"message":"<a & b>"}"#] {
        store
            .append_run(RunRecord {
                request: request.into(),
                ..run(RunAction::Admitted, 3, "s", "r2")
            })
            .unwrap();
    }
    let want = "34771ed2 {\"action\":\"admitted\",\"time_ms\":3,\"session_id\":\"s\",\"run_id\":\"r2\",\"request\":\"{\\\"message\\\":\\\"next\\\"}\"}\n\
                cb0983f9 {\"action\":\"admitted\",\"time_ms\":3,\"session_id\":\"s\",\"run_id\":\"r2\",\"request\":\"{\\\"message\\\":\\\"<a & b>\\\"}\"}\n";
    assert_eq!(std::fs::read_to_string(&path).unwrap(), want);
}

#[test]
fn a_torn_run_record_is_not_read_and_the_next_append_drops_it() {
    let (_scratch, path) = scratch("torn");
    let store = RunsFile::open(&path).unwrap();
    store
        .append_run(run(RunAction::Admitted, 1, "s", "r1"))
        .unwrap();
    let whole = std::fs::read(&path).unwrap();
    let mut torn = whole.clone();
    torn.extend_from_slice(&whole[..whole.len() / 2]);
    std::fs::write(&path, torn).unwrap();
    assert_eq!(ids(&store, "s"), ["r1"]);
    store
        .append_run(run(RunAction::Admitted, 2, "s", "r2"))
        .unwrap();
    assert_eq!(ids(&store, "s"), ["r1", "r2"]);
    assert!(std::fs::read(&path).unwrap().len() < 2 * whole.len() + whole.len() / 2);
}

#[test]
fn a_settled_record_a_log_kept_is_read_as_the_end_of_its_run() {
    let (_scratch, path) = scratch("kept");
    write_log(
        &path,
        &[
            run(RunAction::Admitted, 1, "s", "r1"),
            run(RunAction::Admitted, 2, "s", "r2"),
            run(RunAction::Admitted, 3, "s", "r3"),
            run(RunAction::Settled, 4, "s", "r1"),
            run(RunAction::Started, 5, "s", "r3"),
        ],
    );
    let runs = RunsFile::open(&path).unwrap().open_runs("s").unwrap();
    assert_eq!(runs.len(), 2);
    assert!(runs[0].run_id == "r2" && !runs[0].started);
    assert!(runs[1].run_id == "r3" && runs[1].started);
}

#[test]
fn a_redelivered_run_replaces_its_held_run_in_one_record() {
    let (_scratch, path) = scratch("redeliver");
    let memory = MemoryRuns::default();
    let file = RunsFile::open(&path).unwrap();
    for store in [&memory as &dyn Runs, &file] {
        for record in [
            run(RunAction::Admitted, 1, "s", "held"),
            run(RunAction::Admitted, 2, "s", "after"),
            replacing(3, "redelivered", "held"),
        ] {
            store.append_run(record).unwrap();
        }
        assert_eq!(ids(store, "s"), ["redelivered", "after"]);
    }
    let (_kept_scratch, kept) = scratch("redeliver-kept");
    write_log(
        &kept,
        &[
            run(RunAction::Admitted, 1, "s", "held"),
            replacing(2, "redelivered", "held"),
        ],
    );
    let reader = RunsFile::open(&kept).unwrap();
    assert_eq!(ids(&reader, "s"), ["redelivered"]);
    reader
        .append_run(run(RunAction::Settled, 3, "s", "redelivered"))
        .unwrap();
    assert!(ids(&reader, "s").is_empty());
}

#[test]
fn a_redelivered_run_keeps_its_place_ahead_of_a_run_still_held_behind_it() {
    let (_scratch, path) = scratch("place");
    let store = RunsFile::open(&path).unwrap();
    for record in [
        run(RunAction::Admitted, 1, "s", "a"),
        run(RunAction::Admitted, 2, "s", "b"),
        replacing(3, "a2", "a"),
        run(RunAction::Started, 4, "other", "x"),
    ] {
        store.append_run(record).unwrap();
    }
    assert_eq!(ids(&RunsFile::open(&path).unwrap(), "s"), ["a2", "b"]);
}

#[test]
fn a_record_after_a_run_was_replaced_or_settled_does_not_reopen_it() {
    let store = MemoryRuns::default();
    for record in [
        run(RunAction::Admitted, 1, "s", "held"),
        replacing(2, "redelivered", "held"),
        run(RunAction::Started, 3, "s", "held"),
    ] {
        store.append_run(record).unwrap();
    }
    assert_eq!(ids(&store, "s"), ["redelivered"]);
    let runs = open_runs(
        &[
            run(RunAction::Admitted, 1, "s", "held"),
            replacing(2, "redelivered", "held"),
            run(RunAction::Started, 3, "s", "held"),
            run(RunAction::Admitted, 4, "s", "done"),
            run(RunAction::Settled, 5, "s", "done"),
            run(RunAction::Started, 6, "s", "done"),
        ],
        "s",
    );
    assert_eq!(runs.len(), 1);
    assert_eq!(runs[0].run_id, "redelivered");
}

#[test]
fn a_held_prompt_keeps_its_root_run_id_across_redeliveries() {
    let (_scratch, path) = scratch("root");
    let memory = MemoryRuns::default();
    let file = RunsFile::open(&path).unwrap();
    for store in [&memory as &dyn Runs, &file] {
        for record in [
            run(RunAction::Admitted, 1, "s", "held"),
            run(RunAction::Admitted, 2, "s", "after"),
            replacing(3, "first", "held"),
            replacing(4, "second", "first"),
        ] {
            store.append_run(record).unwrap();
        }
        let runs = store.open_runs("s").unwrap();
        assert_eq!(runs.len(), 2);
        assert_eq!(
            (runs[0].run_id.as_str(), runs[0].root.as_str()),
            ("second", "held")
        );
        assert_eq!(
            (runs[1].run_id.as_str(), runs[1].root.as_str()),
            ("after", "after")
        );
    }
}
