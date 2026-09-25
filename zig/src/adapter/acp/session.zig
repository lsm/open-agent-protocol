const std = @import("std");
const harness_pins = @import("harness_pins");
const rpc = @import("rpc");
const gojson = @import("gojson");
const goquote = @import("goquote");

pub const capability_revision = harness_pins.acp_capability_revision;
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const endpoint_id = "acp.v1";
pub const harness_owner = "acp-agent";

pub const Error = error{
    RunActive,
    RunNotFound,
    RunTerminal,
    InteractionNotFound,
    InteractionResolved,
    InvalidResolution,
    WrongResponder,
};

pub const Options = struct {
    session_id: []const u8 = "session",
    native_id: []const u8 = "native-session",
    responder: []const u8 = "user",
    model: []const u8 = "",
    endpoint: []const u8 = endpoint_id,
    revision: []const u8 = capability_revision,
    counter: ?*usize = null,
    now_ms: ?*const fn () i64 = null,
    id_style: IdStyle = .letter,
};

pub const IdStyle = enum { letter, decimal };

pub const Identity = struct {
    run_id: []const u8 = "",
    message_ids: []const []const u8 = &.{},
};

pub const Admission = struct {
    submission_id: []const u8 = "",
    message_ids: []const []const u8 = &.{},
};

const Run = struct {
    id: []const u8 = "",
    message_id: []const u8 = "",
    sequence: i64 = 0,
    terminal: bool = false,
    cancel_requested: bool = false,
};

const Tool = struct {
    native_id: []const u8,
    id: []const u8,
    run_id: []const u8,
    title: []const u8 = "",
    name: []const u8 = "",
    kind: []const u8 = "",
    status: []const u8 = "",
    raw_input: ?std.json.Value = null,
    raw_output: ?std.json.Value = null,
    json_content: ?std.json.Value = null,
    locations: ?std.json.Value = null,
    requested: bool = false,
    started: bool = false,
    terminal: bool = false,
};

const Choice = struct {
    id: []const u8,
    name: []const u8,
    kind: []const u8,
};

const Gate = struct {
    id: []const u8,
    run_id: []const u8,
    tool: usize,
    choices: []const Choice,
    requested_event: []const u8 = "",
    resolved: bool = false,
};

const MessageBinding = struct {
    native_id: []const u8,
    id: []const u8,
    text: std.ArrayList(u8) = .empty,
};

const Keep = struct {
    arguments: bool = false,
    arguments_null: bool = false,
    progress: bool = false,
    result: bool = false,
    result_null: bool = false,
    failure: bool = false,
};

