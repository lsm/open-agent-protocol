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
    return isHostOrSubdomainOf(base_url, "githubcopilot.com");
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

fn unbracket(host: []const u8) []const u8 {
    if (host.len >= 2 and host[0] == '[' and host[host.len - 1] == ']') {
        return host[1 .. host.len - 1];
    }
    return host;
}

pub fn isOllama(base_url: ?[]const u8) bool {
    const url = base_url orelse return false;
    const uri = std.Uri.parse(url) catch return false;
    if (uri.port != 11434) return false;
    const host = uri.host orelse return false;
    const value = unbracket(host.percent_encoded);
    const loopback = [_][]const u8{ "localhost", "127.0.0.1", "::1" };
    for (loopback) |candidate| {
        if (std.ascii.eqlIgnoreCase(value, candidate)) return true;
    }
    return false;
}

const azure_labels = [_][]const u8{ "openai.azure.com", "cognitiveservices.azure.com" };

pub fn isAzure(base_url: ?[]const u8) bool {
    for (azure_labels) |label| {
        if (isHostOrSubdomainOf(base_url, label)) return true;
    }
    return false;
}

pub fn isGoogle(base_url: ?[]const u8) bool {
    const host = hostOf(base_url) orelse return false;
    if (std.ascii.eqlIgnoreCase(host, "generativelanguage.googleapis.com")) return true;
    if (std.ascii.eqlIgnoreCase(host, "aiplatform.googleapis.com")) return true;
    const suffix = "-aiplatform.googleapis.com";
    if (host.len > suffix.len and std.ascii.eqlIgnoreCase(host[host.len - suffix.len ..], suffix)) {
        if (std.mem.indexOfScalar(u8, host[0 .. host.len - suffix.len], '.') == null) return true;
    }
    return false;
}

pub fn isZai(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "zukijourney.com") or
        isHostOrSubdomainOf(base_url, "z.ai") or
        isHostOrSubdomainOf(base_url, "bigmodel.cn");
}

pub fn isOpenRouter(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "openrouter.ai");
}

pub fn isChutes(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "chutes.ai");
}

pub fn isQwen(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "dashscope.aliyuncs.com") or
        isHostOrSubdomainOf(base_url, "dashscope-intl.aliyuncs.com");
}

pub fn isDeepSeek(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "deepseek.com");
}

pub fn isExplicitDeepSeekVendor(vendor_id: []const u8) bool {
    return std.mem.eql(u8, vendor_id, "deepseek");
}

pub fn usesDeepSeekWire(vendor_id: []const u8, base_url: ?[]const u8) bool {
    return isExplicitDeepSeekVendor(vendor_id) or isDeepSeek(base_url);
}

pub fn deepSeekEffort(effort: []const u8) []const u8 {
    if (std.mem.eql(u8, effort, "minimal") or std.mem.eql(u8, effort, "low")) return "low";
    if (std.mem.eql(u8, effort, "xhigh") or std.mem.eql(u8, effort, "max") or std.mem.eql(u8, effort, "ultra")) return "max";
    return "high";
}

pub fn isOpenCodeGateway(vendor_id: []const u8) bool {
    return std.mem.eql(u8, vendor_id, "opencode-zen") or std.mem.eql(u8, vendor_id, "opencode-go");
}

pub fn openCodeEffort(model_id: []const u8, effort: []const u8) ?[]const u8 {
    if (std.mem.eql(u8, effort, "off") or std.mem.eql(u8, effort, "none")) return null;
    if (containsAny(model_id, &.{ "glm-5.2", "glm-5-2", "glm-5p2" })) return if (isTopEffort(effort)) "max" else "high";
    if (containsAny(model_id, &.{ "deepseek-chat", "deepseek-reasoner", "deepseek-r1", "deepseek-v3", "minimax", "glm", "kimi", "k2p", "qwen", "big-pickle" })) return null;
    if (containsAny(model_id, &.{"deepseek-v4"})) return deepSeekEffort(effort);
    if (std.mem.eql(u8, effort, "minimal") or std.mem.eql(u8, effort, "low")) return "low";
    if (std.mem.eql(u8, effort, "medium")) return "medium";
    return "high";
}

