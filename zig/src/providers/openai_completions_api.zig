const std = @import("std");
const provider_catalog = @import("provider_catalog");
const ai_types = @import("ai_types");
const event_stream = @import("event_stream");
const api_registry = @import("api_registry");
const sse_parser = @import("sse_parser");
const error_detail = @import("provider_error_detail");
const json_writer = @import("json_writer");
const github_copilot = @import("github_copilot");
const tool_call_tracker = @import("tool_call_tracker");
const provider_caps = @import("provider_caps");
const sanitize = @import("sanitize");
const retry = @import("retry");
const pre_transform = @import("pre_transform");
const StringBuilder = @import("string_builder").StringBuilder;
const compat_mod = @import("compat");
const version = @import("version_options").version;

const MergedCompat = struct {
    supports_store: bool,
    supports_developer_role: bool,
    supports_reasoning_effort: bool,
    supports_usage_in_streaming: bool,
    max_tokens_field: []const u8,
    requires_tool_result_name: bool,
    requires_assistant_after_tool_result: bool,
    requires_thinking_as_text: bool,
    requires_mistral_tool_ids: bool,
    thinking_format: enum { openai, zai, qwen },
    supports_strict_mode: bool,
};

fn mergeCompat(model: ai_types.Model) MergedCompat {
    const caps = provider_caps.detectCapabilities(model.base_url);
    const compat: ai_types.OpenAICompatOptions = model.compat orelse .{};
    const is_openai_native = provider_caps.isOpenAIHost(model.base_url);
    const honors_native_caps = is_openai_native or isTransparentOpenAIProxy(model);
    const detected_developer_role = if (honors_native_caps) caps.supports_developer_role else false;
    const deepseek_wire = provider_caps.usesDeepSeekWire(model.provider, model.base_url);
    const detected_reasoning_effort = if (deepseek_wire and model.reasoning and caps.supports_reasoning_effort == false and provider_caps.isExplicitDeepSeekVendor(model.provider))
        true
    else if (provider_caps.isOpenCodeGateway(model.provider))
        model.reasoning
    else if (honors_native_caps or deepseek_wire)
        caps.supports_reasoning_effort
    else
        false;
    const detected_max_tokens_field: []const u8 = if (honors_native_caps) caps.max_tokens_field else "max_tokens";

    return .{
        .supports_store = compat.supports_store orelse is_openai_native,
        .supports_developer_role = compat.supports_developer_role orelse detected_developer_role,
        .supports_reasoning_effort = compat.supports_reasoning_effort orelse detected_reasoning_effort,
        .supports_usage_in_streaming = compat.supports_usage_in_streaming orelse true,
        .max_tokens_field = if (compat.max_tokens_field) |field| switch (field) {
            .max_completion_tokens => "max_completion_tokens",
            .max_tokens => "max_tokens",
        } else detected_max_tokens_field,
        .requires_tool_result_name = compat.requires_tool_result_name orelse caps.requires_tool_result_name,
        .requires_assistant_after_tool_result = compat.requires_assistant_after_tool_result orelse caps.requires_assistant_after_tool,
        .requires_thinking_as_text = compat.requires_thinking_as_text orelse caps.requires_thinking_as_text,
        .requires_mistral_tool_ids = compat.requires_mistral_tool_ids orelse caps.requires_mistral_tool_ids,
        .thinking_format = if (compat.thinking_format) |format| switch (format) {
            .openai => .openai,
            .zai => .zai,
            .qwen => .qwen,
        } else switch (caps.thinking_format) {
            .openai => .openai,
            .zai => .zai,
            .qwen => .qwen,
        },
        .supports_strict_mode = compat.supports_strict_mode orelse honors_native_caps,
    };
}

fn isTransparentOpenAIProxy(model: ai_types.Model) bool {
    if (!std.mem.eql(u8, model.provider, "openai")) return false;
    const compat = model.compat orelse return false;
    return compat.supports_store == true and
        compat.supports_developer_role == true and
        compat.supports_reasoning_effort == true;
}

fn allowsAnonymous(model: ai_types.Model) bool {
    if (!model.allows_anonymous) return false;
    const vendors = [_][]const u8{ "openai", "deepseek", "kimi", "github-copilot" };
    for (vendors) |vendor| {
        if (std.mem.eql(u8, model.provider, vendor)) return false;
    }
    return true;
}

fn appendTextContent(msg: ai_types.Message, out: *std.ArrayList(u8), allocator: std.mem.Allocator) !void {
    switch (msg) {
        .user => |u| switch (u.content) {
            .text => |t| try out.appendSlice(allocator, t),
            .parts => |parts| {
                for (parts) |p| {
                    switch (p) {
                        .text => |t| {
                            if (out.items.len > 0) try out.append(allocator, '\n');
                            try out.appendSlice(allocator, t.text);
                        },
                        .image => {},
                    }
                }
            },
        },
        .assistant => |a| {
            for (a.content) |c| {
                switch (c) {
                    .text => |t| {
                        if (out.items.len > 0) try out.append(allocator, '\n');
                        try out.appendSlice(allocator, t.text);
                    },
                    .thinking => |t| {
                        if (out.items.len > 0) try out.append(allocator, '\n');
                        try out.appendSlice(allocator, t.thinking);
                    },
                    .tool_call => {},
                }
            }
        },
        .tool_result => |tr| {
            for (tr.content) |c| {
                switch (c) {
                    .text => |t| {
                        if (out.items.len > 0) try out.append(allocator, '\n');
                        try out.appendSlice(allocator, t.text);
                    },
                    .image => {},
                }
            }
        },
    }
}

fn isOpenRouterAnthropic(model: ai_types.Model) bool {
    if (model.base_url.len == 0) return false;
    if (std.mem.find(u8, model.base_url, "openrouter") == null) return false;
    if (!std.mem.startsWith(u8, model.id, "anthropic/")) return false;
    return true;
}

fn shouldSkipAssistant(msg: ai_types.Message) bool {
    switch (msg) {
        .assistant => |a| {
            return a.stop_reason == .aborted or a.stop_reason == .@"error";
        },
        else => {},
    }
    return false;
}

fn collectToolCallIds(allocator: std.mem.Allocator, messages: []const ai_types.Message) !std.StringHashMap(void) {
    var tool_call_ids = std.StringHashMap(void).init(allocator);
    errdefer {
        var iter = tool_call_ids.keyIterator();
        while (iter.next()) |key| {
            allocator.free(key.*);
        }
        tool_call_ids.deinit();
    }

    for (messages) |msg| {
        switch (msg) {
            .assistant => |a| {
                for (a.content) |c| {
                    if (c == .tool_call) {
                        const id_dup = try allocator.dupe(u8, c.tool_call.id);
                        try tool_call_ids.put(id_dup, {});
                    }
                }
            },
            else => {},
        }
    }

    return tool_call_ids;
}

fn isOrphanedToolResult(msg: ai_types.Message, tool_call_ids: *const std.StringHashMap(void)) bool {
    if (tool_call_ids.count() == 0) {
        return false;
    }
    switch (msg) {
        .tool_result => |tr| {
            if (tr.tool_call_id.len > 0) {
                return !tool_call_ids.contains(tr.tool_call_id);
            }
        },
        else => {},
    }
    return false;
}

fn freeToolCallIds(allocator: std.mem.Allocator, map: *std.StringHashMap(void)) void {
    var iter = map.keyIterator();
    while (iter.next()) |key| {
        allocator.free(key.*);
    }
    map.deinit();
}

fn writeMessagesArray(
    writer: *json_writer.JsonWriter,
    context: ai_types.Context,
    model: ai_types.Model,
    allocator: std.mem.Allocator,
) !void {
    const merged = mergeCompat(model);
    const is_github_copilot = std.mem.eql(u8, model.provider, "github-copilot");

    const should_add_cache_control = isOpenRouterAnthropic(model);
    var last_user_msg_idx: ?usize = null;
    if (should_add_cache_control) {
        var idx: usize = 0;
        for (context.messages) |msg| {
            if (msg == .user) {
                last_user_msg_idx = idx;
            }
            idx += 1;
        }
    }

    try writer.writeKey("messages");
    try writer.beginArray();

    var tool_call_ids = collectToolCallIds(allocator, context.messages) catch std.StringHashMap(void).init(allocator);
    defer freeToolCallIds(allocator, &tool_call_ids);

    if (context.getSystemPrompt()) |sp| {
        try writer.beginObject();
        const use_developer = model.reasoning and merged.supports_developer_role;
        const system_role: []const u8 = if (use_developer) "developer" else "system";
        try writer.writeStringField("role", system_role);
        const sanitized = try sanitize.sanitizeSurrogatesInPlace(allocator, sp);
        defer {
            if (sanitized.ptr != sp.ptr) {
                allocator.free(@constCast(sanitized));
            }
        }
        try writer.writeStringField("content", sanitized);
        try writer.endObject();
    }

    var msg_idx: usize = 0;
    var prev_role: []const u8 = "";
    while (msg_idx < context.messages.len) : (msg_idx += 1) {
        const msg = context.messages[msg_idx];

        if (shouldSkipAssistant(msg)) continue;

        if (isOrphanedToolResult(msg, &tool_call_ids)) continue;

        if (msg == .user) {
            const u = msg.user;
            const is_last_user_msg = should_add_cache_control and last_user_msg_idx != null and msg_idx == last_user_msg_idx.?;

            try writer.beginObject();
            try writer.writeStringField("role", "user");

            switch (u.content) {
                .text => |t| {
                    const sanitized = try sanitize.sanitizeSurrogatesInPlace(allocator, t);
                    defer {
                        if (sanitized.ptr != t.ptr) {
                            allocator.free(@constCast(sanitized));
                        }
                    }
                    if (is_last_user_msg) {
                        try writer.writeKey("content");
                        try writer.beginArray();
                        try writer.beginObject();
                        try writer.writeStringField("type", "text");
                        try writer.writeStringField("text", sanitized);
                        try writer.writeKey("cache_control");
                        try writer.beginObject();
                        try writer.writeStringField("type", "ephemeral");
                        try writer.endObject();
                        try writer.endArray();
                    } else {
                        try writer.writeStringField("content", sanitized);
                    }
                },
                .parts => |parts| {
                    var has_images = false;
                    for (parts) |p| {
                        if (p == .image) {
                            has_images = true;
                            break;
                        }
                    }

                    if (has_images or is_last_user_msg) {
                        try writer.writeKey("content");
                        try writer.beginArray();
                        for (parts, 0..) |p, part_idx| {
                            const is_last_part = part_idx == parts.len - 1;
                            switch (p) {
                                .text => |t| {
                                    const sanitized = try sanitize.sanitizeSurrogatesInPlace(allocator, t.text);
                                    defer {
                                        if (sanitized.ptr != t.text.ptr) {
                                            allocator.free(@constCast(sanitized));
                                        }
                                    }
                                    try writer.beginObject();
                                    try writer.writeStringField("type", "text");
                                    try writer.writeStringField("text", sanitized);
                                    if (is_last_user_msg and is_last_part) {
                                        try writer.writeKey("cache_control");
                                        try writer.beginObject();
                                        try writer.writeStringField("type", "ephemeral");
                                        try writer.endObject();
                                    }
                                    try writer.endObject();
                                },
                                .image => |img| {
                                    try writeImageUrlPart(writer, img, allocator);
                                },
                            }
                        }
                        try writer.endArray();
                    } else {
                        var text_buf = std.ArrayList(u8).empty;
                        defer text_buf.deinit(allocator);
                        for (parts) |p| {
                            switch (p) {
                                .text => |t| {
                                    if (text_buf.items.len > 0) try text_buf.append(allocator, '\n');
                                    try text_buf.appendSlice(allocator, t.text);
                                },
                                .image => {},
                            }
                        }
                        try writer.writeStringField("content", text_buf.items);
                    }
                },
            }
            try writer.endObject();
            prev_role = "user";
            continue;
        }

        if (msg == .assistant) {
            const a = msg.assistant;

            var has_text = false;
            var has_thinking = false;
            var has_tool_calls = false;
            for (a.content) |c| switch (c) {
                .text => |t| {
                    if (t.text.len > 0 and std.mem.trim(u8, t.text, " \t\r\n").len > 0) has_text = true;
                },
                .thinking => |t| {
                    if (t.thinking.len > 0 and std.mem.trim(u8, t.thinking, " \t\r\n").len > 0) has_thinking = true;
                },
                .tool_call => {
                    has_tool_calls = true;
                },
                .image => {},
            };

            if (!has_text and !has_thinking and !has_tool_calls) continue;
            const thinking_as_text = merged.requires_thinking_as_text or (has_thinking and !has_text and !has_tool_calls);

            try writer.beginObject();
            try writer.writeStringField("role", "assistant");

            if (has_text) {
                if (is_github_copilot) {
                    var text_buf = std.ArrayList(u8).empty;
                    defer text_buf.deinit(allocator);
                    for (a.content) |c| switch (c) {
                        .text => |t| {
                            if (t.text.len > 0 and std.mem.trim(u8, t.text, " \t\r\n").len > 0) {
                                if (text_buf.items.len > 0) try text_buf.appendSlice(allocator, "");
                                try text_buf.appendSlice(allocator, t.text);
                            }
                        },
                        else => {},
                    };
                    try writer.writeStringField("content", text_buf.items);
                } else {
                    try writer.writeKey("content");
                    try writer.beginArray();
                    if (has_thinking and merged.requires_thinking_as_text) {
                        for (a.content) |c| switch (c) {
                            .thinking => |t| {
                                if (t.thinking.len > 0 and std.mem.trim(u8, t.thinking, " \t\r\n").len > 0) {
                                    try writer.beginObject();
                                    try writer.writeStringField("type", "text");
                                    try writer.writeStringField("text", t.thinking);
                                    try writer.endObject();
                                }
                            },
                            else => {},
                        };
                    }
                    for (a.content) |c| switch (c) {
                        .text => |t| {
                            if (t.text.len > 0 and std.mem.trim(u8, t.text, " \t\r\n").len > 0) {
                                try writer.beginObject();
                                try writer.writeStringField("type", "text");
                                try writer.writeStringField("text", t.text);
                                try writer.endObject();
                            }
                        },
                        else => {},
                    };
                    try writer.endArray();
                }
            } else if (thinking_as_text and has_thinking) {
                try writer.writeKey("content");
                try writer.beginArray();
                for (a.content) |c| switch (c) {
                    .thinking => |t| {
                        if (t.thinking.len > 0 and std.mem.trim(u8, t.thinking, " \t\r\n").len > 0) {
                            try writer.beginObject();
                            try writer.writeStringField("type", "text");
                            try writer.writeStringField("text", t.thinking);
                            try writer.endObject();
                        }
                    },
                    else => {},
                };
                try writer.endArray();
            } else {
                if (merged.requires_thinking_as_text) {
                    try writer.writeStringField("content", "");
                } else {
                    try writer.writeKey("content");
                    try writer.writeNull();
                }
            }

            if (has_thinking and !thinking_as_text) {
                var reasoning_field: []const u8 = "reasoning_content";
                for (a.content) |c| switch (c) {
                    .thinking => |t| {
                        if (t.thinking_signature) |sig| {
                            if (sig.len > 0) reasoning_field = sig;
                        }
                        break;
                    },
                    else => {},
                };

                var thinking_buf = std.ArrayList(u8).empty;
                defer thinking_buf.deinit(allocator);
                for (a.content) |c| switch (c) {
                    .thinking => |t| {
                        if (t.thinking.len > 0 and std.mem.trim(u8, t.thinking, " \t\r\n").len > 0) {
                            if (thinking_buf.items.len > 0) try thinking_buf.append(allocator, '\n');
                            try thinking_buf.appendSlice(allocator, t.thinking);
                        }
                    },
                    else => {},
                };
                if (thinking_buf.items.len > 0) {
                    try writer.writeStringField(reasoning_field, thinking_buf.items);
                }
            }

            if (has_tool_calls) {
                try writer.writeKey("tool_calls");
                try writer.beginArray();
                for (a.content) |c| switch (c) {
                    .tool_call => |tc| {
                        try writer.beginObject();
                        try writer.writeStringField("id", tc.id);
                        try writer.writeStringField("type", "function");
                        try writer.writeKey("function");
                        try writer.beginObject();
                        try writer.writeStringField("name", tc.name);
                        try writer.writeStringField("arguments", tc.arguments_json);
                        try writer.endObject();
                        try writer.endObject();
                    },
                    else => {},
                };
                try writer.endArray();

                var has_reasoning_details = false;
                for (a.content) |c| {
                    if (c == .tool_call and c.tool_call.thought_signature != null) {
                        has_reasoning_details = true;
                        break;
                    }
                }
                if (has_reasoning_details) {
                    try writer.writeKey("reasoning_details");
                    try writer.beginArray();
                    for (a.content) |c| {
                        if (c == .tool_call) {
                            if (c.tool_call.thought_signature) |sig| {
                                try writer.writeRawJson(sig);
                            }
                        }
                    }
                    try writer.endArray();
                }
            }

            try writer.endObject();
            prev_role = "assistant";
            continue;
        }

        if (msg == .tool_result) {
            var image_blocks = std.ArrayList(ai_types.UserContentPart).empty;
            defer image_blocks.deinit(allocator);

            while (msg_idx < context.messages.len and context.messages[msg_idx] == .tool_result) {
                const tr = context.messages[msg_idx].tool_result;

                if (isOrphanedToolResult(context.messages[msg_idx], &tool_call_ids)) {
                    msg_idx += 1;
                    continue;
                }

                if (merged.requires_assistant_after_tool_result and std.mem.eql(u8, prev_role, "tool")) {
                    try writer.beginObject();
                    try writer.writeStringField("role", "assistant");
                    try writer.writeStringField("content", "");
                    try writer.endObject();
                }

                var text_buf = std.ArrayList(u8).empty;
                defer text_buf.deinit(allocator);
                var has_images = false;
                for (tr.content) |c| switch (c) {
                    .text => |t| {
                        if (text_buf.items.len > 0) try text_buf.append(allocator, '\n');
                        try text_buf.appendSlice(allocator, t.text);
                    },
                    .image => {
                        has_images = true;
                    },
                };

                try writer.beginObject();
                try writer.writeStringField("role", "tool");
                try writer.writeStringField("tool_call_id", tr.tool_call_id);
                if (merged.requires_tool_result_name) {
                    try writer.writeStringField("name", tr.tool_name);
                }
                const content_str = if (text_buf.items.len > 0) text_buf.items else if (has_images) "(see attached image)" else "";
                try writer.writeStringField("content", content_str);
                try writer.endObject();
                prev_role = "tool";

                if (has_images) {
                    for (tr.content) |c| switch (c) {
                        .image => |img| {
                            try image_blocks.append(allocator, .{ .image = img });
                        },
                        else => {},
                    };
                }

                msg_idx += 1;
            }
            msg_idx -= 1;

            if (image_blocks.items.len > 0) {
                if (merged.requires_assistant_after_tool_result) {
                    try writer.beginObject();
                    try writer.writeStringField("role", "assistant");
                    try writer.writeStringField("content", "");
                    try writer.endObject();
                }

                try writer.beginObject();
                try writer.writeStringField("role", "user");
                try writer.writeKey("content");
                try writer.beginArray();
                for (image_blocks.items) |img_part| switch (img_part) {
                    .image => |img| {
                        try writeImageUrlPart(writer, img, allocator);
                    },
                    else => {},
                };
                try writer.endArray();
                try writer.endObject();
                prev_role = "user";
            }

            continue;
        }
    }

    try writer.endArray();
}

