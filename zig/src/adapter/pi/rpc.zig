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

const commands = [_][]const u8{
    "prompt",                        "steer",               "follow_up",
    "abort",                         "clear_queue",         "new_session",
    "get_state",                     "set_model",           "cycle_model",
    "get_available_models",          "set_thinking_level",  "cycle_thinking_level",
    "get_available_thinking_levels", "set_steering_mode",   "set_follow_up_mode",
    "compact",                       "set_auto_compaction", "set_auto_retry",
    "abort_retry",                   "bash",                "abort_bash",
    "get_session_stats",             "export_html",         "switch_session",
    "fork",                          "clone",               "get_fork_messages",
    "get_entries",                   "get_tree",            "get_last_assistant_text",
    "set_session_name",              "get_messages",        "get_commands",
};

const MemberType = enum { text, flag, number, text_list, any };

const Omission = enum { on_null, on_zero };

const Member = struct { name: []const u8, kind: MemberType, omits: Omission = .on_zero };

const response_members = [_]Member{
    .{ .name = "id", .kind = .text },
    .{ .name = "type", .kind = .text },
    .{ .name = "command", .kind = .text },
    .{ .name = "success", .kind = .flag },
    .{ .name = "data", .kind = .any },
    .{ .name = "error", .kind = .text },
};

const extension_members = [_]Member{
    .{ .name = "type", .kind = .text },
    .{ .name = "id", .kind = .text },
    .{ .name = "method", .kind = .text },
    .{ .name = "title", .kind = .text },
    .{ .name = "options", .kind = .text_list },
    .{ .name = "timeout", .kind = .number },
    .{ .name = "message", .kind = .text },
    .{ .name = "placeholder", .kind = .text },
    .{ .name = "prefill", .kind = .text },
    .{ .name = "notifyType", .kind = .text },
    .{ .name = "statusKey", .kind = .text },
    .{ .name = "statusText", .kind = .text, .omits = .on_null },
    .{ .name = "widgetKey", .kind = .text },
    .{ .name = "widgetLines", .kind = .text_list },
    .{ .name = "widgetPlacement", .kind = .text },
    .{ .name = "text", .kind = .text },
};

fn omittedByMarshal(value: std.json.Value, member: Member) bool {
    if (value == .null) return true;
    if (member.omits == .on_null) return false;
    return switch (value) {
        .string => |text| text.len == 0,
        .array => |items| items.items.len == 0,
        else => false,
    };
}

fn declaredMember(table: []const Member, name: []const u8) ?Member {
    for (table) |entry| {
        if (std.mem.eql(u8, entry.name, name)) return entry;
    }
    return null;
}

fn memberTypeHolds(value: std.json.Value, kind: MemberType) bool {
    if (value == .null) return true;
    return switch (kind) {
        .any => true,
        .text => value == .string,
        .flag => value == .bool,
        .number => value == .integer,
        .text_list => blk: {
            if (value != .array) break :blk false;
            for (value.array.items) |entry| {
                if (entry != .string) break :blk false;
            }
            break :blk true;
        },
    };
}

const MethodShape = struct {
    name: []const u8,
    required_text: []const []const u8 = &.{},
    required_any: []const []const u8 = &.{},
    optional: []const []const u8 = &.{},
    constrained: []const u8 = "",
    permitted: []const []const u8 = &.{},
};

const method_shapes = [_]MethodShape{
    .{ .name = "select", .required_text = &.{"title"}, .required_any = &.{"options"}, .optional = &.{"timeout"} },
    .{ .name = "confirm", .required_text = &.{ "title", "message" }, .optional = &.{"timeout"} },
    .{ .name = "input", .required_text = &.{"title"}, .optional = &.{ "placeholder", "timeout" } },
    .{ .name = "editor", .required_text = &.{"title"}, .optional = &.{"prefill"} },
    .{ .name = "notify", .required_text = &.{"message"}, .optional = &.{"notifyType"}, .constrained = "notifyType", .permitted = &.{ "info", "warning", "error" } },
    .{ .name = "setStatus", .required_text = &.{"statusKey"}, .optional = &.{"statusText"} },
    .{ .name = "setWidget", .required_text = &.{"widgetKey"}, .optional = &.{ "widgetLines", "widgetPlacement" }, .constrained = "widgetPlacement", .permitted = &.{ "aboveEditor", "belowEditor" } },
    .{ .name = "setTitle", .required_text = &.{"title"} },
    .{ .name = "set_editor_text", .optional = &.{"text"} },
};

