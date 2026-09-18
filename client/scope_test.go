package client

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/protocol"
)

func scopedSessionStub(t *testing.T, canned protocol.Envelope) *Session {
	t.Helper()
	opened, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID("open-response"), protocol.SessionOpenResponse{
		SessionID: "s-1", Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened.SessionID = "s-1"
	body, err := json.Marshal(canned)
	if err != nil {
		t.Fatal(err)
	}
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if r.Method == http.MethodPost {

			var request protocol.Envelope
			data, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(data, &request)
			reply := opened
			reply.InReplyTo = request.ID
			encoded, _ := json.Marshal(reply)
			_, _ = w.Write(encoded)
			return
		}
		_, _ = w.Write(body)
	})
	session, err := c.Open(context.Background(), "memory", "s-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return session
}

func stateResponse(t *testing.T, envelopeScope, payloadScope protocol.SessionID) protocol.Envelope {
	t.Helper()
	response, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, protocol.EnvelopeID("state-response"), protocol.SessionState{
		SessionID: payloadScope, Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}

	response.SessionID, response.InReplyTo, response.CapabilityRevision = envelopeScope, "someone-elses-request", "reference-memory-v9"
	return response
}

func catalogResponse(t *testing.T, envelopeScope, payloadScope protocol.SessionID, sources []protocol.ToolSourceDescriptor) protocol.Envelope {
	t.Helper()
	response, err := protocol.NewEnvelope(protocol.TypeActionToolsListResponse, protocol.EnvelopeID("tools-response"), protocol.ToolsListResponse{
		SessionID: payloadScope, Sources: sources, Tools: []protocol.ToolDefinition{},
	})
	if err != nil {
		t.Fatal(err)
	}

	response.SessionID, response.InReplyTo, response.CapabilityRevision = envelopeScope, "someone-elses-request", "reference-memory-v9"
	return response
}

func TestClientRejectsMisroutedGetResponses(t *testing.T) {
	leaked := []protocol.ToolSourceDescriptor{{ID: "secret", Kind: protocol.ToolSourceProcess, Endpoint: "stdio:another-sessions-source"}}
	for _, testCase := range []struct {
		name   string
		canned protocol.Envelope
		call   func(*Session) error
		want   string
	}{
		{
			"state scoped elsewhere", stateResponse(t, "s-other", "s-other"),
			func(s *Session) error { _, err := s.State(context.Background()); return err },
			`scoped to session "s-other", want "s-1"`,
		},
		{
			"state payload disagrees with its envelope", stateResponse(t, "s-1", "s-other"),
			func(s *Session) error { _, err := s.State(context.Background()); return err },
			`payload names session "s-other", envelope "s-1"`,
		},
		{
			"state payload names no session", stateResponse(t, "s-1", ""),
			func(s *Session) error { _, err := s.State(context.Background()); return err },
			`payload names session "", envelope "s-1"`,
		},
		{
			"catalog scoped elsewhere", catalogResponse(t, "s-other", "s-other", leaked),
			func(s *Session) error { _, err := s.Tools(context.Background()); return err },
			`scoped to session "s-other", want "s-1"`,
		},
		{
			"catalog payload disagrees with its envelope", catalogResponse(t, "s-1", "s-other", nil),
			func(s *Session) error { _, err := s.Tools(context.Background()); return err },
			`payload names session "s-other", envelope "s-1"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := scopedSessionStub(t, testCase.canned)
			err := testCase.call(session)
			if err == nil {
				t.Fatal("a misrouted response was accepted as this session's")
			}
			if !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("error %q does not report the scope defect %q", err, testCase.want)
			}
		})
	}
}

func TestClientRejectsACatalogItCannotBindToADescriptor(t *testing.T) {
	unbound := catalogResponse(t, "s-1", "s-1", nil)
	unbound.CapabilityRevision = ""
	session := scopedSessionStub(t, unbound)
	if _, err := session.Tools(context.Background()); err == nil {
		t.Fatal("a catalog with no capability revision was accepted")
	} else if !strings.Contains(err.Error(), "carries no capability revision") {
		t.Fatalf("error %q does not report the missing revision", err)
	}

	session = scopedSessionStub(t, catalogResponse(t, "s-1", "s-1", nil))
	listing, err := session.Tools(context.Background())
	if err != nil {
		t.Fatalf("a bound catalog was rejected: %v", err)
	}
	if listing.Revision != "reference-memory-v9" {
		t.Fatalf("catalog revision %q, want the envelope's", listing.Revision)
	}
}

func TestClientRejectsAnUnscopedAnswerToItsScopedCatalogRequest(t *testing.T) {
	session := scopedSessionStub(t, catalogResponse(t, "s-1", "", nil))
	_, err := session.Tools(context.Background())
	if err == nil {
		t.Fatal("an unscoped catalog was accepted as this session's effective catalog")
	}
	if !strings.Contains(err.Error(), `payload names session "", envelope "s-1"`) {
		t.Fatalf("error %q does not report the missing payload scope", err)
	}

	session = scopedSessionStub(t, catalogResponse(t, "s-1", "s-1", nil))
	if _, err := session.Tools(context.Background()); err != nil {
		t.Fatalf("a correctly scoped catalog was rejected: %v", err)
	}
}
