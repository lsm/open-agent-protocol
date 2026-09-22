const std = @import("std");
const rpc = @import("rpc");
const goquote = @import("goquote");

pub const capability_revision = "hermes-v2026.8.31-oap-v1";
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const endpoint_id = "hermes.gateway";

const Reduce = std.mem.Allocator.Error;

pub const Error = error{ RunActive, SessionUnusable, InteractionNotFound, InvalidResolution };

pub const Options = struct {
    session_id: []const u8 = "session",
    native_id: []const u8 = "sess0001",
    model: []const u8 = "hermes-test",
    responder: []const u8 = "user",
    endpoint: []const u8 = endpoint_id,
    revision: []const u8 = capability_revision,
};

pub const Identity = struct {
    run_id: []const u8 = "",
};

const Tool = struct {
    native_id: []const u8,
    id: []const u8,
    run_id: []const u8,
    name: []const u8 = "",
    args: ?std.json.Value = null,
    requested: []const u8 = "",
    started: []const u8 = "",
    terminal: bool = false,
};

pub const Option = struct {
    id: []const u8,
    label: []const u8,
};

pub const Question = struct {
    id: []const u8,
    prompt: []const u8,
    kind: []const u8,
    options: []const Option = &.{},
};

pub const Answer = struct {
    question_id: []const u8,
    selected_option_ids: []const []const u8 = &.{},
    text: []const u8 = "",
};

pub const Interaction = struct {
    id: []const u8,
    run_id: []const u8,
    kind: []const u8,
    request_id: []const u8 = "",
    requested: []const u8 = "",
    resolved: bool = false,
    questions: []const Question = &.{},
};

const Observation = struct {
    kind: []const u8,
    payload: std.json.Value,
};

