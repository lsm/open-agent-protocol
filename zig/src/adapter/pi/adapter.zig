const std = @import("std");
const harness_pins = @import("harness_pins");
const builtin = @import("builtin");
const contract = @import("contract");
const oap_types = @import("oap_types");
const process = @import("process");
const compat = @import("compat");
const json_encode = @import("json_encode");
const session = @import("session.zig");
const rpc = @import("rpc.zig");

pub const endpoint_id = session.endpoint_id;
pub const capability_revision = harness_pins.pi_capability_revision;

const reopen_support_reason = "switch_session loads the bound session file; an absent, empty or mismatched file is refused";
const reopen_recovery_reason = "Pi restored the bound conversation and reports the current model, thinking level and compaction switch; OAP runs and cursors remain process-local";

const Binding = struct {
    sessionId: []const u8,
    sessionFile: []const u8,
};

fn readBinding(arena: std.mem.Allocator, raw: []const u8) !Binding {
    const binding = try std.json.parseFromSliceLeaky(Binding, arena, raw, .{});
    if (binding.sessionId.len == 0 or !std.fs.path.isAbsolute(binding.sessionFile)) return error.InvalidBinding;
    if (compat.fs.fileKind(compat.fs.getCwd(), binding.sessionFile) != .file) return error.InvalidBinding;
    var file = try compat.fs.openFile(compat.fs.getCwd(), binding.sessionFile, .{});
    defer file.close(compat.fs.defaultIo());
    if ((try file.stat(compat.fs.defaultIo())).kind != .file) return error.InvalidBinding;
    var header: std.ArrayList(u8) = .empty;
    var buffer: [4096]u8 = undefined;
    while (true) {
        const count = file.readStreaming(compat.fs.defaultIo(), &.{&buffer}) catch |err| switch (err) {
            error.EndOfStream => break,
            else => return err,
        };
        if (count == 0) break;
        const end = std.mem.indexOfScalar(u8, buffer[0..count], '\n');
        const limit = end orelse count;
        if (header.items.len + limit > 64 * 1024) return error.InvalidBinding;
        try header.appendSlice(arena, buffer[0..limit]);
        if (end != null) break;
    }
    const value = try std.json.parseFromSliceLeaky(std.json.Value, arena, header.items, .{});
    if (!std.mem.eql(u8, textOf(value, "type"), "session") or !std.mem.eql(u8, textOf(value, "id"), binding.sessionId)) return error.InvalidBinding;
    return binding;
}

const features = [_]contract.Feature{
    .{ .key = "action.permissions", .level = .unavailable, .reason = "extension dialogs are generic user input, not permissions" },
    .{ .key = "action.tools", .level = .degraded, .reason = "observed tool lifecycle only; no portable catalog" },
    .{ .key = "action.tools.execute", .level = .unavailable, .reason = "Pi executes tools internally" },
    .{ .key = "capabilities", .level = .emulated, .reason = "conservative descriptor synthesized for the pinned RPC vocabulary" },
    .{ .key = "protocol.initialize", .level = .emulated, .reason = "Pi has no negotiation; readiness is a get_state handshake" },
    .{ .key = "run.cancel", .level = .degraded, .reason = "abort intent is local; agent_settled remains terminal authority" },
    .{ .key = "run.compaction", .level = .native, .reason = "Pi's compaction_start and compaction_end inside a prompt run, threshold and overflow alike, become the run's compaction events; it compacts before agent_settled, so the run is still open" },
    .{ .key = "run.reconciliation", .level = .emulated, .reason = "get_state reconciles streaming state" },
    .{ .key = "run.replay", .level = .degraded, .reason = "bounded adapter journal; gaps explicit" },
    .{ .key = "run.resume", .level = .degraded, .reason = "bounded process-memory replay" },
    .{ .key = "run.status", .level = .emulated },
    .{ .key = "run.streaming", .level = .native },
    .{ .key = "session.compact", .level = .native, .reason = "a compaction request on an idle session runs Pi's compact command, with focus as its custom instructions; Pi never continues the turn, so continue is refused" },
    .{ .key = "session.message.delivery.auto", .level = .emulated, .reason = "idle auto is normalized to native prompt/start" },
    .{ .key = "session.message.delivery.queue", .level = .unavailable, .reason = "v0.1 admission cannot expose Pi queued prompt semantics safely" },
    .{ .key = "session.message.delivery.steer", .level = .emulated, .reason = "guidance rides Pi's native steer command and settles at the turn boundary Pi injects it" },
    .{ .key = "session.message.submit", .level = .emulated, .reason = "successful prompt response proves admission only" },
    .{ .key = contract.feature_open_reopen, .level = .native, .reason = reopen_support_reason },
    .{ .key = "session.open", .level = .emulated, .reason = "one ready Pi process is associated with one OAP session" },
    .{ .key = "session.state", .level = .emulated, .reason = "adapter projection reconciled with get_state" },
    .{ .key = contract.feature_session_reasoning, .level = .native, .reason = "set_thinking_level once the process is ready and again between runs, confirmed by get_state; a level Pi does not run the model at is refused, and a live change restores the level it replaced", .modes = &.{ contract.mode_session_open, contract.mode_session_live } },
    .{ .key = contract.feature_compaction_policy, .level = .native, .reason = "set_auto_compaction switches Pi's own threshold on or off, at open and between runs; its threshold is a settings-file reserve, so share and tokens are refused", .modes = &.{ contract.mode_session_open, contract.mode_session_live } },
};

fn compactionEnabled(arena: std.mem.Allocator, policy_json: ?[]const u8, refusal: *contract.Refusal) contract.Failure!?bool {
    const raw = policy_json orelse return null;
    const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, raw, .{}) catch return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unsatisfiable, "compaction_policy");
    const kind = textOf(parsed, "kind");
    if (std.mem.eql(u8, kind, "auto")) return true;
    if (std.mem.eql(u8, kind, "off")) return false;
    return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unsatisfiable, "compaction_policy");
}

pub const descriptor = contract.Descriptor{
    .endpoint = .{ .id = endpoint_id, .name = "Pi RPC Adapter", .version = harness_pins.pi_endpoint_version, .adapter = "pi-rpc-stdio" },
    .capability_revision = capability_revision,
    .features = &features,
};

const NativeContent = struct {
    text: []const u8,
    images: []const NativeImage,
};

const steer_reason_no_active_run = "no_active_run";
const steer_reason_terminal = "terminal";
const steer_reason_queued = "queued";
const steer_reason_unknown_target = "unknown_target";
const steer_reason_not_steerable = "not_steerable";

