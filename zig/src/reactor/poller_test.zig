const std = @import("std");
const c = std.c;
const probe = @import("test_support.zig");
const poller = @import("poller.zig");

fn pipe() ![2]c.fd_t {
    var fds: [2]c.fd_t = undefined;
    if (c.pipe(&fds) != 0) return error.PipeFailed;
    return fds;
}

test "wake is coalesced and a one-event buffer is sufficient" {
    var p = try poller.Poller.init();
    defer p.deinit();
    try p.wake();
    try p.wake();
    var events: [1]poller.Ready = undefined;
    try std.testing.expectEqual(@as(usize, 1), try p.wait(&events));
    try std.testing.expect(events[0].wake);
    try p.wake();
    try std.testing.expectEqual(@as(usize, 1), try p.wait(&events));
    try std.testing.expect(events[0].wake);
}

test "readiness retains the latest token and unregistering removes a live watch" {
    var p = try poller.Poller.init();
    defer p.deinit();
    const fds = try pipe();
    defer _ = c.close(fds[0]);
    defer _ = c.close(fds[1]);
    try p.watch(fds[0], 10, .read);
    try p.watch(fds[0], 20, .read);
    try std.testing.expectEqual(@as(isize, 1), c.write(fds[1], "x", 1));
    var events: [1]poller.Ready = undefined;
    try std.testing.expectEqual(@as(usize, 1), try p.wait(&events));
    try std.testing.expectEqual(@as(usize, 20), events[0].token);
    try std.testing.expect(events[0].read);
    try p.remove(fds[0]);
    try p.remove(fds[0]);
    try p.wake();
    _ = try p.wait(&events);
    try std.testing.expect(events[0].wake);
}

test "write readiness is delivered" {
    var p = try poller.Poller.init();
    defer p.deinit();
    const fds = try pipe();
    defer _ = c.close(fds[1]);
    try p.watch(fds[1], 33, .write);
    var events: [1]poller.Ready = undefined;
    _ = try p.wait(&events);
    try std.testing.expect(events[0].write);
    try p.remove(fds[1]);
    try p.watch(fds[0], 44, .read);
    try p.remove(fds[0]);
    _ = c.close(fds[0]);
}

test "invalid input reports an error without entering wait" {
    var p = try poller.Poller.init();
    defer p.deinit();
    try std.testing.expectError(error.EmptyBuffer, p.wait(&.{}));
    try std.testing.expectError(error.InvalidToken, p.watch(0, std.math.maxInt(usize), .read));
    try std.testing.expectError(error.KernelFailure, p.watch(-1, 1, .read));
}

test "peer closure is delivered even without buffered bytes" {
    var p = try poller.Poller.init();
    defer p.deinit();
    const fds = try pipe();
    defer _ = c.close(fds[0]);
    try p.watch(fds[0], 55, .read);
    _ = c.close(fds[1]);
    var events: [1]poller.Ready = undefined;
    _ = try p.wait(&events);
    try std.testing.expectEqual(@as(usize, 55), events[0].token);
    try std.testing.expect(events[0].closed);
}

const Idle = struct {
    p: *poller.Poller,
    entered: std.atomic.Value(bool) = .init(false),
    tid: std.Thread.Id = undefined,
    returned: std.atomic.Value(bool) = .init(false),
    failed: bool = false,

    fn run(self: *Idle) void {
        var events: [1]poller.Ready = undefined;
        self.tid = std.Thread.getCurrentId();
        self.entered.store(true, .release);
        const count = self.p.wait(&events) catch {
            self.failed = true;
            self.returned.store(true, .release);
            return;
        };
        self.failed = count != 1 or !events[0].wake;
        self.returned.store(true, .release);
    }
};

