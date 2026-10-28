const std = @import("std");
const native = @import("native");

pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";
pub const cost_extension = "com.github.copilot.cost";
pub const routed_model_extension = "com.github.copilot.routed_model";
pub const endpoint_id = "copilot.headless";
pub const execution_owner = "copilot-cli";

pub const Error = error{ InvalidSubmission, RunNotFound, RunActive, SessionClosed } || std.mem.Allocator.Error;
pub const ResolveError = error{ InteractionNotFound, InteractionResolved, InvalidResolution, WrongResponder } || std.mem.Allocator.Error;

const choices = [_][2][]const u8{ .{ "approve-once", "Allow once" }, .{ "reject", "Deny" } };

pub const default_max_queued = 8;

pub const Status = enum { queued, running, cancelling, completed, failed, cancelled };

pub const Options = struct {
    session_id: []const u8,
    native_id: []const u8,
    revision: []const u8,
    participant: []const u8 = "user",
    max_queued: usize = default_max_queued,
    counter: ?*u64 = null,
    now_ms: ?*const fn () i64 = null,
};

pub const Run = struct {
    id: []const u8,
    message_id: []const u8,
    native_message_id: []const u8 = "",
    interaction_id: []const u8 = "",
    status: Status = .queued,
    next: u64 = 1,
    started: bool = false,
    terminal: bool = false,
    cancel_requested: bool = false,
    parts: std.ArrayList([]const u8) = .empty,
    input_tokens: ?u64 = null,
    output_tokens: ?u64 = null,
    measured: bool = false,
    cost: f64 = 0,
    routed_model: []const u8 = "",
};

pub const Admission = struct {
    run_id: []const u8,
    message_id: []const u8,
    admission: []const u8,
    effective_delivery: []const u8,
    status: Status,
};

pub const Tool = struct {
    run: *Run,
    native_id: []const u8,
    id: []const u8,
    name: []const u8,
    arguments: std.json.Value,
    started_event: []const u8 = "",
    terminal: bool = false,
};

pub const Gate = struct {
    id: []const u8,
    native_id: []const u8,
    run: *Run,
    tool: *Tool,
    requested: []const u8 = "",
    resolved: bool = false,
};

pub const Answer = struct { gate: *Gate, approve: bool };

pub const Asked = struct {
    id: []const u8,
    native_id: []const u8,
    run: *Run,
    tool_call_id: []const u8,
    choices: []const []const u8,
    requested: []const u8 = "",
    resolved: bool = false,
};

pub const Given = struct { question_id: []const u8, text: []const u8 = "", selected_option_ids: []const []const u8 = &.{} };

pub const Reply = struct { asked: *Asked, answer: []const u8, freeform: bool, given: Given };

pub const question_id = "answer";

pub const Plan = enum { settled, abort, withdraw };

