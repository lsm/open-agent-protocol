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
pub const frame_too_large_message = "claude rpc: frame exceeds configured limit";
pub const max_nesting_depth = 10000;

fn frameDetail(comptime detail: []const u8) []const u8 {
    return invalid_frame_prefix ++ ": " ++ detail;
}

fn unprintableRune(code: u21) bool {
    return code <= 0x9f or code == 0xa0 or code == 0xad;
}

fn quoteGo(arena: std.mem.Allocator, value: []const u8) []const u8 {
    var out = std.ArrayList(u8).empty;
    out.append(arena, '"') catch return value;
    var index: usize = 0;
    while (index < value.len) {
        const byte = value[index];
        if (byte < 0x80) {
            index += 1;
            switch (byte) {
                '"' => out.appendSlice(arena, "\\\"") catch return value,
                '\\' => out.appendSlice(arena, "\\\\") catch return value,
                '\n' => out.appendSlice(arena, "\\n") catch return value,
                '\r' => out.appendSlice(arena, "\\r") catch return value,
                '\t' => out.appendSlice(arena, "\\t") catch return value,
                0x07 => out.appendSlice(arena, "\\a") catch return value,
                0x08 => out.appendSlice(arena, "\\b") catch return value,
                0x0b => out.appendSlice(arena, "\\v") catch return value,
                0x0c => out.appendSlice(arena, "\\f") catch return value,
                0x00...0x06, 0x0e...0x1f, 0x7f => {
                    const hex = std.fmt.allocPrint(arena, "\\x{x:0>2}", .{byte}) catch return value;
                    out.appendSlice(arena, hex) catch return value;
                },
                else => out.append(arena, byte) catch return value,
            }
            continue;
        }
        const width = std.unicode.utf8ByteSequenceLength(byte) catch {
            index += 1;
            const hex = std.fmt.allocPrint(arena, "\\x{x:0>2}", .{byte}) catch return value;
            out.appendSlice(arena, hex) catch return value;
            continue;
        };
        const decoded = if (index + width <= value.len) std.unicode.utf8Decode(value[index .. index + width]) catch null else null;
        if (decoded) |code| {
            index += width;
            if (unprintableRune(code)) {
                const hex = std.fmt.allocPrint(arena, "\\u{x:0>4}", .{code}) catch return value;
                out.appendSlice(arena, hex) catch return value;
            } else out.appendSlice(arena, value[index - width .. index]) catch return value;
            continue;
        }
        index += 1;
        const hex = std.fmt.allocPrint(arena, "\\x{x:0>2}", .{byte}) catch return value;
        out.appendSlice(arena, hex) catch return value;
    }
    out.append(arena, '"') catch return value;
    return out.items;
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

    fn tooLarge(self: *Diagnostic) Error {
        self.message = frame_too_large_message;
        return Error.FrameTooLarge;
    }

    fn invalidNested(self: *Diagnostic, arena: std.mem.Allocator, comptime member_name: []const u8) Error {
        self.message = std.fmt.allocPrint(arena, "{s}: " ++ member_name ++ " must be one object: {s}: frame must be exactly one JSON object", .{ invalid_control_prefix, invalid_message_prefix }) catch invalid_control_prefix;
        return Error.InvalidControl;
    }

    fn duplicateKey(self: *Diagnostic, arena: std.mem.Allocator, key: []const u8) Error {
        self.message = std.fmt.allocPrint(arena, "{s}: duplicate object key {s}", .{ invalid_message_prefix, quoteGo(arena, key) }) catch invalid_message_prefix;
        return Error.InvalidMessage;
    }

    fn refuseFrame(self: *Diagnostic, message: []const u8) Error {
        self.message = message;
        return Error.InvalidMessage;
    }

    fn refuseQuoted(self: *Diagnostic, arena: std.mem.Allocator, kind: Error, comptime shape: []const u8, value: []const u8) Error {
        const prefix: []const u8 = if (kind == Error.InvalidControl) invalid_control_prefix else invalid_frame_prefix;
        self.message = std.fmt.allocPrint(arena, "{s}: " ++ shape, .{ prefix, quoteGo(arena, value) }) catch prefix;
        return kind;
    }
};

pub const Need = enum { present, text, array, number, integer, boolean, text_array, object, object_array };

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

    pub fn next(self: *FrameReader, diagnostic: ?*Diagnostic) !?[]const u8 {
        var discard = Diagnostic{};
        const report = diagnostic orelse &discard;
        if (self.cursor >= self.source.len) return null;
        const rest = self.source[self.cursor..];
        const newline = std.mem.indexOfScalar(u8, rest, '\n') orelse {
            self.cursor = self.source.len;
            if (rest.len > self.limit + 1) return report.tooLarge();
            return report.invalid("unterminated frame");
        };
        if (newline > self.limit) return report.tooLarge();
        const frame = rest[0..newline];
        self.cursor += newline + 1;
        if (std.mem.indexOfScalar(u8, frame, '\r') != null) return report.invalid("carriage return is not valid framing");
        if (frame.len == 0) return report.invalid("empty frame");
        if (!std.unicode.utf8ValidateSlice(frame)) return report.invalid("frame is not UTF-8");
        return frame;
    }
};

const Frame = struct {
    is_object: bool,
    expect_key: bool = false,
    seen: std.StringHashMapUnmanaged(void) = .empty,
};

fn hexEscapeAt(data: []const u8, index: usize) ?u21 {
    if (index + 6 > data.len) return null;
    if (data[index] != '\\' or data[index + 1] != 'u') return null;
    var code: u21 = 0;
    for (data[index + 2 .. index + 6]) |digit| {
        const nibble: u21 = switch (digit) {
            '0'...'9' => digit - '0',
            'a'...'f' => digit - 'a' + 10,
            'A'...'F' => digit - 'A' + 10,
            else => return null,
        };
        code = code * 16 + nibble;
    }
    return code;
}

fn isSurrogate(code: u21) bool {
    return code >= 0xd800 and code <= 0xdfff;
}

fn carriesSurrogateEscape(data: []const u8) bool {
    var index: usize = 0;
    while (index + 6 <= data.len) : (index += 1) {
        const code = hexEscapeAt(data, index) orelse continue;
        if (isSurrogate(code)) return true;
    }
    return false;
}