pub const Config = struct {
    executable: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    working_directory: ?[]const u8 = null,
    frame_limit: usize = rpc.frame_limit_default,
    exit_grace_ns: u64 = process.default_exit_grace_ns,
    request_timeout_ns: u64 = 60 * std.time.ns_per_s,
    admission_timeout_ns: u64 = 10 * 60 * std.time.ns_per_s,
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

const Dialog = enum { select, confirm, text };

const Ask = struct {
    native_id: []const u8,
    interaction_id: []const u8,
    dialog: Dialog,
    labels: []const []const u8,
};

const Reply = struct {
    success: bool,
    data: ?std.json.Value = null,
    message: []const u8 = "",
};

const NativeImage = struct {
    data: []const u8,
    mime_type: []const u8,
};

pub const Session = struct {
    owner: *Adapter,
    gpa: std.mem.Allocator,
    id: []u8,
    participant: []u8,
    reducer_arena: *std.heap.ArenaAllocator,
    transport: *process.Transport,
    reducer: ?*session.Reducer = null,
    settled_cursor: u64 = 0,
    current_model: []const u8 = "",
    native_session: []const u8 = "",
    native_binding: []const u8 = "",
    recovered: bool = false,
    next_request: usize = 0,
    awaited: []const u8 = "",
    reply: ?Reply = null,
    compacting_id: []const u8 = "",
    asks: std.ArrayList(Ask) = .empty,
    unbound: std.ArrayList(Ask) = .empty,
    bound: usize = 0,
    statuses: std.StringHashMapUnmanaged([]const u8) = .empty,
    unusable: bool = false,
    ended: bool = false,
    reaped: bool = false,
    reports_level: bool = false,
    reported_policy: ?[]const u8 = null,

    fn open(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const compaction = try compactionEnabled(arena, request.compaction_policy_json, refusal);
        const binding: ?Binding = if (request.reopen) readBinding(arena, request.native_session_id) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        } else null;
        const self = try construct(owner, arena, request, refusal);
        errdefer self.destroy();
        var state_data = try self.command(arena, "get_state", null, &.{}, refusal);
        if (binding) |bound| state_data = try self.reopenNative(arena, bound, refusal);
        try self.initializeState(state_data, refusal);
        if (compaction) |enabled| {
            _ = try self.commandWith(arena, "set_auto_compaction", null, &.{}, .{ .name = "enabled", .value = .{ .bool = enabled } }, refusal);
            self.reported_policy = try self.owned().dupe(u8, request.compaction_policy_json.?);
        }
        if (request.reasoning_level) |level| {
            _ = try self.commandWith(arena, "set_thinking_level", null, &.{}, .{ .name = "level", .value = .{ .string = level } }, refusal);
            const confirmed = try self.command(arena, "get_state", null, &.{}, refusal);
            const running = textOf(confirmed, "thinkingLevel");
            if (!std.mem.eql(u8, running, level)) return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
            self.reports_level = true;
        }
        return self;
    }

    fn reopenNative(self: *Session, arena: std.mem.Allocator, bound: Binding, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        const switched = self.commandWith(arena, "switch_session", null, &.{}, .{ .name = "sessionPath", .value = .{ .string = bound.sessionFile } }, refusal) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        };
        const cancelled = memberOf(switched, "cancelled") orelse std.json.Value.null;
        if (cancelled != .bool or cancelled.bool) return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        const state_data = self.command(arena, "get_state", null, &.{}, refusal) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        };
        const streaming = memberOf(state_data, "isStreaming") orelse std.json.Value.null;
        const compacting = memberOf(state_data, "isCompacting") orelse std.json.Value.null;
        if (invalidState(state_data) != null or !std.mem.eql(u8, textOf(state_data, "sessionId"), bound.sessionId) or !std.mem.eql(u8, textOf(state_data, "sessionFile"), bound.sessionFile) or streaming != .bool or streaming.bool or compacting != .bool or compacting.bool) return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        self.recovered = true;
        self.reports_level = true;
        const auto = memberOf(state_data, "autoCompactionEnabled") orelse std.json.Value.null;
        if (auto != .bool) return refusal.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
        self.reported_policy = if (auto.bool) "{\"kind\":\"auto\"}" else "{\"kind\":\"off\"}";
        return state_data;
    }

    fn initializeState(self: *Session, state_data: std.json.Value, refusal: *contract.Refusal) contract.Failure!void {
        const native_session = memberOf(state_data, "sessionId") orelse std.json.Value.null;
        if (invalidState(state_data)) |reason| return refusal.fail(error.BackendFailed, reason);
        const streaming = memberOf(state_data, "isStreaming") orelse std.json.Value.null;
        if (streaming == .bool and streaming.bool) return refusal.fail(error.BackendFailed, "the Pi agent was already streaming when the session opened");
        self.current_model = modelOf(self.owned(), memberOf(state_data, "model")) catch |err| return lift(err);
        self.native_session = try self.owned().dupe(u8, native_session.string);
        const file_path = textOf(state_data, "sessionFile");
        if (std.fs.path.isAbsolute(file_path)) self.native_binding = try std.json.Stringify.valueAlloc(self.owned(), Binding{ .sessionId = self.native_session, .sessionFile = file_path }, .{});
    }

    fn construct(owner: *Adapter, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!*Session {
        const gpa = owner.allocator;
        const config = owner.config;
        for (config.args) |arg| {
            if (std.mem.eql(u8, arg, "--")) return refusal.fail(error.BackendFailed, "a standalone -- in the Pi args prevents enforced extension disabling");
            if (std.mem.eql(u8, arg, "--extension") or std.mem.eql(u8, arg, "-e") or std.mem.startsWith(u8, arg, "--extension=")) return refusal.fail(error.BackendFailed, "explicit Pi extensions are incompatible with canonical prompt admission");
        }
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
        const argv = try std.mem.concat(arena, []const u8, &.{ config.args, &.{ "--mode", "rpc", "--no-extensions" } });
        const transport = process.Transport.open(gpa, .{
            .executable = config.executable,
            .args = argv,
            .environment = config.environment,
            .working_directory = config.working_directory,
            .frame_limit = config.frame_limit,
            .exit_grace_ns = config.exit_grace_ns,
        }) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            const message = try std.fmt.allocPrint(arena, "the Pi agent could not start: {s}", .{@errorName(err)});
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
        .native_id = nativeId,
        .state = state,
        .submit = submit,
        .resolve = resolve,
        .cancel = cancel,
        .compact = compact,
        .pump = pump,
        .drain = drain,
        .readable = readable,
        .activity = activity,
        .close = close,
        .update_settings = updateSettings,
    };

    fn updateSettings(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionSettingsUpdateRequest, refusal: *contract.Refusal) contract.Failure!contract.Updated {
        const self = cast(ptr);
        try contract.refuseUnadvertisedLiveSettings(descriptor, request, refusal);
        const compaction = try compactionEnabled(arena, request.compaction_policy_json, refusal);
        if (self.ended or self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.live() != null) return error.RunActive;
        const kept = self.owned();
        var response = oap_types.SessionSettingsUpdateResponse{ .session_id = self.id };
        if (request.reasoning_level) |level| {
            const before = try self.command(arena, "get_state", null, &.{}, refusal);
            const previous = try arena.dupe(u8, textOf(before, "thinkingLevel"));
            _ = try self.commandWith(arena, "set_thinking_level", null, &.{}, .{ .name = "level", .value = .{ .string = level } }, refusal);
            const confirmed = try self.command(arena, "get_state", null, &.{}, refusal);
            if (!std.mem.eql(u8, textOf(confirmed, "thinkingLevel"), level)) {
                _ = try self.commandWith(arena, "set_thinking_level", null, &.{}, .{ .name = "level", .value = .{ .string = previous } }, refusal);
                return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unsatisfiable, "reasoning_level");
            }
            response.previous_reasoning_level = previous;
            response.reasoning_level = level;
        }
        if (compaction) |enabled| {
            _ = try self.commandWith(arena, "set_auto_compaction", null, &.{}, .{ .name = "enabled", .value = .{ .bool = enabled } }, refusal);
            response.previous_compaction_policy_json = self.reported_policy;
            self.reported_policy = try kept.dupe(u8, request.compaction_policy_json.?);
            response.compaction_policy_json = self.reported_policy;
        }
        if (request.reasoning_level != null) self.reports_level = true;
        return .{ .response = response, .state = try state(ptr, arena, refusal) };
    }

    fn cast(ptr: *anyopaque) *Session {
        return @ptrCast(@alignCast(ptr));
    }

    fn owned(self: *Session) std.mem.Allocator {
        return self.reducer_arena.allocator();
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

    fn live(self: *Session) ?*session.Reducer {
        const reducer = self.reducer orelse return null;
        if (reducer.terminal) return null;
        return reducer;
    }

    fn fail(self: *Session, detail: []const u8) contract.Failure!void {
        if (self.ended) return;
        self.ended = true;
        self.reap();
        if (self.live()) |reducer| session.transportFailed(reducer, detail) catch |err| return lift(err);
        try self.recordTerminal();
    }

    fn send(self: *Session, value: std.json.Value) contract.Failure!bool {
        if (self.ended) return false;
        const frame = json_encode.valueAlloc(self.owned(), value) catch |err| return lift(err);
        self.transport.write(frame) catch |err| {
            try self.fail(@errorName(err));
            return false;
        };
        return true;
    }

    const Field = struct { name: []const u8, value: std.json.Value };

    fn commandFrame(self: *Session, kind: []const u8, message: ?[]const u8, images: []const NativeImage, field: ?Field) !struct { id: []const u8, value: std.json.Value } {
        self.next_request += 1;
        const id = try std.fmt.allocPrint(self.owned(), "req_{d}", .{self.next_request});
        var frame: std.json.ObjectMap = .empty;
        try frame.put(self.owned(), "id", .{ .string = id });
        try frame.put(self.owned(), "type", .{ .string = kind });
        if (field) |extra| try frame.put(self.owned(), extra.name, extra.value);
        if (message) |text| {
            try frame.put(self.owned(), "message", .{ .string = text });
            if (images.len > 0) {
                var list = std.json.Array.init(self.owned());
                for (images) |image| {
                    var entry: std.json.ObjectMap = .empty;
                    try entry.put(self.owned(), "type", .{ .string = "image" });
                    try entry.put(self.owned(), "data", .{ .string = image.data });
                    try entry.put(self.owned(), "mimeType", .{ .string = image.mime_type });
                    try list.append(.{ .object = entry });
                }
                try frame.put(self.owned(), "images", .{ .array = list });
            }
            if (std.mem.eql(u8, kind, "prompt")) try frame.put(self.owned(), "streamingBehavior", .{ .string = "steer" });
        }
        return .{ .id = id, .value = .{ .object = frame } };
    }

    fn command(self: *Session, arena: std.mem.Allocator, kind: []const u8, message: ?[]const u8, images: []const NativeImage, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        return self.commandWith(arena, kind, message, images, null, refusal);
    }

    fn commandWith(self: *Session, arena: std.mem.Allocator, kind: []const u8, message: ?[]const u8, images: []const NativeImage, field: ?Field, refusal: *contract.Refusal) contract.Failure!std.json.Value {
        const built = self.commandFrame(kind, message, images, field) catch |err| return lift(err);
        self.awaited = built.id;
        self.reply = null;
        defer self.awaited = "";
        if (!try self.send(built.value)) {
            const text = try std.fmt.allocPrint(arena, "the Pi agent exited before answering {s}", .{kind});
            return refusal.fail(error.BackendFailed, text);
        }
        const started = monotonic();
        while (self.reply == null) {
            if (self.ended) {
                const text = try std.fmt.allocPrint(arena, "the Pi agent exited before answering {s}", .{kind});
                return refusal.fail(error.BackendFailed, text);
            }
            if (monotonic() -| started > self.owner.config.request_timeout_ns) {
                try self.fail("the Pi agent stopped answering");
                const text = try std.fmt.allocPrint(arena, "the Pi agent did not answer {s} within {d} ms", .{ kind, self.owner.config.request_timeout_ns / std.time.ns_per_ms });
                return refusal.fail(error.BackendFailed, text);
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const answered = self.reply.?;
        self.reply = null;
        if (!answered.success) {
            const text = try std.fmt.allocPrint(arena, "pi {s} failed: {s}", .{ kind, answered.message });
            return refusal.fail(error.BackendFailed, text);
        }
        return answered.data orelse std.json.Value.null;
    }

    fn receive(self: *Session, wait_ns: u64) contract.Failure!?struct { frame: rpc.Frame, value: std.json.Value } {
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
                const frame = rpc.classify(self.owned(), held) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    const detail = try std.fmt.allocPrint(self.owned(), "pi rpc: {s}", .{@errorName(err)});
                    try self.fail(detail);
                    return null;
                };
                const value = std.json.parseFromSliceLeaky(std.json.Value, self.owned(), held, .{}) catch |err| return lift(err);
                return .{ .frame = frame, .value = value };
            },
        }
    }

    fn step(self: *Session, wait_ns: u64) contract.Failure!bool {
        const was_ended = self.ended;
        const received = try self.receive(wait_ns) orelse {
            if (self.ended != was_ended) try self.settleAsks();
            return self.ended != was_ended;
        };
        switch (received.frame.kind) {
            .response => {
                const id = textOf(received.value, "id");
                const success = memberOf(received.value, "success") orelse std.json.Value.null;
                const reply = Reply{
                    .success = success == .bool and success.bool,
                    .data = memberOf(received.value, "data"),
                    .message = textOf(received.value, "error"),
                };
                if (self.awaited.len > 0 and std.mem.eql(u8, id, self.awaited)) self.reply = reply;
                if (self.compacting_id.len > 0 and std.mem.eql(u8, id, self.compacting_id)) {
                    self.compacting_id = "";
                    if (self.reducer) |reducer| session.settleCompaction(reducer, reply.success, reply.data, reply.message) catch |err| return lift(err);
                }
            },
            .event => if (self.reducer) |reducer| {
                session.apply(reducer, received.value) catch |err| return lift(err);
                try self.bindAsks();
            },
            .extension_ui_request => try self.extensionRequest(received.value),
        }
        try self.recordTerminal();
        try self.settleAsks();
        return true;
    }

    fn extensionRequest(self: *Session, request: std.json.Value) contract.Failure!void {
        const native_id = textOf(request, "id");
        const method = textOf(request, "method");
        const dialog: ?Dialog = if (std.mem.eql(u8, method, "select"))
            .select
        else if (std.mem.eql(u8, method, "confirm"))
            .confirm
        else if (std.mem.eql(u8, method, "input") or std.mem.eql(u8, method, "editor"))
            .text
        else
            null;
        const reducer = self.live() orelse return;
        const kind = dialog orelse return;
        var labels = std.ArrayList([]const u8).empty;
        if (memberOf(request, "options")) |options| {
            if (options == .array) {
                for (options.array.items) |option| {
                    if (option == .string) try labels.append(self.owned(), option.string);
                }
            }
        }
        try self.unbound.append(self.owned(), .{
            .native_id = try self.owned().dupe(u8, native_id),
            .interaction_id = "",
            .dialog = kind,
            .labels = labels.items,
        });
        session.applyExtension(reducer, request) catch |err| return lift(err);
        try self.bindAsks();
    }

    fn bindAsks(self: *Session) contract.Failure!void {
        const reducer = self.reducer orelse return;
        while (self.bound < reducer.interactions.items.len and self.unbound.items.len > 0) : (self.bound += 1) {
            var ask = self.unbound.orderedRemove(0);
            ask.interaction_id = reducer.interactions.items[self.bound].id;
            try self.asks.append(self.owned(), ask);
        }
    }

    const Outcome = union(enum) { value: []const u8, confirmed: bool, cancelled };

    fn extensionAnswer(self: *Session, native_id: []const u8, outcome: Outcome) contract.Failure!std.json.Value {
        var frame: std.json.ObjectMap = .empty;
        try frame.put(self.owned(), "type", .{ .string = "extension_ui_response" });
        try frame.put(self.owned(), "id", .{ .string = native_id });
        switch (outcome) {
            .value => |text| try frame.put(self.owned(), "value", .{ .string = text }),
            .confirmed => |flag| try frame.put(self.owned(), "confirmed", .{ .bool = flag }),
            .cancelled => try frame.put(self.owned(), "cancelled", .{ .bool = true }),
        }
        return .{ .object = frame };
    }

    fn settleAsks(self: *Session) contract.Failure!void {
        if (self.live() != null and !self.ended) return;
        self.asks.clearRetainingCapacity();
        self.unbound.clearRetainingCapacity();
    }

    fn cursor(self: *Session) u64 {
        const reducer = self.reducer orelse return self.settled_cursor;
        return if (reducer.sequence > 1) reducer.sequence - 1 else self.settled_cursor;
    }

    fn recordTerminal(self: *Session) contract.Failure!void {
        const reducer = self.reducer orelse return;
        if (!reducer.terminal or self.statuses.contains(reducer.run_id)) return;
        var status: []const u8 = "failed";
        for (reducer.emitted.items) |envelope| {
            const kind = envelope.object.get("type").?.string;
            if (std.mem.eql(u8, kind, "run.completed")) status = "completed";
            if (std.mem.eql(u8, kind, "run.cancelled")) status = "cancelled";
        }
        try self.statuses.put(self.owned(), reducer.run_id, status);
    }

    fn nativeId(ptr: *anyopaque) []const u8 {
        return cast(ptr).native_binding;
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id;
    }

    fn state(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        const self = cast(ptr);
        if (self.ended or self.unusable) return error.SessionClosed;
        const state_data = try self.command(arena, "get_state", null, &.{}, refusal);
        const native_session = memberOf(state_data, "sessionId") orelse std.json.Value.null;
        if (invalidState(state_data)) |reason| return refusal.fail(error.BackendFailed, reason);
        if (!std.mem.eql(u8, native_session.string, self.native_session)) {
            self.unusable = true;
            return refusal.fail(error.BackendFailed, "the Pi agent's native session changed");
        }
        if (self.ended or self.unusable) return error.SessionClosed;
        const reducer = self.live();
        const active_run_id: ?[]const u8 = if (reducer) |running| try arena.dupe(u8, running.run_id) else null;
        const current_model_id: ?[]const u8 = if (self.current_model.len > 0) try arena.dupe(u8, self.current_model) else null;
        const active_runs = try self.pendingSteerEntries(arena, reducer);
        const transcript_cursor: ?[]const u8 = if (self.cursor() > 0) try std.fmt.allocPrint(arena, "{d}", .{self.cursor()}) else null;
        const reasoning_level: ?[]const u8 = if (self.reports_level) try arena.dupe(u8, textOf(state_data, "thinkingLevel")) else null;
        const compaction_policy_json: ?[]const u8 = if (self.reported_policy) |policy| try arena.dupe(u8, policy) else null;
        return .{
            .session_id = self.id,
            .status = if (reducer == null) .idle else if (self.asks.items.len > 0) .waiting_for_input else .running,
            .active_run_id = active_run_id,
            .active_runs = active_runs,
            .current_model_id = current_model_id,
            .transcript_cursor = transcript_cursor,
            .updated_at_ms = wallClock(),
            .reasoning_level = reasoning_level,
            .compaction_policy_json = compaction_policy_json,
            .recovered = self.recovered,
            .recovery_reason = if (self.recovered) reopen_recovery_reason else null,
        };
    }

    fn nativeContent(self: *Session, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest) contract.Failure!NativeContent {
        var texts = std.ArrayList([]const u8).empty;
        var images = std.ArrayList(NativeImage).empty;
        for (request.messages) |message| {
            if (message.role != .user) return error.InvalidSubmission;
            switch (message.content) {
                .text => |text| try texts.append(arena, text),
                .parts => |parts| for (parts) |part| switch (part) {
                    .text => |text| try texts.append(arena, text),
                    .image => |image| {
                        const data = image.data orelse return error.InvalidSubmission;
                        const media_type = image.media_type orelse return error.InvalidSubmission;
                        if (data.len == 0 or media_type.len == 0) return error.InvalidSubmission;
                        try images.append(arena, .{ .data = data, .mime_type = media_type });
                    },
                    else => return error.InvalidSubmission,
                },
            }
        }
        if (texts.items.len == 0) return error.InvalidSubmission;
        return .{ .text = try std.mem.join(self.owned(), "\n\n", texts.items), .images = try images.toOwnedSlice(arena) };
    }

    fn pendingSteerEntries(self: *Session, arena: std.mem.Allocator, reducer: ?*session.Reducer) contract.Failure![]oap_types.ActiveRun {
        _ = self;
        const running = reducer orelse return &.{};
        const pending = running.pendingSteers();
        if (pending.len == 0) return &.{};
        const carried = try arena.alloc(oap_types.PendingSteer, pending.len);
        for (pending, carried) |pending_steer, *slot| {
            slot.* = .{ .submission_id = try arena.dupe(u8, pending_steer.submission_id), .request_id = try arena.dupe(u8, pending_steer.request_id) };
        }
        const anchors = running.admittedSteerRequests();
        var carried_anchors: []const []const u8 = &.{};
        if (anchors.len > 0) {
            const slot = try arena.alloc([]const u8, anchors.len);
            for (anchors, slot) |anchor, *at| at.* = try arena.dupe(u8, anchor);
            carried_anchors = slot;
        }
        const entry = try arena.alloc(oap_types.ActiveRun, 1);
        entry[0] = .{
            .run_id = try arena.dupe(u8, running.run_id),
            .status = reducerStatus(running),
            .relationship = "primary",
            .as_of_sequence = running.sequence - 1,
            .admitted_submit_requests = carried_anchors,
            .pending_interactions = try running.pendingInteractionIDs(arena),
            .pending_steers = carried,
        };
        return entry;
    }

    fn reducerStatus(reducer: *session.Reducer) oap_types.RunStatus {
        const status = reducer.runStatus();
        if (std.mem.eql(u8, status, "cancelling")) return .cancelling;
        if (std.mem.eql(u8, status, "waiting_for_input")) return .waiting_for_input;
        return .running;
    }

    fn steer(self: *Session, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        if (request.session_id.len == 0 or request.messages.len == 0) return error.InvalidSubmission;
        try contract.refuseUnadvertisedControls(descriptor, request, refusal);
        if (self.ended or self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        const named = request.target_run_id orelse "";
        const reducer = self.live() orelse {
            const reason = if (named.len > 0)
                (if (self.statuses.contains(named)) steer_reason_terminal else steer_reason_unknown_target)
            else
                steer_reason_no_active_run;
            refusal.* = .{ .reason = reason, .message = "adapter: steer target cannot take guidance: the session has no started run" };
            return error.InvalidSteerTarget;
        };
        if (named.len > 0 and !std.mem.eql(u8, named, reducer.run_id)) {
            const reason = if (self.statuses.contains(named)) steer_reason_terminal else steer_reason_unknown_target;
            refusal.* = .{ .reason = reason, .message = try std.fmt.allocPrint(arena, "adapter: steer target cannot take guidance: run \"{s}\" is {s}", .{ named, reason }) };
            return error.InvalidSteerTarget;
        }
        if (!reducer.started) {
            const reason = if (named.len > 0) steer_reason_queued else steer_reason_no_active_run;
            refusal.* = .{ .reason = reason, .message = "adapter: steer target cannot take guidance: the run has not started" };
            return error.InvalidSteerTarget;
        }
        if (reducer.cancel_intent) {
            refusal.* = .{ .reason = steer_reason_not_steerable, .message = "adapter: steer target cannot take guidance: the run is cancelling" };
            return error.InvalidSteerTarget;
        }
        if (reducer.compacting) {
            refusal.* = .{ .reason = steer_reason_not_steerable, .message = "adapter: steer target cannot take guidance: the run is a compaction" };
            return error.InvalidSteerTarget;
        }
        const content = try self.nativeContent(arena, request);
        _ = self.command(arena, "steer", content.text, content.images, refusal) catch |err| return err;
        if (reducer.terminal or reducer.cancel_intent) {
            const reason = if (reducer.terminal) steer_reason_terminal else steer_reason_not_steerable;
            refusal.* = .{ .reason = reason, .message = "adapter: steer target cannot take guidance: the run settled while the steer was in flight" };
            return error.InvalidSteerTarget;
        }
        const message_ids = try arena.alloc([]const u8, request.messages.len);
        const kept_ids = try self.owned().alloc([]const u8, request.messages.len);
        for (request.messages, message_ids, kept_ids) |message, *slot, *kept| {
            const given = message.id orelse "";
            const id = if (given.len > 0) given else try reducer.counters.nextID(arena, "message");
            slot.* = id;
            kept.* = try self.owned().dupe(u8, id);
        }
        const submission_id = try reducer.counters.nextID(arena, "submission");
        try session.Reducer.admitSteer(reducer, try self.owned().dupe(u8, submission_id), try self.owned().dupe(u8, envelope_id), kept_ids);
        return .{
            .session_id = self.id,
            .accepted = true,
            .submission_id = submission_id,
            .requested_delivery = .steer,
            .effective_delivery = .steer,
            .admission = .steered,
            .run_id = try arena.dupe(u8, reducer.run_id),
            .status = reducerStatus(reducer),
            .message_ids = message_ids,
            .target_sequence = reducer.sequence - 1,
        };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        if (request.delivery == .steer) return self.steer(arena, request, envelope_id, refusal);
        if (request.session_id.len == 0 or request.messages.len == 0 or request.delivery != .auto) return error.InvalidSubmission;
        const content = try self.nativeContent(arena, request);
        const joined = content.text;
        if (std.mem.startsWith(u8, joined, "/")) return error.InvalidSubmission;
        if (self.ended or self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.live() != null) return error.RunActive;

        const reducer = try self.owned().create(session.Reducer);
        reducer.* = session.Reducer.init(self.owned());
        reducer.session_id = self.id;
        reducer.responder = self.participant;
        reducer.revision = capability_revision;
        reducer.model_id = self.current_model;
        reducer.mints_submission = true;
        self.bound = 0;
        self.unbound.clearRetainingCapacity();
        reducer.counters.shared = &self.owner.ids;
        reducer.counters.now_ms = wallClock;
        const message_ids = try arena.alloc([]const u8, request.messages.len);
        for (request.messages, message_ids) |message, *slot| {
            const given = message.id orelse "";
            slot.* = if (given.len > 0) try arena.dupe(u8, given) else try arena.dupe(u8, try reducer.counters.nextID(self.owned(), "message"));
        }
        reducer.run_id = try reducer.counters.nextID(self.owned(), "run");
        reducer.message_id = try reducer.counters.nextID(self.owned(), "message");
        self.settled_cursor = self.cursor();
        self.reducer = reducer;

        _ = self.command(arena, "prompt", joined, content.images, refusal) catch |err| {
            self.reducer = null;
            self.unusable = true;
            return err;
        };
        const started = monotonic();
        while (!reducer.started) {
            if (reducer.terminal or self.ended) return refusal.fail(error.BackendFailed, "the Pi agent ended the turn before starting it");
            if (monotonic() -| started > self.owner.config.admission_timeout_ns) {
                try self.fail("the Pi agent did not start the turn in time");
                return refusal.fail(error.BackendFailed, "the Pi agent did not start the turn in time");
            }
            _ = try self.step(self.owner.config.poll_ns);
        }
        const submission_id = try arena.dupe(u8, reducer.submission_id);
        const run_id = try arena.dupe(u8, reducer.run_id);
        const model_id: ?[]const u8 = if (self.current_model.len > 0) try arena.dupe(u8, self.current_model) else null;
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
            .model_id = model_id,
            .message_ids = message_ids,
        };
    }

    fn compact(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionCompactRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        _ = envelope_id;
        const self = cast(ptr);
        switch (request.delivery) {
            .auto => {},
            .queue, .steer, .btw => {
                refusal.* = .{
                    .feature = switch (request.delivery) {
                        .queue => "session.message.delivery.queue",
                        .steer => "session.message.delivery.steer",
                        else => "session.message.delivery.btw",
                    },
                    .reason = contract.reason_unadvertised,
                    .detail = "Pi compacts an idle session only",
                };
                return error.UnsupportedFeature;
            },
        }
        if (request.continue_run) {
            refusal.* = .{ .feature = "session.compact", .reason = contract.reason_unsatisfiable, .field = "continue", .detail = "Pi's compact command never continues the turn" };
            return error.UnsupportedFeature;
        }
        if (request.session_id.len == 0) return error.InvalidSubmission;
        if (self.ended or self.unusable) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, self.id)) return error.RunNotFound;
        if (self.live() != null) return error.RunActive;

        const reducer = try self.owned().create(session.Reducer);
        reducer.* = session.Reducer.init(self.owned());
        reducer.session_id = self.id;
        reducer.responder = self.participant;
        reducer.revision = capability_revision;
        reducer.model_id = self.current_model;
        self.bound = 0;
        self.unbound.clearRetainingCapacity();
        reducer.counters.shared = &self.owner.ids;
        reducer.counters.now_ms = wallClock;
        reducer.run_id = try reducer.counters.nextID(self.owned(), "run");
        reducer.submission_id = try reducer.counters.nextID(self.owned(), "submission");
        self.settled_cursor = self.cursor();
        self.reducer = reducer;
        session.openCompaction(reducer) catch |err| return lift(err);

        const focus: ?Field = if (request.focus) |text| .{ .name = "customInstructions", .value = .{ .string = try self.owned().dupe(u8, text) } } else null;
        const built = self.commandFrame("compact", null, &.{}, focus) catch |err| return lift(err);
        if (!try self.send(built.value)) return refusal.fail(error.BackendFailed, "the Pi agent exited before taking compact");
        self.compacting_id = built.id;
        const submission_id = try arena.dupe(u8, reducer.submission_id);
        const run_id = try arena.dupe(u8, reducer.run_id);
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
        };
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = refusal;
        const self = cast(ptr);
        const request = switch (resolution) {
            .input => |input| input,
            .permission => return error.InteractionNotFound,
        };
        if (self.ended or self.unusable) return error.SessionClosed;
        const at = self.askFor(request.interaction_id) orelse return error.InteractionNotFound;
        const reducer = self.live() orelse return error.InteractionNotFound;
        if (!std.mem.eql(u8, request.run_id, reducer.run_id) or !std.mem.eql(u8, request.session_id, self.id)) return error.InvalidResolution;
        if (!std.mem.eql(u8, request.responded_by, self.participant) or !std.mem.eql(u8, request.requested_by, endpoint_id)) return error.InvalidResolution;
        if (request.answers.len != 1) return error.InvalidResolution;
        const answer = request.answers[0];
        const ask = self.asks.items[at];
        if (!contract.validInputAnswer(try posed(arena, ask), answer)) return error.InvalidResolution;
        const outcome: Outcome = switch (ask.dialog) {
            .text => .{ .value = try self.owned().dupe(u8, answer.text orelse return error.InvalidResolution) },
            .confirm => confirmed: {
                if (answer.selected_option_ids.len != 1) return error.InvalidResolution;
                const chosen = answer.selected_option_ids[0];
                if (!std.mem.eql(u8, chosen, "yes") and !std.mem.eql(u8, chosen, "no")) return error.InvalidResolution;
                break :confirmed .{ .confirmed = std.mem.eql(u8, chosen, "yes") };
            },
            .select => selected: {
                if (answer.selected_option_ids.len != 1) return error.InvalidResolution;
                const chosen = answer.selected_option_ids[0];
                if (!std.mem.startsWith(u8, chosen, "option-")) return error.InvalidResolution;
                const index = std.fmt.parseInt(usize, chosen["option-".len..], 10) catch return error.InvalidResolution;
                if (index < 1 or index > ask.labels.len) return error.InvalidResolution;
                break :selected .{ .value = ask.labels[index - 1] };
            },
        };
        const reducer_answer = switch (ask.dialog) {
            .text => outcome.value,
            else => try self.owned().dupe(u8, answer.selected_option_ids[0]),
        };
        session.resolveExtension(reducer, ask.interaction_id, reducer_answer) catch |err| return switch (err) {
            error.InteractionNotFound, error.InteractionResolved => error.InteractionNotFound,
            error.InvalidResolution => error.InvalidResolution,
            else => lift(err),
        };
        _ = self.asks.orderedRemove(at);
        _ = try self.send(try self.extensionAnswer(ask.native_id, outcome));
    }

    fn posed(arena: std.mem.Allocator, ask: Ask) std.mem.Allocator.Error!contract.Question {
        return switch (ask.dialog) {
            .text => .{ .id = "value", .kind = .text },
            .confirm => .{ .id = "value", .kind = .single_choice, .options = &.{ "yes", "no" } },
            .select => select: {
                const options = try arena.alloc([]const u8, ask.labels.len);
                for (options, 1..) |*slot, position| slot.* = try std.fmt.allocPrint(arena, "option-{d}", .{position});
                break :select .{ .id = "value", .kind = .single_choice, .options = options };
            },
        };
    }

    fn askFor(self: *Session, interaction_id: []const u8) ?usize {
        for (self.asks.items, 0..) |ask, index| {
            if (std.mem.eql(u8, ask.interaction_id, interaction_id)) return index;
        }
        return null;
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        const self = cast(ptr);
        if (self.ended or self.unusable) return error.SessionClosed;
        if (self.statuses.contains(run_id)) return error.RunTerminal;
        const reducer = self.live() orelse return error.RunNotFound;
        if (!std.mem.eql(u8, reducer.run_id, run_id)) return error.RunNotFound;
        if (!reducer.cancel_intent) {
            session.cancel(reducer) catch |err| return lift(err);
            _ = self.command(arena, "abort", null, &.{}, refusal) catch |err| {
                if (err == error.BackendFailed and !self.ended) {
                    session.abortFailed(reducer, refusal.message) catch |failure| return lift(failure);
                    try self.recordTerminal();
                    try self.settleAsks();
                }
                return err;
            };
        }
        try self.recordTerminal();
        return .{ .session_id = self.id, .run_id = try arena.dupe(u8, run_id), .accepted = true, .status = .cancelling };
    }

    fn readable(ptr: *anyopaque) ?std.Io.File.Handle {
        const self = cast(ptr);
        return self.transport.readable();
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
        const reducer = self.reducer orelse return;
        try self.recordTerminal();
        try appendEvents(allocator, reducer.emitted.items, out);
        reducer.emitted.clearRetainingCapacity();
    }

    fn activity(ptr: *anyopaque) contract.Activity {
        const self = cast(ptr);
        if (self.live() == null) return .idle;
        if (self.asks.items.len > 0) return .waiting;
        return .running;
    }

    fn close(ptr: *anyopaque, force: bool) contract.Failure!void {
        _ = force;
        cast(ptr).destroy();
    }
};

