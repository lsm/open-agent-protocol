package workwire

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	base "github.com/lsm/open-agent-protocol/go/adapter"
	"github.com/lsm/open-agent-protocol/go/binding"
	"github.com/lsm/open-agent-protocol/go/protocol"
	"github.com/lsm/open-agent-protocol/go/serve"
)

const (
	ReadDefault = 100
	ReadMax     = 500
)

type Refusal struct {
	Code    string
	Message string
	Details map[string]any
}

var historyUnreadable = &Refusal{Code: "history_failed", Message: "the session history could not be read"}

type Front struct {
	hub  *serve.Hub
	next atomic.Int64
}

func New(hub *serve.Hub) *Front {
	return &Front{hub: hub}
}

type ref struct {
	Adapter   string `json:"adapter"`
	SessionID string `json:"session_id,omitempty"`
	NativeID  string `json:"native_id,omitempty"`
}

type pending struct {
	InteractionID string `json:"interaction_id"`
}

type workJSON struct {
	Ref         ref      `json:"ref"`
	Held        *bool    `json:"held,omitempty"`
	Native      *bool    `json:"native,omitempty"`
	Status      string   `json:"status,omitempty"`
	State       string   `json:"state,omitempty"`
	Directory   string   `json:"directory,omitempty"`
	Title       string   `json:"title,omitempty"`
	Link        string   `json:"link,omitempty"`
	RunID       string   `json:"run_id,omitempty"`
	LastReply   string   `json:"last_reply,omitempty"`
	UpdatedAtMS int64    `json:"updated_at_ms"`
	Pending     *pending `json:"pending,omitempty"`
}

func heldWork(found serve.Work) workJSON {
	out := workJSON{
		Ref:         ref{Adapter: found.Adapter, SessionID: string(found.SessionID)},
		Status:      string(found.Status),
		Directory:   found.Directory,
		Title:       found.Title,
		Link:        found.Link,
		RunID:       string(found.RunID),
		LastReply:   found.LastReply,
		UpdatedAtMS: found.UpdatedAtMS,
	}
	if found.PendingInteraction != "" {
		out.Pending = &pending{InteractionID: string(found.PendingInteraction)}
	}
	return out
}

func unheldWork(entry binding.Entry) workJSON {
	held := false
	state := "live"
	if entry.Action == binding.ActionClosed {
		state = "closed"
	}
	return workJSON{
		Ref:         ref{Adapter: entry.Record.Adapter, SessionID: entry.Record.SessionID},
		Held:        &held,
		State:       state,
		Directory:   entry.Record.Directory,
		UpdatedAtMS: entry.TimeMS,
	}
}

func nativeWork(found serve.Native) workJSON {
	held, native := false, true
	state := "idle"
	if found.Session.Running {
		state = "running"
	}
	return workJSON{
		Ref:         ref{Adapter: found.Adapter, NativeID: found.Session.NativeID},
		Held:        &held,
		Native:      &native,
		State:       state,
		Title:       found.Session.Title,
		Directory:   found.Session.Directory,
		Link:        found.Session.Link,
		UpdatedAtMS: found.Session.UpdatedAtMS,
	}
}

type history struct {
	entries    []binding.Entry
	unreadable bool
}

func (f *Front) latest(ctx context.Context) history {
	store := f.hub.Binding()
	if store == nil {
		return history{}
	}
	entries, err := store.Sessions(ctx)
	if err != nil {
		return history{unreadable: true}
	}
	return history{entries: binding.Sessions(entries)}
}

func (f *Front) unheld(ctx context.Context, id string) (binding.Entry, bool, *Refusal) {
	recorded := f.latest(ctx)
	if recorded.unreadable {
		return binding.Entry{}, false, historyUnreadable
	}
	for _, entry := range recorded.entries {
		if entry.Record.SessionID == id {
			return entry, true, nil
		}
	}
	return binding.Entry{}, false, nil
}

func (f *Front) stateRefusal(err error, id string) *Refusal {
	var closed *serve.SessionClosedError
	if errors.As(err, &closed) || errors.Is(err, base.ErrSessionClosed) {
		return &Refusal{Code: "session_closed", Message: fmt.Sprintf("the session %q is closed", id)}
	}
	return &Refusal{Code: "unknown_session", Message: fmt.Sprintf("no session %q", id)}
}