pub const Cancel = struct { status: Status, plan: Plan };

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: u64 = 0,
    clock: i64 = 0,
    closed: bool = false,
    runs: std.ArrayList(*Run) = .empty,
    current: ?*Run = null,
    tools: std.ArrayList(*Tool) = .empty,
    gates: std.ArrayList(*Gate) = .empty,
    asks: std.ArrayList(*Asked) = .empty,
    envelopes: std.ArrayList(std.json.Value) = .empty,

    pub fn init(arena: *std.heap.ArenaAllocator, options: Options) Reducer {
        return .{ .arena = arena, .options = options };
    }

    fn allocator(self: *Reducer) std.mem.Allocator {
        return self.arena.allocator();
    }

    fn now(self: *Reducer) i64 {
        if (self.options.now_ms) |clock| return clock();
        self.clock += 1;
        return self.clock;
    }

    fn nextID(self: *Reducer, kind: []const u8) std.mem.Allocator.Error![]const u8 {
        const counter = self.options.counter orelse &self.ids;
        counter.* += 1;
        return std.fmt.allocPrint(self.allocator(), "{s}-{d}", .{ kind, counter.* });
    }

    pub fn live(self: *Reducer) usize {
        var total: usize = 0;
        for (self.runs.items) |run| {
            if (!run.terminal) total += 1;
        }
        return total;
    }

    pub fn find(self: *Reducer, run_id: []const u8) ?*Run {
        for (self.runs.items) |run| {
            if (std.mem.eql(u8, run.id, run_id)) return run;
        }
        return null;
    }

    pub fn admit(self: *Reducer, session_id: []const u8) Error!Admission {
        return self.admitWith(session_id, false);
    }

    pub fn reserve(self: *Reducer, session_id: []const u8) Error!Admission {
        return self.admitWith(session_id, true);
    }

    fn admitWith(self: *Reducer, session_id: []const u8, reservation: bool) Error!Admission {
        if (self.closed) return error.SessionClosed;
        if (session_id.len == 0) return error.InvalidSubmission;
        if (!std.mem.eql(u8, session_id, self.options.session_id)) return error.RunNotFound;
        const busy = self.live();
        if (busy > self.options.max_queued) return error.RunActive;
        const run_id = try self.nextID("run");
        const message_id = try self.nextID("message");
        const run = try self.allocator().create(Run);
        const starts = busy == 0 and !reservation;
        run.* = .{ .id = run_id, .message_id = message_id, .status = if (starts) .running else .queued };
        try self.runs.append(self.allocator(), run);
        if (starts) return .{ .run_id = run.id, .message_id = run.message_id, .admission = "started", .effective_delivery = "start", .status = .running };
        return .{ .run_id = run.id, .message_id = run.message_id, .admission = "queued", .effective_delivery = "queue", .status = .queued };
    }

    pub fn bind(self: *Reducer, run_id: []const u8, native_message_id: []const u8) Error!void {
        const run = self.find(run_id) orelse return error.RunNotFound;
        run.native_message_id = try self.allocator().dupe(u8, native_message_id);
    }

    pub fn forget(self: *Reducer, run_id: []const u8) void {
        for (self.runs.items, 0..) |run, index| {
            if (!std.mem.eql(u8, run.id, run_id) or run.started or run.next != 1) continue;
            _ = self.runs.orderedRemove(index);
            return;
        }
    }

    pub fn refuse(self: *Reducer, run_id: []const u8, code: []const u8, message: []const u8) Error!void {
        const run = self.find(run_id) orelse return error.RunNotFound;
        try self.fail(run, code, message);
    }

    pub fn cancel(self: *Reducer, run_id: []const u8) Error!Cancel {
        const run = self.find(run_id) orelse return error.RunNotFound;
        if (run.terminal) return .{ .status = run.status, .plan = .settled };
        run.cancel_requested = true;
        if (!run.started and run.native_message_id.len == 0) {
            try self.settle(run, "run.cancelled", try self.scoped(run));
            return .{ .status = .cancelled, .plan = .settled };
        }
        run.status = .cancelling;
        return .{ .status = .cancelling, .plan = if (run.started) .abort else .withdraw };
    }

    pub fn withdrawn(self: *Reducer, run_id: []const u8) Error!void {
        const run = self.find(run_id) orelse return error.RunNotFound;
        if (run.started or run.terminal) return;
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "reason", .{ .string = "Copilot withdrew the queued prompt" });
        try self.settle(run, "run.cancelled", payload);
    }

    pub fn observe(self: *Reducer, event: native.Event) Error!void {
        if (!std.mem.eql(u8, event.session_id, self.options.native_id)) {
            const run = self.current orelse return;
            return self.fail(run, "copilot_foreign_session", "an event for another Copilot session reached this one");
        }
        const kind = event.kind orelse return;
        switch (kind) {
            .user_message => try self.userMessage(event.data),
            .message_delta => {
                const run = self.current orelse return;
                const text = native.text(event.data, "deltaContent");
                if (text.len == 0) return;
                var part: std.json.ObjectMap = .empty;
                try part.put(self.allocator(), "type", .{ .string = "text" });
                try part.put(self.allocator(), "text", .{ .string = try self.allocator().dupe(u8, text) });
                var payload = try self.scoped(run);
                try payload.put(self.allocator(), "message_id", .{ .string = run.message_id });
                try payload.put(self.allocator(), "part", .{ .object = part });
                try self.emit(run, "content.delta", payload, false);
            },
            .message => {
                const run = self.current orelse return;
                const content = native.text(event.data, "content");
                if (content.len > 0) try run.parts.append(self.allocator(), try self.allocator().dupe(u8, content));
            },
            .usage => {
                const run = self.current orelse return;
                run.input_tokens = accumulate(run.input_tokens, count(event.data, "inputTokens"), !run.measured);
                run.output_tokens = accumulate(run.output_tokens, count(event.data, "outputTokens"), !run.measured);
                run.measured = true;
                if (event.data.get("cost")) |cost| run.cost += number(cost);
                const model = native.text(event.data, "model");
                if (model.len > 0) run.routed_model = try self.allocator().dupe(u8, model);
            },
            .session_idle => {
                const aborted = event.data.get("aborted");
                if (aborted != null and aborted.? == .bool and aborted.?.bool) {
                    for (self.runs.items) |run| {
                        if (run.started and run.cancel_requested and !run.terminal) try self.cancelled(run);
                    }
                    const run = self.current orelse return;
                    return self.fail(run, "copilot_aborted", "Copilot ended the busy period as aborted, without a cancel for this run");
                }
                const run = self.current orelse return;
                try self.complete(run);
            },
            .tool_start => try self.startTool(event.data),
            .tool_partial, .shell_output => {
                const run = self.current orelse return;
                const tool = self.findTool(run, native.text(event.data, "toolCallId")) orelse return;
                if (tool.terminal) return;
                const said = if (native.text(event.data, "partialOutput").len > 0) native.text(event.data, "partialOutput") else native.text(event.data, "text");
                var payload = try self.toolPayload(tool);
                try payload.put(self.allocator(), "progress", .{ .string = try self.allocator().dupe(u8, said) });
                _ = try self.record(run, "action.call.progress", payload, false, tool.started_event);
            },
            .tool_complete => try self.endTool(event.data),
            .permission_requested => try self.askPermission(event.data),
            .permission_completed => {
                const gate = self.findGate(native.text(event.data, "requestId")) orelse return;
                if (gate.resolved) return;
                gate.resolved = true;
                _ = try self.record(gate.run, "action.permission.resolved", try self.resolution(gate, "cancelled", "", null, .{ "copilot_permission_settled_elsewhere", "Copilot settled the permission without this session's answer" }), false, gate.requested);
            },
            .user_input_requested => try self.askUser(event.data),
            .user_input_completed => {
                const asked = self.findAsked(native.text(event.data, "requestId")) orelse return;
                if (asked.resolved) return;
                asked.resolved = true;
                _ = try self.record(asked.run, "user.input.resolved", try self.inputResolution(asked, "cancelled", null), false, asked.requested);
            },
            .session_error => {
                const run = self.current orelse return;
                const said = native.text(event.data, "message");
                try self.fail(run, "copilot_session_error", if (said.len > 0) try self.allocator().dupe(u8, said) else "Copilot reported a session error");
            },
            else => {},
        }
    }

    fn findTool(self: *Reducer, run: *Run, native_id: []const u8) ?*Tool {
        for (self.tools.items) |tool| {
            if (tool.run == run and std.mem.eql(u8, tool.native_id, native_id)) return tool;
        }
        return null;
    }

    fn findGate(self: *Reducer, native_id: []const u8) ?*Gate {
        for (self.gates.items) |gate| {
            if (std.mem.eql(u8, gate.native_id, native_id)) return gate;
        }
        return null;
    }

    fn toolPayload(self: *Reducer, tool: *Tool) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload = try self.scoped(tool.run);
        try payload.put(self.allocator(), "tool_call_id", .{ .string = tool.id });
        try payload.put(self.allocator(), "requested_by", .{ .string = endpoint_id });
        try payload.put(self.allocator(), "execution_owner", .{ .string = execution_owner });
        try payload.put(self.allocator(), "name", .{ .string = tool.name });
        return payload;
    }

    fn startTool(self: *Reducer, data: std.json.ObjectMap) Error!void {
        const run = self.current orelse return;
        const native_id = native.text(data, "toolCallId");
        const name = native.text(data, "toolName");
        if (native_id.len == 0 or name.len == 0 or self.findTool(run, native_id) != null) return self.fail(run, "copilot_invalid_tool_lifecycle", "a tool start that is unnamed or already started");
        const kept_native_id = try self.allocator().dupe(u8, native_id);
        const tool_call_id = try self.nextID("tool-call");
        const kept_name = try self.allocator().dupe(u8, name);
        const arguments = try clone(self.allocator(), data.get("arguments") orelse .null);
        const tool = try self.allocator().create(Tool);
        tool.* = .{ .run = run, .native_id = kept_native_id, .id = tool_call_id, .name = kept_name, .arguments = arguments };
        try self.tools.append(self.allocator(), tool);
        var requested = try self.toolPayload(tool);
        try requested.put(self.allocator(), "arguments_json", tool.arguments);
        const asked = try self.record(run, "action.call.requested", requested, false, "") orelse "";
        tool.started_event = try self.record(run, "action.call.started", try self.toolPayload(tool), false, asked) orelse "";
    }

    fn endTool(self: *Reducer, data: std.json.ObjectMap) Error!void {
        const run = self.current orelse return;
        const tool = self.findTool(run, native.text(data, "toolCallId")) orelse return self.fail(run, "copilot_invalid_tool_lifecycle", "a tool completion without a start in this run");
        if (tool.terminal) return self.fail(run, "copilot_invalid_tool_lifecycle", "a second completion for one tool call");
        tool.terminal = true;
        var payload = try self.toolPayload(tool);
        const succeeded = data.get("success");
        if (succeeded != null and succeeded.? == .bool and succeeded.?.bool) {
            try payload.put(self.allocator(), "result", try clone(self.allocator(), data.get("result") orelse .null));
            _ = try self.record(run, "action.call.completed", payload, false, tool.started_event);
            return;
        }
        const problem: std.json.ObjectMap = if (data.get("error")) |carried| (if (carried == .object) carried.object else .empty) else .empty;
        const denied = std.mem.eql(u8, native.text(problem, "code"), "denied");
        const said = native.text(problem, "message");
        try payload.put(self.allocator(), "error", try failure(self.allocator(), if (denied) "copilot_tool_denied" else "copilot_tool_failed", if (said.len > 0) said else "Copilot reported the tool call failed"));
        _ = try self.record(run, "action.call.failed", payload, false, tool.started_event);
    }

    fn askPermission(self: *Reducer, data: std.json.ObjectMap) Error!void {
        const run = self.current orelse return;
        const request_id = native.text(data, "requestId");
        const asked = data.get("permissionRequest");
        if (request_id.len == 0 or asked == null or asked.? != .object or self.findGate(request_id) != null) return self.fail(run, "copilot_invalid_permission", "a permission request with no request id, no detail, or one already asked");
        const detail = asked.?.object;
        const tool = self.findTool(run, native.text(detail, "toolCallId")) orelse return self.fail(run, "copilot_permission_without_tool", "Copilot asked permission outside a tool call this run started");
        if (tool.terminal) return self.fail(run, "copilot_permission_without_tool", "Copilot asked permission for a tool call that already ended");
        const interaction_id = try self.nextID("interaction");
        const kept_request_id = try self.allocator().dupe(u8, request_id);
        const gate = try self.allocator().create(Gate);
        gate.* = .{ .id = interaction_id, .native_id = kept_request_id, .run = run, .tool = tool };
        try self.gates.append(self.allocator(), gate);
        const intention = native.text(detail, "intention");
        const kind = native.text(detail, "kind");
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "interaction_id", .{ .string = gate.id });
        try payload.put(self.allocator(), "requested_by", .{ .string = endpoint_id });
        try payload.put(self.allocator(), "responded_by", .{ .string = self.options.participant });
        try payload.put(self.allocator(), "tool_call_id", .{ .string = tool.id });
        try payload.put(self.allocator(), "title", .{ .string = try self.allocator().dupe(u8, if (intention.len > 0) intention else if (kind.len > 0) kind else tool.name) });
        const shown = if (native.text(detail, "fullCommandText").len > 0) native.text(detail, "fullCommandText") else native.text(detail, "fileName");
        if (shown.len > 0) try payload.put(self.allocator(), "description", .{ .string = try self.allocator().dupe(u8, shown) });
        var offered = std.json.Array.init(self.allocator());
        for (choices) |choice| {
            var entry: std.json.ObjectMap = .empty;
            try entry.put(self.allocator(), "id", .{ .string = choice[0] });
            try entry.put(self.allocator(), "label", .{ .string = choice[1] });
            try offered.append(.{ .object = entry });
        }
        try payload.put(self.allocator(), "choices", .{ .array = offered });
        try payload.put(self.allocator(), "arguments_json", tool.arguments);
        gate.requested = try self.record(run, "action.permission.requested", payload, false, "") orelse "";
    }

    fn resolution(self: *Reducer, gate: *Gate, outcome: []const u8, choice: []const u8, granted: ?bool, reason: ?[2][]const u8) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload = try self.scoped(gate.run);
        try payload.put(self.allocator(), "interaction_id", .{ .string = gate.id });
        try payload.put(self.allocator(), "requested_by", .{ .string = endpoint_id });
        try payload.put(self.allocator(), "responded_by", .{ .string = self.options.participant });
        try payload.put(self.allocator(), "tool_call_id", .{ .string = gate.tool.id });
        try payload.put(self.allocator(), "outcome", .{ .string = outcome });
        if (choice.len > 0) try payload.put(self.allocator(), "choice_id", .{ .string = choice });
        if (granted) |value| try payload.put(self.allocator(), "granted", .{ .bool = value });
        if (reason) |problem| try payload.put(self.allocator(), "reason", try failure(self.allocator(), problem[0], problem[1]));
        return payload;
    }

    pub fn answer(self: *Reducer, interaction_id: []const u8, run_id: []const u8, responded_by: []const u8, requested_by: []const u8, choice: []const u8, granted: bool) ResolveError!Answer {
        const gate = for (self.gates.items) |candidate| {
            if (std.mem.eql(u8, candidate.id, interaction_id)) break candidate;
        } else return error.InteractionNotFound;
        if (gate.run.terminal) return error.InteractionNotFound;
        if (gate.resolved) return error.InteractionResolved;
        if (!std.mem.eql(u8, run_id, gate.run.id) or (requested_by.len > 0 and !std.mem.eql(u8, requested_by, endpoint_id))) return error.InvalidResolution;
        if (!std.mem.eql(u8, responded_by, self.options.participant)) return error.WrongResponder;
        const approve = std.mem.eql(u8, choice, choices[0][0]);
        if ((!approve and !std.mem.eql(u8, choice, choices[1][0])) or approve != granted) return error.InvalidResolution;
        return .{ .gate = gate, .approve = approve };
    }

    pub fn answered(self: *Reducer, given: Answer) Error!void {
        if (given.gate.resolved or given.gate.run.terminal) return;
        given.gate.resolved = true;
        const payload = try self.resolution(given.gate, if (given.approve) "resolved" else "rejected", if (given.approve) choices[0][0] else choices[1][0], given.approve, null);
        _ = try self.record(given.gate.run, "action.permission.resolved", payload, false, given.gate.requested);
    }

    fn findAsked(self: *Reducer, native_id: []const u8) ?*Asked {
        for (self.asks.items) |asked| {
            if (std.mem.eql(u8, asked.native_id, native_id)) return asked;
        }
        return null;
    }

    fn askUser(self: *Reducer, data: std.json.ObjectMap) Error!void {
        const run = self.current orelse return;
        const request_id = native.text(data, "requestId");
        const question = native.text(data, "question");
        if (request_id.len == 0 or question.len == 0 or self.findAsked(request_id) != null) return self.fail(run, "copilot_invalid_user_input", "a user input request with no request id, no question, or one already asked");
        var listed: std.ArrayList([]const u8) = .empty;
        if (data.get("choices")) |offered| {
            if (offered != .array) return self.fail(run, "copilot_invalid_user_input", "a user input request whose choices are not a list");
            for (offered.array.items) |choice| {
                if (choice != .string or choice.string.len == 0) return self.fail(run, "copilot_invalid_user_input", "a user input choice that is not a non-empty string");
                try listed.append(self.allocator(), try self.allocator().dupe(u8, choice.string));
            }
        }
        const tool_call_id = if (self.findTool(run, native.text(data, "toolCallId"))) |tool| tool.id else "";
        const interaction_id = try self.nextID("interaction");
        const kept_request_id = try self.allocator().dupe(u8, request_id);
        const asked = try self.allocator().create(Asked);
        asked.* = .{ .id = interaction_id, .native_id = kept_request_id, .run = run, .tool_call_id = tool_call_id, .choices = listed.items };
        try self.asks.append(self.allocator(), asked);
        var entry: std.json.ObjectMap = .empty;
        try entry.put(self.allocator(), "id", .{ .string = question_id });
        try entry.put(self.allocator(), "prompt", .{ .string = try self.allocator().dupe(u8, question) });
        try entry.put(self.allocator(), "kind", .{ .string = if (listed.items.len > 0) "single_choice" else "text" });
        try entry.put(self.allocator(), "required", .{ .bool = true });
        if (listed.items.len > 0) {
            var options = std.json.Array.init(self.allocator());
            for (listed.items, 0..) |label, index| {
                var option: std.json.ObjectMap = .empty;
                try option.put(self.allocator(), "id", .{ .string = try std.fmt.allocPrint(self.allocator(), "choice-{d}", .{index + 1}) });
                try option.put(self.allocator(), "label", .{ .string = label });
                try options.append(.{ .object = option });
            }
            try entry.put(self.allocator(), "options", .{ .array = options });
        }
        var questions = std.json.Array.init(self.allocator());
        try questions.append(.{ .object = entry });
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "interaction_id", .{ .string = asked.id });
        try payload.put(self.allocator(), "requested_by", .{ .string = endpoint_id });
        try payload.put(self.allocator(), "responded_by", .{ .string = self.options.participant });
        if (tool_call_id.len > 0) try payload.put(self.allocator(), "tool_call_id", .{ .string = tool_call_id });
        try payload.put(self.allocator(), "title", .{ .string = "Copilot asks a question" });
        try payload.put(self.allocator(), "questions", .{ .array = questions });
        asked.requested = try self.record(run, "user.input.requested", payload, false, "") orelse "";
    }

    fn inputResolution(self: *Reducer, asked: *Asked, status: []const u8, given: ?Given) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload = try self.scoped(asked.run);
        try payload.put(self.allocator(), "interaction_id", .{ .string = asked.id });
        try payload.put(self.allocator(), "requested_by", .{ .string = endpoint_id });
        try payload.put(self.allocator(), "responded_by", .{ .string = self.options.participant });
        if (asked.tool_call_id.len > 0) try payload.put(self.allocator(), "tool_call_id", .{ .string = asked.tool_call_id });
        try payload.put(self.allocator(), "status", .{ .string = status });
        if (given) |reply| {
            var entry: std.json.ObjectMap = .empty;
            try entry.put(self.allocator(), "question_id", .{ .string = question_id });
            if (reply.selected_option_ids.len > 0) {
                var selected = std.json.Array.init(self.allocator());
                for (reply.selected_option_ids) |option| try selected.append(.{ .string = try self.allocator().dupe(u8, option) });
                try entry.put(self.allocator(), "selected_option_ids", .{ .array = selected });
            } else try entry.put(self.allocator(), "text", .{ .string = try self.allocator().dupe(u8, reply.text) });
            var answers = std.json.Array.init(self.allocator());
            try answers.append(.{ .object = entry });
            try payload.put(self.allocator(), "answers", .{ .array = answers });
        }
        return payload;
    }

    pub fn answerInput(self: *Reducer, interaction_id: []const u8, run_id: []const u8, responded_by: []const u8, requested_by: []const u8, answers: []const Given) ResolveError!Reply {
        const asked = for (self.asks.items) |candidate| {
            if (std.mem.eql(u8, candidate.id, interaction_id)) break candidate;
        } else return error.InteractionNotFound;
        if (asked.run.terminal) return error.InteractionNotFound;
        if (asked.resolved) return error.InteractionResolved;
        if (!std.mem.eql(u8, run_id, asked.run.id) or (requested_by.len > 0 and !std.mem.eql(u8, requested_by, endpoint_id))) return error.InvalidResolution;
        if (!std.mem.eql(u8, responded_by, self.options.participant)) return error.WrongResponder;
        if (answers.len != 1 or !std.mem.eql(u8, answers[0].question_id, question_id)) return error.InvalidResolution;
        const given = answers[0];
        if (asked.choices.len == 0) {
            if (given.text.len == 0 or given.selected_option_ids.len > 0) return error.InvalidResolution;
            return .{ .asked = asked, .answer = given.text, .freeform = true, .given = given };
        }
        if (given.text.len > 0 or given.selected_option_ids.len != 1) return error.InvalidResolution;
        const chosen = given.selected_option_ids[0];
        if (!std.mem.startsWith(u8, chosen, "choice-")) return error.InvalidResolution;
        const index = std.fmt.parseInt(usize, chosen["choice-".len..], 10) catch return error.InvalidResolution;
        if (index == 0 or index > asked.choices.len) return error.InvalidResolution;
        return .{ .asked = asked, .answer = asked.choices[index - 1], .freeform = false, .given = given };
    }

    pub fn inputAnswered(self: *Reducer, reply: Reply) Error!void {
        if (reply.asked.resolved or reply.asked.run.terminal) return;
        reply.asked.resolved = true;
        _ = try self.record(reply.asked.run, "user.input.resolved", try self.inputResolution(reply.asked, "submitted", reply.given), false, reply.asked.requested);
    }

    fn settleTools(self: *Reducer, run: *Run, cancelled_run: bool) Error!void {
        for (self.asks.items) |asked| {
            if (asked.run != run or asked.resolved) continue;
            asked.resolved = true;
            _ = try self.record(run, "user.input.resolved", try self.inputResolution(asked, "cancelled", null), false, asked.requested);
        }
        for (self.gates.items) |gate| {
            if (gate.run != run or gate.resolved) continue;
            gate.resolved = true;
            _ = try self.record(run, "action.permission.resolved", try self.resolution(gate, "cancelled", "", null, .{ "run_settled", "the run ended before the permission was answered" }), false, gate.requested);
        }
        for (self.tools.items) |tool| {
            if (tool.run != run or tool.terminal) continue;
            tool.terminal = true;
            var payload = try self.toolPayload(tool);
            if (cancelled_run) {
                _ = try self.record(run, "action.call.cancelled", payload, false, tool.started_event);
            } else {
                try payload.put(self.allocator(), "error", try failure(self.allocator(), "incomplete_tool", "the Copilot run settled with an unfinished tool call"));
                _ = try self.record(run, "action.call.failed", payload, false, tool.started_event);
            }
        }
    }

    fn userMessage(self: *Reducer, data: std.json.ObjectMap) Error!void {
        const message_id = native.text(data, "messageId");
        if (message_id.len == 0) return;
        for (self.runs.items) |run| {
            if (run.started or run.terminal or !std.mem.eql(u8, run.native_message_id, message_id)) continue;
            if (self.current) |previous| {
                if (!previous.terminal) {
                    if (previous.cancel_requested) try self.cancelled(previous) else try self.complete(previous);
                }
            }
            run.started = true;
            run.status = .running;
            run.interaction_id = try self.allocator().dupe(u8, native.text(data, "interactionId"));
            self.current = run;
            var payload = try self.scoped(run);
            try payload.put(self.allocator(), "status", .{ .string = "running" });
            return self.emit(run, "run.started", payload, false);
        }
    }

    fn cancelled(self: *Reducer, run: *Run) Error!void {
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "reason", .{ .string = "Copilot confirmed the abort" });
        try self.settle(run, "run.cancelled", payload);
    }

    fn complete(self: *Reducer, run: *Run) Error!void {
        var content = std.json.Array.init(self.allocator());
        for (run.parts.items) |text| {
            var part: std.json.ObjectMap = .empty;
            try part.put(self.allocator(), "type", .{ .string = "text" });
            try part.put(self.allocator(), "text", .{ .string = text });
            try content.append(.{ .object = part });
        }
        var response: std.json.ObjectMap = .empty;
        try response.put(self.allocator(), "id", .{ .string = run.message_id });
        try response.put(self.allocator(), "role", .{ .string = "assistant" });
        try response.put(self.allocator(), "content", if (content.items.len == 0) .{ .string = "" } else .{ .array = content });
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "final_response", .{ .object = response });
        try payload.put(self.allocator(), "stop_reason", .{ .string = "end_turn" });
        try self.settle(run, "run.completed", payload);
    }

    fn fail(self: *Reducer, run: *Run, code: []const u8, message: []const u8) Error!void {
        var payload = try self.scoped(run);
        try payload.put(self.allocator(), "error", try failure(self.allocator(), code, message));
        try self.settle(run, "run.failed", payload);
    }

    fn settle(self: *Reducer, run: *Run, kind: []const u8, payload_in: std.json.ObjectMap) Error!void {
        if (run.terminal) return;
        try self.settleTools(run, std.mem.eql(u8, kind, "run.cancelled"));
        var payload = payload_in;
        if (run.measured) {
            var usage: std.json.ObjectMap = .empty;
            if (run.input_tokens) |counted| try usage.put(self.allocator(), "input_tokens", try unsigned(self.allocator(), counted));
            if (run.output_tokens) |counted| try usage.put(self.allocator(), "output_tokens", try unsigned(self.allocator(), counted));
            try payload.put(self.allocator(), "usage", .{ .object = usage });
        }
        try self.emit(run, kind, payload, true);
        if (self.current == run) self.current = null;
    }

    fn scoped(self: *Reducer, run: *Run) std.mem.Allocator.Error!std.json.ObjectMap {
        var payload: std.json.ObjectMap = .empty;
        try payload.put(self.allocator(), "session_id", .{ .string = self.options.session_id });
        try payload.put(self.allocator(), "run_id", .{ .string = run.id });
        return payload;
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool) Error!void {
        _ = try self.record(run, kind, payload, terminal, "");
    }

    fn record(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.ObjectMap, terminal: bool, in_reply_to: []const u8) Error!?[]const u8 {
        if (run.terminal) return null;
        const id = try self.nextID("event");
        const sequence = run.next;
        run.next += 1;
        var envelope: std.json.ObjectMap = .empty;
        try envelope.put(self.allocator(), "protocol", .{ .string = protocol_name });
        try envelope.put(self.allocator(), "version", .{ .string = protocol_version });
        try envelope.put(self.allocator(), "profile", .{ .string = profile });
        try envelope.put(self.allocator(), "type", .{ .string = kind });
        try envelope.put(self.allocator(), "id", .{ .string = id });
        try envelope.put(self.allocator(), "payload", .{ .object = payload });
        try envelope.put(self.allocator(), "sequence", try unsigned(self.allocator(), sequence));
        try envelope.put(self.allocator(), "timestamp_ms", .{ .integer = self.now() });
        if (in_reply_to.len > 0) try envelope.put(self.allocator(), "in_reply_to", .{ .string = in_reply_to });
        try envelope.put(self.allocator(), "session_id", .{ .string = self.options.session_id });
        try envelope.put(self.allocator(), "run_id", .{ .string = run.id });
        if (payload.get("tool_call_id")) |carried| try envelope.put(self.allocator(), "tool_call_id", carried);
        try envelope.put(self.allocator(), "capability_revision", .{ .string = self.options.revision });
        if (terminal) {
            if (try self.extensions(run)) |carried| try envelope.put(self.allocator(), "extensions", carried);
            run.terminal = true;
            run.status = if (std.mem.eql(u8, kind, "run.completed")) .completed else if (std.mem.eql(u8, kind, "run.cancelled")) .cancelled else .failed;
        }
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
        return id;
    }

    fn extensions(self: *Reducer, run: *Run) std.mem.Allocator.Error!?std.json.Value {
        if (!run.measured) return null;
        var carried: std.json.ObjectMap = .empty;
        if (std.math.isFinite(run.cost)) {
            var cost: std.json.ObjectMap = .empty;
            try cost.put(self.allocator(), "premium_requests", .{ .float = run.cost });
            try carried.put(self.allocator(), cost_extension, .{ .object = cost });
        }
        if (run.routed_model.len > 0) {
            var routed: std.json.ObjectMap = .empty;
            try routed.put(self.allocator(), "model", .{ .string = run.routed_model });
            try carried.put(self.allocator(), routed_model_extension, .{ .object = routed });
        }
        if (carried.count() == 0) return null;
        return .{ .object = carried };
    }
};

