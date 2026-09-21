const std = @import("std");
const rpc = @import("rpc");

pub const capability_revision = "claude-code-2.1.263-oap-v3";
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const endpoint_id = "claude-code.cli";
pub const harness_owner = "claude-code";
pub const native_source = "claude-code-native";
pub const mcp_tool_prefix = "mcp__";
pub const mcp_source_prefix = "mcp:";

pub const Options = struct {
    session_id: []const u8 = "session",
    model: []const u8 = "claude-test",
    responder: []const u8 = "user",
};

pub const Decision = enum { allow, deny };

const Run = struct {
    id: []const u8 = "",
    message_id: []const u8 = "",
    submission_id: []const u8 = "",
    submission_uuid: []const u8 = "",
    model: []const u8 = "",
    started: bool = false,
    sequence: i64 = 0,
    buffered: std.ArrayList(rpc.Message) = .empty,
    deferred: ?std.json.ObjectMap = null,
};

pub const CatalogEntry = struct {
    name: []const u8,
    source: []const u8,
};

const Child = struct {
    task_id: []const u8,
    run_id: []const u8,
    task_type: []const u8,
    settled: bool = false,
};

const Tool = struct {
    native_id: []const u8,
    id: []const u8,
    run_id: []const u8,
    name: []const u8,
    source: []const u8 = "",
    started_event: []const u8 = "",
    terminal: bool = false,
};