fn invalidState(data: std.json.Value) ?[]const u8 {
    const native_session = memberOf(data, "sessionId") orelse std.json.Value.null;
    if (native_session != .string or native_session.string.len == 0) return "the Pi agent's get_state named no session";
    for ([_][]const u8{ "messageCount", "pendingMessageCount" }) |key| {
        const count = memberOf(data, key) orelse continue;
        if (count != .integer or count.integer < 0) return "the Pi agent's get_state reported a negative count";
    }
    for ([_][]const u8{ "steeringMode", "followUpMode" }) |key| {
        if (!oneOf(textOf(data, key), &.{ "all", "one-at-a-time" })) return "the Pi agent's get_state reported an invalid queue mode";
    }
    if (!oneOf(textOf(data, "thinkingLevel"), &.{ "off", "minimal", "low", "medium", "high", "xhigh", "max" })) return "the Pi agent's get_state reported an invalid thinking level";
    return null;
}

fn oneOf(text: []const u8, allowed: []const []const u8) bool {
    for (allowed) |candidate| if (std.mem.eql(u8, text, candidate)) return true;
    return false;
}

fn memberOf(value: std.json.Value, key: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(key);
}

fn textOf(value: std.json.Value, key: []const u8) []const u8 {
    const member = memberOf(value, key) orelse return "";
    return if (member == .string) member.string else "";
}

