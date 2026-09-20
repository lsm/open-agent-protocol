const std = @import("std");
const jsonschema = @import("jsonschema");

pub const code_duplicate_envelope_id = "duplicate_envelope_id";
pub const code_illegal_run_transition = "illegal_run_transition";
pub const code_missing_run_started = "missing_run_started";
pub const code_missing_run_terminal = "missing_run_terminal";
pub const code_duplicate_run_terminal = "duplicate_run_terminal";
pub const code_event_after_terminal = "event_after_terminal";
pub const code_sequence_gap = "sequence_gap";
pub const code_sequence_regression = "sequence_regression";
pub const code_cancel_not_settled = "cancel_not_settled";
pub const code_queue_order_violation = "queue_order_violation";
pub const code_queue_limit_exceeded = "queue_limit_exceeded";
pub const code_undisclosed_queue_limit = "undisclosed_queue_limit";
pub const code_unavailable_capability = "unavailable_capability";
pub const code_stale_capability_revision = "stale_capability_revision";
pub const code_degraded_without_optin = "degraded_without_optin";
pub const code_unsatisfiable_control = "unsatisfiable_control";
pub const code_unapplied_control = "unapplied_control";
pub const code_duplicate_tool_name = "duplicate_tool_name";
pub const code_model_not_in_catalog = "model_not_in_catalog";
pub const code_duplicate_model_id = "duplicate_model_id";
pub const code_ambiguous_default_model = "ambiguous_default_model";
pub const code_unannounced_catalog_change = "unannounced_catalog_change";

pub const implemented = [_][]const u8{
    code_duplicate_envelope_id,
    code_illegal_run_transition,
    code_missing_run_started,
    code_missing_run_terminal,
    code_duplicate_run_terminal,
    code_event_after_terminal,
    code_sequence_gap,
    code_sequence_regression,
    code_cancel_not_settled,
    code_queue_order_violation,
    code_queue_limit_exceeded,
    code_undisclosed_queue_limit,
    code_unavailable_capability,
    code_stale_capability_revision,
    code_degraded_without_optin,
    code_unsatisfiable_control,
    code_unapplied_control,
    code_duplicate_tool_name,
    code_model_not_in_catalog,
    code_duplicate_model_id,
    code_ambiguous_default_model,
    code_unannounced_catalog_change,
};

pub fn isImplemented(code: []const u8) bool {
    for (implemented) |name| {
        if (std.mem.eql(u8, name, code)) return true;
    }
    return false;
}

pub const Diagnostic = struct {
    code: []const u8,
    index: usize,
};

const run_event_types = [_][]const u8{
    "run.started",                 "run.status.updated",         "content.delta",
    "run.completed",               "run.failed",                 "run.cancelled",
    "action.call.requested",       "action.call.started",        "action.call.progress",
    "action.call.completed",       "action.call.failed",         "action.call.cancelled",
    "action.permission.requested", "action.permission.resolved", "user.input.requested",
    "user.input.resolved",
};

fn isRunEvent(declared: []const u8) bool {
    for (run_event_types) |name| {
        if (std.mem.eql(u8, name, declared)) return true;
    }
    return false;
}

fn isTerminal(declared: []const u8) bool {
    return std.mem.eql(u8, declared, "run.completed") or
        std.mem.eql(u8, declared, "run.failed") or
        std.mem.eql(u8, declared, "run.cancelled");
}

fn preStartSettlement(declared: []const u8) bool {
    return std.mem.eql(u8, declared, "run.failed") or std.mem.eql(u8, declared, "run.cancelled");
}

fn legalRunStatusTransition(from: []const u8, to: []const u8) bool {
    if (std.mem.eql(u8, from, "queued")) {
        return std.mem.eql(u8, to, "running") or std.mem.eql(u8, to, "cancelling");
    }
    if (std.mem.eql(u8, from, "running") or std.mem.eql(u8, from, "waiting_for_input")) {
        return std.mem.eql(u8, to, "running") or std.mem.eql(u8, to, "waiting_for_input") or
            std.mem.eql(u8, to, "cancelling");
    }
    if (std.mem.eql(u8, from, "cancelling")) return std.mem.eql(u8, to, "cancelling");
    return false;
}

fn namesShape(value: []const u8, started: []const u8, queued: []const u8) []const u8 {
    if (std.mem.eql(u8, value, started)) return "started";
    if (std.mem.eql(u8, value, queued)) return "queued";
    return "";
}

fn admissionShape(admission: []const u8, delivery: []const u8, status: []const u8) bool {
    const votes = [_][]const u8{
        namesShape(admission, "started", "queued"),
        namesShape(delivery, "start", "queue"),
        namesShape(status, "running", "queued"),
    };
    var named: []const u8 = "";
    for (votes) |shape| {
        if (shape.len == 0) return false;
        if (named.len != 0 and !std.mem.eql(u8, named, shape)) return false;
        named = shape;
    }
    return true;
}

fn field(envelope: std.json.Value, name: []const u8) []const u8 {
    if (envelope != .object) return "";
    const value = envelope.object.get(name) orelse return "";
    if (value != .string) return "";
    return value.string;
}

fn unsigned(envelope: std.json.Value, name: []const u8) ?u64 {
    if (envelope != .object) return null;
    const value = envelope.object.get(name) orelse return null;
    return switch (value) {
        .integer => |n| if (n < 0) null else @intCast(n),
        else => null,
    };
}

fn member(container: std.json.Value, name: []const u8) ?std.json.Value {
    if (container != .object) return null;
    return container.object.get(name);
}

fn memberString(container: std.json.Value, name: []const u8) []const u8 {
    const value = member(container, name) orelse return "";
    if (value != .string) return "";
    return value.string;
}

fn memberBool(container: std.json.Value, name: []const u8) bool {
    const value = member(container, name) orelse return false;
    if (value != .bool) return false;
    return value.bool;
}

const Run = struct {
    id: []const u8,
    session: []const u8,
    admitted: bool = false,
    started: bool = false,
    terminal: bool = false,
    terminal_type: []const u8 = "",
    next: u64 = 1,
    last_index: usize = 0,
    cancel_accepted: bool = false,
    recovered: bool = false,
    admitted_queued: bool = false,
    order: usize = 0,
    admitted_model: []const u8 = "",
    controls: Controls = .{},
    deferred_controls: bool = false,
    status: []const u8 = "",
};

const Unjudged = struct {
    model: []const u8,
    revision: []const u8,
    admitted: bool,
    index: usize,
    refusal: std.json.Value,
};

const Session = struct {
    active: []const u8 = "",
    provided: std.ArrayList([]const u8) = .empty,
    unjudged: std.ArrayList(Unjudged) = .empty,
    order: std.ArrayList([]const u8) = .empty,
    current_model: []const u8 = "",
    current_known: bool = false,
    expected_default: []const u8 = "",
    guard_default: bool = false,
};

const Window = struct {
    request: []const u8,
    session: []const u8,
    delivery: []const u8,
    offered: bool = false,
    busy_at_request: bool = false,
    busy_ever: bool = false,
    started_ever: bool = false,
    reached_strict: bool = false,
    reached_loose: bool = false,
    max_active: ?i64 = null,
    max_queued: ?i64 = null,
    mutation: bool = false,
    closed: bool = false,
};

const rung_capability: u8 = 1;
const rung_degradation: u8 = 2;
const rung_unsatisfiable: u8 = 3;
const unnamed_defect = "";

const Expectation = struct {
    rung: u8,
    key: []const u8,
    pointer: []const u8,
    code: []const u8,
    reason: []const u8 = "",
    detail_name: []const u8 = "",
    detail_value: []const u8 = "",
    diagnostic: []const u8,
};

const Controls = struct {
    present: bool = false,
    model_present: bool = false,
    model: []const u8 = "",
    mode: []const u8 = "",
    schema: ?std.json.Value = null,
    fixed_result: ?std.json.Value = null,
    choice: ?ToolChoice = null,
    catalog: []const []const u8 = &.{},
    catalog_known: bool = false,
    calls: std.StringArrayHashMapUnmanaged(void) = .empty,
};

const Pending = struct {
    control: ?Expectation = null,
    attachment: ?Expectation = null,
    subscribe: ?Expectation = null,
    fired: bool = false,
    provided: []const []const u8 = &.{},
    model_listed: bool = false,
    limit_refusal: ?Expectation = null,
    model_query: bool = false,
    satisfies: bool = false,
    model_unjudged: bool = false,
    revision: []const u8 = "",
    satisfiable: std.StringArrayHashMapUnmanaged(void) = .empty,
    controls: Controls = .{},
    session: []const u8 = "",
};

const ToolChoice = struct {
    mode: []const u8 = "",
    name: []const u8 = "",
    allowed: []const []const u8 = &.{},
    disallowed: []const []const u8 = &.{},
    has_allowed: bool = false,
};

const ModelCatalog = struct {
    revision: []const u8 = "",
    known: bool = false,
    binding: bool = false,
    models: ?std.json.Value = null,
    ids: std.StringArrayHashMapUnmanaged(void) = .empty,

    fn binds(self: *const ModelCatalog, revision: []const u8) bool {
        return self.known and self.binding and revision.len != 0 and
            std.mem.eql(u8, self.revision, revision);
    }
};

const Limits = struct {
    max_active: ?i64 = null,
    max_queued: ?i64 = null,
};

const Snapshot = struct {
    revision: []const u8,
    stale: bool,
    models: []const u8,
    queue: []const u8,
    limits: ?Limits,
};

const Recovery = struct {
    run: []const u8 = "",
    gap: bool = false,
    cursor: u64 = 0,
    cursor_set: bool = false,
    state_checked: bool = false,
    first_replay_seen: bool = false,
};

const Request = struct {
    message: ?std.json.Value = null,
    payload: std.json.Value = .{ .null = {} },
    declared: []const u8,
    revision: []const u8 = "",
    carries_message: bool = false,
    responded: bool = false,
    gates: []const Gate = &.{},
};

pub const PackedType = struct {
    name: []const u8,
    role: []const u8,
    capability: []const u8 = "",
    response: []const u8 = "",
    refusals: []const []const u8 = &.{},
};

pub const PackedMember = struct {
    payload_type: []const u8,
    name: []const u8,
    capability: []const u8 = "",
};

pub const Packs = struct {
    types: []const PackedType = &.{},
    members: []const PackedMember = &.{},

    fn declaredType(self: Packs, name: []const u8) ?PackedType {
        for (self.types) |held| {
            if (std.mem.eql(u8, held.name, name)) return held;
        }
        return null;
    }
};

const Gate = struct {
    key: []const u8,
    refusals: []const []const u8 = &.{},
    typed: bool = false,
    advertised: bool = false,
};

