const std = @import("std");
const owned_slice_mod = @import("owned_slice");

pub const OwnedSlice = owned_slice_mod.OwnedSlice;

pub const KnownApi = enum {
    openai_completions,
    openai_responses,
    azure_openai_responses,
    openai_codex_responses,
    anthropic_messages,
    google_generative_ai,
    google_gemini_cli,
    ollama,
};

pub const ThinkingLevel = enum { off, minimal, low, medium, high, xhigh, max };

pub const ServiceTier = enum {
    default,
    flex,
    priority,
};

pub const ReasoningSummary = enum {
    auto,
    concise,
    detailed,
};

pub const ThinkingBudgets = struct {
    minimal: ?u32 = null,
    low: ?u32 = null,
    medium: ?u32 = null,
    high: ?u32 = null,
    xhigh: ?u32 = null,
    max: ?u32 = null,
};

pub const CacheRetention = enum { none, short, long };

pub const StopReason = enum {
    stop,
    length,
    tool_use,
    content_filter,
    @"error",
    aborted,
};

pub const HeaderPair = struct {
    name: []const u8,
    value: []const u8,

    pub fn deinit(self: *HeaderPair, allocator: std.mem.Allocator) void {
        allocator.free(self.name);
        allocator.free(self.value);
    }
};

pub const RetryConfig = struct {
    max_retry_delay_ms: ?u32 = 60_000,
};

pub const CancelToken = struct {
    cancelled: *std.atomic.Value(bool),

    pub fn isCancelled(self: CancelToken) bool {
        return self.cancelled.load(.acquire);
    }
};

pub const Metadata = struct {
    user_id: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),

    pub fn getUserId(self: *const Metadata) ?[]const u8 {
        const uid = self.user_id.slice();
        return if (uid.len > 0) uid else null;
    }

    pub fn deinit(self: *Metadata, allocator: std.mem.Allocator) void {
        self.user_id.deinit(allocator);
    }
};

pub const ToolChoice = union(enum) {
    auto: void,
    none: void,
    required: void,
    function: []const u8,
};

pub const StreamOptions = struct {
    temperature: ?f32 = null,
    max_tokens: ?u32 = null,
    api_key: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    cache_retention: ?CacheRetention = null,
    session_id: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    headers: ?[]const HeaderPair = null,
    retry: RetryConfig = .{},
    cancel_token: ?CancelToken = null,
    on_payload_fn: ?*const fn (ctx: ?*anyopaque, payload_json: []const u8) void = null,
    on_payload_ctx: ?*anyopaque = null,
    thinking_enabled: bool = false,
    thinking_budget_tokens: ?u32 = null,
    thinking_effort: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    reasoning_effort: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    reasoning_summary: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    include_reasoning_encrypted: bool = false,
    reasoning_enabled: bool = true,
    service_tier: ?ServiceTier = null,
    metadata: ?Metadata = null,
    tool_choice: ?ToolChoice = null,
    owned_tool_choice_function: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    http_timeout_ms: ?u64 = 30_000,
    ping_interval_ms: ?u64 = null,
    owned_headers: ?OwnedSlice(HeaderPair) = null,

    pub fn getApiKey(self: *const StreamOptions) ?[]const u8 {
        const key = self.api_key.slice();
        return if (key.len > 0) key else null;
    }

    pub fn getSessionId(self: *const StreamOptions) ?[]const u8 {
        const sid = self.session_id.slice();
        return if (sid.len > 0) sid else null;
    }

    pub fn getThinkingEffort(self: *const StreamOptions) ?[]const u8 {
        const effort = self.thinking_effort.slice();
        return if (effort.len > 0) effort else null;
    }

    pub fn getReasoningEffort(self: *const StreamOptions) ?[]const u8 {
        const effort = self.reasoning_effort.slice();
        return if (effort.len > 0) effort else null;
    }

    pub fn getReasoningSummary(self: *const StreamOptions) ?[]const u8 {
        const summary = self.reasoning_summary.slice();
        return if (summary.len > 0) summary else null;
    }

    pub fn deinit(self: *StreamOptions, allocator: std.mem.Allocator) void {
        self.api_key.deinit(allocator);
        self.session_id.deinit(allocator);
        self.thinking_effort.deinit(allocator);
        self.reasoning_effort.deinit(allocator);
        self.reasoning_summary.deinit(allocator);
        if (self.metadata) |*meta| {
            meta.deinit(allocator);
        }

        self.owned_tool_choice_function.deinit(allocator);
        if (self.owned_headers) |*headers| {
            headers.deinit(allocator);
        }
    }
};

pub const SimpleStreamOptions = struct {
    temperature: ?f32 = null,
    max_tokens: ?u32 = null,
    api_key: ?[]const u8 = null,
    cache_retention: ?CacheRetention = null,
    session_id: ?[]const u8 = null,
    headers: ?[]const HeaderPair = null,
    retry: RetryConfig = .{},
    cancel_token: ?CancelToken = null,
    on_payload_fn: ?*const fn (ctx: ?*anyopaque, payload_json: []const u8) void = null,
    on_payload_ctx: ?*anyopaque = null,
    reasoning: ?ThinkingLevel = null,
    thinking_budgets: ?ThinkingBudgets = null,
    reasoning_summary: ?[]const u8 = null,
    http_timeout_ms: ?u64 = 30_000,
};

pub const TextContent = struct {
    text: []const u8,
    text_signature: ?[]const u8 = null,
};

pub const ThinkingContent = struct {
    thinking: []const u8,
    thinking_signature: ?[]const u8 = null,
};

pub const ImageContent = struct {
    data: []const u8,
    mime_type: []const u8,
};

pub const ToolCall = struct {
    id: []const u8,
    name: []const u8,
    arguments_json: []const u8,
    thought_signature: ?[]const u8 = null,
};

pub const AssistantContent = union(enum) {
    text: TextContent,
    thinking: ThinkingContent,
    tool_call: ToolCall,
    image: ImageContent,
};

pub const UserContentPart = union(enum) {
    text: TextContent,
    image: ImageContent,

    pub fn deinit(self: *UserContentPart, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .text => |*t| {
                allocator.free(t.text);
                if (t.text_signature) |s| allocator.free(s);
            },
            .image => |*img| {
                allocator.free(img.data);
                allocator.free(img.mime_type);
            },
        }
    }
};

pub const UserContent = union(enum) {
    text: []const u8,
    parts: []const UserContentPart,

    pub fn deinit(self: *UserContent, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .text => |t| allocator.free(t),
            .parts => |parts| {
                const mut_parts: []UserContentPart = @constCast(parts);
                for (mut_parts) |*part| {
                    part.deinit(allocator);
                }
                allocator.free(parts);
            },
        }
    }
};

pub const UsageCost = struct {
    input: f64 = 0,
    output: f64 = 0,
    cache_read: f64 = 0,
    cache_write: f64 = 0,
    total: f64 = 0,
};