const Run = struct {
    id: []const u8 = "",
    message_id: []const u8 = "",
    sequence: i64 = 0,
    started: bool = false,
    open_seen: bool = false,
    terminal: bool = false,
};

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    identity: Identity = .{},
    ids: usize = 0,
    clock: i64 = 0,
    run: ?Run = null,
    tools: std.ArrayList(Tool) = .empty,
    last_seq: i64 = 0,
    buffered: std.ArrayList(Observation) = .empty,
    unusable: bool = false,
    interactions: std.ArrayList(Interaction) = .empty,
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
        return std.fmt.allocPrint(self.allocator(), "{s}-{u}", .{ kind, idScalar(self.ids - 1) });
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

    fn active(self: *Reducer) ?*Run {
        if (self.run == null) return null;
        const run = &self.run.?;
        return if (run.terminal) null else run;
    }

    pub fn submit(self: *Reducer) !void {
        return self.submitAs(.{});
    }

    pub fn submitAs(self: *Reducer, identity: Identity) !void {
        if (self.unusable) return Error.SessionUnusable;
        if (self.run) |run| {
            if (!run.terminal) return Error.RunActive;
        }
        self.identity = identity;
        self.run = .{ .message_id = try self.nextID("message") };
    }

    fn startRun(self: *Reducer) Reduce!void {
        const run = self.active() orelse return;
        if (run.started) return;
        run.started = true;
        const minted = try self.nextID("run");
        run.id = if (self.identity.run_id.len > 0) self.identity.run_id else minted;

        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "status", str("running"));
        if (self.options.model.len > 0) try self.put(&payload, "model_id", str(self.options.model));
        try self.put(&payload, "started_at_ms", int(self.now()));
        _ = try self.emit(run, "run.started", .{ .object = payload });

        const replay = self.buffered;
        self.buffered = .empty;
        for (replay.items) |observation| {
            if (self.run == null or self.run.?.terminal) return;
            try self.applyRunEvent(observation.kind, observation.payload);
        }
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value) ![]const u8 {
        return self.emitEnvelope(run, kind, payload, "");
    }

    fn emitEnvelope(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, in_reply_to: []const u8) ![]const u8 {
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
        if (payload == .object) {
            if (std.mem.startsWith(u8, kind, "action.call.")) {
                if (payload.object.get("tool_call_id")) |carried| {
                    if (carried == .string) try self.put(&envelope, "tool_call_id", carried);
                }
            }
            if (std.mem.eql(u8, kind, "user.input.requested") or std.mem.eql(u8, kind, "user.input.resolved")) {
                if (payload.object.get("interaction_id")) |carried| {
                    if (carried == .string) try self.put(&envelope, "turn_id", carried);
                }
            }
        }
        try self.put(&envelope, "capability_revision", str(self.options.revision));
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
        return id;
    }

    pub fn observe(self: *Reducer, message: rpc.Message, parsed: std.json.Value) !void {
        if (message.kind == .request) return self.disown("reverse request");
        if (message.kind != .notification) return;
        if (!std.mem.eql(u8, message.method, "event")) return self.disown("non-event notification");
        if (parsed != .object) return;
        const params = parsed.object.get("params") orelse return;
        if (params != .object) return;
        const kind = params.object.get("type") orelse return;
        if (kind != .string) return;
        const native_id = stringMember(params.object, "session_id");
        if (native_id.len == 0) return;
        if (!std.mem.eql(u8, native_id, self.options.native_id)) {
            try self.disown(try std.fmt.allocPrint(self.allocator(), "event for foreign session {s}", .{goquote.quote(self.allocator(), native_id)}));
            return;
        }
        const seq = integerMember(params.object, "seq");
        if (seq != self.last_seq + 1) {
            try self.disown(try std.fmt.allocPrint(self.allocator(), "non-contiguous seq {d} after {d}", .{ seq, self.last_seq }));
            return;
        }
        self.last_seq = seq;
        if (self.unusable) return;
        const payload = params.object.get("payload") orelse std.json.Value{ .null = {} };
        try self.applyEvent(kind.string, payload);
    }

    fn applyEvent(self: *Reducer, kind: []const u8, payload: std.json.Value) !void {
        const run = self.active() orelse {
            if (!runScoped(kind)) return;
            return self.disown(try std.fmt.allocPrint(self.allocator(), "session event {s} without a reserved run", .{goquote.quote(self.allocator(), kind)}));
        };
        if (!run.started) return self.reserveObservation(kind, payload);
        try self.applyRunEvent(kind, payload);
    }

    fn reserveObservation(self: *Reducer, kind: []const u8, payload: std.json.Value) !void {
        if (std.mem.eql(u8, kind, "message.start")) return self.openTurn();
        try self.buffered.append(self.allocator(), .{ .kind = kind, .payload = payload });
    }

    fn openTurn(self: *Reducer) Reduce!void {
        const run = self.active() orelse return;
        if (run.open_seen) {
            try self.failRun("hermes_invalid_grammar", "turn opened twice");
            return;
        }
        run.open_seen = true;
        if (!run.started) try self.startRun();
    }

    fn applyRunEvent(self: *Reducer, kind: []const u8, payload: std.json.Value) Reduce!void {
        if (std.mem.eql(u8, kind, "message.start")) return self.openTurn();
        if (std.mem.eql(u8, kind, "message.delta")) {
            try self.emitDelta(payload, "text");
            return;
        }
        if (std.mem.eql(u8, kind, "reasoning.delta") or std.mem.eql(u8, kind, "thinking.delta")) {
            try self.emitDelta(payload, "reasoning");
            return;
        }
        if (std.mem.eql(u8, kind, "message.complete")) {
            try self.settleRun(payload);
            return;
        }
        if (std.mem.eql(u8, kind, "tool.start")) {
            try self.startTool(payload);
            return;
        }
        if (std.mem.eql(u8, kind, "tool.complete")) {
            try self.endTool(payload);
            return;
        }
        if (std.mem.eql(u8, kind, "approval.request") or std.mem.eql(u8, kind, "clarify.request") or std.mem.eql(u8, kind, "sudo.request") or std.mem.eql(u8, kind, "secret.request")) {
            try self.openInteraction(kind, payload);
            return;
        }
        if (std.mem.eql(u8, kind, "clarify.expire") or std.mem.eql(u8, kind, "sudo.expire") or std.mem.eql(u8, kind, "secret.expire")) {
            try self.expireInteraction(payload);
            return;
        }
    }

    fn findTool(self: *Reducer, run_id: []const u8, native_id: []const u8) ?usize {
        for (self.tools.items, 0..) |tool, index| {
            if (std.mem.eql(u8, tool.run_id, run_id) and std.mem.eql(u8, tool.native_id, native_id)) return index;
        }
        return null;
    }

    fn toolPayload(self: *Reducer, tool: *const Tool, arguments: bool) !std.json.ObjectMap {
        var body = self.object();
        try self.put(&body, "session_id", str(self.options.session_id));
        try self.put(&body, "run_id", str(tool.run_id));
        try self.put(&body, "tool_call_id", str(tool.id));
        try self.put(&body, "requested_by", str(self.options.endpoint));
        try self.put(&body, "execution_owner", str("hermes"));
        if (tool.name.len > 0) try self.put(&body, "name", str(tool.name));
        if (arguments) {
            if (tool.args) |value| try self.put(&body, "arguments_json", value);
        }
        return body;
    }

    fn startTool(self: *Reducer, payload: std.json.Value) !void {
        const run = self.active() orelse return;
        if (!run.started) return;
        if (payload != .object) {
            try self.failRun("hermes_invalid_event", "invalid tool start");
            return;
        }
        const native_id = stringMember(payload.object, "tool_id");
        if (self.findTool(run.id, native_id) != null) {
            try self.failRun("hermes_tool_lifecycle", "duplicate tool call");
            return;
        }
        const minted = try self.nextID("tool-call");
        try self.tools.append(self.allocator(), .{
            .native_id = native_id,
            .id = minted,
            .run_id = run.id,
            .name = stringMember(payload.object, "name"),
            .args = payload.object.get("args") orelse std.json.Value{ .null = {} },
        });
        const at = self.tools.items.len - 1;
        const requested = try self.toolPayload(&self.tools.items[at], true);
        const request_id = try self.emit(run, "action.call.requested", .{ .object = requested });
        const started = try self.toolPayload(&self.tools.items[at], false);
        const start_id = try self.emitEnvelope(run, "action.call.started", .{ .object = started }, request_id);
        self.tools.items[at].requested = request_id;
        self.tools.items[at].started = start_id;
    }

    fn endTool(self: *Reducer, payload: std.json.Value) !void {
        const run = self.active() orelse return;
        if (!run.started) return;
        if (payload != .object) {
            try self.failRun("hermes_invalid_event", "invalid tool complete");
            return;
        }
        const native_id = stringMember(payload.object, "tool_id");
        const at = self.findTool(run.id, native_id) orelse {
            try self.failRun("hermes_tool_lifecycle", "unmatched tool completion");
            return;
        };
        if (self.tools.items[at].terminal) {
            try self.failRun("hermes_tool_lifecycle", "unmatched tool completion");
            return;
        }
        self.tools.items[at].terminal = true;
        var body = try self.toolPayload(&self.tools.items[at], false);
        try self.put(&body, "result", payload.object.get("result") orelse std.json.Value{ .null = {} });
        _ = try self.emitEnvelope(run, "action.call.completed", .{ .object = body }, self.tools.items[at].started);
    }

    pub fn transportFailed(self: *Reducer, detail: []const u8) !void {
        self.unusable = true;
        const run = self.active() orelse return;
        if (!run.started) {
            run.terminal = true;
            self.buffered.clearRetainingCapacity();
            return;
        }
        var failure = self.object();
        try self.put(&failure, "code", str("hermes_process_exit"));
        try self.put(&failure, "message", str(detail));
        var body = self.object();
        try self.put(&body, "session_id", str(self.options.session_id));
        try self.put(&body, "run_id", str(run.id));
        try self.put(&body, "error", .{ .object = failure });
        try self.put(&body, "settled_by", str("inferred"));
        _ = try self.emit(run, "run.failed", .{ .object = body });
        run.terminal = true;
    }

    fn emitDelta(self: *Reducer, payload: std.json.Value, part_kind: []const u8) !void {
        const run = self.active() orelse return;
        if (!run.started) return;
        if (payload != .object) {
            try self.failRun("hermes_invalid_event", "invalid delta");
            return;
        }
        const text = stringMember(payload.object, "text");

        var part = self.object();
        try self.put(&part, "type", str(part_kind));
        try self.put(&part, part_kind, str(text));
        var body = self.object();
        try self.put(&body, "session_id", str(self.options.session_id));
        try self.put(&body, "run_id", str(run.id));
        try self.put(&body, "message_id", str(run.message_id));
        try self.put(&body, "part", .{ .object = part });
        _ = try self.emit(run, "content.delta", .{ .object = body });
    }

    fn settleRun(self: *Reducer, payload: std.json.Value) !void {
        const run = self.active() orelse return;
        if (!run.started) return;
        if (payload != .object) {
            try self.failRun("hermes_invalid_event", "invalid settlement");
            return;
        }
        const status = stringMember(payload.object, "status");
        if (status.len == 0) {
            try self.failRun("hermes_invalid_settlement", "child-mirror settlement on the parent stream");
            return;
        }
        if (std.mem.eql(u8, status, "complete")) {
            var response = self.object();
            try self.put(&response, "id", str(run.message_id));
            try self.put(&response, "role", str("assistant"));
            try self.put(&response, "content", str(stringMember(payload.object, "text")));

            var body = self.object();
            try self.put(&body, "session_id", str(self.options.session_id));
            try self.put(&body, "run_id", str(run.id));
            try self.put(&body, "final_response", .{ .object = response });
            try self.put(&body, "stop_reason", str("completed"));
            var totals = self.object();
            if (payload.object.get("usage")) |usage| {
                if (usage == .object) {
                    const input = integerMember(usage.object, "input");
                    const output = integerMember(usage.object, "output");
                    const total = integerMember(usage.object, "total");
                    if (input != 0) try self.put(&totals, "input_tokens", int(input));
                    if (output != 0) try self.put(&totals, "output_tokens", int(output));
                    if (total != 0) try self.put(&totals, "total_tokens", int(total));
                }
            }
            try self.put(&body, "usage", .{ .object = totals });
            _ = try self.emit(run, "run.completed", .{ .object = body });
            run.terminal = true;
            return;
        }
        var code = try std.fmt.allocPrint(self.allocator(), "hermes_{s}", .{status});
        if (payload.object.get("error_surface")) |surface| {
            if (surface == .object) {
                const surfaced = stringMember(surface.object, "code");
                if (surfaced.len > 0) code = try std.fmt.allocPrint(self.allocator(), "hermes_{s}", .{surfaced});
            }
        }
        var message = stringMember(payload.object, "error");
        if (message.len == 0) message = stringMember(payload.object, "text");
        if (message.len == 0) message = try std.fmt.allocPrint(self.allocator(), "turn {s}", .{status});
        try self.failRun(code, message);
    }

    fn findInteraction(self: *Reducer, id: []const u8) ?usize {
        for (self.interactions.items, 0..) |binding, index| {
            if (std.mem.eql(u8, binding.id, id)) return index;
        }
        return null;
    }

    pub fn pendingInteraction(self: *Reducer, kind: []const u8) ?*const Interaction {
        const run = self.active() orelse return null;
        for (self.interactions.items) |*binding| {
            if (binding.resolved) continue;
            if (!std.mem.eql(u8, binding.run_id, run.id)) continue;
            if (!std.mem.eql(u8, binding.kind, kind)) continue;
            return binding;
        }
        return null;
    }

    fn optionsFrom(self: *Reducer, choices: std.json.Array) ![]const Option {
        var list = std.ArrayList(Option).empty;
        for (choices.items) |entry| {
            try list.append(self.allocator(), .{ .id = entry.string, .label = entry.string });
        }
        return list.items;
    }

    fn questionFrom(self: *Reducer, id: []const u8, prompt: []const u8, choices: ?std.json.Value, multi: bool) !Question {
        const offered: ?std.json.Array = if (choices) |value| (if (value == .array) value.array else null) else null;
        if (offered == null or offered.?.items.len == 0) {
            return .{ .id = id, .prompt = prompt, .kind = "text" };
        }
        return .{
            .id = id,
            .prompt = prompt,
            .kind = if (multi) "multi_choice" else "single_choice",
            .options = try self.optionsFrom(offered.?),
        };
    }

    fn questionsValue(self: *Reducer, questions: []const Question) !std.json.Value {
        var list = std.json.Array.init(self.allocator());
        for (questions) |question| {
            var body = self.object();
            try self.put(&body, "id", str(question.id));
            try self.put(&body, "prompt", str(question.prompt));
            try self.put(&body, "kind", str(question.kind));
            try self.put(&body, "required", .{ .bool = true });
            if (question.options.len > 0) {
                var options = std.json.Array.init(self.allocator());
                for (question.options) |option| {
                    var entry = self.object();
                    try self.put(&entry, "id", str(option.id));
                    try self.put(&entry, "label", str(option.label));
                    try options.append(.{ .object = entry });
                }
                try self.put(&body, "options", .{ .array = options });
            }
            try list.append(.{ .object = body });
        }
        return .{ .array = list };
    }

    fn answersValue(self: *Reducer, answers: []const Answer) !std.json.Value {
        var list = std.json.Array.init(self.allocator());
        for (answers) |answer| {
            var body = self.object();
            try self.put(&body, "question_id", str(answer.question_id));
            if (answer.text.len > 0) try self.put(&body, "text", str(answer.text));
            if (answer.selected_option_ids.len > 0) {
                var ids = std.json.Array.init(self.allocator());
                for (answer.selected_option_ids) |selected| try ids.append(str(selected));
                try self.put(&body, "selected_option_ids", .{ .array = ids });
            }
            try list.append(.{ .object = body });
        }
        return .{ .array = list };
    }

    fn emitStatus(self: *Reducer, run: *Run, status: []const u8, pending: []const u8) !void {
        var body = self.object();
        try self.put(&body, "session_id", str(self.options.session_id));
        try self.put(&body, "run_id", str(run.id));
        try self.put(&body, "status", str(status));
        if (pending.len > 0) try self.put(&body, "pending_user_input_id", str(pending));
        try self.put(&body, "updated_at_ms", int(self.now()));
        _ = try self.emit(run, "run.status.updated", .{ .object = body });
    }

    fn resolvedPayload(self: *Reducer, binding: Interaction, run: *Run, status: []const u8, answers: ?[]const Answer) !std.json.Value {
        var body = self.object();
        try self.put(&body, "interaction_id", str(binding.id));
        try self.put(&body, "requested_by", str(self.options.endpoint));
        try self.put(&body, "responded_by", str(self.options.responder));
        try self.put(&body, "session_id", str(self.options.session_id));
        try self.put(&body, "run_id", str(run.id));
        try self.put(&body, "status", str(status));
        if (answers) |carried| try self.put(&body, "answers", try self.answersValue(carried));
        return .{ .object = body };
    }

    fn openInteraction(self: *Reducer, kind: []const u8, payload: std.json.Value) !void {
        const run = self.active() orelse return;
        if (!run.started) return;
        const id = try self.nextID("interaction");
        var binding = Interaction{ .id = id, .run_id = run.id, .kind = "" };
        var title: []const u8 = "";
        var description: []const u8 = "";
        var questions = std.ArrayList(Question).empty;

        if (std.mem.eql(u8, kind, "approval.request")) {
            const members = [_]Member{
                .{ .name = "command", .kind = .string },
                .{ .name = "pattern_key", .kind = .string },
                .{ .name = "pattern_keys", .kind = .strings },
                .{ .name = "description", .kind = .string },
                .{ .name = "allow_permanent", .kind = .boolean },
                .{ .name = "allow_session", .kind = .boolean },
                .{ .name = "smart_denied", .kind = .boolean },
                .{ .name = "choices", .kind = .strings },
            };
            const fields = strictObject(payload, &members) orelse {
                try self.failRun("hermes_invalid_event", "invalid approval gate");
                return;
            };
            const command = stringMember(fields, "command");
            binding.kind = "approval";
            title = "Command approval";
            description = command;
            var options: []const Option = &.{};
            if (fields.get("choices")) |choices| {
                if (choices == .array) options = try self.optionsFrom(choices.array);
            }
            try questions.append(self.allocator(), .{ .id = "choice", .prompt = command, .kind = "single_choice", .options = options });
        } else if (std.mem.eql(u8, kind, "clarify.request")) {
            const members = [_]Member{
                .{ .name = "request_id", .kind = .string },
                .{ .name = "question", .kind = .string },
                .{ .name = "choices", .kind = .strings },
                .{ .name = "multi_select", .kind = .boolean },
                .{ .name = "questions", .kind = .objects },
            };
            const fields = strictObject(payload, &members) orelse {
                try self.failRun("hermes_invalid_event", "invalid clarify gate");
                return;
            };
            binding.kind = "clarify";
            binding.request_id = stringMember(fields, "request_id");
            title = "Clarification";
            const batch = batched(fields);
            if (batch) |entries| {
                const question_members = [_]Member{
                    .{ .name = "qid", .kind = .string },
                    .{ .name = "question", .kind = .string },
                    .{ .name = "choices", .kind = .strings },
                    .{ .name = "multi_select", .kind = .boolean },
                };
                for (entries.items) |entry| {
                    const carried = strictObject(entry, &question_members) orelse {
                        try self.failRun("hermes_invalid_event", "invalid clarify gate");
                        return;
                    };
                    try questions.append(self.allocator(), try self.questionFrom(stringMember(carried, "qid"), stringMember(carried, "question"), carried.get("choices"), boolMember(carried, "multi_select")));
                }
            } else {
                try questions.append(self.allocator(), try self.questionFrom("answer", stringMember(fields, "question"), fields.get("choices"), boolMember(fields, "multi_select")));
            }
        } else if (std.mem.eql(u8, kind, "sudo.request")) {
            binding.kind = "sudo";
            binding.request_id = requestIDProbe(payload);
            title = "Password required";
            try questions.append(self.allocator(), .{ .id = "password", .prompt = "Enter the sudo password", .kind = "text" });
        } else {
            const members = [_]Member{
                .{ .name = "request_id", .kind = .string },
                .{ .name = "prompt", .kind = .string },
                .{ .name = "env_var", .kind = .string },
                .{ .name = "metadata", .kind = .any },
            };
            const fields = strictObject(payload, &members) orelse {
                try self.failRun("hermes_invalid_event", "invalid secret gate");
                return;
            };
            binding.kind = "secret";
            binding.request_id = stringMember(fields, "request_id");
            title = "Secret required";
            description = stringMember(fields, "env_var");
            try questions.append(self.allocator(), .{ .id = "value", .prompt = stringMember(fields, "prompt"), .kind = "text" });
        }

        if (binding.request_id.len == 0 and !std.mem.eql(u8, binding.kind, "approval")) {
            try self.failRun("hermes_invalid_event", "gate without request_id");
            return;
        }
        if (!answerableSet(questions.items)) {
            try self.failRun("hermes_invalid_event", "gate with a question nobody can answer");
            return;
        }
        binding.questions = questions.items;
        try self.interactions.append(self.allocator(), binding);
        const at = self.interactions.items.len - 1;

        var requested = self.object();
        try self.put(&requested, "interaction_id", str(id));
        try self.put(&requested, "requested_by", str(self.options.endpoint));
        try self.put(&requested, "responded_by", str(self.options.responder));
        try self.put(&requested, "session_id", str(self.options.session_id));
        try self.put(&requested, "run_id", str(run.id));
        try self.put(&requested, "title", str(title));
        if (description.len > 0) try self.put(&requested, "description", str(description));
        try self.put(&requested, "questions", try self.questionsValue(questions.items));
        try self.put(&requested, "allow_cancel", .{ .bool = true });
        self.interactions.items[at].requested = try self.emit(run, "user.input.requested", .{ .object = requested });
        try self.emitStatus(run, "waiting_for_input", id);
    }

    fn expireInteraction(self: *Reducer, payload: std.json.Value) !void {
        const run = self.active() orelse return;
        const members = [_]Member{.{ .name = "request_id", .kind = .string }};
        const fields = strictObject(payload, &members) orelse {
            try self.failRun("hermes_invalid_event", "invalid expire");
            return;
        };
        const request_id = stringMember(fields, "request_id");
        if (request_id.len == 0) {
            try self.failRun("hermes_invalid_event", "expire without request_id");
            return;
        }
        for (self.interactions.items, 0..) |binding, at| {
            if (binding.resolved) continue;
            if (!std.mem.eql(u8, binding.run_id, run.id)) continue;
            if (!std.mem.eql(u8, binding.request_id, request_id)) continue;
            self.interactions.items[at].resolved = true;
            const body = try self.resolvedPayload(binding, run, "cancelled", null);
            _ = try self.emitEnvelope(run, "user.input.resolved", body, binding.requested);
            try self.emitStatus(run, "running", "");
            return;
        }
    }

    pub fn resolve(self: *Reducer, interaction_id: []const u8, answers: []const Answer) !void {
        if (self.unusable) return Error.SessionUnusable;
        const at = self.findInteraction(interaction_id) orelse return Error.InteractionNotFound;
        if (self.interactions.items[at].resolved) return Error.InteractionNotFound;
        const run = self.active() orelse return Error.InteractionNotFound;
        const binding = self.interactions.items[at];
        if (!std.mem.eql(u8, binding.run_id, run.id)) return Error.InteractionNotFound;
        if (answers.len != binding.questions.len) return Error.InvalidResolution;
        for (answers, 0..) |answer, index| {
            const question = questionNamed(binding.questions, answer.question_id) orelse return Error.InvalidResolution;
            if (!validAnswer(question, answer)) return Error.InvalidResolution;
            for (answers[0..index]) |earlier| {
                if (std.mem.eql(u8, earlier.question_id, answer.question_id)) return Error.InvalidResolution;
            }
        }
        self.interactions.items[at].resolved = true;
        const body = try self.resolvedPayload(binding, run, "submitted", answers);
        _ = try self.emitEnvelope(run, "user.input.resolved", body, binding.requested);
        try self.emitStatus(run, "running", "");
    }

    fn runScoped(kind: []const u8) bool {
        const scoped = [_][]const u8{
            "message.start",    "message.delta",  "reasoning.delta", "thinking.delta",
            "message.complete", "tool.start",     "tool.complete",   "approval.request",
            "clarify.request",  "sudo.request",   "secret.request",  "secret.expire",
            "sudo.expire",      "clarify.expire",
        };
        for (scoped) |name| {
            if (std.mem.eql(u8, name, kind)) return true;
        }
        return false;
    }

    fn disown(self: *Reducer, what: []const u8) !void {
        self.unusable = true;
        try self.failRun("hermes_external_activity", what);
    }

    fn failRun(self: *Reducer, code: []const u8, message: []const u8) !void {
        const run = self.active() orelse return;
        if (!run.started) return;
        var failure = self.object();
        try self.put(&failure, "code", str(code));
        try self.put(&failure, "message", str(message));
        var body = self.object();
        try self.put(&body, "session_id", str(self.options.session_id));
        try self.put(&body, "run_id", str(run.id));
        try self.put(&body, "error", .{ .object = failure });
        _ = try self.emit(run, "run.failed", .{ .object = body });
        run.terminal = true;
    }
};

