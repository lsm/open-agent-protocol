const std = @import("std");
const semantic = @import("semantic");
const provider = @import("provider_semantic");
const packs_mod = @import("packs");
const build_options = @import("build_options");

const judged_floor = 477;
const tolerant_fixtures = 3;

const nothing_outstanding = [_][]const u8{};

const partial_code_gaps = [_]struct { code: []const u8, fixtures: []const []const u8 }{
    .{ .code = "session_state_mismatch", .fixtures = &.{
        "compound-open-anchor-names-an-open-without-a-message",
        "compound-open-queued-without-position",
        "control-call-recovered-ack-omitted",
        "control-call-recovered-state-keeps-resolved",
        "control-state-acknowledges-unpending",
        "control-state-omits-acknowledged",
        "models-current-mismatch",
        "models-held-position-moved-no-model",
        "open-attach-contradicts-stated-member",
        "open-sources-state-disagrees-with-catalog",
        "open-sources-state-omits-attachment",
        "queue-open-adds-an-interaction-the-entry-denied",
        "queue-open-claims-two-started-runs",
        "queue-open-keeps-a-resolved-interaction-pending",
        "queue-open-lists-a-settled-run",
        "queue-open-lists-one-run-twice",
        "queue-open-names-a-run-from-nowhere",
        "queue-open-recovers-a-cancelling-run-beside-a-started-one",
        "queue-open-reopens-and-claims-two-started-runs",
        "queue-state-anchor-request-refused",
        "queue-state-anchor-request-unaccounted",
        "queue-state-anchor-request-unanswered",
        "queue-state-as-of-never-reached",
        "queue-state-cancelling-reservation-past-its-promotion",
        "queue-state-cancelling-reservation-without-its-place",
        "queue-state-cancelling-run-drops-a-place-it-never-left",
        "queue-state-cancelling-run-left-the-queue-early",
        "queue-state-drops-reserved-run",
        "queue-state-drops-unsettled-run",
        "queue-state-earlier-anchor-answers-the-entry",
        "queue-state-earlier-anchor-vindicates-the-entry",
        "queue-state-entry-anchor-admitted-elsewhere",
        "queue-state-entry-leads-a-different-run",
        "queue-state-entry-leads-without-an-anchor",
        "queue-state-executes-a-run-that-never-starts",
        "queue-state-false-settled-claim",
        "queue-state-in-window-run-unnamed",
        "queue-state-lead-claims-a-second-started-run",
        "queue-state-lead-counts-a-place-its-sibling-never-took",
        "queue-state-lead-does-not-excuse-the-known-entries",
        "queue-state-lead-executes-a-run-that-never-starts",
        "queue-state-lead-gives-up-a-place-it-never-left",
        "queue-state-lead-invents-a-queue-place",
        "queue-state-lead-keeps-a-place-past-its-promotion",
        "queue-state-lead-listed-before-a-known-run",
        "queue-state-lead-lists-a-settled-run",
        "queue-state-lead-misplaces-its-reservation",
        "queue-state-lead-names-no-active-run",
        "queue-state-lead-places-itself-beyond-the-queue",
        "queue-state-lead-queued-by-a-started-admission",
        "queue-state-leads-claim-one-place-twice",
        "queue-state-leads-listed-out-of-admission-order",
        "queue-state-listed-at-its-terminal-position",
        "queue-state-lists-a-run-it-says-it-settled",
        "queue-state-lists-settled-run",
        "queue-state-missing-reservation",
        "queue-state-model-anchor-foreign-session",
        "queue-state-model-anchor-genesis-superseded",
        "queue-state-model-anchor-never-promoted",
        "queue-state-model-anchor-superseded",
        "queue-state-model-anchor-without-mutation",
        "queue-state-model-anchor-wrong-sequence",
        "queue-state-omission-excused-then-repeated",
        "queue-state-omits-active-runs",
        "queue-state-omits-before-settlement",
        "queue-state-omits-established-run",
        "queue-state-omits-pending-interaction",
        "queue-state-pending-without-as-of",
        "queue-state-promoted-run-erased",
        "queue-state-queued-at-forthcoming-start",
        "queue-state-queued-entry-without-position",
        "queue-state-reservation-named-active",
        "queue-state-reservations-called-idle",
        "queue-state-running-beside-a-pending-interaction",
        "queue-state-running-beside-a-waiting-run",
        "queue-state-settled-claim-then-activity",
        "queue-state-settled-wrong-sequence",
        "queue-state-settles-one-run-twice",
        "queue-state-started-behind-a-reservation",
        "queue-state-started-listed-queued",
        "queue-state-started-run-called-queued",
        "queue-state-terminal-status",
        "queue-state-two-started-entries",
        "queue-state-waiting-without-a-waiting-run",
        "state-omits-active-runs-with-pending-permission",
        "tools-catalog-follows-contradicting-open",
    } },
};

