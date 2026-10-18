const std = @import("std");
const native = @import("native");

pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const cost_extension = "com.github.copilot.cost";
pub const routed_model_extension = "com.github.copilot.routed_model";

pub const Error = error{ InvalidSubmission, RunNotFound, RunActive, SessionClosed } || std.mem.Allocator.Error;

pub const Status = enum { queued, running, cancelling, completed, failed, cancelled };

pub const Options = struct {
    session_id: []const u8,
    native_id: []const u8,
    revision: []const u8,
    max_queued: usize = 8,
    counter: ?*u64 = null,
    now_ms: ?*const fn () i64 = null,
};

pub const Run = struct {
    id: []const u8,
    message_id: []const u8,
    native_message_id: []const u8 = "",
    interaction_id: []const u8 = "",
    status: Status = .queued,
    next: u64 = 1,
    started: bool = false,
    terminal: bool = false,
    cancel_requested: bool = false,
    parts: std.ArrayList([]const u8) = .empty,
    input_tokens: u64 = 0,
    output_tokens: u64 = 0,
    measured: bool = false,
    cost: f64 = 0,
    routed_model: []const u8 = "",
};

pub const Admission = struct {
    run_id: []const u8,
    message_id: []const u8,
    admission: []const u8,
    effective_delivery: []const u8,
    status: Status,
};

pub const Plan = enum { settled, abort, withdraw };