fn modelOf(arena: std.mem.Allocator, value: ?std.json.Value) ![]const u8 {
    const model = value orelse return "";
    const id = textOf(model, "id");
    const provider = textOf(model, "provider");
    if (id.len == 0) return textOf(model, "name");
    if (provider.len == 0) return id;
    return std.fmt.allocPrint(arena, "{s}/{s}", .{ provider, id });
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

pub const FakePi = struct {
    tmp: std.testing.TmpDir,
    path: []u8,

    pub fn init(allocator: std.mem.Allocator, script: []const u8) !FakePi {
        if (builtin.os.tag == .windows) return error.SkipZigTest;
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        try tmp.dir.writeFile(testing.io, .{ .sub_path = "pi", .data = script, .flags = .{ .permissions = .executable_file } });
        const cwd = try std.process.currentPathAlloc(testing.io, allocator);
        defer allocator.free(cwd);
        const path = try std.Io.Dir.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "pi" });
        return .{ .tmp = tmp, .path = path };
    }

    pub fn deinit(self: *FakePi, allocator: std.mem.Allocator) void {
        allocator.free(self.path);
        self.tmp.cleanup();
    }

    pub fn written(self: *FakePi, allocator: std.mem.Allocator) ![]u8 {
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

    pub fn config(self: *const FakePi) Config {
        return .{
            .executable = self.path,
            .environment = &.{"PATH=/usr/bin:/bin"},
            .exit_grace_ns = 2 * std.time.ns_per_s,
            .request_timeout_ns = 10 * std.time.ns_per_s,
            .admission_timeout_ns = 10 * std.time.ns_per_s,
            .poll_ns = 2 * std.time.ns_per_ms,
        };
    }
};

pub const fake_prelude =
    \\#!/bin/sh
    \\printf '%s\n' "$*" >"$(dirname "$0")/argv.log"
    \\exec 3>>"$(dirname "$0")/stdin.log"
    \\take() { IFS= read -r line || exit 0; printf '%s\n' "$line" >&3; }
    \\take; printf '{"type":"response","id":"req_1","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false,"model":{"id":"model","provider":"fixture"}}}\n'
    \\
;

const assistant_hello = "{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"hello\"}],\"api\":\"messages\",\"provider\":\"fixture\",\"model\":\"model\",\"usage\":{\"input\":1,\"output\":1,\"cacheRead\":0,\"cacheWrite\":0,\"totalTokens\":2,\"cost\":{\"input\":0,\"output\":0,\"cacheRead\":0,\"cacheWrite\":0,\"total\":0}},\"stopReason\":\"stop\",\"timestamp\":1}";

const fake_prompt_accepted =
    \\take; printf '{"type":"response","id":"req_2","command":"prompt","success":true}\n'
    \\printf '{"type":"agent_start"}\n'
    \\
;

pub const fake_text_turn = fake_prompt_accepted ++
    "printf '%s\\n' '{\"type\":\"message_end\",\"message\":" ++ assistant_hello ++ "}'\n" ++
    "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
    "printf '{\"type\":\"agent_settled\"}\\n'\n";

pub const fake_dialog_turn = fake_prompt_accepted ++
    \\printf '{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Proceed?","message":"Continue"}\n'
    \\take
    \\
