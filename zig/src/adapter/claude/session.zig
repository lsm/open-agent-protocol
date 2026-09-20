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

pub const Options = struct {
    session_id: []const u8 = "session",
    model: []const u8 = "claude-test",
};

const Run = struct {
    id: []const u8 = "",
    message_id: []const u8 = "",
    submission_id: []const u8 = "",
    submission_uuid: []const u8 = "",
    model: []const u8 = "",
    started: bool = false,
    terminal: bool = false,
    sequence: i64 = 0,
    buffered: std.ArrayList(rpc.Message) = .empty,
};

const Tool = struct {
    native_id: []const u8,
    id: []const u8,
    name: []const u8,
    source: []const u8 = "",
    started_event: []const u8 = "",
    terminal: bool = false,
};

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: usize = 0,
    clock: i64 = 0,
    current_model: []const u8,
    run: ?Run = null,
    tools: std.ArrayList(Tool) = .empty,
    attribution: std.StringHashMapUnmanaged([]const u8) = .empty,
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

    fn str(text: []const u8) std.json.Value {
        return .{ .string = text };
    }

    fn int(value: i64) std.json.Value {
        return .{ .integer = value };
    }

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value) ![]const u8 {
        return self.emitCorrelated(run, kind, payload, "", "");
    }

    fn emitCorrelated(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value, tool_call_id: []const u8, in_reply_to: []const u8) ![]const u8 {
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
        if (self.findTool(native_id) != null) return;
        const tool = Tool{
            .native_id = native_id,
            .id = try self.nextID("tool-call"),
            .name = name,
            .source = self.attributionFor(name),
        };

        var requested = try self.toolPayload(run, tool);
        if (input) |value| try self.put(&requested, "arguments_json", value);
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
        const tool = self.findTool(native_id) orelse return;
        if (tool.terminal) return;
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

    fn startRun(self: *Reducer) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (run.started or run.terminal) return;
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
            if (self.run == null or self.run.?.terminal) break;
            try self.applyRunObservation(buffered);
        }
    }

    fn emitDelta(self: *Reducer, kind: []const u8, text: []const u8) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (!run.started or run.terminal) return;
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

    fn settle(self: *Reducer, frame: std.json.ObjectMap) !void {
        if (self.run == null) return;
        const run = &self.run.?;
        if (!run.started or run.terminal) return;
        run.terminal = true;

        var response = self.object();
        try self.put(&response, "id", str(run.message_id));
        try self.put(&response, "role", str("assistant"));
        if (frame.get("result")) |result| {
            if (result == .string) try self.put(&response, "content", str(result.string));
        }

        var payload = self.object();
        try self.put(&payload, "session_id", str(self.options.session_id));
        try self.put(&payload, "run_id", str(run.id));
        try self.put(&payload, "final_response", .{ .object = response });
        try self.put(&payload, "stop_reason", str(stopReason(frame)));
        if (frame.get("usage")) |usage| {
            if (usage == .object) {
                const input = integerMember(usage.object, "input_tokens");
                const output = integerMember(usage.object, "output_tokens");
                if (input != null or output != null) {
                    var totals = self.object();
                    if (input) |value| try self.put(&totals, "input_tokens", int(value));
                    if (output) |value| try self.put(&totals, "output_tokens", int(value));
                    try self.put(&totals, "total_tokens", int((input orelse 0) + (output orelse 0)));
                    try self.put(&payload, "usage", .{ .object = totals });
                }
            }
        }
        if (integerMember(frame, "duration_ms")) |duration| {
            try self.put(&payload, "duration_ms", int(duration));
        }
        _ = try self.emit(run, "run.completed", .{ .object = payload });
        self.run = null;
    }

    pub fn observe(self: *Reducer, message: rpc.Message) !void {
        if (message.kind != .observation) return;
        if (self.run) |run| {
            if (!run.started and !run.terminal) {
                if (self.echoMatches(message)) {
                    try self.startRun();
                    try self.applyRunObservation(message);
                } else {
                    try self.run.?.buffered.append(self.allocator(), message);
                }
                return;
            }
            if (run.started and !run.terminal) {
                try self.applyRunObservation(message);
                return;
            }
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
                    const name = entry.object.get("name") orelse continue;
                    if (name != .string or name.string.len == 0) continue;
                    if (containsName(servers.items, name.string)) continue;
                    try servers.append(self.allocator(), name.string);
                }
            }
        }
        self.attribution.clearRetainingCapacity();
        const listed = frame.get("tools") orelse return;
        if (listed != .array) return;
        for (listed.array.items) |entry| {
            if (entry != .string or entry.string.len == 0) continue;
            if (self.attribution.contains(entry.string)) continue;
            if (!servedByHarness(entry.string, servers.items)) continue;
            try self.attribution.put(self.allocator(), entry.string, native_source);
        }
    }

    fn attributionFor(self: *Reducer, name: []const u8) []const u8 {
        return self.attribution.get(name) orelse "";
    }

    fn echoMatches(self: *Reducer, message: rpc.Message) bool {
        const run = self.run orelse return false;
        const frame = message.object.object;
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
                    const text = delta.object.get("text") orelse return;
                    if (text == .string) try self.emitDelta("text", text.string);
                } else if (std.mem.eql(u8, delta_type.string, "thinking_delta")) {
                    const text = delta.object.get("thinking") orelse return;
                    if (text == .string) try self.emitDelta("thinking", text.string);
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
                const name = block.object.get("name") orelse continue;
                if (id != .string or id.string.len == 0 or name != .string) continue;
                try self.startTool(id.string, name.string, block.object.get("input"));
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
                var failed = false;
                if (block.object.get("is_error")) |flag| {
                    if (flag == .bool) failed = flag.bool;
                }
                try self.endTool(id.string, block.object.get("content"), failed);
            }
            return;
        }
        if (std.mem.eql(u8, message.type, "result")) {
            try self.settle(frame);
            return;
        }
    }
};

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

