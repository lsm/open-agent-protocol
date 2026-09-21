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
        if (self.terminal) return;
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
        self.sequence += 1;
        try self.emitted.append(self.arena, .{ .object = map.* });
        if (terminal) self.terminal = true;
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
