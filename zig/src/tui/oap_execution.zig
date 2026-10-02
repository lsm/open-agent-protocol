const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const json_encode = @import("json_encode");
const model_ref = @import("model_ref");
const tui_runtime = @import("tui_runtime");
const tui_session = @import("tui_session");
const adapter_endpoint = @import("adapter_endpoint");
const oapx_adapter = @import("oapx_adapter");
const OwnedSlice = @import("owned_slice").OwnedSlice;

const TuiEvent = tui_session.TuiEvent;

const protocol_name = "open-agent-protocol";
const protocol_version = "0.1";
const profile = "open-agent-protocol.agent-control-core";
const participant = "oapx.tui";
const idle_sleep_ns = 2 * std.time.ns_per_ms;
const startup_attempts = 2000;
pub const in_process_frame_limit: usize = 64 << 20;

pub const OapExecution = struct {
    allocator: std.mem.Allocator,
    adapter: *oapx_adapter.Adapter,
    endpoint: adapter_endpoint.Endpoint,
    sink: ?tui_runtime.EventSink = null,
    revision: []u8 = &.{},
    session_id: []u8 = &.{},
    run_id: []u8 = &.{},
    ids: u64 = 0,
    inbound: std.ArrayList([]u8) = .empty,
    inbound_mutex: std.atomic.Mutex = .unlocked,
    thread: ?std.Thread = null,
    stopping: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    turn_open: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    cancel_pending: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    in_assistant: bool = false,
    text: std.ArrayList(u8) = .empty,
    session_models: std.ArrayList([]u8) = .empty,

    pub fn create(allocator: std.mem.Allocator, options: tui_runtime.TuiRuntimeOptions) !*OapExecution {
        const adapter = try allocator.create(oapx_adapter.Adapter);
        errdefer allocator.destroy(adapter);
        adapter.* = oapx_adapter.Adapter.init(allocator, options);
        const self = try allocator.create(OapExecution);
        self.* = .{ .allocator = allocator, .adapter = adapter, .endpoint = adapter_endpoint.Endpoint.init(allocator, adapter.adapter(), .{ .frame_limit = in_process_frame_limit }) };
        return self;
    }

    pub fn destroy(self: *OapExecution) void {
        const allocator = self.allocator;
        self.halt();
        self.endpoint.deinit();
        for (self.inbound.items) |line| allocator.free(line);
        self.inbound.deinit(allocator);
        self.text.deinit(allocator);
        self.forgetSessionModels();
        self.session_models.deinit(allocator);
        allocator.free(self.revision);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        allocator.destroy(self.adapter);
        allocator.destroy(self);
    }

    pub fn remote(self: *OapExecution) tui_runtime.RemoteExecution {
        return .{ .ctx = self, .vtable = &vtable };
    }

    const vtable = tui_runtime.RemoteExecution.VTable{
        .start = start,
        .submit = submit,
        .cancel = cancel,
        .switch_model = switchModel,
        .stop = stop,
    };

    fn cast(ctx: *anyopaque) *OapExecution {
        return @ptrCast(@alignCast(ctx));
    }

    fn start(ctx: *anyopaque, sink: tui_runtime.EventSink, settings: tui_runtime.RemoteSettings) anyerror!void {
        const self = cast(ctx);
        self.sink = sink;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();

        var initialize = Map.init(a);
        try initialize.put("protocol_versions", try strings(a, &.{protocol_version}));
        try initialize.put("profiles", try strings(a, &.{profile}));
        const initialized = try self.exchange(a, "protocol.initialize.request", initialize.value(), false);
        const revision = initialized.object.get("capability_revision") orelse return error.OapInitializeFailed;
        if (revision != .string) return error.OapInitializeFailed;
        const kept_revision = try self.allocator.dupe(u8, revision.string);
        self.allocator.free(self.revision);
        self.revision = kept_revision;

        var settings_map = Map.init(a);
        try settings_map.put("thinking_level", .{ .string = @tagName(settings.thinking_level) });
        if (settings.context_window) |window| try settings_map.put("context_window", .{ .integer = window });
        try settings_map.put("output", switch (settings.output) {
            .auto => .{ .string = "auto" },
            .max => .{ .string = "max" },
            .tokens => |count| .{ .integer = count },
        });
        try settings_map.put("permission_mode", .{ .string = @tagName(settings.permission_mode) });
        if (settings.workspace_root.len > 0) try settings_map.put("workspace_root", .{ .string = settings.workspace_root });
        try settings_map.put("user_input", .{ .bool = false });
        var metadata = Map.init(a);
        try metadata.put(oapx_adapter.settings_key, settings_map.value());
        var open = Map.init(a);
        try open.put("metadata", metadata.value());
        const opened = try self.exchange(a, "session.open.request", open.value(), true);
        const payload = opened.object.get("payload") orelse return error.OapOpenFailed;
        if (payload != .object) return error.OapOpenFailed;
        const session_id = payload.object.get("session_id") orelse return error.OapOpenFailed;
        if (session_id != .string) return error.OapOpenFailed;
        const kept_session = try self.allocator.dupe(u8, session_id.string);
        self.allocator.free(self.session_id);
        self.session_id = kept_session;

        var listing = Map.init(a);
        try listing.put("session_id", .{ .string = self.session_id });
        const listed = try self.exchange(a, "models.request", listing.value(), true);
        self.forgetSessionModels();
        if (listed.object.get("payload")) |models_payload| {
            if (models_payload == .object) {
                if (models_payload.object.get("models")) |models_value| {
                    if (models_value == .array) {
                        for (models_value.array.items) |entry| {
                            if (entry != .object) continue;
                            const id = stringOf(entry.object, "id") orelse continue;
                            const kept = try self.allocator.dupe(u8, id);
                            errdefer self.allocator.free(kept);
                            try self.session_models.append(self.allocator, kept);
                        }
                    }
                }
            }
        }

        if (settings.model) |model| {
            const wanted = try modelRef(a, model);
            var switch_map = Map.init(a);
            try switch_map.put("session_id", .{ .string = self.session_id });
            try switch_map.put("model_id", .{ .string = wanted });
            _ = self.exchange(a, "session.model.switch.request", switch_map.value(), true) catch |err| switch (err) {
                error.OapRequestRefused => {
                    const message = try std.fmt.allocPrint(self.allocator, "{s} is not in the OAP session's catalog, so the session runs on its own default model; refresh or restart oapx tui to pick it up", .{wanted});
                    self.deliver(.{ .system_warning = .{ .message = OwnedSlice(u8).initOwned(message) } });
                },
                else => return err,
            };
        }
        self.stopping.store(false, .release);
        self.thread = try std.Thread.spawn(.{}, run, .{self});
    }

    fn exchange(self: *OapExecution, a: std.mem.Allocator, kind: []const u8, payload: std.json.Value, scoped: bool) !std.json.Value {
        const id = try self.nextId(a, "start");
        const line = try self.envelope(a, kind, id, payload, scoped, null);
        try self.endpoint.handleLine(line);
        var attempts: usize = 0;
        while (attempts < startup_attempts) : (attempts += 1) {
            while (self.endpoint.popOutbound()) |answer| {
                defer self.allocator.free(answer);
                const parsed = std.json.parseFromSliceLeaky(std.json.Value, a, try a.dupe(u8, answer), .{}) catch continue;
                if (parsed != .object) continue;
                const reply_to = parsed.object.get("in_reply_to") orelse continue;
                if (reply_to != .string or !std.mem.eql(u8, reply_to.string, id)) continue;
                const answered = parsed.object.get("type") orelse continue;
                if (answered == .string and std.mem.eql(u8, answered.string, "error.response")) return error.OapRequestRefused;
                return parsed;
            }
            _ = try self.endpoint.pump(0);
            compat.time.sleepNs(std.time.ns_per_ms);
        }
        return error.OapRequestUnanswered;
    }

    fn submit(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        var part = Map.init(a);
        try part.put("type", .{ .string = "text" });
        try part.put("text", .{ .string = text });
        var parts = std.json.Array.init(a);
        try parts.append(part.value());
        var message = Map.init(a);
        try message.put("role", .{ .string = "user" });
        try message.put("content", .{ .array = parts });
        var messages = std.json.Array.init(a);
        try messages.append(message.value());
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("messages", .{ .array = messages });
        try payload.put("delivery", .{ .string = "auto" });
        self.lockInbound();
        self.allocator.free(self.run_id);
        self.run_id = &.{};
        self.cancel_pending.store(false, .release);
        self.inbound_mutex.unlock();
        self.turn_open.store(true, .release);
        errdefer self.turn_open.store(false, .release);
        try self.enqueue(a, "session.message.submit.request", "submit", payload.value(), null);
    }

    fn cancel(ctx: *anyopaque) void {
        const self = cast(ctx);
        self.sendCancel() catch {};
    }

    fn sendCancel(self: *OapExecution) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        self.lockInbound();
        const run_id = a.dupe(u8, self.run_id) catch |err| {
            self.inbound_mutex.unlock();
            return err;
        };
        if (run_id.len == 0) {
            self.cancel_pending.store(true, .release);
            self.inbound_mutex.unlock();
            return;
        }
        self.inbound_mutex.unlock();
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("run_id", .{ .string = run_id });
        try self.enqueue(a, "run.cancel.request", "cancel", payload.value(), run_id);
    }

    fn forgetSessionModels(self: *OapExecution) void {
        for (self.session_models.items) |id| self.allocator.free(id);
        self.session_models.clearRetainingCapacity();
    }

    fn sessionLists(self: *OapExecution, ref: []const u8) bool {
        for (self.session_models.items) |id| {
            if (std.mem.eql(u8, id, ref)) return true;
        }
        return false;
    }

    fn switchModel(ctx: *anyopaque, model: ai_types.Model) anyerror!void {
        const self = cast(ctx);
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        const ref = try modelRef(a, model);
        if (!self.sessionLists(ref)) return error.ModelNotFound;
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("model_id", .{ .string = ref });
        try self.enqueue(a, "session.model.switch.request", "switch", payload.value(), null);
    }

    fn stop(ctx: *anyopaque) void {
        cast(ctx).halt();
    }

    fn halt(self: *OapExecution) void {
        self.stopping.store(true, .release);
        if (self.thread) |thread| {
            thread.join();
            self.thread = null;
        }
    }

    fn lockInbound(self: *OapExecution) void {
        while (!self.inbound_mutex.tryLock()) std.atomic.spinLoopHint();
    }

    fn enqueue(self: *OapExecution, a: std.mem.Allocator, kind: []const u8, prefix: []const u8, payload: std.json.Value, run_id: ?[]const u8) !void {
        self.lockInbound();
        const id = self.nextId(a, prefix) catch |err| {
            self.inbound_mutex.unlock();
            return err;
        };
        self.inbound_mutex.unlock();
        const line = try self.envelope(a, kind, id, payload, true, run_id);
        const owned = try self.allocator.dupe(u8, line);
        errdefer self.allocator.free(owned);
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        try self.inbound.append(self.allocator, owned);
    }

    fn nextId(self: *OapExecution, a: std.mem.Allocator, prefix: []const u8) ![]u8 {
        self.ids += 1;
        return std.fmt.allocPrint(a, "{s}-{d}", .{ prefix, self.ids });
    }

    fn envelope(self: *OapExecution, a: std.mem.Allocator, kind: []const u8, id: []const u8, payload: std.json.Value, scoped: bool, run_id: ?[]const u8) ![]u8 {
        var map = Map.init(a);
        try map.put("protocol", .{ .string = protocol_name });
        try map.put("version", .{ .string = protocol_version });
        try map.put("profile", .{ .string = profile });
        try map.put("type", .{ .string = kind });
        try map.put("id", .{ .string = id });
        if (self.revision.len > 0) try map.put("capability_revision", .{ .string = self.revision });
        if (scoped and self.session_id.len > 0) try map.put("session_id", .{ .string = self.session_id });
        if (run_id) |value| try map.put("run_id", .{ .string = value });
        try map.put("payload", payload);
        return json_encode.valueAlloc(a, map.value());
    }

    fn run(self: *OapExecution) void {
        while (!self.stopping.load(.acquire)) {
            const moved = self.cycle() catch |err| moved: {
                self.deliver(.{ .@"error" = .{ .message = OwnedSlice(u8).initBorrowed(@errorName(err)) } });
                if (self.turn_open.load(.acquire)) self.endTurn(.@"error");
                break :moved false;
            };
            if (!moved) compat.time.sleepNs(idle_sleep_ns);
        }
    }

    fn cycle(self: *OapExecution) !bool {
        var moved = false;
        while (self.takeInbound()) |line| {
            defer self.allocator.free(line);
            try self.endpoint.handleLine(line);
            moved = true;
        }
        if (try self.endpoint.pump(0)) moved = true;
        while (self.endpoint.popOutbound()) |line| {
            defer self.allocator.free(line);
            try self.translateLine(line);
            moved = true;
        }
        return moved;
    }

    fn takeInbound(self: *OapExecution) ?[]u8 {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        if (self.inbound.items.len == 0) return null;
        return self.inbound.orderedRemove(0);
    }

    fn deliver(self: *OapExecution, event: TuiEvent) void {
        const sink = self.sink orelse {
            var owned = event;
            owned.deinit(self.allocator);
            return;
        };
        sink.push(sink.ctx, event);
    }

    fn ownedText(self: *OapExecution, text: []const u8) !OwnedSlice(u8) {
        return OwnedSlice(u8).initOwned(try self.allocator.dupe(u8, text));
    }

    fn translateLine(self: *OapExecution, line: []const u8) !void {
        var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, line, .{}) catch return;
        defer parsed.deinit();
        if (parsed.value != .object) return;
        const root = parsed.value.object;
        if (stringOf(root, "control")) |control| {
            if (!std.mem.eql(u8, control, "stream.lost")) return;
            const message = stringOf(root, "message") orelse "this run's events stopped reaching the terminal UI";
            self.deliver(.{ .system_warning = .{ .message = try self.ownedText(message) } });
            if (self.turn_open.load(.acquire)) {
                self.sendCancel() catch {};
                try self.closeAssistant(.@"error");
                self.endTurn(.@"error");
            }
            return;
        }
        const kind = stringOf(root, "type") orelse return;
        const payload = if (root.get("payload")) |value| (if (value == .object) value.object else null) else null;
        if (std.mem.eql(u8, kind, "error.response")) {
            const reply_to = stringOf(root, "in_reply_to") orelse "";
            const message = if (payload) |body| errorMessage(body) else "the endpoint refused the request";
            if (std.mem.startsWith(u8, reply_to, "submit-")) {
                self.deliver(.{ .@"error" = .{ .message = try self.ownedText(message) } });
                self.endTurn(.@"error");
            } else {
                self.deliver(.{ .system_warning = .{ .message = try self.ownedText(message) } });
            }
            return;
        }
        const body = payload orelse return;
        if (std.mem.eql(u8, kind, "run.started")) {
            const run_id = stringOf(body, "run_id") orelse "";
            const kept = try self.allocator.dupe(u8, run_id);
            self.lockInbound();
            self.allocator.free(self.run_id);
            self.run_id = kept;
            self.inbound_mutex.unlock();
            self.text.clearRetainingCapacity();
            self.in_assistant = false;
            self.deliver(.{ .agent_start = .{} });
            self.deliver(.{ .turn_start = .{} });
            if (self.cancel_pending.swap(false, .acq_rel)) try self.sendCancel();
            return;
        }
        if (std.mem.eql(u8, kind, "content.delta")) {
            const part = body.get("part") orelse return;
            if (part != .object) return;
            const part_type = stringOf(part.object, "type") orelse return;
            if (std.mem.eql(u8, part_type, "text")) {
                const text = stringOf(part.object, "text") orelse return;
                self.openAssistant();
                try self.text.appendSlice(self.allocator, text);
                self.deliver(.{ .text_delta = .{ .content_index = 0, .delta = try self.ownedText(text) } });
            } else if (std.mem.eql(u8, part_type, "reasoning")) {
                const text = stringOf(part.object, "reasoning") orelse return;
                self.openAssistant();
                self.deliver(.{ .thinking_delta = .{ .content_index = 0, .delta = try self.ownedText(text) } });
            }
            return;
        }
        if (std.mem.eql(u8, kind, "action.call.requested")) {
            try self.closeAssistant(.tool_use);
            const arguments = try jsonText(self.allocator, body.get("arguments_json"));
            defer self.allocator.free(arguments);
            var call_id = try self.ownedText(stringOf(body, "tool_call_id") orelse "");
            errdefer call_id.deinit(self.allocator);
            var name = try self.ownedText(stringOf(body, "name") orelse "");
            errdefer name.deinit(self.allocator);
            const args_json = try self.ownedText(arguments);
            self.deliver(.{ .tool_execution_start = .{ .tool_call_id = call_id, .tool_name = name, .args_json = args_json } });
            return;
        }
        if (std.mem.eql(u8, kind, "action.call.completed") or std.mem.eql(u8, kind, "action.call.failed")) {
            const failed = std.mem.eql(u8, kind, "action.call.failed");
            const result = if (failed)
                try jsonText(self.allocator, if (body.get("error")) |value| (if (value == .object) value.object.get("message") else null) else null)
            else
                try jsonText(self.allocator, body.get("result"));
            defer self.allocator.free(result);
            var call_id = try self.ownedText(stringOf(body, "tool_call_id") orelse "");
            errdefer call_id.deinit(self.allocator);
            var name = try self.ownedText(stringOf(body, "name") orelse "");
            errdefer name.deinit(self.allocator);
            const result_json = try self.ownedText(result);
            self.deliver(.{ .tool_execution_end = .{ .tool_call_id = call_id, .tool_name = name, .result_json = result_json, .is_error = failed } });
            return;
        }
        if (std.mem.eql(u8, kind, "run.completed")) {
            if (!self.turn_open.load(.acquire)) return;
            const stop_reason = stopReason(stringOf(body, "stop_reason") orelse "end_turn");
            if (!self.in_assistant and self.text.items.len == 0) {
                if (body.get("final_response")) |final| {
                    if (final == .object) {
                        if (stringOf(final.object, "content")) |content| {
                            if (content.len > 0) {
                                self.openAssistant();
                                try self.text.appendSlice(self.allocator, content);
                                self.deliver(.{ .text_delta = .{ .content_index = 0, .delta = try self.ownedText(content) } });
                            }
                        }
                    }
                }
            }
            try self.closeAssistant(stop_reason);
            self.deliver(.{ .turn_end = .{ .stop_reason = stop_reason } });
            self.endTurn(.completed);
            return;
        }
        if (std.mem.eql(u8, kind, "run.failed")) {
            if (!self.turn_open.load(.acquire)) return;
            try self.closeAssistant(.@"error");
            const message = if (body.get("error")) |value| (if (value == .object) errorMessage(value.object) else "the run failed") else "the run failed";
            self.deliver(.{ .@"error" = .{ .message = try self.ownedText(message) } });
            self.endTurn(.@"error");
            return;
        }
        if (std.mem.eql(u8, kind, "run.cancelled")) {
            if (!self.turn_open.load(.acquire)) return;
            try self.closeAssistant(.aborted);
            self.endTurn(.cancelled);
            return;
        }
    }

    fn endTurn(self: *OapExecution, reason: tui_session.TuiEndReason) void {
        self.turn_open.store(false, .release);
        self.deliver(.{ .agent_end = .{ .reason = reason } });
    }

    fn openAssistant(self: *OapExecution) void {
        if (self.in_assistant) return;
        self.in_assistant = true;
        self.text.clearRetainingCapacity();
        self.deliver(.{ .message_start = .{ .role = .assistant } });
    }

    fn closeAssistant(self: *OapExecution, stop_reason: ai_types.StopReason) !void {
        if (!self.in_assistant) return;
        self.in_assistant = false;
        self.deliver(.{ .message_end = .{ .role = .assistant, .text = try self.ownedText(self.text.items), .stop_reason = stop_reason } });
        self.text.clearRetainingCapacity();
    }
};

