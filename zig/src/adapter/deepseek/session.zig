const std = @import("std");

pub const capability_revision = "deepseek-harness-47f9438-oap-v1";
pub const endpoint_id = "deepseek.harness";
pub const execution_owner = "deepseek-harness";
const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";

pub const Error = error{ InvalidFrame, OutOfMemory, SessionUnusable, SessionClosed };

pub const Counters = struct {
    ids: usize = 0,
    clock: i64 = 0,

    pub fn nextID(self: *Counters, arena: std.mem.Allocator, kind: []const u8) ![]const u8 {
        self.ids += 1;
        const scalar = 'a' + self.ids - 1;
        const point: u21 = if (scalar > 0x10FFFF) 0xFFFD else @intCast(scalar);
        return std.fmt.allocPrint(arena, "{s}-{u}", .{ kind, point });
    }

    pub fn nextTick(self: *Counters) i64 {
        self.clock += 1;
        return self.clock;
    }
};

pub const ToolState = struct {
    native_id: []const u8,
    id: []const u8,
    name: []const u8,
    args: ?std.json.Value,
    terminal: bool = false,
    started_event: []const u8 = "",
};

pub const Notification = struct {
    method: []const u8,
    params: std.json.Value,
};

pub const ChildState = struct {
    id: []const u8,
    terminal: bool = false,
    failed: bool = false,
};

pub const Reducer = struct {
    arena: std.mem.Allocator,
    counters: Counters = .{},
    session_id: []const u8 = "session",
    model: []const u8 = "deepseek-chat",
    run_id: []const u8 = "",
    message_id: []const u8 = "",
    sequence: u64 = 1,
    started: bool = false,
    terminal: bool = false,
    turn: i64 = 0,
    step: i64 = 0,
    turn_ended: bool = false,
    idle_after_end: bool = false,
    end_kind: []const u8 = "",
    final: ?std.json.Value = null,
    receipt: []const u8 = "",
    pending: std.ArrayList(std.json.Value) = .empty,
    tools: std.ArrayList(*ToolState) = .empty,
    children: std.ArrayList(*ChildState) = .empty,
    buffered: std.ArrayList(Notification) = .empty,
    last_seq: i64 = 0,
    seq_seen: bool = false,
    unusable: bool = false,
    closed: bool = false,
    reserved: bool = false,
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

fn numberOf(value: std.json.Value, name: []const u8) i64 {
    const found = memberOf(value, name) orelse return 0;
    return switch (found) {
        .integer => |n| n,
        else => 0,
    };
}

fn listed(names: []const []const u8, name: []const u8) bool {
    for (names) |candidate| {
        if (std.mem.eql(u8, candidate, name)) return true;
    }
    return false;
}

pub fn openSession(reducer: *Reducer) void {
    _ = reducer.counters.nextTick();
}

pub fn initialize(reducer: *Reducer, model: []const u8) void {
    if (model.len != 0) reducer.model = model;
}

pub fn externalActivity(reducer: *Reducer, what: []const u8) !void {
    reducer.unusable = true;
    if (!reducer.reserved or reducer.terminal) return;
    try failRun(reducer, "deepseek_external_activity", what);
}

pub fn abortSubmission(reducer: *Reducer) void {
    if (reducer.terminal) return;
    reducer.terminal = true;
    reducer.reserved = false;
}

pub fn close(reducer: *Reducer) void {
    reducer.closed = true;
}

pub fn submit(reducer: *Reducer) !void {
    if (reducer.closed) return Error.SessionClosed;
    if (reducer.unusable) return Error.SessionUnusable;
    reducer.reserved = true;
    _ = try reducer.counters.nextID(reducer.arena, "message");
    reducer.message_id = try reducer.counters.nextID(reducer.arena, "message");
    reducer.run_id = "";
    reducer.sequence = 1;
    reducer.started = false;
    reducer.terminal = false;
    reducer.turn = 0;
    reducer.step = 0;
    reducer.turn_ended = false;
    reducer.idle_after_end = false;
    reducer.end_kind = "";
    reducer.final = null;
    reducer.receipt = "";
    reducer.pending.clearRetainingCapacity();
    reducer.tools.clearRetainingCapacity();
    reducer.children.clearRetainingCapacity();
    reducer.buffered.clearRetainingCapacity();
}

pub fn rejectedSubmit(reducer: *Reducer) !void {
    _ = try reducer.counters.nextID(reducer.arena, "message");
}

pub fn start(reducer: *Reducer) !void {
    if (reducer.started) return;
    reducer.run_id = try reducer.counters.nextID(reducer.arena, "run");
    reducer.started = true;
    const started_at = reducer.counters.nextTick();
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "status", Reducer.str("running"));
    try payload.put(reducer.arena, "model_id", Reducer.str(reducer.model));
    try payload.put(reducer.arena, "started_at_ms", .{ .integer = started_at });
    try reducer.emit("run.started", .{ .object = payload.* }, false);
}

fn failRun(reducer: *Reducer, code: []const u8, message: []const u8) !void {
    try failSettled(reducer, code, message, "");
}

fn failSettled(reducer: *Reducer, code: []const u8, message: []const u8, settled_by: []const u8) !void {
    if (!reducer.started) {
        reducer.terminal = true;
        return;
    }
    const err = try reducer.object();
    try err.put(reducer.arena, "code", Reducer.str(code));
    try err.put(reducer.arena, "message", Reducer.str(message));
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "error", .{ .object = err.* });
    if (settled_by.len != 0) try payload.put(reducer.arena, "settled_by", Reducer.str(settled_by));
    try reducer.emit("run.failed", .{ .object = payload.* }, true);
}