pub const Usage = struct {
    input: u64 = 0,
    output: u64 = 0,
    cache_read: u64 = 0,
    cache_write: u64 = 0,
    total_tokens: u64 = 0,
    cost: UsageCost = .{},

    pub fn calculateCost(self: *Usage, model_cost: Cost) void {
        const rates = model_cost.ratesFor(self.input + self.cache_read + self.cache_write);
        self.cost.input = (@as(f64, @floatFromInt(self.input)) / 1_000_000.0) * rates.input;
        self.cost.output = (@as(f64, @floatFromInt(self.output)) / 1_000_000.0) * rates.output;
        self.cost.cache_read = (@as(f64, @floatFromInt(self.cache_read)) / 1_000_000.0) * rates.cache_read;
        self.cost.cache_write = (@as(f64, @floatFromInt(self.cache_write)) / 1_000_000.0) * rates.cache_write;
        self.cost.total = self.cost.input + self.cost.output + self.cost.cache_read + self.cost.cache_write;
    }
};

pub const UserMessage = struct {
    content: UserContent,
    timestamp: i64,

    pub fn deinit(self: *UserMessage, allocator: std.mem.Allocator) void {
        self.content.deinit(allocator);
    }
};

pub const ArtifactReference = struct {
    artifact_id: []const u8,
    uri: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    mime_type: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    byte_size: ?u64 = null,
    sha256: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    description: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),

    pub fn getUri(self: *const ArtifactReference) ?[]const u8 {
        const value = self.uri.slice();
        return if (value.len > 0) value else null;
    }

    pub fn getMimeType(self: *const ArtifactReference) ?[]const u8 {
        const value = self.mime_type.slice();
        return if (value.len > 0) value else null;
    }

    pub fn getSha256(self: *const ArtifactReference) ?[]const u8 {
        const value = self.sha256.slice();
        return if (value.len > 0) value else null;
    }

    pub fn getDescription(self: *const ArtifactReference) ?[]const u8 {
        const value = self.description.slice();
        return if (value.len > 0) value else null;
    }

    pub fn deinit(self: *ArtifactReference, allocator: std.mem.Allocator) void {
        allocator.free(self.artifact_id);
        self.uri.deinit(allocator);
        self.mime_type.deinit(allocator);
        self.sha256.deinit(allocator);
        self.description.deinit(allocator);
    }
};

pub const AssistantMessage = struct {
    content: []const AssistantContent,
    api: []const u8,
    provider: []const u8,
    model: []const u8,
    usage: Usage,
    stop_reason: StopReason,
    error_message: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    timestamp: i64,
    is_owned: bool = false,

    pub fn getErrorMessage(self: *const AssistantMessage) ?[]const u8 {
        const err = self.error_message.slice();
        return if (err.len > 0) err else null;
    }

    pub fn deinit(self: *AssistantMessage, allocator: std.mem.Allocator) void {
        for (self.content) |block| {
            switch (block) {
                .text => |t| {
                    if (t.text.len > 0) allocator.free(t.text);
                    if (t.text_signature) |s| allocator.free(s);
                },
                .thinking => |t| {
                    if (t.thinking.len > 0) allocator.free(t.thinking);
                    if (t.thinking_signature) |s| allocator.free(s);
                },
                .tool_call => |tc| {
                    allocator.free(tc.id);
                    allocator.free(tc.name);
                    if (tc.arguments_json.len > 0) allocator.free(tc.arguments_json);
                    if (tc.thought_signature) |s| allocator.free(s);
                },
                .image => |img| {
                    allocator.free(img.data);
                    allocator.free(img.mime_type);
                },
            }
        }
        allocator.free(self.content);
        if (self.is_owned) {
            allocator.free(self.api);
            allocator.free(self.provider);
            allocator.free(self.model);
        }
        self.error_message.deinit(allocator);
    }
};

pub fn deinitAssistantContentElements(allocator: std.mem.Allocator, blocks: []AssistantContent) void {
    for (blocks) |block| {
        switch (block) {
            .text => |t| {
                if (t.text.len > 0) allocator.free(t.text);
                if (t.text_signature) |s| allocator.free(s);
            },
            .thinking => |t| {
                if (t.thinking.len > 0) allocator.free(t.thinking);
                if (t.thinking_signature) |s| allocator.free(s);
            },
            .tool_call => |tc| {
                allocator.free(tc.id);
                allocator.free(tc.name);
                if (tc.arguments_json.len > 0) allocator.free(tc.arguments_json);
                if (tc.thought_signature) |s| allocator.free(s);
            },
            .image => |img| {
                allocator.free(img.data);
                allocator.free(img.mime_type);
            },
        }
    }
}

pub fn deinitAssistantContent(allocator: std.mem.Allocator, blocks: []AssistantContent) void {
    deinitAssistantContentElements(allocator, blocks);
    allocator.free(blocks);
}

pub const ToolResultMessage = struct {
    tool_call_id: []const u8,
    tool_name: []const u8,
    content: []const UserContentPart,
    details_json: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    artifacts: OwnedSlice(ArtifactReference) = OwnedSlice(ArtifactReference).initBorrowed(&.{}),
    working_directory: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    working_directory_observed: bool = false,
    is_error: bool,
    timestamp: i64,

    pub fn getDetailsJson(self: *const ToolResultMessage) ?[]const u8 {
        const details = self.details_json.slice();
        return if (details.len > 0) details else null;
    }

    pub fn workingDirectory(self: *const ToolResultMessage) ?[]const u8 {
        const directory = self.working_directory.slice();
        return if (directory.len > 0) directory else null;
    }

    pub fn observedWorkingDirectory(self: *const ToolResultMessage) ?[]const u8 {
        if (!self.working_directory_observed) return null;
        return self.workingDirectory();
    }

    pub fn deinit(self: *ToolResultMessage, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        allocator.free(self.tool_name);
        const mut_content: []UserContentPart = @constCast(self.content);
        for (mut_content) |*part| {
            part.deinit(allocator);
        }
        allocator.free(self.content);
        self.details_json.deinit(allocator);
        self.artifacts.deinit(allocator);
        self.working_directory.deinit(allocator);
    }
};

pub const Message = union(enum) {
    user: UserMessage,
    assistant: AssistantMessage,
    tool_result: ToolResultMessage,

    pub fn deinit(self: *Message, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .user => |*msg| msg.deinit(allocator),
            .assistant => |*msg| msg.deinit(allocator),
            .tool_result => |*msg| msg.deinit(allocator),
        }
    }

    pub fn timestamp(self: Message) i64 {
        return switch (self) {
            .user => |m| m.timestamp,
            .assistant => |m| m.timestamp,
            .tool_result => |m| m.timestamp,
        };
    }
};

pub const Tool = struct {
    name: []const u8,
    description: []const u8,
    parameters_schema_json: []const u8,

    pub fn deinit(self: *Tool, allocator: std.mem.Allocator) void {
        allocator.free(self.name);
        allocator.free(self.description);
        allocator.free(self.parameters_schema_json);
    }
};

pub const Context = struct {
    system_prompt: OwnedSlice(u8) = OwnedSlice(u8).initBorrowed(""),
    messages: []const Message,
    tools: ?[]const Tool = null,
    is_owned: bool = false,

    pub fn getSystemPrompt(self: *const Context) ?[]const u8 {
        const prompt = self.system_prompt.slice();
        return if (prompt.len > 0) prompt else null;
    }

    pub fn deinit(self: *Context, allocator: std.mem.Allocator) void {
        self.system_prompt.deinit(allocator);
        if (!self.is_owned) return;

        const mut_messages: []Message = @constCast(self.messages);
        for (mut_messages) |*msg| {
            msg.deinit(allocator);
        }
        allocator.free(self.messages);
        if (self.tools) |tools| {
            const mut_tools: []Tool = @constCast(tools);
            for (mut_tools) |*tool| {
                tool.deinit(allocator);
            }
            allocator.free(tools);
        }
    }
};