++
    "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
    "printf '{\"type\":\"agent_settled\"}\\n'\n";

pub const fake_idle = "while take; do :; done\n";

const Probe = struct {
    fake: FakePi,
    adapter: Adapter,
    arena: std.heap.ArenaAllocator,
    handle: ?contract.Session = null,

    fn init(self: *Probe, script: []const u8) !void {
        self.fake = try FakePi.init(testing.allocator, script);
        self.adapter = Adapter.init(testing.allocator, self.fake.config());
        self.arena = std.heap.ArenaAllocator.init(testing.allocator);
        self.handle = null;
    }

    fn deinit(self: *Probe) void {
        if (self.handle) |opened| opened.teardown();
        self.arena.deinit();
        self.fake.deinit(testing.allocator);
    }

    fn open(self: *Probe, refusal: *contract.Refusal) !contract.Session {
        const opened = try self.adapter.adapter().vtable.open(&self.adapter, self.arena.allocator(), .{ .session_id = "s1", .participant = "user" }, refusal);
        self.handle = opened;
        return opened;
    }

    fn submit(self: *Probe, text: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const messages = try self.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = text } }});
        const request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
        return self.handle.?.submit(self.arena.allocator(), &request, "", refusal);
    }

    fn pumpUntil(self: *Probe, comptime kind: []const u8, seen: *std.ArrayList(contract.Event)) !contract.Event {
        var rounds: usize = 0;
        while (rounds < 2000) : (rounds += 1) {
            var drained = std.ArrayList(contract.Event).empty;
            try self.handle.?.drain(self.arena.allocator(), &drained);
            try seen.appendSlice(self.arena.allocator(), drained.items);
            for (drained.items) |event| {
                if (std.mem.indexOf(u8, event.line, "\"type\":\"" ++ kind ++ "\"") != null) return event;
            }
            _ = try self.handle.?.pump(5 * std.time.ns_per_ms);
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

test "an open sets Pi's compaction and thinking level in Go's command form and reports what get_state confirms" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"set_auto_compaction","success":true}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"set_thinking_level","success":true}\n'
        \\take; printf '{"type":"response","id":"req_4","command":"get_state","success":true,"data":{"thinkingLevel":"high","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_5","command":"get_state","success":true,"data":{"thinkingLevel":"high","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.adapter.adapter().open(probe.arena.allocator(), .{ .session_id = "s1", .participant = "user", .reasoning_level = "high", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal);
    probe.handle = opened;
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"set_auto_compaction","enabled":false}
        \\{"id":"req_3","type":"set_thinking_level","level":"high"}
        \\{"id":"req_4","type":"get_state"}
        \\
    , written);
    const reported = try opened.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings("high", reported.reasoning_level.?);
    try testing.expectEqualStrings("{\"kind\":\"off\"}", reported.compaction_policy_json.?);
}

test "a level Pi does not confirm is refused, and a threshold before Pi starts" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"set_thinking_level","success":true}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"get_state","success":true,"data":{"thinkingLevel":"high","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, probe.adapter.adapter().open(probe.arena.allocator(), .{ .session_id = "s1", .participant = "user", .reasoning_level = "max" }, &refusal));
    try testing.expectEqualStrings(contract.feature_session_reasoning, refusal.feature);

    var unstarted: Probe = undefined;
    try unstarted.init(fake_prelude ++ fake_idle);
    defer unstarted.deinit();
    try testing.expectError(error.UnsupportedFeature, unstarted.adapter.adapter().open(unstarted.arena.allocator(), .{ .session_id = "s2", .participant = "user", .compaction_policy_json = "{\"kind\":\"tokens\",\"tokens\":1000}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
    try testing.expectError(error.FileNotFound, unstarted.fake.written(unstarted.arena.allocator()));
}

test "a live update sets Pi's thinking level and compaction between runs and reports what get_state confirms" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"set_thinking_level","success":true}\n'
        \\take; printf '{"type":"response","id":"req_4","command":"get_state","success":true,"data":{"thinkingLevel":"high","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_5","command":"set_auto_compaction","success":true}\n'
        \\take; printf '{"type":"response","id":"req_6","command":"get_state","success":true,"data":{"thinkingLevel":"high","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const updater = opened.vtable.update_settings.?;
    const updated = try updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "high", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal);
    try testing.expectEqualStrings("off", updated.response.previous_reasoning_level.?);
    try testing.expectEqualStrings("high", updated.response.reasoning_level.?);
    try testing.expectEqualStrings("{\"kind\":\"off\"}", updated.response.compaction_policy_json.?);
    try testing.expectEqualStrings("high", updated.state.reasoning_level.?);
    try testing.expectEqualStrings("{\"kind\":\"off\"}", updated.state.compaction_policy_json.?);
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"get_state"}
        \\{"id":"req_3","type":"set_thinking_level","level":"high"}
        \\{"id":"req_4","type":"get_state"}
        \\{"id":"req_5","type":"set_auto_compaction","enabled":false}
        \\{"id":"req_6","type":"get_state"}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "a live level Pi does not confirm is refused and Pi is put back on the level it replaced" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"medium","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"set_thinking_level","success":true}\n'
        \\take; printf '{"type":"response","id":"req_4","command":"get_state","success":true,"data":{"thinkingLevel":"high","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_5","command":"set_thinking_level","success":true}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const updater = opened.vtable.update_settings.?;
    try testing.expectError(error.UnsupportedFeature, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .reasoning_level = "max", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_session_reasoning, refusal.feature);
    try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"get_state"}
        \\{"id":"req_3","type":"set_thinking_level","level":"max"}
        \\{"id":"req_4","type":"get_state"}
        \\{"id":"req_5","type":"set_thinking_level","level":"medium"}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "a live update is refused for what Pi cannot do, before anything is written" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    const opened = try probe.open(&refusal);
    const before = try probe.fake.written(probe.arena.allocator());
    const updater = opened.vtable.update_settings.?;
    try testing.expectError(error.UnsupportedFeature, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "s1", .compaction_policy_json = "{\"kind\":\"tokens\",\"tokens\":1000}" }, &refusal));
    try testing.expectEqualStrings(contract.feature_compaction_policy, refusal.feature);
    try testing.expectEqualStrings("compaction_policy", refusal.field);
    try testing.expectError(error.RunNotFound, updater(opened.ptr, probe.arena.allocator(), &.{ .session_id = "other", .reasoning_level = "high" }, &refusal));
    try testing.expectEqualStrings(before, try probe.fake.written(probe.arena.allocator()));
}

test "an open asks get_state and takes the model it reports, and the prompt reaches Pi in Go's command form" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    try testing.expectEqualStrings("fixture/model", admitted.model_id.?);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"prompt","message":"hello","streamingBehavior":"steer"}
        \\
    , written);
}

test "a submission carrying an inline image reaches Pi as Go's images command" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    var parts = [_]oap_types.ContentPart{
        .{ .text = "look" },
        .{ .image = .{ .data = "aGk=", .media_type = "image/png" } },
    };
    const messages = try probe.arena.allocator().dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .parts = &parts } }});
    var request = oap_types.MessageSubmitRequest{ .session_id = "s1", .messages = messages, .delivery = .auto };
    _ = try probe.handle.?.submit(probe.arena.allocator(), &request, "", &refusal);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"prompt","message":"look","images":[{"type":"image","data":"aGk=","mimeType":"image/png"}],"streamingBehavior":"steer"}
        \\
    , written);
}

test "run.started names the model get_state reported, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const started = try probe.pumpUntil("run.started", &seen);
    try testing.expectEqualStrings("fixture/model", (try probe.payloadOf(started)).get("model_id").?.string);
}

test "a turn is admitted on agent_start and settles on agent_end, citing the served revision" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expect(std.mem.indexOf(u8, seen.items[0].line, "\"type\":\"run.started\"") != null);
    for (seen.items, 1..) |event, sequence| {
        try testing.expectEqualStrings(admitted.run_id.?, event.run_id);
        try testing.expectEqual(@as(u64, sequence), event.sequence);
        try testing.expect(std.mem.indexOf(u8, event.line, "\"capability_revision\":\"" ++ capability_revision ++ "\"") != null);
    }
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectError(error.RunTerminal, probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal));
}

test "a confirm dialog becomes a user input interaction, a malformed answer is refused unwritten, and a valid one reaches Pi as confirmed" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_dialog_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("go", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    try testing.expectEqual(contract.Activity.waiting, probe.handle.?.activity());
    const payload = try probe.payloadOf(asked);
    const selected = try probe.arena.allocator().dupe([]const u8, &.{"yes"});
    const answers = try probe.arena.allocator().dupe(oap_types.InputAnswer, &.{.{ .question_id = "value", .selected_option_ids = selected }});
    const request = oap_types.UserInputResolveRequest{
        .interaction_id = payload.get("interaction_id").?.string,
        .requested_by = payload.get("requested_by").?.string,
        .responded_by = payload.get("responded_by").?.string,
        .session_id = payload.get("session_id").?.string,
        .run_id = payload.get("run_id").?.string,
        .answers = answers,
    };
    for ([_]oap_types.InputAnswer{
        .{ .question_id = "other", .selected_option_ids = selected },
        .{ .question_id = "value", .text = "yes", .selected_option_ids = selected },
        .{ .question_id = "value", .selected_option_ids = try probe.arena.allocator().dupe([]const u8, &.{"maybe"}) },
    }) |wrong| {
        var refused = request;
        refused.answers = try probe.arena.allocator().dupe(oap_types.InputAnswer, &.{wrong});
        try testing.expectError(error.InvalidResolution, probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &refused }, &refusal));
    }
    try testing.expect(std.mem.indexOf(u8, try probe.fake.written(probe.arena.allocator()), "extension_ui_response") == null);
    try probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &request }, &refusal);
    _ = try probe.waitWritten("{\"type\":\"extension_ui_response\",\"id\":\"ui-1\",\"confirmed\":true}");
    _ = try probe.pumpUntil("run.completed", &seen);
    try testing.expectError(error.InteractionNotFound, probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &request }, &refusal));
}

test "a cancel sends abort once and answers cancelling" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        \\take; printf '{"type":"response","id":"req_3","command":"abort","success":true}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    const first = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, first.status);
    const again = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, again.status);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqual(@as(usize, 1), std.mem.count(u8, written, "\"type\":\"abort\""));
    try testing.expectError(error.RunNotFound, probe.handle.?.cancel(probe.arena.allocator(), "run-unknown", &refusal));
}

test "a cancel that settles the run inside the abort still answers cancelling" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        "take; printf '%s\\n' '{\"type\":\"message_end\",\"message\":" ++ assistant_hello ++ "}'\n" ++
        "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
        "printf '{\"type\":\"agent_settled\"}\\n'\n" ++
        "printf '{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"abort\",\"success\":true}\\n'\n" ++
        "while take; do :; done\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.started", &seen);

    const cancelled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expect(cancelled.accepted);
    try testing.expectEqual(oap_types.RunStatus.cancelling, cancelled.status);
    _ = try probe.pumpUntil("run.completed", &seen);
}

test "a Pi agent already streaming at open refuses the session" {
    var probe: Probe = undefined;
    try probe.init(
        \\#!/bin/sh
        \\IFS= read -r line; printf '{"type":"response","id":"req_1","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":true}}\n'
        \\while IFS= read -r line; do :; done
        \\
    );
    defer probe.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, probe.open(&refusal));
    try testing.expectEqualStrings("the Pi agent was already streaming when the session opened", refusal.message);
}

