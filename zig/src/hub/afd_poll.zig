const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");
const windows = std.os.windows;

pub const supported = builtin.os.tag == .windows;

pub const Events = packed struct(u32) {
    receive: bool = false,
    receive_expedited: bool = false,
    send: bool = false,
    disconnect: bool = false,
    abort: bool = false,
    local_close: bool = false,
    connect: bool = false,
    accept: bool = false,
    connect_fail: bool = false,
    reserved: u23 = 0,

    pub fn any(self: Events) bool {
        return @as(u32, @bitCast(self)) != 0;
    }
};

pub const Watch = struct {
    handle: windows.HANDLE,
    events: Events,
};

pub const HandleInfo = extern struct {
    handle: windows.HANDLE,
    events: Events,
    status: windows.NTSTATUS,
};

const Header = extern struct {
    timeout: i64,
    count: u32,
    exclusive: u32,
};

const header_alignment = std.mem.Alignment.of(HandleInfo);

pub const Poll = struct {
    allocator: std.mem.Allocator,
    buffer: []align(header_alignment.toByteUnits()) u8 = &.{},
    iosb: windows.IO_STATUS_BLOCK = undefined,
    device: ?windows.HANDLE = null,
    state: State = .idle,

    pub const State = enum { idle, pending, done };

    pub fn init(allocator: std.mem.Allocator) Poll {
        return .{ .allocator = allocator };
    }

    pub fn deinit(self: *Poll) void {
        self.cancel();
        self.allocator.free(self.buffer);
        self.* = undefined;
    }

    pub fn arm(self: *Poll, device: windows.HANDLE, watched: []const Watch) !void {
        std.debug.assert(self.state != .pending);
        self.state = .idle;
        const size = @sizeOf(Header) + watched.len * @sizeOf(HandleInfo);
        if (self.buffer.len < size) {
            const grown = try self.allocator.alignedAlloc(u8, header_alignment, size);
            self.allocator.free(self.buffer);
            self.buffer = grown;
        }
        self.header().* = .{ .timeout = std.math.maxInt(i64), .count = @intCast(watched.len), .exclusive = 0 };
        for (self.handles(watched.len), watched) |*info, watch| {
            info.* = .{ .handle = watch.handle, .events = watch.events, .status = .SUCCESS };
        }
        self.device = device;
        self.state = .pending;
        const status = windows.ntdll.NtDeviceIoControlFile(
            device,
            null,
            completed,
            self,
            &self.iosb,
            windows.IOCTL.AFD.POLL,
            self.buffer.ptr,
            @intCast(size),
            self.buffer.ptr,
            @intCast(size),
        );
        if (failedAtOnce(status)) {
            self.state = .idle;
            return error.PollFailed;
        }
    }

    pub fn ready(self: *const Poll) error{PollFailed}!?[]const HandleInfo {
        if (self.state != .done) return null;
        switch (self.iosb.u.Status) {
            .SUCCESS => {},
            .CANCELLED => return &.{},
            else => return error.PollFailed,
        }
        const signalled: *const Header = @ptrCast(self.buffer.ptr);
        return self.handlesConst(signalled.count);
    }

    pub fn cancel(self: *Poll) void {
        if (self.state != .pending) return;
        var cancel_iosb: windows.IO_STATUS_BLOCK = undefined;
        _ = windows.ntdll.NtCancelIoFileEx(self.device.?, &self.iosb, &cancel_iosb);
        while (self.state == .pending) _ = compat.time.sleepUntilCompletion(std.time.ns_per_s);
        self.state = .idle;
    }

    fn failedAtOnce(status: windows.NTSTATUS) bool {
        return @backingInt(status) >> 30 == 3;
    }

    fn header(self: *Poll) *Header {
        return @ptrCast(self.buffer.ptr);
    }

    fn handles(self: *Poll, count: usize) []HandleInfo {
        const first: [*]HandleInfo = @ptrCast(@alignCast(self.buffer.ptr + @sizeOf(Header)));
        return first[0..count];
    }

    fn handlesConst(self: *const Poll, count: usize) []const HandleInfo {
        const first: [*]const HandleInfo = @ptrCast(@alignCast(self.buffer.ptr + @sizeOf(Header)));
        return first[0..count];
    }

    fn completed(context: ?*anyopaque, _: *windows.IO_STATUS_BLOCK, _: windows.ULONG) align(4) callconv(.winapi) void {
        const self: *Poll = @ptrCast(@alignCast(context));
        self.state = .done;
    }
};

const testing = std.testing;