fn methodShape(name: []const u8) ?MethodShape {
    for (method_shapes) |shape| {
        if (std.mem.eql(u8, shape.name, name)) return shape;
    }
    return null;
}

fn nonEmptyString(object: std.json.ObjectMap, name: []const u8) bool {
    const value = object.get(name) orelse return false;
    return value == .string and value.string.len != 0;
}

fn presentAndNotNull(object: std.json.ObjectMap, name: []const u8) bool {
    const value = object.get(name) orelse return false;
    return value != .null;
}

fn validateResponse(object: std.json.ObjectMap) !void {
    const command = object.get("command") orelse return Error.InvalidFrame;
    if (command != .string or !namedIn(&commands, command.string)) return Error.InvalidFrame;

    var it = object.iterator();
    while (it.next()) |entry| {
        const member = declaredMember(&response_members, entry.key_ptr.*) orelse return Error.InvalidFrame;
        if (!memberTypeHolds(entry.value_ptr.*, member.kind)) return Error.InvalidFrame;
    }

    var succeeded = false;
    if (object.get("success")) |value| {
        if (value == .bool) succeeded = value.bool;
    }
    var reported = false;
    if (object.get("error")) |value| {
        reported = value == .string and value.string.len != 0;
    }
    if (succeeded and reported) return Error.InvalidFrame;
    if (!succeeded and !reported) return Error.InvalidFrame;
}

fn validateExtensionRequest(object: std.json.ObjectMap) !void {
    if (!nonEmptyString(object, "id")) return Error.InvalidFrame;
    const method = object.get("method") orelse return Error.InvalidFrame;
    if (method != .string) return Error.InvalidFrame;
    const shape = methodShape(method.string) orelse return Error.InvalidFrame;

    for (shape.required_text) |name| {
        if (!nonEmptyString(object, name)) return Error.InvalidFrame;
    }
    for (shape.required_any) |name| {
        if (!presentAndNotNull(object, name)) return Error.InvalidFrame;
    }
    if (shape.constrained.len != 0) {
        if (object.get(shape.constrained)) |value| {
            if (value != .null) {
                if (value != .string) return Error.InvalidFrame;
                if (value.string.len != 0 and !namedIn(shape.permitted, value.string)) return Error.InvalidFrame;
            }
        }
    }
    var it = object.iterator();
    while (it.next()) |entry| {
        const name = entry.key_ptr.*;
        const member = declaredMember(&extension_members, name) orelse return Error.InvalidFrame;
        if (!memberTypeHolds(entry.value_ptr.*, member.kind)) return Error.InvalidFrame;
        if (std.mem.eql(u8, name, "type") or std.mem.eql(u8, name, "id") or std.mem.eql(u8, name, "method")) continue;
        if (namedIn(shape.required_text, name)) continue;
        if (namedIn(shape.required_any, name)) continue;
        if (namedIn(shape.optional, name)) continue;
        if (omittedByMarshal(entry.value_ptr.*, member)) continue;
        return Error.InvalidFrame;
    }
}

fn admitsExtension(line: []const u8) !void {
    _ = try classify(std.testing.allocator, line);
}

fn refusesExtension(line: []const u8) !void {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, line));
}

test "a constrained member holding null is absent, and an empty one is unconstrained" {
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"notify\",\"message\":\"m\",\"notifyType\":null}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"notify\",\"message\":\"m\",\"notifyType\":\"\"}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"setWidget\",\"widgetKey\":\"k\",\"widgetPlacement\":null}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"notify\",\"message\":\"m\",\"notifyType\":\"nope\"}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"notify\",\"message\":\"m\",\"notifyType\":7}");
}

test "a member foreign to the method escapes the check when omitempty would drop it" {
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"title\":\"\"}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"title\":null}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"widgetLines\":[]}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"title\":\"x\"}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"widgetLines\":[\"a\"]}");
}

