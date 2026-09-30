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
    BodyTruncated,
    ReadFailed,
    Stopped,
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
    filled: usize = 0,
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

pub const KeepGoing = struct {
    context: *const anyopaque = undefined,
    check: *const fn (*const anyopaque) bool,

    fn yes(self: KeepGoing) bool {
        return self.check(self.context);
    }
};

pub const always_going = KeepGoing{ .context = undefined, .check = struct {
    fn yes(_: *const anyopaque) bool {
        return true;
    }
}.yes };

fn readUntil(stream: *compat.net.Stream, buffer: []u8, deadline_ms: u64, cycle_ms: i32, keep_going: KeepGoing) Failure!usize {
    while (true) {
        if (!keep_going.yes()) return error.Stopped;
        const left_ms: i64 = @as(i64, @intCast(deadline_ms)) - @as(i64, @intCast(elapsedMs() catch return error.Timeout));
        if (left_ms <= 0) return error.Timeout;
        if (comptime !pollable) return stream.read(buffer) catch error.ReadFailed;
        const wait: i32 = @intCast(@min(left_ms, @as(i64, @max(@as(i64, cycle_ms), 1))));
        const ready = compat.net.readableWithin(compat.net.streamHandle(stream), wait) catch return error.ReadFailed;
        if (!ready) continue;
        return stream.readSome(buffer) catch error.ReadFailed;
    }
}

pub fn digits(text: []const u8) ?usize {
    if (text.len == 0) return null;
    var value: usize = 0;
    for (text) |byte| {
        if (!std.ascii.isDigit(byte)) return null;
        value = std.math.mul(usize, value, 10) catch return null;
        value = std.math.add(usize, value, byte - '0') catch return null;
    }
    return value;
}

pub fn readHead(allocator: std.mem.Allocator, stream: *compat.net.Stream, header_wait_ms: i32, cycle_ms: i32, keep_going: KeepGoing, body_allowed: *bool, declared: *usize) Failure!Request {
    var head = std.ArrayList(u8).empty;
    defer head.deinit(allocator);
    var byte: [1]u8 = undefined;
    const headers_done = (elapsedMs() catch return error.Timeout) + @as(u64, @intCast(@max(header_wait_ms, 0)));
    while (head.items.len < max_header_bytes) {
        const n = try readUntil(stream, &byte, headers_done, cycle_ms, keep_going);
        if (n == 0) return error.Truncated;
        try head.append(allocator, byte[0]);
        if (std.mem.endsWith(u8, head.items, "\r\n\r\n")) break;
    }
    if (!std.mem.endsWith(u8, head.items, "\r\n\r\n")) return error.HeaderTooLarge;

    var lines = std.mem.splitSequence(u8, head.items, "\r\n");
    const request_line = lines.next() orelse return error.Malformed;
    var parts = std.mem.splitScalar(u8, request_line, ' ');
    const verb = parts.next() orelse return error.Malformed;
    const target = parts.next() orelse return error.Malformed;
    const version = parts.next() orelse return error.Malformed;
    if (parts.next() != null) return error.Malformed;
    if (verb.len == 0 or target.len == 0) return error.Malformed;
    if (!std.mem.eql(u8, version, "HTTP/1.1")) return error.Malformed;
    body_allowed.* = bodyAllowedFor(verb);

    var request = Request{};
    errdefer request.deinit(allocator);
    request.method = try allocator.dupe(u8, verb);
    request.target = try allocator.dupe(u8, target);
    request.split = try splitTarget(allocator, target);

    var seen_host = false;
    var seen_type = false;
    var seen_length = false;
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
            if (seen_type) return error.Malformed;
            seen_type = true;
            request.content_type = try allocator.dupe(u8, value);
        } else if (std.ascii.eqlIgnoreCase(name, "content-length")) {
            if (seen_length) return error.Malformed;
            seen_length = true;
            request.content_length = digits(value) orelse return error.Malformed;
            declared.* = request.content_length;
        } else if (std.ascii.eqlIgnoreCase(name, "transfer-encoding")) {
            return error.UnsupportedTransferEncoding;
        }
    }

    if (request.content_length > max_body_bytes) return error.BodyTooLarge;
    return request;
}

pub const drain_cap_bytes = 64 * 1024;
pub const drain_cycle_ms: i32 = 50;
pub const drain_total_cap_bytes = 1024 * 1024;
pub const drain_total_ms: i32 = 2500;

pub fn drain(stream: *compat.net.Stream, remaining_in: usize, keep_going: KeepGoing) usize {
    var remaining: usize = @min(remaining_in, drain_total_cap_bytes);
    var spent: usize = 0;
    var scratch: [1024]u8 = undefined;
    const started = elapsedMs() catch return spent;
    while (remaining > 0) {
        if (!keep_going.yes()) return spent;
        const now = elapsedMs() catch return spent;
        if (now -| started >= drain_total_ms) return spent;
        var owed: usize = @min(remaining, drain_cap_bytes);
        const deadline = now + @as(u64, @intCast(@max(drain_cycle_ms, 0)));
        while (owed > 0) {
            if (!keep_going.yes()) return spent;
            const chunk = @min(owed, scratch.len);
            const n: usize = @intCast(readUntil(stream, scratch[0..chunk], deadline, drain_cycle_ms, keep_going) catch return spent);
            if (n == 0) return spent;
            owed -= n;
            remaining -= n;
            spent += n;
            if (spent >= drain_total_cap_bytes) return spent;
        }
    }
    return spent;
}

pub fn readBody(allocator: std.mem.Allocator, stream: *compat.net.Stream, request: *Request, idle_wait_ms: i32, cycle_ms: i32, keep_going: KeepGoing) Failure!void {
    if (request.content_length == 0) return;
    request.body = try allocator.alloc(u8, request.content_length);
    const idle_ms: u64 = @intCast(@max(idle_wait_ms, 0));
    while (request.filled < request.body.len) {
        const round_deadline = (elapsedMs() catch return error.Timeout) + idle_ms;
        const n = try readUntil(stream, request.body[request.filled..], round_deadline, cycle_ms, keep_going);
        if (n == 0) return error.BodyTruncated;
        request.filled += n;
    }
}