pub const CostRates = struct {
    input: f64,
    output: f64,
    cache_read: f64,
    cache_write: f64,
};

pub const CostTier = struct {
    above_input_tokens: u64,
    rates: CostRates,
};

pub const Cost = struct {
    input: f64,
    output: f64,
    cache_read: f64,
    cache_write: f64,
    tier: ?CostTier = null,

    pub fn ratesFor(self: Cost, input_tokens: u64) CostRates {
        if (self.tier) |tier| {
            if (input_tokens > tier.above_input_tokens) return tier.rates;
        }
        return .{ .input = self.input, .output = self.output, .cache_read = self.cache_read, .cache_write = self.cache_write };
    }
};

pub const PublishedCost = struct {
    input: ?f64 = null,
    output: ?f64 = null,
    cache_read: ?f64 = null,
    cache_write: ?f64 = null,
};

pub const OpenAICompatOptions = struct {
    supports_store: ?bool = null,
    supports_developer_role: ?bool = null,
    supports_reasoning_effort: ?bool = null,
    supports_usage_in_streaming: ?bool = null,
    max_tokens_field: ?enum { max_completion_tokens, max_tokens } = null,
    requires_tool_result_name: ?bool = null,
    requires_assistant_after_tool_result: ?bool = null,
    requires_thinking_as_text: ?bool = null,
    requires_mistral_tool_ids: ?bool = null,
    thinking_format: ?enum { openai, zai, qwen } = null,
    supports_strict_mode: ?bool = null,
    supports_anthropic_cache_ttl: ?bool = null,
};

pub const RoutingPreferences = struct {
    only: ?[][]const u8 = null,
    order: ?[][]const u8 = null,
};

pub const Model = struct {
    id: []const u8,
    name: []const u8,
    api: []const u8,
    provider: []const u8,
    base_url: []const u8,
    reasoning: bool,
    input: []const []const u8,
    cost: Cost,
    context_window: u32,
    max_tokens: u32,
    headers: ?[]const HeaderPair = null,
    compat: ?OpenAICompatOptions = null,
    allows_anonymous: bool = false,
    credential_withheld: bool = false,
    carries_version: ?bool = null,
    published_cost: ?PublishedCost = null,
    release_date: ?[]const u8 = null,
    family: ?[]const u8 = null,
    is_owned: bool = false,

    pub fn deinit(self: *Model, allocator: std.mem.Allocator) void {
        if (!self.is_owned) return;

        if (self.release_date) |value| allocator.free(value);
        if (self.family) |value| allocator.free(value);

        allocator.free(self.id);
        allocator.free(self.name);
        allocator.free(self.api);
        allocator.free(self.provider);
        allocator.free(self.base_url);
        for (self.input) |input| {
            allocator.free(input);
        }
        allocator.free(self.input);
        if (self.headers) |headers| {
            for (headers) |header| {
                allocator.free(header.name);
                allocator.free(header.value);
            }
            allocator.free(headers);
        }
    }
};

pub const AssistantMessageEvent = union(enum) {
    start: struct { partial: AssistantMessage },
    text_start: struct { content_index: usize, partial: AssistantMessage },
    text_delta: struct { content_index: usize, delta: []const u8, partial: AssistantMessage },
    text_end: struct { content_index: usize, content: []const u8, partial: AssistantMessage },
    thinking_start: struct { content_index: usize, partial: AssistantMessage },
    thinking_delta: struct { content_index: usize, delta: []const u8, partial: AssistantMessage },
    thinking_end: struct { content_index: usize, content: []const u8, partial: AssistantMessage },
    toolcall_start: struct {
        content_index: usize,
        id: []const u8,
        name: []const u8,
        partial: AssistantMessage,
    },
    toolcall_delta: struct { content_index: usize, delta: []const u8, partial: AssistantMessage },
    toolcall_end: struct { content_index: usize, tool_call: ToolCall, partial: AssistantMessage },
    done: struct { reason: StopReason, message: AssistantMessage },
    @"error": struct { reason: StopReason, err: AssistantMessage },
    keepalive: void,
};

pub fn cloneToolCall(allocator: std.mem.Allocator, tool_call: ToolCall) error{OutOfMemory}!ToolCall {
    const id = try allocator.dupe(u8, tool_call.id);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, tool_call.name);
    errdefer allocator.free(name);
    const arguments_json = try allocator.dupe(u8, tool_call.arguments_json);
    errdefer allocator.free(arguments_json);
    const thought_signature = if (tool_call.thought_signature) |s|
        try allocator.dupe(u8, s)
    else
        null;
    errdefer if (thought_signature) |s| allocator.free(s);

    return .{
        .id = id,
        .name = name,
        .arguments_json = arguments_json,
        .thought_signature = thought_signature,
    };
}

pub fn deinitToolCall(allocator: std.mem.Allocator, tool_call: *ToolCall) void {
    allocator.free(tool_call.id);
    allocator.free(tool_call.name);
    if (tool_call.arguments_json.len > 0) allocator.free(tool_call.arguments_json);
    if (tool_call.thought_signature) |s| allocator.free(s);
}

