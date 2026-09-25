const std = @import("std");
const json_encode = @import("json_encode");
const json_writer = @import("json_writer");
const oap_types = @import("oap_types");

pub const DecodeError = error{
    InvalidEnvelope,
    UnknownEnvelopeType,
    ProtocolMismatch,
    VersionMismatch,
    ProfileMismatch,
    MissingField,
    InvalidField,
};

pub fn serializeEnvelope(env: oap_types.Envelope, allocator: std.mem.Allocator) ![]u8 {
    var buffer = std.ArrayList(u8).empty;
    errdefer buffer.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buffer, allocator);

    try w.beginObject();
    try w.writeStringField("protocol", oap_types.PROTOCOL);
    try w.writeStringField("version", oap_types.VERSION);
    try w.writeStringField("profile", oap_types.PROFILE);
    try w.writeStringField("type", env.payload.typeName());
    try w.writeStringField("id", env.id);
    if (env.sequence) |sequence| try w.writeIntField("sequence", sequence);
    if (env.timestamp_ms) |timestamp| try w.writeIntField("timestamp_ms", timestamp);
    if (env.in_reply_to) |value| try w.writeStringField("in_reply_to", value);
    if (env.session_id) |value| try w.writeStringField("session_id", value);
    if (env.run_id) |value| try w.writeStringField("run_id", value);
    if (env.turn_id) |value| try w.writeStringField("turn_id", value);
    if (env.tool_call_id) |value| try w.writeStringField("tool_call_id", value);
    if (env.capability_revision) |value| try w.writeStringField("capability_revision", value);

    try w.writeKey("payload");
    try serializePayload(&w, env.payload);
    try w.endObject();

    const out = try allocator.dupe(u8, buffer.items);
    buffer.deinit(allocator);
    return out;
}

pub fn writeJsonValueOrString(w: *json_writer.JsonWriter, raw: []const u8) !void {
    if (isWellFormedJson(raw)) {
        try w.writeRawJson(raw);
    } else {
        try w.writeString(raw);
    }
}

fn isWellFormedJson(raw: []const u8) bool {
    const trimmed = std.mem.trim(u8, raw, " \t\r\n");
    if (trimmed.len == 0) return false;
    var scanner = std.json.Scanner.initCompleteInput(std.heap.page_allocator, trimmed);
    defer scanner.deinit();
    while (true) {
        const token = scanner.next() catch return false;
        if (token == .end_of_document) return true;
    }
}

pub fn serializeContentPart(w: *json_writer.JsonWriter, part: oap_types.ContentPart) !void {
    try w.beginObject();
    switch (part) {
        .text => |value| {
            try w.writeStringField("type", "text");
            try w.writeStringField("text", value);
        },
        .reasoning => |value| {
            try w.writeStringField("type", "reasoning");
            try w.writeStringField("reasoning", value.text);
            if (value.carry) |carry| try w.writeStringField("carry", carry);
        },
        .tool_call => |value| {
            try w.writeStringField("type", "tool_call");
            try w.writeStringField("tool_call_id", value.tool_call_id);
            try w.writeStringField("name", value.name);
            try w.writeKey("arguments_json");
            try writeJsonValueOrString(w, value.arguments_json);
            if (value.carry) |carry| try w.writeStringField("carry", carry);
        },
        .tool_result => |value| {
            try w.writeStringField("type", "tool_result");
            try w.writeStringField("tool_call_id", value.tool_call_id);
            try w.writeKey("result");
            try writeJsonValueOrString(w, value.result_json);
            if (value.is_error) |is_error| try w.writeBoolField("is_error", is_error);
        },
    }
    try w.endObject();
}

pub fn serializeMessage(w: *json_writer.JsonWriter, message: oap_types.Message) !void {
    try w.beginObject();
    if (message.id) |id| try w.writeStringField("id", id);
    try w.writeStringField("role", @tagName(message.role));
    try w.writeKey("content");
    switch (message.content) {
        .text => |value| try w.writeString(value),
        .parts => |parts| {
            try w.beginArray();
            for (parts) |part| try serializeContentPart(w, part);
            try w.endArray();
        },
    }
    try w.endObject();
}

pub fn serializeUsage(w: *json_writer.JsonWriter, usage: oap_types.Usage) !void {
    try w.writeKey("usage");
    try w.beginObject();
    if (usage.input_tokens) |value| try w.writeIntField("input_tokens", value);
    if (usage.output_tokens) |value| try w.writeIntField("output_tokens", value);
    if (usage.total_tokens) |value| try w.writeIntField("total_tokens", value);
    try w.endObject();
}

fn serializeProtocolError(w: *json_writer.JsonWriter, err: oap_types.ProtocolError) !void {
    try w.beginObject();
    try w.writeStringField("code", err.code);
    try w.writeStringField("message", err.message);
    if (err.retriable) |retriable| try w.writeBoolField("retriable", retriable);
    if (err.details.len > 0) {
        try w.writeKey("details");
        try w.beginObject();
        for (err.details) |entry| try w.writeStringField(entry.key, entry.value);
        try w.endObject();
    }
    try w.endObject();
}

fn serializeSessionState(w: *json_writer.JsonWriter, state: oap_types.SessionState) !void {
    try w.writeStringField("session_id", state.session_id);
    try w.writeStringField("status", @tagName(state.status));
    if (state.active_run_id) |value| try w.writeStringField("active_run_id", value);
    if (state.active_runs.len > 0) {
        try w.writeKey("active_runs");
        try w.beginArray();
        for (state.active_runs) |entry| {
            try w.beginObject();
            try w.writeStringField("run_id", entry.run_id);
            try w.writeStringField("status", @tagName(entry.status));
            try w.writeStringField("relationship", entry.relationship);
            if (entry.queue_position) |position| try w.writeIntField("queue_position", position);
            if (entry.as_of_sequence) |sequence| try w.writeIntField("as_of_sequence", sequence);
            if (entry.admitted_submit_requests.len > 0) try serializeStringArray(w, "admitted_submit_requests", entry.admitted_submit_requests);
            if (entry.pending_interactions.len > 0) try serializeStringArray(w, "pending_interactions", entry.pending_interactions);
            if (entry.acknowledged_interactions.len > 0) try serializeStringArray(w, "acknowledged_interactions", entry.acknowledged_interactions);
            try w.endObject();
        }
        try w.endArray();
    }
    if (state.current_model_id) |value| try w.writeStringField("current_model_id", value);
    if (state.transcript_cursor) |value| try w.writeStringField("transcript_cursor", value);
    if (state.updated_at_ms) |value| try w.writeIntField("updated_at_ms", value);
    if (state.metadata_json) |metadata| {
        try w.writeKey("metadata");
        try writeJsonValueOrString(w, metadata);
    }
    if (state.sources.len > 0) try serializeSources(w, state.sources);
    if (state.as_of) |capture| {
        try w.writeKey("as_of");
        try w.beginObject();
        if (capture.admitted_submit_requests.len > 0) try serializeStringArray(w, "admitted_submit_requests", capture.admitted_submit_requests);
        if (capture.settled.len > 0) {
            try w.writeKey("settled");
            try w.beginArray();
            for (capture.settled) |entry| try serializeRunPosition(w, entry);
            try w.endArray();
        }
        if (capture.model_run_sequence) |position| {
            try w.writeKey("model_run_sequence");
            try serializeRunPosition(w, position);
        }
        try w.endObject();
    }
}

fn serializeRunPosition(w: *json_writer.JsonWriter, position: oap_types.RunPosition) !void {
    try w.beginObject();
    if (position.run_id) |run_id| {
        try w.writeStringField("run_id", run_id);
    } else {
        try w.writeKey("run_id");
        try w.writeNull();
    }
    try w.writeIntField("sequence", position.sequence);
    try w.endObject();
}

fn serializeEndpoint(w: *json_writer.JsonWriter, endpoint: oap_types.Endpoint) !void {
    try w.beginObject();
    try w.writeStringField("id", endpoint.id);
    if (endpoint.name) |value| try w.writeStringField("name", value);
    if (endpoint.version) |value| try w.writeStringField("version", value);
    if (endpoint.adapter) |value| try w.writeStringField("adapter", value);
    try w.endObject();
}

pub fn serializeStringArray(w: *json_writer.JsonWriter, key: []const u8, values: []const []const u8) !void {
    try w.writeKey(key);
    try w.beginArray();
    for (values) |value| try w.writeString(value);
    try w.endArray();
}

fn serializeFeatureMap(w: *json_writer.JsonWriter, features: []const oap_types.Feature) !void {
    try w.writeKey("features");
    try w.beginObject();
    for (features) |feature| {
        try w.writeKey(feature.key);
        try w.beginObject();
        try w.writeStringField("level", @tagName(feature.level));
        if (feature.scope) |scope| try w.writeStringField("scope", scope);
        if (feature.reason) |reason| try w.writeStringField("reason", reason);
        if (feature.modes.len > 0) try serializeStringArray(w, "modes", feature.modes);
        if (feature.constraints_json) |constraints| {
            try w.writeKey("constraints");
            try writeJsonValueOrString(w, constraints);
        }
        if (feature.limits_json) |limits| {
            try w.writeKey("limits");
            try writeJsonValueOrString(w, limits);
        }
        try w.endObject();
    }
    try w.endObject();
}

fn serializeSources(w: *json_writer.JsonWriter, sources: []const oap_types.ToolSourceDescriptor) !void {
    try w.writeKey("sources");
    try w.beginArray();
    for (sources) |source| {
        try w.beginObject();
        try w.writeStringField("id", source.id);
        try w.writeStringField("kind", source.kind);
        if (source.display_name) |value| try w.writeStringField("display_name", value);
        if (source.protocol) |value| try w.writeStringField("protocol", value);
        if (source.endpoint) |value| try w.writeStringField("endpoint", value);
        try w.endObject();
    }
    try w.endArray();
}

fn serializeToolDefinition(w: *json_writer.JsonWriter, tool: oap_types.ToolDefinition) !void {
    try w.beginObject();
    try w.writeStringField("name", tool.name);
    if (tool.description) |value| try w.writeStringField("description", value);
    try w.writeKey("input_schema");
    try writeJsonValueOrString(w, tool.input_schema_json);
    try w.writeStringField("execution_owner", tool.execution_owner);
    if (tool.source) |value| try w.writeStringField("source", value);
    if (tool.features.len > 0) try serializeFeatureMap(w, tool.features);
    try w.endObject();
}

fn serializeInteractionScope(
    w: *json_writer.JsonWriter,
    interaction_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    session_id: []const u8,
    run_id: []const u8,
) !void {
    try w.writeStringField("interaction_id", interaction_id);
    try w.writeStringField("requested_by", requested_by);
    try w.writeStringField("responded_by", responded_by);
    try w.writeStringField("session_id", session_id);
    try w.writeStringField("run_id", run_id);
}