const MemberKind = enum { string, boolean, strings, objects, any };

const Member = struct {
    name: []const u8,
    kind: MemberKind,
};

fn memberSpec(members: []const Member, name: []const u8) ?Member {
    for (members) |member| {
        if (std.mem.eql(u8, member.name, name)) return member;
    }
    return null;
}

fn memberMatches(kind: MemberKind, value: std.json.Value) bool {
    if (value == .null) return true;
    return switch (kind) {
        .string => value == .string,
        .boolean => value == .bool,
        .strings => everyEntry(value, .string),
        .objects => everyEntry(value, .object),
        .any => true,
    };
}

fn everyEntry(value: std.json.Value, want: std.meta.Tag(std.json.Value)) bool {
    if (value != .array) return false;
    for (value.array.items) |entry| {
        if (entry != want) return false;
    }
    return true;
}

fn strictObject(payload: std.json.Value, members: []const Member) ?std.json.ObjectMap {
    if (payload != .object) return null;
    var entries = payload.object.iterator();
    while (entries.next()) |entry| {
        const spec = memberSpec(members, entry.key_ptr.*) orelse return null;
        if (!memberMatches(spec.kind, entry.value_ptr.*)) return null;
    }
    return payload.object;
}

fn batched(object: std.json.ObjectMap) ?std.json.Array {
    const value = object.get("questions") orelse return null;
    if (value != .array or value.array.items.len == 0) return null;
    return value.array;
}

