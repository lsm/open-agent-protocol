const std = @import("std");

pub const PROTOCOL = "open-agent-protocol";
pub const VERSION = "0.1";
pub const PROFILE = "open-agent-protocol.agent-control-core";

pub const Role = enum {
    system,
    developer,
    user,
    assistant,
    tool,
};

pub const SupportLevel = enum {
    native,
    emulated,
    degraded,
    unavailable,
};

pub const SessionStatus = enum {
    idle,
    queued,
    running,
    waiting_for_input,
    closed,
    @"error",
};

pub const RunStatus = enum {
    queued,
    running,
    waiting_for_input,
    cancelling,
    completed,
    failed,
    cancelled,
};

pub const RequestedDelivery = enum {
    auto,
    queue,
    steer,
    btw,
};

pub const EffectiveDelivery = enum {
    start,
    queue,
    steer,
    btw,
};

pub const Admission = enum {
    started,
    queued,
    steered,
    side_started,
    rejected,
};

pub const EmittedErrorCode = enum {
    invalid_request,
    unsupported_feature,
    capability_degraded,
    stale_capabilities,
    model_not_found,
    session_not_found,
    session_busy,
    run_not_found,
    run_already_terminal,
    provider_error,
    credential_missing,
    credential_expired,
    credential_rejected,
    internal_error,

    pub fn text(self: EmittedErrorCode) []const u8 {
        return @tagName(self);
    }
};

pub const RunControl = enum {
    model_id,
    instructions,
    tool_choice,
    output_schema,

    pub fn capabilityKey(self: RunControl) []const u8 {
        return switch (self) {
            .model_id => "run.model_selection",
            .instructions => "run.instructions",
            .tool_choice => "run.tool_selection",
            .output_schema => "run.structured_output",
        };
    }
};

pub const DetailEntry = struct {
    key: []const u8,
    value: []const u8,
};

pub fn ProtocolErrorOf(comptime Code: type) type {
    return struct {
        const Self = @This();

        code: Code,
        message: []const u8,
        retriable: ?bool = null,
        details: []const DetailEntry = &.{},

        pub fn detail(self: *const Self, key: []const u8) ?[]const u8 {
            for (self.details) |entry| {
                if (std.mem.eql(u8, entry.key, key)) return entry.value;
            }
            return null;
        }

        pub fn deinit(self: *Self, allocator: std.mem.Allocator) void {
            if (Code == []const u8) allocator.free(self.code);
            allocator.free(self.message);
            for (self.details) |entry| {
                allocator.free(entry.key);
                allocator.free(entry.value);
            }
            allocator.free(self.details);
        }
    };
}

pub const ProtocolError = ProtocolErrorOf([]const u8);

pub const ReasoningPart = struct {
    text: []const u8,
    carry: ?[]const u8 = null,

    pub fn deinit(self: *ReasoningPart, allocator: std.mem.Allocator) void {
        allocator.free(self.text);
        if (self.carry) |value| allocator.free(value);
    }
};

pub const ContentPart = union(enum) {
    text: []const u8,
    reasoning: ReasoningPart,
    tool_call: ToolCallPart,
    tool_result: ToolResultPart,

    pub fn deinit(self: *ContentPart, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .text => |value| allocator.free(value),
            .reasoning => |*part| part.deinit(allocator),
            .tool_call => |*part| part.deinit(allocator),
            .tool_result => |*part| part.deinit(allocator),
        }
    }
};

pub const ToolCallPart = struct {
    tool_call_id: []const u8,
    name: []const u8,
    arguments_json: []const u8,
    arguments_partial: ?[]const u8 = null,
    carry: ?[]const u8 = null,

    pub fn deinit(self: *ToolCallPart, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.name);
        allocator.free(self.arguments_json);
        if (self.arguments_partial) |value| allocator.free(value);
        if (self.carry) |value| allocator.free(value);
    }
};

pub const ToolResultPart = struct {
    tool_call_id: []const u8,
    result_json: []const u8,
    is_error: ?bool = null,

    pub fn deinit(self: *ToolResultPart, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.result_json);
    }
};

pub const Content = union(enum) {
    text: []const u8,
    parts: []ContentPart,

    pub fn deinit(self: *Content, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .text => |value| allocator.free(value),
            .parts => |parts| {
                for (parts) |*part| part.deinit(allocator);
                allocator.free(parts);
            },
        }
    }
};

