const std = @import("std");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");
const compat = @import("compat");

pub const max_header_bytes = 16 * 1024;
pub const max_body_bytes = 16 * 1024 * 1024;

pub const bad_request = "400 Bad Request";
pub const header_too_large = "431 Request Header Fields Too Large";
pub const request_timeout = "408 Request Timeout";
pub const header_read_ms: i32 = @intCast(30 * std.time.ns_per_s / std.time.ns_per_ms);
pub const idle_read_ms: i32 = @intCast(2 * 60 * std.time.ns_per_s / std.time.ns_per_ms);

pub const Failure = error{
    HeaderTooLarge,
    Truncated,
    Malformed,
    BodyTooLarge,
    ReadFailed,
    Timeout,
    UnsupportedTransferEncoding,
} || std.mem.Allocator.Error;

pub const Refusal = struct {
    status: []const u8,
    code: []const u8,
    message: []const u8,
};

pub const host_refused = Refusal{
    .status = "403 Forbidden",
    .code = "unrecognized_host",
    .message = "unrecognized Host header; this daemon serves loopback clients only",
};

pub const origin_refused = Refusal{
    .status = "403 Forbidden",
    .code = "cross_origin_request",
    .message = "the daemon does not serve cross-origin requests",
};

pub const too_large = Refusal{
    .status = "413 Payload Too Large",
    .code = "request_too_large",
    .message = "the request body is over the 16 MiB cap",
};

pub const read_failed = Refusal{
    .status = bad_request,
    .code = "request_read",
    .message = "the request body could not be read",
};

pub const Target = struct {
    path: []const u8,
    query: []const u8,
};

pub const Request = struct {
    method: []const u8 = &.{},
    target: []const u8 = &.{},
    split: Target = .{ .path = &.{}, .query = &.{} },
    host: ?[]const u8 = null,
    origin: bool = false,
    content_type: ?[]const u8 = null,
    content_length: usize = 0,
    body: []u8 = &.{},

    pub fn deinit(self: *Request, allocator: std.mem.Allocator) void {
        allocator.free(self.method);
        allocator.free(self.target);
        allocator.free(self.split.path);
        allocator.free(self.split.query);
        if (self.host) |value| allocator.free(value);
        if (self.content_type) |value| allocator.free(value);
        if (self.body.len > 0) allocator.free(self.body);
        self.* = undefined;
    }
};

pub fn splitTarget(allocator: std.mem.Allocator, target: []const u8) Failure!Target {
    const at = std.mem.indexOfScalar(u8, target, '?') orelse {
        const query = try allocator.dupe(u8, "");
        errdefer allocator.free(query);
        const whole = try allocator.dupe(u8, target);
        return .{ .path = whole, .query = query };
    };
    const path = try allocator.dupe(u8, target[0..at]);
    errdefer allocator.free(path);
    const query = try allocator.dupe(u8, target[at + 1 ..]);
    return .{ .path = path, .query = query };
}

pub const pollable = @import("builtin").os.tag != .windows;

fn elapsedMs() !u64 {
    return @intCast(try compat.time.monotonicNanos() / std.time.ns_per_ms);
}

fn readWithin(stream: *compat.net.Stream, buffer: []u8, wait_ms: i32) Failure!usize {
    if (comptime !pollable) return stream.read(buffer) catch error.ReadFailed;
    const ready = compat.net.readableWithin(compat.net.streamHandle(stream), wait_ms) catch return error.ReadFailed;
    if (!ready) return error.Timeout;
    return stream.read(buffer) catch error.ReadFailed;
}

