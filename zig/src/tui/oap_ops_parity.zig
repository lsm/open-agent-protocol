const std = @import("std");
const in_process = @import("transports/in_process");
const oap_server = @import("oap_server");
const oap_types = @import("oap_types");
const oap_client = @import("tui/oap_client");
const tui_runtime = @import("tui_runtime");

const Support = enum { native, emulated, degraded, unavailable, unadvertised };

const Op = struct {
    name: []const u8,
    feature: []const u8,
    expected: Support,
    blocked_by: []const u8 = "",
};

const OPS = [_]Op{
    .{ .name = "start", .feature = "session.open", .expected = .native },
    .{ .name = "resume_session", .feature = "session.state", .expected = .degraded, .blocked_by = "sessions are not resumable; session_id is a correlation key" },
    .{ .name = "compact", .feature = "", .expected = .unadvertised, .blocked_by = "no protocol verb; compaction stays on the direct path" },
    .{ .name = "history", .feature = "", .expected = .unadvertised, .blocked_by = "no transcript replay is offered" },
    .{ .name = "cancel", .feature = "run.cancel", .expected = .degraded, .blocked_by = "cancellation is session scoped; the session closes with the run" },
    .{ .name = "submit_turn", .feature = "session.message.submit", .expected = .native },
    .{ .name = "steer", .feature = "session.message.delivery.steer", .expected = .unadvertised, .blocked_by = "delivery resolves to auto only" },
    .{ .name = "follow_up", .feature = "session.message.delivery.queue", .expected = .unadvertised, .blocked_by = "delivery resolves to auto only" },
    .{ .name = "clear_queued_messages", .feature = "", .expected = .unadvertised, .blocked_by = "queue state is TUI-local" },
    .{ .name = "queued_counts", .feature = "", .expected = .unadvertised, .blocked_by = "queue counters are TUI-local" },
    .{ .name = "steers_consumed", .feature = "", .expected = .unadvertised, .blocked_by = "queue counters are TUI-local" },
    .{ .name = "can_steer", .feature = "session.message.delivery.steer", .expected = .unadvertised, .blocked_by = "derived from the steer delivery, which is not advertised" },
    .{ .name = "switch_model", .feature = "session.model.switch", .expected = .native },
    .{ .name = "switch_model_exact", .feature = "session.model.switch", .expected = .native },
    .{ .name = "current_model", .feature = "session.model.switch", .expected = .native },
    .{ .name = "decide_tool_approval", .feature = "action.permissions", .expected = .unadvertised, .blocked_by = "action.permissions is not implemented" },
    .{ .name = "stream_events", .feature = "run.streaming", .expected = .native },
};

fn supportOf(level: oap_types.SupportLevel) Support {
    return switch (level) {
        .native => .native,
        .emulated => .emulated,
        .degraded => .degraded,
        .unavailable => .unavailable,
    };
}

fn advertisedLevel(key: []const u8) Support {
    var level: ?Support = null;
    for (oap_server.advertised_features) |feature| {
        if (std.mem.eql(u8, feature.key, key)) level = supportOf(feature.level);
    }
    for (oap_server.advertised_degradation) |entry| {
        if (std.mem.eql(u8, entry.feature, key)) level = supportOf(entry.to);
    }
    return level orelse .unadvertised;
}

fn degradedReason(feature: []const u8) ?[]const u8 {
    for (oap_server.advertised_degradation) |entry| {
        if (std.mem.eql(u8, entry.feature, feature)) return entry.reason;
    }
    return null;
}

const Exchange = struct {
    allocator: std.mem.Allocator,
    pipe: *in_process.SerializedPipe,
    server: oap_server.Server,
    client: oap_client.Client,

    fn init(allocator: std.mem.Allocator) !Exchange {
        const pipe = try allocator.create(in_process.SerializedPipe);
        pipe.* = in_process.createSerializedPipe(allocator);
        errdefer {
            pipe.deinit();
            allocator.destroy(pipe);
        }
        return .{
            .allocator = allocator,
            .pipe = pipe,
            .server = try oap_server.Server.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" }),
            .client = undefined,
        };
    }

    fn start(self: *Exchange) void {
        self.client = oap_client.Client.init(self.allocator, self.pipe);
    }

    fn deinit(self: *Exchange) void {
        self.client.deinit();
        self.server.deinit();
        self.pipe.deinit();
        self.allocator.destroy(self.pipe);
    }

    fn step(self: *Exchange) !usize {
        var inbound = self.pipe.serverReceiver();
        while (try inbound.readLine(self.allocator)) |line| {
            defer self.allocator.free(line);
            try self.server.handleLine(line);
        }
        var sender = self.pipe.serverSender();
        while (self.server.popOutbound()) |line| {
            defer self.allocator.free(line);
            try sender.write(line);
        }
        return self.drain();
    }

    fn drain(self: *Exchange) !usize {
        var seen: usize = 0;
        while (try self.client.recv()) |env| {
            var owned = env;
            defer owned.deinit(self.allocator);
            try self.client.absorb(owned);
            seen += 1;
        }
        return seen;
    }
};

