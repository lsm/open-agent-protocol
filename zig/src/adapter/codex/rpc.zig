const std = @import("std");
const gojson = @import("gojson");

pub const frame_limit_default: usize = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidMessage,
    InvalidID,
};

pub const Id = union(enum) {
    integer: i64,
    string: []const u8,

    pub fn eql(self: Id, other: Id) bool {
        return switch (self) {
            .integer => |value| other == .integer and other.integer == value,
            .string => |value| other == .string and std.mem.eql(u8, other.string, value),
        };
    }
};

pub const Kind = enum { request, notification, response, failure };

pub const Failure = struct {
    code: i64 = 0,
    message: []const u8 = "",
    data: ?std.json.Value = null,
};

pub const Message = struct {
    kind: Kind,
    id: ?Id = null,
    method: []const u8 = "",
    params: ?std.json.Value = null,
    result: ?std.json.Value = null,
    failure: ?Failure = null,
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
            if (rest.len - 1 > self.limit) return Error.FrameTooLarge;
            return Error.InvalidMessage;
        };
        if (break_at > self.limit) return Error.FrameTooLarge;
        self.at += break_at + 1;
        if (break_at == 0) return Error.InvalidMessage;
        return rest[0..break_at];
    }
};

pub fn parseInteger(literal: []const u8) ?i64 {
    return std.fmt.parseInt(i64, literal, 10) catch null;
}

fn parseID(value: std.json.Value) Error!Id {
    return switch (value) {
        .string => |text| .{ .string = text },
        .number_string => |literal| .{ .integer = parseInteger(literal) orelse return Error.InvalidID },
        .integer => |number| .{ .integer = number },
        else => Error.InvalidID,
    };
}

fn integerShaped(value: std.json.Value) bool {
    return switch (value) {
        .null, .integer => true,
        .number_string => |literal| parseInteger(literal) != null,
        else => false,
    };
}

fn parseFailure(value: std.json.Value) Error!Failure {
    if (value != .object) return Error.InvalidMessage;
    const fields = value.object;
    if (fields.get("code") == null) return Error.InvalidMessage;
    var failure = Failure{};
    var entries = fields.iterator();
    while (entries.next()) |entry| {
        const member = entry.value_ptr.*;
        if (gojson.foldEql(entry.key_ptr.*, "code")) {
            if (!integerShaped(member)) return Error.InvalidMessage;
            switch (member) {
                .integer => |number| failure.code = number,
                .number_string => |literal| failure.code = parseInteger(literal).?,
                else => {},
            }
        } else if (gojson.foldEql(entry.key_ptr.*, "message")) {
            switch (member) {
                .string => |text| failure.message = text,
                .null => {},
                else => return Error.InvalidMessage,
            }
        } else if (gojson.foldEql(entry.key_ptr.*, "data")) {
            failure.data = member;
        }
    }
    if (failure.message.len == 0) return Error.InvalidMessage;
    return failure;
}

pub fn parseMessage(arena: std.mem.Allocator, line: []const u8) !Message {
    if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidMessage;
    if (!gojson.withinNestingLimit(line)) return Error.InvalidMessage;
    const scan = gojson.replaceLoneSurrogates(arena, line);
    switch (try gojson.walkFrame(arena, scan)) {
        .ok => {},
        else => return Error.InvalidMessage,
    }
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, scan, .{ .parse_numbers = false }) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return Error.InvalidMessage,
    };
    if (document != .object) return Error.InvalidMessage;
    const object = document.object;
    if (object.count() == 0) return Error.InvalidMessage;
    if (object.get("jsonrpc") != null) return Error.InvalidMessage;

    const identity = object.get("id");
    const method = object.get("method");
    const result = object.get("result");
    const failure = object.get("error");
    if (result != null and failure != null) return Error.InvalidMessage;
    if (method != null and (result != null or failure != null)) return Error.InvalidMessage;

    var message = Message{ .kind = .notification };
    if (identity) |value| message.id = try parseID(value);
    if (method != null and identity != null) {
        message.kind = .request;
    } else if (method != null) {
        message.kind = .notification;
    } else if (identity != null and result != null) {
        message.kind = .response;
    } else if (identity != null and failure != null) {
        message.kind = .failure;
    } else {
        return Error.InvalidMessage;
    }
    if (method) |value| {
        const named: []const u8 = switch (value) {
            .string => |text| text,
            .null => "",
            else => return Error.InvalidMessage,
        };
        if (named.len == 0) return Error.InvalidMessage;
        message.method = named;
        message.params = object.get("params");
    }
    if (result) |value| message.result = value;
    if (failure) |value| message.failure = try parseFailure(value);
    return message;
}

