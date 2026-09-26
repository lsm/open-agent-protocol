const std = @import("std");
const oap_types = @import("oap_types");
const config = @import("config");
const contract = @import("contract");
const memory = @import("memory");

pub const default_stream_queue = 64;
pub const default_journal_capacity = 256;
pub const default_hold_ns = 30 * std.time.ns_per_s;
pub const default_shutdown_ns = 10 * std.time.ns_per_s;
pub const default_participant = "user";

pub const Failure = error{
    UnknownAdapter,
    AdapterExists,
    UnknownSession,
    SessionExists,
    ScopeMismatch,
    NoRunToResume,
    StaleCapabilities,
    UnresolvableAttachment,
    InvalidCursor,
    SubscriptionFull,
    AdapterDescriptorUnbound,
    CatalogMisScoped,
    CatalogUnlabelled,
} || contract.Failure;

pub const Options = struct {
    stream_queue: usize = default_stream_queue,
    journal_capacity: usize = default_journal_capacity,
    hold_ns: u64 = default_hold_ns,
    shutdown_ns: u64 = default_shutdown_ns,
    tool_sources: []const contract.ConfiguredSource = &.{},
};

pub const Ending = enum {
    open,
    overflow,
    stream_failed,
    session_closed,
    expired,
};

pub const Delivery = struct {
    line: []const u8,
    run_id: []const u8,
    sequence: u64,
};

pub const SubscribeOptions = struct {
    run_id: []const u8 = "",
    after: ?u64 = null,
};

pub const OpenRequest = struct {
    session_id: []const u8 = "",
    participant: []const u8 = default_participant,
    subscribe: bool = false,
    allow_degraded_features: []const []const u8 = &.{},
    tools_json: ?[]const u8 = null,
    tool_sources_json: ?[]const u8 = null,

    fn payload(self: OpenRequest) oap_types.SessionOpenRequest {
        return .{
            .session_id = if (self.session_id.len > 0) self.session_id else null,
            .subscribe = self.subscribe,
            .tools_json = self.tools_json,
            .tool_sources_json = self.tool_sources_json,
            .allow_degraded_features = self.allow_degraded_features,
        };
    }

    fn contractRequest(self: OpenRequest) contract.OpenRequest {
        return .{
            .session_id = self.session_id,
            .participant = self.participant,
            .allow_degraded_features = self.allow_degraded_features,
            .tools_json = self.tools_json,
            .tool_sources_json = self.tool_sources_json,
        };
    }
};

pub const Opened = struct {
    session_id: []const u8,
    state: oap_types.SessionState,
    revision: []const u8,
    subscription: ?*Subscription = null,
};

pub const Catalog = struct {
    models: oap_types.ModelsResponse,
    revision: []const u8,
};

pub const ToolSet = struct {
    tools: oap_types.ToolsListResponse,
    revision: []const u8,
};

pub const Status = struct {
    session_id: []const u8,
    adapter: []const u8,
    status: oap_types.SessionStatus,
    active_run_id: []const u8,
    active_runs: []const oap_types.ActiveRun,
    created_at_ms: i64,
};

pub const Listed = struct {
    name: []const u8,
    revision: []const u8 = "",
    capabilities: ?contract.Descriptor = null,
    message: []const u8 = "",
    failed: bool = false,
};

pub const Builder = struct {
    context: *anyopaque,
    make: *const fn (context: *anyopaque, arena: std.mem.Allocator, entry: config.AdapterEntry) contract.Failure!contract.Adapter,
};

pub const Subscription = struct {
    hub: *Hub,
    session_id: []const u8,
    run_id: []const u8 = "",
    joined_after: u64 = 0,
    joined: bool = false,
    gap: ?contract.Gap = null,
    replay: std.ArrayList(contract.Event) = .empty,
    replay_at: usize = 0,
    queue: std.ArrayList(contract.Event) = .empty,
    queue_at: usize = 0,
    highest: u64 = 0,
    ending: Ending = .open,
    overflow_run: []const u8 = "",
    overflow_sequence: u64 = 0,
    held: bool = false,
    expires_ns: u64 = 0,
    detached: bool = false,

    pub fn next(self: *Subscription) ?Delivery {
        const allocator = self.hub.allocator;
        self.trim(allocator);
        if (self.replay.items.len > 0) {
            const event = self.replay.items[0];
            self.replay_at = 1;
            self.advance(event);
            return .{ .line = event.line, .run_id = event.run_id, .sequence = event.sequence };
        }
        if (self.queue.items.len > 0) {
            const event = self.queue.items[0];
            self.queue_at = 1;
            self.advance(event);
            return .{ .line = event.line, .run_id = event.run_id, .sequence = event.sequence };
        }
        return null;
    }

    fn trim(self: *Subscription, allocator: std.mem.Allocator) void {
        while (self.replay_at > 0) {
            allocator.free(self.replay.orderedRemove(0).line);
            self.replay_at -= 1;
        }
        while (self.queue_at > 0) {
            allocator.free(self.queue.orderedRemove(0).line);
            self.queue_at -= 1;
        }
    }

    fn advance(self: *Subscription, event: contract.Event) void {
        if (!std.mem.eql(u8, self.run_id, event.run_id)) {
            self.run_id = event.run_id;
            self.highest = 0;
        }
        if (event.sequence > self.highest) self.highest = event.sequence;
    }

    pub fn close(self: *Subscription) void {
        if (self.detached) return;
        self.detached = true;
        self.hub.detach(self);
    }

    fn release(self: *Subscription, allocator: std.mem.Allocator) void {
        for (self.replay.items) |event| allocator.free(event.line);
        self.replay.deinit(allocator);
        for (self.queue.items) |event| allocator.free(event.line);
        self.queue.deinit(allocator);
        if (self.overflow_run.len > 0) allocator.free(self.overflow_run);
        self.* = undefined;
    }
};