const observed_only_events = [_][]const u8{
    "agent-preset/selected",
    "approval/asked",
    "approval/decided",
    "approval/policy",
    "command/done",
    "command/run",
    "compaction/end",
    "compaction/prune",
    "compaction/start",
    "compaction/summary",
    "deliverables/presented",
    "feedback/message-delete",
    "feedback/message-put",
    "feedback/record",
    "goal/change",
    "hook/invoked",
    "hook/result",
    "llm/retry",
    "llm/retry-started",
    "model/selection",
    "permission/preset",
    "plan/mode",
    "sandbox/mode",
    "schedule/change",
    "session-log-deepseek/delivery-accepted",
    "session/title",
    "session/title-llm-request",
    "subagent/catalog",
    "subagent/descriptor",
    "subagent/model-selection-policy",
    "system/message",
    "team/member",
    "team/message/delivered",
    "team/message/queued",
    "team/task",
    "tool-workflow/agent-end",
    "tool-workflow/agent-start",
    "tool-workflow/run-end",
    "tool-workflow/run-start",
    "tool/ptc-dispatch",
    "tool/ptc-dispatch-start",
    "web/deepseek-search-llm-request",
};

const ignored_events = [_][]const u8{
    "user/message",   "agent/inbox/spliced", "todo/write",
    "request/header", "request/context",     "session/end-seed",
};

fn sameStep(reducer: *Reducer, data: std.json.Value) !bool {
    if (reducer.turn_ended or numberOf(data, "turn") != reducer.turn or numberOf(data, "step") != reducer.step) {
        try failRun(reducer, "deepseek_invalid_grammar", "event outside open owned step");
        return false;
    }
    return true;
}

pub fn applyEvent(reducer: *Reducer, event: std.json.Value) !void {
    if (reducer.terminal) return;
    const kind = textOf(event, "type");
    const data = memberOf(event, "data") orelse std.json.Value{ .null = {} };
    if (std.mem.eql(u8, kind, "turn/start")) {
        if (numberOf(data, "turn") != reducer.turn) {
            try failRun(reducer, "deepseek_invalid_grammar", "overlapping foreign turn");
        }
        return;
    }
    if (std.mem.eql(u8, kind, "step/start")) {
        const step = numberOf(data, "step");
        if (numberOf(data, "turn") != reducer.turn or step <= reducer.step) {
            try failRun(reducer, "deepseek_invalid_grammar", "invalid step start");
            return;
        }
        reducer.step = step;
        return;
    }
    if (std.mem.eql(u8, kind, "step/end")) {
        if (numberOf(data, "turn") != reducer.turn or numberOf(data, "step") != reducer.step) {
            try failRun(reducer, "deepseek_invalid_grammar", "invalid step end");
        }
        return;
    }
    if (std.mem.eql(u8, kind, "assistant/attempt")) {
        _ = try sameStep(reducer, data);
        return;
    }
    if (std.mem.eql(u8, kind, "assistant/message")) {
        if (!try sameStep(reducer, data)) return;
        if (memberOf(data, "stream")) |records| try emitStreamRecords(reducer, records);
        reducer.final = data;
        return;
    }
    if (std.mem.eql(u8, kind, "tool/call")) {
        if (!try sameStep(reducer, data)) return;
        try startTool(reducer, data);
        return;
    }
    if (std.mem.eql(u8, kind, "tool/result")) {
        if (!try sameStep(reducer, data)) return;
        try endTool(reducer, data);
        return;
    }
    if (std.mem.eql(u8, kind, "turn/end")) {
        if (numberOf(data, "turn") != reducer.turn or reducer.turn_ended) {
            try failRun(reducer, "deepseek_invalid_grammar", "invalid turn end");
            return;
        }
        const reason = memberOf(data, "reason") orelse std.json.Value{ .null = {} };
        reducer.end_kind = textOf(reason, "kind");
        reducer.turn_ended = true;
        try trySettle(reducer);
        return;
    }
    if (listed(&ignored_events, kind)) return;
    if (listed(&observed_only_events, kind)) return;
    if (memberOf(event, "ignorable")) |flag| {
        if (flag == .bool and flag.bool) return;
    }
    try failRun(reducer, "deepseek_unknown_event", "unknown required event");
}

pub fn observeStatus(reducer: *Reducer, status: []const u8) !void {
    if (!reducer.started or !reducer.turn_ended) return;
    if (!std.mem.eql(u8, status, "idle")) return;
    reducer.idle_after_end = true;
    try trySettle(reducer);
}

fn emitDelta(reducer: *Reducer, kind: []const u8, body: []const u8) !void {
    const part = try reducer.object();
    try part.put(reducer.arena, "type", Reducer.str(kind));
    try part.put(reducer.arena, kind, Reducer.str(body));
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "message_id", Reducer.str(reducer.message_id));
    try payload.put(reducer.arena, "part", .{ .object = part.* });
    try reducer.emit("content.delta", .{ .object = payload.* }, false);
}

fn emitStreamRecords(reducer: *Reducer, records: std.json.Value) !void {
    if (records != .array) return;
    for (records.array.items) |record| {
        const kind = textOf(record, "type");
        if (std.mem.eql(u8, kind, "text-chunks") or std.mem.eql(u8, kind, "reasoning-chunks")) {
            const part: []const u8 = if (std.mem.eql(u8, kind, "text-chunks")) "text" else "reasoning";
            const texts = memberOf(record, "texts") orelse continue;
            if (texts != .array) continue;
            for (texts.array.items) |text| {
                if (text != .string) continue;
                try emitDelta(reducer, part, text.string);
            }
            continue;
        }
        if (!std.mem.eql(u8, kind, "chunk")) continue;
        const chunk = memberOf(record, "chunk") orelse continue;
        const chunk_kind = textOf(chunk, "type");
        if (std.mem.eql(u8, chunk_kind, "text-delta")) {
            try emitDelta(reducer, "text", textOf(chunk, "text"));
        } else if (std.mem.eql(u8, chunk_kind, "reasoning-delta")) {
            try emitDelta(reducer, "reasoning", textOf(chunk, "text"));
        }
    }
}

fn decodedArguments(reducer: *Reducer, carried: ?std.json.Value) !?std.json.Value {
    const value = carried orelse return null;
    if (value != .string) return value;
    return std.json.parseFromSliceLeaky(std.json.Value, reducer.arena, value.string, .{}) catch null;
}

fn findTool(reducer: *Reducer, native_id: []const u8) ?*ToolState {
    for (reducer.tools.items) |tool| {
        if (std.mem.eql(u8, tool.native_id, native_id)) return tool;
    }
    return null;
}

