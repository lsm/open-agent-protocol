const std = @import("std");

pub const default_frame_limit = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidMessage,
    InvalidControl,
};

pub const Kind = enum {
    observation,
    control_request,
    control_response,
    control_cancel,
};

pub const type_control_request = "control_request";
pub const type_control_response = "control_response";
pub const type_control_cancel = "control_cancel_request";

pub const ControlResponse = struct {
    request_id: []const u8 = "",
    success: bool = false,
    response: ?std.json.Value = null,
    err: []const u8 = "",
};

pub const Message = struct {
    kind: Kind,
    type: []const u8,
    subtype: []const u8 = "",
    request_id: []const u8 = "",
    response: ?ControlResponse = null,
    raw: []const u8,
    object: std.json.Value,
};

pub const FrameReader = struct {
    source: []const u8,
    cursor: usize = 0,
    limit: usize = default_frame_limit,

    pub fn next(self: *FrameReader) !?[]const u8 {
        if (self.cursor >= self.source.len) return null;
        const rest = self.source[self.cursor..];
        const newline = std.mem.indexOfScalar(u8, rest, '\n') orelse {
            self.cursor = self.source.len;
            return Error.InvalidMessage;
        };
        if (newline > self.limit) return Error.FrameTooLarge;
        const frame = rest[0..newline];
        self.cursor += newline + 1;
        if (frame.len == 0) return Error.InvalidMessage;
        if (std.mem.indexOfScalar(u8, frame, '\r') != null) return Error.InvalidMessage;
        if (!std.unicode.utf8ValidateSlice(frame)) return Error.InvalidMessage;
        return frame;
    }
};

pub fn parseMessage(arena: std.mem.Allocator, data: []const u8) !Message {
    if (data.len == 0 or data[0] != '{' or data[data.len - 1] != '}') return Error.InvalidMessage;
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, data, .{}) catch return Error.InvalidMessage;
    if (parsed != .object) return Error.InvalidMessage;
    const object = parsed.object;

    const type_value = object.get("type") orelse return Error.InvalidMessage;
    if (type_value != .string or type_value.string.len == 0) return Error.InvalidMessage;

    var message = Message{
        .kind = .observation,
        .type = type_value.string,
        .raw = data,
        .object = parsed,
    };
    if (object.get("subtype")) |subtype| {
        if (subtype != .string or subtype.string.len == 0) return Error.InvalidMessage;
        message.subtype = subtype.string;
    }

    if (std.mem.eql(u8, message.type, type_control_request)) {
        message.kind = .control_request;
        const id = object.get("request_id") orelse return Error.InvalidControl;
        if (id != .string or id.string.len == 0) return Error.InvalidControl;
        message.request_id = id.string;
        const request = object.get("request") orelse return Error.InvalidControl;
        if (request != .object) return Error.InvalidControl;
        const subtype = request.object.get("subtype") orelse return Error.InvalidControl;
        if (subtype != .string or subtype.string.len == 0) return Error.InvalidControl;
        message.subtype = subtype.string;
        return message;
    }
    if (std.mem.eql(u8, message.type, type_control_response)) {
        message.kind = .control_response;
        const response = object.get("response") orelse return Error.InvalidControl;
        if (response != .object) return Error.InvalidControl;
        const state = response.object.get("subtype") orelse return Error.InvalidControl;
        if (state != .string or state.string.len == 0) return Error.InvalidControl;
        const id = response.object.get("request_id") orelse return Error.InvalidControl;
        if (id != .string or id.string.len == 0) return Error.InvalidControl;
        var envelope = ControlResponse{ .request_id = id.string };
        if (std.mem.eql(u8, state.string, "success")) {
            envelope.success = true;
            if (response.object.get("response")) |payload| {
                if (payload != .null) envelope.response = payload;
            }
        } else if (std.mem.eql(u8, state.string, "error")) {
            const detail = response.object.get("error") orelse return Error.InvalidControl;
            if (detail != .string or detail.string.len == 0) return Error.InvalidControl;
            envelope.err = detail.string;
        } else return Error.InvalidControl;
        message.response = envelope;
        return message;
    }
    if (std.mem.eql(u8, message.type, type_control_cancel)) {
        message.kind = .control_cancel;
        const id = object.get("request_id") orelse return Error.InvalidControl;
        if (id != .string or id.string.len == 0) return Error.InvalidControl;
        message.request_id = id.string;
        return message;
    }
    return message;
}

const testing = std.testing;

