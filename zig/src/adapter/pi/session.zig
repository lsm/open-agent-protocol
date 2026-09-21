const std = @import("std");

pub const capability_revision = "pi-v0.85.1-oap-v1";
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";

pub const Error = error{
    InvalidResolution,
    InteractionNotFound,
    InteractionResolved,
    InvalidFrame,
    OutOfMemory,
};

pub const Counters = struct {
    ids: usize = 0,
    clock: i64 = 0,

    pub fn nextID(self: *Counters, arena: std.mem.Allocator, kind: []const u8) ![]const u8 {
        self.ids += 1;
        return std.fmt.allocPrint(arena, "{s}-{d}", .{ kind, self.ids });
    }

    pub fn nextTick(self: *Counters) i64 {
        self.clock += 1;
        return self.clock;
    }
};

const Status = enum {
    running,
    cancelling,
    completed,
    failed,
    cancelled,

    fn text(self: Status) []const u8 {
        return @tagName(self);
    }
};

pub const Interaction = struct {
    id: []const u8,
    text: bool,
    offered: []const []const u8 = &.{},
    resolved: bool = false,
};

pub const Reducer = struct {
    arena: std.mem.Allocator,
    counters: Counters = .{},
    session_id: []const u8 = "session",
    run_id: []const u8 = "",
    message_id: []const u8 = "",
    sequence: u64 = 1,
    started: bool = false,
    terminal: bool = false,
    cancel_intent: bool = false,
    candidate: ?std.json.Value = null,
    candidate_present: bool = false,
    final: ?WireMessage = null,
    text: std.ArrayList(u8) = .empty,
    reasoning: std.ArrayList(u8) = .empty,
    pending: std.ArrayList(std.json.Value) = .empty,
    tools: std.ArrayList(*ToolState) = .empty,
    interactions: std.ArrayList(*Interaction) = .empty,
    pending_ui: std.ArrayList(std.json.Value) = .empty,
    emitted: std.ArrayList(std.json.Value) = .empty,

    pub fn init(arena: std.mem.Allocator) Reducer {
        return .{ .arena = arena };
    }

    pub fn envelopes(self: *Reducer) []std.json.Value {
        return self.emitted.items;
    }

    fn object(self: *Reducer) !*std.json.ObjectMap {
        const map = try self.arena.create(std.json.ObjectMap);
        map.* = .{};
        return map;
    }

    fn str(value: []const u8) std.json.Value {
        return .{ .string = value };
    }

    fn emit(self: *Reducer, kind: []const u8, payload: std.json.Value, terminal: bool) !void {
        _ = try self.emitReplying(kind, payload, terminal, "");
    }

    fn emitReplying(self: *Reducer, kind: []const u8, payload: std.json.Value, terminal: bool, reply: []const u8) ![]const u8 {
        if (self.terminal) return "";
        const id = try self.counters.nextID(self.arena, "event");
        const now = self.counters.nextTick();
        const map = try self.object();
        try map.put(self.arena, "protocol", str(protocol_name));
        try map.put(self.arena, "version", str(protocol_version));
        try map.put(self.arena, "profile", str(profile));
        try map.put(self.arena, "type", str(kind));
        try map.put(self.arena, "id", str(id));
        try map.put(self.arena, "payload", payload);
        try map.put(self.arena, "sequence", .{ .integer = @intCast(self.sequence) });
        try map.put(self.arena, "timestamp_ms", .{ .integer = now });
        try map.put(self.arena, "session_id", str(self.session_id));
        try map.put(self.arena, "run_id", str(self.run_id));
        try map.put(self.arena, "capability_revision", str(capability_revision));
        if (reply.len != 0) try map.put(self.arena, "in_reply_to", str(reply));
        if (std.mem.startsWith(u8, kind, "action.call.")) {
            if (payload.object.get("tool_call_id")) |carried| try map.put(self.arena, "tool_call_id", carried);
        }
        self.sequence += 1;
        try self.emitted.append(self.arena, .{ .object = map.* });
        if (terminal) self.terminal = true;
        return id;
    }
};

const user_members = [_][]const u8{ "role", "content", "timestamp" };

const tool_result_members = [_][]const u8{
    "role",      "toolCallId", "toolName", "content",
    "usage",     "details",    "isError",  "addedToolNames",
    "timestamp",
};

const assistant_members = [_][]const u8{
    "role",          "content",      "api",                   "provider",
    "model",         "usage",        "stopReason",            "timestamp",
    "responseModel", "responseId",   "providerThinkingLevel", "diagnostics",
    "deferred",      "errorMessage", "rawStopReason",         "endTurn",
};

const assistant_required = [_][]const u8{ "content", "api", "provider", "model", "usage", "stopReason", "timestamp" };

const EventShape = struct { name: []const u8, members: []const []const u8 };

const event_shapes = [_]EventShape{
    .{ .name = "agent_start", .members = &.{"type"} },
    .{ .name = "agent_settled", .members = &.{"type"} },
    .{ .name = "message_update", .members = &.{ "type", "usage", "assistantMessageEvent" } },
    .{ .name = "message_end", .members = &.{ "type", "message" } },
    .{ .name = "agent_end", .members = &.{ "type", "messages", "willRetry" } },
    .{ .name = "tool_execution_start", .members = &.{ "type", "toolCallId", "toolName", "args" } },
    .{ .name = "tool_execution_update", .members = &.{ "type", "toolCallId", "toolName", "args", "partialResult" } },
    .{ .name = "tool_execution_end", .members = &.{ "type", "toolCallId", "toolName", "result", "isError" } },
};

fn eventShape(name: []const u8) ?EventShape {
    for (&event_shapes) |shape| {
        if (std.mem.eql(u8, shape.name, name)) return shape;
    }
    return null;
}

fn decodeEvent(reducer: *Reducer, event: std.json.Value, kind: []const u8) !bool {
    const shape = eventShape(kind) orelse return true;
    if (event != .object) {
        try failRun(reducer, "pi_invalid_event", "event is not an object");
        return false;
    }
    for (event.object.keys()) |name| {
        if (!listed(shape.members, name)) {
            try failRun(reducer, "pi_invalid_event", "event carried a member outside its pinned shape");
            return false;
        }
    }
    return true;
}

fn listed(names: []const []const u8, name: []const u8) bool {
    for (names) |candidate| {
        if (std.mem.eql(u8, candidate, name)) return true;
    }
    return false;
}

fn memberOf(value: std.json.Value, name: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(name);
}

fn textOf(value: std.json.Value, name: []const u8) []const u8 {
    const found = memberOf(value, name) orelse return "";
    return switch (found) {
        .string => |s| s,
        else => "",
    };
}

pub const WireMessage = struct {
    content: ?std.json.Value,
    stop_reason: []const u8,
    error_message: []const u8,
};

fn closedRoleMembers(raw: std.json.Value, names: []const []const u8) !void {
    for (raw.object.keys()) |name| {
        if (!listed(names, name)) return Error.InvalidFrame;
    }
}

fn typedStrings(raw: std.json.Value, names: []const []const u8) !void {
    for (names) |name| {
        const value = raw.object.get(name) orelse continue;
        if (value != .string and value != .null) return Error.InvalidFrame;
    }
}

fn typedArray(raw: std.json.Value, name: []const u8) !void {
    const value = raw.object.get(name) orelse return;
    if (value != .array and value != .null) return Error.InvalidFrame;
}

fn typedStringArray(raw: std.json.Value, name: []const u8) !void {
    const value = raw.object.get(name) orelse return;
    if (value == .null) return;
    if (value != .array) return Error.InvalidFrame;
    for (value.array.items) |entry| {
        if (entry != .string and entry != .null) return Error.InvalidFrame;
    }
}

fn typedBool(raw: std.json.Value, name: []const u8) !void {
    const value = raw.object.get(name) orelse return;
    if (value != .bool and value != .null) return Error.InvalidFrame;
}

fn typedInteger(raw: std.json.Value, name: []const u8) !void {
    const value = raw.object.get(name) orelse return;
    if (value != .integer and value != .null) return Error.InvalidFrame;
}

fn findInteraction(reducer: *Reducer, id: []const u8) ?*Interaction {
    for (reducer.interactions.items) |interaction| {
        if (std.mem.eql(u8, interaction.id, id)) return interaction;
    }
    return null;
}

pub fn pendingInteractionID(reducer: *Reducer) ?[]const u8 {
    for (reducer.interactions.items) |interaction| {
        if (!interaction.resolved) return interaction.id;
    }
    return null;
}

fn requireIndex(raw: std.json.Value) !void {
    const index = integerMember(raw, "contentIndex") orelse return Error.InvalidFrame;
    if (index < 0) return Error.InvalidFrame;
}

