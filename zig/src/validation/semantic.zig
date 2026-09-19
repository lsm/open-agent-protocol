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
pub const code_queue_order_violation = "queue_order_violation";
pub const code_queue_limit_exceeded = "queue_limit_exceeded";
pub const code_undisclosed_queue_limit = "undisclosed_queue_limit";

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
    code_queue_order_violation,
    code_queue_limit_exceeded,
    code_undisclosed_queue_limit,
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
    admitted_queued: bool = false,
    order: usize = 0,
    status: []const u8 = "",
};

const Session = struct {
    active: []const u8 = "",
    order: std.ArrayList([]const u8) = .empty,
};

const Window = struct {
    request: []const u8,
    session: []const u8,
    delivery: []const u8,
    offered: bool = false,
    busy_at_request: bool = false,
    busy_ever: bool = false,
    started_ever: bool = false,
    reached_strict: bool = false,
    reached_loose: bool = false,
    mutation: bool = false,
    closed: bool = false,
};

const Limits = struct {
    max_active: ?i64 = null,
    max_queued: ?i64 = null,
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
    features: std.StringArrayHashMapUnmanaged([]const u8) = .empty,
    modes: std.StringArrayHashMapUnmanaged([]const u8) = .empty,
    current_capability: []const u8 = "",
    capabilities_stale: bool = false,
    limits: ?Limits = null,
    windows: std.StringArrayHashMapUnmanaged(*Window) = .empty,

    pub fn init(allocator: std.mem.Allocator) Machine {
        return .{ .allocator = allocator, .arena = std.heap.ArenaAllocator.init(allocator) };
    }

    pub fn deinit(self: *Machine) void {
        self.diagnostics.deinit(self.allocator);
        self.ids.deinit(self.allocator);
        self.runs.deinit(self.allocator);
        self.recoveries.deinit(self.allocator);
        self.requests.deinit(self.allocator);
        self.features.deinit(self.allocator);
        self.modes.deinit(self.allocator);
        self.windows.deinit(self.allocator);
        for (self.sessions.values()) |holder| holder.order.deinit(self.allocator);
        self.sessions.deinit(self.allocator);
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

        if (std.mem.eql(u8, declared, "capabilities.response")) {
            try self.capabilitiesResponse(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "capabilities.updated")) {
            self.current_capability = field(envelope, "capability_revision");
            self.capabilities_stale = true;
            self.limits = null;
            return;
        }
        if (std.mem.eql(u8, declared, "session.message.submit.request")) {
            try self.openSubmitWindow(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "error.response")) {
            try self.settleQueueRefusal(index, envelope, payload);
            try self.closeSubmitWindow(field(envelope, "in_reply_to"));
            return;
        }
        if (std.mem.eql(u8, declared, "session.message.submit.response")) {
            try self.admit(index, envelope, payload);
            try self.closeSubmitWindow(field(envelope, "in_reply_to"));
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

    fn capabilitiesResponse(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        self.current_capability = field(envelope, "capability_revision");
        self.capabilities_stale = false;
        self.features.clearRetainingCapacity();
        self.modes.clearRetainingCapacity();
        try self.collectFeatures(payload);
        self.limits = readLimits(member(payload, "limits"));
        try self.checkQueueLimits(index);
    }

    fn collectFeatures(self: *Machine, payload: std.json.Value) !void {
        if (member(payload, "layers")) |layers| {
            if (layers == .object) {
                var names = std.ArrayList([]const u8).empty;
                defer names.deinit(self.allocator);
                var layer = layers.object.iterator();
                while (layer.next()) |entry| try names.append(self.allocator, entry.key_ptr.*);
                std.mem.sort([]const u8, names.items, {}, lessThanName);
                var at = names.items.len;
                while (at > 0) {
                    at -= 1;
                    try self.absorbFeatures(member(layers.object.get(names.items[at]).?, "features"));
                }
            }
        }
        try self.absorbFeatures(member(payload, "features"));
    }

    fn absorbFeatures(self: *Machine, declared: ?std.json.Value) !void {
        const features = declared orelse return;
        if (features != .object) return;
        var it = features.object.iterator();
        while (it.next()) |entry| {
            const level = memberString(entry.value_ptr.*, "level");
            if (level.len == 0) continue;
            try self.features.put(self.allocator, entry.key_ptr.*, level);
            const mode = memberString(entry.value_ptr.*, "mode");
            if (mode.len != 0) try self.modes.put(self.allocator, entry.key_ptr.*, mode);
        }
    }

    fn checkQueueLimits(self: *Machine, index: usize) !void {
        if (!self.queueOffered()) return;
        const limits = self.limits orelse Limits{};
        const queued = limits.max_queued orelse 0;
        if (queued < 1) {
            try self.add(code_undisclosed_queue_limit, index);
            return;
        }
        if (limits.max_active) |active| {
            if (active < queued + 1) try self.add(code_undisclosed_queue_limit, index);
        }
    }

    fn advertisedLevel(self: *const Machine, key: []const u8) ?[]const u8 {
        if (self.current_capability.len == 0 or self.capabilities_stale) return null;
        return self.features.get(key) orelse "unavailable";
    }

    fn queueOffered(self: *const Machine) bool {
        const level = self.advertisedLevel(feature_delivery_queue) orelse return false;
        return affirmative(level);
    }

    const Counts = struct { active: usize = 0, queued: usize = 0, started: usize = 0 };

    fn queueCounts(self: *const Machine, session: []const u8) Counts {
        const holder = self.sessions.get(session) orelse return .{};
        var counts = Counts{};
        for (holder.order.items) |id| {
            const run = self.runs.get(id) orelse continue;
            if (run.terminal) continue;
            counts.active += 1;
            if (run.admitted_queued and !run.started) counts.queued += 1 else counts.started += 1;
        }
        return counts;
    }

    fn exceeds(self: *const Machine, counts: Counts, outstanding: usize) bool {
        const limits = self.limits orelse return false;
        if (limits.max_active) |bound| {
            if (@as(i64, @intCast(counts.active + outstanding + 1)) > bound) return true;
        }
        if (limits.max_queued) |bound| {
            if (@as(i64, @intCast(counts.queued + outstanding + 1)) > bound) return true;
        }
        return false;
    }

    fn openWindows(self: *const Machine, session: []const u8, out: *std.ArrayList(*Window)) !void {
        for (self.windows.values()) |window| {
            if (window.closed) continue;
            if (!std.mem.eql(u8, window.session, session)) continue;
            try out.append(self.allocator, window);
        }
    }

    fn refreshQueueWindows(self: *Machine, session: []const u8) !void {
        var open = std.ArrayList(*Window).empty;
        defer open.deinit(self.allocator);
        try self.openWindows(session, &open);
        if (open.items.len == 0) return;
        const counts = self.queueCounts(session);

        var outstanding: usize = 0;
        for (open.items) |window| {
            if (!self.answered(window.request)) outstanding += 1;
        }
        for (open.items) |window| {
            if (counts.active > 0) window.busy_ever = true;
            if (counts.started > 0) window.started_ever = true;
            if (self.exceeds(counts, 0)) window.reached_strict = true;
            var others = outstanding;
            if (!self.answered(window.request)) others -= 1;
            if (self.exceeds(counts, others)) window.reached_loose = true;
        }
    }

    fn answered(self: *const Machine, request: []const u8) bool {
        const found = self.requests.get(request) orelse return false;
        return found.responded;
    }

    fn openSubmitWindow(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        _ = index;
        const session = memberString(payload, "session_id");
        const counts = self.queueCounts(session);
        var open = std.ArrayList(*Window).empty;
        defer open.deinit(self.allocator);
        try self.openWindows(session, &open);

        const window = try self.arena.allocator().create(Window);
        window.* = .{
            .request = field(envelope, "id"),
            .session = session,
            .delivery = memberString(payload, "delivery"),
            .busy_at_request = counts.active > 0,
            .busy_ever = counts.active > 0,
            .started_ever = counts.started > 0,
            .offered = self.queueOffered(),
            .mutation = member(payload, "model_id") != null and
                std.mem.eql(u8, self.modes.get(feature_model_selection) orelse "", "session_mutation"),
        };
        window.reached_strict = self.exceeds(counts, 0);
        window.reached_loose = self.exceeds(counts, open.items.len);
        try self.windows.put(self.allocator, window.request, window);
    }

    fn closeSubmitWindow(self: *Machine, request: []const u8) !void {
        const window = self.windows.get(request) orelse return;
        if (window.closed) return;
        window.closed = true;
        try self.refreshQueueWindows(window.session);
    }

    fn settleQueueRefusal(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const window = self.windows.get(field(envelope, "in_reply_to")) orelse return;
        const raised = memberString(member(payload, "error") orelse std.json.Value{ .null = {} }, "code");
        switch (stateCondition(window)) {
            .busy_session => {
                if (!std.mem.eql(u8, raised, error_run_active)) {
                    try self.add(code_illegal_run_transition, index);
                }
            },
            .queue_limit => {
                if (window.reached_strict) {
                    if (!std.mem.eql(u8, raised, error_run_active)) {
                        try self.add(code_queue_limit_exceeded, index);
                    }
                } else if (std.mem.eql(u8, raised, error_run_active) and !window.reached_loose and
                    !(window.mutation and window.started_ever))
                {
                    try self.add(code_queue_limit_exceeded, index);
                }
            },
            .none => {},
        }
    }

    fn queueOverlap(self: *Machine, index: usize, payload: std.json.Value) !bool {
        const counts = self.queueCounts(memberString(payload, "session_id"));
        if (counts.active == 0) return false;
        if (std.mem.eql(u8, memberString(payload, "admission"), "queued") and self.queueOffered()) return false;
        try self.add(code_illegal_run_transition, index);
        return true;
    }

    fn queueAdmission(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const window = self.windows.get(field(envelope, "in_reply_to"));
        var requested = memberString(payload, "requested_delivery");
        if (window) |open| {
            if (open.delivery.len != 0) requested = open.delivery;
        }
        const queued = std.mem.eql(u8, memberString(payload, "admission"), "queued");
        if (std.mem.eql(u8, requested, "queue") and !queued) {
            try self.add(code_illegal_run_transition, index);
        } else if (std.mem.eql(u8, requested, "auto") and queued and
            window != null and window.?.busy_at_request and
            !std.mem.eql(u8, memberString(payload, "delivery_resolution"), resolution_session_busy))
        {
            try self.add(code_illegal_run_transition, index);
        }
        if (!queued) return;

        const limits = self.limits orelse return;
        const counts = self.queueCounts(memberString(payload, "session_id"));
        if (limits.max_active) |bound| {
            if (@as(i64, @intCast(counts.active + 1)) > bound) {
                try self.add(code_queue_limit_exceeded, index);
                return;
            }
        }
        if (limits.max_queued) |bound| {
            if (@as(i64, @intCast(counts.queued + 1)) > bound) {
                try self.add(code_queue_limit_exceeded, index);
            }
        }
    }

    fn checkQueueOrder(self: *Machine, index: usize, envelope: std.json.Value, run: *Run, declared: []const u8) !void {
        if (!run.admitted or member(envelope, "sequence") == null) return;
        if (isTerminal(declared) and !run.started) return;
        const holder = self.sessions.get(run.session) orelse return;
        for (holder.order.items) |id| {
            const earlier = self.runs.get(id) orelse continue;
            if (std.mem.eql(u8, earlier.id, run.id) or earlier.order >= run.order) continue;
            if (!earlier.terminal) {
                try self.add(code_queue_order_violation, index);
                return;
            }
        }
    }

    fn admit(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
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

        const overlap = try self.queueOverlap(index, payload);
        if (!overlap) try self.queueAdmission(index, envelope, payload);

        const run = try self.arena.allocator().create(Run);
        run.* = .{
            .id = run_id,
            .session = session_id,
            .admitted = true,
            .last_index = index,
            .admitted_queued = queued,
            .order = holder.order.items.len,
            .status = "queued",
        };
        try self.runs.put(self.allocator, run_id, run);
        try holder.order.append(self.allocator, run_id);

        try self.refreshQueueWindows(session_id);
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
        try self.admit(index, envelope, .{ .object = synthesized });
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
                        .queued = queued,
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
        queued: bool = false,
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
            .admitted_queued = shape.queued,
            .order = holder.order.items.len,
            .status = shape.status,
        };
        try self.runs.put(self.allocator, run_id, run);
        try holder.order.append(self.allocator, run_id);
        try self.refreshQueueWindows(session_id);
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
        try self.checkQueueOrder(index, envelope, state, declared);

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
                if (self.sessions.get(state.session)) |holder| holder.active = state.id;
                try self.refreshQueueWindows(state.session);
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
            try self.refreshQueueWindows(state.session);
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

pub const feature_delivery_queue = "session.message.delivery.queue";
const feature_model_selection = "run.model_selection";
const error_run_active = "run_active";
const resolution_session_busy = "session_busy";

fn affirmative(level: []const u8) bool {
    return std.mem.eql(u8, level, "native") or
        std.mem.eql(u8, level, "emulated") or
        std.mem.eql(u8, level, "degraded");
}

fn readLimits(declared: ?std.json.Value) ?Limits {
    const limits = declared orelse return null;
    if (limits != .object) return null;
    return .{
        .max_active = boundOf(limits, "max_active_runs_per_session"),
        .max_queued = boundOf(limits, "max_queued_runs_per_session"),
    };
}

fn boundOf(limits: std.json.Value, name: []const u8) ?i64 {
    const value = member(limits, name) orelse return null;
    return switch (value) {
        .integer => |n| n,
        else => null,
    };
}

const Condition = enum { none, busy_session, queue_limit };

fn stateCondition(window: *const Window) Condition {
    if (window.delivery.len != 0 and
        !std.mem.eql(u8, window.delivery, "auto") and
        !std.mem.eql(u8, window.delivery, "queue")) return .none;
    const explicit = std.mem.eql(u8, window.delivery, "queue");
    if (window.offered and (explicit or window.busy_ever)) return .queue_limit;
    if (!window.offered and !explicit and window.busy_ever) return .busy_session;
    return .none;
}

fn lessThanName(_: void, a: []const u8, b: []const u8) bool {
    return std.mem.order(u8, a, b) == .lt;
}

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
    const matched = want.len == got.len and blk: {
        for (want, got) |expected, actual| {
            if (!std.mem.eql(u8, expected, actual)) break :blk false;
        }
        break :blk true;
    };
    if (!matched) {
        std.debug.print("\nwant:", .{});
        for (want) |code| std.debug.print(" {s}", .{code});
        std.debug.print("\ngot: ", .{});
        for (got) |code| std.debug.print(" {s}", .{code});
        std.debug.print("\n", .{});
        return error.DiagnosticsDiffer;
    }
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

const queue_capabilities =
    \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
    \\{"session.message.delivery.queue":{"level":"native"}},
    \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}}
;

test "a second run may only join a busy session as a reservation the endpoint offers" {
    try expectCodes(
        \\[{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q2","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"started","effective_delivery":"start","status":"running"}}]
    , &.{ "illegal_run_transition", "missing_run_terminal", "missing_run_started", "missing_run_terminal" });
}

