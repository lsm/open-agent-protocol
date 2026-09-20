const std = @import("std");
const rpc = @import("rpc");

pub const capability_revision = "claude-code-2.1.263-oap-v3";
pub const protocol_name = "open-agent-protocol";
pub const protocol_version = "0.1";
pub const profile = "open-agent-protocol.agent-control-core";

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
};

pub const Reducer = struct {
    arena: *std.heap.ArenaAllocator,
    options: Options,
    ids: usize = 0,
    clock: i64 = 0,
    current_model: []const u8,
    run: ?Run = null,
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

    fn emit(self: *Reducer, run: *Run, kind: []const u8, payload: std.json.Value) !void {
        run.sequence += 1;
        var envelope = self.object();
        try self.put(&envelope, "protocol", str(protocol_name));
        try self.put(&envelope, "version", str(protocol_version));
        try self.put(&envelope, "profile", str(profile));
        try self.put(&envelope, "type", str(kind));
        try self.put(&envelope, "id", str(try self.nextID("event")));
        try self.put(&envelope, "payload", payload);
        try self.put(&envelope, "sequence", int(run.sequence));
        try self.put(&envelope, "timestamp_ms", int(self.now()));
        try self.put(&envelope, "session_id", str(self.options.session_id));
        try self.put(&envelope, "run_id", str(run.id));
        try self.put(&envelope, "capability_revision", str(capability_revision));
        try self.envelopes.append(self.allocator(), .{ .object = envelope });
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
        try self.emit(run, "run.started", .{ .object = payload });
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
        try self.emit(run, "content.delta", .{ .object = payload });
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
        try self.emit(run, "run.completed", .{ .object = payload });
        self.run = null;
    }

    pub fn observe(self: *Reducer, message: rpc.Message) !void {
        if (message.kind != .observation) return;
        const frame = message.object.object;
        if (std.mem.eql(u8, message.type, "system")) {
            if (std.mem.eql(u8, message.subtype, "init")) {
                if (frame.get("model")) |model| {
                    if (model == .string) self.current_model = model.string;
                }
            }
            return;
        }
        if (std.mem.eql(u8, message.type, "stream_event")) {
            if (frame.get("parent_tool_use_id")) |parent| {
                if (parent != .null) return;
            }
            const event = frame.get("event") orelse return;
            if (event != .object) return;
            const event_type = event.object.get("type") orelse return;
            if (event_type != .string) return;
            if (std.mem.eql(u8, event_type.string, "message_start")) {
                if (!self.belongsToRun(frame)) return;
                try self.startRun();
                return;
            }
            if (std.mem.eql(u8, event_type.string, "content_block_delta")) {
                if (!self.belongsToRun(frame)) return;
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
        if (std.mem.eql(u8, message.type, "result")) {
            try self.settle(frame);
            return;
        }
    }

    fn belongsToRun(self: *Reducer, frame: std.json.ObjectMap) bool {
        const run = self.run orelse return false;
        const single = frame.get("user_message_uuid");
        const many = frame.get("user_message_uuids");
        const has_single = single != null and single.? == .string;
        const has_many = many != null and many.? == .array and many.?.array.items.len > 0;
        if (!has_single and !has_many) return true;
        if (has_single and std.mem.eql(u8, single.?.string, run.submission_uuid)) return true;
        if (has_many) {
            for (many.?.array.items) |item| {
                if (item == .string and std.mem.eql(u8, item.string, run.submission_uuid)) return true;
            }
        }
        return false;
    }
};

fn integerMember(map: std.json.ObjectMap, key: []const u8) ?i64 {
    const value = map.get(key) orelse return null;
    return switch (value) {
        .integer => |number| number,
        .float => |number| @intFromFloat(number),
        else => null,
    };
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