fn replaceLoneSurrogates(arena: std.mem.Allocator, data: []const u8) []const u8 {
    if (!carriesSurrogateEscape(data)) return data;
    var out = std.ArrayList(u8).empty;
    out.ensureTotalCapacity(arena, data.len) catch return data;
    var index: usize = 0;
    var in_string = false;
    while (index < data.len) {
        const byte = data[index];
        if (!in_string) {
            if (byte == '"') in_string = true;
            out.append(arena, byte) catch return data;
            index += 1;
            continue;
        }
        if (byte == '\\') {
            if (hexEscapeAt(data, index)) |code| {
                if (code >= 0xd800 and code <= 0xdbff) {
                    if (hexEscapeAt(data, index + 6)) |trailing| {
                        if (trailing >= 0xdc00 and trailing <= 0xdfff) {
                            out.appendSlice(arena, data[index .. index + 12]) catch return data;
                            index += 12;
                            continue;
                        }
                    }
                }
                if (isSurrogate(code)) {
                    out.appendSlice(arena, "\\ufffd") catch return data;
                    index += 6;
                    continue;
                }
                out.appendSlice(arena, data[index .. index + 6]) catch return data;
                index += 6;
                continue;
            }
            if (index + 2 > data.len) {
                out.append(arena, byte) catch return data;
                index += 1;
                continue;
            }
            out.appendSlice(arena, data[index .. index + 2]) catch return data;
            index += 2;
            continue;
        }
        if (byte == '"') in_string = false;
        out.append(arena, byte) catch return data;
        index += 1;
    }
    return out.items;
}

const Walk = union(enum) { ok, duplicate: []const u8, trailing, too_deep };

fn walkFrame(arena: std.mem.Allocator, data: []const u8) !Walk {
    var scanner = std.json.Scanner.initCompleteInput(arena, data);
    defer scanner.deinit();
    var stack = std.ArrayList(Frame).empty;
    var settled = false;
    while (true) {
        const token = scanner.nextAlloc(arena, .alloc_always) catch return if (settled) Walk.trailing else Walk.ok;
        if (settled) {
            if (token == .end_of_document) break;
            return .trailing;
        }
        var closed = false;
        switch (token) {
            .object_begin, .array_begin => {
                if (stack.items.len >= max_nesting_depth) return .too_deep;
                try stack.append(arena, .{ .is_object = token == .object_begin, .expect_key = token == .object_begin });
            },
            .object_end, .array_end => {
                _ = stack.pop();
                if (stack.items.len == 0) settled = true;
                closed = true;
            },
            .end_of_document => break,
            .allocated_string => |text| {
                const top = &stack.items[stack.items.len - 1];
                if (top.is_object and top.expect_key) {
                    if (top.seen.contains(text)) return Walk{ .duplicate = text };
                    try top.seen.put(arena, text, {});
                    top.expect_key = false;
                    continue;
                }
                closed = true;
            },
            else => closed = true,
        }
        if (!closed or stack.items.len == 0) continue;
        const top = &stack.items[stack.items.len - 1];
        if (top.is_object) top.expect_key = true;
    }
    return .ok;
}

fn foldEql(left: []const u8, right: []const u8) bool {
    if (left.len != right.len) return false;
    for (left, right) |a, b| {
        if (std.ascii.toLower(a) != std.ascii.toLower(b)) return false;
    }
    return true;
}

pub fn lookup(object: std.json.ObjectMap, key: []const u8) ?std.json.Value {
    return member(object, &.{key});
}

fn member(object: std.json.ObjectMap, path: []const []const u8) ?std.json.Value {
    var found: ?std.json.Value = null;
    var entries = object.iterator();
    while (entries.next()) |entry| {
        if (!foldEql(entry.key_ptr.*, path[0])) continue;
        if (path.len == 1) {
            found = entry.value_ptr.*;
            continue;
        }
        if (entry.value_ptr.* != .object) continue;
        if (member(entry.value_ptr.object, path[1..])) |nested| found = nested;
    }
    return found;
}

fn anyWrong(object: std.json.ObjectMap, path: []const []const u8, need: Declared) bool {
    var entries = object.iterator();
    while (entries.next()) |entry| {
        if (!foldEql(entry.key_ptr.*, path[0])) continue;
        if (path.len == 1) {
            if (wrongValue(entry.value_ptr.*, need)) return true;
            continue;
        }
        if (entry.value_ptr.* == .null) continue;
        if (entry.value_ptr.* != .object) return true;
        if (anyWrong(entry.value_ptr.object, path[1..], need)) return true;
    }
    return false;
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
    path: []const []const u8,
    need: Need,
    items: []const Declared = &.{},
};

const origin_declared = [_]Declared{
    .{ .path = &.{"kind"}, .need = .text },
    .{ .path = &.{"from"}, .need = .text },
};
const usage_declared = [_]Declared{
    .{ .path = &.{"input_tokens"}, .need = .integer },
    .{ .path = &.{"output_tokens"}, .need = .integer },
    .{ .path = &.{"cache_read_input_tokens"}, .need = .integer },
    .{ .path = &.{"cache_creation_input_tokens"}, .need = .integer },
};
const task_usage_declared = [_]Declared{
    .{ .path = &.{"total_tokens"}, .need = .integer },
    .{ .path = &.{"tool_uses"}, .need = .integer },
    .{ .path = &.{"duration_ms"}, .need = .integer },
};
const mcp_server_declared = [_]Declared{
    .{ .path = &.{"name"}, .need = .text },
    .{ .path = &.{"status"}, .need = .text },
};

const block_text_members = [_][]const u8{ "type", "text", "thinking", "id", "name", "tool_use_id" };

fn wrongContentBlock(object: std.json.ObjectMap) bool {
    const content = member(object, &.{ "message", "content" }) orelse return false;
    return wrongBlocks(content);
}

pub fn wrongBlocks(content: std.json.Value) bool {
    if (content != .array) return false;
    for (content.array.items) |item| {
        if (item == .null) continue;
        if (item != .object) return true;
        for (block_text_members) |key| {
            const value = lookup(item.object, key) orelse continue;
            if (value == .null) continue;
            if (value != .string) return true;
        }
        if (lookup(item.object, "is_error")) |flag| {
            if (flag != .null and flag != .bool) return true;
        }
    }
    return false;
}

