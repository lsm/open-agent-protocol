const std = @import("std");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");
const json_writer = @import("json_writer");
const contract = @import("contract");

pub const default_frame_limit: usize = 1 << 20;
pub const default_participant = "user";
pub const default_settle_window_ns: u64 = 10 * std.time.ns_per_s;

pub const Error = error{
    MalformedLine,
    UnaddressableEnvelope,
    FrameTooLarge,
};

const base_members = [_][]const u8{ "protocol", "version", "profile", "type", "id", "payload" };
const scope_members = [_][]const u8{ "in_reply_to", "session_id", "run_id", "turn_id", "tool_call_id", "capability_revision" };

const Denial = struct {
    code: []const u8,
    message: []const u8,
    details: []const oap_types.DetailEntry = &.{},
};

const Cursor = struct {
    run_id: []u8,
    delivered: u64 = 0,
    latest: u64 = 0,
    lost: bool = false,
};

const Journaled = struct {
    line: []u8,
    cursor: usize,
    sequence: u64,
};

const Entry = struct {
    session: contract.Session,
    cursors: std.ArrayList(Cursor) = .empty,
    journal: std.ArrayList(Journaled) = .empty,
    state_sequence: u64 = 0,

    fn deinit(self: *Entry, allocator: std.mem.Allocator) void {
        for (self.cursors.items) |cursor| allocator.free(cursor.run_id);
        self.cursors.deinit(allocator);
        for (self.journal.items) |kept| allocator.free(kept.line);
        self.journal.deinit(allocator);
        self.session.close();
    }
};

pub const default_journal_capacity = 256;

pub const Options = struct {
    frame_limit: usize = default_frame_limit,
    journal_capacity: usize = default_journal_capacity,
};

const Served = (contract.Failure || error{ Denied, FrameTooLarge });

