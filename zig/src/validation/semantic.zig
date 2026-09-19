const std = @import("std");

pub const code_duplicate_envelope_id = "duplicate_envelope_id";
pub const code_illegal_run_transition = "illegal_run_transition";
pub const code_missing_run_started = "missing_run_started";
pub const code_missing_run_terminal = "missing_run_terminal";
pub const code_duplicate_run_terminal = "duplicate_run_terminal";
pub const code_event_after_terminal = "event_after_terminal";
pub const code_sequence_gap = "sequence_gap";
pub const code_sequence_regression = "sequence_regression";
pub const code_cancel_not_settled = "cancel_not_settled";

pub const implemented = [_][]const u8{
    code_duplicate_envelope_id,
    code_illegal_run_transition,
    code_missing_run_started,
    code_missing_run_terminal,
    code_duplicate_run_terminal,
    code_event_after_terminal,
    code_sequence_gap,
    code_sequence_regression,
    code_cancel_not_settled,
};

pub fn isImplemented(code: []const u8) bool {
    for (implemented) |name| {
        if (std.mem.eql(u8, name, code)) return true;
    }
    return false;
}

pub const Diagnostic = struct {
    code: []const u8,
    index: usize,
};

const run_event_types = [_][]const u8{
    "run.started",                 "run.status.updated",         "content.delta",
    "run.completed",               "run.failed",                 "run.cancelled",
    "action.call.requested",       "action.call.started",        "action.call.progress",
    "action.call.completed",       "action.call.failed",         "action.call.cancelled",
    "action.permission.requested", "action.permission.resolved", "user.input.requested",
    "user.input.resolved",
};

fn isRunEvent(declared: []const u8) bool {
    for (run_event_types) |name| {
        if (std.mem.eql(u8, name, declared)) return true;
    }
    return false;
}

fn isTerminal(declared: []const u8) bool {
    return std.mem.eql(u8, declared, "run.completed") or
        std.mem.eql(u8, declared, "run.failed") or
        std.mem.eql(u8, declared, "run.cancelled");
}

fn preStartSettlement(declared: []const u8) bool {
    return std.mem.eql(u8, declared, "run.failed") or std.mem.eql(u8, declared, "run.cancelled");
}

fn legalRunStatusTransition(from: []const u8, to: []const u8) bool {
    if (std.mem.eql(u8, from, "queued")) {
        return std.mem.eql(u8, to, "running") or std.mem.eql(u8, to, "cancelling");
    }
    if (std.mem.eql(u8, from, "running") or std.mem.eql(u8, from, "waiting_for_input")) {
        return std.mem.eql(u8, to, "running") or std.mem.eql(u8, to, "waiting_for_input") or
            std.mem.eql(u8, to, "cancelling");
    }
    if (std.mem.eql(u8, from, "cancelling")) return std.mem.eql(u8, to, "cancelling");
    return false;
}

fn namesShape(value: []const u8, started: []const u8, queued: []const u8) []const u8 {
    if (std.mem.eql(u8, value, started)) return "started";
    if (std.mem.eql(u8, value, queued)) return "queued";
    return "";
}

fn admissionShape(admission: []const u8, delivery: []const u8, status: []const u8) bool {
    const votes = [_][]const u8{
        namesShape(admission, "started", "queued"),
        namesShape(delivery, "start", "queue"),
        namesShape(status, "running", "queued"),
    };
    var named: []const u8 = "";
    for (votes) |shape| {
        if (shape.len == 0) return false;
        if (named.len != 0 and !std.mem.eql(u8, named, shape)) return false;
        named = shape;
    }
    return true;
}

fn field(envelope: std.json.Value, name: []const u8) []const u8 {
    if (envelope != .object) return "";
    const value = envelope.object.get(name) orelse return "";
    if (value != .string) return "";
    return value.string;
}

fn unsigned(envelope: std.json.Value, name: []const u8) ?u64 {
    if (envelope != .object) return null;
    const value = envelope.object.get(name) orelse return null;
    return switch (value) {
        .integer => |n| if (n < 0) null else @intCast(n),
        else => null,
    };
}

fn member(container: std.json.Value, name: []const u8) ?std.json.Value {
    if (container != .object) return null;
    return container.object.get(name);
}

fn memberString(container: std.json.Value, name: []const u8) []const u8 {
    const value = member(container, name) orelse return "";
    if (value != .string) return "";
    return value.string;
}

fn memberBool(container: std.json.Value, name: []const u8) bool {
    const value = member(container, name) orelse return false;
    if (value != .bool) return false;
    return value.bool;
}