pub fn decodeWireMessage(raw: std.json.Value) !?WireMessage {
    if (raw != .object) return Error.InvalidFrame;
    const role_value = raw.object.get("role") orelse return Error.InvalidFrame;
    const role: []const u8 = switch (role_value) {
        .string => |s| s,
        .null => "",
        else => return Error.InvalidFrame,
    };
    if (std.mem.eql(u8, role, "assistant")) {
        try closedRoleMembers(raw, &assistant_members);
        for (&assistant_required) |name| {
            if (raw.object.get(name) == null) return Error.InvalidFrame;
        }
        try typedStrings(raw, &.{ "api", "provider", "model", "stopReason", "responseModel", "responseId", "providerThinkingLevel", "errorMessage", "rawStopReason" });
        try typedInteger(raw, "timestamp");
        try typedArray(raw, "diagnostics");
        try typedBool(raw, "endTurn");
        try validateWireContent(raw.object.get("content").?, false);
        return .{
            .content = raw.object.get("content"),
            .stop_reason = textOf(raw, "stopReason"),
            .error_message = textOf(raw, "errorMessage"),
        };
    }
    if (std.mem.eql(u8, role, "user")) {
        try closedRoleMembers(raw, &user_members);
        try typedInteger(raw, "timestamp");
        for (&[_][]const u8{ "content", "timestamp" }) |name| {
            if (raw.object.get(name) == null) return Error.InvalidFrame;
        }
        try validateWireContent(raw.object.get("content").?, true);
        return null;
    }
    if (std.mem.eql(u8, role, "toolResult")) {
        try closedRoleMembers(raw, &tool_result_members);
        try typedStrings(raw, &.{ "toolCallId", "toolName" });
        try typedInteger(raw, "timestamp");
        try typedBool(raw, "isError");
        try typedStringArray(raw, "addedToolNames");
        for (&[_][]const u8{ "toolCallId", "toolName", "content", "isError", "timestamp" }) |name| {
            if (raw.object.get(name) == null) return Error.InvalidFrame;
        }
        try validateWireContent(raw.object.get("content").?, true);
        return null;
    }
    return Error.InvalidFrame;
}

fn validateToolCallShape(part: std.json.Value) !void {
    if (part == .null) return;
    if (part != .object) return Error.InvalidFrame;
    try closedMembers(part, &.{ "type", "id", "name", "arguments", "thoughtSignature", "namespace" });
    try typedStrings(part, &.{ "type", "id", "name", "thoughtSignature", "namespace" });
}

fn validateToolCallBlock(part: std.json.Value) !void {
    if (part != .object) return Error.InvalidFrame;
    try validateToolCallShape(part);
    try requireMembers(part, &.{ "id", "name", "arguments" });
}

pub fn validateWireContent(raw: std.json.Value, allow_image: bool) !void {
    if (raw == .null or raw == .string) return;
    if (raw != .array) return Error.InvalidFrame;
    for (raw.array.items) |part| {
        if (part != .object) return Error.InvalidFrame;
        const kind_value = part.object.get("type") orelse return Error.InvalidFrame;
        const kind: []const u8 = switch (kind_value) {
            .string => |s| s,
            .null => "",
            else => return Error.InvalidFrame,
        };
        if (std.mem.eql(u8, kind, "text")) {
            try closedMembers(part, &.{ "type", "text", "textSignature" });
            try requireMembers(part, &.{"text"});
            try typedStrings(part, &.{ "text", "textSignature" });
        } else if (std.mem.eql(u8, kind, "thinking")) {
            if (allow_image) return Error.InvalidFrame;
            try closedMembers(part, &.{ "type", "thinking", "thinkingSignature", "redacted" });
            try requireMembers(part, &.{"thinking"});
            try typedStrings(part, &.{ "thinking", "thinkingSignature" });
            try typedBool(part, "redacted");
        } else if (std.mem.eql(u8, kind, "toolCall")) {
            if (allow_image) return Error.InvalidFrame;
            try validateToolCallBlock(part);
        } else if (std.mem.eql(u8, kind, "image")) {
            if (!allow_image) return Error.InvalidFrame;
            try closedMembers(part, &.{ "type", "data", "mimeType" });
            try requireMembers(part, &.{ "data", "mimeType" });
            try typedStrings(part, &.{ "data", "mimeType" });
        } else return Error.InvalidFrame;
    }
}

fn requireMembers(value: std.json.Value, names: []const []const u8) !void {
    for (names) |name| {
        if (value.object.get(name) == null) return Error.InvalidFrame;
    }
}

fn closedMembers(part: std.json.Value, allowed: []const []const u8) !void {
    for (part.object.keys()) |name| {
        if (!listed(allowed, name)) return Error.InvalidFrame;
    }
}

const ignored_events = [_][]const u8{
    "auto_retry_start",                  "auto_retry_end",
    "turn_start",                        "turn_end",
    "message_start",                     "queue_update",
    "compaction_start",                  "compaction_end",
    "entry_appended",                    "session_info_changed",
    "thinking_level_changed",            "summarization_retry_scheduled",
    "summarization_retry_attempt_start", "summarization_retry_finished",
    "bash_execution_update",             "extension_error",
};

pub fn open(reducer: *Reducer) !void {
    _ = try reducer.counters.nextID(reducer.arena, "message");
    reducer.run_id = try reducer.counters.nextID(reducer.arena, "run");
    reducer.message_id = try reducer.counters.nextID(reducer.arena, "message");
    _ = reducer.counters.nextTick();
    _ = reducer.counters.nextTick();
    const start = try reducer.object();
    try start.put(reducer.arena, "type", Reducer.str("agent_start"));
    try apply(reducer, .{ .object = start.* });
    _ = try reducer.counters.nextID(reducer.arena, "submission");
}

pub fn apply(reducer: *Reducer, event: std.json.Value) !void {
    if (reducer.terminal) return;
    const kind = textOf(event, "type");
    if (!reducer.started and !std.mem.eql(u8, kind, "agent_start")) {
        if (std.mem.eql(u8, kind, "agent_settled")) {
            try failRun(reducer, "pi_invalid_lifecycle", "agent_settled arrived without agent_start");
            return;
        }
        try reducer.pending.append(reducer.arena, event);
        return;
    }
    if (!try decodeEvent(reducer, event, kind)) return;
    if (std.mem.eql(u8, kind, "agent_start")) {
        if (reducer.started) {
            try failRun(reducer, "pi_invalid_lifecycle", "duplicate agent_start");
            return;
        }
        reducer.started = true;
        const started_at = reducer.counters.nextTick();
        const payload = try reducer.object();
        try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
        try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
        try payload.put(reducer.arena, "status", Reducer.str("running"));
        try payload.put(reducer.arena, "started_at_ms", .{ .integer = started_at });
        try reducer.emit("run.started", .{ .object = payload.* }, false);
        if (reducer.cancel_intent) try statusUpdate(reducer, "cancelling", "");
        const queued = try reducer.pending.toOwnedSlice(reducer.arena);
        for (queued) |pending| {
            if (reducer.terminal) break;
            try apply(reducer, pending);
        }
        const waiting = try reducer.pending_ui.toOwnedSlice(reducer.arena);
        for (waiting) |request| {
            if (reducer.terminal) break;
            try applyExtension(reducer, request);
        }
        return;
    }
    if (std.mem.eql(u8, kind, "message_end")) {
        const carried = memberOf(event, "message") orelse return Error.InvalidFrame;
        const decoded = decodeWireMessage(carried) catch {
            try failRun(reducer, "pi_invalid_message_end", "message_end carried an undecodable message");
            return;
        };
        if (decoded) |message| reducer.final = message;
        return;
    }
    if (std.mem.eql(u8, kind, "agent_end")) {
        const listed_messages = memberOf(event, "messages") orelse std.json.Value{ .null = {} };
        if (listed_messages != .array and listed_messages != .null) {
            try failRun(reducer, "pi_invalid_event", "agent_end carried a messages member that is not a list");
            return;
        }
        if (memberOf(event, "willRetry")) |retry| {
            if (retry != .bool and retry != .null) {
                try failRun(reducer, "pi_invalid_event", "agent_end carried a willRetry that is not a boolean");
                return;
            }
            if (retry == .bool and retry.bool) {
                reducer.candidate = null;
                reducer.candidate_present = false;
                reducer.final = null;
                return;
            }
        }
        reducer.candidate = listed_messages;
        reducer.candidate_present = true;
        return;
    }
    if (std.mem.eql(u8, kind, "agent_settled")) {
        try settleRun(reducer);
        return;
    }
    if (std.mem.eql(u8, kind, "message_update")) {
        try applyMessageUpdate(reducer, event);
        return;
    }
    if (std.mem.eql(u8, kind, "tool_execution_start")) {
        try startTool(reducer, event);
        return;
    }
    if (std.mem.eql(u8, kind, "tool_execution_update")) {
        try updateTool(reducer, event);
        return;
    }
    if (std.mem.eql(u8, kind, "tool_execution_end")) {
        try endTool(reducer, event);
        return;
    }
    if (listed(&ignored_events, kind)) return;
    try failRun(reducer, "pi_unknown_event", "unknown event");
}