const Cursor = struct {
    run_id: []u8,
    latest: u64 = 0,
};

const Journaled = struct {
    line: []u8,
    run_id: []const u8,
    sequence: u64,
};

const Registered = struct {
    name: []u8,
    adapter: contract.Adapter,
    revision: []const u8 = "",
};

const Held = struct {
    subscription: *Subscription,
    expires_ns: u64,
};

const Entry = struct {
    adapter_name: []u8,
    session_id: []u8,
    session: contract.Session,
    created_at_ms: i64,
    run_id: []u8,
    closed: bool = false,
    journal: std.ArrayList(Journaled) = .empty,
    cursors: std.ArrayList(Cursor) = .empty,
    subscribers: std.ArrayList(*Subscription) = .empty,

    fn deinit(self: *Entry, allocator: std.mem.Allocator) void {
        for (self.cursors.items) |cursor| allocator.free(cursor.run_id);
        self.cursors.deinit(allocator);
        for (self.journal.items) |kept| allocator.free(kept.line);
        self.journal.deinit(allocator);
        self.subscribers.deinit(allocator);
        if (!self.closed) self.session.close();
        allocator.free(self.adapter_name);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        self.* = undefined;
    }
};

pub const Hub = struct {
    allocator: std.mem.Allocator,
    clock: *const fn () u64,
    stream_queue: usize,
    journal_capacity: usize,
    hold_ns: u64,
    shutdown_ns: u64,
    tool_sources: []const contract.ConfiguredSource,
    adapters: std.ArrayList(Registered) = .empty,
    entries: std.ArrayList(Entry) = .empty,
    subscriptions: std.ArrayList(*Subscription) = .empty,
    holds: std.ArrayList(Held) = .empty,

    pub fn init(allocator: std.mem.Allocator, now: *const fn () u64, options: Options) Hub {
        return .{
            .allocator = allocator,
            .clock = now,
            .stream_queue = options.stream_queue,
            .journal_capacity = options.journal_capacity,
            .hold_ns = options.hold_ns,
            .shutdown_ns = options.shutdown_ns,
            .tool_sources = options.tool_sources,
        };
    }

    pub fn deinit(self: *Hub) void {
        self.holds.deinit(self.allocator);
        for (self.subscriptions.items) |subscription| {
            subscription.release(self.allocator);
            self.allocator.destroy(subscription);
        }
        self.subscriptions.deinit(self.allocator);
        for (self.entries.items) |*entry| entry.deinit(self.allocator);
        self.entries.deinit(self.allocator);
        for (self.adapters.items) |*registered| {
            self.allocator.free(registered.name);
            registered.* = undefined;
        }
        self.adapters.deinit(self.allocator);
        self.* = undefined;
    }

    pub fn register(self: *Hub, name: []const u8, adapter: contract.Adapter) !void {
        if (self.find(name) != null) return error.AdapterExists;
        var refusal = contract.Refusal{};
        const descriptor = try adapter.probe(&refusal);
        if (descriptor.capability_revision.len == 0) return error.AdapterDescriptorUnbound;
        const owned = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned);
        try self.adapters.ensureUnusedCapacity(self.allocator, 1);
        self.adapters.appendAssumeCapacity(.{ .name = owned, .adapter = adapter, .revision = descriptor.capability_revision });
    }

    pub fn load(self: *Hub, arena: std.mem.Allocator, file: config.File, builder: Builder) !void {
        for (file.adapters) |entry| {
            if (entry.journal_capacity) |capacity| {
                if (capacity > 0 and self.journal_capacity == default_journal_capacity) {
                    self.journal_capacity = @intCast(capacity);
                }
            }
            const adapter = try builder.make(builder.context, arena, entry);
            try self.register(entry.name, adapter);
        }
    }

    pub fn listing(self: *Hub, arena: std.mem.Allocator) ![]Listed {
        var listed = std.ArrayList(Listed).empty;
        errdefer listed.deinit(arena);
        for (self.adapters.items) |*registered| {
            const owned = try arena.dupe(u8, registered.name);
            errdefer arena.free(owned);
            try listed.append(arena, .{ .name = owned, .revision = registered.revision });
            const last = &listed.items[listed.items.len - 1];
            var refusal = contract.Refusal{};
            if (registered.adapter.probe(&refusal)) |descriptor| {
                registered.revision = descriptor.capability_revision;
                last.capabilities = descriptor;
                last.revision = descriptor.capability_revision;
            } else |err| {
                if (err == error.OutOfMemory) return err;
                last.failed = true;
                last.message = if (refusal.message.len > 0) refusal.message else "the adapter could not be probed";
            }
        }
        return listed.items;
    }

    pub fn names(self: *Hub, arena: std.mem.Allocator) ![]const []const u8 {
        var listed = std.ArrayList([]const u8).empty;
        errdefer listed.deinit(arena);
        for (self.adapters.items) |*registered| try listed.append(arena, registered.name);
        return listed.items;
    }

    pub fn probe(self: *Hub, name: []const u8) Failure!contract.Descriptor {
        const registered = self.find(name) orelse return error.UnknownAdapter;
        var refusal = contract.Refusal{};
        return registered.adapter.probe(&refusal);
    }

    pub fn revision(self: *Hub, name: []const u8) Failure![]const u8 {
        const registered = self.find(name) orelse return error.UnknownAdapter;
        if (registered.revision.len == 0) return error.AdapterDescriptorUnbound;
        return registered.revision;
    }

    pub fn sessionCount(self: *const Hub) usize {
        return self.entries.items.len;
    }

    pub fn open(self: *Hub, arena: std.mem.Allocator, adapter_name: []const u8, request: OpenRequest) Failure!Opened {
        const registered = self.find(adapter_name) orelse return error.UnknownAdapter;
        if (registered.revision.len == 0) return error.AdapterDescriptorUnbound;
        if (request.session_id.len > 0 and self.findSession(request.session_id) != null) return error.SessionExists;
        var refusal = contract.Refusal{};
        const descriptor = try registered.adapter.probe(&refusal);
        try contract.refuseUnadvertisedOpenElections(descriptor, &request.payload(), &refusal);
        var session = try registered.adapter.open(arena, request.contractRequest(), &refusal);
        var adopted = false;
        errdefer if (!adopted) session.close();
        const opened_state = try session.state(arena, &refusal);
        const entry = try self.adopt(adapter_name, session, @intCast(self.clock() / std.time.ns_per_ms));
        adopted = true;
        var opened = Opened{ .session_id = entry.session_id, .state = opened_state, .revision = registered.revision };
        if (request.subscribe) {
            opened.subscription = self.subscribe(arena, opened.session_id, .{}) catch |err| {
                self.removeSession(entry);
                return err;
            };
        }
        return opened;
    }

    pub fn submit(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.MessageSubmitRequest) Failure!oap_types.MessageSubmitResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        var refusal = contract.Refusal{};
        const admission = try entry.session.submit(arena, request, &refusal);
        if (admission.run_id) |run_id| try self.promote(entry, run_id);
        return admission;
    }

    pub fn resolve(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, resolution: contract.Resolution) Failure!void {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        var refusal = contract.Refusal{};
        return entry.session.resolve(arena, resolution, &refusal);
    }

    pub fn resolveCall(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request_id: []const u8, request: *const oap_types.CallResolveRequest) Failure!oap_types.CallResolveResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        const resolver = entry.session.vtable.resolve_call orelse return error.UnsupportedFeature;
        var refusal = contract.Refusal{};
        return resolver(entry.session.ptr, arena, request_id, request, &refusal);
    }

    pub fn cancel(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, run_id: []const u8) Failure!oap_types.RunCancelResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        var refusal = contract.Refusal{};
        return entry.session.cancel(arena, run_id, &refusal);
    }

    pub fn state(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!oap_types.SessionState {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        var refusal = contract.Refusal{};
        return entry.session.state(arena, &refusal);
    }

    pub fn models(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.ModelsRequest) Failure!Catalog {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        if (request.session_id.len > 0 and !std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        const lister = entry.session.vtable.models orelse return error.UnsupportedFeature;
        var refusal = contract.Refusal{};
        const served = try lister(entry.session.ptr, arena, request, &refusal);
        if (!std.mem.eql(u8, served.session_id, session_id)) return error.CatalogMisScoped;
        const stamped = try self.revision(entry.adapter_name);
        if (stamped.len == 0) return error.CatalogUnlabelled;
        return .{ .models = served, .revision = stamped };
    }

    pub fn tools(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.ToolsListRequest) Failure!ToolSet {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        if (request.session_id) |named| {
            if (!std.mem.eql(u8, named, session_id)) return error.ScopeMismatch;
        }
        const lister = entry.session.vtable.tools orelse return error.ToolCatalogUnavailable;
        var refusal = contract.Refusal{};
        const catalog = try lister(entry.session.ptr, arena, request, &refusal);
        if (catalog.session_id) |named| {
            if (!std.mem.eql(u8, named, session_id)) return error.CatalogMisScoped;
        }
        if (catalog.tools.len == 0) return .{ .tools = catalog, .revision = "" };
        const stamped = try self.revision(entry.adapter_name);
        if (stamped.len == 0) return error.CatalogUnlabelled;
        return .{ .tools = catalog, .revision = stamped };
    }

    pub fn sessions(self: *Hub, arena: std.mem.Allocator) ![]Status {
        var listed = std.ArrayList(Status).empty;
        errdefer listed.deinit(arena);
        for (self.entries.items) |*entry| {
            if (entry.closed) {
                try listed.append(arena, .{
                    .session_id = entry.session_id,
                    .adapter = entry.adapter_name,
                    .status = .closed,
                    .active_run_id = "",
                    .active_runs = &.{},
                    .created_at_ms = entry.created_at_ms,
                });
                continue;
            }
            var refusal = contract.Refusal{};
            const listed_state = entry.session.state(arena, &refusal) catch |err| switch (err) {
                error.SessionClosed => continue,
                else => return err,
            };
            try listed.append(arena, .{
                .session_id = entry.session_id,
                .adapter = entry.adapter_name,
                .status = listed_state.status,
                .active_run_id = listed_state.active_run_id orelse "",
                .active_runs = listed_state.active_runs,
                .created_at_ms = entry.created_at_ms,
            });
        }
        return listed.items;
    }

    pub fn close(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!void {
        _ = arena;
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        entry.session.close();
        self.closeSession(entry);
    }

    pub fn subscribe(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, options: SubscribeOptions) Failure!*Subscription {
        _ = arena;
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (entry.closed) return error.SessionClosed;
        if (options.after == null and options.run_id.len > 0) return error.InvalidCursor;
        if (self.heldFor(entry)) |held| {
            if (options.after == null) {
                _ = self.dropHold(held);
                held.held = false;
                return held;
            }
            self.retireSubscription(held, .expired);
        }
        if (entry.subscribers.items.len >= self.stream_queue) return error.SubscriptionFull;
        const subscription = try self.allocator.create(Subscription);
        errdefer self.allocator.destroy(subscription);
        subscription.* = .{ .hub = self, .session_id = session_id };
        errdefer subscription.release(self.allocator);
        if (options.after) |after| {
            try self.replay(entry, subscription, after, options.run_id);
        } else {
            const joined = self.journaled(entry, entry.run_id);
            subscription.joined = joined > 0;
            subscription.joined_after = joined;
        }
        try entry.subscribers.append(self.allocator, subscription);
        try self.subscriptions.append(self.allocator, subscription);
        return subscription;
    }

    pub fn hold(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!*Subscription {
        const subscription = try self.subscribe(arena, session_id, .{});
        subscription.held = true;
        subscription.expires_ns = self.clock() + self.hold_ns;
        try self.holds.ensureUnusedCapacity(self.allocator, 1);
        self.holds.appendAssumeCapacity(.{ .subscription = subscription, .expires_ns = subscription.expires_ns });
        return subscription;
    }

    pub fn expireHolds(self: *Hub) void {
        const now = self.clock();
        var index: usize = 0;
        while (index < self.holds.items.len) {
            if (self.holds.items[index].expires_ns > now) {
                index += 1;
                continue;
            }
            const held = self.holds.orderedRemove(index);
            self.retireSubscription(held.subscription, .expired);
        }
    }

    pub fn pump(self: *Hub, allocator: std.mem.Allocator) !void {
        self.expireHolds();
        var scratch = std.heap.ArenaAllocator.init(allocator);
        defer scratch.deinit();
        for (self.entries.items) |*entry| {
            if (entry.closed) continue;
            var events = std.ArrayList(contract.Event).empty;
            entry.session.drain(scratch.allocator(), &events) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => {
                    self.closeSession(entry);
                    continue;
                },
            };
            for (events.items) |event| {
                try self.remember(entry, event);
                try self.fanOut(entry, event);
            }
        }
    }

    pub fn closeSessions(self: *Hub) void {
        const deadline = self.clock() + self.shutdown_ns;
        for (self.entries.items) |*entry| {
            if (entry.closed) continue;
            if (self.clock() >= deadline) break;
            var scratch = std.heap.ArenaAllocator.init(self.allocator);
            defer scratch.deinit();
            entry.session.close();
            self.closeSession(entry);
        }
    }

    pub fn toolSource(self: *const Hub, id: []const u8) ?contract.ConfiguredSource {
        for (self.tool_sources) |source| {
            if (std.mem.eql(u8, source.id, id)) return source;
        }
        return null;
    }

    fn find(self: *Hub, name: []const u8) ?*Registered {
        for (self.adapters.items) |*registered| {
            if (std.mem.eql(u8, registered.name, name)) return registered;
        }
        return null;
    }

    fn findSession(self: *Hub, session_id: []const u8) ?*Entry {
        if (session_id.len == 0) return null;
        for (self.entries.items) |*candidate| {
            if (std.mem.eql(u8, candidate.session_id, session_id)) return candidate;
        }
        return null;
    }

    fn promote(self: *Hub, entry: *Entry, run_id: []const u8) !void {
        const owned = try self.allocator.dupe(u8, run_id);
        self.allocator.free(entry.run_id);
        entry.run_id = owned;
    }

    fn knownRun(self: *Hub, entry: *const Entry, run_id: []const u8) bool {
        _ = self;
        for (entry.cursors.items) |cursor| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return true;
        }
        return false;
    }

    fn journaled(self: *Hub, entry: *const Entry, run_id: []const u8) u64 {
        _ = self;
        if (run_id.len == 0) return 0;
        for (entry.cursors.items) |cursor| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return cursor.latest;
        }
        return 0;
    }

    fn remember(self: *Hub, entry: *Entry, event: contract.Event) !void {
        const index = try self.cursorFor(entry, event.run_id);
        entry.cursors.items[index].latest = @max(entry.cursors.items[index].latest, event.sequence);
        if (self.journal_capacity == 0) return;
        const line = try self.allocator.dupe(u8, event.line);
        errdefer self.allocator.free(line);
        try entry.journal.ensureUnusedCapacity(self.allocator, 1);
        if (entry.journal.items.len == self.journal_capacity) self.allocator.free(entry.journal.orderedRemove(0).line);
        entry.journal.appendAssumeCapacity(.{ .line = line, .run_id = entry.cursors.items[index].run_id, .sequence = event.sequence });
    }

    fn cursorFor(self: *Hub, entry: *Entry, run_id: []const u8) !usize {
        for (entry.cursors.items, 0..) |cursor, index| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return index;
        }
        const owned = try self.allocator.dupe(u8, run_id);
        errdefer self.allocator.free(owned);
        try entry.cursors.ensureUnusedCapacity(self.allocator, 1);
        entry.cursors.appendAssumeCapacity(.{ .run_id = owned });
        return entry.cursors.items.len - 1;
    }

    fn fanOut(self: *Hub, entry: *Entry, event: contract.Event) !void {
        const cursor_index = try self.cursorFor(entry, event.run_id);
        const owned_run = entry.cursors.items[cursor_index].run_id;
        var index: usize = 0;
        while (index < entry.subscribers.items.len) {
            const subscription = entry.subscribers.items[index];
            if (subscription.ending != .open) {
                _ = entry.subscribers.orderedRemove(index);
                continue;
            }
            if (subscription.queue.items.len >= self.stream_queue) {
                try self.markOverflow(subscription, owned_run);
                _ = entry.subscribers.orderedRemove(index);
                continue;
            }
            const copy = try self.allocator.dupe(u8, event.line);
            errdefer self.allocator.free(copy);
            try subscription.queue.ensureUnusedCapacity(self.allocator, 1);
            subscription.queue.appendAssumeCapacity(.{ .line = copy, .run_id = owned_run, .sequence = event.sequence });
            index += 1;
        }
    }

    fn markOverflow(self: *Hub, subscription: *Subscription, run_id: []const u8) !void {
        const owned = try self.allocator.dupe(u8, run_id);
        if (subscription.overflow_run.len > 0) self.allocator.free(subscription.overflow_run);
        subscription.overflow_run = owned;
        subscription.overflow_sequence = subscription.highest;
        subscription.ending = .overflow;
    }

    fn adopt(self: *Hub, adapter_name: []const u8, session: contract.Session, created_at_ms: i64) !*Entry {
        const owned_name = try self.allocator.dupe(u8, adapter_name);
        errdefer self.allocator.free(owned_name);
        const owned_id = try self.allocator.dupe(u8, session.id());
        errdefer self.allocator.free(owned_id);
        const owned_run = try self.allocator.dupe(u8, "");
        errdefer self.allocator.free(owned_run);
        try self.entries.ensureUnusedCapacity(self.allocator, 1);
        self.entries.appendAssumeCapacity(.{
            .adapter_name = owned_name,
            .session_id = owned_id,
            .session = session,
            .created_at_ms = created_at_ms,
            .run_id = owned_run,
        });
        return &self.entries.items[self.entries.items.len - 1];
    }

    fn removeSession(self: *Hub, entry: *Entry) void {
        for (self.entries.items, 0..) |*candidate, index| {
            if (candidate.session_id.ptr != entry.session_id.ptr) continue;
            var removed = self.entries.orderedRemove(index);
            removed.deinit(self.allocator);
            return;
        }
        entry.deinit(self.allocator);
    }

    fn closeSession(self: *Hub, entry: *Entry) void {
        _ = self;
        entry.closed = true;
        for (entry.subscribers.items) |subscription| subscription.ending = .session_closed;
        entry.subscribers.clearRetainingCapacity();
    }

    fn retireSubscription(self: *Hub, subscription: *Subscription, ending: Ending) void {
        subscription.ending = ending;
        const entry = self.findSession(subscription.session_id) orelse return;
        for (entry.subscribers.items) |existing| {
            if (existing == subscription) existing.ending = ending;
        }
    }

    fn detach(self: *Hub, subscription: *Subscription) void {
        const entry = self.findSession(subscription.session_id) orelse return;
        for (entry.subscribers.items, 0..) |existing, index| {
            if (existing == subscription) {
                _ = entry.subscribers.orderedRemove(index);
                return;
            }
        }
    }

    fn heldFor(self: *Hub, entry: *const Entry) ?*Subscription {
        for (self.holds.items) |held| {
            if (std.mem.eql(u8, held.subscription.session_id, entry.session_id)) return held.subscription;
        }
        return null;
    }

    fn dropHold(self: *Hub, subscription: *Subscription) bool {
        for (self.holds.items, 0..) |held, index| {
            if (held.subscription == subscription) {
                _ = self.holds.orderedRemove(index);
                return true;
            }
        }
        return false;
    }

    fn replay(self: *Hub, entry: *Entry, subscription: *Subscription, after: u64, run_id: []const u8) Failure!void {
        const named = if (run_id.len > 0) run_id else entry.run_id;
        if (named.len == 0) return error.NoRunToResume;
        if (!self.knownRun(entry, named)) return error.RunNotFound;
        const latest = self.journaled(entry, named);
        if (after > latest) return error.ReplayCursorFuture;
        var oldest: u64 = 0;
        for (entry.journal.items) |kept| {
            if (!std.mem.eql(u8, kept.run_id, named)) continue;
            if (oldest == 0) oldest = kept.sequence;
        }
        if (after < latest and (oldest == 0 or after + 1 < oldest)) {
            subscription.gap = .{ .requested_after = after, .oldest_available = oldest, .latest_available = latest };
            subscription.ending = .expired;
            return;
        }
        subscription.run_id = named;
        subscription.highest = after;
        for (entry.journal.items) |kept| {
            if (!std.mem.eql(u8, kept.run_id, named)) continue;
            if (kept.sequence <= after) continue;
            const copy = try self.allocator.dupe(u8, kept.line);
            errdefer self.allocator.free(copy);
            try subscription.replay.append(self.allocator, .{ .line = copy, .run_id = named, .sequence = kept.sequence });
        }
    }
};