const result_declared = [_]Declared{
    .{ .path = &.{"usage"}, .need = .object, .items = &usage_declared },
    .{ .path = &.{"origin"}, .need = .object, .items = &origin_declared },
    .{ .path = &.{"duration_ms"}, .need = .integer },
    .{ .path = &.{"duration_api_ms"}, .need = .integer },
    .{ .path = &.{"is_error"}, .need = .boolean },
    .{ .path = &.{"num_turns"}, .need = .integer },
    .{ .path = &.{"queued_turn_count"}, .need = .integer },
    .{ .path = &.{"api_error_status"}, .need = .integer },
    .{ .path = &.{"total_cost_usd"}, .need = .number },
    .{ .path = &.{"errors"}, .need = .text_array },
    .{ .path = &.{"user_message_uuid"}, .need = .text },
    .{ .path = &.{"user_message_uuids"}, .need = .text_array },
    .{ .path = &.{"terminal_reason"}, .need = .text },
    .{ .path = &.{"result"}, .need = .text },
    .{ .path = &.{"stop_reason"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
};
const stream_event_declared = [_]Declared{
    .{ .path = &.{"user_message_uuid"}, .need = .text },
    .{ .path = &.{"user_message_uuids"}, .need = .text_array },
    .{ .path = &.{"parent_tool_use_id"}, .need = .text },
};
const assistant_declared = [_]Declared{
    .{ .path = &.{ "message", "content" }, .need = .object_array },
    .{ .path = &.{ "message", "id" }, .need = .text },
    .{ .path = &.{ "message", "stop_reason" }, .need = .text },
    .{ .path = &.{"parent_tool_use_id"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
    .{ .path = &.{"session_id"}, .need = .text },
    .{ .path = &.{"user_message_uuid"}, .need = .text },
    .{ .path = &.{"user_message_uuids"}, .need = .text_array },
    .{ .path = &.{"is_api_error_message"}, .need = .boolean },
    .{ .path = &.{"error"}, .need = .text },
};
const task_updated_declared = [_]Declared{
    .{ .path = &.{"patch"}, .need = .object },
    .{ .path = &.{"uuid"}, .need = .text },
    .{ .path = &.{"session_id"}, .need = .text },
    .{ .path = &.{ "patch", "status" }, .need = .text },
    .{ .path = &.{ "patch", "description" }, .need = .text },
    .{ .path = &.{ "patch", "end_time" }, .need = .integer },
    .{ .path = &.{ "patch", "error" }, .need = .text },
    .{ .path = &.{ "patch", "is_backgrounded" }, .need = .boolean },
};

const user_declared = [_]Declared{
    .{ .path = &.{"origin"}, .need = .object, .items = &origin_declared },
    .{ .path = &.{"parent_tool_use_id"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
    .{ .path = &.{"session_id"}, .need = .text },
};

const init_declared = [_]Declared{
    .{ .path = &.{"tools"}, .need = .text_array },
    .{ .path = &.{"capabilities"}, .need = .text_array },
    .{ .path = &.{"mcp_servers"}, .need = .object_array, .items = &mcp_server_declared },
    .{ .path = &.{"permissionMode"}, .need = .text },
    .{ .path = &.{"claude_code_version"}, .need = .text },
    .{ .path = &.{"apiKeySource"}, .need = .text },
    .{ .path = &.{"cwd"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
};

const status_declared = [_]Declared{
    .{ .path = &.{"status"}, .need = .text },
    .{ .path = &.{"session_id"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
};

const session_state_declared = [_]Declared{
    .{ .path = &.{"session_id"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
};

const task_started_declared = [_]Declared{
    .{ .path = &.{"tool_use_id"}, .need = .text },
    .{ .path = &.{"task_type"}, .need = .text },
    .{ .path = &.{"subagent_type"}, .need = .text },
    .{ .path = &.{"is_backgrounded"}, .need = .boolean },
};

const task_progress_declared = [_]Declared{
    .{ .path = &.{"tool_use_id"}, .need = .text },
    .{ .path = &.{"usage"}, .need = .object, .items = &task_usage_declared },
};

const task_notification_declared = [_]Declared{
    .{ .path = &.{"tool_use_id"}, .need = .text },
    .{ .path = &.{"usage"}, .need = .object, .items = &task_usage_declared },
};

const can_use_tool_declared = [_]Declared{
    .{ .path = &.{ "request", "blocked_path" }, .need = .text },
    .{ .path = &.{ "request", "decision_reason" }, .need = .text },
    .{ .path = &.{ "request", "title" }, .need = .text },
    .{ .path = &.{ "request", "display_name" }, .need = .text },
    .{ .path = &.{ "request", "description" }, .need = .text },
    .{ .path = &.{ "request", "agent_id" }, .need = .text },
};

const notice_declared = [_]Declared{
    .{ .path = &.{"session_id"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
};

const tool_progress_declared = [_]Declared{
    .{ .path = &.{"parent_tool_use_id"}, .need = .text },
    .{ .path = &.{"elapsed_time_seconds"}, .need = .number },
    .{ .path = &.{"task_id"}, .need = .text },
    .{ .path = &.{"uuid"}, .need = .text },
};

const command_lifecycle_declared = [_]Declared{
    .{ .path = &.{"uuid"}, .need = .text },
};

fn declaredMembers(frame_type: []const u8, subtype: []const u8) []const Declared {
    if (std.mem.eql(u8, frame_type, "result")) return &result_declared;
    if (std.mem.eql(u8, frame_type, "stream_event")) return &stream_event_declared;
    if (std.mem.eql(u8, frame_type, "assistant")) return &assistant_declared;
    if (std.mem.eql(u8, frame_type, "user")) return &user_declared;
    if (std.mem.eql(u8, frame_type, "tool_progress")) return &tool_progress_declared;
    if (std.mem.eql(u8, frame_type, "command_lifecycle")) return &command_lifecycle_declared;
    if (!std.mem.eql(u8, frame_type, "system")) return &.{};
    if (std.mem.eql(u8, subtype, "init")) return &init_declared;
    if (std.mem.eql(u8, subtype, "status")) return &status_declared;
    if (std.mem.eql(u8, subtype, "session_state_changed")) return &session_state_declared;
    if (std.mem.eql(u8, subtype, "task_started")) return &task_started_declared;
    if (std.mem.eql(u8, subtype, "task_progress")) return &task_progress_declared;
    if (std.mem.eql(u8, subtype, "task_notification")) return &task_notification_declared;
    if (std.mem.eql(u8, subtype, "task_updated")) return &task_updated_declared;
    return &notice_declared;
}

fn wrongType(object: std.json.ObjectMap, declared: []const Declared) bool {
    for (declared) |need| {
        if (anyWrong(object, need.path, need)) return true;
    }
    return false;
}

fn wrongRequiredType(object: std.json.ObjectMap, required: []const Member) bool {
    for (required) |need| {
        const typed: Need = switch (need.need) {
            .text => .text,
            .array => .array,
            else => continue,
        };
        if (anyWrong(object, need.path, .{ .path = need.path, .need = typed })) return true;
    }
    return false;
}

fn wrongValue(value: std.json.Value, need: Declared) bool {
    if (value == .null) return false;
    const ok = switch (need.need) {
        .text => value == .string,
        .array => value == .array,
        .object => value == .object,
        .number => value == .integer or value == .float,
        .integer => value == .integer,
        .boolean => value == .bool,
        .text_array => blk: {
            if (value != .array) break :blk false;
            for (value.array.items) |item| {
                if (item == .null) continue;
                if (item != .string) break :blk false;
            }
            break :blk true;
        },
        .object_array => blk: {
            if (value != .array) break :blk false;
            for (value.array.items) |item| {
                if (item == .null) continue;
                if (item != .object) break :blk false;
                if (wrongType(item.object, need.items)) break :blk false;
            }
            break :blk true;
        },
        else => true,
    };
    if (!ok) return true;
    if (need.need == .object and wrongType(value.object, need.items)) return true;
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
    const scan = replaceLoneSurrogates(arena, data);
    switch (walkFrame(arena, scan) catch Walk.ok) {
        .duplicate => |key| return report.duplicateKey(arena, key),
        .trailing => return report.invalid("trailing JSON value"),
        .too_deep => return report.invalid("exceeded max depth"),
        .ok => {},
    }
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, scan, .{}) catch {
        return report.invalid("frame is not decodable JSON");
    };
    if (parsed != .object) return report.invalid("frame must be exactly one JSON object");
    const object = parsed.object;

    const type_value = object.get("type") orelse return report.invalid("type is required");
    if (type_value != .string or type_value.string.len == 0) return report.invalid("type must be a non-empty string");

    var message = Message{
        .kind = .observation,
        .type = type_value.string,
        .raw = data,
        .object = parsed,
    };
    if (object.get("subtype")) |subtype| {
        if (subtype != .string or subtype.string.len == 0) return report.invalid("subtype must be a non-empty string");
        message.subtype = subtype.string;
    }

    if (std.mem.eql(u8, message.type, type_control_request)) {
        message.kind = .control_request;
        const id = object.get("request_id") orelse return report.invalidControl("control_request requires a non-empty request_id");
        if (id != .string or id.string.len == 0) return report.invalidControl("control_request requires a non-empty request_id");
        message.request_id = id.string;
        const request = object.get("request") orelse return report.invalidControl("request is required");
        if (request != .object) return report.invalidNested(arena, "request");
        const subtype = request.object.get("subtype") orelse return report.invalidControl("control request requires a non-empty subtype");
        if (subtype != .string or subtype.string.len == 0) return report.invalidControl("control request requires a non-empty subtype");
        message.subtype = subtype.string;
        if (std.mem.eql(u8, message.subtype, "can_use_tool")) {
            if (unsatisfied(object, &can_use_tool_members)) |detail| return report.refuseFrame(detail);
            if (wrongType(object, &can_use_tool_declared) or wrongRequiredType(object, &can_use_tool_members)) {
                return report.refuseFrame(frameDetail("frame declares a member of the wrong type"));
            }
        }
        return message;
    }
    if (std.mem.eql(u8, message.type, type_control_response)) {
        message.kind = .control_response;
        const response = object.get("response") orelse return report.invalidControl("response is required");
        if (response != .object) return report.invalidNested(arena, "response");
        const state = response.object.get("subtype") orelse return report.invalidControl("control response requires a non-empty subtype");
        if (state != .string or state.string.len == 0) return report.invalidControl("control response requires a non-empty subtype");
        const id = response.object.get("request_id") orelse return report.invalidControl("control response requires a non-empty request_id");
        if (id != .string or id.string.len == 0) return report.invalidControl("control response requires a non-empty request_id");
        var envelope = ControlResponse{ .request_id = id.string };
        if (std.mem.eql(u8, state.string, "success")) {
            envelope.success = true;
            if (response.object.get("response")) |payload| {
                if (payload != .null) envelope.response = payload;
            }
        } else if (std.mem.eql(u8, state.string, "error")) {
            const detail = response.object.get("error") orelse return report.invalidControl("error control response requires a non-empty error");
            if (detail != .string or detail.string.len == 0) return report.invalidControl("error control response requires a non-empty error");
            envelope.err = detail.string;
        } else return report.refuseQuoted(arena, Error.InvalidControl, "control response subtype {s} is not success or error", state.string);
        message.response = envelope;
        return message;
    }
    if (std.mem.eql(u8, message.type, type_control_cancel)) {
        message.kind = .control_cancel;
        const id = object.get("request_id") orelse return report.invalidControl("control_cancel_request requires a non-empty request_id");
        if (id != .string or id.string.len == 0) return report.invalidControl("control_cancel_request requires a non-empty request_id");
        message.request_id = id.string;
        return message;
    }
    if (unsatisfied(object, observationMembers(message.type, message.subtype))) |detail| {
        return report.refuseFrame(detail);
    }
    if (std.mem.eql(u8, message.type, "system") and std.mem.eql(u8, message.subtype, "task_notification") and !taskStatusIsTerminal(object)) {
        const status = member(object, &.{"status"}) orelse std.json.Value{ .string = "" };
        const shown = if (status == .string) status.string else "";
        return report.refuseQuoted(arena, Error.InvalidMessage, "task_notification status {s} is not completed, failed, or stopped", shown);
    }
    if (wrongType(object, declaredMembers(message.type, message.subtype)) or
        wrongRequiredType(object, observationMembers(message.type, message.subtype)) or
        (std.mem.eql(u8, message.type, "assistant") and wrongContentBlock(object)))
    {
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
    try testing.expectEqualStrings("{\"type\":\"result\"}", (try reader.next(null)).?);
    try testing.expectEqual(@as(?[]const u8, null), try reader.next(null));

    var empty = FrameReader{ .source = "\n" };
    try testing.expectError(Error.InvalidMessage, empty.next(null));

    var carriage = FrameReader{ .source = "{\"type\":\"a\"}\r\n" };
    try testing.expectError(Error.InvalidMessage, carriage.next(null));

    var unterminated = FrameReader{ .source = "{\"type\":\"a\"}" };
    try testing.expectError(Error.InvalidMessage, unterminated.next(null));

    var invalid_utf8 = FrameReader{ .source = "{\"a\":\"\xff\"}\n" };
    try testing.expectError(Error.InvalidMessage, invalid_utf8.next(null));

    var eof = FrameReader{ .source = "" };
    try testing.expectEqual(@as(?[]const u8, null), try eof.next(null));
}

test "every framing refusal names itself the way the oracle names it" {
    var empty = FrameReader{ .source = "\n" };
    var empty_report = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, empty.next(&empty_report));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": empty frame", empty_report.message);

    var carriage = FrameReader{ .source = "{\"type\":\"a\"}\r\n" };
    var carriage_report = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, carriage.next(&carriage_report));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": carriage return is not valid framing", carriage_report.message);

    var not_utf8 = FrameReader{ .source = "{\"a\":\"\xff\"}\n" };
    var utf8_report = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, not_utf8.next(&utf8_report));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": frame is not UTF-8", utf8_report.message);

    var unterminated = FrameReader{ .source = "{\"type\":\"a\"}" };
    var unterminated_report = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, unterminated.next(&unterminated_report));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": unterminated frame", unterminated_report.message);

    var oversized = FrameReader{ .source = "{\"type\":\"result\"}\n", .limit = 4 };
    var oversized_report = Diagnostic{};
    try testing.expectError(Error.FrameTooLarge, oversized.next(&oversized_report));
    try testing.expectEqualStrings(frame_too_large_message, oversized_report.message);

    var unterminated_oversized = FrameReader{ .source = "{\"type\":\"result\"}", .limit = 4 };
    var unterminated_oversized_report = Diagnostic{};
    try testing.expectError(Error.FrameTooLarge, unterminated_oversized.next(&unterminated_oversized_report));
    try testing.expectEqualStrings(frame_too_large_message, unterminated_oversized_report.message);
}

test "a frame past the limit is refused rather than buffered" {
    var reader = FrameReader{ .source = "{\"type\":\"result\"}\n", .limit = 4 };
    try testing.expectError(Error.FrameTooLarge, reader.next(null));
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

test "a nested member of the wrong type is fatal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    try refuses(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\",\"content\":[]},\"origin\":\"human\"}");
    try accepts(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\",\"content\":[]},\"origin\":{\"kind\":\"human\"}}");
    try accepts(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\",\"content\":[]},\"origin\":null}");

    try accepts(&arena, "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\",\"content\":[7]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[\"text\"]}}");
    try accepts(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[{\"type\":\"text\"}]}}");

    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"task_updated\",\"session_id\":\"s\",\"task_id\":\"t\",\"patch\":7}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"task_updated\",\"session_id\":\"s\",\"task_id\":\"t\",\"patch\":{\"status\":\"killed\"}}");
}

test "a refusal quotes the value the oracle quotes" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var state = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_response\",\"response\":{\"subtype\":\"maybe\",\"request_id\":\"r\"}}", &state));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": control response subtype \"maybe\" is not success or error", state.message);

    var status = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"system\",\"subtype\":\"task_notification\",\"session_id\":\"s\",\"task_id\":\"t\",\"status\":\"running\",\"output_file\":\"/o\",\"summary\":\"d\",\"uuid\":\"u\"}", &status));
    try testing.expectEqualStrings(invalid_frame_prefix ++ ": task_notification status \"running\" is not completed, failed, or stopped", status.message);

    var missing = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"subtype\":\"init\"}", &missing));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": type is required", missing.message);

    var empty = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"\"}", &empty));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": type must be a non-empty string", empty.message);

    var cancel = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_cancel_request\"}", &cancel));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": control_cancel_request requires a non-empty request_id", cancel.message);
}

