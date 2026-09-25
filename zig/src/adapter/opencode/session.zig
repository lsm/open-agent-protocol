const std = @import("std");
const harness_pins = @import("harness_pins");
const native = @import("native");
const gomarshal = @import("gomarshal");

pub const capability_revision = harness_pins.opencode_capability_revision;
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const endpoint_id = "opencode.server";
pub const pinned_commit = harness_pins.opencode_opencode_commit;
pub const cost_extension = "io.github.anomalyco.opencode.cost";
pub const max_active_runs = 2;
pub const max_queued_runs = 1;
pub const default_history_limit: usize = 100;

pub const Error = error{
    InvalidSubmission,
    SessionClosed,
    RunNotFound,
    RunActive,
    RunTerminal,
    Unsupported,
    CancellationAmbiguous,
    DegradedWithoutOptIn,
} || std.mem.Allocator.Error;

pub const Status = enum { queued, running, cancelling, completed, failed, cancelled };

pub const Failure = struct { message: []const u8, api: bool = false };

pub const PromptOutcome = union(enum) { admitted: native.Admitted, failed: Failure };
pub const ActiveOutcome = union(enum) { listed: bool, failed: Failure };
pub const HistoryOutcome = union(enum) { page: native.HistoryPage, failed: Failure };

pub const Native = struct {
    context: *anyopaque,
    prompt: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!PromptOutcome,
    interrupt: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8) std.mem.Allocator.Error!?Failure,
    active: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8) std.mem.Allocator.Error!ActiveOutcome,
    history: *const fn (context: *anyopaque, arena: std.mem.Allocator, session: []const u8, after: i64, limit: usize) std.mem.Allocator.Error!HistoryOutcome,
};

pub const Options = struct {
    session_id: []const u8 = "session",
    native_id: []const u8,
    model: []const u8 = "",
    message_prefix: []const u8 = "msg_oap",
    history_limit: usize = default_history_limit,
    revision: []const u8 = capability_revision,
    counter: ?*u64 = null,
    now_ms: ?*const fn () i64 = null,
};

pub const Admission = struct {
    session_id: []const u8,
    submission_id: []const u8,
    requested_delivery: []const u8,
    effective_delivery: []const u8 = "queue",
    delivery_resolution: []const u8 = "",
    admission: []const u8 = "queued",
    run_id: []const u8,
    status: Status = .queued,
    model_id: []const u8 = "",
    message_ids: []const []const u8 = &.{},
};

pub const CancelResponse = struct { session_id: []const u8, run_id: []const u8, status: Status };

const Part = struct { reasoning: bool, text: []const u8 };

pub const Run = struct {
    id: []const u8,
    message_id: []const u8,
    status: Status = .queued,
    next: u64 = 1,
    terminal: bool = false,
    prompted: bool = false,
    promotion_seen: bool = false,
    holding: bool = false,
    held: std.ArrayList(std.json.Value) = .empty,
    start_published: bool = false,
    queued_admission: bool = true,
    cancel_requested: bool = false,
    settling: bool = false,
    parts: std.ArrayList(Part) = .empty,
    input_tokens: u64 = 0,
    output_tokens: u64 = 0,
    total_tokens: u64 = 0,
    cost: f64 = 0,
    last_finish: []const u8 = "",
    failure: ?[]const u8 = null,
    open_steps: i64 = 0,
};

const Tool = struct {
    run: *Run,
    native_id: []const u8,
    id: []const u8,
    name: []const u8,
    args: std.json.Value,
    progress: ?std.json.Value = null,
    result: ?std.json.Value = null,
    terminal: bool = false,
    started_event: []const u8 = "",
};

const Pending = struct { native_id: []const u8, run: *Run };

const Settlement = struct { run: *Run, watermark: i64, fencing: bool = false };

const ToolFields = struct {
    arguments: bool = false,
    progress: bool = false,
    result: bool = false,
    failure: ?[2][]const u8 = null,
};

fn str(text: []const u8) std.json.Value {
    return .{ .string = text };
}

fn int(value: i64) std.json.Value {
    return .{ .integer = value };
}

fn unsigned(arena: std.mem.Allocator, value: u64) std.mem.Allocator.Error!std.json.Value {
    if (value <= std.math.maxInt(i64)) return .{ .integer = @intCast(value) };
    return .{ .number_string = try std.fmt.allocPrint(arena, "{d}", .{value}) };
}

pub fn tokenCount(value: f64) u64 {
    if (!(value > 0)) return 0;
    if (value >= 18446744073709551616.0) return std.math.maxInt(u64);
    return @intFromFloat(value);
}

