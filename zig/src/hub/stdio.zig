const std = @import("std");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");
const json_encode = @import("json_encode");
const contract = @import("contract");
const hubmod = @import("hub");

pub const Hub = hubmod.Hub;

pub const default_frame_limit: usize = 16 * 1024 * 1024 + 2 * 1024 * 1024;
pub const minimum_frame_limit: usize = 256;
pub const default_max_ops: usize = 16;
pub const default_max_subscriptions: usize = 64;
pub const message_limit: usize = 300;

pub const op_adapters = "adapters";
pub const op_capabilities = "capabilities";
pub const op_sessions = "sessions";
pub const op_state = "state";
pub const op_models = "models";
pub const op_tools = "tools";
pub const op_open = "open";
pub const op_submit = "submit";
pub const op_resolve = "resolve";
pub const op_cancel = "cancel";
pub const op_close = "close";
pub const op_events = "events";

pub const Error = error{
    MalformedLine,
    FrameLimitTooSmall,
    OutputStalled,
    StdinFailed,
    BackendRefused,
    ResponseTooLarge,
    NoSpaceLeft,
    MalformedJson,
    MissingPayload,
    UnreadablePayload,
} || std.mem.Allocator.Error || hubmod.Failure;

pub const Sink = struct {
    context: *anyopaque,
    write: *const fn (context: *anyopaque, line: []const u8) anyerror!void,
};

pub const Refusal = struct {
    code: []const u8 = "internal",
    message: []const u8 = "",
    details: []const oap_types.DetailEntry = &.{},
};

pub const Supplied = struct {
    adapter: bool = false,
    session_id: bool = false,
    run_id: bool = false,
    after: bool = false,
    request: bool = false,
    allow_degraded_features: bool = false,
};

const no_parameters: []const []const u8 = &.{};
const adapter_parameter: []const []const u8 = &.{"adapter"};
const session_parameter: []const []const u8 = &.{"session_id"};
const all_parameters = [_][]const u8{ "adapter", "session_id", "run_id", "after", "request", "allow_degraded_features" };

pub const Request = struct {
    id: i64,
    op: []const u8,
    adapter: ?[]const u8 = null,
    session_id: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
    after: ?u64 = null,
    payload: ?std.json.Value = null,
    allow_degraded_features: []const []const u8 = &.{},
    supplied: Supplied = .{},

    fn takes(self: Request, parameter: []const u8) bool {
        if (std.mem.eql(u8, parameter, "adapter")) return self.supplied.adapter;
        if (std.mem.eql(u8, parameter, "session_id")) return self.supplied.session_id;
        if (std.mem.eql(u8, parameter, "run_id")) return self.supplied.run_id;
        if (std.mem.eql(u8, parameter, "after")) return self.supplied.after;
        if (std.mem.eql(u8, parameter, "request")) return self.supplied.request;
        if (std.mem.eql(u8, parameter, "allow_degraded_features")) return self.supplied.allow_degraded_features;
        unreachable;
    }

    fn only(self: Request, parameters: []const []const u8) ?Refusal {
        for (all_parameters) |parameter| {
            if (!self.takes(parameter)) continue;
            if (declares(parameters, parameter)) continue;
            return .{ .code = "invalid_request", .message = "this op does not define that parameter" };
        }
        return null;
    }
};

fn emptyObject(arena: std.mem.Allocator) std.mem.Allocator.Error!std.json.ObjectMap {
    if (std.json.ObjectMap.init(arena, &.{}, &.{})) |ready| {
        return ready;
    } else |_| {
        return error.OutOfMemory;
    }
}

fn jsonArray(arena: std.mem.Allocator, items: []const std.json.Value) std.mem.Allocator.Error!std.json.Value {
    var list: std.json.Array = undefined;
    if (std.json.Array.initCapacity(arena, items.len)) |ready| {
        list = ready;
    } else |_| {
        return error.OutOfMemory;
    }
    list.appendSliceAssumeCapacity(items);
    return .{ .array = list };
}

fn jsonStrings(arena: std.mem.Allocator, items: []const []const u8) !std.json.Value {
    const values = try arena.alloc(std.json.Value, items.len);
    for (items, values) |item, *slot| slot.* = .{ .string = item };
    return jsonArray(arena, values);
}

fn declares(parameters: []const []const u8, parameter: []const u8) bool {
    for (parameters) |candidate| {
        if (std.mem.eql(u8, candidate, parameter)) return true;
    }
    return false;
}

fn member(root: std.json.ObjectMap, name: []const u8) ?std.json.Value {
    return root.get(name);
}

fn stringMember(root: std.json.ObjectMap, name: []const u8) ?[]const u8 {
    const value = member(root, name) orelse return null;
    if (value != .string) return null;
    return value.string;
}

pub fn decode(arena: std.mem.Allocator, line: []const u8) Error!Request {
    if (line.len == 0) return Error.MalformedLine;
    if (std.mem.indexOfScalar(u8, line, '\r') != null) return Error.MalformedLine;
    if (!std.unicode.utf8ValidateSlice(line)) return Error.MalformedLine;
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return Error.MalformedLine;
    };
    if (document != .object) return Error.MalformedLine;
    const root = document.object;
    for (root.keys()) |key| {
        if (!allowed(key)) return Error.MalformedLine;
    }
    const id_value = member(root, "id") orelse return Error.MalformedLine;
    if (id_value != .integer) return Error.MalformedLine;
    if (id_value.integer < std.math.minInt(i64) or id_value.integer > std.math.maxInt(i64)) return Error.MalformedLine;
    const op = stringMember(root, "op") orelse return Error.MalformedLine;
    if (op.len == 0) return Error.MalformedLine;
    var request = Request{ .id = @intCast(id_value.integer), .op = op };
    request.supplied.adapter = member(root, "adapter") != null;
    request.supplied.session_id = member(root, "session_id") != null;
    request.supplied.run_id = member(root, "run_id") != null;
    request.supplied.after = member(root, "after") != null;
    request.supplied.request = member(root, "request") != null;
    request.supplied.allow_degraded_features = member(root, "allow_degraded_features") != null;
    request.adapter = stringMember(root, "adapter");
    request.session_id = stringMember(root, "session_id");
    request.run_id = stringMember(root, "run_id");
    if (request.supplied.after) {
        const after = member(root, "after").?;
        if (after != .null) {
            if (after != .integer) return Error.MalformedLine;
            if (after.integer < 0 or after.integer > std.math.maxInt(u64)) return Error.MalformedLine;
            request.after = @intCast(after.integer);
        }
    }
    if (request.supplied.request) request.payload = member(root, "request");
    if (request.supplied.allow_degraded_features) {
        const degraded = member(root, "allow_degraded_features").?;
        if (degraded != .array) return Error.MalformedLine;
        const keys = try arena.alloc([]const u8, degraded.array.items.len);
        for (degraded.array.items, keys) |item, *slot| {
            if (item != .string) return Error.MalformedLine;
            slot.* = item.string;
        }
        request.allow_degraded_features = keys;
    }
    return request;
}

fn allowed(key: []const u8) bool {
    const names = [_][]const u8{ "id", "op", "adapter", "session_id", "run_id", "after", "request", "allow_degraded_features" };
    for (names) |name| {
        if (std.mem.eql(u8, key, name)) return true;
    }
    return false;
}

pub const Options = struct {
    frame_limit: usize = default_frame_limit,
    max_ops: usize = default_max_ops,
    max_subscriptions: usize = default_max_subscriptions,
};

pub const Watch = struct {
    id: i64,
    session_id: []const u8,
    subscription: *hubmod.Subscription,
};

const Member = struct { key: []const u8, integer: u64 };

fn signal(arena: std.mem.Allocator, event: []const u8, id: i64, session_id: []const u8, run_id: []const u8, members: []const Member, message: []const u8) Error!std.ArrayList(u8) {
    var scratch = std.heap.ArenaAllocator.init(arena);
    defer scratch.deinit();
    const quoted = json_encode.valueAlloc(scratch.allocator(), .{ .string = session_id }) catch return error.OutOfMemory;
    var line = std.ArrayList(u8).empty;
    errdefer line.deinit(arena);
    try appendAll(arena, &line, try std.fmt.allocPrint(scratch.allocator(), "{{\"event\":\"{s}\",\"id\":{d},\"session_id\":{s}", .{ event, id, quoted }));
    if (run_id.len > 0) {
        const named = json_encode.valueAlloc(scratch.allocator(), .{ .string = run_id }) catch return error.OutOfMemory;
        try appendAll(arena, &line, try std.fmt.allocPrint(scratch.allocator(), ",\"run_id\":{s}", .{named}));
    }
    for (members) |entry| {
        try appendAll(arena, &line, try std.fmt.allocPrint(scratch.allocator(), ",\"{s}\":{d}", .{ entry.key, entry.integer }));
    }
    if (message.len > 0) {
        const quoted_message = json_encode.valueAlloc(scratch.allocator(), .{ .string = message }) catch return error.OutOfMemory;
        try appendAll(arena, &line, try std.fmt.allocPrint(scratch.allocator(), ",\"message\":{s}", .{quoted_message}));
    }
    try appendAll(arena, &line, "}");
    return line;
}