fn isPartialCode(code: []const u8) bool {
    for (partial_code_gaps) |held| {
        if (std.mem.eql(u8, held.code, code)) return true;
    }
    return false;
}

fn knownPartialGap(code: []const u8, id: []const u8) bool {
    for (partial_code_gaps) |held| {
        if (!std.mem.eql(u8, held.code, code)) continue;
        for (held.fixtures) |name| {
            if (std.mem.eql(u8, name, id)) return true;
        }
    }
    return false;
}

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
    var no_longer_gapped = std.ArrayList([]const u8).empty;
    defer no_longer_gapped.deinit(allocator);
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
        var gapped = std.ArrayList([]const u8).empty;
        defer gapped.deinit(allocator);
        if (entry.get("codes")) |codes| {
            for (codes.array.items) |code| {
                const ported = if (provider_profile) provider.isImplemented(code.string) else semantic.isImplemented(code.string);
                const partial = !provider_profile and isPartialCode(code.string);
                if (!ported and !partial) continue;
                if (knownPartialGap(code.string, id)) {
                    try gapped.append(allocator, code.string);
                    continue;
                }
                try expected.append(allocator, code.string);
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
            var pack_dirs = std.ArrayList([]const u8).empty;
            defer {
                for (pack_dirs.items) |dir| allocator.free(dir);
                pack_dirs.deinit(allocator);
            }
            if (entry.get("packs")) |declared_packs| {
                for (declared_packs.array.items) |name| {
                    try pack_dirs.append(allocator, try std.fs.path.join(allocator, &.{ root, "fixtures", name.string }));
                }
            }
            var loaded = try packs_mod.describe(allocator, pack_dirs.items);
            defer loaded.deinit();

            var types = std.ArrayList(semantic.PackedType).empty;
            defer types.deinit(allocator);
            for (loaded.types) |held| {
                try types.append(allocator, .{
                    .name = held.name,
                    .role = held.role,
                    .capability = held.capability,
                    .response = held.response,
                    .refusals = held.refusals,
                });
            }
            var declared_members = std.ArrayList(semantic.PackedMember).empty;
            defer declared_members.deinit(allocator);
            for (loaded.members) |held| {
                try declared_members.append(allocator, .{
                    .payload_type = held.payload_type,
                    .name = held.name,
                    .capability = held.capability,
                });
            }

            var machine = semantic.Machine.init(allocator);
            defer machine.deinit();
            machine.packs = .{ .types = types.items, .members = declared_members.items };
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
            continue;
        }
        for (gapped.items) |code| {
            if (std.mem.indexOf(u8, got, code) == null) continue;
            try no_longer_gapped.append(allocator, id);
            try report.print(allocator, "  {s}: listed as a gap, and {s} is raised\n", .{ id, code });
        }
    }

    std.debug.print("\nsemantic judged={d} disagreeing={d} skipped_tolerant={d} provider={d}\n", .{ judged, disagreeing.items.len, skipped_tolerant, provider_judged });

    if (no_longer_gapped.items.len != 0) {
        std.debug.print("listed as gaps, and now judged:\n{s}", .{report.items});
        return error.AGapListEntryNoLongerApplies;
    }
    std.mem.sort([]const u8, disagreeing.items, {}, lessThan);
    var declared = std.ArrayList([]const u8).empty;
    defer declared.deinit(allocator);
    for (nothing_outstanding) |name| try declared.append(allocator, name);
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
