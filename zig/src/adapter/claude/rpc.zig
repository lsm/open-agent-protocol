const std = @import("std");

pub const default_frame_limit = 8 << 20;

pub const Error = error{
    FrameTooLarge,
    InvalidMessage,
    InvalidControl,
};

pub const invalid_message_prefix = "claude rpc: invalid stream-json message";
pub const invalid_control_prefix = "claude rpc: invalid control-plane message";
pub const invalid_frame_prefix = "claude native: invalid frame for a known type";

fn frameDetail(comptime detail: []const u8) []const u8 {
    return invalid_frame_prefix ++ ": " ++ detail;
}

pub const Diagnostic = struct {
    message: []const u8 = "",

    fn invalid(self: *Diagnostic, comptime detail: []const u8) Error {
        self.message = invalid_message_prefix ++ ": " ++ detail;
        return Error.InvalidMessage;
    }

    fn invalidControl(self: *Diagnostic, comptime detail: []const u8) Error {
        self.message = invalid_control_prefix ++ ": " ++ detail;
        return Error.InvalidControl;
    }

    fn refuseFrame(self: *Diagnostic, message: []const u8) Error {
        self.message = message;
        return Error.InvalidMessage;
    }
};

pub const Need = enum { present, text, array, number, boolean, text_array };

pub const Member = struct {
    path: []const []const u8,
    need: Need = .text,
    detail: []const u8,
};

const terminal_task_statuses = [_][]const u8{ "completed", "failed", "stopped", "killed" };

const user_detail = frameDetail("user frame requires message.role and message.content");
const assistant_detail = frameDetail("assistant frame requires message.model and message.content");
const stream_event_detail = frameDetail("stream_event requires event, uuid, and session_id");
const tool_progress_detail = frameDetail("tool_progress requires tool_use_id, tool_name, and session_id");
const command_lifecycle_detail = frameDetail("command_lifecycle requires command_uuid, state, and session_id");
const conversation_reset_detail = frameDetail("conversation_reset requires new_conversation_id, uuid, and session_id");
const init_detail = frameDetail("init frame requires session_id, model, and tools");
const session_state_detail = frameDetail("session_state_changed requires state");
const task_started_detail = frameDetail("task_started requires task_id, description, uuid, and session_id");
const task_progress_detail = frameDetail("task_progress requires task_id, description, uuid, and session_id");
const task_notification_detail = frameDetail("task_notification requires task_id, status, output_file, summary, uuid, and session_id");
const task_status_detail = frameDetail("task_notification status is not completed, failed, or stopped");
const task_updated_detail = frameDetail("task_updated requires task_id");