pub const Machine = struct {
    allocator: std.mem.Allocator,
    arena: std.heap.ArenaAllocator,
    diagnostics: std.ArrayList(Diagnostic) = .empty,
    ids: std.StringArrayHashMapUnmanaged(usize) = .empty,
    runs: std.StringArrayHashMapUnmanaged(*Run) = .empty,
    sessions: std.StringArrayHashMapUnmanaged(*Session) = .empty,
    recoveries: std.StringArrayHashMapUnmanaged(*Recovery) = .empty,
    requests: std.StringArrayHashMapUnmanaged(Request) = .empty,
    packs: Packs = .{},
    features: std.StringArrayHashMapUnmanaged([]const u8) = .empty,
    modes: std.StringArrayHashMapUnmanaged([]const u8) = .empty,
    supports: std.StringArrayHashMapUnmanaged(std.json.Value) = .empty,
    catalog: std.ArrayList([]const u8) = .empty,
    declared_sources: std.StringArrayHashMapUnmanaged(void) = .empty,
    control_participant: []const u8 = "",
    catalog_known: bool = false,
    catalog_ambiguous: bool = false,
    catalogs: std.StringArrayHashMapUnmanaged(*ModelCatalog) = .empty,
    current_capability: []const u8 = "",
    capabilities_stale: bool = false,
    limits: ?Limits = null,
    windows: std.StringArrayHashMapUnmanaged(*Window) = .empty,
    submits: std.StringArrayHashMapUnmanaged(*Pending) = .empty,

    pub fn init(allocator: std.mem.Allocator) Machine {
        return .{ .allocator = allocator, .arena = std.heap.ArenaAllocator.init(allocator) };
    }

    pub fn deinit(self: *Machine) void {
        self.diagnostics.deinit(self.allocator);
        self.ids.deinit(self.allocator);
        self.runs.deinit(self.allocator);
        self.recoveries.deinit(self.allocator);
        self.requests.deinit(self.allocator);
        self.features.deinit(self.allocator);
        self.modes.deinit(self.allocator);
        self.supports.deinit(self.allocator);
        self.catalog.deinit(self.allocator);
        self.declared_sources.deinit(self.allocator);
        self.catalogs.deinit(self.allocator);
        self.windows.deinit(self.allocator);
        self.submits.deinit(self.allocator);
        for (self.sessions.values()) |holder| {
            holder.order.deinit(self.allocator);
            holder.provided.deinit(self.allocator);
            holder.unjudged.deinit(self.allocator);
        }
        self.sessions.deinit(self.allocator);
        self.arena.deinit();
    }

    fn add(self: *Machine, code: []const u8, index: usize) !void {
        try self.diagnostics.append(self.allocator, .{ .code = code, .index = index });
    }

    fn sessionFor(self: *Machine, session: []const u8) !*Session {
        if (self.sessions.get(session)) |existing| return existing;
        const created = try self.arena.allocator().create(Session);
        created.* = .{};
        try self.sessions.put(self.allocator, session, created);
        return created;
    }

    pub fn apply(self: *Machine, index: usize, envelope: std.json.Value) !void {
        const id = field(envelope, "id");
        if (id.len != 0) {
            if (self.ids.get(id) != null) {
                try self.add(code_duplicate_envelope_id, index);
                return;
            }
            try self.ids.put(self.allocator, id, index);
        }

        const declared = field(envelope, "type");
        const payload = member(envelope, "payload") orelse std.json.Value{ .null = {} };

        if (self.isRequestType(declared)) {
            try self.requests.put(self.allocator, id, .{
                .declared = declared,
                .payload = payload,
                .revision = field(envelope, "capability_revision"),
                .message = member(payload, "message"),
                .carries_message = member(payload, "message") != null,
            });
        }
        var duplicate = false;
        if (self.isResponseType(declared)) {
            duplicate = try self.correlate(index, envelope, declared);
        }
        const revision = field(envelope, "capability_revision");
        if (revision.len != 0 and self.current_capability.len != 0 and
            !std.mem.eql(u8, revision, self.current_capability) and
            !exemptFromRevision(declared))
        {
            try self.add(code_stale_capability_revision, index);
        }
        if (duplicate) return;

        try self.dispatch(index, envelope, declared, payload);
        try self.packEnvelope(index, envelope, declared, payload);
    }

    fn isRequestType(self: *const Machine, declared: []const u8) bool {
        if (self.packs.declaredType(declared)) |held| return std.mem.eql(u8, held.role, "request");
        return std.mem.endsWith(u8, declared, ".request");
    }

    fn isResponseType(self: *const Machine, declared: []const u8) bool {
        if (self.packs.declaredType(declared)) |held| return std.mem.eql(u8, held.role, "response");
        return std.mem.endsWith(u8, declared, ".response");
    }

    fn answers(self: *const Machine, asked: []const u8, declared: []const u8) bool {
        if (self.packs.declaredType(asked)) |held| {
            if (held.response.len == 0) return false;
            return std.mem.eql(u8, declared, held.response);
        }
        const suffix = ".request";
        if (!std.mem.endsWith(u8, asked, suffix)) return false;
        const stem = asked[0 .. asked.len - suffix.len];
        if (!std.mem.startsWith(u8, declared, stem)) return false;
        return std.mem.eql(u8, declared[stem.len..], ".response");
    }

    fn advertisedKey(self: *const Machine, key: []const u8) bool {
        if (self.current_capability.len == 0 or self.capabilities_stale) return false;
        return affirmative(self.features.get(key) orelse "");
    }

    fn memberGates(self: *Machine, declared: []const u8, payload: std.json.Value, out: *std.ArrayList(Gate)) !void {
        if (payload != .object) return;
        for (self.packs.members) |held| {
            if (held.capability.len == 0) continue;
            if (!std.mem.eql(u8, held.payload_type, declared)) continue;
            if (payload.object.get(held.name) == null) continue;
            try out.append(self.arena.allocator(), .{ .key = held.capability });
        }
    }

    fn packEnvelope(self: *Machine, index: usize, envelope: std.json.Value, declared: []const u8, payload: std.json.Value) !void {
        if (self.packs.types.len == 0 and self.packs.members.len == 0) return;
        const packed_type = self.packs.declaredType(declared);
        const role = if (packed_type) |held| held.role else if (std.mem.endsWith(u8, declared, ".request"))
            "request"
        else if (std.mem.endsWith(u8, declared, ".response"))
            "response"
        else
            "event";

        if (packed_type) |held| {
            if (std.mem.eql(u8, role, "event") and held.capability.len != 0) {
                try self.featureKeys(index, envelope, &.{held.capability});
            }
        }

        var gates = std.ArrayList(Gate).empty;
        if (!std.mem.eql(u8, role, "request")) {
            try self.memberGates(declared, payload, &gates);
            for (gates.items) |gate| try self.featureKeys(index, envelope, &.{gate.key});
            gates.clearRetainingCapacity();
        }

        if (std.mem.eql(u8, role, "request")) {
            if (packed_type) |held| {
                if (held.capability.len != 0) {
                    try gates.append(self.arena.allocator(), .{
                        .key = held.capability,
                        .refusals = held.refusals,
                        .typed = true,
                    });
                }
            }
            try self.memberGates(declared, payload, &gates);
            for (gates.items) |*gate| gate.advertised = self.advertisedKey(gate.key);
            if (self.requests.getPtr(field(envelope, "id"))) |asked| {
                asked.gates = try gates.toOwnedSlice(self.arena.allocator());
            }
            return;
        }

        const failure = std.mem.eql(u8, declared, "error.response");
        if (!std.mem.eql(u8, role, "response") and !failure) return;
        const asked = self.requests.get(field(envelope, "in_reply_to")) orelse return;
        if (failure) {
            try self.settlePackRefusal(index, payload, asked.gates);
            return;
        }
        for (asked.gates) |gate| {
            if (!gate.advertised) try self.add(code_unavailable_capability, index);
        }
    }

    fn settlePackRefusal(self: *Machine, index: usize, payload: std.json.Value, gates: []const Gate) !void {
        const raised = member(payload, "error") orelse std.json.Value{ .null = {} };
        var unadvertised = false;
        var named_by_refusal = false;
        const details = member(raised, "details") orelse std.json.Value{ .null = {} };
        const named = memberString(details, "feature");
        for (gates) |gate| {
            if (gate.advertised) continue;
            unadvertised = true;
            if (std.mem.eql(u8, gate.key, named)) named_by_refusal = true;
        }
        if (!unadvertised) return;
        if (std.mem.eql(u8, memberString(raised, "code"), error_unsupported_feature) and
            std.mem.eql(u8, memberString(details, "reason"), reason_unadvertised) and
            named_by_refusal) return;
        try self.add(code_unavailable_capability, index);
    }

    fn dispatch(self: *Machine, index: usize, envelope: std.json.Value, declared: []const u8, payload: std.json.Value) !void {
        if (std.mem.eql(u8, declared, "protocol.initialize.request")) {
            if (member(payload, "participant")) |who| {
                const named = memberString(who, "id");
                if (named.len != 0) self.control_participant = named;
            }
            return;
        }
        if (std.mem.eql(u8, declared, "capabilities.response")) {
            try self.capabilitiesResponse(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "capabilities.updated")) {
            const previous = memberString(payload, "previous_revision");
            if (self.current_capability.len != 0 and !std.mem.eql(u8, previous, self.current_capability)) {
                try self.add(code_stale_capability_revision, index);
            }
            if (std.mem.eql(u8, field(envelope, "capability_revision"), previous)) {
                try self.add(code_stale_capability_revision, index);
            }
            self.current_capability = field(envelope, "capability_revision");
            self.capabilities_stale = true;
            self.limits = null;
            return;
        }
        if (std.mem.eql(u8, declared, "session.message.submit.request")) {
            if (self.capabilities_stale) try self.add(code_stale_capability_revision, index);
            try self.openSubmitWindow(index, envelope, payload, "", true);
            return;
        }
        if (std.mem.eql(u8, declared, "models.request")) {
            try self.modelsRequest(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "models.response")) {
            try self.modelsResponse(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "session.open.request")) {
            if (member(payload, "message")) |message| {
                try self.openSubmitWindow(index, envelope, message, memberString(payload, "session_id"), false);
            }
            try self.sessionOpenRequest(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "action.tools.list.request")) {
            try self.gatedRequest(index, envelope, payload, feature_tools_list, "/payload");
            return;
        }
        if (std.mem.eql(u8, declared, "action.tools.list.response")) {
            try self.gatedResponse(index, envelope, .control);
            try self.duplicateNames(index, member(payload, "tools"));
            return;
        }
        if (std.mem.eql(u8, declared, "action.call.resolve.request") or
            std.mem.eql(u8, declared, "action.call.resolve.response"))
        {
            try self.feature(index, envelope, "tools");
            return;
        }
        if (interactionFeature(declared)) |name| {
            try self.feature(index, envelope, name);
            return;
        }
        if (std.mem.eql(u8, declared, "error.response")) {
            if (!try self.settleControlRefusal(index, envelope, payload)) {
                try self.settleQueueRefusal(index, envelope, payload);
            }
            try self.closeSubmitWindow(field(envelope, "in_reply_to"));
            return;
        }
        if (std.mem.eql(u8, declared, "session.message.submit.response")) {
            try self.admit(index, envelope, payload);
            try self.closeSubmitWindow(field(envelope, "in_reply_to"));
            return;
        }
        if (std.mem.eql(u8, declared, "session.open.response")) {
            try self.openResponse(index, envelope, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "session.state.response") or
            std.mem.eql(u8, declared, "session.state.updated"))
        {
            try self.stateDocument(index, payload);
            return;
        }
        if (std.mem.eql(u8, declared, "run.cancel.response")) {
            try self.cancelResponse(index, payload);
            return;
        }
        if (isRunEvent(declared)) {
            try self.runEvent(index, envelope, declared);
        }
    }

    fn correlate(self: *Machine, index: usize, envelope: std.json.Value, declared: []const u8) !bool {
        const request = self.requests.getPtr(field(envelope, "in_reply_to")) orelse return false;
        const failure = std.mem.eql(u8, declared, "error.response");
        if (!failure and !self.answers(request.declared, declared)) return false;
        if (request.responded) return true;
        request.responded = true;
        if (!failure and
            !std.mem.eql(u8, request.declared, "protocol.initialize.request") and
            !std.mem.eql(u8, request.declared, "capabilities.request") and
            request.revision.len != 0 and
            !std.mem.eql(u8, field(envelope, "capability_revision"), request.revision))
        {
            try self.add(code_stale_capability_revision, index);
        }
        return false;
    }

    fn capabilitiesResponse(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const outgoing = Snapshot{
            .revision = self.current_capability,
            .stale = self.capabilities_stale,
            .models = self.features.get(feature_models_list) orelse "",
            .queue = self.features.get(feature_delivery_queue) orelse "",
            .limits = self.limits,
        };
        self.current_capability = field(envelope, "capability_revision");
        self.capabilities_stale = false;
        self.features.clearRetainingCapacity();
        self.modes.clearRetainingCapacity();
        self.supports.clearRetainingCapacity();
        self.catalog.clearRetainingCapacity();
        try self.collectFeatures(payload);
        try self.collectCatalog(payload);
        try self.collectSources(payload);
        self.catalog_known = true;
        self.limits = readLimits(member(payload, "limits"));
        try self.checkQueueLimits(index);
        try self.checkAdvertisement(index, outgoing);
        self.catalog_ambiguous = duplicateToolName(self.catalog.items) != null;
        if (self.catalog_ambiguous) try self.add(code_duplicate_tool_name, index);
        try self.checkRefreshAgainstProvided(index);
    }

    fn collectFeatures(self: *Machine, payload: std.json.Value) !void {
        if (member(payload, "layers")) |layers| {
            if (layers == .object) {
                var names = std.ArrayList([]const u8).empty;
                defer names.deinit(self.allocator);
                var layer = layers.object.iterator();
                while (layer.next()) |entry| try names.append(self.allocator, entry.key_ptr.*);
                std.mem.sort([]const u8, names.items, {}, lessThanName);
                var at = names.items.len;
                while (at > 0) {
                    at -= 1;
                    try self.absorbFeatures(member(layers.object.get(names.items[at]).?, "features"));
                }
            }
        }
        try self.absorbFeatures(member(payload, "features"));
    }

    fn absorbFeatures(self: *Machine, declared: ?std.json.Value) !void {
        const features = declared orelse return;
        if (features != .object) return;
        var it = features.object.iterator();
        while (it.next()) |entry| {
            const level = memberString(entry.value_ptr.*, "level");
            if (level.len == 0) continue;
            try self.features.put(self.allocator, entry.key_ptr.*, level);
            try self.modes.put(self.allocator, entry.key_ptr.*, memberString(entry.value_ptr.*, "mode"));
            try self.supports.put(self.allocator, entry.key_ptr.*, entry.value_ptr.*);
        }
    }

    fn gatedRequest(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, key: []const u8, pointer: []const u8) !void {
        const pending = try self.arena.allocator().create(Pending);
        pending.* = .{ .session = memberString(payload, "session_id") };
        defer self.submits.put(self.allocator, field(envelope, "id"), pending) catch {};
        const level = try self.controlDescriptor(index, envelope, key) orelse return;
        if (!affirmative(level)) {
            pending.control = .{
                .rung = rung_capability,
                .key = key,
                .pointer = pointer,
                .code = error_unsupported_feature,
                .reason = reason_unadvertised,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_unavailable_capability,
            };
            return;
        }
        if (std.mem.eql(u8, level, "degraded") and !allowsDegraded(payload, key)) {
            pending.control = .{
                .rung = rung_degradation,
                .key = key,
                .pointer = pointer,
                .code = error_capability_degraded,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_degraded_without_optin,
            };
            return;
        }
    }

    fn disclosesMode(self: *const Machine, key: []const u8, mode: []const u8) bool {
        return self.disclosedMode(key, mode);
    }

    fn subscribeGate(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, pending: *Pending) !void {
        if (!memberBool(payload, "subscribe")) return;
        const key = feature_open_subscribe;
        const level = try self.controlDescriptor(index, envelope, key) orelse return;
        if (!affirmative(level)) {
            self.propose(&pending.subscribe, .{
                .rung = rung_capability,
                .key = key,
                .pointer = "/payload/subscribe",
                .code = error_unsupported_feature,
                .reason = reason_unadvertised,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_unavailable_capability,
            });
            return;
        }
        if (std.mem.eql(u8, level, "degraded") and !allowsDegraded(payload, key)) {
            self.propose(&pending.subscribe, .{
                .rung = rung_degradation,
                .key = key,
                .pointer = "/payload/subscribe",
                .code = error_capability_degraded,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_degraded_without_optin,
            });
        }
    }

    fn sessionOpenRequest(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const sources = member(payload, "tool_sources");
        const tools = member(payload, "tools");
        const attaching = sources != null and sources.? == .array and sources.?.array.items.len > 0;
        const providing = tools != null and tools.? == .array and tools.?.array.items.len > 0;

        const pending = try self.arena.allocator().create(Pending);
        pending.* = .{ .session = memberString(payload, "session_id") };
        defer self.submits.put(self.allocator, field(envelope, "id"), pending) catch {};

        try self.subscribeGate(index, envelope, payload, pending);
        if (providing) {
            const names = try self.arena.allocator().alloc([]const u8, tools.?.array.items.len);
            for (tools.?.array.items, 0..) |tool, at| names[at] = memberString(tool, "name");
            pending.provided = names;
        }
        if (member(payload, "message")) |message| {
            try self.submitControls(index, envelope, message, pending);
            try self.deliveryExpectation(index, envelope, message, pending);
        }
        if (!attaching and !providing) return;
        _ = try self.controlDescriptor(index, envelope, feature_tool_sources_attach) orelse return;
        if (attaching) try self.attachExpectations(payload, sources.?, pending);
        if (providing) try self.provideExpectations(payload, tools.?, sources, pending);
        if (pending.attachment != null) pending.limit_refusal = null;
    }

    fn attachExpectations(self: *Machine, payload: std.json.Value, sources: std.json.Value, pending: *Pending) !void {
        const key = feature_tool_sources_attach;
        const level = self.features.get(key) orelse "";
        if (!affirmative(level) or !self.disclosesMode(key, "session_open")) {
            self.propose(&pending.attachment, .{
                .rung = rung_capability,
                .key = key,
                .pointer = "/payload/tool_sources",
                .code = error_unsupported_feature,
                .reason = reason_unadvertised,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_unavailable_capability,
            });
            return;
        }
        if (std.mem.eql(u8, level, "degraded") and !allowsDegraded(payload, key)) {
            self.propose(&pending.attachment, .{
                .rung = rung_degradation,
                .key = key,
                .pointer = "/payload/tool_sources",
                .code = error_capability_degraded,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_degraded_without_optin,
            });
            return;
        }
        var defective = false;
        var seen = std.ArrayList([]const u8).empty;
        defer seen.deinit(self.allocator);
        for (sources.array.items, 0..) |attachment, at| {
            const id = memberString(attachment, "id");
            if (listedIn(seen.items, id) or self.declared_sources.get(id) != null) {
                defective = true;
                self.propose(&pending.attachment, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tool_sources", at, "/id"),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "source",
                    .detail_value = id,
                    .diagnostic = unnamed_defect,
                });
            }
            try seen.append(self.allocator, id);
            if (std.mem.eql(u8, memberString(attachment, "kind"), "remote") and
                !self.disclosesMode(key, "remote"))
            {
                defective = true;
                self.propose(&pending.attachment, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tool_sources", at, "/kind"),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "source",
                    .detail_value = memberString(attachment, "id"),
                    .diagnostic = code_unavailable_capability,
                });
            }
        }
        if (defective) return;
        try self.attachLimitViolation(key, sources, pending);
    }

    fn attachLimitViolation(self: *Machine, key: []const u8, sources: std.json.Value, pending: *Pending) !void {
        const support = self.supports.get(key) orelse return;
        const limits = member(support, "limits") orelse return;
        if (member(limits, "max_sources")) |declared| {
            if (declared == .integer and declared.integer >= 1 and
                sources.array.items.len > @as(usize, @intCast(declared.integer)))
            {
                const at: usize = @intCast(declared.integer);
                self.propose(&pending.limit_refusal, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tool_sources", at, ""),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "source",
                    .detail_value = memberString(sources.array.items[at], "id"),
                    .diagnostic = code_unavailable_capability,
                });
            }
        }
        const transports = member(limits, "transports") orelse return;
        if (transports != .array) return;
        var bounded = false;
        for (transports.array.items) |entry| {
            if (entry == .string and isToolSourceKind(entry.string)) bounded = true;
        }
        if (!bounded) return;
        for (sources.array.items, 0..) |attachment, at| {
            const kind = memberString(attachment, "kind");
            var disclosed = false;
            for (transports.array.items) |entry| {
                if (entry != .string or !isToolSourceKind(entry.string)) continue;
                if (std.mem.eql(u8, entry.string, kind)) disclosed = true;
            }
            if (disclosed) continue;
            self.propose(&pending.limit_refusal, .{
                .rung = rung_unsatisfiable,
                .key = key,
                .pointer = try self.pointerAt("/payload/tool_sources", at, "/kind"),
                .code = error_unsupported_feature,
                .reason = reason_unsatisfiable,
                .detail_name = "source",
                .detail_value = memberString(attachment, "id"),
                .diagnostic = code_unavailable_capability,
            });
        }
    }

    fn pointerAt(self: *Machine, base: []const u8, at: usize, suffix: []const u8) ![]const u8 {
        return std.fmt.allocPrint(self.arena.allocator(), "{s}/{d}{s}", .{ base, at, suffix });
    }

    fn provideExpectations(self: *Machine, payload: std.json.Value, tools: std.json.Value, sources: ?std.json.Value, pending: *Pending) !void {
        const key = feature_tools_provide;
        const level = self.features.get(key) orelse "";
        if (!affirmative(level)) {
            self.propose(&pending.attachment, .{
                .rung = rung_capability,
                .key = key,
                .pointer = "/payload/tools",
                .code = error_unsupported_feature,
                .reason = reason_unadvertised,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_unavailable_capability,
            });
            return;
        }
        if (std.mem.eql(u8, level, "degraded") and !allowsDegraded(payload, key)) {
            self.propose(&pending.attachment, .{
                .rung = rung_degradation,
                .key = key,
                .pointer = "/payload/tools",
                .code = error_capability_degraded,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_degraded_without_optin,
            });
            return;
        }
        var attached = std.ArrayList([]const u8).empty;
        defer attached.deinit(self.allocator);
        if (sources) |listed| {
            if (listed == .array) {
                for (listed.array.items) |attachment| {
                    try attached.append(self.allocator, memberString(attachment, "id"));
                }
            }
        }
        for (tools.array.items, 0..) |tool, at| {
            const owner = memberString(tool, "execution_owner");
            if (self.control_participant.len != 0 and owner.len != 0 and
                !std.mem.eql(u8, owner, self.control_participant))
            {
                self.propose(&pending.attachment, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tools", at, "/execution_owner"),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "tool",
                    .detail_value = memberString(tool, "name"),
                    .diagnostic = unnamed_defect,
                });
            }
            const source = memberString(tool, "source");
            if (source.len != 0 and self.declared_sources.get(source) == null and
                !listedIn(attached.items, source))
            {
                self.propose(&pending.attachment, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tools", at, "/source"),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "source",
                    .detail_value = source,
                    .diagnostic = unnamed_defect,
                });
            }
        }
        try self.provideLimitViolation(key, tools, pending);
        var seen = std.ArrayList([]const u8).empty;
        defer seen.deinit(self.allocator);
        for (tools.array.items, 0..) |tool, at| {
            const name = memberString(tool, "name");
            if (listedIn(seen.items, name) or (self.catalog_known and listedIn(self.catalog.items, name))) {
                self.propose(&pending.attachment, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tools", at, "/name"),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "tool",
                    .detail_value = name,
                    .diagnostic = code_duplicate_tool_name,
                });
            }
            try seen.append(self.allocator, name);
        }
    }

    fn provideLimitViolation(self: *Machine, key: []const u8, tools: std.json.Value, pending: *Pending) !void {
        const support = self.supports.get(key) orelse return;
        const limits = member(support, "limits") orelse return;
        if (member(limits, "max_tools")) |declared| {
            if (declared == .integer and declared.integer >= 1 and
                tools.array.items.len > @as(usize, @intCast(declared.integer)))
            {
                const at: usize = @intCast(declared.integer);
                self.propose(&pending.limit_refusal, .{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = try self.pointerAt("/payload/tools", at, ""),
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = "tool",
                    .detail_value = memberString(tools.array.items[at], "name"),
                    .diagnostic = code_unavailable_capability,
                });
            }
        }
        const dialect = memberString(limits, "schema_dialect");
        if (dialect.len == 0) return;
        for (tools.array.items, 0..) |tool, at| {
            const declared = member(tool, "input_schema") orelse continue;
            const stated = memberString(declared, "$schema");
            if (stated.len == 0 or std.mem.eql(u8, stated, dialect)) continue;
            self.propose(&pending.limit_refusal, .{
                .rung = rung_unsatisfiable,
                .key = key,
                .pointer = try self.pointerAt("/payload/tools", at, "/input_schema"),
                .code = error_unsupported_feature,
                .reason = reason_unsatisfiable,
                .detail_name = "tool",
                .detail_value = memberString(tool, "name"),
                .diagnostic = code_unavailable_capability,
            });
        }
    }

    const Surface = enum { control, open };

    fn gatedResponse(self: *Machine, index: usize, envelope: std.json.Value, surface: Surface) !void {
        const pending = self.submits.get(field(envelope, "in_reply_to")) orelse return;
        switch (surface) {
            .control => {
                if (pending.control) |expectation| {
                    if (pending.fired) return;
                    pending.fired = true;
                    try self.raise(expectation, index);
                }
            },
            .open => {
                if (pending.attachment) |expectation| try self.raise(expectation, index);
                if (pending.subscribe) |expectation| try self.raise(expectation, index);
            },
        }
    }

    fn modelsRequest(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        try self.gatedRequest(index, envelope, payload, feature_models_list, "/payload");
        if (self.submits.get(field(envelope, "id"))) |query| query.model_query = true;
    }

    fn modelsResponse(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        try self.featureKeys(index, envelope, &.{feature_models_list});
        if (self.submits.get(field(envelope, "in_reply_to"))) |query| {
            if (query.control) |expectation| {
                if (expectation.rung == rung_degradation) try self.add(code_degraded_without_optin, index);
            }
        }

        const listed = member(payload, "models") orelse std.json.Value{ .null = {} };
        var ids: std.StringArrayHashMapUnmanaged(void) = .empty;
        var defaults: usize = 0;
        var duplicate = false;
        if (listed == .array) {
            for (listed.array.items) |model| {
                const id = memberString(model, "id");
                if (ids.get(id) != null) duplicate = true;
                try ids.put(self.arena.allocator(), id, {});
                if (memberBool(model, "default")) defaults += 1;
            }
        }
        if (duplicate) try self.add(code_duplicate_model_id, index);
        if (defaults > 1) try self.add(code_ambiguous_default_model, index);

        const current = memberString(payload, "current_model_id");
        if (current.len != 0 and ids.get(current) == null) {
            try self.add(code_model_not_in_catalog, index);
        }

        const revision = field(envelope, "capability_revision");
        const level = self.features.get(feature_models_list) orelse "";
        const binding = std.mem.eql(u8, level, "native") or std.mem.eql(u8, level, "emulated");
        if (revision.len == 0 or !std.mem.eql(u8, revision, self.current_capability) or self.capabilities_stale) {
            return;
        }
        const session_id = memberString(payload, "session_id");
        const served = try self.arena.allocator().create(ModelCatalog);
        served.* = .{ .revision = revision, .known = true, .binding = binding, .models = listed, .ids = ids };
        if (binding) {
            if (self.catalogs.get(session_id)) |held| {
                if (held.known and held.binding and std.mem.eql(u8, held.revision, revision) and
                    !sameCatalog(held.models, served.models))
                {
                    try self.add(code_unannounced_catalog_change, index);
                }
            }
        }
        try self.catalogs.put(self.allocator, session_id, served);
        try self.reconcileUnjudged(session_id, served);
    }

    fn reconcileUnjudged(self: *Machine, session_id: []const u8, served: *ModelCatalog) !void {
        if (!served.binding or served.revision.len == 0) return;
        const holder = try self.sessionFor(session_id);
        var kept: usize = 0;
        for (holder.unjudged.items) |entry| {
            if (!std.mem.eql(u8, entry.revision, served.revision)) {
                holder.unjudged.items[kept] = entry;
                kept += 1;
                continue;
            }
            const listed = served.ids.get(entry.model) != null;
            if (entry.admitted) {
                if (!listed) try self.add(code_model_not_in_catalog, entry.index);
            } else if (listed) {
                if (std.mem.eql(u8, memberString(entry.refusal, "code"), error_model_not_found)) {
                    try self.add(code_model_not_in_catalog, entry.index);
                }
            } else {
                const details = member(entry.refusal, "details") orelse std.json.Value{ .null = {} };
                const requested = memberString(details, "model_id");
                const named = std.mem.eql(u8, memberString(entry.refusal, "code"), error_model_not_found) and
                    std.mem.eql(u8, requested, entry.model);
                if (!named) try self.add(code_model_not_in_catalog, entry.index);
            }
        }
        holder.unjudged.shrinkRetainingCapacity(kept);
    }

    fn checkRefreshAgainstProvided(self: *Machine, index: usize) !void {
        for (self.sessions.values()) |holder| {
            for (holder.provided.items) |name| {
                if (listedIn(self.catalog.items, name)) try self.add(code_duplicate_tool_name, index);
            }
        }
    }

    fn checkAdvertisement(self: *Machine, index: usize, outgoing: Snapshot) !void {
        if (outgoing.revision.len == 0 or outgoing.stale or
            !std.mem.eql(u8, outgoing.revision, self.current_capability)) return;
        if (!std.mem.eql(u8, self.features.get(feature_models_list) orelse "", outgoing.models)) {
            try self.add(code_unannounced_catalog_change, index);
        }
        if (!std.mem.eql(u8, self.features.get(feature_delivery_queue) orelse "", outgoing.queue)) {
            try self.add(code_stale_capability_revision, index);
        }
        const before = outgoing.limits orelse Limits{};
        const after = self.limits orelse Limits{};
        if (!sameBound(before.max_active, after.max_active)) try self.add(code_stale_capability_revision, index);
        if (!sameBound(before.max_queued, after.max_queued)) try self.add(code_stale_capability_revision, index);
    }

    fn absorbSources(self: *Machine, declared: ?std.json.Value) !void {
        const sources = declared orelse return;
        if (sources != .array) return;
        for (sources.array.items) |source| {
            const id = memberString(source, "id");
            if (id.len != 0) try self.declared_sources.put(self.allocator, id, {});
        }
    }

    fn collectSources(self: *Machine, payload: std.json.Value) !void {
        self.declared_sources.clearRetainingCapacity();
        try self.absorbSources(member(payload, "sources"));
        if (member(payload, "layers")) |layers| {
            if (layers == .object) {
                var layer = layers.object.iterator();
                while (layer.next()) |entry| try self.absorbSources(member(entry.value_ptr.*, "sources"));
            }
        }
    }

    fn collectCatalog(self: *Machine, payload: std.json.Value) !void {
        try self.absorbTools(member(payload, "tools"));
        if (member(payload, "layers")) |layers| {
            if (layers == .object) {
                var names = std.ArrayList([]const u8).empty;
                defer names.deinit(self.allocator);
                var layer = layers.object.iterator();
                while (layer.next()) |entry| try names.append(self.allocator, entry.key_ptr.*);
                std.mem.sort([]const u8, names.items, {}, lessThanName);
                for (names.items) |name| {
                    try self.absorbTools(member(layers.object.get(name).?, "tools"));
                }
            }
        }
    }

    fn absorbTools(self: *Machine, declared: ?std.json.Value) !void {
        const tools = declared orelse return;
        if (tools != .array) return;
        for (tools.array.items) |tool| {
            try self.catalog.append(self.allocator, memberString(tool, "name"));
        }
    }

    fn disclosedMode(self: *const Machine, key: []const u8, mode: []const u8) bool {
        const support = self.supports.get(key) orelse return false;
        const modes = member(support, "modes") orelse return false;
        if (modes != .array) return false;
        for (modes.array.items) |entry| {
            if (entry == .string and std.mem.eql(u8, entry.string, mode)) return true;
        }
        return false;
    }

    fn checkQueueLimits(self: *Machine, index: usize) !void {
        if (!self.queueOffered()) return;
        const limits = self.limits orelse Limits{};
        const queued = limits.max_queued orelse 0;
        if (queued < 1) {
            try self.add(code_undisclosed_queue_limit, index);
            return;
        }
        if (limits.max_active) |active| {
            if (active < queued + 1) try self.add(code_undisclosed_queue_limit, index);
        }
    }

    fn advertisedLevel(self: *const Machine, key: []const u8) ?[]const u8 {
        if (self.current_capability.len == 0 or self.capabilities_stale) return null;
        return self.features.get(key) orelse "unavailable";
    }

    fn queueOffered(self: *const Machine) bool {
        const level = self.advertisedLevel(feature_delivery_queue) orelse return false;
        return affirmative(level);
    }

    const Counts = struct { active: usize = 0, queued: usize = 0, started: usize = 0 };

    fn queueCounts(self: *const Machine, session: []const u8) Counts {
        const holder = self.sessions.get(session) orelse return .{};
        var counts = Counts{};
        for (holder.order.items) |id| {
            const run = self.runs.get(id) orelse continue;
            if (run.terminal) continue;
            counts.active += 1;
            if (run.admitted_queued and !run.started) counts.queued += 1 else counts.started += 1;
        }
        return counts;
    }

    fn exceeds(window: *const Window, counts: Counts, outstanding: usize) bool {
        if (window.max_active) |bound| {
            if (@as(i64, @intCast(counts.active + outstanding + 1)) > bound) return true;
        }
        if (window.max_queued) |bound| {
            if (@as(i64, @intCast(counts.queued + outstanding + 1)) > bound) return true;
        }
        return false;
    }

    fn openWindows(self: *const Machine, session: []const u8, out: *std.ArrayList(*Window)) !void {
        for (self.windows.values()) |window| {
            if (window.closed) continue;
            if (!std.mem.eql(u8, window.session, session)) continue;
            try out.append(self.allocator, window);
        }
    }

    fn refreshQueueWindows(self: *Machine, session: []const u8) !void {
        var open = std.ArrayList(*Window).empty;
        defer open.deinit(self.allocator);
        try self.openWindows(session, &open);
        if (open.items.len == 0) return;
        const counts = self.queueCounts(session);

        var outstanding: usize = 0;
        for (open.items) |window| {
            if (!self.answered(window.request)) outstanding += 1;
        }
        for (open.items) |window| {
            if (counts.active > 0) window.busy_ever = true;
            if (counts.started > 0) window.started_ever = true;
            if (exceeds(window, counts, 0)) window.reached_strict = true;
            var others = outstanding;
            if (!self.answered(window.request)) others -= 1;
            if (exceeds(window, counts, others)) window.reached_loose = true;
        }
    }

    fn answered(self: *const Machine, request: []const u8) bool {
        const found = self.requests.get(request) orelse return false;
        return found.responded;
    }

    fn feature(self: *Machine, index: usize, envelope: std.json.Value, name: []const u8) !void {
        const allocator = self.arena.allocator();
        const keys = [_][]const u8{
            name,
            try std.fmt.allocPrint(allocator, "session.message.{s}", .{name}),
            try std.fmt.allocPrint(allocator, "agent_control.{s}", .{name}),
            try std.fmt.allocPrint(allocator, "action.{s}", .{name}),
        };
        try self.featureKeys(index, envelope, &keys);
    }

    fn featureKeys(self: *Machine, index: usize, envelope: std.json.Value, keys: []const []const u8) !void {
        if (self.current_capability.len == 0 or self.capabilities_stale) {
            try self.add(code_unavailable_capability, index);
            return;
        }
        if (!std.mem.eql(u8, field(envelope, "capability_revision"), self.current_capability)) {
            try self.add(code_stale_capability_revision, index);
            return;
        }
        for (keys) |key| {
            const level = self.features.get(key) orelse continue;
            if (!affirmative(level)) try self.add(code_unavailable_capability, index);
            return;
        }
        try self.add(code_unavailable_capability, index);
    }

    fn controlDescriptor(self: *Machine, index: usize, envelope: std.json.Value, key: []const u8) !?[]const u8 {
        if (self.current_capability.len == 0 or self.capabilities_stale) {
            try self.add(code_unavailable_capability, index);
            return null;
        }
        if (!std.mem.eql(u8, field(envelope, "capability_revision"), self.current_capability)) {
            try self.add(code_stale_capability_revision, index);
            return null;
        }
        return self.features.get(key) orelse "unavailable";
    }

    fn submitControls(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, pending: *Pending) !void {
        const carried = [_]struct { key: []const u8, member: []const u8 }{
            .{ .key = feature_model_selection, .member = "model_id" },
            .{ .key = feature_instructions, .member = "instructions" },
            .{ .key = feature_tool_selection, .member = "tool_choice" },
            .{ .key = feature_structured_output, .member = "output_schema" },
        };
        var any = false;
        for (carried) |control| {
            if (member(payload, control.member) != null) any = true;
        }
        if (!any) return;
        pending.controls.present = true;
        pending.controls.mode = self.modes.get(feature_model_selection) orelse "";

        for (carried) |control| {
            if (member(payload, control.member) == null) continue;
            const level = try self.controlDescriptor(index, envelope, control.key) orelse continue;
            if (!affirmative(level)) {
                self.propose(&pending.control, .{
                    .rung = rung_capability,
                    .key = control.key,
                    .pointer = controlPointer(control.key),
                    .code = error_unsupported_feature,
                    .reason = reason_unadvertised,
                    .detail_name = "feature",
                    .detail_value = control.key,
                    .diagnostic = code_unavailable_capability,
                });
                continue;
            }
            if (std.mem.eql(u8, level, "degraded") and !allowsDegraded(payload, control.key)) {
                self.propose(&pending.control, .{
                    .rung = rung_degradation,
                    .key = control.key,
                    .pointer = controlPointer(control.key),
                    .code = error_capability_degraded,
                    .detail_name = "feature",
                    .detail_value = control.key,
                    .diagnostic = code_degraded_without_optin,
                });
                continue;
            }
            if (std.mem.eql(u8, control.key, feature_tool_selection)) try self.duplicateToolNames(index);
            if (std.mem.eql(u8, control.key, feature_structured_output)) {
                pending.controls.schema = member(payload, "output_schema");
                const support = self.supports.get(control.key);
                if (support) |declared| {
                    const constraints = member(declared, "constraints");
                    if (constraints) |held| pending.controls.fixed_result = member(held, "fixed_result");
                }
            }
            if (try self.unsatisfiable(control.key, payload, pending)) |defect| {
                self.propose(&pending.control, defect);
                continue;
            }
            if (pending.satisfies) try pending.satisfiable.put(self.arena.allocator(), control.key, {});
        }
    }

    fn duplicateNames(self: *Machine, index: usize, declared: ?std.json.Value) !void {
        const tools = declared orelse return;
        if (tools != .array) return;
        var names = std.ArrayList([]const u8).empty;
        defer names.deinit(self.allocator);
        for (tools.array.items) |tool| try names.append(self.allocator, memberString(tool, "name"));
        if (duplicateToolName(names.items) != null) try self.add(code_duplicate_tool_name, index);
    }

    fn duplicateToolNames(self: *Machine, index: usize) !void {
        if (self.catalog_ambiguous) return;
        if (!self.catalog_known) return;
        if (duplicateToolName(self.catalog.items) != null) {
            try self.add(code_duplicate_tool_name, index);
        }
    }

    fn unsatisfiable(self: *Machine, key: []const u8, payload: std.json.Value, pending: *Pending) !?Expectation {
        pending.satisfies = true;
        const unsatisfiableAs = struct {
            fn at(control: []const u8, pointer: []const u8, name: []const u8, value: []const u8) Expectation {
                return .{
                    .rung = rung_unsatisfiable,
                    .key = control,
                    .pointer = pointer,
                    .code = error_unsupported_feature,
                    .reason = reason_unsatisfiable,
                    .detail_name = if (value.len == 0) "" else name,
                    .detail_value = value,
                    .diagnostic = code_unsatisfiable_control,
                };
            }
        }.at;

        if (std.mem.eql(u8, key, feature_model_selection)) {
            pending.controls.model_present = true;
            pending.controls.model = memberString(payload, "model_id");
            if (pending.controls.model.len == 0) {
                pending.satisfies = false;
                return Expectation{
                    .rung = rung_unsatisfiable,
                    .key = key,
                    .pointer = "/payload/model_id",
                    .code = error_model_not_found,
                    .detail_name = "model_id",
                    .detail_value = "",
                    .diagnostic = code_unsatisfiable_control,
                };
            }
            const catalog = self.catalogs.get(pending.session);
            if (catalog == null or !catalog.?.binds(self.current_capability)) {
                pending.model_unjudged = true;
                pending.revision = self.current_capability;
            }
            if (catalog) |listed| {
                if (listed.binds(self.current_capability) and listed.ids.get(pending.controls.model) != null) {
                    pending.model_listed = true;
                }
                if (listed.binds(self.current_capability) and listed.ids.get(pending.controls.model) == null) {
                    pending.satisfies = false;
                    return Expectation{
                        .rung = rung_unsatisfiable,
                        .key = key,
                        .pointer = "/payload/model_id",
                        .code = error_model_not_found,
                        .detail_name = "model_id",
                        .detail_value = pending.controls.model,
                        .diagnostic = code_model_not_in_catalog,
                    };
                }
            }
            return null;
        }
        if (std.mem.eql(u8, key, feature_instructions)) return null;
        if (std.mem.eql(u8, key, feature_tool_selection)) {
            const raw = member(payload, "tool_choice") orelse return null;
            const policy = toolChoicePolicy(self.arena.allocator(), raw) catch {
                pending.satisfies = false;
                return unsatisfiableAs(key, "/payload/tool_choice", "", "");
            };
            const known = self.catalog_known and duplicateToolName(self.catalog.items) == null;
            pending.controls.choice = policy;
            pending.controls.catalog = try self.arena.allocator().dupe([]const u8, self.catalog.items);
            pending.controls.catalog_known = known;
            if (try self.toolChoiceDefect(policy, known)) |defect| {
                pending.satisfies = false;
                const detail: []const u8 = if (defect.tool.len == 0) "" else "tool";
                return unsatisfiableAs(key, defect.pointer, detail, defect.tool);
            }
            pending.satisfies = self.disclosedMode(key, policy.mode);
            return null;
        }
        if (std.mem.eql(u8, key, feature_structured_output)) {
            const raw = member(payload, "output_schema") orelse return null;
            if (outputSchemaDefect(raw)) {
                pending.satisfies = false;
                return unsatisfiableAs(key, "/payload/output_schema", "field", "output_schema");
            }
            if (pending.controls.fixed_result) |fixed| {
                if (!try self.conformsToSchema(raw, fixed)) {
                    pending.satisfies = false;
                    return unsatisfiableAs(key, "/payload/output_schema", "field", "output_schema");
                }
            }
            return null;
        }
        pending.satisfies = false;
        return null;
    }

    const ChoiceDefect = struct { pointer: []const u8, tool: []const u8 = "" };

    fn toolChoiceDefect(self: *Machine, policy: ToolChoice, known: bool) !?ChoiceDefect {
        const catalog = self.catalog.items;
        if (known) {
            for (policy.allowed) |name| {
                if (!listedIn(catalog, name)) return .{ .pointer = "/payload/tool_choice/allowed", .tool = name };
            }
            for (policy.disallowed) |name| {
                if (!listedIn(catalog, name)) return .{ .pointer = "/payload/tool_choice/disallowed", .tool = name };
            }
        }
        var filtered = std.ArrayList([]const u8).empty;
        defer filtered.deinit(self.allocator);
        for (catalog) |name| {
            if (policy.has_allowed and !listedIn(policy.allowed, name)) continue;
            if (listedIn(policy.disallowed, name)) continue;
            try filtered.append(self.allocator, name);
        }
        if (std.mem.eql(u8, policy.mode, "required") and known and filtered.items.len == 0) {
            return .{ .pointer = "/payload/tool_choice/mode" };
        }
        if (std.mem.eql(u8, policy.mode, "named")) {
            const named = ChoiceDefect{ .pointer = "/payload/tool_choice/name", .tool = policy.name };
            if (listedIn(policy.disallowed, policy.name)) return named;
            if (policy.has_allowed and !listedIn(policy.allowed, policy.name)) return named;
            if (known and !listedIn(filtered.items, policy.name)) return named;
        }
        return null;
    }

    fn raise(self: *Machine, expectation: Expectation, index: usize) !void {
        if (expectation.diagnostic.len == 0) return;
        try self.add(expectation.diagnostic, index);
    }

    fn propose(self: *Machine, slot: *?Expectation, candidate: Expectation) void {
        _ = self;
        const held = slot.* orelse {
            slot.* = candidate;
            return;
        };
        if (outranks(candidate, held)) slot.* = candidate;
    }

    fn retained(pending: *const Pending, out: *[4]Expectation) []const Expectation {
        var at: usize = 0;
        for ([_]?Expectation{ pending.control, pending.attachment, pending.subscribe, pending.limit_refusal }) |slot| {
            if (slot) |held| {
                out[at] = held;
                at += 1;
            }
        }
        return out[0..at];
    }

    fn deliveryExpectation(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, pending: *Pending) !void {
        const delivery = memberString(payload, "delivery");
        if (delivery.len == 0 or std.mem.eql(u8, delivery, "auto")) return;
        if (!std.mem.eql(u8, delivery, "queue")) {
            const named = try std.fmt.allocPrint(self.arena.allocator(), "delivery.{s}", .{delivery});
            try self.feature(index, envelope, named);
            return;
        }
        const key = try std.fmt.allocPrint(self.arena.allocator(), "session.message.delivery.{s}", .{delivery});
        const level = try self.controlDescriptor(index, envelope, key) orelse return;
        if (std.mem.eql(u8, delivery, "queue") and !affirmative(level)) {
            self.propose(&pending.control, .{
                .rung = rung_capability,
                .key = key,
                .pointer = "/payload/delivery",
                .code = error_unsupported_feature,
                .reason = reason_unadvertised,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_unavailable_capability,
            });
            return;
        }
        if (std.mem.eql(u8, level, "degraded") and !allowsDegraded(payload, key)) {
            self.propose(&pending.control, .{
                .rung = rung_degradation,
                .key = key,
                .pointer = "/payload/delivery",
                .code = error_capability_degraded,
                .detail_name = "feature",
                .detail_value = key,
                .diagnostic = code_degraded_without_optin,
            });
        }
    }

    fn settleSubmitAdmission(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !Controls {
        const pending = self.submits.get(field(envelope, "in_reply_to")) orelse return .{};
        if (pending.control) |expectation| {
            if (!pending.fired) {
                pending.fired = true;
                try self.raise(expectation, index);
            }
            return .{};
        }
        if (pending.model_unjudged) {
            const holder = try self.sessionFor(pending.session);
            try holder.unjudged.append(self.allocator, .{
                .model = pending.controls.model,
                .revision = pending.revision,
                .admitted = true,
                .index = index,
                .refusal = .{ .null = {} },
            });
        }
        if (pending.controls.model_present and
            !std.mem.eql(u8, memberString(payload, "model_id"), pending.controls.model))
        {
            try self.add(code_unapplied_control, index);
        }
        return pending.controls;
    }

    fn applyModelControl(self: *Machine, holder: *Session, controls: Controls) void {
        _ = self;
        if (std.mem.eql(u8, controls.mode, "per_run")) {
            holder.expected_default = holder.current_model;
            holder.guard_default = holder.current_known;
            return;
        }
        if (std.mem.eql(u8, controls.mode, "session_mutation")) {
            holder.current_model = controls.model;
            holder.current_known = true;
        }
    }

    fn checkCallAgainstChoice(self: *Machine, index: usize, payload: std.json.Value, run: *Run) !void {
        if (!run.controls.present) return;
        const name = memberString(payload, "name");
        if (name.len == 0) return;
        try run.controls.calls.put(self.arena.allocator(), name, {});
        const choice = run.controls.choice orelse return;
        if (!permits(choice, name, run.controls.catalog, run.controls.catalog_known)) {
            try self.add(code_unapplied_control, index);
        }
    }

    fn conformsToSchema(self: *Machine, schema: std.json.Value, result: std.json.Value) !bool {
        var registry = jsonschema.Registry{ .allocator = self.allocator };
        defer registry.deinit();
        var validator = jsonschema.Validator.init(self.allocator, &registry);
        defer validator.deinit();
        try validator.overrides.put(self.allocator, output_schema_document, schema);
        const failure = validator.validateSchema(schema, output_schema_document, result) catch |raised| switch (raised) {
            error.OutOfMemory => return error.OutOfMemory,
            else => return false,
        };
        return failure == null;
    }

    fn checkCompletedControls(self: *Machine, index: usize, payload: std.json.Value, run: *Run) !void {
        const controls = run.controls;
        if (!controls.present) return;
        const reported = memberString(payload, "model_id");
        if (reported.len != 0 and controls.model_present and !std.mem.eql(u8, reported, controls.model)) {
            try self.add(code_unapplied_control, index);
        }
        if (controls.schema) |schema| {
            const result = member(payload, "result");
            if (result == null) {
                try self.add(code_unapplied_control, index);
            } else if (!try self.conformsToSchema(schema, result.?)) {
                try self.add(code_unapplied_control, index);
            } else if (controls.fixed_result) |fixed| {
                if (!valueEql(fixed, result.?)) try self.add(code_unapplied_control, index);
            }
        }
        if (controls.choice) |choice| {
            if (std.mem.eql(u8, choice.mode, "required") and controls.calls.count() == 0) {
                try self.add(code_unapplied_control, index);
            }
            if (std.mem.eql(u8, choice.mode, "named") and controls.calls.get(choice.name) == null) {
                try self.add(code_unapplied_control, index);
            }
        }
    }

    fn settleControlRefusal(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !bool {
        const request = field(envelope, "in_reply_to");
        const pending = self.submits.get(request) orelse return false;
        const raised = member(payload, "error") orelse std.json.Value{ .null = {} };
        if (self.requests.get(request)) |asked| {
            if (std.mem.eql(u8, asked.declared, "session.open.request") and
                openLevelRefusal(memberString(raised, "code"))) return true;
        }
        var slots: [4]Expectation = undefined;
        const held = retained(pending, &slots);
        if (held.len != 0) {
            var speaker = held[0];
            for (held) |candidate| {
                if (conformingRefusal(raised, candidate)) return true;
                if (outranks(candidate, speaker)) speaker = candidate;
            }
            try self.raise(speaker, index);
            if (pending.control != null) return true;
        }
        if (pending.model_query) return true;
        if (pending.model_listed and
            std.mem.eql(u8, memberString(raised, "code"), error_model_not_found))
        {
            try self.add(code_model_not_in_catalog, index);
        }
        if (pending.model_unjudged) {
            const holder = try self.sessionFor(pending.session);
            try holder.unjudged.append(self.allocator, .{
                .model = pending.controls.model,
                .revision = pending.revision,
                .admitted = false,
                .index = index,
                .refusal = raised,
            });
        }
        const named = memberString(member(raised, "details") orelse std.json.Value{ .null = {} }, "feature");
        if (std.mem.eql(u8, memberString(raised, "code"), error_unsupported_feature) and
            named.len != 0 and pending.satisfiable.get(named) != null)
        {
            try self.add(code_unsatisfiable_control, index);
            return true;
        }
        return false;
    }

    fn openSubmitWindow(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, scope: []const u8, own: bool) !void {
        const session = if (scope.len != 0) scope else memberString(payload, "session_id");
        const counts = self.queueCounts(session);
        var open = std.ArrayList(*Window).empty;
        defer open.deinit(self.allocator);
        try self.openWindows(session, &open);

        const window = try self.arena.allocator().create(Window);
        window.* = .{
            .request = field(envelope, "id"),
            .session = session,
            .delivery = memberString(payload, "delivery"),
            .busy_at_request = counts.active > 0,
            .busy_ever = counts.active > 0,
            .started_ever = counts.started > 0,
            .offered = self.queueOffered(),
            .max_active = if (self.limits) |held| held.max_active else null,
            .max_queued = if (self.limits) |held| held.max_queued else null,
            .mutation = member(payload, "model_id") != null and
                std.mem.eql(u8, self.modes.get(feature_model_selection) orelse "", "session_mutation"),
        };
        window.reached_strict = exceeds(window, counts, 0);
        window.reached_loose = exceeds(window, counts, open.items.len);
        try self.windows.put(self.allocator, window.request, window);
        try self.refreshQueueWindows(session);
        if (!own) return;

        const pending = try self.arena.allocator().create(Pending);
        pending.* = .{ .session = session };
        try self.submitControls(index, envelope, payload, pending);
        try self.deliveryExpectation(index, envelope, payload, pending);
        try self.submits.put(self.allocator, window.request, pending);
    }

    fn closeSubmitWindow(self: *Machine, request: []const u8) !void {
        const window = self.windows.get(request) orelse return;
        if (window.closed) return;
        window.closed = true;
        try self.refreshQueueWindows(window.session);
    }

    fn settleQueueRefusal(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const window = self.windows.get(field(envelope, "in_reply_to")) orelse return;
        const raised = memberString(member(payload, "error") orelse std.json.Value{ .null = {} }, "code");
        switch (stateCondition(window)) {
            .busy_session => {
                if (!std.mem.eql(u8, raised, error_run_active)) {
                    try self.add(code_illegal_run_transition, index);
                }
            },
            .queue_limit => {
                if (window.reached_strict) {
                    if (!std.mem.eql(u8, raised, error_run_active)) {
                        try self.add(code_queue_limit_exceeded, index);
                    }
                } else if (std.mem.eql(u8, raised, error_run_active) and !window.reached_loose and
                    !(window.mutation and window.started_ever))
                {
                    try self.add(code_queue_limit_exceeded, index);
                }
            },
            .none => {},
        }
    }

    fn queueOverlap(self: *Machine, index: usize, payload: std.json.Value) !bool {
        const counts = self.queueCounts(memberString(payload, "session_id"));
        if (counts.active == 0) return false;
        if (std.mem.eql(u8, memberString(payload, "admission"), "queued") and self.queueOffered()) return false;
        try self.add(code_illegal_run_transition, index);
        return true;
    }

    fn queueAdmission(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const window = self.windows.get(field(envelope, "in_reply_to"));
        var requested = memberString(payload, "requested_delivery");
        if (window) |open| {
            if (open.delivery.len != 0) requested = open.delivery;
        }
        const queued = std.mem.eql(u8, memberString(payload, "admission"), "queued");
        if (std.mem.eql(u8, requested, "queue") and !queued) {
            try self.add(code_illegal_run_transition, index);
        } else if (std.mem.eql(u8, requested, "auto") and queued and
            window != null and window.?.busy_at_request and
            !std.mem.eql(u8, memberString(payload, "delivery_resolution"), resolution_session_busy))
        {
            try self.add(code_illegal_run_transition, index);
        }
        if (!queued) return;

        const request = field(envelope, "in_reply_to");
        if (!self.queueAlreadyGated(request)) {
            if (self.advertisedLevel(feature_delivery_queue)) |level| {
                if (!affirmative(level)) {
                    try self.add(code_unavailable_capability, index);
                } else if (std.mem.eql(u8, level, "degraded") and !self.optedIntoQueue(request)) {
                    try self.add(code_degraded_without_optin, index);
                }
            }
        }

        const limits = self.limits orelse return;
        const counts = self.queueCounts(memberString(payload, "session_id"));
        if (limits.max_active) |bound| {
            if (@as(i64, @intCast(counts.active + 1)) > bound) {
                try self.add(code_queue_limit_exceeded, index);
                return;
            }
        }
        if (limits.max_queued) |bound| {
            if (@as(i64, @intCast(counts.queued + 1)) > bound) {
                try self.add(code_queue_limit_exceeded, index);
            }
        }
    }

    fn queueAlreadyGated(self: *const Machine, request: []const u8) bool {
        const pending = self.submits.get(request) orelse return false;
        const expectation = pending.control orelse return false;
        return std.mem.eql(u8, expectation.key, feature_delivery_queue);
    }

    fn optedIntoQueue(self: *const Machine, request: []const u8) bool {
        const asked = self.requests.get(request) orelse return false;
        return allowsDegraded(asked.payload, feature_delivery_queue);
    }

    fn checkQueueOrder(self: *Machine, index: usize, envelope: std.json.Value, run: *Run, declared: []const u8) !void {
        if (!run.admitted or member(envelope, "sequence") == null) return;
        if (isTerminal(declared) and !run.started) return;
        const holder = self.sessions.get(run.session) orelse return;
        for (holder.order.items) |id| {
            const earlier = self.runs.get(id) orelse continue;
            if (std.mem.eql(u8, earlier.id, run.id) or earlier.order >= run.order) continue;
            if (!earlier.terminal) {
                try self.add(code_queue_order_violation, index);
                return;
            }
        }
    }

    fn admit(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        if (!memberBool(payload, "accepted")) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        const run_id = memberString(payload, "run_id");
        if (run_id.len == 0) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        if (!admissionShape(
            memberString(payload, "admission"),
            memberString(payload, "effective_delivery"),
            memberString(payload, "status"),
        )) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        if (self.runs.get(run_id) != null) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        const session_id = memberString(payload, "session_id");
        const holder = try self.sessionFor(session_id);
        const queued = std.mem.eql(u8, memberString(payload, "admission"), "queued");

        const overlap = try self.queueOverlap(index, payload);
        if (!overlap) try self.queueAdmission(index, envelope, payload);
        const controls = try self.settleSubmitAdmission(index, envelope, payload);
        if (controls.present and controls.model_present and !queued) self.applyModelControl(holder, controls);

        const run = try self.arena.allocator().create(Run);
        run.* = .{
            .id = run_id,
            .session = session_id,
            .admitted = true,
            .last_index = index,
            .admitted_queued = queued,
            .order = holder.order.items.len,
            .admitted_model = memberString(payload, "model_id"),
            .controls = controls,
            .deferred_controls = queued and controls.present and controls.model_present,
            .status = "queued",
        };
        try self.runs.put(self.allocator, run_id, run);
        try holder.order.append(self.allocator, run_id);

        try self.refreshQueueWindows(session_id);
        if (!queued) {
            if (holder.active.len == 0) {
                holder.active = run_id;
            } else if (self.runs.get(holder.active)) |previous| {
                if (previous.terminal) holder.active = run_id;
            } else {
                holder.active = run_id;
            }
        }
    }

    fn openResponse(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value) !void {
        const session_id = memberString(payload, "session_id");
        try self.gatedResponse(index, envelope, .open);
        {
            const holder = try self.sessionFor(session_id);
            if (self.submits.get(field(envelope, "in_reply_to"))) |opened| {
                if (opened.attachment == null) {
                    for (opened.provided) |name| {
                        if (listedIn(holder.provided.items, name)) continue;
                        try holder.provided.append(self.allocator, name);
                    }
                }
            }
            const reported = memberString(payload, "current_model_id");
            if (reported.len != 0) {
                holder.current_model = reported;
                holder.current_known = true;
            }
        }
        if (member(payload, "recovery")) |recovery| {
            if (memberBool(recovery, "recovered")) {
                const created = try self.arena.allocator().create(Recovery);
                created.* = .{
                    .run = memberString(recovery, "previous_run_id"),
                    .gap = std.mem.eql(u8, memberString(recovery, "reason"), "replay_gap"),
                };
                const cursor = memberString(recovery, "resume_cursor");
                if (cursor.len != 0) {
                    if (std.fmt.parseUnsigned(u64, cursor, 10)) |parsed| {
                        created.cursor = parsed;
                        created.cursor_set = true;
                    } else |_| {}
                }
                try self.recoveries.put(self.allocator, session_id, created);
                try self.bootstrapRecoveredRuns(index, session_id, payload);
            }
        }
        try self.compoundOpen(index, envelope, payload, session_id);
    }

    fn compoundOpen(self: *Machine, index: usize, envelope: std.json.Value, payload: std.json.Value, session_id: []const u8) !void {
        const request = self.requests.get(field(envelope, "in_reply_to")) orelse return;
        if (!request.carries_message) return;
        if (self.submits.get(field(envelope, "in_reply_to"))) |held| {
            if (!std.mem.eql(u8, held.session, session_id)) {
                held.session = session_id;
                if (self.windows.get(field(envelope, "in_reply_to"))) |window| window.session = session_id;
                try self.refreshQueueWindows(session_id);
            }
        }

        const listed = member(payload, "active_runs") orelse return;
        if (listed != .array) return;
        var found: ?std.json.Value = null;
        var count: usize = 0;
        for (listed.array.items) |entry| {
            if (self.runs.get(memberString(entry, "run_id")) != null) continue;
            found = entry;
            count += 1;
        }
        if (count != 1) return;
        const entry = found.?;
        const status = memberString(entry, "status");

        var synthesized: std.json.ObjectMap = .empty;
        const allocator = self.arena.allocator();
        try synthesized.put(allocator, "session_id", .{ .string = session_id });
        try synthesized.put(allocator, "accepted", .{ .bool = true });
        try synthesized.put(allocator, "run_id", .{ .string = memberString(entry, "run_id") });
        try synthesized.put(allocator, "status", .{ .string = status });
        try synthesized.put(allocator, "admission", .{ .string = admissionFor(status) });
        try synthesized.put(allocator, "effective_delivery", .{ .string = effectiveFor(status) });
        if (self.requests.get(field(envelope, "in_reply_to"))) |asked| {
            if (asked.message) |message| {
                const delivery = memberString(message, "delivery");
                if (delivery.len != 0) try synthesized.put(allocator, "requested_delivery", .{ .string = delivery });
            }
        }
        var selected: []const u8 = "";
        if (self.requests.get(field(envelope, "in_reply_to"))) |asked| {
            if (asked.message) |message| selected = memberString(message, "model_id");
        }
        const attributed = if (selected.len != 0) selected else memberString(payload, "current_model_id");
        if (attributed.len != 0) try synthesized.put(allocator, "model_id", .{ .string = attributed });
        try self.admit(index, envelope, .{ .object = synthesized });
    }

    fn bootstrapRecoveredRuns(self: *Machine, index: usize, session_id: []const u8, payload: std.json.Value) !void {
        const recovery = self.recoveries.get(session_id);
        const holder = try self.sessionFor(session_id);
        var listed: usize = 0;
        if (member(payload, "active_runs")) |entries| {
            if (entries == .array) {
                for (entries.array.items) |entry| {
                    listed += 1;
                    const run_id = memberString(entry, "run_id");
                    if (run_id.len == 0 or self.runs.get(run_id) != null) continue;
                    const status = memberString(entry, "status");
                    const queued = std.mem.eql(u8, status, "queued") or
                        (std.mem.eql(u8, status, "cancelling") and cancellingHoldsItsPlace(entry));
                    try self.introduceRecovered(index, holder, run_id, session_id, .{
                        .queued = queued,
                        .started = !queued,
                        .next = resumeSequence(recovery, run_id, unsigned(entry, "as_of_sequence")),
                        .status = status,
                    });
                }
            }
        }
        const active = memberString(payload, "active_run_id");
        if (active.len != 0 and self.runs.get(active) == null) {
            try self.introduceRecovered(index, holder, active, session_id, .{
                .started = true,
                .next = resumeSequence(recovery, active, null),
                .status = "running",
            });
        }
    }

    const Recovered = struct {
        started: bool,
        next: u64,
        status: []const u8,
        queued: bool = false,
    };

    fn introduceRecovered(self: *Machine, index: usize, holder: *Session, run_id: []const u8, session_id: []const u8, shape: Recovered) !void {
        const run = try self.arena.allocator().create(Run);
        run.* = .{
            .id = run_id,
            .session = session_id,
            .admitted = true,
            .started = shape.started,
            .next = shape.next,
            .last_index = index,
            .recovered = true,
            .admitted_queued = shape.queued,
            .order = holder.order.items.len,
            .status = shape.status,
        };
        try self.runs.put(self.allocator, run_id, run);
        try holder.order.append(self.allocator, run_id);
        try self.refreshQueueWindows(session_id);
    }

    fn stateDocument(self: *Machine, index: usize, payload: std.json.Value) !void {
        const session_id = memberString(payload, "session_id");
        {
            const holder = try self.sessionFor(session_id);
            const reported = memberString(payload, "current_model_id");
            if (holder.guard_default and !std.mem.eql(u8, reported, holder.expected_default)) {
                try self.add(code_unapplied_control, index);
            }
            holder.current_model = reported;
            holder.current_known = true;
        }
        const recovery = self.recoveries.get(session_id) orelse return;
        if (recovery.state_checked) return;
        recovery.state_checked = true;
        if (recovery.gap) return;
        const active = memberString(payload, "active_run_id");
        if (active.len == 0 or self.runs.get(active) != null) return;
        const holder = try self.sessionFor(session_id);
        try self.introduceRecovered(index, holder, active, session_id, .{
            .started = true,
            .next = resumeSequence(recovery, active, null),
            .status = "running",
        });
    }

    fn cancelResponse(self: *Machine, index: usize, payload: std.json.Value) !void {
        if (!memberBool(payload, "accepted")) return;
        const run_id = memberString(payload, "run_id");
        const run = self.runs.get(run_id);
        if (run == null or (run.?.terminal and !std.mem.eql(u8, run.?.terminal_type, "run.cancelled"))) {
            try self.add(code_illegal_run_transition, index);
            return;
        }
        if (!run.?.terminal) {
            run.?.cancel_accepted = true;
            run.?.status = "cancelling";
        }
    }

    fn runEvent(self: *Machine, index: usize, envelope: std.json.Value, declared: []const u8) !void {
        const run_id = field(envelope, "run_id");
        var run = self.runs.get(run_id);
        if (run == null) {
            try self.add(code_illegal_run_transition, index);
            const created = try self.arena.allocator().create(Run);
            created.* = .{ .id = run_id, .session = field(envelope, "session_id") };
            try self.runs.put(self.allocator, run_id, created);
            run = created;
        }
        const state = run.?;
        state.last_index = index;

        const sequence = unsigned(envelope, "sequence");
        if (self.recoveries.get(state.session)) |recovery| {
            if (!recovery.gap and !recovery.first_replay_seen and std.mem.eql(u8, run_id, recovery.run)) {
                recovery.first_replay_seen = true;
                if (recovery.cursor_set and (sequence == null or sequence.? != recovery.cursor + 1)) {
                    try self.add(code_sequence_gap, index);
                }
            }
        }

        if (sequence) |value| {
            if (value < state.next) {
                try self.add(code_sequence_regression, index);
            } else if (value > state.next) {
                try self.add(code_sequence_gap, index);
            }
            if (value >= state.next) state.next = value + 1;
        }

        if (state.terminal) {
            if (isTerminal(declared)) {
                if (std.mem.eql(u8, declared, "run.cancelled") and !state.cancel_accepted and !state.recovered) {
                    try self.add(code_illegal_run_transition, index);
                }
                try self.add(code_duplicate_run_terminal, index);
            } else {
                try self.add(code_event_after_terminal, index);
            }
            return;
        }
        try self.checkQueueOrder(index, envelope, state, declared);

        if (std.mem.eql(u8, declared, "run.started")) {
            if (state.started) {
                try self.add(code_illegal_run_transition, index);
            } else {
                state.started = true;
                state.status = "running";
                if (state.admitted_model.len != 0) {
                    const reported = memberString(member(envelope, "payload") orelse std.json.Value{ .null = {} }, "model_id");
                    if (reported.len == 0 and state.controls.model_present) {
                        try self.add(code_unapplied_control, index);
                    } else if (reported.len != 0 and !std.mem.eql(u8, reported, state.admitted_model)) {
                        try self.add(code_unapplied_control, index);
                    }
                }
                if (self.sessions.get(state.session)) |holder| {
                    holder.active = state.id;
                    if (state.deferred_controls) {
                        state.deferred_controls = false;
                        self.applyModelControl(holder, state.controls);
                    }
                }
            }
            return;
        }

        if (!state.started and !preStartSettlement(declared)) {
            try self.add(code_missing_run_started, index);
        }

        if (std.mem.eql(u8, declared, "run.status.updated")) {
            const payload = member(envelope, "payload") orelse std.json.Value{ .null = {} };
            const next_status = memberString(payload, "status");
            if (!legalRunStatusTransition(state.status, next_status)) {
                try self.add(code_illegal_run_transition, index);
            } else {
                state.status = next_status;
            }
        }

        if (runEventFeature(declared)) |name| try self.feature(index, envelope, name);

        if (std.mem.eql(u8, declared, "action.call.requested")) {
            try self.checkCallAgainstChoice(index, member(envelope, "payload") orelse std.json.Value{ .null = {} }, state);
        }

        if (isTerminal(declared)) {
            if (std.mem.eql(u8, declared, "run.cancelled") and !state.cancel_accepted and !state.recovered) {
                try self.add(code_illegal_run_transition, index);
            }
            if (std.mem.eql(u8, declared, "run.completed")) {
                try self.checkCompletedControls(index, member(envelope, "payload") orelse std.json.Value{ .null = {} }, state);
            }
            state.terminal = true;
            state.terminal_type = declared;
            if (std.mem.eql(u8, declared, "run.completed")) state.status = "completed";
            if (std.mem.eql(u8, declared, "run.failed")) state.status = "failed";
            if (std.mem.eql(u8, declared, "run.cancelled")) state.status = "cancelled";
            if (self.sessions.get(state.session)) |holder| {
                if (std.mem.eql(u8, holder.active, state.id)) holder.active = "";
            }
            try self.refreshQueueWindows(state.session);
        }
    }

    pub fn close(self: *Machine) !void {
        for (self.runs.values()) |run| {
            if (run.admitted and !run.started and !run.terminal) {
                try self.add(code_missing_run_started, run.last_index);
            }
            if (run.admitted and !run.terminal) {
                try self.add(
                    if (run.cancel_accepted) code_cancel_not_settled else code_missing_run_terminal,
                    run.last_index,
                );
            }
        }
    }
};

pub const feature_delivery_queue = "session.message.delivery.queue";
const output_schema_document = "output-schema";
const feature_model_selection = "run.model_selection";
const feature_models_list = "models.list";
const feature_tools_list = "action.tools.list";
const feature_tool_sources_attach = "action.tool_sources.attach";
const feature_tools_provide = "action.tools.provide";
const feature_open_subscribe = "session.open.subscribe";
const feature_instructions = "run.instructions";
const feature_tool_selection = "run.tool_selection";
const feature_structured_output = "run.structured_output";
const error_unsupported_feature = "unsupported_feature";
const error_capability_degraded = "capability_degraded";
const error_model_not_found = "model_not_found";
const reason_unadvertised = "unadvertised";

const reason_unsatisfiable = "unsatisfiable";

fn openLevelRefusal(code: []const u8) bool {
    return listedIn(&.{ "session_exists", "unknown_adapter", "session_closed", "stale_capabilities" }, code);
}

fn sameCatalog(a: ?std.json.Value, b: ?std.json.Value) bool {
    const left = a orelse return b == null;
    const right = b orelse return false;
    if (left != .array or right != .array) return valueEql(left, right);
    if (left.array.items.len != right.array.items.len) return false;
    for (left.array.items) |model| {
        const id = memberString(model, "id");
        var matched = false;
        for (right.array.items) |other| {
            if (!std.mem.eql(u8, memberString(other, "id"), id)) continue;
            if (!valueEql(model, other)) return false;
            matched = true;
        }
        if (!matched) return false;
    }
    return true;
}

fn sameBound(a: ?i64, b: ?i64) bool {
    if (a == null or b == null) return a == null and b == null;
    return a.? == b.?;
}

fn exemptFromRevision(declared: []const u8) bool {
    return std.mem.eql(u8, declared, "protocol.initialize.request") or
        std.mem.eql(u8, declared, "capabilities.request") or
        std.mem.eql(u8, declared, "capabilities.updated") or
        std.mem.eql(u8, declared, "error.response");
}

fn permits(choice: ToolChoice, name: []const u8, catalog: []const []const u8, known: bool) bool {
    if (std.mem.eql(u8, choice.mode, "none")) return false;
    if (known and !listedIn(catalog, name)) return false;
    if (choice.has_allowed and !listedIn(choice.allowed, name)) return false;
    if (listedIn(choice.disallowed, name)) return false;
    if (std.mem.eql(u8, choice.mode, "named") and !std.mem.eql(u8, name, choice.name)) return false;
    return true;
}

fn numeric(value: std.json.Value) ?f64 {
    return switch (value) {
        .integer => |n| @floatFromInt(n),
        .float => |n| n,
        .number_string => |text| std.fmt.parseFloat(f64, text) catch null,
        else => null,
    };
}

fn exactInteger(value: std.json.Value) ?i128 {
    return switch (value) {
        .integer => |n| n,
        .float => |n| integralFloat(n),
        .number_string => |text| std.fmt.parseInt(i128, text, 10) catch null,
        else => null,
    };
}

fn integralFloat(value: f64) ?i128 {
    if (!std.math.isFinite(value)) return null;
    if (@trunc(value) != value) return null;
    if (value >= 0x1p127 or value < -0x1p127) return null;
    return @intFromFloat(value);
}

fn valueEql(a: std.json.Value, b: std.json.Value) bool {
    if (exactInteger(a)) |left| {
        if (exactInteger(b)) |right| return left == right;
    }
    if (numeric(a)) |left| {
        const right = numeric(b) orelse return false;
        return left == right;
    }
    if (numeric(b) != null) return false;
    return switch (a) {
        .null => b == .null,
        .bool => |x| b == .bool and b.bool == x,
        .integer, .float, .number_string => unreachable,
        .string => |x| b == .string and std.mem.eql(u8, b.string, x),
        .array => |x| blk: {
            if (b != .array or b.array.items.len != x.items.len) break :blk false;
            for (x.items, b.array.items) |left, right| {
                if (!valueEql(left, right)) break :blk false;
            }
            break :blk true;
        },
        .object => |x| blk: {
            if (b != .object or b.object.count() != x.count()) break :blk false;
            var it = x.iterator();
            while (it.next()) |entry| {
                const other = b.object.get(entry.key_ptr.*) orelse break :blk false;
                if (!valueEql(entry.value_ptr.*, other)) break :blk false;
            }
            break :blk true;
        },
    };
}

fn listedIn(names: []const []const u8, wanted: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, wanted)) return true;
    }
    return false;
}