pub fn readRequest(allocator: std.mem.Allocator, stream: *compat.net.Stream, header_wait_ms: i32, idle_wait_ms: i32, cycle_ms: i32, keep_going: KeepGoing, body_allowed: *bool) Failure!Request {
    var declared: usize = 0;
    var request = try readHead(allocator, stream, header_wait_ms, cycle_ms, keep_going, body_allowed, &declared);
    errdefer request.deinit(allocator);
    try readBody(allocator, stream, &request, idle_wait_ms, cycle_ms, keep_going);
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
    const trimmed = std.mem.trim(u8, host orelse "", " \t");
    if (trimmed.len == 0) return true;
    if (trimmed[0] == '[') {
        const close_at = std.mem.indexOfScalar(u8, trimmed, ']') orelse return true;
        if (std.mem.indexOfScalarPos(u8, trimmed, close_at, ':') == null) return true;
    }
    const name = hostOf(trimmed);
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
        error.ReadFailed, error.BodyTruncated => read_failed,
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

pub const AcceptOutcome = enum {
    serve_again,
    back_off,
    stop,
};

pub const accept_backoff_ms: i32 = 100;

pub fn classifyAccept(failure: std.Io.net.Server.AcceptError) AcceptOutcome {
    return switch (failure) {
        error.ConnectionAborted, error.WouldBlock => .serve_again,
        error.ProcessFdQuotaExceeded, error.SystemFdQuotaExceeded, error.SystemResources => .back_off,
        error.SocketNotListening,
        error.NetworkDown,
        error.BlockedByFirewall,
        error.ProtocolFailure,
        error.Unexpected,
        error.Canceled,
        => .stop,
    };
}

pub const Bind = struct {
    host: []const u8,
    port: u16,
};

pub const BindFailure = error{
    NoPort,
    NoHost,
    UnclosedBracket,
    UnbracketedIpv6,
    NotAPort,
};

pub fn parseBind(bind: []const u8) BindFailure!Bind {
    const colon = std.mem.lastIndexOfScalar(u8, bind, ':') orelse return error.NoPort;
    if (bind[0] == '[' and std.mem.indexOfScalar(u8, bind, ']') == null) return error.UnclosedBracket;
    const host = hostOf(bind);
    if (host.len == 0) return error.NoHost;
    if (bind[0] != '[' and std.mem.indexOfScalar(u8, host, ':') != null) return error.UnbracketedIpv6;
    const port = std.fmt.parseInt(u16, std.mem.trim(u8, bind[colon + 1 ..], " "), 10) catch return error.NotAPort;
    return .{ .host = host, .port = port };
}

pub const Answer = union(enum) {
    refusal: Refusal,
    not_found,
};

pub fn bodyAllowedFor(method: []const u8) bool {
    return !std.ascii.eqlIgnoreCase(method, "HEAD");
}

pub const media_refused = Refusal{
    .status = "415 Unsupported Media Type",
    .code = "unsupported_media_type",
    .message = "a request with a body declares application/json; the daemon reads no other media type",
};

pub fn answer(allow: []const []const u8, request: Request) Answer {
    if (request.origin) return .{ .refusal = origin_refused };
    if (hostRefused(allow, request.host)) return .{ .refusal = host_refused };
    if (carriesBody(request) and !declaresJson(request.content_type)) return .{ .refusal = media_refused };
    return .not_found;
}

pub fn carriesBody(request: Request) bool {
    return request.content_length > 0;
}

pub fn declaresJson(content_type: ?[]const u8) bool {
    const named = content_type orelse return false;
    const media = std.mem.trim(u8, named, " \t");
    const cut = std.mem.indexOfScalar(u8, media, ';') orelse media.len;
    if (!std.ascii.eqlIgnoreCase(std.mem.trim(u8, media[0..cut], " \t"), "application/json")) return false;
    return parametersWellFormed(media[cut..]);
}

const parameter_store_bytes = 2 * max_header_bytes;

fn parametersWellFormed(parameters: []const u8) bool {
    var store: [parameter_store_bytes]u8 = undefined;
    var used: usize = 0;
    var rest = std.mem.trim(u8, parameters, " \t");
    while (true) {
        rest = std.mem.trimStart(u8, rest, " \t");
        if (rest.len == 0) return true;
        if (rest[0] != ';') return false;
        rest = std.mem.trimStart(u8, rest[1..], " \t");
        if (rest.len == 0) return true;
        if (rest[0] == ';') return false;
        const name_end = std.mem.indexOfScalar(u8, rest, '=') orelse return false;
        const name = std.mem.trim(u8, rest[0..name_end], " \t");
        if (!isToken(name)) return false;
        var after = std.mem.trimStart(u8, rest[name_end + 1 ..], " \t");
        const entry = used;
        if (entry + 4 + name.len >= store[0..].len) return false;
        if (after.len > 0 and after[0] == '"') {
            const closed = closingQuote(after) orelse return false;
            const raw = after[1..closed];
            if (entry + 4 + name.len + raw.len > store[0..].len) return false;
            used = appendQuoted(store[0..], entry, name, raw);
            after = std.mem.trim(u8, after[closed + 1 ..], " \t");
            if (after.len != 0 and after[0] != ';') return false;
        } else {
            const next = std.mem.indexOfScalar(u8, after, ';') orelse after.len;
            const value = std.mem.trim(u8, after[0..next], " \t");
            if (!isToken(value)) return false;
            if (entry + 4 + name.len + value.len > store[0..].len) return false;
            used = appendToken(store[0..], entry, name, value);
            after = after[next..];
        }
        if (entryHasConflict(store[0..used], entry, name)) return false;
        const semi = std.mem.indexOfScalar(u8, after, ';') orelse after.len;
        if (semi == after.len) {
            if (std.mem.trim(u8, after, " \t").len != 0) return false;
            return true;
        }
        rest = after[semi..];
    }
}

fn writeHeader(store: []u8, entry: usize, name_len: usize, value_len: usize) void {
    std.mem.writeInt(u16, store[entry..][0..2], @intCast(name_len), .big);
    std.mem.writeInt(u16, store[entry + 2 ..][0..2], @intCast(value_len), .big);
}

fn appendToken(store: []u8, entry: usize, name: []const u8, value: []const u8) usize {
    writeHeader(store, entry, name.len, value.len);
    var cursor = entry + 4;
    @memcpy(store[cursor..][0..name.len], name);
    cursor += name.len;
    @memcpy(store[cursor..][0..value.len], value);
    return cursor + value.len;
}

fn escapeAt(raw: []const u8, at: usize) bool {
    return raw[at] == '\\' and at + 1 < raw.len and isTspecial(raw[at + 1]);
}

fn isTspecial(byte: u8) bool {
    return std.mem.indexOfScalar(u8, "()<>@,;:\\\"/[]?=", byte) != null;
}

fn appendQuoted(store: []u8, entry: usize, name: []const u8, raw: []const u8) usize {
    var decoded: usize = 0;
    var at: usize = 0;
    while (at < raw.len) : (at += 1) {
        if (escapeAt(raw, at)) at += 1;
        decoded += 1;
    }
    writeHeader(store, entry, name.len, decoded);
    var cursor = entry + 4;
    @memcpy(store[cursor..][0..name.len], name);
    cursor += name.len;
    at = 0;
    while (at < raw.len) : (at += 1) {
        if (escapeAt(raw, at)) {
            store[cursor] = raw[at + 1];
            cursor += 1;
            at += 1;
            continue;
        }
        store[cursor] = raw[at];
        cursor += 1;
    }
    return cursor;
}

fn entryHasConflict(store: []u8, entry: usize, name: []const u8) bool {
    const this_value_len = std.mem.readInt(u16, store[entry + 2 ..][0..2], .big);
    const this_value = store[entry + 4 + name.len ..][0..this_value_len];
    var at: usize = 0;
    while (at < entry) {
        const prior_name_len = std.mem.readInt(u16, store[at..][0..2], .big);
        const prior_value_len = std.mem.readInt(u16, store[at + 2 ..][0..2], .big);
        const prior_name = store[at + 4 ..][0..prior_name_len];
        const prior_value = store[at + 4 + prior_name_len ..][0..prior_value_len];
        if (std.ascii.eqlIgnoreCase(prior_name, name) and
            (prior_value_len != this_value.len or !std.mem.eql(u8, prior_value, this_value)))
        {
            return true;
        }
        at += 4 + prior_name_len + prior_value_len;
    }
    return false;
}

fn closingQuote(quoted: []const u8) ?usize {
    var at: usize = 1;
    while (at < quoted.len) : (at += 1) {
        if (quoted[at] == '\\') {
            at += 1;
            continue;
        }
        if (quoted[at] == '"') return at;
    }
    return null;
}

fn isToken(text: []const u8) bool {
    if (text.len == 0) return false;
    for (text) |byte| {
        if (std.ascii.isAlphanumeric(byte)) continue;
        if (std.mem.indexOfScalar(u8, "!#$%&'*+-.^_`|~{}", byte) == null) return false;
    }
    return true;
}

pub fn writeAnswer(stream: *compat.net.Stream, arena: std.mem.Allocator, next_id: u64, given: Answer, body_allowed: bool) !void {
    switch (given) {
        .refusal => |refused| {
            const body = try refusalEnvelope(arena, next_id, refused);
            try writeHead(stream, refused.status, "application/json", body, body_allowed);
        },
        .not_found => try writeHead(stream, "404 Not Found", "text/plain; charset=utf-8", not_found_body, body_allowed),
    }
}

pub fn addressText(arena: std.mem.Allocator, bound: compat.net.Address) ![]const u8 {
    return switch (bound) {
        .ip4 => |four| try std.fmt.allocPrint(arena, "{d}.{d}.{d}.{d}:{d}", .{ four.bytes[0], four.bytes[1], four.bytes[2], four.bytes[3], bound.getPort() }),
        .ip6 => |six| try std.fmt.allocPrint(arena, "[{x}:{x}:{x}:{x}:{x}:{x}:{x}:{x}]:{d}", .{
            std.mem.readInt(u16, six.bytes[0..2], .big),
            std.mem.readInt(u16, six.bytes[2..4], .big),
            std.mem.readInt(u16, six.bytes[4..6], .big),
            std.mem.readInt(u16, six.bytes[6..8], .big),
            std.mem.readInt(u16, six.bytes[8..10], .big),
            std.mem.readInt(u16, six.bytes[10..12], .big),
            std.mem.readInt(u16, six.bytes[12..14], .big),
            std.mem.readInt(u16, six.bytes[14..16], .big),
            bound.getPort(),
        }),
    };
}

pub fn connectionPending(server: *const compat.net.Server, wait_ms: i32) bool {
    if (comptime !pollable) return true;
    const ready = compat.net.readableWithin(compat.net.serverHandle(server), wait_ms) catch return false;
    return ready;
}

pub fn writeTransportFailure(stream: *compat.net.Stream, arena: std.mem.Allocator, next_id: u64, failure: Failure, body_allowed: bool) !void {
    if (transportRefusal(failure)) |refused| {
        const body = try refusalEnvelope(arena, next_id, refused);
        return writeHead(stream, refused.status, "application/json", body, body_allowed);
    }
    return writeHead(stream, statusFor(failure), "text/plain; charset=utf-8", "bad request", body_allowed);
}

fn writeHead(stream: *compat.net.Stream, status: []const u8, content_type: []const u8, body: []const u8, body_allowed: bool) !void {
    var counted: [24]u8 = undefined;
    const length = try std.fmt.bufPrint(&counted, "{d}\r\n\r\n", .{body.len});
    try stream.writeAll("HTTP/1.1 ");
    try stream.writeAll(status);
    try stream.writeAll("\r\nContent-Type: ");
    try stream.writeAll(content_type);
    try stream.writeAll("\r\nContent-Length: ");
    try stream.writeAll(length);
    if (body_allowed) try stream.writeAll(body);
}

const not_found_body = "not found";

pub fn defaultBind() []const u8 {
    return "127.0.0.1:6270";
}

const testing = std.testing;
const test_cycle_ms: i32 = 20;
var discarded: usize = 0;

var spare = true;

fn fresh() *bool {
    spare = true;
    return &spare;
}

const Pipe = struct {
    server: compat.net.Server,
    client: compat.net.Stream,
    accepted: compat.net.Stream,
    answered: bool = false,

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

    fn closeAccepted(self: *Pipe) void {
        if (self.answered) return;
        self.answered = true;
        self.accepted.close();
    }

    fn close(self: *Pipe) void {
        self.closeAccepted();
        self.client.close();
        compat.net.closeServer(&self.server);
    }
};

fn readToEnd(stream: *compat.net.Stream, buffer: []u8) ![]const u8 {
    var filled: usize = 0;
    while (filled < buffer.len) {
        const n = try stream.read(buffer[filled..]);
        if (n == 0) break;
        filled += n;
    }
    return buffer[0..filled];
}

fn parseUnder(allocator: std.mem.Allocator, raw: []const u8) !void {
    var pipe = try Pipe.openWith(allocator);
    defer pipe.close();
    try pipe.client.writeAll(raw);
    var request = try readRequest(allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh());
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
    return readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh());
}

