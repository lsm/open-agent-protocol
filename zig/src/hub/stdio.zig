const std = @import("std");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");
const json_encode = @import("json_encode");
const json_writer = @import("json_writer");
const contract = @import("contract");
const jsonschema = @import("jsonschema");
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
pub const op_settings = "settings";
pub const op_events = "events";
pub const op_history = "history";
pub const op_work_status = "work.status";
pub const op_work_list = "work.list";
pub const op_work_start = "work.start";
pub const op_work_send = "work.send";
pub const op_work_stop = "work.stop";
pub const op_work_read = "work.read";
pub const work_read_default: usize = 100;
pub const work_read_max: usize = 500;

pub const Error = error{
    MalformedLine,
    FrameLimitTooSmall,
    OutputStalled,
    StdinFailed,
    InputFailed,
    BackendRefused,
    ResponseTooLarge,
    NoSpaceLeft,
    MalformedJson,
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
    cursor: bool = false,
    limit: bool = false,
};

const no_parameters: []const []const u8 = &.{};
const session_and_degraded: []const []const u8 = &.{ "session_id", "allow_degraded_features" };
const adapter_parameter: []const []const u8 = &.{"adapter"};
const open_parameters: []const []const u8 = &.{ "adapter", "request" };
const max_envelope_bytes: usize = 16 << 20;
const session_parameter: []const []const u8 = &.{"session_id"};
const events_parameters: []const []const u8 = &.{ "session_id", "run_id", "after" };
const session_request_parameters: []const []const u8 = &.{ "session_id", "request" };
const history_parameters: []const []const u8 = &.{ "cursor", "limit" };
const work_start_parameters: []const []const u8 = &.{ "adapter", "request" };
const request_parameter: []const []const u8 = &.{"request"};
const work_read_parameters: []const []const u8 = &.{ "session_id", "after", "limit" };
const all_parameters = [_][]const u8{ "adapter", "session_id", "run_id", "after", "request", "cursor", "limit", "allow_degraded_features" };

pub const Request = struct {
    id: i64,
    op: []const u8,
    adapter: ?[]const u8 = null,
    session_id: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
    after: ?u64 = null,
    payload: ?std.json.Value = null,
    allow_degraded_features: []const []const u8 = &.{},
    cursor: ?[]const u8 = null,
    limit: ?i64 = null,
    supplied: Supplied = .{},
    after_unreadable: bool = false,

    fn takes(self: Request, parameter: []const u8) bool {
        if (std.mem.eql(u8, parameter, "adapter")) return self.supplied.adapter;
        if (std.mem.eql(u8, parameter, "session_id")) return self.supplied.session_id;
        if (std.mem.eql(u8, parameter, "run_id")) return self.supplied.run_id;
        if (std.mem.eql(u8, parameter, "after")) return self.supplied.after;
        if (std.mem.eql(u8, parameter, "request")) return self.supplied.request;
        if (std.mem.eql(u8, parameter, "allow_degraded_features")) return self.supplied.allow_degraded_features;
        if (std.mem.eql(u8, parameter, "cursor")) return self.supplied.cursor;
        if (std.mem.eql(u8, parameter, "limit")) return self.supplied.limit;
        unreachable;
    }

    fn only(self: Request, arena: std.mem.Allocator, parameters: []const []const u8) !?Refusal {
        var extra: std.ArrayList([]const u8) = .empty;
        for (all_parameters) |parameter| {
            if (declares(parameters, parameter) or !self.takes(parameter)) continue;
            try extra.append(arena, parameter);
        }
        if (extra.items.len == 0) return null;
        const message = try std.fmt.allocPrint(arena, "op \"{s}\" accepts no {s} parameter", .{
            self.op,
            try std.mem.join(arena, ", ", extra.items),
        });
        return Refusal{ .code = "invalid_request", .message = message };
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
fn refusalWith(
    arena: std.mem.Allocator,
    code: []const u8,
    message: []const u8,
    details: []const oap_types.DetailEntry,
) std.mem.Allocator.Error!Refusal {
    return Refusal{ .code = code, .message = message, .details = try arena.dupe(oap_types.DetailEntry, details) };
}

fn substitutedSources(
    arena: std.mem.Allocator,
    hub: *const hubmod.Hub,
    wire: ?[]const u8,
) Error!?[]const u8 {
    const listed = wire orelse return null;
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, listed, .{}) catch |failure| switch (failure) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return null,
    };
    if (parsed != .array) return null;
    var buffer: std.ArrayList(u8) = .empty;
    var writer = json_writer.JsonWriter.init(&buffer, arena);
    try writer.beginArray();
    for (parsed.array.items) |entry| {
        if (entry != .object) continue;
        const id = if (entry.object.get("id")) |value| (if (value == .string) value.string else "") else "";
        const configured = if (id.len > 0) hub.toolSource(id) else null;
        if (configured) |source| {
            try writeConfiguredSource(&writer, source, entry.object);
        } else {
            const kept = try std.json.Stringify.valueAlloc(arena, entry, .{});
            try writer.writeRawJson(kept);
        }
    }
    try writer.endArray();
    return buffer.toOwnedSlice(arena) catch return error.OutOfMemory;
}

fn writeConfiguredSource(
    writer: *json_writer.JsonWriter,
    source: contract.ConfiguredSource,
    wire: std.json.ObjectMap,
) Error!void {
    try writer.beginObject();
    try writer.writeStringField("id", source.id);
    try writer.writeStringField("kind", source.kind);
    try writeMember(writer, "display_name", source.display_name);
    try writeMember(writer, "protocol", source.protocol);
    try writeMember(writer, "endpoint", source.endpoint);
    try writeMember(writer, "command", source.command);
    try writeList(writer, "args", source.args);
    try writeList(writer, "environment", mergedEnvironment(writer.allocator, source.environment, wire) catch return error.OutOfMemory);
    try writer.endObject();
}

fn writeMember(writer: *json_writer.JsonWriter, key: []const u8, value: []const u8) Error!void {
    if (value.len == 0) return;
    try writer.writeStringField(key, value);
}

fn writeList(writer: *json_writer.JsonWriter, key: []const u8, values: []const []const u8) Error!void {
    if (values.len == 0) return;
    try writer.writeKey(key);
    try writer.beginArray();
    for (values) |value| try writer.writeString(value);
    try writer.endArray();
}

fn mergedEnvironment(arena: std.mem.Allocator, operator: []const []const u8, wire: std.json.ObjectMap) ![]const []const u8 {
    var taken: std.StringHashMapUnmanaged(void) = .empty;
    defer taken.deinit(arena);
    var merged: std.ArrayList([]const u8) = .empty;
    for (operator) |entry| {
        try taken.put(arena, nameOf(entry), {});
        try merged.append(arena, entry);
    }
    const listed = wire.get("environment") orelse return try merged.toOwnedSlice(arena);
    if (listed != .array) return try merged.toOwnedSlice(arena);
    for (listed.array.items) |entry| {
        if (entry != .string) continue;
        const name = nameOf(entry.string);
        if (taken.contains(name)) continue;
        try taken.put(arena, name, {});
        try merged.append(arena, entry.string);
    }
    return try merged.toOwnedSlice(arena);
}

fn nameOf(entry: []const u8) []const u8 {
    const cut = std.mem.indexOfScalar(u8, entry, '=') orelse return entry;
    return entry[0..cut];
}

fn detailForReason(arena: std.mem.Allocator, reason: contract.Refusal) std.mem.Allocator.Error![]oap_types.DetailEntry {
    const named = [_]struct { key: []const u8, value: []const u8 }{
        .{ .key = "feature", .value = reason.feature },
        .{ .key = "reason", .value = reason.reason },
        .{ .key = "tool", .value = reason.tool },
        .{ .key = "field", .value = reason.field },
        .{ .key = "source", .value = reason.source },
    };
    var entries: [named.len]oap_types.DetailEntry = undefined;
    var count: usize = 0;
    for (named) |entry| {
        if (entry.value.len == 0) continue;
        entries[count] = .{ .key = entry.key, .value = entry.value };
        count += 1;
    }
    if (count == 0) return &.{};
    return try arena.dupe(oap_types.DetailEntry, entries[0..count]);
}

fn ownRevisions(arena: std.mem.Allocator, refused: *hubmod.OpenRefusal) std.mem.Allocator.Error!void {
    refused.expected_revision = try arena.dupe(u8, refused.expected_revision);
    refused.current_revision = try arena.dupe(u8, refused.current_revision);
}

fn featureOnly(arena: std.mem.Allocator, reason: contract.Refusal) std.mem.Allocator.Error![]oap_types.DetailEntry {
    if (reason.feature.len == 0) return &.{};
    return try arena.dupe(oap_types.DetailEntry, &.{.{ .key = "feature", .value = reason.feature }});
}

pub const refusal_statuses = [_]struct { code: []const u8, status: []const u8 }{
    .{ .code = "unknown_adapter", .status = "404 Not Found" },
    .{ .code = "unknown_session", .status = "404 Not Found" },
    .{ .code = "session_closed", .status = "409 Conflict" },
    .{ .code = "session_exists", .status = "409 Conflict" },
    .{ .code = "unsupported_feature", .status = "400 Bad Request" },
    .{ .code = "capability_degraded", .status = "400 Bad Request" },
    .{ .code = "scope_mismatch", .status = "400 Bad Request" },
    .{ .code = "request_cancelled", .status = "400 Bad Request" },
    .{ .code = "request_too_large", .status = "413 Payload Too Large" },
    .{ .code = "run_not_found", .status = "404 Not Found" },
    .{ .code = "unsupported_media_type", .status = "415 Unsupported Media Type" },
    .{ .code = "request_read", .status = "400 Bad Request" },
    .{ .code = "unrecognized_host", .status = "403 Forbidden" },
    .{ .code = "cross_origin_request", .status = "403 Forbidden" },
    .{ .code = "invalid_submission", .status = "400 Bad Request" },
    .{ .code = "invalid_cursor", .status = "400 Bad Request" },
    .{ .code = "replay_cursor_future", .status = "400 Bad Request" },
    .{ .code = "resolution_rejected", .status = "409 Conflict" },
    .{ .code = "run_terminal", .status = "409 Conflict" },
    .{ .code = "no_run_to_resume", .status = "409 Conflict" },
    .{ .code = "stale_capabilities", .status = "409 Conflict" },
    .{ .code = "run_active", .status = "409 Conflict" },
    .{ .code = "model_not_found", .status = "400 Bad Request" },
    .{ .code = "state_failed", .status = "500 Internal Server Error" },
    .{ .code = "history_failed", .status = "500 Internal Server Error" },
    .{ .code = "tools_failed", .status = "502 Bad Gateway" },
    .{ .code = "internal", .status = "500 Internal Server Error" },
    .{ .code = "probe_failed", .status = "500 Internal Server Error" },
    .{ .code = "open_failed", .status = "502 Bad Gateway" },
};

pub fn statusForRefusal(code: []const u8) ?[]const u8 {
    for (refusal_statuses) |entry| {
        if (std.mem.eql(u8, entry.code, code)) return entry.status;
    }
    return null;
}

pub fn featureReasonDetails(feature: []const u8, reason: []const u8) [2]oap_types.DetailEntry {
    return .{ .{ .key = "feature", .value = feature }, .{ .key = "reason", .value = reason } };
}

pub fn featureOnlyDetail(feature: []const u8) [1]oap_types.DetailEntry {
    return .{.{ .key = "feature", .value = feature }};
}

pub fn detailsJson(arena: std.mem.Allocator, details: []const oap_types.DetailEntry) Error!std.json.Value {
    var object = try emptyObject(arena);
    errdefer object.deinit(arena);
    for (details) |entry| try object.put(arena, entry.key, .{ .string = entry.value });
    return .{ .object = object };
}

fn requireStringOrNull(root: std.json.ObjectMap, name: []const u8) Error!void {
    const value = member(root, name) orelse return;
    if (value == .null or value == .string) return;
    return Error.MalformedLine;
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
    request.supplied.cursor = member(root, "cursor") != null;
    request.supplied.limit = member(root, "limit") != null;
    try requireStringOrNull(root, "cursor");
    request.cursor = stringMember(root, "cursor");
    if (member(root, "limit")) |limit| {
        if (limit == .integer) {
            request.limit = limit.integer;
        } else if (limit != .null) {
            return Error.MalformedLine;
        }
    }
    try requireStringOrNull(root, "adapter");
    try requireStringOrNull(root, "session_id");
    try requireStringOrNull(root, "run_id");
    request.adapter = stringMember(root, "adapter");
    request.session_id = stringMember(root, "session_id");
    request.run_id = stringMember(root, "run_id");
    if (request.supplied.after) {
        const after = member(root, "after").?;
        if (after == .null) {
            request.after = null;
        } else if (after == .integer and after.integer >= 0) {
            request.after = @intCast(after.integer);
        } else {
            request.after_unreadable = true;
        }
    }
    if (request.supplied.request) request.payload = member(root, "request");
    if (request.supplied.allow_degraded_features) {
        const degraded = member(root, "allow_degraded_features").?;
        if (degraded == .null) {
            request.allow_degraded_features = &.{};
        } else {
            if (degraded != .array) return Error.MalformedLine;
            const keys = try arena.alloc([]const u8, degraded.array.items.len);
            for (degraded.array.items, keys) |item, *slot| {
                if (item != .string) return Error.MalformedLine;
                slot.* = item.string;
            }
            request.allow_degraded_features = keys;
        }
    }
    return request;
}

fn allowed(key: []const u8) bool {
    const names = [_][]const u8{ "id", "op", "adapter", "session_id", "run_id", "after", "request", "cursor", "limit", "allow_degraded_features" };
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
pub const Defect = struct {
    line: [512]u8 = undefined,
    len: usize = 0,
    cut: bool = false,

    pub fn text(self: *const Defect) []const u8 {
        return self.line[0..self.len];
    }
};

fn subscribeRefusal(arena: std.mem.Allocator, err: hubmod.Failure, id: []const u8) !Refusal {
    return switch (err) {
        error.UnknownSession => .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{id}) },
        error.SessionClosed => .{ .code = "session_closed", .message = "the session is closed" },
        error.NoRunToResume => .{ .code = "no_run_to_resume", .message = "the session has no run to resume a cursor on" },
        error.ReplayCursorFuture => .{ .code = "replay_cursor_future", .message = "the cursor is past the latest sequence the run has emitted" },
        error.RunNotFound => .{ .code = "run_not_found", .message = "the session never had the run the cursor names" },
        error.InvalidCursor => .{ .code = "invalid_cursor", .message = "run_id names the run a cursor belongs to; it has no meaning without after" },
        error.SubscriptionFull => .{ .code = "busy", .message = "the session already has as many subscribers as the hub serves" },
        error.OutOfMemory => error.OutOfMemory,
        else => .{ .code = "internal", .message = @errorName(err) },
    };
}

fn detailsOf(arena: std.mem.Allocator, reported: *const contract.Refusal) ![]const oap_types.DetailEntry {
    var details = std.ArrayList(oap_types.DetailEntry).empty;
    try details.append(arena, .{ .key = "feature", .value = reported.feature });
    try details.append(arena, .{ .key = "reason", .value = reported.reason });
    if (reported.tool.len > 0) try details.append(arena, .{ .key = "tool", .value = reported.tool });
    if (reported.field.len > 0) try details.append(arena, .{ .key = "field", .value = reported.field });
    if (reported.source.len > 0) try details.append(arena, .{ .key = "source", .value = reported.source });
    return details.items;
}

fn messageOr(reported: *const contract.Refusal, fallback: []const u8) []const u8 {
    return if (reported.message.len > 0) reported.message else fallback;
}

pub fn controlRefusal(arena: std.mem.Allocator, err: hubmod.Failure, reported: *const contract.Refusal, id: []const u8) !Refusal {
    return switch (err) {
        error.UnknownSession => .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{id}) },
        error.ScopeMismatch => .{ .code = "scope_mismatch", .message = try std.fmt.allocPrint(arena, "the payload names a session other than \"{s}\"", .{id}) },
        error.SessionClosed => .{ .code = "session_closed", .message = messageOr(reported, "the session is closed") },
        error.RunActive => .{ .code = "run_active", .message = messageOr(reported, "the session already has a run in flight") },
        error.InvalidSubmission => .{ .code = "invalid_submission", .message = messageOr(reported, "the submission is invalid") },
        error.UnsupportedFeature, error.ToolCatalogUnavailable => {
            const message = if (reported.detail.len > 0)
                try std.fmt.allocPrint(arena, "unsupported input: {s} ({s}): {s}", .{ reported.feature, reported.reason, reported.detail })
            else
                try std.fmt.allocPrint(arena, "unsupported input: {s} ({s})", .{ reported.feature, reported.reason });
            const details = try detailsOf(arena, reported);
            return .{ .code = "unsupported_feature", .message = message, .details = details };
        },
        error.CapabilityDegraded => {
            const message = try std.fmt.allocPrint(arena, "unsupported input: {s} is degraded and was not opted into", .{reported.feature});
            const details = try arena.dupe(oap_types.DetailEntry, &.{.{ .key = "feature", .value = reported.feature }});
            return .{ .code = "capability_degraded", .message = message, .details = details };
        },
        error.ModelNotFound => {
            const message = try std.fmt.allocPrint(arena, "model not found: \"{s}\"", .{reported.model_id});
            const details = try arena.dupe(oap_types.DetailEntry, &.{.{ .key = "model_id", .value = reported.model_id }});
            return .{ .code = "model_not_found", .message = message, .details = details };
        },
        error.InvalidSteerTarget => .{
            .code = "invalid_steer_target",
            .message = messageOr(reported, "the steer names no run it can reach"),
            .details = try arena.dupe(oap_types.DetailEntry, &.{.{ .key = "reason", .value = reported.reason }}),
        },
        error.RunNotFound => .{ .code = "run_not_found", .message = messageOr(reported, "no such run") },
        error.RunTerminal => .{ .code = "run_terminal", .message = messageOr(reported, "the run has already settled") },
        error.InteractionNotFound, error.InvalidResolution => .{ .code = "resolution_rejected", .message = messageOr(reported, @errorName(err)) },
        error.OutOfMemory => error.OutOfMemory,
        else => .{ .code = "internal", .message = messageOr(reported, @errorName(err)) },
    };
}