const Gate = struct {
    id: []const u8,
    run_id: []const u8,
    request_id: []const u8 = "",
    requested_event: []const u8 = "",
    resolved: bool = false,
};

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: usize = 0,
    clock: i64 = 0,
    current_model: []const u8,
    run: ?Run = null,
    tools: std.ArrayList(Tool) = .empty,
    catalog: std.ArrayList(CatalogEntry) = .empty,
    catalog_known: bool = false,
    served: ?std.StringHashMapUnmanaged([]const u8) = null,
    attribution: std.StringHashMapUnmanaged([]const u8) = .empty,
    gates: std.ArrayList(Gate) = .empty,
    unusable: bool = false,
    children: std.ArrayList(Child) = .empty,
    envelopes: std.ArrayList(std.json.Value) = .empty,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options) Reducer {
        return .{ .arena = arena, .options = options, .current_model = options.model };
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn nextID(self: *Reducer, kind: []const u8) ![]const u8 {
        self.ids += 1;
        return std.fmt.allocPrint(self.allocator(), "{s}-{d}", .{ kind, self.ids });
    }

    fn now(self: *Reducer) i64 {
        self.clock += 1;
        return self.clock;
    }

    pub fn open(self: *Reducer) void {
        _ = self.now();
    }

    pub fn submit(self: *Reducer, submission_uuid: []const u8) !void {
        if (self.unusable) return error.SessionClosed;
        if (self.run != null) return error.RunActive;
        const submission_id = try self.nextID("submission");
        const message_id = try self.nextID("message");
        self.run = Run{
            .submission_id = submission_id,
            .message_id = message_id,
            .submission_uuid = submission_uuid,
            .model = self.current_model,
        };
    }

    fn object(self: *Reducer) std.json.ObjectMap {
        _ = self;
        return .empty;
    }

    fn put(self: *Reducer, map: *std.json.ObjectMap, key: []const u8, value: std.json.Value) !void {
        try map.put(self.allocator(), key, value);
    }

    fn array(self: *Reducer) std.json.Array {
        return .init(self.allocator());
    }

    fn str(text: []const u8) std.json.Value {
        return .{ .string = text };
    }

    fn int(value: i64) std.json.Value {
        return .{ .integer = value };
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value) ![]const u8 {
        return self.emitEnvelope(run, kind, payload, .{});
    }

    fn emitCorrelated(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, tool_call_id: []const u8, in_reply_to: []const u8) ![]const u8 {
        return self.emitEnvelope(run, kind, payload, .{ .tool_call_id = tool_call_id, .in_reply_to = in_reply_to });
    }

    fn emitTurn(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, turn_id: []const u8, in_reply_to: []const u8) ![]const u8 {
        return self.emitEnvelope(run, kind, payload, .{ .turn_id = turn_id, .in_reply_to = in_reply_to });
    }

    const Correlation = struct {
        tool_call_id: []const u8 = "",
        turn_id: []const u8 = "",
        in_reply_to: []const u8 = "",
    };

    fn emitEnvelope(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, correlation: Correlation) ![]const u8 {
        const tool_call_id = correlation.tool_call_id;
        const in_reply_to = correlation.in_reply_to;
        run.sequence += 1;
        const id = try self.nextID("event");
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
        if (tool_call_id.len > 0) try self.put(&envelope, "tool_call_id", str(tool_call_id));
        if (correlation.turn_id.len > 0) try self.put(&envelope, "turn_id", str(correlation.turn_id));
        try self.put(&envelope, "capability_revision", str(capability_revision));
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
        return id;
    }

    fn toolPayload(self: *Reducer, run: *Run, tool: Tool) !std.json.ObjectMap {
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "tool_call_id", str(tool.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "execution_owner", str(harness_owner));
        if (tool.source.len > 0) try self.put(&payload, "source", str(tool.source));
        try self.put(&payload, "name", str(tool.name));
        return payload;
    }

    fn findTool(self: *Reducer, native_id: []const u8) ?*Tool {
        for (self.tools.items) |*tool| {
            if (std.mem.eql(u8, tool.native_id, native_id)) return tool;
        }
        return null;
    }

    fn startTool(self: *Reducer, native_id: []const u8, name: []const u8, input: ?std.json.Value) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (self.findTool(native_id) != null) {
            try self.failRun("claude_tool_lifecycle", "duplicate tool call");
            return;
        }
        const tool = Tool{
            .native_id = native_id,
            .id = try self.nextID("tool-call"),
            .run_id = run.id,
            .name = name,
            .source = self.attributionFor(name),
        };

        var requested = try self.toolPayload(run, tool);
        try self.put(&requested, "arguments_json", input orelse .null);
        const requested_id = try self.emitCorrelated(run, "action.call.requested", .{ .object = requested }, tool.id, "");

        const started = try self.toolPayload(run, tool);
        const started_id = try self.emitCorrelated(run, "action.call.started", .{ .object = started }, tool.id, requested_id);

        var stored = tool;
        stored.started_event = started_id;
        try self.tools.append(self.allocator(), stored);
    }

    fn endTool(self: *Reducer, native_id: []const u8, content: ?std.json.Value, is_error: bool) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        const tool = self.findTool(native_id) orelse {
            try self.failRun("claude_tool_lifecycle", "unmatched tool completion");
            return;
        };
        if (!std.mem.eql(u8, tool.run_id, run.id)) return;
        if (tool.terminal) {
            try self.failRun("claude_tool_lifecycle", "unmatched tool completion");
            return;
        }
        tool.terminal = true;
        var payload = try self.toolPayload(run, tool.*);
        if (is_error) {
            var failure = self.object();
            try self.put(&failure, "code", str("claude_tool_error"));
            try self.put(&failure, "message", str(toolResultText(content)));
            try self.put(&payload, "error", .{ .object = failure });
            _ = try self.emitCorrelated(run, "action.call.failed", .{ .object = payload }, tool.id, tool.started_event);
            return;
        }
        try self.put(&payload, "result", content orelse str(""));
        _ = try self.emitCorrelated(run, "action.call.completed", .{ .object = payload }, tool.id, tool.started_event);
    }

    pub fn pendingInteraction(self: *Reducer) ?[]const u8 {
        const run = self.run orelse return null;
        for (self.gates.items) |gate| {
            if (!gate.resolved and std.mem.eql(u8, gate.run_id, run.id)) return gate.id;
        }
        return null;
    }

    fn findGate(self: *Reducer, id: []const u8) ?*Gate {
        const run = self.run orelse return null;
        for (self.gates.items) |*gate| {
            if (std.mem.eql(u8, gate.id, id) and std.mem.eql(u8, gate.run_id, run.id)) return gate;
        }
        return null;
    }

    fn statusPayload(self: *Reducer, run: *Run, status: []const u8, pending: []const u8) !std.json.Value {
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "status", str(status));
        if (pending.len > 0) try self.put(&payload, "pending_user_input_id", str(pending));
        try self.put(&payload, "updated_at_ms", int(self.now()));
        return .{ .object = payload };
    }

    fn foreignActivity(self: *Reducer, what: []const u8) !void {
        self.unusable = true;
        if (self.run == null) return;
        if (!self.run.?.started) {
            self.run = null;
            return;
        }
        try self.failRun("claude_external_activity", what);
    }

    fn cancelGate(self: *Reducer, request_id: []const u8) !void {
        const owner = self.run orelse return;
        for (self.gates.items) |*gate| {
            if (gate.resolved or !std.mem.eql(u8, gate.request_id, request_id)) continue;
            if (!std.mem.eql(u8, gate.run_id, owner.id)) continue;
            gate.resolved = true;
            const run = &self.run.?;
            if (!run.started) return;
            var payload = self.object();
            try self.put(&payload, "interaction_id", str(gate.id));
            try self.put(&payload, "requested_by", str(endpoint_id));
            try self.put(&payload, "responded_by", str(self.options.responder));
            try self.put(&payload, "session_id", str(self.options.session_id));
            try self.put(&payload, "run_id", str(run.id));
            try self.put(&payload, "status", str("cancelled"));
            _ = try self.emitTurn(run, "user.input.resolved", .{ .object = payload }, gate.id, gate.requested_event);
            _ = try self.emit(run, "run.status.updated", try self.statusPayload(run, "running", ""));
            return;
        }
    }

    fn openGate(self: *Reducer, message: rpc.Message) !void {
        if (self.run == null or !self.run.?.started) {
            try self.foreignActivity("can_use_tool outside an owned run");
            return;
        }
        const run = &self.run.?;
        const request = message.object.object.get("request") orelse return;
        if (request != .object) return;
        const ask = request.object;
        const tool_name = stringMember(ask, "tool_name") orelse "";

        const id = try self.nextID("interaction");
        const title = stringMember(ask, "title") orelse
            try std.fmt.allocPrint(self.allocator(), "Use {s}", .{tool_name});
        const description = stringMember(ask, "description") orelse
            stringMember(ask, "decision_reason") orelse "";

        var prompt = tool_name;
        if (ask.get("input")) |input| {
            const encoded = try std.json.Stringify.valueAlloc(self.allocator(), input, .{});
            if (encoded.len > 0) {
                prompt = try std.fmt.allocPrint(self.allocator(), "{s} {s}", .{ tool_name, encoded });
            }
        }

        var payload = self.object();
        try self.put(&payload, "interaction_id", str(id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "responded_by", str(self.options.responder));
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "title", str(title));
        if (description.len > 0) try self.put(&payload, "description", str(description));
        try self.put(&payload, "questions", try self.decisionQuestions(prompt));
        try self.put(&payload, "allow_cancel", .{ .bool = true });

        const requested = try self.emitTurn(run, "user.input.requested", .{ .object = payload }, id, "");
        try self.gates.append(self.allocator(), .{ .id = id, .run_id = run.id, .request_id = message.request_id, .requested_event = requested });
        _ = try self.emit(run, "run.status.updated", try self.statusPayload(run, "waiting_for_input", id));
    }

    fn decisionQuestions(self: *Reducer, prompt: []const u8) !std.json.Value {
        var allow = self.object();
        try self.put(&allow, "id", str("allow"));
        try self.put(&allow, "label", str("Allow"));
        var deny = self.object();
        try self.put(&deny, "id", str("deny"));
        try self.put(&deny, "label", str("Deny"));
        var options = self.array();
        try options.append(.{ .object = allow });
        try options.append(.{ .object = deny });

        var question = self.object();
        try self.put(&question, "id", str("decision"));
        try self.put(&question, "prompt", str(prompt));
        try self.put(&question, "kind", str("single_choice"));
        try self.put(&question, "required", .{ .bool = true });
        try self.put(&question, "options", .{ .array = options });

        var questions = self.array();
        try questions.append(.{ .object = question });
        return .{ .array = questions };
    }

    pub fn resolve(self: *Reducer, interaction_id: []const u8, decision: Decision) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        const gate = self.findGate(interaction_id) orelse return;
        if (gate.resolved) return;
        gate.resolved = true;

        var selected = self.array();
        try selected.append(str(@tagName(decision)));
        var answer = self.object();
        try self.put(&answer, "question_id", str("decision"));
        try self.put(&answer, "selected_option_ids", .{ .array = selected });
        var answers = self.array();
        try answers.append(.{ .object = answer });

        var payload = self.object();
        try self.put(&payload, "interaction_id", str(gate.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "responded_by", str(self.options.responder));
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "status", str("submitted"));
        try self.put(&payload, "answers", .{ .array = answers });

        _ = try self.emitTurn(run, "user.input.resolved", .{ .object = payload }, gate.id, gate.requested_event);
        _ = try self.emit(run, "run.status.updated", try self.statusPayload(run, "running", ""));
    }

    fn startRun(self: *Reducer) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        run.started = true;
        run.id = try self.nextID("run");
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "status", str("running"));
        try self.put(&payload, "model_id", str(run.model));
        try self.put(&payload, "started_at_ms", int(self.now()));
        _ = try self.emit(run, "run.started", .{ .object = payload });

        const replay = run.buffered;
        run.buffered = .empty;
        for (replay.items) |buffered| {
            if (self.run == null) return;
            try self.applyRunObservation(buffered);
        }
    }

    fn emitDelta(self: *Reducer, kind: []const u8, text: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        var part = self.object();
        if (std.mem.eql(u8, kind, "thinking")) {
            try self.put(&part, "type", str("reasoning"));
            try self.put(&part, "reasoning", str(text));
        } else {
            try self.put(&part, "type", str("text"));
            try self.put(&part, "text", str(text));
        }
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "message_id", str(run.message_id));
        try self.put(&payload, "part", .{ .object = part });
        _ = try self.emit(run, "content.delta", .{ .object = payload });
    }

    fn usageOf(self: *Reducer, frame: std.json.ObjectMap) !std.json.Value {
        var totals = self.object();
        var input: i64 = 0;
        var output: i64 = 0;
        if (frame.get("usage")) |usage| {
            if (usage == .object) {
                input = integerMember(usage.object, "input_tokens") orelse 0;
                output = integerMember(usage.object, "output_tokens") orelse 0;
            }
        }
        if (input != 0) try self.put(&totals, "input_tokens", int(input));
        if (output != 0) try self.put(&totals, "output_tokens", int(output));
        if (input + output != 0) try self.put(&totals, "total_tokens", int(input + output));
        return .{ .object = totals };
    }

    fn terminalPayload(self: *Reducer, run: *Run) !std.json.ObjectMap {
        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        return payload;
    }

    fn closeTerminal(self: *Reducer, frame: std.json.ObjectMap, payload: *std.json.ObjectMap) !void {
        try self.put(payload, "usage", try self.usageOf(frame));
        if (integerMember(frame, "duration_ms")) |duration| {
            if (duration != 0) try self.put(payload, "duration_ms", int(duration));
        }
    }

    fn settle(self: *Reducer, frame: std.json.ObjectMap) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (!run.started) return;
        if (!self.frameEchoMatches(frame)) return;
        const queued = integerMember(frame, "queued_turn_count") orelse 0;
        if (queued > 0 or self.unsettledDeferringChildren() > 0) {
            run.deferred = frame;
            return;
        }
        try self.publishTerminal(frame);
    }

    fn unsettledDeferringChildren(self: *Reducer) usize {
        const run = self.run orelse return 0;
        var count: usize = 0;
        for (self.children.items) |child| {
            if (child.settled or !std.mem.eql(u8, child.run_id, run.id)) continue;
            if (defersTerminal(child.task_type)) count += 1;
        }
        return count;
    }

    fn trackChild(self: *Reducer, frame: std.json.ObjectMap) !void {
        const task_id = stringMember(frame, "task_id") orelse return;
        const run = self.run orelse return;
        try self.children.append(self.allocator(), .{
            .task_id = task_id,
            .run_id = run.id,
            .task_type = stringMember(frame, "task_type") orelse "",
        });
    }

    fn settleChild(self: *Reducer, frame: std.json.ObjectMap) !void {
        const task_id = stringMember(frame, "task_id") orelse return;
        const run = self.run orelse return;
        var found = false;
        for (self.children.items) |*child| {
            if (!std.mem.eql(u8, child.task_id, task_id) or child.settled) continue;
            if (!std.mem.eql(u8, child.run_id, run.id)) continue;
            child.settled = true;
            found = true;
        }
        if (!found) return;
        if (self.unsettledDeferringChildren() > 0) return;
        try self.publishDeferred();
    }

    fn publishDeferred(self: *Reducer) !void {
        if (self.run == null) return;
        const deferred = self.run.?.deferred orelse return;
        self.run.?.deferred = null;
        try self.publishTerminal(deferred);
    }

    fn publishTerminal(self: *Reducer, frame: std.json.ObjectMap) !void {
        const run = &self.run.?;
        run.deferred = null;
        try self.sweepRun();

        var payload = try self.terminalPayload(run);
        if (cancelled(frame)) {
            const reason = try std.fmt.allocPrint(self.allocator(), "interrupt confirmed by terminal_reason {s}", .{stringMember(frame, "terminal_reason") orelse ""});
            try self.put(&payload, "reason", str(reason));
            try self.closeTerminal(frame, &payload);
            _ = try self.emit(run, "run.cancelled", .{ .object = payload });
        } else if (failed(frame)) {
            var failure = self.object();
            try self.put(&failure, "code", str(try self.failureCode(frame)));
            try self.put(&failure, "message", str(try self.errorResultText(frame)));
            try self.put(&payload, "error", .{ .object = failure });
            try self.closeTerminal(frame, &payload);
            _ = try self.emit(run, "run.failed", .{ .object = payload });
        } else {
            var response = self.object();
            try self.put(&response, "id", str(run.message_id));
            try self.put(&response, "role", str("assistant"));
            try self.put(&response, "content", str(stringMember(frame, "result") orelse ""));
            try self.put(&payload, "final_response", .{ .object = response });
            const reason: []const u8 = if (maxTurns(frame)) "max_turns" else stopReason(frame);
            try self.put(&payload, "stop_reason", str(reason));
            try self.closeTerminal(frame, &payload);
            _ = try self.emit(run, "run.completed", .{ .object = payload });
        }
        self.run = null;
    }

    fn failRun(self: *Reducer, code: []const u8, message: []const u8) !void {
        return self.failRunSettled(code, message, "");
    }

    pub fn transportFailed(self: *Reducer, detail: []const u8) !void {
        self.unusable = true;
        return self.failRunSettled("claude_process_exit", detail, "inferred");
    }

    fn failRunSettled(self: *Reducer, code: []const u8, message: []const u8, settled_by: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (!run.started) {
            self.run = null;
            return;
        }
        var failure = self.object();
        try self.put(&failure, "code", str(code));
        try self.put(&failure, "message", str(message));
        var payload = try self.terminalPayload(run);
        try self.put(&payload, "error", .{ .object = failure });
        if (settled_by.len > 0) try self.put(&payload, "settled_by", str(settled_by));
        _ = try self.emit(run, "run.failed", .{ .object = payload });
        self.run = null;
    }

    fn errorResultText(self: *Reducer, frame: std.json.ObjectMap) ![]const u8 {
        if (frame.get("errors")) |listed| {
            if (listed == .array and listed.array.items.len > 0 and everyItemIsAString(listed.array)) {
                var joined = std.ArrayList(u8).empty;
                for (listed.array.items, 0..) |entry, index| {
                    if (index > 0) try joined.appendSlice(self.allocator(), "; ");
                    try joined.appendSlice(self.allocator(), entry.string);
                }
                return joined.items;
            }
        }
        if (stringMember(frame, "result")) |result| {
            const trimmed = std.mem.trim(u8, result, " \t\r\n");
            if (trimmed.len > 0) return trimmed;
        }
        if (stringMember(frame, "subtype")) |subtype| {
            if (!std.mem.eql(u8, subtype, "success")) return subtype;
        }
        if (integerMember(frame, "api_error_status")) |status| {
            return std.fmt.allocPrint(self.allocator(), "API error (HTTP {d})", .{status});
        }
        return "unknown error";
    }

    fn failureCode(self: *Reducer, frame: std.json.ObjectMap) ![]const u8 {
        if (integerMember(frame, "api_error_status")) |status| {
            return std.fmt.allocPrint(self.allocator(), "claude_api_{d}", .{status});
        }
        const subtype = stringMember(frame, "subtype") orelse "";
        const terminal_reason = stringMember(frame, "terminal_reason") orelse "";
        if (terminal_reason.len > 0 and std.mem.eql(u8, subtype, "success")) {
            return std.fmt.allocPrint(self.allocator(), "claude_{s}", .{terminal_reason});
        }
        return std.fmt.allocPrint(self.allocator(), "claude_{s}", .{subtype});
    }

    fn sweepRun(self: *Reducer) !void {
        const run = &self.run.?;
        for (self.tools.items) |*tool| {
            if (!std.mem.eql(u8, tool.run_id, run.id) or tool.terminal) continue;
            tool.terminal = true;
            const payload = try self.toolPayload(run, tool.*);
            _ = try self.emitCorrelated(run, "action.call.cancelled", .{ .object = payload }, tool.id, tool.started_event);
        }
        for (self.gates.items) |*gate| {
            if (gate.resolved or !std.mem.eql(u8, gate.run_id, run.id)) continue;
            gate.resolved = true;
            var payload = self.object();
            try self.put(&payload, "interaction_id", str(gate.id));
            try self.put(&payload, "requested_by", str(endpoint_id));
            try self.put(&payload, "responded_by", str(self.options.responder));
            try self.put(&payload, "session_id", str(self.options.session_id));
            try self.put(&payload, "run_id", str(run.id));
            try self.put(&payload, "status", str("cancelled"));
            _ = try self.emitTurn(run, "user.input.resolved", .{ .object = payload }, gate.id, gate.requested_event);
        }
    }

    pub fn observe(self: *Reducer, message: rpc.Message) !void {
        if (message.kind == .control_cancel) {
            try self.cancelGate(message.request_id);
            return;
        }
        if (message.kind == .control_request) {
            if (std.mem.eql(u8, message.subtype, "can_use_tool")) {
                try self.openGate(message);
                return;
            }
            const what = try std.fmt.allocPrint(self.allocator(), "reverse control request \"{s}\"", .{message.subtype});
            try self.foreignActivity(what);
            return;
        }
        if (message.kind != .observation) return;
        if (self.run) |run| {
            if (self.unusable) return;
            if (!run.started) {
                if (self.echoMatches(message)) {
                    try self.startRun();
                    try self.applyRunObservation(message);
                } else {
                    try self.run.?.buffered.append(self.allocator(), message);
                }
                return;
            }
            try self.applyRunObservation(message);
            return;
        }
        try self.observeIdle(message);
    }

    fn observeIdle(self: *Reducer, message: rpc.Message) !void {
        if (!std.mem.eql(u8, message.type, "system")) return;
        if (!std.mem.eql(u8, message.subtype, "init")) return;
        const frame = message.object.object;
        if (frame.get("model")) |model| {
            if (model == .string) self.current_model = model.string;
        }
        try self.projectCatalog(frame);
    }

    fn projectCatalog(self: *Reducer, frame: std.json.ObjectMap) !void {
        var servers = std.ArrayList([]const u8).empty;
        if (frame.get("mcp_servers")) |listed| {
            if (listed == .array) {
                for (listed.array.items) |entry| {
                    if (entry != .object) continue;
                    const name = stringMember(entry.object, "name") orelse continue;
                    if (containsName(servers.items, name)) continue;
                    try servers.append(self.allocator(), name);
                }
            }
        }
        self.catalog.clearRetainingCapacity();
        if (frame.get("tools")) |listed| {
            if (listed == .array) {
                for (listed.array.items) |entry| {
                    if (entry != .string or entry.string.len == 0) continue;
                    if (self.catalogHolds(entry.string)) continue;
                    try self.catalog.append(self.allocator(), .{
                        .name = entry.string,
                        .source = toolSourceFor(self.allocator(), entry.string, servers.items) catch native_source,
                    });
                }
            }
        }
        self.catalog_known = true;
        try self.publishAttribution();
    }

    fn catalogHolds(self: *Reducer, name: []const u8) bool {
        for (self.catalog.items) |entry| {
            if (std.mem.eql(u8, entry.name, name)) return true;
        }
        return false;
    }

    fn publishAttribution(self: *Reducer) !void {
        self.attribution.clearRetainingCapacity();
        if (self.served) |served| {
            var it = served.iterator();
            while (it.next()) |entry| {
                try self.attribution.put(self.allocator(), entry.key_ptr.*, entry.value_ptr.*);
            }
            return;
        }
        for (self.catalog.items) |entry| {
            if (!std.mem.eql(u8, entry.source, native_source)) continue;
            try self.attribution.put(self.allocator(), entry.name, entry.source);
        }
    }

    pub fn listTools(self: *Reducer) !?[]const CatalogEntry {
        if (!self.catalog_known) return null;
        var served: std.StringHashMapUnmanaged([]const u8) = .empty;
        for (self.catalog.items) |entry| {
            if (entry.source.len == 0) continue;
            try served.put(self.allocator(), entry.name, entry.source);
        }
        self.served = served;
        try self.publishAttribution();
        return self.catalog.items;
    }

    fn attributionFor(self: *Reducer, name: []const u8) []const u8 {
        return self.attribution.get(name) orelse "";
    }

    fn echoMatches(self: *Reducer, message: rpc.Message) bool {
        return self.frameEchoMatches(message.object.object);
    }

    fn frameEchoMatches(self: *Reducer, frame: std.json.ObjectMap) bool {
        const run = self.run orelse return false;
        if (frame.get("user_message_uuid")) |single| {
            if (single == .string and std.mem.eql(u8, single.string, run.submission_uuid)) return true;
        }
        if (frame.get("user_message_uuids")) |many| {
            if (many == .array) {
                for (many.array.items) |item| {
                    if (item == .string and std.mem.eql(u8, item.string, run.submission_uuid)) return true;
                }
            }
        }
        return false;
    }

    fn applyRunObservation(self: *Reducer, message: rpc.Message) !void {
        const frame = message.object.object;
        if (std.mem.eql(u8, message.type, "system")) {
            if (std.mem.eql(u8, message.subtype, "task_started")) {
                try self.trackChild(frame);
                return;
            }
            if (std.mem.eql(u8, message.subtype, "task_notification")) {
                try self.settleChild(frame);
                return;
            }
            if (std.mem.eql(u8, message.subtype, "task_updated")) {
                if (terminalTaskPatch(frame)) try self.settleChild(frame);
                return;
            }
            if (std.mem.eql(u8, message.subtype, "session_state_changed")) {
                if (stringMember(frame, "state")) |state| {
                    if (std.mem.eql(u8, state, "idle")) try self.publishDeferred();
                }
                return;
            }
            try self.observeIdle(message);
            return;
        }
        if (std.mem.eql(u8, message.type, "stream_event")) {
            if (frame.get("parent_tool_use_id")) |parent| {
                if (parent != .null) return;
            }
            if (namesASubmission(frame) and !self.echoMatches(message)) return;
            const event = frame.get("event") orelse return;
            if (event != .object) return;
            const event_type = event.object.get("type") orelse return;
            if (event_type != .string) return;
            if (std.mem.eql(u8, event_type.string, "content_block_delta")) {
                const delta = event.object.get("delta") orelse return;
                if (delta != .object) return;
                const delta_type = delta.object.get("type") orelse return;
                if (delta_type != .string) return;
                if (std.mem.eql(u8, delta_type.string, "text_delta")) {
                    try self.emitDelta("text", deltaText(delta.object, "text") orelse return);
                } else if (std.mem.eql(u8, delta_type.string, "thinking_delta")) {
                    try self.emitDelta("thinking", deltaText(delta.object, "thinking") orelse return);
                }
            }
            return;
        }
        if (std.mem.eql(u8, message.type, "assistant")) {
            if (frame.get("parent_tool_use_id")) |parent| {
                if (parent != .null) return;
            }
            const native_message = frame.get("message") orelse return;
            if (native_message != .object) return;
            const content = native_message.object.get("content") orelse return;
            if (content != .array) return;
            for (content.array.items) |block| {
                if (block != .object) continue;
                const kind = block.object.get("type") orelse continue;
                if (kind != .string or !std.mem.eql(u8, kind.string, "tool_use")) continue;
                const id = block.object.get("id") orelse continue;
                if (id != .string or id.string.len == 0) continue;
                const named = block.object.get("name") orelse std.json.Value{ .string = "" };
                try self.startTool(id.string, named.string, block.object.get("input"));
            }
            return;
        }
        if (std.mem.eql(u8, message.type, "user")) {
            if (frame.get("parent_tool_use_id")) |parent| {
                if (parent != .null) return;
            }
            if (frame.get("origin")) |origin| {
                if (origin != .object) return;
                const kind = origin.object.get("kind") orelse return;
                if (kind != .string or !std.mem.eql(u8, kind.string, "human")) return;
            }
            const native_message = frame.get("message") orelse return;
            if (native_message != .object) return;
            const content = native_message.object.get("content") orelse return;
            if (content != .array) return;
            for (content.array.items) |block| {
                if (block != .object) continue;
                const kind = block.object.get("type") orelse continue;
                if (kind != .string or !std.mem.eql(u8, kind.string, "tool_result")) continue;
                const id = block.object.get("tool_use_id") orelse continue;
                if (id != .string or id.string.len == 0) continue;
                var errored = false;
                if (block.object.get("is_error")) |flag| {
                    if (flag == .bool) errored = flag.bool;
                }
                try self.endTool(id.string, block.object.get("content"), errored);
            }
            return;
        }
        if (std.mem.eql(u8, message.type, "result")) {
            try self.settle(frame);
            return;
        }
    }
};