pub const Message = struct {
    id: ?[]const u8 = null,
    role: Role,
    content: Content,

    pub fn deinit(self: *Message, allocator: std.mem.Allocator) void {
        if (self.id) |id| allocator.free(id);
        self.content.deinit(allocator);
    }
};

pub const Usage = struct {
    input_tokens: ?u64 = null,
    output_tokens: ?u64 = null,
    total_tokens: ?u64 = null,

    pub fn isEmpty(self: Usage) bool {
        return self.input_tokens == null and self.output_tokens == null and self.total_tokens == null;
    }
};

pub const Endpoint = struct {
    id: []const u8,
    name: ?[]const u8 = null,
    version: ?[]const u8 = null,
    adapter: ?[]const u8 = null,

    pub fn deinit(self: *Endpoint, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        if (self.name) |value| allocator.free(value);
        if (self.version) |value| allocator.free(value);
        if (self.adapter) |value| allocator.free(value);
    }
};

pub const Participant = struct {
    id: []const u8,
    name: ?[]const u8 = null,
    version: ?[]const u8 = null,

    pub fn deinit(self: *Participant, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        if (self.name) |value| allocator.free(value);
        if (self.version) |value| allocator.free(value);
    }
};

pub const Feature = struct {
    key: []const u8,
    level: SupportLevel,
    scope: ?[]const u8 = null,
    reason: ?[]const u8 = null,
    modes: []const []const u8 = &.{},
    constraints_json: ?[]const u8 = null,
    limits_json: ?[]const u8 = null,

    pub fn deinit(self: *Feature, allocator: std.mem.Allocator) void {
        allocator.free(self.key);
        if (self.scope) |value| allocator.free(value);
        if (self.reason) |value| allocator.free(value);
        freeStringList(allocator, self.modes);
        if (self.constraints_json) |value| allocator.free(value);
        if (self.limits_json) |value| allocator.free(value);
    }
};

pub const Degradation = struct {
    feature: []const u8,
    from: ?SupportLevel = null,
    to: SupportLevel,
    reason: []const u8,

    pub fn deinit(self: *Degradation, allocator: std.mem.Allocator) void {
        allocator.free(self.feature);
        allocator.free(self.reason);
    }
};

pub const Binding = struct {
    kind: []const u8,
    serialization: ?[]const u8 = null,

    pub fn deinit(self: *Binding, allocator: std.mem.Allocator) void {
        allocator.free(self.kind);
        if (self.serialization) |value| allocator.free(value);
    }
};

pub const ToolSourceDescriptor = struct {
    id: []const u8,
    kind: []const u8,
    display_name: ?[]const u8 = null,
    protocol: ?[]const u8 = null,
    endpoint: ?[]const u8 = null,

    pub fn deinit(self: *ToolSourceDescriptor, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        allocator.free(self.kind);
        if (self.display_name) |value| allocator.free(value);
        if (self.protocol) |value| allocator.free(value);
        if (self.endpoint) |value| allocator.free(value);
    }
};

pub const ToolDefinition = struct {
    name: []const u8,
    description: ?[]const u8 = null,
    input_schema_json: []const u8,
    execution_owner: []const u8,
    source: ?[]const u8 = null,
    features: []Feature = &.{},

    pub fn deinit(self: *ToolDefinition, allocator: std.mem.Allocator) void {
        allocator.free(self.name);
        if (self.description) |value| allocator.free(value);
        allocator.free(self.input_schema_json);
        allocator.free(self.execution_owner);
        if (self.source) |value| allocator.free(value);
        for (self.features) |*entry| entry.deinit(allocator);
        allocator.free(self.features);
    }
};

pub const ToolsListRequest = struct {
    session_id: ?[]const u8 = null,
    allow_degraded_features: []const []const u8 = &.{},

    pub fn deinit(self: *ToolsListRequest, allocator: std.mem.Allocator) void {
        if (self.session_id) |value| allocator.free(value);
        freeStringList(allocator, self.allow_degraded_features);
    }

    pub fn allowsDegraded(self: *const ToolsListRequest, key: []const u8) bool {
        return containsKey(self.allow_degraded_features, key);
    }
};

