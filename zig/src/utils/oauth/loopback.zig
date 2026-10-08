const std = @import("std");
const compat = @import("compat");
const net = compat.net;

pub const supported = net.supports_unix_channels;

const callback_path = "/callback";
const poll_ms: i32 = 100;
const request_read_ms: i64 = 2_000;
const max_request_line = 8192;
pub const default_wait_ms: i64 = 10 * 60 * 1000;

const done_page = "<!doctype html><meta charset=\"utf-8\"><title>Signed in</title><p>Signed in. You can close this tab.</p>";
const failed_page = "<!doctype html><meta charset=\"utf-8\"><title>Sign-in failed</title><p>Sign-in failed. Return to the application and try again.</p>";

pub const Listener = struct {
    server: net.Server,
    port: u16,

    pub fn open() !Listener {
        if (!supported) return error.UnsupportedPlatform;
        const address = try net.Address.parse("127.0.0.1", 0);
        var server = try net.tcpListen(address, .{});
        errdefer net.closeServer(&server);
        return .{ .server = server, .port = net.listenAddress(&server).getPort() };
    }

    pub fn close(self: *Listener) void {
        net.closeServer(&self.server);
        self.* = undefined;
    }

    pub fn redirectUri(self: *const Listener, allocator: std.mem.Allocator) ![]u8 {
        return std.fmt.allocPrint(allocator, "http://localhost:{d}" ++ callback_path, .{self.port});
    }

    pub fn waitForCode(
        self: *Listener,
        allocator: std.mem.Allocator,
        expected_state: []const u8,
        isCancelled: *const fn () bool,
        wait_ms: i64,
    ) ![]u8 {
        const deadline = compat.time.nowMillis() + wait_ms;
        while (true) {
            if (isCancelled()) return error.AuthFlowCancelled;
            if (compat.time.nowMillis() >= deadline) return error.LoginTimedOut;
            if (!try net.readableWithin(net.serverHandle(&self.server), poll_ms)) continue;
            var stream = net.acceptStream(&self.server) catch continue;
            defer stream.close();
            if (isCancelled()) {
                respond(&stream, "409 Conflict", failed_page);
                return error.AuthFlowCancelled;
            }
            const outcome = try answer(allocator, &stream, expected_state, isCancelled);
            switch (outcome) {
                .ignored => continue,
                .refused => return error.OAuthFailed,
                .code => |code| return code,
            }
        }
    }
};

const Outcome = union(enum) {
    ignored,
    refused,
    code: []u8,
};

fn answer(allocator: std.mem.Allocator, stream: *net.Stream, expected_state: []const u8, isCancelled: *const fn () bool) !Outcome {
    var buffer: [max_request_line]u8 = undefined;
    const line = try readRequestLine(stream, &buffer, isCancelled) orelse {
        respond(stream, "400 Bad Request", failed_page);
        return .ignored;
    };
    const target = requestTarget(line) orelse {
        respond(stream, "400 Bad Request", failed_page);
        return .ignored;
    };
    const query_start = std.mem.indexOfScalar(u8, target, '?');
    const path = target[0 .. query_start orelse target.len];
    if (!std.mem.eql(u8, path, callback_path)) {
        respond(stream, "404 Not Found", "");
        return .ignored;
    }
    const query = if (query_start) |index| target[index + 1 ..] else "";

    const state = try queryValue(allocator, query, "state");
    defer if (state) |value| allocator.free(value);
    if (state == null or !std.mem.eql(u8, state.?, expected_state)) {
        respond(stream, "400 Bad Request", failed_page);
        return .ignored;
    }
    const code = try queryValue(allocator, query, "code");
    if (code == null or code.?.len == 0) {
        if (code) |value| allocator.free(value);
        respond(stream, "400 Bad Request", failed_page);
        return .refused;
    }
    respond(stream, "200 OK", done_page);
    return .{ .code = code.? };
}

fn readRequestLine(stream: *net.Stream, buffer: []u8, isCancelled: *const fn () bool) !?[]const u8 {
    const deadline = compat.time.nowMillis() + request_read_ms;
    var filled: usize = 0;
    while (filled < buffer.len) {
        if (std.mem.indexOf(u8, buffer[0..filled], "\r\n")) |end| return buffer[0..end];
        if (isCancelled()) return error.AuthFlowCancelled;
        const remaining = deadline - compat.time.nowMillis();
        if (remaining <= 0) return null;
        const ready = net.readableWithin(net.streamHandle(stream), @intCast(@min(remaining, poll_ms))) catch return null;
        if (!ready) continue;
        const read = stream.readSome(buffer[filled..]) catch return null;
        if (read == 0) return null;
        filled += read;
    }
    return null;
}