pub fn cloneAssistantMessage(allocator: std.mem.Allocator, msg: AssistantMessage) !AssistantMessage {
    var content = try allocator.alloc(AssistantContent, msg.content.len);
    var cloned_count: usize = 0;
    errdefer {
        for (content[0..cloned_count]) |block| {
            switch (block) {
                .text => |t| {
                    allocator.free(t.text);
                    if (t.text_signature) |s| allocator.free(s);
                },
                .thinking => |t| {
                    allocator.free(t.thinking);
                    if (t.thinking_signature) |s| allocator.free(s);
                },
                .tool_call => |tc| {
                    allocator.free(tc.id);
                    allocator.free(tc.name);
                    allocator.free(tc.arguments_json);
                    if (tc.thought_signature) |s| allocator.free(s);
                },
                .image => |img| {
                    allocator.free(img.data);
                    allocator.free(img.mime_type);
                },
            }
        }
        allocator.free(content);
    }

    for (msg.content, 0..) |block, i| {
        content[i] = switch (block) {
            .text => |t| blk: {
                const text = try allocator.dupe(u8, t.text);
                errdefer allocator.free(text);
                const text_signature = if (t.text_signature) |s| try allocator.dupe(u8, s) else null;
                errdefer if (text_signature) |s| allocator.free(s);
                break :blk .{ .text = .{
                    .text = text,
                    .text_signature = text_signature,
                } };
            },
            .thinking => |t| blk: {
                const thinking = try allocator.dupe(u8, t.thinking);
                errdefer allocator.free(thinking);
                const thinking_signature = if (t.thinking_signature) |s| try allocator.dupe(u8, s) else null;
                errdefer if (thinking_signature) |s| allocator.free(s);
                break :blk .{ .thinking = .{
                    .thinking = thinking,
                    .thinking_signature = thinking_signature,
                } };
            },
            .tool_call => |tc| blk: {
                const id = try allocator.dupe(u8, tc.id);
                errdefer allocator.free(id);
                const name = try allocator.dupe(u8, tc.name);
                errdefer allocator.free(name);
                const arguments_json = try allocator.dupe(u8, tc.arguments_json);
                errdefer allocator.free(arguments_json);
                const thought_signature = if (tc.thought_signature) |s| try allocator.dupe(u8, s) else null;
                errdefer if (thought_signature) |s| allocator.free(s);
                break :blk .{ .tool_call = .{
                    .id = id,
                    .name = name,
                    .arguments_json = arguments_json,
                    .thought_signature = thought_signature,
                } };
            },
            .image => |img| blk: {
                const data = try allocator.dupe(u8, img.data);
                errdefer allocator.free(data);
                const mime_type = try allocator.dupe(u8, img.mime_type);
                errdefer allocator.free(mime_type);
                break :blk .{ .image = .{
                    .data = data,
                    .mime_type = mime_type,
                } };
            },
        };
        cloned_count += 1;
    }

    const error_msg = if (msg.getErrorMessage()) |e|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, e))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer {
        var em = error_msg;
        em.deinit(allocator);
    }

    const api = try allocator.dupe(u8, msg.api);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, msg.provider);
    errdefer allocator.free(provider);
    const model_str = try allocator.dupe(u8, msg.model);
    errdefer allocator.free(model_str);

    return .{
        .content = content,
        .api = api,
        .provider = provider,
        .model = model_str,
        .usage = msg.usage,
        .stop_reason = msg.stop_reason,
        .error_message = error_msg,
        .timestamp = msg.timestamp,
        .is_owned = true,
    };
}

pub const CarriedPartial = struct {
    partial: AssistantMessage,
    owned: ?[]AssistantContent,

    pub fn release(self: CarriedPartial, allocator: std.mem.Allocator) void {
        const owned = self.owned orelse return;
        allocator.free(owned);
    }
};

pub fn partialWithContent(
    allocator: std.mem.Allocator,
    base: AssistantMessage,
    content: []const AssistantContent,
    index: usize,
) error{OutOfMemory}!CarriedPartial {
    if (index >= content.len) return .{ .partial = base, .owned = null };
    const slice = try allocator.alloc(AssistantContent, index + 1);
    @memcpy(slice, content[0 .. index + 1]);
    var out = base;
    out.content = slice;
    return .{ .partial = out, .owned = slice };
}

pub fn deinitAssistantMessageOwned(allocator: std.mem.Allocator, msg: *AssistantMessage) void {
    msg.deinit(allocator);
}

pub fn buildOwnedMessage(
    allocator: std.mem.Allocator,
    content: []AssistantContent,
    api_src: []const u8,
    provider_src: []const u8,
    model_src: []const u8,
    usage: Usage,
    stop_reason: StopReason,
    timestamp: i64,
) error{OutOfMemory}!AssistantMessage {
    const api = try allocator.dupe(u8, api_src);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, provider_src);
    errdefer allocator.free(provider);
    const model_id = try allocator.dupe(u8, model_src);
    return .{
        .content = content,
        .api = api,
        .provider = provider,
        .model = model_id,
        .usage = usage,
        .stop_reason = stop_reason,
        .timestamp = timestamp,
        .is_owned = true,
    };
}

pub const LOST_CLONE_MESSAGE = "an event could not be queued: out of memory";

pub fn settleProviderOutcome(stream: anytype, out: AssistantMessage) void {
    if (stream.pushFailed()) {
        var dropped = out;
        dropped.deinit(stream.allocator);
        stream.completeWithError(LOST_CLONE_MESSAGE);
        return;
    }
    if (stream.completeIfOpen(out)) return;
    var dropped = out;
    dropped.deinit(stream.allocator);
}

pub const OwnedMessage = struct {
    const Self = @This();

    message: AssistantMessage,

    pub fn cloneOf(allocator: std.mem.Allocator, msg: AssistantMessage) !Self {
        return .{ .message = try cloneAssistantMessage(allocator, msg) };
    }

    pub fn borrow(self: *const Self) *const AssistantMessage {
        return &self.message;
    }

    pub fn intoMessage(self: *Self) AssistantMessage {
        const released = self.message;
        self.* = undefined;
        return released;
    }

    pub fn deinit(self: *Self, allocator: std.mem.Allocator) void {
        self.message.deinit(allocator);
        self.* = undefined;
    }
};

pub fn cloneAssistantMessageEvent(allocator: std.mem.Allocator, event: AssistantMessageEvent) !AssistantMessageEvent {
    return switch (event) {
        .start => |s| .{ .start = .{
            .partial = try cloneAssistantMessage(allocator, s.partial),
        } },
        .text_start => |t| .{ .text_start = .{
            .content_index = t.content_index,
            .partial = try cloneAssistantMessage(allocator, t.partial),
        } },
        .text_delta => |d| blk: {
            const delta = try allocator.dupe(u8, d.delta);
            errdefer allocator.free(delta);
            var partial = try cloneAssistantMessage(allocator, d.partial);
            errdefer partial.deinit(allocator);
            break :blk .{ .text_delta = .{
                .content_index = d.content_index,
                .delta = delta,
                .partial = partial,
            } };
        },
        .text_end => |t| blk: {
            const content = try allocator.dupe(u8, t.content);
            errdefer allocator.free(content);
            var partial = try cloneAssistantMessage(allocator, t.partial);
            errdefer partial.deinit(allocator);
            break :blk .{ .text_end = .{
                .content_index = t.content_index,
                .content = content,
                .partial = partial,
            } };
        },
        .thinking_start => |t| .{ .thinking_start = .{
            .content_index = t.content_index,
            .partial = try cloneAssistantMessage(allocator, t.partial),
        } },
        .thinking_delta => |d| blk: {
            const delta = try allocator.dupe(u8, d.delta);
            errdefer allocator.free(delta);
            var partial = try cloneAssistantMessage(allocator, d.partial);
            errdefer partial.deinit(allocator);
            break :blk .{ .thinking_delta = .{
                .content_index = d.content_index,
                .delta = delta,
                .partial = partial,
            } };
        },
        .thinking_end => |t| blk: {
            const content = try allocator.dupe(u8, t.content);
            errdefer allocator.free(content);
            var partial = try cloneAssistantMessage(allocator, t.partial);
            errdefer partial.deinit(allocator);
            break :blk .{ .thinking_end = .{
                .content_index = t.content_index,
                .content = content,
                .partial = partial,
            } };
        },
        .toolcall_start => |t| blk: {
            const id = try allocator.dupe(u8, t.id);
            errdefer allocator.free(id);
            const name = try allocator.dupe(u8, t.name);
            errdefer allocator.free(name);
            var partial = try cloneAssistantMessage(allocator, t.partial);
            errdefer partial.deinit(allocator);
            break :blk .{ .toolcall_start = .{
                .content_index = t.content_index,
                .id = id,
                .name = name,
                .partial = partial,
            } };
        },
        .toolcall_delta => |d| blk: {
            const delta = try allocator.dupe(u8, d.delta);
            errdefer allocator.free(delta);
            var partial = try cloneAssistantMessage(allocator, d.partial);
            errdefer partial.deinit(allocator);
            break :blk .{ .toolcall_delta = .{
                .content_index = d.content_index,
                .delta = delta,
                .partial = partial,
            } };
        },
        .toolcall_end => |t| blk: {
            const id = try allocator.dupe(u8, t.tool_call.id);
            errdefer allocator.free(id);
            const name = try allocator.dupe(u8, t.tool_call.name);
            errdefer allocator.free(name);
            const arguments_json = try allocator.dupe(u8, t.tool_call.arguments_json);
            errdefer allocator.free(arguments_json);
            const thought_signature = if (t.tool_call.thought_signature) |s|
                try allocator.dupe(u8, s)
            else
                null;
            errdefer if (thought_signature) |s| allocator.free(s);
            var partial = try cloneAssistantMessage(allocator, t.partial);
            errdefer partial.deinit(allocator);

            break :blk .{ .toolcall_end = .{
                .content_index = t.content_index,
                .tool_call = .{
                    .id = id,
                    .name = name,
                    .arguments_json = arguments_json,
                    .thought_signature = thought_signature,
                },
                .partial = partial,
            } };
        },
        .done => |d| .{ .done = .{
            .reason = d.reason,
            .message = try cloneAssistantMessage(allocator, d.message),
        } },
        .@"error" => |e| .{ .@"error" = .{
            .reason = e.reason,
            .err = try cloneAssistantMessage(allocator, e.err),
        } },
        .keepalive => .keepalive,
    };
}

