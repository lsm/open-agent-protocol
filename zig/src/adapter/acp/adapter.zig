const std = @import("std");
const harness_pins = @import("harness_pins");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const process = @import("process");
const session = @import("session");
const rpc = @import("rpc");
const compat = @import("compat");
const json_encode = @import("json_encode");
const gomarshal = @import("gomarshal");

pub const endpoint_id = session.endpoint_id;
pub const capability_revision = harness_pins.acp_capability_revision;
pub const acp_protocol_version: i64 = 1;
pub const client_name = "open-agent-protocol";
pub const client_version = "0.1";

const features = [_]contract.Feature{
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "ACP initialize is normalized into the OAP adapter boundary" },
    .{ .key = "capabilities", .level = .emulated, .reason = "effective support is synthesized conservatively from stable ACP v1 and adapter policy" },
    .{ .key = "session.open", .level = .native },
    .{ .key = "session.state", .level = .emulated, .reason = "adapter-owned projection" },
    .{ .key = "session.message.submit", .level = .emulated, .reason = "admission is synthesized after the prompt request is written" },
    .{ .key = "session.message.delivery.auto", .level = .emulated, .reason = "auto is normalized to start" },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "run.cancel", .level = .degraded, .reason = "ACP cancellation is an unacknowledged session notification; prompt settlement is authoritative" },
    .{ .key = "run.resume", .level = .degraded, .reason = "canonical replay is bounded process memory only" },
    .{ .key = "run.reconciliation", .level = .emulated, .reason = "state is adapter-owned" },
    .{ .key = "run.replay", .level = .degraded, .reason = "bounded process-memory journal; gaps are explicit" },
    .{ .key = "action.tools", .level = .degraded, .reason = "observed ACP presentation tool calls only; no catalog" },
    .{ .key = contract.feature_tool_sources_attach, .level = .native, .reason = "session/new carries the MCP server array; stdio descriptors only at this pin", .modes = &.{"session_open"}, .limits_json = "{\"transports\":[\"process\"]}" },
    .{ .key = "action.tools.execute", .level = .degraded, .reason = "observed tool lifecycle is normalized" },
    .{ .key = "action.permissions", .level = .native, .reason = "ACP permission choice semantics with synthesized portable identity" },
};

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "ACP v1 Adapter", .version = harness_pins.acp_endpoint_version, .adapter = "acp-v1-stdio" },
    .capability_revision = capability_revision,
    .features = &features,
};

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: []const u8,
    frame_limit: usize = rpc.frame_limit_default,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    poll_ns: u64 = 5 * std.time.ns_per_ms,
};

fn wallClock() i64 {
    return compat.time.nowMillis();
}

fn monotonic() u64 {
    return compat.time.monotonicNanos() catch 0;
}

pub const Adapter = struct {
    allocator: std.mem.Allocator,
    config: Config,
    ids: usize = 0,

    pub fn init(allocator: std.mem.Allocator, config: Config) Adapter {
        return .{ .allocator = allocator, .config = config };
    }

    pub fn adapter(self: *Adapter) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        _ = ptr;
        _ = refusal;
        return descriptor;
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        const self: *Adapter = @ptrCast(@alignCast(ptr));
        const opened = try Session.open(self, arena, request, refusal);
        return opened.handle();
    }
};

const Attached = struct {
    servers: []const std.json.Value = &.{},
    sources: []const oap_types.ToolSourceDescriptor = &.{},
};

fn attach(owner: *Adapter, arena: std.mem.Allocator, sources_json: ?[]const u8, refusal: *contract.Refusal) contract.Failure!Attached {
    const text = sources_json orelse return .{};
    const document = std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{ .allocate = .alloc_always }) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return error.InvalidSubmission;
    };
    if (document != .array or document.array.items.len == 0) return .{};
    const items = document.array.items;
    const servers = try arena.alloc(std.json.Value, items.len);
    const sources = try arena.alloc(oap_types.ToolSourceDescriptor, items.len);
    for (items, servers, sources, 0..) |item, *server, *source, index| {
        const id = memberText(item, "id");
        if (id.len == 0) return refuseAttachment(refusal, "", "an attachment needs an id");
        for (items[0..index]) |earlier| {
            if (std.mem.eql(u8, memberText(earlier, "id"), id)) return refuseAttachment(refusal, id, "the id already names a configured or attached MCP server");
        }
        if (!std.mem.eql(u8, memberText(item, "kind"), "process")) return refuseAttachment(refusal, id, "ACP v1 accepts stdio MCP descriptors only at this pin");
        const environment = memberArray(item, "environment");
        for (environment, 0..) |entry, position| {
            for (environment[0..position]) |prior| {
                if (std.mem.eql(u8, envName(textOf(prior)), envName(textOf(entry)))) {
                    return refuseAttachment(refusal, id, try std.fmt.allocPrint(arena, "environment names {s} twice", .{envName(textOf(entry))}));
                }
            }
        }
        const command = memberText(item, "command");
        if (command.len == 0) {
            return refusal.fail(error.InvalidResolution, try std.fmt.allocPrint(arena, "adapter: invalid interaction resolution: tool source \"{s}\" declares a process source with no command", .{id}));
        }
        var native: std.json.ObjectMap = .empty;
        try native.put(arena, "name", .{ .string = id });
        try native.put(arena, "command", .{ .string = command });
        const args = memberArray(item, "args");
        if (args.len > 0) {
            var listed = std.json.Array.init(arena);
            for (args) |arg| try listed.append(.{ .string = textOf(arg) });
            try native.put(arena, "args", .{ .array = listed });
        }
        var env = std.json.Array.init(arena);
        for (environment) |entry| {
            const carried = textOf(entry);
            const name = envName(carried);
            const value = if (carried.len > name.len) carried[name.len + 1 ..] else allowlisted(owner.config.environment, name) orelse continue;
            var variable: std.json.ObjectMap = .empty;
            try variable.put(arena, "name", .{ .string = name });
            try variable.put(arena, "value", .{ .string = value });
            try env.append(.{ .object = variable });
        }
        if (env.items.len > 0) try native.put(arena, "env", .{ .array = env });
        server.* = .{ .object = native };
        source.* = .{ .id = id, .kind = "process", .display_name = optionalText(item, "display_name"), .protocol = optionalText(item, "protocol"), .endpoint = optionalText(item, "endpoint") };
    }
    return .{ .servers = servers, .sources = sources };
}