fn appendAll(arena: std.mem.Allocator, line: *std.ArrayList(u8), text: []const u8) Error!void {
    line.appendSlice(arena, text) catch return error.OutOfMemory;
}

fn append(arena: std.mem.Allocator, line: *std.ArrayList(u8), text: []const u8) !void {
    try line.ensureUnusedCapacity(arena, text.len + 16);
    line.appendSliceAssumeCapacity(arena, "{\"");
    line.appendSliceAssumeCapacity(arena, text);
}

pub const Frontend = struct {
    hub: *Hub,
    sink: Sink,
    allocator: std.mem.Allocator,
    frame_limit: usize,
    max_ops: usize,
    max_subscriptions: usize,
    in_flight: usize = 0,
    next_envelope: u64 = 0,
    subscriptions: std.ArrayList(Watch) = .empty,
    stopped: bool = false,

    pub fn init(allocator: std.mem.Allocator, hub: *Hub, sink: Sink, options: Options) Error!Frontend {
        if (options.frame_limit < minimum_frame_limit) return Error.FrameLimitTooSmall;
        return .{
            .hub = hub,
            .sink = sink,
            .allocator = allocator,
            .frame_limit = options.frame_limit,
            .max_ops = options.max_ops,
            .max_subscriptions = options.max_subscriptions,
        };
    }

    pub fn deinit(self: *Frontend) void {
        self.subscriptions.deinit(self.allocator);
        self.* = undefined;
    }

    pub fn handleLine(self: *Frontend, line: []const u8) Error!void {
        if (self.stopped) return;
        if (line.len > self.frame_limit) {
            self.stopped = true;
            return Error.FrameLimitTooSmall;
        }
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();
        const request = decode(arena, line) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {
                self.stopped = true;
                return Error.MalformedLine;
            },
        };
        if (self.in_flight >= self.max_ops) {
            return self.refuse(arena, request.id, .{ .code = "busy", .message = "the frontend is already running 16 operations; send this request again" });
        }
        self.in_flight += 1;
        defer self.in_flight -= 1;
        const outcome = self.dispatch(arena, request) catch |err| {
            self.in_flight -= 1;
            return err;
        };
        switch (outcome) {
            .answer => |value| try self.answerValue(request.id, value),
            .answer_line => |payload| try self.answerLine(request.id, payload),
            .refused => |refusal| try self.refuse(arena, request.id, refusal),
            .streaming => return,
        }
    }

    const Outcome = union(enum) {
        answer: std.json.Value,
        answer_line: []const u8,
        refused: Refusal,
        streaming,
    };

    fn dispatch(self: *Frontend, arena: std.mem.Allocator, request: Request) Error!Outcome {
        if (std.mem.eql(u8, request.op, op_adapters)) {
            if (request.only(no_parameters)) |refusal| return .{ .refused = refusal };
            return .{ .answer = try self.adapters(arena) };
        }
        if (std.mem.eql(u8, request.op, op_sessions)) {
            if (request.only(no_parameters)) |refusal| return .{ .refused = refusal };
            return .{ .answer = try self.sessions(arena) };
        }
        if (std.mem.eql(u8, request.op, op_capabilities)) {
            if (request.only(adapter_parameter)) |refusal| return .{ .refused = refusal };
            const name = request.adapter orelse return .{ .refused = .{ .code = "invalid_request", .message = "adapter is required" } };
            return self.capabilities(arena, name);
        }
        if (std.mem.eql(u8, request.op, op_close)) {
            if (request.only(session_parameter)) |refusal| return .{ .refused = refusal };
            const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
            return self.closeSession(arena, session_id);
        }
        if (std.mem.eql(u8, request.op, op_state)) {
            if (request.only(session_parameter)) |refusal| return .{ .refused = refusal };
            const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
            return self.state(arena, session_id);
        }
        if (std.mem.eql(u8, request.op, op_open)) return self.open(arena, request);
        if (std.mem.eql(u8, request.op, op_submit)) return self.submit(arena, request);
        if (std.mem.eql(u8, request.op, op_cancel)) return self.cancel(arena, request);
        if (std.mem.eql(u8, request.op, op_resolve)) return self.resolve(arena, request);
        if (std.mem.eql(u8, request.op, op_models)) return self.models(arena, request);
        if (std.mem.eql(u8, request.op, op_tools)) return self.tools(arena, request);
        if (std.mem.eql(u8, request.op, op_events)) return self.events(arena, request);
        return .{ .refused = .{ .code = "unknown_op", .message = "this frontend does not serve that op" } };
    }

    fn adapters(self: *Frontend, arena: std.mem.Allocator) !std.json.Value {
        const listed = try self.hub.listing(arena);
        const entries = try arena.alloc(std.json.Value, listed.len);
        for (listed, entries) |status, *entry| {
            var object = try emptyObject(arena);
            try object.put(arena, "name", .{ .string = status.name });
            if (status.capabilities) |descriptor| {
                try object.put(arena, "capability_revision", .{ .string = status.revision });
                try object.put(arena, "capabilities", try capabilityValue(arena, descriptor));
            } else {
                try object.put(arena, "error", .{ .string = status.message });
            }
            entry.* = .{ .object = object };
        }
        var root = try emptyObject(arena);
        try root.put(arena, "adapters", try jsonArray(arena, entries));
        return .{ .object = root };
    }

    fn sessions(self: *Frontend, arena: std.mem.Allocator) !std.json.Value {
        const listed = try self.hub.sessions(arena);
        const entries = try arena.alloc(std.json.Value, listed.len);
        for (listed, entries) |status, *entry| {
            var object = try emptyObject(arena);
            try object.put(arena, "session_id", .{ .string = status.session_id });
            try object.put(arena, "adapter", .{ .string = status.adapter });
            try object.put(arena, "status", .{ .string = @tagName(status.status) });
            if (status.active_run_id.len > 0) try object.put(arena, "active_run_id", .{ .string = status.active_run_id });
            if (status.active_runs.len > 0) {
                const runs = try arena.alloc(std.json.Value, status.active_runs.len);
                for (status.active_runs, runs) |active, *run| {
                    var run_object = try emptyObject(arena);
                    try run_object.put(arena, "run_id", .{ .string = active.run_id });
                    try run_object.put(arena, "status", .{ .string = @tagName(active.status) });
                    if (active.relationship.len > 0) try run_object.put(arena, "relationship", .{ .string = active.relationship });
                    run.* = .{ .object = run_object };
                }
                try object.put(arena, "active_runs", try jsonArray(arena, runs));
            }
            try object.put(arena, "created_at", .{ .string = try timestamp(arena, status.created_at_ms) });
            entry.* = .{ .object = object };
        }
        var root = try emptyObject(arena);
        try root.put(arena, "sessions", try jsonArray(arena, entries));
        return .{ .object = root };
    }

    fn capabilities(self: *Frontend, arena: std.mem.Allocator, name: []const u8) !Outcome {
        const descriptor = self.hub.probe(name) catch |err| switch (err) {
            error.UnknownAdapter => return .{ .refused = .{ .code = "unknown_adapter", .message = try std.fmt.allocPrint(arena, "no adapter \"{s}\"", .{name}) } },
            error.OutOfMemory => return error.OutOfMemory,
            else => return .{ .refused = .{ .code = "probe_failed", .message = try std.fmt.allocPrint(arena, "the adapter \"{s}\" could not be probed", .{name}) } },
        };
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oapx-hub-{d}", .{self.next_envelope});
        const correlation = try std.fmt.allocPrint(arena, "oapx-hub-request-{d}", .{self.next_envelope});
        const declared = try declaredFeatures(arena, descriptor.features);
        const catalog = try arena.dupe(oap_types.ToolDefinition, descriptor.tools);
        const sources = try arena.dupe(oap_types.ToolSourceDescriptor, descriptor.sources);
        const payload = oap_types.CapabilitiesResponse{
            .endpoint = descriptor.endpoint,
            .protocol_versions = &.{oap_types.VERSION},
            .profiles = &.{oap_types.PROFILE},
            .features = declared,
            .tools = catalog,
            .sources = sources,
            .limits = descriptor.limits,
        };
        const envelope = oap_types.Envelope{
            .id = answer_id,
            .in_reply_to = correlation,
            .capability_revision = descriptor.capability_revision,
            .payload = .{ .capabilities_response = payload },
        };
        return .{ .answer_line = try oap_envelope.serializeEnvelope(envelope, arena) };
    }

    fn state(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !Outcome {
        const reported = self.hub.state(arena, session_id) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oapx-hub-{d}", .{self.next_envelope});
        const correlation = try std.fmt.allocPrint(arena, "oapx-hub-request-{d}", .{self.next_envelope});
        const envelope = oap_types.Envelope{
            .id = answer_id,
            .in_reply_to = correlation,
            .payload = .{ .session_state_response = reported },
        };
        return .{ .answer_line = try oap_envelope.serializeEnvelope(envelope, arena) };
    }

    fn closeSession(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !Outcome {
        self.hub.close(arena, session_id) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        return .{ .answer = .{ .null = {} } };
    }

    const request_parameters: []const []const u8 = &.{ "session_id", "request" };
    const events_parameters: []const []const u8 = &.{ "session_id", "run_id", "after" };
    const open_parameters: []const []const u8 = &.{ "adapter", "request" };
    const degraded_parameters: []const []const u8 = &.{ "session_id", "allow_degraded_features" };

    fn requestEnvelope(self: *Frontend, request: Request, arena: std.mem.Allocator) Error!oap_types.Envelope {
        _ = self;
        const value = request.payload orelse return error.MissingPayload;
        const line = if (json_encode.valueAlloc(arena, value)) |ready| ready else |_| return error.OutOfMemory;
        return oap_envelope.deserializeEnvelope(line, arena) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return error.UnreadablePayload,
        };
    }

    fn withEnvelope(self: *Frontend, arena: std.mem.Allocator, in_reply_to: []const u8, revision: []const u8, body: oap_types.Payload) !Outcome {
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oapx-hub-{d}", .{self.next_envelope});
        const line = try oap_envelope.serializeEnvelope(.{
            .id = answer_id,
            .in_reply_to = in_reply_to,
            .capability_revision = if (revision.len > 0) revision else null,
            .payload = body,
        }, arena);
        return .{ .answer_line = line };
    }

    fn open(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(open_parameters)) |refusal| return .{ .refused = refusal };
        const adapter = request.adapter orelse return .{ .refused = .{ .code = "invalid_request", .message = "adapter is required" } };
        const envelope = self.requestEnvelope(request, arena) catch |err| {
            return .{ .refused = .{ .code = try payloadCode(err), .message = "the request envelope is not one this op serves" } };
        };
        const opening = switch (envelope.payload) {
            .session_open_request => |value| value,
            else => return .{ .refused = .{ .code = "type_mismatch", .message = "open takes a session.open.request" } },
        };
        if (opening.message_json) |message| {
            _ = json_encode.parse(arena, message) catch |err| {
                return .{ .refused = .{ .code = try payloadCode(err), .message = "the open message is not JSON" } };
            };
        }
        const opened = self.hub.open(arena, adapter, .{
            .session_id = opening.session_id orelse "",
            .subscribe = opening.subscribe,
            .allow_degraded_features = opening.allow_degraded_features,
            .tools_json = opening.tools_json,
            .tool_sources_json = opening.tool_sources_json,
        }) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, opening.session_id orelse "") };
        };
        return self.withEnvelope(arena, envelope.id, opened.revision, .{ .session_open_response = opened.state });
    }

    fn submit(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(request_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
        const envelope = self.requestEnvelope(request, arena) catch |err| {
            return .{ .refused = .{ .code = try payloadCode(err), .message = "the request envelope is not one this op serves" } };
        };
        const submission = switch (envelope.payload) {
            .message_submit_request => |value| value,
            else => return .{ .refused = .{ .code = "type_mismatch", .message = "submit takes a session.message.submit.request" } },
        };
        const admission = self.hub.submit(arena, session_id, &submission) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        return self.withEnvelope(arena, envelope.id, "", .{ .message_submit_response = admission });
    }

    fn cancel(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(request_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
        const envelope = self.requestEnvelope(request, arena) catch |err| {
            return .{ .refused = .{ .code = try payloadCode(err), .message = "the request envelope is not one this op serves" } };
        };
        const cancellation = switch (envelope.payload) {
            .run_cancel_request => |value| value,
            else => return .{ .refused = .{ .code = "type_mismatch", .message = "cancel takes a run.cancel.request" } },
        };
        if (!std.mem.eql(u8, cancellation.session_id, session_id)) {
            return .{ .refused = .{ .code = "scope_mismatch", .message = "the payload names a session other than the addressed one" } };
        }
        const cancellation_answer = self.hub.cancel(arena, session_id, cancellation.run_id) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        return self.withEnvelope(arena, envelope.id, "", .{ .run_cancel_response = cancellation_answer });
    }

    fn resolve(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(request_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
        const envelope = self.requestEnvelope(request, arena) catch |err| {
            return .{ .refused = .{ .code = try payloadCode(err), .message = "the request envelope is not one this op serves" } };
        };
        switch (envelope.payload) {
            .permission_resolve_request => |value| {
                if (!std.mem.eql(u8, value.session_id, session_id)) return .{ .refused = .{ .code = "scope_mismatch", .message = "the payload names a session other than the addressed one" } };
                self.hub.resolve(arena, session_id, .{ .permission = &value }) catch |err| {
                    return .{ .refused = try self.resolveRefusal(arena, err, session_id) };
                };
                return self.withEnvelope(arena, envelope.id, "", .{ .permission_resolve_response = .{
                    .interaction_id = value.interaction_id,
                    .session_id = value.session_id,
                    .run_id = value.run_id,
                    .accepted = true,
                } });
            },
            .user_input_resolve_request => |value| {
                if (!std.mem.eql(u8, value.session_id, session_id)) return .{ .refused = .{ .code = "scope_mismatch", .message = "the payload names a session other than the addressed one" } };
                self.hub.resolve(arena, session_id, .{ .input = &value }) catch |err| {
                    return .{ .refused = try self.resolveRefusal(arena, err, session_id) };
                };
                return self.withEnvelope(arena, envelope.id, "", .{ .user_input_resolve_response = .{
                    .interaction_id = value.interaction_id,
                    .session_id = value.session_id,
                    .run_id = value.run_id,
                    .accepted = true,
                } });
            },
            .call_resolve_request => |value| {
                if (!std.mem.eql(u8, value.session_id, session_id)) return .{ .refused = .{ .code = "scope_mismatch", .message = "the payload names a session other than the addressed one" } };
                const call_answer = self.hub.resolveCall(arena, session_id, value.interaction_id, &value) catch |err| {
                    return .{ .refused = try self.resolveRefusal(arena, err, session_id) };
                };
                return self.withEnvelope(arena, envelope.id, "", .{ .call_resolve_response = call_answer });
            },
            else => return .{ .refused = .{ .code = "type_mismatch", .message = "resolve takes a permission, user-input or call resolve request" } },
        }
    }

    fn events(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(events_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
        const named = request.run_id orelse "";
        if (named.len > 0 and request.after == null) {
            return .{ .refused = .{ .code = "invalid_cursor", .message = "a run_id needs a cursor" } };
        }
        const subscription = self.hub.subscribe(arena, session_id, .{ .run_id = named, .after = request.after }) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        try self.answerValue(request.id, .{ .null = {} });
        if (subscription.joined) {
            try self.subscribedLine(request.id, session_id, subscription.run_id, subscription.joined_after);
        }
        if (subscription.gap) |gap| {
            try self.gapLine(request.id, session_id, gap.requested_after, gap.oldest_available, gap.latest_available);
            subscription.ending = .expired;
            return .{ .streaming = {} };
        }
        if (subscription.ending != .open) {
            try self.endLine(request.id, session_id, subscription);
            return .{ .streaming = {} };
        }
        try self.subscriptions.append(self.allocator, .{ .id = request.id, .session_id = session_id, .subscription = subscription });
        return .{ .streaming = {} };
    }

    pub fn pump(self: *Frontend, allocator: std.mem.Allocator, wait_ns: u64) Error!void {
        try self.hub.pump(allocator, wait_ns);
        var index: usize = 0;
        while (index < self.subscriptions.items.len) {
            const watch = self.subscriptions.items[index];
            var progressed = false;
            while (watch.subscription.next()) |delivery| {
                progressed = true;
                try self.envelopeLine(self.allocator, watch.id, watch.session_id, delivery);
            }
            if (watch.subscription.ending == .open) {
                if (progressed) continue;
                index += 1;
                continue;
            }
            try self.endLine(watch.id, watch.session_id, watch.subscription);
            watch.subscription.close();
            _ = self.subscriptions.orderedRemove(index);
        }
    }

    fn subscribedLine(self: *Frontend, id: i64, session_id: []const u8, run_id: []const u8, joined: u64) Error!void {
        const members = [_]Member{.{ .key = "joined_after", .integer = joined }};
        var line = try signal(self.allocator, "oap-subscribed", id, session_id, run_id, &members, "the subscription begins after this sequence; resubscribe with a cursor at or before it to replay what preceded this point");
        defer line.deinit(self.allocator);
        try self.write(line.items);
    }

    fn gapLine(self: *Frontend, id: i64, session_id: []const u8, requested: u64, oldest: u64, latest: u64) Error!void {
        const members = [_]Member{ .{ .key = "requested_after", .integer = requested }, .{ .key = "oldest_available", .integer = oldest }, .{ .key = "latest_available", .integer = latest } };
        var line = try signal(self.allocator, "oap-replay-gap", id, session_id, "", &members, "the cursor is older than this run's journal; resubscribe at or after oldest_available");
        defer line.deinit(self.allocator);
        try self.write(line.items);
    }

    fn endLine(self: *Frontend, id: i64, session_id: []const u8, subscription: *hubmod.Subscription) Error!void {
        const ending = subscription.ending;
        if (ending == .run_terminal) return;
        const named = if (ending == .overflow) subscription.overflow_run else subscription.run_id;
        var written: std.ArrayList(u8) = .empty;
        defer written.deinit(self.allocator);
        switch (ending) {
            .overflow => {
                const members = [_]Member{.{ .key = "last_sequence", .integer = subscription.overflow_sequence }};
                written = try signal(self.allocator, "oap-overflow", id, session_id, named, &members, "this consumer fell behind; resubscribe after last_sequence to continue");
                try self.write(written.items);
            },
            .session_closed => {
                written = try signal(self.allocator, "oap-session-closed", id, session_id, "", &.{}, "the session closed under this subscription");
                try self.write(written.items);
            },
            .stream_failed => {
                const members = [_]Member{.{ .key = "sequence", .integer = subscription.highest }};
                written = try signal(self.allocator, "oap-stream-failed", id, session_id, named, &members, "the run's event stream failed; resume with a cursor after this sequence");
                try self.write(written.items);
            },
            .expired => {
                const members = [_]Member{ .{ .key = "requested_after", .integer = 0 }, .{ .key = "oldest_available", .integer = 0 }, .{ .key = "latest_available", .integer = 0 } };
                written = try signal(self.allocator, "oap-replay-gap", id, session_id, named, &members, "this subscription was released before it was read");
                try self.write(written.items);
            },
            else => {},
        }
    }

    fn envelopeLine(self: *Frontend, arena: std.mem.Allocator, id: i64, session_id: []const u8, delivery: hubmod.Delivery) Error!void {
        var scratch = std.heap.ArenaAllocator.init(arena);
        defer scratch.deinit();
        const quoted = json_encode.valueAlloc(scratch.allocator(), .{ .string = session_id }) catch return error.OutOfMemory;
        const line = if (std.fmt.allocPrint(arena, "{{\"event\":\"envelope\",\"id\":{d},\"session_id\":{s},\"sequence\":{d},\"envelope\":{s}}}", .{ id, quoted, delivery.sequence, delivery.line })) |ready| ready else |_| return error.OutOfMemory;
        defer arena.free(line);
        if (line.len > self.frame_limit) {
            const members = [_]Member{.{ .key = "sequence", .integer = delivery.sequence }};
            var shed = try signal(self.allocator, "oap-frame-limit", id, session_id, delivery.run_id, &members, "this envelope does not fit the frame limit; resubscribe after this sequence");
            defer shed.deinit(self.allocator);
            try self.write(shed.items);
            return;
        }
        try self.write(line);
    }

    fn models(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(degraded_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
        const listing = self.hub.models(arena, session_id, &.{ .session_id = session_id, .allow_degraded_features = request.allow_degraded_features }) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        return self.withEnvelope(arena, try std.fmt.allocPrint(arena, "oapx-hub-request-{d}", .{self.next_envelope + 1}), listing.revision, .{ .models_response = listing.models });
    }

    fn tools(self: *Frontend, arena: std.mem.Allocator, request: Request) !Outcome {
        if (request.only(degraded_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse return .{ .refused = .{ .code = "invalid_request", .message = "session_id is required" } };
        const listing = self.hub.tools(arena, session_id, &.{ .session_id = session_id, .allow_degraded_features = request.allow_degraded_features }) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        const body = try self.withEnvelope(arena, try std.fmt.allocPrint(arena, "oapx-hub-request-{d}", .{self.next_envelope + 1}), listing.revision, .{ .tools_list_response = listing.tools });
        return body;
    }

    fn resolveRefusal(self: *Frontend, arena: std.mem.Allocator, err: hubmod.Failure, session_id: []const u8) !Refusal {
        return switch (err) {
            error.InvalidResolution => .{ .code = "resolution_rejected", .message = try std.fmt.allocPrint(arena, "the interaction on the session \"{s}\" was refused", .{session_id}) },
            error.RunNotFound => .{ .code = "run_not_found", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" has no such run", .{session_id}) },
            error.InteractionNotFound => .{ .code = "resolution_rejected", .message = try std.fmt.allocPrint(arena, "the interaction on the session \"{s}\" is not found", .{session_id}) },
            error.UnsupportedFeature => .{ .code = "unsupported_feature", .message = "this session has no call resolver" },
            else => try self.refusalFor(arena, err, session_id),
        };
    }

    fn refusalFor(self: *Frontend, arena: std.mem.Allocator, err: hubmod.Failure, session_id: []const u8) !Refusal {
        _ = self;
        return switch (err) {
            error.UnknownSession => .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{session_id}) },
            error.SessionClosed => .{ .code = "session_closed", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" is closed", .{session_id}) },
            error.RunActive => .{ .code = "run_active", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" still has a run in flight; cancel it first", .{session_id}) },
            error.SessionExists => .{ .code = "session_exists", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" already exists", .{session_id}) },
            error.UnknownAdapter => .{ .code = "unknown_adapter", .message = "no adapter by that name is registered" },
            error.AdapterExists => .{ .code = "internal", .message = "that adapter is already registered" },
            error.RunNotFound => .{ .code = "run_not_found", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" has no such run", .{session_id}) },
            error.RunTerminal => .{ .code = "run_terminal", .message = try std.fmt.allocPrint(arena, "the run on the session \"{s}\" already settled", .{session_id}) },
            error.InvalidSubmission => .{ .code = "invalid_submission", .message = "the submission is not one this session accepts" },
            error.UnsupportedFeature => .{ .code = "unsupported_feature", .message = "this session does not serve that feature" },
            error.CapabilityDegraded => .{ .code = "capability_degraded", .message = "this feature is degraded and the request did not opt in" },
            error.ModelNotFound => .{ .code = "model_not_found", .message = "this session does not serve that model" },
            error.NoRunToResume => .{ .code = "no_run_to_resume", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" has no run to replay", .{session_id}) },
            error.InvalidCursor => .{ .code = "invalid_cursor", .message = "a cursor needs both a run and a sequence" },
            error.ReplayCursorFuture => .{ .code = "replay_cursor_future", .message = "the cursor is ahead of the run" },
            error.ToolCatalogUnavailable => .{ .code = "tools_failed", .message = "this session has no tool catalog" },
            error.UnresolvableAttachment => .{ .code = "unsupported_feature", .message = "a tool source will not attach" },
            error.StaleCapabilities => .{ .code = "stale_capabilities", .message = "the cited revision is not the probed one" },
            error.CatalogMisScoped => .{ .code = "scope_mismatch", .message = "the catalog names another session" },
            error.CatalogUnlabelled => .{ .code = "internal", .message = "the catalog was served under no revision" },
            error.ScopeMismatch => .{ .code = "scope_mismatch", .message = try std.fmt.allocPrint(arena, "the request names a session other than \"{s}\"", .{session_id}) },
            error.OutOfMemory => return error.OutOfMemory,
            else => .{ .code = "internal", .message = @errorName(err) },
        };
    }

    fn answerValue(self: *Frontend, id: i64, value: std.json.Value) Error!void {
        const arena = self.allocator;
        var object = try emptyObject(arena);
        defer object.deinit(arena);
        try object.put(arena, "id", .{ .integer = id });
        try object.put(arena, "ok", .{ .bool = true });
        try object.put(arena, "result", value);
        const line = json_encode.valueAlloc(arena, .{ .object = object }) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
        };
        defer arena.free(line);
        if (line.len > self.frame_limit) return Error.ResponseTooLarge;
        try self.write(line);
    }

    fn answerLine(self: *Frontend, id: i64, payload: []const u8) Error!void {
        const arena = self.allocator;
        const line = if (std.fmt.allocPrint(arena, "{{\"id\":{d},\"ok\":true,\"result\":{s}}}", .{ id, payload })) |ready| ready else |_| return error.OutOfMemory;
        defer arena.free(line);
        if (line.len > self.frame_limit) return Error.ResponseTooLarge;
        try self.write(line);
    }

    fn refuse(self: *Frontend, arena: std.mem.Allocator, id: i64, refusal: Refusal) Error!void {
        var object = try emptyObject(arena);
        try object.put(arena, "id", .{ .integer = id });
        try object.put(arena, "ok", .{ .bool = false });
        try object.put(arena, "result", .{ .null = {} });
        var body = try emptyObject(arena);
        try body.put(arena, "code", .{ .string = refusal.code });
        try body.put(arena, "message", .{ .string = try trim(arena, refusal.message) });
        if (refusal.details.len > 0) {
            const details = try arena.alloc(std.json.Value, refusal.details.len);
            for (refusal.details, details) |detail, *entry| {
                var pair = try emptyObject(arena);
                try pair.put(arena, "key", .{ .string = detail.key });
                try pair.put(arena, "value", .{ .string = detail.value });
                entry.* = .{ .object = pair };
            }
            try body.put(arena, "details", try jsonArray(arena, details));
        }
        try object.put(arena, "error", .{ .object = body });
        const line = json_encode.valueAlloc(arena, .{ .object = object }) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
        };
        defer arena.free(line);
        if (line.len > self.frame_limit) return Error.ResponseTooLarge;
        try self.write(line);
    }

    fn write(self: *Frontend, line: []const u8) Error!void {
        self.sink.write(self.sink.context, line) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return Error.OutputStalled,
        };
    }
};