const testing = std.testing;

var now_ns: u64 = 1_000_000_000;

fn testClock() u64 {
    return now_ns;
}

fn tick(by: u64) void {
    now_ns += by;
}

fn submitFor(arena: std.mem.Allocator, session_id: []const u8) !oap_types.MessageSubmitRequest {
    const messages = try arena.dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "run" } }});
    return .{ .session_id = session_id, .messages = messages, .delivery = .auto };
}

test "a hub registers an adapter, names it, and refuses an unknown one" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try testing.expectError(error.UnknownAdapter, hub.probe("nobody"));
    try testing.expectError(error.UnknownAdapter, hub.revision("nobody"));
    try hub.register("memory", adapter.adapter());
    try testing.expectError(error.AdapterExists, hub.register("memory", adapter.adapter()));
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const named = try hub.names(scratch.allocator());
    try testing.expectEqual(@as(usize, 1), named.len);
    try testing.expectEqualStrings("memory", named[0]);
    try testing.expectEqualStrings(memory.capability_revision, try hub.revision("memory"));
}

test "a hub opens many sessions, each with its own journal" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const alpha = try hub.open(arena, "memory", .{ .session_id = "alpha" });
    try testing.expectEqualStrings("alpha", alpha.session_id);
    try testing.expectEqualStrings(memory.capability_revision, alpha.revision);
    const beta = try hub.open(arena, "memory", .{ .session_id = "beta" });
    try testing.expectEqualStrings("beta", beta.session_id);
    try testing.expectEqual(@as(usize, 2), hub.sessionCount());
    try testing.expectError(error.SessionExists, hub.open(arena, "memory", .{ .session_id = "alpha" }));
    try testing.expectError(error.UnknownAdapter, hub.open(arena, "absent", .{}));

    const listed = try hub.sessions(arena);
    try testing.expectEqual(@as(usize, 2), listed.len);
    try testing.expectEqual(oap_types.SessionStatus.idle, listed[0].status);
    try testing.expectEqualStrings("alpha", listed[0].session_id);
    try testing.expectEqualStrings("beta", listed[1].session_id);
    try testing.expectEqualStrings("memory", listed[0].adapter);

    const request = try submitFor(arena, "alpha");
    _ = try hub.submit(arena, "alpha", &request);
    try hub.pump(testing.allocator);
    const replayed = try hub.subscribe(arena, "alpha", .{ .run_id = "run-1", .after = 1 });
    var drained: usize = 0;
    while (replayed.next()) |_| drained += 1;
    try testing.expect(drained > 0);

    const beta_state = try hub.state(arena, "beta");
    try testing.expectEqualStrings("beta", beta_state.session_id);
}

