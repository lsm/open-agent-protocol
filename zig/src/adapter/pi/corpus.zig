const std = @import("std");
const corpus = @import("corpus");
const session = @import("session.zig");
const rpc = @import("rpc.zig");

const Direction = enum { pi_to_host, host_to_pi, harness_control };

const Route = struct { name: []const u8, direction: Direction };

const routes = [_]Route{
    .{ .name = "pi-to-host", .direction = .pi_to_host },
    .{ .name = "host-to-pi", .direction = .host_to_pi },
    .{ .name = "harness-control", .direction = .harness_control },
};

fn directionOf(script: std.json.Value) ?Direction {
    if (script != .object) return null;
    const named = corpus.stringMember(script.object, "direction") orelse return null;
    for (routes) |entry| {
        if (std.mem.eql(u8, entry.name, named)) return entry.direction;
    }
    return null;
}

fn actionOf(script: std.json.Value) []const u8 {
    if (script != .object) return "";
    return corpus.stringMember(script.object, "action") orelse "";
}

fn decodeWire(scratch: std.mem.Allocator, script: std.json.Value, wire: []const u8) !void {
    const direction = directionOf(script) orelse return error.UnroutedScriptDirection;
    if (direction != .pi_to_host) return;
    const refuses = std.mem.eql(u8, actionOf(script), "decode-error");

    const source = try std.mem.concat(scratch, u8, &.{ wire, "\n" });
    var decoder = rpc.Decoder{ .source = source };
    const decoded = decoder.next(scratch) catch {
        if (refuses) return;
        return error.ProductionCodecRefusedCorpusFrame;
    };
    if (refuses) return error.ProductionCodecAcceptedInvalidFrame;
    _ = decoded orelse return error.ProductionCodecYieldedNoFrame;
}

fn checkControl(script: std.json.Value, raw: std.json.Value) !void {
    const direction = directionOf(script) orelse return error.UnroutedScriptDirection;
    if (direction != .harness_control) return;
    if (!std.mem.eql(u8, actionOf(script), "process-exit")) return error.UnroutedHarnessControlAction;
    if (raw != .object) return error.InvalidHarnessControl;
    const kind = corpus.stringMember(raw.object, "type") orelse "";
    const reported = corpus.stringMember(raw.object, "error") orelse "";
    if (!std.mem.eql(u8, kind, "process_exit")) return error.InvalidHarnessControl;
    if (reported.len == 0) return error.InvalidHarnessControl;
}

fn decodeFrame(scratch: std.mem.Allocator, item: corpus.Step) !void {
    try checkControl(item.script, item.raw);
    return decodeWire(scratch, item.script, item.encoded);
}

pub const CorpusCase = struct {
    id: []const u8,
    path: []const u8,
    prompt_failure: bool = false,
    codec_only: bool = false,
};

const Driver = struct {
    pub const corpus_relative = "fixtures/adapters/pi-v0.85.1";
    pub const blank_expectation = corpus.BlankExpectation.zero_byte_only;
    pub const Reducer = session.Reducer;
    pub const Case = CorpusCase;

    pub fn open(arena: *std.heap.ArenaAllocator, case: CorpusCase) Reducer {
        var reducer = session.Reducer.init(arena.allocator());
        if (case.prompt_failure or case.codec_only) return reducer;
        session.open(&reducer) catch {};
        return reducer;
    }

    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, item: corpus.Step, case: CorpusCase) !corpus.Handled {
        _ = case;
        try decodeFrame(scratch, item);
        if (std.mem.eql(u8, item.action, "outbound-only")) return .handled;
        if (std.mem.eql(u8, item.action, "state")) return .handled;
        if (std.mem.eql(u8, item.action, "decode-error")) return .handled;
        if (std.mem.eql(u8, item.action, "prompt-failure")) return .handled;
        if (std.mem.eql(u8, item.action, "cancel")) {
            try session.cancel(reducer);
            return .handled;
        }
        if (std.mem.eql(u8, item.action, "process-exit")) {
            try session.transportFailed(reducer, corpus.stringMember(item.raw.object, "error") orelse "");
            return .handled;
        }
        if (std.mem.eql(u8, item.action, "observe-extension")) {
            try session.applyExtension(reducer, item.raw);
            return .handled;
        }
        if (std.mem.eql(u8, item.action, "resolve-extension")) {
            const pending = session.pendingInteractionID(reducer) orelse return error.NoInteractionToResolve;
            try session.resolveExtension(reducer, pending, "yes");
            try session.apply(reducer, item.raw);
            return .handled;
        }
        if (std.mem.eql(u8, item.action, "observe") or item.action.len == 0) {
            try session.apply(reducer, item.raw);
            return .handled;
        }
        return .unhandled;
    }

    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.envelopes();
    }
};

pub const Harness = corpus.Harness(Driver);

test "every pi corpus case replays to the recorded envelopes" {
    try Harness.expectEveryCase(std.testing.allocator, &.{
        .{ .id = "cancel-settled", .path = "cancel-settled" },
        .{ .id = "completed-text", .path = "completed-text" },
        .{ .id = "extension-dialog", .path = "extension-dialog" },
        .{ .id = "malformed-command", .path = "malformed-command", .codec_only = true },
        .{ .id = "native-controls", .path = "native-controls" },
        .{ .id = "process-exit", .path = "process-exit" },
        .{ .id = "prompt-rejected", .path = "prompt-rejected", .prompt_failure = true },
        .{ .id = "retry-compaction", .path = "retry-compaction" },
        .{ .id = "streaming-deltas", .path = "streaming-deltas" },
        .{ .id = "tool-lifecycle", .path = "tool-lifecycle" },
    });
}