fn duplicateToolName(catalog: []const []const u8) ?[]const u8 {
    for (catalog, 0..) |name, at| {
        for (catalog[0..at]) |earlier| {
            if (std.mem.eql(u8, earlier, name)) return name;
        }
    }
    return null;
}

const ToolChoiceDefect = error{NotThePolicy};

fn stringList(allocator: std.mem.Allocator, value: std.json.Value) !([]const []const u8) {
    if (value != .array) return ToolChoiceDefect.NotThePolicy;
    const out = try allocator.alloc([]const u8, value.array.items.len);
    for (value.array.items, 0..) |entry, at| {
        if (entry != .string) return ToolChoiceDefect.NotThePolicy;
        out[at] = entry.string;
    }
    return out;
}

fn toolChoicePolicy(allocator: std.mem.Allocator, raw: std.json.Value) !ToolChoice {
    if (raw != .object) return ToolChoiceDefect.NotThePolicy;
    var policy = ToolChoice{};
    var it = raw.object.iterator();
    while (it.next()) |entry| {
        const key = entry.key_ptr.*;
        if (!std.mem.eql(u8, key, "mode") and !std.mem.eql(u8, key, "name") and
            !std.mem.eql(u8, key, "allowed") and !std.mem.eql(u8, key, "disallowed"))
        {
            return ToolChoiceDefect.NotThePolicy;
        }
    }
    if (raw.object.get("mode")) |mode| {
        if (mode != .string) return ToolChoiceDefect.NotThePolicy;
        policy.mode = mode.string;
    }
    if (!listedIn(&.{ "auto", "none", "required", "named" }, policy.mode)) {
        return ToolChoiceDefect.NotThePolicy;
    }
    const named = raw.object.get("name");
    if ((named != null) != std.mem.eql(u8, policy.mode, "named")) {
        return ToolChoiceDefect.NotThePolicy;
    }
    if (named) |value| {
        if (value != .string or value.string.len == 0) return ToolChoiceDefect.NotThePolicy;
        policy.name = value.string;
    }
    const allowed = raw.object.get("allowed");
    const disallowed = raw.object.get("disallowed");
    if (allowed != null and disallowed != null) return ToolChoiceDefect.NotThePolicy;
    if (allowed) |value| {
        policy.allowed = try stringList(allocator, value);
        policy.has_allowed = true;
    }
    if (disallowed) |value| {
        policy.disallowed = try stringList(allocator, value);
    }
    return policy;
}

