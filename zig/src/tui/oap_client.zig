const std = @import("std");
const in_process = @import("transports/in_process");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");

const PipeTransport = in_process.SerializedPipe;

pub const Error = error{
    NoSession,
    NoActiveRun,
    NotInitialized,
};

pub const Client = struct {
    allocator: std.mem.Allocator,
    pipe: *PipeTransport,
    next_request_id: u64 = 0,
    outstanding: std.StringHashMapUnmanaged(void) = .empty,
    session_id: ?[]u8 = null,
    capability_revision: ?[]u8 = null,
    pending_run_id: ?[]u8 = null,
    unanswered: u64 = 0,
    last_error: ?[]u8 = null,
    error_responses: u64 = 0,

    pub fn init(allocator: std.mem.Allocator, pipe: *PipeTransport) Client {
        return .{ .allocator = allocator, .pipe = pipe };
    }

    fn sender(self: *const Client) @import("transport").AsyncSender {
        return self.pipe.clientSender();
    }

    pub fn deinit(self: *Client) void {
        var it = self.outstanding.keyIterator();
        while (it.next()) |key| self.allocator.free(key.*);
        self.outstanding.deinit(self.allocator);
        if (self.session_id) |id| self.allocator.free(id);
        if (self.capability_revision) |rev| self.allocator.free(rev);
        if (self.pending_run_id) |id| self.allocator.free(id);
        if (self.last_error) |message| self.allocator.free(message);
        self.* = undefined;
    }

    pub fn outstandingCount(self: *const Client) usize {
        return self.outstanding.count();
    }

    fn requestId(self: *Client) ![]const u8 {
        self.next_request_id += 1;
        const id = try std.fmt.allocPrint(self.allocator, "tui-req-{d}", .{self.next_request_id});
        errdefer self.allocator.free(id);
        try self.outstanding.put(self.allocator, id, {});
        return id;
    }

    fn settleRequest(self: *Client, id: []const u8) bool {
        if (self.outstanding.fetchRemove(id)) |entry| {
            self.allocator.free(entry.key);
            return true;
        }
        return false;
    }

    pub fn send(self: *Client, envelope: oap_types.Envelope) !void {
        const line = try oap_envelope.serializeEnvelope(envelope, self.allocator);
        defer self.allocator.free(line);
        try self.sender().write(line);
    }

    pub fn recv(self: *Client) !?oap_types.Envelope {
        var receiver = self.pipe.clientReceiver();
        const line = (try receiver.readLine(self.allocator)) orelse {
            self.pipe.compact();
            return null;
        };
        defer self.allocator.free(line);
        return oap_envelope.deserializeEnvelope(line, self.allocator) catch |err| switch (err) {
            error.OutOfMemory => error.OutOfMemory,
            else => error.MalformedLine,
        };
    }

    pub fn initialize(self: *Client) !void {
        const versions = [_][]const u8{oap_types.VERSION};
        const profiles = [_][]const u8{oap_types.PROFILE};
        const id = try self.requestId();
        try self.sendRequest(.{ .id = id, .payload = .{ .initialize_request = .{
            .protocol_versions = &versions,
            .profiles = &profiles,
        } } });
    }

    pub fn openSession(self: *Client) !void {
        if (self.capability_revision == null) return Error.NotInitialized;
        const id = try self.requestId();
        try self.sendRequest(.{
            .id = id,
            .capability_revision = self.capability_revision,
            .payload = .{ .session_open_request = .{} },
        });
    }

    pub fn submit(self: *Client, text: []const u8) !void {
        const session_id = self.session_id orelse return Error.NoSession;
        const id = try self.requestId();
        var parts = [_]oap_types.ContentPart{.{ .text = text }};
        var messages = [_]oap_types.Message{.{ .role = .user, .content = .{ .parts = &parts } }};
        try self.sendRequest(.{
            .id = id,
            .session_id = session_id,
            .capability_revision = self.capability_revision,
            .payload = .{ .message_submit_request = .{
                .session_id = session_id,
                .messages = &messages,
                .delivery = .auto,
            } },
        });
    }

    pub fn switchModel(self: *Client, model_id: []const u8) !void {
        const session_id = self.session_id orelse return Error.NoSession;
        const id = try self.requestId();
        try self.sendRequest(.{
            .id = id,
            .session_id = session_id,
            .capability_revision = self.capability_revision,
            .payload = .{ .session_model_switch_request = .{ .session_id = session_id, .model_id = model_id } },
        });
    }

    pub fn cancel(self: *Client) !void {
        const session_id = self.session_id orelse return Error.NoSession;
        const run_id = self.pending_run_id orelse return Error.NoActiveRun;
        const id = try self.requestId();
        try self.sendRequest(.{
            .id = id,
            .session_id = session_id,
            .run_id = run_id,
            .capability_revision = self.capability_revision,
            .payload = .{ .run_cancel_request = .{ .session_id = session_id, .run_id = run_id } },
        });
    }

    pub fn absorb(self: *Client, envelope: oap_types.Envelope) !void {
        if (envelope.in_reply_to) |reply_to| {
            if (!self.settleRequest(reply_to)) {
                self.unanswered += 1;
                return;
            }
        }

        switch (envelope.payload) {
            .initialize_response, .capabilities_response, .capabilities_updated => {
                if (envelope.capability_revision) |revision| try self.rememberRevision(revision);
            },
            else => {},
        }

        switch (envelope.payload) {
            .session_open_response => |payload| try self.rememberSessionId(payload.session_id),
            .message_submit_response => |payload| {
                if (payload.accepted) {
                    if (payload.run_id) |run_id| try self.rememberRunId(run_id);
                }
            },
            .run_status_updated, .content_delta => try self.confirmRun(envelope.run_id),
            .run_completed => |payload| try self.releaseRun(payload.run_id),
            .run_failed => |payload| try self.releaseRun(payload.run_id),
            .run_cancelled => |payload| try self.releaseRun(payload.run_id),
            .session_state_updated => |payload| {
                if (payload.status == .closed) try self.closeSession();
            },
            .error_response => |payload| try self.rememberError(payload),
            else => {},
        }
    }

    pub fn closeSession(self: *Client) !void {
        if (self.session_id) |id| {
            self.allocator.free(id);
            self.session_id = null;
        }
        self.forgetRun();
    }

    fn rememberError(self: *Client, payload: oap_types.ProtocolError) !void {
        const owned = try self.allocator.dupe(u8, payload.code);
        if (self.last_error) |old| self.allocator.free(old);
        self.last_error = owned;
        self.error_responses += 1;
    }

    fn confirmRun(self: *Client, run_id: ?[]const u8) !void {
        const id = run_id orelse return;
        const pending = self.pending_run_id orelse return self.rememberRunId(id);
        if (!std.mem.eql(u8, pending, id)) return;
    }

    pub fn releaseRun(self: *Client, run_id: []const u8) !void {
        const pending = self.pending_run_id orelse return;
        if (!std.mem.eql(u8, pending, run_id)) return;
        self.allocator.free(pending);
        self.pending_run_id = null;
    }

    pub fn takeSessionId(self: *Client) ?[]u8 {
        const owned = self.session_id orelse return null;
        self.session_id = null;
        return owned;
    }

    pub fn rememberSessionId(self: *Client, id: []const u8) !void {
        const owned = try self.allocator.dupe(u8, id);
        if (self.session_id) |old| self.allocator.free(old);
        self.session_id = owned;
    }

    pub fn rememberRevision(self: *Client, revision: []const u8) !void {
        const owned = try self.allocator.dupe(u8, revision);
        if (self.capability_revision) |old| self.allocator.free(old);
        self.capability_revision = owned;
    }

    pub fn rememberRunId(self: *Client, id: []const u8) !void {
        const owned = try self.allocator.dupe(u8, id);
        if (self.pending_run_id) |old| self.allocator.free(old);
        self.pending_run_id = owned;
    }

    pub fn forgetRun(self: *Client) void {
        if (self.pending_run_id) |old| {
            self.allocator.free(old);
            self.pending_run_id = null;
        }
    }

    fn sendRequest(self: *Client, envelope: oap_types.Envelope) !void {
        self.send(envelope) catch |err| {
            _ = self.settleRequest(envelope.id);
            return err;
        };
    }
};

