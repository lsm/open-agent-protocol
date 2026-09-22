const std = @import("std");
const gojson = @import("gojson");
const goquote = @import("goquote");

pub const frame_limit_default: usize = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidMessage,
    InvalidID,
};

pub const invalid_message_prefix = "hermes rpc: invalid JSON-RPC message";
pub const invalid_id_message = "hermes rpc: id must be a string or integer";
pub const frame_too_large_message = "hermes rpc: frame exceeds configured limit";

pub const Diagnostic = struct {
    message: []const u8 = "",

    fn invalid(self: *Diagnostic, comptime detail: []const u8) Error {
        self.message = invalid_message_prefix ++ ": " ++ detail;
        return Error.InvalidMessage;
    }

    fn tooLarge(self: *Diagnostic) Error {
        self.message = frame_too_large_message;
        return Error.FrameTooLarge;
    }

    fn invalidID(self: *Diagnostic) Error {
        self.message = invalid_id_message;
        return Error.InvalidID;
    }

    fn invalidNumericID(self: *Diagnostic, arena: std.mem.Allocator, raw: []const u8) Error {
        self.message = std.fmt.allocPrint(arena, "{s}: {s}", .{ invalid_id_message, goquote.quote(arena, raw) }) catch invalid_id_message;
        return Error.InvalidID;
    }

    fn quoted(self: *Diagnostic, arena: std.mem.Allocator, comptime shape: []const u8, value: []const u8) Error {
        self.message = std.fmt.allocPrint(arena, "{s}: " ++ shape, .{ invalid_message_prefix, goquote.quote(arena, value) }) catch invalid_message_prefix;
        return Error.InvalidMessage;
    }
};

pub const Kind = enum { request, notification, response, failure };

pub const Message = struct {
    kind: Kind,
    method: []const u8 = "",
    raw: []const u8,
};

const allowed_members = [_][]const u8{ "jsonrpc", "id", "method", "params", "result", "error" };
const allowed_error_members = [_][]const u8{ "code", "message", "data" };

fn allowed(names: []const []const u8, candidate: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, candidate)) return true;
    }
    return false;
}

pub const Decoder = struct {
    source: []const u8,
    at: usize = 0,
    limit: usize = frame_limit_default,

    pub fn next(self: *Decoder, arena: std.mem.Allocator, diagnostic: ?*Diagnostic) !?Message {
        var discard = Diagnostic{};
        const report = diagnostic orelse &discard;
        const line = try self.readFrame(report) orelse return null;
        return try parseMessage(arena, line, report);
    }

    fn readFrame(self: *Decoder, report: *Diagnostic) !?[]const u8 {
        if (self.at >= self.source.len) return null;
        const rest = self.source[self.at..];
        const break_at = std.mem.indexOfScalar(u8, rest, '\n') orelse {
            if (rest.len - 1 > self.limit) return report.tooLarge();
            return report.invalid("unterminated frame");
        };
        if (break_at > self.limit) return report.tooLarge();
        const line = rest[0..break_at];
        self.at += break_at + 1;
        if (line.len == 0) return report.invalid("empty frame");
        if (std.mem.indexOfScalar(u8, line, '\r') != null) return report.invalid("carriage return is not valid framing");
        if (!std.unicode.utf8ValidateSlice(line)) return report.invalid("frame is not UTF-8");
        return line;
    }
};