fn failure(arena: std.mem.Allocator, code: []const u8, message: []const u8) std.mem.Allocator.Error!std.json.Value {
    var problem: std.json.ObjectMap = .empty;
    try problem.put(arena, "code", .{ .string = try arena.dupe(u8, code) });
    try problem.put(arena, "message", .{ .string = try arena.dupe(u8, message) });
    return .{ .object = problem };
}

fn clone(arena: std.mem.Allocator, value: std.json.Value) std.mem.Allocator.Error!std.json.Value {
    return switch (value) {
        .string => |text| .{ .string = try arena.dupe(u8, text) },
        .number_string => |digits| .{ .number_string = try arena.dupe(u8, digits) },
        .array => |items| copied: {
            var out = try std.json.Array.initCapacity(arena, items.items.len);
            for (items.items) |item| out.appendAssumeCapacity(try clone(arena, item));
            break :copied .{ .array = out };
        },
        .object => |members| copied: {
            var out: std.json.ObjectMap = .empty;
            var walk = members.iterator();
            while (walk.next()) |member| try out.put(arena, try arena.dupe(u8, member.key_ptr.*), try clone(arena, member.value_ptr.*));
            break :copied .{ .object = out };
        },
        else => value,
    };
}

fn unsigned(arena: std.mem.Allocator, value: u64) std.mem.Allocator.Error!std.json.Value {
    if (value <= std.math.maxInt(i64)) return .{ .integer = @intCast(value) };
    return .{ .number_string = try std.fmt.allocPrint(arena, "{d}", .{value}) };
}