const Map = struct {
    allocator: std.mem.Allocator,
    map: std.json.ObjectMap = .empty,

    fn init(allocator: std.mem.Allocator) Map {
        return .{ .allocator = allocator };
    }

    fn put(self: *Map, key: []const u8, member: std.json.Value) !void {
        try self.map.put(self.allocator, key, member);
    }

    fn value(self: *Map) std.json.Value {
        return .{ .object = self.map };
    }
};

fn strings(a: std.mem.Allocator, items: []const []const u8) !std.json.Value {
    var array = std.json.Array.init(a);
    for (items) |item| try array.append(.{ .string = item });
    return .{ .array = array };
}

fn modelRef(a: std.mem.Allocator, model: ai_types.Model) ![]u8 {
    return model_ref.formatModelRef(a, model.provider, model.api, model.id);
}

fn stringOf(object: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = object.get(key) orelse return null;
    return if (value == .string) value.string else null;
}

fn errorMessage(body: std.json.ObjectMap) []const u8 {
    if (body.get("error")) |value| {
        if (value == .object) {
            if (stringOf(value.object, "message")) |message| return message;
        }
    }
    return stringOf(body, "message") orelse "the endpoint refused the request";
}

fn jsonText(allocator: std.mem.Allocator, value: ?std.json.Value) ![]u8 {
    const present = value orelse return allocator.dupe(u8, "");
    if (present == .string) return allocator.dupe(u8, present.string);
    return json_encode.valueAlloc(allocator, present);
}

