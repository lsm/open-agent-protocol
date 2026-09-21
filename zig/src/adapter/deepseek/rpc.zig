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

const top_level_members = [_][]const u8{ "jsonrpc", "id", "method", "params", "result", "error" };
const failure_members = [_][]const u8{ "code", "message", "data" };

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

fn listed(names: []const []const u8, name: []const u8) bool {
    for (names) |candidate| {
        if (std.mem.eql(u8, candidate, name)) return true;
    }
    return false;
}

fn identifiesRequest(value: std.json.Value) bool {
    return switch (value) {
        .string, .integer => true,
        else => false,
    };
}

pub fn parseMessage(arena: std.mem.Allocator, line: []const u8) !Message {
    if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidMessage;
    if (line.len == 0 or line[0] != '{' or line[line.len - 1] != '}') return Error.InvalidMessage;

    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch return Error.InvalidMessage;
    if (document != .object) return Error.InvalidMessage;
    const object = document.object;

    for (object.keys()) |name| {
        if (!listed(&top_level_members, name)) return Error.InvalidMessage;
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
        if (!identifiesRequest(value)) return Error.InvalidID;
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
        for (value.object.keys()) |name| {
            if (!listed(&failure_members, name)) return Error.InvalidMessage;
        }
        const code = value.object.get("code") orelse return Error.InvalidMessage;
        if (code != .integer and code != .null) return Error.InvalidMessage;
        const detail = value.object.get("message") orelse return Error.InvalidMessage;
        if (detail != .string or detail.string.len == 0) return Error.InvalidMessage;
    }
    return .{ .kind = kind, .method = named, .raw = line };
}

fn scratch(holder: *?std.heap.ArenaAllocator) std.mem.Allocator {
    if (holder.* == null) holder.* = std.heap.ArenaAllocator.init(std.testing.allocator);
    return holder.*.?.allocator();
}

fn refuses(line: []const u8) !void {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const parsed = parseMessage(scratch(&holder), line);
    try std.testing.expect(std.meta.isError(parsed));
}

fn admits(line: []const u8, kind: Kind, method: []const u8) !void {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const message = try parseMessage(scratch(&holder), line);
    try std.testing.expectEqual(kind, message.kind);
    try std.testing.expectEqualStrings(method, message.method);
}

test "a frame is one LF-terminated line and the terminator is required" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var unterminated = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"}" };
    try std.testing.expectError(Error.InvalidMessage, unterminated.next(scratch(&holder)));

    var decoder = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"}\n" };
    const message = try decoder.next(scratch(&holder));
    try std.testing.expect(message != null);
    try std.testing.expectEqual(Kind.notification, message.?.kind);
    try std.testing.expect(try decoder.next(scratch(&holder)) == null);
}

test "carriage returns, empty lines and invalid UTF-8 are framing defects" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var carriage = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"}\r\n" };
    try std.testing.expectError(Error.InvalidMessage, carriage.next(scratch(&holder)));
    var empty = Decoder{ .source = "\n" };
    try std.testing.expectError(Error.InvalidMessage, empty.next(scratch(&holder)));
    var mangled = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"\xff\"}\n" };
    try std.testing.expectError(Error.InvalidMessage, mangled.next(scratch(&holder)));
}

test "a frame over the limit fails closed rather than being truncated" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var bounded = Decoder{ .source = "{\"jsonrpc\":\"2.0\",\"method\":\"m\"}\n", .limit = 8 };
    try std.testing.expectError(Error.FrameTooLarge, bounded.next(scratch(&holder)));
}

test "the four JSON-RPC shapes are told apart by which members are present" {
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"m\",\"params\":{}}", .request, "m");
    try admits("{\"jsonrpc\":\"2.0\",\"method\":\"m\"}", .notification, "m");
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{}}", .response, "");
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"x\"}}", .failure, "");
}

test "an object matching none of the four shapes is refused rather than guessed at" {
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1}");
    try refuses("{\"jsonrpc\":\"2.0\",\"result\":{}}");
    try refuses("{\"jsonrpc\":\"2.0\"}");
    try refuses("{}");
}

test "result and error are exclusive, neither joins a method, and params needs one" {
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"error\":{\"code\":1,\"message\":\"x\"}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"m\",\"result\":{}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"params\":{}}");
}

test "jsonrpc must be the string 2.0" {
    try refuses("{\"id\":1,\"result\":{}}");
    try refuses("{\"jsonrpc\":\"1.0\",\"id\":1,\"result\":{}}");
    try refuses("{\"jsonrpc\":2.0,\"id\":1,\"result\":{}}");
    try refuses("{\"jsonrpc\":null,\"id\":1,\"result\":{}}");
}

test "the top-level member set is closed" {
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"extra\":1,\"result\":{}}");
}

test "a frame is exactly one JSON object with no surrounding text" {
    try refuses(" {\"jsonrpc\":\"2.0\",\"method\":\"m\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"m\"} ");
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"m\"}{\"jsonrpc\":\"2.0\",\"method\":\"n\"}");
}

test "duplicate keys are refused at every nesting level" {
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"method\":\"n\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"a\":1,\"a\":2}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":[{\"a\":1,\"a\":2}]}");
}

test "an id is a string or an integer and nothing else" {
    try admits("{\"jsonrpc\":\"2.0\",\"id\":\"s\",\"method\":\"m\"}", .request, "m");
    try admits("{\"jsonrpc\":\"2.0\",\"id\":-1,\"method\":\"m\"}", .request, "m");
    try admits("{\"jsonrpc\":\"2.0\",\"id\":0,\"method\":\"m\"}", .request, "m");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1.5,\"method\":\"m\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1e3,\"method\":\"m\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":99999999999999999999,\"method\":\"m\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":null,\"method\":\"m\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":true,\"method\":\"m\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":[1],\"method\":\"m\"}");
}

test "a method is a non-empty string and its params are an object or an array" {
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"\",\"id\":1}");
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":1,\"id\":1}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":null}");
    try admits("{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":[]}", .notification, "m");
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":\"x\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":1}");
    try refuses("{\"jsonrpc\":\"2.0\",\"method\":\"m\",\"params\":null}");
}

test "a present member holding null is present, not absent" {
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":null}", .response, "");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":null}");
}

test "a null error code decodes to zero and is accepted; a null message is not" {
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":null,\"message\":\"x\"}}", .failure, "");
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":null,\"message\":\"x\",\"data\":null}}", .failure, "");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":null}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":null,\"message\":null}}");
}

test "an error object carries an integer code and a non-empty message, and nothing else" {
    try admits("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"x\",\"data\":{\"k\":1}}}", .failure, "");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"message\":\"x\"}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"\"}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":1}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":\"e\",\"message\":\"x\"}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1.5,\"message\":\"x\"}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"x\",\"other\":1}}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":\"boom\"}");
    try refuses("{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":[]}");
}
