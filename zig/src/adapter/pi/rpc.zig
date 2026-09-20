const std = @import("std");

pub const frame_limit_default: usize = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidFrame,
};

pub const Kind = enum { response, extension_ui_request, event };

pub const Frame = struct {
    kind: Kind,
    event_type: []const u8 = "",
    raw: []const u8,
};

const EventShape = struct {
    name: []const u8,
    required: []const []const u8 = &.{},
    optional: []const []const u8 = &.{},
};

const event_shapes = [_]EventShape{
    .{ .name = "agent_start" },
    .{ .name = "agent_end", .required = &.{ "messages", "willRetry" } },
    .{ .name = "agent_settled" },
    .{ .name = "turn_start" },
    .{ .name = "turn_end", .required = &.{ "message", "toolResults" } },
    .{ .name = "message_start", .required = &.{"message"} },
    .{ .name = "message_update", .required = &.{ "usage", "assistantMessageEvent" } },
    .{ .name = "message_end", .required = &.{"message"} },
    .{ .name = "tool_execution_start", .required = &.{ "toolCallId", "toolName", "args" } },
    .{ .name = "tool_execution_update", .required = &.{ "toolCallId", "toolName", "args", "partialResult" } },
    .{ .name = "tool_execution_end", .required = &.{ "toolCallId", "toolName", "result", "isError" } },
    .{ .name = "queue_update", .required = &.{ "steering", "followUp" } },
    .{ .name = "compaction_start", .required = &.{"reason"} },
    .{ .name = "compaction_end", .required = &.{ "reason", "aborted", "willRetry" }, .optional = &.{ "result", "errorMessage" } },
    .{ .name = "entry_appended", .required = &.{"entry"} },
    .{ .name = "session_info_changed", .optional = &.{"name"} },
    .{ .name = "thinking_level_changed", .required = &.{"level"} },
    .{ .name = "auto_retry_start", .required = &.{ "attempt", "maxAttempts", "delayMs", "errorMessage" } },
    .{ .name = "auto_retry_end", .required = &.{ "success", "attempt" }, .optional = &.{"finalError"} },
    .{ .name = "summarization_retry_scheduled", .required = &.{ "attempt", "maxAttempts", "delayMs", "errorMessage" } },
    .{ .name = "summarization_retry_attempt_start", .required = &.{"source"}, .optional = &.{"reason"} },
    .{ .name = "summarization_retry_finished" },
    .{ .name = "bash_execution_update", .required = &.{"delta"}, .optional = &.{"id"} },
    .{ .name = "extension_error", .required = &.{ "extensionPath", "event", "error" } },
};

fn eventShape(name: []const u8) ?EventShape {
    for (event_shapes) |shape| {
        if (std.mem.eql(u8, shape.name, name)) return shape;
    }
    return null;
}

fn namedIn(names: []const []const u8, name: []const u8) bool {
    for (names) |entry| {
        if (std.mem.eql(u8, entry, name)) return true;
    }
    return false;
}

pub const Decoder = struct {
    source: []const u8,
    at: usize = 0,
    limit: usize = frame_limit_default,

    pub fn next(self: *Decoder, allocator: std.mem.Allocator) !?Frame {
        const line = try self.readFrame() orelse return null;
        return try classify(allocator, line);
    }

    fn readFrame(self: *Decoder) !?[]const u8 {
        if (self.at >= self.source.len) return null;
        const rest = self.source[self.at..];
        const break_at = std.mem.indexOfScalar(u8, rest, '\n') orelse {
            if (rest.len > self.limit) return Error.FrameTooLarge;
            return Error.InvalidFrame;
        };
        if (break_at > self.limit) return Error.FrameTooLarge;
        const line = rest[0..break_at];
        self.at += break_at + 1;
        if (line.len == 0) return Error.InvalidFrame;
        if (std.mem.indexOfScalar(u8, line, '\r') != null) return Error.InvalidFrame;
        if (!std.unicode.utf8ValidateSlice(line)) return Error.InvalidFrame;
        return line;
    }
};

pub fn classify(allocator: std.mem.Allocator, line: []const u8) !Frame {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch return Error.InvalidFrame;
    defer parsed.deinit();
    if (parsed.value != .object) return Error.InvalidFrame;
    const object = parsed.value.object;

    const declared = object.get("type") orelse return Error.InvalidFrame;
    if (declared != .string or declared.string.len == 0) return Error.InvalidFrame;

    if (std.mem.eql(u8, declared.string, "response")) {
        try validateResponse(object);
        return .{ .kind = .response, .raw = line };
    }
    if (std.mem.eql(u8, declared.string, "extension_ui_request")) {
        try validateExtensionRequest(object);
        return .{ .kind = .extension_ui_request, .raw = line };
    }
    const shape = eventShape(declared.string) orelse return Error.InvalidFrame;
    try validateMembers(object, shape);
    return .{ .kind = .event, .event_type = shape.name, .raw = line };
}