pub const Frontend = struct {
    hub: *Hub,
    sink: Sink,
    allocator: std.mem.Allocator,
    frame_limit: usize,
    max_ops: usize,
    max_subscriptions: usize,
    in_flight: usize = 0,
    recorded: Defect = .{},
    next_envelope: u64 = 0,
    streams: std.ArrayList(Watched) = .empty,
    stopped: bool = false,

    const Watched = struct {
        id: i64,
        subscription: *hubmod.Subscription,
        run: std.ArrayList(u8) = .empty,
        sequence: u64 = 0,
    };

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
        for (self.streams.items) |*watched| {
            watched.subscription.close();
            watched.run.deinit(self.allocator);
        }
        self.streams.deinit(self.allocator);
        self.* = undefined;
    }

    fn events(self: *Frontend, arena: std.mem.Allocator, request: Request) Error!Outcome {
        if (try request.only(arena, events_parameters)) |refusal| return .{ .refused = refusal };
        const session_id = request.session_id orelse "";
        const run = request.run_id orelse "";
        if (!self.hub.knows(session_id)) {
            return .{ .refused = .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{session_id}) } };
        }
        if (request.after_unreadable) {
            return .{ .refused = .{ .code = "invalid_cursor", .message = "the cursor is not an unsigned sequence" } };
        }
        if (request.after == null and run.len > 0) {
            return .{ .refused = .{ .code = "invalid_cursor", .message = "run_id names the run a cursor belongs to; it has no meaning without after" } };
        }
        if (self.max_subscriptions > 0 and self.streams.items.len >= self.max_subscriptions) {
            return .{ .refused = .{ .code = "busy", .message = try std.fmt.allocPrint(arena, "the frontend already holds {d} subscriptions; a subscription ends at its run's terminal, at an overflow or stream failure, or when its session closes — send this request again once one has", .{self.max_subscriptions}) } };
        }
        const subscription = self.hub.subscribe(arena, session_id, .{ .run_id = run, .after = request.after }) catch |err| {
            return .{ .refused = try subscribeRefusal(arena, err, session_id) };
        };
        self.answer(request.id, .{ .null = {} }) catch |err| {
            subscription.close();
            return err;
        };
        if (subscription.gap) |gap| {
            defer subscription.close();
            var object = try signalObject(arena, "oap-replay-gap", request.id, session_id);
            try object.put(arena, "requested_after", .{ .integer = @intCast(gap.requested_after) });
            try object.put(arena, "oldest_available", .{ .integer = @intCast(gap.oldest_available) });
            try object.put(arena, "latest_available", .{ .integer = @intCast(gap.latest_available) });
            try object.put(arena, "message", .{ .string = "requested replay cursor is no longer retained; resume with a cursor at or after oldest_available - 1" });
            try self.writeSignal(arena, object);
            return .streaming;
        }
        if (subscription.joined) {
            var object = try signalObject(arena, "oap-subscribed", request.id, session_id);
            if (subscription.joined_run.len > 0) try object.put(arena, "run_id", .{ .string = subscription.joined_run });
            try object.put(arena, "joined_after", .{ .integer = @intCast(subscription.joined_after) });
            try object.put(arena, "message", .{ .string = "the subscription begins after this sequence; resubscribe with a cursor at or before it to replay what preceded this point" });
            try self.writeSignal(arena, object);
        }
        self.streams.append(self.allocator, .{ .id = request.id, .subscription = subscription, .sequence = request.after orelse 0 }) catch |err| {
            subscription.close();
            return err;
        };
        try self.pumpStreams();
        return .streaming;
    }

    pub fn pumpStreams(self: *Frontend) Error!void {
        var index: usize = 0;
        while (index < self.streams.items.len) {
            if (try self.feed(&self.streams.items[index])) {
                var ended = self.streams.orderedRemove(index);
                ended.subscription.close();
                ended.run.deinit(self.allocator);
            } else {
                index += 1;
            }
        }
    }

    fn feed(self: *Frontend, watched: *Watched) Error!bool {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();
        const subscription = watched.subscription;
        const session_json = try std.json.Stringify.valueAlloc(arena, subscription.session_id, .{});
        while (subscription.next()) |delivered| {
            const line = if (delivered.sequence > 0)
                try std.fmt.allocPrint(arena, "{{\"event\":\"envelope\",\"id\":{d},\"session_id\":{s},\"sequence\":{d},\"envelope\":{s}}}", .{ watched.id, session_json, delivered.sequence, delivered.line })
            else
                try std.fmt.allocPrint(arena, "{{\"event\":\"envelope\",\"id\":{d},\"session_id\":{s},\"envelope\":{s}}}", .{ watched.id, session_json, delivered.line });
            if (line.len > self.frame_limit) {
                var object = try signalObject(arena, "oap-frame-limit", watched.id, subscription.session_id);
                try object.put(arena, "run_id", .{ .string = delivered.run_id });
                try object.put(arena, "sequence", .{ .integer = @intCast(if (delivered.sequence > 0) delivered.sequence else watched.sequence) });
                try object.put(arena, "message", .{ .string = "envelope exceeds the frame limit; the subscription ended — resume with a cursor after this sequence to continue past it" });
                try self.writeSignal(arena, object);
                return true;
            }
            try self.write(line);
            if (!std.mem.eql(u8, watched.run.items, delivered.run_id)) {
                watched.run.clearRetainingCapacity();
                try watched.run.appendSlice(self.allocator, delivered.run_id);
                watched.sequence = 0;
            }
            if (delivered.sequence > watched.sequence) watched.sequence = delivered.sequence;
        }
        switch (subscription.ending) {
            .open => return false,
            .run_terminal, .expired => return true,
            .overflow => {
                var object = try signalObject(arena, "oap-overflow", watched.id, subscription.session_id);
                try object.put(arena, "run_id", .{ .string = subscription.overflow_run });
                try object.put(arena, "last_sequence", .{ .integer = @intCast(subscription.overflow_sequence) });
                try object.put(arena, "message", .{ .string = "event stream consumer fell behind; resume with a cursor after this sequence" });
                try self.writeSignal(arena, object);
                return true;
            },
            .session_closed => {
                var object = try signalObject(arena, "oap-session-closed", watched.id, subscription.session_id);
                try object.put(arena, "message", .{ .string = "the session is closed" });
                try self.writeSignal(arena, object);
                return true;
            },
            .stream_failed => {
                var object = try signalObject(arena, "oap-stream-failed", watched.id, subscription.session_id);
                if (watched.run.items.len > 0) try object.put(arena, "run_id", .{ .string = watched.run.items });
                try object.put(arena, "sequence", .{ .integer = @intCast(watched.sequence) });
                try object.put(arena, "message", .{ .string = "the run's event stream failed; resume with a cursor after this sequence" });
                try self.writeSignal(arena, object);
                return true;
            },
        }
    }

    fn signalObject(arena: std.mem.Allocator, event: []const u8, id: i64, session_id: []const u8) Error!std.json.ObjectMap {
        var object = try emptyObject(arena);
        try object.put(arena, "event", .{ .string = event });
        try object.put(arena, "id", .{ .integer = id });
        if (session_id.len > 0) try object.put(arena, "session_id", .{ .string = session_id });
        return object;
    }

    fn writeSignal(self: *Frontend, arena: std.mem.Allocator, object: std.json.ObjectMap) Error!void {
        const line = json_encode.valueAlloc(arena, .{ .object = object }) catch return error.OutOfMemory;
        if (line.len <= self.frame_limit) return self.write(line);
        var minimal = try emptyObject(arena);
        var members = object.iterator();
        while (members.next()) |kept| {
            const essential = std.mem.eql(u8, kept.key_ptr.*, "event") or kept.value_ptr.* == .integer;
            if (essential) try minimal.put(arena, kept.key_ptr.*, kept.value_ptr.*);
        }
        const short = json_encode.valueAlloc(arena, .{ .object = minimal }) catch return error.OutOfMemory;
        if (short.len <= self.frame_limit) try self.write(short);
    }

    pub fn noteDefect(self: *Frontend, line: []const u8) void {
        const kept = @min(line.len, self.recorded.line.len);
        @memcpy(self.recorded.line[0..kept], line[0..kept]);
        self.recorded.len = kept;
        self.recorded.cut = line.len > kept;
    }
    pub fn defect(self: *const Frontend) ?[]const u8 {
        if (self.recorded.len == 0) return null;
        return self.recorded.text();
    }

    pub fn handleLine(self: *Frontend, line: []const u8) Error!void {
        if (self.stopped) return;
        self.recorded = .{};
        if (line.len > self.frame_limit) {
            self.stopped = true;
            self.noteDefect(line);
            return Error.FrameLimitTooSmall;
        }
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();
        const request = decode(arena, line) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {
                self.stopped = true;
                self.noteDefect(line);
                return Error.MalformedLine;
            },
        };
        if (self.in_flight >= self.max_ops) {
            const message = try std.fmt.allocPrint(arena, "the frontend is already running {d} operations; send this request again", .{self.max_ops});
            return self.refuse(arena, request.id, .{ .code = "busy", .message = message });
        }
        self.in_flight += 1;
        defer self.in_flight -= 1;
        const outcome = try self.dispatch(arena, request);
        switch (outcome) {
            .answer => |value| try self.answer(request.id, value),
            .answer_line => |payload| try self.answerLine(request.id, payload),
            .refused => |refusal| try self.refuse(arena, request.id, refusal),
            .streaming => return,
        }
    }

    pub const Outcome = union(enum) {
        answer: std.json.Value,
        answer_line: []const u8,
        refused: Refusal,
        streaming,
    };

    fn dispatch(self: *Frontend, arena: std.mem.Allocator, request: Request) Error!Outcome {
        if (std.mem.eql(u8, request.op, op_adapters)) {
            if (try request.only(arena, no_parameters)) |refusal| return .{ .refused = refusal };
            return .{ .answer = try self.adapters(arena) };
        }
        if (std.mem.eql(u8, request.op, op_sessions)) {
            if (try request.only(arena, no_parameters)) |refusal| return .{ .refused = refusal };
            return .{ .answer = try self.sessions(arena) };
        }
        if (std.mem.eql(u8, request.op, op_history)) {
            if (try request.only(arena, history_parameters)) |refusal| return .{ .refused = refusal };
            return self.history(arena, request.cursor, request.limit);
        }
        if (std.mem.eql(u8, request.op, op_work_status)) {
            if (try request.only(arena, session_parameter)) |refusal| return .{ .refused = refusal };
            return self.workStatus(arena, request.session_id orelse "");
        }
        if (std.mem.eql(u8, request.op, op_work_start)) {
            if (try request.only(arena, work_start_parameters)) |refusal| return .{ .refused = refusal };
            return self.workStart(arena, request.adapter orelse "", request.payload);
        }
        if (std.mem.eql(u8, request.op, op_work_send)) {
            if (try request.only(arena, session_request_parameters)) |refusal| return .{ .refused = refusal };
            return self.workSend(arena, request.session_id orelse "", request.payload);
        }
        if (std.mem.eql(u8, request.op, op_work_stop)) {
            if (try request.only(arena, session_parameter)) |refusal| return .{ .refused = refusal };
            return self.workStop(arena, request.session_id orelse "");
        }
        if (std.mem.eql(u8, request.op, op_work_read)) {
            if (try request.only(arena, work_read_parameters)) |refusal| return .{ .refused = refusal };
            if (request.after_unreadable) return .{ .refused = .{ .code = "invalid_request", .message = "after is a whole number" } };
            return self.workRead(arena, request.session_id orelse "", request.after, request.limit);
        }
        if (std.mem.eql(u8, request.op, op_work_list)) {
            if (try request.only(arena, request_parameter)) |refusal| return .{ .refused = refusal };
            return self.workList(arena, .{ .include_closed = workFlag(request.payload, "include_closed"), .include_native = workFlag(request.payload, "include_native") });
        }
        if (std.mem.eql(u8, request.op, op_capabilities)) {
            if (try request.only(arena, adapter_parameter)) |refusal| return .{ .refused = refusal };
            const name = request.adapter orelse "";
            if (name.len == 0) {
                return .{ .refused = .{ .code = "invalid_request", .message = "adapter is required" } };
            }
            return self.capabilities(arena, name);
        }
        if (std.mem.eql(u8, request.op, op_close)) {
            if (try request.only(arena, session_parameter)) |refusal| return .{ .refused = refusal };
            const session_id = request.session_id orelse "";
            return self.closeSession(arena, session_id);
        }
        if (std.mem.eql(u8, request.op, op_state)) {
            if (try request.only(arena, session_parameter)) |refusal| return .{ .refused = refusal };
            const session_id = request.session_id orelse "";
            return self.state(arena, session_id);
        }
        if (std.mem.eql(u8, request.op, op_open)) {
            if (try request.only(arena, open_parameters)) |refusal| return .{ .refused = refusal };
            const name = request.adapter orelse "";
            if (name.len == 0) {
                return .{ .refused = .{ .code = "invalid_request", .message = "adapter is required" } };
            }
            return self.openSession(arena, name, request, false);
        }
        if (std.mem.eql(u8, request.op, op_events)) return self.events(arena, request);
        if (std.mem.eql(u8, request.op, op_submit) or std.mem.eql(u8, request.op, op_resolve) or std.mem.eql(u8, request.op, op_cancel) or std.mem.eql(u8, request.op, op_settings)) {
            if (try request.only(arena, session_request_parameters)) |refusal| return .{ .refused = refusal };
            const session_id = request.session_id orelse "";
            const value = request.payload orelse std.json.Value{ .null = {} };
            const controlled = if (std.mem.eql(u8, request.op, op_submit))
                try self.submitControl(arena, session_id, value)
            else if (std.mem.eql(u8, request.op, op_resolve))
                try self.resolveControl(arena, session_id, value)
            else if (std.mem.eql(u8, request.op, op_cancel))
                try self.cancelControl(arena, session_id, value)
            else
                try self.settingsControl(arena, session_id, value);
            return switch (controlled) {
                .refused => |refused| .{ .refused = refused.refusal },
                .envelope => |envelope| .{ .answer_line = try oap_envelope.serializeEnvelope(envelope, arena) },
            };
        }
        if (std.mem.eql(u8, request.op, op_models)) {
            if (try request.only(arena, session_and_degraded)) |refusal| return .{ .refused = refusal };
            return self.models(arena, request.session_id orelse "", request.allow_degraded_features);
        }
        if (std.mem.eql(u8, request.op, op_tools)) {
            if (try request.only(arena, session_and_degraded)) |refusal| return .{ .refused = refusal };
            return self.tools(arena, request.session_id, request.allow_degraded_features);
        }
        return .{ .refused = .{ .code = "unknown_op", .message = try std.fmt.allocPrint(arena, "no op \"{s}\"", .{request.op}) } };
    }

    pub fn adapters(self: *Frontend, arena: std.mem.Allocator) !std.json.Value {
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

    pub fn sessions(self: *Frontend, arena: std.mem.Allocator) !std.json.Value {
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
                    if (active.queue_position) |position| try run_object.put(arena, "queue_position", .{ .integer = @intCast(position) });
                    if (active.as_of_sequence) |sequence| try run_object.put(arena, "as_of_sequence", .{ .integer = @intCast(sequence) });
                    if (active.admitted_submit_requests.len > 0) try run_object.put(arena, "admitted_submit_requests", try jsonStrings(arena, active.admitted_submit_requests));
                    if (active.pending_interactions.len > 0) try run_object.put(arena, "pending_interactions", try jsonStrings(arena, active.pending_interactions));
                    if (active.acknowledged_interactions.len > 0) try run_object.put(arena, "acknowledged_interactions", try jsonStrings(arena, active.acknowledged_interactions));
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

    pub fn history(self: *Frontend, arena: std.mem.Allocator, cursor: ?[]const u8, limit: ?i64) !Outcome {
        const limit_message = std.fmt.comptimePrint("binding: limit must be from 1 to {d}", .{hubmod.binding.max_limit});
        var bound: usize = 0;
        if (limit) |given| {
            if (given < 1 or given > hubmod.binding.max_limit) return .{ .refused = .{ .code = "invalid_request", .message = limit_message } };
            bound = @intCast(given);
        }
        const store = self.hub.bindings orelse return .{ .refused = try refusalWith(arena, "unsupported_feature", "serve: this hub keeps no session history", &.{
            .{ .key = "feature", .value = "session.list" },
            .{ .key = "reason", .value = "unadvertised" },
        }) };
        const unreadable = Refusal{ .code = "history_failed", .message = "the session history could not be read" };
        const recorded = store.sessions(arena) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return .{ .refused = unreadable },
        };
        const page = hubmod.binding.list(arena, recorded, cursor, bound) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            error.InvalidCursor => return .{ .refused = .{ .code = "invalid_cursor", .message = "binding: a cursor this history did not issue" } },
            error.InvalidLimit => return .{ .refused = .{ .code = "invalid_request", .message = limit_message } },
        };
        const entries = try arena.alloc(std.json.Value, page.entries.len);
        for (page.entries, entries) |entry, *slot| {
            var object = try emptyObject(arena);
            try object.put(arena, "session_id", .{ .string = entry.record.session_id });
            try object.put(arena, "adapter", .{ .string = entry.record.adapter });
            if (entry.record.harness_version.len > 0) try object.put(arena, "harness_version", .{ .string = entry.record.harness_version });
            try object.put(arena, "state", .{ .string = if (entry.action == .closed) "closed" else "live" });
            try object.put(arena, "updated_at_ms", .{ .integer = entry.time_ms });
            if (entry.record.model.len > 0) try object.put(arena, "model", .{ .string = entry.record.model });
            if (entry.record.directory.len > 0) try object.put(arena, "directory", .{ .string = entry.record.directory });
            slot.* = .{ .object = object };
        }
        var root = try emptyObject(arena);
        try root.put(arena, "sessions", try jsonArray(arena, entries));
        if (page.next_cursor) |next| try root.put(arena, "next_cursor", .{ .string = next });
        return .{ .answer = .{ .object = root } };
    }

    pub fn workStatus(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !Outcome {
        if (!self.hub.knows(session_id)) {
            if (try self.unheld(arena, session_id)) |entry| return .{ .answer = try unheldJson(arena, entry) };
        }
        const found = self.hub.work(arena, session_id) catch |err| {
            return .{ .refused = try self.stateRefusal(arena, err, session_id) };
        };
        return .{ .answer = try workJson(arena, found) };
    }

    fn workFlag(params: ?std.json.Value, name: []const u8) bool {
        const given = params orelse return false;
        if (given != .object) return false;
        const value = given.object.get(name) orelse return false;
        return value == .bool and value.bool;
    }

    fn latestBindings(self: *Frontend, arena: std.mem.Allocator) !?[]const hubmod.binding.Entry {
        const store = self.hub.bindings orelse return null;
        const recorded = store.sessions(arena) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return null,
        };
        return try hubmod.binding.latestEach(arena, recorded);
    }

    fn unheldJson(arena: std.mem.Allocator, entry: hubmod.binding.Entry) !std.json.Value {
        var ref = try emptyObject(arena);
        try ref.put(arena, "adapter", .{ .string = entry.record.adapter });
        try ref.put(arena, "session_id", .{ .string = entry.record.session_id });
        var object = try emptyObject(arena);
        try object.put(arena, "ref", .{ .object = ref });
        try object.put(arena, "held", .{ .bool = false });
        try object.put(arena, "state", .{ .string = if (entry.action == .closed) "closed" else "live" });
        if (entry.record.directory.len > 0) try object.put(arena, "directory", .{ .string = entry.record.directory });
        try object.put(arena, "updated_at_ms", .{ .integer = entry.time_ms });
        return .{ .object = object };
    }

    fn unheld(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !?hubmod.binding.Entry {
        const latest = (try self.latestBindings(arena)) orelse return null;
        for (latest) |entry| {
            if (std.mem.eql(u8, entry.record.session_id, session_id)) return entry;
        }
        return null;
    }

    pub const ListOptions = struct {
        include_closed: bool = false,
        include_native: bool = false,
    };

    fn nativeJson(arena: std.mem.Allocator, found: hubmod.Native) !std.json.Value {
        var ref = try emptyObject(arena);
        try ref.put(arena, "adapter", .{ .string = found.adapter });
        try ref.put(arena, "native_id", .{ .string = found.session.native_id });
        var object = try emptyObject(arena);
        try object.put(arena, "ref", .{ .object = ref });
        try object.put(arena, "held", .{ .bool = false });
        try object.put(arena, "native", .{ .bool = true });
        try object.put(arena, "state", .{ .string = if (found.session.running) "running" else "idle" });
        if (found.session.title.len > 0) try object.put(arena, "title", .{ .string = found.session.title });
        if (found.session.directory.len > 0) try object.put(arena, "directory", .{ .string = found.session.directory });
        try object.put(arena, "updated_at_ms", .{ .integer = found.session.updated_at_ms });
        return .{ .object = object };
    }

    pub fn workList(self: *Frontend, arena: std.mem.Allocator, options: ListOptions) !Outcome {
        const Piece = struct { directory: []const u8, at_ms: i64, value: std.json.Value };
        var pieces: std.ArrayList(Piece) = .empty;
        for (try self.hub.works(arena)) |piece| {
            try pieces.append(arena, .{ .directory = piece.directory, .at_ms = piece.updated_at_ms, .value = try workJson(arena, piece) });
        }
        var known_native: std.ArrayList([]const u8) = .empty;
        if (try self.latestBindings(arena)) |latest| {
            for (latest) |entry| {
                if (entry.record.native_session_id.len > 0) try known_native.append(arena, entry.record.native_session_id);
                if (self.hub.knows(entry.record.session_id)) continue;
                if (entry.action == .closed and !options.include_closed) continue;
                try pieces.append(arena, .{ .directory = entry.record.directory, .at_ms = entry.time_ms, .value = try unheldJson(arena, entry) });
            }
        }
        var unavailable: std.ArrayList(std.json.Value) = .empty;
        if (options.include_native) {
            const found = try self.hub.natives(arena, known_native.items);
            for (found.sessions) |native| {
                try pieces.append(arena, .{ .directory = native.session.directory, .at_ms = native.session.updated_at_ms, .value = try nativeJson(arena, native) });
            }
            for (found.failures) |failure| {
                var object = try emptyObject(arena);
                try object.put(arena, "adapter", .{ .string = failure.adapter });
                try object.put(arena, "message", .{ .string = try trim(arena, failure.message) });
                try unavailable.append(arena, .{ .object = object });
            }
        }
        std.mem.sort(Piece, pieces.items, {}, struct {
            fn newer(_: void, left: Piece, right: Piece) bool {
                return left.at_ms > right.at_ms;
            }
        }.newer);
        var directories: std.ArrayList([]const u8) = .empty;
        var members: std.ArrayList(std.ArrayList(std.json.Value)) = .empty;
        var latest_at: std.ArrayList(i64) = .empty;
        for (pieces.items) |piece| {
            const at = for (directories.items, 0..) |directory, index| {
                if (std.mem.eql(u8, directory, piece.directory)) break index;
            } else blk: {
                try directories.append(arena, piece.directory);
                try members.append(arena, .empty);
                try latest_at.append(arena, piece.at_ms);
                break :blk directories.items.len - 1;
            };
            try members.items[at].append(arena, piece.value);
        }
        var groups: std.ArrayList(std.json.Value) = .empty;
        for (directories.items, members.items, latest_at.items) |directory, listed, last| {
            var group = try emptyObject(arena);
            try group.put(arena, "directory", .{ .string = directory });
            try group.put(arena, "last_activity_ms", .{ .integer = last });
            try group.put(arena, "work", try jsonArray(arena, listed.items));
            try groups.append(arena, .{ .object = group });
        }
        var root = try emptyObject(arena);
        try root.put(arena, "groups", try jsonArray(arena, groups.items));
        if (unavailable.items.len > 0) try root.put(arena, "unavailable", try jsonArray(arena, unavailable.items));
        return .{ .answer = .{ .object = root } };
    }

    fn workEnvelope(self: *Frontend, arena: std.mem.Allocator, kind: []const u8, session_id: []const u8, payload: std.json.ObjectMap) !std.json.Value {
        self.next_envelope += 1;
        var envelope = try emptyObject(arena);
        try envelope.put(arena, "protocol", .{ .string = "open-agent-protocol" });
        try envelope.put(arena, "version", .{ .string = "0.1" });
        try envelope.put(arena, "profile", .{ .string = "open-agent-protocol.agent-control-core" });
        try envelope.put(arena, "type", .{ .string = kind });
        try envelope.put(arena, "id", .{ .string = try std.fmt.allocPrint(arena, "work-{d}", .{self.next_envelope}) });
        if (session_id.len > 0) try envelope.put(arena, "session_id", .{ .string = session_id });
        try envelope.put(arena, "payload", .{ .object = payload });
        return .{ .object = envelope };
    }

    fn userMessage(arena: std.mem.Allocator, text: []const u8) !std.json.Value {
        var message = try emptyObject(arena);
        try message.put(arena, "role", .{ .string = "user" });
        try message.put(arena, "content", .{ .string = text });
        var submitted = try emptyObject(arena);
        try submitted.put(arena, "messages", try jsonArray(arena, &.{.{ .object = message }}));
        try submitted.put(arena, "delivery", .{ .string = "auto" });
        return .{ .object = submitted };
    }

    fn workText(params: ?std.json.Value, name: []const u8) ?[]const u8 {
        const given = params orelse return null;
        if (given != .object) return null;
        const value = given.object.get(name) orelse return null;
        return if (value == .string) value.string else null;
    }

    fn workParamsRefusal(params: ?std.json.Value, required: []const u8) ?Refusal {
        const given = params orelse return .{ .code = "invalid_request", .message = "request is required" };
        if (given != .object) return .{ .code = "invalid_request", .message = "request is an object" };
        const value = given.object.get(required) orelse return .{ .code = "invalid_request", .message = "request.message is required" };
        if (value != .string or value.string.len == 0) return .{ .code = "invalid_request", .message = "request.message is a non-empty string" };
        return null;
    }

    pub fn workStart(self: *Frontend, arena: std.mem.Allocator, adapter: []const u8, params: ?std.json.Value) !Outcome {
        if (adapter.len == 0) return .{ .refused = .{ .code = "invalid_request", .message = "adapter is required" } };
        if (workParamsRefusal(params, "message")) |refusal| return .{ .refused = refusal };
        const configured = self.hub.adapterDirectory(adapter) orelse return .{ .refused = .{ .code = "unknown_adapter", .message = try std.fmt.allocPrint(arena, "no adapter is registered as \"{s}\"", .{adapter}) } };
        if (workText(params, "directory")) |directory| {
            if (!std.mem.eql(u8, directory, configured)) return .{ .refused = try refusalWith(arena, "invalid_request", "a session runs in its adapter's working directory; another directory needs its own adapter entry", &.{
                .{ .key = "working_directory", .value = configured },
            }) };
        }
        var open_payload = try emptyObject(arena);
        try open_payload.put(arena, "message", try userMessage(arena, workText(params, "message").?));
        const request = Request{
            .id = 0,
            .op = op_open,
            .adapter = adapter,
            .payload = try self.workEnvelope(arena, "session.open.request", "", open_payload),
            .supplied = .{ .adapter = true, .request = true },
        };
        const opened = try self.openSession(arena, adapter, request, false);
        const line = switch (opened) {
            .answer_line => |answered| answered,
            .refused => |refusal| return .{ .refused = refusal },
            else => return .{ .refused = .{ .code = "internal", .message = "the open answered without a session" } },
        };
        const opened_answer = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return .{ .refused = .{ .code = "internal", .message = "the open's answer could not be read back" } },
        };
        const session_id = opened_answer.object.get("session_id").?.string;
        if (workText(params, "title")) |title| try self.hub.setTitle(session_id, title);
        return self.workStatus(arena, session_id);
    }

    pub fn workSend(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8, params: ?std.json.Value) !Outcome {
        if (workParamsRefusal(params, "message")) |refusal| return .{ .refused = refusal };
        if (!self.hub.knows(session_id)) {
            if (try self.unheld(arena, session_id)) |entry| {
                if (try self.reopenFor(arena, entry)) |refusal| return .{ .refused = refusal };
            }
        }
        var submitted = (try userMessage(arena, workText(params, "message").?)).object;
        try submitted.put(arena, "session_id", .{ .string = session_id });
        const controlled = try self.submitControl(arena, session_id, try self.workEnvelope(arena, "session.message.submit.request", session_id, submitted));
        switch (controlled) {
            .refused => |refused| return .{ .refused = refused.refusal },
            .envelope => {},
        }
        return self.workStatus(arena, session_id);
    }

    fn reopenFor(self: *Frontend, arena: std.mem.Allocator, entry: hubmod.binding.Entry) !?Refusal {
        var open_payload = try emptyObject(arena);
        try open_payload.put(arena, "session_id", .{ .string = entry.record.session_id });
        try open_payload.put(arena, "reopen", .{ .bool = true });
        var envelope = try self.workEnvelope(arena, "session.open.request", "", open_payload);
        try envelope.object.put(arena, "session_id", .{ .string = entry.record.session_id });
        const request = Request{
            .id = 0,
            .op = op_open,
            .adapter = entry.record.adapter,
            .payload = envelope,
            .supplied = .{ .adapter = true, .request = true },
        };
        return switch (try self.openSession(arena, entry.record.adapter, request, false)) {
            .refused => |refusal| refusal,
            else => null,
        };
    }

    pub fn workStop(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !Outcome {
        if (!self.hub.knows(session_id)) {
            if (try self.unheld(arena, session_id)) |entry| return .{ .answer = try unheldJson(arena, entry) };
        }
        const current = self.hub.work(arena, session_id) catch |err| {
            return .{ .refused = try self.stateRefusal(arena, err, session_id) };
        };
        const live = current.status == .running or current.status == .queued or current.status == .needs_you;
        if (!live or current.run_id.len == 0) return .{ .answer = try workJson(arena, current) };
        var cancel = try emptyObject(arena);
        try cancel.put(arena, "session_id", .{ .string = session_id });
        try cancel.put(arena, "run_id", .{ .string = current.run_id });
        var envelope = try self.workEnvelope(arena, "run.cancel.request", session_id, cancel);
        try envelope.object.put(arena, "run_id", .{ .string = current.run_id });
        const controlled = try self.cancelControl(arena, session_id, envelope);
        switch (controlled) {
            .refused => |refused| return .{ .refused = refused.refusal },
            .envelope => {},
        }
        return self.workStatus(arena, session_id);
    }

    pub fn workRead(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8, after: ?u64, limit: ?i64) !Outcome {
        var bound: usize = work_read_default;
        if (limit) |given| {
            if (given < 1 or given > work_read_max) return .{ .refused = .{ .code = "invalid_request", .message = std.fmt.comptimePrint("work.read: limit must be from 1 to {d}", .{work_read_max}) } };
            bound = @intCast(given);
        }
        if (!self.hub.knows(session_id)) {
            if (try self.unheld(arena, session_id) != null) {
                var empty = try emptyObject(arena);
                try empty.put(arena, "turns", try jsonArray(arena, &.{}));
                return .{ .answer = .{ .object = empty } };
            }
        }
        const read = self.hub.transcript(session_id, after, bound) catch |err| {
            return .{ .refused = try self.stateRefusal(arena, err, session_id) };
        };
        const turns = try arena.alloc(std.json.Value, read.turns.len);
        for (read.turns, turns, 0..) |turn, *slot, offset| {
            var object = try emptyObject(arena);
            try object.put(arena, "index", .{ .integer = @intCast(read.first_index + offset) });
            try object.put(arena, "role", .{ .string = @tagName(turn.role) });
            try object.put(arena, "text", .{ .string = turn.text });
            if (turn.run_id.len > 0) try object.put(arena, "run_id", .{ .string = turn.run_id });
            if (turn.outcome.len > 0) try object.put(arena, "outcome", .{ .string = turn.outcome });
            try object.put(arena, "at_ms", .{ .integer = turn.at_ms });
            slot.* = .{ .object = object };
        }
        var root = try emptyObject(arena);
        try root.put(arena, "turns", try jsonArray(arena, turns));
        return .{ .answer = .{ .object = root } };
    }

    fn workJson(arena: std.mem.Allocator, piece: hubmod.Work) !std.json.Value {
        var ref = try emptyObject(arena);
        try ref.put(arena, "adapter", .{ .string = piece.adapter });
        try ref.put(arena, "session_id", .{ .string = piece.session_id });
        var object = try emptyObject(arena);
        try object.put(arena, "ref", .{ .object = ref });
        try object.put(arena, "status", .{ .string = @tagName(piece.status) });
        if (piece.directory.len > 0) try object.put(arena, "directory", .{ .string = piece.directory });
        if (piece.title.len > 0) try object.put(arena, "title", .{ .string = piece.title });
        if (piece.run_id.len > 0) try object.put(arena, "run_id", .{ .string = piece.run_id });
        if (piece.last_reply.len > 0) try object.put(arena, "last_reply", .{ .string = piece.last_reply });
        try object.put(arena, "updated_at_ms", .{ .integer = piece.updated_at_ms });
        if (piece.pending_interaction.len > 0) {
            var pending = try emptyObject(arena);
            try pending.put(arena, "interaction_id", .{ .string = piece.pending_interaction });
            try object.put(arena, "pending", .{ .object = pending });
        }
        return .{ .object = object };
    }

    pub fn capabilities(self: *Frontend, arena: std.mem.Allocator, name: []const u8) !Outcome {
        self.next_envelope += 1;
        const correlation = try std.fmt.allocPrint(arena, "oap-request-{d}", .{self.next_envelope});
        const descriptor = self.hub.probe(name) catch |err| switch (err) {
            error.UnknownAdapter => return .{ .refused = .{ .code = "unknown_adapter", .message = try std.fmt.allocPrint(arena, "no adapter \"{s}\"", .{name}) } },
            error.OutOfMemory => return error.OutOfMemory,
            else => return .{ .refused = .{ .code = "probe_failed", .message = try std.fmt.allocPrint(arena, "the adapter \"{s}\" could not be probed", .{name}) } },
        };
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oap-response-{d}", .{self.next_envelope});
        const declared = try declaredFeatures(arena, descriptor.features);
        const definitions = try arena.dupe(oap_types.ToolDefinition, descriptor.tools);
        const sources = try arena.dupe(oap_types.ToolSourceDescriptor, descriptor.sources);
        const payload = oap_types.CapabilitiesResponse{
            .endpoint = descriptor.endpoint,
            .protocol_versions = &.{oap_types.VERSION},
            .profiles = &.{oap_types.PROFILE},
            .features = declared,
            .tools = definitions,
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

    pub fn state(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !Outcome {
        const reported = self.hub.state(arena, session_id) catch |err| {
            return .{ .refused = try self.stateRefusal(arena, err, session_id) };
        };
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oap-response-{d}", .{self.next_envelope});
        self.next_envelope += 1;
        const correlation = try std.fmt.allocPrint(arena, "oap-request-{d}", .{self.next_envelope});
        const envelope = oap_types.Envelope{
            .id = answer_id,
            .in_reply_to = correlation,
            .session_id = reported.session_id,
            .payload = .{ .session_state_response = reported },
        };
        return .{ .answer_line = try oap_envelope.serializeEnvelope(envelope, arena) };
    }

    pub const Control = union(enum) {
        refused: struct { refusal: Refusal, run_id: ?[]const u8 = null },
        envelope: oap_types.Envelope,
    };

    fn responseId(self: *Frontend, arena: std.mem.Allocator) ![]const u8 {
        self.next_envelope += 1;
        return std.fmt.allocPrint(arena, "oap-response-{d}", .{self.next_envelope});
    }

    pub fn submitControl(self: *Frontend, arena: std.mem.Allocator, id: []const u8, value: std.json.Value) !Control {
        const gated = try gateEnvelope(arena, value, &.{ .message_submit_request, .session_compact_request }, "the request envelope is not a session.message.submit.request or a session.compact.request");
        var envelope = switch (gated) {
            .refused => |refused| return .{ .refused = .{ .refusal = refused, .run_id = null } },
            .envelope => |envelope| envelope,
        };
        var reported = contract.Refusal{};
        const compacting = envelope.payload == .session_compact_request;
        const admission = switch (envelope.payload) {
            .session_compact_request => |*request| self.hub.compactReporting(arena, id, request, envelope.id, &reported),
            else => self.hub.submitReporting(arena, id, &envelope.payload.message_submit_request, envelope.id, &reported),
        } catch |err| {
            return .{ .refused = .{ .refusal = try controlRefusal(arena, err, &reported, id), .run_id = null } };
        };
        return .{ .envelope = .{
            .id = try self.responseId(arena),
            .in_reply_to = envelope.id,
            .session_id = admission.session_id,
            .run_id = admission.run_id,
            .capability_revision = envelope.capability_revision,
            .payload = if (compacting) .{ .session_compact_response = admission } else .{ .message_submit_response = admission },
        } };
    }

    pub fn resolveControl(self: *Frontend, arena: std.mem.Allocator, id: []const u8, value: std.json.Value) !Control {
        var run_id: ?[]const u8 = null;
        const gated = try gateEnvelope(
            arena,
            value,
            &.{ .permission_resolve_request, .user_input_resolve_request, .call_resolve_request },
            "the request envelope is not action.permission.resolve.request, user.input.resolve.request or action.call.resolve.request",
        );
        var envelope = switch (gated) {
            .refused => |refused| return .{ .refused = .{ .refusal = refused, .run_id = run_id } },
            .envelope => |envelope| envelope,
        };
        var reported = contract.Refusal{};
        switch (envelope.payload) {
            .call_resolve_request => |*request| {
                run_id = request.run_id;
                const settled = self.hub.resolveCallReporting(arena, id, envelope.id, request, &reported) catch |err| {
                    return .{ .refused = .{ .refusal = try controlRefusal(arena, err, &reported, id), .run_id = run_id } };
                };
                return .{ .envelope = .{
                    .id = try self.responseId(arena),
                    .in_reply_to = envelope.id,
                    .session_id = id,
                    .run_id = request.run_id,
                    .capability_revision = envelope.capability_revision,
                    .payload = .{ .call_resolve_response = settled },
                } };
            },
            .permission_resolve_request => |*request| {
                run_id = request.run_id;
                self.hub.resolve(arena, id, .{ .permission = request }) catch |err| {
                    return .{ .refused = .{ .refusal = try controlRefusal(arena, err, &reported, id), .run_id = run_id } };
                };
                return .{ .envelope = .{
                    .id = try self.responseId(arena),
                    .in_reply_to = envelope.id,
                    .session_id = id,
                    .run_id = request.run_id,
                    .capability_revision = envelope.capability_revision,
                    .payload = .{ .permission_resolve_response = .{ .interaction_id = request.interaction_id, .session_id = request.session_id, .run_id = request.run_id, .accepted = true } },
                } };
            },
            .user_input_resolve_request => |*request| {
                run_id = request.run_id;
                self.hub.resolve(arena, id, .{ .input = request }) catch |err| {
                    return .{ .refused = .{ .refusal = try controlRefusal(arena, err, &reported, id), .run_id = run_id } };
                };
                return .{ .envelope = .{
                    .id = try self.responseId(arena),
                    .in_reply_to = envelope.id,
                    .session_id = id,
                    .run_id = request.run_id,
                    .capability_revision = envelope.capability_revision,
                    .payload = .{ .user_input_resolve_response = .{ .interaction_id = request.interaction_id, .session_id = request.session_id, .run_id = request.run_id, .accepted = true } },
                } };
            },
            else => unreachable,
        }
    }

    pub fn cancelControl(self: *Frontend, arena: std.mem.Allocator, id: []const u8, value: std.json.Value) !Control {
        var run_id: ?[]const u8 = null;
        const gated = try gateEnvelope(arena, value, &.{.run_cancel_request}, "the request envelope is not a run.cancel.request");
        const envelope = switch (gated) {
            .refused => |refused| return .{ .refused = .{ .refusal = refused, .run_id = run_id } },
            .envelope => |envelope| envelope,
        };
        const request = envelope.payload.run_cancel_request;
        if (!self.hub.knows(id)) {
            return .{ .refused = .{ .refusal = .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{id}) }, .run_id = run_id } };
        }
        if (!std.mem.eql(u8, request.session_id, id)) {
            return .{ .refused = .{ .refusal = .{
                .code = "scope_mismatch",
                .message = try std.fmt.allocPrint(arena, "payload session_id \"{s}\" does not match the addressed session \"{s}\"", .{ request.session_id, id }),
            }, .run_id = run_id } };
        }
        run_id = request.run_id;
        var reported = contract.Refusal{};
        const acknowledged = self.hub.cancel(arena, id, request.run_id) catch |err| {
            return .{ .refused = .{ .refusal = try controlRefusal(arena, err, &reported, id), .run_id = run_id } };
        };
        return .{ .envelope = .{
            .id = try self.responseId(arena),
            .in_reply_to = envelope.id,
            .session_id = acknowledged.session_id,
            .run_id = acknowledged.run_id,
            .capability_revision = envelope.capability_revision,
            .payload = .{ .run_cancel_response = acknowledged },
        } };
    }

    pub fn settingsControl(self: *Frontend, arena: std.mem.Allocator, id: []const u8, value: std.json.Value) !Control {
        const gated = try gateEnvelope(arena, value, &.{.session_settings_update_request}, "the request envelope is not a session.settings.update.request");
        const envelope = switch (gated) {
            .refused => |refused| return .{ .refused = .{ .refusal = refused, .run_id = null } },
            .envelope => |envelope| envelope,
        };
        const request = &envelope.payload.session_settings_update_request;
        var reported = contract.Refusal{};
        const updated = self.hub.updateSettings(arena, id, request, &reported) catch |err| {
            return .{ .refused = .{ .refusal = try controlRefusal(arena, err, &reported, id), .run_id = null } };
        };
        return .{ .envelope = .{
            .id = try self.responseId(arena),
            .in_reply_to = envelope.id,
            .session_id = updated.session_id,
            .capability_revision = envelope.capability_revision,
            .payload = .{ .session_settings_update_response = updated },
        } };
    }

    pub const Gate = union(enum) {
        refused: Refusal,
        envelope: oap_types.Envelope,
    };

    fn gateRequest(self: *Frontend, arena: std.mem.Allocator, payload: ?std.json.Value) Error!Gate {
        _ = self;
        return gateEnvelope(arena, payload, &.{.session_open_request}, "the request envelope is not a session.open.request");
    }

    pub fn gateEnvelope(
        arena: std.mem.Allocator,
        payload: ?std.json.Value,
        wanted: []const std.meta.Tag(oap_types.Payload),
        mismatch: []const u8,
    ) Error!Gate {
        const value = payload orelse return .{ .refused = .{ .code = "invalid_request", .message = "the request is required" } };
        if (value == .null) return .{ .refused = .{ .code = "invalid_request", .message = "the request is required" } };
        const raw = try oap_envelope.ownedRawJson(value, arena);
        if (raw.len > max_envelope_bytes) {
            return .{ .refused = .{ .code = "request_too_large", .message = "the request envelope exceeds the size limit" } };
        }
        var registry = jsonschema.Registry.initFromBundled(arena) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return .{ .refused = .{ .code = "internal", .message = "the bundled schemas could not be loaded" } },
        };
        defer registry.deinit();
        if (value != .object) {
            return .{ .refused = .{ .code = "malformed_json", .message = "the request envelope is not an object" } };
        }
        var validator = jsonschema.Validator.init(arena, &registry);
        defer validator.deinit();
        const failure = validator.validate("envelope.schema.json", value) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return .{ .refused = .{ .code = "malformed_json", .message = "the request envelope could not be validated" } },
        };
        if (failure != null) {
            return .{ .refused = .{ .code = "schema_invalid", .message = "the request envelope does not satisfy envelope.schema.json" } };
        }
        var envelope = oap_envelope.deserializeEnvelope(raw, arena) catch {
            return .{ .refused = .{ .code = "malformed_json", .message = "the request envelope could not be decoded" } };
        };
        if (std.mem.indexOfScalar(std.meta.Tag(oap_types.Payload), wanted, std.meta.activeTag(envelope.payload)) == null) {
            envelope.deinit(arena);
            return .{ .refused = .{ .code = "type_mismatch", .message = mismatch } };
        }
        return .{ .envelope = envelope };
    }

    const attachment_members = [_][]const u8{ "kind", "display_name", "protocol", "endpoint" };

    fn attachmentRefusal(
        self: *Frontend,
        arena: std.mem.Allocator,
        sources: ?std.json.Value,
    ) Error!?Refusal {
        const listed = sources orelse return null;
        if (listed != .array) return null;
        for (listed.array.items) |entry| {
            if (entry != .object) continue;
            const fields = entry.object;
            const id = if (fields.get("id")) |value| (if (value == .string) value.string else "") else "";
            if (fields.get("command")) |value| {
                if (value == .string and value.string.len > 0) {
                    return try attachmentRefuse(arena, id, "the daemon does not accept a command from the wire; name the source by id");
                }
            }
            if (fields.get("args")) |value| {
                if (value == .array and value.array.items.len > 0) {
                    return try attachmentRefuse(arena, id, "the daemon does not accept args from the wire; name the source by id");
                }
            }
            if (fields.get("environment")) |value| {
                if (value == .array) {
                    for (value.array.items) |entry_value| {
                        if (entry_value != .string) continue;
                        if (std.mem.indexOfScalar(u8, entry_value.string, '=') != null) {
                            return try attachmentRefuse(arena, id, "the daemon accepts only the bare NAME allowlist, never a NAME=value assignment");
                        }
                    }
                }
            }
            const configured = self.hub.toolSource(id);
            if (configured == null) {
                if (fields.get("kind")) |value| {
                    if (value == .string and std.mem.eql(u8, value.string, "process")) {
                        return try attachmentRefuse(arena, id, "no tool source of that id is configured on the daemon");
                    }
                }
                continue;
            }
            for (attachment_members) |named| {
                const wire = if (fields.get(named)) |value| (if (value == .string) value.string else null) else null;
                const text = wire orelse continue;
                if (text.len == 0) continue;
                const operator = configuredSourceMember(configured.?, named);
                if (std.mem.eql(u8, text, operator)) continue;
                const message = try std.fmt.allocPrint(arena, "the daemon does not accept {s} from the wire for a configured source; name it by id", .{named});
                return try attachmentRefuse(arena, id, message);
            }
        }
        return null;
    }

    fn attachmentRefuse(arena: std.mem.Allocator, source: []const u8, message: []const u8) !Refusal {
        return refusalWith(arena, "unsupported_feature", message, try arena.dupe(oap_types.DetailEntry, &.{
            .{ .key = "feature", .value = contract.feature_tool_sources_attach },
            .{ .key = "reason", .value = contract.reason_unsatisfiable },
            .{ .key = "source", .value = source },
        }));
    }

    fn configuredSourceMember(source: contract.ConfiguredSource, key: []const u8) []const u8 {
        if (std.mem.eql(u8, key, "kind")) return source.kind;
        if (std.mem.eql(u8, key, "display_name")) return source.display_name;
        if (std.mem.eql(u8, key, "protocol")) return source.protocol;
        if (std.mem.eql(u8, key, "endpoint")) return source.endpoint;
        return "";
    }

    pub fn openSession(
        self: *Frontend,
        arena: std.mem.Allocator,
        adapter: []const u8,
        request: Request,
        holding: bool,
    ) Error!Outcome {
        const gated = try self.gateRequest(arena, request.payload);
        var envelope: oap_types.Envelope = switch (gated) {
            .refused => |refusal| return .{ .refused = refusal },
            .envelope => |value| value,
        };
        const open = switch (envelope.payload) {
            .session_open_request => |*payload| payload,
            else => unreachable,
        };
        var metadata: ?std.json.Value = null;
        if (request.payload) |value| {
            if (value == .object) {
                if (value.object.get("payload")) |body| {
                    if (body == .object) {
                        if (body.object.get("tool_sources")) |sources| {
                            if (try self.attachmentRefusal(arena, sources)) |refusal| {
                                envelope.deinit(arena);
                                return .{ .refused = refusal };
                            }
                        }
                    }
                }
            }
        }
        if (request.payload) |value| {
            if (value == .object) {
                if (value.object.get("payload")) |body| {
                    if (body == .object) {
                        if (body.object.get("metadata")) |named| metadata = named;
                    }
                }
            }
        }
        if (!holding and open.subscribe and self.max_subscriptions > 0 and self.streams.items.len >= self.max_subscriptions) {
            envelope.deinit(arena);
            return .{ .refused = .{ .code = "busy", .message = try std.fmt.allocPrint(arena, "the frontend already holds {d} subscriptions; a subscription ends at its run's terminal, at an overflow or stream failure, or when its session closes — send this request again once one has", .{self.max_subscriptions}) } };
        }
        var refused: hubmod.OpenRefusal = .{};
        const opened = self.hub.openReporting(arena, adapter, .{
            .session_id = open.session_id orelse "",
            .metadata = metadata,
            .capability_revision = envelope.capability_revision,
            .subscribe = open.subscribe,
            .reopen = open.reopen,
            .allow_degraded_features = open.allow_degraded_features,
            .tools_json = open.tools_json,
            .tool_sources_json = try substitutedSources(arena, self.hub, open.tool_sources_json),
            .reasoning_level = open.reasoning_level,
            .compaction_policy_json = open.compaction_policy_json,
        }, &refused) catch |err| {
            try ownRevisions(arena, &refused);
            envelope.deinit(arena);
            return .{ .refused = try openRefusal(arena, err, request, &refused) };
        };
        if (opened.subscription) |subscription| {
            if (holding) {
                _ = self.hub.holdSubscription(subscription) catch |err| {
                    subscription.close();
                    self.hub.discardSession(opened.session_id);
                    envelope.deinit(arena);
                    return err;
                };
            }
        }
        var answered = opened.state;
        if (open.message_json != null) {
            const admission = self.admitOpeningMessage(arena, request, envelope.id, opened.state.session_id) catch |err| {
                if (!holding) if (opened.subscription) |subscription| subscription.close();
                self.hub.discardSession(opened.session_id);
                envelope.deinit(arena);
                return err;
            };
            switch (admission) {
                .refused => |refusal| {
                    if (!holding) if (opened.subscription) |subscription| subscription.close();
                    self.hub.discardSession(opened.session_id);
                    envelope.deinit(arena);
                    return .{ .refused = refusal };
                },
                .admitted => |admitted| answered = try withAdmittedRun(arena, answered, admitted, envelope.id),
            }
        }
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oap-response-{d}", .{self.next_envelope});
        const opened_envelope = oap_types.Envelope{
            .id = answer_id,
            .in_reply_to = envelope.id,
            .session_id = answered.session_id,
            .capability_revision = if (open.subscribe or open.reopen or contract.carriesEntries(open.tool_sources_json)) opened.revision else envelope.capability_revision,
            .payload = .{ .session_open_response = answered },
        };
        const line = try oap_envelope.serializeEnvelope(opened_envelope, arena);
        const named = open.session_id != null;
        envelope.deinit(arena);
        if (holding) return .{ .answer_line = line };
        const subscription = opened.subscription orelse return .{ .answer_line = line };
        if (!self.fits(request.id, line)) {
            subscription.close();
            if (named) return .{ .refused = .{ .code = "response_too_large", .message = "the open response exceeds the frame limit; the session is open under the session_id the request supplied" } };
            self.hub.discardSession(opened.session_id);
            return .{ .refused = .{ .code = "response_too_large", .message = "the open response exceeds the frame limit; the session was rolled back" } };
        }
        self.answerLine(request.id, line) catch |err| {
            subscription.close();
            return err;
        };
        self.streams.append(self.allocator, .{ .id = request.id, .subscription = subscription }) catch |err| {
            subscription.close();
            return err;
        };
        try self.pumpStreams();
        return .streaming;
    }

    const OpeningAdmission = union(enum) {
        admitted: oap_types.MessageSubmitResponse,
        refused: Refusal,
    };

    fn admitOpeningMessage(self: *Frontend, arena: std.mem.Allocator, request: Request, envelope_id: []const u8, session_id: []const u8) !OpeningAdmission {
        const sent = request.payload.?.object;
        const message = sent.get("payload").?.object.get("message").?;
        if (message != .object) return .{ .refused = .{ .code = "invalid_request", .message = "an open's message is an object" } };
        var body = try message.object.clone(arena);
        try body.put(arena, "session_id", .{ .string = session_id });
        var submit = try sent.clone(arena);
        try submit.put(arena, "type", .{ .string = "session.message.submit.request" });
        try submit.put(arena, "session_id", .{ .string = session_id });
        try submit.put(arena, "payload", .{ .object = body });
        _ = submit.swapRemove("capability_revision");
        const gated = try gateEnvelope(arena, .{ .object = submit }, &.{.message_submit_request}, "an open's message is not a message submission");
        var envelope = switch (gated) {
            .refused => |refused| return .{ .refused = refused },
            .envelope => |envelope| envelope,
        };
        defer envelope.deinit(arena);
        var reported = contract.Refusal{};
        const admitted = self.hub.submitReporting(arena, session_id, &envelope.payload.message_submit_request, envelope_id, &reported) catch |err| {
            return .{ .refused = try controlRefusal(arena, err, &reported, session_id) };
        };
        return .{ .admitted = admitted };
    }

    fn withAdmittedRun(arena: std.mem.Allocator, current: oap_types.SessionState, admission: oap_types.MessageSubmitResponse, envelope_id: []const u8) !oap_types.SessionState {
        const run_id = admission.run_id orelse return current;
        const status = admission.status orelse .running;
        var queued_ahead: u64 = 0;
        for (current.active_runs) |run| {
            if (run.status == .queued) queued_ahead += 1;
        }
        const runs = try arena.alloc(oap_types.ActiveRun, current.active_runs.len + 1);
        @memcpy(runs[0..current.active_runs.len], current.active_runs);
        runs[current.active_runs.len] = .{
            .run_id = run_id,
            .status = status,
            .relationship = "primary",
            .queue_position = if (status == .queued) queued_ahead + 1 else null,
            .admitted_submit_requests = try arena.dupe([]const u8, &.{envelope_id}),
        };
        var next = current;
        next.active_runs = runs;
        if (status != .queued) {
            next.active_run_id = run_id;
            next.status = .running;
        } else if (current.status == .idle) {
            next.status = .queued;
        }
        return next;
    }

    fn openRefusal(
        arena: std.mem.Allocator,
        err: hubmod.Failure,
        request: Request,
        refused: *const hubmod.OpenRefusal,
    ) !Refusal {
        return switch (err) {
            error.UnknownAdapter => .{ .code = "unknown_adapter", .message = try std.fmt.allocPrint(arena, "no adapter is registered as \"{s}\"", .{request.adapter orelse ""}) },
            error.SessionExists => .{ .code = "session_exists", .message = "the session id is already open" },
            error.UnknownSession => .{ .code = "unknown_session", .message = if (refused.reason.message.len > 0) refused.reason.message else "no closed session is kept under that id" },
            error.SessionClosed => .{ .code = "session_closed", .message = "the session was already closed when the open probed it" },
            error.StaleCapabilities => try refusalWith(arena, "stale_capabilities", "the open cites a capability revision that is no longer current", try arena.dupe(oap_types.DetailEntry, &.{
                .{ .key = "expected_revision", .value = refused.expected_revision },
                .{ .key = "current_revision", .value = refused.current_revision },
            })),
            error.UnsupportedFeature, error.ToolCatalogUnavailable => try refusalWith(arena, "unsupported_feature", "the adapter does not advertise a feature the request elected", try detailForReason(arena, refused.reason)),
            error.CapabilityDegraded => try refusalWith(arena, "capability_degraded", "a feature the request did not opt into is degraded", try featureOnly(arena, refused.reason)),
            error.ModelNotFound => try refusalWith(arena, "model_not_found", "the open names a model the adapter's catalog does not carry", try arena.dupe(oap_types.DetailEntry, &.{.{ .key = "model_id", .value = refused.reason.model_id }})),
            error.AdapterDescriptorUnbound => .{ .code = "internal", .message = "the adapter descriptor carries no capability revision" },
            error.BackendFailed => .{ .code = "probe_failed", .message = if (refused.reason.message.len > 0) refused.reason.message else "the adapter's probe refused" },
            error.OutOfMemory => error.OutOfMemory,
            else => .{ .code = "open_failed", .message = @errorName(err) },
        };
    }

    pub fn models(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8, degraded: []const []const u8) !Outcome {
        const catalog = self.hub.models(arena, session_id, &.{
            .session_id = session_id,
            .allow_degraded_features = degraded,
        }) catch |err| {
            return .{ .refused = try self.catalogRefusal(arena, err, session_id, contract.feature_models_list, "internal") };
        };
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oap-response-{d}", .{self.next_envelope});
        self.next_envelope += 1;
        const correlation = try std.fmt.allocPrint(arena, "oap-request-{d}", .{self.next_envelope});
        const envelope = oap_types.Envelope{
            .id = answer_id,
            .in_reply_to = correlation,
            .session_id = session_id,
            .capability_revision = catalog.revision,
            .payload = .{ .models_response = catalog.models },
        };
        return .{ .answer_line = try oap_envelope.serializeEnvelope(envelope, arena) };
    }

    pub fn tools(self: *Frontend, arena: std.mem.Allocator, session_id: ?[]const u8, degraded: []const []const u8) !Outcome {
        const served = self.hub.tools(arena, session_id orelse "", &.{
            .session_id = session_id,
            .allow_degraded_features = degraded,
        }) catch |err| {
            return .{ .refused = try self.catalogRefusal(arena, err, session_id orelse "", contract.feature_tools_list, "tools_failed") };
        };
        self.next_envelope += 1;
        const answer_id = try std.fmt.allocPrint(arena, "oap-response-{d}", .{self.next_envelope});
        self.next_envelope += 1;
        const correlation = try std.fmt.allocPrint(arena, "oap-request-{d}", .{self.next_envelope});
        const envelope = oap_types.Envelope{
            .id = answer_id,
            .in_reply_to = correlation,
            .session_id = served.tools.session_id orelse (session_id orelse ""),
            .capability_revision = served.revision,
            .payload = .{ .tools_list_response = served.tools },
        };
        return .{ .answer_line = try oap_envelope.serializeEnvelope(envelope, arena) };
    }

    pub fn closeSession(self: *Frontend, arena: std.mem.Allocator, session_id: []const u8) !Outcome {
        self.hub.close(arena, session_id) catch |err| {
            return .{ .refused = try self.refusalFor(arena, err, session_id) };
        };
        return .{ .answer = .{ .null = {} } };
    }

    fn catalogRefusal(
        self: *Frontend,
        arena: std.mem.Allocator,
        err: hubmod.Failure,
        session_id: []const u8,
        feature: []const u8,
        fallback: []const u8,
    ) !Refusal {
        _ = self;
        return switch (err) {
            error.UnknownSession => .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{session_id}) },
            error.SessionClosed => .{ .code = "session_closed", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{session_id}) },
            error.ScopeMismatch => .{ .code = "scope_mismatch", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{session_id}) },
            error.UnsupportedFeature, error.ToolCatalogUnavailable => try refusalWith(arena, "unsupported_feature", @errorName(err), &featureReasonDetails(feature, contract.reason_unadvertised)),
            error.CapabilityDegraded => try refusalWith(arena, "capability_degraded", @errorName(err), &featureOnlyDetail(feature)),
            error.ModelNotFound => try refusalWith(arena, "model_not_found", @errorName(err), &.{}),
            error.CatalogMisScoped => .{ .code = fallback, .message = "the adapter served a catalog scoped to another session" },
            error.CatalogUnlabelled => .{ .code = fallback, .message = "the adapter served a catalog with no capability revision" },
            error.OutOfMemory => return error.OutOfMemory,
            else => .{ .code = fallback, .message = @errorName(err) },
        };
    }

    fn refusalFor(self: *Frontend, arena: std.mem.Allocator, err: hubmod.Failure, session_id: []const u8) !Refusal {
        _ = self;
        return switch (err) {
            error.UnknownSession => .{ .code = "unknown_session", .message = try std.fmt.allocPrint(arena, "no session \"{s}\"", .{session_id}) },
            error.SessionClosed => .{ .code = "session_closed", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" is closed", .{session_id}) },
            error.RunActive => .{ .code = "run_active", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" still has a run in flight; cancel it first", .{session_id}) },
            error.SessionExists => .{ .code = "session_exists", .message = try std.fmt.allocPrint(arena, "the session \"{s}\" already exists", .{session_id}) },
            error.ScopeMismatch => .{ .code = "scope_mismatch", .message = try std.fmt.allocPrint(arena, "the request names a session other than \"{s}\"", .{session_id}) },
            error.OutOfMemory => return error.OutOfMemory,
            else => .{ .code = "internal", .message = @errorName(err) },
        };
    }

    fn stateRefusal(self: *Frontend, arena: std.mem.Allocator, err: hubmod.Failure, session_id: []const u8) !Refusal {
        return switch (err) {
            error.UnknownSession,
            error.SessionClosed,
            error.ScopeMismatch,
            => self.refusalFor(arena, err, session_id),
            error.OutOfMemory => return error.OutOfMemory,
            else => .{ .code = "state_failed", .message = @errorName(err) },
        };
    }

    fn answer(self: *Frontend, id: i64, value: std.json.Value) Error!void {
        const arena = self.allocator;
        var object = try emptyObject(arena);
        defer object.deinit(arena);
        try object.put(arena, "id", .{ .integer = id });
        try object.put(arena, "ok", .{ .bool = true });
        try object.put(arena, "result", value);
        const line = self.frame(arena, object) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {
                try self.tooLarge(arena, id);
                return;
            },
        };
        defer arena.free(line);
        try self.write(line);
    }

    fn fits(self: *const Frontend, id: i64, payload: []const u8) bool {
        var digits: [24]u8 = undefined;
        const counted = std.fmt.bufPrint(&digits, "{d}", .{id}) catch return false;
        return payload.len + counted.len + "{\"id\":,\"ok\":true,\"result\":}".len <= self.frame_limit;
    }

    fn answerLine(self: *Frontend, id: i64, payload: []const u8) Error!void {
        const arena = self.allocator;
        const line = std.fmt.allocPrint(arena, "{{\"id\":{d},\"ok\":true,\"result\":{s}}}", .{ id, payload }) catch return error.OutOfMemory;
        defer arena.free(line);
        if (line.len > self.frame_limit) {
            try self.tooLarge(arena, id);
            return;
        }
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
            try body.put(arena, "details", try detailsJson(arena, refusal.details));
        }
        try object.put(arena, "error", .{ .object = body });
        if (self.frame(arena, object)) |line| {
            defer arena.free(line);
            try self.write(line);
            return;
        } else |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {},
        }
        var shed = try emptyObject(arena);
        try shed.put(arena, "id", .{ .integer = id });
        try shed.put(arena, "ok", .{ .bool = false });
        try shed.put(arena, "result", .{ .null = {} });
        var slimmer = try emptyObject(arena);
        try slimmer.put(arena, "code", .{ .string = refusal.code });
        try slimmer.put(arena, "message", .{ .string = refusal.message });
        try shed.put(arena, "error", .{ .object = slimmer });
        if (self.frame(arena, shed)) |line| {
            defer arena.free(line);
            try self.write(line);
            return;
        } else |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {},
        }
        try self.tooLarge(arena, id);
    }

    fn frame(self: *Frontend, arena: std.mem.Allocator, object: anytype) ![]const u8 {
        const line = json_encode.valueAlloc(arena, .{ .object = object }) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
        };
        if (line.len > self.frame_limit) {
            arena.free(line);
            return Error.ResponseTooLarge;
        }
        return line;
    }

    fn tooLarge(self: *Frontend, arena: std.mem.Allocator, id: i64) Error!void {
        var object = try emptyObject(arena);
        defer object.deinit(arena);
        try object.put(arena, "id", .{ .integer = id });
        try object.put(arena, "ok", .{ .bool = false });
        try object.put(arena, "result", .{ .null = {} });
        var body = try emptyObject(arena);
        defer body.deinit(arena);
        try body.put(arena, "code", .{ .string = "response_too_large" });
        try body.put(arena, "message", .{ .string = "the encoded response exceeds the frame limit" });
        try object.put(arena, "error", .{ .object = body });
        const line = try self.frame(arena, object);
        defer arena.free(line);
        try self.write(line);
    }

    fn write(self: *Frontend, line: []const u8) Error!void {
        self.sink.write(self.sink.context, line) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return Error.OutputStalled,
        };
    }
};

