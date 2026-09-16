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

// scopedSessionStub serves a valid open for session "s-1" and then answers
// every GET with the canned envelope the test supplies, so a misrouted
// response can be fed to the GET-style methods the real daemon never
// misroutes.
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
			// The open must correlate with whatever id the client minted, or
			// it is refused before any session exists to test with.
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

// stateResponse and catalogResponse build one GET answer whose envelope and
// payload scopes are set independently, which is the whole point: both
// documents are individually schema-valid and only the protocol binds them to
// one scope.
func stateResponse(t *testing.T, envelopeScope, payloadScope protocol.SessionID) protocol.Envelope {
	t.Helper()
	response, err := protocol.NewEnvelope(protocol.TypeSessionStateResponse, protocol.EnvelopeID("state-response"), protocol.SessionState{
		SessionID: payloadScope, Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Stamped with a revision so these cases keep failing on the defect they
	// name: the revision is checked before the payload is read, and a canned
	// response without one would make every scope case pass for the wrong
	// reason. TestClientRejectsACatalogItCannotBindToADescriptor covers the
	// missing revision on its own.
	response.SessionID, response.InReplyTo, response.CapabilityRevision = envelopeScope, "someone-elses-request", "reference-memory-v5"
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
	// Stamped with a revision so these cases keep failing on the defect they
	// name: the revision is checked before the payload is read, and a canned
	// response without one would make every scope case pass for the wrong
	// reason. TestClientRejectsACatalogItCannotBindToADescriptor covers the
	// missing revision on its own.
	response.SessionID, response.InReplyTo, response.CapabilityRevision = envelopeScope, "someone-elses-request", "reference-memory-v5"
	return response
}

// TestClientRejectsMisroutedGetResponses is the binding a GET cannot get from
// exchange. Those routes carry no request envelope, so the request-based scope
// check never runs, and without an explicit check the client would hand back
// another session's state or catalog — after the tool-sources unit, including
// the sources that session attached — as though it were this one's.
//
// Both GET-style methods are covered because both have the shape. A check on
// one and not the other would be worse than one rule stated once: a caller
// cannot reason about a guarantee that holds on some routes.
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

// TestClientRejectsACatalogItCannotBindToADescriptor is the revision half of
// the same binding. A tool catalog is valid for exactly one descriptor
// snapshot: it is a function of the descriptor and of the sources this session
// attached under it, and capabilities.updated is the only signal that a cached
// listing has stopped describing the session. A listing arriving with no
// revision cannot be bound to a snapshot, so it can be neither cached nor
// invalidated, and it is refused here rather than handed back with an empty
// field the caller has to notice. Session.Models refuses the same way.
func TestClientRejectsACatalogItCannotBindToADescriptor(t *testing.T) {
	unbound := catalogResponse(t, "s-1", "s-1", nil)
	unbound.CapabilityRevision = ""
	session := scopedSessionStub(t, unbound)
	if _, err := session.Tools(context.Background()); err == nil {
		t.Fatal("a catalog with no capability revision was accepted")
	} else if !strings.Contains(err.Error(), "carries no capability revision") {
		t.Fatalf("error %q does not report the missing revision", err)
	}

	// The same catalog with a revision is accepted and hands the revision
	// back, so the rule is about the pairing and not about the catalog.
	session = scopedSessionStub(t, catalogResponse(t, "s-1", "s-1", nil))
	listing, err := session.Tools(context.Background())
	if err != nil {
		t.Fatalf("a bound catalog was rejected: %v", err)
	}
	if listing.Revision != "reference-memory-v5" {
		t.Fatalf("catalog revision %q, want the envelope's", listing.Revision)
	}
}

// TestClientRejectsAnUnscopedAnswerToItsScopedCatalogRequest is the other
// corner of the scoping rule, and the one that reads backwards until the
// question is named. `session_id` is optional on a catalog *payload*, because
// an endpoint-level catalog belongs to no session — but it is the answer to an
// unscoped request, and Session.Tools never sends one: it always names this
// session. Accepting an unscoped answer would hand back a catalog missing
// exactly the sources this session attached at open, reported as its effective
// catalog. What an answer may omit is decided by the question asked, not by
// the payload's own schema.
func TestClientRejectsAnUnscopedAnswerToItsScopedCatalogRequest(t *testing.T) {
	session := scopedSessionStub(t, catalogResponse(t, "s-1", "", nil))
	_, err := session.Tools(context.Background())
	if err == nil {
		t.Fatal("an unscoped catalog was accepted as this session's effective catalog")
	}
	if !strings.Contains(err.Error(), `payload names session "", envelope "s-1"`) {
		t.Fatalf("error %q does not report the missing payload scope", err)
	}

	// The same request answered in scope is accepted, so the rule is about the
	// scope the answer carries and not about the catalog being served at all.
	session = scopedSessionStub(t, catalogResponse(t, "s-1", "s-1", nil))
	if _, err := session.Tools(context.Background()); err != nil {
		t.Fatalf("a correctly scoped catalog was rejected: %v", err)
	}
}
