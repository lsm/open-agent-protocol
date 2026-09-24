const std = @import("std");
const gojson = @import("gojson");

pub const method_initialize = "initialize";
pub const method_initialized = "initialized";
pub const method_thread_start = "thread/start";
pub const method_thread_resume = "thread/resume";
pub const method_turn_start = "turn/start";
pub const method_turn_interrupt = "turn/interrupt";
pub const method_turn_started = "turn/started";
pub const method_turn_completed = "turn/completed";
pub const method_item_started = "item/started";
pub const method_item_completed = "item/completed";
pub const method_agent_delta = "item/agentMessage/delta";
pub const method_command_approval = "item/commandExecution/requestApproval";
pub const method_file_approval = "item/fileChange/requestApproval";
pub const method_user_input = "item/tool/requestUserInput";

pub const turn_completed = "completed";
pub const turn_interrupted = "interrupted";
pub const turn_failed = "failed";
pub const turn_in_progress = "inProgress";

pub const Shape = union(enum) {
    string,
    boolean,
    integer,
    raw,
    object: []const Field,
    array: *const Shape,
};

pub const Field = struct {
    name: []const u8,
    shape: Shape,
};

const string_shape: Shape = .string;

const turn_error_fields = [_]Field{
    .{ .name = "message", .shape = .string },
    .{ .name = "codexErrorInfo", .shape = .raw },
    .{ .name = "additionalDetails", .shape = .string },
    .{ .name = "misalignment", .shape = .raw },
};

const tool_error_fields = [_]Field{
    .{ .name = "message", .shape = .string },
};

const file_change_fields = [_]Field{
    .{ .name = "path", .shape = .string },
    .{ .name = "kind", .shape = .raw },
    .{ .name = "diff", .shape = .string },
};
const file_change_shape: Shape = .{ .object = &file_change_fields };

const item_fields = [_]Field{
    .{ .name = "type", .shape = .string },
    .{ .name = "id", .shape = .string },
    .{ .name = "text", .shape = .string },
    .{ .name = "command", .shape = .string },
    .{ .name = "cwd", .shape = .string },
    .{ .name = "path", .shape = .string },
    .{ .name = "changes", .shape = .{ .array = &file_change_shape } },
    .{ .name = "server", .shape = .string },
    .{ .name = "tool", .shape = .string },
    .{ .name = "status", .shape = .string },
    .{ .name = "arguments", .shape = .raw },
    .{ .name = "aggregatedOutput", .shape = .string },
    .{ .name = "exitCode", .shape = .integer },
    .{ .name = "durationMs", .shape = .integer },
    .{ .name = "result", .shape = .raw },
    .{ .name = "error", .shape = .{ .object = &tool_error_fields } },
};
const item_shape: Shape = .{ .object = &item_fields };

const turn_fields = [_]Field{
    .{ .name = "id", .shape = .string },
    .{ .name = "status", .shape = .string },
    .{ .name = "items", .shape = .{ .array = &item_shape } },
    .{ .name = "error", .shape = .{ .object = &turn_error_fields } },
};
const turn_shape: Shape = .{ .object = &turn_fields };

const thread_fields = [_]Field{
    .{ .name = "id", .shape = .string },
    .{ .name = "turns", .shape = .{ .array = &turn_shape } },
};

const turn_notification_fields = [_]Field{
    .{ .name = "threadId", .shape = .string },
    .{ .name = "turn", .shape = turn_shape },
};

pub const turn_started_notification: Shape = .{ .object = &turn_notification_fields };
pub const turn_completed_notification: Shape = .{ .object = &turn_notification_fields };

const agent_delta_fields = [_]Field{
    .{ .name = "threadId", .shape = .string },
    .{ .name = "turnId", .shape = .string },
    .{ .name = "itemId", .shape = .string },
    .{ .name = "delta", .shape = .string },
};
pub const agent_delta_notification: Shape = .{ .object = &agent_delta_fields };

const item_notification_fields = [_]Field{
    .{ .name = "threadId", .shape = .string },
    .{ .name = "turnId", .shape = .string },
    .{ .name = "item", .shape = item_shape },
};
pub const item_notification: Shape = .{ .object = &item_notification_fields };

const thread_start_response_fields = [_]Field{
    .{ .name = "thread", .shape = .{ .object = &thread_fields } },
    .{ .name = "model", .shape = .string },
    .{ .name = "modelProvider", .shape = .string },
    .{ .name = "serviceTier", .shape = .string },
    .{ .name = "cwd", .shape = .string },
};
pub const thread_start_response: Shape = .{ .object = &thread_start_response_fields };

const thread_resume_response_fields = [_]Field{
    .{ .name = "thread", .shape = .{ .object = &thread_fields } },
};
pub const thread_resume_response: Shape = .{ .object = &thread_resume_response_fields };