pub fn readRequest(allocator: std.mem.Allocator, stream: *compat.net.Stream, header_wait_ms: i32, idle_wait_ms: i32) Failure!Request {
    var head = std.ArrayList(u8).empty;
    defer head.deinit(allocator);
    var byte: [1]u8 = undefined;
    const started = elapsedMs() catch 0;
    while (head.items.len < max_header_bytes) {
        const budget: i64 = @as(i64, header_wait_ms) - @as(i64, @intCast((elapsedMs() catch 0) - started));
        if (budget <= 0) return error.Timeout;
        const n = try readWithin(stream, &byte, @intCast(@min(budget, @as(i64, std.math.maxInt(i32)))));
        if (n == 0) return error.Truncated;
        try head.append(allocator, byte[0]);
        if (std.mem.endsWith(u8, head.items, "\r\n\r\n")) break;
    }
    if (!std.mem.endsWith(u8, head.items, "\r\n\r\n")) return error.HeaderTooLarge;

    var lines = std.mem.splitSequence(u8, head.items, "\r\n");
    const request_line = lines.next() orelse return error.Malformed;
    var parts = std.mem.splitScalar(u8, request_line, ' ');
    const method = parts.next() orelse return error.Malformed;
    const target = parts.next() orelse return error.Malformed;
    const version = parts.next() orelse return error.Malformed;
    if (parts.next() != null) return error.Malformed;
    if (method.len == 0 or target.len == 0) return error.Malformed;
    if (!std.mem.eql(u8, version, "HTTP/1.1")) return error.Malformed;

    var request = Request{};
    errdefer request.deinit(allocator);
    request.method = try allocator.dupe(u8, method);
    request.target = try allocator.dupe(u8, target);
    request.split = try splitTarget(allocator, target);

    var seen_host = false;
    while (lines.next()) |line| {
        if (line.len == 0) break;
        const at = std.mem.indexOfScalar(u8, line, ':') orelse continue;
        const name = line[0..at];
        const value = std.mem.trim(u8, line[at + 1 ..], " \t");
        if (std.ascii.eqlIgnoreCase(name, "host")) {
            if (seen_host) return error.Malformed;
            seen_host = true;
            request.host = try allocator.dupe(u8, value);
        } else if (std.ascii.eqlIgnoreCase(name, "origin")) {
            request.origin = true;
        } else if (std.ascii.eqlIgnoreCase(name, "content-type")) {
            request.content_type = try allocator.dupe(u8, value);
        } else if (std.ascii.eqlIgnoreCase(name, "content-length")) {
            request.content_length = std.fmt.parseInt(usize, value, 10) catch return error.Malformed;
        } else if (std.ascii.eqlIgnoreCase(name, "transfer-encoding")) {
            return error.UnsupportedTransferEncoding;
        }
    }

    if (request.content_length > max_body_bytes) return error.BodyTooLarge;
    if (request.content_length == 0) return request;
    request.body = try allocator.alloc(u8, request.content_length);
    var filled: usize = 0;
    while (filled < request.body.len) {
        const n = try readWithin(stream, request.body[filled..], idle_wait_ms);
        if (n == 0) return error.Truncated;
        filled += n;
    }
    return request;
}

const loopback_names = [_][]const u8{ "localhost", "127.0.0.1", "::1" };

pub fn loopbackHosts(addr: []const u8) ?[]const []const u8 {
    const host = hostOf(addr);
    for (loopback_names) |name| {
        if (std.mem.eql(u8, host, name)) return &loopback_names;
    }
    return null;
}

pub fn hostOf(addr: []const u8) []const u8 {
    if (addr.len == 0) return addr;
    if (addr[0] == '[') {
        const close = std.mem.indexOfScalar(u8, addr, ']') orelse return addr;
        return addr[1..close];
    }
    if (std.mem.lastIndexOfScalar(u8, addr, ':')) |at| {
        if (std.mem.indexOfScalar(u8, addr[0..at], ':') == null) return addr[0..at];
    }
    return addr;
}

pub fn hostRefused(allow: []const []const u8, host: ?[]const u8) bool {
    if (allow.len == 0) return false;
    const name = hostOf(std.mem.trim(u8, host orelse "", " \t"));
    for (allow) |candidate| {
        if (std.ascii.eqlIgnoreCase(candidate, name)) return false;
    }
    return true;
}

