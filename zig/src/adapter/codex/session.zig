const std = @import("std");
const rpc = @import("rpc");
const native = @import("native");

pub const codex_commit = "8d7cc24a87f4aa66aa434eb4f25f4f4bafc0e0a9";
pub const capability_revision = "codex-appserver-8d7cc24-oap-v1";
pub const adapter_name = "codex-appserver-stdio";
pub const endpoint_id = "codex.app-server";
pub const endpoint_name = "Codex app-server Adapter";
pub const execution_owner = "codex.app-server";
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const client_name = "open-agent-protocol";

pub const Error = error{
    InvalidParticipant,
    AlreadyOpened,
    NotOpen,
    SessionClosed,
    RunActive,
    RunNotFound,
    InvalidSubmission,
    UnsupportedInput,
    ModelNotFound,
    InteractionNotFound,
    InteractionResolved,
    WrongResponder,
    InvalidResolution,
};

pub const Reduce = std.mem.Allocator.Error || rpc.Error;

pub const Options = struct {
    session_id: []const u8 = "",
    participant: []const u8 = "user",
    model: []const u8 = "",
    working_directory: []const u8 = "",
    approval_policy: []const u8 = "",
    sandbox: []const u8 = "",
    resume_thread_id: []const u8 = "",
    first_request_id: i64 = 1,
    id_width: usize = 0,
};

pub const InputMessage = struct {
    id: []const u8 = "",
    role: []const u8 = "user",
    text: ?[]const u8,
};

pub const Submission = struct {
    messages: []const InputMessage,
    model_id: ?[]const u8 = null,
};

pub const Admission = struct {
    session_id: []const u8,
    submission_id: []const u8,
    run_id: []const u8,
    model_id: []const u8,
    message_ids: []const []const u8,
};

pub const CancelResult = struct {
    accepted: bool,
    status: []const u8,
};

pub const Refusal = struct {
    method: []const u8,
    code: ?i64 = null,
    message: []const u8,
};

pub const Settled = union(enum) {
    opened,
    admitted: Admission,
    cancel: CancelResult,
    refused: Refusal,
};

pub const State = struct {
    status: []const u8 = "idle",
    active_run_id: []const u8 = "",
    updated_at_ms: i64 = 0,
    transcript_cursor: []const u8 = "",
    current_model_id: []const u8 = "",
};

pub const Answer = struct {
    question_id: []const u8,
    text: []const u8 = "",
    selected_option_ids: []const []const u8 = &.{},
};

pub const PermissionResolve = struct {
    interaction_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    choice_id: []const u8,
    granted: bool,
    updates_arguments: bool = false,
};

pub const InputResolve = struct {
    interaction_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    answers: []const Answer,
};

pub const Resolution = struct {
    run_id: []const u8,
    responded_by: []const u8,
    permission: ?PermissionResolve = null,
    input: ?InputResolve = null,
};

const Run = struct {
    id: []const u8,
    message_id: []const u8,
    model: []const u8,
    message_ids: []const []const u8,
    turn_id: []const u8 = "",
    admitted: bool = false,
    status: []const u8 = "queued",
    next_sequence: u64 = 1,
    started: bool = false,
    terminal: bool = false,
    cancel_pending: bool = false,
    cancel_in_flight: bool = false,
    text: std.ArrayList(u8) = .empty,
};

const Item = struct {
    native_id: []const u8,
    run_id: []const u8,
    tool_call_id: []const u8,
    name: []const u8,
    arguments: std.json.Value,
    started: bool = true,
    terminal: bool = false,
};

const Decision = struct {
    id: []const u8,
    label: []const u8,
    granted: bool,
    outcome: []const u8,
};

const standard_decisions = [_]Decision{
    .{ .id = "accept", .label = "Approve once", .granted = true, .outcome = "resolved" },
    .{ .id = "acceptForSession", .label = "Approve for session", .granted = true, .outcome = "resolved" },
    .{ .id = "decline", .label = "Deny", .granted = false, .outcome = "rejected" },
    .{ .id = "cancel", .label = "Deny and cancel run", .granted = false, .outcome = "cancelled" },
};

const Decisions = [standard_decisions.len]bool;

fn decisionIndex(id: []const u8) ?usize {
    for (standard_decisions, 0..) |decision, index| {
        if (std.mem.eql(u8, decision.id, id)) return index;
    }
    return null;
}

const Option = struct {
    id: []const u8,
    label: []const u8,
    description: []const u8,
};

const Question = struct {
    id: []const u8,
    prompt: []const u8,
    options: []const Option,
};

const InteractionKind = enum { permission, input };

const Interaction = struct {
    id: []const u8,
    kind: InteractionKind,
    run_id: []const u8,
    tool_call_id: []const u8,
    request: rpc.Id,
    resolved: bool = false,
    decisions: Decisions = [_]bool{false} ** standard_decisions.len,
    questions: []const Question = &.{},
};

const Call = union(enum) {
    thread_start,
    thread_resume,
    turn_start: usize,
    turn_interrupt: usize,
};

const Pending = struct {
    id: i64,
    call: Call,
};

const Scope = struct {
    thread: []const u8,
    turn: []const u8,
    item: []const u8,
    reason: []const u8,
    decisions: Decisions,
};

fn str(text: []const u8) std.json.Value {
    return .{ .string = text };
}

