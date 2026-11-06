const std = @import("std");
const builtin = @import("builtin");
const c = std.c;
const linux = std.os.linux;

pub const Interest = enum { read, write, read_write };
pub const Ready = struct {
    token: usize,
    read: bool = false,
    write: bool = false,
    closed: bool = false,
    failed: bool = false,
    wake: bool = false,
};
pub const Error = error{ KernelFailure, InvalidToken, EmptyBuffer };
const wake_token = std.math.maxInt(usize);
const Backend = switch (builtin.os.tag) {
    .macos => Kqueue,
    .linux => Epoll,
    else => @compileError("reactor requires kqueue or epoll"),
};

pub const Poller = struct {
    backend: Backend,

    pub fn init() Error!Poller {
        return .{ .backend = try Backend.init() };
    }

    pub fn deinit(self: *Poller) void {
        self.backend.deinit();
        self.* = undefined;
    }

    pub fn watch(self: *Poller, fd: c.fd_t, token: usize, interest: Interest) Error!void {
        if (token == wake_token) return error.InvalidToken;
        try self.backend.watch(fd, token, interest);
    }

    pub fn waitReturns(self: *const Poller) usize {
        return self.backend.returns.load(.acquire);
    }

    pub fn nativeHandle(self: *const Poller) c.fd_t {
        return self.backend.fd;
    }

    pub fn remove(self: *Poller, fd: c.fd_t) Error!void {
        try self.backend.remove(fd);
    }

    pub fn wake(self: *const Poller) Error!void {
        try self.backend.wake();
    }

    pub fn wait(self: *Poller, out: []Ready) Error!usize {
        if (out.len == 0) return error.EmptyBuffer;
        return self.backend.wait(out);
    }
};

const Kqueue = struct {
    fd: c.fd_t,
    returns: std.atomic.Value(usize) = .init(0),

    fn init() Error!Kqueue {
        const fd = c.kqueue();
        if (fd < 0) return error.KernelFailure;
        errdefer _ = c.close(fd);
        if (c.fcntl(fd, c.F.SETFD, @as(c_int, c.FD_CLOEXEC)) < 0) return error.KernelFailure;
        const self: Kqueue = .{ .fd = fd };
        try self.change(1, c.EVFILT.USER, c.EV.ADD | c.EV.CLEAR, 0, wake_token);
        return self;
    }

    fn deinit(self: *Kqueue) void {
        _ = c.close(self.fd);
    }

    fn change(self: *const Kqueue, ident: usize, filter: i16, flags: u16, fflags: u32, token: usize) Error!void {
        const changes = [_]c.Kevent{.{ .ident = ident, .filter = filter, .flags = flags, .fflags = fflags, .data = 0, .udata = token }};
        while (true) {
            const result = c.kevent(self.fd, &changes, 1, undefined, 0, null);
            switch (std.posix.errno(result)) {
                .SUCCESS => return,
                .INTR => continue,
                .NOENT => if (flags & c.EV.DELETE != 0) return else return error.KernelFailure,
                else => return error.KernelFailure,
            }
        }
    }

    fn watch(self: *Kqueue, fd: c.fd_t, token: usize, interest: Interest) Error!void {
        if (fd < 0) return error.KernelFailure;
        errdefer self.remove(fd) catch {};
        try self.change(@intCast(fd), c.EVFILT.READ, if (interest == .write) c.EV.DELETE else c.EV.ADD, 0, token);
        try self.change(@intCast(fd), c.EVFILT.WRITE, if (interest == .read) c.EV.DELETE else c.EV.ADD, 0, token);
    }

    fn remove(self: *Kqueue, fd: c.fd_t) Error!void {
        if (fd < 0) return error.KernelFailure;
        try self.change(@intCast(fd), c.EVFILT.READ, c.EV.DELETE, 0, 0);
        try self.change(@intCast(fd), c.EVFILT.WRITE, c.EV.DELETE, 0, 0);
    }

    fn wake(self: *const Kqueue) Error!void {
        try self.change(1, c.EVFILT.USER, 0, c.NOTE.TRIGGER, wake_token);
    }

    fn wait(self: *Kqueue, out: []Ready) Error!usize {
        var events: [64]c.Kevent = undefined;
        while (true) {
            const count = c.kevent(self.fd, undefined, 0, &events, @intCast(@min(events.len, out.len)), null);
            _ = self.returns.fetchAdd(1, .release);
            switch (std.posix.errno(count)) {
                .SUCCESS => {},
                .INTR => continue,
                else => return error.KernelFailure,
            }
            for (events[0..@intCast(count)], out[0..@intCast(count)]) |event, *ready| {
                ready.* = .{
                    .token = event.udata,
                    .read = event.filter == c.EVFILT.READ,
                    .write = event.filter == c.EVFILT.WRITE,
                    .closed = event.flags & c.EV.EOF != 0,
                    .failed = event.flags & c.EV.ERROR != 0 or ((event.filter == c.EVFILT.READ or event.filter == c.EVFILT.WRITE) and event.flags & c.EV.EOF != 0 and event.fflags != 0),
                    .wake = event.filter == c.EVFILT.USER,
                };
            }
            return @intCast(count);
        }
    }
};

