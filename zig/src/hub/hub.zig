const std = @import("std");
const builtin = @import("builtin");
const oap_types = @import("oap_types");
const config = @import("config");
const contract = @import("contract");
const memory = @import("memory");
const compat = @import("compat");
pub const binding = @import("binding.zig");

pub const default_stream_queue = 64;
pub const default_journal_capacity = 256;
pub const default_hold_ns = 30 * std.time.ns_per_s;
pub const default_shutdown_ns = 10 * std.time.ns_per_s;
pub const close_attempts = 3;
pub const close_retry_wait_ns = 100 * std.time.ns_per_ms;
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
    ConfigRefused,
} || contract.Failure;

pub const OpenRefusal = struct {
    reason: contract.Refusal = .{},
    expected_revision: []const u8 = "",
    current_revision: []const u8 = "",
};

pub const Options = struct {
    max_subscriptions: usize = 0,
    stream_queue: usize = default_stream_queue,
    journal_capacity: usize = default_journal_capacity,
    hold_ns: u64 = default_hold_ns,
    shutdown_ns: u64 = default_shutdown_ns,
    tool_sources: []const contract.ConfiguredSource = &.{},
    bindings: ?*binding.Store = null,
};

pub const Ending = enum {
    open,
    run_terminal,
    overflow,
    stream_failed,
    session_closed,
    expired,
};

const Pending = struct {
    line: []u8,
    run_id: []const u8,
    sequence: u64,
    terminal: bool,
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
    metadata: ?std.json.Value = null,
    capability_revision: ?[]const u8 = null,
    subscribe: bool = false,
    reopen: bool = false,
    allow_degraded_features: []const []const u8 = &.{},
    tools_json: ?[]const u8 = null,
    tool_sources_json: ?[]const u8 = null,
    reasoning_level: ?[]const u8 = null,
    compaction_policy_json: ?[]const u8 = null,

    fn payload(self: OpenRequest) oap_types.SessionOpenRequest {
        return .{
            .session_id = if (self.session_id.len > 0) self.session_id else null,
            .subscribe = self.subscribe,
            .reopen = self.reopen,
            .tools_json = self.tools_json,
            .tool_sources_json = self.tool_sources_json,
            .allow_degraded_features = self.allow_degraded_features,
            .reasoning_level = self.reasoning_level,
            .compaction_policy_json = self.compaction_policy_json,
        };
    }

    fn contractRequest(self: OpenRequest, native_session_id: []const u8) contract.OpenRequest {
        return .{
            .session_id = self.session_id,
            .participant = self.participant,
            .metadata = self.metadata,
            .allow_degraded_features = self.allow_degraded_features,
            .tools_json = self.tools_json,
            .tool_sources_json = self.tool_sources_json,
            .reopen = self.reopen,
            .native_session_id = native_session_id,
            .reasoning_level = self.reasoning_level,
            .compaction_policy_json = self.compaction_policy_json,
        };
    }
};

