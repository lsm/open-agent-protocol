const std = @import("std");
const corpus = @import("corpus");
const pins = @import("harness_pins");
const native = @import("native");
const session = @import("copilot_session");

const CorpusCase = struct { id: []const u8, path: []const u8 };
const Driver = struct {
    pub const Reducer = session.Reducer;
    pub const Case = CorpusCase;
    pub const corpus_relative = pins.copilot_cli_corpus;
    pub const blank_expectation = corpus.BlankExpectation.zero_byte_or_empty_array;
    pub fn open(arena: *std.heap.ArenaAllocator, case: Case) Reducer {
        _ = case;
        return session.Reducer.init(arena, .{ .session_id = "s1", .native_id = "ses", .revision = pins.copilot_cli_capability_revision });
    }
    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, item: corpus.Step, case: Case) !corpus.Handled {
        _ = case;
        const direction = corpus.stringMember(item.script.object, "direction") orelse return error.InvalidCorpusDirection;
        const outbound = std.mem.eql(u8, item.action, "admit");
        if (!std.mem.eql(u8, direction, if (outbound) "host-to-copilot" else "copilot-to-host")) return error.InvalidCorpusDirection;
        if (std.mem.eql(u8, item.action, "reject-wire")) {
            try std.testing.expectError(error.InvalidWire, native.parseMessage(scratch, item.source));
            return .handled;
        }
        const message = try native.parseMessage(scratch, item.source);
        if (std.mem.eql(u8, item.action, "reject-event")) {
            try std.testing.expectError(error.InvalidWire, native.decodeEvent(message));
        } else if (std.mem.eql(u8, item.action, "admit")) {
            if (message.kind != .request or !std.mem.eql(u8, message.method, "session.send")) return error.InvalidAdmission;
            _ = try reducer.admit("s1");
        } else if (std.mem.eql(u8, item.action, "bind")) {
            if (message.kind != .response or message.result == null or message.result.? != .object) return error.InvalidBinding;
            const id = native.text(message.result.?.object, "messageId");
            if (id.len == 0 or reducer.runs.items.len == 0) return error.InvalidBinding;
            try reducer.bind(reducer.runs.items[reducer.runs.items.len - 1].id, id);
        } else if (std.mem.eql(u8, item.action, "observe")) {
            const event = try native.decodeEvent(message) orelse return error.InvalidEvent;
            try reducer.observe(event);
        } else return .unhandled;
        return .handled;
    }
    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.envelopes.items;
    }
};
const Harness = corpus.Harness(Driver);

test "the synthetic Copilot cases replay through the native decoder and reducer" {
    try Harness.expectEveryCase(std.testing.allocator, &.{
        .{ .id = "completed-text", .path = "completed-text" },
        .{ .id = "reasoning-usage", .path = "reasoning-usage" },
        .{ .id = "queued-busy-period", .path = "queued-busy-period" },
        .{ .id = "tool-success", .path = "tool-success" },
        .{ .id = "tool-denied", .path = "tool-denied" },
        .{ .id = "session-error", .path = "session-error" },
        .{ .id = "foreign-session", .path = "foreign-session" },
        .{ .id = "unknown-event", .path = "unknown-event" },
        .{ .id = "invalid-wire", .path = "invalid-wire" },
        .{ .id = "invalid-event", .path = "invalid-event" },
        .{ .id = "usage-sparse", .path = "usage-sparse" },
        .{ .id = "usage-precision", .path = "usage-precision" },
        .{ .id = "usage-wide", .path = "usage-wide" },
        .{ .id = "usage-partial-call", .path = "usage-partial-call" },
        .{ .id = "usage-overflow", .path = "usage-overflow" },
        .{ .id = "usage-negative-zero", .path = "usage-negative-zero" },
        .{ .id = "cost-wide", .path = "cost-wide" },
        .{ .id = "cost-unrepresentable", .path = "cost-unrepresentable" },
    });
}

test "the replay refuses a response labelled as an outbound request" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const case = CorpusCase{ .id = "inline", .path = "inline" };
    var reducer = Driver.open(&arena, case);
    try std.testing.expectError(error.InvalidCorpusDirection, Harness.steps(arena.allocator(), "{\"direction\":\"host-to-copilot\",\"action\":\"bind\",\"raw\":{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"messageId\":\"m1\"}}}\n", &reducer, case));
}