test "a Pi agent that dies mid-run fails the run with its exit and closes the session" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++ "exit 5\n");
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("doomed", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("pi_process_exit", failure.get("code").?.string);
    try testing.expectEqualStrings("child exited with status 5", failure.get("message").?.string);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
}

test "a dialog still open when its run settles resolves cancelled and writes Pi nothing, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        \\printf '{"type":"extension_ui_request","id":"ui-1","method":"confirm","title":"Proceed?","message":"Continue"}\n'
        \\
    ++
        "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
        "printf '{\"type\":\"agent_settled\"}\\n'\n" ++
        \\take; printf '{"type":"response","id":"req_3","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("go", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);
    const resolved = seen.items[seen.items.len - 2];
    try testing.expect(std.mem.indexOf(u8, resolved.line, "\"type\":\"user.input.resolved\"") != null);
    try testing.expectEqualStrings("cancelled", (try probe.payloadOf(resolved)).get("status").?.string);
    _ = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"prompt","message":"go","streamingBehavior":"steer"}
        \\{"id":"req_3","type":"get_state"}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "a dialog raised outside a run is ignored and writes Pi nothing, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"extension_ui_request","id":"ui-9","method":"confirm","title":"Idle?","message":"No run"}\n'
        \\printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    _ = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"get_state"}
        \\{"id":"req_3","type":"get_state"}
        \\
    , try probe.fake.written(probe.arena.allocator()));
}

test "the child runs with extensions disabled, and an explicit extension argument refuses the open" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const argv = try probe.fake.tmp.dir.readFileAlloc(testing.io, "argv.log", probe.arena.allocator(), .limited(4096));
    try testing.expectEqualStrings("--mode rpc --no-extensions\n", argv);
    try testing.expectEqual(oap_types.SupportLevel.unavailable, descriptor.level("action.permissions"));

    var refused: Probe = undefined;
    try refused.init(fake_prelude ++ fake_idle);
    defer refused.deinit();
    refused.adapter.config.args = &.{ "--extension", "x.ts" };
    try testing.expectError(error.BackendFailed, refused.open(&refusal));
    try testing.expectEqualStrings("explicit Pi extensions are incompatible with canonical prompt admission", refusal.message);
}

test "a prompt Pi refuses closes the session rather than leaving an unstarted run behind" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"prompt","success":false,"error":"no model"}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.submit("hello", &refusal));
    try testing.expectEqualStrings("pi prompt failed: no model", refusal.message);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("again", &refusal));
}

test "a dialog raised before agent_start surfaces once the run starts and its answer reaches Pi, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"prompt","success":true}\n'
        \\printf '{"type":"extension_ui_request","id":"ui-0","method":"confirm","title":"Early?","message":"Before start"}\n'
        \\printf '{"type":"agent_start"}\n'
        \\take
        \\
    ++ "printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
        "printf '{\"type\":\"agent_settled\"}\\n'\n" ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const asked = try probe.pumpUntil("user.input.requested", &seen);
    const payload = try probe.payloadOf(asked);
    const answers = try probe.arena.allocator().dupe(oap_types.InputAnswer, &.{.{ .question_id = "value", .selected_option_ids = try probe.arena.allocator().dupe([]const u8, &.{"no"}) }});
    const request = oap_types.UserInputResolveRequest{
        .interaction_id = payload.get("interaction_id").?.string,
        .requested_by = payload.get("requested_by").?.string,
        .responded_by = payload.get("responded_by").?.string,
        .session_id = payload.get("session_id").?.string,
        .run_id = payload.get("run_id").?.string,
        .answers = answers,
    };
    try probe.handle.?.resolve(probe.arena.allocator(), .{ .input = &request }, &refusal);
    _ = try probe.waitWritten("{\"type\":\"extension_ui_response\",\"id\":\"ui-0\",\"confirmed\":false}");
    _ = try probe.pumpUntil("run.completed", &seen);
}

test "an abort Pi refuses fails the run with pi_abort_failed, as Go does" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++
        \\take; printf '{"type":"response","id":"req_3","command":"abort","success":false,"error":"busy"}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("long", &refusal);
    try testing.expectError(error.BackendFailed, probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal));
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const failure = (try probe.payloadOf(failed)).get("error").?.object;
    try testing.expectEqualStrings("pi_abort_failed", failure.get("code").?.string);
    try testing.expectEqualStrings("pi abort failed: busy", failure.get("message").?.string);
    try testing.expectEqual(contract.Activity.idle, probe.handle.?.activity());
}

test "a state request reads get_state again, idle and mid-run, and answers the adapter projection with the last sequence as its cursor" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":false}}\n'
        \\take; printf '{"type":"response","id":"req_3","command":"prompt","success":true}\n'
        \\printf '{"type":"agent_start"}\n'
        \\take; printf '{"type":"response","id":"req_4","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"native-session","isStreaming":true}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const idle = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.idle, idle.status);
    try testing.expectEqualStrings("fixture/model", idle.current_model_id.?);
    try testing.expect(idle.transcript_cursor == null);
    const admitted = try probe.submit("long", &refusal);
    const running = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.running, running.status);
    try testing.expectEqualStrings(admitted.run_id.?, running.active_run_id.?);
    try testing.expectEqualStrings("1", running.transcript_cursor.?);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expectEqualStrings(
        \\{"id":"req_1","type":"get_state"}
        \\{"id":"req_2","type":"get_state"}
        \\{"id":"req_3","type":"prompt","message":"long","streamingBehavior":"steer"}
        \\{"id":"req_4","type":"get_state"}
        \\
    , written);
}

test "a get_state naming another native session makes the session unusable" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        \\take; printf '{"type":"response","id":"req_2","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":0,"pendingMessageCount":0,"sessionId":"other-session","isStreaming":false}}\n'
        \\
    ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectEqualStrings("the Pi agent's native session changed", refusal.message);
    try testing.expectError(error.SessionClosed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectError(error.SessionClosed, probe.submit("again", &refusal));
}

fn stateData(comptime session_id: []const u8, comptime counts: []const u8, comptime steering: []const u8, comptime follow_up: []const u8, comptime thinking: []const u8) []const u8 {
    return "\"thinkingLevel\":\"" ++ thinking ++ "\",\"steeringMode\":\"" ++ steering ++ "\",\"followUpMode\":\"" ++ follow_up ++ "\"," ++ counts ++ ",\"sessionId\":\"" ++ session_id ++ "\"";
}

const valid_counts = "\"messageCount\":0,\"pendingMessageCount\":0";

fn expectRefusedGetState(data: []const u8, message: []const u8) !void {
    const at_open = try std.fmt.allocPrint(testing.allocator, "#!/bin/sh\nIFS= read -r line; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_1\",\"command\":\"get_state\",\"success\":true,\"data\":{{{s}}}}}'\nwhile IFS= read -r line; do :; done\n", .{data});
    defer testing.allocator.free(at_open);
    var opening: Probe = undefined;
    try opening.init(at_open);
    defer opening.deinit();
    var refusal = contract.Refusal{};
    try testing.expectError(error.BackendFailed, opening.open(&refusal));
    try testing.expectEqualStrings(message, refusal.message);

    const on_state = try std.fmt.allocPrint(testing.allocator, "{s}take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"get_state\",\"success\":true,\"data\":{{{s}}}}}'\n" ++
        "take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"get_state\",\"success\":true,\"data\":{{\"thinkingLevel\":\"max\",\"steeringMode\":\"one-at-a-time\",\"followUpMode\":\"all\",\"sessionId\":\"native-session\"}}}}'\n{s}", .{ fake_prelude, data, fake_idle });
    defer testing.allocator.free(on_state);
    var probe: Probe = undefined;
    try probe.init(on_state);
    defer probe.deinit();
    _ = try probe.open(&refusal);
    try testing.expectError(error.BackendFailed, probe.handle.?.state(probe.arena.allocator(), &refusal));
    try testing.expectEqualStrings(message, refusal.message);
    const after = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(oap_types.SessionStatus.idle, after.status);
}

test "a get_state naming no session is refused at open and on a state request, which leaves the session usable" {
    try expectRefusedGetState(stateData("", valid_counts, "all", "all", "off"), "the Pi agent's get_state named no session");
}

test "a get_state with a negative messageCount is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", "\"messageCount\":-1,\"pendingMessageCount\":0", "all", "all", "off"), "the Pi agent's get_state reported a negative count");
}

test "a get_state with a negative pendingMessageCount is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", "\"messageCount\":0,\"pendingMessageCount\":-1", "all", "all", "off"), "the Pi agent's get_state reported a negative count");
}

test "a get_state with an unknown steeringMode is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", valid_counts, "some", "all", "off"), "the Pi agent's get_state reported an invalid queue mode");
}

test "a get_state with an unknown followUpMode is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", valid_counts, "all", "some", "off"), "the Pi agent's get_state reported an invalid queue mode");
}

test "a get_state with an unknown thinkingLevel is refused at open and on a state request" {
    try expectRefusedGetState(stateData("native-session", valid_counts, "all", "all", "extreme"), "the Pi agent's get_state reported an invalid thinking level");
}

const fake_steer_accepted =
    \\take; printf '{"type":"response","id":"req_3","command":"steer","success":true}\n'
    \\
;

const fake_steer_state =
    \\take; printf '{"type":"response","id":"req_4","command":"get_state","success":true,"data":{"thinkingLevel":"off","steeringMode":"all","followUpMode":"one-at-a-time","messageCount":1,"pendingMessageCount":1,"sessionId":"native-session","isStreaming":true,"model":{"id":"model","provider":"fixture"}}}\n'
    \\printf '{"type":"turn_end","message":{},"toolResults":[]}\n'
    \\
;

fn steerRequest(arena: std.mem.Allocator, target: ?[]const u8) !oap_types.MessageSubmitRequest {
    const messages = try arena.dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "adjust" } }});
    return .{ .session_id = "s1", .messages = messages, .delivery = .steer, .target_run_id = target };
}

test "a steer reaches Pi as its own command, admits against the started run and settles at the turn boundary" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++ fake_steer_accepted ++ fake_steer_state ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);

    var request = try steerRequest(probe.arena.allocator(), admitted.run_id);
    const steered = try probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal);
    try testing.expectEqual(oap_types.RequestedDelivery.steer, steered.requested_delivery);
    try testing.expectEqual(oap_types.EffectiveDelivery.steer, steered.effective_delivery);
    try testing.expectEqual(oap_types.Admission.steered, steered.admission);
    try testing.expectEqual(@as(?u64, 1), steered.target_sequence);
    try testing.expectEqualStrings(admitted.run_id.?, steered.run_id.?);
    try testing.expectEqual(@as(usize, 1), steered.message_ids.len);

    const written = try probe.fake.written(probe.arena.allocator());
    var steer_line: ?[]const u8 = null;
    var lines = std.mem.splitScalar(u8, written, '\n');
    while (lines.next()) |line| {
        if (std.mem.indexOf(u8, line, "\"type\":\"steer\"") != null) steer_line = line;
    }
    const frame = steer_line orelse return error.MissingSteerFrame;
    try testing.expectEqualStrings("{\"id\":\"req_3\",\"type\":\"steer\",\"message\":\"adjust\"}", frame);

    const state = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(@as(usize, 1), state.active_runs.len);
    try testing.expectEqual(@as(usize, 1), state.active_runs[0].pending_steers.len);
    try testing.expectEqualStrings("steer-request", state.active_runs[0].pending_steers[0].request_id);
    try testing.expectEqualStrings(steered.submission_id, state.active_runs[0].pending_steers[0].submission_id);
    try testing.expectEqual(@as(?u64, 1), state.active_runs[0].as_of_sequence);
    try testing.expectEqual(@as(usize, 1), state.active_runs[0].admitted_submit_requests.len);
    try testing.expectEqualStrings("steer-request", state.active_runs[0].admitted_submit_requests[0]);

    var seen = std.ArrayList(contract.Event).empty;
    const applied = try probe.pumpUntil("run.steer.applied", &seen);
    const payload = try probe.payloadOf(applied);
    try testing.expectEqualStrings("steer-request", payload.get("request_id").?.string);
    try testing.expectEqualStrings(steered.submission_id, payload.get("submission_id").?.string);
    try testing.expectEqualStrings("turn", payload.get("boundary").?.string);
    try testing.expectEqual(@as(usize, 1), payload.get("message_ids").?.array.items.len);
}

