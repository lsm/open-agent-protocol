const std = @import("std");
const gojson = @import("gojson");

pub const frame_limit_default: usize = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidMessage,
    InvalidID,
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

    pub fn next(self: *Decoder, arena: std.mem.Allocator) !?Message {
        const line = try self.readFrame() orelse return null;
        return try parseMessage(arena, line);
    }

    fn readFrame(self: *Decoder) !?[]const u8 {
        if (self.at >= self.source.len) return null;
        const rest = self.source[self.at..];
        const break_at = std.mem.indexOfScalar(u8, rest, '\n') orelse {
            if (rest.len > self.limit + 1) return Error.FrameTooLarge;
            return Error.InvalidMessage;
        };
        if (break_at > self.limit) return Error.FrameTooLarge;
        const line = rest[0..break_at];
        self.at += break_at + 1;
        if (line.len == 0) return Error.InvalidMessage;
        if (std.mem.indexOfScalar(u8, line, '\r') != null) return Error.InvalidMessage;
        if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidMessage;
        return line;
    }
};

pub fn parseMessage(arena: std.mem.Allocator, line: []const u8) !Message {
    if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidMessage;
    if (line.len == 0 or line[0] != '{' or line[line.len - 1] != '}') return Error.InvalidMessage;
    if (!gojson.withinNestingLimit(line)) return Error.InvalidMessage;

    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, gojson.replaceLoneSurrogates(arena, line), .{}) catch return Error.InvalidMessage;
    if (document != .object) return Error.InvalidMessage;
    if (!gojson.finiteNumbers(document)) return Error.InvalidMessage;
    const object = document.object;

    var members = object.iterator();
    while (members.next()) |entry| {
        if (!allowed(&allowed_members, entry.key_ptr.*)) return Error.InvalidMessage;
    }

    const version = object.get("jsonrpc") orelse return Error.InvalidMessage;
    if (version != .string or !std.mem.eql(u8, version.string, "2.0")) return Error.InvalidMessage;

    const identity = object.get("id");
    const method = object.get("method");
    const params = object.get("params");
    const result = object.get("result");
    const failure = object.get("error");

    if (result != null and failure != null) return Error.InvalidMessage;
    if (method != null and (result != null or failure != null)) return Error.InvalidMessage;
    if (method == null and params != null) return Error.InvalidMessage;

    if (identity) |value| {
        if (value != .string and value != .integer) return Error.InvalidID;
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
        return Error.InvalidMessage;
    }

    var named: []const u8 = "";
    if (method) |value| {
        if (value != .string or value.string.len == 0) return Error.InvalidMessage;
        named = value.string;
        if (params) |carried| {
            if (carried != .object and carried != .null) return Error.InvalidMessage;
        }
    }
    if (failure) |value| {
        if (value != .object) return Error.InvalidMessage;
        var fields = value.object.iterator();
        while (fields.next()) |entry| {
            if (!allowed(&allowed_error_members, entry.key_ptr.*)) return Error.InvalidMessage;
        }
        const code = value.object.get("code") orelse return Error.InvalidMessage;
        if (code != .integer and code != .null) return Error.InvalidMessage;
        const detail = value.object.get("message") orelse return Error.InvalidMessage;
        if (detail != .string or detail.string.len == 0) return Error.InvalidMessage;
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
    .{ .frame = "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"a\":\"\\ud800\"}}", .verdict = "response", .method = "" },
};

test "every frame decodes exactly as the pinned Hermes codec decodes it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for (oracle) |expectation| {
        const refuses = std.mem.eql(u8, expectation.verdict, "refused");
        if (parseMessage(scratch, expectation.frame)) |message| {
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
    _ = try decoder.next(scratch);
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
            const message = try parseMessage(scratch, frame);
            try testing.expectEqualStrings("x", message.method);
        } else {
            try testing.expectError(Error.InvalidMessage, parseMessage(scratch, frame));
        }
    }
}
