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
        const line = (try receiver.readLine(self.allocator)) orelse return null;
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
        try self.send(.{ .id = id, .payload = .{ .initialize_request = .{
            .protocol_versions = &versions,
            .profiles = &profiles,
        } } });
    }

    pub fn openSession(self: *Client) !void {
        if (self.capability_revision == null) return Error.NotInitialized;
        const id = try self.requestId();
        try self.send(.{
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
        try self.send(.{
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
        try self.send(.{
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
        try self.send(.{
            .id = id,
            .session_id = session_id,
            .run_id = run_id,
            .capability_revision = self.capability_revision,
            .payload = .{ .run_cancel_request = .{ .session_id = session_id, .run_id = run_id } },
        });
    }

    pub fn absorb(self: *Client, envelope: oap_types.Envelope) void {
        if (envelope.in_reply_to) |reply_to| {
            if (!self.settleRequest(reply_to)) {
                self.unanswered += 1;
                return;
            }
        }

        switch (envelope.payload) {
            .initialize_response, .capabilities_response, .capabilities_updated => {
                if (envelope.capability_revision) |revision| {
                    self.rememberRevision(revision) catch { self.unanswered += 1; };
                }
            },
            else => {},
        }

        switch (envelope.payload) {
            .session_open_response => |payload| {
                self.rememberSessionId(payload.session_id) catch { self.unanswered += 1; };
            },
            .message_submit_response => |payload| {
                if (payload.accepted) {
                    if (payload.run_id) |run_id| {
                        self.rememberRunId(run_id) catch { self.unanswered += 1; };
                    }
                }
            },
            .run_status_updated, .content_delta => {
                if (envelope.run_id) |run_id| {
                    self.rememberRunId(run_id) catch { self.unanswered += 1; };
                }
            },
            .run_completed, .run_failed, .run_cancelled => self.forgetRun(),
            else => {},
        }
    }

    pub fn absorbProbe(self: *Client, envelope: oap_types.Envelope) void {
        self.absorb(envelope);
    }

    pub fn settleProbe(self: *Client, run_id: []const u8) !void {
        try self.rememberRunId(run_id);
        const final_message = oap_types.Message{ .role = .assistant, .content = .{ .parts = &.{} } };
        self.absorb(.{
            .id = "term",
            .run_id = run_id,
            .payload = .{ .run_completed = .{
                .session_id = "s",
                .run_id = run_id,
                .final_response = final_message,
                .stop_reason = "stop",
            } },
        });
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
};

const Exchange = struct {
    allocator: std.mem.Allocator,
    pipe: *PipeTransport,
    server: @import("oap_server").Server,
    client: Client,

    fn init(allocator: std.mem.Allocator, options: @import("oap_server").Server.Options) !Exchange {
        const pipe = try allocator.create(PipeTransport);
        errdefer allocator.destroy(pipe);
        pipe.* = in_process.createSerializedPipe(allocator);
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
            self.client.absorb(owned);
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
    client.absorbProbe(.{ .id = "stray", .in_reply_to = "tui-req-999", .payload = .{ .capabilities_response = .{ .endpoint = .{ .id = "e" } } } });

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
    client.absorbProbe(.{ .id = "evt", .capability_revision = "rev-older", .payload = .{ .content_delta = .{ .session_id = "s", .run_id = "r", .part = .{ .text = "late" } } } });
    try std.testing.expectEqualStrings("rev-2", client.capability_revision.?);

    client.absorbProbe(.{ .id = "upd", .capability_revision = "rev-3", .payload = .{ .capabilities_updated = .{ .previous_revision = "rev-2" } } });
    try std.testing.expectEqualStrings("rev-3", client.capability_revision.?);
}

test "a terminal run event clears the run, so a later cancel says no run" {
    const allocator = std.testing.allocator;
    var exchange = try Exchange.init(allocator, .{ .default_model_id = "anthropic/anthropic-messages@m" });
    defer exchange.deinit();
    exchange.start();

    try exchange.client.initialize();
    _ = try exchange.step();
    try exchange.client.openSession();
    _ = try exchange.step();
    try exchange.client.submit("go on");
    _ = try exchange.step();
    try std.testing.expect(exchange.client.pending_run_id != null);
    try std.testing.expectEqual(@as(usize, 0), exchange.client.outstandingCount());

    try exchange.client.settleProbe("run-1");
    try std.testing.expect(exchange.client.pending_run_id == null);
    try std.testing.expectError(Error.NoActiveRun, exchange.client.cancel());
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