test "a quoted value is escaped the way %q escapes it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    try testing.expectEqualStrings("\"plain\"", quoteGo(scratch, "plain"));
    try testing.expectEqualStrings("\"we\\\"ird\"", quoteGo(scratch, "we\"ird"));
    try testing.expectEqualStrings("\"back\\\\slash\"", quoteGo(scratch, "back\\slash"));
    try testing.expectEqualStrings("\"a\\nb\"", quoteGo(scratch, "a\nb"));
    try testing.expectEqualStrings("\"a\\x00b\"", quoteGo(scratch, "a\x00b"));
    try testing.expectEqualStrings("\"\\a\\b\\v\\f\"", quoteGo(scratch, "\x07\x08\x0b\x0c"));
    try testing.expectEqualStrings("\"\\r\\t\\x1f\"", quoteGo(scratch, "\r\t\x1f"));
    try testing.expectEqualStrings("\"\\u0080\\u009f\"", quoteGo(scratch, "\u{80}\u{9f}"));
    try testing.expectEqualStrings("\"\\u00a0\\u00ad\"", quoteGo(scratch, "\u{a0}\u{ad}"));
    try testing.expectEqualStrings("\"\u{b0}\u{bf}\u{ab}\u{a9}\"", quoteGo(scratch, "\u{b0}\u{bf}\u{ab}\u{a9}"));
    try testing.expectEqualStrings("\"\\xc2A\"", quoteGo(scratch, "\xc2A"));
    try testing.expectEqualStrings("\"a\\xc2b\"", quoteGo(scratch, "a\xc2b"));
    try testing.expectEqualStrings("\"\\xff\"", quoteGo(scratch, "\xff"));
    try testing.expectEqualStrings("\"\\xc2\"", quoteGo(scratch, "\xc2"));
    try testing.expectEqualStrings("\"\\xe2\\x80\"", quoteGo(scratch, "\xe2\x80"));
    try testing.expectEqualStrings("\"\u{1f600}\"", quoteGo(scratch, "\u{1f600}"));
    try testing.expectEqualStrings("\"caf\u{e9} na\u{ef}ve\"", quoteGo(scratch, "caf\u{e9} na\u{ef}ve"));

    var quoted = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(scratch, "{\"type\":\"control_response\",\"response\":{\"subtype\":\"we\\\"ird\",\"request_id\":\"r\"}}", &quoted));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": control response subtype \"we\\\"ird\" is not success or error", quoted.message);
}