func (f *Front) Status(ctx context.Context, id string) (any, *Refusal) {
	if !f.hub.Knows(protocol.SessionID(id)) {
		entry, found, refusal := f.unheld(ctx, id)
		if refusal != nil {
			return nil, refusal
		}
		if found {
			return unheldWork(entry), nil
		}
	}
	found, err := f.hub.Work(ctx, protocol.SessionID(id))
	if err != nil {
		return nil, f.stateRefusal(err, id)
	}
	return heldWork(found), nil
}

type group struct {
	Directory      string     `json:"directory"`
	LastActivityMS int64      `json:"last_activity_ms"`
	Work           []workJSON `json:"work"`
}

type unavailable struct {
	Adapter string `json:"adapter"`
	Message string `json:"message"`
}

type listJSON struct {
	Groups      []group       `json:"groups"`
	NextCursor  string        `json:"next_cursor,omitempty"`
	Unavailable []unavailable `json:"unavailable,omitempty"`
}

const (
	listLimitDefault = 50
	listLimitMax     = 100
	nativeDepthMax   = 500
)

type ListRequest struct {
	Directory     string
	Adapters      []string
	IncludeClosed bool
	IncludeNative bool
	Limit         *int
	Cursor        string
	Search        string
}

type listCursor struct {
	At    int64  `json:"at"`
	Key   string `json:"key"`
	Depth int    `json:"depth"`
}

var badListCursor = &Refusal{Code: "invalid_request", Message: "cursor is not one this list issued"}

func ParseListRequest(params json.RawMessage) (ListRequest, *Refusal) {
	var request ListRequest
	trimmed := strings.TrimSpace(string(params))
	if trimmed == "" || trimmed == "null" {
		return request, nil
	}
	var given struct {
		Directory     *string  `json:"directory"`
		Adapters      []string `json:"adapters"`
		IncludeClosed *bool    `json:"include_closed"`
		IncludeNative *bool    `json:"include_native"`
		Limit         *int     `json:"limit"`
		Cursor        *string  `json:"cursor"`
		Search        *string  `json:"search"`
	}
	if err := json.Unmarshal(params, &given); err != nil {
		return request, &Refusal{Code: "invalid_request", Message: "request is an object of directory, adapters, include_closed, include_native, limit, cursor and search"}
	}
	if given.Search != nil {
		request.Search = *given.Search
	}
	if given.Directory != nil {
		request.Directory = *given.Directory
	}
	request.Adapters = given.Adapters
	request.IncludeClosed = given.IncludeClosed != nil && *given.IncludeClosed
	request.IncludeNative = given.IncludeNative != nil && *given.IncludeNative
	request.Limit = given.Limit
	if given.Cursor != nil {
		request.Cursor = *given.Cursor
	}
	return request, nil
}

