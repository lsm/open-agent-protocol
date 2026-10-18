package binding

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRunStoresKeepOpenRunsInAdmissionOrderAndForgetSettledOnes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "runs.jsonl")
	backends := map[string]func(t *testing.T) Runs{
		"memory": func(*testing.T) Runs { return MemoryRuns() },
		"file": func(t *testing.T) Runs {
			store, err := RunsFile(path)
			if err != nil {
				t.Fatal(err)
			}
			return store
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store := open(t)
			for _, record := range []RunRecord{
				{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "r1"},
				{Action: RunStarted, TimeMS: 2, SessionID: "s", RunID: "r1"},
				{Action: RunAdmitted, TimeMS: 3, SessionID: "s", RunID: "r2", Request: `{"message":"next"}`},
				{Action: RunAdmitted, TimeMS: 4, SessionID: "other", RunID: "r3"},
				{Action: RunSettled, TimeMS: 5, SessionID: "other", RunID: "r3"},
			} {
				if err := store.AppendRun(ctx, record); err != nil {
					t.Fatal(err)
				}
			}
			if name == "file" {
				store = open(t)
			}
			runs, err := store.OpenRuns(ctx, "s")
			if err != nil {
				t.Fatal(err)
			}
			if len(runs) != 2 || runs[0].RunID != "r1" || !runs[0].Started || runs[1].RunID != "r2" || runs[1].Started || runs[1].Request != `{"message":"next"}` {
				t.Fatalf("open runs of s = %+v, want r1 started then r2 queued with its request", runs)
			}
			if other, err := store.OpenRuns(ctx, "other"); err != nil || len(other) != 0 {
				t.Fatalf("open runs of other = %+v, %v; want none, since r3 settled", other, err)
			}
		})
	}
}

func TestARunRecordLineIsTheChecksumOfItsJSONAndTheJSONInFixedMemberOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRun(context.Background(), RunRecord{Action: RunAdmitted, TimeMS: 3, SessionID: "s", RunID: "r2", Request: `{"message":"next"}`}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRun(context.Background(), RunRecord{Action: RunAdmitted, TimeMS: 3, SessionID: "s", RunID: "r2", Request: `{"message":"<a & b>"}`}); err != nil {
		t.Fatal(err)
	}
	bytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := `34771ed2 {"action":"admitted","time_ms":3,"session_id":"s","run_id":"r2","request":"{\"message\":\"next\"}"}` + "\n" +
		`cb0983f9 {"action":"admitted","time_ms":3,"session_id":"s","run_id":"r2","request":"{\"message\":\"<a & b>\"}"}` + "\n"
	if string(bytes) != want {
		t.Fatalf("file = %q, want %q", bytes, want)
	}
}

func TestATornRunRecordIsNotReadAndTheNextAppendDropsIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRun(ctx, RunRecord{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "r1"}); err != nil {
		t.Fatal(err)
	}
	whole, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(whole, whole[:len(whole)/2]...), 0o600); err != nil {
		t.Fatal(err)
	}
	if runs, err := store.OpenRuns(ctx, "s"); err != nil || len(runs) != 1 {
		t.Fatalf("open runs over a torn tail = %+v, %v; want r1 alone", runs, err)
	}
	if err := store.AppendRun(ctx, RunRecord{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "r2"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runs, _ := store.OpenRuns(ctx, "s"); len(runs) != 2 || runs[1].RunID != "r2" || len(after) >= 2*len(whole)+len(whole)/2 {
		t.Fatalf("after the next append: %+v and %d bytes; want r1, r2 and the torn tail gone", runs, len(after))
	}
}

func TestASettledRecordALogKeptIsReadAsTheEndOfItsRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	var log []byte
	for _, record := range []RunRecord{
		{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "r1"},
		{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "r2"},
		{Action: RunAdmitted, TimeMS: 3, SessionID: "s", RunID: "r3"},
		{Action: RunSettled, TimeMS: 4, SessionID: "s", RunID: "r1"},
		{Action: RunStarted, TimeMS: 5, SessionID: "s", RunID: "r3"},
	} {
		payload, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, frame(payload)...)
	}
	if err := os.WriteFile(path, log, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := store.OpenRuns(context.Background(), "s")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].RunID != "r2" || runs[0].Started || runs[1].RunID != "r3" || !runs[1].Started {
		t.Fatalf("open runs = %+v, want r2 queued then r3 started, r1 gone", runs)
	}
}