fn int(value: i64) std.json.Value {
    return .{ .integer = value };
}

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: usize = 0,
    clock: i64 = 0,
    next_request: i64,
    thread_id: []const u8 = "",
    session_id: []const u8 = "",
    opening: bool = false,
    opened: bool = false,
    closed: bool = false,
    transport_closed: bool = false,
    state: State = .{},
    active: ?usize = null,
    runs: std.ArrayList(Run) = .empty,
    items: std.ArrayList(Item) = .empty,
    interactions: std.ArrayList(Interaction) = .empty,
    pending: std.ArrayList(Pending) = .empty,
    early: std.ArrayList(rpc.Message) = .empty,
    envelopes: std.ArrayList(std.json.Value) = .empty,
    writes: std.ArrayList([]const u8) = .empty,
    settled: std.ArrayList(Settled) = .empty,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options) Reducer {
        return .{ .arena = arena, .options = options, .next_request = options.first_request_id };
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        self.clock += 1;
        return self.clock;
    }

    fn nextID(self: *Reducer, kind: []const u8) ![]const u8 {
        self.ids += 1;
        const digits = try std.fmt.allocPrint(self.allocator(), "{d}", .{self.ids});
        const padding = self.options.id_width -| digits.len;
        const id = try self.allocator().alloc(u8, kind.len + 1 + padding + digits.len);
        @memcpy(id[0..kind.len], kind);
        id[kind.len] = '-';
        @memset(id[kind.len + 1 .. kind.len + 1 + padding], '0');
        @memcpy(id[kind.len + 1 + padding ..], digits);
        return id;
    }

    fn put(self: *Reducer, map: *std.json.ObjectMap, key: []const u8, value: std.json.Value) !void {
        try map.put(self.allocator(), key, value);
    }

    fn putNonEmpty(self: *Reducer, map: *std.json.ObjectMap, key: []const u8, value: []const u8) !void {
        if (value.len != 0) try self.put(map, key, str(value));
    }

    fn call(self: *Reducer, what: Call, method: []const u8, params: std.json.Value) !void {
        const id = self.next_request;
        self.next_request += 1;
        const frame = try rpc.encode(self.allocator(), .{ .request = .{ .id = id, .method = method, .params = params } });
        try self.writes.append(self.allocator(), frame);
        try self.pending.append(self.allocator(), .{ .id = id, .call = what });
    }

    fn respond(self: *Reducer, id: rpc.Id, result: std.json.Value) !void {
        const frame = try rpc.encode(self.allocator(), .{ .response = .{ .id = id, .result = result } });
        try self.writes.append(self.allocator(), frame);
    }

    fn respondError(self: *Reducer, id: rpc.Id, code: i64, message: []const u8, data: ?std.json.Value) !void {
        const frame = try rpc.encode(self.allocator(), .{ .failure = .{ .id = id, .code = code, .message = message, .data = data } });
        try self.writes.append(self.allocator(), frame);
    }

    pub fn open(self: *Reducer) !void {
        if (self.options.participant.len == 0) return Error.InvalidParticipant;
        if (self.opening or self.opened) return Error.AlreadyOpened;
        self.opening = true;
        if (self.options.resume_thread_id.len == 0) {
            const params = try native.threadStartParams(self.allocator(), .{
                .model = self.options.model,
                .cwd = self.options.working_directory,
                .approval_policy = self.options.approval_policy,
                .sandbox = self.options.sandbox,
            });
            try self.call(.thread_start, native.method_thread_start, params);
            return;
        }
        const params = try native.threadResumeParams(self.allocator(), self.options.resume_thread_id);
        try self.call(.thread_resume, native.method_thread_resume, params);
    }

    pub fn submit(self: *Reducer, submission: Submission) !void {
        if (!self.opened) return Error.NotOpen;
        var model = self.options.model;
        if (submission.model_id) |requested| {
            if (requested.len == 0) return Error.ModelNotFound;
            model = requested;
        }
        if (submission.messages.len == 0) return Error.InvalidSubmission;
        const texts = try self.allocator().alloc([]const u8, submission.messages.len);
        const message_ids = try self.allocator().alloc([]const u8, submission.messages.len);
        for (submission.messages, 0..) |message, index| {
            if (!std.mem.eql(u8, message.role, "user")) return Error.UnsupportedInput;
            texts[index] = message.text orelse return Error.UnsupportedInput;
            message_ids[index] = if (message.id.len != 0) message.id else try self.nextID("message");
        }
        if (self.closed or self.transport_closed) return Error.SessionClosed;
        if (self.active != null) return Error.RunActive;

        const run_id = try self.nextID("run");
        const message_id = try self.nextID("message");
        try self.runs.append(self.allocator(), .{ .id = run_id, .message_id = message_id, .model = model, .message_ids = message_ids });
        const index = self.runs.items.len - 1;
        self.active = index;
        self.state.status = "queued";
        self.state.active_run_id = run_id;
        self.state.updated_at_ms = self.now();
        const params = try native.turnStartParams(self.allocator(), self.thread_id, texts, model);
        try self.call(.{ .turn_start = index }, native.method_turn_start, params);
    }

    pub fn cancel(self: *Reducer, run_id: []const u8) !?CancelResult {
        const index = self.admittedRun(run_id) orelse return Error.RunNotFound;
        const run = &self.runs.items[index];
        if (run.terminal) return .{ .accepted = std.mem.eql(u8, run.status, "cancelled"), .status = run.status };
        if (run.cancel_pending) return .{ .accepted = true, .status = "cancelling" };
        if (run.cancel_in_flight) return null;
        if (self.transport_closed) return Error.SessionClosed;
        run.cancel_in_flight = true;
        const params = try native.turnInterruptParams(self.allocator(), self.thread_id, run.turn_id);
        try self.call(.{ .turn_interrupt = index }, native.method_turn_interrupt, params);
        return null;
    }

    pub fn close(self: *Reducer) !void {
        if (self.closed) return;
        if (self.active != null) return Error.RunActive;
        self.closed = true;
        self.state.status = "closed";
        self.state.active_run_id = "";
    }

    pub fn pendingInteraction(self: *Reducer) ?[]const u8 {
        for (self.interactions.items) |interaction| {
            if (!interaction.resolved) return interaction.id;
        }
        return null;
    }

    fn admittedRun(self: *Reducer, run_id: []const u8) ?usize {
        for (self.runs.items, 0..) |run, index| {
            if (run.admitted and std.mem.eql(u8, run.id, run_id)) return index;
        }
        return null;
    }

    fn runFor(self: *Reducer, thread: []const u8, turn: []const u8) ?usize {
        if (!std.mem.eql(u8, thread, self.thread_id)) return null;
        var index = self.runs.items.len;
        while (index > 0) {
            index -= 1;
            const run = self.runs.items[index];
            if (run.admitted and std.mem.eql(u8, run.turn_id, turn)) return index;
        }
        return null;
    }

    fn findItem(self: *Reducer, native_id: []const u8) ?usize {
        for (self.items.items, 0..) |item, index| {
            if (std.mem.eql(u8, item.native_id, native_id)) return index;
        }
        return null;
    }

    fn findInteraction(self: *Reducer, id: []const u8) ?usize {
        for (self.interactions.items, 0..) |interaction, index| {
            if (std.mem.eql(u8, interaction.id, id)) return index;
        }
        return null;
    }

    pub fn observe(self: *Reducer, message: rpc.Message) Reduce!void {
        if (self.closed or self.transport_closed) return;
        switch (message.kind) {
            .response, .failure => try self.settle(message),
            .request, .notification => {
                if (!self.opened) {
                    try self.early.append(self.allocator(), message);
                    return;
                }
                if (message.kind == .request) return self.handleRequest(message);
                try self.handleNotification(message);
            },
        }
    }

    fn idText(self: *Reducer, id: rpc.Id) ![]const u8 {
        return switch (id) {
            .integer => |number| std.fmt.allocPrint(self.allocator(), "{d}", .{number}),
            .string => |text| text,
        };
    }

    fn settle(self: *Reducer, message: rpc.Message) !void {
        const id = message.id.?;
        var found: ?usize = null;
        for (self.pending.items, 0..) |entry, index| {
            if (id == .integer and id.integer == entry.id) found = index;
        }
        const at = found orelse {
            const detail = try std.fmt.allocPrint(self.allocator(), "codex app-server rpc: response id is not pending: {s}", .{try self.idText(id)});
            return self.transportFailed(detail);
        };
        const entry = self.pending.orderedRemove(at);
        if (message.failure) |failure| return self.callFailed(entry.call, .{ .method = callMethod(entry.call), .code = failure.code, .message = failure.message });
        try self.callSucceeded(entry.call, message.result.?);
    }

    fn callMethod(what: Call) []const u8 {
        return switch (what) {
            .thread_start => native.method_thread_start,
            .thread_resume => native.method_thread_resume,
            .turn_start => native.method_turn_start,
            .turn_interrupt => native.method_turn_interrupt,
        };
    }

    fn undecodable(what: Call) Refusal {
        return .{ .method = callMethod(what), .message = "response does not decode into the pinned native type" };
    }

    fn callSucceeded(self: *Reducer, what: Call, result: std.json.Value) !void {
        switch (what) {
            .thread_start => {
                if (!native.decodes(result, native.thread_start_response)) return self.callFailed(what, undecodable(what));
                const thread = native.text(result, &.{ "thread", "id" });
                if (thread.len == 0) return self.callFailed(what, .{ .method = callMethod(what), .message = "thread/start returned no thread id" });
                try self.opens(thread);
            },
            .thread_resume => {
                if (!native.decodes(result, native.thread_resume_response)) return self.callFailed(what, undecodable(what));
                const thread = native.text(result, &.{ "thread", "id" });
                if (thread.len == 0 or !std.mem.eql(u8, thread, self.options.resume_thread_id)) {
                    return self.callFailed(what, .{ .method = callMethod(what), .message = "thread/resume returned an unexpected thread id" });
                }
                try self.opens(thread);
            },
            .turn_start => |index| {
                if (!native.decodes(result, native.turn_start_response)) return self.callFailed(what, undecodable(what));
                const turn = native.text(result, &.{ "turn", "id" });
                if (turn.len == 0) {
                    self.active = null;
                    self.closed = true;
                    self.state.status = "closed";
                    self.state.active_run_id = "";
                    try self.settled.append(self.allocator(), .{ .refused = .{ .method = callMethod(what), .message = "turn/start returned no turn id" } });
                    return;
                }
                try self.admit(index, turn);
            },
            .turn_interrupt => |index| {
                if (!native.decodes(result, native.turn_interrupt_response)) return self.callFailed(what, undecodable(what));
                try self.interruptAcknowledged(index);
            },
        }
    }

    fn callFailed(self: *Reducer, what: Call, refusal: Refusal) !void {
        switch (what) {
            .thread_start, .thread_resume => self.opening = false,
            .turn_start => {
                self.active = null;
                self.state.active_run_id = "";
                if (self.transport_closed) {
                    self.closed = true;
                    self.state.status = "closed";
                } else {
                    self.state.status = "idle";
                }
            },
            .turn_interrupt => |index| self.runs.items[index].cancel_in_flight = false,
        }
        try self.settled.append(self.allocator(), .{ .refused = refusal });
    }

    fn opens(self: *Reducer, thread: []const u8) !void {
        self.thread_id = thread;
        self.session_id = if (self.options.session_id.len != 0) self.options.session_id else try self.nextID("session");
        self.state = .{ .status = "idle", .current_model_id = self.options.model, .updated_at_ms = self.now() };
        self.opening = false;
        self.opened = true;
        try self.settled.append(self.allocator(), .opened);
        const early = self.early.items;
        self.early = .empty;
        for (early) |message| try self.observe(message);
    }

    fn admit(self: *Reducer, index: usize, turn: []const u8) !void {
        const run = &self.runs.items[index];
        run.turn_id = turn;
        run.admitted = true;
        const submission_id = try self.nextID("submission");
        try self.settled.append(self.allocator(), .{ .admitted = .{
            .session_id = self.session_id,
            .submission_id = submission_id,
            .run_id = run.id,
            .model_id = run.model,
            .message_ids = run.message_ids,
        } });
    }

    fn interruptAcknowledged(self: *Reducer, index: usize) !void {
        const run = &self.runs.items[index];
        run.cancel_in_flight = false;
        if (run.terminal) {
            try self.settled.append(self.allocator(), .{ .cancel = .{ .accepted = std.mem.eql(u8, run.status, "cancelled"), .status = run.status } });
            return;
        }
        run.cancel_pending = true;
        run.status = "cancelling";
        var payload = try self.runPayload(index);
        try self.put(&payload, "status", str("cancelling"));
        try self.put(&payload, "updated_at_ms", int(self.now()));
        try self.emit(index, "run.status.updated", payload, "", false);
        try self.settled.append(self.allocator(), .{ .cancel = .{ .accepted = true, .status = "cancelling" } });
    }

    pub fn transportFailed(self: *Reducer, detail: []const u8) !void {
        if (self.transport_closed) return;
        self.transport_closed = true;
        const message = if (detail.len != 0) detail else "Codex app-server transport closed before terminal settlement";
        const outstanding = self.pending.items;
        self.pending = .empty;
        for (outstanding) |entry| try self.callFailed(entry.call, .{ .method = callMethod(entry.call), .message = message });
        if (self.active) |index| try self.failRun(index, "native_transport_closed", message, "inferred");
    }

    fn runPayload(self: *Reducer, index: usize) !std.json.ObjectMap {
        var payload = std.json.ObjectMap.empty;
        try self.put(&payload, "session_id", str(self.session_id));
        try self.put(&payload, "run_id", str(self.runs.items[index].id));
        return payload;
    }

    fn emit(self: *Reducer, index: usize, kind: []const u8, payload: std.json.ObjectMap, tool_call_id: []const u8, terminal: bool) !void {
        const run = &self.runs.items[index];
        if (run.terminal) return;
        const sequence = run.next_sequence;
        run.next_sequence += 1;
        const id = try self.nextID("event");
        const timestamp = self.now();
        var envelope = std.json.ObjectMap.empty;
        try self.put(&envelope, "protocol", str(protocol_name));
        try self.put(&envelope, "version", str(protocol_version));
        try self.put(&envelope, "profile", str(profile));
        try self.put(&envelope, "type", str(kind));
        try self.put(&envelope, "id", str(id));
        try self.put(&envelope, "payload", .{ .object = payload });
        try self.put(&envelope, "sequence", int(@intCast(sequence)));
        try self.put(&envelope, "timestamp_ms", int(timestamp));
        try self.put(&envelope, "session_id", str(self.session_id));
        try self.put(&envelope, "run_id", str(run.id));
        try self.putNonEmpty(&envelope, "tool_call_id", tool_call_id);
        try self.put(&envelope, "capability_revision", str(capability_revision));
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
        if (!terminal) return;
        run.terminal = true;
        run.status = terminalStatus(kind);
        self.state.status = "idle";
        self.state.active_run_id = "";
        self.state.transcript_cursor = try std.fmt.allocPrint(self.allocator(), "{d}", .{sequence});
        self.state.updated_at_ms = timestamp;
        if (self.active == index) self.active = null;
    }

    fn terminalStatus(kind: []const u8) []const u8 {
        if (std.mem.eql(u8, kind, "run.completed")) return "completed";
        if (std.mem.eql(u8, kind, "run.cancelled")) return "cancelled";
        return "failed";
    }

    fn failActive(self: *Reducer, code: []const u8, message: []const u8) !void {
        if (self.active) |index| try self.failRun(index, code, message, "");
    }

    fn failRun(self: *Reducer, index: usize, code: []const u8, message: []const u8, settled_by: []const u8) !void {
        const detail = if (message.len != 0) message else code;
        try self.closePendingInteractions(index, native.turn_failed);
        try self.closePendingActions(index, native.turn_failed);
        var failure = std.json.ObjectMap.empty;
        try self.put(&failure, "code", str(code));
        try self.put(&failure, "message", str(detail));
        var payload = try self.runPayload(index);
        try self.put(&payload, "error", .{ .object = failure });
        try self.putNonEmpty(&payload, "settled_by", settled_by);
        try self.emit(index, "run.failed", payload, "", true);
    }

    fn handleNotification(self: *Reducer, message: rpc.Message) !void {
        const method = message.method;
        if (std.mem.eql(u8, method, native.method_turn_started)) {
            if (!native.decodesPresent(message.params, native.turn_started_notification)) return self.failActive("invalid_native_event", "invalid turn/started payload");
            return self.onStarted(message.params.?);
        }
        if (std.mem.eql(u8, method, native.method_agent_delta)) {
            if (!native.decodesPresent(message.params, native.agent_delta_notification)) return self.failActive("invalid_native_event", "invalid agent message delta");
            return self.onDelta(message.params.?);
        }
        if (std.mem.eql(u8, method, native.method_item_started) or std.mem.eql(u8, method, native.method_item_completed)) {
            if (!native.decodesPresent(message.params, native.item_notification)) return self.failActive("invalid_native_event", "invalid item lifecycle payload");
            return self.onItem(std.mem.eql(u8, method, native.method_item_started), message.params.?);
        }
        if (std.mem.eql(u8, method, native.method_turn_completed)) {
            if (!native.decodesPresent(message.params, native.turn_completed_notification)) return self.failActive("invalid_native_event", "invalid turn/completed payload");
            return self.onCompleted(message.params.?);
        }
    }

    fn onStarted(self: *Reducer, params: std.json.Value) !void {
        const index = self.runFor(native.text(params, &.{"threadId"}), native.text(params, &.{ "turn", "id" })) orelse return;
        const run = &self.runs.items[index];
        if (run.started or run.terminal) return;
        run.started = true;
        run.status = "running";
        self.state.status = "running";
        self.state.updated_at_ms = self.now();
        var payload = try self.runPayload(index);
        try self.put(&payload, "status", str("running"));
        try self.putNonEmpty(&payload, "model_id", run.model);
        try self.put(&payload, "started_at_ms", int(self.now()));
        try self.emit(index, "run.started", payload, "", false);
    }

    fn onDelta(self: *Reducer, params: std.json.Value) !void {
        const index = self.runFor(native.text(params, &.{"threadId"}), native.text(params, &.{"turnId"})) orelse return;
        const run = &self.runs.items[index];
        if (!run.started or run.terminal) return;
        const delta = native.text(params, &.{"delta"});
        try run.text.appendSlice(self.allocator(), delta);
        var part = std.json.ObjectMap.empty;
        try self.put(&part, "type", str("text"));
        try self.putNonEmpty(&part, "text", delta);
        var payload = try self.runPayload(index);
        try self.putNonEmpty(&payload, "message_id", run.message_id);
        try self.put(&payload, "part", .{ .object = part });
        try self.emit(index, "content.delta", payload, "", false);
    }

    fn itemName(self: *Reducer, params: std.json.Value) ![]const u8 {
        const kind = native.text(params, &.{ "item", "type" });
        if (std.mem.eql(u8, kind, "commandExecution")) return "codex.command";
        if (std.mem.eql(u8, kind, "fileChange")) return "codex.file_change";
        if (!std.mem.eql(u8, kind, "mcpToolCall")) return "";
        const server = native.text(params, &.{ "item", "server" });
        const tool = native.text(params, &.{ "item", "tool" });
        if (server.len == 0 and tool.len == 0) return "mcp.tool";
        return std.fmt.allocPrint(self.allocator(), "mcp.{s}.{s}", .{ server, tool });
    }

    fn itemArguments(self: *Reducer, params: std.json.Value) !std.json.Value {
        if (native.raw(params, &.{ "item", "arguments" })) |carried| return carried;
        const kind = native.text(params, &.{ "item", "type" });
        var arguments = std.json.ObjectMap.empty;
        if (std.mem.eql(u8, kind, "commandExecution")) {
            try self.put(&arguments, "command", str(native.text(params, &.{ "item", "command" })));
            try self.put(&arguments, "cwd", str(native.text(params, &.{ "item", "cwd" })));
            return .{ .object = arguments };
        }
        if (std.mem.eql(u8, kind, "fileChange")) {
            const changes = native.pointer(params, &.{ "item", "changes" }) orelse {
                try self.put(&arguments, "changes", .null);
                return .{ .object = arguments };
            };
            var list = std.json.Array.init(self.allocator());
            for (changes.array.items) |change| {
                var entry = std.json.ObjectMap.empty;
                try self.put(&entry, "path", str(native.text(change, &.{"path"})));
                try self.put(&entry, "kind", native.raw(change, &.{"kind"}) orelse .null);
                try self.put(&entry, "diff", str(native.text(change, &.{"diff"})));
                try list.append(.{ .object = entry });
            }
            try self.put(&arguments, "changes", .{ .array = list });
            return .{ .object = arguments };
        }
        try self.put(&arguments, "server", str(native.text(params, &.{ "item", "server" })));
        try self.put(&arguments, "tool", str(native.text(params, &.{ "item", "tool" })));
        return .{ .object = arguments };
    }

    fn actionPayload(self: *Reducer, index: usize, item: Item, with_arguments: bool) !std.json.ObjectMap {
        var payload = try self.runPayload(index);
        try self.put(&payload, "tool_call_id", str(item.tool_call_id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "execution_owner", str(execution_owner));
        try self.putNonEmpty(&payload, "name", item.name);
        if (with_arguments) try self.put(&payload, "arguments_json", item.arguments);
        return payload;
    }

    fn onItem(self: *Reducer, started: bool, params: std.json.Value) !void {
        const index = self.runFor(native.text(params, &.{"threadId"}), native.text(params, &.{"turnId"})) orelse return;
        const run = self.runs.items[index];
        if (!run.started or run.terminal) return;
        const name = try self.itemName(params);
        const native_id = native.text(params, &.{ "item", "id" });
        if (name.len == 0 or native_id.len == 0) return;
        const existing = self.findItem(native_id);
        if (started) {
            if (existing != null) return;
            const tool_call_id = try self.nextID("tool-call");
            const arguments = try self.itemArguments(params);
            try self.items.append(self.allocator(), .{ .native_id = native_id, .run_id = run.id, .tool_call_id = tool_call_id, .name = name, .arguments = arguments });
            const item = self.items.items[self.items.items.len - 1];
            try self.emit(index, "action.call.requested", try self.actionPayload(index, item, true), item.tool_call_id, false);
            try self.emit(index, "action.call.started", try self.actionPayload(index, item, true), item.tool_call_id, false);
            return;
        }
        const at = existing orelse return self.failRun(index, "invalid_native_action", "item/completed arrived without one active item/started", "");
        const item = &self.items.items[at];
        if (!std.mem.eql(u8, item.run_id, run.id) or item.terminal) return self.failRun(index, "invalid_native_action", "item/completed arrived without one active item/started", "");
        item.terminal = true;
        var payload = try self.actionPayload(index, item.*, false);
        const failure = native.pointer(params, &.{ "item", "error" });
        if (failure != null or std.mem.eql(u8, native.text(params, &.{ "item", "status" }), "failed")) {
            var message: []const u8 = "native action failed";
            if (failure != null) {
                const carried = native.text(params, &.{ "item", "error", "message" });
                if (carried.len != 0) message = carried;
            }
            var reason = std.json.ObjectMap.empty;
            try self.put(&reason, "code", str("native_action_failed"));
            try self.put(&reason, "message", str(message));
            try self.put(&payload, "error", .{ .object = reason });
            return self.emit(index, "action.call.failed", payload, item.tool_call_id, false);
        }
        if (native.raw(params, &.{ "item", "result" })) |result| {
            try self.put(&payload, "result", result);
        } else {
            var output = std.json.ObjectMap.empty;
            const carried = native.pointer(params, &.{ "item", "aggregatedOutput" });
            try self.put(&output, "output", str(if (carried) |value| value.string else ""));
            try self.put(&payload, "result", .{ .object = output });
        }
        try self.emit(index, "action.call.completed", payload, item.tool_call_id, false);
    }

    fn onCompleted(self: *Reducer, params: std.json.Value) !void {
        const index = self.runFor(native.text(params, &.{"threadId"}), native.text(params, &.{ "turn", "id" })) orelse return;
        if (self.runs.items[index].terminal) return;
        const status = native.text(params, &.{ "turn", "status" });
        try self.closePendingInteractions(index, status);
        try self.closePendingActions(index, status);
        var payload = try self.runPayload(index);
        if (std.mem.eql(u8, status, native.turn_completed)) {
            const run = self.runs.items[index];
            var response = std.json.ObjectMap.empty;
            try self.putNonEmpty(&response, "id", run.message_id);
            try self.put(&response, "role", str("assistant"));
            try self.put(&response, "content", str(run.text.items));
            try self.put(&payload, "final_response", .{ .object = response });
            try self.put(&payload, "stop_reason", str("end_turn"));
            return self.emit(index, "run.completed", payload, "", true);
        }
        if (std.mem.eql(u8, status, native.turn_interrupted)) {
            try self.put(&payload, "reason", str("native turn interrupted"));
            return self.emit(index, "run.cancelled", payload, "", true);
        }
        if (std.mem.eql(u8, status, native.turn_failed)) {
            var message: []const u8 = "native turn failed";
            if (native.pointer(params, &.{ "turn", "error" }) != null) {
                const carried = native.text(params, &.{ "turn", "error", "message" });
                if (carried.len != 0) message = carried;
            }
            var failure = std.json.ObjectMap.empty;
            try self.put(&failure, "code", str("native_turn_failed"));
            try self.put(&failure, "message", str(message));
            try self.put(&payload, "error", .{ .object = failure });
            return self.emit(index, "run.failed", payload, "", true);
        }
        try self.failRun(index, "invalid_native_terminal", "turn/completed did not contain a terminal status", "");
    }

    fn closePendingInteractions(self: *Reducer, index: usize, status: []const u8) !void {
        const run_id = self.runs.items[index].id;
        var unsettled = std.ArrayList(usize).empty;
        for (self.interactions.items, 0..) |*interaction, at| {
            if (!std.mem.eql(u8, interaction.run_id, run_id) or interaction.resolved) continue;
            interaction.resolved = true;
            try unsettled.append(self.allocator(), at);
        }
        for (unsettled.items) |at| {
            const interaction = self.interactions.items[at];
            try self.respondError(interaction.request, -32800, "OAP run terminated before interaction resolution", null);
            var payload = try self.interactionPayload(interaction, index);
            if (interaction.kind == .permission) {
                try self.putNonEmpty(&payload, "tool_call_id", interaction.tool_call_id);
                try self.put(&payload, "outcome", str(if (std.mem.eql(u8, status, native.turn_interrupted)) "cancelled" else "failed"));
                var reason = std.json.ObjectMap.empty;
                try self.put(&reason, "code", str("run_terminated"));
                try self.put(&reason, "message", str("run terminated before permission resolution"));
                try self.put(&payload, "reason", .{ .object = reason });
                try self.emit(index, "action.permission.resolved", payload, interaction.tool_call_id, false);
                continue;
            }
            try self.put(&payload, "status", str("cancelled"));
            try self.emit(index, "user.input.resolved", payload, "", false);
        }
    }

    fn closePendingActions(self: *Reducer, index: usize, status: []const u8) !void {
        const run_id = self.runs.items[index].id;
        var unsettled = std.ArrayList(usize).empty;
        for (self.items.items, 0..) |*item, at| {
            if (!std.mem.eql(u8, item.run_id, run_id) or !item.started or item.terminal) continue;
            item.terminal = true;
            try unsettled.append(self.allocator(), at);
        }
        for (unsettled.items) |at| {
            const item = self.items.items[at];
            var payload = try self.actionPayload(index, item, false);
            if (std.mem.eql(u8, status, native.turn_interrupted)) {
                try self.emit(index, "action.call.cancelled", payload, item.tool_call_id, false);
                continue;
            }
            var failure = std.json.ObjectMap.empty;
            try self.put(&failure, "code", str("native_action_incomplete"));
            try self.put(&failure, "message", str("native turn ended before the action completed"));
            try self.put(&payload, "error", .{ .object = failure });
            try self.emit(index, "action.call.failed", payload, item.tool_call_id, false);
        }
    }

    fn interactionPayload(self: *Reducer, interaction: Interaction, index: usize) !std.json.ObjectMap {
        var payload = std.json.ObjectMap.empty;
        try self.put(&payload, "interaction_id", str(interaction.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "responded_by", str(self.options.participant));
        try self.put(&payload, "session_id", str(self.session_id));
        try self.put(&payload, "run_id", str(self.runs.items[index].id));
        return payload;
    }

    fn approvalScope(method: []const u8, params: ?std.json.Value) ?Scope {
        const command = std.mem.eql(u8, method, native.method_command_approval);
        const shape = if (command) native.command_approval_params else native.file_approval_params;
        if (!native.decodesPresent(params, shape)) return null;
        const value = params.?;
        var scope = Scope{
            .thread = native.text(value, &.{"threadId"}),
            .turn = native.text(value, &.{"turnId"}),
            .item = native.text(value, &.{"itemId"}),
            .reason = "",
            .decisions = [_]bool{true} ** standard_decisions.len,
        };
        if (scope.thread.len == 0 or scope.turn.len == 0 or scope.item.len == 0) return null;
        if (native.pointer(value, &.{"reason"})) |reason| scope.reason = reason.string;
        if (!command) return scope;
        const available = native.pointer(value, &.{"availableDecisions"}) orelse return scope;
        if (!native.decodes(available, native.approval_decisions)) return null;
        scope.decisions = [_]bool{false} ** standard_decisions.len;
        var offered = false;
        for (available.array.items) |entry| {
            const decision = if (entry == .string) entry.string else "";
            const at = decisionIndex(decision) orelse return null;
            scope.decisions[at] = true;
            offered = true;
        }
        if (!offered) return null;
        return scope;
    }

    fn handleRequest(self: *Reducer, message: rpc.Message) !void {
        const id = message.id.?;
        const method = message.method;
        if (std.mem.eql(u8, method, native.method_command_approval) or std.mem.eql(u8, method, native.method_file_approval)) {
            const scope = approvalScope(method, message.params) orelse return self.respondError(id, -32602, "invalid approval request scope", null);
            if (!std.mem.eql(u8, scope.thread, self.thread_id)) return self.respondError(id, -32602, "invalid approval request scope", null);
            const index = self.runFor(scope.thread, scope.turn) orelse return self.respondError(id, -32602, "approval request does not target the active run", null);
            const run = self.runs.items[index];
            if (run.terminal) return self.respondError(id, -32602, "approval request does not target the active run", null);
            const at = self.findItem(scope.item) orelse return self.respondError(id, -32602, "approval request does not target an active action", null);
            const item = self.items.items[at];
            if (!std.mem.eql(u8, item.run_id, run.id) or !item.started or item.terminal) return self.respondError(id, -32602, "approval request does not target an active action", null);
            const interaction_id = try self.nextID("interaction");
            const interaction = Interaction{ .id = interaction_id, .kind = .permission, .run_id = run.id, .tool_call_id = item.tool_call_id, .request = id, .decisions = scope.decisions };
            try self.interactions.append(self.allocator(), interaction);
            var choices = std.json.Array.init(self.allocator());
            for (standard_decisions, 0..) |decision, position| {
                if (!scope.decisions[position]) continue;
                var choice = std.json.ObjectMap.empty;
                try self.put(&choice, "id", str(decision.id));
                try self.put(&choice, "label", str(decision.label));
                try choices.append(.{ .object = choice });
            }
            var payload = try self.interactionPayload(interaction, index);
            try self.putNonEmpty(&payload, "tool_call_id", item.tool_call_id);
            try self.put(&payload, "title", str(if (std.mem.eql(u8, method, native.method_file_approval)) "Allow Codex file changes" else "Allow Codex action"));
            try self.putNonEmpty(&payload, "description", scope.reason);
            try self.put(&payload, "choices", .{ .array = choices });
            try self.put(&payload, "arguments_json", item.arguments);
            return self.emit(index, "action.permission.requested", payload, item.tool_call_id, false);
        }
        if (std.mem.eql(u8, method, native.method_user_input)) return self.requestInput(id, message.params);
        try self.respondError(id, -32601, "unsupported Codex server request", try native.methodData(self.allocator(), method));
    }

    fn requestInput(self: *Reducer, id: rpc.Id, params: ?std.json.Value) !void {
        if (!native.decodesPresent(params, native.user_input_request_params)) return self.respondError(id, -32602, "invalid user input request", null);
        const value = params.?;
        const thread = native.text(value, &.{"threadId"});
        const turn = native.text(value, &.{"turnId"});
        const asked = native.items(value, &.{"questions"});
        if (!std.mem.eql(u8, thread, self.thread_id) or turn.len == 0 or asked.len == 0) return self.respondError(id, -32602, "invalid user input request", null);
        const index = self.runFor(thread, turn) orelse return self.respondError(id, -32602, "user input does not target the active run", null);
        if (self.runs.items[index].terminal) return self.respondError(id, -32602, "user input does not target the active run", null);

        const questions = try self.allocator().alloc(Question, asked.len);
        for (asked, 0..) |question, position| {
            const question_id = native.text(question, &.{"id"});
            const prompt = native.text(question, &.{"question"});
            if (question_id.len == 0 or prompt.len == 0 or native.flag(question, &.{"isSecret"})) return self.respondError(id, -32602, "invalid user input question", null);
            for (questions[0..position]) |earlier| {
                if (std.mem.eql(u8, earlier.id, question_id)) return self.respondError(id, -32602, "duplicate user input question id", null);
            }
            const offered = native.items(question, &.{"options"});
            const options = try self.allocator().alloc(Option, offered.len);
            for (offered, 0..) |option, at| {
                const option_id = try std.fmt.allocPrint(self.allocator(), "option-{d}", .{at + 1});
                options[at] = .{ .id = option_id, .label = native.text(option, &.{"label"}), .description = native.text(option, &.{"description"}) };
            }
            questions[position] = .{ .id = question_id, .prompt = prompt, .options = options };
        }

        const interaction_id = try self.nextID("interaction");
        const interaction = Interaction{ .id = interaction_id, .kind = .input, .run_id = self.runs.items[index].id, .tool_call_id = native.text(value, &.{"itemId"}), .request = id, .questions = questions };
        try self.interactions.append(self.allocator(), interaction);
        self.runs.items[index].status = "waiting_for_input";
        self.state.status = "waiting_for_input";
        self.state.updated_at_ms = self.now();

        var listed = std.json.Array.init(self.allocator());
        for (questions) |question| try listed.append(try self.questionValue(question));
        var payload = try self.interactionPayload(interaction, index);
        try self.put(&payload, "title", str("Codex needs input"));
        try self.put(&payload, "questions", .{ .array = listed });
        try self.emit(index, "user.input.requested", payload, "", false);

        var status = try self.runPayload(index);
        try self.put(&status, "status", str("waiting_for_input"));
        try self.put(&status, "pending_user_input_id", str(interaction_id));
        try self.put(&status, "updated_at_ms", int(self.now()));
        try self.emit(index, "run.status.updated", status, "", false);
    }

    fn questionValue(self: *Reducer, question: Question) !std.json.Value {
        var entry = std.json.ObjectMap.empty;
        try self.put(&entry, "id", str(question.id));
        try self.put(&entry, "prompt", str(question.prompt));
        try self.put(&entry, "kind", str(if (question.options.len != 0) "single_choice" else "text"));
        try self.put(&entry, "required", .{ .bool = true });
        if (question.options.len != 0) {
            var options = std.json.Array.init(self.allocator());
            for (question.options) |option| {
                var choice = std.json.ObjectMap.empty;
                try self.put(&choice, "id", str(option.id));
                try self.put(&choice, "label", str(option.label));
                try self.putNonEmpty(&choice, "description", option.description);
                try options.append(.{ .object = choice });
            }
            try self.put(&entry, "options", .{ .array = options });
        }
        return .{ .object = entry };
    }

    pub fn resolve(self: *Reducer, resolution: Resolution) !void {
        const interaction_id = if (resolution.permission != null and resolution.input == null)
            resolution.permission.?.interaction_id
        else if (resolution.input != null and resolution.permission == null)
            resolution.input.?.interaction_id
        else
            return Error.InvalidResolution;
        const at = self.findInteraction(interaction_id) orelse return Error.InteractionNotFound;
        const interaction = self.interactions.items[at];
        if (!std.mem.eql(u8, interaction.run_id, resolution.run_id)) return Error.InteractionNotFound;
        if (interaction.resolved) return Error.InteractionResolved;
        if (!std.mem.eql(u8, resolution.responded_by, self.options.participant)) return Error.WrongResponder;
        const index = self.admittedRun(interaction.run_id) orelse return Error.InteractionResolved;
        if (self.runs.items[index].terminal) return Error.InteractionResolved;
        if (interaction.kind == .permission) return self.resolvePermission(at, index, resolution.permission orelse return Error.InvalidResolution);
        try self.resolveInput(at, index, resolution.input orelse return Error.InvalidResolution);
    }

    fn scoped(self: *Reducer, index: usize, session_id: []const u8, run_id: []const u8, requested_by: []const u8, responded_by: []const u8) bool {
        return std.mem.eql(u8, session_id, self.session_id) and
            std.mem.eql(u8, run_id, self.runs.items[index].id) and
            std.mem.eql(u8, requested_by, endpoint_id) and
            std.mem.eql(u8, responded_by, self.options.participant);
    }

    fn resolvePermission(self: *Reducer, at: usize, index: usize, request: PermissionResolve) !void {
        if (!self.scoped(index, request.session_id, request.run_id, request.requested_by, request.responded_by) or request.updates_arguments) return Error.InvalidResolution;
        const interaction = self.interactions.items[at];
        const position = decisionIndex(request.choice_id) orelse return Error.InvalidResolution;
        if (!interaction.decisions[position]) return Error.InvalidResolution;
        const decision = standard_decisions[position];
        if (request.granted != decision.granted) return Error.InvalidResolution;
        try self.respond(interaction.request, try native.approvalResponse(self.allocator(), request.choice_id));
        self.interactions.items[at].resolved = true;
        var payload = try self.interactionPayload(interaction, index);
        try self.putNonEmpty(&payload, "tool_call_id", interaction.tool_call_id);
        try self.put(&payload, "outcome", str(decision.outcome));
        try self.put(&payload, "choice_id", str(request.choice_id));
        try self.put(&payload, "granted", .{ .bool = decision.granted });
        try self.emit(index, "action.permission.resolved", payload, interaction.tool_call_id, false);
    }

    fn nativeAnswers(self: *Reducer, questions: []const Question, answers: []const Answer) ![]native.NativeAnswer {
        for (answers, 0..) |answer, position| {
            const question = questionNamed(questions, answer.question_id) orelse return Error.InvalidResolution;
            for (answers[0..position]) |earlier| {
                if (std.mem.eql(u8, earlier.question_id, answer.question_id)) return Error.InvalidResolution;
            }
            if (!answerFits(question, answer)) return Error.InvalidResolution;
        }
        if (answers.len != questions.len) return Error.InvalidResolution;
        const shaped = try self.allocator().alloc(native.NativeAnswer, questions.len);
        for (questions, 0..) |question, position| {
            const answer = answerFor(answers, question.id).?;
            if (question.options.len == 0) {
                shaped[position] = .{ .question_id = question.id, .answer = answer.text };
                continue;
            }
            const label = optionLabel(question, answer.selected_option_ids[0]) orelse return Error.InvalidResolution;
            shaped[position] = .{ .question_id = question.id, .answer = label };
        }
        return shaped;
    }

    fn resolveInput(self: *Reducer, at: usize, index: usize, request: InputResolve) !void {
        if (!self.scoped(index, request.session_id, request.run_id, request.requested_by, request.responded_by)) return Error.InvalidResolution;
        const interaction = self.interactions.items[at];
        const shaped = try self.nativeAnswers(interaction.questions, request.answers);
        try self.respond(interaction.request, try native.userInputResponse(self.allocator(), shaped));
        self.interactions.items[at].resolved = true;
        self.runs.items[index].status = "running";
        self.state.status = "running";
        self.state.updated_at_ms = self.now();
        var listed = std.json.Array.init(self.allocator());
        for (request.answers) |answer| {
            var entry = std.json.ObjectMap.empty;
            try self.put(&entry, "question_id", str(answer.question_id));
            try self.putNonEmpty(&entry, "text", answer.text);
            if (answer.selected_option_ids.len != 0) {
                var selected = std.json.Array.init(self.allocator());
                for (answer.selected_option_ids) |option| try selected.append(str(option));
                try self.put(&entry, "selected_option_ids", .{ .array = selected });
            }
            try listed.append(.{ .object = entry });
        }
        var payload = try self.interactionPayload(interaction, index);
        try self.put(&payload, "status", str("submitted"));
        if (listed.items.len != 0) try self.put(&payload, "answers", .{ .array = listed });
        try self.emit(index, "user.input.resolved", payload, "", false);
        var status = try self.runPayload(index);
        try self.put(&status, "status", str("running"));
        try self.put(&status, "updated_at_ms", int(self.now()));
        try self.emit(index, "run.status.updated", status, "", false);
    }
};

fn questionNamed(questions: []const Question, id: []const u8) ?Question {
    for (questions) |question| {
        if (std.mem.eql(u8, question.id, id)) return question;
    }
    return null;
}

fn answerFor(answers: []const Answer, id: []const u8) ?Answer {
    for (answers) |answer| {
        if (std.mem.eql(u8, answer.question_id, id)) return answer;
    }
    return null;
}

fn optionLabel(question: Question, id: []const u8) ?[]const u8 {
    for (question.options) |option| {
        if (std.mem.eql(u8, option.id, id)) return option.label;
    }
    return null;
}

fn answerFits(question: Question, answer: Answer) bool {
    const has_text = answer.text.len != 0;
    if (has_text and answer.selected_option_ids.len != 0) return false;
    if (question.options.len == 0) {
        if (!has_text) return false;
    } else if (answer.selected_option_ids.len != 1) {
        return false;
    }
    for (answer.selected_option_ids, 0..) |selected, position| {
        if (selected.len == 0 or optionLabel(question, selected) == null) return false;
        for (answer.selected_option_ids[0..position]) |earlier| {
            if (std.mem.eql(u8, earlier, selected)) return false;
        }
    }
    return true;
}

const Feature = struct {
    name: []const u8,
    level: []const u8,
    reason: []const u8 = "",
    scope: []const u8 = "",
};

const tool_families = "only pinned command, file-change, and MCP item families are normalized";

pub const features = [_]Feature{
    .{ .name = "action.permissions", .level = "native", .reason = "command and file-change reverse approvals are correlated and round-trip once" },
    .{ .name = "action.tools", .level = "degraded", .reason = tool_families },
    .{ .name = "action.tools.execute", .level = "degraded", .reason = tool_families },
    .{ .name = "capabilities", .level = "native" },
    .{ .name = "protocol.initialize", .level = "native" },
    .{ .name = "run.cancel", .level = "native", .reason = "turn/interrupt targets an exact native thread and turn; settlement is asynchronous" },
    .{ .name = "run.instructions", .level = "unavailable", .reason = "this pin exposes no per-turn instruction override" },
    .{ .name = "run.model_selection", .level = "native", .reason = "turn/start carries the model for one turn", .scope = "run" },
    .{ .name = "run.reconciliation", .level = "emulated", .reason = "state is the adapter's canonical projection of native observations" },
    .{ .name = "run.replay", .level = "degraded", .reason = "only adapter-emitted events in bounded process memory are replayable" },
    .{ .name = "run.resume", .level = "degraded", .reason = "thread/resume restores native attachment; canonical replay is bounded process memory" },
    .{ .name = "run.status", .level = "native" },
    .{ .name = "run.streaming", .level = "native" },
    .{ .name = "run.structured_output", .level = "unavailable", .reason = "this pin exposes no per-turn output schema" },
    .{ .name = "run.tool_selection", .level = "unavailable", .reason = "this pin exposes no per-turn tool policy" },
    .{ .name = "session.message.delivery.auto", .level = "native" },
    .{ .name = "session.message.submit", .level = "native" },
    .{ .name = "session.open", .level = "native" },
    .{ .name = "session.state", .level = "native" },
    .{ .name = "user_input", .level = "degraded", .reason = "Codex option questions normalize to OAP single-choice input" },
};

pub fn descriptor(arena: std.mem.Allocator) !std.json.Value {
    var endpoint = std.json.ObjectMap.empty;
    try endpoint.put(arena, "id", str(endpoint_id));
    try endpoint.put(arena, "name", str(endpoint_name));
    try endpoint.put(arena, "version", str(codex_commit));
    try endpoint.put(arena, "adapter", str(adapter_name));
    var versions = std.json.Array.init(arena);
    try versions.append(str(protocol_version));
    var profiles = std.json.Array.init(arena);
    try profiles.append(str(profile));
    var table = std.json.ObjectMap.empty;
    for (features) |feature| {
        var support = std.json.ObjectMap.empty;
        try support.put(arena, "level", str(feature.level));
        if (feature.reason.len != 0) try support.put(arena, "reason", str(feature.reason));
        if (feature.scope.len != 0) try support.put(arena, "scope", str(feature.scope));
        try table.put(arena, feature.name, .{ .object = support });
    }
    var capabilities = std.json.ObjectMap.empty;
    try capabilities.put(arena, "endpoint", .{ .object = endpoint });
    try capabilities.put(arena, "protocol_versions", .{ .array = versions });
    try capabilities.put(arena, "profiles", .{ .array = profiles });
    try capabilities.put(arena, "features", .{ .object = table });
    return .{ .object = capabilities };
}

const testing = std.testing;

const thread_result = "{\"thread\":{\"id\":\"native-thread\"}}";
const turn_result = "{\"turn\":{\"id\":\"native-turn\",\"status\":\"inProgress\"}}";
const started_frame = "{\"method\":\"turn/started\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"inProgress\"}}}";
const command_started = "{\"method\":\"item/started\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"commandExecution\",\"id\":\"native-item\",\"command\":\"true\"}}}";

fn feed(reducer: *Reducer, line: []const u8) !void {
    try reducer.observe(try rpc.parseMessage(reducer.allocator(), line));
}

fn answerCall(reducer: *Reducer, result: []const u8) !void {
    const id = reducer.pending.items[0].id;
    try feed(reducer, try std.fmt.allocPrint(reducer.allocator(), "{{\"id\":{d},\"result\":{s}}}", .{ id, result }));
}

fn failCall(reducer: *Reducer) !void {
    const id = reducer.pending.items[0].id;
    try feed(reducer, try std.fmt.allocPrint(reducer.allocator(), "{{\"id\":{d},\"error\":{{\"code\":-32000,\"message\":\"refused\"}}}}", .{id}));
}

fn opened(arena: *std.heap.ArenaAllocator) !Reducer {
    var reducer = Reducer.init(arena, .{ .session_id = "session-1", .model = "glm-test", .id_width = 2 });
    try reducer.open();
    try answerCall(&reducer, thread_result);
    return reducer;
}

fn admitted(arena: *std.heap.ArenaAllocator) !Reducer {
    var reducer = try opened(arena);
    try reducer.submit(.{ .messages = &.{.{ .text = "hello" }} });
    try answerCall(&reducer, turn_result);
    return reducer;
}

fn running(arena: *std.heap.ArenaAllocator) !Reducer {
    var reducer = try admitted(arena);
    try feed(&reducer, started_frame);
    return reducer;
}

fn typeAt(reducer: *Reducer, index: usize) []const u8 {
    return reducer.envelopes.items[index].object.get("type").?.string;
}

fn payloadAt(reducer: *Reducer, index: usize) std.json.ObjectMap {
    return reducer.envelopes.items[index].object.get("payload").?.object;
}

fn lastType(reducer: *Reducer) []const u8 {
    return typeAt(reducer, reducer.envelopes.items.len - 1);
}

fn lastPayload(reducer: *Reducer) std.json.ObjectMap {
    return payloadAt(reducer, reducer.envelopes.items.len - 1);
}

fn lastWrite(reducer: *Reducer) []const u8 {
    return reducer.writes.items[reducer.writes.items.len - 1];
}

fn errorCode(reducer: *Reducer) []const u8 {
    return lastPayload(reducer).get("error").?.object.get("code").?.string;
}

fn errorMessage(reducer: *Reducer) []const u8 {
    return lastPayload(reducer).get("error").?.object.get("message").?.string;
}

test "open writes thread/start with only the configured members and settles on the native thread id" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{ .session_id = "s", .model = "m", .sandbox = "workspace-write" });
    try reducer.open();
    try testing.expectEqualStrings("{\"id\":1,\"method\":\"thread/start\",\"params\":{\"model\":\"m\",\"sandbox\":\"workspace-write\"}}", lastWrite(&reducer));
    try testing.expectError(Error.AlreadyOpened, reducer.open());
    try answerCall(&reducer, thread_result);
    try testing.expect(reducer.opened);
    try testing.expectEqualStrings("native-thread", reducer.thread_id);
    try testing.expectEqualStrings("m", reducer.state.current_model_id);
    try testing.expectEqual(@as(i64, 1), reducer.state.updated_at_ms);
    try testing.expect(reducer.settled.items[0] == .opened);
}

