const std = @import("std");
const compat = @import("compat");
const auth_types = @import("auth_types");
const auth_server = @import("auth_server");
const auth_providers = @import("auth/providers");
const json_writer = @import("json_writer");
const oap_types = @import("oap_types");

const OwnedSlice = auth_types.OwnedSlice;

const Flow = struct {
    next_inbound: u64 = 2,
    next_event: u64 = 1,
    input_unavailable: bool = false,
};

const PendingQuery = struct {
    request_id: []u8,
    capability_revision: ?[]u8,

    fn deinit(self: *PendingQuery, allocator: std.mem.Allocator) void {
        allocator.free(self.request_id);
        if (self.capability_revision) |revision| allocator.free(revision);
    }
};

pub const Adapter = struct {
    allocator: std.mem.Allocator,
    server: *auth_server.AuthProtocolServer,
    outbox: std.ArrayList([]u8) = .empty,
    queries: std.AutoHashMap(auth_types.Ulid, PendingQuery),
    flows: std.AutoHashMap(auth_types.Ulid, Flow),
    expected_capability_revision: ?[]const u8 = null,
    disconnected: bool = false,
    signed_in: bool = false,

    const Self = @This();

    pub fn init(allocator: std.mem.Allocator, server: *auth_server.AuthProtocolServer) Self {
        return .{
            .allocator = allocator,
            .server = server,
            .queries = std.AutoHashMap(auth_types.Ulid, PendingQuery).init(allocator),
            .flows = std.AutoHashMap(auth_types.Ulid, Flow).init(allocator),
        };
    }

    pub fn deinit(self: *Self) void {
        for (self.outbox.items) |line| self.allocator.free(line);
        self.outbox.deinit(self.allocator);
        var queries = self.queries.valueIterator();
        while (queries.next()) |pending| pending.deinit(self.allocator);
        self.queries.deinit();
        self.flows.deinit();
        self.* = undefined;
    }

    pub fn takeSignIn(self: *Self) bool {
        defer self.signed_in = false;
        return self.signed_in;
    }

    pub fn setCapabilityRevision(self: *Self, revision: []const u8) void {
        self.expected_capability_revision = revision;
    }

    pub fn handleLine(self: *Self, line: []const u8) !bool {
        var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, line, .{}) catch |err| {
            if (err == error.OutOfMemory) return err;
            return false;
        };
        defer parsed.deinit();
        if (parsed.value != .object) return false;
        const root = parsed.value.object;
        const type_value = root.get("type") orelse return false;
        if (type_value != .string or !isAuthRequest(type_value.string)) return false;
        const profile = stringField(root, "profile") orelse return false;
        if (!std.mem.eql(u8, profile, oap_types.PROFILE)) return false;

        const request_id = stringField(root, "id") orelse return error.InvalidAuthEnvelope;
        if (request_id.len == 0) return error.InvalidAuthEnvelope;
        if (self.disconnected) {
            try self.emitError(request_id, "invalid_request", "authentication channel is closed");
            return true;
        }
        const protocol = stringField(root, "protocol") orelse return error.InvalidAuthEnvelope;
        const version = stringField(root, "version") orelse return error.InvalidAuthEnvelope;
        if (!std.mem.eql(u8, protocol, oap_types.PROTOCOL) or
            !std.mem.eql(u8, version, oap_types.VERSION))
        {
            try self.emitError(request_id, "invalid_request", "invalid OAP auth envelope");
            return true;
        }
        if (root.get("capability_revision")) |revision_value| {
            if (revision_value != .string) {
                try self.emitError(request_id, "invalid_request", "invalid capability revision");
                return true;
            }
            if (self.expected_capability_revision) |expected| {
                if (!std.mem.eql(u8, revision_value.string, expected)) {
                    try self.emit("error.response", request_id, null, null, .{
                        .@"error" = .{
                            .code = "stale_capabilities",
                            .message = "request pinned a capability revision this endpoint no longer serves",
                            .details = .{
                                .expected_revision = revision_value.string,
                                .current_revision = expected,
                            },
                        },
                    });
                    return true;
                }
            }
        }
        const response_revision = stringField(root, "capability_revision") orelse self.expected_capability_revision;
        const payload_value = root.get("payload") orelse {
            try self.emitError(request_id, "invalid_request", "missing auth payload");
            return true;
        };
        if (payload_value != .object) {
            try self.emitError(request_id, "invalid_request", "invalid auth payload");
            return true;
        }

        _ = try self.pump();
        self.dispatch(type_value.string, request_id, response_revision, payload_value.object) catch |err| {
            if (err == error.OutOfMemory) return err;
            const code = if (err == error.UnknownAuthFlow) "flow_not_found" else "invalid_request";
            try self.emitError(request_id, code, "authentication request was rejected");
        };
        return true;
    }

    pub fn pump(self: *Self) !usize {
        const before = self.outbox.items.len;
        while (self.server.popOutbound()) |native| {
            var owned = native;
            defer owned.deinit(self.server.allocator);
            try self.translateNative(owned);
        }
        return self.outbox.items.len - before;
    }

    pub fn cancelAllOnDisconnect(self: *Self) !void {
        _ = try self.pump();
        if (self.disconnected) return;

        var ids = std.ArrayList(auth_types.Ulid).empty;
        defer ids.deinit(self.allocator);
        var iterator = self.flows.keyIterator();
        while (iterator.next()) |id| try ids.append(self.allocator, id.*);

        for (ids.items) |flow_id| {
            const flow = self.flows.getPtr(flow_id) orelse continue;
            const native: auth_types.Envelope = .{
                .stream_id = flow_id,
                .message_id = auth_types.generateUlid(),
                .sequence = flow.next_inbound,
                .timestamp = compat.time.nowMillis(),
                .payload = .{ .auth_cancel = .{ .flow_id = flow_id } },
            };
            if (try self.server.handleEnvelope(native)) |ack| {
                var owned = ack;
                defer owned.deinit(self.server.allocator);
                if (owned.payload == .nack) return error.AuthDisconnectCancellationRejected;
            }
            flow.next_inbound += 1;
        }
        self.disconnected = true;
        _ = try self.pump();
    }

    pub fn popOutbound(self: *Self) ?[]u8 {
        if (self.outbox.items.len == 0) return null;
        return self.outbox.orderedRemove(0);
    }

    fn dispatch(self: *Self, type_name: []const u8, request_id: []const u8, response_revision: ?[]const u8, payload: std.json.ObjectMap) !void {
        if (std.mem.eql(u8, type_name, "auth.providers.request")) {
            if (payload.count() != 0) return error.InvalidAuthPayload;
            return self.providers(request_id, response_revision);
        }
        if (std.mem.eql(u8, type_name, "auth.login.start.request")) {
            if (payload.count() != 1) return error.InvalidAuthPayload;
            const provider_id = try requiredString(payload, "provider_id");
            if (provider_id.len == 0) return error.InvalidAuthPayload;
            return self.start(request_id, response_revision, provider_id);
        }
        if (std.mem.eql(u8, type_name, "auth.login.cancel.request")) {
            if (payload.count() != 1) return error.InvalidAuthPayload;
            const flow_id = try requiredString(payload, "flow_id");
            return self.cancel(request_id, response_revision, flow_id);
        }
        return error.InvalidAuthPayload;
    }

    fn providers(self: *Self, request_id: []const u8, response_revision: ?[]const u8) !void {
        const scope = auth_types.generateUlid();
        const message_id = auth_types.generateUlid();
        var pending: PendingQuery = .{
            .request_id = try self.allocator.dupe(u8, request_id),
            .capability_revision = null,
        };
        var transferred = false;
        errdefer if (!transferred) pending.deinit(self.allocator);
        if (response_revision) |revision| pending.capability_revision = try self.allocator.dupe(u8, revision);
        try self.queries.put(message_id, pending);
        transferred = true;
        errdefer if (self.queries.fetchRemove(message_id)) |removed| {
            var value = removed.value;
            value.deinit(self.allocator);
        };
        const native: auth_types.Envelope = .{
            .stream_id = scope,
            .message_id = message_id,
            .sequence = 1,
            .timestamp = compat.time.nowMillis(),
            .payload = .{ .auth_providers_request = .{} },
        };
        if (try self.server.handleEnvelope(native)) |ack| {
            var owned = ack;
            defer owned.deinit(self.server.allocator);
            if (owned.payload == .nack) {
                const removed = self.queries.fetchRemove(message_id).?;
                var value = removed.value;
                defer value.deinit(self.allocator);
                try self.emitNack(request_id, owned.payload.nack);
                return;
            }
        }
        _ = try self.pump();
    }

    fn start(self: *Self, request_id: []const u8, response_revision: ?[]const u8, provider_id: []const u8) !void {
        const flow_id = auth_types.generateUlid();
        const native: auth_types.Envelope = .{
            .stream_id = flow_id,
            .message_id = auth_types.generateUlid(),
            .sequence = 1,
            .timestamp = compat.time.nowMillis(),
            .payload = .{ .auth_login_start = .{ .provider_id = OwnedSlice(u8).initBorrowed(provider_id) } },
        };
        if (try self.server.handleEnvelope(native)) |ack| {
            var owned = ack;
            defer owned.deinit(self.server.allocator);
            if (owned.payload == .nack) return self.emitNack(request_id, owned.payload.nack);
        }
        try self.flows.put(flow_id, .{});
        const flow_text = try auth_types.ulidToString(flow_id, self.allocator);
        defer self.allocator.free(flow_text);
        try self.emit("auth.login.start.response", request_id, null, response_revision, .{ .flow_id = flow_text });
        _ = try self.pump();
    }

    fn cancel(self: *Self, request_id: []const u8, response_revision: ?[]const u8, flow_text: []const u8) !void {
        const flow_id = auth_types.parseUlid(flow_text) orelse return error.UnknownAuthFlow;
        const flow = self.flows.getPtr(flow_id) orelse {
            return self.emit("auth.login.cancel.response", request_id, null, response_revision, .{
                .flow_id = flow_text,
                .accepted = false,
            });
        };
        const native: auth_types.Envelope = .{
            .stream_id = flow_id,
            .message_id = auth_types.generateUlid(),
            .sequence = flow.next_inbound,
            .timestamp = compat.time.nowMillis(),
            .payload = .{ .auth_cancel = .{ .flow_id = flow_id } },
        };
        if (try self.server.handleEnvelope(native)) |ack| {
            var owned = ack;
            defer owned.deinit(self.server.allocator);
            if (owned.payload == .nack) return self.emitNack(request_id, owned.payload.nack);
        }
        flow.next_inbound += 1;
        try self.emit("auth.login.cancel.response", request_id, null, response_revision, .{
            .flow_id = flow_text,
            .accepted = true,
        });
        _ = try self.pump();
    }

    fn translateNative(self: *Self, native: auth_types.Envelope) !void {
        switch (native.payload) {
            .auth_providers_response => |response| {
                const reply_id = native.in_reply_to orelse return;
                const pending = self.queries.fetchRemove(reply_id) orelse return;
                var value = pending.value;
                defer value.deinit(self.allocator);
                try self.emitProviders(value.request_id, value.capability_revision, response);
            },
            .auth_event => |event| try self.translateEvent(event),
            .auth_login_result => |result| try self.translateResult(result),
            else => {},
        }
    }

    fn translateEvent(self: *Self, event: auth_types.AuthEvent) !void {
        const flow_id: auth_types.Ulid = switch (event) {
            .auth_url => |value| value.flow_id,
            .prompt => |value| value.flow_id,
            .progress => |value| value.flow_id,
            .success => return,
            .@"error" => return,
        };
        const flow = self.flows.getPtr(flow_id) orelse return;
        const flow_text = try auth_types.ulidToString(flow_id, self.allocator);
        defer self.allocator.free(flow_text);
        const sequence = flow.next_event;
        switch (event) {
            .auth_url => |value| try self.emit("auth.login.event", null, sequence, null, .{
                .flow_id = flow_text,
                .provider_id = value.provider_id.slice(),
                .kind = "url",
                .url = value.url.slice(),
                .instructions = value.instructions.slice(),
            }),
            .prompt => |value| {
                if (value.allow_empty) return self.answerWithDefault(flow, flow_id, value.prompt_id.slice());
                flow.input_unavailable = true;
                try self.emit("auth.login.event", null, sequence, null, .{
                    .flow_id = flow_text,
                    .provider_id = value.provider_id.slice(),
                    .kind = "progress",
                    .message = "manual login input is unavailable over OAP",
                });
                const cancel_request: auth_types.Envelope = .{
                    .stream_id = flow_id,
                    .message_id = auth_types.generateUlid(),
                    .sequence = flow.next_inbound,
                    .timestamp = compat.time.nowMillis(),
                    .payload = .{ .auth_cancel = .{ .flow_id = flow_id } },
                };
                if (try self.server.handleEnvelope(cancel_request)) |ack| {
                    var owned = ack;
                    defer owned.deinit(self.server.allocator);
                    if (owned.payload == .nack) return error.AuthInputCancellationRejected;
                }
                flow.next_inbound += 1;
            },
            .progress => |value| try self.emit("auth.login.event", null, sequence, null, .{
                .flow_id = flow_text,
                .provider_id = value.provider_id.slice(),
                .kind = "progress",
                .message = value.message.slice(),
            }),
            .success, .@"error" => unreachable,
        }
        flow.next_event += 1;
    }

    fn answerWithDefault(self: *Self, flow: *Flow, flow_id: auth_types.Ulid, prompt_id: []const u8) !void {
        const answer: auth_types.Envelope = .{
            .stream_id = flow_id,
            .message_id = auth_types.generateUlid(),
            .sequence = flow.next_inbound,
            .timestamp = compat.time.nowMillis(),
            .payload = .{ .auth_prompt_response = .{
                .flow_id = flow_id,
                .prompt_id = OwnedSlice(u8).initBorrowed(prompt_id),
                .answer = OwnedSlice(u8).initBorrowed(""),
            } },
        };
        if (try self.server.handleEnvelope(answer)) |ack| {
            var owned = ack;
            defer owned.deinit(self.server.allocator);
            if (owned.payload == .nack) return error.AuthDefaultAnswerRejected;
        }
        flow.next_inbound += 1;
    }

    fn translateResult(self: *Self, result: auth_types.AuthLoginResult) !void {
        const removed = self.flows.fetchRemove(result.flow_id) orelse return;
        const flow = removed.value;
        const flow_text = try auth_types.ulidToString(result.flow_id, self.allocator);
        defer self.allocator.free(flow_text);
        if (flow.input_unavailable) {
            try self.emit("auth.login.completed", null, flow.next_event, null, .{
                .flow_id = flow_text,
                .provider_id = result.provider_id.slice(),
                .status = "failed",
                .@"error" = .{ .code = "auth_input_unavailable", .message = "manual login input is unavailable over OAP" },
            });
        } else if (result.status == .failed) {
            try self.emit("auth.login.completed", null, flow.next_event, null, .{
                .flow_id = flow_text,
                .provider_id = result.provider_id.slice(),
                .status = @tagName(result.status),
                .@"error" = .{ .code = "auth_failed", .message = "provider login failed" },
            });
        } else {
            try self.emit("auth.login.completed", null, flow.next_event, null, .{
                .flow_id = flow_text,
                .provider_id = result.provider_id.slice(),
                .status = @tagName(result.status),
            });
            if (result.status == .success) self.signed_in = true;
        }
    }

    fn emitProviders(self: *Self, reply_id: []const u8, response_revision: ?[]const u8, response: auth_types.AuthProvidersResponse) !void {
        var payload = std.ArrayList(u8).empty;
        defer payload.deinit(self.allocator);
        var writer = json_writer.JsonWriter.init(&payload, self.allocator);
        try writer.beginObject();
        try writer.writeKey("providers");
        try writer.beginArray();
        for (response.providers.slice()) |provider| {
            try writer.beginObject();
            try writer.writeStringField("id", provider.id.slice());
            try writer.writeStringField("name", provider.name.slice());
            try writer.writeKey("auth_kinds");
            try writer.beginArray();
            for (provider.auth_kinds) |kind| {
                try writer.writeString(@tagName(kind));
            }
            try writer.endArray();
            try writer.writeStringField("auth_status", @tagName(provider.auth_status));
            if (provider.last_error.slice().len > 0) try writer.writeStringField("last_error", provider.last_error.slice());
            if (provider.override_host.slice().len > 0) try writer.writeStringField("override_host", provider.override_host.slice());
            try writer.endObject();
        }
        try writer.endArray();
        try writer.endObject();
        try self.emitRaw("auth.providers.response", reply_id, null, response_revision, payload.items);
    }

    fn emitNack(self: *Self, request_id: []const u8, nack: auth_types.Nack) !void {
        const code = if (nack.error_code) |value| @tagName(value) else "invalid_request";
        try self.emitError(request_id, code, "authentication request was rejected");
    }

    fn emitError(self: *Self, request_id: []const u8, code: []const u8, message: []const u8) !void {
        try self.emit("error.response", request_id, null, null, .{
            .@"error" = .{ .code = code, .message = message },
        });
    }

    fn emit(self: *Self, type_name: []const u8, in_reply_to: ?[]const u8, sequence: ?u64, capability_revision: ?[]const u8, payload: anytype) !void {
        const payload_json = try std.json.Stringify.valueAlloc(self.allocator, payload, .{});
        defer self.allocator.free(payload_json);
        try self.emitRaw(type_name, in_reply_to, sequence, capability_revision, payload_json);
    }

    fn emitRaw(self: *Self, type_name: []const u8, in_reply_to: ?[]const u8, sequence: ?u64, capability_revision: ?[]const u8, payload_json: []const u8) !void {
        var line = std.ArrayList(u8).empty;
        errdefer line.deinit(self.allocator);
        var writer = json_writer.JsonWriter.init(&line, self.allocator);
        const id = try auth_types.ulidToString(auth_types.generateUlid(), self.allocator);
        defer self.allocator.free(id);
        try writer.beginObject();
        try writer.writeStringField("protocol", oap_types.PROTOCOL);
        try writer.writeStringField("version", oap_types.VERSION);
        try writer.writeStringField("profile", oap_types.PROFILE);
        try writer.writeStringField("type", type_name);
        try writer.writeStringField("id", id);
        if (in_reply_to) |reply_id| try writer.writeStringField("in_reply_to", reply_id);
        if (sequence) |value| try writer.writeIntField("sequence", value);
        if (capability_revision) |revision| try writer.writeStringField("capability_revision", revision);
        try writer.writeIntField("timestamp_ms", compat.time.nowMillis());
        try writer.writeKey("payload");
        try writer.writeRawJson(payload_json);
        try writer.endObject();
        const owned_line = try line.toOwnedSlice(self.allocator);
        errdefer self.allocator.free(owned_line);
        try self.outbox.append(self.allocator, owned_line);
    }
};