pub fn normalizeModel(arena: std.mem.Allocator, model: ?native.ModelRef) std.mem.Allocator.Error![]const u8 {
    const ref = model orelse return "";
    if (ref.id.len == 0) return "";
    if (ref.provider_id.len > 0) return std.mem.concat(arena, u8, &.{ ref.provider_id, "/", ref.id });
    return ref.id;
}

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    client: Native,
    ids: u64 = 0,
    clock: i64 = 0,
    unusable: bool = false,
    suppressed: bool = false,
    active: ?*Run = null,
    reserved: ?*Run = null,
    runs: std.ArrayList(*Run) = .empty,
    pending: std.ArrayList(Pending) = .empty,
    tools: std.ArrayList(*Tool) = .empty,
    reduced: std.AutoHashMapUnmanaged(i64, void) = .empty,
    catalog: std.ArrayList([]const u8) = .empty,
    settlements: std.ArrayList(Settlement) = .empty,
    envelopes: std.ArrayList(std.json.Value) = .empty,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options, client: Native) Reducer {
        return .{ .arena = arena, .options = options, .client = client };
    }

    pub fn open(self: *Reducer) Error!void {
        if (self.options.session_id.len == 0) self.options.session_id = try self.nextID("session");
        _ = self.now();
        try self.observeModel(self.options.model);
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        if (self.options.now_ms) |clock| return clock();
        self.clock += 1;
        return self.clock;
    }

    fn tick(self: *Reducer) u64 {
        const counter = self.options.counter orelse &self.ids;
        counter.* += 1;
        return counter.*;
    }

    fn nextID(self: *Reducer, kind: []const u8) std.mem.Allocator.Error![]const u8 {
        return std.fmt.allocPrint(self.allocator(), "{s}-{d}", .{ kind, self.tick() });
    }

    fn nextMessageID(self: *Reducer) std.mem.Allocator.Error![]const u8 {
        return std.fmt.allocPrint(self.allocator(), "{s}{d:0>16}", .{ self.options.message_prefix, self.tick() });
    }

    fn put(self: *Reducer, map: *std.json.ObjectMap, key: []const u8, value: std.json.Value) std.mem.Allocator.Error!void {
        try map.put(self.allocator(), key, value);
    }

    fn scoped(self: *Reducer, run: *Run) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload: std.json.ObjectMap = .empty;
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        return payload;
    }

    fn published(run: ?*Run) bool {
        const candidate = run orelse return false;
        return !candidate.terminal or candidate.holding;
    }

    fn find(self: *Reducer, run_id: []const u8) ?*Run {
        for (self.runs.items) |run| {
            if (std.mem.eql(u8, run.id, run_id)) return run;
        }
        return null;
    }

    pub fn runStatus(self: *Reducer, run_id: []const u8) ?Status {
        const run = self.find(run_id) orelse return null;
        return run.status;
    }

    pub fn submit(self: *Reducer, session_id: []const u8, text: []const u8, delivery: []const u8) Error!Admission {
        const queue = std.mem.eql(u8, delivery, "queue");
        if (!queue and delivery.len > 0 and !std.mem.eql(u8, delivery, "auto")) return error.Unsupported;
        if (session_id.len == 0) return error.InvalidSubmission;
        if (self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, session_id, self.options.session_id)) return error.RunNotFound;

        var live: usize = 0;
        var queued: usize = 0;
        for ([_]?*Run{ self.active, self.reserved }) |candidate| {
            if (!published(candidate)) continue;
            live += 1;
            if (candidate.?.queued_admission and !candidate.?.start_published) queued += 1;
        }
        if (live >= max_active_runs or queued >= max_queued_runs or published(self.reserved)) return error.RunActive;
        const behind = live > 0;

        const run_id = try self.nextID("run");
        const message_id = try self.nextID("message");
        const run = try self.allocator().create(Run);
        run.* = .{ .id = run_id, .message_id = message_id };
        self.reserved = run;
        try self.runs.append(self.allocator(), run);
        _ = self.now();

        var admission = Admission{
            .session_id = self.options.session_id,
            .submission_id = try self.nextID("submission"),
            .requested_delivery = if (queue) "queue" else "auto",
            .run_id = run.id,
            .model_id = self.options.model,
        };
        if (behind) admission.delivery_resolution = "session_busy";

        const native_message = try self.nextMessageID();
        if (!native.validMessageID(native_message)) {
            try self.abandon(run, "opencode_invalid_message_id", "ID generator must produce a msg_-prefixed identity for kind opencode-message", "inferred");
            return admission;
        }
        try self.pending.append(self.allocator(), .{ .native_id = native_message, .run = run });

        const request = native.PromptRequest{ .id = native_message, .prompt = .{ .text = text }, .delivery = if (queue) "queue" else "steer" };
        const admitted = switch (try self.client.prompt(self.client.context, self.allocator(), self.options.native_id, request)) {
            .failed => |failure| {
                _ = self.takePending(native_message);
                try self.abandon(run, "opencode_admission_ambiguous", failure.message, settledBy(failure));
                return admission;
            },
            .admitted => |receipt| receipt,
        };
        if (!std.mem.eql(u8, admitted.id, native_message)) {
            _ = self.takePending(native_message);
            const message = try std.fmt.allocPrint(self.allocator(), "server admitted {s} for request {s}", .{ admitted.id, native_message });
            try self.abandon(run, "opencode_foreign_admission", message, "");
            return admission;
        }
        admission.message_ids = try self.allocator().dupe([]const u8, &.{admitted.id});
        if (admitted.promoted_seq != null and !behind and !queue) {
            admission.admission = "started";
            admission.effective_delivery = "start";
            admission.status = .running;
            run.status = .running;
            run.queued_admission = false;
            if (self.reserved == run) {
                self.reserved = null;
                self.active = run;
            }
        }
        return admission;
    }

    fn takePending(self: *Reducer, native_id: []const u8) ?*Run {
        for (self.pending.items, 0..) |entry, index| {
            if (!std.mem.eql(u8, entry.native_id, native_id)) continue;
            _ = self.pending.orderedRemove(index);
            return entry.run;
        }
        return null;
    }

    fn settledBy(failure: Failure) []const u8 {
        return if (failure.api) "" else "inferred";
    }

    fn reductionTarget(self: *Reducer) ?*Run {
        if (self.suppressed) return null;
        if (self.reserved) |reserved| {
            if (reserved.promotion_seen and !reserved.terminal) return reserved;
        }
        return self.active;
    }

    pub fn observe(self: *Reducer, event: native.Event) Error!void {
        try self.handle(event);
        var index: usize = self.settlements.items.len;
        while (index > 0) {
            index -= 1;
            const pending = self.settlements.items[index];
            if (pending.fencing) continue;
            if (!pending.run.terminal and pending.run.open_steps <= 0) continue;
            pending.run.settling = false;
            _ = self.settlements.orderedRemove(index);
        }
    }

    fn decodeFor(
        self: *Reducer,
        comptime T: type,
        comptime decode: fn (std.mem.Allocator, native.Event, *native.Diagnostic) native.Error!T,
        event: native.Event,
        run: ?*Run,
        code: []const u8,
    ) Error!?T {
        var diag = native.Diagnostic{};
        return decode(self.allocator(), event, &diag) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.InvalidWire, error.UnsupportedType => {
                if (run) |target| try self.failRun(target, code, diag.message);
                return null;
            },
        };
    }

    fn handle(self: *Reducer, event: native.Event) Error!void {
        if (!std.mem.eql(u8, event.durable.aggregate_id, self.options.native_id)) {
            return self.abandon(null, "opencode_foreign_session", "durable event belongs to another session", "");
        }
        const seen = try self.reduced.getOrPut(self.allocator(), event.durable.seq);
        if (seen.found_existing) return;
        const run = self.reductionTarget();
        switch (event.kind) {
            .prompt_admitted => _ = try self.decodeFor(native.PromptedData, native.decodePrompted, event, run, "opencode_invalid_prompt_event"),
            .prompted => {
                const data = (try self.decodeFor(native.PromptedData, native.decodePrompted, event, run, "opencode_invalid_prompt_event")) orelse return;
                try self.promote(data.message_id);
            },
            .step_started => {
                const data = (try self.decodeFor(native.StepStartedData, native.decodeStepStarted, event, run, "opencode_invalid_step_event")) orelse return;
                try self.observeModel(try normalizeModel(self.allocator(), data.model));
                const target = run orelse return;
                if (!target.terminal) target.open_steps += 1;
            },
            .step_ended => {
                const data = (try self.decodeFor(native.StepEndedData, native.decodeStepEnded, event, run, "opencode_invalid_step_event")) orelse return;
                const target = run orelse return;
                if (target.terminal) return;
                target.open_steps -= 1;
                target.last_finish = data.finish;
                target.cost += data.cost;
                const input = tokenCount(data.input_tokens);
                const output = tokenCount(data.output_tokens);
                target.input_tokens +%= input;
                target.output_tokens +%= output;
                target.total_tokens +%= input +% output;
                if (target.open_steps <= 0) try self.beginSettlement(target, event.durable.seq);
            },
            .step_failed => {
                const data = (try self.decodeFor(native.StepFailedData, native.decodeStepFailed, event, run, "opencode_invalid_step_event")) orelse return;
                const target = run orelse return;
                if (target.terminal) return;
                target.open_steps -= 1;
                target.failure = data.failure.message;
                if (target.open_steps <= 0) try self.beginSettlement(target, event.durable.seq);
            },
            .text_ended => {
                const data = (try self.decodeFor(native.TextData, native.decodeTextEnded, event, run, "opencode_invalid_text_event")) orelse return;
                try self.appendPart(run orelse return, false, data.text);
            },
            .reasoning_ended => {
                const data = (try self.decodeFor(native.TextData, native.decodeReasoningEnded, event, run, "opencode_invalid_reasoning_event")) orelse return;
                try self.appendPart(run orelse return, true, data.text);
            },
            .tool_called => {
                const data = (try self.decodeFor(native.ToolCalledData, native.decodeToolCalled, event, run, "opencode_invalid_tool_event")) orelse return;
                const target = run orelse return;
                if (data.call_id.len == 0 or data.tool.len == 0) return;
                try self.startTool(target, data.call_id, data.tool, try gomarshal.canonicalAny(self.allocator(), data.input));
            },
            .tool_progress => {
                const data = (try self.decodeFor(native.ToolContentData, native.decodeToolProgress, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.updateTool(run orelse return, data.call_id, try self.contentValue(data.content));
            },
            .tool_success => {
                const data = (try self.decodeFor(native.ToolContentData, native.decodeToolSuccess, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.endTool(run orelse return, data.call_id, null, try self.contentValue(data.content));
            },
            .tool_failed => {
                const data = (try self.decodeFor(native.ToolFailedData, native.decodeToolFailed, event, run, "opencode_invalid_tool_event")) orelse return;
                try self.endTool(run orelse return, data.call_id, data.failure.message, .null);
            },
            .agent_switched, .model_switched, .moved, .context_updated, .synthetic, .shell_started, .shell_ended, .text_started, .reasoning_started, .tool_input_started, .tool_input_ended, .retried, .compaction_started, .compaction_ended, .revert_staged, .revert_cleared, .revert_committed => {},
        }
    }

    fn promote(self: *Reducer, message_id: []const u8) Error!void {
        var owner: ?*Run = null;
        if (self.takePending(message_id)) |candidate| {
            if (!candidate.terminal) {
                if (self.reserved == candidate) {
                    owner = candidate;
                    candidate.promotion_seen = true;
                    candidate.holding = if (self.active) |current| !current.terminal else false;
                } else if (self.active == candidate) owner = candidate;
            }
        }
        self.suppressed = owner == null;
        const run = owner orelse return;
        const reservation = self.reserved == run;
        const started_at = self.now();
        var payload = try self.scoped(run);
        try self.put(&payload, "status", str("running"));
        if (self.options.model.len > 0) try self.put(&payload, "model_id", str(self.options.model));
        try self.put(&payload, "started_at_ms", int(started_at));
        if (!try self.emitWith(run, "run.started", payload, false, null)) return;
        run.prompted = true;
        if (reservation) try self.promoteReserved();
    }

    fn appendPart(self: *Reducer, run: *Run, reasoning: bool, text: []const u8) Error!void {
        if (run.terminal) return;
        try run.parts.append(self.allocator(), .{ .reasoning = reasoning, .text = text });
        var payload = try self.scoped(run);
        try self.put(&payload, "message_id", str(run.message_id));
        try self.put(&payload, "part", try self.partValue(.{ .reasoning = reasoning, .text = text }));
        _ = try self.emitWith(run, "content.delta", payload, false, null);
    }

    fn partValue(self: *Reducer, part: Part) std.mem.Allocator.Error!std.json.Value {
        var value: std.json.ObjectMap = .empty;
        try self.put(&value, "type", str(if (part.reasoning) "reasoning" else "text"));
        if (part.text.len > 0) try self.put(&value, if (part.reasoning) "reasoning" else "text", str(part.text));
        return .{ .object = value };
    }

    fn contentValue(self: *Reducer, content: ?[]const native.Content) std.mem.Allocator.Error!std.json.Value {
        const items = content orelse return .null;
        var array = try std.json.Array.initCapacity(self.allocator(), items.len);
        for (items) |item| {
            var entry: std.json.ObjectMap = .empty;
            try self.put(&entry, "type", str(item.kind));
            if (item.text.len > 0) try self.put(&entry, "text", str(item.text));
            array.appendAssumeCapacity(.{ .object = entry });
        }
        return .{ .array = array };
    }

    fn observeModel(self: *Reducer, model: []const u8) std.mem.Allocator.Error!void {
        if (model.len == 0) return;
        for (self.catalog.items) |seen| {
            if (std.mem.eql(u8, seen, model)) return;
        }
        try self.catalog.append(self.allocator(), model);
    }

    fn findTool(self: *Reducer, run: *Run, native_id: []const u8) ?*Tool {
        for (self.tools.items) |tool| {
            if (tool.run == run and std.mem.eql(u8, tool.native_id, native_id)) return tool;
        }
        return null;
    }

    fn toolPayload(self: *Reducer, tool: *Tool, fields: ToolFields) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload = try self.scoped(tool.run);
        try self.put(&payload, "tool_call_id", str(tool.id));
        try self.put(&payload, "requested_by", str(endpoint_id));
        try self.put(&payload, "execution_owner", str("opencode-server"));
        if (tool.name.len > 0) try self.put(&payload, "name", str(tool.name));
        if (fields.arguments) try self.put(&payload, "arguments_json", tool.args);
        if (fields.progress) {
            if (tool.progress) |progress| try self.put(&payload, "progress", progress);
        }
        if (fields.result) {
            if (tool.result) |result| try self.put(&payload, "result", result);
        }
        if (fields.failure) |failure| {
            var problem: std.json.ObjectMap = .empty;
            try self.put(&problem, "code", str(failure[0]));
            try self.put(&problem, "message", str(failure[1]));
            try self.put(&payload, "error", .{ .object = problem });
        }
        return payload;
    }

    fn startTool(self: *Reducer, run: *Run, native_id: []const u8, name: []const u8, args: std.json.Value) Error!void {
        if (self.findTool(run, native_id) != null) return self.failRun(run, "opencode_invalid_tool_lifecycle", "duplicate or foreign tool start");
        const id = try self.nextID("tool-call");
        const tool = try self.allocator().create(Tool);
        tool.* = .{ .run = run, .native_id = native_id, .id = id, .name = name, .args = args };
        try self.tools.append(self.allocator(), tool);
        const requested = try self.emitEnvelope(run, "action.call.requested", try self.toolPayload(tool, .{ .arguments = true, .progress = true, .result = true }), false, "", null);
        const started = try self.emitEnvelope(run, "action.call.started", try self.toolPayload(tool, .{ .result = true }), false, requested orelse "", null);
        tool.started_event = started orelse "";
    }

    fn updateTool(self: *Reducer, run: *Run, native_id: []const u8, progress: std.json.Value) Error!void {
        const tool = self.findTool(run, native_id) orelse return self.failRun(run, "opencode_invalid_tool_lifecycle", "tool progress without active start");
        if (tool.terminal) return self.failRun(run, "opencode_invalid_tool_lifecycle", "tool progress without active start");
        tool.progress = progress;
        _ = try self.emitEnvelope(run, "action.call.progress", try self.toolPayload(tool, .{ .progress = true, .result = true }), false, tool.started_event, null);
    }

    fn endTool(self: *Reducer, run: *Run, native_id: []const u8, failure: ?[]const u8, content: std.json.Value) Error!void {
        const tool = self.findTool(run, native_id) orelse return self.failRun(run, "opencode_invalid_tool_lifecycle", "tool end without active start");
        if (tool.terminal) return self.failRun(run, "opencode_invalid_tool_lifecycle", "duplicate or foreign tool end");
        tool.terminal = true;
        if (failure) |message| {
            _ = try self.emitEnvelope(run, "action.call.failed", try self.toolPayload(tool, .{ .failure = .{ "opencode_tool_failed", message } }), false, tool.started_event, null);
            return;
        }
        tool.result = content;
        _ = try self.emitEnvelope(run, "action.call.completed", try self.toolPayload(tool, .{ .result = true }), false, tool.started_event, null);
    }

    fn settleTools(self: *Reducer, run: *Run, cancelled: bool) Error!void {
        var unfinished = std.ArrayList(*Tool).empty;
        for (self.tools.items) |tool| {
            if (tool.run != run or tool.terminal) continue;
            tool.terminal = true;
            try unfinished.append(self.allocator(), tool);
        }
        for (unfinished.items) |tool| {
            if (cancelled) {
                _ = try self.emitEnvelope(run, "action.call.cancelled", try self.toolPayload(tool, .{ .progress = true, .result = true }), false, tool.started_event, null);
            } else {
                _ = try self.emitEnvelope(run, "action.call.failed", try self.toolPayload(tool, .{ .progress = true, .failure = .{ "incomplete_tool", "OpenCode run settled with an unfinished tool" } }), false, tool.started_event, null);
            }
        }
    }

    fn reportedCost(self: *Reducer, run: *Run) std.mem.Allocator.Error!?std.json.Value {
        if (!std.math.isFinite(run.cost)) return null;
        var total: std.json.ObjectMap = .empty;
        try self.put(&total, "total_cost_usd", .{ .float = run.cost });
        var extensions: std.json.ObjectMap = .empty;
        try self.put(&extensions, cost_extension, .{ .object = total });
        return .{ .object = extensions };
    }

    fn beginSettlement(self: *Reducer, run: *Run, watermark: i64) Error!void {
        if (run.terminal or run.settling) return;
        run.settling = true;
        try self.settlements.append(self.allocator(), .{ .run = run, .watermark = watermark });
        try self.poll();
    }

    pub fn poll(self: *Reducer) Error!void {
        while (self.settlements.items.len > 0) {
            const index = self.settlements.items.len - 1;
            const pending = self.settlements.items[index];
            if (pending.fencing) return;
            const run = pending.run;
            if (run.terminal or run.open_steps > 0) {
                run.settling = false;
                _ = self.settlements.orderedRemove(index);
                continue;
            }
            switch (try self.client.active(self.client.context, self.allocator(), self.options.native_id)) {
                .failed => |failure| {
                    run.settling = false;
                    _ = self.settlements.orderedRemove(index);
                    try self.abandon(run, "opencode_quiescence_failed", failure.message, settledBy(failure));
                },
                .listed => |listed| {
                    if (listed) return;
                    try self.fence(index);
                },
            }
        }
    }

    fn fence(self: *Reducer, index: usize) Error!void {
        self.settlements.items[index].fencing = true;
        const run = self.settlements.items[index].run;
        var after = self.settlements.items[index].watermark;
        while (!run.terminal) {
            const page = switch (try self.client.history(self.client.context, self.allocator(), self.options.native_id, after, self.options.history_limit)) {
                .failed => |failure| {
                    _ = self.settlements.orderedRemove(index);
                    run.settling = false;
                    return self.abandon(run, "opencode_history_failed", failure.message, settledBy(failure));
                },
                .page => |page| page,
            };
            for (page.events) |event| try self.handle(event);
            if (!page.has_more or page.events.len == 0) break;
            after = page.events[page.events.len - 1].durable.seq;
        }
        _ = self.settlements.orderedRemove(index);
        if (!run.terminal and run.open_steps <= 0) try self.settleRun(run);
        run.settling = false;
    }

    fn settleRun(self: *Reducer, run: *Run) Error!void {
        const cancel_requested = run.cancel_requested;
        const reported = try self.reportedCost(run);
        try self.settleTools(run, cancel_requested);
        if (cancel_requested) {
            var payload = try self.scoped(run);
            try self.put(&payload, "reason", str("OpenCode interrupt confirmed idle"));
            _ = try self.emitWith(run, "run.cancelled", payload, true, reported);
            return;
        }
        if (run.failure) |failure| return self.failRun(run, "opencode_step_failed", failure);
        var payload = try self.scoped(run);
        var response: std.json.ObjectMap = .empty;
        try self.put(&response, "id", str(run.message_id));
        try self.put(&response, "role", str("assistant"));
        if (run.parts.items.len == 0) {
            try self.put(&response, "content", str(""));
        } else {
            var parts = try std.json.Array.initCapacity(self.allocator(), run.parts.items.len);
            for (run.parts.items) |part| parts.appendAssumeCapacity(try self.partValue(part));
            try self.put(&response, "content", .{ .array = parts });
        }
        try self.put(&payload, "final_response", .{ .object = response });
        try self.put(&payload, "stop_reason", str(run.last_finish));
        var usage: std.json.ObjectMap = .empty;
        if (run.input_tokens != 0) try self.put(&usage, "input_tokens", try unsigned(self.allocator(), run.input_tokens));
        if (run.output_tokens != 0) try self.put(&usage, "output_tokens", try unsigned(self.allocator(), run.output_tokens));
        if (run.total_tokens != 0) try self.put(&usage, "total_tokens", try unsigned(self.allocator(), run.total_tokens));
        try self.put(&payload, "usage", .{ .object = usage });
        _ = try self.emitWith(run, "run.completed", payload, true, reported);
    }

    fn failRun(self: *Reducer, run: *Run, code: []const u8, message: []const u8) Error!void {
        return self.failRunSettled(run, code, message, "");
    }

    fn failRunSettled(self: *Reducer, run: *Run, code: []const u8, message: []const u8, settled_by: []const u8) Error!void {
        try self.settleTools(run, true);
        var payload = try self.scoped(run);
        var problem: std.json.ObjectMap = .empty;
        try self.put(&problem, "code", str(code));
        try self.put(&problem, "message", str(message));
        try self.put(&payload, "error", .{ .object = problem });
        if (settled_by.len > 0) try self.put(&payload, "settled_by", str(settled_by));
        _ = try self.emitWith(run, "run.failed", payload, true, try self.reportedCost(run));
    }

    fn abandon(self: *Reducer, origin: ?*Run, code: []const u8, message: []const u8, settled_by: []const u8) Error!void {
        const run = self.active;
        const reserved = self.reserved;
        self.unusable = true;
        if (run) |current| {
            if (!current.terminal) try self.failRunSettled(current, code, message, settled_by);
        }
        const waiting = reserved orelse return;
        if (waiting.terminal) return;
        if (waiting != origin and !waiting.promotion_seen) {
            const dropped = try std.mem.concat(self.allocator(), u8, &.{ "the reservation was dropped before promotion: ", message });
            return self.failRunSettled(waiting, "queue_dropped", dropped, "inferred");
        }
        return self.failRunSettled(waiting, code, message, settled_by);
    }

    pub fn transportFailed(self: *Reducer, message: []const u8) Error!void {
        return self.abandon(null, "opencode_stream_failed", if (message.len == 0) "EOF" else message, "inferred");
    }

    pub fn cancel(self: *Reducer, run_id: []const u8) Error!CancelResponse {
        if (self.unusable) return error.SessionClosed;
        const run = self.find(run_id) orelse return error.RunNotFound;
        const response = CancelResponse{ .session_id = self.options.session_id, .run_id = run.id, .status = .cancelling };
        if (run.terminal) {
            if (run.status == .cancelled) return .{ .session_id = response.session_id, .run_id = run.id, .status = .cancelled };
            return error.RunTerminal;
        }
        if (run.cancel_requested) return .{ .session_id = response.session_id, .run_id = run.id, .status = run.status };
        const prompted = run.prompted;
        const reservation = self.reserved == run and !run.promotion_seen;
        run.cancel_requested = true;
        run.status = .cancelling;
        if (reservation) {
            var payload = try self.scoped(run);
            try self.put(&payload, "reason", str("reservation cancelled before promotion"));
            try self.put(&payload, "settled_by", str("inferred"));
            _ = try self.emitWith(run, "run.cancelled", payload, true, try self.reportedCost(run));
            return response;
        }
        if (try self.client.interrupt(self.client.context, self.allocator(), self.options.native_id)) |failure| {
            try self.abandon(run, "opencode_cancellation_ambiguous", failure.message, settledBy(failure));
            return error.CancellationAmbiguous;
        }
        if (prompted) {
            const updated = self.now();
            var payload = try self.scoped(run);
            try self.put(&payload, "status", str("cancelling"));
            try self.put(&payload, "updated_at_ms", int(updated));
            _ = try self.emitWith(run, "run.status.updated", payload, false, null);
        }
        return response;
    }

    pub fn models(self: *Reducer, session_id: []const u8, allow_degraded: bool) Error!std.json.Value {
        if (!allow_degraded) return error.DegradedWithoutOptIn;
        if (self.unusable) return error.SessionClosed;
        if (session_id.len > 0 and !std.mem.eql(u8, session_id, self.options.session_id)) return error.RunNotFound;
        var catalog: std.json.ObjectMap = .empty;
        try self.put(&catalog, "session_id", str(self.options.session_id));
        if (self.options.model.len > 0) try self.put(&catalog, "current_model_id", str(self.options.model));
        var listed = try std.json.Array.initCapacity(self.allocator(), self.catalog.items.len);
        for (self.catalog.items) |model| {
            var descriptor: std.json.ObjectMap = .empty;
            try self.put(&descriptor, "id", str(model));
            if (std.mem.indexOfScalar(u8, model, '/')) |slash| try self.put(&descriptor, "provider_id", str(model[0..slash]));
            if (std.mem.eql(u8, model, self.options.model)) try self.put(&descriptor, "default", .{ .bool = true });
            listed.appendAssumeCapacity(.{ .object = descriptor });
        }
        try self.put(&catalog, "models", .{ .array = listed });
        return .{ .object = catalog };
    }

    fn promoteReserved(self: *Reducer) Error!void {
        const reserved = self.reserved orelse return;
        if (!reserved.promotion_seen) return;
        if (self.active) |current| {
            if (!current.terminal) return;
        }
        self.reserved = null;
        const held = reserved.held;
        reserved.held = .empty;
        reserved.holding = false;
        if (!reserved.terminal) {
            self.active = reserved;
            reserved.status = .running;
        }
        for (held.items) |envelope| try self.publish(reserved, envelope);
    }

    fn emitWith(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool, extensions: ?std.json.Value) Error!bool {
        _ = try self.emitEnvelope(run, kind, payload, terminal, "", extensions) orelse return false;
        if (terminal) try self.promoteReserved();
        return true;
    }

    fn emitEnvelope(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool, in_reply_to: []const u8, extensions: ?std.json.Value) Error!?[]const u8 {
        if (run.terminal) return null;
        const id = try self.nextID("event");
        const timestamp = self.now();
        const sequence = run.next;
        run.next += 1;
        var envelope: std.json.ObjectMap = .empty;
        try self.put(&envelope, "protocol", str(protocol_name));
        try self.put(&envelope, "version", str(protocol_version));
        try self.put(&envelope, "profile", str(profile));
        try self.put(&envelope, "type", str(kind));
        try self.put(&envelope, "id", str(id));
        try self.put(&envelope, "payload", .{ .object = payload });
        try self.put(&envelope, "sequence", try unsigned(self.allocator(), sequence));
        try self.put(&envelope, "timestamp_ms", int(timestamp));
        if (in_reply_to.len > 0) try self.put(&envelope, "in_reply_to", str(in_reply_to));
        try self.put(&envelope, "session_id", str(self.options.session_id));
        try self.put(&envelope, "run_id", str(run.id));
        if (std.mem.startsWith(u8, kind, "action.call.")) {
            if (payload.get("tool_call_id")) |carried| try self.put(&envelope, "tool_call_id", carried);
        }
        try self.put(&envelope, "capability_revision", str(self.options.revision));
        if (extensions) |carried| try self.put(&envelope, "extensions", carried);
        if (std.mem.eql(u8, kind, "run.started") and !run.holding) run.status = .running;
        if (terminal) {
            run.terminal = true;
            run.status = if (std.mem.eql(u8, kind, "run.completed")) .completed else if (std.mem.eql(u8, kind, "run.cancelled")) .cancelled else .failed;
            if (self.active == run) self.active = null;
            if (self.reserved == run and !run.holding) self.reserved = null;
        }
        const value = std.json.Value{ .object = envelope };
        if (run.holding) {
            try run.held.append(self.allocator(), value);
            return id;
        }
        try self.publish(run, value);
        return id;
    }

    fn publish(self: *Reducer, run: *Run, envelope: std.json.Value) std.mem.Allocator.Error!void {
        const kind = envelope.object.get("type").?.string;
        if (std.mem.eql(u8, kind, "run.started")) run.start_published = true;
        try self.envelopes.append(self.allocator(), envelope);
    }
};

pub fn admissionValue(arena: std.mem.Allocator, admission: Admission) std.mem.Allocator.Error!std.json.Value {
    var payload: std.json.ObjectMap = .empty;
    try payload.put(arena, "session_id", str(admission.session_id));
    try payload.put(arena, "accepted", .{ .bool = true });
    try payload.put(arena, "submission_id", str(admission.submission_id));
    try payload.put(arena, "requested_delivery", str(admission.requested_delivery));
    try payload.put(arena, "effective_delivery", str(admission.effective_delivery));
    if (admission.delivery_resolution.len > 0) try payload.put(arena, "delivery_resolution", str(admission.delivery_resolution));
    try payload.put(arena, "admission", str(admission.admission));
    try payload.put(arena, "run_id", str(admission.run_id));
    try payload.put(arena, "status", str(@tagName(admission.status)));
    if (admission.model_id.len > 0) try payload.put(arena, "model_id", str(admission.model_id));
    if (admission.message_ids.len > 0) {
        var ids = try std.json.Array.initCapacity(arena, admission.message_ids.len);
        for (admission.message_ids) |id| ids.appendAssumeCapacity(str(id));
        try payload.put(arena, "message_ids", .{ .array = ids });
    }
    return .{ .object = payload };
}

const Feature = struct { key: []const u8, level: []const u8, reason: []const u8 };

const features = [_]Feature{
    .{ .key = "action.permissions", .level = "unavailable", .reason = "durable stream carries no permission events; the polling surface is unexercised" },
    .{ .key = "action.tools", .level = "native", .reason = "tool.called/progress/success/failed lifecycle observed natively" },
    .{ .key = "action.tools.execute", .level = "unavailable", .reason = "tools execute server-side; no client-hosted execution surface" },
    .{ .key = "capabilities", .level = "emulated", .reason = "descriptor synthesized from the pinned route inventory" },
    .{ .key = "models.list", .level = "degraded", .reason = "the models this session is observed to run, projected from the native session record and durable step events; the server's own model.list route has no pinned response shape at this revision" },
    .{ .key = "protocol.initialize", .level = "emulated", .reason = "OpenCode has no initialize handshake; OpenAPI and catalogs describe the server" },
    .{ .key = "run.cancel", .level = "degraded", .reason = "interrupt is intent with idle no-op; settlement derived from durable evidence and the active set" },
    .{ .key = "run.reconciliation", .level = "emulated", .reason = "adapter-owned projection over active and durable sequence" },
    .{ .key = "run.replay", .level = "degraded", .reason = "bounded adapter journal; the native durable cursor is exposed as the transcript cursor" },
    .{ .key = "run.resume", .level = "degraded", .reason = "conversation resume exists natively but is not exercised; OAP resume replays the adapter journal" },
    .{ .key = "run.status", .level = "native", .reason = "session.active and durable step events" },
    .{ .key = "run.streaming", .level = "degraded", .reason = "durable stream carries full-value text.ended boundaries, not live deltas" },
    .{ .key = "session.message.delivery.auto", .level = "emulated", .reason = "no native auto; maps to steer which starts immediately when idle" },
    .{ .key = "session.message.delivery.queue", .level = "native", .reason = "SessionInput.Admitted carries delivery=queue with promotedSeq; a reservation is admitted durably and promoted by session.next.prompted" },
    .{ .key = "session.message.delivery.steer", .level = "unavailable", .reason = "an explicit steer request is rejected as outside the v0.1 subset; the server's default delivery is exposed through an auto request" },
    .{ .key = "session.message.submit", .level = "native", .reason = "durable admission receipt with typed conflict rejection" },
    .{ .key = "session.open", .level = "native", .reason = "POST /api/session with server-assigned identity" },
    .{ .key = "session.state", .level = "emulated", .reason = "active set and adapter-owned projection" },
};

pub fn capabilities(arena: std.mem.Allocator) std.mem.Allocator.Error!std.json.Value {
    var endpoint: std.json.ObjectMap = .empty;
    try endpoint.put(arena, "id", str(endpoint_id));
    try endpoint.put(arena, "name", str("OpenCode Server Adapter"));
    try endpoint.put(arena, "version", str(native.pinned_tag));
    try endpoint.put(arena, "adapter", str("opencode-http-sse"));
    var versions = try std.json.Array.initCapacity(arena, 1);
    versions.appendAssumeCapacity(str(protocol_version));
    var profiles = try std.json.Array.initCapacity(arena, 1);
    profiles.appendAssumeCapacity(str(profile));
    var table: std.json.ObjectMap = .empty;
    for (features) |feature| {
        var support: std.json.ObjectMap = .empty;
        try support.put(arena, "level", str(feature.level));
        try support.put(arena, "reason", str(feature.reason));
        try table.put(arena, feature.key, .{ .object = support });
    }
    var limits: std.json.ObjectMap = .empty;
    try limits.put(arena, "max_active_runs_per_session", int(max_active_runs));
    try limits.put(arena, "max_queued_runs_per_session", int(max_queued_runs));
    var descriptor: std.json.ObjectMap = .empty;
    try descriptor.put(arena, "endpoint", .{ .object = endpoint });
    try descriptor.put(arena, "protocol_versions", .{ .array = versions });
    try descriptor.put(arena, "profiles", .{ .array = profiles });
    try descriptor.put(arena, "features", .{ .object = table });
    try descriptor.put(arena, "limits", .{ .object = limits });
    return .{ .object = descriptor };
}

const testing = std.testing;

const Fake = struct {
    listed: bool = false,
    interrupt_failure: ?Failure = null,
    prompts: usize = 0,

    fn from(context: *anyopaque) *Fake {
        return @ptrCast(@alignCast(context));
    }

    fn prompt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!PromptOutcome {
        _ = arena;
        const self = from(context);
        self.prompts += 1;
        const count: i64 = @intCast(self.prompts);
        return .{ .admitted = .{ .admitted_seq = count, .id = request.id, .session_id = session_id, .delivery = request.delivery, .promoted_seq = count } };
    }

    fn interrupt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!?Failure {
        _ = arena;
        _ = session_id;
        return from(context).interrupt_failure;
    }

    fn active(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!ActiveOutcome {
        _ = arena;
        _ = session_id;
        return .{ .listed = from(context).listed };
    }

    fn history(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, after: i64, limit: usize) std.mem.Allocator.Error!HistoryOutcome {
        _ = context;
        _ = arena;
        _ = session_id;
        _ = after;
        _ = limit;
        return .{ .page = .{} };
    }

    fn client(self: *Fake) Native {
        return .{ .context = self, .prompt = prompt, .interrupt = interrupt, .active = active, .history = history };
    }
};

const native_session = "ses_fake00000000000000";

fn nativeEvent(arena: std.mem.Allocator, seq: i64, kind: []const u8, data: []const u8) !native.Event {
    var diag = native.Diagnostic{};
    const wire = try std.fmt.allocPrint(arena, "{{\"id\":\"evt_{d}\",\"type\":\"session.next.{s}\",\"durable\":{{\"aggregateID\":\"" ++ native_session ++ "\",\"seq\":{d},\"version\":1}},\"data\":{s}}}", .{ seq, kind, seq, data });
    return native.decodeEvent(arena, wire, &diag);
}

fn promptedEvent(arena: std.mem.Allocator, seq: i64, message: []const u8) !native.Event {
    return nativeEvent(arena, seq, "prompted", try std.fmt.allocPrint(arena, "{{\"messageID\":\"{s}\",\"prompt\":{{\"text\":\"hello\"}},\"delivery\":\"steer\"}}", .{message}));
}

fn stepStarted(arena: std.mem.Allocator, seq: i64, model: []const u8) !native.Event {
    return nativeEvent(arena, seq, "step.started", try std.fmt.allocPrint(arena, "{{\"model\":{{\"id\":\"{s}\",\"providerID\":\"fixture\"}}}}", .{model}));
}

fn stepEnded(arena: std.mem.Allocator, seq: i64) !native.Event {
    return nativeEvent(arena, seq, "step.ended", "{\"finish\":\"stop\",\"cost\":0,\"tokens\":{\"input\":1,\"output\":1}}");
}

fn expectKinds(reducer: *Reducer, want: []const []const u8) !void {
    try testing.expectEqual(want.len, reducer.envelopes.items.len);
    for (want, reducer.envelopes.items) |kind, envelope| try testing.expectEqualStrings(kind, envelope.object.get("type").?.string);
}

fn payloadOf(envelope: std.json.Value) std.json.ObjectMap {
    return envelope.object.get("payload").?.object;
}

fn errorCode(envelope: std.json.Value) []const u8 {
    return payloadOf(envelope).get("error").?.object.get("code").?.string;
}

test "submit refuses a delivery outside auto and queue, an empty session and another session" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    try testing.expectError(error.Unsupported, reducer.submit("session", "hi", "steer"));
    try testing.expectError(error.Unsupported, reducer.submit("session", "hi", "btw"));
    try testing.expectError(error.InvalidSubmission, reducer.submit("", "hi", "auto"));
    try testing.expectError(error.RunNotFound, reducer.submit("other", "hi", "auto"));
    try testing.expectEqual(@as(usize, 0), fake.prompts);
    const admission = try reducer.submit("session", "hi", "");
    try testing.expectEqualStrings("auto", admission.requested_delivery);
}

test "identities come from one counter in the order the oracle allocates them" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try testing.expectEqualStrings("run-1", admission.run_id);
    try testing.expectEqualStrings("message-2", reducer.find("run-1").?.message_id);
    try testing.expectEqualStrings("submission-3", admission.submission_id);
    try testing.expectEqualStrings("msg_oap0000000000000004", admission.message_ids[0]);
    try testing.expectEqual(@as(i64, 2), reducer.clock);
}

test "a second reservation is refused while one is published, and nothing is admitted once unusable" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try testing.expectEqualStrings("started", first.admission);
    const second = try reducer.submit("session", "later", "auto");
    try testing.expectEqualStrings("queued", second.admission);
    try testing.expectEqualStrings("session_busy", second.delivery_resolution);
    try testing.expectError(error.RunActive, reducer.submit("session", "more", "queue"));
    try testing.expectEqual(@as(usize, 2), fake.prompts);
    try reducer.transportFailed("");
    try expectKinds(&reducer, &.{ "run.failed", "run.failed" });
    try testing.expectEqualStrings("opencode_stream_failed", errorCode(reducer.envelopes.items[0]));
    try testing.expectEqualStrings("EOF", payloadOf(reducer.envelopes.items[0]).get("error").?.object.get("message").?.string);
    try testing.expectEqualStrings("queue_dropped", errorCode(reducer.envelopes.items[1]));
    try testing.expectError(error.SessionClosed, reducer.submit("session", "again", "auto"));
    try testing.expectError(error.SessionClosed, reducer.cancel(first.run_id));
}

