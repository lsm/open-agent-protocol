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

const MemberType = enum { text, flag, number, text_list, image_list, any };

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
                if (entry != .string and entry != .null) break :blk false;
            }
            break :blk true;
        },
        .image_list => blk: {
            if (value != .array) break :blk false;
            for (value.array.items) |entry| {
                if (entry == .null) continue;
                if (entry != .object) break :blk false;
                var fields = entry.object.iterator();
                while (fields.next()) |field| {
                    if (!foldedIn(&image_members, field.key_ptr.*)) break :blk false;
                    const carried = field.value_ptr.*;
                    if (carried != .string and carried != .null) break :blk false;
                }
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

const image_members = [_][]const u8{ "type", "data", "mimeType" };

const command_members = [_]Member{
    .{ .name = "type", .kind = .text },
    .{ .name = "id", .kind = .text },
    .{ .name = "message", .kind = .text, .omits = .on_null },
    .{ .name = "images", .kind = .image_list },
    .{ .name = "streamingBehavior", .kind = .text },
    .{ .name = "parentSession", .kind = .text, .omits = .on_null },
    .{ .name = "provider", .kind = .text, .omits = .on_null },
    .{ .name = "modelId", .kind = .text, .omits = .on_null },
    .{ .name = "level", .kind = .text },
    .{ .name = "mode", .kind = .text },
    .{ .name = "customInstructions", .kind = .text, .omits = .on_null },
    .{ .name = "enabled", .kind = .flag, .omits = .on_null },
    .{ .name = "command", .kind = .text, .omits = .on_null },
    .{ .name = "excludeFromContext", .kind = .flag, .omits = .on_null },
    .{ .name = "outputPath", .kind = .text, .omits = .on_null },
    .{ .name = "sessionPath", .kind = .text, .omits = .on_null },
    .{ .name = "entryId", .kind = .text, .omits = .on_null },
    .{ .name = "since", .kind = .text, .omits = .on_null },
    .{ .name = "name", .kind = .text, .omits = .on_null },
};

const thinking_levels = [_][]const u8{ "off", "minimal", "low", "medium", "high", "xhigh", "max" };

const queue_modes = [_][]const u8{ "all", "one-at-a-time" };

const CommandShape = struct {
    name: []const u8,
    allowed: []const []const u8 = &.{},
    required: []const []const u8 = &.{},
    constrained: []const u8 = "",
    permitted: []const []const u8 = &.{},
};

const command_shapes = [_]CommandShape{
    .{ .name = "prompt", .allowed = &.{ "message", "images", "streamingBehavior" }, .required = &.{"message"} },
    .{ .name = "steer", .allowed = &.{ "message", "images" }, .required = &.{"message"} },
    .{ .name = "follow_up", .allowed = &.{ "message", "images" }, .required = &.{"message"} },
    .{ .name = "new_session", .allowed = &.{"parentSession"} },
    .{ .name = "set_model", .allowed = &.{ "provider", "modelId" }, .required = &.{ "provider", "modelId" } },
    .{ .name = "set_thinking_level", .allowed = &.{"level"}, .constrained = "level", .permitted = &thinking_levels },
    .{ .name = "set_steering_mode", .allowed = &.{"mode"}, .constrained = "mode", .permitted = &queue_modes },
    .{ .name = "set_follow_up_mode", .allowed = &.{"mode"}, .constrained = "mode", .permitted = &queue_modes },
    .{ .name = "compact", .allowed = &.{"customInstructions"} },
    .{ .name = "set_auto_compaction", .allowed = &.{"enabled"}, .required = &.{"enabled"} },
    .{ .name = "set_auto_retry", .allowed = &.{"enabled"}, .required = &.{"enabled"} },
    .{ .name = "bash", .allowed = &.{ "command", "excludeFromContext" }, .required = &.{"command"} },
    .{ .name = "export_html", .allowed = &.{"outputPath"} },
    .{ .name = "switch_session", .allowed = &.{"sessionPath"}, .required = &.{"sessionPath"} },
    .{ .name = "fork", .allowed = &.{"entryId"}, .required = &.{"entryId"} },
    .{ .name = "get_entries", .allowed = &.{"since"} },
    .{ .name = "set_session_name", .allowed = &.{"name"}, .required = &.{"name"} },
};

fn foldedMember(object: std.json.ObjectMap, name: []const u8) ?std.json.Value {
    if (object.get(name)) |value| return value;
    for (object.keys()) |key| {
        if (std.ascii.eqlIgnoreCase(key, name)) return object.get(key);
    }
    return null;
}

fn foldedIn(names: []const []const u8, name: []const u8) bool {
    for (names) |entry| {
        if (std.ascii.eqlIgnoreCase(entry, name)) return true;
    }
    return false;
}

fn foldedDeclared(table: []const Member, name: []const u8) ?Member {
    for (table) |entry| {
        if (std.ascii.eqlIgnoreCase(entry.name, name)) return entry;
    }
    return null;
}

fn commandShape(name: []const u8) CommandShape {
    for (command_shapes) |shape| {
        if (std.mem.eql(u8, shape.name, name)) return shape;
    }
    return .{ .name = name };
}

pub fn validateCommand(value: std.json.Value) !void {
    if (value != .object) return Error.InvalidFrame;
    const object = value.object;

    const declared = foldedMember(object, "type") orelse return Error.InvalidFrame;
    if (declared != .string or !namedIn(&commands, declared.string)) return Error.InvalidFrame;
    const shape = commandShape(declared.string);

    var it = object.iterator();
    while (it.next()) |entry| {
        const name = entry.key_ptr.*;
        const member = foldedDeclared(&command_members, name) orelse return Error.InvalidFrame;
        if (!memberTypeHolds(entry.value_ptr.*, member.kind)) return Error.InvalidFrame;
        if (std.ascii.eqlIgnoreCase(name, "type") or std.ascii.eqlIgnoreCase(name, "id")) continue;
        if (foldedIn(shape.allowed, name)) continue;
        if (omittedByMarshal(entry.value_ptr.*, member)) continue;
        return Error.InvalidFrame;
    }

    for (shape.required) |name| {
        const carried = foldedMember(object, name) orelse return Error.InvalidFrame;
        if (carried == .null) return Error.InvalidFrame;
    }

    if (shape.constrained.len != 0) {
        const carried = foldedMember(object, shape.constrained) orelse std.json.Value{ .null = {} };
        const text: []const u8 = if (carried == .string) carried.string else "";
        if (!namedIn(shape.permitted, text)) return Error.InvalidFrame;
    }
}

const canonical_order = [_][]const u8{
    "id",                "type",               "message",            "images",
    "streamingBehavior", "parentSession",      "provider",           "modelId",
    "level",             "mode",               "customInstructions", "enabled",
    "command",           "excludeFromContext", "outputPath",         "sessionPath",
    "entryId",           "since",              "name",
};

fn writeGoString(out: *std.ArrayList(u8), arena: std.mem.Allocator, text: []const u8) !void {
    try out.append(arena, '"');
    var at: usize = 0;
    while (at < text.len) {
        const byte = text[at];
        if (byte == 0xE2 and at + 3 <= text.len and text[at + 1] == 0x80 and (text[at + 2] == 0xA8 or text[at + 2] == 0xA9)) {
            try out.appendSlice(arena, if (text[at + 2] == 0xA8) "\\u2028" else "\\u2029");
            at += 3;
            continue;
        }
        at += 1;
        switch (byte) {
            '"' => try out.appendSlice(arena, "\\\""),
            '\\' => try out.appendSlice(arena, "\\\\"),
            '\n' => try out.appendSlice(arena, "\\n"),
            '\r' => try out.appendSlice(arena, "\\r"),
            '\t' => try out.appendSlice(arena, "\\t"),
            0x08 => try out.appendSlice(arena, "\\b"),
            0x0c => try out.appendSlice(arena, "\\f"),
            '<' => try out.appendSlice(arena, "\\u003c"),
            '>' => try out.appendSlice(arena, "\\u003e"),
            '&' => try out.appendSlice(arena, "\\u0026"),
            else => {
                if (byte < 0x20) {
                    try out.appendSlice(arena, try std.fmt.allocPrint(arena, "\\u{x:0>4}", .{byte}));
                } else {
                    try out.append(arena, byte);
                }
            },
        }
    }
    try out.append(arena, '"');
}

fn writeImages(out: *std.ArrayList(u8), arena: std.mem.Allocator, value: std.json.Value) !void {
    try out.append(arena, '[');
    for (value.array.items, 0..) |entry, at| {
        if (at != 0) try out.append(arena, ',');
        try out.append(arena, '{');
        for (image_members, 0..) |name, index| {
            if (index != 0) try out.append(arena, ',');
            try writeGoString(out, arena, name);
            try out.append(arena, ':');
            var text: []const u8 = "";
            if (entry == .object) {
                if (foldedMember(entry.object, name)) |held| {
                    if (held == .string) text = held.string;
                }
            }
            try writeGoString(out, arena, text);
        }
        try out.append(arena, '}');
    }
    try out.append(arena, ']');
}

pub fn canonicalCommand(arena: std.mem.Allocator, value: std.json.Value) ![]const u8 {
    try validateCommand(value);
    var out = std.ArrayList(u8).empty;
    try out.append(arena, '{');
    var written: usize = 0;
    for (canonical_order) |name| {
        const carried = foldedMember(value.object, name) orelse continue;
        const member = foldedDeclared(&command_members, name) orelse continue;
        const always = std.mem.eql(u8, name, "type");
        if (!always and omittedByMarshal(carried, member)) continue;
        if (written != 0) try out.append(arena, ',');
        written += 1;
        try writeGoString(&out, arena, name);
        try out.append(arena, ':');
        if (std.mem.eql(u8, name, "images")) {
            try writeImages(&out, arena, carried);
        } else if (member.kind == .flag) {
            try out.appendSlice(arena, if (carried == .bool and carried.bool) "true" else "false");
        } else {
            try writeGoString(&out, arena, if (carried == .string) carried.string else "");
        }
    }
    try out.append(arena, '}');
    return out.toOwnedSlice(arena);
}

fn expectCanonical(text: []const u8, want: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, text, .{});
    try std.testing.expectEqualStrings(want, try canonicalCommand(scratch, parsed));
}

test "a canonical command escapes a string the way json.Marshal does" {
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u003cb\"}", "{\"type\":\"prompt\",\"message\":\"a\\u003cb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u003eb\"}", "{\"type\":\"prompt\",\"message\":\"a\\u003eb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u0026b\"}", "{\"type\":\"prompt\",\"message\":\"a\\u0026b\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\\"b\"}", "{\"type\":\"prompt\",\"message\":\"a\\\"b\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\\\b\"}", "{\"type\":\"prompt\",\"message\":\"a\\\\b\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\nb\"}", "{\"type\":\"prompt\",\"message\":\"a\\nb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\rb\"}", "{\"type\":\"prompt\",\"message\":\"a\\rb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\tb\"}", "{\"type\":\"prompt\",\"message\":\"a\\tb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\bb\"}", "{\"type\":\"prompt\",\"message\":\"a\\bb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\fb\"}", "{\"type\":\"prompt\",\"message\":\"a\\fb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u000bb\"}", "{\"type\":\"prompt\",\"message\":\"a\\u000bb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u0000b\"}", "{\"type\":\"prompt\",\"message\":\"a\\u0000b\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u001fb\"}", "{\"type\":\"prompt\",\"message\":\"a\\u001fb\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u2028b\"}", "{\"type\":\"prompt\",\"message\":\"a\\u2028b\"}");
    try expectCanonical("{\"type\":\"prompt\",\"message\":\"a\\u2029b\"}", "{\"type\":\"prompt\",\"message\":\"a\\u2029b\"}");
}

