package sdk

import (
	"encoding/json"
	"errors"
	"io"
)

func mustMarshal(payload any) json.RawMessage {
	if payload == nil {
		return json.RawMessage("{}")
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return json.RawMessage("{}")
	}
	return encoded
}

const maxFrameBytes = 16 << 20

type frameReader struct {
	reader *bufferedLineReader
}

func newFrameReader(r io.Reader) *frameReader {
	return &frameReader{reader: newBufferedLineReader(r, maxFrameBytes)}
}

var errMalformedFrame = errors.New("oap sdk: malformed JSON frame")

func (fr *frameReader) nextInbound() (*inbound, error) {
	line, err := fr.reader.readLine()
	if err != nil {
		return nil, err
	}
	return decodeInbound(line)
}