const Exchange = struct {
    allocator: std.mem.Allocator,
    pipe: *PipeTransport,
    server: @import("oap_server").Server,
    client: Client,

    fn init(allocator: std.mem.Allocator, options: @import("oap_server").Server.Options) !Exchange {
        const pipe = try allocator.create(PipeTransport);
        pipe.* = in_process.createSerializedPipe(allocator);
        errdefer {
            pipe.deinit();
            allocator.destroy(pipe);
        }
        return .{
            .allocator = allocator,
            .pipe = pipe,
            .server = try @import("oap_server").Server.init(allocator, options),
            .client = undefined,
        };
    }

    fn start(self: *Exchange) void {
        self.client = Client.init(self.allocator, self.pipe);
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

test "a client carries a real agent-control exchange over the in-process pipe" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" });
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    try std.testing.expectEqual(@as(usize, 1), try exchange.step());
    try std.testing.expect(exchange.client.capability_revision != null);
    try std.testing.expect(exchange.client.session_id == null);

    try exchange.client.openSession();
    try std.testing.expectEqual(@as(usize, 1), try exchange.step());
    try std.testing.expect(exchange.client.session_id != null);

    try exchange.client.submit("go on");
    try std.testing.expectEqual(@as(usize, 3), try exchange.step());
    try std.testing.expect(exchange.client.pending_run_id != null);
    try std.testing.expect(!std.mem.eql(u8, exchange.client.session_id.?, exchange.client.pending_run_id.?));
}

test "a submit before a session is opened is refused without a request on the wire" {
    const allocator = std.testing.allocator;
    var pipe = in_process.createSerializedPipe(allocator);
    defer pipe.deinit();
    var client = Client.init(allocator, &pipe);
    defer client.deinit();

    try std.testing.expectError(Error.NoSession, client.submit("go on"));
    try std.testing.expectError(Error.NoSession, client.switchModel("test/model"));
    try std.testing.expectError(Error.NoSession, client.cancel());
    try std.testing.expectError(Error.NotInitialized, client.openSession());

    var inbound = pipe.serverReceiver();
    try std.testing.expect((try inbound.readLine(allocator)) == null);
}

test "a response that answers no outstanding request is counted, not absorbed" {
    const allocator = std.testing.allocator;
    var pipe = in_process.createSerializedPipe(allocator);
    defer pipe.deinit();
    var client = Client.init(allocator, &pipe);
    defer client.deinit();

    try client.rememberRevision("rev-1");
    try client.absorb(.{ .id = "stray", .in_reply_to = "tui-req-999", .payload = .{ .capabilities_response = .{ .endpoint = .{ .id = "e" } } } });

    try std.testing.expectEqual(@as(u64, 1), client.unanswered);
    try std.testing.expectEqualStrings("rev-1", client.capability_revision.?);
}

test "the capability revision only moves on the responses that carry it" {
    const allocator = std.testing.allocator;
    var pipe = in_process.createSerializedPipe(allocator);
    defer pipe.deinit();
    var client = Client.init(allocator, &pipe);
    defer client.deinit();

    try client.rememberRevision("rev-2");
    try client.absorb(.{ .id = "evt", .capability_revision = "rev-older", .payload = .{ .content_delta = .{ .session_id = "s", .run_id = "r", .part = .{ .text = "late" } } } });
    try std.testing.expectEqualStrings("rev-2", client.capability_revision.?);

    try client.absorb(.{ .id = "upd", .capability_revision = "rev-3", .payload = .{ .capabilities_updated = .{ .previous_revision = "rev-2" } } });
    try std.testing.expectEqualStrings("rev-3", client.capability_revision.?);
}

fn drainOne(client: *Client, allocator: std.mem.Allocator) !void {
    const envelope = (try client.recv()) orelse return error.PipeAlreadyDrained;
    var owned = envelope;
    defer owned.deinit(allocator);
}

fn terminalEnvelope(run_id: []const u8) oap_types.Envelope {
    const final_message = oap_types.Message{ .role = .assistant, .content = .{ .parts = &.{} } };
    return .{
        .id = "term",
        .run_id = run_id,
        .payload = .{ .run_completed = .{
            .session_id = "s",
            .run_id = run_id,
            .final_response = final_message,
            .stop_reason = "stop",
        } },
    };
}

fn admittedExchange(allocator: std.mem.Allocator) !Exchange {
    var exchange = try Exchange.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" });
    errdefer exchange.deinit();
    exchange.start();
    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();
    try exchange.client.submit("go on");
    _ = try exchange.step();
    return exchange;
}

test "a terminal run event clears the run, so a later cancel says no run" {
    const allocator = std.testing.allocator;
    var exchange = try admittedExchange(allocator);
    defer exchange.deinit();

    const admitted = exchange.client.pending_run_id orelse return error.NoAdmittedRun;
    const owned_admitted = try allocator.dupe(u8, admitted);
    defer allocator.free(owned_admitted);
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstandingCount());

    try exchange.client.absorb(terminalEnvelope(owned_admitted));
    try std.testing.expect(exchange.client.pending_run_id == null);
    try std.testing.expectError(Error.NoActiveRun, exchange.client.cancel());
}

