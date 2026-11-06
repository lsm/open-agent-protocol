const std = @import("std");
const c = std.c;
const poller = @import("poller.zig");
const sources = @import("sources.zig");

test "timers fire once, can be cancelled and can reuse a consumed token" {
    var poll = try poller.Poller.init();
    defer poll.deinit();
    var watches = sources.Sources.init(std.testing.allocator, &poll);
    defer watches.deinit();
    try watches.arm(1, 1);
    try std.testing.expectError(error.DuplicateToken, watches.arm(1, 1));
    var events: [1]poller.Ready = undefined;
    _ = try poll.wait(&events);
    try std.testing.expectEqual(@as(usize, 1), events[0].token);
    try std.testing.expectEqual(sources.Kind.timer, (try watches.take(1)).?);
    try std.testing.expectEqual(@as(?sources.Kind, null), try watches.take(1));
    try watches.arm(1, 1);
    try watches.cancel(1);
    try watches.cancel(1);
    try poll.wake();
    _ = try poll.wait(&events);
    try std.testing.expect(events[0].wake);
}

test "child watches deliver exit once and leave reaping to the caller" {
    var poll = try poller.Poller.init();
    defer poll.deinit();
    var watches = sources.Sources.init(std.testing.allocator, &poll);
    defer watches.deinit();
    var fds: [2]c.fd_t = undefined;
    try std.testing.expectEqual(@as(c_int, 0), c.pipe(&fds));
    defer _ = c.close(fds[0]);
    defer _ = c.close(fds[1]);
    const pid = c.fork();
    if (pid < 0) return error.ForkFailed;
    if (pid == 0) {
        var byte: [1]u8 = undefined;
        _ = c.read(fds[0], &byte, 1);
        c._exit(0);
    }
    var reaped = false;
    defer if (!reaped) {
        _ = c.kill(pid, c.SIG.KILL);
        _ = c.waitpid(pid, null, 0);
    };
    try std.testing.expectEqual(sources.ChildWatch.registered, try watches.watchChild(2, pid));
    try std.testing.expectError(error.DuplicateChild, watches.watchChild(3, pid));
    try watches.cancel(2);
    try std.testing.expectEqual(sources.ChildWatch.registered, try watches.watchChild(3, pid));
    try std.testing.expectEqual(@as(isize, 1), c.write(fds[1], "x", 1));
    var events: [1]poller.Ready = undefined;
    _ = try poll.wait(&events);
    try std.testing.expectEqual(@as(usize, 3), events[0].token);
    try std.testing.expect(!events[0].failed);
    try std.testing.expectEqual(sources.Kind.child, (try watches.take(3)).?);
    try std.testing.expectEqual(pid, c.waitpid(pid, null, 0));
    reaped = true;
    try std.testing.expectEqual(sources.ChildWatch.exited, try watches.watchChild(4, pid));
}

fn allocationProbe(allocator: std.mem.Allocator) !void {
    var poll = try poller.Poller.init();
    defer poll.deinit();
    var watches = sources.Sources.init(allocator, &poll);
    defer watches.deinit();
    try watches.arm(1, 1000);
    try watches.arm(2, 1000);
    try watches.cancel(1);
}

test "allocation failures release registered timers" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, allocationProbe, .{});
}

test "invalid source arguments do not allocate a kernel watch" {
    var poll = try poller.Poller.init();
    defer poll.deinit();
    var watches = sources.Sources.init(std.testing.allocator, &poll);
    defer watches.deinit();
    try std.testing.expectError(error.InvalidToken, watches.arm(std.math.maxInt(usize), 0));
    try std.testing.expectError(error.InvalidPid, watches.watchChild(1, 0));
}
