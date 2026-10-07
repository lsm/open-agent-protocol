const std = @import("std");
const harness_pins = @import("harness_pins");
const gojson = @import("gojson");
const goquote = @import("goquote");
const gomarshal = @import("gomarshal");

pub const pinned_tag = harness_pins.opencode_endpoint_version;

pub const Error = error{ InvalidWire, UnsupportedType } || std.mem.Allocator.Error;

pub const Diagnostic = struct { message: []const u8 = "", session_id: []const u8 = "" };

pub const invalid_wire = "opencode native: invalid wire payload";

pub const EventType = enum {
    inbox_enqueued,
    inbox_delivered,
    inbox_cancelled,
    inbox_delivery_changed,
    execution_started,
    execution_succeeded,
    execution_failed,
    execution_interrupted,
    step_started,
    step_streamed,
    step_ended,
    step_failed,
    text_started,
    text_delta,
    text_ended,
    reasoning_started,
    reasoning_delta,
    reasoning_ended,
    tool_input_started,
    tool_input_delta,
    tool_input_ended,
    tool_called,
    tool_progress,
    tool_success,
    tool_failed,
    created,
    agent_selected,
    model_selected,
    moved,
    renamed,
    metadata_updated,
    permissions,
    viewed,
    usage_updated,
    deleted,
    forked,
    instructions_updated,
    synthetic,
    skill_activated,
    shell_started,
    shell_ended,
    retry_scheduled,
    compaction_started,
    compaction_delta,
    compaction_ended,
    compaction_failed,
    revert_staged,
    revert_cleared,
    revert_committed,

    const names = [_][]const u8{
        "session.inbox.enqueued",         "session.inbox.delivered",       "session.inbox.cancelled",
        "session.inbox.delivery.changed", "session.execution.started",     "session.execution.succeeded",
        "session.execution.failed",       "session.execution.interrupted", "session.step.started",
        "session.step.streamed",          "session.step.ended",            "session.step.failed",
        "session.text.started",           "session.text.delta",            "session.text.ended",
        "session.reasoning.started",      "session.reasoning.delta",       "session.reasoning.ended",
        "session.tool.input.started",     "session.tool.input.delta",      "session.tool.input.ended",
        "session.tool.called",            "session.tool.progress",         "session.tool.success",
        "session.tool.failed",            "session.created",               "session.agent.selected",
        "session.model.selected",         "session.moved",                 "session.renamed",
        "session.metadata.updated",       "session.permissions",           "session.viewed",
        "session.usage.updated",          "session.deleted",               "session.forked",
        "session.instructions.updated",   "session.synthetic",             "session.skill.activated",
        "session.shell.started",          "session.shell.ended",           "session.retry.scheduled",
        "session.compaction.started",     "session.compaction.delta",      "session.compaction.ended",
        "session.compaction.failed",      "session.revert.staged",         "session.revert.cleared",
        "session.revert.committed",
    };

    pub fn wire(self: EventType) []const u8 {
        return names[@intFromEnum(self)];
    }

    pub fn parse(text: []const u8) ?EventType {
        for (names, 0..) |name, index| {
            if (std.mem.eql(u8, name, text)) return @enumFromInt(index);
        }
        return null;
    }

    pub fn durable(self: EventType) bool {
        return switch (self) {
            .text_delta, .reasoning_delta, .tool_input_delta, .tool_progress, .compaction_delta, .usage_updated => false,
            else => true,
        };
    }
};

pub const server_connected = "server.connected";

pub fn sessionScoped(type_name: []const u8) bool {
    return std.mem.startsWith(u8, type_name, "session.");
}

fn patterned(text: []const u8, prefix: []const u8) bool {
    if (!std.mem.startsWith(u8, text, prefix) or text.len == prefix.len) return false;
    for (text[prefix.len..]) |byte| {
        switch (byte) {
            'A'...'Z', 'a'...'z', '0'...'9', '_', '-' => {},
            else => return false,
        }
    }
    return true;
}

pub fn validSessionID(text: []const u8) bool {
    return patterned(text, "ses");
}

pub fn validMessageID(text: []const u8) bool {
    return patterned(text, "msg_");
}

pub fn validEventID(text: []const u8) bool {
    return patterned(text, "evt_");
}

pub fn validDelivery(text: []const u8) bool {
    return std.mem.eql(u8, text, "steer") or std.mem.eql(u8, text, "queue");
}

pub const Durable = struct { aggregate_id: []const u8, seq: i64, version: i64 };

pub const Event = struct {
    id: []const u8,
    type_name: []const u8,
    kind: ?EventType = null,
    session_id: []const u8 = "",
    durable: ?Durable = null,
    data: []const u8,
};

pub const ModelRef = struct { id: []const u8 = "", provider_id: []const u8 = "", variant: []const u8 = "" };

pub const PromptRequest = struct {
    id: []const u8 = "",
    text: []const u8 = "",
    delivery: []const u8 = "",
};

pub const Admitted = struct {
    id: []const u8 = "",
    session_id: []const u8 = "",
    kind: []const u8 = "",
    delivery: []const u8 = "",
    time_created: i64 = 0,
};

pub const SessionInfo = struct {
    id: []const u8 = "",
    project_id: []const u8 = "",
    agent: []const u8 = "",
    model: ?ModelRef = null,
    created: i64 = 0,
    updated: i64 = 0,
    directory: []const u8 = "",
    title: []const u8 = "",
};

pub const ErrorBlock = struct { kind: []const u8 = "", message: []const u8 = "" };

pub const InboxData = struct { inbox_id: []const u8 = "" };

pub const ExecutionFailedData = struct { failure: ErrorBlock = .{} };

pub const ExecutionInterruptedData = struct { reason: []const u8 = "" };

pub const StepStartedData = struct { model: ModelRef = .{} };

pub const StepEndedData = struct {
    finish: []const u8 = "",
    cost: f64 = 0,
    input_tokens: f64 = 0,
    output_tokens: f64 = 0,
};

pub const StepFailedData = struct { failure: ErrorBlock = .{}, cost: f64 = 0 };

pub const TextData = struct { text: []const u8 = "" };

pub const ToolInputData = struct { id: []const u8 = "", name: []const u8 = "" };

pub const Content = struct { kind: []const u8 = "", text: []const u8 = "", uri: []const u8 = "", mime: []const u8 = "", name: []const u8 = "" };

pub const ToolCalledData = struct {
    id: []const u8 = "",
    input: std.json.Value = .null,
};

