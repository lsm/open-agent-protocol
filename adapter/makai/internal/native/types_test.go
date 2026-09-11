package native

import (
	"encoding/json"
	"errors"
	"testing"
)

const (
	testSession = SessionID("Abcdefghijklmnopqrstu")
	testMessage = MessageID("01ARZ3NDEKTSV4RRFFQ69G5FAV")
)

func TestExactPinnedIdentifiers(t *testing.T) {
	for _, tc := range []struct {
		name    string
		valid   bool
		session SessionID
		message MessageID
	}{
		{"session", true, testSession, ""}, {"short session", false, "short", ""}, {"punctuated session", false, "abcdefghijklmnopqrst-", ""},
		{"ulid", true, "", testMessage}, {"lowercase ulid", true, "", "01arz3ndektsv4rrffq69g5fav"}, {"ulid overflow", false, "", "81ARZ3NDEKTSV4RRFFQ69G5FAV"}, {"ulid alphabet", false, "", "01ARZ3NDEKTSV4RRFFQ69G5FAU"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.session.Valid()
			if tc.message != "" {
				got = tc.message.Valid()
			}
			if got != tc.valid {
				t.Fatalf("Valid()=%v want %v", got, tc.valid)
			}
		})
	}
}

func TestEnvelopeRoundTripAndPayloadValidation(t *testing.T) {
	env, err := NewEnvelope(TypeAgentMessage, testSession, testMessage, 2, 42, AgentMessage{SessionID: testSession, MessageJSON: `{"role":"user"}`, OptionsJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	var got Envelope
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(false); err != nil {
		t.Fatal(err)
	}
	payload, err := DecodePayload[AgentMessage](got)
	if err != nil || payload.SessionID != testSession {
		t.Fatalf("payload=%+v err=%v", payload, err)
	}
}

func TestEnvelopeRejectsInvalidShapes(t *testing.T) {
	base := Envelope{Type: TypePing, SessionID: testSession, MessageID: testMessage, Sequence: 1, Timestamp: 0, Version: 1, Payload: json.RawMessage(`{}`)}
	cases := []struct {
		name   string
		mutate func(*Envelope)
	}{
		{"version", func(e *Envelope) { e.Version = 2 }}, {"zero sequence", func(e *Envelope) { e.Sequence = 0 }}, {"negative timestamp", func(e *Envelope) { e.Timestamp = -1 }},
		{"unknown type", func(e *Envelope) { e.Type = "future" }}, {"unknown payload member", func(e *Envelope) { e.Payload = json.RawMessage(`{"extra":1}`) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := base
			tc.mutate(&env)
			if err := env.Validate(false); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	t.Run("pinned error sequence zero", func(t *testing.T) {
		env := base
		env.Type = TypeAgentError
		env.Sequence = 0
		env.Payload = json.RawMessage(`{"code":"internal_error","message":"boom"}`)
		if err := env.Validate(true); err != nil {
			t.Fatal(err)
		}
		if err := env.Validate(false); err == nil {
			t.Fatal("outbound sequence zero accepted")
		}
	})
}

func TestNestedJSONAndEnumsAreStrict(t *testing.T) {
	cases := []Envelope{
		{Type: TypeAgentEvent, SessionID: testSession, MessageID: testMessage, Sequence: 1, Timestamp: 1, Version: 1, Payload: json.RawMessage(`{"event_json":"{"}`)},
		{Type: TypeAgentEvent, SessionID: testSession, MessageID: testMessage, Sequence: 1, Timestamp: 1, Version: 1, Payload: json.RawMessage(`{"event_json":"{\"type\":\"one\",\"type\":\"two\"}"}`)},
		{Type: TypeAgentError, SessionID: testSession, MessageID: testMessage, Sequence: 1, Timestamp: 1, Version: 1, Payload: json.RawMessage(`{"code":"new_code","message":"x"}`)},
		{Type: TypeSessionInfo, SessionID: testSession, MessageID: testMessage, Sequence: 1, Timestamp: 1, Version: 1, Payload: json.RawMessage(`{"session_id":"Abcdefghijklmnopqrstu","status":"new","model":"x","message_count":0,"created_at":0,"updated_at":0}`)},
	}
	for _, env := range cases {
		if err := env.Validate(false); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("got %v", err)
		}
	}
}