test "a subscriber that falls behind is ended with the cursor to resume from" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "slow" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "slow");
    _ = try hub.submit(arena, "slow", &request);
    try hub.pump(testing.allocator);
    try testing.expectEqual(Ending.overflow, subscription.ending);
    try testing.expectEqualStrings("run-1", subscription.overflow_run);
    try testing.expectEqual(@as(u64, 0), subscription.overflow_sequence);
}

test "a subscriber inside its bound receives every envelope" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "kept" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "kept");
    _ = try hub.submit(arena, "kept", &request);
    try hub.pump(testing.allocator);
    try testing.expectEqual(Ending.open, subscription.ending);
    var drained: usize = 0;
    while (subscription.next()) |_| drained += 1;
    try testing.expect(drained > 2);
}

test "a cursor older than the journal is a gap, never fake continuity" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 3 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "gap" });
    for (0..4) |_| {
        const request = try submitFor(arena, "gap");
        const admitted = try hub.submit(arena, "gap", &request);
        try hub.pump(testing.allocator);
        _ = try hub.cancel(arena, "gap", admitted.run_id.?);
        try hub.pump(testing.allocator);
    }
    const gapped = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 1 });
    try testing.expect(gapped.gap != null);
    try testing.expectEqual(@as(u64, 1), gapped.gap.?.requested_after);
    try testing.expect(gapped.gap.?.latest_available > 1);
    try testing.expect(gapped.next() == null);
}