pub const ToolProgressData = struct {
    id: []const u8 = "",
    metadata: std.json.Value = .null,
};

pub const ToolContentData = struct {
    id: []const u8 = "",
    content: ?[]const Content = null,
};

pub const ToolFailedData = struct {
    id: []const u8 = "",
    failure: ErrorBlock = .{},
};

pub const ApiError = struct {
    status: u16,
    tag: []const u8 = "",

    pub fn message(self: ApiError, arena: std.mem.Allocator) std.mem.Allocator.Error![]const u8 {
        if (self.tag.len == 0) return std.fmt.allocPrint(arena, "opencode native: HTTP {d}", .{self.status});
        return std.fmt.allocPrint(arena, "opencode native: HTTP {d} {s}", .{ self.status, self.tag });
    }
};

pub const Kind = union(enum) {
    string,
    integer,
    float,
    boolean,
    raw,
    any_object,
    strings,
    raws,
    record: []const Field,
    optional_record: []const Field,
    record_map: []const Field,
    record_list: []const Field,
    contents,
};

pub const Field = struct { name: []const u8, kind: Kind };

const model_fields = [_]Field{
    .{ .name = "id", .kind = .string },
    .{ .name = "providerID", .kind = .string },
    .{ .name = "variant", .kind = .string },
};
const durable_fields = [_]Field{
    .{ .name = "aggregateID", .kind = .string },
    .{ .name = "seq", .kind = .integer },
    .{ .name = "version", .kind = .integer },
};
const envelope_fields = [_]Field{
    .{ .name = "id", .kind = .string },
    .{ .name = "created", .kind = .integer },
    .{ .name = "type", .kind = .string },
    .{ .name = "location", .kind = .raw },
    .{ .name = "metadata", .kind = .raw },
    .{ .name = "durable", .kind = .{ .optional_record = &durable_fields } },
    .{ .name = "data", .kind = .raw },
};
const cache_fields = [_]Field{
    .{ .name = "read", .kind = .float },
    .{ .name = "write", .kind = .float },
};
const token_fields = [_]Field{
    .{ .name = "input", .kind = .float },
    .{ .name = "output", .kind = .float },
    .{ .name = "reasoning", .kind = .float },
    .{ .name = "cache", .kind = .{ .record = &cache_fields } },
};
const response_fields = [_]Field{
    .{ .name = "body", .kind = .string },
};
const error_fields = [_]Field{
    .{ .name = "type", .kind = .string },
    .{ .name = "message", .kind = .string },
    .{ .name = "status", .kind = .integer },
    .{ .name = "response", .kind = .{ .optional_record = &response_fields } },
};
const inbox_item_fields = [_]Field{
    .{ .name = "type", .kind = .string },
    .{ .name = "payload", .kind = .raw },
    .{ .name = "delivery", .kind = .string },
};
const inbox_enqueued_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "inboxID", .kind = .string },
    .{ .name = "item", .kind = .{ .record = &inbox_item_fields } },
};
const inbox_ref_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "inboxID", .kind = .string },
};
const inbox_delivery_changed_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "inboxID", .kind = .string },
    .{ .name = "delivery", .kind = .string },
};
const execution_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
};
const execution_failed_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "error", .kind = .{ .record = &error_fields } },
};
const execution_interrupted_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "reason", .kind = .string },
};
const step_started_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "agent", .kind = .string },
    .{ .name = "model", .kind = .{ .record = &model_fields } },
    .{ .name = "snapshot", .kind = .string },
    .{ .name = "started", .kind = .integer },
};
const step_ended_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "finish", .kind = .string },
    .{ .name = "rawFinish", .kind = .string },
    .{ .name = "providerState", .kind = .raw },
    .{ .name = "cost", .kind = .float },
    .{ .name = "tokens", .kind = .{ .record = &token_fields } },
    .{ .name = "snapshot", .kind = .string },
    .{ .name = "files", .kind = .strings },
};
const step_failed_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "error", .kind = .{ .record = &error_fields } },
    .{ .name = "finish", .kind = .string },
    .{ .name = "rawFinish", .kind = .string },
    .{ .name = "providerState", .kind = .raw },
    .{ .name = "cost", .kind = .float },
    .{ .name = "tokens", .kind = .{ .optional_record = &token_fields } },
    .{ .name = "snapshot", .kind = .string },
    .{ .name = "files", .kind = .strings },
};
const part_ended_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "ordinal", .kind = .integer },
    .{ .name = "text", .kind = .string },
    .{ .name = "state", .kind = .raw },
};
const tool_input_started_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "id", .kind = .string },
    .{ .name = "name", .kind = .string },
};
const tool_called_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "id", .kind = .string },
    .{ .name = "input", .kind = .any_object },
    .{ .name = "executed", .kind = .boolean },
    .{ .name = "state", .kind = .raw },
};
const tool_progress_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "id", .kind = .string },
    .{ .name = "metadata", .kind = .raw },
};
const tool_success_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "id", .kind = .string },
    .{ .name = "content", .kind = .contents },
    .{ .name = "metadata", .kind = .raw },
    .{ .name = "executed", .kind = .boolean },
    .{ .name = "resultState", .kind = .raw },
};
const tool_failed_fields = [_]Field{
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "assistantMessageID", .kind = .string },
    .{ .name = "id", .kind = .string },
    .{ .name = "error", .kind = .{ .record = &error_fields } },
    .{ .name = "content", .kind = .contents },
    .{ .name = "metadata", .kind = .raw },
    .{ .name = "executed", .kind = .boolean },
    .{ .name = "resultState", .kind = .raw },
};
const content_fields = [_]Field{
    .{ .name = "type", .kind = .string },
    .{ .name = "text", .kind = .string },
    .{ .name = "uri", .kind = .string },
    .{ .name = "mime", .kind = .string },
    .{ .name = "name", .kind = .string },
};
const admitted_time_fields = [_]Field{
    .{ .name = "created", .kind = .integer },
};
const admitted_fields = [_]Field{
    .{ .name = "id", .kind = .string },
    .{ .name = "sessionID", .kind = .string },
    .{ .name = "time", .kind = .{ .record = &admitted_time_fields } },
    .{ .name = "type", .kind = .string },
    .{ .name = "payload", .kind = .raw },
    .{ .name = "delivery", .kind = .string },
};
const session_time_fields = [_]Field{
    .{ .name = "created", .kind = .integer },
    .{ .name = "updated", .kind = .integer },
    .{ .name = "idle", .kind = .integer },
    .{ .name = "viewed", .kind = .integer },
    .{ .name = "archived", .kind = .integer },
};
const session_info_fields = [_]Field{
    .{ .name = "id", .kind = .string },
    .{ .name = "parentID", .kind = .string },
    .{ .name = "fork", .kind = .raw },
    .{ .name = "projectID", .kind = .string },
    .{ .name = "outcome", .kind = .string },
    .{ .name = "agent", .kind = .string },
    .{ .name = "model", .kind = .{ .optional_record = &model_fields } },
    .{ .name = "cost", .kind = .float },
    .{ .name = "tokens", .kind = .{ .record = &token_fields } },
    .{ .name = "time", .kind = .{ .record = &session_time_fields } },
    .{ .name = "title", .kind = .string },
    .{ .name = "location", .kind = .raw },
    .{ .name = "subpath", .kind = .string },
    .{ .name = "revert", .kind = .raw },
    .{ .name = "metadata", .kind = .raw },
    .{ .name = "permissions", .kind = .raw },
};
const active_entry_fields = [_]Field{
    .{ .name = "type", .kind = .string },
};
const interrupted_fields = [_]Field{
    .{ .name = "interrupted", .kind = .boolean },
};
const info_fields = [_]Field{
    .{ .name = "version", .kind = .string },
    .{ .name = "pid", .kind = .integer },
    .{ .name = "urls", .kind = .strings },
    .{ .name = "paths", .kind = .raw },
    .{ .name = "capabilities", .kind = .raw },
};