fn settleRun(reducer: *Reducer) !void {
    var final: ?WireMessage = null;
    if (reducer.candidate) |messages| {
        if (messages == .array) {
            var at = messages.array.items.len;
            while (at > 0) {
                at -= 1;
                const decoded = decodeWireMessage(messages.array.items[at]) catch {
                    try failRun(reducer, "pi_invalid_final_message", "a candidate message would not decode");
                    return;
                };
                if (decoded) |message| {
                    final = message;
                    break;
                }
            }
        }
    }
    if (final == null and reducer.candidate_present) final = reducer.final;
    const content: ?std.json.Value = if (final) |message| message.content else null;
    const stop_reason: []const u8 = if (final) |message| message.stop_reason else "";
    const error_message: []const u8 = if (final) |message| message.error_message else "";
    const aborted = final != null and std.mem.eql(u8, stop_reason, "aborted");
    if (reducer.cancel_intent and (!reducer.candidate_present or aborted)) {
        try settleChildren(reducer, true);
        const payload = try reducer.object();
        try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
        try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
        try payload.put(reducer.arena, "reason", Reducer.str("Pi settled after abort intent"));
        try reducer.emit("run.cancelled", .{ .object = payload.* }, true);
        return;
    }
    try settleChildren(reducer, false);
    if (!reducer.candidate_present) {
        try failRun(reducer, "pi_missing_agent_end", "agent_settled arrived without terminal agent_end");
        return;
    }
    if (content == null) {
        try failRun(reducer, "pi_missing_final_message", "agent settlement omitted assistant message");
        return;
    }
    if (error_message.len != 0 or std.mem.eql(u8, stop_reason, "error") or std.mem.eql(u8, stop_reason, "aborted")) {
        const message = if (error_message.len != 0) error_message else "Pi agent failed";
        try failWith(reducer, "pi_agent_failed", message);
        return;
    }
    const reason = if (stop_reason.len == 0) "end_turn" else stop_reason;
    const converted = wireContentOf(reducer, content.?) catch {
        try failRun(reducer, "pi_invalid_final_message", "final message carried content the reducer cannot map");
        return;
    };
    const response = try reducer.object();
    try response.put(reducer.arena, "id", Reducer.str(reducer.message_id));
    try response.put(reducer.arena, "role", Reducer.str("assistant"));
    try response.put(reducer.arena, "content", converted);
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "final_response", .{ .object = response.* });
    try payload.put(reducer.arena, "stop_reason", Reducer.str(reason));
    try reducer.emit("run.completed", .{ .object = payload.* }, true);
}

fn failRun(reducer: *Reducer, code: []const u8, message: []const u8) !void {
    if (!reducer.started) {
        reducer.terminal = true;
        return;
    }
    try failWith(reducer, code, message);
}

fn failWith(reducer: *Reducer, code: []const u8, message: []const u8) !void {
    try settleChildren(reducer, true);
    const err = try reducer.object();
    try err.put(reducer.arena, "code", Reducer.str(code));
    try err.put(reducer.arena, "message", Reducer.str(message));
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "error", .{ .object = err.* });
    try reducer.emit("run.failed", .{ .object = payload.* }, true);
}

pub const endpoint_id = "pi.rpc";

pub const ToolState = struct {
    native_id: []const u8,
    id: []const u8,
    name: []const u8,
    args: std.json.Value,
    progress: ?std.json.Value = null,
    result: ?std.json.Value = null,
    terminal: bool = false,
    started_event: []const u8 = "",
};

fn findTool(reducer: *Reducer, native_id: []const u8) ?*ToolState {
    for (reducer.tools.items) |tool| {
        if (std.mem.eql(u8, tool.native_id, native_id)) return tool;
    }
    return null;
}

fn toolPayload(reducer: *Reducer, tool: *ToolState, carry_arguments: bool, carry_progress: bool, carry_result: bool) !std.json.Value {
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "tool_call_id", Reducer.str(tool.id));
    try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
    try payload.put(reducer.arena, "execution_owner", Reducer.str("pi"));
    try payload.put(reducer.arena, "name", Reducer.str(tool.name));
    if (carry_arguments) try payload.put(reducer.arena, "arguments_json", tool.args);
    if (carry_progress) {
        if (tool.progress) |progress| try payload.put(reducer.arena, "progress", progress);
    }
    if (carry_result) {
        if (tool.result) |result| try payload.put(reducer.arena, "result", result);
    }
    return .{ .object = payload.* };
}

fn toolIdentifiers(reducer: *Reducer, event: std.json.Value) !bool {
    typedStrings(event, &.{ "toolCallId", "toolName" }) catch {
        try failRun(reducer, "pi_invalid_event", "tool event carried an identifier that is not a string");
        return false;
    };
    return true;
}

fn startTool(reducer: *Reducer, event: std.json.Value) !void {
    if (!try toolIdentifiers(reducer, event)) return;
    const native_id = textOf(event, "toolCallId");
    const name = textOf(event, "toolName");
    const args = memberOf(event, "args") orelse {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "invalid tool start");
        return;
    };
    if (native_id.len == 0 or name.len == 0) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "invalid tool start");
        return;
    }
    if (findTool(reducer, native_id) != null) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "duplicate tool start");
        return;
    }
    const tool = try reducer.arena.create(ToolState);
    tool.* = .{
        .native_id = native_id,
        .id = try reducer.counters.nextID(reducer.arena, "tool-call"),
        .name = name,
        .args = args,
    };
    try reducer.tools.append(reducer.arena, tool);
    const requested = try reducer.emitReplying("action.call.requested", try toolPayload(reducer, tool, true, false, false), false, "");
    tool.started_event = try reducer.emitReplying("action.call.started", try toolPayload(reducer, tool, false, false, false), false, requested);
}

fn updateTool(reducer: *Reducer, event: std.json.Value) !void {
    if (!try toolIdentifiers(reducer, event)) return;
    const native_id = textOf(event, "toolCallId");
    const tool = findTool(reducer, native_id) orelse {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool update without matching active start");
        return;
    };
    if (tool.terminal or !std.mem.eql(u8, tool.name, textOf(event, "toolName"))) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool update without matching active start");
        return;
    }
    if (memberOf(event, "partialResult") == null) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool update without matching active start");
        return;
    }
    tool.progress = memberOf(event, "partialResult");
    _ = try reducer.emitReplying("action.call.progress", try toolPayload(reducer, tool, false, true, false), false, tool.started_event);
}

fn endTool(reducer: *Reducer, event: std.json.Value) !void {
    if (!try toolIdentifiers(reducer, event)) return;
    const native_id = textOf(event, "toolCallId");
    const tool = findTool(reducer, native_id) orelse {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool end without matching active start");
        return;
    };
    if (tool.terminal or !std.mem.eql(u8, tool.name, textOf(event, "toolName"))) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool end without matching active start");
        return;
    }
    if (memberOf(event, "result") == null) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool end without matching active start");
        return;
    }
    const carried = memberOf(event, "isError") orelse std.json.Value{ .null = {} };
    if (carried != .bool and carried != .null) {
        try failRun(reducer, "pi_invalid_event", "tool end carried a non-boolean isError");
        return;
    }
    tool.result = memberOf(event, "result");
    tool.terminal = true;
    const failed = carried == .bool and carried.bool;
    if (failed) {
        var payload = try toolPayload(reducer, tool, false, false, false);
        const err = try reducer.object();
        try err.put(reducer.arena, "code", Reducer.str("pi_tool_failed"));
        try err.put(reducer.arena, "message", Reducer.str("Pi tool execution failed"));
        try payload.object.put(reducer.arena, "error", .{ .object = err.* });
        _ = try reducer.emitReplying("action.call.failed", payload, false, tool.started_event);
        return;
    }
    _ = try reducer.emitReplying("action.call.completed", try toolPayload(reducer, tool, false, false, true), false, tool.started_event);
}

const Part = struct { kind: []const u8, text: []const u8 };

fn integerMember(value: std.json.Value, name: []const u8) ?i64 {
    const found = memberOf(value, name) orelse return null;
    return switch (found) {
        .integer => |n| n,
        .null => 0,
        else => null,
    };
}

fn decodeProviderEvent(raw: std.json.Value) !?Part {
    if (raw != .object) return Error.InvalidFrame;
    const kind_value = raw.object.get("type") orelse return Error.InvalidFrame;
    const kind: []const u8 = switch (kind_value) {
        .string => |s| s,
        else => return Error.InvalidFrame,
    };
    if (kind.len == 0) return Error.InvalidFrame;
    if (std.mem.eql(u8, kind, "text_delta") or std.mem.eql(u8, kind, "thinking_delta")) {
        try closedMembers(raw, &.{ "type", "contentIndex", "delta" });
        const delta_value = raw.object.get("delta") orelse return Error.InvalidFrame;
        const delta: []const u8 = switch (delta_value) {
            .string => |s| s,
            .null => "",
            else => return Error.InvalidFrame,
        };
        try requireIndex(raw);
        if (std.mem.eql(u8, kind, "text_delta")) return .{ .kind = "text", .text = delta };
        return .{ .kind = "reasoning", .text = delta };
    }
    if (std.mem.eql(u8, kind, "start")) {
        try closedMembers(raw, &.{"type"});
        return null;
    }
    if (std.mem.eql(u8, kind, "text_start") or std.mem.eql(u8, kind, "thinking_start")) {
        try closedMembers(raw, &.{ "type", "contentIndex" });
        try requireIndex(raw);
        return null;
    }
    if (std.mem.eql(u8, kind, "text_end") or std.mem.eql(u8, kind, "thinking_end")) {
        try closedMembers(raw, &.{ "type", "contentIndex", "content" });
        try requireMembers(raw, &.{"content"});
        try typedStrings(raw, &.{"content"});
        try requireIndex(raw);
        return null;
    }
    if (std.mem.eql(u8, kind, "toolcall_start")) {
        try closedMembers(raw, &.{ "type", "contentIndex", "id", "toolName" });
        try requireMembers(raw, &.{ "id", "toolName" });
        try typedStrings(raw, &.{ "id", "toolName" });
        try requireIndex(raw);
        return null;
    }
    if (std.mem.eql(u8, kind, "toolcall_end")) {
        try closedMembers(raw, &.{ "type", "contentIndex", "toolCall" });
        try requireMembers(raw, &.{"toolCall"});
        try validateToolCallShape(raw.object.get("toolCall").?);
        try requireIndex(raw);
        return null;
    }
    if (std.mem.eql(u8, kind, "toolcall_delta")) {
        try closedMembers(raw, &.{ "type", "contentIndex", "delta" });
        try requireMembers(raw, &.{"delta"});
        try typedStrings(raw, &.{"delta"});
        try requireIndex(raw);
        return null;
    }
    if (std.mem.eql(u8, kind, "done")) {
        try closedMembers(raw, &.{ "type", "reason", "message" });
        try requireMembers(raw, &.{ "reason", "message" });
        try typedStrings(raw, &.{"reason"});
        return null;
    }
    if (std.mem.eql(u8, kind, "error")) {
        try closedMembers(raw, &.{ "type", "reason", "error" });
        try requireMembers(raw, &.{ "reason", "error" });
        try typedStrings(raw, &.{"reason"});
        return null;
    }
    return Error.InvalidFrame;
}