pub const Endpoint = struct {
    allocator: std.mem.Allocator,
    adapter: contract.Adapter,
    frame_limit: usize,
    journal_capacity: usize,
    participant: ?[]u8 = null,
    ids: u64 = 0,
    entries: std.ArrayList(Entry) = .empty,
    outbound: std.ArrayList([]u8) = .empty,
    denial: Denial = .{ .code = "internal", .message = "" },

    pub fn init(allocator: std.mem.Allocator, adapter: contract.Adapter, options: Options) Endpoint {
        return .{ .allocator = allocator, .adapter = adapter, .frame_limit = options.frame_limit, .journal_capacity = options.journal_capacity };
    }

    pub fn deinit(self: *Endpoint) void {
        self.closeSessions();
        self.entries.deinit(self.allocator);
        for (self.outbound.items) |line| self.allocator.free(line);
        self.outbound.deinit(self.allocator);
        if (self.participant) |owned| self.allocator.free(owned);
        self.* = undefined;
    }

    pub fn popOutbound(self: *Endpoint) ?[]u8 {
        if (self.outbound.items.len == 0) return null;
        return self.outbound.orderedRemove(0);
    }

    pub fn sessionCount(self: *const Endpoint) usize {
        return self.entries.items.len;
    }

    pub fn handleLine(self: *Endpoint, line: []const u8) !void {
        if (line.len > self.frame_limit) return Error.FrameTooLarge;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();

        const parsed = std.json.parseFromSliceLeaky(std.json.Value, arena, line, .{}) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            return Error.MalformedLine;
        };
        if (parsed != .object) return Error.MalformedLine;
        const root = parsed.object;

        const declared = try declaredString(root, "protocol");
        if (declared == null or declared.?.len == 0) {
            const control = try declaredString(root, "control") orelse return Error.MalformedLine;
            if (control.len == 0) return Error.MalformedLine;
            try self.drainAll();
            try self.answerControl(arena, root, control);
            return self.drainAll();
        }
        const id = (declaredString(root, "id") catch return Error.UnaddressableEnvelope) orelse return Error.UnaddressableEnvelope;
        if (id.len == 0) return Error.UnaddressableEnvelope;

        try self.drainAll();
        self.serve(arena, root, line) catch |err| switch (err) {
            error.Denied => try self.writeError(root, id, self.denial),
            else => |failure| return failure,
        };
        try self.drainAll();
    }

    pub fn pump(self: *Endpoint, wait_ns: u64) !bool {
        var progressed = false;
        const count = self.entries.items.len;
        if (count == 0) return false;
        const share = @max(wait_ns / count, std.time.ns_per_ms);
        for (self.entries.items) |*entry| {
            if (try entry.session.pump(share)) progressed = true;
        }
        try self.drainAll();
        return progressed;
    }

    pub fn finish(self: *Endpoint, window_ns: u64, clock: *const fn () u64) !void {
        const started = clock();
        while (self.running() and clock() -| started < window_ns) {
            _ = try self.pump(10 * std.time.ns_per_ms);
        }
        try self.drainAll();
        self.closeSessions();
    }

    fn running(self: *Endpoint) bool {
        for (self.entries.items) |*entry| {
            if (entry.session.activity() == .running) return true;
        }
        return false;
    }

    fn closeSessions(self: *Endpoint) void {
        for (self.entries.items) |*entry| entry.deinit(self.allocator);
        self.entries.clearRetainingCapacity();
    }

    fn nextId(self: *Endpoint, arena: std.mem.Allocator, kind: []const u8) ![]const u8 {
        self.ids += 1;
        return std.fmt.allocPrint(arena, "oapx-{s}-{d}", .{ kind, self.ids });
    }

    fn controlParticipant(self: *const Endpoint) []const u8 {
        return self.participant orelse default_participant;
    }

    fn find(self: *Endpoint, session_id: []const u8) ?*Entry {
        for (self.entries.items) |*entry| {
            if (std.mem.eql(u8, entry.session.id(), session_id)) return entry;
        }
        return null;
    }

    fn deny(self: *Endpoint, code: []const u8, message: []const u8, details: []const oap_types.DetailEntry) error{Denied} {
        self.denial = .{ .code = code, .message = message, .details = details };
        return error.Denied;
    }

    fn serve(self: *Endpoint, arena: std.mem.Allocator, root: std.json.ObjectMap, line: []const u8) Served!void {
        var refusal = contract.Refusal{};
        self.dispatch(arena, root, line, &refusal) catch |err| switch (err) {
            error.Denied, error.OutOfMemory, error.FrameTooLarge => |passed| return passed,
            else => |failure| return self.refuse(arena, failure, refusal),
        };
    }

    fn dispatch(self: *Endpoint, arena: std.mem.Allocator, root: std.json.ObjectMap, line: []const u8, refusal: *contract.Refusal) Served!void {
        var missing = std.ArrayList(u8).empty;
        for (base_members) |member| {
            const value = root.get(member);
            if (value != null and value.? != .null) continue;
            if (missing.items.len > 0) try missing.appendSlice(arena, ", ");
            try missing.appendSlice(arena, member);
        }
        if (missing.items.len > 0) {
            const message = try std.fmt.allocPrint(arena, "the envelope omits required member(s): {s}", .{missing.items});
            return self.deny("invalid_request", message, &.{});
        }
        for (base_members[0..4]) |member| {
            if (root.get(member).? != .string) {
                const message = try std.fmt.allocPrint(arena, "the envelope member {s} is not a string", .{member});
                return self.deny("invalid_request", message, &.{});
            }
        }
        for (scope_members) |member| {
            const value = root.get(member) orelse continue;
            if (value != .string) {
                const message = try std.fmt.allocPrint(arena, "the envelope member {s} is not a string", .{member});
                return self.deny("invalid_request", message, &.{});
            }
        }

        const descriptor = try self.adapter.probe(refusal);
        const declared_type = root.get("type").?.string;
        if (root.get("capability_revision")) |revision| {
            const exempt = std.mem.eql(u8, declared_type, "protocol.initialize.request") or
                std.mem.eql(u8, declared_type, "capabilities.request");
            if (!exempt and revision.string.len > 0 and !std.mem.eql(u8, revision.string, descriptor.capability_revision)) {
                const message = try std.fmt.allocPrint(arena, "capability revision \"{s}\" is not the current \"{s}\"", .{ revision.string, descriptor.capability_revision });
                const details = try arena.dupe(oap_types.DetailEntry, &.{
                    .{ .key = "expected_revision", .value = revision.string },
                    .{ .key = "current_revision", .value = descriptor.capability_revision },
                });
                return self.deny("stale_capabilities", message, details);
            }
        }

        const request = oap_envelope.deserializeEnvelope(line, arena) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            oap_envelope.DecodeError.UnknownEnvelopeType => {
                const message = try std.fmt.allocPrint(arena, "this endpoint serves no {s}", .{declared_type});
                return self.deny("unsupported_request", message, &.{});
            },
            oap_envelope.DecodeError.ProtocolMismatch => return self.deny("invalid_request", "envelope protocol is not open-agent-protocol", &.{}),
            oap_envelope.DecodeError.VersionMismatch => return self.deny("invalid_request", "envelope version is not 0.1", &.{}),
            oap_envelope.DecodeError.ProfileMismatch => return self.deny("invalid_request", "envelope profile is not the agent-control core", &.{}),
            else => return self.deny("invalid_payload", "the payload does not decode as this request type", &.{}),
        };

        switch (request.payload) {
            .initialize_request => |*payload| try self.initialize(arena, &request, payload, descriptor),
            .capabilities_request => try self.capabilities(arena, &request, descriptor),
            .session_open_request => |*payload| try self.open(arena, &request, payload, descriptor, refusal),
            .session_state_request => try self.state(arena, &request, refusal),
            .session_model_switch_request => |*payload| try self.switchModel(arena, &request, payload, refusal),
            .message_submit_request => |*payload| try self.submit(arena, &request, payload, descriptor, refusal),
            .run_cancel_request => |*payload| try self.cancel(arena, &request, payload, refusal),
            .user_input_resolve_request => |*payload| try self.resolveInput(arena, &request, payload, refusal),
            .permission_resolve_request => |*payload| try self.resolvePermission(arena, &request, payload, refusal),
            .call_resolve_request => |*payload| try self.resolveCall(arena, &request, payload, refusal),
            .models_request => |*payload| try self.models(arena, &request, payload, descriptor, refusal),
            .tools_list_request => |*payload| try self.tools(arena, &request, payload, descriptor, refusal),
            else => {
                const message = try std.fmt.allocPrint(arena, "this endpoint serves no {s}", .{declared_type});
                return self.deny("unsupported_request", message, &.{});
            },
        }
    }

    fn respond(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, answer: oap_types.Envelope) Served!void {
        var envelope = answer;
        envelope.id = try self.nextId(arena, "response");
        envelope.in_reply_to = request.id;
        const line = try oap_envelope.serializeEnvelope(envelope, self.allocator);
        errdefer self.allocator.free(line);
        if (line.len + 1 > self.frame_limit) return error.FrameTooLarge;
        try self.outbound.append(self.allocator, line);
    }

    fn entryFor(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope) Served!*Entry {
        const addressed = request.session_id orelse return self.deny("invalid_request", "this request must name its session", &.{});
        return self.find(addressed) orelse self.deny("unknown_session", try std.fmt.allocPrint(arena, "no session \"{s}\"", .{addressed}), &.{});
    }

    fn requireScope(self: *Endpoint, arena: std.mem.Allocator, payload_session: []const u8, entry: *Entry) Served!void {
        if (std.mem.eql(u8, payload_session, entry.session.id())) return;
        const message = try std.fmt.allocPrint(arena, "payload session_id \"{s}\" does not match the addressed session \"{s}\"", .{ payload_session, entry.session.id() });
        return self.deny("scope_mismatch", message, &.{});
    }

    fn initialize(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.InitializeRequest, descriptor: contract.Descriptor) Served!void {
        if (!listed(payload.protocol_versions, oap_types.VERSION) or !listed(payload.profiles, oap_types.PROFILE)) {
            const details = try arena.dupe(oap_types.DetailEntry, &.{
                .{ .key = "feature", .value = "protocol.initialize" },
                .{ .key = "reason", .value = contract.reason_unsatisfiable },
            });
            return self.deny("unsupported_feature", "this endpoint serves open-agent-protocol 0.1 agent-control-core only", details);
        }
        if (payload.participant) |participant| {
            if (participant.id.len > 0) {
                const owned = try self.allocator.dupe(u8, participant.id);
                if (self.participant) |previous| self.allocator.free(previous);
                self.participant = owned;
            }
        }
        try self.respond(arena, request, .{
            .id = "",
            .capability_revision = descriptor.capability_revision,
            .payload = .{ .initialize_response = .{
                .protocol_version = oap_types.VERSION,
                .profile = oap_types.PROFILE,
                .endpoint = descriptor.endpoint,
            } },
        });
    }

    fn capabilities(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, descriptor: contract.Descriptor) Served!void {
        const features = try arena.alloc(oap_types.Feature, descriptor.features.len);
        for (descriptor.features, features) |declared, *feature| {
            feature.* = .{
                .key = declared.key,
                .level = declared.level,
                .scope = declared.scope,
                .reason = declared.reason,
                .modes = declared.modes,
                .constraints_json = declared.constraints_json,
                .limits_json = declared.limits_json,
            };
        }
        const bindings = try arena.dupe(oap_types.Binding, &.{.{ .kind = "stdio", .serialization = "jsonl" }});
        const sources = try arena.dupe(oap_types.ToolSourceDescriptor, descriptor.sources);
        const catalog = try arena.dupe(oap_types.ToolDefinition, descriptor.tools);
        try self.respond(arena, request, .{
            .id = "",
            .capability_revision = descriptor.capability_revision,
            .payload = .{ .capabilities_response = .{
                .endpoint = descriptor.endpoint,
                .protocol_versions = &.{oap_types.VERSION},
                .profiles = &.{oap_types.PROFILE},
                .bindings = bindings,
                .features = features,
                .tools = catalog,
                .sources = sources,
                .limits = descriptor.limits,
            } },
        });
    }

    fn open(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.SessionOpenRequest, descriptor: contract.Descriptor, refusal: *contract.Refusal) Served!void {
        try contract.refuseUnadvertisedOpen(descriptor, payload, refusal);
        if (payload.session_id) |requested| {
            if (self.find(requested) != null) {
                const message = try std.fmt.allocPrint(arena, "session \"{s}\" already exists", .{requested});
                return self.deny("session_exists", message, &.{});
            }
        }
        try self.entries.ensureUnusedCapacity(self.allocator, 1);
        const session = try self.adapter.open(arena, .{
            .session_id = payload.session_id orelse "",
            .participant = self.controlParticipant(),
            .allow_degraded_features = payload.allow_degraded_features,
            .tools_json = payload.tools_json,
            .tool_sources_json = payload.tool_sources_json,
        }, refusal);
        if (self.find(session.id()) != null) {
            session.close();
            return self.deny("session_exists", "the backend opened a session under an id already in use", &.{});
        }
        const state_now = session.state(arena, refusal) catch |failure| {
            session.close();
            return failure;
        };
        self.entries.appendAssumeCapacity(.{ .session = session });
        try self.respond(arena, request, .{
            .id = "",
            .session_id = state_now.session_id,
            .capability_revision = request.capability_revision,
            .payload = .{ .session_open_response = state_now },
        });
    }

    fn state(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        const current = try entry.session.state(arena, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .capability_revision = request.capability_revision,
            .payload = .{ .session_state_response = current },
        });
    }

    fn switchModel(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.SessionModelSwitchRequest, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        const switcher = entry.session.vtable.switch_model orelse
            return refusal.unsupported(contract.feature_model_switch, contract.reason_unadvertised);
        const switched = try switcher(entry.session.ptr, arena, payload, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .capability_revision = request.capability_revision,
            .payload = .{ .session_model_switch_response = switched.response },
        });
        const previous = switched.response.previous_model_id orelse "";
        if (std.mem.eql(u8, previous, switched.response.model_id)) return;
        entry.state_sequence += 1;
        const updated = oap_types.Envelope{
            .id = try self.nextId(arena, "event"),
            .sequence = entry.state_sequence,
            .session_id = entry.session.id(),
            .capability_revision = request.capability_revision,
            .payload = .{ .session_state_updated = switched.state },
        };
        const line = try oap_envelope.serializeEnvelope(updated, self.allocator);
        errdefer self.allocator.free(line);
        try self.outbound.append(self.allocator, line);
    }

    fn submit(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.MessageSubmitRequest, descriptor: contract.Descriptor, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        try contract.refuseUnadvertisedControls(descriptor, payload, refusal);
        const admission = try entry.session.submit(arena, payload, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = admission.session_id,
            .run_id = admission.run_id,
            .capability_revision = request.capability_revision,
            .payload = .{ .message_submit_response = admission },
        });
    }

    fn cancel(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.RunCancelRequest, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        const acknowledged = try entry.session.cancel(arena, payload.run_id, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .run_id = payload.run_id,
            .capability_revision = request.capability_revision,
            .payload = .{ .run_cancel_response = acknowledged },
        });
    }

    fn resolveInput(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.UserInputResolveRequest, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        try entry.session.resolve(arena, .{ .input = payload }, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .run_id = payload.run_id,
            .capability_revision = request.capability_revision,
            .payload = .{ .user_input_resolve_response = .{
                .interaction_id = payload.interaction_id,
                .session_id = payload.session_id,
                .run_id = payload.run_id,
                .accepted = true,
            } },
        });
    }

    fn resolvePermission(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.PermissionResolveRequest, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        try entry.session.resolve(arena, .{ .permission = payload }, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .run_id = payload.run_id,
            .capability_revision = request.capability_revision,
            .payload = .{ .permission_resolve_response = .{
                .interaction_id = payload.interaction_id,
                .session_id = payload.session_id,
                .run_id = payload.run_id,
                .accepted = true,
            } },
        });
    }

    fn resolveCall(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.CallResolveRequest, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        const resolver = entry.session.vtable.resolve_call orelse
            return refusal.unsupported(contract.feature_tools_provide, contract.reason_unadvertised);
        const result = try resolver(entry.session.ptr, arena, payload, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .run_id = payload.run_id,
            .capability_revision = request.capability_revision,
            .payload = .{ .call_resolve_response = result },
        });
    }

    fn models(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.ModelsRequest, descriptor: contract.Descriptor, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        try self.requireScope(arena, payload.session_id, entry);
        const lister = entry.session.vtable.models orelse
            return refusal.unsupported(contract.feature_models_list, contract.reason_unadvertised);
        const catalog = try lister(entry.session.ptr, arena, payload, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .capability_revision = descriptor.capability_revision,
            .payload = .{ .models_response = catalog },
        });
    }

    fn tools(self: *Endpoint, arena: std.mem.Allocator, request: *const oap_types.Envelope, payload: *const oap_types.ToolsListRequest, descriptor: contract.Descriptor, refusal: *contract.Refusal) Served!void {
        const entry = try self.entryFor(arena, request);
        if (payload.session_id) |scoped| try self.requireScope(arena, scoped, entry);
        const lister = entry.session.vtable.tools orelse
            return self.deny("tool_catalog_unavailable", "adapter: no portable tool catalog is served", &.{});
        const catalog = try lister(entry.session.ptr, arena, payload, refusal);
        try self.respond(arena, request, .{
            .id = "",
            .session_id = entry.session.id(),
            .capability_revision = descriptor.capability_revision,
            .payload = .{ .tools_list_response = catalog },
        });
    }

    fn refuse(self: *Endpoint, arena: std.mem.Allocator, failure: contract.Failure, refusal: contract.Refusal) Served!void {
        var details = std.ArrayList(oap_types.DetailEntry).empty;
        const mapped = codeFor(failure);
        const code = mapped.code;
        const fallback = mapped.fallback;
        if (refusal.feature.len > 0) try details.append(arena, .{ .key = "feature", .value = refusal.feature });
        if (refusal.reason.len > 0) try details.append(arena, .{ .key = "reason", .value = refusal.reason });
        if (refusal.tool.len > 0) try details.append(arena, .{ .key = "tool", .value = refusal.tool });
        if (refusal.field.len > 0) try details.append(arena, .{ .key = "field", .value = refusal.field });
        if (refusal.source.len > 0) try details.append(arena, .{ .key = "source", .value = refusal.source });
        if (refusal.model_id.len > 0) try details.append(arena, .{ .key = "model_id", .value = refusal.model_id });
        if (refusal.backend.len > 0) try details.append(arena, .{ .key = "backend", .value = refusal.backend });
        var message = if (refusal.message.len > 0) refusal.message else fallback;
        if (failure == error.UnsupportedFeature and refusal.message.len == 0 and refusal.feature.len > 0) {
            message = if (refusal.detail.len > 0)
                try std.fmt.allocPrint(arena, "adapter: unsupported input: {s} ({s}): {s}", .{ refusal.feature, refusal.reason, refusal.detail })
            else
                try std.fmt.allocPrint(arena, "adapter: unsupported input: {s} ({s})", .{ refusal.feature, refusal.reason });
        }
        if (failure == error.ModelNotFound and refusal.message.len == 0 and refusal.model_id.len > 0) {
            message = try std.fmt.allocPrint(arena, "adapter: model is not in the effective catalog: \"{s}\"", .{refusal.model_id});
        }
        if (failure == error.CapabilityDegraded and refusal.message.len == 0) {
            message = try std.fmt.allocPrint(arena, "adapter: unsupported input: {s} is degraded and was not opted into", .{refusal.feature});
        }
        return self.deny(code, message, details.items);
    }

    fn writeError(self: *Endpoint, root: std.json.ObjectMap, request_id: []const u8, denial: Denial) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        const envelope = oap_types.Envelope{
            .id = try self.nextId(scratch.allocator(), "error"),
            .in_reply_to = request_id,
            .session_id = scopeString(root, "session_id"),
            .run_id = scopeString(root, "run_id"),
            .payload = .{ .error_response = .{
                .code = denial.code,
                .message = denial.message,
                .details = denial.details,
            } },
        };
        const line = try oap_envelope.serializeEnvelope(envelope, self.allocator);
        errdefer self.allocator.free(line);
        if (line.len + 1 > self.frame_limit) return Error.FrameTooLarge;
        try self.outbound.append(self.allocator, line);
    }

    fn answerControl(self: *Endpoint, arena: std.mem.Allocator, root: std.json.ObjectMap, control: []const u8) !void {
        const id = try declaredString(root, "id");
        const session_id = try declaredString(root, "session_id");
        const run_id = try declaredString(root, "run_id");
        var after: u64 = 0;
        if (root.get("after")) |declared| {
            if (declared != .integer or declared.integer < 0) return Error.MalformedLine;
            after = @intCast(declared.integer);
        }
        if (!std.mem.eql(u8, control, "replay")) {
            const message = try std.fmt.allocPrint(arena, "this endpoint serves no \"{s}\" control", .{control});
            return self.writeControl(.{ .control = "replay.error", .id = id, .code = "unsupported_control", .message = message });
        }
        const addressed = session_id orelse "";
        if (addressed.len == 0) {
            return self.writeControl(.{ .control = "replay.error", .id = id, .code = "invalid_request", .message = "a replay must name its session" });
        }
        const entry = self.find(addressed) orelse {
            return self.writeControl(.{ .control = "replay.error", .id = id, .session_id = addressed, .run_id = run_id, .code = "unknown_session", .message = "no session is open under that session_id" });
        };
        var refusal = contract.Refusal{};
        const replaying = if (entry.session.vtable.replay) |replayer|
            replayer(entry.session.ptr, arena, run_id orelse "", after, &refusal)
        else
            self.replayJournal(entry, arena, run_id orelse "", after);
        const replayed = replaying catch |failure| {
            if (failure == error.OutOfMemory) return error.OutOfMemory;
            const code: []const u8 = switch (failure) {
                error.RunNotFound => "run_not_found",
                error.ReplayCursorFuture => "replay_cursor_future",
                error.SessionClosed => "session_closed",
                else => "internal",
            };
            return self.writeControl(.{ .control = "replay.error", .id = id, .session_id = addressed, .run_id = run_id, .code = code, .message = if (refusal.message.len > 0) refusal.message else @errorName(failure) });
        };
        switch (replayed) {
            .gap => |gap| try self.writeControl(.{
                .control = "replay.gap",
                .id = id,
                .session_id = addressed,
                .run_id = run_id,
                .requested_after = gap.requested_after,
                .oldest_available = gap.oldest_available,
                .latest_available = gap.latest_available,
                .message = "the requested replay cursor is no longer retained; ask again from oldest_available - 1",
            }),
            .events => |events| {
                const resolved = if (events.len > 0) events[0].run_id else if (run_id) |named| named else if (entry.cursors.items.len > 0) entry.cursors.items[entry.cursors.items.len - 1].run_id else "";
                try self.writeControl(.{ .control = "replay.accepted", .id = id, .session_id = addressed, .run_id = resolved, .after = after });
                if (resolved.len > 0) (try self.cursorFor(entry, resolved)).lost = false;
                for (events) |event| try self.pushEvent(entry, event);
            },
        }
    }

    const ControlFrame = struct {
        control: []const u8,
        id: ?[]const u8 = null,
        session_id: ?[]const u8 = null,
        run_id: ?[]const u8 = null,
        after: ?u64 = null,
        requested_after: ?u64 = null,
        oldest_available: ?u64 = null,
        latest_available: ?u64 = null,
        code: ?[]const u8 = null,
        message: ?[]const u8 = null,
    };

    fn writeControl(self: *Endpoint, frame: ControlFrame) !void {
        var buffer = std.ArrayList(u8).empty;
        defer buffer.deinit(self.allocator);
        var w = json_writer.JsonWriter.init(&buffer, self.allocator);
        try w.beginObject();
        try w.writeStringField("control", frame.control);
        if (frame.id) |value| try w.writeStringField("id", value);
        if (frame.session_id) |value| if (value.len > 0) try w.writeStringField("session_id", value);
        if (frame.run_id) |value| if (value.len > 0) try w.writeStringField("run_id", value);
        if (frame.after) |value| try w.writeIntField("after", value);
        if (frame.requested_after) |value| try w.writeIntField("requested_after", value);
        if (frame.oldest_available) |value| try w.writeIntField("oldest_available", value);
        if (frame.latest_available) |value| try w.writeIntField("latest_available", value);
        if (frame.code) |value| try w.writeStringField("code", value);
        if (frame.message) |value| try w.writeStringField("message", value);
        try w.endObject();
        const line = try self.allocator.dupe(u8, buffer.items);
        errdefer self.allocator.free(line);
        try self.outbound.append(self.allocator, line);
    }

    fn drainAll(self: *Endpoint) !void {
        for (self.entries.items) |*entry| try self.drainEntry(entry);
    }

    fn drainEntry(self: *Endpoint, entry: *Entry) !void {
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        var events = std.ArrayList(contract.Event).empty;
        try entry.session.drain(scratch.allocator(), &events);
        for (events.items) |event| {
            try self.remember(entry, event);
            try self.pushEvent(entry, event);
        }
    }

    fn remember(self: *Endpoint, entry: *Entry, event: contract.Event) !void {
        _ = try self.cursorFor(entry, event.run_id);
        const index = self.cursorIndex(entry, event.run_id).?;
        const cursor = &entry.cursors.items[index];
        cursor.latest = @max(cursor.latest, event.sequence);
        if (self.journal_capacity == 0) return;
        const line = try self.allocator.dupe(u8, event.line);
        errdefer self.allocator.free(line);
        try entry.journal.ensureUnusedCapacity(self.allocator, 1);
        if (entry.journal.items.len == self.journal_capacity) self.allocator.free(entry.journal.orderedRemove(0).line);
        entry.journal.appendAssumeCapacity(.{ .line = line, .cursor = index, .sequence = event.sequence });
    }

    fn cursorIndex(self: *Endpoint, entry: *Entry, run_id: []const u8) ?usize {
        _ = self;
        for (entry.cursors.items, 0..) |cursor, index| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return index;
        }
        return null;
    }

    fn replayJournal(self: *Endpoint, entry: *Entry, arena: std.mem.Allocator, run_id: []const u8, after: u64) contract.Failure!contract.Replay {
        const index = if (run_id.len > 0) self.cursorIndex(entry, run_id) orelse return error.RunNotFound else if (entry.cursors.items.len > 0) entry.cursors.items.len - 1 else return error.RunNotFound;
        const latest = entry.cursors.items[index].latest;
        if (after > latest) return error.ReplayCursorFuture;
        var oldest: u64 = 0;
        var suffix = std.ArrayList(contract.Event).empty;
        for (entry.journal.items) |kept| {
            if (kept.cursor != index) continue;
            if (oldest == 0) oldest = kept.sequence;
            if (kept.sequence > after) try suffix.append(arena, .{ .line = kept.line, .run_id = entry.cursors.items[index].run_id, .sequence = kept.sequence });
        }
        if (after < latest and (oldest == 0 or after + 1 < oldest)) {
            return .{ .gap = .{ .requested_after = after, .oldest_available = oldest, .latest_available = latest } };
        }
        return .{ .events = suffix.items };
    }

    fn cursorFor(self: *Endpoint, entry: *Entry, run_id: []const u8) !*Cursor {
        for (entry.cursors.items) |*cursor| {
            if (std.mem.eql(u8, cursor.run_id, run_id)) return cursor;
        }
        try entry.cursors.ensureUnusedCapacity(self.allocator, 1);
        const owned = try self.allocator.dupe(u8, run_id);
        entry.cursors.appendAssumeCapacity(.{ .run_id = owned });
        return &entry.cursors.items[entry.cursors.items.len - 1];
    }

    fn pushEvent(self: *Endpoint, entry: *Entry, event: contract.Event) !void {
        const cursor = try self.cursorFor(entry, event.run_id);
        const fits = event.line.len + 1 <= self.frame_limit;
        if (cursor.lost) {
            if (fits and try settlesRun(self.allocator, event.line)) try self.deliver(cursor, event);
            return;
        }
        if (!fits) {
            cursor.lost = true;
            return self.writeControl(.{
                .control = "stream.lost",
                .run_id = cursor.run_id,
                .after = cursor.delivered,
                .code = "frame_limit",
                .message = "this run's events stopped reaching the host; replay from after to continue",
            });
        }
        try self.deliver(cursor, event);
    }

    fn deliver(self: *Endpoint, cursor: *Cursor, event: contract.Event) !void {
        const line = try self.allocator.dupe(u8, event.line);
        errdefer self.allocator.free(line);
        try self.outbound.append(self.allocator, line);
        cursor.delivered = event.sequence;
    }
};