fn writeImageUrlPart(writer: *json_writer.JsonWriter, img: ai_types.ImageContent, allocator: std.mem.Allocator) !void {
    try writer.beginObject();
    try writer.writeStringField("type", "image_url");
    try writer.writeKey("image_url");
    try writer.beginObject();
    try writer.writeKey("url");
    try writer.buffer.appendSlice(allocator, "\"data:");
    try writer.buffer.appendSlice(allocator, img.mime_type);
    try writer.buffer.appendSlice(allocator, ";base64,");
    try writer.buffer.appendSlice(allocator, img.data);
    try writer.buffer.append(allocator, '"');
    writer.needs_comma = true;
    try writer.endObject();
}

fn buildRequestBody(
    model: ai_types.Model,
    context: ai_types.Context,
    options: ai_types.StreamOptions,
    allocator: std.mem.Allocator,
) ![]u8 {
    var buf = std.ArrayList(u8).empty;
    errdefer buf.deinit(allocator);

    const merged = mergeCompat(model);

    var transformed = try pre_transform.preTransform(allocator, context.messages, .{
        .target_api = model.api,
        .target_provider = model.provider,
        .target_model_id = model.id,
        .max_tool_id_len = if (provider_caps.isOpenAIHost(model.base_url) or isTransparentOpenAIProxy(model)) 40 else 0,
        .mistral_tool_ids = merged.requires_mistral_tool_ids,
        .insert_synthetic_results = true,
        .tools = context.tools,
    });
    defer transformed.deinit();

    var tx_context = context;
    tx_context.messages = transformed.messages;

    var w = json_writer.JsonWriter.init(&buf, allocator);
    try w.beginObject();
    try w.writeStringField("model", model.id);
    try writeMessagesArray(&w, tx_context, model, allocator);
    try w.writeBoolField("stream", true);
    if (merged.supports_usage_in_streaming) {
        try w.writeKey("stream_options");
        try w.beginObject();
        try w.writeBoolField("include_usage", true);
        try w.endObject();
    }
    try w.writeIntField(merged.max_tokens_field, options.max_tokens orelse model.max_tokens);
    if (options.temperature) |t| {
        if (!isKimiModel(model) or t == 1.0) {
            try w.writeKey("temperature");
            try w.writeFloat(t);
        }
    }
    if (options.getReasoningEffort()) |effort| {
        if (model.reasoning and merged.supports_reasoning_effort) {
            const sent: ?[]const u8 = if (provider_caps.isOpenCodeGateway(model.provider))
                (if (options.reasoning_enabled) provider_caps.openCodeEffort(model.id, effort) else null)
            else if (provider_caps.usesDeepSeekWire(model.provider, model.base_url))
                provider_caps.deepSeekEffort(effort)
            else
                effort;
            if (sent) |value| try w.writeStringField("reasoning_effort", value);
        }
    }
    if (context.tools) |tools| {
        if (tools.len > 0) {
            try w.writeKey("tools");
            try w.beginArray();
            for (tools) |tool| {
                try w.beginObject();
                try w.writeStringField("type", "function");
                try w.writeKey("function");
                try w.beginObject();
                try w.writeStringField("name", tool.name);
                try w.writeStringField("description", tool.description);
                try w.writeKey("parameters");
                try w.writeRawJson(tool.parameters_schema_json);
                if (merged.supports_strict_mode) {
                    try w.writeBoolField("strict", true);
                }
                try w.endObject();
                try w.endObject();
            }
            try w.endArray();

            if (options.tool_choice) |tc| {
                try w.writeKey("tool_choice");
                switch (tc) {
                    .auto => try w.writeString("auto"),
                    .none => try w.writeString("none"),
                    .required => try w.writeString("required"),
                    .function => |name| {
                        try w.beginObject();
                        try w.writeStringField("type", "function");
                        try w.writeKey("function");
                        try w.beginObject();
                        try w.writeStringField("name", name);
                        try w.endObject();
                    },
                }
            }
        }
    }
    if (merged.supports_store) {
        try w.writeBoolField("store", false);
    }
    try w.endObject();

    return buf.toOwnedSlice(allocator);
}

const ThreadCtx = struct {
    allocator: std.mem.Allocator,
    stream: *event_stream.AssistantMessageEventStream,
    model: ai_types.Model,
    api_key: []const u8,
    request_body: []u8,
    context: ai_types.Context,
    cancel_token: ?ai_types.CancelToken = null,
    on_payload_fn: ?*const fn (ctx: ?*anyopaque, payload_json: []const u8) void = null,
    on_payload_ctx: ?*anyopaque = null,
    retry_config: ?ai_types.RetryConfig = null,
    ping_interval_ms: ?u64 = null,
    conversation_id: ?[]u8 = null,

    fn deinit(self: *ThreadCtx) void {
        if (self.conversation_id) |id| self.allocator.free(id);
        self.allocator.free(self.api_key);
        self.allocator.free(self.request_body);
        var mut_context = self.context;
        mut_context.deinit(self.allocator);
        var mut_model = self.model;
        mut_model.deinit(self.allocator);
        self.allocator.destroy(self);
    }
};

const BlockType = enum {
    none,
    text,
    thinking,
    tool_call,
};

const ToolCallEvent = struct {
    api_index: usize,
    is_start: bool,
    id: ?[]const u8,
    name: ?[]const u8,
    arguments_delta: ?[]const u8,

    fn deinit(self: *ToolCallEvent, allocator: std.mem.Allocator) void {
        if (self.id) |id| allocator.free(id);
        if (self.name) |name| allocator.free(name);
        if (self.arguments_delta) |delta| allocator.free(delta);
    }
};

const ReasoningDetailEvent = struct {
    tool_call_id: []const u8,
    detail_json: []const u8,

    fn deinit(self: *ReasoningDetailEvent, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.detail_json);
    }
};

const reasoning_fields: []const []const u8 = &.{ "reasoning_content", "reasoning", "reasoning_text" };

fn findReasoningField(delta: std.json.ObjectMap) ?struct { field: []const u8, value: []const u8 } {
    for (reasoning_fields) |field| {
        if (delta.get(field)) |val| {
            if (val == .string and val.string.len > 0) {
                return .{ .field = field, .value = val.string };
            }
        }
    }
    return null;
}

fn canCompletePartialTextOnStreamError(text_len: usize, thinking_len: usize, tool_call_count: usize) bool {
    return tool_call_count == 0 and (text_len > 0 or thinking_len > 0);
}

const ChunkOutcome = enum { more, finished, stream_error };

const reply_trace_headers = [_][]const u8{ "x-request-id", "request-id", "x-opencode-endpoint-id", "x-opencode-log-id" };
const reply_trace_bytes = 400;

const ReplyTrace = struct {
    chunks: usize = 0,
    first: Kept = .{},
    last: Kept = .{},
    finish: Kept = .{},
    headers: [reply_trace_headers.len]Kept = [_]Kept{.{}} ** reply_trace_headers.len,

    const Kept = struct {
        buf: [reply_trace_bytes]u8 = undefined,
        len: usize = 0,
        cut: bool = false,

        fn keep(self: *Kept, bytes: []const u8) void {
            self.len = @min(bytes.len, self.buf.len);
            @memcpy(self.buf[0..self.len], bytes[0..self.len]);
            self.cut = bytes.len > self.buf.len;
        }

        fn slice(self: *const Kept) []const u8 {
            return self.buf[0..self.len];
        }
    };

    fn noteFinish(self: *ReplyTrace, data: []const u8) void {
        if (std.mem.eql(u8, data, "[DONE]")) return;
        self.finish.keep(data);
    }

    fn noteHeaders(self: *ReplyTrace, head: anytype) void {
        if (std.mem.find(u8, head.bytes, "\r\n") == null) return;
        var headers = head.iterateHeaders();
        while (headers.next()) |header| {
            for (reply_trace_headers, 0..) |name, index| {
                if (std.ascii.eqlIgnoreCase(header.name, name)) self.headers[index].keep(header.value);
            }
        }
    }

    fn observe(self: *ReplyTrace, data: []const u8) void {
        self.chunks += 1;
        if (std.mem.eql(u8, data, "[DONE]")) return;
        if (self.first.len == 0) self.first.keep(data);
        self.last.keep(data);
    }

    fn describe(self: *const ReplyTrace, allocator: std.mem.Allocator, summary: []const u8, usage: ai_types.Usage) ![]u8 {
        var out: std.Io.Writer.Allocating = .init(allocator);
        errdefer out.deinit();
        self.writeDescription(allocator, &out.writer, summary, usage) catch |err| switch (err) {
            error.WriteFailed => return error.OutOfMemory,
            else => |other| return other,
        };
        return out.toOwnedSlice();
    }

    fn writeDescription(self: *const ReplyTrace, allocator: std.mem.Allocator, writer: *std.Io.Writer, summary: []const u8, usage: ai_types.Usage) !void {
        try writer.print("{s} (", .{summary});
        var finish: ?[]const u8 = null;
        var id: ?[]const u8 = null;
        var first = try chunkFacts(allocator, self.first.slice());
        defer first.deinit(allocator);
        var last = try chunkFacts(allocator, self.last.slice());
        defer last.deinit(allocator);
        var finishing = try chunkFacts(allocator, self.finish.slice());
        defer finishing.deinit(allocator);
        finish = finishing.finish_reason orelse last.finish_reason orelse first.finish_reason;
        id = first.id orelse last.id;
        if (finish) |reason| try writer.print("finish_reason: {s}", .{reason}) else try writer.writeAll("no finish_reason");
        const input = usage.input + usage.cache_read;
        if (input + usage.output > 0) {
            try writer.print("; usage: {d} input ({d} cached), {d} output", .{ input, usage.cache_read, usage.output });
        } else {
            try writer.writeAll("; no usage reported");
        }
        try writer.print("; {d} chunk{s}", .{ self.chunks, if (self.chunks == 1) "" else "s" });
        if (id) |value| try writer.print("; id: {s}", .{value});
        for (reply_trace_headers, self.headers) |name, value| {
            if (value.len > 0) try writer.print("; {s}: {s}", .{ name, value.slice() });
        }
        if (self.last.len > 0) try writer.print("; last chunk: {s}{s}", .{ self.last.slice(), if (self.last.cut) "…" else "" });
        try writer.writeByte(')');
    }
};

const ChunkFacts = struct {
    finish_reason: ?[]u8 = null,
    id: ?[]u8 = null,

    fn deinit(self: *ChunkFacts, allocator: std.mem.Allocator) void {
        if (self.finish_reason) |value| allocator.free(value);
        if (self.id) |value| allocator.free(value);
        self.* = undefined;
    }
};

fn chunkFacts(allocator: std.mem.Allocator, data: []const u8) !ChunkFacts {
    if (data.len == 0) return .{};
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, data, .{}) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return .{},
    };
    defer parsed.deinit();
    if (parsed.value != .object) return .{};
    var facts: ChunkFacts = .{};
    errdefer facts.deinit(allocator);
    if (parsed.value.object.get("id")) |value| {
        if (value == .string and value.string.len > 0) facts.id = try allocator.dupe(u8, value.string);
    }
    const choices = parsed.value.object.get("choices") orelse return facts;
    if (choices != .array or choices.array.items.len == 0) return facts;
    const choice = choices.array.items[0];
    if (choice != .object) return facts;
    if (choice.object.get("finish_reason")) |value| {
        if (value == .string) facts.finish_reason = try allocator.dupe(u8, value.string);
    }
    return facts;
}

fn streamErrorMessage(allocator: std.mem.Allocator, data: []const u8) ?[]u8 {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, data, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const reported = parsed.value.object.get("error") orelse return null;
    const message = switch (reported) {
        .string => |text| text,
        .object => |object| if (object.get("message")) |found| (if (found == .string) found.string else "") else "",
        else => "",
    };
    if (message.len == 0) return std.fmt.allocPrint(allocator, "provider stream error: {f}", .{std.json.fmt(reported, .{})}) catch null;
    return std.fmt.allocPrint(allocator, "provider stream error: {s}", .{message}) catch null;
}