pub const ToolsListResponse = struct {
    session_id: ?[]const u8 = null,
    sources: []ToolSourceDescriptor = &.{},
    tools: []ToolDefinition = &.{},

    pub fn deinit(self: *ToolsListResponse, allocator: std.mem.Allocator) void {
        if (self.session_id) |value| allocator.free(value);
        for (self.sources) |*entry| entry.deinit(allocator);
        allocator.free(self.sources);
        for (self.tools) |*entry| entry.deinit(allocator);
        allocator.free(self.tools);
    }
};

pub const InputAnswer = struct {
    question_id: []const u8,
    text: ?[]const u8 = null,
    selected_option_ids: []const []const u8 = &.{},

    pub fn deinit(self: *InputAnswer, allocator: std.mem.Allocator) void {
        allocator.free(self.question_id);
        if (self.text) |value| allocator.free(value);
        freeStringList(allocator, self.selected_option_ids);
    }
};

pub const UserInputResolveRequest = struct {
    interaction_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    answers: []InputAnswer,

    pub fn deinit(self: *UserInputResolveRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.interaction_id);
        allocator.free(self.requested_by);
        allocator.free(self.responded_by);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        for (self.answers) |*answer| answer.deinit(allocator);
        allocator.free(self.answers);
    }
};

pub const PermissionResolveRequest = struct {
    interaction_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    granted: bool,
    choice_id: ?[]const u8 = null,
    reason: ?[]const u8 = null,
    updated_arguments_json: ?[]const u8 = null,

    pub fn deinit(self: *PermissionResolveRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.interaction_id);
        allocator.free(self.requested_by);
        allocator.free(self.responded_by);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        if (self.choice_id) |value| allocator.free(value);
        if (self.reason) |value| allocator.free(value);
        if (self.updated_arguments_json) |value| allocator.free(value);
    }
};

pub const InteractionResolveResponse = struct {
    interaction_id: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    accepted: bool,

    pub fn deinit(self: *InteractionResolveResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.interaction_id);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
    }
};

pub const CallResolveRequest = struct {
    interaction_id: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    tool_call_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    started: bool = false,
    result_json: ?[]const u8 = null,
    err: ?ProtocolError = null,

    pub fn deinit(self: *CallResolveRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.interaction_id);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        allocator.free(self.tool_call_id);
        allocator.free(self.requested_by);
        allocator.free(self.responded_by);
        if (self.result_json) |value| allocator.free(value);
        if (self.err) |*value| value.deinit(allocator);
    }
};

pub const CallResolveResponse = struct {
    interaction_id: []const u8,
    session_id: []const u8,
    run_id: []const u8,
    tool_call_id: []const u8,
    accepted: bool,
    reason: ?[]const u8 = null,
    settlement_id: ?[]const u8 = null,

    pub fn deinit(self: *CallResolveResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.interaction_id);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        allocator.free(self.tool_call_id);
        if (self.reason) |value| allocator.free(value);
        if (self.settlement_id) |value| allocator.free(value);
    }
};

pub const InitializeRequest = struct {
    protocol_versions: []const []const u8,
    profiles: []const []const u8,
    participant: ?Participant = null,

    pub fn deinit(self: *InitializeRequest, allocator: std.mem.Allocator) void {
        freeStringList(allocator, self.protocol_versions);
        freeStringList(allocator, self.profiles);
        if (self.participant) |*participant| participant.deinit(allocator);
    }
};

pub const InitializeResponse = struct {
    protocol_version: []const u8,
    profile: []const u8,
    endpoint: Endpoint,

    pub fn deinit(self: *InitializeResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.protocol_version);
        allocator.free(self.profile);
        self.endpoint.deinit(allocator);
    }
};

pub const Limits = struct {
    max_active_runs_per_session: ?u32 = null,
    max_queued_runs_per_session: ?u32 = null,
};