test "a resumed thread must come back under the id it was resumed with" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{ .resume_thread_id = "native-thread" });
    try reducer.open();
    try testing.expectEqualStrings("{\"id\":1,\"method\":\"thread/resume\",\"params\":{\"threadId\":\"native-thread\"}}", lastWrite(&reducer));
    try answerCall(&reducer, "{\"thread\":{\"id\":\"other\"}}");
    try testing.expect(!reducer.opened);
    try testing.expect(reducer.settled.items[0] == .refused);

    var fresh = Reducer.init(&arena, .{ .resume_thread_id = "native-thread" });
    try fresh.open();
    try answerCall(&fresh, thread_result);
    try testing.expect(fresh.opened);
    try testing.expectEqualStrings("session-1", fresh.session_id);
}

test "an open with no participant is refused before anything is written" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{ .participant = "" });
    try testing.expectError(Error.InvalidParticipant, reducer.open());
    try testing.expectEqual(@as(usize, 0), reducer.writes.items.len);
}

test "a thread/start response with no thread id or the wrong shape refuses the open" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    for ([_][]const u8{ "{\"thread\":{}}", "{\"thread\":{\"id\":7}}", "[]" }) |result| {
        var reducer = Reducer.init(&arena, .{});
        try reducer.open();
        try answerCall(&reducer, result);
        try testing.expect(!reducer.opened);
        try testing.expect(reducer.settled.items[0] == .refused);
    }
}