pub const Cancel = struct { status: Status, plan: Plan };

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: u64 = 0,
    clock: i64 = 0,
    closed: bool = false,
    runs: std.ArrayList(*Run) = .empty,
    current: ?*Run = null,
    envelopes: std.ArrayList(std.json.Value) = .empty,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options) Reducer {
        return .{ .arena = arena, .options = options };
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        if (self.options.now_ms) |clock| return clock();
        self.clock += 1;
        return self.clock;
    }

    fn nextID(self: *Reducer, kind: []const u8) std.mem.Allocator.Error![]const u8 {
        const counter = self.options.counter orelse &self.ids;
        counter.* += 1;
        return std.fmt.allocPrint(self.allocator(), "{s}-{d}", .{ kind, counter.* });
    }

    fn live(self: *Reducer) usize {
        var total: usize = 0;
        for (self.runs.items) |run| {
            if (!run.terminal) total += 1;
        }
        return total;
    }

    pub fn find(self: *Reducer, run_id: []const u8) ?*Run {
        for (self.runs.items) |run| {
            if (std.mem.eql(u8, run.id, run_id)) return run;
        }
        return null;
    }

    pub fn admit(self: *Reducer, session_id: []const u8) Error!Admission {
        if (self.closed) return error.SessionClosed;
        if (session_id.len == 0) return error.InvalidSubmission;
        if (!std.mem.eql(u8, session_id, self.options.session_id)) return error.RunNotFound;
        const busy = self.live();
        if (busy > self.options.max_queued) return error.RunActive;
        const run_id = try self.nextID("run");
        const message_id = try self.nextID("message");
        const run = try self.allocator().create(Run);
        run.* = .{ .id = run_id, .message_id = message_id, .status = if (busy == 0) .running else .queued };
        try self.runs.append(self.allocator(), run);
        if (busy == 0) return .{ .run_id = run.id, .message_id = run.message_id, .admission = "started", .effective_delivery = "start", .status = .running };
        return .{ .run_id = run.id, .message_id = run.message_id, .admission = "queued", .effective_delivery = "queue", .status = .queued };
    }

    pub fn bind(self: *Reducer, run_id: []const u8, native_message_id: []const u8) Error!void {
        const run = self.find(run_id) orelse return error.RunNotFound;
        run.native_message_id = try self.allocator().dupe(u8, native_message_id);
    }

    pub fn refuse(self: *Reducer, run_id: []const u8, code: []const u8, message: []const u8) Error!void {
        const run = self.find(run_id) orelse return error.RunNotFound;
        try self.fail(run, code, message);
    }

    pub fn cancel(self: *Reducer, run_id: []const u8) Error!Cancel {
        const run = self.find(run_id) orelse return error.RunNotFound;
        if (run.terminal) return .{ .status = run.status, .plan = .settled };
        run.cancel_requested = true;
        if (!run.started and run.native_message_id.len == 0) {
            try self.settle(run, "run.cancelled", try self.scoped(run));
            return .{ .status = .cancelled, .plan = .settled };
        }
        run.status = .cancelling;
        return .{ .status = .cancelling, .plan = if (run.started) .abort else .withdraw };
    }

    pub fn withdrawn(self: *Reducer, run_id: []const u8) Error!void {
        const run = self.find(run_id) orelse return error.RunNotFound;
        if (run.started or run.terminal) return;
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "reason", .{ .string = "Copilot withdrew the queued prompt" });
        try self.settle(run, "run.cancelled", payload);
    }

    pub fn observe(self: *Reducer, event: native.Event) Error!void {
        if (!std.mem.eql(u8, event.session_id, self.options.native_id)) {
            const run = self.current orelse return;
            return self.fail(run, "copilot_foreign_session", "an event for another Copilot session reached this one");
        }
        const kind = event.kind orelse return;
        switch (kind) {
            .user_message => try self.userMessage(event.data),
            .message_delta => {
                const run = self.current orelse return;
                const text = native.text(event.data, "deltaContent");
                if (text.len == 0) return;
                var part: std.json.ObjectMap = .empty;
                try part.put(self.allocator(), "type", .{ .string = "text" });
                try part.put(self.allocator(), "text", .{ .string = try self.allocator().dupe(u8, text) });
                var payload = try self.scoped(run);
                try payload.put(self.allocator(), "message_id", .{ .string = run.message_id });
                try payload.put(self.allocator(), "part", .{ .object = part });
                try self.emit(run, "content.delta", payload, false);
            },
            .message => {
                const run = self.current orelse return;
                const content = native.text(event.data, "content");
                if (content.len > 0) try run.parts.append(self.allocator(), try self.allocator().dupe(u8, content));
            },
            .usage => {
                const run = self.current orelse return;
                run.input_tokens +|= count(event.data, "inputTokens");
                run.output_tokens +|= count(event.data, "outputTokens");
                run.measured = true;
                if (event.data.get("cost")) |cost| run.cost += number(cost);
                const model = native.text(event.data, "model");
                if (model.len > 0) run.routed_model = try self.allocator().dupe(u8, model);
            },
            .session_idle => {
                const aborted = event.data.get("aborted");
                if (aborted != null and aborted.? == .bool and aborted.?.bool) {
                    for (self.runs.items) |run| {
                        if (run.started and run.cancel_requested and !run.terminal) try self.cancelled(run);
                    }
                    const run = self.current orelse return;
                    return self.fail(run, "copilot_aborted", "Copilot ended the busy period as aborted, without a cancel for this run");
                }
                const run = self.current orelse return;
                try self.complete(run);
            },
            .session_error => {
                const run = self.current orelse return;
                const said = native.text(event.data, "message");
                try self.fail(run, "copilot_session_error", if (said.len > 0) try self.allocator().dupe(u8, said) else "Copilot reported a session error");
            },
            else => {},
        }
    }

    fn userMessage(self: *Reducer, data: std.json.ObjectMap) Error!void {
        const message_id = native.text(data, "messageId");
        if (message_id.len == 0) return;
        for (self.runs.items) |run| {
            if (run.started or run.terminal or !std.mem.eql(u8, run.native_message_id, message_id)) continue;
            if (self.current) |previous| {
                if (!previous.terminal) {
                    if (previous.cancel_requested) try self.cancelled(previous) else try self.complete(previous);
                }
            }
            run.started = true;
            run.status = .running;
            run.interaction_id = try self.allocator().dupe(u8, native.text(data, "interactionId"));
            self.current = run;
            var payload = try self.scoped(run);
            try payload.put(self.allocator(), "status", .{ .string = "running" });
            return self.emit(run, "run.started", payload, false);
        }
    }

    fn cancelled(self: *Reducer, run: *Run) Error!void {
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "reason", .{ .string = "Copilot confirmed the abort" });
        try self.settle(run, "run.cancelled", payload);
    }

    fn complete(self: *Reducer, run: *Run) Error!void {
        var content = std.json.Array.init(self.allocator());
        for (run.parts.items) |text| {
            var part: std.json.ObjectMap = .empty;
            try part.put(self.allocator(), "type", .{ .string = "text" });
            try part.put(self.allocator(), "text", .{ .string = text });
            try content.append(.{ .object = part });
        }
        var response: std.json.ObjectMap = .empty;
        try response.put(self.allocator(), "id", .{ .string = run.message_id });
        try response.put(self.allocator(), "role", .{ .string = "assistant" });
        try response.put(self.allocator(), "content", if (content.items.len == 0) .{ .string = "" } else .{ .array = content });
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "final_response", .{ .object = response });
        try payload.put(self.allocator(), "stop_reason", .{ .string = "end_turn" });
        try self.settle(run, "run.completed", payload);
    }

    fn fail(self: *Reducer, run: *Run, code: []const u8, message: []const u8) Error!void {
        var failure: std.json.ObjectMap = .empty;
        try failure.put(self.allocator(), "code", .{ .string = code });
        try failure.put(self.allocator(), "message", .{ .string = message });
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "error", .{ .object = failure });
        try self.settle(run, "run.failed", payload);
    }

    fn settle(self: *Reducer, run: *Run, kind: []const u8, payload_in: std.json.ObjectMap) Error!void {
        var payload = payload_in;
        if (run.measured) {
            var usage: std.json.ObjectMap = .empty;
            try usage.put(self.allocator(), "input_tokens", try unsigned(self.allocator(), run.input_tokens));
            try usage.put(self.allocator(), "output_tokens", try unsigned(self.allocator(), run.output_tokens));
            try usage.put(self.allocator(), "total_tokens", try unsigned(self.allocator(), run.input_tokens +| run.output_tokens));
            try payload.put(self.allocator(), "usage", .{ .object = usage });
        }
        try self.emit(run, kind, payload, true);
        if (self.current == run) self.current = null;
    }

    fn scoped(self: *Reducer, run: *Run) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload: std.json.ObjectMap = .empty;
        try payload.put(self.allocator(), "session_id", .{ .string = self.options.session_id });
        try payload.put(self.allocator(), "run_id", .{ .string = run.id });
        return payload;
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool) Error!void {
        if (run.terminal) return;
        const id = try self.nextID("event");
        const sequence = run.next;
        run.next += 1;
        var envelope: std.json.ObjectMap = .empty;
        try envelope.put(self.allocator(), "protocol", .{ .string = protocol_name });
        try envelope.put(self.allocator(), "version", .{ .string = protocol_version });
        try envelope.put(self.allocator(), "profile", .{ .string = profile });
        try envelope.put(self.allocator(), "type", .{ .string = kind });
        try envelope.put(self.allocator(), "id", .{ .string = id });
        try envelope.put(self.allocator(), "payload", .{ .object = payload });
        try envelope.put(self.allocator(), "sequence", try unsigned(self.allocator(), sequence));
        try envelope.put(self.allocator(), "timestamp_ms", .{ .integer = self.now() });
        try envelope.put(self.allocator(), "session_id", .{ .string = self.options.session_id });
        try envelope.put(self.allocator(), "run_id", .{ .string = run.id });
        try envelope.put(self.allocator(), "capability_revision", .{ .string = self.options.revision });
        if (terminal) {
            if (try self.extensions(run)) |carried| try envelope.put(self.allocator(), "extensions", carried);
            run.terminal = true;
            run.status = if (std.mem.eql(u8, kind, "run.completed")) .completed else if (std.mem.eql(u8, kind, "run.cancelled")) .cancelled else .failed;
        }
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
    }

    fn extensions(self: *Reducer, run: *Run) std.mem.Allocator.Error!?std.json.Value {
        if (!run.measured) return null;
        var carried: std.json.ObjectMap = .empty;
        if (std.math.isFinite(run.cost)) {
            var cost: std.json.ObjectMap = .empty;
            try cost.put(self.allocator(), "premium_requests", .{ .float = run.cost });
            try carried.put(self.allocator(), cost_extension, .{ .object = cost });
        }
        if (run.routed_model.len > 0) {
            var routed: std.json.ObjectMap = .empty;
            try routed.put(self.allocator(), "model", .{ .string = run.routed_model });
            try carried.put(self.allocator(), routed_model_extension, .{ .object = routed });
        }
        if (carried.count() == 0) return null;
        return .{ .object = carried };
    }
};