fn applyMessageUpdate(reducer: *Reducer, event: std.json.Value) !void {
    const carried = event.object.get("assistantMessageEvent") orelse {
        try failRun(reducer, "pi_invalid_message_update", "message_update carried no assistant event");
        return;
    };
    const part = decodeProviderEvent(carried) catch {
        try failRun(reducer, "pi_invalid_message_update", "message_update carried an undecodable assistant event");
        return;
    };
    const decoded = part orelse return;
    if (std.mem.eql(u8, decoded.kind, "text")) try reducer.text.appendSlice(reducer.arena, decoded.text);
    if (std.mem.eql(u8, decoded.kind, "reasoning")) try reducer.reasoning.appendSlice(reducer.arena, decoded.text);
    const shape = try reducer.object();
    try shape.put(reducer.arena, "type", Reducer.str(decoded.kind));
    try shape.put(reducer.arena, decoded.kind, Reducer.str(decoded.text));
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "message_id", Reducer.str(reducer.message_id));
    try payload.put(reducer.arena, "part", .{ .object = shape.* });
    try reducer.emit("content.delta", .{ .object = payload.* }, false);
}

pub const participant = "user";

fn statusUpdate(reducer: *Reducer, status: []const u8, pending_input: []const u8) !void {
    const updated_at = reducer.counters.nextTick();
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "status", Reducer.str(status));
    if (pending_input.len != 0) try payload.put(reducer.arena, "pending_user_input_id", Reducer.str(pending_input));
    try payload.put(reducer.arena, "updated_at_ms", .{ .integer = updated_at });
    try reducer.emit("run.status.updated", .{ .object = payload.* }, false);
}

pub fn cancel(reducer: *Reducer) !void {
    if (reducer.terminal or reducer.cancel_intent) return;
    reducer.cancel_intent = true;
    if (!reducer.started) return;
    try statusUpdate(reducer, "cancelling", "");
}

pub fn transportFailed(reducer: *Reducer, message: []const u8) !void {
    if (reducer.terminal) return;
    if (!reducer.started) {
        reducer.terminal = true;
        return;
    }
    try settleChildren(reducer, true);
    const err = try reducer.object();
    try err.put(reducer.arena, "code", Reducer.str("pi_process_exit"));
    try err.put(reducer.arena, "message", Reducer.str(message));
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "error", .{ .object = err.* });
    try payload.put(reducer.arena, "settled_by", Reducer.str("inferred"));
    try reducer.emit("run.failed", .{ .object = payload.* }, true);
}

const interactive_methods = [_][]const u8{ "select", "input", "editor", "confirm" };

pub fn applyExtension(reducer: *Reducer, request: std.json.Value) !void {
    if (reducer.terminal) return;
    if (!reducer.started) {
        try reducer.pending_ui.append(reducer.arena, request);
        return;
    }
    const method = textOf(request, "method");
    if (!listed(&interactive_methods, method)) return;
    const title = textOf(request, "title");
    const message = textOf(request, "message");
    const question = try reducer.object();
    try question.put(reducer.arena, "id", Reducer.str("value"));
    if (std.mem.eql(u8, method, "select")) {
        const options = memberOf(request, "options") orelse std.json.Value{ .null = {} };
        if (options != .array or options.array.items.len == 0) {
            try failRun(reducer, "pi_invalid_extension", "select extension offered no options");
            return;
        }
        var listed_options = std.ArrayList(std.json.Value).empty;
        for (options.array.items, 0..) |option, at| {
            const label: []const u8 = switch (option) {
                .string => |s| s,
                else => "",
            };
            if (label.len == 0) {
                try failRun(reducer, "pi_invalid_extension", "select extension offered an empty option label");
                return;
            }
            const shape = try reducer.object();
            try shape.put(reducer.arena, "id", Reducer.str(try std.fmt.allocPrint(reducer.arena, "option-{d}", .{at + 1})));
            try shape.put(reducer.arena, "label", Reducer.str(label));
            try listed_options.append(reducer.arena, .{ .object = shape.* });
        }
        try question.put(reducer.arena, "prompt", Reducer.str(title));
        try question.put(reducer.arena, "kind", Reducer.str("single_choice"));
        try question.put(reducer.arena, "required", .{ .bool = true });
        try question.put(reducer.arena, "options", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try listed_options.toOwnedSlice(reducer.arena)) });
    } else if (std.mem.eql(u8, method, "confirm")) {
        var listed_options = std.ArrayList(std.json.Value).empty;
        for ([_][2][]const u8{ .{ "yes", "Yes" }, .{ "no", "No" } }) |pair| {
            const shape = try reducer.object();
            try shape.put(reducer.arena, "id", Reducer.str(pair[0]));
            try shape.put(reducer.arena, "label", Reducer.str(pair[1]));
            try listed_options.append(reducer.arena, .{ .object = shape.* });
        }
        try question.put(reducer.arena, "prompt", Reducer.str(message));
        try question.put(reducer.arena, "kind", Reducer.str("single_choice"));
        try question.put(reducer.arena, "required", .{ .bool = true });
        try question.put(reducer.arena, "options", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try listed_options.toOwnedSlice(reducer.arena)) });
    } else {
        try question.put(reducer.arena, "prompt", Reducer.str(title));
        try question.put(reducer.arena, "kind", Reducer.str("text"));
        try question.put(reducer.arena, "required", .{ .bool = true });
    }
    const interaction = try reducer.arena.create(Interaction);
    interaction.* = .{
        .id = try reducer.counters.nextID(reducer.arena, "interaction"),
        .text = !std.mem.eql(u8, method, "select") and !std.mem.eql(u8, method, "confirm"),
        .offered = try offeredOptions(reducer, question),
    };
    try reducer.interactions.append(reducer.arena, interaction);
    var questions = std.ArrayList(std.json.Value).empty;
    try questions.append(reducer.arena, .{ .object = question.* });
    const payload = try reducer.object();
    try payload.put(reducer.arena, "interaction_id", Reducer.str(interaction.id));
    try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
    try payload.put(reducer.arena, "responded_by", Reducer.str(participant));
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "title", Reducer.str(title));
    try payload.put(reducer.arena, "description", Reducer.str(message));
    try payload.put(reducer.arena, "questions", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try questions.toOwnedSlice(reducer.arena)) });
    try payload.put(reducer.arena, "allow_cancel", .{ .bool = true });
    try reducer.emit("user.input.requested", .{ .object = payload.* }, false);
    try statusUpdate(reducer, "waiting_for_input", interaction.id);
}

fn offeredOptions(reducer: *Reducer, question: *std.json.ObjectMap) ![]const []const u8 {
    const listed_value = question.get("options") orelse return &.{};
    if (listed_value != .array) return &.{};
    var ids = std.ArrayList([]const u8).empty;
    for (listed_value.array.items) |option| {
        try ids.append(reducer.arena, textOf(option, "id"));
    }
    return try ids.toOwnedSlice(reducer.arena);
}

fn offers(interaction: *Interaction, id: []const u8) bool {
    for (interaction.offered) |candidate| {
        if (std.mem.eql(u8, candidate, id)) return true;
    }
    return false;
}

pub fn resolveExtension(reducer: *Reducer, interaction_id: []const u8, answer: []const u8) !void {
    const interaction = findInteraction(reducer, interaction_id) orelse return Error.InteractionNotFound;
    if (interaction.resolved or reducer.terminal) return Error.InteractionResolved;
    const selected = try reducer.object();
    try selected.put(reducer.arena, "question_id", Reducer.str("value"));
    if (interaction.text) {
        if (answer.len == 0) return Error.InvalidResolution;
        try selected.put(reducer.arena, "text", Reducer.str(answer));
    } else {
        if (!offers(interaction, answer)) return Error.InvalidResolution;
        var ids = std.ArrayList(std.json.Value).empty;
        try ids.append(reducer.arena, Reducer.str(answer));
        try selected.put(reducer.arena, "selected_option_ids", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try ids.toOwnedSlice(reducer.arena)) });
    }
    var answers = std.ArrayList(std.json.Value).empty;
    try answers.append(reducer.arena, .{ .object = selected.* });
    const payload = try reducer.object();
    try payload.put(reducer.arena, "interaction_id", Reducer.str(interaction.id));
    try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
    try payload.put(reducer.arena, "responded_by", Reducer.str(participant));
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "status", Reducer.str("submitted"));
    try payload.put(reducer.arena, "answers", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try answers.toOwnedSlice(reducer.arena)) });
    interaction.resolved = true;
    try reducer.emit("user.input.resolved", .{ .object = payload.* }, false);
    try statusUpdate(reducer, "running", "");
}

