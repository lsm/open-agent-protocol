package serve

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/protocol"
)

const (
	TurnsKept      = 512
	TurnTextLimit  = 64 * 1024
	LastReplyLimit = 4096
)

type WorkStatus string

const (
	WorkQueued   WorkStatus = "queued"
	WorkRunning  WorkStatus = "running"
	WorkNeedsYou WorkStatus = "needs_you"
	WorkDone     WorkStatus = "done"
	WorkFailed   WorkStatus = "failed"
	WorkStopped  WorkStatus = "stopped"
)

type Work struct {
	SessionID          protocol.SessionID
	Adapter            string
	Directory          string
	Status             WorkStatus
	RunID              protocol.RunID
	Title              string
	Link               string
	LastReply          string
	UpdatedAtMS        int64
	PendingInteraction protocol.InteractionID
}

type Turn struct {
	Role    string
	Text    string
	RunID   protocol.RunID
	Outcome string
	AtMS    int64
}

type Transcript struct {
	FirstIndex uint64
	Turns      []Turn
}

type workOutcome struct {
	status WorkStatus
	runID  protocol.RunID
	reply  string
}

type waitState struct {
	waiting bool
	pending protocol.InteractionID
}

type workLog struct {
	title     string
	directory string
	turns     []Turn
	dropped   uint64
	outcome   *workOutcome
	waits     map[protocol.RunID]waitState
}