fn stopReason(text: []const u8) ai_types.StopReason {
    if (std.mem.eql(u8, text, "max_tokens")) return .length;
    if (std.mem.eql(u8, text, "tool_use")) return .tool_use;
    if (std.mem.eql(u8, text, "content_filter")) return .content_filter;
    if (std.mem.eql(u8, text, "error")) return .@"error";
    if (std.mem.eql(u8, text, "cancelled")) return .aborted;
    return .stop;
}

const testing = std.testing;
const agent = @import("agent");
const event_stream = @import("event_stream");

const scripted_model = ai_types.Model{
    .id = "scripted-model",
    .name = "Scripted Model",
    .api = "openai-completions",
    .provider = "scripted",
    .base_url = "http://127.0.0.1:1",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 8192,
    .max_tokens = 1024,
};

const Script = struct {
    reply: []const u8 = "over the wire",
    wait_for_cancel: bool = false,
    last_thinking: ai_types.ThinkingLevel = .off,
};

fn scriptedMessage(allocator: std.mem.Allocator, text: []const u8, reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const blocks = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(blocks);
    blocks[0] = .{ .text = .{ .text = try allocator.dupe(u8, text) } };
    return .{ .content = blocks, .api = scripted_model.api, .provider = scripted_model.provider, .model = scripted_model.id, .usage = .{}, .stop_reason = reason, .timestamp = 0 };
}

