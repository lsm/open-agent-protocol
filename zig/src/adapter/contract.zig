const std = @import("std");
const oap_types = @import("oap_types");

pub const Failure = error{
    Unavailable,
    SessionClosed,
    RunActive,
    InvalidSubmission,
    RunNotFound,
    RunTerminal,
    ReplayCursorFuture,
    InteractionNotFound,
    InvalidResolution,
    UnsupportedFeature,
    CapabilityDegraded,
    ModelNotFound,
    ToolCatalogUnavailable,
    BackendFailed,
} || std.mem.Allocator.Error;

pub const reason_unadvertised = "unadvertised";
pub const reason_unsatisfiable = "unsatisfiable";

pub const feature_models_list = "models.list";
pub const feature_model_switch = "session.model.switch";
pub const feature_tools_list = "action.tools.list";
pub const feature_tools_provide = "action.tools.provide";
pub const feature_tool_sources_attach = "action.tool_sources.attach";
pub const feature_open_subscribe = "session.open.subscribe";
pub const feature_submit = "session.message.submit";

pub const Refusal = struct {
    feature: []const u8 = "",
    reason: []const u8 = "",
    field: []const u8 = "",
    model_id: []const u8 = "",
    backend: []const u8 = "",
    message: []const u8 = "",

    pub fn unsupported(self: *Refusal, feature: []const u8, reason: []const u8) Failure {
        self.* = .{ .feature = feature, .reason = reason };
        return error.UnsupportedFeature;
    }

    pub fn unsupportedField(self: *Refusal, feature: []const u8, reason: []const u8, field: []const u8) Failure {
        self.* = .{ .feature = feature, .reason = reason, .field = field };
        return error.UnsupportedFeature;
    }

    pub fn degraded(self: *Refusal, feature: []const u8) Failure {
        self.* = .{ .feature = feature };
        return error.CapabilityDegraded;
    }

    pub fn missingModel(self: *Refusal, model_id: []const u8) Failure {
        self.* = .{ .model_id = model_id };
        return error.ModelNotFound;
    }

    pub fn fail(self: *Refusal, failure: Failure, message: []const u8) Failure {
        self.* = .{ .message = message };
        return failure;
    }
};

pub const Feature = struct {
    key: []const u8,
    level: oap_types.SupportLevel,
    reason: ?[]const u8 = null,
    scope: ?[]const u8 = null,
    modes: []const []const u8 = &.{},
    constraints_json: ?[]const u8 = null,
    limits_json: ?[]const u8 = null,
};

pub const Descriptor = struct {
    endpoint: oap_types.Endpoint,
    capability_revision: []const u8,
    features: []const Feature,
    tools: []const oap_types.ToolDefinition = &.{},
    sources: []const oap_types.ToolSourceDescriptor = &.{},
    limits: ?oap_types.Limits = null,

    pub fn level(self: Descriptor, key: []const u8) oap_types.SupportLevel {
        for (self.features) |feature| {
            if (std.mem.eql(u8, feature.key, key)) return feature.level;
        }
        return .unavailable;
    }
};

pub const OpenRequest = struct {
    session_id: []const u8 = "",
    participant: []const u8,
    allow_degraded_features: []const []const u8 = &.{},
    tools_json: ?[]const u8 = null,
    tool_sources_json: ?[]const u8 = null,
};

pub const Resolution = union(enum) {
    input: *const oap_types.UserInputResolveRequest,
    permission: *const oap_types.PermissionResolveRequest,
};

pub const Event = struct {
    line: []const u8,
    run_id: []const u8,
    sequence: u64,
};

pub const Activity = enum { idle, running, waiting };

pub const Replay = union(enum) {
    events: []const Event,
    gap: Gap,
};

pub const Gap = struct {
    requested_after: u64,
    oldest_available: u64,
    latest_available: u64,
};

pub const Switched = struct {
    response: oap_types.SessionModelSwitchResponse,
    state: oap_types.SessionState,
};