fn number(value: std.json.Value) f64 {
    return switch (value) {
        .integer => |whole| @floatFromInt(whole),
        .float => |real| real,
        else => 0,
    };
}

fn count(data: std.json.ObjectMap, name: []const u8) ?u64 {
    const value = data.get(name) orelse return null;
    return switch (value) {
        .integer => |whole| if (whole >= 0) @intCast(whole) else null,
        .number_string => |raw| blk: {
            if (raw.len == 0) break :blk null;
            for (raw) |digit| if (!std.ascii.isDigit(digit)) break :blk null;
            break :blk std.fmt.parseInt(u64, raw, 10) catch null;
        },
        else => null,
    };
}

fn accumulate(previous: ?u64, next: ?u64, first: bool) ?u64 {
    if (first) return next;
    const sum = @addWithOverflow(previous orelse return null, next orelse return null);
    return if (sum[1] == 0) sum[0] else null;
}

const testing = std.testing;

fn feed(reducer: *Reducer, scratch: std.mem.Allocator, kind: []const u8, data: []const u8) !void {
    const frame = try std.fmt.allocPrint(scratch, "{{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{{\"sessionId\":\"ses\",\"event\":{{\"type\":\"{s}\",\"data\":{s}}}}}}}", .{ kind, data });
    try reducer.observe((try native.decodeEvent(try native.parseMessage(scratch, frame))).?);
}