const turn_start_response_fields = [_]Field{
    .{ .name = "turn", .shape = turn_shape },
};
pub const turn_start_response: Shape = .{ .object = &turn_start_response_fields };

pub const turn_interrupt_response: Shape = .{ .object = &.{} };

const initialize_response_fields = [_]Field{
    .{ .name = "userAgent", .shape = .string },
    .{ .name = "codexHome", .shape = .string },
    .{ .name = "platformFamily", .shape = .string },
    .{ .name = "platformOs", .shape = .string },
};
pub const initialize_response: Shape = .{ .object = &initialize_response_fields };

const command_approval_fields = [_]Field{
    .{ .name = "threadId", .shape = .string },
    .{ .name = "turnId", .shape = .string },
    .{ .name = "itemId", .shape = .string },
    .{ .name = "kind", .shape = .string },
    .{ .name = "startedAtMs", .shape = .integer },
    .{ .name = "approvalId", .shape = .string },
    .{ .name = "environmentId", .shape = .string },
    .{ .name = "reason", .shape = .string },
    .{ .name = "networkApprovalContext", .shape = .raw },
    .{ .name = "command", .shape = .string },
    .{ .name = "cwd", .shape = .string },
    .{ .name = "commandActions", .shape = .raw },
    .{ .name = "additionalPermissions", .shape = .raw },
    .{ .name = "availableDecisions", .shape = .raw },
    .{ .name = "proposedExecpolicyAmendment", .shape = .raw },
    .{ .name = "proposedNetworkPolicyAmendments", .shape = .raw },
};
pub const command_approval_params: Shape = .{ .object = &command_approval_fields };

const file_approval_fields = [_]Field{
    .{ .name = "threadId", .shape = .string },
    .{ .name = "turnId", .shape = .string },
    .{ .name = "itemId", .shape = .string },
    .{ .name = "startedAtMs", .shape = .integer },
    .{ .name = "reason", .shape = .string },
    .{ .name = "grantRoot", .shape = .string },
};
pub const file_approval_params: Shape = .{ .object = &file_approval_fields };

pub const approval_decisions: Shape = .{ .array = &string_shape };

const user_input_option_fields = [_]Field{
    .{ .name = "label", .shape = .string },
    .{ .name = "description", .shape = .string },
};
const user_input_option_shape: Shape = .{ .object = &user_input_option_fields };

const user_input_question_fields = [_]Field{
    .{ .name = "id", .shape = .string },
    .{ .name = "header", .shape = .string },
    .{ .name = "question", .shape = .string },
    .{ .name = "isOther", .shape = .boolean },
    .{ .name = "isSecret", .shape = .boolean },
    .{ .name = "options", .shape = .{ .array = &user_input_option_shape } },
};
const user_input_question_shape: Shape = .{ .object = &user_input_question_fields };

const user_input_request_fields = [_]Field{
    .{ .name = "threadId", .shape = .string },
    .{ .name = "turnId", .shape = .string },
    .{ .name = "itemId", .shape = .string },
    .{ .name = "questions", .shape = .{ .array = &user_input_question_shape } },
    .{ .name = "isBlocking", .shape = .boolean },
    .{ .name = "autoResolutionMs", .shape = .integer },
};
pub const user_input_request_params: Shape = .{ .object = &user_input_request_fields };

fn integerLiteral(value: std.json.Value) bool {
    return switch (value) {
        .integer => true,
        .number_string => |literal| blk: {
            _ = std.fmt.parseInt(i64, literal, 10) catch break :blk false;
            break :blk true;
        },
        else => false,
    };
}

fn fieldFor(fields: []const Field, name: []const u8) ?Field {
    for (fields) |field| {
        if (std.mem.eql(u8, field.name, name)) return field;
    }
    for (fields) |field| {
        if (gojson.foldEql(field.name, name)) return field;
    }
    return null;
}

pub fn decodes(value: std.json.Value, shape: Shape) bool {
    if (value == .null) return true;
    return switch (shape) {
        .raw => true,
        .string => value == .string,
        .boolean => value == .bool,
        .integer => integerLiteral(value),
        .array => |element| blk: {
            if (value != .array) break :blk false;
            for (value.array.items) |item| {
                if (!decodes(item, element.*)) break :blk false;
            }
            break :blk true;
        },
        .object => |fields| blk: {
            if (value != .object) break :blk false;
            var members = value.object.iterator();
            while (members.next()) |entry| {
                const field = fieldFor(fields, entry.key_ptr.*) orelse continue;
                if (!decodes(entry.value_ptr.*, field.shape)) break :blk false;
            }
            break :blk true;
        },
    };
}