test "an idle poller stays blocked until an explicit wake" {
    var p = try poller.Poller.init();
    defer p.deinit();
    var idle: Idle = .{ .p = &p };
    {
        const thread = try std.Thread.spawn(.{}, Idle.run, .{&idle});
        defer {
            p.wake() catch @panic("idle probe could not stop");
            thread.join();
        }
        while (!idle.entered.load(.acquire)) std.atomic.spinLoopHint();
        var blocked = false;
        for (0..1000) |_| {
            if (try probe.isBlocked(thread, idle.tid)) {
                blocked = true;
                break;
            }
            const grace: c.timespec = .{ .sec = 0, .nsec = std.time.ns_per_ms };
            _ = c.nanosleep(&grace, null);
        }
        try std.testing.expect(blocked);
        const delay: c.timespec = .{ .sec = 0, .nsec = 150 * std.time.ns_per_ms };
        try std.testing.expectEqual(@as(c_int, 0), c.nanosleep(&delay, null));
        try std.testing.expect(!idle.returned.load(.acquire));
        try std.testing.expectEqual(@as(usize, 0), p.waitReturns());
        std.debug.print("idle poller: 150 ms, zero wait returns before explicit wake\n", .{});
    }
    try std.testing.expect(idle.returned.load(.acquire));
    try std.testing.expect(!idle.failed);
}

test "a refused nonblocking connection reports socket failure" {
    const reserved = c.socket(c.AF.INET, c.SOCK.STREAM, 0);
    if (reserved < 0) return error.SocketFailed;
    defer _ = c.close(reserved);
    var address: c.sockaddr.in = .{ .port = 0, .addr = std.mem.nativeToBig(u32, 0x7f000001) };
    try std.testing.expectEqual(@as(c_int, 0), c.bind(reserved, @ptrCast(&address), @sizeOf(@TypeOf(address))));
    var size: c.socklen_t = @sizeOf(@TypeOf(address));
    try std.testing.expectEqual(@as(c_int, 0), c.getsockname(reserved, @ptrCast(&address), &size));
    const socket = c.socket(c.AF.INET, c.SOCK.STREAM, 0);
    if (socket < 0) return error.SocketFailed;
    defer _ = c.close(socket);
    const flags = c.fcntl(socket, c.F.GETFL);
    if (flags < 0) return error.SocketFailed;
    const nonblock: c_int = @bitCast(c.O{ .NONBLOCK = true });
    if (c.fcntl(socket, c.F.SETFL, flags | nonblock) < 0) return error.SocketFailed;
    try std.testing.expect(c.connect(socket, @ptrCast(&address), size) < 0);
    var p = try poller.Poller.init();
    defer p.deinit();
    try p.watch(socket, 77, .write);
    var events: [1]poller.Ready = undefined;
    _ = try p.wait(&events);
    try std.testing.expectEqual(@as(usize, 77), events[0].token);
    try std.testing.expect(events[0].failed);
}

comptime {
    _ = @import("sources_test.zig");
    _ = @import("pool_test.zig");
    _ = @import("reactor_test.zig");
}

test "duplex interest observes reads and writes and switching interest removes the old watch" {
    var fds: [2]c.fd_t = undefined;
    try std.testing.expectEqual(@as(c_int, 0), c.socketpair(c.AF.UNIX, c.SOCK.STREAM, 0, &fds));
    defer _ = c.close(fds[0]);
    defer _ = c.close(fds[1]);
    var poll = try poller.Poller.init();
    defer poll.deinit();
    try std.testing.expectEqual(@as(isize, 1), c.write(fds[1], "x", 1));
    try poll.watch(fds[0], 80, .read_write);
    var events: [2]poller.Ready = undefined;
    const count = try poll.wait(&events);
    var read = false;
    var write = false;
    for (events[0..count]) |event| {
        try std.testing.expectEqual(@as(usize, 80), event.token);
        read = read or event.read;
        write = write or event.write;
    }
    try std.testing.expect(read and write);
    var byte: [1]u8 = undefined;
    try std.testing.expectEqual(@as(isize, 1), c.read(fds[0], &byte, 1));
    try poll.watch(fds[0], 81, .read);
    try poll.wake();
    try std.testing.expectEqual(@as(usize, 1), try poll.wait(&events));
    try std.testing.expect(events[0].wake);
    try poll.watch(fds[0], 82, .write);
    _ = try poll.wait(&events);
    try std.testing.expect(events[0].write);
    try std.testing.expectEqual(@as(usize, 82), events[0].token);
}