test "an explicit queue request is admitted as a reservation or not at all" {
    try expectCodes(
        \\[{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"queue"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}}]
    , &.{ "illegal_run_transition", "missing_run_started", "missing_run_terminal" });
}

test "an auto submission a busy session reserved must say what produced the reservation" {
    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\,
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q0","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "illegal_run_transition", "missing_run_terminal", "missing_run_started", "missing_run_terminal" });
}

test "a later admission may not publish while an earlier one is still open" {
    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\,
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q2","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"queued","effective_delivery":"queue","status":"queued"}},
        \\{"type":"run.started","id":"e2","run_id":"b","session_id":"s","sequence":1,"payload":{}}]
    , &.{ "queue_order_violation", "missing_run_terminal", "missing_run_terminal" });
}

test "a queue is advertised with a bound a submission could reach" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}}}}]
    , &.{"undisclosed_queue_limit"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":2,"max_queued_runs_per_session":2}}}]
    , &.{"undisclosed_queue_limit"});

    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\]
    , &.{});
}

test "a reservation past the disclosed bound is the bound exceeded" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":9,"max_queued_runs_per_session":1}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q2","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "queue_limit_exceeded", "missing_run_started", "missing_run_terminal", "missing_run_started", "missing_run_terminal" });
}

test "a refusal at a bound the session had not reached reports a bound that does not exist" {
    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.request","id":"q2","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q2","payload":{"error":{"code":"run_active"}}}]
    , &.{ "queue_limit_exceeded", "missing_run_terminal" });
}

test "an outstanding sibling moves the bound a refusal is judged against" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}},
        \\{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.request","id":"q2","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.request","id":"q3","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q2","payload":{"error":{"code":"run_active"}}}]
    , &.{ "missing_run_started", "missing_run_terminal" });
}
