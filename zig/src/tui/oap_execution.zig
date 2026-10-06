const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const json_encode = @import("json_encode");
const model_ref = @import("model_ref");
const tui_runtime = @import("tui_runtime");
const tui_session = @import("tui_session");
const CompactionEnd = @TypeOf(@as(tui_session.TuiEvent, undefined).compaction_end);
const CompactionOutcome = @FieldType(CompactionEnd, "outcome");
const adapter_endpoint = @import("adapter_endpoint");
const oapx_adapter = @import("oapx_adapter");
const hub_link = @import("hub_link");
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
    adapter: ?*oapx_adapter.Adapter = null,
    endpoint: ?adapter_endpoint.Endpoint = null,
    hub: ?*hub_link.HubLink = null,
    sink: ?tui_runtime.EventSink = null,
    revision: []u8 = &.{},
    session_id: []u8 = &.{},
    run_id: []u8 = &.{},
    submitted: []u8 = &.{},
    ids: u64 = 0,
    inbound: std.ArrayList([]u8) = .empty,
    inbound_mutex: std.atomic.Mutex = .unlocked,
    records: std.ArrayList(TuiEvent) = .empty,
    records_mutex: std.atomic.Mutex = .unlocked,
    thread: ?std.Thread = null,
    stopping: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    turn_open: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    cancel_pending: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    in_assistant: bool = false,
    text: std.ArrayList(u8) = .empty,
    session_models: std.ArrayList([]u8) = .empty,
    live_reasoning: bool = false,
    live_compaction: bool = false,
    has_history: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    live_policy: bool = false,
    live_steer: bool = false,
    steers: std.ArrayList(PendingSteer) = .empty,
    steers_settled: std.atomic.Value(u64) = std.atomic.Value(u64).init(0),
    compacting: bool = false,
    compaction_result: ?CompactionEnd = null,
    sent_policy: []u8 = &.{},
    pending_permission: ?PendingPermission = null,
    queued_runs: std.ArrayList(QueuedRun) = .empty,
    output_tokens: u64 = 0,
    closed_messages: usize = 0,
    awaiting_promotion: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    cancelling: bool = false,

    const QueuedRun = struct {
        text: []u8,
        submit_id: []u8,
        run_id: []u8 = &.{},
        dropped: bool = false,

        fn deinit(self: *QueuedRun, allocator: std.mem.Allocator) void {
            allocator.free(self.text);
            allocator.free(self.submit_id);
            allocator.free(self.run_id);
            self.* = undefined;
        }
    };

    const PendingSteer = struct {
        text: []u8,
        request_id: []u8,
        admitted: bool = false,

        fn deinit(self: *PendingSteer, allocator: std.mem.Allocator) void {
            allocator.free(self.text);
            allocator.free(self.request_id);
            self.* = undefined;
        }
    };

    const PendingPermission = struct {
        interaction_id: []u8,
        tool_call_id: []u8,
        requested_by: []u8,
        responded_by: []u8,
        run_id: []u8,
        offers_approve_always: bool = false,
        offers_reject_always: bool = false,

        fn deinit(self: *PendingPermission, allocator: std.mem.Allocator) void {
            allocator.free(self.interaction_id);
            allocator.free(self.tool_call_id);
            allocator.free(self.requested_by);
            allocator.free(self.responded_by);
            allocator.free(self.run_id);
            self.* = undefined;
        }
    };

    pub fn create(allocator: std.mem.Allocator, options: tui_runtime.TuiRuntimeOptions) !*OapExecution {
        const adapter = try allocator.create(oapx_adapter.Adapter);
        errdefer allocator.destroy(adapter);
        adapter.* = oapx_adapter.Adapter.init(allocator, options);
        const self = try allocator.create(OapExecution);
        self.* = .{ .allocator = allocator, .adapter = adapter, .endpoint = adapter_endpoint.Endpoint.init(allocator, adapter.adapter(), .{ .frame_limit = in_process_frame_limit }) };
        adapter.recorder = .{ .ctx = self, .record = record };
        return self;
    }

    fn record(ctx: *anyopaque, session_id: []const u8, event: *const TuiEvent) void {
        const self = cast(ctx);
        if (!std.mem.eql(u8, session_id, self.session_id)) return;
        var cloned = event.clone(self.allocator) catch return;
        while (!self.records_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.records_mutex.unlock();
        self.records.append(self.allocator, cloned) catch cloned.deinit(self.allocator);
    }

    fn takeRecord(ctx: *anyopaque) ?TuiEvent {
        const self = cast(ctx);
        while (!self.records_mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.records_mutex.unlock();
        if (self.records.items.len == 0) return null;
        return self.records.orderedRemove(0);
    }

    fn recordsSession(ctx: *anyopaque) bool {
        return cast(ctx).adapter != null;
    }

    pub fn setTranscripts(self: *OapExecution, store: oapx_adapter.TranscriptStore) void {
        if (self.adapter) |held| held.transcripts = store;
    }

    pub fn attach(allocator: std.mem.Allocator, base: []const u8, adapter_name: []const u8) !*OapExecution {
        const link = try hub_link.HubLink.create(allocator, base, adapter_name);
        errdefer link.destroy();
        const self = try allocator.create(OapExecution);
        self.* = .{ .allocator = allocator, .hub = link };
        return self;
    }

    fn sendLine(self: *OapExecution, line: []const u8) !void {
        if (self.hub) |link| return link.handleLine(line);
        try self.endpoint.?.handleLine(line);
    }

    fn pumpLink(self: *OapExecution) !bool {
        if (self.hub) |link| return link.pump();
        return self.endpoint.?.pump(0);
    }

    fn popLine(self: *OapExecution) ?[]u8 {
        if (self.hub) |link| return link.popOutbound();
        return self.endpoint.?.popOutbound();
    }

    pub fn destroy(self: *OapExecution) void {
        const allocator = self.allocator;
        self.halt();
        if (self.endpoint) |*endpoint| endpoint.deinit();
        if (self.hub) |link| {
            link.closeSession();
            link.destroy();
        }
        for (self.inbound.items) |line| allocator.free(line);
        self.inbound.deinit(allocator);
        for (self.records.items) |*held| held.deinit(allocator);
        self.records.deinit(allocator);
        self.text.deinit(allocator);
        self.forgetSessionModels();
        self.session_models.deinit(allocator);
        self.forgetPermission();
        self.dropCompactionResult();
        for (self.queued_runs.items) |*queued| queued.deinit(allocator);
        self.queued_runs.deinit(allocator);
        for (self.steers.items) |*pending| pending.deinit(allocator);
        self.steers.deinit(allocator);
        allocator.free(self.revision);
        allocator.free(self.sent_policy);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        allocator.free(self.submitted);
        if (self.adapter) |adapter| allocator.destroy(adapter);
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
        .set_reasoning = setReasoning,
        .compacts = compacts,
        .take_record = takeRecord,
        .records_session = recordsSession,
        .compactable = compactable,
        .compact = compact,
        .set_compaction_policy = setCompactionPolicy,
        .decide_approval = decideApproval,
        .follow_up = followUp,
        .steer = steer,
        .clear_queued = clearQueued,
        .queued = queuedCount,
        .steers_pending = steersPending,
        .steers_settled = steersSettled,
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
        var empty = Map.init(a);
        const initialized = if (self.hub != null)
            try self.exchange(a, "capabilities.request", empty.value(), false)
        else
            try self.exchange(a, "protocol.initialize.request", initialize.value(), false);
        const revision = initialized.object.get("capability_revision") orelse return error.OapInitializeFailed;
        if (revision != .string) return error.OapInitializeFailed;
        const kept_revision = try self.allocator.dupe(u8, revision.string);
        self.allocator.free(self.revision);
        self.revision = kept_revision;
        const described = if (self.hub != null)
            initialized
        else
            try self.exchange(a, "capabilities.request", empty.value(), false);
        self.live_reasoning = advertisesLive(described, "session.reasoning");
        self.live_compaction = advertises(described, "session.compact");
        self.live_policy = advertisesLive(described, "session.compaction.policy");
        self.live_steer = advertises(described, "session.message.delivery.steer");

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
        if (self.hub != null) {
            if (settings.model) |model| try settings_map.put("model", .{ .string = try modelRef(a, model) });
        }
        var metadata = Map.init(a);
        try metadata.put(oapx_adapter.settings_key, settings_map.value());
        var open = Map.init(a);
        try open.put("metadata", metadata.value());
        const opened = self.exchange(a, "session.open.request", open.value(), true) catch |err| retry: {
            if (err != error.OapRequestRefused or self.hub == null or settings.model == null) return err;
            _ = settings_map.map.swapRemove("model");
            try metadata.put(oapx_adapter.settings_key, settings_map.value());
            try open.put("metadata", metadata.value());
            const reopened = try self.exchange(a, "session.open.request", open.value(), true);
            const message = try std.fmt.allocPrint(self.allocator, "the hub refused to open the session on {s}, so it runs on the hub's default model", .{try modelRef(a, settings.model.?)});
            self.deliver(.{ .system_warning = .{ .message = OwnedSlice(u8).initOwned(message) } });
            break :retry reopened;
        };
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

        if (settings.model != null and self.hub == null) {
            const wanted = try modelRef(a, settings.model.?);
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
        try self.sendLine(line);
        var attempts: usize = 0;
        while (attempts < startup_attempts) : (attempts += 1) {
            while (self.popLine()) |answer| {
                defer self.allocator.free(answer);
                const parsed = std.json.parseFromSliceLeaky(std.json.Value, a, try a.dupe(u8, answer), .{}) catch continue;
                if (parsed != .object) continue;
                const reply_to = parsed.object.get("in_reply_to") orelse continue;
                if (reply_to != .string or !std.mem.eql(u8, reply_to.string, id)) continue;
                const answered = parsed.object.get("type") orelse continue;
                if (answered == .string and std.mem.eql(u8, answered.string, "error.response")) return error.OapRequestRefused;
                return parsed;
            }
            _ = try self.pumpLink();
            compat.time.sleepNs(std.time.ns_per_ms);
        }
        return error.OapRequestUnanswered;
    }

    fn submit(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        const payload = try self.submission(a, text, "auto");
        const kept_text = try self.allocator.dupe(u8, text);
        self.lockInbound();
        self.allocator.free(self.submitted);
        self.submitted = kept_text;
        self.allocator.free(self.run_id);
        self.run_id = &.{};
        self.cancel_pending.store(false, .release);
        self.cancelling = false;
        self.inbound_mutex.unlock();
        self.turn_open.store(true, .release);
        errdefer self.turn_open.store(false, .release);
        _ = try self.enqueue(a, "session.message.submit.request", "submit", payload, null);
    }

    fn followUp(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        if (self.compacting) return error.CompactionInProgress;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        const payload = try self.submission(a, text, "queue");
        const id = try self.reserveId(a, "queue");
        const kept_text = try self.allocator.dupe(u8, text);
        const kept_id = self.allocator.dupe(u8, id) catch |err| {
            self.allocator.free(kept_text);
            return err;
        };
        self.lockInbound();
        if (!self.turn_open.load(.acquire) or self.cancelling) {
            self.inbound_mutex.unlock();
            self.allocator.free(kept_text);
            self.allocator.free(kept_id);
            return error.AgentAlreadyStreaming;
        }
        self.queued_runs.append(self.allocator, .{ .text = kept_text, .submit_id = kept_id }) catch |err| {
            self.inbound_mutex.unlock();
            self.allocator.free(kept_text);
            self.allocator.free(kept_id);
            return err;
        };
        self.inbound_mutex.unlock();
        self.enqueueWithId(a, "session.message.submit.request", id, payload, null) catch |err| {
            self.lockInbound();
            var dropped = self.queued_runs.pop().?;
            self.inbound_mutex.unlock();
            dropped.deinit(self.allocator);
            return err;
        };
    }

    fn steer(ctx: *anyopaque, text: []const u8) anyerror!void {
        const self = cast(ctx);
        if (!self.live_steer) return error.UnavailableOverOap;
        if (self.compacting) return error.CompactionInProgress;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        var payload = try self.submission(a, text, "steer");
        const id = try self.reserveId(a, "steer");
        const kept_text = try self.allocator.dupe(u8, text);
        const kept_id = self.allocator.dupe(u8, id) catch |err| {
            self.allocator.free(kept_text);
            return err;
        };
        const target = self.currentRunId(a) catch |err| {
            self.allocator.free(kept_text);
            self.allocator.free(kept_id);
            return err;
        };
        if (target.len > 0) payload.object.put(a, "target_run_id", .{ .string = target }) catch |err| {
            self.allocator.free(kept_text);
            self.allocator.free(kept_id);
            return err;
        };
        self.lockInbound();
        if (!self.turn_open.load(.acquire) or self.cancelling) {
            self.inbound_mutex.unlock();
            self.allocator.free(kept_text);
            self.allocator.free(kept_id);
            return error.AgentAlreadyStreaming;
        }
        self.steers.append(self.allocator, .{ .text = kept_text, .request_id = kept_id }) catch |err| {
            self.inbound_mutex.unlock();
            self.allocator.free(kept_text);
            self.allocator.free(kept_id);
            return err;
        };
        self.inbound_mutex.unlock();
        self.enqueueWithId(a, "session.message.submit.request", id, payload, null) catch |err| {
            self.lockInbound();
            var dropped = self.steers.pop().?;
            self.inbound_mutex.unlock();
            dropped.deinit(self.allocator);
            return err;
        };
    }

    fn currentRunId(self: *OapExecution, a: std.mem.Allocator) ![]const u8 {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        return a.dupe(u8, self.run_id);
    }

    fn steersPending(ctx: *anyopaque) usize {
        const self = cast(ctx);
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        return self.steers.items.len;
    }

    fn steersSettled(ctx: *anyopaque) u64 {
        return cast(ctx).steers_settled.load(.acquire);
    }

    fn takeSteer(self: *OapExecution, request_id: []const u8) ?PendingSteer {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        for (self.steers.items, 0..) |pending, index| {
            if (!std.mem.eql(u8, pending.request_id, request_id)) continue;
            _ = self.steers_settled.fetchAdd(1, .acq_rel);
            return self.steers.orderedRemove(index);
        }
        return null;
    }

    fn clearQueued(ctx: *anyopaque) void {
        cast(ctx).dropQueued();
    }

    fn queuedCount(ctx: *anyopaque) usize {
        const self = cast(ctx);
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        var count: usize = 0;
        for (self.queued_runs.items) |queued| {
            if (!queued.dropped) count += 1;
        }
        return count;
    }

    fn dropQueued(self: *OapExecution) void {
        var cancels: std.ArrayList([]u8) = .empty;
        defer {
            for (cancels.items) |run_id| self.allocator.free(run_id);
            cancels.deinit(self.allocator);
        }
        self.lockInbound();
        var index: usize = 0;
        while (index < self.queued_runs.items.len) {
            const queued = &self.queued_runs.items[index];
            if (queued.run_id.len == 0) {
                queued.dropped = true;
                index += 1;
                continue;
            }
            var taken = self.queued_runs.orderedRemove(index);
            if (cancels.append(self.allocator, taken.run_id)) |_| {
                taken.run_id = &.{};
            } else |_| {}
            taken.deinit(self.allocator);
        }
        self.inbound_mutex.unlock();
        for (cancels.items) |run_id| self.cancelRun(run_id) catch {};
    }

    fn cancelRun(self: *OapExecution, run_id: []const u8) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("run_id", .{ .string = run_id });
        _ = try self.enqueue(a, "run.cancel.request", "cancel", payload.value(), run_id);
    }

    fn admitQueued(self: *OapExecution, submit_id: []const u8, run_id: []const u8) !void {
        const kept = try self.allocator.dupe(u8, run_id);
        var cancel_now = false;
        self.lockInbound();
        for (self.queued_runs.items, 0..) |*queued, index| {
            if (!std.mem.eql(u8, queued.submit_id, submit_id)) continue;
            if (queued.dropped) {
                var taken = self.queued_runs.orderedRemove(index);
                taken.deinit(self.allocator);
                cancel_now = true;
                break;
            }
            self.allocator.free(queued.run_id);
            queued.run_id = kept;
            self.inbound_mutex.unlock();
            return;
        }
        self.inbound_mutex.unlock();
        defer self.allocator.free(kept);
        if (cancel_now) try self.cancelRun(kept);
    }

    fn refuseQueued(self: *OapExecution, submit_id: []const u8) void {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        for (self.queued_runs.items, 0..) |queued, index| {
            if (!std.mem.eql(u8, queued.submit_id, submit_id)) continue;
            var taken = self.queued_runs.orderedRemove(index);
            taken.deinit(self.allocator);
            return;
        }
    }

    fn takePromoted(self: *OapExecution, run_id: []const u8) ?[]u8 {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        if (self.queued_runs.items.len == 0) return null;
        const head = &self.queued_runs.items[0];
        if (head.dropped or head.run_id.len == 0 or !std.mem.eql(u8, head.run_id, run_id)) return null;
        var taken = self.queued_runs.orderedRemove(0);
        const text = taken.text;
        taken.text = &.{};
        taken.deinit(self.allocator);
        return text;
    }

    fn holdOrClose(self: *OapExecution) bool {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        for (self.queued_runs.items) |queued| {
            if (!queued.dropped) return true;
        }
        self.turn_open.store(false, .release);
        return false;
    }

    fn closeTurn(self: *OapExecution) void {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        self.turn_open.store(false, .release);
    }

    fn isCurrentRun(self: *OapExecution, body: std.json.ObjectMap) bool {
        const named = stringOf(body, "run_id") orelse return true;
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        return self.run_id.len == 0 or std.mem.eql(u8, named, self.run_id);
    }

    fn submission(self: *OapExecution, a: std.mem.Allocator, text: []const u8, delivery: []const u8) !std.json.Value {
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
        try payload.put("delivery", .{ .string = delivery });
        return payload.value();
    }

    fn cancel(ctx: *anyopaque) void {
        const self = cast(ctx);
        self.lockInbound();
        self.cancelling = true;
        self.inbound_mutex.unlock();
        if (self.awaiting_promotion.load(.acquire)) {
            self.dropQueued();
            return;
        }
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
        _ = try self.enqueue(a, "run.cancel.request", "cancel", payload.value(), run_id);
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
        _ = try self.enqueue(a, "session.model.switch.request", "switch", payload.value(), null);
    }

    fn setReasoning(ctx: *anyopaque, level: ai_types.ThinkingLevel) anyerror!void {
        const self = cast(ctx);
        if (!self.live_reasoning) return error.UnavailableOverOap;
        if (self.turn_open.load(.acquire) or queuedCount(ctx) > 0) return error.RunInProgress;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("reasoning_level", .{ .string = @tagName(level) });
        _ = try self.enqueue(a, "session.settings.update.request", "settings", payload.value(), null);
    }

    fn compacts(ctx: *anyopaque) bool {
        return cast(ctx).live_compaction;
    }

    fn compactable(ctx: *anyopaque) bool {
        return cast(ctx).has_history.load(.acquire);
    }

    fn compact(ctx: *anyopaque, focus: []const u8) anyerror!void {
        const self = cast(ctx);
        if (!self.live_compaction) return error.UnavailableOverOap;
        if (self.turn_open.load(.acquire) or queuedCount(ctx) > 0) return error.RunInProgress;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        if (focus.len > 0) try payload.put("focus", .{ .string = focus });
        self.lockInbound();
        self.allocator.free(self.run_id);
        self.run_id = &.{};
        self.compacting = true;
        self.dropCompactionResult();
        self.cancel_pending.store(false, .release);
        self.cancelling = false;
        self.inbound_mutex.unlock();
        self.turn_open.store(true, .release);
        errdefer {
            self.turn_open.store(false, .release);
            self.compacting = false;
        }
        _ = try self.enqueue(a, "session.compact.request", "compact", payload.value(), null);
    }

    fn setCompactionPolicy(ctx: *anyopaque, policy_json: []const u8) anyerror!void {
        const self = cast(ctx);
        if (!self.live_policy) return error.UnavailableOverOap;
        self.lockInbound();
        const already = std.mem.eql(u8, self.sent_policy, policy_json);
        self.inbound_mutex.unlock();
        if (already) return;
        if (self.turn_open.load(.acquire) or queuedCount(ctx) > 0) return error.RunInProgress;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        var payload = Map.init(a);
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("compaction_policy", try std.json.parseFromSliceLeaky(std.json.Value, a, policy_json, .{}));
        const kept = try self.allocator.dupe(u8, policy_json);
        errdefer self.allocator.free(kept);
        _ = try self.enqueue(a, "session.settings.update.request", "settings", payload.value(), null);
        self.lockInbound();
        self.allocator.free(self.sent_policy);
        self.sent_policy = kept;
        self.inbound_mutex.unlock();
    }

    fn settleCompaction(self: *OapExecution, outcome: CompactionOutcome, message: []const u8) !void {
        const ended = self.compaction_result orelse CompactionEnd{ .outcome = outcome, .message = try self.ownedText(message) };
        self.compaction_result = null;
        if (ended.outcome == .completed) self.has_history.store(false, .release);
        self.compacting = false;
        self.awaiting_promotion.store(false, .release);
        self.turn_open.store(false, .release);
        self.deliver(.{ .compaction_end = ended });
    }

    fn dropCompactionResult(self: *OapExecution) void {
        const held = self.compaction_result orelse return;
        self.compaction_result = null;
        var event: TuiEvent = .{ .compaction_end = held };
        event.deinit(self.allocator);
    }

    fn decideApproval(ctx: *anyopaque, tool_call_id: []const u8, decision: tui_runtime.ToolApprovalDecision) anyerror!void {
        const self = cast(ctx);
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const a = scratch.allocator();
        self.lockInbound();
        const held = self.pending_permission orelse {
            self.inbound_mutex.unlock();
            return error.ToolApprovalNotPending;
        };
        if (!std.mem.eql(u8, held.tool_call_id, tool_call_id)) {
            self.inbound_mutex.unlock();
            return error.ToolApprovalNotPending;
        }
        var pending = held;
        self.pending_permission = null;
        self.inbound_mutex.unlock();
        defer pending.deinit(self.allocator);
        var payload = Map.init(a);
        try payload.put("interaction_id", .{ .string = pending.interaction_id });
        try payload.put("requested_by", .{ .string = pending.requested_by });
        try payload.put("responded_by", .{ .string = pending.responded_by });
        try payload.put("session_id", .{ .string = self.session_id });
        try payload.put("run_id", .{ .string = pending.run_id });
        const granted = decision == .approve or decision == .approve_always;
        const choice: []const u8 = switch (decision) {
            .approve => "approve",
            .reject => "deny",
            .approve_always => if (pending.offers_approve_always) "approve_always" else "approve",
            .reject_always => if (pending.offers_reject_always) "reject_always" else "deny",
        };
        try payload.put("granted", .{ .bool = granted });
        try payload.put("choice_id", .{ .string = choice });
        _ = try self.enqueue(a, "action.permission.resolve.request", "resolve", payload.value(), pending.run_id);
    }

    fn forgetPermission(self: *OapExecution) void {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        if (self.pending_permission) |*pending| pending.deinit(self.allocator);
        self.pending_permission = null;
    }

    fn holdPermission(self: *OapExecution, body: std.json.ObjectMap) !void {
        const interaction_id = try self.allocator.dupe(u8, stringOf(body, "interaction_id") orelse "");
        errdefer self.allocator.free(interaction_id);
        const tool_call_id = try self.allocator.dupe(u8, stringOf(body, "tool_call_id") orelse "");
        errdefer self.allocator.free(tool_call_id);
        const requested_by = try self.allocator.dupe(u8, stringOf(body, "requested_by") orelse "");
        errdefer self.allocator.free(requested_by);
        const responded_by = try self.allocator.dupe(u8, stringOf(body, "responded_by") orelse "");
        errdefer self.allocator.free(responded_by);
        const run_id = try self.allocator.dupe(u8, stringOf(body, "run_id") orelse "");
        errdefer self.allocator.free(run_id);
        self.forgetPermission();
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        self.pending_permission = .{
            .interaction_id = interaction_id,
            .tool_call_id = tool_call_id,
            .requested_by = requested_by,
            .responded_by = responded_by,
            .run_id = run_id,
            .offers_approve_always = offersChoice(body, "approve_always"),
            .offers_reject_always = offersChoice(body, "reject_always"),
        };
    }

    fn offersChoice(body: std.json.ObjectMap, id: []const u8) bool {
        const choices = body.get("choices") orelse return false;
        if (choices != .array) return false;
        for (choices.array.items) |choice| {
            if (choice != .object) continue;
            const named = choice.object.get("id") orelse continue;
            if (named == .string and std.mem.eql(u8, named.string, id)) return true;
        }
        return false;
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

    fn enqueue(self: *OapExecution, a: std.mem.Allocator, kind: []const u8, prefix: []const u8, payload: std.json.Value, run_id: ?[]const u8) ![]u8 {
        const id = try self.reserveId(a, prefix);
        try self.enqueueWithId(a, kind, id, payload, run_id);
        return id;
    }

    fn reserveId(self: *OapExecution, a: std.mem.Allocator, prefix: []const u8) ![]u8 {
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        return self.nextId(a, prefix);
    }

    fn enqueueWithId(self: *OapExecution, a: std.mem.Allocator, kind: []const u8, id: []const u8, payload: std.json.Value, run_id: ?[]const u8) !void {
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
            try self.sendLine(line);
            moved = true;
        }
        if (try self.pumpLink()) moved = true;
        while (self.popLine()) |line| {
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
                self.dropQueued();
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
            if (std.mem.startsWith(u8, reply_to, "settings-")) {
                self.lockInbound();
                self.allocator.free(self.sent_policy);
                self.sent_policy = &.{};
                self.inbound_mutex.unlock();
            }
            if (std.mem.startsWith(u8, reply_to, "compact-")) {
                try self.settleCompaction(.failed, message);
            } else if (std.mem.startsWith(u8, reply_to, "submit-")) {
                self.deliver(.{ .@"error" = .{ .message = try self.ownedText(message) } });
                self.closeTurn();
                self.dropQueued();
                self.endTurn(.@"error");
            } else if (std.mem.startsWith(u8, reply_to, "steer-")) {
                if (self.takeSteer(reply_to)) |taken| {
                    var pending = taken;
                    pending.deinit(self.allocator);
                }
                const note = try std.fmt.allocPrint(self.allocator, "the steer was not applied: {s}", .{message});
                self.deliver(.{ .system_warning = .{ .message = OwnedSlice(u8).initOwned(note) } });
            } else if (std.mem.startsWith(u8, reply_to, "queue-")) {
                self.refuseQueued(reply_to);
                self.deliver(.{ .system_warning = .{ .message = try self.ownedText(message) } });
                if (self.awaiting_promotion.load(.acquire) and !self.holdOrClose()) self.endTurn(.completed);
            } else {
                self.deliver(.{ .system_warning = .{ .message = try self.ownedText(message) } });
            }
            return;
        }
        const body = payload orelse return;
        if (std.mem.eql(u8, kind, "session.message.submit.response")) {
            const reply_to = stringOf(root, "in_reply_to") orelse "";
            if (std.mem.startsWith(u8, reply_to, "steer-")) {
                self.lockInbound();
                defer self.inbound_mutex.unlock();
                for (self.steers.items) |*pending| {
                    if (std.mem.eql(u8, pending.request_id, reply_to)) pending.admitted = true;
                }
                return;
            }
            if (!std.mem.startsWith(u8, reply_to, "queue-")) return;
            try self.admitQueued(reply_to, stringOf(body, "run_id") orelse "");
            return;
        }
        if (self.compacting) {
            if (try self.translateCompaction(kind, body)) return;
        } else if (std.mem.eql(u8, kind, "run.compaction.started")) {
            self.deliver(.{ .compaction_start = .{ .in_run = true } });
            return;
        } else if (std.mem.eql(u8, kind, "run.compaction.ended")) {
            self.deliver(.{ .compaction_end = try self.compactionEnd(body, true) });
            return;
        }
        if (std.mem.eql(u8, kind, "run.started")) {
            self.has_history.store(true, .release);
            const run_id = stringOf(body, "run_id") orelse "";
            if (self.turn_open.load(.acquire)) {
                if (self.takePromoted(run_id)) |text| {
                    self.awaiting_promotion.store(false, .release);
                    defer self.allocator.free(text);
                    const kept = try self.allocator.dupe(u8, run_id);
                    self.lockInbound();
                    self.allocator.free(self.run_id);
                    self.run_id = kept;
                    self.inbound_mutex.unlock();
                    self.text.clearRetainingCapacity();
                    self.in_assistant = false;
                    self.closed_messages = 0;
                    self.deliver(.{ .message_end = .{ .role = .user, .text = try self.ownedText(text) } });
                    self.deliver(.{ .turn_start = .{} });
                    if (self.cancel_pending.swap(false, .acq_rel)) try self.sendCancel();
                    return;
                }
            }
            const kept = try self.allocator.dupe(u8, run_id);
            self.lockInbound();
            self.allocator.free(self.run_id);
            self.run_id = kept;
            self.inbound_mutex.unlock();
            self.text.clearRetainingCapacity();
            self.in_assistant = false;
            self.closed_messages = 0;
            self.deliver(.{ .agent_start = .{} });
            self.lockInbound();
            const echoed = self.submitted;
            self.submitted = &.{};
            self.inbound_mutex.unlock();
            if (echoed.len > 0) {
                defer self.allocator.free(echoed);
                self.deliver(.{ .message_end = .{ .role = .user, .text = try self.ownedText(echoed) } });
            }
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
        if (std.mem.eql(u8, kind, "run.steer.applied") or std.mem.eql(u8, kind, "run.steer.dropped")) {
            var pending = self.takeSteer(stringOf(body, "request_id") orelse "") orelse return;
            defer pending.deinit(self.allocator);
            if (std.mem.eql(u8, kind, "run.steer.dropped")) {
                if (self.cancelling) return;
                const reason = if (body.get("reason")) |value| (if (value == .object) errorMessage(value.object) else "the run ended first") else "the run ended first";
                const note = try std.fmt.allocPrint(self.allocator, "the steer was not applied: {s}", .{reason});
                self.deliver(.{ .system_warning = .{ .message = OwnedSlice(u8).initOwned(note) } });
                return;
            }
            try self.closeAssistant(.stop);
            self.deliver(.{ .message_end = .{ .role = .user, .text = try self.ownedText(pending.text), .steering = true } });
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
        if (std.mem.eql(u8, kind, "action.permission.requested")) {
            try self.closeAssistant(.tool_use);
            try self.holdPermission(body);
            const arguments = try jsonText(self.allocator, body.get("arguments_json"));
            defer self.allocator.free(arguments);
            var call_id = try self.ownedText(stringOf(body, "tool_call_id") orelse "");
            errdefer call_id.deinit(self.allocator);
            var name = try self.ownedText(stringOf(body, "title") orelse "");
            errdefer name.deinit(self.allocator);
            const args_json = try self.ownedText(arguments);
            self.deliver(.{ .tool_approval_requested = .{ .tool_call_id = call_id, .tool_name = name, .args_json = args_json } });
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
        if (std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled")) {
            if (!self.turn_open.load(.acquire)) return;
            if (!self.isCurrentRun(body)) {
                if (std.mem.eql(u8, kind, "run.failed")) self.deliver(.{ .system_warning = .{ .message = try self.ownedText(errorMessage(body)) } });
                self.settleReserved(stringOf(body, "run_id") orelse "");
                if (self.awaiting_promotion.load(.acquire) and !self.holdOrClose()) self.endTurn(.completed);
                return;
            }
            self.output_tokens = if (self.closed_messages == 0) usageTokens(body) else 0;
            if (contextTokens(root)) |tokens| self.deliver(.{ .context_usage = .{ .estimated_tokens = tokens } });
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
            if (self.holdOrClose()) {
                self.awaiting_promotion.store(true, .release);
                return;
            }
            self.endTurn(.completed);
            return;
        }
        if (std.mem.eql(u8, kind, "run.failed")) {
            if (!self.turn_open.load(.acquire)) return;
            try self.closeAssistant(.@"error");
            const message = if (body.get("error")) |value| (if (value == .object) errorMessage(value.object) else "the run failed") else "the run failed";
            self.deliver(.{ .@"error" = .{ .message = try self.ownedText(message) } });
            self.closeTurn();
            self.dropQueued();
            self.endTurn(.@"error");
            return;
        }
        if (std.mem.eql(u8, kind, "run.cancelled")) {
            if (!self.turn_open.load(.acquire)) return;
            try self.closeAssistant(.aborted);
            self.closeTurn();
            self.dropQueued();
            self.endTurn(.cancelled);
            return;
        }
    }

    fn translateCompaction(self: *OapExecution, kind: []const u8, body: std.json.ObjectMap) !bool {
        if (std.mem.eql(u8, kind, "run.started")) {
            const kept = try self.allocator.dupe(u8, stringOf(body, "run_id") orelse "");
            self.lockInbound();
            self.allocator.free(self.run_id);
            self.run_id = kept;
            self.inbound_mutex.unlock();
            if (self.cancel_pending.swap(false, .acq_rel)) try self.sendCancel();
            return true;
        }
        if (std.mem.eql(u8, kind, "run.compaction.started")) return true;
        if (std.mem.eql(u8, kind, "run.compaction.ended")) {
            if (self.compaction_result == null) self.compaction_result = try self.compactionEnd(body, false);
            return true;
        }
        if (std.mem.eql(u8, kind, "run.completed")) {
            try self.settleCompaction(.completed, "");
            return true;
        }
        if (std.mem.eql(u8, kind, "run.failed")) {
            try self.settleCompaction(.failed, if (body.get("error")) |value| (if (value == .object) errorMessage(value.object) else "the compaction failed") else "the compaction failed");
            return true;
        }
        if (std.mem.eql(u8, kind, "run.cancelled")) {
            try self.settleCompaction(.cancelled, "");
            return true;
        }
        return false;
    }

    fn compactionEnd(self: *OapExecution, body: std.json.ObjectMap, in_run: bool) !CompactionEnd {
        const outcome_text = stringOf(body, "outcome") orelse "failed";
        const outcome = std.meta.stringToEnum(CompactionOutcome, outcome_text) orelse .failed;
        const summary = if (body.get("summary")) |value| (if (value == .object) stringOf(value.object, "content") orelse "" else "") else "";
        const failure = if (body.get("error")) |value| (if (value == .object) errorMessage(value.object) else "") else "";
        var text = try self.ownedText(summary);
        errdefer text.deinit(self.allocator);
        const message = try self.ownedText(failure);
        const tokens_after: u64 = if (body.get("history_tokens")) |value| (if (value == .integer and value.integer > 0) @intCast(value.integer) else 0) else 0;
        return .{ .in_run = in_run, .outcome = outcome, .text = text, .message = message, .tokens_after = tokens_after };
    }

    fn settleReserved(self: *OapExecution, run_id: []const u8) void {
        if (run_id.len == 0) return;
        self.lockInbound();
        defer self.inbound_mutex.unlock();
        for (self.queued_runs.items, 0..) |queued, index| {
            if (!std.mem.eql(u8, queued.run_id, run_id)) continue;
            var taken = self.queued_runs.orderedRemove(index);
            taken.deinit(self.allocator);
            return;
        }
    }

    fn endTurn(self: *OapExecution, reason: tui_session.TuiEndReason) void {
        self.lockInbound();
        for (self.steers.items) |*pending| pending.deinit(self.allocator);
        _ = self.steers_settled.fetchAdd(self.steers.items.len, .acq_rel);
        self.steers.clearRetainingCapacity();
        self.inbound_mutex.unlock();
        self.compacting = false;
        self.dropCompactionResult();
        self.awaiting_promotion.store(false, .release);
        self.turn_open.store(false, .release);
        self.output_tokens = 0;
        self.closed_messages = 0;
        self.forgetPermission();
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
        const output_tokens = self.output_tokens;
        self.output_tokens = 0;
        self.closed_messages += 1;
        self.deliver(.{ .message_end = .{ .role = .assistant, .text = try self.ownedText(self.text.items), .stop_reason = stop_reason, .output_tokens = output_tokens } });
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

fn advertises(described: std.json.Value, key: []const u8) bool {
    if (described != .object) return false;
    const payload = described.object.get("payload") orelse return false;
    if (payload != .object) return false;
    const features = payload.object.get("features") orelse return false;
    if (features != .object) return false;
    const feature = features.object.get(key) orelse return false;
    if (feature != .object) return false;
    const level = stringOf(feature.object, "level") orelse return false;
    return !std.mem.eql(u8, level, "unavailable");
}

fn advertisesLive(described: std.json.Value, key: []const u8) bool {
    if (described != .object) return false;
    const payload = described.object.get("payload") orelse return false;
    if (payload != .object) return false;
    const features = payload.object.get("features") orelse return false;
    if (features != .object) return false;
    const feature = features.object.get(key) orelse return false;
    if (feature != .object) return false;
    const level = stringOf(feature.object, "level") orelse return false;
    if (std.mem.eql(u8, level, "unavailable")) return false;
    const modes = feature.object.get("modes") orelse return false;
    if (modes != .array) return false;
    for (modes.array.items) |mode| {
        if (mode == .string and std.mem.eql(u8, mode.string, "session_live")) return true;
    }
    return false;
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

fn usageTokens(body: std.json.ObjectMap) u64 {
    const usage = body.get("usage") orelse return 0;
    if (usage != .object) return 0;
    const tokens = usage.object.get("output_tokens") orelse return 0;
    return if (tokens == .integer and tokens.integer > 0) @intCast(tokens.integer) else 0;
}

fn contextTokens(root: std.json.ObjectMap) ?u64 {
    const extensions = root.get("extensions") orelse return null;
    if (extensions != .object) return null;
    const ours = extensions.object.get(oapx_adapter.settings_key) orelse return null;
    if (ours != .object) return null;
    const tokens = ours.object.get("context_tokens") orelse return null;
    return if (tokens == .integer and tokens.integer >= 0) @intCast(tokens.integer) else null;
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
    stop_reason: ai_types.StopReason = .stop,
    tool_first: bool = false,
    calls: usize = 0,
    output_tokens: u64 = 0,
    preamble: []const u8 = "",
    hold_first: bool = false,
    released: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    last_thinking: ai_types.ThinkingLevel = .off,
};

fn scriptedMessage(allocator: std.mem.Allocator, text: []const u8, reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const blocks = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(blocks);
    blocks[0] = .{ .text = .{ .text = try allocator.dupe(u8, text) } };
    return .{ .content = blocks, .api = scripted_model.api, .provider = scripted_model.provider, .model = scripted_model.id, .usage = .{}, .stop_reason = reason, .timestamp = 0 };
}

fn toolCallMessage(allocator: std.mem.Allocator, preamble: []const u8) !ai_types.AssistantMessage {
    const blocks = try allocator.alloc(ai_types.AssistantContent, if (preamble.len > 0) 2 else 1);
    errdefer allocator.free(blocks);
    const said = try allocator.dupe(u8, preamble);
    errdefer allocator.free(said);
    const id = try allocator.dupe(u8, "call-1");
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, "echo_tool");
    errdefer allocator.free(name);
    const arguments = try allocator.dupe(u8, "{}");
    blocks[blocks.len - 1] = .{ .tool_call = .{ .id = id, .name = name, .arguments_json = arguments } };
    if (preamble.len > 0) blocks[0] = .{ .text = .{ .text = said } } else allocator.free(said);
    return .{ .content = blocks, .api = scripted_model.api, .provider = scripted_model.provider, .model = scripted_model.id, .usage = .{}, .stop_reason = .tool_use, .timestamp = 0 };
}

var echo_runs = std.atomic.Value(usize).init(0);

fn echoTool(tool_call_id: []const u8, args_json: []const u8, cancel_token: ?ai_types.CancelToken, on_update_ctx: ?*anyopaque, on_update: ?agent.ToolUpdateCallback, allocator: std.mem.Allocator) anyerror!agent.AgentToolResult {
    _ = tool_call_id;
    _ = args_json;
    _ = cancel_token;
    _ = on_update_ctx;
    _ = on_update;
    _ = echo_runs.fetchAdd(1, .acq_rel);
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    errdefer allocator.free(content);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "echoed") } };
    return .{ .content = @FieldType(agent.AgentToolResult, "content").initOwned(content) };
}

const echo_tools = [_]agent.AgentTool{.{
    .label = "Echo",
    .name = "echo_tool",
    .description = "Echo a word back",
    .parameters_schema_json = "{\"type\":\"object\"}",
    .execute = echoTool,
}};

fn bareMessage(reason: ai_types.StopReason) ai_types.AssistantMessage {
    return .{ .content = &.{}, .api = scripted_model.api, .provider = scripted_model.provider, .model = scripted_model.id, .usage = .{}, .stop_reason = reason, .timestamp = 0 };
}

fn scriptedStream(ctx: ?*anyopaque, model: ai_types.Model, context: ai_types.Context, options: agent.ProtocolOptions, allocator: std.mem.Allocator) anyerror!*event_stream.AssistantMessageEventStream {
    _ = model;
    _ = context;
    const script: *Script = @ptrCast(@alignCast(ctx.?));
    script.last_thinking = options.thinking_level;
    script.calls += 1;
    if (script.hold_first and script.calls == 1) {
        var waits: usize = 0;
        while (!script.released.load(.acquire) and waits < 5000) : (waits += 1) {
            std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
        }
    }
    const stream = try allocator.create(event_stream.AssistantMessageEventStream);
    stream.* = event_stream.AssistantMessageEventStream.init(allocator);
    if (script.tool_first and script.calls == 1) {
        try stream.push(.{ .start = .{ .partial = bareMessage(.tool_use) } });
        if (script.preamble.len > 0) try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = script.preamble, .partial = bareMessage(.tool_use) } });
        try stream.push(.{ .done = .{ .reason = .tool_use, .message = try toolCallMessage(allocator, script.preamble) } });
        stream.complete(try toolCallMessage(allocator, script.preamble));
        return stream;
    }
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
    const partial = bareMessage(script.stop_reason);
    try stream.push(.{ .start = .{ .partial = partial } });
    try stream.push(.{ .text_delta = .{ .content_index = 0, .delta = script.reply, .partial = partial } });
    var done = try scriptedMessage(allocator, script.reply, script.stop_reason);
    done.usage.output = script.output_tokens;
    try stream.push(.{ .done = .{ .reason = script.stop_reason, .message = done } });
    var settled = try scriptedMessage(allocator, script.reply, script.stop_reason);
    settled.usage.output = script.output_tokens;
    stream.complete(settled);
    return stream;
}