fn stringMember(map: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = map.get(key) orelse return null;
    if (value != .string or value.string.len == 0) return null;
    return value.string;
}

fn namesASubmission(frame: std.json.ObjectMap) bool {
    if (frame.get("user_message_uuid")) |single| {
        if (single == .string and single.string.len > 0) return true;
    }
    if (frame.get("user_message_uuids")) |many| {
        if (many == .array and many.array.items.len > 0) return true;
    }
    return false;
}

fn containsName(names: []const []const u8, candidate: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, candidate)) return true;
    }
    return false;
}

fn toolSourceFor(allocator: std.mem.Allocator, name: []const u8, servers: []const []const u8) ![]const u8 {
    if (!std.mem.startsWith(u8, name, mcp_tool_prefix)) return native_source;
    const rest = name[mcp_tool_prefix.len..];
    var longest: []const u8 = "";
    for (servers) |server| {
        if (server.len <= longest.len) continue;
        if (rest.len < server.len + 2) continue;
        if (!std.mem.eql(u8, rest[0..server.len], server)) continue;
        if (!std.mem.eql(u8, rest[server.len .. server.len + 2], "__")) continue;
        longest = server;
    }
    if (longest.len == 0) return native_source;
    return std.fmt.allocPrint(allocator, mcp_source_prefix ++ "{s}", .{longest});
}