const Bound = struct {
    session_id: []const u8,
    adapter_name: []const u8,
    native_id: []const u8,
    version: []const u8 = "",
    model: []const u8 = "",
    directory: []const u8 = "",
    reasoning_level: []const u8 = "",
    compaction_policy: []const u8 = "",

    fn deinit(self: *Bound, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.adapter_name);
        allocator.free(self.native_id);
        allocator.free(self.version);
        allocator.free(self.model);
        allocator.free(self.directory);
        allocator.free(self.reasoning_level);
        allocator.free(self.compaction_policy);
        self.* = undefined;
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

pub const WorkStatus = enum { queued, running, needs_you, done, failed, stopped };

pub const Work = struct {
    session_id: []const u8,
    adapter: []const u8,
    directory: []const u8,
    status: WorkStatus,
    run_id: []const u8 = "",
    last_reply: []const u8 = "",
    pending_interaction: []const u8 = "",
    title: []const u8 = "",
    updated_at_ms: i64,
};

pub const Native = struct {
    adapter: []const u8,
    session: contract.NativeSession,
};

pub const NativeFailure = struct {
    adapter: []const u8,
    message: []const u8,
};

pub const Natives = struct {
    sessions: []const Native,
    failures: []const NativeFailure,
};

pub const native_list_limit: usize = 50;

pub const last_reply_limit: usize = 4096;
pub const turn_text_limit: usize = 64 * 1024;
pub const turns_kept: usize = 512;

pub const TurnRole = enum { user, assistant };

pub const Turn = struct {
    role: TurnRole,
    text: []u8,
    run_id: []u8,
    outcome: []const u8,
    at_ms: i64,
};

pub const Transcript = struct {
    first_index: u64,
    turns: []const Turn,
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

pub const Sweep = struct {
    sessions: usize = 0,
    closed: usize = 0,
    refused: usize = 0,
    refused_attempts: usize = 0,
    unattempted: usize = 0,

    pub fn clean(self: Sweep) bool {
        return self.refused == 0 and self.unattempted == 0;
    }
};

const Settled = struct {
    attempts: usize = 0,
    closed: bool = false,
};

const pollable = builtin.os.tag != .windows;

pub fn expiredAt(expires_ns: u64, at_ns: u64) bool {
    return expires_ns <= at_ns;
}

pub const Hold = struct {
    expires_ns: u64,

    pub fn expired(self: Hold, at_ns: u64) bool {
        return expiredAt(self.expires_ns, at_ns);
    }
};

pub const Subscription = struct {
    hub: *Hub,
    session_id: []u8,
    run_id: []u8 = &.{},
    joined_after: u64 = 0,
    joined: bool = false,
    joined_run: []u8 = &.{},
    gap: ?contract.Gap = null,
    replay: std.ArrayList(Pending) = .empty,
    replay_at: usize = 0,
    queue: std.ArrayList(Pending) = .empty,
    queue_at: usize = 0,
    highest: u64 = 0,
    ending: Ending = .open,
    overflow_run: []u8 = &.{},
    overflow_sequence: u64 = 0,
    held: bool = false,
    expires_ns: u64 = 0,
    detached: bool = false,

    pub fn next(self: *Subscription) ?Delivery {
        const allocator = self.hub.allocator;
        self.trim(allocator);
        if (self.ending == .run_terminal or self.ending == .expired) return null;
        if (self.replay.items.len > 0) {
            const event = self.replay.items[0];
            self.replay_at = 1;
            self.advance(event);
            return self.settle(event);
        }
        if (self.queue.items.len > 0) {
            const event = self.queue.items[0];
            self.queue_at = 1;
            self.advance(event);
            return self.settle(event);
        }
        return null;
    }

    fn settle(self: *Subscription, event: Pending) Delivery {
        if (self.ending == .open and event.terminal) {
            self.ending = .run_terminal;
            self.hub.leave(self);
        }
        return .{ .line = event.line, .run_id = event.run_id, .sequence = event.sequence };
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

    fn advance(self: *Subscription, event: Pending) void {
        if (!std.mem.eql(u8, self.run_id, event.run_id)) {
            const owned = self.hub.allocator.dupe(u8, event.run_id) catch {
                self.ending = .stream_failed;
                self.replay_at = self.replay.items.len;
                self.queue_at = self.queue.items.len;
                return;
            };
            if (self.run_id.len > 0) self.hub.allocator.free(self.run_id);
            self.run_id = owned;
            self.highest = 0;
        }
        if (event.sequence > self.highest) self.highest = event.sequence;
        if (self.ending == .overflow and std.mem.eql(u8, self.overflow_run, event.run_id)) {
            self.overflow_sequence = event.sequence;
        }
    }

    pub fn close(self: *Subscription) void {
        if (self.detached) return;
        self.detached = true;
        self.hub.detach(self);
        self.forgetEvents(self.hub.allocator);
    }

    fn forgetEvents(self: *Subscription, allocator: std.mem.Allocator) void {
        for (self.replay.items) |event| allocator.free(event.line);
        self.replay.clearRetainingCapacity();
        self.replay_at = 0;
        for (self.queue.items) |event| allocator.free(event.line);
        self.queue.clearRetainingCapacity();
        self.queue_at = 0;
    }

    fn release(self: *Subscription, allocator: std.mem.Allocator) void {
        self.forgetEvents(allocator);
        self.replay.deinit(allocator);
        self.queue.deinit(allocator);
        if (self.overflow_run.len > 0) allocator.free(self.overflow_run);
        if (self.run_id.len > 0) allocator.free(self.run_id);
        if (self.joined_run.len > 0) allocator.free(self.joined_run);
        allocator.free(self.session_id);
        self.* = undefined;
    }
};

const Cursor = struct {
    run_id: []u8,
    latest: u64 = 0,
    terminal: u64 = 0,
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
    directory: []u8 = &.{},
};

const Held = struct {
    subscription: *Subscription,
    expires_ns: u64,
};

const Entry = struct {
    adapter_name: []u8,
    session_id: []u8,
    session: contract.Session,
    session_closed: bool = false,
    created_at_ms: i64,
    run_id: []u8,
    journal: std.ArrayList(Journaled) = .empty,
    cursors: std.ArrayList(Cursor) = .empty,
    subscribers: std.ArrayList(*Subscription) = .empty,
    turns: std.ArrayList(Turn) = .empty,
    turns_dropped: u64 = 0,
    title: []u8 = &.{},

    fn deinit(self: *Entry, allocator: std.mem.Allocator) void {
        for (self.turns.items) |turn| {
            allocator.free(turn.text);
            allocator.free(turn.run_id);
        }
        self.turns.deinit(allocator);
        if (self.title.len > 0) allocator.free(self.title);
        for (self.cursors.items) |cursor| allocator.free(cursor.run_id);
        self.cursors.deinit(allocator);
        for (self.journal.items) |kept| allocator.free(kept.line);
        self.journal.deinit(allocator);
        self.subscribers.deinit(allocator);
        if (!self.session_closed) self.session.teardown();
        allocator.free(self.adapter_name);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        self.* = undefined;
    }
};

pub const Hub = struct {
    allocator: std.mem.Allocator,
    clock: *const fn () u64,
    max_subscriptions: usize,
    stream_queue: usize,
    journal_capacity: usize,
    hold_ns: u64,
    shutdown_ns: u64,
    tool_sources: []const contract.ConfiguredSource,
    adapters: std.ArrayList(Registered) = .empty,
    entries: std.ArrayList(Entry) = .empty,
    subscriptions: std.ArrayList(*Subscription) = .empty,
    holds: std.ArrayList(Held) = .empty,
    bound: std.ArrayList(Bound) = .empty,
    bindings: ?*binding.Store = null,

    pub fn init(allocator: std.mem.Allocator, now: *const fn () u64, options: Options) Hub {
        return .{
            .allocator = allocator,
            .clock = now,
            .max_subscriptions = options.max_subscriptions,
            .stream_queue = options.stream_queue,
            .journal_capacity = options.journal_capacity,
            .hold_ns = options.hold_ns,
            .shutdown_ns = options.shutdown_ns,
            .tool_sources = options.tool_sources,
            .bindings = options.bindings,
        };
    }

    pub fn deinit(self: *Hub) void {
        self.holds.deinit(self.allocator);
        for (self.bound.items) |*record| record.deinit(self.allocator);
        self.bound.deinit(self.allocator);
        for (self.subscriptions.items) |subscription| {
            subscription.release(self.allocator);
            self.allocator.destroy(subscription);
        }
        self.subscriptions.deinit(self.allocator);
        for (self.entries.items) |*entry| entry.deinit(self.allocator);
        self.entries.deinit(self.allocator);
        for (self.adapters.items) |*registered| {
            self.allocator.free(registered.name);
            if (registered.directory.len > 0) self.allocator.free(registered.directory);
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

    pub fn load(self: *Hub, arena: std.mem.Allocator, file: config.File, builder: Builder, diagnostic: *config.Diagnostic) !void {
        var taken = self.journal_capacity != default_journal_capacity;
        for (file.adapters) |entry| {
            if (entry.journal_capacity) |capacity| {
                if (capacity < 0) {
                    diagnostic.message = std.fmt.allocPrint(arena, "config: adapter \"{s}\": \"journal_capacity\" must not be negative", .{entry.name}) catch "config: an adapter names a negative \"journal_capacity\"";
                    return error.ConfigRefused;
                }
                if (!taken) {
                    if (capacity > 0) self.journal_capacity = @intCast(capacity);
                    taken = true;
                }
            }
            const adapter = try builder.make(builder.context, arena, entry);
            try self.register(entry.name, adapter);
            if (entry.working_directory) |directory| {
                if (directory.len > 0) self.find(entry.name).?.directory = try self.allocator.dupe(u8, directory);
            }
        }
    }

    pub fn listing(self: *Hub, arena: std.mem.Allocator) ![]Listed {
        var listed = std.ArrayList(Listed).empty;
        errdefer listed.deinit(arena);
        defer std.mem.sort(Listed, listed.items, {}, byName);
        for (self.adapters.items) |*registered| {
            const owned = try arena.dupe(u8, registered.name);
            errdefer arena.free(owned);
            try listed.append(arena, .{ .name = owned, .revision = registered.revision });
            const last = &listed.items[listed.items.len - 1];
            var refusal = contract.Refusal{};
            if (registered.adapter.probe(&refusal)) |descriptor| {
                if (descriptor.capability_revision.len == 0) {
                    last.failed = true;
                    last.message = "adapter descriptor carries no capability revision";
                    continue;
                }
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
        defer std.mem.sort([]const u8, listed.items, {}, byText);
        for (self.adapters.items) |*registered| try listed.append(arena, registered.name);
        return listed.items;
    }

    pub fn probe(self: *Hub, name: []const u8) Failure!contract.Descriptor {
        const registered = self.find(name) orelse return error.UnknownAdapter;
        var refusal = contract.Refusal{};
        const descriptor = try registered.adapter.probe(&refusal);
        if (descriptor.capability_revision.len == 0) return error.AdapterDescriptorUnbound;
        return descriptor;
    }

    pub fn revision(self: *Hub, name: []const u8) Failure![]const u8 {
        const registered = self.find(name) orelse return error.UnknownAdapter;
        if (registered.revision.len == 0) return error.AdapterDescriptorUnbound;
        return registered.revision;
    }

    fn byName(_: void, left: Listed, right: Listed) bool {
        return std.mem.lessThan(u8, left.name, right.name);
    }

    fn byText(_: void, left: []const u8, right: []const u8) bool {
        return std.mem.lessThan(u8, left, right);
    }

    pub fn work(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!Work {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (try self.workOf(arena, entry)) |found| return found;
        self.releaseSession(entry);
        return error.SessionClosed;
    }

    pub fn works(self: *Hub, arena: std.mem.Allocator) Failure![]Work {
        var listed = std.ArrayList(Work).empty;
        var releasing = std.ArrayList([]const u8).empty;
        for (self.entries.items) |*entry| {
            if (try self.workOf(arena, entry)) |found| {
                try listed.append(arena, found);
            } else {
                try releasing.append(arena, try arena.dupe(u8, entry.session_id));
            }
        }
        for (releasing.items) |session_id| {
            if (self.findSession(session_id)) |entry| self.releaseSession(entry);
        }
        std.mem.sort(Work, listed.items, {}, byRecentWork);
        return listed.toOwnedSlice(arena);
    }

    fn byRecentWork(_: void, left: Work, right: Work) bool {
        if (left.updated_at_ms != right.updated_at_ms) return left.updated_at_ms > right.updated_at_ms;
        return std.mem.lessThan(u8, left.session_id, right.session_id);
    }

    fn workOf(self: *Hub, arena: std.mem.Allocator, entry: *Entry) Failure!?Work {
        if (entry.session_closed) return null;
        var refusal = contract.Refusal{};
        const current = entry.session.state(arena, &refusal) catch |err| switch (err) {
            error.SessionClosed => return null,
            error.OutOfMemory => return error.OutOfMemory,
            else => oap_types.SessionState{ .session_id = entry.session_id, .status = .@"error" },
        };
        var found = Work{
            .session_id = entry.session_id,
            .adapter = entry.adapter_name,
            .directory = if (self.find(entry.adapter_name)) |registered| registered.directory else "",
            .status = .done,
            .run_id = current.active_run_id orelse "",
            .title = entry.title,
            .updated_at_ms = current.updated_at_ms orelse entry.created_at_ms,
        };
        for (current.active_runs) |run| {
            if (run.pending_interactions.len > 0) found.pending_interaction = run.pending_interactions[0];
        }
        switch (current.status) {
            .closed => return null,
            .queued => found.status = .queued,
            .waiting_for_input => found.status = .needs_you,
            .running => found.status = if (found.pending_interaction.len > 0) .needs_you else .running,
            .@"error" => found.status = .failed,
            .idle => try latestOutcome(arena, entry, &found),
        }
        return found;
    }

    fn latestOutcome(arena: std.mem.Allocator, entry: *const Entry, found: *Work) !void {
        var index = entry.journal.items.len;
        while (index > 0) {
            index -= 1;
            const kept = entry.journal.items[index];
            const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, kept.line, .{}) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => continue,
            };
            if (parsed != .object) continue;
            const kind = parsed.object.get("type") orelse continue;
            if (kind != .string) continue;
            const status: WorkStatus = if (std.mem.eql(u8, kind.string, "run.completed"))
                .done
            else if (std.mem.eql(u8, kind.string, "run.failed"))
                .failed
            else if (std.mem.eql(u8, kind.string, "run.cancelled"))
                .stopped
            else
                continue;
            found.status = status;
            found.run_id = kept.run_id;
            if (status == .done) found.last_reply = try replyText(arena, parsed.object.get("payload"));
            return;
        }
    }

    fn replyCut(text: []const u8) usize {
        return cutAt(text, last_reply_limit);
    }

    fn cutAt(text: []const u8, limit: usize) usize {
        if (text.len <= limit) return text.len;
        var cut = limit;
        while (cut > 0 and text[cut] & 0xC0 == 0x80) cut -= 1;
        return cut;
    }

    fn replyText(arena: std.mem.Allocator, payload: ?std.json.Value) ![]const u8 {
        const text = try fullReplyText(arena, payload);
        return text[0..replyCut(text)];
    }

    fn fullReplyText(arena: std.mem.Allocator, payload: ?std.json.Value) ![]const u8 {
        const body = payload orelse return "";
        if (body != .object) return "";
        const response = body.object.get("final_response") orelse return "";
        if (response != .object) return "";
        const content = response.object.get("content") orelse return "";
        var text: []const u8 = "";
        switch (content) {
            .string => |plain| text = plain,
            .array => |parts| {
                var joined: std.ArrayList(u8) = .empty;
                for (parts.items) |part| {
                    if (part != .object) continue;
                    const piece = part.object.get("text") orelse continue;
                    if (piece == .string) try joined.appendSlice(arena, piece.string);
                }
                text = joined.items;
            },
            else => return "",
        }
        return text;
    }

    fn bySessionId(_: void, left: Status, right: Status) bool {
        return std.mem.lessThan(u8, left.session_id, right.session_id);
    }

    pub fn sessionCount(self: *const Hub) usize {
        return self.entries.items.len;
    }

    pub fn open(self: *Hub, arena: std.mem.Allocator, adapter_name: []const u8, request: OpenRequest) Failure!Opened {
        return self.openReporting(arena, adapter_name, request, null);
    }

    pub fn openReporting(self: *Hub, arena: std.mem.Allocator, adapter_name: []const u8, request: OpenRequest, reported: ?*OpenRefusal) Failure!Opened {
        const registered = self.find(adapter_name) orelse return error.UnknownAdapter;
        if (registered.revision.len == 0) return error.AdapterDescriptorUnbound;
        var local: OpenRefusal = .{};
        const refused = reported orelse &local;
        const descriptor = try registered.adapter.probe(&refused.reason);
        if (request.subscribe or request.reopen or request.reasoning_level != null or request.compaction_policy_json != null or contract.carriesEntries(request.tool_sources_json)) {
            if (request.capability_revision) |wanted| {
                if (wanted.len > 0 and !std.mem.eql(u8, wanted, registered.revision)) {
                    refused.expected_revision = registered.revision;
                    refused.current_revision = wanted;
                    return error.StaleCapabilities;
                }
            }
        }
        if (request.session_id.len > 0 and self.findSession(request.session_id) != null) return error.SessionExists;
        try contract.refuseUnadvertisedOpenElections(descriptor, &request.payload(), &refused.reason);
        var native_session_id: []const u8 = "";
        if (request.reopen) {
            if (self.findBound(request.session_id)) |record| {
                if (!std.mem.eql(u8, record.adapter_name, adapter_name)) return error.UnknownSession;
                native_session_id = try arena.dupe(u8, record.native_id);
            } else {
                const store = self.bindings orelse return error.UnknownSession;
                const stored = store.latest(arena, request.session_id) catch |err| {
                    if (err == error.OutOfMemory) return error.OutOfMemory;
                    return refused.reason.fail(error.BackendFailed, "the binding store holds a record that was not written whole");
                } orelse {
                    if (store.last_failure) |failure| return refused.reason.fail(error.BackendFailed, try std.fmt.allocPrint(arena, "no binding is recorded for this session, and the binding store last failed to record one: {s}", .{@errorName(failure)}));
                    return error.UnknownSession;
                };
                if (!std.mem.eql(u8, stored.record.adapter, adapter_name)) return error.UnknownSession;
                native_session_id = stored.record.native_session_id;
            }
        }
        var session = registered.adapter.open(arena, request.contractRequest(native_session_id), &refused.reason) catch |err| {
            if (request.reopen and err == error.UnknownSession) return refused.reason.unsupported(contract.feature_open_reopen, contract.reason_unsatisfiable);
            return err;
        };
        var adopted = false;
        errdefer if (!adopted) session.teardown();
        if (self.findSession(session.id()) != null) return error.SessionExists;
        const opened_state = try session.state(arena, &refused.reason);
        var record = try self.boundFor(adapter_name, session, descriptor.endpoint.version orelse "", opened_state.current_model_id orelse "", request.reasoning_level orelse "", request.compaction_policy_json orelse "");
        var kept = false;
        errdefer if (!kept) record.deinit(self.allocator);
        const entry = try self.adopt(adapter_name, session, @intCast(self.clock() / std.time.ns_per_ms));
        adopted = true;
        self.keepBound(record);
        kept = true;
        self.recordBinding(if (request.reopen) .reopened else .opened, entry.session_id, opened_state.updated_at_ms orelse @intCast(self.clock() / std.time.ns_per_ms));
        var opened = Opened{ .session_id = entry.session_id, .state = opened_state, .revision = registered.revision };
        if (request.subscribe) {
            opened.subscription = self.subscribe(arena, opened.session_id, .{}) catch |err| {
                self.recordBinding(.closed, entry.session_id, @intCast(self.clock() / std.time.ns_per_ms));
                self.removeSession(entry);
                return err;
            };
        }
        return opened;
    }

    pub fn discardSession(self: *Hub, session_id: []const u8) void {
        const entry = self.findSession(session_id) orelse return;
        self.releaseSession(entry);
    }

    pub fn knows(self: *Hub, session_id: []const u8) bool {
        return self.findSession(session_id) != null;
    }

    pub fn submit(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8) Failure!oap_types.MessageSubmitResponse {
        var refusal = contract.Refusal{};
        return self.submitReporting(arena, session_id, request, envelope_id, &refusal);
    }

    pub fn submitReporting(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) Failure!oap_types.MessageSubmitResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        const admission = entry.session.submit(arena, request, envelope_id, refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
        try self.recordTurn(entry, .user, try userText(self.allocator, request), admission.run_id orelse "", "");
        return self.admitted(entry, admission);
    }

    fn userText(allocator: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest) ![]u8 {
        var joined: std.ArrayList(u8) = .empty;
        errdefer joined.deinit(allocator);
        for (request.messages) |message| {
            if (message.role != .user) continue;
            switch (message.content) {
                .text => |text| {
                    if (joined.items.len > 0) try joined.append(allocator, '\n');
                    try joined.appendSlice(allocator, text);
                },
                .parts => |parts| for (parts) |part| switch (part) {
                    .text => |text| {
                        if (joined.items.len > 0) try joined.append(allocator, '\n');
                        try joined.appendSlice(allocator, text);
                    },
                    else => {},
                },
            }
        }
        return joined.toOwnedSlice(allocator);
    }

    fn recordTurn(self: *Hub, entry: *Entry, role: TurnRole, owned_text: []u8, run_id: []const u8, outcome: []const u8) !void {
        errdefer self.allocator.free(owned_text);
        const kept_run = try self.allocator.dupe(u8, run_id);
        errdefer self.allocator.free(kept_run);
        try entry.turns.ensureUnusedCapacity(self.allocator, 1);
        if (entry.turns.items.len == turns_kept) {
            const oldest = entry.turns.orderedRemove(0);
            self.allocator.free(oldest.text);
            self.allocator.free(oldest.run_id);
            entry.turns_dropped += 1;
        }
        const text = if (owned_text.len > turn_text_limit) blk: {
            const cut = cutAt(owned_text, turn_text_limit);
            const shorter = try self.allocator.dupe(u8, owned_text[0..cut]);
            self.allocator.free(owned_text);
            break :blk shorter;
        } else owned_text;
        entry.turns.appendAssumeCapacity(.{ .role = role, .text = text, .run_id = kept_run, .outcome = outcome, .at_ms = @intCast(self.clock() / std.time.ns_per_ms) });
    }

    pub fn transcript(self: *Hub, session_id: []const u8, after: ?u64, limit: usize) Failure!Transcript {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        const start_index: u64 = if (after) |given| given + 1 else entry.turns_dropped;
        const from: usize = if (start_index <= entry.turns_dropped) 0 else @intCast(@min(start_index - entry.turns_dropped, entry.turns.items.len));
        const to = @min(entry.turns.items.len, from + limit);
        return .{ .first_index = entry.turns_dropped + from, .turns = entry.turns.items[from..to] };
    }

    pub fn natives(self: *Hub, arena: std.mem.Allocator, known: []const []const u8) Failure!Natives {
        var found_sessions = std.ArrayList(Native).empty;
        var failures = std.ArrayList(NativeFailure).empty;
        for (self.adapters.items) |registered| {
            var refusal = contract.Refusal{};
            const answered = registered.adapter.nativeList(arena, .{ .directory = registered.directory, .limit = native_list_limit }, &refusal) orelse continue;
            const listed = answered catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => {
                    try failures.append(arena, .{ .adapter = registered.name, .message = if (refusal.message.len > 0) refusal.message else @errorName(err) });
                    continue;
                },
            };
            for (listed) |found| {
                if (self.boundNative(registered.name, found.native_id) or declared(known, found.native_id)) continue;
                try found_sessions.append(arena, .{ .adapter = registered.name, .session = found });
            }
        }
        return .{ .sessions = found_sessions.items, .failures = failures.items };
    }

    fn boundNative(self: *const Hub, adapter: []const u8, native_id: []const u8) bool {
        for (self.bound.items) |record| {
            if (std.mem.eql(u8, record.adapter_name, adapter) and std.mem.eql(u8, record.native_id, native_id)) return true;
        }
        return false;
    }

    fn declared(known: []const []const u8, native_id: []const u8) bool {
        for (known) |candidate| {
            if (std.mem.eql(u8, candidate, native_id)) return true;
        }
        return false;
    }

    pub fn adapterDirectory(self: *Hub, name: []const u8) ?[]const u8 {
        const registered = self.find(name) orelse return null;
        return registered.directory;
    }

    pub fn setTitle(self: *Hub, session_id: []const u8, title: []const u8) !void {
        const entry = self.findSession(session_id) orelse return;
        const kept = try self.allocator.dupe(u8, title);
        if (entry.title.len > 0) self.allocator.free(entry.title);
        entry.title = kept;
    }

    pub fn compactReporting(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.SessionCompactRequest, envelope_id: []const u8, refusal: *contract.Refusal) Failure!oap_types.MessageSubmitResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        const compactor = entry.session.vtable.compact orelse return refusal.unsupported(contract.feature_session_compact, contract.reason_unadvertised);
        const admission = compactor(entry.session.ptr, arena, request, envelope_id, refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
        return self.admitted(entry, admission);
    }

    fn admitted(self: *Hub, entry: *Entry, admission: oap_types.MessageSubmitResponse) Failure!oap_types.MessageSubmitResponse {
        if (admission.run_id) |run_id| {
            self.noteRun(entry, run_id, admission.admission == .started) catch |err| {
                self.endSubscriptions(entry, .stream_failed);
                return err;
            };
        }
        return admission;
    }

    pub fn resolve(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, resolution: contract.Resolution) Failure!void {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        switch (resolution) {
            .input => |request| if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch,
            .permission => |request| if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch,
        }
        var refusal = contract.Refusal{};
        entry.session.resolve(arena, resolution, &refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
    }

    pub fn resolveCall(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request_id: []const u8, request: *const oap_types.CallResolveRequest) Failure!oap_types.CallResolveResponse {
        var refusal = contract.Refusal{};
        return self.resolveCallReporting(arena, session_id, request_id, request, &refusal);
    }

    pub fn resolveCallReporting(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request_id: []const u8, request: *const oap_types.CallResolveRequest, refusal: *contract.Refusal) Failure!oap_types.CallResolveResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        const resolver = entry.session.vtable.resolve_call orelse return refusal.unsupported(contract.feature_tools_provide, contract.reason_unadvertised);
        return resolver(entry.session.ptr, arena, request_id, request, refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
    }

    pub fn cancel(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, run_id: []const u8) Failure!oap_types.RunCancelResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        var refusal = contract.Refusal{};
        return entry.session.cancel(arena, run_id, &refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
    }

    pub fn updateSettings(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.SessionSettingsUpdateRequest, refusal: *contract.Refusal) Failure!oap_types.SessionSettingsUpdateResponse {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (!std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        if (request.reasoning_level == null and request.compaction_policy_json == null) return error.InvalidSubmission;
        const updater = entry.session.vtable.update_settings orelse {
            if (request.reasoning_level != null) return refusal.unsupportedField(contract.feature_session_reasoning, contract.reason_unadvertised, "reasoning_level");
            return refusal.unsupportedField(contract.feature_compaction_policy, contract.reason_unadvertised, "compaction_policy");
        };
        const updated = updater(entry.session.ptr, arena, request, refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
        return updated.response;
    }

    pub fn state(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!oap_types.SessionState {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        var refusal = contract.Refusal{};
        const reported = entry.session.state(arena, &refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.UnknownSession;
            },
            else => |failure| return failure,
        };
        if (reported.status == .closed) {
            self.releaseSession(entry);
        }
        return reported;
    }

    pub fn models(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.ModelsRequest) Failure!Catalog {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (request.session_id.len > 0 and !std.mem.eql(u8, request.session_id, session_id)) return error.ScopeMismatch;
        const lister = entry.session.vtable.models orelse return error.UnsupportedFeature;
        var refusal = contract.Refusal{};
        const served = lister(entry.session.ptr, arena, request, &refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
        if (!std.mem.eql(u8, served.response.session_id, session_id)) return error.CatalogMisScoped;
        if (served.revision.len == 0) return error.CatalogUnlabelled;
        return .{ .models = served.response, .revision = served.revision };
    }

    pub fn tools(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, request: *const oap_types.ToolsListRequest) Failure!ToolSet {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        if (request.session_id) |named| {
            if (!std.mem.eql(u8, named, session_id)) return error.ScopeMismatch;
        }
        const lister = entry.session.vtable.tools orelse return error.ToolCatalogUnavailable;
        var refusal = contract.Refusal{};
        const catalog = lister(entry.session.ptr, arena, request, &refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.SessionClosed;
            },
            else => |failure| return failure,
        };
        if (catalog.response.session_id) |named| {
            if (!std.mem.eql(u8, named, session_id)) return error.CatalogMisScoped;
        }
        if (catalog.revision.len == 0) return error.CatalogUnlabelled;
        return .{ .tools = catalog.response, .revision = catalog.revision };
    }

    pub fn sessions(self: *Hub, arena: std.mem.Allocator) ![]Status {
        var listed = std.ArrayList(Status).empty;
        errdefer listed.deinit(arena);
        defer std.mem.sort(Status, listed.items, {}, bySessionId);
        var releasing = std.ArrayList([]const u8).empty;
        defer releasing.deinit(arena);
        for (self.entries.items) |*entry| {
            var refusal = contract.Refusal{};
            const listed_state = entry.session.state(arena, &refusal) catch |err| switch (err) {
                error.SessionClosed => {
                    try releasing.append(arena, entry.session_id);
                    continue;
                },
                else => oap_types.SessionState{
                    .session_id = entry.session_id,
                    .status = .@"error",
                },
            };
            if (listed_state.status == .closed) {
                try releasing.append(arena, entry.session_id);
                continue;
            }
            try listed.append(arena, .{
                .session_id = entry.session_id,
                .adapter = entry.adapter_name,
                .status = listed_state.status,
                .active_run_id = listed_state.active_run_id orelse "",
                .active_runs = listed_state.active_runs,
                .created_at_ms = entry.created_at_ms,
            });
        }
        for (releasing.items) |session_id| {
            const entry = self.findSession(session_id) orelse continue;
            self.releaseSession(entry);
        }
        return listed.items;
    }

    pub fn close(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!void {
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        var refusal = contract.Refusal{};
        const reported = entry.session.state(arena, &refusal) catch |err| switch (err) {
            error.SessionClosed => {
                self.releaseSession(entry);
                return error.UnknownSession;
            },
            else => |failure| return failure,
        };
        if (reported.active_run_id != null or reported.active_runs.len > 0) return error.RunActive;
        if (entry.session.close()) |_| {
            entry.session_closed = true;
        } else |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
        }
        self.releaseSession(entry);
    }

    pub fn subscribe(self: *Hub, arena: std.mem.Allocator, session_id: []const u8, options: SubscribeOptions) Failure!*Subscription {
        _ = arena;
        const entry = self.findSession(session_id) orelse return error.UnknownSession;
        const named_run = try self.allocator.dupe(u8, options.run_id);
        defer self.allocator.free(named_run);
        if (options.after == null and named_run.len > 0) return error.InvalidCursor;
        if (self.heldFor(entry)) |held| {
            if (options.after == null) {
                if (held.detached or held.ending == .expired) {
                    _ = self.dropHold(held);
                    self.release(held, .expired);
                } else {
                    _ = self.dropHold(held);
                    held.held = false;
                    return held;
                }
            } else {
                _ = self.dropHold(held);
                self.release(held, .expired);
            }
        }
        if (self.max_subscriptions > 0 and entry.subscribers.items.len >= self.max_subscriptions) return error.SubscriptionFull;
        const subscription = try self.allocator.create(Subscription);
        errdefer self.allocator.destroy(subscription);
        subscription.* = .{ .hub = self, .session_id = try self.allocator.dupe(u8, session_id) };
        errdefer subscription.release(self.allocator);
        if (options.after) |after| {
            try self.replay(entry, subscription, after, named_run);
        } else {
            const joined = self.journaled(entry, entry.run_id);
            subscription.joined = joined > 0;
            subscription.joined_after = joined;
            if (joined > 0) subscription.joined_run = try self.allocator.dupe(u8, entry.run_id);
        }
        try self.subscriptions.append(self.allocator, subscription);
        errdefer _ = self.subscriptions.pop();
        if (subscription.ending == .open) {
            try entry.subscribers.append(self.allocator, subscription);
            errdefer _ = entry.subscribers.pop();
        }
        return subscription;
    }

    pub fn hold(self: *Hub, arena: std.mem.Allocator, session_id: []const u8) Failure!Hold {
        const subscription = try self.subscribe(arena, session_id, .{});
        errdefer self.discard(subscription, .expired);
        const expires = self.clock() + self.hold_ns;
        try self.holds.ensureUnusedCapacity(self.allocator, 1);
        self.holds.appendAssumeCapacity(.{ .subscription = subscription, .expires_ns = expires });
        subscription.held = true;
        subscription.expires_ns = expires;
        return .{ .expires_ns = expires };
    }

    pub fn holdSubscription(self: *Hub, subscription: *Subscription) std.mem.Allocator.Error!Hold {
        const expires = self.clock() + self.hold_ns;
        try self.holds.ensureUnusedCapacity(self.allocator, 1);
        self.holds.appendAssumeCapacity(.{ .subscription = subscription, .expires_ns = expires });
        subscription.held = true;
        subscription.expires_ns = expires;
        return .{ .expires_ns = expires };
    }

    pub fn expireHolds(self: *Hub) void {
        const now = self.clock();
        var index: usize = 0;
        while (index < self.holds.items.len) {
            if (!expiredAt(self.holds.items[index].expires_ns, now)) {
                index += 1;
                continue;
            }
            const held = self.holds.orderedRemove(index);
            self.release(held.subscription, .expired);
        }
    }

    fn handleOf(entry: *const Entry) ?std.Io.File.Handle {
        if (!pollable) return null;
        const slot = entry.session.vtable.readable orelse return null;
        return slot(entry.session.ptr);
    }

    pub fn readableHandles(self: *Hub, arena: std.mem.Allocator) std.mem.Allocator.Error![]std.Io.File.Handle {
        var handles: std.ArrayList(std.Io.File.Handle) = .empty;
        for (self.entries.items) |*entry| {
            const handle = handleOf(entry) orelse continue;
            try handles.append(arena, handle);
        }
        return handles.items;
    }

    fn awaitAny(self: *Hub, arena: std.mem.Allocator, wait_ns: u64) !bool {
        if (comptime !pollable) return false;
        var watched = std.ArrayList(std.posix.pollfd).empty;
        for (self.entries.items) |*entry| {
            const handle = handleOf(entry) orelse continue;
            try watched.append(arena, .{ .fd = handle, .events = std.posix.POLL.IN, .revents = 0 });
        }
        if (watched.items.len == 0) return false;
        const budget: i32 = @intCast(@min(wait_ns / std.time.ns_per_ms, std.math.maxInt(i32)));
        const awoken = std.posix.poll(watched.items, budget) catch 0;
        return awoken > 0;
    }

    fn drive(self: *Hub, share: u64) !void {
        for (self.entries.items) |*entry| {
            const wait = if (handleOf(entry) != null) 0 else share;
            _ = entry.session.pump(wait) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => {
                    self.endSubscriptions(entry, .stream_failed);
                },
            };
        }
    }

    pub fn pump(self: *Hub, allocator: std.mem.Allocator, wait_ns: u64) !void {
        self.expireHolds();
        self.reclaim();
        var scratch = std.heap.ArenaAllocator.init(allocator);
        defer scratch.deinit();
        const share = @max(wait_ns / @max(self.entries.items.len, 1), std.time.ns_per_ms);
        try self.drive(share);
        if (try self.awaitAny(scratch.allocator(), wait_ns)) try self.drive(share);
        for (self.entries.items) |*entry| {
            var events = std.ArrayList(contract.Event).empty;
            entry.session.drain(scratch.allocator(), &events) catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => {
                    self.endSubscriptions(entry, .stream_failed);
                    continue;
                },
            };
            for (events.items) |event| {
                const settled = terminal(self.allocator, event.line) catch |err| {
                    if (err != error.OutOfMemory) return err;
                    self.endSubscriptions(entry, .stream_failed);
                    return err;
                };
                self.remember(entry, event, settled) catch |err| {
                    self.endSubscriptions(entry, .stream_failed);
                    return err;
                };
                self.fanOut(entry, event, settled) catch |err| {
                    self.endSubscriptions(entry, .stream_failed);
                    return err;
                };
            }
        }
    }

    pub fn closeSessions(self: *Hub) Sweep {
        var summary = Sweep{};
        const deadline = self.clock() + self.shutdown_ns;
        while (self.entries.items.len > 0) {
            const now = self.clock();
            if (now >= deadline) {
                summary.unattempted = self.entries.items.len;
                break;
            }
            const share = (deadline - now) / @as(u64, @intCast(self.entries.items.len));
            var scratch = std.heap.ArenaAllocator.init(self.allocator);
            defer scratch.deinit();
            const outcome = self.settle(&self.entries.items[0], scratch.allocator(), now + share);
            summary.closed += @intFromBool(outcome.closed);
            if (outcome.closed) {
                summary.refused += 0;
            } else {
                summary.refused += 1;
                summary.refused_attempts += outcome.attempts;
            }
        }
        summary.sessions = summary.closed + summary.refused + summary.unattempted;
        return summary;
    }

    fn settle(self: *Hub, entry: *Entry, arena: std.mem.Allocator, until_ns: u64) Settled {
        var outcome = Settled{};
        var attempt: usize = 0;
        while (attempt < close_attempts) : (attempt += 1) {
            outcome.attempts += 1;
            self.stopRuns(entry, arena);
            if (entry.session.close()) |_| {
                entry.session_closed = true;
                outcome.closed = true;
                break;
            } else |err| {
                if (err == error.OutOfMemory) break;
            }
            const now = self.clock();
            if (now >= until_ns) break;
            const wait = @min(close_retry_wait_ns, until_ns - now);
            _ = entry.session.pump(wait) catch {};
        }
        self.releaseSession(entry);
        return outcome;
    }

    fn stopRuns(self: *Hub, entry: *Entry, arena: std.mem.Allocator) void {
        _ = self;
        var refusal = contract.Refusal{};
        const reported = entry.session.state(arena, &refusal) catch return;
        for (reported.active_runs) |active| {
            if (active.run_id.len == 0) continue;
            _ = entry.session.cancel(arena, active.run_id, &refusal) catch {};
        }
        if (reported.active_run_id) |named| {
            if (named.len == 0) {} else if (!namedIn(reported.active_runs, named)) {
                _ = entry.session.cancel(arena, named, &refusal) catch {};
            }
        }
    }

    fn discard(self: *Hub, subscription: *Subscription, ending: Ending) void {
        subscription.ending = ending;
        subscription.held = false;
        self.detach(subscription);
        for (self.subscriptions.items, 0..) |existing, index| {
            if (existing == subscription) {
                _ = self.subscriptions.orderedRemove(index);
                break;
            }
        }
        subscription.release(self.allocator);
        self.allocator.destroy(subscription);
    }

    fn release(self: *Hub, subscription: *Subscription, ending: Ending) void {
        subscription.ending = ending;
        subscription.held = false;
        subscription.detached = true;
        self.detach(subscription);
        subscription.forgetEvents(self.allocator);
    }

    pub fn reclaim(self: *Hub) void {
        var index: usize = 0;
        while (index < self.subscriptions.items.len) {
            const subscription = self.subscriptions.items[index];
            if (!spent(subscription)) {
                index += 1;
                continue;
            }
            _ = self.subscriptions.orderedRemove(index);
            _ = self.dropHold(subscription);
            subscription.release(self.allocator);
            self.allocator.destroy(subscription);
        }
    }

    fn spent(subscription: *const Subscription) bool {
        if (!subscription.detached) return false;
        if (subscription.held) return false;
        return subscription.replay.items.len == 0 and subscription.queue.items.len == 0;
    }

    pub fn toolSource(self: *const Hub, id: []const u8) ?contract.ConfiguredSource {
        for (self.tool_sources) |source| {
            if (std.mem.eql(u8, source.id, id)) return source;
        }
        return null;
    }

    fn namedIn(runs: []const oap_types.ActiveRun, run_id: []const u8) bool {
        for (runs) |active| {
            if (std.mem.eql(u8, active.run_id, run_id)) return true;
        }
        return false;
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

    fn ordinal(entry: *const Entry, run_id: []const u8) i64 {
        for (entry.cursors.items, 0..) |cursor, index| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return @intCast(index);
        }
        return -1;
    }

    fn noteRun(self: *Hub, entry: *Entry, run_id: []const u8, current: bool) !void {
        var fresh = false;
        _ = try self.cursorFor(entry, run_id, &fresh);
        if (!current) return;
        const owned = try self.allocator.dupe(u8, run_id);
        self.allocator.free(entry.run_id);
        entry.run_id = owned;
    }

    fn settledAt(self: *Hub, entry: *const Entry, run_id: []const u8) u64 {
        _ = self;
        for (entry.cursors.items) |cursor| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return cursor.terminal;
        }
        return 0;
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

    fn recordOutcome(self: *Hub, entry: *Entry, event: contract.Event) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const parsed = std.json.parseFromSliceLeaky(std.json.Value, scratch.allocator(), event.line, .{}) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return,
        };
        if (parsed != .object) return;
        const kind = parsed.object.get("type") orelse return;
        if (kind != .string) return;
        const outcome: []const u8 = if (std.mem.eql(u8, kind.string, "run.completed"))
            "completed"
        else if (std.mem.eql(u8, kind.string, "run.failed"))
            "failed"
        else if (std.mem.eql(u8, kind.string, "run.cancelled"))
            "cancelled"
        else
            return;
        const reply = try fullReplyText(scratch.allocator(), parsed.object.get("payload"));
        try self.recordTurn(entry, .assistant, try self.allocator.dupe(u8, reply), event.run_id, outcome);
    }

    fn terminal(allocator: std.mem.Allocator, line: []const u8) !bool {
        var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return false,
        };
        defer parsed.deinit();
        if (parsed.value != .object) return false;
        const kind = parsed.value.object.get("type") orelse return false;
        if (kind != .string) return false;
        return std.mem.eql(u8, kind.string, "run.completed") or
            std.mem.eql(u8, kind.string, "run.failed") or
            std.mem.eql(u8, kind.string, "run.cancelled");
    }

    fn remember(self: *Hub, entry: *Entry, event: contract.Event, settled: bool) !void {
        var fresh = false;
        const index = try self.cursorFor(entry, event.run_id, &fresh);
        if (!settled and
            !std.mem.eql(u8, entry.run_id, event.run_id) and
            ordinal(entry, event.run_id) > ordinal(entry, entry.run_id))
        {
            try self.noteRun(entry, event.run_id, true);
        }
        entry.cursors.items[index].latest = @max(entry.cursors.items[index].latest, event.sequence);
        if (settled) {
            entry.cursors.items[index].terminal = event.sequence;
            try self.recordOutcome(entry, event);
        }
        if (self.journal_capacity == 0) return;
        const line = try self.allocator.dupe(u8, event.line);
        errdefer self.allocator.free(line);
        try entry.journal.ensureUnusedCapacity(self.allocator, 1);
        if (entry.journal.items.len == self.journal_capacity) self.allocator.free(entry.journal.orderedRemove(0).line);
        entry.journal.appendAssumeCapacity(.{ .line = line, .run_id = entry.cursors.items[index].run_id, .sequence = event.sequence });
    }

    fn cursorFor(self: *Hub, entry: *Entry, run_id: []const u8, fresh: *bool) !usize {
        fresh.* = false;
        for (entry.cursors.items, 0..) |cursor, index| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return index;
        }
        const owned = try self.allocator.dupe(u8, run_id);
        errdefer self.allocator.free(owned);
        try entry.cursors.ensureUnusedCapacity(self.allocator, 1);
        entry.cursors.appendAssumeCapacity(.{ .run_id = owned });
        fresh.* = true;
        return entry.cursors.items.len - 1;
    }

    fn spentFor(self: *Hub, entry: *const Entry, run_id: []const u8) bool {
        if (run_id.len == 0) return false;
        if (std.mem.eql(u8, run_id, entry.run_id)) return false;
        return self.settledAt(entry, run_id) != 0;
    }

    fn lossRun(self: *Hub, entry: *const Entry, subscription: *const Subscription, dropped: []const u8) []const u8 {
        var newest = dropped;
        var newest_ordinal = ordinal(entry, dropped);
        var live = !self.spentFor(entry, dropped);
        var index: usize = 0;
        while (index < subscription.queue.items.len) : (index += 1) {
            self.considerLoss(entry, &newest, &newest_ordinal, &live, subscription.queue.items[index].run_id);
        }
        self.considerLoss(entry, &newest, &newest_ordinal, &live, subscription.run_id);
        self.considerLoss(entry, &newest, &newest_ordinal, &live, entry.run_id);
        return newest;
    }

    fn considerLoss(
        self: *Hub,
        entry: *const Entry,
        newest: *[]const u8,
        newest_ordinal: *i64,
        live: *bool,
        run_id: []const u8,
    ) void {
        if (run_id.len == 0) return;
        if (std.mem.eql(u8, run_id, newest.*)) return;
        const run_live = !self.spentFor(entry, run_id);
        if (live.* and !run_live) return;
        if (!live.* and run_live) {
            newest.* = run_id;
            newest_ordinal.* = ordinal(entry, run_id);
            live.* = true;
            return;
        }
        if (ordinal(entry, run_id) <= newest_ordinal.*) return;
        newest.* = run_id;
        newest_ordinal.* = ordinal(entry, run_id);
    }

    fn fanOut(self: *Hub, entry: *Entry, event: contract.Event, settled: bool) !void {
        var fresh = false;
        const cursor_index = try self.cursorFor(entry, event.run_id, &fresh);
        const owned_run = entry.cursors.items[cursor_index].run_id;
        var index: usize = 0;
        while (index < entry.subscribers.items.len) {
            const subscription = entry.subscribers.items[index];
            if (subscription.ending != .open) {
                _ = entry.subscribers.orderedRemove(index);
                continue;
            }
            if (subscription.queue.items.len >= self.stream_queue) {
                try self.markOverflow(entry, subscription, owned_run, subscription.highest);
                _ = entry.subscribers.orderedRemove(index);
                continue;
            }
            const copy = try self.allocator.dupe(u8, event.line);
            errdefer self.allocator.free(copy);
            try subscription.queue.ensureUnusedCapacity(self.allocator, 1);
            subscription.queue.appendAssumeCapacity(.{ .line = copy, .run_id = owned_run, .sequence = event.sequence, .terminal = settled });
            index += 1;
        }
    }

    fn seedOverflow(self: *Hub, subscription: *Subscription, run_id: []const u8, sequence: u64) !void {
        const owned = try self.allocator.dupe(u8, run_id);
        if (subscription.overflow_run.len > 0) self.allocator.free(subscription.overflow_run);
        subscription.overflow_run = owned;
        subscription.overflow_sequence = sequence;
        subscription.ending = .overflow;
    }

    fn markOverflow(
        self: *Hub,
        entry: *const Entry,
        subscription: *Subscription,
        dropped: []const u8,
        current_sequence: u64,
    ) !void {
        const lost = self.lossRun(entry, subscription, dropped);
        const sequence = if (std.mem.eql(u8, lost, subscription.run_id)) current_sequence else 0;
        return self.seedOverflow(subscription, lost, sequence);
    }

    fn findBound(self: *Hub, session_id: []const u8) ?*Bound {
        for (self.bound.items) |*record| {
            if (std.mem.eql(u8, record.session_id, session_id)) return record;
        }
        return null;
    }

    fn boundFor(self: *Hub, adapter_name: []const u8, session: contract.Session, version: []const u8, model: []const u8, reasoning_level: []const u8, compaction_policy: []const u8) !Bound {
        try self.bound.ensureUnusedCapacity(self.allocator, 1);
        const session_id = try self.allocator.dupe(u8, session.id());
        errdefer self.allocator.free(session_id);
        const owned_adapter = try self.allocator.dupe(u8, adapter_name);
        errdefer self.allocator.free(owned_adapter);
        const native_id = try self.allocator.dupe(u8, session.nativeId());
        errdefer self.allocator.free(native_id);
        const owned_version = try self.allocator.dupe(u8, version);
        errdefer self.allocator.free(owned_version);
        const owned_model = try self.allocator.dupe(u8, model);
        errdefer self.allocator.free(owned_model);
        const directory = if (self.find(adapter_name)) |registered| registered.directory else "";
        const owned_directory = try self.allocator.dupe(u8, directory);
        errdefer self.allocator.free(owned_directory);
        const owned_level = try self.allocator.dupe(u8, reasoning_level);
        errdefer self.allocator.free(owned_level);
        const owned_policy = try self.allocator.dupe(u8, compaction_policy);
        return .{ .session_id = session_id, .adapter_name = owned_adapter, .native_id = native_id, .version = owned_version, .model = owned_model, .directory = owned_directory, .reasoning_level = owned_level, .compaction_policy = owned_policy };
    }

    fn recordBinding(self: *Hub, action: binding.Action, session_id: []const u8, time_ms: i64) void {
        const store = self.bindings orelse return;
        const bound = self.findBound(session_id) orelse return;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const policy: ?binding.CompactionPolicy = if (bound.compaction_policy.len == 0) null else std.json.parseFromSliceLeaky(binding.CompactionPolicy, scratch.allocator(), bound.compaction_policy, .{ .ignore_unknown_fields = true }) catch null;
        store.append(.{ .action = action, .time_ms = time_ms, .record = .{
            .session_id = bound.session_id,
            .adapter = bound.adapter_name,
            .harness_version = bound.version,
            .native_session_id = bound.native_id,
            .directory = bound.directory,
            .model = bound.model,
            .reasoning_level = bound.reasoning_level,
            .compaction_policy = policy,
        } }) catch {};
    }

    fn keepBound(self: *Hub, record: Bound) void {
        if (self.findBound(record.session_id)) |existing| {
            existing.deinit(self.allocator);
            existing.* = record;
            return;
        }
        self.bound.appendAssumeCapacity(record);
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

    fn endSubscriptions(self: *Hub, entry: *Entry, ending: Ending) void {
        _ = self;
        for (entry.subscribers.items) |subscription| subscription.ending = ending;
        entry.subscribers.clearRetainingCapacity();
    }

    fn releaseSession(self: *Hub, entry: *Entry) void {
        self.recordBinding(.closed, entry.session_id, @intCast(self.clock() / std.time.ns_per_ms));
        self.endSubscriptions(entry, .session_closed);
        var index: usize = 0;
        while (index < self.holds.items.len) {
            const held = self.holds.items[index];
            if (!std.mem.eql(u8, held.subscription.session_id, entry.session_id)) {
                index += 1;
                continue;
            }
            _ = self.holds.orderedRemove(index);
            self.release(held.subscription, .session_closed);
        }
        self.removeSession(entry);
    }

    pub fn leave(self: *Hub, subscription: *Subscription) void {
        self.detach(subscription);
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
        const owned = try self.allocator.dupe(u8, named);
        if (subscription.run_id.len > 0) self.allocator.free(subscription.run_id);
        subscription.run_id = owned;
        subscription.highest = after;
        for (entry.journal.items) |kept| {
            if (!std.mem.eql(u8, kept.run_id, named)) continue;
            if (kept.sequence <= after) continue;
            if (subscription.replay.items.len >= self.stream_queue) {
                const reached = if (subscription.replay.items.len > 0)
                    subscription.replay.items[subscription.replay.items.len - 1].sequence
                else
                    after;
                try self.seedOverflow(subscription, named, reached);
                break;
            }
            const copy = try self.allocator.dupe(u8, kept.line);
            errdefer self.allocator.free(copy);
            try subscription.replay.append(self.allocator, .{
                .line = copy,
                .run_id = subscription.run_id,
                .sequence = kept.sequence,
                .terminal = try terminal(self.allocator, kept.line),
            });
        }
        const settled = self.settledAt(entry, named);
        if (settled > 0 and after >= settled) subscription.ending = .run_terminal;
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
    defer adapter.deinit();
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
    defer adapter.deinit();
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
    _ = try hub.submit(arena, "alpha", &request, "");
    try hub.pump(testing.allocator, 0);
    const replayed = try hub.subscribe(arena, "alpha", .{ .run_id = "run-1", .after = 1 });
    var drained: usize = 0;
    while (replayed.next()) |_| drained += 1;
    try testing.expect(drained > 0);

    const beta_state = try hub.state(arena, "beta");
    try testing.expectEqualStrings("beta", beta_state.session_id);
}

test "an adapter-assigned session id that is already taken is refused, not adopted twice" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const minted = try hub.open(arena, "memory", .{});
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());
    try testing.expectError(error.SessionExists, hub.open(arena, "memory", .{ .session_id = minted.session_id }));
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());

    const listed = try hub.sessions(arena);
    try testing.expectEqual(@as(usize, 1), listed.len);
    try testing.expectEqualStrings(minted.session_id, listed[0].session_id);
    try testing.expectEqualStrings(minted.session_id, (try hub.state(arena, minted.session_id)).session_id);
}

test "a cursor may name an admitted run whose events have not arrived yet" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "undrained" });
    const request = try submitFor(arena, "undrained");
    const admitted = try hub.submit(arena, "undrained", &request, "");
    try testing.expectEqual(oap_types.Admission.started, admitted.admission);

    const joined = try hub.subscribe(arena, opened.session_id, .{ .run_id = admitted.run_id.?, .after = 0 });
    try testing.expectEqualStrings(admitted.run_id.?, joined.run_id);
    try testing.expectEqual(Ending.open, joined.ending);
    try testing.expect(joined.next() == null);
    try hub.pump(testing.allocator, 0);
    try testing.expect(joined.next() != null);
}