fn requestTarget(line: []const u8) ?[]const u8 {
    var parts = std.mem.splitScalar(u8, line, ' ');
    const method = parts.next() orelse return null;
    if (!std.mem.eql(u8, method, "GET")) return null;
    const target = parts.next() orelse return null;
    const version = parts.next() orelse return null;
    if (!std.mem.startsWith(u8, version, "HTTP/1.")) return null;
    return target;
}

fn queryValue(allocator: std.mem.Allocator, query: []const u8, key: []const u8) !?[]u8 {
    var pairs = std.mem.splitScalar(u8, query, '&');
    while (pairs.next()) |pair| {
        const eq = std.mem.indexOfScalar(u8, pair, '=') orelse continue;
        if (!std.mem.eql(u8, pair[0..eq], key)) continue;
        return try percentDecode(allocator, pair[eq + 1 ..]);
    }
    return null;
}

fn percentDecode(allocator: std.mem.Allocator, value: []const u8) ![]u8 {
    var out = try std.ArrayList(u8).initCapacity(allocator, value.len);
    errdefer out.deinit(allocator);
    var index: usize = 0;
    while (index < value.len) : (index += 1) {
        const byte = value[index];
        if (byte == '+') {
            out.appendAssumeCapacity(' ');
        } else if (byte == '%' and index + 2 < value.len) {
            const decoded = std.fmt.parseInt(u8, value[index + 1 .. index + 3], 16) catch {
                out.appendAssumeCapacity(byte);
                continue;
            };
            out.appendAssumeCapacity(decoded);
            index += 2;
        } else {
            out.appendAssumeCapacity(byte);
        }
    }
    return out.toOwnedSlice(allocator);
}

fn respond(stream: *net.Stream, status: []const u8, body: []const u8) void {
    var head: [256]u8 = undefined;
    const header = std.fmt.bufPrint(&head, "HTTP/1.1 {s}\r\nContent-Type: text/html; charset=utf-8\r\nContent-Length: {d}\r\nCache-Control: no-store\r\nConnection: close\r\n\r\n", .{ status, body.len }) catch return;
    stream.writeAll(header) catch return;
    stream.writeAll(body) catch return;
}

const TestBrowser = struct {
    port: u16,
    target: []const u8,
    response: [512]u8 = undefined,
    response_len: usize = 0,

    fn run(self: *TestBrowser) void {
        var stream = net.tcpConnectHost(std.heap.page_allocator, "127.0.0.1", self.port) catch return;
        defer stream.close();
        var request: [1024]u8 = undefined;
        const line = std.fmt.bufPrint(&request, "GET {s} HTTP/1.1\r\nHost: localhost\r\n\r\n", .{self.target}) catch return;
        stream.writeAll(line) catch return;
        while (self.response_len < self.response.len) {
            if (!(net.readableWithin(net.streamHandle(&stream), 3_000) catch return)) return;
            const read = stream.readSome(self.response[self.response_len..]) catch return;
            if (read == 0) return;
            self.response_len += read;
        }
    }

    fn status(self: *const TestBrowser) []const u8 {
        const text = self.response[0..self.response_len];
        const end = std.mem.indexOf(u8, text, "\r\n") orelse return text;
        return text[0..end];
    }
};

fn neverCancelled() bool {
    return false;
}

var test_cancel_at: i64 = 0;
var cancel_checks: usize = 0;

fn cancelledOnSecondCheck() bool {
    cancel_checks += 1;
    return cancel_checks >= 2;
}

fn cancelledAfterDeadline() bool {
    return compat.time.nowMillis() >= test_cancel_at;
}

test "loopback listener returns the code from a callback that carries the expected state and tells the browser it is done" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();
    const redirect = try listener.redirectUri(std.testing.allocator);
    defer std.testing.allocator.free(redirect);
    const expected = try std.fmt.allocPrint(std.testing.allocator, "http://localhost:{d}/callback", .{listener.port});
    defer std.testing.allocator.free(expected);
    try std.testing.expectEqualStrings(expected, redirect);

    var browser = TestBrowser{ .port = listener.port, .target = "/callback?code=abc%2F123&state=s-1" };
    const thread = try std.Thread.spawn(.{}, TestBrowser.run, .{&browser});
    const code = try listener.waitForCode(std.testing.allocator, "s-1", neverCancelled, 5_000);
    defer std.testing.allocator.free(code);
    thread.join();

    try std.testing.expectEqualStrings("abc/123", code);
    try std.testing.expectEqualStrings("HTTP/1.1 200 OK", browser.status());
    try std.testing.expect(std.mem.indexOf(u8, browser.response[0..browser.response_len], "Signed in.") != null);
}