func TestARedeliveredRunReplacesItsHeldRunInOneRecord(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	file, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for name, store := range map[string]Runs{"memory": MemoryRuns(), "file": file} {
		for _, record := range []RunRecord{
			{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "held", Request: "{}"},
			{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "after", Request: "{}"},
			{Action: RunAdmitted, TimeMS: 3, SessionID: "s", RunID: "redelivered", Request: "{}", Replaces: "held"},
		} {
			if err := store.AppendRun(ctx, record); err != nil {
				t.Fatal(err)
			}
		}
		runs, err := store.OpenRuns(ctx, "s")
		if err != nil {
			t.Fatal(err)
		}
		if len(runs) != 2 || runs[0].RunID != "redelivered" || runs[1].RunID != "after" {
			t.Fatalf("%s: open runs = %+v, want redelivered in held's slot, then after", name, runs)
		}
	}
	var log []byte
	for _, record := range []RunRecord{
		{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "held"},
		{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "redelivered", Replaces: "held"},
	} {
		payload, err := marshalRun(record)
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, frame(payload)...)
	}
	kept := filepath.Join(t.TempDir(), "kept.jsonl")
	if err := os.WriteFile(kept, log, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := RunsFile(kept)
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := reader.OpenRuns(ctx, "s"); err != nil || len(runs) != 1 || runs[0].RunID != "redelivered" {
		t.Fatalf("a log that kept the held run: %+v, %v; want redelivered alone", runs, err)
	}
}

func TestSettlingTheReplacingRunOverAKeptLogDoesNotReviveTheReplacedOne(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	var log []byte
	for _, record := range []RunRecord{
		{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "held"},
		{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "redelivered", Replaces: "held"},
	} {
		payload, err := marshalRun(record)
		if err != nil {
			t.Fatal(err)
		}
		log = append(log, frame(payload)...)
	}
	if err := os.WriteFile(path, log, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRun(ctx, RunRecord{Action: RunSettled, TimeMS: 3, SessionID: "s", RunID: "redelivered"}); err != nil {
		t.Fatal(err)
	}
	if runs, err := store.OpenRuns(ctx, "s"); err != nil || len(runs) != 0 {
		t.Fatalf("open runs = %+v, %v; want none: held was replaced and its replacement settled", runs, err)
	}
}

func TestARedeliveredRunKeepsItsPlaceAheadOfARunStillHeldBehindIt(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "runs.jsonl")
	store, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range []RunRecord{
		{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "a"},
		{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "b"},
		{Action: RunAdmitted, TimeMS: 3, SessionID: "s", RunID: "a2", Replaces: "a"},
		{Action: RunStarted, TimeMS: 4, SessionID: "other", RunID: "x"},
	} {
		if err := store.AppendRun(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	reread, err := RunsFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runs, err := reread.OpenRuns(ctx, "s"); err != nil || len(runs) != 2 || runs[0].RunID != "a2" || runs[1].RunID != "b" {
		t.Fatalf("open runs after a reread = %+v, %v; want a2 in a's place, then b", runs, err)
	}
}

func TestARecordAfterARunWasReplacedOrSettledDoesNotReopenIt(t *testing.T) {
	ctx := context.Background()
	store := MemoryRuns()
	for _, record := range []RunRecord{
		{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "held"},
		{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "redelivered", Replaces: "held"},
		{Action: RunStarted, TimeMS: 3, SessionID: "s", RunID: "held"},
	} {
		if err := store.AppendRun(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	if runs, err := store.OpenRuns(ctx, "s"); err != nil || len(runs) != 1 || runs[0].RunID != "redelivered" {
		t.Fatalf("open runs = %+v, %v; want redelivered alone", runs, err)
	}
	if runs := openRuns([]RunRecord{
		{Action: RunAdmitted, TimeMS: 1, SessionID: "s", RunID: "held"},
		{Action: RunAdmitted, TimeMS: 2, SessionID: "s", RunID: "redelivered", Replaces: "held"},
		{Action: RunStarted, TimeMS: 3, SessionID: "s", RunID: "held"},
		{Action: RunAdmitted, TimeMS: 4, SessionID: "s", RunID: "done"},
		{Action: RunSettled, TimeMS: 5, SessionID: "s", RunID: "done"},
		{Action: RunStarted, TimeMS: 6, SessionID: "s", RunID: "done"},
	}, "s"); len(runs) != 1 || runs[0].RunID != "redelivered" {
		t.Fatalf("over a kept log: %+v, want redelivered alone", runs)
	}
}
