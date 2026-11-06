const std = @import("std");
const c = std.c;
const builtin = @import("builtin");

extern "c" fn pthread_mach_thread_np(thread: c.pthread_t) c_uint;

pub fn isBlocked(thread: std.Thread, tid: std.Thread.Id) !bool {
    if (builtin.os.tag == .macos) {
        const darwin = std.c;
        var info: darwin.thread_basic_info = undefined;
        var count: darwin.mach_msg_type_number_t = darwin.THREAD.BASIC.INFO_COUNT;
        if (darwin.thread_info(pthread_mach_thread_np(thread.getHandle()), darwin.THREAD.BASIC.INFO, @ptrCast(&info), &count) != 0) return error.ThreadInfoFailed;
        const th_state_waiting = 3;
        return info.run_state == th_state_waiting;
    }
    var path_buffer: [128]u8 = undefined;
    const path = try std.mem.printSentinel(&path_buffer, "/proc/self/task/{d}/stat", .{tid}, 0);
    const fd = c.open(path.ptr, .{});
    if (fd < 0) return error.ThreadInfoFailed;
    defer _ = c.close(fd);
    var buffer: [1024]u8 = undefined;
    const got = c.read(fd, &buffer, buffer.len);
    if (got < 0) return error.ThreadInfoFailed;
    const text = buffer[0..@intCast(got)];
    const close = std.mem.lastIndexOfScalar(u8, text, ')') orelse return error.ThreadInfoFailed;
    return close + 2 < text.len and text[close + 2] == 'S';
}