pub const Session = struct {
    ptr: *anyopaque,
    vtable: *const VTable,

    pub const VTable = struct {
        id: *const fn (ptr: *anyopaque) []const u8,
        state: *const fn (ptr: *anyopaque, arena: std.mem.Allocator, refusal: *Refusal) Failure!oap_types.SessionState,
        submit: *const fn (ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *Refusal) Failure!oap_types.MessageSubmitResponse,
        resolve: *const fn (ptr: *anyopaque, arena: std.mem.Allocator, resolution: Resolution, refusal: *Refusal) Failure!void,
        cancel: *const fn (ptr: *anyopaque, arena: std.mem.Allocator, run_id: []const u8, refusal: *Refusal) Failure!oap_types.RunCancelResponse,
        pump: *const fn (ptr: *anyopaque, wait_ns: u64) Failure!bool,
        drain: *const fn (ptr: *anyopaque, allocator: std.mem.Allocator, out: *std.ArrayList(Event)) Failure!void,
        activity: *const fn (ptr: *anyopaque) Activity,
        close: *const fn (ptr: *anyopaque) void,
        tools: ?*const fn (ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ToolsListRequest, refusal: *Refusal) Failure!oap_types.ToolsListResponse = null,
        models: ?*const fn (ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.ModelsRequest, refusal: *Refusal) Failure!oap_types.ModelsResponse = null,
        switch_model: ?*const fn (ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.SessionModelSwitchRequest, refusal: *Refusal) Failure!Switched = null,
        resolve_call: ?*const fn (ptr: *anyopaque, arena: std.mem.Allocator, request: *const oap_types.CallResolveRequest, refusal: *Refusal) Failure!oap_types.CallResolveResponse = null,
        replay: ?*const fn (ptr: *anyopaque, allocator: std.mem.Allocator, run_id: []const u8, after: u64, refusal: *Refusal) Failure!Replay = null,
    };

    pub fn id(self: Session) []const u8 {
        return self.vtable.id(self.ptr);
    }

    pub fn state(self: Session, arena: std.mem.Allocator, refusal: *Refusal) Failure!oap_types.SessionState {
        return self.vtable.state(self.ptr, arena, refusal);
    }

    pub fn submit(self: Session, arena: std.mem.Allocator, request: *const oap_types.MessageSubmitRequest, refusal: *Refusal) Failure!oap_types.MessageSubmitResponse {
        return self.vtable.submit(self.ptr, arena, request, refusal);
    }

    pub fn resolve(self: Session, arena: std.mem.Allocator, resolution: Resolution, refusal: *Refusal) Failure!void {
        return self.vtable.resolve(self.ptr, arena, resolution, refusal);
    }

    pub fn cancel(self: Session, arena: std.mem.Allocator, run_id: []const u8, refusal: *Refusal) Failure!oap_types.RunCancelResponse {
        return self.vtable.cancel(self.ptr, arena, run_id, refusal);
    }

    pub fn pump(self: Session, wait_ns: u64) Failure!bool {
        return self.vtable.pump(self.ptr, wait_ns);
    }

    pub fn drain(self: Session, allocator: std.mem.Allocator, out: *std.ArrayList(Event)) Failure!void {
        return self.vtable.drain(self.ptr, allocator, out);
    }

    pub fn activity(self: Session) Activity {
        return self.vtable.activity(self.ptr);
    }

    pub fn close(self: Session) void {
        self.vtable.close(self.ptr);
    }
};

pub const Adapter = struct {
    ptr: *anyopaque,
    vtable: *const VTable,

    pub const VTable = struct {
        probe: *const fn (ptr: *anyopaque, refusal: *Refusal) Failure!Descriptor,
        open: *const fn (ptr: *anyopaque, arena: std.mem.Allocator, request: OpenRequest, refusal: *Refusal) Failure!Session,
    };

    pub fn probe(self: Adapter, refusal: *Refusal) Failure!Descriptor {
        return self.vtable.probe(self.ptr, refusal);
    }

    pub fn open(self: Adapter, arena: std.mem.Allocator, request: OpenRequest, refusal: *Refusal) Failure!Session {
        return self.vtable.open(self.ptr, arena, request, refusal);
    }
};

pub const Unavailable = struct {
    backend: []const u8,
    message: []const u8,

    pub fn adapter(self: *Unavailable) Adapter {
        return .{ .ptr = self, .vtable = &.{ .probe = probe, .open = open } };
    }

    fn refuse(ptr: *anyopaque, refusal: *Refusal) Failure {
        const self: *Unavailable = @ptrCast(@alignCast(ptr));
        refusal.* = .{ .backend = self.backend, .message = self.message };
        return error.Unavailable;
    }

    fn probe(ptr: *anyopaque, refusal: *Refusal) Failure!Descriptor {
        return refuse(ptr, refusal);
    }

    fn open(ptr: *anyopaque, arena: std.mem.Allocator, request: OpenRequest, refusal: *Refusal) Failure!Session {
        _ = arena;
        _ = request;
        return refuse(ptr, refusal);
    }
};

const RunControl = struct { key: []const u8, present: bool };

