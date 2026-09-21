const std = @import("std");
const corpus = @import("corpus");
const session = @import("session.zig");

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
        _ = scratch;
        _ = case;
        const action = item.action;
        if (std.mem.eql(u8, action, "submit")) {
            try session.submit(reducer);
            return .handled;
        }
        if (std.mem.eql(u8, action, "wait-submit")) return .handled;
        if (std.mem.eql(u8, action, "oap-control")) return .handled;
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
            if (std.mem.eql(u8, method, "session.event")) {
                const event = params.object.get("event") orelse return .handled;
                try session.observe(reducer, event);
                return .handled;
            }
            if (std.mem.eql(u8, method, "session.status")) {
                try session.observeStatus(reducer, corpus.stringMember(params.object, "status") orelse "");
                return .handled;
            }
            return .handled;
        }
        return .unhandled;
    }

    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.envelopes();
    }
};

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
