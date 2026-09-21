const std = @import("std");
const corpus = @import("corpus");
const session = @import("session.zig");
const rpc = @import("rpc.zig");

const Direction = enum { dsh_to_host, host_to_dsh, harness_control };

const Routing = struct { action: []const u8, direction: Direction };

const routings = [_]Routing{
    .{ .action = "auto", .direction = .dsh_to_host },
    .{ .action = "reply", .direction = .dsh_to_host },
    .{ .action = "reply-error", .direction = .dsh_to_host },
    .{ .action = "observe", .direction = .dsh_to_host },
    .{ .action = "observe-invalid", .direction = .dsh_to_host },
    .{ .action = "decode-error", .direction = .dsh_to_host },
    .{ .action = "decode-error-unterminated", .direction = .dsh_to_host },
    .{ .action = "submit", .direction = .host_to_dsh },
    .{ .action = "open", .direction = .host_to_dsh },
    .{ .action = "shutdown", .direction = .host_to_dsh },
    .{ .action = "overlap-submit", .direction = .host_to_dsh },
    .{ .action = "process-exit", .direction = .harness_control },
    .{ .action = "oap-control", .direction = .harness_control },
    .{ .action = "wait-submit", .direction = .harness_control },
};

const refused = [_][]const u8{ "decode-error", "decode-error-unterminated" };

fn directionOf(action: []const u8) ?Direction {
    for (routings) |entry| {
        if (std.mem.eql(u8, entry.action, action)) return entry.direction;
    }
    return null;
}

fn refusesDecode(action: []const u8) bool {
    for (refused) |name| {
        if (std.mem.eql(u8, name, action)) return true;
    }
    return false;
}

fn wireBytes(item: corpus.Step) []const u8 {
    return if (item.raw == .string) item.raw.string else item.encoded;
}

fn decodes(scratch: std.mem.Allocator, source: []const u8) bool {
    var decoder = rpc.Decoder{ .source = source };
    const decoded = decoder.next(scratch) catch return false;
    return decoded != null;
}

fn decodeWire(scratch: std.mem.Allocator, action: []const u8, wire: []const u8) !void {
    const direction = directionOf(action) orelse return error.UnroutedScriptAction;
    if (direction == .harness_control) return;

    if (std.mem.eql(u8, action, "decode-error-unterminated")) {
        if (decodes(scratch, wire)) return error.UnterminatedFrameDecoded;
        if (!decodes(scratch, try std.mem.concat(scratch, u8, &.{ wire, "\n" }))) {
            return error.UnterminatedFrameRefusedForSomethingElse;
        }
        return;
    }

    if (decodes(scratch, try std.mem.concat(scratch, u8, &.{ wire, "\n" }))) {
        if (refusesDecode(action)) return error.ProductionCodecAcceptedInvalidFrame;
        return;
    }
    if (!refusesDecode(action)) return error.ProductionCodecRefusedCorpusFrame;
}

fn decodeFrame(scratch: std.mem.Allocator, item: corpus.Step) !void {
    return decodeWire(scratch, item.action, wireBytes(item));
}

pub const CorpusCase = struct { id: []const u8, path: []const u8 };