fn refuseAttachment(refusal: *contract.Refusal, source: []const u8, detail: []const u8) contract.Failure {
    refusal.* = .{ .feature = contract.feature_tool_sources_attach, .reason = contract.reason_unsatisfiable, .source = source, .detail = detail };
    return error.UnsupportedFeature;
}

fn ownedSource(allocator: std.mem.Allocator, source: oap_types.ToolSourceDescriptor) !oap_types.ToolSourceDescriptor {
    const id = try allocator.dupe(u8, source.id);
    const kind = try allocator.dupe(u8, source.kind);
    const display_name = if (source.display_name) |text| try allocator.dupe(u8, text) else null;
    const protocol_name = if (source.protocol) |text| try allocator.dupe(u8, text) else null;
    const endpoint = if (source.endpoint) |text| try allocator.dupe(u8, text) else null;
    return .{ .id = id, .kind = kind, .display_name = display_name, .protocol = protocol_name, .endpoint = endpoint };
}

fn allowlisted(environment: []const []const u8, name: []const u8) ?[]const u8 {
    var found: ?[]const u8 = null;
    for (environment) |entry| {
        const cut = std.mem.indexOfScalar(u8, entry, '=') orelse continue;
        if (std.mem.eql(u8, entry[0..cut], name)) found = entry[cut + 1 ..];
    }
    return found;
}

test "an allowlisted name listed twice resolves to its last value, as goap's map does" {
    try testing.expectEqualStrings("2", allowlisted(&.{ "K=1", "OTHER=x", "K=2" }, "K").?);
    try testing.expect(allowlisted(&.{"K"}, "K") == null);
}

fn envName(entry: []const u8) []const u8 {
    const cut = std.mem.indexOfScalar(u8, entry, '=') orelse return entry;
    return entry[0..cut];
}

fn textOf(value: std.json.Value) []const u8 {
    return if (value == .string) value.string else "";
}

fn memberText(value: std.json.Value, key: []const u8) []const u8 {
    if (value != .object) return "";
    return textOf(value.object.get(key) orelse return "");
}

fn optionalText(value: std.json.Value, key: []const u8) ?[]const u8 {
    const text = memberText(value, key);
    return if (text.len > 0) text else null;
}

fn memberArray(value: std.json.Value, key: []const u8) []const std.json.Value {
    if (value != .object) return &.{};
    const carried = value.object.get(key) orelse return &.{};
    return if (carried == .array) carried.array.items else &.{};
}

const Ask = struct {
    native_id: std.json.Value,
    interaction_id: []const u8,
};

