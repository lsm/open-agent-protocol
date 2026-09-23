const std = @import("std");
const compat = @import("compat");
const policy = @import("oap_provider_http_policy");
const sse_parser = @import("sse_parser");

pub const MAX_RESPONSE_BYTES: usize = 1024 * 1024;

fn hasMediaType(actual: []const u8, expected: []const u8) bool {
    return std.mem.eql(u8, actual, expected) or
        (actual.len > expected.len and std.mem.startsWith(u8, actual, expected) and actual[expected.len] == ';');
}

pub const Client = struct {
    allocator: std.mem.Allocator,
    base_url: []u8,
    security: policy.Security,

    pub fn init(allocator: std.mem.Allocator, base_url: []const u8, security: policy.Security) !Client {
        _ = try policy.validateBaseUrl(base_url, security);
        return .{
            .allocator = allocator,
            .base_url = try allocator.dupe(u8, base_url),
            .security = security,
        };
    }

    pub fn deinit(self: *Client) void {
        self.allocator.free(self.base_url);
        self.* = undefined;
    }

    pub fn postUnary(self: *Client, body: []const u8) ![]u8 {
        _ = try policy.validateBaseUrl(self.base_url, self.security);
        const trimmed = std.mem.trimEnd(u8, self.base_url, "/");
        const endpoint = try std.fmt.allocPrint(self.allocator, "{s}{s}", .{ trimmed, policy.ENDPOINT_PATH });
        defer self.allocator.free(endpoint);
        const uri = std.Uri.parse(endpoint) catch return error.InvalidProviderServiceUrl;

        var http = compat.http.HttpClient.init(self.allocator);
        defer http.deinit();
        var request = try http.openRequest(.POST, uri, .{
            .extra_headers = &.{
                .{ .name = "Content-Type", .value = "application/json" },
                .{ .name = "Accept", .value = "application/json" },
            },
            .keep_alive = false,
        });
        defer request.deinit();
        try compat.http.sendRequest(&request, body);
        var redirect_buffer: [4096]u8 = undefined;
        var response = try compat.http.receiveResponse(&request, &redirect_buffer);
        if (response.head.status != .ok) return error.ProviderServiceHttpFailure;
        if (response.head.content_type == null or
            !hasMediaType(response.head.content_type.?, "application/json"))
        {
            return error.UnexpectedProviderServiceContentType;
        }
        var transfer_buffer: [4096]u8 = undefined;
        const reader = compat.http.responseReader(&response, &transfer_buffer);
        return compat.http.allocRemainingResponse(self.allocator, reader, MAX_RESPONSE_BYTES);
    }

    pub fn postStream(
        self: *Client,
        body: []const u8,
        context: ?*anyopaque,
        on_envelope: *const fn (?*anyopaque, []const u8) anyerror!void,
    ) !void {
        _ = try policy.validateBaseUrl(self.base_url, self.security);
        const trimmed = std.mem.trimEnd(u8, self.base_url, "/");
        const endpoint = try std.fmt.allocPrint(self.allocator, "{s}{s}", .{ trimmed, policy.ENDPOINT_PATH });
        defer self.allocator.free(endpoint);
        const uri = std.Uri.parse(endpoint) catch return error.InvalidProviderServiceUrl;

        var http = compat.http.HttpClient.init(self.allocator);
        defer http.deinit();
        var request = try http.openRequest(.POST, uri, .{
            .extra_headers = &.{
                .{ .name = "Content-Type", .value = "application/json" },
                .{ .name = "Accept", .value = "text/event-stream, application/json" },
            },
            .keep_alive = false,
        });
        defer request.deinit();
        try compat.http.sendRequest(&request, body);
        var redirect_buffer: [4096]u8 = undefined;
        var response = try compat.http.receiveResponse(&request, &redirect_buffer);
        if (response.head.status != .ok) return error.ProviderServiceHttpFailure;
        const content_type = response.head.content_type orelse return error.UnexpectedProviderServiceContentType;
        const is_stream = hasMediaType(content_type, "text/event-stream");
        if (!is_stream and !hasMediaType(content_type, "application/json")) {
            return error.UnexpectedProviderServiceContentType;
        }
        var transfer_buffer: [4096]u8 = undefined;
        const reader = compat.http.responseReader(&response, &transfer_buffer);
        if (!is_stream) {
            const answer = try compat.http.allocRemainingResponse(self.allocator, reader, MAX_RESPONSE_BYTES);
            defer self.allocator.free(answer);
            try on_envelope(context, answer);
            return;
        }

        var parser = sse_parser.SSEParser.initWithLimits(self.allocator, .{
            .line_bytes = MAX_RESPONSE_BYTES,
            .event_bytes = MAX_RESPONSE_BYTES,
        });
        defer parser.deinit();
        var buffer: [4096]u8 = undefined;
        while (true) {
            const n = try compat.http.readResponse(reader, &buffer);
            if (n == 0) break;
            const events = try parser.feed(buffer[0..n]);
            for (events) |event| try on_envelope(context, event.data);
        }
    }
};