const Seen = struct {
    text: std.ArrayList(u8) = .empty,
    final_text: std.ArrayList(u8) = .empty,
    end: ?tui_session.TuiEndReason = null,
    agent_starts: usize = 0,
    warnings: usize = 0,
    output_limit_warned: bool = false,
    output_tokens: u64 = 0,
    context_tokens: ?u64 = null,
    user_text: std.ArrayList(u8) = .empty,

    fn deinit(self: *Seen) void {
        self.text.deinit(testing.allocator);
        self.final_text.deinit(testing.allocator);
        self.user_text.deinit(testing.allocator);
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
                .system_warning => |payload| {
                    seen.warnings += 1;
                    if (std.mem.indexOf(u8, payload.message.slice(), "output token limit") != null) seen.output_limit_warned = true;
                },
                .text_delta => |payload| try seen.text.appendSlice(testing.allocator, payload.delta.slice()),
                .context_usage => |payload| seen.context_tokens = payload.estimated_tokens,
                .message_end => |payload| if (payload.role == .assistant) {
                    seen.output_tokens = payload.output_tokens;
                    seen.final_text.clearRetainingCapacity();
                    try seen.final_text.appendSlice(testing.allocator, payload.text.slice());
                } else if (payload.role == .user) {
                    try seen.user_text.appendSlice(testing.allocator, payload.text.slice());
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

test "a turn over OAP reports its output tokens and the context it filled to the status bar" {
    var script = Script{ .output_tokens = 42 };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .off);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("count");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(u64, 42), seen.output_tokens);
    try testing.expect(seen.context_tokens != null);
    try testing.expect(seen.context_tokens.? > 0);
}

test "a run over OAP with an earlier assistant message leaves its output tokens to the estimate rather than counting them twice" {
    var script = Script{ .output_tokens = 42, .tool_first = true, .preamble = "let me check" };
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
        .tools = &echo_tools,
    });
    defer execution.destroy();
    var runtime = try tui_runtime.TuiRuntime.init(testing.allocator, .{
        .models = &models,
        .initial_model_id = scripted_model.id,
        .remote = execution.remote(),
    });
    defer runtime.deinit();

    try runtime.submitTurn("use the tool");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqualStrings("over the wire", seen.final_text.items);
    try testing.expectEqual(@as(u64, 0), seen.output_tokens);
}