test "a terminal for a run that is not the pending one leaves that run cancelable" {
    const allocator = std.testing.allocator;
    var exchange = try admittedExchange(allocator);
    defer exchange.deinit();

    const first = try allocator.dupe(u8, exchange.client.pending_run_id.?);
    defer allocator.free(first);
    try exchange.client.rememberRunId("run-queued");

    try exchange.client.absorb(terminalEnvelope(first));
    try std.testing.expectEqualStrings("run-queued", exchange.client.pending_run_id.?);
    try exchange.client.cancel();

    var inbound = exchange.pipe.serverReceiver();
    const cancel_line = (try inbound.readLine(allocator)) orelse return error.NoCancelOnWire;
    defer allocator.free(cancel_line);
    try std.testing.expect(std.mem.indexOf(u8, cancel_line, "run.cancel") != null);
}

fn sendFailureProbe(allocator: std.mem.Allocator) !void {
    var pipe = in_process.createSerializedPipe(std.testing.allocator);
    defer pipe.deinit();
    var client = Client.init(allocator, &pipe);
    defer client.deinit();

    client.initialize() catch |err| switch (err) {
        error.OutOfMemory => {
            if (client.outstandingCount() != 0) return error.RequestLeftOutstanding;
            return err;
        },
        else => return err,
    };
    if (client.outstandingCount() != 1) return error.RequestNotRecorded;
}

