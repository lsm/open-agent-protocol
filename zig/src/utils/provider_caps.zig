const std = @import("std");

pub const ProviderType = enum {
    anthropic,
    openai_compatible,
    openai_native,
    google,
    bedrock,
    azure,
    ollama,
    unknown,
};

pub const ProviderCapabilities = struct {
    streaming: bool = true,

    extended_thinking: bool = false,

    prompt_caching: bool = false,

    vision: bool = false,

    function_calling: bool = true,

    requires_mistral_tool_ids: bool = false,

    supports_reasoning_effort: bool = false,

    reasoning_field: ?[]const u8 = null,

    provider_type: ProviderType = .unknown,

    supports_developer_role: bool = false,

    max_tokens_field: []const u8 = "max_tokens",

    thinking_format: enum { openai, zai, qwen } = .openai,

    requires_thinking_as_text: bool = false,

    requires_assistant_after_tool: bool = false,

    requires_tool_result_name: bool = false,
};

pub fn isGitHubCopilot(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    return std.mem.find(u8, url, "api.githubcopilot.com") != null;
}

pub fn isMistral(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "mistral.ai");
}

pub fn isGroq(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "groq.com");
}

pub fn isCerebras(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "cerebras.ai");
}

pub fn isZai(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    return std.mem.find(u8, url, "api.zukijourney.com") != null or std.mem.find(u8, url, "zai") != null;
}

pub fn isOpenRouter(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    return std.mem.find(u8, url, "openrouter.ai") != null;
}

pub fn isChutes(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "chutes.ai");
}

pub fn isQwen(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    return std.mem.find(u8, url, "dashscope") != null or std.mem.find(u8, url, "qwen") != null;
}

pub fn isDeepSeek(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    return std.mem.find(u8, url, "api.deepseek.com") != null;
}

pub fn isHostOrSubdomainOf(base_url: ?[]const u8, domain: []const u8) bool {
    const url = base_url orelse return false;
    const uri = std.Uri.parse(url) catch return false;
    const host = uri.host orelse return false;
    const value = host.percent_encoded;
    return std.ascii.eqlIgnoreCase(value, domain) or
        (value.len > domain.len and std.ascii.eqlIgnoreCase(value[value.len - domain.len ..], domain) and value[value.len - domain.len - 1] == '.');
}

pub fn isOpenAIHost(base_url: []const u8) bool {
    return isHostOrSubdomainOf(base_url, "openai.com");
}

pub fn isAnthropic(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    return std.mem.find(u8, url, "api.anthropic.com") != null;
}

pub fn detectProviderType(base_url: ?[]const u8) ProviderType {
    const url = base_url orelse return .unknown;

    if (isAnthropic(url)) return .anthropic;
    if (isOpenAIHost(url)) return .openai_native;
    if (isGitHubCopilot(url)) return .openai_compatible;
    if (isMistral(url)) return .openai_compatible;
    if (isGroq(url)) return .openai_compatible;
    if (isCerebras(url)) return .openai_compatible;
    if (isZai(url)) return .openai_compatible;
    if (isOpenRouter(url)) return .openai_compatible;
    if (std.mem.find(u8, url, "generativelanguage.googleapis.com") != null) return .google;
    if (std.mem.find(u8, url, "aiplatform.googleapis.com") != null) return .google;
    if (std.mem.find(u8, url, "bedrock-runtime.") != null or std.mem.find(u8, url, "bedrock.") != null) return .bedrock;
    if (std.mem.find(u8, url, ".openai.azure.com") != null or std.mem.find(u8, url, "cognitiveservices.azure.com") != null) return .azure;
    if (std.mem.find(u8, url, "localhost:11434") != null or std.mem.find(u8, url, "127.0.0.1:11434") != null or std.mem.find(u8, url, "ollama") != null) return .ollama;

    if (url.len > 0) return .openai_compatible;

    return .unknown;
}

