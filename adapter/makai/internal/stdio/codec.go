package stdio

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/lsm/open-agent-protocol/adapter/makai/internal/native"
)

const DefaultFrameLimit = 8 << 20

var (
	ErrFrameTooLarge = errors.New("makai stdio: frame exceeds configured limit")
	ErrInvalidFrame  = errors.New("makai stdio: invalid JSONL frame")
)

type Ready struct {
	Type            string `json:"type"`
	ProtocolVersion string `json:"protocol_version"`
}
type Frame struct {
	Ready    *Ready
	Envelope *native.Envelope
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
	if err := decodeStrict(data, &header, false); err != nil || header.Type == "" {
		return Frame{}, fmt.Errorf("%w: missing type", ErrInvalidFrame)
	}
	if header.Type == "ready" {
		var ready Ready
		if err := decodeStrict(data, &ready, true); err != nil {
			return Frame{}, fmt.Errorf("%w: ready: %v", ErrInvalidFrame, err)
		}
		if ready.ProtocolVersion != "1" {
			return Frame{}, fmt.Errorf("%w: unsupported ready protocol version %q", ErrInvalidFrame, ready.ProtocolVersion)
		}
		return Frame{Ready: &ready}, nil
	}
	if err := requireObjectMembers(data, "type", "session_id", "message_id", "sequence", "timestamp", "version", "payload"); err != nil {
		return Frame{}, fmt.Errorf("%w: envelope: %v", ErrInvalidFrame, err)
	}
	var env native.Envelope
	if err := decodeStrict(data, &env, true); err != nil {
		return Frame{}, fmt.Errorf("%w: envelope: %v", ErrInvalidFrame, err)
	}
	if err := env.Validate(true); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", ErrInvalidFrame, err)
	}
	return Frame{Envelope: &env}, nil
}
func (d *Decoder) readFrame() ([]byte, error) {
	frame := make([]byte, 0, 4096)
	for {
		part, err := d.reader.ReadSlice('\n')
		if len(frame)+len(part) > d.limit+1 {
			return nil, ErrFrameTooLarge
		}
		frame = append(frame, part...)
		switch {
		case err == nil:
			frame = frame[:len(frame)-1]
			if len(frame) > 0 && frame[len(frame)-1] == '\r' {
				return nil, fmt.Errorf("%w: CRLF framing is not supported", ErrInvalidFrame)
			}
			if len(frame) == 0 {
				return nil, fmt.Errorf("%w: empty frame", ErrInvalidFrame)
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
func (e *Encoder) Encode(env native.Envelope) error {
	if err := env.Validate(false); err != nil {
		return err
	}
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	if len(data) > e.limit {
		return ErrFrameTooLarge
	}
	data = append(data, '\n')
	for len(data) > 0 {
		n, werr := e.writer.Write(data)
		if werr != nil {
			return werr
		}
		if n <= 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
func decodeStrict(data []byte, dst any, unknown bool) error {
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if unknown {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func requireObjectMembers(data []byte, required ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	for _, name := range required {
		if _, ok := object[name]; !ok {
			return fmt.Errorf("missing %s", name)
		}
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := make(map[string]struct{})
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, duplicate := seen[key]; duplicate {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		case '[':
			for dec.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := dec.Token()
			return err
		default:
			return errors.New("unexpected closing delimiter")
		}
	}
	if err := walk(); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
