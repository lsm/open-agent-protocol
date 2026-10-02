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