fn bareMessage(reason: ai_types.StopReason) ai_types.AssistantMessage {
    return .{ .content = &.{}, .api = scripted_model.api, .provider = scripted_model.provider, .model = scripted_model.id, .usage = .{}, .stop_reason = reason, .timestamp = 0 };
}

fn scriptedStream(ctx: ?*anyopaque, model: ai_types.Model, context: ai_types.Context, options: agent.ProtocolOptions, allocator: std.mem.Allocator) anyerror!*event_stream.AssistantMessageEventStream {
    _ = model;
    _ = context;
    const script: *Script = @ptrCast(@alignCast(ctx.?));
    script.last_thinking = options.thinking_level;
    const stream = try allocator.create(event_stream.AssistantMessageEventStream);
    stream.* = event_stream.AssistantMessageEventStream.init(allocator);
    if (script.wait_for_cancel) {
        if (options.cancel_token) |token| {
            var waits: usize = 0;
            while (!token.isCancelled() and waits < 2000) : (waits += 1) {
                std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
            }
        }
        try stream.push(.{ .done = .{ .reason = .aborted, .message = bareMessage(.aborted) } });
        stream.complete(bareMessage(.aborted));
        return stream;
    }
    const partial = bareMessage(.stop);
    try stream.push(.{ .start = .{ .partial = partial } });
    try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = script.reply, .partial = partial } });
    try stream.push(.{ .done = .{ .reason = .stop, .message = try scriptedMessage(allocator, script.reply, .stop) } });
    stream.complete(try scriptedMessage(allocator, script.reply, .stop));
    return stream;
}

