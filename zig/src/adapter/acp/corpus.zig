const std = @import("std");
const rpc = @import("rpc");
const session = @import("session");
const corpus = @import("adapter_corpus");

const handshake_actions = [_][]const u8{ "", "observe" };

const stop_reasons = [_]struct { action: []const u8, stop: []const u8 }{
    .{ .action = "complete", .stop = "end_turn" },
    .{ .action = "complete-await", .stop = "end_turn" },
    .{ .action = "cancelled", .stop = "cancelled" },
    .{ .action = "refusal", .stop = "refusal" },
};

fn containsName(names: []const []const u8, candidate: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, candidate)) return true;
    }
    return false;
}

const Driver = struct {
    pub const corpus_relative = "fixtures/adapters/acp-v1";
    pub const blank_expectation = corpus.BlankExpectation.empty_array_only;
    pub const Reducer = session.Reducer;

    pub const Case = struct {
        id: []const u8,
        path: []const u8,
        transport_error: []const u8 = "fixture process exited",
    };

    pub fn open(arena: *std.heap.ArenaAllocator, case: Case) Reducer {
        _ = case;
        var reducer = Reducer.init(arena, .{});
        reducer.open();
        reducer.submit(1) catch {};
        return reducer;
    }

    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.envelopes.items;
    }

    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, s: corpus.Step, case: Case) !corpus.Handled {
        const before = reducer.envelopes.items.len;
        const handled = try dispatch(reducer, scratch, s, case);
        if (handled == .handled) try expectEmission(s.script, reducer.envelopes.items.len - before);
        return handled;
    }

    fn dispatch(reducer: *Reducer, scratch: std.mem.Allocator, s: corpus.Step, case: Case) !corpus.Handled {
        const message = try rpc.parseMessage(scratch, s.encoded);

        if (containsName(&handshake_actions, s.action)) return .handled;

        if (std.mem.eql(u8, s.action, "update")) {
            try reducer.observe(message, s.raw);
            return .handled;
        }
        if (std.mem.eql(u8, s.action, "permission")) {
            try reducer.observe(message, s.raw);
            const pending = reducer.pendingInteraction() orelse return error.NoPendingInteraction;
            if (s.script != .object) return error.InvalidScriptLine;
            const choice = corpus.stringMember(s.script.object, "choice_id") orelse return error.InvalidScriptLine;
            const granted = s.script.object.get("granted") orelse return error.InvalidScriptLine;
            if (granted != .bool) return error.InvalidScriptLine;
            try reducer.resolve(pending, reducer.run.?.id, "user", choice, granted.bool);
            return .handled;
        }
        for (stop_reasons) |settled| {
            if (!std.mem.eql(u8, s.action, settled.action)) continue;
            try reducer.settlePrompt(settled.stop);
            return .handled;
        }
        if (std.mem.eql(u8, s.action, "cancel")) {
            try reducer.cancel();
            return .handled;
        }
        if (std.mem.eql(u8, s.action, "prompt-error")) {
            if (s.raw != .object) return error.InvalidScriptLine;
            const failure = s.raw.object.get("error") orelse return error.InvalidScriptLine;
            if (failure != .object) return error.InvalidScriptLine;
            const code = failure.object.get("code") orelse return error.InvalidScriptLine;
            if (code != .integer) return error.InvalidScriptLine;
            const request = s.raw.object.get("id") orelse std.json.Value{ .null = {} };
            const detail = corpus.stringMember(failure.object, "message") orelse "";
            try reducer.promptFailed(code.integer, request, detail);
            return .handled;
        }
        if (std.mem.eql(u8, s.action, "process-exit")) {
            try reducer.transportFailed(case.transport_error);
            return .handled;
        }
        return .unhandled;
    }
};

fn expectEmission(script: std.json.Value, emitted: usize) !void {
    if (script != .object) return error.InvalidScriptLine;
    if (script.object.get("await_events")) |declared| {
        if (declared != .integer or declared.integer < 0) return error.InvalidScriptLine;
        if (emitted != @as(usize, @intCast(declared.integer))) return error.EmissionCountMismatch;
    }
    const classification = corpus.stringMember(script.object, "classification") orelse return error.InvalidScriptLine;
    if (std.mem.eql(u8, classification, "observed-only") and emitted != 0) return error.ObservedOnlyFrameEmitted;
}