test "an oversized unterminated tail is too large, not malformed" {
    var short = FrameReader{ .source = "{\"type\":\"result\"}", .limit = 4 };
    try testing.expectError(Error.FrameTooLarge, short.next(null));

    var within = FrameReader{ .source = "{}", .limit = 64 };
    try testing.expectError(Error.InvalidMessage, within.next(null));

    var terminated = FrameReader{ .source = "{\"type\":\"result\"}\n", .limit = 4 };
    try testing.expectError(Error.FrameTooLarge, terminated.next(null));
}

test "nesting past the cap the oracle enforces is refused here too" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    const at_cap = try nestedFrame(scratch, max_nesting_depth - 1);
    var accepted = Diagnostic{};
    _ = try parseMessage(scratch, at_cap, &accepted);

    const past_cap = try nestedFrame(scratch, max_nesting_depth);
    var refused = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(scratch, past_cap, &refused));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": exceeded max depth", refused.message);

    const objects = try nestedObjectFrame(scratch, max_nesting_depth);
    var objects_refused = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(scratch, objects, &objects_refused));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": exceeded max depth", objects_refused.message);
}

fn nestedFrame(arena: std.mem.Allocator, depth: usize) ![]const u8 {
    var out = std.ArrayList(u8).empty;
    try out.appendSlice(arena, "{\"type\":\"a\",\"m\":");
    try out.appendNTimes(arena, '[', depth);
    try out.appendNTimes(arena, ']', depth);
    try out.appendSlice(arena, "}");
    return out.items;
}

