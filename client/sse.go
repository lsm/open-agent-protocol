package client

import (
	"bufio"
	"bytes"
	"io"
)

// frame is one dispatched text/event-stream event: a blank line's worth of
// accumulated field lines.
type frame struct {
	// event is the dispatch name, "message" when no event field was set.
	event string
	// data is the joined data lines (no trailing newline).
	data []byte
	// lastID is the id field's value; hasID reports whether one was set.
	lastID string
	hasID  bool
}

// scanSSE reads one text/event-stream document from r and invokes handle once
// per dispatched frame. It follows the WHATWG parsing rules that matter on
// this wire: CR, LF, and CRLF all terminate lines; a leading UTF-8 BOM is
// stripped; comment lines (leading ':') and unknown fields such as retry are
// ignored; one optional space after the field colon is dropped; multiple data
// lines join with newlines; an id containing NUL is discarded; a frame is
// dispatched only at its blank line, so an unterminated trailing frame is
// discarded. handle returning false stops the scan without reading further.
// The returned error is the read error that ended the stream; io.EOF means
// the document ended cleanly.
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

// readLine returns the next line without its CR, LF, or CRLF terminator. A
// final line at io.EOF carries the error out undelivered, so an unterminated
// trailing frame never dispatches.
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
				// A CR at end of file still terminates its line.
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

// open finalizes a line, stripping a UTF-8 byte-order mark from the stream's
// first line only.
func (s *sseScanner) open(line []byte) []byte {
	if !s.started {
		s.started = true
		return bytes.TrimPrefix(line, []byte{0xEF, 0xBB, 0xBF})
	}
	return line
}

// field applies one field line to the frame under construction.
func (s *sseScanner) field(line []byte) {
	if line[0] == ':' {
		return // comment or keepalive
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
		// "retry" and any unknown field are ignored.
	}
}

// dispatch emits the accumulated frame, if any, resets the accumulator, and
// reports whether scanning should continue.
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