fn toolPayload(reducer: *Reducer, tool: *ToolState, carry_arguments: bool) !std.json.Value {
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "tool_call_id", Reducer.str(tool.id));
    try payload.put(reducer.arena, "requested_by", Reducer.str(endpoint_id));
    try payload.put(reducer.arena, "execution_owner", Reducer.str(execution_owner));
    try payload.put(reducer.arena, "name", Reducer.str(tool.name));
    if (carry_arguments) {
        if (tool.args) |args| try payload.put(reducer.arena, "arguments_json", args);
    }
    return .{ .object = payload.* };
}

fn startTool(reducer: *Reducer, data: std.json.Value) !void {
    const call_id = textOf(data, "callId");
    if (findTool(reducer, call_id) != null) {
        try failRun(reducer, "deepseek_tool_lifecycle", "duplicate tool call");
        return;
    }
    const tool = try reducer.arena.create(ToolState);
    tool.* = .{
        .native_id = call_id,
        .id = try reducer.counters.nextID(reducer.arena, "tool-call"),
        .name = textOf(data, "name"),
        .args = try decodedArguments(reducer, memberOf(data, "arguments")),
    };
    try reducer.tools.append(reducer.arena, tool);
    const requested = try reducer.emitReplying("action.call.requested", try toolPayload(reducer, tool, true), false, "");
    tool.started_event = try reducer.emitReplying("action.call.started", try toolPayload(reducer, tool, false), false, requested);
}

fn endTool(reducer: *Reducer, data: std.json.Value) !void {
    const message = memberOf(data, "message") orelse std.json.Value{ .null = {} };
    const source = memberOf(message, "source") orelse std.json.Value{ .null = {} };
    const tool = findTool(reducer, textOf(source, "callId")) orelse {
        try failRun(reducer, "deepseek_tool_lifecycle", "unmatched tool result");
        return;
    };
    if (tool.terminal) {
        try failRun(reducer, "deepseek_tool_lifecycle", "unmatched tool result");
        return;
    }
    tool.terminal = true;
    var payload = try toolPayload(reducer, tool, false);
    if (memberOf(data, "error")) |failure| {
        if (failure != .null) {
            const err = try reducer.object();
            try err.put(reducer.arena, "code", Reducer.str(textOf(failure, "code")));
            try err.put(reducer.arena, "message", Reducer.str(textOf(failure, "name")));
            try payload.object.put(reducer.arena, "error", .{ .object = err.* });
            _ = try reducer.emitReplying("action.call.failed", payload, false, tool.started_event);
            return;
        }
    }
    if (memberOf(message, "content")) |content| {
        try payload.object.put(reducer.arena, "result", content);
    }
    _ = try reducer.emitReplying("action.call.completed", payload, false, tool.started_event);
}

fn blocksContent(reducer: *Reducer, blocks: std.json.Value) !std.json.Value {
    var parts = std.ArrayList(std.json.Value).empty;
    if (blocks == .array) {
        for (blocks.array.items) |block| {
            const kind = textOf(block, "type");
            if (std.mem.eql(u8, kind, "text") or std.mem.eql(u8, kind, "reasoning")) {
                const shape = try reducer.object();
                try shape.put(reducer.arena, "type", Reducer.str(kind));
                try shape.put(reducer.arena, kind, Reducer.str(textOf(block, "text")));
                try parts.append(reducer.arena, .{ .object = shape.* });
                continue;
            }
            if (std.mem.eql(u8, kind, "tool-call")) {
                const tool = findTool(reducer, textOf(block, "id")) orelse return Error.InvalidFrame;
                if (!std.mem.eql(u8, tool.name, textOf(block, "name"))) return Error.InvalidFrame;
                const carried = try decodedArguments(reducer, memberOf(block, "arguments")) orelse return Error.InvalidFrame;
                const shape = try reducer.object();
                try shape.put(reducer.arena, "type", Reducer.str("tool_call"));
                try shape.put(reducer.arena, "tool_call_id", Reducer.str(tool.id));
                try shape.put(reducer.arena, "name", Reducer.str(tool.name));
                try shape.put(reducer.arena, "arguments_json", carried);
                try parts.append(reducer.arena, .{ .object = shape.* });
                continue;
            }
            return Error.InvalidFrame;
        }
    }
    if (parts.items.len == 0) return Reducer.str("");
    if (parts.items.len == 1 and std.mem.eql(u8, textOf(parts.items[0], "type"), "text")) {
        return Reducer.str(textOf(parts.items[0], "text"));
    }
    return .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try parts.toOwnedSlice(reducer.arena)) };
}

fn trySettle(reducer: *Reducer) !void {
    if (!reducer.turn_ended or !reducer.idle_after_end or reducer.terminal) return;
    var failed_child = false;
    for (reducer.children.items) |child| {
        if (!child.terminal) return;
        failed_child = failed_child or child.failed;
    }
    if (failed_child) {
        try failRun(reducer, "deepseek_child_failed", "subagent failed");
        return;
    }
    if (!std.mem.eql(u8, reducer.end_kind, "completed")) {
        const code = try std.fmt.allocPrint(reducer.arena, "deepseek_{s}", .{reducer.end_kind});
        const message = try std.fmt.allocPrint(reducer.arena, "native turn ended: {s}", .{reducer.end_kind});
        try failRun(reducer, code, message);
        return;
    }
    const final = reducer.final orelse {
        try failRun(reducer, "deepseek_missing_final_message", "completed turn omitted assistant message");
        return;
    };
    const message = memberOf(final, "message") orelse std.json.Value{ .null = {} };
    const content = blocksContent(reducer, memberOf(message, "content") orelse std.json.Value{ .null = {} }) catch {
        try failRun(reducer, "deepseek_invalid_final_message", "final message carried content the reducer cannot map");
        return;
    };
    const response = try reducer.object();
    try response.put(reducer.arena, "id", Reducer.str(reducer.message_id));
    try response.put(reducer.arena, "role", Reducer.str("assistant"));
    try response.put(reducer.arena, "content", content);
    const payload = try reducer.object();
    try payload.put(reducer.arena, "session_id", Reducer.str(reducer.session_id));
    try payload.put(reducer.arena, "run_id", Reducer.str(reducer.run_id));
    try payload.put(reducer.arena, "final_response", .{ .object = response.* });
    try payload.put(reducer.arena, "stop_reason", Reducer.str("completed"));
    if (memberOf(final, "usage")) |carried| {
        if (carried != .null) {
            const input = numberOf(carried, "inputTokens");
            const output = numberOf(carried, "outputTokens");
            const usage = try reducer.object();
            try usage.put(reducer.arena, "input_tokens", .{ .integer = input });
            try usage.put(reducer.arena, "output_tokens", .{ .integer = output });
            try usage.put(reducer.arena, "total_tokens", .{ .integer = input +% output });
            try payload.put(reducer.arena, "usage", .{ .object = usage.* });
        }
    }
    try reducer.emit("run.completed", .{ .object = payload.* }, true);
}

