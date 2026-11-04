const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");

pub const Kind = enum { permission, input };

pub const Request = struct {
    kind: Kind,
    tool_call_id: []const u8,
    tool_name: []const u8,
    arguments: []const u8,
    response: ?[]const u8 = null,
    published: bool = false,
};

const wait_fallback_ms: i64 = 1_000;

const Signal = struct {
    count: std.atomic.Value(u32) = std.atomic.Value(u32).init(0),

    fn seen(self: *const Signal) u32 {
        return self.count.load(.acquire);
    }

    fn raise(self: *Signal) void {
        _ = self.count.fetchAdd(1, .release);
        compat.fs.defaultIo().futexWake(u32, &self.count.raw, std.math.maxInt(u32));
    }

    fn wait(self: *Signal, seen_count: u32) void {
        const timeout: std.Io.Timeout = .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(wait_fallback_ms), .clock = .boot } };
        compat.fs.defaultIo().futexWaitTimeout(u32, &self.count.raw, seen_count, timeout) catch {};
    }
};

pub const Gate = struct {
    mutex: std.atomic.Mutex = .unlocked,
    request: ?*Request = null,
    cancelled: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    signal: Signal = .{},

    pub fn wake(self: *Gate) void {
        self.signal.raise();
    }

    pub fn cancel(self: *Gate) void {
        self.cancelled.store(true, .release);
        self.signal.raise();
    }

    pub fn lock(self: *Gate) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    }

    pub fn wait(self: *Gate, allocator: std.mem.Allocator, kind: Kind, tool_call_id: []const u8, tool_name: []const u8, arguments: []const u8, cancel_token: ?ai_types.CancelToken) ![]const u8 {
        const owned_call_id = try allocator.dupe(u8, tool_call_id);
        defer allocator.free(owned_call_id);
        const owned_name = try allocator.dupe(u8, tool_name);
        defer allocator.free(owned_name);
        const owned_arguments = try allocator.dupe(u8, arguments);
        defer allocator.free(owned_arguments);
        var request = Request{ .kind = kind, .tool_call_id = owned_call_id, .tool_name = owned_name, .arguments = owned_arguments };
        self.lock();
        if (self.request != null) {
            self.mutex.unlock();
            return error.InteractionAlreadyPending;
        }
        self.request = &request;
        self.mutex.unlock();
        defer {
            self.lock();
            self.request = null;
            self.mutex.unlock();
        }
        while (true) {
            const seen = self.signal.seen();
            self.lock();
            const response = request.response;
            self.mutex.unlock();
            if (response) |answer| return answer;
            if (self.cancelled.load(.acquire)) return error.Cancelled;
            if (cancel_token) |token| {
                if (token.isCancelled()) return error.Cancelled;
            }
            self.signal.wait(seen);
        }
    }
};

pub const CallGate = struct {
    mutex: std.atomic.Mutex = .unlocked,
    tool_call_id: ?[]u8 = null,
    answer: ?[]u8 = null,
    cancelled: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    signal: Signal = .{},

    fn lock(self: *CallGate) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    }

    pub fn cancel(self: *CallGate) void {
        self.cancelled.store(true, .release);
        self.signal.raise();
    }

    pub fn post(self: *CallGate, allocator: std.mem.Allocator, tool_call_id: []const u8, answer: []const u8) !void {
        const owned_id = try allocator.dupe(u8, tool_call_id);
        errdefer allocator.free(owned_id);
        const owned_answer = try allocator.dupe(u8, answer);
        self.lock();
        self.clearLocked(allocator);
        self.tool_call_id = owned_id;
        self.answer = owned_answer;
        self.mutex.unlock();
        self.signal.raise();
    }

    pub fn wait(self: *CallGate, allocator: std.mem.Allocator, tool_call_id: []const u8, cancel_token: ?ai_types.CancelToken) ![]u8 {
        while (true) {
            const seen = self.signal.seen();
            self.lock();
            if (self.answer) |answer| {
                if (std.mem.eql(u8, self.tool_call_id.?, tool_call_id)) {
                    allocator.free(self.tool_call_id.?);
                    self.tool_call_id = null;
                    self.answer = null;
                    self.mutex.unlock();
                    return answer;
                }
            }
            self.mutex.unlock();
            if (self.cancelled.load(.acquire)) return error.Cancelled;
            if (cancel_token) |token| {
                if (token.isCancelled()) return error.Cancelled;
            }
            self.signal.wait(seen);
        }
    }

    pub fn deinit(self: *CallGate, allocator: std.mem.Allocator) void {
        self.lock();
        defer self.mutex.unlock();
        self.clearLocked(allocator);
    }

    fn clearLocked(self: *CallGate, allocator: std.mem.Allocator) void {
        if (self.tool_call_id) |held| allocator.free(held);
        if (self.answer) |held| allocator.free(held);
        self.tool_call_id = null;
        self.answer = null;
    }
};

fn cancelledWaitProbe(allocator: std.mem.Allocator) !void {
    var gate = Gate{};
    gate.cancelled.store(true, .release);
    const response = gate.wait(allocator, .permission, "call", "tool", "{}", null) catch |err| {
        if (err == error.Cancelled) {
            try std.testing.expect(gate.request == null);
            return;
        }
        return err;
    };
    allocator.free(response);
    return error.UnexpectedResponse;
}

