const std = @import("std");

pub const capability_revision = "deepseek-harness-47f9438-oap-v1";
pub const endpoint_id = "deepseek.harness";
pub const execution_owner = "deepseek-harness";
const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";

pub const Error = error{ InvalidFrame, OutOfMemory };

pub const Counters = struct {
    ids: usize = 0,
    clock: i64 = 0,

    pub fn nextID(self: *Counters, arena: std.mem.Allocator, kind: []const u8) ![]const u8 {
        self.ids += 1;
        const letter: u8 = @intCast('a' + self.ids - 1);
        return std.fmt.allocPrint(arena, "{s}-{c}", .{ kind, letter });
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

pub fn open(reducer: *Reducer) !void {
    _ = try reducer.counters.nextID(reducer.arena, "message");
    reducer.message_id = try reducer.counters.nextID(reducer.arena, "message");
    _ = reducer.counters.nextTick();
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
    try reducer.emit("run.failed", .{ .object = payload.* }, true);
}

const ignored_events = [_][]const u8{
    "user/message",   "agent/inbox/spliced", "todo/write",
    "request/header", "request/context",     "session/end-seed",
};

fn sameStep(reducer: *Reducer, data: std.json.Value) bool {
    if (reducer.turn_ended) return false;
    return numberOf(data, "turn") == reducer.turn and numberOf(data, "step") == reducer.step;
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
    if (std.mem.eql(u8, kind, "assistant/attempt")) return;
    if (std.mem.eql(u8, kind, "assistant/message")) {
        if (!sameStep(reducer, data)) return;
        if (memberOf(data, "stream")) |records| try emitStreamRecords(reducer, records);
        reducer.final = data;
        return;
    }
    if (std.mem.eql(u8, kind, "tool/call")) {
        if (!sameStep(reducer, data)) return;
        try startTool(reducer, data);
        return;
    }
    if (std.mem.eql(u8, kind, "tool/result")) {
        if (!sameStep(reducer, data)) return;
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
    try failRun(reducer, "deepseek_unknown_event", "unknown required event");
}

pub fn observeStatus(reducer: *Reducer, status: []const u8) !void {
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
        .args = memberOf(data, "arguments"),
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
                const shape = try reducer.object();
                try shape.put(reducer.arena, "type", Reducer.str("tool_call"));
                try shape.put(reducer.arena, "tool_call_id", Reducer.str(tool.id));
                try shape.put(reducer.arena, "name", Reducer.str(tool.name));
                if (tool.args) |args| try shape.put(reducer.arena, "arguments_json", args);
                try parts.append(reducer.arena, .{ .object = shape.* });
                continue;
            }
            return Error.InvalidFrame;
        }
    }
    if (parts.items.len == 1 and std.mem.eql(u8, textOf(parts.items[0], "type"), "text")) {
        return Reducer.str(textOf(parts.items[0], "text"));
    }
    return .{ .array = std.json.Array.fromOwnedSlice(reducer.arena, try parts.toOwnedSlice(reducer.arena)) };
}

fn trySettle(reducer: *Reducer) !void {
    if (!reducer.turn_ended or !reducer.idle_after_end or reducer.terminal) return;
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

pub fn transportFailed(reducer: *Reducer) !void {
    if (reducer.terminal or !reducer.started) {
        reducer.terminal = true;
        return;
    }
    try failRun(reducer, "deepseek_transport_failed", "the harness transport closed");
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
}
