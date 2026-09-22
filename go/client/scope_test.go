package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/lsm/open-agent-protocol/go/protocol"
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

	response.SessionID, response.InReplyTo, response.CapabilityRevision = envelopeScope, "someone-elses-request", "reference-memory-v11"
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

	response.SessionID, response.InReplyTo, response.CapabilityRevision = envelopeScope, "someone-elses-request", "reference-memory-v11"
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
	if listing.Revision != "reference-memory-v11" {
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

func postStub(t *testing.T, canned protocol.Envelope) *Session {
	t.Helper()
	opened, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID("open-response"), protocol.SessionOpenResponse{
		SessionID: "s-1", Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened.SessionID = "s-1"
	var openedOnce bool
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		var request protocol.Envelope
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &request)
		reply := canned
		if !openedOnce {
			openedOnce = true
			reply = opened
		}
		reply.InReplyTo = request.ID
		encoded, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	})
	session, err := c.Open(context.Background(), "memory", "s-1")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return session
}

func cannedResponse(t *testing.T, typ protocol.EnvelopeType, id string, envelopeScope protocol.SessionID, payload any, runScope ...protocol.RunID) protocol.Envelope {
	t.Helper()
	response, err := protocol.NewEnvelope(typ, protocol.EnvelopeID(id), payload)
	if err != nil {
		t.Fatal(err)
	}
	response.SessionID, response.CapabilityRevision = envelopeScope, "reference-memory-v11"
	for _, run := range runScope {
		response.RunID = run
	}
	return response
}

