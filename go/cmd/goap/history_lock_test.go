//go:build unix

package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestASecondHubOnOneSessionHistoryIsRefusedWhileTheFirstHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-yet", "sessions.jsonl")
	first, err := lockSessionHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lockSessionHistory(path); err == nil || !strings.Contains(err.Error(), "--session-history") {
		t.Fatalf("second lock = %v, want a refusal naming --session-history", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := lockSessionHistory(path)
	if err != nil {
		t.Fatalf("lock after the first closed = %v", err)
	}
	_ = second.Close()
}
