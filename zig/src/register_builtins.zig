const std = @import("std");
const api_registry = @import("api_registry");
const provider_catalog = @import("provider_catalog");
const anthropic_api = @import("anthropic_messages_api");
const openai_completions_api = @import("openai_completions_api");
const openai_responses_api = @import("openai_responses_api");
const azure_api = @import("azure_openai_responses_api");
const google_api = @import("google_generative_api");
const ollama_api = @import("ollama_api");

pub fn registerBuiltInApiProviders(registry: *api_registry.ApiRegistry) !void {
    try anthropic_api.registerAnthropicMessagesApiProvider(registry);
    try openai_completions_api.registerOpenAICompletionsApiProvider(registry);
    try openai_responses_api.registerOpenAIResponsesApiProvider(registry);
    try azure_api.registerAzureOpenAIResponsesApiProvider(registry);
    try openai_responses_api.registerOpenAICodexResponsesApiProvider(registry);
    try google_api.registerGoogleGenerativeApiProvider(registry);
    try google_api.registerGoogleGeminiCliApiProvider(registry);
    try ollama_api.registerOllamaApiProvider(registry);
}

test "registerBuiltInApiProviders registers expected api providers" {
    var registry = api_registry.ApiRegistry.init(std.testing.allocator);
    defer registry.deinit();

    try registerBuiltInApiProviders(&registry);

    try std.testing.expect(registry.getApiProvider("anthropic-messages") != null);
    try std.testing.expect(registry.getApiProvider("openai-completions") != null);
    try std.testing.expect(registry.getApiProvider("openai-responses") != null);
    try std.testing.expect(registry.getApiProvider("azure-openai-responses") != null);
    try std.testing.expect(registry.getApiProvider("openai-codex-responses") != null);
    try std.testing.expect(registry.getApiProvider("google-generative-ai") != null);
    try std.testing.expect(registry.getApiProvider("google-gemini-cli") != null);
    try std.testing.expect(registry.getApiProvider("ollama") != null);
}

test "every wire in the catalog is claimed by exactly one wire module" {
    const claims = [_][]const []const u8{
        anthropic_api.wires,
        openai_completions_api.wires,
        openai_responses_api.wires,
        ollama_api.wires,
    };

    for (provider_catalog.wire_paths) |wire| {
        var claimants: usize = 0;
        for (claims) |claim| {
            for (claim) |claimed| {
                if (!std.mem.eql(u8, claimed, wire.id)) continue;
                claimants += 1;
                const found = provider_catalog.wirePath(claimed) orelse return error.TestWireClaimNotInCatalog;
                try std.testing.expectEqualStrings(wire.suffix, found.suffix);
            }
        }
        if (wire.model_scoped) {
            try std.testing.expectEqual(@as(usize, 0), claimants);
        } else {
            try std.testing.expectEqual(@as(usize, 1), claimants);
        }
    }
}

test "no wire module claims a wire the catalog does not hold" {
    const claims = [_][]const []const u8{
        anthropic_api.wires,
        openai_completions_api.wires,
        openai_responses_api.wires,
        ollama_api.wires,
    };
    for (claims) |claim| {
        for (claim) |claimed| {
            if (provider_catalog.wirePath(claimed) == null) return error.TestWireClaimNotInCatalog;
        }
    }
}

test "the request url a wire module builds matches the url the catalog pins" {
    for (provider_catalog.pinned) |row| {
        const wire = provider_catalog.wirePath(row.wire) orelse return error.TestWireClaimNotInCatalog;
        if (wire.model_scoped) {
            try std.testing.expect(row.request_url == null);
            continue;
        }
        const built = try provider_catalog.joinUrlOwned(std.testing.allocator, row.base_url, wire);
        defer std.testing.allocator.free(built);
        try std.testing.expectEqualStrings(row.request_url.?, built);
    }
}