test "a cursor within the journal replays the suffix, and an unrunnable one is refused" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "cursor" });
    const request = try submitFor(arena, "cursor");
    _ = try hub.submit(arena, "cursor", &request);
    try hub.pump(testing.allocator);

    const replayed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 2 });
    const first = replayed.next().?;
    try testing.expectEqual(@as(u64, 3), first.sequence);

    try testing.expectError(error.ReplayCursorFuture, hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 9999 }));
    try testing.expectError(error.InvalidCursor, hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = null }));
    try testing.expectError(error.UnknownSession, hub.subscribe(arena, "absent", .{}));
    try testing.expectError(error.RunNotFound, hub.subscribe(arena, opened.session_id, .{ .run_id = "run-77", .after = 0 }));
}

test "a session with no run to replay says so rather than parking" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "quiet" });
    try testing.expectError(error.NoRunToResume, hub.subscribe(arena, opened.session_id, .{ .run_id = "", .after = 0 }));
}

test "a subscribing open registers before its message runs, so it misses nothing" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "compound", .subscribe = true });
    const subscription = opened.subscription.?;
    const request = try submitFor(arena, "compound");
    _ = try hub.submit(arena, "compound", &request);
    try hub.pump(testing.allocator);
    const first = subscription.next().?;
    try testing.expectEqual(@as(u64, 1), first.sequence);
    try testing.expect(std.mem.indexOf(u8, first.line, "run.started") != null);
    try testing.expectEqual(Ending.open, subscription.ending);
}

