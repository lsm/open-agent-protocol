const std = @import("std");
const harness_pins = @import("harness_pins");
const native = @import("native");
const gomarshal = @import("gomarshal");

pub const capability_revision = harness_pins.opencode_capability_revision;
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const endpoint_id = "opencode.server";
pub const pinned_commit = harness_pins.opencode_opencode_commit;
pub const cost_extension = "io.github.anomalyco.opencode.cost";
pub const max_active_runs = 2;
pub const max_queued_runs = 1;

pub const Error = error{
    InvalidSubmission,
    SessionClosed,
    RunNotFound,
    RunActive,
    RunTerminal,
    Unsupported,
    CancellationAmbiguous,
    DegradedWithoutOptIn,
} || std.mem.Allocator.Error;

pub const Status = enum { queued, running, cancelling, completed, failed, cancelled };

pub const Failure = struct { message: []const u8, api: bool = false };

pub const PromptOutcome = union(enum) { admitted: native.Admitted, failed: Failure };
pub const InterruptOutcome = union(enum) { interrupted: bool, failed: Failure };

pub const Native = struct {
    context: *anyopaque,
    prompt: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!PromptOutcome,
    interrupt: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8) std.mem.Allocator.Error!InterruptOutcome,
    cancel_inbox: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8, inbox: []const u8) std.mem.Allocator.Error!?Failure,
    reply_permission: ?*const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8, request_id: []const u8, decision: []const u8, message: []const u8) std.mem.Allocator.Error!?Failure = null,
};

pub const permission_choices = [_][3][]const u8{
    .{ "once", "Allow once", "allow this call" },
    .{ "always", "Always allow", "allow it and save the rule OpenCode offers" },
    .{ "reject", "Reject", "decline the call; without a reason OpenCode ends the execution" },
};

pub const ResolveError = error{ InteractionNotFound, InteractionResolved, InvalidResolution, WrongResponder, SessionClosed, ReplyFailed } || Error;

pub const Options = struct {
    session_id: []const u8 = "session",
    native_id: []const u8,
    participant: []const u8 = "",
    model: []const u8 = "",
    message_prefix: []const u8 = "msg_oap",
    revision: []const u8 = capability_revision,
    counter: ?*u64 = null,
    now_ms: ?*const fn () i64 = null,
};

pub const Admission = struct {
    session_id: []const u8,
    submission_id: []const u8,
    requested_delivery: []const u8,
    effective_delivery: []const u8 = "queue",
    delivery_resolution: []const u8 = "",
    admission: []const u8 = "queued",
    run_id: []const u8,
    status: Status = .queued,
    model_id: []const u8 = "",
    message_ids: []const []const u8 = &.{},
};

pub const CancelResponse = struct { session_id: []const u8, run_id: []const u8, status: Status };

const Part = struct { reasoning: bool, text: []const u8 };

pub const Settled = struct {
    run_id: []const u8,
    sequence: u64,
};

pub const Run = struct {
    id: []const u8,
    message_id: []const u8,
    native_id: []const u8 = "",
    status: Status = .queued,
    next: u64 = 1,
    terminal: bool = false,
    prompted: bool = false,
    promotion_seen: bool = false,
    holding: bool = false,
    held: std.ArrayList(std.json.Value) = .empty,
    start_published: bool = false,
    published_seq: u64 = 0,
    queued_admission: bool = true,
    cancel_requested: bool = false,
    parts: std.ArrayList(Part) = .empty,
    streamed: std.StringHashMapUnmanaged(std.ArrayList(u8)) = .empty,
    ended_parts: [2]std.StringHashMapUnmanaged(void) = .{ .empty, .empty },
    steps: [2]std.StringHashMapUnmanaged(void) = .{ .empty, .empty },
    input_tokens: u64 = 0,
    output_tokens: u64 = 0,
    total_tokens: u64 = 0,
    cost: f64 = 0,
    last_finish: []const u8 = "",
    failure: ?[]const u8 = null,
    declined: bool = false,
};

const Tool = struct {
    run: *Run,
    native_id: []const u8,
    id: []const u8,
    name: []const u8,
    args: std.json.Value,
    progress: ?std.json.Value = null,
    result: ?std.json.Value = null,
    terminal: bool = false,
    started_event: []const u8 = "",
};

const Pending = struct { native_id: []const u8, run: *Run };

const Gate = struct {
    id: []const u8,
    native_id: []const u8,
    run: *Run,
    tool: *Tool,
    requested: []const u8 = "",
    resolved: bool = false,
};

const ToolFields = struct {
    arguments: bool = false,
    progress: bool = false,
    result: bool = false,
    failure: ?[2][]const u8 = null,
};

fn str(text: []const u8) std.json.Value {
    return .{ .string = text };
}

fn int(value: i64) std.json.Value {
    return .{ .integer = value };
}

fn unsigned(arena: std.mem.Allocator, value: u64) std.mem.Allocator.Error!std.json.Value {
    if (value <= std.math.maxInt(i64)) return .{ .integer = @intCast(value) };
    return .{ .number_string = try std.fmt.allocPrint(arena, "{d}", .{value}) };
}

pub fn tokenCount(value: f64) u64 {
    if (!(value > 0)) return 0;
    if (value >= 18446744073709551616.0) return std.math.maxInt(u64);
    return @intFromFloat(value);
}

pub fn normalizeModel(arena: std.mem.Allocator, model: ?native.ModelRef) std.mem.Allocator.Error![]const u8 {
    const ref = model orelse return "";
    if (ref.id.len == 0) return "";
    if (ref.provider_id.len > 0) return std.mem.concat(arena, u8, &.{ ref.provider_id, "/", ref.id });
    return ref.id;
}

pub const ReconcileTarget = struct { oldest: []const u8 = "", delivered: []const u8 = "" };

pub fn recordSettled(records: []const std.json.Value) bool {
    var settled = true;
    for (records) |record| {
        if (record != .object) continue;
        const kind = recordText(record, "type");
        if (std.mem.eql(u8, kind, "user")) settled = false;
        if (std.mem.eql(u8, kind, "idle")) settled = true;
    }
    return settled;
}

fn recordText(record: std.json.Value, name: []const u8) []const u8 {
    const value = record.object.get(name) orelse return "";
    return if (value == .string) value.string else "";
}

fn recordNumber(record: std.json.Value, name: []const u8) f64 {
    const value = record.object.get(name) orelse return 0;
    return switch (value) {
        .integer => |number| @floatFromInt(number),
        .float => |number| number,
        .number_string => |text| std.fmt.parseFloat(f64, text) catch 0,
        else => 0,
    };
}