const Run = struct {
    id: []const u8,
    session: []const u8,
    admitted: bool = false,
    started: bool = false,
    terminal: bool = false,
    terminal_type: []const u8 = "",
    next: u64 = 1,
    last_index: usize = 0,
    cancel_accepted: bool = false,
    recovered: bool = false,
    status: []const u8 = "",
};

const Session = struct {
    active: []const u8 = "",
    order: usize = 0,
};

const Recovery = struct {
    run: []const u8 = "",
    gap: bool = false,
    cursor: u64 = 0,
    cursor_set: bool = false,
    state_checked: bool = false,
    first_replay_seen: bool = false,
};

const Request = struct {
    declared: []const u8,
    carries_message: bool = false,
    responded: bool = false,
};

pub const Machine = struct {
    allocator: std.mem.Allocator,
    arena: std.heap.ArenaAllocator,
    diagnostics: std.ArrayList(Diagnostic) = .empty,
    ids: std.StringArrayHashMapUnmanaged(usize) = .empty,
    runs: std.StringArrayHashMapUnmanaged(*Run) = .empty,
    sessions: std.StringArrayHashMapUnmanaged(*Session) = .empty,
    recoveries: std.StringArrayHashMapUnmanaged(*Recovery) = .empty,
    requests: std.StringArrayHashMapUnmanaged(Request) = .empty,

    pub fn init(allocator: std.mem.Allocator) Machine {
        return .{ .allocator = allocator, .arena = std.heap.ArenaAllocator.init(allocator) };
    }

    pub fn deinit(self: *Machine) void {
        self.diagnostics.deinit(self.allocator);
        self.ids.deinit(self.allocator);
        self.runs.deinit(self.allocator);
        self.sessions.deinit(self.allocator);
        self.recoveries.deinit(self.allocator);
        self.requests.deinit(self.allocator);
        self.arena.deinit();
    }

    fn add(self: *Machine, code: []const u8, index: usize) !void {
        try self.diagnostics.append(self.allocator, .{ .code = code, .index = index });
    }

    fn sessionFor(self: *Machine, session: []const u8) !*Session {
        if (self.sessions.get(session)) |existing| return existing;
        const created = try self.arena.allocator().create(Session);
        created.* = .{};
        try self.sessions.put(self.allocator, session, created);
        return created;
    }

    pub fn apply(self: *Machine, index: usize, envelope: std.json.Value) !void {
        const id = field(envelope, "id");
        if (id.len != 0) {
            if (self.ids.get(id) != null) {
                try self.add(code_duplicate_envelope_id, index);
                return;
            }
            try self.ids.put(self.allocator, id, index);
        }

        const declared = field(envelope, "type");
        const payload = member(envelope, "payload") orelse std.json.Value{ .null = {} };

        if (std.mem.endsWith(u8, declared, ".request")) {
            try self.requests.put(self.allocator, id, .{
                .declared = declared,
                .carries_message = member(payload, "message") != null,
            });
        }
        if (std.mem.endsWith(u8, declared, ".response") and try self.duplicateResponse(envelope, declared)) return;

        if (std.mem.eql(u8, declared, "session.message.submit.response")) {
            try self.admit(index, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "session.open.response")) {
            try self.openResponse(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "session.state.response") or
            std.mem.eql(u8, declared, "session.state.updated"))
        {
            try self.stateDocument(index, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "run.cancel.response")) {
            try self.cancelResponse(index, payload);
            return;
        }
        if (isRunEvent(declared)) try self.runEvent(index, envelope, declared);
    }

    fn duplicateResponse(self: *Machine, envelope: std.json.Value, declared: []const u8) !bool {
        const request = self.requests.getPtr(field(envelope, "in_reply_to")) orelse return false;
        if (!std.mem.eql(u8, declared, "error.response")) {
            const suffix = request.declared[0 .. request.declared.len - ".request".len];
            if (!std.mem.startsWith(u8, declared, suffix) or
                !std.mem.eql(u8, declared[suffix.len..], ".response")) return false;
        }
        if (request.responded) return true;
        request.responded = true;
        return false;
    }

    fn admit(self: *Machine, index: usize, payload: std.json.Value) !void {
        if (!memberBool(payload, "accepted")) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        const run_id = memberString(payload, "run_id");
        if (run_id.len == 0) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        if (!admissionShape(
            memberString(payload, "admission"),
            memberString(payload, "effective_delivery"),
            memberString(payload, "status"),
        )) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        if (self.runs.get(run_id) != null) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        const session_id = memberString(payload, "session_id");
        const holder = try self.sessionFor(session_id);
        const queued = std.mem.eql(u8, memberString(payload, "admission"), "queued");

        const run = try self.arena.allocator().create(Run);
        run.* = .{
            .id = run_id,
            .session = session_id,
            .admitted = true,
            .last_index = index,
            .status = "queued",
        };
        try self.runs.put(self.allocator, run_id, run);
        holder.order += 1;

        if (!queued) {
            if (holder.active.len == 0) {
                holder.active = run_id;
            } else if (self.runs.get(holder.active)) |previous| {
                if (previous.terminal) holder.active = run_id;
            } else {
                holder.active = run_id;
            }
        }
    }

    fn openResponse(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const session_id = memberString(payload, "session_id");
        if (member(payload, "recovery")) |recovery| {
            if (memberBool(recovery, "recovered")) {
                const created = try self.arena.allocator().create(Recovery);
                created.* = .{
                    .run = memberString(recovery, "previous_run_id"),
                    .gap = std.mem.eql(u8, memberString(recovery, "reason"), "replay_gap"),
                };
                const cursor = memberString(recovery, "resume_cursor");
                if (cursor.len != 0) {
                    if (std.fmt.parseUnsigned(u64, cursor, 10)) |parsed| {
                        created.cursor = parsed;
                        created.cursor_set = true;
                    } else |_| {}
                }
                try self.recoveries.put(self.allocator, session_id, created);
                try self.bootstrapRecoveredRuns(index, session_id, payload);
            }
        }
        try self.compoundOpen(index, envelope, payload, session_id);
    }

    fn compoundOpen(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, session_id: []const u8) !void {
        const request = self.requests.get(field(envelope, "in_reply_to")) orelse return;
        if (!request.carries_message) return;

        const listed = member(payload, "active_runs") orelse return;
        if (listed != .array) return;
        var found: ?std.json.Value = null;
        var count: usize = 0;
        for (listed.array.items) |entry| {
            if (self.runs.get(memberString(entry, "run_id")) != null) continue;
            found = entry;
            count += 1;
        }
        if (count != 1) return;
        const entry = found.?;
        const status = memberString(entry, "status");

        var synthesized: std.json.ObjectMap = .empty;
        const allocator = self.arena.allocator();
        try synthesized.put(allocator, "session_id", .{ .string = session_id });
        try synthesized.put(allocator, "accepted", .{ .bool = true });
        try synthesized.put(allocator, "run_id", .{ .string = memberString(entry, "run_id") });
        try synthesized.put(allocator, "status", .{ .string = status });
        try synthesized.put(allocator, "admission", .{ .string = admissionFor(status) });
        try synthesized.put(allocator, "effective_delivery", .{ .string = effectiveFor(status) });
        try self.admit(index, .{ .object = synthesized });
    }

    fn bootstrapRecoveredRuns(self: *Machine, index: usize, session_id: []const u8, payload: std.json.Value) !void {
        const recovery = self.recoveries.get(session_id);
        const holder = try self.sessionFor(session_id);
        var listed: usize = 0;
        if (member(payload, "active_runs")) |entries| {
            if (entries == .array) {
                for (entries.array.items) |entry| {
                    listed += 1;
                    const run_id = memberString(entry, "run_id");
                    if (run_id.len == 0 or self.runs.get(run_id) != null) continue;
                    const status = memberString(entry, "status");
                    const queued = std.mem.eql(u8, status, "queued") or
                        (std.mem.eql(u8, status, "cancelling") and cancellingHoldsItsPlace(entry));
                    try self.introduceRecovered(index, holder, run_id, session_id, .{
                        .started = !queued,
                        .next = resumeSequence(recovery, run_id, unsigned(entry, "as_of_sequence")),
                        .status = status,
                    });
                }
            }
        }
        const active = memberString(payload, "active_run_id");
        if (active.len != 0 and self.runs.get(active) == null) {
            try self.introduceRecovered(index, holder, active, session_id, .{
                .started = true,
                .next = resumeSequence(recovery, active, null),
                .status = "running",
            });
        }
    }

    const Recovered = struct {
        started: bool,
        next: u64,
        status: []const u8,
    };

    fn introduceRecovered(self: *Machine, index: usize, holder: *Session, run_id: []const u8, session_id: []const u8, shape: Recovered) !void {
        const run = try self.arena.allocator().create(Run);
        run.* = .{
            .id = run_id,
            .session = session_id,
            .admitted = true,
            .started = shape.started,
            .next = shape.next,
            .last_index = index,
            .recovered = true,
            .status = shape.status,
        };
        try self.runs.put(self.allocator, run_id, run);
        holder.order += 1;
    }

    fn stateDocument(self: *Machine, index: usize, payload: std.json.Value) !void {
        const session_id = memberString(payload, "session_id");
        const recovery = self.recoveries.get(session_id) orelse return;
        if (recovery.state_checked) return;
        recovery.state_checked = true;
        if (recovery.gap) return;
        const active = memberString(payload, "active_run_id");
        if (active.len == 0 or self.runs.get(active) != null) return;
        const holder = try self.sessionFor(session_id);
        try self.introduceRecovered(index, holder, active, session_id, .{
            .started = true,
            .next = resumeSequence(recovery, active, null),
            .status = "running",
        });
    }

    fn cancelResponse(self: *Machine, index: usize, payload: std.json.Value) !void {
        if (!memberBool(payload, "accepted")) return;
        const run_id = memberString(payload, "run_id");
        const run = self.runs.get(run_id);
        if (run == null or (run.?.terminal and !std.mem.eql(u8, run.?.terminal_type, "run.cancelled"))) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        if (!run.?.terminal) {
            run.?.cancel_accepted = true;
            run.?.status = "cancelling";
        }
    }

    fn runEvent(self: *Machine, index: usize, envelope: std.json.Value, declared: []const u8) !void {
        const run_id = field(envelope, "run_id");
        var run = self.runs.get(run_id);
        if (run == null) {
            try self.add(code_illegal_run_transition, index);
            const created = try self.arena.allocator().create(Run);
            created.* = .{ .id = run_id, .session = field(envelope, "session_id") };
            try self.runs.put(self.allocator, run_id, created);
            run = created;
        }
        const state = run.?;
        state.last_index = index;

        const sequence = unsigned(envelope, "sequence");
        if (self.recoveries.get(state.session)) |recovery| {
            if (!recovery.gap and !recovery.first_replay_seen and std.mem.eql(u8, run_id, recovery.run)) {
                recovery.first_replay_seen = true;
                if (recovery.cursor_set and (sequence == null or sequence.? != recovery.cursor + 1)) {
                    try self.add(code_sequence_gap, index);
                }
            }
        }

        if (sequence) |value| {
            if (value < state.next) {
                try self.add(code_sequence_regression, index);
            } else if (value > state.next) {
                try self.add(code_sequence_gap, index);
            }
            if (value >= state.next) state.next = value + 1;
        }

        if (state.terminal) {
            if (isTerminal(declared)) {
                if (std.mem.eql(u8, declared, "run.cancelled") and !state.cancel_accepted and !state.recovered) {
                    try self.add(code_illegal_run_transition, index);
                }
                try self.add(code_duplicate_run_terminal, index);
            } else {
                try self.add(code_event_after_terminal, index);
            }
            return;
        }

        if (std.mem.eql(u8, declared, "run.started")) {
            if (state.started) {
                try self.add(code_illegal_run_transition, index);
            } else {
                state.started = true;
                state.status = "running";
            }
            return;
        }

        if (!state.started and !preStartSettlement(declared)) {
            try self.add(code_missing_run_started, index);
        }

        if (std.mem.eql(u8, declared, "run.status.updated")) {
            const payload = member(envelope, "payload") orelse std.json.Value{ .null = {} };
            const next_status = memberString(payload, "status");
            if (!legalRunStatusTransition(state.status, next_status)) {
                try self.add(code_illegal_run_transition, index);
            } else {
                state.status = next_status;
            }
        }

        if (isTerminal(declared)) {
            if (std.mem.eql(u8, declared, "run.cancelled") and !state.cancel_accepted and !state.recovered) {
                try self.add(code_illegal_run_transition, index);
            }
            state.terminal = true;
            state.terminal_type = declared;
            if (std.mem.eql(u8, declared, "run.completed")) state.status = "completed";
            if (std.mem.eql(u8, declared, "run.failed")) state.status = "failed";
            if (std.mem.eql(u8, declared, "run.cancelled")) state.status = "cancelled";
            if (self.sessions.get(state.session)) |holder| {
                if (std.mem.eql(u8, holder.active, state.id)) holder.active = "";
            }
        }
    }

    pub fn close(self: *Machine) !void {
        for (self.runs.values()) |run| {
            if (run.admitted and !run.started and !run.terminal) {
                try self.add(code_missing_run_started, run.last_index);
            }
            if (run.admitted and !run.terminal) {
                try self.add(
                    if (run.cancel_accepted) code_cancel_not_settled else code_missing_run_terminal,
                    run.last_index,
                );
            }
        }
    }
};