fn outputSchemaDefect(raw: std.json.Value) bool {
    if (raw != .object) return true;
    if (raw.object.get("type")) |declared| {
        switch (declared) {
            .string => |name| if (!std.mem.eql(u8, name, "object")) return true,
            .array => |names| {
                if (names.items.len == 0) return true;
                for (names.items) |entry| {
                    if (entry != .string or !std.mem.eql(u8, entry.string, "object")) return true;
                }
            },
            else => return true,
        }
    }
    if (jsonschema.unsupportedKeyword(raw) != null) return true;
    return schemaNodeDefect(raw, .{ .object = raw.object });
}

fn schemaNodeDefect(root: std.json.Value, node: std.json.Value) bool {
    switch (node) {
        .object => |object| {
            if (object.get("$ref")) |reference| {
                if (reference != .string) return true;
                if (!std.mem.startsWith(u8, reference.string, "#")) return true;
                if (!jsonschema.resolvesLocally(root, reference.string)) return true;
            }
            if (object.get("required")) |required| {
                if (required != .array) return true;
                for (required.array.items) |entry| {
                    if (entry != .string) return true;
                }
            }
            if (object.get("properties")) |properties| {
                if (properties != .object) return true;
            }
            var it = object.iterator();
            while (it.next()) |entry| {
                if (std.mem.eql(u8, entry.key_ptr.*, "required")) continue;
                if (std.mem.eql(u8, entry.key_ptr.*, "enum")) continue;
                if (std.mem.eql(u8, entry.key_ptr.*, "const")) continue;
                if (schemaNodeDefect(root, entry.value_ptr.*)) return true;
            }
            return false;
        },
        .array => |items| {
            for (items.items) |entry| {
                if (schemaNodeDefect(root, entry)) return true;
            }
            return false;
        },
        else => return false,
    }
}