test "a reply over OAP that stops at its output token limit warns the user as the local runtime does" {
    var script = Script{ .stop_reason = .length };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .off);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("long");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expect(seen.output_limit_warned);

    script.stop_reason = .stop;
    try runtime.submitTurn("short");
    var next = Seen{};
    defer next.deinit();
    try drainTurn(&runtime, &next);
    try testing.expectEqual(@as(usize, 0), next.warnings);
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

fn waitForRun(execution: *OapExecution) void {
    var waits: usize = 0;
    while (waits < 2000) : (waits += 1) {
        execution.lockInbound();
        const known = execution.run_id.len > 0;
        execution.inbound_mutex.unlock();
        if (known) return;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
}

fn waitForReservation(execution: *OapExecution) void {
    var waits: usize = 0;
    while (waits < 2000) : (waits += 1) {
        execution.lockInbound();
        const admitted = execution.queued_runs.items.len > 0 and execution.queued_runs.items[0].run_id.len > 0;
        execution.inbound_mutex.unlock();
        if (admitted) return;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
}

fn waitForSteer(execution: *OapExecution) void {
    var waits: usize = 0;
    while (waits < 2000) : (waits += 1) {
        execution.lockInbound();
        const admitted = execution.steers.items.len > 0 and execution.steers.items[0].admitted;
        execution.inbound_mutex.unlock();
        if (admitted) return;
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
}

test "a follow-up queued during a turn over OAP runs after it inside the same turn" {
    var script = Script{ .hold_first = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("first");
    waitForRun(execution);
    try runtime.followUp("second");
    try testing.expectEqual(@as(usize, 1), runtime.queuedCounts().follow_up);
    waitForReservation(execution);
    script.released.store(true, .release);

    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(usize, 1), seen.agent_starts);
    try testing.expectEqual(@as(usize, 2), script.calls);
    try testing.expectEqualStrings("firstsecond", seen.user_text.items);
    try testing.expectEqualStrings("over the wireover the wire", seen.text.items);
    try testing.expectEqual(@as(usize, 0), runtime.queuedCounts().follow_up);
}

test "clearing a follow-up queued over OAP cancels its reservation, so the turn ends after the first run" {
    var script = Script{ .hold_first = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("first");
    waitForRun(execution);
    try runtime.followUp("second");
    waitForReservation(execution);
    runtime.clearQueuedMessages();
    try testing.expectEqual(@as(usize, 0), runtime.queuedCounts().follow_up);
    script.released.store(true, .release);

    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(usize, 1), script.calls);
    try testing.expectEqualStrings("first", seen.user_text.items);

    try runtime.submitTurn("third");
    var next = Seen{};
    defer next.deinit();
    try drainTurn(&runtime, &next);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), next.end);
    var settle: usize = 0;
    while (settle < 300 and script.calls <= 2) : (settle += 1) {
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    try testing.expectEqual(@as(usize, 2), script.calls);
}

test "a follow-up sent while no turn runs over OAP starts one" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    try runtime.followUp("now");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(usize, 1), script.calls);
}