pub fn invalidObservation(reducer: *Reducer, event_type: []const u8) !void {
    const message = try std.fmt.allocPrint(reducer.arena, "deepseek native: invalid pinned message: unknown required event \"{s}\"", .{event_type});
    try transportFailed(reducer, message);
}

pub fn transportFailed(reducer: *Reducer, message: []const u8) !void {
    reducer.unusable = true;
    if (reducer.terminal or !reducer.started) {
        reducer.terminal = true;
        return;
    }
    try failSettled(reducer, "deepseek_process_exit", message, "inferred");
}

fn directUser(source: std.json.Value) bool {
    if (source != .object) return false;
    if (!std.mem.eql(u8, textOf(source, "kind"), "user")) return false;
    for ([_][]const u8{ "plugin", "provider", "model", "callId", "form", "summary" }) |name| {
        if (textOf(source, name).len != 0) return false;
    }
    for ([_][]const u8{ "sections", "replayState" }) |name| {
        if (memberOf(source, name)) |carried| {
            if (carried == .array and carried.array.items.len != 0) return false;
        }
    }
    return true;
}

pub fn receipt(reducer: *Reducer, message_id: []const u8) !void {
    reducer.receipt = message_id;
    try evaluateAdmission(reducer);
}

pub fn observe(reducer: *Reducer, event: std.json.Value) !void {
    if (reducer.started) {
        try applyEvent(reducer, event);
        return;
    }
    try reducer.pending.append(reducer.arena, event);
    try evaluateAdmission(reducer);
}

fn findChild(reducer: *Reducer, id: []const u8) ?*ChildState {
    for (reducer.children.items) |child| {
        if (std.mem.eql(u8, child.id, id)) return child;
    }
    return null;
}

fn openChild(reducer: *Reducer, id: []const u8) !void {
    const child = try reducer.arena.create(ChildState);
    child.* = .{ .id = id };
    try reducer.children.append(reducer.arena, child);
}

pub fn observeNotification(reducer: *Reducer, method: []const u8, params: std.json.Value) !void {
    if (reducer.closed) return Error.SessionClosed;
    if (!reducer.reserved or reducer.terminal) {
        const own_status = std.mem.eql(u8, method, "session.status") and
            std.mem.eql(u8, textOf(params, "sessionId"), reducer.session_id);
        if (!own_status) try externalActivity(reducer, "notification without reserved run");
        return;
    }
    if (std.mem.eql(u8, method, "session.event")) {
        if (!std.mem.eql(u8, textOf(params, "sessionId"), reducer.session_id)) {
            try failRun(reducer, "deepseek_session_mismatch", "session.event for foreign session");
            return;
        }
        const event = memberOf(params, "event") orelse std.json.Value{ .null = {} };
        const seq = numberOf(event, "seq");
        if (reducer.seq_seen and seq <= reducer.last_seq) {
            try failRun(reducer, "deepseek_invalid_sequence", "non-monotonic native event sequence");
            return;
        }
        reducer.seq_seen = true;
        reducer.last_seq = seq;
        try observe(reducer, event);
        return;
    }

    if (!reducer.started) try reducer.buffered.append(reducer.arena, .{ .method = method, .params = params });

    if (std.mem.eql(u8, method, "session.status")) {
        if (!std.mem.eql(u8, textOf(params, "sessionId"), reducer.session_id)) {
            try failRun(reducer, "deepseek_session_mismatch", "session.status for foreign session");
            return;
        }
        try observeStatus(reducer, textOf(params, "status"));
        return;
    }
    if (std.mem.eql(u8, method, "subagent.started")) {
        const child_id = textOf(params, "childSessionId");
        if (!std.mem.eql(u8, textOf(params, "parentSessionId"), reducer.session_id) or std.mem.eql(u8, child_id, reducer.session_id)) {
            try failRun(reducer, "deepseek_external_activity", "foreign or recursive subagent");
            return;
        }
        if (!reducer.started) return;
        if (findChild(reducer, child_id) != null) {
            try failRun(reducer, "deepseek_child_lifecycle", "duplicate child start");
            return;
        }
        try openChild(reducer, child_id);
        return;
    }
    if (std.mem.eql(u8, method, "subagent.finished")) {
        if (!std.mem.eql(u8, textOf(params, "parentSessionId"), reducer.session_id)) {
            try failRun(reducer, "deepseek_external_activity", "foreign child finish");
            return;
        }
        if (!reducer.started) return;
        const child = findChild(reducer, textOf(params, "childSessionId"));
        if (child == null or child.?.terminal) {
            try failRun(reducer, "deepseek_child_lifecycle", "unmatched child finish");
            return;
        }
        child.?.terminal = true;
        child.?.failed = !std.mem.eql(u8, textOf(params, "status"), "ok") or std.mem.eql(u8, textOf(params, "stopReason"), "max-tokens");
        try trySettle(reducer);
        return;
    }
    try failRun(reducer, "deepseek_unknown_notification", "unknown notification");
}