pub fn trim(arena: std.mem.Allocator, message: []const u8) Error![]const u8 {
    if (std.unicode.utf8CountCodepoints(message) catch 0 <= message_limit) return message;
    var kept: usize = 0;
    var index: usize = 0;
    while (index < message.len) {
        if (kept == message_limit) break;
        index += std.unicode.utf8ByteSequenceLength(message[index]) catch 1;
        kept += 1;
    }
    return std.fmt.allocPrint(arena, "{s}…", .{message[0..index]});
}

fn timestamp(arena: std.mem.Allocator, milliseconds: i64) ![]const u8 {
    const seconds: u64 = @intCast(@divFloor(milliseconds, 1000));
    const epoch = std.time.epoch.EpochSeconds{ .secs = seconds };
    const day = epoch.getEpochDay().calculateYearDay();
    const month = day.calculateMonthDay();
    const clock = epoch.getDaySeconds();
    var buffer: [32]u8 = undefined;
    const written = try std.fmt.bufPrint(&buffer, "{d:0>4}-{d:0>2}-{d:0>2}T{d:0>2}:{d:0>2}:{d:0>2}Z", .{
        day.year,
        @intFromEnum(month.month),
        @as(u16, month.day_index) + 1,
        clock.getHoursIntoDay(),
        clock.getMinutesIntoHour(),
        clock.getSecondsIntoMinute(),
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

pub const cycle_budget_ns: u64 = 50 * std.time.ns_per_ms;

pub const Stream = struct {
    read: *const fn (context: *anyopaque, buffer: []u8) anyerror!usize,
    context: *anyopaque,
    readable: ?std.Io.File.Handle = null,
    stop: ?*const fn () bool = null,
};

const ReadFailure = error{InputFailed};

pub fn serve(allocator: std.mem.Allocator, frontend: *Frontend, stream: Stream) Error!void {
    var pending: std.ArrayList(u8) = .empty;
    defer pending.deinit(allocator);
    var scratch = std.heap.ArenaAllocator.init(allocator);
    defer scratch.deinit();

    while (!frontend.stopped) {
        if (stream.stop) |stop| {
            if (stop()) {
                frontend.stopped = true;
                return;
            }
        }
        _ = scratch.reset(.retain_capacity);
        const arena = scratch.allocator();
        const input_ready = try waitOn(frontend, stream, arena, cycle_budget_ns);
        var more = false;
        if (input_ready) {
            more = try readAvailable(allocator, stream, &pending);
            if (pending.items.len > pending_bound(frontend.frame_limit)) {
                frontend.noteDefect(pending.items);
                return Error.FrameLimitTooSmall;
            }
            while (std.mem.indexOfScalar(u8, pending.items, '\n')) |cut| {
                const line = pending.items[0..cut];
                const rest = pending.items[cut + 1 ..];
                frontend.handleLine(line) catch |err| switch (err) {
                    error.OutOfMemory => return error.OutOfMemory,
                    Error.OutputStalled => return Error.OutputStalled,
                    else => break,
                };
                std.mem.copyForwards(u8, pending.items[0..rest.len], rest);
                pending.items.len = rest.len;
            }
            if (frontend.stopped) return error.StdinFailed;
        }
        try frontend.hub.pump(allocator, 0);
        try frontend.pumpStreams();
        if (input_ready and !more) {
            if (pending.items.len > 0) {
                frontend.noteDefect(pending.items);
                return error.StdinFailed;
            }
            return;
        }
    }
}

fn readAvailable(allocator: std.mem.Allocator, stream: Stream, pending: *std.ArrayList(u8)) !bool {
    var buffer: [4096]u8 = undefined;
    const read = stream.read(stream.context, &buffer) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        error.EndOfStream => return false,
        else => return ReadFailure.InputFailed,
    };
    if (read == 0) return false;
    try pending.ensureUnusedCapacity(allocator, read);
    pending.appendSliceAssumeCapacity(buffer[0..read]);
    return true;
}
fn pending_bound(limit: usize) usize {
    return limit + 1;
}

const pollable = @import("builtin").os.tag != .windows;
fn waitOn(frontend: *Frontend, stream: Stream, arena: std.mem.Allocator, wait_ns: u64) !bool {
    if (comptime !pollable) return true;
    const input = stream.readable orelse return true;
    const children = try frontend.hub.readableHandles(arena);
    var watched: std.ArrayList(std.posix.pollfd) = .empty;
    for (children) |handle| try watched.append(arena, .{ .fd = handle, .events = std.posix.POLL.IN, .revents = 0 });
    try watched.append(arena, .{ .fd = input, .events = std.posix.POLL.IN, .revents = 0 });
    const budget: i32 = @intCast(@min(wait_ns / std.time.ns_per_ms, std.math.maxInt(i32)));
    const awoken = std.posix.poll(watched.items, budget) catch 0;
    if (awoken == 0) return false;
    const input_events = watched.items[watched.items.len - 1].revents;
    return (input_events & (std.posix.POLL.IN | std.posix.POLL.HUP | std.posix.POLL.ERR | std.posix.POLL.NVAL)) != 0;
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

const reference_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "reference", .name = "Reference", .version = "0.1", .adapter = "script" },
    .capability_revision = "reference-v1",
    .features = &.{
        .{ .key = "session.open.subscribe", .level = .native },
        .{ .key = contract.feature_tool_sources_attach, .level = .native, .modes = &.{"session_open"} },
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

    fn message(self: *Harness) ![]const u8 {
        const answer = try self.lastValue();
        const body = answer.object.get("error") orelse return error.Shape;
        return textMember(self.arena(), body, "message");
    }
};

const ReferenceState = struct {
    opened: usize = 0,
    lister_revision: []const u8 = "reference-lister-v2",
    has_lister: bool = true,
    last_id: []const u8 = "session-1",
    last_id_buffer: [128]u8 = undefined,
    saw_metadata_members: usize = 0,
    saw_tool_sources_buffer: [1024]u8 = undefined,
    saw_tool_sources_json: []const u8 = "",
    running: bool = false,
    submit_refuses: bool = false,
    native_fails: bool = false,
    state_fails: bool = false,
    lister_closed: bool = false,
    closed: bool = false,
    pending_events: []const contract.Event = &.{},
};

var reference_holder: ReferenceState = .{};

fn reference() contract.Adapter {
    return .{ .ptr = @ptrCast(@constCast(&reference_holder)), .vtable = &.{ .probe = referenceProbe, .open = referenceOpen, .native_list = referenceNativeList } };
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
    state.saw_metadata_members = if (request.metadata) |named| named.object.count() else 0;
    const handed = request.tool_sources_json orelse "";
    if (handed.len <= state.saw_tool_sources_buffer.len) {
        @memcpy(state.saw_tool_sources_buffer[0..handed.len], handed);
        state.saw_tool_sources_json = state.saw_tool_sources_buffer[0..handed.len];
    } else {
        state.saw_tool_sources_json = "";
    }
    const asked = if (request.session_id.len > 0) request.session_id else "session-1";
    if (asked.len <= state.last_id_buffer.len) {
        @memcpy(state.last_id_buffer[0..asked.len], asked);
        state.last_id = state.last_id_buffer[0..asked.len];
    } else {
        state.last_id = "session-1";
    }
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

fn referenceId(ptr: *anyopaque) []const u8 {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    if (state.opened == 0) return "session-1";
    return state.last_id;
}

fn referenceModels(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!contract.Catalog {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = refusal;
    if (state.lister_closed) return error.SessionClosed;
    if (!state.has_lister) return error.UnsupportedFeature;
    const models = try arena.dupe(oap_types.ModelDescriptor, &.{.{ .id = "reference-model", .default = true }});
    return .{ .revision = state.lister_revision, .response = .{
        .session_id = request.session_id,
        .current_model_id = "reference-model",
        .models = models,
    } };
}

fn referenceTools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!contract.ToolSet {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = refusal;
    _ = arena;
    if (state.lister_closed) return error.SessionClosed;
    if (!state.has_lister) return error.ToolCatalogUnavailable;
    return .{ .revision = state.lister_revision, .response = .{ .session_id = request.session_id } };
}

fn referenceState(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    if (state.state_fails) return error.BackendFailed;
    _ = refusal;
    const session_id = try arena.dupe(u8, referenceId(state));
    if (state.closed) return .{ .session_id = session_id, .status = .closed };
    if (!state.running) return .{ .session_id = session_id, .status = .idle };
    const run = try arena.dupe(u8, "run-1");
    const runs = try arena.dupe(oap_types.ActiveRun, &.{.{ .run_id = run, .status = .running, .relationship = "primary" }});
    return .{ .session_id = session_id, .status = .running, .active_run_id = run, .active_runs = runs };
}

fn referenceNativeList(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.NativeListRequest, refusal: *contract.Refusal) contract.Failure![]const contract.NativeSession {
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = request;
    if (state.native_fails) return refusal.fail(error.BackendFailed, "the harness would not list");
    return try arena.dupe(contract.NativeSession, &.{
        .{ .native_id = "thread-a", .title = "older thread", .directory = "/work/a", .updated_at_ms = 5 },
        .{ .native_id = "thread-b", .title = "busy thread", .directory = "/work/a", .updated_at_ms = 7, .running = true },
    });
}

fn referenceSubmit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
    _ = envelope_id;
    const state: *ReferenceState = @ptrCast(@alignCast(ptr));
    _ = request;
    if (state.submit_refuses) return refusal.unsupported(contract.feature_submit, contract.reason_unsatisfiable);
    state.running = true;
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
    try out.appendSlice(allocator, state.pending_events);
    state.pending_events = &.{};
}

fn referenceActivity(ptr: *anyopaque) contract.Activity {
    _ = ptr;
    return .idle;
}

fn referenceClose(ptr: *anyopaque, force: bool) contract.Failure!void {
    _ = ptr;
    _ = force;
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

test "a refusal that will not fit is reduced, then refused as too large" {
    const harness = try Harness.init(testing.allocator, .{}, .{ .frame_limit = minimum_frame_limit });
    defer harness.deinit();
    const name = "adapter-" ++ "n" ** 200;
    const line = try std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"capabilities\",\"adapter\":\"{s}\"}}", .{name});
    try harness.send(line);
    const answer = (try harness.lastValue()).object.get("error").?.object;
    try testing.expectEqualStrings("response_too_large", answer.get("code").?.string);
    try testing.expectEqualStrings("the encoded response exceeds the frame limit", answer.get("message").?.string);
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

test "a cursor on an op that takes none is a refusal, not a framing defect" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const values = [_][]const u8{ "-1", "1.5", "\"1\"", "true" };
    for (values) |value| {
        const line = try std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"adapters\",\"after\":{s}}}", .{value});
        try harness.send(line);
        try testing.expectEqualStrings("invalid_request", try harness.code());
    }
    try harness.send("{\"id\":2,\"op\":\"adapters\"}");
    try testing.expect((try harness.lastValue()).object.get("ok").?.bool);
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

test "a timestamp is RFC 3339 at second precision in UTC, and drops the fraction" {
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    try testing.expectEqualStrings("1970-01-01T00:00:00Z", try timestamp(arena, 0));
    try testing.expectEqualStrings("2023-11-14T22:15:23Z", try timestamp(arena, 1_700_000_123_456));
    try testing.expectEqualStrings("2023-11-14T22:15:23Z", try timestamp(arena, 1_700_000_123_000));
    try testing.expectEqualStrings("2023-11-14T22:15:23Z", try timestamp(arena, 1_700_000_123_999));
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
    try testing.expectEqualStrings("native", try textMember(harness.arena(), features.get(contract.feature_tool_sources_attach).?, "level"));
    const attach = features.get(contract.feature_tool_sources_attach).?;
    try testing.expectEqualStrings("session_open", attach.object.get("modes").?.array.items[0].string);
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
    try testing.expectEqualStrings("1970-01-01T00:00:00Z", try textMember(harness.arena(), sessions.items[0], "created_at"));
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
    try testing.expectError(error.UnknownSession, harness.hub.state(arena, opened.session_id));

    try harness.send("{\"id\":2,\"op\":\"close\",\"session_id\":\"closing\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"close\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
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
    _ = try harness.hub.submit(arena, "busy", &request, "");
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
    try testing.expectEqualStrings("oap-response-1", try textMember(harness.arena(), envelope, "id"));
    try testing.expectEqualStrings("oap-request-2", try textMember(harness.arena(), envelope, "in_reply_to"));
    try testing.expectEqualStrings("asked", try textMember(harness.arena(), envelope, "session_id"));
    const payload = envelope.object.get("payload").?;
    try testing.expectEqualStrings("asked", try textMember(harness.arena(), payload, "session_id"));
    try harness.send("{\"id\":2,\"op\":\"state\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"state\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
}

const Scripted = struct {
    chunks: []const []const u8,
    at: usize = 0,
    fail_at: ?usize = null,

    fn read(context: *anyopaque, buffer: []u8) anyerror!usize {
        const self: *Scripted = @ptrCast(@alignCast(context));
        if (self.fail_at != null and self.at == self.fail_at.?) return error.BrokenPipe;
        if (self.at >= self.chunks.len) return 0;
        const chunk = self.chunks[self.at];
        self.at += 1;
        if (chunk.len > buffer.len) return error.FrameTooLarge;
        @memcpy(buffer[0..chunk.len], chunk);
        return chunk.len;
    }
};

var null_device_cycles: usize = 0;

fn giveUpOnNullDevice() bool {
    null_device_cycles += 1;
    return null_device_cycles > 40;
}

test "input on a device poll cannot watch, such as /dev/null on macOS, still reaches its end" {
    if (comptime !pollable) return error.SkipZigTest;
    var device = try std.Io.Dir.cwd().openFile(testing.io, "/dev/null", .{});
    defer device.close(testing.io);
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{});
    defer harness.deinit();
    null_device_cycles = 0;
    try harness.runWithHandleUntilStopped(device.handle, giveUpOnNullDevice);
    try testing.expect(null_device_cycles <= 1);
}

const RefusesWrites = struct {
    fn write(_: *anyopaque, _: []const u8) anyerror!void {
        return Error.OutputStalled;
    }
};

const ScriptedHarness = struct {
    allocator: std.mem.Allocator,
    backing: *Harness,
    scripted: Scripted,

    fn init(allocator: std.mem.Allocator, hub_options: hubmod.Options, options: Options, chunks: []const []const u8) !*ScriptedHarness {
        const self = try allocator.create(ScriptedHarness);
        self.* = .{
            .allocator = allocator,
            .backing = try Harness.init(allocator, hub_options, options),
            .scripted = .{ .chunks = chunks },
        };
        return self;
    }

    fn run(self: *ScriptedHarness) !void {
        return self.serveWithHandle(null);
    }

    fn runRefusingWrites(self: *ScriptedHarness) !void {
        var frontend = try Frontend.init(self.allocator, &self.backing.hub, .{
            .context = undefined,
            .write = RefusesWrites.write,
        }, .{});
        defer frontend.deinit();
        return serve(self.allocator, &frontend, .{
            .read = Scripted.read,
            .context = &self.scripted,
            .readable = null,
        });
    }

    fn failReadAt(self: *ScriptedHarness, index: usize) void {
        self.scripted.fail_at = index;
    }

    fn runWithHandle(self: *ScriptedHarness, handle: ?std.Io.File.Handle) !void {
        return self.serveWithHandle(handle);
    }

    fn serveWithHandle(self: *ScriptedHarness, handle: ?std.Io.File.Handle) !void {
        var frontend = try Frontend.init(self.allocator, &self.backing.hub, self.backing.recorder.sink(), .{});
        defer frontend.deinit();
        try serve(self.allocator, &frontend, .{
            .read = Scripted.read,
            .context = &self.scripted,
            .readable = handle,
        });
    }

    fn runWithHandleUntilStopped(self: *ScriptedHarness, handle: std.Io.File.Handle, stop: *const fn () bool) !void {
        var frontend = try Frontend.init(self.allocator, &self.backing.hub, self.backing.recorder.sink(), .{});
        defer frontend.deinit();
        try serve(self.allocator, &frontend, .{
            .read = Scripted.read,
            .context = &self.scripted,
            .readable = handle,
            .stop = stop,
        });
    }

    fn runUntilStopped(self: *ScriptedHarness, stop: *const fn () bool) !void {
        var frontend = try Frontend.init(self.allocator, &self.backing.hub, self.backing.recorder.sink(), .{});
        defer frontend.deinit();
        try serve(self.allocator, &frontend, .{
            .read = Scripted.read,
            .context = &self.scripted,
            .readable = null,
            .stop = stop,
        });
    }

    fn deinit(self: *ScriptedHarness) void {
        const allocator = self.allocator;
        self.backing.deinit();
        allocator.destroy(self);
    }
};

test "the serve loop reads even when it has no handle to wait on" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{"{\"id\":1,\"op\":\"adapters\"}\n"});
    defer harness.deinit();
    try harness.run();
    try testing.expectEqual(@as(usize, 1), harness.backing.recorder.lines.items.len);
    try testing.expect(std.mem.indexOf(u8, harness.backing.recorder.lines.items[0], "\"ok\":true") != null);
}

var stop_at_cycle: usize = 0;
var stop_cycles: usize = 0;

fn stopAfterCycle() bool {
    const reached = stop_cycles >= stop_at_cycle;
    stop_cycles += 1;
    return reached;
}

test "the serve loop ends when the stream is told to stop, without a line from the host and without reading one" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{});
    defer harness.deinit();
    stop_at_cycle = 0;
    stop_cycles = 0;
    try harness.runUntilStopped(stopAfterCycle);
    try testing.expectEqual(@as(usize, 0), harness.backing.recorder.lines.items.len);
    try testing.expectEqual(@as(usize, 0), harness.scripted.at);
}