const user_members = [_]Member{
    .{ .path = &.{ "message", "role" }, .detail = user_detail },
    .{ .path = &.{ "message", "content" }, .need = .present, .detail = user_detail },
};
const assistant_members = [_]Member{
    .{ .path = &.{ "message", "model" }, .detail = assistant_detail },
    .{ .path = &.{ "message", "content" }, .need = .array, .detail = assistant_detail },
};
const result_members = [_]Member{
    .{ .path = &.{"subtype"}, .detail = frameDetail("result frame requires subtype") },
    .{ .path = &.{"session_id"}, .detail = frameDetail("result frame requires session_id") },
};
const stream_event_members = [_]Member{
    .{ .path = &.{"event"}, .need = .present, .detail = stream_event_detail },
    .{ .path = &.{"uuid"}, .detail = stream_event_detail },
    .{ .path = &.{"session_id"}, .detail = stream_event_detail },
};
const tool_progress_members = [_]Member{
    .{ .path = &.{"tool_use_id"}, .detail = tool_progress_detail },
    .{ .path = &.{"tool_name"}, .detail = tool_progress_detail },
    .{ .path = &.{"session_id"}, .detail = tool_progress_detail },
};
const command_lifecycle_members = [_]Member{
    .{ .path = &.{"command_uuid"}, .detail = command_lifecycle_detail },
    .{ .path = &.{"state"}, .detail = command_lifecycle_detail },
    .{ .path = &.{"session_id"}, .detail = command_lifecycle_detail },
};
const conversation_reset_members = [_]Member{
    .{ .path = &.{"new_conversation_id"}, .detail = conversation_reset_detail },
    .{ .path = &.{"uuid"}, .detail = conversation_reset_detail },
    .{ .path = &.{"session_id"}, .detail = conversation_reset_detail },
};
const init_members = [_]Member{
    .{ .path = &.{"session_id"}, .detail = init_detail },
    .{ .path = &.{"model"}, .detail = init_detail },
    .{ .path = &.{"tools"}, .need = .array, .detail = init_detail },
};
const session_state_members = [_]Member{
    .{ .path = &.{"state"}, .detail = session_state_detail },
};
const task_started_members = [_]Member{
    .{ .path = &.{"task_id"}, .detail = task_started_detail },
    .{ .path = &.{"description"}, .detail = task_started_detail },
    .{ .path = &.{"uuid"}, .detail = task_started_detail },
    .{ .path = &.{"session_id"}, .detail = task_started_detail },
};
const task_progress_members = [_]Member{
    .{ .path = &.{"task_id"}, .detail = task_progress_detail },
    .{ .path = &.{"description"}, .detail = task_progress_detail },
    .{ .path = &.{"uuid"}, .detail = task_progress_detail },
    .{ .path = &.{"session_id"}, .detail = task_progress_detail },
};
const task_notification_members = [_]Member{
    .{ .path = &.{"task_id"}, .detail = task_notification_detail },
    .{ .path = &.{"status"}, .detail = task_notification_detail },
    .{ .path = &.{"output_file"}, .detail = task_notification_detail },
    .{ .path = &.{"summary"}, .detail = task_notification_detail },
    .{ .path = &.{"uuid"}, .detail = task_notification_detail },
    .{ .path = &.{"session_id"}, .detail = task_notification_detail },
};
const task_updated_members = [_]Member{
    .{ .path = &.{"task_id"}, .detail = task_updated_detail },
};
const can_use_tool_members = [_]Member{
    .{ .path = &.{ "request", "tool_name" }, .detail = frameDetail("can_use_tool requires tool_name, input, and tool_use_id") },
    .{ .path = &.{ "request", "tool_use_id" }, .detail = frameDetail("can_use_tool requires tool_name, input, and tool_use_id") },
    .{ .path = &.{ "request", "input" }, .need = .present, .detail = frameDetail("can_use_tool requires tool_name, input, and tool_use_id") },
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

fn member(object: std.json.ObjectMap, path: []const []const u8) ?std.json.Value {
    var current = object;
    for (path, 0..) |key, depth| {
        const value = current.get(key) orelse return null;
        if (depth + 1 == path.len) return value;
        if (value != .object) return null;
        current = value.object;
    }
    return null;
}

fn unsatisfied(object: std.json.ObjectMap, required: []const Member) ?[]const u8 {
    for (required) |need| {
        const value = member(object, need.path) orelse return need.detail;
        switch (need.need) {
            .text => if (value != .string or value.string.len == 0) return need.detail,
            .array => if (value != .array) return need.detail,
            else => {},
        }
    }
    return null;
}

const Declared = struct {
    key: []const u8,
    need: Need,
};

const result_declared = [_]Declared{
    .{ .key = "duration_ms", .need = .number },
    .{ .key = "duration_api_ms", .need = .number },
    .{ .key = "is_error", .need = .boolean },
    .{ .key = "num_turns", .need = .number },
    .{ .key = "queued_turn_count", .need = .number },
    .{ .key = "api_error_status", .need = .number },
    .{ .key = "total_cost_usd", .need = .number },
    .{ .key = "errors", .need = .text_array },
    .{ .key = "user_message_uuid", .need = .text },
    .{ .key = "user_message_uuids", .need = .text_array },
    .{ .key = "terminal_reason", .need = .text },
    .{ .key = "result", .need = .text },
    .{ .key = "stop_reason", .need = .text },
    .{ .key = "uuid", .need = .text },
};
const stream_event_declared = [_]Declared{
    .{ .key = "user_message_uuid", .need = .text },
    .{ .key = "user_message_uuids", .need = .text_array },
};
const assistant_declared = [_]Declared{
    .{ .key = "user_message_uuid", .need = .text },
    .{ .key = "user_message_uuids", .need = .text_array },
    .{ .key = "is_api_error_message", .need = .boolean },
    .{ .key = "error", .need = .text },
};
const task_updated_declared = [_]Declared{
    .{ .key = "uuid", .need = .text },
    .{ .key = "session_id", .need = .text },
};

fn declaredMembers(frame_type: []const u8, subtype: []const u8) []const Declared {
    if (std.mem.eql(u8, frame_type, "result")) return &result_declared;
    if (std.mem.eql(u8, frame_type, "stream_event")) return &stream_event_declared;
    if (std.mem.eql(u8, frame_type, "assistant")) return &assistant_declared;
    if (std.mem.eql(u8, frame_type, "system") and std.mem.eql(u8, subtype, "task_updated")) return &task_updated_declared;
    return &.{};
}

fn wrongType(object: std.json.ObjectMap, declared: []const Declared) bool {
    for (declared) |need| {
        const value = object.get(need.key) orelse continue;
        if (value == .null) continue;
        const ok = switch (need.need) {
            .present => true,
            .text => value == .string,
            .array => value == .array,
            .number => value == .integer or value == .float,
            .boolean => value == .bool,
            .text_array => blk: {
                if (value != .array) break :blk false;
                for (value.array.items) |item| {
                    if (item != .string) break :blk false;
                }
                break :blk true;
            },
        };
        if (!ok) return true;
    }
    return false;
}

fn observationMembers(frame_type: []const u8, subtype: []const u8) []const Member {
    if (std.mem.eql(u8, frame_type, "user")) return &user_members;
    if (std.mem.eql(u8, frame_type, "assistant")) return &assistant_members;
    if (std.mem.eql(u8, frame_type, "result")) return &result_members;
    if (std.mem.eql(u8, frame_type, "stream_event")) return &stream_event_members;
    if (std.mem.eql(u8, frame_type, "tool_progress")) return &tool_progress_members;
    if (std.mem.eql(u8, frame_type, "command_lifecycle")) return &command_lifecycle_members;
    if (std.mem.eql(u8, frame_type, "conversation_reset")) return &conversation_reset_members;
    if (!std.mem.eql(u8, frame_type, "system")) return &.{};
    if (std.mem.eql(u8, subtype, "init")) return &init_members;
    if (std.mem.eql(u8, subtype, "session_state_changed")) return &session_state_members;
    if (std.mem.eql(u8, subtype, "task_started")) return &task_started_members;
    if (std.mem.eql(u8, subtype, "task_progress")) return &task_progress_members;
    if (std.mem.eql(u8, subtype, "task_notification")) return &task_notification_members;
    if (std.mem.eql(u8, subtype, "task_updated")) return &task_updated_members;
    return &.{};
}

fn taskStatusIsTerminal(object: std.json.ObjectMap) bool {
    const status = member(object, &.{"status"}) orelse return false;
    if (status != .string) return false;
    for (terminal_task_statuses) |terminal| {
        if (std.mem.eql(u8, status.string, terminal)) return true;
    }
    return false;
}

pub fn parseMessage(arena: std.mem.Allocator, data: []const u8, diagnostic: ?*Diagnostic) !Message {
    var discard = Diagnostic{};
    const report = diagnostic orelse &discard;
    if (data.len == 0 or data[0] != '{' or data[data.len - 1] != '}') {
        return report.invalid("frame must be exactly one JSON object");
    }
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, data, .{}) catch {
        return report.invalid("frame is not decodable JSON");
    };
    if (parsed != .object) return report.invalid("frame must be exactly one JSON object");
    const object = parsed.object;

    const type_value = object.get("type") orelse return report.invalid("frame requires type");
    if (type_value != .string or type_value.string.len == 0) return report.invalid("frame requires type");

    var message = Message{
        .kind = .observation,
        .type = type_value.string,
        .raw = data,
        .object = parsed,
    };
    if (object.get("subtype")) |subtype| {
        if (subtype != .string or subtype.string.len == 0) return report.invalid("frame subtype must not be empty");
        message.subtype = subtype.string;
    }

    if (std.mem.eql(u8, message.type, type_control_request)) {
        message.kind = .control_request;
        const id = object.get("request_id") orelse return report.invalidControl("control request requires request_id");
        if (id != .string or id.string.len == 0) return report.invalidControl("control request requires request_id");
        message.request_id = id.string;
        const request = object.get("request") orelse return report.invalidControl("control request requires request");
        if (request != .object) return report.invalidControl("control request requires request");
        const subtype = request.object.get("subtype") orelse return report.invalidControl("control request requires request.subtype");
        if (subtype != .string or subtype.string.len == 0) return report.invalidControl("control request requires request.subtype");
        message.subtype = subtype.string;
        if (std.mem.eql(u8, message.subtype, "can_use_tool")) {
            if (unsatisfied(object, &can_use_tool_members)) |detail| return report.refuseFrame(detail);
        }
        return message;
    }
    if (std.mem.eql(u8, message.type, type_control_response)) {
        message.kind = .control_response;
        const response = object.get("response") orelse return report.invalidControl("control response requires response");
        if (response != .object) return report.invalidControl("control response requires response");
        const state = response.object.get("subtype") orelse return report.invalidControl("control response requires response.subtype");
        if (state != .string or state.string.len == 0) return report.invalidControl("control response requires response.subtype");
        const id = response.object.get("request_id") orelse return report.invalidControl("control response requires response.request_id");
        if (id != .string or id.string.len == 0) return report.invalidControl("control response requires response.request_id");
        var envelope = ControlResponse{ .request_id = id.string };
        if (std.mem.eql(u8, state.string, "success")) {
            envelope.success = true;
            if (response.object.get("response")) |payload| {
                if (payload != .null) envelope.response = payload;
            }
        } else if (std.mem.eql(u8, state.string, "error")) {
            const detail = response.object.get("error") orelse return report.invalidControl("an error control response requires error");
            if (detail != .string or detail.string.len == 0) return report.invalidControl("an error control response requires error");
            envelope.err = detail.string;
        } else return report.invalidControl("control response subtype is neither success nor error");
        message.response = envelope;
        return message;
    }
    if (std.mem.eql(u8, message.type, type_control_cancel)) {
        message.kind = .control_cancel;
        const id = object.get("request_id") orelse return report.invalidControl("control cancel requires request_id");
        if (id != .string or id.string.len == 0) return report.invalidControl("control cancel requires request_id");
        message.request_id = id.string;
        return message;
    }
    if (unsatisfied(object, observationMembers(message.type, message.subtype))) |detail| {
        return report.refuseFrame(detail);
    }
    if (std.mem.eql(u8, message.type, "system") and std.mem.eql(u8, message.subtype, "task_notification") and !taskStatusIsTerminal(object)) {
        return report.refuseFrame(task_status_detail);
    }
    if (wrongType(object, declaredMembers(message.type, message.subtype))) {
        return report.refuseFrame(frameDetail("frame declares a member of the wrong type"));
    }
    return message;
}