fn isAuthRequest(type_name: []const u8) bool {
    return std.mem.eql(u8, type_name, "auth.providers.request") or
        std.mem.eql(u8, type_name, "auth.login.start.request") or
        std.mem.eql(u8, type_name, "auth.login.cancel.request");
}

fn stringField(obj: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    return if (value == .string) value.string else null;
}

fn requiredString(obj: std.json.ObjectMap, key: []const u8) ![]const u8 {
    return stringField(obj, key) orelse error.InvalidAuthPayload;
}

test "OAP auth adapter fails a manual prompt without carrying an answer" {
    const allocator = std.testing.allocator;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();
    adapter.setCapabilityRevision("current-revision");

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-1","capability_revision":"current-revision","payload":{"provider_id":"test-fixture"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));

    const response = adapter.popOutbound() orelse return error.MissingStartResponse;
    defer allocator.free(response);
    var parsed_start = try std.json.parseFromSlice(std.json.Value, allocator, response, .{});
    defer parsed_start.deinit();
    const start = parsed_start.value.object;
    try std.testing.expectEqualStrings("auth.login.start.response", try requiredString(start, "type"));
    try std.testing.expectEqualStrings("start-1", try requiredString(start, "in_reply_to"));
    try std.testing.expectEqualStrings("current-revision", try requiredString(start, "capability_revision"));
    var last_sequence: u64 = 0;
    const deadline = compat.time.nowMillis() + 5_000;
    var completed = false;
    var progress_seen = false;
    while (!completed and compat.time.nowMillis() < deadline) {
        _ = try adapter.pump();
        while (adapter.popOutbound()) |line| {
            defer allocator.free(line);
            try std.testing.expect(std.mem.indexOf(u8, line, "\"answer\"") == null);
            var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
            defer parsed.deinit();
            const root = parsed.value.object;
            const type_name = try requiredString(root, "type");
            if (!std.mem.eql(u8, type_name, "auth.login.event") and !std.mem.eql(u8, type_name, "auth.login.completed")) continue;
            const sequence = (root.get("sequence") orelse return error.MissingSequence).integer;
            try std.testing.expectEqual(last_sequence + 1, @as(u64, @intCast(sequence)));
            last_sequence += 1;
            const payload = (root.get("payload") orelse return error.MissingPayload).object;
            if (std.mem.eql(u8, type_name, "auth.login.event")) {
                const kind = try requiredString(payload, "kind");
                try std.testing.expect(!std.mem.eql(u8, kind, "prompt"));
                if (std.mem.eql(u8, kind, "progress")) progress_seen = true;
                continue;
            }
            try std.testing.expectEqualStrings("failed", try requiredString(payload, "status"));
            const failure = (payload.get("error") orelse return error.MissingAuthError).object;
            try std.testing.expectEqualStrings("auth_input_unavailable", try requiredString(failure, "code"));
            completed = true;
        }
        if (!completed) compat.time.sleepNs(std.time.ns_per_ms);
    }
    try std.testing.expect(completed);
    try std.testing.expect(progress_seen);
    const forbidden =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.reply.request","id":"reply-1","payload":{"flow_id":"f","prompt_id":"p","answer":"SENSITIVE_TEST_CODE"}}
    ;
    try std.testing.expect(!try adapter.handleLine(forbidden));
}