fn validateMembers(object: std.json.ObjectMap, shape: EventShape) !void {
    for (shape.required) |name| {
        if (object.get(name) == null) return Error.InvalidFrame;
    }
    var it = object.iterator();
    while (it.next()) |entry| {
        const name = entry.key_ptr.*;
        if (std.mem.eql(u8, name, "type")) continue;
        if (namedIn(shape.required, name)) continue;
        if (namedIn(shape.optional, name)) continue;
        return Error.InvalidFrame;
    }
}

fn validateResponse(object: std.json.ObjectMap) !void {
    const command = object.get("command") orelse return Error.InvalidFrame;
    if (command != .string or command.string.len == 0) return Error.InvalidFrame;
    const success = object.get("success") orelse return Error.InvalidFrame;
    if (success != .bool) return Error.InvalidFrame;
}

fn validateExtensionRequest(object: std.json.ObjectMap) !void {
    const id = object.get("id") orelse return Error.InvalidFrame;
    if (id != .string or id.string.len == 0) return Error.InvalidFrame;
    const method = object.get("method") orelse return Error.InvalidFrame;
    if (method != .string or method.string.len == 0) return Error.InvalidFrame;
}

test "a frame is one LF-terminated line and the terminator is required" {
    var decoder = Decoder{ .source = "{\"type\":\"agent_start\"}" };
    try std.testing.expectError(Error.InvalidFrame, decoder.next(std.testing.allocator));

    var terminated = Decoder{ .source = "{\"type\":\"agent_start\"}\n" };
    const frame = try terminated.next(std.testing.allocator);
    try std.testing.expect(frame != null);
    try std.testing.expectEqual(Kind.event, frame.?.kind);
    try std.testing.expectEqualStrings("agent_start", frame.?.event_type);
    try std.testing.expect(try terminated.next(std.testing.allocator) == null);
}

test "a carriage return is not accepted as CRLF framing" {
    var decoder = Decoder{ .source = "{\"type\":\"agent_start\"}\r\n" };
    try std.testing.expectError(Error.InvalidFrame, decoder.next(std.testing.allocator));
}

test "an empty line is a framing defect rather than a skipped frame" {
    var decoder = Decoder{ .source = "\n{\"type\":\"agent_start\"}\n" };
    try std.testing.expectError(Error.InvalidFrame, decoder.next(std.testing.allocator));
}

test "a frame that is not UTF-8 is refused" {
    var decoder = Decoder{ .source = "{\"type\":\"agent_start\",\"x\":\"\xff\"}\n" };
    try std.testing.expectError(Error.InvalidFrame, decoder.next(std.testing.allocator));
}

test "a frame over the configured limit is refused rather than truncated" {
    var decoder = Decoder{ .source = "{\"type\":\"agent_start\"}\n", .limit = 4 };
    try std.testing.expectError(Error.FrameTooLarge, decoder.next(std.testing.allocator));
}

test "an unknown event type is refused, because this harness declares its union" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"invented_event\"}"));
}

test "a declared event missing a required member is refused" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"turn_end\",\"message\":{}}"));
    const complete = try classify(std.testing.allocator, "{\"type\":\"turn_end\",\"message\":{},\"toolResults\":[]}");
    try std.testing.expectEqual(Kind.event, complete.kind);
}

test "a declared event carrying a member its shape does not name is refused" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"agent_start\",\"surprise\":1}"));
}

test "an optional member is accepted and does not become required" {
    const without = try classify(std.testing.allocator, "{\"type\":\"session_info_changed\"}");
    try std.testing.expectEqual(Kind.event, without.kind);
    const with = try classify(std.testing.allocator, "{\"type\":\"session_info_changed\",\"name\":\"x\"}");
    try std.testing.expectEqual(Kind.event, with.kind);
}

test "a duplicate key is refused by the parser rather than silently resolved" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"agent_start\",\"type\":\"turn_start\"}"));
}

test "responses and extension requests are their own frame families" {
    const response = try classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":true}");
    try std.testing.expectEqual(Kind.response, response.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\"}"));

    const request = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"confirm\"}");
    try std.testing.expectEqual(Kind.extension_ui_request, request.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"method\":\"confirm\"}"));
}

test "extension_error is a frame this harness emits although its declared union omits it" {
    const frame = try classify(std.testing.allocator, "{\"type\":\"extension_error\",\"extensionPath\":\"/x\",\"event\":\"e\",\"error\":\"boom\"}");
    try std.testing.expectEqual(Kind.event, frame.kind);
}