fn requestIDProbe(payload: std.json.Value) []const u8 {
    if (payload != .object) return "";
    const value = payload.object.get("request_id") orelse return "";
    return if (value == .string) value.string else "";
}

fn boolMember(map: std.json.ObjectMap, key: []const u8) bool {
    const value = map.get(key) orelse return false;
    return if (value == .bool) value.bool else false;
}

fn answerableSet(questions: []const Question) bool {
    for (questions, 0..) |question, index| {
        if (!answerable(question)) return false;
        for (questions[0..index]) |earlier| {
            if (std.mem.eql(u8, earlier.id, question.id)) return false;
        }
    }
    return true;
}

fn answerable(question: Question) bool {
    if (question.id.len == 0 or question.prompt.len == 0) return false;
    for (question.options) |option| {
        if (option.id.len == 0 or option.label.len == 0) return false;
    }
    if (std.mem.eql(u8, question.kind, "text")) return true;
    return question.options.len > 0;
}

fn offers(question: Question, id: []const u8) bool {
    for (question.options) |option| {
        if (std.mem.eql(u8, option.id, id)) return true;
    }
    return false;
}

fn questionNamed(questions: []const Question, id: []const u8) ?Question {
    for (questions) |question| {
        if (std.mem.eql(u8, question.id, id)) return question;
    }
    return null;
}

fn validAnswer(question: Question, answer: Answer) bool {
    const has_text = answer.text.len > 0;
    if (has_text and answer.selected_option_ids.len > 0) return false;
    if (std.mem.eql(u8, question.kind, "text")) {
        if (!has_text) return false;
    } else if (std.mem.eql(u8, question.kind, "single_choice")) {
        if (answer.selected_option_ids.len != 1) return false;
    } else if (std.mem.eql(u8, question.kind, "multi_choice")) {
        if (answer.selected_option_ids.len == 0) return false;
    } else return false;
    for (answer.selected_option_ids, 0..) |selected, index| {
        if (selected.len == 0 or !offers(question, selected)) return false;
        for (answer.selected_option_ids[0..index]) |earlier| {
            if (std.mem.eql(u8, earlier, selected)) return false;
        }
    }
    return true;
}

fn idScalar(index: usize) u21 {
    return std.math.cast(u21, 'a' + index) orelse 0xFFFD;
}

fn stringMember(map: std.json.ObjectMap, key: []const u8) []const u8 {
    const value = map.get(key) orelse return "";
    return if (value == .string) value.string else "";
}