fn nestedObjectFrame(arena: std.mem.Allocator, depth: usize) ![]const u8 {
    var out = std.ArrayList(u8).empty;
    try out.appendSlice(arena, "{\"type\":\"a\",\"m\":");
    for (0..depth) |_| try out.appendSlice(arena, "{\"k\":");
    try out.appendSlice(arena, "1");
    try out.appendNTimes(arena, '}', depth);
    try out.appendSlice(arena, "}");
    return out.items;
}

test "an escape is four hex digits and nothing JSON does not spell" {
    try testing.expectEqual(@as(?u21, 0xd800), hexEscapeAt("\\ud800", 0));
    try testing.expectEqual(@as(?u21, 0xd800), hexEscapeAt("\\uD800", 0));
    try testing.expectEqual(@as(?u21, 0x0041), hexEscapeAt("\\u0041", 0));

    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\U0041", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\u+d80", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\ud_80", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\u 800", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("\\ud80", 0));
    try testing.expectEqual(@as(?u21, null), hexEscapeAt("x\\ud800", 0));
}

test "an unpaired surrogate escape becomes the replacement character the oracle substitutes" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    const lone = try parseMessage(scratch, "{\"type\":\"\\ud800\"}", null);
    try testing.expectEqualStrings("\u{fffd}", lone.type);

    const pair = try parseMessage(scratch, "{\"type\":\"a\\ud83d\\ude00b\"}", null);
    try testing.expectEqualStrings("a\u{1f600}b", pair.type);

    const two = try parseMessage(scratch, "{\"type\":\"\\ud800\\ud800\"}", null);
    try testing.expectEqualStrings("\u{fffd}\u{fffd}", two.type);

    const low_then_high = try parseMessage(scratch, "{\"type\":\"\\udc00\\ud800\"}", null);
    try testing.expectEqualStrings("\u{fffd}\u{fffd}", low_then_high.type);

    const high_then_pair = try parseMessage(scratch, "{\"type\":\"\\ud83d\\ud83d\\ude00\"}", null);
    try testing.expectEqualStrings("\u{fffd}\u{1f600}", high_then_pair.type);

    const trailing_text = try parseMessage(scratch, "{\"type\":\"\\ud800a\"}", null);
    try testing.expectEqualStrings("\u{fffd}a", trailing_text.type);

    for ([_][]const u8{ "{\"type\":\"\\u+d80\"}", "{\"type\":\"\\ud_80\"}", "{\"type\":\"\\u 800\"}" }) |lax| {
        var report = Diagnostic{};
        try testing.expectError(Error.InvalidMessage, parseMessage(scratch, lax, &report));
        try testing.expectEqualStrings(invalid_message_prefix ++ ": frame is not decodable JSON", report.message);
    }

    const upper_hex = try parseMessage(scratch, "{\"type\":\"\\uD800\"}", null);
    try testing.expectEqualStrings("\u{fffd}", upper_hex.type);

    var upper = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(scratch, "{\"type\":\"\\Ud800\"}", &upper));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": frame is not decodable JSON", upper.message);

    const other_escapes = try parseMessage(scratch, "{\"type\":\"\\u0041\\u00e9\\ud800\"}", null);
    try testing.expectEqualStrings("A\u{e9}\u{fffd}", other_escapes.type);

    const escaped_backslash = try parseMessage(scratch, "{\"type\":\"\\\\ud800\"}", null);
    try testing.expectEqualStrings("\\ud800", escaped_backslash.type);

    const outside_string = try parseMessage(scratch, "{\"type\":\"a\",\"n\":1}", null);
    try testing.expectEqualStrings("a", outside_string.type);

    var duplicate = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(scratch, "{\"type\":\"\\ud800\",\"type\":\"b\"}", &duplicate));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": duplicate object key \"type\"", duplicate.message);
}

test "a duplicate key is classified as one, not as undecodable JSON" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var duplicate = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"a\",\"type\":\"b\"}", &duplicate));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": duplicate object key \"type\"", duplicate.message);

    var nested = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"a\",\"m\":{\"x\":1,\"x\":2}}", &nested));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": duplicate object key \"x\"", nested.message);

    var in_array = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"a\",\"m\":[{\"x\":1},{\"y\":2,\"y\":3}]}", &in_array));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": duplicate object key \"y\"", in_array.message);

    var quoted = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"a\",\"a\\tb\":1,\"a\\tb\":2}", &quoted));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": duplicate object key \"a\\tb\"", quoted.message);

    var trailing = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"a\"}{\"type\":\"b\"}", &trailing));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": trailing JSON value", trailing.message);

    var spaced = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":\"a\"} {\"type\":\"b\"}", &spaced));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": trailing JSON value", spaced.message);

    try accepts(&arena, "{\"type\":\"a\",\"m\":{\"x\":1},\"n\":[{\"y\":2}]}");

    var garbage = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"type\":}", &garbage));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": frame is not decodable JSON", garbage.message);
}