fn payloadCode(err: Error) ![]const u8 {
    return switch (err) {
        error.MissingPayload => "invalid_request",
        error.MalformedJson => "invalid_payload",
        else => "schema_invalid",
    };
}

fn trim(arena: std.mem.Allocator, message: []const u8) ![]const u8 {
    if (message.len <= message_limit) return message;
    const head = std.unicode.utf8ByteSequenceLength(message[message_limit]) catch message_limit;
    return std.fmt.allocPrint(arena, "{s}…", .{message[0 .. message_limit - head]});
}

fn timestamp(arena: std.mem.Allocator, milliseconds: i64) ![]const u8 {
    const seconds: u64 = @intCast(@divFloor(milliseconds, 1000));
    const rest: u64 = @intCast(@mod(milliseconds, 1000));
    const epoch = std.time.epoch.EpochSeconds{ .secs = seconds };
    const day = epoch.getEpochDay().calculateYearDay();
    const month = day.calculateMonthDay();
    const clock = epoch.getDaySeconds();
    var buffer: [40]u8 = undefined;
    const written = try std.fmt.bufPrint(&buffer, "{d:0>4}-{d:0>2}-{d:0>2}T{d:0>2}:{d:0>2}:{d:0>2}.{d:0>3}Z", .{
        day.year,
        @intFromEnum(month.month),
        @as(u16, month.day_index) + 1,
        clock.getHoursIntoDay(),
        clock.getMinutesIntoHour(),
        clock.getSecondsIntoMinute(),
        rest,
    });
    return arena.dupe(u8, written);
}

