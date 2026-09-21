const std = @import("std");
const corpus = @import("corpus");
const session = @import("session.zig");
const rpc = @import("rpc.zig");

const Routing = enum { agent_frame, host_command, harness_control, either_direction };

const Route = struct { action: []const u8, routing: Routing };

const routes = [_]Route{
    .{ .action = "", .routing = .agent_frame },
    .{ .action = "observe", .routing = .agent_frame },
    .{ .action = "observe-extension", .routing = .agent_frame },
    .{ .action = "resolve-extension", .routing = .agent_frame },
    .{ .action = "cancel", .routing = .agent_frame },
    .{ .action = "state", .routing = .host_command },
    .{ .action = "prompt-failure", .routing = .host_command },
    .{ .action = "decode-error", .routing = .host_command },
    .{ .action = "process-exit", .routing = .harness_control },
    .{ .action = "outbound-only", .routing = .either_direction },
};

fn routingOf(action: []const u8) ?Routing {
    for (routes) |entry| {
        if (std.mem.eql(u8, entry.action, action)) return entry.routing;
    }
    return null;
}

fn decodeWire(scratch: std.mem.Allocator, action: []const u8, wire: []const u8) !void {
    const routing = routingOf(action) orelse return error.UnroutedScriptAction;
    if (routing != .agent_frame) return;

    const source = try std.mem.concat(scratch, u8, &.{ wire, "\n" });
    var decoder = rpc.Decoder{ .source = source };
    const decoded = decoder.next(scratch) catch return error.ProductionCodecRefusedCorpusFrame;
    _ = decoded orelse return error.ProductionCodecYieldedNoFrame;
}

fn decodeFrame(scratch: std.mem.Allocator, item: corpus.Step) !void {
    return decodeWire(scratch, item.action, item.encoded);
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
            try session.resolveExtension(reducer, "yes");
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

test "an agent frame the production codec refuses is a corpus defect" {
    for ([_][]const u8{ "", "observe", "observe-extension", "resolve-extension", "cancel" }) |action| {
        try expectWire(action, "{\"type\":\"turn_start\"}", {});
        try expectWire(action, "{\"type\":\"turn_end\"}", error.ProductionCodecRefusedCorpusFrame);
        try expectWire(action, "{\"type\":\"prompt\",\"message\":\"hi\"}", error.ProductionCodecRefusedCorpusFrame);
    }
}

test "host commands and harness control carry no frame this port decodes" {
    try expectWire("decode-error", "{\"type\":\"prompt\"}", {});
    try expectWire("state", "{\"type\":\"get_state\"}", {});
    try expectWire("outbound-only", "{\"type\":\"steer\",\"message\":\"adjust\"}", {});
    try expectWire("process-exit", "{\"type\":\"process_exit\"}", {});
}

test "an action outside the routing table is refused" {
    try expectWire("nonesuch", "{\"type\":\"turn_start\"}", error.UnroutedScriptAction);
}
