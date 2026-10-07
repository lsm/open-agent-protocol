const std = @import("std");

fn defaultIo() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

pub fn nowMillis() i64 {
    return std.Io.Timestamp.now(defaultIo(), .real).toMilliseconds();
}

pub fn nowSeconds() i64 {
    return std.Io.Timestamp.now(defaultIo(), .real).toSeconds();
}

pub fn nowNanos() i64 {
    return @intCast(std.Io.Timestamp.now(defaultIo(), .real).toNanoseconds());
}

var monotonic_origin: ?std.Io.Timestamp = null;
var monotonic_mutex: std.Io.Mutex = .init;

pub fn monotonicNanos() !u64 {
    monotonic_mutex.lockUncancelable(defaultIo());
    defer monotonic_mutex.unlock(defaultIo());

    const now = std.Io.Timestamp.now(defaultIo(), .boot);
    if (monotonic_origin == null) {
        monotonic_origin = now;
    }

    return @intCast(monotonic_origin.?.durationTo(now).nanoseconds);
}

pub fn monotonicMillis() !i64 {
    return @intCast(try monotonicNanos() / std.time.ns_per_ms);
}

pub fn sleepNs(ns: u64) void {
    const capped_ns = @min(ns, @as(u64, std.math.maxInt(i64)));
    defaultIo().sleep(.fromNanoseconds(@intCast(capped_ns)), .boot) catch {};
}

pub fn sleepMs(ms: u64) void {
    sleepNs(std.math.mul(u64, ms, std.time.ns_per_ms) catch std.math.maxInt(u64));
}

test "compat time helpers return expected public types" {
    const millis: i64 = nowMillis();
    const seconds: i64 = nowSeconds();
    const nanos: i64 = nowNanos();
    const monotonic: u64 = try monotonicNanos();
    const monotonic_ms: i64 = try monotonicMillis();

    _ = millis;
    _ = seconds;
    _ = nanos;
    _ = monotonic;
    _ = monotonic_ms;
}

test "compat time helpers return plausible wall-clock timestamps" {
    const seconds = nowSeconds();
    const millis = nowMillis();
    const nanos = nowNanos();

    try std.testing.expect(seconds > 0);
    try std.testing.expect(millis > 0);
    try std.testing.expect(nanos > 0);
    try std.testing.expect(@divTrunc(millis, std.time.ms_per_s) >= seconds - 1);
    try std.testing.expect(@divTrunc(nanos, std.time.ns_per_s) >= seconds - 1);
}

test "compat monotonic nanoseconds are nondecreasing" {
    const before = try monotonicNanos();
    sleepNs(1);
    const after = try monotonicNanos();

    try std.testing.expect(after >= before);
}

test "compat sleep helpers bound short sleeps" {
    const start_ns = try monotonicNanos();
    sleepNs(1 * std.time.ns_per_ms);
    const elapsed_ns = try monotonicNanos() - start_ns;

    try std.testing.expect(elapsed_ns >= 1 * std.time.ns_per_ms);
}

test "compat sleep helpers accept zero duration" {
    sleepNs(0);
    sleepMs(0);
}

pub fn isoMillis(stamp: []const u8) ?i64 {
    if (stamp.len < 20 or stamp[4] != '-' or stamp[7] != '-' or stamp[10] != 'T' or stamp[13] != ':' or stamp[16] != ':') return null;
    const year = std.fmt.parseInt(i64, stamp[0..4], 10) catch return null;
    const month = std.fmt.parseInt(i64, stamp[5..7], 10) catch return null;
    const day = std.fmt.parseInt(i64, stamp[8..10], 10) catch return null;
    const hour = std.fmt.parseInt(i64, stamp[11..13], 10) catch return null;
    const minute = std.fmt.parseInt(i64, stamp[14..16], 10) catch return null;
    const second = std.fmt.parseInt(i64, stamp[17..19], 10) catch return null;
    var millis: i64 = 0;
    var index: usize = 19;
    if (stamp[19] == '.') {
        index = 20;
        var scale: i64 = 100;
        while (index < stamp.len and std.ascii.isDigit(stamp[index])) : (index += 1) {
            millis += (stamp[index] - '0') * scale;
            scale = @divTrunc(scale, 10);
        }
    }
    var offset: i64 = 0;
    if (index < stamp.len and (stamp[index] == '+' or stamp[index] == '-')) {
        if (stamp.len != index + 6 or stamp[index + 3] != ':') return null;
        const hours = std.fmt.parseInt(i64, stamp[index + 1 .. index + 3], 10) catch return null;
        const minutes = std.fmt.parseInt(i64, stamp[index + 4 .. index + 6], 10) catch return null;
        offset = (hours * 60 + minutes) * 60_000;
        if (stamp[index] == '-') offset = -offset;
    }
    const shifted = if (month <= 2) year - 1 else year;
    const era = @divFloor(shifted, 400);
    const of_era = shifted - era * 400;
    const day_of_year = @divFloor(153 * (month + (if (month > 2) @as(i64, -3) else 9)) + 2, 5) + day - 1;
    const day_of_era = of_era * 365 + @divFloor(of_era, 4) - @divFloor(of_era, 100) + day_of_year;
    const days = era * 146097 + day_of_era - 719468;
    return ((days * 24 + hour) * 60 + minute) * 60_000 + second * 1000 + millis - offset;
}

test "an ISO 8601 UTC stamp reads as milliseconds since the epoch, and anything else as null" {
    try std.testing.expectEqual(@as(?i64, 1791317553250), isoMillis("2026-10-06T20:12:33.250Z"));
    try std.testing.expectEqual(@as(?i64, 951782400000), isoMillis("2000-02-29T00:00:00Z"));
    try std.testing.expectEqual(@as(?i64, null), isoMillis("yesterday"));
}

test "an ISO 8601 stamp with an offset reads as the same instant in UTC" {
    try std.testing.expectEqual(@as(?i64, 1791317553250), isoMillis("2026-10-06T20:12:33.250+00:00"));
    try std.testing.expectEqual(@as(?i64, 1791317553250), isoMillis("2026-10-06T22:42:33.250+02:30"));
    try std.testing.expectEqual(@as(?i64, 1791317553000), isoMillis("2026-10-06T15:12:33-05:00"));
    try std.testing.expectEqual(@as(?i64, null), isoMillis("2026-10-06T20:12:33+0200"));
}
