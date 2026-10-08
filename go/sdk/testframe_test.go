package sdk

import "encoding/json"

type frame struct {
	Protocol           string          `json:"protocol,omitempty"`
	Profile            string          `json:"profile,omitempty"`
	ID                 string          `json:"id,omitempty"`
	InferenceID        string          `json:"inference_id,omitempty"`
	Type               string          `json:"type"`
	SessionID          string          `json:"session_id,omitempty"`
	RunID              string          `json:"run_id,omitempty"`
	Sequence           int64           `json:"sequence,omitempty"`
	Version            any             `json:"version"`
	InReplyTo          string          `json:"in_reply_to,omitempty"`
	CapabilityRevision string          `json:"capability_revision,omitempty"`
	Payload            json.RawMessage `json:"payload,omitempty"`
}

func (f *frame) payload() jsonObject {
	obj, _ := decodeObject(f.Payload)
	if obj == nil {
		return jsonObject{}
	}
	return obj
}

func nextTestFrame(reader *frameReader) (*frame, error) {
	for {
		line, err := reader.reader.readLine()
		if err != nil {
			return nil, err
		}
		var f frame
		if json.Unmarshal(line, &f) == nil {
			return &f, nil
		}
	}
}