const Answer = struct {
    result: ?std.json.Value = null,
    code: i64 = 0,
    message: []const u8 = "",
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    participant: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: session.Reducer = undefined,
    native_id: []const u8 = "",
    next_request: i64 = 1,
    awaited: ?i64 = null,
    answer: ?Answer = null,
    prompt_id: ?i64 = null,
    asks: std.ArrayList(Ask) = .empty,
    statuses: std.StringHashMapUnmanaged([]const u8) = .empty,
    scanned: usize = 0,
    ended: bool = false,
    reaped: bool = false,
    sources: []oap_types.ToolSourceDescriptor = &.{},

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const attached = try attach(owner, arena, request.tool_sources_json, refusal);
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        self.sources = try self.owned().alloc(oap_types.ToolSourceDescriptor, attached.sources.len);
        for (attached.sources, self.sources) |source, *slot| slot.* = try ownedSource(self.owned(), source);
        const initialized = try self.call(arena, "initialize", try self.initializeParams(), refusal);
        const version = memberOf(initialized, "protocolVersion");
        const capabilities = memberOf(initialized, "agentCapabilities");
        const versioned = if (version) |value| value == .integer and value.integer == acp_protocol_version else false;
        const capable = if (capabilities) |value| value != .null else false;
        if (!versioned or !capable) return refusal.fail(error.BackendFailed, "the ACP agent answered initialize with another protocol version or no agentCapabilities");
        const created = try self.call(arena, "session/new", try self.sessionNewParams(attached.servers), refusal);
        const native_session = memberOf(created, "sessionId") orelse std.json.Value.null;
        if (native_session != .string or native_session.string.len == 0) return refusal.fail(error.BackendFailed, "the ACP agent answered session/new with no session id");
        self.native_id = native_session.string;
        self.reducer = session.Reducer.init(self.reducer_arena, .{
            .session_id = self.id,
            .native_id = self.native_id,
            .responder = self.participant,
            .revision = capability_revision,
            .counter = &owner.ids,
            .now_ms = wallClock,
            .id_style = .decimal,
        });
        self.reducer.open();
        return self;
    }

    fn construct(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const config = owner.config;
        const self = try gpa.create(Session);
        errdefer gpa.destroy(self);
        const id = if (request.session_id.len > 0) try gpa.dupe(u8, request.session_id) else try mint(owner, gpa, "session");
        errdefer gpa.free(id);
        const participant = try gpa.dupe(u8, request.participant);
        errdefer gpa.free(participant);
        const reducer_arena = try gpa.create(std.heap.ArenaAllocator);
        errdefer gpa.destroy(reducer_arena);
        reducer_arena.* = std.heap.ArenaAllocator.init(gpa);
        errdefer reducer_arena.deinit();
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = config.args,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the ACP agent could not start: {s}", .{@errorName(err)});
            return refusal.fail(error.BackendFailed, message);
        };
        self.* = .{
            .owner = owner,
            .gpa = gpa,
            .id = id,
            .participant = participant,
            .reducer_arena = reducer_arena,
            .transport = transport,
        };
        return self;
    }

    fn mint(owner: *Adapter, allocator: std.mem.Allocator, kind: []const u8) ![]u8 {
        owner.ids += 1;
        return std.fmt.allocPrint(allocator, "{s}-{d}", .{ kind, owner.ids });
    }

    fn handle(self: *Session) contract.Session {
        return .{ .ptr = self, .vtable = &vtable };
    }

    const vtable = contract.Session.VTable{
        .id = idOf,
        .state = state,
        .submit = submit,
        .resolve = resolve,
        .cancel = cancel,
        .pump = pump,
        .drain = drain,
        .activity = activity,
        .close = close,
    };

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn owned(self: *Session) std.mem.Allocator {
        return self.reducer_arena.allocator();
    }

    fn opened(self: *Session) bool {
        return self.native_id.len > 0;
    }

    fn destroy(self: *Session) void {
        const gpa = self.gpa;
        self.reap();
        self.transport.deinit();
        self.reducer_arena.deinit();
        gpa.destroy(self.reducer_arena);
        gpa.free(self.id);
        gpa.free(self.participant);
        gpa.destroy(self);
    }

    fn reap(self: *Session) void {
        if (self.reaped) return;
        self.reaped = true;
        self.transport.close();
    }

    fn fail(self: *Session, detail: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        self.reap();
        if (self.opened()) self.reducer.transportFailed(detail) catch |err| return lift(err);
    }

    fn object(self: *Session) std.json.ObjectMap {
        _ = self;
        return .empty;
    }

    fn put(self: *Session, map: *std.json.ObjectMap, key: []const u8, value: std.json.Value) !void {
        try map.put(self.owned(), key, value);
    }

    fn initializeParams(self: *Session) !std.json.Value {
        var info = self.object();
        try self.put(&info, "name", .{ .string = client_name });
        try self.put(&info, "version", .{ .string = client_version });
        var params = self.object();
        try self.put(&params, "protocolVersion", .{ .integer = acp_protocol_version });
        try self.put(&params, "clientCapabilities", .{ .object = self.object() });
        try self.put(&params, "clientInfo", .{ .object = info });
        return .{ .object = params };
    }

    fn sessionNewParams(self: *Session, attached: []const std.json.Value) !std.json.Value {
        var params = self.object();
        try self.put(&params, "cwd", .{ .string = self.owner.config.working_directory });
        var servers = std.json.Array.init(self.owned());
        try servers.appendSlice(attached);
        try self.put(&params, "mcpServers", .{ .array = servers });
        return .{ .object = params };
    }

    fn encode(self: *Session, frame: std.json.ObjectMap) ![]const u8 {
        return gomarshal.marshal(self.owned(), .{ .object = frame }) catch |err| switch (err) {
            error.OutOfMemory => error.OutOfMemory,
            error.UnsupportedValue => error.InvalidSubmission,
        };
    }

    fn requestFrame(self: *Session, id: i64, method: []const u8, params: std.json.Value) ![]const u8 {
        var frame = self.object();
        try self.put(&frame, "id", .{ .integer = id });
        try self.put(&frame, "jsonrpc", .{ .string = "2.0" });
        try self.put(&frame, "method", .{ .string = method });
        try self.put(&frame, "params", params);
        return self.encode(frame);
    }

    fn notificationFrame(self: *Session, method: []const u8, params: std.json.Value) ![]const u8 {
        var frame = self.object();
        try self.put(&frame, "jsonrpc", .{ .string = "2.0" });
        try self.put(&frame, "method", .{ .string = method });
        try self.put(&frame, "params", params);
        return self.encode(frame);
    }

    fn resultFrame(self: *Session, id: std.json.Value, result: std.json.Value) ![]const u8 {
        var frame = self.object();
        try self.put(&frame, "id", id);
        try self.put(&frame, "jsonrpc", .{ .string = "2.0" });
        try self.put(&frame, "result", result);
        return self.encode(frame);
    }

    fn errorFrame(self: *Session, id: std.json.Value, code: i64, message: []const u8) ![]const u8 {
        var failure = self.object();
        try self.put(&failure, "code", .{ .integer = code });
        try self.put(&failure, "message", .{ .string = message });
        var frame = self.object();
        try self.put(&frame, "error", .{ .object = failure });
        try self.put(&frame, "id", id);
        try self.put(&frame, "jsonrpc", .{ .string = "2.0" });
        return self.encode(frame);
    }

    fn permissionOutcome(self: *Session, outcome: []const u8, option_id: []const u8) !std.json.Value {
        var chosen = self.object();
        try self.put(&chosen, "outcome", .{ .string = outcome });
        if (option_id.len > 0) try self.put(&chosen, "optionId", .{ .string = option_id });
        var result = self.object();
        try self.put(&result, "outcome", .{ .object = chosen });
        return .{ .object = result };
    }

    fn send(self: *Session, frame: []const u8) contract.Failure!bool {
        if (self.ended) return false;
        self.transport.write(frame) catch |err| {
            try self.fail(@errorName(err));
            return false;
        };
        return true;
    }

    fn call(self: *Session, arena: std.mem.Allocator, method: []const u8, params: std.json.Value, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        const id = self.next_request;
        self.next_request += 1;
        const frame = self.requestFrame(id, method, params) catch |err| return lift(err);
        self.awaited = id;
        self.answer = null;
        defer self.awaited = null;
        if (!try self.send(frame)) {
            const message = try std.fmt.allocPrint(arena, "the ACP agent exited before answering {s}", .{method});
            return refusal.fail(error.BackendFailed, message);
        }
        const started = monotonic();
        while (self.answer == null) {
            if (self.ended) {
                const message = try std.fmt.allocPrint(arena, "the ACP agent exited before answering {s}", .{method});
                return refusal.fail(error.BackendFailed, message);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the ACP agent stopped answering");
                const message = try std.fmt.allocPrint(arena, "the ACP agent did not answer {s} within {d} ms", .{ method, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, message);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const answered = self.answer.?;
        self.answer = null;
        if (answered.result) |result| return result;
        const message = try std.fmt.allocPrint(arena, "acp rpc error {d} for {s}: {s}", .{ answered.code, method, answered.message });
        return refusal.fail(error.BackendFailed, message);
    }

    fn receive(self: *Session, wait_ns: u64) contract.Failure!?std.json.Value {
        if (self.ended) return null;
        const polled = self.transport.poll(wait_ns) catch |err| {
            try self.fail(@errorName(err));
            return null;
        };
        switch (polled) {
            .quiet => return null,
            .ended => {
                self.reap();
                try self.fail(self.transport.departed().text(self.owned()));
                return null;
            },
            .frame => |bytes| {
                const held = try self.owned().dupe(u8, bytes);
                _ = rpc.parseMessage(self.owned(), held) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    const detail = try std.fmt.allocPrint(self.owned(), "acp rpc: {s}", .{@errorName(err)});
                    try self.fail(detail);
                    return null;
                };
                return std.json.parseFromSliceLeaky(std.json.Value, self.owned(), held, .{}) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    try self.fail("acp rpc: undecodable frame");
                    return null;
                };
            },
        }
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const was_ended = self.ended;
        const parsed = try self.receive(wait_ns) orelse {
            if (self.ended != was_ended) try self.settleOrphans();
            return self.ended != was_ended;
        };
        try self.dispatch(parsed);
        try self.settleOrphans();
        return true;
    }

    fn dispatch(self: *Session, parsed: std.json.Value) contract.Failure!void {
        const frame = parsed.object;
        const method_value = frame.get("method");
        const id_value = frame.get("id");
        if (method_value) |method| {
            const name = if (method == .string) method.string else "";
            const kind: rpc.Kind = if (id_value != null) .request else .notification;
            if (!self.opened()) return;
            if (kind == .request) return self.handleRequest(name, id_value.?, parsed);
            self.reducer.observe(.{ .kind = .notification, .method = name, .raw = "" }, parsed) catch |err| return lift(err);
            return;
        }
        const id = if (id_value) |value| (if (value == .integer) value.integer else null) else null;
        const answered: Answer = if (frame.get("error")) |failure| .{
            .code = if (memberOf(failure, "code")) |code| (if (code == .integer) code.integer else 0) else 0,
            .message = if (memberOf(failure, "message")) |text| (if (text == .string) text.string else "") else "",
        } else .{ .result = frame.get("result") orelse std.json.Value.null };
        if (id != null and self.awaited != null and id.? == self.awaited.?) {
            self.answer = answered;
            return;
        }
        if (id != null and self.prompt_id != null and id.? == self.prompt_id.?) {
            self.prompt_id = null;
            if (answered.result) |result| {
                const stop = memberOf(result, "stopReason") orelse std.json.Value.null;
                self.reducer.settlePrompt(if (stop == .string) stop.string else "") catch |err| return lift(err);
            } else {
                self.reducer.promptFailed(answered.code, id_value.?, answered.message) catch |err| return lift(err);
            }
            return;
        }
        try self.fail("acp rpc: response id is not pending");
    }

    fn handleRequest(self: *Session, method: []const u8, native_id: std.json.Value, parsed: std.json.Value) contract.Failure!void {
        if (!std.mem.eql(u8, method, "session/request_permission")) {
            _ = try self.send(self.errorFrame(native_id, -32601, "method not supported") catch |err| return lift(err));
            return;
        }
        const had_run = self.runLive();
        const gates_before = self.reducer.gates.items.len;
        self.reducer.observe(.{ .kind = .request, .method = method, .raw = "" }, parsed) catch |err| return lift(err);
        if (self.reducer.gates.items.len > gates_before) {
            const gate = self.reducer.gates.items[self.reducer.gates.items.len - 1];
            try self.asks.append(self.owned(), .{ .native_id = native_id, .interaction_id = gate.id });
            return;
        }
        if (!had_run) {
            _ = try self.send(self.resultFrame(native_id, self.permissionOutcome("cancelled", "") catch |err| return lift(err)) catch |err| return lift(err));
            return;
        }
        const refused = if (self.runLive()) "invalid tool call" else "invalid permission request";
        _ = try self.send(self.errorFrame(native_id, -32602, refused) catch |err| return lift(err));
    }

    fn runLive(self: *Session) bool {
        const run = self.reducer.run orelse return false;
        return !run.terminal;
    }

    fn settleOrphans(self: *Session) contract.Failure!void {
        try self.recordTerminals();
        if (!self.opened()) return;
        const run = self.reducer.run orelse return;
        if (!run.terminal) return;
        for (self.asks.items) |ask| {
            _ = try self.send(self.resultFrame(ask.native_id, self.permissionOutcome("cancelled", "") catch |err| return lift(err)) catch |err| return lift(err));
        }
        self.asks.clearRetainingCapacity();
    }

    fn recordTerminals(self: *Session) contract.Failure!void {
        if (!self.opened()) return;
        const emitted = self.reducer.envelopes.items;
        while (self.scanned < emitted.len) : (self.scanned += 1) {
            const envelope = emitted[self.scanned].object;
            const kind = envelope.get("type").?.string;
            const status = terminalStatus(kind) orelse continue;
            try self.statuses.put(self.owned(), envelope.get("run_id").?.string, status);
        }
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = refusal;
        const self = cast(ptr);
        if (self.ended) return error.SessionClosed;
        const live = if (self.reducer.run) |run| !run.terminal else false;
        const active_run_id: ?[]const u8 = if (live) try arena.dupe(u8, self.reducer.run.?.id) else null;
        const sources = try arena.dupe(oap_types.ToolSourceDescriptor, self.sources);
        const transcript_cursor: ?[]const u8 = if (self.reducer.last_sequence > 0) try std.fmt.allocPrint(arena, "{d}", .{self.reducer.last_sequence}) else null;
        return .{
            .session_id = self.id,
            .status = if (live) .running else .idle,
            .active_run_id = active_run_id,
            .updated_at_ms = wallClock(),
            .sources = sources,
            .transcript_cursor = transcript_cursor,
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        if (request.session_id.len == 0 or request.messages.len == 0 or request.delivery != .auto) return error.InvalidSubmission;
        var prompt = std.json.Array.init(self.owned());
        const given = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, given) |message, *slot| {
            if (message.role != .user) return error.InvalidSubmission;
            const text = switch (message.content) {
                .text => |content| try self.owned().dupe(u8, content),
                .parts => return error.InvalidSubmission,
            };
            var block = self.object();
            try self.put(&block, "type", .{ .string = "text" });
            try self.put(&block, "text", .{ .string = text });
            try prompt.append(.{ .object = block });
            slot.* = try self.owned().dupe(u8, message.id orelse "");
        }
        if (self.ended) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.reducer.run) |run| {
            if (!run.terminal) return error.RunActive;
        }
        var params = self.object();
        try self.put(&params, "sessionId", .{ .string = self.native_id });
        try self.put(&params, "prompt", .{ .array = prompt });
        const id = self.next_request;
        self.next_request += 1;
        const frame = self.requestFrame(id, "session/prompt", .{ .object = params }) catch |err| return lift(err);
        if (!try self.send(frame)) return refusal.fail(error.BackendFailed, "the ACP agent did not take the prompt");
        self.prompt_id = id;
        self.reducer.submitAs(request.messages.len, .{ .message_ids = given }) catch |err| return lift(err);
        const run = self.reducer.run.?;
        const admission = self.reducer.admission;
        const message_ids = try arena.alloc([]const u8, admission.message_ids.len);
        for (admission.message_ids, message_ids) |source, *slot| slot.* = try arena.dupe(u8, source);
        const submission_id = try arena.dupe(u8, admission.submission_id);
        const run_id = try arena.dupe(u8, run.id);
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = .auto,
            .effective_delivery = .start,
            .delivery_resolution = "session_idle",
            .admission = .started,
            .run_id = run_id,
            .status = .running,
            .message_ids = message_ids,
        };
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        const self = cast(ptr);
        const request = switch (resolution) {
            .permission => |permission| permission,
            .input => return error.InteractionNotFound,
        };
        if (self.ended) return error.SessionClosed;
        const at = self.askFor(request.interaction_id) orelse return error.InteractionNotFound;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
        if (request.requested_by.len > 0 and !std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
        const choice = request.choice_id orelse return error.InvalidResolution;
        const choice_id = try self.owned().dupe(u8, choice);
        const interaction_id = try self.owned().dupe(u8, request.interaction_id);
        const run_id = try self.owned().dupe(u8, request.run_id);
        const responder = try self.owned().dupe(u8, request.responded_by);
        const admitted = self.reducer.admitResolution(interaction_id, run_id, responder, choice_id, request.granted) catch |err| return switch (err) {
            error.InteractionNotFound, error.InteractionResolved => error.InteractionNotFound,
            error.WrongResponder, error.InvalidResolution => error.InvalidResolution,
            else => lift(err),
        };
        const ask = self.asks.orderedRemove(at);
        const outcome = self.permissionOutcome("selected", choice_id) catch |err| return lift(err);
        const frame = self.resultFrame(ask.native_id, outcome) catch |err| return lift(err);
        const gate = admitted orelse {
            _ = try self.send(frame);
            return self.recordTerminals();
        };
        self.transport.write(frame) catch |err| {
            const detail = @errorName(err);
            self.reducer.answerFailed(detail) catch |failure| return lift(failure);
            try self.fail(detail);
            try self.recordTerminals();
            const message = try std.fmt.allocPrint(arena, "the ACP agent did not take the permission answer: {s}", .{detail});
            return refusal.fail(error.BackendFailed, message);
        };
        self.reducer.commitResolution(gate, choice_id) catch |err| return lift(err);
        try self.recordTerminals();
    }

    fn askFor(self: *Session, interaction_id: []const u8) ?usize {
        for (self.asks.items, 0..) |ask, index| {
            if (std.mem.eql(u8, ask.interaction_id, interaction_id)) return index;
        }
        return null;
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        const self = cast(ptr);
        if (self.ended) return error.SessionClosed;
        try self.recordTerminals();
        if (self.statuses.get(run_id)) |status| {
            if (!std.mem.eql(u8, status, "cancelled")) return error.RunTerminal;
            return .{ .session_id = self.id, .run_id = try arena.dupe(u8, run_id), .accepted = true, .status = .cancelled };
        }
        const run = self.reducer.run orelse return error.RunNotFound;
        if (!std.mem.eql(u8, run.id, run_id)) return error.RunNotFound;
        if (!run.cancel_requested) {
            var params = self.object();
            try self.put(&params, "sessionId", .{ .string = self.native_id });
            const frame = self.notificationFrame("session/cancel", .{ .object = params }) catch |err| return lift(err);
            if (!try self.send(frame)) return refusal.fail(error.BackendFailed, "the ACP agent did not take session/cancel");
            self.reducer.cancel() catch |err| return lift(err);
        }
        return .{ .session_id = self.id, .run_id = try arena.dupe(u8, run_id), .accepted = true, .status = .cancelling };
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        const self = cast(ptr);
        var progressed = false;
        var wait = wait_ns;
        var frames: usize = 0;
        while (frames < 256) : (frames += 1) {
            if (!try self.step(wait)) break;
            progressed = true;
            wait = std.time.ns_per_ms;
        }
        return progressed;
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        try self.recordTerminals();
        try appendEvents(allocator, self.reducer.envelopes.items, out);
        self.reducer.envelopes.clearRetainingCapacity();
        self.scanned = 0;
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        const run = self.reducer.run orelse return .idle;
        if (run.terminal) return .idle;
        if (self.asks.items.len > 0) return .waiting;
        return .running;
    }

    fn close(ptr: *anyopaque) void {
        cast(ptr).destroy();
    }
};