test "the first message of a turn over OAP comes back as the user's message, as the local loop reports it" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("name this session");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqualStrings("name this session", seen.user_text.items);
}

test "a runtime over OAP refuses what the protocol path cannot carry yet" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
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
    try testing.expectError(error.UnavailableOverOap, runtime.setPermissionMode(.ask));
    try testing.expectError(error.UnavailableOverOap, runtime.setContextWindow(4096));
    try testing.expectError(error.UnavailableOverOap, runtime.setOutput(.max));
    try testing.expectError(error.UnavailableOverOap, runtime.setWorkspaceRoot("/elsewhere"));
    try testing.expectEqual(tui_runtime.PermissionMode.bypass, runtime.permissionMode());

    try runtime.submitTurn("go");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(ai_types.ThinkingLevel.high, script.last_thinking);
}

test "a thinking level changed on an open OAP session reaches the next run through a live settings update" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    try testing.expect(execution.live_reasoning);
    try runtime.setThinkingLevel(.max);
    try testing.expectEqual(ai_types.ThinkingLevel.max, runtime.thinkingLevel());

    try runtime.submitTurn("go");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(ai_types.ThinkingLevel.max, script.last_thinking);
}

test "a thinking level change is refused over OAP when the session advertises reasoning only at open" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    const described = try std.json.parseFromSlice(std.json.Value, testing.allocator, "{\"payload\":{\"features\":{\"session.reasoning\":{\"level\":\"native\",\"modes\":[\"session_open\"]}}}}", .{});
    defer described.deinit();
    execution.live_reasoning = advertisesLive(described.value, "session.reasoning");
    try testing.expect(!execution.live_reasoning);
    try testing.expectError(error.UnavailableOverOap, runtime.setThinkingLevel(.max));
    try testing.expectEqual(ai_types.ThinkingLevel.low, runtime.thinkingLevel());
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