fn integerMember(map: std.json.ObjectMap, key: []const u8) ?i64 {
    const value = map.get(key) orelse return null;
    return switch (value) {
        .integer => |number| number,
        .float => |number| @intFromFloat(number),
        else => null,
    };
}

fn toolResultText(content: ?std.json.Value) []const u8 {
    if (content) |value| {
        if (value == .string and value.string.len > 0) return value.string;
    }
    return "tool call failed";
}

fn defersTerminal(task_type: []const u8) bool {
    return std.mem.eql(u8, task_type, "local_agent") or std.mem.eql(u8, task_type, "local_workflow");
}

fn terminalTaskPatch(frame: std.json.ObjectMap) bool {
    const patch = frame.get("patch") orelse return false;
    if (patch != .object) return false;
    const status = stringMember(patch.object, "status") orelse return false;
    for ([_][]const u8{ "completed", "failed", "stopped", "killed" }) |terminal| {
        if (std.mem.eql(u8, status, terminal)) return true;
    }
    return false;
}

fn cancelled(frame: std.json.ObjectMap) bool {
    const reason = stringMember(frame, "terminal_reason") orelse return false;
    return std.mem.eql(u8, reason, "aborted_streaming") or std.mem.eql(u8, reason, "aborted_tools");
}

fn maxTurns(frame: std.json.ObjectMap) bool {
    if (stringMember(frame, "subtype")) |subtype| {
        if (std.mem.eql(u8, subtype, "error_max_turns")) return true;
    }
    if (stringMember(frame, "terminal_reason")) |reason| {
        if (std.mem.eql(u8, reason, "max_turns")) return true;
    }
    return false;
}