func (f *Front) List(ctx context.Context, request ListRequest) (any, *Refusal) {
	limit := listLimitDefault
	if request.Limit != nil {
		if *request.Limit < 1 || *request.Limit > listLimitMax {
			return nil, &Refusal{Code: "invalid_request", Message: "limit is 1 to 100"}
		}
		limit = *request.Limit
	}
	var after *listCursor
	if request.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		var decoded listCursor
		if err != nil || json.Unmarshal(raw, &decoded) != nil || decoded.Key == "" || decoded.Depth < 1 {
			return nil, badListCursor
		}
		after = &decoded
	}
	wanted := func(adapter, directory string) bool {
		if len(request.Adapters) > 0 && !slices.Contains(request.Adapters, adapter) {
			return false
		}
		return request.Directory == "" || directory == request.Directory
	}
	type piece struct {
		directory string
		at        int64
		key       string
		value     workJSON
	}
	searching := request.Search != ""
	var pieces []piece
	held := map[protocol.SessionID]serve.Work{}
	for _, found := range f.hub.Works(ctx) {
		held[found.SessionID] = found
		if !searching && wanted(found.Adapter, found.Directory) {
			pieces = append(pieces, piece{directory: found.Directory, at: found.UpdatedAtMS, key: "s:" + string(found.SessionID), value: heldWork(found)})
		}
	}
	known := map[string]bool{}
	unheld := map[string]binding.Entry{}
	recorded := f.latest(ctx)
	if recorded.unreadable {
		return nil, historyUnreadable
	}
	for _, entry := range recorded.entries {
		if entry.Record.NativeSessionID != "" {
			known[entry.Record.NativeSessionID] = true
		}
		if f.hub.Knows(protocol.SessionID(entry.Record.SessionID)) {
			continue
		}
		if entry.Action == binding.ActionClosed && !request.IncludeClosed {
			continue
		}
		if entry.Record.NativeSessionID != "" {
			unheld[entry.Record.Adapter+"/"+entry.Record.NativeSessionID] = entry
		}
		if !searching && wanted(entry.Record.Adapter, entry.Record.Directory) {
			pieces = append(pieces, piece{directory: entry.Record.Directory, at: entry.TimeMS, key: "s:" + entry.Record.SessionID, value: unheldWork(entry)})
		}
	}
	depth := 0
	if after != nil {
		depth = after.Depth
	}
	var missing []unavailable
	if request.IncludeNative || searching {
		found, failures := f.hub.NativesFor(ctx, known, serve.NativeQuery{Adapters: request.Adapters, Directory: request.Directory, Limit: min(depth+limit+1, nativeDepthMax), Search: request.Search})
		for _, native := range found {
			if searching {
				if session, ok := held[f.hub.SessionForNative(native.Adapter, native.Session.NativeID)]; ok {
					if wanted(session.Adapter, session.Directory) {
						pieces = append(pieces, piece{directory: session.Directory, at: session.UpdatedAtMS, key: "s:" + string(session.SessionID), value: heldWork(session)})
					}
					continue
				}
				if entry, ok := unheld[native.Adapter+"/"+native.Session.NativeID]; ok {
					if wanted(entry.Record.Adapter, entry.Record.Directory) {
						pieces = append(pieces, piece{directory: entry.Record.Directory, at: entry.TimeMS, key: "s:" + entry.Record.SessionID, value: unheldWork(entry)})
					}
					continue
				}
				if known[native.Session.NativeID] {
					continue
				}
			}
			if wanted(native.Adapter, native.Session.Directory) {
				pieces = append(pieces, piece{directory: native.Session.Directory, at: native.Session.UpdatedAtMS, key: "n:" + native.Adapter + "/" + native.Session.NativeID, value: nativeWork(native)})
			}
		}
		for _, failure := range failures {
			missing = append(missing, unavailable{Adapter: failure.Adapter, Message: strings.TrimSpace(failure.Message)})
		}
	}
	sort.SliceStable(pieces, func(i, j int) bool {
		if pieces[i].at != pieces[j].at {
			return pieces[i].at > pieces[j].at
		}
		return pieces[i].key < pieces[j].key
	})
	if after != nil {
		pieces = slices.DeleteFunc(pieces, func(each piece) bool {
			return each.at > after.At || (each.at == after.At && each.key <= after.Key)
		})
	}
	out := listJSON{Groups: []group{}, Unavailable: missing}
	if len(pieces) > limit {
		last := pieces[limit-1]
		encoded, _ := json.Marshal(listCursor{At: last.at, Key: last.key, Depth: depth + limit})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
		pieces = pieces[:limit]
	}
	at := map[string]int{}
	for _, each := range pieces {
		index, seen := at[each.directory]
		if !seen {
			index = len(out.Groups)
			at[each.directory] = index
			out.Groups = append(out.Groups, group{Directory: each.directory, LastActivityMS: each.at, Work: []workJSON{}})
		}
		out.Groups[index].Work = append(out.Groups[index].Work, each.value)
	}
	if request.Directory != "" && after == nil && len(out.Groups) == 0 {
		out.Groups = append(out.Groups, group{Directory: request.Directory, Work: []workJSON{}})
	}
	return out, nil
}

func text(params json.RawMessage, name string) (string, bool) {
	var object map[string]json.RawMessage
	if json.Unmarshal(params, &object) != nil {
		return "", false
	}
	raw, ok := object[name]
	if !ok {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", false
	}
	return value, true
}