const correlated_types = [_][]const u8{
    "action.call.requested",       "action.call.started",
    "action.call.progress",        "action.call.completed",
    "action.call.failed",          "action.call.cancelled",
    "action.permission.requested", "action.permission.resolved",
};

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: usize = 0,
    clock: i64 = 0,
    run: ?Run = null,
    tools: std.ArrayList(Tool) = .empty,
    gates: std.ArrayList(Gate) = .empty,
    messages: std.ArrayList(MessageBinding) = .empty,
    envelopes: std.ArrayList(std.json.Value) = .empty,
    admission: Admission = .{},
    last_sequence: i64 = 0,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options) Reducer {
        return .{ .arena = arena, .options = options };
    }

    pub fn open(self: *Reducer) void {
        _ = self.now();
    }

    fn active(self: *Reducer) ?*Run {
        if (self.run == null) return null;
        const run = &self.run.?;
        return if (run.terminal) null else run;
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        if (self.options.now_ms) |wall| return wall();
        self.clock += 1;
        return self.clock;
    }

    fn nextID(self: *Reducer, kind: []const u8) ![]const u8 {
        const counter = self.options.counter orelse &self.ids;
        counter.* += 1;
        return switch (self.options.id_style) {
            .letter => std.fmt.allocPrint(self.allocator(), "{s}-{u}", .{ kind, idScalar(counter.* - 1) }),
            .decimal => std.fmt.allocPrint(self.allocator(), "{s}-{d}", .{ kind, counter.* }),
        };
    }

    fn newMessageID(self: *Reducer) ![]const u8 {
        const suffix = try self.nextID("message");
        return std.fmt.allocPrint(self.allocator(), "{s}/{s}", .{ self.options.session_id, suffix });
    }

    fn object(self: *Reducer) std.json.ObjectMap {
        _ = self;
        return .empty;
    }

    fn put(self: *Reducer, map: *std.json.ObjectMap, key: []const u8, value: std.json.Value) !void {
        try map.put(self.allocator(), key, value);
    }

    fn str(text: []const u8) std.json.Value {
        return .{ .string = text };
    }

    fn int(value: i64) std.json.Value {
        return .{ .integer = value };
    }

    pub fn submit(self: *Reducer, message_count: usize) !void {
        return self.submitAs(message_count, .{});
    }

    pub fn submitAs(self: *Reducer, message_count: usize, identity: Identity) !void {
        if (self.run) |run| {
            if (!run.terminal) return Error.RunActive;
        }
        const message_ids = try self.allocator().alloc([]const u8, message_count);
        for (message_ids, 0..) |*slot, index| {
            const given = if (index < identity.message_ids.len) identity.message_ids[index] else "";
            slot.* = if (given.len > 0) given else try self.nextID("message");
        }
        const minted_run = try self.nextID("run");
        const run_id = if (identity.run_id.len > 0) identity.run_id else minted_run;
        const message_id = try self.newMessageID();
        _ = self.now();

        self.run = .{ .id = run_id, .message_id = message_id };
        self.messages.clearRetainingCapacity();
        try self.messages.append(self.allocator(), .{ .native_id = "", .id = message_id });

        const run = &self.run.?;
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "status", str("running"));
        if (self.options.model.len > 0) try self.put(&payload, "model_id", str(self.options.model));
        try self.put(&payload, "started_at_ms", int(self.now()));
        _ = try self.emit(run, "run.started", .{ .object = payload });
        const submission_id = try self.nextID("submission");
        self.admission = .{ .submission_id = submission_id, .message_ids = message_ids };
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value) ![]const u8 {
        return self.emitEnvelope(run, kind, payload, "");
    }

    fn emitEnvelope(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, in_reply_to: []const u8) ![]const u8 {
        const id = try self.nextID("event");
        run.sequence += 1;
        self.last_sequence = run.sequence;
        var envelope = self.object();
        try self.put(&envelope, "protocol", str(protocol_name));
        try self.put(&envelope, "version", str(protocol_version));
        try self.put(&envelope, "profile", str(profile));
        try self.put(&envelope, "type", str(kind));
        try self.put(&envelope, "id", str(id));
        try self.put(&envelope, "payload", payload);
        try self.put(&envelope, "sequence", int(run.sequence));
        try self.put(&envelope, "timestamp_ms", int(self.now()));
        if (in_reply_to.len > 0) try self.put(&envelope, "in_reply_to", str(in_reply_to));
        try self.put(&envelope, "session_id", str(self.options.session_id));
        try self.put(&envelope, "run_id", str(run.id));
        if (correlatedType(kind)) {
            if (payload == .object) {
                const carried = payload.object.get("tool_call_id") orelse std.json.Value{ .null = {} };
                if (carried == .string) try self.put(&envelope, "tool_call_id", carried);
            }
        }
        try self.put(&envelope, "capability_revision", str(self.options.revision));
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
        return id;
    }

    fn textFor(self: *Reducer, message_id: []const u8) *MessageBinding {
        for (self.messages.items) |*binding| {
            if (std.mem.eql(u8, binding.id, message_id)) return binding;
        }
        self.messages.append(self.allocator(), .{ .native_id = "", .id = message_id }) catch {};
        return &self.messages.items[self.messages.items.len - 1];
    }

    pub fn settlePrompt(self: *Reducer, stop_reason: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (run.terminal) return;

        if (std.mem.eql(u8, stop_reason, "end_turn") or
            std.mem.eql(u8, stop_reason, "max_tokens") or
            std.mem.eql(u8, stop_reason, "max_turn_requests"))
        {
            try self.settleChildren(run, false);
            const binding = self.textFor(run.message_id);
            var response = self.object();
            try self.put(&response, "id", str(run.message_id));
            try self.put(&response, "role", str("assistant"));
            try self.put(&response, "content", str(binding.text.items));
            var payload = try self.terminalPayload(run);
            try self.put(&payload, "final_response", .{ .object = response });
            try self.put(&payload, "stop_reason", str(stop_reason));
            _ = try self.emit(run, "run.completed", .{ .object = payload });
            run.terminal = true;
            return;
        }
        if (std.mem.eql(u8, stop_reason, "cancelled")) {
            try self.settleChildren(run, true);
            try self.emitCancelled(run, "ACP prompt returned cancelled");
            return;
        }
        if (std.mem.eql(u8, stop_reason, "refusal")) {
            try self.settleChildren(run, true);
            try self.emitFailure(run, "refusal", "agent refused the prompt", "");
            return;
        }
        try self.settleChildren(run, true);
        const detail = try std.fmt.allocPrint(self.allocator(), "unsupported ACP stop reason {s}", .{goquote.quote(self.allocator(), stop_reason)});
        try self.emitFailure(run, "acp_invalid_stop_reason", detail, "");
    }

    pub fn promptFailed(self: *Reducer, code: i64, request: std.json.Value, message: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (run.terminal) return;
        if (code == -32800 and run.cancel_requested) {
            try self.settleChildren(run, true);
            try self.emitCancelled(run, "ACP prompt cancellation confirmed");
            return;
        }
        const named = switch (request) {
            .string => |text| goquote.quote(self.allocator(), text),
            .integer => |value| try std.fmt.allocPrint(self.allocator(), "{d}", .{value}),
            else => "<unset>",
        };
        const detail = try std.fmt.allocPrint(self.allocator(), "acp rpc error {d} for request {s}: {s}", .{ code, named, message });
        try self.settleChildren(run, true);
        try self.emitFailure(run, "acp_prompt_error", detail, "");
    }

    pub fn cancel(self: *Reducer) !void {
        if (self.run == null) return Error.RunNotFound;
        const run = &self.run.?;
        if (run.terminal) return Error.RunTerminal;
        if (run.cancel_requested) return;
        run.cancel_requested = true;
        var payload = try self.terminalPayload(run);
        try self.put(&payload, "status", str("cancelling"));
        try self.put(&payload, "updated_at_ms", int(self.now()));
        _ = try self.emit(run, "run.status.updated", .{ .object = payload });
    }

    fn terminalPayload(self: *Reducer, run: *Run) !std.json.ObjectMap {
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        return payload;
    }

    fn emitCancelled(self: *Reducer, run: *Run, reason: []const u8) !void {
        var payload = try self.terminalPayload(run);
        try self.put(&payload, "reason", str(reason));
        _ = try self.emit(run, "run.cancelled", .{ .object = payload });
        run.terminal = true;
    }

    fn emitFailure(self: *Reducer, run: *Run, code: []const u8, message: []const u8, settled_by: []const u8) !void {
        var failure = self.object();
        try self.put(&failure, "code", str(code));
        try self.put(&failure, "message", str(message));
        var payload = try self.terminalPayload(run);
        try self.put(&payload, "error", .{ .object = failure });
        if (settled_by.len > 0) try self.put(&payload, "settled_by", str(settled_by));
        _ = try self.emit(run, "run.failed", .{ .object = payload });
        run.terminal = true;
    }

    fn settleChildren(self: *Reducer, run: *Run, cancelling: bool) !void {
        var gate_index: usize = 0;
        while (gate_index < self.gates.items.len) : (gate_index += 1) {
            const gate = &self.gates.items[gate_index];
            if (!std.mem.eql(u8, gate.run_id, run.id) or gate.resolved) continue;
            gate.resolved = true;
            const gate_id = gate.id;
            const requested_event = gate.requested_event;
            const tool_id = self.tools.items[gate.tool].id;
            var reason = self.object();
            try self.put(&reason, "code", str("run_settled"));
            try self.put(&reason, "message", str("parent run settled the permission request"));
            var payload = self.object();
            try self.put(&payload, "interaction_id", str(gate_id));
            try self.put(&payload, "requested_by", str(self.options.endpoint));
            try self.put(&payload, "responded_by", str(self.options.responder));
            try self.put(&payload, "session_id", str(self.options.session_id));
            try self.put(&payload, "run_id", str(run.id));
            try self.put(&payload, "tool_call_id", str(tool_id));
            try self.put(&payload, "outcome", str("cancelled"));
            try self.put(&payload, "reason", .{ .object = reason });
            _ = try self.emitEnvelope(run, "action.permission.resolved", .{ .object = payload }, requested_event);
        }

        var tool_index: usize = 0;
        while (tool_index < self.tools.items.len) : (tool_index += 1) {
            const tool = &self.tools.items[tool_index];
            if (!std.mem.eql(u8, tool.run_id, run.id) or tool.terminal) continue;
            tool.terminal = true;
            if (!tool.started and !cancelling) {
                tool.started = true;
                const started = try self.toolPayload(tool, .{ .arguments = true });
                _ = try self.emit(run, "action.call.started", .{ .object = started });
            }
            var payload = try self.toolPayload(tool, .{});
            if (!cancelling) {
                var failure = self.object();
                try self.put(&failure, "code", str("incomplete_tool"));
                try self.put(&failure, "message", str("prompt completed with unfinished ACP tool"));
                try self.put(&payload, "error", .{ .object = failure });
            }
            const kind = if (cancelling) "action.call.cancelled" else "action.call.failed";
            _ = try self.emit(run, kind, .{ .object = payload });
        }
    }

    fn failActive(self: *Reducer, code: []const u8, message: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (run.terminal) return;
        try self.settleChildren(run, true);
        try self.emitFailure(run, code, message, "");
    }

    pub fn transportFailed(self: *Reducer, detail: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (run.terminal) return;
        try self.settleChildren(run, true);
        try self.emitFailure(run, "acp_transport_failure", detail, "inferred");
    }

    pub fn observe(self: *Reducer, message: rpc.Message, parsed: std.json.Value) !void {
        if (parsed != .object) return;
        if (message.kind == .request) {
            try self.handleRequest(message.method, parsed.object);
            return;
        }
        if (message.kind != .notification) return;
        if (!std.mem.eql(u8, message.method, "session/update")) return;
        const params = parsed.object.get("params") orelse std.json.Value{ .null = {} };
        if (params != .object) {
            try self.failActive("acp_invalid_update", "malformed or foreign session/update");
            return;
        }
        const native_session = gojson.foldedSet(params.object, &.{"sessionId"}) orelse std.json.Value{ .null = {} };
        if (gojson.foldedWrongType(params.object, &.{"sessionId"}, .string) or native_session != .string or !std.mem.eql(u8, native_session.string, self.options.native_id)) {
            try self.failActive("acp_invalid_update", "malformed or foreign session/update");
            return;
        }
        const update = gojson.foldedLast(params.object, &.{"update"}) orelse {
            try self.failActive("acp_invalid_update", "malformed session update");
            return;
        };
        if (update != .object and update != .null) {
            try self.failActive("acp_invalid_update", "malformed session update");
            return;
        }
        if (self.active() == null) return;
        if (update == .null) {
            try self.failActive("acp_unknown_update", "unknown stable ACP session update");
            return;
        }

        if (gojson.foldedWrongType(update.object, &.{"sessionUpdate"}, .string)) {
            try self.failActive("acp_invalid_update", "malformed session update");
            return;
        }
        const kind = gojson.foldedSet(update.object, &.{"sessionUpdate"}) orelse std.json.Value{ .null = {} };
        if (kind != .string) {
            if (kind == .null) {
                try self.failActive("acp_unknown_update", "unknown stable ACP session update");
            } else {
                try self.failActive("acp_invalid_update", "malformed session update");
            }
            return;
        }
        try self.applyUpdate(update.object, kind.string);
    }

    fn applyUpdate(self: *Reducer, update: std.json.ObjectMap, kind: []const u8) !void {
        if (std.mem.eql(u8, kind, "agent_message_chunk")) {
            try self.applyChunk(update);
            return;
        }
        if (std.mem.eql(u8, kind, "tool_call")) {
            if (!toolCallDecodes(update)) {
                try self.failActive("acp_invalid_tool_call", "malformed tool call");
                return;
            }
            _ = try self.applyToolCall(update, null);
            return;
        }
        if (std.mem.eql(u8, kind, "tool_call_update")) {
            if (!toolUpdateDecodes(update)) {
                try self.failActive("acp_invalid_tool_update", "malformed tool update");
                return;
            }
            try self.applyToolUpdate(update);
            return;
        }
        if (ignoredUpdate(kind)) return;
        if (kind.len > 0 and kind[0] == '_') return;
        try self.failActive("acp_unknown_update", "unknown stable ACP session update");
    }

    fn applyChunk(self: *Reducer, update: std.json.ObjectMap) !void {
        if (gojson.foldedWrongType(update, &.{"content"}, .object) or !chunkDecodes(update)) {
            try self.failActive("acp_invalid_message_chunk", "unsupported assistant chunk");
            return;
        }
        const content_type = gojson.foldedSet(update, &.{ "content", "type" }) orelse std.json.Value{ .null = {} };
        if (content_type != .string or !std.mem.eql(u8, content_type.string, "text")) {
            try self.failActive("acp_invalid_message_chunk", "unsupported assistant chunk");
            return;
        }
        const text = stringAt(update, &.{ "content", "text" });

        const run = &self.run.?;
        var message_id = run.message_id;
        const native_message = gojson.foldedSet(update, &.{"messageId"}) orelse std.json.Value{ .null = {} };
        if (native_message == .string and native_message.string.len > 0) {
            message_id = try self.bindMessage(native_message.string);
            run.message_id = message_id;
        }
        const binding = self.textFor(message_id);
        try binding.text.appendSlice(self.allocator(), text);

        var part = self.object();
        try self.put(&part, "type", str("text"));
        try self.put(&part, "text", str(text));
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "message_id", str(message_id));
        try self.put(&payload, "part", .{ .object = part });
        _ = try self.emit(run, "content.delta", .{ .object = payload });
    }

    fn bindMessage(self: *Reducer, native_id: []const u8) ![]const u8 {
        var bound: usize = 0;
        for (self.messages.items) |binding| {
            if (binding.native_id.len == 0) continue;
            bound += 1;
            if (std.mem.eql(u8, binding.native_id, native_id)) return binding.id;
        }
        const run = &self.run.?;
        if (bound == 0) {
            try self.messages.append(self.allocator(), .{ .native_id = native_id, .id = run.message_id });
            return run.message_id;
        }
        const minted = try self.newMessageID();
        try self.messages.append(self.allocator(), .{ .native_id = native_id, .id = minted });
        return minted;
    }

    fn findTool(self: *Reducer, native_id: []const u8) ?usize {
        for (self.tools.items, 0..) |tool, index| {
            if (std.mem.eql(u8, tool.native_id, native_id)) return index;
        }
        return null;
    }

    fn applyToolCall(self: *Reducer, update: std.json.ObjectMap, parent: ?[]const u8) !bool {
        const native_id = nestedString(update, parent, "toolCallId");
        const title = nestedString(update, parent, "title");
        if (native_id.len == 0 or title.len == 0) {
            try self.failActive("acp_invalid_tool_call", "tool id and title are required");
            return false;
        }
        const run = &self.run.?;
        var index = self.findTool(native_id);
        if (index) |found| {
            if (!std.mem.eql(u8, self.tools.items[found].run_id, run.id)) {
                try self.failActive("acp_tool_id_reuse", "tool id reused across prompts");
                return false;
            }
        } else {
            const minted = try self.nextID("tool-call");
            try self.tools.append(self.allocator(), .{ .native_id = native_id, .id = minted, .run_id = run.id });
            index = self.tools.items.len - 1;
        }
        const at = index.?;
        if (self.tools.items[at].terminal) {
            try self.failActive("acp_tool_after_terminal", "tool updated after terminal");
            return false;
        }
        const tool = &self.tools.items[at];
        tool.title = title;
        tool.name = nestedName(update, parent);
        tool.kind = nestedString(update, parent, "kind");
        tool.raw_input = nestedLast(update, parent, "rawInput");
        tool.raw_output = nestedLast(update, parent, "rawOutput");
        tool.json_content = nestedLast(update, parent, "content");
        tool.locations = nestedLast(update, parent, "locations");
        const first = !tool.requested;
        tool.requested = true;
        const status = nestedString(update, parent, "status");
        if (first) {
            const payload = try self.toolPayload(tool, .{ .arguments = true, .arguments_null = true });
            _ = try self.emit(run, "action.call.requested", .{ .object = payload });
        }
        try self.applyToolStatus(at, status);
        return true;
    }

    fn applyToolUpdate(self: *Reducer, update: std.json.ObjectMap) !void {
        const native_id = stringMember(update, "toolCallId");
        const run = &self.run.?;
        const index = self.findTool(native_id) orelse {
            try self.failActive("acp_tool_patch_without_call", "tool patch before creation");
            return;
        };
        if (!std.mem.eql(u8, self.tools.items[index].run_id, run.id)) {
            try self.failActive("acp_tool_patch_without_call", "tool patch before creation");
            return;
        }
        if (self.tools.items[index].terminal) {
            try self.failActive("acp_tool_after_terminal", "tool updated after terminal");
            return;
        }
        const tool = &self.tools.items[index];
        if (presentString(update, "title")) |value| {
            if (value.len > 0) tool.title = value;
        }
        if (presentString(update, "name")) |value| {
            if (value.len > 0) tool.name = value;
        }
        if (presentString(update, "kind")) |value| tool.kind = value;
        if (gojson.foldedLast(update, &.{"rawInput"})) |value| tool.raw_input = value;
        if (gojson.foldedLast(update, &.{"rawOutput"})) |value| tool.raw_output = value;
        if (gojson.foldedLast(update, &.{"content"})) |value| tool.json_content = value;
        if (gojson.foldedLast(update, &.{"locations"})) |value| tool.locations = value;
        const started = tool.started;
        const status = presentString(update, "status") orelse "";
        if (status.len == 0) {
            if (started) {
                const payload = try self.toolPayload(tool, .{ .progress = true });
                _ = try self.emit(run, "action.call.progress", .{ .object = payload });
            }
            return;
        }
        try self.applyToolStatus(index, status);
    }

    fn applyToolStatus(self: *Reducer, index: usize, status: []const u8) !void {
        if (status.len == 0 or std.mem.eql(u8, status, "pending")) return;
        const tool = &self.tools.items[index];
        if (tool.terminal) return;
        var kind: []const u8 = "action.call.progress";
        var keep = Keep{ .arguments = true, .progress = true, .result = true };
        var synthesize = false;
        if (std.mem.eql(u8, status, "in_progress")) {
            if (!tool.started) {
                kind = "action.call.started";
                keep = .{ .arguments = true };
                tool.started = true;
            }
        } else if (std.mem.eql(u8, status, "completed")) {
            kind = "action.call.completed";
            keep = .{ .result = true, .result_null = true };
            synthesize = !tool.started;
            tool.started = true;
            tool.terminal = true;
        } else if (std.mem.eql(u8, status, "failed")) {
            kind = "action.call.failed";
            keep = .{ .failure = true };
            synthesize = !tool.started;
            tool.started = true;
            tool.terminal = true;
        } else {
            try self.failActive("acp_invalid_tool_status", "unknown tool status");
            return;
        }
        tool.status = status;
        const run = &self.run.?;
        if (synthesize) {
            const started = try self.toolPayload(tool, .{ .arguments = true });
            _ = try self.emit(run, "action.call.started", .{ .object = started });
        }
        const payload = try self.toolPayload(tool, keep);
        _ = try self.emit(run, kind, .{ .object = payload });
    }

    fn toolPayload(self: *Reducer, tool: *const Tool, keep: Keep) !std.json.ObjectMap {
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(tool.run_id));
        try self.put(&payload, "tool_call_id", str(tool.id));
        try self.put(&payload, "requested_by", str(self.options.endpoint));
        try self.put(&payload, "execution_owner", str(harness_owner));
        const name = if (tool.name.len > 0) tool.name else tool.title;
        if (name.len > 0) try self.put(&payload, "name", str(name));
        if (keep.arguments) {
            if (tool.raw_input) |value| {
                try self.put(&payload, "arguments_json", value);
            } else if (keep.arguments_null) {
                try self.put(&payload, "arguments_json", .{ .null = {} });
            }
        }
        if (keep.progress) {
            var progress = self.object();
            try self.put(&progress, "content", tool.json_content orelse .{ .null = {} });
            try self.put(&progress, "locations", tool.locations orelse .{ .null = {} });
            try self.put(&payload, "progress", .{ .object = progress });
        }
        if (keep.result) {
            if (tool.raw_output) |value| {
                try self.put(&payload, "result", value);
            } else if (keep.result_null) {
                try self.put(&payload, "result", .{ .null = {} });
            }
        }
        if (keep.failure) {
            var failure = self.object();
            try self.put(&failure, "code", str("tool_failed"));
            try self.put(&failure, "message", str("ACP tool call failed"));
            try self.put(&payload, "error", .{ .object = failure });
        }
        return payload;
    }

    fn handleRequest(self: *Reducer, method: []const u8, request: std.json.ObjectMap) !void {
        if (!std.mem.eql(u8, method, "session/request_permission")) return;
        const params = request.get("params") orelse std.json.Value{ .null = {} };
        if (!permissionDecodes(params, self.options.native_id)) {
            try self.failActive("acp_invalid_permission", "malformed permission request");
            return;
        }
        const options = gojson.foldedSet(params.object, &.{"options"}).?.array;
        if (self.active() == null) return;

        if (!try self.applyToolCall(params.object, "toolCall")) return;
        const index = self.findTool(stringAt(params.object, &.{ "toolCall", "toolCallId" })).?;
        const run = &self.run.?;

        const id = try self.nextID("interaction");
        var kept = std.ArrayList(Choice).empty;
        var choices = std.json.Array.init(self.allocator());
        for (options.items) |entry| {
            if (entry != .object) continue;
            const option_id = stringMember(entry.object, "optionId");
            if (option_id.len == 0) continue;
            const name = stringMember(entry.object, "name");
            if (name.len == 0) continue;
            const kind = stringMember(entry.object, "kind");
            if (!definedOptionKind(kind)) continue;
            try kept.append(self.allocator(), .{ .id = option_id, .name = name, .kind = kind });
            var choice = self.object();
            try self.put(&choice, "id", str(option_id));
            try self.put(&choice, "label", str(name));
            try self.put(&choice, "description", str(kind));
            try choices.append(.{ .object = choice });
        }
        if (kept.items.len == 0) {
            try self.failActive("acp_invalid_permission", "empty permission options");
            return;
        }
        try self.gates.append(self.allocator(), .{ .id = id, .run_id = run.id, .tool = index, .choices = kept.items });
        const gate = &self.gates.items[self.gates.items.len - 1];

        var payload = self.object();
        try self.put(&payload, "interaction_id", str(id));
        try self.put(&payload, "requested_by", str(self.options.endpoint));
        try self.put(&payload, "responded_by", str(self.options.responder));
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "tool_call_id", str(self.tools.items[index].id));
        try self.put(&payload, "title", str(stringAt(params.object, &.{ "toolCall", "title" })));
        try self.put(&payload, "choices", .{ .array = choices });
        if (self.tools.items[index].raw_input) |value| try self.put(&payload, "arguments_json", value);
        gate.requested_event = try self.emit(run, "action.permission.requested", .{ .object = payload });
    }

    pub fn pendingInteraction(self: *Reducer) ?[]const u8 {
        for (self.gates.items) |gate| {
            if (!gate.resolved) return gate.id;
        }
        return null;
    }

    pub fn resolve(self: *Reducer, interaction_id: []const u8, run_id: []const u8, responder: []const u8, choice_id: []const u8, granted: bool) !void {
        var found: ?usize = null;
        for (self.gates.items, 0..) |gate, index| {
            if (std.mem.eql(u8, gate.id, interaction_id)) found = index;
        }
        const at = found orelse return Error.InteractionNotFound;
        if (self.gates.items[at].resolved) return Error.InteractionResolved;
        if (!std.mem.eql(u8, responder, self.options.responder)) return Error.WrongResponder;
        if (!std.mem.eql(u8, self.gates.items[at].run_id, run_id)) return Error.InvalidResolution;
        if (self.run == null) return Error.InvalidResolution;
        const run = &self.run.?;

        var chosen: ?Choice = null;
        for (self.gates.items[at].choices) |choice| {
            if (std.mem.eql(u8, choice.id, choice_id)) chosen = choice;
        }
        const option = chosen orelse return Error.InvalidResolution;
        const allows = std.mem.eql(u8, option.kind, "allow_once") or std.mem.eql(u8, option.kind, "allow_always");
        if (allows != granted) return Error.InvalidResolution;
        if (run.terminal) return;

        const gate = &self.gates.items[at];
        gate.resolved = true;
        const requested_event = gate.requested_event;
        const tool_id = self.tools.items[gate.tool].id;

        var payload = self.object();
        try self.put(&payload, "interaction_id", str(interaction_id));
        try self.put(&payload, "requested_by", str(self.options.endpoint));
        try self.put(&payload, "responded_by", str(self.options.responder));
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "tool_call_id", str(tool_id));
        try self.put(&payload, "outcome", str(if (allows) "resolved" else "rejected"));
        try self.put(&payload, "choice_id", str(option.id));
        try self.put(&payload, "granted", .{ .bool = allows });
        _ = try self.emitEnvelope(run, "action.permission.resolved", .{ .object = payload }, requested_event);
    }
};

