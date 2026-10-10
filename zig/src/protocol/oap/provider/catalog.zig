const std = @import("std");
const provider_catalog = @import("provider_catalog");

const types = @import("oap_provider_types");
const ai_types = @import("ai_types");

pub const OAPX_API_NAMES = [_][]const u8{
    "anthropic-messages",
    "openai-completions",
    "openai-responses",
    "azure-openai-responses",
    "openai-codex-responses",
    "google-generative-ai",
    "google-gemini-cli",
    "ollama",
};

pub const WireMapping = struct {
    wire: types.Wire,
    framing: types.Framing,
    wire_id: ?[]const u8 = null,
};

pub fn mapApiToWire(api: []const u8) ?WireMapping {
    if (std.mem.eql(u8, api, "anthropic-messages")) {
        return .{ .wire = .@"anthropic-messages", .framing = .sse };
    }
    if (std.mem.eql(u8, api, "openai-completions")) {
        return .{ .wire = .@"openai-chat-completions", .framing = .sse };
    }
    if (std.mem.eql(u8, api, "openai-responses")) {
        return .{ .wire = .@"openai-responses", .framing = .sse };
    }
    if (std.mem.eql(u8, api, "azure-openai-responses")) {
        return .{ .wire = .@"openai-responses", .framing = .sse };
    }
    if (std.mem.eql(u8, api, "openai-codex-responses")) {
        return .{ .wire = .@"openai-responses", .framing = .sse };
    }
    if (std.mem.eql(u8, api, "google-generative-ai")) {
        return .{ .wire = .other, .framing = .sse, .wire_id = "google-generative-ai" };
    }
    if (std.mem.eql(u8, api, "google-gemini-cli")) {
        return .{ .wire = .other, .framing = .sse, .wire_id = "google-gemini-cli" };
    }
    if (std.mem.eql(u8, api, "ollama")) {
        return .{ .wire = .other, .framing = .ndjson, .wire_id = "ollama-chat" };
    }
    return null;
}

pub fn apiForWire(wire: types.Wire, wire_id: ?[]const u8) ?[]const u8 {
    return switch (wire) {
        .@"anthropic-messages" => "anthropic-messages",
        .@"openai-chat-completions" => "openai-completions",
        .@"openai-responses" => "openai-responses",
        .other => {
            const id = wire_id orelse return null;
            for (OAPX_API_NAMES) |api| {
                const mapping = mapApiToWire(api) orelse continue;
                if (mapping.wire == .other and std.mem.eql(u8, mapping.wire_id.?, id)) return api;
            }
            return null;
        },
    };
}

fn isExpressible(api: []const u8) bool {
    return mapApiToWire(api) != null;
}

pub fn hasNamedWire(api: []const u8) bool {
    const mapping = mapApiToWire(api) orelse return false;
    return mapping.wire.isNamed();
}

fn unnamedWireApis(buffer: [][]const u8) [][]const u8 {
    var count: usize = 0;
    for (OAPX_API_NAMES) |api| {
        if (hasNamedWire(api)) continue;
        buffer[count] = api;
        count += 1;
    }
    return buffer[0..count];
}

test "every registered api's wire leads back to an api on that same wire" {
    for (OAPX_API_NAMES) |api| {
        const mapping = mapApiToWire(api).?;
        const back = apiForWire(mapping.wire, mapping.wire_id) orelse return error.WireHasNoApi;
        const again = mapApiToWire(back).?;
        try std.testing.expectEqual(mapping.wire, again.wire);
        if (mapping.wire_id) |id| try std.testing.expectEqualStrings(id, again.wire_id.?);
    }
    try std.testing.expectEqualStrings("ollama", apiForWire(.other, "ollama-chat").?);
    try std.testing.expect(apiForWire(.other, "no-such-wire") == null);
    try std.testing.expect(apiForWire(.other, null) == null);
}

