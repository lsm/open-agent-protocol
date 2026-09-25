const std = @import("std");
const builtin = @import("builtin");
const compat = @import("compat");

const is_windows = builtin.os.tag == .windows;

pub const atomic_chunk_bytes = 512;

pub const Error = error{ OutputStalled, BrokenPipe } || std.posix.UnexpectedError || std.posix.PollError || std.Thread.SpawnError;

const win = if (is_windows) struct {
    const HANDLE = std.os.windows.HANDLE;
    const BOOL = std.os.windows.BOOL;
    const DWORD = std.os.windows.DWORD;
    extern "kernel32" fn WriteFile(file: HANDLE, buffer: [*]const u8, len: DWORD, written: ?*DWORD, overlapped: ?*anyopaque) callconv(.winapi) BOOL;
    extern "kernel32" fn CancelSynchronousIo(thread: HANDLE) callconv(.winapi) BOOL;
    extern "kernel32" fn OpenThread(access: DWORD, inherit: BOOL, id: DWORD) callconv(.winapi) ?HANDLE;
    extern "kernel32" fn GetCurrentThreadId() callconv(.winapi) DWORD;
    extern "kernel32" fn Sleep(ms: DWORD) callconv(.winapi) void;
    extern "kernel32" fn CreatePipe(read: *HANDLE, write: *HANDLE, attributes: ?*anyopaque, size: DWORD) callconv(.winapi) BOOL;
    const thread_terminate: DWORD = 0x0001;
} else struct {};

const Watchdog = if (is_windows) struct {
    since: std.atomic.Value(u64) = .init(0),
    stalled: std.atomic.Value(bool) = .init(false),
    stopping: std.atomic.Value(bool) = .init(false),
    writer: ?win.HANDLE = null,
    thread: ?std.Thread = null,
} else struct {};

pub const Output = struct {
    file: std.Io.File,
    stall_ns: u64,
    watchdog: Watchdog = .{},
    stall_notice: ?Notice = null,

    pub const Notice = struct {
        file: std.Io.File,
        message: []const u8,
    };

    pub fn init(file: std.Io.File, stall_ns: u64) Output {
        return .{ .file = file, .stall_ns = stall_ns };
    }

    pub fn start(self: *Output) Error!void {
        if (!is_windows) return;
        const writer = win.OpenThread(win.thread_terminate, .FALSE, win.GetCurrentThreadId()) orelse return error.Unexpected;
        errdefer std.os.windows.CloseHandle(writer);
        self.watchdog.writer = writer;
        errdefer self.watchdog.writer = null;
        self.watchdog.thread = try std.Thread.spawn(.{}, watch, .{self});
    }

    pub fn deinit(self: *Output) void {
        if (is_windows) {
            self.watchdog.stopping.store(true, .release);
            if (self.watchdog.thread) |thread| thread.join();
            if (self.watchdog.writer) |writer| std.os.windows.CloseHandle(writer);
        }
        self.* = undefined;
    }

    pub fn writeAll(self: *Output, bytes: []const u8) Error!void {
        const written = if (is_windows) self.writeWindows(bytes) else self.writePosix(bytes);
        written catch |err| {
            if (err == error.OutputStalled) if (self.stall_notice) |notice| compat.stdio.writeAll(notice.file, notice.message) catch {};
            return err;
        };
    }

    fn writePosix(self: *Output, bytes: []const u8) Error!void {
        var at: usize = 0;
        var progressed = clock();
        while (at < bytes.len) {
            var fds = [_]std.posix.pollfd{.{ .fd = self.file.handle, .events = std.posix.POLL.OUT, .revents = 0 }};
            const ready = try std.posix.poll(&fds, 50);
            if (ready == 0 or fds[0].revents & std.posix.POLL.OUT == 0) {
                if (fds[0].revents & (std.posix.POLL.ERR | std.posix.POLL.HUP) != 0) return error.BrokenPipe;
                if (clock() -| progressed > self.stall_ns) return error.OutputStalled;
                continue;
            }
            const end = @min(bytes.len, at + atomic_chunk_bytes);
            const written = std.posix.system.write(self.file.handle, bytes[at..end].ptr, end - at);
            switch (std.posix.errno(written)) {
                .SUCCESS => {
                    at += @intCast(written);
                    progressed = clock();
                },
                .AGAIN => {
                    if (clock() -| progressed > self.stall_ns) return error.OutputStalled;
                    compat.time.sleepNs(std.time.ns_per_ms);
                },
                .INTR => {},
                .PIPE => return error.BrokenPipe,
                else => |err| return std.posix.unexpectedErrno(err),
            }
        }
    }

    fn writeWindows(self: *Output, bytes: []const u8) Error!void {
        var at: usize = 0;
        while (at < bytes.len) {
            const end = @min(bytes.len, at + atomic_chunk_bytes);
            var written: win.DWORD = 0;
            self.watchdog.since.store(clock() + 1, .release);
            const ok = win.WriteFile(self.file.handle, bytes[at..end].ptr, @intCast(end - at), &written, null);
            self.watchdog.since.store(0, .release);
            if (ok == .FALSE) {
                if (self.watchdog.stalled.load(.acquire)) return error.OutputStalled;
                return switch (std.os.windows.GetLastError()) {
                    .OPERATION_ABORTED => error.OutputStalled,
                    .BROKEN_PIPE, .NO_DATA => error.BrokenPipe,
                    else => |err| std.os.windows.unexpectedError(err),
                };
            }
            self.watchdog.stalled.store(false, .release);
            at += written;
        }
    }

    fn watch(self: *Output) void {
        while (!self.watchdog.stopping.load(.acquire)) {
            win.Sleep(20);
            const since = self.watchdog.since.load(.acquire);
            if (since == 0 or clock() -| since <= self.stall_ns) continue;
            self.watchdog.stalled.store(true, .release);
            _ = win.CancelSynchronousIo(self.watchdog.writer.?);
        }
    }
};

