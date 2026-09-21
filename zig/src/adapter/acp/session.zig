const std = @import("std");
const rpc = @import("rpc");

pub const capability_revision = "acp-v1.7.0-schema-v1.21.0-oap-v3";
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

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options) Reducer {
        return .{ .arena = arena, .options = options };
    }

    pub fn open(self: *Reducer) void {
        _ = self.now();
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        self.clock += 1;
        return self.clock;
    }

    fn nextID(self: *Reducer, kind: []const u8) ![]const u8 {
        self.ids += 1;
        const letter: u8 = @intCast('a' + (self.ids - 1));
        return std.fmt.allocPrint(self.allocator(), "{s}-{c}", .{ kind, letter });
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
        if (self.run) |run| {
            if (!run.terminal) return Error.RunActive;
        }
        var minted: usize = 0;
        while (minted < message_count) : (minted += 1) {
            _ = try self.nextID("message");
        }
        const run_id = try self.nextID("run");
        const message_id = try self.newMessageID();
        _ = self.now();

        self.run = .{ .id = run_id, .message_id = message_id };
        try self.messages.append(self.allocator(), .{ .native_id = "", .id = message_id });

        const run = &self.run.?;
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "status", str("running"));
        if (self.options.model.len > 0) try self.put(&payload, "model_id", str(self.options.model));
        try self.put(&payload, "started_at_ms", int(self.now()));
        _ = try self.emit(run, "run.started", .{ .object = payload });
        _ = try self.nextID("submission");
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value) ![]const u8 {
        return self.emitEnvelope(run, kind, payload, "");
    }

    fn emitEnvelope(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, in_reply_to: []const u8) ![]const u8 {
        if (run.terminal) return "";
        const id = try self.nextID("event");
        run.sequence += 1;
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
        try self.put(&envelope, "capability_revision", str(capability_revision));
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
        const detail = try std.fmt.allocPrint(self.allocator(), "unsupported ACP stop reason \"{s}\"", .{stop_reason});
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
            .string => |text| text,
            .integer => |value| try std.fmt.allocPrint(self.allocator(), "{d}", .{value}),
            else => "",
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
            try self.put(&payload, "requested_by", str(endpoint_id));
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
        const native_session = params.object.get("sessionId") orelse std.json.Value{ .null = {} };
        if (native_session != .string or !std.mem.eql(u8, native_session.string, self.options.native_id)) {
            try self.failActive("acp_invalid_update", "malformed or foreign session/update");
            return;
        }
        const update = params.object.get("update") orelse std.json.Value{ .null = {} };
        if (update != .object) {
            try self.failActive("acp_invalid_update", "malformed session update");
            return;
        }
        if (self.run == null) return;

        const kind = update.object.get("sessionUpdate") orelse std.json.Value{ .null = {} };
        if (kind != .string) {
            try self.failActive("acp_unknown_update", "unknown stable ACP session update");
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
            _ = try self.applyToolCall(update);
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
        const content = update.get("content") orelse std.json.Value{ .null = {} };
        if (content != .object) {
            try self.failActive("acp_invalid_message_chunk", "unsupported assistant chunk");
            return;
        }
        const content_type = content.object.get("type") orelse std.json.Value{ .null = {} };
        if (content_type != .string or !std.mem.eql(u8, content_type.string, "text")) {
            try self.failActive("acp_invalid_message_chunk", "unsupported assistant chunk");
            return;
        }
        const text_value = content.object.get("text") orelse std.json.Value{ .null = {} };
        const text = if (text_value == .string) text_value.string else "";

        const run = &self.run.?;
        var message_id = run.message_id;
        const native_message = update.get("messageId") orelse std.json.Value{ .null = {} };
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

    fn applyToolCall(self: *Reducer, update: std.json.ObjectMap) !bool {
        const native_id = stringMember(update, "toolCallId");
        const title = stringMember(update, "title");
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
        tool.kind = stringMember(update, "kind");
        tool.raw_input = update.get("rawInput");
        tool.raw_output = update.get("rawOutput");
        tool.json_content = update.get("content");
        tool.locations = update.get("locations");
        const first = !tool.requested;
        tool.requested = true;
        const status = stringMember(update, "status");
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
        if (presentString(update, "title")) |value| tool.title = value;
        if (presentString(update, "kind")) |value| tool.kind = value;
        if (update.get("rawInput")) |value| tool.raw_input = value;
        if (update.get("rawOutput")) |value| tool.raw_output = value;
        if (update.get("content")) |value| tool.json_content = value;
        if (update.get("locations")) |value| tool.locations = value;
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
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "execution_owner", str(harness_owner));
        if (tool.title.len > 0) try self.put(&payload, "name", str(tool.title));
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
        const tool_call = params.object.get("toolCall").?.object;
        const options = params.object.get("options").?.array;
        if (self.run == null) return;

        if (!try self.applyToolCall(tool_call)) return;
        const index = self.findTool(stringMember(tool_call, "toolCallId")).?;
        const run = &self.run.?;

        const id = try self.nextID("interaction");
        var kept = std.ArrayList(Choice).empty;
        var choices = std.json.Array.init(self.allocator());
        for (options.items) |entry| {
            const option_id = stringMember(entry.object, "optionId");
            if (option_id.len == 0) continue;
            const name = stringMember(entry.object, "name");
            const kind = stringMember(entry.object, "kind");
            try kept.append(self.allocator(), .{ .id = option_id, .name = name, .kind = kind });
            var choice = self.object();
            try self.put(&choice, "id", str(option_id));
            try self.put(&choice, "label", str(name));
            if (kind.len > 0) try self.put(&choice, "description", str(kind));
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
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "responded_by", str(self.options.responder));
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "tool_call_id", str(self.tools.items[index].id));
        try self.put(&payload, "title", str(stringMember(tool_call, "title")));
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
        try self.put(&payload, "requested_by", str(endpoint_id));
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

fn correlatedType(kind: []const u8) bool {
    for (correlated_types) |known| {
        if (std.mem.eql(u8, known, kind)) return true;
    }
    return false;
}

fn stringMember(map: std.json.ObjectMap, key: []const u8) []const u8 {
    const value = map.get(key) orelse return "";
    return if (value == .string) value.string else "";
}

fn presentString(map: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = map.get(key) orelse return null;
    return if (value == .string) value.string else null;
}

fn typedString(map: std.json.ObjectMap, key: []const u8) bool {
    const value = map.get(key) orelse return true;
    return value == .string or value == .null;
}

const tool_call_strings = [_][]const u8{ "sessionUpdate", "toolCallId", "title", "kind", "status" };

fn toolCallDecodes(update: std.json.ObjectMap) bool {
    for (tool_call_strings) |key| {
        if (!typedString(update, key)) return false;
    }
    return true;
}

fn toolUpdateDecodes(update: std.json.ObjectMap) bool {
    return toolCallDecodes(update);
}

const permission_option_strings = [_][]const u8{ "optionId", "name", "kind" };

fn permissionDecodes(params: std.json.Value, native_id: []const u8) bool {
    if (params != .object) return false;
    const session = params.object.get("sessionId") orelse return false;
    if (session != .string or !std.mem.eql(u8, session.string, native_id)) return false;
    const tool_call = params.object.get("toolCall") orelse return false;
    if (tool_call != .object or !toolCallDecodes(tool_call.object)) return false;
    if (stringMember(tool_call.object, "toolCallId").len == 0) return false;
    const options = params.object.get("options") orelse return false;
    if (options != .array or options.array.items.len == 0) return false;
    for (options.array.items) |entry| {
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

test "an update member that is absent or not an object is a separate refusal from a foreign session" {
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session"}}
    , "acp_invalid_update", "malformed session update");
    try expectRefusal(
        \\{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":"plan"}}
    , "acp_invalid_update", "malformed session update");
}

test "an update naming no string kind is refused as an unknown update" {
    try expectRefusal(wrap(
        \\{"sessionUpdate":7}
    ), "acp_unknown_update", "unknown stable ACP session update");
    try expectRefusal(wrap(
        \\{"kind":"read"}
    ), "acp_unknown_update", "unknown stable ACP session update");
    try expectRefusal(wrap(
        \\{"sessionUpdate":"invented_later"}
    ), "acp_unknown_update", "unknown stable ACP session update");
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
}

test "a settled run absorbs every later frame without emitting" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try reducer.settlePrompt("end_turn");

    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"late"}}
    ));
    try feed(&reducer, scratch, wrap(
        \\{"sessionUpdate":"invented_later"}
    ));
    try reducer.settlePrompt("cancelled");
    try reducer.transportFailed("gone");
    try reducer.promptFailed(-32603, .{ .integer = 3 }, "gone");

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
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
    try testing.expectEqualStrings("acp rpc error -32000 for request req-7: boom", messageAt(&named, 1));
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

    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 5));
    try testing.expectEqualStrings("acp_tool_id_reuse", codeAt(&reducer, 5));
    try testing.expectEqualStrings("tool id reused across prompts", messageAt(&reducer, 5));

    var patched = try openRun(&arena);
    try feed(&patched, scratch, tool_call_frame);
    try patched.settlePrompt("end_turn");
    try patched.submit(1);
    try feed(&patched, scratch, wrap(
        \\{"sessionUpdate":"tool_call_update","toolCallId":"native-tool","status":"completed"}
    ));
    try testing.expectEqualStrings("acp_tool_patch_without_call", codeAt(&patched, 5));
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
    try testing.expectEqualStrings("action.call.failed", typeAt(&completed, 2));
    const failure = payloadAt(&completed, 2).get("error").?.object;
    try testing.expectEqualStrings("incomplete_tool", failure.get("code").?.string);
    try testing.expectEqualStrings("prompt completed with unfinished ACP tool", failure.get("message").?.string);
    try testing.expectEqualStrings("run.completed", typeAt(&completed, 3));

    var cancelled = try openRun(&arena);
    try feed(&cancelled, scratch, tool_call_frame);
    try cancelled.settlePrompt("cancelled");
    try testing.expectEqualStrings("action.call.cancelled", typeAt(&cancelled, 2));
    try testing.expect(payloadAt(&cancelled, 2).get("error") == null);
    try testing.expect(payloadAt(&cancelled, 2).get("arguments_json") == null);
    try testing.expectEqualStrings("run.cancelled", typeAt(&cancelled, 3));
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