test "a serve loop told to stop answers nothing further, however many lines the host still had queued" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{ "{\"id\":1,\"op\":\"adapters\"}\n", "{\"id\":2,\"op\":\"adapters\"}\n" });
    defer harness.deinit();
    stop_at_cycle = 1;
    stop_cycles = 0;
    try harness.runUntilStopped(stopAfterCycle);
    try testing.expectEqual(@as(usize, 1), harness.backing.recorder.lines.items.len);
    try testing.expectEqual(@as(usize, 1), harness.scripted.at);
    try testing.expect(std.mem.indexOf(u8, harness.backing.recorder.lines.items[0], "\"id\":1") != null);
}

test "a request stream that never ends its line is a framing defect, bounded" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{ .frame_limit = 256 }, &.{"q" ** 400});
    defer harness.deinit();
    var frontend = try Frontend.init(testing.allocator, &harness.backing.hub, harness.backing.recorder.sink(), .{ .frame_limit = 256 });
    defer frontend.deinit();
    try testing.expectError(error.FrameLimitTooSmall, serve(testing.allocator, &frontend, .{
        .read = Scripted.read,
        .context = &harness.scripted,
        .readable = null,
    }));
    try testing.expectEqual(@as(usize, 0), harness.backing.recorder.lines.items.len);
    try testing.expectEqualStrings("q" ** 400, frontend.defect().?);
    try testing.expect(!frontend.recorded.cut);
}