fn askedTurn(decision: tui_runtime.ToolApprovalDecision) !struct { end: ?tui_session.TuiEndReason, approvals: usize, ran: usize } {
    var script = Script{ .tool_first = true };
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
        .tools = &echo_tools,
    });
    defer execution.destroy();
    var runtime = try tui_runtime.TuiRuntime.init(testing.allocator, .{
        .models = &models,
        .initial_model_id = scripted_model.id,
        .remote = execution.remote(),
    });
    defer runtime.deinit();
    try runtime.setPermissionMode(.ask);
    const before = echo_runs.load(.acquire);

    try runtime.submitTurn("use the tool");
    var approvals: usize = 0;
    var waits: usize = 0;
    while (waits < 5000) : (waits += 1) {
        while (runtime.streamEvents().poll()) |event| {
            var owned_event = event;
            defer owned_event.deinit(testing.allocator);
            switch (owned_event) {
                .tool_approval_requested => |payload| {
                    approvals += 1;
                    try testing.expectEqualStrings("call-1", payload.tool_call_id.slice());
                    try testing.expectEqualStrings("echo_tool", payload.tool_name.slice());
                    try runtime.decideToolApproval(payload.tool_call_id.slice(), decision);
                    try testing.expectError(error.ToolApprovalNotPending, runtime.decideToolApproval(payload.tool_call_id.slice(), decision));
                },
                .agent_end => |payload| return .{ .end = payload.reason, .approvals = approvals, .ran = echo_runs.load(.acquire) - before },
                else => {},
            }
        }
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    return error.TestTurnNeverEnded;
}