fn serializePayload(w: *json_writer.JsonWriter, payload: oap_types.Payload) !void {
    try w.beginObject();
    switch (payload) {
        .capabilities_request => {},
        .initialize_request => |value| {
            try serializeStringArray(w, "protocol_versions", value.protocol_versions);
            try serializeStringArray(w, "profiles", value.profiles);
            if (value.participant) |participant| {
                try w.writeKey("participant");
                try w.beginObject();
                try w.writeStringField("id", participant.id);
                if (participant.name) |name| try w.writeStringField("name", name);
                if (participant.version) |version| try w.writeStringField("version", version);
                try w.endObject();
            }
        },
        .initialize_response => |value| {
            try w.writeStringField("protocol_version", value.protocol_version);
            try w.writeStringField("profile", value.profile);
            try w.writeKey("endpoint");
            try serializeEndpoint(w, value.endpoint);
        },
        .capabilities_response => |value| {
            try w.writeKey("endpoint");
            try serializeEndpoint(w, value.endpoint);
            if (value.protocol_versions.len > 0) {
                try serializeStringArray(w, "protocol_versions", value.protocol_versions);
            }
            if (value.profiles.len > 0) {
                try serializeStringArray(w, "profiles", value.profiles);
            }
            if (value.bindings.len > 0) {
                try w.writeKey("bindings");
                try w.beginArray();
                for (value.bindings) |binding| {
                    try w.beginObject();
                    try w.writeStringField("kind", binding.kind);
                    if (binding.serialization) |serialization| {
                        try w.writeStringField("serialization", serialization);
                    }
                    try w.endObject();
                }
                try w.endArray();
            }
            if (value.features.len > 0) try serializeFeatureMap(w, value.features);
            if (value.requested_delivery_modes.len > 0 or value.effective_delivery_modes.len > 0) {
                try w.writeKey("layers");
                try w.beginObject();
                try w.writeKey("agent_loop");
                try w.beginObject();
                if (value.requested_delivery_modes.len > 0) {
                    try w.writeKey("requested_delivery_modes");
                    try w.beginArray();
                    for (value.requested_delivery_modes) |mode| try w.writeString(@tagName(mode));
                    try w.endArray();
                }
                if (value.effective_delivery_modes.len > 0) {
                    try w.writeKey("effective_delivery_modes");
                    try w.beginArray();
                    for (value.effective_delivery_modes) |mode| try w.writeString(@tagName(mode));
                    try w.endArray();
                }
                try w.endObject();
                try w.endObject();
            }
            if (value.degradation.len > 0) {
                try w.writeKey("degradation");
                try w.beginArray();
                for (value.degradation) |record| {
                    try w.beginObject();
                    try w.writeStringField("feature", record.feature);
                    if (record.from) |from| try w.writeStringField("from", @tagName(from));
                    try w.writeStringField("to", @tagName(record.to));
                    try w.writeStringField("reason", record.reason);
                    try w.endObject();
                }
                try w.endArray();
            }
            if (value.tools.len > 0) {
                try w.writeKey("tools");
                try w.beginArray();
                for (value.tools) |tool| try serializeToolDefinition(w, tool);
                try w.endArray();
            }
            if (value.sources.len > 0) try serializeSources(w, value.sources);
            if (value.limits) |limits| {
                try w.writeKey("limits");
                try w.beginObject();
                if (limits.max_active_runs_per_session) |active| try w.writeIntField("max_active_runs_per_session", active);
                if (limits.max_queued_runs_per_session) |queued| try w.writeIntField("max_queued_runs_per_session", queued);
                try w.endObject();
            }
        },
        .models_request => |value| {
            try w.writeStringField("session_id", value.session_id);
            if (value.allow_degraded_features.len > 0) {
                try serializeStringArray(w, "allow_degraded_features", value.allow_degraded_features);
            }
        },
        .models_response => |value| {
            try w.writeStringField("session_id", value.session_id);
            if (value.current_model_id) |model_id| try w.writeStringField("current_model_id", model_id);
            try w.writeKey("models");
            try w.beginArray();
            for (value.models) |model| {
                try w.beginObject();
                try w.writeStringField("id", model.id);
                if (model.display_name) |name| try w.writeStringField("display_name", name);
                if (model.provider_id) |provider_id| try w.writeStringField("provider_id", provider_id);
                if (model.context_window) |window| try w.writeIntField("context_window", window);
                if (model.default) try w.writeBoolField("default", true);
                try w.endObject();
            }
            try w.endArray();
            if (value.providers.len > 0) {
                try w.writeKey("providers");
                try w.beginArray();
                for (value.providers) |provider| {
                    try w.beginObject();
                    try w.writeStringField("id", provider.id);
                    inline for (.{ "display_name", "wire", "kind", "endpoint", "service_id", "upstream_provider_id" }) |name| {
                        if (@field(provider, name)) |member| try w.writeStringField(name, member);
                    }
                    try w.endObject();
                }
                try w.endArray();
            }
        },
        .session_open_request => |value| {
            if (value.session_id) |session_id| try w.writeStringField("session_id", session_id);
            if (value.subscribe) try w.writeBoolField("subscribe", true);
            if (value.message_json) |message| {
                try w.writeKey("message");
                try writeJsonValueOrString(w, message);
            }
            if (value.tool_sources_json) |sources| {
                try w.writeKey("tool_sources");
                try writeJsonValueOrString(w, sources);
            }
            if (value.tools_json) |tools| {
                try w.writeKey("tools");
                try writeJsonValueOrString(w, tools);
            }
            if (value.allow_degraded_features.len > 0) {
                try serializeStringArray(w, "allow_degraded_features", value.allow_degraded_features);
            }
        },
        .session_state_request => |value| {
            try w.writeStringField("session_id", value.session_id);
        },
        .session_open_response, .session_state_response, .session_state_updated => |value| {
            try serializeSessionState(w, value);
        },
        .session_model_switch_request => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("model_id", value.model_id);
            if (value.allow_degraded_features.len > 0) {
                try serializeStringArray(w, "allow_degraded_features", value.allow_degraded_features);
            }
        },
        .session_model_switch_response => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("model_id", value.model_id);
            if (value.previous_model_id) |previous| try w.writeStringField("previous_model_id", previous);
        },
        .message_submit_request => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeKey("messages");
            try w.beginArray();
            for (value.messages) |message| try serializeMessage(w, message);
            try w.endArray();
            try w.writeStringField("delivery", @tagName(value.delivery));
            if (value.model_id) |model_id| try w.writeStringField("model_id", model_id);
            if (value.instructions) |instructions| try w.writeStringField("instructions", instructions);
            if (value.tool_choice_json) |tool_choice| {
                try w.writeKey("tool_choice");
                try writeJsonValueOrString(w, tool_choice);
            }
            if (value.output_schema_json) |output_schema| {
                try w.writeKey("output_schema");
                try writeJsonValueOrString(w, output_schema);
            }
            if (value.allow_degraded_features.len > 0) {
                try serializeStringArray(w, "allow_degraded_features", value.allow_degraded_features);
            }
        },
        .message_submit_response => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeBoolField("accepted", value.accepted);
            try w.writeStringField("submission_id", value.submission_id);
            try w.writeStringField("requested_delivery", @tagName(value.requested_delivery));
            try w.writeStringField("effective_delivery", @tagName(value.effective_delivery));
            if (value.delivery_resolution) |resolution| {
                try w.writeStringField("delivery_resolution", resolution);
            }
            try w.writeStringField("admission", @tagName(value.admission));
            if (value.run_id) |run_id| try w.writeStringField("run_id", run_id);
            if (value.status) |status| try w.writeStringField("status", @tagName(status));
            if (value.model_id) |model_id| try w.writeStringField("model_id", model_id);
            if (value.message_ids.len > 0) try serializeStringArray(w, "message_ids", value.message_ids);
        },
        .run_cancel_request => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            if (value.reason) |reason| try w.writeStringField("reason", reason);
        },
        .run_cancel_response => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeBoolField("accepted", value.accepted);
            try w.writeStringField("status", @tagName(value.status));
        },
        .run_started => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeStringField("status", "running");
            if (value.model_id) |model_id| try w.writeStringField("model_id", model_id);
            if (value.started_at_ms) |started| try w.writeIntField("started_at_ms", started);
        },
        .run_status_updated => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeStringField("status", @tagName(value.status));
            if (value.updated_at_ms) |updated| try w.writeIntField("updated_at_ms", updated);
        },
        .content_delta => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            if (value.message_id) |message_id| try w.writeStringField("message_id", message_id);
            try w.writeKey("part");
            try serializeContentPart(w, value.part);
        },
        .run_completed => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeKey("final_response");
            try serializeMessage(w, value.final_response);
            try w.writeStringField("stop_reason", value.stop_reason);
            if (value.model_id) |model_id| try w.writeStringField("model_id", model_id);
            if (!value.usage.isEmpty()) try serializeUsage(w, value.usage);
            if (value.duration_ms) |duration| try w.writeIntField("duration_ms", duration);
        },
        .run_failed => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeKey("error");
            try serializeProtocolError(w, value.err);
            if (!value.usage.isEmpty()) try serializeUsage(w, value.usage);
            if (value.duration_ms) |duration| try w.writeIntField("duration_ms", duration);
        },
        .run_cancelled => |value| {
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            if (value.reason) |reason| try w.writeStringField("reason", reason);
            if (!value.usage.isEmpty()) try serializeUsage(w, value.usage);
            if (value.duration_ms) |duration| try w.writeIntField("duration_ms", duration);
        },
        .user_input_resolve_request => |value| {
            try serializeInteractionScope(w, value.interaction_id, value.requested_by, value.responded_by, value.session_id, value.run_id);
            try w.writeKey("answers");
            try w.beginArray();
            for (value.answers) |answer| {
                try w.beginObject();
                try w.writeStringField("question_id", answer.question_id);
                if (answer.text) |text| try w.writeStringField("text", text);
                if (answer.selected_option_ids.len > 0) {
                    try serializeStringArray(w, "selected_option_ids", answer.selected_option_ids);
                }
                try w.endObject();
            }
            try w.endArray();
        },
        .user_input_resolve_response, .permission_resolve_response => |value| {
            try w.writeStringField("interaction_id", value.interaction_id);
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeBoolField("accepted", value.accepted);
        },
        .permission_resolve_request => |value| {
            try serializeInteractionScope(w, value.interaction_id, value.requested_by, value.responded_by, value.session_id, value.run_id);
            if (value.choice_id) |choice| try w.writeStringField("choice_id", choice);
            try w.writeBoolField("granted", value.granted);
            if (value.reason) |reason| try w.writeStringField("reason", reason);
            if (value.updated_arguments_json) |arguments| {
                try w.writeKey("updated_arguments_json");
                try writeJsonValueOrString(w, arguments);
            }
        },
        .call_resolve_request => |value| {
            try w.writeStringField("interaction_id", value.interaction_id);
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeStringField("tool_call_id", value.tool_call_id);
            try w.writeStringField("requested_by", value.requested_by);
            try w.writeStringField("responded_by", value.responded_by);
            if (value.started) {
                try w.writeKey("started");
                try w.beginObject();
                try w.endObject();
            }
            if (value.result_json) |result| {
                try w.writeKey("result");
                try writeJsonValueOrString(w, result);
            }
            if (value.err) |failure| {
                try w.writeKey("error");
                try serializeProtocolError(w, failure);
            }
        },
        .call_resolve_response => |value| {
            try w.writeStringField("interaction_id", value.interaction_id);
            try w.writeStringField("session_id", value.session_id);
            try w.writeStringField("run_id", value.run_id);
            try w.writeStringField("tool_call_id", value.tool_call_id);
            try w.writeBoolField("accepted", value.accepted);
            if (value.reason) |reason| try w.writeStringField("reason", reason);
            if (value.settlement_id) |settlement| {
                try w.writeKey("details");
                try w.beginObject();
                try w.writeStringField("settlement_id", settlement);
                try w.endObject();
            }
        },
        .tools_list_request => |value| {
            if (value.session_id) |session_id| try w.writeStringField("session_id", session_id);
            if (value.allow_degraded_features.len > 0) {
                try serializeStringArray(w, "allow_degraded_features", value.allow_degraded_features);
            }
        },
        .tools_list_response => |value| {
            if (value.session_id) |session_id| try w.writeStringField("session_id", session_id);
            if (value.sources.len > 0) try serializeSources(w, value.sources);
            try w.writeKey("tools");
            try w.beginArray();
            for (value.tools) |tool| try serializeToolDefinition(w, tool);
            try w.endArray();
        },
        .error_response => |value| {
            try w.writeKey("error");
            try serializeProtocolError(w, value);
        },
    }
    try w.endObject();
}

pub fn deserializeEnvelope(line: []const u8, allocator: std.mem.Allocator) !oap_types.Envelope {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        return DecodeError.InvalidEnvelope;
    };
    defer parsed.deinit();
    if (parsed.value != .object) return DecodeError.InvalidEnvelope;
    const root = parsed.value.object;

    const protocol = try requiredString(root, "protocol");
    if (!std.mem.eql(u8, protocol, oap_types.PROTOCOL)) return DecodeError.ProtocolMismatch;
    const version = try requiredString(root, "version");
    if (!std.mem.eql(u8, version, oap_types.VERSION)) return DecodeError.VersionMismatch;
    const profile = try requiredString(root, "profile");
    if (!std.mem.eql(u8, profile, oap_types.PROFILE)) return DecodeError.ProfileMismatch;

    const type_str = try requiredString(root, "type");
    const id_str = try requiredString(root, "id");
    if (id_str.len == 0) return DecodeError.InvalidField;

    const payload_value = root.get("payload") orelse return DecodeError.MissingField;
    if (payload_value != .object) return DecodeError.InvalidField;

    var sequence: ?u64 = null;
    if (root.get("sequence")) |value| {
        if (value != .integer or value.integer < 1) return DecodeError.InvalidField;
        sequence = @intCast(value.integer);
    }
    var timestamp_ms: ?i64 = null;
    if (root.get("timestamp_ms")) |value| {
        if (value != .integer) return DecodeError.InvalidField;
        timestamp_ms = value.integer;
    }

    const in_reply_to = try optionalOwnedString(root, "in_reply_to", allocator);
    errdefer if (in_reply_to) |owned| allocator.free(owned);
    const session_id = try optionalOwnedString(root, "session_id", allocator);
    errdefer if (session_id) |owned| allocator.free(owned);
    const run_id = try optionalOwnedString(root, "run_id", allocator);
    errdefer if (run_id) |owned| allocator.free(owned);
    const turn_id = try optionalOwnedString(root, "turn_id", allocator);
    errdefer if (turn_id) |owned| allocator.free(owned);
    const tool_call_id = try optionalOwnedString(root, "tool_call_id", allocator);
    errdefer if (tool_call_id) |owned| allocator.free(owned);
    const capability_revision = try optionalOwnedString(root, "capability_revision", allocator);
    errdefer if (capability_revision) |owned| allocator.free(owned);

    const id = try allocator.dupe(u8, id_str);
    errdefer allocator.free(id);

    const payload = try deserializePayload(type_str, payload_value.object, allocator);

    return .{
        .id = id,
        .payload = payload,
        .sequence = sequence,
        .timestamp_ms = timestamp_ms,
        .in_reply_to = in_reply_to,
        .session_id = session_id,
        .run_id = run_id,
        .turn_id = turn_id,
        .tool_call_id = tool_call_id,
        .capability_revision = capability_revision,
    };
}

pub fn requiredString(obj: std.json.ObjectMap, key: []const u8) ![]const u8 {
    const value = obj.get(key) orelse return DecodeError.MissingField;
    if (value != .string) return DecodeError.InvalidField;
    return value.string;
}

pub fn requiredOwnedString(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) ![]const u8 {
    const value = try requiredString(obj, key);
    if (value.len == 0) return DecodeError.InvalidField;
    return allocator.dupe(u8, value);
}

pub fn optionalOwnedString(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) !?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .string) return DecodeError.InvalidField;
    if (value.string.len == 0) return DecodeError.InvalidField;
    return try allocator.dupe(u8, value.string);
}

pub fn optionalBool(obj: std.json.ObjectMap, key: []const u8) !?bool {
    const value = obj.get(key) orelse return null;
    if (value != .bool) return DecodeError.InvalidField;
    return value.bool;
}

pub fn requiredBool(obj: std.json.ObjectMap, key: []const u8) !bool {
    return (try optionalBool(obj, key)) orelse DecodeError.MissingField;
}

pub fn optionalUnsigned(obj: std.json.ObjectMap, key: []const u8) !?u64 {
    const value = obj.get(key) orelse return null;
    if (value != .integer or value.integer < 0) return DecodeError.InvalidField;
    return @intCast(value.integer);
}

pub fn optionalInteger(obj: std.json.ObjectMap, key: []const u8) !?i64 {
    const value = obj.get(key) orelse return null;
    if (value != .integer) return DecodeError.InvalidField;
    return value.integer;
}

pub fn requiredEnum(comptime T: type, obj: std.json.ObjectMap, key: []const u8) !T {
    const value = try requiredString(obj, key);
    return std.meta.stringToEnum(T, value) orelse DecodeError.InvalidField;
}

pub fn optionalEnum(comptime T: type, obj: std.json.ObjectMap, key: []const u8) !?T {
    const value = obj.get(key) orelse return null;
    if (value != .string) return DecodeError.InvalidField;
    return std.meta.stringToEnum(T, value.string) orelse DecodeError.InvalidField;
}

pub fn decodeEnumList(
    comptime T: type,
    obj: std.json.ObjectMap,
    key: []const u8,
    allocator: std.mem.Allocator,
) ![]const T {
    const value = obj.get(key) orelse return &.{};
    if (value != .array) return DecodeError.InvalidField;
    const decoded = try allocator.alloc(T, value.array.items.len);
    errdefer allocator.free(decoded);
    for (value.array.items, 0..) |item, index| {
        if (item != .string) return DecodeError.InvalidField;
        decoded[index] = std.meta.stringToEnum(T, item.string) orelse return DecodeError.InvalidField;
    }
    return decoded;
}