fn runEventFeature(declared: []const u8) ?[]const u8 {
    if (std.mem.startsWith(u8, declared, "action.call.")) return "tools";
    if (std.mem.startsWith(u8, declared, "action.permission.")) return "permissions";
    if (std.mem.startsWith(u8, declared, "user.input.")) return "user_input";
    return null;
}

fn interactionFeature(declared: []const u8) ?[]const u8 {
    if (std.mem.eql(u8, declared, "action.permission.resolve.request") or
        std.mem.eql(u8, declared, "action.permission.resolve.response")) return "permissions";
    if (std.mem.eql(u8, declared, "user.input.resolve.request") or
        std.mem.eql(u8, declared, "user.input.resolve.response") or
        std.mem.eql(u8, declared, "user.input.cancel.request") or
        std.mem.eql(u8, declared, "user.input.cancel.response")) return "user_input";
    return null;
}

fn controlPointer(key: []const u8) []const u8 {
    if (std.mem.eql(u8, key, feature_model_selection)) return "/payload/model_id";
    if (std.mem.eql(u8, key, feature_instructions)) return "/payload/instructions";
    if (std.mem.eql(u8, key, feature_tool_selection)) return "/payload/tool_choice";
    if (std.mem.eql(u8, key, feature_structured_output)) return "/payload/output_schema";
    return "/payload";
}