pub fn refusalEnvelope(arena: std.mem.Allocator, next_id: u64, refused: Refusal) ![]const u8 {
    const id = try std.fmt.allocPrint(arena, "oap-error-{d}", .{next_id});
    const correlation = try std.fmt.allocPrint(arena, "oap-request-{d}", .{next_id});
    return oap_envelope.serializeEnvelope(.{
        .id = id,
        .in_reply_to = correlation,
        .payload = .{ .error_response = .{ .code = refused.code, .message = refused.message } },
    }, arena);
}

pub fn transportRefusal(failed: Failure) ?Refusal {
    return switch (failed) {
        error.BodyTooLarge => too_large,
        error.ReadFailed => read_failed,
        else => null,
    };
}

pub fn statusFor(failed: Failure) []const u8 {
    return switch (failed) {
        error.HeaderTooLarge => header_too_large,
        error.Timeout => request_timeout,
        else => bad_request,
    };
}

pub fn writeRefusal(stream: *compat.net.Stream, refused: Refusal, body: []const u8) !void {
    try writeHead(stream, refused.status, "application/json", body);
}

pub fn writeStatus(stream: *compat.net.Stream, status: []const u8, body: []const u8) !void {
    try writeHead(stream, status, "text/plain; charset=utf-8", body);
}

fn writeHead(stream: *compat.net.Stream, status: []const u8, content_type: []const u8, body: []const u8) !void {
    var counted: [24]u8 = undefined;
    const length = try std.fmt.bufPrint(&counted, "{d}\r\n\r\n", .{body.len});
    try stream.writeAll("HTTP/1.1 ");
    try stream.writeAll(status);
    try stream.writeAll("\r\nContent-Type: ");
    try stream.writeAll(content_type);
    try stream.writeAll("\r\nContent-Length: ");
    try stream.writeAll(length);
    try stream.writeAll(body);
}

pub const not_found_body = "not found";

const testing = std.testing;

const Pipe = struct {
    server: compat.net.Server,
    client: compat.net.Stream,
    accepted: compat.net.Stream,

    fn openWith(allocator: std.mem.Allocator) !Pipe {
        const address = try compat.net.resolveAddress(allocator, "127.0.0.1", 0);
        var server = try compat.net.tcpListen(address, .{ .reuse_address = true });
        errdefer compat.net.closeServer(&server);
        const client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
        const connection = try compat.net.accept(&server);
        return .{ .server = server, .client = client, .accepted = connection.stream };
    }

    fn open() !Pipe {
        return openWith(testing.allocator);
    }

    fn close(self: *Pipe) void {
        self.accepted.close();
        self.client.close();
        compat.net.closeServer(&self.server);
    }
};

fn parseUnder(allocator: std.mem.Allocator, raw: []const u8) !void {
    var pipe = try Pipe.openWith(allocator);
    defer pipe.close();
    try pipe.client.writeAll(raw);
    var request = try readRequest(allocator, &pipe.accepted, header_read_ms, idle_read_ms);
    request.deinit(allocator);
}

test "reading a request frees what it built when any allocation fails" {
    try testing.checkAllAllocationFailures(testing.allocator, parseUnder, .{"GET /adapters?after=3 HTTP/1.1\r\nHost: a\r\n\r\n"});
    try testing.checkAllAllocationFailures(testing.allocator, parseUnder, .{"POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\n\r\nbody"});
}

fn requestOver(raw: []const u8) !Request {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll(raw);
    return readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms);
}

test "a request is read off the wire, headers and body included" {
    var request = try requestOver("POST /adapters/x/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}");
    defer request.deinit(testing.allocator);
    try testing.expectEqualStrings("POST", request.method);
    try testing.expectEqualStrings("/adapters/x/sessions", request.split.path);
    try testing.expectEqualStrings("", request.split.query);
    try testing.expectEqualStrings("127.0.0.1:6270", request.host.?);
    try testing.expectEqualStrings("application/json", request.content_type.?);
    try testing.expect(!request.origin);
    try testing.expectEqualStrings("{}", request.body);
}

