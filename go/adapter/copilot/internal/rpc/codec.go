package rpc

import (
	"bufio"
	"errors"
	"io"
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
	return &Codec{reader: bufio.NewReaderSize(reader, 1025), writer: writer, limit: limit}
}

func (c *Codec) Read() ([]byte, error)   { return nil, ErrFrame }
func (c *Codec) Write(body []byte) error { return ErrFrame }