func paramsRefusal(params json.RawMessage) *Refusal {
	trimmed := strings.TrimSpace(string(params))
	if trimmed == "" || trimmed == "null" {
		return &Refusal{Code: "invalid_request", Message: "request is required"}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(params, &object) != nil {
		return &Refusal{Code: "invalid_request", Message: "request is an object"}
	}
	raw, ok := object["message"]
	if !ok {
		return &Refusal{Code: "invalid_request", Message: "request.message is required"}
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || value == "" {
		return &Refusal{Code: "invalid_request", Message: "request.message is a non-empty string"}
	}
	return nil
}

func (f *Front) envelopeID() protocol.EnvelopeID {
	return protocol.EnvelopeID(fmt.Sprintf("work-%d", f.next.Add(1)))
}

func userMessage(message string) []protocol.Message {
	content, _ := json.Marshal(message)
	return []protocol.Message{{Role: protocol.RoleUser, Content: protocol.MessageContent(content)}}
}

func (f *Front) Start(ctx context.Context, adapter string, params json.RawMessage) (any, *Refusal) {
	if adapter == "" {
		return nil, &Refusal{Code: "invalid_request", Message: "adapter is required"}
	}
	if refusal := paramsRefusal(params); refusal != nil {
		return nil, refusal
	}
	if _, ok := f.hub.Registry().Lookup(adapter); !ok {
		return nil, &Refusal{Code: "unknown_adapter", Message: fmt.Sprintf("no adapter is registered as %q", adapter)}
	}
	configured := f.hub.Registry().WorkingDirectory(adapter)
	directory := ""
	if given, ok := text(params, "directory"); ok && given != configured {
		if !f.hub.Registry().ServesAnyDirectory(adapter) {
			return nil, &Refusal{Code: "invalid_request", Message: `a session runs in its adapter's working directory; another directory needs its own adapter entry, or "any_directory": true on this one`, Details: map[string]any{"working_directory": configured}}
		}
		if !filepath.IsAbs(given) {
			return nil, &Refusal{Code: "invalid_request", Message: "request.directory must be an absolute path"}
		}
		directory = given
	}
	if native, ok := text(params, "native_id"); ok {
		return f.adopt(ctx, adapter, native, directory, params)
	}
	entry, _, err := f.hub.OpenIn(ctx, adapter, directory, base.OpenRequest{})
	if err != nil {
		return nil, openRefusal(err, adapter)
	}
	message, _ := text(params, "message")
	if _, err := entry.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: entry.ID(), Messages: userMessage(message), Delivery: protocol.DeliveryAuto}, EnvelopeID: f.envelopeID()}); err != nil {
		_ = entry.Close(context.WithoutCancel(ctx))
		return nil, submitRefusal(err)
	}
	if title, ok := text(params, "title"); ok {
		entry.SetTitle(title)
	}
	return f.Status(ctx, string(entry.ID()))
}

func (f *Front) adopt(ctx context.Context, adapter, native, directory string, params json.RawMessage) (any, *Refusal) {
	if native == "" {
		return nil, &Refusal{Code: "invalid_request", Message: "request.native_id is a non-empty string"}
	}
	if held := f.boundSession(ctx, adapter, native); held != "" {
		return f.Send(ctx, held, params)
	}
	running, err := f.hub.NativeRunning(ctx, adapter, native)
	if err != nil {
		return nil, &Refusal{Code: "backend_failed", Message: "cannot tell whether another process is running this session: " + err.Error()}
	}
	if running {
		return nil, &Refusal{Code: "run_active", Message: "another process is running this session; continuing it here would fork the conversation"}
	}
	entry, _, err := f.hub.OpenIn(ctx, adapter, directory, base.OpenRequest{Reopen: true, Adopted: true, NativeSessionID: native})
	if err != nil {
		return nil, openRefusal(err, adapter)
	}
	if title, ok := text(params, "title"); ok {
		entry.SetTitle(title)
	}
	return f.Send(ctx, string(entry.ID()), params)
}

func (f *Front) boundSession(ctx context.Context, adapter, native string) string {
	if held := f.hub.SessionForNative(adapter, native); held != "" {
		return string(held)
	}
	for _, entry := range f.latest(ctx).entries {
		if entry.Record.Adapter == adapter && entry.Record.NativeSessionID == native {
			return entry.Record.SessionID
		}
	}
	return ""
}

