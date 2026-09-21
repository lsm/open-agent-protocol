const std = @import("std");

pub const capability_revision = "pi-v0.85.1-oap-v1";
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";

pub const Error = error{
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
    final: ?std.json.Value = null,
    text: std.ArrayList(u8) = .empty,
    reasoning: std.ArrayList(u8) = .empty,
    pending: std.ArrayList(std.json.Value) = .empty,
    tools: std.ArrayList(*ToolState) = .empty,
    interaction_id: []const u8 = "",
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

const message_members = [_][]const u8{
    "role",          "content",      "api",                   "provider",
    "model",         "usage",        "stopReason",            "timestamp",
    "responseModel", "responseId",   "providerThinkingLevel", "diagnostics",
    "deferred",      "errorMessage", "rawStopReason",         "endTurn",
};

const assistant_required = [_][]const u8{ "content", "api", "provider", "model", "usage", "stopReason", "timestamp" };

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

pub fn decodeWireMessage(raw: std.json.Value) !?WireMessage {
    if (raw != .object) return Error.InvalidFrame;
    for (raw.object.keys()) |name| {
        if (!listed(&message_members, name)) return Error.InvalidFrame;
    }
    const role_value = raw.object.get("role") orelse return Error.InvalidFrame;
    const role: []const u8 = switch (role_value) {
        .string => |s| s,
        .null => "",
        else => return Error.InvalidFrame,
    };
    if (std.mem.eql(u8, role, "assistant")) {
        for (&assistant_required) |name| {
            if (raw.object.get(name) == null) return Error.InvalidFrame;
        }
        try validateWireContent(raw.object.get("content").?, false);
        return .{
            .content = raw.object.get("content"),
            .stop_reason = textOf(raw, "stopReason"),
            .error_message = textOf(raw, "errorMessage"),
        };
    }
    if (std.mem.eql(u8, role, "user")) {
        for (&[_][]const u8{ "content", "timestamp" }) |name| {
            if (raw.object.get(name) == null) return Error.InvalidFrame;
        }
        try validateWireContent(raw.object.get("content").?, true);
        return null;
    }
    if (std.mem.eql(u8, role, "toolResult")) {
        for (&[_][]const u8{ "toolCallId", "toolName", "content", "isError", "timestamp" }) |name| {
            if (raw.object.get(name) == null) return Error.InvalidFrame;
        }
        try validateWireContent(raw.object.get("content").?, true);
        return null;
    }
    return Error.InvalidFrame;
}

pub fn validateWireContent(raw: std.json.Value, allow_image: bool) !void {
    if (raw == .string) return;
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
            if (part.object.get("text") == null) return Error.InvalidFrame;
        } else if (std.mem.eql(u8, kind, "thinking")) {
            if (allow_image) return Error.InvalidFrame;
            try closedMembers(part, &.{ "type", "thinking", "thinkingSignature", "redacted" });
            if (part.object.get("thinking") == null) return Error.InvalidFrame;
        } else if (std.mem.eql(u8, kind, "toolCall")) {
            if (allow_image) return Error.InvalidFrame;
            try closedMembers(part, &.{ "type", "id", "name", "arguments", "thoughtSignature", "namespace" });
            for (&[_][]const u8{ "id", "name", "arguments" }) |name| {
                if (part.object.get(name) == null) return Error.InvalidFrame;
            }
        } else if (std.mem.eql(u8, kind, "image")) {
            if (!allow_image) return Error.InvalidFrame;
            try closedMembers(part, &.{ "type", "data", "mimeType" });
            for (&[_][]const u8{ "data", "mimeType" }) |name| {
                if (part.object.get(name) == null) return Error.InvalidFrame;
            }
        } else return Error.InvalidFrame;
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
        const queued = try reducer.pending.toOwnedSlice(reducer.arena);
        for (queued) |pending| {
            if (reducer.terminal) break;
            try apply(reducer, pending);
        }
        return;
    }
    if (std.mem.eql(u8, kind, "message_end")) {
        const carried = memberOf(event, "message") orelse return Error.InvalidFrame;
        const decoded = decodeWireMessage(carried) catch {
            try failRun(reducer, "pi_invalid_message_end", "message_end carried an undecodable message");
            return;
        };
        if (decoded) |message| reducer.final = message.content;
        return;
    }
    if (std.mem.eql(u8, kind, "agent_end")) {
        if (memberOf(event, "willRetry")) |retry| {
            if (retry == .bool and retry.bool) {
                reducer.candidate = null;
                reducer.candidate_present = false;
                reducer.final = null;
                return;
            }
        }
        reducer.candidate = memberOf(event, "messages");
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
    var content: ?std.json.Value = if (final) |message| message.content else null;
    var stop_reason: []const u8 = if (final) |message| message.stop_reason else "";
    var error_message: []const u8 = if (final) |message| message.error_message else "";
    if (final == null and reducer.candidate_present) {
        content = reducer.final;
        if (content != null) {
            stop_reason = "";
            error_message = "";
        }
    }
    const aborted = final != null and std.mem.eql(u8, stop_reason, "aborted");
    if (reducer.cancel_intent and (!reducer.candidate_present or aborted)) {
        const payload = try reducer.object();
        try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
        try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
        try payload.put(reducer.arena, "reason", Reducer.str("Pi settled after abort intent"));
        try reducer.emit("run.cancelled", .{ .object = payload.* }, true);
        return;
    }
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
    const response = try reducer.object();
    try response.put(reducer.arena, "id", Reducer.str(reducer.message_id));
    try response.put(reducer.arena, "role", Reducer.str("assistant"));
    try response.put(reducer.arena, "content", content.?);
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "final_response", .{ .object = response.* });
    try payload.put(reducer.arena, "stop_reason", Reducer.str(reason));
    try reducer.emit("run.completed", .{ .object = payload.* }, true);
}