fn isTopEffort(effort: []const u8) bool {
    return std.mem.eql(u8, effort, "xhigh") or std.mem.eql(u8, effort, "max") or std.mem.eql(u8, effort, "ultra");
}

fn containsAny(haystack: []const u8, needles: []const []const u8) bool {
    for (needles) |needle| {
        if (std.ascii.indexOfIgnoreCase(haystack, needle) != null) return true;
    }
    return false;
}

fn hostIsOrSubdomainOf(host: []const u8, domain: []const u8) bool {
    return std.ascii.eqlIgnoreCase(host, domain) or
        (host.len > domain.len and std.ascii.eqlIgnoreCase(host[host.len - domain.len ..], domain) and host[host.len - domain.len - 1] == '.');
}

pub fn hostOf(base_url: ?[]const u8) ?[]const u8 {
    const url = base_url orelse return null;
    const uri = std.Uri.parse(url) catch return null;
    const host = uri.host orelse return null;
    return host.percent_encoded;
}

pub fn isHostOrSubdomainOf(base_url: ?[]const u8, domain: []const u8) bool {
    const host = hostOf(base_url) orelse return false;
    return hostIsOrSubdomainOf(host, domain);
}

const bedrock_first_labels = [_][]const u8{ "bedrock", "bedrock-runtime", "bedrock-fips", "bedrock-runtime-fips" };
const aws_parents = [_][]const u8{ "amazonaws.com", "amazonaws.com.cn" };

pub fn isBedrock(base_url: ?[]const u8) bool {
    const host = hostOf(base_url) orelse return false;
    const dot = std.mem.indexOfScalar(u8, host, '.') orelse return false;
    const label = host[0..dot];
    for (bedrock_first_labels) |candidate| {
        if (!std.ascii.eqlIgnoreCase(label, candidate)) continue;
        for (aws_parents) |parent| {
            if (hostIsOrSubdomainOf(host, parent)) return true;
        }
    }
    return false;
}

pub fn isOpenAIHost(base_url: []const u8) bool {
    return isHostOrSubdomainOf(base_url, "openai.com");
}

pub fn isAnthropic(base_url: ?[]const u8) bool {
    return isHostOrSubdomainOf(base_url, "anthropic.com");
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
    if (isGoogle(url)) return .google;
    if (isBedrock(url)) return .bedrock;
    if (isAzure(url)) return .azure;
    if (isOllama(url)) return .ollama;

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
                    caps.supports_reasoning_effort = true;
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

test "an ollama host is loopback on 11434 and nothing else" {
    const hosts = [_][]const u8{
        "http://127.0.0.1:11434",
        "http://127.0.0.1:11434/",
        "http://127.0.0.1:11434/api/chat",
        "http://localhost:11434",
        "http://localhost:11434/api/chat",
        "http://[::1]:11434",
        "http://LOCALHOST:11434",
    };
    for (hosts) |url| {
        try std.testing.expect(isOllama(url));
    }

    const not_hosts = [_][]const u8{
        "http://127.0.0.1:11435",
        "http://localhost:11435",
        "http://localhost",
        "http://127.0.0.1",
        "https://ollama.internal:11434",
        "https://my-ollama.example.com",
        "http://ollama.internal:11434",
        "https://ollama.example.com/v1",
        "http://example.com:11434/ollama",
        "http://notlocalhost:11434",
        "http://127.0.0.2:11434",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isOllama(url));
    }

    try std.testing.expect(!isOllama(null));
}

test "the catalogued ollama local default still detects as ollama" {
    const url = "http://127.0.0.1:11434";
    try std.testing.expect(isOllama(url));
    try std.testing.expectEqual(ProviderType.ollama, detectProviderType(url));
}