fn cancellingHoldsItsPlace(entry: std.json.Value) bool {
    return member(entry, "as_of_sequence") == null or member(entry, "queue_position") != null;
}

fn resumeSequence(recovery: ?*Recovery, run_id: []const u8, as_of: ?u64) u64 {
    if (as_of) |value| return value + 1;
    if (recovery) |found| {
        if (!found.gap and found.cursor_set and (found.run.len == 0 or std.mem.eql(u8, found.run, run_id))) {
            return found.cursor + 1;
        }
    }
    return 1;
}

fn admissionFor(status: []const u8) []const u8 {
    if (std.mem.eql(u8, status, "queued")) return "queued";
    return "started";
}

fn effectiveFor(status: []const u8) []const u8 {
    if (std.mem.eql(u8, status, "queued")) return "queue";
    return "start";
}

fn codesOf(allocator: std.mem.Allocator, trace: []const u8) ![]const []const u8 {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, trace, .{});
    defer parsed.deinit();

    var machine = Machine.init(allocator);
    defer machine.deinit();
    for (parsed.value.array.items, 0..) |envelope, index| try machine.apply(index, envelope);
    try machine.close();

    var out = std.ArrayList([]const u8).empty;
    for (machine.diagnostics.items) |diagnostic| try out.append(allocator, diagnostic.code);
    return out.toOwnedSlice(allocator);
}