fn kinds(reducer: *Reducer, scratch: std.mem.Allocator) ![]const u8 {
    var out: std.ArrayList(u8) = .empty;
    for (reducer.envelopes.items) |envelope| {
        if (out.items.len > 0) try out.append(scratch, ' ');
        try out.appendSlice(scratch, envelope.object.get("type").?.string);
        try out.append(scratch, ':');
        try out.appendSlice(scratch, envelope.object.get("run_id").?.string);
    }
    return out.items;
}

fn payloadOf(reducer: *Reducer, index: usize) std.json.ObjectMap {
    return reducer.envelopes.items[index].object.get("payload").?.object;
}

test "a prompt starts its run at its own user.message, streams, and completes at idle with its usage and cost" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "copilot-test-oap-v1" });
    const admission = try reducer.admit("s1");
    try testing.expectEqualStrings("started", admission.admission);
    try reducer.bind(admission.run_id, "m-ours");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m-other\",\"content\":\"not ours\"}");
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m-ours\",\"interactionId\":\"i1\",\"content\":\"hi\"}");
    try feed(&reducer, scratch, "assistant.message_delta", "{\"messageId\":\"a1\",\"deltaContent\":\"po\"}");
    try feed(&reducer, scratch, "assistant.message_delta", "{\"messageId\":\"a1\",\"deltaContent\":\"ng\"}");
    try feed(&reducer, scratch, "assistant.message", "{\"messageId\":\"a1\",\"content\":\"pong\",\"phase\":\"final_answer\"}");
    try feed(&reducer, scratch, "assistant.usage", "{\"model\":\"mai-code-1.1-flash\",\"inputTokens\":11952,\"outputTokens\":5,\"cost\":1.0,\"isAuto\":true}");
    try feed(&reducer, scratch, "session.title_changed", "{}");
    try feed(&reducer, scratch, "session.idle", "{\"mode\":\"interactive\"}");
    try testing.expectEqualStrings("run.started:run-1 content.delta:run-1 content.delta:run-1 run.completed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("po", payloadOf(&reducer, 1).get("part").?.object.get("text").?.string);
    const completed = payloadOf(&reducer, 3);
    try testing.expectEqualStrings("pong", completed.get("final_response").?.object.get("content").?.array.items[0].object.get("text").?.string);
    try testing.expect(completed.get("usage").?.object.get("total_tokens") == null);
    const extensions = reducer.envelopes.items[3].object.get("extensions").?.object;
    try testing.expectEqualStrings("mai-code-1.1-flash", extensions.get(routed_model_extension).?.object.get("model").?.string);
    try testing.expectEqual(@as(f64, 1.0), extensions.get(cost_extension).?.object.get("premium_requests").?.float);
    try testing.expectEqual(@as(i64, 4), reducer.envelopes.items[3].object.get("sequence").?.integer);
    try testing.expectEqual(Status.completed, reducer.find("run-1").?.status);
}