fn parse(arena: std.mem.Allocator, text: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{});
}

fn started(arena: std.mem.Allocator) !Reducer {
    var reducer = Reducer.init(arena);
    try open(&reducer);
    return reducer;
}

fn lastFailure(reducer: *Reducer) ?[]const u8 {
    if (reducer.emitted.items.len == 0) return null;
    const last = reducer.emitted.items[reducer.emitted.items.len - 1];
    const kind = textOf(last, "type");
    if (!std.mem.eql(u8, kind, "run.failed")) return null;
    const payload = memberOf(last, "payload") orelse return null;
    const err = memberOf(payload, "error") orelse return null;
    return textOf(err, "code");
}

fn expectRefusal(script: []const []const u8, code: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try started(arena.allocator());
    for (script) |line| {
        try apply(&reducer, try parse(arena.allocator(), line));
    }
    const raised = lastFailure(&reducer) orelse {
        std.debug.print("no refusal; emitted {d} envelopes\n", .{reducer.emitted.items.len});
        return error.NoRefusal;
    };
    try std.testing.expectEqualStrings(code, raised);
}

test "a second agent_start is a lifecycle defect" {
    try expectRefusal(&.{"{\"type\":\"agent_start\"}"}, "pi_invalid_lifecycle");
}

test "agent_settled before any agent_start is a lifecycle defect" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(arena.allocator());
    _ = try reducer.counters.nextID(arena.allocator(), "message");
    reducer.run_id = try reducer.counters.nextID(arena.allocator(), "run");
    reducer.message_id = try reducer.counters.nextID(arena.allocator(), "message");
    try apply(&reducer, try parse(arena.allocator(), "{\"type\":\"agent_settled\"}"));
    try std.testing.expect(reducer.emitted.items.len == 0);
    try std.testing.expect(reducer.terminal);
}

test "an undecodable message_end is refused" {
    try expectRefusal(&.{"{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\"}}"}, "pi_invalid_message_end");
}

test "an event outside the pinned vocabulary is refused" {
    try expectRefusal(&.{"{\"type\":\"invented_event\"}"}, "pi_unknown_event");
}

test "a candidate message that will not decode is refused at settlement" {
    try expectRefusal(&.{
        "{\"type\":\"agent_end\",\"messages\":[{\"role\":\"assistant\"}],\"willRetry\":false}",
        "{\"type\":\"agent_settled\"}",
    }, "pi_invalid_final_message");
}

test "settling without an agent_end is refused" {
    try expectRefusal(&.{"{\"type\":\"agent_settled\"}"}, "pi_missing_agent_end");
}

test "settling with an agent_end carrying no assistant message is refused" {
    try expectRefusal(&.{
        "{\"type\":\"agent_end\",\"messages\":[],\"willRetry\":false}",
        "{\"type\":\"agent_settled\"}",
    }, "pi_missing_final_message");
}

test "a tool start missing its id or name is refused" {
    try expectRefusal(&.{"{\"type\":\"tool_execution_start\",\"toolCallId\":\"\",\"toolName\":\"grep\",\"args\":{}}"}, "pi_invalid_tool_lifecycle");
    try expectRefusal(&.{"{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"\",\"args\":{}}"}, "pi_invalid_tool_lifecycle");
}

test "a second start for one tool id is refused" {
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
    }, "pi_invalid_tool_lifecycle");
}

test "a tool update naming no start, or a different name, is refused" {
    try expectRefusal(&.{"{\"type\":\"tool_execution_update\",\"toolCallId\":\"t9\",\"toolName\":\"grep\",\"partialResult\":{}}"}, "pi_invalid_tool_lifecycle");
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_update\",\"toolCallId\":\"t1\",\"toolName\":\"other\",\"partialResult\":{}}",
    }, "pi_invalid_tool_lifecycle");
}

test "a tool end naming no start, or one already settled, is refused" {
    try expectRefusal(&.{"{\"type\":\"tool_execution_end\",\"toolCallId\":\"t9\",\"toolName\":\"grep\",\"result\":{},\"isError\":false}"}, "pi_invalid_tool_lifecycle");
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"result\":{},\"isError\":false}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"result\":{},\"isError\":false}",
    }, "pi_invalid_tool_lifecycle");
}

test "an event carrying a member outside its pinned shape is refused" {
    try expectRefusal(&.{"{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"x\"},\"extra\":1}"}, "pi_invalid_event");
    try expectRefusal(&.{"{\"type\":\"agent_start\",\"extra\":1}"}, "pi_invalid_event");
    try expectRefusal(&.{"{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{},\"extra\":1}"}, "pi_invalid_event");
    try expectRefusal(&.{"{\"type\":\"agent_end\",\"messages\":[],\"willRetry\":false,\"extra\":1}"}, "pi_invalid_event");
    try expectRefusal(&.{"{\"type\":\"message_end\",\"message\":{},\"extra\":1}"}, "pi_invalid_event");
}

test "a tool frame omitting its payload member is refused" {
    try expectRefusal(&.{"{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\"}"}, "pi_invalid_tool_lifecycle");
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_update\",\"toolCallId\":\"t1\",\"toolName\":\"grep\"}",
    }, "pi_invalid_tool_lifecycle");
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"isError\":false}",
    }, "pi_invalid_tool_lifecycle");
}

test "a provider event failing its closed shape is refused" {
    try expectRefusal(&.{"{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"done\",\"reason\":\"stop\"}}"}, "pi_invalid_message_update");
    try expectRefusal(&.{"{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"toolcall_delta\",\"contentIndex\":0,\"delta\":\"x\",\"extra\":1}}"}, "pi_invalid_message_update");
}

test "a message_update carrying no assistant event is refused" {
    try expectRefusal(&.{"{\"type\":\"message_update\",\"usage\":{}}"}, "pi_invalid_message_update");
}

test "a message_update whose assistant event will not decode is refused" {
    try expectRefusal(&.{"{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":-1,\"delta\":\"x\"}}"}, "pi_invalid_message_update");
}

fn expectExtensionRefusal(request: []const u8, code: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try started(arena.allocator());
    try applyExtension(&reducer, try parse(arena.allocator(), request));
    const raised = lastFailure(&reducer) orelse return error.NoRefusal;
    try std.testing.expectEqualStrings(code, raised);
}

test "a select extension offering no options is refused" {
    try expectExtensionRefusal("{\"type\":\"extension_ui_request\",\"id\":\"ui-1\",\"method\":\"select\",\"title\":\"Pick\",\"options\":[]}", "pi_invalid_extension");
}

test "a select extension offering an empty option label is refused" {
    try expectExtensionRefusal("{\"type\":\"extension_ui_request\",\"id\":\"ui-1\",\"method\":\"select\",\"title\":\"Pick\",\"options\":[\"ok\",\"\"]}", "pi_invalid_extension");
}

fn settleChildren(reducer: *Reducer, cancelled: bool) !void {
    for (reducer.tools.items) |tool| {
        if (tool.terminal) continue;
        tool.terminal = true;
        var payload = try toolPayload(reducer, tool, false, false, true);
        if (cancelled) {
            _ = try reducer.emitReplying("action.call.cancelled", payload, false, tool.started_event);
            continue;
        }
        const err = try reducer.object();
        try err.put(reducer.arena, "code", Reducer.str("pi_incomplete_tool"));
        try err.put(reducer.arena, "message", Reducer.str("Pi run settled before tool completion"));
        try payload.object.put(reducer.arena, "error", .{ .object = err.* });
        _ = try reducer.emitReplying("action.call.failed", payload, false, tool.started_event);
    }
    for (reducer.interactions.items) |interaction| {
        if (interaction.resolved) continue;
        interaction.resolved = true;
        const payload = try reducer.object();
        try payload.put(reducer.arena, "interaction_id", Reducer.str(interaction.id));
        try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
        try payload.put(reducer.arena, "responded_by", Reducer.str(participant));
        try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
        try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
        try payload.put(reducer.arena, "status", Reducer.str("cancelled"));
        try reducer.emit("user.input.resolved", .{ .object = payload.* }, false);
    }
}

fn textPart(reducer: *Reducer, kind: []const u8, body: []const u8) !std.json.Value {
    const shape = try reducer.object();
    try shape.put(reducer.arena, "type", Reducer.str(kind));
    try shape.put(reducer.arena, kind, Reducer.str(body));
    return .{ .object = shape.* };
}

fn fallbackContent(reducer: *Reducer) !std.json.Value {
    var parts = std.ArrayList(std.json.Value).empty;
    if (reducer.reasoning.items.len != 0) try parts.append(reducer.arena, try textPart(reducer, "reasoning", reducer.reasoning.items));
    if (reducer.text.items.len != 0) try parts.append(reducer.arena, try textPart(reducer, "text", reducer.text.items));
    if (parts.items.len == 0) return Reducer.str("");
    if (parts.items.len == 1 and std.mem.eql(u8, textOf(parts.items[0], "type"), "text")) {
        return Reducer.str(textOf(parts.items[0], "text"));
    }
    return .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try parts.toOwnedSlice(reducer.arena)) };
}