fn completedAt(record: std.json.Value) i64 {
    const time = record.object.get("time") orelse return 0;
    if (time != .object) return 0;
    const completed = time.object.get("completed") orelse return 0;
    return switch (completed) {
        .integer => |number| number,
        .float => |number| if (number > 0) 1 else 0,
        .number_string => |text| if (std.fmt.parseFloat(f64, text) catch 0 > 0) 1 else 0,
        else => 0,
    };
}

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    client: Native,
    ids: u64 = 0,
    clock: i64 = 0,
    unusable: bool = false,
    suppressed: bool = false,
    active: ?*Run = null,
    reserved: ?*Run = null,
    runs: std.ArrayList(*Run) = .empty,
    pending: std.ArrayList(Pending) = .empty,
    tools: std.ArrayList(*Tool) = .empty,
    gates: std.ArrayList(*Gate) = .empty,
    reply_failure: []const u8 = "",
    reduced: std.AutoHashMapUnmanaged(i64, void) = .empty,
    catalog: std.ArrayList([]const u8) = .empty,
    tool_names: std.StringArrayHashMapUnmanaged([]const u8) = .empty,
    envelopes: std.ArrayList(std.json.Value) = .empty,
    last_seq: i64 = 0,
    settled: std.ArrayList(Settled) = .empty,
    reconciled: std.StringHashMapUnmanaged(void) = .empty,
    replaying: bool = false,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options, client: Native) Reducer {
        return .{ .arena = arena, .options = options, .client = client };
    }

    pub fn open(self: *Reducer) Error!void {
        if (self.options.session_id.len == 0) self.options.session_id = try self.nextID("session");
        _ = self.now();
        try self.observeModel(self.options.model);
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        if (self.options.now_ms) |clock| return clock();
        self.clock += 1;
        return self.clock;
    }

    fn tick(self: *Reducer) u64 {
        const counter = self.options.counter orelse &self.ids;
        counter.* += 1;
        return counter.*;
    }

    fn nextID(self: *Reducer, kind: []const u8) std.mem.Allocator.Error![]const u8 {
        return std.fmt.allocPrint(self.allocator(), "{s}-{d}", .{ kind, self.tick() });
    }

    fn nextMessageID(self: *Reducer) std.mem.Allocator.Error![]const u8 {
        return std.fmt.allocPrint(self.allocator(), "{s}{d:0>16}", .{ self.options.message_prefix, self.tick() });
    }

    fn put(self: *Reducer, map: *std.json.ObjectMap, key: []const u8, value: std.json.Value) std.mem.Allocator.Error!void {
        try map.put(self.allocator(), key, value);
    }

    fn scoped(self: *Reducer, run: *Run) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload: std.json.ObjectMap = .empty;
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        return payload;
    }

    fn published(run: ?*Run) bool {
        const candidate = run orelse return false;
        return !candidate.terminal or candidate.holding;
    }

    fn find(self: *Reducer, run_id: []const u8) ?*Run {
        for (self.runs.items) |run| {
            if (std.mem.eql(u8, run.id, run_id)) return run;
        }
        return null;
    }

    pub fn runStatus(self: *Reducer, run_id: []const u8) ?Status {
        const run = self.find(run_id) orelse return null;
        return run.status;
    }

    pub fn submit(self: *Reducer, session_id: []const u8, text: []const u8, delivery: []const u8) Error!Admission {
        const queue = std.mem.eql(u8, delivery, "queue");
        if (!queue and delivery.len > 0 and !std.mem.eql(u8, delivery, "auto")) return error.Unsupported;
        if (session_id.len == 0) return error.InvalidSubmission;
        if (self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, session_id, self.options.session_id)) return error.RunNotFound;

        var live: usize = 0;
        var queued: usize = 0;
        for ([_]?*Run{ self.active, self.reserved }) |candidate| {
            if (!published(candidate)) continue;
            live += 1;
            if (candidate.?.queued_admission and !candidate.?.start_published) queued += 1;
        }
        if (live >= max_active_runs or queued >= max_queued_runs or published(self.reserved)) return error.RunActive;
        const behind = live > 0;

        const run_id = try self.nextID("run");
        const message_id = try self.nextID("message");
        const run = try self.allocator().create(Run);
        run.* = .{ .id = run_id, .message_id = message_id };
        self.reserved = run;
        try self.runs.append(self.allocator(), run);
        _ = self.now();

        var admission = Admission{
            .session_id = self.options.session_id,
            .submission_id = try self.nextID("submission"),
            .requested_delivery = if (queue) "queue" else "auto",
            .run_id = run.id,
            .model_id = self.options.model,
        };
        if (behind) admission.delivery_resolution = "session_busy";

        const native_message = try self.nextMessageID();
        if (!native.validMessageID(native_message)) {
            try self.abandon(run, "opencode_invalid_message_id", "ID generator must produce a msg_-prefixed identity for kind opencode-message", "inferred");
            return admission;
        }
        run.native_id = native_message;
        try self.pending.append(self.allocator(), .{ .native_id = native_message, .run = run });

        const request = native.PromptRequest{ .id = native_message, .text = text, .delivery = if (queue or behind) "queue" else "steer" };
        const admitted = switch (try self.client.prompt(self.client.context, self.allocator(), self.options.native_id, request)) {
            .failed => |failure| {
                _ = self.takePending(native_message);
                try self.abandon(run, "opencode_admission_ambiguous", failure.message, settledBy(failure));
                return admission;
            },
            .admitted => |receipt| receipt,
        };
        if (!std.mem.eql(u8, admitted.id, native_message)) {
            _ = self.takePending(native_message);
            const message = try std.fmt.allocPrint(self.allocator(), "server admitted {s} for request {s}", .{ admitted.id, native_message });
            try self.abandon(run, "opencode_foreign_admission", message, "");
            return admission;
        }
        admission.message_ids = try self.allocator().dupe([]const u8, &.{admitted.id});
        if (!behind and !queue) {
            admission.admission = "started";
            admission.effective_delivery = "start";
            admission.status = .running;
            run.status = .running;
            run.queued_admission = false;
            if (self.reserved == run) {
                self.reserved = null;
                self.active = run;
            }
        }
        return admission;
    }

    fn takePending(self: *Reducer, native_id: []const u8) ?*Run {
        for (self.pending.items, 0..) |entry, index| {
            if (!std.mem.eql(u8, entry.native_id, native_id)) continue;
            _ = self.pending.orderedRemove(index);
            return entry.run;
        }
        return null;
    }

    fn settledBy(failure: Failure) []const u8 {
        return if (failure.api) "" else "inferred";
    }

    fn reductionTarget(self: *Reducer) ?*Run {
        if (self.suppressed) return null;
        if (self.reserved) |reserved| {
            if (reserved.promotion_seen and !reserved.terminal) return reserved;
        }
        return self.active;
    }

    pub fn observe(self: *Reducer, event: native.Event) Error!void {
        return self.handle(event);
    }

    fn decodeFor(
        self: *Reducer,
        comptime T: type,
        comptime decode: fn (std.mem.Allocator, native.Event, *native.Diagnostic) native.Error!T,
        event: native.Event,
        run: ?*Run,
        code: []const u8,
    ) Error!?T {
        var diag = native.Diagnostic{};
        return decode(self.allocator(), event, &diag) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.InvalidWire, error.UnsupportedType => {
                if (run) |target| try self.failRun(target, code, diag.message);
                return null;
            },
        };
    }

    fn handle(self: *Reducer, event: native.Event) Error!void {
        if (!std.mem.eql(u8, event.session_id, self.options.native_id)) {
            return self.abandon(null, "opencode_foreign_session", "event belongs to another session", "");
        }
        if (event.durable) |position| {
            const seen = try self.reduced.getOrPut(self.allocator(), position.seq);
            if (seen.found_existing) return;
            if (position.seq > self.last_seq) self.last_seq = position.seq;
        }
        const run = self.reductionTarget();
        const kind = event.kind orelse return;
        switch (kind) {
            .inbox_enqueued => _ = try self.decodeFor(native.InboxData, native.decodeInboxEnqueued, event, run, "opencode_invalid_inbox_event"),
            .inbox_delivered => {
                const data = (try self.decodeFor(native.InboxData, native.decodeInboxRef, event, run, "opencode_invalid_inbox_event")) orelse return;
                const repeated = self.reconciled.contains(data.inbox_id);
                if (self.replaying) try self.reconciled.put(self.allocator(), data.inbox_id, {});
                if (!repeated) try self.delivered(data.inbox_id);
            },
            .inbox_cancelled => {
                const data = (try self.decodeFor(native.InboxData, native.decodeInboxRef, event, run, "opencode_invalid_inbox_event")) orelse return;
                const pending = self.takePending(data.inbox_id) orelse return;
                if (pending.terminal) return;
                var payload = try self.scoped(pending);
                try self.put(&payload, "reason", str("OpenCode cancelled the input before delivering it"));
                _ = try self.emitWith(pending, "run.cancelled", payload, true, try self.reportedCost(pending));
            },
            .inbox_delivery_changed => _ = try self.decodeFor(native.InboxData, native.decodeInboxDeliveryChanged, event, run, "opencode_invalid_inbox_event"),
            .execution_started => _ = try self.decodeFor(void, native.decodeExecution, event, run, "opencode_invalid_execution_event"),
            .execution_succeeded => {
                (try self.decodeFor(void, native.decodeExecution, event, run, "opencode_invalid_execution_event")) orelse return;
                const target = run orelse return;
                if (target.prompted) try self.settleRun(target);
            },
            .execution_failed => {
                const data = (try self.decodeFor(native.ExecutionFailedData, native.decodeExecutionFailed, event, run, "opencode_invalid_execution_event")) orelse return;
                const target = run orelse return;
                if (!target.prompted) return;
                if (target.failure != null) return self.settleRun(target);
                try self.failRun(target, "opencode_execution_failed", data.failure.message);
            },
            .execution_interrupted => {
                const data = (try self.decodeFor(native.ExecutionInterruptedData, native.decodeExecutionInterrupted, event, run, "opencode_invalid_execution_event")) orelse return;
                const target = run orelse return;
                if (!target.prompted) return;
                if (target.cancel_requested) {
                    try self.settleTools(target, true);
                    var payload = try self.scoped(target);
                    try self.put(&payload, "reason", str("OpenCode interrupted the execution"));
                    _ = try self.emitWith(target, "run.cancelled", payload, true, try self.reportedCost(target));
                    return;
                }
                if (target.declined) return self.failRun(target, "opencode_permission_declined", "a declined tool call ended OpenCode's execution");
                const message = try std.mem.concat(self.allocator(), u8, &.{ "OpenCode interrupted the execution: ", data.reason });
                try self.failRun(target, "opencode_execution_interrupted", message);
            },
            .permission_asked => {
                const data = (try self.decodeFor(native.PermissionAskedData, native.decodePermissionAsked, event, run, "opencode_invalid_permission_event")) orelse return;
                try self.askPermission(run orelse return, data);
            },
            .permission_replied => {
                const data = (try self.decodeFor(native.PermissionRepliedData, native.decodePermissionReplied, event, run, "opencode_invalid_permission_event")) orelse return;
                try self.repliedElsewhere(data);
            },
            .step_started => {
                const data = (try self.decodeFor(native.StepStartedData, native.decodeStepStarted, event, run, "opencode_invalid_step_event")) orelse return;
                try self.observeModel(try normalizeModel(self.allocator(), data.model));
            },
            .step_ended => {
                const data = (try self.decodeFor(native.StepEndedData, native.decodeStepEnded, event, run, "opencode_invalid_step_event")) orelse return;
                const target = run orelse return;
                if (!try self.sighted(&target.steps, data.message)) return;
                if (target.terminal) return;
                target.last_finish = data.finish;
                target.cost += data.cost;
                const input = tokenCount(data.input_tokens);
                const output = tokenCount(data.output_tokens);
                target.input_tokens +%= input;
                target.output_tokens +%= output;
                target.total_tokens +%= input +% output;
            },
            .step_failed => {
                const data = (try self.decodeFor(native.StepFailedData, native.decodeStepFailed, event, run, "opencode_invalid_step_event")) orelse return;
                const target = run orelse return;
                if (!try self.sighted(&target.steps, data.message)) return;
                if (!target.terminal and !target.cancel_requested) target.failure = data.failure.message;
                target.cost += data.cost;
            },
            .text_delta, .reasoning_delta => {
                const data = (try self.decodeFor(native.TextData, native.decodePartDelta, event, run, "opencode_invalid_text_event")) orelse return;
                try self.streamPart(run orelse return, kind == .reasoning_delta, data);
            },
            .text_ended, .reasoning_ended => {
                const data = (try self.decodeFor(native.TextData, native.decodePartEnded, event, run, "opencode_invalid_text_event")) orelse return;
                try self.appendPart(run orelse return, kind == .reasoning_ended, data);
            },
            .tool_input_started => {
                const data = (try self.decodeFor(native.ToolInputData, native.decodeToolInputStarted, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.tool_names.put(self.allocator(), data.id, data.name);
            },
            .tool_called => {
                const data = (try self.decodeFor(native.ToolCalledData, native.decodeToolCalled, event, run, "opencode_invalid_tool_event")) orelse return;
                const name = self.tool_names.get(data.id) orelse "";
                const target = run orelse return;
                if (data.id.len == 0 or name.len == 0) return;
                try self.startTool(target, data.id, name, try gomarshal.canonicalAny(self.allocator(), data.input));
            },
            .tool_progress => {
                const data = (try self.decodeFor(native.ToolProgressData, native.decodeToolProgress, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.updateTool(run orelse return, data.id, data.metadata);
            },
            .tool_success => {
                const data = (try self.decodeFor(native.ToolContentData, native.decodeToolSuccess, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.endTool(run orelse return, data.id, null, try self.contentValue(data.content));
            },
            .tool_failed => {
                const data = (try self.decodeFor(native.ToolFailedData, native.decodeToolFailed, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.endTool(run orelse return, data.id, data.failure.message, .null);
            },
            else => {},
        }
    }

    fn delivered(self: *Reducer, inbox: []const u8) Error!void {
        var owner: ?*Run = null;
        var previous: ?*Run = null;
        if (self.takePending(inbox)) |candidate| {
            if (!candidate.terminal) {
                if (self.reserved == candidate) {
                    owner = candidate;
                    candidate.promotion_seen = true;
                    if (self.active) |current| {
                        if (!current.terminal and current != candidate) previous = current;
                    }
                } else if (self.active == candidate) owner = candidate;
            }
        }
        self.suppressed = owner == null;
        const run = owner orelse {
            if (self.active) |current| {
                if (!current.terminal and current.prompted) try self.settleRun(current);
            }
            return;
        };
        if (previous) |current| try self.settleRun(current);
        try self.promoteReserved();
        const started_at = self.now();
        var payload = try self.scoped(run);
        try self.put(&payload, "status", str("running"));
        if (self.options.model.len > 0) try self.put(&payload, "model_id", str(self.options.model));
        try self.put(&payload, "started_at_ms", int(started_at));
        if (!try self.emitWith(run, "run.started", payload, false, null)) return;
        run.prompted = true;
        if (run.cancel_requested) {
            switch (try self.client.interrupt(self.client.context, self.allocator(), self.options.native_id)) {
                .interrupted => {},
                .failed => |failure| try self.abandon(run, "opencode_cancellation_ambiguous", failure.message, settledBy(failure)),
            }
        }
    }

    fn sighted(self: *Reducer, seen: *[2]std.StringHashMapUnmanaged(void), key: []const u8) Error!bool {
        const own: usize = if (self.replaying) 1 else 0;
        if (seen[1 - own].contains(key) or (self.replaying and seen[own].contains(key))) return false;
        try seen[own].put(self.allocator(), key, {});
        return true;
    }

    pub fn reconcileTarget(self: *Reducer) ReconcileTarget {
        if (self.active) |run| {
            if (!run.terminal) return .{ .oldest = run.native_id, .delivered = if (run.prompted) run.native_id else "" };
        }
        if (self.reserved) |run| {
            if (!run.terminal) return .{ .oldest = run.native_id };
        }
        return .{};
    }

    pub fn replay(self: *Reducer, records: []const std.json.Value, delivered_id: []const u8, live: bool) Error!void {
        self.replaying = true;
        defer self.replaying = false;
        const scope = self.options.native_id;
        for (records) |record| {
            if (record != .object) continue;
            const kind = recordText(record, "type");
            const id = recordText(record, "id");
            if (std.mem.eql(u8, kind, "user")) {
                if (!std.mem.eql(u8, id, delivered_id)) try self.replayEvent("session.inbox.delivered", .{ .sessionID = scope, .inboxID = id });
            } else if (std.mem.eql(u8, kind, "assistant")) {
                const done = completedAt(record) > 0;
                var texts: i64 = 0;
                var thoughts: i64 = 0;
                const content = record.object.get("content") orelse std.json.Value.null;
                if (content == .array) for (content.array.items) |part| {
                    if (part != .object) continue;
                    const part_kind = recordText(part, "type");
                    if (std.mem.eql(u8, part_kind, "text")) {
                        const text = recordText(part, "text");
                        if (done or text.len > 0) try self.replayEvent("session.text.ended", .{ .sessionID = scope, .assistantMessageID = id, .ordinal = texts, .text = text });
                        texts += 1;
                    } else if (std.mem.eql(u8, part_kind, "reasoning")) {
                        if (done or completedAt(part) > 0) try self.replayEvent("session.reasoning.ended", .{ .sessionID = scope, .assistantMessageID = id, .ordinal = thoughts, .text = recordText(part, "text") });
                        thoughts += 1;
                    }
                };
                if (!done) continue;
                const failure = record.object.get("error") orelse std.json.Value.null;
                if (failure == .object) {
                    try self.replayEvent("session.step.failed", .{ .sessionID = scope, .assistantMessageID = id, .@"error" = .{ .type = recordText(failure, "type"), .message = recordText(failure, "message") }, .cost = recordNumber(record, "cost") });
                    continue;
                }
                try self.replayEvent("session.step.ended", .{ .sessionID = scope, .assistantMessageID = id, .finish = recordText(record, "finish"), .cost = recordNumber(record, "cost"), .tokens = record.object.get("tokens") orelse std.json.Value.null });
            } else if (std.mem.eql(u8, kind, "idle")) {
                const outcome = recordText(record, "outcome");
                if (std.mem.eql(u8, outcome, "succeeded")) {
                    try self.replayEvent("session.execution.succeeded", .{ .sessionID = scope });
                } else if (std.mem.eql(u8, outcome, "failed")) {
                    try self.replayEvent("session.execution.failed", .{ .sessionID = scope, .@"error" = .{ .type = "unknown", .message = "OpenCode recorded the execution as failed" } });
                } else if (std.mem.eql(u8, outcome, "interrupted")) {
                    try self.replayEvent("session.execution.interrupted", .{ .sessionID = scope, .reason = "recorded while the event stream was down" });
                }
            }
        }
        if (!live) try self.replayEvent("session.execution.interrupted", .{ .sessionID = scope, .reason = "the server ended the execution without recording it while the event stream was down" });
    }

    fn replayEvent(self: *Reducer, comptime name: []const u8, data: anytype) Error!void {
        const text = try std.json.Stringify.valueAlloc(self.allocator(), data, .{});
        try self.handle(.{ .id = "", .type_name = name, .kind = native.EventType.parse(name), .session_id = self.options.native_id, .data = text });
    }

    fn partKey(self: *Reducer, reasoning: bool, data: native.TextData) Error![]const u8 {
        return std.fmt.allocPrint(self.allocator(), "{c}{d}:{s}", .{ @as(u8, if (reasoning) 'r' else 't'), data.ordinal, data.message });
    }

    fn streamPart(self: *Reducer, run: *Run, reasoning: bool, data: native.TextData) Error!void {
        if (run.terminal or data.text.len == 0) return;
        const slot = try run.streamed.getOrPut(self.allocator(), try self.partKey(reasoning, data));
        if (!slot.found_existing) slot.value_ptr.* = .empty;
        try slot.value_ptr.appendSlice(self.allocator(), data.text);
        try self.emitPart(run, reasoning, data.text);
    }

    fn appendPart(self: *Reducer, run: *Run, reasoning: bool, data: native.TextData) Error!void {
        if (!try self.sighted(&run.ended_parts, try self.partKey(reasoning, data))) return;
        if (run.terminal) return;
        try run.parts.append(self.allocator(), .{ .reasoning = reasoning, .text = data.text });
        var rest = data.text;
        if (run.streamed.fetchRemove(try self.partKey(reasoning, data))) |streamed| {
            rest = if (std.mem.startsWith(u8, data.text, streamed.value.items)) data.text[streamed.value.items.len..] else "";
        }
        if (rest.len == 0) return;
        try self.emitPart(run, reasoning, rest);
    }

    fn emitPart(self: *Reducer, run: *Run, reasoning: bool, text: []const u8) Error!void {
        var payload = try self.scoped(run);
        try self.put(&payload, "message_id", str(run.message_id));
        try self.put(&payload, "part", try self.partValue(.{ .reasoning = reasoning, .text = text }));
        _ = try self.emitWith(run, "content.delta", payload, false, null);
    }

    fn partValue(self: *Reducer, part: Part) std.mem.Allocator.Error!std.json.Value {
        var value: std.json.ObjectMap = .empty;
        try self.put(&value, "type", str(if (part.reasoning) "reasoning" else "text"));
        try self.put(&value, if (part.reasoning) "reasoning" else "text", str(part.text));
        return .{ .object = value };
    }

    fn contentValue(self: *Reducer, content: ?[]const native.Content) std.mem.Allocator.Error!std.json.Value {
        const items = content orelse return .null;
        var array = try std.json.Array.initCapacity(self.allocator(), items.len);
        for (items) |item| {
            var entry: std.json.ObjectMap = .empty;
            try self.put(&entry, "type", str(item.kind));
            if (item.text.len > 0) try self.put(&entry, "text", str(item.text));
            if (item.uri.len > 0) try self.put(&entry, "uri", str(item.uri));
            if (item.mime.len > 0) try self.put(&entry, "mime", str(item.mime));
            if (item.name.len > 0) try self.put(&entry, "name", str(item.name));
            array.appendAssumeCapacity(.{ .object = entry });
        }
        return .{ .array = array };
    }

    fn observeModel(self: *Reducer, model: []const u8) std.mem.Allocator.Error!void {
        if (model.len == 0) return;
        for (self.catalog.items) |seen| {
            if (std.mem.eql(u8, seen, model)) return;
        }
        try self.catalog.append(self.allocator(), model);
    }

    fn findTool(self: *Reducer, run: *Run, native_id: []const u8) ?*Tool {
        for (self.tools.items) |tool| {
            if (tool.run == run and std.mem.eql(u8, tool.native_id, native_id)) return tool;
        }
        return null;
    }

    fn toolPayload(self: *Reducer, tool: *Tool, fields: ToolFields) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload = try self.scoped(tool.run);
        try self.put(&payload, "tool_call_id", str(tool.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "execution_owner", str("opencode-server"));
        if (tool.name.len > 0) try self.put(&payload, "name", str(tool.name));
        if (fields.arguments) try self.put(&payload, "arguments_json", tool.args);
        if (fields.progress) {
            if (tool.progress) |progress| try self.put(&payload, "progress", progress);
        }
        if (fields.result) {
            if (tool.result) |result| try self.put(&payload, "result", result);
        }
        if (fields.failure) |failure| {
            var problem: std.json.ObjectMap = .empty;
            try self.put(&problem, "code", str(failure[0]));
            try self.put(&problem, "message", str(failure[1]));
            try self.put(&payload, "error", .{ .object = problem });
        }
        return payload;
    }

    fn startTool(self: *Reducer, run: *Run, native_id: []const u8, name: []const u8, args: std.json.Value) Error!void {
        if (self.findTool(run, native_id) != null) return self.failRun(run, "opencode_invalid_tool_lifecycle", "duplicate or foreign tool start");
        const id = try self.nextID("tool-call");
        const tool = try self.allocator().create(Tool);
        tool.* = .{ .run = run, .native_id = native_id, .id = id, .name = name, .args = args };
        try self.tools.append(self.allocator(), tool);
        const requested = try self.emitEnvelope(run, "action.call.requested", try self.toolPayload(tool, .{ .arguments = true, .progress = true, .result = true }), false, "", null);
        const started = try self.emitEnvelope(run, "action.call.started", try self.toolPayload(tool, .{ .result = true }), false, requested orelse "", null);
        tool.started_event = started orelse "";
    }

    fn updateTool(self: *Reducer, run: *Run, native_id: []const u8, progress: std.json.Value) Error!void {
        const tool = self.findTool(run, native_id) orelse return self.failRun(run, "opencode_invalid_tool_lifecycle", "tool progress without active start");
        if (tool.terminal) return self.failRun(run, "opencode_invalid_tool_lifecycle", "tool progress without active start");
        tool.progress = progress;
        _ = try self.emitEnvelope(run, "action.call.progress", try self.toolPayload(tool, .{ .progress = true, .result = true }), false, tool.started_event, null);
    }

    fn endTool(self: *Reducer, run: *Run, native_id: []const u8, failure: ?[]const u8, content: std.json.Value) Error!void {
        const tool = self.findTool(run, native_id) orelse return self.failRun(run, "opencode_invalid_tool_lifecycle", "tool end without active start");
        if (tool.terminal) return self.failRun(run, "opencode_invalid_tool_lifecycle", "duplicate or foreign tool end");
        tool.terminal = true;
        if (failure) |message| {
            _ = try self.emitEnvelope(run, "action.call.failed", try self.toolPayload(tool, .{ .failure = .{ "opencode_tool_failed", message } }), false, tool.started_event, null);
            return;
        }
        tool.result = content;
        _ = try self.emitEnvelope(run, "action.call.completed", try self.toolPayload(tool, .{ .result = true }), false, tool.started_event, null);
    }

    fn permissionTitle(self: *Reducer, data: native.PermissionAskedData) std.mem.Allocator.Error![]const u8 {
        if (data.resources.len == 0) return data.action;
        const joined = try std.mem.join(self.allocator(), ", ", data.resources);
        return std.mem.concat(self.allocator(), u8, &.{ data.action, ": ", joined });
    }

    fn askPermission(self: *Reducer, run: *Run, data: native.PermissionAskedData) Error!void {
        if (run.terminal or !run.prompted or data.id.len == 0) return;
        for (self.gates.items) |gate| {
            if (std.mem.eql(u8, gate.native_id, data.id)) return;
        }
        const title = try self.permissionTitle(data);
        const tool = self.findTool(run, data.source_id) orelse return self.failRun(run, "opencode_permission_without_tool", try std.mem.concat(self.allocator(), u8, &.{ "OpenCode asked permission for ", title, " outside a tool call this run started" }));
        if (tool.terminal or data.source_id.len == 0) return self.failRun(run, "opencode_permission_without_tool", try std.mem.concat(self.allocator(), u8, &.{ "OpenCode asked permission for ", title, " outside a tool call this run started" }));
        const gate = try self.allocator().create(Gate);
        gate.* = .{ .id = try self.nextID("interaction"), .native_id = data.id, .run = run, .tool = tool };
        try self.gates.append(self.allocator(), gate);
        var payload = try self.scoped(run);
        try self.put(&payload, "interaction_id", str(gate.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "responded_by", str(self.options.participant));
        try self.put(&payload, "tool_call_id", str(tool.id));
        try self.put(&payload, "title", str(title));
        if (data.message.len > 0) try self.put(&payload, "description", str(data.message));
        var choices = try std.json.Array.initCapacity(self.allocator(), permission_choices.len);
        for (permission_choices) |choice| {
            var entry: std.json.ObjectMap = .empty;
            try self.put(&entry, "id", str(choice[0]));
            try self.put(&entry, "label", str(choice[1]));
            try self.put(&entry, "description", str(choice[2]));
            choices.appendAssumeCapacity(.{ .object = entry });
        }
        try self.put(&payload, "choices", .{ .array = choices });
        try self.put(&payload, "arguments_json", tool.args);
        gate.requested = (try self.emitEnvelope(run, "action.permission.requested", payload, false, "", null)) orelse "";
    }

    pub fn openGates(self: *Reducer, arena: std.mem.Allocator) std.mem.Allocator.Error![]const []const u8 {
        var pending: std.ArrayList([]const u8) = .empty;
        for (self.gates.items) |gate| {
            if (!gate.resolved) try pending.append(arena, try arena.dupe(u8, gate.id));
        }
        return pending.items;
    }

    fn gateResolution(self: *Reducer, gate: *Gate, outcome: []const u8, choice: []const u8, granted: ?bool, reason: ?[2][]const u8) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload = try self.scoped(gate.run);
        try self.put(&payload, "interaction_id", str(gate.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "responded_by", str(self.options.participant));
        try self.put(&payload, "tool_call_id", str(gate.tool.id));
        try self.put(&payload, "outcome", str(outcome));
        if (choice.len > 0) try self.put(&payload, "choice_id", str(choice));
        if (granted) |value| try self.put(&payload, "granted", .{ .bool = value });
        if (reason) |problem| {
            var entry: std.json.ObjectMap = .empty;
            try self.put(&entry, "code", str(problem[0]));
            try self.put(&entry, "message", str(problem[1]));
            try self.put(&payload, "reason", .{ .object = entry });
        }
        return payload;
    }

    fn repliedElsewhere(self: *Reducer, data: native.PermissionRepliedData) Error!void {
        for (self.gates.items) |gate| {
            if (!std.mem.eql(u8, gate.native_id, data.request_id)) continue;
            if (gate.resolved or gate.run.terminal) return;
            gate.resolved = true;
            const message = try std.mem.concat(self.allocator(), u8, &.{ "OpenCode recorded the reply \"", data.reply, "\" from outside this session" });
            _ = try self.emitEnvelope(gate.run, "action.permission.resolved", try self.gateResolution(gate, "cancelled", "", null, .{ "opencode_permission_replied_elsewhere", message }), false, gate.requested, null);
            return;
        }
    }

    fn settleGates(self: *Reducer, run: *Run) Error!void {
        for (self.gates.items) |gate| {
            if (gate.run != run or gate.resolved) continue;
            gate.resolved = true;
            _ = try self.emitEnvelope(run, "action.permission.resolved", try self.gateResolution(gate, "cancelled", "", null, .{ "run_settled", "the run ended before the permission was answered" }), false, gate.requested, null);
        }
    }

    pub fn resolvePermission(self: *Reducer, interaction_id: []const u8, run_id: []const u8, responded_by: []const u8, requested_by: []const u8, choice: []const u8, granted: bool, reason: []const u8) ResolveError!void {
        if (self.unusable) return error.SessionClosed;
        const gate = for (self.gates.items) |candidate| {
            if (std.mem.eql(u8, candidate.id, interaction_id)) break candidate;
        } else return error.InteractionNotFound;
        if (gate.run.terminal) return error.InteractionNotFound;
        if (gate.resolved) return error.InteractionResolved;
        if (!std.mem.eql(u8, run_id, gate.run.id) or (requested_by.len > 0 and !std.mem.eql(u8, requested_by, endpoint_id))) return error.InvalidResolution;
        if (!std.mem.eql(u8, responded_by, self.options.participant)) return error.WrongResponder;
        const allows = std.mem.eql(u8, choice, "once") or std.mem.eql(u8, choice, "always");
        if ((!allows and !std.mem.eql(u8, choice, "reject")) or allows != granted) return error.InvalidResolution;
        const reply = self.client.reply_permission orelse return error.InteractionNotFound;
        if (try reply(self.client.context, self.allocator(), self.options.native_id, gate.native_id, choice, reason)) |failure| {
            self.reply_failure = failure.message;
            return error.ReplyFailed;
        }
        if (gate.run.terminal) return error.InteractionNotFound;
        gate.resolved = true;
        if (!allows and reason.len == 0) gate.run.declined = true;
        _ = try self.emitEnvelope(gate.run, "action.permission.resolved", try self.gateResolution(gate, if (allows) "resolved" else "rejected", choice, allows, null), false, gate.requested, null);
    }

    fn settleTools(self: *Reducer, run: *Run, cancelled: bool) Error!void {
        try self.settleGates(run);
        var unfinished = std.ArrayList(*Tool).empty;
        for (self.tools.items) |tool| {
            if (tool.run != run or tool.terminal) continue;
            tool.terminal = true;
            try unfinished.append(self.allocator(), tool);
        }
        for (unfinished.items) |tool| {
            if (cancelled) {
                _ = try self.emitEnvelope(run, "action.call.cancelled", try self.toolPayload(tool, .{ .progress = true, .result = true }), false, tool.started_event, null);
            } else {
                _ = try self.emitEnvelope(run, "action.call.failed", try self.toolPayload(tool, .{ .progress = true, .failure = .{ "incomplete_tool", "OpenCode run settled with an unfinished tool" } }), false, tool.started_event, null);
            }
        }
    }

    fn reportedCost(self: *Reducer, run: *Run) std.mem.Allocator.Error!?std.json.Value {
        if (!std.math.isFinite(run.cost)) return null;
        var total: std.json.ObjectMap = .empty;
        try self.put(&total, "total_cost_usd", .{ .float = run.cost });
        var extensions: std.json.ObjectMap = .empty;
        try self.put(&extensions, cost_extension, .{ .object = total });
        return .{ .object = extensions };
    }

    fn settleRun(self: *Reducer, run: *Run) Error!void {
        const cancel_requested = run.cancel_requested;
        const reported = try self.reportedCost(run);
        try self.settleTools(run, cancel_requested);
        if (cancel_requested) {
            var payload = try self.scoped(run);
            try self.put(&payload, "reason", str("OpenCode interrupt confirmed idle"));
            _ = try self.emitWith(run, "run.cancelled", payload, true, reported);
            return;
        }
        if (run.failure) |failure| return self.failRun(run, "opencode_step_failed", failure);
        var payload = try self.scoped(run);
        var response: std.json.ObjectMap = .empty;
        try self.put(&response, "id", str(run.message_id));
        try self.put(&response, "role", str("assistant"));
        if (run.parts.items.len == 0) {
            try self.put(&response, "content", str(""));
        } else {
            var parts = try std.json.Array.initCapacity(self.allocator(), run.parts.items.len);
            for (run.parts.items) |part| parts.appendAssumeCapacity(try self.partValue(part));
            try self.put(&response, "content", .{ .array = parts });
        }
        try self.put(&payload, "final_response", .{ .object = response });
        try self.put(&payload, "stop_reason", str(if (run.last_finish.len > 0) run.last_finish else "unknown"));
        var usage: std.json.ObjectMap = .empty;
        if (run.input_tokens != 0) try self.put(&usage, "input_tokens", try unsigned(self.allocator(), run.input_tokens));
        if (run.output_tokens != 0) try self.put(&usage, "output_tokens", try unsigned(self.allocator(), run.output_tokens));
        if (run.total_tokens != 0) try self.put(&usage, "total_tokens", try unsigned(self.allocator(), run.total_tokens));
        try self.put(&payload, "usage", .{ .object = usage });
        _ = try self.emitWith(run, "run.completed", payload, true, reported);
    }

    fn failRun(self: *Reducer, run: *Run, code: []const u8, message: []const u8) Error!void {
        return self.failRunSettled(run, code, message, "");
    }

    fn failRunSettled(self: *Reducer, run: *Run, code: []const u8, message: []const u8, settled_by: []const u8) Error!void {
        try self.settleTools(run, true);
        var payload = try self.scoped(run);
        var problem: std.json.ObjectMap = .empty;
        try self.put(&problem, "code", str(code));
        try self.put(&problem, "message", str(message));
        try self.put(&payload, "error", .{ .object = problem });
        if (settled_by.len > 0) try self.put(&payload, "settled_by", str(settled_by));
        _ = try self.emitWith(run, "run.failed", payload, true, try self.reportedCost(run));
    }

    fn abandon(self: *Reducer, origin: ?*Run, code: []const u8, message: []const u8, settled_by: []const u8) Error!void {
        const run = self.active;
        const reserved = self.reserved;
        self.unusable = true;
        if (run) |current| {
            if (!current.terminal) try self.failRunSettled(current, code, message, settled_by);
        }
        const waiting = reserved orelse return;
        if (waiting.terminal) return;
        if (waiting != origin and !waiting.promotion_seen) {
            const dropped = try std.mem.concat(self.allocator(), u8, &.{ "the reservation was dropped before promotion: ", message });
            return self.failRunSettled(waiting, "queue_dropped", dropped, "inferred");
        }
        return self.failRunSettled(waiting, code, message, settled_by);
    }

    pub fn transportFailed(self: *Reducer, message: []const u8) Error!void {
        return self.abandon(null, "opencode_stream_failed", if (message.len == 0) "EOF" else message, "inferred");
    }

    pub fn cancel(self: *Reducer, run_id: []const u8) Error!CancelResponse {
        if (self.unusable) return error.SessionClosed;
        const run = self.find(run_id) orelse return error.RunNotFound;
        const response = CancelResponse{ .session_id = self.options.session_id, .run_id = run.id, .status = .cancelling };
        if (run.terminal) {
            if (run.status == .cancelled) return .{ .session_id = response.session_id, .run_id = run.id, .status = .cancelled };
            return error.RunTerminal;
        }
        if (run.cancel_requested) return .{ .session_id = response.session_id, .run_id = run.id, .status = run.status };
        const prompted = run.prompted;
        const reservation = !prompted and !run.promotion_seen;
        run.cancel_requested = true;
        run.status = .cancelling;
        if (reservation) {
            if (run.native_id.len > 0) {
                if (try self.client.cancel_inbox(self.client.context, self.allocator(), self.options.native_id, run.native_id)) |failure| {
                    try self.abandon(run, "opencode_cancellation_ambiguous", failure.message, settledBy(failure));
                    return error.CancellationAmbiguous;
                }
            }
            return response;
        }
        switch (try self.client.interrupt(self.client.context, self.allocator(), self.options.native_id)) {
            .interrupted => {},
            .failed => |failure| {
                try self.abandon(run, "opencode_cancellation_ambiguous", failure.message, settledBy(failure));
                return error.CancellationAmbiguous;
            },
        }
        if (prompted) {
            const updated = self.now();
            var payload = try self.scoped(run);
            try self.put(&payload, "status", str("cancelling"));
            try self.put(&payload, "updated_at_ms", int(updated));
            _ = try self.emitWith(run, "run.status.updated", payload, false, null);
        }
        return response;
    }

    pub fn models(self: *Reducer, session_id: []const u8, allow_degraded: bool) Error!std.json.Value {
        if (!allow_degraded) return error.DegradedWithoutOptIn;
        if (self.unusable) return error.SessionClosed;
        if (session_id.len > 0 and !std.mem.eql(u8, session_id, self.options.session_id)) return error.RunNotFound;
        var catalog: std.json.ObjectMap = .empty;
        try self.put(&catalog, "session_id", str(self.options.session_id));
        if (self.options.model.len > 0) try self.put(&catalog, "current_model_id", str(self.options.model));
        var listed = try std.json.Array.initCapacity(self.allocator(), self.catalog.items.len);
        for (self.catalog.items) |model| {
            var descriptor: std.json.ObjectMap = .empty;
            try self.put(&descriptor, "id", str(model));
            if (std.mem.indexOfScalar(u8, model, '/')) |slash| try self.put(&descriptor, "provider_id", str(model[0..slash]));
            if (std.mem.eql(u8, model, self.options.model)) try self.put(&descriptor, "default", .{ .bool = true });
            listed.appendAssumeCapacity(.{ .object = descriptor });
        }
        try self.put(&catalog, "models", .{ .array = listed });
        return .{ .object = catalog };
    }

    fn promoteReserved(self: *Reducer) Error!void {
        const reserved = self.reserved orelse return;
        if (!reserved.promotion_seen) return;
        if (self.active) |current| {
            if (!current.terminal) return;
        }
        self.reserved = null;
        const held = reserved.held;
        reserved.held = .empty;
        reserved.holding = false;
        if (!reserved.terminal) {
            self.active = reserved;
            reserved.status = .running;
        }
        for (held.items) |envelope| try self.publish(reserved, envelope);
    }

    fn emitWith(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool, extensions: ?std.json.Value) Error!bool {
        _ = try self.emitEnvelope(run, kind, payload, terminal, "", extensions) orelse return false;
        if (terminal) try self.promoteReserved();
        return true;
    }

    fn emitEnvelope(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool, in_reply_to: []const u8, extensions: ?std.json.Value) Error!?[]const u8 {
        if (run.terminal) return null;
        const id = try self.nextID("event");
        const timestamp = self.now();
        const sequence = run.next;
        run.next += 1;
        var envelope: std.json.ObjectMap = .empty;
        try self.put(&envelope, "protocol", str(protocol_name));
        try self.put(&envelope, "version", str(protocol_version));
        try self.put(&envelope, "profile", str(profile));
        try self.put(&envelope, "type", str(kind));
        try self.put(&envelope, "id", str(id));
        try self.put(&envelope, "payload", .{ .object = payload });
        try self.put(&envelope, "sequence", try unsigned(self.allocator(), sequence));
        try self.put(&envelope, "timestamp_ms", int(timestamp));
        if (in_reply_to.len > 0) try self.put(&envelope, "in_reply_to", str(in_reply_to));
        try self.put(&envelope, "session_id", str(self.options.session_id));
        try self.put(&envelope, "run_id", str(run.id));
        if (std.mem.startsWith(u8, kind, "action.call.") or std.mem.startsWith(u8, kind, "action.permission.")) {
            if (payload.get("tool_call_id")) |carried| try self.put(&envelope, "tool_call_id", carried);
        }
        try self.put(&envelope, "capability_revision", str(self.options.revision));
        if (extensions) |carried| try self.put(&envelope, "extensions", carried);
        if (std.mem.eql(u8, kind, "run.started") and !run.holding) run.status = .running;
        if (terminal) {
            run.terminal = true;
            run.status = if (std.mem.eql(u8, kind, "run.completed")) .completed else if (std.mem.eql(u8, kind, "run.cancelled")) .cancelled else .failed;
            if (self.active == run) self.active = null;
            if (self.reserved == run and !run.holding) self.reserved = null;
        }
        const value = std.json.Value{ .object = envelope };
        if (run.holding) {
            try run.held.append(self.allocator(), value);
            return id;
        }
        try self.publish(run, value);
        return id;
    }

    fn publish(self: *Reducer, run: *Run, envelope: std.json.Value) std.mem.Allocator.Error!void {
        const kind = envelope.object.get("type").?.string;
        if (std.mem.eql(u8, kind, "run.started")) run.start_published = true;
        try self.envelopes.append(self.allocator(), envelope);
        const carried = envelope.object.get("sequence") orelse return;
        const sequence: u64 = switch (carried) {
            .integer => |value| @intCast(value),
            .number_string => |text| std.fmt.parseInt(u64, text, 10) catch return,
            else => return,
        };
        if (sequence > run.published_seq) run.published_seq = sequence;
        if (std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled")) {
            try self.settled.append(self.allocator(), .{ .run_id = run.id, .sequence = sequence });
        }
    }
};

pub fn admissionValue(arena: std.mem.Allocator, admission: Admission) std.mem.Allocator.Error!std.json.Value {
    var payload: std.json.ObjectMap = .empty;
    try payload.put(arena, "session_id", str(admission.session_id));
    try payload.put(arena, "accepted", .{ .bool = true });
    try payload.put(arena, "submission_id", str(admission.submission_id));
    try payload.put(arena, "requested_delivery", str(admission.requested_delivery));
    try payload.put(arena, "effective_delivery", str(admission.effective_delivery));
    if (admission.delivery_resolution.len > 0) try payload.put(arena, "delivery_resolution", str(admission.delivery_resolution));
    try payload.put(arena, "admission", str(admission.admission));
    try payload.put(arena, "run_id", str(admission.run_id));
    try payload.put(arena, "status", str(@tagName(admission.status)));
    if (admission.model_id.len > 0) try payload.put(arena, "model_id", str(admission.model_id));
    if (admission.message_ids.len > 0) {
        var ids = try std.json.Array.initCapacity(arena, admission.message_ids.len);
        for (admission.message_ids) |id| ids.appendAssumeCapacity(str(id));
        try payload.put(arena, "message_ids", .{ .array = ids });
    }
    return .{ .object = payload };
}

const Feature = struct { key: []const u8, level: []const u8, reason: []const u8, modes: []const []const u8 = &.{} };

const features = [_]Feature{
    .{ .key = "action.permissions", .level = "native", .reason = "permission.asked for a tool call becomes action.permission.requested, answered once, always or reject through POST /api/session/:id/permission/:requestID/reply" },
    .{ .key = "action.tools", .level = "native", .reason = "tool.called/progress/success/failed lifecycle observed natively" },
    .{ .key = "action.tools.execute", .level = "unavailable", .reason = "tools execute server-side; no client-hosted execution surface" },
    .{ .key = "capabilities", .level = "emulated", .reason = "descriptor synthesized from the pinned route inventory" },
    .{ .key = "models.list", .level = "degraded", .reason = "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision" },
    .{ .key = "protocol.initialize", .level = "emulated", .reason = "OpenCode has no initialize handshake; OpenAPI and catalogs describe the server" },
    .{ .key = "run.cancel", .level = "degraded", .reason = "interrupt is intent with an idle no-op; a running run settles at session.execution.interrupted and a queued one at session.inbox.cancelled" },
    .{ .key = "run.reconciliation", .level = "emulated", .reason = "adapter-owned projection over the session events; when the event stream ends it subscribes again once and reconciles the open runs from the session record" },
    .{ .key = "run.replay", .level = "degraded", .reason = "bounded adapter journal; the native durable cursor is exposed as the transcript cursor" },
    .{ .key = "run.resume", .level = "degraded", .reason = "conversation resume exists natively but is not exercised; OAP resume replays the adapter journal" },
    .{ .key = "run.status", .level = "native", .reason = "session.inbox.delivered starts a run and session.execution.* settles it" },
    .{ .key = "run.streaming", .level = "native", .reason = "session.text.delta and session.reasoning.delta are forwarded as they arrive, and a part's ended event adds only the text its deltas did not carry" },
    .{ .key = "session.compaction.policy", .level = "unavailable", .reason = "compaction is the server's config, fixed when its operator starts it; the adapter attaches to a running server" },
    .{ .key = "session.message.delivery.auto", .level = "emulated", .reason = "no native auto; steer when the session is idle, queue behind an open run" },
    .{ .key = "session.message.delivery.queue", .level = "native", .reason = "a prompt with delivery=queue is admitted to the session inbox and starts its run at session.inbox.delivered" },
    .{ .key = "session.message.delivery.steer", .level = "unavailable", .reason = "an explicit steer request is rejected as outside the v0.1 subset; the server's default delivery is exposed through an auto request" },
    .{ .key = "session.message.submit", .level = "native", .reason = "durable admission receipt with typed conflict rejection" },
    .{ .key = "session.open", .level = "native", .reason = "POST /api/session with server-assigned identity" },
    .{ .key = "session.open.reopen", .level = "native", .reason = "GET /api/session/:id attaches to the bound server session and follows its events from the attach on; an unknown or running session is refused" },
    .{ .key = "session.reasoning", .level = "native", .reason = "the session's model carries the level as its variant, which the runner sends on every step: set at create and between runs by switching the session to the same model with the new variant; it needs a model, and a variant the session record does not confirm is refused", .modes = &.{ "session_open", "session_live" } },
    .{ .key = "session.state", .level = "emulated", .reason = "active set and adapter-owned projection" },
};

pub fn capabilities(arena: std.mem.Allocator) std.mem.Allocator.Error!std.json.Value {
    var endpoint: std.json.ObjectMap = .empty;
    try endpoint.put(arena, "id", str(endpoint_id));
    try endpoint.put(arena, "name", str("OpenCode Server Adapter"));
    try endpoint.put(arena, "version", str(native.pinned_tag));
    try endpoint.put(arena, "adapter", str("opencode-http-sse"));
    var versions = try std.json.Array.initCapacity(arena, 1);
    versions.appendAssumeCapacity(str(protocol_version));
    var profiles = try std.json.Array.initCapacity(arena, 1);
    profiles.appendAssumeCapacity(str(profile));
    var table: std.json.ObjectMap = .empty;
    for (features) |feature| {
        var support: std.json.ObjectMap = .empty;
        try support.put(arena, "level", str(feature.level));
        try support.put(arena, "reason", str(feature.reason));
        if (feature.modes.len != 0) {
            var modes = try std.json.Array.initCapacity(arena, feature.modes.len);
            for (feature.modes) |mode| modes.appendAssumeCapacity(str(mode));
            try support.put(arena, "modes", .{ .array = modes });
        }
        try table.put(arena, feature.key, .{ .object = support });
    }
    var limits: std.json.ObjectMap = .empty;
    try limits.put(arena, "max_active_runs_per_session", int(max_active_runs));
    try limits.put(arena, "max_queued_runs_per_session", int(max_queued_runs));
    var descriptor: std.json.ObjectMap = .empty;
    try descriptor.put(arena, "endpoint", .{ .object = endpoint });
    try descriptor.put(arena, "protocol_versions", .{ .array = versions });
    try descriptor.put(arena, "profiles", .{ .array = profiles });
    try descriptor.put(arena, "features", .{ .object = table });
    try descriptor.put(arena, "limits", .{ .object = limits });
    return .{ .object = descriptor };
}

const testing = std.testing;

const Fake = struct {
    interrupt_failure: ?Failure = null,
    prompts: usize = 0,
    interrupts: usize = 0,
    cancelled: std.ArrayList([]const u8) = .empty,
    replies: std.ArrayList([]const u8) = .empty,
    reply_failure: ?Failure = null,

    fn from(context: *anyopaque) *Fake {
        return @ptrCast(@alignCast(context));
    }

    fn prompt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!PromptOutcome {
        _ = arena;
        const self = from(context);
        self.prompts += 1;
        return .{ .admitted = .{ .id = request.id, .session_id = session_id, .kind = "user", .delivery = request.delivery, .time_created = 1 } };
    }

    fn interrupt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!InterruptOutcome {
        _ = arena;
        _ = session_id;
        const self = from(context);
        self.interrupts += 1;
        if (self.interrupt_failure) |failure| return .{ .failed = failure };
        return .{ .interrupted = true };
    }

    fn cancelInbox(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, inbox: []const u8) std.mem.Allocator.Error!?Failure {
        _ = session_id;
        try from(context).cancelled.append(arena, inbox);
        return null;
    }

    fn replyPermission(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, request_id: []const u8, decision: []const u8, message: []const u8) std.mem.Allocator.Error!?Failure {
        _ = session_id;
        const self = from(context);
        try self.replies.append(arena, try std.mem.concat(arena, u8, &.{ request_id, "|", decision, "|", message }));
        return self.reply_failure;
    }

    fn client(self: *Fake) Native {
        return .{ .context = self, .prompt = prompt, .interrupt = interrupt, .cancel_inbox = cancelInbox, .reply_permission = replyPermission };
    }
};

const native_session = "ses_fake00000000000000";

fn nativeEvent(arena: std.mem.Allocator, seq: i64, kind: []const u8, data: []const u8) !native.Event {
    var diag = native.Diagnostic{};
    const rest = if (data.len > 2) try std.mem.concat(arena, u8, &.{ ",", data[1..] }) else "}";
    const scoped_data = try std.mem.concat(arena, u8, &.{ "{\"sessionID\":\"" ++ native_session ++ "\"", rest });
    const durable = (native.EventType.parse(try std.mem.concat(arena, u8, &.{ "session.", kind })) orelse return error.UnknownKind).durable();
    const position = if (durable) try std.fmt.allocPrint(arena, ",\"durable\":{{\"aggregateID\":\"" ++ native_session ++ "\",\"seq\":{d},\"version\":1}}", .{seq}) else "";
    const wire = try std.fmt.allocPrint(arena, "{{\"id\":\"evt_{d}\",\"type\":\"session.{s}\"{s},\"data\":{s}}}", .{ seq, kind, position, scoped_data });
    return native.decodeEvent(arena, wire, &diag);
}

fn deliveredEvent(arena: std.mem.Allocator, seq: i64, message: []const u8) !native.Event {
    return nativeEvent(arena, seq, "inbox.delivered", try std.fmt.allocPrint(arena, "{{\"inboxID\":\"{s}\"}}", .{message}));
}

fn stepStarted(arena: std.mem.Allocator, seq: i64, model: []const u8) !native.Event {
    return nativeEvent(arena, seq, "step.started", try std.fmt.allocPrint(arena, "{{\"assistantMessageID\":\"msg_a\",\"agent\":\"build\",\"model\":{{\"id\":\"{s}\",\"providerID\":\"fixture\"}},\"started\":1}}", .{model}));
}

fn stepEnded(arena: std.mem.Allocator, seq: i64) !native.Event {
    return nativeEvent(arena, seq, "step.ended", "{\"assistantMessageID\":\"msg_a\",\"finish\":\"stop\",\"cost\":0,\"tokens\":{\"input\":1,\"output\":1,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}}}");
}

fn succeeded(arena: std.mem.Allocator, seq: i64) !native.Event {
    return nativeEvent(arena, seq, "execution.succeeded", "{}");
}

fn expectKinds(reducer: *Reducer, want: []const []const u8) !void {
    try testing.expectEqual(want.len, reducer.envelopes.items.len);
    for (want, reducer.envelopes.items) |kind, envelope| try testing.expectEqualStrings(kind, envelope.object.get("type").?.string);
}

fn payloadOf(envelope: std.json.Value) std.json.ObjectMap {
    return envelope.object.get("payload").?.object;
}

fn errorCode(envelope: std.json.Value) []const u8 {
    return payloadOf(envelope).get("error").?.object.get("code").?.string;
}

test "submit refuses a delivery outside auto and queue, an empty session and another session" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    try testing.expectError(error.Unsupported, reducer.submit("session", "hi", "steer"));
    try testing.expectError(error.Unsupported, reducer.submit("session", "hi", "btw"));
    try testing.expectError(error.InvalidSubmission, reducer.submit("", "hi", "auto"));
    try testing.expectError(error.RunNotFound, reducer.submit("other", "hi", "auto"));
    try testing.expectEqual(@as(usize, 0), fake.prompts);
    const admission = try reducer.submit("session", "hi", "");
    try testing.expectEqualStrings("auto", admission.requested_delivery);
}

test "identities come from one counter in the order the oracle allocates them" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try testing.expectEqualStrings("run-1", admission.run_id);
    try testing.expectEqualStrings("message-2", reducer.find("run-1").?.message_id);
    try testing.expectEqualStrings("submission-3", admission.submission_id);
    try testing.expectEqualStrings("msg_oap0000000000000004", admission.message_ids[0]);
    try testing.expectEqual(@as(i64, 2), reducer.clock);
}

test "a second reservation is refused while one is published, and nothing is admitted once unusable" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try testing.expectEqualStrings("started", first.admission);
    const second = try reducer.submit("session", "later", "auto");
    try testing.expectEqualStrings("queued", second.admission);
    try testing.expectEqualStrings("session_busy", second.delivery_resolution);
    try testing.expectError(error.RunActive, reducer.submit("session", "more", "queue"));
    try testing.expectEqual(@as(usize, 2), fake.prompts);
    try reducer.transportFailed("");
    try expectKinds(&reducer, &.{ "run.failed", "run.failed" });
    try testing.expectEqualStrings("opencode_stream_failed", errorCode(reducer.envelopes.items[0]));
    try testing.expectEqualStrings("EOF", payloadOf(reducer.envelopes.items[0]).get("error").?.object.get("message").?.string);
    try testing.expectEqualStrings("queue_dropped", errorCode(reducer.envelopes.items[1]));
    try testing.expectError(error.SessionClosed, reducer.submit("session", "again", "auto"));
    try testing.expectError(error.SessionClosed, reducer.cancel(first.run_id));
}

test "an identity generator that breaks the message pattern settles the reservation without prompting" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session, .message_prefix = "message_" }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try testing.expectEqualStrings("queued", admission.admission);
    try testing.expectEqual(@as(usize, 0), fake.prompts);
    try expectKinds(&reducer, &.{"run.failed"});
    try testing.expectEqualStrings("opencode_invalid_message_id", errorCode(reducer.envelopes.items[0]));
    try testing.expectEqualStrings("inferred", payloadOf(reducer.envelopes.items[0]).get("settled_by").?.string);
}

test "a run starts at its input's delivery and settles when the execution succeeds, not at a step's end" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try nativeEvent(scratch, 1, "execution.started", "{}"));
    try expectKinds(&reducer, &.{});
    try reducer.observe(try deliveredEvent(scratch, 2, admission.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 3, "a"));
    try reducer.observe(try stepEnded(scratch, 4));
    try reducer.observe(try stepStarted(scratch, 5, "a"));
    try reducer.observe(try stepEnded(scratch, 6));
    try expectKinds(&reducer, &.{"run.started"});
    try reducer.observe(try succeeded(scratch, 7));
    try expectKinds(&reducer, &.{ "run.started", "run.completed" });
    try testing.expectEqual(@as(i64, 4), payloadOf(reducer.envelopes.items[1]).get("usage").?.object.get("total_tokens").?.integer);
}

test "cancel answers by the run's settled status, deletes an undelivered input, and fails the session when the interrupt is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    try testing.expectError(error.RunNotFound, reducer.cancel("run-9"));
    const done = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, done.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try stepEnded(scratch, 3));
    try reducer.observe(try succeeded(scratch, 4));
    try testing.expectEqual(Status.completed, reducer.runStatus(done.run_id).?);
    try testing.expectError(error.RunTerminal, reducer.cancel(done.run_id));

    const cancelled = try reducer.submit("session", "again", "auto");
    try reducer.observe(try deliveredEvent(scratch, 5, cancelled.message_ids[0]));
    try testing.expectEqual(Status.cancelling, (try reducer.cancel(cancelled.run_id)).status);
    try testing.expectEqual(Status.cancelling, (try reducer.cancel(cancelled.run_id)).status);
    try testing.expectEqual(@as(usize, 1), fake.interrupts);
    try reducer.observe(try nativeEvent(scratch, 6, "step.failed", "{\"assistantMessageID\":\"msg_a\",\"error\":{\"type\":\"aborted\",\"message\":\"Step interrupted\"}}"));
    try reducer.observe(try nativeEvent(scratch, 7, "execution.interrupted", "{\"reason\":\"user\"}"));
    try testing.expectEqual(Status.cancelled, (try reducer.cancel(cancelled.run_id)).status);

    const undelivered = try reducer.submit("session", "third", "auto");
    _ = try reducer.cancel(undelivered.run_id);
    try testing.expectEqual(@as(usize, 1), fake.cancelled.items.len);
    try testing.expectEqualStrings(undelivered.message_ids[0], fake.cancelled.items[0]);
    try testing.expectEqual(@as(usize, 1), fake.interrupts);
    try reducer.observe(try nativeEvent(scratch, 8, "inbox.cancelled", try std.fmt.allocPrint(scratch, "{{\"inboxID\":\"{s}\"}}", .{undelivered.message_ids[0]})));
    try testing.expectEqual(Status.cancelled, reducer.runStatus(undelivered.run_id).?);

    const refused = try reducer.submit("session", "fourth", "auto");
    try reducer.observe(try deliveredEvent(scratch, 9, refused.message_ids[0]));
    fake.interrupt_failure = .{ .message = "opencode native: HTTP 500", .api = true };
    try testing.expectError(error.CancellationAmbiguous, reducer.cancel(refused.run_id));
    const last = reducer.envelopes.items[reducer.envelopes.items.len - 1];
    try testing.expectEqualStrings("run.failed", last.object.get("type").?.string);
    try testing.expectEqualStrings("opencode_cancellation_ambiguous", errorCode(last));
    try testing.expectEqual(@as(?std.json.Value, null), payloadOf(last).get("settled_by"));
}

