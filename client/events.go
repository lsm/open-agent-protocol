package client

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/lsm/open-agent-protocol/protocol"
)

// SSE event names for the daemon's terminal transport signals. They are
// framing, not OAP envelopes.
const (
	signalOverflow = "oap-overflow"
	signalGap      = "oap-replay-gap"
)

const (
	// initialReconnectBackoff is the first wait between reconnect attempts
	// after a transport failure; it doubles up to maxReconnectBackoff.
	initialReconnectBackoff = 100 * time.Millisecond
	maxReconnectBackoff     = time.Second
	// maxEmptyCycles bounds consecutive reconnects whose connection ends
	// without delivering anything: an EventsAfter cursor at a settled run's
	// tail produces empty replays forever, and an endless silent reconnect
	// loop is worse than a reported disconnect.
	maxEmptyCycles = 3
)

// Events returns the session's event stream as a live subscription. The
// subscription is opened before Events returns — the daemon registers it once
// the response begins — so the canonical order (Events before Submit) cannot
// miss the run's first envelope; a subscription opened mid-run receives only
// events from that point on.
//
// Envelopes are returned in order. By default a dropped connection is resumed
// invisibly: the client reconnects with the last observed sequence as the
// cursor, the daemon replays the suffix, and the stream continues without
// duplicates. WithStrictResume turns this off and reports the drop instead.
// Next returns io.EOF once the stream ends at a run's terminal event. A
// connection failure while opening the subscription is reported by the first
// Next call.
func (s *Session) Events(ctx context.Context) *EventStream {
	stream := &EventStream{session: s, ctx: ctx, strict: s.client.strict}
	stream.establish()
	return stream
}

// EventsAfter returns the event stream replayed from a cursor: the current
// run's envelopes after the given sequence first, then live events. It is the
// manual resume path for consumers holding a cursor from an OverflowError,
// ReplayGapError, or DisconnectError.
func (s *Session) EventsAfter(ctx context.Context, after uint64) *EventStream {
	stream := &EventStream{session: s, ctx: ctx, strict: s.client.strict, startAfter: &after, lastSeq: after}
	stream.establish()
	return stream
}

// establish opens the initial connection so the subscription is live before
// the caller proceeds; its failure is surfaced by the first Next call.
func (es *EventStream) establish() {
	if err := es.connect(); err != nil {
		es.finished, es.err = true, err
	}
}

// EventStream is one ordered envelope stream over a session. It is not safe
// for concurrent use: exactly one goroutine consumes it through Next.
type EventStream struct {
	session    *Session
	ctx        context.Context
	strict     bool
	startAfter *uint64

	response *http.Response
	reader   *bufio.Reader
	// cursor is the last observed (run, sequence); runID is empty until the
	// first envelope fixes it.
	runID   protocol.RunID
	lastSeq uint64
	// resumed marks a connection opened with a cursor: its first envelope
	// must continue runID at lastSeq+1 exactly.
	resumed bool
	// everConnected distinguishes the initial connect (whose transport
	// failures surface at once) from reconnects (which back off and retry).
	everConnected bool
	// speculated records that the replay-from-start reconnect fallback was
	// already tried, so a session without a run parks live instead of
	// retrying the speculative cursor forever.
	speculated bool
	// terminal records that a run's terminal envelope was delivered, so a
	// subsequent end of stream is the documented clean end, not a drop.
	terminal bool
	// connEvents counts envelopes delivered by the current connection.
	connEvents  int
	emptyCycles int
	backoff     time.Duration

	finished bool
	err      error
}

// Next returns the next envelope. It returns io.EOF after the stream ends
// cleanly at a run's terminal event, and any other error is terminal for the
// stream: once Next reports an error other than io.EOF, subsequent calls
// return the same error.
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
			// A daemon signal or a stream defect: surfaced, never retried.
			return protocol.Envelope{}, es.stop(err)
		}
		es.closeResponse()
		if err := es.ctx.Err(); err != nil {
			return protocol.Envelope{}, es.stop(err)
		}
		if es.terminal {
			// The stream's documented clean end at run terminality.
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
		// Invisible resume: reconnect with the cursor and continue.
	}
}