pub const CapabilitiesResponse = struct {
    endpoint: Endpoint,
    protocol_versions: []const []const u8 = &.{},
    profiles: []const []const u8 = &.{},
    bindings: []Binding = &.{},
    features: []Feature = &.{},
    requested_delivery_modes: []const RequestedDelivery = &.{},
    effective_delivery_modes: []const EffectiveDelivery = &.{},
    degradation: []Degradation = &.{},
    tools: []ToolDefinition = &.{},
    sources: []ToolSourceDescriptor = &.{},
    limits: ?Limits = null,

    pub fn deinit(self: *CapabilitiesResponse, allocator: std.mem.Allocator) void {
        self.endpoint.deinit(allocator);
        freeStringList(allocator, self.protocol_versions);
        freeStringList(allocator, self.profiles);
        for (self.bindings) |*binding| binding.deinit(allocator);
        allocator.free(self.bindings);
        for (self.features) |*entry| entry.deinit(allocator);
        allocator.free(self.features);
        allocator.free(self.requested_delivery_modes);
        allocator.free(self.effective_delivery_modes);
        for (self.degradation) |*record| record.deinit(allocator);
        allocator.free(self.degradation);
        for (self.tools) |*tool| tool.deinit(allocator);
        allocator.free(self.tools);
        for (self.sources) |*source| source.deinit(allocator);
        allocator.free(self.sources);
    }

    pub fn feature(self: *const CapabilitiesResponse, key: []const u8) ?Feature {
        for (self.features) |entry| {
            if (std.mem.eql(u8, entry.key, key)) return entry;
        }
        return null;
    }
};

pub const SessionOpenRequest = struct {
    session_id: ?[]const u8 = null,
    subscribe: bool = false,
    message_json: ?[]const u8 = null,
    tools_json: ?[]const u8 = null,
    tool_sources_json: ?[]const u8 = null,
    allow_degraded_features: []const []const u8 = &.{},

    pub fn deinit(self: *SessionOpenRequest, allocator: std.mem.Allocator) void {
        if (self.session_id) |value| allocator.free(value);
        if (self.message_json) |value| allocator.free(value);
        if (self.tools_json) |value| allocator.free(value);
        if (self.tool_sources_json) |value| allocator.free(value);
        freeStringList(allocator, self.allow_degraded_features);
    }

    pub fn allowsDegraded(self: *const SessionOpenRequest, key: []const u8) bool {
        return containsKey(self.allow_degraded_features, key);
    }
};

pub const SessionStateRequest = struct {
    session_id: []const u8,

    pub fn deinit(self: *SessionStateRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
    }
};

pub const ModelsRequest = struct {
    session_id: []const u8,
    allow_degraded_features: []const []const u8 = &.{},

    pub fn allowsDegraded(self: *const ModelsRequest, feature: []const u8) bool {
        for (self.allow_degraded_features) |allowed| {
            if (std.mem.eql(u8, allowed, feature)) return true;
        }
        return false;
    }

    pub fn deinit(self: *ModelsRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        freeStringList(allocator, self.allow_degraded_features);
    }
};

pub const ModelDescriptor = struct {
    id: []const u8,
    display_name: ?[]const u8 = null,
    provider_id: ?[]const u8 = null,
    context_window: ?u64 = null,
    default: bool = false,

    pub fn deinit(self: *ModelDescriptor, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        if (self.display_name) |value| allocator.free(value);
        if (self.provider_id) |value| allocator.free(value);
    }
};

pub const ProviderDescriptor = struct {
    id: []const u8,
    display_name: ?[]const u8 = null,
    wire: ?[]const u8 = null,
    kind: ?[]const u8 = null,
    endpoint: ?[]const u8 = null,
    service_id: ?[]const u8 = null,
    upstream_provider_id: ?[]const u8 = null,

    pub fn deinit(self: *ProviderDescriptor, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        inline for (.{ "display_name", "wire", "kind", "endpoint", "service_id", "upstream_provider_id" }) |name| {
            if (@field(self, name)) |value| allocator.free(value);
        }
    }
};

pub const ModelsResponse = struct {
    session_id: []const u8,
    current_model_id: ?[]const u8 = null,
    models: []ModelDescriptor,
    providers: []ProviderDescriptor = &.{},

    pub fn deinit(self: *ModelsResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        if (self.current_model_id) |value| allocator.free(value);
        for (self.models) |*model| model.deinit(allocator);
        allocator.free(self.models);
        for (self.providers) |*provider| provider.deinit(allocator);
        allocator.free(self.providers);
    }
};

pub const SessionModelSwitchRequest = struct {
    session_id: []const u8,
    model_id: []const u8,
    allow_degraded_features: []const []const u8 = &.{},

    pub fn deinit(self: *SessionModelSwitchRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.model_id);
        freeStringList(allocator, self.allow_degraded_features);
    }
};