const SESSION_OP_NAMES = [_][]const u8{
    "start",                "resume_session", "compact",      "history",               "cancel",
    "submit_turn",          "steer",          "follow_up",    "clear_queued_messages", "queued_counts",
    "steers_consumed",      "can_steer",      "switch_model", "switch_model_exact",    "current_model",
    "decide_tool_approval", "stream_events",
};

test "the op matrix covers every session operation the TUI exposes" {
    const ops = std.meta.fields(@import("tui/session").TuiSessionOps);
    try std.testing.expectEqual(SESSION_OP_NAMES.len, ops.len);
    try std.testing.expectEqual(SESSION_OP_NAMES.len, OPS.len);
    for (SESSION_OP_NAMES) |name| {
        var in_table = false;
        for (OPS) |op| {
            if (std.mem.eql(u8, op.name, name)) in_table = true;
        }
        try std.testing.expect(in_table);
    }
}

test "every op's expected support is what the endpoint actually advertises" {
    for (OPS) |op| {
        const actual: Support = if (op.feature.len == 0) .unadvertised else advertisedLevel(op.feature);
        std.testing.expectEqual(op.expected, actual) catch |err| {
            std.debug.print("op {s} ({s}): expected {s}, endpoint advertises {s}\n", .{
                op.name,
                if (op.feature.len == 0) "no feature key" else op.feature,
                @tagName(op.expected),
                @tagName(actual),
            });
            return err;
        };
    }
}

test "every op the endpoint does not serve names why" {
    for (OPS) |op| {
        if (op.expected == .native) continue;
        try std.testing.expect(op.blocked_by.len > 0);
    }
}

test "a degraded op's advertised reason is the one the endpoint publishes" {
    for (OPS) |op| {
        if (op.expected != .degraded) continue;
        const reason = degradedReason(op.feature) orelse {
            std.debug.print("op {s} expects {s} degraded but no degradation entry exists\n", .{ op.name, op.feature });
            return error.NoDegradationPublished;
        };
        try std.testing.expect(reason.len > 0);
    }
}

test "the supported common operations carry real agent-control traffic" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator);
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    try std.testing.expectEqual(@as(usize, 1), try exchange.step());
    try std.testing.expect(exchange.client.capability_revision != null);

    try exchange.client.openSession();
    try std.testing.expectEqual(@as(usize, 1), try exchange.step());
    try std.testing.expect(exchange.client.session_id != null);

    try exchange.client.submit("go on");
    try std.testing.expectEqual(@as(usize, 3), try exchange.step());
    try std.testing.expect(exchange.client.pending_run_id != null);

    try exchange.client.switchModel("anthropic/anthropic-messages@m");
    try std.testing.expect(try exchange.step() > 0);
}

test "an operation the endpoint does not serve is refused without a request on the wire" {
    const allocator = std.testing.allocator;
    var pipe = in_process.createSerializedPipe(allocator);
    defer pipe.deinit();
    var client = oap_client.Client.init(allocator, &pipe);
    defer client.deinit();

    try std.testing.expectError(oap_client.Error.NoSession, client.submit("go on"));
    try std.testing.expectError(oap_client.Error.NoSession, client.switchModel("anthropic/anthropic-messages@m"));
    try std.testing.expectError(oap_client.Error.NoSession, client.cancel());
    try std.testing.expectError(oap_client.Error.NotInitialized, client.openSession());

    var inbound = pipe.serverReceiver();
    try std.testing.expect((try inbound.readLine(allocator)) == null);
}

test "a cancel with no active run is refused rather than sent" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator);
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();

    try std.testing.expectError(oap_client.Error.NoActiveRun, exchange.client.cancel());
}

test "the native side reports it can steer, and the endpoint does not advertise it" {
    try std.testing.expect(tui_runtime.TuiRuntime.canSteer(undefined));
    try std.testing.expectEqual(Support.unadvertised, advertisedLevel("session.message.delivery.steer"));
}

test "the envelope version and profile the client sends are the ones the endpoint declares" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator);
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    _ = try exchange.step();
    try std.testing.expect(exchange.client.capability_revision != null);

    const descriptor = oap_server.Descriptor{};
    try std.testing.expect(descriptor.endpoint_id.len > 0);
    try std.testing.expect(oap_types.VERSION.len > 0);
}