test "an identity generator that breaks the message pattern settles the reservation without prompting" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session, .message_prefix = "message_" }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try testing.expectEqualStrings("queued", admission.admission);
    try testing.expectEqual(@as(usize, 0), fake.prompts);
    try expectKinds(&reducer, &.{"run.failed"});
    try testing.expectEqualStrings("opencode_invalid_message_id", errorCode(reducer.envelopes.items[0]));
    try testing.expectEqualStrings("inferred", payloadOf(reducer.envelopes.items[0]).get("settled_by").?.string);
}

test "a settlement waits while the session is listed and is dropped when a step reopens the turn" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{ .listed = true };
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try promptedEvent(scratch, 1, admission.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try stepEnded(scratch, 3));
    try reducer.poll();
    try testing.expectEqual(@as(usize, 1), reducer.settlements.items.len);
    try testing.expectEqual(@as(i64, 3), reducer.settlements.items[0].watermark);
    try reducer.observe(try stepStarted(scratch, 4, "a"));
    try testing.expectEqual(@as(usize, 0), reducer.settlements.items.len);
    try testing.expect(!reducer.find(admission.run_id).?.settling);
    try reducer.observe(try stepEnded(scratch, 5));
    try testing.expectEqual(@as(i64, 5), reducer.settlements.items[0].watermark);
    try expectKinds(&reducer, &.{"run.started"});
    fake.listed = false;
    try reducer.poll();
    try reducer.poll();
    try expectKinds(&reducer, &.{ "run.started", "run.completed" });
    try testing.expectEqual(@as(i64, 4), payloadOf(reducer.envelopes.items[1]).get("usage").?.object.get("total_tokens").?.integer);
}

