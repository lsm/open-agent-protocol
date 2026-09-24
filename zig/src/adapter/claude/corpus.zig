const std = @import("std");
const rpc = @import("rpc");
const session = @import("session");
const corpus = @import("adapter_corpus");

const transport_only_actions = [_][]const u8{ "expect-write", "reply", "wait-run", "drain" };

const reducer_blind_control_ops = [_][]const u8{ "close", "assert-state", "resume", "cancel" };

const current_version = "2.1.280";
const floor_version = "2.1.263";

fn containsName(names: []const []const u8, candidate: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, candidate)) return true;
    }
    return false;
}

const ClaudeCase = struct {
    id: []const u8,
    path: []const u8,
    transport_error: []const u8 = "",
};

fn Driver(comptime version: []const u8) type {
    return struct {
        pub const corpus_relative = "fixtures/adapters/claude-code-" ++ version;
        pub const blank_expectation = corpus.BlankExpectation.zero_byte_or_empty_array;
        pub const Reducer = session.Reducer;
        pub const Case = ClaudeCase;
        pub const excluded_cases = [_][]const u8{transport_error_case.id};

        pub fn open(arena: *std.heap.ArenaAllocator, case: Case) Reducer {
            _ = case;
            var reducer = Reducer.init(arena, .{});
            reducer.open();
            return reducer;
        }

        pub fn envelopes(reducer: *Reducer) []std.json.Value {
            return reducer.envelopes.items;
        }

        pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, s: corpus.Step, case: Case) !corpus.Handled {
            if (std.mem.eql(u8, s.action, "submit")) {
                const uuid = if (s.raw == .object) corpus.stringMember(s.raw.object, "uuid") orelse "" else "";
                try reducer.submit(uuid);
                return .handled;
            }
            if (std.mem.eql(u8, s.action, "observe")) {
                try reducer.observe(try rpc.parseMessage(scratch, s.encoded, null));
                return .handled;
            }
            if (std.mem.eql(u8, s.action, "decode-error")) {
                const bytes = if (s.raw == .string) s.raw.string else s.encoded;
                var diagnostic = rpc.Diagnostic{};
                if (rpc.parseMessage(scratch, bytes, &diagnostic)) |_| {
                    return error.FrameDecodedUnexpectedly;
                } else |_| {
                    try reducer.transportFailed(diagnostic.message);
                }
                return .handled;
            }
            if (std.mem.eql(u8, s.action, "process-exit")) {
                try reducer.transportFailed(case.transport_error);
                return .handled;
            }
            if (std.mem.eql(u8, s.action, "oap-control")) {
                if (s.raw != .object) return error.InvalidScriptLine;
                const op = corpus.stringMember(s.raw.object, "op") orelse return error.InvalidScriptLine;
                if (std.mem.eql(u8, op, "assert-catalog")) {
                    _ = try reducer.listTools();
                    return .handled;
                }
                if (!std.mem.eql(u8, op, "resolve")) {
                    if (!containsName(&reducer_blind_control_ops, op)) return error.UnhandledControlOp;
                    return .handled;
                }
                const decision = corpus.stringMember(s.raw.object, "decision") orelse return error.InvalidScriptLine;
                const pending = reducer.pendingInteraction() orelse return error.NoPendingInteraction;
                if (std.mem.eql(u8, decision, "allow")) {
                    try reducer.resolve(pending, .allow);
                } else if (std.mem.eql(u8, decision, "deny")) {
                    try reducer.resolve(pending, .deny);
                } else return error.InvalidScriptLine;
                return .handled;
            }
            if (containsName(&transport_only_actions, s.action)) return .handled;
            return .unhandled;
        }
    };
}

const Current = corpus.Harness(Driver(current_version));
const Floor = corpus.Harness(Driver(floor_version));