fn parseChunk(
    data: []const u8,
    text: *std.ArrayList(u8),
    thinking: *std.ArrayList(u8),
    usage: *ai_types.Usage,
    stop_reason: *ai_types.StopReason,
    current_block: *BlockType,
    reasoning_signature: *?[]const u8,
    tool_call_events: *std.ArrayList(ToolCallEvent),
    reasoning_detail_events: *std.ArrayList(ReasoningDetailEvent),
    allocator: std.mem.Allocator,
) !ChunkOutcome {
    if (std.mem.eql(u8, data, "[DONE]")) return .finished;

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, data, .{}) catch return .more;
    defer parsed.deinit();

    const root = parsed.value;
    if (root != .object) return .more;
    if (root.object.get("error")) |reported| {
        if (reported != .null) return .stream_error;
    }
    var finished = false;

    if (root.object.get("usage")) |u| {
        if (u == .object) {
            if (u.object.get("prompt_tokens")) |v| {
                if (v == .integer) usage.input = @intCast(v.integer);
            }
            if (u.object.get("prompt_tokens_details")) |details| {
                if (details == .object) {
                    if (details.object.get("cached_tokens")) |cached| {
                        if (cached == .integer) {
                            const cached_tokens: u64 = @intCast(cached.integer);
                            usage.cache_read = cached_tokens;
                            if (usage.input > cached_tokens) {
                                usage.input -= cached_tokens;
                            }
                        }
                    }
                }
            }
            var completion_tokens: u64 = 0;
            if (u.object.get("completion_tokens")) |v| {
                if (v == .integer) completion_tokens = @intCast(v.integer);
            }
            var reasoning_tokens: u64 = 0;
            if (u.object.get("completion_tokens_details")) |details| {
                if (details == .object) {
                    if (details.object.get("reasoning_tokens")) |rt| {
                        if (rt == .integer) reasoning_tokens = @intCast(rt.integer);
                    }
                }
            }
            usage.output = completion_tokens + reasoning_tokens;
            if (u.object.get("total_tokens")) |v| {
                if (v == .integer) usage.total_tokens = @intCast(v.integer);
            }
        }
    }

    if (root.object.get("choices")) |choices| {
        if (choices != .array or choices.array.items.len == 0) return .more;
        const ch = choices.array.items[0];
        if (ch != .object) return .more;

        if (ch.object.get("finish_reason")) |fr| {
            if (fr == .string) {
                finished = true;
                if (std.mem.eql(u8, fr.string, "length")) stop_reason.* = .length else if (std.mem.eql(u8, fr.string, "tool_calls")) stop_reason.* = .tool_use else if (std.mem.eql(u8, fr.string, "content_filter")) stop_reason.* = .@"error" else stop_reason.* = .stop;
            }
        }

        if (ch.object.get("delta")) |d| {
            if (d == .object) {
                if (d.object.get("tool_calls")) |tool_calls| {
                    if (tool_calls == .array) {
                        for (tool_calls.array.items) |tc| {
                            if (tc == .object) {
                                const tc_index: usize = if (tc.object.get("index")) |idx|
                                    if (idx == .integer) @intCast(idx.integer) else 0
                                else
                                    0;

                                const tc_id = tc.object.get("id");
                                const tc_func = tc.object.get("function");

                                if (tc_id) |id| {
                                    if (id == .string and id.string.len > 0) {
                                        current_block.* = .tool_call;
                                        var name_str: []const u8 = "";
                                        if (tc_func) |f| {
                                            if (f == .object) {
                                                if (f.object.get("name")) |n| {
                                                    if (n == .string) {
                                                        name_str = n.string;
                                                    }
                                                }
                                            }
                                        }

                                        const duped_id = try allocator.dupe(u8, id.string);
                                        const duped_name = try allocator.dupe(u8, name_str);

                                        try tool_call_events.append(allocator, .{
                                            .api_index = tc_index,
                                            .is_start = true,
                                            .id = duped_id,
                                            .name = duped_name,
                                            .arguments_delta = null,
                                        });
                                    }
                                }

                                if (tc_func) |f| {
                                    if (f == .object) {
                                        if (f.object.get("arguments")) |args| {
                                            if (args == .string and args.string.len > 0) {
                                                current_block.* = .tool_call;
                                                const duped_args = try allocator.dupe(u8, args.string);
                                                try tool_call_events.append(allocator, .{
                                                    .api_index = tc_index,
                                                    .is_start = false,
                                                    .id = null,
                                                    .name = null,
                                                    .arguments_delta = duped_args,
                                                });
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
                if (findReasoningField(d.object)) |reasoning| {
                    if (reasoning_signature.* == null) {
                        reasoning_signature.* = try allocator.dupe(u8, reasoning.field);
                    }
                    current_block.* = .thinking;
                    try thinking.appendSlice(allocator, reasoning.value);
                } else if (d.object.get("content")) |c| {
                    if (c == .string and c.string.len > 0) {
                        current_block.* = .text;
                        try text.appendSlice(allocator, c.string);
                    }
                }

                if (d.object.get("reasoning_details")) |rd| {
                    if (rd == .array) {
                        for (rd.array.items) |detail| {
                            if (detail == .object) {
                                const detail_type = detail.object.get("type");
                                const detail_id = detail.object.get("id");
                                const detail_data = detail.object.get("data");

                                if (detail_type) |t| {
                                    if (t == .string and std.mem.eql(u8, t.string, "reasoning.encrypted")) {
                                        if (detail_id) |id| {
                                            if (id == .string and id.string.len > 0) {
                                                if (detail_data) |dat| {
                                                    if (dat == .string and dat.string.len > 0) {
                                                        var detail_buf = std.ArrayList(u8).empty;
                                                        defer detail_buf.deinit(allocator);
                                                        var detail_writer = json_writer.JsonWriter.init(&detail_buf, allocator);
                                                        try detail_writer.beginObject();
                                                        try detail_writer.writeStringField("type", "reasoning.encrypted");
                                                        try detail_writer.writeStringField("id", id.string);
                                                        try detail_writer.writeStringField("data", dat.string);
                                                        try detail_writer.endObject();
                                                        const detail_json = try allocator.dupe(u8, detail_buf.items);
                                                        const tool_call_id = try allocator.dupe(u8, id.string);

                                                        try reasoning_detail_events.append(allocator, .{
                                                            .tool_call_id = tool_call_id,
                                                            .detail_json = detail_json,
                                                        });
                                                    }
                                                }
                                            }
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    }
    return if (finished) .finished else .more;
}

pub const wires: []const []const u8 = &.{"openai-completions"};

const request_wire = provider_catalog.wirePath("openai-completions") orelse unreachable;

const copilot_wire = provider_catalog.Wire{ .id = "github-copilot", .suffix = "/chat/completions" };

fn buildBearerAuthValue(allocator: std.mem.Allocator, token: []const u8) ![]u8 {
    var sb = StringBuilder{};
    sb.count("Bearer ");
    sb.count(token);
    try sb.allocate(allocator);
    errdefer sb.deinit(allocator);

    _ = sb.append("Bearer ");
    _ = sb.append(token);

    std.debug.assert(sb.len == sb.cap);
    const out = sb.ptr.?[0..sb.cap];
    sb.ptr = null;
    sb.cap = 0;
    sb.len = 0;
    return out;
}

fn pushEvent(stream: *event_stream.AssistantMessageEventStream, event: ai_types.AssistantMessageEvent) void {
    _ = stream.pushBlocking(event);
}

const client_user_agent = "oapx/" ++ version;

fn wantsConversationId(model: ai_types.Model) bool {
    return std.mem.eql(u8, model.provider, "opencode-go");
}

fn hashField(hasher: *std.hash.Wyhash, tag: u8, bytes: []const u8) void {
    var length: [8]u8 = undefined;
    std.mem.writeInt(u64, &length, bytes.len, .little);
    hasher.update(&.{tag});
    hasher.update(&length);
    hasher.update(bytes);
}

fn conversationId(allocator: std.mem.Allocator, session_id: ?[]const u8, context: ai_types.Context) ![]u8 {
    if (session_id) |id| return allocator.dupe(u8, id);
    var hasher = std.hash.Wyhash.init(0);
    hashField(&hasher, 's', context.system_prompt.slice());
    for (context.messages) |message| {
        if (message != .user) continue;
        switch (message.user.content) {
            .text => |text| hashField(&hasher, 't', text),
            .parts => |parts| for (parts) |part| switch (part) {
                .text => |text| hashField(&hasher, 't', text.text),
                .image => |image| {
                    hashField(&hasher, 'm', image.mime_type);
                    hashField(&hasher, 'i', image.data);
                },
            },
        }
        break;
    }
    return std.fmt.allocPrint(allocator, "oapx-{x:0>16}", .{hasher.final()});
}

fn isKimiModel(model: ai_types.Model) bool {
    return std.mem.eql(u8, model.provider, "kimi");
}

fn maybeDumpProviderPayload(allocator: std.mem.Allocator, request_body: []const u8) void {
    const path = compat_mod.getEnvVarOwned(allocator, "OAPX_DEBUG_PROVIDER_PAYLOAD") catch return;
    defer allocator.free(path);
    if (path.len == 0) return;
    compat_mod.fs.writeFile(compat_mod.fs.getCwd(), path, request_body) catch {};
}

fn runThread(ctx: *ThreadCtx) void {
    const allocator = ctx.allocator;
    const stream = ctx.stream;
    const model = ctx.model;
    const api_key = ctx.api_key;
    const request_body = ctx.request_body;
    const context = ctx.context;
    const cancel_token = ctx.cancel_token;
    const on_payload_fn = ctx.on_payload_fn;
    const on_payload_ctx = ctx.on_payload_ctx;
    const retry_config = ctx.retry_config;

    defer {
        awaitCleanupRelease();
        stream.markThreadDone();
    }

    if (on_payload_fn) |cb| {
        cb(on_payload_ctx, request_body);
    }
    maybeDumpProviderPayload(allocator, request_body);

    if (cancel_token) |ct| {
        if (ct.isCancelled()) {
            ctx.deinit();
            stream.completeWithError("request cancelled");
            return;
        }
    }

    var client = compat_mod.http.HttpClient.init(allocator);
    defer client.deinit();

    const wire = if (std.mem.eql(u8, model.provider, "github-copilot")) copilot_wire else request_wire;
    const url = provider_catalog.joinModelUrlOwned(allocator, model, wire) catch {
        ctx.deinit();
        stream.completeWithError("oom building url");
        return;
    };
    defer allocator.free(url);

    const auth = buildBearerAuthValue(allocator, api_key) catch {
        ctx.deinit();
        stream.completeWithError("oom building auth header");
        return;
    };
    defer allocator.free(auth);

    const uri = std.Uri.parse(url) catch {
        ctx.deinit();
        stream.completeWithError("invalid provider URL");
        return;
    };

    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    if (api_key.len > 0) {
        headers.append(allocator, .{ .name = "authorization", .value = auth }) catch {
            ctx.deinit();
            stream.completeWithError("oom headers");
            return;
        };
    }
    headers.append(allocator, .{ .name = "content-type", .value = "application/json" }) catch {
        ctx.deinit();
        stream.completeWithError("oom headers");
        return;
    };
    headers.append(allocator, .{ .name = "accept", .value = "text/event-stream" }) catch {
        ctx.deinit();
        stream.completeWithError("oom headers");
        return;
    };

    if (std.mem.eql(u8, model.provider, "github-copilot")) {
        headers.append(allocator, .{ .name = "user-agent", .value = github_copilot.COPILOT_HEADERS.user_agent }) catch {
            ctx.deinit();
            stream.completeWithError("oom copilot headers");
            return;
        };
        headers.append(allocator, .{ .name = "editor-version", .value = github_copilot.COPILOT_HEADERS.editor_version }) catch {
            ctx.deinit();
            stream.completeWithError("oom copilot headers");
            return;
        };
        headers.append(allocator, .{ .name = "editor-plugin-version", .value = github_copilot.COPILOT_HEADERS.editor_plugin_version }) catch {
            ctx.deinit();
            stream.completeWithError("oom copilot headers");
            return;
        };
        headers.append(allocator, .{ .name = "copilot-integration-id", .value = github_copilot.COPILOT_HEADERS.copilot_integration_id }) catch {
            ctx.deinit();
            stream.completeWithError("oom copilot headers");
            return;
        };

        const has_images = github_copilot.hasCopilotVisionInput(context.messages);
        const copilot_headers = github_copilot.buildCopilotDynamicHeaders(
            context.messages,
            has_images,
            allocator,
        ) catch {
            ctx.deinit();
            stream.completeWithError("oom copilot headers");
            return;
        };
        defer allocator.free(copilot_headers);

        for (copilot_headers) |h| {
            headers.append(allocator, h) catch {
                ctx.deinit();
                stream.completeWithError("oom headers");
                return;
            };
        }
    }

    if (model.headers) |model_headers| {
        for (model_headers) |header| {
            if (compat_mod.http.headerPresent(headers.items, header.name)) continue;
            headers.append(allocator, .{ .name = header.name, .value = header.value }) catch {
                ctx.deinit();
                stream.completeWithError("oom headers");
                return;
            };
        }
    }

    if (ctx.conversation_id) |id| {
        headers.append(allocator, .{ .name = "x-opencode-session", .value = id }) catch {
            ctx.deinit();
            stream.completeWithError("oom headers");
            return;
        };
    }

    const user_agent_override: ?[]const u8 = if (isKimiModel(model))
        "claude-code/0.1.0"
    else if (wantsConversationId(model))
        client_user_agent
    else
        null;

    const MAX_RETRIES: u8 = 3;
    const BASE_DELAY_MS: u32 = 1000;
    const max_delay_ms: u32 = if (retry_config) |rc| rc.max_retry_delay_ms orelse 60000 else 60000;

    var response: compat_mod.http.Response = undefined;
    var head_buf: [4096]u8 = undefined;
    var retry_attempt: u8 = 0;
    var req: compat_mod.http.Request = undefined;
    var req_initialized = false;
    defer if (req_initialized) req.deinit();

    while (true) {
        if (cancel_token) |ct| {
            if (ct.isCancelled()) {
                ctx.deinit();
                stream.completeWithError("request cancelled");
                return;
            }
        }

        if (req_initialized) {
            req.deinit();
            req_initialized = false;
        }

        req = client.openRequest(.POST, uri, .{ .extra_headers = headers.items, .user_agent = user_agent_override }) catch {
            if (retry_attempt < MAX_RETRIES) {
                const delay = retry.calculateDelay(retry_attempt, BASE_DELAY_MS, max_delay_ms);
                if (retry.sleepMs(delay, if (cancel_token) |ct| ct.cancelled else null)) {
                    retry_attempt += 1;
                    continue;
                }
                ctx.deinit();
                stream.completeWithError("request cancelled");
                return;
            }
            ctx.deinit();
            stream.completeWithError("failed to open request");
            return;
        };
        req_initialized = true;

        compat_mod.http.sendRequest(&req, request_body) catch {
            if (retry_attempt < MAX_RETRIES) {
                const delay = retry.calculateDelay(retry_attempt, BASE_DELAY_MS, max_delay_ms);
                if (retry.sleepMs(delay, if (cancel_token) |ct| ct.cancelled else null)) {
                    retry_attempt += 1;
                    continue;
                }
                ctx.deinit();
                stream.completeWithError("request cancelled");
                return;
            }
            ctx.deinit();
            stream.completeWithError("failed to send request");
            return;
        };

        response = compat_mod.http.receiveResponse(&req, &head_buf) catch {
            if (retry_attempt < MAX_RETRIES) {
                const delay = retry.calculateDelay(retry_attempt, BASE_DELAY_MS, max_delay_ms);
                if (retry.sleepMs(delay, if (cancel_token) |ct| ct.cancelled else null)) {
                    retry_attempt += 1;
                    continue;
                }
                ctx.deinit();
                stream.completeWithError("request cancelled");
                return;
            }
            ctx.deinit();
            stream.completeWithError("failed to receive response");
            return;
        };

        if (response.head.status == .ok) {
            break;
        }

        const status_code: u16 = @intFromEnum(response.head.status);
        const should_retry = retry.isRetryable(status_code) and retry_attempt < MAX_RETRIES;

        if (should_retry) {
            const error_text: []const u8 = &.{};

            const is_retryable_error = retry.isRetryableError(error_text);

            var delay = retry.calculateDelay(retry_attempt, BASE_DELAY_MS, max_delay_ms);

            if (std.mem.find(u8, response.head.bytes, "\r\n") != null) {
                var retry_after_iter = response.head.iterateHeaders();
                while (retry_after_iter.next()) |header| {
                    if (std.ascii.eqlIgnoreCase(header.name, "retry-after")) {
                        if (retry.extractRetryDelayFromHeader(header.value)) |server_delay| {
                            if (server_delay <= max_delay_ms) {
                                delay = server_delay;
                            }
                        }
                        break;
                    }
                }
            }

            if (retry.extractRetryDelayFromBody(error_text)) |body_delay| {
                if (body_delay <= max_delay_ms) {
                    delay = body_delay;
                }
            }

            if (!is_retryable_error and !retry.isRetryable(status_code)) {
                break;
            }

            if (!retry.sleepMs(delay, if (cancel_token) |ct| ct.cancelled else null)) {
                ctx.deinit();
                stream.completeWithError("request cancelled");
                return;
            }

            retry_attempt += 1;
            continue;
        }

        break;
    }

    if (response.head.status != .ok) {
        var error_buf: [4096]u8 = undefined;
        const error_reader = compat_mod.http.responseReader(&response, &error_buf);
        const error_body = compat_mod.http.allocRemainingResponse(allocator, error_reader, 8192) catch null;
        defer if (error_body) |eb| allocator.free(eb);

        const detail = if (error_body) |eb| error_detail.describe(allocator, eb) catch null else null;
        defer if (detail) |text| allocator.free(text);

        const status_code: u16 = @intFromEnum(response.head.status);
        const error_msg = std.fmt.allocPrint(allocator, "{s} request failed: HTTP {d}{s}", .{
            model.provider,
            status_code,
            detail orelse "",
        }) catch "openai request failed";
        defer if (!std.mem.eql(u8, error_msg, "openai request failed")) allocator.free(error_msg);

        ctx.deinit();
        stream.completeWithError(error_msg);
        return;
    }

    var parser = sse_parser.SSEParser.init(allocator);
    defer parser.deinit();

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);

    var tool_call_tracker_instance = tool_call_tracker.ToolCallTracker.init(allocator);
    defer tool_call_tracker_instance.deinit();
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }
    var next_content_index: usize = 0;
    var tool_call_count: usize = 0;
    var finished = false;
    var trace: ReplyTrace = .{};
    trace.noteHeaders(&response.head);

    var transfer_buf: [4096]u8 = undefined;
    var read_buf: [8192]u8 = undefined;
    const reader = compat_mod.http.responseReader(&response, &transfer_buf);

    var last_ping_time: i64 = 0;
    const ping_interval = ctx.ping_interval_ms orelse 0;

    pushEvent(stream, .{
        .start = .{
            .partial = .{
                .content = &.{},
                .api = model.api,
                .provider = model.provider,
                .model = model.id,
                .usage = .{},
                .stop_reason = .stop,
                .timestamp = compat_mod.time.nowMillis(),
            },
        },
    });

    read_loop: while (true) {
        if (ping_interval > 0) {
            const now = compat_mod.time.nowMillis();
            if (now - last_ping_time >= ping_interval) {
                stream.push(.{ .keepalive = {} }) catch {};
                last_ping_time = now;
            }
        }

        if (cancel_token) |ct| {
            if (ct.isCancelled()) {
                ctx.deinit();
                stream.completeWithError("request cancelled");
                return;
            }
        }

        const n = compat_mod.http.readResponse(reader, &read_buf) catch {
            ctx.deinit();
            stream.completeWithError("read error");
            return;
        };
        if (n == 0) break;

        const events = parser.feed(read_buf[0..n]) catch |err| {
            ctx.deinit();
            stream.completeWithError(sse_parser.errorMessage(err));
            return;
        };

        for (events) |ev| {
            trace.observe(ev.data);
            for (tool_call_events.items) |*tce| {
                @constCast(tce).deinit(allocator);
            }
            tool_call_events.clearRetainingCapacity();
            for (reasoning_detail_events.items) |*rde| {
                @constCast(rde).deinit(allocator);
            }
            reasoning_detail_events.clearRetainingCapacity();

            const prev_text_len = text.items.len;
            const prev_thinking_len = thinking.items.len;

            const outcome = parseChunk(ev.data, &text, &thinking, &usage, &stop_reason, &current_block, &reasoning_signature, &tool_call_events, &reasoning_detail_events, allocator) catch {
                if (canCompletePartialTextOnStreamError(text.items.len, thinking.items.len, tool_call_count)) {
                    stop_reason = .length;
                    break :read_loop;
                }
                ctx.deinit();
                stream.completeWithError("json parse error");
                return;
            };
            switch (outcome) {
                .more => {},
                .finished => {
                    finished = true;
                    trace.noteFinish(ev.data);
                },
                .stream_error => {
                    const message = streamErrorMessage(allocator, ev.data);
                    defer if (message) |owned| allocator.free(owned);
                    ctx.deinit();
                    stream.completeWithError(message orelse "provider stream error");
                    return;
                },
            }

            if (text.items.len > prev_text_len) {
                const delta = text.items[prev_text_len..];
                pushEvent(stream, .{
                    .text_delta = .{
                        .content_index = 0,
                        .delta = delta,
                        .partial = .{
                            .content = &.{},
                            .api = model.api,
                            .provider = model.provider,
                            .model = model.id,
                            .usage = usage,
                            .stop_reason = stop_reason,
                            .timestamp = compat_mod.time.nowMillis(),
                        },
                    },
                });
            }

            if (thinking.items.len > prev_thinking_len and !isKimiModel(model)) {
                const delta = thinking.items[prev_thinking_len..];
                pushEvent(stream, .{
                    .thinking_delta = .{
                        .content_index = 0,
                        .delta = delta,
                        .partial = .{
                            .content = &.{},
                            .api = model.api,
                            .provider = model.provider,
                            .model = model.id,
                            .usage = usage,
                            .stop_reason = stop_reason,
                            .timestamp = compat_mod.time.nowMillis(),
                        },
                    },
                });
            }

            for (reasoning_detail_events.items) |rde| {
                tool_call_tracker_instance.setThoughtSignatureById(rde.tool_call_id, rde.detail_json) catch {};
            }

            for (tool_call_events.items) |tce| {
                if (tce.is_start) {
                    const content_index = next_content_index;
                    const id = tce.id orelse "";
                    const name = tce.name orelse "";
                    _ = tool_call_tracker_instance.startCall(tce.api_index, content_index, id, name) catch {
                        ctx.deinit();
                        stream.completeWithError("oom tool call start");
                        return;
                    };
                    next_content_index += 1;
                    tool_call_count += 1;

                    pushEvent(stream, .{
                        .toolcall_start = .{
                            .content_index = content_index,
                            .id = id,
                            .name = name,
                            .partial = .{
                                .content = &.{},
                                .api = model.api,
                                .provider = model.provider,
                                .model = model.id,
                                .usage = usage,
                                .stop_reason = stop_reason,
                                .timestamp = compat_mod.time.nowMillis(),
                            },
                        },
                    });
                } else if (tce.arguments_delta) |delta| {
                    tool_call_tracker_instance.appendDelta(tce.api_index, delta) catch {
                        ctx.deinit();
                        stream.completeWithError("oom tool call delta");
                        return;
                    };

                    if (tool_call_tracker_instance.getContentIndex(tce.api_index)) |content_index| {
                        pushEvent(stream, .{
                            .toolcall_delta = .{
                                .content_index = content_index,
                                .delta = delta,
                                .partial = .{
                                    .content = &.{},
                                    .api = model.api,
                                    .provider = model.provider,
                                    .model = model.id,
                                    .usage = usage,
                                    .stop_reason = stop_reason,
                                    .timestamp = compat_mod.time.nowMillis(),
                                },
                            },
                        });
                    }
                }
            }
        }
    }

    if (usage.total_tokens == 0) usage.total_tokens = usage.input + usage.output + usage.cache_read + usage.cache_write;
    usage.calculateCost(model.cost);

    var api_indices = std.ArrayList(usize).empty;
    defer api_indices.deinit(allocator);
    api_indices.ensureTotalCapacity(allocator, tool_call_tracker_instance.count()) catch {};

    var tc_iter = tool_call_tracker_instance.calls.iterator();
    while (tc_iter.next()) |entry| {
        api_indices.append(allocator, entry.key_ptr.*) catch {};
    }

    std.mem.sort(usize, api_indices.items, tool_call_tracker_instance, struct {
        fn lessThan(tracker: tool_call_tracker.ToolCallTracker, a: usize, b: usize) bool {
            const a_idx = tracker.getContentIndex(a) orelse 0;
            const b_idx = tracker.getContentIndex(b) orelse 0;
            return a_idx < b_idx;
        }
    }.lessThan);

    const has_thinking = thinking.items.len > 0;
    const has_text = text.items.len > 0;
    const content_count: usize = if (has_thinking) 1 else 0;
    const content_count_final = content_count + (if (has_text) @as(usize, 1) else @as(usize, 0)) + tool_call_count;

    if (content_count_final == 0 and stop_reason != .length) {
        const summary = if (finished) "the model returned an empty reply" else "the stream ended before the model replied";
        const message = trace.describe(allocator, summary, usage) catch null;
        defer if (message) |owned| allocator.free(owned);
        ctx.deinit();
        stream.completeWithError(message orelse summary);
        return;
    }

    if (content_count_final == 0) {
        var content = allocator.alloc(ai_types.AssistantContent, 1) catch {
            ctx.deinit();
            stream.completeWithError("oom building result");
            return;
        };
        content[0] = .{ .text = .{ .text = "" } };
        const api = allocator.dupe(u8, model.api) catch {
            ai_types.deinitAssistantContent(allocator, content);
            ctx.deinit();
            stream.completeWithError("oom building result");
            return;
        };
        const provider = allocator.dupe(u8, model.provider) catch {
            allocator.free(api);
            ai_types.deinitAssistantContent(allocator, content);
            ctx.deinit();
            stream.completeWithError("oom building result");
            return;
        };
        const model_id = allocator.dupe(u8, model.id) catch {
            allocator.free(api);
            allocator.free(provider);
            ai_types.deinitAssistantContent(allocator, content);
            ctx.deinit();
            stream.completeWithError("oom building result");
            return;
        };
        const out = ai_types.AssistantMessage{
            .content = content,
            .api = api,
            .provider = provider,
            .model = model_id,
            .usage = usage,
            .stop_reason = stop_reason,
            .timestamp = compat_mod.time.nowMillis(),
            .is_owned = true,
        };
        ctx.deinit();
        ai_types.settleProviderOutcome(stream, out);
        return;
    }

    var content = allocator.alloc(ai_types.AssistantContent, content_count_final) catch {
        ctx.deinit();
        stream.completeWithError("oom building result");
        return;
    };
    var idx: usize = 0;

    if (has_thinking) {
        content[idx] = .{
            .thinking = .{
                .thinking = allocator.dupe(u8, thinking.items) catch {
                    allocator.free(content);
                    ctx.deinit();
                    stream.completeWithError("oom building thinking");
                    return;
                },
                .thinking_signature = if (reasoning_signature) |sig| allocator.dupe(u8, sig) catch {
                    for (content[0..idx]) |*block| {
                        switch (block.*) {
                            .thinking => |t| allocator.free(t.thinking),
                            else => {},
                        }
                    }
                    allocator.free(content);
                    ctx.deinit();
                    stream.completeWithError("oom building signature");
                    return;
                } else null,
            },
        };
        idx += 1;
    }

    if (has_text) {
        content[idx] = .{
            .text = .{
                .text = allocator.dupe(u8, text.items) catch {
                    for (content[0..idx]) |*block| {
                        switch (block.*) {
                            .thinking => |t| {
                                allocator.free(t.thinking);
                                if (t.thinking_signature) |sig| allocator.free(sig);
                            },
                            else => {},
                        }
                    }
                    allocator.free(content);
                    ctx.deinit();
                    stream.completeWithError("oom building text");
                    return;
                },
            },
        };
        idx += 1;
    }

    for (api_indices.items) |api_idx| {
        if (tool_call_tracker_instance.completeCall(api_idx, allocator)) |tc| {
            content[idx] = .{ .tool_call = tc };

            pushEvent(stream, .{
                .toolcall_end = .{
                    .content_index = idx,
                    .tool_call = tc,
                    .partial = .{
                        .content = content[0..idx],
                        .api = model.api,
                        .provider = model.provider,
                        .model = model.id,
                        .usage = usage,
                        .stop_reason = stop_reason,
                        .timestamp = compat_mod.time.nowMillis(),
                    },
                },
            });

            idx += 1;
        }
    }

    const api = allocator.dupe(u8, model.api) catch {
        ai_types.deinitAssistantContent(allocator, content);
        ctx.deinit();
        stream.completeWithError("oom");
        return;
    };
    const provider = allocator.dupe(u8, model.provider) catch {
        allocator.free(api);
        ai_types.deinitAssistantContent(allocator, content);
        ctx.deinit();
        stream.completeWithError("oom");
        return;
    };
    const model_id = allocator.dupe(u8, model.id) catch {
        allocator.free(api);
        allocator.free(provider);
        ai_types.deinitAssistantContent(allocator, content);
        ctx.deinit();
        stream.completeWithError("oom");
        return;
    };

    const out = ai_types.AssistantMessage{
        .content = content,
        .api = api,
        .provider = provider,
        .model = model_id,
        .usage = usage,
        .stop_reason = stop_reason,
        .timestamp = compat_mod.time.nowMillis(),
        .is_owned = true,
    };

    ctx.deinit();
    ai_types.settleProviderOutcome(stream, out);
}

pub fn streamOpenAICompletions(
    model: ai_types.Model,
    context: ai_types.Context,
    options: ?ai_types.StreamOptions,
    allocator: std.mem.Allocator,
) !*event_stream.AssistantMessageEventStream {
    const resolved = options orelse ai_types.StreamOptions{};

    var key_owned: ?[]const u8 = null;
    const api_key = blk: {
        if (resolved.getApiKey()) |k| {
            if (k.len > 0) break :blk try allocator.dupe(u8, k);
        }
        key_owned = provider_catalog.apiKeyFromEnv(allocator, model.provider);
        if (key_owned) |k| {
            if (k.len > 0) break :blk k;
            allocator.free(k);
            key_owned = null;
        }
        if (allowsAnonymous(model)) break :blk try allocator.dupe(u8, "");
        return error.MissingApiKey;
    };
    errdefer allocator.free(api_key);

    const owned_model = try ai_types.cloneModel(allocator, model);
    errdefer {
        var mut_m = owned_model;
        mut_m.deinit(allocator);
    }

    const owned_context = try ai_types.cloneContext(allocator, context);
    errdefer {
        var mut_ctx = owned_context;
        mut_ctx.deinit(allocator);
    }

    const req_body = try buildRequestBody(owned_model, owned_context, resolved, allocator);
    errdefer allocator.free(req_body);

    const conversation_id: ?[]u8 = if (wantsConversationId(owned_model)) try conversationId(allocator, resolved.getSessionId(), owned_context) else null;
    errdefer if (conversation_id) |id| allocator.free(id);

    const s = try allocator.create(event_stream.AssistantMessageEventStream);
    errdefer allocator.destroy(s);
    s.* = event_stream.AssistantMessageEventStream.init(allocator);
    s.wait_for_thread_on_deinit = true;
    s.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };

    const ctx = try allocator.create(ThreadCtx);
    errdefer {
        allocator.destroy(s);
        allocator.free(req_body);
        var mut_ctx = owned_context;
        mut_ctx.deinit(allocator);
        var mut_m = owned_model;
        mut_m.deinit(allocator);
        allocator.free(api_key);
        allocator.destroy(ctx);
    }
    ctx.* = .{
        .allocator = allocator,
        .stream = s,
        .model = owned_model,
        .api_key = @constCast(api_key),
        .request_body = req_body,
        .context = owned_context,
        .cancel_token = resolved.cancel_token,
        .on_payload_fn = resolved.on_payload_fn,
        .on_payload_ctx = resolved.on_payload_ctx,
        .retry_config = resolved.retry,
        .ping_interval_ms = resolved.ping_interval_ms,
        .conversation_id = conversation_id,
    };

    const th = try std.Thread.spawn(.{}, runThread, .{ctx});
    th.detach();
    return s;
}

test "OpenCode Go gets a conversation id, the session's when there is one, else one stable across a conversation's turns" {
    const allocator = std.testing.allocator;
    var model = traceModel("https://opencode.ai/zen/go/v1");
    model.provider = "opencode-go";
    try std.testing.expect(wantsConversationId(model));
    model.provider = "opencode-zen";
    try std.testing.expect(!wantsConversationId(model));

    const first = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "plan the work" }, .timestamp = 0 } }};
    const later = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "plan the work" }, .timestamp = 0 } },
        .{ .user = .{ .content = .{ .text = "and then do it" }, .timestamp = 1 } },
    };
    const other = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "something else" }, .timestamp = 0 } }};

    const given = try conversationId(allocator, "ses-tui-1", .{ .messages = &first });
    defer allocator.free(given);
    try std.testing.expectEqualStrings("ses-tui-1", given);

    const a = try conversationId(allocator, null, .{ .messages = &first });
    defer allocator.free(a);
    const b = try conversationId(allocator, null, .{ .messages = &later });
    defer allocator.free(b);
    const c = try conversationId(allocator, null, .{ .messages = &other });
    defer allocator.free(c);
    try std.testing.expectEqualStrings(a, b);
    try std.testing.expect(!std.mem.eql(u8, a, c));
    try std.testing.expect(std.mem.startsWith(u8, a, "oapx-"));

    const image_a = [_]ai_types.UserContentPart{.{ .image = .{ .data = "aaaa", .mime_type = "image/png" } }};
    const image_b = [_]ai_types.UserContentPart{.{ .image = .{ .data = "bbbb", .mime_type = "image/png" } }};
    const split = [_]ai_types.UserContentPart{ .{ .text = .{ .text = "plan the " } }, .{ .text = .{ .text = "work" } } };
    const pictured_a = [_]ai_types.Message{.{ .user = .{ .content = .{ .parts = &image_a }, .timestamp = 0 } }};
    const pictured_b = [_]ai_types.Message{.{ .user = .{ .content = .{ .parts = &image_b }, .timestamp = 0 } }};
    const split_message = [_]ai_types.Message{.{ .user = .{ .content = .{ .parts = &split }, .timestamp = 0 } }};
    const pa = try conversationId(allocator, null, .{ .messages = &pictured_a });
    defer allocator.free(pa);
    const pb = try conversationId(allocator, null, .{ .messages = &pictured_b });
    defer allocator.free(pb);
    const sp = try conversationId(allocator, null, .{ .messages = &split_message });
    defer allocator.free(sp);
    try std.testing.expect(!std.mem.eql(u8, pa, pb));
    try std.testing.expect(!std.mem.eql(u8, sp, a));
    try std.testing.expect(std.mem.startsWith(u8, client_user_agent, "oapx/"));
}

fn thinkingLevelToString(level: ai_types.ThinkingLevel) []const u8 {
    return switch (level) {
        .off => "off",
        .minimal => "minimal",
        .low => "low",
        .medium => "medium",
        .high => "high",
        .xhigh, .max => "xhigh",
    };
}

pub fn streamSimpleOpenAICompletions(
    model: ai_types.Model,
    context: ai_types.Context,
    options: ?ai_types.SimpleStreamOptions,
    allocator: std.mem.Allocator,
) !*event_stream.AssistantMessageEventStream {
    const o = options orelse ai_types.SimpleStreamOptions{};
    return streamOpenAICompletions(model, context, .{
        .temperature = o.temperature,
        .max_tokens = o.max_tokens,
        .api_key = if (o.api_key) |k| ai_types.OwnedSlice(u8).initBorrowed(k) else ai_types.OwnedSlice(u8).initBorrowed(""),
        .cache_retention = o.cache_retention,
        .session_id = if (o.session_id) |sid| ai_types.OwnedSlice(u8).initBorrowed(sid) else ai_types.OwnedSlice(u8).initBorrowed(""),
        .headers = o.headers,
        .retry = o.retry,
        .cancel_token = o.cancel_token,
        .on_payload_fn = o.on_payload_fn,
        .on_payload_ctx = o.on_payload_ctx,
        .reasoning_effort = if (o.reasoning) |r| ai_types.OwnedSlice(u8).initBorrowed(
            if (provider_caps.usesDeepSeekWire(model.provider, model.base_url))
                provider_caps.deepSeekEffort(@tagName(r))
            else
                thinkingLevelToString(r),
        ) else ai_types.OwnedSlice(u8).initBorrowed(""),
    }, allocator);
}

pub fn registerOpenAICompletionsApiProvider(registry: *api_registry.ApiRegistry) !void {
    try registry.registerApiProvider(.{
        .api = "openai-completions",
        .stream = streamOpenAICompletions,
        .stream_simple = streamSimpleOpenAICompletions,
    }, null);
}

test "anonymous streaming is opt-in and never applies to an openai vendor id" {
    const base: ai_types.Model = .{
        .id = "m",
        .name = "M",
        .api = "openai-completions",
        .provider = "gateway",
        .base_url = "https://gw.test",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1000,
        .max_tokens = 100,
    };
    try std.testing.expect(!allowsAnonymous(base));

    var opted = base;
    opted.allows_anonymous = true;
    try std.testing.expect(allowsAnonymous(opted));

    for ([_][]const u8{ "openai", "deepseek", "kimi", "github-copilot" }) |vendor_id| {
        var vendor = opted;
        vendor.provider = vendor_id;
        try std.testing.expect(!allowsAnonymous(vendor));
    }
}

test "buildRequestBody includes stream_options and tools without memory leak" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const tool = ai_types.Tool{
        .name = "test_tool",
        .description = "A test tool",
        .parameters_schema_json = "{\"type\":\"object\",\"properties\":{\"arg\":{\"type\":\"string\"}}}",
    };

    const assistant_content = [_]ai_types.AssistantContent{
        .{ .tool_call = .{
            .id = "call_123",
            .name = "test_tool",
            .arguments_json = "{\"arg\":\"value\"}",
        } },
    };

    const assistant_msg = ai_types.Message{ .assistant = .{
        .content = &assistant_content,
        .api = "openai-completions",
        .provider = "openai",
        .model = "gpt-4o-mini",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = compat_mod.time.nowSeconds(),
    } };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{assistant_msg},
        .tools = &[_]ai_types.Tool{tool},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, body, .{});
    defer parsed.deinit();
    try std.testing.expect(std.mem.find(u8, body, "stream_options") != null);
    try std.testing.expect(std.mem.find(u8, body, "include_usage") != null);
    try std.testing.expect(std.mem.find(u8, body, "tools") != null);
    try std.testing.expect(std.mem.find(u8, body, "tool_calls") != null);
    try std.testing.expect(std.mem.find(u8, body, "test_tool") != null);
}