test "the queued input's delivery ends the previous run, and the promoted one fails with the transport's code" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, first.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try stepEnded(scratch, 3));
    const second = try reducer.submit("session", "later", "queue");
    try reducer.observe(try deliveredEvent(scratch, 4, second.message_ids[0]));
    try expectKinds(&reducer, &.{ "run.started", "run.completed", "run.started" });
    try reducer.transportFailed("gone");
    try expectKinds(&reducer, &.{ "run.started", "run.completed", "run.started", "run.failed" });
    try testing.expectEqualStrings(first.run_id, reducer.envelopes.items[1].object.get("run_id").?.string);
    try testing.expectEqualStrings(second.run_id, reducer.envelopes.items[3].object.get("run_id").?.string);
    try testing.expectEqualStrings("opencode_stream_failed", errorCode(reducer.envelopes.items[3]));
}

test "another client's delivered input ends this run and its turn is not taken" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, first.message_ids[0]));
    try reducer.observe(try deliveredEvent(scratch, 2, "msg_someone"));
    try reducer.observe(try nativeEvent(scratch, 3, "text.ended", "{\"assistantMessageID\":\"msg_b\",\"ordinal\":0,\"text\":\"theirs\"}"));
    try expectKinds(&reducer, &.{ "run.started", "run.completed" });
    try testing.expectEqualStrings("unknown", payloadOf(reducer.envelopes.items[1]).get("stop_reason").?.string);
}

