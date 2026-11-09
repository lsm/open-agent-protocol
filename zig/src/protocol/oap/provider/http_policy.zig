const std = @import("std");

pub const Security = enum {
    loopback,
    tls,
    mesh_proxy,
};

pub const ENDPOINT_PATH = "/oap/v0.1/provider";

pub fn validateToken(token: ?[]const u8) !void {
    const value = token orelse return;
    const content = std.mem.trimEnd(u8, value, "=");
    if (content.len == 0) return error.InvalidProviderServiceToken;
    for (content) |byte| switch (byte) {
        'A'...'Z', 'a'...'z', '0'...'9', '-', '.', '_', '~', '+', '/' => {},
        else => return error.InvalidProviderServiceToken,
    };
}

pub fn validateBaseUrl(url: []const u8, security: Security) !std.Uri {
    const uri = std.Uri.parse(url) catch return error.InvalidProviderServiceUrl;
    if (uri.host == null or uri.user != null or uri.password != null or uri.fragment != null or uri.query != null) {
        return error.InvalidProviderServiceUrl;
    }
    if (!uri.path.isEmpty() and !std.mem.eql(u8, uri.path.percent_encoded, "/")) {
        return error.InvalidProviderServiceUrl;
    }

    var host_buffer: [std.Io.net.HostName.max_len]u8 = undefined;
    const host = host: {
        const raw = uri.host.?.toRaw(&host_buffer) catch return error.InvalidProviderServiceUrl;
        if (std.mem.startsWith(u8, raw, "[") and std.mem.endsWith(u8, raw, "]")) {
            _ = std.Io.net.IpAddress.parseIp6(raw[1 .. raw.len - 1], 0) catch return error.InvalidProviderServiceUrl;
            break :host raw;
        }
        break :host (std.Io.net.HostName.fromUri(uri, &host_buffer) catch return error.InvalidProviderServiceUrl).bytes;
    };
    const is_loopback = std.mem.eql(u8, host, "127.0.0.1") or
        std.mem.eql(u8, host, "::1") or std.mem.eql(u8, host, "[::1]");

    switch (security) {
        .loopback, .mesh_proxy => {
            if (!std.mem.eql(u8, uri.scheme, "http") or !is_loopback) return error.UnprotectedProviderService;
        },
        .tls => {
            if (!std.mem.eql(u8, uri.scheme, "https")) return error.UnprotectedProviderService;
        },
    }
    return uri;
}

test "provider service policy accepts loopback and authenticated transport choices" {
    _ = try validateBaseUrl("http://127.0.0.1:8080", .loopback);
    _ = try validateBaseUrl("http://[::1]:8080", .loopback);
    _ = try validateBaseUrl("http://127.0.0.1:15001", .mesh_proxy);
    _ = try validateBaseUrl("https://provider.example:443", .tls);
    _ = try validateBaseUrl("https://[2001:db8::1]:443", .tls);
}

test "provider service policy refuses unprotected remote and ambiguous URLs" {
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://[2001:db8::1]:8080", .loopback));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://[not-an-ip]", .tls));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://provider.default.svc.cluster.local:8080", .loopback));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://provider.default.svc.cluster.local:8080", .mesh_proxy));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://127.0.0.1.evil:8080", .loopback));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://provider.example", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://user:secret@provider.example", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://provider.example/?redirect=other", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://provider.example/#fragment", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://provider.example/other", .tls));
}

test "service token validation accepts exactly the b64token alphabet with trailing padding" {
    try validateToken(null);
    for ([_][]const u8{ "a", "abcDEF012-._~+/", "a=", "a==", "a====" }) |token| try validateToken(token);
    for ([_][]const u8{ "caf\u{e9}", "a=b", "=", "==", "a?b", "a:b", "a,b", "a[b" }) |token| {
        try std.testing.expectError(error.InvalidProviderServiceToken, validateToken(token));
    }
    for ([_][]const u8{ "", " ", "a\tb", "a\r\nb", "a\x00b", "a\x7fb", "a\u{85}b", "a\u{a0}b", "a\u{2003}b", "a\u{2028}b", "a\u{3000}b" }) |token| {
        try std.testing.expectError(error.InvalidProviderServiceToken, validateToken(token));
    }
}