test "buildRequestBody omits non-default temperature for Kimi" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "kimi-k2.7-code",
        .name = "Kimi K2.7 Code",
        .api = "openai-completions",
        .provider = "kimi",
        .base_url = "https://api.kimi.com/coding/",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 262_144,
        .max_tokens = 100,
    };

    const messages = [_]ai_types.Message{.{ .user = .{
        .content = .{ .text = "Hello" },
        .timestamp = 0,
    } }};
    const ctx = ai_types.Context{
        .system_prompt = ai_types.OwnedSlice(u8).initBorrowed("You are helpful"),
        .messages = &messages,
        .tools = null,
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100, .temperature = 0.7 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "\"temperature\"") == null);
}

test "buildRequestBody with assistant message containing tool_calls" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const assistant_content = [_]ai_types.AssistantContent{
        .{ .text = .{ .text = "Let me help you with that." } },
        .{ .tool_call = .{
            .id = "call_456",
            .name = "bash",
            .arguments_json = "{\"cmd\":\"ls -la\"}",
        } },
    };

    const assistant_msg = ai_types.Message{ .assistant = .{
        .content = &assistant_content,
        .api = "openai-completions",
        .provider = "openai",
        .model = "gpt-4o-mini",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = compat_mod.time.nowSeconds(),
    } };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{assistant_msg},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "tool_calls") != null);
    try std.testing.expect(std.mem.find(u8, body, "call_456") != null);
    try std.testing.expect(std.mem.find(u8, body, "bash") != null);
    try std.testing.expect(std.mem.find(u8, body, "ls -la") != null);
}