pub fn deinitAssistantMessageEvent(allocator: std.mem.Allocator, event: *AssistantMessageEvent) void {
    switch (event.*) {
        .start => |*s| s.partial.deinit(allocator),
        .text_start => |*t| t.partial.deinit(allocator),
        .thinking_start => |*t| t.partial.deinit(allocator),
        .toolcall_start => |*t| {
            allocator.free(t.id);
            allocator.free(t.name);
            t.partial.deinit(allocator);
        },
        .text_delta => |*d| {
            allocator.free(d.delta);
            d.partial.deinit(allocator);
        },
        .text_end => |*t| {
            allocator.free(t.content);
            t.partial.deinit(allocator);
        },
        .thinking_delta => |*t| {
            allocator.free(t.delta);
            t.partial.deinit(allocator);
        },
        .thinking_end => |*t| {
            allocator.free(t.content);
            t.partial.deinit(allocator);
        },
        .toolcall_delta => |*t| {
            allocator.free(t.delta);
            t.partial.deinit(allocator);
        },
        .toolcall_end => |*t| {
            allocator.free(t.tool_call.id);
            allocator.free(t.tool_call.name);
            if (t.tool_call.arguments_json.len > 0) allocator.free(t.tool_call.arguments_json);
            if (t.tool_call.thought_signature) |s| allocator.free(s);
            t.partial.deinit(allocator);
        },
        .done => |*d| d.message.deinit(allocator),
        .@"error" => |*e| e.err.deinit(allocator),
        .keepalive => {},
    }
}

pub fn cloneMessage(allocator: std.mem.Allocator, msg: Message) !Message {
    return switch (msg) {
        .user => |u| .{ .user = .{
            .content = try cloneUserContent(u.content, allocator),
            .timestamp = u.timestamp,
        } },
        .assistant => |a| .{ .assistant = try cloneAssistantMessage(allocator, a) },
        .tool_result => |tr| .{ .tool_result = try cloneToolResultMessage(allocator, tr) },
    };
}

fn cloneUserContent(content: UserContent, allocator: std.mem.Allocator) !UserContent {
    return switch (content) {
        .text => |t| .{ .text = try allocator.dupe(u8, t) },
        .parts => |parts| blk: {
            const cloned_parts = try allocator.alloc(UserContentPart, parts.len);
            var initialized: usize = 0;
            errdefer {
                for (cloned_parts[0..initialized]) |*part| part.deinit(allocator);
                allocator.free(cloned_parts);
            }

            for (parts, 0..) |part, i| {
                cloned_parts[i] = try cloneUserContentPart(allocator, part);
                initialized += 1;
            }
            break :blk .{ .parts = cloned_parts };
        },
    };
}

fn cloneUserContentPart(allocator: std.mem.Allocator, part: UserContentPart) !UserContentPart {
    return switch (part) {
        .text => |t| blk: {
            const text = try allocator.dupe(u8, t.text);
            errdefer allocator.free(text);
            const text_signature = if (t.text_signature) |sig|
                try allocator.dupe(u8, sig)
            else
                null;
            errdefer if (text_signature) |sig| allocator.free(sig);

            break :blk .{ .text = .{
                .text = text,
                .text_signature = text_signature,
            } };
        },
        .image => |img| blk: {
            const data = try allocator.dupe(u8, img.data);
            errdefer allocator.free(data);
            const mime_type = try allocator.dupe(u8, img.mime_type);
            errdefer allocator.free(mime_type);

            break :blk .{ .image = .{
                .data = data,
                .mime_type = mime_type,
            } };
        },
    };
}

fn cloneToolResultMessage(allocator: std.mem.Allocator, tr: ToolResultMessage) !ToolResultMessage {
    const cloned_content = try allocator.alloc(UserContentPart, tr.content.len);
    var initialized: usize = 0;
    errdefer {
        for (cloned_content[0..initialized]) |*part| part.deinit(allocator);
        allocator.free(cloned_content);
    }

    for (tr.content, 0..) |part, i| {
        cloned_content[i] = try cloneUserContentPart(allocator, part);
        initialized += 1;
    }

    var details_json = if (tr.getDetailsJson()) |dj|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, dj))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer details_json.deinit(allocator);

    const cloned_artifacts = try cloneArtifactReferences(allocator, tr.artifacts.slice());
    errdefer {
        var artifacts = OwnedSlice(ArtifactReference).initOwned(cloned_artifacts);
        artifacts.deinit(allocator);
    }

    const tool_call_id = try allocator.dupe(u8, tr.tool_call_id);
    errdefer allocator.free(tool_call_id);
    const tool_name = try allocator.dupe(u8, tr.tool_name);
    errdefer allocator.free(tool_name);
    const directory = try allocator.dupe(u8, tr.working_directory.slice());
    errdefer allocator.free(directory);

    return .{
        .tool_call_id = tool_call_id,
        .tool_name = tool_name,
        .content = cloned_content,
        .details_json = details_json,
        .artifacts = OwnedSlice(ArtifactReference).initOwned(cloned_artifacts),
        .working_directory = OwnedSlice(u8).initOwned(directory),
        .working_directory_observed = tr.working_directory_observed,
        .is_error = tr.is_error,
        .timestamp = tr.timestamp,
    };
}

fn cloneArtifactReferences(allocator: std.mem.Allocator, artifacts: []const ArtifactReference) ![]ArtifactReference {
    const cloned = try allocator.alloc(ArtifactReference, artifacts.len);
    var initialized: usize = 0;
    errdefer {
        for (cloned[0..initialized]) |*artifact| artifact.deinit(allocator);
        allocator.free(cloned);
    }

    for (artifacts, 0..) |artifact, i| {
        cloned[i] = try cloneArtifactReference(allocator, artifact);
        initialized += 1;
    }

    return cloned;
}

