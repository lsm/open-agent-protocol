const std = @import("std");
const in_process = @import("transports/in_process");
const oap_server = @import("oap_server");
const oap_types = @import("oap_types");
const oap_client = @import("tui/oap_client");
const oap_envelope = @import("oap_envelope");
const tui_session = @import("tui/session");
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
    replies: std.ArrayList(oap_types.Envelope) = .empty,

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
        for (self.replies.items) |*reply| reply.deinit(self.allocator);
        self.replies.deinit(self.allocator);
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
            errdefer owned.deinit(self.allocator);
            try self.client.absorb(owned);
            try self.replies.append(self.allocator, owned);
            seen += 1;
        }
        return seen;
    }
};

fn findOpenReply(replies: []const oap_types.Envelope) ?oap_types.SessionState {
    for (replies) |reply| {
        if (reply.payload == .session_open_response) return reply.payload.session_open_response;
    }
    return null;
}

fn findSubmitReply(replies: []const oap_types.Envelope) ?oap_types.MessageSubmitResponse {
    for (replies) |reply| {
        if (reply.payload == .message_submit_response) return reply.payload.message_submit_response;
    }
    return null;
}

fn findSwitchReply(replies: []const oap_types.Envelope) ?oap_types.SessionModelSwitchResponse {
    for (replies) |reply| {
        if (reply.payload == .session_model_switch_response) return reply.payload.session_model_switch_response;
    }
    return null;
}

fn findStateUpdate(replies: []const oap_types.Envelope) ?oap_types.SessionState {
    for (replies) |reply| {
        if (reply.payload == .session_state_updated) return reply.payload.session_state_updated;
    }
    return null;
}

test "the op matrix covers every session operation the TUI exposes" {
    const ops = std.meta.fields(tui_session.TuiSessionOps);
    try std.testing.expectEqual(OPS.len, ops.len);
    inline for (ops) |field| {
        var in_matrix = false;
        for (OPS) |op| {
            if (std.mem.eql(u8, op.name, field.name)) in_matrix = true;
        }
        try std.testing.expect(in_matrix);
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

test "a session open replies with the session the client adopted, and answers its request" {
    var exchange = try Exchange.init(std.testing.allocator);
    defer exchange.deinit();
    exchange.start();
    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();

    const reply = findOpenReply(exchange.replies.items) orelse return error.NoSessionOpenReply;
    try std.testing.expectEqualStrings(reply.session_id, exchange.client.session_id.?);
    try std.testing.expect(reply.status == .idle);
    try std.testing.expect(reply.active_run_id == null);
    try std.testing.expect(reply.current_model_id != null);
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstanding.count());
}

test "a submitted turn is admitted with a run the client adopted, and auto delivery never becomes steer or queue" {
    var exchange = try Exchange.init(std.testing.allocator);
    defer exchange.deinit();
    exchange.start();
    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();
    try exchange.client.submit("go on");
    _ = try exchange.step();

    const reply = findSubmitReply(exchange.replies.items) orelse return error.NoSubmitReply;
    try std.testing.expect(reply.accepted);
    try std.testing.expectEqualStrings(reply.session_id, exchange.client.session_id.?);
    const run_id = reply.run_id orelse return error.SubmitReplyCarriedNoRun;
    try std.testing.expectEqualStrings(run_id, exchange.client.pending_run_id.?);
    try std.testing.expect(!std.mem.eql(u8, run_id, reply.session_id));
    try std.testing.expect(reply.requested_delivery == .auto);
    try std.testing.expect(reply.effective_delivery == .start);
    try std.testing.expect(reply.admission == .started);
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstanding.count());
}

test "a model switch reports the model the session moved to, and the one it left" {
    var exchange = try Exchange.init(std.testing.allocator);
    defer exchange.deinit();
    exchange.start();
    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();

    const opened = findOpenReply(exchange.replies.items) orelse return error.NoSessionOpenReply;
    const before = opened.current_model_id.?;
    try exchange.server.addModel("anthropic/anthropic-messages@second");
    const target = "anthropic/anthropic-messages@second";
    try exchange.client.switchModel(target);
    _ = try exchange.step();

    const reply = findSwitchReply(exchange.replies.items) orelse return error.NoModelSwitchReply;
    try std.testing.expectEqualStrings(reply.session_id, exchange.client.session_id.?);
    try std.testing.expectEqualStrings(reply.model_id, target);
    const previous = reply.previous_model_id orelse return error.SwitchReplyCarriedNoPreviousModel;
    try std.testing.expectEqualStrings(previous, before);
    try std.testing.expect(!std.mem.eql(u8, previous, target));
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstanding.count());

    const state = findStateUpdate(exchange.replies.items) orelse return error.NoSessionStateUpdate;
    try std.testing.expectEqualStrings(state.current_model_id.?, target);
    try std.testing.expectEqualStrings(state.session_id, exchange.client.session_id.?);
    try std.testing.expect(state.status == .idle);
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

test "an initialize that declares a version the endpoint does not serve is refused" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator);
    defer exchange.deinit();
    exchange.start();

    const versions = [_][]const u8{"0.0.0-not-a-version"};
    const profiles = [_][]const u8{oap_types.PROFILE};
    const envelope = oap_types.Envelope{
        .id = "req-bogus-version",
        .payload = .{ .initialize_request = .{
            .protocol_versions = &versions,
            .profiles = &profiles,
        } },
    };
    const line = try oap_envelope.serializeEnvelope(envelope, allocator);
    defer allocator.free(line);
    var sender = exchange.pipe.clientSender();
    try sender.write(line);

    var inbound = exchange.pipe.serverReceiver();
    while (try inbound.readLine(allocator)) |request| {
        defer allocator.free(request);
        try exchange.server.handleLine(request);
    }

    var refused_unsupported = false;
    var emitted: usize = 0;
    while (exchange.server.popOutbound()) |reply| {
        defer allocator.free(reply);
        emitted += 1;
        var parsed = try oap_envelope.deserializeEnvelope(reply, allocator);
        defer parsed.deinit(allocator);
        const payload = parsed.payload;
        if (payload != .error_response) continue;
        const code = payload.error_response.code;
        if (std.mem.eql(u8, code, oap_types.EmittedErrorCode.unsupported_feature.text())) refused_unsupported = true;
    }
    try std.testing.expect(emitted > 0);
    try std.testing.expect(refused_unsupported);
}

test "the client's own initialize is accepted, so the version it sends is the served one" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator);
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    try std.testing.expectEqual(@as(usize, 1), try exchange.step());
    try std.testing.expect(exchange.client.capability_revision != null);
}