test "a held subscription is adopted by the request that follows" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "held" });
    const held = try hub.hold(arena, opened.session_id);
    const request = try submitFor(arena, "held");
    _ = try hub.submit(arena, "held", &request);
    try hub.pump(testing.allocator);
    const adopted = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(held, adopted);
    try testing.expect(!adopted.held);
    const first = adopted.next().?;
    try testing.expectEqual(@as(u64, 1), first.sequence);
}

test "a hold nothing adopts is released when its window closes" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .hold_ns = 50 * std.time.ns_per_ms });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "abandoned" });
    const held = try hub.hold(arena, opened.session_id);
    try testing.expect(held.held);
    hub.expireHolds();
    try testing.expectEqual(Ending.open, held.ending);
    tick(100 * std.time.ns_per_ms);
    hub.expireHolds();
    try testing.expectEqual(Ending.expired, held.ending);
}

test "a cursor-bearing subscription does not adopt a hold, and releases it" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .hold_ns = 50 * std.time.ns_per_ms, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "cursored" });
    const held = try hub.hold(arena, opened.session_id);
    const request = try submitFor(arena, "cursored");
    _ = try hub.submit(arena, "cursored", &request);
    try hub.pump(testing.allocator);
    const replayed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 1 });
    try testing.expect(held != replayed);
    try testing.expectEqual(Ending.expired, held.ending);
}