fn allowsDegraded(payload: std.json.Value, key: []const u8) bool {
    const allowed = member(payload, "allow_degraded_features") orelse return false;
    if (allowed != .array) return false;
    for (allowed.array.items) |entry| {
        if (entry == .string and std.mem.eql(u8, entry.string, key)) return true;
    }
    return false;
}

fn outranks(candidate: Expectation, held: Expectation) bool {
    if (candidate.rung != held.rung) return candidate.rung < held.rung;
    if (!std.mem.eql(u8, candidate.key, held.key)) {
        return std.mem.order(u8, candidate.key, held.key) == .lt;
    }
    return pointerLess(candidate.pointer, held.pointer);
}

fn pointerIndex(segment: []const u8) ?usize {
    return std.fmt.parseUnsigned(usize, segment, 10) catch null;
}

fn pointerLess(a: []const u8, b: []const u8) bool {
    var left = std.mem.tokenizeScalar(u8, a, '/');
    var right = std.mem.tokenizeScalar(u8, b, '/');
    while (true) {
        const here = left.next();
        const there = right.next();
        if (here == null or there == null) return here == null and there != null;
        if (std.mem.eql(u8, here.?, there.?)) continue;
        if (pointerIndex(here.?)) |one| {
            if (pointerIndex(there.?)) |other| return one < other;
        }
        return std.mem.order(u8, here.?, there.?) == .lt;
    }
}