fn settlesRun(allocator: std.mem.Allocator, line: []const u8) !bool {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch |err| switch (err) {
        error.OutOfMemory => return err,
        else => return false,
    };
    defer parsed.deinit();
    if (parsed.value != .object) return false;
    const kind = parsed.value.object.get("type") orelse return false;
    if (kind != .string) return false;
    return listed(&.{ "run.completed", "run.failed", "run.cancelled" }, kind.string);
}

const Mapped = struct { code: []const u8, fallback: []const u8 };

fn codeFor(failure: contract.Failure) Mapped {
    return switch (failure) {
        error.UnsupportedFeature => .{ .code = "unsupported_feature", .fallback = "adapter: unsupported input" },
        error.CapabilityDegraded => .{ .code = "capability_degraded", .fallback = "adapter: unsupported input: the feature is degraded and was not opted into" },
        error.ModelNotFound => .{ .code = "model_not_found", .fallback = "adapter: model is not in the effective catalog" },
        error.SessionClosed => .{ .code = "session_closed", .fallback = "adapter: session closed" },
        error.RunActive => .{ .code = "run_active", .fallback = "adapter: a run is already active" },
        error.InvalidSubmission => .{ .code = "invalid_submission", .fallback = "adapter: invalid submission" },
        error.RunNotFound => .{ .code = "run_not_found", .fallback = "adapter: run not found" },
        error.RunTerminal => .{ .code = "run_terminal", .fallback = "adapter: run already completed or failed" },
        error.InteractionNotFound => .{ .code = "resolution_rejected", .fallback = "adapter: interaction not found" },
        error.InvalidResolution => .{ .code = "resolution_rejected", .fallback = "adapter: invalid interaction resolution" },
        error.ToolCatalogUnavailable => .{ .code = "tool_catalog_unavailable", .fallback = "adapter: no portable tool catalog is served" },
        error.Unavailable => .{ .code = "unavailable", .fallback = "this backend is unavailable" },
        error.ReplayCursorFuture => .{ .code = "replay_cursor_future", .fallback = "adapter: replay cursor is newer than the run" },
        error.BackendFailed, error.OutOfMemory => .{ .code = "internal", .fallback = "the backend failed" },
    };
}

