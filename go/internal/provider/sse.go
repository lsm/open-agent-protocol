package provider

import (
	"bytes"
	"errors"
)

var (
	ErrLineTooLarge  = errors.New("sse line too large")
	ErrEventTooLarge = errors.New("sse event too large")
	ErrSSEParse      = errors.New("sse parse error")
)

const (
	DefaultLineLimit  = 1024 * 1024
	DefaultEventLimit = 4 * 1024 * 1024
)

type SSEEvent struct {
	Type    string
	HasType bool
	Data    string
}

type SSELimits struct {
	LineBytes  int
	EventBytes int
}

type SSEParser struct {
	limits    SSELimits
	line      []byte
	eventType string
	hasType   bool
	data      []byte
	hasData   bool
	pendingCR bool
}

func NewSSEParser() *SSEParser {
	return NewSSEParserWithLimits(SSELimits{})
}

func NewSSEParserWithLimits(limits SSELimits) *SSEParser {
	if limits.LineBytes <= 0 {
		limits.LineBytes = DefaultLineLimit
	}
	if limits.EventBytes <= 0 {
		limits.EventBytes = DefaultEventLimit
	}
	return &SSEParser{limits: limits}
}

func (p *SSEParser) Feed(chunk []byte) ([]SSEEvent, error) {
	var events []SSEEvent
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		if p.pendingCR {
			p.pendingCR = false
			if b == '\n' {
				continue
			}
		}
		switch b {
		case '\n':
			if err := p.finishLine(&events); err != nil {
				return nil, err
			}
		case '\r':
			if err := p.finishLine(&events); err != nil {
				return nil, err
			}
			p.pendingCR = true
		default:
			if err := p.appendLineByte(b); err != nil {
				return nil, err
			}
		}
	}
	return events, nil
}

func (p *SSEParser) Reset() {
	p.line = p.line[:0]
	p.pendingCR = false
	p.eventType = ""
	p.hasType = false
	p.data = p.data[:0]
	p.hasData = false
}

func (p *SSEParser) finishLine(events *[]SSEEvent) error {
	if len(p.line) == 0 {
		if event, ok := p.finalizeEvent(); ok {
			*events = append(*events, event)
		}
		return nil
	}
	if err := p.processLine(p.line); err != nil {
		return err
	}
	p.line = p.line[:0]
	return nil
}

func (p *SSEParser) processLine(line []byte) error {
	if len(line) == 0 {
		return nil
	}
	if line[0] == ':' {
		return nil
	}
	field := line
	var value []byte
	if at := bytes.IndexByte(line, ':'); at >= 0 {
		field = line[:at]
		value = line[at+1:]
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	switch string(field) {
	case "event":
		return p.setEventType(value)
	case "data":
		return p.appendEventData(value)
	}
	return nil
}

func (p *SSEParser) finalizeEvent() (SSEEvent, bool) {
	if !p.hasData {
		return SSEEvent{}, false
	}
	event := SSEEvent{Type: p.eventType, HasType: p.hasType, Data: string(p.data)}
	p.eventType = ""
	p.hasType = false
	p.data = p.data[:0]
	p.hasData = false
	return event, true
}

func (p *SSEParser) appendLineByte(b byte) error {
	if len(p.line) >= p.limits.LineBytes {
		return ErrLineTooLarge
	}
	p.line = append(p.line, b)
	return nil
}

func (p *SSEParser) setEventType(t []byte) error {
	if len(t) > subtractFloor(p.limits.EventBytes, len(p.data)) {
		return ErrEventTooLarge
	}
	p.eventType = string(t)
	p.hasType = true
	return nil
}

func (p *SSEParser) appendEventData(v []byte) error {
	separator := 0
	if p.hasData {
		separator = 1
	}
	afterType := subtractFloor(p.limits.EventBytes, len(p.eventType))
	remaining := subtractFloor(afterType, len(p.data))
	if separator > remaining || len(v) > remaining-separator {
		return ErrEventTooLarge
	}
	if separator != 0 {
		p.data = append(p.data, '\n')
	}
	p.data = append(p.data, v...)
	p.hasData = true
	return nil
}

func subtractFloor(total, part int) int {
	if part >= total {
		return 0
	}
	return total - part
}

func SSEFrame(data string) string {
	return "data: " + data + "\n\n"
}

func SSEErrorMessage(err error) string {
	switch {
	case errors.Is(err, ErrLineTooLarge):
		return "sse line too large"
	case errors.Is(err, ErrEventTooLarge):
		return "sse event too large"
	default:
		return "sse parse error"
	}
}