test "a body must declare application/json, and only that" {
    const loopback: []const []const u8 = &.{};

    var wrong = try requestOver("POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\n{}");
    defer wrong.deinit(testing.allocator);
    try testing.expectEqualStrings("unsupported_media_type", answer(loopback, wrong).refusal.code);

    var silent = try requestOver("POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Length: 2\r\n\r\n{}");
    defer silent.deinit(testing.allocator);
    try testing.expectEqualStrings("unsupported_media_type", answer(loopback, silent).refusal.code);

    var right = try requestOver("POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}");
    defer right.deinit(testing.allocator);
    try testing.expectEqual(Answer.not_found, answer(loopback, right));
}

test "a charset is admitted, because application/json registers no parameters" {
    const loopback: []const []const u8 = &.{};
    for ([_][]const u8{ "utf-8", "utf8", "UTF-8", "latin1", "us-ascii", "not-a-charset" }) |charset| {
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: application/json; charset={s}\r\nContent-Length: 2\r\n\r\n{{}}", .{charset});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        try testing.expectEqual(Answer.not_found, answer(loopback, request));
    }
    var spaced = try requestOver("POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type:  Application/JSON ; charset=utf-8\r\nContent-Length: 2\r\n\r\n{}");
    defer spaced.deinit(testing.allocator);
    var tabbed = try requestOver("POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type:\tapplication/json\t;\tcharset=utf-8\r\nContent-Length: 2\r\n\r\n{}");
    defer tabbed.deinit(testing.allocator);
    try testing.expectEqual(Answer.not_found, answer(loopback, tabbed));
    try testing.expectEqual(Answer.not_found, answer(loopback, spaced));
}

test "a request with no body names no media type and is not refused for it" {
    const loopback: []const []const u8 = &.{};
    var listing = try requestOver("GET /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\n\r\n");
    defer listing.deinit(testing.allocator);
    try testing.expectEqual(Answer.not_found, answer(loopback, listing));
}