test "closing a session ends every subscription under it, and a closed one is refused" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "closing" });
    const first = try hub.subscribe(arena, opened.session_id, .{});
    const second = try hub.subscribe(arena, opened.session_id, .{});
    try hub.close(arena, opened.session_id);
    try testing.expectEqual(Ending.session_closed, first.ending);
    try testing.expectEqual(Ending.session_closed, second.ending);
    try testing.expectError(error.SessionClosed, hub.close(arena, opened.session_id));
    try testing.expectError(error.SessionClosed, hub.subscribe(arena, opened.session_id, .{}));
    const request = try submitFor(arena, "closing");
    try testing.expectError(error.SessionClosed, hub.submit(arena, "closing", &request));
    try testing.expectError(error.UnknownSession, hub.state(arena, "absent"));
}

test "a session-scoped request may not address another session" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "scoped" });
    const elsewhere = try submitFor(arena, "elsewhere");
    try testing.expectError(error.ScopeMismatch, hub.submit(arena, opened.session_id, &elsewhere));
    try testing.expectError(error.RunNotFound, hub.cancel(arena, opened.session_id, "run-9"));
}

test "a catalog is checked before it is served, and stamped with its revision" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "catalog" });

    const models = try hub.models(arena, opened.session_id, &.{ .session_id = opened.session_id });
    try testing.expectEqualStrings(memory.capability_revision, models.revision);
    try testing.expectEqualStrings(opened.session_id, models.models.session_id);
    try testing.expect(models.models.models.len > 0);

    const tools = try hub.tools(arena, opened.session_id, &.{ .session_id = opened.session_id });
    try testing.expectEqualStrings(memory.capability_revision, tools.revision);
    try testing.expect(tools.tools.tools.len > 0);

    try testing.expectError(error.ScopeMismatch, hub.models(arena, opened.session_id, &.{ .session_id = "elsewhere" }));
    try testing.expectError(error.ScopeMismatch, hub.tools(arena, opened.session_id, &.{ .session_id = "elsewhere" }));
}

