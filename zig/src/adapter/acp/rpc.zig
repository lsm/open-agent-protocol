const std = @import("std");

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
            if (rest.len > self.limit) return Error.FrameTooLarge;
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
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch return Error.InvalidMessage;
    if (document != .object) return Error.InvalidMessage;
    const object = document.object;

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
            if (carried != .object and carried != .array) return Error.InvalidMessage;
        }
    }
    if (failure) |value| {
        if (value != .object) return Error.InvalidMessage;
        const code = value.object.get("code") orelse return Error.InvalidMessage;
        if (code != .integer and code != .null) return Error.InvalidMessage;
        const detail = value.object.get("message") orelse return Error.InvalidMessage;
        if (detail != .string and detail != .null) return Error.InvalidMessage;
    }
    return .{ .kind = kind, .method = named, .raw = line };
}

fn scratch(holder: *?std.heap.ArenaAllocator) std.mem.Allocator {
    if (holder.* == null) holder.* = std.heap.ArenaAllocator.init(std.testing.allocator);
    return holder.*.?.allocator();
}

test "a frame is one LF-terminated line and the terminator is required" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var unterminated = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}" };
    try std.testing.expectError(Error.InvalidMessage, unterminated.next(scratch(&holder)));

    var decoder = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n" };
    const message = try decoder.next(scratch(&holder));
    try std.testing.expect(message != null);
    try std.testing.expectEqual(Kind.notification, message.?.kind);
    try std.testing.expect(try decoder.next(scratch(&holder)) == null);
}

test "carriage returns, empty lines and invalid UTF-8 are framing defects" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var carriage = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\r\n" };
    try std.testing.expectError(Error.InvalidMessage, carriage.next(scratch(&holder)));
    var empty = Decoder{ .source = "\n" };
    try std.testing.expectError(Error.InvalidMessage, empty.next(scratch(&holder)));
    var mangled = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"\xff\"}\n" };
    try std.testing.expectError(Error.InvalidMessage, mangled.next(scratch(&holder)));
}

test "a frame over the configured limit is refused rather than truncated" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var decoder = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}\n", .limit = 4 };
    try std.testing.expectError(Error.FrameTooLarge, decoder.next(scratch(&holder)));
}

test "the version member must be present and must equal 2.0" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"method\":\"x\"}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"1.0\",\"method\":\"x\"}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":2.0,\"method\":\"x\"}"));
}

test "the four shapes are told apart by which members are present" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const request = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/new\"}");
    try std.testing.expectEqual(Kind.request, request.kind);
    try std.testing.expectEqualStrings("session/new", request.method);

    const notification = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\"}");
    try std.testing.expectEqual(Kind.notification, notification.kind);

    const response = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}");
    try std.testing.expectEqual(Kind.response, response.kind);

    const failed = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1,\"message\":\"no\"}}");
    try std.testing.expectEqual(Kind.failure, failed.kind);
}

test "an object carrying no recognizable shape is refused" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\"}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"result\":{}}"));
}

test "result and error are mutually exclusive, and neither accompanies a method" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"error\":{\"code\":-1,\"message\":\"no\"}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x\",\"result\":{}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"x\",\"error\":{\"code\":-1,\"message\":\"no\"}}"));
}

test "params belong to a method and must be an object or an array" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"params\":{}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":\"text\"}"));
    const object = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{}}");
    try std.testing.expectEqual(Kind.notification, object.kind);
    const array = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":[]}");
    try std.testing.expectEqual(Kind.notification, array.kind);
}

test "a method must be a non-empty string" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"\"}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":7}"));
}

test "an id is a string or an integer and nothing else" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const text = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":\"a\",\"result\":{}}");
    try std.testing.expectEqual(Kind.response, text.kind);
    try std.testing.expectError(Error.InvalidID, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1.5,\"result\":{}}"));
    try std.testing.expectError(Error.InvalidID, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":null,\"result\":{}}"));
    try std.testing.expectError(Error.InvalidID, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":{},\"result\":{}}"));
}

test "an error object carries both a code and a message" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":-1}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"message\":\"no\"}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":\"no\"}"));
}

test "an error object's code is an integer and its message a string" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":\"x\",\"message\":\"bad\"}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1.5,\"message\":\"bad\"}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":99999999999999999999,\"message\":\"bad\"}}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":7}}"));
}

test "an error object member holding null decodes as a no-op, not as a type defect" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const nulled = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":null,\"message\":null}}");
    try std.testing.expectEqual(Kind.failure, nulled.kind);
}

test "an error object tolerates an empty message and an unknown member" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const empty = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"\"}}");
    try std.testing.expectEqual(Kind.failure, empty.kind);
    const foreign = try parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"bad\",\"other\":1}}");
    try std.testing.expectEqual(Kind.failure, foreign.kind);
}

test "a duplicate key is refused at every nesting level" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"method\":\"y\"}"));
    try std.testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":{\"a\":1,\"a\":2}}"));
}
