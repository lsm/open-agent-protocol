const std = @import("std");
const jsonschema = @import("jsonschema");
const packs_mod = @import("packs");
const tolerate = @import("tolerate");
const build_options = @import("build_options");

const judged_floor = 520;
const tolerant_fixtures = 3;

const unhandled_pack_composition = [_][]const u8{
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
    var tolerant_judged: usize = 0;
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
        const tolerant = if (entry.get("mode")) |mode| std.mem.eql(u8, mode.string, "tolerant") else false;
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

        var override_arena = std.heap.ArenaAllocator.init(allocator);
        defer override_arena.deinit();

        var validator = jsonschema.Validator.init(allocator, &registry);
        defer validator.deinit();

        if (tolerant) {
            for (registry.documents.keys()) |name| {
                if (tolerate.isMetaSchema(name)) continue;
                const tolerated = try tolerate.document(override_arena.allocator(), registry.root(name).?);
                try validator.overrides.put(allocator, name, tolerated);
            }
        }

        for (loaded.members) |member| {
            const target = packs_mod.payloadTarget(&registry, documentFor(profile), member.payload_type) orelse continue;
            const current = validator.overrides.get(target.document) orelse registry.root(target.document).?;
            const member_schema = if (tolerant)
                try tolerate.document(override_arena.allocator(), member.schema)
            else
                member.schema;
            const widened = try jsonschema.withMember(
                override_arena.allocator(),
                current,
                target.definition,
                member.name,
                member_schema,
            );
            try validator.overrides.put(allocator, target.document, widened);
        }

        var saw_failure = false;
        var gave_up = false;
        for (envelopes) |envelope| {
            const result = validator.validateWithBranches(documentFor(profile), envelope, loaded.branches) catch {
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
        if (tolerant) tolerant_judged += 1;
        if (expect_schema_failure and !saw_failure) {
            try disagreements.print(allocator, "  {s}: manifest says schema-invalid, Zig accepted\n", .{entry.get("id").?.string});
        }
        if (!expect_schema_failure and saw_failure) {
            try disagreements.print(allocator, "  {s}: manifest says schema-valid, Zig rejected\n", .{entry.get("id").?.string});
        }
    }

    std.debug.print("\njudged={d} tolerant={d} skipped_packs={d} unsupported={d} undecodable={d} unhandled={d}\n", .{ judged, tolerant_judged, skipped_packs, unsupported, undecodable, unhandled });
    if (disagreements.items.len > 0) {
        std.debug.print("disagreements:\n{s}", .{disagreements.items});
        return error.SchemaPhaseDisagrees;
    }
    if (unsupported != 0) return error.InterpreterRefusedAFixtureItOnceJudged;
    if (undecodable != 0) return error.FixtureStoppedDecoding;
    if (skipped_packs != 0) return error.PackFailedToLoad;
    try std.testing.expect(judged >= judged_floor);
    try std.testing.expectEqual(unhandled_pack_composition.len, unhandled);
    try std.testing.expectEqual(tolerant_fixtures, tolerant_judged);
}

fn acceptsUnder(allocator: std.mem.Allocator, registry: *const jsonschema.Registry, tolerant: bool, envelope: std.json.Value) !bool {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    var validator = jsonschema.Validator.init(allocator, registry);
    defer validator.deinit();
    if (tolerant) {
        for (registry.documents.keys()) |name| {
            if (tolerate.isMetaSchema(name)) continue;
            try validator.overrides.put(allocator, name, try tolerate.document(arena.allocator(), registry.root(name).?));
        }
    }
    return (try validator.validate("envelope.schema.json", envelope)) == null;
}

test "tolerance is what admits an unclaimed type, and it still demands the envelope skeleton" {
    const allocator = std.testing.allocator;
    const root = build_options.repository_root;

    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();

    const path = try std.fs.path.join(allocator, &.{ root, "fixtures", "valid", "ext-unpacked-type-tolerated.json" });
    defer allocator.free(path);
    const bytes = try readAll(allocator, path);
    defer allocator.free(bytes);

    var trace = try std.json.parseFromSlice(std.json.Value, allocator, bytes, .{});
    defer trace.deinit();
    const envelope = switch (trace.value) {
        .array => |a| a.items[0],
        .object => |o| o.get("events").?.array.items[0],
        else => unreachable,
    };

    try std.testing.expectEqualStrings("com.example.storage.objects.read", envelope.object.get("type").?.string);
    try std.testing.expect(!try acceptsUnder(allocator, &registry, false, envelope));
    try std.testing.expect(try acceptsUnder(allocator, &registry, true, envelope));

    var truncated: std.json.ObjectMap = .empty;
    defer truncated.deinit(allocator);
    var field = envelope.object.iterator();
    while (field.next()) |entry| {
        if (std.mem.eql(u8, entry.key_ptr.*, "id")) continue;
        try truncated.put(allocator, entry.key_ptr.*, entry.value_ptr.*);
    }
    try std.testing.expect(!try acceptsUnder(allocator, &registry, true, .{ .object = truncated }));
}

test "a pack claims its type back from the tolerant fallback" {
    const allocator = std.testing.allocator;
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();

    try registry.addDocument("pack/storage.schema.json",
        \\{"$id":"pack/storage.schema.json","$defs":{"objectsRead":{"type":"object",
        \\"required":["session_id"],"properties":{"type":{"const":"com.example.storage.objects.read"},
        \\"session_id":{"type":"string"},"payload":{"type":"object"}}}}}
    );
    const branches = [_]jsonschema.Alternative{.{
        .declared_type = "com.example.storage.objects.read",
        .ref = "pack/storage.schema.json#/$defs/objectsRead",
    }};

    const lines = [_]struct { line: []const u8, accepted: bool }{
        .{ .line = 
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.storage.objects.read","id":"e1","session_id":"s","payload":{}}
        , .accepted = true },
        .{ .line = 
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core",
        \\"type":"com.example.storage.objects.read","id":"e1","payload":{}}
        , .accepted = false },
    };

    for (lines) |case| {
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, case.line, .{});
        defer parsed.deinit();

        var arena = std.heap.ArenaAllocator.init(allocator);
        defer arena.deinit();
        var validator = jsonschema.Validator.init(allocator, &registry);
        defer validator.deinit();
        for (registry.documents.keys()) |name| {
            if (tolerate.isMetaSchema(name)) continue;
            try validator.overrides.put(allocator, name, try tolerate.document(arena.allocator(), registry.root(name).?));
        }

        const failure = try validator.validateWithBranches("envelope.schema.json", parsed.value, &branches);
        try std.testing.expectEqual(case.accepted, failure == null);
    }
}
