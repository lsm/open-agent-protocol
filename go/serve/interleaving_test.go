package serve_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"testing"
	"time"

	base "github.com/lsm/oxap/go/adapter"
	"github.com/lsm/oxap/go/protocol"
	"github.com/lsm/oxap/go/serve"
)

type deliveredEvent struct {
	run      protocol.RunID
	sequence uint64
	id       protocol.EnvelopeID
	terminal bool
}

type interleavingReader struct {
	session protocol.SessionID
	joined  protocol.RunID
	after   uint64
	earlier map[protocol.RunID]bool
	once    bool
	events  []deliveredEvent
	err     error
	done    chan struct{}
}

func (r *interleavingReader) read(subscription *serve.Subscription) {
	defer close(r.done)
	for {
		envelope, err := subscription.Next()
		if err != nil {
			r.err = err
			return
		}
		event := deliveredEvent{run: envelope.RunID, id: envelope.ID}
		if envelope.Sequence != nil {
			event.sequence = *envelope.Sequence
		}
		switch envelope.Type {
		case protocol.TypeRunCompleted, protocol.TypeRunFailed, protocol.TypeRunCancelled:
			event.terminal = true
		}
		r.events = append(r.events, event)
		if r.once && event.terminal {
			subscription.Close()
			return
		}
	}
}

type interleaving struct {
	t       *testing.T
	ctx     context.Context
	hub     *serve.Hub
	slots   [3]*serve.Session
	opened  int
	readers []*interleavingReader

	admitted map[protocol.SessionID]map[protocol.RunID]bool
}

func (d *interleaving) open(slot int) {
	d.t.Helper()
	id := protocol.SessionID(fmt.Sprintf("seeded-%d", d.opened))
	d.opened++
	session, _, err := d.hub.Open(d.ctx, "memory", base.OpenRequest{SessionID: id})
	if err != nil {
		d.t.Fatalf("open %s: %v", id, err)
	}
	d.slots[slot] = session
}

func (d *interleaving) active(slot int) protocol.RunID {
	d.t.Helper()
	state, err := d.slots[slot].State(d.ctx)
	if err != nil {
		d.t.Fatalf("state %s: %v", d.slots[slot].ID(), err)
	}
	if len(state.ActiveRuns) == 0 {
		return ""
	}
	return state.ActiveRuns[0].RunID
}

func (d *interleaving) submit(slot int) {
	d.t.Helper()
	session := d.slots[slot]
	admission, err := session.Submit(d.ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{
		SessionID: session.ID(),
		Delivery:  protocol.DeliveryAuto,
		Messages:  []protocol.Message{{Role: protocol.RoleUser, Content: protocol.TextContent("run")}},
	}})
	if errors.Is(err, base.ErrRunActive) {
		return
	}
	if err != nil {
		d.t.Fatalf("submit %s: %v", session.ID(), err)
	}
	if admission.Admission != protocol.AdmissionStarted && admission.Admission != protocol.AdmissionQueued {
		d.t.Fatalf("submit %s admitted as %s", session.ID(), admission.Admission)
	}
	if d.admitted[session.ID()] == nil {
		d.admitted[session.ID()] = make(map[protocol.RunID]bool)
	}
	d.admitted[session.ID()][admission.RunID] = true
}

func (d *interleaving) cancel(slot int) {
	d.t.Helper()
	run := d.active(slot)
	if run == "" {
		return
	}
	_, err := d.slots[slot].Cancel(d.ctx, run)
	var settled *base.RunTerminalError
	if err != nil && !errors.Is(err, base.ErrRunNotFound) && !errors.As(err, &settled) {
		d.t.Fatalf("cancel %s: %v", run, err)
	}
}

func (d *interleaving) close(slot int) {
	d.t.Helper()
	err := d.slots[slot].Close(d.ctx)
	if errors.Is(err, base.ErrRunActive) {
		return
	}
	if err != nil {
		d.t.Fatalf("close %s: %v", d.slots[slot].ID(), err)
	}
	d.open(slot)
}

func (d *interleaving) subscribe(slot int, fromStart bool) {
	d.t.Helper()
	session := d.slots[slot]
	var options []serve.SubscribeOption
	if fromStart {
		options = append(options, serve.After("", 0))
	}
	subscription, err := d.hub.Subscribe(d.ctx, session.ID(), options...)
	if errors.Is(err, serve.ErrNoRunToResume) {
		return
	}
	if err != nil {
		d.t.Fatalf("subscribe %s: %v", session.ID(), err)
	}
	reader := &interleavingReader{session: session.ID(), earlier: maps.Clone(d.admitted[session.ID()]), done: make(chan struct{})}
	reader.joined, reader.after, _ = subscription.JoinedAt()
	d.readers = append(d.readers, reader)
	go reader.read(subscription)
}

