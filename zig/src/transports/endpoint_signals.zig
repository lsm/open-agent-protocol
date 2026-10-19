const std = @import("std");
const builtin = @import("builtin");

const is_windows = builtin.os.tag == .windows;

var signalled = std.atomic.Value(u32).init(0);
var close_grace_ms: u32 = 4500;

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
    const action = std.posix.Sigaction{ .handler = .{ .handler = onSignal }, .mask = std.posix.sigemptyset(), .flags = 0 };
    std.posix.sigaction(std.posix.SIG.INT, &action, null);
    std.posix.sigaction(std.posix.SIG.TERM, &action, null);
}

pub fn received() bool {
    return signalled.load(.acquire) > 0;
}

pub fn count() u32 {
    return signalled.load(.acquire);
}

pub fn reset() void {
    signalled.store(0, .release);
}

fn onSignal(signal: std.posix.SIG) callconv(.c) void {
    _ = signal;
    _ = signalled.fetchAdd(1, .acq_rel);
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