test "a queued prompt starts at its own user.message, which settles the one before it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const first = try reducer.admit("s1");
    try reducer.bind(first.run_id, "m1");
    const second = try reducer.admit("s1");
    try testing.expectEqualStrings("queued", second.admission);
    try testing.expectEqualStrings("queue", second.effective_delivery);
    try reducer.bind(second.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\",\"delivery\":\"idle\"}");
    try feed(&reducer, scratch, "assistant.message", "{\"messageId\":\"a1\",\"content\":\"1\"}");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\",\"delivery\":\"queued\"}");
    try feed(&reducer, scratch, "assistant.message", "{\"messageId\":\"a2\",\"content\":\"second\"}");
    try feed(&reducer, scratch, "session.idle", "{}");
    try testing.expectEqualStrings("run.started:run-1 run.completed:run-1 run.started:run-3 run.completed:run-3", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("second", payloadOf(&reducer, 3).get("final_response").?.object.get("content").?.array.items[0].object.get("text").?.string);
    try testing.expect(payloadOf(&reducer, 1).get("usage") == null);
}

test "an abort this session asked for cancels the run, and one it did not ask for fails it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const asked = try reducer.admit("s1");
    try reducer.bind(asked.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    const cancel = try reducer.cancel(asked.run_id);
    try testing.expectEqual(Plan.abort, cancel.plan);
    try testing.expectEqual(Status.cancelling, cancel.status);
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    const unasked = try reducer.admit("s1");
    try reducer.bind(unasked.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    try testing.expectEqualStrings("run.started:run-1 run.cancelled:run-1 run.started:run-5 run.failed:run-5", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("copilot_aborted", payloadOf(&reducer, 3).get("error").?.object.get("code").?.string);
}

test "a queued run cancels without an abort, and a session error fails the running one" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const running = try reducer.admit("s1");
    try reducer.bind(running.run_id, "m1");
    const waiting = try reducer.admit("s1");
    const cancel = try reducer.cancel(waiting.run_id);
    try testing.expectEqual(Plan.settled, cancel.plan);
    try testing.expectEqual(Status.cancelled, cancel.status);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "session.error", "{\"errorType\":\"rate_limit\",\"message\":\"quota exceeded\"}");
    try testing.expectEqualStrings("run.cancelled:run-3 run.started:run-1 run.failed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("quota exceeded", payloadOf(&reducer, 2).get("error").?.object.get("message").?.string);
    try testing.expectError(error.RunNotFound, reducer.admit("elsewhere"));
    try testing.expectError(error.InvalidSubmission, reducer.admit(""));
}

test "admission refuses once the queue is full" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r", .max_queued = 1 });
    _ = try reducer.admit("s1");
    _ = try reducer.admit("s1");
    try testing.expectError(error.RunActive, reducer.admit("s1"));
}

test "a cancelled run whose next prompt arrives before the aborted idle settles cancelled, not completed" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const first = try reducer.admit("s1");
    try reducer.bind(first.run_id, "m1");
    const second = try reducer.admit("s1");
    try reducer.bind(second.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try testing.expectEqual(Plan.abort, (try reducer.cancel(first.run_id)).plan);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    try testing.expectEqualStrings("run.started:run-1 run.cancelled:run-1 run.started:run-3 run.failed:run-3", try kinds(&reducer, scratch));
}

test "an event for another Copilot session fails the running run rather than folding into it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    const foreign = "{\"jsonrpc\":\"2.0\",\"method\":\"session.event\",\"params\":{\"sessionId\":\"other\",\"event\":{\"type\":\"assistant.message_delta\",\"data\":{\"messageId\":\"x\",\"deltaContent\":\"theirs\"}}}}";
    try reducer.observe((try native.decodeEvent(try native.parseMessage(scratch, foreign))).?);
    try testing.expectEqualStrings("run.started:run-1 run.failed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("copilot_foreign_session", payloadOf(&reducer, 1).get("error").?.object.get("code").?.string);
}

test "a prompt already sent but not yet started is withdrawn, and settles cancelled once Copilot withdraws it" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const running = try reducer.admit("s1");
    try reducer.bind(running.run_id, "m1");
    const waiting = try reducer.admit("s1");
    try reducer.bind(waiting.run_id, "m2");
    const cancel = try reducer.cancel(waiting.run_id);
    try testing.expectEqual(Plan.withdraw, cancel.plan);
    try testing.expectEqual(Status.cancelling, cancel.status);
    try testing.expectEqual(@as(usize, 0), reducer.envelopes.items.len);
    try reducer.withdrawn(waiting.run_id);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try testing.expectEqualStrings("run.cancelled:run-3", try kinds(&reducer, scratch));
}

test "a cost that overflows is left out rather than written as a non-JSON number" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "assistant.usage", "{\"model\":\"m\",\"inputTokens\":1,\"outputTokens\":1,\"cost\":1e308}");
    try feed(&reducer, scratch, "assistant.usage", "{\"model\":\"m\",\"inputTokens\":1,\"outputTokens\":1,\"cost\":1e308}");
    try feed(&reducer, scratch, "session.idle", "{}");
    const extensions = reducer.envelopes.items[1].object.get("extensions").?.object;
    try testing.expect(extensions.get(cost_extension) == null);
    try testing.expectEqualStrings("m", extensions.get(routed_model_extension).?.object.get("model").?.string);
}

fn expectReply(reducer: *Reducer, index: usize, to: usize) !void {
    try testing.expectEqualStrings(reducer.envelopes.items[to].object.get("id").?.string, reducer.envelopes.items[index].object.get("in_reply_to").?.string);
}

const shell_start = "{\"toolCallId\":\"call_1\",\"toolName\":\"bash\",\"arguments\":{\"command\":\"echo ok\"},\"toolTitle\":\"Running command\"}";
const shell_asked = "{\"requestId\":\"req-1\",\"permissionRequest\":{\"kind\":\"shell\",\"toolCallId\":\"call_1\",\"fullCommandText\":\"echo ok\",\"intention\":\"Run the command\"}}";