fn capabilityValue(arena: std.mem.Allocator, descriptor: contract.Descriptor) !std.json.Value {
    var object = try emptyObject(arena);
    try object.put(arena, "endpoint", try endpointValue(arena, descriptor.endpoint));
    try object.put(arena, "protocol_versions", try jsonStrings(arena, &.{oap_types.VERSION}));
    try object.put(arena, "profiles", try jsonStrings(arena, &.{oap_types.PROFILE}));
    try object.put(arena, "features", try featureMapValue(arena, try declaredFeatures(arena, descriptor.features)));
    if (descriptor.tools.len > 0) try object.put(arena, "tools", try catalogValue(arena, descriptor.tools));
    if (descriptor.sources.len > 0) try object.put(arena, "sources", try sourcesValue(arena, descriptor.sources));
    if (descriptor.limits) |limits| try object.put(arena, "limits", try limitsValue(arena, limits));
    return .{ .object = object };
}

fn endpointValue(arena: std.mem.Allocator, endpoint: oap_types.Endpoint) !std.json.Value {
    var object = try emptyObject(arena);
    try object.put(arena, "id", .{ .string = endpoint.id });
    if (endpoint.name) |value| try object.put(arena, "name", .{ .string = value });
    if (endpoint.version) |value| try object.put(arena, "version", .{ .string = value });
    if (endpoint.adapter) |value| try object.put(arena, "adapter", .{ .string = value });
    return .{ .object = object };
}