fn integerMember(map: std.json.ObjectMap, key: []const u8) i64 {
    const value = map.get(key) orelse return 0;
    return if (value == .integer) value.integer else 0;
}

const testing = std.testing;

fn feed(reducer: *Reducer, scratch: std.mem.Allocator, line: []const u8) !void {
    const message = try rpc.parseMessage(scratch, line);
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, line, .{});
    try reducer.observe(message, parsed);
}

fn event(scratch: std.mem.Allocator, seq: i64, kind: []const u8, payload: []const u8) ![]const u8 {
    return std.fmt.allocPrint(scratch, "{{\"jsonrpc\":\"2.0\",\"method\":\"event\",\"params\":{{\"type\":\"{s}\",\"session_id\":\"sess0001\",\"seq\":{d},\"payload\":{s}}}}}", .{ kind, seq, payload });
}

fn bare(scratch: std.mem.Allocator, seq: i64, kind: []const u8) ![]const u8 {
    return std.fmt.allocPrint(scratch, "{{\"jsonrpc\":\"2.0\",\"method\":\"event\",\"params\":{{\"type\":\"{s}\",\"session_id\":\"sess0001\",\"seq\":{d}}}}}", .{ kind, seq });
}

fn feedEvent(reducer: *Reducer, scratch: std.mem.Allocator, kind: []const u8, payload: []const u8) !void {
    try feed(reducer, scratch, try event(scratch, reducer.last_seq + 1, kind, payload));
}

fn feedBare(reducer: *Reducer, scratch: std.mem.Allocator, kind: []const u8) !void {
    try feed(reducer, scratch, try bare(scratch, reducer.last_seq + 1, kind));
}

fn openRun(arena: *std.heap.ArenaAllocator) !Reducer {
    var reducer = Reducer.init(arena, .{});
    reducer.open();
    try reducer.submit();
    try feedBare(&reducer, arena.allocator(), "message.start");
    return reducer;
}

fn gate(arena: *std.heap.ArenaAllocator, kind: []const u8, payload: []const u8) !Reducer {
    var reducer = try openRun(arena);
    try feedEvent(&reducer, arena.allocator(), kind, payload);
    return reducer;
}

fn typeAt(reducer: *Reducer, index: usize) []const u8 {
    return reducer.envelopes.items[index].object.get("type").?.string;
}

fn payloadAt(reducer: *Reducer, index: usize) std.json.ObjectMap {
    return reducer.envelopes.items[index].object.get("payload").?.object;
}

fn questionsAt(reducer: *Reducer, index: usize) std.json.Array {
    return payloadAt(reducer, index).get("questions").?.array;
}

fn expectGateRefused(kind: []const u8, payload: []const u8, code: []const u8, message: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, kind, payload);

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 1));
    const failure = payloadAt(&reducer, 1).get("error").?.object;
    try testing.expectEqualStrings(code, failure.get("code").?.string);
    try testing.expectEqualStrings(message, failure.get("message").?.string);
}

test "an approval gate carries the command as both its description and its only prompt" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "approval.request",
        \\{"command":"rm -rf /tmp/x","choices":["once","deny"]}
    );

    try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);
    try testing.expectEqualStrings("user.input.requested", typeAt(&reducer, 1));
    const requested = payloadAt(&reducer, 1);
    try testing.expectEqualStrings("Command approval", requested.get("title").?.string);
    try testing.expectEqualStrings("rm -rf /tmp/x", requested.get("description").?.string);
    try testing.expect(requested.get("allow_cancel").?.bool);
    try testing.expectEqualStrings(endpoint_id, requested.get("requested_by").?.string);
    try testing.expectEqualStrings("user", requested.get("responded_by").?.string);

    const questions = questionsAt(&reducer, 1);
    try testing.expectEqual(@as(usize, 1), questions.items.len);
    const question = questions.items[0].object;
    try testing.expectEqualStrings("choice", question.get("id").?.string);
    try testing.expectEqualStrings("rm -rf /tmp/x", question.get("prompt").?.string);
    try testing.expectEqualStrings("single_choice", question.get("kind").?.string);
    try testing.expect(question.get("required").?.bool);
    const options = question.get("options").?.array;
    try testing.expectEqual(@as(usize, 2), options.items.len);
    try testing.expectEqualStrings("once", options.items[0].object.get("id").?.string);
    try testing.expectEqualStrings("once", options.items[0].object.get("label").?.string);
    try testing.expectEqualStrings("deny", options.items[1].object.get("id").?.string);
}

test "a gate announces itself and then parks the run on the interaction it opened" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "approval.request",
        \\{"command":"ls","choices":["once"]}
    );

    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try testing.expectEqualStrings("interaction-d", interaction);
    try testing.expectEqualStrings(interaction, reducer.envelopes.items[1].object.get("turn_id").?.string);
    try testing.expectEqualStrings("run.status.updated", typeAt(&reducer, 2));
    const status = payloadAt(&reducer, 2);
    try testing.expectEqualStrings("waiting_for_input", status.get("status").?.string);
    try testing.expectEqualStrings(interaction, status.get("pending_user_input_id").?.string);
    try testing.expectEqual(@as(i64, 5), status.get("updated_at_ms").?.integer);
    try testing.expectEqual(@as(i64, 6), reducer.envelopes.items[2].object.get("timestamp_ms").?.integer);
}

test "a clarify gate offers the choices it carries and falls back to free text without them" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var offered = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a","b"]}
    );
    const asked = questionsAt(&offered, 1).items[0].object;
    try testing.expectEqualStrings("Clarification", payloadAt(&offered, 1).get("title").?.string);
    try testing.expect(payloadAt(&offered, 1).get("description") == null);
    try testing.expectEqualStrings("answer", asked.get("id").?.string);
    try testing.expectEqualStrings("which?", asked.get("prompt").?.string);
    try testing.expectEqualStrings("single_choice", asked.get("kind").?.string);

    var free = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?"}
    );
    const open_ended = questionsAt(&free, 1).items[0].object;
    try testing.expectEqualStrings("text", open_ended.get("kind").?.string);
    try testing.expect(open_ended.get("options") == null);
}

test "an empty choice list makes a question free text even when it asks for several answers" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":[],"multi_select":true}
    );

    try testing.expectEqualStrings("text", questionsAt(&reducer, 1).items[0].object.get("kind").?.string);
}

test "a batch clarify gate mints one question per entry and reads each entry's own multi_select" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"one","question":"first?","choices":["a"]},{"qid":"two","question":"second?","choices":["b","c"],"multi_select":true}]}
    );

    const questions = questionsAt(&reducer, 1);
    try testing.expectEqual(@as(usize, 2), questions.items.len);
    try testing.expectEqualStrings("one", questions.items[0].object.get("id").?.string);
    try testing.expectEqualStrings("first?", questions.items[0].object.get("prompt").?.string);
    try testing.expectEqualStrings("single_choice", questions.items[0].object.get("kind").?.string);
    try testing.expectEqualStrings("two", questions.items[1].object.get("id").?.string);
    try testing.expectEqualStrings("multi_choice", questions.items[1].object.get("kind").?.string);
}

test "a batch entry wins over the single form even when both are present" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"ignored","choices":["z"],"questions":[{"qid":"one","question":"first?","choices":["a"]}]}
    );

    const questions = questionsAt(&reducer, 1);
    try testing.expectEqual(@as(usize, 1), questions.items.len);
    try testing.expectEqualStrings("one", questions.items[0].object.get("id").?.string);
}

