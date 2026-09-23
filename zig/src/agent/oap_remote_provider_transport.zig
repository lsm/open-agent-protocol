const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const bridge = @import("agent_oap_provider_bridge");
const http_client = @import("oap_provider_http_client");
const http_policy = @import("oap_provider_http_policy");
const provider_envelope = @import("oap_provider_envelope");
const provider_types = @import("oap_provider_types");

pub const Config = struct {
    base_url: []const u8,
    security: http_policy.Security,

    pub fn factory(self: *const Config) bridge.TransportFactory {
        return .{ .ctx = @ptrCast(@constCast(self)), .open_fn = open };
    }
};

pub fn discoverModels(allocator: std.mem.Allocator, config: *const Config) ![][]u8 {
    var client = try http_client.Client.init(allocator, config.base_url, config.security);
    defer client.deinit();
    const describe_request = try provider_envelope.serializeEnvelope(.{
        .id = "remote-describe",
        .payload = .{ .provider_describe_request = .{} },
    }, allocator);
    defer allocator.free(describe_request);
    const describe_line = try client.postUnary(describe_request);
    defer allocator.free(describe_line);
    var describe = try provider_envelope.deserializeEnvelope(describe_line, allocator);
    defer describe.deinit(allocator);
    if (!std.mem.eql(u8, describe.in_reply_to orelse "", "remote-describe")) return error.InvalidProviderServiceResponse;
    if (describe.payload != .provider_describe_response) return error.InvalidProviderServiceResponse;
    const providers = describe.payload.provider_describe_response.providers;
    for (providers, 0..) |provider, index| {
        if (provider.credential_grant != .none) return error.RemoteProviderCredentialGrantUnsupported;
        for (providers[0..index]) |prior| {
            if (std.mem.eql(u8, provider.id, prior.id)) return error.InvalidProviderServiceResponse;
        }
    }

    const list_request = try provider_envelope.serializeEnvelope(.{
        .id = "remote-models",
        .payload = .{ .provider_models_list_request = .{} },
    }, allocator);
    defer allocator.free(list_request);
    const list_line = try client.postUnary(list_request);
    defer allocator.free(list_line);
    var list = try provider_envelope.deserializeEnvelope(list_line, allocator);
    defer list.deinit(allocator);
    if (!std.mem.eql(u8, list.in_reply_to orelse "", "remote-models")) return error.InvalidProviderServiceResponse;
    if (list.payload != .provider_models_list_response) return error.InvalidProviderServiceResponse;
    var refs = std.ArrayList([]u8).empty;
    errdefer {
        for (refs.items) |ref| allocator.free(ref);
        refs.deinit(allocator);
    }
    for (list.payload.provider_models_list_response.models) |model| {
        const parsed = provider_types.parseModelRef(model.model_ref) orelse return error.InvalidProviderServiceResponse;
        if (!std.mem.eql(u8, parsed.provider_id, model.provider_id) or
            !std.mem.eql(u8, parsed.model_id, model.model_id) or
            parsed.wire != model.wire) return error.InvalidProviderServiceResponse;
        var known = false;
        for (providers) |provider| {
            if (!std.mem.eql(u8, provider.id, model.provider_id)) continue;
            const same_wire_id = if (provider.wire_id) |wire_id|
                parsed.wire_id != null and std.mem.eql(u8, wire_id, parsed.wire_id.?)
            else
                parsed.wire_id == null;
            if (provider.wire != model.wire or !same_wire_id) return error.InvalidProviderServiceResponse;
            known = true;
        }
        if (!known) return error.InvalidProviderServiceResponse;
        const ref = try allocator.dupe(u8, model.model_ref);
        errdefer allocator.free(ref);
        try refs.append(allocator, ref);
    }
    return refs.toOwnedSlice(allocator);
}