const FakeClaudeAuth = struct {
    fn fetch(allocator: std.mem.Allocator, url: []const u8, _: compat.http.FetchOptions) compat.http.FetchError!compat.http.Fetched {
        if (!std.mem.endsWith(u8, url, "/v1/oauth/token")) return error.RequestFailed;
        return .{ .status = 200, .body = try allocator.dupe(u8, "{\"access_token\":\"access-secret-2\",\"refresh_token\":\"refresh-secret-2\",\"expires_in\":3600}") };
    }

    fn param(url: []const u8, key: []const u8) []const u8 {
        const start = (std.mem.indexOf(u8, url, key) orelse return "") + key.len;
        const end = std.mem.indexOfScalarPos(u8, url, start, '&') orelse url.len;
        return url[start..end];
    }

    fn approve(url: []const u8, code: []const u8) ![]u8 {
        const redirect = param(url, "redirect_uri=http%3A%2F%2Flocalhost%3A");
        const port = try std.fmt.parseInt(u16, redirect[0 .. std.mem.indexOf(u8, redirect, "%2F") orelse return error.NoPort], 10);
        var stream = try compat.net.tcpConnectHost(std.testing.allocator, "127.0.0.1", port);
        defer stream.close();
        var request: [512]u8 = undefined;
        try stream.writeAll(try std.fmt.bufPrint(&request, "GET /callback?code={s}&state={s} HTTP/1.1\r\n\r\n", .{ code, param(url, "&state=") }));
        var response = std.ArrayList(u8).empty;
        errdefer response.deinit(std.testing.allocator);
        var chunk: [256]u8 = undefined;
        while (true) {
            if (!try compat.net.readableWithin(compat.net.streamHandle(&stream), 3_000)) return error.NoAnswer;
            const read = try stream.readSome(&chunk);
            if (read == 0) break;
            try response.appendSlice(std.testing.allocator, chunk[0..read]);
        }
        return response.toOwnedSlice(std.testing.allocator);
    }

    fn startLogin(adapter: *Adapter, id: []const u8) ![]u8 {
        const line = try std.fmt.allocPrint(std.testing.allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.start.request\",\"id\":\"{s}\",\"payload\":{{\"provider_id\":\"anthropic\"}}}}", .{id});
        defer std.testing.allocator.free(line);
        try std.testing.expect(try adapter.handleLine(line));
        const response = adapter.popOutbound() orelse return error.MissingStartResponse;
        defer std.testing.allocator.free(response);
        var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, response, .{});
        defer parsed.deinit();
        return std.testing.allocator.dupe(u8, try requiredString((parsed.value.object.get("payload") orelse return error.MissingPayload).object, "flow_id"));
    }

    fn awaitUrl(adapter: *Adapter) ![]u8 {
        const deadline = compat.time.nowMillis() + 5_000;
        while (compat.time.nowMillis() < deadline) {
            _ = try adapter.pump();
            while (adapter.popOutbound()) |line| {
                defer std.testing.allocator.free(line);
                var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, line, .{});
                defer parsed.deinit();
                const payload = (parsed.value.object.get("payload") orelse continue).object;
                const kind = stringField(payload, "kind") orelse continue;
                if (std.mem.eql(u8, kind, "url")) return std.testing.allocator.dupe(u8, try requiredString(payload, "url"));
            }
            compat.time.sleepNs(std.time.ns_per_ms);
        }
        return error.NoUrlEvent;
    }

    fn awaitStatus(adapter: *Adapter) ![]u8 {
        const deadline = compat.time.nowMillis() + 5_000;
        while (compat.time.nowMillis() < deadline) {
            _ = try adapter.pump();
            while (adapter.popOutbound()) |line| {
                defer std.testing.allocator.free(line);
                try std.testing.expect(std.mem.indexOf(u8, line, "secret") == null);
                var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, line, .{});
                defer parsed.deinit();
                if (!std.mem.eql(u8, try requiredString(parsed.value.object, "type"), "auth.login.completed")) continue;
                return std.testing.allocator.dupe(u8, try requiredString((parsed.value.object.get("payload") orelse return error.MissingPayload).object, "status"));
            }
            compat.time.sleepNs(std.time.ns_per_ms);
        }
        return error.LoginNeverCompleted;
    }

    fn server() auth_server.AuthProtocolServer {
        return auth_server.AuthProtocolServer.init(std.testing.allocator, .{
            .persist_credentials = false,
            .answers_prompts = false,
            .anthropic_fetch = fetch,
        });
    }
};