fn cloneArtifactReference(allocator: std.mem.Allocator, artifact: ArtifactReference) !ArtifactReference {
    const artifact_id = try allocator.dupe(u8, artifact.artifact_id);
    errdefer allocator.free(artifact_id);

    const uri = if (artifact.getUri()) |value|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, value))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer {
        var mutable = uri;
        mutable.deinit(allocator);
    }

    const mime_type = if (artifact.getMimeType()) |value|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, value))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer {
        var mutable = mime_type;
        mutable.deinit(allocator);
    }

    const sha256 = if (artifact.getSha256()) |value|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, value))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer {
        var mutable = sha256;
        mutable.deinit(allocator);
    }

    const description = if (artifact.getDescription()) |value|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, value))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer {
        var mutable = description;
        mutable.deinit(allocator);
    }

    return .{
        .artifact_id = artifact_id,
        .uri = uri,
        .mime_type = mime_type,
        .byte_size = artifact.byte_size,
        .sha256 = sha256,
        .description = description,
    };
}

pub fn cloneContext(allocator: std.mem.Allocator, ctx: Context) !Context {
    var system_prompt = if (ctx.getSystemPrompt()) |sp|
        OwnedSlice(u8).initOwned(try allocator.dupe(u8, sp))
    else
        OwnedSlice(u8).initBorrowed("");
    errdefer system_prompt.deinit(allocator);

    const messages = try allocator.alloc(Message, ctx.messages.len);
    var initialized_messages: usize = 0;
    errdefer {
        for (messages[0..initialized_messages]) |*m| m.deinit(allocator);
        allocator.free(messages);
    }

    for (ctx.messages, 0..) |msg, i| {
        messages[i] = try cloneMessage(allocator, msg);
        initialized_messages += 1;
    }

    var tools: ?[]Tool = null;
    if (ctx.tools) |t| {
        const owned_tools = try allocator.alloc(Tool, t.len);
        var initialized_tools: usize = 0;
        errdefer {
            for (owned_tools[0..initialized_tools]) |*tool| tool.deinit(allocator);
            allocator.free(owned_tools);
        }

        for (t, 0..) |tool, i| {
            const name = try allocator.dupe(u8, tool.name);
            errdefer allocator.free(name);
            const description = try allocator.dupe(u8, tool.description);
            errdefer allocator.free(description);
            const parameters_schema_json = try allocator.dupe(u8, tool.parameters_schema_json);
            errdefer allocator.free(parameters_schema_json);

            owned_tools[i] = .{
                .name = name,
                .description = description,
                .parameters_schema_json = parameters_schema_json,
            };
            initialized_tools += 1;
        }

        tools = owned_tools;
    }

    return .{
        .system_prompt = system_prompt,
        .messages = messages,
        .tools = tools,
        .is_owned = true,
    };
}

pub fn cloneModel(allocator: std.mem.Allocator, model: Model) !Model {
    const id = try allocator.dupe(u8, model.id);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, model.name);
    errdefer allocator.free(name);
    const api = try allocator.dupe(u8, model.api);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, model.provider);
    errdefer allocator.free(provider);
    const base_url = try allocator.dupe(u8, model.base_url);
    errdefer allocator.free(base_url);

    const input = try allocator.alloc([]const u8, model.input.len);
    var input_filled: usize = 0;
    errdefer {
        for (input[0..input_filled]) |in| allocator.free(in);
        allocator.free(input);
    }
    for (model.input) |inp| {
        input[input_filled] = try allocator.dupe(u8, inp);
        input_filled += 1;
    }

    var headers: ?[]HeaderPair = null;
    if (model.headers) |h| {
        const pairs = try allocator.alloc(HeaderPair, h.len);
        var filled: usize = 0;
        errdefer {
            for (pairs[0..filled]) |*hp| {
                allocator.free(hp.name);
                allocator.free(hp.value);
            }
            allocator.free(pairs);
        }

        for (h) |hp| {
            const pair_name = try allocator.dupe(u8, hp.name);
            errdefer allocator.free(pair_name);
            const pair_value = try allocator.dupe(u8, hp.value);

            pairs[filled] = .{ .name = pair_name, .value = pair_value };
            filled += 1;
        }

        headers = pairs;
    }
    errdefer if (headers) |hs| {
        for (hs) |*hp| {
            allocator.free(hp.name);
            allocator.free(hp.value);
        }
        allocator.free(hs);
    };
    const release_date = if (model.release_date) |value| try allocator.dupe(u8, value) else null;
    errdefer if (release_date) |value| allocator.free(value);
    const family = if (model.family) |value| try allocator.dupe(u8, value) else null;

    return .{
        .id = id,
        .name = name,
        .api = api,
        .provider = provider,
        .base_url = base_url,
        .reasoning = model.reasoning,
        .input = input,
        .cost = model.cost,
        .context_window = model.context_window,
        .max_tokens = model.max_tokens,
        .headers = headers,
        .compat = model.compat,
        .allows_anonymous = model.allows_anonymous,
        .credential_withheld = model.credential_withheld,
        .carries_version = model.carries_version,
        .published_cost = model.published_cost,
        .release_date = release_date,
        .family = family,
        .is_owned = true,
    };
}

test "Context deinit frees owned system_prompt even with borrowed messages" {
    const allocator = std.testing.allocator;

    var ctx = Context{
        .system_prompt = OwnedSlice(u8).initOwned(try allocator.dupe(u8, "be concise")),
        .messages = &.{},
        .is_owned = false,
    };

    ctx.deinit(allocator);
}

test "cloneAssistantMessage deep copies text content" {
    const content = [_]AssistantContent{.{ .text = .{ .text = "hello" } }};
    const msg = AssistantMessage{
        .content = &content,
        .api = "openai-completions",
        .provider = "openai",
        .model = "gpt-4o",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 123,
    };

    var cloned = try cloneAssistantMessage(std.testing.allocator, msg);
    defer deinitAssistantMessageOwned(std.testing.allocator, &cloned);

    try std.testing.expectEqualStrings("hello", cloned.content[0].text.text);
    try std.testing.expectEqualStrings("openai", cloned.provider);
}

test "cloneAssistantMessage is leak-free when an allocation fails mid-clone" {
    const allocator = std.testing.allocator;

    const content = [_]AssistantContent{
        .{ .text = .{ .text = "hello", .text_signature = "sig" } },
        .{ .thinking = .{ .thinking = "hmm", .thinking_signature = "tsig" } },
        .{ .tool_call = .{ .id = "call-1", .name = "get_weather", .arguments_json = "{}", .thought_signature = "ts" } },
        .{ .image = .{ .data = "img", .mime_type = "image/png" } },
    };
    const msg = AssistantMessage{
        .content = &content,
        .api = "openai-completions",
        .provider = "openai",
        .model = "gpt-4o",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };

    var fail_index: usize = 0;
    while (fail_index <= 20) : (fail_index += 1) {
        var failing = std.testing.FailingAllocator.init(allocator, .{ .fail_index = fail_index });
        if (cloneAssistantMessage(failing.allocator(), msg)) |cloned| {
            var mutable = cloned;
            mutable.deinit(failing.allocator());
        } else |err| {
            try std.testing.expectEqual(error.OutOfMemory, err);
        }
    }
}

