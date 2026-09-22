package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	signalOverflow = "oap-overflow"
	signalGap      = "oap-replay-gap"
)

const (
	initialReconnectBackoff = 100 * time.Millisecond
	maxReconnectBackoff     = time.Second

	maxEmptyCycles = 3
)

func (s *Session) Events(ctx context.Context) *EventStream {
	stream := &EventStream{session: s, ctx: ctx, strict: s.client.strict}
	stream.establish()
	return stream
}

func (s *Session) EventsAfter(ctx context.Context, runID protocol.RunID, after uint64) *EventStream {
	stream := &EventStream{session: s, ctx: ctx, strict: s.client.strict, startAfter: &after, lastSeq: after, runID: runID}
	stream.establish()
	return stream
}

func (es *EventStream) establish() {
	if err := es.connect(); err != nil {
		es.finished, es.err = true, err
	}
}

type EventStream struct {
	session    *Session
	ctx        context.Context
	strict     bool
	startAfter *uint64

	response *http.Response
	reader   *bufio.Reader

	runID   protocol.RunID
	lastSeq uint64

	resumed bool

	everConnected bool

	speculated bool

	terminal bool

	connEvents  int
	emptyCycles int
	backoff     time.Duration

	finished bool
	err      error
}

func (es *EventStream) Next() (protocol.Envelope, error) {
	if es.finished {
		if es.err != nil {
			return protocol.Envelope{}, es.err
		}
		return protocol.Envelope{}, io.EOF
	}
	for {
		if es.response == nil {
			if err := es.connect(); err != nil {
				return protocol.Envelope{}, es.stop(err)
			}
		}
		envelope, err := es.poll()
		if err == nil {
			return envelope, nil
		}
		var drop *connectionDrop
		if !errors.As(err, &drop) {

			return protocol.Envelope{}, es.stop(err)
		}
		es.closeResponse()
		if err := es.ctx.Err(); err != nil {
			return protocol.Envelope{}, es.stop(err)
		}
		if es.terminal {

			return protocol.Envelope{}, es.stop(io.EOF)
		}
		if es.connEvents == 0 {
			es.emptyCycles++
			if es.emptyCycles >= maxEmptyCycles {
				return protocol.Envelope{}, es.stop(&DisconnectError{
					RunID: es.runID, LastSequence: es.lastSeq,
					Cause: fmt.Errorf("reconnected %d times without receiving an event", es.emptyCycles),
				})
			}
		} else {
			es.emptyCycles = 0
		}
		if es.strict {
			return protocol.Envelope{}, es.stop(&DisconnectError{
				RunID: es.runID, LastSequence: es.lastSeq, Cause: drop.cause,
			})
		}

	}
}

func (es *EventStream) stop(err error) error {
	es.closeResponse()
	es.finished = true
	es.err = err
	return err
}

func (es *EventStream) connect() error {
	for {
		if err := es.ctx.Err(); err != nil {
			return err
		}
		after, speculative := es.cursor()
		response, err := es.open(after)
		if err != nil {
			var serverErr *ServerError
			if speculative && errors.As(err, &serverErr) && serverErr.Code == "no_run_to_resume" {
				es.speculated = true
				continue
			}
			if errors.As(err, &serverErr) {
				return err
			}
			if !es.everConnected {
				return err
			}
			if err := es.wait(); err != nil {
				return err
			}
			continue
		}
		es.everConnected = true
		es.response = response
		es.reader = bufio.NewReader(response.Body)
		es.resumed = after != ""
		es.connEvents = 0
		es.backoff = 0
		return nil
	}
}

func (es *EventStream) cursor() (cursor string, speculative bool) {
	switch {
	case es.everConnected && es.runID != "":
		return strconv.FormatUint(es.lastSeq, 10), false
	case es.startAfter != nil:
		return strconv.FormatUint(*es.startAfter, 10), false
	case es.everConnected && !es.speculated:
		return "0", true
	default:
		return "", false
	}
}

func (es *EventStream) open(after string) (*http.Response, error) {
	path := es.session.path("/events")
	if after != "" {
		path += "?after=" + after
	}
	request, err := http.NewRequestWithContext(es.ctx, http.MethodGet, es.session.client.url(path), nil)
	if err != nil {
		return nil, err
	}
	if after != "" {
		request.Header.Set("Last-Event-ID", after)
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := es.session.client.eventsHTTP.Do(request)
	if err != nil {
		return nil, fmt.Errorf("client: stream session %s events: %w", es.session.id, err)
	}
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if err := es.session.client.failureError(response, body); err != nil {
			return nil, err
		}

		return nil, &ServerError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("event stream returned status %d, want %d OK", response.StatusCode, http.StatusOK),
		}
	}
	if mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err != nil || mediaType != "text/event-stream" {

		response.Body.Close()
		return nil, &ServerError{
			Status:  response.StatusCode,
			Message: fmt.Sprintf("event stream content type %q, want text/event-stream", response.Header.Get("Content-Type")),
		}
	}
	return response, nil
}