test "loopback listener turns away a callback with the wrong state or path and keeps waiting for the right one" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();

    var forged = TestBrowser{ .port = listener.port, .target = "/callback?code=forged&state=other" };
    var elsewhere = TestBrowser{ .port = listener.port, .target = "/favicon.ico" };
    var genuine = TestBrowser{ .port = listener.port, .target = "/callback?state=s-2&code=real" };
    const browsers = [_]*TestBrowser{ &forged, &elsewhere, &genuine };
    const thread = try std.Thread.spawn(.{}, struct {
        fn run(list: []const *TestBrowser) void {
            for (list) |browser| browser.run();
        }
    }.run, .{@as([]const *TestBrowser, &browsers)});
    const code = try listener.waitForCode(std.testing.allocator, "s-2", neverCancelled, 5_000);
    defer std.testing.allocator.free(code);
    thread.join();

    try std.testing.expectEqualStrings("real", code);
    try std.testing.expectEqualStrings("HTTP/1.1 400 Bad Request", forged.status());
    try std.testing.expectEqualStrings("HTTP/1.1 404 Not Found", elsewhere.status());
    try std.testing.expectEqualStrings("HTTP/1.1 200 OK", genuine.status());
}

test "loopback listener fails a callback that carries the expected state but no code" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();

    var browser = TestBrowser{ .port = listener.port, .target = "/callback?error=access_denied&state=s-3" };
    const thread = try std.Thread.spawn(.{}, TestBrowser.run, .{&browser});
    try std.testing.expectError(error.OAuthFailed, listener.waitForCode(std.testing.allocator, "s-3", neverCancelled, 5_000));
    thread.join();
    try std.testing.expectEqualStrings("HTTP/1.1 400 Bad Request", browser.status());
}

test "loopback listener tells a browser that reaches it just after a cancel that sign-in failed" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();

    var browser = TestBrowser{ .port = listener.port, .target = "/callback?code=late&state=s-5" };
    const thread = try std.Thread.spawn(.{}, TestBrowser.run, .{&browser});
    while (!(try net.readableWithin(net.serverHandle(&listener.server), 10))) {}
    cancel_checks = 0;
    try std.testing.expectError(error.AuthFlowCancelled, listener.waitForCode(std.testing.allocator, "s-5", cancelledOnSecondCheck, 5_000));
    thread.join();
    try std.testing.expectEqualStrings("HTTP/1.1 409 Conflict", browser.status());
}

const Dribbler = struct {
    port: u16,
    stop: std.atomic.Value(bool) = .init(false),

    fn run(self: *Dribbler) void {
        var stream = net.tcpConnectHost(std.heap.page_allocator, "127.0.0.1", self.port) catch return;
        defer stream.close();
        const request = "GET /callback?code=slow&state=never HTTP/1.1";
        for (request) |byte| {
            if (self.stop.load(.seq_cst)) return;
            stream.writeAll(&.{byte}) catch return;
            compat.time.sleepMs(250);
        }
    }
};

test "loopback listener drops a client that never finishes its request line and then takes the genuine callback" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();

    var slow = Dribbler{ .port = listener.port };
    const slow_thread = try std.Thread.spawn(.{}, Dribbler.run, .{&slow});
    defer slow_thread.join();
    defer slow.stop.store(true, .seq_cst);
    compat.time.sleepMs(50);
    var genuine = TestBrowser{ .port = listener.port, .target = "/callback?code=real&state=s-6" };
    const genuine_thread = try std.Thread.spawn(.{}, TestBrowser.run, .{&genuine});

    const started = compat.time.nowMillis();
    const code = try listener.waitForCode(std.testing.allocator, "s-6", neverCancelled, 10_000);
    defer std.testing.allocator.free(code);
    genuine_thread.join();
    try std.testing.expectEqualStrings("real", code);
    try std.testing.expect(compat.time.nowMillis() - started < 4_000);
}

test "loopback listener notices a cancel while a client is still sending its request line" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();

    var slow = Dribbler{ .port = listener.port };
    const slow_thread = try std.Thread.spawn(.{}, Dribbler.run, .{&slow});
    defer slow_thread.join();
    defer slow.stop.store(true, .seq_cst);
    while (!(try net.readableWithin(net.serverHandle(&listener.server), 10))) {}

    const started = compat.time.nowMillis();
    test_cancel_at = started + 300;
    try std.testing.expectError(error.AuthFlowCancelled, listener.waitForCode(std.testing.allocator, "s-7", cancelledAfterDeadline, 10_000));
    try std.testing.expect(compat.time.nowMillis() - started < 1_000);
}

test "loopback listener stops waiting soon after the flow is cancelled" {
    if (!supported) return error.SkipZigTest;
    var listener = try Listener.open();
    defer listener.close();

    const started = compat.time.nowMillis();
    test_cancel_at = started + 200;
    try std.testing.expectError(error.AuthFlowCancelled, listener.waitForCode(std.testing.allocator, "s-4", cancelledAfterDeadline, 5_000));
    try std.testing.expect(compat.time.nowMillis() - started < 1_000);
}