const State = struct {
    allocator: std.mem.Allocator,
    client: http_client.Client,
    mutex: std.Io.Mutex = .init,
    pending: std.ArrayList([]u8) = .empty,
    thread: ?std.Thread = null,
    done: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    closing: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    failure: ?anyerror = null,
    request_line: ?[]u8 = null,
    abandoned: bool = false,

    fn destroy(self: *State) void {
        if (self.request_line) |line| self.allocator.free(line);
        for (self.pending.items) |line| self.allocator.free(line);
        self.pending.deinit(self.allocator);
        self.client.deinit();
        self.allocator.destroy(self);
    }

    fn enqueue(self: *State, line: []const u8) !void {
        const copy = try self.allocator.dupe(u8, line);
        errdefer self.allocator.free(copy);
        while (true) {
            if (self.closing.load(.acquire)) return error.RemoteTransportClosed;
            self.mutex.lockUncancelable(defaultIo());
            if (self.pending.items.len < 128) {
                defer self.mutex.unlock(defaultIo());
                try self.pending.append(self.allocator, copy);
                return;
            }
            self.mutex.unlock(defaultIo());
            compat.time.sleepMs(1);
        }
    }
};

fn defaultIo() std.Io {
    return if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
}

pub fn open(
    context: ?*anyopaque,
    allocator: std.mem.Allocator,
    _: ai_types.Model,
    api_key: ?[]const u8,
) !bridge.Transport {
    if (api_key != null) return error.RemoteCallerCredentialUnavailable;
    const config: *const Config = @ptrCast(@alignCast(context));
    const state = try allocator.create(State);
    errdefer allocator.destroy(state);
    state.* = .{
        .allocator = allocator,
        .client = try http_client.Client.init(allocator, config.base_url, config.security),
    };
    return .{
        .ctx = state,
        .send_line_fn = sendLine,
        .pump_fn = pump,
        .recv_line_fn = recvLine,
        .close_fn = close,
    };
}

fn onEnvelope(context: ?*anyopaque, line: []const u8) !void {
    const state: *State = @ptrCast(@alignCast(context));
    var envelope = try provider_envelope.deserializeEnvelope(line, state.allocator);
    defer envelope.deinit(state.allocator);
    try state.enqueue(line);
    switch (envelope.payload) {
        .inference_completed, .inference_failed, .protocol_error => return error.RemoteStreamTerminal,
        .inference_create_response => |response| if (!response.accepted) return error.RemoteStreamTerminal,
        else => {},
    }
}

fn runStream(state: *State) void {
    defer {
        state.mutex.lockUncancelable(defaultIo());
        const abandoned = state.abandoned;
        state.done.store(true, .release);
        state.mutex.unlock(defaultIo());
        if (abandoned) state.destroy();
    }
    const result = state.client.postStream(state.request_line.?, state, onEnvelope);
    if (result) |_| {} else |err| {
        if (err == error.RemoteStreamTerminal) return;
        state.mutex.lockUncancelable(defaultIo());
        state.failure = err;
        state.mutex.unlock(defaultIo());
    }
}

fn sendLine(context: ?*anyopaque, line: []const u8) !void {
    const state: *State = @ptrCast(@alignCast(context));
    if (state.thread == null) {
        state.request_line = try state.allocator.dupe(u8, line);
        errdefer {
            state.allocator.free(state.request_line.?);
            state.request_line = null;
        }
        state.thread = try std.Thread.spawn(.{}, runStream, .{state});
        return;
    }
    const answer = try state.client.postUnary(line);
    defer state.allocator.free(answer);
    try validateCancelAnswer(state.allocator, line, answer);
}

fn validateCancelAnswer(allocator: std.mem.Allocator, request_line: []const u8, answer_line: []const u8) !void {
    var request = try provider_envelope.deserializeEnvelope(request_line, allocator);
    defer request.deinit(allocator);
    if (request.payload != .inference_cancel_request) return error.UnexpectedProviderControl;
    var answer = try provider_envelope.deserializeEnvelope(answer_line, allocator);
    defer answer.deinit(allocator);
    if (!std.mem.eql(u8, answer.in_reply_to orelse "", request.id)) return error.InvalidProviderServiceResponse;
    switch (answer.payload) {
        .inference_cancel_response => {},
        .protocol_error => return error.ProviderServiceCancelRefused,
        else => return error.InvalidProviderServiceResponse,
    }
}