test "a parameter list that Go refuses is refused here too, and one it admits is admitted" {
    const loopback: []const []const u8 = &.{};
    for ([_][]const u8{
        "application/json; charset",
        "application/json; charset=",
        "application/json; =utf-8",
        "application/json; charset=utf-8; x",
        "application/json; x=\"unterminated",
        "application/json;;",
        "application/json; ;",
        "application/json;charset=\"utf-8\" junk; x=1",
        "application/json; a=1; a=2",
        "application/json; CHARSET=utf-8; charset=UTF-8",
        "application/json; a=\"x\\\"y\"; a=\"x\\\\y\"",
        "application/json; a=1; a=\"2\"",
        "application/json; a=\"x\"; a=\"y\"",
    }) |declared| {
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: {s}\r\nContent-Length: 2\r\n\r\n{{}}", .{declared});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        try testing.expectEqualStrings("unsupported_media_type", answer(loopback, request).refusal.code);
    }
    for ([_][]const u8{
        "application/json",
        "application/json;",
        "application/json ; charset=utf-8",
        "application/json;charset=utf-8;x=1",
        "application/json; charset=\"utf-8\"",
        "application/json; charset =utf-8",
        "application/json; charset= utf-8",
        "application/json; charset=\"a\\\"b\"",
        "application/json; charset=\"a;b\"",
        "application/json; charset= \"utf-8\"",
        "application/json; a=1; a=1",
        "application/json; a=\"x\"; a=x",
        "application/json; a=\"x\"; a=\"x\"",
        "application/json; A=1; a=1",
        "application/json; a=1; a=\"1\"",
        "application/json; a=\"x\\\"y\"; a=\"x\\\"y\"",
        "application/json; a={b}",
        "application/json; a={b}; c=1",
        "application/json; a=$b",
        "application/json; a=^b",
    }) |declared| {
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: {s}\r\nContent-Length: 2\r\n\r\n{{}}", .{declared});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        try testing.expectEqual(Answer.not_found, answer(loopback, request));
    }
}

test "sixty-five parameters and a long value are admitted, because the header bound is the limit" {
    const loopback: []const []const u8 = &.{};
    for ([_]usize{ 64, 65, 200 }) |count| {
        var list: std.ArrayListUnmanaged(u8) = .empty;
        defer list.deinit(testing.allocator);
        try list.appendSlice(testing.allocator, "application/json");
        for (0..count) |index| {
            const piece = try std.fmt.allocPrint(testing.allocator, "; p{d}={d}", .{ index, index });
            defer testing.allocator.free(piece);
            try list.appendSlice(testing.allocator, piece);
        }
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: {s}\r\nContent-Length: 2\r\n\r\n{{}}", .{list.items});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        try testing.expectEqual(Answer.not_found, answer(loopback, request));
    }
    const long_value = "x" ** 2048;
    const long_raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: application/json; a={s}\r\nContent-Length: 2\r\n\r\n{{}}", .{long_value});
    defer testing.allocator.free(long_raw);
    var long_request = try requestOver(long_raw);
    defer long_request.deinit(testing.allocator);
    try testing.expectEqual(Answer.not_found, answer(loopback, long_request));
}

test "a backslash is consumed only before a tspecial, which is what Go does" {
    const loopback: []const []const u8 = &.{};
    for ([_][]const u8{
        "application/json; a=\"C:\\\\path\"; a=\"C:\\\\path\"",
        "application/json; a=\"x\\\\qy\"; a=\"x\\\\qy\"",
    }) |declared| {
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: {s}\r\nContent-Length: 2\r\n\r\n{{}}", .{declared});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        try testing.expectEqual(Answer.not_found, answer(loopback, request));
    }
    for ([_][]const u8{
        "application/json; a=\"C:\\\\path\\\\x\"; a=C:pathx",
        "application/json; a=\"C:\\\\path\"; a=\"C:path\"",
        "application/json; a=\"x\\\\qy\"; a=\"xqy\"",
        "application/json; a=\"x\\\\1\"; a=\"x1\"",
        "application/json; a=\"x\\\\\\\\\"; a=\"x\\\\\"",
        "application/json; a=\"x\\\\\\\"\"; a=\"x\\\"\"",
        "application/json; a=\"x\\\\ \"; a=\"x \"",
        "application/json; a=\"x\\\\\\\"\"; a=x\\\"",
        "application/json; a=\"a\\\\;b=c\"; a=\"a;b=c\"",
    }) |declared| {
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: {s}\r\nContent-Length: 2\r\n\r\n{{}}", .{declared});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        try testing.expectEqualStrings("unsupported_media_type", answer(loopback, request).refusal.code);
    }
}

test "an empty parameter value is admitted quoted and refused bare, as Go does" {
    const loopback: []const []const u8 = &.{};
    const cases = [_]struct { declared: []const u8, refused: bool }{
        .{ .declared = "application/json; a=\"\"", .refused = false },
        .{ .declared = "application/json; a=\"\"; b=1", .refused = false },
        .{ .declared = "application/json; a=; b=1", .refused = true },
        .{ .declared = "application/json; a=; b=;", .refused = true },
        .{ .declared = "application/json; a=; b=1; c=2", .refused = true },
        .{ .declared = "application/json; a=;", .refused = true },
        .{ .declared = "application/json; a=\"\" b=1", .refused = true },
    };
    for (cases) |case| {
        const raw = try std.fmt.allocPrint(testing.allocator, "POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: {s}\r\nContent-Length: 2\r\n\r\n{{}}", .{case.declared});
        defer testing.allocator.free(raw);
        var request = try requestOver(raw);
        defer request.deinit(testing.allocator);
        if (case.refused) {
            try testing.expectEqualStrings("unsupported_media_type", answer(loopback, request).refusal.code);
        } else {
            try testing.expectEqual(Answer.not_found, answer(loopback, request));
        }
    }
}

test "a zero-length body with a wrong media type is not gated, because the gate reads length" {
    const loopback: []const []const u8 = &.{};
    var posted = try requestOver("POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: text/plain\r\nContent-Length: 0\r\n\r\n");
    defer posted.deinit(testing.allocator);
    try testing.expect(!carriesBody(posted));
    try testing.expectEqual(Answer.not_found, answer(loopback, posted));

    var empty = try requestOver("POST /adapters/a/sessions HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: text/plain\r\n\r\n");
    defer empty.deinit(testing.allocator);
    try testing.expect(!carriesBody(empty));
    try testing.expectEqual(Answer.not_found, answer(loopback, empty));

    var closed = try requestOver("POST /sessions/s/close HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: text/plain\r\nContent-Length: 0\r\n\r\n");
    defer closed.deinit(testing.allocator);
    try testing.expect(!carriesBody(closed));
    try testing.expectEqual(Answer.not_found, answer(loopback, closed));
}

test "a listing carrying a body with a wrong media type is gated, because the gate reads the head" {
    const loopback: []const []const u8 = &.{};
    var listing = try requestOver("GET /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nContent-Type: text/plain\r\nContent-Length: 4\r\n\r\nbody");
    defer listing.deinit(testing.allocator);
    try testing.expect(carriesBody(listing));
    try testing.expectEqualStrings("unsupported_media_type", answer(loopback, listing).refusal.code);
}