pub const Outbound = union(enum) {
    request: struct { id: i64, method: []const u8, params: ?std.json.Value = null },
    notification: struct { method: []const u8, params: ?std.json.Value = null },
    response: struct { id: Id, result: std.json.Value },
    failure: struct { id: Id, code: i64, message: []const u8, data: ?std.json.Value = null },
};

const hex_digits = "0123456789abcdef";

pub fn appendString(out: *std.ArrayList(u8), allocator: std.mem.Allocator, text: []const u8) !void {
    try out.append(allocator, '"');
    var index: usize = 0;
    while (index < text.len) {
        const byte = text[index];
        if (byte >= 0x80) {
            const width = std.unicode.utf8ByteSequenceLength(byte) catch 0;
            const code = if (width != 0 and index + width <= text.len) std.unicode.utf8Decode(text[index .. index + width]) catch null else null;
            if (code == null) {
                try out.appendSlice(allocator, "\\ufffd");
                index += 1;
                continue;
            }
            if (code.? == 0x2028 or code.? == 0x2029) {
                try out.appendSlice(allocator, if (code.? == 0x2028) "\\u2028" else "\\u2029");
            } else {
                try out.appendSlice(allocator, text[index .. index + width]);
            }
            index += width;
            continue;
        }
        index += 1;
        switch (byte) {
            '"' => try out.appendSlice(allocator, "\\\""),
            '\\' => try out.appendSlice(allocator, "\\\\"),
            '\n' => try out.appendSlice(allocator, "\\n"),
            '\r' => try out.appendSlice(allocator, "\\r"),
            '\t' => try out.appendSlice(allocator, "\\t"),
            0x08 => try out.appendSlice(allocator, "\\b"),
            0x0c => try out.appendSlice(allocator, "\\f"),
            '<', '>', '&', 0x00...0x07, 0x0b, 0x0e...0x1f => {
                try out.appendSlice(allocator, "\\u00");
                try out.append(allocator, hex_digits[byte >> 4]);
                try out.append(allocator, hex_digits[byte & 0x0f]);
            },
            else => try out.append(allocator, byte),
        }
    }
    try out.append(allocator, '"');
}

const Level = union(enum) {
    array: struct { items: []const std.json.Value, at: usize = 0 },
    object: struct { keys: []const []const u8, values: []const std.json.Value, at: usize = 0 },
};

fn openValue(out: *std.ArrayList(u8), allocator: std.mem.Allocator, value: std.json.Value, levels: *std.ArrayList(Level)) !void {
    switch (value) {
        .null => try out.appendSlice(allocator, "null"),
        .bool => |flag| try out.appendSlice(allocator, if (flag) "true" else "false"),
        .integer => |number| try out.print(allocator, "{d}", .{number}),
        .float => |number| {
            const literal = try std.json.Stringify.valueAlloc(allocator, number, .{});
            defer allocator.free(literal);
            try out.appendSlice(allocator, literal);
        },
        .number_string => |literal| try out.appendSlice(allocator, literal),
        .string => |text| try appendString(out, allocator, text),
        .array => |list| {
            try out.append(allocator, '[');
            try levels.append(allocator, .{ .array = .{ .items = list.items } });
        },
        .object => |map| {
            try out.append(allocator, '{');
            try levels.append(allocator, .{ .object = .{ .keys = map.keys(), .values = map.values() } });
        },
    }
}