fn failed(frame: std.json.ObjectMap) bool {
    if (maxTurns(frame)) return false;
    if (frame.get("is_error")) |flag| {
        if (flag == .bool and flag.bool) return true;
    }
    const subtype = stringMember(frame, "subtype") orelse return true;
    return !std.mem.eql(u8, subtype, "success");
}

fn deltaText(delta: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = delta.get(key) orelse return "";
    return if (value == .string) value.string else null;
}

fn everyItemIsAString(listed: std.json.Array) bool {
    for (listed.items) |entry| {
        if (entry != .string) return false;
    }
    return true;
}

fn stopReason(frame: std.json.ObjectMap) []const u8 {
    if (stringMember(frame, "terminal_reason")) |reason| return reason;
    if (stringMember(frame, "stop_reason")) |reason| return reason;
    return "completed";
}

const testing = std.testing;

fn observeText(reducer: *Reducer, arena: std.mem.Allocator, text: []const u8) !void {
    const message = try rpc.parseMessage(arena, text, null);
    try reducer.observe(message);
}

fn startedModels(reducer: *Reducer, arena: std.mem.Allocator) ![]const []const u8 {
    var models = std.ArrayList([]const u8).empty;
    for (reducer.envelopes.items) |envelope| {
        const kind = envelope.object.get("type").?;
        if (!std.mem.eql(u8, kind.string, "run.started")) continue;
        try models.append(arena, envelope.object.get("payload").?.object.get("model_id").?.string);
    }
    return models.items;
}

test "ids are allocated from one counter across four kinds" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, arena.allocator(),
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );

    try testing.expectEqualStrings("submission-1", reducer.run.?.submission_id);
    try testing.expectEqualStrings("message-2", reducer.run.?.message_id);
    try testing.expectEqualStrings("run-3", reducer.run.?.id);
    try testing.expectEqualStrings("event-4", reducer.envelopes.items[0].object.get("id").?.string);
}

test "a run reports the model captured at submit, not the one init later published" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","tools":[],"uuid":"i1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","terminal_reason":"completed","result":"one","user_message_uuid":"turn-1","uuid":"r1"}
    );

    try reducer.submit("turn-2");
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-b","tools":[],"uuid":"i2"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e2","user_message_uuid":"turn-2"}
    );

    const models = try startedModels(&reducer, scratch);
    try testing.expectEqual(@as(usize, 2), models.len);
    try testing.expectEqualStrings("claude-test", models[0]);
    try testing.expectEqualStrings("model-a", models[1]);
}

test "a pending run's init is not adopted until the run starts" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","tools":[],"uuid":"i1"}
    );
    try testing.expectEqualStrings("claude-test", reducer.current_model);

    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try testing.expectEqualStrings("model-a", reducer.current_model);
}

fn toolSources(reducer: *Reducer, arena: std.mem.Allocator) ![]const []const u8 {
    var sources = std.ArrayList([]const u8).empty;
    for (reducer.envelopes.items) |envelope| {
        const kind = envelope.object.get("type").?;
        if (!std.mem.eql(u8, kind.string, "action.call.requested")) continue;
        const payload = envelope.object.get("payload").?.object;
        const source = payload.get("source") orelse std.json.Value{ .string = "" };
        try sources.append(arena, source.string);
    }
    return sources.items;
}

test "a call is attributed to the harness only when init advertised the tool" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","tools":["Bash","mcp__files__read"],"mcp_servers":[{"name":"files"}],"uuid":"i1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"},{"type":"tool_use","id":"t2","name":"Read"},{"type":"tool_use","id":"t3","name":"mcp__files__read"}]},"uuid":"a1"}
    );

    const sources = try toolSources(&reducer, scratch);
    try testing.expectEqual(@as(usize, 3), sources.len);
    try testing.expectEqualStrings(native_source, sources[0]);
    try testing.expectEqualStrings("", sources[1]);
    try testing.expectEqualStrings("", sources[2]);
}

fn emittedTypes(reducer: *Reducer, arena: std.mem.Allocator) ![]const []const u8 {
    var kinds = std.ArrayList([]const u8).empty;
    for (reducer.envelopes.items) |envelope| {
        try kinds.append(arena, envelope.object.get("type").?.string);
    }
    return kinds.items;
}