fn idScalar(index: usize) u21 {
    return std.math.cast(u21, 'a' + index) orelse 0xFFFD;
}

const content_block_strings = [_][]const u8{ "type", "text", "data", "mimeType", "uri" };

fn chunkDecodes(update: std.json.ObjectMap) bool {
    if (!typedString(update, "messageId")) return false;
    for (content_block_strings) |key| {
        if (gojson.foldedWrongType(update, &.{ "content", key }, .string)) return false;
    }
    return true;
}

fn correlatedType(kind: []const u8) bool {
    for (correlated_types) |known| {
        if (std.mem.eql(u8, known, kind)) return true;
    }
    return false;
}

fn stringAt(map: std.json.ObjectMap, path: []const []const u8) []const u8 {
    const value = gojson.foldedSet(map, path) orelse return "";
    return if (value == .string) value.string else "";
}

fn stringMember(map: std.json.ObjectMap, key: []const u8) []const u8 {
    return stringAt(map, &.{key});
}

fn nestedString(map: std.json.ObjectMap, parent: ?[]const u8, key: []const u8) []const u8 {
    if (parent) |name| return stringAt(map, &.{ name, key });
    return stringAt(map, &.{key});
}

fn nestedLast(map: std.json.ObjectMap, parent: ?[]const u8, key: []const u8) ?std.json.Value {
    if (parent) |name| return gojson.foldedLast(map, &.{ name, key });
    return gojson.foldedLast(map, &.{key});
}

