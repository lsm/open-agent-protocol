const std = @import("std");
const pollable = @import("builtin").os.tag != .windows;

pub const Token = struct {
    queue: *Queue,
    index: ?usize,
    previous: ?*Token = null,
    next: ?*Token = null,
    queued: bool = false,

    pub fn notify(ctx: *anyopaque) void {
        const self: *Token = @ptrCast(@alignCast(ctx));
        self.queue.push(self);
    }
};

pub const Queue = struct {
    io: std.Io,
    mutex: std.Io.Mutex = .init,
    head: ?*Token = null,
    tail: ?*Token = null,
    pipe: if (pollable) [2]std.Io.File.Handle else void,

    pub fn init(io: std.Io) !Queue {
        return .{
            .io = io,
            .pipe = if (comptime pollable) try std.Io.Threaded.pipe2(.{ .NONBLOCK = true, .CLOEXEC = true }) else {},
        };
    }

    pub fn deinit(self: *Queue) void {
        if (comptime pollable) {
            std.Io.Threaded.closeFd(self.pipe[0]);
            std.Io.Threaded.closeFd(self.pipe[1]);
        }
        self.* = undefined;
    }

    pub fn handle(self: *const Queue) ?std.Io.File.Handle {
        if (comptime pollable) return self.pipe[0];
        return null;
    }

    pub fn push(self: *Queue, token: *Token) void {
        self.mutex.lockUncancelable(self.io);
        defer self.mutex.unlock(self.io);
        if (token.queued) return;
        token.previous = self.tail;
        token.next = null;
        token.queued = true;
        if (self.tail) |tail| tail.next = token else self.head = token;
        self.tail = token;
        if (comptime pollable) {
            if (token.previous == null) {
                const byte = [_]u8{1};
                while (std.posix.errno(std.posix.system.write(self.pipe[1], &byte, 1)) == .INTR) {}
            }
        }
    }

    pub fn pop(self: *Queue) ?*Token {
        self.mutex.lockUncancelable(self.io);
        defer self.mutex.unlock(self.io);
        const token = self.head orelse return null;
        self.unlink(token);
        return token;
    }

    pub fn remove(self: *Queue, token: *Token) void {
        self.mutex.lockUncancelable(self.io);
        defer self.mutex.unlock(self.io);
        if (token.queued) self.unlink(token);
    }

    fn unlink(self: *Queue, token: *Token) void {
        if (token.previous) |previous| previous.next = token.next else self.head = token.next;
        if (token.next) |next| next.previous = token.previous else self.tail = token.previous;
        token.previous = null;
        token.next = null;
        token.queued = false;
        if (comptime pollable) {
            if (self.head == null) {
                var bytes: [64]u8 = undefined;
                while ((std.posix.read(self.pipe[0], &bytes) catch 0) > 0) {}
            }
        }
    }

    pub fn pending(self: *Queue) bool {
        self.mutex.lockUncancelable(self.io);
        defer self.mutex.unlock(self.io);
        return self.head != null;
    }
};