test "a backend that cannot make progress ends its stream, and the session stays open" {
    var flaky = Flaky{ .allocator = testing.allocator, .fail_pump = true };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "flaky", .{ .session_id = "deadchild" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.stream_failed, subscription.ending);
    try testing.expectEqual(@as(usize, 0), flaky.closes);
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());
    const still = try hub.state(arena, "deadchild");
    try testing.expectEqualStrings("deadchild", still.session_id);

    try hub.close(arena, "deadchild");
    try testing.expectEqual(@as(usize, 1), flaky.closes);
    try testing.expectError(error.UnknownSession, hub.state(arena, "deadchild"));
}

test "the current run follows the events, so an unqualified cursor reaches a promoted run" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "promoted" });
    const first = try submitFor(arena, "promoted");
    const started = try hub.submit(arena, "promoted", &first, "");
    try hub.pump(testing.allocator, 0);

    var queued = first;
    queued.delivery = .queue;
    const waiting = try hub.submit(arena, "promoted", &queued, "");
    try testing.expectEqual(oap_types.Admission.queued, waiting.admission);
    try hub.pump(testing.allocator, 0);

    _ = try hub.cancel(arena, "promoted", started.run_id.?);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqualStrings(waiting.run_id.?, hub.entries.items[0].run_id);

    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "", .after = 0 });
    try testing.expectEqualStrings(waiting.run_id.?, resumed.run_id);
    const first_event = resumed.next().?;
    try testing.expectEqualStrings(waiting.run_id.?, first_event.run_id);
    try testing.expectEqual(@as(u64, 1), first_event.sequence);
}

