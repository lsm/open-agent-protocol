const std = @import("std");
const corpus = @import("corpus");
const session = @import("session.zig");

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
        _ = scratch;
        _ = case;
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