pub const admitted_response = [_]Field{.{ .name = "data", .kind = .{ .record = &admitted_fields } }};
pub const session_info_response = [_]Field{.{ .name = "data", .kind = .{ .record = &session_info_fields } }};
const message_cursor_fields = [_]Field{
    .{ .name = "previous", .kind = .string },
    .{ .name = "next", .kind = .string },
};
pub const messages_response = [_]Field{ .{ .name = "data", .kind = .raws }, .{ .name = "cursor", .kind = .{ .record = &message_cursor_fields } } };
pub const sessions_response = [_]Field{ .{ .name = "data", .kind = .{ .record_list = &session_info_fields } }, .{ .name = "cursor", .kind = .raw } };
pub const active_response = [_]Field{.{ .name = "data", .kind = .{ .record_map = &active_entry_fields } }};
pub const interrupt_response = interrupted_fields;
pub const info_response = info_fields;

fn replacementNeeded(data: []const u8) bool {
    var index: usize = 0;
    var in_string = false;
    while (index < data.len) {
        const byte = data[index];
        if (byte >= 0x80) {
            const width = sequenceWidth(data[index..]);
            if (width == 0) return true;
            index += width;
            continue;
        }
        if (!in_string) {
            if (byte == '"') in_string = true;
            index += 1;
            continue;
        }
        if (byte == '"') {
            in_string = false;
            index += 1;
            continue;
        }
        if (byte != '\\') {
            index += 1;
            continue;
        }
        const code = gojson.hexEscapeAt(data, index) orelse {
            index += 2;
            continue;
        };
        if (code >= 0xd800 and code <= 0xdbff) {
            if (gojson.hexEscapeAt(data, index + 6)) |trailing| {
                if (trailing >= 0xdc00 and trailing <= 0xdfff) {
                    index += 12;
                    continue;
                }
            }
        }
        if (gojson.isSurrogate(code)) return true;
        index += 6;
    }
    return false;
}

fn sequenceWidth(bytes: []const u8) usize {
    const width = std.unicode.utf8ByteSequenceLength(bytes[0]) catch return 0;
    if (width > bytes.len) return 0;
    _ = std.unicode.utf8Decode(bytes[0..width]) catch return 0;
    return width;
}

pub fn replaceInvalid(arena: std.mem.Allocator, data: []const u8) std.mem.Allocator.Error![]const u8 {
    if (!replacementNeeded(data)) return data;
    var out = try std.ArrayList(u8).initCapacity(arena, data.len + 8);
    var index: usize = 0;
    var in_string = false;
    while (index < data.len) {
        const byte = data[index];
        if (byte >= 0x80) {
            const width = sequenceWidth(data[index..]);
            if (width == 0) {
                try out.appendSlice(arena, "\u{fffd}");
                index += 1;
                continue;
            }
            try out.appendSlice(arena, data[index .. index + width]);
            index += width;
            continue;
        }
        if (!in_string or byte != '\\') {
            if (byte == '"') in_string = !in_string;
            try out.append(arena, byte);
            index += 1;
            continue;
        }
        const code = gojson.hexEscapeAt(data, index) orelse {
            const end = @min(index + 2, data.len);
            try out.appendSlice(arena, data[index..end]);
            index = end;
            continue;
        };
        if (code >= 0xd800 and code <= 0xdbff) {
            if (gojson.hexEscapeAt(data, index + 6)) |trailing| {
                if (trailing >= 0xdc00 and trailing <= 0xdfff) {
                    try out.appendSlice(arena, data[index .. index + 12]);
                    index += 12;
                    continue;
                }
            }
        }
        try out.appendSlice(arena, if (gojson.isSurrogate(code)) "\\ufffd" else data[index .. index + 6]);
        index += 6;
    }
    return out.items;
}

const Walk = union(enum) { ok, empty, syntax, trailing, duplicate: []const u8 };

const WalkFrame = struct {
    is_object: bool,
    expect_key: bool,
    seen: std.StringHashMapUnmanaged(void) = .empty,
};

fn walk(arena: std.mem.Allocator, data: []const u8) std.mem.Allocator.Error!Walk {
    if (std.mem.trim(u8, data, " \t\r\n").len == 0) return .empty;
    var scanner = std.json.Scanner.initCompleteInput(arena, data);
    var stack = std.ArrayList(WalkFrame).empty;
    var settled = false;
    var started = false;
    while (true) {
        const token = scanner.nextAlloc(arena, .alloc_if_needed) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return if (settled) .trailing else .syntax,
        };
        if (token == .end_of_document) return if (started) .ok else .empty;
        if (settled) return .trailing;
        started = true;
        var completed = false;
        switch (token) {
            .object_begin, .array_begin => try stack.append(arena, .{ .is_object = token == .object_begin, .expect_key = token == .object_begin }),
            .object_end, .array_end => {
                _ = stack.pop();
                completed = true;
            },
            .string, .allocated_string => |text| {
                if (stack.items.len > 0) {
                    const top = &stack.items[stack.items.len - 1];
                    if (top.is_object and top.expect_key) {
                        if (top.seen.contains(text)) return .{ .duplicate = text };
                        try top.seen.put(arena, text, {});
                        top.expect_key = false;
                        continue;
                    }
                }
                completed = true;
            },
            else => completed = true,
        }
        if (!completed) continue;
        if (stack.items.len == 0) {
            settled = true;
            continue;
        }
        const top = &stack.items[stack.items.len - 1];
        if (top.is_object) top.expect_key = true;
    }
}

