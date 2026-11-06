const std = @import("std");
const c = std.c;
const driver = @import("reactor.zig");
const pool = @import("pool.zig");
const kernel_probe = @import("test_support.zig");

const Signal = struct {
    mutex: c.pthread_mutex_t = .{},
    condition: c.pthread_cond_t = .{},
    value: bool = false,

    fn deinit(self: *Signal) void {
        _ = c.pthread_cond_destroy(&self.condition);
        _ = c.pthread_mutex_destroy(&self.mutex);
    }

    fn set(self: *Signal) void {
        _ = c.pthread_mutex_lock(&self.mutex);
        self.value = true;
        _ = c.pthread_cond_broadcast(&self.condition);
        _ = c.pthread_mutex_unlock(&self.mutex);
    }

    fn wait(self: *Signal) void {
        _ = c.pthread_mutex_lock(&self.mutex);
        defer _ = c.pthread_mutex_unlock(&self.mutex);
        while (!self.value) _ = c.pthread_cond_wait(&self.condition, &self.mutex);
    }
};

fn discard(_: *anyopaque, _: *driver.Reactor, _: driver.Event) void {}

test "an idle reactor and pool produce zero kernel wait returns" {
    var context: u8 = 0;
    const reactor = try driver.Reactor.create(std.testing.allocator, 4, 2, discard, &context);
    defer reactor.destroy() catch @panic("reactor did not stop");
    const workers = try pool.Pool.create(std.testing.allocator, 2, 2);
    defer workers.destroy();
    var blocked = false;
    for (0..1000) |_| {
        const tid = reactor.threadId();
        if (tid != 0 and try kernel_probe.isBlocked(reactor.thread, tid)) {
            blocked = true;
            break;
        }
        const grace: c.timespec = .{ .sec = 0, .nsec = std.time.ns_per_ms };
        _ = c.nanosleep(&grace, null);
    }
    try std.testing.expect(blocked);
    try std.testing.expectEqual(@as(usize, 0), reactor.poll.waitReturns());
    const hold: c.timespec = .{ .sec = 0, .nsec = 150 * std.time.ns_per_ms };
    try std.testing.expectEqual(@as(c_int, 0), c.nanosleep(&hold, null));
    try std.testing.expectEqual(@as(usize, 0), reactor.poll.waitReturns());
    std.debug.print("idle reactor: 150 ms, two idle pool workers, zero kernel wait returns\n", .{});
    try reactor.stop();
    try std.testing.expectEqual(@as(usize, 1), reactor.poll.waitReturns());
}

const Busy = struct {
    started: Signal = .{},
    release: Signal = .{},
    count: usize = 0,
    tid: std.Thread.Id = 0,

    fn block(context: *anyopaque, reactor: *driver.Reactor) void {
        const self: *Busy = @ptrCast(@alignCast(context));
        self.tid = std.Thread.getCurrentId();
        std.debug.assert(self.tid == reactor.threadId());
        self.started.set();
        self.release.wait();
    }

    fn countCommand(context: *anyopaque, _: *driver.Reactor) void {
        const self: *Busy = @ptrCast(@alignCast(context));
        self.count += 1;
    }
};

test "commands run on the owner and bounded backpressure preserves accepted work" {
    var busy: Busy = .{};
    defer busy.started.deinit();
    defer busy.release.deinit();
    const reactor = try driver.Reactor.create(std.testing.allocator, 2, 1, discard, &busy);
    defer reactor.destroy() catch @panic("reactor did not stop");
    defer busy.release.set();
    try reactor.post(.{ .run = Busy.block, .context = &busy });
    busy.started.wait();
    try reactor.post(.{ .run = Busy.countCommand, .context = &busy });
    try reactor.post(.{ .run = Busy.countCommand, .context = &busy });
    try std.testing.expectError(error.Full, reactor.post(.{ .run = Busy.countCommand, .context = &busy }));
    busy.release.set();
    try reactor.stop();
    try std.testing.expectEqual(@as(usize, 2), busy.count);
    try std.testing.expectEqual(reactor.threadId(), busy.tid);
    try std.testing.expectError(error.Closed, reactor.post(.{ .run = Busy.countCommand, .context = &busy }));
}

const Delivery = struct {
    done: Signal = .{},
    fd: c.fd_t,
    failure: ?anyerror = null,
    saw_io: bool = false,
    saw_timer: bool = false,
    commands: usize = 0,
    at_io: usize = 1000,

    fn begin(context: *anyopaque, reactor: *driver.Reactor) void {
        const self: *Delivery = @ptrCast(@alignCast(context));
        reactor.poll.watch(self.fd, 10, .read) catch |err| {
            self.fail(err);
            return;
        };
        reactor.sources.arm(20, 1) catch |err| {
            self.fail(err);
            return;
        };
        reactor.post(.{ .run = flood, .context = context }) catch |err| self.fail(err);
    }

    fn flood(context: *anyopaque, reactor: *driver.Reactor) void {
        const self: *Delivery = @ptrCast(@alignCast(context));
        self.commands += 1;
        if (self.commands < 1000) reactor.post(.{ .run = flood, .context = context }) catch |err| {
            if (err != error.Closed) self.fail(err);
        };
    }

    fn event(context: *anyopaque, reactor: *driver.Reactor, item: driver.Event) void {
        const self: *Delivery = @ptrCast(@alignCast(context));
        switch (item) {
            .io => |ready| {
                if (ready.token != 10) {
                    self.fail(error.UnexpectedToken);
                    return;
                }
                var byte: [1]u8 = undefined;
                if (c.read(self.fd, &byte, 1) != 1) {
                    self.fail(error.ReadFailed);
                    return;
                }
                reactor.poll.remove(self.fd) catch |err| {
                    self.fail(err);
                    return;
                };
                self.at_io = self.commands;
                self.saw_io = true;
            },
            .timer => |token| {
                if (token != 20) {
                    self.fail(error.UnexpectedToken);
                    return;
                }
                self.saw_timer = true;
            },
            .child => self.fail(error.UnexpectedChild),
        }
        if (self.saw_io and self.saw_timer) self.done.set();
    }

    fn fail(self: *Delivery, err: anyerror) void {
        self.failure = err;
        self.done.set();
    }
};

