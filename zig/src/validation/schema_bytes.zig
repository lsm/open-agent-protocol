const std = @import("std");

pub const action = @embedFile("schema_action");
pub const auth = @embedFile("schema_auth");
pub const capabilities = @embedFile("schema_capabilities");
pub const common = @embedFile("schema_common");
pub const envelope = @embedFile("schema_envelope");
pub const inference = @embedFile("schema_inference");
pub const interaction = @embedFile("schema_interaction");
pub const manifest = @embedFile("schema_manifest");
pub const pack = @embedFile("schema_pack");
pub const provider_envelope = @embedFile("schema_provider_envelope");
pub const provider = @embedFile("schema_provider");
pub const run = @embedFile("schema_run");
pub const session = @embedFile("schema_session");

pub const Entry = struct {
    name: []const u8,
    bytes: []const u8,
};

pub const all = [_]Entry{
    .{ .name = "action.schema.json", .bytes = action },
    .{ .name = "auth.schema.json", .bytes = auth },
    .{ .name = "capabilities.schema.json", .bytes = capabilities },
    .{ .name = "common.schema.json", .bytes = common },
    .{ .name = "envelope.schema.json", .bytes = envelope },
    .{ .name = "inference.schema.json", .bytes = inference },
    .{ .name = "interaction.schema.json", .bytes = interaction },
    .{ .name = "manifest.schema.json", .bytes = manifest },
    .{ .name = "pack.schema.json", .bytes = pack },
    .{ .name = "provider-envelope.schema.json", .bytes = provider_envelope },
    .{ .name = "provider.schema.json", .bytes = provider },
    .{ .name = "run.schema.json", .bytes = run },
    .{ .name = "session.schema.json", .bytes = session },
};

test "every bundled schema is embedded, non-empty and parses" {
    try std.testing.expectEqual(@as(usize, 13), all.len);
    for (all) |entry| {
        try std.testing.expect(entry.bytes.len > 0);
        var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, entry.bytes, .{});
        defer parsed.deinit();
        try std.testing.expect(parsed.value == .object);
        const id = parsed.value.object.get("$id") orelse return error.SchemaHasNoId;
        try std.testing.expectEqualStrings(entry.name, id.string);
    }
}
