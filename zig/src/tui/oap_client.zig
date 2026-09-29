const std = @import("std");
const in_process = @import("transports/in_process");
const oap_types = @import("oap_types");
const oap_envelope = @import("oap_envelope");

const PipeTransport = in_process.SerializedPipe;

pub const Error = error{
    NoSession,
    NotInitialized,
    OutOfOrder,
};

pub const Client = struct {
    allocator: std.mem.Allocator,
    pipe: *PipeTransport,
    next_request_id: u64 = 0,
    session_id: ?[]u8 = null,
    capability_revision: ?[]u8 = null,
    pending_run_id: ?[]u8 = null,

    pub fn init(allocator: std.mem.Allocator, pipe: *PipeTransport) Client {
        return .{ .allocator = allocator, .pipe = pipe };
    }

    fn sender(self: *const Client) @import("transport").AsyncSender {
        return self.pipe.clientSender();
    }

    pub fn deinit(self: *Client) void {
        if (self.session_id) |id| self.allocator.free(id);
        if (self.capability_revision) |rev| self.allocator.free(rev);
        if (self.pending_run_id) |id| self.allocator.free(id);
        self.* = undefined;
    }

    fn requestId(self: *Client) ![]const u8 {
        self.next_request_id += 1;
        return std.fmt.allocPrint(self.allocator, "tui-req-{d}", .{self.next_request_id});
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
        defer self.allocator.free(id);
        try self.send(.{ .id = id, .payload = .{ .initialize_request = .{
            .protocol_versions = &versions,
            .profiles = &profiles,
        } } });
    }

    pub fn openSession(self: *Client) !void {
        if (self.capability_revision == null) return Error.NotInitialized;
        const id = try self.requestId();
        defer self.allocator.free(id);
        try self.send(.{
            .id = id,
            .capability_revision = self.capability_revision,
            .payload = .{ .session_open_request = .{} },
        });
    }

    pub fn submit(self: *Client, text: []const u8) !void {
        const session_id = self.session_id orelse return Error.NoSession;
        const id = try self.requestId();
        defer self.allocator.free(id);
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
        defer self.allocator.free(id);
        try self.send(.{
            .id = id,
            .session_id = session_id,
            .capability_revision = self.capability_revision,
            .payload = .{ .session_model_switch_request = .{ .session_id = session_id, .model_id = model_id } },
        });
    }

    pub fn cancel(self: *Client) !void {
        const session_id = self.session_id orelse return Error.NoSession;
        const run_id = self.pending_run_id orelse return Error.NoSession;
        const id = try self.requestId();
        defer self.allocator.free(id);
        try self.send(.{
            .id = id,
            .session_id = session_id,
            .run_id = run_id,
            .capability_revision = self.capability_revision,
            .payload = .{ .run_cancel_request = .{ .session_id = session_id, .run_id = run_id } },
        });
    }

    pub fn absorb(self: *Client, envelope: oap_types.Envelope) !void {
        if (envelope.capability_revision) |revision| try self.rememberRevision(revision);
        switch (envelope.payload) {
            .session_open_response => |payload| try self.rememberSessionId(payload.session_id),
            .message_submit_response => |payload| {
                if (payload.accepted) {
                    if (payload.run_id) |run_id| try self.rememberRunId(run_id);
                }
            },
            .run_status_updated, .content_delta => {
                if (envelope.run_id) |run_id| try self.rememberRunId(run_id);
            },
            else => {},
        }
    }

    pub fn takeSessionId(self: *Client) ?[]u8 {
        const owned = self.session_id orelse return null;
        self.session_id = null;
        return owned;
    }

    pub fn rememberSessionId(self: *Client, id: []const u8) !void {
        if (self.session_id) |old| self.allocator.free(old);
        self.session_id = try self.allocator.dupe(u8, id);
    }

    pub fn rememberRevision(self: *Client, revision: []const u8) !void {
        if (self.capability_revision) |old| self.allocator.free(old);
        self.capability_revision = try self.allocator.dupe(u8, revision);
    }

    pub fn rememberRunId(self: *Client, id: []const u8) !void {
        if (self.pending_run_id) |old| self.allocator.free(old);
        self.pending_run_id = try self.allocator.dupe(u8, id);
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