fn listed(values: []const []const u8, wanted: []const u8) bool {
    for (values) |value| {
        if (std.mem.eql(u8, value, wanted)) return true;
    }
    return false;
}

fn declaredString(root: std.json.ObjectMap, key: []const u8) Error!?[]const u8 {
    const value = root.get(key) orelse return null;
    return switch (value) {
        .string => |text| text,
        .null => null,
        else => Error.MalformedLine,
    };
}

fn scopeString(root: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = root.get(key) orelse return null;
    if (value != .string or value.string.len == 0) return null;
    return value.string;
}

const testing = std.testing;

const fake_features = [_]contract.Feature{
    .{ .key = "session.message.submit", .level = .degraded, .reason = "scripted" },
    .{ .key = "run.cancel", .level = .degraded },
    .{ .key = "run.instructions", .level = .native },
    .{ .key = "user_input", .level = .native },
};

const fake_sources = [_]oap_types.ToolSourceDescriptor{.{ .id = "fake-native", .kind = "native", .display_name = "Fake tools" }};

const fake_descriptor = contract.Descriptor{
    .endpoint = .{ .id = "fake.endpoint", .name = "Fake Endpoint", .version = "1" },
    .capability_revision = "fake-v1",
    .features = &fake_features,
    .sources = &fake_sources,
};

const Fake = struct {
    allocator: std.mem.Allocator,
    opened: usize = 0,
    closed: usize = 0,
    participant: [32]u8 = undefined,
    participant_len: usize = 0,
    submit_failure: ?contract.Failure = null,
    submit_refusal: contract.Refusal = .{},
    offers_models: bool = false,
    offers_switch: bool = false,
    offers_tools: bool = false,
    oversized_event: bool = false,

    fn adapter(self: *Fake) contract.Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn probe(ptr: *anyopaque, refusal: *contract.Refusal) contract.Failure!contract.Descriptor {
        _ = ptr;
        _ = refusal;
        return fake_descriptor;
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: contract.OpenRequest, refusal: *contract.Refusal) contract.Failure!contract.Session {
        _ = arena;
        _ = refusal;
        const self: *Fake = @ptrCast(@alignCast(ptr));
        self.opened += 1;
        @memcpy(self.participant[0..request.participant.len], request.participant);
        self.participant_len = request.participant.len;
        const session = try self.allocator.create(FakeSession);
        errdefer self.allocator.destroy(session);
        const minted = if (request.session_id.len > 0)
            try self.allocator.dupe(u8, request.session_id)
        else
            try std.fmt.allocPrint(self.allocator, "fake-session-{d}", .{self.opened});
        session.* = .{ .fake = self, .id_text = minted };
        return session.session();
    }

    fn lastParticipant(self: *const Fake) []const u8 {
        return self.participant[0..self.participant_len];
    }
};