test "the catalog needs degraded consent, answers only this session, and grows with step evidence" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session, .model = "fixture/base" }, fake.client());
    try reducer.open();
    try testing.expectError(error.DegradedWithoutOptIn, reducer.models("session", false));
    try testing.expectError(error.RunNotFound, reducer.models("other", true));
    try testing.expectEqualStrings(
        "{\"session_id\":\"session\",\"current_model_id\":\"fixture/base\",\"models\":[{\"id\":\"fixture/base\",\"provider_id\":\"fixture\",\"default\":true}]}",
        try gomarshal.marshal(scratch, try reducer.models("", true)),
    );
    try reducer.observe(try stepStarted(scratch, 1, "next"));
    try reducer.observe(try stepStarted(scratch, 2, "base"));
    try testing.expectEqualStrings(
        "{\"session_id\":\"session\",\"current_model_id\":\"fixture/base\",\"models\":[{\"id\":\"fixture/base\",\"provider_id\":\"fixture\",\"default\":true},{\"id\":\"fixture/next\",\"provider_id\":\"fixture\"}]}",
        try gomarshal.marshal(scratch, try reducer.models("session", true)),
    );
}

test "a token count converts the way the oracle converts it on arm64" {
    try testing.expectEqual(@as(u64, 2), tokenCount(2.9));
    try testing.expectEqual(@as(u64, 0), tokenCount(0));
    try testing.expectEqual(@as(u64, 0), tokenCount(-3));
    try testing.expectEqual(@as(u64, std.math.maxInt(u64)), tokenCount(1e20));
    try testing.expectEqual(@as(u64, 18446744073709549568), tokenCount(18446744073709549568.0));
}