test "a steer without a started run is refused as an invalid steer target" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    var request = try steerRequest(probe.arena.allocator(), null);
    try testing.expectError(error.InvalidSteerTarget, probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal));
    try testing.expectEqualStrings("no_active_run", refusal.reason);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"type\":\"steer\"") == null);
}

test "a steer naming another run is refused without reaching Pi" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var request = try steerRequest(probe.arena.allocator(), "run-elsewhere");
    try testing.expectError(error.InvalidSteerTarget, probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal));
    try testing.expectEqualStrings("unknown_target", refusal.reason);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "\"type\":\"steer\"") == null);
}

const fake_settled_before_steer =
    "take; printf '%s\\n' '{\"type\":\"agent_end\",\"messages\":[" ++ assistant_hello ++ "],\"willRetry\":false}'\n" ++
    "printf '{\"type\":\"agent_settled\"}\n'\n" ++
    "printf '{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"steer\",\"success\":true}\n'\n";

test "a steer naming a settled run is refused as terminal" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);

    var request = try steerRequest(probe.arena.allocator(), admitted.run_id);
    try testing.expectError(error.InvalidSteerTarget, probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal));
    try testing.expectEqualStrings("terminal", refusal.reason);
}

test "a steer whose target settles while the steer is in flight is refused" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++ fake_settled_before_steer ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);

    var request = try steerRequest(probe.arena.allocator(), admitted.run_id);
    try testing.expectError(error.InvalidSteerTarget, probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal));
    try testing.expectEqualStrings("terminal", refusal.reason);

    var settled = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &settled);
    try testing.expect(std.mem.indexOf(u8, completed.line, "run.completed") != null);
}

const fake_dialog_open = fake_prompt_accepted ++
    "printf '%s\\n' '{\"type\":\"extension_ui_request\",\"id\":\"ui-1\",\"method\":\"confirm\",\"title\":\"Proceed?\",\"message\":\"Continue\"}'\n" ++
    "take; printf '{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"steer\",\"success\":true}\n'\n" ++
    "take; printf '{\"type\":\"response\",\"id\":\"req_4\",\"command\":\"get_state\",\"success\":true,\"data\":{\"thinkingLevel\":\"off\",\"steeringMode\":\"all\",\"followUpMode\":\"one-at-a-time\",\"messageCount\":1,\"pendingMessageCount\":1,\"sessionId\":\"native-session\",\"isStreaming\":true,\"model\":{\"id\":\"model\",\"provider\":\"fixture\"}}}\n'\n" ++
    "";

test "a steer during an extension dialog reports the waiting status" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_dialog_open ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const gate = try probe.pumpUntil("user.input.requested", &seen);
    const gate_payload = try probe.payloadOf(gate);
    const interaction_id = gate_payload.get("interaction_id").?.string;

    var request = try steerRequest(probe.arena.allocator(), admitted.run_id);
    const steered = try probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal);
    try testing.expectEqual(oap_types.RunStatus.waiting_for_input, steered.status.?);

    const state = try probe.handle.?.state(probe.arena.allocator(), &refusal);
    try testing.expectEqual(@as(usize, 1), state.active_runs.len);
    try testing.expectEqual(@as(usize, 1), state.active_runs[0].pending_interactions.len);
    try testing.expectEqualStrings(interaction_id, state.active_runs[0].pending_interactions[0]);
}

test "a steer naming an unknown run on an idle session is refused as unknown_target" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    _ = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);

    var request = try steerRequest(probe.arena.allocator(), "run-typo");
    try testing.expectError(error.InvalidSteerTarget, probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal));
    try testing.expectEqualStrings("unknown_target", refusal.reason);
}

test "a steer refuses a run control as unadvertised" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_text_turn ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const admitted = try probe.submit("hello", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.completed", &seen);

    var request = try steerRequest(probe.arena.allocator(), admitted.run_id);
    request.model_id = "other";
    try testing.expectError(error.UnsupportedFeature, probe.handle.?.submit(probe.arena.allocator(), &request, "steer-request", &refusal));
    try testing.expectEqualStrings("run.model_selection", refusal.feature);
    try testing.expectEqualStrings(contract.reason_unadvertised, refusal.reason);
}

const fake_compaction_result = "{\"summary\":\"the summary\",\"firstKeptEntryId\":\"e1\",\"tokensBefore\":20,\"estimatedTokensAfter\":5,\"usage\":{},\"details\":{}}";

fn compactRequest(focus: ?[]const u8) oap_types.SessionCompactRequest {
    return .{ .session_id = "s1", .focus = focus };
}

test "a compaction request runs Pi's compact command as a run of its own, with the focus as its instructions" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        "take; printf '%s\\n' '{\"type\":\"compaction_start\",\"reason\":\"manual\"}'\n" ++
        "printf '%s\\n' '{\"type\":\"compaction_end\",\"reason\":\"manual\",\"result\":" ++ fake_compaction_result ++ ",\"aborted\":false,\"willRetry\":false}'\n" ++
        "printf '%s\\n' '{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"compact\",\"success\":true,\"data\":" ++ fake_compaction_result ++ "}'\n" ++
        fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const request = compactRequest("keep the plan");
    const admitted = try probe.handle.?.vtable.compact.?(probe.handle.?.ptr, probe.arena.allocator(), &request, "compact", &refusal);
    try testing.expectEqual(oap_types.Admission.started, admitted.admission);
    var seen = std.ArrayList(contract.Event).empty;
    const completed = try probe.pumpUntil("run.completed", &seen);
    const kinds = [_][]const u8{ "run.started", "run.compaction.started", "run.compaction.ended", "run.completed" };
    try testing.expectEqual(kinds.len, seen.items.len);
    for (kinds, seen.items) |kind, event| {
        const tagged = try std.fmt.allocPrint(probe.arena.allocator(), "\"type\":\"{s}\"", .{kind});
        try testing.expect(std.mem.indexOf(u8, event.line, tagged) != null);
    }
    const started = try probe.payloadOf(seen.items[1]);
    try testing.expectEqualStrings("requested", started.get("reason").?.string);
    const ended = try probe.payloadOf(seen.items[2]);
    const settled = try probe.payloadOf(completed);
    try testing.expectEqualStrings("compacted", settled.get("stop_reason").?.string);
    try testing.expectEqualStrings(ended.get("summary").?.object.get("id").?.string, settled.get("final_response").?.object.get("id").?.string);
    const written = try probe.fake.written(probe.arena.allocator());
    try testing.expect(std.mem.indexOf(u8, written, "{\"id\":\"req_2\",\"type\":\"compact\",\"customInstructions\":\"keep the plan\"}") != null);
}

test "a compaction Pi refuses fails the run with Pi's reason" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        "take; printf '%s\\n' '{\"type\":\"compaction_start\",\"reason\":\"manual\"}'\n" ++
        "printf '%s\\n' '{\"type\":\"compaction_end\",\"reason\":\"manual\",\"aborted\":false,\"willRetry\":false,\"errorMessage\":\"Compaction failed: Nothing to compact (session too small)\"}'\n" ++
        "printf '%s\\n' '{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"compact\",\"success\":false,\"error\":\"Nothing to compact (session too small)\"}'\n" ++
        fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const request = compactRequest(null);
    _ = try probe.handle.?.vtable.compact.?(probe.handle.?.ptr, probe.arena.allocator(), &request, "compact", &refusal);
    var seen = std.ArrayList(contract.Event).empty;
    const failed = try probe.pumpUntil("run.failed", &seen);
    const payload = try probe.payloadOf(failed);
    try testing.expectEqualStrings("pi_compaction_failed", payload.get("error").?.object.get("code").?.string);
    try testing.expect(std.mem.indexOf(u8, payload.get("error").?.object.get("message").?.string, "Nothing to compact") != null);
}

test "a cancelled compaction aborts Pi and settles cancelled" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++
        "take; printf '%s\\n' '{\"type\":\"compaction_start\",\"reason\":\"manual\"}'\n" ++
        "take; printf '%s\\n' '{\"type\":\"compaction_end\",\"reason\":\"manual\",\"aborted\":true,\"willRetry\":false}'\n" ++
        "printf '%s\\n' '{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"compact\",\"success\":false,\"error\":\"Compaction cancelled\"}'\n" ++
        "printf '%s\\n' '{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"abort\",\"success\":true}'\n" ++
        fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    const request = compactRequest(null);
    const admitted = try probe.handle.?.vtable.compact.?(probe.handle.?.ptr, probe.arena.allocator(), &request, "compact", &refusal);
    const cancelled = try probe.handle.?.cancel(probe.arena.allocator(), admitted.run_id.?, &refusal);
    try testing.expectEqual(oap_types.RunStatus.cancelling, cancelled.status);
    var seen = std.ArrayList(contract.Event).empty;
    _ = try probe.pumpUntil("run.cancelled", &seen);
    var outcome: []const u8 = "";
    for (seen.items) |event| {
        if (std.mem.indexOf(u8, event.line, "\"type\":\"run.compaction.ended\"") != null) outcome = (try probe.payloadOf(event)).get("outcome").?.string;
    }
    try testing.expectEqualStrings("cancelled", outcome);
}

test "a compaction request is refused for what Pi cannot do" {
    var probe: Probe = undefined;
    try probe.init(fake_prelude ++ fake_prompt_accepted ++ fake_idle);
    defer probe.deinit();
    var refusal = contract.Refusal{};
    _ = try probe.open(&refusal);
    var continuing = compactRequest(null);
    continuing.continue_run = true;
    try testing.expectError(error.UnsupportedFeature, probe.handle.?.vtable.compact.?(probe.handle.?.ptr, probe.arena.allocator(), &continuing, "", &refusal));
    try testing.expectEqualStrings("session.compact", refusal.feature);
    try testing.expectEqualStrings("continue", refusal.field);
    var queued = compactRequest(null);
    queued.delivery = .queue;
    refusal = .{};
    try testing.expectError(error.UnsupportedFeature, probe.handle.?.vtable.compact.?(probe.handle.?.ptr, probe.arena.allocator(), &queued, "", &refusal));
    try testing.expectEqualStrings("session.message.delivery.queue", refusal.feature);
    _ = try probe.submit("busy", &refusal);
    const idle = compactRequest(null);
    try testing.expectError(error.RunActive, probe.handle.?.vtable.compact.?(probe.handle.?.ptr, probe.arena.allocator(), &idle, "", &refusal));
}