fn parseForTest(text: []const u8, arena: *std.heap.ArenaAllocator) !Message {
    return parseMessage(arena.allocator(), text);
}

test "a frame must be one newline-terminated UTF-8 object" {
    var reader = FrameReader{ .source = "{\"type\":\"result\"}\n" };
    try testing.expectEqualStrings("{\"type\":\"result\"}", (try reader.next()).?);
    try testing.expectEqual(@as(?[]const u8, null), try reader.next());

    var empty = FrameReader{ .source = "\n" };
    try testing.expectError(Error.InvalidMessage, empty.next());

    var carriage = FrameReader{ .source = "{\"type\":\"a\"}\r\n" };
    try testing.expectError(Error.InvalidMessage, carriage.next());

    var unterminated = FrameReader{ .source = "{\"type\":\"a\"}" };
    try testing.expectError(Error.InvalidMessage, unterminated.next());

    var invalid_utf8 = FrameReader{ .source = "{\"a\":\"\xff\"}\n" };
    try testing.expectError(Error.InvalidMessage, invalid_utf8.next());

    var eof = FrameReader{ .source = "" };
    try testing.expectEqual(@as(?[]const u8, null), try eof.next());
}

test "a frame past the limit is refused rather than buffered" {
    var reader = FrameReader{ .source = "{\"type\":\"result\"}\n", .limit = 4 };
    try testing.expectError(Error.FrameTooLarge, reader.next());
}

test "a duplicate key anywhere in the frame is fatal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"type\":\"a\",\"type\":\"b\"}", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"type\":\"a\",\"m\":{\"x\":1,\"x\":2}}", &arena));
    _ = try parseForTest("{\"type\":\"a\",\"m\":{\"x\":1},\"n\":{\"x\":2}}", &arena);
}

test "a known discriminator with a violated shape is fatal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"subtype\":\"init\"}", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"type\":\"\"}", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"type\":\"system\",\"subtype\":\"\"}", &arena));
    try testing.expectError(Error.InvalidControl, parseForTest("{\"type\":\"control_request\",\"request\":{\"subtype\":\"initialize\"}}", &arena));
    try testing.expectError(Error.InvalidControl, parseForTest("{\"type\":\"control_request\",\"request_id\":\"r\",\"request\":{}}", &arena));
    try testing.expectError(Error.InvalidControl, parseForTest("{\"type\":\"control_response\",\"response\":{\"subtype\":\"other\",\"request_id\":\"r\"}}", &arena));
    try testing.expectError(Error.InvalidControl, parseForTest("{\"type\":\"control_response\",\"response\":{\"subtype\":\"error\",\"request_id\":\"r\"}}", &arena));
    try testing.expectError(Error.InvalidControl, parseForTest("{\"type\":\"control_cancel_request\"}", &arena));
}

test "an unknown top-level type is an observation, not a defect" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const message = try parseForTest("{\"type\":\"invented_later\",\"payload\":{}}", &arena);
    try testing.expectEqual(Kind.observation, message.kind);
    try testing.expectEqualStrings("invented_later", message.type);
}

test "a control request carries its inner subtype" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const message = try parseForTest("{\"type\":\"control_request\",\"request_id\":\"r1\",\"request\":{\"subtype\":\"can_use_tool\"}}", &arena);
    try testing.expectEqual(Kind.control_request, message.kind);
    try testing.expectEqualStrings("r1", message.request_id);
    try testing.expectEqualStrings("can_use_tool", message.subtype);
}

test "a control response separates success from error" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const ok = try parseForTest("{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"r1\",\"response\":{}}}", &arena);
    try testing.expect(ok.response.?.success);
    try testing.expectEqualStrings("r1", ok.response.?.request_id);

    const failed = try parseForTest("{\"type\":\"control_response\",\"response\":{\"subtype\":\"error\",\"request_id\":\"r2\",\"error\":\"nope\"}}", &arena);
    try testing.expect(!failed.response.?.success);
    try testing.expectEqualStrings("nope", failed.response.?.err);

    const null_payload = try parseForTest("{\"type\":\"control_response\",\"response\":{\"subtype\":\"success\",\"request_id\":\"r3\",\"response\":null}}", &arena);
    try testing.expectEqual(@as(?std.json.Value, null), null_payload.response.?.response);
}

test "a frame that is not exactly one object is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectError(Error.InvalidMessage, parseForTest("[]", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"type\":\"a\"} {\"type\":\"b\"}", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("null", &arena));
}