fn detail(arena: std.mem.Allocator, diag: *Diagnostic, comptime format: []const u8, args: anytype) Error {
    diag.message = try std.fmt.allocPrint(arena, format, args);
    return error.InvalidWire;
}

pub fn parseDocument(arena: std.mem.Allocator, data: []const u8, diag: *Diagnostic) Error!std.json.Value {
    if (!gojson.withinNestingLimit(data)) return detail(arena, diag, "exceeded max depth", .{});
    const clean = try replaceInvalid(arena, data);
    switch (try walk(arena, clean)) {
        .ok => {},
        .empty => return detail(arena, diag, "EOF", .{}),
        .syntax => return detail(arena, diag, "invalid JSON", .{}),
        .trailing => return detail(arena, diag, "trailing JSON value", .{}),
        .duplicate => |key| return detail(arena, diag, "duplicate object key {s}", .{goquote.quote(arena, key)}),
    }
    return std.json.parseFromSliceLeaky(std.json.Value, arena, clean, .{ .parse_numbers = false, .allocate = .alloc_always }) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return detail(arena, diag, "invalid JSON", .{}),
    };
}

fn findField(fields: []const Field, name: []const u8) ?Field {
    for (fields) |field| {
        if (std.mem.eql(u8, field.name, name)) return field;
    }
    for (fields) |field| {
        if (gojson.foldEql(field.name, name)) return field;
    }
    return null;
}

fn jsonKind(value: std.json.Value) []const u8 {
    return switch (value) {
        .null => "null",
        .bool => "bool",
        .integer, .float, .number_string => "number",
        .string => "string",
        .array => "array",
        .object => "object",
    };
}

fn mismatch(arena: std.mem.Allocator, diag: *Diagnostic, value: std.json.Value, name: []const u8) Error {
    return detail(arena, diag, "json: cannot unmarshal {s} into field {s}", .{ jsonKind(value), goquote.quote(arena, name) });
}

fn integerText(text: []const u8) ?i64 {
    for (text) |byte| {
        if (byte == '.' or byte == 'e' or byte == 'E') return null;
    }
    return std.fmt.parseInt(i64, text, 10) catch null;
}

fn finiteFloat(text: []const u8) ?f64 {
    const parsed = std.fmt.parseFloat(f64, text) catch return null;
    return if (std.math.isFinite(parsed)) parsed else null;
}

fn checkKind(arena: std.mem.Allocator, diag: *Diagnostic, value: std.json.Value, field: Field, strict: bool) Error!void {
    if (value == .null) return;
    switch (field.kind) {
        .raw => {},
        .string => if (value != .string) return mismatch(arena, diag, value, field.name),
        .boolean => if (value != .bool) return mismatch(arena, diag, value, field.name),
        .integer => {
            if (value != .number_string or integerText(value.number_string) == null) return mismatch(arena, diag, value, field.name);
        },
        .float => {
            if (value != .number_string or finiteFloat(value.number_string) == null) return mismatch(arena, diag, value, field.name);
        },
        .any_object => {
            if (value != .object or !gojson.finiteNumbers(value)) return mismatch(arena, diag, value, field.name);
        },
        .strings => {
            if (value != .array) return mismatch(arena, diag, value, field.name);
            for (value.array.items) |item| {
                if (item != .null and item != .string) return mismatch(arena, diag, item, field.name);
            }
        },
        .raws => if (value != .array) return mismatch(arena, diag, value, field.name),
        .record, .optional_record => |fields| try checkRecord(arena, diag, value, fields, field.name, strict),
        .record_map => |fields| {
            if (value != .object) return mismatch(arena, diag, value, field.name);
            for (value.object.values()) |entry| try checkRecord(arena, diag, entry, fields, field.name, strict);
        },
        .record_list => |fields| {
            if (value != .array) return mismatch(arena, diag, value, field.name);
            for (value.array.items) |item| try checkRecord(arena, diag, item, fields, field.name, strict);
        },
        .contents => {
            if (value != .array) return mismatch(arena, diag, value, field.name);
            for (value.array.items) |item| try checkRecord(arena, diag, item, &content_fields, field.name, false);
        },
    }
}

fn checkRecord(arena: std.mem.Allocator, diag: *Diagnostic, value: std.json.Value, fields: []const Field, name: []const u8, strict: bool) Error!void {
    switch (value) {
        .null => return,
        .object => |members| {
            var entries = members.iterator();
            while (entries.next()) |entry| {
                const field = findField(fields, entry.key_ptr.*) orelse {
                    if (!strict) continue;
                    return detail(arena, diag, "json: unknown field {s}", .{goquote.quote(arena, entry.key_ptr.*)});
                };
                try checkKind(arena, diag, entry.value_ptr.*, field, strict);
            }
        },
        else => return mismatch(arena, diag, value, name),
    }
}

pub fn decodeStrict(arena: std.mem.Allocator, data: []const u8, fields: []const Field, diag: *Diagnostic) Error!std.json.Value {
    const document = try parseDocument(arena, data, diag);
    try checkRecord(arena, diag, document, fields, "", true);
    return document;
}

fn memberAt(document: std.json.Value, path: []const []const u8) ?std.json.Value {
    if (document != .object) return null;
    return gojson.foldedSet(document.object, path);
}

fn textAt(document: std.json.Value, path: []const []const u8) []const u8 {
    const value = memberAt(document, path) orelse return "";
    return if (value == .string) value.string else "";
}

fn integerAt(document: std.json.Value, path: []const []const u8) i64 {
    const value = memberAt(document, path) orelse return 0;
    return if (value == .number_string) integerText(value.number_string) orelse 0 else 0;
}

fn floatAt(document: std.json.Value, path: []const []const u8) f64 {
    const value = memberAt(document, path) orelse return 0;
    return if (value == .number_string) finiteFloat(value.number_string) orelse 0 else 0;
}

fn flagAt(document: std.json.Value, path: []const []const u8) bool {
    const value = memberAt(document, path) orelse return false;
    return value == .bool and value.bool;
}

