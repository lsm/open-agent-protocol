const std = @import("std");
const jsonschema = @import("jsonschema");
const packs_mod = @import("packs");
const build_options = @import("build_options");

const judged_floor = 514;

const unhandled_pack_composition = [_][]const u8{
    "ext-pack-cross-ref-declared",
    "ext-descriptor-member-advertised",
    "ext-member-on-event-unadvertised",
};

fn isUnhandled(id: []const u8) bool {
    for (unhandled_pack_composition) |name| {
        if (std.mem.eql(u8, name, id)) return true;
    }
    return false;
}

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
    var undecodable: usize = 0;
    var unhandled: usize = 0;
    var disagreements = std.ArrayList(u8).empty;
    defer disagreements.deinit(allocator);

    for (manifest.value.object.get("fixtures").?.array.items) |raw| {
        const entry = raw.object;
        const kind = entry.get("kind").?.string;
        const phase = if (entry.get("phase")) |p| p.string else "";
        var pack_dirs = std.ArrayList([]const u8).empty;
        defer {
            for (pack_dirs.items) |d| allocator.free(d);
            pack_dirs.deinit(allocator);
        }
        if (entry.get("packs")) |declared| {
            for (declared.array.items) |name| {
                try pack_dirs.append(allocator, try std.fs.path.join(allocator, &.{ root, "fixtures", name.string }));
            }
        }
        if (isUnhandled(entry.get("id").?.string)) {
            unhandled += 1;
            continue;
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
        const full = try std.fs.path.join(allocator, &.{ root, "fixtures", relative });
        defer allocator.free(full);

        const bytes = readAll(allocator, full) catch |err| {
            std.debug.print("unreadable fixture {s} at {s}: {s}\n", .{ entry.get("id").?.string, full, @errorName(err) });
            return error.FixtureUnreadable;
        };
        defer allocator.free(bytes);
        var trace = std.json.parseFromSlice(std.json.Value, allocator, bytes, .{}) catch {
            undecodable += 1;
            continue;
        };
        defer trace.deinit();

        const envelopes = switch (trace.value) {
            .array => |a| a.items,
            .object => |o| if (o.get("events")) |e| e.array.items else &[_]std.json.Value{trace.value},
            else => continue,
        };

        var loaded = packs_mod.load(allocator, &registry, pack_dirs.items) catch {
            skipped_packs += 1;
            continue;
        };
        defer loaded.deinit();

        var refs = std.ArrayList([]const u8).empty;
        defer refs.deinit(allocator);
        for (loaded.branches) |branch| try refs.append(allocator, branch.ref);

        var validator = jsonschema.Validator.init(allocator, &registry);
        defer validator.deinit();

        var saw_failure = false;
        var gave_up = false;
        for (envelopes) |envelope| {
            const result = validator.validateWithBranches(documentFor(profile), envelope, refs.items) catch {
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

    std.debug.print("\njudged={d} skipped_packs={d} skipped_tolerant={d} unsupported={d} undecodable={d} unhandled={d}\n", .{ judged, skipped_packs, skipped_tolerant, unsupported, undecodable, unhandled });
    if (disagreements.items.len > 0) {
        std.debug.print("disagreements:\n{s}", .{disagreements.items});
        return error.SchemaPhaseDisagrees;
    }
    if (unsupported != 0) return error.InterpreterRefusedAFixtureItOnceJudged;
    if (undecodable != 0) return error.FixtureStoppedDecoding;
    try std.testing.expect(judged >= judged_floor);
    try std.testing.expectEqual(unhandled_pack_composition.len, unhandled);
}