fn declaredFeatures(arena: std.mem.Allocator, features: []const contract.Feature) ![]oap_types.Feature {
    const declared = try arena.alloc(oap_types.Feature, features.len);
    for (features, declared) |feature, *entry| {
        entry.* = .{
            .key = feature.key,
            .level = feature.level,
            .scope = feature.scope,
            .reason = feature.reason,
            .modes = feature.modes,
            .constraints_json = feature.constraints_json,
            .limits_json = feature.limits_json,
        };
    }
    return declared;
}

fn featureMapValue(arena: std.mem.Allocator, features: []const oap_types.Feature) !std.json.Value {
    var object = try emptyObject(arena);
    for (features) |feature| {
        var support = try emptyObject(arena);
        try support.put(arena, "level", .{ .string = @tagName(feature.level) });
        if (feature.reason) |reason| try support.put(arena, "reason", .{ .string = reason });
        if (feature.scope) |scope| try support.put(arena, "scope", .{ .string = scope });
        if (feature.modes.len > 0) {
            try support.put(arena, "modes", try jsonStrings(arena, feature.modes));
        }
        if (feature.constraints_json) |text| {
            try support.put(arena, "constraints", try json_encode.parse(arena, text));
        }
        if (feature.limits_json) |text| {
            try support.put(arena, "limits", try json_encode.parse(arena, text));
        }
        try object.put(arena, feature.key, .{ .object = support });
    }
    return .{ .object = object };
}

fn sourcesValue(arena: std.mem.Allocator, sources: []const oap_types.ToolSourceDescriptor) !std.json.Value {
    const entries = try arena.alloc(std.json.Value, sources.len);
    for (sources, entries) |source, *entry| {
        var object = try emptyObject(arena);
        try object.put(arena, "id", .{ .string = source.id });
        try object.put(arena, "kind", .{ .string = source.kind });
        if (source.display_name) |value| try object.put(arena, "display_name", .{ .string = value });
        if (source.protocol) |value| try object.put(arena, "protocol", .{ .string = value });
        if (source.endpoint) |value| try object.put(arena, "endpoint", .{ .string = value });
        entry.* = .{ .object = object };
    }
    return try jsonArray(arena, entries);
}

fn catalogValue(arena: std.mem.Allocator, tools: []const oap_types.ToolDefinition) !std.json.Value {
    const entries = try arena.alloc(std.json.Value, tools.len);
    for (tools, entries) |tool, *entry| {
        var object = try emptyObject(arena);
        try object.put(arena, "name", .{ .string = tool.name });
        if (tool.description) |value| try object.put(arena, "description", .{ .string = value });
        try object.put(arena, "input_schema", try json_encode.parse(arena, tool.input_schema_json));
        try object.put(arena, "execution_owner", .{ .string = tool.execution_owner });
        if (tool.source) |value| try object.put(arena, "source", .{ .string = value });
        if (tool.features.len > 0) try object.put(arena, "features", try featureMapValue(arena, tool.features));
        entry.* = .{ .object = object };
    }
    return try jsonArray(arena, entries);
}

fn limitsValue(arena: std.mem.Allocator, limits: oap_types.Limits) !std.json.Value {
    var object = try emptyObject(arena);
    if (limits.max_active_runs_per_session) |value| try object.put(arena, "max_active_runs_per_session", .{ .integer = value });
    if (limits.max_queued_runs_per_session) |value| try object.put(arena, "max_queued_runs_per_session", .{ .integer = value });
    return .{ .object = object };
}

const testing = std.testing;

var frozen_ns: u64 = 0;

fn wallClock() u64 {
    return frozen_ns;
}

const Recorder = struct {
    lines: std.ArrayList([]u8) = .empty,
    allocator: std.mem.Allocator,

    fn sink(self: *Recorder) Sink {
        return .{ .context = @ptrCast(self), .write = writeLine };
    }

    fn writeLine(context: *anyopaque, line: []const u8) anyerror!void {
        const self: *Recorder = @ptrCast(@alignCast(context));
        try self.lines.append(self.allocator, try self.allocator.dupe(u8, line));
    }

    fn last(self: *const Recorder) []const u8 {
        return self.lines.items[self.lines.items.len - 1];
    }

    fn deinit(self: *Recorder) void {
        for (self.lines.items) |line| self.allocator.free(line);
        self.lines.deinit(self.allocator);
    }
};

const scripted_envelopes = [_][]const u8{
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"run.started\",\"id\":\"e1\",\"payload\":{\"session_id\":\"s\"}}",
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.delta\",\"id\":\"e2\",\"payload\":{\"session_id\":\"s\"}}",
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.completed\",\"id\":\"e3\",\"payload\":{\"session_id\":\"s\"}}",
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"run.completed\",\"id\":\"e4\",\"payload\":{\"session_id\":\"s\"}}",
};

const reference_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "reference", .name = "Reference", .version = "0.1", .adapter = "script" },
    .capability_revision = "reference-v1",
    .features = &.{
        .{ .key = "session.open.subscribe", .level = .native },
        .{ .key = "session.tool_sources.attach", .level = .degraded, .reason = "sources are recorded, not loaded" },
        .{ .key = "run.cancel", .level = .emulated, .scope = "primary", .modes = &.{"stream"}, .constraints_json = "{\"max\":2}" },
    },
};

const Harness = struct {
    allocator: std.mem.Allocator,
    recorder: Recorder,
    hub: Hub,
    frontend: Frontend,
    backing: std.heap.ArenaAllocator,

    fn init(allocator: std.mem.Allocator, hub_options: hubmod.Options, options: Options) !*Harness {
        const self = try allocator.create(Harness);
        self.* = .{
            .allocator = allocator,
            .recorder = .{ .allocator = allocator },
            .hub = undefined,
            .frontend = undefined,
            .backing = std.heap.ArenaAllocator.init(allocator),
        };
        self.hub = Hub.init(allocator, wallClock, hub_options);
        try self.hub.register("reference", reference());
        try self.hub.register("zebra", reference());
        self.frontend = try Frontend.init(allocator, &self.hub, self.recorder.sink(), options);
        return self;
    }

    fn deinit(self: *Harness) void {
        self.frontend.deinit();
        self.hub.deinit();
        self.recorder.deinit();
        self.backing.deinit();
        self.allocator.destroy(self);
    }

    fn arena(self: *Harness) std.mem.Allocator {
        return self.backing.allocator();
    }

    fn send(self: *Harness, line: []const u8) !void {
        try self.frontend.handleLine(line);
    }

    fn lastValue(self: *Harness) !std.json.Value {
        return std.json.parseFromSliceLeaky(std.json.Value, self.arena(), self.recorder.last(), .{});
    }

    fn code(self: *Harness) ![]const u8 {
        const answer = try self.lastValue();
        const body = answer.object.get("error") orelse return error.Shape;
        return textMember(self.arena(), body, "code");
    }
};

