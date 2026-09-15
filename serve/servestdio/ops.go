package servestdio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/lsm/open-agent-protocol/protocol"
)

// The op names, one per HTTP route of serve/servehttp; semantics are
// identical, only the framing differs.
const opAdapters = "adapters"

// responseLine is one daemon → host response, correlated by the request id.
// Result is the HTTP route's success body verbatim — a plain JSON document
// for the listings — and the JSON null for the routes HTTP answers without a
// body.
type responseLine struct {
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  *wireError      `json:"error,omitempty"`
}

// wireError carries the code and message of the error.response payload the
// HTTP route would have returned: the same codes, the same bounded messages.
type wireError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// serveRequest executes one op and writes its response. The op runs on the
// frontend's dispatch context, which teardown cancels so an in-flight
// handler detaches from the hub and settles as an error response; the
// response send runs on the caller's context, so a send parked behind a
// slow consumer still delivers through the writer's bounded drain and is
// cut loose only by the caller's own cancellation.
func (s *Server) serveRequest(dispatchCtx, sendCtx context.Context, request requestLine, lines chan<- []byte) {
	result, werr := s.dispatch(dispatchCtx, request)
	s.respond(sendCtx, lines, request, result, werr)
}

// respond writes one op's correlated response line. A response whose
// encoding exceeds the frame limit is replaced by the bounded
// response_too_large refusal, so an oversized result still gets a
// correlated, framable answer.
func (s *Server) respond(ctx context.Context, lines chan<- []byte, request requestLine, result json.RawMessage, werr *wireError) {
	response := responseLine{ID: *request.ID, OK: werr == nil, Result: result}
	if werr != nil {
		response.Result = json.RawMessage("null")
		response.Error = werr
	}
	if err := s.send(ctx, lines, response); err != nil {
		s.logger.Printf("servestdio: response %d: %v", *request.ID, err)
		// Only a size refusal has a bounded correlated answer. A send
		// abandoned by the context ended the session, and must not emit a
		// refusal that blames the response's size.
		if !errors.Is(err, ErrLineTooLarge) {
			return
		}
		fallback := responseLine{ID: *request.ID, OK: false, Result: json.RawMessage("null"), Error: &wireError{
			Code: "response_too_large", Message: "the encoded response exceeds the frame limit",
		}}
		if fallbackErr := s.send(ctx, lines, fallback); fallbackErr != nil {
			s.logger.Printf("servestdio: response %d: %v", *request.ID, fallbackErr)
		}
	}
}

func (s *Server) dispatch(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	switch request.Op {
	case opAdapters:
		if werr := request.only(); werr != nil {
			return nil, werr
		}
		return s.adaptersOp(ctx)
	default:
		return nil, &wireError{Code: "unknown_op", Message: fmt.Sprintf("no op %q", trimMessage(request.Op))}
	}
}

// Param names of requestLine, shared by the per-op shape checks.
const (
	paramAdapter = "adapter"
	paramSession = "session_id"
	paramRequest = "request"
	paramAfter   = "after"
)

// only refuses a well-formed line that carries params its op does not define.
// Presence is the rule — a supplied-but-empty or null param is still supplied
// — because the protocol is closed: speaking the wrong shape is a request
// error the host can correct, unlike an unknown field, which fails the whole
// frontend closed.
func (request requestLine) only(fields ...string) *wireError {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field] = true
	}
	var extra []string
	for _, param := range []string{paramAdapter, paramSession, paramAfter, paramRequest} {
		if !allowed[param] && request.present[param] {
			extra = append(extra, param)
		}
	}
	if len(extra) == 0 {
		return nil
	}
	return &wireError{Code: "invalid_request", Message: fmt.Sprintf("op %q accepts no %s parameter", trimMessage(request.Op), strings.Join(extra, ", "))}
}

// --- daemon-management surfaces ---

type adapterInfo struct {
	Name               string                         `json:"name"`
	CapabilityRevision string                         `json:"capability_revision,omitempty"`
	Capabilities       *protocol.CapabilityDescriptor `json:"capabilities,omitempty"`
	Error              string                         `json:"error,omitempty"`
}

func (s *Server) adaptersOp(ctx context.Context) (json.RawMessage, *wireError) {
	statuses := s.hub.Adapters(ctx)
	infos := make([]adapterInfo, 0, len(statuses))
	for _, status := range statuses {
		info := adapterInfo{Name: status.Name}
		if status.Err != nil {
			info.Error = trimMessage(status.Err.Error())
		} else {
			info.CapabilityRevision = status.Descriptor.CapabilityRevision
			info.Capabilities = &status.Descriptor.Capabilities
		}
		infos = append(infos, info)
	}
	return marshalResult(map[string]any{"adapters": infos})
}

// --- encoding helpers ---

func marshalResult(value any) (json.RawMessage, *wireError) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, internalError(err)
	}
	return data, nil
}

func internalError(err error) *wireError {
	return &wireError{Code: "internal", Message: trimMessage(err.Error())}
}

// trimMessage bounds an error message on a rune boundary so the truncated
// string stays valid UTF-8 for JSON marshaling.
func trimMessage(message string) string {
	const limit = 300
	runes := []rune(message)
	if len(runes) <= limit {
		return message
	}
	return string(runes[:limit]) + "…"
}