fn present(document: std.json.Value, name: []const u8) bool {
    if (document != .object) return false;
    const value = gojson.foldedLast(document.object, &.{name}) orelse return false;
    return value != .null;
}

const Member = struct { key: []const u8, source: []const u8 };

fn topMembers(arena: std.mem.Allocator, data: []const u8) std.mem.Allocator.Error![]const Member {
    var scanner = std.json.Scanner.initCompleteInput(arena, data);
    var found = std.ArrayList(Member).empty;
    const opened = scanner.next() catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return found.items,
    };
    if (opened != .object_begin) return found.items;
    while (true) {
        const token = scanner.nextAlloc(arena, .alloc_always) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return found.items,
        };
        const key = switch (token) {
            .allocated_string => |name| name,
            else => return found.items,
        };
        const start = scanner.cursor;
        scanner.skipValue() catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return found.items,
        };
        const span = data[start..scanner.cursor];
        const colon = std.mem.indexOfScalar(u8, span, ':') orelse return found.items;
        try found.append(arena, .{ .key = key, .source = std.mem.trim(u8, span[colon + 1 ..], " \t\r\n") });
    }
}

fn lastMemberSource(arena: std.mem.Allocator, data: []const u8, name: []const u8) std.mem.Allocator.Error![]const u8 {
    var chosen: []const u8 = "";
    for (try topMembers(arena, data)) |candidate| {
        if (gojson.foldEql(candidate.key, name)) chosen = candidate.source;
    }
    return chosen;
}

pub fn decodeEvent(arena: std.mem.Allocator, data: []const u8, diag: *Diagnostic) Error!Event {
    var inner = Diagnostic{};
    const document = decodeStrict(arena, data, &envelope_fields, &inner) catch |err| switch (err) {
        error.InvalidWire => return detail(arena, diag, invalid_wire ++ ": envelope: {s}", .{inner.message}),
        else => return err,
    };
    const id = textAt(document, &.{"id"});
    if (!validEventID(id)) return detail(arena, diag, invalid_wire ++ ": invalid event id", .{});
    const named = textAt(document, &.{"type"});
    const clean = try replaceInvalid(arena, data);
    const source = try arena.dupe(u8, try lastMemberSource(arena, clean, "data"));
    var event = Event{ .id = id, .type_name = named, .data = source };
    if (present(document, "durable")) event.durable = .{
        .aggregate_id = textAt(document, &.{ "durable", "aggregateID" }),
        .seq = integerAt(document, &.{ "durable", "seq" }),
        .version = integerAt(document, &.{ "durable", "version" }),
    };
    if (!sessionScoped(named)) return event;
    const scope = memberAt(document, &.{"data"}) orelse std.json.Value.null;
    const session_id = textAt(scope, &.{"sessionID"});
    if (!validSessionID(session_id)) return detail(arena, diag, invalid_wire ++ ": {s} without a session id", .{named});
    event.session_id = session_id;
    diag.session_id = session_id;
    const kind = EventType.parse(named) orelse {
        diag.message = try std.fmt.allocPrint(arena, "opencode native: unsupported event type {s}", .{goquote.quote(arena, named)});
        return error.UnsupportedType;
    };
    event.kind = kind;
    if (kind.durable()) {
        const position = event.durable orelse return detail(arena, diag, invalid_wire ++ ": {s} without its durable position", .{named});
        if (!std.mem.eql(u8, position.aggregate_id, session_id) or position.seq < 0) return detail(arena, diag, invalid_wire ++ ": {s} without its durable position", .{named});
    } else if (event.durable != null) return detail(arena, diag, invalid_wire ++ ": ephemeral {s} with a durable position", .{named});
    return event;
}

fn decodeData(arena: std.mem.Allocator, event: Event, fields: []const Field, diag: *Diagnostic) Error!std.json.Value {
    var inner = Diagnostic{};
    return decodeStrict(arena, event.data, fields, &inner) catch |err| switch (err) {
        error.InvalidWire => return detail(arena, diag, invalid_wire ++ ": {s} data: {s}", .{ event.type_name, inner.message }),
        else => return err,
    };
}

fn errorOf(document: std.json.Value) ErrorBlock {
    return .{ .kind = textAt(document, &.{ "error", "type" }), .message = textAt(document, &.{ "error", "message" }) };
}