fn queueAndSettle(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, first.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try nativeEvent(scratch, 3, "tool.input.started", "{\"assistantMessageID\":\"msg_a\",\"id\":\"c\",\"name\":\"t\"}"));
    try reducer.observe(try nativeEvent(scratch, 4, "tool.called", "{\"assistantMessageID\":\"msg_a\",\"id\":\"c\",\"input\":{\"b\":1,\"a\":2},\"executed\":true}"));
    try reducer.observe(try stepEnded(scratch, 5));
    const second = try reducer.submit("session", "later", "queue");
    try reducer.observe(try deliveredEvent(scratch, 6, second.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 7, "b"));
    try reducer.observe(try nativeEvent(scratch, 8, "text.ended", "{\"assistantMessageID\":\"msg_b\",\"ordinal\":0,\"text\":\"second\"}"));
    try reducer.observe(try stepEnded(scratch, 9));
    try reducer.observe(try succeeded(scratch, 10));
    if (reducer.envelopes.items.len != 8) return error.TestUnexpectedResult;
    for (reducer.envelopes.items) |envelope| _ = try gomarshal.marshal(scratch, envelope);
}

test "queueing and settling a promoted run propagates every allocation failure and leaks nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, queueAndSettle, .{});
}

fn partLabel(arena: std.mem.Allocator, part: std.json.Value) ![]const u8 {
    const kind = part.object.get("type").?.string;
    return std.mem.concat(arena, u8, &.{ kind, ":", part.object.get(kind).?.string });
}