test "the sudo gate asks for a password and the secret gate names the variable it fills" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var sudo = try gate(&arena, "sudo.request",
        \\{"request_id":"bbbb2222"}
    );
    const password = questionsAt(&sudo, 1).items[0].object;
    try testing.expectEqualStrings("Password required", payloadAt(&sudo, 1).get("title").?.string);
    try testing.expect(payloadAt(&sudo, 1).get("description") == null);
    try testing.expectEqualStrings("password", password.get("id").?.string);
    try testing.expectEqualStrings("Enter the sudo password", password.get("prompt").?.string);
    try testing.expectEqualStrings("text", password.get("kind").?.string);

    var secret = try gate(&arena, "secret.request",
        \\{"request_id":"cccc3333","prompt":"CI token","env_var":"CI_TOKEN"}
    );
    const value = questionsAt(&secret, 1).items[0].object;
    try testing.expectEqualStrings("Secret required", payloadAt(&secret, 1).get("title").?.string);
    try testing.expectEqualStrings("CI_TOKEN", payloadAt(&secret, 1).get("description").?.string);
    try testing.expectEqualStrings("value", value.get("id").?.string);
    try testing.expectEqualStrings("CI token", value.get("prompt").?.string);
}

test "only an approval gate may arrive without a request id" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var approval = try gate(&arena, "approval.request",
        \\{"command":"ls","choices":["once"]}
    );
    try testing.expectEqualStrings("user.input.requested", typeAt(&approval, 1));

    try expectGateRefused("clarify.request",
        \\{"question":"which?","choices":["a"]}
    , "hermes_invalid_event", "gate without request_id");
    try expectGateRefused("sudo.request", "{}", "hermes_invalid_event", "gate without request_id");
    try expectGateRefused("secret.request",
        \\{"prompt":"CI token","env_var":"CI_TOKEN"}
    , "hermes_invalid_event", "gate without request_id");
}

test "a gate payload is refused member by member, and each kind names itself when it is" {
    try expectGateRefused("approval.request",
        \\{"command":"ls","choices":["once"],"unknown":1}
    , "hermes_invalid_event", "invalid approval gate");
    try expectGateRefused("approval.request",
        \\{"command":7,"choices":["once"]}
    , "hermes_invalid_event", "invalid approval gate");
    try expectGateRefused("approval.request",
        \\{"command":"ls","choices":[7]}
    , "hermes_invalid_event", "invalid approval gate");
    try expectGateRefused("approval.request",
        \\{"command":"ls","choices":["once"],"allow_session":"yes"}
    , "hermes_invalid_event", "invalid approval gate");
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","multi_select":1}
    , "hermes_invalid_event", "invalid clarify gate");
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"one","question":"first?","choices":["a"],"unknown":1}]}
    , "hermes_invalid_event", "invalid clarify gate");
    try expectGateRefused("secret.request",
        \\{"request_id":"cccc3333","prompt":"CI token","env_var":"CI_TOKEN","unknown":1}
    , "hermes_invalid_event", "invalid secret gate");
}

test "a null member is absent to the decoder, as it is to encoding/json" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "approval.request",
        \\{"command":"ls","choices":["once"],"description":null,"pattern_keys":null,"smart_denied":null}
    );

    try testing.expectEqualStrings("user.input.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("ls", payloadAt(&reducer, 1).get("description").?.string);
}

test "the sudo gate reads its request id leniently where the others decode it strictly" {
    try expectGateRefused("sudo.request",
        \\{"request_id":7,"extra":true}
    , "hermes_invalid_event", "gate without request_id");
    try expectGateRefused("secret.request",
        \\{"request_id":7,"prompt":"CI token","env_var":"CI_TOKEN"}
    , "hermes_invalid_event", "invalid secret gate");
}

test "a gate that arrives before the run started emits nothing and mints nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();
    try reducer.submit();
    try feedEvent(&reducer, arena.allocator(), "approval.request",
        \\{"command":"ls","choices":["once"]}
    );

    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try testing.expectEqual(@as(usize, 0), reducer.interactions.items.len);
    try testing.expectEqual(@as(usize, 1), reducer.ids);
}

test "an expire settles the gate carrying its request id and leaves every other one open" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"dddd4444","question":"which?","choices":["a","b"]}
    );
    try feedEvent(&reducer, scratch, "secret.request",
        \\{"request_id":"eeee5555","prompt":"CI token","env_var":"CI_TOKEN"}
    );
    try feedEvent(&reducer, scratch, "clarify.expire",
        \\{"request_id":"dddd4444"}
    );

    try testing.expectEqual(@as(usize, 7), reducer.envelopes.items.len);
    try testing.expectEqualStrings("user.input.resolved", typeAt(&reducer, 5));
    const resolved = payloadAt(&reducer, 5);
    try testing.expectEqualStrings("cancelled", resolved.get("status").?.string);
    try testing.expect(resolved.get("answers") == null);
    try testing.expectEqualStrings(payloadAt(&reducer, 1).get("interaction_id").?.string, resolved.get("interaction_id").?.string);
    try testing.expectEqualStrings(reducer.envelopes.items[1].object.get("id").?.string, reducer.envelopes.items[5].object.get("in_reply_to").?.string);
    try testing.expectEqualStrings("run.status.updated", typeAt(&reducer, 6));
    try testing.expectEqualStrings("running", payloadAt(&reducer, 6).get("status").?.string);
    try testing.expect(payloadAt(&reducer, 6).get("pending_user_input_id") == null);
    try testing.expect(!reducer.interactions.items[1].resolved);

    try feedEvent(&reducer, scratch, "clarify.expire",
        \\{"request_id":"dddd4444"}
    );
    try testing.expectEqual(@as(usize, 7), reducer.envelopes.items.len);
}

test "an expire naming no open gate is silent, and a malformed one fails the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"dddd4444","question":"which?","choices":["a"]}
    );
    try feedEvent(&reducer, scratch, "clarify.expire",
        \\{"request_id":"ffff6666"}
    );
    try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);

    try feedEvent(&reducer, scratch, "clarify.expire",
        \\{"request_id":"dddd4444","unknown":1}
    );
    try testing.expectEqual(@as(usize, 4), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 3));
    try testing.expectEqualStrings("invalid expire", payloadAt(&reducer, 3).get("error").?.object.get("message").?.string);
}

test "a resolution echoes the answers it was given and returns the run to running" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "approval.request",
        \\{"command":"ls","choices":["once","deny"]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try reducer.resolve(interaction, &[_]Answer{.{ .question_id = "choice", .selected_option_ids = &[_][]const u8{"once"} }});

    try testing.expectEqual(@as(usize, 5), reducer.envelopes.items.len);
    try testing.expectEqualStrings("user.input.resolved", typeAt(&reducer, 3));
    const resolved = payloadAt(&reducer, 3);
    try testing.expectEqualStrings("submitted", resolved.get("status").?.string);
    try testing.expectEqualStrings(interaction, reducer.envelopes.items[3].object.get("turn_id").?.string);
    try testing.expectEqualStrings(reducer.envelopes.items[1].object.get("id").?.string, reducer.envelopes.items[3].object.get("in_reply_to").?.string);
    const answers = resolved.get("answers").?.array;
    try testing.expectEqual(@as(usize, 1), answers.items.len);
    try testing.expectEqualStrings("choice", answers.items[0].object.get("question_id").?.string);
    try testing.expect(answers.items[0].object.get("text") == null);
    try testing.expectEqualStrings("once", answers.items[0].object.get("selected_option_ids").?.array.items[0].string);
    try testing.expectEqualStrings("running", payloadAt(&reducer, 4).get("status").?.string);
}

test "a free-text answer carries text where a choice answer carries option ids" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "sudo.request",
        \\{"request_id":"bbbb2222"}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try reducer.resolve(interaction, &[_]Answer{.{ .question_id = "password", .text = "hunter2" }});

    const answer = payloadAt(&reducer, 3).get("answers").?.array.items[0].object;
    try testing.expectEqualStrings("hunter2", answer.get("text").?.string);
    try testing.expect(answer.get("selected_option_ids") == null);
}

