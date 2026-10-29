const std = @import("std");
const harness_pins = @import("harness_pins");
const gojson = @import("gojson");

pub const Error = error{InvalidWire} || std.mem.Allocator.Error;

pub const method_event = "session.event";

pub const Id = union(enum) {
    integer: i64,
    string: []const u8,
};

pub const Kind = enum { request, notification, response, failure };

pub const Message = struct {
    kind: Kind,
    id: ?Id = null,
    method: []const u8 = "",
    params: ?std.json.Value = null,
    result: ?std.json.Value = null,
    failure_code: i64 = 0,
    failure_message: []const u8 = "",
};

pub fn parseMessage(arena: std.mem.Allocator, frame: []const u8) Error!Message {
    if (!gojson.withinNestingLimit(frame)) return Error.InvalidWire;
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, frame, .{}) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return Error.InvalidWire,
    };
    if (document != .object) return Error.InvalidWire;
    const object = document.object;
    const version = object.get("jsonrpc") orelse return Error.InvalidWire;
    if (version != .string or !std.mem.eql(u8, version.string, "2.0")) return Error.InvalidWire;
    var message = Message{ .kind = .notification };
    if (object.get("id")) |id| message.id = switch (id) {
        .integer => |number| .{ .integer = number },
        .string => |name| .{ .string = name },
        else => return Error.InvalidWire,
    };
    const result = object.get("result");
    const failure = object.get("error");
    if (object.get("method")) |method| {
        if (method != .string or method.string.len == 0 or result != null or failure != null) return Error.InvalidWire;
        message.method = method.string;
        message.params = object.get("params");
        message.kind = if (message.id == null) .notification else .request;
        return message;
    }
    if (message.id == null or (result == null) == (failure == null)) return Error.InvalidWire;
    if (result) |value| {
        message.kind = .response;
        message.result = value;
        return message;
    }
    const body = failure.?;
    if (body != .object) return Error.InvalidWire;
    const code = body.object.get("code") orelse return Error.InvalidWire;
    if (code != .integer) return Error.InvalidWire;
    const said = text(body.object, "message");
    if (said.len == 0) return Error.InvalidWire;
    message.kind = .failure;
    message.failure_code = code.integer;
    message.failure_message = said;
    return message;
}

pub fn encodeRequest(arena: std.mem.Allocator, id: i64, method: []const u8, params: std.json.Value) Error![]const u8 {
    return std.json.Stringify.valueAlloc(arena, .{ .jsonrpc = "2.0", .id = id, .method = method, .params = params }, .{});
}

pub fn encodeFailure(arena: std.mem.Allocator, id: Id, code: i64, message: []const u8) Error![]const u8 {
    const error_body = .{ .code = code, .message = message };
    return switch (id) {
        .integer => |number| std.json.Stringify.valueAlloc(arena, .{ .jsonrpc = "2.0", .id = number, .@"error" = error_body }, .{}),
        .string => |name| std.json.Stringify.valueAlloc(arena, .{ .jsonrpc = "2.0", .id = name, .@"error" = error_body }, .{}),
    };
}

pub const EventType = enum {
    user_message,
    turn_start,
    turn_end,
    message_delta,
    message,
    reasoning_delta,
    reasoning,
    usage,
    tool_start,
    tool_partial,
    shell_output,
    tool_complete,
    permission_requested,
    permission_completed,
    user_input_requested,
    user_input_completed,
    assistant_idle,
    session_idle,
    session_error,
    model_change,

    const names = [_][]const u8{
        "user.message",                  "assistant.turn_start", "assistant.turn_end",
        "assistant.message_delta",       "assistant.message",    "assistant.reasoning_delta",
        "assistant.reasoning",           "assistant.usage",      "tool.execution_start",
        "tool.execution_partial_result", "tool.shell_output",    "tool.execution_complete",
        "permission.requested",          "permission.completed", "user_input.requested",
        "user_input.completed",          "assistant.idle",       "session.idle",
        "session.error",                 "session.model_change",
    };

    pub fn wire(self: EventType) []const u8 {
        return names[@backingInt(self)];
    }

    pub fn parse(name: []const u8) ?EventType {
        for (names, 0..) |candidate, index| {
            if (std.mem.eql(u8, candidate, name)) return @fromBackingInt(@intCast(index));
        }
        return null;
    }
};

pub const Event = struct {
    session_id: []const u8,
    type_name: []const u8,
    kind: ?EventType,
    ephemeral: bool,
    data: std.json.ObjectMap,
};

pub fn decodeEvent(message: Message) Error!?Event {
    if (message.kind != .notification or !std.mem.eql(u8, message.method, method_event)) return null;
    const params = message.params orelse return Error.InvalidWire;
    if (params != .object) return Error.InvalidWire;
    const session_id = text(params.object, "sessionId");
    const event = params.object.get("event") orelse return Error.InvalidWire;
    if (session_id.len == 0 or event != .object) return Error.InvalidWire;
    const type_name = text(event.object, "type");
    if (type_name.len == 0) return Error.InvalidWire;
    const data = event.object.get("data") orelse return Error.InvalidWire;
    if (data != .object) return Error.InvalidWire;
    const ephemeral = event.object.get("ephemeral");
    return .{
        .session_id = session_id,
        .type_name = type_name,
        .kind = EventType.parse(type_name),
        .ephemeral = ephemeral != null and ephemeral.? == .bool and ephemeral.?.bool,
        .data = data.object,
    };
}

pub fn text(object: std.json.ObjectMap, name: []const u8) []const u8 {
    const value = object.get(name) orelse return "";
    return if (value == .string) value.string else "";
}

const testing = std.testing;