fn boundPiFixture(arena: std.mem.Allocator, fake: *const FakePi) !Binding {
    const path = try std.fs.path.join(arena, &.{ std.fs.path.dirname(fake.path).?, "session.jsonl" });
    try fake.tmp.dir.writeFile(testing.io, .{ .sub_path = "session.jsonl", .data = "{\"type\":\"session\",\"id\":\"bound-session\",\"version\":3,\"cwd\":\"/workspace\"}\n" });
    return .{ .sessionId = "bound-session", .sessionFile = path };
}

fn restoredPiState(arena: std.mem.Allocator, path: []const u8, id: []const u8) ![]const u8 {
    return std.json.Stringify.valueAlloc(arena, .{
        .sessionId = id,
        .sessionFile = path,
        .thinkingLevel = "high",
        .steeringMode = "all",
        .followUpMode = "one-at-a-time",
        .messageCount = @as(usize, 3),
        .pendingMessageCount = @as(usize, 0),
        .isStreaming = false,
        .isCompacting = false,
        .autoCompactionEnabled = true,
        .model = .{ .id = "restored", .provider = "fixture" },
    }, .{});
}

test "Pi reopens its recorded file and reports the restored settings without OAP runs" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const held = arena.allocator();
    var fake = try FakePi.init(testing.allocator, fake_prelude ++ fake_idle);
    defer fake.deinit(testing.allocator);
    const binding = try boundPiFixture(held, &fake);
    const state_data = try restoredPiState(held, binding.sessionFile, binding.sessionId);
    const script = try std.fmt.allocPrint(held, "{s}" ++
        "take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"switch_session\",\"success\":true,\"data\":{{\"cancelled\":false}}}}'\n" ++
        "take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_3\",\"command\":\"get_state\",\"success\":true,\"data\":{s}}}'\n" ++
        "take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_4\",\"command\":\"get_state\",\"success\":true,\"data\":{s}}}'\n" ++ fake_idle, .{ fake_prelude, state_data, state_data });
    try fake.tmp.dir.writeFile(testing.io, .{ .sub_path = "pi", .data = script, .flags = .{ .permissions = .executable_file } });
    var adapter = Adapter.init(testing.allocator, fake.config());
    var refusal = contract.Refusal{};
    const encoded = try std.json.Stringify.valueAlloc(held, binding, .{});
    const opened = try adapter.adapter().open(held, .{ .session_id = "oap-session", .participant = "user", .reopen = true, .native_session_id = encoded }, &refusal);
    defer opened.teardown();
    const state_value = try opened.state(held, &refusal);
    try testing.expectEqualStrings("oap-session", state_value.session_id);
    try testing.expectEqual(oap_types.SessionStatus.idle, state_value.status);
    try testing.expect(state_value.recovered);
    try testing.expectEqualStrings(reopen_recovery_reason, state_value.recovery_reason.?);
    try testing.expectEqualStrings("fixture/restored", state_value.current_model_id.?);
    try testing.expectEqualStrings("high", state_value.reasoning_level.?);
    try testing.expectEqualStrings("{\"kind\":\"auto\"}", state_value.compaction_policy_json.?);
    try testing.expect(state_value.active_run_id == null and state_value.transcript_cursor == null);
    try testing.expectEqualStrings(encoded, opened.nativeId());
    var events: std.ArrayList(contract.Event) = .empty;
    try opened.drain(held, &events);
    try testing.expectEqual(@as(usize, 0), events.items.len);
    const written = try fake.written(held);
    try testing.expect(std.mem.indexOf(u8, written, "\"type\":\"switch_session\"") != null);
    try testing.expect(std.mem.indexOf(u8, written, binding.sessionFile) != null);
    try testing.expect(std.mem.indexOf(u8, written, "\"type\":\"prompt\"") == null);
}

test "Pi refuses invalid files before starting its native process" {
    const samples = [_][]const u8{ "", "{", "{\"type\":\"message\",\"id\":\"bound-session\"}", "{\"type\":\"session\",\"id\":\"someone-else\"}", "{\"type\":\"session\",\"id\":\"bound-session\",\"id\":\"bound-session\"}" };
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const held = arena.allocator();
    var fake = try FakePi.init(testing.allocator, fake_prelude ++ fake_idle);
    defer fake.deinit(testing.allocator);
    const binding = try boundPiFixture(held, &fake);
    var adapter = Adapter.init(testing.allocator, fake.config());
    const encoded = try std.json.Stringify.valueAlloc(held, binding, .{});
    for (samples) |sample| {
        try fake.tmp.dir.writeFile(testing.io, .{ .sub_path = "session.jsonl", .data = sample });
        var refusal = contract.Refusal{};
        try testing.expectError(error.UnsupportedFeature, adapter.adapter().open(held, .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = encoded }, &refusal));
        try testing.expectEqualStrings(contract.feature_open_reopen, refusal.feature);
        try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);
        try testing.expectError(error.FileNotFound, fake.tmp.dir.openFile(testing.io, "stdin.log", .{}));
    }
    try fake.tmp.dir.deleteFile(testing.io, "session.jsonl");
    var refusal = contract.Refusal{};
    try testing.expectError(error.UnsupportedFeature, adapter.adapter().open(held, .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = encoded }, &refusal));
    for ([_][]const u8{ "", "{", "{}", "{\"sessionId\":\"bound-session\",\"sessionFile\":\"relative.jsonl\"}" }) |raw| {
        refusal = .{};
        try testing.expectError(error.UnsupportedFeature, adapter.adapter().open(held, .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = raw }, &refusal));
    }
}

test "Pi refuses a cancelled or unconfirmed native file switch" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const held = arena.allocator();
    for ([_][]const u8{ "null", "{}", "{\"cancelled\":true}", "{\"cancelled\":\"false\"}" }) |reply| {
        const script = try std.fmt.allocPrint(held, "{s}take; printf '%s\\n' '{{\"type\":\"response\",\"id\":\"req_2\",\"command\":\"switch_session\",\"success\":true,\"data\":{s}}}'\n{s}", .{ fake_prelude, reply, fake_idle });
        var fake = try FakePi.init(testing.allocator, script);
        defer fake.deinit(testing.allocator);
        const binding = try boundPiFixture(held, &fake);
        var adapter = Adapter.init(testing.allocator, fake.config());
        var refusal = contract.Refusal{};
        const encoded = try std.json.Stringify.valueAlloc(held, binding, .{});
        try testing.expectError(error.UnsupportedFeature, adapter.adapter().open(held, .{ .session_id = "s1", .participant = "user", .reopen = true, .native_session_id = encoded }, &refusal));
        try testing.expectEqualStrings(contract.feature_open_reopen, refusal.feature);
        try testing.expectEqualStrings(contract.reason_unsatisfiable, refusal.reason);
    }
}

fn bindingAllocationProbe(allocator: std.mem.Allocator, raw: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    _ = try readBinding(arena.allocator(), raw);
}

test "Pi binding header allocations release on every failure" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    var fake = try FakePi.init(testing.allocator, fake_prelude ++ fake_idle);
    defer fake.deinit(testing.allocator);
    const binding = try boundPiFixture(arena.allocator(), &fake);
    const encoded = try std.json.Stringify.valueAlloc(arena.allocator(), binding, .{});
    try testing.checkAllAllocationFailures(testing.allocator, bindingAllocationProbe, .{encoded});
}

test "Pi replays the captured reload exchange through its native loader" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const held = arena.allocator();
    const root = harness_pins.pi_corpus;
    const path = try std.fs.path.join(held, &.{ root, "session-reopen", "native.jsonl" });
    const input = try compat.fs.readFileAlloc(held, compat.fs.getCwd(), path, 128 * 1024);
    var script: std.ArrayList(u8) = .empty;
    try script.appendSlice(held, "#!/bin/sh\nexec 3>>\"$(dirname \"$0\")/stdin.log\"\ntake() { IFS= read -r line || exit 0; printf '%s\\n' \"$line\" >&3; }\n");
    var commands: std.ArrayList(std.json.Value) = .empty;
    var bound: ?Binding = null;
    var lines = std.mem.tokenizeScalar(u8, input, '\n');
    while (lines.next()) |line| {
        const frame = try std.json.parseFromSliceLeaky(std.json.Value, held, line, .{});
        const raw = memberOf(frame, "raw").?;
        if (std.mem.eql(u8, textOf(frame, "direction"), "host-to-pi")) {
            try commands.append(held, raw);
            try script.appendSlice(held, "take\n");
        } else {
            const literal = try json_encode.valueAlloc(held, raw);
            try script.appendSlice(held, try std.fmt.allocPrint(held, "printf '%s\\n' '{s}'\n", .{literal}));
            const data = memberOf(raw, "data") orelse std.json.Value.null;
            if (std.mem.eql(u8, textOf(raw, "id"), "req_3")) bound = .{ .sessionId = textOf(data, "sessionId"), .sessionFile = textOf(data, "sessionFile") };
        }
    }
    try script.appendSlice(held, fake_idle);
    var fake = try FakePi.init(testing.allocator, script.items);
    defer fake.deinit(testing.allocator);
    var adapter = Adapter.init(testing.allocator, fake.config());
    var refusal = contract.Refusal{};
    const opened = try Session.construct(&adapter, held, .{ .session_id = "session", .participant = "user" }, &refusal);
    defer opened.destroy();
    _ = try opened.command(held, "get_state", null, &.{}, &refusal);
    const loaded = try opened.reopenNative(held, bound.?, &refusal);
    try opened.initializeState(loaded, &refusal);
    const state_value = try opened.handle().state(held, &refusal);
    const expected_path = try std.fs.path.join(held, &.{ root, "session-reopen", "expected-oap.json" });
    const expected = try std.json.parseFromSliceLeaky(std.json.Value, held, try compat.fs.readFileAlloc(held, compat.fs.getCwd(), expected_path, 4096), .{});
    try testing.expect(state_value.recovered);
    try testing.expectEqualStrings(textOf(expected, "session_id"), state_value.session_id);
    try testing.expectEqualStrings(textOf(expected, "current_model_id"), state_value.current_model_id.?);
    try testing.expectEqualStrings(textOf(expected, "reasoning_level"), state_value.reasoning_level.?);
    try testing.expectEqualStrings(textOf(memberOf(expected, "recovery").?, "reason"), state_value.recovery_reason.?);
    const policy = try std.json.parseFromSliceLeaky(std.json.Value, held, state_value.compaction_policy_json.?, .{});
    try testing.expectEqualStrings(textOf(memberOf(expected, "compaction_policy").?, "kind"), textOf(policy, "kind"));
    try testing.expectEqual(@as(usize, 1), policy.object.count());
    try testing.expect(state_value.status == .idle and state_value.active_run_id == null and state_value.transcript_cursor == null);
    const recorded = try std.json.parseFromSliceLeaky(Binding, held, opened.native_binding, .{});
    try testing.expectEqualStrings(bound.?.sessionId, recorded.sessionId);
    try testing.expectEqualStrings(bound.?.sessionFile, recorded.sessionFile);
    var seen: std.ArrayList(contract.Event) = .empty;
    try opened.handle().drain(held, &seen);
    try testing.expectEqual(@as(usize, 0), seen.items.len);
    const written = try fake.written(held);
    var sent = std.mem.tokenizeScalar(u8, written, '\n');
    for (commands.items) |command| {
        const actual = try std.json.parseFromSliceLeaky(std.json.Value, held, sent.next().?, .{});
        try testing.expectEqual(command.object.count(), actual.object.count());
        var members = command.object.iterator();
        while (members.next()) |member| try testing.expectEqualStrings(member.value_ptr.string, textOf(actual, member.key_ptr.*));
    }
    try testing.expect(sent.next() == null);
}