fn drainBuffered(reducer: *Reducer) !void {
    const replayed = try reducer.arena.dupe(Notification, reducer.buffered.items);
    reducer.buffered.clearRetainingCapacity();
    for (replayed) |notification| {
        if (reducer.terminal) break;
        const params = notification.params;
        const parent_matches = std.mem.eql(u8, textOf(params, "parentSessionId"), reducer.session_id);
        if (std.mem.eql(u8, notification.method, "session.status")) {
            if (!std.mem.eql(u8, textOf(params, "sessionId"), reducer.session_id)) {
                try failRun(reducer, "deepseek_session_mismatch", "session.status for foreign session");
            } else if (reducer.turn_ended and std.mem.eql(u8, textOf(params, "status"), "idle")) {
                reducer.idle_after_end = true;
            }
            continue;
        }
        const child_id = textOf(params, "childSessionId");
        if (std.mem.eql(u8, notification.method, "subagent.started")) {
            if (!parent_matches or std.mem.eql(u8, child_id, reducer.session_id) or findChild(reducer, child_id) != null) {
                try failRun(reducer, "deepseek_child_lifecycle", "invalid buffered child start");
            } else {
                try openChild(reducer, child_id);
            }
            continue;
        }
        if (std.mem.eql(u8, notification.method, "subagent.finished")) {
            const child = findChild(reducer, child_id);
            if (!parent_matches or child == null or child.?.terminal) {
                try failRun(reducer, "deepseek_child_lifecycle", "invalid buffered child finish");
            } else {
                child.?.terminal = true;
                child.?.failed = !std.mem.eql(u8, textOf(params, "status"), "ok") or std.mem.eql(u8, textOf(params, "stopReason"), "max-tokens");
            }
            continue;
        }
    }
    try trySettle(reducer);
}

fn evaluateAdmission(reducer: *Reducer) !void {
    if (reducer.started or reducer.terminal or reducer.receipt.len == 0) return;
    var matches: usize = 0;
    var turn: i64 = 0;
    var step: i64 = 0;
    var entered = false;
    var closed = false;
    var turn_start: ?std.json.Value = null;
    var candidate = std.ArrayList(std.json.Value).empty;
    defer candidate.deinit(reducer.arena);
    var admitted = std.ArrayList(std.json.Value).empty;
    defer admitted.deinit(reducer.arena);

    for (reducer.pending.items) |event| {
        const kind = textOf(event, "type");
        const data = memberOf(event, "data") orelse std.json.Value{ .null = {} };
        if (std.mem.eql(u8, kind, "agent/inbox/spliced")) {
            const inserted = memberOf(data, "inserted") orelse continue;
            if (inserted != .array) continue;
            for (inserted.array.items) |message| {
                if (std.mem.eql(u8, textOf(message, "id"), reducer.receipt) and
                    directUser(memberOf(message, "source") orelse std.json.Value{ .null = {} }))
                {
                    matches += 1;
                }
            }
            continue;
        }
        if (std.mem.eql(u8, kind, "turn/start")) {
            turn = numberOf(data, "turn");
            step = 0;
            closed = false;
            entered = false;
            turn_start = event;
            candidate.clearRetainingCapacity();
            try candidate.append(reducer.arena, event);
            continue;
        }
        if (std.mem.eql(u8, kind, "step/start")) {
            if (turn != numberOf(data, "turn")) continue;
            if (entered) {
                try candidate.append(reducer.arena, event);
                continue;
            }
            step = numberOf(data, "step");
            candidate.clearRetainingCapacity();
            if (turn_start) |opened| try candidate.append(reducer.arena, opened);
            try candidate.append(reducer.arena, event);
            continue;
        }
        if (std.mem.eql(u8, kind, "user/message")) {
            if (turn == 0 or step == 0) continue;
            try candidate.append(reducer.arena, event);
            if (!entered and std.mem.eql(u8, textOf(data, "id"), reducer.receipt) and
                directUser(memberOf(data, "source") orelse std.json.Value{ .null = {} }))
            {
                entered = true;
                reducer.turn = turn;
                reducer.step = step;
                admitted.clearRetainingCapacity();
                try admitted.appendSlice(reducer.arena, candidate.items);
            }
            continue;
        }
        if (std.mem.eql(u8, kind, "turn/end")) {
            if (numberOf(data, "turn") == turn) {
                try candidate.append(reducer.arena, event);
                closed = true;
                if (!entered) {
                    turn = 0;
                    step = 0;
                    candidate.clearRetainingCapacity();
                }
            }
            continue;
        }
        if (turn != 0) try candidate.append(reducer.arena, event);
    }

    if (matches != 1 or reducer.turn == 0) {
        if (closed or matches > 1) reducer.terminal = true;
        return;
    }
    if (closed) {
        admitted.clearRetainingCapacity();
        try admitted.appendSlice(reducer.arena, candidate.items);
    } else if (turn == reducer.turn and candidate.items.len > admitted.items.len) {
        admitted.clearRetainingCapacity();
        try admitted.appendSlice(reducer.arena, candidate.items);
    }

    const replayed = try reducer.arena.dupe(std.json.Value, admitted.items);
    reducer.pending.clearRetainingCapacity();
    try start(reducer);
    for (replayed) |event| {
        if (reducer.terminal) break;
        const kind = textOf(event, "type");
        if (std.mem.eql(u8, kind, "turn/start") or std.mem.eql(u8, kind, "step/start") or std.mem.eql(u8, kind, "user/message")) continue;
        try applyEvent(reducer, event);
    }
    try drainBuffered(reducer);
}

fn parse(arena: std.mem.Allocator, text: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{});
}

fn admittedRun(arena: std.mem.Allocator) !Reducer {
    var reducer = Reducer.init(arena);
    openSession(&reducer);
    try submit(&reducer);
    try observe(&reducer, try parse(arena, "{\"type\":\"agent/inbox/spliced\",\"data\":{\"inserted\":[{\"id\":\"m-1\",\"source\":{\"kind\":\"user\"}}]}}"));
    try receipt(&reducer, "m-1");
    try observe(&reducer, try parse(arena, "{\"type\":\"turn/start\",\"data\":{\"turn\":1}}"));
    try observe(&reducer, try parse(arena, "{\"type\":\"step/start\",\"data\":{\"turn\":1,\"step\":1}}"));
    try observe(&reducer, try parse(arena, "{\"type\":\"user/message\",\"data\":{\"id\":\"m-1\",\"source\":{\"kind\":\"user\"}}}"));
    return reducer;
}