test "stream errors can finalize accumulated text but not partial tool calls" {
    try std.testing.expect(canCompletePartialTextOnStreamError(1, 0, 0));
    try std.testing.expect(canCompletePartialTextOnStreamError(0, 1, 0));
    try std.testing.expect(!canCompletePartialTextOnStreamError(0, 0, 0));
    try std.testing.expect(!canCompletePartialTextOnStreamError(1, 0, 1));
}

test "parseChunk ignores a chunk that is not json and a json value that is not an object" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .content_filter;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunks = [_][]const u8{
        "{not json at all",
        "{\"choices\":",
        "[]",
        "42",
        "\"a string\"",
        "null",
    };
    for (chunks) |chunk| {
        _ = try parseChunk(
            chunk,
            &text,
            &thinking,
            &usage,
            &stop_reason,
            &current_block,
            &reasoning_signature,
            &tool_call_events,
            &reasoning_detail_events,
            allocator,
        );
    }

    try std.testing.expectEqual(@as(usize, 0), text.items.len);
    try std.testing.expectEqual(@as(usize, 0), thinking.items.len);
    try std.testing.expectEqual(@as(usize, 0), tool_call_events.items.len);
    try std.testing.expectEqual(@as(usize, 0), reasoning_detail_events.items.len);
    try std.testing.expectEqual(ai_types.Usage{}, usage);
    try std.testing.expectEqual(ai_types.StopReason.content_filter, stop_reason);
}

test "parseChunk keeps reading after a chunk it could not read" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .content_filter;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const args = [_][]const u8{
        "not json",
        "{\"choices\":[{\"delta\":{\"content\":\"kept\"}}]}",
    };
    for (args) |chunk| {
        _ = try parseChunk(
            chunk,
            &text,
            &thinking,
            &usage,
            &stop_reason,
            &current_block,
            &reasoning_signature,
            &tool_call_events,
            &reasoning_detail_events,
            allocator,
        );
    }

    try std.testing.expectEqualStrings("kept", text.items);
}

test "a deepseek request sends past reasoning back as reasoning_content, never as the answer" {
    const allocator = std.testing.allocator;
    const model = ai_types.Model{
        .id = "deepseek-flash",
        .name = "DeepSeek Flash",
        .api = "openai-completions",
        .provider = "deepseek",
        .base_url = "https://api.deepseek.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1_048_576,
        .max_tokens = 100,
    };

    const messages = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "list the files" }, .timestamp = 0 } },
        .{ .assistant = .{
            .content = &.{
                .{ .thinking = .{ .thinking = "plan the listing", .thinking_signature = "reasoning_content" } },
                .{ .tool_call = .{ .id = "call-1", .name = "ls", .arguments_json = "{}" } },
            },
            .api = "openai-completions",
            .provider = "deepseek",
            .model = "deepseek-flash",
            .usage = .{},
            .stop_reason = .tool_use,
            .timestamp = 0,
        } },
        .{ .tool_result = .{ .tool_call_id = "call-1", .tool_name = "ls", .content = &.{.{ .text = .{ .text = "a.zig" } }}, .is_error = false, .timestamp = 0 } },
        .{ .assistant = .{
            .content = &.{
                .{ .thinking = .{ .thinking = "one file", .thinking_signature = "reasoning_content" } },
                .{ .text = .{ .text = "There is a.zig." } },
            },
            .api = "openai-completions",
            .provider = "deepseek",
            .model = "deepseek-flash",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = 0,
        } },
    };

    const body = try buildRequestBody(model, .{ .messages = &messages }, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);
    const parsed = try std.json.parseFromSlice(std.json.Value, allocator, body, .{});
    defer parsed.deinit();

    var assistants: usize = 0;
    for (parsed.value.object.get("messages").?.array.items) |m| {
        if (!std.mem.eql(u8, m.object.get("role").?.string, "assistant")) continue;
        assistants += 1;
        const reasoning = m.object.get("reasoning_content") orelse return error.TestExpectedReasoningContent;
        const content = m.object.get("content").?;
        if (assistants == 1) {
            try std.testing.expectEqualStrings("plan the listing", reasoning.string);
            try std.testing.expect(content == .null);
        } else {
            try std.testing.expectEqualStrings("one file", reasoning.string);
            try std.testing.expectEqual(@as(usize, 1), content.array.items.len);
            try std.testing.expectEqualStrings("There is a.zig.", content.array.items[0].object.get("text").?.string);
        }
    }
    try std.testing.expectEqual(@as(usize, 2), assistants);
}

test "a reply that holds only reasoning goes back to deepseek as its content, since deepseek refuses one with neither content nor tool calls" {
    const allocator = std.testing.allocator;
    const model = ai_types.Model{
        .id = "deepseek-flash",
        .name = "DeepSeek Flash",
        .api = "openai-completions",
        .provider = "deepseek",
        .base_url = "https://api.deepseek.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1_048_576,
        .max_tokens = 100,
    };
    const messages = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "what is the plan?" }, .timestamp = 0 } },
        .{ .assistant = .{
            .content = &.{.{ .thinking = .{ .thinking = "the plan, written as reasoning", .thinking_signature = "reasoning_content" } }},
            .api = "openai-completions",
            .provider = "deepseek",
            .model = "deepseek-flash",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = 0,
        } },
        .{ .user = .{ .content = .{ .text = "write the answer" }, .timestamp = 0 } },
    };

    const body = try buildRequestBody(model, .{ .messages = &messages }, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);
    const parsed = try std.json.parseFromSlice(std.json.Value, allocator, body, .{});
    defer parsed.deinit();

    const written = parsed.value.object.get("messages").?.array.items;
    try std.testing.expectEqual(@as(usize, 3), written.len);
    const reply = written[1].object;
    try std.testing.expectEqualStrings("assistant", reply.get("role").?.string);
    try std.testing.expect(reply.get("reasoning_content") == null);
    try std.testing.expect(reply.get("tool_calls") == null);
    const content = reply.get("content").?.array.items;
    try std.testing.expectEqual(@as(usize, 1), content.len);
    try std.testing.expectEqualStrings("the plan, written as reasoning", content[0].object.get("text").?.string);
}

test "a deepseek request carries the thinking level as one of deepseek's three efforts" {
    const allocator = std.testing.allocator;
    const model = ai_types.Model{
        .id = "deepseek-flash",
        .name = "DeepSeek Flash",
        .api = "openai-completions",
        .provider = "deepseek",
        .base_url = "https://api.deepseek.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1_048_576,
        .max_tokens = 100,
    };
    const messages = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "hi" }, .timestamp = 0 } }};
    const cases = [_]struct { level: []const u8, sent: []const u8 }{
        .{ .level = "minimal", .sent = "low" },
        .{ .level = "low", .sent = "low" },
        .{ .level = "medium", .sent = "high" },
        .{ .level = "high", .sent = "high" },
        .{ .level = "xhigh", .sent = "max" },
        .{ .level = "max", .sent = "max" },
        .{ .level = "ultra", .sent = "max" },
    };
    for (cases) |case| {
        const body = try buildRequestBody(model, .{ .messages = &messages }, .{
            .max_tokens = 100,
            .reasoning_effort = ai_types.OwnedSlice(u8).initBorrowed(case.level),
        }, allocator);
        defer allocator.free(body);
        const parsed = try std.json.parseFromSlice(std.json.Value, allocator, body, .{});
        defer parsed.deinit();
        try std.testing.expectEqualStrings(case.sent, parsed.value.object.get("reasoning_effort").?.string);
    }
}

test "an opencode request carries the effort opencode offers the model, and none for a family it offers none" {
    const allocator = std.testing.allocator;
    const messages = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "hi" }, .timestamp = 0 } }};
    const cases = [_]struct { provider: []const u8, id: []const u8, level: []const u8, sent: ?[]const u8 }{
        .{ .provider = "opencode-go", .id = "deepseek-v4.1-flash", .level = "xhigh", .sent = "max" },
        .{ .provider = "opencode-go", .id = "deepseek-v4.1-flash", .level = "low", .sent = "low" },
        .{ .provider = "opencode-zen", .id = "gpt-6-luna", .level = "medium", .sent = "medium" },
        .{ .provider = "opencode-go", .id = "kimi-k3", .level = "high", .sent = null },
    };
    for (cases) |case| {
        const model = ai_types.Model{
            .id = case.id,
            .name = case.id,
            .api = "openai-completions",
            .provider = case.provider,
            .base_url = "https://opencode.ai/zen/go/v1",
            .reasoning = true,
            .input = &[_][]const u8{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 1_048_576,
            .max_tokens = 100,
        };
        const body = try buildRequestBody(model, .{ .messages = &messages }, .{
            .max_tokens = 100,
            .reasoning_effort = ai_types.OwnedSlice(u8).initBorrowed(case.level),
        }, allocator);
        defer allocator.free(body);
        const parsed = try std.json.parseFromSlice(std.json.Value, allocator, body, .{});
        defer parsed.deinit();
        const sent = parsed.value.object.get("reasoning_effort");
        if (case.sent) |expected| {
            try std.testing.expectEqualStrings(expected, sent.?.string);
        } else {
            try std.testing.expect(sent == null);
        }
    }
}