const ReferenceState = struct {
    opened: usize = 0,
    id_buffer: [64]u8 = undefined,
    id_len: usize = 0,
    running: bool = false,
    runs: [4][]const u8 = .{ "", "", "", "" },
    run_count: usize = 0,
    outbox: [8][]const u8 = .{ "", "", "", "", "", "", "", "" },
    outbox_len: usize = 0,

    fn id(self: *const ReferenceState) []const u8 {
        if (self.id_len == 0) return "session-1";
        return self.id_buffer[0..self.id_len];
    }

    fn knows(self: *const ReferenceState, run_id: []const u8) bool {
        for (self.runs[0..self.run_count]) |known| {
            if (std.mem.eql(u8, known, run_id)) return true;
        }
        return false;
    }
};

var reference_holder: ReferenceState = .{};

fn reference() contract.Adapter {
    return .{ .ptr = @constCast(@ptrCast(&reference_holder)), .vtable = &.{ .probe = referenceProbe, .open = referenceOpen } };
}

fn referenceProbe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
    _ = ptr;
    _ = refusal;
    return reference_descriptor;
}

fn referenceOpen(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = arena;
    _ = refusal;
    state.opened += 1;
    state.running = false;
    state.run_count = 0;
    state.outbox_len = 0;
    const named = if (request.session_id.len > 0) request.session_id else "session-1";
    @memcpy(state.id_buffer[0..named.len], named);
    state.id_len = named.len;
    return .{ .ptr = state, .vtable = &.{
        .id = referenceId,
        .state = referenceState,
        .submit = referenceSubmit,
        .resolve = referenceResolve,
        .cancel = referenceCancel,
        .pump = referencePump,
        .drain = referenceDrain,
        .activity = referenceActivity,
        .close = referenceClose,
        .models = referenceModels,
        .tools = referenceTools,
    } };
}

fn referenceModels(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ModelsResponse {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = request;
    _ = refusal;
    const catalog = try arena.dupe(oap_types.ModelDescriptor, &.{
        .{ .id = "reference-a", .display_name = "Reference A", .provider_id = "reference", .context_window = 4096, .default = true },
    });
    return .{ .session_id = try arena.dupe(u8, referenceId(state)), .models = catalog };
}

fn referenceTools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ToolsListResponse {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = request;
    _ = refusal;
    const catalog = try arena.dupe(oap_types.ToolDefinition, &.{
        .{ .name = "echo", .input_schema_json = "{\"type\":\"object\"}", .execution_owner = "client" },
    });
    return .{ .session_id = try arena.dupe(u8, referenceId(state)), .tools = catalog };
}

fn referenceId(ptr: *anyopaque) []const u8 {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    return state.id();
}

fn referenceState(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = refusal;
    const session_id = try arena.dupe(u8, referenceId(state));
    if (!state.running) return .{ .session_id = session_id, .status = .idle };
    const run = try arena.dupe(u8, "run-1");
    const runs = try arena.dupe(oap_types.ActiveRun, &.{.{ .run_id = run, .status = .running, .relationship = "primary" }});
    return .{ .session_id = session_id, .status = .running, .active_run_id = run, .active_runs = runs };
}

fn referenceSubmit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = request;
    _ = refusal;
    state.running = true;
    if (state.run_count < state.runs.len) {
        state.runs[state.run_count] = "run-1";
        state.run_count += 1;
    }
    for (scripted_envelopes) |line| {
        if (state.outbox_len < state.outbox.len) {
            state.outbox[state.outbox_len] = line;
            state.outbox_len += 1;
        }
    }
    const session_id = try arena.dupe(u8, referenceId(state));
    const submission = try arena.dupe(u8, "s1");
    const run = try arena.dupe(u8, "run-1");
    return .{
        .session_id = session_id,
        .accepted = true,
        .submission_id = submission,
        .requested_delivery = .auto,
        .effective_delivery = .start,
        .admission = .started,
        .run_id = run,
        .status = .running,
    };
}

fn referenceResolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
    _ = ptr;
    _ = arena;
    _ = resolution;
    _ = refusal;
}

fn referenceCancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = refusal;
    if (!state.knows(run_id)) return error.RunNotFound;
    state.running = false;
    const session_id = try arena.dupe(u8, referenceId(state));
    const named = try arena.dupe(u8, run_id);
    return .{ .session_id = session_id, .run_id = named, .accepted = true, .status = .cancelling };
}

fn referencePump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
    _ = ptr;
    _ = wait_ns;
    return false;
}

fn referenceDrain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    var index: usize = 0;
    while (index < state.outbox_len) : (index += 1) {
        const line = try allocator.dupe(u8, state.outbox[index]);
        const run = try allocator.dupe(u8, if (state.run_count > 0) state.runs[0] else "run-1");
        try out.append(allocator, .{ .line = line, .run_id = run, .sequence = index + 1 });
    }
    state.outbox_len = 0;
}

fn referenceActivity(ptr: *anyopaque) contract.Activity {
    _ = ptr;
    return .idle;
}

fn referenceClose(ptr: *anyopaque) void {
    _ = ptr;
}

fn textMember(arena: std.mem.Allocator, document: std.json.Value, key: []const u8) ![]const u8 {
    if (document != .object) return error.Shape;
    const value = document.object.get(key) orelse return error.Shape;
    if (value != .string) return error.Shape;
    return arena.dupe(u8, value.string);
}

test "a framing defect stops serving, and the daemon says so" {
    const defects = [_][]const u8{
        "",
        "{\"id\":1,\"op\":\"adapters\"}\r",
        "{\"id\":1,\"op\":\"adapters\"",
        "{\"op\":\"adapters\"}",
        "{\"id\":1}",
        "{\"id\":1.5,\"op\":\"adapters\"}",
        "{\"id\":1,\"id\":2,\"op\":\"adapters\"}",
        "{\"id\":1,\"op\":\"adapters\",\"bogus\":1}",
        "{\"id\":1,\"op\":\"adapters\"} trailing",
        "[]",
        "{\"id\":9223372036854775808,\"op\":\"adapters\"}",
    };
    for (defects) |defect| {
        const harness = try Harness.init(testing.allocator, .{}, .{});
        defer harness.deinit();
        try testing.expectError(Error.MalformedLine, harness.send(defect));
        try testing.expect(harness.frontend.stopped);
        try testing.expectEqual(@as(usize, 0), harness.recorder.lines.items.len);
    }
}

test "a line over the frame limit is a defect, and the limit is not negotiable below the floor" {
    var harness = try Harness.init(testing.allocator, .{}, .{ .frame_limit = minimum_frame_limit });
    defer harness.deinit();
    var oversized: [minimum_frame_limit + 1]u8 = undefined;
    @memset(&oversized, 'x');
    try testing.expectError(Error.FrameLimitTooSmall, harness.send(&oversized));
    try testing.expect(harness.frontend.stopped);
    try testing.expectError(Error.FrameLimitTooSmall, Frontend.init(testing.allocator, &harness.hub, harness.recorder.sink(), .{ .frame_limit = minimum_frame_limit - 1 }));
}

test "a parameter an op does not define is refused, and a supplied null still counts" {
    const cases = [_][]const u8{
        "{\"id\":1,\"op\":\"adapters\",\"session_id\":\"s\"}",
        "{\"id\":1,\"op\":\"sessions\",\"adapter\":\"reference\"}",
        "{\"id\":1,\"op\":\"adapters\",\"adapter\":null}",
        "{\"id\":1,\"op\":\"close\",\"adapter\":\"reference\"}",
    };
    for (cases) |line| {
        const harness = try Harness.init(testing.allocator, .{}, .{});
        defer harness.deinit();
        try harness.send(line);
        try testing.expectEqualStrings("invalid_request", try harness.code());
        try testing.expectEqual(@as(i64, 1), (try harness.lastValue()).object.get("id").?.integer);
    }
}

test "an op the frontend does not serve is a correlated refusal, not a defect" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":-7,\"op\":\"nonsense\"}");
    const answer = try harness.lastValue();
    try testing.expectEqual(@as(i64, -7), answer.object.get("id").?.integer);
    try testing.expectEqualStrings("unknown_op", try harness.code());
    try testing.expect(!harness.frontend.stopped);
}