test "a nested control member that is not an object names the cause the oracle names" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var request = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_request\",\"request_id\":\"r\",\"request\":7}", &request));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": request must be one object: " ++ invalid_message_prefix ++ ": frame must be exactly one JSON object", request.message);

    var response = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_response\",\"response\":\"no\"}", &response));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": response must be one object: " ++ invalid_message_prefix ++ ": frame must be exactly one JSON object", response.message);

    var missing = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_request\",\"request_id\":\"r\"}", &missing));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": request is required", missing.message);
}

test "a null array item is skipped where the oracle unmarshals it as a no-op" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    try accepts(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[null]}}");
    try accepts(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[null,{\"type\":\"text\",\"text\":\"x\"}]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[7]}}");

    const result = "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\",";
    try accepts(&arena, result ++ "\"errors\":[null]}");
    try accepts(&arena, result ++ "\"user_message_uuids\":[null]}");
    try refuses(&arena, result ++ "\"errors\":[7]}");

    const init = "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"model\":\"m\",\"tools\":";
    try accepts(&arena, init ++ "[null]}");
    try accepts(&arena, init ++ "[],\"mcp_servers\":[null]}");
    try refuses(&arena, init ++ "[],\"mcp_servers\":[{\"name\":7}]}");
}

test "a frame member matches the way encoding/json matches a struct tag" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    try accepts(&arena, "{\"type\":\"result\",\"subtype\":\"success\",\"SESSION_ID\":\"s\"}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"init\",\"SESSION_ID\":\"s\",\"MODEL\":\"m\",\"TOOLS\":[]}");
    try accepts(&arena, "{\"type\":\"assistant\",\"MESSAGE\":{\"MODEL\":\"m\",\"CONTENT\":[]}}");

    const result = "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\",";
    try refuses(&arena, result ++ "\"IS_ERROR\":\"yes\"}");
    try accepts(&arena, result ++ "\"Is_Error\":true}");
    try refuses(&arena, result ++ "\"DURATION_MS\":0.5}");
    try refuses(&arena, result ++ "\"SESSION_ID\":7}");
    try refuses(&arena, result ++ "\"is_error\":true,\"IS_ERROR\":\"yes\"}");
    try refuses(&arena, result ++ "\"IS_ERROR\":\"yes\",\"is_error\":true}");
    try refuses(&arena, result ++ "\"uuid\":\"u\",\"UUID\":7}");
    try accepts(&arena, result ++ "\"is_error\":true,\"Is_Error\":false}");

    var both: std.json.ObjectMap = .empty;
    try both.put(arena.allocator(), "SESSION_ID", .{ .string = "first" });
    try both.put(arena.allocator(), "session_id", .{ .string = "last" });
    try testing.expectEqualStrings("last", lookup(both, "session_id").?.string);
    try testing.expectEqualStrings("last", lookup(both, "SESSION_ID").?.string);

    var reversed: std.json.ObjectMap = .empty;
    try reversed.put(arena.allocator(), "session_id", .{ .string = "first" });
    try reversed.put(arena.allocator(), "SESSION_ID", .{ .string = "last" });
    try testing.expectEqualStrings("last", lookup(reversed, "session_id").?.string);

    try refuses(&arena, result ++ "\"SESSION_ID\":\"\"}");
    try accepts(&arena, "{\"type\":\"result\",\"subtype\":\"success\",\"SESSION_ID\":\"\",\"session_id\":\"s\"}");
    try accepts(&arena, "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"\",\"SESSION_ID\":\"s\"}");

    const updated = "{\"type\":\"system\",\"subtype\":\"task_updated\",\"task_id\":\"t\",";
    try refuses(&arena, updated ++ "\"patch\":{\"status\":7},\"PATCH\":{}}");
    try refuses(&arena, updated ++ "\"patch\":{},\"PATCH\":{\"status\":7}}");
    try accepts(&arena, updated ++ "\"patch\":{\"status\":\"completed\"},\"PATCH\":{\"description\":\"d\"}}");
    try accepts(&arena, "{\"type\":\"assistant\",\"message\":{\"model\":\"m\"},\"MESSAGE\":{\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"message\":{\"model\":\"a\",\"content\":[]},\"MESSAGE\":{\"model\":\"\"}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":\"hello\",\"MESSAGE\":{\"model\":\"m\",\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":7,\"MESSAGE\":{\"model\":\"m\",\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":[],\"MESSAGE\":{\"model\":\"m\",\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[]},\"MESSAGE\":\"hello\"}");
    try accepts(&arena, "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":null,\"MESSAGE\":{\"model\":\"m\",\"content\":[]}}");
    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"task_updated\",\"task_id\":\"t\",\"patch\":\"x\"}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"task_updated\",\"task_id\":\"t\",\"patch\":null}");
    try accepts(&arena, "{\"type\":\"assistant\",\"MESSAGE\":{\"model\":\"\"},\"message\":{\"model\":\"a\",\"content\":[]}}");
    try accepts(&arena, "{\"type\":\"assistant\",\"message\":{\"model\":\"\",\"content\":[]},\"MESSAGE\":{\"model\":\"a\"}}");

    var envelope = Diagnostic{};
    try testing.expectError(Error.InvalidMessage, parseMessage(arena.allocator(), "{\"TYPE\":\"keep_alive\"}", &envelope));
    try testing.expectEqualStrings(invalid_message_prefix ++ ": type is required", envelope.message);

    var control = Diagnostic{};
    try testing.expectError(Error.InvalidControl, parseMessage(arena.allocator(), "{\"type\":\"control_request\",\"REQUEST_ID\":\"r\",\"request\":{\"subtype\":\"interrupt\"}}", &control));
    try testing.expectEqualStrings(invalid_control_prefix ++ ": control_request requires a non-empty request_id", control.message);
}

