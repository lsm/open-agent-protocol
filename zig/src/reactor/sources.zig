const std = @import("std");
const builtin = @import("builtin");
const c = std.c;
const linux = std.os.linux;
const poller = @import("poller.zig");

pub const Kind = enum { timer, child };
pub const ChildWatch = enum { registered, exited };
pub const Error = poller.Error || std.mem.Allocator.Error || error{ DuplicateToken, DuplicateChild, InvalidPid };

pub const Sources = struct {
    const Entry = struct { kind: Kind, ident: usize, fd: ?c.fd_t = null };

    allocator: std.mem.Allocator,
    poll: *poller.Poller,
    entries: std.AutoHashMapUnmanaged(usize, Entry) = .empty,
    children: std.AutoHashMapUnmanaged(c.pid_t, usize) = .empty,

    pub fn init(allocator: std.mem.Allocator, poll: *poller.Poller) Sources {
        return .{ .allocator = allocator, .poll = poll };
    }

    pub fn deinit(self: *Sources) void {
        var iterator = self.entries.iterator();
        while (iterator.next()) |entry| self.unregister(entry.value_ptr.*) catch {};
        self.entries.deinit(self.allocator);
        self.children.deinit(self.allocator);
        self.* = undefined;
    }

    fn reserve(self: *Sources, token: usize) Error!void {
        if (token == std.math.maxInt(usize)) return error.InvalidToken;
        if (self.entries.contains(token)) return error.DuplicateToken;
        try self.entries.ensureUnusedCapacity(self.allocator, 1);
    }

    pub fn arm(self: *Sources, token: usize, milliseconds: u32) Error!void {
        try self.reserve(token);
        const after = @max(milliseconds, 1);
        if (builtin.os.tag == .macos) {
            _ = try self.change(.timer, token, c.EV.ADD | c.EV.ONESHOT, after, token);
            self.entries.putAssumeCapacity(token, .{ .kind = .timer, .ident = token });
        } else {
            const fd = try descriptor(linux.timerfd_create(.MONOTONIC, .{ .CLOEXEC = true, .NONBLOCK = true }));
            errdefer _ = c.close(fd);
            const spec: linux.itimerspec = .{
                .it_interval = .{ .sec = 0, .nsec = 0 },
                .it_value = .{ .sec = @intCast(after / 1000), .nsec = @intCast(@as(u64, after % 1000) * std.time.ns_per_ms) },
            };
            if (linux.errno(linux.timerfd_settime(fd, .{}, &spec, null)) != .SUCCESS) return error.KernelFailure;
            try self.poll.watch(fd, token, .read);
            self.entries.putAssumeCapacity(token, .{ .kind = .timer, .ident = token, .fd = fd });
        }
    }

    pub fn watchChild(self: *Sources, token: usize, pid: c.pid_t) Error!ChildWatch {
        if (pid <= 0) return error.InvalidPid;
        try self.reserve(token);
        if (self.children.contains(pid)) return error.DuplicateChild;
        try self.children.ensureUnusedCapacity(self.allocator, 1);
        if (builtin.os.tag == .macos) {
            if (!try self.change(.child, @intCast(pid), c.EV.ADD | c.EV.ONESHOT, 0, token)) return .exited;
            self.entries.putAssumeCapacity(token, .{ .kind = .child, .ident = @intCast(pid) });
        } else {
            const result = linux.pidfd_open(pid, 0);
            if (linux.errno(result) == .SRCH) return .exited;
            const fd = try descriptor(result);
            errdefer _ = c.close(fd);
            try self.poll.watch(fd, token, .read);
            self.entries.putAssumeCapacity(token, .{ .kind = .child, .ident = @intCast(pid), .fd = fd });
        }
        self.children.putAssumeCapacity(pid, token);
        return .registered;
    }

    pub fn cancel(self: *Sources, token: usize) Error!void {
        const removed = self.entries.fetchRemove(token) orelse return;
        const entry = removed.value;
        if (entry.kind == .child) _ = self.children.remove(@intCast(entry.ident));
        try self.unregister(entry);
    }

    pub fn take(self: *Sources, token: usize) Error!?Kind {
        const entry = self.entries.get(token) orelse return null;
        try self.cancel(token);
        return entry.kind;
    }

    fn unregister(self: *Sources, entry: Entry) Error!void {
        if (builtin.os.tag == .macos) {
            _ = try self.change(entry.kind, entry.ident, c.EV.DELETE, 0, 0);
        } else {
            const fd = entry.fd.?;
            defer _ = c.close(fd);
            try self.poll.remove(fd);
        }
    }

    fn descriptor(result: usize) Error!c.fd_t {
        if (linux.errno(result) != .SUCCESS) return error.KernelFailure;
        return @intCast(result);
    }

    fn change(self: *Sources, kind: Kind, ident: usize, flags: u16, data: u32, token: usize) Error!bool {
        const changes = [_]c.Kevent{.{
            .ident = ident,
            .filter = if (kind == .timer) c.EVFILT.TIMER else c.EVFILT.PROC,
            .flags = flags,
            .fflags = if (kind == .child) c.NOTE.EXIT else 0,
            .data = data,
            .udata = token,
        }};
        while (true) {
            const result = c.kevent(self.poll.nativeHandle(), &changes, 1, undefined, 0, null);
            switch (std.posix.errno(result)) {
                .SUCCESS => return true,
                .INTR => continue,
                .NOENT => if (flags & c.EV.DELETE != 0) return true else return error.KernelFailure,
                .SRCH => if (kind == .child) return false else return error.KernelFailure,
                else => return error.KernelFailure,
            }
        }
    }
};
