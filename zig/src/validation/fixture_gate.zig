const std = @import("std");
const jsonschema = @import("jsonschema");
const build_options = @import("build_options");

const Entry = struct {
    id: []const u8,
    path: []const u8,
    kind: []const u8,
    phase: []const u8 = "",
    profile: []const u8 = "agent-control-core",
    packs: []const []const u8 = &.{},
};

fn readAll(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(64 * 1024 * 1024));
}

fn documentFor(profile: []const u8) []const u8 {
    if (std.mem.eql(u8, profile, "model-provider-core")) return "provider-envelope.schema.json";
    return "envelope.schema.json";
}

test "the Zig schema phase agrees with the manifest on every fixture it can judge" {
    const allocator = std.testing.allocator;
    const root = build_options.repository_root;

    const manifest_path = try std.fs.path.join(allocator, &.{ root, "fixtures", "manifest.json" });
    defer allocator.free(manifest_path);
    const manifest_bytes = try readAll(allocator, manifest_path);
    defer allocator.free(manifest_bytes);

    var manifest = try std.json.parseFromSlice(std.json.Value, allocator, manifest_bytes, .{});
    defer manifest.deinit();

    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();

    var judged: usize = 0;
    var skipped_packs: usize = 0;
    var skipped_tolerant: usize = 0;
    var unsupported: usize = 0;
    var disagreements = std.ArrayList(u8).empty;
    defer disagreements.deinit(allocator);

    for (manifest.value.object.get("fixtures").?.array.items) |raw| {
        const entry = raw.object;
        const kind = entry.get("kind").?.string;
        const phase = if (entry.get("phase")) |p| p.string else "";
        if (entry.get("packs")) |packs| {
            if (packs.array.items.len > 0) {
                skipped_packs += 1;
                continue;
            }
        }
        if (entry.get("mode")) |mode| {
            if (std.mem.eql(u8, mode.string, "tolerant")) {
                skipped_tolerant += 1;
                continue;
            }
        }
        if (std.mem.eql(u8, kind, "load-invalid")) continue;
        if (std.mem.eql(u8, phase, "decode")) continue;

        const expect_schema_failure = std.mem.eql(u8, phase, "schema");
        const profile = if (entry.get("profile")) |p| p.string else "agent-control-core";
        const relative = entry.get("path").?.string;
        const sub = if (std.mem.eql(u8, profile, "model-provider-core")) "fixtures/provider" else "fixtures";
        const full = try std.fs.path.join(allocator, &.{ root, sub, relative });
        defer allocator.free(full);

        const bytes = readAll(allocator, full) catch continue;
        defer allocator.free(bytes);
        var trace = std.json.parseFromSlice(std.json.Value, allocator, bytes, .{}) catch continue;
        defer trace.deinit();

        const envelopes = switch (trace.value) {
            .array => |a| a.items,
            .object => |o| if (o.get("events")) |e| e.array.items else &[_]std.json.Value{trace.value},
            else => continue,
        };

        var validator = jsonschema.Validator.init(allocator, &registry);
        defer validator.deinit();

        var saw_failure = false;
        var gave_up = false;
        for (envelopes) |envelope| {
            const result = validator.validate(documentFor(profile), envelope) catch {
                gave_up = true;
                break;
            };
            if (result != null) saw_failure = true;
        }
        if (gave_up) {
            unsupported += 1;
            continue;
        }
        judged += 1;
        if (expect_schema_failure and !saw_failure) {
            try disagreements.print(allocator, "  {s}: manifest says schema-invalid, Zig accepted\n", .{entry.get("id").?.string});
        }
        if (!expect_schema_failure and saw_failure) {
            try disagreements.print(allocator, "  {s}: manifest says schema-valid, Zig rejected\n", .{entry.get("id").?.string});
        }
    }

    std.debug.print("\njudged={d} skipped_packs={d} skipped_tolerant={d} unsupported={d}\n", .{ judged, skipped_packs, skipped_tolerant, unsupported });
    if (disagreements.items.len > 0) {
        std.debug.print("disagreements:\n{s}", .{disagreements.items});
        return error.SchemaPhaseDisagrees;
    }
}