const Seen = struct {
    text: std.ArrayList(u8) = .empty,
    final_text: std.ArrayList(u8) = .empty,
    end: ?tui_session.TuiEndReason = null,
    agent_starts: usize = 0,

    fn deinit(self: *Seen) void {
        self.text.deinit(testing.allocator);
        self.final_text.deinit(testing.allocator);
    }
};

fn drainTurn(runtime: *tui_runtime.TuiRuntime, seen: *Seen) !void {
    var waits: usize = 0;
    while (waits < 5000) : (waits += 1) {
        while (runtime.streamEvents().poll()) |event| {
            var owned_event = event;
            defer owned_event.deinit(testing.allocator);
            switch (owned_event) {
                .agent_start => seen.agent_starts += 1,
                .text_delta => |payload| try seen.text.appendSlice(testing.allocator, payload.delta.slice()),
                .message_end => |payload| if (payload.role == .assistant) {
                    seen.final_text.clearRetainingCapacity();
                    try seen.final_text.appendSlice(testing.allocator, payload.text.slice());
                },
                .agent_end => |payload| {
                    seen.end = payload.reason;
                    return;
                },
                else => {},
            }
        }
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    return error.TestTurnNeverEnded;
}

fn remoteRuntime(script: *Script, execution: **OapExecution, thinking: ai_types.ThinkingLevel) !tui_runtime.TuiRuntime {
    const models = [_]ai_types.Model{scripted_model};
    const engine_options = tui_runtime.TuiRuntimeOptions{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    };
    execution.* = try OapExecution.create(testing.allocator, engine_options);
    return tui_runtime.TuiRuntime.init(testing.allocator, .{
        .models = &models,
        .initial_model_id = scripted_model.id,
        .thinking_level = thinking,
        .remote = execution.*.remote(),
    });
}

test "a turn submitted to a runtime over OAP streams back as the events the terminal UI renders" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .high);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("hello");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);

    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(usize, 1), seen.agent_starts);
    try testing.expectEqualStrings("over the wire", seen.text.items);
    try testing.expectEqualStrings("over the wire", seen.final_text.items);
    try testing.expectEqual(ai_types.ThinkingLevel.high, script.last_thinking);
    try testing.expect(runtime.isIdle());
}

