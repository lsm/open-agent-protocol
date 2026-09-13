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

func TestAgentStartDualKeyTransition(t *testing.T) {
	// v0.2.0 (#198): emitters send the canonical session_id key plus the
	// permanent resume_session_id alias with the same value; the decoder
	// accepts either key and the canonical one wins when both appear.
	association := testSession
	start := AgentStart{ConfigJSON: `{}`, SessionID: &association, ResumeSessionID: &association}
	env, err := NewEnvelope(TypeAgentStart, testSession, testMessage, 1, 1, start)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	canonical, legacy := string(payload["session_id"]), string(payload["resume_session_id"])
	if canonical == "" || legacy == "" || canonical != legacy {
		t.Fatalf("dual-key emission: session_id=%s resume_session_id=%s", canonical, legacy)
	}
	if effective := start.EffectiveSessionID(); effective == nil || *effective != testSession {
		t.Fatalf("effective=%v", effective)
	}
	decoded, err := DecodePayload[AgentStart](env)
	if err != nil || decoded.EffectiveSessionID() == nil || *decoded.EffectiveSessionID() != testSession {
		t.Fatalf("decoded=%+v err=%v", decoded, err)
	}
}

func TestAgentStartAcceptsEachKeyOnDecode(t *testing.T) {
	other := SessionID("Zbcdefghijklmnopqrstu")
	cases := []struct {
		name      string
		json      string
		effective SessionID
	}{
		{"canonical only", `{"config_json":"{}","session_id":"Abcdefghijklmnopqrstu"}`, testSession},
		{"legacy alias only", `{"config_json":"{}","resume_session_id":"Abcdefghijklmnopqrstu"}`, testSession},
		{"canonical wins", `{"config_json":"{}","session_id":"Abcdefghijklmnopqrstu","resume_session_id":"Zbcdefghijklmnopqrstu"}`, testSession},
		{"omitted", `{"config_json":"{}"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var decoded AgentStart
			if err := strictDecode([]byte(tc.json), &decoded); err != nil {
				t.Fatal(err)
			}
			effective := decoded.EffectiveSessionID()
			if tc.effective == "" {
				if effective != nil {
					t.Fatalf("effective=%v want nil", effective)
				}
				return
			}
			if effective == nil || *effective != tc.effective {
				t.Fatalf("effective=%v want %s", effective, tc.effective)
			}
			env := Envelope{Type: TypeAgentStart, SessionID: tc.effective, MessageID: testMessage, Sequence: 1, Timestamp: 1, Version: 1, Payload: json.RawMessage(tc.json)}
			if err := env.Validate(false); err != nil {
				t.Fatalf("agreement via payload key: %v", err)
			}
			foreign := env
			foreign.SessionID = other
			if err := foreign.Validate(false); err == nil {
				t.Fatal("foreign payload session accepted")
			}
		})
	}
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