test "an approved tool call is requested and started at its start, asks inside the call, and completes with its result" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r", .participant = "reviewer" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "tool.execution_start", shell_start);
    try feed(&reducer, scratch, "permission.requested", shell_asked);
    const asked = payloadOf(&reducer, 3);
    try testing.expectEqualStrings("Run the command", asked.get("title").?.string);
    try testing.expectEqualStrings("echo ok", asked.get("description").?.string);
    try testing.expectEqualStrings("reviewer", asked.get("responded_by").?.string);
    try testing.expectEqualStrings(endpoint_id, asked.get("requested_by").?.string);
    try testing.expectEqualStrings("echo ok", asked.get("arguments_json").?.object.get("command").?.string);
    const interaction = asked.get("interaction_id").?.string;
    try testing.expectError(error.WrongResponder, reducer.answer(interaction, "run-1", "someone", "", "approve-once", true));
    try testing.expectError(error.InvalidResolution, reducer.answer(interaction, "run-1", "reviewer", "", "approve-once", false));
    try testing.expectError(error.InvalidResolution, reducer.answer(interaction, "run-1", "reviewer", "", "approve-for-session", true));
    try testing.expectError(error.InvalidResolution, reducer.answer(interaction, "run-2", "reviewer", "", "approve-once", true));
    try testing.expectError(error.InteractionNotFound, reducer.answer("interaction-0", "run-1", "reviewer", "", "approve-once", true));
    const given = try reducer.answer(interaction, "run-1", "reviewer", endpoint_id, "approve-once", true);
    try testing.expectEqualStrings("req-1", given.gate.native_id);
    try testing.expect(given.approve);
    try reducer.answered(given);
    try testing.expectError(error.InteractionResolved, reducer.answer(interaction, "run-1", "reviewer", "", "approve-once", true));
    try feed(&reducer, scratch, "permission.completed", "{\"requestId\":\"req-1\",\"toolCallId\":\"call_1\",\"result\":{\"kind\":\"approved\"}}");
    try feed(&reducer, scratch, "tool.shell_output", "{\"toolCallId\":\"call_1\",\"stream\":\"stdout\",\"text\":\"ok\\n\"}");
    try feed(&reducer, scratch, "tool.execution_partial_result", "{\"toolCallId\":\"call_1\",\"partialOutput\":\"ok\\n\"}");
    try feed(&reducer, scratch, "tool.execution_complete", "{\"toolCallId\":\"call_1\",\"success\":true,\"result\":{\"content\":\"ok\"}}");
    try feed(&reducer, scratch, "session.idle", "{}");
    try testing.expectEqualStrings("run.started:run-1 action.call.requested:run-1 action.call.started:run-1 action.permission.requested:run-1 action.permission.resolved:run-1 action.call.progress:run-1 action.call.progress:run-1 action.call.completed:run-1 run.completed:run-1", try kinds(&reducer, scratch));
    try expectReply(&reducer, 2, 1);
    try expectReply(&reducer, 4, 3);
    try expectReply(&reducer, 7, 2);
    try testing.expectEqualStrings("resolved", payloadOf(&reducer, 4).get("outcome").?.string);
    try testing.expectEqualStrings("approve-once", payloadOf(&reducer, 4).get("choice_id").?.string);
    try testing.expectEqualStrings("ok\n", payloadOf(&reducer, 5).get("progress").?.string);
    try testing.expectEqualStrings("ok", payloadOf(&reducer, 7).get("result").?.object.get("content").?.string);
    const tool_call = payloadOf(&reducer, 1).get("tool_call_id").?.string;
    for (reducer.envelopes.items[1..8]) |envelope| try testing.expectEqualStrings(tool_call, envelope.object.get("tool_call_id").?.string);
}

test "a denied tool call fails as denied, and a permission Copilot settles itself resolves cancelled" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "tool.execution_start", shell_start);
    try feed(&reducer, scratch, "permission.requested", shell_asked);
    const given = try reducer.answer(payloadOf(&reducer, 3).get("interaction_id").?.string, "run-1", "user", "", "reject", false);
    try testing.expect(!given.approve);
    try reducer.answered(given);
    try feed(&reducer, scratch, "tool.execution_complete", "{\"toolCallId\":\"call_1\",\"success\":false,\"error\":{\"message\":\"The user rejected this tool call.\",\"code\":\"denied\"}}");
    try feed(&reducer, scratch, "tool.execution_start", "{\"toolCallId\":\"call_2\",\"toolName\":\"view\",\"arguments\":{\"path\":\"a\"}}");
    try feed(&reducer, scratch, "permission.requested", "{\"requestId\":\"req-2\",\"permissionRequest\":{\"kind\":\"read\",\"toolCallId\":\"call_2\"}}");
    try feed(&reducer, scratch, "permission.completed", "{\"requestId\":\"req-2\",\"result\":{\"kind\":\"approved\"}}");
    try feed(&reducer, scratch, "tool.execution_complete", "{\"toolCallId\":\"call_2\",\"success\":false}");
    try feed(&reducer, scratch, "session.idle", "{}");
    try testing.expectEqualStrings("run.started:run-1 action.call.requested:run-1 action.call.started:run-1 action.permission.requested:run-1 action.permission.resolved:run-1 action.call.failed:run-1 action.call.requested:run-1 action.call.started:run-1 action.permission.requested:run-1 action.permission.resolved:run-1 action.call.failed:run-1 run.completed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("rejected", payloadOf(&reducer, 4).get("outcome").?.string);
    try testing.expect(!payloadOf(&reducer, 4).get("granted").?.bool);
    try testing.expectEqualStrings("copilot_tool_denied", payloadOf(&reducer, 5).get("error").?.object.get("code").?.string);
    try testing.expectEqualStrings("read", payloadOf(&reducer, 8).get("title").?.string);
    try testing.expectEqualStrings("cancelled", payloadOf(&reducer, 9).get("outcome").?.string);
    try testing.expectEqualStrings("copilot_permission_settled_elsewhere", payloadOf(&reducer, 9).get("reason").?.object.get("code").?.string);
    try testing.expectEqualStrings("copilot_tool_failed", payloadOf(&reducer, 10).get("error").?.object.get("code").?.string);
}

test "a run that settles with an open permission and an unfinished call settles both, cancelled or failed with the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const first = try reducer.admit("s1");
    try reducer.bind(first.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "tool.execution_start", shell_start);
    try feed(&reducer, scratch, "permission.requested", shell_asked);
    const interaction = payloadOf(&reducer, 3).get("interaction_id").?.string;
    _ = try reducer.cancel(first.run_id);
    try feed(&reducer, scratch, "session.idle", "{\"aborted\":true}");
    try testing.expectError(error.InteractionNotFound, reducer.answer(interaction, "run-1", "user", "", "approve-once", true));
    const second = try reducer.admit("s1");
    try reducer.bind(second.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try feed(&reducer, scratch, "tool.execution_start", shell_start);
    try feed(&reducer, scratch, "session.error", "{\"message\":\"quota exceeded\"}");
    try testing.expectEqualStrings("run.started:run-1 action.call.requested:run-1 action.call.started:run-1 action.permission.requested:run-1 action.permission.resolved:run-1 action.call.cancelled:run-1 run.cancelled:run-1 run.started:run-12 action.call.requested:run-12 action.call.started:run-12 action.call.failed:run-12 run.failed:run-12", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("run_settled", payloadOf(&reducer, 4).get("reason").?.object.get("code").?.string);
    try testing.expectEqualStrings("incomplete_tool", payloadOf(&reducer, 10).get("error").?.object.get("code").?.string);
}

test "a permission outside any started call, a repeated start, and an unmatched completion fail the run" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const cases = [_][2][]const u8{
        .{ "permission.requested", shell_asked },
        .{ "tool.execution_start", shell_start },
        .{ "tool.execution_complete", "{\"toolCallId\":\"call_9\",\"success\":true}" },
    };
    const codes = [_][]const u8{ "copilot_permission_without_tool", "copilot_invalid_tool_lifecycle", "copilot_invalid_tool_lifecycle" };
    for (cases, codes, 0..) |case, code, index| {
        const admitted = try reducer.admit("s1");
        const message_id = try std.fmt.allocPrint(scratch, "m{d}", .{index});
        try reducer.bind(admitted.run_id, message_id);
        try feed(&reducer, scratch, "user.message", try std.fmt.allocPrint(scratch, "{{\"messageId\":\"{s}\"}}", .{message_id}));
        if (index == 1) try feed(&reducer, scratch, "tool.execution_start", shell_start);
        try feed(&reducer, scratch, case[0], case[1]);
        const last = reducer.envelopes.items[reducer.envelopes.items.len - 1];
        try testing.expectEqualStrings("run.failed", last.object.get("type").?.string);
        try testing.expectEqualStrings(code, last.object.get("payload").?.object.get("error").?.object.get("code").?.string);
    }
}

