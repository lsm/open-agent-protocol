fn connectHttpsProxy(
    client: *Client,
    proxy: *Proxy,
    host: HostName,
    port: u16,
) !*Connection {
    const io = client.io;
    if (proxy.protocol != .plain) return error.UnsupportedProxyProtocol;
    if (try client.connection_pool.findConnection(io, .{
        .host = host,
        .port = port,
        .protocol = .tls,
    })) |connection| return connection;

    const connection = try client.connectTcpOptions(.{
        .host = proxy.host,
        .port = proxy.port,
        .protocol = .plain,
    });
    connection.proxied = false;
    errdefer {
        connection.closing = true;
        client.connection_pool.release(connection, io);
    }
    const authorization = [_]http.Header{.{ .name = "proxy-authorization", .value = proxy.authorization orelse "" }};
    var tunnel_req = try client.request(.CONNECT, .{
        .scheme = "http",
        .host = .{ .raw = host.bytes },
        .port = port,
    }, .{
        .redirect_behavior = .unhandled,
        .connection = connection,
        .extra_headers = if (proxy.authorization != null) &authorization else &.{},
    });
    defer {
        tunnel_req.connection = null;
        tunnel_req.deinit();
    }
    try tunnel_req.sendBodiless();
    const response = try tunnel_req.receiveHead(&.{});
    if (response.head.status != .ok) return error.ProxyConnectRefused;
    if (connection.reader().bufferedLen() != 0) return error.InvalidProxyConnectResponse;

    if (disable_tls) return error.TlsInitializationFailed;
    const tls = try Connection.Tls.create(client, host, port, connection.getStream());
    errdefer tls.destroy();
    try client.connection_pool.addUsed(io, &tls.connection);
    tunnel_req.connection = null;
    client.connection_pool.mutex.lockUncancelable(io);
    client.connection_pool.used.remove(&connection.pool_node);
    client.connection_pool.mutex.unlock(io);
    Connection.Plain.destroy(@alignCast(@fieldParentPtr("connection", connection)));
    return &tls.connection;
}

fn bypassProxy(exclusions: []const u8, destination: []const u8, port: u16) bool {
    const host = std.mem.trimEnd(u8, destination, ".");
    var entries = std.mem.splitScalar(u8, exclusions, ',');
    while (entries.next()) |entry| {
        var pattern = std.mem.trim(u8, entry, " \t");
        if (std.mem.eql(u8, pattern, "*")) return true;
        if (std.mem.findScalarLast(u8, pattern, ':')) |separator| {
            const required_port = std.fmt.parseInt(u16, pattern[separator + 1 ..], 10) catch continue;
            if (required_port != port) continue;
            pattern = pattern[0..separator];
        }
        pattern = std.mem.trim(u8, pattern, ".");
        if (pattern.len == 0 or host.len < pattern.len) continue;
        const offset = host.len - pattern.len;
        if (!std.ascii.eqlIgnoreCase(host[offset..], pattern)) continue;
        if (offset == 0 or host[offset - 1] == '.') return true;
    }
    return false;
}

pub fn connect(client: *Client, host: HostName, port: u16, protocol: Protocol) ConnectError!*Connection {
    if (bypassProxy(client.no_proxy, host.bytes, port)) return client.connectTcp(host, port, protocol);
    if (switch (protocol) {
        .plain => client.http_proxy_invalid,
        .tls => client.https_proxy_invalid,
    }) return error.UnsupportedProxyProtocol;
    const proxy = switch (protocol) {
        .plain => client.http_proxy,
        .tls => client.https_proxy,
    } orelse return client.connectTcp(host, port, protocol);
    if (protocol == .tls) {
        return connectHttpsProxy(client, proxy, host, port) catch |err| switch (err) {
            error.OutOfMemory => error.OutOfMemory,
            error.Canceled => error.Canceled,
            else => error.TlsInitializationFailed,
        };
    }
    if (proxy.host.eql(host) and proxy.port == port and proxy.protocol == protocol) {
        return client.connectTcp(host, port, protocol);
    }
    if (proxy.supports_connect) tunnel: {
        return connectProxied(client, proxy, host, port) catch |err| switch (err) {
            error.TunnelNotSupported => break :tunnel,
            else => |e| return e,
        };
    }
    const connection = try client.connectTcp(proxy.host, proxy.port, proxy.protocol);
    connection.proxied = true;
    return connection;
}

fn configuredProxy(arena: Allocator, environ_map: *const std.process.Environ.Map, names: []const []const u8, invalid: *bool) Allocator.Error!?*Proxy {
    invalid.* = false;
    const proxy = createProxyFromEnvVar(arena, environ_map, names) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => {
            invalid.* = true;
            return null;
        },
    };
    if (proxy == null) {
        for (names) |name| {
            if (environ_map.get(name)) |value| {
                if (value.len == 0) continue;
                invalid.* = true;
                break;
            }
        }
    }
    return proxy;
}

pub fn initDefaultProxies(client: *Client, arena: Allocator, environ_map: *const std.process.Environ.Map) !void {
    const io = client.io;
    const pool = &client.connection_pool;
    try pool.mutex.lock(io);
    defer pool.mutex.unlock(io);
    assert(pool.used.first == null);
    while (pool.free.popFirst()) |node| {
        const connection: *Connection = @alignCast(@fieldParentPtr("pool_node", node));
        connection.destroy(io);
    }
    pool.free_len = 0;
    client.no_proxy = try arena.dupe(u8, environ_map.get("no_proxy") orelse environ_map.get("NO_PROXY") orelse "");
    if (client.http_proxy == null) {
        client.http_proxy = try configuredProxy(arena, environ_map, &.{
            "http_proxy", "HTTP_PROXY", "all_proxy", "ALL_PROXY",
        }, &client.http_proxy_invalid);
    }
    if (client.https_proxy == null) {
        client.https_proxy = try configuredProxy(arena, environ_map, &.{
            "https_proxy", "HTTPS_PROXY", "all_proxy", "ALL_PROXY",
        }, &client.https_proxy_invalid);
    }
}
