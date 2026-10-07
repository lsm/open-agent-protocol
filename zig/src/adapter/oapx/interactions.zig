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

pub const Gate = struct {
    mutex: std.atomic.Mutex = .unlocked,
    request: ?*Request = null,
    cancelled: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),

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
            self.lock();
            const response = request.response;
            self.mutex.unlock();
            if (response) |answer| return answer;
            if (self.cancelled.load(.acquire)) return error.Cancelled;
            if (cancel_token) |token| {
                if (token.isCancelled()) return error.Cancelled;
            }
            compat.time.sleepNs(std.time.ns_per_ms);
        }
    }
};

pub const CallGate = struct {
    mutex: std.atomic.Mutex = .unlocked,
    tool_call_id: ?[]u8 = null,
    answer: ?[]u8 = null,
    cancelled: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),

    fn lock(self: *CallGate) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
    }

    pub fn post(self: *CallGate, allocator: std.mem.Allocator, tool_call_id: []const u8, answer: []const u8) !void {
        const owned_id = try allocator.dupe(u8, tool_call_id);
        errdefer allocator.free(owned_id);
        const owned_answer = try allocator.dupe(u8, answer);
        self.lock();
        defer self.mutex.unlock();
        self.clearLocked(allocator);
        self.tool_call_id = owned_id;
        self.answer = owned_answer;
    }

    pub fn wait(self: *CallGate, allocator: std.mem.Allocator, tool_call_id: []const u8, cancel_token: ?ai_types.CancelToken) ![]u8 {
        while (true) {
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
            compat.time.sleepNs(std.time.ns_per_ms);
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
