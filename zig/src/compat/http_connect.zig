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
        .proxied_host = host,
        .proxied_port = port,
    });
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

pub fn connect(client: *Client, host: HostName, port: u16, protocol: Protocol) ConnectError!*Connection {
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