test "cancel answers by the run's settled status, repeats its intent, and fails the session when the interrupt is refused" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    try testing.expectError(error.RunNotFound, reducer.cancel("run-9"));
    const done = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try promptedEvent(scratch, 1, done.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try stepEnded(scratch, 3));
    try testing.expectEqual(Status.completed, reducer.runStatus(done.run_id).?);
    try testing.expectError(error.RunTerminal, reducer.cancel(done.run_id));

    const cancelled = try reducer.submit("session", "again", "auto");
    try reducer.observe(try promptedEvent(scratch, 4, cancelled.message_ids[0]));
    try testing.expectEqual(Status.cancelling, (try reducer.cancel(cancelled.run_id)).status);
    try testing.expectEqual(Status.cancelling, (try reducer.cancel(cancelled.run_id)).status);
    try reducer.observe(try stepStarted(scratch, 5, "a"));
    try reducer.observe(try stepEnded(scratch, 6));
    try testing.expectEqual(Status.cancelled, (try reducer.cancel(cancelled.run_id)).status);

    const refused = try reducer.submit("session", "third", "auto");
    fake.interrupt_failure = .{ .message = "opencode native: HTTP 500", .api = true };
    try testing.expectError(error.CancellationAmbiguous, reducer.cancel(refused.run_id));
    const last = reducer.envelopes.items[reducer.envelopes.items.len - 1];
    try testing.expectEqualStrings("run.failed", last.object.get("type").?.string);
    try testing.expectEqualStrings("opencode_cancellation_ambiguous", errorCode(last));
    try testing.expectEqual(@as(?std.json.Value, null), payloadOf(last).get("settled_by"));
}