fn unsigned(arena: std.mem.Allocator, value: u64) std.mem.Allocator.Error!std.json.Value {
    if (value <= std.math.maxInt(i64)) return .{ .integer = @intCast(value) };
    return .{ .number_string = try std.fmt.allocPrint(arena, "{d}", .{value}) };
}

fn number(value: std.json.Value) f64 {
    return switch (value) {
        .integer => |whole| @floatFromInt(whole),
        .float => |real| real,
        else => 0,
    };
}

fn count(data: std.json.ObjectMap, name: []const u8) u64 {
    const value = data.get(name) orelse return 0;
    const real = number(value);
    if (!(real > 0)) return 0;
    if (real >= 18446744073709551616.0) return std.math.maxInt(u64);
    return @intFromFloat(real);
}

const testing = std.testing;

fn feed(reducer: *Reducer, scratch: std.mem.Allocator, kind: []const u8, data: []const u8) !void {
    const frame = try std.fmt.allocPrint(scratch, "{{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{{\"sessionId\":\"ses\",\"event\":{{\"type\":\"{s}\",\"data\":{s}}}}}}}", .{ kind, data });
    try reducer.observe((try native.decodeEvent(try native.parseMessage(scratch, frame))).?);
}

fn kinds(reducer: *Reducer, scratch: std.mem.Allocator) ![]const u8 {
    var out: std.ArrayList(u8) = .empty;
    for (reducer.envelopes.items) |envelope| {
        if (out.items.len > 0) try out.append(scratch, ' ');
        try out.appendSlice(scratch, envelope.object.get("type").?.string);
        try out.append(scratch, ':');
        try out.appendSlice(scratch, envelope.object.get("run_id").?.string);
    }
    return out.items;
}