fn drain(out: *std.ArrayList(u8), allocator: std.mem.Allocator, levels: *std.ArrayList(Level)) !void {
    while (levels.items.len != 0) {
        switch (levels.items[levels.items.len - 1]) {
            .array => |*level| {
                if (level.at == level.items.len) {
                    try out.append(allocator, ']');
                    _ = levels.pop();
                    continue;
                }
                if (level.at != 0) try out.append(allocator, ',');
                const value = level.items[level.at];
                level.at += 1;
                try openValue(out, allocator, value, levels);
            },
            .object => |*level| {
                if (level.at == level.keys.len) {
                    try out.append(allocator, '}');
                    _ = levels.pop();
                    continue;
                }
                if (level.at != 0) try out.append(allocator, ',');
                try appendString(out, allocator, level.keys[level.at]);
                try out.append(allocator, ':');
                const value = level.values[level.at];
                level.at += 1;
                try openValue(out, allocator, value, levels);
            },
        }
    }
}

pub fn appendValue(out: *std.ArrayList(u8), allocator: std.mem.Allocator, value: std.json.Value) !void {
    var levels = std.ArrayList(Level).empty;
    defer levels.deinit(allocator);
    try openValue(out, allocator, value, &levels);
    try drain(out, allocator, &levels);
}

pub fn appendValues(out: *std.ArrayList(u8), allocator: std.mem.Allocator, values: []const std.json.Value) !void {
    var levels = std.ArrayList(Level).empty;
    defer levels.deinit(allocator);
    try out.append(allocator, '[');
    try levels.append(allocator, .{ .array = .{ .items = values } });
    try drain(out, allocator, &levels);
}

pub fn encodeValue(allocator: std.mem.Allocator, value: std.json.Value) ![]u8 {
    var out = std.ArrayList(u8).empty;
    errdefer out.deinit(allocator);
    try appendValue(&out, allocator, value);
    return out.toOwnedSlice(allocator);
}

pub fn encodeValues(allocator: std.mem.Allocator, values: []const std.json.Value) ![]u8 {
    var out = std.ArrayList(u8).empty;
    errdefer out.deinit(allocator);
    try appendValues(&out, allocator, values);
    return out.toOwnedSlice(allocator);
}

fn appendID(out: *std.ArrayList(u8), allocator: std.mem.Allocator, id: Id) !void {
    switch (id) {
        .integer => |number| try out.print(allocator, "{d}", .{number}),
        .string => |text| try appendString(out, allocator, text),
    }
}

pub fn encode(allocator: std.mem.Allocator, message: Outbound) ![]u8 {
    var out = std.ArrayList(u8).empty;
    errdefer out.deinit(allocator);
    switch (message) {
        .request => |request| {
            if (request.method.len == 0) return Error.InvalidMessage;
            try out.print(allocator, "{{\"id\":{d},\"method\":", .{request.id});
            try appendString(&out, allocator, request.method);
            if (request.params) |params| {
                try out.appendSlice(allocator, ",\"params\":");
                try appendValue(&out, allocator, params);
            }
        },
        .notification => |notification| {
            if (notification.method.len == 0) return Error.InvalidMessage;
            try out.appendSlice(allocator, "{\"method\":");
            try appendString(&out, allocator, notification.method);
            if (notification.params) |params| {
                try out.appendSlice(allocator, ",\"params\":");
                try appendValue(&out, allocator, params);
            }
        },
        .response => |response| {
            try out.appendSlice(allocator, "{\"id\":");
            try appendID(&out, allocator, response.id);
            try out.appendSlice(allocator, ",\"result\":");
            try appendValue(&out, allocator, response.result);
        },
        .failure => |failure| {
            if (failure.message.len == 0) return Error.InvalidMessage;
            try out.print(allocator, "{{\"error\":{{\"code\":{d},\"message\":", .{failure.code});
            try appendString(&out, allocator, failure.message);
            if (failure.data) |data| {
                try out.appendSlice(allocator, ",\"data\":");
                try appendValue(&out, allocator, data);
            }
            try out.appendSlice(allocator, "},\"id\":");
            try appendID(&out, allocator, failure.id);
        },
    }
    try out.append(allocator, '}');
    return out.toOwnedSlice(allocator);
}