test "text and reasoning deltas stream" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, admission.message_ids[0]));
    try reducer.observe(try nativeEvent(scratch, 0, "reasoning.delta", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":0,\"delta\":\"th\"}"));
    try reducer.observe(try nativeEvent(scratch, 0, "reasoning.delta", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":0,\"delta\":\"ink\"}"));
    try reducer.observe(try nativeEvent(scratch, 0, "text.delta", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":0,\"delta\":\"\"}"));
    try reducer.observe(try nativeEvent(scratch, 2, "reasoning.ended", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":0,\"text\":\"think\"}"));
    try reducer.observe(try nativeEvent(scratch, 0, "text.delta", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":1,\"delta\":\"x\"}"));
    try reducer.observe(try nativeEvent(scratch, 0, "text.delta", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":2,\"delta\":\"pl\"}"));
    try reducer.observe(try nativeEvent(scratch, 3, "text.ended", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":1,\"text\":\"hello\"}"));
    try reducer.observe(try nativeEvent(scratch, 4, "text.ended", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":2,\"text\":\"plain\"}"));
    try reducer.observe(try nativeEvent(scratch, 5, "text.ended", "{\"assistantMessageID\":\"msg_a\",\"ordinal\":2,\"text\":\"pl!\"}"));
    try reducer.observe(try stepEnded(scratch, 6));
    try reducer.observe(try succeeded(scratch, 7));
    try expectKinds(&reducer, &.{ "run.started", "content.delta", "content.delta", "content.delta", "content.delta", "content.delta", "content.delta", "run.completed" });
    const streamed = [_][]const u8{ "reasoning:th", "reasoning:ink", "text:x", "text:pl", "text:ain", "text:pl!" };
    for (streamed, reducer.envelopes.items[1..7]) |want, envelope| try testing.expectEqualStrings(want, try partLabel(scratch, payloadOf(envelope).get("part").?));
    const final = payloadOf(reducer.envelopes.items[7]).get("final_response").?.object.get("content").?.array.items;
    const whole = [_][]const u8{ "reasoning:think", "text:hello", "text:plain", "text:pl!" };
    try testing.expectEqual(whole.len, final.len);
    for (whole, final) |want, part| try testing.expectEqualStrings(want, try partLabel(scratch, part));
}