pub fn parseMessage(arena: std.mem.Allocator, line: []const u8, diagnostic: ?*Diagnostic) !Message {
    var discard = Diagnostic{};
    const report = diagnostic orelse &discard;
    if (!std.unicode.utf8ValidateSlice(line)) return report.invalid("frame is not UTF-8");
    if (line.len == 0 or line[0] != '{' or line[line.len - 1] != '}') return report.invalid("frame must be exactly one JSON object");

    const scan = gojson.replaceLoneSurrogates(arena, line);
    switch (gojson.walkFrame(arena, scan) catch gojson.Walk.ok) {
        .duplicate => |key| return report.quoted(arena, "duplicate object key {s}", key),
        .trailing => return report.invalid("trailing JSON value"),
        .too_deep => return report.invalid("exceeded max depth"),
        .ok => {},
    }

    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, scan, .{}) catch return report.invalid("frame is not decodable JSON");
    if (document != .object) return report.invalid("expected one object");
    if (!gojson.finiteNumbers(document)) return report.invalid("frame carries a number Go cannot decode");
    const object = document.object;

    var members = object.iterator();
    while (members.next()) |entry| {
        if (!allowed(&allowed_members, entry.key_ptr.*)) return report.quoted(arena, "unknown member {s}", entry.key_ptr.*);
    }

    const version = object.get("jsonrpc") orelse return report.invalid("jsonrpc must equal \"2.0\"");
    if (version != .string or !std.mem.eql(u8, version.string, "2.0")) return report.invalid("jsonrpc must equal \"2.0\"");

    const identity = object.get("id");
    const method = object.get("method");
    const params = object.get("params");
    const result = object.get("result");
    const failure = object.get("error");

    if (result != null and failure != null) return report.invalid("result and error are mutually exclusive");
    if (method != null and (result != null or failure != null)) return report.invalid("method cannot accompany a response");
    if (method == null and params != null) return report.invalid("params requires method");

    if (identity) |value| {
        switch (value) {
            .string, .integer => {},
            .number_string => |raw| return report.invalidNumericID(arena, raw),
            .float => |raw| return report.invalidNumericID(arena, std.fmt.allocPrint(arena, "{d}", .{raw}) catch ""),
            else => return report.invalidID(),
        }
    }

    var kind: Kind = undefined;
    if (method != null and identity != null) {
        kind = .request;
    } else if (method != null) {
        kind = .notification;
    } else if (identity != null and result != null) {
        kind = .response;
    } else if (identity != null and failure != null) {
        kind = .failure;
    } else {
        return report.invalid("unrecognized object shape");
    }

    var named: []const u8 = "";
    if (method) |value| {
        if (value != .string or value.string.len == 0) return report.invalid("method must be a non-empty string");
        named = value.string;
        if (params) |carried| {
            if (carried != .object and carried != .null) return report.invalid("params must be an object or null");
        }
    }
    if (failure) |value| {
        if (value == .null) return report.invalid("error code is required");
        if (value != .object) return report.invalid("malformed error object");
        var fields = value.object.iterator();
        while (fields.next()) |entry| {
            if (!allowed(&allowed_error_members, entry.key_ptr.*)) return report.quoted(arena, "unknown error member {s}", entry.key_ptr.*);
        }
        const code = value.object.get("code") orelse return report.invalid("error code is required");
        const detail = value.object.get("message") orelse return report.invalid("error message is required");
        if (code != .integer and code != .null) return report.invalid("malformed error object");
        if (detail != .string or detail.string.len == 0) return report.invalid("malformed error object");
    }
    return .{ .kind = kind, .method = named, .raw = line };
}

const testing = std.testing;

const Expectation = struct {
    frame: []const u8,
    verdict: []const u8,
    method: []const u8 = "",
};