test "a question with choices asks single choice, and an answer picks a listed choice by its option id" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r", .participant = "reviewer" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "tool.execution_start", "{\"toolCallId\":\"call_1\",\"toolName\":\"ask_user\",\"arguments\":{}}");
    try feed(&reducer, scratch, "user_input.requested", "{\"requestId\":\"req-1\",\"question\":\"Which colour?\",\"choices\":[\"Blue\",\"Green\"],\"allowFreeform\":true,\"toolCallId\":\"call_1\"}");
    const asked = payloadOf(&reducer, 3);
    const question = asked.get("questions").?.array.items[0].object;
    try testing.expectEqualStrings("single_choice", question.get("kind").?.string);
    try testing.expectEqualStrings("Which colour?", question.get("prompt").?.string);
    try testing.expectEqualStrings("choice-2", question.get("options").?.array.items[1].object.get("id").?.string);
    try testing.expectEqualStrings("Green", question.get("options").?.array.items[1].object.get("label").?.string);
    try testing.expectEqualStrings(payloadOf(&reducer, 1).get("tool_call_id").?.string, asked.get("tool_call_id").?.string);
    const interaction = asked.get("interaction_id").?.string;
    try testing.expectError(error.WrongResponder, reducer.answerInput(interaction, "run-1", "someone", "", &.{.{ .question_id = question_id, .selected_option_ids = &.{"choice-1"} }}));
    try testing.expectError(error.InvalidResolution, reducer.answerInput(interaction, "run-1", "reviewer", "", &.{.{ .question_id = question_id, .text = "purple" }}));
    try testing.expectError(error.InvalidResolution, reducer.answerInput(interaction, "run-1", "reviewer", "", &.{.{ .question_id = question_id, .selected_option_ids = &.{"choice-3"} }}));
    try testing.expectError(error.InvalidResolution, reducer.answerInput(interaction, "run-1", "reviewer", "", &.{.{ .question_id = "other", .selected_option_ids = &.{"choice-1"} }}));
    const reply = try reducer.answerInput(interaction, "run-1", "reviewer", endpoint_id, &.{.{ .question_id = question_id, .selected_option_ids = &.{"choice-2"} }});
    try testing.expectEqualStrings("req-1", reply.asked.native_id);
    try testing.expectEqualStrings("Green", reply.answer);
    try testing.expect(!reply.freeform);
    try reducer.inputAnswered(reply);
    try testing.expectError(error.InteractionResolved, reducer.answerInput(interaction, "run-1", "reviewer", "", &.{.{ .question_id = question_id, .selected_option_ids = &.{"choice-2"} }}));
    try feed(&reducer, scratch, "user_input.completed", "{\"requestId\":\"req-1\",\"answer\":\"Green\",\"wasFreeform\":false}");
    try feed(&reducer, scratch, "tool.execution_complete", "{\"toolCallId\":\"call_1\",\"success\":true,\"result\":{\"content\":\"Green\"}}");
    try feed(&reducer, scratch, "session.idle", "{}");
    try testing.expectEqualStrings("run.started:run-1 action.call.requested:run-1 action.call.started:run-1 user.input.requested:run-1 user.input.resolved:run-1 action.call.completed:run-1 run.completed:run-1", try kinds(&reducer, scratch));
    try expectReply(&reducer, 4, 3);
    const resolved = payloadOf(&reducer, 4);
    try testing.expectEqualStrings("submitted", resolved.get("status").?.string);
    try testing.expectEqualStrings("choice-2", resolved.get("answers").?.array.items[0].object.get("selected_option_ids").?.array.items[0].string);
}

test "a question without choices asks for text, and one Copilot settles itself or the run outlives resolves cancelled" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    var reducer = Reducer.init(&arena, .{ .session_id = "s1", .native_id = "ses", .revision = "r" });
    const admitted = try reducer.admit("s1");
    try reducer.bind(admitted.run_id, "m1");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m1\"}");
    try feed(&reducer, scratch, "user_input.requested", "{\"requestId\":\"req-1\",\"question\":\"Name?\",\"allowFreeform\":true}");
    const first = payloadOf(&reducer, 1);
    try testing.expectEqualStrings("text", first.get("questions").?.array.items[0].object.get("kind").?.string);
    try testing.expect(first.get("questions").?.array.items[0].object.get("options") == null);
    try testing.expect(first.get("tool_call_id") == null);
    const interaction = first.get("interaction_id").?.string;
    try testing.expectError(error.InvalidResolution, reducer.answerInput(interaction, "run-1", "user", "", &.{.{ .question_id = question_id, .selected_option_ids = &.{"choice-1"} }}));
    const reply = try reducer.answerInput(interaction, "run-1", "user", "", &.{.{ .question_id = question_id, .text = "Ada" }});
    try testing.expectEqualStrings("Ada", reply.answer);
    try testing.expect(reply.freeform);
    try feed(&reducer, scratch, "user_input.completed", "{\"requestId\":\"req-1\",\"answer\":\"elsewhere\",\"wasFreeform\":true}");
    try testing.expectError(error.InteractionResolved, reducer.answerInput(interaction, "run-1", "user", "", &.{.{ .question_id = question_id, .text = "Ada" }}));
    try feed(&reducer, scratch, "user_input.requested", "{\"requestId\":\"req-2\",\"question\":\"Again?\"}");
    try feed(&reducer, scratch, "session.error", "{\"message\":\"gone\"}");
    try testing.expectEqualStrings("run.started:run-1 user.input.requested:run-1 user.input.resolved:run-1 user.input.requested:run-1 user.input.resolved:run-1 run.failed:run-1", try kinds(&reducer, scratch));
    try testing.expectEqualStrings("cancelled", payloadOf(&reducer, 2).get("status").?.string);
    try testing.expectEqualStrings("cancelled", payloadOf(&reducer, 4).get("status").?.string);
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"none\"}");
    const second = try reducer.admit("s1");
    try reducer.bind(second.run_id, "m2");
    try feed(&reducer, scratch, "user.message", "{\"messageId\":\"m2\"}");
    try feed(&reducer, scratch, "user_input.requested", "{\"requestId\":\"req-3\",\"question\":\"Bad\",\"choices\":[7]}");
    const last = reducer.envelopes.items[reducer.envelopes.items.len - 1];
    try testing.expectEqualStrings("copilot_invalid_user_input", last.object.get("payload").?.object.get("error").?.object.get("code").?.string);
}