fn servedByHarness(name: []const u8, servers: []const []const u8) bool {
    if (!std.mem.startsWith(u8, name, mcp_tool_prefix)) return true;
    const rest = name[mcp_tool_prefix.len..];
    for (servers) |server| {
        if (rest.len <= server.len + 2) continue;
        if (!std.mem.startsWith(u8, rest, server)) continue;
        if (std.mem.startsWith(u8, rest[server.len..], "__")) return false;
    }
    return true;
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

fn stopReason(frame: std.json.ObjectMap) []const u8 {
    if (frame.get("terminal_reason")) |reason| {
        if (reason == .string) {
            if (std.mem.eql(u8, reason.string, "completed")) return "completed";
            if (std.mem.startsWith(u8, reason.string, "aborted")) return "cancelled";
            return reason.string;
        }
    }
    return "completed";
}

const testing = std.testing;

fn observeText(reducer: *Reducer, arena: std.mem.Allocator, text: []const u8) !void {
    const message = try rpc.parseMessage(arena, text);
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
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
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
        \\{"type":"system","subtype":"init","model":"model-a","uuid":"i1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"result","subtype":"success","terminal_reason":"completed","result":"one","uuid":"r1"}
    );

    try reducer.submit("turn-2");
    try observeText(&reducer, scratch,
        \\{"type":"system","subtype":"init","model":"model-b","uuid":"i2"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e2","user_message_uuid":"turn-2"}
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
        \\{"type":"system","subtype":"init","model":"model-a","uuid":"i1"}
    );
    try testing.expectEqualStrings("claude-test", reducer.current_model);

    try observeText(&reducer, scratch,
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
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
        \\{"type":"system","subtype":"init","model":"model-a","tools":["Bash","mcp__files__read"],"mcp_servers":[{"name":"files"}],"uuid":"i1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash"},{"type":"tool_use","id":"t2","name":"Read"},{"type":"tool_use","id":"t3","name":"mcp__files__read"}]},"uuid":"a1"}
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
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash"}]},"uuid":"a1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"user","origin":{},"message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"out"}]},"uuid":"u1"}
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
        \\{"type":"stream_event","event":{"type":"message_start"},"uuid":"e1","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"mine"}},"uuid":"e2","user_message_uuid":"turn-1"}
    );
    try observeText(&reducer, scratch,
        \\{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"theirs"}},"uuid":"e3","user_message_uuid":"turn-9"}
    );

    const kinds = try emittedTypes(&reducer, scratch);
    try testing.expectEqual(@as(usize, 2), kinds.len);
    try testing.expectEqualStrings("content.delta", kinds[1]);
}

test "an init outside a pending run is adopted when it arrives" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var reducer = Reducer.init(&arena, .{});
    reducer.open();

    try observeText(&reducer, arena.allocator(),
        \\{"type":"system","subtype":"init","model":"model-a","uuid":"i1"}
    );
    try testing.expectEqualStrings("model-a", reducer.current_model);
}