fn memberOf(value: std.json.Value, key: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(key);
}

fn terminalStatus(kind: []const u8) ?[]const u8 {
    if (std.mem.eql(u8, kind, "run.completed")) return "completed";
    if (std.mem.eql(u8, kind, "run.failed")) return "failed";
    if (std.mem.eql(u8, kind, "run.cancelled")) return "cancelled";
    return null;
}

fn appendEvents(allocator: std.mem.Allocator, emitted: []const std.json.Value, out: *std.ArrayList(contract.Event)) contract.Failure!void {
    try out.ensureUnusedCapacity(allocator, emitted.len);
    const first = out.items.len;
    errdefer {
        for (out.items[first..]) |event| {
            allocator.free(event.line);
            allocator.free(event.run_id);
        }
        out.shrinkRetainingCapacity(first);
    }
    for (emitted) |value| {
        const line = try json_encode.valueAlloc(allocator, value);
        errdefer allocator.free(line);
        const run_id = try allocator.dupe(u8, value.object.get("run_id").?.string);
        const sequence: u64 = @intCast(value.object.get("sequence").?.integer);
        out.appendAssumeCapacity(.{ .line = line, .run_id = run_id, .sequence = sequence });
    }
}

fn lift(err: anyerror) contract.Failure {
    if (err == error.OutOfMemory) return error.OutOfMemory;
    return error.BackendFailed;
}