test "a resolution is refused unless every answer suits the question it names" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "approval.request",
        \\{"command":"ls","choices":["once","deny"]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;

    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{}));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "other", .selected_option_ids = &[_][]const u8{"once"} }}));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "choice", .text = "once" }}));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "choice", .selected_option_ids = &[_][]const u8{"maybe"} }}));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "choice", .selected_option_ids = &[_][]const u8{ "once", "deny" } }}));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "choice", .text = "once", .selected_option_ids = &[_][]const u8{"once"} }}));
    try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);
}

test "a multi-choice answer takes several distinct options but never the same one twice" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a","b"],"multi_select":true}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;

    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{ "a", "a" } }}));
    try reducer.resolve(interaction, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{ "a", "b" } }});
    try testing.expectEqual(@as(usize, 2), payloadAt(&reducer, 3).get("answers").?.array.items[0].object.get("selected_option_ids").?.array.items.len);
}

test "a gate resolves once, and an expired one is already spoken for" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try reducer.resolve(interaction, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }});
    try testing.expectError(Error.InteractionNotFound, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }}));
    try testing.expectError(Error.InteractionNotFound, reducer.resolve("interaction-z", &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }}));

    try feedEvent(&reducer, scratch, "clarify.expire",
        \\{"request_id":"aaaa1111"}
    );
    try testing.expectEqual(@as(usize, 5), reducer.envelopes.items.len);
}

test "a settled run refuses a resolution instead of emitting past its terminal" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try feedEvent(&reducer, scratch, "message.complete",
        \\{"status":"complete","text":"done"}
    );

    try testing.expectEqualStrings("run.completed", typeAt(&reducer, 3));
    try testing.expectError(Error.InteractionNotFound, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }}));
    try testing.expectEqual(@as(usize, 4), reducer.envelopes.items.len);
}

test "the pending gate a driver looks up is the open one of that kind" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    try feedEvent(&reducer, scratch, "sudo.request",
        \\{"request_id":"bbbb2222"}
    );

    try testing.expect(reducer.pendingInteraction("secret") == null);
    try testing.expectEqualStrings("sudo", reducer.pendingInteraction("sudo").?.kind);
    const clarify = reducer.pendingInteraction("clarify").?.id;
    try reducer.resolve(clarify, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }});
    try testing.expect(reducer.pendingInteraction("clarify") == null);
}

test "a gate left open by one run is not answerable from the next" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    const stranded = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try feedEvent(&reducer, scratch, "message.complete",
        \\{"status":"complete","text":"done"}
    );
    try reducer.submit();
    try feedBare(&reducer, scratch, "message.start");

    try testing.expectEqualStrings("run.started", typeAt(&reducer, 4));
    try testing.expectError(Error.InteractionNotFound, reducer.resolve(stranded, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }}));
    try testing.expectEqual(@as(usize, 5), reducer.envelopes.items.len);
}

test "a batch resolution matches each answer to the question it names, in any order" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"one","question":"first?","choices":["a"]},{"qid":"two","question":"second?","choices":["b"]}]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try reducer.resolve(interaction, &[_]Answer{
        .{ .question_id = "two", .selected_option_ids = &[_][]const u8{"b"} },
        .{ .question_id = "one", .selected_option_ids = &[_][]const u8{"a"} },
    });

    const answered = payloadAt(&reducer, 3).get("answers").?.array;
    try testing.expectEqual(@as(usize, 2), answered.items.len);
    try testing.expectEqualStrings("two", answered.items[0].object.get("question_id").?.string);
    try testing.expectEqualStrings("one", answered.items[1].object.get("question_id").?.string);
}

test "a batch resolution answering one question twice leaves the other unanswered and is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"one","question":"first?","choices":["a"]},{"qid":"two","question":"second?","choices":["b"]}]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;

    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{
        .{ .question_id = "one", .selected_option_ids = &[_][]const u8{"a"} },
        .{ .question_id = "one", .selected_option_ids = &[_][]const u8{"a"} },
    }));
    try testing.expectError(Error.InvalidResolution, reducer.resolve(interaction, &[_]Answer{
        .{ .question_id = "one", .selected_option_ids = &[_][]const u8{"a"} },
        .{ .question_id = "three", .selected_option_ids = &[_][]const u8{"b"} },
    }));
    try testing.expectEqual(@as(usize, 3), reducer.envelopes.items.len);
}

test "a gate stranded by a settled run is not offered in place of the new run's own" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    const stranded = reducer.pendingInteraction("clarify").?.id;
    try feedEvent(&reducer, scratch, "message.complete",
        \\{"status":"complete","text":"done"}
    );
    try testing.expect(reducer.pendingInteraction("clarify") == null);

    try reducer.submit();
    try feedBare(&reducer, scratch, "message.start");
    try feedEvent(&reducer, scratch, "clarify.request",
        \\{"request_id":"bbbb2222","question":"again?","choices":["b"]}
    );

    const fresh = reducer.pendingInteraction("clarify").?;
    try testing.expect(!std.mem.eql(u8, stranded, fresh.id));
    try testing.expectEqualStrings("bbbb2222", fresh.request_id);
}

test "a gate whose question carries no id, no prompt or no choice is refused, not emitted" {
    try expectGateRefused("approval.request",
        \\{"command":"ls"}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("approval.request",
        \\{"command":"ls","choices":[]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("approval.request",
        \\{"command":"","choices":["once"]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","question":""}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"","question":"first?","choices":["a"]}]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"one","question":"","choices":["a"]}]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("secret.request",
        \\{"request_id":"cccc3333","prompt":"","env_var":"CI_TOKEN"}
    , "hermes_invalid_event", "gate with a question nobody can answer");
}

test "a clarify gate with an empty choice list is free text, not a choice with nothing to choose" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":[]}
    );

    try testing.expectEqualStrings("user.input.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("text", questionsAt(&reducer, 1).items[0].object.get("kind").?.string);
}

test "an expire that names no request cancels nothing, least of all an approval" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "approval.request",
        \\{"command":"ls","choices":["once"]}
    );
    try feedEvent(&reducer, scratch, "clarify.expire", "{}");

    try testing.expectEqual(@as(usize, 4), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 3));
    try testing.expectEqualStrings("expire without request_id", payloadAt(&reducer, 3).get("error").?.object.get("message").?.string);
    try testing.expect(!reducer.interactions.items[0].resolved);
}

test "an event for another gateway session is not this session's to reduce" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","method":"event","params":{"type":"message.complete","session_id":"other","seq":2,"payload":{"status":"complete","text":"done"}}}
    );

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 1));
    const failure = payloadAt(&reducer, 1).get("error").?.object;
    try testing.expectEqualStrings("hermes_external_activity", failure.get("code").?.string);
    try testing.expectEqualStrings("event for foreign session \"other\"", failure.get("message").?.string);
}

test "an event that skips a place in the gateway's own sequence is not trusted" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch, try event(scratch, 5, "message.delta",
        \\{"text":"hi"}
    ));

    try testing.expectEqualStrings("run.failed", typeAt(&reducer, 1));
    const failure = payloadAt(&reducer, 1).get("error").?.object;
    try testing.expectEqualStrings("hermes_external_activity", failure.get("code").?.string);
    try testing.expectEqualStrings("non-contiguous seq 5 after 1", failure.get("message").?.string);
}

test "an event carrying no session at all is dropped rather than disowned" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","method":"event","params":{"type":"gateway.ready","payload":{}}}
    );

    try testing.expectEqual(@as(usize, 1), reducer.envelopes.items.len);
    try testing.expect(!reducer.unusable);
    try testing.expectEqual(@as(i64, 1), reducer.last_seq);
}

test "a session that has seen someone else's traffic takes no further instruction" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try gate(&arena, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    const interaction = payloadAt(&reducer, 1).get("interaction_id").?.string;
    try feed(&reducer, scratch,
        \\{"jsonrpc":"2.0","method":"event","params":{"type":"message.delta","session_id":"other","seq":3,"payload":{"text":"hi"}}}
    );

    try testing.expect(reducer.unusable);
    try testing.expectError(Error.SessionUnusable, reducer.submit());
    try testing.expectError(Error.SessionUnusable, reducer.resolve(interaction, &[_]Answer{.{ .question_id = "answer", .selected_option_ids = &[_][]const u8{"a"} }}));
}