const testing = std.testing;

fn parseForTest(text: []const u8, arena: *std.heap.ArenaAllocator) !Message {
    return parseMessage(arena.allocator(), text, null);
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
    const message = try parseForTest("{\"type\":\"control_request\",\"request_id\":\"r1\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Bash\",\"tool_use_id\":\"t1\",\"input\":{}}}", &arena);
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

test "a decode failure names the reason the transport reports" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var truncated = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"result\",\"subtype\":\"succe", &truncated));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": frame must be exactly one JSON object", truncated.message);

    var undecodable = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":}", &undecodable));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": frame is not decodable JSON", undecodable.message);

    var accepted = Diagnostic{};
    _ = try parseMessage(arena.allocator(), "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\"}", &accepted);
    try testing.expectEqualStrings("", accepted.message);
}

test "a frame that is not exactly one object is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectError(Error.InvalidMessage, parseForTest("[]", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("{\"type\":\"a\"} {\"type\":\"b\"}", &arena));
    try testing.expectError(Error.InvalidMessage, parseForTest("null", &arena));
}

fn refuses(arena: *std.heap.ArenaAllocator, text: []const u8) !void {
    var diagnostic = Diagnostic{};
    testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), text, &diagnostic)) catch |err| {
        std.debug.print("\naccepted a frame the oracle refuses: {s}\n", .{text});
        return err;
    };
}