test "a read that fails is its own failure, not a framing defect" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{"{\"id\":1,\"op\":\"adapters\"}\n"});
    defer harness.deinit();
    harness.failReadAt(1);
    try testing.expectError(error.InputFailed, harness.run());
}

test "a host that stopped reading ends the serve instead of being retried forever" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{"{\"id\":1,\"op\":\"adapters\"}\n"});
    defer harness.deinit();
    try testing.expectError(error.OutputStalled, harness.runRefusingWrites());
    try testing.expectEqual(@as(usize, 0), harness.backing.recorder.lines.items.len);
}

test "a final line the host never terminated is a framing defect, not a dropped request" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{"{\"id\":1,\"op\":\"adapters\"}\n{\"id\":2,\"op\":\"adap"});
    defer harness.deinit();
    try testing.expectError(error.StdinFailed, harness.run());
    try testing.expectEqual(@as(usize, 1), harness.backing.recorder.lines.items.len);
}

test "a framing defect names the line that caused it, bounded" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{});
    defer harness.deinit();
    var short = try Frontend.init(testing.allocator, &harness.backing.hub, harness.backing.recorder.sink(), .{});
    defer short.deinit();
    try testing.expect(short.defect() == null);
    try testing.expectError(error.MalformedLine, short.handleLine("not json"));
    try testing.expectEqualStrings("not json", short.defect().?);
    var long = try Frontend.init(testing.allocator, &harness.backing.hub, harness.backing.recorder.sink(), .{});
    defer long.deinit();
    try testing.expectError(error.MalformedLine, long.handleLine("q" ** 900));
    try testing.expectEqual(@as(usize, 512), long.defect().?.len);
    try testing.expect(long.recorded.cut);
}