test "cloneToolCall deep copies borrowed tool-call strings" {
    const allocator = std.testing.allocator;

    const borrowed = ToolCall{
        .id = "call-1",
        .name = "get_weather",
        .arguments_json = "{\"city\":\"SF\"}",
        .thought_signature = "sig-1",
    };

    var owned = try cloneToolCall(allocator, borrowed);
    defer deinitToolCall(allocator, &owned);

    try std.testing.expectEqualStrings("call-1", owned.id);
    try std.testing.expectEqualStrings("get_weather", owned.name);
    try std.testing.expectEqualStrings("{\"city\":\"SF\"}", owned.arguments_json);
    try std.testing.expectEqualStrings("sig-1", owned.thought_signature.?);
    try std.testing.expect(@intFromPtr(owned.id.ptr) != @intFromPtr(borrowed.id.ptr));
    try std.testing.expect(@intFromPtr(owned.name.ptr) != @intFromPtr(borrowed.name.ptr));
}

test "ToolResultMessage details_json uses OwnedSlice and deep clones" {
    const allocator = std.testing.allocator;

    const content = try allocator.alloc(UserContentPart, 1);
    content[0] = .{ .text = .{ .text = try allocator.dupe(u8, "ok") } };

    var msg = ToolResultMessage{
        .tool_call_id = try allocator.dupe(u8, "call-1"),
        .tool_name = try allocator.dupe(u8, "test_tool"),
        .content = content,
        .details_json = OwnedSlice(u8).initOwned(try allocator.dupe(u8, "{\"k\":1}")),
        .is_error = false,
        .timestamp = 1,
    };
    defer msg.deinit(allocator);

    var cloned = try cloneToolResultMessage(allocator, msg);
    defer cloned.deinit(allocator);

    try std.testing.expectEqualStrings("{\"k\":1}", msg.getDetailsJson().?);
    try std.testing.expectEqualStrings("{\"k\":1}", cloned.getDetailsJson().?);
    try std.testing.expect(@intFromPtr(msg.details_json.slice().ptr) != @intFromPtr(cloned.details_json.slice().ptr));
}

test "AssistantMessageEventStream deinit drains unpolled events" {
    const event_stream = @import("event_stream");
    var stream = event_stream.AssistantMessageEventStream.init(std.testing.allocator);
    defer stream.deinit();

    const delta_str = try std.testing.allocator.dupe(u8, "test delta content");
    const partial = AssistantMessage{
        .content = &.{},
        .api = "google-generative-ai",
        .provider = "google",
        .model = "gemini-2.5-flash",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };
    const event = AssistantMessageEvent{
        .text_delta = .{
            .content_index = 0,
            .delta = delta_str,
            .partial = partial,
        },
    };
    try stream.push(event);

    if (stream.poll()) |evt| {
        switch (evt) {
            .text_delta => |d| std.testing.allocator.free(d.delta),
            else => {},
        }
    }

    const result = AssistantMessage{
        .content = &.{},
        .api = "google-generative-ai",
        .provider = "google",
        .model = "gemini-2.5-flash",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };
    stream.complete(result);
}

test "AssistantMessageEventStream deinit drains unpolled toolcall_end events" {
    const event_stream = @import("event_stream");
    var stream = event_stream.AssistantMessageEventStream.init(std.testing.allocator);
    defer stream.deinit();

    const tool_id = try std.testing.allocator.dupe(u8, "tool-123");
    const tool_name = try std.testing.allocator.dupe(u8, "bash");
    const args_json = try std.testing.allocator.dupe(u8, "{\"cmd\": \"ls\"}");
    const partial = AssistantMessage{
        .content = &.{},
        .api = "openai-completions",
        .provider = "openai",
        .model = "gpt-4o",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };

    const event = AssistantMessageEvent{
        .toolcall_end = .{
            .content_index = 0,
            .tool_call = .{
                .id = tool_id,
                .name = tool_name,
                .arguments_json = args_json,
            },
            .partial = partial,
        },
    };
    try stream.push(event);

    if (stream.poll()) |evt| {
        switch (evt) {
            .toolcall_end => |tc| {
                std.testing.allocator.free(tc.tool_call.id);
                std.testing.allocator.free(tc.tool_call.name);
                std.testing.allocator.free(tc.tool_call.arguments_json);
            },
            else => {},
        }
    }

    const result = AssistantMessage{
        .content = &.{},
        .api = "openai-completions",
        .provider = "openai",
        .model = "gpt-4o",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };
    stream.complete(result);
}

test "Usage.calculateCost computes correct dollar costs" {
    var usage = Usage{
        .input = 1_000_000,
        .output = 500_000,
        .cache_read = 200_000,
        .cache_write = 100_000,
    };

    const model_cost = Cost{
        .input = 3.0,
        .output = 15.0,
        .cache_read = 0.30,
        .cache_write = 3.75,
    };

    usage.calculateCost(model_cost);

    try std.testing.expectApproxEqAbs(3.0, usage.cost.input, 0.0001);
    try std.testing.expectApproxEqAbs(7.5, usage.cost.output, 0.0001);
    try std.testing.expectApproxEqAbs(0.06, usage.cost.cache_read, 0.0001);
    try std.testing.expectApproxEqAbs(0.375, usage.cost.cache_write, 0.0001);
    try std.testing.expectApproxEqAbs(10.935, usage.cost.total, 0.0001);
}

test "a request whose input, cache included, passes a cost tier's threshold is charged the tier's rates" {
    const tiered = Cost{
        .input = 0.10,
        .output = 0.50,
        .cache_read = 0.01,
        .cache_write = 0.125,
        .tier = .{ .above_input_tokens = 100_000, .rates = .{ .input = 0.50, .output = 2.50, .cache_read = 0.05, .cache_write = 0.625 } },
    };

    var within = Usage{ .input = 60_000, .output = 1_000_000, .cache_read = 40_000 };
    within.calculateCost(tiered);
    try std.testing.expectApproxEqAbs(0.50, within.cost.output, 0.0001);

    var above = Usage{ .input = 60_000, .output = 1_000_000, .cache_read = 40_001 };
    above.calculateCost(tiered);
    try std.testing.expectApproxEqAbs(2.50, above.cost.output, 0.0001);
    try std.testing.expectApproxEqAbs(0.03, above.cost.input, 0.0001);
}

test "OpenAICompatOptions defaults are correct" {
    const compat = OpenAICompatOptions{};

    try std.testing.expect(compat.supports_store == null);
    try std.testing.expect(compat.supports_developer_role == null);
    try std.testing.expect(compat.supports_reasoning_effort == null);
    try std.testing.expect(compat.supports_usage_in_streaming == null);
    try std.testing.expect(compat.max_tokens_field == null);
    try std.testing.expect(compat.requires_tool_result_name == null);
    try std.testing.expect(compat.requires_assistant_after_tool_result == null);
    try std.testing.expect(compat.requires_thinking_as_text == null);
    try std.testing.expect(compat.requires_mistral_tool_ids == null);
    try std.testing.expect(compat.thinking_format == null);
    try std.testing.expect(compat.supports_strict_mode == null);
}