test "writeMessagesArray drops an orphan wherever it falls in a run of results" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const messages = [_]ai_types.Message{
        .{ .assistant = .{
            .content = &.{
                .{ .tool_call = .{ .id = "call-1", .name = "read", .arguments_json = "{}" } },
                .{ .tool_call = .{ .id = "call-2", .name = "read", .arguments_json = "{}" } },
            },
            .api = "openai-completions",
            .provider = "openai",
            .model = "gpt-4o-mini",
            .usage = .{},
            .stop_reason = .tool_use,
            .timestamp = 0,
        } },
        .{ .tool_result = .{ .tool_call_id = "call-1", .tool_name = "read", .content = &.{.{ .text = .{ .text = "first" } }}, .is_error = false, .timestamp = 0 } },
        .{ .tool_result = .{ .tool_call_id = "call-9", .tool_name = "read", .content = &.{.{ .text = .{ .text = "orphan" } }}, .is_error = false, .timestamp = 0 } },
        .{ .tool_result = .{ .tool_call_id = "call-2", .tool_name = "read", .content = &.{.{ .text = .{ .text = "second" } }}, .is_error = false, .timestamp = 0 } },
    };

    const ctx = ai_types.Context{ .messages = &messages };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    const parsed = try std.json.parseFromSlice(std.json.Value, allocator, body, .{});
    defer parsed.deinit();

    const written = parsed.value.object.get("messages").?.array;
    var tool_ids = std.ArrayList([]const u8).empty;
    defer tool_ids.deinit(allocator);
    for (written.items) |m| {
        if (m.object.get("role")) |role| {
            if (role.string.len == 4 and std.mem.eql(u8, role.string, "tool")) {
                try tool_ids.append(allocator, m.object.get("tool_call_id").?.string);
            }
        }
    }

    try std.testing.expectEqual(@as(usize, 2), tool_ids.items.len);
    try std.testing.expectEqualStrings("call-1", tool_ids.items[0]);
    try std.testing.expectEqualStrings("call-2", tool_ids.items[1]);
}

test "parseChunk does not leak memory with reasoning content" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer tool_call_events.deinit(allocator);
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunk_data =
        \\{"choices":[{"delta":{"reasoning_content":"Let me think about this..."}}]}
    ;

    _ = try parseChunk(
        chunk_data,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 0), text.items.len);
    try std.testing.expect(thinking.items.len > 0);
    try std.testing.expectEqualStrings("reasoning_content", reasoning_signature.?);
    try std.testing.expectEqual(BlockType.thinking, current_block);
}

test "parseChunk handles multiple chunks without leaking" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer tool_call_events.deinit(allocator);
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunks = [_][]const u8{
        \\{"choices":[{"delta":{"reasoning_content":"First"}}]}
        ,
        \\{"choices":[{"delta":{"reasoning_content":" Second"}}]}
        ,
        \\{"choices":[{"delta":{"content":"Final text"}}]}
        ,
    };

    for (chunks) |chunk| {
        _ = try parseChunk(
            chunk,
            &text,
            &thinking,
            &usage,
            &stop_reason,
            &current_block,
            &reasoning_signature,
            &tool_call_events,
            &reasoning_detail_events,
            allocator,
        );
    }

    try std.testing.expectEqualStrings("Final text", text.items);
    try std.testing.expectEqualStrings("First Second", thinking.items);
}

test "parseChunk handles tool_calls without leaking" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunk1 =
        \\{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"bash","arguments":""}}]}}]}
    ;

    _ = try parseChunk(
        chunk1,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), tool_call_events.items.len);
    try std.testing.expect(tool_call_events.items[0].is_start);
    try std.testing.expectEqual(@as(usize, 0), tool_call_events.items[0].api_index);
    try std.testing.expectEqualStrings("call_abc123", tool_call_events.items[0].id.?);
    try std.testing.expectEqualStrings("bash", tool_call_events.items[0].name.?);

    for (tool_call_events.items) |*tce| {
        @constCast(tce).deinit(allocator);
    }
    tool_call_events.clearRetainingCapacity();

    const chunk2 =
        \\{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"cmd\": \"ls\""}}]}}]}
    ;

    _ = try parseChunk(
        chunk2,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), tool_call_events.items.len);
    try std.testing.expect(!tool_call_events.items[0].is_start);
    try std.testing.expectEqual(@as(usize, 0), tool_call_events.items[0].api_index);
    try std.testing.expectEqualStrings("{\"cmd\": \"ls\"", tool_call_events.items[0].arguments_delta.?);

    for (tool_call_events.items) |*tce| {
        @constCast(tce).deinit(allocator);
    }
    tool_call_events.clearRetainingCapacity();

    const chunk3 =
        \\{"choices":[{"finish_reason":"tool_calls"}]}
    ;

    _ = try parseChunk(
        chunk3,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(ai_types.StopReason.tool_use, stop_reason);
}

test "parseChunk handles multiple tool_calls" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunk1 =
        \\{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_001","type":"function","function":{"name":"read","arguments":""}}]}}]}
    ;

    _ = try parseChunk(
        chunk1,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), tool_call_events.items.len);
    try std.testing.expect(tool_call_events.items[0].is_start);
    try std.testing.expectEqualStrings("call_001", tool_call_events.items[0].id.?);

    for (tool_call_events.items) |*tce| {
        @constCast(tce).deinit(allocator);
    }
    tool_call_events.clearRetainingCapacity();

    const chunk2 =
        \\{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_002","type":"function","function":{"name":"write","arguments":""}}]}}]}
    ;

    _ = try parseChunk(
        chunk2,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), tool_call_events.items.len);
    try std.testing.expect(tool_call_events.items[0].is_start);
    try std.testing.expectEqualStrings("call_002", tool_call_events.items[0].id.?);
    try std.testing.expectEqualStrings("write", tool_call_events.items[0].name.?);

    for (tool_call_events.items) |*tce| {
        @constCast(tce).deinit(allocator);
    }
    tool_call_events.clearRetainingCapacity();

    const chunk3 =
        \\{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}
    ;

    _ = try parseChunk(
        chunk3,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), tool_call_events.items.len);
    try std.testing.expect(!tool_call_events.items[0].is_start);
    try std.testing.expectEqual(@as(usize, 0), tool_call_events.items[0].api_index);
}

test "isOpenRouterAnthropic detection" {
    const model1 = ai_types.Model{
        .id = "anthropic/claude-3-opus",
        .name = "Claude 3 Opus",
        .api = "openai-completions",
        .provider = "openrouter",
        .base_url = "https://openrouter.ai/api/v1",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 100,
    };
    try std.testing.expect(isOpenRouterAnthropic(model1));

    const model2 = ai_types.Model{
        .id = "openai/gpt-4o",
        .name = "GPT-4o",
        .api = "openai-completions",
        .provider = "openrouter",
        .base_url = "https://openrouter.ai/api/v1",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };
    try std.testing.expect(!isOpenRouterAnthropic(model2));

    const model3 = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };
    try std.testing.expect(!isOpenRouterAnthropic(model3));
}

test "buildRequestBody adds cache_control for OpenRouter Anthropic models" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "anthropic/claude-3-opus",
        .name = "Claude 3 Opus",
        .api = "openai-completions",
        .provider = "openrouter",
        .base_url = "https://openrouter.ai/api/v1",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 100,
    };

    const user_msg1 = ai_types.Message{ .user = .{ .content = .{ .text = "First message" }, .timestamp = compat_mod.time.nowSeconds() } };
    const user_msg2 = ai_types.Message{ .user = .{ .content = .{ .text = "Second message" }, .timestamp = compat_mod.time.nowSeconds() } };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{ user_msg1, user_msg2 },
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "cache_control") != null);
    try std.testing.expect(std.mem.find(u8, body, "ephemeral") != null);

    try std.testing.expect(std.mem.find(u8, body, "\"content\":[") != null);
    try std.testing.expect(std.mem.find(u8, body, "\"type\":\"text\"") != null);
}

test "buildRequestBody does not add cache_control for non-OpenRouter Anthropic" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const user_msg = ai_types.Message{ .user = .{ .content = .{ .text = "Hello" }, .timestamp = compat_mod.time.nowSeconds() } };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{user_msg},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "cache_control") == null);
}

test "mergeCompat uses model-level compat options over detected capabilities" {
    const model = ai_types.Model{
        .id = "custom-model",
        .name = "Custom Model",
        .api = "openai-completions",
        .provider = "custom",
        .base_url = "https://api.openai.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{
            .supports_store = false,
            .supports_reasoning_effort = false,
            .max_tokens_field = .max_tokens,
            .supports_strict_mode = false,
        },
    };

    const merged = mergeCompat(model);

    try std.testing.expect(!merged.supports_store);
    try std.testing.expect(!merged.supports_reasoning_effort);
    try std.testing.expectEqualStrings("max_tokens", merged.max_tokens_field);
    try std.testing.expect(!merged.supports_strict_mode);
}

test "mergeCompat falls back to detected capabilities when model compat is null" {
    const model = ai_types.Model{
        .id = "gpt-4o",
        .name = "GPT-4o",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const merged = mergeCompat(model);

    try std.testing.expect(merged.supports_store);
    try std.testing.expect(merged.supports_developer_role);
    try std.testing.expect(merged.supports_reasoning_effort);
    try std.testing.expectEqualStrings("max_completion_tokens", merged.max_tokens_field);
}

test "mergeCompat keeps custom OpenAI endpoints generic" {
    const model: ai_types.Model = .{
        .id = "custom-model",
        .name = "Custom Model",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://proxy.example.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const merged = mergeCompat(model);
    try std.testing.expect(!merged.supports_store);
    try std.testing.expect(!merged.supports_developer_role);
    try std.testing.expect(!merged.supports_reasoning_effort);
    try std.testing.expectEqualStrings("max_tokens", merged.max_tokens_field);
}

test "a declared capability does not drag OpenAI-native defaults along with it" {
    const model: ai_types.Model = .{
        .id = "gateway-model",
        .name = "Gateway Model",
        .api = "openai-completions",
        .provider = "gateway",
        .base_url = "https://gw.internal",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{ .supports_anthropic_cache_ttl = true },
    };

    const merged = mergeCompat(model);
    try std.testing.expectEqualStrings("max_tokens", merged.max_tokens_field);
    try std.testing.expect(!merged.supports_strict_mode);
    try std.testing.expect(!merged.supports_store);
    try std.testing.expect(!merged.supports_developer_role);
    try std.testing.expect(!merged.supports_reasoning_effort);

    var keyless = model;
    keyless.compat = null;
    const detected = mergeCompat(keyless);
    try std.testing.expectEqualStrings(detected.max_tokens_field, merged.max_tokens_field);
    try std.testing.expectEqual(detected.supports_strict_mode, merged.supports_strict_mode);
    try std.testing.expectEqual(detected.supports_usage_in_streaming, merged.supports_usage_in_streaming);
}

test "a partial compat block keeps the detected thinking format" {
    const model: ai_types.Model = .{
        .id = "glm-4.6",
        .name = "GLM 4.6",
        .api = "openai-completions",
        .provider = "gateway",
        .base_url = "https://api.zukijourney.com/v1",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{ .supports_anthropic_cache_ttl = true },
    };

    const merged = mergeCompat(model);
    try std.testing.expect(merged.thinking_format == .zai);

    var keyless = model;
    keyless.compat = null;
    try std.testing.expectEqual(mergeCompat(keyless).thinking_format, merged.thinking_format);
}

test "mergeCompat keeps gateway URLs containing the OpenAI host in their path generic" {
    const model: ai_types.Model = .{
        .id = "custom-model",
        .name = "Custom Model",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://gateway.example/api.openai.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const merged = mergeCompat(model);
    try std.testing.expect(!merged.supports_store);
    try std.testing.expect(!merged.supports_developer_role);
    try std.testing.expect(!merged.supports_reasoning_effort);
    try std.testing.expectEqualStrings("max_tokens", merged.max_tokens_field);
}

test "buildRequestBody uses max_tokens field from compat options" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "mistral-large",
        .name = "Mistral Large",
        .api = "openai-completions",
        .provider = "mistral",
        .base_url = "https://api.mistral.ai",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{
            .max_tokens_field = .max_tokens,
            .supports_store = false,
        },
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 50 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "\"max_tokens\":50") != null);
    try std.testing.expect(std.mem.find(u8, body, "max_completion_tokens") == null);
    try std.testing.expect(std.mem.find(u8, body, "\"store\"") == null);
}

test "buildRequestBody adds strict mode when supported" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{
            .supports_strict_mode = true,
        },
    };

    const tool = ai_types.Tool{
        .name = "test_tool",
        .description = "A test tool",
        .parameters_schema_json = "{\"type\":\"object\"}",
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{},
        .tools = &[_]ai_types.Tool{tool},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "\"strict\":true") != null);
}

test "buildRequestBody omits strict mode when not supported" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "custom-model",
        .name = "Custom Model",
        .api = "openai-completions",
        .provider = "custom",
        .base_url = "https://api.custom.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{
            .supports_strict_mode = false,
        },
    };

    const tool = ai_types.Tool{
        .name = "test_tool",
        .description = "A test tool",
        .parameters_schema_json = "{\"type\":\"object\"}",
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{},
        .tools = &[_]ai_types.Tool{tool},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "\"strict\"") == null);
}

test "buildRequestBody adds store: false for OpenAI native" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "\"store\":false") != null);
}

test "buildRequestBody omits store field for non-OpenAI providers" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "mistral-large",
        .name = "Mistral Large",
        .api = "openai-completions",
        .provider = "mistral",
        .base_url = "https://api.mistral.ai",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "\"store\"") == null);
}

test "parseChunk extracts reasoning_details for encrypted reasoning round-trip" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunk1 =
        \\{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"bash","arguments":""}}]}}]}
    ;

    _ = try parseChunk(
        chunk1,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), tool_call_events.items.len);
    for (tool_call_events.items) |*tce| {
        @constCast(tce).deinit(allocator);
    }
    tool_call_events.clearRetainingCapacity();

    const chunk2 =
        \\{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"call_abc123","data":"encrypted_data_here"}]}}]}
    ;

    _ = try parseChunk(
        chunk2,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), reasoning_detail_events.items.len);
    try std.testing.expectEqualStrings("call_abc123", reasoning_detail_events.items[0].tool_call_id);
    try std.testing.expectEqualStrings(
        "{\"type\":\"reasoning.encrypted\",\"id\":\"call_abc123\",\"data\":\"encrypted_data_here\"}",
        reasoning_detail_events.items[0].detail_json,
    );
}

test "parseChunk encodes a reasoning detail's id and data rather than splicing them" {
    const allocator = std.testing.allocator;

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunk =
        \\{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"call\"1","data":"a\"b\\c"}]}}]}
    ;

    _ = try parseChunk(
        chunk,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), reasoning_detail_events.items.len);
    try std.testing.expectEqualStrings("call\"1", reasoning_detail_events.items[0].tool_call_id);

    const detail = try std.json.parseFromSlice(std.json.Value, allocator, reasoning_detail_events.items[0].detail_json, .{});
    defer detail.deinit();

    try std.testing.expectEqualStrings("reasoning.encrypted", detail.value.object.get("type").?.string);
    try std.testing.expectEqualStrings("call\"1", detail.value.object.get("id").?.string);
    try std.testing.expectEqualStrings("a\"b\\c", detail.value.object.get("data").?.string);
}

test "a reasoning detail carrying a quote leaves the request body parseable" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "o1",
        .name = "O1",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 100,
    };

    var text = std.ArrayList(u8).empty;
    defer text.deinit(allocator);
    var thinking = std.ArrayList(u8).empty;
    defer thinking.deinit(allocator);
    var usage = ai_types.Usage{};
    var stop_reason: ai_types.StopReason = .stop;
    var current_block: BlockType = .none;
    var reasoning_signature: ?[]const u8 = null;
    defer if (reasoning_signature) |sig| allocator.free(sig);
    var tool_call_events = std.ArrayList(ToolCallEvent).empty;
    defer {
        for (tool_call_events.items) |*tce| {
            @constCast(tce).deinit(allocator);
        }
        tool_call_events.deinit(allocator);
    }
    var reasoning_detail_events = std.ArrayList(ReasoningDetailEvent).empty;
    defer {
        for (reasoning_detail_events.items) |*rde| {
            @constCast(rde).deinit(allocator);
        }
        reasoning_detail_events.deinit(allocator);
    }

    const chunk =
        \\{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.encrypted","id":"call_abc123","data":"a\"b"}]}}]}
    ;

    _ = try parseChunk(
        chunk,
        &text,
        &thinking,
        &usage,
        &stop_reason,
        &current_block,
        &reasoning_signature,
        &tool_call_events,
        &reasoning_detail_events,
        allocator,
    );

    try std.testing.expectEqual(@as(usize, 1), reasoning_detail_events.items.len);

    const assistant_content = try allocator.alloc(ai_types.AssistantContent, 1);
    defer allocator.free(assistant_content);
    assistant_content[0] = .{
        .tool_call = .{
            .id = "call_abc123",
            .name = "bash",
            .arguments_json = "{}",
            .thought_signature = reasoning_detail_events.items[0].detail_json,
        },
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{
            .{ .assistant = .{
                .content = assistant_content,
                .api = "openai-completions",
                .provider = "openai",
                .model = "o1",
                .usage = .{},
                .stop_reason = .tool_use,
                .timestamp = 0,
            } },
        },
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    const parsed = std.json.parseFromSlice(std.json.Value, allocator, body, .{}) catch {
        std.debug.print("the body is not json: {s}\n", .{body});
        return error.TestExpectedEqual;
    };
    defer parsed.deinit();

    const messages = parsed.value.object.get("messages").?.array;
    const details = messages.items[0].object.get("reasoning_details").?.array;
    try std.testing.expectEqualStrings("a\"b", details.items[0].object.get("data").?.string);
}