fn wireContentOf(reducer: *Reducer, raw: std.json.Value) !std.json.Value {
    if (raw == .null) return Reducer.str("");
    if (raw == .string) return raw;
    if (raw != .array) return Error.InvalidFrame;
    var parts = std.ArrayList(std.json.Value).empty;
    for (raw.array.items) |part| {
        if (part != .object) return Error.InvalidFrame;
        const kind = textOf(part, "type");
        if (std.mem.eql(u8, kind, "text")) {
            try closedMembers(part, &.{ "type", "text", "textSignature" });
            try parts.append(reducer.arena, try textPart(reducer, "text", textOf(part, "text")));
        } else if (std.mem.eql(u8, kind, "thinking")) {
            try closedMembers(part, &.{ "type", "thinking", "thinkingSignature", "redacted" });
            try parts.append(reducer.arena, try textPart(reducer, "reasoning", textOf(part, "thinking")));
        } else if (std.mem.eql(u8, kind, "toolCall")) {
            try closedMembers(part, &.{ "type", "id", "name", "arguments", "thoughtSignature", "namespace" });
            const tool = findTool(reducer, textOf(part, "id")) orelse return Error.InvalidFrame;
            const name = textOf(part, "name");
            if (name.len == 0) return Error.InvalidFrame;
            const shape = try reducer.object();
            try shape.put(reducer.arena, "type", Reducer.str("tool_call"));
            try shape.put(reducer.arena, "tool_call_id", Reducer.str(tool.id));
            try shape.put(reducer.arena, "name", Reducer.str(name));
            if (part.object.get("arguments")) |arguments| try shape.put(reducer.arena, "arguments_json", arguments);
            try parts.append(reducer.arena, .{ .object = shape.* });
        } else return Error.InvalidFrame;
    }
    if (parts.items.len == 0) return fallbackContent(reducer);
    return .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try parts.toOwnedSlice(reducer.arena)) };
}

fn replay(arena: std.mem.Allocator, script: []const []const u8) !Reducer {
    var reducer = try started(arena);
    for (script) |line| {
        try apply(&reducer, try parse(arena, line));
    }
    return reducer;
}

fn typesOf(reducer: *Reducer, out: [][]const u8) [][]const u8 {
    for (reducer.emitted.items, 0..) |envelope, at| {
        if (at >= out.len) break;
        out[at] = textOf(envelope, "type");
    }
    return out[0..@min(out.len, reducer.emitted.items.len)];
}

fn finalContent(reducer: *Reducer) ?std.json.Value {
    for (reducer.emitted.items) |envelope| {
        if (!std.mem.eql(u8, textOf(envelope, "type"), "run.completed")) continue;
        const payload = memberOf(envelope, "payload") orelse return null;
        const response = memberOf(payload, "final_response") orelse return null;
        return memberOf(response, "content");
    }
    return null;
}

const settled_text = "{\"type\":\"agent_settled\"}";

fn agentEndWith(comptime content: []const u8) []const u8 {
    return "{\"type\":\"agent_end\",\"willRetry\":false,\"messages\":[{\"role\":\"assistant\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"stop\",\"timestamp\":1,\"content\":" ++ content ++ "}]}";
}

test "a thinking part becomes a reasoning part rather than travelling as itself" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try replay(arena.allocator(), &.{
        agentEndWith("[{\"type\":\"thinking\",\"thinking\":\"why\"},{\"type\":\"text\",\"text\":\"so\"}]"),
        settled_text,
    });
    const content = finalContent(&reducer) orelse return error.NoCompletion;
    try std.testing.expect(content == .array);
    try std.testing.expectEqualStrings("reasoning", textOf(content.array.items[0], "type"));
    try std.testing.expectEqualStrings("why", textOf(content.array.items[0], "reasoning"));
    try std.testing.expectEqualStrings("text", textOf(content.array.items[1], "type"));
}

test "a final toolCall part carries the OAP tool-call id, not the native one" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try replay(arena.allocator(), &.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"result\":{},\"isError\":false}",
        agentEndWith("[{\"type\":\"toolCall\",\"id\":\"t1\",\"name\":\"grep\",\"arguments\":{}}]"),
        settled_text,
    });
    const content = finalContent(&reducer) orelse return error.NoCompletion;
    try std.testing.expectEqualStrings("tool_call", textOf(content.array.items[0], "type"));
    try std.testing.expectEqualStrings("tool-call-6", textOf(content.array.items[0], "tool_call_id"));
}

test "a final toolCall part with an empty name is refused, not emitted without one" {
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"result\":{},\"isError\":false}",
        agentEndWith("[{\"type\":\"toolCall\",\"id\":\"t1\",\"name\":\"\",\"arguments\":{}}]"),
        settled_text,
    }, "pi_invalid_final_message");
}

test "a final message referencing a tool the run never started is refused" {
    try expectRefusal(&.{
        agentEndWith("[{\"type\":\"toolCall\",\"id\":\"missing\",\"name\":\"grep\",\"arguments\":{}}]"),
        settled_text,
    }, "pi_invalid_final_message");
}

test "an empty final content falls back to the accumulated deltas" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try replay(arena.allocator(), &.{
        "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"thinking_delta\",\"contentIndex\":0,\"delta\":\"why\"}}",
        "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":1,\"delta\":\"so\"}}",
        agentEndWith("[]"),
        settled_text,
    });
    const content = finalContent(&reducer) orelse return error.NoCompletion;
    try std.testing.expect(content == .array);
    try std.testing.expectEqualStrings("reasoning", textOf(content.array.items[0], "type"));
    try std.testing.expectEqualStrings("so", textOf(content.array.items[1], "text"));
}

test "a settlement with an open tool fails it rather than leaving it dangling" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try replay(arena.allocator(), &.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        agentEndWith("\"done\""),
        settled_text,
    });
    var buffer: [8][]const u8 = undefined;
    const kinds = typesOf(&reducer, &buffer);
    try std.testing.expectEqualStrings("action.call.failed", kinds[kinds.len - 2]);
    try std.testing.expectEqualStrings("run.completed", kinds[kinds.len - 1]);
}

test "a cancellation with an open tool cancels it rather than failing it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try started(arena.allocator());
    try apply(&reducer, try parse(arena.allocator(), "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}"));
    try cancel(&reducer);
    try apply(&reducer, try parse(arena.allocator(), settled_text));
    var buffer: [8][]const u8 = undefined;
    const kinds = typesOf(&reducer, &buffer);
    try std.testing.expectEqualStrings("action.call.cancelled", kinds[kinds.len - 2]);
    try std.testing.expectEqualStrings("run.cancelled", kinds[kinds.len - 1]);
}

test "a settlement with an unresolved interaction cancels it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try started(arena.allocator());
    try applyExtension(&reducer, try parse(arena.allocator(), "{\"type\":\"extension_ui_request\",\"id\":\"ui-1\",\"method\":\"confirm\",\"title\":\"Go?\",\"message\":\"Continue\"}"));
    try apply(&reducer, try parse(arena.allocator(), agentEndWith("\"done\"")));
    try apply(&reducer, try parse(arena.allocator(), settled_text));
    var buffer: [8][]const u8 = undefined;
    const kinds = typesOf(&reducer, &buffer);
    try std.testing.expectEqualStrings("user.input.resolved", kinds[kinds.len - 2]);
}

test "a message_end error carries its stop reason into the settlement" {
    try expectRefusal(&.{
        "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"error\",\"timestamp\":1,\"content\":\"boom\"}}",
        "{\"type\":\"agent_end\",\"willRetry\":false,\"messages\":[]}",
        settled_text,
    }, "pi_agent_failed");
}

test "a cancel recorded before the start publishes the cancelling status after it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(arena.allocator());
    _ = try reducer.counters.nextID(arena.allocator(), "message");
    reducer.run_id = try reducer.counters.nextID(arena.allocator(), "run");
    reducer.message_id = try reducer.counters.nextID(arena.allocator(), "message");
    try cancel(&reducer);
    try std.testing.expect(reducer.emitted.items.len == 0);
    try apply(&reducer, try parse(arena.allocator(), "{\"type\":\"agent_start\"}"));
    var buffer: [4][]const u8 = undefined;
    const kinds = typesOf(&reducer, &buffer);
    try std.testing.expectEqualStrings("run.started", kinds[0]);
    try std.testing.expectEqualStrings("run.status.updated", kinds[1]);
}

fn payloadOf(reducer: *Reducer, kind: []const u8) ?std.json.Value {
    var index = reducer.emitted.items.len;
    while (index > 0) {
        index -= 1;
        if (!std.mem.eql(u8, textOf(reducer.emitted.items[index], "type"), kind)) continue;
        return memberOf(reducer.emitted.items[index], "payload");
    }
    return null;
}

fn countOf(reducer: *Reducer, kind: []const u8) usize {
    var seen: usize = 0;
    for (reducer.emitted.items) |envelope| {
        if (std.mem.eql(u8, textOf(envelope, "type"), kind)) seen += 1;
    }
    return seen;
}