fn nestedName(map: std.json.ObjectMap, parent: ?[]const u8) []const u8 {
    const value = nestedLast(map, parent, "name") orelse return "";
    return if (value == .string) value.string else "";
}

fn presentString(map: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = gojson.foldedLast(map, &.{key}) orelse return null;
    return if (value == .string) value.string else null;
}

fn typedString(map: std.json.ObjectMap, key: []const u8) bool {
    return !gojson.foldedWrongType(map, &.{key}, .string);
}

const tool_call_strings = [_][]const u8{ "sessionUpdate", "toolCallId", "title", "kind", "status" };

fn toolCallDecodes(update: std.json.ObjectMap) bool {
    for (tool_call_strings) |key| {
        if (!typedString(update, key)) return false;
    }
    return true;
}

fn nestedToolCallDecodes(params: std.json.ObjectMap) bool {
    for (tool_call_strings) |key| {
        if (gojson.foldedWrongType(params, &.{ "toolCall", key }, .string)) return false;
    }
    return true;
}

fn toolUpdateDecodes(update: std.json.ObjectMap) bool {
    return toolCallDecodes(update);
}

const permission_option_strings = [_][]const u8{ "optionId", "name", "kind" };

const permission_option_kinds = [_][]const u8{ "allow_once", "allow_always", "reject_once", "reject_always" };

fn definedOptionKind(kind: []const u8) bool {
    for (permission_option_kinds) |known| {
        if (std.mem.eql(u8, known, kind)) return true;
    }
    return false;
}

fn permissionDecodes(params: std.json.Value, native_id: []const u8) bool {
    if (params != .object) return false;
    const session = gojson.foldedSet(params.object, &.{"sessionId"}) orelse return false;
    if (session != .string or !std.mem.eql(u8, session.string, native_id)) return false;
    if (gojson.foldedWrongType(params.object, &.{"sessionId"}, .string)) return false;
    const tool_call = gojson.foldedSet(params.object, &.{"toolCall"}) orelse return false;
    if (gojson.foldedWrongType(params.object, &.{"toolCall"}, .object)) return false;
    if (tool_call != .object or !nestedToolCallDecodes(params.object)) return false;
    if (stringAt(params.object, &.{ "toolCall", "toolCallId" }).len == 0) return false;
    if (gojson.foldedWrongType(params.object, &.{"options"}, .array)) return false;
    const options = gojson.foldedLast(params.object, &.{"options"}) orelse return false;
    if (options != .array or options.array.items.len == 0) return false;
    for (options.array.items) |entry| {
        if (entry == .null) continue;
        if (entry != .object) return false;
        for (permission_option_strings) |key| {
            if (!typedString(entry.object, key)) return false;
        }
    }
    return true;
}

const ignored_updates = [_][]const u8{
    "user_message_chunk",  "agent_thought_chunk",  "plan",
    "plan_update",         "plan_removed",         "available_commands_update",
    "current_mode_update", "config_option_update", "session_info_update",
    "usage_update",
};

fn ignoredUpdate(kind: []const u8) bool {
    for (ignored_updates) |known| {
        if (std.mem.eql(u8, known, kind)) return true;
    }
    return false;
}

const testing = std.testing;

fn wrap(comptime body: []const u8) []const u8 {
    return "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"native-session\",\"update\":" ++ body ++ "}}";
}

fn feed(reducer: *Reducer, scratch: std.mem.Allocator, text: []const u8) !void {
    const message = try rpc.parseMessage(scratch, text);
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, text, .{});
    try reducer.observe(message, parsed);
}

fn typeAt(reducer: *Reducer, index: usize) []const u8 {
    return reducer.envelopes.items[index].object.get("type").?.string;
}

fn payloadAt(reducer: *Reducer, index: usize) std.json.ObjectMap {
    return reducer.envelopes.items[index].object.get("payload").?.object;
}

fn codeAt(reducer: *Reducer, index: usize) []const u8 {
    return payloadAt(reducer, index).get("error").?.object.get("code").?.string;
}

fn messageAt(reducer: *Reducer, index: usize) []const u8 {
    return payloadAt(reducer, index).get("error").?.object.get("message").?.string;
}

fn openRun(arena: *std.heap.ArenaAllocator) !Reducer {
    var reducer = Reducer.init(arena, .{});
    reducer.open();
    try reducer.submit(1);
    return reducer;
}

fn expectRefusal(text: []const u8, code: []const u8, message: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), text);

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 1));
    try testing.expectEqualStrings(code, codeAt(&reducer, 1));
    try testing.expectEqualStrings(message, messageAt(&reducer, 1));
    try testing.expect(payloadAt(&reducer, 1).get("settled_by") == null);
}

test "one counter mints every id and the derived order fixes which letter each takes" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);

    try testing.expectEqualStrings("run-b", reducer.run.?.id);
    try testing.expectEqualStrings("session/message-c", reducer.run.?.message_id);
    try testing.expectEqualStrings("event-d", reducer.envelopes.items[0].object.get("id").?.string);
    try testing.expectEqual(@as(i64, 3), payloadAt(&reducer, 0).get("started_at_ms").?.integer);
    try testing.expectEqual(@as(i64, 4), reducer.envelopes.items[0].object.get("timestamp_ms").?.integer);
    try testing.expectEqual(@as(i64, 1), reducer.envelopes.items[0].object.get("sequence").?.integer);
}

test "a submission id is burned after run.started so the next event skips a letter" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}
    ));

    try testing.expectEqualStrings("event-f", reducer.envelopes.items[1].object.get("id").?.string);
}

test "a second submit is refused while the run is still open" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try testing.expectError(Error.RunActive, reducer.submit(1));
}

test "params that are absent, not an object, or name another session are refused" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update"}
    , "acp_invalid_update", "malformed or foreign session/update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":[]}
    , "acp_invalid_update", "malformed or foreign session/update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"other","update":{"sessionUpdate":"plan"}}}
    , "acp_invalid_update", "malformed or foreign session/update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"plan"}}}
    , "acp_invalid_update", "malformed or foreign session/update");
}

test "an update member that is absent or wrongly typed is malformed, while a null one is unknown" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session"}}
    , "acp_invalid_update", "malformed session update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":"plan"}}
    , "acp_invalid_update", "malformed session update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":[]}}
    , "acp_invalid_update", "malformed session update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":7}}
    , "acp_invalid_update", "malformed session update");

    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":null}}
    , "acp_unknown_update", "unknown stable ACP session update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{}}}
    , "acp_unknown_update", "unknown stable ACP session update");
}

test "a null update before any run is dropped rather than refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();
    try feed(&reducer, arena.allocator(),
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":null}}
    );
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
}

test "a discriminator that is absent or null is unknown, and one of the wrong type is malformed" {
    try expectRefusal(wrap(
        \\{"kind":"read"}
    ), "acp_unknown_update", "unknown stable ACP session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":null}
    ), "acp_unknown_update", "unknown stable ACP session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":""}
    ), "acp_unknown_update", "unknown stable ACP session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"invented_later"}
    ), "acp_unknown_update", "unknown stable ACP session update");

    try expectRefusal(wrap(
        \\{"sessionUpdate":7}
    ), "acp_invalid_update", "malformed session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":true}
    ), "acp_invalid_update", "malformed session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":{}}
    ), "acp_invalid_update", "malformed session update");
}

test "the ten ignored updates and any underscore-prefixed kind emit nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);

    for (ignored_updates) |kind| {
        const text = try std.fmt.allocPrint(scratch, "{s}{s}{s}", .{
            "{\"jsonrpc\":\"2.0\",\"method\":\"session/update\",\"params\":{\"sessionId\":\"native-session\",\"update\":{\"sessionUpdate\":\"",
            kind,
            "\"}}}",
        });
        try feed(&reducer, scratch, text);
    }
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"_vendor_private"}
    ));

    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
}

test "a chunk carrying anything but a text content block is refused" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk"}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":"hi"}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"image","data":"x"}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
}

test "a text block with no text member is accepted and contributes nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text"}}
    ));
    try reducer.settlePrompt("end_turn");

    try testing.expectEqualStrings("content.delta", typeAt(&reducer, 1));
    try testing.expectEqualStrings("", payloadAt(&reducer, 1).get("part").?.object.get("text").?.string);
    try testing.expectEqualStrings("", payloadAt(&reducer, 2).get("final_response").?.object.get("content").?.string);
}

test "the first native message id adopts the run message and the second mints its own" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"one"}}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":"m2","content":{"type":"text","text":"two"}}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"three"}}
    ));
    try reducer.settlePrompt("end_turn");

    try testing.expectEqualStrings("session/message-c", payloadAt(&reducer, 1).get("message_id").?.string);
    try testing.expectEqualStrings("session/message-g", payloadAt(&reducer, 2).get("message_id").?.string);
    try testing.expectEqualStrings("session/message-c", payloadAt(&reducer, 3).get("message_id").?.string);

    const final = payloadAt(&reducer, 4).get("final_response").?.object;
    try testing.expectEqualStrings("session/message-c", final.get("id").?.string);
    try testing.expectEqualStrings("onethree", final.get("content").?.string);
}