test "OAP auth adapter logs into Claude through a localhost callback the browser returns to, and sends no secret" {
    if (!compat.net.supports_unix_channels) return error.SkipZigTest;
    var native = FakeClaudeAuth.server();
    defer native.deinit();
    var adapter = Adapter.init(std.testing.allocator, &native);
    defer adapter.deinit();

    const flow_id = try FakeClaudeAuth.startLogin(&adapter, "start-claude");
    defer std.testing.allocator.free(flow_id);
    const url = try FakeClaudeAuth.awaitUrl(&adapter);
    defer std.testing.allocator.free(url);
    try std.testing.expect(std.mem.startsWith(u8, url, "https://claude.ai/oauth/authorize?"));
    const page = try FakeClaudeAuth.approve(url, "claude-code-2");
    defer std.testing.allocator.free(page);
    try std.testing.expect(std.mem.startsWith(u8, page, "HTTP/1.1 200 OK"));
    const status = try FakeClaudeAuth.awaitStatus(&adapter);
    defer std.testing.allocator.free(status);
    try std.testing.expectEqualStrings("success", status);
}

test "OAP auth adapter cancels a Claude login that is still waiting for the browser" {
    if (!compat.net.supports_unix_channels) return error.SkipZigTest;
    var native = FakeClaudeAuth.server();
    defer native.deinit();
    var adapter = Adapter.init(std.testing.allocator, &native);
    defer adapter.deinit();

    const flow_id = try FakeClaudeAuth.startLogin(&adapter, "start-claude-cancel");
    defer std.testing.allocator.free(flow_id);
    const url = try FakeClaudeAuth.awaitUrl(&adapter);
    defer std.testing.allocator.free(url);
    const cancel_line = try std.fmt.allocPrint(std.testing.allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.cancel.request\",\"id\":\"cancel-claude\",\"payload\":{{\"flow_id\":\"{s}\"}}}}", .{flow_id});
    defer std.testing.allocator.free(cancel_line);
    try std.testing.expect(try adapter.handleLine(cancel_line));
    const status = try FakeClaudeAuth.awaitStatus(&adapter);
    defer std.testing.allocator.free(status);
    try std.testing.expectEqualStrings("cancelled", status);
    if (FakeClaudeAuth.approve(url, "too-late")) |page| {
        defer std.testing.allocator.free(page);
        try std.testing.expect(!std.mem.startsWith(u8, page, "HTTP/1.1 200"));
    } else |_| {}
}