test "a bedrock host has bedrock or bedrock-runtime as its first label under amazonaws" {
    const hosts = [_][]const u8{
        "https://bedrock.us-east-1.amazonaws.com",
        "https://bedrock-runtime.us-east-1.amazonaws.com",
        "https://bedrock-runtime.us-east-1.amazonaws.com/model/x/invoke",
        "https://bedrock-fips.us-east-1.amazonaws.com",
        "https://bedrock-runtime-fips.us-east-1.amazonaws.com",
        "https://bedrock-runtime.cn-north-1.amazonaws.com.cn",
        "https://BEDROCK-RUNTIME.US-EAST-1.AMAZONAWS.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isBedrock(url));
    }

    const not_hosts = [_][]const u8{
        "https://amazonaws.com",
        "https://us-east-1.amazonaws.com",
        "https://s3.us-east-1.amazonaws.com",
        "https://mybedrock.us-east-1.amazonaws.com",
        "https://us-east-1.bedrock.amazonaws.com",
        "https://bedrock-runtime.amazonaws.com.evil.example",
        "https://bedrock.us-east-1.amazonaws.co",
        "https://evil.example/?next=bedrock-runtime.us-east-1.amazonaws.com",
        "https://evil.example/v1/bedrock.us-east-1.amazonaws.com",
        "https://gateway.example/proxy/bedrock-runtime.us-east-1.amazonaws.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isBedrock(url));
    }

    try std.testing.expect(!isBedrock(null));
}

test "a bedrock host still detects as bedrock" {
    const url = "https://bedrock-runtime.us-east-1.amazonaws.com";
    try std.testing.expect(isBedrock(url));
    try std.testing.expectEqual(ProviderType.bedrock, detectProviderType(url));
}

test "an azure host matches a label under azure.com and never azure.com itself" {
    const hosts = [_][]const u8{
        "https://contoso.openai.azure.com",
        "https://contoso.openai.azure.com/openai/deployments/gpt/chat/completions",
        "https://contoso.cognitiveservices.azure.com",
        "https://openai.azure.com",
        "https://CONTOSO.COGNITIVESERVICES.AZURE.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isAzure(url));
    }

    const not_hosts = [_][]const u8{
        "https://azure.com",
        "https://contoso.azure.com",
        "https://notopenai.azure.com",
        "https://notcognitiveservices.azure.com",
        "https://notservices.ai.azure.com",
        "https://contoso.services.ai.azure.com",
        "https://cognitiveservices.azure.com.evil.example",
        "https://services.ai.azure.com.evil.example",
        "https://openai.azure.com.evil.example",
        "https://evilcontoso.openai.azure.co",
        "https://evil.example/?next=contoso.cognitiveservices.azure.com",
        "https://evil.example/v1/contoso.services.ai.azure.com",
        "https://gateway.example/proxy/contoso.openai.azure.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isAzure(url));
    }

    try std.testing.expect(!isAzure(null));
}

test "each azure label is anchored on its own" {
    const others = [_][]const u8{
        "https://contoso.cognitiveservices.azure.com",
        "https://contoso.openai.azure.com",
    };
    for (others) |url| {
        var matched: usize = 0;
        if (isHostOrSubdomainOf(url, "openai.azure.com")) matched += 1;
        if (isHostOrSubdomainOf(url, "cognitiveservices.azure.com")) matched += 1;
        try std.testing.expectEqual(@as(usize, 1), matched);
    }
}

test "an azure openai host still detects as azure" {
    const url = "https://contoso.openai.azure.com";
    try std.testing.expect(isAzure(url));
    try std.testing.expectEqual(ProviderType.azure, detectProviderType(url));
}

