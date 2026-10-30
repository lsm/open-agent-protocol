package native

import "encoding/json"

type Event struct {
	SessionID string
	Type      string
	Ephemeral bool
	Data      map[string]json.RawMessage
}

func DecodeEvent(m Message) (*Event, error) {
	if m.Kind != Notification || m.Method != "session.event" {
		return nil, nil
	}
	var params, event map[string]json.RawMessage
	if json.Unmarshal(m.Params, &params) != nil || params == nil || json.Unmarshal(params["event"], &event) != nil || event == nil {
		return nil, ErrWire
	}
	e := &Event{SessionID: Text(params, "sessionId"), Type: Text(event, "type")}
	if e.SessionID == "" || e.Type == "" || json.Unmarshal(event["data"], &e.Data) != nil || e.Data == nil {
		return nil, ErrWire
	}
	_ = json.Unmarshal(event["ephemeral"], &e.Ephemeral)
	return e, nil
}