fn expectWire(script_line: []const u8, wire: []const u8, want: anyerror!void) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const script = try std.json.parseFromSliceLeaky(std.json.Value, scratch, script_line, .{});
    const got = decodeWire(scratch, script, wire);
    if (want) |_| {
        try got;
    } else |expected| {
        try std.testing.expectError(expected, got);
    }
}

fn expectInbound(wire: []const u8, want: anyerror!void) !void {
    try expectWire("{\"direction\":\"pi-to-host\"}", wire, want);
}

test "an inbound frame the production codec refuses is a corpus defect" {
    try expectInbound("{\"type\":\"turn_start\"}", {});
    try expectInbound("{\"type\":\"turn_end\"}", error.ProductionCodecRefusedCorpusFrame);
    try expectInbound("{\"type\":\"prompt\",\"message\":\"hi\"}", error.ProductionCodecRefusedCorpusFrame);
}

test "an inbound command response is decoded, not waved through as outbound" {
    try expectInbound("{\"type\":\"response\",\"command\":\"steer\",\"success\":true}", {});
    try expectInbound(
        "{\"type\":\"response\",\"command\":\"nonesuch\",\"success\":true}",
        error.ProductionCodecRefusedCorpusFrame,
    );
    try expectInbound(
        "{\"type\":\"response\",\"command\":\"steer\",\"success\":true,\"error\":\"boom\"}",
        error.ProductionCodecRefusedCorpusFrame,
    );
}

test "an outbound command and harness control carry no frame this port decodes" {
    for ([_][]const u8{ "{\"direction\":\"host-to-pi\"}", "{\"direction\":\"harness-control\"}" }) |script| {
        try expectWire(script, "{\"type\":\"prompt\"}", {});
        try expectWire(script, "{\"type\":\"get_state\"}", {});
        try expectWire(script, "{\"type\":\"steer\",\"message\":\"adjust\"}", {});
        try expectWire(script, "{\"type\":\"process_exit\"}", {});
    }
}

test "a script line whose direction this harness does not know is refused" {
    try expectWire("{\"direction\":\"nonesuch\"}", "{\"type\":\"turn_start\"}", error.UnroutedScriptDirection);
    try expectWire("{}", "{\"type\":\"turn_start\"}", error.UnroutedScriptDirection);
    try expectWire("{\"direction\":7}", "{\"type\":\"turn_start\"}", error.UnroutedScriptDirection);
    try expectWire("[]", "{\"type\":\"turn_start\"}", error.UnroutedScriptDirection);
}

fn expectControl(script_line: []const u8, raw_line: []const u8, want: anyerror!void) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const script = try std.json.parseFromSliceLeaky(std.json.Value, scratch, script_line, .{});
    const raw = try std.json.parseFromSliceLeaky(std.json.Value, scratch, raw_line, .{});
    const got = checkControl(script, raw);
    if (want) |_| {
        try got;
    } else |expected| {
        try std.testing.expectError(expected, got);
    }
}

test "a harness control frame must be a process exit reporting why" {
    const exit_script = "{\"direction\":\"harness-control\",\"action\":\"process-exit\"}";
    try expectControl(exit_script, "{\"type\":\"process_exit\",\"error\":\"boom\"}", {});
    try expectControl(exit_script, "{\"type\":\"process_exit\",\"error\":\"\"}", error.InvalidHarnessControl);
    try expectControl(exit_script, "{\"type\":\"process_exit\"}", error.InvalidHarnessControl);
    try expectControl(exit_script, "{\"type\":\"turn_start\",\"error\":\"boom\"}", error.InvalidHarnessControl);
    try expectControl(exit_script, "[]", error.InvalidHarnessControl);
    try expectControl(
        "{\"direction\":\"harness-control\",\"action\":\"observe\"}",
        "{\"type\":\"process_exit\",\"error\":\"boom\"}",
        error.UnroutedHarnessControlAction,
    );
}

test "a control frame check ignores the directions that carry a wire frame" {
    try expectControl("{\"direction\":\"pi-to-host\",\"action\":\"observe\"}", "{\"type\":\"turn_start\"}", {});
    try expectControl("{\"direction\":\"host-to-pi\",\"action\":\"state\"}", "{\"type\":\"get_state\"}", {});
}

test "an inbound frame recorded as a decode error must be refused, not accepted" {
    const refusing = "{\"direction\":\"pi-to-host\",\"action\":\"decode-error\"}";
    try expectWire(refusing, "{\"type\":\"turn_end\"}", {});
    try expectWire(refusing, "{\"type\":\"turn_start\"}", error.ProductionCodecAcceptedInvalidFrame);
}

test "a script line the production codec refuses fails the case at the call site" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const inline_case = CorpusCase{ .id = "inline", .path = "inline" };

    var reducer = session.Reducer.init(scratch);
    const refused_line = "{\"direction\":\"pi-to-host\",\"action\":\"observe\",\"raw\":{\"type\":\"turn_end\"}}\n";
    try std.testing.expectError(
        error.ProductionCodecRefusedCorpusFrame,
        Harness.steps(scratch, refused_line, &reducer, inline_case),
    );

    var accepted_reducer = session.Reducer.init(scratch);
    const accepted = "{\"direction\":\"pi-to-host\",\"action\":\"observe\",\"raw\":{\"type\":\"turn_start\"}}\n";
    try Harness.steps(scratch, accepted, &accepted_reducer, inline_case);
}

test "an outbound script line still reaches the reducer with its frame undecoded" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const inline_case = CorpusCase{ .id = "inline", .path = "inline" };

    var reducer = session.Reducer.init(scratch);
    const outbound = "{\"direction\":\"host-to-pi\",\"action\":\"outbound-only\",\"raw\":{\"type\":\"steer\",\"message\":\"adjust\"}}\n";
    try Harness.steps(scratch, outbound, &reducer, inline_case);
}