test "the Origin refusal is still the one that comes first" {
    var request = try requestOver("POST /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\nOrigin: http://elsewhere.test\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\n{}");
    defer request.deinit(testing.allocator);
    try testing.expectEqualStrings("cross_origin_request", answer(&.{}, request).refusal.code);
}

test "a HEAD request is read like any other, so the answer can be shaped from its method" {
    var request = try requestOver("HEAD /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\n\r\n");
    defer request.deinit(testing.allocator);
    try testing.expectEqualStrings("HEAD", request.method);
    try testing.expectEqualStrings("/adapters", request.split.path);
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

test "every header this reader owns is taken once, so a second copy is refused rather than kept" {
    const cases = [_][]const u8{
        "POST /a HTTP/1.1\r\nHost: a\r\nContent-Type: application/json\r\nContent-Type: text/plain\r\n\r\n",
        "POST /a HTTP/1.1\r\nHost: a\r\nContent-Length: 0\r\nContent-Length: 2\r\n\r\n",
        "POST /a HTTP/1.1\r\nHost: a\r\ncontent-type: application/json\r\nContent-Type: text/plain\r\n\r\n",
    };
    for (cases) |raw| try testing.expectError(error.Malformed, requestOver(raw));
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
    try testing.expectError(error.BodyTooLarge, readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh()));
}

test "a client that hangs up mid-body is refused request_read, as the draft pins it" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 64\r\n\r\n\"{\"a\":");
    const io = if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
    pipe.client.inner.shutdown(io, .send) catch return error.NoHalfClose;
    try testing.expectError(error.BodyTruncated, readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh()));
    var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch_state.deinit();
    try writeTransportFailure(&pipe.accepted, scratch_state.allocator(), 4, error.BodyTruncated, true);
    pipe.closeAccepted();
    var spoken: [4096]u8 = undefined;
    const said = try readToEnd(&pipe.client, &spoken);
    try testing.expect(std.mem.startsWith(u8, said, "HTTP/1.1 400 Bad Request"));
    try testing.expect(std.mem.indexOf(u8, said, "request_read") != null);
    try testing.expect(std.mem.indexOf(u8, said, "error.response") != null);
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
    try testing.expectError(error.Truncated, readRequest(testing.allocator, &accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh()));
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
    try testing.expectError(error.Timeout, readRequest(testing.allocator, &pipe.accepted, 50, idle_read_ms, test_cycle_ms, always_going, fresh()));
    try testing.expectEqualStrings(request_timeout, statusFor(error.Timeout));
}

test "the header budget is the whole request's, not a fresh one per byte" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("G");
    const started = elapsedMs() catch return error.TestUnexpectedResult;
    try testing.expectError(error.Timeout, readRequest(testing.allocator, &pipe.accepted, 120, idle_read_ms, test_cycle_ms, always_going, fresh()));
    try testing.expect((elapsedMs() catch return error.TestUnexpectedResult) - started < 2000);
}

test "a header whose name merely starts with a known one is not that header" {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("GET /adapters HTTP/1.1\r\nHostname: evil.test\r\nOrigin-Repeat: 1\r\nContent-Types: 7\r\n\r\n");
    var request = try readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh());
    defer request.deinit(testing.allocator);
    try testing.expect(request.host == null);
    try testing.expect(!request.origin);
    try testing.expect(request.content_type == null);
    try testing.expectEqual(@as(usize, 0), request.content_length);
}

test "an accept a peer aborted before the call is served again, not obeyed" {
    try testing.expectEqual(AcceptOutcome.serve_again, classifyAccept(error.ConnectionAborted));
    try testing.expectEqual(AcceptOutcome.serve_again, classifyAccept(error.WouldBlock));
}

test "running out of descriptors backs off rather than stopping, because a listener with no descriptors is not a dead listener" {
    for ([_]std.Io.net.Server.AcceptError{ error.ProcessFdQuotaExceeded, error.SystemFdQuotaExceeded, error.SystemResources }) |each| {
        try testing.expectEqual(AcceptOutcome.back_off, classifyAccept(each));
    }
    try testing.expect(accept_backoff_ms > 0);
    try testing.expect(accept_backoff_ms <= 1000);
}

test "only a failure of the listener itself stops the daemon, and the set is written out so a new error has to be a decision" {
    const fatal = [_]std.Io.net.Server.AcceptError{
        error.SocketNotListening,
        error.NetworkDown,
        error.BlockedByFirewall,
        error.ProtocolFailure,
        error.Unexpected,
        error.Canceled,
    };
    for (fatal) |each| try testing.expectEqual(AcceptOutcome.stop, classifyAccept(each));
}

test "a client that resets a connection the listener had not taken yet does not stop the hub" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const address = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
    var server = try compat.net.tcpListen(address, .{ .reuse_address = true });
    defer compat.net.closeServer(&server);
    for (0..8) |_| {
        var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
        const linger = std.posix.linger{ .onoff = 1, .linger = 0 };
        std.posix.setsockopt(@intCast(client.inner.socket.handle), std.posix.SOL.SOCKET, std.posix.SO.LINGER, std.mem.asBytes(&linger)) catch break;
        client.close();
    }
    var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
    defer client.close();
    try testing.expect(connectionPending(&server, 2000));
    const connection = try compat.net.accept(&server);
    var accepted = connection.stream;
    accepted.close();
    try testing.expect(compat.net.listenAddress(&server).getPort() != 0);
}

test "a bracketed Host with no port is refused, because Go keeps the brackets and does not admit it" {
    const allow = loopbackHosts("[::1]:6270").?;
    try testing.expect(hostRefused(allow, "[::1]"));
    try testing.expect(hostRefused(allow, "[::1"));
    try testing.expect(!hostRefused(allow, "[::1]:6270"));
    try testing.expect(!hostRefused(allow, "[::1]:9999"));
    try testing.expect(!hostRefused(allow, "127.0.0.1:6270"));
    try testing.expect(hostRefused(allow, "]"));
}

test "a HEAD is answered with the length a GET would send and no body at all" {
    for ([_][]const u8{ "HEAD", "head", "Head" }) |method| {
        try testing.expect(!bodyAllowedFor(method));
    }
    for ([_][]const u8{ "GET", "POST", "PUT", "DELETE" }) |method| {
        try testing.expect(bodyAllowedFor(method));
    }
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("HEAD /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\n\r\n");
    var request = try readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh());
    request.deinit(testing.allocator);
    var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch_state.deinit();
    try writeAnswer(&pipe.accepted, scratch_state.allocator(), 1, .not_found, bodyAllowedFor("HEAD"));
    pipe.closeAccepted();
    var spoken: [4096]u8 = undefined;
    const said = try readToEnd(&pipe.client, &spoken);
    try testing.expect(std.mem.startsWith(u8, said, "HTTP/1.1 404 Not Found"));
    try testing.expect(std.mem.indexOf(u8, said, "Content-Length: 9") != null);
    try testing.expect(std.mem.indexOf(u8, said, "not found") == null);
    try testing.expectEqualStrings("HTTP/1.1 404 Not Found\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: 9\r\n\r\n", said);
}