const Driver = struct {
    pub const corpus_relative = "fixtures/adapters/deepseek-harness-47f9438";
    pub const blank_expectation = corpus.BlankExpectation.zero_byte_only;
    pub const Reducer = session.Reducer;
    pub const Case = CorpusCase;

    pub fn open(arena: *std.heap.ArenaAllocator, case: CorpusCase) Reducer {
        _ = case;
        var reducer = session.Reducer.init(arena.allocator());
        session.openSession(&reducer);
        return reducer;
    }

    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, item: corpus.Step, case: CorpusCase) !corpus.Handled {
        _ = case;
        try decodeFrame(scratch, item);
        const action = item.action;
        if (std.mem.eql(u8, action, "submit")) {
            try session.submit(reducer);
            return .handled;
        }
        if (std.mem.eql(u8, action, "wait-submit")) return .handled;
        if (std.mem.eql(u8, action, "oap-control")) {
            try assertControl(reducer, item.raw);
            return .handled;
        }
        if (std.mem.eql(u8, action, "open")) {
            const params = item.raw.object.get("params") orelse return .handled;
            const model = if (params == .object) corpus.stringMember(params.object, "model") orelse "" else "";
            session.initialize(reducer, model);
            return .handled;
        }
        if (std.mem.eql(u8, action, "shutdown")) return .handled;

        if (std.mem.eql(u8, action, "decode-error")) return .handled;
        if (std.mem.eql(u8, action, "decode-error-unterminated")) return .handled;
        if (std.mem.eql(u8, action, "overlap-submit")) {
            try session.rejectedSubmit(reducer);
            return .handled;
        }
        if (std.mem.eql(u8, action, "reply-error")) return .handled;
        if (std.mem.eql(u8, action, "process-exit")) {
            try session.transportFailed(reducer, corpus.stringMember(item.raw.object, "error") orelse "");
            return .handled;
        }
        if (std.mem.eql(u8, action, "observe-invalid")) {
            const params = item.raw.object.get("params") orelse return .handled;
            const event = params.object.get("event") orelse return .handled;
            try session.invalidObservation(reducer, corpus.stringMember(event.object, "type") orelse "");
            return .handled;
        }
        if (std.mem.eql(u8, action, "reply")) {
            const result = item.raw.object.get("result") orelse return .handled;
            try session.receipt(reducer, corpus.stringMember(result.object, "messageId") orelse "");
            return .handled;
        }
        if (std.mem.eql(u8, action, "observe") or std.mem.eql(u8, action, "auto")) {
            if (item.raw.object.get("result")) |result| {
                try session.receipt(reducer, corpus.stringMember(result.object, "messageId") orelse "");
                return .handled;
            }
            const method = corpus.stringMember(item.raw.object, "method") orelse return .handled;
            const params = item.raw.object.get("params") orelse return .handled;
            try session.observeNotification(reducer, method, params);
            return .handled;
        }
        return .unhandled;
    }

    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.envelopes();
    }
};

fn advertisedControl(op: []const u8) !bool {
    if (std.mem.eql(u8, op, "cancel")) return @hasDecl(session, "cancel");
    if (std.mem.eql(u8, op, "resume")) return @hasDecl(session, "resume");
    if (std.mem.eql(u8, op, "resolve")) return @hasDecl(session, "resolve");
    return error.UnroutedControlOperation;
}

fn assertControl(reducer: *session.Reducer, raw: std.json.Value) !void {
    if (raw != .object) return error.ControlIsNotAnObject;
    const op = corpus.stringMember(raw.object, "op") orelse return error.ControlWithoutOperation;
    const expect = corpus.stringMember(raw.object, "expect") orelse "";

    if (std.mem.eql(u8, op, "assert-state")) {
        const want = corpus.stringMember(raw.object, "status") orelse return error.ControlWithoutStatus;
        const running = reducer.started and !reducer.terminal;
        if (std.mem.eql(u8, want, "running")) {
            if (!running) return error.ReducerIsNotRunning;
            return;
        }
        if (std.mem.eql(u8, want, "idle")) {
            if (running) return error.ReducerIsNotIdle;
            return;
        }
        return error.UnroutedControlStatus;
    }

    if (!std.mem.eql(u8, expect, "unavailable")) return error.UnroutedControlExpectation;
    if (try advertisedControl(op)) return error.ControlRecordedUnavailableIsAdvertised;
}

pub const Harness = corpus.Harness(Driver);

test "every deepseek corpus case replays to the recorded envelopes" {
    try Harness.expectEveryCase(std.testing.allocator, &.{
        .{ .id = "framing-strictness", .path = "framing-strictness" },
        .{ .id = "initialize-lifecycle", .path = "initialize-lifecycle" },
        .{ .id = "initialize-pre-observe", .path = "initialize-pre-observe" },
        .{ .id = "injected-origin", .path = "injected-origin" },
        .{ .id = "no-run-paths", .path = "no-run-paths" },
        .{ .id = "overlap-rejected", .path = "overlap-rejected" },
        .{ .id = "owned-start-completed", .path = "owned-start-completed" },
        .{ .id = "process-loss", .path = "process-loss" },
        .{ .id = "streaming-chunks", .path = "streaming-chunks" },
        .{ .id = "subagent-settlement", .path = "subagent-settlement" },
        .{ .id = "tool-lifecycle", .path = "tool-lifecycle" },
        .{ .id = "turn-end-reasons", .path = "turn-end-reasons" },
        .{ .id = "unknown-events", .path = "unknown-events" },
        .{ .id = "unsupported-controls", .path = "unsupported-controls" },
    });
}