test "an in-process session hands the loop's own records over, tool calls and results included, which the OAP events cannot rebuild" {
    var script = Script{ .tool_first = true };
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
        .tools = &echo_tools,
    });
    defer execution.destroy();
    var runtime = try tui_runtime.TuiRuntime.init(testing.allocator, .{
        .models = &models,
        .initial_model_id = scripted_model.id,
        .remote = execution.remote(),
    });
    defer runtime.deinit();
    try testing.expect(runtime.recordsFromEndpoint());

    try runtime.submitTurn("use the tool");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);

    var tool_calls: usize = 0;
    var tool_results: usize = 0;
    var ends: usize = 0;
    while (runtime.takeEndpointRecord()) |record| {
        var owned = record;
        defer owned.deinit(testing.allocator);
        switch (owned) {
            .message_end => |payload| {
                if (payload.role == .assistant and std.mem.indexOf(u8, payload.tool_calls_json.slice(), "call-1") != null) tool_calls += 1;
                if (payload.role == .tool_result and std.mem.eql(u8, payload.tool_call_id.slice(), "call-1")) tool_results += 1;
            },
            .agent_end => ends += 1,
            else => {},
        }
    }
    try testing.expectEqual(@as(usize, 1), tool_calls);
    try testing.expectEqual(@as(usize, 1), tool_results);
    try testing.expectEqual(@as(usize, 1), ends);
}