test "cancelling a turn over OAP ends it cancelled and the next turn runs on the same session" {
    var script = Script{ .wait_for_cancel = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("wait");
    var waits: usize = 0;
    while (waits < 2000) : (waits += 1) {
        execution.lockInbound();
        const known = execution.run_id.len > 0;
        execution.inbound_mutex.unlock();
        if (known) break;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    runtime.cancel();
    var cancelled = Seen{};
    defer cancelled.deinit();
    try drainTurn(&runtime, &cancelled);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .cancelled), cancelled.end);

    script.wait_for_cancel = false;
    try runtime.submitTurn("again");
    var next = Seen{};
    defer next.deinit();
    try drainTurn(&runtime, &next);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), next.end);
}

test "a runtime over OAP refuses what the protocol path cannot carry yet" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try testing.expectError(error.UnavailableOverOap, runtime.steer("x"));
    try testing.expectError(error.UnavailableOverOap, runtime.followUp("x"));
    try testing.expectError(error.UnavailableOverOap, runtime.compact(.{}));
    try testing.expectError(error.UnavailableOverOap, runtime.resumeSession());
}

test "a submit the endpoint cannot frame ends the turn in an error instead of leaving it streaming" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    const oversized = try testing.allocator.alloc(u8, in_process_frame_limit + 1);
    defer testing.allocator.free(oversized);
    @memset(oversized, 'x');
    try runtime.submitTurn(oversized);
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .@"error"), seen.end);
    try testing.expect(runtime.isIdle());

    try runtime.submitTurn("after");
    var next = Seen{};
    defer next.deinit();
    try drainTurn(&runtime, &next);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), next.end);
}