test "a google host is the two api hosts or one regional aiplatform label" {
    const hosts = [_][]const u8{
        "https://generativelanguage.googleapis.com",
        "https://generativelanguage.googleapis.com/v1beta",
        "https://aiplatform.googleapis.com",
        "https://us-central1-aiplatform.googleapis.com",
        "https://europe-west4-aiplatform.googleapis.com/v1/projects/p/locations/l/publishers/google",
        "https://US-CENTRAL1-AIPLATFORM.GOOGLEAPIS.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isGoogle(url));
    }

    const not_hosts = [_][]const u8{
        "https://googleapis.com",
        "https://storage.googleapis.com",
        "https://notgenerativelanguage.googleapis.com",
        "https://foo.generativelanguage.googleapis.com.evil.com",
        "https://aiplatform.googleapis.com.evil.com",
        "https://foo.generativelanguage.googleapis.com",
        "https://x.aiplatform.googleapis.com",
        "https://foo.us-central1-aiplatform.googleapis.com",
        "https://notgenerativelanguage.googleapis.com.attacker.test",
        "https://evil-aiplatform.googleapis.com.attacker.test",
        "https://evil.example/?next=aiplatform.googleapis.com",
        "https://evil.example/v1/generativelanguage.googleapis.com",
        "https://gateway.example/proxy/aiplatform.googleapis.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isGoogle(url));
    }

    try std.testing.expect(!isGoogle(null));
}

test "the catalogued google row still detects as google with its caps" {
    const url = "https://generativelanguage.googleapis.com";
    try std.testing.expect(isGoogle(url));
    try std.testing.expectEqual(ProviderType.google, detectProviderType(url));
    const caps = detectCapabilities(url);
    try std.testing.expectEqual(ProviderType.google, caps.provider_type);
    try std.testing.expect(caps.vision);
    try std.testing.expect(caps.function_calling);
}

test "a zai host is z.ai, bigmodel.cn or zukijourney.com, or a subdomain of one" {
    const hosts = [_][]const u8{
        "https://api.z.ai/api/coding/paas/v4",
        "https://open.bigmodel.cn/api/coding/paas/v4",
        "https://api.zukijourney.com",
        "https://api.zukijourney.com/api/paas/v4",
        "https://zukijourney.com",
        "https://API.ZUKIJOURNEY.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isZai(url));
    }

    const not_hosts = [_][]const u8{
        "https://myzukijourney.com",
        "https://notz.ai",
        "https://z.ai.evil.example",
        "https://evil.example/?next=api.z.ai",
        "https://mybigmodel.cn",
        "https://zukijourney.com.evil.example",
        "https://evil.example/?next=api.zukijourney.com",
        "https://evil.example/v1/zai",
        "https://gateway.example/proxy/zai",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isZai(url));
    }

    try std.testing.expect(!isZai(null));
}

test "the Z.AI coding plan's bases take the zai thinking format and replay reasoning as reasoning_content" {
    for ([_][]const u8{ "https://api.z.ai/api/coding/paas/v4", "https://open.bigmodel.cn/api/coding/paas/v4" }) |url| {
        const caps = detectCapabilities(url);
        try std.testing.expectEqual(.zai, caps.thinking_format);
        try std.testing.expect(!caps.requires_thinking_as_text);
    }
}

test "a qwen host is dashscope.aliyuncs.com or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://dashscope.aliyuncs.com",
        "https://coding-intl.dashscope.aliyuncs.com",
        "https://coding-intl.dashscope.aliyuncs.com/v1",
        "https://DASHSCOPE.ALIYUNCS.COM",
        "https://dashscope-intl.aliyuncs.com",
        "https://dashscope-intl.aliyuncs.com/compatible-mode/v1",
    };
    for (hosts) |url| {
        try std.testing.expect(isQwen(url));
    }

    const not_hosts = [_][]const u8{
        "https://mydashscope.aliyuncs.com.attacker.example",
        "https://aliyuncs.com",
        "https://www.aliyuncs.com",
        "https://notdashscope.aliyuncs.com",
        "https://evil.example/?next=dashscope",
        "https://evil.example/v1/qwen",
        "https://gateway.example/proxy/dashscope.aliyuncs.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isQwen(url));
    }

    try std.testing.expect(!isQwen(null));
}

