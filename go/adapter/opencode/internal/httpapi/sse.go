package httpapi

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
)

const DefaultFrameLimit = 8 << 20

var (
	ErrFrameTooLarge = errors.New("opencode httpapi: SSE event exceeds configured limit")
	ErrInvalidFrame  = errors.New("opencode httpapi: invalid SSE frame")
)

type SSEEvent struct {
	Name string
	ID   string
	Data []byte
}

type SSEDecoder struct {
	reader *bufio.Reader
	limit  int
}

func NewSSEDecoder(reader io.Reader, limit int) *SSEDecoder {
	if limit <= 0 {
		limit = DefaultFrameLimit
	}
	return &SSEDecoder{reader: bufio.NewReaderSize(reader, 64<<10), limit: limit}
}

func (d *SSEDecoder) Decode() (SSEEvent, error) {
	var data bytes.Buffer
	event := SSEEvent{}
	size := 0
	for {
		line, err := d.readLine()
		if err != nil {
			return SSEEvent{}, err
		}
		size += len(line)
		if size > d.limit {
			return SSEEvent{}, ErrFrameTooLarge
		}
		if len(line) == 0 {
			if data.Len() == 0 && event.Name == "" && event.ID == "" {
				continue
			}
			if data.Len() == 0 {
				return SSEEvent{}, fmt.Errorf("%w: event without data", ErrInvalidFrame)
			}
			event.Data = append([]byte(nil), data.Bytes()...)
			return event, nil
		}
		if line[0] == ':' {
			continue
		}
		name, value, found := strings.Cut(string(line), ":")
		if found && strings.HasPrefix(value, " ") {
			value = value[1:]
		}
		switch name {
		case "data":
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(value)
		case "event":
			if event.Name != "" {
				return SSEEvent{}, fmt.Errorf("%w: duplicate event field", ErrInvalidFrame)
			}
			event.Name = value
		case "id":
			if event.ID != "" {
				return SSEEvent{}, fmt.Errorf("%w: duplicate id field", ErrInvalidFrame)
			}
			if strings.ContainsRune(value, '\x00') {
				return SSEEvent{}, fmt.Errorf("%w: id contains NUL", ErrInvalidFrame)
			}
			event.ID = value
		case "retry":

		default:

		}
	}
}

func (d *SSEDecoder) readLine() ([]byte, error) {
	line := make([]byte, 0, 1024)
	for {
		part, err := d.reader.ReadSlice('\n')
		if len(line)+len(part) > d.limit+2 {
			return nil, ErrFrameTooLarge
		}
		line = append(line, part...)
		switch {
		case err == nil:
			line = line[:len(line)-1]
			if n := len(line); n > 0 && line[n-1] == '\r' {
				line = line[:n-1]
			}
			if idx := bytes.IndexByte(line, '\r'); idx >= 0 {
				return nil, fmt.Errorf("%w: bare CR inside line", ErrInvalidFrame)
			}
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return nil, fmt.Errorf("%w: unterminated line", ErrInvalidFrame)
		default:
			return nil, err
		}
	}
}