pub const SessionModelSwitchResponse = struct {
    session_id: []const u8,
    model_id: []const u8,
    previous_model_id: ?[]const u8 = null,

    pub fn deinit(self: *SessionModelSwitchResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.model_id);
        if (self.previous_model_id) |value| allocator.free(value);
    }
};

pub const ActiveRun = struct {
    run_id: []const u8,
    status: RunStatus,
    relationship: []const u8,
    queue_position: ?u64 = null,
    as_of_sequence: ?u64 = null,
    admitted_submit_requests: []const []const u8 = &.{},
    pending_interactions: []const []const u8 = &.{},
    acknowledged_interactions: []const []const u8 = &.{},

    pub fn deinit(self: *ActiveRun, allocator: std.mem.Allocator) void {
        allocator.free(self.run_id);
        allocator.free(self.relationship);
        freeStringList(allocator, self.admitted_submit_requests);
        freeStringList(allocator, self.pending_interactions);
        freeStringList(allocator, self.acknowledged_interactions);
    }
};

pub const RunPosition = struct {
    run_id: []const u8,
    sequence: u64,
};

pub const SessionCapture = struct {
    admitted_submit_requests: []const []const u8 = &.{},
    settled: []const RunPosition = &.{},
    model_run_sequence: ?RunPosition = null,

    pub fn deinit(self: *SessionCapture, allocator: std.mem.Allocator) void {
        freeStringList(allocator, self.admitted_submit_requests);
        for (self.settled) |entry| allocator.free(entry.run_id);
        allocator.free(self.settled);
        if (self.model_run_sequence) |position| allocator.free(position.run_id);
    }
};

pub const SessionState = struct {
    session_id: []const u8,
    status: SessionStatus,
    active_run_id: ?[]const u8 = null,
    active_runs: []ActiveRun = &.{},
    current_model_id: ?[]const u8 = null,
    transcript_cursor: ?[]const u8 = null,
    updated_at_ms: ?i64 = null,
    sources: []ToolSourceDescriptor = &.{},
    as_of: ?SessionCapture = null,

    pub fn deinit(self: *SessionState, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        if (self.active_run_id) |value| allocator.free(value);
        for (self.active_runs) |*entry| entry.deinit(allocator);
        allocator.free(self.active_runs);
        if (self.current_model_id) |value| allocator.free(value);
        if (self.transcript_cursor) |value| allocator.free(value);
        for (self.sources) |*entry| entry.deinit(allocator);
        allocator.free(self.sources);
        if (self.as_of) |*capture| capture.deinit(allocator);
    }
};

pub const MessageSubmitRequest = struct {
    session_id: []const u8,
    messages: []Message,
    delivery: RequestedDelivery,
    model_id: ?[]const u8 = null,
    instructions: ?[]const u8 = null,
    tool_choice_json: ?[]const u8 = null,
    output_schema_json: ?[]const u8 = null,
    allow_degraded_features: []const []const u8 = &.{},

    pub fn deinit(self: *MessageSubmitRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        for (self.messages) |*message| message.deinit(allocator);
        allocator.free(self.messages);
        if (self.model_id) |value| allocator.free(value);
        if (self.instructions) |value| allocator.free(value);
        if (self.tool_choice_json) |value| allocator.free(value);
        if (self.output_schema_json) |value| allocator.free(value);
        freeStringList(allocator, self.allow_degraded_features);
    }

    pub fn allowsDegraded(self: *const MessageSubmitRequest, key: []const u8) bool {
        return containsKey(self.allow_degraded_features, key);
    }

    pub fn control(self: *const MessageSubmitRequest, which: RunControl) ?[]const u8 {
        return switch (which) {
            .model_id => self.model_id,
            .instructions => self.instructions,
            .tool_choice => self.tool_choice_json,
            .output_schema => self.output_schema_json,
        };
    }
};

pub const MessageSubmitResponse = struct {
    session_id: []const u8,
    accepted: bool,
    submission_id: []const u8,
    requested_delivery: RequestedDelivery,
    effective_delivery: EffectiveDelivery,
    delivery_resolution: ?[]const u8 = null,
    admission: Admission,
    run_id: ?[]const u8 = null,
    status: ?RunStatus = null,
    model_id: ?[]const u8 = null,
    message_ids: []const []const u8 = &.{},

    pub fn deinit(self: *MessageSubmitResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.submission_id);
        if (self.delivery_resolution) |value| allocator.free(value);
        if (self.run_id) |value| allocator.free(value);
        if (self.model_id) |value| allocator.free(value);
        freeStringList(allocator, self.message_ids);
    }
};