test "a cancelled interaction releases its request at every allocation failure" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, cancelledWaitProbe, .{});
}

test "a call answer posted before its tool waits is still the one the tool receives" {
    var gate = CallGate{};
    defer gate.deinit(std.testing.allocator);
    try gate.post(std.testing.allocator, "call-1", "{\"result\":1}");
    const answer = try gate.wait(std.testing.allocator, "call-1", null);
    defer std.testing.allocator.free(answer);
    try std.testing.expectEqualStrings("{\"result\":1}", answer);
    try std.testing.expect(gate.answer == null);
}

test "a cancelled call gate releases a waiting tool without an answer" {
    var gate = CallGate{};
    defer gate.deinit(std.testing.allocator);
    try gate.post(std.testing.allocator, "other-call", "{}");
    gate.cancelled.store(true, .release);
    try std.testing.expectError(error.Cancelled, gate.wait(std.testing.allocator, "call-1", null));
}

const prompt_ms: i64 = 500;

const ParkedGate = struct {
    gate: *Gate,
    token: ?ai_types.CancelToken = null,
    parked: std.atomic.Value(bool) = .init(false),
    outcome: ?anyerror = null,
    ended_ms: std.atomic.Value(i64) = .init(0),

    fn run(self: *ParkedGate) void {
        self.parked.store(true, .release);
        const answer = self.gate.wait(std.testing.allocator, .permission, "call", "tool", "{}", self.token) catch |err| {
            self.outcome = err;
            self.ended_ms.store(compat.time.nowMillis(), .release);
            return;
        };
        std.testing.allocator.free(answer);
        self.ended_ms.store(compat.time.nowMillis(), .release);
    }
};

fn parkAtGate(parked: *ParkedGate) !std.Thread {
    const thread = try std.Thread.spawn(.{}, ParkedGate.run, .{parked});
    const by = compat.time.nowMillis() + 5_000;
    while (compat.time.nowMillis() < by) : (compat.time.sleepMs(5)) {
        parked.gate.lock();
        const waiting = parked.gate.request != null;
        parked.gate.mutex.unlock();
        if (waiting) break;
    }
    compat.time.sleepMs(50);
    return thread;
}

test "an answer wakes a parked gate at once" {
    var gate = Gate{};
    var parked = ParkedGate{ .gate = &gate };
    const thread = try parkAtGate(&parked);
    const response = try std.testing.allocator.dupe(u8, "yes");
    const sent = compat.time.nowMillis();
    gate.lock();
    gate.request.?.response = response;
    gate.mutex.unlock();
    gate.wake();
    thread.join();
    try std.testing.expect(parked.outcome == null);
    try std.testing.expect(parked.ended_ms.load(.acquire) - sent < prompt_ms);
}

test "cancelling wakes a parked gate at once" {
    var gate = Gate{};
    var parked = ParkedGate{ .gate = &gate };
    const thread = try parkAtGate(&parked);
    const sent = compat.time.nowMillis();
    gate.cancel();
    thread.join();
    try std.testing.expectEqual(@as(?anyerror, error.Cancelled), parked.outcome);
    try std.testing.expect(parked.ended_ms.load(.acquire) - sent < prompt_ms);
    try std.testing.expect(gate.request == null);
}

test "a cancel seen only on the token still ends a parked gate" {
    var gate = Gate{};
    var cancelled = std.atomic.Value(bool).init(false);
    var parked = ParkedGate{ .gate = &gate, .token = .{ .cancelled = &cancelled } };
    const thread = try parkAtGate(&parked);
    const sent = compat.time.nowMillis();
    cancelled.store(true, .release);
    thread.join();
    try std.testing.expectEqual(@as(?anyerror, error.Cancelled), parked.outcome);
    try std.testing.expect(parked.ended_ms.load(.acquire) - sent <= wait_fallback_ms + prompt_ms);
}

const ParkedCall = struct {
    gate: *CallGate,
    outcome: ?anyerror = null,
    ended_ms: std.atomic.Value(i64) = .init(0),

    fn run(self: *ParkedCall) void {
        const answer = self.gate.wait(std.testing.allocator, "call", null) catch |err| {
            self.outcome = err;
            self.ended_ms.store(compat.time.nowMillis(), .release);
            return;
        };
        std.testing.allocator.free(answer);
        self.ended_ms.store(compat.time.nowMillis(), .release);
    }
};

test "a posted answer and a cancel each wake a parked call at once" {
    var gate = CallGate{};
    defer gate.deinit(std.testing.allocator);
    var answered = ParkedCall{ .gate = &gate };
    var thread = try std.Thread.spawn(.{}, ParkedCall.run, .{&answered});
    compat.time.sleepMs(100);
    var sent = compat.time.nowMillis();
    try gate.post(std.testing.allocator, "call", "{}");
    thread.join();
    try std.testing.expect(answered.outcome == null);
    try std.testing.expect(answered.ended_ms.load(.acquire) - sent < prompt_ms);

    var cancelled = ParkedCall{ .gate = &gate };
    thread = try std.Thread.spawn(.{}, ParkedCall.run, .{&cancelled});
    compat.time.sleepMs(100);
    sent = compat.time.nowMillis();
    gate.cancel();
    thread.join();
    try std.testing.expectEqual(@as(?anyerror, error.Cancelled), cancelled.outcome);
    try std.testing.expect(cancelled.ended_ms.load(.acquire) - sent < prompt_ms);
}