test "the adapters listing answers every registered adapter, sorted, with its revision" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"adapters\"}");
    const answer = try harness.lastValue();
    try testing.expect(answer.object.get("ok").?.bool);
    const adapters = answer.object.get("result").?.object.get("adapters").?.array;
    try testing.expectEqual(@as(usize, 2), adapters.items.len);
    try testing.expectEqualStrings("reference", try textMember(harness.arena(), adapters.items[0], "name"));
    try testing.expectEqualStrings("zebra", try textMember(harness.arena(), adapters.items[1], "name"));
    try testing.expectEqualStrings("reference-v1", try textMember(harness.arena(), adapters.items[0], "capability_revision"));
    const capabilities = adapters.items[0].object.get("capabilities").?;
    const features = capabilities.object.get("features").?.object;
    try testing.expectEqualStrings("native", try textMember(harness.arena(), features.get("session.open.subscribe").?, "level"));
    try testing.expectEqualStrings("degraded", try textMember(harness.arena(), features.get("session.tool_sources.attach").?, "level"));
    try testing.expectEqualStrings("sources are recorded, not loaded", try textMember(harness.arena(), features.get("session.tool_sources.attach").?, "reason"));
    const emulated = features.get("run.cancel").?;
    try testing.expectEqualStrings("primary", try textMember(harness.arena(), emulated, "scope"));
    try testing.expectEqual(@as(i64, 2), emulated.object.get("constraints").?.object.get("max").?.integer);
}

test "an adapter whose probe fails is listed with an error member, not dropped" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.hub.register("aardvark", fading());
    try harness.send("{\"id\":1,\"op\":\"adapters\"}");
    const adapters = (try harness.lastValue()).object.get("result").?.object.get("adapters").?.array;
    try testing.expectEqual(@as(usize, 3), adapters.items.len);
    try testing.expectEqualStrings("aardvark", try textMember(harness.arena(), adapters.items[0], "name"));
    try testing.expect(adapters.items[0].object.get("error") != null);
    try testing.expect(adapters.items[0].object.get("capabilities") == null);
    try testing.expect(adapters.items[1].object.get("error") == null);
}

test "the sessions listing is sorted by session id and reports the adapter" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    _ = try harness.hub.open(arena, "zebra", .{ .session_id = "zulu" });
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "alpha" });
    try harness.send("{\"id\":1,\"op\":\"sessions\"}");
    const sessions = (try harness.lastValue()).object.get("result").?.object.get("sessions").?.array;
    try testing.expectEqual(@as(usize, 2), sessions.items.len);
    try testing.expectEqualStrings("alpha", try textMember(harness.arena(), sessions.items[0], "session_id"));
    try testing.expectEqualStrings("reference", try textMember(harness.arena(), sessions.items[0], "adapter"));
    try testing.expectEqualStrings("zulu", try textMember(harness.arena(), sessions.items[1], "session_id"));
    try testing.expectEqualStrings("idle", try textMember(harness.arena(), sessions.items[0], "status"));
    try testing.expectEqualStrings("1970-01-01T00:00:00.000Z", try textMember(harness.arena(), sessions.items[0], "created_at"));
}

test "capabilities answers a probed descriptor as an envelope, and refuses an unknown one" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"capabilities\",\"adapter\":\"reference\"}");
    const answer = try harness.lastValue();
    const envelope = answer.object.get("result").?;
    try testing.expectEqualStrings("capabilities.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("reference-v1", try textMember(harness.arena(), envelope, "capability_revision"));
    try testing.expect(envelope.object.get("in_reply_to") != null);

    try harness.send("{\"id\":2,\"op\":\"capabilities\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"capabilities\",\"adapter\":\"nobody\"}");
    try testing.expectEqualStrings("unknown_adapter", try harness.code());
}

test "a close answers a bare null, and its refusals name the code the draft pins" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const opened = try harness.hub.open(arena, "reference", .{ .session_id = "closing" });
    try harness.send("{\"id\":1,\"op\":\"close\",\"session_id\":\"closing\"}");
    const answer = try harness.lastValue();
    try testing.expect(answer.object.get("ok").?.bool);
    try testing.expect(answer.object.get("result").? == .null);
    try testing.expectError(error.SessionClosed, harness.hub.state(arena, opened.session_id));

    try harness.send("{\"id\":2,\"op\":\"close\",\"session_id\":\"closing\"}");
    const repeated = try harness.lastValue();
    try testing.expect(repeated.object.get("ok").?.bool);
    try harness.send("{\"id\":3,\"op\":\"close\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":4,\"op\":\"close\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
}

test "a close of a session with a run in flight is run_active" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "busy" });
    const request = oap_types.MessageSubmitRequest{ .session_id = "busy", .messages = &.{}, .delivery = .auto };
    _ = try harness.hub.submit(arena, "busy", &request);
    try harness.send("{\"id\":1,\"op\":\"close\",\"session_id\":\"busy\"}");
    try testing.expectEqualStrings("run_active", try harness.code());
}

test "state answers a daemon-minted envelope and refuses an unknown session" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "asked" });
    try harness.send("{\"id\":1,\"op\":\"state\",\"session_id\":\"asked\"}");
    const envelope = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("session.state.response", try textMember(harness.arena(), envelope, "type"));
    try harness.send("{\"id\":2,\"op\":\"state\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"state\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
}

test "a refusal's message is bounded so a long one still frames" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const name = "adapter-" ++ "n" ** 400;
    const line = try std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"capabilities\",\"adapter\":\"{s}\"}}", .{name});
    try harness.send(line);
    const message = (try harness.lastValue()).object.get("error").?.object.get("message").?.string;
    try testing.expect(std.mem.endsWith(u8, message, "\u{2026}"));
    try testing.expect(message.len <= message_limit + 4);
    try testing.expect(std.unicode.utf8ValidateSlice(message));
}

test "the in-flight bound refuses any op, naming the bound that refused it" {
    const harness = try Harness.init(testing.allocator, .{}, .{ .max_ops = 1 });
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"adapters\"}");
    try testing.expectEqual(@as(usize, 0), harness.frontend.in_flight);
    harness.frontend.in_flight = 1;
    try harness.send("{\"id\":2,\"op\":\"sessions\"}");
    const message = (try harness.lastValue()).object.get("error").?.object.get("message").?.string;
    try testing.expect(std.mem.indexOf(u8, message, "send this request again") != null);
}

const fading_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "aardvark", .name = "Aardvark", .version = "0.1", .adapter = "script" },
    .capability_revision = "aardvark-v1",
    .features = &.{},
};

const Fading = struct {
    registered: bool = false,
};

var fading_holder: Fading = .{};

fn fading() contract.Adapter {
    return .{ .ptr = @constCast(@ptrCast(&fading_holder)), .vtable = &.{ .probe = fadingProbe, .open = referenceOpen } };
}

fn fadingProbe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
    const state: *Fading = @ptrCast(@alignCast(ptr));
    _ = refusal;
    if (state.registered) return .{ .endpoint = fading_descriptor.endpoint, .capability_revision = "", .features = &.{} };
    state.registered = true;
    return fading_descriptor;
}

fn openLine(harness: *Harness, extra: []const u8) ![]const u8 {
    return std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"req-1\",\"payload\":{{{s}}}}}}}", .{extra});
}

test "an open answers the whole state document, correlated and stamped with the revision" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    const answer = try harness.lastValue();
    try testing.expect(answer.object.get("ok").?.bool);
    const envelope = answer.object.get("result").?;
    try testing.expectEqualStrings("session.open.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("req-1", try textMember(harness.arena(), envelope, "in_reply_to"));
    try testing.expectEqualStrings("reference-v1", try textMember(harness.arena(), envelope, "capability_revision"));
    const state = envelope.object.get("payload").?;
    try testing.expectEqualStrings("alpha", try textMember(harness.arena(), state, "session_id"));
    try testing.expectEqualStrings("idle", try textMember(harness.arena(), state, "status"));
}

test "an open refuses a payload that is not a session.open.request" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"payload\":{}}}");
    try testing.expectEqualStrings("schema_invalid", try harness.code());
    try harness.send("{\"id\":2,\"op\":\"open\",\"adapter\":\"reference\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.cancel.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"alpha\",\"run_id\":\"run-1\"}}}");
    try testing.expectEqualStrings("type_mismatch", try harness.code());
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try testing.expectEqualStrings("session_exists", try harness.code());
    try testing.expect(!harness.frontend.stopped);
}

test "an open refuses an unknown adapter and a missing one" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"open\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"req-1\",\"payload\":{}}}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":2,\"op\":\"open\",\"adapter\":\"nobody\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"req-2\",\"payload\":{}}}");
    try testing.expectEqualStrings("unknown_adapter", try harness.code());
}

test "an open's compound message must be JSON, or the request is invalid_payload" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\",\"message\":\"{not json\""));
    try testing.expectEqualStrings("schema_invalid", try harness.code());
    try testing.expectEqual(@as(usize, 0), harness.hub.sessionCount());
}

test "a submit answers the admission, correlated to the request envelope" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    const envelope = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("session.message.submit.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("req-2", try textMember(harness.arena(), envelope, "in_reply_to"));
    try testing.expectEqualStrings("run-1", try textMember(harness.arena(), envelope.object.get("payload").?, "run_id"));
}

test "a submit of another session is scope_mismatch, and an unknown one is unknown_session" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"elsewhere\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try testing.expectEqualStrings("scope_mismatch", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"submit\",\"session_id\":\"absent\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-3\",\"payload\":{\"session_id\":\"absent\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":4,\"op\":\"submit\",\"session_id\":\"alpha\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
}