test "a promoted reservation fails with the transport's code, not as a dropped reservation" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{ .listed = true };
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try promptedEvent(scratch, 1, first.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try stepEnded(scratch, 3));
    const second = try reducer.submit("session", "later", "queue");
    try reducer.observe(try promptedEvent(scratch, 4, second.message_ids[0]));
    try testing.expect(reducer.find(second.run_id).?.holding);
    try reducer.transportFailed("gone");
    try expectKinds(&reducer, &.{ "run.started", "run.failed", "run.started", "run.failed" });
    try testing.expectEqualStrings(first.run_id, reducer.envelopes.items[1].object.get("run_id").?.string);
    try testing.expectEqualStrings(second.run_id, reducer.envelopes.items[3].object.get("run_id").?.string);
    try testing.expectEqualStrings("opencode_stream_failed", errorCode(reducer.envelopes.items[3]));
}

test "the catalog needs degraded consent, answers only this session, and grows with step evidence" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{};
    var reducer = Reducer.init(&arena, .{ .native_id = native_session, .model = "fixture/base" }, fake.client());
    try reducer.open();
    try testing.expectError(error.DegradedWithoutOptIn, reducer.models("session", false));
    try testing.expectError(error.RunNotFound, reducer.models("other", true));
    try testing.expectEqualStrings(
        "{\"session_id\":\"session\",\"current_model_id\":\"fixture/base\",\"models\":[{\"id\":\"fixture/base\",\"provider_id\":\"fixture\",\"default\":true}]}",
        try gomarshal.marshal(scratch, try reducer.models("", true)),
    );
    try reducer.observe(try stepStarted(scratch, 1, "next"));
    try reducer.observe(try stepStarted(scratch, 2, "base"));
    try testing.expectEqualStrings(
        "{\"session_id\":\"session\",\"current_model_id\":\"fixture/base\",\"models\":[{\"id\":\"fixture/base\",\"provider_id\":\"fixture\",\"default\":true},{\"id\":\"fixture/next\",\"provider_id\":\"fixture\"}]}",
        try gomarshal.marshal(scratch, try reducer.models("session", true)),
    );
}

