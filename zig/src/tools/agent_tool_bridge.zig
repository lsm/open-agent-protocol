const std = @import("std");
const agent_types = @import("agent_types");
const ai_types = @import("ai_types");
const agent_loop = @import("agent_loop");
const compat = @import("compat");

const IDLE_SLEEP_NS = std.time.ns_per_ms;

pub const SessionId = agent_types.SessionId;
pub const Ulid = agent_types.Ulid;

pub const Request = struct {
    session_id: SessionId,
    generation: u64,
    tool_call_id: []u8,
    tool_name: []u8,
    args_json: []u8,

    pub fn deinit(self: *Request, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.tool_name);
        allocator.free(self.args_json);
        self.* = undefined;
    }
};

pub const Key = struct {
    session_id: SessionId,
    tool_call_id: []u8,
    request_message_id: Ulid,
    generation: u64,

    pub fn deinit(self: *Key, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        self.* = undefined;
    }
};

pub const Result = struct {
    session_id: SessionId,
    tool_call_id: []u8,
    in_reply_to: ?Ulid,
    result_json: []u8,
    details_json: []u8,
    is_error: bool,

    pub fn deinit(self: *Result, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.result_json);
        allocator.free(self.details_json);
        self.* = undefined;
    }
};

pub const Bridge = struct {
    mutex: std.atomic.Mutex = .unlocked,
    requests: std.ArrayList(Request) = .empty,
    in_flight: std.ArrayList(Key) = .empty,
    results: std.ArrayList(Result) = .empty,
    disconnected: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),

    pub fn markDisconnected(self: *Bridge) void {
        self.disconnected.store(true, .release);
    }

    pub fn isDisconnected(self: *Bridge) bool {
        return self.disconnected.load(.acquire);
    }

    pub fn deinit(self: *Bridge, allocator: std.mem.Allocator) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        for (self.requests.items) |*request| request.deinit(allocator);
        self.requests.deinit(allocator);
        for (self.in_flight.items) |*key| key.deinit(allocator);
        self.in_flight.deinit(allocator);
        for (self.results.items) |*result| result.deinit(allocator);
        self.results.deinit(allocator);
        self.mutex.unlock();
        self.* = undefined;
    }

    pub fn enqueueRequest(
        self: *Bridge,
        allocator: std.mem.Allocator,
        session_id: SessionId,
        generation: u64,
        tool_call_id: []const u8,
        tool_name: []const u8,
        args_json: []const u8,
    ) !void {
        const owned_tool_call_id = try allocator.dupe(u8, tool_call_id);
        errdefer allocator.free(owned_tool_call_id);
        const owned_tool_name = try allocator.dupe(u8, tool_name);
        errdefer allocator.free(owned_tool_name);
        const owned_args_json = try allocator.dupe(u8, args_json);
        errdefer allocator.free(owned_args_json);

        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        try self.requests.append(allocator, .{
            .session_id = session_id,
            .generation = generation,
            .tool_call_id = owned_tool_call_id,
            .tool_name = owned_tool_name,
            .args_json = owned_args_json,
        });
    }

    pub fn peekFrontRequest(self: *Bridge) ?Request {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        if (self.requests.items.len == 0) return null;
        return self.requests.items[0];
    }

    pub fn popFrontRequest(self: *Bridge, allocator: std.mem.Allocator) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        if (self.requests.items.len == 0) return;
        var removed = self.requests.orderedRemove(0);
        removed.deinit(allocator);
    }

    pub fn markInFlight(self: *Bridge, allocator: std.mem.Allocator, session_id: SessionId, tool_call_id: []const u8, request_message_id: Ulid, generation: u64) !void {
        const owned_tool_call_id = try allocator.dupe(u8, tool_call_id);
        errdefer allocator.free(owned_tool_call_id);
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        self.removeInFlightLocked(allocator, session_id, tool_call_id);
        self.removeResultsLocked(allocator, session_id, tool_call_id);
        try self.in_flight.append(allocator, .{
            .session_id = session_id,
            .tool_call_id = owned_tool_call_id,
            .request_message_id = request_message_id,
            .generation = generation,
        });
    }

    pub fn enqueueResult(self: *Bridge, allocator: std.mem.Allocator, result: Result) !bool {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        const in_flight_key = self.findInFlightLocked(result.session_id, result.tool_call_id) orelse return false;
        const reply_to = result.in_reply_to orelse return false;
        if (!std.mem.eql(u8, &reply_to, &in_flight_key.request_message_id)) return false;
        if (self.hasResultLocked(result.session_id, result.tool_call_id)) return false;
        try self.results.append(allocator, result);
        return true;
    }

    pub fn popResult(self: *Bridge, allocator: std.mem.Allocator, session_id: SessionId, tool_call_id: []const u8, generation: u64) ?Result {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        const key_idx = self.findInFlightIndexForGenerationLocked(session_id, tool_call_id, generation) orelse return null;
        const key = self.in_flight.items[key_idx];
        for (self.results.items, 0..) |result, idx| {
            if (std.mem.eql(u8, &result.session_id, &session_id) and std.mem.eql(u8, result.tool_call_id, tool_call_id)) {
                const reply_to = result.in_reply_to orelse continue;
                if (!std.mem.eql(u8, &reply_to, &key.request_message_id)) continue;
                var removed_key = self.in_flight.orderedRemove(key_idx);
                removed_key.deinit(allocator);
                return self.results.orderedRemove(idx);
            }
        }
        return null;
    }

    pub fn discardInFlight(self: *Bridge, allocator: std.mem.Allocator, session_id: SessionId, tool_call_id: []const u8) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        self.removeInFlightLocked(allocator, session_id, tool_call_id);
    }

    pub fn discardSession(self: *Bridge, allocator: std.mem.Allocator, session_id: SessionId) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        var request_idx: usize = 0;
        while (request_idx < self.requests.items.len) {
            if (std.mem.eql(u8, &self.requests.items[request_idx].session_id, &session_id)) {
                var removed = self.requests.orderedRemove(request_idx);
                removed.deinit(allocator);
                continue;
            }
            request_idx += 1;
        }
        var in_flight_idx: usize = 0;
        while (in_flight_idx < self.in_flight.items.len) {
            if (std.mem.eql(u8, &self.in_flight.items[in_flight_idx].session_id, &session_id)) {
                var removed = self.in_flight.orderedRemove(in_flight_idx);
                removed.deinit(allocator);
                continue;
            }
            in_flight_idx += 1;
        }
        var result_idx: usize = 0;
        while (result_idx < self.results.items.len) {
            if (std.mem.eql(u8, &self.results.items[result_idx].session_id, &session_id)) {
                var removed = self.results.orderedRemove(result_idx);
                removed.deinit(allocator);
                continue;
            }
            result_idx += 1;
        }
    }

    fn findInFlightLocked(self: *Bridge, session_id: SessionId, tool_call_id: []const u8) ?Key {
        for (self.in_flight.items) |key| {
            if (std.mem.eql(u8, &key.session_id, &session_id) and std.mem.eql(u8, key.tool_call_id, tool_call_id)) return key;
        }
        return null;
    }

    fn findInFlightIndexForGenerationLocked(self: *Bridge, session_id: SessionId, tool_call_id: []const u8, generation: u64) ?usize {
        for (self.in_flight.items, 0..) |key, idx| {
            if (key.generation == generation and std.mem.eql(u8, &key.session_id, &session_id) and std.mem.eql(u8, key.tool_call_id, tool_call_id)) return idx;
        }
        return null;
    }

    fn hasResultLocked(self: *Bridge, session_id: SessionId, tool_call_id: []const u8) bool {
        for (self.results.items) |result| {
            if (std.mem.eql(u8, &result.session_id, &session_id) and std.mem.eql(u8, result.tool_call_id, tool_call_id)) return true;
        }
        return false;
    }

    fn removeInFlightLocked(self: *Bridge, allocator: std.mem.Allocator, session_id: SessionId, tool_call_id: []const u8) void {
        var idx: usize = 0;
        while (idx < self.in_flight.items.len) {
            if (std.mem.eql(u8, &self.in_flight.items[idx].session_id, &session_id) and std.mem.eql(u8, self.in_flight.items[idx].tool_call_id, tool_call_id)) {
                var removed = self.in_flight.orderedRemove(idx);
                removed.deinit(allocator);
                continue;
            }
            idx += 1;
        }
    }

    fn removeResultsLocked(self: *Bridge, allocator: std.mem.Allocator, session_id: SessionId, tool_call_id: []const u8) void {
        var idx: usize = 0;
        while (idx < self.results.items.len) {
            if (std.mem.eql(u8, &self.results.items[idx].session_id, &session_id) and std.mem.eql(u8, self.results.items[idx].tool_call_id, tool_call_id)) {
                var removed = self.results.orderedRemove(idx);
                removed.deinit(allocator);
                continue;
            }
            idx += 1;
        }
    }
};