test "a subscriber that falls behind is ended with a cursor on the run that overflowed" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 4, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "slow" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const first = try submitFor(arena, "slow");
    _ = try hub.submit(arena, "slow", &first, "");
    try hub.pump(testing.allocator, 0);
    const read = subscription.next().?;
    const last_read = read.sequence;
    try testing.expectEqualStrings("run-1", subscription.run_id);

    _ = try hub.cancel(arena, "slow", "run-1");
    const second = try submitFor(arena, "slow");
    const admitted = try hub.submit(arena, "slow", &second, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.overflow, subscription.ending);
    try testing.expectEqualStrings(admitted.run_id.?, subscription.overflow_run);
    try testing.expectEqual(@as(u64, 0), subscription.overflow_sequence);
    try testing.expect(last_read > 0);
    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = subscription.overflow_run, .after = subscription.overflow_sequence });
    try testing.expect(resumed.next() != null);
}

test "a replay that outgrows the mailbox names the replayed run, not the session's current one" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "replayed" });
    const first = try submitFor(arena, "replayed");
    const settled = try hub.submit(arena, "replayed", &first, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "replayed", settled.run_id.?);
    try hub.pump(testing.allocator, 0);

    const second = try submitFor(arena, "replayed");
    const current = try hub.submit(arena, "replayed", &second, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqualStrings(current.run_id.?, hub.entries.items[0].run_id);

    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = settled.run_id.?, .after = 0 });
    try testing.expectEqual(Ending.overflow, resumed.ending);
    try testing.expectEqualStrings(settled.run_id.?, resumed.overflow_run);
    try testing.expectEqual(@as(u64, 2), resumed.overflow_sequence);

    const again = try hub.subscribe(arena, opened.session_id, .{ .run_id = resumed.overflow_run, .after = resumed.overflow_sequence });
    try testing.expectEqualStrings(settled.run_id.?, again.overflow_run);
    const first_again = again.next().?;
    try testing.expectEqualStrings(settled.run_id.?, first_again.run_id);
    try testing.expectEqual(@as(u64, 3), first_again.sequence);
}

test "the overflow cursor is where the client stopped after draining, not where the queue filled" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 4, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "tail" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const first = try submitFor(arena, "tail");
    const admitted = try hub.submit(arena, "tail", &first, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.open, subscription.ending);

    var delivered: u64 = 0;
    var seen: usize = 0;
    while (subscription.next()) |event| {
        delivered = event.sequence;
        seen += 1;
        if (seen == 2) {
            _ = try hub.cancel(arena, "tail", admitted.run_id.?);
            try hub.pump(testing.allocator, 0);
        }
    }
    try testing.expectEqual(Ending.overflow, subscription.ending);
    try testing.expectEqualStrings(admitted.run_id.?, subscription.overflow_run);
    try testing.expectEqual(delivered, subscription.overflow_sequence);
    try testing.expect(subscription.overflow_sequence > 2);

    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = subscription.overflow_run, .after = subscription.overflow_sequence });
    try testing.expectEqual(Ending.open, resumed.ending);
    const tail = resumed.next().?;
    try testing.expectEqual(subscription.overflow_sequence + 1, tail.sequence);
}

test "a loss on a settled run names the live current run, which is what Go's candidate set reaches" {
    var flaky = Flaky{ .allocator = testing.allocator, .fail_drain = false };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 4, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "flaky", .{ .session_id = "current" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(Ending.open, subscription.ending);

    flaky.script[0] = .{ .run = "run-a", .sequence = 1, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.delta\",\"id\":\"a1\"}" };
    flaky.script[1] = .{ .run = "run-a", .sequence = 2, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"run.completed\",\"id\":\"a2\"}" };
    flaky.script_len = 2;
    try hub.pump(testing.allocator, 0);
    try testing.expectEqualStrings("run-a", hub.entries.items[0].run_id);
    try testing.expectEqual(Ending.open, subscription.ending);

    flaky.script[0] = .{ .run = "run-b", .sequence = 1, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.delta\",\"id\":\"b1\"}" };
    flaky.script[1] = .{ .run = "run-b", .sequence = 2, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.delta\",\"id\":\"b2\"}" };
    flaky.script[2] = .{ .run = "run-a", .sequence = 3, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.delta\",\"id\":\"a3\"}" };
    flaky.script_len = 3;
    try hub.pump(testing.allocator, 0);

    try testing.expectEqual(Ending.overflow, subscription.ending);
    try testing.expectEqualStrings("run-b", hub.entries.items[0].run_id);
    try testing.expectEqualStrings("run-b", subscription.overflow_run);
}

test "a loss on a newer run names the newer run rather than the one being read" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "newer" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const first = try submitFor(arena, "newer");
    const first_run = (try hub.submit(arena, "newer", &first, "")).run_id.?;
    try hub.pump(testing.allocator, 0);
    const read = subscription.next().?;
    try testing.expect(read.sequence > 0);
    try testing.expectEqualStrings(first_run, subscription.run_id);

    _ = try hub.cancel(arena, "newer", first_run);
    const second = try submitFor(arena, "newer");
    const second_run = (try hub.submit(arena, "newer", &second, "")).run_id.?;
    try hub.pump(testing.allocator, 0);
    try testing.expect(runNumber(first_run) < runNumber(second_run));
    try testing.expectEqual(Ending.overflow, subscription.ending);
    try testing.expectEqualStrings(second_run, subscription.overflow_run);
    try testing.expectEqual(@as(u64, 0), subscription.overflow_sequence);
    try testing.expectEqualStrings(first_run, subscription.run_id);
}

fn runNumber(run_id: []const u8) usize {
    if (!std.mem.startsWith(u8, run_id, "run-")) return 0;
    return std.fmt.parseInt(usize, run_id["run-".len..], 10) catch 0;
}

test "an overflowed hold is adopted so the adopter learns the cursor it lost" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .journal_capacity = 256, .hold_ns = 50 * std.time.ns_per_ms });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "starved" });
    const held = try hub.hold(arena, opened.session_id);
    try testing.expect(!held.expired(testClock()));
    const request = try submitFor(arena, "starved");
    _ = try hub.submit(arena, "starved", &request, "");
    try hub.pump(testing.allocator, 0);

    const adopted = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(Ending.overflow, adopted.ending);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
}

test "a finished subscription is reclaimed, so a long-lived hub's memory is bounded" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 4, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "churn" });

    const kept = try hub.subscribe(arena, opened.session_id, .{});
    const read = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(@as(usize, 2), hub.subscriptions.items.len);

    kept.close();
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
    try testing.expectEqual(read, hub.subscriptions.items[0]);
}

test "a subscription that closes with events queued leaves the hub with nothing outstanding" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "gone" });
    const leaving = try hub.subscribe(arena, opened.session_id, .{});
    const staying = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "gone");
    _ = try hub.submit(arena, "gone", &request, "");
    try hub.pump(testing.allocator, 0);
    try testing.expect(leaving.queue.items.len > 0);

    leaving.close();
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
    try testing.expectEqual(staying, hub.subscriptions.items[0]);
}

test "a resumed subscription ends at the terminal it replays" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "replayed-terminal" });
    const request = try submitFor(arena, "replayed-terminal");
    _ = try hub.submit(arena, "replayed-terminal", &request, "");
    var queued = request;
    queued.delivery = .queue;
    const waiting = try hub.submit(arena, "replayed-terminal", &queued, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "replayed-terminal", waiting.run_id.?);
    try hub.pump(testing.allocator, 0);

    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = waiting.run_id.?, .after = 0 });
    try testing.expectEqual(Ending.open, resumed.ending);
    var delivered: usize = 0;
    while (resumed.next()) |_| delivered += 1;
    try testing.expectEqual(@as(usize, 1), delivered);
    try testing.expectEqual(Ending.run_terminal, resumed.ending);
}