test "a token count converts the way the oracle converts it on arm64" {
    try testing.expectEqual(@as(u64, 2), tokenCount(2.9));
    try testing.expectEqual(@as(u64, 0), tokenCount(0));
    try testing.expectEqual(@as(u64, 0), tokenCount(-3));
    try testing.expectEqual(@as(u64, std.math.maxInt(u64)), tokenCount(1e20));
    try testing.expectEqual(@as(u64, 18446744073709549568), tokenCount(18446744073709549568.0));
}

fn holdAndRelease(allocator: std.mem.Allocator) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var fake = Fake{ .listed = true };
    var reducer = Reducer.init(&arena, .{ .native_id = native_session }, fake.client());
    try reducer.open();
    const first = try reducer.submit("session", "hi", "auto");
    try reducer.observe(try promptedEvent(scratch, 1, first.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 2, "a"));
    try reducer.observe(try nativeEvent(scratch, 3, "tool.called", "{\"callID\":\"c\",\"tool\":\"t\",\"input\":{\"b\":1,\"a\":2}}"));
    try reducer.observe(try stepEnded(scratch, 4));
    const second = try reducer.submit("session", "later", "queue");
    try reducer.observe(try promptedEvent(scratch, 5, second.message_ids[0]));
    try reducer.observe(try stepStarted(scratch, 6, "b"));
    try reducer.observe(try nativeEvent(scratch, 7, "text.ended", "{\"text\":\"second\"}"));
    fake.listed = false;
    try reducer.poll();
    try reducer.observe(try stepEnded(scratch, 8));
    if (reducer.envelopes.items.len != 8) return error.TestUnexpectedResult;
    for (reducer.envelopes.items) |envelope| _ = try gomarshal.marshal(scratch, envelope);
}

test "holding and releasing a promoted reservation propagates every allocation failure and leaks nothing" {
    try testing.checkAllAllocationFailures(testing.allocator, holdAndRelease, .{});
}
