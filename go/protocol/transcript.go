package protocol

import (
	"encoding/json"
	"slices"
)

const FeatureTranscriptLoad = "transcript.load"

const (
	TranscriptLoadDefaultLimit = 100
	TranscriptLoadMaxLimit     = 500
	TranscriptPageBytes        = 512 << 10
)

type TranscriptEntryID string

type TranscriptLoadRequest struct {
	SessionID             SessionID `json:"session_id"`
	Cursor                string    `json:"cursor,omitempty"`
	Limit                 int       `json:"limit,omitempty"`
	AllowDegradedFeatures []string  `json:"allow_degraded_features,omitempty"`
}

func (r TranscriptLoadRequest) AllowsDegraded(key string) bool {
	return slices.Contains(r.AllowDegradedFeatures, key)
}

type TranscriptEntryKind string

const (
	TranscriptMessage    TranscriptEntryKind = "message"
	TranscriptCompaction TranscriptEntryKind = "compaction"
)

type TranscriptCompactionEntry struct {
	CompactionID CompactionID `json:"compaction_id,omitempty"`
	Summary      string       `json:"summary"`
}

type TranscriptEntry struct {
	EntryID    TranscriptEntryID          `json:"entry_id"`
	Kind       TranscriptEntryKind        `json:"kind"`
	AtMS       int64                      `json:"at_ms,omitempty"`
	RunID      RunID                      `json:"run_id,omitempty"`
	Message    *Message                   `json:"message,omitempty"`
	Compaction *TranscriptCompactionEntry `json:"compaction,omitempty"`
	Truncated  bool                       `json:"truncated,omitempty"`
	Extensions map[string]json.RawMessage `json:"extensions,omitempty"`
}

type TranscriptGapReason string

const (
	TranscriptGapPruned    TranscriptGapReason = "pruned"
	TranscriptGapRewritten TranscriptGapReason = "rewritten"
)

type TranscriptGap struct {
	Reason    TranscriptGapReason `json:"reason"`
	ResumedAt TranscriptEntryID   `json:"resumed_at,omitempty"`
}

type TranscriptLoadResponse struct {
	SessionID  SessionID         `json:"session_id"`
	Entries    []TranscriptEntry `json:"entries"`
	NextCursor string            `json:"next_cursor,omitempty"`
	Complete   bool              `json:"complete"`
	Gap        *TranscriptGap    `json:"gap,omitempty"`
}