pub const RunCancelRequest = struct {
    session_id: []const u8,
    run_id: []const u8,
    reason: ?[]const u8 = null,

    pub fn deinit(self: *RunCancelRequest, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        if (self.reason) |value| allocator.free(value);
    }
};

pub const RunCancelResponse = struct {
    session_id: []const u8,
    run_id: []const u8,
    accepted: bool,
    status: RunStatus,

    pub fn deinit(self: *RunCancelResponse, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
    }
};

pub const RunStarted = struct {
    session_id: []const u8,
    run_id: []const u8,
    model_id: ?[]const u8 = null,
    started_at_ms: ?i64 = null,

    pub fn deinit(self: *RunStarted, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        if (self.model_id) |value| allocator.free(value);
    }
};

pub const RunStatusUpdated = struct {
    session_id: []const u8,
    run_id: []const u8,
    status: RunStatus,
    updated_at_ms: ?i64 = null,

    pub fn deinit(self: *RunStatusUpdated, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
    }
};

pub const ContentDelta = struct {
    session_id: []const u8,
    run_id: []const u8,
    message_id: ?[]const u8 = null,
    part: ContentPart,

    pub fn deinit(self: *ContentDelta, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        if (self.message_id) |value| allocator.free(value);
        self.part.deinit(allocator);
    }
};

pub const RunCompleted = struct {
    session_id: []const u8,
    run_id: []const u8,
    final_response: Message,
    stop_reason: []const u8,
    model_id: ?[]const u8 = null,
    usage: Usage = .{},
    duration_ms: ?u64 = null,

    pub fn deinit(self: *RunCompleted, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        self.final_response.deinit(allocator);
        allocator.free(self.stop_reason);
        if (self.model_id) |value| allocator.free(value);
    }
};

pub const RunFailed = struct {
    session_id: []const u8,
    run_id: []const u8,
    err: ProtocolError,
    usage: Usage = .{},
    duration_ms: ?u64 = null,

    pub fn deinit(self: *RunFailed, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        self.err.deinit(allocator);
    }
};

pub const RunCancelled = struct {
    session_id: []const u8,
    run_id: []const u8,
    reason: ?[]const u8 = null,
    usage: Usage = .{},
    duration_ms: ?u64 = null,

    pub fn deinit(self: *RunCancelled, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.run_id);
        if (self.reason) |value| allocator.free(value);
    }
};

