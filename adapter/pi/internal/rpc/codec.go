package rpc

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"github.com/lsm/open-agent-protocol/adapter/pi/internal/native"
)

const DefaultFrameLimit = 8 << 20

var (
	ErrFrameTooLarge = errors.New("pi rpc: frame exceeds configured limit")
	ErrInvalidFrame  = errors.New("pi rpc: invalid JSONL frame")
)

type Frame struct {
	Response         *native.Response
	Event            *native.Event
	ExtensionRequest *native.ExtensionUIRequest
}

type Decoder struct {
	reader *bufio.Reader
	limit  int
}

func NewDecoder(reader io.Reader, limit int) *Decoder {
	if limit <= 0 {
		limit = DefaultFrameLimit
	}
	return &Decoder{reader: bufio.NewReader(reader), limit: limit}
}

func (d *Decoder) Decode() (Frame, error) {
	data, err := d.readFrame()
	if err != nil {
		return Frame{}, err
	}
	var header struct {
		Type string `json:"type"`
	}
	if err := decodeHeader(data, &header); err != nil || header.Type == "" {
		return Frame{}, fmt.Errorf("%w: missing type", ErrInvalidFrame)
	}
	switch header.Type {
	case "response":
		var response native.Response
		if err := native.DecodeStrict(data, &response); err != nil {
			return Frame{}, fmt.Errorf("%w: response: %v", ErrInvalidFrame, err)
		}
		if err := response.Validate(); err != nil {
			return Frame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
		}
		return Frame{Response: &response}, nil
	case "extension_ui_request":
		var request native.ExtensionUIRequest
		if err := native.DecodeStrict(data, &request); err != nil {
			return Frame{}, fmt.Errorf("%w: extension request: %v", ErrInvalidFrame, err)
		}
		if err := request.Validate(); err != nil {
			return Frame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
		}
		return Frame{ExtensionRequest: &request}, nil
	default:
		typeValue := native.EventType(header.Type)
		if err := native.ValidateEvent(data, typeValue); err != nil {
			return Frame{}, fmt.Errorf("%w: event: %v", ErrInvalidFrame, err)
		}
		return Frame{Event: &native.Event{Type: typeValue, Raw: append(json.RawMessage(nil), data...)}}, nil
	}
}

func decodeHeader(data []byte, dst any) error {

	var object map[string]json.RawMessage
	if err := native.DecodeStrict(data, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("expected object")
	}
	value, ok := object["type"]
	if !ok {
		return errors.New("missing type")
	}
	return json.Unmarshal(value, &dst.(*struct {
		Type string `json:"type"`
	}).Type)
}

func (d *Decoder) readFrame() ([]byte, error) {
	frame := make([]byte, 0, min(d.limit, 4096))
	for {
		part, err := d.reader.ReadSlice('\n')
		if len(frame)+len(part) > d.limit+1 {
			return nil, ErrFrameTooLarge
		}
		frame = append(frame, part...)
		switch {
		case err == nil:
			frame = frame[:len(frame)-1]
			if bytes.IndexByte(frame, '\r') >= 0 {
				return nil, fmt.Errorf("%w: carriage return is not valid framing", ErrInvalidFrame)
			}
			if len(frame) == 0 {
				return nil, fmt.Errorf("%w: empty frame", ErrInvalidFrame)
			}
			if !utf8.Valid(frame) {
				return nil, fmt.Errorf("%w: frame is not UTF-8", ErrInvalidFrame)
			}
			return frame, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(frame) > 0:
			return nil, fmt.Errorf("%w: unterminated frame", ErrInvalidFrame)
		default:
			return nil, err
		}
	}
}

type Encoder struct {
	writer io.Writer
	limit  int
}

func NewEncoder(writer io.Writer, limit ...int) *Encoder {
	n := DefaultFrameLimit
	if len(limit) > 0 && limit[0] > 0 {
		n = limit[0]
	}
	return &Encoder{writer: writer, limit: n}
}

func (e *Encoder) Encode(value any) error {
	switch message := value.(type) {
	case native.Command:
		if err := message.Validate(); err != nil {
			return err
		}
	case native.ExtensionUIResponse:
		if err := message.Validate(); err != nil {
			return err
		}
	case *native.Command:
		if message == nil {
			return native.ErrInvalid
		}
		if err := message.Validate(); err != nil {
			return err
		}
	case *native.ExtensionUIResponse:
		if message == nil {
			return native.ErrInvalid
		}
		if err := message.Validate(); err != nil {
			return err
		}
	default:
		return fmt.Errorf("%w: unsupported outbound value %T", ErrInvalidFrame, value)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > e.limit {
		return ErrFrameTooLarge
	}
	data = append(data, '\n')
	for len(data) > 0 {
		n, err := e.writer.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