func (f *Front) Send(ctx context.Context, id string, params json.RawMessage) (any, *Refusal) {
	if refusal := paramsRefusal(params); refusal != nil {
		return nil, refusal
	}
	if !f.hub.Knows(protocol.SessionID(id)) {
		entry, found, refusal := f.unheld(ctx, id)
		if refusal != nil {
			return nil, refusal
		}
		if found {
			if _, _, err := f.hub.Open(ctx, entry.Record.Adapter, base.OpenRequest{SessionID: protocol.SessionID(id), Reopen: true}); err != nil {
				return nil, openRefusal(err, entry.Record.Adapter)
			}
		}
	}
	entry, err := f.hub.Session(protocol.SessionID(id))
	if err != nil {
		return nil, f.stateRefusal(err, id)
	}
	message, _ := text(params, "message")
	if _, err := entry.Submit(ctx, base.SubmitRequest{Request: protocol.MessageSubmitRequest{SessionID: entry.ID(), Messages: userMessage(message), Delivery: protocol.DeliveryAuto}, EnvelopeID: f.envelopeID()}); err != nil {
		return nil, submitRefusal(err)
	}
	return f.Status(ctx, id)
}

func (f *Front) Stop(ctx context.Context, id string) (any, *Refusal) {
	if !f.hub.Knows(protocol.SessionID(id)) {
		entry, found, refusal := f.unheld(ctx, id)
		if refusal != nil {
			return nil, refusal
		}
		if found {
			return unheldWork(entry), nil
		}
	}
	current, err := f.hub.Work(ctx, protocol.SessionID(id))
	if err != nil {
		return nil, f.stateRefusal(err, id)
	}
	live := current.Status == serve.WorkRunning || current.Status == serve.WorkQueued || current.Status == serve.WorkNeedsYou
	if !live || current.RunID == "" {
		return heldWork(current), nil
	}
	entry, err := f.hub.Session(protocol.SessionID(id))
	if err != nil {
		return nil, f.stateRefusal(err, id)
	}
	if _, err := entry.Cancel(ctx, current.RunID); err != nil {
		return nil, cancelRefusal(err)
	}
	return f.Status(ctx, id)
}