const testing = std.testing;

pub const FakeAgent = struct {
    tmp: std.testing.TmpDir,
    path: []u8,
    cwd: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakeAgent {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "agent", .data = script, .flags = .{ .permissions = .executable_file } });
        const current = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(current);
        const cwd = try allocator.dupe(u8, current);
        errdefer allocator.free(cwd);
        const path = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "agent" });
        errdefer allocator.free(path);
        return .{ .tmp = tmp, .path = path, .cwd = cwd };
    }

    pub fn deinit(self: *FakeAgent, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        allocator.free(self.cwd);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakeAgent, allocator: std.mem.Allocator) ![]u8 {
        const file = try self.tmp.dir.openFile(testing.io, "stdin.log", .{});
        defer file.close(testing.io);
        var out = std.ArrayList(u8).empty;
        errdefer out.deinit(allocator);
        var buffer: [4096]u8 = undefined;
        while (true) {
            const count = file.readStreaming(testing.io, &.{&buffer}) catch |err| switch (err) {
                error.EndOfStream => break,
                else => |failure| return failure,
            };
            if (count == 0) break;
            try out.appendSlice(allocator, buffer[0..count]);
        }
        return out.toOwnedSlice(allocator);
    }

    pub fn config(self: *const FakeAgent) Config {
        return .{
            .executable = self.path,
            .environment = &.{"PATH=/usr/bin:/bin"},
            .working_directory = self.cwd,
            .exit_grace_ns = 2 * std.time.ns_per_s,
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\take; printf '{"id":1,"jsonrpc":"2.0","result":{"agentCapabilities":{},"protocolVersion":1}}\n'
    \\take; printf '{"id":2,"jsonrpc":"2.0","result":{"sessionId":"native-session"}}\n'
    \\
;

pub const fake_text_turn =
    \\take
    \\printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"content":{"text":"fixture-ok","type":"text"},"messageId":"native-message","sessionUpdate":"agent_message_chunk"}}}\n'
    \\printf '{"id":3,"jsonrpc":"2.0","result":{"stopReason":"end_turn"}}\n'
    \\