test "once a session over OAP is open, settings the protocol cannot carry are refused rather than changed locally" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.setThinkingLevel(.high);
    try runtime.start();
    try testing.expectError(error.UnavailableOverOap, runtime.setThinkingLevel(.max));
    try testing.expectError(error.UnavailableOverOap, runtime.setPermissionMode(.ask));
    try testing.expectError(error.UnavailableOverOap, runtime.setContextWindow(4096));
    try testing.expectError(error.UnavailableOverOap, runtime.setOutput(.max));
    try testing.expectError(error.UnavailableOverOap, runtime.setWorkspaceRoot("/elsewhere"));
    try testing.expectEqual(ai_types.ThinkingLevel.high, runtime.thinkingLevel());
    try testing.expectEqual(tui_runtime.PermissionMode.bypass, runtime.permissionMode());

    try runtime.submitTurn("go");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(ai_types.ThinkingLevel.high, script.last_thinking);
}

test "an open-time model the OAP session does not list leaves the session usable on its own default" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    var missing = scripted_model;
    missing.id = "missing-model";
    const app_models = [_]ai_types.Model{ scripted_model, missing };
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var runtime = try tui_runtime.TuiRuntime.init(testing.allocator, .{
        .models = &app_models,
        .initial_model_id = missing.id,
        .remote = execution.remote(),
    });
    defer runtime.deinit();

    try runtime.submitTurn("still works");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
}

