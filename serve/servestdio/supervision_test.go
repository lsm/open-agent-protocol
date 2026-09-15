package servestdio

import (
	"errors"
	"io"
	"testing"
)

// The model harness of the B′ design (GH #17, direction 4): selectOutcome
// is the supervision owner's single decision core, a pure function of the
// latched custody cells and the abandonment flags, and the lifecycle
// invariants stated over it are checkable exhaustively — the reachable
// input space is the cross product this file enumerates, a few hundred
// cases. Every assertion below is an invariant derived from the design
// contract, not a restatement of the implementation's switch: the four
// rounds of per-finding regressions chased interleavings one step behind
// because each pinned one schedule; this harness pins the decision table
// whole, so no schedule over the reporters can produce an outcome outside
// it.
//
// The invariants:
//
//   - I2 (one bounded diagnostic): the outcome is exactly one error value
//     or nil, chosen by the documented precedence — asserted structurally
//     by every case, since each case checks one outcome against the
//     invariant predicates.
//   - I3 (custody conservation, the round-4 R4b tombstone): a latched
//     writer failure is never lost — whenever the loop produced no
//     translation of its own, the outcome IS the write error, whatever
//     was abandoned around it.
//   - I5 (no dropped host fault): a latched reader defect or read failure
//     is never silently swallowed when no loop translation and no writer
//     failure outrank it — even when the loop was abandoned before it
//     could number the line.
//   - stall honesty: ErrShutdownStalled is returned exactly when some
//     stage was abandoned and no host fault outranks it — and never
//     otherwise.
//   - clean end: nothing latched, nothing abandoned, returns nil; io.EOF
//     alone is the clean end, never an error.
//
// The state machine's remaining invariants are structural or behavioral:
// I1 (every path reaches `returned` within three windows) and I4 (no timer
// or saturation event exists in serving) hold by construction — the
// serving select has no timer case — and are pinned behaviorally by the
// retained corpus (the bounded-shutdown tests) and by the round-4
// regression TestBusyWorkIsBackpressureNotATimeout.

// modelWriteFailure stands in for the writer cell's failure value.
var modelWriteFailure = errors.New("model: the host closed stdout")

// modelReadFailure stands in for the reader cell's own read failure
// (distinct from a framing defect, which the loop would translate).
var modelReadFailure = errors.New("model: the input failed")

// TestSelectOutcomeEnumeratesTheReachableSpace walks the full reachable
// cross product — loop outcome × writer failure × reader terminal × the
// three abandonment flags — and asserts every invariant on every case.
func TestSelectOutcomeEnumeratesTheReachableSpace(t *testing.T) {
	loopErrs := []error{
		nil, // clean, or ended by ctx/writer latch
		&MalformedLineError{Line: 3, Detail: "model"}, // the numbered translation
		modelReadFailure, // the loop passed a raw read failure through
	}
	writerErrs := []error{nil, modelWriteFailure}
	readerErrs := []error{
		nil,                           // the reader has not ended
		io.EOF,                        // the clean host close
		&frameDefect{detail: "model"}, // a framing defect the loop may not have translated
		modelReadFailure,              // the input's own read failure
	}
	cases := 0
	for _, loopErr := range loopErrs {
		for _, writerErr := range writerErrs {
			for _, readerErr := range readerErrs {
				for flags := 0; flags < 8; flags++ {
					report := sessionReport{
						loopErr:        loopErr,
						writerErr:      writerErr,
						readerErr:      readerErr,
						loopAbandoned:  flags&1 != 0,
						workAbandoned:  flags&2 != 0,
						drainAbandoned: flags&4 != 0,
					}
					assertInvariants(t, report, selectOutcome(report))
					cases++
				}
			}
		}
	}
	if cases != 3*2*4*8 {
		t.Fatalf("enumerated %d cases, want %d", cases, 3*2*4*8)
	}
}

