const std = @import("std");
const builtin = @import("builtin");

const is_windows = builtin.os.tag == .windows;

var signalled = std.atomic.Value(u32).init(0);
var close_grace_ms: u32 = 4500;
var wake_pipe: ?[2]std.Io.File.Handle = null;

const win = if (is_windows) struct {
    const BOOL = std.os.windows.BOOL;
    const DWORD = std.os.windows.DWORD;
    const HandlerRoutine = *const fn (DWORD) callconv(.winapi) BOOL;
    extern "kernel32" fn SetConsoleCtrlHandler(handler: ?HandlerRoutine, add: BOOL) callconv(.winapi) BOOL;
    extern "kernel32" fn Sleep(ms: DWORD) callconv(.winapi) void;
    const ctrl_c: DWORD = 0;
    const ctrl_break: DWORD = 1;
    const ctrl_close: DWORD = 2;
} else struct {};

pub fn install() error{Unexpected}!void {
    if (is_windows) {
        if (win.SetConsoleCtrlHandler(onConsole, .TRUE) == .FALSE) return error.Unexpected;
        return;
    }
    if (wake_pipe == null) wake_pipe = std.Io.Threaded.pipe2(.{ .NONBLOCK = true, .CLOEXEC = true }) catch return error.Unexpected;
    const action = std.posix.Sigaction{ .handler = .{ .handler = onSignal }, .mask = std.posix.sigemptyset(), .flags = 0 };
    std.posix.sigaction(std.posix.SIG.INT, &action, null);
    std.posix.sigaction(std.posix.SIG.TERM, &action, null);
}

pub fn received() bool {
    return signalled.load(.acquire) > 0;
}

pub fn count() u32 {
    drain();
    return signalled.load(.acquire);
}

pub fn reset() void {
    drain();
    signalled.store(0, .release);
}

pub fn readable() ?std.Io.File.Handle {
    const pipe = wake_pipe orelse return null;
    return pipe[0];
}

fn drain() void {
    if (comptime !is_windows) {
        const pipe = wake_pipe orelse return;
        var bytes: [64]u8 = undefined;
        while ((std.posix.read(pipe[0], &bytes) catch 0) > 0) {}
    }
}

fn onSignal(signal: std.posix.SIG) callconv(.c) void {
    _ = signal;
    const saved_errno = if (builtin.link_libc) std.c._errno().* else 0;
    defer if (builtin.link_libc) {
        std.c._errno().* = saved_errno;
    };
    _ = signalled.fetchAdd(1, .acq_rel);
    if (wake_pipe) |pipe| {
        const byte = [_]u8{1};
        while (std.posix.errno(std.posix.system.write(pipe[1], &byte, 1)) == .INTR) {}
    }
}

const onConsole = if (is_windows) struct {
    fn handle(kind: win.DWORD) callconv(.winapi) win.BOOL {
        switch (kind) {
            win.ctrl_c, win.ctrl_break => {},
            win.ctrl_close => {
                _ = signalled.fetchAdd(1, .acq_rel);
                win.Sleep(close_grace_ms);
                return .TRUE;
            },
            else => return .FALSE,
        }
        _ = signalled.fetchAdd(1, .acq_rel);
        return .TRUE;
    }
}.handle else {};

test "installing the handlers succeeds" {
    try install();
}

test "a SIGTERM marks the endpoint signalled until reset" {
    if (is_windows) return error.SkipZigTest;
    try install();
    defer reset();
    try std.testing.expect(!received());
    try std.posix.raise(std.posix.SIG.TERM);
    try std.testing.expect(received());
    try std.testing.expectEqual(@as(u32, 1), count());
    try std.posix.raise(std.posix.SIG.INT);
    try std.testing.expectEqual(@as(u32, 2), count());
    reset();
    try std.testing.expect(!received());
}

test "signals make a borrowed handle readable and counting drains the wake" {
    if (is_windows) return error.SkipZigTest;
    try install();
    defer reset();
    var watched = [_]std.posix.pollfd{.{ .fd = readable().?, .events = std.posix.POLL.IN, .revents = 0 }};
    try std.testing.expectEqual(@as(usize, 0), try std.posix.poll(&watched, 0));
    const saved_errno = if (builtin.link_libc) std.c._errno().* else 0;
    try std.posix.raise(std.posix.SIG.TERM);
    if (builtin.link_libc) try std.testing.expectEqual(saved_errno, std.c._errno().*);
    try std.testing.expectEqual(@as(usize, 1), try std.posix.poll(&watched, 0));
    try std.testing.expectEqual(@as(u32, 1), count());
    try std.testing.expectEqual(@as(usize, 0), try std.posix.poll(&watched, 0));
    try std.posix.raise(std.posix.SIG.INT);
    try std.testing.expectEqual(@as(u32, 2), count());
}

test "a console interrupt, break or close marks the endpoint signalled; logoff and shutdown pass to the next handler" {
    if (!is_windows) return error.SkipZigTest;
    defer reset();
    const grace = close_grace_ms;
    close_grace_ms = 0;
    defer close_grace_ms = grace;
    for ([_]win.DWORD{ win.ctrl_c, win.ctrl_break, win.ctrl_close }) |kind| {
        reset();
        try std.testing.expectEqual(win.BOOL.TRUE, onConsole(kind));
        try std.testing.expect(received());
    }
    for ([_]win.DWORD{ 5, 6 }) |kind| {
        reset();
        try std.testing.expectEqual(win.BOOL.FALSE, onConsole(kind));
        try std.testing.expect(!received());
    }
}