test "Model with compat options" {
    const model = Model{
        .id = "test-model",
        .name = "Test Model",
        .api = "openai-completions",
        .provider = "test",
        .base_url = "https://api.test.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .compat = .{
            .supports_store = false,
            .max_tokens_field = .max_tokens,
            .thinking_format = .zai,
        },
    };

    try std.testing.expect(model.compat != null);
    try std.testing.expect(!model.compat.?.supports_store.?);
    try std.testing.expect(model.compat.?.max_tokens_field.? == .max_tokens);
    try std.testing.expect(model.compat.?.thinking_format.? == .zai);
}

test "RoutingPreferences defaults are correct" {
    const prefs = RoutingPreferences{};

    try std.testing.expect(prefs.only == null);
    try std.testing.expect(prefs.order == null);
}

test "cloneModel frees duped header pairs when a later allocation fails" {
    const allocator = std.testing.allocator;

    const headers = [_]HeaderPair{
        .{ .name = "x-one", .value = "first" },
        .{ .name = "x-two", .value = "second" },
        .{ .name = "x-three", .value = "third" },
    };

    const model = Model{
        .id = "test-model",
        .name = "Test Model",
        .api = "openai-completions",
        .provider = "test",
        .base_url = "https://api.test.com",
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 100,
        .headers = &headers,
    };

    const Case = struct {
        fn run(failing: std.mem.Allocator, source: Model) !void {
            var cloned = try cloneModel(failing, source);
            cloned.deinit(failing);
        }
    };

    try std.testing.checkAllAllocationFailures(allocator, Case.run, .{model});
}

test "OwnedMessage copy is freed by its own deinit, and intoMessage hands it off" {
    const allocator = std.testing.allocator;

    const content = [_]AssistantContent{.{ .text = .{ .text = "borrowed" } }};
    const source = AssistantMessage{
        .content = &content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 3,
    };

    var owned = try OwnedMessage.cloneOf(allocator, source);
    try std.testing.expectEqualStrings("borrowed", owned.borrow().content[0].text.text);
    try std.testing.expect(owned.borrow().is_owned);
    owned.deinit(allocator);

    var second = try OwnedMessage.cloneOf(allocator, source);
    const released = second.intoMessage();
    try std.testing.expectEqualStrings("borrowed", released.content[0].text.text);
    var mutable = released;
    mutable.deinit(allocator);
}

test "OwnedMessage cloneOf frees its copy when a later allocation fails" {
    const allocator = std.testing.allocator;

    const content = [_]AssistantContent{
        .{ .text = .{ .text = "one" } },
        .{ .text = .{ .text = "two" } },
    };
    const source = AssistantMessage{
        .content = &content,
        .api = "test-api",
        .provider = "test-provider",
        .model = "test-model",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 4,
    };

    const Case = struct {
        fn run(failing: std.mem.Allocator, src: AssistantMessage) !void {
            var owned = try OwnedMessage.cloneOf(failing, src);
            owned.deinit(failing);
        }
    };

    try std.testing.checkAllAllocationFailures(allocator, Case.run, .{source});
}

test "buildOwnedMessage releases only what it allocated when a later dupe fails" {
    var fail_at: usize = 1;
    while (fail_at <= 6) : (fail_at += 1) {
        var failing = std.testing.FailingAllocator.init(std.testing.allocator, .{ .fail_index = fail_at });
        const alloc = failing.allocator();

        const content = alloc.alloc(AssistantContent, 1) catch continue;
        content[0] = .{ .text = .{ .text = alloc.dupe(u8, "answer") catch {
            alloc.free(content);
            continue;
        } } };

        const built = buildOwnedMessage(alloc, content, "anthropic-messages", "anthropic", "claude", .{}, .stop, 0);
        if (built) |*message| {
            var owned = message.*;
            owned.deinit(alloc);
        } else |err| {
            try std.testing.expectEqual(error.OutOfMemory, err);
            var held = AssistantMessage{
                .content = content,
                .api = "",
                .provider = "",
                .model = "",
                .usage = .{},
                .stop_reason = .stop,
                .timestamp = 0,
                .is_owned = true,
            };
            held.deinit(alloc);
        }
    }
}

test "partialWithContent puts the block at the index it is asked for" {
    const allocator = std.testing.allocator;
    const content = [_]AssistantContent{
        .{ .text = .{ .text = "before" } },
        .{ .thinking = .{ .thinking = "pondering", .thinking_signature = "sig-9" } },
    };
    const carried = try partialWithContent(allocator, .{
        .content = &.{},
        .api = "anthropic-messages",
        .provider = "anthropic",
        .model = "claude",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    }, &content, 1);
    defer carried.release(allocator);
    try std.testing.expectEqual(@as(usize, 2), carried.partial.content.len);
    switch (carried.partial.content[1]) {
        .thinking => |t| try std.testing.expectEqualStrings("sig-9", t.thinking_signature.?),
        else => return error.NotCarried,
    }
}

test "partialWithContent leaves the partial alone when the index is not there" {
    const allocator = std.testing.allocator;
    const content = [_]AssistantContent{.{ .text = .{ .text = "only" } }};
    const carried = try partialWithContent(allocator, .{
        .content = &.{},
        .api = "anthropic-messages",
        .provider = "anthropic",
        .model = "claude",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    }, &content, 4);
    try std.testing.expectEqual(@as(?[]AssistantContent, null), carried.owned);
    try std.testing.expectEqual(@as(usize, 0), carried.partial.content.len);
}


test "a copied tool result keeps its directory and the observed flag" {
    const cases = [_]struct { path: []const u8, observed: bool }{
        .{ .path = "/observed/dir", .observed = true },
        .{ .path = "/start/dir", .observed = false },
    };
    for (cases, 0..) |case, index| {
        const source = try std.testing.allocator.dupe(u8, case.path);
        defer std.testing.allocator.free(source);
        const original = ToolResultMessage{
            .tool_call_id = "call-1",
            .tool_name = "dirtool",
            .content = &.{.{ .text = .{ .text = "done" } }},
            .details_json = OwnedSlice(u8).initBorrowed(""),
            .working_directory = OwnedSlice(u8).initOwned(source),
            .working_directory_observed = case.observed,
            .is_error = false,
            .timestamp = @intCast(index),
        };
        var clone = try cloneToolResultMessage(std.testing.allocator, original);
        defer clone.deinit(std.testing.allocator);
        try std.testing.expectEqualStrings(case.path, clone.workingDirectory().?);
        const observed = clone.observedWorkingDirectory();
        if (case.observed) {
            try std.testing.expectEqualStrings(case.path, observed.?);
        } else {
            try std.testing.expect(observed == null);
        }
    }
}

test "release frees the carried array once the cloned event has its own copy" {
    const allocator = std.testing.allocator;
    const content = [_]AssistantContent{.{ .text = .{ .text = "held" } }};
    const carried = try partialWithContent(allocator, .{
        .content = &.{},
        .api = "anthropic-messages",
        .provider = "anthropic",
        .model = "claude",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    }, &content, 0);
    const slice = carried.owned.?;
    try std.testing.expectEqual(slice.ptr, carried.partial.content.ptr);
    carried.release(allocator);
}