const Harness = corpus.Harness(Driver);

const claimed_cases = [_]Driver.Case{
    .{ .id = "cancel-confirmed", .path = "cases/cancel-confirmed" },
    .{ .id = "completion-wins-race", .path = "cases/completion-wins-race" },
    .{ .id = "malformed-update", .path = "cases/malformed-update" },
    .{ .id = "new-prompt-completed", .path = "cases/new-prompt-completed" },
    .{ .id = "open-with-tool-sources", .path = "cases/open-with-tool-sources" },
    .{ .id = "process-exit", .path = "cases/process-exit" },
    .{ .id = "prompt-error", .path = "cases/prompt-error" },
    .{ .id = "refusal", .path = "cases/refusal" },
    .{ .id = "replay-degradation", .path = "cases/replay-degradation" },
    .{ .id = "tool-lifecycle-permission", .path = "cases/tool-lifecycle-permission" },
    .{ .id = "update-after-terminal", .path = "cases/update-after-terminal" },
};

test "the Zig reducer reproduces every ACP expectation, all eleven of them" {
    try Harness.expectEveryCase(std.testing.allocator, &claimed_cases);
}

fn driveScript(script: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const case = Driver.Case{ .id = "inline", .path = "" };
    var reducer = Driver.open(&arena, case);
    try Harness.steps(arena.allocator(), script, &reducer, case);
}

test "a script step this harness does not drive is refused, not skipped" {
    try driveScript(
        \\{"action":"observe","classification":"mapped","raw":{"jsonrpc":"2.0","id":1,"result":{}}}
    );
    try std.testing.expectError(error.UnhandledScriptAction, driveScript(
        \\{"action":"invented-later","classification":"mapped","raw":{"jsonrpc":"2.0","id":1,"result":{}}}
    ));
}

test "every line is decoded by the production parser, whether or not it reaches the reducer" {
    try std.testing.expectError(rpc.Error.InvalidMessage, driveScript(
        \\{"action":"observe","classification":"mapped","raw":{"jsonrpc":"1.0","id":1,"result":{}}}
    ));
    try std.testing.expectError(rpc.Error.InvalidID, driveScript(
        \\{"action":"observe","classification":"mapped","raw":{"jsonrpc":"2.0","id":1.5,"result":{}}}
    ));
}

test "a permission line states its decision or the case fails" {
    try std.testing.expectError(error.InvalidScriptLine, driveScript(
        \\{"action":"permission","classification":"mapped","raw":{"jsonrpc":"2.0","id":"p1","method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}}
    ));
    try driveScript(
        \\{"action":"permission","classification":"mapped","choice_id":"a","granted":true,"raw":{"jsonrpc":"2.0","id":"p1","method":"session/request_permission","params":{"sessionId":"native-session","toolCall":{"toolCallId":"t","title":"T"},"options":[{"optionId":"a","name":"A","kind":"allow_once"}]}}}
    );
}

test "a line that declares an event count must emit exactly that many" {
    try std.testing.expectError(error.EmissionCountMismatch, driveScript(
        \\{"action":"observe","classification":"mapped","await_events":1,"raw":{"jsonrpc":"2.0","id":1,"result":{}}}
    ));
    try std.testing.expectError(error.EmissionCountMismatch, driveScript(
        \\{"action":"update","classification":"mapped","await_events":2,"raw":{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}}
    ));
    try driveScript(
        \\{"action":"update","classification":"mapped","await_events":1,"raw":{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}}
    );
}

test "a frame the corpus records as observed-only must reduce to nothing" {
    try std.testing.expectError(error.ObservedOnlyFrameEmitted, driveScript(
        \\{"action":"update","classification":"observed-only","raw":{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hi"}}}}}
    ));
    try driveScript(
        \\{"action":"update","classification":"observed-only","raw":{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"native-session","update":{"sessionUpdate":"plan"}}}}
    );
}

test "a script line carrying no classification is refused" {
    try std.testing.expectError(error.InvalidScriptLine, driveScript(
        \\{"action":"observe","raw":{"jsonrpc":"2.0","id":1,"result":{}}}
    ));
}