test "an empty native message id leaves the run message binding untouched" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":"","content":{"type":"text","text":"hi"}}
    ));

    try testing.expectEqualStrings("session/message-c", payloadAt(&reducer, 1).get("message_id").?.string);
    try testing.expectEqual(@as(usize, 1), reducer.messages.items.len);
}

test "the three completing stop reasons carry the reason they settled on" {
    for ([_][]const u8{ "end_turn", "max_tokens", "max_turn_requests" }) |reason| {
        var arena = std.heap.ArenaAllocator.init(testing.allocator);
        defer arena.deinit();
        var reducer = try openRun(&arena);
        try reducer.settlePrompt(reason);

        try testing.expectEqualStrings("run.completed", typeAt(&reducer, 1));
        try testing.expectEqualStrings(reason, payloadAt(&reducer, 1).get("stop_reason").?.string);
    }
}

test "cancelled and refusal settle differently and an unknown stop reason is quoted back" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var cancelled = try openRun(&arena);
    try cancelled.settlePrompt("cancelled");
    try testing.expectEqualStrings("run.cancelled", typeAt(&cancelled, 1));
    try testing.expectEqualStrings("ACP prompt returned cancelled", payloadAt(&cancelled, 1).get("reason").?.string);

    var refused = try openRun(&arena);
    try refused.settlePrompt("refusal");
    try testing.expectEqualStrings("run.failed", typeAt(&refused, 1));
    try testing.expectEqualStrings("refusal", codeAt(&refused, 1));
    try testing.expectEqualStrings("agent refused the prompt", messageAt(&refused, 1));

    var unknown = try openRun(&arena);
    try unknown.settlePrompt("end_of_days");
    try testing.expectEqualStrings("acp_invalid_stop_reason", codeAt(&unknown, 1));
    try testing.expectEqualStrings("unsupported ACP stop reason \"end_of_days\"", messageAt(&unknown, 1));

    var awkward = try openRun(&arena);
    try awkward.settlePrompt("bad\"\n");
    try testing.expectEqualStrings("unsupported ACP stop reason \"bad\\\"\\n\"", messageAt(&awkward, 1));

    var exotic = try openRun(&arena);
    try exotic.settlePrompt("zero\u{200b}width");
    try testing.expectEqualStrings("unsupported ACP stop reason \"zero\\u200bwidth\"", messageAt(&exotic, 1));
}

test "a settled run absorbs every later frame without emitting or minting" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try reducer.settlePrompt("end_turn");
    const settled = reducer.ids;

    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"late"}}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"invented_later"}
    ));
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, permission_frame);
    try reducer.settlePrompt("cancelled");
    try reducer.transportFailed("gone");
    try reducer.promptFailed(-32603, .{ .integer = 3 }, "gone");

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqual(settled, reducer.ids);
    try testing.expectEqual(@as(usize, 0), reducer.tools.items.len);
    try testing.expectEqual(@as(usize, 0), reducer.gates.items.len);
}

test "a tool id seen only after the run settled does not poison the next run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try reducer.settlePrompt("end_turn");
    try feed(&reducer, scratch, tool_call_frame);
    try reducer.submit(1);
    try feed(&reducer, scratch, tool_call_frame);

    try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 3));
}

fn expectLetterBytes(index: usize, want: []const u8) !void {
    const text = try std.fmt.allocPrint(testing.allocator, "{u}", .{idScalar(index)});
    defer testing.allocator.free(text);
    try testing.expectEqualSlices(u8, want, text);
}

test "the id letter is the byte sequence the oracle rune conversion produces" {
    try expectLetterBytes(0, "\x61");
    try expectLetterBytes(25, "\x7a");
    try expectLetterBytes(26, "\x7b");
    try expectLetterBytes(30, "\x7f");
    try expectLetterBytes(31, "\xc2\x80");
    try expectLetterBytes(159, "\xc4\x80");
    try expectLetterBytes(0xD800 - 'a', "\xef\xbf\xbd");
    try expectLetterBytes(0xDFFF - 'a', "\xef\xbf\xbd");
    try expectLetterBytes(0xE000 - 'a', "\xee\x80\x80");
    try expectLetterBytes(0x10FFFF - 'a', "\xf4\x8f\xbf\xbf");
    try expectLetterBytes(0x110000 - 'a', "\xef\xbf\xbd");
    try expectLetterBytes(0x200000 - 'a', "\xef\xbf\xbd");
}

test "an ordinary run crosses the one-byte boundary long before it reaches 160" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    var chunk: usize = 0;
    while (chunk < 155) : (chunk += 1) {
        try feed(&reducer, scratch, wrap(
            \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"x"}}
        ));
    }

    try testing.expectEqualStrings("event-{", reducer.envelopes.items[22].object.get("id").?.string);
    try testing.expectEqualStrings("event-\u{80}", reducer.envelopes.items[27].object.get("id").?.string);
    try testing.expectEqualStrings("event-\u{100}", reducer.envelopes.items[155].object.get("id").?.string);
    for (reducer.envelopes.items) |envelope| {
        try testing.expect(std.unicode.utf8ValidateSlice(envelope.object.get("id").?.string));
    }
}

test "a native message id is bound within its own run, never across two" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"one"}}
    ));
    try reducer.settlePrompt("end_turn");
    const first = payloadAt(&reducer, 2).get("final_response").?.object;
    try testing.expectEqualStrings("one", first.get("content").?.string);

    try reducer.submit(1);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":"m1","content":{"type":"text","text":"two"}}
    ));
    try reducer.settlePrompt("end_turn");

    const second = payloadAt(&reducer, 5).get("final_response").?.object;
    try testing.expectEqualStrings("two", second.get("content").?.string);
    try testing.expect(!std.mem.eql(u8, first.get("id").?.string, second.get("id").?.string));
    try testing.expectEqualStrings(second.get("id").?.string, payloadAt(&reducer, 4).get("message_id").?.string);
}

test "a present chunk member of the wrong type fails the decode, while null does not" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":7}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi","uri":[]}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":7,"content":{"type":"text","text":"hi"}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");

    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), wrap(
        \\{"sessionUpdate":"agent_message_chunk","messageId":null,"content":{"type":"text","text":null}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&reducer, 1));
    try testing.expectEqualStrings("", payloadAt(&reducer, 1).get("part").?.object.get("text").?.string);
}

test "a transport failure is inferred while every other refusal is not" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try reducer.transportFailed("io: read/write on closed pipe");

    try testing.expectEqualStrings("acp_transport_failure", codeAt(&reducer, 1));
    try testing.expectEqualStrings("io: read/write on closed pipe", messageAt(&reducer, 1));
    try testing.expectEqualStrings("inferred", payloadAt(&reducer, 1).get("settled_by").?.string);
}

test "a prompt error quotes the code and the request id that carried it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var numbered = try openRun(&arena);
    try numbered.promptFailed(-32603, .{ .integer = 3 }, "fixture prompt failure");
    try testing.expectEqualStrings("acp_prompt_error", codeAt(&numbered, 1));
    try testing.expectEqualStrings("acp rpc error -32603 for request 3: fixture prompt failure", messageAt(&numbered, 1));

    var named = try openRun(&arena);
    try named.promptFailed(-32000, .{ .string = "req-7" }, "boom");
    try testing.expectEqualStrings("acp rpc error -32000 for request \"req-7\": boom", messageAt(&named, 1));

    var awkward = try openRun(&arena);
    try awkward.promptFailed(-32000, .{ .string = "we\"ird" }, "boom");
    try testing.expectEqualStrings("acp rpc error -32000 for request \"we\\\"ird\": boom", messageAt(&awkward, 1));

    var spaced = try openRun(&arena);
    try spaced.promptFailed(-32000, .{ .string = "with space" }, "boom");
    try testing.expectEqualStrings("acp rpc error -32000 for request \"with space\": boom", messageAt(&spaced, 1));

    var unset = try openRun(&arena);
    try unset.promptFailed(-32000, .{ .null = {} }, "boom");
    try testing.expectEqualStrings("acp rpc error -32000 for request <unset>: boom", messageAt(&unset, 1));
}

test "a prompt cancellation is confirmed only when cancel was requested first" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var unrequested = try openRun(&arena);
    try unrequested.promptFailed(-32800, .{ .integer = 3 }, "cancelled");
    try testing.expectEqualStrings("run.failed", typeAt(&unrequested, 1));
    try testing.expectEqualStrings("acp_prompt_error", codeAt(&unrequested, 1));

    var requested = try openRun(&arena);
    try requested.cancel();
    try requested.promptFailed(-32800, .{ .integer = 3 }, "cancelled");
    try testing.expectEqualStrings("run.cancelled", typeAt(&requested, 2));
    try testing.expectEqualStrings("ACP prompt cancellation confirmed", payloadAt(&requested, 2).get("reason").?.string);
}

test "cancel announces cancelling once and is silent the second time" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try reducer.cancel();
    try reducer.cancel();

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.status.updated", typeAt(&reducer, 1));
    try testing.expectEqualStrings("cancelling", payloadAt(&reducer, 1).get("status").?.string);
    try testing.expectEqual(@as(i64, 5), payloadAt(&reducer, 1).get("updated_at_ms").?.integer);
    try testing.expectEqual(@as(i64, 6), reducer.envelopes.items[1].object.get("timestamp_ms").?.integer);
}

test "cancel refuses a run that does not exist and one that already settled" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var empty = Reducer.init(&arena, .{});
    empty.open();
    try testing.expectError(Error.RunNotFound, empty.cancel());

    var reducer = try openRun(&arena);
    try reducer.settlePrompt("end_turn");
    try testing.expectError(Error.RunTerminal, reducer.cancel());
}

const tool_call_frame = wrap(
    \\{"sessionUpdate":"tool_call","toolCallId":"native-tool","title":"Read file","kind":"read","status":"pending","rawInput":{"path":"fixture.txt"}}
);

