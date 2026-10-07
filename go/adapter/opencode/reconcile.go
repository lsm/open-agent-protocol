package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"slices"

	"github.com/lsm/open-agent-protocol/go/adapter/opencode/internal/native"
)

const reconcilePagesMax = 16

type storedRecord struct {
	ID      native.MessageID       `json:"id"`
	Type    string                 `json:"type"`
	Outcome string                 `json:"outcome"`
	Finish  string                 `json:"finish"`
	Error   *native.SessionError   `json:"error"`
	Cost    float64                `json:"cost"`
	Tokens  native.TokenAccounting `json:"tokens"`
	Time    struct {
		Completed int64 `json:"completed"`
	} `json:"time"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Time struct {
			Completed int64 `json:"completed"`
		} `json:"time"`
	} `json:"content"`
}

func (s *session) recover() bool {
	reader, ok := s.client.(messageReader)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.timeout)
	defer cancel()
	subCtx, subCancel := context.WithCancel(context.Background())
	stopWaiting := context.AfterFunc(ctx, subCancel)
	subscription, err := s.client.Subscribe(subCtx, s.nativeID)
	if waited := !stopWaiting(); err != nil || waited {
		if err == nil {
			_ = subscription.Close()
		}
		subCancel()
		return false
	}
	s.transitionMu.Lock()
	defer s.transitionMu.Unlock()
	events, err := s.reconciliation(ctx, reader)
	s.mu.Lock()
	if err != nil || s.closed {
		s.mu.Unlock()
		_ = subscription.Close()
		subCancel()
		return false
	}
	previous := s.subCancel
	s.subscription, s.events, s.subCancel = subscription, subscription.Events(), subCancel
	s.replaying = true
	s.mu.Unlock()
	previous()
	for _, event := range events {
		s.handleEventLocked(event)
	}
	s.mu.Lock()
	s.replaying = false
	s.mu.Unlock()
	return true
}

func (s *session) reconciliation(ctx context.Context, reader messageReader) ([]native.Event, error) {
	s.mu.Lock()
	active, reserved := s.active, s.reserved
	var oldest, delivered native.MessageID
	switch {
	case active != nil && !active.terminal:
		oldest = active.nativeMessageID
		if active.prompted {
			delivered = oldest
		}
	case reserved != nil && !reserved.terminal:
		oldest = reserved.nativeMessageID
	}
	s.mu.Unlock()
	if oldest == "" {
		return nil, nil
	}
	records, err := s.recordsFrom(ctx, reader, oldest)
	if err != nil {
		return nil, err
	}
	if records == nil {
		if delivered != "" {
			return nil, errors.New("the session record does not hold the open run's input")
		}
		return nil, nil
	}
	settled := true
	for _, record := range records {
		switch record.Type {
		case "user":
			settled = false
		case "idle":
			settled = true
		}
	}
	running := false
	if !settled {
		active, err := s.client.Active(ctx)
		if err != nil {
			return nil, err
		}
		running = active[s.nativeID]
	}
	return s.replayed(records, delivered, settled || running), nil
}

func (s *session) recordsFrom(ctx context.Context, reader messageReader, oldest native.MessageID) ([]storedRecord, error) {
	var newest []storedRecord
	cursor := ""
	for pages := 0; pages < reconcilePagesMax; pages++ {
		page, err := reader.Messages(ctx, s.nativeID, cursor, nativeReadPage, true)
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Data {
			var record storedRecord
			if json.Unmarshal(raw, &record) != nil {
				continue
			}
			newest = append(newest, record)
			if record.ID == oldest {
				slices.Reverse(newest)
				return newest, nil
			}
		}
		if page.Cursor.Next == "" || page.Cursor.Next == cursor {
			break
		}
		cursor = page.Cursor.Next
	}
	return nil, nil
}

func (s *session) replayed(records []storedRecord, delivered native.MessageID, live bool) []native.Event {
	var events []native.Event
	add := func(typ native.Type, data any) {
		raw, err := json.Marshal(data)
		if err == nil {
			events = append(events, native.Event{Type: typ, Data: raw, SessionID: s.nativeID})
		}
	}
	for _, record := range records {
		switch record.Type {
		case "user":
			if record.ID != delivered {
				add(native.TypeInboxDelivered, native.InboxRefData{SessionID: s.nativeID, InboxID: record.ID})
			}
		case "assistant":
			done := record.Time.Completed > 0
			texts, thoughts := 0, 0
			for _, part := range record.Content {
				switch part.Type {
				case "text":
					if done || part.Text != "" {
						add(native.TypeTextEnded, native.TextEndedData{SessionID: s.nativeID, AssistantMessage: record.ID, Ordinal: texts, Text: part.Text})
					}
					texts++
				case "reasoning":
					if done || part.Time.Completed > 0 {
						add(native.TypeReasoningEnded, native.ReasoningEndedData{SessionID: s.nativeID, AssistantMessage: record.ID, Ordinal: thoughts, Text: part.Text})
					}
					thoughts++
				}
			}
			if !done {
				continue
			}
			if record.Error != nil {
				cost := record.Cost
				add(native.TypeStepFailed, native.StepFailedData{SessionID: s.nativeID, AssistantMessage: record.ID, Error: *record.Error, Cost: &cost})
				continue
			}
			add(native.TypeStepEnded, native.StepEndedData{SessionID: s.nativeID, AssistantMessage: record.ID, Finish: record.Finish, Cost: record.Cost, Tokens: record.Tokens})
		case "idle":
			switch record.Outcome {
			case "succeeded":
				add(native.TypeExecutionSucceeded, native.ExecutionData{SessionID: s.nativeID})
			case "failed":
				add(native.TypeExecutionFailed, native.ExecutionFailedData{SessionID: s.nativeID, Error: native.SessionError{Type: "unknown", Message: "OpenCode recorded the execution as failed"}})
			case "interrupted":
				add(native.TypeExecutionInterrupted, native.ExecutionInterruptedData{SessionID: s.nativeID, Reason: "recorded while the event stream was down"})
			}
		}
	}
	if !live {
		add(native.TypeExecutionInterrupted, native.ExecutionInterruptedData{SessionID: s.nativeID, Reason: "the server ended the execution without recording it while the event stream was down"})
	}
	return events
}