fn payloadOf(reducer: *Reducer, index: usize) std.json.ObjectMap {
    return reducer.envelopes.items[index].object.get("payload").?.object;
}

test "a prompt starts its run at its own user.message, streams, and completes at idle with its usage and cost" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "copilot-test-oap-v1" });
    const admission = try reducer.admit("s1");
    try testing.expectEqualStrings("started", admission.admission);
    try reducer.bind(admission.run_id, "m-ours");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m-other\",\"content\":\"not ours\"}");
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m-ours\",\"interactionId\":\"i1\",\"content\":\"hi\"}");
    try feed(&reducer, scratch, "assistant.message_delta", "{\"messageId\":\"a1\",\"deltaContent\":\"po\"}");
    try feed(&reducer, scratch, "assistant.message_delta", "{\"messageId\":\"a1\",\"deltaContent\":\"ng\"}");
    try feed(&reducer, scratch, "assistant.message", "{\"messageId\":\"a1\",\"content\":\"pong\",\"phase\":\"final_answer\"}");
    try feed(&reducer, scratch, "assistant.usage", "{\"model\":\"mai-code-1.1-flash\",\"inputTokens\":11952,\"outputTokens\":5,\"cost\":1.0,\"isAuto\":true}");
    try feed(&reducer, scratch, "session.title_changed", "{}");
    try feed(&reducer, scratch, "session.idle", "{\"mode\":\"interactive\"}");
    try testing.expectEqualStrings("run.started:run-1 content.delta:run-1 content.delta:run-1 run.completed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("po", payloadOf(&reducer, 1).get("part").?.object.get("text").?.string);
    const completed = payloadOf(&reducer, 3);
    try testing.expectEqualStrings("pong", completed.get("final_response").?.object.get("content").?.array.items[0].object.get("text").?.string);
    try testing.expectEqual(@as(i64, 11957), completed.get("usage").?.object.get("total_tokens").?.integer);
    const extensions = reducer.envelopes.items[3].object.get("extensions").?.object;
    try testing.expectEqualStrings("mai-code-1.1-flash", extensions.get(routed_model_extension).?.object.get("model").?.string);
    try testing.expectEqual(@as(f64, 1.0), extensions.get(cost_extension).?.object.get("premium_requests").?.float);
    try testing.expectEqual(@as(i64, 4), reducer.envelopes.items[3].object.get("sequence").?.integer);
    try testing.expectEqual(Status.completed, reducer.find("run-1").?.status);
}

test "a queued prompt starts at its own user.message, which settles the one before it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const first = try reducer.admit("s1");
    try reducer.bind(first.run_id, "m1");
    const second = try reducer.admit("s1");
    try testing.expectEqualStrings("queued", second.admission);
    try testing.expectEqualStrings("queue", second.effective_delivery);
    try reducer.bind(second.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\",\"delivery\":\"idle\"}");
    try feed(&reducer, scratch, "assistant.message", "{\"messageId\":\"a1\",\"content\":\"1\"}");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\",\"delivery\":\"queued\"}");
    try feed(&reducer, scratch, "assistant.message", "{\"messageId\":\"a2\",\"content\":\"second\"}");
    try feed(&reducer, scratch, "session.idle", "{}");
    try testing.expectEqualStrings("run.started:run-1 run.completed:run-1 run.started:run-3 run.completed:run-3", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("second", payloadOf(&reducer, 3).get("final_response").?.object.get("content").?.array.items[0].object.get("text").?.string);
    try testing.expect(payloadOf(&reducer, 1).get("usage") == null);
}