test "the catalogued alibaba row still gets the qwen thinking format" {
    const url = "https://coding-intl.dashscope.aliyuncs.com/v1";
    try std.testing.expect(isQwen(url));
    try std.testing.expectEqual(ProviderType.openai_compatible, detectProviderType(url));
    try std.testing.expectEqual(.qwen, detectCapabilities(url).thinking_format);
}

test "an anthropic host is anthropic.com or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.anthropic.com",
        "https://api.anthropic.com/v1",
        "https://anthropic.com",
        "https://API.ANTHROPIC.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isAnthropic(url));
    }

    const not_hosts = [_][]const u8{
        "https://myanthropic.com",
        "https://notanthropic.com",
        "https://anthropic.com.evil.example",
        "https://evil.example/?next=api.anthropic.com",
        "https://evil.example/v1/api.anthropic.com",
        "https://gateway.example/proxy/api.anthropic.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isAnthropic(url));
    }

    try std.testing.expect(!isAnthropic(null));
}

test "the catalogued anthropic row still gets the anthropic caps" {
    const url = "https://api.anthropic.com";
    try std.testing.expect(isAnthropic(url));
    try std.testing.expectEqual(ProviderType.anthropic, detectProviderType(url));
    const caps = detectCapabilities(url);
    try std.testing.expectEqual(ProviderType.anthropic, caps.provider_type);
    try std.testing.expect(caps.extended_thinking);
    try std.testing.expect(caps.prompt_caching);
    try std.testing.expect(caps.vision);
}

test "an openrouter host is openrouter.ai or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://openrouter.ai",
        "https://openrouter.ai/api/v1",
        "https://OPENROUTER.AI",
    };
    for (hosts) |url| {
        try std.testing.expect(isOpenRouter(url));
    }

    const not_hosts = [_][]const u8{
        "https://myopenrouter.ai",
        "https://notopenrouter.ai",
        "https://openrouter.ai.evil.example",
        "https://evil.example/?next=openrouter.ai",
        "https://evil.example/v1/openrouter.ai",
        "https://gateway.example/proxy/openrouter.ai",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isOpenRouter(url));
    }

    try std.testing.expect(!isOpenRouter(null));
}

test "the catalogued openrouter row still detects as openai compatible" {
    const url = "https://openrouter.ai/api/v1";
    try std.testing.expect(isOpenRouter(url));
    try std.testing.expectEqual(ProviderType.openai_compatible, detectProviderType(url));
}

test "a deepseek host is deepseek.com or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.deepseek.com",
        "https://api.deepseek.com/v1",
        "https://deepseek.com",
        "https://API.DEEPSEEK.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isDeepSeek(url));
    }

    const not_hosts = [_][]const u8{
        "https://mydeepseek.com",
        "https://notdeepseek.com",
        "https://deepseek.com.evil.example",
        "https://evil.example/?next=api.deepseek.com",
        "https://evil.example/v1/api.deepseek.com",
        "https://gateway.example/proxy/api.deepseek.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isDeepSeek(url));
    }

    try std.testing.expect(!isDeepSeek(null));
}

test "the catalogued deepseek row takes its reasoning back as reasoning, with an effort" {
    const url = "https://api.deepseek.com";
    try std.testing.expect(isDeepSeek(url));
    const caps = detectCapabilities(url);
    try std.testing.expectEqual(ProviderType.openai_compatible, caps.provider_type);
    try std.testing.expect(!caps.requires_thinking_as_text);
    try std.testing.expect(caps.supports_reasoning_effort);
}