test "a tool call is announced once and its arguments default to a JSON null" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, tool_call_frame);

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("tool-call-f", reducer.envelopes.items[1].object.get("tool_call_id").?.string);
    try testing.expectEqualStrings("acp.v1", payloadAt(&reducer, 1).get("requested_by").?.string);
    try testing.expectEqualStrings("acp-agent", payloadAt(&reducer, 1).get("execution_owner").?.string);

    var bare = try openRun(&arena);
    try feed(&bare, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"t","title":"Bare"}
    ));
    try testing.expect(payloadAt(&bare, 1).get("arguments_json").? == .null);
}

test "a tool call missing an id or a title is refused before anything is minted" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":"tool_call","title":"Read file"}
    ), "acp_invalid_tool_call", "tool id and title are required");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"native-tool"}
    ), "acp_invalid_tool_call", "tool id and title are required");
}

test "a tool frame whose typed members are not strings is malformed, not incomplete" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":7,"title":"Read file"}
    ), "acp_invalid_tool_call", "malformed tool call");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":7}
    ), "acp_invalid_tool_update", "malformed tool update");
}

test "a tool patch naming no prior call is refused" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed"}
    ), "acp_tool_patch_without_call", "tool patch before creation");
}

test "a tool id first seen under an earlier run is a reuse, not a patch" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try reducer.settlePrompt("end_turn");
    try reducer.submit(1);
    try feed(&reducer, scratch, tool_call_frame);

    try testing.expectEqualStrings("action.call.started", typeAt(&reducer, 2));
    try testing.expectEqualStrings("action.call.failed", typeAt(&reducer, 3));
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 6));
    try testing.expectEqualStrings("acp_tool_id_reuse", codeAt(&reducer, 6));
    try testing.expectEqualStrings("tool id reused across prompts", messageAt(&reducer, 6));

    var patched = try openRun(&arena);
    try feed(&patched, scratch, tool_call_frame);
    try patched.settlePrompt("end_turn");
    try patched.submit(1);
    try feed(&patched, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed"}
    ));
    try testing.expectEqualStrings("acp_tool_patch_without_call", codeAt(&patched, 6));
}

test "a settled tool refuses both a repeat call and a later patch" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var recalled = try openRun(&arena);
    try feed(&recalled, scratch, tool_call_frame);
    try feed(&recalled, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed"}
    ));
    try feed(&recalled, scratch, tool_call_frame);
    try testing.expectEqualStrings("acp_tool_after_terminal", codeAt(&recalled, 4));
    try testing.expectEqualStrings("tool updated after terminal", messageAt(&recalled, 4));

    var patched = try openRun(&arena);
    try feed(&patched, scratch, tool_call_frame);
    try feed(&patched, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed"}
    ));
    try feed(&patched, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"failed"}
    ));
    try testing.expectEqualStrings("acp_tool_after_terminal", codeAt(&patched, 4));
}

test "a status outside the four ACP names is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"native-tool","title":"Read file","status":"invented_later"}
    ));

    try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("action.call.cancelled", typeAt(&reducer, 2));
    try testing.expectEqualStrings("acp_invalid_tool_status", codeAt(&reducer, 3));
    try testing.expectEqualStrings("unknown tool status", messageAt(&reducer, 3));
}

test "a pending status is a no-op while in_progress starts the call exactly once" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"in_progress","content":[{"type":"content"}]}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"in_progress"}
    ));

    try testing.expectEqualStrings("action.call.started", typeAt(&reducer, 2));
    try testing.expect(payloadAt(&reducer, 2).get("progress") == null);
    try testing.expectEqualStrings("action.call.progress", typeAt(&reducer, 3));
    try testing.expect(payloadAt(&reducer, 3).get("error") == null);
    const progress = payloadAt(&reducer, 3).get("progress").?.object;
    try testing.expectEqual(@as(usize, 1), progress.get("content").?.array.items.len);
    try testing.expect(progress.get("locations").? == .null);
}

test "a patch with no status reports progress only once the call has started" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","content":[]}
    ));
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);

    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"in_progress"}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","title":"Renamed"}
    ));

    try testing.expectEqualStrings("action.call.progress", typeAt(&reducer, 3));
    try testing.expectEqualStrings("Renamed", payloadAt(&reducer, 3).get("name").?.string);
    try testing.expect(payloadAt(&reducer, 3).get("arguments_json") == null);
    try testing.expect(payloadAt(&reducer, 3).get("result") == null);
}

test "a tool name outranks the title and a wrongly typed one is ignored" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"native-tool","title":"Read file","name":"read_file","status":"pending"}
    ));
    try testing.expectEqualStrings("read_file", payloadAt(&reducer, 1).get("name").?.string);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"in_progress","name":7}
    ));
    try testing.expectEqualStrings("read_file", payloadAt(&reducer, 2).get("name").?.string);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed","name":"grep"}
    ));
    try testing.expectEqualStrings("action.call.completed", typeAt(&reducer, 3));
    try testing.expectEqualStrings("grep", payloadAt(&reducer, 3).get("name").?.string);
}

test "a null tool name falls back to the title" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"native-tool","title":"Read file","name":null}
    ));
    try testing.expectEqualStrings("Read file", payloadAt(&reducer, 1).get("name").?.string);
}

test "a terminal status that never started synthesizes the start it skipped" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed","rawOutput":{"ok":true}}
    ));

    try testing.expectEqualStrings("action.call.started", typeAt(&reducer, 2));
    try testing.expectEqualStrings("fixture.txt", payloadAt(&reducer, 2).get("arguments_json").?.object.get("path").?.string);
    try testing.expectEqualStrings("action.call.completed", typeAt(&reducer, 3));
    try testing.expect(payloadAt(&reducer, 3).get("arguments_json") == null);
    try testing.expect(payloadAt(&reducer, 3).get("result").?.object.get("ok").?.bool);
}

test "a completed call with no output still carries a JSON null result" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed"}
    ));

    try testing.expect(payloadAt(&reducer, 3).get("result").? == .null);
}

test "a failed call reports the failure and drops the output it may have carried" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"failed","rawOutput":{"why":"denied"}}
    ));

    try testing.expectEqualStrings("action.call.failed", typeAt(&reducer, 3));
    try testing.expect(payloadAt(&reducer, 3).get("result") == null);
    try testing.expect(payloadAt(&reducer, 3).get("arguments_json") == null);
    const failure = payloadAt(&reducer, 3).get("error").?.object;
    try testing.expectEqualStrings("tool_failed", failure.get("code").?.string);
    try testing.expectEqualStrings("ACP tool call failed", failure.get("message").?.string);
}

test "an unfinished tool fails on completion and is cancelled on cancellation" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var completed = try openRun(&arena);
    try feed(&completed, scratch, tool_call_frame);
    try completed.settlePrompt("end_turn");
    try testing.expectEqualStrings("action.call.started", typeAt(&completed, 2));
    try testing.expectEqual(@as(usize, 5), completed.envelopes.items.len);
    try testing.expectEqualStrings("Read file", payloadAt(&completed, 2).get("name").?.string);
    try testing.expectEqualStrings("action.call.failed", typeAt(&completed, 3));
    const failure = payloadAt(&completed, 3).get("error").?.object;
    try testing.expectEqualStrings("incomplete_tool", failure.get("code").?.string);
    try testing.expectEqualStrings("prompt completed with unfinished ACP tool", failure.get("message").?.string);
    try testing.expectEqualStrings("run.completed", typeAt(&completed, 4));

    var cancelled = try openRun(&arena);
    try feed(&cancelled, scratch, tool_call_frame);
    try cancelled.settlePrompt("cancelled");
    try testing.expectEqualStrings("action.call.cancelled", typeAt(&cancelled, 2));
    try testing.expect(payloadAt(&cancelled, 2).get("error") == null);
    try testing.expect(payloadAt(&cancelled, 2).get("arguments_json") == null);
    try testing.expectEqualStrings("run.cancelled", typeAt(&cancelled, 3));
    try testing.expectEqual(@as(usize, 4), cancelled.envelopes.items.len);
}

const permission_frame =
    \\{"jsonrpc":"2.0","id":"permission-1","method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"sessionUpdate":"tool_call","toolCallId":"native-tool","title":"Read file","rawInput":{"path":"fixture.txt"}},"options":[{"optionId":"allow","name":"Allow once","kind":"allow_once"},{"optionId":"deny","name":"Reject","kind":"reject_once"}]}}
;

test "a permission request creates the tool it names and offers every labelled option" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);

    try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("action.permission.requested", typeAt(&reducer, 2));
    const payload = payloadAt(&reducer, 2);
    try testing.expectEqualStrings("interaction-h", payload.get("interaction_id").?.string);
    try testing.expectEqualStrings("tool-call-f", payload.get("tool_call_id").?.string);
    try testing.expectEqualStrings("Read file", payload.get("title").?.string);
    try testing.expectEqualStrings("user", payload.get("responded_by").?.string);
    try testing.expectEqualStrings("fixture.txt", payload.get("arguments_json").?.object.get("path").?.string);

    const choices = payload.get("choices").?.array.items;
    try testing.expectEqual(@as(usize, 2), choices.len);
    try testing.expectEqualStrings("allow", choices[0].object.get("id").?.string);
    try testing.expectEqualStrings("Allow once", choices[0].object.get("label").?.string);
    try testing.expectEqualStrings("allow_once", choices[0].object.get("description").?.string);
}

test "a request for another method is ignored rather than refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(),
        \\{"jsonrpc":"2.0","id":1,"method":"fs/read_text_file","params":{"path":"x"}}
    );

    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
}

test "a permission request that fails to decode is refused before the gate exists" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"other","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    , "acp_invalid_permission", "malformed permission request");
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"title":"T"},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    , "acp_invalid_permission", "malformed permission request");
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[]}}
    , "acp_invalid_permission", "malformed permission request");
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":["allow"]}}
    , "acp_invalid_permission", "malformed permission request");
}