pub fn refuseUnadvertisedControls(descriptor: Descriptor, request: *const oap_types.MessageSubmitRequest, refusal: *Refusal) Failure!void {
    const controls = [_]RunControl{
        .{ .key = "run.instructions", .present = request.instructions != null },
        .{ .key = "run.model_selection", .present = request.model_id != null },
        .{ .key = "run.structured_output", .present = request.output_schema_json != null },
        .{ .key = "run.tool_selection", .present = request.tool_choice_json != null },
    };
    for (controls) |control| {
        if (!control.present) continue;
        switch (descriptor.level(control.key)) {
            .unavailable => return refusal.unsupported(control.key, reason_unadvertised),
            .degraded => if (!request.allowsDegraded(control.key)) return refusal.degraded(control.key),
            .native, .emulated => {},
        }
    }
    const mode = deliveryFeature(request.delivery) orelse return;
    switch (descriptor.level(mode)) {
        .unavailable => return refusal.unsupported(mode, reason_unadvertised),
        .degraded => if (!request.allowsDegraded(mode)) return refusal.degraded(mode),
        .native, .emulated => {},
    }
}

fn deliveryFeature(delivery: oap_types.RequestedDelivery) ?[]const u8 {
    return switch (delivery) {
        .auto => null,
        .queue => "session.message.delivery.queue",
        .steer => "session.message.delivery.steer",
        .btw => "session.message.delivery.btw",
    };
}

pub fn refuseUnadvertisedOpen(descriptor: Descriptor, request: *const oap_types.SessionOpenRequest, refusal: *Refusal) Failure!void {
    const elections = [_]struct { key: []const u8, present: bool }{
        .{ .key = feature_open_subscribe, .present = request.subscribe },
        .{ .key = feature_tool_sources_attach, .present = request.tool_sources_json != null },
        .{ .key = feature_tools_provide, .present = request.tools_json != null },
    };
    for (elections) |election| {
        if (!election.present) continue;
        switch (descriptor.level(election.key)) {
            .unavailable => return refusal.unsupported(election.key, reason_unadvertised),
            .degraded => if (!request.allowsDegraded(election.key)) return refusal.degraded(election.key),
            .native, .emulated => {},
        }
    }
    if (request.message_json != null) return refusal.unsupportedField(feature_submit, reason_unsatisfiable, "message");
}

pub const QuestionKind = enum { text, single_choice, multi_choice };

pub const Question = struct {
    id: []const u8,
    kind: QuestionKind,
    options: []const []const u8 = &.{},

    fn offers(self: Question, option: []const u8) bool {
        for (self.options) |offered| {
            if (std.mem.eql(u8, offered, option)) return true;
        }
        return false;
    }
};

pub fn validInputAnswer(question: Question, answer: oap_types.InputAnswer) bool {
    if (!std.mem.eql(u8, answer.question_id, question.id)) return false;
    const has_text = if (answer.text) |text| text.len > 0 else false;
    if (has_text and answer.selected_option_ids.len != 0) return false;
    switch (question.kind) {
        .text => if (!has_text) return false,
        .single_choice => if (answer.selected_option_ids.len != 1) return false,
        .multi_choice => if (answer.selected_option_ids.len == 0) return false,
    }
    for (answer.selected_option_ids, 0..) |selected, index| {
        if (selected.len == 0 or !question.offers(selected)) return false;
        for (answer.selected_option_ids[0..index]) |earlier| {
            if (std.mem.eql(u8, earlier, selected)) return false;
        }
    }
    return true;
}

const testing = std.testing;

const probe_features = [_]Feature{
    .{ .key = "run.model_selection", .level = .native },
    .{ .key = "run.instructions", .level = .degraded },
    .{ .key = feature_open_subscribe, .level = .degraded },
};

const probe_descriptor = Descriptor{
    .endpoint = .{ .id = "probe.endpoint" },
    .capability_revision = "probe-v1",
    .features = &probe_features,
};

fn submitWith(model_id: ?[]const u8, instructions: ?[]const u8, output_schema: ?[]const u8, tool_choice: ?[]const u8, allowed: []const []const u8) oap_types.MessageSubmitRequest {
    return .{
        .session_id = "s",
        .messages = &.{},
        .delivery = .auto,
        .model_id = model_id,
        .instructions = instructions,
        .output_schema_json = output_schema,
        .tool_choice_json = tool_choice,
        .allow_degraded_features = allowed,
    };
}

test "a descriptor reports a key it never listed as unavailable" {
    try testing.expectEqual(oap_types.SupportLevel.native, probe_descriptor.level("run.model_selection"));
    try testing.expectEqual(oap_types.SupportLevel.unavailable, probe_descriptor.level("run.tool_selection"));
}