const FakeSession = struct {
    fake: *Fake,
    id_text: []u8,
    pending: std.ArrayList([]u8) = .empty,
    runs: usize = 0,
    active: bool = false,
    settles_on_pump: bool = false,

    fn session(self: *FakeSession) contract.Session {
        return .{ .ptr = self, .vtable = &vtable };
    }

    fn cast(ptr: *anyopaque) *FakeSession {
        return @ptrCast(@alignCast(ptr));
    }

    fn queue(self: *FakeSession, comptime format: []const u8, args: anytype) !void {
        const line = try std.fmt.allocPrint(self.fake.allocator, format, args);
        errdefer self.fake.allocator.free(line);
        try self.pending.append(self.fake.allocator, line);
    }

    fn idOf(ptr: *anyopaque) []const u8 {
        return cast(ptr).id_text;
    }

    fn stateOf(ptr: *anyopaque, arena: std.mem.Allocator, refusal: *contract.Refusal) contract.Failure!oap_types.SessionState {
        _ = arena;
        _ = refusal;
        const self = cast(ptr);
        return .{ .session_id = self.id_text, .status = if (self.active) .running else .idle };
    }

    fn submit(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *contract.Refusal) contract.Failure!oap_types.MessageSubmitResponse {
        const self = cast(ptr);
        if (self.fake.submit_failure) |failure| {
            refusal.* = self.fake.submit_refusal;
            return failure;
        }
        if (self.active) return error.RunActive;
        self.runs += 1;
        self.active = true;
        const run_id = try std.fmt.allocPrint(arena, "run-{d}", .{self.runs});
        try self.queue("{{\"type\":\"run.started\",\"run_id\":\"run-{d}\",\"sequence\":1}}", .{self.runs});
        if (self.fake.oversized_event) {
            try self.queue("{{\"type\":\"content.delta\",\"run_id\":\"run-{d}\",\"sequence\":2,\"text\":\"{s}\"}}", .{ self.runs, "x" ** 1024 });
            try self.queue("{{\"type\":\"run.completed\",\"run_id\":\"run-{d}\",\"sequence\":3}}", .{self.runs});
        }
        return .{
            .session_id = request.session_id,
            .accepted = true,
            .submission_id = "submission-1",
            .requested_delivery = .auto,
            .effective_delivery = .start,
            .admission = .started,
            .run_id = run_id,
            .status = .running,
        };
    }

    fn resolve(ptr: *anyopaque, arena: std.mem.Allocator, resolution: contract.Resolution, refusal: *contract.Refusal) contract.Failure!void {
        _ = arena;
        _ = refusal;
        const self = cast(ptr);
        switch (resolution) {
            .input => |request| try self.queue("{{\"type\":\"user.input.resolved\",\"run_id\":\"{s}\",\"sequence\":2}}", .{request.run_id}),
            .permission => return error.InteractionNotFound,
        }
    }

    fn cancel(ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *contract.Refusal) contract.Failure!oap_types.RunCancelResponse {
        _ = arena;
        _ = refusal;
        const self = cast(ptr);
        if (!self.active) return error.RunNotFound;
        self.active = false;
        try self.queue("{{\"type\":\"run.cancelled\",\"run_id\":\"{s}\",\"sequence\":2}}", .{run_id});
        return .{ .session_id = self.id_text, .run_id = run_id, .accepted = true, .status = .cancelling };
    }

    fn pump(ptr: *anyopaque, wait_ns: u64) contract.Failure!bool {
        _ = wait_ns;
        const self = cast(ptr);
        if (!self.settles_on_pump or !self.active) return false;
        self.active = false;
        try self.queue("{{\"type\":\"run.completed\",\"run_id\":\"run-{d}\",\"sequence\":2}}", .{self.runs});
        return true;
    }

    fn drain(ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(contract.Event)) contract.Failure!void {
        const self = cast(ptr);
        for (self.pending.items) |line| {
            const copy = try allocator.dupe(u8, line);
            const sequence: u64 = if (std.mem.indexOf(u8, line, "\"sequence\":1") != null) 1 else if (std.mem.indexOf(u8, line, "\"sequence\":2") != null) 2 else 3;
            try out.append(allocator, .{ .line = copy, .run_id = runOf(copy), .sequence = sequence });
        }
        for (self.pending.items) |line| self.fake.allocator.free(line);
        self.pending.clearRetainingCapacity();
    }

    fn runOf(line: []const u8) []const u8 {
        const start = (std.mem.indexOf(u8, line, "\"run_id\":\"") orelse return "") + "\"run_id\":\"".len;
        const end = std.mem.indexOfScalarPos(u8, line, start, '"') orelse return "";
        return line[start..end];
    }

    fn activityOf(ptr: *anyopaque) contract.Activity {
        return if (cast(ptr).active) .running else .idle;
    }

    fn close(ptr: *anyopaque) void {
        const self = cast(ptr);
        const allocator = self.fake.allocator;
        self.fake.closed += 1;
        for (self.pending.items) |line| allocator.free(line);
        self.pending.deinit(allocator);
        allocator.free(self.id_text);
        allocator.destroy(self);
    }

    fn models(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ModelsResponse {
        _ = ptr;
        _ = refusal;
        const listed_models = try arena.dupe(oap_types.ModelDescriptor, &.{.{ .id = "fake-model", .default = true }});
        return .{ .session_id = request.session_id, .current_model_id = "fake-model", .models = listed_models };
    }

    fn switchModel(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionModelSwitchRequest, refusal: *contract.Refusal) contract.Failure!contract.Switched {
        _ = arena;
        _ = refusal;
        const self = cast(ptr);
        return .{
            .response = .{ .session_id = self.id_text, .model_id = request.model_id, .previous_model_id = "fake-model" },
            .state = .{ .session_id = self.id_text, .status = .idle, .current_model_id = request.model_id },
        };
    }

    fn tools(ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *contract.Refusal) contract.Failure!oap_types.ToolsListResponse {
        _ = ptr;
        _ = arena;
        if (!request.allowsDegraded(contract.feature_tools_list)) return refusal.degraded(contract.feature_tools_list);
        return .{ .session_id = request.session_id };
    }

    const vtable = contract.Session.VTable{
        .id = idOf,
        .state = stateOf,
        .submit = submit,
        .resolve = resolve,
        .cancel = cancel,
        .pump = pump,
        .drain = drain,
        .activity = activityOf,
        .close = close,
    };

    const offering = contract.Session.VTable{
        .id = idOf,
        .state = stateOf,
        .submit = submit,
        .resolve = resolve,
        .cancel = cancel,
        .pump = pump,
        .drain = drain,
        .activity = activityOf,
        .close = close,
        .models = models,
        .switch_model = switchModel,
        .tools = tools,
    };
};

const Harness = struct {
    fake: Fake,
    endpoint: Endpoint,
    arena: std.heap.ArenaAllocator,

    fn init(self: *Harness, allocator: std.mem.Allocator, options: Options) void {
        self.fake = .{ .allocator = allocator };
        self.endpoint = Endpoint.init(allocator, self.fake.adapter(), options);
        self.arena = std.heap.ArenaAllocator.init(allocator);
    }

    fn deinit(self: *Harness) void {
        self.endpoint.deinit();
        self.arena.deinit();
    }

    fn send(self: *Harness, line: []const u8) ![]std.json.Value {
        try self.endpoint.handleLine(line);
        return self.collect();
    }

    fn collect(self: *Harness) ![]std.json.Value {
        var answers = std.ArrayList(std.json.Value).empty;
        while (self.endpoint.popOutbound()) |line| {
            defer self.endpoint.allocator.free(line);
            try answers.append(self.arena.allocator(), try std.json.parseFromSliceLeaky(std.json.Value, self.arena.allocator(), line, .{}));
        }
        return answers.items;
    }

    fn offerEverything(self: *Harness) void {
        for (self.endpoint.entries.items) |*entry| entry.session.vtable = &FakeSession.offering;
    }
};

fn framed(comptime kind: []const u8, comptime id: []const u8, comptime scope: []const u8, comptime payload: []const u8) []const u8 {
    return "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\"," ++
        "\"type\":\"" ++ kind ++ "\",\"id\":\"" ++ id ++ "\"" ++ scope ++ ",\"payload\":" ++ payload ++ "}";
}

fn field(value: std.json.Value, comptime path: []const []const u8) []const u8 {
    var current = value;
    inline for (path) |key| {
        current = current.object.get(key) orelse return "";
    }
    return if (current == .string) current.string else "";
}

const open_line = framed("session.open.request", "open-1", "", "{\"session_id\":\"s1\"}");
const submit_line = framed("session.message.submit.request", "submit-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}");

test "a line that is not one JSON object is a framing defect" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    try testing.expectError(Error.MalformedLine, harness.endpoint.handleLine("not json"));
    try testing.expectError(Error.MalformedLine, harness.endpoint.handleLine("[1,2]"));
    try testing.expectError(Error.MalformedLine, harness.endpoint.handleLine("{\"hello\":1}"));
    try testing.expectError(Error.MalformedLine, harness.endpoint.handleLine("{\"protocol\":7,\"id\":\"a\"}"));
}

