const std = @import("std");
const semantic = @import("semantic");
const provider = @import("provider_semantic");
const build_options = @import("build_options");

const judged_floor = 477;
const tolerant_fixtures = 3;

const control_expectation_pending = [_][]const u8{
    "queue-busy-auto-unadvertised-control",
};

fn readAll(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(64 * 1024 * 1024));
}

fn lessThan(_: void, a: []const u8, b: []const u8) bool {
    return std.mem.order(u8, a, b) == .lt;
}

fn joined(allocator: std.mem.Allocator, codes: [][]const u8) ![]u8 {
    std.mem.sort([]const u8, codes, {}, lessThan);
    var out = std.ArrayList(u8).empty;
    for (codes, 0..) |code, at| {
        if (at != 0) try out.append(allocator, ' ');
        try out.appendSlice(allocator, code);
    }
    return out.toOwnedSlice(allocator);
}

test "the Zig semantic phase emits exactly the lifecycle codes the manifest declares" {
    const allocator = std.testing.allocator;
    const root = build_options.repository_root;

    const manifest_path = try std.fs.path.join(allocator, &.{ root, "fixtures", "manifest.json" });
    defer allocator.free(manifest_path);
    const manifest_bytes = try readAll(allocator, manifest_path);
    defer allocator.free(manifest_bytes);

    var manifest = try std.json.parseFromSlice(std.json.Value, allocator, manifest_bytes, .{});
    defer manifest.deinit();

    var judged: usize = 0;
    var skipped_tolerant: usize = 0;
    var provider_judged: usize = 0;
    var report = std.ArrayList(u8).empty;
    defer report.deinit(allocator);
    var disagreeing = std.ArrayList([]const u8).empty;
    defer disagreeing.deinit(allocator);

    for (manifest.value.object.get("fixtures").?.array.items) |raw| {
        const entry = raw.object;
        const id = entry.get("id").?.string;
        const kind = entry.get("kind").?.string;
        const phase = if (entry.get("phase")) |p| p.string else "";
        const profile = if (entry.get("profile")) |p| p.string else "agent-control-core";

        if (std.mem.eql(u8, kind, "load-invalid")) continue;
        if (std.mem.eql(u8, phase, "decode") or std.mem.eql(u8, phase, "schema")) continue;
        const provider_profile = std.mem.eql(u8, profile, "model-provider-core");
        if (provider_profile) provider_judged += 1;
        if (entry.get("mode")) |mode| {
            if (std.mem.eql(u8, mode.string, "tolerant")) {
                skipped_tolerant += 1;
                continue;
            }
        }

        var expected = std.ArrayList([]const u8).empty;
        defer expected.deinit(allocator);
        if (entry.get("codes")) |codes| {
            for (codes.array.items) |code| {
                const ported = if (provider_profile) provider.isImplemented(code.string) else semantic.isImplemented(code.string);
                if (ported) try expected.append(allocator, code.string);
            }
        }

        const full = try std.fs.path.join(allocator, &.{ root, "fixtures", entry.get("path").?.string });
        defer allocator.free(full);
        const bytes = try readAll(allocator, full);
        defer allocator.free(bytes);
        var trace = try std.json.parseFromSlice(std.json.Value, allocator, bytes, .{});
        defer trace.deinit();

        const envelopes = switch (trace.value) {
            .array => |a| a.items,
            .object => |o| if (o.get("events")) |e| e.array.items else &[_]std.json.Value{trace.value},
            else => continue,
        };

        var emitted = std.ArrayList([]const u8).empty;
        defer emitted.deinit(allocator);
        if (provider_profile) {
            var machine = provider.Machine.init(allocator);
            defer machine.deinit();
            for (envelopes, 0..) |envelope, index| try machine.apply(index, envelope);
            try machine.close();
            for (machine.diagnostics.items) |diagnostic| try emitted.append(allocator, diagnostic.code);
        } else {
            var machine = semantic.Machine.init(allocator);
            defer machine.deinit();
            for (envelopes, 0..) |envelope, index| try machine.apply(index, envelope);
            try machine.close();
            for (machine.diagnostics.items) |diagnostic| try emitted.append(allocator, diagnostic.code);
        }

        judged += 1;
        const want = try joined(allocator, expected.items);
        defer allocator.free(want);
        const got = try joined(allocator, emitted.items);
        defer allocator.free(got);
        if (!std.mem.eql(u8, want, got)) {
            try disagreeing.append(allocator, id);
            try report.print(allocator, "  {s}\n    want [{s}]\n    got  [{s}]\n", .{ id, want, got });
        }
    }

    std.debug.print("\nsemantic judged={d} disagreeing={d} skipped_tolerant={d} provider={d}\n", .{ judged, disagreeing.items.len, skipped_tolerant, provider_judged });

    std.mem.sort([]const u8, disagreeing.items, {}, lessThan);
    var declared = std.ArrayList([]const u8).empty;
    defer declared.deinit(allocator);
    for (control_expectation_pending) |name| try declared.append(allocator, name);
    const outstanding = try joined(allocator, disagreeing.items);
    defer allocator.free(outstanding);
    const accounted = try joined(allocator, declared.items);
    defer allocator.free(accounted);
    if (!std.mem.eql(u8, outstanding, accounted)) {
        std.debug.print("disagreements:\n{s}\ndeclared: [{s}]\n", .{ report.items, accounted });
        return error.SemanticPhaseDisagrees;
    }
    try std.testing.expect(judged >= judged_floor);
    try std.testing.expectEqual(tolerant_fixtures, skipped_tolerant);
}