test "a github copilot host is githubcopilot.com or a subdomain of it" {
    const hosts = [_][]const u8{
        "https://api.githubcopilot.com",
        "https://api.individual.githubcopilot.com",
        "https://api.acme.githubcopilot.com",
        "https://githubcopilot.com",
        "https://API.GITHUBCOPILOT.COM",
    };
    for (hosts) |url| {
        try std.testing.expect(isGitHubCopilot(url));
    }

    const not_hosts = [_][]const u8{
        "https://notgithubcopilot.com",
        "https://mygithubcopilot.com",
        "https://githubcopilot.com.attacker.test",
        "https://api.githubcopilot.com@attacker.test",
        "https://evil.example/?next=api.githubcopilot.com",
        "https://evil.example/v1/api.githubcopilot.com",
        "https://gateway.example/proxy/api.githubcopilot.com",
        "not a url at all",
        "",
    };
    for (not_hosts) |url| {
        try std.testing.expect(!isGitHubCopilot(url));
    }

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

test "an explicit deepseek vendor is recognised behind any host, and a lookalike host is not" {
    try std.testing.expect(usesDeepSeekWire("deepseek", "https://proxy.internal.example/v1"));
    try std.testing.expect(usesDeepSeekWire("deepseek", null));
    try std.testing.expect(usesDeepSeekWire("openai", "https://api.deepseek.com"));
    try std.testing.expect(usesDeepSeekWire("openai", "https://gateway.deepseek.com/v1"));
    try std.testing.expect(!usesDeepSeekWire("openai", "https://deepseek.com.evil.example/v1"));
    try std.testing.expect(!usesDeepSeekWire("openai", "https://proxy.internal.example/v1"));
    try std.testing.expect(!usesDeepSeekWire("deepseek-like", "https://api.deepseek.com.evil.example"));
}

test "the deepseek level table follows the published mapping" {
    try std.testing.expectEqualStrings("low", deepSeekEffort("minimal"));
    try std.testing.expectEqualStrings("low", deepSeekEffort("low"));
    try std.testing.expectEqualStrings("high", deepSeekEffort("medium"));
    try std.testing.expectEqualStrings("high", deepSeekEffort("high"));
    try std.testing.expectEqualStrings("max", deepSeekEffort("xhigh"));
    try std.testing.expectEqualStrings("max", deepSeekEffort("max"));
    try std.testing.expectEqualStrings("max", deepSeekEffort("ultra"));
}

test "opencode sends each model family the efforts opencode itself offers it" {
    try std.testing.expect(isOpenCodeGateway("opencode-zen"));
    try std.testing.expect(isOpenCodeGateway("opencode-go"));
    try std.testing.expect(!isOpenCodeGateway("deepseek"));
    try std.testing.expectEqualStrings("low", openCodeEffort("deepseek-v4.1-flash", "minimal").?);
    try std.testing.expectEqualStrings("high", openCodeEffort("deepseek-v4.1-flash", "medium").?);
    try std.testing.expectEqualStrings("max", openCodeEffort("deepseek-v4-pro", "xhigh").?);
    try std.testing.expectEqualStrings("high", openCodeEffort("glm-5.2", "medium").?);
    try std.testing.expectEqualStrings("max", openCodeEffort("glm-5.2", "xhigh").?);
    try std.testing.expectEqualStrings("medium", openCodeEffort("grok-4.7", "medium").?);
    try std.testing.expectEqualStrings("high", openCodeEffort("gpt-6-luna", "xhigh").?);
    try std.testing.expectEqualStrings("low", openCodeEffort("mimo-v2.6-pro", "minimal").?);
    try std.testing.expect(openCodeEffort("deepseek-v4.1-flash", "off") == null);
    try std.testing.expect(openCodeEffort("glm-5.3", "high") == null);
    try std.testing.expect(openCodeEffort("kimi-k3", "high") == null);
    try std.testing.expect(openCodeEffort("qwen3.8-flash", "high") == null);
    try std.testing.expect(openCodeEffort("minimax-m3", "high") == null);
    try std.testing.expect(openCodeEffort("deepseek-v3.2", "high") == null);
}

test "an unrecognised effort keeps the pre-existing fallback rather than becoming an empty value" {
    try std.testing.expectEqualStrings("high", deepSeekEffort("nonsense"));
    try std.testing.expectEqualStrings("high", deepSeekEffort(""));
    try std.testing.expectEqualStrings("high", deepSeekEffort("off"));
    try std.testing.expectEqualStrings("high", deepSeekEffort("none"));
}