test "a nested type member in a tool result is not mistaken for an envelope type" {
    var flaky = Flaky{ .allocator = testing.allocator, .fail_drain = false };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "flaky", .{ .session_id = "nested" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    flaky.emit_line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"action.call.completed\",\"id\":\"e1\",\"payload\":{\"result\":{\"type\":\"run.cancelled\"}}}";
    try hub.pump(testing.allocator, 0);
    const nested = subscription.next().?;
    try testing.expect(std.mem.indexOf(u8, nested.line, "action.call.completed") != null);
    try testing.expectEqual(Ending.open, subscription.ending);
    try testing.expect(subscription.next() == null);

    flaky.emit_line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"run.completed\",\"id\":\"e2\"}";
    try hub.pump(testing.allocator, 0);
    _ = subscription.next().?;
    try testing.expectEqual(Ending.run_terminal, subscription.ending);
}

test "a run's terminal ends the stream even when a later run numbers higher" {
    var flaky = Flaky{ .allocator = testing.allocator, .fail_drain = false };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 32, .journal_capacity = 32 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "flaky", .{ .session_id = "spanning-runs" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    flaky.script[0] = .{ .run = "run-a", .sequence = 2, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"run.completed\",\"id\":\"a2\"}" };
    flaky.script[1] = .{ .run = "run-b", .sequence = 9, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"run.cancelled\",\"id\":\"b9\"}" };
    flaky.script_len = 2;
    try hub.pump(testing.allocator, 0);

    const first = subscription.next().?;
    try testing.expectEqualStrings("run-a", first.run_id);
    try testing.expectEqual(Ending.run_terminal, subscription.ending);
    while (subscription.next()) |_| {}
    try testing.expectEqualStrings("run-a", subscription.run_id);
}

test "a run's terminal envelope never becomes the session's current run" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    _ = try hub.open(arena, "memory", .{ .session_id = "terminal-run" });
    const first = try submitFor(arena, "terminal-run");
    const started = try hub.submit(arena, "terminal-run", &first, "");
    var queued = first;
    queued.delivery = .queue;
    const waiting = try hub.submit(arena, "terminal-run", &queued, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "terminal-run", waiting.run_id.?);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqualStrings(started.run_id.?, hub.entries.items[0].run_id);
}

test "the registry lists and names its adapters in sorted order" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("zulu", adapter.adapter());
    try hub.register("alpha", adapter.adapter());
    try hub.register("mike", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const named = try hub.names(arena);
    try testing.expectEqual(@as(usize, 3), named.len);
    try testing.expectEqualStrings("alpha", named[0]);
    try testing.expectEqualStrings("mike", named[1]);
    try testing.expectEqualStrings("zulu", named[2]);

    const listed = try hub.listing(arena);
    try testing.expectEqual(@as(usize, 3), listed.len);
    try testing.expectEqualStrings("alpha", listed[0].name);
    try testing.expectEqualStrings("mike", listed[1].name);
    try testing.expectEqualStrings("zulu", listed[2].name);
}

test "a replay larger than the mailbox seeds a cursor instead of growing without bound" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 3, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "wide-replay" });
    const request = try submitFor(arena, "wide-replay");
    _ = try hub.submit(arena, "wide-replay", &request, "");
    try hub.pump(testing.allocator, 0);

    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 0 });
    try testing.expectEqual(Ending.overflow, resumed.ending);
    try testing.expectEqualStrings("run-1", resumed.overflow_run);
    try testing.expectEqual(@as(usize, 3), resumed.replay.items.len);
    try testing.expectEqual(@as(u64, 3), resumed.overflow_sequence);
    var seen: usize = 0;
    while (resumed.next()) |_| seen += 1;
    try testing.expectEqual(@as(usize, 3), seen);

    const again = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = resumed.overflow_sequence });
    try testing.expect(again.next() != null);
    try testing.expectEqual(Ending.open, again.ending);
}

test "a registry document's journal capacity is read, and its zero keeps the default" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    var diagnostic = config.Diagnostic{};
    try hub.load(arena, config.File{ .adapters = &.{.{ .name = "memory", .kind = "memory", .journal_capacity = 12 }} }, .{ .context = &adapter, .make = scripted }, &diagnostic);
    try testing.expectEqual(@as(usize, 12), hub.journal_capacity);

    var zeroed = Hub.init(testing.allocator, testClock, .{});
    defer zeroed.deinit();
    try zeroed.load(arena, config.File{ .adapters = &.{.{ .name = "memory", .kind = "memory", .journal_capacity = 0 }} }, .{ .context = &adapter, .make = scripted }, &diagnostic);
    try testing.expectEqual(@as(usize, default_journal_capacity), zeroed.journal_capacity);

    var refused = Hub.init(testing.allocator, testClock, .{});
    defer refused.deinit();
    try testing.expectError(error.ConfigRefused, refused.load(arena, config.File{ .adapters = &.{.{ .name = "memory", .kind = "memory", .journal_capacity = -1 }} }, .{ .context = &adapter, .make = scripted }, &diagnostic));
    try testing.expectEqualStrings("config: adapter \"memory\": \"journal_capacity\" must not be negative", diagnostic.message);
    try testing.expectEqual(@as(usize, 0), refused.sessionCount());
}

test "a hub built with no journal capacity keeps nothing" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .journal_capacity = 0 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "unremembered" });
    const request = try submitFor(arena, "unremembered");
    _ = try hub.submit(arena, "unremembered", &request, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), hub.entries.items[0].journal.items.len);
    const live = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(Ending.open, live.ending);
}

fn scripted(context: *anyopaque, arena: std.mem.Allocator, entry: config.AdapterEntry) contract.Failure!contract.Adapter {
    _ = arena;
    _ = entry;
    const adapter: *memory.Adapter = @ptrCast(@alignCast(context));
    return adapter.adapter();
}

test "a cursor sitting on a settled run's terminal ends at once and hears nothing later" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 3 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "at-terminal" });
    const request = try submitFor(arena, "at-terminal");
    _ = try hub.submit(arena, "at-terminal", &request, "");
    var queued = request;
    queued.delivery = .queue;
    const waiting = try hub.submit(arena, "at-terminal", &queued, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "at-terminal", waiting.run_id.?);
    try hub.pump(testing.allocator, 0);

    const settled = try hub.subscribe(arena, opened.session_id, .{ .run_id = waiting.run_id.?, .after = 1 });
    try testing.expectEqual(Ending.run_terminal, settled.ending);
    try testing.expect(settled.next() == null);

    const later = try submitFor(arena, "at-terminal");
    _ = try hub.submit(arena, "at-terminal", &later, "");
    try hub.pump(testing.allocator, 0);
    try testing.expect(settled.next() == null);
    try testing.expectEqual(Ending.run_terminal, settled.ending);
}

test "a queued run's id can be named by a cursor before it has emitted" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "named-queue" });
    const first = try submitFor(arena, "named-queue");
    _ = try hub.submit(arena, "named-queue", &first, "");
    var queued = first;
    queued.delivery = .queue;
    const waiting = try hub.submit(arena, "named-queue", &queued, "");
    try hub.pump(testing.allocator, 0);

    const joined = try hub.subscribe(arena, opened.session_id, .{ .run_id = waiting.run_id.?, .after = 0 });
    try testing.expectEqualStrings(waiting.run_id.?, joined.run_id);
    try testing.expect(joined.next() == null);
    _ = try hub.cancel(arena, "named-queue", waiting.run_id.?);
    try hub.pump(testing.allocator, 0);
    try testing.expect(joined.next() != null);
}

test "a descriptor that loses its revision is listed with an error, not as healthy" {
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("fading", fadingAdapter(&fading_registered));
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const listed = try hub.listing(arena);
    try testing.expectEqual(@as(usize, 1), listed.len);
    try testing.expect(listed[0].failed);
    try testing.expectEqualStrings("adapter descriptor carries no capability revision", listed[0].message);
    try testing.expect(listed[0].capabilities == null);
    try testing.expectError(error.AdapterDescriptorUnbound, hub.probe("fading"));
}

test "a queued admission does not become the session's current run" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "queued" });
    const request = try submitFor(arena, "queued");
    const first = try hub.submit(arena, "queued", &request, "");
    try testing.expectEqual(oap_types.Admission.started, first.admission);
    try hub.pump(testing.allocator, 0);

    var queued = request;
    queued.delivery = .queue;
    const second = try hub.submit(arena, "queued", &queued, "");
    try testing.expectEqual(oap_types.Admission.queued, second.admission);
    try hub.pump(testing.allocator, 0);

    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "", .after = 1 });
    try testing.expectEqualStrings(first.run_id.?, resumed.run_id);
    try testing.expect(resumed.next() != null);
}

test "a subscriber that read nothing is told the run it lost, at that run's first sequence" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "silent" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "silent");
    _ = try hub.submit(arena, "silent", &request, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.overflow, subscription.ending);
    try testing.expectEqualStrings("run-1", subscription.overflow_run);
    try testing.expectEqual(@as(u64, 0), subscription.overflow_sequence);
}

test "a subscriber inside its bound receives every envelope" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "kept" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "kept");
    _ = try hub.submit(arena, "kept", &request, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.open, subscription.ending);
    var drained: usize = 0;
    while (subscription.next()) |_| drained += 1;
    try testing.expect(drained > 2);
}

test "a cursor older than the journal is a gap, never fake continuity" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 3 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "gap" });
    for (0..4) |_| {
        const request = try submitFor(arena, "gap");
        const admitted = try hub.submit(arena, "gap", &request, "");
        try hub.pump(testing.allocator, 0);
        _ = try hub.cancel(arena, "gap", admitted.run_id.?);
        try hub.pump(testing.allocator, 0);
    }
    const gapped = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 1 });
    try testing.expect(gapped.gap != null);
    try testing.expectEqual(@as(u64, 1), gapped.gap.?.requested_after);
    try testing.expect(gapped.gap.?.latest_available > 1);
    try testing.expect(gapped.next() == null);
}

test "a cursor within the journal replays the suffix, and an unrunnable one is refused" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "cursor" });
    const request = try submitFor(arena, "cursor");
    _ = try hub.submit(arena, "cursor", &request, "");
    try hub.pump(testing.allocator, 0);

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
    defer adapter.deinit();
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
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "compound", .subscribe = true });
    const subscription = opened.subscription.?;
    const request = try submitFor(arena, "compound");
    _ = try hub.submit(arena, "compound", &request, "");
    try hub.pump(testing.allocator, 0);
    const first = subscription.next().?;
    try testing.expectEqual(@as(u64, 1), first.sequence);
    try testing.expect(std.mem.indexOf(u8, first.line, "run.started") != null);
    try testing.expectEqual(Ending.open, subscription.ending);
}

test "a held subscription is adopted by the request that follows" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "held" });
    const held = try hub.hold(arena, opened.session_id);
    try testing.expect(!held.expired(testClock()));
    const request = try submitFor(arena, "held");
    _ = try hub.submit(arena, "held", &request, "");
    try hub.pump(testing.allocator, 0);
    const adopted = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expect(!adopted.held);
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
    const first = adopted.next().?;
    try testing.expectEqual(@as(u64, 1), first.sequence);
}

test "a hold nothing adopts is released when its window closes" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .hold_ns = 50 * std.time.ns_per_ms });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "abandoned" });
    const held = try hub.hold(arena, opened.session_id);
    try testing.expect(!held.expired(testClock()));
    try testing.expectEqual(@as(usize, 1), hub.holds.items.len);
    hub.expireHolds();
    try testing.expectEqual(@as(usize, 1), hub.holds.items.len);
    tick(100 * std.time.ns_per_ms);
    try testing.expect(held.expired(testClock()));
    hub.expireHolds();
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try testing.expectEqual(@as(usize, 0), hub.entries.items[0].subscribers.items.len);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), hub.subscriptions.items.len);

    const fresh = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(Ending.open, fresh.ending);
}

test "an expired hold with events queued leaves the hub with nothing outstanding" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .hold_ns = 50 * std.time.ns_per_ms });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "queued" });
    const held = try hub.hold(arena, opened.session_id);
    const request = try submitFor(arena, "queued");
    _ = try hub.submit(arena, "queued", &request, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.holds.items.len);
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);

    tick(100 * std.time.ns_per_ms);
    try testing.expect(held.expired(testClock()));
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try testing.expectEqual(@as(usize, 0), hub.entries.items[0].subscribers.items.len);
    try testing.expectEqual(@as(usize, 0), hub.subscriptions.items.len);
}

test "a cursor-bearing subscription does not adopt a hold, and releases it" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .hold_ns = 50 * std.time.ns_per_ms, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "cursored" });
    _ = try hub.hold(arena, opened.session_id);
    const request = try submitFor(arena, "cursored");
    _ = try hub.submit(arena, "cursored", &request, "");
    try hub.pump(testing.allocator, 0);
    const replayed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 1 });
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
    try testing.expectEqual(Ending.open, replayed.ending);
}

test "closing a session ends every subscription under it, and releases the session" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
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
    try testing.expectError(error.UnknownSession, hub.subscribe(arena, opened.session_id, .{}));
    const request = try submitFor(arena, "closing");
    try testing.expectError(error.UnknownSession, hub.submit(arena, "closing", &request, ""));
    try testing.expectError(error.UnknownSession, hub.state(arena, "absent"));
}

test "a subscription delivers the events it had queued before it reports the session closed" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "drained" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "drained");
    const started = try hub.submit(arena, "drained", &request, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "drained", started.run_id.?);
    try hub.pump(testing.allocator, 0);
    const queued = subscription.queue.items.len;
    try testing.expect(queued > 0);

    try hub.close(arena, opened.session_id);
    try testing.expectEqual(Ending.session_closed, subscription.ending);
    var delivered: usize = 0;
    var last: []const u8 = "";
    while (subscription.next()) |event| {
        delivered += 1;
        last = try arena.dupe(u8, event.line);
    }
    try testing.expectEqual(queued, delivered);
    try testing.expect(try Hub.terminal(arena, last));
    try testing.expectEqual(Ending.session_closed, subscription.ending);
    subscription.close();
    try hub.pump(testing.allocator, 0);
}

test "a released session's id is free again, and its memory is gone" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "reused" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "reused");
    const started = try hub.submit(arena, "reused", &request, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "reused", started.run_id.?);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());
    try testing.expect(hub.entries.items[0].journal.items.len > 0);
    try testing.expect(hub.entries.items[0].cursors.items.len > 0);
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);

    try hub.close(arena, opened.session_id);
    try testing.expectEqual(Ending.session_closed, subscription.ending);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
    try testing.expectEqual(@as(usize, 0), hub.entries.items.len);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);

    const again = try hub.open(arena, "memory", .{ .session_id = "reused" });
    try testing.expectEqualStrings("reused", again.session_id);
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());

    subscription.close();
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), hub.subscriptions.items.len);
}

test "a hold its session closes under is released, so the next pump reclaims it" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "closing", .subscribe = true });
    _ = try hub.holdSubscription(opened.subscription.?);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
    try hub.close(arena, "closing");
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), hub.subscriptions.items.len);
}

test "a released session's hold does not follow its id into the next session" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64, .hold_ns = 10 * std.time.ns_per_s });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "twice" });
    const held = try hub.hold(arena, opened.session_id);
    try testing.expect(!held.expired(testClock()));
    try testing.expectEqual(@as(usize, 1), hub.holds.items.len);
    const kept = hub.holds.items[0].subscription;
    try testing.expect(kept.held);

    try hub.close(arena, opened.session_id);
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try testing.expectEqual(Ending.session_closed, kept.ending);
    try testing.expect(!kept.held);

    const again = try hub.open(arena, "memory", .{ .session_id = "twice" });
    const fresh = try hub.subscribe(arena, again.session_id, .{});
    try testing.expectEqual(Ending.open, fresh.ending);
    try testing.expect(!fresh.held);
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);
}

test "close hands its entry over under allocation failure" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            defer adapter.deinit();
            var hub = Hub.init(gpa, testClock, .{ .stream_queue = 4, .journal_capacity = 4 });
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            const arena = scratch.allocator();

            const opened = try hub.open(arena, "memory", .{ .session_id = "failing-close" });
            _ = try hub.subscribe(arena, opened.session_id, .{});
            try hub.close(arena, opened.session_id);
            try testing.expectEqual(@as(usize, 0), hub.sessionCount());
        }
    }.attempt, .{});
}