const testing = std.testing;

fn scratch(holder: *?std.heap.ArenaAllocator) std.mem.Allocator {
    if (holder.* == null) holder.* = std.heap.ArenaAllocator.init(testing.allocator);
    return holder.*.?.allocator();
}

test "a frame is one LF-terminated line, a carriage return is JSON whitespace, and the terminator is required" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    var unterminated = Decoder{ .source = "{\"method\":\"x\"}" };
    try testing.expectError(Error.InvalidMessage, unterminated.next(scratch(&holder)));

    var decoder = Decoder{ .source = "{\"method\":\"x\"}\n {\"method\":\"y\"}\r\n" };
    try testing.expectEqualStrings("x", (try decoder.next(scratch(&holder))).?.method);
    try testing.expectEqualStrings("y", (try decoder.next(scratch(&holder))).?.method);
    try testing.expect(try decoder.next(scratch(&holder)) == null);

    var empty = Decoder{ .source = "\n" };
    try testing.expectError(Error.InvalidMessage, empty.next(scratch(&holder)));
}

const Bound = struct { content: usize, terminated: bool, verdict: enum { accepted, unterminated, too_large } };

const frame_bounds = [_]Bound{
    .{ .content = 15, .terminated = true, .verdict = .accepted },
    .{ .content = 16, .terminated = true, .verdict = .accepted },
    .{ .content = 16, .terminated = false, .verdict = .unterminated },
    .{ .content = 17, .terminated = true, .verdict = .too_large },
    .{ .content = 17, .terminated = false, .verdict = .unterminated },
    .{ .content = 18, .terminated = false, .verdict = .too_large },
};

test "the limit counts the terminator, so an unterminated frame gets one byte more than a terminated one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const allocator = arena.allocator();
    for (frame_bounds) |bound| {
        const name = try allocator.alloc(u8, bound.content - 13);
        @memset(name, 'x');
        const frame = try std.fmt.allocPrint(allocator, "{{\"method\":\"{s}\"}}{s}", .{ name, if (bound.terminated) "\n" else "" });
        try testing.expectEqual(bound.content + @as(usize, if (bound.terminated) 1 else 0), frame.len);
        var decoder = Decoder{ .source = frame, .limit = 16 };
        const outcome = decoder.next(allocator);
        switch (bound.verdict) {
            .accepted => try testing.expectEqual(Kind.notification, (try outcome).?.kind),
            .unterminated => try testing.expectError(Error.InvalidMessage, outcome),
            .too_large => try testing.expectError(Error.FrameTooLarge, outcome),
        }
    }
}

test "the Codex dialect carries no jsonrpc member and refuses one that does" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    try testing.expectError(Error.InvalidMessage, parseMessage(scratch(&holder), "{\"jsonrpc\":\"2.0\",\"method\":\"x\"}"));
    try testing.expectEqual(Kind.notification, (try parseMessage(scratch(&holder), "{\"method\":\"x\"}")).kind);
}