test "every registered api is describable and five earn a named wire" {
    var named: usize = 0;
    for (OAPX_API_NAMES) |api| {
        try std.testing.expect(isExpressible(api));
        if (hasNamedWire(api)) named += 1;
    }
    try std.testing.expectEqual(@as(usize, 5), named);
}

test "the three that name no wire are the ones no second implementer speaks" {
    var buffer: [OAPX_API_NAMES.len][]const u8 = undefined;
    const unnamed = unnamedWireApis(&buffer);

    try std.testing.expectEqual(@as(usize, 3), unnamed.len);
    try std.testing.expectEqualStrings("google-generative-ai", unnamed[0]);
    try std.testing.expectEqualStrings("google-gemini-cli", unnamed[1]);
    try std.testing.expectEqualStrings("ollama", unnamed[2]);
}

test "two endpoints share the responses wire and are told apart by provider" {
    const azure = mapApiToWire("azure-openai-responses").?;
    const codex = mapApiToWire("openai-codex-responses").?;
    const native = mapApiToWire("openai-responses").?;

    try std.testing.expectEqual(types.Wire.@"openai-responses", azure.wire);
    try std.testing.expectEqual(types.Wire.@"openai-responses", codex.wire);
    try std.testing.expectEqual(types.Wire.@"openai-responses", native.wire);
}

test "ndjson is reachable now that an unnamed wire can carry it" {
    var ndjson_sources: usize = 0;
    for (OAPX_API_NAMES) |api| {
        const mapping = mapApiToWire(api) orelse continue;
        if (mapping.framing == .ndjson) ndjson_sources += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), ndjson_sources);

    const ollama = mapApiToWire("ollama").?;
    try std.testing.expectEqual(types.Framing.ndjson, ollama.framing);
    try std.testing.expect(!ollama.wire.isNamed());
}

test "a named wire is never invented for a shape only its originator speaks" {
    try std.testing.expect(!hasNamedWire("ollama"));
    try std.testing.expect(!hasNamedWire("google-generative-ai"));
    try std.testing.expect(hasNamedWire("anthropic-messages"));
}

pub fn sameWire(left: WireMapping, right: WireMapping) bool {
    if (left.wire != right.wire) return false;
    const left_id = left.wire_id orelse return right.wire_id == null;
    const right_id = right.wire_id orelse return false;
    return std.mem.eql(u8, left_id, right_id);
}

pub fn ownedModelEntriesForWire(
    allocator: std.mem.Allocator,
    models: []const ai_types.Model,
    provider_id: []const u8,
    wanted: WireMapping,
    source: types.ModelSource,
) ![]types.ModelEntry {
    var entries = std.ArrayList(types.ModelEntry).empty;
    errdefer {
        for (entries.items) |*entry| entry.deinit(allocator);
        entries.deinit(allocator);
    }
    for (models) |model| {
        if (!std.mem.eql(u8, model.provider, provider_id)) continue;
        const mapping = mapApiToWire(model.api) orelse continue;
        if (!sameWire(mapping, wanted)) continue;
        var entry = try ownedModelEntry(allocator, provider_id, model, mapping, source);
        var entry_owned = true;
        defer if (entry_owned) entry.deinit(allocator);
        try entries.append(allocator, entry);
        entry_owned = false;
    }
    return entries.toOwnedSlice(allocator);
}

pub fn ownedModelEntriesForRow(
    allocator: std.mem.Allocator,
    models: []const ai_types.Model,
    provider_id: []const u8,
    wire: ?types.Wire,
    source: types.ModelSource,
) ![]types.ModelEntry {
    var entries = std.ArrayList(types.ModelEntry).empty;
    errdefer {
        for (entries.items) |*entry| entry.deinit(allocator);
        entries.deinit(allocator);
    }
    for (models) |model| {
        if (!std.mem.eql(u8, model.provider, provider_id)) continue;
        const mapping = mapApiToWire(model.api) orelse continue;
        if (wire) |wanted| {
            if (mapping.wire != wanted) continue;
        }
        var entry = try ownedModelEntry(allocator, provider_id, model, mapping, source);
        var entry_owned = true;
        defer if (entry_owned) entry.deinit(allocator);
        try entries.append(allocator, entry);
        entry_owned = false;
    }
    return entries.toOwnedSlice(allocator);
}