func (es *EventStream) wait() error {
	if es.backoff == 0 {
		es.backoff = initialReconnectBackoff
	} else if es.backoff < maxReconnectBackoff {
		es.backoff *= 2
	}
	timer := time.NewTimer(es.backoff)
	defer timer.Stop()
	select {
	case <-es.ctx.Done():
		return es.ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (es *EventStream) closeResponse() {
	if es.response != nil {
		_ = es.response.Body.Close()
		es.response, es.reader = nil, nil
	}
}

func (es *EventStream) poll() (protocol.Envelope, error) {
	var (
		envelope protocol.Envelope
		failure  error
	)
	scanErr := scanSSE(es.reader, func(f frame) bool {
		switch f.event {
		case signalOverflow:
			signal, err := decodeSignal[overflowSignal](f.data)
			if err != nil {
				failure = err
				return false
			}

			failure = &OverflowError{RunID: protocol.RunID(signal.RunID), LastSequence: signal.LastSequence, Message: signal.Message}
			return false
		case signalGap:
			signal, err := decodeSignal[gapSignal](f.data)
			if err != nil {
				failure = err
			} else {
				failure = &ReplayGapError{RunID: es.runID, RequestedAfter: signal.RequestedAfter, OldestAvailable: signal.OldestAvailable, LatestAvailable: signal.LatestAvailable, Message: signal.Message}
			}
			return false
		case "message":
			parsed, err := protocol.ParseEnvelope(f.data)
			if err != nil {
				failure = &MalformedFrameError{Detail: "message frame is not an envelope", Cause: err}
				return false
			}
			if err := es.session.client.checkEnvelope(parsed, f.data); err != nil {
				failure = err
				return false
			}
			if err := es.deliver(parsed, f); err != nil {
				failure = err
				return false
			}
			envelope = parsed
			return false
		default:

			return true
		}
	})
	if failure != nil {
		es.closeResponse()
		return protocol.Envelope{}, failure
	}
	if scanErr != nil {
		es.closeResponse()
		return protocol.Envelope{}, &connectionDrop{cause: scanErr}
	}
	return envelope, nil
}

func (es *EventStream) deliver(envelope protocol.Envelope, f frame) error {

	if envelope.SessionID != es.session.id {
		return &MalformedFrameError{Detail: fmt.Sprintf("envelope for session %q on the %q stream", envelope.SessionID, es.session.id)}
	}

	if err := payloadScopeDefect(envelope); err != nil {
		return err
	}

	if envelope.Sequence == nil || *envelope.Sequence == 0 {
		return &MalformedFrameError{Detail: "event envelope carries no sequence"}
	}
	if f.hasID {
		id, err := strconv.ParseUint(f.lastID, 10, 64)
		if err != nil {
			return &MalformedFrameError{Detail: fmt.Sprintf("frame id %q is not a sequence", f.lastID), Cause: err}
		}
		if *envelope.Sequence != id {
			return &MalformedFrameError{Detail: fmt.Sprintf("frame id %d disagrees with envelope sequence %d", id, *envelope.Sequence)}
		}
	}
	resumed := es.resumed
	if resumed {
		es.resumed = false
		if es.runID != "" && envelope.RunID != es.runID {
			return &ResumeMismatchError{AfterSequence: es.lastSeq, ExpectedRunID: es.runID, ObservedRunID: envelope.RunID, ObservedSequence: *envelope.Sequence}
		}

		if *envelope.Sequence != es.lastSeq+1 {
			return &SequenceGapError{RunID: envelope.RunID, Expected: es.lastSeq + 1, Observed: *envelope.Sequence}
		}
	}
	if envelope.RunID != es.runID {
		if resumed && es.runID == "" {

			es.runID = envelope.RunID
			es.terminal = isTerminal(envelope.Type)
		} else if es.runID == "" {

			es.runID = envelope.RunID
			es.lastSeq = 0
			es.terminal = isTerminal(envelope.Type)
		} else {

			if *envelope.Sequence != 1 {
				return &SequenceGapError{RunID: envelope.RunID, Expected: 1, Observed: *envelope.Sequence}
			}
			es.runID = envelope.RunID
			es.lastSeq = 0
			es.terminal = isTerminal(envelope.Type)
		}
	} else if isTerminal(envelope.Type) {
		es.terminal = true
	}

	if es.lastSeq > 0 {
		switch {
		case *envelope.Sequence <= es.lastSeq:
			return &DuplicateSequenceError{RunID: envelope.RunID, Sequence: *envelope.Sequence}
		case *envelope.Sequence != es.lastSeq+1:
			return &SequenceGapError{RunID: envelope.RunID, Expected: es.lastSeq + 1, Observed: *envelope.Sequence}
		}
	}
	es.lastSeq = *envelope.Sequence
	es.connEvents++
	return nil
}

func isTerminal(typ protocol.EnvelopeType) bool {
	switch typ {
	case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
		return true
	}
	return false
}

func decodeSignal[Signal any](data []byte) (Signal, error) {
	var signal Signal
	if err := json.Unmarshal(data, &signal); err != nil {
		var zero Signal
		return zero, &MalformedFrameError{Detail: "signal frame payload did not decode", Cause: err}
	}
	return signal, nil
}

type overflowSignal struct {
	RunID        string `json:"run_id"`
	LastSequence uint64 `json:"last_sequence"`
	Message      string `json:"message"`
}

type gapSignal struct {
	RequestedAfter  uint64 `json:"requested_after"`
	OldestAvailable uint64 `json:"oldest_available"`
	LatestAvailable uint64 `json:"latest_available"`
	Message         string `json:"message"`
}

type connectionDrop struct{ cause error }

func (e *connectionDrop) Error() string {
	return fmt.Sprintf("client: event stream connection ended: %v", e.cause)
}

func (e *connectionDrop) Unwrap() error { return e.cause }

type OverflowError struct {
	RunID        protocol.RunID
	LastSequence uint64
	Message      string
}

func (e *OverflowError) Error() string {
	return fmt.Sprintf("client: event stream overflowed behind sequence %d (run %s); resume with a cursor after it", e.LastSequence, e.RunID)
}

type ReplayGapError struct {
	RunID           protocol.RunID
	RequestedAfter  uint64
	OldestAvailable uint64
	LatestAvailable uint64
	Message         string
}

func (e *ReplayGapError) Error() string {
	floor := uint64(0)
	if e.OldestAvailable > 1 {
		floor = e.OldestAvailable - 1
	}
	return fmt.Sprintf("client: replay cursor %d expired (retained %d through %d); resume at or after %d", e.RequestedAfter, e.OldestAvailable, e.LatestAvailable, floor)
}

type DisconnectError struct {
	RunID        protocol.RunID
	LastSequence uint64
	Cause        error
}

func (e *DisconnectError) Error() string {
	return fmt.Sprintf("client: event stream disconnected after sequence %d (run %s): %v", e.LastSequence, e.RunID, e.Cause)
}

func (e *DisconnectError) Unwrap() error { return e.Cause }

type MalformedFrameError struct {
	Detail string
	Cause  error
}

func (e *MalformedFrameError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("client: malformed event stream frame: %s: %v", e.Detail, e.Cause)
	}
	return fmt.Sprintf("client: malformed event stream frame: %s", e.Detail)
}

func (e *MalformedFrameError) Unwrap() error { return e.Cause }

type DuplicateSequenceError struct {
	RunID    protocol.RunID
	Sequence uint64
}

func (e *DuplicateSequenceError) Error() string {
	return fmt.Sprintf("client: duplicate sequence %d in run %s", e.Sequence, e.RunID)
}

type SequenceGapError struct {
	RunID    protocol.RunID
	Expected uint64
	Observed uint64
}

func (e *SequenceGapError) Error() string {
	return fmt.Sprintf("client: run %s skipped from sequence %d to %d", e.RunID, e.Expected, e.Observed)
}

type ResumeMismatchError struct {
	AfterSequence    uint64
	ExpectedRunID    protocol.RunID
	ObservedRunID    protocol.RunID
	ObservedSequence uint64
}

func (e *ResumeMismatchError) Error() string {
	return fmt.Sprintf("client: replay after sequence %d continued run %s at sequence %d, want run %s at %d", e.AfterSequence, e.ObservedRunID, e.ObservedSequence, e.ExpectedRunID, e.AfterSequence+1)
}