const text_request = "{\"type\":\"extension_ui_request\",\"id\":\"ui-1\",\"method\":\"input\",\"title\":\"Name?\",\"message\":\"\"}";
const confirm_request = "{\"type\":\"extension_ui_request\",\"id\":\"ui-2\",\"method\":\"confirm\",\"title\":\"Go?\",\"message\":\"Continue\"}";

test "a toolResult message carries members the assistant shape does not admit" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    const result = "{\"role\":\"toolResult\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"content\":\"ok\",\"isError\":false,\"timestamp\":1,\"details\":{},\"usage\":{},\"addedToolNames\":[]}";
    try std.testing.expect(try decodeWireMessage(try parse(a, result)) == null);

    const crossed = "{\"role\":\"user\",\"content\":\"hi\",\"timestamp\":1,\"api\":\"a\"}";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, crossed)));

    const user_with_tool_member = "{\"role\":\"user\",\"content\":\"hi\",\"timestamp\":1,\"toolName\":\"grep\"}";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, user_with_tool_member)));

    const assistant_with_tool_member = "{\"role\":\"assistant\",\"content\":\"hi\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1,\"toolCallId\":\"t1\"}";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, assistant_with_tool_member)));
}

test "a null final content is empty text, and an empty part list is the delta fallback" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try apply(&reducer, try parse(a, "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"streamed\"}}"));
    try apply(&reducer, try parse(a, "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1,\"content\":null}}"));
    try apply(&reducer, try parse(a, agentEndWith("null")));
    try apply(&reducer, try parse(a, settled_text));
    try std.testing.expect(lastFailure(&reducer) == null);
    const payload = payloadOf(&reducer, "run.completed") orelse return error.RunDidNotComplete;
    const response = memberOf(payload, "final_response") orelse return error.NoFinalResponse;
    const content = memberOf(response, "content") orelse return error.NoContent;
    try std.testing.expect(content == .string);
    try std.testing.expectEqualStrings("", content.string);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    const b = second.allocator();
    var fell_back = try started(b);
    try apply(&fell_back, try parse(b, "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"text_delta\",\"contentIndex\":0,\"delta\":\"streamed\"}}"));
    try apply(&fell_back, try parse(b, "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1,\"content\":[]}}"));
    try apply(&fell_back, try parse(b, agentEndWith("[]")));
    try apply(&fell_back, try parse(b, settled_text));
    const fallback_payload = payloadOf(&fell_back, "run.completed") orelse return error.RunDidNotComplete;
    const fallback_response = memberOf(fallback_payload, "final_response") orelse return error.NoFinalResponse;
    const fallback_content = memberOf(fallback_response, "content") orelse return error.NoContent;
    try std.testing.expectEqualStrings("streamed", fallback_content.string);
}

test "a tool end whose isError is not a boolean is refused" {
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"result\":\"r\",\"isError\":\"true\"}",
    }, "pi_invalid_event");
}

test "a process exit with an open tool settles it before the failure" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try apply(&reducer, try parse(a, "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}"));
    try applyExtension(&reducer, try parse(a, confirm_request));
    try transportFailed(&reducer, "gone");
    var buffer: [16][]const u8 = undefined;
    const kinds = typesOf(&reducer, &buffer);
    try std.testing.expectEqualStrings("run.failed", kinds[kinds.len - 1]);
    try std.testing.expectEqualStrings("user.input.resolved", kinds[kinds.len - 2]);
    try std.testing.expectEqualStrings("action.call.cancelled", kinds[kinds.len - 3]);
}

test "a process exit before the start settles the run without emitting" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(arena.allocator());
    try transportFailed(&reducer, "gone");
    try std.testing.expect(reducer.terminal);
    try std.testing.expect(reducer.emitted.items.len == 0);
}

test "a tool end whose isError will not decode leaves the tool open for settlement" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try apply(&reducer, try parse(a, "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}"));
    try apply(&reducer, try parse(a, "{\"type\":\"tool_execution_end\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"result\":\"r\",\"isError\":\"true\"}"));
    var buffer: [16][]const u8 = undefined;
    const kinds = typesOf(&reducer, &buffer);
    try std.testing.expectEqualStrings("run.failed", kinds[kinds.len - 1]);
    try std.testing.expectEqualStrings("action.call.cancelled", kinds[kinds.len - 2]);
}

test "an extension request before the start is replayed, not dropped" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = Reducer.init(a);
    try applyExtension(&reducer, try parse(a, confirm_request));
    try std.testing.expect(countOf(&reducer, "user.input.requested") == 0);
    try open(&reducer);
    try std.testing.expect(countOf(&reducer, "user.input.requested") == 1);
}

test "a text interaction is answered with text, never with an option id" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, text_request));
    try resolveExtension(&reducer, reducer.interactions.items[0].id, "Ada");

    const payload = payloadOf(&reducer, "user.input.resolved") orelse return error.NoResolution;
    const answers = memberOf(payload, "answers") orelse return error.NoAnswers;
    try std.testing.expect(answers == .array and answers.array.items.len == 1);
    const answer = answers.array.items[0];
    try std.testing.expectEqualStrings("Ada", textOf(answer, "text"));
    try std.testing.expect(memberOf(answer, "selected_option_ids") == null);
}

test "a second interaction is not born resolved, and a resolved one is not resolved twice" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, confirm_request));
    const first = reducer.interactions.items[0].id;
    try resolveExtension(&reducer, first, "yes");
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 1);

    try std.testing.expectError(Error.InteractionResolved, resolveExtension(&reducer, first, "yes"));
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 1);

    try applyExtension(&reducer, try parse(a, confirm_request));
    try apply(&reducer, try parse(a, agentEndWith("\"done\"")));
    try apply(&reducer, try parse(a, settled_text));
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 2);
}

test "a second pending interaction is not lost by the first" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, confirm_request));
    try applyExtension(&reducer, try parse(a, text_request));
    try std.testing.expect(countOf(&reducer, "user.input.requested") == 2);

    try apply(&reducer, try parse(a, agentEndWith("\"done\"")));
    try apply(&reducer, try parse(a, settled_text));
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 2);
}

test "a resolution answers the interaction the caller named, not the oldest" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, confirm_request));
    try applyExtension(&reducer, try parse(a, text_request));
    try resolveExtension(&reducer, reducer.interactions.items[1].id, "typed");

    const payload = payloadOf(&reducer, "user.input.resolved") orelse return error.NoResolution;
    try std.testing.expectEqualStrings(reducer.interactions.items[1].id, textOf(payload, "interaction_id"));
    try std.testing.expect(!reducer.interactions.items[0].resolved);
    try std.testing.expect(reducer.interactions.items[1].resolved);

    try std.testing.expectError(Error.InteractionNotFound, resolveExtension(&reducer, "interaction-nonesuch", "yes"));
}

test "a stop reason that is not a string is refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const message = "{\"role\":\"assistant\",\"content\":\"hi\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":true,\"timestamp\":1}";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, message)));

    const timestamp = "{\"role\":\"assistant\",\"content\":\"hi\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":\"1\"}";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, timestamp)));
}

test "an agent_end whose messages are not a list is refused" {
    try expectRefusal(&.{
        "{\"type\":\"agent_end\",\"willRetry\":false,\"messages\":{\"0\":{}}}",
    }, "pi_invalid_event");
}

test "a non-delta provider event with a bad content index is refused" {
    try expectRefusal(&.{
        "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"text_start\",\"contentIndex\":-1}}",
    }, "pi_invalid_message_update");
    try expectRefusal(&.{
        "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":{\"type\":\"thinking_start\",\"contentIndex\":\"0\"}}",
    }, "pi_invalid_message_update");
}

test "a content block member of the wrong type is refused, on every block kind" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    const refused = [_][]const u8{
        "{\"role\":\"user\",\"timestamp\":1,\"content\":[{\"type\":\"text\",\"text\":7}]}",
        "{\"role\":\"user\",\"timestamp\":1,\"content\":[{\"type\":\"text\",\"text\":\"x\",\"textSignature\":7}]}",
        "{\"role\":\"user\",\"timestamp\":1,\"content\":[{\"type\":\"image\",\"data\":7,\"mimeType\":\"image/png\"}]}",
        "{\"role\":\"assistant\",\"content\":[{\"type\":\"thinking\",\"thinking\":\"x\",\"redacted\":\"yes\"}],\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1}",
        "{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":7,\"name\":\"grep\",\"arguments\":{}}],\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1}",
    };
    for (refused) |text| {
        try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, text)));
    }

    const accepted = "{\"role\":\"assistant\",\"content\":[{\"type\":\"toolCall\",\"id\":\"c\",\"name\":\"grep\",\"arguments\":{}}],\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1}";
    _ = try decodeWireMessage(try parse(a, accepted));
}

test "an agent_end retry flag that is not a boolean is refused" {
    try expectRefusal(&.{
        "{\"type\":\"agent_end\",\"willRetry\":\"yes\",\"messages\":[]}",
    }, "pi_invalid_event");
}

fn expectUpdateRefusal(comptime event_text: []const u8) !void {
    const line = "{\"type\":\"message_update\",\"usage\":{},\"assistantMessageEvent\":" ++ event_text ++ "}";
    try expectRefusal(&.{line}, "pi_invalid_message_update");
}