test "auth server shutdown stops a Claude login that is still waiting for the browser" {
    if (!compat.net.supports_unix_channels) return error.SkipZigTest;
    var native = FakeClaudeAuth.server();
    var shut_down = false;
    defer if (!shut_down) native.deinit();
    var adapter = Adapter.init(std.testing.allocator, &native);
    defer adapter.deinit();

    const flow_id = try FakeClaudeAuth.startLogin(&adapter, "start-claude-shutdown");
    defer std.testing.allocator.free(flow_id);
    const url = try FakeClaudeAuth.awaitUrl(&adapter);
    defer std.testing.allocator.free(url);
    const started = compat.time.nowMillis();
    shut_down = true;
    native.deinit();
    try std.testing.expect(compat.time.nowMillis() - started < 1_000);
}

const FakeCodexAuth = struct {
    var pending_polls: usize = 0;
    var polls: usize = 0;

    fn fetch(allocator: std.mem.Allocator, url: []const u8, _: compat.http.FetchOptions) compat.http.FetchError!compat.http.Fetched {
        if (std.mem.endsWith(u8, url, "/deviceauth/token")) {
            polls += 1;
            if (polls <= pending_polls) return .{ .status = 403, .body = try allocator.dupe(u8, "{}") };
        }
        const body: []const u8 = if (std.mem.endsWith(u8, url, "/deviceauth/usercode"))
            if (pending_polls > 0)
                "{\"device_auth_id\":\"device-secret-1\",\"user_code\":\"WXYZ-0000\",\"interval\":\"1\"}"
            else
                "{\"device_auth_id\":\"device-secret-1\",\"user_code\":\"WXYZ-0000\",\"interval\":\"0\"}"
        else if (std.mem.endsWith(u8, url, "/deviceauth/token"))
            "{\"authorization_code\":\"authorization-secret-1\",\"code_challenge\":\"c\",\"code_verifier\":\"verifier-secret-1\"}"
        else if (std.mem.endsWith(u8, url, "/oauth/token"))
            "{\"access_token\":\"access-secret-1\",\"refresh_token\":\"refresh-secret-1\",\"expires_in\":3600}"
        else
            return error.RequestFailed;
        return .{ .status = 200, .body = try allocator.dupe(u8, body) };
    }
};

test "OAP auth adapter logs into Codex with a device code it shows as a URL and user code, and sends no secret" {
    const allocator = std.testing.allocator;
    FakeCodexAuth.pending_polls = 0;
    FakeCodexAuth.polls = 0;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .answers_prompts = false,
        .codex_fetch = FakeCodexAuth.fetch,
        .codex_browser_port = null,
    });
    defer native.deinit();
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-codex","payload":{"provider_id":"openai-codex"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));
    const response = adapter.popOutbound() orelse return error.MissingStartResponse;
    allocator.free(response);

    var url_seen = false;
    var status: ?[]u8 = null;
    defer if (status) |value| allocator.free(value);
    const deadline = compat.time.nowMillis() + 5_000;
    while (status == null and compat.time.nowMillis() < deadline) {
        _ = try adapter.pump();
        while (adapter.popOutbound()) |line| {
            defer allocator.free(line);
            try std.testing.expect(std.mem.indexOf(u8, line, "secret") == null);
            var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
            defer parsed.deinit();
            const root = parsed.value.object;
            const type_name = try requiredString(root, "type");
            const payload = (root.get("payload") orelse return error.MissingPayload).object;
            if (std.mem.eql(u8, type_name, "auth.login.event") and std.mem.eql(u8, try requiredString(payload, "kind"), "url")) {
                try std.testing.expectEqualStrings("https://auth.openai.com/codex/device", try requiredString(payload, "url"));
                try std.testing.expectEqualStrings("Enter code: WXYZ-0000", try requiredString(payload, "instructions"));
                url_seen = true;
            }
            if (std.mem.eql(u8, type_name, "auth.login.completed")) status = try allocator.dupe(u8, try requiredString(payload, "status"));
        }
        if (status == null) compat.time.sleepNs(std.time.ns_per_ms);
    }
    try std.testing.expect(url_seen);
    try std.testing.expectEqualStrings("success", status orelse return error.LoginNeverCompleted);
    try std.testing.expect(adapter.takeSignIn());
    try std.testing.expect(!adapter.takeSignIn());
}

test "OAP auth adapter cancels a Codex device login that is still waiting for approval" {
    const allocator = std.testing.allocator;
    FakeCodexAuth.pending_polls = 3;
    FakeCodexAuth.polls = 0;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .answers_prompts = false,
        .codex_fetch = FakeCodexAuth.fetch,
        .codex_browser_port = null,
    });
    defer native.deinit();
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-codex-cancel","payload":{"provider_id":"openai-codex"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));
    const response = adapter.popOutbound() orelse return error.MissingStartResponse;
    defer allocator.free(response);
    var parsed_start = try std.json.parseFromSlice(std.json.Value, allocator, response, .{});
    defer parsed_start.deinit();
    const flow_id = try requiredString((parsed_start.value.object.get("payload") orelse return error.MissingPayload).object, "flow_id");

    var cancel_sent = false;
    var status: ?[]u8 = null;
    defer if (status) |value| allocator.free(value);
    const deadline = compat.time.nowMillis() + 5_000;
    while (status == null and compat.time.nowMillis() < deadline) {
        _ = try adapter.pump();
        var url_arrived = false;
        while (adapter.popOutbound()) |line| {
            defer allocator.free(line);
            var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
            defer parsed.deinit();
            const root = parsed.value.object;
            const type_name = try requiredString(root, "type");
            const payload = (root.get("payload") orelse continue).object;
            if (std.mem.eql(u8, type_name, "auth.login.event") and std.mem.eql(u8, try requiredString(payload, "kind"), "url")) url_arrived = true;
            if (std.mem.eql(u8, type_name, "auth.login.completed")) status = try allocator.dupe(u8, try requiredString(payload, "status"));
        }
        if (url_arrived and !cancel_sent) {
            const cancel_line = try std.fmt.allocPrint(
                allocator,
                "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.cancel.request\",\"id\":\"cancel-codex\",\"payload\":{{\"flow_id\":\"{s}\"}}}}",
                .{flow_id},
            );
            defer allocator.free(cancel_line);
            try std.testing.expect(try adapter.handleLine(cancel_line));
            cancel_sent = true;
        }
        if (status == null) compat.time.sleepNs(std.time.ns_per_ms);
    }
    try std.testing.expect(cancel_sent);
    try std.testing.expectEqualStrings("cancelled", status orelse return error.LoginNeverCompleted);
    try std.testing.expect(!adapter.takeSignIn());
    try std.testing.expect(FakeCodexAuth.polls <= FakeCodexAuth.pending_polls);
}

test "auth server shutdown stops a Codex device login that is still waiting for approval" {
    const allocator = std.testing.allocator;
    FakeCodexAuth.pending_polls = 3;
    FakeCodexAuth.polls = 0;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .answers_prompts = false,
        .codex_fetch = FakeCodexAuth.fetch,
        .codex_browser_port = null,
    });
    var shut_down = false;
    defer if (!shut_down) native.deinit();
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-codex-shutdown","payload":{"provider_id":"openai-codex"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));
    var url_arrived = false;
    const deadline = compat.time.nowMillis() + 5_000;
    while (!url_arrived and compat.time.nowMillis() < deadline) {
        _ = try adapter.pump();
        while (adapter.popOutbound()) |line| {
            defer allocator.free(line);
            if (std.mem.indexOf(u8, line, "\"kind\":\"url\"") != null) url_arrived = true;
        }
        if (!url_arrived) compat.time.sleepNs(std.time.ns_per_ms);
    }
    try std.testing.expect(url_arrived);

    const started = compat.time.nowMillis();
    shut_down = true;
    native.deinit();
    try std.testing.expect(compat.time.nowMillis() - started < 1_000);
    try std.testing.expect(FakeCodexAuth.polls <= FakeCodexAuth.pending_polls);
}

