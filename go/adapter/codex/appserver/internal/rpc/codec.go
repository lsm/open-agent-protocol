package rpc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const DefaultFrameLimit = 8 << 20

var ErrFrameTooLarge = errors.New("codex app-server rpc: frame exceeds configured limit")

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

func (decoder *Decoder) Decode() (Message, error) {
	frame, err := decoder.readFrame()
	if err != nil {
		return Message{}, err
	}
	return ParseMessage(frame)
}

func (decoder *Decoder) readFrame() ([]byte, error) {
	frame := make([]byte, 0, 4096)
	for {
		fragment, err := decoder.reader.ReadSlice('\n')
		if len(frame)+len(fragment) > decoder.limit+1 {
			return nil, ErrFrameTooLarge
		}
		frame = append(frame, fragment...)
		switch {
		case err == nil:
			frame = frame[:len(frame)-1]
			if len(frame) == 0 {
				return nil, fmt.Errorf("%w: empty frame", ErrInvalidMessage)
			}
			return frame, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(frame) > 0:
			return nil, fmt.Errorf("%w: unterminated frame", ErrInvalidMessage)
		default:
			return nil, err
		}
	}
}

type Encoder struct {
	writer io.Writer
}

func NewEncoder(writer io.Writer) *Encoder { return &Encoder{writer: writer} }

func (encoder *Encoder) Encode(message Message) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	for len(data) > 0 {
		written, writeErr := encoder.writer.Write(data)
		if writeErr != nil {
			return writeErr
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}