pub fn decodesPresent(value: ?std.json.Value, shape: Shape) bool {
    const present = value orelse return false;
    return decodes(present, shape);
}

pub fn text(value: std.json.Value, path: []const []const u8) []const u8 {
    if (value != .object) return "";
    const found = gojson.foldedSet(value.object, path) orelse return "";
    return if (found == .string) found.string else "";
}

pub fn flag(value: std.json.Value, path: []const []const u8) bool {
    if (value != .object) return false;
    const found = gojson.foldedSet(value.object, path) orelse return false;
    return found == .bool and found.bool;
}

pub fn pointer(value: std.json.Value, path: []const []const u8) ?std.json.Value {
    if (value != .object) return null;
    const found = gojson.foldedLast(value.object, path) orelse return null;
    return if (found == .null) null else found;
}

pub fn raw(value: std.json.Value, path: []const []const u8) ?std.json.Value {
    if (value != .object) return null;
    return gojson.foldedLast(value.object, path);
}

pub fn items(value: std.json.Value, path: []const []const u8) []std.json.Value {
    const found = pointer(value, path) orelse return &.{};
    return if (found == .array) found.array.items else &.{};
}

fn object() std.json.ObjectMap {
    return .empty;
}

fn putText(arena: std.mem.Allocator, map: *std.json.ObjectMap, key: []const u8, value: []const u8) !void {
    try map.put(arena, key, .{ .string = value });
}

fn putNonEmpty(arena: std.mem.Allocator, map: *std.json.ObjectMap, key: []const u8, value: []const u8) !void {
    if (value.len != 0) try putText(arena, map, key, value);
}

pub fn initializeParams(arena: std.mem.Allocator, name: []const u8, version: []const u8) !std.json.Value {
    var info = object();
    try putText(arena, &info, "name", name);
    try putText(arena, &info, "version", version);
    var params = object();
    try params.put(arena, "clientInfo", .{ .object = info });
    return .{ .object = params };
}

pub const ThreadStart = struct {
    model: []const u8 = "",
    cwd: []const u8 = "",
    approval_policy: []const u8 = "",
    sandbox: []const u8 = "",
};

pub fn threadStartParams(arena: std.mem.Allocator, start: ThreadStart) !std.json.Value {
    var params = object();
    try putNonEmpty(arena, &params, "model", start.model);
    try putNonEmpty(arena, &params, "cwd", start.cwd);
    try putNonEmpty(arena, &params, "approvalPolicy", start.approval_policy);
    try putNonEmpty(arena, &params, "sandbox", start.sandbox);
    return .{ .object = params };
}

pub fn threadResumeParams(arena: std.mem.Allocator, thread_id: []const u8) !std.json.Value {
    var params = object();
    try putText(arena, &params, "threadId", thread_id);
    return .{ .object = params };
}

pub fn turnStartParams(arena: std.mem.Allocator, thread_id: []const u8, texts: []const []const u8, model: []const u8) !std.json.Value {
    var input = std.json.Array.init(arena);
    for (texts) |entry| {
        var element = object();
        try putText(arena, &element, "type", "text");
        try putNonEmpty(arena, &element, "text", entry);
        try input.append(.{ .object = element });
    }
    var params = object();
    try putText(arena, &params, "threadId", thread_id);
    try params.put(arena, "input", .{ .array = input });
    try putNonEmpty(arena, &params, "model", model);
    return .{ .object = params };
}

pub fn turnInterruptParams(arena: std.mem.Allocator, thread_id: []const u8, turn_id: []const u8) !std.json.Value {
    var params = object();
    try putText(arena, &params, "threadId", thread_id);
    try putText(arena, &params, "turnId", turn_id);
    return .{ .object = params };
}

pub fn approvalResponse(arena: std.mem.Allocator, decision: []const u8) !std.json.Value {
    var result = object();
    try putText(arena, &result, "decision", decision);
    return .{ .object = result };
}

pub const NativeAnswer = struct {
    question_id: []const u8,
    answer: []const u8,
};

fn answerBefore(_: void, left: NativeAnswer, right: NativeAnswer) bool {
    return std.mem.order(u8, left.question_id, right.question_id) == .lt;
}

pub fn userInputResponse(arena: std.mem.Allocator, answers: []NativeAnswer) !std.json.Value {
    std.mem.sort(NativeAnswer, answers, {}, answerBefore);
    var keyed = object();
    for (answers) |entry| {
        var list = std.json.Array.init(arena);
        try list.append(.{ .string = entry.answer });
        var holder = object();
        try holder.put(arena, "answers", .{ .array = list });
        try keyed.put(arena, entry.question_id, .{ .object = holder });
    }
    var result = object();
    try result.put(arena, "answers", .{ .object = keyed });
    return .{ .object = result };
}