fn conformingRefusal(raised: std.json.Value, expectation: Expectation) bool {
    if (!std.mem.eql(u8, memberString(raised, "code"), expectation.code)) return false;
    const details = member(raised, "details") orelse std.json.Value{ .null = {} };
    if (std.mem.eql(u8, expectation.code, error_unsupported_feature)) {
        if (!std.mem.eql(u8, memberString(details, "feature"), expectation.key)) return false;
    }
    if (expectation.reason.len != 0) {
        if (!std.mem.eql(u8, memberString(details, "reason"), expectation.reason)) return false;
    }
    if (expectation.detail_name.len != 0) {
        if (!std.mem.eql(u8, memberString(details, expectation.detail_name), expectation.detail_value)) return false;
    }
    return true;
}
const error_run_active = "run_active";
const resolution_session_busy = "session_busy";

fn affirmative(level: []const u8) bool {
    return std.mem.eql(u8, level, "native") or
        std.mem.eql(u8, level, "emulated") or
        std.mem.eql(u8, level, "degraded");
}

fn readLimits(declared: ?std.json.Value) ?Limits {
    const limits = declared orelse return null;
    if (limits != .object) return null;
    return .{
        .max_active = boundOf(limits, "max_active_runs_per_session"),
        .max_queued = boundOf(limits, "max_queued_runs_per_session"),
    };
}

fn boundOf(limits: std.json.Value, name: []const u8) ?i64 {
    const value = member(limits, name) orelse return null;
    return switch (value) {
        .integer => |n| n,
        else => null,
    };
}

const Condition = enum { none, busy_session, queue_limit };

fn stateCondition(window: *const Window) Condition {
    if (window.delivery.len != 0 and
        !std.mem.eql(u8, window.delivery, "auto") and
        !std.mem.eql(u8, window.delivery, "queue")) return .none;
    const explicit = std.mem.eql(u8, window.delivery, "queue");
    if (window.offered and (explicit or window.busy_ever)) return .queue_limit;
    if (!window.offered and !explicit and window.busy_ever) return .busy_session;
    return .none;
}

fn isToolSourceKind(kind: []const u8) bool {
    for ([_][]const u8{ "native", "local", "process", "remote", "hosted" }) |name| {
        if (std.mem.eql(u8, name, kind)) return true;
    }
    return false;
}

fn lessThanName(_: void, a: []const u8, b: []const u8) bool {
    return std.mem.order(u8, a, b) == .lt;
}

fn cancellingHoldsItsPlace(entry: std.json.Value) bool {
    return member(entry, "as_of_sequence") == null or member(entry, "queue_position") != null;
}

fn resumeSequence(recovery: ?*Recovery, run_id: []const u8, as_of: ?u64) u64 {
    if (as_of) |value| return value + 1;
    if (recovery) |found| {
        if (!found.gap and found.cursor_set and (found.run.len == 0 or std.mem.eql(u8, found.run, run_id))) {
            return found.cursor + 1;
        }
    }
    return 1;
}

fn admissionFor(status: []const u8) []const u8 {
    if (std.mem.eql(u8, status, "queued")) return "queued";
    return "started";
}

fn effectiveFor(status: []const u8) []const u8 {
    if (std.mem.eql(u8, status, "queued")) return "queue";
    return "start";
}

fn codesOf(allocator: std.mem.Allocator, trace: []const u8) ![]const []const u8 {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, trace, .{});
    defer parsed.deinit();

    var machine = Machine.init(allocator);
    defer machine.deinit();
    for (parsed.value.array.items, 0..) |envelope, index| try machine.apply(index, envelope);
    try machine.close();

    var out = std.ArrayList([]const u8).empty;
    for (machine.diagnostics.items) |diagnostic| try out.append(allocator, diagnostic.code);
    return out.toOwnedSlice(allocator);
}

fn expectCodes(trace: []const u8, want: []const []const u8) !void {
    const allocator = std.testing.allocator;
    const got = try codesOf(allocator, trace);
    defer allocator.free(got);
    const matched = want.len == got.len and blk: {
        for (want, got) |expected, actual| {
            if (!std.mem.eql(u8, expected, actual)) break :blk false;
        }
        break :blk true;
    };
    if (!matched) {
        std.debug.print("\nwant:", .{});
        for (want) |code| std.debug.print(" {s}", .{code});
        std.debug.print("\ngot: ", .{});
        for (got) |code| std.debug.print(" {s}", .{code});
        std.debug.print("\n", .{});
        return error.DiagnosticsDiffer;
    }
}

const admitted =
    \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
    \\{"session_id":"s","accepted":true,"run_id":"run","admission":"started",
    \\"effective_delivery":"start","status":"running"}}
;

test "an admitted run that starts and completes raises nothing" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"content.delta","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"run.completed","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{});
}

test "the sequence must be contiguous in both directions" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"content.delta","id":"e2","run_id":"run","session_id":"s","sequence":4,"payload":{}},
        \\{"type":"run.completed","id":"e3","run_id":"run","session_id":"s","sequence":5,"payload":{}}]
    , &.{"sequence_gap"});

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"content.delta","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"content.delta","id":"e3","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"run.completed","id":"e4","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{"sequence_regression"});
}

test "a run settles once, and nothing follows the settlement" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"run.completed","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{"duplicate_run_terminal"});

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}},
        \\{"type":"content.delta","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{}}]
    , &.{"event_after_terminal"});
}

test "an admitted run owes a start and a terminal" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\]
    , &.{ "missing_run_started", "missing_run_terminal" });

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}}]
    , &.{"missing_run_terminal"});
}

test "a cancellation settles the run it was accepted for" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.cancelled","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}}]
    , &.{"illegal_run_transition"});

    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.cancel.request","id":"c1","payload":{"session_id":"s","run_id":"run"}},
        \\{"type":"run.cancel.response","id":"c2","in_reply_to":"c1","payload":{"session_id":"s","run_id":"run","accepted":true}}]
    , &.{"cancel_not_settled"});
}

test "an admission must name a run and resolve to one shape" {
    try expectCodes(
        \\[{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"run","admission":"started",
        \\"effective_delivery":"queue","status":"running"}}]
    , &.{"illegal_run_transition"});

    try expectCodes(
        \\[{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":false,"error":{"code":"run_active"}}}]
    , &.{"illegal_run_transition"});
}

test "a run event with no admission behind it is a transition the trace never took" {
    try expectCodes(
        \\[{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{}}]
    , &.{"illegal_run_transition"});
}

test "an envelope id is spent once" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.completed","id":"e1","run_id":"run","session_id":"s","sequence":2,"payload":{}}]
    , &.{ "duplicate_envelope_id", "missing_run_terminal" });
}

test "a status update may only move where the lifecycle allows" {
    try expectCodes(
        \\[
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"run.status.updated","id":"e2","run_id":"run","session_id":"s","sequence":2,"payload":{"status":"waiting_for_input"}},
        \\{"type":"run.status.updated","id":"e3","run_id":"run","session_id":"s","sequence":3,"payload":{"status":"queued"}},
        \\{"type":"run.completed","id":"e4","run_id":"run","session_id":"s","sequence":4,"payload":{}}]
    , &.{"illegal_run_transition"});
}

const queue_capabilities =
    \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
    \\{"session.message.delivery.queue":{"level":"native"}},
    \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}}
;

test "a second run may only join a busy session as a reservation the endpoint offers" {
    try expectCodes(
        \\[{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q2","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"started","effective_delivery":"start","status":"running"}}]
    , &.{ "illegal_run_transition", "missing_run_terminal", "missing_run_started", "missing_run_terminal" });
}

test "an explicit queue request is admitted as a reservation or not at all" {
    try expectCodes(
        \\[{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"queue"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}}]
    , &.{ "unavailable_capability", "illegal_run_transition", "missing_run_started", "missing_run_terminal" });
}

test "an auto submission a busy session reserved must say what produced the reservation" {
    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\,
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q0","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "illegal_run_transition", "missing_run_terminal", "missing_run_started", "missing_run_terminal" });
}

test "a later admission may not publish while an earlier one is still open" {
    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\,
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q2","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"queued","effective_delivery":"queue","status":"queued"}},
        \\{"type":"run.started","id":"e2","run_id":"b","session_id":"s","sequence":1,"payload":{}}]
    , &.{ "queue_order_violation", "missing_run_terminal", "missing_run_terminal" });
}

test "a queue is advertised with a bound a submission could reach" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}}}}]
    , &.{"undisclosed_queue_limit"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":2,"max_queued_runs_per_session":2}}}]
    , &.{"undisclosed_queue_limit"});

    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\]
    , &.{});
}

test "a reservation past the disclosed bound is the bound exceeded" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":9,"max_queued_runs_per_session":1}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}},
        \\{"type":"session.message.submit.response","id":"r2","in_reply_to":"q2","payload":
        \\{"session_id":"s","accepted":true,"run_id":"b","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "queue_limit_exceeded", "missing_run_started", "missing_run_terminal", "missing_run_started", "missing_run_terminal" });
}

test "a refusal at a bound the session had not reached reports a bound that does not exist" {
    try expectCodes(
        \\[
    ++ queue_capabilities ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.request","id":"q2","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q2","payload":{"error":{"code":"run_active"}}}]
    , &.{ "queue_limit_exceeded", "missing_run_terminal" });
}

test "a sibling that opens after a window moves the bound that window is judged against" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":1}}},
        \\{"type":"session.message.submit.request","id":"q0","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r0","in_reply_to":"q0","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.request","id":"q2","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q1","payload":{"error":{"code":"run_active"}}}]
    , &.{"missing_run_terminal"});
}

test "an outstanding sibling moves the bound a refusal is judged against" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"native"}},
        \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}},
        \\{"type":"session.message.submit.request","id":"q1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.request","id":"q2","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.request","id":"q3","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q2","payload":{"error":{"code":"run_active"}}}]
    , &.{ "missing_run_started", "missing_run_terminal" });
}

test "a bound below one is no bound at all" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":-1}}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"}]}},
        \\{"type":"session.open.response","id":"o2","in_reply_to":"o1","capability_revision":"v1","payload":{"session_id":"s"}}]
    , &.{});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":0}}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":1}}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"},{"id":"b","kind":"process"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{"unavailable_capability"});
}

test "an open records what it provided even when another surface refused" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tools.provide":{"level":"native"}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"subscribe":true,"tools":[{"name":"echo"}]}},
        \\{"type":"session.open.response","id":"o2","in_reply_to":"o1","capability_revision":"v1","payload":{"session_id":"s"}},
        \\{"type":"capabilities.updated","id":"k2","capability_revision":"v2","payload":{"previous_revision":"v1"}},
        \\{"type":"capabilities.response","id":"k3","capability_revision":"v2","payload":{"features":
        \\{"action.tools.provide":{"level":"native"}},"tools":[{"name":"echo"}]}}]
    , &.{ "unavailable_capability", "duplicate_tool_name" });
}

test "a rejected submission is one refusal, not a verdict on the controls it carried" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","model_id":"m"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1",
        \\"payload":{"session_id":"s","accepted":false}}]
    , &.{"illegal_run_transition"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","model_id":"m"}},
        \\{"type":"error.response","id":"r1","in_reply_to":"q1","payload":{"error":{"code":"internal_error"}}}]
    , &.{"unavailable_capability"});
}

test "a catalog query is not a control this machine judges the refusal of" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tools.list":{"level":"native"}}}},
        \\{"type":"action.tools.list.request","id":"t1","capability_revision":"v1","payload":{"session_id":"s"}},
        \\{"type":"error.response","id":"t2","in_reply_to":"t1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"action.tools.list"}}}}]
    , &.{});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"action.tools.list.request","id":"t1","capability_revision":"v1","payload":{"session_id":"s"}},
        \\{"type":"error.response","id":"t2","in_reply_to":"t1","payload":{"error":{"code":"internal_error"}}}]
    , &.{"unavailable_capability"});
}

test "a settled run reports the settlement it broke, not the capability it also wanted" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":2,"capability_revision":"v1","payload":{}},
        \\{"type":"action.call.requested","id":"e3","run_id":"run","session_id":"s","sequence":3,"capability_revision":"v1",
        \\"payload":{"call_id":"c","name":"echo"}}]
    , &.{"event_after_terminal"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
    ++ admitted ++
        \\,
        \\{"type":"run.started","id":"e1","run_id":"run","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"action.call.requested","id":"e3","run_id":"run","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"call_id":"c","name":"echo"}},
        \\{"type":"run.completed","id":"e2","run_id":"run","session_id":"s","sequence":3,"capability_revision":"v1","payload":{}}]
    , &.{"unavailable_capability"});
}

test "a defect on one surface speaks before a bound the other surface merely reached" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":1}},
        \\"action.tools.provide":{"level":"native"}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"},{"id":"b","kind":"process"}],
        \\"tools":[{"name":"echo"},{"name":"echo"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{"duplicate_tool_name"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":1}},
        \\"action.tools.provide":{"level":"native"}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"},{"id":"b","kind":"process"}],
        \\"tools":[{"name":"echo"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{"unavailable_capability"});
}

test "a bound the endpoint honoured is not raised against the open it admitted" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],
        \\"limits":{"transports":["process"]}}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"http"}]}},
        \\{"type":"session.open.response","id":"o2","in_reply_to":"o1","capability_revision":"v1","payload":{"session_id":"s"}}]
    , &.{});
}

test "the layer that wins a feature wins its mode too, including the mode it omits" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"layers":{
        \\"alpha":{"features":{"run.model_selection":{"level":"native","mode":"session_mutation"}}},
        \\"beta":{"features":{"session.message.delivery.queue":{"level":"native"}}}},
        \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.request","id":"q2","capability_revision":"v1",
        \\"payload":{"session_id":"s","delivery":"auto","model_id":"m"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q2","payload":{"error":{"code":"run_active"}}}]
    , &.{"missing_run_terminal"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"layers":{
        \\"alpha":{"features":{"run.model_selection":{"level":"native"}}},
        \\"beta":{"features":{"run.model_selection":{"level":"native","mode":"session_mutation"},
        \\"session.message.delivery.queue":{"level":"native"}}}},
        \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"payload":{}},
        \\{"type":"session.message.submit.request","id":"q2","capability_revision":"v1",
        \\"payload":{"session_id":"s","delivery":"auto","model_id":"m"}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q2","payload":{"error":{"code":"run_active"}}}]
    , &.{ "queue_limit_exceeded", "missing_run_terminal" });
}