test "submit mints the prompt ids, the run and its message before writing turn/start, and the submission id after" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try opened(&arena);
    try reducer.submit(.{ .messages = &.{ .{ .text = "a" }, .{ .id = "given", .text = "b" } }, .model_id = "glm-per-turn" });
    try testing.expectEqualStrings(
        "{\"id\":2,\"method\":\"turn/start\",\"params\":{\"threadId\":\"native-thread\",\"input\":[{\"type\":\"text\",\"text\":\"a\"},{\"type\":\"text\",\"text\":\"b\"}],\"model\":\"glm-per-turn\"}}",
        lastWrite(&reducer),
    );
    try testing.expectEqualStrings("queued", reducer.state.status);
    try testing.expectEqualStrings("run-02", reducer.state.active_run_id);
    try answerCall(&reducer, turn_result);
    const admission = reducer.settled.items[1].admitted;
    try testing.expectEqualStrings("message-01", admission.message_ids[0]);
    try testing.expectEqualStrings("given", admission.message_ids[1]);
    try testing.expectEqualStrings("run-02", admission.run_id);
    try testing.expectEqualStrings("submission-04", admission.submission_id);
    try testing.expectEqualStrings("glm-per-turn", admission.model_id);
    try testing.expectEqualStrings("glm-test", reducer.state.current_model_id);
}