pub const Payload = union(enum) {
    initialize_request: InitializeRequest,
    initialize_response: InitializeResponse,
    capabilities_request: void,
    capabilities_response: CapabilitiesResponse,
    models_request: ModelsRequest,
    models_response: ModelsResponse,
    session_open_request: SessionOpenRequest,
    session_open_response: SessionState,
    session_state_request: SessionStateRequest,
    session_state_response: SessionState,
    session_state_updated: SessionState,
    session_model_switch_request: SessionModelSwitchRequest,
    session_model_switch_response: SessionModelSwitchResponse,
    message_submit_request: MessageSubmitRequest,
    message_submit_response: MessageSubmitResponse,
    run_cancel_request: RunCancelRequest,
    run_cancel_response: RunCancelResponse,
    run_started: RunStarted,
    run_status_updated: RunStatusUpdated,
    content_delta: ContentDelta,
    run_completed: RunCompleted,
    run_failed: RunFailed,
    run_cancelled: RunCancelled,
    user_input_resolve_request: UserInputResolveRequest,
    user_input_resolve_response: InteractionResolveResponse,
    permission_resolve_request: PermissionResolveRequest,
    permission_resolve_response: InteractionResolveResponse,
    call_resolve_request: CallResolveRequest,
    call_resolve_response: CallResolveResponse,
    tools_list_request: ToolsListRequest,
    tools_list_response: ToolsListResponse,
    error_response: ProtocolError,

    pub fn deinit(self: *Payload, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .capabilities_request => {},
            .initialize_request => |*value| value.deinit(allocator),
            .initialize_response => |*value| value.deinit(allocator),
            .capabilities_response => |*value| value.deinit(allocator),
            .models_request => |*value| value.deinit(allocator),
            .models_response => |*value| value.deinit(allocator),
            .session_open_request => |*value| value.deinit(allocator),
            .session_open_response => |*value| value.deinit(allocator),
            .session_state_request => |*value| value.deinit(allocator),
            .session_state_response => |*value| value.deinit(allocator),
            .session_state_updated => |*value| value.deinit(allocator),
            .session_model_switch_request => |*value| value.deinit(allocator),
            .session_model_switch_response => |*value| value.deinit(allocator),
            .message_submit_request => |*value| value.deinit(allocator),
            .message_submit_response => |*value| value.deinit(allocator),
            .run_cancel_request => |*value| value.deinit(allocator),
            .run_cancel_response => |*value| value.deinit(allocator),
            .run_started => |*value| value.deinit(allocator),
            .run_status_updated => |*value| value.deinit(allocator),
            .content_delta => |*value| value.deinit(allocator),
            .run_completed => |*value| value.deinit(allocator),
            .run_failed => |*value| value.deinit(allocator),
            .run_cancelled => |*value| value.deinit(allocator),
            .user_input_resolve_request => |*value| value.deinit(allocator),
            .user_input_resolve_response => |*value| value.deinit(allocator),
            .permission_resolve_request => |*value| value.deinit(allocator),
            .permission_resolve_response => |*value| value.deinit(allocator),
            .call_resolve_request => |*value| value.deinit(allocator),
            .call_resolve_response => |*value| value.deinit(allocator),
            .tools_list_request => |*value| value.deinit(allocator),
            .tools_list_response => |*value| value.deinit(allocator),
            .error_response => |*value| value.deinit(allocator),
        }
    }

    pub fn typeName(self: Payload) []const u8 {
        return switch (self) {
            .initialize_request => "protocol.initialize.request",
            .initialize_response => "protocol.initialize.response",
            .capabilities_request => "capabilities.request",
            .capabilities_response => "capabilities.response",
            .models_request => "models.request",
            .models_response => "models.response",
            .session_open_request => "session.open.request",
            .session_open_response => "session.open.response",
            .session_state_request => "session.state.request",
            .session_state_response => "session.state.response",
            .session_state_updated => "session.state.updated",
            .session_model_switch_request => "session.model.switch.request",
            .session_model_switch_response => "session.model.switch.response",
            .message_submit_request => "session.message.submit.request",
            .message_submit_response => "session.message.submit.response",
            .run_cancel_request => "run.cancel.request",
            .run_cancel_response => "run.cancel.response",
            .run_started => "run.started",
            .run_status_updated => "run.status.updated",
            .content_delta => "content.delta",
            .run_completed => "run.completed",
            .run_failed => "run.failed",
            .run_cancelled => "run.cancelled",
            .user_input_resolve_request => "user.input.resolve.request",
            .user_input_resolve_response => "user.input.resolve.response",
            .permission_resolve_request => "action.permission.resolve.request",
            .permission_resolve_response => "action.permission.resolve.response",
            .call_resolve_request => "action.call.resolve.request",
            .call_resolve_response => "action.call.resolve.response",
            .tools_list_request => "action.tools.list.request",
            .tools_list_response => "action.tools.list.response",
            .error_response => "error.response",
        };
    }

    pub fn isRunScopedEvent(self: Payload) bool {
        return switch (self) {
            .run_started,
            .run_status_updated,
            .content_delta,
            .run_completed,
            .run_failed,
            .run_cancelled,
            => true,
            else => false,
        };
    }

    pub fn isTerminal(self: Payload) bool {
        return switch (self) {
            .run_completed, .run_failed, .run_cancelled => true,
            else => false,
        };
    }
};

pub const Envelope = struct {
    id: []const u8,
    payload: Payload,
    sequence: ?u64 = null,
    timestamp_ms: ?i64 = null,
    in_reply_to: ?[]const u8 = null,
    session_id: ?[]const u8 = null,
    run_id: ?[]const u8 = null,
    turn_id: ?[]const u8 = null,
    tool_call_id: ?[]const u8 = null,
    capability_revision: ?[]const u8 = null,

    pub fn deinit(self: *Envelope, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        if (self.in_reply_to) |value| allocator.free(value);
        if (self.session_id) |value| allocator.free(value);
        if (self.run_id) |value| allocator.free(value);
        if (self.turn_id) |value| allocator.free(value);
        if (self.tool_call_id) |value| allocator.free(value);
        if (self.capability_revision) |value| allocator.free(value);
        self.payload.deinit(allocator);
    }
};