test "an unadvertised run control is refused naming its key and the unadvertised reason" {
    var refusal = Refusal{};
    const request = submitWith(null, null, null, "{\"type\":\"auto\"}", &.{});
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedControls(probe_descriptor, &request, &refusal));
    try testing.expectEqualStrings("run.tool_selection", refusal.feature);
    try testing.expectEqualStrings(reason_unadvertised, refusal.reason);
}

test "a degraded run control needs the caller's consent, and an advertised one passes" {
    var refusal = Refusal{};
    const unconsented = submitWith(null, "be brief", null, null, &.{});
    try testing.expectError(error.CapabilityDegraded, refuseUnadvertisedControls(probe_descriptor, &unconsented, &refusal));
    try testing.expectEqualStrings("run.instructions", refusal.feature);

    const consented = submitWith("m", "be brief", null, null, &.{"run.instructions"});
    try refuseUnadvertisedControls(probe_descriptor, &consented, &refusal);
}

test "controls are judged in the order instructions, model, structured output, tool selection" {
    var refusal = Refusal{};
    const bare = Descriptor{ .endpoint = .{ .id = "e" }, .capability_revision = "r", .features = &.{} };
    const every = submitWith("m", "i", "{}", "{}", &.{});
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedControls(bare, &every, &refusal));
    try testing.expectEqualStrings("run.instructions", refusal.feature);

    const without_instructions = submitWith("m", null, "{}", "{}", &.{});
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedControls(bare, &without_instructions, &refusal));
    try testing.expectEqualStrings("run.model_selection", refusal.feature);

    const schema_and_choice = submitWith(null, null, "{}", "{}", &.{});
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedControls(bare, &schema_and_choice, &refusal));
    try testing.expectEqualStrings("run.structured_output", refusal.feature);
}

test "a non-auto delivery is judged under its own key after the run controls, and auto is never gated" {
    var refusal = Refusal{};
    const bare = Descriptor{ .endpoint = .{ .id = "e" }, .capability_revision = "r", .features = &.{} };
    const modes = [_]struct { delivery: oap_types.RequestedDelivery, key: []const u8 }{
        .{ .delivery = .queue, .key = "session.message.delivery.queue" },
        .{ .delivery = .steer, .key = "session.message.delivery.steer" },
        .{ .delivery = .btw, .key = "session.message.delivery.btw" },
    };
    for (modes) |mode| {
        var request = submitWith(null, null, null, null, &.{});
        request.delivery = mode.delivery;
        try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedControls(bare, &request, &refusal));
        try testing.expectEqualStrings(mode.key, refusal.feature);
        try testing.expectEqualStrings(reason_unadvertised, refusal.reason);
    }

    var instructed = submitWith(null, "i", null, null, &.{});
    instructed.delivery = .queue;
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedControls(bare, &instructed, &refusal));
    try testing.expectEqualStrings("run.instructions", refusal.feature);

    const plain = submitWith(null, null, null, null, &.{});
    try refuseUnadvertisedControls(bare, &plain, &refusal);
}

test "a degraded delivery mode needs the caller's consent, and an advertised one passes" {
    var refusal = Refusal{};
    const offered = [_]Feature{
        .{ .key = "session.message.delivery.queue", .level = .degraded },
        .{ .key = "session.message.delivery.steer", .level = .native },
    };
    const descriptor = Descriptor{ .endpoint = .{ .id = "e" }, .capability_revision = "r", .features = &offered };

    var unconsented = submitWith(null, null, null, null, &.{});
    unconsented.delivery = .queue;
    try testing.expectError(error.CapabilityDegraded, refuseUnadvertisedControls(descriptor, &unconsented, &refusal));
    try testing.expectEqualStrings("session.message.delivery.queue", refusal.feature);

    var consented = submitWith(null, null, null, null, &.{"session.message.delivery.queue"});
    consented.delivery = .queue;
    try refuseUnadvertisedControls(descriptor, &consented, &refusal);

    var steered = submitWith(null, null, null, null, &.{});
    steered.delivery = .steer;
    try refuseUnadvertisedControls(descriptor, &steered, &refusal);
}