test "a HEAD whose read fails after its method gets no body either" {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("HEAD / HTTP/1.1\r\nHost: a\r\nContent-Length: 16777217\r\n\r\n");
    var body_allowed = true;
    try testing.expectError(error.BodyTooLarge, readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, &body_allowed));
    try testing.expect(!body_allowed);
    var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch_state.deinit();
    try writeTransportFailure(&pipe.accepted, scratch_state.allocator(), 9, error.BodyTooLarge, body_allowed);
    pipe.closeAccepted();
    var spoken: [4096]u8 = undefined;
    const said = try readToEnd(&pipe.client, &spoken);
    const end_of_headers = std.mem.indexOf(u8, said, "\r\n\r\n").? + 4;
    try testing.expectEqual(end_of_headers, said.len);

    var other = try Pipe.open();
    defer other.close();
    const get_allowed = true;
    try writeTransportFailure(&other.accepted, scratch_state.allocator(), 9, error.BodyTooLarge, get_allowed);
    other.closeAccepted();
    var spoken_get: [4096]u8 = undefined;
    const got = try readToEnd(&other.client, &spoken_get);
    const get_headers = std.mem.indexOf(u8, got, "\r\n\r\n").? + 4;
    try testing.expectEqual(get_headers, end_of_headers);
    try testing.expectEqualStrings(said, got[0..end_of_headers]);
    try testing.expect(got.len > get_headers);
}

test "a bind is read as a host and a port, and a malformed one says which part it is" {
    const refused = [_]struct { bind: []const u8, want: BindFailure }{
        .{ .bind = "127.0.0.1", .want = error.NoPort },
        .{ .bind = ":0", .want = error.NoHost },
        .{ .bind = "[::1", .want = error.UnclosedBracket },
        .{ .bind = "::1", .want = error.UnbracketedIpv6 },
        .{ .bind = "127.0.0.1:0:0", .want = error.UnbracketedIpv6 },
        .{ .bind = "127.0.0.1:port", .want = error.NotAPort },
        .{ .bind = "127.0.0.1:99999", .want = error.NotAPort },
        .{ .bind = "", .want = error.NoPort },
    };
    for (refused) |case| {
        testing.expectError(case.want, parseBind(case.bind)) catch |err| {
            std.debug.print("parseBind(\"{s}\") gave {s}, wanted {s}\n", .{ case.bind, @errorName(err), @errorName(case.want) });
            return err;
        };
    }
    const four = try parseBind("127.0.0.1:6270");
    try testing.expectEqualStrings("127.0.0.1", four.host);
    try testing.expectEqual(@as(u16, 6270), four.port);
    const six = try parseBind("[::1]:0");
    try testing.expectEqualStrings("::1", six.host);
    try testing.expectEqual(@as(u16, 0), six.port);
    const named = try parseBind("localhost:1");
    try testing.expectEqualStrings("localhost", named.host);
    try testing.expectEqual(@as(u16, 1), named.port);
}

test "a request carrying both an Origin and a foreign Host is refused the Origin first" {
    const allow = loopbackHosts("127.0.0.1:6270").?;
    const both = [_]Answer{
        answer(allow, .{ .method = "GET", .target = "GET", .split = .{ .path = "/adapters", .query = "" }, .host = "evil.test", .origin = true }),
        answer(allow, .{ .method = "GET", .target = "GET", .split = .{ .path = "/adapters", .query = "" }, .host = "evil.test" }),
        answer(allow, .{ .method = "GET", .target = "GET", .split = .{ .path = "/adapters", .query = "" }, .host = "127.0.0.1:6270" }),
        answer(&.{}, .{ .method = "GET", .target = "GET", .split = .{ .path = "/adapters", .query = "" }, .host = "evil.test" }),
    };
    try testing.expectEqualStrings("cross_origin_request", both[0].refusal.code);
    try testing.expectEqualStrings("unrecognized_host", both[1].refusal.code);
    try testing.expect(both[2] == .not_found);
    try testing.expect(both[3] == .not_found);
}

test "the daemon answers over a real socket, and the bytes say which refusal it was" {
    const allow = loopbackHosts("127.0.0.1:0").?;
    const cases = [_]struct { raw: []const u8, want: []const u8 }{
        .{ .raw = "GET /adapters HTTP/1.1\r\nHost: 127.0.0.1:6270\r\n\r\n", .want = "404" },
        .{ .raw = "GET /adapters HTTP/1.1\r\nHost: LOCALHOST\r\n\r\n", .want = "404" },
        .{ .raw = "GET /adapters HTTP/1.1\r\nHost: evil.test\r\n\r\n", .want = "unrecognized_host" },
        .{ .raw = "GET /adapters HTTP/1.1\r\nHost: a\r\nOrigin: http://evil.test\r\n\r\n", .want = "cross_origin_request" },
        .{ .raw = "GET /adapters HTTP/1.1\r\nHost: evil.test\r\nOrigin: http://evil.test\r\n\r\n", .want = "cross_origin_request" },
    };
    for (cases, 0..) |case, index| {
        var pipe = try Pipe.open();
        defer pipe.close();
        try pipe.client.writeAll(case.raw);
        var request = try readRequest(testing.allocator, &pipe.accepted, header_read_ms, idle_read_ms, test_cycle_ms, always_going, fresh());
        defer request.deinit(testing.allocator);
        var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer scratch_state.deinit();
        try writeAnswer(&pipe.accepted, scratch_state.allocator(), index + 1, answer(allow, request), bodyAllowedFor(request.method));
        pipe.closeAccepted();
        var spoken: [64 * 1024]u8 = undefined;
        const said = try readToEnd(&pipe.client, &spoken);
        try testing.expect(std.mem.startsWith(u8, said, "HTTP/1.1 "));
        try testing.expect(std.mem.indexOf(u8, said, case.want) != null);
        if (index == 0) try testing.expect(std.mem.indexOf(u8, said, "error.response") == null);
    }
}

test "the accept poll reports an idle listener as idle and a waiting one as waiting" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const address = try compat.net.resolveAddress(testing.allocator, "127.0.0.1", 0);
    var server = try compat.net.tcpListen(address, .{ .reuse_address = true });
    defer compat.net.closeServer(&server);
    try testing.expect(!connectionPending(&server, 20));
    var client = try compat.net.tcpConnect(compat.net.listenAddress(&server));
    try testing.expect(connectionPending(&server, 2000));
    const connection = try compat.net.accept(&server);
    var accepted = connection.stream;
    accepted.close();
    client.close();
}

test "the banner names the bound address, IPv4 plainly and IPv6 bracketed" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const four: compat.net.Address = .{ .ip4 = .{ .bytes = .{ 127, 0, 0, 1 }, .port = 52402 } };
    try testing.expectEqualStrings("127.0.0.1:52402", try addressText(arena, four));
    const six: compat.net.Address = .{ .ip6 = .{
        .bytes = .{ 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1 },
        .port = 63829,
    } };
    try testing.expectEqualStrings("[0:0:0:0:0:0:0:1]:63829", try addressText(arena, six));
}