fn containsKey(list: []const []const u8, key: []const u8) bool {
    for (list) |entry| {
        if (std.mem.eql(u8, entry, key)) return true;
    }
    return false;
}

pub fn freeStringList(allocator: std.mem.Allocator, list: []const []const u8) void {
    for (list) |entry| allocator.free(entry);
    allocator.free(list);
}

pub fn dupeStringList(allocator: std.mem.Allocator, list: []const []const u8) ![]const []const u8 {
    const out = try allocator.alloc([]const u8, list.len);
    var filled: usize = 0;
    errdefer {
        for (out[0..filled]) |entry| allocator.free(entry);
        allocator.free(out);
    }
    for (list, 0..) |entry, index| {
        out[index] = try allocator.dupe(u8, entry);
        filled = index + 1;
    }
    return out;
}

test "payload type names match the core envelope vocabulary" {
    const submit = Payload{ .capabilities_request = {} };
    try std.testing.expectEqualStrings("capabilities.request", submit.typeName());

    const delta = Payload{ .content_delta = .{
        .session_id = "s",
        .run_id = "r",
        .part = .{ .text = "hi" },
    } };
    try std.testing.expectEqualStrings("content.delta", delta.typeName());
    try std.testing.expect(delta.isRunScopedEvent());
    try std.testing.expect(!delta.isTerminal());
}

test "terminal classification covers exactly the three v0.1 terminals" {
    const completed = Payload{ .run_completed = .{
        .session_id = "s",
        .run_id = "r",
        .final_response = .{ .role = .assistant, .content = .{ .text = "ok" } },
        .stop_reason = "end_turn",
    } };
    const failed = Payload{ .run_failed = .{
        .session_id = "s",
        .run_id = "r",
        .err = .{ .code = EmittedErrorCode.internal_error.text(), .message = "boom" },
    } };
    const cancelled = Payload{ .run_cancelled = .{ .session_id = "s", .run_id = "r" } };

    try std.testing.expect(completed.isTerminal());
    try std.testing.expect(failed.isTerminal());
    try std.testing.expect(cancelled.isTerminal());

    const started = Payload{ .run_started = .{ .session_id = "s", .run_id = "r" } };
    try std.testing.expect(!started.isTerminal());
    try std.testing.expect(started.isRunScopedEvent());
}

test "run control capability keys are the four gated keys" {
    try std.testing.expectEqualStrings("run.model_selection", RunControl.model_id.capabilityKey());
    try std.testing.expectEqualStrings("run.instructions", RunControl.instructions.capabilityKey());
    try std.testing.expectEqualStrings("run.tool_selection", RunControl.tool_choice.capabilityKey());
    try std.testing.expectEqualStrings("run.structured_output", RunControl.output_schema.capabilityKey());
}

test "protocol error detail lookup finds a declared key" {
    const err = ProtocolError{
        .code = EmittedErrorCode.unsupported_feature.text(),
        .message = "unadvertised",
        .details = &.{
            .{ .key = "feature", .value = "run.instructions" },
            .{ .key = "reason", .value = "unadvertised" },
        },
    };
    try std.testing.expectEqualStrings("run.instructions", err.detail("feature").?);
    try std.testing.expectEqualStrings("unadvertised", err.detail("reason").?);
    try std.testing.expect(err.detail("model_id") == null);
}

test "dupeStringList round trips and frees" {
    const allocator = std.testing.allocator;
    const source = [_][]const u8{ "0.1", "0.2" };
    const copy = try dupeStringList(allocator, &source);
    defer freeStringList(allocator, copy);

    try std.testing.expectEqual(@as(usize, 2), copy.len);
    try std.testing.expectEqualStrings("0.1", copy[0]);
    try std.testing.expectEqualStrings("0.2", copy[1]);
}

test "submit request reports declared degraded opt-in keys" {
    const allow = [_][]const u8{"models.list"};
    const request = MessageSubmitRequest{
        .session_id = "s",
        .messages = &.{},
        .delivery = .auto,
        .allow_degraded_features = &allow,
    };
    try std.testing.expect(request.allowsDegraded("models.list"));
    try std.testing.expect(!request.allowsDegraded("run.instructions"));
    try std.testing.expect(request.control(.model_id) == null);
}
