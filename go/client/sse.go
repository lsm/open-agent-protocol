package client

import (
	"bufio"
	"bytes"
	"io"
)

type frame struct {
	event string

	data []byte

	lastID string
	hasID  bool
}

func scanSSE(r *bufio.Reader, handle func(frame) bool) error {
	scanner := &sseScanner{r: r}
	for {
		line, err := scanner.readLine()
		if err != nil {
			return err
		}
		if len(line) == 0 {
			if !scanner.dispatch(handle) {
				return nil
			}
			continue
		}
		scanner.field(line)
	}
}

type sseScanner struct {
	r       *bufio.Reader
	started bool
	event   string
	data    bytes.Buffer
	nData   int
	lastID  string
	hasID   bool
}

func (s *sseScanner) readLine() ([]byte, error) {
	var line []byte
	for {
		b, err := s.r.ReadByte()
		if err != nil {
			return line, err
		}
		switch b {
		case '\n':
			return s.open(line), nil
		case '\r':
			next, err := s.r.ReadByte()
			if err == io.EOF {

				return s.open(line), nil
			}
			if err != nil {
				return line, err
			}
			if next != '\n' {
				_ = s.r.UnreadByte()
			}
			return s.open(line), nil
		default:
			line = append(line, b)
		}
	}
}

func (s *sseScanner) open(line []byte) []byte {
	if !s.started {
		s.started = true
		return bytes.TrimPrefix(line, []byte{0xEF, 0xBB, 0xBF})
	}
	return line
}

func (s *sseScanner) field(line []byte) {
	if line[0] == ':' {
		return
	}
	name, value, found := bytes.Cut(line, []byte{':'})
	if found && len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(name) {
	case "event":
		s.event = string(value)
	case "data":
		if s.nData > 0 {
			s.data.WriteByte('\n')
		}
		s.nData++
		s.data.Write(value)
	case "id":
		if !bytes.ContainsRune(value, 0) {
			s.lastID = string(value)
			s.hasID = true
		}
	default:

	}
}

func (s *sseScanner) dispatch(handle func(frame) bool) bool {
	if s.nData == 0 {
		s.reset()
		return true
	}
	name := s.event
	if name == "" {
		name = "message"
	}
	cont := handle(frame{event: name, data: append([]byte(nil), s.data.Bytes()...), lastID: s.lastID, hasID: s.hasID})
	s.reset()
	return cont
}

func (s *sseScanner) reset() {
	s.event = ""
	s.data.Reset()
	s.nData = 0
	s.lastID, s.hasID = "", false
}