test "an envelope carrying no id is unaddressable, whatever else it says" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    try testing.expectError(Error.UnaddressableEnvelope, harness.endpoint.handleLine("{\"protocol\":\"open-agent-protocol\",\"type\":\"capabilities.request\"}"));
    try testing.expectError(Error.UnaddressableEnvelope, harness.endpoint.handleLine("{\"protocol\":\"open-agent-protocol\",\"id\":\"\"}"));
    try testing.expectError(Error.UnaddressableEnvelope, harness.endpoint.handleLine("{\"protocol\":\"open-agent-protocol\",\"id\":4}"));
}

test "a line over the frame limit is refused before it is parsed" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{ .frame_limit = 16 });
    defer harness.deinit();
    try testing.expectError(Error.FrameTooLarge, harness.endpoint.handleLine("{\"control\":\"replay\",\"id\":\"r1\"}"));
}

test "a control this endpoint does not serve is answered, and the stream carries on" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const answered = try harness.send("{\"control\":\"rewind\",\"id\":\"c1\"}");
    try testing.expectEqual(@as(usize, 1), answered.len);
    try testing.expectEqualStrings("replay.error", field(answered[0], &.{"control"}));
    try testing.expectEqualStrings("c1", field(answered[0], &.{"id"}));
    try testing.expectEqualStrings("unsupported_control", field(answered[0], &.{"code"}));

    const capabilities = try harness.send(framed("capabilities.request", "cap-1", "", "{}"));
    try testing.expectEqualStrings("capabilities.response", field(capabilities[0], &.{"type"}));
}

test "a replay re-delivers a run's journalled suffix, refuses a future or unknown cursor, and reports a gap past capacity" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{ .journal_capacity = 2 });
    defer harness.deinit();
    _ = try harness.send(open_line);
    harness.fake.oversized_event = true;
    const submitted = try harness.send(framed("session.message.submit.request", "s-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"));
    const run_id = field(submitted[1], &.{"run_id"});
    const events = submitted.len - 1;
    try testing.expect(events >= 3);

    const tail = try harness.send(try std.fmt.allocPrint(harness.arena.allocator(), "{{\"control\":\"replay\",\"id\":\"r1\",\"session_id\":\"s1\",\"run_id\":\"{s}\",\"after\":{d}}}", .{ run_id, events - 1 }));
    try testing.expectEqualStrings("replay.accepted", field(tail[0], &.{"control"}));
    try testing.expectEqual(@as(usize, 2), tail.len);
    try testing.expectEqual(@as(i64, @intCast(events)), tail[1].object.get("sequence").?.integer);

    const gap = try harness.send(try std.fmt.allocPrint(harness.arena.allocator(), "{{\"control\":\"replay\",\"id\":\"r2\",\"session_id\":\"s1\",\"run_id\":\"{s}\",\"after\":0}}", .{run_id}));
    try testing.expectEqualStrings("replay.gap", field(gap[0], &.{"control"}));
    try testing.expectEqual(@as(i64, @intCast(events - 1)), gap[0].object.get("oldest_available").?.integer);

    const future = try harness.send(try std.fmt.allocPrint(harness.arena.allocator(), "{{\"control\":\"replay\",\"id\":\"r3\",\"session_id\":\"s1\",\"run_id\":\"{s}\",\"after\":99}}", .{run_id}));
    try testing.expectEqualStrings("replay_cursor_future", field(future[0], &.{"code"}));
    const unknown_run = try harness.send("{\"control\":\"replay\",\"id\":\"r4\",\"session_id\":\"s1\",\"run_id\":\"nope\",\"after\":0}");
    try testing.expectEqualStrings("run_not_found", field(unknown_run[0], &.{"code"}));

    const unnamed = try harness.send("{\"control\":\"replay\",\"id\":\"r5\"}");
    try testing.expectEqualStrings("invalid_request", field(unnamed[0], &.{"code"}));
    const unknown = try harness.send("{\"control\":\"replay\",\"id\":\"r6\",\"session_id\":\"nope\"}");
    try testing.expectEqualStrings("unknown_session", field(unknown[0], &.{"code"}));
}

test "a replay after a lost frame delivers the run again from the cursor" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{ .frame_limit = 800 });
    defer harness.deinit();
    _ = try harness.send(open_line);
    harness.fake.oversized_event = true;
    const answered = try harness.send(framed("session.message.submit.request", "s-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"));
    const run_id = field(answered[1], &.{"run_id"});
    const replayed = try harness.send(try std.fmt.allocPrint(harness.arena.allocator(), "{{\"control\":\"replay\",\"id\":\"r1\",\"session_id\":\"s1\",\"run_id\":\"{s}\",\"after\":2}}", .{run_id}));
    try testing.expectEqualStrings("replay.accepted", field(replayed[0], &.{"control"}));
    try testing.expectEqualStrings("run.completed", field(replayed[replayed.len - 1], &.{"type"}));
}

test "an envelope missing a base member draws a correlated invalid_request naming it" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const answered = try harness.send("{\"protocol\":\"open-agent-protocol\",\"id\":\"bare-1\",\"type\":\"capabilities.request\"}");
    try testing.expectEqualStrings("error.response", field(answered[0], &.{"type"}));
    try testing.expectEqualStrings("bare-1", field(answered[0], &.{"in_reply_to"}));
    try testing.expectEqualStrings("invalid_request", field(answered[0], &.{ "payload", "error", "code" }));
    try testing.expect(std.mem.indexOf(u8, field(answered[0], &.{ "payload", "error", "message" }), "version, profile, payload") != null);
}

test "a request type this endpoint does not serve draws unsupported_request" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const unknown = try harness.send(framed("session.rename.request", "x-1", "", "{}"));
    try testing.expectEqualStrings("unsupported_request", field(unknown[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("x-1", field(unknown[0], &.{"in_reply_to"}));

    const event = try harness.send(framed("run.status.updated", "x-2", "", "{\"session_id\":\"s\",\"run_id\":\"r\",\"status\":\"running\"}"));
    try testing.expectEqualStrings("unsupported_request", field(event[0], &.{ "payload", "error", "code" }));

    const undecodable = try harness.send(framed("session.message.submit.request", "x-3", "", "{\"session_id\":\"s\"}"));
    try testing.expectEqualStrings("invalid_payload", field(undecodable[0], &.{ "payload", "error", "code" }));
}

test "initialize declares the backend's endpoint and revision, and names the control participant for later opens" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const initialized = try harness.send(framed("protocol.initialize.request", "init-1", "", "{\"protocol_versions\":[\"0.1\"],\"profiles\":[\"open-agent-protocol.agent-control-core\"],\"participant\":{\"id\":\"operator\"}}"));
    try testing.expectEqualStrings("protocol.initialize.response", field(initialized[0], &.{"type"}));
    try testing.expectEqualStrings("fake.endpoint", field(initialized[0], &.{ "payload", "endpoint", "id" }));
    try testing.expectEqualStrings("fake-v1", field(initialized[0], &.{"capability_revision"}));

    _ = try harness.send(open_line);
    try testing.expectEqualStrings("operator", harness.fake.lastParticipant());
}

test "an open before any initialize names the default control participant" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);
    try testing.expectEqualStrings(default_participant, harness.fake.lastParticipant());
}