pub fn ownedRawJson(value: std.json.Value, allocator: std.mem.Allocator) ![]const u8 {
    if (value == .string) return allocator.dupe(u8, value.string);
    return json_encode.valueAlloc(allocator, value);
}

pub fn optionalRawJson(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) !?[]const u8 {
    const value = obj.get(key) orelse return null;
    return try ownedRawJson(value, allocator);
}

pub fn deserializeUsage(obj: std.json.ObjectMap) !oap_types.Usage {
    const value = obj.get("usage") orelse return .{};
    if (value != .object) return DecodeError.InvalidField;
    return .{
        .input_tokens = try optionalUnsigned(value.object, "input_tokens"),
        .output_tokens = try optionalUnsigned(value.object, "output_tokens"),
        .total_tokens = try optionalUnsigned(value.object, "total_tokens"),
    };
}

pub fn deserializeContentPart(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.ContentPart {
    if (value != .object) return DecodeError.InvalidField;
    const obj = value.object;
    const part_type = try requiredString(obj, "type");

    if (obj.get("carry") != null and
        !std.mem.eql(u8, part_type, "reasoning") and
        !std.mem.eql(u8, part_type, "tool_call"))
    {
        return DecodeError.InvalidField;
    }

    if (std.mem.eql(u8, part_type, "text")) {
        const text = try requiredString(obj, "text");
        return .{ .text = try allocator.dupe(u8, text) };
    }
    if (std.mem.eql(u8, part_type, "reasoning")) {
        const reasoning = try requiredString(obj, "reasoning");
        const text = try allocator.dupe(u8, reasoning);
        errdefer allocator.free(text);
        const carry = try optionalOwnedString(obj, "carry", allocator);
        return .{ .reasoning = .{ .text = text, .carry = carry } };
    }
    if (std.mem.eql(u8, part_type, "tool_call")) {
        const tool_call_id = try requiredOwnedString(obj, "tool_call_id", allocator);
        errdefer allocator.free(tool_call_id);
        const name = try requiredOwnedString(obj, "name", allocator);
        errdefer allocator.free(name);
        const arguments = obj.get("arguments_json") orelse return DecodeError.MissingField;
        const arguments_json = try ownedRawJson(arguments, allocator);
        errdefer allocator.free(arguments_json);
        const carry = try optionalOwnedString(obj, "carry", allocator);
        return .{ .tool_call = .{
            .tool_call_id = tool_call_id,
            .name = name,
            .arguments_json = arguments_json,
            .carry = carry,
        } };
    }
    if (std.mem.eql(u8, part_type, "tool_result")) {
        const tool_call_id = try requiredOwnedString(obj, "tool_call_id", allocator);
        errdefer allocator.free(tool_call_id);
        const result = obj.get("result") orelse return DecodeError.MissingField;
        const result_json = try ownedRawJson(result, allocator);
        errdefer allocator.free(result_json);
        return .{ .tool_result = .{
            .tool_call_id = tool_call_id,
            .result_json = result_json,
            .is_error = try optionalBool(obj, "is_error"),
        } };
    }
    return DecodeError.InvalidField;
}

pub fn deserializeContent(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.Content {
    switch (value) {
        .string => |text| return .{ .text = try allocator.dupe(u8, text) },
        .array => |array| {
            if (array.items.len == 0) return DecodeError.InvalidField;
            const parts = try allocator.alloc(oap_types.ContentPart, array.items.len);
            var filled: usize = 0;
            errdefer {
                for (parts[0..filled]) |*part| part.deinit(allocator);
                allocator.free(parts);
            }
            for (array.items, 0..) |item, index| {
                parts[index] = try deserializeContentPart(item, allocator);
                filled = index + 1;
            }
            return .{ .parts = parts };
        },
        else => return DecodeError.InvalidField,
    }
}

pub fn deserializeMessage(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.Message {
    if (value != .object) return DecodeError.InvalidField;
    const obj = value.object;
    const id = try optionalOwnedString(obj, "id", allocator);
    errdefer if (id) |owned| allocator.free(owned);
    const role = try requiredEnum(oap_types.Role, obj, "role");
    const content_value = obj.get("content") orelse return DecodeError.MissingField;
    const content = try deserializeContent(content_value, allocator);
    return .{ .id = id, .role = role, .content = content };
}

fn deserializeProtocolError(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.ProtocolError {
    if (value != .object) return DecodeError.InvalidField;
    const obj = value.object;
    const code = try requiredOwnedString(obj, "code", allocator);
    errdefer allocator.free(code);
    const message = try requiredOwnedString(obj, "message", allocator);
    errdefer allocator.free(message);
    const retriable = try optionalBool(obj, "retriable");

    var details: []const oap_types.DetailEntry = &.{};
    if (obj.get("details")) |details_value| {
        if (details_value != .object) return DecodeError.InvalidField;
        const entries = try allocator.alloc(oap_types.DetailEntry, details_value.object.count());
        var filled: usize = 0;
        errdefer {
            for (entries[0..filled]) |entry| {
                allocator.free(entry.key);
                allocator.free(entry.value);
            }
            allocator.free(entries);
        }
        var iterator = details_value.object.iterator();
        while (iterator.next()) |entry| {
            if (entry.value_ptr.* != .string) return DecodeError.InvalidField;
            const key = try allocator.dupe(u8, entry.key_ptr.*);
            errdefer allocator.free(key);
            const detail_value = try allocator.dupe(u8, entry.value_ptr.string);
            entries[filled] = .{ .key = key, .value = detail_value };
            filled += 1;
        }
        details = entries;
    }

    return .{ .code = code, .message = message, .retriable = retriable, .details = details };
}

fn deserializeSessionState(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.SessionState {
    const session_id = try requiredOwnedString(obj, "session_id", allocator);
    errdefer allocator.free(session_id);
    const status = try requiredEnum(oap_types.SessionStatus, obj, "status");
    const active_run_id = try optionalOwnedString(obj, "active_run_id", allocator);
    errdefer if (active_run_id) |owned| allocator.free(owned);
    const current_model_id = try optionalOwnedString(obj, "current_model_id", allocator);
    errdefer if (current_model_id) |owned| allocator.free(owned);
    const updated_at_ms = try optionalInteger(obj, "updated_at_ms");
    var state = oap_types.SessionState{
        .session_id = session_id,
        .status = status,
        .active_run_id = active_run_id,
        .current_model_id = current_model_id,
        .updated_at_ms = updated_at_ms,
    };
    errdefer {
        for (state.active_runs) |*entry| entry.deinit(allocator);
        allocator.free(state.active_runs);
        if (state.transcript_cursor) |owned| allocator.free(owned);
        if (state.metadata_json) |owned| allocator.free(owned);
        for (state.sources) |*entry| entry.deinit(allocator);
        allocator.free(state.sources);
    }
    if (obj.get("active_runs")) |value| state.active_runs = try deserializeActiveRuns(value, allocator);
    state.transcript_cursor = try optionalOwnedString(obj, "transcript_cursor", allocator);
    state.metadata_json = try optionalObjectJson(obj, "metadata", allocator);
    if (obj.get("sources")) |value| state.sources = try deserializeSources(value, allocator);
    if (obj.get("as_of")) |value| state.as_of = try deserializeSessionCapture(value, allocator);
    return state;
}

fn optionalStringArray(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) ![]const []const u8 {
    if (obj.get(key) == null) return &.{};
    return deserializeStringArray(obj, key, allocator);
}

fn deserializeActiveRuns(value: std.json.Value, allocator: std.mem.Allocator) ![]oap_types.ActiveRun {
    if (value != .array) return DecodeError.InvalidField;
    const runs = try allocator.alloc(oap_types.ActiveRun, value.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (runs[0..filled]) |*entry| entry.deinit(allocator);
        allocator.free(runs);
    }
    for (value.array.items) |item| {
        if (item != .object) return DecodeError.InvalidField;
        const status = try requiredEnum(oap_types.RunStatus, item.object, "status");
        const queue_position = try optionalUnsigned(item.object, "queue_position");
        const as_of_sequence = try optionalUnsigned(item.object, "as_of_sequence");
        const run_id = try requiredOwnedString(item.object, "run_id", allocator);
        errdefer allocator.free(run_id);
        const relationship = try requiredOwnedString(item.object, "relationship", allocator);
        errdefer allocator.free(relationship);
        const admitted = try optionalStringArray(item.object, "admitted_submit_requests", allocator);
        errdefer oap_types.freeStringList(allocator, admitted);
        const pending = try optionalStringArray(item.object, "pending_interactions", allocator);
        errdefer oap_types.freeStringList(allocator, pending);
        const acknowledged = try optionalStringArray(item.object, "acknowledged_interactions", allocator);
        runs[filled] = .{
            .run_id = run_id,
            .status = status,
            .relationship = relationship,
            .queue_position = queue_position,
            .as_of_sequence = as_of_sequence,
            .admitted_submit_requests = admitted,
            .pending_interactions = pending,
            .acknowledged_interactions = acknowledged,
        };
        filled += 1;
    }
    return runs;
}

fn deserializeRunPosition(value: std.json.Value, allocator: std.mem.Allocator, genesis_allowed: bool) !oap_types.RunPosition {
    if (value != .object) return DecodeError.InvalidField;
    const sequence = (try optionalUnsigned(value.object, "sequence")) orelse return DecodeError.MissingField;
    if (genesis_allowed) {
        const carried = value.object.get("run_id") orelse return DecodeError.MissingField;
        if (carried == .null) return .{ .run_id = null, .sequence = sequence };
    }
    const run_id = try requiredOwnedString(value.object, "run_id", allocator);
    return .{ .run_id = run_id, .sequence = sequence };
}

fn deserializeSessionCapture(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.SessionCapture {
    if (value != .object) return DecodeError.InvalidField;
    var capture = oap_types.SessionCapture{};
    errdefer capture.deinit(allocator);
    capture.admitted_submit_requests = try optionalStringArray(value.object, "admitted_submit_requests", allocator);
    if (value.object.get("settled")) |settled| {
        if (settled != .array) return DecodeError.InvalidField;
        const positions = try allocator.alloc(oap_types.RunPosition, settled.array.items.len);
        var filled: usize = 0;
        errdefer {
            for (positions[0..filled]) |entry| if (entry.run_id) |owned| allocator.free(owned);
            allocator.free(positions);
        }
        for (settled.array.items) |item| {
            positions[filled] = try deserializeRunPosition(item, allocator, false);
            filled += 1;
        }
        capture.settled = positions;
    }
    if (value.object.get("model_run_sequence")) |position| capture.model_run_sequence = try deserializeRunPosition(position, allocator, true);
    return capture;
}

fn deserializeEndpoint(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.Endpoint {
    if (value != .object) return DecodeError.InvalidField;
    const obj = value.object;
    const id = try requiredOwnedString(obj, "id", allocator);
    errdefer allocator.free(id);
    const name = try optionalOwnedString(obj, "name", allocator);
    errdefer if (name) |owned| allocator.free(owned);
    const version = try optionalOwnedString(obj, "version", allocator);
    errdefer if (version) |owned| allocator.free(owned);
    const adapter = try optionalOwnedString(obj, "adapter", allocator);
    return .{ .id = id, .name = name, .version = version, .adapter = adapter };
}

pub fn deserializeStringArray(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) ![]const []const u8 {
    const value = obj.get(key) orelse return DecodeError.MissingField;
    if (value != .array) return DecodeError.InvalidField;
    const out = try allocator.alloc([]const u8, value.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (out[0..filled]) |entry| allocator.free(entry);
        allocator.free(out);
    }
    for (value.array.items, 0..) |item, index| {
        if (item != .string) return DecodeError.InvalidField;
        out[index] = try allocator.dupe(u8, item.string);
        filled = index + 1;
    }
    return out;
}

fn deserializePayload(
    type_str: []const u8,
    obj: std.json.ObjectMap,
    allocator: std.mem.Allocator,
) !oap_types.Payload {
    if (std.mem.eql(u8, type_str, "capabilities.request")) {
        return .{ .capabilities_request = {} };
    }
    if (std.mem.eql(u8, type_str, "protocol.initialize.request")) {
        const protocol_versions = try deserializeStringArray(obj, "protocol_versions", allocator);
        errdefer oap_types.freeStringList(allocator, protocol_versions);
        const profiles = try deserializeStringArray(obj, "profiles", allocator);
        errdefer oap_types.freeStringList(allocator, profiles);
        if (protocol_versions.len == 0 or profiles.len == 0) return DecodeError.InvalidField;

        var participant: ?oap_types.Participant = null;
        if (obj.get("participant")) |value| {
            if (value != .object) return DecodeError.InvalidField;
            const id = try requiredOwnedString(value.object, "id", allocator);
            errdefer allocator.free(id);
            const name = try optionalOwnedString(value.object, "name", allocator);
            errdefer if (name) |owned| allocator.free(owned);
            const version = try optionalOwnedString(value.object, "version", allocator);
            participant = .{ .id = id, .name = name, .version = version };
        }
        return .{ .initialize_request = .{
            .protocol_versions = protocol_versions,
            .profiles = profiles,
            .participant = participant,
        } };
    }
    if (std.mem.eql(u8, type_str, "protocol.initialize.response")) {
        const protocol_version = try requiredOwnedString(obj, "protocol_version", allocator);
        errdefer allocator.free(protocol_version);
        const profile = try requiredOwnedString(obj, "profile", allocator);
        errdefer allocator.free(profile);
        const endpoint_value = obj.get("endpoint") orelse return DecodeError.MissingField;
        const endpoint = try deserializeEndpoint(endpoint_value, allocator);
        return .{ .initialize_response = .{
            .protocol_version = protocol_version,
            .profile = profile,
            .endpoint = endpoint,
        } };
    }
    if (std.mem.eql(u8, type_str, "capabilities.response")) {
        return .{ .capabilities_response = try deserializeCapabilities(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "models.request")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const allow_degraded = if (obj.get("allow_degraded_features") != null)
            try deserializeStringArray(obj, "allow_degraded_features", allocator)
        else
            &.{};
        return .{ .models_request = .{
            .session_id = session_id,
            .allow_degraded_features = allow_degraded,
        } };
    }
    if (std.mem.eql(u8, type_str, "models.response")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const current_model_id = try optionalOwnedString(obj, "current_model_id", allocator);
        errdefer if (current_model_id) |value| allocator.free(value);
        const models_value = obj.get("models") orelse return DecodeError.MissingField;
        if (models_value != .array) return DecodeError.InvalidField;
        const models = try allocator.alloc(oap_types.ModelDescriptor, models_value.array.items.len);
        var built: usize = 0;
        errdefer {
            for (models[0..built]) |*model| model.deinit(allocator);
            allocator.free(models);
        }
        for (models_value.array.items, 0..) |item, index| {
            if (item != .object) return DecodeError.InvalidField;
            const id = try requiredOwnedString(item.object, "id", allocator);
            errdefer allocator.free(id);
            const display_name = try optionalOwnedString(item.object, "display_name", allocator);
            errdefer if (display_name) |value| allocator.free(value);
            const provider_id = try optionalOwnedString(item.object, "provider_id", allocator);
            errdefer if (provider_id) |value| allocator.free(value);
            const default_value = item.object.get("default");
            if (default_value != null and default_value.? != .bool) return DecodeError.InvalidField;
            models[index] = .{
                .id = id,
                .display_name = display_name,
                .provider_id = provider_id,
                .context_window = try optionalUnsigned(item.object, "context_window"),
                .default = if (default_value) |value| value.bool else false,
            };
            built = index + 1;
        }
        const providers: []oap_types.ProviderDescriptor = if (obj.get("providers")) |value| try deserializeProviders(value, allocator) else &.{};
        return .{ .models_response = .{
            .session_id = session_id,
            .current_model_id = current_model_id,
            .models = models,
            .providers = providers,
        } };
    }
    if (std.mem.eql(u8, type_str, "session.open.request")) {
        return .{ .session_open_request = try deserializeSessionOpen(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "session.state.request")) {
        return .{ .session_state_request = .{
            .session_id = try requiredOwnedString(obj, "session_id", allocator),
        } };
    }
    if (std.mem.eql(u8, type_str, "session.open.response")) {
        return .{ .session_open_response = try deserializeSessionState(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "session.state.response")) {
        return .{ .session_state_response = try deserializeSessionState(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "session.state.updated")) {
        return .{ .session_state_updated = try deserializeSessionState(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "session.model.switch.request")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const model_id = try requiredOwnedString(obj, "model_id", allocator);
        errdefer allocator.free(model_id);
        const allow_degraded = if (obj.get("allow_degraded_features") != null)
            try deserializeStringArray(obj, "allow_degraded_features", allocator)
        else
            &.{};
        return .{ .session_model_switch_request = .{
            .session_id = session_id,
            .model_id = model_id,
            .allow_degraded_features = allow_degraded,
        } };
    }
    if (std.mem.eql(u8, type_str, "session.model.switch.response")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const model_id = try requiredOwnedString(obj, "model_id", allocator);
        errdefer allocator.free(model_id);
        return .{ .session_model_switch_response = .{
            .session_id = session_id,
            .model_id = model_id,
            .previous_model_id = try optionalOwnedString(obj, "previous_model_id", allocator),
        } };
    }
    if (std.mem.eql(u8, type_str, "session.message.submit.request")) {
        return .{ .message_submit_request = try deserializeSubmitRequest(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "session.message.submit.response")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const submission_id = try requiredOwnedString(obj, "submission_id", allocator);
        errdefer allocator.free(submission_id);
        const delivery_resolution = try optionalOwnedString(obj, "delivery_resolution", allocator);
        errdefer if (delivery_resolution) |owned| allocator.free(owned);
        const run_id = try optionalOwnedString(obj, "run_id", allocator);
        errdefer if (run_id) |owned| allocator.free(owned);
        const model_id = try optionalOwnedString(obj, "model_id", allocator);
        errdefer if (model_id) |owned| allocator.free(owned);
        const accepted = try requiredBool(obj, "accepted");
        const requested_delivery = try requiredEnum(oap_types.RequestedDelivery, obj, "requested_delivery");
        const effective_delivery = try requiredEnum(oap_types.EffectiveDelivery, obj, "effective_delivery");
        const admission = try requiredEnum(oap_types.Admission, obj, "admission");
        const status = try optionalEnum(oap_types.RunStatus, obj, "status");
        const message_ids: []const []const u8 = if (obj.get("message_ids") != null)
            try deserializeStringArray(obj, "message_ids", allocator)
        else
            &.{};
        return .{ .message_submit_response = .{
            .session_id = session_id,
            .accepted = accepted,
            .submission_id = submission_id,
            .requested_delivery = requested_delivery,
            .effective_delivery = effective_delivery,
            .delivery_resolution = delivery_resolution,
            .admission = admission,
            .run_id = run_id,
            .status = status,
            .model_id = model_id,
            .message_ids = message_ids,
        } };
    }
    if (std.mem.eql(u8, type_str, "run.cancel.request")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        return .{ .run_cancel_request = .{
            .session_id = session_id,
            .run_id = run_id,
            .reason = try optionalOwnedString(obj, "reason", allocator),
        } };
    }
    if (std.mem.eql(u8, type_str, "run.cancel.response")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const accepted = try requiredBool(obj, "accepted");
        const status = try requiredEnum(oap_types.RunStatus, obj, "status");
        return .{ .run_cancel_response = .{
            .session_id = session_id,
            .run_id = run_id,
            .accepted = accepted,
            .status = status,
        } };
    }
    if (std.mem.eql(u8, type_str, "run.started")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const model_id = try optionalOwnedString(obj, "model_id", allocator);
        errdefer if (model_id) |owned| allocator.free(owned);
        const started_at_ms = try optionalInteger(obj, "started_at_ms");
        return .{ .run_started = .{
            .session_id = session_id,
            .run_id = run_id,
            .model_id = model_id,
            .started_at_ms = started_at_ms,
        } };
    }
    if (std.mem.eql(u8, type_str, "run.status.updated")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const status = try requiredEnum(oap_types.RunStatus, obj, "status");
        const updated_at_ms = try optionalInteger(obj, "updated_at_ms");
        return .{ .run_status_updated = .{
            .session_id = session_id,
            .run_id = run_id,
            .status = status,
            .updated_at_ms = updated_at_ms,
        } };
    }
    if (std.mem.eql(u8, type_str, "content.delta")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const message_id = try optionalOwnedString(obj, "message_id", allocator);
        errdefer if (message_id) |owned| allocator.free(owned);
        const part_value = obj.get("part") orelse return DecodeError.MissingField;
        const part = try deserializeContentPart(part_value, allocator);
        return .{ .content_delta = .{
            .session_id = session_id,
            .run_id = run_id,
            .message_id = message_id,
            .part = part,
        } };
    }
    if (std.mem.eql(u8, type_str, "run.completed")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const final_value = obj.get("final_response") orelse return DecodeError.MissingField;
        var final_response = try deserializeMessage(final_value, allocator);
        errdefer final_response.deinit(allocator);
        const stop_reason = try requiredOwnedString(obj, "stop_reason", allocator);
        errdefer allocator.free(stop_reason);
        const model_id = try optionalOwnedString(obj, "model_id", allocator);
        errdefer if (model_id) |owned| allocator.free(owned);
        const usage = try deserializeUsage(obj);
        const duration_ms = try optionalUnsigned(obj, "duration_ms");
        return .{ .run_completed = .{
            .session_id = session_id,
            .run_id = run_id,
            .final_response = final_response,
            .stop_reason = stop_reason,
            .model_id = model_id,
            .usage = usage,
            .duration_ms = duration_ms,
        } };
    }
    if (std.mem.eql(u8, type_str, "run.failed")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const error_value = obj.get("error") orelse return DecodeError.MissingField;
        var err = try deserializeProtocolError(error_value, allocator);
        errdefer err.deinit(allocator);
        const usage = try deserializeUsage(obj);
        const duration_ms = try optionalUnsigned(obj, "duration_ms");
        return .{ .run_failed = .{
            .session_id = session_id,
            .run_id = run_id,
            .err = err,
            .usage = usage,
            .duration_ms = duration_ms,
        } };
    }
    if (std.mem.eql(u8, type_str, "run.cancelled")) {
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        errdefer allocator.free(run_id);
        const reason = try optionalOwnedString(obj, "reason", allocator);
        errdefer if (reason) |owned| allocator.free(owned);
        const usage = try deserializeUsage(obj);
        const duration_ms = try optionalUnsigned(obj, "duration_ms");
        return .{ .run_cancelled = .{
            .session_id = session_id,
            .run_id = run_id,
            .reason = reason,
            .usage = usage,
            .duration_ms = duration_ms,
        } };
    }
    if (std.mem.eql(u8, type_str, "error.response")) {
        const error_value = obj.get("error") orelse return DecodeError.MissingField;
        return .{ .error_response = try deserializeProtocolError(error_value, allocator) };
    }
    if (std.mem.eql(u8, type_str, "user.input.resolve.request")) {
        return .{ .user_input_resolve_request = try deserializeInputResolve(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "user.input.resolve.response")) {
        return .{ .user_input_resolve_response = try deserializeResolveResponse(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "action.permission.resolve.request")) {
        return .{ .permission_resolve_request = try deserializePermissionResolve(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "action.permission.resolve.response")) {
        return .{ .permission_resolve_response = try deserializeResolveResponse(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "action.call.resolve.request")) {
        return .{ .call_resolve_request = try deserializeCallResolve(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "action.call.resolve.response")) {
        return .{ .call_resolve_response = try deserializeCallResolveResponse(obj, allocator) };
    }
    if (std.mem.eql(u8, type_str, "action.tools.list.request")) {
        const session_id = try optionalOwnedString(obj, "session_id", allocator);
        errdefer if (session_id) |owned| allocator.free(owned);
        const allow_degraded: []const []const u8 = if (obj.get("allow_degraded_features") != null)
            try deserializeStringArray(obj, "allow_degraded_features", allocator)
        else
            &.{};
        return .{ .tools_list_request = .{ .session_id = session_id, .allow_degraded_features = allow_degraded } };
    }
    if (std.mem.eql(u8, type_str, "action.tools.list.response")) {
        return .{ .tools_list_response = try deserializeToolsList(obj, allocator) };
    }

    return DecodeError.UnknownEnvelopeType;
}

fn optionalObjectJson(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) !?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .object) return DecodeError.InvalidField;
    return try ownedRawJson(value, allocator);
}

fn electedArrayJson(obj: std.json.ObjectMap, key: []const u8, allocator: std.mem.Allocator) !?[]const u8 {
    const value = obj.get(key) orelse return null;
    if (value != .array) return DecodeError.InvalidField;
    if (value.array.items.len == 0) return null;
    return try ownedRawJson(value, allocator);
}

fn deserializeSessionOpen(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.SessionOpenRequest {
    const session_id = try optionalOwnedString(obj, "session_id", allocator);
    errdefer if (session_id) |owned| allocator.free(owned);
    const subscribe = (try optionalBool(obj, "subscribe")) orelse false;
    const message_json: ?[]const u8 = if (obj.get("message")) |value| blk: {
        if (value != .object) return DecodeError.InvalidField;
        break :blk try ownedRawJson(value, allocator);
    } else null;
    errdefer if (message_json) |owned| allocator.free(owned);
    const tools_json = try electedArrayJson(obj, "tools", allocator);
    errdefer if (tools_json) |owned| allocator.free(owned);
    const tool_sources_json = try electedArrayJson(obj, "tool_sources", allocator);
    errdefer if (tool_sources_json) |owned| allocator.free(owned);
    const allow_degraded: []const []const u8 = if (obj.get("allow_degraded_features") != null)
        try deserializeStringArray(obj, "allow_degraded_features", allocator)
    else
        &.{};
    return .{
        .session_id = session_id,
        .subscribe = subscribe,
        .message_json = message_json,
        .tools_json = tools_json,
        .tool_sources_json = tool_sources_json,
        .allow_degraded_features = allow_degraded,
    };
}

fn deserializeFeatureMap(value: std.json.Value, allocator: std.mem.Allocator) ![]oap_types.Feature {
    if (value != .object) return DecodeError.InvalidField;
    const features = try allocator.alloc(oap_types.Feature, value.object.count());
    var filled: usize = 0;
    errdefer {
        for (features[0..filled]) |*entry| entry.deinit(allocator);
        allocator.free(features);
    }
    var iterator = value.object.iterator();
    while (iterator.next()) |entry| {
        if (entry.value_ptr.* != .object) return DecodeError.InvalidField;
        const level = try requiredEnum(oap_types.SupportLevel, entry.value_ptr.object, "level");
        const key = try allocator.dupe(u8, entry.key_ptr.*);
        errdefer allocator.free(key);
        const scope = try optionalOwnedString(entry.value_ptr.object, "scope", allocator);
        errdefer if (scope) |owned| allocator.free(owned);
        const reason = try optionalOwnedString(entry.value_ptr.object, "reason", allocator);
        errdefer if (reason) |owned| allocator.free(owned);
        const modes = try optionalStringArray(entry.value_ptr.object, "modes", allocator);
        errdefer oap_types.freeStringList(allocator, modes);
        const constraints_json = try optionalObjectJson(entry.value_ptr.object, "constraints", allocator);
        errdefer if (constraints_json) |owned| allocator.free(owned);
        const limits_json = try optionalObjectJson(entry.value_ptr.object, "limits", allocator);
        features[filled] = .{ .key = key, .level = level, .scope = scope, .reason = reason, .modes = modes, .constraints_json = constraints_json, .limits_json = limits_json };
        filled += 1;
    }
    return features;
}

fn deserializeProviders(value: std.json.Value, allocator: std.mem.Allocator) ![]oap_types.ProviderDescriptor {
    if (value != .array) return DecodeError.InvalidField;
    const providers = try allocator.alloc(oap_types.ProviderDescriptor, value.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (providers[0..filled]) |*entry| entry.deinit(allocator);
        allocator.free(providers);
    }
    for (value.array.items) |item| {
        if (item != .object) return DecodeError.InvalidField;
        var provider = oap_types.ProviderDescriptor{ .id = try requiredOwnedString(item.object, "id", allocator) };
        errdefer provider.deinit(allocator);
        inline for (.{ "display_name", "wire", "kind", "endpoint", "service_id", "upstream_provider_id" }) |name| {
            @field(provider, name) = try optionalOwnedString(item.object, name, allocator);
        }
        providers[filled] = provider;
        filled += 1;
    }
    return providers;
}

fn deserializeSources(value: std.json.Value, allocator: std.mem.Allocator) ![]oap_types.ToolSourceDescriptor {
    if (value != .array) return DecodeError.InvalidField;
    const sources = try allocator.alloc(oap_types.ToolSourceDescriptor, value.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (sources[0..filled]) |*entry| entry.deinit(allocator);
        allocator.free(sources);
    }
    for (value.array.items) |item| {
        if (item != .object) return DecodeError.InvalidField;
        const id = try requiredOwnedString(item.object, "id", allocator);
        errdefer allocator.free(id);
        const kind = try requiredOwnedString(item.object, "kind", allocator);
        errdefer allocator.free(kind);
        const display_name = try optionalOwnedString(item.object, "display_name", allocator);
        errdefer if (display_name) |owned| allocator.free(owned);
        const protocol = try optionalOwnedString(item.object, "protocol", allocator);
        errdefer if (protocol) |owned| allocator.free(owned);
        const endpoint = try optionalOwnedString(item.object, "endpoint", allocator);
        sources[filled] = .{ .id = id, .kind = kind, .display_name = display_name, .protocol = protocol, .endpoint = endpoint };
        filled += 1;
    }
    return sources;
}

fn deserializeToolDefinition(value: std.json.Value, allocator: std.mem.Allocator) !oap_types.ToolDefinition {
    if (value != .object) return DecodeError.InvalidField;
    const obj = value.object;
    const schema = obj.get("input_schema") orelse return DecodeError.MissingField;
    if (schema != .object) return DecodeError.InvalidField;
    const name = try requiredOwnedString(obj, "name", allocator);
    errdefer allocator.free(name);
    const description = if (obj.get("description")) |described| blk: {
        if (described != .string) return DecodeError.InvalidField;
        break :blk try allocator.dupe(u8, described.string);
    } else null;
    errdefer if (description) |owned| allocator.free(owned);
    const input_schema_json = try ownedRawJson(schema, allocator);
    errdefer allocator.free(input_schema_json);
    const execution_owner = try requiredOwnedString(obj, "execution_owner", allocator);
    errdefer allocator.free(execution_owner);
    const source = try optionalOwnedString(obj, "source", allocator);
    errdefer if (source) |owned| allocator.free(owned);
    const features: []oap_types.Feature = if (obj.get("features")) |declared|
        try deserializeFeatureMap(declared, allocator)
    else
        &.{};
    return .{
        .name = name,
        .description = description,
        .input_schema_json = input_schema_json,
        .execution_owner = execution_owner,
        .source = source,
        .features = features,
    };
}

fn deserializeToolsList(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.ToolsListResponse {
    const listed = obj.get("tools") orelse return DecodeError.MissingField;
    if (listed != .array) return DecodeError.InvalidField;
    const session_id = try optionalOwnedString(obj, "session_id", allocator);
    errdefer if (session_id) |owned| allocator.free(owned);
    const sources: []oap_types.ToolSourceDescriptor = if (obj.get("sources")) |declared|
        try deserializeSources(declared, allocator)
    else
        &.{};
    errdefer {
        for (sources) |*entry| entry.deinit(allocator);
        allocator.free(sources);
    }
    const tools = try allocator.alloc(oap_types.ToolDefinition, listed.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (tools[0..filled]) |*entry| entry.deinit(allocator);
        allocator.free(tools);
    }
    for (listed.array.items) |item| {
        tools[filled] = try deserializeToolDefinition(item, allocator);
        filled += 1;
    }
    return .{ .session_id = session_id, .sources = sources, .tools = tools };
}

fn deserializeAnswers(value: std.json.Value, allocator: std.mem.Allocator) ![]oap_types.InputAnswer {
    if (value != .array or value.array.items.len == 0) return DecodeError.InvalidField;
    const answers = try allocator.alloc(oap_types.InputAnswer, value.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (answers[0..filled]) |*entry| entry.deinit(allocator);
        allocator.free(answers);
    }
    for (value.array.items) |item| {
        if (item != .object) return DecodeError.InvalidField;
        const question_id = try requiredOwnedString(item.object, "question_id", allocator);
        errdefer allocator.free(question_id);
        const text = if (item.object.get("text")) |written| blk: {
            if (written != .string) return DecodeError.InvalidField;
            break :blk try allocator.dupe(u8, written.string);
        } else null;
        errdefer if (text) |owned| allocator.free(owned);
        const selected: []const []const u8 = if (item.object.get("selected_option_ids") != null)
            try deserializeStringArray(item.object, "selected_option_ids", allocator)
        else
            &.{};
        answers[filled] = .{ .question_id = question_id, .text = text, .selected_option_ids = selected };
        filled += 1;
    }
    return answers;
}

const InteractionScope = struct {
    interaction_id: []const u8,
    requested_by: []const u8,
    responded_by: []const u8,
    session_id: []const u8,
    run_id: []const u8,

    fn decode(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !InteractionScope {
        const interaction_id = try requiredOwnedString(obj, "interaction_id", allocator);
        errdefer allocator.free(interaction_id);
        const requested_by = try requiredOwnedString(obj, "requested_by", allocator);
        errdefer allocator.free(requested_by);
        const responded_by = try requiredOwnedString(obj, "responded_by", allocator);
        errdefer allocator.free(responded_by);
        const session_id = try requiredOwnedString(obj, "session_id", allocator);
        errdefer allocator.free(session_id);
        const run_id = try requiredOwnedString(obj, "run_id", allocator);
        return .{
            .interaction_id = interaction_id,
            .requested_by = requested_by,
            .responded_by = responded_by,
            .session_id = session_id,
            .run_id = run_id,
        };
    }

    fn deinit(self: InteractionScope, allocator: std.mem.Allocator) void {
        allocator.free(self.interaction_id);
        allocator.free(self.requested_by);
        allocator.free(self.responded_by);
        allocator.free(self.session_id);
        allocator.free(self.run_id);
    }
};

fn deserializeInputResolve(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.UserInputResolveRequest {
    const listed = obj.get("answers") orelse return DecodeError.MissingField;
    const scope = try InteractionScope.decode(obj, allocator);
    errdefer scope.deinit(allocator);
    const answers = try deserializeAnswers(listed, allocator);
    return .{
        .interaction_id = scope.interaction_id,
        .requested_by = scope.requested_by,
        .responded_by = scope.responded_by,
        .session_id = scope.session_id,
        .run_id = scope.run_id,
        .answers = answers,
    };
}

fn deserializePermissionResolve(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.PermissionResolveRequest {
    const granted = try requiredBool(obj, "granted");
    const scope = try InteractionScope.decode(obj, allocator);
    errdefer scope.deinit(allocator);
    const choice_id = if (obj.get("choice_id")) |chosen| blk: {
        if (chosen != .string) return DecodeError.InvalidField;
        break :blk try allocator.dupe(u8, chosen.string);
    } else null;
    errdefer if (choice_id) |owned| allocator.free(owned);
    const reason = if (obj.get("reason")) |given| blk: {
        if (given != .string) return DecodeError.InvalidField;
        break :blk try allocator.dupe(u8, given.string);
    } else null;
    errdefer if (reason) |owned| allocator.free(owned);
    const updated_arguments_json = try optionalRawJson(obj, "updated_arguments_json", allocator);
    return .{
        .interaction_id = scope.interaction_id,
        .requested_by = scope.requested_by,
        .responded_by = scope.responded_by,
        .session_id = scope.session_id,
        .run_id = scope.run_id,
        .granted = granted,
        .choice_id = choice_id,
        .reason = reason,
        .updated_arguments_json = updated_arguments_json,
    };
}

fn deserializeResolveResponse(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.InteractionResolveResponse {
    const accepted = try requiredBool(obj, "accepted");
    const interaction_id = try requiredOwnedString(obj, "interaction_id", allocator);
    errdefer allocator.free(interaction_id);
    const session_id = try requiredOwnedString(obj, "session_id", allocator);
    errdefer allocator.free(session_id);
    const run_id = try requiredOwnedString(obj, "run_id", allocator);
    return .{ .interaction_id = interaction_id, .session_id = session_id, .run_id = run_id, .accepted = accepted };
}

fn deserializeCallResolve(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.CallResolveRequest {
    const started = if (obj.get("started")) |marker| blk: {
        if (marker != .object) return DecodeError.InvalidField;
        break :blk true;
    } else false;
    const interaction_id = try requiredOwnedString(obj, "interaction_id", allocator);
    errdefer allocator.free(interaction_id);
    const session_id = try requiredOwnedString(obj, "session_id", allocator);
    errdefer allocator.free(session_id);
    const run_id = try requiredOwnedString(obj, "run_id", allocator);
    errdefer allocator.free(run_id);
    const tool_call_id = try requiredOwnedString(obj, "tool_call_id", allocator);
    errdefer allocator.free(tool_call_id);
    const requested_by = try requiredOwnedString(obj, "requested_by", allocator);
    errdefer allocator.free(requested_by);
    const responded_by = try requiredOwnedString(obj, "responded_by", allocator);
    errdefer allocator.free(responded_by);
    const result_json = try optionalRawJson(obj, "result", allocator);
    errdefer if (result_json) |owned| allocator.free(owned);
    const failure: ?oap_types.ProtocolError = if (obj.get("error")) |raised|
        try deserializeProtocolError(raised, allocator)
    else
        null;
    return .{
        .interaction_id = interaction_id,
        .session_id = session_id,
        .run_id = run_id,
        .tool_call_id = tool_call_id,
        .requested_by = requested_by,
        .responded_by = responded_by,
        .started = started,
        .result_json = result_json,
        .err = failure,
    };
}

fn deserializeCallResolveResponse(obj: std.json.ObjectMap, allocator: std.mem.Allocator) !oap_types.CallResolveResponse {
    const accepted = try requiredBool(obj, "accepted");
    const settled = if (obj.get("details")) |details| blk: {
        if (details != .object) return DecodeError.InvalidField;
        break :blk details.object.get("settlement_id");
    } else null;
    if (settled) |value| {
        if (value != .string or value.string.len == 0) return DecodeError.InvalidField;
    }
    const interaction_id = try requiredOwnedString(obj, "interaction_id", allocator);
    errdefer allocator.free(interaction_id);
    const session_id = try requiredOwnedString(obj, "session_id", allocator);
    errdefer allocator.free(session_id);
    const run_id = try requiredOwnedString(obj, "run_id", allocator);
    errdefer allocator.free(run_id);
    const tool_call_id = try requiredOwnedString(obj, "tool_call_id", allocator);
    errdefer allocator.free(tool_call_id);
    const reason = try optionalOwnedString(obj, "reason", allocator);
    errdefer if (reason) |owned| allocator.free(owned);
    const settlement_id: ?[]const u8 = if (settled) |value| try allocator.dupe(u8, value.string) else null;
    return .{
        .interaction_id = interaction_id,
        .session_id = session_id,
        .run_id = run_id,
        .tool_call_id = tool_call_id,
        .accepted = accepted,
        .reason = reason,
        .settlement_id = settlement_id,
    };
}

fn deserializeSubmitRequest(
    obj: std.json.ObjectMap,
    allocator: std.mem.Allocator,
) !oap_types.MessageSubmitRequest {
    const session_id = try requiredOwnedString(obj, "session_id", allocator);
    errdefer allocator.free(session_id);

    const messages_value = obj.get("messages") orelse return DecodeError.MissingField;
    if (messages_value != .array or messages_value.array.items.len == 0) return DecodeError.InvalidField;
    const messages = try allocator.alloc(oap_types.Message, messages_value.array.items.len);
    var filled: usize = 0;
    errdefer {
        for (messages[0..filled]) |*message| message.deinit(allocator);
        allocator.free(messages);
    }
    for (messages_value.array.items, 0..) |item, index| {
        messages[index] = try deserializeMessage(item, allocator);
        filled = index + 1;
    }

    const delivery = try requiredEnum(oap_types.RequestedDelivery, obj, "delivery");

    const model_id = if (obj.get("model_id")) |value| blk: {
        if (value != .string) return DecodeError.InvalidField;
        break :blk try allocator.dupe(u8, value.string);
    } else null;
    errdefer if (model_id) |owned| allocator.free(owned);

    const instructions = if (obj.get("instructions")) |value| blk: {
        if (value != .string) return DecodeError.InvalidField;
        break :blk try allocator.dupe(u8, value.string);
    } else null;
    errdefer if (instructions) |owned| allocator.free(owned);

    const tool_choice_json = try optionalRawJson(obj, "tool_choice", allocator);
    errdefer if (tool_choice_json) |owned| allocator.free(owned);
    const output_schema_json = try optionalRawJson(obj, "output_schema", allocator);
    errdefer if (output_schema_json) |owned| allocator.free(owned);

    var allow_degraded: []const []const u8 = &.{};
    if (obj.get("allow_degraded_features") != null) {
        allow_degraded = try deserializeStringArray(obj, "allow_degraded_features", allocator);
    }

    return .{
        .session_id = session_id,
        .messages = messages,
        .delivery = delivery,
        .model_id = model_id,
        .instructions = instructions,
        .tool_choice_json = tool_choice_json,
        .output_schema_json = output_schema_json,
        .allow_degraded_features = allow_degraded,
    };
}

fn deserializeCapabilities(
    obj: std.json.ObjectMap,
    allocator: std.mem.Allocator,
) !oap_types.CapabilitiesResponse {
    const endpoint_value = obj.get("endpoint") orelse return DecodeError.MissingField;
    const endpoint = try deserializeEndpoint(endpoint_value, allocator);

    var result = oap_types.CapabilitiesResponse{ .endpoint = endpoint };
    errdefer result.deinit(allocator);

    if (obj.get("protocol_versions") != null) {
        result.protocol_versions = try deserializeStringArray(obj, "protocol_versions", allocator);
    }
    if (obj.get("profiles") != null) {
        result.profiles = try deserializeStringArray(obj, "profiles", allocator);
    }
    if (obj.get("bindings")) |value| {
        if (value != .array) return DecodeError.InvalidField;
        const bindings = try allocator.alloc(oap_types.Binding, value.array.items.len);
        var filled: usize = 0;
        errdefer {
            for (bindings[0..filled]) |*binding| binding.deinit(allocator);
            allocator.free(bindings);
        }
        for (value.array.items, 0..) |item, index| {
            if (item != .object) return DecodeError.InvalidField;
            const kind = try requiredOwnedString(item.object, "kind", allocator);
            errdefer allocator.free(kind);
            const serialization = try optionalOwnedString(item.object, "serialization", allocator);
            bindings[index] = .{ .kind = kind, .serialization = serialization };
            filled = index + 1;
        }
        result.bindings = bindings;
    }
    if (obj.get("features")) |value| {
        result.features = try deserializeFeatureMap(value, allocator);
    }
    if (obj.get("tools")) |value| {
        if (value != .array) return DecodeError.InvalidField;
        const tools = try allocator.alloc(oap_types.ToolDefinition, value.array.items.len);
        var filled: usize = 0;
        errdefer {
            for (tools[0..filled]) |*tool| tool.deinit(allocator);
            allocator.free(tools);
        }
        for (value.array.items) |item| {
            tools[filled] = try deserializeToolDefinition(item, allocator);
            filled += 1;
        }
        result.tools = tools;
    }
    if (obj.get("sources")) |value| {
        result.sources = try deserializeSources(value, allocator);
    }
    if (obj.get("limits")) |value| {
        if (value != .object) return DecodeError.InvalidField;
        const active = try optionalUnsigned(value.object, "max_active_runs_per_session");
        const queued = try optionalUnsigned(value.object, "max_queued_runs_per_session");
        result.limits = .{
            .max_active_runs_per_session = if (active) |count| std.math.cast(u32, count) orelse return DecodeError.InvalidField else null,
            .max_queued_runs_per_session = if (queued) |count| std.math.cast(u32, count) orelse return DecodeError.InvalidField else null,
        };
    }
    if (obj.get("layers")) |layers| {
        if (layers != .object) return DecodeError.InvalidField;
        if (layers.object.get("agent_loop")) |agent_loop| {
            if (agent_loop != .object) return DecodeError.InvalidField;
            result.requested_delivery_modes = try decodeEnumList(
                oap_types.RequestedDelivery,
                agent_loop.object,
                "requested_delivery_modes",
                allocator,
            );
            result.effective_delivery_modes = try decodeEnumList(
                oap_types.EffectiveDelivery,
                agent_loop.object,
                "effective_delivery_modes",
                allocator,
            );
        }
    }
    if (obj.get("degradation")) |value| {
        if (value != .array) return DecodeError.InvalidField;
        const records = try allocator.alloc(oap_types.Degradation, value.array.items.len);
        var filled: usize = 0;
        errdefer {
            for (records[0..filled]) |*record| record.deinit(allocator);
            allocator.free(records);
        }
        for (value.array.items, 0..) |item, index| {
            if (item != .object) return DecodeError.InvalidField;
            const feature = try requiredOwnedString(item.object, "feature", allocator);
            errdefer allocator.free(feature);
            const reason = try requiredOwnedString(item.object, "reason", allocator);
            errdefer allocator.free(reason);
            const from = try optionalEnum(oap_types.SupportLevel, item.object, "from");
            const to = try requiredEnum(oap_types.SupportLevel, item.object, "to");
            records[index] = .{
                .feature = feature,
                .from = from,
                .to = to,
                .reason = reason,
            };
            filled = index + 1;
        }
        result.degradation = records;
    }

    return result;
}

test "a carry on a content part kind that cannot hold one is refused rather than ignored" {
    const allocator = std.testing.allocator;

    const refused = [_][]const u8{
        "{\"type\":\"text\",\"text\":\"spoken\",\"carry\":\"sig\"}",
        "{\"type\":\"tool_result\",\"tool_call_id\":\"c1\",\"result\":\"ok\",\"carry\":\"sig\"}",
        "{\"type\":\"reasoning\",\"reasoning\":\"prior\",\"carry\":\"\"}",
        "{\"type\":\"tool_call\",\"tool_call_id\":\"c1\",\"name\":\"s\",\"arguments_json\":\"{}\",\"carry\":\"\"}",
    };
    for (refused) |raw| {
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, raw, .{});
        defer parsed.deinit();
        try std.testing.expectError(DecodeError.InvalidField, deserializeContentPart(parsed.value, allocator));
    }

    const accepted = "{\"type\":\"reasoning\",\"reasoning\":\"prior\",\"carry\":\"sig\"}";
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, accepted, .{});
    defer parsed.deinit();
    var part = try deserializeContentPart(parsed.value, allocator);
    defer part.deinit(allocator);
    try std.testing.expectEqualStrings("sig", part.reasoning.carry orelse "");
}

test "round trips a message submit request" {
    const allocator = std.testing.allocator;

    var parts = [_]oap_types.ContentPart{.{ .text = "hello" }};
    var messages = [_]oap_types.Message{.{
        .role = .user,
        .content = .{ .parts = &parts },
    }};
    const request = oap_types.Envelope{
        .id = "req-1",
        .session_id = "sess-1",
        .payload = .{ .message_submit_request = .{
            .session_id = "sess-1",
            .messages = &messages,
            .delivery = .auto,
            .model_id = "anthropic/anthropic-messages@claude",
        } },
    };

    const line = try serializeEnvelope(request, allocator);
    defer allocator.free(line);

    var decoded = try deserializeEnvelope(line, allocator);
    defer decoded.deinit(allocator);

    try std.testing.expectEqualStrings("req-1", decoded.id);
    try std.testing.expectEqualStrings("sess-1", decoded.session_id.?);
    const submit = decoded.payload.message_submit_request;
    try std.testing.expectEqual(oap_types.RequestedDelivery.auto, submit.delivery);
    try std.testing.expectEqual(@as(usize, 1), submit.messages.len);
    try std.testing.expectEqual(oap_types.Role.user, submit.messages[0].role);
    try std.testing.expectEqualStrings("hello", submit.messages[0].content.parts[0].text);
    try std.testing.expectEqualStrings("anthropic/anthropic-messages@claude", submit.model_id.?);
}

test "round trips a run completed terminal with usage" {
    const allocator = std.testing.allocator;

    const completed = oap_types.Envelope{
        .id = "ev-1",
        .session_id = "sess-1",
        .run_id = "run-1",
        .sequence = 4,
        .payload = .{ .run_completed = .{
            .session_id = "sess-1",
            .run_id = "run-1",
            .final_response = .{ .role = .assistant, .content = .{ .text = "done" } },
            .stop_reason = "end_turn",
            .usage = .{ .input_tokens = 12, .output_tokens = 5, .total_tokens = 17 },
            .duration_ms = 42,
        } },
    };

    const line = try serializeEnvelope(completed, allocator);
    defer allocator.free(line);

    var decoded = try deserializeEnvelope(line, allocator);
    defer decoded.deinit(allocator);

    try std.testing.expectEqual(@as(u64, 4), decoded.sequence.?);
    const payload = decoded.payload.run_completed;
    try std.testing.expectEqualStrings("end_turn", payload.stop_reason);
    try std.testing.expectEqualStrings("done", payload.final_response.content.text);
    try std.testing.expectEqual(@as(u64, 17), payload.usage.total_tokens.?);
    try std.testing.expectEqual(@as(u64, 42), payload.duration_ms.?);
}

test "round trips a typed error response with details" {
    const allocator = std.testing.allocator;

    const envelope = oap_types.Envelope{
        .id = "err-1",
        .in_reply_to = "req-9",
        .payload = .{ .error_response = .{
            .code = oap_types.EmittedErrorCode.unsupported_feature.text(),
            .message = "run.instructions is not advertised",
            .retriable = false,
            .details = &.{
                .{ .key = "feature", .value = "run.instructions" },
                .{ .key = "reason", .value = "unadvertised" },
            },
        } },
    };

    const line = try serializeEnvelope(envelope, allocator);
    defer allocator.free(line);

    var decoded = try deserializeEnvelope(line, allocator);
    defer decoded.deinit(allocator);

    try std.testing.expectEqualStrings("req-9", decoded.in_reply_to.?);
    const err = decoded.payload.error_response;
    try std.testing.expectEqualStrings("unsupported_feature", err.code);
    try std.testing.expectEqual(false, err.retriable.?);
    try std.testing.expectEqualStrings("run.instructions", err.detail("feature").?);
    try std.testing.expectEqualStrings("unadvertised", err.detail("reason").?);
}

test "round trips a capabilities response with features and degradation" {
    const allocator = std.testing.allocator;

    var bindings = [_]oap_types.Binding{.{ .kind = "stdio", .serialization = "jsonl" }};
    var features = [_]oap_types.Feature{
        .{ .key = "run.cancel", .level = .degraded, .reason = "session scoped" },
        .{ .key = "run.model_selection", .level = .native, .scope = "run" },
    };
    var degradation = [_]oap_types.Degradation{.{
        .feature = "run.cancel",
        .from = .native,
        .to = .degraded,
        .reason = "cancellation tears the session down",
    }};
    const versions = [_][]const u8{"0.1"};
    const profiles = [_][]const u8{oap_types.PROFILE};
    const requested = [_]oap_types.RequestedDelivery{.auto};
    const effective = [_]oap_types.EffectiveDelivery{.start};

    const envelope = oap_types.Envelope{
        .id = "cap-1",
        .in_reply_to = "req-2",
        .capability_revision = "rev-1",
        .payload = .{ .capabilities_response = .{
            .endpoint = .{ .id = "makai", .name = "Makai", .version = "0.2.0" },
            .protocol_versions = &versions,
            .profiles = &profiles,
            .bindings = &bindings,
            .features = &features,
            .requested_delivery_modes = &requested,
            .effective_delivery_modes = &effective,
            .degradation = &degradation,
        } },
    };

    const line = try serializeEnvelope(envelope, allocator);
    defer allocator.free(line);

    var decoded = try deserializeEnvelope(line, allocator);
    defer decoded.deinit(allocator);

    try std.testing.expectEqualStrings("rev-1", decoded.capability_revision.?);
    const capabilities = decoded.payload.capabilities_response;
    try std.testing.expectEqualStrings("makai", capabilities.endpoint.id);
    try std.testing.expectEqual(oap_types.SupportLevel.degraded, capabilities.feature("run.cancel").?.level);
    try std.testing.expectEqualStrings("run", capabilities.feature("run.model_selection").?.scope.?);
    try std.testing.expectEqual(@as(usize, 1), capabilities.degradation.len);
    try std.testing.expectEqual(oap_types.SupportLevel.degraded, capabilities.degradation[0].to);
    try std.testing.expectEqual(@as(usize, 1), capabilities.requested_delivery_modes.len);
    try std.testing.expectEqual(oap_types.RequestedDelivery.auto, capabilities.requested_delivery_modes[0]);
    try std.testing.expectEqual(@as(usize, 1), capabilities.effective_delivery_modes.len);
    try std.testing.expectEqual(oap_types.EffectiveDelivery.start, capabilities.effective_delivery_modes[0]);
}

test "rejects an unknown delivery mode in a capabilities response" {
    const allocator = std.testing.allocator;

    const line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
        "\",\"type\":\"capabilities.response\",\"id\":\"cap-2\",\"payload\":{\"endpoint\":{\"id\":\"makai\"}," ++
        "\"layers\":{\"agent_loop\":{\"requested_delivery_modes\":[\"telepathy\"]}}}}";

    try std.testing.expectError(DecodeError.InvalidField, deserializeEnvelope(line, allocator));
}

test "a recognized frame that fails validation late frees what it already decoded" {
    const allocator = std.testing.allocator;

    const prefix = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++
        oap_types.PROFILE ++ "\",\"id\":\"e-1\",\"type\":\"";

    const lines = [_][]const u8{
        prefix ++ "run.cancel.response\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\"}}",
        prefix ++ "run.cancel.response\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"accepted\":true,\"status\":\"levitating\"}}",
        prefix ++ "run.status.updated\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"status\":\"levitating\"}}",
        prefix ++ "run.started\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"model_id\":\"m\",\"started_at_ms\":\"soon\"}}",
        prefix ++ "run.cancelled\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"reason\":\"why\",\"duration_ms\":\"later\"}}",
        prefix ++ "run.failed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"error\":{\"code\":\"internal_error\",\"message\":\"boom\"},\"duration_ms\":\"later\"}}",
        prefix ++ "run.completed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"final_response\":{\"role\":\"assistant\",\"content\":\"hi\"},\"stop_reason\":\"end_turn\",\"model_id\":\"m\",\"duration_ms\":\"later\"}}",
        prefix ++ "session.message.submit.response\",\"payload\":{\"session_id\":\"s\",\"submission_id\":\"sub\",\"delivery_resolution\":\"why\",\"run_id\":\"r\",\"model_id\":\"m\",\"accepted\":true,\"requested_delivery\":\"auto\",\"effective_delivery\":\"start\",\"admission\":\"telepathic\"}}",
        prefix ++ "session.state.response\",\"payload\":{\"session_id\":\"s\",\"status\":\"open\",\"active_run_id\":\"r\",\"current_model_id\":\"m\",\"updated_at_ms\":\"soon\"}}",
        prefix ++ "capabilities.response\",\"payload\":{\"endpoint\":{\"id\":\"makai\"},\"degradation\":[{\"feature\":\"run.cancel\",\"reason\":\"why\",\"to\":\"levitating\"}]}}",
        prefix ++ "capabilities.response\",\"payload\":{\"endpoint\":{\"id\":\"makai\"},\"layers\":{\"agent_loop\":{\"effective_delivery_modes\":[\"telepathy\"]}}}}",
    };

    for (lines) |line| {
        if (deserializeEnvelope(line, allocator)) |decoded| {
            var owned = decoded;
            owned.deinit(allocator);
            return error.TestExpectedDecodeFailure;
        } else |_| {}
    }
}

test "rejects a foreign protocol, version, or profile" {
    const allocator = std.testing.allocator;

    try std.testing.expectError(DecodeError.ProtocolMismatch, deserializeEnvelope(
        "{\"protocol\":\"other\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++ "\",\"type\":\"capabilities.request\",\"id\":\"a\",\"payload\":{}}",
        allocator,
    ));
    try std.testing.expectError(DecodeError.VersionMismatch, deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"9.9\",\"profile\":\"" ++ oap_types.PROFILE ++ "\",\"type\":\"capabilities.request\",\"id\":\"a\",\"payload\":{}}",
        allocator,
    ));
    try std.testing.expectError(DecodeError.ProfileMismatch, deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"other\",\"type\":\"capabilities.request\",\"id\":\"a\",\"payload\":{}}",
        allocator,
    ));
}

test "rejects malformed and unknown envelopes" {
    const allocator = std.testing.allocator;

    try std.testing.expectError(DecodeError.InvalidEnvelope, deserializeEnvelope("not json", allocator));
    try std.testing.expectError(DecodeError.InvalidEnvelope, deserializeEnvelope("[]", allocator));
    try std.testing.expectError(DecodeError.MissingField, deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++ "\",\"type\":\"capabilities.request\",\"id\":\"a\"}",
        allocator,
    ));
    try std.testing.expectError(DecodeError.UnknownEnvelopeType, deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++ "\",\"type\":\"session.rename.request\",\"id\":\"a\",\"payload\":{}}",
        allocator,
    ));
    try std.testing.expectError(DecodeError.InvalidField, deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++ "\",\"type\":\"capabilities.request\",\"id\":\"a\",\"payload\":{},\"sequence\":0}",
        allocator,
    ));
}

test "a present but empty run control survives decoding as a present control" {
    const allocator = std.testing.allocator;

    var decoded = try deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
            "\",\"type\":\"session.message.submit.request\",\"id\":\"a\",\"session_id\":\"s\"," ++
            "\"payload\":{\"session_id\":\"s\",\"delivery\":\"auto\",\"model_id\":\"\"," ++
            "\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}",
        allocator,
    );
    defer decoded.deinit(allocator);

    const submit = decoded.payload.message_submit_request;
    try std.testing.expect(submit.model_id != null);
    try std.testing.expectEqual(@as(usize, 0), submit.model_id.?.len);
    try std.testing.expectEqualStrings("hi", submit.messages[0].content.text);
}

test "round trips reasoning and tool content parts" {
    const allocator = std.testing.allocator;

    const reasoning = oap_types.Envelope{
        .id = "ev-r",
        .session_id = "s",
        .run_id = "r",
        .sequence = 2,
        .payload = .{ .content_delta = .{
            .session_id = "s",
            .run_id = "r",
            .message_id = "m",
            .part = .{ .reasoning = .{ .text = "thinking" } },
        } },
    };
    const reasoning_line = try serializeEnvelope(reasoning, allocator);
    defer allocator.free(reasoning_line);
    var decoded_reasoning = try deserializeEnvelope(reasoning_line, allocator);
    defer decoded_reasoning.deinit(allocator);
    try std.testing.expectEqualStrings("thinking", decoded_reasoning.payload.content_delta.part.reasoning.text);
    try std.testing.expectEqualStrings("m", decoded_reasoning.payload.content_delta.message_id.?);

    const call = oap_types.Envelope{
        .id = "ev-c",
        .session_id = "s",
        .run_id = "r",
        .sequence = 3,
        .payload = .{ .content_delta = .{
            .session_id = "s",
            .run_id = "r",
            .part = .{ .tool_call = .{
                .tool_call_id = "call-1",
                .name = "read",
                .arguments_json = "{\"path\":\"a.txt\"}",
            } },
        } },
    };
    const call_line = try serializeEnvelope(call, allocator);
    defer allocator.free(call_line);
    var decoded_call = try deserializeEnvelope(call_line, allocator);
    defer decoded_call.deinit(allocator);
    try std.testing.expectEqualStrings("call-1", decoded_call.payload.content_delta.part.tool_call.tool_call_id);
    try std.testing.expectEqualStrings("read", decoded_call.payload.content_delta.part.tool_call.name);
}

test "serialized envelopes always carry the protocol triple" {
    const allocator = std.testing.allocator;

    const envelope = oap_types.Envelope{
        .id = "x",
        .payload = .{ .capabilities_request = {} },
    };
    const line = try serializeEnvelope(envelope, allocator);
    defer allocator.free(line);

    try std.testing.expect(std.mem.indexOf(u8, line, "\"protocol\":\"open-agent-protocol\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, line, "\"version\":\"0.1\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, line, "\"profile\":\"" ++ oap_types.PROFILE ++ "\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, line, "\"type\":\"capabilities.request\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, line, "\n") == null);
}

test "an error code outside this endpoint's own set decodes as a value and survives a round trip" {
    const allocator = std.testing.allocator;

    const prefix = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++
        oap_types.PROFILE ++ "\",\"id\":\"e-1\",\"type\":\"";

    const cases = [_]struct { line: []const u8, code: []const u8 }{
        .{
            .line = prefix ++ "error.response\",\"in_reply_to\":\"r-1\",\"payload\":{\"error\":{\"code\":\"claude_api_429\",\"message\":\"rate limited\"}}}",
            .code = "claude_api_429",
        },
        .{
            .line = prefix ++ "error.response\",\"in_reply_to\":\"r-1\",\"payload\":{\"error\":{\"code\":\"com.example.storage.object_not_found\",\"message\":\"absent\"}}}",
            .code = "com.example.storage.object_not_found",
        },
        .{
            .line = prefix ++ "run.failed\",\"payload\":{\"session_id\":\"s\",\"run_id\":\"r\",\"error\":{\"code\":\"hermes_rate_limited\",\"message\":\"slow down\"}}}",
            .code = "hermes_rate_limited",
        },
    };

    for (cases) |case| {
        var decoded = try deserializeEnvelope(case.line, allocator);
        defer decoded.deinit(allocator);

        const decoded_code = switch (decoded.payload) {
            .error_response => |err| err.code,
            .run_failed => |failed| failed.err.code,
            else => return error.TestUnexpectedPayload,
        };
        try std.testing.expectEqualStrings(case.code, decoded_code);

        const line = try serializeEnvelope(decoded, allocator);
        defer allocator.free(line);
        const quoted = try std.fmt.allocPrint(allocator, "\"code\":\"{s}\"", .{case.code});
        defer allocator.free(quoted);
        try std.testing.expect(std.mem.indexOf(u8, line, quoted) != null);
    }
}

fn roundTrip(envelope: oap_types.Envelope, allocator: std.mem.Allocator) !oap_types.Envelope {
    const line = try serializeEnvelope(envelope, allocator);
    defer allocator.free(line);
    return deserializeEnvelope(line, allocator);
}

test "a user input resolution round trips every answer shape" {
    const allocator = std.testing.allocator;

    var answers = [_]oap_types.InputAnswer{
        .{ .question_id = "decision", .selected_option_ids = &.{"allow"} },
        .{ .question_id = "note", .text = "go ahead" },
    };
    var decoded = try roundTrip(.{
        .id = "resolve-1",
        .session_id = "s",
        .run_id = "r",
        .capability_revision = "rev",
        .payload = .{ .user_input_resolve_request = .{
            .interaction_id = "i-1",
            .requested_by = "claude-code.cli",
            .responded_by = "user",
            .session_id = "s",
            .run_id = "r",
            .answers = &answers,
        } },
    }, allocator);
    defer decoded.deinit(allocator);

    const resolution = decoded.payload.user_input_resolve_request;
    try std.testing.expectEqualStrings("i-1", resolution.interaction_id);
    try std.testing.expectEqualStrings("claude-code.cli", resolution.requested_by);
    try std.testing.expectEqualStrings("user", resolution.responded_by);
    try std.testing.expectEqual(@as(usize, 2), resolution.answers.len);
    try std.testing.expectEqualStrings("allow", resolution.answers[0].selected_option_ids[0]);
    try std.testing.expect(resolution.answers[0].text == null);
    try std.testing.expectEqualStrings("go ahead", resolution.answers[1].text.?);
    try std.testing.expectEqual(@as(usize, 0), resolution.answers[1].selected_option_ids.len);
}

test "a user input resolution with no answers is refused rather than decoded empty" {
    const allocator = std.testing.allocator;
    try std.testing.expectError(DecodeError.InvalidField, deserializeEnvelope(
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
            "\",\"type\":\"user.input.resolve.request\",\"id\":\"a\",\"payload\":{\"interaction_id\":\"i\"," ++
            "\"requested_by\":\"e\",\"responded_by\":\"u\",\"session_id\":\"s\",\"run_id\":\"r\",\"answers\":[]}}",
        allocator,
    ));
}

test "a permission resolution round trips its optional members" {
    const allocator = std.testing.allocator;

    var decoded = try roundTrip(.{
        .id = "resolve-2",
        .payload = .{ .permission_resolve_request = .{
            .interaction_id = "i-2",
            .requested_by = "e",
            .responded_by = "u",
            .session_id = "s",
            .run_id = "r",
            .granted = false,
            .choice_id = "deny-once",
            .reason = "not today",
            .updated_arguments_json = "{\"path\":\"b\"}",
        } },
    }, allocator);
    defer decoded.deinit(allocator);

    const resolution = decoded.payload.permission_resolve_request;
    try std.testing.expect(!resolution.granted);
    try std.testing.expectEqualStrings("deny-once", resolution.choice_id.?);
    try std.testing.expectEqualStrings("not today", resolution.reason.?);
    try std.testing.expectEqualStrings("{\"path\":\"b\"}", resolution.updated_arguments_json.?);
}

test "both interaction acknowledgements round trip under their own type" {
    const allocator = std.testing.allocator;

    const acknowledgement = oap_types.InteractionResolveResponse{ .interaction_id = "i", .session_id = "s", .run_id = "r", .accepted = true };
    var input = try roundTrip(.{ .id = "a-1", .payload = .{ .user_input_resolve_response = acknowledgement } }, allocator);
    defer input.deinit(allocator);
    try std.testing.expect(input.payload.user_input_resolve_response.accepted);
    try std.testing.expectEqualStrings("user.input.resolve.response", input.payload.typeName());

    var permission = try roundTrip(.{ .id = "a-2", .payload = .{ .permission_resolve_response = acknowledgement } }, allocator);
    defer permission.deinit(allocator);
    try std.testing.expectEqualStrings("i", permission.payload.permission_resolve_response.interaction_id);
    try std.testing.expectEqualStrings("action.permission.resolve.response", permission.payload.typeName());
}

test "an action call resolution and its refusal round trip" {
    const allocator = std.testing.allocator;

    var request = try roundTrip(.{ .id = "call-1", .payload = .{ .call_resolve_request = .{
        .interaction_id = "i",
        .session_id = "s",
        .run_id = "r",
        .tool_call_id = "t",
        .requested_by = "e",
        .responded_by = "u",
        .result_json = "{\"ok\":true}",
    } } }, allocator);
    defer request.deinit(allocator);
    try std.testing.expectEqualStrings("{\"ok\":true}", request.payload.call_resolve_request.result_json.?);
    try std.testing.expect(!request.payload.call_resolve_request.started);

    var refusal = try roundTrip(.{ .id = "call-2", .payload = .{ .call_resolve_response = .{
        .interaction_id = "i",
        .session_id = "s",
        .run_id = "r",
        .tool_call_id = "t",
        .accepted = false,
        .reason = "already_resolved",
        .settlement_id = "settled-1",
    } } }, allocator);
    defer refusal.deinit(allocator);
    try std.testing.expectEqualStrings("already_resolved", refusal.payload.call_resolve_response.reason.?);
    try std.testing.expectEqualStrings("settled-1", refusal.payload.call_resolve_response.settlement_id.?);
}

test "a tool catalog round trips its sources and definitions" {
    const allocator = std.testing.allocator;

    var sources = [_]oap_types.ToolSourceDescriptor{
        .{ .id = "claude-code-native", .kind = "native", .display_name = "Claude Code built-in tools" },
        .{ .id = "mcp:files", .kind = "process", .protocol = "mcp", .display_name = "files" },
    };
    var features = [_]oap_types.Feature{.{ .key = "action.tools.execute", .level = .unavailable, .reason = "the CLI executes its own tools" }};
    var tools = [_]oap_types.ToolDefinition{.{
        .name = "Bash",
        .input_schema_json = "{\"type\":\"object\"}",
        .execution_owner = "claude-code",
        .source = "claude-code-native",
        .features = &features,
    }};
    var decoded = try roundTrip(.{ .id = "tools-1", .capability_revision = "rev", .payload = .{ .tools_list_response = .{
        .session_id = "s",
        .sources = &sources,
        .tools = &tools,
    } } }, allocator);
    defer decoded.deinit(allocator);

    const catalog = decoded.payload.tools_list_response;
    try std.testing.expectEqualStrings("s", catalog.session_id.?);
    try std.testing.expectEqual(@as(usize, 2), catalog.sources.len);
    try std.testing.expectEqualStrings("mcp", catalog.sources[1].protocol.?);
    try std.testing.expectEqualStrings("Bash", catalog.tools[0].name);
    try std.testing.expectEqualStrings("{\"type\":\"object\"}", catalog.tools[0].input_schema_json);
    try std.testing.expectEqual(oap_types.SupportLevel.unavailable, catalog.tools[0].features[0].level);

    var request = try roundTrip(.{ .id = "tools-2", .payload = .{ .tools_list_request = .{
        .session_id = "s",
        .allow_degraded_features = &.{"action.tools.list"},
    } } }, allocator);
    defer request.deinit(allocator);
    try std.testing.expect(request.payload.tools_list_request.allowsDegraded("action.tools.list"));
    try std.testing.expect(!request.payload.tools_list_request.allowsDegraded("models.list"));
}

test "an open elects tools and tool sources only when it names at least one" {
    const allocator = std.testing.allocator;
    const prefix = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
        "\",\"type\":\"session.open.request\",\"id\":\"o\",\"payload\":";

    var empty = try deserializeEnvelope(prefix ++ "{\"session_id\":\"s\",\"tools\":[],\"tool_sources\":[]}}", allocator);
    defer empty.deinit(allocator);
    try std.testing.expect(empty.payload.session_open_request.tools_json == null);
    try std.testing.expect(empty.payload.session_open_request.tool_sources_json == null);
    try std.testing.expect(!empty.payload.session_open_request.subscribe);

    var elected = try deserializeEnvelope(prefix ++
        "{\"subscribe\":true,\"tools\":[{\"name\":\"t\"}],\"tool_sources\":[{\"id\":\"x\",\"kind\":\"process\"}]," ++
        "\"message\":{\"delivery\":\"auto\",\"messages\":[]},\"allow_degraded_features\":[\"session.open.subscribe\"]}}", allocator);
    defer elected.deinit(allocator);
    const open = elected.payload.session_open_request;
    try std.testing.expect(open.subscribe);
    try std.testing.expect(open.tools_json != null);
    try std.testing.expect(open.tool_sources_json != null);
    try std.testing.expect(open.message_json != null);
    try std.testing.expect(open.allowsDegraded("session.open.subscribe"));

    try std.testing.expectError(DecodeError.InvalidField, deserializeEnvelope(prefix ++ "{\"tools\":{}}}", allocator));
    try std.testing.expectError(DecodeError.InvalidField, deserializeEnvelope(prefix ++ "{\"message\":[]}}", allocator));
}

test "an open whose provided tool schema nests past 256 levels keeps it whole" {
    const allocator = std.testing.allocator;
    const schema = ("{\"items\":" ** 400) ++ "{}" ++ ("}" ** 400);
    const tools = "[{\"name\":\"t\",\"input_schema\":" ++ schema ++ "}]";
    var opened = try deserializeEnvelope("{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
        "\",\"type\":\"session.open.request\",\"id\":\"o\",\"payload\":{\"session_id\":\"s\",\"tools\":" ++ tools ++ "}}", allocator);
    defer opened.deinit(allocator);
    try std.testing.expectEqualStrings(tools, opened.payload.session_open_request.tools_json.?);
}

test "a capabilities response carries the tool sources it declares" {
    const allocator = std.testing.allocator;

    var sources = [_]oap_types.ToolSourceDescriptor{.{ .id = "claude-code-native", .kind = "native" }};
    var decoded = try roundTrip(.{ .id = "cap", .payload = .{ .capabilities_response = .{
        .endpoint = .{ .id = "claude-code.cli" },
        .sources = &sources,
    } } }, allocator);
    defer decoded.deinit(allocator);
    try std.testing.expectEqual(@as(usize, 1), decoded.payload.capabilities_response.sources.len);
    try std.testing.expectEqualStrings("native", decoded.payload.capabilities_response.sources[0].kind);
}

test "a models request carries the degraded features its caller opts into" {
    const allocator = std.testing.allocator;

    var decoded = try roundTrip(.{ .id = "models", .session_id = "s", .payload = .{ .models_request = .{
        .session_id = "s",
        .allow_degraded_features = &.{"models.list"},
    } } }, allocator);
    defer decoded.deinit(allocator);
    try std.testing.expect(decoded.payload.models_request.allowsDegraded("models.list"));
    try std.testing.expect(!decoded.payload.models_request.allowsDegraded("run.instructions"));
}

test "a capabilities response carries the run limits it discloses" {
    const allocator = std.testing.allocator;

    var decoded = try roundTrip(.{ .id = "cap", .payload = .{ .capabilities_response = .{
        .endpoint = .{ .id = "opencode.server" },
        .limits = .{ .max_active_runs_per_session = 2, .max_queued_runs_per_session = 1 },
    } } }, allocator);
    defer decoded.deinit(allocator);
    const limits = decoded.payload.capabilities_response.limits.?;
    try std.testing.expectEqual(@as(?u32, 2), limits.max_active_runs_per_session);
    try std.testing.expectEqual(@as(?u32, 1), limits.max_queued_runs_per_session);
}

test "an admission names the messages it admitted" {
    const allocator = std.testing.allocator;

    var decoded = try roundTrip(.{ .id = "adm", .payload = .{ .message_submit_response = .{
        .session_id = "s",
        .accepted = true,
        .submission_id = "sub",
        .requested_delivery = .auto,
        .effective_delivery = .start,
        .admission = .started,
        .run_id = "r",
        .status = .running,
        .message_ids = &.{"message-2"},
    } } }, allocator);
    defer decoded.deinit(allocator);
    try std.testing.expectEqual(@as(usize, 1), decoded.payload.message_submit_response.message_ids.len);
    try std.testing.expectEqualStrings("message-2", decoded.payload.message_submit_response.message_ids[0]);
}

fn decodeAndRelease(allocator: std.mem.Allocator, line: []const u8) !void {
    var decoded = try deserializeEnvelope(line, allocator);
    decoded.deinit(allocator);
}

test "every new payload decoder frees what it built when an allocation fails" {
    const prefix = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
        "\",\"id\":\"e-1\",\"type\":\"";
    const lines = [_][]const u8{
        prefix ++ "user.input.resolve.request\",\"payload\":{\"interaction_id\":\"i\",\"requested_by\":\"e\",\"responded_by\":\"u\"," ++
            "\"session_id\":\"s\",\"run_id\":\"r\",\"answers\":[{\"question_id\":\"q\",\"selected_option_ids\":[\"a\",\"b\"]},{\"question_id\":\"n\",\"text\":\"t\"}]}}",
        prefix ++ "action.permission.resolve.request\",\"payload\":{\"interaction_id\":\"i\",\"requested_by\":\"e\",\"responded_by\":\"u\"," ++
            "\"session_id\":\"s\",\"run_id\":\"r\",\"granted\":true,\"choice_id\":\"c\",\"reason\":\"why\",\"updated_arguments_json\":{\"a\":1}}}",
        prefix ++ "action.call.resolve.request\",\"payload\":{\"interaction_id\":\"i\",\"session_id\":\"s\",\"run_id\":\"r\",\"tool_call_id\":\"t\"," ++
            "\"requested_by\":\"e\",\"responded_by\":\"u\",\"result\":{\"x\":2},\"error\":{\"code\":\"c\",\"message\":\"m\",\"details\":{\"k\":\"v\"}}}}",
        prefix ++ "action.call.resolve.response\",\"payload\":{\"interaction_id\":\"i\",\"session_id\":\"s\",\"run_id\":\"r\",\"tool_call_id\":\"t\"," ++
            "\"accepted\":false,\"reason\":\"already_resolved\",\"details\":{\"settlement_id\":\"z\"}}}",
        prefix ++ "action.tools.list.request\",\"payload\":{\"session_id\":\"s\",\"allow_degraded_features\":[\"action.tools.list\"]}}",
        prefix ++ "action.tools.list.response\",\"payload\":{\"session_id\":\"s\",\"sources\":[{\"id\":\"n\",\"kind\":\"native\",\"display_name\":\"d\"}]," ++
            "\"tools\":[{\"name\":\"Bash\",\"description\":\"run\",\"input_schema\":{\"type\":\"object\"},\"execution_owner\":\"o\",\"source\":\"n\"," ++
            "\"features\":{\"action.tools.execute\":{\"level\":\"unavailable\",\"reason\":\"r\"}}}]}}",
        prefix ++ "session.open.request\",\"payload\":{\"session_id\":\"s\",\"subscribe\":true,\"message\":{\"delivery\":\"auto\"}," ++
            "\"tools\":[{\"name\":\"t\"}],\"tool_sources\":[{\"id\":\"x\"}],\"allow_degraded_features\":[\"a\",\"b\"]}}",
        prefix ++ "capabilities.response\",\"payload\":{\"endpoint\":{\"id\":\"e\"},\"features\":{\"run.cancel\":{\"level\":\"degraded\"}}," ++
            "\"sources\":[{\"id\":\"n\",\"kind\":\"native\"},{\"id\":\"m\",\"kind\":\"process\",\"protocol\":\"mcp\",\"endpoint\":\"x\"}]}}",
        prefix ++ "session.message.submit.response\",\"payload\":{\"session_id\":\"s\",\"accepted\":true,\"submission_id\":\"sub\"," ++
            "\"requested_delivery\":\"auto\",\"effective_delivery\":\"start\",\"admission\":\"started\",\"run_id\":\"r\",\"message_ids\":[\"m1\",\"m2\"]}}",
        prefix ++ "session.state.response\",\"payload\":{\"session_id\":\"s\",\"status\":\"queued\",\"active_runs\":[{\"run_id\":\"r\",\"status\":\"queued\"," ++
            "\"relationship\":\"primary\",\"queue_position\":1,\"as_of_sequence\":0,\"admitted_submit_requests\":[\"a\"],\"pending_interactions\":[\"p\"]," ++
            "\"acknowledged_interactions\":[\"k\"]}],\"transcript_cursor\":\"4\",\"metadata\":{\"k\":1},\"sources\":[{\"id\":\"n\",\"kind\":\"native\"}]," ++
            "\"as_of\":{\"admitted_submit_requests\":[\"a\"],\"settled\":[{\"run_id\":\"q\",\"sequence\":3}],\"model_run_sequence\":{\"run_id\":\"q\",\"sequence\":2}}}}",
        prefix ++ "models.response\",\"payload\":{\"session_id\":\"s\",\"models\":[{\"id\":\"m\",\"context_window\":8192}]," ++
            "\"providers\":[{\"id\":\"p\",\"display_name\":\"P\",\"wire\":\"w\",\"kind\":\"direct\"}]}}",
        prefix ++ "capabilities.response\",\"payload\":{\"endpoint\":{\"id\":\"e\"},\"features\":{\"a\":{\"level\":\"emulated\",\"modes\":[\"session_open\"]," ++
            "\"constraints\":{\"fixed_result\":{\"ok\":true}},\"limits\":{\"max_sources\":2}}},\"tools\":[{\"name\":\"t\",\"input_schema\":{},\"execution_owner\":\"o\"}]}}",
    };
    for (lines) |line| {
        try std.testing.checkAllAllocationFailures(std.testing.allocator, decodeAndRelease, .{line});
    }
}

test "session state, models and capabilities round trip the members goap serves" {
    const allocator = std.testing.allocator;
    const line = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
        "\",\"id\":\"e-1\",\"type\":\"session.state.response\",\"payload\":{\"session_id\":\"s\",\"status\":\"waiting_for_input\",\"active_run_id\":\"r\"," ++
        "\"active_runs\":[{\"run_id\":\"r\",\"status\":\"running\",\"relationship\":\"primary\",\"as_of_sequence\":4,\"pending_interactions\":[\"p\"]}]," ++
        "\"transcript_cursor\":\"4\",\"updated_at_ms\":7,\"metadata\":{\"claude_native_session_id\":\"n-1\"},\"sources\":[{\"id\":\"n\",\"kind\":\"native\"}],\"as_of\":{\"settled\":[{\"run_id\":\"q\",\"sequence\":12}]}}}";
    var decoded = try deserializeEnvelope(line, allocator);
    defer decoded.deinit(allocator);
    const state = decoded.payload.session_state_response;
    try std.testing.expectEqual(@as(?u64, 4), state.active_runs[0].as_of_sequence);
    try std.testing.expectEqualStrings("p", state.active_runs[0].pending_interactions[0]);
    try std.testing.expectEqualStrings("4", state.transcript_cursor.?);
    try std.testing.expectEqual(@as(u64, 12), state.as_of.?.settled[0].sequence);
    const encoded = try serializeEnvelope(decoded, allocator);
    defer allocator.free(encoded);
    try std.testing.expect(std.mem.indexOf(u8, encoded, "\"active_runs\":[{\"run_id\":\"r\",\"status\":\"running\",\"relationship\":\"primary\",\"as_of_sequence\":4,\"pending_interactions\":[\"p\"]}]") != null);
    try std.testing.expect(std.mem.indexOf(u8, encoded, "\"sources\":[{\"id\":\"n\",\"kind\":\"native\"}],\"as_of\":{\"settled\":[{\"run_id\":\"q\",\"sequence\":12}]}") != null);
    try std.testing.expect(std.mem.indexOf(u8, encoded, "\"updated_at_ms\":7,\"metadata\":{\"claude_native_session_id\":\"n-1\"},") != null);

    var models = try roundTrip(.{ .id = "m", .payload = .{ .models_response = .{
        .session_id = "s",
        .models = @constCast(&[_]oap_types.ModelDescriptor{.{ .id = "a", .context_window = 8192, .default = true }}),
        .providers = @constCast(&[_]oap_types.ProviderDescriptor{.{ .id = "reference", .wire = "openai-chat-completions", .kind = "direct" }}),
    } } }, allocator);
    defer models.deinit(allocator);
    try std.testing.expectEqual(@as(?u64, 8192), models.payload.models_response.models[0].context_window);
    try std.testing.expectEqualStrings("direct", models.payload.models_response.providers[0].kind.?);

    var capabilities = try roundTrip(.{ .id = "c", .payload = .{ .capabilities_response = .{
        .endpoint = .{ .id = "e" },
        .features = @constCast(&[_]oap_types.Feature{.{ .key = "action.tool_sources.attach", .level = .emulated, .modes = &.{"session_open"}, .limits_json = "{\"max_sources\":2}", .constraints_json = "{\"fixed_result\":{\"ok\":true}}" }}),
        .tools = @constCast(&[_]oap_types.ToolDefinition{.{ .name = "scripted_tool", .input_schema_json = "{\"type\":\"object\"}", .execution_owner = "o" }}),
    } } }, allocator);
    defer capabilities.deinit(allocator);
    const feature = capabilities.payload.capabilities_response.features[0];
    try std.testing.expectEqualStrings("session_open", feature.modes[0]);
    try std.testing.expectEqualStrings("{\"max_sources\":2}", feature.limits_json.?);
    try std.testing.expectEqualStrings("{\"fixed_result\":{\"ok\":true}}", feature.constraints_json.?);
    try std.testing.expectEqualStrings("scripted_tool", capabilities.payload.capabilities_response.tools[0].name);
}

test "a model run sequence at genesis round trips its null run_id, and a settled run must name one" {
    const allocator = std.testing.allocator;
    const prefix = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_types.PROFILE ++
        "\",\"id\":\"e-1\",\"type\":\"session.state.response\",\"payload\":{\"session_id\":\"s\",\"status\":\"idle\",\"as_of\":";
    var decoded = try deserializeEnvelope(prefix ++ "{\"model_run_sequence\":{\"run_id\":null,\"sequence\":0}}}}", allocator);
    defer decoded.deinit(allocator);
    const position = decoded.payload.session_state_response.as_of.?.model_run_sequence.?;
    try std.testing.expect(position.run_id == null);
    const encoded = try serializeEnvelope(decoded, allocator);
    defer allocator.free(encoded);
    try std.testing.expect(std.mem.indexOf(u8, encoded, "\"model_run_sequence\":{\"run_id\":null,\"sequence\":0}") != null);
    try std.testing.expectError(DecodeError.InvalidField, deserializeEnvelope(prefix ++ "{\"settled\":[{\"run_id\":null,\"sequence\":1}]}}}", allocator));
}