test "a choice that is the empty string is no choice at all" {
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":[""]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
    try expectGateRefused("approval.request",
        \\{"command":"ls","choices":["once",""]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
}

test "a batch that asks the same question twice can never be answered, so it is refused" {
    try expectGateRefused("clarify.request",
        \\{"request_id":"aaaa1111","questions":[{"qid":"one","question":"first?","choices":["a"]},{"qid":"one","question":"second?","choices":["b"]}]}
    , "hermes_invalid_event", "gate with a question nobody can answer");
}

test "an event that arrives before the turn opens is kept and replayed, not dropped" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();
    try reducer.submit();

    try feedEvent(&reducer, scratch, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try testing.expectEqual(@as(usize, 1), reducer.buffered.items.len);

    try feedBare(&reducer, scratch, "message.start");

    try testing.expectEqualStrings("run.started", typeAt(&reducer, 0));
    try testing.expectEqualStrings("user.input.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("run.status.updated", typeAt(&reducer, 2));
    try testing.expectEqual(@as(usize, 0), reducer.buffered.items.len);
    try testing.expect(reducer.pendingInteraction("clarify") != null);
}

test "a run-scoped event with no run reserved is someone else's, and the rest are noise" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var stray = Reducer.init(&arena, .{});
    stray.open();
    try feedEvent(&stray, scratch, "message.delta",
        \\{"text":"x"}
    );
    try testing.expect(stray.unusable);
    try testing.expectEqual(@as(usize, 0), stray.envelopes.items.len);
    try testing.expectError(Error.SessionUnusable, stray.submit());

    var quiet = Reducer.init(&arena, .{});
    quiet.open();
    try feedEvent(&quiet, scratch, "gateway.ready",
        \\{"replay_epoch":"e3"}
    );
    try testing.expect(!quiet.unusable);
    try quiet.submit();
}

test "traffic the pinned protocol never carries disowns the session" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reverse = try openRun(&arena);
    try feed(&reverse, scratch,
        \\{"jsonrpc":"2.0","id":7,"method":"gateway.ask","params":{}}
    );
    try testing.expect(reverse.unusable);
    try testing.expectEqualStrings("reverse request", payloadAt(&reverse, 1).get("error").?.object.get("message").?.string);

    var stranger = try openRun(&arena);
    try feed(&stranger, scratch,
        \\{"jsonrpc":"2.0","method":"gateway.notice","params":{}}
    );
    try testing.expect(stranger.unusable);
    try testing.expectEqualStrings("non-event notification", payloadAt(&stranger, 1).get("error").?.object.get("message").?.string);

    var reply = try openRun(&arena);
    try feed(&reply, scratch,
        \\{"jsonrpc":"2.0","id":2,"result":{"status":"streaming"}}
    );
    try testing.expect(!reply.unusable);
    try testing.expectEqual(@as(usize, 1), reply.envelopes.items.len);
}

test "a completed run always reports usage, and reports only the totals it has" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var none = try openRun(&arena);
    try feedEvent(&none, scratch, "message.complete",
        \\{"status":"complete","text":"done"}
    );
    const empty = payloadAt(&none, 1).get("usage").?.object;
    try testing.expectEqual(@as(usize, 0), empty.count());

    var partial = try openRun(&arena);
    try feedEvent(&partial, scratch, "message.complete",
        \\{"status":"complete","text":"done","usage":{"input":3,"output":0,"total":3}}
    );
    const totals = payloadAt(&partial, 1).get("usage").?.object;
    try testing.expectEqual(@as(i64, 3), totals.get("input_tokens").?.integer);
    try testing.expect(totals.get("output_tokens") == null);
    try testing.expectEqual(@as(i64, 3), totals.get("total_tokens").?.integer);
}

test "a run-scoped event after the run settled belongs to no run, and says so" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feedEvent(&reducer, scratch, "message.complete",
        \\{"status":"complete","text":"done"}
    );
    try testing.expectEqualStrings("run.completed", typeAt(&reducer, 1));

    try feedEvent(&reducer, scratch, "message.delta",
        \\{"text":"late"}
    );

    try testing.expectEqual(@as(usize, 2), reducer.envelopes.items.len);
    try testing.expect(reducer.unusable);
    try testing.expectError(Error.SessionUnusable, reducer.submit());
}

test "a settled run still tolerates traffic that belongs to no turn" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = try openRun(&arena);
    try feedEvent(&reducer, scratch, "message.complete",
        \\{"status":"complete","text":"done"}
    );
    try feedEvent(&reducer, scratch, "gateway.ready",
        \\{"replay_epoch":"e3"}
    );

    try testing.expect(!reducer.unusable);
    try reducer.submit();
}

test "a transport that dies takes the session with it, started run or not" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var started = try openRun(&arena);
    try started.transportFailed("gateway exited");
    try testing.expectEqualStrings("run.failed", typeAt(&started, 1));
    try testing.expectEqualStrings("inferred", payloadAt(&started, 1).get("settled_by").?.string);
    try testing.expect(started.unusable);
    try testing.expectError(Error.SessionUnusable, started.submit());

    var reserved = Reducer.init(&arena, .{});
    reserved.open();
    try reserved.submit();
    try feedEvent(&reserved, scratch, "clarify.request",
        \\{"request_id":"aaaa1111","question":"which?","choices":["a"]}
    );
    try testing.expectEqual(@as(usize, 1), reserved.buffered.items.len);

    try reserved.transportFailed("gateway exited");
    try testing.expectEqual(@as(usize, 0), reserved.envelopes.items.len);
    try testing.expectEqual(@as(usize, 0), reserved.buffered.items.len);
    try testing.expect(reserved.unusable);
    try testing.expect(reserved.active() == null);

    var idle = Reducer.init(&arena, .{});
    idle.open();
    try idle.transportFailed("gateway exited");
    try testing.expect(idle.unusable);
}

test "an injected run id replaces the minted one without skipping it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const approval =
        \\{"command":"rm -rf /tmp/x","choices":["once","deny"]}
    ;

    var minted = try openRun(&arena);
    try feedEvent(&minted, scratch, "approval.request", approval);

    var injected = Reducer.init(&arena, .{});
    injected.open();
    try injected.submitAs(.{ .run_id = "run-supplied" });
    try feedBare(&injected, scratch, "message.start");
    try feedEvent(&injected, scratch, "approval.request", approval);

    try testing.expectEqualStrings("run-supplied", payloadAt(&injected, 0).get("run_id").?.string);
    try testing.expect(!std.mem.eql(u8, "run-supplied", payloadAt(&minted, 0).get("run_id").?.string));

    try testing.expectEqualStrings("user.input.requested", typeAt(&injected, 1));
    try testing.expectEqualStrings(
        payloadAt(&minted, 1).get("interaction_id").?.string,
        payloadAt(&injected, 1).get("interaction_id").?.string,
    );
}

test "the endpoint and revision the options name are what the envelopes carry" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = Reducer.init(&arena, .{ .endpoint = "hermes.supplied", .revision = "supplied-revision" });
    reducer.open();
    try reducer.submit();
    try feedBare(&reducer, scratch, "message.start");
    try feedEvent(&reducer, scratch, "approval.request",
        \\{"command":"rm -rf /tmp/x","choices":["once","deny"]}
    );

    try testing.expectEqualStrings("user.input.requested", typeAt(&reducer, 1));
    try testing.expectEqualStrings("hermes.supplied", payloadAt(&reducer, 1).get("requested_by").?.string);
    try testing.expectEqualStrings("supplied-revision", reducer.envelopes.items[1].object.get("capability_revision").?.string);
}