fn accepts(arena: *std.heap.ArenaAllocator, text: []const u8) !void {
    _ = parseMessage(arena.allocator(), text, null) catch |err| {
        std.debug.print("\nrefused a frame the oracle accepts: {s}\n", .{text});
        return err;
    };
}

test "a frame missing a member its own type requires is fatal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    try refuses(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\"}}");
    try accepts(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\",\"content\":\"interrupted\"}}");

    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":\"text\"}}");
    try accepts(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[]}}");

    try refuses(&arena, "{\"type\":\"result\",\"subtype\":\"success\"}");
    try refuses(&arena, "{\"type\":\"result\",\"session_id\":\"s\"}");
    try refuses(&arena, "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"\"}");
    try refuses(&arena, "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":7}");
    try refuses(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":\"hello\"}");

    try refuses(&arena, "{\"type\":\"stream_event\",\"event\":{},\"uuid\":\"e1\"}");
    try refuses(&arena, "{\"type\":\"stream_event\",\"session_id\":\"s\",\"uuid\":\"e1\"}");

    try refuses(&arena, "{\"type\":\"tool_progress\",\"session_id\":\"s\",\"tool_use_id\":\"t\"}");
    try refuses(&arena, "{\"type\":\"command_lifecycle\",\"session_id\":\"s\",\"state\":\"x\"}");
    try refuses(&arena, "{\"type\":\"conversation_reset\",\"session_id\":\"s\",\"uuid\":\"u\"}");
}