pub fn freeModelEntries(allocator: std.mem.Allocator, entries: []types.ModelEntry) void {
    for (entries) |*entry| entry.deinit(allocator);
    allocator.free(entries);
}

fn ownedModelEntry(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    model: ai_types.Model,
    mapping: WireMapping,
    source: types.ModelSource,
) !types.ModelEntry {
    const model_ref = try buildModelRef(allocator, provider_id, mapping.wire, mapping.wire_id, model.id);
    errdefer allocator.free(model_ref);
    const model_id = try allocator.dupe(u8, model.id);
    errdefer allocator.free(model_id);
    const display_name = if (model.name.len > 0) try allocator.dupe(u8, model.name) else null;
    errdefer if (display_name) |value| allocator.free(value);
    const owned_provider = try allocator.dupe(u8, provider_id);
    errdefer allocator.free(owned_provider);
    const capabilities = try ownedCapabilities(allocator, model);
    errdefer allocator.free(capabilities);
    const input_modalities = try ownedModalities(allocator, model.input);
    errdefer allocator.free(input_modalities);
    const release_date = if (model.release_date) |value| try allocator.dupe(u8, value) else null;
    errdefer if (release_date) |value| allocator.free(value);
    const family = if (model.family) |value| try allocator.dupe(u8, value) else null;

    return .{
        .model_ref = model_ref,
        .model_id = model_id,
        .display_name = display_name,
        .provider_id = owned_provider,
        .wire = mapping.wire,
        .context_window = if (model.context_window > 0) model.context_window else null,
        .max_output_tokens = if (model.max_tokens > 0) model.max_tokens else null,
        .capabilities = capabilities,
        .source = source,
        .input_modalities = input_modalities,
        .cost = if (model.published_cost) |cost| .{ .input = cost.input, .output = cost.output, .cache_read = cost.cache_read, .cache_write = cost.cache_write } else null,
        .release_date = release_date,
        .family = family,
    };
}

fn ownedCapabilities(allocator: std.mem.Allocator, model: ai_types.Model) ![]const types.ModelCapability {
    var list = std.ArrayList(types.ModelCapability).empty;
    errdefer list.deinit(allocator);
    for (model.input) |name| {
        const capability: ?types.ModelCapability = if (std.mem.eql(u8, name, "image"))
            .vision
        else if (std.mem.eql(u8, name, "audio"))
            .audio_input
        else
            null;
        if (capability) |value| try list.append(allocator, value);
    }
    if (model.reasoning) try list.append(allocator, .reasoning);
    return list.toOwnedSlice(allocator);
}

fn ownedModalities(allocator: std.mem.Allocator, input: []const []const u8) ![]const types.Modality {
    var list = std.ArrayList(types.Modality).empty;
    errdefer list.deinit(allocator);
    for (input) |name| {
        const modality = std.meta.stringToEnum(types.Modality, name) orelse continue;
        try list.append(allocator, modality);
    }
    return list.toOwnedSlice(allocator);
}

pub fn buildModelRef(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    wire: types.Wire,
    wire_id: ?[]const u8,
    model_id: []const u8,
) ![]const u8 {
    if (wire_id) |id| {
        return std.fmt.allocPrint(
            allocator,
            "{s}/{s}:{s}@{s}",
            .{ provider_id, wire.toString(), id, model_id },
        );
    }
    return std.fmt.allocPrint(allocator, "{s}/{s}@{s}", .{ provider_id, wire.toString(), model_id });
}