fn clock() u64 {
    return compat.time.monotonicNanos() catch 0;
}

const TestPipe = struct {
    read: std.Io.File,
    write: std.Io.File,

    fn open() !TestPipe {
        if (is_windows) {
            var read: win.HANDLE = undefined;
            var write: win.HANDLE = undefined;
            if (win.CreatePipe(&read, &write, null, 4096) == .FALSE) return error.Unexpected;
            return .{ .read = .{ .handle = read, .flags = .{ .nonblocking = false } }, .write = .{ .handle = write, .flags = .{ .nonblocking = false } } };
        }
        const pipe = try compat.stdio.pipe();
        try compat.stdio.setNonBlocking(pipe[1]);
        return .{ .read = pipe[0], .write = pipe[1] };
    }

    fn closeRead(self: *TestPipe) void {
        compat.stdio.close(self.read);
    }

    fn closeWrite(self: *TestPipe) void {
        compat.stdio.close(self.write);
    }
};

test "an output nobody reads fails with OutputStalled once the bound passes" {
    var pipe = try TestPipe.open();
    defer pipe.closeRead();
    defer pipe.closeWrite();
    var output = Output.init(pipe.write, 200 * std.time.ns_per_ms);
    try output.start();
    defer output.deinit();
    const filler = [_]u8{'x'} ** (256 * 1024);
    const began = clock();
    try std.testing.expectError(error.OutputStalled, output.writeAll(&filler));
    try std.testing.expect(clock() - began >= 200 * std.time.ns_per_ms);
}

test "an output whose reader closed fails with BrokenPipe" {
    var pipe = try TestPipe.open();
    defer pipe.closeWrite();
    pipe.closeRead();
    var output = Output.init(pipe.write, 5 * std.time.ns_per_s);
    try output.start();
    defer output.deinit();
    try std.testing.expectError(error.BrokenPipe, output.writeAll("line\n"));
}

const Drain = struct {
    file: std.Io.File,
    total: usize = 0,

    fn run(self: *Drain) void {
        var buffer: [4096]u8 = undefined;
        while (true) {
            const n = compat.stdio.read(self.file, &buffer) catch return;
            if (n == 0) return;
            self.total += n;
        }
    }
};

test "an output with a reader delivers every byte" {
    var pipe = try TestPipe.open();
    defer pipe.closeRead();
    var output = Output.init(pipe.write, 300 * std.time.ns_per_ms);
    try output.start();
    var drain = Drain{ .file = pipe.read };
    const reader = try std.Thread.spawn(.{}, Drain.run, .{&drain});
    const payload = [_]u8{'y'} ** (1024 * 1024);
    try output.writeAll(&payload);
    output.deinit();
    pipe.closeWrite();
    reader.join();
    try std.testing.expectEqual(payload.len, drain.total);
}

test "a pipe with less room than one atomic chunk counts as a stall, not a write error" {
    if (is_windows) return error.SkipZigTest;
    var pipe = try TestPipe.open();
    defer pipe.closeRead();
    defer pipe.closeWrite();
    const filler = [_]u8{'x'} ** 4096;
    while (true) {
        const written = std.posix.system.write(pipe.write.handle, &filler, filler.len);
        if (std.posix.errno(written) == .AGAIN) break;
        try std.testing.expectEqual(std.posix.E.SUCCESS, std.posix.errno(written));
    }
    while (true) {
        const written = std.posix.system.write(pipe.write.handle, &filler, 1);
        if (std.posix.errno(written) == .AGAIN) break;
    }
    var drained: [100]u8 = undefined;
    _ = try compat.stdio.read(pipe.read, &drained);
    var output = Output.init(pipe.write, 200 * std.time.ns_per_ms);
    try output.start();
    defer output.deinit();
    try std.testing.expectError(error.OutputStalled, output.writeAll(&filler));
}