pub const Executor = struct {
    bridge: *Bridge,
    session_id: SessionId,
    generation: u64,
    disconnect_failed: *std.atomic.Value(bool),
};

pub fn executeViaAgentProtocol(
    ctx: ?*anyopaque,
    tool_call_id: []const u8,
    tool_name: []const u8,
    args_json: []const u8,
    cancel_token: ?ai_types.CancelToken,
    on_update_ctx: ?*anyopaque,
    on_update: ?*const fn (?*anyopaque, []const u8, []const u8, []const u8) void,
    allocator: std.mem.Allocator,
) anyerror!agent_loop.AgentToolResult {
    _ = on_update_ctx;
    _ = on_update;
    const executor: *Executor = @ptrCast(@alignCast(ctx.?));
    try executor.bridge.enqueueRequest(allocator, executor.session_id, executor.generation, tool_call_id, tool_name, args_json);

    const popAndBuild = struct {
        fn run(
            bridge: *Bridge,
            alloc: std.mem.Allocator,
            pop_session_id: SessionId,
            pop_tool_call_id: []const u8,
            pop_generation: u64,
        ) !?agent_loop.AgentToolResult {
            const result = bridge.popResult(alloc, pop_session_id, pop_tool_call_id, pop_generation) orelse return null;
            var owned_result = result;
            defer owned_result.deinit(alloc);
            const content = try parseToolResultContentPartsJson(alloc, owned_result.result_json);
            errdefer deinitUserContentParts(alloc, content);
            const details_json = try alloc.dupe(u8, owned_result.details_json);
            errdefer alloc.free(details_json);
            return .{
                .content = ai_types.OwnedSlice(ai_types.UserContentPart).initOwned(content),
                .details_json = ai_types.OwnedSlice(u8).initOwned(details_json),
                .is_error = owned_result.is_error,
            };
        }
    }.run;

    while (true) {
        if (cancel_token) |token| {
            if (token.isCancelled()) return error.Cancelled;
        }
        if (try popAndBuild(executor.bridge, allocator, executor.session_id, tool_call_id, executor.generation)) |tool_result| return tool_result;
        if (executor.bridge.isDisconnected()) {
            if (try popAndBuild(executor.bridge, allocator, executor.session_id, tool_call_id, executor.generation)) |tool_result| return tool_result;
            executor.disconnect_failed.store(true, .release);
            if (cancel_token) |token| token.cancelled.store(true, .release);
            return error.ClientDisconnected;
        }
        compat.time.sleepNs(IDLE_SLEEP_NS);
    }
}