test "a submission the adapter cannot carry is refused with the reason Go gives" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try opened(&arena);
    try testing.expectError(Error.ModelNotFound, reducer.submit(.{ .messages = &.{.{ .text = "a" }}, .model_id = "" }));
    try testing.expectError(Error.InvalidSubmission, reducer.submit(.{ .messages = &.{} }));
    try testing.expectError(Error.UnsupportedInput, reducer.submit(.{ .messages = &.{.{ .role = "assistant", .text = "a" }} }));
    try testing.expectError(Error.UnsupportedInput, reducer.submit(.{ .messages = &.{.{ .text = null }} }));
    try reducer.submit(.{ .messages = &.{.{ .text = "a" }} });
    try testing.expectError(Error.RunActive, reducer.submit(.{ .messages = &.{.{ .text = "b" }} }));

    var closed = Reducer.init(&arena, .{});
    try testing.expectError(Error.NotOpen, closed.submit(.{ .messages = &.{.{ .text = "a" }} }));
}

test "a refused turn/start leaves the session idle and usable, while a turn with no id retires it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try opened(&arena);
    try reducer.submit(.{ .messages = &.{.{ .text = "a" }} });
    try failCall(&reducer);
    try testing.expectEqualStrings("idle", reducer.state.status);
    try testing.expect(reducer.active == null);
    try testing.expectEqual(@as(i64, -32000), reducer.settled.items[1].refused.code.?);
    try reducer.submit(.{ .messages = &.{.{ .text = "b" }} });
    try answerCall(&reducer, "{\"turn\":{\"status\":\"inProgress\"}}");
    try testing.expect(reducer.closed);
    try testing.expectEqualStrings("closed", reducer.state.status);
    try testing.expectError(Error.SessionClosed, reducer.submit(.{ .messages = &.{.{ .text = "c" }} }));
}