test "the serve loop stops at a framing defect rather than answering the rest" {
    const harness = try ScriptedHarness.init(testing.allocator, .{}, .{}, &.{
        "{\"id\":1,\"op\":\"adapters\"}\nnot json\n{\"id\":2,\"op\":\"sessions\"}\n",
    });
    defer harness.deinit();
    try testing.expectError(error.StdinFailed, harness.run());
    try testing.expectEqual(@as(usize, 1), harness.backing.recorder.lines.items.len);
}

test "models and tools answer a catalog, stamped with the lister's revision" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const opened = try harness.hub.open(arena, "reference", .{ .session_id = "catalog" });

    try harness.send("{\"id\":1,\"op\":\"models\",\"session_id\":\"catalog\"}");
    const models = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("models.response", try textMember(harness.arena(), models, "type"));
    try testing.expectEqualStrings("oap-response-1", try textMember(harness.arena(), models, "id"));
    try testing.expectEqualStrings("oap-request-2", try textMember(harness.arena(), models, "in_reply_to"));
    try testing.expectEqualStrings("catalog", try textMember(harness.arena(), models, "session_id"));
    try testing.expectEqualStrings("reference-lister-v2", try textMember(harness.arena(), models, "capability_revision"));
    const models_payload = models.object.get("payload").?;
    try testing.expectEqualStrings("catalog", try textMember(harness.arena(), models_payload, "session_id"));

    try harness.send("{\"id\":2,\"op\":\"tools\",\"session_id\":\"catalog\"}");
    const tools = (try harness.lastValue()).object.get("result").?;
    try testing.expectEqualStrings("action.tools.list.response", try textMember(harness.arena(), tools, "type"));
    try testing.expectEqualStrings("oap-response-3", try textMember(harness.arena(), tools, "id"));
    try testing.expectEqualStrings("oap-request-4", try textMember(harness.arena(), tools, "in_reply_to"));
    try testing.expectEqualStrings("reference-lister-v2", try textMember(harness.arena(), tools, "capability_revision"));
    _ = opened;
}

test "a catalog refuses an unknown session, and names a parameter the op does not take" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"models\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try testing.expectEqualStrings("no session \"absent\"", try harness.message());

    try harness.send("{\"id\":2,\"op\":\"models\",\"run_id\":\"r1\"}");
    const refused = (try harness.lastValue()).object.get("error").?.object;
    try testing.expectEqualStrings("invalid_request", refused.get("code").?.string);
    try testing.expectEqualStrings("op \"models\" accepts no run_id parameter", refused.get("message").?.string);

    try harness.send("{\"id\":3,\"op\":\"tools\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try testing.expectEqualStrings("no session \"\"", try harness.message());
}

test "a catalog the lister serves with no revision is refused, not stamped" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "unlabelled" });
    reference_holder.lister_revision = "";
    defer reference_holder.lister_revision = "reference-lister-v2";
    try harness.send("{\"id\":1,\"op\":\"models\",\"session_id\":\"unlabelled\"}");
    try testing.expectEqualStrings("internal", try harness.code());
    try testing.expectEqualStrings("the adapter served a catalog with no capability revision", try harness.message());
}

test "the two catalog ops fall back to their own code, not a shared one" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "unlabelled" });
    reference_holder.lister_revision = "";
    defer reference_holder.lister_revision = "reference-lister-v2";
    try harness.send("{\"id\":1,\"op\":\"tools\",\"session_id\":\"unlabelled\"}");
    try testing.expectEqualStrings("tools_failed", try harness.code());
    try harness.send("{\"id\":2,\"op\":\"models\",\"session_id\":\"unlabelled\"}");
    try testing.expectEqualStrings("internal", try harness.code());
}

test "an adapter with no lister is unsupported_feature, naming the feature each op asked for" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "bare" });
    reference_holder.has_lister = false;
    defer reference_holder.has_lister = true;

    try harness.send("{\"id\":1,\"op\":\"models\",\"session_id\":\"bare\"}");
    const models = try harness.lastValue();
    const models_details = models.object.get("error").?.object.get("details").?;
    try testing.expectEqualStrings("models.list", try textMember(harness.arena(), models_details, "feature"));
    try testing.expectEqualStrings("unadvertised", try textMember(harness.arena(), models_details, "reason"));
    try harness.send("{\"id\":2,\"op\":\"tools\",\"session_id\":\"bare\"}");
    const tools = try harness.lastValue();
    const tools_details = tools.object.get("error").?.object.get("details").?;
    try testing.expectEqualStrings("action.tools.list", try textMember(harness.arena(), tools_details, "feature"));
}

test "a session that closes under a catalog is told session_closed, and is gone after" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "closing" });
    reference_holder.lister_closed = true;
    defer reference_holder.lister_closed = false;

    try harness.send("{\"id\":1,\"op\":\"models\",\"session_id\":\"closing\"}");
    try testing.expectEqualStrings("session_closed", try harness.code());
    try harness.send("{\"id\":2,\"op\":\"tools\",\"session_id\":\"closing\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
}

test "a null parameter is supplied and refused, and a wrongly typed one is a defect" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"adapters\",\"adapter\":null}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":2,\"op\":\"sessions\",\"session_id\":null}");
    const refused = (try harness.lastValue()).object.get("error").?.object;
    try testing.expectEqualStrings("invalid_request", refused.get("code").?.string);
    try testing.expectEqualStrings("op \"sessions\" accepts no session_id parameter", refused.get("message").?.string);
    try harness.send("{\"id\":7,\"op\":\"adapters\",\"allow_degraded_features\":null}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":8,\"op\":\"adapters\",\"after\":null}");
    try testing.expectEqualStrings("invalid_request", try harness.code());

    for ([_][]const u8{
        "{\"id\":3,\"op\":\"adapters\",\"adapter\":7}",
        "{\"id\":4,\"op\":\"state\",\"session_id\":[\"a\"]}",
        "{\"id\":5,\"op\":\"state\",\"run_id\":true}",
        "{\"id\":6,\"op\":\"open\",\"allow_degraded_features\":\"a\"}",
    }) |line| {
        const built = try harness.arena().dupe(u8, line);
        try testing.expectError(error.MalformedLine, decode(harness.arena(), built));
    }
}

test "a refusal's message is bounded so a long one still frames" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const name = "adapter-" ++ "n" ** 400;
    const line = try std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"capabilities\",\"adapter\":\"{s}\"}}", .{name});
    try testing.expect(std.unicode.utf8ValidateSlice(line));
    try harness.send(line);
    const message = (try harness.lastValue()).object.get("error").?.object.get("message").?.string;
    try testing.expect(std.mem.endsWith(u8, message, "\u{2026}"));
    try testing.expect(std.unicode.utf8CountCodepoints(message) catch 0 <= message_limit + 1);
    try testing.expect(std.unicode.utf8ValidateSlice(message));
}

test "a bounded message is cut on a character boundary, not inside one" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const name = "adapter-" ++ "\u{00e9}" ** 400;
    const line = try std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"capabilities\",\"adapter\":\"{s}\"}}", .{name});
    try harness.send(line);
    const message = (try harness.lastValue()).object.get("error").?.object.get("message").?.string;
    try testing.expect(std.unicode.utf8ValidateSlice(message));
    try testing.expect(std.mem.endsWith(u8, message, "\u{2026}"));
    try testing.expect(std.unicode.utf8CountCodepoints(message) catch 0 <= message_limit + 1);

    var varied: [1200]u8 = undefined;
    for (&varied, 0..) |*byte, index| byte.* = if (index % 3 == 0) 0xC3 else if (index % 3 == 1) 0xA9 else 'a';
    const mixed = try std.fmt.allocPrint(harness.arena(), "{{\"id\":2,\"op\":\"capabilities\",\"adapter\":\"{s}\"}}", .{varied[0..]});
    try testing.expect(std.unicode.utf8ValidateSlice(mixed));
    try harness.send(mixed);
    const second = (try harness.lastValue()).object.get("error").?.object.get("message").?.string;
    try testing.expect(std.unicode.utf8ValidateSlice(second));
    try testing.expect(std.mem.endsWith(u8, second, "\u{2026}"));
}

test "the sessions listing carries each session's runs as an array of objects" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    _ = try harness.hub.open(arena, "reference", .{ .session_id = "listed" });

    try harness.send("{\"id\":1,\"op\":\"sessions\"}");
    const rows = (try harness.lastValue()).object.get("result").?.object.get("sessions").?.array;
    try testing.expectEqual(@as(usize, 1), rows.items.len);
    try testing.expectEqualStrings("listed", try textMember(harness.arena(), rows.items[0], "session_id"));
    try testing.expectEqualStrings("reference", try textMember(harness.arena(), rows.items[0], "adapter"));
}

test "a closed session answers its final state, and the next one is unknown" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    _ = try harness.hub.open(arena_state.allocator(), "reference", .{ .session_id = "ended" });
    reference_holder.closed = true;
    defer reference_holder.closed = false;
    try harness.send("{\"id\":1,\"op\":\"state\",\"session_id\":\"ended\"}");
    const envelope = (try harness.lastValue()).object.get("result").?;
    const payload = envelope.object.get("payload").?;
    try testing.expectEqualStrings("closed", try textMember(harness.arena(), payload, "status"));
    try testing.expectEqualStrings("ended", try textMember(harness.arena(), payload, "session_id"));
    try harness.send("{\"id\":2,\"op\":\"state\",\"session_id\":\"ended\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
}

test "a state that cannot be read is state_failed, not internal" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"state\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());

    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    _ = try harness.hub.open(arena_state.allocator(), "reference", .{ .session_id = "unreadable" });
    reference_holder.state_fails = true;
    defer reference_holder.state_fails = false;
    try harness.send("{\"id\":2,\"op\":\"state\",\"session_id\":\"unreadable\"}");
    try testing.expectEqualStrings("state_failed", try harness.code());

    reference_holder.state_fails = false;
    try harness.send("{\"id\":3,\"op\":\"state\",\"session_id\":\"unreadable\"}");
    try testing.expectEqualStrings("session.state.response", try textMember(harness.arena(), (try harness.lastValue()).object.get("result").?, "type"));
}

const open_envelope =
    "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\"}}";

fn openLine(arena: std.mem.Allocator, adapter: []const u8, envelope: []const u8) ![]const u8 {
    return std.fmt.allocPrint(arena, "{{\"id\":1,\"op\":\"open\",\"adapter\":\"{s}\",\"request\":{s}}}", .{ adapter, envelope });
}

fn openResult(harness: *Harness) !std.json.ObjectMap {
    return (try harness.lastValue()).object.get("result").?.object;
}

fn listedSessions(harness: *Harness) !usize {
    try harness.send("{\"id\":2,\"op\":\"sessions\"}");
    return (try openResult(harness)).get("sessions").?.array.items.len;
}

const event_head = "\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\"";
const started_line = "{" ++ event_head ++ ",\"type\":\"run.started\",\"id\":\"e1\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"sequence\":1,\"payload\":{\"session_id\":\"s1\",\"run_id\":\"run-1\"}}";
const completed_line = "{" ++ event_head ++ ",\"type\":\"run.completed\",\"id\":\"e2\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"sequence\":2,\"payload\":{\"session_id\":\"s1\",\"run_id\":\"run-1\"}}";

test "events acknowledges, then streams the run's envelopes on its own id and ends at the terminal" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    defer reference_holder.pending_events = &.{};
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    try harness.send("{\"id\":2,\"op\":\"events\",\"session_id\":\"s1\"}");
    try testing.expectEqualStrings("{\"id\":2,\"ok\":true,\"result\":null}", harness.recorder.last());
    const before = harness.recorder.lines.items.len;
    reference_holder.pending_events = &.{ .{ .line = started_line, .run_id = "run-1", .sequence = 1 }, .{ .line = completed_line, .run_id = "run-1", .sequence = 2 } };
    try harness.hub.pump(testing.allocator, 0);
    try harness.frontend.pumpStreams();
    const streamed = harness.recorder.lines.items[before..];
    try testing.expectEqual(@as(usize, 2), streamed.len);
    for (streamed, [_]i64{ 1, 2 }) |line, sequence| {
        const value = try std.json.parseFromSliceLeaky(std.json.Value, harness.arena(), line, .{});
        try testing.expectEqualStrings("envelope", value.object.get("event").?.string);
        try testing.expectEqual(@as(i64, 2), value.object.get("id").?.integer);
        try testing.expectEqualStrings("s1", value.object.get("session_id").?.string);
        try testing.expectEqual(sequence, value.object.get("sequence").?.integer);
        try testing.expectEqualStrings("run-1", value.object.get("envelope").?.object.get("run_id").?.string);
    }
    try testing.expectEqual(@as(usize, 0), harness.frontend.streams.items.len);
}

const replied_line = "{" ++ event_head ++ ",\"type\":\"run.completed\",\"id\":\"e2\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"sequence\":2,\"payload\":{\"session_id\":\"s1\",\"run_id\":\"run-1\",\"final_response\":{\"id\":\"m1\",\"role\":\"assistant\",\"content\":\"pong\"}}}";
const failed_line = "{" ++ event_head ++ ",\"type\":\"run.failed\",\"id\":\"e2\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"sequence\":2,\"payload\":{\"session_id\":\"s1\",\"run_id\":\"run-1\"}}";
const cancelled_line = "{" ++ event_head ++ ",\"type\":\"run.cancelled\",\"id\":\"e2\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"sequence\":2,\"payload\":{\"session_id\":\"s1\",\"run_id\":\"run-1\"}}";

fn workResult(harness: *Harness, id: i64) !std.json.ObjectMap {
    try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":{d},\"op\":\"work.status\",\"session_id\":\"s1\"}}", .{id}));
    return (try harness.lastValue()).object.get("result").?.object;
}

test "work.status names an idle session that never ran done, a running one running, and an absent one unknown_session" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    const idle = try workResult(harness, 2);
    try testing.expectEqualStrings("done", idle.get("status").?.string);
    try testing.expectEqualStrings("s1", idle.get("ref").?.object.get("session_id").?.string);
    try testing.expectEqualStrings("reference", idle.get("ref").?.object.get("adapter").?.string);
    try testing.expect(idle.get("last_reply") == null);

    try harness.send(try controlLine(harness.arena(), 3, op_submit, "s1", "{" ++ control_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"sub-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}"));
    const running = try workResult(harness, 4);
    try testing.expectEqualStrings("running", running.get("status").?.string);
    try testing.expectEqualStrings("run-1", running.get("run_id").?.string);

    try harness.send("{\"id\":5,\"op\":\"work.status\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
}

test "work.list adds an adapter's own sessions only on include_native, and names an adapter that could not list" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"work.list\"}");
    try testing.expectEqual(@as(usize, 0), (try harness.lastValue()).object.get("result").?.object.get("groups").?.array.items.len);

    try harness.send("{\"id\":2,\"op\":\"work.list\",\"request\":{\"include_native\":true}}");
    const groups = (try harness.lastValue()).object.get("result").?.object.get("groups").?.array.items;
    try testing.expectEqual(@as(usize, 1), groups.len);
    try testing.expectEqualStrings("/work/a", groups[0].object.get("directory").?.string);
    const work = groups[0].object.get("work").?.array.items;
    try testing.expectEqual(@as(usize, 4), work.len);
    try testing.expectEqualStrings("thread-b", work[0].object.get("ref").?.object.get("native_id").?.string);
    try testing.expectEqualStrings("running", work[0].object.get("state").?.string);
    try testing.expectEqual(true, work[0].object.get("native").?.bool);
    try testing.expectEqualStrings("older thread", work[3].object.get("title").?.string);

    reference_holder.native_fails = true;
    defer reference_holder.native_fails = false;
    try harness.send("{\"id\":3,\"op\":\"work.list\",\"request\":{\"include_native\":true}}");
    const failed = (try harness.lastValue()).object.get("result").?.object;
    try testing.expectEqual(@as(usize, 0), failed.get("groups").?.array.items.len);
    const unavailable = failed.get("unavailable").?.array.items;
    try testing.expectEqual(@as(usize, 2), unavailable.len);
    try testing.expectEqualStrings("reference", unavailable[0].object.get("adapter").?.string);
    try testing.expectEqualStrings("the harness would not list", unavailable[0].object.get("message").?.string);
}