pub fn decodeInboxEnqueued(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!InboxData {
    const document = try decodeData(arena, event, &inbox_enqueued_fields, diag);
    return .{ .inbox_id = textAt(document, &.{"inboxID"}) };
}

pub fn decodeInboxRef(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!InboxData {
    const document = try decodeData(arena, event, &inbox_ref_fields, diag);
    return .{ .inbox_id = textAt(document, &.{"inboxID"}) };
}

pub fn decodeInboxDeliveryChanged(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!InboxData {
    const document = try decodeData(arena, event, &inbox_delivery_changed_fields, diag);
    return .{ .inbox_id = textAt(document, &.{"inboxID"}) };
}

pub fn decodeExecution(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!void {
    _ = try decodeData(arena, event, &execution_fields, diag);
}

pub fn decodeExecutionFailed(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ExecutionFailedData {
    const document = try decodeData(arena, event, &execution_failed_fields, diag);
    return .{ .failure = errorOf(document) };
}

pub fn decodeExecutionInterrupted(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ExecutionInterruptedData {
    const document = try decodeData(arena, event, &execution_interrupted_fields, diag);
    return .{ .reason = textAt(document, &.{"reason"}) };
}

pub fn decodeStepStarted(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!StepStartedData {
    const document = try decodeData(arena, event, &step_started_fields, diag);
    return .{ .model = .{
        .id = textAt(document, &.{ "model", "id" }),
        .provider_id = textAt(document, &.{ "model", "providerID" }),
        .variant = textAt(document, &.{ "model", "variant" }),
    } };
}

pub fn decodeStepEnded(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!StepEndedData {
    const document = try decodeData(arena, event, &step_ended_fields, diag);
    return .{
        .finish = textAt(document, &.{"finish"}),
        .cost = floatAt(document, &.{"cost"}),
        .input_tokens = floatAt(document, &.{ "tokens", "input" }),
        .output_tokens = floatAt(document, &.{ "tokens", "output" }),
    };
}

pub fn decodeStepFailed(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!StepFailedData {
    const document = try decodeData(arena, event, &step_failed_fields, diag);
    return .{ .failure = errorOf(document), .cost = floatAt(document, &.{"cost"}) };
}

pub fn decodePartEnded(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!TextData {
    const document = try decodeData(arena, event, &part_ended_fields, diag);
    return .{ .text = textAt(document, &.{"text"}) };
}

pub fn decodeToolInputStarted(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ToolInputData {
    const document = try decodeData(arena, event, &tool_input_started_fields, diag);
    return .{ .id = textAt(document, &.{"id"}), .name = textAt(document, &.{"name"}) };
}

pub fn decodeToolCalled(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ToolCalledData {
    const document = try decodeData(arena, event, &tool_called_fields, diag);
    return .{ .id = textAt(document, &.{"id"}), .input = memberAt(document, &.{"input"}) orelse .null };
}

fn contentsOf(arena: std.mem.Allocator, document: std.json.Value) std.mem.Allocator.Error!?[]const Content {
    const value = memberAt(document, &.{"content"}) orelse return null;
    if (value != .array) return null;
    const items = try arena.alloc(Content, value.array.items.len);
    for (value.array.items, items) |item, *slot| {
        slot.* = .{
            .kind = textAt(item, &.{"type"}),
            .text = textAt(item, &.{"text"}),
            .uri = textAt(item, &.{"uri"}),
            .mime = textAt(item, &.{"mime"}),
            .name = textAt(item, &.{"name"}),
        };
    }
    return items;
}

pub fn decodeToolProgress(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ToolProgressData {
    const document = try decodeData(arena, event, &tool_progress_fields, diag);
    return .{ .id = textAt(document, &.{"id"}), .metadata = memberAt(document, &.{"metadata"}) orelse .null };
}

pub fn decodeToolSuccess(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ToolContentData {
    const document = try decodeData(arena, event, &tool_success_fields, diag);
    return .{ .id = textAt(document, &.{"id"}), .content = try contentsOf(arena, document) };
}

pub fn decodeToolFailed(arena: std.mem.Allocator, event: Event, diag: *Diagnostic) Error!ToolFailedData {
    const document = try decodeData(arena, event, &tool_failed_fields, diag);
    return .{ .id = textAt(document, &.{"id"}), .failure = errorOf(document) };
}

pub fn admittedOf(document: std.json.Value) Admitted {
    return .{
        .id = textAt(document, &.{ "data", "id" }),
        .session_id = textAt(document, &.{ "data", "sessionID" }),
        .kind = textAt(document, &.{ "data", "type" }),
        .delivery = textAt(document, &.{ "data", "delivery" }),
        .time_created = integerAt(document, &.{ "data", "time", "created" }),
    };
}

pub fn validAdmitted(admitted: Admitted) bool {
    return validMessageID(admitted.id) and validSessionID(admitted.session_id) and validDelivery(admitted.delivery) and std.mem.eql(u8, admitted.kind, "user") and admitted.time_created >= 0;
}

pub fn sessionInfoOf(document: std.json.Value) SessionInfo {
    return sessionInfoFrom(memberAt(document, &.{"data"}) orelse std.json.Value.null);
}

pub fn sessionsOf(arena: std.mem.Allocator, document: std.json.Value) std.mem.Allocator.Error![]const SessionInfo {
    const data = memberAt(document, &.{"data"}) orelse return &.{};
    if (data != .array) return &.{};
    const infos = try arena.alloc(SessionInfo, data.array.items.len);
    for (data.array.items, infos) |item, *info| info.* = sessionInfoFrom(item);
    return infos;
}

fn sessionInfoFrom(data: std.json.Value) SessionInfo {
    const model: ?ModelRef = if (present(data, "model")) .{
        .id = textAt(data, &.{ "model", "id" }),
        .provider_id = textAt(data, &.{ "model", "providerID" }),
        .variant = textAt(data, &.{ "model", "variant" }),
    } else null;
    return .{
        .id = textAt(data, &.{"id"}),
        .project_id = textAt(data, &.{"projectID"}),
        .agent = textAt(data, &.{"agent"}),
        .model = model,
        .created = integerAt(data, &.{ "time", "created" }),
        .updated = integerAt(data, &.{ "time", "updated" }),
        .directory = textAt(data, &.{ "location", "directory" }),
        .title = textAt(data, &.{"title"}),
    };
}

pub fn validSessionInfo(info: SessionInfo) bool {
    return validSessionID(info.id) and info.project_id.len > 0 and info.created >= 0 and info.updated >= 0;
}

pub fn runningSessions(arena: std.mem.Allocator, document: std.json.Value) std.mem.Allocator.Error![]const []const u8 {
    const data = memberAt(document, &.{"data"}) orelse return &.{};
    if (data != .object) return &.{};
    var running = std.ArrayList([]const u8).empty;
    var entries = data.object.iterator();
    while (entries.next()) |entry| {
        if (std.mem.eql(u8, textAt(entry.value_ptr.*, &.{"type"}), "running")) try running.append(arena, entry.key_ptr.*);
    }
    return running.items;
}

pub fn interruptedOf(document: std.json.Value) bool {
    return flagAt(document, &.{"interrupted"});
}

pub fn decodeApiError(arena: std.mem.Allocator, status: u16, body: []const u8) std.mem.Allocator.Error!ApiError {
    var found = ApiError{ .status = status };
    if (!gojson.withinNestingLimit(body)) return found;
    const clean = try replaceInvalid(arena, body);
    switch (try walk(arena, clean)) {
        .ok, .duplicate => {},
        .empty, .syntax, .trailing => return found,
    }
    var tag: []const u8 = "";
    for (try topMembers(arena, clean)) |candidate| {
        if (!gojson.foldEql(candidate.key, "_tag")) continue;
        const value = std.json.parseFromSliceLeaky(std.json.Value, arena, candidate.source, .{ .parse_numbers = false, .allocate = .alloc_always }) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => continue,
        };
        switch (value) {
            .string => |named| tag = named,
            .null => {},
            else => return found,
        }
    }
    found.tag = tag;
    return found;
}

pub fn appendModelRef(out: *std.ArrayList(u8), arena: std.mem.Allocator, model: ModelRef) std.mem.Allocator.Error!void {
    try out.appendSlice(arena, "{\"id\":");
    try gomarshal.appendString(out, arena, model.id);
    try out.appendSlice(arena, ",\"providerID\":");
    try gomarshal.appendString(out, arena, model.provider_id);
    if (model.variant.len > 0) {
        try out.appendSlice(arena, ",\"variant\":");
        try gomarshal.appendString(out, arena, model.variant);
    }
    try out.append(arena, '}');
}

pub fn marshalPromptRequest(arena: std.mem.Allocator, request: PromptRequest) std.mem.Allocator.Error![]const u8 {
    var out = std.ArrayList(u8).empty;
    try out.append(arena, '{');
    if (request.id.len > 0) {
        try out.appendSlice(arena, "\"id\":");
        try gomarshal.appendString(&out, arena, request.id);
        try out.append(arena, ',');
    }
    try out.appendSlice(arena, "\"text\":");
    try gomarshal.appendString(&out, arena, request.text);
    if (request.delivery.len > 0) {
        try out.appendSlice(arena, ",\"delivery\":");
        try gomarshal.appendString(&out, arena, request.delivery);
    }
    try out.append(arena, '}');
    return out.items;
}

const testing = std.testing;

fn expectRefused(data: []const u8, want: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    _ = decodeEvent(arena.allocator(), data, &diag) catch {
        try testing.expectEqualStrings(want, diag.message);
        return;
    };
    return error.TestExpectedRefusal;
}

const step_ended_frame =
    \\{"id":"evt_c004","created":4,"type":"session.step.ended","location":{"directory":"/w"},"durable":{"aggregateID":"ses_fake00000000000000","seq":4,"version":1},"data":{"sessionID":"ses_fake00000000000000","assistantMessageID":"msg_a1","finish":"stop","rawFinish":"stop","cost":0,"tokens":{"input":2,"output":5,"reasoning":0,"cache":{"read":0,"write":0}}}}
;

test "a durable frame decodes to its identity, session, position and the bytes of its data" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    const event = try decodeEvent(arena.allocator(), step_ended_frame, &diag);
    try testing.expectEqualStrings("evt_c004", event.id);
    try testing.expectEqual(@as(?EventType, .step_ended), event.kind);
    try testing.expectEqualStrings("ses_fake00000000000000", event.session_id);
    try testing.expectEqual(@as(i64, 4), event.durable.?.seq);
    try testing.expectEqual(@as(i64, 1), event.durable.?.version);
    try testing.expect(std.mem.startsWith(u8, event.data, "{\"sessionID\":"));
    const data = try decodeStepEnded(arena.allocator(), event, &diag);
    try testing.expectEqualStrings("stop", data.finish);
    try testing.expectEqual(@as(f64, 2), data.input_tokens);
    try testing.expectEqual(@as(f64, 5), data.output_tokens);
}

test "an event of no session decodes without one, and an ephemeral one carries no position" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    const connected = try decodeEvent(arena.allocator(), "{\"id\":\"evt_connected\",\"type\":\"server.connected\",\"data\":{}}", &diag);
    try testing.expectEqual(@as(?EventType, null), connected.kind);
    try testing.expectEqualStrings("", connected.session_id);
    const delta = try decodeEvent(arena.allocator(), "{\"id\":\"evt_d\",\"created\":1,\"type\":\"session.text.delta\",\"data\":{\"sessionID\":\"ses_a\",\"assistantMessageID\":\"msg_1\",\"ordinal\":0,\"delta\":\"h\"}}", &diag);
    try testing.expectEqual(@as(?EventType, .text_delta), delta.kind);
    try testing.expectEqual(@as(?Durable, null), delta.durable);
}

test "the envelope is decoded strictly, with the refusal the oracle states" {
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a"},"extra":1}
    , "opencode native: invalid wire payload: envelope: json: unknown field \"extra\"");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1,"x":0},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: envelope: json: unknown field \"x\"");
    try expectRefused(
        \\{"id":"evt_1","id":"evt_2","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: envelope: duplicate object key \"id\"");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"a":1,"a":2}}
    , "opencode native: invalid wire payload: envelope: duplicate object key \"a\"");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1.0,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: envelope: json: cannot unmarshal number into field \"seq\"");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a"}} {}
    , "opencode native: invalid wire payload: envelope: trailing JSON value");
    try expectRefused("", "opencode native: invalid wire payload: envelope: EOF");
}

test "an envelope that decodes is still refused for identity, session, type and durability" {
    try expectRefused(
        \\{"id":"evt_","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: invalid event id");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{}}
    , "opencode native: invalid wire payload: session.renamed without a session id");
    try expectRefused(
        \\{"id":"evt_1","type":"session.brand.new","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: unsupported event type \"session.brand.new\"");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":null,"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: session.renamed without its durable position");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_b","seq":1,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: session.renamed without its durable position");
    try expectRefused(
        \\{"id":"evt_1","type":"session.renamed","durable":{"aggregateID":"ses_a","seq":-1,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: session.renamed without its durable position");
    try expectRefused(
        \\{"id":"evt_1","type":"session.text.delta","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a"}}
    , "opencode native: invalid wire payload: ephemeral session.text.delta with a durable position");
    try expectRefused("null", "opencode native: invalid wire payload: invalid event id");
}

test "an unsupported event still names the session it belongs to" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    try testing.expectError(error.UnsupportedType, decodeEvent(arena.allocator(), "{\"id\":\"evt_1\",\"type\":\"session.brand.new\",\"data\":{\"sessionID\":\"ses_b\"}}", &diag));
    try testing.expectEqualStrings("ses_b", diag.session_id);
}

test "member names match the way encoding/json matches them, ignoring case" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    const event = try decodeEvent(arena.allocator(),
        \\{"ID":"evt_1","Type":"session.text.ended","DURABLE":{"aggregateid":"ses_a","Seq":7,"version":1},"Data":{"sessionID":"ses_a","TEXT":"hi","text":null}}
    , &diag);
    try testing.expectEqualStrings("evt_1", event.id);
    try testing.expectEqual(@as(i64, 7), event.durable.?.seq);
    const data = try decodePartEnded(arena.allocator(), event, &diag);
    try testing.expectEqualStrings("hi", data.text);
}

test "event data is decoded strictly when the reducer reads it, not when the frame arrives" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    const event = try decodeEvent(arena.allocator(),
        \\{"id":"evt_1","type":"session.step.ended","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a","finish":"stop","surprise":true}}
    , &diag);
    try testing.expectError(error.InvalidWire, decodeStepEnded(arena.allocator(), event, &diag));
    try testing.expectEqualStrings("opencode native: invalid wire payload: session.step.ended data: json: unknown field \"surprise\"", diag.message);

    const overflowing = try decodeEvent(arena.allocator(),
        \\{"id":"evt_2","type":"session.step.ended","durable":{"aggregateID":"ses_a","seq":2,"version":1},"data":{"sessionID":"ses_a","cost":1e400}}
    , &diag);
    try testing.expectError(error.InvalidWire, decodeStepEnded(arena.allocator(), overflowing, &diag));

    const unbounded_input = try decodeEvent(arena.allocator(),
        \\{"id":"evt_3","type":"session.tool.called","durable":{"aggregateID":"ses_a","seq":3,"version":1},"data":{"sessionID":"ses_a","id":"c","input":{"n":[1e400]},"executed":true}}
    , &diag);
    try testing.expectError(error.InvalidWire, decodeToolCalled(arena.allocator(), unbounded_input, &diag));
}

test "a tool content item may carry members the pinned type does not name" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    const event = try decodeEvent(arena.allocator(),
        \\{"id":"evt_1","type":"session.tool.success","durable":{"aggregateID":"ses_a","seq":1,"version":1},"data":{"sessionID":"ses_a","id":"c","content":[{"type":"file","uri":"u","mime":"m","extra":1},null],"executed":true}}
    , &diag);
    const data = try decodeToolSuccess(arena.allocator(), event, &diag);
    try testing.expectEqual(@as(usize, 2), data.content.?.len);
    try testing.expectEqualStrings("file", data.content.?[0].kind);
    try testing.expectEqualStrings("u", data.content.?[0].uri);
    try testing.expectEqualStrings("", data.content.?[1].kind);

    const typed = try decodeEvent(arena.allocator(),
        \\{"id":"evt_2","type":"session.tool.success","durable":{"aggregateID":"ses_a","seq":2,"version":1},"data":{"sessionID":"ses_a","id":"c","content":[{"type":5}]}}
    , &diag);
    try testing.expectError(error.InvalidWire, decodeToolSuccess(arena.allocator(), typed, &diag));
}

test "a string decodes the way encoding/json decodes one, replacing what is not UTF-8" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var diag = Diagnostic{};
    const event = try decodeEvent(arena.allocator(), "{\"id\":\"evt_1\",\"type\":\"session.text.ended\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":1,\"version\":1},\"data\":{\"sessionID\":\"ses_a\",\"text\":\"a\xffb\\ud800c\\ud83d\\ude00\"}}", &diag);
    const data = try decodePartEnded(arena.allocator(), event, &diag);
    try testing.expectEqualStrings("a\u{fffd}b\u{fffd}c\u{1f600}", data.text);
}