fn expectCodes(trace: []const u8, want: []const []const u8) !void {
    const allocator = std.testing.allocator;
    const got = try codesOf(allocator, trace);
    defer allocator.free(got);
    try std.testing.expectEqual(want.len, got.len);
    for (want, got) |expected, actual| try std.testing.expectEqualStrings(expected, actual);
}

const admitted =
    \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
    \\{"session_id":"s","accepted":true,"run_id":"run","admission":"started",
    \\"effective_delivery":"start","status":"running"}}
;

test "an admitted run that starts and completes raises nothing" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"content.delta","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"run.completed","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{});
}

test "the sequence must be contiguous in both directions" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"content.delta","id":"e2","run_id":"run","session_id":"s","sequence":4,"payload":{}},
        \\{"type":"run.completed","id":"e3","run_id":"run","session_id":"s","sequence":5,"payload":{}}]
    , &.{"sequence_gap"});

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"content.delta","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"content.delta","id":"e3","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"run.completed","id":"e4","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{"sequence_regression"});
}

test "a run settles once, and nothing follows the settlement" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"run.completed","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{"duplicate_run_terminal"});

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"content.delta","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{"event_after_terminal"});
}

test "an admitted run owes a start and a terminal" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\]
    , &.{ "missing_run_started", "missing_run_terminal" });

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}}]
    , &.{"missing_run_terminal"});
}