// stop makes err permanent for the stream: io.EOF is the clean end.
func (es *EventStream) stop(err error) error {
	es.closeResponse()
	es.finished = true
	es.err = err
	return err
}

// connect opens one SSE connection, retrying transport failures with backoff
// after the first successful connection. A cursor is attached whenever the
// stream holds one; a reconnect with no observed envelope yet replays the
// current run from its start, falling back to a live subscription when the
// session has no run to replay.
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

// cursor reports the reconnect cursor for the next connection. It is empty
// for a fresh live subscription; a reconnect that has observed nothing
// speculatively replays the current run from its start (speculative true), so
// envelopes emitted during the disconnect are not missed.
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

// open performs one GET on the session's event stream with the cursor both as
// the Last-Event-ID header and as the explicit ?after= query parameter.
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
		return nil, statusError(response, body)
	}
	return response, nil
}

// wait sleeps one backoff step, doubling up to the cap.
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

// poll reads frames until one envelope is delivered, a terminal signal or
// stream defect is found, or the connection ends. A connectionDrop wraps the
// causes that mean "the connection ended, decide whether to resume".
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
			} else {
				failure = &OverflowError{RunID: protocol.RunID(signal.RunID), LastSequence: signal.LastSequence, Message: signal.Message}
			}
			return false
		case signalGap:
			signal, err := decodeSignal[gapSignal](f.data)
			if err != nil {
				failure = err
			} else {
				failure = &ReplayGapError{RequestedAfter: signal.RequestedAfter, OldestAvailable: signal.OldestAvailable, LatestAvailable: signal.LatestAvailable, Message: signal.Message}
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
			// An unknown named event is framing the client does not define;
			// skipping it keeps the stream forward-compatible.
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

// deliver applies the stream's cursor integrity rules to one envelope and
// records its position.
func (es *EventStream) deliver(envelope protocol.Envelope, f frame) error {
	if f.hasID {
		id, err := strconv.ParseUint(f.lastID, 10, 64)
		if err != nil {
			return &MalformedFrameError{Detail: fmt.Sprintf("frame id %q is not a sequence", f.lastID), Cause: err}
		}
		switch {
		case envelope.Sequence == nil:
			return &MalformedFrameError{Detail: fmt.Sprintf("frame id %d frames an envelope with no sequence", id)}
		case *envelope.Sequence != id:
			return &MalformedFrameError{Detail: fmt.Sprintf("frame id %d disagrees with envelope sequence %d", id, *envelope.Sequence)}
		}
	}
	if es.resumed {
		es.resumed = false
		if envelope.Sequence == nil {
			return &MalformedFrameError{Detail: "a resumed stream must deliver sequenced envelopes"}
		}
		if es.runID != "" && envelope.RunID != es.runID {
			return &ResumeMismatchError{AfterSequence: es.lastSeq, ExpectedRunID: es.runID, ObservedRunID: envelope.RunID, ObservedSequence: *envelope.Sequence}
		}
	}
	if envelope.RunID != es.runID {
		// Sequences are per-run: a new run starts a fresh sequence space.
		es.runID = envelope.RunID
		es.lastSeq = 0
		es.terminal = isTerminal(envelope.Type)
	} else if isTerminal(envelope.Type) {
		es.terminal = true
	}
	if envelope.Sequence != nil {
		// Run sequences are contiguous: within one run every envelope carries
		// the previous sequence plus one. A regression is a replay defect, and
		// a skip means an envelope was lost — advancing past it would hide it
		// from the consumer and from a later cursor resume, so both surface.
		if es.lastSeq > 0 {
			switch {
			case *envelope.Sequence <= es.lastSeq:
				return &DuplicateSequenceError{RunID: envelope.RunID, Sequence: *envelope.Sequence}
			case *envelope.Sequence != es.lastSeq+1:
				return &SequenceGapError{RunID: envelope.RunID, Expected: es.lastSeq + 1, Observed: *envelope.Sequence}
			}
		}
		es.lastSeq = *envelope.Sequence
	}
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

// connectionDrop wraps the read error (io.EOF included) that ended one SSE
// connection: the stream may resume from its cursor.
type connectionDrop struct{ cause error }

func (e *connectionDrop) Error() string {
	return fmt.Sprintf("client: event stream connection ended: %v", e.cause)
}

func (e *connectionDrop) Unwrap() error { return e.cause }

// OverflowError is the daemon's oap-overflow signal: this connection's bounded
// buffer fell behind. LastSequence is the last sequence delivered on the
// stream; resume with EventsAfter and a cursor after it.
type OverflowError struct {
	RunID        protocol.RunID
	LastSequence uint64
	Message      string
}

func (e *OverflowError) Error() string {
	return fmt.Sprintf("client: event stream overflowed behind sequence %d (run %s); resume with a cursor after it", e.LastSequence, e.RunID)
}

// ReplayGapError is the daemon's oap-replay-gap signal: the requested cursor
// is no longer retained. OldestAvailable and LatestAvailable bound what is;
// a consumer that accepts the loss resumes with a cursor at or after
// OldestAvailable - 1.
type ReplayGapError struct {
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

// DisconnectError reports a dropped event stream that was not resumed:
// strict mode reports every drop, and auto-resume reports a stream that ends
// repeatedly without events. RunID and LastSequence are the stream's last
// observed position; resume with EventsAfter(LastSequence).
type DisconnectError struct {
	RunID        protocol.RunID
	LastSequence uint64
	Cause        error
}

func (e *DisconnectError) Error() string {
	return fmt.Sprintf("client: event stream disconnected after sequence %d (run %s): %v", e.LastSequence, e.RunID, e.Cause)
}

func (e *DisconnectError) Unwrap() error { return e.Cause }

// MalformedFrameError reports a stream frame the client cannot interpret: a
// message frame that is not an envelope, a signal payload that does not
// decode, or an id field disagreeing with the envelope it frames.
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

// DuplicateSequenceError reports an envelope whose (run, sequence) position
// was already delivered: a resumed stream replayed what the consumer already
// saw, which is a wire defect to surface, not skip.
type DuplicateSequenceError struct {
	RunID    protocol.RunID
	Sequence uint64
}

func (e *DuplicateSequenceError) Error() string {
	return fmt.Sprintf("client: duplicate sequence %d in run %s", e.Sequence, e.RunID)
}

// SequenceGapError reports an envelope that skipped one or more sequences in
// its run: an envelope was lost in transit or never published, and advancing
// the cursor past the hole would silently drop it.
type SequenceGapError struct {
	RunID    protocol.RunID
	Expected uint64
	Observed uint64
}

func (e *SequenceGapError) Error() string {
	return fmt.Sprintf("client: run %s skipped from sequence %d to %d", e.RunID, e.Expected, e.Observed)
}

// ResumeMismatchError reports a replayed suffix that does not continue the
// stream it was asked to: the run changed under the cursor, or the first
// replayed sequence is not the cursor plus one.
type ResumeMismatchError struct {
	AfterSequence    uint64
	ExpectedRunID    protocol.RunID
	ObservedRunID    protocol.RunID
	ObservedSequence uint64
}

func (e *ResumeMismatchError) Error() string {
	return fmt.Sprintf("client: replay after sequence %d continued run %s at sequence %d, want run %s at %d", e.AfterSequence, e.ObservedRunID, e.ObservedSequence, e.ExpectedRunID, e.AfterSequence+1)
}