fn recordsOf(arena: std.mem.Allocator, text: []const u8) ![]const std.json.Value {
    var diag = native.Diagnostic{};
    const document = try native.parseDocument(arena, text, &diag);
    return document.array.items;
}

fn lastPayload(reducer: *Reducer) std.json.ObjectMap {
    return payloadOf(reducer.envelopes.items[reducer.envelopes.items.len - 1]);
}

fn finalLabels(arena: std.mem.Allocator, reducer: *Reducer) ![]const []const u8 {
    var labels: std.ArrayList([]const u8) = .empty;
    for (lastPayload(reducer).get("final_response").?.object.get("content").?.array.items) |part| try labels.append(arena, try partLabel(arena, part));
    return labels.items;
}

fn streamedLabels(arena: std.mem.Allocator, reducer: *Reducer) ![]const []const u8 {
    var labels: std.ArrayList([]const u8) = .empty;
    for (reducer.envelopes.items) |envelope| {
        if (!std.mem.eql(u8, envelope.object.get("type").?.string, "content.delta")) continue;
        try labels.append(arena, try partLabel(arena, payloadOf(envelope).get("part").?));
    }
    return labels.items;
}

fn expectLabels(want: []const []const u8, got: []const []const u8) !void {
    try testing.expectEqual(want.len, got.len);
    for (want, got) |expected, actual| try testing.expectEqualStrings(expected, actual);
}

test "a replayed record settles a run the record shows finished" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    const input = admission.message_ids[0];
    try reducer.observe(try deliveredEvent(scratch, 1, input));
    try reducer.observe(try nativeEvent(scratch, 0, "text.delta", "{\"assistantMessageID\":\"msg_a1\",\"ordinal\":0,\"delta\":\"po\"}"));
    const wanted = reducer.reconcileTarget();
    try testing.expectEqualStrings(input, wanted.oldest);
    try testing.expectEqualStrings(input, wanted.delivered);
    const records = try recordsOf(scratch, try std.fmt.allocPrint(scratch, "[{{\"id\":\"{s}\",\"type\":\"user\",\"text\":\"hi\"}},{{\"id\":\"msg_a1\",\"type\":\"assistant\",\"content\":[{{\"type\":\"text\",\"text\":\"pong\"}}],\"finish\":\"stop\",\"cost\":0.5,\"tokens\":{{\"input\":3,\"output\":2,\"reasoning\":0,\"cache\":{{\"read\":0,\"write\":0}}}},\"time\":{{\"created\":2,\"completed\":3}}}},{{\"id\":\"msg_idle\",\"type\":\"idle\",\"outcome\":\"succeeded\"}}]", .{input}));
    try testing.expect(recordSettled(records));
    try reducer.replay(records, wanted.delivered, true);
    try expectLabels(&.{ "text:po", "text:ng" }, try streamedLabels(scratch, &reducer));
    try testing.expectEqualStrings("run.completed", reducer.envelopes.items[reducer.envelopes.items.len - 1].object.get("type").?.string);
    try expectLabels(&.{"text:pong"}, try finalLabels(scratch, &reducer));
    try testing.expectEqual(@as(i64, 5), lastPayload(&reducer).get("usage").?.object.get("total_tokens").?.integer);
    try testing.expectEqualStrings("stop", lastPayload(&reducer).get("stop_reason").?.string);
}

test "a replayed record of a running turn lets the live stream finish it without repeating a part" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    const input = admission.message_ids[0];
    try reducer.observe(try deliveredEvent(scratch, 1, input));
    const records = try recordsOf(scratch, try std.fmt.allocPrint(scratch, "[{{\"id\":\"{s}\",\"type\":\"user\"}},{{\"id\":\"msg_a1\",\"type\":\"assistant\",\"content\":[{{\"type\":\"reasoning\",\"text\":\"hmm\",\"time\":{{\"created\":2,\"completed\":2}}}},{{\"type\":\"text\",\"text\":\"first\"}},{{\"type\":\"reasoning\",\"text\":\"deep\",\"time\":{{\"created\":2,\"completed\":2}}}},{{\"type\":\"text\",\"text\":\"\"}}],\"time\":{{\"created\":2}}}}]", .{input}));
    try testing.expect(!recordSettled(records));
    try reducer.replay(records, input, true);
    try reducer.observe(try nativeEvent(scratch, 3, "reasoning.ended", "{\"assistantMessageID\":\"msg_a1\",\"ordinal\":0,\"text\":\"hmm\"}"));
    try reducer.observe(try nativeEvent(scratch, 4, "text.ended", "{\"assistantMessageID\":\"msg_a1\",\"ordinal\":1,\"text\":\"second\"}"));
    try reducer.observe(try nativeEvent(scratch, 5, "step.ended", "{\"assistantMessageID\":\"msg_a1\",\"finish\":\"stop\",\"cost\":0,\"tokens\":{\"input\":1,\"output\":1,\"reasoning\":0,\"cache\":{\"read\":0,\"write\":0}}}"));
    try reducer.observe(try succeeded(scratch, 6));
    try expectLabels(&.{ "reasoning:hmm", "text:first", "reasoning:deep", "text:second" }, try streamedLabels(scratch, &reducer));
    try expectLabels(&.{ "reasoning:hmm", "text:first", "reasoning:deep", "text:second" }, try finalLabels(scratch, &reducer));
    try testing.expectEqual(@as(i64, 2), lastPayload(&reducer).get("usage").?.object.get("total_tokens").?.integer);
}

test "a replayed record starts a queued run it shows delivered and ignores the live repeat of that delivery" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = (try reducer.submit("session", "one", "auto")).message_ids[0];
    try reducer.observe(try deliveredEvent(scratch, 1, first));
    const second = (try reducer.submit("session", "two", "auto")).message_ids[0];
    const wanted = reducer.reconcileTarget();
    try testing.expectEqualStrings(first, wanted.oldest);
    const records = try recordsOf(scratch, try std.fmt.allocPrint(scratch, "[{{\"id\":\"{s}\",\"type\":\"user\"}},{{\"id\":\"msg_a1\",\"type\":\"assistant\",\"content\":[{{\"type\":\"text\",\"text\":\"one\"}}],\"finish\":\"stop\",\"cost\":0,\"time\":{{\"created\":2,\"completed\":3}}}},{{\"id\":\"msg_idle1\",\"type\":\"idle\",\"outcome\":\"succeeded\"}},{{\"id\":\"{s}\",\"type\":\"user\"}}]", .{ first, second }));
    try testing.expect(!recordSettled(records));
    try reducer.replay(records, wanted.delivered, true);
    try reducer.observe(try deliveredEvent(scratch, 10, second));
    try reducer.observe(try nativeEvent(scratch, 11, "text.ended", "{\"assistantMessageID\":\"msg_a2\",\"ordinal\":0,\"text\":\"two\"}"));
    try reducer.observe(try stepEnded(scratch, 12));
    try reducer.observe(try succeeded(scratch, 13));
    try expectKinds(&reducer, &.{ "run.started", "content.delta", "run.completed", "run.started", "content.delta", "run.completed" });
    try expectLabels(&.{"text:two"}, try finalLabels(scratch, &reducer));
}

test "a replayed record fails a run the record shows failed, and one the server stopped without recording" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const failing = (try reducer.submit("session", "one", "auto")).message_ids[0];
    try reducer.observe(try deliveredEvent(scratch, 1, failing));
    try reducer.replay(try recordsOf(scratch, try std.fmt.allocPrint(scratch, "[{{\"id\":\"{s}\",\"type\":\"user\"}},{{\"id\":\"msg_a1\",\"type\":\"assistant\",\"content\":[],\"finish\":\"error\",\"error\":{{\"type\":\"provider\",\"message\":\"bad request\",\"status\":400}},\"cost\":0,\"time\":{{\"created\":2,\"completed\":3}}}},{{\"id\":\"msg_idle\",\"type\":\"idle\",\"outcome\":\"failed\"}}]", .{failing})), failing, true);
    try testing.expectEqualStrings("opencode_step_failed", errorCode(reducer.envelopes.items[reducer.envelopes.items.len - 1]));
    try testing.expectEqualStrings("bad request", lastPayload(&reducer).get("error").?.object.get("message").?.string);
    const stopped = (try reducer.submit("session", "two", "auto")).message_ids[0];
    try reducer.observe(try deliveredEvent(scratch, 2, stopped));
    try reducer.replay(try recordsOf(scratch, try std.fmt.allocPrint(scratch, "[{{\"id\":\"{s}\",\"type\":\"user\"}}]", .{stopped})), stopped, false);
    try testing.expectEqualStrings("opencode_execution_interrupted", errorCode(reducer.envelopes.items[reducer.envelopes.items.len - 1]));
}

