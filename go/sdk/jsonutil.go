package sdk

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type jsonObject map[string]any

func decodeObject(raw json.RawMessage) (jsonObject, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, false
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return nil, false
	}
	return jsonObject(obj), true
}

func (o jsonObject) str(keys ...string) string {
	for _, key := range keys {
		if value, ok := o[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func (o jsonObject) strOrDefault(fallback string, keys ...string) string {
	if value := o.str(keys...); value != "" {
		return value
	}
	return fallback
}

func (o jsonObject) num(keys ...string) (float64, bool) {
	for _, key := range keys {
		if value, ok := o[key].(float64); ok {
			return value, true
		}
	}
	return 0, false
}

func (o jsonObject) intOr(fallback int, keys ...string) int {
	if value, ok := o.num(keys...); ok {
		return int(value)
	}
	return fallback
}

func (o jsonObject) boolean(keys ...string) (bool, bool) {
	for _, key := range keys {
		if value, ok := o[key].(bool); ok {
			return value, true
		}
	}
	return false, false
}

func (o jsonObject) obj(keys ...string) jsonObject {
	for _, key := range keys {
		if value, ok := o[key].(map[string]any); ok {
			return jsonObject(value)
		}
	}
	return nil
}

func (o jsonObject) arr(keys ...string) []any {
	for _, key := range keys {
		if value, ok := o[key].([]any); ok {
			return value
		}
	}
	return nil
}

type bufferedLineReader struct {
	reader *bufio.Reader
	limit  int
}

func newBufferedLineReader(r io.Reader, limit int) *bufferedLineReader {
	return &bufferedLineReader{reader: bufio.NewReaderSize(r, 64<<10), limit: limit}
}

func (lr *bufferedLineReader) readLine() ([]byte, error) {
	var accumulated []byte
	for {
		chunk, err := lr.reader.ReadSlice('\n')
		if len(accumulated)+len(chunk) > lr.limit {
			if discardErr := lr.discardLine(err); discardErr != nil {
				return nil, discardErr
			}
			return nil, fmt.Errorf("%w: frame exceeds the %d byte limit", errMalformedFrame, lr.limit)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			accumulated = append(accumulated, chunk...)
			continue
		}
		if err != nil {
			if len(accumulated)+len(chunk) == 0 {
				return nil, err
			}

			accumulated = append(accumulated, chunk...)
			lr.reader = bufio.NewReaderSize(errReader{err}, 16)
			return accumulated, nil
		}
		return append(accumulated, chunk...), nil
	}
}

func (lr *bufferedLineReader) discardLine(err error) error {
	for errors.Is(err, bufio.ErrBufferFull) {
		_, err = lr.reader.ReadSlice('\n')
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