test "budgeted command dispatch allows I/O and one-shot timers to progress" {
    var fds: [2]c.fd_t = undefined;
    try std.testing.expectEqual(@as(c_int, 0), c.pipe(&fds));
    defer _ = c.close(fds[0]);
    defer _ = c.close(fds[1]);
    var delivery: Delivery = .{ .fd = fds[0] };
    defer delivery.done.deinit();
    const reactor = try driver.Reactor.create(std.testing.allocator, 4, 1, Delivery.event, &delivery);
    defer reactor.destroy() catch @panic("reactor did not stop");
    try std.testing.expectEqual(@as(isize, 1), c.write(fds[1], "x", 1));
    try reactor.post(.{ .run = Delivery.begin, .context = &delivery });
    delivery.done.wait();
    try reactor.stop();
    try std.testing.expectEqual(@as(?anyerror, null), delivery.failure);
    try std.testing.expect(delivery.saw_io and delivery.saw_timer);
    try std.testing.expect(delivery.at_io < 1000);
}

fn allocationProbe(allocator: std.mem.Allocator) !void {
    var context: u8 = 0;
    const reactor = try driver.Reactor.create(allocator, 4, 2, discard, &context);
    defer reactor.destroy() catch @panic("reactor did not stop");
}

test "reactor construction cleans up allocation failures" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, allocationProbe, .{});
}

const Bridge = struct {
    reactor: *driver.Reactor,
    done: Signal = .{},
    failed: std.atomic.Value(bool) = .init(false),
    producer: std.atomic.Value(std.Thread.Id) = .init(0),
    owner: std.Thread.Id = 0,
    count: usize = 0,

    fn produce(context: *anyopaque) void {
        const self: *Bridge = @ptrCast(@alignCast(context));
        self.producer.store(std.Thread.getCurrentId(), .release);
        self.reactor.post(.{ .run = consume, .context = self }) catch {
            self.failed.store(true, .release);
            self.done.set();
        };
    }

    fn consume(context: *anyopaque, _: *driver.Reactor) void {
        const self: *Bridge = @ptrCast(@alignCast(context));
        self.owner = std.Thread.getCurrentId();
        self.count += 1;
        if (self.count == 32) self.done.set();
    }
};

test "pool workers post completions to one reactor owner without losing commands" {
    var context: u8 = 0;
    var bridge: Bridge = .{ .reactor = undefined };
    defer bridge.done.deinit();
    const reactor = try driver.Reactor.create(std.testing.allocator, 64, 4, discard, &context);
    defer reactor.destroy() catch @panic("reactor did not stop");
    bridge.reactor = reactor;
    const workers = try pool.Pool.create(std.testing.allocator, 2, 32);
    defer workers.destroy();
    for (0..32) |_| try workers.post(.{ .run = Bridge.produce, .context = &bridge });
    bridge.done.wait();
    workers.shutdown();
    try reactor.stop();
    try std.testing.expect(!bridge.failed.load(.acquire));
    try std.testing.expectEqual(@as(usize, 32), bridge.count);
    try std.testing.expectEqual(reactor.threadId(), bridge.owner);
    try std.testing.expect(bridge.producer.load(.acquire) != bridge.owner);
}

const Child = struct {
    registered: Signal = .{},
    done: Signal = .{},
    pid: c.pid_t,
    failed: bool = false,
    seen: bool = false,

    fn begin(context: *anyopaque, reactor: *driver.Reactor) void {
        const self: *Child = @ptrCast(@alignCast(context));
        const watched = reactor.sources.watchChild(30, self.pid) catch {
            self.failed = true;
            self.registered.set();
            self.done.set();
            return;
        };
        self.failed = watched != .registered;
        self.registered.set();
        if (self.failed) self.done.set();
    }

    fn event(context: *anyopaque, _: *driver.Reactor, item: driver.Event) void {
        const self: *Child = @ptrCast(@alignCast(context));
        self.seen = switch (item) {
            .child => |token| token == 30,
            else => false,
        };
        self.done.set();
    }
};

test "the reactor classifies a real child exit as a child event" {
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
    var child: Child = .{ .pid = pid };
    defer child.registered.deinit();
    defer child.done.deinit();
    const reactor = try driver.Reactor.create(std.testing.allocator, 4, 2, Child.event, &child);
    defer reactor.destroy() catch @panic("reactor did not stop");
    try reactor.post(.{ .run = Child.begin, .context = &child });
    child.registered.wait();
    try std.testing.expectEqual(@as(isize, 1), c.write(fds[1], "x", 1));
    child.done.wait();
    try reactor.stop();
    try std.testing.expectEqual(pid, c.waitpid(pid, null, 0));
    reaped = true;
    try std.testing.expect(!child.failed);
    try std.testing.expect(child.seen);
}

test "invalid reactor capacities and budgets do not start a thread" {
    var context: u8 = 0;
    try std.testing.expectError(error.InvalidConfiguration, driver.Reactor.create(std.testing.allocator, 0, 1, discard, &context));
    try std.testing.expectError(error.InvalidConfiguration, driver.Reactor.create(std.testing.allocator, 1, 0, discard, &context));
}