test "a request that fails to send is not left outstanding" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, sendFailureProbe, .{});
}

test "a drained read compacts the pipe instead of retaining every line" {
    const allocator = std.testing.allocator;
    var exchange = try admittedExchange(allocator);
    defer exchange.deinit();
    const session_id = exchange.client.session_id.?;

    try exchange.server.noteContent(session_id, .{ .text = "one" });
    try exchange.server.noteContent(session_id, .{ .text = "two" });
    var sender = exchange.pipe.serverSender();
    while (exchange.server.popOutbound()) |line| {
        defer allocator.free(line);
        try sender.write(line);
    }
    try std.testing.expect(exchange.pipe.to_client.items.len > 0);

    try drainOne(&exchange.client, allocator);
    try std.testing.expect(exchange.pipe.to_client.items.len > 0);
    try drainOne(&exchange.client, allocator);
    try std.testing.expect(exchange.pipe.to_client.items.len > 0);

    try std.testing.expect((try exchange.client.recv()) == null);
    try std.testing.expectEqual(@as(usize, 0), exchange.pipe.to_client.items.len);
}

test "a closed session is forgotten, so a later submit refuses instead of writing" {
    const allocator = std.testing.allocator;
    var exchange = try admittedExchange(allocator);
    defer exchange.deinit();
    try std.testing.expect(exchange.client.session_id != null);

    try exchange.client.absorb(.{
        .id = "state",
        .payload = .{ .session_state_updated = .{ .session_id = "s", .status = .closed } },
    });
    try std.testing.expect(exchange.client.session_id == null);
    try std.testing.expect(exchange.client.pending_run_id == null);
    try std.testing.expectError(Error.NoSession, exchange.client.submit("go on"));
}