test "an open surface's refusal does not answer for the queue" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"message":{},"tools":[{"name":"echo"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{ "unavailable_capability", "illegal_run_transition", "missing_run_terminal" });
}

test "a compound open attributes the model the message asked for, judged or not" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"message":{"model_id":"mx"}}},
        \\{"type":"session.open.response","id":"o2","in_reply_to":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"current_model_id":"m0","active_runs":[{"run_id":"r1","status":"running"}]}},
        \\{"type":"run.started","id":"e1","run_id":"r1","session_id":"s","sequence":1,"capability_revision":"v1",
        \\"payload":{"model_id":"mx"}},
        \\{"type":"run.completed","id":"e2","run_id":"r1","session_id":"s","sequence":2,"capability_revision":"v1","payload":{}}]
    , &.{"unavailable_capability"});
}

test "a transports list naming no kind at all is no bound" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"transports":[]}}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"transports":["carrier-pigeon"]}}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"process"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{});
}

test "an update continues the revision it replaces, and introduces a different one" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"capabilities.updated","id":"k2","capability_revision":"v2","payload":{"previous_revision":"v1"}}]
    , &.{});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"capabilities.updated","id":"k2","capability_revision":"v2","payload":{"previous_revision":"v9"}}]
    , &.{"stale_capability_revision"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"capabilities.updated","id":"k2","capability_revision":"v1","payload":{"previous_revision":"v1"}}]
    , &.{"stale_capability_revision"});
}

test "an output schema referring to a definition it does not carry is a schema nothing can satisfy" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"$ref":"#/$defs/missing"}}}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1","payload":{}}]
    , &.{"unsatisfiable_control"});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"$ref":"#/$defs/ok"}},
        \\"$defs":{"ok":{"type":"integer"}}}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":1}}}]
    , &.{});
}

test "a refusal naming the tool the filter could not resolve discharges the expectation" {
    const advertised =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.tool_selection":{"level":"native"}},"tools":[{"name":"echo"}]}}
    ;
    const submitted =
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","tool_choice":{"mode":"auto","allowed":["ghost"]}}}
    ;
    try expectCodes(
        \\[
    ++ advertised ++
        \\,
    ++ submitted ++
        \\,
        \\{"type":"error.response","id":"x1","in_reply_to":"q1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"run.tool_selection","reason":"unsatisfiable","tool":"ghost"}}}}]
    , &.{});

    try expectCodes(
        \\[
    ++ advertised ++
        \\,
    ++ submitted ++
        \\,
        \\{"type":"error.response","id":"x1","in_reply_to":"q1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"run.tool_selection","reason":"unsatisfiable","tool":""}}}}]
    , &.{"unsatisfiable_control"});
}

test "a fixed result is compared exactly, past the width a double carries" {
    const descriptor =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native","constraints":{"fixed_result":{"n":9007199254740993}}}}}}
    ;
    const submitted =
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object"}}}
    ;
    const admitted_run =
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}}
    ;
    try expectCodes(
        \\[
    ++ descriptor ++ "," ++ submitted ++ "," ++ admitted_run ++
        \\,
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":9007199254740992.0}}}]
    , &.{"unapplied_control"});

    try expectCodes(
        \\[
    ++ descriptor ++ "," ++ submitted ++ "," ++ admitted_run ++
        \\,
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":9007199254740993}}}]
    , &.{});
}

test "an escaped pointer token resolves to the member whose name carries the slash" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"$ref":"#/$defs/a~1b~0c"}},
        \\"$defs":{"a/b~c":{"type":"integer"}}}}},
        \\{"type":"error.response","id":"x1","in_reply_to":"q1","payload":{"error":{"code":"internal_error"}}}]
    , &.{});
}

test "an open past the tool count the endpoint disclosed is refused for that bound" {
    const bounded =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tools.provide":{"level":"native","limits":{"max_tools":1}}}}}
    ;
    const opened =
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"one"},{"name":"two"}]}}
    ;
    try expectCodes(
        \\[
    ++ bounded ++ "," ++ opened ++
        \\,
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"action.tools.provide","reason":"unsatisfiable","tool":"two"}}}}]
    , &.{});

    try expectCodes(
        \\[
    ++ bounded ++ "," ++ opened ++
        \\,
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{"unavailable_capability"});
}

test "a magnitude past what an i128 holds is not an exact integer" {
    try std.testing.expect(integralFloat(0x1p127) == null);
    try std.testing.expect(integralFloat(-0x1p127).? == std.math.minInt(i128));
    try std.testing.expect(integralFloat(0x1p126).? == 1 << 126);
    try std.testing.expect(integralFloat(1.5) == null);
    try std.testing.expect(integralFloat(std.math.inf(f64)) == null);

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native","constraints":{"fixed_result":
        \\{"n":170141183460469231731687303715884105728.0}}}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object"}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":170141183460469231731687303715884105728.0}}}]
    , &.{});
}

test "the bound that speaks is the one earliest in the payload, counting indices as numbers" {
    try std.testing.expect(pointerLess("/payload/tools/2", "/payload/tools/10"));
    try std.testing.expect(!pointerLess("/payload/tools/10", "/payload/tools/2"));
    try std.testing.expect(pointerLess("/payload/tool_sources/0/kind", "/payload/tool_sources/2"));
    try std.testing.expect(pointerLess("/payload/tool_sources/2", "/payload/tool_sources/2/kind"));

    const bounded =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],
        \\"limits":{"max_sources":2,"transports":["process"]}}}}}
    ;
    const opened =
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"a","kind":"hosted"},{"id":"b","kind":"process"},{"id":"c","kind":"process"}]}}
    ;
    try expectCodes(
        \\[
    ++ bounded ++ "," ++ opened ++
        \\,
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"action.tool_sources.attach","reason":"unsatisfiable","source":"a"}}}}]
    , &.{});

    try expectCodes(
        \\[
    ++ bounded ++ "," ++ opened ++
        \\,
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"action.tool_sources.attach","reason":"unsatisfiable","source":"c"}}}}]
    , &.{"unavailable_capability"});

    const wide =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],
        \\"limits":{"max_sources":10,"transports":["process"]}}}}}
    ;
    const many =
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"s0","kind":"process"},{"id":"s1","kind":"process"},{"id":"s2","kind":"hosted"},{"id":"s3","kind":"process"},{"id":"s4","kind":"process"},{"id":"s5","kind":"process"},{"id":"s6","kind":"process"},{"id":"s7","kind":"process"},{"id":"s8","kind":"process"},{"id":"s9","kind":"process"},{"id":"s10","kind":"process"}]}}
    ;
    try expectCodes(
        \\[
    ++ wide ++ "," ++ many ++
        \\,
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"action.tool_sources.attach","reason":"unsatisfiable","source":"s2"}}}}]
    , &.{});

    try expectCodes(
        \\[
    ++ wide ++ "," ++ many ++
        \\,
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"unsupported_feature",
        \\"details":{"feature":"action.tool_sources.attach","reason":"unsatisfiable","source":"s10"}}}}]
    , &.{"unavailable_capability"});
}

test "a completion is judged against the schema its own definitions describe" {
    const declared =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}}
    ;
    const submitted =
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","required":["n"],
        \\"properties":{"n":{"$ref":"#/$defs/count"}},"$defs":{"count":{"type":"integer"}}}}}
    ;
    const started =
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}}
    ;
    try expectCodes(
        \\[
    ++ declared ++ "," ++ submitted ++ "," ++ started ++
        \\,
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":1}}}]
    , &.{});

    try expectCodes(
        \\[
    ++ declared ++ "," ++ submitted ++ "," ++ started ++
        \\,
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":"one"}}}]
    , &.{"unapplied_control"});
}

test "a reservation the endpoint never advertised a queue for is the capability missing" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "unavailable_capability", "missing_run_started", "missing_run_terminal" });

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"degraded"}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s","delivery":"auto"}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "undisclosed_queue_limit", "degraded_without_optin", "missing_run_started", "missing_run_terminal" });

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"session.message.delivery.queue":{"level":"degraded"}},
        \\"limits":{"max_active_runs_per_session":5,"max_queued_runs_per_session":2}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","allow_degraded_features":["session.message.delivery.queue"]}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"queued","effective_delivery":"queue","status":"queued"}}]
    , &.{ "missing_run_started", "missing_run_terminal" });
}

test "a schema this interpreter cannot fully evaluate is refused, not waved through" {
    {
        var plain = try std.json.parseFromSlice(std.json.Value, std.testing.allocator,
            \\{"type":"object","properties":{"n":{"type":"string"}}}
        , .{});
        defer plain.deinit();
        try std.testing.expect(jsonschema.unsupportedKeyword(plain.value) == null);
    }
    {
        var bounded = try std.json.parseFromSlice(std.json.Value, std.testing.allocator,
            \\{"type":"object","properties":{"n":{"type":"string","maxLength":2}}}
        , .{});
        defer bounded.deinit();
        try std.testing.expectEqualStrings("maxLength", jsonschema.unsupportedKeyword(bounded.value).?);
    }

    const declared =
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}}
    ;
    const tail =
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":"xxxxxxxx"}}}]
    ;
    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"type":"string","maxLength":2}}}}},
    ++ tail
    , &.{"unsatisfiable_control"});

    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"type":"string"}}}}},
    ++ tail
    , &.{});

    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"type":"string","pattern":"^x+$"}}}}},
    ++ tail
    , &.{"unsatisfiable_control"});

    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"items":[{"type":"string"}]}}}}},
    ++ tail
    , &.{"unsatisfiable_control"});

    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"enum":"notalist"}}}}},
    ++ tail
    , &.{"unapplied_control"});
}

test "an escaped reference resolves the same way on both sides of the boundary" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"$ref":"#/$defs/a~1b"}},
        \\"$defs":{"a/b":{"type":"integer"}}}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":1}}}]
    , &.{});

    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"run.structured_output":{"level":"native"}}}},
        \\{"type":"session.message.submit.request","id":"q1","capability_revision":"v1","payload":{"session_id":"s",
        \\"delivery":"auto","output_schema":{"type":"object","properties":{"n":{"$ref":"#/$defs/a~1b"}},
        \\"$defs":{"a/b":{"type":"integer"}}}}},
        \\{"type":"session.message.submit.response","id":"r1","in_reply_to":"q1","capability_revision":"v1","payload":
        \\{"session_id":"s","accepted":true,"run_id":"a","admission":"started","effective_delivery":"start","status":"running"}},
        \\{"type":"run.started","id":"e1","run_id":"a","session_id":"s","sequence":1,"capability_revision":"v1","payload":{}},
        \\{"type":"run.completed","id":"e2","run_id":"a","session_id":"s","sequence":2,"capability_revision":"v1",
        \\"payload":{"result":{"n":"one"}}}]
    , &.{"unapplied_control"});
}

test "a defect the port does not name still silences the bound beside it" {
    const bounded =
        \\{"type":"protocol.initialize.request","id":"i1","payload":{"participant":{"id":"control"}}},
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tools.provide":{"level":"native","limits":{"max_tools":1}}}}}
    ;
    const refused =
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    ;
    try expectCodes(
        \\[
    ++ bounded ++
        \\,
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"one","execution_owner":"someone-else"},{"name":"two"}]}},
    ++ refused
    , &.{});

    try expectCodes(
        \\[
    ++ bounded ++
        \\,
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"one","source":"nowhere"},{"name":"two"}]}},
    ++ refused
    , &.{});

    try expectCodes(
        \\[
    ++ bounded ++
        \\,
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"one","execution_owner":"control"},{"name":"two"}]}},
    ++ refused
    , &.{"unavailable_capability"});
}

test "an attachment reusing an id the session already resolves silences the bound" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tool_sources.attach":{"level":"native","modes":["session_open"],"limits":{"max_sources":1}}},
        \\"sources":[{"id":"already","kind":"process"}]}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tool_sources":[{"id":"already","kind":"process"},{"id":"fresh","kind":"process"}]}},
        \\{"type":"error.response","id":"o2","in_reply_to":"o1","payload":{"error":{"code":"internal_error"}}}]
    , &.{});
}

fn countingWith(allocator: std.mem.Allocator, trace: []const u8, types: []const PackedType) !usize {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, trace, .{});
    defer parsed.deinit();
    var machine = Machine.init(allocator);
    defer machine.deinit();
    machine.packs = .{ .types = types };
    for (parsed.value.array.items, 0..) |envelope, index| try machine.apply(index, envelope);
    try machine.close();
    var stale: usize = 0;
    for (machine.diagnostics.items) |diagnostic| {
        if (std.mem.eql(u8, diagnostic.code, code_stale_capability_revision)) stale += 1;
    }
    return stale;
}

test "a packed exchange is correlated by the reply its pack declares" {
    const storage = [_]PackedType{
        .{ .name = "x", .role = "request", .response = "x.done" },
        .{ .name = "x.done", .role = "response" },
    };
    const trace =
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":{}}},
        \\{"type":"x","id":"q1","capability_revision":"v1","payload":{}},
        \\{"type":"x.done","id":"r1","in_reply_to":"q1","capability_revision":"v2","payload":{}}]
    ;
    const allocator = std.testing.allocator;
    const unwired = try countingWith(allocator, trace, &.{});
    const wired = try countingWith(allocator, trace, &storage);
    try std.testing.expectEqual(unwired + 1, wired);
}

test "a session records a provided name once, however many opens supply it" {
    try expectCodes(
        \\[{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tools.provide":{"level":"native"}}}},
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"echo"}]}},
        \\{"type":"session.open.response","id":"o2","in_reply_to":"o1","capability_revision":"v1","payload":{"session_id":"s"}},
        \\{"type":"session.open.request","id":"o3","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"echo"}]}},
        \\{"type":"session.open.response","id":"o4","in_reply_to":"o3","capability_revision":"v1","payload":{"session_id":"s"}},
        \\{"type":"capabilities.updated","id":"k2","capability_revision":"v2","payload":{"previous_revision":"v1"}},
        \\{"type":"capabilities.response","id":"k3","capability_revision":"v2","payload":{"features":
        \\{"action.tools.provide":{"level":"native"}},"tools":[{"name":"echo"}]}}]
    , &.{"duplicate_tool_name"});
}

test "the earliest defect speaks even when this port has no name for it" {
    const declared =
        \\{"type":"protocol.initialize.request","id":"i1","payload":{"participant":{"id":"control"}}},
        \\{"type":"capabilities.response","id":"k1","capability_revision":"v1","payload":{"features":
        \\{"action.tools.provide":{"level":"native"}}}}
    ;
    const answered =
        \\{"type":"session.open.response","id":"o2","in_reply_to":"o1","capability_revision":"v1","payload":{"session_id":"s"}}]
    ;
    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"a","execution_owner":"mallory"},{"name":"a"}]}},
    ++ answered
    , &.{});

    try expectCodes(
        \\[
    ++ declared ++
        \\,
        \\{"type":"session.open.request","id":"o1","capability_revision":"v1","payload":{"session_id":"s",
        \\"tools":[{"name":"a"},{"name":"a"},{"name":"b","execution_owner":"mallory"}]}},
    ++ answered
    , &.{"duplicate_tool_name"});
}