test "a close refuses while a run is live, and a released session is unknown" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "busy" });
    const request = try submitFor(arena, "busy");
    const admitted = try hub.submit(arena, "busy", &request, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectError(error.RunActive, hub.close(arena, opened.session_id));

    _ = try hub.cancel(arena, "busy", admitted.run_id.?);
    try hub.close(arena, "busy");
    try testing.expectError(error.UnknownSession, hub.state(arena, "busy"));
    try testing.expectError(error.UnknownSession, hub.close(arena, "busy"));
    try testing.expectError(error.UnknownSession, hub.close(arena, "busy"));
}

test "a subscription ends after it is handed the run's terminal envelope" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "terminal" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "terminal");
    _ = try hub.submit(arena, "terminal", &request, "");
    var queued = request;
    queued.delivery = .queue;
    const waiting = try hub.submit(arena, "terminal", &queued, "");
    try testing.expectEqual(oap_types.Admission.queued, waiting.admission);
    try hub.pump(testing.allocator, 0);

    _ = try hub.cancel(arena, "terminal", waiting.run_id.?);
    try hub.pump(testing.allocator, 0);

    while (subscription.next()) |_| {}
    try testing.expectEqual(Ending.run_terminal, subscription.ending);
    try testing.expect(subscription.next() == null);
}

test "a session-scoped request may not address another session" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "scoped" });
    const elsewhere = try submitFor(arena, "elsewhere");
    try testing.expectError(error.ScopeMismatch, hub.submit(arena, opened.session_id, &elsewhere, ""));
    try testing.expectError(error.RunNotFound, hub.cancel(arena, opened.session_id, "run-9"));
}

test "a resolution may not answer a gate opened on another session" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 64, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "gatekeeper" });
    const foreign = oap_types.PermissionResolveRequest{
        .interaction_id = "permission-2",
        .requested_by = memory.endpoint_id,
        .responded_by = "user",
        .session_id = "somebody-else",
        .run_id = "run-1",
        .granted = true,
        .choice_id = "approve",
    };
    try testing.expectError(error.ScopeMismatch, hub.resolve(arena, opened.session_id, .{ .permission = &foreign }));
    const own = oap_types.PermissionResolveRequest{
        .interaction_id = "permission-2",
        .requested_by = memory.endpoint_id,
        .responded_by = "user",
        .session_id = opened.session_id,
        .run_id = "run-1",
        .granted = true,
        .choice_id = "approve",
    };
    try testing.expectError(error.RunNotFound, hub.resolve(arena, opened.session_id, .{ .permission = &own }));
    const foreign_input = oap_types.UserInputResolveRequest{
        .interaction_id = "input-3",
        .requested_by = memory.endpoint_id,
        .responded_by = "user",
        .session_id = "somebody-else",
        .run_id = "run-1",
        .answers = &.{},
    };
    try testing.expectError(error.ScopeMismatch, hub.resolve(arena, opened.session_id, .{ .input = &foreign_input }));
}

test "a cursor may name the overflow a hold reported, which the discard just freed" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .journal_capacity = 256, .hold_ns = 50 * std.time.ns_per_ms });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "resumer" });
    _ = try hub.hold(arena, opened.session_id);
    const request = try submitFor(arena, "resumer");
    _ = try hub.submit(arena, "resumer", &request, "");
    try hub.pump(testing.allocator, 0);

    try testing.expectEqual(@as(usize, 1), hub.holds.items.len);
    const resumed = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 1 });
    try testing.expectEqual(@as(usize, 0), hub.holds.items.len);
    try testing.expectEqualStrings("run-1", resumed.run_id);
    try testing.expectEqual(@as(u64, 1), resumed.highest);
    const first = resumed.next().?;
    try testing.expectEqual(@as(u64, 2), first.sequence);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);
}

test "a catalog is checked before it is served, and stamped with its revision" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
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

test "a subscribing open is gated on the revision the host asked for" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    try testing.expectError(error.StaleCapabilities, hub.open(arena, "memory", .{
        .session_id = "stale",
        .subscribe = true,
        .capability_revision = "reference-memory-v10",
    }));
    try testing.expect(hub.findSession("stale") == null);

    const matched = try hub.open(arena, "memory", .{
        .session_id = "matched",
        .subscribe = true,
        .capability_revision = "reference-memory-v17",
    });
    try testing.expectEqualStrings("reference-memory-v17", matched.revision);

    const unstated = try hub.open(arena, "memory", .{
        .session_id = "unstated",
        .subscribe = true,
    });
    try testing.expectEqualStrings("reference-memory-v17", unstated.revision);
}

test "the revision gate fires for a subscribing, reopening or attaching open, and for no other" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const plain = try hub.open(arena, "memory", .{
        .session_id = "plain",
        .capability_revision = "reference-memory-v10",
    });
    try testing.expectEqualStrings("plain", plain.session_id);

    _ = try hub.open(arena, "memory", .{ .session_id = "taken" });
    try testing.expectError(error.StaleCapabilities, hub.open(arena, "memory", .{
        .session_id = "taken",
        .subscribe = true,
        .capability_revision = "reference-memory-v10",
    }));

    try testing.expectError(error.StaleCapabilities, hub.open(arena, "memory", .{
        .session_id = "reopening",
        .reopen = true,
        .capability_revision = "reference-memory-v10",
    }));

    try testing.expectError(error.StaleCapabilities, hub.open(arena, "memory", .{
        .session_id = "attaching",
        .tool_sources_json = "[{\"kind\":\"endpoint\",\"id\":\"e1\"}]",
        .capability_revision = "reference-memory-v10",
    }));
    try testing.expect(hub.findSession("attaching") == null);

    const spellings = [_][]const u8{ "[]", "[ ]", "[\n]", "[\r\n \t]", " [ ] " };
    var index: usize = 0;
    for (spellings) |spelling| {
        var name_buffer: [16]u8 = undefined;
        const name = try std.fmt.bufPrint(&name_buffer, "empty-{d}", .{index});
        index += 1;
        const empty = try hub.open(arena, "memory", .{
            .session_id = name,
            .tool_sources_json = spelling,
            .capability_revision = "reference-memory-v10",
        });
        try testing.expectEqualStrings(name, empty.session_id);
    }

    try testing.expectError(error.UnsupportedFeature, hub.open(arena, "memory", .{
        .session_id = "provided",
        .tools_json = "[{\"name\":\"echo\",\"description\":\"d\"}]",
        .capability_revision = "reference-memory-v10",
    }));

    for ([_][]const u8{ "[{}]", "[null]", "[0]", "true", "[\"\"]" }) |spelling| {
        var name_buffer: [16]u8 = undefined;
        const name = try std.fmt.bufPrint(&name_buffer, "short-{d}", .{index});
        index += 1;
        try testing.expectError(error.StaleCapabilities, hub.open(arena, "memory", .{
            .session_id = name,
            .tool_sources_json = spelling,
            .capability_revision = "reference-memory-v10",
        }));
    }
}

test "an open's metadata reaches the adapter" {
    var flaky = Flaky{ .allocator = testing.allocator };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    try testing.expect(flaky.saw_metadata == null);
    _ = try hub.open(arena, "flaky", .{ .session_id = "bare" });
    try testing.expect(flaky.saw_metadata == null);

    const metadata = try std.json.parseFromSlice(std.json.Value, arena, "{\"tenant\":\"acme\",\"attempt\":3}", .{});
    _ = try hub.open(arena, "flaky", .{ .session_id = "labelled", .metadata = metadata.value });
    const seen = flaky.saw_metadata.?;
    try testing.expectEqualStrings("acme", seen.object.get("tenant").?.string);
    try testing.expectEqual(@as(i64, 3), seen.object.get("attempt").?.integer);
}

test "a catalog is stamped with the revision its lister served it under" {
    var flaky = Flaky{ .allocator = testing.allocator, .lister_revision = "lister-v9" };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "flaky", .{ .session_id = "stamped" });
    const models = try hub.models(arena, opened.session_id, &.{ .session_id = opened.session_id });
    try testing.expectEqualStrings("lister-v9", models.revision);
    const tools = try hub.tools(arena, opened.session_id, &.{ .session_id = opened.session_id });
    try testing.expectEqualStrings("lister-v9", tools.revision);
    flaky.lister_revision = "";
    try testing.expectError(error.CatalogUnlabelled, hub.models(arena, opened.session_id, &.{ .session_id = opened.session_id }));
    try testing.expectError(error.CatalogUnlabelled, hub.tools(arena, opened.session_id, &.{ .session_id = opened.session_id }));
}

test "the shutdown sweep cancels a live run before it releases the session" {
    var flaky = Flaky{ .allocator = testing.allocator, .active_run = "run-1" };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    _ = try hub.open(arena, "flaky", .{ .session_id = "settled" });
    _ = try hub.open(arena, "flaky", .{ .session_id = "second" });
    try testing.expectEqual(@as(usize, 0), flaky.cancels);

    const summary = hub.closeSessions();
    try testing.expectEqual(@as(usize, 2), flaky.cancels);
    try testing.expectEqual(@as(usize, 2), flaky.closes);
    try testing.expect(summary.clean());
    try testing.expectEqual(@as(usize, 2), summary.closed);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
    try testing.expectError(error.UnknownSession, hub.state(arena, "settled"));
    try testing.expectError(error.UnknownSession, hub.state(arena, "second"));
}

test "closeSessions settles every session" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
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
    const summary = hub.closeSessions();
    try testing.expectEqual(@as(usize, 3), summary.closed);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
    for (names) |name| try testing.expectError(error.UnknownSession, hub.state(arena, name));
    try testing.expectEqual(@as(usize, 0), (try hub.sessions(arena)).len);
}

test "the registry refuses a name twice and an unlabelled descriptor" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try testing.expectError(error.AdapterDescriptorUnbound, hub.register("bare", bareAdapter()));
    try hub.register("memory", adapter.adapter());
    try testing.expectError(error.AdapterExists, hub.register("memory", adapter.adapter()));
}

test "the listing carries every registered adapter with its revision" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
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
            defer adapter.deinit();
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
            defer adapter.deinit();
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
            defer adapter.deinit();
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
            defer adapter.deinit();
            var hub = Hub.init(gpa, testClock, .{ .stream_queue = 4, .journal_capacity = 4 });
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            const arena = scratch.allocator();
            const opened = try hub.open(arena, "memory", .{ .session_id = "fanout" });
            _ = try hub.subscribe(arena, opened.session_id, .{});
            const request = try submitFor(arena, "fanout");
            _ = try hub.submit(arena, "fanout", &request, "");
            try hub.pump(gpa, 0);
        }
    }.attempt, .{});
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            defer adapter.deinit();
            var hub = Hub.init(gpa, testClock, .{ .stream_queue = 4, .journal_capacity = 4 });
            defer hub.deinit();
            try hub.register("memory", adapter.adapter());
            var scratch = std.heap.ArenaAllocator.init(gpa);
            defer scratch.deinit();
            const arena = scratch.allocator();
            const opened = try hub.open(arena, "memory", .{ .session_id = "replayed" });
            const request = try submitFor(arena, "replayed");
            _ = try hub.submit(arena, "replayed", &request, "");
            try hub.pump(gpa, 0);
            _ = try hub.subscribe(arena, opened.session_id, .{ .run_id = "run-1", .after = 0 });
        }
    }.attempt, .{});
}

test "hold, listing and sessions hand off under allocation failure" {
    try testing.checkAllAllocationFailures(testing.allocator, struct {
        fn attempt(gpa: std.mem.Allocator) !void {
            var adapter = memory.Adapter.init(gpa);
            defer adapter.deinit();
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

const Scripted = struct {
    run: []const u8,
    sequence: u64,
    line: []const u8,
};

const Flaky = struct {
    allocator: std.mem.Allocator,
    lister_revision: []const u8 = "lister-v9",
    saw_metadata: ?std.json.Value = null,
    keep: std.heap.ArenaAllocator = undefined,
    session: contract.Session = undefined,
    owned_id: []const u8 = "",
    closes: usize = 0,
    cancels: usize = 0,
    emit_line: ?[]const u8 = null,
    script: [8]?Scripted = .{ null, null, null, null, null, null, null, null },
    script_len: usize = 0,
    active_run: []const u8 = "",
    fail_drain: bool = true,
    fail_pump: bool = false,
    read_end: ?std.posix.fd_t = null,
    write_end: ?std.posix.fd_t = null,
    waits: [8]u64 = .{ 0, 0, 0, 0, 0, 0, 0, 0 },
    wait_len: usize = 0,
    reported: bool = true,

    fn adapter(self: *Flaky) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = flakyProbe, .open = flakyOpen } };
    }
};

fn flakyModels(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!contract.Catalog {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    _ = refusal;
    const models = try arena.dupe(oap_types.ModelDescriptor, &.{.{ .id = "flaky-model", .default = true }});
    return .{ .revision = self.lister_revision, .response = .{
        .session_id = request.session_id,
        .current_model_id = "flaky-model",
        .models = models,
    } };
}

fn flakyTools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!contract.ToolSet {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    _ = refusal;
    _ = arena;
    return .{ .revision = self.lister_revision, .response = .{ .session_id = request.session_id } };
}

fn flakyDescriptor() contract.Descriptor {
    return .{
        .endpoint = .{ .id = "flaky", .name = "Flaky", .version = "0.1", .adapter = "script" },
        .capability_revision = "flaky-v1",
        .features = &.{},
    };
}

fn flakyProbe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
    _ = ptr;
    _ = refusal;
    return flakyDescriptor();
}

fn flakyOpen(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    _ = arena;
    _ = refusal;
    self.saw_metadata = request.metadata;
    const id = try self.keep.allocator().dupe(u8, if (request.session_id.len > 0) request.session_id else "flaky");
    self.session = .{ .ptr = self, .vtable = &.{
        .id = flakyId,
        .state = flakyState,
        .submit = flakySubmit,
        .resolve = flakyResolve,
        .cancel = flakyCancel,
        .pump = flakyPump,
        .drain = flakyDrain,
        .activity = flakyActivity,
        .close = flakyClose,
        .readable = flakyReadable,
        .models = flakyModels,
        .tools = flakyTools,
    } };
    self.owned_id = id;
    return self.session;
}

fn flakyReadable(ptr: *anyopaque) ?std.Io.File.Handle {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    if (!self.reported) return null;
    return self.read_end;
}

fn flakyId(ptr: *anyopaque) []const u8 {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    return self.owned_id;
}

fn flakyState(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    _ = refusal;
    if (self.active_run.len == 0) return .{ .session_id = try arena.dupe(u8, self.owned_id), .status = .idle };
    const run = try arena.dupe(u8, self.active_run);
    const runs = try arena.alloc(oap_types.ActiveRun, 1);
    runs[0] = .{ .run_id = run, .status = .running, .relationship = "primary" };
    return .{
        .session_id = try arena.dupe(u8, self.owned_id),
        .status = .running,
        .active_run_id = run,
        .active_runs = runs,
    };
}

fn flakySubmit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
    _ = envelope_id;
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    _ = request;
    _ = refusal;
    return .{ .session_id = try arena.dupe(u8, self.owned_id), .accepted = true, .submission_id = try arena.dupe(u8, "s1"), .requested_delivery = .auto, .effective_delivery = .start, .admission = .started, .run_id = try arena.dupe(u8, "run-1"), .status = .running };
}

fn flakyResolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
    _ = ptr;
    _ = arena;
    _ = resolution;
    _ = refusal;
}

fn flakyCancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    _ = refusal;
    self.cancels += 1;
    return .{ .session_id = try arena.dupe(u8, self.owned_id), .run_id = try arena.dupe(u8, run_id), .accepted = true, .status = .cancelling };
}

fn flakyPump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    if (self.wait_len < self.waits.len) {
        self.waits[self.wait_len] = wait_ns;
        self.wait_len += 1;
    }
    if (self.fail_pump) return error.BackendFailed;
    return false;
}

fn flakyDrain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    if (self.fail_drain) return error.BackendFailed;
    if (self.script_len > 0) {
        for (self.script[0..self.script_len]) |entry_point| {
            const item = entry_point.?;
            const copied = try allocator.dupe(u8, item.line);
            const run = try allocator.dupe(u8, item.run);
            try out.append(allocator, .{ .line = copied, .run_id = run, .sequence = item.sequence });
        }
        self.script_len = 0;
        return;
    }
    const line = self.emit_line orelse return;
    self.emit_line = null;
    const copied = try allocator.dupe(u8, line);
    const run = try allocator.dupe(u8, "run-1");
    try out.append(allocator, .{ .line = copied, .run_id = run, .sequence = 1 });
}