test "a cancellation settles the run it was accepted for" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.cancelled","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}}]
    , &.{"illegal_run_transition"});

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.cancel.request","id":"c1","payload":{"session_id":"s","run_id":"run"}},
        \\{"type":"run.cancel.response","id":"c2","in_reply_to":"c1","payload":{"session_id":"s","run_id":"run","accepted":true}}]
    , &.{"cancel_not_settled"});
}

test "an admission must name a run and resolve to one shape" {
    try expectCodes(
        \\[{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"run","admission":"started",
        \\"effective_delivery":"queue","status":"running"}}]
    , &.{"illegal_run_transition"});

    try expectCodes(
        \\[{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":false,"error":{"code":"run_active"}}}]
    , &.{"illegal_run_transition"});
}

test "a run event with no admission behind it is a transition the trace never took" {
    try expectCodes(
        \\[{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}}]
    , &.{"illegal_run_transition"});
}

test "an envelope id is spent once" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e1","run_id":"run","session_id":"s","sequence":2,"payload":{}}]
    , &.{ "duplicate_envelope_id", "missing_run_terminal" });
}

test "a status update may only move where the lifecycle allows" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.status.updated","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{"status":"waiting_for_input"}},
        \\{"type":"run.status.updated","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{"status":"queued"}},
        \\{"type":"run.completed","id":"e4","run_id":"run","session_id":"s","sequence":4,"payload":{}}]
    , &.{"illegal_run_transition"});
}