test "only an error status means the poll was refused before it could complete, because any other status still queues its APC" {
    try testing.expect(Poll.failedAtOnce(.INVALID_HANDLE));
    try testing.expect(Poll.failedAtOnce(.INVALID_PARAMETER));
    try testing.expect(!Poll.failedAtOnce(.SUCCESS));
    try testing.expect(!Poll.failedAtOnce(.PENDING));
    try testing.expect(!Poll.failedAtOnce(.BUFFER_OVERFLOW));
}

test "the poll's wire layout is the one AFD reads" {
    if (comptime !supported) return error.SkipZigTest;
    try testing.expectEqual(@as(usize, 16), @sizeOf(Header));
    try testing.expectEqual(@as(usize, 2 * @sizeOf(usize)), @sizeOf(HandleInfo));
    try testing.expectEqual(@as(u32, 0x1), @as(u32, @bitCast(Events{ .receive = true })));
    try testing.expectEqual(@as(u32, 0x4), @as(u32, @bitCast(Events{ .send = true })));
    try testing.expectEqual(@as(u32, 0x8), @as(u32, @bitCast(Events{ .disconnect = true })));
    try testing.expectEqual(@as(u32, 0x80), @as(u32, @bitCast(Events{ .accept = true })));
}

fn listen() !compat.net.Server {
    const local = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
    return compat.net.tcpListen(local, .{ .reuse_address = true });
}

fn settleWithin(poll: *const Poll, budget_ms: u64) bool {
    const started = compat.time.monotonicNanos() catch return false;
    while (poll.state == .pending) {
        const spent = (compat.time.monotonicNanos() catch return false) -| started;
        if (spent >= budget_ms * std.time.ns_per_ms) return false;
        _ = compat.time.sleepUntilCompletion(budget_ms * std.time.ns_per_ms - spent);
    }
    return true;
}

test "a connection arriving at a polled listener ends an alertable sleep with the listener ready to accept" {
    if (comptime !supported) return error.SkipZigTest;
    var server = try listen();
    defer compat.net.closeServer(&server);
    const listener = compat.net.serverHandle(&server);
    var poll = Poll.init(testing.allocator);
    defer poll.deinit();
    try poll.arm(listener, &.{.{ .handle = listener, .events = .{ .accept = true } }});
    try testing.expect(!settleWithin(&poll, 100));
    try testing.expectEqual(@as(?[]const HandleInfo, null), try poll.ready());

    var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
    defer client.close();
    try testing.expect(settleWithin(&poll, 5000));
    const signalled = (try poll.ready()).?;
    try testing.expectEqual(@as(usize, 1), signalled.len);
    try testing.expectEqual(listener, signalled[0].handle);
    try testing.expect(signalled[0].events.accept);
}

test "a polled connection stays pending while its peer is quiet and reports receive once the peer writes" {
    if (comptime !supported) return error.SkipZigTest;
    var server = try listen();
    defer compat.net.closeServer(&server);
    var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
    defer client.close();
    var accepted = try compat.net.acceptStream(&server);
    defer accepted.close();
    const listener = compat.net.serverHandle(&server);
    const connection = compat.net.streamHandle(&accepted);

    var poll = Poll.init(testing.allocator);
    defer poll.deinit();
    try poll.arm(listener, &.{
        .{ .handle = listener, .events = .{ .accept = true } },
        .{ .handle = connection, .events = .{ .receive = true, .disconnect = true, .abort = true } },
    });
    try testing.expect(!settleWithin(&poll, 150));

    try client.writeAll("x");
    try testing.expect(settleWithin(&poll, 5000));
    const signalled = (try poll.ready()).?;
    try testing.expectEqual(@as(usize, 1), signalled.len);
    try testing.expectEqual(connection, signalled[0].handle);
    try testing.expect(signalled[0].events.receive);
    try testing.expect(!signalled[0].events.accept);
}

test "a cancelled poll has settled by the time cancel returns, and can be armed again" {
    if (comptime !supported) return error.SkipZigTest;
    var server = try listen();
    defer compat.net.closeServer(&server);
    const listener = compat.net.serverHandle(&server);
    var poll = Poll.init(testing.allocator);
    defer poll.deinit();
    try poll.arm(listener, &.{.{ .handle = listener, .events = .{ .accept = true } }});
    poll.cancel();
    try testing.expectEqual(Poll.State.idle, poll.state);
    try testing.expectEqual(@as(?[]const HandleInfo, null), try poll.ready());

    try poll.arm(listener, &.{.{ .handle = listener, .events = .{ .accept = true } }});
    var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
    defer client.close();
    try testing.expect(settleWithin(&poll, 5000));
    try testing.expect((try poll.ready()).?[0].events.accept);
}