type turnJSON struct {
	Index   uint64 `json:"index"`
	Role    string `json:"role"`
	Text    string `json:"text"`
	RunID   string `json:"run_id,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	AtMS    int64  `json:"at_ms"`
}

type readJSON struct {
	Turns []turnJSON `json:"turns"`
}

func (f *Front) Read(ctx context.Context, id string, after *uint64, limit *int64) (any, *Refusal) {
	bound := ReadDefault
	if limit != nil {
		if *limit < 1 || *limit > ReadMax {
			return nil, &Refusal{Code: "invalid_request", Message: fmt.Sprintf("work.read: limit must be from 1 to %d", ReadMax)}
		}
		bound = int(*limit)
	}
	held := true
	var native serve.NativeRef
	if !f.hub.Knows(protocol.SessionID(id)) {
		entry, found, refusal := f.unheld(ctx, id)
		if refusal != nil {
			return nil, refusal
		}
		if found {
			held = false
			native = serve.NativeRef{Adapter: entry.Record.Adapter, NativeID: entry.Record.NativeSessionID, Directory: entry.Record.Directory}
		}
	} else if ref, ok := f.hub.HeldNative(protocol.SessionID(id)); ok {
		native = ref
	}
	wanted := bound
	if after != nil {
		wanted += int(min(*after, uint64(1<<31))) + 1
	}
	if turns, readable, err := f.hub.NativeTranscript(ctx, native, wanted); readable && err == nil && len(turns) > 0 {
		return nativeTurns(turns, after, bound), nil
	}
	if !held {
		return readJSON{Turns: []turnJSON{}}, nil
	}
	read, err := f.hub.Transcript(protocol.SessionID(id), after, bound)
	if err != nil {
		return nil, f.stateRefusal(err, id)
	}
	out := readJSON{Turns: make([]turnJSON, 0, len(read.Turns))}
	for offset, turn := range read.Turns {
		out.Turns = append(out.Turns, turnJSON{Index: read.FirstIndex + uint64(offset), Role: turn.Role, Text: turn.Text, RunID: string(turn.RunID), Outcome: turn.Outcome, AtMS: turn.AtMS})
	}
	return out, nil
}

func nativeTurns(turns []base.NativeTurn, after *uint64, bound int) readJSON {
	from := 0
	if after != nil {
		from = int(min(*after+1, uint64(len(turns))))
		if *after == ^uint64(0) {
			from = len(turns)
		}
	}
	to := min(len(turns), from+bound)
	out := readJSON{Turns: make([]turnJSON, 0, to-from)}
	for index := from; index < to; index++ {
		turn := turns[index]
		out.Turns = append(out.Turns, turnJSON{Index: uint64(index), Role: turn.Role, Text: cut(turn.Text, serve.TurnTextLimit), AtMS: turn.AtMS})
	}
	return out
}

func cut(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	at := limit
	for at > 0 && text[at]&0xC0 == 0x80 {
		at--
	}
	return text[:at]
}

type nativeReach struct {
	List   bool `json:"list"`
	Read   bool `json:"read"`
	Search bool `json:"search"`
}

type reachJSON struct {
	Adapter      string      `json:"adapter"`
	Directory    string      `json:"directory,omitempty"`
	AnyDirectory bool        `json:"any_directory"`
	Verbs        []string    `json:"verbs"`
	Native       nativeReach `json:"native"`
}

type capabilitiesJSON struct {
	Adapters    []reachJSON   `json:"adapters"`
	Unavailable []unavailable `json:"unavailable,omitempty"`
}

var verbs = []struct {
	op      string
	restsOn string
}{
	{op: "work.list"},
	{op: "work.start", restsOn: "session.message.submit"},
	{op: "work.send", restsOn: "session.message.submit"},
	{op: "work.status"},
	{op: "work.stop", restsOn: "run.cancel"},
	{op: "work.read"},
}

func withdrawn(descriptor base.Descriptor, key string) bool {
	feature, ok := descriptor.Capabilities.Features[key]
	return !ok || feature.Level == protocol.SupportUnavailable
}

func (f *Front) Capabilities(ctx context.Context) (any, *Refusal) {
	out := capabilitiesJSON{Adapters: []reachJSON{}}
	for _, reach := range f.hub.WorkReach(ctx) {
		if reach.Descriptor == nil {
			out.Unavailable = append(out.Unavailable, unavailable{Adapter: reach.Name, Message: reach.Message})
			continue
		}
		served := []string{}
		for _, verb := range verbs {
			if verb.restsOn != "" && withdrawn(*reach.Descriptor, verb.restsOn) {
				continue
			}
			served = append(served, verb.op)
		}
		out.Adapters = append(out.Adapters, reachJSON{Adapter: reach.Name, Directory: reach.Directory, AnyDirectory: reach.AnyDirectory, Verbs: served, Native: nativeReach{List: reach.NativeList, Read: reach.NativeRead, Search: reach.NativeSearch}})
	}
	return out, nil
}

func openRefusal(err error, adapter string) *Refusal {
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		return &Refusal{Code: code, Message: message, Details: details}
	}
	switch {
	case errors.Is(err, serve.ErrUnknownAdapter):
		return &Refusal{Code: "unknown_adapter", Message: fmt.Sprintf("no adapter is registered as %q", adapter)}
	case errors.Is(err, base.ErrUnknownSession):
		return &Refusal{Code: "unknown_session", Message: err.Error()}
	case errors.Is(err, serve.ErrSessionExists):
		return &Refusal{Code: "session_exists", Message: err.Error()}
	case errors.Is(err, base.ErrSessionClosed):
		return &Refusal{Code: "session_closed", Message: err.Error()}
	}
	return &Refusal{Code: "backend_failed", Message: err.Error()}
}

func submitRefusal(err error) *Refusal {
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		return &Refusal{Code: code, Message: message, Details: details}
	}
	code := "internal"
	switch {
	case errors.Is(err, base.ErrSessionClosed):
		code = "session_closed"
	case errors.Is(err, base.ErrRunActive):
		code = "run_active"
	case errors.Is(err, base.ErrInvalidSubmission), errors.Is(err, base.ErrUnsupportedInput):
		code = "invalid_submission"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		code = "request_cancelled"
	}
	return &Refusal{Code: code, Message: err.Error()}
}

func cancelRefusal(err error) *Refusal {
	code := "internal"
	var terminal *base.RunTerminalError
	switch {
	case errors.As(err, &terminal):
		code = "run_terminal"
	case errors.Is(err, base.ErrRunNotFound):
		code = "run_not_found"
	case errors.Is(err, base.ErrSessionClosed):
		code = "session_closed"
	}
	if code, message, details, ok := serve.ControlRefusal(err); ok {
		return &Refusal{Code: code, Message: message, Details: details}
	}
	return &Refusal{Code: code, Message: err.Error()}
}
