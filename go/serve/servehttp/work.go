package servehttp

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"github.com/lsm/open-agent-protocol/go/internal/workwire"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

var workStatuses = map[string]int{
	"unknown_adapter":     http.StatusNotFound,
	"unknown_session":     http.StatusNotFound,
	"session_closed":      http.StatusConflict,
	"session_exists":      http.StatusConflict,
	"unsupported_feature": http.StatusBadRequest,
	"capability_degraded": http.StatusBadRequest,
	"scope_mismatch":      http.StatusBadRequest,
	"request_cancelled":   http.StatusBadRequest,
	"run_not_found":       http.StatusNotFound,
	"invalid_submission":  http.StatusBadRequest,
	"run_terminal":        http.StatusConflict,
	"stale_capabilities":  http.StatusConflict,
	"run_active":          http.StatusConflict,
	"model_not_found":     http.StatusBadRequest,
	"history_failed":      http.StatusInternalServerError,
	"malformed_json":      http.StatusBadRequest,
	"invalid_request":     http.StatusBadRequest,
	"invalid_payload":     http.StatusBadRequest,
}

func workStatus(code string) int {
	if status, ok := workStatuses[code]; ok {
		return status
	}
	return http.StatusInternalServerError
}

func (s *Server) routeWork(mux *http.ServeMux) {
	mux.HandleFunc("GET /work", s.handleWorkList)
	mux.HandleFunc("GET /work/capabilities", s.handleWorkCapabilities)
	mux.HandleFunc("GET /work/sessions/{id}", s.handleWorkStatus)
	mux.HandleFunc("POST /adapters/{name}/work", s.handleWorkStart)
	mux.HandleFunc("POST /work/sessions/{id}/send", s.handleWorkSend)
	mux.HandleFunc("POST /work/sessions/{id}/stop", s.handleWorkStop)
	mux.HandleFunc("GET /work/sessions/{id}/read", s.handleWorkRead)
}

func (s *Server) answerWork(w http.ResponseWriter, answer any, refusal *workwire.Refusal, session string) {
	if refusal != nil {
		s.writeErrorDetails(w, workStatus(refusal.Code), refusal.Code, refusal.Message, refusal.Details, protocol.Envelope{SessionID: protocol.SessionID(session)})
		return
	}
	writeJSON(w, http.StatusOK, answer)
}

func queryFlag(r *http.Request, name string) bool {
	value := r.URL.Query().Get(name)
	return value == "true" || value == "1"
}

func (s *Server) workBody(w http.ResponseWriter, r *http.Request) (json.RawMessage, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		s.writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "a request with a body declares application/json; the daemon reads no other media type", protocol.Envelope{})
		return nil, false
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the daemon limit", protocol.Envelope{})
		} else {
			s.writeError(w, http.StatusBadRequest, "request_read", "request body could not be read", protocol.Envelope{})
		}
		return nil, false
	}
	var object map[string]json.RawMessage
	if len(body) == 0 || json.Unmarshal(body, &object) != nil || object == nil {
		s.answerWork(w, nil, malformedWork, "")
		return nil, false
	}
	return body, true
}

var malformedWork = &workwire.Refusal{Code: "malformed_json", Message: "the request body is not a JSON envelope"}

func (s *Server) handleWorkList(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	request := workwire.ListRequest{Directory: query.Get("directory"), IncludeClosed: queryFlag(r, "include_closed"), IncludeNative: queryFlag(r, "include_native"), Cursor: query.Get("cursor"), Search: query.Get("search")}
	if adapters := query.Get("adapters"); adapters != "" {
		request.Adapters = strings.Split(adapters, ",")
	}
	if query.Has("limit") {
		limit, err := strconv.Atoi(query.Get("limit"))
		if err != nil {
			s.answerWork(w, nil, &workwire.Refusal{Code: "invalid_request", Message: "limit is 1 to 100"}, "")
			return
		}
		request.Limit = &limit
	}
	answer, refusal := s.work.List(r.Context(), request)
	s.answerWork(w, answer, refusal, "")
}

func (s *Server) handleWorkCapabilities(w http.ResponseWriter, r *http.Request) {
	answer, refusal := s.work.Capabilities(r.Context())
	s.answerWork(w, answer, refusal, "")
}

func (s *Server) handleWorkStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	answer, refusal := s.work.Status(r.Context(), id)
	s.answerWork(w, answer, refusal, id)
}

func (s *Server) handleWorkStart(w http.ResponseWriter, r *http.Request) {
	body, ok := s.workBody(w, r)
	if !ok {
		return
	}
	answer, refusal := s.work.Start(r.Context(), r.PathValue("name"), body)
	s.answerWork(w, answer, refusal, "")
}

func (s *Server) handleWorkSend(w http.ResponseWriter, r *http.Request) {
	body, ok := s.workBody(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	answer, refusal := s.work.Send(r.Context(), id, body)
	s.answerWork(w, answer, refusal, id)
}

func (s *Server) handleWorkStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	answer, refusal := s.work.Stop(r.Context(), id)
	s.answerWork(w, answer, refusal, id)
}

func (s *Server) handleWorkRead(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	query := r.URL.Query()
	var after *uint64
	if query.Has("after") {
		parsed, err := strconv.ParseUint(query.Get("after"), 10, 64)
		if err != nil {
			s.answerWork(w, nil, &workwire.Refusal{Code: "invalid_request", Message: "after is a whole number"}, id)
			return
		}
		after = &parsed
	}
	var limit *int64
	if query.Has("limit") {
		parsed, err := strconv.ParseInt(query.Get("limit"), 10, 64)
		if err != nil {
			parsed = 0
		}
		limit = &parsed
	}
	answer, refusal := s.work.Read(r.Context(), id, after, limit)
	s.answerWork(w, answer, refusal, id)
}