test "a reverse request that arrives before the thread is open is answered once it is" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{ .session_id = "s" });
    try reducer.open();
    try feed(&reducer, "{\"id\":9,\"method\":\"item/permissions/requestApproval\",\"params\":{}}");
    try testing.expectEqual(@as(usize, 1), reducer.writes.items.len);
    try answerCall(&reducer, thread_result);
    try testing.expectEqualStrings("{\"error\":{\"code\":-32601,\"message\":\"unsupported Codex server request\",\"data\":{\"method\":\"item/permissions/requestApproval\"}},\"id\":9}", lastWrite(&reducer));
}

test "turn/started opens the run once and a delta before it is dropped" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try admitted(&arena);
    try feed(&reducer, "{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"delta\":\"early\"}}");
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try feed(&reducer, started_frame);
    try feed(&reducer, started_frame);
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
    try testing.expectEqualStrings("running", reducer.state.status);
    try feed(&reducer, "{\"method\":\"item/agentMessage/delta\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"delta\":\"\"}}");
    try testing.expect(payloadAt(&reducer, 1).get("part").?.object.get("text") == null);
}

test "a notification for another thread or turn is not guessed onto the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"other\",\"turn\":{\"id\":\"native-turn\",\"status\":\"completed\"}}}");
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"other\",\"status\":\"completed\"}}}");
    try feed(&reducer, "{\"method\":\"thread/status/changed\",\"params\":{\"threadId\":\"native-thread\"}}");
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
}

test "each native notification that fails to decode fails the active run under its own message" {
    const cases = [_]struct { frame: []const u8, message: []const u8 }{
        .{ .frame = "{\"method\":\"turn/started\",\"params\":{\"threadId\":1}}", .message = "invalid turn/started payload" },
        .{ .frame = "{\"method\":\"item/agentMessage/delta\"}", .message = "invalid agent message delta" },
        .{ .frame = "{\"method\":\"item/completed\",\"params\":{\"item\":[]}}", .message = "invalid item lifecycle payload" },
        .{ .frame = "{\"method\":\"turn/completed\",\"params\":\"x\"}", .message = "invalid turn/completed payload" },
    };
    for (cases) |case| {
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var reducer = try running(&arena);
        try feed(&reducer, case.frame);
        try testing.expectEqualStrings("run.failed", lastType(&reducer));
        try testing.expectEqualStrings("invalid_native_event", errorCode(&reducer));
        try testing.expectEqualStrings(case.message, errorMessage(&reducer));
        try testing.expect(lastPayload(&reducer).get("settled_by") == null);
    }
}