test "an abort this session asked for cancels the run, and one it did not ask for fails it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const asked = try reducer.admit("s1");
    try reducer.bind(asked.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    const cancel = try reducer.cancel(asked.run_id);
    try testing.expectEqual(Plan.abort, cancel.plan);
    try testing.expectEqual(Status.cancelling, cancel.status);
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    const unasked = try reducer.admit("s1");
    try reducer.bind(unasked.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    try testing.expectEqualStrings("run.started:run-1 run.cancelled:run-1 run.started:run-5 run.failed:run-5", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("copilot_aborted", payloadOf(&reducer, 3).get("error").?.object.get("code").?.string);
}

test "a queued run cancels without an abort, and a session error fails the running one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const running = try reducer.admit("s1");
    try reducer.bind(running.run_id, "m1");
    const waiting = try reducer.admit("s1");
    const cancel = try reducer.cancel(waiting.run_id);
    try testing.expectEqual(Plan.settled, cancel.plan);
    try testing.expectEqual(Status.cancelled, cancel.status);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "session.error", "{\"errorType\":\"rate_limit\",\"message\":\"quota exceeded\"}");
    try testing.expectEqualStrings("run.cancelled:run-3 run.started:run-1 run.failed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("quota exceeded", payloadOf(&reducer, 2).get("error").?.object.get("message").?.string);
    try testing.expectError(error.RunNotFound, reducer.admit("elsewhere"));
    try testing.expectError(error.InvalidSubmission, reducer.admit(""));
}

test "admission refuses once the queue is full" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r", .max_queued = 1 });
    _ = try reducer.admit("s1");
    _ = try reducer.admit("s1");
    try testing.expectError(error.RunActive, reducer.admit("s1"));
}

test "a cancelled run whose next prompt arrives before the aborted idle settles cancelled, not completed" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const first = try reducer.admit("s1");
    try reducer.bind(first.run_id, "m1");
    const second = try reducer.admit("s1");
    try reducer.bind(second.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try testing.expectEqual(Plan.abort, (try reducer.cancel(first.run_id)).plan);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    try testing.expectEqualStrings("run.started:run-1 run.cancelled:run-1 run.started:run-3 run.failed:run-3", try kinds(&reducer, scratch));
}

test "an event for another Copilot session fails the running run rather than folding into it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    const foreign = "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"other\",\"event\":{\"type\":\"assistant.message_delta\",\"data\":{\"messageId\":\"x\",\"deltaContent\":\"theirs\"}}}}";
    try reducer.observe((try native.decodeEvent(try native.parseMessage(scratch, foreign))).?);
    try testing.expectEqualStrings("run.started:run-1 run.failed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("copilot_foreign_session", payloadOf(&reducer, 1).get("error").?.object.get("code").?.string);
}

test "a prompt already sent but not yet started is withdrawn, and settles cancelled once Copilot withdraws it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const running = try reducer.admit("s1");
    try reducer.bind(running.run_id, "m1");
    const waiting = try reducer.admit("s1");
    try reducer.bind(waiting.run_id, "m2");
    const cancel = try reducer.cancel(waiting.run_id);
    try testing.expectEqual(Plan.withdraw, cancel.plan);
    try testing.expectEqual(Status.cancelling, cancel.status);
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try reducer.withdrawn(waiting.run_id);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try testing.expectEqualStrings("run.cancelled:run-3", try kinds(&reducer, scratch));
}

test "a cost that overflows is left out rather than written as a non-JSON number" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "assistant.usage", "{\"model\":\"m\",\"inputTokens\":1,\"outputTokens\":1,\"cost\":1e308}");
    try feed(&reducer, scratch, "assistant.usage", "{\"model\":\"m\",\"inputTokens\":1,\"outputTokens\":1,\"cost\":1e308}");
    try feed(&reducer, scratch, "session.idle", "{}");
    const extensions = reducer.envelopes.items[1].object.get("extensions").?.object;
    try testing.expect(extensions.get(cost_extension) == null);
    try testing.expectEqualStrings("m", extensions.get(routed_model_extension).?.object.get("model").?.string);
}