test "initialize refuses a peer offering no version or profile this endpoint serves" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const refused = try harness.send(framed("protocol.initialize.request", "init-1", "", "{\"protocol_versions\":[\"9.9\"],\"profiles\":[\"open-agent-protocol.agent-control-core\"]}"));
    try testing.expectEqualStrings("unsupported_feature", field(refused[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("protocol.initialize", field(refused[0], &.{ "payload", "error", "details", "feature" }));
}

test "capabilities carry the descriptor's features, sources and revision over a stdio binding" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const answered = try harness.send(framed("capabilities.request", "cap-1", "", "{}"));
    const payload = answered[0].object.get("payload").?;
    try testing.expectEqualStrings("fake-v1", field(answered[0], &.{"capability_revision"}));
    try testing.expectEqualStrings("degraded", field(payload, &.{ "features", "run.cancel", "level" }));
    try testing.expectEqualStrings("scripted", field(payload, &.{ "features", "session.message.submit", "reason" }));
    try testing.expectEqualStrings("fake-native", field(payload.object.get("sources").?.array.items[0], &.{"id"}));
    try testing.expectEqualStrings("stdio", field(payload.object.get("bindings").?.array.items[0], &.{"kind"}));
}

test "a stale revision is refused on every request but initialize and capabilities" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const stale = try harness.send(framed("session.open.request", "open-1", ",\"capability_revision\":\"fake-v0\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("stale_capabilities", field(stale[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("fake-v0", field(stale[0], &.{ "payload", "error", "details", "expected_revision" }));
    try testing.expectEqualStrings("fake-v1", field(stale[0], &.{ "payload", "error", "details", "current_revision" }));
    try testing.expectEqual(@as(usize, 0), harness.fake.opened);

    const discovery = try harness.send(framed("capabilities.request", "cap-1", ",\"capability_revision\":\"fake-v0\"", "{}"));
    try testing.expectEqualStrings("capabilities.response", field(discovery[0], &.{"type"}));

    const current = try harness.send(framed("session.open.request", "open-2", ",\"capability_revision\":\"fake-v1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("session.open.response", field(current[0], &.{"type"}));
    try testing.expectEqualStrings("fake-v1", field(current[0], &.{"capability_revision"}));
}

test "a session-scoped request must name a session this endpoint opened" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const unnamed = try harness.send(framed("session.state.request", "state-1", "", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("invalid_request", field(unnamed[0], &.{ "payload", "error", "code" }));
    const unknown = try harness.send(framed("session.state.request", "state-2", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("unknown_session", field(unknown[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("no session \"s1\"", field(unknown[0], &.{ "payload", "error", "message" }));
    try testing.expectEqualStrings("s1", field(unknown[0], &.{"session_id"}));
}

test "a second open under an id already open is refused session_exists without reaching the backend" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    _ = try harness.send(open_line);
    const again = try harness.send(framed("session.open.request", "open-2", "", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("session_exists", field(again[0], &.{ "payload", "error", "code" }));
    try testing.expectEqual(@as(usize, 1), harness.fake.opened);
}

test "an open electing a feature the descriptor lacks is refused before any session exists" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();

    const refused = try harness.send(framed("session.open.request", "open-1", "", "{\"session_id\":\"s1\",\"tools\":[{\"name\":\"t\"}]}"));
    try testing.expectEqualStrings("unsupported_feature", field(refused[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("action.tools.provide", field(refused[0], &.{ "payload", "error", "details", "feature" }));
    try testing.expectEqualStrings("unadvertised", field(refused[0], &.{ "payload", "error", "details", "reason" }));
    try testing.expectEqual(@as(usize, 0), harness.fake.opened);
    try testing.expectEqual(@as(usize, 0), harness.endpoint.sessionCount());
}

test "a payload scoped to another session is refused scope_mismatch" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);

    const crossed = try harness.send(framed("session.message.submit.request", "submit-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s2\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"));
    try testing.expectEqualStrings("scope_mismatch", field(crossed[0], &.{ "payload", "error", "code" }));
}

test "the admission is written before the run's first event" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);

    const answered = try harness.send(submit_line);
    try testing.expectEqual(@as(usize, 2), answered.len);
    try testing.expectEqualStrings("session.message.submit.response", field(answered[0], &.{"type"}));
    try testing.expectEqualStrings("run-1", field(answered[0], &.{"run_id"}));
    try testing.expectEqualStrings("run.started", field(answered[1], &.{"type"}));
}

test "events already produced are written before the next answer" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);
    _ = try harness.send(submit_line);

    const session: *FakeSession = @ptrCast(@alignCast(harness.endpoint.entries.items[0].session.ptr));
    try session.queue("{{\"type\":\"content.delta\",\"run_id\":\"run-1\",\"sequence\":2}}", .{});
    const answered = try harness.send(framed("session.state.request", "state-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqual(@as(usize, 2), answered.len);
    try testing.expectEqualStrings("content.delta", field(answered[0], &.{"type"}));
    try testing.expectEqualStrings("session.state.response", field(answered[1], &.{"type"}));
    try testing.expectEqualStrings("running", field(answered[1], &.{ "payload", "status" }));
}

test "a cancel is acknowledged before the terminal it leads to" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);
    _ = try harness.send(submit_line);

    const answered = try harness.send(framed("run.cancel.request", "cancel-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"run_id\":\"run-1\"}"));
    try testing.expectEqual(@as(usize, 2), answered.len);
    try testing.expectEqualStrings("run.cancel.response", field(answered[0], &.{"type"}));
    try testing.expectEqualStrings("cancelling", field(answered[0], &.{ "payload", "status" }));
    try testing.expectEqualStrings("run.cancelled", field(answered[1], &.{"type"}));

    const late = try harness.send(framed("run.cancel.request", "cancel-2", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"run_id\":\"run-1\"}"));
    try testing.expectEqualStrings("run_not_found", field(late[0], &.{ "payload", "error", "code" }));
}

test "a resolution is acknowledged under the type its request named" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);

    const input = try harness.send(framed("user.input.resolve.request", "resolve-1", ",\"session_id\":\"s1\"", "{\"interaction_id\":\"i\",\"requested_by\":\"e\",\"responded_by\":\"user\",\"session_id\":\"s1\",\"run_id\":\"run-9\",\"answers\":[{\"question_id\":\"q\",\"text\":\"ok\"}]}"));
    try testing.expectEqualStrings("user.input.resolve.response", field(input[0], &.{"type"}));
    try testing.expectEqualStrings("run-9", field(input[0], &.{"run_id"}));
    try testing.expectEqualStrings("user.input.resolved", field(input[1], &.{"type"}));

    const permission = try harness.send(framed("action.permission.resolve.request", "resolve-2", ",\"session_id\":\"s1\"", "{\"interaction_id\":\"i\",\"requested_by\":\"e\",\"responded_by\":\"user\",\"session_id\":\"s1\",\"run_id\":\"run-9\",\"granted\":true}"));
    try testing.expectEqualStrings("resolution_rejected", field(permission[0], &.{ "payload", "error", "code" }));
}

test "each backend refusal reaches the host under its typed code and details" {
    const cases = [_]struct { failure: contract.Failure, refusal: contract.Refusal, code: []const u8, detail: []const u8, value: []const u8, message: []const u8 = "" }{
        .{ .failure = error.UnsupportedFeature, .refusal = .{ .feature = "run.tool_selection", .reason = "unadvertised" }, .code = "unsupported_feature", .detail = "feature", .value = "run.tool_selection", .message = "adapter: unsupported input: run.tool_selection (unadvertised)" },
        .{ .failure = error.CapabilityDegraded, .refusal = .{ .feature = "run.instructions" }, .code = "capability_degraded", .detail = "feature", .value = "run.instructions", .message = "adapter: unsupported input: run.instructions is degraded and was not opted into" },
        .{ .failure = error.ModelNotFound, .refusal = .{ .model_id = "ghost" }, .code = "model_not_found", .detail = "model_id", .value = "ghost" },
        .{ .failure = error.SessionClosed, .refusal = .{}, .code = "session_closed", .detail = "", .value = "" },
        .{ .failure = error.RunActive, .refusal = .{}, .code = "run_active", .detail = "", .value = "" },
        .{ .failure = error.InvalidSubmission, .refusal = .{}, .code = "invalid_submission", .detail = "", .value = "" },
        .{ .failure = error.BackendFailed, .refusal = .{ .message = "the child exited" }, .code = "internal", .detail = "", .value = "" },
        .{ .failure = error.Unavailable, .refusal = .{ .backend = "hermes" }, .code = "unavailable", .detail = "backend", .value = "hermes" },
    };
    for (cases) |case| {
        var harness: Harness = undefined;
        harness.init(testing.allocator, .{});
        defer harness.deinit();
        _ = try harness.send(open_line);
        harness.fake.submit_failure = case.failure;
        harness.fake.submit_refusal = case.refusal;

        const refused = try harness.send(submit_line);
        try testing.expectEqual(@as(usize, 1), refused.len);
        try testing.expectEqualStrings(case.code, field(refused[0], &.{ "payload", "error", "code" }));
        try testing.expectEqualStrings("submit-1", field(refused[0], &.{"in_reply_to"}));
        if (case.detail.len > 0) {
            const details = refused[0].object.get("payload").?.object.get("error").?.object.get("details").?;
            try testing.expectEqualStrings(case.value, details.object.get(case.detail).?.string);
        }
        if (case.refusal.message.len > 0) {
            try testing.expectEqualStrings(case.refusal.message, field(refused[0], &.{ "payload", "error", "message" }));
        }
        if (case.message.len > 0) {
            try testing.expectEqualStrings(case.message, field(refused[0], &.{ "payload", "error", "message" }));
        }
    }
}

test "an unadvertised run control is refused before the backend sees the submission" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);

    const refused = try harness.send(framed("session.message.submit.request", "submit-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"delivery\":\"auto\",\"model_id\":\"m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"));
    try testing.expectEqualStrings("unsupported_feature", field(refused[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("run.model_selection", field(refused[0], &.{ "payload", "error", "details", "feature" }));
    const session: *FakeSession = @ptrCast(@alignCast(harness.endpoint.entries.items[0].session.ptr));
    try testing.expectEqual(@as(usize, 0), session.runs);
}

test "an explicit queue the backend does not advertise is refused naming the key before the backend sees it" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);

    const refused = try harness.send(framed("session.message.submit.request", "submit-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"delivery\":\"queue\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"));
    try testing.expectEqualStrings("unsupported_feature", field(refused[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("session.message.delivery.queue", field(refused[0], &.{ "payload", "error", "details", "feature" }));
    try testing.expectEqualStrings("unadvertised", field(refused[0], &.{ "payload", "error", "details", "reason" }));
    const session: *FakeSession = @ptrCast(@alignCast(harness.endpoint.entries.items[0].session.ptr));
    try testing.expectEqual(@as(usize, 0), session.runs);
}

test "models, model switch, tools and call resolution are refused when the backend offers none" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);

    const models = try harness.send(framed("models.request", "models-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("unsupported_feature", field(models[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("models.list", field(models[0], &.{ "payload", "error", "details", "feature" }));
    try testing.expectEqualStrings("adapter: unsupported input: models.list (unadvertised)", field(models[0], &.{ "payload", "error", "message" }));

    const switched = try harness.send(framed("session.model.switch.request", "switch-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"model_id\":\"m\"}"));
    try testing.expectEqualStrings("session.model.switch", field(switched[0], &.{ "payload", "error", "details", "feature" }));

    const tools = try harness.send(framed("action.tools.list.request", "tools-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("tool_catalog_unavailable", field(tools[0], &.{ "payload", "error", "code" }));
    try testing.expectEqualStrings("adapter: no portable tool catalog is served", field(tools[0], &.{ "payload", "error", "message" }));

    const call = try harness.send(framed("action.call.resolve.request", "call-1", ",\"session_id\":\"s1\"", "{\"interaction_id\":\"i\",\"session_id\":\"s1\",\"run_id\":\"r\",\"tool_call_id\":\"t\",\"requested_by\":\"e\",\"responded_by\":\"u\",\"started\":{}}"));
    try testing.expectEqualStrings("action.tools.provide", field(call[0], &.{ "payload", "error", "details", "feature" }));
}

test "a backend that offers models, switching and tools is answered through them" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);
    harness.offerEverything();

    const models = try harness.send(framed("models.request", "models-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("models.response", field(models[0], &.{"type"}));
    try testing.expectEqualStrings("fake-v1", field(models[0], &.{"capability_revision"}));

    const switched = try harness.send(framed("session.model.switch.request", "switch-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"model_id\":\"other\"}"));
    try testing.expectEqual(@as(usize, 2), switched.len);
    try testing.expectEqualStrings("session.model.switch.response", field(switched[0], &.{"type"}));
    try testing.expectEqualStrings("session.state.updated", field(switched[1], &.{"type"}));
    try testing.expectEqualStrings("other", field(switched[1], &.{ "payload", "current_model_id" }));
    try testing.expectEqual(@as(i64, 1), switched[1].object.get("sequence").?.integer);

    const degraded = try harness.send(framed("action.tools.list.request", "tools-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\"}"));
    try testing.expectEqualStrings("capability_degraded", field(degraded[0], &.{ "payload", "error", "code" }));
    const listed_tools = try harness.send(framed("action.tools.list.request", "tools-2", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"allow_degraded_features\":[\"action.tools.list\"]}"));
    try testing.expectEqualStrings("action.tools.list.response", field(listed_tools[0], &.{"type"}));
}

test "an event past the frame limit is reported lost once, later events are dropped, and the terminal still settles the run" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{ .frame_limit = 800 });
    defer harness.deinit();
    _ = try harness.send(open_line);
    harness.fake.oversized_event = true;

    const answered = try harness.send(framed("session.message.submit.request", "s-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"delivery\":\"auto\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}"));
    try testing.expectEqual(@as(usize, 4), answered.len);
    try testing.expectEqualStrings("run.started", field(answered[1], &.{"type"}));
    try testing.expectEqualStrings("stream.lost", field(answered[2], &.{"control"}));
    try testing.expectEqualStrings("frame_limit", field(answered[2], &.{"code"}));
    try testing.expectEqual(@as(i64, 1), answered[2].object.get("after").?.integer);
    try testing.expect(std.mem.indexOf(u8, field(answered[2], &.{"message"}), "replay from after") != null);
    try testing.expectEqualStrings("run.completed", field(answered[3], &.{"type"}));
}

test "an unavailable backend answers every request unavailable, naming itself" {
    var unavailable = contract.Unavailable{ .backend = "hermes", .message = "the hermes backend is not in this build" };
    var endpoint = Endpoint.init(testing.allocator, unavailable.adapter(), .{});
    defer endpoint.deinit();

    const requests = [_][]const u8{
        framed("protocol.initialize.request", "init-1", "", "{\"protocol_versions\":[\"0.1\"],\"profiles\":[\"open-agent-protocol.agent-control-core\"]}"),
        framed("capabilities.request", "cap-1", "", "{}"),
        open_line,
    };
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    for (requests) |request| {
        try endpoint.handleLine(request);
        const line = endpoint.popOutbound().?;
        defer testing.allocator.free(line);
        const answer = try std.json.parseFromSliceLeaky(std.json.Value, arena.allocator(), line, .{});
        try testing.expectEqualStrings("unavailable", field(answer, &.{ "payload", "error", "code" }));
        try testing.expectEqualStrings("hermes", field(answer, &.{ "payload", "error", "details", "backend" }));
        try testing.expectEqualStrings("the hermes backend is not in this build", field(answer, &.{ "payload", "error", "message" }));
    }
}

var fake_clock: u64 = 0;

fn tick() u64 {
    fake_clock += std.time.ns_per_ms;
    return fake_clock;
}

test "finishing lets a running run settle before closing, and closes every session" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);
    _ = try harness.send(submit_line);
    const session: *FakeSession = @ptrCast(@alignCast(harness.endpoint.entries.items[0].session.ptr));
    session.settles_on_pump = true;

    try harness.endpoint.finish(default_settle_window_ns, tick);
    const settled = try harness.collect();
    try testing.expectEqualStrings("run.completed", field(settled[settled.len - 1], &.{"type"}));
    try testing.expectEqual(@as(usize, 1), harness.fake.closed);
    try testing.expectEqual(@as(usize, 0), harness.endpoint.sessionCount());
}

test "finishing does not wait on a run that never settles past its window" {
    var harness: Harness = undefined;
    harness.init(testing.allocator, .{});
    defer harness.deinit();
    _ = try harness.send(open_line);
    _ = try harness.send(submit_line);

    fake_clock = 0;
    try harness.endpoint.finish(50 * std.time.ns_per_ms, tick);
    try testing.expectEqual(@as(usize, 1), harness.fake.closed);
}

fn driveConversation(allocator: std.mem.Allocator) !void {
    var harness: Harness = undefined;
    harness.init(allocator, .{});
    defer harness.deinit();
    const lines = [_][]const u8{
        framed("protocol.initialize.request", "init-1", "", "{\"protocol_versions\":[\"0.1\"],\"profiles\":[\"open-agent-protocol.agent-control-core\"],\"participant\":{\"id\":\"operator\"}}"),
        framed("capabilities.request", "cap-1", "", "{}"),
        open_line,
        submit_line,
        framed("run.cancel.request", "cancel-1", ",\"session_id\":\"s1\"", "{\"session_id\":\"s1\",\"run_id\":\"run-1\"}"),
        framed("session.state.request", "state-1", ",\"capability_revision\":\"stale\"", "{\"session_id\":\"s1\"}"),
        "{\"control\":\"replay\",\"id\":\"r1\",\"session_id\":\"s1\"}",
    };
    for (lines) |line| {
        harness.endpoint.handleLine(line) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return err,
        };
        while (harness.endpoint.popOutbound()) |out| allocator.free(out);
    }
}

test "a whole conversation frees everything it built when any allocation fails" {
    try testing.checkAllAllocationFailures(testing.allocator, driveConversation, .{});
}