test "a can_use_tool request is typed past its three required members" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    const base = "{\"type\":\"control_request\",\"request_id\":\"r\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_name\":\"Bash\",\"tool_use_id\":\"t1\",\"input\":{}";
    try accepts(&arena, base ++ "}}");
    try refuses(&arena, base ++ ",\"title\":7}}");
    try refuses(&arena, base ++ ",\"blocked_path\":7}}");
    try refuses(&arena, base ++ ",\"decision_reason\":7}}");
    try refuses(&arena, base ++ ",\"display_name\":7}}");
    try refuses(&arena, base ++ ",\"description\":7}}");
    try refuses(&arena, base ++ ",\"agent_id\":7}}");
    try accepts(&arena, base ++ ",\"title\":null}}");
    try accepts(&arena, base ++ ",\"permission_suggestions\":7}}");
    try accepts(&arena, base ++ ",\"title\":\"ok\",\"agent_id\":\"a\"}}");
    try accepts(&arena, "{\"type\":\"control_request\",\"request_id\":\"r\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_use_id\":\"t1\",\"input\":{},\"TOOL_NAME\":\"Bash\"}}");
    try refuses(&arena, "{\"type\":\"control_request\",\"request_id\":\"r\",\"request\":{\"subtype\":\"can_use_tool\",\"tool_use_id\":\"t1\",\"input\":{},\"tool_name\":\"Bash\",\"TOOL_NAME\":7}}");
}

test "the typed pass covers every declared member of a typed frame" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    const assistant = "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[]";
    try refuses(&arena, assistant ++ ",\"id\":7}}");
    try refuses(&arena, assistant ++ ",\"stop_reason\":7}}");
    try accepts(&arena, assistant ++ ",\"id\":\"m1\",\"stop_reason\":null}}");

    const updated = "{\"type\":\"system\",\"subtype\":\"task_updated\",\"task_id\":\"t\",\"patch\":{";
    try refuses(&arena, updated ++ "\"description\":7}}");
    try refuses(&arena, updated ++ "\"end_time\":0.5}}");
    try refuses(&arena, updated ++ "\"error\":7}}");
    try refuses(&arena, updated ++ "\"is_backgrounded\":\"yes\"}}");
    try accepts(&arena, updated ++ "\"description\":\"d\",\"end_time\":7,\"error\":\"e\",\"is_backgrounded\":true}}");
    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"task_updated\",\"task_id\":\"t\",\"patch\":7}");

    const init = "{\"type\":\"system\",\"subtype\":\"init\",\"session_id\":\"s\",\"model\":\"m\",\"tools\":";
    try refuses(&arena, init ++ "[7]}");
    try refuses(&arena, init ++ "[],\"mcp_servers\":[{\"name\":7}]}");
    try refuses(&arena, init ++ "[],\"mcp_servers\":[7]}");
    try refuses(&arena, init ++ "[],\"permissionMode\":7}");
    try accepts(&arena, init ++ "[\"Bash\"],\"mcp_servers\":[{\"name\":\"n\",\"status\":\"ok\"}],\"permissionMode\":\"ask\"}");

    const result = "{\"type\":\"result\",\"subtype\":\"success\",\"session_id\":\"s\",";
    try refuses(&arena, result ++ "\"duration_ms\":0.5}");
    try refuses(&arena, result ++ "\"num_turns\":1.5}");
    try refuses(&arena, result ++ "\"usage\":{\"input_tokens\":0.5}}");
    try accepts(&arena, result ++ "\"duration_ms\":12,\"total_cost_usd\":0.5,\"usage\":{\"input_tokens\":3}}");

    const progress = "{\"type\":\"tool_progress\",\"tool_use_id\":\"t\",\"tool_name\":\"Bash\",\"session_id\":\"s\",";
    try refuses(&arena, progress ++ "\"elapsed_time_seconds\":\"3\"}");
    try accepts(&arena, progress ++ "\"elapsed_time_seconds\":3.5}");

    try refuses(&arena, "{\"type\":\"system\",\"subtype\":\"compact_boundary\",\"uuid\":7}");
    try accepts(&arena, "{\"type\":\"system\",\"subtype\":\"compact_boundary\",\"uuid\":\"u\"}");
}

test "the typed table covers the members the reducer gates on" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    try refuses(&arena, "{\"type\":\"stream_event\",\"session_id\":\"s\",\"event\":{},\"uuid\":\"e\",\"parent_tool_use_id\":7}");
    try accepts(&arena, "{\"type\":\"stream_event\",\"session_id\":\"s\",\"event\":{},\"uuid\":\"e\",\"parent_tool_use_id\":null}");
    try accepts(&arena, "{\"type\":\"stream_event\",\"session_id\":\"s\",\"event\":{},\"uuid\":\"e\",\"parent_tool_use_id\":\"t1\"}");

    const assistant = "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[]},";
    try refuses(&arena, assistant ++ "\"parent_tool_use_id\":7}");
    try refuses(&arena, assistant ++ "\"uuid\":7}");

    const user = "{\"type\":\"user\",\"session_id\":\"s\",\"message\":{\"role\":\"user\",\"content\":[]},";
    try refuses(&arena, user ++ "\"parent_tool_use_id\":7}");

    const result = "{\"type\":\"result\",\"session_id\":\"s\",\"subtype\":\"success\",";
    try refuses(&arena, result ++ "\"usage\":7}");
    try refuses(&arena, result ++ "\"origin\":7}");

    const updated = "{\"type\":\"system\",\"subtype\":\"task_updated\",\"session_id\":\"s\",\"task_id\":\"t\",";
    try refuses(&arena, updated ++ "\"patch\":{\"status\":7}}");
    try accepts(&arena, updated ++ "\"patch\":{\"status\":\"killed\"}}");
}

test "an assistant content block is typed the way the oracle types it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const assistant = "{\"type\":\"assistant\",\"session_id\":\"s\",\"message\":{\"model\":\"m\",\"content\":[";

    try refuses(&arena, assistant ++ "{\"type\":7}]}}");
    try refuses(&arena, assistant ++ "{\"type\":\"tool_use\",\"id\":7,\"name\":\"Bash\"}]}}");
    try refuses(&arena, assistant ++ "{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":7}]}}");
    try refuses(&arena, assistant ++ "{\"type\":\"tool_result\",\"tool_use_id\":7}]}}");
    try refuses(&arena, assistant ++ "{\"type\":\"tool_result\",\"tool_use_id\":\"t1\",\"is_error\":\"yes\"}]}}");

    try accepts(&arena, assistant ++ "{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"Bash\",\"input\":{}}]}}");
    try accepts(&arena, assistant ++ "{\"type\":\"tool_use\",\"id\":\"t1\"}]}}");
    try accepts(&arena, assistant ++ "{\"type\":\"tool_result\",\"tool_use_id\":\"t1\",\"is_error\":true}]}}");
    try accepts(&arena, assistant ++ "{\"type\":\"text\",\"text\":\"hi\"}]}}");
}