pub fn detectCapabilities(base_url: ?[]const u8) ProviderCapabilities {
    const provider_type = detectProviderType(base_url);

    return switch (provider_type) {
        .anthropic => .{
            .extended_thinking = true,
            .prompt_caching = true,
            .vision = true,
            .function_calling = true,
            .provider_type = .anthropic,
        },
        .openai_native => capabilities: {
            var caps: ProviderCapabilities = .{
                .extended_thinking = true,
                .prompt_caching = true,
                .vision = true,
                .function_calling = true,
                .supports_reasoning_effort = true,
                .provider_type = .openai_native,
                .supports_developer_role = true,
                .max_tokens_field = "max_completion_tokens",
            };
            if (base_url) |url| {
                if (isZai(url)) {
                    caps.thinking_format = .zai;
                }
            }
            break :capabilities caps;
        },
        .openai_compatible => capabilities: {
            var caps: ProviderCapabilities = .{
                .vision = true,
                .function_calling = true,
                .provider_type = .openai_compatible,
            };
            if (base_url) |url| {
                caps.requires_mistral_tool_ids = isMistral(url);
                if (isMistral(url) or isChutes(url)) {
                    caps.max_tokens_field = "max_tokens";
                }
                if (isZai(url)) {
                    caps.thinking_format = .zai;
                }
                if (isQwen(url)) {
                    caps.thinking_format = .qwen;
                }
                if (isDeepSeek(url)) {
                    caps.requires_thinking_as_text = true;
                }
            }
            break :capabilities caps;
        },
        .google => .{
            .extended_thinking = true,
            .prompt_caching = true,
            .vision = true,
            .function_calling = true,
            .provider_type = .google,
        },
        .bedrock => .{
            .extended_thinking = true,
            .prompt_caching = true,
            .vision = true,
            .function_calling = true,
            .provider_type = .bedrock,
        },
        .azure => .{
            .extended_thinking = true,
            .prompt_caching = true,
            .vision = true,
            .function_calling = true,
            .provider_type = .azure,
        },
        .ollama => .{
            .vision = true,
            .function_calling = true,
            .provider_type = .ollama,
        },
        .unknown => .{},
    };
}

test "isGitHubCopilot detection" {
    try std.testing.expect(isGitHubCopilot("https://api.githubcopilot.com/v1/chat"));
    try std.testing.expect(!isGitHubCopilot("https://api.openai.com/v1/chat"));
    try std.testing.expect(!isGitHubCopilot(null));
}

test "a chutes host is chutes.ai or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.chutes.ai",
        "https://api.chutes.ai/v1",
        "https://chutes.ai",
        "https://API.CHUTES.AI",
    };
    for (hosts) |url| {
        try std.testing.expect(isChutes(url));
    }

    const not_hosts = [_][]const u8{
        "https://mychutes.ai",
        "https://notchutes.ai",
        "https://chutes.ai.evil.example",
        "https://evil.example/?next=chutes.ai",
        "https://evil.example/v1/chutes.ai",
        "https://gateway.example/proxy/chutes.ai",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isChutes(url));
    }

    try std.testing.expect(!isChutes(null));
}

test "a cerebras host is cerebras.ai or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.cerebras.ai",
        "https://api.cerebras.ai/v1",
        "https://cerebras.ai",
        "https://API.CEREBRAS.AI",
    };
    for (hosts) |url| {
        try std.testing.expect(isCerebras(url));
    }

    const not_hosts = [_][]const u8{
        "https://mycerebras.ai",
        "https://notcerebras.ai",
        "https://cerebras.ai.evil.example",
        "https://evil.example/?next=api.cerebras.ai",
        "https://evil.example/v1/api.cerebras.ai",
        "https://gateway.example/proxy/api.cerebras.ai",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isCerebras(url));
    }

    try std.testing.expect(!isCerebras(null));
}

test "a groq host is groq.com or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.groq.com",
        "https://api.groq.com/openai/v1",
        "https://groq.com",
        "https://API.GROQ.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isGroq(url));
    }

    const not_hosts = [_][]const u8{
        "https://mygroq.com",
        "https://notgroq.com",
        "https://groq.com.evil.example",
        "https://evil.example/?next=api.groq.com",
        "https://evil.example/v1/api.groq.com",
        "https://gateway.example/proxy/api.groq.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isGroq(url));
    }

    try std.testing.expect(!isGroq(null));
}

test "a mistral host is mistral.ai or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.mistral.ai",
        "https://api.mistral.ai/v1",
        "https://mistral.ai",
        "https://API.MISTRAL.AI",
    };
    for (hosts) |url| {
        try std.testing.expect(isMistral(url));
    }

    const not_hosts = [_][]const u8{
        "https://mymistral.ai",
        "https://notmistral.ai",
        "https://mistral.ai.evil.example",
        "https://evil.example/?next=api.mistral.ai",
        "https://evil.example/v1/api.mistral.ai",
        "https://gateway.example/proxy/api.mistral.ai",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isMistral(url));
    }

    try std.testing.expect(!isMistral(null));
}

test "a host matches its own domain or a subdomain of it and nothing else" {
    const matches = [_][]const u8{
        "https://mistral.ai",
        "https://api.mistral.ai",
        "https://API.MISTRAL.AI",
        "https://inference.mistral.ai/v1",
        "https://user:key@mistral.ai",
    };
    for (matches) |url| {
        try std.testing.expect(isHostOrSubdomainOf(url, "mistral.ai"));
    }

    const misses = [_][]const u8{
        "https://mymistral.ai",
        "https://notmistral.ai",
        "https://mistral.ai.evil.example",
        "https://evil.example/?next=api.mistral.ai",
        "https://evil.example/v1/api.mistral.ai",
        "https://gateway.example/proxy/api.mistral.ai",
        "not a url at all",
        "",
    };
    for (misses) |url| {
        try std.testing.expect(!isHostOrSubdomainOf(url, "mistral.ai"));
    }

    try std.testing.expect(!isHostOrSubdomainOf(null, "mistral.ai"));
    try std.testing.expect(isHostOrSubdomainOf("https://api.mistral.ai", "ai"));
    try std.testing.expect(!isHostOrSubdomainOf("https://api.mistral.ai", "mistral.ai.uk"));
}