test "work.status answers session_closed for a session it finds closed, releases it, and is then unknown; work.list releases it too" {
    for ([_][]const u8{ "work.status", "work.list" }) |op| {
        const harness = try Harness.init(testing.allocator, .{}, .{});
        defer harness.deinit();
        try harness.send(try openLine(harness.arena(), "reference", open_envelope));
        reference_holder.closed = true;
        defer reference_holder.closed = false;
        if (std.mem.eql(u8, op, "work.status")) {
            try harness.send("{\"id\":2,\"op\":\"work.status\",\"session_id\":\"s1\"}");
            try testing.expectEqualStrings("session_closed", try harness.code());
            try harness.send("{\"id\":3,\"op\":\"work.status\",\"session_id\":\"s1\"}");
            try testing.expectEqualStrings("unknown_session", try harness.code());
        } else {
            try harness.send("{\"id\":2,\"op\":\"work.list\"}");
            try testing.expectEqual(@as(usize, 0), (try harness.lastValue()).object.get("result").?.object.get("groups").?.array.items.len);
        }
        try testing.expectEqual(@as(usize, 0), harness.hub.sessionCount());
    }
}

test "work.status of an idle session reads its latest run's terminal: completed is done with the reply, failed is failed, cancelled is stopped" {
    const cases = [_]struct { line: []const u8, status: []const u8, reply: ?[]const u8 }{
        .{ .line = replied_line, .status = "done", .reply = "pong" },
        .{ .line = failed_line, .status = "failed", .reply = null },
        .{ .line = cancelled_line, .status = "stopped", .reply = null },
    };
    for (cases) |case| {
        const harness = try Harness.init(testing.allocator, .{}, .{});
        defer harness.deinit();
        defer reference_holder.pending_events = &.{};
        try harness.send(try openLine(harness.arena(), "reference", open_envelope));
        reference_holder.pending_events = &.{ .{ .line = started_line, .run_id = "run-1", .sequence = 1 }, .{ .line = case.line, .run_id = "run-1", .sequence = 2 } };
        try harness.hub.pump(testing.allocator, 0);
        reference_holder.pending_events = &.{};
        const settled = try workResult(harness, 2);
        try testing.expectEqualStrings(case.status, settled.get("status").?.string);
        try testing.expectEqualStrings("run-1", settled.get("run_id").?.string);
        if (case.reply) |reply| {
            try testing.expectEqualStrings(reply, settled.get("last_reply").?.string);
        } else {
            try testing.expect(settled.get("last_reply") == null);
        }
    }
}

test "work.start opens with the message and its title, work.send adds a turn, and work.read returns both and the run's reply" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    defer reference_holder.pending_events = &.{};
    try harness.send("{\"id\":1,\"op\":\"work.start\",\"adapter\":\"reference\",\"request\":{\"message\":\"go\",\"title\":\"a task\"}}");
    const started = (try harness.lastValue()).object.get("result").?.object;
    try testing.expectEqualStrings("running", started.get("status").?.string);
    try testing.expectEqualStrings("a task", started.get("title").?.string);
    const session_id = started.get("ref").?.object.get("session_id").?.string;

    reference_holder.pending_events = &.{ .{ .line = started_line, .run_id = "run-1", .sequence = 1 }, .{ .line = replied_line, .run_id = "run-1", .sequence = 2 } };
    try harness.hub.pump(testing.allocator, 0);
    reference_holder.pending_events = &.{};
    try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":2,\"op\":\"work.send\",\"session_id\":\"{s}\",\"request\":{{\"message\":\"more\"}}}}", .{session_id}));
    try testing.expect((try harness.lastValue()).object.get("result") != null);

    try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":3,\"op\":\"work.read\",\"session_id\":\"{s}\"}}", .{session_id}));
    const turns = (try harness.lastValue()).object.get("result").?.object.get("turns").?.array.items;
    try testing.expectEqual(@as(usize, 3), turns.len);
    try testing.expectEqualStrings("user", turns[0].object.get("role").?.string);
    try testing.expectEqualStrings("go", turns[0].object.get("text").?.string);
    try testing.expectEqualStrings("assistant", turns[1].object.get("role").?.string);
    try testing.expectEqualStrings("pong", turns[1].object.get("text").?.string);
    try testing.expectEqualStrings("completed", turns[1].object.get("outcome").?.string);
    try testing.expectEqualStrings("more", turns[2].object.get("text").?.string);

    try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":4,\"op\":\"work.read\",\"session_id\":\"{s}\",\"after\":0,\"limit\":1}}", .{session_id}));
    const paged = (try harness.lastValue()).object.get("result").?.object.get("turns").?.array.items;
    try testing.expectEqual(@as(usize, 1), paged.len);
    try testing.expectEqual(@as(i64, 1), paged[0].object.get("index").?.integer);
}

test "work.read refuses an unreadable after and answers no turns past the largest one" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    try harness.send("{\"id\":2,\"op\":\"work.read\",\"session_id\":\"s1\",\"after\":\"x\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    const read = try harness.hub.transcript("s1", std.math.maxInt(u64), 10);
    try testing.expectEqual(@as(usize, 0), read.turns.len);
}

test "work.start refuses a missing message, an unknown adapter and a directory its adapter does not run in" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send("{\"id\":1,\"op\":\"work.start\",\"adapter\":\"reference\",\"request\":{}}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":2,\"op\":\"work.start\",\"adapter\":\"nope\",\"request\":{\"message\":\"go\"}}");
    try testing.expectEqualStrings("unknown_adapter", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"work.start\",\"adapter\":\"reference\",\"request\":{\"message\":\"go\",\"directory\":\"/elsewhere\"}}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try testing.expectEqual(@as(usize, 0), harness.hub.sessionCount());
}

test "work.stop cancels the running run and answers a session with none unchanged" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    const idle = try workResult(harness, 2);
    try harness.send("{\"id\":3,\"op\":\"work.stop\",\"session_id\":\"s1\"}");
    try testing.expectEqualStrings(idle.get("status").?.string, (try harness.lastValue()).object.get("result").?.object.get("status").?.string);
    try harness.send(try controlLine(harness.arena(), 4, op_submit, "s1", "{" ++ control_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"sub-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}"));
    try testing.expectEqualStrings("running", (try workResult(harness, 5)).get("status").?.string);
    try harness.send("{\"id\":6,\"op\":\"work.stop\",\"session_id\":\"s1\"}");
    try testing.expect(!std.mem.eql(u8, "running", (try harness.lastValue()).object.get("result").?.object.get("status").?.string));
    try testing.expect(!reference_holder.running);
}

test "work.list groups the sessions it holds by directory and refuses a parameter it does not take" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    try harness.send("{\"id\":2,\"op\":\"work.list\"}");
    const groups = (try harness.lastValue()).object.get("result").?.object.get("groups").?.array.items;
    try testing.expectEqual(@as(usize, 1), groups.len);
    const work = groups[0].object.get("work").?.array.items;
    try testing.expectEqual(@as(usize, 1), work.len);
    try testing.expectEqualStrings("s1", work[0].object.get("ref").?.object.get("session_id").?.string);
    try harness.send("{\"id\":3,\"op\":\"work.list\",\"session_id\":\"s1\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
}

test "events refuses an absent session ahead of everything, an unreadable cursor, a run without a cursor and a parameter it does not take" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    try harness.send("{\"id\":2,\"op\":\"events\",\"session_id\":\"absent\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"events\",\"session_id\":\"s1\",\"run_id\":\"run-1\"}");
    try testing.expectEqualStrings("invalid_cursor", try harness.code());
    try harness.send("{\"id\":4,\"op\":\"events\",\"session_id\":\"s1\",\"adapter\":\"reference\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    for ([_][]const u8{ "\"8\"", "-1", "1.5" }) |cursor| {
        try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":5,\"op\":\"events\",\"session_id\":\"s1\",\"after\":{s}}}", .{cursor}));
        try testing.expectEqualStrings("invalid_cursor", try harness.code());
    }
    try harness.send("{\"id\":6,\"op\":\"events\",\"session_id\":\"absent\",\"run_id\":\"run-1\"}");
    try testing.expectEqualStrings("unknown_session", try harness.code());
    try testing.expectEqual(@as(usize, 0), harness.frontend.streams.items.len);
}

test "a signal too long for the frame limit falls back to its event, id and numbers" {
    const harness = try Harness.init(testing.allocator, .{}, .{ .frame_limit = minimum_frame_limit });
    defer harness.deinit();
    var object = try Frontend.signalObject(harness.arena(), "oap-overflow", 9, "s1");
    try object.put(harness.arena(), "run_id", .{ .string = &([_]u8{'r'} ** minimum_frame_limit) });
    try object.put(harness.arena(), "last_sequence", .{ .integer = 4 });
    try harness.frontend.writeSignal(harness.arena(), object);
    try testing.expectEqualStrings("{\"event\":\"oap-overflow\",\"id\":9,\"last_sequence\":4}", harness.recorder.last());
}

test "a subscription whose session closes ends with oap-session-closed" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    try harness.send("{\"id\":2,\"op\":\"events\",\"session_id\":\"s1\"}");
    try testing.expectEqual(@as(usize, 1), harness.frontend.streams.items.len);
    try harness.send("{\"id\":3,\"op\":\"close\",\"session_id\":\"s1\"}");
    try harness.frontend.pumpStreams();
    var saw_closed = false;
    for (harness.recorder.lines.items) |line| {
        if (std.mem.indexOf(u8, line, "\"event\":\"oap-session-closed\"") != null and std.mem.indexOf(u8, line, "\"id\":2") != null) saw_closed = true;
    }
    try testing.expect(saw_closed);
    try testing.expectEqual(@as(usize, 0), harness.frontend.streams.items.len);
}

const control_head = "\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\"";

fn controlLine(arena: std.mem.Allocator, id: i64, op: []const u8, session_id: []const u8, envelope: []const u8) ![]const u8 {
    return std.fmt.allocPrint(arena, "{{\"id\":{d},\"op\":\"{s}\",\"session_id\":\"{s}\",\"request\":{s}}}", .{ id, op, session_id, envelope });
}

fn resultType(harness: *Harness) ![]const u8 {
    return (try openResult(harness)).get("type").?.string;
}

test "submit, resolve and cancel answer over stdio as they do over HTTP, each correlated to its request" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));

    try harness.send(try controlLine(harness.arena(), 2, op_submit, "s1", "{" ++ control_head ++ ",\"type\":\"session.message.submit.request\",\"id\":\"sub-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}"));
    try testing.expectEqualStrings("session.message.submit.response", try resultType(harness));
    try testing.expectEqualStrings("sub-1", (try openResult(harness)).get("in_reply_to").?.string);
    try testing.expectEqualStrings("run-1", (try openResult(harness)).get("run_id").?.string);

    try harness.send(try controlLine(harness.arena(), 3, op_resolve, "s1", "{" ++ control_head ++ ",\"type\":\"action.permission.resolve.request\",\"id\":\"res-1\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"payload\":{\"interaction_id\":\"i1\",\"requested_by\":\"agent\",\"responded_by\":\"user\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"granted\":true}}"));
    try testing.expectEqualStrings("action.permission.resolve.response", try resultType(harness));
    try testing.expectEqualStrings("res-1", (try openResult(harness)).get("in_reply_to").?.string);

    try harness.send(try controlLine(harness.arena(), 4, op_cancel, "s1", "{" ++ control_head ++ ",\"type\":\"run.cancel.request\",\"id\":\"can-1\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"s1\",\"run_id\":\"run-1\"}}"));
    try testing.expectEqualStrings("run.cancel.response", try resultType(harness));
    try testing.expectEqualStrings("can-1", (try openResult(harness)).get("in_reply_to").?.string);
}

test "the control ops refuse as the HTTP routes do: an absent session, a missing request, a parameter they do not take, an adapter without settings" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    const cancel_absent = "{" ++ control_head ++ ",\"type\":\"run.cancel.request\",\"id\":\"can-2\",\"session_id\":\"absent\",\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"absent\",\"run_id\":\"run-1\"}}";
    try harness.send(try controlLine(harness.arena(), 2, op_cancel, "absent", cancel_absent));
    try testing.expectEqualStrings("unknown_session", try harness.code());

    try harness.send("{\"id\":3,\"op\":\"submit\",\"session_id\":\"s1\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());

    try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":4,\"op\":\"cancel\",\"session_id\":\"s1\",\"run_id\":\"run-1\",\"request\":{s}}}", .{cancel_absent}));
    try testing.expectEqualStrings("invalid_request", try harness.code());

    try harness.send(try controlLine(harness.arena(), 5, op_settings, "s1", "{" ++ control_head ++ ",\"type\":\"session.settings.update.request\",\"id\":\"set-1\",\"session_id\":\"s1\",\"payload\":{\"session_id\":\"s1\",\"compaction_policy\":{\"kind\":\"off\"}}}"));
    try testing.expectEqualStrings("unsupported_feature", try harness.code());
}

test "an open answers with the request envelope's own id and the session it made" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    try harness.send(try openLine(harness.arena(), "reference", open_envelope));
    const result = try openResult(harness);
    try testing.expectEqualStrings("session.open.response", result.get("type").?.string);
    try testing.expectEqualStrings("o1", result.get("in_reply_to").?.string);
    try testing.expectEqualStrings("s1", result.get("session_id").?.string);
    try testing.expect(!std.mem.eql(u8, result.get("id").?.string, "o1"));
    try testing.expectEqual(@as(usize, 1), try listedSessions(harness));
}

test "an open carrying a message admits it, and its answer names the run the message started" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const envelope = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"message\":{\"messages\":[{\"role\":\"user\",\"content\":\"go\"}],\"delivery\":\"auto\"}}}";
    try harness.send(try openLine(harness.arena(), "reference", envelope));
    const result = try openResult(harness);
    try testing.expectEqualStrings("session.open.response", result.get("type").?.string);
    const state = result.get("payload").?.object;
    try testing.expectEqualStrings("running", state.get("status").?.string);
    try testing.expectEqualStrings("run-1", state.get("active_run_id").?.string);
    const run = state.get("active_runs").?.array.items[0].object;
    try testing.expectEqualStrings("run-1", run.get("run_id").?.string);
    try testing.expectEqualStrings("primary", run.get("relationship").?.string);
    try testing.expectEqualStrings("o1", run.get("admitted_submit_requests").?.array.items[0].string);
    try testing.expectEqual(@as(usize, 1), try listedSessions(harness));
}

test "an open whose message the adapter refuses answers that refusal and leaves no session behind" {
    reference_holder.submit_refuses = true;
    defer reference_holder.submit_refuses = false;
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const envelope = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"message\":{\"messages\":[{\"role\":\"user\",\"content\":\"go\"}],\"delivery\":\"auto\"}}}";
    try harness.send(try openLine(harness.arena(), "reference", envelope));
    try testing.expectEqualStrings("unsupported_feature", try harness.code());
    try testing.expectEqual(@as(usize, 0), try listedSessions(harness));
}

test "a subscribing open past the subscription ceiling is busy, and one whose answer cannot be framed streams nothing" {
    const subscribing = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"capability_revision\":\"reference-v1\",\"payload\":{\"session_id\":\"s1\",\"subscribe\":true}}";
    {
        const harness = try Harness.init(testing.allocator, .{}, .{ .max_subscriptions = 1 });
        defer harness.deinit();
        try harness.send(try openLine(harness.arena(), "reference", subscribing));
        try testing.expectEqual(@as(usize, 1), harness.frontend.streams.items.len);
        const second = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o2\",\"capability_revision\":\"reference-v1\",\"payload\":{\"session_id\":\"s2\",\"subscribe\":true}}";
        try harness.send(try openLine(harness.arena(), "reference", second));
        try testing.expectEqualStrings("busy", try harness.code());
        try testing.expect(!harness.hub.knows("s2"));
    }
    {
        const unnamed = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o3\",\"capability_revision\":\"reference-v1\",\"payload\":{\"subscribe\":true}}";
        const request_line = try openLine(testing.allocator, "reference", unnamed);
        defer testing.allocator.free(request_line);
        const harness = try Harness.init(testing.allocator, .{}, .{ .frame_limit = @max(request_line.len + 1, minimum_frame_limit) });
        defer harness.deinit();
        try harness.send(request_line);
        try testing.expectEqualStrings("response_too_large", try harness.code());
        try testing.expectEqual(@as(usize, 0), harness.frontend.streams.items.len);
        try testing.expectEqual(@as(usize, 0), (try harness.hub.sessions(harness.arena())).len);
    }
}

test "a subscribing open answers, then streams the session's envelopes on the open's own id" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    defer reference_holder.pending_events = &.{};
    const envelope = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"capability_revision\":\"reference-v1\",\"payload\":{\"session_id\":\"s1\",\"subscribe\":true}}";
    try harness.send(try openLine(harness.arena(), "reference", envelope));
    try testing.expectEqualStrings("session.open.response", (try openResult(harness)).get("type").?.string);
    try testing.expectEqual(@as(usize, 1), harness.frontend.streams.items.len);
    const before = harness.recorder.lines.items.len;
    reference_holder.pending_events = &.{ .{ .line = started_line, .run_id = "run-1", .sequence = 1 }, .{ .line = completed_line, .run_id = "run-1", .sequence = 2 } };
    try harness.hub.pump(testing.allocator, 0);
    try harness.frontend.pumpStreams();
    const streamed = harness.recorder.lines.items[before..];
    try testing.expectEqual(@as(usize, 2), streamed.len);
    for (streamed) |line| {
        const value = try std.json.parseFromSliceLeaky(std.json.Value, harness.arena(), line, .{});
        try testing.expectEqualStrings("envelope", value.object.get("event").?.string);
        try testing.expectEqual(@as(i64, 1), value.object.get("id").?.integer);
    }
    try testing.expectEqual(@as(usize, 0), harness.frontend.streams.items.len);
}

test "a refusal carries the adapter's own feature and reason, and nothing more" {
    const arena = testing.allocator;
    const both = try detailForReason(arena, .{ .feature = "session.open.subscribe", .reason = contract.reason_unadvertised });
    defer if (both.len > 0) arena.free(both);
    try testing.expectEqual(@as(usize, 2), both.len);
    try testing.expectEqualStrings("feature", both[0].key);
    try testing.expectEqualStrings("session.open.subscribe", both[0].value);
    try testing.expectEqualStrings("reason", both[1].key);
    try testing.expectEqualStrings("unadvertised", both[1].value);

    const feature_only = try detailForReason(arena, .{ .feature = "session.tool_sources.attach" });
    defer if (feature_only.len > 0) arena.free(feature_only);
    try testing.expectEqual(@as(usize, 1), feature_only.len);
    try testing.expectEqualStrings("session.tool_sources.attach", feature_only[0].value);

    const named_source = try detailForReason(arena, .{
        .feature = "session.tool_sources.attach",
        .reason = contract.reason_unsatisfiable,
        .source = "x1",
    });
    defer if (named_source.len > 0) arena.free(named_source);
    try testing.expectEqual(@as(usize, 3), named_source.len);
    try testing.expectEqualStrings("session.tool_sources.attach", named_source[0].value);
    try testing.expectEqualStrings("unsatisfiable", named_source[1].value);
    try testing.expectEqualStrings("source", named_source[2].key);
    try testing.expectEqualStrings("x1", named_source[2].value);

    const nothing = try detailForReason(arena, .{});
    try testing.expectEqual(@as(usize, 0), nothing.len);
}

test "an open citing a stale revision is refused, naming both revisions" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const envelope = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"capability_revision\":\"reference-v0\",\"payload\":{\"session_id\":\"s1\",\"tool_sources\":[{\"id\":\"extra\",\"kind\":\"local\"}]}}";
    try harness.send(try openLine(harness.arena(), "reference", envelope));
    try testing.expectEqualStrings("stale_capabilities", try harness.code());
    const details = (try harness.lastValue()).object.get("error").?.object.get("details").?.object;
    try testing.expectEqualStrings("reference-v1", details.get("expected_revision").?.string);
    try testing.expectEqualStrings("reference-v0", details.get("current_revision").?.string);
    try testing.expectEqual(@as(usize, 0), try listedSessions(harness));
}