;

pub const fake_permission_turn =
    \\take
    \\printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}\n'
    \\printf '{"id":"permission-1","jsonrpc":"2.0","method":"session/request_permission","params":{"options":[{"kind":"allow_once","name":"Allow once","optionId":"allow"},{"kind":"reject_once","name":"Reject","optionId":"deny"}],"sessionId":"native-session","toolCall":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}\n'
    \\take
    \\printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"rawOutput":{"ok":true},"sessionUpdate":"tool_call_update","status":"completed","toolCallId":"native-tool"}}}\n'
    \\printf '{"id":3,"jsonrpc":"2.0","result":{"stopReason":"end_turn"}}\n'
    \\
;

pub const fake_cancelled_turn =
    \\take
    \\take
    \\printf '{"id":3,"jsonrpc":"2.0","result":{"stopReason":"cancelled"}}\n'
    \\
;

pub const fake_idle = "while take; do :; done\n";

const Probe = struct {
    fake: FakeAgent,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakeAgent.init(testing.allocator, script);
        self.adapter = Adapter.init(testing.allocator, self.fake.config());
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.handle = null;
    }

    fn deinit(self: *Probe) void {
        if (self.handle) |live| live.close();
        self.arena.deinit();
        self.fake.deinit(testing.allocator);
    }

    fn open(self: *Probe, refusal: *contract.Refusal) !contract.Session {
        return self.openAttaching(null, refusal);
    }

    fn openAttaching(self: *Probe, sources_json: ?[]const u8, refusal: *contract.Refusal) !contract.Session {
        const live = try self.adapter.adapter().vtable.open(&self.adapter, self.arena.allocator(), .{ .session_id = "s1", .participant = "user", .tool_sources_json = sources_json }, refusal);
        self.handle = live;
        return live;
    }

    fn submit(self: *Probe, text: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
        return self.handle.?.submit(self.arena.allocator(), &request, refusal);
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
            var drained = std.ArrayList(contract.Event).empty;
            try self.handle.?.drain(self.arena.allocator(), &drained);
            try seen.appendSlice(self.arena.allocator(), drained.items);
            for (drained.items) |event| {
                if (std.mem.indexOf(u8, event.line, "\"type\":\"" ++ kind ++ "\"") != null) return event;
            }
        }
        return error.EventNeverArrived;
    }

    fn waitWritten(self: *Probe, needle: []const u8) ![]u8 {
        var rounds: usize = 0;
        while (rounds < 400) : (rounds += 1) {
            const written = try self.fake.written(self.arena.allocator());
            if (std.mem.indexOf(u8, written, needle) != null) return written;
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
        }
        return error.FrameNeverWritten;
    }

    fn payloadOf(self: *Probe, event: contract.Event) !std.json.ObjectMap {
        const parsed = try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), event.line, .{});
        return parsed.object.get("payload").?.object;
    }
};