test "a session with no in-process endpoint has no records to hand over" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try testing.expect(runtime.recordsFromEndpoint());
    const adapter = execution.adapter.?;
    execution.adapter = null;
    defer execution.adapter = adapter;
    try testing.expect(!runtime.recordsFromEndpoint());
    try testing.expect(runtime.takeEndpointRecord() == null);
}

test "in ask mode over OAP the model's tool call waits for the user's approval and then runs" {
    const outcome = try askedTurn(.approve);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), outcome.end);
    try testing.expectEqual(@as(usize, 1), outcome.approvals);
    try testing.expectEqual(@as(usize, 1), outcome.ran);
}

test "in ask mode over OAP a denied tool call never runs and the turn still ends" {
    const outcome = try askedTurn(.reject);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), outcome.end);
    try testing.expectEqual(@as(usize, 1), outcome.approvals);
    try testing.expectEqual(@as(usize, 0), outcome.ran);
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

const Captured = struct {
    ends: usize = 0,
    user_messages: usize = 0,
    warnings: usize = 0,

    fn sink(self: *Captured) tui_runtime.EventSink {
        return .{ .ctx = self, .push = push };
    }

    fn push(ctx: *anyopaque, event: TuiEvent) void {
        const self: *Captured = @ptrCast(@alignCast(ctx));
        var owned = event;
        defer owned.deinit(testing.allocator);
        if (owned == .agent_end) self.ends += 1;
        if (owned == .system_warning) self.warnings += 1;
        if (owned == .message_end and owned.message_end.role == .user) self.user_messages += 1;
    }
};

test "a held turn ends when the reservation it waits on settles without ever starting" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var captured = Captured{};
    execution.sink = captured.sink();
    execution.run_id = try testing.allocator.dupe(u8, "run-1");
    execution.turn_open.store(true, .release);
    try execution.queued_runs.append(testing.allocator, .{
        .text = try testing.allocator.dupe(u8, "later"),
        .submit_id = try testing.allocator.dupe(u8, "queue-1"),
        .run_id = try testing.allocator.dupe(u8, "run-2"),
    });

    try execution.translateLine("{\"type\":\"run.completed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}");
    try testing.expectEqual(@as(usize, 0), captured.ends);
    try testing.expect(execution.turn_open.load(.acquire));

    try execution.translateLine("{\"type\":\"run.cancelled\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-2\"}}");
    try testing.expectEqual(@as(usize, 1), captured.ends);
    try testing.expect(!execution.turn_open.load(.acquire));
    try testing.expectEqual(@as(usize, 0), execution.queued_runs.items.len);
}

test "a reserved follow-up that fails before it starts says why instead of vanishing" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var captured = Captured{};
    execution.sink = captured.sink();
    execution.run_id = try testing.allocator.dupe(u8, "run-1");
    execution.turn_open.store(true, .release);
    try execution.queued_runs.append(testing.allocator, .{
        .text = try testing.allocator.dupe(u8, "later"),
        .submit_id = try testing.allocator.dupe(u8, "queue-1"),
        .run_id = try testing.allocator.dupe(u8, "run-2"),
    });

    try execution.translateLine("{\"type\":\"run.completed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}");
    try testing.expectEqual(@as(usize, 0), captured.warnings);
    try execution.translateLine("{\"type\":\"run.failed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-2\",\"error\":{\"code\":\"backend_failed\",\"message\":\"the follow-up could not start\"}}}");
    try testing.expectEqual(@as(usize, 1), captured.warnings);
    try testing.expectEqual(@as(usize, 1), captured.ends);
    try testing.expect(!execution.turn_open.load(.acquire));
}

test "a follow-up is refused once the turn is cancelling or closed, rather than queued where nothing will run it" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var captured = Captured{};
    execution.sink = captured.sink();
    execution.run_id = try testing.allocator.dupe(u8, "run-1");
    execution.turn_open.store(true, .release);

    OapExecution.cancel(execution);
    try testing.expectError(error.AgentAlreadyStreaming, OapExecution.followUp(execution, "after the abort"));
    try testing.expectEqual(@as(usize, 0), execution.queued_runs.items.len);

    execution.cancelling = false;
    try execution.translateLine("{\"type\":\"run.completed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}");
    try testing.expectEqual(@as(usize, 1), captured.ends);
    try testing.expectError(error.AgentAlreadyStreaming, OapExecution.followUp(execution, "after the end"));
    try testing.expectEqual(@as(usize, 0), execution.queued_runs.items.len);
}

test "a follow-up on a hub-attached session is sent to the hub as a queued submit" {
    const execution = try OapExecution.attach(testing.allocator, "http://127.0.0.1:1", "memory");
    defer execution.destroy();
    execution.turn_open.store(true, .release);
    try OapExecution.followUp(execution, "later");
    try testing.expectEqual(@as(usize, 1), execution.queued_runs.items.len);
    try testing.expectEqual(@as(usize, 1), execution.inbound.items.len);
    const sent = execution.inbound.items[0];
    try testing.expect(std.mem.indexOf(u8, sent, "\"delivery\":\"queue\"") != null);
    try testing.expect(std.mem.indexOf(u8, sent, execution.queued_runs.items[0].submit_id) != null);
}

test "a held turn ends when the endpoint refuses the last reservation it waits on" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var captured = Captured{};
    execution.sink = captured.sink();
    execution.run_id = try testing.allocator.dupe(u8, "run-1");
    execution.turn_open.store(true, .release);
    try execution.queued_runs.append(testing.allocator, .{
        .text = try testing.allocator.dupe(u8, "later"),
        .submit_id = try testing.allocator.dupe(u8, "queue-1"),
    });

    try execution.translateLine("{\"type\":\"run.completed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}");
    try testing.expectEqual(@as(usize, 0), captured.ends);
    try execution.translateLine("{\"type\":\"error.response\",\"in_reply_to\":\"queue-1\",\"payload\":{\"error\":{\"code\":\"queue_full\",\"message\":\"the queue is full\"}}}");
    try testing.expectEqual(@as(usize, 1), captured.warnings);
    try testing.expectEqual(@as(usize, 1), captured.ends);
    try testing.expect(!execution.turn_open.load(.acquire));
}