test "a row serves every model the snapshot names for it, each publishing the facts it was loaded with" {
    const allocator = std.testing.allocator;
    const models = [_]ai_types.Model{
        .{
            .id = "claude-sonnet-4-5",
            .name = "Claude Sonnet 4.5",
            .api = "anthropic-messages",
            .provider = "anthropic",
            .base_url = "",
            .reasoning = true,
            .input = &.{ "text", "image" },
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 200_000,
            .max_tokens = 8_192,
            .published_cost = .{ .input = 3, .output = 15 },
            .release_date = "2025-09-29",
            .family = "claude-sonnet",
        },
        .{
            .id = "claude-opus-4-1",
            .name = "Claude Opus 4.1",
            .api = "anthropic-messages",
            .provider = "anthropic",
            .base_url = "",
            .reasoning = false,
            .input = &.{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 200_000,
            .max_tokens = 8_192,
        },
        .{
            .id = "gpt-4o",
            .name = "GPT-4o",
            .api = "openai-responses",
            .provider = "openai",
            .base_url = "",
            .reasoning = false,
            .input = &.{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 128_000,
            .max_tokens = 16_384,
        },
    };

    const entries = try ownedModelEntriesForRow(
        allocator,
        &models,
        "anthropic",
        types.Wire.@"anthropic-messages",
        .discovered,
    );
    defer freeModelEntries(allocator, entries);

    try std.testing.expectEqual(@as(usize, 2), entries.len);
    try std.testing.expectEqualStrings("claude-sonnet-4-5", entries[0].model_id);
    try std.testing.expectEqualStrings("claude-opus-4-1", entries[1].model_id);
    try std.testing.expectEqualStrings("anthropic", entries[0].provider_id);
    try std.testing.expectEqualStrings("anthropic/anthropic-messages@claude-sonnet-4-5", entries[0].model_ref);
    try std.testing.expectEqual(@as(u32, 200_000), entries[0].context_window.?);
    try std.testing.expectEqual(@as(usize, 2), entries[0].input_modalities.len);
    try std.testing.expectEqual(types.Modality.text, entries[0].input_modalities[0]);
    try std.testing.expectEqual(types.ModelSource.discovered, entries[0].source);
    try std.testing.expectEqual(@as(usize, 2), entries[0].capabilities.len);
    try std.testing.expectEqual(types.ModelCapability.vision, entries[0].capabilities[0]);
    try std.testing.expectEqual(types.ModelCapability.reasoning, entries[0].capabilities[1]);
    try std.testing.expectEqual(@as(usize, 0), entries[1].capabilities.len);
    const cost = entries[0].cost orelse return error.TestExpectedCost;
    try std.testing.expectEqual(@as(?f64, 3), cost.input);
    try std.testing.expectEqual(@as(?f64, 15), cost.output);
    try std.testing.expectEqual(@as(?f64, null), cost.cache_read);
    try std.testing.expectEqualStrings("2025-09-29", entries[0].release_date.?);
    try std.testing.expectEqualStrings("claude-sonnet", entries[0].family.?);
    try std.testing.expect(entries[1].cost == null);
    try std.testing.expect(entries[1].release_date == null);
    try std.testing.expect(entries[1].family == null);
}

test "a row the snapshot names no model for serves nothing" {
    const allocator = std.testing.allocator;
    const models = [_]ai_types.Model{.{
        .id = "gpt-4o",
        .name = "GPT-4o",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 16_384,
    }};

    const entries = try ownedModelEntriesForRow(allocator, &models, "anthropic", null, .discovered);
    defer freeModelEntries(allocator, entries);

    try std.testing.expectEqual(@as(usize, 0), entries.len);
}

test "a row is served only the models its own wire can carry" {
    const allocator = std.testing.allocator;
    const models = [_]ai_types.Model{
        .{
            .id = "gpt-4o",
            .name = "GPT-4o",
            .api = "openai-responses",
            .provider = "openai",
            .base_url = "",
            .reasoning = false,
            .input = &.{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 128_000,
            .max_tokens = 16_384,
        },
        .{
            .id = "gpt-4o-mini",
            .name = "GPT-4o mini",
            .api = "openai-completions",
            .provider = "openai",
            .base_url = "",
            .reasoning = false,
            .input = &.{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 128_000,
            .max_tokens = 16_384,
        },
    };

    const entries = try ownedModelEntriesForRow(allocator, &models, "openai", types.Wire.@"openai-responses", .discovered);
    defer freeModelEntries(allocator, entries);

    try std.testing.expectEqual(@as(usize, 1), entries.len);
    try std.testing.expectEqualStrings("gpt-4o", entries[0].model_id);
    try std.testing.expectEqual(types.Wire.@"openai-responses", entries[0].wire);
}

fn ownedEntriesProbe(allocator: std.mem.Allocator) !void {
    const models = [_]ai_types.Model{.{
        .id = "claude-sonnet-4-5",
        .name = "Claude Sonnet 4.5",
        .api = "anthropic-messages",
        .provider = "anthropic",
        .base_url = "",
        .reasoning = true,
        .input = &.{ "text", "image" },
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 8_192,
        .published_cost = .{ .input = 3, .output = 15 },
        .release_date = "2025-09-29",
        .family = "claude-sonnet",
    }};
    const entries = try ownedModelEntriesForRow(allocator, &models, "anthropic", types.Wire.@"anthropic-messages", .discovered);
    freeModelEntries(allocator, entries);
}

test "the owned entries free everything when one allocation fails midway" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, ownedEntriesProbe, .{});
}

test "an unnamed wire carries an opaque discriminator in the reference" {
    const allocator = std.testing.allocator;
    const mapping = mapApiToWire("ollama").?;
    const model_ref = try buildModelRef(allocator, "ollama", mapping.wire, mapping.wire_id, "llama3");
    defer allocator.free(model_ref);
    try std.testing.expectEqualStrings("ollama/other:ollama-chat@llama3", model_ref);

    const parsed = types.parseModelRef(model_ref).?;
    try std.testing.expectEqual(types.Wire.other, parsed.wire);
    try std.testing.expectEqualStrings("ollama-chat", parsed.wire_id.?);
    try std.testing.expectEqualStrings("ollama", parsed.provider_id);
    try std.testing.expectEqualStrings("llama3", parsed.model_id);
}

test "two unnamed wires on one provider stay distinguishable" {
    const allocator = std.testing.allocator;
    const generative = mapApiToWire("google-generative-ai").?;
    const cli = mapApiToWire("google-gemini-cli").?;

    const a = try buildModelRef(allocator, "google", generative.wire, generative.wire_id, "gemini");
    defer allocator.free(a);
    const b = try buildModelRef(allocator, "google", cli.wire, cli.wire_id, "gemini");
    defer allocator.free(b);

    try std.testing.expect(!std.mem.eql(u8, a, b));
    try std.testing.expectEqualStrings("google-generative-ai", types.parseModelRef(a).?.wire_id.?);
    try std.testing.expectEqualStrings("google-gemini-cli", types.parseModelRef(b).?.wire_id.?);
}

test "a named wire carries no discriminator and parses without one" {
    const allocator = std.testing.allocator;
    const mapping = mapApiToWire("anthropic-messages").?;
    try std.testing.expect(mapping.wire_id == null);

    const model_ref = try buildModelRef(allocator, "anthropic", mapping.wire, mapping.wire_id, "claude");
    defer allocator.free(model_ref);
    try std.testing.expectEqualStrings("anthropic/anthropic-messages@claude", model_ref);

    const parsed = types.parseModelRef(model_ref).?;
    try std.testing.expectEqual(types.Wire.@"anthropic-messages", parsed.wire);
    try std.testing.expect(parsed.wire_id == null);
    try std.testing.expectEqualStrings("anthropic", parsed.provider_id);
    try std.testing.expectEqualStrings("claude", parsed.model_id);
}