test "an open is refused for each election its descriptor does not carry" {
    var refusal = Refusal{};
    const bare = Descriptor{ .endpoint = .{ .id = "e" }, .capability_revision = "r", .features = &.{} };

    const plain = oap_types.SessionOpenRequest{ .session_id = "s" };
    try refuseUnadvertisedOpen(bare, &plain, &refusal);

    const attaching = oap_types.SessionOpenRequest{ .tool_sources_json = "[{\"id\":\"x\"}]" };
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedOpen(bare, &attaching, &refusal));
    try testing.expectEqualStrings(feature_tool_sources_attach, refusal.feature);
    try testing.expectEqualStrings(reason_unadvertised, refusal.reason);

    const providing = oap_types.SessionOpenRequest{ .tools_json = "[{\"name\":\"t\"}]" };
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedOpen(bare, &providing, &refusal));
    try testing.expectEqualStrings(feature_tools_provide, refusal.feature);

    const subscribing = oap_types.SessionOpenRequest{ .subscribe = true };
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedOpen(bare, &subscribing, &refusal));
    try testing.expectEqualStrings(feature_open_subscribe, refusal.feature);

    const compound = oap_types.SessionOpenRequest{ .message_json = "{}" };
    try testing.expectError(error.UnsupportedFeature, refuseUnadvertisedOpen(bare, &compound, &refusal));
    try testing.expectEqualStrings(feature_submit, refusal.feature);
    try testing.expectEqualStrings(reason_unsatisfiable, refusal.reason);
    try testing.expectEqualStrings("message", refusal.field);
}

test "a degraded subscription at open needs the caller's consent" {
    var refusal = Refusal{};
    const unconsented = oap_types.SessionOpenRequest{ .subscribe = true };
    try testing.expectError(error.CapabilityDegraded, refuseUnadvertisedOpen(probe_descriptor, &unconsented, &refusal));
    try testing.expectEqualStrings(feature_open_subscribe, refusal.feature);

    const consented = oap_types.SessionOpenRequest{ .subscribe = true, .allow_degraded_features = &.{feature_open_subscribe} };
    try refuseUnadvertisedOpen(probe_descriptor, &consented, &refusal);
}

test "an unavailable backend refuses both probe and open, naming itself" {
    var unavailable = Unavailable{ .backend = "hermes", .message = "the hermes backend is not in this build" };
    const adapter = unavailable.adapter();
    var refusal = Refusal{};
    try testing.expectError(error.Unavailable, adapter.probe(&refusal));
    try testing.expectEqualStrings("hermes", refusal.backend);
    refusal = .{};
    try testing.expectError(error.Unavailable, adapter.open(testing.allocator, .{ .participant = "user" }, &refusal));
    try testing.expectEqualStrings("the hermes backend is not in this build", refusal.message);
}

test "an input answer is valid only in the shape its question asks for" {
    const text = Question{ .id = "note", .kind = .text };
    const single = Question{ .id = "pick", .kind = .single_choice, .options = &.{ "a", "b" } };
    const multi = Question{ .id = "many", .kind = .multi_choice, .options = &.{ "x", "y" } };
    const cases = [_]struct { question: Question, answer: oap_types.InputAnswer, valid: bool }{
        .{ .question = text, .answer = .{ .question_id = "note", .text = "hello" }, .valid = true },
        .{ .question = text, .answer = .{ .question_id = "note" }, .valid = false },
        .{ .question = text, .answer = .{ .question_id = "note", .text = "" }, .valid = false },
        .{ .question = text, .answer = .{ .question_id = "note", .text = "hello", .selected_option_ids = &.{"a"} }, .valid = false },
        .{ .question = text, .answer = .{ .question_id = "other", .text = "hello" }, .valid = false },
        .{ .question = single, .answer = .{ .question_id = "pick", .selected_option_ids = &.{"a"} }, .valid = true },
        .{ .question = single, .answer = .{ .question_id = "pick" }, .valid = false },
        .{ .question = single, .answer = .{ .question_id = "pick", .selected_option_ids = &.{ "a", "b" } }, .valid = false },
        .{ .question = single, .answer = .{ .question_id = "pick", .selected_option_ids = &.{"c"} }, .valid = false },
        .{ .question = single, .answer = .{ .question_id = "pick", .selected_option_ids = &.{""} }, .valid = false },
        .{ .question = single, .answer = .{ .question_id = "pick", .text = "a" }, .valid = false },
        .{ .question = multi, .answer = .{ .question_id = "many", .selected_option_ids = &.{"x"} }, .valid = true },
        .{ .question = multi, .answer = .{ .question_id = "many", .selected_option_ids = &.{ "x", "y" } }, .valid = true },
        .{ .question = multi, .answer = .{ .question_id = "many" }, .valid = false },
        .{ .question = multi, .answer = .{ .question_id = "many", .selected_option_ids = &.{ "x", "x" } }, .valid = false },
        .{ .question = multi, .answer = .{ .question_id = "many", .selected_option_ids = &.{ "x", "z" } }, .valid = false },
    };
    for (cases) |case| {
        try testing.expectEqual(case.valid, validInputAnswer(case.question, case.answer));
    }
}