test "cancel response is consumed without joining the bounded inference queue" {
    const request = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"inference.cancel.request\",\"id\":\"bridge.cancel\",\"inference_id\":\"i1\",\"payload\":{}}";
    const answer = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"inference.cancel.response\",\"id\":\"answer\",\"in_reply_to\":\"bridge.cancel\",\"inference_id\":\"i1\",\"payload\":{\"accepted\":true}}";
    try validateCancelAnswer(std.testing.allocator, request, answer);
}

fn pump(context: ?*anyopaque) !void {
    const state: *State = @ptrCast(@alignCast(context));
    state.mutex.lockUncancelable(defaultIo());
    defer state.mutex.unlock(defaultIo());
    if (state.pending.items.len == 0 and state.done.load(.acquire)) {
        if (state.failure) |err| return err;
        return error.ProviderStreamEndedWithoutTerminal;
    }
}

fn recvLine(context: ?*anyopaque, allocator: std.mem.Allocator) !?[]u8 {
    const state: *State = @ptrCast(@alignCast(context));
    state.mutex.lockUncancelable(defaultIo());
    const line = if (state.pending.items.len > 0) state.pending.orderedRemove(0) else null;
    state.mutex.unlock(defaultIo());
    const owned = line orelse return null;
    if (allocator.ptr == state.allocator.ptr) return owned;
    defer state.allocator.free(owned);
    return try allocator.dupe(u8, owned);
}

fn close(context: ?*anyopaque) void {
    const state: *State = @ptrCast(@alignCast(context));
    state.closing.store(true, .release);
    if (state.thread) |thread| {
        var waited: u64 = 0;
        while (!state.done.load(.acquire) and waited < 3000) : (waited += 1) compat.time.sleepMs(1);
        state.mutex.lockUncancelable(defaultIo());
        const done = state.done.load(.acquire);
        if (!done) state.abandoned = true;
        state.mutex.unlock(defaultIo());
        if (!done) {
            thread.detach();
            return;
        }
        thread.join();
    }
    state.destroy();
}

test "remote provider transport refuses caller-held credentials" {
    var config = Config{ .base_url = "http://127.0.0.1:8080", .security = .loopback };
    try std.testing.expectError(
        error.RemoteCallerCredentialUnavailable,
        open(&config, std.testing.allocator, undefined, "secret"),
    );
}