test "a reservation not yet admitted is never taken by the current run's own start" {
    var script = Script{};
    const models = [_]ai_types.Model{scripted_model};
    const execution = try OapExecution.create(testing.allocator, .{
        .protocol = .{ .stream_fn = scriptedStream, .ctx = &script },
        .models = &models,
        .initial_model_id = scripted_model.id,
    });
    defer execution.destroy();
    var captured = Captured{};
    execution.sink = captured.sink();
    execution.turn_open.store(true, .release);
    try execution.queued_runs.append(testing.allocator, .{
        .text = try testing.allocator.dupe(u8, "later"),
        .submit_id = try testing.allocator.dupe(u8, "queue-1"),
    });

    try execution.translateLine("{\"type\":\"run.started\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"run-1\",\"status\":\"running\"}}");
    try testing.expectEqual(@as(usize, 0), captured.user_messages);
    try testing.expectEqual(@as(usize, 1), execution.queued_runs.items.len);
}

test "a thinking level change over OAP waits for the open turn to end instead of committing early" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    execution.turn_open.store(true, .release);
    try testing.expectError(error.RunInProgress, runtime.setThinkingLevel(.max));
    try testing.expectEqual(ai_types.ThinkingLevel.low, runtime.thinkingLevel());
    execution.turn_open.store(false, .release);
    try runtime.setThinkingLevel(.max);
    try testing.expectEqual(ai_types.ThinkingLevel.max, runtime.thinkingLevel());
}

const Compactions = struct {
    in_run_started: usize = 0,
    in_run_completed: usize = 0,
    started: usize = 0,
    outcome: ?CompactionOutcome = null,
    text: std.ArrayList(u8) = .empty,
    agent_end: ?tui_session.TuiEndReason = null,

    fn deinit(self: *Compactions) void {
        self.text.deinit(testing.allocator);
    }
};

fn drainCompactions(runtime: *tui_runtime.TuiRuntime, seen: *Compactions, until_requested: bool) !void {
    var waits: usize = 0;
    while (waits < 5000) : (waits += 1) {
        while (runtime.streamEvents().poll()) |event| {
            var owned_event = event;
            defer owned_event.deinit(testing.allocator);
            switch (owned_event) {
                .compaction_start => |payload| if (payload.in_run) {
                    seen.in_run_started += 1;
                } else {
                    seen.started += 1;
                },
                .compaction_end => |payload| if (payload.in_run) {
                    if (payload.outcome == .completed) seen.in_run_completed += 1;
                } else {
                    seen.outcome = payload.outcome;
                    try seen.text.appendSlice(testing.allocator, payload.text.slice());
                    if (until_requested) return;
                },
                .agent_end => |payload| {
                    seen.agent_end = payload.reason;
                    if (!until_requested) return;
                },
                else => {},
            }
        }
        std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    return error.TestTurnNeverEnded;
}

test "a compaction over OAP runs the endpoint's compaction and ends on its summary" {
    var script = Script{ .reply = "the session so far" };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("remember the parser");
    var turn = Seen{};
    defer turn.deinit();
    try drainTurn(&runtime, &turn);

    try runtime.compact(.{ .focus = "the parser" });
    var seen = Compactions{};
    defer seen.deinit();
    try drainCompactions(&runtime, &seen, true);
    try testing.expect(!execution.turn_open.load(.acquire));
    try testing.expect(!execution.compacting);
    try testing.expectEqual(@as(usize, 1), seen.started);
    try testing.expectEqual(@as(?CompactionOutcome, .completed), seen.outcome);
    try testing.expect(seen.text.items.len > 0);
    try testing.expect(seen.agent_end == null);
    try testing.expectEqual(@as(usize, 2), script.calls);

    try runtime.submitTurn("and after");
    var after = Seen{};
    defer after.deinit();
    try drainTurn(&runtime, &after);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), after.end);
}

test "a compaction over OAP with nothing to compact is refused before anything is sent, as the local loop refuses it" {
    var script = Script{ .reply = "the session so far" };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    try testing.expectError(error.NothingToCompact, runtime.compact(.{}));
    try testing.expect(runtime.streamEvents().poll() == null);
    try testing.expect(!execution.turn_open.load(.acquire));

    try runtime.submitTurn("remember the parser");
    var turn = Seen{};
    defer turn.deinit();
    try drainTurn(&runtime, &turn);
    try runtime.compact(.{});
    var seen = Compactions{};
    defer seen.deinit();
    try drainCompactions(&runtime, &seen, true);
    try testing.expectEqual(@as(?CompactionOutcome, .completed), seen.outcome);
    try testing.expectError(error.NothingToCompact, runtime.compact(.{}));

    try runtime.submitTurn("and after");
    var after = Seen{};
    defer after.deinit();
    try drainTurn(&runtime, &after);
    try runtime.compact(.{});
    var again = Compactions{};
    defer again.deinit();
    try drainCompactions(&runtime, &again, true);
    try testing.expectEqual(@as(?CompactionOutcome, .completed), again.outcome);
}

test "an autocompact setting reaches the endpoint as its policy, and the next run compacts inside itself" {
    var script = Script{ .tool_first = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.setCompactionPolicy("{\"kind\":\"tokens\",\"tokens\":1}");
    try testing.expectEqualStrings("{\"kind\":\"tokens\",\"tokens\":1}", execution.sent_policy);
    try runtime.submitTurn("use the tool");
    var seen = Compactions{};
    defer seen.deinit();
    try drainCompactions(&runtime, &seen, false);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.agent_end);
    try testing.expectEqual(@as(usize, 1), seen.in_run_started);
    try testing.expectEqual(@as(usize, 1), seen.in_run_completed);
    try testing.expectEqual(@as(usize, 0), seen.started);
}

test "a compaction and a policy are refused before anything is sent when the session does not carry them" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    execution.live_compaction = false;
    execution.live_policy = false;
    try testing.expectError(error.UnavailableOverOap, runtime.compact(.{}));
    try testing.expectError(error.UnavailableOverOap, runtime.setCompactionPolicy("{\"kind\":\"off\"}"));
    try testing.expect(runtime.isIdle());
    try testing.expectEqual(@as(usize, 0), execution.sent_policy.len);
}

test "a compaction in flight refuses a follow-up, a refused settings update forgets the policy, and a torn-down turn forgets the compaction" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();
    try runtime.start();

    execution.compacting = true;
    try testing.expectError(error.CompactionInProgress, OapExecution.followUp(execution, "later"));
    execution.endTurn(.@"error");
    try testing.expect(!execution.compacting);
    while (runtime.streamEvents().poll()) |event| {
        var owned = event;
        owned.deinit(testing.allocator);
    }

    try runtime.setCompactionPolicy("{\"kind\":\"share\",\"share_percent\":50}");
    try testing.expect(execution.sent_policy.len > 0);
    try execution.translateLine("{\"type\":\"error.response\",\"in_reply_to\":\"settings-9\",\"payload\":{\"error\":{\"code\":\"run_active\",\"message\":\"busy\"}}}");
    try testing.expectEqual(@as(usize, 0), execution.sent_policy.len);
}

test "a steer sent during a turn over OAP joins it at the next turn boundary and is shown once applied" {
    var script = Script{ .hold_first = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("first");
    waitForRun(execution);
    try runtime.steer("change course");
    try testing.expectEqual(@as(usize, 1), runtime.queuedCounts().steering);
    try testing.expectEqual(@as(u64, 0), runtime.steersConsumedCount());
    waitForSteer(execution);
    script.released.store(true, .release);

    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(usize, 1), seen.agent_starts);
    try testing.expectEqual(@as(usize, 2), script.calls);
    try testing.expectEqualStrings("firstchange course", seen.user_text.items);
    try testing.expectEqual(@as(usize, 0), seen.warnings);
    try testing.expectEqual(@as(usize, 0), runtime.queuedCounts().steering);
    try testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
}

test "a steer sent while no turn runs over OAP starts one and counts as settled" {
    var script = Script{};
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.start();
    try runtime.steer("now");
    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .completed), seen.end);
    try testing.expectEqual(@as(usize, 1), script.calls);
    try testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
}

test "a steer still waiting when the turn is cancelled over OAP settles without a warning" {
    var script = Script{ .wait_for_cancel = true };
    var execution: *OapExecution = undefined;
    var runtime = try remoteRuntime(&script, &execution, .low);
    defer execution.destroy();
    defer runtime.deinit();

    try runtime.submitTurn("first");
    waitForRun(execution);
    try runtime.steer("change course");
    waitForSteer(execution);
    runtime.cancel();

    var seen = Seen{};
    defer seen.deinit();
    try drainTurn(&runtime, &seen);
    try testing.expectEqual(@as(?tui_session.TuiEndReason, .cancelled), seen.end);
    try testing.expectEqual(@as(usize, 0), seen.warnings);
    try testing.expectEqualStrings("first", seen.user_text.items);
    try testing.expectEqual(@as(usize, 0), runtime.queuedCounts().steering);
    try testing.expectEqual(@as(u64, 1), runtime.steersConsumedCount());
}