fn failRun(reducer: *Reducer, code: []const u8, message: []const u8) !void {
    try failWith(reducer, code, message);
}

fn failWith(reducer: *Reducer, code: []const u8, message: []const u8) !void {
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

fn startTool(reducer: *Reducer, event: std.json.Value) !void {
    const native_id = textOf(event, "toolCallId");
    const name = textOf(event, "toolName");
    const args = memberOf(event, "args") orelse std.json.Value{ .null = {} };
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
    const native_id = textOf(event, "toolCallId");
    const tool = findTool(reducer, native_id) orelse {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool update without matching active start");
        return;
    };
    if (tool.terminal or !std.mem.eql(u8, tool.name, textOf(event, "toolName"))) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool update without matching active start");
        return;
    }
    tool.progress = memberOf(event, "partialResult");
    _ = try reducer.emitReplying("action.call.progress", try toolPayload(reducer, tool, false, true, false), false, tool.started_event);
}

fn endTool(reducer: *Reducer, event: std.json.Value) !void {
    const native_id = textOf(event, "toolCallId");
    const tool = findTool(reducer, native_id) orelse {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool end without matching active start");
        return;
    };
    if (tool.terminal or !std.mem.eql(u8, tool.name, textOf(event, "toolName"))) {
        try failRun(reducer, "pi_invalid_tool_lifecycle", "tool end without matching active start");
        return;
    }
    tool.result = memberOf(event, "result");
    tool.terminal = true;
    const failed = if (memberOf(event, "isError")) |flag| flag == .bool and flag.bool else false;
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

const message_update_members = [_][]const u8{ "type", "usage", "assistantMessageEvent" };

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
        const index = integerMember(raw, "contentIndex") orelse return Error.InvalidFrame;
        if (index < 0) return Error.InvalidFrame;
        if (std.mem.eql(u8, kind, "text_delta")) return .{ .kind = "text", .text = delta };
        return .{ .kind = "reasoning", .text = delta };
    }
    if (std.mem.eql(u8, kind, "start")) {
        try closedMembers(raw, &.{"type"});
        return null;
    }
    if (std.mem.eql(u8, kind, "text_start") or std.mem.eql(u8, kind, "thinking_start")) {
        try closedMembers(raw, &.{ "type", "contentIndex" });
        if (raw.object.get("contentIndex") == null) return Error.InvalidFrame;
        return null;
    }
    if (std.mem.eql(u8, kind, "text_end") or std.mem.eql(u8, kind, "thinking_end")) {
        try closedMembers(raw, &.{ "type", "contentIndex", "content" });
        if (raw.object.get("contentIndex") == null) return Error.InvalidFrame;
        return null;
    }
    if (std.mem.eql(u8, kind, "toolcall_start") or std.mem.eql(u8, kind, "toolcall_end") or std.mem.eql(u8, kind, "toolcall_delta")) {
        return null;
    }
    if (std.mem.eql(u8, kind, "done") or std.mem.eql(u8, kind, "error")) {
        return null;
    }
    return Error.InvalidFrame;
}

fn applyMessageUpdate(reducer: *Reducer, event: std.json.Value) !void {
    for (event.object.keys()) |name| {
        if (!listed(&message_update_members, name)) {
            try failRun(reducer, "pi_invalid_message_update", "message_update carried an unknown member");
            return;
        }
    }
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
    if (reducer.terminal or !reducer.started) return;
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
    if (!reducer.started or reducer.terminal) return;
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
    reducer.interaction_id = try reducer.counters.nextID(reducer.arena, "interaction");
    var questions = std.ArrayList(std.json.Value).empty;
    try questions.append(reducer.arena, .{ .object = question.* });
    const payload = try reducer.object();
    try payload.put(reducer.arena, "interaction_id", Reducer.str(reducer.interaction_id));
    try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
    try payload.put(reducer.arena, "responded_by", Reducer.str(participant));
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "title", Reducer.str(title));
    try payload.put(reducer.arena, "description", Reducer.str(message));
    try payload.put(reducer.arena, "questions", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try questions.toOwnedSlice(reducer.arena)) });
    try payload.put(reducer.arena, "allow_cancel", .{ .bool = true });
    try reducer.emit("user.input.requested", .{ .object = payload.* }, false);
    try statusUpdate(reducer, "waiting_for_input", reducer.interaction_id);
}

pub fn resolveExtension(reducer: *Reducer, option_id: []const u8) !void {
    if (reducer.terminal or reducer.interaction_id.len == 0) return;
    const selected = try reducer.object();
    var ids = std.ArrayList(std.json.Value).empty;
    try ids.append(reducer.arena, Reducer.str(option_id));
    try selected.put(reducer.arena, "question_id", Reducer.str("value"));
    try selected.put(reducer.arena, "selected_option_ids", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try ids.toOwnedSlice(reducer.arena)) });
    var answers = std.ArrayList(std.json.Value).empty;
    try answers.append(reducer.arena, .{ .object = selected.* });
    const payload = try reducer.object();
    try payload.put(reducer.arena, "interaction_id", Reducer.str(reducer.interaction_id));
    try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
    try payload.put(reducer.arena, "responded_by", Reducer.str(participant));
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "status", Reducer.str("submitted"));
    try payload.put(reducer.arena, "answers", .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try answers.toOwnedSlice(reducer.arena)) });
    try reducer.emit("user.input.resolved", .{ .object = payload.* }, false);
    try statusUpdate(reducer, "running", "");
}