func TestClientRejectsMisroutedPostResponses(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		canned protocol.Envelope
		call   func(*Session) error
		want   string
	}{
		{
			"submit payload names another session",
			cannedResponse(t, protocol.TypeSessionMessageSubmitResponse, "submit-response", "s-1", protocol.MessageSubmitResponse{
				SessionID: "s-other", RunID: "r-1", Accepted: true, SubmissionID: "sub-1", Status: protocol.RunRunning,
			}),
			func(s *Session) error {
				_, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{
					Delivery: protocol.DeliveryAuto,
					Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}},
				})
				return err
			},
			`payload names session "s-other", envelope "s-1"`,
		},
		{
			"cancel payload names another session",
			cannedResponse(t, protocol.TypeRunCancelResponse, "cancel-response", "s-1", protocol.RunCancelResponse{
				SessionID: "s-other", RunID: "r-1", Accepted: true,
			}, "r-1"),
			func(s *Session) error { _, err := s.Cancel(context.Background(), "r-1"); return err },
			`payload names session "s-other", envelope "s-1"`,
		},
		{
			"permission resolution answers another interaction",
			cannedResponse(t, protocol.TypeActionPermissionResolveResponse, "permission-response", "s-1", protocol.PermissionResolveResponse{
				InteractionID: "i-other", SessionID: "s-1", RunID: "r-1", Accepted: true,
			}, "r-1"),
			func(s *Session) error {
				return s.ResolvePermission(context.Background(), protocol.PermissionResolveRequest{
					InteractionID: "i-1", RunID: "r-1", RequestedBy: "agent", RespondedBy: "control", ChoiceID: "approve", Granted: true,
				})
			},
			`resolves interaction "i-other", want "i-1"`,
		},
		{
			"input resolution answers another interaction",
			cannedResponse(t, protocol.TypeUserInputResolveResponse, "input-response", "s-1", protocol.UserInputResolveResponse{
				InteractionID: "i-other", SessionID: "s-1", RunID: "r-1", Accepted: true,
			}, "r-1"),
			func(s *Session) error {
				return s.ResolveInput(context.Background(), protocol.UserInputResolveRequest{
					InteractionID: "i-1", RunID: "r-1", RequestedBy: "agent", RespondedBy: "control",
					Answers: []protocol.InputAnswer{{QuestionID: "q", SelectedOptionIDs: []string{"yes"}}},
				})
			},
			`resolves interaction "i-other", want "i-1"`,
		},
		{
			"submit payload names another run",
			cannedResponse(t, protocol.TypeSessionMessageSubmitResponse, "submit-run", "s-1", protocol.MessageSubmitResponse{
				SessionID: "s-1", RunID: "r-other", Accepted: true, SubmissionID: "sub-1", Status: protocol.RunRunning,
			}, "r-1"),
			func(s *Session) error {
				_, err := s.Submit(context.Background(), protocol.MessageSubmitRequest{
					Delivery: protocol.DeliveryAuto,
					Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("hi")}},
				})
				return err
			},
			`payload names run "r-other", envelope "r-1"`,
		},
		{
			"cancel payload names another run",
			cannedResponse(t, protocol.TypeRunCancelResponse, "cancel-run", "s-1", protocol.RunCancelResponse{
				SessionID: "s-1", RunID: "r-other", Accepted: true,
			}, "r-1"),
			func(s *Session) error { _, err := s.Cancel(context.Background(), "r-1"); return err },
			`payload names run "r-other", envelope "r-1"`,
		},
		{
			"resolution payload names another run",
			cannedResponse(t, protocol.TypeUserInputResolveResponse, "input-run", "s-1", protocol.UserInputResolveResponse{
				InteractionID: "i-1", SessionID: "s-1", RunID: "r-other", Accepted: true,
			}, "r-1"),
			func(s *Session) error {
				return s.ResolveInput(context.Background(), protocol.UserInputResolveRequest{
					InteractionID: "i-1", RunID: "r-1", RequestedBy: "agent", RespondedBy: "control",
					Answers: []protocol.InputAnswer{{QuestionID: "q", SelectedOptionIDs: []string{"yes"}}},
				})
			},
			`payload names run "r-other", envelope "r-1"`,
		},
		{
			"input resolution is scoped to another session",
			cannedResponse(t, protocol.TypeUserInputResolveResponse, "input-elsewhere", "s-1", protocol.UserInputResolveResponse{
				InteractionID: "i-1", SessionID: "s-other", RunID: "r-1", Accepted: true,
			}, "r-1"),
			func(s *Session) error {
				return s.ResolveInput(context.Background(), protocol.UserInputResolveRequest{
					InteractionID: "i-1", RunID: "r-1", RequestedBy: "agent", RespondedBy: "control",
					Answers: []protocol.InputAnswer{{QuestionID: "q", SelectedOptionIDs: []string{"yes"}}},
				})
			},
			`payload names session "s-other", envelope "s-1"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			session := postStub(t, testCase.canned)
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

func TestClientRejectsAnOpenResponseThatNamesAnotherSession(t *testing.T) {
	opened, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID("open-response"), protocol.SessionOpenResponse{
		SessionID: "s-other", Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	opened.SessionID = "s-1"
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		var request protocol.Envelope
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &request)
		reply := opened
		reply.InReplyTo = request.ID
		encoded, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	})
	_, err = c.Open(context.Background(), "memory", "s-1")
	if err == nil {
		t.Fatal("an open response whose payload named another session was accepted")
	}
	if !strings.Contains(err.Error(), `payload names session "s-other", envelope "s-1"`) {
		t.Fatalf("error %q does not report the scope defect", err)
	}
}

func TestClientRejectsAnEventWhosePayloadLeavesItsEnvelope(t *testing.T) {
	sequence := uint64(1)
	for _, testCase := range []struct {
		name  string
		build func(*testing.T) protocol.Envelope
		want  string
	}{
		{
			"payload names another session",
			func(t *testing.T) protocol.Envelope {
				e := eventEnvelope(t, protocol.RunStatusUpdatedPayload{SessionID: "someone-else", RunID: "r-1", Status: protocol.RunRunning})
				e.RunID = "r-1"
				return e
			},
			`payload names session "someone-else", envelope "wire"`,
		},
		{
			"payload names another run",
			func(t *testing.T) protocol.Envelope {
				e := eventEnvelope(t, protocol.RunStatusUpdatedPayload{SessionID: "wire", RunID: "r-other", Status: protocol.RunRunning})
				e.RunID = "r-1"
				return e
			},
			`payload names run "r-other", envelope "r-1"`,
		},
		{
			"payload names another tool call",
			func(t *testing.T) protocol.Envelope {
				e := eventEnvelope(t, protocol.ActionCallPayload{SessionID: "wire", RunID: "r-1", ToolCallID: "tc-other", ExecutionOwner: "agent", Name: "grep"})
				e.Type, e.RunID, e.ToolCallID = protocol.TypeActionCallStarted, "r-1", "tc-1"
				return e
			},
			`payload names tool call "tc-other", envelope "tc-1"`,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			envelope := testCase.build(t)
			envelope.Sequence = &sequence
			data, err := envelope.MarshalJSON()
			if err != nil {
				t.Fatal(err)
			}
			c := defectServer(t, fmt.Sprintf("data: %s\n\n", data))
			_, err = openDefectSession(t, c).Events(context.Background()).Next()
			var malformed *MalformedFrameError
			if !errors.As(err, &malformed) {
				t.Fatalf("error %v (%T), want MalformedFrameError", err, err)
			}
			if !strings.Contains(malformed.Error(), testCase.want) {
				t.Fatalf("malformed detail %q, want %q", malformed.Error(), testCase.want)
			}
		})
	}
}

func eventEnvelope(t *testing.T, payload any) protocol.Envelope {
	t.Helper()
	envelope, err := protocol.NewEnvelope(protocol.TypeRunStatusUpdated, protocol.EnvelopeID("wire-scope"), payload)
	if err != nil {
		t.Fatal(err)
	}
	envelope.SessionID = "wire"
	return envelope
}

func TestClientRejectsAnOpenResponseWhoseEnvelopeNamesNoSession(t *testing.T) {
	opened, err := protocol.NewEnvelope(protocol.TypeSessionOpenResponse, protocol.EnvelopeID("open-response"), protocol.SessionOpenResponse{
		SessionID: "s-1", Status: protocol.SessionIdle,
	})
	if err != nil {
		t.Fatal(err)
	}
	c := requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		var request protocol.Envelope
		data, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(data, &request)
		reply := opened
		reply.InReplyTo = request.ID
		encoded, _ := json.Marshal(reply)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(encoded)
	})
	_, err = c.Open(context.Background(), "memory", "")
	if err == nil {
		t.Fatal("an open response whose envelope named no session was accepted, and its payload adopted")
	}
	if !strings.Contains(err.Error(), `payload names session "s-1", envelope ""`) {
		t.Fatalf("error %q does not report the scope defect", err)
	}
}

func errorEnvelopeEchoingRequest(t *testing.T, scope func(*protocol.Envelope)) *Client {
	t.Helper()
	return requestStub(t, func(w http.ResponseWriter, r *http.Request) {
		asked, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		request, err := protocol.ParseEnvelope(asked)
		if err != nil {
			t.Fatal(err)
		}
		failure, err := protocol.NewEnvelope(protocol.TypeErrorResponse, protocol.EnvelopeID("err-scope"), protocol.ErrorResponse{
			Error: protocol.ProtocolError{Code: "unknown_session", Message: "no such session"},
		})
		if err != nil {
			t.Fatal(err)
		}
		failure.InReplyTo = request.ID
		scope(&failure)
		body, err := failure.MarshalJSON()
		if err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write(body)
	})
}

func submitAgainstScopedFailure(t *testing.T, scope func(*protocol.Envelope)) error {
	t.Helper()
	c := errorEnvelopeEchoingRequest(t, scope)
	session := &Session{client: c, id: "wire", adapter: "memory"}
	_, err := session.Submit(context.Background(), protocol.MessageSubmitRequest{
		Messages: []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("x")}},
		Delivery: protocol.DeliveryAuto,
	})
	return err
}

func TestClientRefusesAnErrorEnvelopeScopedToAnotherSession(t *testing.T) {
	err := submitAgainstScopedFailure(t, func(failure *protocol.Envelope) {
		failure.SessionID = "someone-elses-session"
	})
	if err == nil {
		t.Fatal("an error attributed to a different session was surfaced as this session's")
	}
	if !strings.Contains(err.Error(), "scoped to session") {
		t.Fatalf("error does not name the scope defect: %v", err)
	}
	if strings.Contains(err.Error(), "correlation") {
		t.Fatalf("the correlation check fired instead, so this test proves nothing about scope: %v", err)
	}
}

func TestClientRefusesAnErrorEnvelopeScopedToAnotherRun(t *testing.T) {
	c := errorEnvelopeEchoingRequest(t, func(failure *protocol.Envelope) {
		failure.SessionID = "wire"
		failure.RunID = "someone-elses-run"
	})
	session := &Session{client: c, id: "wire", adapter: "memory"}
	_, err := session.Cancel(context.Background(), "r-1")
	if err == nil {
		t.Fatal("an error attributed to a different run was surfaced as this run's")
	}
	if !strings.Contains(err.Error(), "scoped to run") {
		t.Fatalf("error does not name the run defect: %v", err)
	}
}

func TestClientLetsAnErrorPassWhenTheRequestNamesNoRun(t *testing.T) {
	err := submitAgainstScopedFailure(t, func(failure *protocol.Envelope) {
		failure.SessionID = "wire"
		failure.RunID = "a-run-the-request-never-named"
	})
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("a submit carries no run to be scoped against, so the run check must not fire: %v", err)
	}
}

func TestClientSurfacesACorrectlyScopedErrorEnvelope(t *testing.T) {
	err := submitAgainstScopedFailure(t, func(failure *protocol.Envelope) {
		failure.SessionID = "wire"
	})
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("a correctly scoped error was not surfaced as a ServerError: %v", err)
	}
	if serverErr.Code != "unknown_session" || serverErr.Message != "no such session" {
		t.Fatalf("code=%q message=%q", serverErr.Code, serverErr.Message)
	}
}