test "the four shapes are told apart by which members are present" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    const request = try parseMessage(a, "{\"id\":7,\"method\":\"item/tool/requestUserInput\",\"params\":{}}");
    try testing.expectEqual(Kind.request, request.kind);
    try testing.expect(request.id.?.eql(.{ .integer = 7 }));
    try testing.expectEqual(Kind.notification, (try parseMessage(a, "{\"method\":\"turn/started\"}")).kind);
    try testing.expectEqual(Kind.response, (try parseMessage(a, "{\"id\":\"s\",\"result\":null}")).kind);
    const failed = try parseMessage(a, "{\"id\":1,\"error\":{\"code\":-32600,\"message\":\"no\",\"data\":[1]}}");
    try testing.expectEqual(Kind.failure, failed.kind);
    try testing.expectEqual(@as(i64, -32600), failed.failure.?.code);
    try testing.expectEqualStrings("no", failed.failure.?.message);
    try testing.expect(failed.failure.?.data.? == .array);
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"result\":{}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"params\":{}}"));
}

test "an empty object, a non-object, a trailing value and a duplicate key are refused" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "null"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "[{\"method\":\"x\"}]"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":\"x\"} {\"method\":\"y\"}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":\"x\",\"method\":\"y\"}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":\"x\",\"params\":{\"a\":1,\"a\":2}}"));
}

test "result and error are mutually exclusive, and neither accompanies a method" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"result\":{},\"error\":{\"code\":1,\"message\":\"no\"}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"method\":\"x\",\"result\":{}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":\"x\",\"error\":{\"code\":1,\"message\":\"no\"}}"));
}

test "a method is a non-empty string, and null counts as empty" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":\"\"}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":null}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":7}"));
}

test "an id is a string or a base-ten integer that fits sixty-four bits" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    try testing.expect((try parseMessage(a, "{\"id\":-5,\"result\":{}}")).id.?.eql(.{ .integer = -5 }));
    try testing.expect((try parseMessage(a, "{\"id\":\"a\",\"result\":{}}")).id.?.eql(.{ .string = "a" }));
    try testing.expectError(Error.InvalidID, parseMessage(a, "{\"id\":1.0,\"result\":{}}"));
    try testing.expectError(Error.InvalidID, parseMessage(a, "{\"id\":1e2,\"result\":{}}"));
    try testing.expectError(Error.InvalidID, parseMessage(a, "{\"id\":9223372036854775808,\"result\":{}}"));
    try testing.expectError(Error.InvalidID, parseMessage(a, "{\"id\":null,\"result\":{}}"));
    try testing.expectError(Error.InvalidID, parseMessage(a, "{\"id\":true,\"method\":\"x\"}"));
}

test "an error object needs a literal code member and a non-empty message, matched as Go matches struct fields" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":{\"message\":\"no\"}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":{\"Code\":1,\"message\":\"no\"}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":{\"code\":1}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":{\"code\":1,\"message\":\"\"}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":{\"code\":1.5,\"message\":\"no\"}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":{\"code\":\"1\",\"message\":\"no\"}}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":null}"));
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"id\":1,\"error\":\"no\"}"));
    const folded = try parseMessage(a, "{\"id\":1,\"error\":{\"code\":null,\"MESSAGE\":\"folded\",\"extra\":1}}");
    try testing.expectEqual(@as(i64, 0), folded.failure.?.code);
    try testing.expectEqualStrings("folded", folded.failure.?.message);
}

test "invalid UTF-8 is refused and a lone surrogate escape decodes as the replacement character" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    try testing.expectError(Error.InvalidMessage, parseMessage(a, "{\"method\":\"\xff\"}"));
    const replaced = try parseMessage(a, "{\"method\":\"x\\ud800\"}");
    try testing.expectEqualStrings("x\u{fffd}", replaced.method);
}

test "numbers keep the literal they arrived with" {
    var holder: ?std.heap.ArenaAllocator = null;
    defer if (holder) |*a| a.deinit();
    const a = scratch(&holder);
    const message = try parseMessage(a, "{\"method\":\"x\",\"params\":{\"n\":1.50,\"m\":10}}");
    try testing.expectEqualStrings("1.50", message.params.?.object.get("n").?.number_string);
    const encoded = try encodeValue(a, message.params.?);
    try testing.expectEqualStrings("{\"n\":1.50,\"m\":10}", encoded);
}