test "a canonical command leaves every other code point as itself" {
    for ([_][]const u8{ "caf\u{e9}", "\u{65e5}\u{672c}", "emoji\u{1F600}", "a\u{a0}b", "a\u{200b}b", "a\u{feff}b", "a\u{7f}b", "a/b", "a'b" }) |text| {
        var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
        defer arena.deinit();
        const scratch = arena.allocator();
        const line = try std.fmt.allocPrint(scratch, "{{\"type\":\"prompt\",\"message\":\"{s}\"}}", .{text});
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, line, .{});
        try std.testing.expectEqualStrings(line, try canonicalCommand(scratch, parsed));
    }
}

test "a literal character the encoder would escape makes the frame noncanonical" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, "{\"type\":\"prompt\",\"message\":\"a<b\"}", .{});
    try std.testing.expectEqualStrings("{\"type\":\"prompt\",\"message\":\"a\\u003cb\"}", try canonicalCommand(scratch, parsed));
}

test "a canonical command carries its members in the order the struct declares" {
    try expectCanonical("{\"type\":\"steer\",\"message\":\"adjust\"}", "{\"type\":\"steer\",\"message\":\"adjust\"}");
    try expectCanonical("{\"message\":\"adjust\",\"type\":\"steer\"}", "{\"type\":\"steer\",\"message\":\"adjust\"}");
    try expectCanonical("{\"type\":\"abort\",\"id\":\"r1\"}", "{\"id\":\"r1\",\"type\":\"abort\"}");
    try expectCanonical(
        "{\"type\":\"prompt\",\"images\":[],\"message\":\"hi\",\"streamingBehavior\":\"steer\"}",
        "{\"type\":\"prompt\",\"message\":\"hi\",\"streamingBehavior\":\"steer\"}",
    );
}

