const std = @import("std");

pub const Security = enum {
    loopback,
    tls,
    mesh_proxy,
};

pub const ENDPOINT_PATH = "/oap/v0.1/provider";

pub fn validateBaseUrl(url: []const u8, security: Security) !std.Uri {
    const uri = std.Uri.parse(url) catch return error.InvalidProviderServiceUrl;
    if (uri.host == null or uri.user != null or uri.password != null or uri.fragment != null or uri.query != null) {
        return error.InvalidProviderServiceUrl;
    }
    if (!uri.path.isEmpty() and !std.mem.eql(u8, uri.path.percent_encoded, "/")) {
        return error.InvalidProviderServiceUrl;
    }

    var host_buffer: [std.Io.net.HostName.max_len]u8 = undefined;
    const host = (uri.getHost(&host_buffer) catch return error.InvalidProviderServiceUrl).bytes;
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
}

test "provider service policy refuses unprotected remote and ambiguous URLs" {
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://provider.default.svc.cluster.local:8080", .loopback));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://provider.default.svc.cluster.local:8080", .mesh_proxy));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://127.0.0.1.evil:8080", .loopback));
    try std.testing.expectError(error.UnprotectedProviderService, validateBaseUrl("http://provider.example", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://user:secret@provider.example", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://provider.example/?redirect=other", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://provider.example/#fragment", .tls));
    try std.testing.expectError(error.InvalidProviderServiceUrl, validateBaseUrl("https://provider.example/other", .tls));
}