test "an error response is surfaced rather than dropped" {
    const allocator = std.testing.allocator;
    var pipe = in_process.createSerializedPipe(allocator);
    defer pipe.deinit();
    var client = Client.init(allocator, &pipe);
    defer client.deinit();

    try client.absorb(.{ .id = "err", .payload = .{ .error_response = .{ .code = "session_not_found", .message = "gone" } } });
    try std.testing.expectEqual(@as(u64, 1), client.error_responses);
    try std.testing.expectEqualStrings("session_not_found", client.last_error.?);

    try client.absorb(.{ .id = "err2", .payload = .{ .error_response = .{ .code = "run_busy", .message = "busy" } } });
    try std.testing.expectEqual(@as(u64, 2), client.error_responses);
    try std.testing.expectEqualStrings("run_busy", client.last_error.?);
}

test "outstanding requests are settled as their responses arrive" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" });
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    try std.testing.expectEqual(@as(usize, 1), exchange.client.outstandingCount());
    _ = try exchange.step();
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstandingCount());

    try exchange.client.openSession();
    try std.testing.expectEqual(@as(usize, 1), exchange.client.outstandingCount());
    _ = try exchange.step();
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstandingCount());
    try std.testing.expectEqual(@as(u64, 0), exchange.client.unanswered);
}

test "a cancel with a session but no run says the run is missing" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" });
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();
    try std.testing.expect(exchange.client.session_id != null);
    try std.testing.expectError(Error.NoActiveRun, exchange.client.cancel());
}

fn rememberProbe(allocator: std.mem.Allocator) !void {
    var pipe = in_process.createSerializedPipe(std.testing.allocator);
    defer pipe.deinit();
    var client = Client.init(allocator, &pipe);
    defer client.deinit();

    try client.rememberRevision("rev-1");
    try client.rememberRevision("rev-2");
    try client.rememberSessionId("sess-1");
    try client.rememberSessionId("sess-2");
    try client.rememberRunId("run-1");
    try client.rememberRunId("run-2");
    client.forgetRun();
    try client.rememberRunId("run-3");

    client.absorb(.{
        .id = "caps",
        .capability_revision = "rev-4",
        .payload = .{ .capabilities_response = .{ .endpoint = .{ .id = "e" } } },
    }) catch |err| switch (err) {
        error.OutOfMemory => {
            if (client.unanswered != 0) return error.UnansweredCountedSomethingElse;
            return err;
        },
    };
    if (client.unanswered != 0) return error.UnansweredCountedSomethingElse;
    if (client.capability_revision == null) return error.RevisionNotTaken;
}

test "the client's remembered fields survive an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, rememberProbe, .{});
}

test "a session is opened only after initialize" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator, .{});
    defer exchange.deinit();
    exchange.start();

    try std.testing.expectError(Error.NotInitialized, exchange.client.openSession());
    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();
    try std.testing.expect(exchange.client.session_id != null);
}

test "a cancel carries the run the submit admitted" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" });
    defer exchange.deinit();
    exchange.start();

    try std.testing.expectError(Error.NoSession, exchange.client.cancel());

    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();
    try exchange.client.submit("go on");
    _ = try exchange.step();

    const run_before = try allocator.dupe(u8, exchange.client.pending_run_id.?);
    defer allocator.free(run_before);
    try exchange.client.cancel();
    _ = try exchange.step();
    try std.testing.expectEqualStrings(run_before, exchange.client.pending_run_id.?);
}