test "remote provider client validates the operator URL before network access" {
    try std.testing.expectError(error.UnprotectedProviderService, Client.init(std.testing.allocator, "http://provider.svc.cluster.local", .loopback));
    try std.testing.expectError(error.InvalidProviderServiceUrl, Client.init(std.testing.allocator, "https://user:pass@provider.test", .tls));
    var client = try Client.init(std.testing.allocator, "https://provider.test", .tls);
    client.deinit();
}

const MockUnaryServer = struct {
    server: compat.net.Server,
    thread: ?std.Thread = null,
    saw_provider_path: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    response_type: []const u8 = "application/json",
    response_body: []const u8 = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"provider.describe.response\",\"id\":\"answer\",\"in_reply_to\":\"request\",\"capability_revision\":\"r1\",\"payload\":{\"providers\":[]}}",

    fn start(self: *MockUnaryServer) !void {
        self.thread = try std.Thread.spawn(.{}, serve, .{self});
    }

    fn stop(self: *MockUnaryServer) void {
        if (self.thread) |thread| thread.join();
        compat.net.closeServer(&self.server);
    }

    fn serve(self: *MockUnaryServer) void {
        var conn = compat.net.accept(&self.server) catch return;
        defer conn.stream.close();
        var request: [8192]u8 = undefined;
        var filled: usize = 0;
        while (filled < request.len) {
            const n = conn.stream.read(request[filled .. filled + 1]) catch return;
            if (n == 0) return;
            filled += n;
            if (filled >= 4 and std.mem.eql(u8, request[filled - 4 .. filled], "\r\n\r\n")) break;
        }
        self.saw_provider_path.store(std.mem.startsWith(u8, request[0..filled], "POST /oap/v0.1/provider HTTP/1.1"), .release);
        var lines = std.mem.splitSequence(u8, request[0..filled], "\r\n");
        var content_length: usize = 0;
        while (lines.next()) |line| {
            if (!std.ascii.startsWithIgnoreCase(line, "content-length:")) continue;
            content_length = std.fmt.parseInt(usize, std.mem.trim(u8, line["content-length:".len..], " \t"), 10) catch return;
        }
        var remaining = content_length;
        while (remaining > 0) {
            const n = conn.stream.read(request[0..@min(remaining, request.len)]) catch return;
            if (n == 0) return;
            remaining -= n;
        }
        var header: [160]u8 = undefined;
        const response = std.fmt.bufPrint(&header, "HTTP/1.1 200 OK\r\nContent-Type: {s}\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{ self.response_type, self.response_body.len }) catch return;
        conn.stream.writeAll(response) catch return;
        conn.stream.writeAll(self.response_body) catch return;
    }
};

test "remote provider client posts an OAP envelope and receives a unary answer" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{ .server = try compat.net.tcpListen(address, .{ .reuse_address = true }) };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    const answer = try client.postUnary("{\"type\":\"provider.describe.request\"}");
    defer std.testing.allocator.free(answer);
    try std.testing.expect(std.mem.indexOf(u8, answer, "provider.describe.response") != null);
    try std.testing.expect(server.saw_provider_path.load(.acquire));
}

const FrameCollector = struct {
    count: usize = 0,
    first: ?[]u8 = null,

    fn collect(context: ?*anyopaque, line: []const u8) !void {
        const self: *FrameCollector = @ptrCast(@alignCast(context));
        self.count += 1;
        if (self.first == null) self.first = try std.testing.allocator.dupe(u8, line);
    }
};

test "remote provider client consumes SSE envelopes" {
    const address = try compat.net.resolveAddress(std.testing.allocator, "127.0.0.1", 0);
    var server = MockUnaryServer{
        .server = try compat.net.tcpListen(address, .{ .reuse_address = true }),
        .response_type = "text/event-stream",
        .response_body = "data: {\"type\":\"inference.create.response\"}\n\ndata: {\"type\":\"inference.completed\"}\n\n",
    };
    try server.start();
    defer server.stop();
    const url = try std.fmt.allocPrint(std.testing.allocator, "http://127.0.0.1:{d}", .{compat.net.listenAddress(&server.server).getPort()});
    defer std.testing.allocator.free(url);
    var client = try Client.init(std.testing.allocator, url, .loopback);
    defer client.deinit();
    var collector = FrameCollector{};
    defer if (collector.first) |line| std.testing.allocator.free(line);
    try client.postStream("{\"type\":\"inference.create.request\"}", &collector, FrameCollector.collect);
    try std.testing.expectEqual(@as(usize, 2), collector.count);
    try std.testing.expectEqualStrings("{\"type\":\"inference.create.response\"}", collector.first.?);
}
