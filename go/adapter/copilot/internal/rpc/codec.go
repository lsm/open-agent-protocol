package rpc

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"unicode/utf8"
)

const DefaultFrameLimit = 8 << 20

var ErrFrame = errors.New("invalid Copilot Content-Length frame")

type Codec struct {
	reader *bufio.Reader
	writer io.Writer
	limit  int
}

func NewCodec(reader io.Reader, writer io.Writer, limit int) *Codec {
	if limit <= 0 {
		limit = DefaultFrameLimit
	}
	return &Codec{reader: bufio.NewReaderSize(reader, 1028), writer: writer, limit: limit}
}

func (c *Codec) Read() ([]byte, error) {
	length, size := uint64(0), 0
	found := false
	for {
		line, err := c.reader.ReadSlice('\n')
		if err != nil {
			if errors.Is(err, io.EOF) && size == 0 && len(line) == 0 {
				return nil, io.EOF
			}
			return nil, ErrFrame
		}
		size += len(line)
		if size > 1028 || !bytes.HasSuffix(line, []byte("\r\n")) {
			return nil, ErrFrame
		}
		if bytes.Equal(line, []byte("\r\n")) {
			break
		}
		key, value, ok := bytes.Cut(line[:len(line)-2], []byte(":"))
		if !ok {
			return nil, ErrFrame
		}
		if !bytes.EqualFold(bytes.Trim(key, " "), []byte("Content-Length")) {
			continue
		}
		if found {
			return nil, ErrFrame
		}
		found = true
		value = bytes.Trim(value, " ")
		if len(value) == 0 {
			return nil, ErrFrame
		}
		for _, digit := range value {
			if digit < '0' || digit > '9' {
				return nil, ErrFrame
			}
		}
		length, err = strconv.ParseUint(string(value), 10, 64)
		if err != nil {
			return nil, ErrFrame
		}
	}
	if !found || length == 0 || length > uint64(c.limit) {
		return nil, ErrFrame
	}
	body := make([]byte, int(length))
	if _, err := io.ReadFull(c.reader, body); err != nil {
		return nil, ErrFrame
	}
	if !utf8.Valid(body) {
		return nil, ErrFrame
	}
	return body, nil
}

func (c *Codec) Write(body []byte) error {
	if len(body) == 0 || len(body) > c.limit || !utf8.Valid(body) {
		return ErrFrame
	}
	frame := fmt.Appendf(nil, "Content-Length: %d\r\n\r\n", len(body))
	frame = append(frame, body...)
	for len(frame) > 0 {
		n, err := c.writer.Write(frame)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		frame = frame[n:]
	}
	return nil
}