func (d *interleaving) step(random *rand.Rand) {
	d.t.Helper()
	slot := random.IntN(len(d.slots))
	switch random.IntN(5) {
	case 0, 1:
		d.submit(slot)
	case 2:
		d.cancel(slot)
	case 3:
		d.close(slot)
	default:
		d.subscribe(slot, random.IntN(2) == 0)
	}
}

func (d *interleaving) settle() {
	d.t.Helper()
	deadline := time.Now().Add(testTimeout)
	for slot := range d.slots {
		for d.active(slot) != "" {
			if time.Now().After(deadline) {
				d.t.Fatalf("%s never settled", d.slots[slot].ID())
			}
			d.cancel(slot)
		}
		d.replayEveryRun(slot)
		if err := d.slots[slot].Close(d.ctx); err != nil {
			d.t.Fatalf("close %s: %v", d.slots[slot].ID(), err)
		}
	}
	for _, reader := range d.readers {
		select {
		case <-reader.done:
		case <-time.After(testTimeout):
			d.t.Fatalf("a reader of %s never ended", reader.session)
		}
	}
}

func (d *interleaving) replayEveryRun(slot int) {
	d.t.Helper()
	session := d.slots[slot].ID()
	for run := range d.admitted[session] {
		subscription, err := d.hub.Subscribe(d.ctx, session, serve.After(run, 0))
		if err != nil {
			d.t.Fatalf("replay %s: %v", run, err)
		}
		reader := &interleavingReader{session: session, once: true, done: make(chan struct{})}
		go reader.read(subscription)
		select {
		case <-reader.done:
		case <-time.After(testTimeout):
			d.t.Fatalf("%s never replayed to an end", run)
		}
		if len(reader.events) == 0 || !reader.events[len(reader.events)-1].terminal {
			d.t.Fatalf("%s replayed %d events and no terminal", run, len(reader.events))
		}
		for index, event := range reader.events {
			if event.sequence != uint64(index+1) || event.terminal != (index == len(reader.events)-1) {
				d.t.Fatalf("%s replayed #%d at %d, terminal %v", run, event.sequence, index, event.terminal)
			}
		}
	}
}

func (d *interleaving) check() {
	d.t.Helper()
	type position struct {
		session  protocol.SessionID
		run      protocol.RunID
		sequence uint64
	}
	seen := make(map[position]protocol.EnvelopeID)
	ended := make(map[position]bool)
	terminals := 0
	for _, reader := range d.readers {
		if reader.err != nil && !errors.Is(reader.err, io.EOF) {
			var closed *serve.SessionClosedError
			var overflow *serve.OverflowError
			if !errors.As(reader.err, &closed) && !errors.As(reader.err, &overflow) {
				d.t.Fatalf("a reader of %s ended with %v", reader.session, reader.err)
			}
		}
		last := make(map[protocol.RunID]uint64)
		finished := make(map[protocol.RunID]bool)
		for _, event := range reader.events {
			if finished[event.run] {
				d.t.Fatalf("%s delivered %s #%d after its terminal", reader.session, event.run, event.sequence)
			}
			previous, reading := last[event.run]
			if reading && event.sequence != previous+1 {
				d.t.Fatalf("%s run %s went from #%d to #%d", reader.session, event.run, previous, event.sequence)
			}
			if !reading && (event.run == reader.joined || !reader.earlier[event.run]) {
				first := uint64(1)
				if event.run == reader.joined {
					first = reader.after + 1
				}
				if event.sequence != first {
					d.t.Fatalf("%s reader of %s began at #%d, want #%d", reader.session, event.run, event.sequence, first)
				}
			}
			last[event.run] = event.sequence
			finished[event.run] = event.terminal
			at := position{reader.session, event.run, event.sequence}
			if id, known := seen[at]; known && id != event.id {
				d.t.Fatalf("%s %s #%d carried both %s and %s", reader.session, event.run, event.sequence, id, event.id)
			}
			seen[at] = event.id
			if event.terminal && !ended[at] {
				ended[at] = true
				terminals++
			}
		}
	}
	for at := range seen {
		for end := range ended {
			if at.session == end.session && at.run == end.run && at.sequence > end.sequence {
				d.t.Fatalf("%s %s #%d follows its terminal #%d", at.session, at.run, at.sequence, end.sequence)
			}
		}
	}
	if terminals == 0 {
		d.t.Fatal("no reader saw a run settle")
	}
}

func TestASeededInterleavingKeepsEveryRunContiguousAndItsTerminalLast(t *testing.T) {
	for seed := range uint64(32) {
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 4*testTimeout)
			defer cancel()
			driver := &interleaving{t: t, ctx: ctx, hub: memoryHub(t, 4096), admitted: make(map[protocol.SessionID]map[protocol.RunID]bool)}
			for slot := range driver.slots {
				driver.open(slot)
			}
			random := rand.New(rand.NewPCG(seed, 0))
			for range 200 {
				driver.step(random)
			}
			driver.settle()
			driver.check()
		})
	}
}