test "an attachment that names something to run is refused, and no session is made" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const envelopes = [_][]const u8{
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"tool_sources\":[{\"id\":\"l1\",\"kind\":\"local\",\"command\":\"/bin/sh\"}]}}",
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"tool_sources\":[{\"id\":\"l1\",\"kind\":\"local\",\"args\":[\"-c\"]}]}}",
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"tool_sources\":[{\"id\":\"l1\",\"kind\":\"local\",\"environment\":[\"PATH=/tmp\"]}]}}",
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"tool_sources\":[{\"id\":\"l1\",\"kind\":\"process\"}]}}",
    };
    for (envelopes) |envelope| {
        try harness.send(try openLine(harness.arena(), "reference", envelope));
        try testing.expectEqualStrings("unsupported_feature", try harness.code());
    }
    try testing.expectEqual(@as(usize, 0), try listedSessions(harness));
}

test "an unconfigured id may be named from the wire, but a process one may not" {
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const arena = harness.arena();
    const namable = try std.json.parseFromSliceLeaky(
        std.json.Value,
        arena,
        "[{\"id\":\"x1\",\"kind\":\"endpoint\"}]",
        .{},
    );
    try testing.expect((try harness.frontend.attachmentRefusal(arena, namable)) == null);

    const process = try std.json.parseFromSliceLeaky(
        std.json.Value,
        arena,
        "[{\"id\":\"x1\",\"kind\":\"process\"}]",
        .{},
    );
    const refusal = (try harness.frontend.attachmentRefusal(arena, process)).?;
    try testing.expectEqualStrings("unsupported_feature", refusal.code);
    try testing.expectEqual(@as(usize, 3), refusal.details.len);
    try testing.expectEqualStrings(contract.feature_tool_sources_attach, refusal.details[0].value);
    try testing.expectEqualStrings("x1", refusal.details[2].value);
}

test "an allocator that refuses is not a request with no sources" {
    var fails = std.testing.FailingAllocator.init(std.testing.allocator, .{ .fail_index = 0 });
    const configured = [_]contract.ConfiguredSource{.{
        .id = "pinned",
        .kind = "remote",
        .endpoint = "https://operator.test",
    }};
    var hub: Hub = undefined;
    hub = Hub.init(fails.allocator(), wallClock, .{ .tool_sources = &configured });
    defer hub.deinit();
    try testing.expectError(error.OutOfMemory, substitutedSources(fails.allocator(), &hub, "[{\"id\":\"pinned\",\"kind\":\"remote\"}]"));
}

test "the adapter is handed the operator's source, and an unconfigured one as written" {
    const configured = [_]contract.ConfiguredSource{.{
        .id = "pinned",
        .kind = "remote",
        .endpoint = "https://operator.test",
        .environment = &.{ "PATH=/operator", "TOKEN=operator" },
    }};
    const harness = try Harness.init(testing.allocator, .{ .tool_sources = &configured }, .{});
    defer harness.deinit();
    const pinned = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"tool_sources\":[{\"id\":\"pinned\",\"kind\":\"remote\",\"environment\":[\"PATH\",\"EXTRA\"]},{\"id\":\"free\",\"kind\":\"hosted\"}]}}";
    try harness.send(try std.fmt.allocPrint(harness.arena(), "{{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":{s}}}", .{pinned}));

    try testing.expect((try harness.lastValue()).object.get("ok").?.bool);
    const handed = std.json.parseFromSliceLeaky(std.json.Value, harness.arena(), reference_holder.saw_tool_sources_json, .{}) catch return error.TestUnexpectedResult;
    const listed = handed.array.items;
    try testing.expectEqual(@as(usize, 2), listed.len);

    try testing.expectEqualStrings("https://operator.test", listed[0].object.get("endpoint").?.string);
    const environment = listed[0].object.get("environment").?.array.items;
    try testing.expectEqual(@as(usize, 3), environment.len);
    try testing.expectEqualStrings("PATH=/operator", environment[0].string);
    try testing.expectEqualStrings("TOKEN=operator", environment[1].string);
    try testing.expectEqualStrings("EXTRA", environment[2].string);

    try testing.expectEqualStrings("free", listed[1].object.get("id").?.string);
    try testing.expectEqualStrings("hosted", listed[1].object.get("kind").?.string);
}

test "a configured source is named by id, not re-described from the wire" {
    const configured = [_]contract.ConfiguredSource{.{
        .id = "x1",
        .kind = "endpoint",
        .endpoint = "https://operator.test",
    }};
    const harness = try Harness.init(testing.allocator, .{ .tool_sources = &configured }, .{});
    defer harness.deinit();
    const arena = harness.arena();
    const agreeing = try std.json.parseFromSliceLeaky(
        std.json.Value,
        arena,
        "[{\"id\":\"x1\",\"kind\":\"endpoint\",\"endpoint\":\"https://operator.test\"}]",
        .{},
    );
    try testing.expect((try harness.frontend.attachmentRefusal(arena, agreeing)) == null);

    const contested = try std.json.parseFromSliceLeaky(
        std.json.Value,
        arena,
        "[{\"id\":\"x1\",\"kind\":\"local\",\"endpoint\":\"https://wire.test\"}]",
        .{},
    );
    const refusal = (try harness.frontend.attachmentRefusal(arena, contested)).?;
    try testing.expectEqualStrings("unsupported_feature", refusal.code);

    const unstatted = try std.json.parseFromSliceLeaky(
        std.json.Value,
        arena,
        "[{\"id\":\"x1\",\"display_name\":\"Mine\"}]",
        .{},
    );
    const blank = (try harness.frontend.attachmentRefusal(arena, unstatted)).?;
    try testing.expectEqualStrings("unsupported_feature", blank.code);
}

test "a metadata-carrying open hands the adapter what the payload carried" {
    reference_holder.saw_metadata_members = 0;
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const envelope = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"s1\",\"metadata\":{\"tenant\":\"acme\",\"attempt\":3}}}";
    try harness.send(try openLine(harness.arena(), "reference", envelope));
    try testing.expect((try openResult(harness)).get("session_id") != null);
    try testing.expectEqual(@as(usize, 2), reference_holder.saw_metadata_members);
}

test "a metadata at the envelope root is not the payload's metadata" {
    reference_holder.saw_metadata_members = 0;
    const harness = try Harness.init(testing.allocator, .{}, .{});
    defer harness.deinit();
    const envelope = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"metadata\":{\"tenant\":\"acme\"},\"payload\":{\"session_id\":\"s1\"}}";
    try harness.send(try openLine(harness.arena(), "reference", envelope));
    try testing.expect((try openResult(harness)).get("session_id") != null);
    try testing.expectEqual(@as(usize, 0), reference_holder.saw_metadata_members);
}

test "the refusals the open gate and the payload read name" {
    const cases = [_]struct { line: []const u8, code: []const u8 }{
        .{ .line = "{\"id\":1,\"op\":\"open\",\"request\":{}}", .code = "invalid_request" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\"}", .code = "invalid_request" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":null}", .code = "invalid_request" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":{\"id\":\"x\",\"type\":\"nonesuch\"}}", .code = "schema_invalid" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":7}}}", .code = "schema_invalid" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"nope\",\"request\":{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"session.open.request\",\"id\":\"o1\",\"payload\":{\"session_id\":\"z\"}}}", .code = "unknown_adapter" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":7}", .code = "malformed_json" },
        .{ .line = "{\"id\":1,\"op\":\"open\",\"adapter\":\"reference\",\"request\":[]}", .code = "malformed_json" },
    };
    for (cases) |case| {
        const harness = try Harness.init(testing.allocator, .{}, .{});
        defer harness.deinit();
        try harness.send(case.line);
        try testing.expectEqualStrings(case.code, try harness.code());
    }
}

test "every code the draft names has a status, or is one the draft leaves undefined" {
    const named_without_status = [_][]const u8{
        "busy",
        "unknown_op",
        "response_too_large",
        "invalid_request",
        "malformed_json",
        "schema_invalid",
        "type_mismatch",
        "invalid_payload",
    };
    for (named_without_status) |code| {
        try testing.expect(statusForRefusal(code) == null);
    }
    const named = [_][]const u8{
        "capability_degraded",    "internal",          "invalid_cursor",
        "invalid_submission",     "model_not_found",   "no_run_to_resume",
        "open_failed",            "probe_failed",      "replay_cursor_future",
        "request_cancelled",      "request_too_large", "resolution_rejected",
        "run_active",             "run_not_found",     "run_terminal",
        "scope_mismatch",         "session_closed",    "session_exists",
        "stale_capabilities",     "state_failed",      "tools_failed",
        "unknown_adapter",        "unknown_session",   "unsupported_feature",
        "unsupported_media_type", "request_read",      "unrecognized_host",
        "cross_origin_request",   "history_failed",
    };
    for (named) |code| {
        try testing.expect(statusForRefusal(code) != null);
    }
    try testing.expectEqual(@as(usize, 29), refusal_statuses.len);
}

test "an unnamed refusal code carries no status, so the wire rule can refuse it" {
    try testing.expect(statusForRefusal("method_not_allowed") == null);
    try testing.expect(statusForRefusal("invented_later") == null);
    try testing.expect(statusForRefusal("405") == null);
}
test "every code the transport can answer carries the status the draft pins" {
    const named = [_]struct { code: []const u8, status: []const u8 }{
        .{ .code = "unknown_adapter", .status = "404 Not Found" },
        .{ .code = "unknown_session", .status = "404 Not Found" },
        .{ .code = "session_closed", .status = "409 Conflict" },
        .{ .code = "session_exists", .status = "409 Conflict" },
        .{ .code = "stale_capabilities", .status = "409 Conflict" },
        .{ .code = "run_active", .status = "409 Conflict" },
        .{ .code = "unsupported_feature", .status = "400 Bad Request" },
        .{ .code = "capability_degraded", .status = "400 Bad Request" },
        .{ .code = "scope_mismatch", .status = "400 Bad Request" },
        .{ .code = "request_cancelled", .status = "400 Bad Request" },
        .{ .code = "model_not_found", .status = "400 Bad Request" },
        .{ .code = "request_too_large", .status = "413 Payload Too Large" },
        .{ .code = "state_failed", .status = "500 Internal Server Error" },
        .{ .code = "internal", .status = "500 Internal Server Error" },
        .{ .code = "probe_failed", .status = "500 Internal Server Error" },
        .{ .code = "tools_failed", .status = "502 Bad Gateway" },
        .{ .code = "open_failed", .status = "502 Bad Gateway" },
        .{ .code = "run_not_found", .status = "404 Not Found" },
        .{ .code = "invalid_submission", .status = "400 Bad Request" },
        .{ .code = "invalid_cursor", .status = "400 Bad Request" },
        .{ .code = "replay_cursor_future", .status = "400 Bad Request" },
        .{ .code = "resolution_rejected", .status = "409 Conflict" },
        .{ .code = "run_terminal", .status = "409 Conflict" },
        .{ .code = "no_run_to_resume", .status = "409 Conflict" },
        .{ .code = "unsupported_media_type", .status = "415 Unsupported Media Type" },
        .{ .code = "request_read", .status = "400 Bad Request" },
        .{ .code = "unrecognized_host", .status = "403 Forbidden" },
        .{ .code = "cross_origin_request", .status = "403 Forbidden" },
        .{ .code = "history_failed", .status = "500 Internal Server Error" },
    };
    for (named) |entry| {
        try testing.expectEqualStrings(entry.status, statusForRefusal(entry.code).?);
    }
    try testing.expectEqual(@as(usize, 29), refusal_statuses.len);
    try testing.expectEqual(@as(usize, 29), named.len);
}

const many_detail_keys = [_][]const u8{
    "feature", "source",   "revision",          "session_id", "run_id",
    "cursor",  "expected", "current",           "attachment", "subscription",
    "limit",   "oldest",   "newest",            "resolution", "interaction",
    "reason",  "detail",   "expected_revision", "capability", "mode",
};

fn manyDetailEntries(buf: []u8) [many_detail_keys.len]oap_types.DetailEntry {
    var entries: [many_detail_keys.len]oap_types.DetailEntry = undefined;
    var at: usize = 0;
    for (many_detail_keys, 0..) |key, index| {
        const slot = buf[at..][0..key.len];
        @memcpy(slot, key);
        entries[index] = .{ .key = key, .value = slot };
        at += key.len;
    }
    return entries;
}

fn detailsUnderFailure(arena: std.mem.Allocator) !void {
    var backing: [256]u8 = undefined;
    const entries = manyDetailEntries(&backing);
    var rendered = try detailsJson(arena, &entries);
    defer rendered.object.deinit(arena);
    try std.testing.expectEqual(@as(usize, many_detail_keys.len), rendered.object.count());
}

test "a refusal detail that cannot be put releases the map it had already grown" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, detailsUnderFailure, .{});
}

test "a refusal detail borrows its key and value rather than copying them" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    var kept: [2]oap_types.DetailEntry = .{ .{ .key = "", .value = "" }, .{ .key = "", .value = "" } };
    const arena = arena_state.allocator();
    const feature = "action.tls_attach";
    const source = "x1";
    const feature_key = "feature";
    const source_key = "source";
    const keys_at = try arena.alloc(u8, feature_key.len + source_key.len);
    @memcpy(keys_at[0..feature_key.len], feature_key);
    @memcpy(keys_at[feature_key.len..], source_key);
    const values_at = try arena.alloc(u8, feature.len + source.len);
    @memcpy(values_at[0..feature.len], feature);
    @memcpy(values_at[feature.len..], source);
    kept[0] = .{ .key = keys_at[0..feature_key.len], .value = values_at[0..feature.len] };
    kept[1] = .{ .key = keys_at[feature_key.len..], .value = values_at[feature.len..] };
    var rendered = try detailsJson(arena, &kept);
    defer rendered.object.deinit(arena);
    try testing.expectEqual(@as(usize, 2), rendered.object.count());
    for (kept) |entry| {
        const stored = rendered.object.getPtr(entry.key).?;
        try testing.expectEqualStrings(entry.value, stored.string);
        try testing.expect(stored.string.ptr == entry.value.ptr);
        try testing.expectEqual(entry.value.len, stored.string.len);
    }
    var borrowed_keys: usize = 0;
    for (rendered.object.keys()) |key| {
        for (kept) |entry| {
            if (key.ptr == entry.key.ptr and key.len == entry.key.len) {
                borrowed_keys += 1;
            }
        }
    }
    try testing.expectEqual(@as(usize, 2), borrowed_keys);
}

test "refusal details render as the object the envelope carries" {
    const arena = testing.allocator;
    var rendered = try detailsJson(arena, &.{
        .{ .key = "feature", .value = "action.tool_sources.attach" },
        .{ .key = "source", .value = "x1" },
    });
    defer rendered.object.deinit(arena);
    try testing.expectEqualStrings("action.tool_sources.attach", rendered.object.get("feature").?.string);
    try testing.expectEqualStrings("x1", rendered.object.get("source").?.string);

    var none = try detailsJson(arena, &.{});
    defer none.object.deinit(arena);
    try testing.expectEqual(@as(usize, 0), none.object.count());
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
    try testing.expect(std.mem.indexOf(u8, message, "already running 1 operations") != null);
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
    return .{ .ptr = @ptrCast(@constCast(&fading_holder)), .vtable = &.{ .probe = fadingProbe, .open = referenceOpen } };
}

fn fadingProbe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
    const state: *Fading = @ptrCast(@alignCast(ptr));
    _ = refusal;
    if (state.registered) return .{ .endpoint = fading_descriptor.endpoint, .capability_revision = "", .features = &.{} };
    state.registered = true;
    return fading_descriptor;
}

test "work.list shows a session serve no longer holds from its binding, live by default and closed on request, and work.status, work.read and work.stop answer it without reopening" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(testing.io, testing.allocator);
    defer testing.allocator.free(cwd);
    const path = try std.fs.path.join(testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "sessions.jsonl" });
    defer testing.allocator.free(path);
    var store = try hubmod.binding.Store.open(testing.allocator, path);
    defer store.deinit();
    try store.append(.{ .action = .opened, .time_ms = 10, .record = .{ .session_id = "left", .adapter = "reference", .directory = "/work/a" } });
    try store.append(.{ .action = .opened, .time_ms = 20, .record = .{ .session_id = "ended", .adapter = "reference", .directory = "/work/a" } });
    try store.append(.{ .action = .closed, .time_ms = 30, .record = .{ .session_id = "ended", .adapter = "reference", .directory = "/work/a" } });
    const harness = try Harness.init(testing.allocator, .{ .bindings = &store }, .{});
    defer harness.deinit();

    try harness.send("{\"id\":1,\"op\":\"work.list\"}");
    const live = (try harness.lastValue()).object.get("result").?.object.get("groups").?.array.items;
    try testing.expectEqual(@as(usize, 1), live.len);
    const only = live[0].object.get("work").?.array.items;
    try testing.expectEqual(@as(usize, 1), only.len);
    try testing.expectEqualStrings("left", only[0].object.get("ref").?.object.get("session_id").?.string);
    try testing.expectEqual(false, only[0].object.get("held").?.bool);
    try testing.expectEqualStrings("live", only[0].object.get("state").?.string);
    try testing.expectEqualStrings("/work/a", live[0].object.get("directory").?.string);

    try harness.send("{\"id\":2,\"op\":\"work.list\",\"request\":{\"include_closed\":true}}");
    const all = (try harness.lastValue()).object.get("result").?.object.get("groups").?.array.items[0].object.get("work").?.array.items;
    try testing.expectEqual(@as(usize, 2), all.len);
    try testing.expectEqualStrings("ended", all[0].object.get("ref").?.object.get("session_id").?.string);
    try testing.expectEqualStrings("closed", all[0].object.get("state").?.string);

    try harness.send("{\"id\":3,\"op\":\"work.status\",\"session_id\":\"ended\"}");
    try testing.expectEqualStrings("closed", (try harness.lastValue()).object.get("result").?.object.get("state").?.string);
    try harness.send("{\"id\":4,\"op\":\"work.read\",\"session_id\":\"left\"}");
    try testing.expectEqual(@as(usize, 0), (try harness.lastValue()).object.get("result").?.object.get("turns").?.array.items.len);
    try harness.send("{\"id\":5,\"op\":\"work.stop\",\"session_id\":\"left\"}");
    try testing.expectEqual(false, (try harness.lastValue()).object.get("result").?.object.get("held").?.bool);
    try testing.expectEqual(@as(usize, 0), harness.hub.sessionCount());
}

test "the history op answers the store's sessions and refuses as the route does" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(testing.io, testing.allocator);
    defer testing.allocator.free(cwd);
    const path = try std.fs.path.join(testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "sessions.jsonl" });
    defer testing.allocator.free(path);
    var store = try hubmod.binding.Store.open(testing.allocator, path);
    defer store.deinit();
    const harness = try Harness.init(testing.allocator, .{ .bindings = &store }, .{});
    defer harness.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    _ = try harness.hub.open(arena_state.allocator(), "reference", .{ .session_id = "listed" });

    try harness.send("{\"id\":1,\"op\":\"history\",\"limit\":1}");
    const result = (try harness.lastValue()).object.get("result").?.object;
    const listed = result.get("sessions").?.array.items;
    try testing.expectEqual(@as(usize, 1), listed.len);
    try testing.expectEqualStrings("listed", listed[0].object.get("session_id").?.string);
    try testing.expectEqualStrings("live", listed[0].object.get("state").?.string);
    try testing.expect(result.get("next_cursor") == null);

    try harness.send("{\"id\":2,\"op\":\"history\",\"cursor\":\"not-a-cursor\"}");
    try testing.expectEqualStrings("invalid_cursor", try harness.code());
    try harness.send("{\"id\":3,\"op\":\"history\",\"limit\":0}");
    try testing.expectEqualStrings("invalid_request", try harness.code());
    try harness.send("{\"id\":4,\"op\":\"history\",\"session_id\":\"listed\"}");
    try testing.expectEqualStrings("invalid_request", try harness.code());

    const bare = try Harness.init(testing.allocator, .{}, .{});
    defer bare.deinit();
    try bare.send("{\"id\":5,\"op\":\"history\"}");
    try testing.expectEqualStrings("unsupported_feature", try bare.code());
}
