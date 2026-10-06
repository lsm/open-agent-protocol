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
	mu        sync.Mutex
	path      string
	validated int64
	confirmed bool
	lastStart int64
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
	store := &fileStore{path: path}
	if err := store.repair(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *fileStore) repair() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.repairLocked()
}

func (s *fileStore) repairLocked() error {
	info, err := os.Stat(s.path)
	if err != nil {
		return err
	}
	if info.Size() == 0 {
		s.forget()
		return nil
	}
	if s.confirmed && info.Size() == s.validated {
		return nil
	}
	file, err := os.OpenFile(s.path, os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	from := int64(0)
	if s.confirmed && info.Size() >= s.lastStart {
		if line, readErr := readLineAt(file, s.lastStart, info.Size()); readErr == nil {
			if _, decodeErr := decode(line); decodeErr == nil {
				from = s.validated
			}
		}
	}
	keep, err := s.validatedThrough(file, from, info.Size())
	if err != nil {
		return err
	}
	if keep != info.Size() {
		if err := file.Truncate(keep); err != nil {
			return err
		}
	}
	s.validated = keep
	s.confirmed = true
	s.lastStart = lastRecordStart(file, keep)
	return nil
}

func (s *fileStore) forget() {
	s.validated = 0
	s.confirmed = false
	s.lastStart = 0
}

func readLineAt(file *os.File, start, size int64) (string, error) {
	if start >= size {
		return "", errors.New("binding: no record at that offset")
	}
	buffer := make([]byte, 0, 512)
	chunk := make([]byte, 512)
	for offset := start; offset < size; {
		read, err := file.ReadAt(chunk, offset)
		if read > 0 {
			offset += int64(read)
			for _, b := range chunk[:read] {
				buffer = append(buffer, b)
				if b == '\n' {
					return string(buffer), nil
				}
			}
		}
		if err != nil {
			break
		}
	}
	return "", errors.New("binding: a record with no line end")
}

func (s *fileStore) validatedThrough(file *os.File, from, size int64) (int64, error) {
	if from >= size {
		return size, nil
	}
	tail := make([]byte, size-from)
	read, err := file.ReadAt(tail, from)
	if read == 0 {
		return from, err
	}
	tail = tail[:read]
	keep := from
	start := 0
	for index, b := range tail {
		if b != '\n' {
			continue
		}
		if _, err := decode(string(tail[start : index+1])); err != nil {
			return keep, nil
		}
		keep = from + int64(index+1)
		start = index + 1
	}
	return keep, nil
}

func lastRecordStart(file *os.File, size int64) int64 {
	if size == 0 {
		return 0
	}
	window := int64(4096)
	if size < window {
		window = size
	}
	buffer := make([]byte, window)
	read, err := file.ReadAt(buffer, size-window)
	if read == 0 && err != nil {
		return 0
	}
	buffer = buffer[:read]
	for index := len(buffer) - 1; index > 0; index-- {
		if buffer[index-1] == '\n' {
			return size - window + int64(index)
		}
	}
	return 0
}

func (s *fileStore) Append(_ context.Context, entry Entry) error {
	line, err := encode(entry)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.repairLocked(); err != nil {
		return err
	}
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
	entries, err := s.History(ctx, sessionID)
	if err != nil || len(entries) == 0 {
		return Entry{}, false, err
	}
	return entries[len(entries)-1], true, nil
}

func (s *fileStore) History(ctx context.Context, sessionID string) ([]Entry, error) {
	return s.read(ctx, func(entry Entry) bool { return entry.Record.SessionID == sessionID })
}

func (s *fileStore) Sessions(ctx context.Context) ([]Entry, error) {
	entries, err := s.read(ctx, func(Entry) bool { return true })
	if err != nil {
		return nil, err
	}
	return Sessions(entries), nil
}

func (s *fileStore) read(ctx context.Context, keep func(Entry) bool) ([]Entry, error) {
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
			s.forget()
			return nil, fmt.Errorf("%w: %d bytes of a record with no line end", ErrTorn, len(line))
		}
		entry, err := decode(line)
		if err != nil {
			s.forget()
			return nil, err
		}
		if keep(entry) {
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
