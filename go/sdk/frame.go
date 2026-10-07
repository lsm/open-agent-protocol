package sdk

import (
	"encoding/json"
	"errors"
	"io"
	"time"
)

const envelopeVersion = 1

type frame struct {
	Protocol           string          `json:"protocol,omitempty"`
	Profile            string          `json:"profile,omitempty"`
	ID                 string          `json:"id,omitempty"`
	InferenceID        string          `json:"inference_id,omitempty"`
	Type               string          `json:"type"`
	StreamID           string          `json:"stream_id,omitempty"`
	SessionID          string          `json:"session_id,omitempty"`
	RunID              string          `json:"run_id,omitempty"`
	MessageID          string          `json:"message_id,omitempty"`
	Sequence           int64           `json:"sequence,omitempty"`
	Timestamp          int64           `json:"timestamp,omitempty"`
	Version            any             `json:"version"`
	InReplyTo          string          `json:"in_reply_to,omitempty"`
	CapabilityRevision string          `json:"capability_revision,omitempty"`
	Payload            json.RawMessage `json:"payload,omitempty"`

	ProtocolVersion string `json:"protocol_version,omitempty"`

	raw json.RawMessage
}

func (f *frame) UnmarshalJSON(data []byte) error {
	type plain frame
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*f = frame(decoded)
	if legacyVersion, ok := f.Version.(float64); ok {
		f.Version = int(legacyVersion)
	}
	return nil
}

func (f *frame) payload() jsonObject {
	if obj, ok := decodeObject(f.Payload); ok {
		return obj
	}
	obj, _ := decodeObject(f.raw)
	return obj
}

func (f *frame) mergedPayload() jsonObject {
	payload, ok := decodeObject(f.Payload)
	if !ok {
		obj, _ := decodeObject(f.raw)
		return obj
	}
	merged, _ := decodeObject(f.raw)
	if merged == nil {
		return payload
	}
	for k, v := range payload {
		merged[k] = v
	}
	return merged
}

func (f *frame) jsonPayload(key string) (jsonObject, error) {
	payload := f.payload()
	raw, ok := payload[key]
	if !ok {
		if raw, ok = payload["event_json"]; !ok {
			raw, ok = payload["result_json"]
		}
	}
	if !ok {
		return payload, nil
	}
	text, isString := raw.(string)
	if !isString {
		return payload, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err != nil {
		return nil, transportErrorf(err, "malformed JSON in %s", key)
	}
	obj, ok := decoded.(map[string]any)
	if !ok {
		return jsonObject{}, nil
	}
	return jsonObject(obj), nil
}

func newStreamEnvelope(frameType, streamID string, payload any) *frame {
	return &frame{
		Type:      frameType,
		StreamID:  streamID,
		MessageID: streamID,
		Sequence:  1,
		Timestamp: time.Now().UnixMilli(),
		Version:   envelopeVersion,
		Payload:   mustMarshal(payload),
	}
}

func newFlowEnvelope(frameType, flowID string, sequence int64, payload any) *frame {
	return &frame{
		Type:      frameType,
		StreamID:  flowID,
		MessageID: newULID(),
		Sequence:  sequence,
		Timestamp: time.Now().UnixMilli(),
		Version:   envelopeVersion,
		Payload:   mustMarshal(payload),
	}
}

func newSessionEnvelope(frameType, sessionID string, sequence int64, payload any) *frame {
	return &frame{
		Type:      frameType,
		SessionID: sessionID,
		MessageID: newULID(),
		Sequence:  sequence,
		Timestamp: time.Now().UnixMilli(),
		Version:   envelopeVersion,
		Payload:   mustMarshal(payload),
	}
}

func newReplyEnvelope(frameType string, request *frame, payload any) *frame {
	return &frame{
		Type:      frameType,
		SessionID: request.SessionID,
		MessageID: newULID(),
		Sequence:  request.Sequence + 1,
		Timestamp: time.Now().UnixMilli(),
		Version:   envelopeVersion,
		InReplyTo: request.MessageID,
		Payload:   mustMarshal(payload),
	}
}

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

func (fr *frameReader) next() (*frame, error) {
	in, err := fr.nextInbound(true)
	if err != nil {
		return nil, err
	}
	return in.legacy, nil
}

func (fr *frameReader) nextInbound(legacy bool) (*inbound, error) {
	line, err := fr.reader.readLine()
	if err != nil {
		return nil, err
	}
	return decodeInbound(line, legacy)
}