test "an open writes initialize and session/new in Go's form, with the agent's working directory" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const scratch = probe.arena.allocator();
    const written = try probe.fake.written(scratch);
    const want = try std.fmt.allocPrint(scratch,
        \\{{"id":1,"jsonrpc":"2.0","method":"initialize","params":{{"protocolVersion":1,"clientCapabilities":{{}},"clientInfo":{{"name":"open-agent-protocol","version":"0.1"}}}}}}
        \\{{"id":2,"jsonrpc":"2.0","method":"session/new","params":{{"cwd":"{s}","mcpServers":[]}}}}
        \\
    , .{probe.fake.cwd});
    try testing.expectEqualStrings(want, written);
}

test "a turn is admitted when its prompt is written and settles on the prompt's stop reason" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    try testing.expect(admitted.accepted);
    try testing.expectEqual(@as(usize, 1), admitted.message_ids.len);
    _ = try probe.waitWritten("{\"id\":3,\"jsonrpc\":\"2.0\",\"method\":\"session/prompt\",\"params\":{\"sessionId\":\"native-session\",\"prompt\":[{\"type\":\"text\",\"text\":\"hello\"}]}}");

    var seen = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &seen);
    try testing.expectEqualStrings("fixture-ok", (try probe.payloadOf(completed)).get("final_response").?.object.get("content").?.string);
    for (seen.items, 1..) |event, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, event.run_id);
        try testing.expectEqual(@as(u64, sequence), event.sequence);
        try testing.expect(std.mem.indexOf(u8, event.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectError(error.RunTerminal, probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal));
}

test "a permission request raises an interaction and the chosen option reaches the agent under its own request id" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_permission_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("read it", &refusal);

    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("action.permission.requested", &seen);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
    const payload = try probe.payloadOf(asked);
    const request = oap_types.PermissionResolveRequest{
        .interaction_id = payload.get("interaction_id").?.string,
        .requested_by = payload.get("requested_by").?.string,
        .responded_by = payload.get("responded_by").?.string,
        .session_id = payload.get("session_id").?.string,
        .run_id = payload.get("run_id").?.string,
        .granted = true,
        .choice_id = "allow",
    };
    try probe.handle.?.resolve(probe.arena.allocator(), .{ .permission = &request }, &refusal);
    _ = try probe.waitWritten("{\"id\":\"permission-1\",\"jsonrpc\":\"2.0\",\"result\":{\"outcome\":{\"outcome\":\"selected\",\"optionId\":\"allow\"}}}");
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expectError(error.InteractionNotFound, probe.handle.?.resolve(probe.arena.allocator(), .{ .permission = &request }, &refusal));
}

test "a cancel sends session/cancel once, answers cancelling, and the cancelled stop reason settles the run" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_cancelled_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    const first = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, first.status);
    const again = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, again.status);
    const cancel_frame = "{\"jsonrpc\":\"2.0\",\"method\":\"session/cancel\",\"params\":{\"sessionId\":\"native-session\"}}";
    const written = try probe.waitWritten(cancel_frame);
    try testing.expectEqual(@as(usize, 1), std.mem.count(u8, written, cancel_frame));
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.cancelled", &seen);
    const settled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelled, settled.status);
    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), "run-unknown", &refusal));
}

test "an agent that answers initialize with another protocol version refuses the open" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\IFS= read -r line; printf '{"id":1,"jsonrpc":"2.0","result":{"agentCapabilities":{},"protocolVersion":2}}\n'
        \\while IFS= read -r line; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the ACP agent answered initialize with another protocol version or no agentCapabilities", refusal.message);
}

test "a request the adapter does not serve is refused method-not-found, and a permission ask outside a run is cancelled" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\printf '{"id":9,"jsonrpc":"2.0","method":"fs/read_text_file","params":{"sessionId":"native-session","path":"/etc/hosts"}}\n'
        \\printf '{"id":10,"jsonrpc":"2.0","method":"session/request_permission","params":{"options":[{"kind":"allow_once","name":"Allow","optionId":"allow"}],"sessionId":"native-session","toolCall":{"title":"T","toolCallId":"t"}}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.waitWritten("{\"error\":{\"code\":-32601,\"message\":\"method not supported\"},\"id\":9,\"jsonrpc\":\"2.0\"}");
    _ = try probe.waitWritten("{\"id\":10,\"jsonrpc\":\"2.0\",\"result\":{\"outcome\":{\"outcome\":\"cancelled\"}}}");
}

test "an agent that dies mid-run fails the run with its exit and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ "take\nexit 4\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("doomed", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("acp_transport_failure", failure.get("code").?.string);
    try testing.expectEqualStrings("child exited with status 4", failure.get("message").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("after", &refusal));
}