test "a canonical command drops every member omitempty would not write" {
    try expectCanonical("{\"type\":\"abort\",\"message\":null}", "{\"type\":\"abort\"}");
    try expectCanonical("{\"type\":\"abort\",\"enabled\":null}", "{\"type\":\"abort\"}");
    try expectCanonical("{\"type\":\"abort\",\"images\":[]}", "{\"type\":\"abort\"}");
    try expectCanonical("{\"type\":\"abort\",\"images\":null}", "{\"type\":\"abort\"}");
    try expectCanonical("{\"type\":\"abort\",\"level\":\"\"}", "{\"type\":\"abort\"}");
    try expectCanonical("{\"type\":\"abort\",\"id\":\"\"}", "{\"type\":\"abort\"}");
    try expectCanonical("{\"type\":\"bash\",\"command\":\"ls\",\"excludeFromContext\":false}", "{\"type\":\"bash\",\"command\":\"ls\",\"excludeFromContext\":false}");
}

test "a canonical command image is three strings even where the frame wrote none" {
    try expectCanonical(
        "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":[{}]}",
        "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":[{\"type\":\"\",\"data\":\"\",\"mimeType\":\"\"}]}",
    );
    try expectCanonical(
        "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":[{\"mimeType\":\"image/png\",\"data\":\"d\",\"type\":\"image\"}]}",
        "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":[{\"type\":\"image\",\"data\":\"d\",\"mimeType\":\"image/png\"}]}",
    );
}

test "a command the vocabulary refuses has no canonical form to compare" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, "{\"type\":\"steer\"}", .{});
    try std.testing.expectError(Error.InvalidFrame, canonicalCommand(scratch, parsed));
}

fn expectCommand(text: []const u8, want: anyerror!void) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(), text, .{}) catch {
        return error.ProbeTextIsNotJson;
    };
    const got = validateCommand(parsed);
    if (want) |_| {
        try got;
    } else |expected| {
        try std.testing.expectError(expected, got);
    }
}

test "a command member is matched the way encoding/json matches its tag" {
    try expectCommand("{\"type\":\"prompt\",\"Message\":\"hi\"}", {});
    try expectCommand("{\"type\":\"prompt\",\"MESSAGE\":\"hi\"}", {});
    try expectCommand("{\"TYPE\":\"prompt\",\"message\":\"hi\"}", {});
    try expectCommand("{\"type\":\"set_thinking_level\",\"LEVEL\":\"high\"}", {});
    try expectCommand("{\"type\":\"bash\",\"COMMAND\":\"ls\"}", {});
    try expectCommand("{\"type\":\"abort\",\"Message\":\"hi\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"prompt\",\"Message\":7}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"prompt\",\"message_\":\"hi\"}", Error.InvalidFrame);
}

test "a folded command canonicalises to the spelling its tag declares" {
    try expectCanonical("{\"type\":\"prompt\",\"Message\":\"hi\"}", "{\"type\":\"prompt\",\"message\":\"hi\"}");
    try expectCanonical("{\"TYPE\":\"prompt\",\"message\":\"hi\"}", "{\"type\":\"prompt\",\"message\":\"hi\"}");
    try expectCanonical(
        "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":[{\"TYPE\":\"image\",\"Data\":\"d\",\"mimetype\":\"image/png\"}]}",
        "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":[{\"type\":\"image\",\"data\":\"d\",\"mimeType\":\"image/png\"}]}",
    );
}

test "a command names a type this pin declares, and is an object" {
    try expectCommand("{\"type\":\"abort\"}", {});
    try expectCommand("{\"type\":\"nonesuch\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":7}", Error.InvalidFrame);
    try expectCommand("{}", Error.InvalidFrame);
    try expectCommand("[]", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"bogus\":1}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"bogus\":\"x\"}", Error.InvalidFrame);
}

test "a command requires the members its own type names, and a null is not one" {
    try expectCommand("{\"type\":\"prompt\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"prompt\",\"message\":null}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"prompt\",\"message\":\"\"}", {});
    try expectCommand("{\"type\":\"prompt\",\"message\":\"hi\"}", {});
    try expectCommand("{\"type\":\"set_model\",\"provider\":\"p\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_model\",\"provider\":\"p\",\"modelId\":\"m\"}", {});
    try expectCommand("{\"type\":\"bash\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"bash\",\"command\":\"ls\",\"excludeFromContext\":true}", {});
    try expectCommand("{\"type\":\"switch_session\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"fork\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_session_name\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_auto_retry\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_auto_retry\",\"enabled\":null}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_auto_retry\",\"enabled\":false}", {});
}

test "a constrained command member is read as its zero value when absent" {
    try expectCommand("{\"type\":\"set_thinking_level\",\"level\":\"high\"}", {});
    try expectCommand("{\"type\":\"set_thinking_level\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_thinking_level\",\"level\":\"\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_thinking_level\",\"level\":\"nonesuch\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"set_steering_mode\",\"mode\":\"all\"}", {});
    try expectCommand("{\"type\":\"set_follow_up_mode\",\"mode\":\"one-at-a-time\"}", {});
    try expectCommand("{\"type\":\"set_steering_mode\"}", Error.InvalidFrame);
}

test "a member foreign to a command escapes the check only where omitempty would drop it" {
    try expectCommand("{\"type\":\"abort\",\"message\":\"x\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"message\":\"\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"message\":null}", {});
    try expectCommand("{\"type\":\"abort\",\"enabled\":false}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"enabled\":null}", {});
    try expectCommand("{\"type\":\"abort\",\"level\":\"high\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"level\":\"\"}", {});
    try expectCommand("{\"type\":\"abort\",\"streamingBehavior\":\"steer\"}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"streamingBehavior\":\"\"}", {});
    try expectCommand("{\"type\":\"abort\",\"images\":[]}", {});
    try expectCommand("{\"type\":\"abort\",\"images\":null}", {});
    try expectCommand("{\"type\":\"abort\",\"images\":[null]}", Error.InvalidFrame);
    try expectCommand("{\"type\":\"abort\",\"id\":\"r1\"}", {});
    try expectCommand("{\"type\":\"abort\",\"id\":\"\"}", {});
}

test "a command image is an object of three strings, or a null standing for one" {
    const prefix = "{\"type\":\"prompt\",\"message\":\"hi\",\"images\":";
    try expectCommand(prefix ++ "[{\"type\":\"image\",\"data\":\"d\",\"mimeType\":\"image/png\"}]}", {});
    try expectCommand(prefix ++ "[{}]}", {});
    try expectCommand(prefix ++ "[null]}", {});
    try expectCommand(prefix ++ "[{\"type\":null,\"data\":null,\"mimeType\":null}]}", {});
    try expectCommand(prefix ++ "[{\"type\":7}]}", Error.InvalidFrame);
    try expectCommand(prefix ++ "[{\"bogus\":1}]}", Error.InvalidFrame);
    try expectCommand(prefix ++ "[{\"bogus\":\"x\"}]}", Error.InvalidFrame);
    try expectCommand(prefix ++ "[{\"type\":\"image\",\"bogus\":\"x\"}]}", Error.InvalidFrame);
    try expectCommand(prefix ++ "[7]}", Error.InvalidFrame);
    try expectCommand(prefix ++ "[\"x\"]}", Error.InvalidFrame);
    try expectCommand(prefix ++ "{}}", Error.InvalidFrame);
}

test "a null entry in a decoded string slice is that slice's zero value, not a defect" {
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"select\",\"title\":\"t\",\"options\":[null]}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"select\",\"title\":\"t\",\"options\":[\"a\",null]}");
    try admitsExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"setWidget\",\"widgetKey\":\"k\",\"widgetLines\":[null]}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"select\",\"title\":\"t\",\"options\":[7]}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"select\",\"title\":\"t\",\"options\":[[]]}");
    try refusesExtension("{\"type\":\"extension_ui_request\",\"id\":\"1\",\"method\":\"select\",\"title\":\"t\",\"options\":null}");
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