test "an action is requested and started together and completes with its native result or its output" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, command_started);
    try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("action.call.started", typeAt(&reducer, 2));
    try testing.expectEqualStrings("tool-call-06", reducer.envelopes.items[2].object.get("tool_call_id").?.string);
    try feed(&reducer, command_started);
    try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);
    try feed(&reducer, "{\"method\":\"item/completed\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"commandExecution\",\"id\":\"native-item\",\"aggregatedOutput\":null}}}");
    try testing.expectEqualStrings("action.call.completed", lastType(&reducer));
    try testing.expectEqualStrings("", lastPayload(&reducer).get("result").?.object.get("output").?.string);
    try testing.expect(lastPayload(&reducer).get("arguments_json") == null);

    try feed(&reducer, "{\"method\":\"item/started\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"mcpToolCall\",\"id\":\"mcp\",\"arguments\":null}}}");
    try testing.expectEqualStrings("mcp.tool", lastPayload(&reducer).get("name").?.string);
    try testing.expect(lastPayload(&reducer).get("arguments_json").? == .null);
    try feed(&reducer, "{\"method\":\"item/completed\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"mcpToolCall\",\"id\":\"mcp\",\"result\":null}}}");
    try testing.expect(lastPayload(&reducer).get("result").? == .null);
}

test "an item family outside the pin, or one without an id, is not an action" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, "{\"method\":\"item/started\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"agentMessage\",\"id\":\"m\"}}}");
    try feed(&reducer, "{\"method\":\"item/started\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"commandExecution\"}}}");
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
    try testing.expectEqual(@as(usize, 0), reducer.items.items.len);
}

test "a failed action reports its native message, or a default when it carries none" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, command_started);
    try feed(&reducer, "{\"method\":\"item/completed\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"commandExecution\",\"id\":\"native-item\",\"error\":{\"message\":\"boom\"}}}}");
    try testing.expectEqualStrings("action.call.failed", lastType(&reducer));
    try testing.expectEqualStrings("native_action_failed", errorCode(&reducer));
    try testing.expectEqualStrings("boom", errorMessage(&reducer));
    try feed(&reducer, "{\"method\":\"item/started\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"fileChange\",\"id\":\"f\"}}}");
    try testing.expect(lastPayload(&reducer).get("arguments_json").?.object.get("changes").? == .null);
    try feed(&reducer, "{\"method\":\"item/completed\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"fileChange\",\"id\":\"f\",\"status\":\"failed\"}}}");
    try testing.expectEqualStrings("native action failed", errorMessage(&reducer));
}

test "an item/completed with no matching open item/started fails the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, "{\"method\":\"item/completed\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"item\":{\"type\":\"commandExecution\",\"id\":\"never\"}}}");
    try testing.expectEqualStrings("run.failed", lastType(&reducer));
    try testing.expectEqualStrings("invalid_native_action", errorCode(&reducer));
}

test "turn/completed maps its status to one terminal and refuses a status that is not terminal" {
    const cases = [_]struct { turn: []const u8, kind: []const u8, code: []const u8, message: []const u8 }{
        .{ .turn = "{\"id\":\"native-turn\",\"status\":\"failed\"}", .kind = "run.failed", .code = "native_turn_failed", .message = "native turn failed" },
        .{ .turn = "{\"id\":\"native-turn\",\"status\":\"failed\",\"error\":{\"message\":\"\"}}", .kind = "run.failed", .code = "native_turn_failed", .message = "native turn failed" },
        .{ .turn = "{\"id\":\"native-turn\",\"status\":\"inProgress\"}", .kind = "run.failed", .code = "invalid_native_terminal", .message = "turn/completed did not contain a terminal status" },
        .{ .turn = "{\"id\":\"native-turn\",\"status\":\"interrupted\"}", .kind = "run.cancelled", .code = "", .message = "" },
    };
    for (cases) |case| {
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var reducer = try running(&arena);
        try feed(&reducer, try std.fmt.allocPrint(arena.allocator(), "{{\"method\":\"turn/completed\",\"params\":{{\"threadId\":\"native-thread\",\"turn\":{s}}}}}", .{case.turn}));
        try testing.expectEqualStrings(case.kind, lastType(&reducer));
        if (case.code.len != 0) {
            try testing.expectEqualStrings(case.code, errorCode(&reducer));
            try testing.expectEqualStrings(case.message, errorMessage(&reducer));
        }
        try testing.expectEqualStrings("idle", reducer.state.status);
        try testing.expect(reducer.active == null);
    }
}

const approval_frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"native-item\",\"startedAtMs\":1}}";

test "an approval outside the thread, the run or an open action is answered -32602 without an interaction" {
    const cases = [_]struct { frame: []const u8, message: []const u8 }{
        .{ .frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"other\",\"turnId\":\"native-turn\",\"itemId\":\"native-item\"}}", .message = "invalid approval request scope" },
        .{ .frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\"}}", .message = "invalid approval request scope" },
        .{ .frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"native-item\",\"startedAtMs\":1.5}}", .message = "invalid approval request scope" },
        .{ .frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"native-item\",\"availableDecisions\":[]}}", .message = "invalid approval request scope" },
        .{ .frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"native-item\",\"availableDecisions\":[\"acceptWithExecpolicyAmendment\"]}}", .message = "invalid approval request scope" },
        .{ .frame = "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"other\",\"itemId\":\"native-item\"}}", .message = "approval request does not target the active run" },
        .{ .frame = "{\"id\":7,\"method\":\"item/fileChange/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"other\"}}", .message = "approval request does not target an active action" },
    };
    for (cases) |case| {
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var reducer = try running(&arena);
        try feed(&reducer, command_started);
        try feed(&reducer, case.frame);
        try testing.expectEqualStrings(try std.fmt.allocPrint(arena.allocator(), "{{\"error\":{{\"code\":-32602,\"message\":\"{s}\"}},\"id\":7}}", .{case.message}), lastWrite(&reducer));
        try testing.expectEqual(@as(usize, 0), reducer.interactions.items.len);
        try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);
    }
}

test "an approval offers only the decisions Codex listed, in the adapter's fixed order" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, command_started);
    try feed(&reducer, "{\"id\":7,\"method\":\"item/commandExecution/requestApproval\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"native-item\",\"availableDecisions\":[\"cancel\",\"acceptForSession\",\"cancel\"],\"reason\":null}}");
    const choices = lastPayload(&reducer).get("choices").?.array.items;
    try testing.expectEqual(@as(usize, 2), choices.len);
    try testing.expectEqualStrings("acceptForSession", choices[0].object.get("id").?.string);
    try testing.expectEqualStrings("cancel", choices[1].object.get("id").?.string);
    try testing.expect(lastPayload(&reducer).get("description") == null);
    const pending = reducer.pendingInteraction().?;
    const permission = PermissionResolve{ .interaction_id = pending, .requested_by = endpoint_id, .responded_by = "user", .session_id = "session-1", .run_id = "run-02", .choice_id = "accept", .granted = true };
    try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = permission }));
    var cancelled = permission;
    cancelled.choice_id = "cancel";
    cancelled.granted = false;
    try reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = cancelled });
    try testing.expectEqualStrings("{\"id\":7,\"result\":{\"decision\":\"cancel\"}}", lastWrite(&reducer));
    try testing.expectEqualStrings("cancelled", lastPayload(&reducer).get("outcome").?.string);
    try testing.expect(!lastPayload(&reducer).get("granted").?.bool);
}

test "only the declared responder resolves a permission, once, with a choice that matches its grant" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, command_started);
    try feed(&reducer, approval_frame);
    const pending = reducer.pendingInteraction().?;
    const base = PermissionResolve{ .interaction_id = pending, .requested_by = endpoint_id, .responded_by = "user", .session_id = "session-1", .run_id = "run-02", .choice_id = "decline", .granted = false };
    const writes = reducer.writes.items.len;
    try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user" }));
    try testing.expectError(Error.InteractionNotFound, reducer.resolve(.{ .run_id = "run-99", .responded_by = "user", .permission = base }));
    try testing.expectError(Error.WrongResponder, reducer.resolve(.{ .run_id = "run-02", .responded_by = "other", .permission = base }));
    var granted = base;
    granted.granted = true;
    try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = granted }));
    var edited = base;
    edited.updates_arguments = true;
    try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = edited }));
    var foreign = base;
    foreign.session_id = "other";
    try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = foreign }));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .input = .{ .interaction_id = pending, .requested_by = endpoint_id, .responded_by = "user", .session_id = "session-1", .run_id = "run-02", .answers = &.{} } }));
    try testing.expectEqual(writes, reducer.writes.items.len);
    try reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = base });
    try testing.expectEqualStrings("rejected", lastPayload(&reducer).get("outcome").?.string);
    try testing.expectError(Error.InteractionResolved, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .permission = base }));
}

const input_frame = "{\"id\":9,\"method\":\"item/tool/requestUserInput\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"itemId\":\"tool-item\",\"questions\":[{\"id\":\"mode\",\"question\":\"Choose\",\"options\":[{\"label\":\"Fast\",\"description\":\"\"},{\"label\":\"Safe\",\"description\":\"More checks\"}]},{\"id\":\"note\",\"question\":\"Add note\",\"options\":[]}]}}";

test "user input questions that are secret, unnamed, unprompted or repeated are refused before an interaction exists" {
    const cases = [_]struct { questions: []const u8, message: []const u8 }{
        .{ .questions = "[]", .message = "invalid user input request" },
        .{ .questions = "[{\"id\":\"a\",\"question\":\"q\",\"isSecret\":true}]", .message = "invalid user input question" },
        .{ .questions = "[{\"id\":\"\",\"question\":\"q\"}]", .message = "invalid user input question" },
        .{ .questions = "[{\"id\":\"a\",\"question\":\"\"}]", .message = "invalid user input question" },
        .{ .questions = "[{\"id\":\"a\",\"question\":\"q\"},{\"id\":\"a\",\"question\":\"r\"}]", .message = "duplicate user input question id" },
        .{ .questions = "[{\"id\":\"a\",\"question\":\"q\",\"isOther\":\"yes\"}]", .message = "invalid user input request" },
    };
    for (cases) |case| {
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var reducer = try running(&arena);
        const ids = reducer.ids;
        try feed(&reducer, try std.fmt.allocPrint(arena.allocator(), "{{\"id\":9,\"method\":\"item/tool/requestUserInput\",\"params\":{{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\",\"questions\":{s}}}}}", .{case.questions}));
        try testing.expectEqualStrings(try std.fmt.allocPrint(arena.allocator(), "{{\"error\":{{\"code\":-32602,\"message\":\"{s}\"}},\"id\":9}}", .{case.message}), lastWrite(&reducer));
        try testing.expectEqual(ids, reducer.ids);
        try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
    }
}