test "a request that could not be read is answered as a transport failure, never as an empty envelope" {
    const cases = [_]struct { failure: Failure, want: []const u8, envelope: bool }{
        .{ .failure = error.BodyTooLarge, .want = "413", .envelope = true },
        .{ .failure = error.ReadFailed, .want = "request_read", .envelope = true },
        .{ .failure = error.BodyTruncated, .want = "request_read", .envelope = true },
        .{ .failure = error.Malformed, .want = "400", .envelope = false },
        .{ .failure = error.Timeout, .want = "408", .envelope = false },
        .{ .failure = error.HeaderTooLarge, .want = "431", .envelope = false },
    };
    for (cases) |case| {
        var pipe = try Pipe.open();
        defer pipe.close();
        var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
        defer scratch_state.deinit();
        try writeTransportFailure(&pipe.accepted, scratch_state.allocator(), 7, case.failure, true);
        pipe.closeAccepted();
        var spoken: [4096]u8 = undefined;
        const said = try readToEnd(&pipe.client, &spoken);
        try testing.expect(std.mem.startsWith(u8, said, "HTTP/1.1 "));
        try testing.expect(std.mem.indexOf(u8, said, case.want) != null);
        try testing.expect((std.mem.indexOf(u8, said, "error.response") != null) == case.envelope);
        if (case.envelope) try testing.expect(std.mem.indexOf(u8, said, "oap-error-7") != null);
        const at = std.mem.indexOf(u8, said, "Content-Length: ") orelse return error.NoLength;
        const declared = std.fmt.parseInt(usize, std.mem.sliceTo(said[at + "Content-Length: ".len ..], '\r'), 10) catch return error.BadLength;
        const body = said[std.mem.indexOf(u8, said, "\r\n\r\n").? + 4 ..];
        try testing.expectEqual(declared, body.len);
        try testing.expect(declared > 0);
    }
}

const flipped = struct {
    var stop: std.atomic.Value(bool) = .init(false);
    const context: u8 = 0;

    fn after(_: *const anyopaque) bool {
        return !stop.load(.acquire);
    }

    fn waitThenFlip() void {
        compat.time.sleepMs(120);
        stop.store(true, .release);
    }
};

test "a poll waiting on a silent peer is re-checked inside its cycle, not held to its deadline" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    flipped.stop.store(false, .release);
    const thread = try std.Thread.spawn(.{}, flipped.waitThenFlip, .{});
    defer thread.join();
    var body = try bodyOf(testing.allocator, 8);
    defer body.deinit(testing.allocator);
    const started = compat.time.nowMillis();
    const outcome = readBody(testing.allocator, &pipe.accepted, &body, 120_000, 20, .{ .context = @ptrCast(&flipped), .check = flipped.after });
    const spent = compat.time.nowMillis() - started;
    try testing.expectError(error.Stopped, outcome);
    try testing.expect(spent < 3000);
    try testing.expectEqual(@as(usize, 0), body.filled);
}

test "a body that arrives in part is taken as it comes, and never waited on for the rest" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/memory/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 64\r\n\r\n{\"a\":");
    var body_allowed = true;
    var head = try readHead(testing.allocator, &pipe.accepted, header_read_ms, test_cycle_ms, always_going, &body_allowed, &discarded);
    defer head.deinit(testing.allocator);
    flipped.stop.store(false, .release);
    const thread = try std.Thread.spawn(.{}, flipped.waitThenFlip, .{});
    defer thread.join();
    const started = compat.time.nowMillis();
    const outcome = readBody(testing.allocator, &pipe.accepted, &head, 120_000, 20, .{ .context = @ptrCast(&flipped), .check = flipped.after });
    const spent = compat.time.nowMillis() - started;
    try testing.expectError(error.Stopped, outcome);
    try testing.expect(spent < 3000);
    try testing.expectEqual(@as(usize, 5), head.filled);
    try testing.expectEqualStrings("{\"a\":", head.body[0..head.filled]);
}

test "a read in flight gives up when the daemon is told to stop, rather than waiting out its deadline" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 8\r\n\r\n{\"a\":");
    const started = elapsedMs() catch return error.TestUnexpectedResult;
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var body = try bodyOf(arena, 8);
    try testing.expectError(error.Stopped, readBody(arena, &pipe.accepted, &body, 60_000, 20, .{
        .context = undefined,
        .check = struct {
            fn stop(_: *const anyopaque) bool {
                return false;
            }
        }.stop,
    }));
    try testing.expect((elapsedMs() catch return error.TestUnexpectedResult) - started < 500);
}

fn bodyOf(allocator: std.mem.Allocator, length: usize) Failure!Request {
    var request = Request{ .method = &.{}, .target = &.{}, .split = .{ .path = &.{}, .query = &.{} }, .content_length = length };
    errdefer request.deinit(allocator);
    request.method = try allocator.dupe(u8, "POST");
    request.target = try allocator.dupe(u8, "/");
    request.split = try splitTarget(allocator, "/");
    return request;
}

test "a body the daemon refused to read is drained before the socket closes, or the close resets the answer away" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: evil.test\r\nContent-Type: application/json\r\nContent-Length: 12\r\n\r\n{\"a\":1,\"b\"");
    var body_allowed = true;
    var declared: usize = 0;
    var head = try readHead(testing.allocator, &pipe.accepted, header_read_ms, test_cycle_ms, always_going, &body_allowed, &declared);
    defer head.deinit(testing.allocator);
    try testing.expectEqual(@as(usize, 12), declared);
    var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch_state.deinit();
    try writeAnswer(&pipe.accepted, scratch_state.allocator(), 3, answer(loopbackHosts("127.0.0.1:0").?, head), body_allowed);
    _ = drain(&pipe.accepted, head.content_length, always_going);
    pipe.closeAccepted();
    var spoken: [4096]u8 = undefined;
    const said = try readToEnd(&pipe.client, &spoken);
    try testing.expect(std.mem.startsWith(u8, said, "HTTP/1.1 403 Forbidden"));
    try testing.expect(std.mem.indexOf(u8, said, "unrecognized_host") != null);
}

test "a drain gives up rather than waiting on a peer that sends nothing more" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    const started = compat.time.nowMillis();
    _ = drain(&pipe.accepted, 4096, always_going);
    try testing.expect(compat.time.nowMillis() - started < 2000);
}

test "a drain stops at its byte cap and reports what it consumed" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    const wanted_cap: usize = 1024 * 1024;
    try testing.expectEqual(wanted_cap, drain_total_cap_bytes);
    const owed: usize = wanted_cap + 512 * 1024;
    var writer = try std.Thread.spawn(.{}, flood, .{ &pipe.client, owed });
    const spent = drain(&pipe.accepted, owed, always_going);
    pipe.closeAccepted();
    writer.join();
    try testing.expectEqual(wanted_cap, spent);
    try testing.expect(spent < owed);
}

fn flood(client: *compat.net.Stream, total: usize) void {
    const block = "z" ** 4096;
    var sent: usize = 0;
    while (sent < total) {
        const take = @min(block.len, total - sent);
        client.writeAll(block[0..take]) catch return;
        sent += take;
    }
}

test "a drain reads a positive number of bytes, and stops when keepGoing says stop" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("u" ** 4096);
    var one = stop_after_one{};
    const keeping: KeepGoing = .{ .context = &one, .check = stop_after_one.check };
    const first = drain(&pipe.accepted, 4096, always_going);
    try testing.expectEqual(@as(usize, 4096), first);
    const spent = drain(&pipe.accepted, 4096, keeping);
    try testing.expectEqual(@as(usize, 0), spent);
    try testing.expect(one.asked.load(.seq_cst));
    try testing.expect(one.asks.load(.seq_cst) >= 2);
}