test "an API error keeps its status and the last string tag it names, and no tag once any names a non-string" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try testing.expectEqualStrings("opencode native: HTTP 409 ConflictError", try (try decodeApiError(scratch, 409, "{\"_tag\":\"ConflictError\"}")).message(scratch));
    try testing.expectEqualStrings("opencode native: HTTP 404 B", try (try decodeApiError(scratch, 404, "{\"_Tag\":\"A\",\"_tag\":\"B\"}")).message(scratch));
    try testing.expectEqualStrings("opencode native: HTTP 404 A", try (try decodeApiError(scratch, 404, "{\"_tag\":\"A\",\"_tag\":null,\"other\":5}")).message(scratch));
    try testing.expectEqualStrings("opencode native: HTTP 404", try (try decodeApiError(scratch, 404, "{\"_tag\":\"A\",\"_TAG\":\"B\",\"_tag\":5,\"_tag\":null}")).message(scratch));
    try testing.expectEqualStrings("opencode native: HTTP 404", try (try decodeApiError(scratch, 404, "{\"_tag\":5,\"_tag\":\"A\"}")).message(scratch));
    try testing.expectEqualStrings("opencode native: HTTP 503", try (try decodeApiError(scratch, 503, "{\"_tag\":")).message(scratch));
    try testing.expectEqualStrings("opencode native: HTTP 500", try (try decodeApiError(scratch, 500, "[\"_tag\"]")).message(scratch));
}