const oracle = [_]Expectation{
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}", .verdict = "notification", .method = "x" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x\"}", .verdict = "request", .method = "x" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"a\",\"method\":\"x\"}", .verdict = "request", .method = "x" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}", .verdict = "response", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":null}", .verdict = "response", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":7}", .verdict = "response", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"no\"}}", .verdict = "failure", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"\"}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"message\":\"no\"}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":null,\"message\":\"no\"}}", .verdict = "failure", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":null}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"no\",\"data\":{\"a\":1}}}", .verdict = "failure", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"no\",\"extra\":1}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1.5,\"message\":\"no\"}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":7}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":null}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{}}", .verdict = "notification", .method = "x" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":null}", .verdict = "notification", .method = "x" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":[]}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":7}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":\"s\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"params\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":7}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":null}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"1.0\",\"method\":\"x\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":2.0,\"method\":\"x\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":null,\"method\":\"x\"}", .verdict = "refused" },
    .{ .frame = "{\"method\":\"x\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"error\":{\"code\":1,\"message\":\"m\"}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"error\":{\"code\":1,\"message\":\"m\"}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x\",\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x\",\"error\":{\"code\":1,\"message\":\"m\"}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1.5,\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":null,\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":true,\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":{},\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":[],\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"\",\"result\":{}}", .verdict = "response", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":9223372036854775807,\"result\":{}}", .verdict = "response", .method = "" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":9223372036854775808,\"result\":{}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"bogus\":1}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"Method\":\"y\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"method\":\"y\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"a\":1,\"a\":2}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"a\":[{\"b\":1,\"b\":2}]}}", .verdict = "refused" },
    .{ .frame = " {\"jsonrpc\":\"2.0\",\"method\":\"x\"}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"} ", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}{\"jsonrpc\":\"2.0\",\"method\":\"y\"}", .verdict = "refused" },
    .{ .frame = "[{\"jsonrpc\":\"2.0\",\"method\":\"x\"}]", .verdict = "refused" },
    .{ .frame = "null", .verdict = "refused" },
    .{ .frame = "", .verdict = "refused" },
    .{ .frame = "{}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"\\u00e9\"}", .verdict = "notification", .method = "é" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"n\":1e400}}", .verdict = "refused" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"n\":1e308}}", .verdict = "notification", .method = "x" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"a\":\"\\ud800\"}}", .verdict = "response", .method = "" },
};

test "every frame decodes exactly as the pinned Hermes codec decodes it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for (oracle) |expectation| {
        const refuses = std.mem.eql(u8, expectation.verdict, "refused");
        if (parseMessage(scratch, expectation.frame, null)) |message| {
            if (refuses) {
                std.debug.print("accepted a frame the oracle refuses: {s}\n", .{expectation.frame});
                return error.TestUnexpectedResult;
            }
            try testing.expectEqualStrings(expectation.verdict, @tagName(message.kind));
            try testing.expectEqualStrings(expectation.method, message.method);
        } else |_| {
            if (!refuses) {
                std.debug.print("refused a frame the oracle accepts: {s}\n", .{expectation.frame});
                return error.TestUnexpectedResult;
            }
        }
    }
}

fn framed(limit: usize, content: usize, terminated: bool) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const pad = try scratch.alloc(u8, content - 29);
    @memset(pad, 'x');
    const frame = try std.mem.concat(scratch, u8, &.{ "{\"jsonrpc\":\"2.0\",\"method\":\"", pad, "\"}" });
    const source = if (terminated) try std.mem.concat(scratch, u8, &.{ frame, "\n" }) else frame;
    var decoder = Decoder{ .source = source, .limit = limit };
    _ = try decoder.next(scratch, null);
}

fn framedError(limit: usize, content: usize, terminated: bool) anyerror {
    return if (framed(limit, content, terminated)) |_| error.TestUnexpectedResult else |err| err;
}

test "only a terminated frame over the limit is too large, an unterminated one is unterminated" {
    try framed(32, 30, true);
    try framed(32, 31, true);
    try framed(32, 32, true);
    try testing.expectEqual(Error.FrameTooLarge, framedError(32, 33, true));

    try testing.expectEqual(Error.InvalidMessage, framedError(32, 30, false));
    try testing.expectEqual(Error.InvalidMessage, framedError(32, 32, false));
    try testing.expectEqual(Error.InvalidMessage, framedError(32, 33, false));
    try testing.expectEqual(Error.FrameTooLarge, framedError(32, 34, false));
}