const Epoll = struct {
    fd: c.fd_t,
    returns: std.atomic.Value(usize) = .init(0),
    wake_fd: c.fd_t,

    fn descriptor(result: usize) Error!c.fd_t {
        if (linux.errno(result) != .SUCCESS) return error.KernelFailure;
        return @intCast(result);
    }

    fn init() Error!Epoll {
        const fd = try descriptor(linux.epoll_create1(linux.EPOLL.CLOEXEC));
        errdefer _ = c.close(fd);
        const wake_fd = try descriptor(linux.eventfd(0, linux.EFD.CLOEXEC | linux.EFD.NONBLOCK));
        errdefer _ = c.close(wake_fd);
        var event: linux.epoll_event = .{ .events = linux.EPOLL.IN, .data = .{ .u64 = wake_token } };
        if (linux.errno(linux.epoll_ctl(fd, linux.EPOLL.CTL_ADD, wake_fd, &event)) != .SUCCESS) return error.KernelFailure;
        return .{ .fd = fd, .wake_fd = wake_fd };
    }

    fn deinit(self: *Epoll) void {
        _ = c.close(self.wake_fd);
        _ = c.close(self.fd);
    }

    fn watch(self: *Epoll, fd: c.fd_t, token: usize, interest: Interest) Error!void {
        var event: linux.epoll_event = .{
            .events = (switch (interest) {
                .read => @as(u32, linux.EPOLL.IN),
                .write => linux.EPOLL.OUT,
                .read_write => linux.EPOLL.IN | linux.EPOLL.OUT,
            }) | linux.EPOLL.RDHUP,
            .data = .{ .u64 = token },
        };
        switch (linux.errno(linux.epoll_ctl(self.fd, linux.EPOLL.CTL_ADD, fd, &event))) {
            .SUCCESS => return,
            .EXIST => if (linux.errno(linux.epoll_ctl(self.fd, linux.EPOLL.CTL_MOD, fd, &event)) == .SUCCESS) return,
            else => {},
        }
        return error.KernelFailure;
    }

    fn remove(self: *Epoll, fd: c.fd_t) Error!void {
        switch (linux.errno(linux.epoll_ctl(self.fd, linux.EPOLL.CTL_DEL, fd, null))) {
            .SUCCESS, .NOENT => return,
            else => return error.KernelFailure,
        }
    }

    fn wake(self: *const Epoll) Error!void {
        const one: u64 = 1;
        while (true) {
            const result = c.write(self.wake_fd, std.mem.asBytes(&one), @sizeOf(u64));
            switch (std.posix.errno(result)) {
                .SUCCESS, .AGAIN => return,
                .INTR => continue,
                else => return error.KernelFailure,
            }
        }
    }

    fn wait(self: *Epoll, out: []Ready) Error!usize {
        var events: [64]linux.epoll_event = undefined;
        while (true) {
            const count = linux.epoll_wait(self.fd, &events, @intCast(@min(events.len, out.len)), -1);
            _ = self.returns.fetchAdd(1, .release);
            switch (linux.errno(count)) {
                .SUCCESS => {},
                .INTR => continue,
                else => return error.KernelFailure,
            }
            for (events[0..count], out[0..count]) |event, *ready| {
                const is_wake = event.data.u64 == wake_token;
                if (is_wake) {
                    var value: u64 = 0;
                    while (true) {
                        const result = c.read(self.wake_fd, std.mem.asBytes(&value), @sizeOf(u64));
                        switch (std.posix.errno(result)) {
                            .SUCCESS, .AGAIN => break,
                            .INTR => continue,
                            else => return error.KernelFailure,
                        }
                    }
                }
                ready.* = .{
                    .token = @intCast(event.data.u64),
                    .read = !is_wake and event.events & linux.EPOLL.IN != 0,
                    .write = !is_wake and event.events & linux.EPOLL.OUT != 0,
                    .closed = event.events & (linux.EPOLL.HUP | linux.EPOLL.RDHUP) != 0,
                    .failed = event.events & linux.EPOLL.ERR != 0,
                    .wake = is_wake,
                };
            }
            return count;
        }
    }
};