test "a non-delta provider payload of the wrong type is refused" {
    try expectUpdateRefusal("{\"type\":\"text_end\",\"contentIndex\":0,\"content\":7}");
    try expectUpdateRefusal("{\"type\":\"toolcall_start\",\"contentIndex\":0,\"id\":7,\"toolName\":\"grep\"}");
    try expectUpdateRefusal("{\"type\":\"toolcall_start\",\"contentIndex\":0,\"id\":\"c\",\"toolName\":7}");
    try expectUpdateRefusal("{\"type\":\"toolcall_delta\",\"contentIndex\":0,\"delta\":7}");
    try expectUpdateRefusal("{\"type\":\"done\",\"reason\":7,\"message\":{}}");
    try expectUpdateRefusal("{\"type\":\"error\",\"reason\":7,\"error\":{}}");
    try expectUpdateRefusal("{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":{\"type\":\"toolCall\",\"id\":7,\"name\":\"grep\",\"arguments\":{}}}");
}

test "a tool result whose isError is not a boolean is refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const bad = "{\"role\":\"toolResult\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"content\":\"ok\",\"isError\":\"true\",\"timestamp\":1}";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, bad)));

    const good = "{\"role\":\"toolResult\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"content\":\"ok\",\"isError\":false,\"timestamp\":1}";
    try std.testing.expect(try decodeWireMessage(try parse(a, good)) == null);
}

test "a retrying agent_end still has its message list checked" {
    try expectRefusal(&.{
        "{\"type\":\"agent_end\",\"willRetry\":true,\"messages\":{}}",
    }, "pi_invalid_event");
}

test "the structured optionals on a message carry their declared shapes" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const head = "{\"role\":\"assistant\",\"content\":\"hi\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1";

    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, head ++ ",\"diagnostics\":{}}")));
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, head ++ ",\"endTurn\":\"yes\"}")));
    _ = try decodeWireMessage(try parse(a, head ++ ",\"diagnostics\":[],\"endTurn\":true}"));

    const result_head = "{\"role\":\"toolResult\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"content\":\"ok\",\"isError\":false,\"timestamp\":1";
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, result_head ++ ",\"addedToolNames\":{}}")));
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, result_head ++ ",\"addedToolNames\":[7]}")));
    try std.testing.expect(try decodeWireMessage(try parse(a, result_head ++ ",\"addedToolNames\":[\"grep\"]}")) == null);
}

test "a null agent_end message list is a nil slice, not a defect" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try apply(&reducer, try parse(a, "{\"type\":\"message_end\",\"message\":{\"role\":\"assistant\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{},\"stopReason\":\"end_turn\",\"timestamp\":1,\"content\":\"earlier\"}}"));
    try apply(&reducer, try parse(a, "{\"type\":\"agent_end\",\"willRetry\":false,\"messages\":null}"));
    try apply(&reducer, try parse(a, settled_text));

    try std.testing.expect(lastFailure(&reducer) == null);
    const payload = payloadOf(&reducer, "run.completed") orelse return error.RunDidNotComplete;
    const response = memberOf(payload, "final_response") orelse return error.NoFinalResponse;
    const content = memberOf(response, "content") orelse return error.NoContent;
    try std.testing.expectEqualStrings("earlier", content.string);
}

test "a nested tool call block carries its own type as a string" {
    try expectUpdateRefusal("{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":{\"type\":7,\"id\":\"c\",\"name\":\"grep\",\"arguments\":{}}}");
}

const select_request = "{\"type\":\"extension_ui_request\",\"id\":\"ui-3\",\"method\":\"select\",\"title\":\"Pick\",\"message\":\"\",\"options\":[\"alpha\",\"beta\"]}";

test "a choice answer outside the advertised options is refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    var confirm = try started(a);
    try applyExtension(&confirm, try parse(a, confirm_request));
    try std.testing.expectError(Error.InvalidResolution, resolveExtension(&confirm, confirm.interactions.items[0].id, "maybe"));
    try std.testing.expect(countOf(&confirm, "user.input.resolved") == 0);
    try std.testing.expect(!confirm.interactions.items[0].resolved);
    try resolveExtension(&confirm, confirm.interactions.items[0].id, "no");
    try std.testing.expect(countOf(&confirm, "user.input.resolved") == 1);

    var choice = try started(a);
    try applyExtension(&choice, try parse(a, select_request));
    try std.testing.expectError(Error.InvalidResolution, resolveExtension(&choice, choice.interactions.items[0].id, "option-99"));
    try resolveExtension(&choice, choice.interactions.items[0].id, "option-2");
    const payload = payloadOf(&choice, "user.input.resolved") orelse return error.NoResolution;
    const answers = memberOf(payload, "answers") orelse return error.NoAnswers;
    const ids = memberOf(answers.array.items[0], "selected_option_ids") orelse return error.NoIds;
    try std.testing.expectEqualStrings("option-2", ids.array.items[0].string);
}

test "a text interaction still takes any answer" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, text_request));
    try resolveExtension(&reducer, reducer.interactions.items[0].id, "anything at all");
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 1);
}

test "an empty answer to a text interaction is refused, not silently dropped" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, text_request));
    try std.testing.expectError(Error.InvalidResolution, resolveExtension(&reducer, reducer.interactions.items[0].id, ""));
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 0);
    try std.testing.expect(!reducer.interactions.items[0].resolved);
    try resolveExtension(&reducer, reducer.interactions.items[0].id, "Ada");
    try std.testing.expect(countOf(&reducer, "user.input.resolved") == 1);
}

test "a null entry in addedToolNames is the empty string, as the oracle decodes it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const head = "{\"role\":\"toolResult\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"content\":\"ok\",\"isError\":false,\"timestamp\":1";
    try std.testing.expect(try decodeWireMessage(try parse(a, head ++ ",\"addedToolNames\":[null]}")) == null);
    try std.testing.expect(try decodeWireMessage(try parse(a, head ++ ",\"addedToolNames\":[\"a\",null]}")) == null);
    try std.testing.expectError(Error.InvalidFrame, decodeWireMessage(try parse(a, head ++ ",\"addedToolNames\":[7]}")));
}

test "a tool event identifier that is not a string is an event defect, not a lifecycle one" {
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":7,\"args\":{}}",
    }, "pi_invalid_event");
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":7,\"toolName\":\"grep\",\"args\":{}}",
    }, "pi_invalid_event");
    try expectRefusal(&.{
        "{\"type\":\"tool_execution_start\",\"toolCallId\":\"t1\",\"toolName\":\"grep\",\"args\":{}}",
        "{\"type\":\"tool_execution_end\",\"toolCallId\":7,\"toolName\":\"grep\",\"result\":\"r\",\"isError\":false}",
    }, "pi_invalid_event");
}

test "a resolution after settlement is refused rather than silently accepted" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    try applyExtension(&reducer, try parse(a, confirm_request));
    const id = reducer.interactions.items[0].id;
    try apply(&reducer, try parse(a, agentEndWith("\"done\"")));
    try apply(&reducer, try parse(a, settled_text));

    try std.testing.expectError(Error.InteractionResolved, resolveExtension(&reducer, id, "yes"));
    try std.testing.expectError(Error.InteractionNotFound, resolveExtension(&reducer, "interaction-nonesuch", "yes"));
}

fn expectUpdateAccepted(nested: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try started(a);
    const line = try std.fmt.allocPrint(a, "{{\"type\":\"message_update\",\"usage\":{{}},\"assistantMessageEvent\":{{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":{s}}}}}", .{nested});
    try apply(&reducer, try parse(a, line));
    try std.testing.expect(lastFailure(&reducer) == null);
}

test "a nested tool call carries whatever the harness sent, typed but not required" {
    for ([_][]const u8{
        "null",
        "{}",
        "{\"id\":\"c\"}",
        "{\"id\":\"c\",\"name\":\"grep\"}",
        "{\"arguments\":null}",
    }) |nested| try expectUpdateAccepted(nested);

    try expectUpdateRefusal("{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":7}");
    try expectUpdateRefusal("{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":[]}");
    try expectUpdateRefusal("{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":{\"bogus\":1}}");
    try expectUpdateRefusal("{\"type\":\"toolcall_end\",\"contentIndex\":0,\"toolCall\":{\"type\":\"toolCall\",\"id\":7,\"name\":\"grep\",\"arguments\":{}}}");
}

test "a final message tool call block still requires the three members a nested one does not" {
    for ([_][]const u8{
        "[{\"type\":\"toolCall\",\"name\":\"grep\",\"arguments\":{}}]",
        "[{\"type\":\"toolCall\",\"id\":\"t1\",\"arguments\":{}}]",
        "[{\"type\":\"toolCall\",\"id\":\"t1\",\"name\":\"grep\"}]",
    }) |content| {
        var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
        defer arena.deinit();
        const a = arena.allocator();
        var reducer = try started(a);
        const line = try std.fmt.allocPrint(a, "{{\"type\":\"message_end\",\"message\":{{\"role\":\"assistant\",\"api\":\"a\",\"provider\":\"p\",\"model\":\"m\",\"usage\":{{}},\"stopReason\":\"stop\",\"timestamp\":1,\"content\":{s}}}}}", .{content});
        try apply(&reducer, try parse(a, line));
        try std.testing.expectEqualStrings("pi_invalid_message_end", lastFailure(&reducer) orelse return error.NoRefusal);
    }
}
