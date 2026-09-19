package protocol

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

type Format string

const (
	FormatJSON  Format = "json"
	FormatArray Format = "array"
	FormatJSONL Format = "jsonl"
)

func Decode(r io.Reader) ([]Envelope, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("read envelopes: %w", err)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, nil
	}
	if trimmed[0] == '[' {
		return DecodeArray(bytes.NewReader(trimmed))
	}
	if envelope, err := ParseEnvelope(trimmed); err == nil {
		return []Envelope{envelope}, nil
	}
	return DecodeJSONL(bytes.NewReader(trimmed))
}

func DecodeArray(r io.Reader) ([]Envelope, error) {
	var envelopes []Envelope
	decoder := json.NewDecoder(r)
	if err := decoder.Decode(&envelopes); err != nil {
		return nil, fmt.Errorf("decode envelope array: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	return envelopes, nil
}

func DecodeJSONL(r io.Reader) ([]Envelope, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	var envelopes []Envelope
	for line := 1; scanner.Scan(); line++ {
		data := bytes.TrimSpace(scanner.Bytes())
		if len(data) == 0 {
			continue
		}
		envelope, err := ParseEnvelope(data)
		if err != nil {
			return nil, fmt.Errorf("decode JSONL line %d: %w", line, err)
		}
		envelopes = append(envelopes, envelope)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan JSONL: %w", err)
	}
	return envelopes, nil
}

func Encode(w io.Writer, envelopes []Envelope, format Format) error {
	encoder := json.NewEncoder(w)
	switch format {
	case FormatJSON:
		if len(envelopes) != 1 {
			return fmt.Errorf("JSON format requires exactly one envelope, got %d", len(envelopes))
		}
		return encoder.Encode(envelopes[0])
	case FormatArray:
		return encoder.Encode(envelopes)
	case FormatJSONL:
		for i := range envelopes {
			if err := encoder.Encode(envelopes[i]); err != nil {
				return fmt.Errorf("encode JSONL envelope %d: %w", i, err)
			}
		}
		return nil
	default:
		return fmt.Errorf("unsupported format %q", format)
	}
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decode trailing JSON: %w", err)
	}
	return fmt.Errorf("unexpected trailing JSON value")
}