test "options that all lack an id are a distinct refusal from a malformed request" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(),
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"","name":"A","kind":"allow_once"}]}}
    );

    try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("action.call.cancelled", typeAt(&reducer, 2));
    try testing.expectEqualStrings("acp_invalid_permission", codeAt(&reducer, 3));
    try testing.expectEqualStrings("empty permission options", messageAt(&reducer, 3));
    try testing.expectEqualStrings("event-j", reducer.envelopes.items[3].object.get("id").?.string);
}

test "a permission request naming an unusable tool call raises the tool refusal" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":""},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    , "acp_invalid_tool_call", "tool id and title are required");
}

test "resolving a gate answers the request that opened it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    const pending = reducer.pendingInteraction().?;
    try reducer.resolve(pending, "run-b", "user", "allow", true);

    try testing.expectEqualStrings("action.permission.resolved", typeAt(&reducer, 3));
    try testing.expectEqualStrings("event-i", reducer.envelopes.items[2].object.get("id").?.string);
    try testing.expectEqualStrings("event-i", reducer.envelopes.items[3].object.get("in_reply_to").?.string);
    const payload = payloadAt(&reducer, 3);
    try testing.expectEqualStrings("resolved", payload.get("outcome").?.string);
    try testing.expectEqualStrings("allow", payload.get("choice_id").?.string);
    try testing.expect(payload.get("granted").?.bool);
    try testing.expect(reducer.pendingInteraction() == null);
}

test "a patch cannot erase the title the call was admitted with" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","title":"","status":"completed"}
    ));

    try testing.expectEqualStrings("action.call.started", typeAt(&reducer, 2));
    try testing.expectEqualStrings("Read file", payloadAt(&reducer, 2).get("name").?.string);
    try testing.expectEqualStrings("Read file", payloadAt(&reducer, 3).get("name").?.string);

    var renamed = try openRun(&arena);
    try feed(&renamed, scratch, tool_call_frame);
    try feed(&renamed, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","title":"Renamed","status":"completed"}
    ));
    try testing.expectEqualStrings("Renamed", payloadAt(&renamed, 3).get("name").?.string);
}

test "an option is offered only with an id, a label and a kind ACP v1 defines" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"","name":"Nameless","kind":"allow_once"},{"optionId":"blank","name":"","kind":"allow_once"},{"optionId":"future","name":"Future","kind":"allow_for_this_repository"},{"optionId":"no","name":"Reject","kind":"reject_always"}]}}
    );

    const choices = payloadAt(&reducer, 2).get("choices").?.array.items;
    try testing.expectEqual(@as(usize, 1), choices.len);
    try testing.expectEqualStrings("no", choices[0].object.get("id").?.string);
    try testing.expectEqualStrings("Reject", choices[0].object.get("label").?.string);
    try testing.expectEqualStrings("reject_always", choices[0].object.get("description").?.string);

    try testing.expectError(Error.InvalidResolution, reducer.resolve(reducer.pendingInteraction().?, "run-b", "user", "future", true));
    try reducer.resolve(reducer.pendingInteraction().?, "run-b", "user", "no", false);
    try testing.expectEqualStrings("rejected", payloadAt(&reducer, 3).get("outcome").?.string);
}

test "a null option is skipped the way a zero-valued one is, not refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[null,{"optionId":"allow","name":"Allow","kind":"allow_once"}]}}
    );

    const choices = payloadAt(&reducer, 2).get("choices").?.array.items;
    try testing.expectEqual(@as(usize, 1), choices.len);
    try testing.expectEqualStrings("allow", choices[0].object.get("id").?.string);

    var only = try openRun(&arena);
    try feed(&only, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[null]}}
    );
    try testing.expectEqualStrings("empty permission options", messageAt(&only, 3));

    var typed = try openRun(&arena);
    try feed(&typed, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[7]}}
    );
    try testing.expectEqualStrings("malformed permission request", messageAt(&typed, 1));
}

test "a request whose every option is unusable raises the empty-options refusal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"future","name":"Future","kind":"allow_for_this_repository"}]}}
    );

    try testing.expectEqualStrings("acp_invalid_permission", codeAt(&reducer, 3));
    try testing.expectEqualStrings("empty permission options", messageAt(&reducer, 3));
}

test "a standing grant is a grant and a prompt error outside cancellation stays a failure" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var standing = try openRun(&arena);
    try feed(&standing, arena.allocator(),
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"always","name":"Always","kind":"allow_always"}]}}
    );
    try standing.resolve(standing.pendingInteraction().?, "run-b", "user", "always", true);
    try testing.expectEqualStrings("resolved", payloadAt(&standing, 3).get("outcome").?.string);

    var failed = try openRun(&arena);
    try failed.cancel();
    try failed.promptFailed(-32603, .{ .integer = 3 }, "boom");
    try testing.expectEqualStrings("run.failed", typeAt(&failed, 2));
    try testing.expectEqualStrings("acp_prompt_error", codeAt(&failed, 2));
}

test "a rejecting choice resolves the gate as rejected" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    try reducer.resolve(reducer.pendingInteraction().?, "run-b", "user", "deny", false);

    const payload = payloadAt(&reducer, 3);
    try testing.expectEqualStrings("rejected", payload.get("outcome").?.string);
    try testing.expect(!payload.get("granted").?.bool);
}

test "only the declared responder resolves a gate, once, with a choice that matches its grant" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    const pending = reducer.pendingInteraction().?;

    try testing.expectError(Error.InteractionNotFound, reducer.resolve("interaction-z", "run-b", "user", "allow", true));
    try testing.expectError(Error.WrongResponder, reducer.resolve(pending, "run-b", "operator", "allow", true));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(pending, "run-z", "user", "allow", true));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(pending, "run-b", "user", "maybe", true));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(pending, "run-b", "user", "allow", false));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(pending, "run-b", "user", "deny", true));
    try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);

    try reducer.resolve(pending, "run-b", "user", "allow", true);
    try testing.expectError(Error.InteractionResolved, reducer.resolve(pending, "run-b", "user", "allow", true));
    try testing.expectEqual(@as(usize, 4), reducer.envelopes.items.len);
}

test "a gate opened under an earlier run is not resolvable from the current one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    const pending = reducer.pendingInteraction().?;
    try reducer.settlePrompt("end_turn");
    try reducer.submit(1);

    try testing.expectError(Error.InteractionResolved, reducer.resolve(pending, "run-b", "user", "allow", true));
}

test "a run that settles under an open gate cancels it with the reason it settled" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    try reducer.settlePrompt("cancelled");

    try testing.expectEqualStrings("action.permission.resolved", typeAt(&reducer, 3));
    try testing.expectEqualStrings("event-i", reducer.envelopes.items[3].object.get("in_reply_to").?.string);
    const payload = payloadAt(&reducer, 3);
    try testing.expectEqualStrings("cancelled", payload.get("outcome").?.string);
    try testing.expect(payload.get("granted") == null);
    const reason = payload.get("reason").?.object;
    try testing.expectEqualStrings("run_settled", reason.get("code").?.string);
    try testing.expectEqualStrings("parent run settled the permission request", reason.get("message").?.string);

    try testing.expectEqualStrings("action.call.cancelled", typeAt(&reducer, 4));
    try testing.expectEqualStrings("run.cancelled", typeAt(&reducer, 5));
}

test "every envelope carries the frozen descriptor revision and the session it belongs to" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    try reducer.settlePrompt("end_turn");

    for (reducer.envelopes.items) |envelope| {
        try testing.expectEqualStrings(capability_revision, envelope.object.get("capability_revision").?.string);
        try testing.expectEqualStrings("session", envelope.object.get("session_id").?.string);
        try testing.expectEqualStrings("run-b", envelope.object.get("run_id").?.string);
        try testing.expectEqualStrings(protocol_name, envelope.object.get("protocol").?.string);
        try testing.expectEqualStrings(profile, envelope.object.get("profile").?.string);
    }
}

test "only the eight action envelopes carry a tool call id beside their payload" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try openRun(&arena);
    try feed(&reducer, arena.allocator(), permission_frame);
    try reducer.settlePrompt("end_turn");

    for (reducer.envelopes.items) |envelope| {
        const kind = envelope.object.get("type").?.string;
        const carried = envelope.object.get("tool_call_id") != null;
        try testing.expectEqual(correlatedType(kind), carried);
    }
}

test "a frame arriving before any submit is dropped rather than refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try feed(&reducer, scratch, tool_call_frame);
    try feed(&reducer, scratch, permission_frame);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"invented_later"}
    ));
    try reducer.settlePrompt("end_turn");
    try reducer.transportFailed("gone");

    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try testing.expectEqual(@as(usize, 0), reducer.ids);
}

test "a run reports the model it was opened with and omits it when there is none" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var anonymous = try openRun(&arena);
    try testing.expect(payloadAt(&anonymous, 0).get("model_id") == null);

    var named = Reducer.init(&arena, .{ .model = "gpt-5-codex" });
    named.open();
    try named.submit(1);
    try testing.expectEqualStrings("gpt-5-codex", payloadAt(&named, 0).get("model_id").?.string);
}

test "a chunk content block decoded by value refuses every shape the oracle refuses" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk"}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":null}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":null}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"bogus":1}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":7}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":[]}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":7}}
    ), "acp_invalid_message_chunk", "unsupported assistant chunk");
}

fn expectPermissionRefusal(tool_call: []const u8, code: []const u8, message: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    const text = try std.mem.concat(scratch, u8, &.{
        "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/request_permission\",\"params\":{\"sessionId\":\"native-session\",\"toolCall\":",
        tool_call,
        ",\"options\":[{\"optionId\":\"a\",\"name\":\"A\",\"kind\":\"allow_once\"}]}}",
    });
    try feed(&reducer, scratch, text);

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings(code, codeAt(&reducer, 1));
    try testing.expectEqualStrings(message, messageAt(&reducer, 1));
}