test "a drain reads on a long-lived process, because its budget is elapsed not uptime" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const origin = elapsedMs() catch return error.TestUnexpectedResult;
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("u" ** 4096);
    try testing.expectEqual(@as(usize, 4096), drain(&pipe.accepted, 4096, always_going));

    const wait_ms: u64 = @intCast(@as(u64, @intCast(drain_total_ms)) + 200);
    compat.time.sleepMs(wait_ms);

    const up = elapsedMs() catch return error.TestUnexpectedResult;
    try testing.expect(up -| origin >= wait_ms);
    try testing.expect(up > @as(u64, @intCast(drain_total_ms)));
    try pipe.client.writeAll("v" ** 4096);
    try testing.expectEqual(@as(usize, 4096), drain(&pipe.accepted, 4096, always_going));
}

const spends_the_budget = struct {
    client: *compat.net.Stream,
    started: u64,
    observed: u64 = 0,
    buffered: usize = 0,
    polls: usize = 0,

    fn check(context: *const anyopaque) bool {
        const self: *@This() = @ptrCast(@alignCast(@constCast(context)));
        _ = self.polls;
        self.polls += 1;
        if (self.buffered != 0) return true;
        var now = elapsedMs() catch return true;
        while (now -| self.started < @as(u64, @intCast(@max(drain_total_ms, 0)))) {
            compat.time.sleepMs(5);
            now = elapsedMs() catch return true;
        }
        self.observed = now -| self.started;
        self.client.writeAll("q" ** 4096) catch {};
        self.buffered = 4096;
        return true;
    }
};

test "a drain whose elapsed budget is already spent consumes nothing, and a budget it does not have would read" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var pipe = try Pipe.open();
    defer pipe.close();
    const before = try elapsedMs();
    var spender = spends_the_budget{ .client = &pipe.client, .started = before };
    const keeping: KeepGoing = .{ .context = &spender, .check = spends_the_budget.check };
    const spent = drain(&pipe.accepted, 8192, keeping);
    try testing.expect(spender.observed >= @as(u64, @intCast(@max(drain_total_ms, 0))));
    try testing.expectEqual(@as(usize, 4096), spender.buffered);
    try testing.expectEqual(@as(usize, 0), spent);
    try testing.expectEqual(@as(usize, 1), spender.polls);
}

const stop_after_one = struct {
    asked: std.atomic.Value(bool) = .init(false),
    asks: std.atomic.Value(usize) = .init(0),

    fn check(context: *const anyopaque) bool {
        const self: *@This() = @ptrCast(@alignCast(@constCast(context)));
        _ = self.asks.fetchAdd(1, .seq_cst);
        if (self.asked.load(.seq_cst)) return false;
        self.asked.store(true, .seq_cst);
        return true;
    }
};

test "the trust model is decided on the head, so a refused request never waits on a body it will not read" {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: evil.test\r\nOrigin: http://evil.test\r\nContent-Type: application/json\r\nContent-Length: 4096\r\n\r\n");
    var body_allowed = true;
    var head = try readHead(testing.allocator, &pipe.accepted, header_read_ms, test_cycle_ms, always_going, &body_allowed, &discarded);
    defer head.deinit(testing.allocator);
    try testing.expectEqual(@as(usize, 4096), head.content_length);
    try testing.expectEqual(@as(usize, 0), head.body.len);
    try testing.expectEqualStrings("cross_origin_request", answer(loopbackHosts("127.0.0.1:0").?, head).refusal.code);
    var scratch_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch_state.deinit();
    try writeAnswer(&pipe.accepted, scratch_state.allocator(), 2, answer(loopbackHosts("127.0.0.1:0").?, head), body_allowed);
    pipe.closeAccepted();
    var spoken: [4096]u8 = undefined;
    const said = try readToEnd(&pipe.client, &spoken);
    try testing.expect(std.mem.indexOf(u8, said, "cross_origin_request") != null);
}

test "a body is read only when a route wants it, and the read is the same one a GET would get" {
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 4\r\n\r\nbody");
    var body_allowed = true;
    var head = try readHead(testing.allocator, &pipe.accepted, header_read_ms, test_cycle_ms, always_going, &body_allowed, &discarded);
    defer head.deinit(testing.allocator);
    try testing.expectEqual(@as(usize, 0), head.body.len);
    try readBody(testing.allocator, &pipe.accepted, &head, idle_read_ms, test_cycle_ms, always_going);
    try testing.expectEqualStrings("body", head.body);
}

test "a request line with an empty method or an empty target is refused, not read as the token beside it" {
    const cases = [_][]const u8{
        " /adapters HTTP/1.1\r\nHost: a\r\n\r\n",
        "GET  HTTP/1.1\r\nHost: a\r\n\r\n",
        "  /adapters HTTP/1.1\r\nHost: a\r\n\r\n",
        "GET \r\nHost: a\r\n\r\n",
        "GET  /adapters  HTTP/1.1\r\nHost: a\r\n\r\n",
    };
    for (cases) |raw| try testing.expectError(error.Malformed, requestOver(raw));
}

test "a Content-Length of anything but ASCII digits is refused, as net/http refuses it" {
    for ([_][]const u8{ "+5", "1_0", "0x10", "-1", " 5", "5 ", "", "1,0", "５" }) |value| {
        try testing.expect(digits(value) == null);
    }
    for ([_]struct { text: []const u8, want: usize }{
        .{ .text = "0", .want = 0 },
        .{ .text = "5", .want = 5 },
        .{ .text = "4096", .want = 4096 },
        .{ .text = "000012", .want = 12 },
    }) |case| {
        try testing.expectEqual(case.want, digits(case.text).?);
    }
    const cases = [_][]const u8{
        "POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: +5\r\n\r\nhello",
        "POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 1_0\r\n\r\nhello",
        "POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: -1\r\n\r\n",
    };
    for (cases) |raw| try testing.expectError(error.Malformed, requestOver(raw));
    var pipe = try Pipe.open();
    defer pipe.close();
    try pipe.client.writeAll("POST /adapters/a/sessions HTTP/1.1\r\nHost: a\r\nContent-Length: 000012\r\n\r\n");
    var allowed = true;
    var declared: usize = 0;
    var request = try readHead(testing.allocator, &pipe.accepted, header_read_ms, test_cycle_ms, always_going, &allowed, &declared);
    defer request.deinit(testing.allocator);
    try testing.expectEqual(@as(usize, 12), request.content_length);
    try testing.expectEqual(@as(usize, 12), declared);
}

test "the default bind is loopback, so every default user keeps the allowlist" {
    const bind = defaultBind();
    try testing.expectEqualStrings("127.0.0.1:6270", bind);
    try testing.expect(loopbackHosts(bind) != null);
    const read = try parseBind(bind);
    try testing.expectEqualStrings("127.0.0.1", read.host);
    try testing.expectEqual(@as(u16, 6270), read.port);
    try testing.expect(!hostRefused(loopbackHosts(bind).?, "127.0.0.1:6270"));
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