const DiscoveryServer = struct {
    server: compat.net.Server,
    thread: ?std.Thread = null,
    grant: []const u8 = "none",
    stream: bool = false,

    fn start(self: *DiscoveryServer) !void {
        self.thread = try std.Thread.spawn(.{}, serve, .{self});
    }

    fn stop(self: *DiscoveryServer) void {
        if (self.thread) |thread| thread.join();
        compat.net.closeServer(&self.server);
    }

    fn serve(self: *DiscoveryServer) void {
        const model_response = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"provider.models.list.response\",\"id\":\"models-answer\",\"in_reply_to\":\"remote-models\",\"payload\":{\"models\":[{\"model_ref\":\"remote/other:mock@sample\",\"model_id\":\"sample\",\"provider_id\":\"remote\",\"wire\":\"other\"}]}}";
        const stream_response =
            "data: {\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"inference.create.response\",\"id\":\"created\",\"in_reply_to\":\"bridge.create\",\"inference_id\":\"i1\",\"payload\":{\"accepted\":true}}\n\n" ++
            "data: {\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"inference.completed\",\"id\":\"completed\",\"inference_id\":\"i1\",\"sequence\":1,\"payload\":{\"stop_reason\":\"stop\",\"message\":{\"role\":\"assistant\",\"content\":\"hello\"}}}\n\n";
        const request_count: usize = if (self.stream or !std.mem.eql(u8, self.grant, "none")) 1 else 2;
        for (0..request_count) |index| {
            var conn = compat.net.accept(&self.server) catch return;
            defer conn.stream.close();
            var header: [8192]u8 = undefined;
            var filled: usize = 0;
            while (filled < header.len) {
                const n = conn.stream.read(header[filled .. filled + 1]) catch return;
                if (n == 0) return;
                filled += n;
                if (filled >= 4 and std.mem.eql(u8, header[filled - 4 .. filled], "\r\n\r\n")) break;
            }
            var lines = std.mem.splitSequence(u8, header[0..filled], "\r\n");
            var remaining: usize = 0;
            while (lines.next()) |line| {
                if (!std.ascii.startsWithIgnoreCase(line, "content-length:")) continue;
                remaining = std.fmt.parseInt(usize, std.mem.trim(u8, line["content-length:".len..], " \t"), 10) catch return;
            }
            while (remaining > 0) {
                const n = conn.stream.read(header[0..@min(remaining, header.len)]) catch return;
                if (n == 0) return;
                remaining -= n;
            }
            const response_body = if (self.stream)
                stream_response
            else if (index == 0)
                std.fmt.allocPrint(std.heap.page_allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"provider.describe.response\",\"id\":\"describe-answer\",\"in_reply_to\":\"remote-describe\",\"payload\":{{\"providers\":[{{\"id\":\"remote\",\"wire\":\"other\",\"wire_id\":\"mock\",\"framing\":\"sse\",\"endpoint\":\"https://vendor.invalid\",\"credential_grant\":\"{s}\"}}]}}}}", .{self.grant}) catch return
            else
                model_response;
            defer if (!self.stream and index == 0) std.heap.page_allocator.free(response_body);
            var answer_header: [160]u8 = undefined;
            const response = std.fmt.bufPrint(&answer_header, "HTTP/1.1 200 OK\r\nContent-Type: {s}\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{ if (self.stream) "text/event-stream" else "application/json", response_body.len }) catch return;
            conn.stream.writeAll(response) catch return;
            conn.stream.writeAll(response_body) catch return;
        }
    }
};

test "remote provider discovery accepts managed credentials and rejects grant channels" {
    for ([_][]const u8{ "none", "out_of_band" }) |grant| {
        const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
        var server = DiscoveryServer{ .server = try compat.net.tcpListen(address, .{ .reuse_address = true }), .grant = grant };
        try server.start();
        defer server.stop();
        const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
        defer std.testing.allocator.free(url);
        const config = Config{ .base_url = url, .security = .loopback };
        if (std.mem.eql(u8, grant, "none")) {
            const models = try discoverModels(std.testing.allocator, &config);
            defer {
                for (models) |model| std.testing.allocator.free(model);
                std.testing.allocator.free(models);
            }
            try std.testing.expectEqual(@as(usize, 1), models.len);
            try std.testing.expectEqualStrings("remote/other:mock@sample", models[0]);
        } else {
            try std.testing.expectError(error.RemoteProviderCredentialGrantUnsupported, discoverModels(std.testing.allocator, &config));
        }
    }
}

test "remote transport carries a streamed inference through its terminal" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = DiscoveryServer{ .server = try compat.net.tcpListen(address, .{ .reuse_address = true }), .stream = true };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var config = Config{ .base_url = url, .security = .loopback };
    const transport = try open(&config, std.testing.allocator, undefined, null);
    defer transport.close();
    try transport.send_line_fn(transport.ctx, "{\"type\":\"inference.create.request\"}");
    var count: usize = 0;
    var waited: usize = 0;
    while (count < 2 and waited < 1000) : (waited += 1) {
        try transport.pump_fn(transport.ctx);
        while (try transport.recv_line_fn(transport.ctx, std.testing.allocator)) |line| {
            defer std.testing.allocator.free(line);
            count += 1;
        }
        if (count < 2) compat.time.sleepMs(1);
    }
    try std.testing.expectEqual(@as(usize, 2), count);
}