test "a user frame the harness did not attribute to a human settles nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"user","session_id":"s","origin":{},"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},"uuid":"u1"}
    );

    const kinds = try emittedTypes(&reducer, scratch);
    try testing.expectEqual(@as(usize, 3), kinds.len);
    try testing.expectEqualStrings("action.call.started", kinds[2]);
}

test "a delta echoing another submission is not attributed to this run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"mine"}},"uuid":"e2","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"theirs"}},"uuid":"e3","user_message_uuid":"turn-9"}
    );

    const kinds = try emittedTypes(&reducer, scratch);
    try testing.expectEqual(@as(usize, 2), kinds.len);
    try testing.expectEqualStrings("content.delta", kinds[1]);
}

fn startedRun(reducer: *Reducer, arena: std.mem.Allocator, uuid: []const u8) !void {
    try reducer.submit(uuid);
    const text = try std.fmt.allocPrint(arena,
        \\{{"type":"stream_event","session_id":"s","event":{{"type":"message_start"}},"uuid":"e","user_message_uuid":"{s}"}}
    , .{uuid});
    try observeText(reducer, arena, text);
}

fn gateRequest(reducer: *Reducer, arena: std.mem.Allocator, extra: []const u8) !void {
    const text = try std.fmt.allocPrint(arena,
        \\{{"type":"control_request","request_id":"ask-1","request":{{"subtype":"can_use_tool","tool_use_id":"t1","tool_name":"Bash","input":{{"command":"ls"}}{s}}}}}
    , .{extra});
    try observeText(reducer, arena, text);
}

fn firstPayload(reducer: *Reducer, kind: []const u8) ?std.json.ObjectMap {
    for (reducer.envelopes.items) |envelope| {
        if (std.mem.eql(u8, envelope.object.get("type").?.string, kind)) {
            return envelope.object.get("payload").?.object;
        }
    }
    return null;
}

test "a permission ask outside an owned run opens no gate" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try gateRequest(&reducer, scratch, "");
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try testing.expectEqual(@as(?[]const u8, null), reducer.pendingInteraction());
    try testing.expect(reducer.unusable);

    var pending = Reducer.init(&arena, .{});
    pending.open();
    try pending.submit("turn-1");
    try gateRequest(&pending, scratch, "");
    try testing.expectEqual(@as(usize, 0), pending.envelopes.items.len);
    try testing.expectEqual(@as(?[]const u8, null), pending.pendingInteraction());
    try testing.expect(pending.unusable);
    try testing.expect(pending.run == null);
}

test "the harness names the gate when it can, and the reducer names it when it cannot" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var named = Reducer.init(&arena, .{});
    named.open();
    try startedRun(&named, scratch, "turn-1");
    try gateRequest(&named, scratch,
        \\,"title":"Run a command","decision_reason":"outside the workspace"
    );
    const named_payload = firstPayload(&named, "user.input.requested").?;
    try testing.expectEqualStrings("Run a command", named_payload.get("title").?.string);
    try testing.expectEqualStrings("outside the workspace", named_payload.get("description").?.string);

    var bare = Reducer.init(&arena, .{});
    bare.open();
    try startedRun(&bare, scratch, "turn-1");
    try gateRequest(&bare, scratch, "");
    const bare_payload = firstPayload(&bare, "user.input.requested").?;
    try testing.expectEqualStrings("Use Bash", bare_payload.get("title").?.string);
    try testing.expectEqual(@as(?std.json.Value, null), bare_payload.get("description"));
}

test "a gate is resolved once, whatever the second answer says" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try gateRequest(&reducer, scratch, "");
    const pending = reducer.pendingInteraction().?;
    try reducer.resolve(pending, .allow);
    const settled = reducer.envelopes.items.len;

    try testing.expectEqual(@as(?[]const u8, null), reducer.pendingInteraction());
    try reducer.resolve(pending, .deny);
    try testing.expectEqual(settled, reducer.envelopes.items.len);
}

fn failureCodeOf(reducer: *Reducer) ?[]const u8 {
    const payload = firstPayload(reducer, "run.failed") orelse return null;
    return payload.get("error").?.object.get("code").?.string;
}

test "a failure names the terminal reason when the subtype says success" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reasoned = Reducer.init(&arena, .{});
    reasoned.open();
    try startedRun(&reasoned, scratch, "turn-1");
    try observeText(&reasoned, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","is_error":true,"terminal_reason":"aborted_budget","user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqualStrings("claude_aborted_budget", failureCodeOf(&reasoned).?);

    var subtyped = Reducer.init(&arena, .{});
    subtyped.open();
    try startedRun(&subtyped, scratch, "turn-1");
    try observeText(&subtyped, scratch,
        \\{"type":"result","session_id":"s","subtype":"error_during_execution","is_error":true,"terminal_reason":"aborted_budget","user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqualStrings("claude_error_during_execution", failureCodeOf(&subtyped).?);

    var api = Reducer.init(&arena, .{});
    api.open();
    try startedRun(&api, scratch, "turn-1");
    try observeText(&api, scratch,
        \\{"type":"result","session_id":"s","subtype":"error_during_execution","is_error":true,"api_error_status":429,"user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqualStrings("claude_api_429", failureCodeOf(&api).?);
}

test "a terminal naming another submission settles nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"theirs","user_message_uuid":"turn-9","uuid":"r1"}
    );
    try testing.expectEqual(@as(?std.json.ObjectMap, null), firstPayload(&reducer, "run.completed"));

    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"mine","user_message_uuid":"turn-1","uuid":"r2"}
    );
    try testing.expectEqualStrings("mine", firstPayload(&reducer, "run.completed").?.get("final_response").?.object.get("content").?.string);
}

test "a call still open when the run settles is cancelled before the terminal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"done","user_message_uuid":"turn-1","uuid":"r1"}
    );

    const kinds = try emittedTypes(&reducer, scratch);
    try testing.expectEqual(@as(usize, 5), kinds.len);
    try testing.expectEqualStrings("action.call.cancelled", kinds[3]);
    try testing.expectEqualStrings("run.completed", kinds[4]);
}

test "a transport that dies before the run starts settles nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();

    var pending = Reducer.init(&arena, .{});
    pending.open();
    try pending.submit("turn-1");
    try pending.transportFailed("the test closed the transport");
    try testing.expectEqual(@as(usize, 0), pending.envelopes.items.len);

    var running = Reducer.init(&arena, .{});
    running.open();
    try startedRun(&running, arena.allocator(), "turn-1");
    try running.transportFailed("the test closed the transport");
    try testing.expectEqualStrings("run.failed", running.envelopes.items[1].object.get("type").?.string);
}

fn childRun(reducer: *Reducer, arena: std.mem.Allocator, task_type: []const u8) !void {
    reducer.open();
    try startedRun(reducer, arena, "turn-1");
    const started = try std.fmt.allocPrint(arena,
        \\{{"type":"system","session_id":"s","subtype":"task_started","description":"a child","task_id":"task-1","task_type":"{s}","uuid":"t1"}}
    , .{task_type});
    try observeText(reducer, arena, started);
    try observeText(reducer, arena,
        \\{"type":"result","session_id":"s","subtype":"success","result":"spawned","user_message_uuid":"turn-1","queued_turn_count":0,"uuid":"r1"}
    );
}

test "a local child holds the terminal until it settles" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});

    try childRun(&reducer, scratch, "local_agent");
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"task_notification","output_file":"/out","summary":"done","task_id":"task-1","status":"completed","uuid":"t2"}
    );
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.completed", reducer.envelopes.items[1].object.get("type").?.string);
}

test "a child of another kind holds nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{});

    try childRun(&reducer, arena.allocator(), "remote_agent");
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.completed", reducer.envelopes.items[1].object.get("type").?.string);
}

