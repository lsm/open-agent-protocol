package binding

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

type RunAction string

const (
	RunAdmitted RunAction = "admitted"
	RunStarted  RunAction = "started"
	RunSettled  RunAction = "settled"
)

type RunRecord struct {
	Action    RunAction `json:"action"`
	TimeMS    int64     `json:"time_ms"`
	SessionID string    `json:"session_id"`
	RunID     string    `json:"run_id"`
	Request   string    `json:"request,omitempty"`
	Replaces  string    `json:"replaces,omitempty"`
}

type OpenRun struct {
	RunID      string
	AdmittedMS int64
	Started    bool
	Request    string
}

type Runs interface {
	AppendRun(ctx context.Context, record RunRecord) error
	OpenRuns(ctx context.Context, sessionID string) ([]OpenRun, error)
}

func ends(record, held RunRecord) bool {
	if held.SessionID != record.SessionID {
		return false
	}
	if record.Action == RunSettled {
		return held.RunID == record.RunID
	}
	return record.Action == RunAdmitted && record.Replaces != "" && held.RunID == record.Replaces
}

func keptAfter(records []RunRecord, record RunRecord) []RunRecord {
	kept := records[:0]
	for _, held := range records {
		if !ends(record, held) {
			kept = append(kept, held)
		}
	}
	if record.Action == RunSettled {
		return kept
	}
	return append(kept, record)
}

func removeRun(runs []OpenRun, at map[string]int, runID string) []OpenRun {
	index, seen := at[runID]
	if !seen {
		return runs
	}
	runs = append(runs[:index], runs[index+1:]...)
	delete(at, runID)
	for other, later := range at {
		if later > index {
			at[other] = later - 1
		}
	}
	return runs
}

func openRuns(records []RunRecord, sessionID string) []OpenRun {
	var runs []OpenRun
	at := make(map[string]int)
	for _, record := range records {
		if record.SessionID != sessionID {
			continue
		}
		if record.Action == RunSettled {
			runs = removeRun(runs, at, record.RunID)
			continue
		}
		if record.Action == RunAdmitted && record.Replaces != "" {
			runs = removeRun(runs, at, record.Replaces)
		}
		index, seen := at[record.RunID]
		if !seen {
			index = len(runs)
			at[record.RunID] = index
			runs = append(runs, OpenRun{RunID: record.RunID, AdmittedMS: record.TimeMS})
		}
		switch record.Action {
		case RunAdmitted:
			runs[index].Request = record.Request
		case RunStarted:
			runs[index].Started = true
		}
	}
	return runs
}

type memoryRuns struct {
	mu      sync.Mutex
	records []RunRecord
}

func MemoryRuns() Runs {
	return &memoryRuns{}
}

func (s *memoryRuns) AppendRun(_ context.Context, record RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.records = keptAfter(s.records, record)
	return nil
}

func (s *memoryRuns) OpenRuns(_ context.Context, sessionID string) ([]OpenRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return openRuns(s.records, sessionID), nil
}

type fileRuns struct {
	mu   sync.Mutex
	path string
}

func RunsFile(path string) (Runs, error) {
	if path == "" {
		return nil, fmt.Errorf("binding: a run store needs a path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return &fileRuns{path: path}, nil
}

func (s *fileRuns) records() ([]RunRecord, error) {
	file, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var records []RunRecord
	reader := bufio.NewReader(file)
	for {
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return records, nil
		}
		if err != nil {
			return nil, err
		}
		payload, err := unframe(line)
		if err != nil {
			return records, nil
		}
		var record RunRecord
		if err := json.Unmarshal(payload, &record); err != nil {
			return records, nil
		}
		records = append(records, record)
	}
}

func marshalRun(record RunRecord) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(record); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buffer.Bytes(), []byte("\n")), nil
}

func (s *fileRuns) AppendRun(_ context.Context, record RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records()
	if err != nil {
		return err
	}
	var next []byte
	for _, held := range keptAfter(records, record) {
		payload, err := marshalRun(held)
		if err != nil {
			return err
		}
		next = append(next, frame(payload)...)
	}
	staging := s.path + ".writing"
	file, err := os.OpenFile(staging, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(next); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(staging, s.path)
}

func (s *fileRuns) OpenRuns(_ context.Context, sessionID string) ([]OpenRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	records, err := s.records()
	if err != nil {
		return nil, err
	}
	return openRuns(records, sessionID), nil
}