fn flakyActivity(ptr: *anyopaque) contract.Activity {
    _ = ptr;
    return .idle;
}

fn flakyClose(ptr: *anyopaque, force: bool) contract.Failure!void {
    _ = force;
    const self: *Flaky = @ptrCast(@alignCast(ptr));
    self.closes += 1;
}

const Stubborn = struct {
    session_id: []const u8 = "stubborn",
    settle_after: usize = 0,
    running: bool = true,
    state_ns: u64 = 0,
    cancels: usize = 0,
    closes: usize = 0,
    refusals: usize = 0,
    torn_down: usize = 0,
    pumped_ns: u64 = 0,
    waits: [8]u64 = .{ 0, 0, 0, 0, 0, 0, 0, 0 },
    wait_len: usize = 0,

    fn adapter(self: *Stubborn) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = flakyProbe, .open = stubbornOpen } };
    }
};

fn stubbornOpen(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    _ = arena;
    _ = request;
    _ = refusal;
    self.running = true;
    return .{ .ptr = self, .vtable = &.{
        .id = stubbornId,
        .state = stubbornState,
        .submit = stubbornSubmit,
        .resolve = flakyResolve,
        .cancel = stubbornCancel,
        .pump = stubbornPump,
        .drain = stubbornDrain,
        .activity = stubbornActivity,
        .close = stubbornClose,
    } };
}

fn stubbornId(ptr: *anyopaque) []const u8 {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    return self.session_id;
}

fn stubbornState(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    _ = refusal;
    if (self.state_ns > 0) tick(self.state_ns);
    const session_id = try arena.dupe(u8, self.session_id);
    if (!self.running) return .{ .session_id = session_id, .status = .idle };
    const run = try arena.dupe(u8, "run-1");
    const runs = try arena.dupe(oap_types.ActiveRun, &.{.{ .run_id = run, .status = .running, .relationship = "primary" }});
    return .{ .session_id = session_id, .status = .running, .active_run_id = run, .active_runs = runs };
}

fn stubbornSubmit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
    _ = envelope_id;
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    _ = request;
    _ = refusal;
    self.running = true;
    const session_id = try arena.dupe(u8, self.session_id);
    const submission_id = try arena.dupe(u8, "s1");
    const run_id = try arena.dupe(u8, "run-1");
    return .{
        .session_id = session_id,
        .accepted = true,
        .submission_id = submission_id,
        .requested_delivery = .auto,
        .effective_delivery = .start,
        .admission = .started,
        .run_id = run_id,
        .status = .running,
    };
}

fn stubbornCancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    _ = refusal;
    self.cancels += 1;
    if (self.settle_after == 0) self.running = false else self.settle_after -= 1;
    const session_id = try arena.dupe(u8, self.session_id);
    const named = try arena.dupe(u8, run_id);
    return .{ .session_id = session_id, .run_id = named, .accepted = true, .status = .cancelling };
}

fn stubbornPump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    self.pumped_ns += wait_ns;
    if (self.wait_len < self.waits.len) {
        self.waits[self.wait_len] = wait_ns;
        self.wait_len += 1;
    }
    tick(wait_ns);
    return false;
}

fn stubbornDrain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
    _ = ptr;
    _ = allocator;
    _ = out;
}

fn stubbornActivity(ptr: *anyopaque) contract.Activity {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    return if (self.running) .running else .idle;
}

fn stubbornClose(ptr: *anyopaque, force: bool) contract.Failure!void {
    const self: *Stubborn = @ptrCast(@alignCast(ptr));
    if (!force and self.running) {
        self.refusals += 1;
        return error.RunActive;
    }
    if (force) {
        self.torn_down += 1;
        return;
    }
    self.closes += 1;
}

test "a close that refuses while a run is live is retried through a cancel until it lands" {
    var stubborn = Stubborn{ .session_id = "held", .settle_after = 1 };
    var hub = Hub.init(testing.allocator, testClock, .{ .shutdown_ns = 60 * std.time.ns_per_s });
    defer hub.deinit();
    try hub.register("stubborn", stubborn.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "stubborn", .{ .session_id = "held" });

    const summary = hub.closeSessions();
    try testing.expect(summary.clean());
    try testing.expectEqual(@as(usize, 1), summary.closed);
    try testing.expectEqual(@as(usize, 2), stubborn.cancels);
    try testing.expectEqual(@as(usize, 1), stubborn.refusals);
    try testing.expectEqual(@as(usize, 1), stubborn.closes);
    try testing.expectEqual(@as(usize, 0), stubborn.torn_down);
    try testing.expect(stubborn.waits[0] == close_retry_wait_ns);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
    try testing.expectError(error.UnknownSession, hub.state(arena, opened.session_id));
}

test "a close that never stops refusing is given the attempts the draft names, and the session is torn down rather than left behind" {
    var stubborn = Stubborn{ .session_id = "wedged", .settle_after = std.math.maxInt(usize) };
    var hub = Hub.init(testing.allocator, testClock, .{ .shutdown_ns = 60 * std.time.ns_per_s });
    defer hub.deinit();
    try hub.register("stubborn", stubborn.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    _ = try hub.open(arena, "stubborn", .{ .session_id = "wedged" });

    const summary = hub.closeSessions();
    try testing.expect(!summary.clean());
    try testing.expectEqual(@as(usize, 1), summary.refused);
    try testing.expectEqual(@as(usize, 0), summary.unattempted);
    try testing.expectEqual(@as(usize, 0), summary.closed);
    try testing.expectEqual(close_attempts, stubborn.refusals);
    try testing.expectEqual(@as(usize, 0), stubborn.closes);
    try testing.expectEqual(@as(usize, 1), stubborn.torn_down);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
}

test "one wedged session is given a share of the window, and the session beside it is still closed" {
    var stubborn = Stubborn{ .session_id = "wedged", .settle_after = std.math.maxInt(usize) };
    var settles = memory.Adapter.init(testing.allocator);
    defer settles.deinit();
    const window = 50 * std.time.ns_per_ms;
    var hub = Hub.init(testing.allocator, testClock, .{ .shutdown_ns = window });
    defer hub.deinit();
    try hub.register("stubborn", stubborn.adapter());
    try hub.register("memory", settles.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    _ = try hub.open(arena, "stubborn", .{ .session_id = "wedged" });
    _ = try hub.open(arena, "memory", .{ .session_id = "settles" });

    const summary = hub.closeSessions();
    try testing.expectEqual(@as(usize, 2), summary.sessions);
    try testing.expectEqual(@as(usize, 1), summary.closed);
    try testing.expectEqual(@as(usize, 1), summary.refused);
    try testing.expectEqual(@as(usize, 0), summary.unattempted);
    try testing.expectEqual(@as(usize, close_attempts - 1), summary.refused_attempts);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
    try testing.expectError(error.UnknownSession, hub.state(arena, "settles"));
    try testing.expectEqual(window / 2, stubborn.pumped_ns);
    try testing.expectEqual(@as(u64, @intCast(window / 2)), stubborn.waits[0]);
    try testing.expect(stubborn.pumped_ns > 0);
}

test "the sweep waits no longer than the window it was given" {
    var stubborn = Stubborn{ .session_id = "wedged", .settle_after = std.math.maxInt(usize) };
    const window = 50 * std.time.ns_per_ms;
    var hub = Hub.init(testing.allocator, testClock, .{ .shutdown_ns = window });
    defer hub.deinit();
    try hub.register("stubborn", stubborn.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    _ = try hub.open(arena, "stubborn", .{ .session_id = "wedged" });

    const summary = hub.closeSessions();
    try testing.expect(!summary.clean());
    try testing.expect(stubborn.refusals < close_attempts);
    try testing.expectEqual(window, stubborn.pumped_ns);
    try testing.expectEqual(@as(usize, 0), hub.sessionCount());
}

test "a session whose own state call overruns its share leaves the rest unattempted, and the sweep says so" {
    var overrunning = Stubborn{ .session_id = "slow", .settle_after = std.math.maxInt(usize), .state_ns = 60 * std.time.ns_per_ms };
    var settles = memory.Adapter.init(testing.allocator);
    defer settles.deinit();
    const window = 50 * std.time.ns_per_ms;
    var hub = Hub.init(testing.allocator, testClock, .{ .shutdown_ns = window });
    defer hub.deinit();
    try hub.register("slow", overrunning.adapter());
    try hub.register("memory", settles.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    _ = try hub.open(arena, "slow", .{ .session_id = "slow" });
    _ = try hub.open(arena, "memory", .{ .session_id = "settles" });

    const summary = hub.closeSessions();
    try testing.expectEqual(@as(usize, 2), summary.sessions);
    try testing.expectEqual(@as(usize, 1), summary.refused);
    try testing.expectEqual(@as(usize, 0), summary.closed);
    try testing.expectEqual(@as(usize, 1), summary.unattempted);
    try testing.expectEqual(@as(usize, 2), summary.closed + summary.refused + summary.unattempted);
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());
}

test "a session whose stream fails is ended, its subscribers told, and its child kept" {
    var flaky = Flaky{ .allocator = testing.allocator };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "flaky", .{ .session_id = "doomed" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.stream_failed, subscription.ending);
    try testing.expectEqual(@as(usize, 0), flaky.closes);
    try testing.expectEqual(@as(usize, 1), hub.sessionCount());
    const still = try hub.state(arena, "doomed");
    try testing.expectEqualStrings("doomed", still.session_id);
    try hub.close(arena, "doomed");
    try testing.expectEqual(@as(usize, 1), flaky.closes);
}

test "a session that reports a handle is waited on, and handed no wait of its own" {
    if (!@hasDecl(std.Io.net, "has_unix_sockets") or !std.Io.net.has_unix_sockets) return error.SkipZigTest;
    var flaky = Flaky{ .allocator = testing.allocator, .fail_drain = false };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();
    const ends = try compat.stdio.pipe();
    defer compat.stdio.close(ends[0]);
    defer compat.stdio.close(ends[1]);
    flaky.read_end = ends[0].handle;
    flaky.write_end = ends[1].handle;

    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "flaky", .{ .session_id = "readable" });
    try testing.expectEqual(@as(?std.Io.File.Handle, ends[0].handle), Hub.handleOf(&hub.entries.items[0]));
    try testing.expectEqualStrings("readable", opened.session_id);

    try hub.pump(testing.allocator, 250 * std.time.ns_per_ms);
    try testing.expectEqual(@as(usize, 1), flaky.wait_len);
    try testing.expectEqual(@as(u64, 0), flaky.waits[0]);
}

test "idle sessions add no per-session delay, however many there are" {
    if (!std.Io.net.has_unix_sockets) return error.SkipZigTest;
    var idle: [6]Flaky = undefined;
    const ends = try compat.stdio.pipe();
    defer compat.stdio.close(ends[0]);
    defer compat.stdio.close(ends[1]);
    for (0..idle.len) |index| {
        idle[index] = .{ .allocator = testing.allocator, .fail_drain = false, .read_end = ends[0].handle, .write_end = ends[1].handle };
        idle[index].keep = std.heap.ArenaAllocator.init(testing.allocator);
    }
    defer for (0..idle.len) |index| idle[index].keep.deinit();

    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    for (0..idle.len) |index| {
        const one = &idle[index];
        var name_buf: [8]u8 = undefined;
        const name = try std.fmt.bufPrint(&name_buf, "idle-{d}", .{index});
        try hub.register(name, one.adapter());
        _ = try hub.open(arena, name, .{ .session_id = name });
    }
    try testing.expectEqual(@as(usize, 6), hub.sessionCount());

    try hub.pump(testing.allocator, 40 * std.time.ns_per_ms);
    for (0..idle.len) |index| {
        const one = &idle[index];
        try testing.expectEqual(@as(usize, 1), one.wait_len);
        try testing.expectEqual(@as(u64, 0), one.waits[0]);
    }
}

test "one silent child does not hold back another session's events" {
    if (!std.Io.net.has_unix_sockets) return error.SkipZigTest;
    const quiet_ends = try compat.stdio.pipe();
    defer compat.stdio.close(quiet_ends[0]);
    defer compat.stdio.close(quiet_ends[1]);
    var quiet = Flaky{ .allocator = testing.allocator, .fail_drain = false, .read_end = quiet_ends[0].handle, .write_end = quiet_ends[1].handle };
    quiet.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer quiet.keep.deinit();

    const busy_ends = try compat.stdio.pipe();
    defer compat.stdio.close(busy_ends[0]);
    defer compat.stdio.close(busy_ends[1]);
    var busy = Flaky{ .allocator = testing.allocator, .fail_drain = false, .read_end = busy_ends[0].handle, .write_end = busy_ends[1].handle };
    busy.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer busy.keep.deinit();

    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("quiet", quiet.adapter());
    try hub.register("busy", busy.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    _ = try hub.open(arena, "quiet", .{ .session_id = "waiting" });
    const opened = try hub.open(arena, "busy", .{ .session_id = "talking" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    busy.script[0] = .{ .run = "run-1", .sequence = 1, .line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"type\":\"content.delta\",\"id\":\"a1\"}" };
    busy.script_len = 1;
    try compat.stdio.writeAll(busy_ends[1], "x");

    try hub.pump(testing.allocator, 40 * std.time.ns_per_ms);
    const first = subscription.next().?;
    try testing.expectEqual(@as(u64, 1), first.sequence);
    for ([_]*Flaky{ &quiet, &busy }) |one| {
        try testing.expect(one.wait_len >= 1);
        for (one.waits[0..one.wait_len]) |wait| try testing.expectEqual(@as(u64, 0), wait);
    }
}

test "a handle-less session keeps the timed pump beside one that reports a handle" {
    if (!std.Io.net.has_unix_sockets) return error.SkipZigTest;
    var waiting = Flaky{ .allocator = testing.allocator, .fail_drain = false };
    waiting.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer waiting.keep.deinit();
    const ends = try compat.stdio.pipe();
    defer compat.stdio.close(ends[0]);
    defer compat.stdio.close(ends[1]);
    waiting.read_end = ends[0].handle;
    waiting.write_end = ends[1].handle;

    var silent = Flaky{ .allocator = testing.allocator, .fail_drain = false, .reported = false };
    silent.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer silent.keep.deinit();

    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("waiting", waiting.adapter());
    try hub.register("silent", silent.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    _ = try hub.open(arena, "waiting", .{ .session_id = "polled" });
    _ = try hub.open(arena, "silent", .{ .session_id = "untimed" });

    try hub.pump(testing.allocator, 40 * std.time.ns_per_ms);
    try testing.expectEqual(@as(usize, 1), waiting.wait_len);
    try testing.expectEqual(@as(u64, 0), waiting.waits[0]);
    try testing.expectEqual(@as(usize, 1), silent.wait_len);
    try testing.expect(silent.waits[0] > 0);
}

test "a session that reports no handle keeps the timed pump" {
    var flaky = Flaky{ .allocator = testing.allocator, .fail_drain = false, .reported = false };
    flaky.keep = std.heap.ArenaAllocator.init(testing.allocator);
    defer flaky.keep.deinit();

    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 8, .journal_capacity = 64 });
    defer hub.deinit();
    try hub.register("flaky", flaky.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    _ = try hub.open(arena, "flaky", .{ .session_id = "timed" });
    try testing.expectEqual(@as(?std.Io.File.Handle, null), Hub.handleOf(&hub.entries.items[0]));

    try hub.pump(testing.allocator, 20 * std.time.ns_per_ms);
    try testing.expectEqual(@as(usize, 1), flaky.wait_len);
    try testing.expect(flaky.waits[0] > 0);
}

test "the subscriber count has its own bound, not the mailbox depth" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .max_subscriptions = 3 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "many" });
    _ = try hub.subscribe(arena, opened.session_id, .{});
    _ = try hub.subscribe(arena, opened.session_id, .{});
    _ = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectError(error.SubscriptionFull, hub.subscribe(arena, opened.session_id, .{}));
}

test "a subscription that has read its terminal stops occupying a ceiling slot" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .max_subscriptions = 1, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "ceiling" });
    const first = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "ceiling");
    const started = try hub.submit(arena, "ceiling", &request, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "ceiling", started.run_id.?);
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);
    try testing.expectError(error.SubscriptionFull, hub.subscribe(arena, opened.session_id, .{}));

    while (first.next()) |_| {}
    try testing.expectEqual(Ending.run_terminal, first.ending);
    try testing.expectEqual(@as(usize, 0), hub.entries.items[0].subscribers.items.len);

    _ = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);
}