// assertInvariants checks the design contract on one point of the
// decision table. The predicates are stated independently of the
// implementation's switch: each holds the outcome to the invariant the
// design documents, so a regression in any cell fails on the contract,
// not on a golden copy of the old answer.
func assertInvariants(t *testing.T, report sessionReport, got error) {
	t.Helper()
	context := func() string {
		return "loopErr=" + errName(report.loopErr) +
			" writerErr=" + errName(report.writerErr) +
			" readerErr=" + errName(report.readerErr) +
			abandonment(report)
	}
	abandoned := report.loopAbandoned || report.workAbandoned || report.drainAbandoned

	// Rule 1 is universal: the loop's own translation outranks everything,
	// in every combination of discoveries and abandonments around it.
	if report.loopErr != nil {
		if !errors.Is(got, report.loopErr) && got != report.loopErr {
			t.Fatalf("loop translation outranked: %s got %v", context(), got)
		}
		return
	}
	// I3, custody conservation: a latched write failure with no loop
	// translation is the outcome — never a stall summary, never nil —
	// whatever was abandoned. This is the R4b interleaving made
	// unexpressible: the zombie loop cannot consume the cell.
	if report.writerErr != nil {
		if got != report.writerErr {
			t.Fatalf("write failure lost (I3): %s got %v", context(), got)
		}
		return
	}
	// I5, no dropped host fault: a reader defect or read failure survives
	// untranslated when nothing outranks it — including the case where the
	// loop was abandoned before numbering the line.
	if report.readerErr != nil && !errors.Is(report.readerErr, io.EOF) {
		if got != report.readerErr {
			t.Fatalf("reader fault dropped (I5): %s got %v", context(), got)
		}
		return
	}
	// Stall honesty, both directions: abandoned with no fault above is
	// exactly ErrShutdownStalled; clean on all counts is exactly nil.
	if abandoned {
		if got != ErrShutdownStalled {
			t.Fatalf("abandonment misreported: %s got %v", context(), got)
		}
		return
	}
	if got != nil {
		t.Fatalf("clean end is not clean: %s got %v", context(), got)
	}
}

// errName names an error for failure messages.
func errName(err error) string {
	if err == nil {
		return "none"
	}
	var malformed *MalformedLineError
	if errors.As(err, &malformed) {
		return "malformed"
	}
	var defect *frameDefect
	if errors.As(err, &defect) {
		return "defect"
	}
	if errors.Is(err, io.EOF) {
		return "eof"
	}
	if errors.Is(err, modelWriteFailure) {
		return "write-failure"
	}
	return "read-failure"
}

// abandonment renders the flags for failure messages.
func abandonment(report sessionReport) string {
	flags := ""
	for _, row := range []struct {
		set bool
		tag string
	}{
		{report.loopAbandoned, " loop-abandoned"},
		{report.workAbandoned, " work-abandoned"},
		{report.drainAbandoned, " drain-abandoned"},
	} {
		if row.set {
			flags += row.tag
		}
	}
	if flags == "" {
		return " settled"
	}
	return flags
}

// TestTerminalCellIsABroadcast pins the custody-cell primitive itself: the
// value is written once, survives any number of reads, and a second report
// is dropped rather than double-closing the latch.
func TestTerminalCellIsABroadcast(t *testing.T) {
	cell := newTerminal()
	if _, latched := cell.peek(); latched {
		t.Fatal("a fresh cell reports itself latched")
	}
	cell.report(modelWriteFailure)
	cell.report(io.EOF) // a defensive second report must be dropped
	if err := cell.outcome(); err != modelWriteFailure {
		t.Fatalf("cell outcome %v, want the first report", err)
	}
	if err := cell.outcome(); err != modelWriteFailure {
		t.Fatalf("second read %v, want the same value — reading must not consume", err)
	}
	if err, latched := cell.peek(); !latched || err != modelWriteFailure {
		t.Fatalf("peek after latch = (%v, %v), want (%v, true)", err, latched, modelWriteFailure)
	}
}