fn lastFailure(reducer: *Reducer) ?[]const u8 {
    if (reducer.emitted.items.len == 0) return null;
    const last = reducer.emitted.items[reducer.emitted.items.len - 1];
    if (!std.mem.eql(u8, textOf(last, "type"), "run.failed")) return null;
    const payload = memberOf(last, "payload") orelse return null;
    const err = memberOf(payload, "error") orelse return null;
    return textOf(err, "code");
}

fn expectRefusal(script: []const []const u8, code: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try admittedRun(arena.allocator());
    for (script) |line| try applyEvent(&reducer, try parse(arena.allocator(), line));
    const raised = lastFailure(&reducer) orelse return error.NoRefusal;
    try std.testing.expectEqualStrings(code, raised);
}

test "an observed-only event is not a required one" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try admittedRun(arena.allocator());
    try applyEvent(&reducer, try parse(arena.allocator(), "{\"type\":\"approval/asked\",\"data\":{}}"));
    try std.testing.expect(!reducer.terminal);
    try applyEvent(&reducer, try parse(arena.allocator(), "{\"type\":\"future/unheard-of\",\"data\":{}}"));
    try std.testing.expectEqualStrings("deepseek_unknown_event", lastFailure(&reducer).?);
}

test "a user message whose source is not the user kind does not admit the run" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = Reducer.init(a);
    openSession(&reducer);
    try submit(&reducer);
    try observe(&reducer, try parse(a, "{\"type\":\"agent/inbox/spliced\",\"data\":{\"inserted\":[{\"id\":\"m-1\",\"source\":{\"kind\":\"user\"}}]}}"));
    try receipt(&reducer, "m-1");
    try observe(&reducer, try parse(a, "{\"type\":\"turn/start\",\"data\":{\"turn\":1}}"));
    try observe(&reducer, try parse(a, "{\"type\":\"step/start\",\"data\":{\"turn\":1,\"step\":1}}"));
    try observe(&reducer, try parse(a, "{\"type\":\"user/message\",\"data\":{\"id\":\"m-1\",\"source\":{\"kind\":\"agent\"}}}"));
    try std.testing.expect(!reducer.started);
    try std.testing.expect(reducer.emitted.items.len == 0);
}

test "a user message claiming the receipt from anything but a direct user does not admit the run" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = Reducer.init(a);
    openSession(&reducer);
    try submit(&reducer);
    try observe(&reducer, try parse(a, "{\"type\":\"agent/inbox/spliced\",\"data\":{\"inserted\":[{\"id\":\"m-1\",\"source\":{\"kind\":\"user\"}}]}}"));
    try receipt(&reducer, "m-1");
    try observe(&reducer, try parse(a, "{\"type\":\"turn/start\",\"data\":{\"turn\":1}}"));
    try observe(&reducer, try parse(a, "{\"type\":\"step/start\",\"data\":{\"turn\":1,\"step\":1}}"));
    try observe(&reducer, try parse(a, "{\"type\":\"user/message\",\"data\":{\"id\":\"m-1\",\"source\":{\"kind\":\"user\",\"plugin\":\"forwarder\"}}}"));
    try std.testing.expect(!reducer.started);
    try std.testing.expect(reducer.emitted.items.len == 0);
}

