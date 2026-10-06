package binding

import (
	"context"
	"sync"
)

type memoryStore struct {
	mu      sync.Mutex
	entries []Entry
}

func Memory() Store {
	return &memoryStore{}
}

func (s *memoryStore) Append(_ context.Context, entry Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Record.ToolSourceIDs = append([]string(nil), entry.Record.ToolSourceIDs...)
	s.entries = append(s.entries, entry)
	return nil
}

func (s *memoryStore) Latest(ctx context.Context, sessionID string) (Entry, bool, error) {
	history, err := s.History(ctx, sessionID)
	if err != nil || len(history) == 0 {
		return Entry{}, false, err
	}
	return history[len(history)-1], true, nil
}

func (s *memoryStore) History(_ context.Context, sessionID string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var history []Entry
	for _, entry := range s.entries {
		if entry.Record.SessionID == sessionID {
			history = append(history, entry)
		}
	}
	return history, nil
}

func (s *memoryStore) Sessions(_ context.Context) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Sessions(s.entries), nil
}
