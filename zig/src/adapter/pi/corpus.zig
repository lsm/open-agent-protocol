const std = @import("std");
const corpus = @import("corpus");
const session = @import("session.zig");

pub const CorpusCase = struct { id: []const u8, path: []const u8 };

const Driver = struct {
    pub const corpus_relative = "fixtures/adapters/pi-v0.85.1";
    pub const blank_expectation = corpus.BlankExpectation.zero_byte_only;
    pub const Reducer = session.Reducer;
    pub const Case = CorpusCase;

    pub fn open(arena: *std.heap.ArenaAllocator, case: CorpusCase) Reducer {
        _ = case;
        var reducer = session.Reducer.init(arena.allocator());
        session.open(&reducer) catch {};
        return reducer;
    }

    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, item: corpus.Step, case: CorpusCase) !corpus.Handled {
        _ = scratch;
        _ = case;
        if (std.mem.eql(u8, item.action, "outbound-only")) return .handled;
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

test "pi completed-text replays to the recorded envelopes" {
    try Harness.expectEveryCase(std.testing.allocator, &.{
        .{ .id = "completed-text", .path = "completed-text" },
    });
}