test "an input request waits the run, an empty option list is free text, and answers go back as labels" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, input_frame);
    try testing.expectEqualStrings("waiting_for_input", reducer.state.status);
    const questions = payloadAt(&reducer, 1).get("questions").?.array.items;
    try testing.expectEqualStrings("single_choice", questions[0].object.get("kind").?.string);
    try testing.expect(questions[0].object.get("options").?.array.items[0].object.get("description") == null);
    try testing.expectEqualStrings("text", questions[1].object.get("kind").?.string);
    try testing.expect(questions[1].object.get("options") == null);
    const pending = reducer.pendingInteraction().?;
    const base = InputResolve{ .interaction_id = pending, .requested_by = endpoint_id, .responded_by = "user", .session_id = "session-1", .run_id = "run-02", .answers = &.{} };
    const rejected = [_][]const Answer{
        &.{.{ .question_id = "mode", .selected_option_ids = &.{"option-2"} }},
        &.{ .{ .question_id = "mode", .selected_option_ids = &.{"option-3"} }, .{ .question_id = "note", .text = "n" } },
        &.{ .{ .question_id = "mode", .selected_option_ids = &.{ "option-1", "option-2" } }, .{ .question_id = "note", .text = "n" } },
        &.{ .{ .question_id = "mode", .text = "Fast" }, .{ .question_id = "note", .text = "n" } },
        &.{ .{ .question_id = "mode", .selected_option_ids = &.{"option-1"} }, .{ .question_id = "note" } },
        &.{ .{ .question_id = "mode", .selected_option_ids = &.{"option-1"} }, .{ .question_id = "mode", .selected_option_ids = &.{"option-1"} } },
        &.{ .{ .question_id = "mode", .selected_option_ids = &.{"option-1"} }, .{ .question_id = "other", .text = "n" } },
    };
    for (rejected) |answers| {
        var attempt = base;
        attempt.answers = answers;
        try testing.expectError(Error.InvalidResolution, reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .input = attempt }));
    }
    var accepted = base;
    accepted.answers = &.{ .{ .question_id = "note", .text = "ship it" }, .{ .question_id = "mode", .selected_option_ids = &.{"option-1"} } };
    try reducer.resolve(.{ .run_id = "run-02", .responded_by = "user", .input = accepted });
    try testing.expectEqualStrings("{\"id\":9,\"result\":{\"answers\":{\"mode\":{\"answers\":[\"Fast\"]},\"note\":{\"answers\":[\"ship it\"]}}}}", lastWrite(&reducer));
    try testing.expectEqualStrings("user.input.resolved", typeAt(&reducer, 3));
    try testing.expectEqualStrings("note", payloadAt(&reducer, 3).get("answers").?.array.items[0].object.get("question_id").?.string);
    try testing.expectEqualStrings("running", lastPayload(&reducer).get("status").?.string);
    try testing.expectEqualStrings("running", reducer.state.status);
}

test "a terminal closes open interactions with -32800 and settles open actions by how the turn ended" {
    const cases = [_]struct { status: []const u8, outcome: []const u8, action: []const u8 }{
        .{ .status = "interrupted", .outcome = "cancelled", .action = "action.call.cancelled" },
        .{ .status = "completed", .outcome = "failed", .action = "action.call.failed" },
    };
    for (cases) |case| {
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var reducer = try running(&arena);
        try feed(&reducer, command_started);
        try feed(&reducer, approval_frame);
        try feed(&reducer, input_frame);
        const before = reducer.envelopes.items.len;
        try feed(&reducer, try std.fmt.allocPrint(arena.allocator(), "{{\"method\":\"turn/completed\",\"params\":{{\"threadId\":\"native-thread\",\"turn\":{{\"id\":\"native-turn\",\"status\":\"{s}\"}}}}}}", .{case.status}));
        try testing.expectEqualStrings("{\"error\":{\"code\":-32800,\"message\":\"OAP run terminated before interaction resolution\"},\"id\":7}", reducer.writes.items[reducer.writes.items.len - 2]);
        try testing.expectEqualStrings("{\"error\":{\"code\":-32800,\"message\":\"OAP run terminated before interaction resolution\"},\"id\":9}", lastWrite(&reducer));
        try testing.expectEqualStrings("action.permission.resolved", typeAt(&reducer, before));
        try testing.expectEqualStrings(case.outcome, payloadAt(&reducer, before).get("outcome").?.string);
        try testing.expectEqualStrings("run_terminated", payloadAt(&reducer, before).get("reason").?.object.get("code").?.string);
        try testing.expectEqualStrings("user.input.resolved", typeAt(&reducer, before + 1));
        try testing.expectEqualStrings("cancelled", payloadAt(&reducer, before + 1).get("status").?.string);
        try testing.expectEqualStrings(case.action, typeAt(&reducer, before + 2));
        try testing.expectEqual(before + 4, reducer.envelopes.items.len);
        try testing.expect(reducer.pendingInteraction() == null);
    }
}

test "a second terminal for a settled turn is absorbed without emitting or minting" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"completed\"}}}");
    const ids = reducer.ids;
    const clock = reducer.clock;
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"failed\"}}}");
    try feed(&reducer, command_started);
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqual(ids, reducer.ids);
    try testing.expectEqual(clock, reducer.clock);
    try testing.expectEqualStrings("2", reducer.state.transcript_cursor);
}

test "cancel writes one interrupt, announces cancelling on its acknowledgement, and is idempotent after" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try testing.expectError(Error.RunNotFound, reducer.cancel("run-99"));
    try testing.expect(try reducer.cancel("run-02") == null);
    try testing.expectEqualStrings("{\"id\":3,\"method\":\"turn/interrupt\",\"params\":{\"threadId\":\"native-thread\",\"turnId\":\"native-turn\"}}", lastWrite(&reducer));
    try testing.expect(try reducer.cancel("run-02") == null);
    try testing.expectEqual(@as(usize, 3), reducer.writes.items.len);
    try answerCall(&reducer, "{}");
    try testing.expectEqualStrings("cancelling", lastPayload(&reducer).get("status").?.string);
    try testing.expectEqualStrings("cancelling", reducer.settled.items[2].cancel.status);
    const again = (try reducer.cancel("run-02")).?;
    try testing.expect(again.accepted);
    try testing.expectEqualStrings("cancelling", again.status);
    try testing.expectEqual(@as(usize, 3), reducer.writes.items.len);
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"interrupted\"}}}");
    const settled = (try reducer.cancel("run-02")).?;
    try testing.expect(settled.accepted);
    try testing.expectEqualStrings("cancelled", settled.status);
}

test "a natural terminal wins the race with an interrupt still in flight" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    _ = try reducer.cancel("run-02");
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"completed\"}}}");
    try answerCall(&reducer, "{}");
    try testing.expectEqualStrings("run.completed", lastType(&reducer));
    const outcome = reducer.settled.items[2].cancel;
    try testing.expect(!outcome.accepted);
    try testing.expectEqualStrings("completed", outcome.status);
    const late = (try reducer.cancel("run-02")).?;
    try testing.expect(!late.accepted);
}

test "a refused or undecodable interrupt leaves the run cancellable again" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    _ = try reducer.cancel("run-02");
    try failCall(&reducer);
    try testing.expect(reducer.settled.items[2] == .refused);
    _ = try reducer.cancel("run-02");
    try answerCall(&reducer, "[]");
    try testing.expect(reducer.settled.items[3] == .refused);
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
    try testing.expect(try reducer.cancel("run-02") == null);
    try testing.expectEqual(@as(usize, 5), reducer.writes.items.len);
}

test "a transport failure settles the active run as inferred and refuses every call still pending" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    _ = try reducer.cancel("run-02");
    try reducer.transportFailed("");
    try testing.expectEqualStrings("native_transport_closed", errorCode(&reducer));
    try testing.expectEqualStrings("Codex app-server transport closed before terminal settlement", errorMessage(&reducer));
    try testing.expectEqualStrings("inferred", lastPayload(&reducer).get("settled_by").?.string);
    try testing.expect(reducer.settled.items[2] == .refused);
    try reducer.transportFailed("again");
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
}

test "a response to no pending call closes the transport the way a strict client does" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, "{\"id\":\"stray\",\"result\":{}}");
    try testing.expectEqualStrings("codex app-server rpc: response id is not pending: stray", errorMessage(&reducer));
    try testing.expect(reducer.transport_closed);
}

test "a transport failure while turn/start is pending retires the session without a run event" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try opened(&arena);
    try reducer.submit(.{ .messages = &.{.{ .text = "a" }} });
    try reducer.transportFailed("gone");
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try testing.expect(reducer.closed);
    try testing.expectEqualStrings("gone", reducer.settled.items[1].refused.message);
}

test "close refuses a session with an open run and is idempotent after" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try testing.expectError(Error.RunActive, reducer.close());
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"completed\"}}}");
    try reducer.close();
    try reducer.close();
    try testing.expectEqualStrings("closed", reducer.state.status);
    try feed(&reducer, started_frame);
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
}

test "the capability descriptor lists its features in Go's sorted map order" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    for (features[1..], 0..) |feature, index| {
        try testing.expect(std.mem.order(u8, features[index].name, feature.name) == .lt);
    }
    const value = try descriptor(arena.allocator());
    try testing.expectEqualStrings("run", value.object.get("features").?.object.get("run.model_selection").?.object.get("scope").?.string);
}

fn scenarioProbe(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var reducer = try running(&arena);
    try feed(&reducer, command_started);
    try feed(&reducer, approval_frame);
    try feed(&reducer, input_frame);
    _ = try reducer.cancel("run-02");
    try answerCall(&reducer, "{}");
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"interrupted\"}}}");
}

test "a reducer run over an arena propagates every allocation failure and leaks nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, scenarioProbe, .{});
}