test "a permission tool call decoded by value splits the same way the oracle splits it" {
    try expectPermissionRefusal("null", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("{}", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("7", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("[]", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("{\"toolCallId\":7,\"title\":\"T\"}", "acp_invalid_permission", "malformed permission request");

    try expectPermissionRefusal("{\"toolCallId\":\"t\",\"title\":7}", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("{\"toolCallId\":\"t\",\"title\":\"T\",\"kind\":7}", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("{\"toolCallId\":\"t\",\"title\":\"T\",\"status\":7}", "acp_invalid_permission", "malformed permission request");
    try expectPermissionRefusal("{\"toolCallId\":\"t\",\"title\":\"T\",\"sessionUpdate\":7}", "acp_invalid_permission", "malformed permission request");

    try expectPermissionRefusal("{\"toolCallId\":\"t\"}", "acp_invalid_tool_call", "tool id and title are required");
    try expectPermissionRefusal("{\"toolCallId\":\"t\",\"title\":null}", "acp_invalid_tool_call", "tool id and title are required");
}

const raw_tool_members = [_][]const u8{ "rawInput", "rawOutput", "content", "locations" };

test "every raw member of a permission tool call takes any JSON, because a RawMessage cannot fail" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    for (raw_tool_members) |member| {
        for ([_][]const u8{ "7", "\"x\"", "null", "[]", "{}", "true" }) |value| {
            var reducer = try openRun(&arena);
            const text = try std.mem.concat(scratch, u8, &.{
                "{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"session/request_permission\",\"params\":{\"sessionId\":\"native-session\",\"toolCall\":{\"toolCallId\":\"t\",\"title\":\"T\",\"",
                member,
                "\":",
                value,
                "},\"options\":[{\"optionId\":\"a\",\"name\":\"A\",\"kind\":\"allow_once\"}]}}",
            });
            try feed(&reducer, scratch, text);
            try testing.expectEqualStrings("action.call.requested", typeAt(&reducer, 1));
            try testing.expectEqualStrings("action.permission.requested", typeAt(&reducer, 2));
        }
    }

    var carried = try openRun(&arena);
    try feed(&carried, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T","rawInput":7},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    );
    try testing.expectEqual(@as(i64, 7), payloadAt(&carried, 1).get("arguments_json").?.integer);
}

test "an injected run id takes the place of the minted one without taking its letter" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var minted = Reducer.init(&arena, .{});
    minted.open();
    try minted.submit(1);
    try feed(&minted, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}
    ));

    var injected = Reducer.init(&arena, .{});
    injected.open();
    try injected.submitAs(1, .{ .run_id = "01JB0RUN" });
    try feed(&injected, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}
    ));

    try testing.expectEqualStrings("run-b", minted.envelopes.items[0].object.get("run_id").?.string);
    try testing.expectEqualStrings("01JB0RUN", injected.envelopes.items[0].object.get("run_id").?.string);
    try testing.expectEqual(minted.envelopes.items.len, injected.envelopes.items.len);
    for (minted.envelopes.items, injected.envelopes.items) |mint, inject| {
        try testing.expectEqualStrings(
            mint.object.get("id").?.string,
            inject.object.get("id").?.string,
        );
    }
    try testing.expectEqual(minted.ids, injected.ids);
}

test "the endpoint and the revision an envelope cites are the ones the caller supplied" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var supplied = Reducer.init(&arena, .{ .endpoint = "makai.agent-control", .revision = "makai-oap-core-v1" });
    supplied.open();
    try supplied.submit(1);

    try testing.expectEqualStrings("makai-oap-core-v1", supplied.envelopes.items[0].object.get("capability_revision").?.string);

    var stock = Reducer.init(&arena, .{});
    stock.open();
    try stock.submit(1);
    try testing.expectEqualStrings(capability_revision, stock.envelopes.items[0].object.get("capability_revision").?.string);
}

test "a differently cased member name is the member, the way encoding/json reads it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var folded = try openRun(&arena);
    try feed(&folded, scratch, wrap(
        \\{"SESSIONUPDATE":"agent_message_chunk","CONTENT":{"TYPE":"text","TEXT":"hi"}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&folded, 1));
    try testing.expectEqualStrings("hi", payloadAt(&folded, 1).get("part").?.object.get("text").?.string);

    var session = try openRun(&arena);
    try feed(&session, scratch,
        \\{"jsonrpc":"2.0","method":"session/update","params":{"SESSIONID":"native-session","UPDATE":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}
    );
    try testing.expectEqualStrings("content.delta", typeAt(&session, 1));
}

test "the last spelling in wire order wins, and a null spelling never does" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var last = try openRun(&arena);
    try feed(&last, scratch, wrap(
        \\{"sessionUpdate":"plan","SESSIONUPDATE":"agent_message_chunk","content":{"type":"text","text":"hi"}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&last, 1));

    var nulled = try openRun(&arena);
    try feed(&nulled, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","SESSIONUPDATE":null,"content":{"type":"text","text":"hi"}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&nulled, 1));
}

test "a wrongly typed spelling refuses the update even when a later one would have done" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":7,"SESSIONUPDATE":"agent_message_chunk","content":{"type":"text","text":"hi"}}
    ), "acp_invalid_update", "malformed session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"agent_message_chunk","SESSIONUPDATE":true,"content":{"type":"text","text":"hi"}}
    ), "acp_invalid_update", "malformed session update");
}

test "the envelope around the update is read exactly, because the codec reads it from a map" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","method":"session/update","PARAMS":{"sessionId":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}
    );

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 1));
    try testing.expectEqualStrings("acp_invalid_update", codeAt(&reducer, 1));
}

test "a null spelling clears a pointer member and is a no-op on a plain one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"t","title":"first","kind":"read","status":"pending"}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"t","TITLE":"second","title":null,"status":"in_progress"}
    ));

    try testing.expectEqualStrings("action.call.started", typeAt(&reducer, 2));
    try testing.expectEqualStrings("first", payloadAt(&reducer, 2).get("name").?.string);

    var kept = try openRun(&arena);
    try feed(&kept, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","CONTENT":{"type":"text","text":"hi"},"content":null}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&kept, 1));
    try testing.expectEqualStrings("hi", payloadAt(&kept, 1).get("part").?.object.get("text").?.string);
}

test "a null spelling clears a slice member, so the gate has no options left" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"t","title":"first","kind":"read","status":"pending"}
    ));
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"first"},"OPTIONS":[{"optionId":"o","name":"n","kind":"allow_once"}],"options":null}}
    );

    const last = reducer.envelopes.items.len - 1;
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, last));
    try testing.expectEqualStrings("acp_invalid_permission", codeAt(&reducer, last));
}

test "a mistyped options spelling refuses the gate, even behind a valid one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"t","title":"first","kind":"read","status":"pending"}
    ));
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"first"},"options":7,"OPTIONS":[{"optionId":"o","name":"n","kind":"allow_once"}]}}
    );

    const last = reducer.envelopes.items.len - 1;
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, last));
    try testing.expectEqualStrings("acp_invalid_permission", codeAt(&reducer, last));
}

test "a mistyped spelling refuses whatever it names, whichever order it arrives in" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":7,"SESSIONID":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}
    , "acp_invalid_update", "malformed or foreign session/update");

    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":7,"CONTENT":{"type":"text","text":"hi"}}
    ));
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 1));
    try testing.expectEqualStrings("acp_invalid_message_chunk", codeAt(&reducer, 1));
}

test "two spellings of a struct member merge leaf by leaf, they do not replace each other" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var split = try openRun(&arena);
    try feed(&split, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"},"CONTENT":{"text":"b"}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&split, 1));
    try testing.expectEqualStrings("b", payloadAt(&split, 1).get("part").?.object.get("text").?.string);

    var reversed = try openRun(&arena);
    try feed(&reversed, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","CONTENT":{"text":"b"},"content":{"type":"text","text":"a"}}
    ));
    try testing.expectEqualStrings("a", payloadAt(&reversed, 1).get("part").?.object.get("text").?.string);

    var typed = try openRun(&arena);
    try feed(&typed, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"},"CONTENT":{"type":"image"}}
    ));
    try testing.expectEqualStrings("run.failed", typeAt(&typed, 1));
    try testing.expectEqualStrings("acp_invalid_message_chunk", codeAt(&typed, 1));
}

test "a split toolCall spelling opens the gate the merged one describes" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"t","title":"first","kind":"read","status":"pending"}
    ));
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t"},"TOOLCALL":{"title":"y"},"options":[{"optionId":"o","name":"n","kind":"allow_once"}]}}
    );

    const last = reducer.envelopes.items.len - 1;
    try testing.expectEqualStrings("action.permission.requested", typeAt(&reducer, last));
}

test "an empty toolCallId is refused by the permission gate, not by the tool gate below it" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"","title":"T"},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    , "acp_invalid_permission", "malformed permission request");
    try expectRefusal(
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"TOOLCALL":{"toolCallId":""},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    , "acp_invalid_permission", "malformed permission request");
}

test "a later spelling that names the toolCallId rescues an empty earlier one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"tool_call","toolCallId":"t","title":"first","kind":"read","status":"pending"}
    ));
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"","title":"T"},"TOOLCALL":{"toolCallId":"t"},"options":[{"optionId":"o","name":"n","kind":"allow_once"}]}}
    );

    const last = reducer.envelopes.items.len - 1;
    try testing.expectEqualStrings("action.permission.requested", typeAt(&reducer, last));
}

test "a null leaf in a later spelling leaves a string member alone" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var typed = try openRun(&arena);
    try feed(&typed, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"},"CONTENT":{"type":null}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&typed, 1));
    try testing.expectEqualStrings("a", payloadAt(&typed, 1).get("part").?.object.get("text").?.string);

    var texted = try openRun(&arena);
    try feed(&texted, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"a"},"CONTENT":{"text":null}}
    ));
    try testing.expectEqualStrings("content.delta", typeAt(&texted, 1));
    try testing.expectEqualStrings("a", payloadAt(&texted, 1).get("part").?.object.get("text").?.string);
}

test "a null leaf clears a raw member but not the title beside it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","id":1,"method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"first","rawInput":{"p":1}},"TOOLCALL":{"title":null,"rawInput":null},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}
    );

    const last = reducer.envelopes.items.len - 1;
    try testing.expectEqualStrings("action.permission.requested", typeAt(&reducer, last));
    const payload = payloadAt(&reducer, last);
    try testing.expectEqualStrings("first", payload.get("title").?.string);
    try testing.expect(payload.get("arguments_json").? == .null);
}