test "a frame nested past ten thousand containers is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var body = std.ArrayList(u8).empty;
    try body.appendSlice(a, "{\"method\":\"x\",\"params\":");
    try body.appendNTimes(a, '[', 10000);
    try body.appendNTimes(a, ']', 10000);
    try body.append(a, '}');
    try testing.expectError(Error.InvalidMessage, parseMessage(a, body.items));
}

test "a frame is written with its members in Go's sorted map order" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var params = std.json.ObjectMap.empty;
    try params.put(a, "threadId", .{ .string = "t" });
    try testing.expectEqualStrings(
        "{\"id\":3,\"method\":\"turn/interrupt\",\"params\":{\"threadId\":\"t\"}}",
        try encode(a, .{ .request = .{ .id = 3, .method = "turn/interrupt", .params = .{ .object = params } } }),
    );
    try testing.expectEqualStrings("{\"method\":\"initialized\"}", try encode(a, .{ .notification = .{ .method = "initialized" } }));
    try testing.expectEqualStrings("{\"id\":\"r\",\"result\":{\"threadId\":\"t\"}}", try encode(a, .{ .response = .{ .id = .{ .string = "r" }, .result = .{ .object = params } } }));
    try testing.expectEqualStrings(
        "{\"error\":{\"code\":-32601,\"message\":\"no\",\"data\":{\"threadId\":\"t\"}},\"id\":9}",
        try encode(a, .{ .failure = .{ .id = .{ .integer = 9 }, .code = -32601, .message = "no", .data = .{ .object = params } } }),
    );
    try testing.expectEqualStrings("{\"error\":{\"code\":-32602,\"message\":\"no\"},\"id\":9}", try encode(a, .{ .failure = .{ .id = .{ .integer = 9 }, .code = -32602, .message = "no" } }));
    try testing.expectError(Error.InvalidMessage, encode(a, .{ .failure = .{ .id = .{ .integer = 9 }, .code = 1, .message = "" } }));
    try testing.expectError(Error.InvalidMessage, encode(a, .{ .request = .{ .id = 1, .method = "" } }));
}

test "strings are escaped the way Go escapes them for HTML safety" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try testing.expectEqualStrings(
        "\"a\\u003cb\\u003e\\u0026c\\u2028d\\u2029e\\n\\u0001\\\"\"",
        try encodeValue(a, .{ .string = "a<b>&c\u{2028}d\u{2029}e\n\x01\"" }),
    );
    try testing.expectEqualStrings("\"\\ufffdx\\u001f\x7f\"", try encodeValue(a, .{ .string = "\xffx\x1f\x7f" }));
}

test "a value nested to the frame limit encodes without overflowing a fixed nesting stack" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var body = std.ArrayList(u8).empty;
    try body.appendSlice(a, "{\"method\":\"x\",\"params\":");
    try body.appendNTimes(a, '[', 9999);
    try body.appendNTimes(a, ']', 9999);
    try body.append(a, '}');
    const message = try parseMessage(a, body.items);
    const frame = try encode(a, .{ .notification = .{ .method = message.method, .params = message.params } });
    try testing.expectEqualStrings(body.items, frame);
}

fn encodeProbe(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(std.heap.page_allocator);
    defer arena.deinit();
    var params = std.json.ObjectMap.empty;
    try params.put(arena.allocator(), "text", .{ .string = "<hello>" });
    const request = try encode(allocator, .{ .request = .{ .id = 1, .method = "turn/start", .params = .{ .object = params } } });
    defer allocator.free(request);
    const failure = try encode(allocator, .{ .failure = .{ .id = .{ .string = "r" }, .code = -1, .message = "m", .data = .{ .object = params } } });
    defer allocator.free(failure);
    const value = try encodeValue(allocator, std.json.Value{ .object = params });
    defer allocator.free(value);
}

test "encoding hands back an owned frame on every allocation failure path" {
    try testing.checkAllAllocationFailures(testing.allocator, encodeProbe, .{});
}
