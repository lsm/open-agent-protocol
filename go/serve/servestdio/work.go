package servestdio

import (
	"context"
	"encoding/json"
	"strconv"

	"github.com/lsm/open-agent-protocol/go/internal/workwire"
)

func (s *Server) workOp(ctx context.Context, request requestLine) (json.RawMessage, *wireError) {
	var answer any
	var refusal *workwire.Refusal
	switch request.Op {
	case opWorkCapabilities:
		if werr := request.only(); werr != nil {
			return nil, werr
		}
		answer, refusal = s.work.Capabilities(ctx)
	case opWorkList:
		if werr := request.only(paramRequest); werr != nil {
			return nil, werr
		}
		listed, parseRefusal := workwire.ParseListRequest(request.Request)
		if parseRefusal != nil {
			refusal = parseRefusal
			break
		}
		answer, refusal = s.work.List(ctx, listed)
	case opWorkStatus:
		if werr := request.only(paramSession); werr != nil {
			return nil, werr
		}
		answer, refusal = s.work.Status(ctx, request.SessionID)
	case opWorkStart:
		if werr := request.only(paramAdapter, paramRequest); werr != nil {
			return nil, werr
		}
		answer, refusal = s.work.Start(ctx, request.Adapter, request.Request)
	case opWorkSend:
		if werr := request.only(paramSession, paramRequest); werr != nil {
			return nil, werr
		}
		answer, refusal = s.work.Send(ctx, request.SessionID, request.Request)
	case opWorkStop:
		if werr := request.only(paramSession); werr != nil {
			return nil, werr
		}
		answer, refusal = s.work.Stop(ctx, request.SessionID)
	case opWorkRead:
		if werr := request.only(paramSession, paramAfter, paramLimit); werr != nil {
			return nil, werr
		}
		var after *uint64
		if len(request.After) > 0 && string(request.After) != "null" {
			parsed, err := strconv.ParseUint(string(request.After), 10, 64)
			if err != nil {
				return nil, &wireError{Code: "invalid_request", Message: "after is a whole number"}
			}
			after = &parsed
		}
		var limit *int64
		if request.Limit != nil {
			given := int64(*request.Limit)
			limit = &given
		}
		answer, refusal = s.work.Read(ctx, request.SessionID, after, limit)
	}
	if refusal != nil {
		return nil, &wireError{Code: refusal.Code, Message: trimMessage(refusal.Message), Details: refusal.Details}
	}
	encoded, err := json.Marshal(answer)
	if err != nil {
		return nil, internalError(err)
	}
	return encoded, nil
}