test "a system frame is judged by its subtype, and an unknown one by nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"model\":\"m\"}");
    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"tools\":[]}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"model\":\"m\",\"tools\":[]}");

    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"session_state_changed\",\"session_id\":\"s\"}");
    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"task_started\",\"session_id\":\"s\",\"task_id\":\"t\",\"uuid\":\"u\"}");
    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"task_updated\",\"session_id\":\"s\"}");

    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"task_notification\",\"session_id\":\"s\",\"task_id\":\"t\",\"status\":\"running\",\"output_file\":\"/o\",\"summary\":\"d\",\"uuid\":\"u\"}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"task_notification\",\"session_id\":\"s\",\"task_id\":\"t\",\"status\":\"killed\",\"output_file\":\"/o\",\"summary\":\"d\",\"uuid\":\"u\"}");

    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"status\"}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"invented_later\"}");
    try accepts(&arena, "{\"type\":\"keep_alive\"}");
    try accepts(&arena, "{\"type\":\"invented_later\"}");
}

test "a permission ask missing what the endpoint must answer is fatal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const prefix = "{\"type\":\"control_request\",\"request_id\":\"r1\",\"request\":{\"subtype\":\"can_use_tool\",";

    try refuses(&arena, prefix ++ "\"tool_use_id\":\"t\",\"input\":{}}}");
    try refuses(&arena, prefix ++ "\"tool_name\":\"Bash\",\"input\":{}}}");
    try refuses(&arena, prefix ++ "\"tool_name\":\"Bash\",\"tool_use_id\":\"t\"}}");
    try accepts(&arena, prefix ++ "\"tool_name\":\"Bash\",\"tool_use_id\":\"t\",\"input\":{}}}");
    try accepts(&arena, "{\"type\":\"control_request\",\"request_id\":\"r1\",\"request\":{\"subtype\":\"hook_callback\"}}");
}

test "a refusal names itself the way the oracle does" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var control = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_request\",\"request\":{\"subtype\":\"can_use_tool\"}}", &control));
    try testing.expect(std.mem.startsWith(u8, control.message, invalid_control_prefix ++ ": "));

    var cancel = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_cancel_request\"}", &cancel));
    try testing.expect(std.mem.startsWith(u8, cancel.message, invalid_control_prefix ++ ": "));

    var response = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_response\",\"response\":{\"subtype\":\"other\",\"request_id\":\"r\"}}", &response));
    try testing.expect(std.mem.startsWith(u8, response.message, invalid_control_prefix ++ ": "));

    var typeless = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"subtype\":\"init\"}", &typeless));
    try testing.expect(std.mem.startsWith(u8, typeless.message, invalid_message_prefix ++ ": "));

    var frame = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"model\":\"m\"}", &frame));
    try testing.expectEqualStrings(invalid_frame_prefix ++ ": init frame requires session_id, model, and tools", frame.message);

    var gate = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"control_request\",\"request_id\":\"r\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Bash\",\"input\":{}}}", &gate));
    try testing.expectEqualStrings(invalid_frame_prefix ++ ": can_use_tool requires tool_name, input, and tool_use_id", gate.message);
}

test "a declared member of the wrong type is fatal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const result = "{\"type\":\"result\",\"session_id\":\"s\",\"subtype\":\"success\",";

    try refuses(&arena, result ++ "\"is_error\":\"yes\"}");
    try refuses(&arena, result ++ "\"queued_turn_count\":\"2\"}");
    try refuses(&arena, result ++ "\"duration_ms\":\"130\"}");
    try refuses(&arena, result ++ "\"errors\":[7]}");
    try refuses(&arena, result ++ "\"user_message_uuids\":\"turn-1\"}");
    try refuses(&arena, result ++ "\"terminal_reason\":7}");

    try accepts(&arena, result ++ "\"is_error\":true,\"queued_turn_count\":2,\"duration_ms\":130,\"errors\":[\"a\"],\"user_message_uuids\":[\"turn-1\"]}");
    try accepts(&arena, result ++ "\"is_error\":false,\"queued_turn_count\":null,\"stop_reason\":null}");
    try accepts(&arena, result ++ "\"total_cost_usd\":0.5}");
}