test "OAP auth adapter takes the default for a question that allows an empty answer and goes on to the login URL" {
    const allocator = std.testing.allocator;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
        .fixture_asks_default = true,
    });
    defer native.deinit();
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-default","payload":{"provider_id":"test-fixture"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));
    const response = adapter.popOutbound() orelse return error.MissingStartResponse;
    allocator.free(response);

    var last_sequence: u64 = 0;
    var url_seen = false;
    var failure_code: ?[]u8 = null;
    defer if (failure_code) |code| allocator.free(code);
    const deadline = compat.time.nowMillis() + 5_000;
    while (failure_code == null and compat.time.nowMillis() < deadline) {
        _ = try adapter.pump();
        while (adapter.popOutbound()) |line| {
            defer allocator.free(line);
            try std.testing.expect(std.mem.indexOf(u8, line, "Fixture domain") == null);
            var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
            defer parsed.deinit();
            const root = parsed.value.object;
            const type_name = try requiredString(root, "type");
            if (!std.mem.eql(u8, type_name, "auth.login.event") and !std.mem.eql(u8, type_name, "auth.login.completed")) continue;
            const sequence = (root.get("sequence") orelse return error.MissingSequence).integer;
            try std.testing.expectEqual(last_sequence + 1, @as(u64, @intCast(sequence)));
            last_sequence += 1;
            const payload = (root.get("payload") orelse return error.MissingPayload).object;
            if (std.mem.eql(u8, type_name, "auth.login.event")) {
                if (std.mem.eql(u8, try requiredString(payload, "kind"), "url")) url_seen = true;
                continue;
            }
            const failure = (payload.get("error") orelse return error.MissingAuthError).object;
            failure_code = try allocator.dupe(u8, try requiredString(failure, "code"));
        }
        if (failure_code == null) compat.time.sleepNs(std.time.ns_per_ms);
    }
    try std.testing.expect(url_seen);
    try std.testing.expectEqualStrings("auth_input_unavailable", failure_code orelse return error.LoginNeverCompleted);
}

const FakeCopilotAuth = struct {
    var polls: std.atomic.Value(usize) = .init(0);
    var granted: bool = false;
    var copilot_token_calls: std.atomic.Value(usize) = .init(0);
    var model_policy_calls: std.atomic.Value(usize) = .init(0);

    fn fetch(allocator: std.mem.Allocator, url: []const u8, _: compat.http.FetchOptions) compat.http.FetchError!compat.http.Fetched {
        const body: []const u8 = if (std.mem.endsWith(u8, url, "/login/device/code"))
            "{\"device_code\":\"device-secret-3\",\"user_code\":\"GH-0001\",\"verification_uri\":\"https://github.com/login/device\",\"expires_in\":5,\"interval\":1}"
        else if (std.mem.endsWith(u8, url, "/login/oauth/access_token")) blk: {
            _ = polls.fetchAdd(1, .seq_cst);
            break :blk if (granted) "{\"access_token\":\"github-secret-3\"}" else "{\"error\":\"authorization_pending\"}";
        } else if (std.mem.endsWith(u8, url, "/copilot_internal/v2/token")) blk: {
            _ = copilot_token_calls.fetchAdd(1, .seq_cst);
            break :blk "{\"token\":\"tid=copilot-secret-3;proxy-ep=proxy.individual.githubcopilot.com\"}";
        } else if (std.mem.endsWith(u8, url, "/policy")) blk: {
            _ = model_policy_calls.fetchAdd(1, .seq_cst);
            break :blk "{}";
        } else return error.RequestFailed;
        return .{ .status = 200, .body = try allocator.dupe(u8, body) };
    }

    fn server() auth_server.AuthProtocolServer {
        polls.store(0, .seq_cst);
        granted = false;
        copilot_token_calls.store(0, .seq_cst);
        model_policy_calls.store(0, .seq_cst);
        return auth_server.AuthProtocolServer.init(std.testing.allocator, .{
            .persist_credentials = false,
            .copilot_fetch = fetch,
        });
    }

    fn startAndAwaitUrl(adapter: *Adapter, id: []const u8) ![]u8 {
        const line = try std.fmt.allocPrint(std.testing.allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.start.request\",\"id\":\"{s}\",\"payload\":{{\"provider_id\":\"github-copilot\"}}}}", .{id});
        defer std.testing.allocator.free(line);
        try std.testing.expect(try adapter.handleLine(line));
        const response = adapter.popOutbound() orelse return error.MissingStartResponse;
        defer std.testing.allocator.free(response);
        var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, response, .{});
        defer parsed.deinit();
        const flow_id = try std.testing.allocator.dupe(u8, try requiredString((parsed.value.object.get("payload") orelse return error.MissingPayload).object, "flow_id"));
        errdefer std.testing.allocator.free(flow_id);
        const deadline = compat.time.nowMillis() + 5_000;
        var url_seen = false;
        while (compat.time.nowMillis() < deadline) {
            _ = try adapter.pump();
            while (adapter.popOutbound()) |event| {
                defer std.testing.allocator.free(event);
                if (std.mem.indexOf(u8, event, "\"kind\":\"url\"") != null) url_seen = true;
            }
            if (url_seen and polls.load(.seq_cst) > 0) return flow_id;
            compat.time.sleepNs(std.time.ns_per_ms);
        }
        return error.NoUrlEvent;
    }
};

test "OAP auth adapter cancels a Copilot device login that is still waiting for approval, and it stops polling" {
    var native = FakeCopilotAuth.server();
    defer native.deinit();
    var adapter = Adapter.init(std.testing.allocator, &native);
    defer adapter.deinit();

    const flow_id = try FakeCopilotAuth.startAndAwaitUrl(&adapter, "start-copilot-cancel");
    defer std.testing.allocator.free(flow_id);
    const cancel_line = try std.fmt.allocPrint(std.testing.allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.cancel.request\",\"id\":\"cancel-copilot\",\"payload\":{{\"flow_id\":\"{s}\"}}}}", .{flow_id});
    defer std.testing.allocator.free(cancel_line);
    try std.testing.expect(try adapter.handleLine(cancel_line));
    compat.time.sleepMs(300);
    const settled = FakeCopilotAuth.polls.load(.seq_cst);
    compat.time.sleepMs(1_500);
    try std.testing.expectEqual(settled, FakeCopilotAuth.polls.load(.seq_cst));
    try std.testing.expectEqual(@as(usize, 0), native.activeFlowCount());
}