test "buildRequestBody includes reasoning_details for tool calls with thought_signature" {
    const allocator = std.testing.allocator;

    const model = ai_types.Model{
        .id = "o1",
        .name = "O1",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 100,
    };

    const tool_call_id = try allocator.dupe(u8, "call_abc123");
    defer allocator.free(tool_call_id);
    const tool_call_name = try allocator.dupe(u8, "bash");
    defer allocator.free(tool_call_name);
    const tool_call_args = try allocator.dupe(u8, "{\"cmd\":\"ls\"}");
    defer allocator.free(tool_call_args);
    const thought_sig = try allocator.dupe(u8, "{\"type\":\"reasoning.encrypted\",\"id\":\"call_abc123\",\"data\":\"encrypted\"}");
    defer allocator.free(thought_sig);

    const assistant_content = try allocator.alloc(ai_types.AssistantContent, 1);
    defer allocator.free(assistant_content);
    assistant_content[0] = .{
        .tool_call = .{
            .id = tool_call_id,
            .name = tool_call_name,
            .arguments_json = tool_call_args,
            .thought_signature = thought_sig,
        },
    };

    const assistant_msg = ai_types.Message{
        .assistant = .{
            .content = assistant_content,
            .api = "openai-completions",
            .provider = "openai",
            .model = "o1",
            .usage = .{},
            .stop_reason = .tool_use,
            .timestamp = compat_mod.time.nowSeconds(),
        },
    };

    const ctx = ai_types.Context{
        .messages = &[_]ai_types.Message{assistant_msg},
    };

    const body = try buildRequestBody(model, ctx, .{ .max_tokens = 100 }, allocator);
    defer allocator.free(body);

    try std.testing.expect(std.mem.find(u8, body, "reasoning_details") != null);
    try std.testing.expect(std.mem.find(u8, body, "reasoning.encrypted") != null);
    try std.testing.expect(std.mem.find(u8, body, "call_abc123") != null);
}

test "streamSimpleOpenAICompletions exits early when pre-cancelled" {
    const allocator = std.testing.allocator;
    const model = ai_types.Model{
        .id = "gpt-4o-mini",
        .name = "GPT-4o Mini",
        .api = "openai-completions",
        .provider = "openai",
        .base_url = "https://api.openai.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
    };
    const context = ai_types.Context{
        .messages = &[_]ai_types.Message{},
    };

    var cancelled = std.atomic.Value(bool).init(true);
    const cancel_token = ai_types.CancelToken{ .cancelled = &cancelled };

    const stream = try streamSimpleOpenAICompletions(model, context, .{
        .api_key = "test-key",
        .cancel_token = cancel_token,
    }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |ev| {
        var mutable_ev = ev;
        ai_types.deinitAssistantMessageEvent(allocator, &mutable_ev);
    }

    try std.testing.expect(stream.getError() != null);
    try std.testing.expectEqualStrings("request cancelled", stream.getError().?);
}

test "this wire finds each row's own key, which a vendor list would not" {
    const rows = [_][]const u8{ "openai", "deepseek", "kimi", "openrouter", "opencode-zen", "vercel", "zenmux", "deepinfra" };
    for (rows) |id| {
        const names = provider_catalog.credentialEnv(id);
        try std.testing.expect(names.len > 0);
        for (names) |name| {
            try compat_mod.setTestEnv(std.testing.allocator, name, "row-key");
            defer compat_mod.clearTestEnv();
            const found = provider_catalog.apiKeyFromEnv(std.testing.allocator, id) orelse {
                std.debug.print("\n{s} records {s} but a request on this wire finds no key\n", .{ id, name });
                return error.TestRowKeyNotFound;
            };
            defer std.testing.allocator.free(found);
            try std.testing.expectEqualStrings("row-key", found);
        }
    }
}

test "the openai completions request url drops a trailing slash and keeps a suffix already present" {
    const cases = [_]struct { base: []const u8, fact: bool, want: []const u8 }{
        .{ .base = "https://api.openai.com", .fact = false, .want = "https://api.openai.com/v1/chat/completions" },
        .{ .base = "https://api.openai.com/", .fact = false, .want = "https://api.openai.com/v1/chat/completions" },
        .{ .base = "https://api.openai.com///", .fact = false, .want = "https://api.openai.com/v1/chat/completions" },
        .{ .base = "https://api.openai.com/v1/chat/completions", .fact = false, .want = "https://api.openai.com/v1/chat/completions" },
        .{ .base = "https://api.openai.com/v1/chat/completions/", .fact = false, .want = "https://api.openai.com/v1/chat/completions" },
        .{ .base = "https://api.groq.com/openai/v1/", .fact = true, .want = "https://api.groq.com/openai/v1/chat/completions" },
    };
    for (cases) |case| {
        const url = try provider_catalog.joinUrlOwned(std.testing.allocator, case.base, request_wire, case.fact);
        defer std.testing.allocator.free(url);
        try std.testing.expectEqualStrings(case.want, url);
    }
}

test "the copilot request url drops a trailing slash and never doubles the version segment" {
    const cases = [_]struct { base: []const u8, want: []const u8 }{
        .{ .base = "https://api.githubcopilot.com", .want = "https://api.githubcopilot.com/chat/completions" },
        .{ .base = "https://api.githubcopilot.com/", .want = "https://api.githubcopilot.com/chat/completions" },
        .{ .base = "https://api.githubcopilot.com/chat/completions", .want = "https://api.githubcopilot.com/chat/completions" },
        .{ .base = "https://gw.test/v1", .want = "https://gw.test/v1/chat/completions" },
    };
    for (cases) |case| {
        const url = try provider_catalog.joinUrlOwned(std.testing.allocator, case.base, copilot_wire, false);
        defer std.testing.allocator.free(url);
        try std.testing.expectEqualStrings(case.want, url);
    }
}

test "the openai completions request url drops the wire's version only when the fact says the base has one" {
    const cases = [_]struct { base: []const u8, suffix: []const u8, fact: bool, want: []const u8 }{
        .{ .base = "https://api.openai.com", .suffix = "/v1/chat/completions", .fact = false, .want = "https://api.openai.com/v1/chat/completions" },
        .{ .base = "https://api.groq.com/openai/v1", .suffix = "/v1/chat/completions", .fact = true, .want = "https://api.groq.com/openai/v1/chat/completions" },
        .{ .base = "http://localhost:8000/v1/", .suffix = "/v1/chat/completions", .fact = true, .want = "http://localhost:8000/v1/chat/completions" },
        .{ .base = "https://api.githubcopilot.com", .suffix = "/chat/completions", .fact = false, .want = "https://api.githubcopilot.com/chat/completions" },
        .{ .base = "https://gw.test/v1", .suffix = "/chat/completions", .fact = false, .want = "https://gw.test/v1/chat/completions" },
        .{ .base = "https://gw.test/v1/chat/completions", .suffix = "/v1/chat/completions", .fact = false, .want = "https://gw.test/v1/chat/completions" },
        .{ .base = "https://api.z.ai/api/coding/paas/v4", .suffix = "/v1/chat/completions", .fact = true, .want = "https://api.z.ai/api/coding/paas/v4/chat/completions" },
        .{ .base = "https://api.lkeap.cloud.tencent.com/coding/v3", .suffix = "/v1/chat/completions", .fact = true, .want = "https://api.lkeap.cloud.tencent.com/coding/v3/chat/completions" },
        .{ .base = "https://api.deepinfra.com/v1/openai", .suffix = "/v1/chat/completions", .fact = true, .want = "https://api.deepinfra.com/v1/openai/chat/completions" },
        .{ .base = "https://gw.test/v2", .suffix = "/v1/chat/completions", .fact = true, .want = "https://gw.test/v2/chat/completions" },
        .{ .base = "https://gw.test/vercel", .suffix = "/v1/chat/completions", .fact = false, .want = "https://gw.test/vercel/v1/chat/completions" },
        .{ .base = "https://api.z.ai/api/coding/paas/v4", .suffix = "/v1/chat/completions", .fact = false, .want = "https://api.z.ai/api/coding/paas/v4/v1/chat/completions" },
    };
    for (cases) |case| {
        const url = try provider_catalog.joinUrlOwned(std.testing.allocator, case.base, .{ .id = "openai-completions", .suffix = case.suffix }, case.fact);
        defer std.testing.allocator.free(url);
        try std.testing.expectEqualStrings(case.want, url);
    }
}

const MockCompletionsServer = struct {
    server: compat_mod.net.Server,
    body: []const u8,
    thread: ?std.Thread = null,
    served: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    saw_chat_path: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    saw_stream_flag: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    saw_session_header: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    saw_client_agent: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    response_headers: []const u8 = "",

    const trace_events =
        \\data: {"id":"chatcmpl-1","object":"chat.completion.chunk","created":1,"model":"regression-model","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}
        \\
        \\data: {"choices":[{"index":0,"delta":{"reasoning_content":"plan"},"finish_reason":null}]}
        \\
        \\data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_trace01","type":"function","function":{"name":"bash","arguments":""}}]},"finish_reason":null}]}
        \\
        \\data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}
        \\
        \\data: {"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":3,"completion_tokens":9,"total_tokens":12}}
        \\
        \\data: [DONE]
        \\
    ;

    const complete_stream = trace_events ++ "\n\n";

    fn listen(body: []const u8) !MockCompletionsServer {
        const address = try compat_mod.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
        return .{
            .server = try compat_mod.net.tcpListen(address, .{ .reuse_address = true }),
            .body = body,
        };
    }

    fn baseUrl(self: *const MockCompletionsServer, allocator: std.mem.Allocator) ![]u8 {
        return std.fmt.allocPrint(allocator, "http://127.0.0.1:{d}", .{compat_mod.net.listenAddress(&self.server).getPort()});
    }

    fn start(self: *MockCompletionsServer) !void {
        self.thread = try std.Thread.spawn(.{}, serve, .{self});
    }

    fn stop(self: *MockCompletionsServer) void {
        if (self.thread) |thread| {
            if (!self.served.load(.acquire)) {
                if (compat_mod.net.tcpConnect(compat_mod.net.listenAddress(&self.server))) |opened| {
                    var kick = opened;
                    kick.close();
                } else |_| {}
            }
            thread.join();
            self.thread = null;
        }
        compat_mod.net.closeServer(&self.server);
    }

    fn contentLength(head: []const u8) ?usize {
        var lines = std.mem.splitSequence(u8, head, "\r\n");
        while (lines.next()) |line| {
            if (std.ascii.startsWithIgnoreCase(line, "content-length:")) {
                return std.fmt.parseInt(usize, std.mem.trim(u8, line["content-length:".len..], " \t"), 10) catch null;
            }
        }
        return null;
    }

    fn readHead(stream: *compat_mod.net.Stream, buffer: []u8) !usize {
        var filled: usize = 0;
        while (filled < buffer.len) {
            const read = try stream.read(buffer[filled .. filled + 1]);
            if (read == 0) return error.EndOfStream;
            filled += read;
            if (filled >= 4 and std.mem.eql(u8, buffer[filled - 4 .. filled], "\r\n\r\n")) return filled - 4;
        }
        return error.StreamTooLong;
    }

    fn headerValue(head: []const u8, name: []const u8) ?[]const u8 {
        var lines = std.mem.splitSequence(u8, head, "\r\n");
        while (lines.next()) |line| {
            const colon = std.mem.indexOfScalar(u8, line, ':') orelse continue;
            if (!std.ascii.eqlIgnoreCase(line[0..colon], name)) continue;
            return std.mem.trim(u8, line[colon + 1 ..], " ");
        }
        return null;
    }

    fn serve(self: *MockCompletionsServer) void {
        defer self.served.store(true, .release);

        var conn = compat_mod.net.accept(&self.server) catch return;
        defer conn.stream.close();

        var request: [16384]u8 = undefined;
        const head_len = readHead(&conn.stream, &request) catch return;
        const head = request[0..head_len];

        var body: [16384]u8 = undefined;
        if (contentLength(head)) |length| {
            if (length > 0 and length <= body.len) {
                _ = conn.stream.read(body[0..length]) catch return;
            }
        }
        const body_slice = body[0..@min(contentLength(head) orelse 0, body.len)];

        if (std.mem.indexOf(u8, head, "/chat/completions ") != null) {
            self.saw_chat_path.store(true, .release);
        }
        if (std.mem.indexOf(u8, body_slice, "\"stream\":true") != null) {
            self.saw_stream_flag.store(true, .release);
        }
        if (std.mem.eql(u8, headerValue(head, "x-opencode-session") orelse "", "ses-tui-1")) {
            self.saw_session_header.store(true, .release);
        }
        if (std.mem.eql(u8, headerValue(head, "user-agent") orelse "", client_user_agent)) {
            self.saw_client_agent.store(true, .release);
        }

        var head_buffer: [512]u8 = undefined;
        const response_head = std.fmt.bufPrint(
            &head_buffer,
            "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nContent-Length: {d}\r\n{s}Connection: close\r\n\r\n",
            .{ self.body.len, self.response_headers },
        ) catch return;

        conn.stream.writeAll(response_head) catch return;
        conn.stream.writeAll(self.body) catch return;
    }
};

fn traceModel(base_url: []const u8) ai_types.Model {
    return .{
        .id = "regression-model",
        .name = "regression-model",
        .api = "openai-completions",
        .provider = "gateway",
        .base_url = base_url,
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 16,
    };
}

fn traceContext() ai_types.Context {
    const messages = struct {
        const items = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "hello" }, .timestamp = 0 } }};
    }.items[0..];
    return .{ .messages = messages };
}

fn streamErrorFor(body: []const u8) !?[]u8 {
    return streamErrorWithHeaders(body, "");
}

fn streamErrorWithHeaders(body: []const u8, response_headers: []const u8) !?[]u8 {
    const allocator = std.testing.allocator;
    var mock = try MockCompletionsServer.listen(body);
    mock.response_headers = response_headers;
    var stopped = false;
    defer if (!stopped) mock.stop();
    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);
    try mock.start();

    const stream = try streamOpenAICompletions(
        traceModel(base_url),
        traceContext(),
        .{ .api_key = ai_types.OwnedSlice(u8).initBorrowed("test-key") },
        allocator,
    );
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var polled = event;
        ai_types.deinitAssistantMessageEvent(allocator, &polled);
    }
    try std.testing.expect(stream.waitForThread(5_000));
    mock.stop();
    stopped = true;
    const reported = stream.getError() orelse return null;
    return try allocator.dupe(u8, reported);
}

test "a stream that ends with no reply fails instead of settling as a finished turn" {
    const cases = [_]struct { body: []const u8, want: []const u8 }{
        .{
            .body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n",
            .want = "the stream ended before the model replied",
        },
        .{
            .body = "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
            .want = "the model returned an empty reply",
        },
        .{
            .body = "data: {\"error\":{\"message\":\"upstream overloaded\",\"code\":529}}\n\ndata: [DONE]\n\n",
            .want = "provider stream error: upstream overloaded",
        },
    };
    for (cases) |case| {
        const reported = try streamErrorFor(case.body) orelse return error.TestExpectedStreamError;
        defer std.testing.allocator.free(reported);
        try std.testing.expect(std.mem.startsWith(u8, reported, case.want));
    }
    try std.testing.expect(try streamErrorFor(MockCompletionsServer.complete_stream) == null);
}