test "a frame nested past the oracle limit is refused before it is parsed" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for ([_]usize{ 9998, 9999 }) |arrays| {
        const opens = try scratch.alloc(u8, arrays);
        @memset(opens, '[');
        const closes = try scratch.alloc(u8, arrays);
        @memset(closes, ']');
        const frame = try std.mem.concat(scratch, u8, &.{ "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"a\":", opens, closes, "}}" });
        if (arrays == 9998) {
            const message = try parseMessage(scratch, frame, null);
            try testing.expectEqualStrings("x", message.method);
        } else {
            try testing.expectError(Error.InvalidMessage, parseMessage(scratch, frame, null));
        }
    }
}

const Refusal = struct {
    frame: []const u8,
    message: []const u8,
};

const refusals = [_]Refusal{
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"\xff\"}", .message = "hermes rpc: invalid JSON-RPC message: frame is not UTF-8" },
    .{ .frame = "[1]", .message = "hermes rpc: invalid JSON-RPC message: frame must be exactly one JSON object" },
    .{ .frame = " {\"jsonrpc\":\"2.0\",\"id\":9,\"result\":{}}", .message = "hermes rpc: invalid JSON-RPC message: frame must be exactly one JSON object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"} {\"x\":1}", .message = "hermes rpc: invalid JSON-RPC message: trailing JSON value" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"jsonrpc\":\"2.0\",\"method\":\"m\"}", .message = "hermes rpc: invalid JSON-RPC message: duplicate object key \"jsonrpc\"" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"bogus\":1}", .message = "hermes rpc: invalid JSON-RPC message: unknown member \"bogus\"" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"a\\\"b\\n\":1}", .message = "hermes rpc: invalid JSON-RPC message: unknown member \"a\\\"b\\n\"" },
    .{ .frame = "{\"method\":\"m\"}", .message = "hermes rpc: invalid JSON-RPC message: jsonrpc must equal \"2.0\"" },
    .{ .frame = "{\"jsonrpc\":2,\"method\":\"m\"}", .message = "hermes rpc: invalid JSON-RPC message: jsonrpc must equal \"2.0\"" },
    .{ .frame = "{\"jsonrpc\":\"1.0\",\"method\":\"m\"}", .message = "hermes rpc: invalid JSON-RPC message: jsonrpc must equal \"2.0\"" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"result\":{},\"error\":{\"code\":1,\"message\":\"x\"}}", .message = "hermes rpc: invalid JSON-RPC message: result and error are mutually exclusive" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"method\":\"m\",\"result\":{}}", .message = "hermes rpc: invalid JSON-RPC message: method cannot accompany a response" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"params\":{}}", .message = "hermes rpc: invalid JSON-RPC message: params requires method" },
    .{ .frame = "{\"jsonrpc\":\"2.0\"}", .message = "hermes rpc: invalid JSON-RPC message: unrecognized object shape" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"\"}", .message = "hermes rpc: invalid JSON-RPC message: method must be a non-empty string" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":7}", .message = "hermes rpc: invalid JSON-RPC message: method must be a non-empty string" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":7}", .message = "hermes rpc: invalid JSON-RPC message: params must be an object or null" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":1,\"message\":\"x\",\"zz\":2}}", .message = "hermes rpc: invalid JSON-RPC message: unknown error member \"zz\"" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"message\":\"x\"}}", .message = "hermes rpc: invalid JSON-RPC message: error code is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":1}}", .message = "hermes rpc: invalid JSON-RPC message: error message is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":1,\"message\":\"\"}}", .message = "hermes rpc: invalid JSON-RPC message: malformed error object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":\"x\",\"message\":\"m\"}}", .message = "hermes rpc: invalid JSON-RPC message: malformed error object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":1,\"message\":7}}", .message = "hermes rpc: invalid JSON-RPC message: malformed error object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":null}", .message = "hermes rpc: invalid JSON-RPC message: error code is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":\"x\"}}", .message = "hermes rpc: invalid JSON-RPC message: error message is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":null}}", .message = "hermes rpc: invalid JSON-RPC message: error message is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{}}", .message = "hermes rpc: invalid JSON-RPC message: error code is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"data\":1}}", .message = "hermes rpc: invalid JSON-RPC message: error code is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"message\":7}}", .message = "hermes rpc: invalid JSON-RPC message: error code is required" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":\"x\",\"message\":7}}", .message = "hermes rpc: invalid JSON-RPC message: malformed error object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":{\"code\":\"x\",\"message\":\"\"}}", .message = "hermes rpc: invalid JSON-RPC message: malformed error object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"m\"}", .message = "hermes rpc: id must be a string or integer" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":true,\"method\":\"m\"}", .message = "hermes rpc: id must be a string or integer" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":[1],\"method\":\"m\"}", .message = "hermes rpc: id must be a string or integer" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1.5,\"method\":\"m\"}", .message = "hermes rpc: id must be a string or integer: \"1.5\"" },
};