test "OAP auth adapter completes an approved Copilot device login through the injected fetch alone, and sends no secret" {
    var native = FakeCopilotAuth.server();
    defer native.deinit();
    FakeCopilotAuth.granted = true;
    var adapter = Adapter.init(std.testing.allocator, &native);
    defer adapter.deinit();

    const line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-copilot-granted","payload":{"provider_id":"github-copilot"}}
    ;
    try std.testing.expect(try adapter.handleLine(line));
    var status: ?[]u8 = null;
    defer if (status) |value| std.testing.allocator.free(value);
    const deadline = compat.time.nowMillis() + 5_000;
    while (status == null and compat.time.nowMillis() < deadline) {
        _ = try adapter.pump();
        while (adapter.popOutbound()) |event| {
            defer std.testing.allocator.free(event);
            try std.testing.expect(std.mem.indexOf(u8, event, "secret") == null);
            var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, event, .{});
            defer parsed.deinit();
            if (!std.mem.eql(u8, try requiredString(parsed.value.object, "type"), "auth.login.completed")) continue;
            status = try std.testing.allocator.dupe(u8, try requiredString((parsed.value.object.get("payload") orelse return error.MissingPayload).object, "status"));
        }
        if (status == null) compat.time.sleepNs(std.time.ns_per_ms);
    }
    try std.testing.expectEqualStrings("success", status orelse return error.LoginNeverCompleted);
    try std.testing.expect(adapter.takeSignIn());
    try std.testing.expect(!adapter.takeSignIn());
    try std.testing.expectEqual(@as(usize, 1), FakeCopilotAuth.copilot_token_calls.load(.seq_cst));
    try std.testing.expect(FakeCopilotAuth.model_policy_calls.load(.seq_cst) > 0);
}

test "auth server shutdown stops a Copilot device login that is still waiting for approval" {
    var native = FakeCopilotAuth.server();
    var shut_down = false;
    defer if (!shut_down) native.deinit();
    var adapter = Adapter.init(std.testing.allocator, &native);
    defer adapter.deinit();

    const flow_id = try FakeCopilotAuth.startAndAwaitUrl(&adapter, "start-copilot-shutdown");
    defer std.testing.allocator.free(flow_id);
    const started = compat.time.nowMillis();
    shut_down = true;
    native.deinit();
    try std.testing.expect(compat.time.nowMillis() - started < 1_000);
}

test "auth adapter repeats the admitted revision on providers and cancel responses" {
    const allocator = std.testing.allocator;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();
    adapter.setCapabilityRevision("current-revision");

    const providers_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.providers.request","id":"providers-1","capability_revision":"current-revision","payload":{}}
    ;
    try std.testing.expect(try adapter.handleLine(providers_line));
    const providers_response = adapter.popOutbound() orelse return error.MissingProvidersResponse;
    defer allocator.free(providers_response);
    var parsed_providers = try std.json.parseFromSlice(std.json.Value, allocator, providers_response, .{});
    defer parsed_providers.deinit();
    const providers = parsed_providers.value.object;
    try std.testing.expectEqualStrings("auth.providers.response", try requiredString(providers, "type"));
    try std.testing.expectEqualStrings("providers-1", try requiredString(providers, "in_reply_to"));
    try std.testing.expectEqualStrings("current-revision", try requiredString(providers, "capability_revision"));

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-2","capability_revision":"current-revision","payload":{"provider_id":"test-fixture"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));
    const start_response = adapter.popOutbound() orelse return error.MissingStartResponse;
    defer allocator.free(start_response);
    var parsed_start = try std.json.parseFromSlice(std.json.Value, allocator, start_response, .{});
    defer parsed_start.deinit();
    const start = parsed_start.value.object;
    try std.testing.expectEqualStrings("auth.login.start.response", try requiredString(start, "type"));
    try std.testing.expectEqualStrings("current-revision", try requiredString(start, "capability_revision"));
    const flow_id = try requiredString((start.get("payload") orelse return error.MissingPayload).object, "flow_id");
    const cancel_line = try std.fmt.allocPrint(
        allocator,
        "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.cancel.request\",\"id\":\"cancel-1\",\"capability_revision\":\"current-revision\",\"payload\":{{\"flow_id\":\"{s}\"}}}}",
        .{flow_id},
    );
    defer allocator.free(cancel_line);
    try std.testing.expect(try adapter.handleLine(cancel_line));
    var found_cancel = false;
    while (adapter.popOutbound()) |line| {
        defer allocator.free(line);
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();
        const root = parsed.value.object;
        if (!std.mem.eql(u8, try requiredString(root, "type"), "auth.login.cancel.response")) continue;
        try std.testing.expectEqualStrings("cancel-1", try requiredString(root, "in_reply_to"));
        try std.testing.expectEqualStrings("current-revision", try requiredString(root, "capability_revision"));
        found_cancel = true;
    }
    try std.testing.expect(found_cancel);
}

test "auth adapter rejects undeclared secret fields without reflecting them" {
    const allocator = std.testing.allocator;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();

    const line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-secret","payload":{"provider_id":"test-fixture","api_key":"must-not-echo"}}
    ;
    try std.testing.expect(try adapter.handleLine(line));
    const response = adapter.popOutbound() orelse return error.MissingErrorResponse;
    defer allocator.free(response);
    try std.testing.expect(std.mem.indexOf(u8, response, "must-not-echo") == null);
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, response, .{});
    defer parsed.deinit();
    try std.testing.expectEqualStrings("error.response", try requiredString(parsed.value.object, "type"));
    const payload = (parsed.value.object.get("payload") orelse return error.MissingPayload).object;
    const nested_error = (payload.get("error") orelse return error.MissingError).object;
    try std.testing.expectEqualStrings("invalid_request", try requiredString(nested_error, "code"));
    try std.testing.expectEqual(@as(usize, 0), native.activeFlowCount());
}

test "auth adapter cancels local login on disconnect and ignores other profiles" {
    const allocator = std.testing.allocator;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();

    const wrong_profile =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"auth.login.start.request","id":"foreign","payload":{"provider_id":"test-fixture"}}
    ;
    try std.testing.expect(!(try adapter.handleLine(wrong_profile)));
    try std.testing.expect(adapter.popOutbound() == null);

    const start_line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.login.start.request","id":"start-disconnect","payload":{"provider_id":"test-fixture"}}
    ;
    try std.testing.expect(try adapter.handleLine(start_line));
    try adapter.cancelAllOnDisconnect();
    try adapter.cancelAllOnDisconnect();

    var terminals: usize = 0;
    while (adapter.popOutbound()) |line| {
        defer allocator.free(line);
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
        defer parsed.deinit();
        const root = parsed.value.object;
        if (!std.mem.eql(u8, try requiredString(root, "type"), "auth.login.completed")) continue;
        const payload = (root.get("payload") orelse return error.MissingPayload).object;
        const status = try requiredString(payload, "status");
        if (std.mem.eql(u8, status, "failed")) {
            const failure = (payload.get("error") orelse return error.MissingAuthError).object;
            try std.testing.expectEqualStrings("auth_input_unavailable", try requiredString(failure, "code"));
        } else {
            try std.testing.expectEqualStrings("cancelled", status);
        }
        terminals += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), terminals);
    try std.testing.expectEqual(@as(usize, 0), native.activeFlowCount());
}