test "a cancel is intent, and a payload naming another session is scope_mismatch" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"cancel\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.cancel.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"elsewhere\",\"run_id\":\"run-1\"}}}");
    try testing.expectEqualStrings("scope_mismatch", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"cancel\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.cancel.request\",\"id\":\"req-3\",\"payload\":{\"session_id\":\"alpha\",\"run_id\":\"run-9\"}}}");
    try testing.expectEqualStrings("run_not_found", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-3\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try harness.send("{\"id\":4,\"op\":\"cancel\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.cancel.request\",\"id\":\"req-4\",\"payload\":{\"session_id\":\"alpha\",\"run_id\":\"run-1\"}}}");
    const envelope = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("run.cancel.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("cancelling", try textMember(harness.arena(), envelope.object.get("payload").?, "status"));
}

test "resolve takes all three request types and refuses a foreign session on each" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    const cases = [_][]const u8{
        "{\"id\":2,\"op\":\"resolve\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"action.permission.resolve.request\",\"id\":\"req-2\",\"payload\":{\"interaction_id\":\"permission-1\",\"session_id\":\"elsewhere\",\"run_id\":\"run-1\",\"requested_by\":\"oapx\",\"responded_by\":\"user\",\"granted\":true}}}",
        "{\"id\":3,\"op\":\"resolve\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"user.input.resolve.request\",\"id\":\"req-3\",\"payload\":{\"interaction_id\":\"input-1\",\"session_id\":\"elsewhere\",\"run_id\":\"run-1\",\"requested_by\":\"oapx\",\"responded_by\":\"user\",\"answers\":[{\"question_id\":\"choice\",\"selected_option_ids\":[\"yes\"]}]}}}",
        "{\"id\":4,\"op\":\"resolve\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"action.call.resolve.request\",\"id\":\"req-4\",\"payload\":{\"interaction_id\":\"call-1\",\"session_id\":\"elsewhere\",\"run_id\":\"run-1\",\"tool_call_id\":\"tool-1\",\"requested_by\":\"oapx\",\"responded_by\":\"user\",\"result\":{\"type\":\"text\",\"text\":\"done\"}}}}",
    };
    for (cases) |line| {
        try harness.send(line);
        try testing.expectEqualStrings("scope_mismatch", try harness.code());
    }
    try harness.send("{\"id\":5,\"op\":\"resolve\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"action.permission.resolve.request\",\"id\":\"req-5\",\"payload\":{\"interaction_id\":\"permission-1\",\"session_id\":\"alpha\",\"run_id\":\"run-1\",\"requested_by\":\"oapx\",\"responded_by\":\"user\",\"granted\":true}}}");
    const envelope = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("action.permission.resolve.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("permission-1", try textMember(harness.arena(), envelope.object.get("payload").?, "interaction_id"));
}

test "a catalog is stamped with the revision it was served under" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"models\",\"session_id\":\"alpha\"}");
    var envelope = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("models.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("reference-v1", try textMember(harness.arena(), envelope, "capability_revision"));
    try testing.expectEqualStrings("reference-a", try textMember(harness.arena(), envelope.object.get("payload").?.object.get("models").?.array.items[0], "id"));

    try harness.send("{\"id\":3,\"op\":\"tools\",\"session_id\":\"alpha\"}");
    envelope = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("action.tools.list.response", try textMember(harness.arena(), envelope, "type"));
    try testing.expectEqualStrings("reference-v1", try textMember(harness.arena(), envelope, "capability_revision"));
    try testing.expectEqualStrings("echo", try textMember(harness.arena(), envelope.object.get("payload").?.object.get("tools").?.array.items[0], "name"));

    try harness.send("{\"id\":4,\"op\":\"models\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":5,\"op\":\"tools\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
}

fn eventMember(harness: *Harness, line: []const u8, key: []const u8) !std.json.Value {
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, harness.arena(), line, .{});
    if (parsed != .object) return error.Shape;
    return parsed.object.get(key) orelse error.Shape;
}

test "an events op acknowledges before the stream, and every envelope line is tagged with its id" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try harness.send("{\"id\":7,\"op\":\"events\",\"session_id\":\"alpha\"}");
    const acknowledgement = harness.recorder.lines.items[harness.recorder.lines.items.len - 1];
    try testing.expectEqualStrings("{\"id\":7,\"ok\":true,\"result\":null}", acknowledgement);
    try harness.frontend.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 7), harness.recorder.lines.items.len);
    const first = harness.recorder.lines.items[3];
    const first_event = try eventMember(harness, first, "event");
    if (first_event != .string) return error.Shape;
    try testing.expectEqualStrings("envelope", first_event.string);
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, harness.arena(), first, .{});
    try testing.expectEqual(@as(i64, 7), parsed.object.get("id").?.integer);
    try testing.expectEqualStrings("alpha", parsed.object.get("session_id").?.string);
    try testing.expectEqual(@as(i64, 1), parsed.object.get("sequence").?.integer);
    try testing.expectEqualStrings("run.started", try textMember(harness.arena(), parsed.object.get("envelope").?, "type"));
}

test "a run's terminal envelope is the marker, and no end line follows it" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try harness.send("{\"id\":7,\"op\":\"events\",\"session_id\":\"alpha\"}");
    try harness.frontend.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), harness.frontend.subscriptions.items.len);
    var terminals: usize = 0;
    for (harness.recorder.lines.items) |line| {
        if (std.mem.indexOf(u8, line, "\"run.completed\"") != null) terminals += 1;
    }
    try testing.expectEqual(@as(usize, 1), terminals);
}

test "a subscription that joins mid-run is told where it began, and the signal never ends it" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try harness.frontend.pump(testing.allocator, 0);
    try harness.send("{\"id\":7,\"op\":\"events\",\"session_id\":\"alpha\"}");
    const join = harness.recorder.last();
    try testing.expect(std.mem.indexOf(u8, join, "\"oap-subscribed\"") != null);
    try testing.expect(std.mem.indexOf(u8, join, "\"joined_after\":4") != null);
    try testing.expectEqual(hubmod.Ending.open, harness.frontend.subscriptions.items[0].subscription.ending);
}

test "a replay gap is not an error: the op acknowledges, then reports the gap and ends" {
    const harness = try Harness.init(testing.allocator, .{ .journal_capacity = 2, .stream_queue = 8 }, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"events\",\"session_id\":\"alpha\",\"run_id\":\"run-77\",\"after\":0}");
    try testing.expectEqualStrings("run_not_found", try harness.code());

    try harness.send("{\"id\":3,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-3\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try harness.frontend.pump(testing.allocator, 0);
    try harness.send("{\"id\":4,\"op\":\"events\",\"session_id\":\"alpha\",\"run_id\":\"run-1\",\"after\":1}");
    const gap = harness.recorder.last();
    const acknowledgement = harness.recorder.lines.items[harness.recorder.lines.items.len - 2];
    try testing.expectEqualStrings("{\"id\":4,\"ok\":true,\"result\":null}", acknowledgement);
    try testing.expect(std.mem.indexOf(u8, gap, "\"oap-replay-gap\"") != null);
    try testing.expect(std.mem.indexOf(u8, gap, "\"requested_after\":1") != null);
    try testing.expectEqual(@as(usize, 0), harness.frontend.subscriptions.items.len);
}

test "an events op refuses a run without a cursor, a closed session, and an unknown one" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"events\",\"session_id\":\"alpha\",\"run_id\":\"run-1\"}");
    try testing.expectEqualStrings("invalid_cursor", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"events\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":4,\"op\":\"events\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    harness.hub.close(harness.arena(), "alpha") catch {};
    try harness.send("{\"id\":5,\"op\":\"events\",\"session_id\":\"alpha\"}");
    try testing.expectEqualStrings("session_closed", try harness.code());
}

test "a subscriber that falls behind is ended out loud with the cursor to resume from" {
    const harness = try Harness.init(testing.allocator, .{ .stream_queue = 2, .journal_capacity = 32 }, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"submit\",\"session_id\":\"alpha\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.message.submit.request\",\"id\":\"req-2\",\"payload\":{\"session_id\":\"alpha\",\"messages\":[{\"role\":\"user\",\"content\":\"run\"}],\"delivery\":\"auto\"}}}");
    try harness.send("{\"id\":7,\"op\":\"events\",\"session_id\":\"alpha\"}");
    try harness.frontend.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), harness.frontend.subscriptions.items.len);
    const end = harness.recorder.last();
    try testing.expect(std.mem.indexOf(u8, end, "\"oap-overflow\"") != null);
    try testing.expect(std.mem.indexOf(u8, end, "\"last_sequence\":0") != null);
}

test "the session closing under a subscription is announced" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness, "\"session_id\":\"alpha\""));
    try harness.send("{\"id\":2,\"op\":\"events\",\"session_id\":\"alpha\"}");
    try testing.expectEqual(@as(usize, 1), harness.frontend.subscriptions.items.len);
    harness.hub.closeSessions();
    try harness.frontend.pump(testing.allocator, 0);
    const end = harness.recorder.last();
    try testing.expect(std.mem.indexOf(u8, end, "\"oap-session-closed\"") != null);
    try testing.expectEqual(@as(usize, 0), harness.frontend.subscriptions.items.len);
}