test "closeSessions settles every session" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const names = try testing.allocator.alloc([]const u8, 3);
    defer {
        for (names) |name| testing.allocator.free(name);
        testing.allocator.free(names);
    }
    for (names, 0..) |*slot, index| {
        const name = try std.fmt.allocPrint(testing.allocator, "sweep-{d}", .{index});
        slot.* = name;
        _ = try hub.open(arena, "memory", .{ .session_id = name });
    }
    try testing.expectEqual(@as(usize, 3), hub.sessionCount());
    hub.closeSessions();
    for (names) |name| try testing.expectError(error.SessionClosed, hub.state(arena, name));
    const listed = try hub.sessions(arena);
    try testing.expectEqual(@as(usize, 3), listed.len);
    for (listed) |status| try testing.expectEqual(oap_types.SessionStatus.closed, status.status);
}

test "the registry refuses a name twice and an unlabelled descriptor" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try testing.expectError(error.AdapterDescriptorUnbound, hub.register("bare", bareAdapter()));
    try hub.register("memory", adapter.adapter());
    try testing.expectError(error.AdapterExists, hub.register("memory", adapter.adapter()));
}

test "the listing carries every registered adapter with its revision" {
    var adapter = memory.Adapter.init(testing.allocator);
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const listed = try hub.listing(arena);
    try testing.expectEqual(@as(usize, 1), listed.len);
    try testing.expectEqualStrings("memory", listed[0].name);
    try testing.expectEqualStrings(memory.capability_revision, listed[0].revision);
    try testing.expect(!listed[0].failed);
    try testing.expect(listed[0].capabilities != null);
}

test "register hands off its name under allocation failure" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            var hub = Hub.init(gpa, testClock, .{});
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
        }
    }.attempt, .{});
}

test "open hands off a session, its name and its run under allocation failure" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            var hub = Hub.init(gpa, testClock, .{});
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            _ = try hub.open(scratch.allocator(), "memory", .{ .session_id = "owned" });
        }
    }.attempt, .{});
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            var hub = Hub.init(gpa, testClock, .{});
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            _ = try hub.open(scratch.allocator(), "memory", .{ .session_id = "subscribing", .subscribe = true });
        }
    }.attempt, .{});
}

test "subscribe, replay and pump hand off their queues under allocation failure" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            var hub = Hub.init(gpa, testClock, .{ .stream_queue = 4, .journal_capacity = 4 });
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            const arena = scratch.allocator();
            const opened = try hub.open(arena, "memory", .{ .session_id = "fanout" });
            _ = try hub.subscribe(arena, opened.session_id, .{});
            const request = try submitFor(arena, "fanout");
            _ = try hub.submit(arena, "fanout", &request);
            try hub.pump(gpa);
        }
    }.attempt, .{});
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            var hub = Hub.init(gpa, testClock, .{ .stream_queue = 4, .journal_capacity = 4 });
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            const arena = scratch.allocator();
            const opened = try hub.open(arena, "memory", .{ .session_id = "replayed" });
            const request = try submitFor(arena, "replayed");
            _ = try hub.submit(arena, "replayed", &request);
            try hub.pump(gpa);
            _ = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 0 });
        }
    }.attempt, .{});
}

test "hold, listing and sessions hand off under allocation failure" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            var hub = Hub.init(gpa, testClock, .{});
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            const arena = scratch.allocator();
            _ = try hub.listing(arena);
            const opened = try hub.open(arena, "memory", .{ .session_id = "held" });
            _ = try hub.hold(arena, opened.session_id);
            hub.expireHolds();
            _ = try hub.sessions(arena);
            _ = try hub.names(arena);
        }
    }.attempt, .{});
}

const bare_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "bare", .name = "Bare", .version = "0.1", .adapter = "script" },
    .capability_revision = "",
    .features = &.{},
};

fn bareAdapter() contract.Adapter {
    return .{ .ptr = @constCast(@ptrCast(&bare_descriptor)), .vtable = &.{ .probe = bareProbe, .open = bareOpen } };
}

fn bareProbe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
    _ = ptr;
    _ = refusal;
    return bare_descriptor;
}

fn bareOpen(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
    _ = ptr;
    _ = arena;
    _ = request;
    _ = refusal;
    return error.Unavailable;
}