test "a response, a failure, a notification and a request each parse as what they are" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    const pong = try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"version\":\"1.0.98\",\"protocolVersion\":3}}");
    try testing.expectEqual(Kind.response, pong.kind);
    try testing.expectEqual(@as(i64, 1), pong.id.?.integer);
    try testing.expectEqualStrings(harness_pins.copilot_cli_endpoint_version, text(pong.result.?.object, "version"));

    const refused = try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"id\":2,\"error\":{\"code\":-32601,\"message\":\"no such method\"}}");
    try testing.expectEqual(Kind.failure, refused.kind);
    try testing.expectEqual(@as(i64, -32601), refused.failure_code);
    try testing.expectEqualStrings("no such method", refused.failure_message);

    const event = try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{}}");
    try testing.expectEqual(Kind.notification, event.kind);

    const asked = try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"id\":\"q1\",\"method\":\"gitHubToken.getToken\",\"params\":{}}");
    try testing.expectEqual(Kind.request, asked.kind);
    try testing.expectEqualStrings("q1", asked.id.?.string);
}

test "a frame that is not JSON-RPC 2.0 is refused rather than guessed at" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    for ([_][]const u8{
        "not json",
        "[]",
        "{\"id\":1,\"result\":{}}",
        "{\"jsonrpc\":\"1.0\",\"id\":1,\"result\":{}}",
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{},\"error\":{\"code\":1}}",
        "{\"jsonrpc\":\"2.0\",\"result\":{}}",
        "{\"jsonrpc\":\"2.0\",\"id\":true,\"result\":{}}",
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"message\":\"no code\"}}",
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1}}",
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":7}}",
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"error\":{\"code\":1,\"message\":\"\"}}",
        "{\"jsonrpc\":\"2.0\",\"method\":\"\"}",
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"m\",\"result\":{}}",
    }) |frame| {
        try testing.expectError(Error.InvalidWire, parseMessage(arena.allocator(), frame));
    }
}

fn nested(allocator: std.mem.Allocator, arrays: usize) ![]const u8 {
    var body = std.ArrayList(u8).empty;
    try body.appendSlice(allocator, "{\"jsonrpc\":\"2.0\",\"method\":\"x\",\"params\":");
    try body.appendNTimes(allocator, '[', arrays);
    try body.appendNTimes(allocator, ']', arrays);
    try body.appendSlice(allocator, "}");
    return body.items;
}

test "a frame nested past ten thousand containers is refused rather than recursed into" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try testing.expectEqual(Kind.notification, (try parseMessage(scratch, try nested(scratch, 9999))).kind);
    try testing.expectError(Error.InvalidWire, parseMessage(scratch, try nested(scratch, 10000)));
}

test "a request encodes as JSON-RPC 2.0 with its id, method and params" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var params: std.json.ObjectMap = .empty;
    try params.put(scratch, "sessionId", .{ .string = "s1" });
    const encoded = try encodeRequest(scratch, 7, "session.abort", .{ .object = params });
    const round = try parseMessage(scratch, encoded);
    try testing.expectEqual(Kind.request, round.kind);
    try testing.expectEqual(@as(i64, 7), round.id.?.integer);
    try testing.expectEqualStrings("session.abort", round.method);
    try testing.expectEqualStrings("s1", text(round.params.?.object, "sessionId"));

    const declined = try parseMessage(scratch, try encodeFailure(scratch, .{ .string = "q1" }, -32601, "not handled"));
    try testing.expectEqual(Kind.failure, declined.kind);
    try testing.expectEqualStrings("q1", declined.id.?.string);
    try testing.expectEqualStrings("not handled", declined.failure_message);
}

test "a session event names its session and type, and an unmapped type is observed rather than refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const delta = (try decodeEvent(try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"s1\",\"event\":{\"type\":\"assistant.message_delta\",\"data\":{\"messageId\":\"m1\",\"deltaContent\":\"pong\"},\"ephemeral\":true,\"id\":\"e1\"}}}"))).?;
    try testing.expectEqualStrings("s1", delta.session_id);
    try testing.expectEqual(EventType.message_delta, delta.kind.?);
    try testing.expect(delta.ephemeral);
    try testing.expectEqualStrings("pong", text(delta.data, "deltaContent"));

    const observed = (try decodeEvent(try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"s1\",\"event\":{\"type\":\"session.title_changed\",\"data\":{},\"ephemeral\":null}}}"))).?;
    try testing.expect(observed.kind == null);
    try testing.expect(!observed.ephemeral);
    try testing.expectEqualStrings("session.title_changed", observed.type_name);

    try testing.expect(try decodeEvent(try parseMessage(scratch, "{\"jsonrpc\":\"2.0\",\"method\":\"session.lifecycle\",\"params\":{}}")) == null);
    for ([_][]const u8{
        "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\"}",
        "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"event\":{\"type\":\"session.idle\",\"data\":{}}}}",
        "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"s1\",\"event\":{\"data\":{}}}}",
        "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"s1\",\"event\":{\"type\":\"session.idle\"}}}",
    }) |frame| {
        try testing.expectError(Error.InvalidWire, decodeEvent(try parseMessage(scratch, frame)));
    }
}

test "every mapped event type round-trips through its wire name" {
    for (std.enums.values(EventType)) |kind| {
        try testing.expectEqual(kind, EventType.parse(kind.wire()).?);
    }
    try testing.expect(EventType.parse("session.start") == null);
}

test "native negative-zero usage keeps its floating sign rather than becoming an integer" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const message = try parseMessage(arena.allocator(), "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"ses\",\"event\":{\"type\":\"assistant.usage\",\"data\":{\"inputTokens\":-0}}}}");
    const event = (try decodeEvent(message)).?;
    const value = event.data.get("inputTokens").?;
    try testing.expect(value == .float);
    try testing.expect(value.float == 0 and std.math.signbit(value.float));
}