pub fn methodData(arena: std.mem.Allocator, method: []const u8) !std.json.Value {
    var data = object();
    try putText(arena, &data, "method", method);
    return .{ .object = data };
}

const testing = std.testing;

fn parse(arena: std.mem.Allocator, text_value: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text_value, .{ .parse_numbers = false });
}

test "a member of the wrong type fails the whole decode, wherever it is nested" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try testing.expect(decodes(try parse(a, "{\"threadId\":\"t\",\"turn\":{\"id\":\"u\",\"status\":\"inProgress\"}}"), turn_started_notification));
    try testing.expect(!decodes(try parse(a, "{\"threadId\":7,\"turn\":{}}"), turn_started_notification));
    try testing.expect(!decodes(try parse(a, "{\"threadId\":\"t\",\"turn\":[]}"), turn_started_notification));
    try testing.expect(!decodes(try parse(a, "{\"turn\":{\"items\":[{\"type\":5}]}}"), turn_started_notification));
    try testing.expect(!decodes(try parse(a, "{\"turn\":{\"error\":{\"message\":1}}}"), turn_completed_notification));
    try testing.expect(!decodes(try parse(a, "[]"), turn_started_notification));
    try testing.expect(!decodes(try parse(a, "\"x\""), item_notification));
}

test "null decodes into anything and an unknown member is ignored" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try testing.expect(decodes(try parse(a, "null"), item_notification));
    try testing.expect(decodes(try parse(a, "{\"threadId\":null,\"item\":{\"changes\":null,\"error\":null},\"extra\":[1]}"), item_notification));
    try testing.expect(!decodesPresent(null, item_notification));
}

test "an integer member takes only a base-ten integer literal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try testing.expect(decodes(try parse(a, "{\"startedAtMs\":-3}"), command_approval_params));
    try testing.expect(!decodes(try parse(a, "{\"startedAtMs\":1.0}"), command_approval_params));
    try testing.expect(!decodes(try parse(a, "{\"startedAtMs\":1e3}"), command_approval_params));
    try testing.expect(!decodes(try parse(a, "{\"startedAtMs\":\"1\"}"), command_approval_params));
}

test "a member name matches its field case-insensitively, the way Go decodes a struct" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    try testing.expect(!decodes(try parse(a, "{\"THREADID\":1}"), agent_delta_notification));
    const folded = try parse(a, "{\"threadid\":\"t\",\"Delta\":\"d\"}");
    try testing.expectEqualStrings("t", text(folded, &.{"threadId"}));
    try testing.expectEqualStrings("d", text(folded, &.{"delta"}));
}

test "a later null leaves a value member alone but clears a pointer, slice or raw member" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const value = try parse(a, "{\"id\":\"i\",\"ID\":null,\"error\":{\"message\":\"m\"},\"Error\":null,\"result\":{},\"Result\":null}");
    try testing.expectEqualStrings("i", text(value, &.{"id"}));
    try testing.expect(pointer(value, &.{"error"}) == null);
    try testing.expect(raw(value, &.{"result"}).? == .null);
    try testing.expect(raw(value, &.{"arguments"}) == null);
}

test "parameters are written in Go's struct order and omit what Go omits" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const encode = struct {
        fn run(allocator: std.mem.Allocator, value: std.json.Value) ![]u8 {
            return std.json.Stringify.valueAlloc(allocator, value, .{});
        }
    }.run;
    try testing.expectEqualStrings("{}", try encode(a, try threadStartParams(a, .{})));
    try testing.expectEqualStrings(
        "{\"model\":\"m\",\"cwd\":\"/w\",\"approvalPolicy\":\"never\",\"sandbox\":\"s\"}",
        try encode(a, try threadStartParams(a, .{ .sandbox = "s", .approval_policy = "never", .cwd = "/w", .model = "m" })),
    );
    try testing.expectEqualStrings(
        "{\"threadId\":\"t\",\"input\":[{\"type\":\"text\",\"text\":\"hi\"},{\"type\":\"text\"}]}",
        try encode(a, try turnStartParams(a, "t", &.{ "hi", "" }, "")),
    );
    var answers = [_]NativeAnswer{ .{ .question_id = "note", .answer = "n" }, .{ .question_id = "mode", .answer = "Safe" } };
    try testing.expectEqualStrings(
        "{\"answers\":{\"mode\":{\"answers\":[\"Safe\"]},\"note\":{\"answers\":[\"n\"]}}}",
        try encode(a, try userInputResponse(a, &answers)),
    );
    try testing.expectEqualStrings("{\"clientInfo\":{\"name\":\"n\",\"version\":\"v\"}}", try encode(a, try initializeParams(a, "n", "v")));
}