test "an openai host is a host ending in openai.com on a label boundary" {
    const hosts = [_][]const u8{
        "https://api.openai.com",
        "https://api.openai.com/v1",
        "https://openai.com",
        "https://eu.openai.com",
        "https://OpenAI.com",
        "https://API.OPENAI.COM",
        "https://user:pass@api.openai.com/v1",
    };
    for (hosts) |url| {
        try std.testing.expect(isOpenAIHost(url));
        try std.testing.expectEqual(ProviderType.openai_native, detectProviderType(url));
    }

    const not_hosts = [_][]const u8{
        "https://myopenai.com",
        "https://notopenai.com",
        "https://openai.com.evil.example",
        "https://api.openai.com.evil.example",
        "https://evil.example/?next=api.openai.com",
        "https://evil.example/openai/api.openai.com/v1",
        "https://azure.microsoft.com/openai/deployments/api.openai.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isOpenAIHost(url));
    }

    try std.testing.expectEqual(ProviderType.unknown, detectProviderType(null));
}

test "a base URL carrying api.openai.com in its path is detected compatible, not native" {
    const embedded = "https://gateway.example/proxy/api.openai.com/v1/chat/completions";
    try std.testing.expect(!isOpenAIHost(embedded));
    try std.testing.expectEqual(ProviderType.openai_compatible, detectProviderType(embedded));

    const caps = detectCapabilities(embedded);
    try std.testing.expectEqual(ProviderType.openai_compatible, caps.provider_type);
    try std.testing.expect(!caps.supports_developer_role);
    try std.testing.expect(!caps.supports_reasoning_effort);
    try std.testing.expectEqualStrings("max_tokens", caps.max_tokens_field);
}

test "an openai.com subdomain is detected native and gets the native caps" {
    const regional = "https://eu.openai.com/v1/chat/completions";
    try std.testing.expect(isOpenAIHost(regional));
    try std.testing.expectEqual(ProviderType.openai_native, detectProviderType(regional));

    const caps = detectCapabilities(regional);
    try std.testing.expectEqual(ProviderType.openai_native, caps.provider_type);
    try std.testing.expect(caps.supports_developer_role);
    try std.testing.expect(caps.supports_reasoning_effort);
    try std.testing.expectEqualStrings("max_completion_tokens", caps.max_tokens_field);
}

test "isMistral detection" {
    try std.testing.expect(isMistral("https://api.mistral.ai/v1/chat/completions"));
    try std.testing.expect(!isMistral("https://api.openai.com/v1/chat"));
    try std.testing.expect(!isMistral(null));
}

test "isGroq detection" {
    try std.testing.expect(isGroq("https://api.groq.com/openai/v1/chat/completions"));
    try std.testing.expect(!isGroq("https://api.openai.com/v1/chat"));
    try std.testing.expect(!isGroq(null));
}

test "detectProviderType Anthropic" {
    try std.testing.expectEqual(ProviderType.anthropic, detectProviderType("https://api.anthropic.com/v1/messages"));
}

test "detectProviderType OpenAI native" {
    try std.testing.expectEqual(ProviderType.openai_native, detectProviderType("https://api.openai.com/v1/chat/completions"));
}

test "detectProviderType Mistral" {
    try std.testing.expectEqual(ProviderType.openai_compatible, detectProviderType("https://api.mistral.ai/v1/chat/completions"));
}

test "detectCapabilities Anthropic" {
    const caps = detectCapabilities("https://api.anthropic.com/v1/messages");
    try std.testing.expect(caps.extended_thinking);
    try std.testing.expect(caps.prompt_caching);
    try std.testing.expect(caps.vision);
    try std.testing.expect(!caps.requires_mistral_tool_ids);
}

test "detectCapabilities Mistral has tool ID requirement" {
    const caps = detectCapabilities("https://api.mistral.ai/v1/chat/completions");
    try std.testing.expect(caps.requires_mistral_tool_ids);
    try std.testing.expectEqual(ProviderType.openai_compatible, caps.provider_type);
}

test "detectCapabilities unknown returns defaults" {
    const caps = detectCapabilities(null);
    try std.testing.expect(!caps.extended_thinking);
    try std.testing.expect(!caps.prompt_caching);
    try std.testing.expectEqual(ProviderType.unknown, caps.provider_type);
}