pub fn parseToolResultContentParts(allocator: std.mem.Allocator, value: std.json.Value) ![]ai_types.UserContentPart {
    var parts = std.ArrayList(ai_types.UserContentPart).empty;
    errdefer {
        for (parts.items) |*part| part.deinit(allocator);
        parts.deinit(allocator);
    }

    try appendToolResultContentParts(allocator, &parts, value);
    if (parts.items.len == 0) try appendTextContentPart(allocator, &parts, "");
    return parts.toOwnedSlice(allocator);
}

fn parseToolResultContentPartsJson(allocator: std.mem.Allocator, result_json: []const u8) ![]ai_types.UserContentPart {
    if (result_json.len == 0) {
        const content = try allocator.alloc(ai_types.UserContentPart, 0);
        return content;
    }

    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, result_json, .{});
    defer parsed.deinit();
    return parseToolResultContentParts(allocator, parsed.value);
}

fn deinitUserContentParts(allocator: std.mem.Allocator, parts: []ai_types.UserContentPart) void {
    for (parts) |*part| part.deinit(allocator);
    allocator.free(parts);
}

fn appendToolResultContentParts(
    allocator: std.mem.Allocator,
    parts: *std.ArrayList(ai_types.UserContentPart),
    value: std.json.Value,
) !void {
    switch (value) {
        .string => |text| try appendTextContentPart(allocator, parts, text),
        .array => |array| {
            for (array.items) |item| {
                try appendToolResultContentParts(allocator, parts, item);
            }
        },
        .object => |obj| {
            if (std.mem.eql(u8, getStringField(obj, "type") orelse "", "tool_result")) {
                if (obj.get("content")) |content| {
                    try appendToolResultContentParts(allocator, parts, content);
                }
                return;
            }

            if (try parseUserContentPart(allocator, value)) |part| {
                parts.append(allocator, part) catch |err| {
                    var owned = part;
                    owned.deinit(allocator);
                    return err;
                };
            }
        },
        else => {},
    }
}

fn appendTextContentPart(
    allocator: std.mem.Allocator,
    parts: *std.ArrayList(ai_types.UserContentPart),
    text: []const u8,
) !void {
    const owned = try allocator.dupe(u8, text);
    errdefer allocator.free(owned);
    try parts.append(allocator, .{ .text = .{ .text = owned } });
}

pub fn parseUserContentPart(allocator: std.mem.Allocator, value: std.json.Value) !?ai_types.UserContentPart {
    if (value != .object) return null;
    const obj = value.object;
    const ty = getStringField(obj, "type") orelse return null;
    if (std.mem.eql(u8, ty, "text")) {
        return .{ .text = .{
            .text = try allocator.dupe(u8, getStringField(obj, "text") orelse ""),
        } };
    }
    if (std.mem.eql(u8, ty, "image")) {
        const data = try allocator.dupe(u8, getStringField(obj, "data") orelse "");
        errdefer allocator.free(data);
        const mime_type = try allocator.dupe(u8, getStringField(obj, "mime_type") orelse "application/octet-stream");
        errdefer allocator.free(mime_type);
        return .{ .image = .{
            .data = data,
            .mime_type = mime_type,
        } };
    }
    return null;
}

pub fn getStringField(obj: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    return if (value == .string) value.string else null;
}