test "a delta carrying a null or empty tool_calls beside its content still delivers the content" {
    const body =
        "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"reasoning_content\":\"plan\",\"tool_calls\":null},\"finish_reason\":null}]}\n\n" ++
        "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\",\"tool_calls\":[]},\"finish_reason\":null}]}\n\n" ++
        "data: {\"choices\":[{\"index\":0,\"delta\":{\"tool_calls\":null},\"finish_reason\":\"stop\"}]}\n\n" ++
        "data: [DONE]\n\n";
    try std.testing.expect(try streamErrorFor(body) == null);
}

test "an empty reply names its finish reason, usage, chunk count, id, the gateway's request headers and the last chunk" {
    const body =
        "data: {\"id\":\"chatcmpl-empty\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n" ++
        "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":612034,\"prompt_tokens_details\":{\"cached_tokens\":600000},\"completion_tokens\":0}}\n\n" ++
        "data: [DONE]\n\n";
    const reported = try streamErrorWithHeaders(body, "x-opencode-endpoint-id: orcarouter-dsv4.1flash\r\nx-opencode-log-id: log-42\r\n") orelse return error.TestExpectedStreamError;
    defer std.testing.allocator.free(reported);
    try std.testing.expectEqualStrings(
        "the model returned an empty reply (finish_reason: stop; usage: 612034 input (600000 cached), 0 output; 3 chunks; id: chatcmpl-empty; x-opencode-endpoint-id: orcarouter-dsv4.1flash; x-opencode-log-id: log-42; last chunk: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":612034,\"prompt_tokens_details\":{\"cached_tokens\":600000},\"completion_tokens\":0}})",
        reported,
    );
}

test "a stream cut before any reply says it saw no finish reason and no usage" {
    const reported = try streamErrorFor("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n") orelse return error.TestExpectedStreamError;
    defer std.testing.allocator.free(reported);
    try std.testing.expectEqualStrings(
        "the stream ended before the model replied (no finish_reason; no usage reported; 1 chunk; last chunk: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]})",
        reported,
    );
}

test "a reply trace keeps a long chunk's head and marks it cut" {
    var trace: ReplyTrace = .{};
    const long = "{\"choices\":[]," ++ ("\"x\":1," ** 100) ++ "\"y\":2}";
    trace.observe(long);
    trace.observe("[DONE]");
    try std.testing.expectEqual(@as(usize, 2), trace.chunks);
    try std.testing.expectEqual(@as(usize, reply_trace_bytes), trace.last.len);
    try std.testing.expect(trace.last.cut);
    const message = try trace.describe(std.testing.allocator, "empty", .{});
    defer std.testing.allocator.free(message);
    try std.testing.expect(std.mem.endsWith(u8, message, "…)"));
}

fn replyTraceDescribeProbe(allocator: std.mem.Allocator) !void {
    var trace: ReplyTrace = .{};
    trace.observe("{\"id\":\"chatcmpl-a\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}]}");
    trace.observe("{\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}");
    const message = try trace.describe(allocator, "empty", .{ .input = 5, .output = 1 });
    allocator.free(message);
}

test "a reply trace names the finish reason a middle chunk carried" {
    var trace: ReplyTrace = .{};
    trace.observe("{\"id\":\"chatcmpl-m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":null}]}");
    trace.observe("{\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}");
    trace.noteFinish("{\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}");
    trace.observe("{\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1}}");
    trace.observe("[DONE]");
    trace.noteFinish("[DONE]");
    const message = try trace.describe(std.testing.allocator, "empty", .{ .input = 5, .output = 1 });
    defer std.testing.allocator.free(message);
    try std.testing.expect(std.mem.startsWith(u8, message, "empty (finish_reason: stop;"));
}

test "a reply trace's description frees what it built on every allocation failure" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, replyTraceDescribeProbe, .{});
}

test "an OpenCode Go request names this client and carries the session id" {
    const allocator = std.testing.allocator;

    var mock = try MockCompletionsServer.listen(MockCompletionsServer.complete_stream);
    var stopped = false;
    defer if (!stopped) mock.stop();

    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);
    try mock.start();

    var model = traceModel(base_url);
    model.provider = "opencode-go";
    const stream = try streamOpenAICompletions(
        model,
        traceContext(),
        .{
            .api_key = ai_types.OwnedSlice(u8).initBorrowed("test-key"),
            .session_id = ai_types.OwnedSlice(u8).initBorrowed("ses-tui-1"),
        },
        allocator,
    );
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }
    while (stream.wait()) |event| {
        var polled = event;
        ai_types.deinitAssistantMessageEvent(allocator, &polled);
    }
    try std.testing.expect(stream.waitForThread(5_000));
    mock.stop();
    stopped = true;

    try std.testing.expect(mock.saw_session_header.load(.acquire));
    try std.testing.expect(mock.saw_client_agent.load(.acquire));
}

test "a streamed text thinking and tool call reports indices that diverge from the terminal assembly order" {
    const allocator = std.testing.allocator;

    var mock = try MockCompletionsServer.listen(MockCompletionsServer.complete_stream);
    var stopped = false;
    defer if (!stopped) mock.stop();

    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);

    try mock.start();

    const stream = try streamOpenAICompletions(
        traceModel(base_url),
        traceContext(),
        .{ .api_key = ai_types.OwnedSlice(u8).initBorrowed("test-key") },
        allocator,
    );
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    var text_index: ?usize = null;
    var thinking_index: ?usize = null;
    var tool_start_index: ?usize = null;
    var tool_end_index: ?usize = null;

    while (stream.wait()) |event| {
        var polled = event;
        defer ai_types.deinitAssistantMessageEvent(allocator, &polled);
        switch (polled) {
            .text_delta => |td| {
                try std.testing.expectEqualStrings("hi", td.delta);
                text_index = td.content_index;
            },
            .thinking_delta => |td| {
                try std.testing.expectEqualStrings("plan", td.delta);
                thinking_index = td.content_index;
            },
            .toolcall_start => |ts| {
                try std.testing.expectEqualStrings("call_trace01", ts.id);
                try std.testing.expectEqualStrings("bash", ts.name);
                tool_start_index = ts.content_index;
            },
            .toolcall_delta => |td| {
                try std.testing.expectEqualStrings("{\"command\":\"ls\"}", td.delta);
            },
            .toolcall_end => |te| {
                try std.testing.expectEqualStrings("call_trace01", te.tool_call.id);
                tool_end_index = te.content_index;
            },
            else => {},
        }
    }

    try std.testing.expect(stream.waitForThread(5_000));
    mock.stop();
    stopped = true;

    try std.testing.expect(stream.waitForThread(5_000));

    try std.testing.expect(stream.getError() == null);
    try std.testing.expect(mock.saw_chat_path.load(.acquire));
    try std.testing.expect(mock.saw_stream_flag.load(.acquire));

    try std.testing.expectEqual(@as(usize, 0), text_index.?);
    try std.testing.expectEqual(@as(usize, 0), thinking_index.?);
    try std.testing.expectEqual(@as(usize, 0), tool_start_index.?);
    try std.testing.expectEqual(@as(usize, 2), tool_end_index.?);

    const result = stream.getResult() orelse return error.TestUnexpectedResult;
    try std.testing.expectEqual(@as(usize, 3), result.content.len);
    try std.testing.expectEqualStrings("plan", result.content[0].thinking.thinking);
    try std.testing.expectEqualStrings("hi", result.content[1].text.text);
    try std.testing.expectEqualStrings("call_trace01", result.content[2].tool_call.id);
    try std.testing.expectEqualStrings("bash", result.content[2].tool_call.name);
    try std.testing.expectEqualStrings("{\"command\":\"ls\"}", result.content[2].tool_call.arguments_json);
}

var cleanup_hold: std.atomic.Value(usize) = std.atomic.Value(usize).init(0);
var cleanup_held: std.atomic.Value(usize) = std.atomic.Value(usize).init(0);
var cleanup_gate: std.atomic.Value(u32) = std.atomic.Value(u32).init(0);
var cleanup_window: std.atomic.Value(u32) = std.atomic.Value(u32).init(0);
var cleanup_paused: std.atomic.Value(bool) = std.atomic.Value(bool).init(false);
var cleanup_waits: std.atomic.Value(usize) = std.atomic.Value(usize).init(0);
var cleanup_paused_gate: std.atomic.Value(u32) = std.atomic.Value(u32).init(0);

fn defaultIo() std.Io {
    return if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
}

fn awaitCleanupRelease() void {
    if (!@import("builtin").is_test) return;
    if (cleanup_hold.load(.acquire) == 0) return;
    _ = cleanup_held.fetchAdd(1, .release);
    const io = defaultIo();
    while (true) {
        const seen = cleanup_gate.load(.acquire);
        if (cleanup_window.load(.acquire) == 1) awaitCleanupWindow(io);
        if (cleanup_hold.load(.acquire) == 0) break;
        _ = cleanup_waits.fetchAdd(1, .release);
        io.futexWaitTimeout(u32, &cleanup_gate.raw, seen, boundedWait()) catch {};
    }
}

fn awaitCleanupWindow(io: std.Io) void {
    _ = cleanup_paused_gate.fetchAdd(1, .release);
    _ = cleanup_paused.store(true, .release);
    defaultIo().futexWake(u32, &cleanup_paused_gate.raw, std.math.maxInt(u32));
    while (cleanup_window.load(.acquire) == 1) {
        io.futexWaitTimeout(u32, &cleanup_window.raw, cleanup_window.load(.acquire), boundedWait()) catch {};
    }
}

fn expectCleanupPaused() !void {
    const io = defaultIo();
    var rounds: usize = 0;
    while (!cleanup_paused.load(.acquire) and rounds < 200) : (rounds += 1) {
        io.futexWaitTimeout(u32, &cleanup_paused_gate.raw, cleanup_paused_gate.load(.acquire), boundedWait()) catch {};
    }
    if (!cleanup_paused.load(.acquire)) {
        std.debug.print("producer never reached the held publish: held={d} paused={any} window={d} waits={d}\n", .{
            cleanup_held.load(.acquire),   cleanup_paused.load(.acquire),
            cleanup_window.load(.acquire), cleanup_waits.load(.acquire),
        });
        return error.TestUnexpectedResult;
    }
}

fn boundedWait() std.Io.Timeout {
    return .{ .duration = .{
        .raw = .fromMilliseconds(25),
        .clock = .boot,
    } };
}

fn wakeCleanupWaiters() void {
    defaultIo().futexWake(u32, &cleanup_gate.raw, std.math.maxInt(u32));
}

fn holdCleanup() void {
    _ = cleanup_hold.store(1, .release);
    _ = cleanup_gate.fetchAdd(1, .release);
    defaultIo().futexWake(u32, &cleanup_gate.raw, std.math.maxInt(u32));
}

fn releaseCleanupGate() void {
    _ = cleanup_hold.store(0, .release);
    _ = cleanup_gate.fetchAdd(1, .release);
    wakeCleanupWaiters();
}

fn cleanupGateGeneration() u32 {
    return cleanup_gate.load(.acquire);
}

test "a release inside the snapshot-to-wait window still reaches the producer" {
    const allocator = std.testing.allocator;
    cleanup_held.store(0, .release);
    cleanup_paused.store(false, .release);
    cleanup_waits.store(0, .release);
    holdCleanup();
    defer {
        cleanup_window.store(0, .release);
        releaseCleanupGate();
    }
    cleanup_window.store(1, .release);

    var mock = try MockCompletionsServer.listen(MockCompletionsServer.complete_stream);
    defer mock.stop();
    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);
    try mock.start();

    const stream = try streamOpenAICompletions(
        traceModel(base_url),
        traceContext(),
        .{ .api_key = ai_types.OwnedSlice(u8).initBorrowed("cleanup-window-key") },
        allocator,
    );
    defer {
        cleanup_window.store(0, .release);
        releaseCleanupGate();
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var polled = event;
        defer ai_types.deinitAssistantMessageEvent(allocator, &polled);
    }

    try expectCleanupPaused();
    try std.testing.expect(!stream.waitForThread(100));

    const waits_before_release = cleanup_waits.load(.acquire);
    releaseCleanupGate();
    cleanup_window.store(0, .release);
    wakeCleanupWaiters();

    try std.testing.expect(stream.waitForThread(5_000));
    try std.testing.expectEqual(waits_before_release, cleanup_waits.load(.acquire));
    try std.testing.expect(stream.getError() == null);
}

test "a wake that carries no release does not let the producer publish done" {
    const allocator = std.testing.allocator;
    cleanup_held.store(0, .release);
    holdCleanup();
    defer releaseCleanupGate();

    var mock = try MockCompletionsServer.listen(MockCompletionsServer.complete_stream);
    defer mock.stop();
    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);
    try mock.start();

    const stream = try streamOpenAICompletions(
        traceModel(base_url),
        traceContext(),
        .{ .api_key = ai_types.OwnedSlice(u8).initBorrowed("cleanup-wake-key") },
        allocator,
    );
    defer {
        releaseCleanupGate();
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var polled = event;
        defer ai_types.deinitAssistantMessageEvent(allocator, &polled);
    }

    try std.testing.expect(!stream.waitForThread(250));
    const held = cleanup_held.load(.acquire);
    const generation = cleanupGateGeneration();

    wakeCleanupWaiters();
    wakeCleanupWaiters();
    try std.testing.expect(!stream.waitForThread(250));

    try std.testing.expectEqual(@as(usize, 1), held);
    try std.testing.expectEqual(generation, cleanupGateGeneration());

    releaseCleanupGate();
    try std.testing.expect(stream.waitForThread(5_000));
    try std.testing.expect(stream.getError() == null);
}

test "a release that lands before the producer waits is still observed" {
    const allocator = std.testing.allocator;
    cleanup_held.store(0, .release);
    holdCleanup();
    defer releaseCleanupGate();

    const generation = cleanupGateGeneration();
    releaseCleanupGate();

    var mock = try MockCompletionsServer.listen(MockCompletionsServer.complete_stream);
    defer mock.stop();
    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);
    try mock.start();

    const stream = try streamOpenAICompletions(
        traceModel(base_url),
        traceContext(),
        .{ .api_key = ai_types.OwnedSlice(u8).initBorrowed("cleanup-early-key") },
        allocator,
    );
    defer {
        releaseCleanupGate();
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var polled = event;
        defer ai_types.deinitAssistantMessageEvent(allocator, &polled);
    }

    try std.testing.expect(cleanupGateGeneration() != generation);
    try std.testing.expectEqual(@as(usize, 0), cleanup_held.load(.acquire));
    try std.testing.expect(stream.waitForThread(5_000));
    try std.testing.expect(stream.getError() == null);
}

test "the producer does not publish done while its own cleanup is unfinished" {
    const allocator = std.testing.allocator;
    cleanup_held.store(0, .release);
    holdCleanup();
    defer releaseCleanupGate();

    var mock = try MockCompletionsServer.listen(MockCompletionsServer.complete_stream);
    defer mock.stop();
    const base_url = try mock.baseUrl(allocator);
    defer allocator.free(base_url);
    try mock.start();

    const stream = try streamOpenAICompletions(
        traceModel(base_url),
        traceContext(),
        .{ .api_key = ai_types.OwnedSlice(u8).initBorrowed("cleanup-order-key") },
        allocator,
    );
    defer {
        releaseCleanupGate();
        stream.deinit();
        allocator.destroy(stream);
    }

    while (stream.wait()) |event| {
        var polled = event;
        defer ai_types.deinitAssistantMessageEvent(allocator, &polled);
    }

    try std.testing.expect(!stream.waitForThread(250));
    try std.testing.expectEqual(@as(usize, 1), cleanup_held.load(.acquire));

    releaseCleanupGate();
    try std.testing.expect(stream.waitForThread(5_000));
    try std.testing.expect(stream.getError() == null);
    const result = stream.getResult() orelse return error.TestUnexpectedResult;
    try std.testing.expect(result.content.len > 0);
}

test "the catalogued Z.AI coding plan row detects as Z.AI, so its thinking format cannot drift back to openai" {
    const base = provider_catalog.defaultBaseUrl("zai-coding-plan").?;
    try std.testing.expect(provider_caps.isZai(base));
    try std.testing.expectEqual(.zai, provider_caps.detectCapabilities(base).thinking_format);
}