func cutAt(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

func (s *Session) recordTurnLocked(role, text string, run protocol.RunID, outcome string, atMS int64) {
	if len(s.work.turns) == TurnsKept {
		s.work.turns = append(s.work.turns[:0:0], s.work.turns[1:]...)
		s.work.dropped++
	}
	s.work.turns = append(s.work.turns, Turn{Role: role, Text: cutAt(text, TurnTextLimit), RunID: run, Outcome: outcome, AtMS: atMS})
}

func (s *Session) recordSubmitted(request protocol.MessageSubmitRequest, run protocol.RunID, atMS int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recordTurnLocked("user", userText(request.Messages), run, "", atMS)
}

func userText(messages []protocol.Message) string {
	var joined strings.Builder
	for _, message := range messages {
		if message.Role != protocol.RoleUser {
			continue
		}
		if text, ok := message.Content.Text(); ok {
			if joined.Len() > 0 {
				joined.WriteByte('\n')
			}
			joined.WriteString(text)
			continue
		}
		parts, _ := message.Content.Parts()
		for _, part := range parts {
			if part.Type != protocol.ContentText {
				continue
			}
			if joined.Len() > 0 {
				joined.WriteByte('\n')
			}
			joined.WriteString(part.Text)
		}
	}
	return joined.String()
}

func replyText(payload json.RawMessage) string {
	var body struct {
		FinalResponse *struct {
			Content json.RawMessage `json:"content"`
		} `json:"final_response"`
	}
	if json.Unmarshal(payload, &body) != nil || body.FinalResponse == nil {
		return ""
	}
	var plain string
	if json.Unmarshal(body.FinalResponse.Content, &plain) == nil {
		return plain
	}
	var parts []struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(body.FinalResponse.Content, &parts) != nil {
		return ""
	}
	var joined strings.Builder
	for _, part := range parts {
		if part.Text != nil {
			joined.WriteString(*part.Text)
		}
	}
	return joined.String()
}

func (s *Session) observeWorkLocked(envelope protocol.Envelope, atMS int64) {
	var status WorkStatus
	outcome := ""
	switch envelope.Type {
	case protocol.TypeRunCompleted:
		status, outcome = WorkDone, "completed"
	case protocol.TypeRunFailed:
		status, outcome = WorkFailed, "failed"
	case protocol.TypeRunCancelled:
		status, outcome = WorkStopped, "cancelled"
	case protocol.TypeRunStatusUpdated:
		var update protocol.RunStatusUpdatedPayload
		if json.Unmarshal(envelope.Payload, &update) != nil {
			return
		}
		if s.work.waits == nil {
			s.work.waits = map[protocol.RunID]waitState{}
		}
		s.work.waits[envelope.RunID] = waitState{waiting: update.Status == protocol.RunWaitingForInput, pending: update.PendingUserInputID}
		return
	default:
		return
	}
	reply := ""
	if status == WorkDone {
		reply = replyText(envelope.Payload)
	}
	s.work.outcome = &workOutcome{status: status, runID: envelope.RunID, reply: reply}
	s.recordTurnLocked("assistant", reply, envelope.RunID, outcome, atMS)
}

func (s *Session) SetTitle(title string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.work.title = title
}

func (s *Session) workOf(ctx context.Context) (Work, bool) {
	state, err := s.State(ctx)
	if errors.Is(err, base.ErrSessionClosed) {
		return Work{}, false
	}
	if err != nil {
		state = protocol.SessionState{SessionID: s.id, Status: protocol.SessionError}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	found := Work{
		SessionID:   s.id,
		Adapter:     s.adapterName,
		Directory:   s.work.directory,
		Status:      WorkDone,
		RunID:       state.ActiveRunID,
		Title:       s.work.title,
		UpdatedAtMS: state.UpdatedAtMS,
	}
	if found.UpdatedAtMS == 0 {
		found.UpdatedAtMS = s.created.UnixMilli()
	}
	for _, run := range state.ActiveRuns {
		if len(run.PendingInteractions) > 0 {
			found.PendingInteraction = run.PendingInteractions[0]
		}
	}
	waiting := false
	if state.Status == protocol.SessionRunning && found.PendingInteraction == "" && found.RunID != "" {
		if wait, ok := s.work.waits[found.RunID]; ok && wait.waiting {
			waiting = true
			found.PendingInteraction = wait.pending
		}
	}
	switch state.Status {
	case protocol.SessionClosed:
		return Work{}, false
	case protocol.SessionQueued:
		found.Status = WorkQueued
	case protocol.SessionWaitingForInput:
		found.Status = WorkNeedsYou
	case protocol.SessionRunning:
		found.Status = WorkRunning
		if waiting || found.PendingInteraction != "" {
			found.Status = WorkNeedsYou
		}
	case protocol.SessionError:
		found.Status = WorkFailed
	case protocol.SessionIdle:
		if s.work.outcome != nil {
			found.Status = s.work.outcome.status
			found.RunID = s.work.outcome.runID
			if found.Status == WorkDone {
				found.LastReply = cutAt(s.work.outcome.reply, LastReplyLimit)
			}
		}
	}
	return found, true
}

func (s *Session) transcript(after *uint64, limit int) Transcript {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := uint64(len(s.work.turns))
	start := s.work.dropped
	if after != nil {
		if *after == ^uint64(0) {
			return Transcript{FirstIndex: s.work.dropped + held}
		}
		start = *after + 1
	}
	from := uint64(0)
	if start > s.work.dropped {
		from = min(start-s.work.dropped, held)
	}
	to := min(held, from+uint64(limit))
	turns := append([]Turn(nil), s.work.turns[from:to]...)
	return Transcript{FirstIndex: s.work.dropped + from, Turns: turns}
}

func (h *Hub) Work(ctx context.Context, id protocol.SessionID) (Work, error) {
	entry, err := h.Session(id)
	if err != nil {
		return Work{}, err
	}
	found, live := entry.workOf(ctx)
	if !live {
		entry.markClosed()
		return Work{}, &SessionClosedError{ID: id}
	}
	found.Link = h.linkOf(ctx, entry)
	return found, nil
}

func (h *Hub) Works(ctx context.Context) []Work {
	var listed []Work
	for _, entry := range h.sessions.list() {
		found, live := entry.workOf(ctx)
		if !live {
			entry.markClosed()
			continue
		}
		found.Link = h.linkOf(ctx, entry)
		listed = append(listed, found)
	}
	return listed
}

func (h *Hub) Transcript(id protocol.SessionID, after *uint64, limit int) (Transcript, error) {
	entry, err := h.Session(id)
	if err != nil {
		return Transcript{}, err
	}
	return entry.transcript(after, limit), nil
}

func (h *Hub) linkOf(_ context.Context, entry *Session) string {
	entry.mu.Lock()
	native := entry.binding.NativeSessionID
	entry.mu.Unlock()
	if native == "" {
		return ""
	}
	implementation, ok := h.registry.Lookup(entry.adapterName)
	if !ok {
		return ""
	}
	linker, ok := implementation.(base.NativeLinker)
	if !ok {
		return ""
	}
	return linker.NativeLink(native)
}

const NativeListLimit = 50

type Native struct {
	Adapter string
	Session base.NativeListing
}

type NativeFailure struct {
	Adapter string
	Message string
}

func (h *Hub) Natives(ctx context.Context, known map[string]bool) ([]Native, []NativeFailure) {
	var found []Native
	var failures []NativeFailure
	for _, name := range h.registry.Names() {
		implementation, _ := h.registry.Lookup(name)
		lister, ok := implementation.(base.NativeLister)
		if !ok {
			continue
		}
		listed, err := lister.NativeList(ctx, base.NativeListRequest{Directory: h.registry.WorkingDirectory(name), Limit: NativeListLimit})
		if err != nil {
			failures = append(failures, NativeFailure{Adapter: name, Message: err.Error()})
			continue
		}
		for _, session := range listed {
			if known[session.NativeID] || h.boundNative(name, session.NativeID) {
				continue
			}
			found = append(found, Native{Adapter: name, Session: session})
		}
	}
	return found, failures
}

func (h *Hub) boundNative(adapter, native string) bool {
	return h.SessionForNative(adapter, native) != ""
}

func (h *Hub) SessionForNative(adapter, native string) protocol.SessionID {
	for _, entry := range h.sessions.list() {
		entry.mu.Lock()
		bound := entry.binding.NativeSessionID
		entry.mu.Unlock()
		if entry.adapterName == adapter && bound == native && native != "" {
			return entry.id
		}
	}
	return ""
}

func (h *Hub) NativeRunning(ctx context.Context, adapter, native string) (bool, error) {
	implementation, ok := h.registry.Lookup(adapter)
	if !ok {
		return false, &UnknownAdapterError{Name: adapter}
	}
	lister, ok := implementation.(base.NativeLister)
	if !ok {
		return false, nil
	}
	listed, err := lister.NativeList(ctx, base.NativeListRequest{Directory: h.registry.WorkingDirectory(adapter), Limit: NativeListLimit})
	if err != nil {
		return false, err
	}
	for _, session := range listed {
		if session.NativeID == native {
			return session.Running, nil
		}
	}
	return false, nil
}

type NativeRef struct {
	Adapter   string
	NativeID  string
	Directory string
}

func (h *Hub) HeldNative(id protocol.SessionID) (NativeRef, bool) {
	entry, err := h.Session(id)
	if err != nil {
		return NativeRef{}, false
	}
	entry.mu.Lock()
	defer entry.mu.Unlock()
	return NativeRef{Adapter: entry.adapterName, NativeID: entry.binding.NativeSessionID, Directory: entry.binding.Directory}, true
}

func (h *Hub) NativeTranscript(ctx context.Context, ref NativeRef, maxTurns int) ([]base.NativeTurn, bool, error) {
	if ref.NativeID == "" {
		return nil, false, nil
	}
	implementation, ok := h.registry.Lookup(ref.Adapter)
	if !ok {
		return nil, false, nil
	}
	reader, ok := implementation.(base.NativeReader)
	if !ok {
		return nil, false, nil
	}
	directory := ref.Directory
	if directory == "" {
		directory = h.registry.WorkingDirectory(ref.Adapter)
	}
	turns, err := reader.NativeRead(ctx, base.NativeReadRequest{NativeID: ref.NativeID, Directory: directory, MaxTurns: maxTurns})
	return turns, true, err
}

type WorkReach struct {
	Name         string
	Directory    string
	AnyDirectory bool
	NativeList   bool
	NativeRead   bool
	Descriptor   *base.Descriptor
	Message      string
}

func (h *Hub) WorkReach(ctx context.Context) []WorkReach {
	names := h.registry.Names()
	sort.Strings(names)
	reached := make([]WorkReach, 0, len(names))
	for _, name := range names {
		implementation, _ := h.registry.Lookup(name)
		_, lists := implementation.(base.NativeLister)
		_, reads := implementation.(base.NativeReader)
		reach := WorkReach{Name: name, Directory: h.registry.WorkingDirectory(name), AnyDirectory: h.registry.ServesAnyDirectory(name), NativeList: lists, NativeRead: reads}
		descriptor, err := implementation.Probe(ctx)
		switch {
		case err != nil:
			reach.Message = err.Error()
		case descriptor.CapabilityRevision == "":
			reach.Message = "adapter descriptor carries no capability revision"
		default:
			reach.Descriptor = &descriptor
		}
		reached = append(reached, reach)
	}
	return reached
}

func (h *Hub) Knows(id protocol.SessionID) bool {
	_, err := h.Session(id)
	return err == nil
}