const claimed_cases = [_]ClaudeCase{
    .{ .id = "initialize-lifecycle", .path = "initialize-lifecycle" },
    .{ .id = "tool-lifecycle", .path = "tool-lifecycle" },
    .{ .id = "permission-gates", .path = "permission-gates" },
    .{ .id = "interrupt-cancel", .path = "interrupt-cancel" },
    .{ .id = "settlement-statuses", .path = "settlement-statuses" },
    .{ .id = "streaming-provenance", .path = "streaming-provenance" },
    .{ .id = "admission-corroboration", .path = "admission-corroboration" },
    .{ .id = "background-children", .path = "background-children" },
    .{ .id = "malformed-stdout", .path = "malformed-stdout" },
    .{ .id = "hygiene-recovery", .path = "hygiene-recovery" },
    .{ .id = "queued-continuation", .path = "queued-continuation" },
    .{ .id = "tools-catalog-sources", .path = "tools-catalog-sources" },
};

test "the Zig reducer reproduces every expectation it claims in the 2.1.280 corpus" {
    try Current.expectEveryCase(std.testing.allocator, &claimed_cases);
}

test "the Zig reducer reproduces every expectation it claims in the 2.1.263 floor corpus" {
    try Floor.expectEveryCase(std.testing.allocator, &claimed_cases);
}

const transport_error_case = ClaudeCase{
    .id = "process-exit",
    .path = "process-exit",
    .transport_error = "the corpus harness closed the transport",
};

fn expectProcessExitDiffersOnlyInTheRuntimeText(comptime Harness: type) !void {
    const allocator = std.testing.allocator;
    const root = try Harness.root(allocator);
    defer allocator.free(root);

    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const played = try Harness.replay(&arena, root, transport_error_case);

    try std.testing.expectEqual(played.expected.len, played.emitted.len);
    const last = played.expected.len - 1;
    for (played.expected[0..last], played.emitted[0..last]) |want, got| {
        try std.testing.expect(corpus.equalValues(want, got));
    }
    try std.testing.expect(corpus.equalExcept(played.expected[last], played.emitted[last], &.{ "payload", "error", "message" }));

    const want_error = played.expected[last].object.get("payload").?.object.get("error").?.object;
    const got_error = played.emitted[last].object.get("payload").?.object.get("error").?.object;
    try std.testing.expectEqualStrings("io: read/write on closed pipe", want_error.get("message").?.string);
    try std.testing.expectEqualStrings(transport_error_case.transport_error, got_error.get("message").?.string);
}

test "process-exit differs only where its expectation quotes the Go runtime, in both corpora" {
    try expectProcessExitDiffersOnlyInTheRuntimeText(Current);
    try expectProcessExitDiffersOnlyInTheRuntimeText(Floor);
}

test "the current corpus records the version the capability revision pins" {
    const allocator = std.testing.allocator;
    try std.testing.expect(std.mem.startsWith(u8, session.capability_revision, "claude-code-" ++ current_version ++ "-"));

    const root = try Current.root(allocator);
    defer allocator.free(root);
    const path = try std.fs.path.join(allocator, &.{ root, "manifest.json" });
    defer allocator.free(path);
    const text = try std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(1024 * 1024));
    defer allocator.free(text);
    const manifest = try std.json.parseFromSlice(std.json.Value, allocator, text, .{});
    defer manifest.deinit();
    try std.testing.expectEqualStrings(current_version, manifest.value.object.get("tag").?.string);
    try std.testing.expectEqualStrings(current_version, manifest.value.object.get("sources").?.object.get("cli_version").?.string);
}

fn driveScript(script: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const case = ClaudeCase{ .id = "inline", .path = "" };
    var reducer = Driver(current_version).open(&arena, case);
    try Current.steps(arena.allocator(), script, &reducer, case);
}

test "a script step this harness does not drive is refused, not skipped" {
    try driveScript(
        \\{"action":"wait-run","raw":{"type":"harness_sync","op":"wait-run"}}
    );
    try std.testing.expectError(error.UnhandledScriptAction, driveScript(
        \\{"action":"invented-later","raw":{}}
    ));

    try driveScript(
        \\{"action":"oap-control","raw":{"type":"oap_control","op":"close"}}
    );
    try std.testing.expectError(error.UnhandledControlOp, driveScript(
        \\{"action":"oap-control","raw":{"type":"oap_control","op":"invented-later"}}
    ));
}