test "a second replay of the same record does not apply the first one again" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const input = (try reducer.submit("session", "hi", "auto")).message_ids[0];
    try reducer.observe(try deliveredEvent(scratch, 1, input));
    const records = try recordsOf(scratch, try std.fmt.allocPrint(scratch, "[{{\"id\":\"{s}\",\"type\":\"user\"}},{{\"id\":\"msg_a1\",\"type\":\"assistant\",\"content\":[{{\"type\":\"text\",\"text\":\"first\"}}],\"finish\":\"tool-calls\",\"cost\":1,\"tokens\":{{\"input\":2,\"output\":2,\"reasoning\":0,\"cache\":{{\"read\":0,\"write\":0}}}},\"time\":{{\"created\":2,\"completed\":3}}}}]", .{input}));
    try reducer.replay(records, input, true);
    try reducer.replay(records, input, true);
    try reducer.observe(try nativeEvent(scratch, 4, "text.ended", "{\"assistantMessageID\":\"msg_a2\",\"ordinal\":0,\"text\":\"second\"}"));
    try reducer.observe(try stepEnded(scratch, 5));
    try reducer.observe(try succeeded(scratch, 6));
    try expectLabels(&.{ "text:first", "text:second" }, try streamedLabels(scratch, &reducer));
    try expectLabels(&.{ "text:first", "text:second" }, try finalLabels(scratch, &reducer));
    try testing.expectEqual(@as(i64, 6), lastPayload(&reducer).get("usage").?.object.get("total_tokens").?.integer);
}

fn permissionEvent(arena: std.mem.Allocator, kind: []const u8, data: []const u8) !native.Event {
    var diag = native.Diagnostic{};
    const wire = try std.fmt.allocPrint(arena, "{{\"id\":\"evt_p\",\"type\":\"permission.{s}\",\"data\":{{\"sessionID\":\"" ++ native_session ++ "\",{s}}}}}", .{ kind, data[1 .. data.len - 1] });
    return native.decodeEvent(arena, wire, &diag);
}

fn gatedReducer(arena: *std.heap.ArenaAllocator, fake: *Fake) !struct { reducer: Reducer, run_id: []const u8, interaction: []const u8 } {
    const scratch = arena.allocator();
    var reducer = Reducer.init(arena, .{ .native_id = native_session, .participant = "user" }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "run it", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, admission.message_ids[0]));
    try reducer.observe(try nativeEvent(scratch, 2, "tool.input.started", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_1\",\"name\":\"shell\"}"));
    try reducer.observe(try nativeEvent(scratch, 3, "tool.called", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_1\",\"input\":{\"command\":\"echo hi\"},\"executed\":false}"));
    try reducer.observe(try permissionEvent(scratch, "asked", "{\"id\":\"per_1\",\"action\":\"shell\",\"resources\":[\"echo hi\"],\"save\":[\"echo *\"],\"source\":{\"type\":\"tool\",\"messageID\":\"msg_a\",\"id\":\"call_1\"}}"));
    const asked = reducer.envelopes.items[reducer.envelopes.items.len - 1];
    try testing.expectEqualStrings("action.permission.requested", asked.object.get("type").?.string);
    return .{ .reducer = reducer, .run_id = admission.run_id, .interaction = payloadOf(asked).get("interaction_id").?.string };
}

fn lastType(reducer: *Reducer) []const u8 {
    return reducer.envelopes.items[reducer.envelopes.items.len - 1].object.get("type").?.string;
}

test "the open permissions are listed in the order they were asked, until each is answered" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var gated = try gatedReducer(&arena, &fake);
    const reducer = &gated.reducer;
    try reducer.observe(try nativeEvent(scratch, 4, "tool.input.started", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_2\",\"name\":\"shell\"}"));
    try reducer.observe(try nativeEvent(scratch, 5, "tool.called", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_2\",\"input\":{\"command\":\"echo bye\"},\"executed\":false}"));
    try reducer.observe(try permissionEvent(scratch, "asked", "{\"id\":\"per_2\",\"action\":\"shell\",\"resources\":[\"echo bye\"],\"source\":{\"type\":\"tool\",\"messageID\":\"msg_a\",\"id\":\"call_2\"}}"));
    const second = payloadOf(reducer.envelopes.items[reducer.envelopes.items.len - 1]).get("interaction_id").?.string;
    try expectLabels(&.{ gated.interaction, second }, try reducer.openGates(scratch));
    try reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "once", true, "");
    try expectLabels(&.{second}, try reducer.openGates(scratch));
}

test "an asked permission becomes a request for its tool call, and an allowed one lets the run finish" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var gated = try gatedReducer(&arena, &fake);
    const reducer = &gated.reducer;
    const asked = reducer.envelopes.items[reducer.envelopes.items.len - 1];
    try testing.expectEqualStrings("shell: echo hi", payloadOf(asked).get("title").?.string);
    try testing.expectEqual(@as(usize, 3), payloadOf(asked).get("choices").?.array.items.len);
    try testing.expectEqualStrings(payloadOf(asked).get("tool_call_id").?.string, asked.object.get("tool_call_id").?.string);
    try testing.expectEqualStrings("user", payloadOf(asked).get("responded_by").?.string);
    try reducer.resolvePermission(gated.interaction, gated.run_id, "user", endpoint_id, "once", true, "");
    try testing.expectEqualStrings("per_1|once|", fake.replies.items[0]);
    try testing.expectError(error.InteractionResolved, reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "once", true, ""));
    const resolved = payloadOf(reducer.envelopes.items[reducer.envelopes.items.len - 1]);
    try testing.expectEqualStrings("resolved", resolved.get("outcome").?.string);
    try testing.expect(resolved.get("granted").?.bool);
    const before = reducer.envelopes.items.len;
    try reducer.observe(try permissionEvent(scratch, "replied", "{\"requestID\":\"per_1\",\"reply\":\"once\"}"));
    try testing.expectEqual(before, reducer.envelopes.items.len);
    try reducer.observe(try nativeEvent(scratch, 4, "tool.success", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_1\",\"content\":[{\"type\":\"text\",\"text\":\"hi\"}],\"executed\":false}"));
    try reducer.observe(try stepEnded(scratch, 5));
    try reducer.observe(try succeeded(scratch, 6));
    try testing.expectEqualStrings("run.completed", lastType(reducer));
}

test "a rejection without a reason ends the run as declined, and one with a reason passes it on" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var gated = try gatedReducer(&arena, &fake);
    try gated.reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "reject", false, "");
    try testing.expectEqualStrings("rejected", payloadOf(gated.reducer.envelopes.items[gated.reducer.envelopes.items.len - 1]).get("outcome").?.string);
    try gated.reducer.observe(try nativeEvent(scratch, 4, "execution.interrupted", "{\"reason\":\"shutdown\"}"));
    try testing.expectEqualStrings("opencode_permission_declined", errorCode(gated.reducer.envelopes.items[gated.reducer.envelopes.items.len - 1]));

    var other_arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer other_arena.deinit();
    var other_fake = Fake{};
    var reasoned = try gatedReducer(&other_arena, &other_fake);
    try reasoned.reducer.resolvePermission(reasoned.interaction, reasoned.run_id, "user", "", "reject", false, "use ls instead");
    try testing.expectEqualStrings("per_1|reject|use ls instead", other_fake.replies.items[0]);
    try reasoned.reducer.observe(try nativeEvent(other_arena.allocator(), 4, "execution.interrupted", "{\"reason\":\"shutdown\"}"));
    try testing.expectEqualStrings("opencode_execution_interrupted", errorCode(reasoned.reducer.envelopes.items[reasoned.reducer.envelopes.items.len - 1]));
}

test "a permission answered elsewhere or left open at the run's end is cancelled" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var gated = try gatedReducer(&arena, &fake);
    const reducer = &gated.reducer;
    try reducer.observe(try permissionEvent(scratch, "replied", "{\"requestID\":\"per_1\",\"reply\":\"always\"}"));
    const elsewhere = payloadOf(reducer.envelopes.items[reducer.envelopes.items.len - 1]);
    try testing.expectEqualStrings("cancelled", elsewhere.get("outcome").?.string);
    try testing.expectEqualStrings("opencode_permission_replied_elsewhere", elsewhere.get("reason").?.object.get("code").?.string);
    try reducer.observe(try nativeEvent(scratch, 4, "tool.input.started", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_2\",\"name\":\"shell\"}"));
    try reducer.observe(try nativeEvent(scratch, 5, "tool.called", "{\"assistantMessageID\":\"msg_a\",\"id\":\"call_2\",\"input\":{\"command\":\"rm x\"},\"executed\":false}"));
    try reducer.observe(try permissionEvent(scratch, "asked", "{\"id\":\"per_2\",\"action\":\"shell\",\"resources\":[\"rm x\"],\"source\":{\"type\":\"tool\",\"messageID\":\"msg_a\",\"id\":\"call_2\"}}"));
    try reducer.observe(try nativeEvent(scratch, 6, "execution.failed", "{\"error\":{\"type\":\"unknown\",\"message\":\"boom\"}}"));
    var settled: usize = 0;
    for (reducer.envelopes.items) |envelope| {
        if (!std.mem.eql(u8, envelope.object.get("type").?.string, "action.permission.resolved")) continue;
        const reason = payloadOf(envelope).get("reason") orelse continue;
        if (std.mem.eql(u8, reason.object.get("code").?.string, "run_settled")) settled += 1;
    }
    try testing.expectEqual(@as(usize, 1), settled);
    try testing.expectEqualStrings("run.failed", lastType(reducer));
}

test "an answer the server cannot take or that contradicts itself leaves the permission open" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var gated = try gatedReducer(&arena, &fake);
    const reducer = &gated.reducer;
    try testing.expectError(error.InvalidResolution, reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "maybe", true, ""));
    try testing.expectError(error.InvalidResolution, reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "reject", true, ""));
    try testing.expectError(error.WrongResponder, reducer.resolvePermission(gated.interaction, gated.run_id, "someone", "", "once", true, ""));
    try testing.expectError(error.InteractionNotFound, reducer.resolvePermission("interaction-none", gated.run_id, "user", "", "once", true, ""));
    fake.reply_failure = .{ .message = "connection refused" };
    try testing.expectError(error.ReplyFailed, reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "once", true, ""));
    try testing.expectEqualStrings("connection refused", reducer.reply_failure);
    fake.reply_failure = null;
    try reducer.resolvePermission(gated.interaction, gated.run_id, "user", "", "always", true, "");
    try testing.expectEqual(@as(usize, 2), fake.replies.items.len);
    try testing.expectEqualStrings("per_1|always|", fake.replies.items[1]);
}

test "a permission outside a tool call the run started fails the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session, .participant = "user" }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "run it", "auto");
    try reducer.observe(try deliveredEvent(scratch, 1, admission.message_ids[0]));
    try reducer.observe(try permissionEvent(scratch, "asked", "{\"id\":\"per_1\",\"action\":\"external_directory\",\"resources\":[\"/etc\"]}"));
    try testing.expectEqualStrings("opencode_permission_without_tool", errorCode(reducer.envelopes.items[reducer.envelopes.items.len - 1]));
}