test "a target's path and query split on the first question mark" {
    const cases = [_]struct { raw: []const u8, path: []const u8, query: []const u8 }{
        .{ .raw = "/adapters", .path = "/adapters", .query = "" },
        .{ .raw = "/sessions/s1/events?after=7&run_id=r1", .path = "/sessions/s1/events", .query = "after=7&run_id=r1" },
        .{ .raw = "/x?a?b", .path = "/x", .query = "a?b" },
    };
    for (cases) |case| {
        var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena_state.deinit();
        const split = try splitTarget(arena_state.allocator(), case.raw);
        try testing.expectEqualStrings(case.path, split.path);
        try testing.expectEqualStrings(case.query, split.query);
    }
}

test "any Origin header is noted, whatever it carries" {
    var request = try requestOver("GET /adapters HTTP/1.1\r\nHost: localhost\r\nOrigin: http://evil.test\r\n\r\n");
    defer request.deinit(testing.allocator);
    try testing.expect(request.origin);
}

test "a second Host header, an unknown version and a missing version are each refused as malformed" {
    try testing.expectError(error.Malformed, requestOver("GET /adapters HTTP/1.1\r\nHost: a\r\nHost: b\r\n\r\n"));
    try testing.expectError(error.Malformed, requestOver("GET /adapters HTTP/2.0\r\nHost: a\r\n\r\n"));
    try testing.expectError(error.Malformed, requestOver("GET /adapters\r\n\r\n"));
    try testing.expectError(error.Malformed, requestOver("GET /adapters HTTP/1.1\r\nContent-Length: nine\r\n\r\n"));
}

test "a chunked body is refused rather than guessed at" {
    try testing.expectError(error.UnsupportedTransferEncoding, requestOver("POST /adapters HTTP/1.1\r\nHost: a\r\nTransfer-Encoding: chunked\r\n\r\n"));
}

test "headers over the cap are refused rather than buffered" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const oversized = try std.fmt.allocPrint(arena_state.allocator(), "GET /adapters HTTP/1.1\r\nHost: a\r\nX-Pad: {s}\r\n\r\n", .{"p" ** (max_header_bytes + 16)});
    try testing.expectError(error.HeaderTooLarge, requestOver(oversized));
    try testing.expectEqualStrings(header_too_large, statusFor(error.HeaderTooLarge));
}

test "a body over the cap is refused before it is read" {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 16777217\r\n\r\n");
    try testing.expectError(error.BodyTooLarge, readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms));
}

test "a host that hangs up mid-request is truncated, not answered" {
    const address = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
    var server = try compat.net.tcpListen(address, .{ .reuse_address = true });
    defer compat.net.closeServer(&server);
    var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
    try client.writeAll("GET /adapters HTTP/1.1\r\n");
    const connection = try compat.net.accept(&server);
    var accepted = connection.stream;
    defer accepted.close();
    client.close();
    try testing.expectError(error.Truncated, readRequest(testing.allocator, &accepted, header_read_ms, idle_read_ms));
}

test "the read budget bounds a read only where the socket can be waited on" {
    try testing.expect(!pollable == (@import("builtin").os.tag == .windows));
}

test "only the two transport codes answer as an error envelope, and the rest as a bare status" {
    try testing.expectEqualStrings("request_too_large", transportRefusal(error.BodyTooLarge).?.code);
    try testing.expectEqualStrings("413 Payload Too Large", transportRefusal(error.BodyTooLarge).?.status);
    try testing.expectEqualStrings("request_read", transportRefusal(error.ReadFailed).?.code);
    try testing.expectEqualStrings(bad_request, transportRefusal(error.ReadFailed).?.status);
    for ([_]Failure{ error.Malformed, error.Truncated, error.UnsupportedTransferEncoding }) |each| {
        try testing.expect(transportRefusal(each) == null);
        try testing.expectEqualStrings(bad_request, statusFor(each));
    }
    try testing.expect(transportRefusal(error.HeaderTooLarge) == null);
    try testing.expectEqualStrings(header_too_large, statusFor(error.HeaderTooLarge));
}