test "a permission request still open when its run settles is answered cancelled" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take
        \\printf '{"id":"permission-1","jsonrpc":"2.0","method":"session/request_permission","params":{"options":[{"kind":"allow_once","name":"Allow once","optionId":"allow"}],"sessionId":"native-session","toolCall":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}\n'
        \\printf '{"id":3,"jsonrpc":"2.0","result":{"stopReason":"end_turn"}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("read it", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    _ = try probe.waitWritten("{\"id\":\"permission-1\",\"jsonrpc\":\"2.0\",\"result\":{\"outcome\":{\"outcome\":\"cancelled\"}}}");
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
}

test "attached process sources reach session/new as MCP servers, allowlisted environment resolved, and state lists them" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.openAttaching("[{\"id\":\"fs\",\"kind\":\"process\",\"display_name\":\"Files\",\"protocol\":\"mcp\",\"command\":\"/bin/fs\",\"args\":[\"-v\"],\"environment\":[\"PATH\",\"LIT=1\",\"MISSING\"]}]", &refusal);
    const scratch = probe.arena.allocator();
    const written = try probe.fake.written(scratch);
    const want = try std.fmt.allocPrint(scratch,
        \\{{"id":2,"jsonrpc":"2.0","method":"session/new","params":{{"cwd":"{s}","mcpServers":[{{"name":"fs","command":"/bin/fs","args":["-v"],"env":[{{"name":"PATH","value":"/usr/bin:/bin"}},{{"name":"LIT","value":"1"}}]}}]}}}}
    , .{probe.fake.cwd});
    try testing.expect(std.mem.indexOf(u8, written, want) != null);
    const state = try probe.handle.?.state(scratch, &refusal);
    try testing.expectEqual(@as(usize, 1), state.sources.len);
    try testing.expectEqualStrings("fs", state.sources[0].id);
    try testing.expectEqualStrings("process", state.sources[0].kind);
    try testing.expectEqualStrings("Files", state.sources[0].display_name.?);
    try testing.expectEqualStrings("mcp", state.sources[0].protocol.?);
}

test "an attachment ACP cannot carry is refused naming the source and why, before the agent starts" {
    const cases = [_]struct { json: []const u8, failure: contract.Failure, source: []const u8, detail: []const u8, message: []const u8 = "" }{
        .{ .json = "[{\"kind\":\"process\",\"command\":\"/bin/x\"}]", .failure = error.UnsupportedFeature, .source = "", .detail = "an attachment needs an id" },
        .{ .json = "[{\"id\":\"a\",\"kind\":\"process\",\"command\":\"/bin/x\"},{\"id\":\"a\",\"kind\":\"process\",\"command\":\"/bin/y\"}]", .failure = error.UnsupportedFeature, .source = "a", .detail = "the id already names a configured or attached MCP server" },
        .{ .json = "[{\"id\":\"notes\",\"kind\":\"local\"}]", .failure = error.UnsupportedFeature, .source = "notes", .detail = "ACP v1 accepts stdio MCP descriptors only at this pin" },
        .{ .json = "[{\"id\":\"a\",\"kind\":\"process\",\"command\":\"/bin/x\",\"environment\":[\"K=1\",\"K\"]}]", .failure = error.UnsupportedFeature, .source = "a", .detail = "environment names K twice" },
        .{ .json = "[{\"id\":\"a\",\"kind\":\"process\"}]", .failure = error.InvalidResolution, .source = "", .detail = "", .message = "adapter: invalid interaction resolution: tool source \"a\" declares a process source with no command" },
    };
    for (cases) |case| {
        var probe: Probe = undefined;
        try probe.init(fake_prelude ++ fake_idle);
        defer probe.deinit();
        var refusal = contract.Refusal{};
        try testing.expectError(case.failure, probe.openAttaching(case.json, &refusal));
        try testing.expectEqualStrings(case.source, refusal.source);
        try testing.expectEqualStrings(case.detail, refusal.detail);
        try testing.expectEqualStrings(case.message, refusal.message);
        if (case.failure == error.UnsupportedFeature) try testing.expectEqualStrings(contract.feature_tool_sources_attach, refusal.feature);
        try testing.expectError(error.FileNotFound, probe.fake.written(probe.arena.allocator()));
    }
}

test "a permission answer the agent cannot read fails the run acp_permission_response_failed and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take
        \\printf '{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}\n'
        \\exec 0<&-
        \\printf '{"id":"permission-1","jsonrpc":"2.0","method":"session/request_permission","params":{"options":[{"kind":"allow_once","name":"Allow once","optionId":"allow"},{"kind":"reject_once","name":"Reject","optionId":"deny"}],"sessionId":"native-session","toolCall":{"kind":"read","rawInput":{"path":"fixture.txt"},"sessionUpdate":"tool_call","status":"pending","title":"Read file","toolCallId":"native-tool"}}}\n'
        \\sleep 5
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const scratch = probe.arena.allocator();
    const admitted = try probe.submit("gated", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const gate = try probe.pumpUntil("action.permission.requested", &seen);
    const answer = oap_types.PermissionResolveRequest{ .interaction_id = (try probe.payloadOf(gate)).get("interaction_id").?.string, .requested_by = endpoint_id, .responded_by = "user", .session_id = "s1", .run_id = admitted.run_id.?, .granted = true, .choice_id = "allow" };
    try testing.expectError(error.BackendFailed, probe.handle.?.resolve(scratch, .{ .permission = &answer }, &refusal));
    try testing.expect(std.mem.startsWith(u8, refusal.message, "the ACP agent did not take the permission answer: "));

    var drained = std.ArrayList(contract.Event).empty;
    try probe.handle.?.drain(scratch, &drained);
    var failed: ?contract.Event = null;
    for (drained.items) |event| {
        if (std.mem.indexOf(u8, event.line, "\"type\":\"run.failed\"") != null) failed = event;
        try testing.expect(std.mem.indexOf(u8, event.line, "\"outcome\":\"resolved\"") == null);
    }
    try testing.expectEqualStrings("acp_permission_response_failed", (try probe.payloadOf(failed.?)).get("error").?.object.get("code").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(scratch, &refusal));
}