test "auth adapter rejects stale revisions and requests after disconnect" {
    const allocator = std.testing.allocator;
    var native = auth_server.AuthProtocolServer.init(allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();
    adapter.setCapabilityRevision("current-revision");

    const stale =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.providers.request","id":"stale-request","capability_revision":"old-revision","payload":{}}
    ;
    try std.testing.expect(try adapter.handleLine(stale));
    const stale_response = adapter.popOutbound() orelse return error.MissingErrorResponse;
    defer allocator.free(stale_response);
    var parsed_stale = try std.json.parseFromSlice(std.json.Value, allocator, stale_response, .{});
    defer parsed_stale.deinit();
    const stale_error = ((parsed_stale.value.object.get("payload") orelse return error.MissingPayload).object.get("error") orelse return error.MissingError).object;
    try std.testing.expectEqualStrings("stale_capabilities", try requiredString(stale_error, "code"));
    const stale_details = (stale_error.get("details") orelse return error.MissingErrorDetails).object;
    try std.testing.expectEqualStrings("old-revision", try requiredString(stale_details, "expected_revision"));
    try std.testing.expectEqualStrings("current-revision", try requiredString(stale_details, "current_revision"));

    try adapter.cancelAllOnDisconnect();
    const closed =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.providers.request","id":"closed-request","capability_revision":"current-revision","payload":{}}
    ;
    try std.testing.expect(try adapter.handleLine(closed));
    const closed_response = adapter.popOutbound() orelse return error.MissingErrorResponse;
    defer allocator.free(closed_response);
    var parsed_closed = try std.json.parseFromSlice(std.json.Value, allocator, closed_response, .{});
    defer parsed_closed.deinit();
    const closed_error = ((parsed_closed.value.object.get("payload") orelse return error.MissingPayload).object.get("error") orelse return error.MissingError).object;
    try std.testing.expectEqualStrings("invalid_request", try requiredString(closed_error, "code"));
    try std.testing.expectEqual(@as(usize, 0), native.activeFlowCount());
}

fn authProvidersAllocationProbe(allocator: std.mem.Allocator) !void {
    var native = auth_server.AuthProtocolServer.init(std.testing.allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();
    adapter.setCapabilityRevision("current-revision");

    const line =
        \\{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"auth.providers.request","id":"providers-oom","capability_revision":"current-revision","payload":{}}
    ;
    try std.testing.expect(try adapter.handleLine(line));
    while (adapter.popOutbound()) |outbound| allocator.free(outbound);
}

const AuthProbeOutcome = struct {
    flow_id: ?[]u8 = null,
    accepted: ?bool = null,
    cancelled: usize = 0,
    input_unavailable: usize = 0,

    fn deinit(self: *AuthProbeOutcome) void {
        if (self.flow_id) |flow_id| std.testing.allocator.free(flow_id);
        self.* = undefined;
    }
};

fn submitAuthProbeLogin(adapter: *Adapter, request_id: []const u8) !void {
    const line = try std.fmt.allocPrint(
        std.testing.allocator,
        "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.start.request\",\"id\":\"{s}\",\"capability_revision\":\"current-revision\",\"payload\":{{\"provider_id\":\"test-fixture\"}}}}",
        .{request_id},
    );
    defer std.testing.allocator.free(line);
    try std.testing.expect(try adapter.handleLine(line));
}

fn submitAuthProbeCancel(adapter: *Adapter, request_id: []const u8, flow_id: []const u8) !void {
    const line = try std.fmt.allocPrint(
        std.testing.allocator,
        "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"auth.login.cancel.request\",\"id\":\"{s}\",\"capability_revision\":\"current-revision\",\"payload\":{{\"flow_id\":\"{s}\"}}}}",
        .{ request_id, flow_id },
    );
    defer std.testing.allocator.free(line);
    try std.testing.expect(try adapter.handleLine(line));
}

fn drainAuthProbe(adapter: *Adapter, allocator: std.mem.Allocator) !AuthProbeOutcome {
    var outcome: AuthProbeOutcome = .{};
    errdefer outcome.deinit();
    while (adapter.popOutbound()) |line| {
        defer allocator.free(line);
        var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, line, .{});
        defer parsed.deinit();
        const root = parsed.value.object;
        const type_name = try requiredString(root, "type");
        const payload = (root.get("payload") orelse return error.MissingPayload).object;
        if (std.mem.eql(u8, type_name, "auth.login.start.response")) {
            if (outcome.flow_id != null) return error.UnexpectedStartResponse;
            outcome.flow_id = try std.testing.allocator.dupe(u8, try requiredString(payload, "flow_id"));
        } else if (std.mem.eql(u8, type_name, "auth.login.cancel.response")) {
            outcome.accepted = (payload.get("accepted") orelse return error.MissingAccepted).bool;
        } else if (std.mem.eql(u8, type_name, "auth.login.completed")) {
            const status = try requiredString(payload, "status");
            if (std.mem.eql(u8, status, "cancelled")) {
                outcome.cancelled += 1;
                continue;
            }
            try std.testing.expectEqualStrings("failed", status);
            const failure = (payload.get("error") orelse return error.MissingAuthError).object;
            try std.testing.expectEqualStrings("auth_input_unavailable", try requiredString(failure, "code"));
            outcome.input_unavailable += 1;
        }
    }
    return outcome;
}

fn authFlowAllocationProbe(allocator: std.mem.Allocator) !void {
    var native = auth_server.AuthProtocolServer.init(std.testing.allocator, .{
        .persist_credentials = false,
        .enable_real_oauth = false,
    });
    defer native.deinit();
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    var adapter = Adapter.init(allocator, &native);
    defer adapter.deinit();
    adapter.setCapabilityRevision("current-revision");

    try submitAuthProbeLogin(&adapter, "start-prompted");
    const deadline = compat.time.nowMillis() + 5_000;
    while (adapter.flows.count() != 0) {
        if (compat.time.nowMillis() >= deadline) return error.PromptedLoginDidNotSettle;
        if ((try adapter.pump()) == 0) compat.time.sleepNs(std.time.ns_per_ms);
    }
    var prompted = try drainAuthProbe(&adapter, allocator);
    defer prompted.deinit();
    try std.testing.expectEqual(@as(usize, 1), prompted.input_unavailable);
    try submitAuthProbeCancel(&adapter, "cancel-settled", prompted.flow_id orelse return error.MissingStartResponse);
    var settled = try drainAuthProbe(&adapter, allocator);
    defer settled.deinit();
    try std.testing.expectEqual(@as(?bool, false), settled.accepted);

    native.options.spawn_login_workers = false;
    try submitAuthProbeLogin(&adapter, "start-idle");
    var idle = try drainAuthProbe(&adapter, allocator);
    defer idle.deinit();
    try submitAuthProbeCancel(&adapter, "cancel-idle", idle.flow_id orelse return error.MissingStartResponse);
    var cancelled = try drainAuthProbe(&adapter, allocator);
    defer cancelled.deinit();
    try std.testing.expectEqual(@as(?bool, true), cancelled.accepted);
    try std.testing.expectEqual(@as(usize, 1), cancelled.cancelled);

    try submitAuthProbeLogin(&adapter, "start-live");
    var live = try drainAuthProbe(&adapter, allocator);
    defer live.deinit();
    try std.testing.expectEqual(@as(usize, 1), adapter.flows.count());
    try adapter.cancelAllOnDisconnect();
    var disconnected = try drainAuthProbe(&adapter, allocator);
    defer disconnected.deinit();
    try std.testing.expectEqual(@as(usize, 1), disconnected.cancelled);
}

test "auth providers translation survives allocation failures" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, authProvidersAllocationProbe, .{});
}

test "auth login, cancellation, and disconnect survive allocation failures" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, authFlowAllocationProbe, .{});
}