test "a peer that connects and never sends a request is given up on, not waited on forever" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try testing.expectError(error.Timeout, readRequest(testing.allocator, &pipe.accepted, 50, idle_read_ms));
    try testing.expectEqualStrings(request_timeout, statusFor(error.Timeout));
}

test "the header budget is the whole request's, not a fresh one per byte" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("G");
    const started = elapsedMs() catch 0;
    try testing.expectError(error.Timeout, readRequest(testing.allocator, &pipe.accepted, 120, idle_read_ms));
    try testing.expect((elapsedMs() catch 0) - started < 2000);
}

test "a header whose name merely starts with a known one is not that header" {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("GET /adapters HTTP/1.1\r\nHostname: evil.test\r\nOrigin-Repeat: 1\r\nContent-Types: 7\r\n\r\n");
    var request = try readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms);
    defer request.deinit(testing.allocator);
    try testing.expect(request.host == null);
    try testing.expect(!request.origin);
    try testing.expect(request.content_type == null);
    try testing.expectEqual(@as(usize, 0), request.content_length);
}

test "the loopback allowlist is the three loopback spellings and nothing else" {
    try testing.expect(loopbackHosts("127.0.0.1:6270") != null);
    try testing.expect(loopbackHosts("localhost:6270") != null);
    try testing.expect(loopbackHosts("[::1]:6270") != null);
    try testing.expect(loopbackHosts("0.0.0.0:6270") == null);
    try testing.expect(loopbackHosts("192.168.1.4:6270") == null);
    try testing.expect(loopbackHosts("[::]:6270") == null);
    try testing.expectEqual(@as(usize, 3), loopbackHosts("127.0.0.1:6270").?.len);
}

test "a Host header is compared without its port and without case" {
    const allow = loopbackHosts("127.0.0.1:6270").?;
    try testing.expect(!hostRefused(allow, "127.0.0.1:6270"));
    try testing.expect(!hostRefused(allow, "127.0.0.1"));
    try testing.expect(!hostRefused(allow, "LOCALHOST:9999"));
    try testing.expect(!hostRefused(allow, "  [::1]:6270 "));
    try testing.expect(hostRefused(allow, "evil.test"));
    try testing.expect(hostRefused(allow, "127.0.0.1.evil.test"));
    try testing.expect(hostRefused(allow, ""));
}

test "a bind that is not loopback has no allowlist, so nothing is refused" {
    try testing.expect(loopbackHosts("0.0.0.0:6270") == null);
    try testing.expect(!hostRefused(loopbackHosts("0.0.0.0:6270") orelse &.{}, "anything.test"));
    try testing.expect(!hostRefused(&.{}, "anything.test"));
}

test "a host:port is split only when the name carries no colon of its own" {
    try testing.expectEqualStrings("127.0.0.1", hostOf("127.0.0.1:6270"));
    try testing.expectEqualStrings("localhost", hostOf("localhost"));
    try testing.expectEqualStrings("::1", hostOf("[::1]:6270"));
    try testing.expectEqualStrings("::1", hostOf("::1"));
    try testing.expectEqualStrings("", hostOf(""));
}

test "a refusal is an error.response naming its code, correlated to a request that never arrived" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    for ([_]Refusal{ host_refused, origin_refused, too_large, read_failed }) |each| {
        const line = try refusalEnvelope(arena, 1, each);
        const parsed = try std.json.parseFromSlice(std.json.Value, arena, line, .{});
        const object = parsed.value.object;
        try testing.expectEqualStrings("error.response", object.get("type").?.string);
        try testing.expectEqualStrings("oap-error-1", object.get("id").?.string);
        try testing.expectEqualStrings("oap-request-1", object.get("in_reply_to").?.string);
        try testing.expectEqualStrings(each.code, object.get("payload").?.object.get("error").?.object.get("code").?.string);
        try testing.expect(object.get("session_id") == null);
        try testing.expect(object.get("run_id") == null);
    }
}