const notification = "{\"jsonrpc\":\"2.0\",\"method\":\"session.status\"}";

fn expectWire(action: []const u8, wire: []const u8, want: anyerror!void) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const got = decodeWire(arena.allocator(), action, wire);
    if (want) |_| {
        try got;
    } else |expected| {
        try std.testing.expectError(expected, got);
    }
}

test "an unterminated corpus frame must fail bare and decode once terminated" {
    try expectWire("decode-error-unterminated", notification, {});
    try expectWire("decode-error-unterminated", notification ++ "\n", error.UnterminatedFrameDecoded);
    try expectWire("decode-error-unterminated", "{\"jsonrpc\":\"1.0\"}", error.UnterminatedFrameRefusedForSomethingElse);
}

test "a frame the production codec refuses is a corpus defect unless its action says otherwise" {
    try expectWire("observe", notification, {});
    try expectWire("observe", "{\"jsonrpc\":\"1.0\"}", error.ProductionCodecRefusedCorpusFrame);
    try expectWire("decode-error", "{\"jsonrpc\":\"1.0\"}", {});
    try expectWire("decode-error", notification, error.ProductionCodecAcceptedInvalidFrame);
}

test "a harness control line carries no frame, and an unrouted action is refused" {
    try expectWire("wait-submit", "{\"op\":\"wait-submit\"}", {});
    try expectWire("nonesuch", notification, error.UnroutedScriptAction);
}

test "a script line the production codec refuses fails the case at the call site" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = session.Reducer.init(scratch);
    session.openSession(&reducer);
    const refused_line = "{\"action\":\"observe\",\"raw\":{\"jsonrpc\":\"1.0\",\"method\":\"session.status\"}}\n";
    try std.testing.expectError(
        error.ProductionCodecRefusedCorpusFrame,
        Harness.steps(scratch, refused_line, &reducer, .{ .id = "inline", .path = "inline" }),
    );

    var accepted_reducer = session.Reducer.init(scratch);
    session.openSession(&accepted_reducer);
    const accepted = "{\"action\":\"observe\",\"raw\":{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"status\":\"idle\"}}}\n";
    try Harness.steps(scratch, accepted, &accepted_reducer, .{ .id = "inline", .path = "inline" });
}

test "a control assertion that does not hold fails the case at the call site" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const inline_case = CorpusCase{ .id = "inline", .path = "inline" };

    var idle = session.Reducer.init(scratch);
    session.openSession(&idle);
    const wants_running = "{\"action\":\"oap-control\",\"raw\":{\"type\":\"oap_control\",\"op\":\"assert-state\",\"status\":\"running\",\"expect\":\"\"}}\n";
    try std.testing.expectError(
        error.ReducerIsNotRunning,
        Harness.steps(scratch, wants_running, &idle, inline_case),
    );

    var held = session.Reducer.init(scratch);
    session.openSession(&held);
    const wants_idle = "{\"action\":\"oap-control\",\"raw\":{\"type\":\"oap_control\",\"op\":\"assert-state\",\"status\":\"idle\",\"expect\":\"\"}}\n";
    try Harness.steps(scratch, wants_idle, &held, inline_case);

    var unavailable = session.Reducer.init(scratch);
    session.openSession(&unavailable);
    const unknown_op = "{\"action\":\"oap-control\",\"raw\":{\"type\":\"oap_control\",\"op\":\"teleport\",\"expect\":\"unavailable\"}}\n";
    try std.testing.expectError(
        error.UnroutedControlOperation,
        Harness.steps(scratch, unknown_op, &unavailable, inline_case),
    );
}

test "a status observed before any prompt admits nothing and writes nothing" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();

    var reducer = session.Reducer.init(scratch);
    session.openSession(&reducer);
    const script =
        "{\"action\":\"open\",\"raw\":{\"id\":1,\"jsonrpc\":\"2.0\",\"method\":\"initialize\",\"params\":{\"model\":\"fixture-model\"}}}\n" ++
        "{\"action\":\"observe\",\"raw\":{\"jsonrpc\":\"2.0\",\"method\":\"session.status\",\"params\":{\"sessionId\":\"session\",\"status\":\"running\"}}}\n";
    try Harness.steps(scratch, script, &reducer, .{ .id = "inline", .path = "inline" });

    try std.testing.expect(!reducer.started);
    try std.testing.expect(reducer.envelopes().len == 0);
}