test "a turn that ends settles nothing until the session reports idle" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try admittedRun(arena.allocator());
    try applyEvent(&reducer, try parse(arena.allocator(), "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&reducer, try parse(arena.allocator(), "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try std.testing.expect(!reducer.terminal);
    try observeStatus(&reducer, "idle");
    try std.testing.expect(reducer.terminal);
}

test "a foreign turn, an out-of-order step and a repeated turn end are grammar defects" {
    try expectRefusal(&.{"{\"type\":\"turn/start\",\"data\":{\"turn\":2}}"}, "deepseek_invalid_grammar");
    try expectRefusal(&.{"{\"type\":\"step/start\",\"data\":{\"turn\":1,\"step\":1}}"}, "deepseek_invalid_grammar");
    try expectRefusal(&.{"{\"type\":\"step/end\",\"data\":{\"turn\":1,\"step\":2}}"}, "deepseek_invalid_grammar");
    try expectRefusal(&.{
        "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}",
        "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}",
    }, "deepseek_invalid_grammar");
}

test "an unknown required event is refused" {
    try expectRefusal(&.{"{\"type\":\"future/required-control\",\"data\":{}}"}, "deepseek_unknown_event");
}

test "a duplicate tool call and an unmatched tool result are lifecycle defects" {
    try expectRefusal(&.{
        "{\"type\":\"tool/call\",\"data\":{\"turn\":1,\"step\":1,\"callId\":\"c1\",\"name\":\"read\",\"arguments\":\"{}\"}}",
        "{\"type\":\"tool/call\",\"data\":{\"turn\":1,\"step\":1,\"callId\":\"c1\",\"name\":\"read\",\"arguments\":\"{}\"}}",
    }, "deepseek_tool_lifecycle");
    try expectRefusal(&.{
        "{\"type\":\"tool/result\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"source\":{\"callId\":\"nope\"}}}}",
    }, "deepseek_tool_lifecycle");
    try expectRefusal(&.{
        "{\"type\":\"tool/call\",\"data\":{\"turn\":1,\"step\":1,\"callId\":\"c1\",\"name\":\"read\",\"arguments\":\"{}\"}}",
        "{\"type\":\"tool/result\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"source\":{\"callId\":\"c1\"},\"content\":[]}}}",
        "{\"type\":\"tool/result\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"source\":{\"callId\":\"c1\"},\"content\":[]}}}",
    }, "deepseek_tool_lifecycle");
}

test "a completed turn with no assistant message and one with unmappable content are refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try admittedRun(arena.allocator());
    try applyEvent(&reducer, try parse(arena.allocator(), "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&reducer, "idle");
    try std.testing.expectEqualStrings("deepseek_missing_final_message", lastFailure(&reducer).?);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    var other = try admittedRun(second.allocator());
    try applyEvent(&other, try parse(second.allocator(), "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"invented\"}]}}}"));
    try applyEvent(&other, try parse(second.allocator(), "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&other, "idle");
    try std.testing.expectEqualStrings("deepseek_invalid_final_message", lastFailure(&other).?);
}

test "a refusal before the run is admitted emits nothing" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(arena.allocator());
    openSession(&reducer);
    try submit(&reducer);
    try transportFailed(&reducer, "gone");
    try std.testing.expect(reducer.emitted.items.len == 0);
    try std.testing.expect(reducer.terminal);
}

test "an id letter is a code point, as the oracle mints it, not a byte" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();

    const cases = [_]struct { n: usize, want: []const u8 }{
        .{ .n = 1, .want = "kind-a" },
        .{ .n = 26, .want = "kind-z" },
        .{ .n = 27, .want = "kind-{" },
        .{ .n = 31, .want = "kind-\x7f" },
        .{ .n = 32, .want = "kind-\u{80}" },
        .{ .n = 100, .want = "kind-\u{c4}" },
        .{ .n = 160, .want = "kind-\u{100}" },
        .{ .n = 55199, .want = "kind-\u{d7ff}" },
        .{ .n = 55200, .want = "kind-\u{fffd}" },
        .{ .n = 57247, .want = "kind-\u{fffd}" },
        .{ .n = 57248, .want = "kind-\u{e000}" },
        .{ .n = 1114015, .want = "kind-\u{10ffff}" },
        .{ .n = 1114016, .want = "kind-\u{fffd}" },
        .{ .n = 2097056, .want = "kind-\u{fffd}" },
    };

    for (cases) |case| {
        var counters = Counters{ .ids = case.n - 1 };
        const got = try counters.nextID(arena.allocator(), "kind");
        try std.testing.expectEqualStrings(case.want, got);
        try std.testing.expect(std.unicode.utf8ValidateSlice(got));
    }
}

fn finalContent(reducer: *Reducer) ?std.json.Value {
    var index = reducer.emitted.items.len;
    while (index > 0) {
        index -= 1;
        const envelope = reducer.emitted.items[index];
        if (!std.mem.eql(u8, textOf(envelope, "type"), "run.completed")) continue;
        const payload = memberOf(envelope, "payload") orelse return null;
        const response = memberOf(payload, "final_response") orelse return null;
        return memberOf(response, "content");
    }
    return null;
}

test "a final message carrying no content block completes with empty text, never an empty array" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try applyEvent(&reducer, try parse(a, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[]}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&reducer, "idle");

    const content = finalContent(&reducer) orelse return error.RunDidNotComplete;
    try std.testing.expect(content == .string);
    try std.testing.expectEqualStrings("", content.string);
}

test "a final tool call carries the arguments of the final block, not those of the call event" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try applyEvent(&reducer, try parse(a, "{\"type\":\"tool/call\",\"data\":{\"turn\":1,\"step\":1,\"callId\":\"c-1\",\"name\":\"grep\",\"arguments\":\"{\\\"q\\\":\\\"first\\\"}\"}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"tool-call\",\"id\":\"c-1\",\"name\":\"grep\",\"arguments\":\"{\\\"q\\\":\\\"final\\\"}\"}]}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&reducer, "idle");

    const content = finalContent(&reducer) orelse return error.RunDidNotComplete;
    try std.testing.expect(content == .array);
    try std.testing.expect(content.array.items.len == 1);
    const carried = memberOf(content.array.items[0], "arguments_json") orelse return error.NoArguments;
    try std.testing.expectEqualStrings("final", textOf(carried, "q"));
}

fn notify(reducer: *Reducer, arena: std.mem.Allocator, method: []const u8, params_text: []const u8) !void {
    try observeNotification(reducer, method, try parse(arena, params_text));
}

test "a notification naming another session is a mismatch, on events and on status alike" {
    var first = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer first.deinit();
    var reducer = try admittedRun(first.allocator());
    try notify(&reducer, first.allocator(), "session.event", "{\"sessionId\":\"elsewhere\",\"event\":{\"type\":\"turn/end\",\"seq\":99,\"data\":{\"turn\":1}}}");
    try std.testing.expectEqualStrings("deepseek_session_mismatch", lastFailure(&reducer).?);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    var other = try admittedRun(second.allocator());
    try notify(&other, second.allocator(), "session.status", "{\"sessionId\":\"elsewhere\",\"status\":\"idle\"}");
    try std.testing.expectEqualStrings("deepseek_session_mismatch", lastFailure(&other).?);
}

test "a native sequence that repeats or goes backwards is refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try notify(&reducer, a, "session.event", "{\"sessionId\":\"session\",\"event\":{\"type\":\"step/end\",\"seq\":7,\"data\":{\"turn\":1,\"step\":1}}}");
    try std.testing.expect(lastFailure(&reducer) == null);
    try notify(&reducer, a, "session.event", "{\"sessionId\":\"session\",\"event\":{\"type\":\"step/end\",\"seq\":7,\"data\":{\"turn\":1,\"step\":1}}}");
    try std.testing.expectEqualStrings("deepseek_invalid_sequence", lastFailure(&reducer).?);
}

test "an event outside the open owned step is a grammar defect, not a frame to drop" {
    try expectRefusal(&.{
        "{\"type\":\"assistant/message\",\"data\":{\"turn\":2,\"step\":1,\"message\":{\"content\":[]}}}",
    }, "deepseek_invalid_grammar");
    try expectRefusal(&.{
        "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}",
        "{\"type\":\"tool/call\",\"data\":{\"turn\":1,\"step\":1,\"callId\":\"c\",\"name\":\"n\",\"arguments\":\"{}\"}}",
    }, "deepseek_invalid_grammar");
}

test "idle arms settlement only once the owned turn has ended" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try observeStatus(&reducer, "idle");
    try std.testing.expect(!reducer.idle_after_end);
    try applyEvent(&reducer, try parse(a, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try std.testing.expect(!reducer.terminal);
    try observeStatus(&reducer, "idle");
    try std.testing.expect(reducer.terminal);
}

test "a subagent that is foreign, recursive or repeated is refused" {
    var first = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer first.deinit();
    var reducer = try admittedRun(first.allocator());
    try notify(&reducer, first.allocator(), "subagent.started", "{\"parentSessionId\":\"elsewhere\",\"childSessionId\":\"c-1\"}");
    try std.testing.expectEqualStrings("deepseek_external_activity", lastFailure(&reducer).?);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    var recursive = try admittedRun(second.allocator());
    try notify(&recursive, second.allocator(), "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"session\"}");
    try std.testing.expectEqualStrings("deepseek_external_activity", lastFailure(&recursive).?);

    var third = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer third.deinit();
    var repeated = try admittedRun(third.allocator());
    try notify(&repeated, third.allocator(), "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\"}");
    try notify(&repeated, third.allocator(), "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\"}");
    try std.testing.expectEqualStrings("deepseek_child_lifecycle", lastFailure(&repeated).?);
}

test "a running child holds the terminal, and a failed one decides it" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try notify(&reducer, a, "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\"}");
    try applyEvent(&reducer, try parse(a, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&reducer, "idle");
    try std.testing.expect(!reducer.terminal);
    try notify(&reducer, a, "subagent.finished", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\",\"status\":\"error\"}");
    try std.testing.expectEqualStrings("deepseek_child_failed", lastFailure(&reducer).?);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    const b = second.allocator();
    var capped = try admittedRun(b);
    try notify(&capped, b, "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\"}");
    try applyEvent(&capped, try parse(b, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&capped, try parse(b, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&capped, "idle");
    try notify(&capped, b, "subagent.finished", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\",\"status\":\"ok\",\"stopReason\":\"max-tokens\"}");
    try std.testing.expectEqualStrings("deepseek_child_failed", lastFailure(&capped).?);
}

test "a notification this port does not know is refused" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var reducer = try admittedRun(arena.allocator());
    try notify(&reducer, arena.allocator(), "future.unheard-of", "{\"sessionId\":\"session\"}");
    try std.testing.expectEqualStrings("deepseek_unknown_notification", lastFailure(&reducer).?);
}

test "an attempt outside the open owned step is a grammar defect like any other event" {
    try expectRefusal(&.{
        "{\"type\":\"assistant/attempt\",\"data\":{\"turn\":2,\"step\":1}}",
    }, "deepseek_invalid_grammar");
}

test "a child of an earlier run does not decide a later one" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    var reducer = try admittedRun(a);
    try notify(&reducer, a, "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\"}");
    try notify(&reducer, a, "subagent.finished", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\",\"status\":\"error\"}");

    try submit(&reducer);
    try observe(&reducer, try parse(a, "{\"type\":\"agent/inbox/spliced\",\"data\":{\"inserted\":[{\"id\":\"m-2\",\"source\":{\"kind\":\"user\"}}]}}"));
    try receipt(&reducer, "m-2");
    try observe(&reducer, try parse(a, "{\"type\":\"turn/start\",\"data\":{\"turn\":2}}"));
    try observe(&reducer, try parse(a, "{\"type\":\"step/start\",\"data\":{\"turn\":2,\"step\":1}}"));
    try observe(&reducer, try parse(a, "{\"type\":\"user/message\",\"data\":{\"id\":\"m-2\",\"source\":{\"kind\":\"user\"}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"assistant/message\",\"data\":{\"turn\":2,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"turn/end\",\"data\":{\"turn\":2,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&reducer, "idle");

    try std.testing.expect(lastFailure(&reducer) == null);
    try std.testing.expect(reducer.terminal);
}

test "a session whose transport died refuses a later submission" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try transportFailed(&reducer, "gone");
    try std.testing.expectError(Error.SessionUnusable, submit(&reducer));
}

test "activity between runs retires the session, except its own status" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = try admittedRun(a);
    try applyEvent(&reducer, try parse(a, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&reducer, try parse(a, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&reducer, "idle");
    try std.testing.expect(reducer.terminal);

    try notify(&reducer, a, "session.status", "{\"sessionId\":\"session\",\"status\":\"idle\"}");
    try std.testing.expect(!reducer.unusable);
    try submit(&reducer);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    const b = second.allocator();
    var other = try admittedRun(b);
    try applyEvent(&other, try parse(b, "{\"type\":\"assistant/message\",\"data\":{\"turn\":1,\"step\":1,\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"ok\"}]}}}"));
    try applyEvent(&other, try parse(b, "{\"type\":\"turn/end\",\"data\":{\"turn\":1,\"reason\":{\"kind\":\"completed\"}}}"));
    try observeStatus(&other, "idle");
    try notify(&other, b, "session.event", "{\"sessionId\":\"session\",\"event\":{\"type\":\"turn/start\",\"seq\":99,\"data\":{\"turn\":9}}}");
    try std.testing.expect(other.unusable);
    try std.testing.expectError(Error.SessionUnusable, submit(&other));
}

test "a shut-down session takes no further submission or notification" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    var reducer = Reducer.init(a);
    openSession(&reducer);
    close(&reducer);
    try std.testing.expectError(Error.SessionClosed, submit(&reducer));
    try std.testing.expectError(Error.SessionClosed, notify(&reducer, a, "session.status", "{\"sessionId\":\"session\",\"status\":\"idle\"}"));
}

test "activity before the first submission retires the session too" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();

    var reducer = Reducer.init(a);
    openSession(&reducer);
    try notify(&reducer, a, "session.status", "{\"sessionId\":\"session\",\"status\":\"running\"}");
    try std.testing.expect(!reducer.unusable);
    try submit(&reducer);

    var second = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer second.deinit();
    const b = second.allocator();
    var early = Reducer.init(b);
    openSession(&early);
    try notify(&early, b, "session.event", "{\"sessionId\":\"session\",\"event\":{\"type\":\"turn/start\",\"seq\":1,\"data\":{\"turn\":1}}}");
    try std.testing.expect(early.unusable);
    try std.testing.expect(early.pending.items.len == 0);
    try std.testing.expectError(Error.SessionUnusable, submit(&early));

    var third = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer third.deinit();
    const c = third.allocator();
    var child = Reducer.init(c);
    openSession(&child);
    try notify(&child, c, "subagent.started", "{\"parentSessionId\":\"session\",\"childSessionId\":\"c-1\"}");
    try std.testing.expect(child.unusable);
}
