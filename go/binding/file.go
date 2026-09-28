package binding

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type fileStore struct {
	mu   sync.Mutex
	path string
}

func File(path string) (Store, error) {
	if path == "" {
		return nil, fmt.Errorf("binding: a file store needs a path")
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		if err := file.Close(); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	return &fileStore{path: path}, nil
}

func (s *fileStore) Append(_ context.Context, entry Entry) error {
	line, err := encode(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.OpenFile(s.path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return err
	}
	return file.Sync()
}

func encode(entry Entry) ([]byte, error) {
	payload, err := json.Marshal(entry)
	if err != nil {
		return nil, err
	}
	sum := crc32.ChecksumIEEE(payload)
	return []byte(fmt.Sprintf("%08x %s\n", sum, payload)), nil
}

func (s *fileStore) Latest(ctx context.Context, sessionID string) (Entry, bool, error) {
	entries, err := s.read(ctx, sessionID)
	if err != nil || len(entries) == 0 {
		return Entry{}, false, err
	}
	return entries[len(entries)-1], true, nil
}

func (s *fileStore) History(ctx context.Context, sessionID string) ([]Entry, error) {
	return s.read(ctx, sessionID)
}

func (s *fileStore) read(ctx context.Context, sessionID string) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	var entries []Entry
	reader := bufio.NewReader(file)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, err := reader.ReadString('\n')
		if err != nil {
			if len(line) == 0 && errors.Is(err, io.EOF) {
				return entries, nil
			}
			return nil, fmt.Errorf("%w: %d bytes of a record with no line end", ErrTorn, len(line))
		}
		entry, err := decode(line)
		if err != nil {
			return nil, err
		}
		if entry.Record.SessionID == sessionID {
			entries = append(entries, entry)
		}
	}
}

func decode(line string) (Entry, error) {
	sum, payload, found := strings.Cut(strings.TrimSuffix(line, "\n"), " ")
	if !found {
		return Entry{}, fmt.Errorf("%w: a record with no checksum", ErrTorn)
	}
	if fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(payload))) != sum {
		return Entry{}, fmt.Errorf("%w: a record whose checksum does not match its bytes", ErrTorn)
	}
	var entry Entry
	if err := json.Unmarshal([]byte(payload), &entry); err != nil {
		return Entry{}, fmt.Errorf("%w: %v", ErrTorn, err)
	}
	return entry, nil
}