test "every refusal the Go codec spells out is spelled the same way here" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    for (refusals) |expectation| {
        var diagnostic = Diagnostic{};
        if (parseMessage(arena.allocator(), expectation.frame, &diagnostic)) |_| {
            std.debug.print("\naccepted: {s}\n", .{expectation.frame});
            return error.FrameWasAccepted;
        } else |_| {}
        try testing.expectEqualStrings(expectation.message, diagnostic.message);
    }
}

test "the framing refusals name what was wrong with the line, not the message" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const cases = [_]Refusal{
        .{ .frame = "{\"jsonrpc\":\"2.0\"\r}\n", .message = "hermes rpc: invalid JSON-RPC message: carriage return is not valid framing" },
        .{ .frame = "\n", .message = "hermes rpc: invalid JSON-RPC message: empty frame" },
        .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"}", .message = "hermes rpc: invalid JSON-RPC message: unterminated frame" },
        .{ .frame = "{\"a\":\"\xff\"}\n", .message = "hermes rpc: invalid JSON-RPC message: frame is not UTF-8" },
    };
    for (cases) |expectation| {
        var decoder = Decoder{ .source = expectation.frame };
        var diagnostic = Diagnostic{};
        if (decoder.next(arena.allocator(), &diagnostic)) |_| {
            std.debug.print("\naccepted: {s}\n", .{expectation.frame});
            return error.FrameWasAccepted;
        } else |_| {}
        try testing.expectEqualStrings(expectation.message, diagnostic.message);
    }
}

const substitutes = [_]Refusal{
    .{ .frame = "{\"jsonrpc\":\"2.0\",}", .message = "hermes rpc: invalid JSON-RPC message: frame is not decodable JSON" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":\"1\",\"error\":7}", .message = "hermes rpc: invalid JSON-RPC message: malformed error object" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":{\"x\":1e999}}", .message = "hermes rpc: invalid JSON-RPC message: frame carries a number Go cannot decode" },
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"} x}", .message = "hermes rpc: invalid JSON-RPC message: trailing JSON value" },
};

test "a refusal that quotes Go's own decoder is named here rather than reproduced" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    for (substitutes) |expectation| {
        var diagnostic = Diagnostic{};
        if (parseMessage(arena.allocator(), expectation.frame, &diagnostic)) |_| {
            std.debug.print("\naccepted: {s}\n", .{expectation.frame});
            return error.FrameWasAccepted;
        } else |_| {}
        try testing.expectEqualStrings(expectation.message, diagnostic.message);
        try testing.expect(std.mem.startsWith(u8, diagnostic.message, invalid_message_prefix));
    }
}

test "no frame is pinned twice, in either table" {
    inline for (.{ refusals, substitutes }) |table| {
        for (table, 0..) |row, i| {
            for (table[i + 1 ..]) |later| {
                if (!std.mem.eql(u8, row.frame, later.frame)) continue;
                std.debug.print("\npinned twice: {s}\n", .{row.frame});
                return error.FramePinnedTwice;
            }
        }
    }
}