test "an ended subscription keeps its handle until close, and close reclaims it" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 256, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "handle" });
    const ended = try hub.subscribe(arena, opened.session_id, .{});
    const request = try submitFor(arena, "handle");
    const started = try hub.submit(arena, "handle", &request, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "handle", started.run_id.?);
    try hub.pump(testing.allocator, 0);
    while (ended.next()) |_| {}
    try testing.expectEqual(Ending.run_terminal, ended.ending);
    try testing.expectEqual(@as(usize, 0), hub.entries.items[0].subscribers.items.len);
    try testing.expectEqual(@as(usize, 1), hub.subscriptions.items.len);

    ended.close();
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(@as(usize, 0), hub.subscriptions.items.len);
}

test "a replay that already lost events never takes a ceiling slot" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2, .max_subscriptions = 1, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "replayed-slot" });
    const request = try submitFor(arena, "replayed-slot");
    const started = try hub.submit(arena, "replayed-slot", &request, "");
    try hub.pump(testing.allocator, 0);
    _ = try hub.cancel(arena, "replayed-slot", started.run_id.?);
    try hub.pump(testing.allocator, 0);

    const replayed = try hub.subscribe(arena, opened.session_id, .{ .run_id = started.run_id.?, .after = 0 });
    try testing.expectEqual(Ending.overflow, replayed.ending);
    try testing.expectEqual(@as(usize, 0), hub.entries.items[0].subscribers.items.len);
    _ = try hub.subscribe(arena, opened.session_id, .{});
    try testing.expectEqual(@as(usize, 1), hub.entries.items[0].subscribers.items.len);
}

test "a hub with no subscriber bound takes any number of them" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 2 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();
    const opened = try hub.open(arena, "memory", .{ .session_id = "unbounded" });
    for (0..100) |_| _ = try hub.subscribe(arena, opened.session_id, .{});
}

test "an overflow cursor survives the mailbox being drained across a run boundary" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .stream_queue = 4, .journal_capacity = 256 });
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const arena = scratch.allocator();

    const opened = try hub.open(arena, "memory", .{ .session_id = "spanning" });
    const subscription = try hub.subscribe(arena, opened.session_id, .{});
    const first = try submitFor(arena, "spanning");
    _ = try hub.submit(arena, "spanning", &first, "");
    try hub.pump(testing.allocator, 0);
    const read = subscription.next().?;
    const expected_run = try testing.allocator.dupe(u8, read.run_id);
    defer testing.allocator.free(expected_run);
    try testing.expectEqualStrings("run-1", expected_run);

    _ = try hub.cancel(arena, "spanning", "run-1");
    const second = try submitFor(arena, "spanning");
    const admitted = try hub.submit(arena, "spanning", &second, "");
    try hub.pump(testing.allocator, 0);
    try testing.expectEqual(Ending.overflow, subscription.ending);
    while (subscription.next()) |_| {}
    try testing.expectEqualStrings("run-1", expected_run);
    try testing.expectEqualStrings(admitted.run_id.?, subscription.overflow_run);
    try testing.expectEqual(@as(u64, 0), subscription.overflow_sequence);
}

const fading_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "fading", .name = "Fading", .version = "0.1", .adapter = "script" },
    .capability_revision = "fading-v1",
    .features = &.{},
};

var fading_registered: bool = false;

fn fadingAdapter(state: *bool) contract.Adapter {
    return .{ .ptr = @ptrCast(@constCast(state)), .vtable = &.{ .probe = fadingProbe, .open = fadingOpen } };
}

fn fadingProbe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
    const state: *bool = @ptrCast(@alignCast(ptr));
    _ = refusal;
    defer state.* = true;
    if (state.*) return .{ .endpoint = fading_descriptor.endpoint, .capability_revision = "", .features = &.{} };
    return fading_descriptor;
}

fn fadingOpen(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
    _ = ptr;
    _ = arena;
    _ = request;
    _ = refusal;
    return error.Unavailable;
}

const bare_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "bare", .name = "Bare", .version = "0.1", .adapter = "script" },
    .capability_revision = "",
    .features = &.{},
};

fn bareAdapter() contract.Adapter {
    return .{ .ptr = @ptrCast(@constCast(&bare_descriptor)), .vtable = &.{ .probe = bareProbe, .open = bareOpen } };
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

test "a closed session reopens through the hub once, and a reopen of a live or unknown session is refused" {
    var adapter = memory.Adapter.init(testing.allocator);
    defer adapter.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", adapter.adapter());
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    _ = try hub.open(arena, "memory", .{ .session_id = "kept" });
    try hub.close(arena, "kept");
    const reopened = try hub.open(arena, "memory", .{ .session_id = "kept", .reopen = true });
    try testing.expect(reopened.state.recovered);
    try testing.expectError(error.SessionExists, hub.open(arena, "memory", .{ .session_id = "kept", .reopen = true }));
    try testing.expectError(error.UnknownSession, hub.open(arena, "memory", .{ .session_id = "ghost", .reopen = true }));
}

const NativeMemory = struct {
    inner: *memory.Adapter,
    handed: []const u8 = "",
    session_vtable: contract.Session.VTable = undefined,

    fn adapter(self: *NativeMemory) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        const self: *NativeMemory = @ptrCast(@alignCast(ptr));
        return self.inner.adapter().probe(refusal);
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        const self: *NativeMemory = @ptrCast(@alignCast(ptr));
        self.handed = try arena.dupe(u8, request.native_session_id);
        const session = try self.inner.adapter().open(arena, request, refusal);
        self.session_vtable = session.vtable.*;
        self.session_vtable.native_id = nativeThread;
        return .{ .ptr = session.ptr, .vtable = &self.session_vtable };
    }

    fn nativeThread(ptr: *anyopaque) []const u8 {
        _ = ptr;
        return "native-thread";
    }
};

test "a reopen hands the adapter the native id its open recorded, and only under the adapter that opened it" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var native = NativeMemory{ .inner = &inner };
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("native", native.adapter());
    try hub.register("memory", inner.adapter());
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    _ = try hub.open(arena, "native", .{ .session_id = "kept" });
    try testing.expectEqualStrings("", native.handed);
    try hub.close(arena, "kept");
    try testing.expectError(error.UnknownSession, hub.open(arena, "memory", .{ .session_id = "kept", .reopen = true }));
    _ = try hub.open(arena, "native", .{ .session_id = "kept", .reopen = true });
    try testing.expectEqualStrings("native-thread", native.handed);
}

test "a binding recorded before a hub restart is read after it, and a torn store is not read" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var native = NativeMemory{ .inner = &inner };
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(testing.io, testing.allocator);
    defer testing.allocator.free(cwd);
    const path = try std.fs.path.join(testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "bindings.jsonl" });
    defer testing.allocator.free(path);
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    {
        var store = try binding.Store.open(testing.allocator, path);
        defer store.deinit();
        var hub = Hub.init(testing.allocator, testClock, .{ .bindings = &store });
        defer hub.deinit();
        try hub.register("native", native.adapter());
        _ = try hub.open(arena, "native", .{ .session_id = "kept" });
        try hub.close(arena, "kept");
    }
    var store = try binding.Store.open(testing.allocator, path);
    defer store.deinit();
    var restarted = Hub.init(testing.allocator, testClock, .{ .bindings = &store });
    defer restarted.deinit();
    try restarted.register("native", native.adapter());
    try restarted.register("memory", inner.adapter());
    try testing.expectError(error.UnknownSession, restarted.open(arena, "memory", .{ .session_id = "kept", .reopen = true }));
    native.handed = "";
    const reopened = try restarted.open(arena, "native", .{ .session_id = "kept", .reopen = true });
    try testing.expectEqualStrings("native-thread", native.handed);
    try testing.expect(reopened.state.recovered);
    try testing.expectEqual(binding.Action.reopened, (try store.latest(arena, "kept")).?.action);
    try restarted.close(arena, "kept");

    const written = try compat.fs.readFileAlloc(arena, compat.fs.getCwd(), path, binding.max_store_bytes);
    try compat.fs.writeFile(compat.fs.getCwd(), path, try std.mem.concat(arena, u8, &.{ written, "00000000 {}\n" }));
    var torn = Hub.init(testing.allocator, testClock, .{ .bindings = &store });
    defer torn.deinit();
    try torn.register("native", native.adapter());
    var refused: OpenRefusal = .{};
    try testing.expectError(error.BackendFailed, torn.openReporting(arena, "native", .{ .session_id = "kept", .reopen = true }, &refused));
    try testing.expect(std.mem.indexOf(u8, refused.reason.message, "not written whole") != null);
}

test "an open records the compaction policy it asked for" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cwd = try std.process.currentPathAlloc(testing.io, testing.allocator);
    defer testing.allocator.free(cwd);
    const path = try std.fs.path.join(testing.allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "bindings.jsonl" });
    defer testing.allocator.free(path);
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var store = try binding.Store.open(testing.allocator, path);
    defer store.deinit();
    var hub = Hub.init(testing.allocator, testClock, .{ .bindings = &store });
    defer hub.deinit();
    try hub.register("memory", inner.adapter());
    _ = try hub.open(arena, "memory", .{ .session_id = "set", .compaction_policy_json = "{\"kind\":\"share\",\"share_percent\":70}" });
    const entry = (try store.latest(arena, "set")).?;
    try testing.expectEqualStrings("", entry.record.reasoning_level);
    try testing.expectEqualStrings("share", entry.record.compaction_policy.?.kind);
    try testing.expectEqual(@as(?i64, 70), entry.record.compaction_policy.?.share_percent);
    try hub.close(arena, "set");
}

test "a binding the store failed to write is reported at reopen rather than read as an unknown session" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var native = NativeMemory{ .inner = &inner };
    var store = binding.Store{ .allocator = testing.allocator, .path = @constCast("/nonexistent-oap-binding-dir/bindings.jsonl"), .staging = @constCast("/nonexistent-oap-binding-dir/bindings.jsonl.writing") };
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    {
        var hub = Hub.init(testing.allocator, testClock, .{ .bindings = &store });
        defer hub.deinit();
        try hub.register("native", native.adapter());
        _ = try hub.open(arena, "native", .{ .session_id = "kept" });
        try hub.close(arena, "kept");
    }
    try testing.expect(store.last_failure != null);
    var restarted = Hub.init(testing.allocator, testClock, .{ .bindings = &store });
    defer restarted.deinit();
    try restarted.register("native", native.adapter());
    var refused: OpenRefusal = .{};
    try testing.expectError(error.BackendFailed, restarted.openReporting(arena, "native", .{ .session_id = "kept", .reopen = true }, &refused));
    try testing.expect(std.mem.indexOf(u8, refused.reason.message, "last failed to record") != null);
}

test "a reopen the hub holds a record for but the adapter has lost is unsupported_feature, not unknown_session" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var native = NativeMemory{ .inner = &inner };
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("native", native.adapter());
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();

    _ = try hub.open(arena, "native", .{ .session_id = "kept" });
    try hub.close(arena, "kept");
    var direct = contract.Refusal{};
    const taken = try inner.adapter().open(arena, .{ .session_id = "kept", .participant = "user", .reopen = true }, &direct);
    taken.teardown();

    var refused = OpenRefusal{};
    try testing.expectError(error.UnsupportedFeature, hub.openReporting(arena, "native", .{ .session_id = "kept", .reopen = true }, &refused));
    try testing.expectEqualStrings(contract.feature_open_reopen, refused.reason.feature);
}

test "a settings update reaches the session's adapter, and one naming nothing or another session is refused before it" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("memory", inner.adapter());
    _ = try hub.open(arena, "memory", .{ .session_id = "set" });
    var refusal = contract.Refusal{};
    try testing.expectError(error.InvalidSubmission, hub.updateSettings(arena, "set", &.{ .session_id = "set" }, &refusal));
    try testing.expectError(error.ScopeMismatch, hub.updateSettings(arena, "set", &.{ .session_id = "other", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal));
    try testing.expectError(error.UnknownSession, hub.updateSettings(arena, "absent", &.{ .session_id = "absent", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal));
    const updated = try hub.updateSettings(arena, "set", &.{ .session_id = "set", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal);
    try testing.expect(std.mem.indexOf(u8, updated.compaction_policy_json.?, "off") != null);
    try hub.close(arena, "set");
}

const ClosingSettings = struct {
    inner: *memory.Adapter,
    session_vtable: contract.Session.VTable = undefined,

    fn adapter(self: *ClosingSettings) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        const self: *ClosingSettings = @ptrCast(@alignCast(ptr));
        return self.inner.adapter().probe(refusal);
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        const self: *ClosingSettings = @ptrCast(@alignCast(ptr));
        const session = try self.inner.adapter().open(arena, request, refusal);
        self.session_vtable = session.vtable.*;
        self.session_vtable.update_settings = closed;
        return .{ .ptr = session.ptr, .vtable = &self.session_vtable };
    }

    fn closed(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionSettingsUpdateRequest, refusal: *contract.Refusal) contract.Failure!contract.Updated {
        _ = ptr;
        _ = arena;
        _ = request;
        _ = refusal;
        return error.SessionClosed;
    }
};

test "a settings update the adapter finds closed answers session_closed and releases the session" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var closing = ClosingSettings{ .inner = &inner };
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("closing", closing.adapter());
    _ = try hub.open(arena, "closing", .{ .session_id = "gone" });
    var refusal = contract.Refusal{};
    try testing.expectError(error.SessionClosed, hub.updateSettings(arena, "gone", &.{ .session_id = "gone", .compaction_policy_json = "{\"kind\":\"off\"}" }, &refusal));
    try testing.expect(!hub.knows("gone"));
}

const ClosingControls = struct {
    inner: *memory.Adapter,
    session_vtable: contract.Session.VTable = undefined,

    fn adapter(self: *ClosingControls) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        const self: *ClosingControls = @ptrCast(@alignCast(ptr));
        return self.inner.adapter().probe(refusal);
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        const self: *ClosingControls = @ptrCast(@alignCast(ptr));
        const session = try self.inner.adapter().open(arena, request, refusal);
        self.session_vtable = session.vtable.*;
        self.session_vtable.submit = submitClosed;
        self.session_vtable.resolve = resolveClosed;
        self.session_vtable.cancel = cancelClosed;
        return .{ .ptr = session.ptr, .vtable = &self.session_vtable };
    }

    fn submitClosed(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, envelope_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        _ = .{ ptr, arena, request, envelope_id, refusal };
        return error.SessionClosed;
    }

    fn resolveClosed(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = .{ ptr, arena, resolution, refusal };
        return error.SessionClosed;
    }

    fn cancelClosed(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        _ = .{ ptr, arena, run_id, refusal };
        return error.SessionClosed;
    }
};

test "a control the adapter finds closed answers session_closed and releases the session, as Go does" {
    var inner = memory.Adapter.init(testing.allocator);
    defer inner.deinit();
    var closing = ClosingControls{ .inner = &inner };
    var arena_state = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var hub = Hub.init(testing.allocator, testClock, .{});
    defer hub.deinit();
    try hub.register("closing", closing.adapter());
    var refusal = contract.Refusal{};

    _ = try hub.open(arena, "closing", .{ .session_id = "submitted" });
    const message = try arena.dupe(oap_types.Message, &.{.{ .role = .user, .content = .{ .text = "hi" } }});
    try testing.expectError(error.SessionClosed, hub.submitReporting(arena, "submitted", &.{ .session_id = "submitted", .messages = message, .delivery = .auto }, "e1", &refusal));
    try testing.expect(!hub.knows("submitted"));

    _ = try hub.open(arena, "closing", .{ .session_id = "resolved" });
    try testing.expectError(error.SessionClosed, hub.resolve(arena, "resolved", .{ .permission = &.{ .session_id = "resolved", .run_id = "run-1", .interaction_id = "i1", .requested_by = "agent", .responded_by = "user", .granted = true } }));
    try testing.expect(!hub.knows("resolved"));

    _ = try hub.open(arena, "closing", .{ .session_id = "cancelled" });
    try testing.expectError(error.SessionClosed, hub.cancel(arena, "cancelled", "run-1"));
    try testing.expect(!hub.knows("cancelled"));
}

test {
    _ = binding;
}

test "a reply longer than the bound is cut on a character boundary" {
    const long = "a" ** (last_reply_limit - 1) ++ "\u{00e9}" ++ "b";
    const cut = Hub.replyCut(long);
    try std.testing.expectEqual(last_reply_limit - 1, cut);
    try std.testing.expect(std.unicode.utf8ValidateSlice(long[0..cut]));
    const short = "short";
    try std.testing.expectEqual(short.len, Hub.replyCut(short));
}