test "a patch that is not terminal leaves the child running" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});

    try childRun(&reducer, scratch, "local_workflow");
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"task_updated","task_id":"task-1","patch":{"status":"running"},"uuid":"t2"}
    );
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"task_updated","task_id":"task-1","patch":{"status":"killed"},"uuid":"t3"}
    );
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
}

test "an idle session publishes a terminal its children are still holding" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});

    try childRun(&reducer, scratch, "local_agent");
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"session_state_changed","state":"busy","uuid":"s1"}
    );
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"session_state_changed","state":"idle","uuid":"s2"}
    );
    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.completed", reducer.envelopes.items[1].object.get("type").?.string);
}

fn advertise(reducer: *Reducer, arena: std.mem.Allocator) !void {
    reducer.open();
    try observeText(reducer, arena,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","uuid":"i1","mcp_servers":[{"name":"files__nested"},{"name":"files"}],"tools":["Bash","mcp__files__read","mcp__files__nested__read","mcp__filesXread","mcp__absent__ghost","Bash"]}
    );
}

fn callEachTool(reducer: *Reducer, arena: std.mem.Allocator) !void {
    try startedRun(reducer, arena, "turn-1");
    try observeText(reducer, arena,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"},{"type":"tool_use","id":"t2","name":"mcp__files__read"},{"type":"tool_use","id":"t3","name":"mcp__files__nested__read"},{"type":"tool_use","id":"t4","name":"mcp__filesXread"},{"type":"tool_use","id":"t5","name":"mcp__absent__ghost"}]},"uuid":"a1"}
    );
}

test "a tool's source is the longest server namespace its name matches" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});

    try advertise(&reducer, scratch);
    const served = (try reducer.listTools()).?;
    try testing.expectEqual(@as(usize, 5), served.len);

    try callEachTool(&reducer, scratch);
    const sources = try toolSources(&reducer, scratch);
    try testing.expectEqual(@as(usize, 5), sources.len);
    try testing.expectEqualStrings(native_source, sources[0]);
    try testing.expectEqualStrings("mcp:files", sources[1]);
    try testing.expectEqualStrings("mcp:files__nested", sources[2]);
    try testing.expectEqualStrings(native_source, sources[3]);
    try testing.expectEqualStrings(native_source, sources[4]);
}

test "a catalog nobody has served attributes only what the endpoint owns" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});

    reducer.open();
    try testing.expectEqual(@as(?[]const CatalogEntry, null), try reducer.listTools());

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","uuid":"i1","mcp_servers":[{"name":"files__nested"},{"name":"files"}],"tools":["Bash","mcp__files__read","mcp__files__nested__read","mcp__filesXread","mcp__absent__ghost","Bash"]}
    );
    try callEachTool(&reducer, scratch);

    const sources = try toolSources(&reducer, scratch);
    try testing.expectEqualStrings(native_source, sources[0]);
    try testing.expectEqualStrings("", sources[1]);
    try testing.expectEqualStrings("", sources[2]);
    try testing.expectEqualStrings(native_source, sources[3]);
    try testing.expectEqualStrings(native_source, sources[4]);
}

fn failureMessageOf(reducer: *Reducer) ?[]const u8 {
    const payload = firstPayload(reducer, "run.failed") orelse return null;
    return payload.get("error").?.object.get("message").?.string;
}

test "a tool lifecycle the oracle refuses fails the run here too" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var duplicated = Reducer.init(&arena, .{});
    duplicated.open();
    try startedRun(&duplicated, scratch, "turn-1");
    try observeText(&duplicated, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&duplicated, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a2"}
    );
    const after_duplicate = try emittedTypes(&duplicated, scratch);
    try testing.expectEqual(@as(usize, 4), after_duplicate.len);
    try testing.expectEqualStrings("run.failed", after_duplicate[3]);
    try testing.expectEqualStrings("claude_tool_lifecycle", failureCodeOf(&duplicated).?);
    try testing.expectEqualStrings("duplicate tool call", failureMessageOf(&duplicated).?);

    var unmatched = Reducer.init(&arena, .{});
    unmatched.open();
    try startedRun(&unmatched, scratch, "turn-1");
    try observeText(&unmatched, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"nobody","content":"out"}]},"uuid":"u1"}
    );
    try testing.expectEqualStrings("unmatched tool completion", failureMessageOf(&unmatched).?);

    var twice = Reducer.init(&arena, .{});
    twice.open();
    try startedRun(&twice, scratch, "turn-1");
    try observeText(&twice, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&twice, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},"uuid":"u1"}
    );
    try observeText(&twice, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},"uuid":"u2"}
    );
    try testing.expectEqualStrings("unmatched tool completion", failureMessageOf(&twice).?);
}

test "a completion for an earlier run's call is ignored, not refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"one","user_message_uuid":"turn-1","uuid":"r1"}
    );
    const settled = reducer.envelopes.items.len;

    try startedRun(&reducer, scratch, "turn-2");
    try observeText(&reducer, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"late"}]},"uuid":"u1"}
    );
    try testing.expectEqual(settled + 1, reducer.envelopes.items.len);
    try testing.expectEqual(@as(?[]const u8, null), failureMessageOf(&reducer));
}

test "a failure message joins every error the harness reported" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var many = Reducer.init(&arena, .{});
    many.open();
    try startedRun(&many, scratch, "turn-1");
    try observeText(&many, scratch,
        \\{"type":"result","session_id":"s","subtype":"error_during_execution","is_error":true,"errors":["first","second"],"user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqualStrings("first; second", failureMessageOf(&many).?);

    var status = Reducer.init(&arena, .{});
    status.open();
    try startedRun(&status, scratch, "turn-1");
    try observeText(&status, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","is_error":true,"api_error_status":503,"user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqualStrings("API error (HTTP 503)", failureMessageOf(&status).?);

    var bare = Reducer.init(&arena, .{});
    bare.open();
    try startedRun(&bare, scratch, "turn-1");
    try observeText(&bare, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","is_error":true,"user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqualStrings("unknown error", failureMessageOf(&bare).?);
}

test "a call the failed run left open is not swept into the next run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"nobody","content":"out"}]},"uuid":"u1"}
    );
    try testing.expectEqualStrings("run.failed", reducer.envelopes.items[3].object.get("type").?.string);
    const refused_at = reducer.envelopes.items.len;

    try startedRun(&reducer, scratch, "turn-2");
    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"two","user_message_uuid":"turn-2","uuid":"r1"}
    );

    const kinds = try emittedTypes(&reducer, scratch);
    try testing.expectEqual(refused_at + 2, kinds.len);
    try testing.expectEqualStrings("run.started", kinds[refused_at]);
    try testing.expectEqualStrings("run.completed", kinds[refused_at + 1]);
}

test "a session the harness broke admits nothing further" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = Reducer.init(&arena, .{});
    reducer.open();
    try startedRun(&reducer, scratch, "turn-1");
    try reducer.transportFailed("the test closed the transport");
    const settled = reducer.envelopes.items.len;

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-b","tools":[],"uuid":"i1"}
    );
    try testing.expectEqualStrings("model-b", reducer.current_model);

    try testing.expectError(error.SessionClosed, reducer.submit("turn-2"));
    try testing.expect(reducer.run == null);

    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"two","user_message_uuid":"turn-2","uuid":"r1"}
    );
    try testing.expectEqual(settled, reducer.envelopes.items.len);
}

