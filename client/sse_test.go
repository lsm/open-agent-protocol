package client

import (
	"bufio"
	"io"
	"strings"
	"testing"
)

func scanFrames(t *testing.T, document string) []frame {
	t.Helper()
	var frames []frame
	err := scanSSE(bufio.NewReader(strings.NewReader(document)), func(f frame) bool {
		frames = append(frames, f)
		return true
	})
	if err != io.EOF {
		t.Fatalf("scan error: %v", err)
	}
	return frames
}

func TestScanSSESimpleFrames(t *testing.T) {
	frames := scanFrames(t, "data: one\n\ndata: two\n\n")
	if len(frames) != 2 {
		t.Fatalf("dispatched %d frames, want 2", len(frames))
	}
	for i, want := range []string{"one", "two"} {
		if frames[i].event != "message" || string(frames[i].data) != want {
			t.Fatalf("frame %d = %q event %q, want message %q", i, frames[i].data, frames[i].event, want)
		}
	}
}

func TestScanSSEMultiLineData(t *testing.T) {
	frames := scanFrames(t, "data: first\ndata: second\ndata:\n\n")
	if len(frames) != 1 {
		t.Fatalf("dispatched %d frames, want 1", len(frames))
	}
	if want := "first\nsecond\n"; string(frames[0].data) != want {
		t.Fatalf("data %q, want %q", frames[0].data, want)
	}
}

func TestScanSSENamedEventAndID(t *testing.T) {
	frames := scanFrames(t, "event: oap-overflow\nid: 42\ndata: {}\n\ndata: next\n\n")
	if len(frames) != 2 {
		t.Fatalf("dispatched %d frames, want 2", len(frames))
	}
	first := frames[0]
	if first.event != "oap-overflow" || first.lastID != "42" || !first.hasID {
		t.Fatalf("first frame %+v", first)
	}
	// Event name and id reset between frames; the default name returns.
	second := frames[1]
	if second.event != "message" || second.hasID {
		t.Fatalf("second frame %+v", second)
	}
}

func TestScanSSECommentsAndKeepalives(t *testing.T) {
	frames := scanFrames(t, ": keepalive\n\ndata: one\n: mid-frame comment\ndata: two\n\n")
	if len(frames) != 1 {
		t.Fatalf("dispatched %d frames, want 1", len(frames))
	}
	if want := "one\ntwo"; string(frames[0].data) != want {
		t.Fatalf("data %q, want %q", frames[0].data, want)
	}
}

func TestScanSSELineTerminators(t *testing.T) {
	document := "data: lf\n\rdata: crlf\r\n\rdata: cr\r\rdata: tail\r\r"
	frames := scanFrames(t, document)
	if len(frames) != 4 {
		t.Fatalf("dispatched %d frames, want 4: %+v", len(frames), frames)
	}
	for i, want := range []string{"lf", "crlf", "cr", "tail"} {
		if string(frames[i].data) != want {
			t.Fatalf("frame %d data %q, want %q", i, frames[i].data, want)
		}
	}
}

func TestScanSSELeadingBOM(t *testing.T) {
	frames := scanFrames(t, "\xEF\xBB\xBFdata: one\n\n")
	if len(frames) != 1 || string(frames[0].data) != "one" {
		t.Fatalf("frames %+v", frames)
	}
}

func TestScanSSEFieldRules(t *testing.T) {
	// One optional space after the colon is dropped, further spaces are kept;
	// a field line without a colon names an unknown field, which is ignored
	// alongside retry and anything unrecognized.
	frames := scanFrames(t, "data:  two spaces\ndata:one\nretry: 100\nunknown: x\nnosolondata\n\n")
	if len(frames) != 1 {
		t.Fatalf("dispatched %d frames, want 1", len(frames))
	}
	if want := " two spaces\none"; string(frames[0].data) != want {
		t.Fatalf("data %q, want %q", frames[0].data, want)
	}
}

func TestScanSSEIDWithNULDiscarded(t *testing.T) {
	frames := scanFrames(t, "id: a\x00b\ndata: one\n\nid: 7\ndata: two\n\n")
	if len(frames) != 2 {
		t.Fatalf("dispatched %d frames, want 2", len(frames))
	}
	if frames[0].hasID {
		t.Fatalf("id containing NUL must be discarded: %+v", frames[0])
	}
	if !frames[1].hasID || frames[1].lastID != "7" {
		t.Fatalf("second frame id: %+v", frames[1])
	}
}

func TestScanSSENoDataNoDispatch(t *testing.T) {
	// Blank lines and id-only frames dispatch nothing; data: with an empty
	// value still dispatches an empty payload.
	frames := scanFrames(t, "\n\nid: 1\n\ndata:\n\n")
	if len(frames) != 1 {
		t.Fatalf("dispatched %d frames, want 1", len(frames))
	}
	if len(frames[0].data) != 0 {
		t.Fatalf("data %q, want empty", frames[0].data)
	}
}

func TestScanSSEUnterminatedTailDiscarded(t *testing.T) {
	frames := scanFrames(t, "data: one\n\ndata: two\n")
	if len(frames) != 1 || string(frames[0].data) != "one" {
		t.Fatalf("frames %+v", frames)
	}
}

func TestScanSSEStopsWhenHandlerDeclines(t *testing.T) {
	// The scan stops at the first declined frame and reads no further, so
	// scanning the same reader again resumes with the following frame.
	reader := bufio.NewReader(strings.NewReader("data: one\n\ndata: two\n\ndata: three\n\n"))
	var first frame
	if err := scanSSE(reader, func(f frame) bool {
		first = f
		return false
	}); err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if string(first.data) != "one" {
		t.Fatalf("first frame %q", first.data)
	}
	var second frame
	if err := scanSSE(reader, func(f frame) bool {
		second = f
		return false
	}); err != nil {
		t.Fatalf("scan error: %v", err)
	}
	if string(second.data) != "two" {
		t.Fatalf("second frame %q", second.data)
	}
}