test "a pointer-carried member is dropped only by null, never by its zero value" {
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"statusText\":null}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"timeout\":null}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"statusText\":\"\"}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"set_editor_text\",\"text\":\"t\",\"timeout\":0}");
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

test "a response names a command the pinned union declares" {
    const ok = try classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":true}");
    try std.testing.expectEqual(Kind.response, ok.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"invented\",\"success\":true}"));
}

test "success and error are mutually determined on a response" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":true,\"error\":\"boom\"}"));
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":false}"));
    const failed = try classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":false,\"error\":\"boom\"}");
    try std.testing.expectEqual(Kind.response, failed.kind);
}

test "an absent success member is false rather than missing" {
    const failed = try classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"error\":\"boom\"}");
    try std.testing.expectEqual(Kind.response, failed.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\"}"));
}

test "a response carrying a member the struct does not declare is refused" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":true,\"surprise\":1}"));
}

test "an extension request names a method the pinned union declares" {
    const ok = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"confirm\",\"title\":\"t\",\"message\":\"m\"}");
    try std.testing.expectEqual(Kind.extension_ui_request, ok.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"invented\"}"));
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"method\":\"confirm\",\"title\":\"t\",\"message\":\"m\"}"));
}

test "each extension method requires the members its own shape names" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"confirm\",\"title\":\"t\"}"));
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"select\",\"title\":\"t\"}"));
    const selected = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"select\",\"title\":\"t\",\"options\":[]}");
    try std.testing.expectEqual(Kind.extension_ui_request, selected.kind);
    const editorText = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"set_editor_text\"}");
    try std.testing.expectEqual(Kind.extension_ui_request, editorText.kind);
}

test "a member valid for another method is not valid for this one" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"setTitle\",\"title\":\"t\",\"message\":\"m\"}"));
}

test "a constrained member is checked against its own value set" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"notify\",\"message\":\"m\",\"notifyType\":\"shout\"}"));
    const warned = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"notify\",\"message\":\"m\",\"notifyType\":\"warning\"}");
    try std.testing.expectEqual(Kind.extension_ui_request, warned.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"setWidget\",\"widgetKey\":\"k\",\"widgetPlacement\":\"sideways\"}"));
}

test "extension_error is a frame this harness emits although its declared union omits it" {
    const frame = try classify(std.testing.allocator, "{\"type\":\"extension_error\",\"extensionPath\":\"/x\",\"event\":\"e\",\"error\":\"boom\"}");
    try std.testing.expectEqual(Kind.event, frame.kind);
}

test "a JSON null reads as the decode no-op the pinned struct performs" {
    const absentSuccess = try classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":null,\"error\":\"boom\"}");
    try std.testing.expectEqual(Kind.response, absentSuccess.kind);
    const absentId = try classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"id\":null,\"success\":true}");
    try std.testing.expectEqual(Kind.response, absentId.kind);
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":false,\"error\":null}"));
}

test "a null slice is absent, which is not the same as an empty one" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"select\",\"title\":\"t\",\"options\":null}"));
    const empty = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"select\",\"title\":\"t\",\"options\":[]}");
    try std.testing.expectEqual(Kind.extension_ui_request, empty.kind);
}

test "a member whose value is the wrong type is refused" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"confirm\",\"title\":\"t\",\"message\":\"m\",\"timeout\":\"soon\"}"));
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"setWidget\",\"widgetKey\":\"k\",\"widgetLines\":[1]}"));
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"response\",\"command\":\"get_state\",\"success\":\"yes\"}"));
    const timed = try classify(std.testing.allocator, "{\"type\":\"extension_ui_request\",\"id\":\"u1\",\"method\":\"confirm\",\"title\":\"t\",\"message\":\"m\",\"timeout\":30}");
    try std.testing.expectEqual(Kind.extension_ui_request, timed.kind);
}

test "a duplicate key is refused at every nesting level, not only the top" {
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"turn_end\",\"message\":{\"a\":1,\"a\":2},\"toolResults\":[]}"));
    try std.testing.expectError(Error.InvalidFrame, classify(std.testing.allocator, "{\"type\":\"turn_end\",\"message\":{},\"toolResults\":[{\"b\":1,\"b\":2}]}"));
}