test "a reverse control request the adapter does not own is external activity" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var running = Reducer.init(&arena, .{});
    running.open();
    try startedRun(&running, scratch, "turn-1");
    try observeText(&running, scratch,
        \\{"type":"control_request","request_id":"c1","request":{"subtype":"hook_callback"}}
    );
    try testing.expectEqualStrings("claude_external_activity", failureCodeOf(&running).?);
    try testing.expectEqualStrings("reverse control request \"hook_callback\"", failureMessageOf(&running).?);

    var idle = Reducer.init(&arena, .{});
    idle.open();
    try observeText(&idle, scratch,
        \\{"type":"control_request","request_id":"c1","request":{"subtype":"hook_callback"}}
    );
    try testing.expectEqual(@as(usize, 0), idle.envelopes.items.len);
    try testing.expect(idle.unusable);
}

test "a gate the CLI withdraws resolves as cancelled and releases the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try gateRequest(&reducer, scratch, "");
    const opened = reducer.envelopes.items.len;

    try observeText(&reducer, scratch,
        \\{"type":"control_cancel_request","request_id":"other"}
    );
    try testing.expectEqual(opened, reducer.envelopes.items.len);

    try observeText(&reducer, scratch,
        \\{"type":"control_cancel_request","request_id":"ask-1"}
    );
    const kinds = try emittedTypes(&reducer, scratch);
    try testing.expectEqual(opened + 2, kinds.len);
    try testing.expectEqualStrings("user.input.resolved", kinds[opened]);
    try testing.expectEqualStrings("run.status.updated", kinds[opened + 1]);
    try testing.expectEqualStrings("cancelled", reducer.envelopes.items[opened].object.get("payload").?.object.get("status").?.string);
    try testing.expectEqual(@as(?[]const u8, null), reducer.pendingInteraction());
}

test "a delta whose member is absent emits an empty part the oracle omits" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var absent = Reducer.init(&arena, .{});
    absent.open();
    try startedRun(&absent, scratch, "turn-1");
    try observeText(&absent, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"content_block_delta","delta":{"type":"text_delta"}},"uuid":"e2"}
    );
    try testing.expectEqualStrings("", firstPayload(&absent, "content.delta").?.get("part").?.object.get("text").?.string);

    var wrong_type = Reducer.init(&arena, .{});
    wrong_type.open();
    try startedRun(&wrong_type, scratch, "turn-1");
    try observeText(&wrong_type, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":7}},"uuid":"e2"}
    );
    try testing.expectEqual(@as(?std.json.ObjectMap, null), firstPayload(&wrong_type, "content.delta"));
}

test "a failed run leaves neither its children nor its gate to the next one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var orphaned_child = Reducer.init(&arena, .{});
    orphaned_child.open();
    try startedRun(&orphaned_child, scratch, "turn-1");
    try observeText(&orphaned_child, scratch,
        \\{"type":"system","session_id":"s","subtype":"task_started","description":"a child","task_id":"task-1","task_type":"local_agent","uuid":"t1"}
    );
    try observeText(&orphaned_child, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"nobody","content":"out"}]},"uuid":"u1"}
    );
    try testing.expectEqualStrings("claude_tool_lifecycle", failureCodeOf(&orphaned_child).?);

    try startedRun(&orphaned_child, scratch, "turn-2");
    try observeText(&orphaned_child, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"two","user_message_uuid":"turn-2","uuid":"r1"}
    );
    const kinds = try emittedTypes(&orphaned_child, scratch);
    try testing.expectEqualStrings("run.completed", kinds[kinds.len - 1]);

    var orphaned_gate = Reducer.init(&arena, .{});
    orphaned_gate.open();
    try startedRun(&orphaned_gate, scratch, "turn-1");
    try gateRequest(&orphaned_gate, scratch, "");
    try observeText(&orphaned_gate, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"nobody","content":"out"}]},"uuid":"u1"}
    );
    try startedRun(&orphaned_gate, scratch, "turn-2");
    try testing.expectEqual(@as(?[]const u8, null), orphaned_gate.pendingInteraction());

    const before = orphaned_gate.envelopes.items.len;
    try observeText(&orphaned_gate, scratch,
        \\{"type":"control_cancel_request","request_id":"ask-1"}
    );
    try testing.expectEqual(before, orphaned_gate.envelopes.items.len);
}

test "a broken session still adopts what it is told while idle" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","tools":[],"uuid":"i1"}
    );
    try reducer.transportFailed("the test closed the transport");
    const settled = reducer.envelopes.items.len;

    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-b","tools":[],"uuid":"i2"}
    );
    try testing.expectEqualStrings("model-b", reducer.current_model);
    try testing.expectEqual(settled, reducer.envelopes.items.len);
}

test "a submission arriving under a live run is refused, not substituted" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try testing.expectError(error.RunActive, reducer.submit("turn-2"));
    try testing.expectEqualStrings("turn-1", reducer.run.?.submission_uuid);

    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try testing.expectError(error.RunActive, reducer.submit("turn-2"));
    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);

    try observeText(&reducer, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"one","user_message_uuid":"turn-1","uuid":"r1"}
    );
    try reducer.submit("turn-2");
    try testing.expectEqualStrings("turn-2", reducer.run.?.submission_uuid);
}

test "a call with no input still carries the member the schema requires" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    const requested = firstPayload(&reducer, "action.call.requested").?;
    const arguments = requested.get("arguments_json").?;
    try testing.expectEqual(std.json.Value{ .null = {} }, arguments);
}

test "a terminal omits a duration the oracle would not have sent" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var instant = Reducer.init(&arena, .{});
    instant.open();
    try startedRun(&instant, scratch, "turn-1");
    try observeText(&instant, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"one","duration_ms":0,"user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqual(@as(?std.json.Value, null), firstPayload(&instant, "run.completed").?.get("duration_ms"));

    var measured = Reducer.init(&arena, .{});
    measured.open();
    try startedRun(&measured, scratch, "turn-1");
    try observeText(&measured, scratch,
        \\{"type":"result","session_id":"s","subtype":"success","result":"one","duration_ms":130,"user_message_uuid":"turn-1","uuid":"r1"}
    );
    try testing.expectEqual(@as(i64, 130), firstPayload(&measured, "run.completed").?.get("duration_ms").?.integer);
}

test "replay stops at the frame that failed the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try reducer.submit("turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"nobody","content":"out"}]},"uuid":"u1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-b","tools":[],"uuid":"i1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","session_id":"s","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );

    try testing.expectEqualStrings("claude_tool_lifecycle", failureCodeOf(&reducer).?);
    try testing.expectEqualStrings("claude-test", reducer.current_model);
}

test "a tool block the harness left nameless still opens its call" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try startedRun(&reducer, scratch, "turn-1");
    try observeText(&reducer, scratch,
        \\{"type":"assistant","session_id":"s","message":{"model":"model-a","content":[{"type":"tool_use","id":"t1"}]},"uuid":"a1"}
    );
    const requested = firstPayload(&reducer, "action.call.requested").?;
    try testing.expectEqualStrings("", requested.get("name").?.string);

    try observeText(&reducer, scratch,
        \\{"type":"user","session_id":"s","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},"uuid":"u1"}
    );
    try testing.expectEqual(@as(?std.json.ObjectMap, null), firstPayload(&reducer, "run.failed"));
    try testing.expect(firstPayload(&reducer, "action.call.completed") != null);
}

test "an init outside a pending run is adopted when it arrives" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try observeText(&reducer, arena.allocator(),
        \\{"type":"system","session_id":"s","subtype":"init","model":"model-a","tools":[],"uuid":"i1"}
    );
    try testing.expectEqualStrings("model-a", reducer.current_model);
}