test "identity patterns are the pinned regular expressions" {
    try testing.expect(validSessionID("ses_fake00000000000000"));
    try testing.expect(validSessionID("sesA"));
    try testing.expect(!validSessionID("ses"));
    try testing.expect(!validSessionID("ses a"));
    try testing.expect(validMessageID("msg_fake0000000000000004"));
    try testing.expect(!validMessageID("msg_"));
    try testing.expect(!validMessageID("msgx"));
    try testing.expect(validEventID("evt_c001"));
    try testing.expect(!validEventID("evt_c.1"));
}

test "a prompt request is written the way the oracle marshals it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectEqualStrings(
        "{\"id\":\"msg_a\",\"text\":\"a\\u003cb\",\"delivery\":\"steer\"}",
        try marshalPromptRequest(arena.allocator(), .{ .id = "msg_a", .text = "a<b", .delivery = "steer" }),
    );
    try testing.expectEqualStrings("{\"text\":\"\"}", try marshalPromptRequest(arena.allocator(), .{}));
}

fn decodeEveryShape(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var diag = Diagnostic{};
    const event = try decodeEvent(scratch, "{\"id\":\"evt_1\",\"type\":\"session.tool.progress\",\"data\":{\"sessionID\":\"ses_a\",\"assistantMessageID\":\"msg_1\",\"id\":\"c\",\"metadata\":{\"k\":[1,\"a\xffb\\ud800\"]}}}", &diag);
    _ = try decodeToolProgress(scratch, event, &diag);
    const called = try decodeEvent(scratch, "{\"id\":\"evt_2\",\"type\":\"session.tool.called\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":2,\"version\":1},\"data\":{\"sessionID\":\"ses_a\",\"id\":\"c\",\"input\":{\"b\":1,\"a\":[2]},\"executed\":true}}", &diag);
    const input = (try decodeToolCalled(scratch, called, &diag)).input;
    _ = try gomarshal.marshal(scratch, try gomarshal.canonicalAny(scratch, input));
    _ = try decodeApiError(scratch, 409, "{\"_tag\":\"ConflictError\",\"message\":\"x\"}");
}

test "decoding propagates every allocation failure and leaks nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, decodeEveryShape, .{});
}

test "an event whose data nests past 256 levels is decoded, not aborted on" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const nested = ("[" ** 300) ++ "1" ++ ("]" ** 300);
    var diag = Diagnostic{};
    const event = try decodeEvent(arena.allocator(), "{\"id\":\"evt_1\",\"type\":\"session.text.ended\",\"durable\":{\"aggregateID\":\"ses_a\",\"seq\":1,\"version\":1},\"data\":{\"sessionID\":\"ses_a\",\"text\":\"a\",\"extra\":" ++ nested ++ "}}", &diag);
    try testing.expectEqualStrings("evt_1", event.id);
    try testing.expect(std.mem.indexOf(u8, event.data, nested) != null);
}
