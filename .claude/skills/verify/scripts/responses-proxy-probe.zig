const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const events = @import("event_stream");
const responses = @import("openai_responses_api");

pub fn main(init: std.process.Init) void {
    perform(init) catch {
        std.Io.File.stdout().writeStreamingAll(init.io, "FAILED\n") catch {};
        std.process.exit(1);
    };
}

fn perform(init: std.process.Init) !void {
    const client = try compat.http.sharedClient();
    const now = std.Io.Clock.real.now(client.client.io);
    try client.client.ca_bundle.addCertsFromFilePathAbsolute(client.client.allocator, client.client.io, now, init.environ_map.get("TEST_CA_FILE").?);
    client.client.now = now;
    const count = try std.fmt.parseInt(usize, init.environ_map.get("TEST_STREAMS") orelse "1", 10);
    var streams: std.ArrayList(*events.AssistantMessageEventStream) = .empty;
    defer {
        for (streams.items) |stream| {
            stream.deinit();
            init.gpa.destroy(stream);
        }
        streams.deinit(init.gpa);
    }
    try streams.ensureTotalCapacity(init.gpa, count);
    const messages = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "sensitive-prompt-fixture" }, .timestamp = 0 } }};
    for (0..count) |_| {
        const stream = try responses.streamOpenAIResponses(.{
            .id = "fixture-model",
            .name = "Fixture model",
            .api = "openai-codex-responses",
            .provider = "synthetic",
            .base_url = init.environ_map.get("TEST_URL").?,
            .reasoning = false,
            .allows_anonymous = true,
            .input = &.{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 1024,
            .max_tokens = 16,
        }, .{ .messages = &messages }, null, init.gpa);
        streams.appendAssumeCapacity(stream);
        try std.Io.File.stdout().writeStreamingAll(init.io, "INFERENCE\n");
    }
    for (streams.items) |stream| {
        while (stream.wait()) |event| {
            var owned = event;
            defer ai_types.deinitAssistantMessageEvent(init.gpa, &owned);
            if (event == .text_delta) try std.Io.File.stdout().writeStreamingAll(init.io, "DELTA\n");
        }
        if (!stream.waitForThread(5_000)) return error.StreamThreadIncomplete;
        if (stream.getError() != null or stream.getResult() == null) return error.InferenceFailed;
        try std.Io.File.stdout().writeStreamingAll(init.io, "SUCCESS\n");
    }
}