test "ask mode is refused over OAP even before the session opens" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try testing.expectError(error.UnavailableOverOap, runtime.setPermissionMode(.ask));
    try runtime.setPermissionMode(.bypass);
}

test "a switch to a model the OAP session does not list is refused before the app's selection moves" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    var unlisted = scripted_model;
    unlisted.id = "unlisted-model";
    const app_models = [_]ai_types.Model{ scripted_model, unlisted };
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var runtime = try tui_runtime.TuiRuntime.init(testing.allocator, .{
        .models = &app_models,
        .initial_model_id = scripted_model.id,
        .remote = execution.remote(),
    });
    defer runtime.deinit();
    try runtime.start();

    try testing.expectError(error.ModelNotFound, runtime.switchModel("unlisted-model"));
    try testing.expectEqualStrings(scripted_model.id, runtime.currentModel().?.id);
    try testing.expectError(error.ModelNotFound, runtime.replaceModels(&.{unlisted}, null));
    try testing.expectEqualStrings(scripted_model.id, runtime.currentModel().?.id);
}

test "a lost stream warns and ends the turn instead of leaving it to stream forever" {
    var script = Script{ .wait_for_cancel = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try runtime.submitTurn("wait");
    var waits: usize = 0;
    while (waits < 2000 and !execution.turn_open.load(.acquire)) : (waits += 1) {
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    try execution.translateLine("{\"control\":\"stream.lost\",\"run_id\":\"r\",\"after\":1,\"code\":\"frame_limit\",\"message\":\"lost\"}");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .@"error"), seen.end);
}

test "a cancel that lands before the run has started is held and sent once it starts" {
    var script = Script{ .wait_for_cancel = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try runtime.submitTurn("wait");
    runtime.cancel();
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .cancelled), seen.end);
}

test "a stopped execution restarts with a live pump" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try runtime.start();
    runtime.stop();
    try runtime.submitTurn("after a restart");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
}

test "a model refresh during a turn over OAP is refused rather than switching the running session" {
    var script = Script{ .wait_for_cancel = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try runtime.submitTurn("wait");
    try testing.expect(!runtime.isIdle());
    try testing.expectError(error.AgentAlreadyStreaming, runtime.replaceModels(&.{scripted_model}, null));
    runtime.cancel();
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
}
