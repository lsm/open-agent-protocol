const std = @import("std");
const compat = @import("compat");
const http = compat.http;
const pkce_mod = @import("oauth/pkce");
const loopback = @import("oauth/loopback");

const client_id = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
const redirect_uri = "https://console.anthropic.com/oauth/code/callback";
const scopes = "org:create_api_key%20user:profile%20user:inference";
const auth_url_base = "https://claude.ai/oauth/authorize";
pub const oauth_request_timeout_ms: u64 = 30_000;

const token_url = "https://console.anthropic.com/v1/oauth/token";

pub const Credentials = struct {
    refresh: []const u8,
    access: []const u8,
    expires: i64,
};

pub const Callbacks = struct {
    onAuth: *const fn (info: AuthInfo) void,
    onPrompt: *const fn (prompt: Prompt) []const u8,
};

pub const AuthInfo = struct {
    url: []const u8,
    instructions: ?[]const u8 = null,
};

pub const Fetch = *const fn (allocator: std.mem.Allocator, url: []const u8, options: http.FetchOptions) http.FetchError!http.Fetched;

pub const LoopbackCallbacks = struct {
    onAuth: *const fn (info: AuthInfo) void,
    isCancelled: *const fn () bool,
    fetch: Fetch = http.fetch,
    wait_ms: i64 = loopback.default_wait_ms,
};

pub const loopback_supported = loopback.supported;

pub const Prompt = struct {
    message: []const u8,
    allow_empty: bool = false,
};

fn buildAuthUrl(allocator: std.mem.Allocator, challenge: []const u8, state: []const u8) ![]u8 {
    return try std.fmt.allocPrint(
        allocator,
        "{s}?code=true&client_id={s}&redirect_uri={s}&scope={s}&response_type=code&code_challenge={s}&code_challenge_method=S256&state={s}",
        .{ auth_url_base, client_id, redirect_uri, scopes, challenge, state },
    );
}

pub fn login(callbacks: Callbacks, allocator: std.mem.Allocator) !Credentials {
    const pkce = try pkce_mod.generate(allocator);
    defer pkce.deinit(allocator);

    const auth_url = try buildAuthUrl(allocator, pkce.challenge, pkce.verifier);
    defer allocator.free(auth_url);

    callbacks.onAuth(.{
        .url = auth_url,
        .instructions = "Paste the code from the URL after '#code=' below:",
    });

    const manual_input = callbacks.onPrompt(.{ .message = "Enter code:" });
    defer allocator.free(manual_input);

    const parsed_auth = try parseAuthFromManualInput(allocator, manual_input);
    defer allocator.free(parsed_auth.code);
    defer allocator.free(parsed_auth.state);

    const state_for_exchange = if (parsed_auth.state.len > 0) parsed_auth.state else pkce.verifier;
    const token_response = try exchangeCode(parsed_auth.code, state_for_exchange, pkce.verifier, allocator);
    defer allocator.free(token_response.refresh_token);
    defer allocator.free(token_response.access_token);

    const expires = compat.time.nowMillis() + (token_response.expires_in * 1000) - (5 * 60 * 1000);

    return .{
        .refresh = try allocator.dupe(u8, token_response.refresh_token),
        .access = try allocator.dupe(u8, token_response.access_token),
        .expires = expires,
    };
}

pub fn loginWithLoopback(callbacks: LoopbackCallbacks, allocator: std.mem.Allocator) !Credentials {
    var listener = try loopback.Listener.open();
    defer listener.close();
    const callback_uri = try listener.redirectUri(allocator);
    defer allocator.free(callback_uri);

    const pkce = try pkce_mod.generate(allocator);
    defer pkce.deinit(allocator);
    const state = try generateState(allocator);
    defer allocator.free(state);

    const auth_url = try buildLoopbackAuthUrl(allocator, callback_uri, pkce.challenge, state);
    defer allocator.free(auth_url);
    callbacks.onAuth(.{
        .url = auth_url,
        .instructions = "Open this URL in a browser on this computer; the login finishes on its own.",
    });

    const code = try listener.waitForCode(allocator, state, callbacks.isCancelled, callbacks.wait_ms);
    defer allocator.free(code);

    const body = try std.json.Stringify.valueAlloc(allocator, .{
        .grant_type = "authorization_code",
        .client_id = client_id,
        .code = code,
        .state = state,
        .redirect_uri = callback_uri,
        .code_verifier = pkce.verifier,
    }, .{});
    defer allocator.free(body);

    const token_response = try exchangeTokensWith(callbacks.fetch, body, allocator);
    defer allocator.free(token_response.refresh_token);
    defer allocator.free(token_response.access_token);

    const refresh = try allocator.dupe(u8, token_response.refresh_token);
    errdefer allocator.free(refresh);
    const access = try allocator.dupe(u8, token_response.access_token);
    return .{
        .refresh = refresh,
        .access = access,
        .expires = compat.time.nowMillis() + (token_response.expires_in * 1000) - (5 * 60 * 1000),
    };
}

fn generateState(allocator: std.mem.Allocator) ![]u8 {
    var random_bytes: [32]u8 = undefined;
    compat.random.fillSecureBytes(&random_bytes);
    const encoder = std.base64.url_safe_no_pad.Encoder;
    const state = try allocator.alloc(u8, encoder.calcSize(random_bytes.len));
    _ = encoder.encode(state, &random_bytes);
    return state;
}

fn buildLoopbackAuthUrl(allocator: std.mem.Allocator, callback_uri: []const u8, challenge: []const u8, state: []const u8) ![]u8 {
    var encoded = std.ArrayList(u8).empty;
    defer encoded.deinit(allocator);
    for (callback_uri) |byte| {
        if (std.ascii.isAlphanumeric(byte) or byte == '-' or byte == '_' or byte == '.' or byte == '~') {
            try encoded.append(allocator, byte);
        } else {
            try encoded.print(allocator, "%{X:0>2}", .{byte});
        }
    }
    return try std.fmt.allocPrint(
        allocator,
        "{s}?code=true&client_id={s}&redirect_uri={s}&scope={s}&response_type=code&code_challenge={s}&code_challenge_method=S256&state={s}",
        .{ auth_url_base, client_id, encoded.items, scopes, challenge, state },
    );
}

pub fn refreshToken(credentials: Credentials, allocator: std.mem.Allocator) !Credentials {
    const body = try std.json.Stringify.valueAlloc(allocator, .{
        .grant_type = "refresh_token",
        .client_id = client_id,
        .refresh_token = credentials.refresh,
    }, .{});
    defer allocator.free(body);

    const token_response = try exchangeTokens(body, allocator);
    defer allocator.free(token_response.refresh_token);
    defer allocator.free(token_response.access_token);

    const expires = compat.time.nowMillis() + (token_response.expires_in * 1000) - (5 * 60 * 1000);

    return .{
        .refresh = try allocator.dupe(u8, token_response.refresh_token),
        .access = try allocator.dupe(u8, token_response.access_token),
        .expires = expires,
    };
}

pub fn getApiKey(credentials: Credentials, allocator: std.mem.Allocator) ![]const u8 {
    return try allocator.dupe(u8, credentials.access);
}

const ParsedAuth = struct {
    code: []const u8,
    state: []const u8,
};

fn parseAuthFromManualInput(allocator: std.mem.Allocator, input: []const u8) !ParsedAuth {
    if (std.mem.trim(u8, input, " \t\r\n").len == 0) return error.OAuthCancelled;

    if (std.mem.find(u8, input, "#code=")) |idx| {
        const code_start = idx + 6;
        var code_end = input.len;
        var state: []const u8 = "";

        if (std.mem.findAny(u8, input[code_start..], "#&")) |end| {
            code_end = code_start + end;
        }

        const code = try allocator.dupe(u8, input[code_start..code_end]);

        if (std.mem.find(u8, input, "&state=")) |state_idx| {
            const state_start = state_idx + 7;
            var state_end = input.len;
            if (std.mem.find(u8, input[state_start..], "&")) |end| {
                state_end = state_start + end;
            }
            state = try allocator.dupe(u8, input[state_start..state_end]);
        } else if (std.mem.find(u8, input, "#state=")) |state_idx| {
            const state_start = state_idx + 7;
            var state_end = input.len;
            if (std.mem.find(u8, input[state_start..], "&")) |end| {
                state_end = state_start + end;
            }
            state = try allocator.dupe(u8, input[state_start..state_end]);
        }

        return .{ .code = code, .state = state };
    }

    if (std.mem.find(u8, input, "?code=")) |idx| {
        const code_start = idx + 6;
        var code_end = input.len;
        var state: []const u8 = "";

        if (std.mem.findAny(u8, input[code_start..], "#&")) |end| {
            code_end = code_start + end;
        }

        const code = try allocator.dupe(u8, input[code_start..code_end]);

        if (std.mem.find(u8, input, "&state=")) |state_idx| {
            const state_start = state_idx + 7;
            var state_end = input.len;
            if (std.mem.find(u8, input[state_start..], "&")) |end| {
                state_end = state_start + end;
            }
            state = try allocator.dupe(u8, input[state_start..state_end]);
        }

        return .{ .code = code, .state = state };
    }

    if (std.mem.find(u8, input, "#")) |hash_idx| {
        const code = try allocator.dupe(u8, input[0..hash_idx]);
        const state = try allocator.dupe(u8, input[hash_idx + 1 ..]);
        return .{ .code = code, .state = state };
    }

    return .{
        .code = try allocator.dupe(u8, input),
        .state = try allocator.dupe(u8, ""),
    };
}

const TokenResponse = struct {
    access_token: []const u8,
    refresh_token: []const u8,
    expires_in: i64,
};

fn getObjectStringField(obj: *const std.json.ObjectMap, key: []const u8) ?[]const u8 {
    if (obj.get(key)) |value| {
        if (value == .string) return value.string;
    }
    return null;
}

fn getObjectI64Field(obj: *const std.json.ObjectMap, key: []const u8) ?i64 {
    if (obj.get(key)) |value| {
        return switch (value) {
            .integer => value.integer,
            .float => @intFromFloat(value.float),
            else => null,
        };
    }
    return null;
}

fn parseTokenResponse(response_body: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, response_body, .{}) catch {
        std.debug.print("Failed to parse Anthropic token response JSON; response body redacted ({d} bytes)\n", .{response_body.len});
        return error.ParseError;
    };
    defer parsed.deinit();

    if (parsed.value != .object) {
        std.debug.print("Anthropic token response is not an object; response body redacted ({d} bytes)\n", .{response_body.len});
        return error.ParseError;
    }

    const obj = &parsed.value.object;
    if (getObjectStringField(obj, "error")) |err| {
        std.debug.print("OAuth error: {s}", .{err});
        if (getObjectStringField(obj, "error_description")) |desc| {
            std.debug.print(" - {s}", .{desc});
        }
        std.debug.print("\n", .{});
        return error.OAuthFailed;
    }

    const access_token = getObjectStringField(obj, "access_token") orelse
        getObjectStringField(obj, "accessToken") orelse {
        std.debug.print("Anthropic token response missing access_token; response body redacted ({d} bytes)\n", .{response_body.len});
        return error.ParseError;
    };
    const refresh_token = getObjectStringField(obj, "refresh_token") orelse
        getObjectStringField(obj, "refreshToken") orelse access_token;

    var expires_in = getObjectI64Field(obj, "expires_in") orelse
        getObjectI64Field(obj, "expiresIn") orelse 3600;
    if (expires_in <= 0) {
        if (getObjectI64Field(obj, "expires_at")) |expires_at| {
            const now_seconds = compat.time.nowSeconds();
            if (expires_at > now_seconds) expires_in = expires_at - now_seconds else expires_in = 3600;
        } else {
            expires_in = 3600;
        }
    }

    return .{
        .access_token = try allocator.dupe(u8, access_token),
        .refresh_token = try allocator.dupe(u8, refresh_token),
        .expires_in = expires_in,
    };
}

fn exchangeCode(code: []const u8, state: []const u8, verifier: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    const body = try std.json.Stringify.valueAlloc(allocator, .{
        .grant_type = "authorization_code",
        .client_id = client_id,
        .code = code,
        .state = state,
        .redirect_uri = redirect_uri,
        .code_verifier = verifier,
    }, .{});
    defer allocator.free(body);

    return try exchangeTokens(body, allocator);
}

fn exchangeTokens(body: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    return exchangeTokensWith(http.fetch, body, allocator);
}

fn exchangeTokensWith(fetch: Fetch, body: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    try headers.append(allocator, .{ .name = "accept", .value = "application/json" });
    try headers.append(allocator, .{ .name = "content-type", .value = "application/json" });

    var fetched = fetch(allocator, token_url, .{
        .method = .POST,
        .extra_headers = headers.items,
        .body = body,
        .accept_encoding = "identity",
        .max_response_bytes = 8192,
        .timeout_ms = oauth_request_timeout_ms,
    }) catch |err| return if (err == error.OutOfMemory) error.OutOfMemory else error.OAuthFailed;
    defer fetched.deinit(allocator);

    if (fetched.status != 200) return error.OAuthFailed;

    return try parseTokenResponse(fetched.body, allocator);
}

test "parseAuthFromManualInput - hash fragment with state" {
    const input = "https://console.anthropic.com/oauth/code/callback#code=abc123&state=xyz";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("abc123", auth.code);
    try std.testing.expectEqualStrings("xyz", auth.state);
}

test "parseAuthFromManualInput - query parameter with state" {
    const input = "https://console.anthropic.com/oauth/code/callback?code=def456&state=xyz";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("def456", auth.code);
    try std.testing.expectEqualStrings("xyz", auth.state);
}

test "parseAuthFromManualInput - raw code#state format" {
    const input = "ghi789#mystate";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("ghi789", auth.code);
    try std.testing.expectEqualStrings("mystate", auth.state);
}

test "parseAuthFromManualInput - raw code only" {
    const input = "ghi789";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("ghi789", auth.code);
    try std.testing.expectEqualStrings("", auth.state);
}

test "getApiKey - returns access token" {
    const credentials = Credentials{
        .refresh = "refresh_token",
        .access = "access_token",
        .expires = compat.time.nowMillis() + 3600000,
    };

    const api_key = try getApiKey(credentials, std.testing.allocator);
    defer std.testing.allocator.free(api_key);

    try std.testing.expectEqualStrings("access_token", api_key);
}

test "parseTokenResponse handles missing refresh and expires" {
    const payload =
        \\{"access_token":"a-token"}
    ;
    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer std.testing.allocator.free(response.access_token);
    defer std.testing.allocator.free(response.refresh_token);

    try std.testing.expectEqualStrings("a-token", response.access_token);
    try std.testing.expectEqualStrings("a-token", response.refresh_token);
    try std.testing.expect(response.expires_in > 0);
}

test "buildAuthUrl includes code=true" {
    const url = try buildAuthUrl(std.testing.allocator, "challenge-value", "state-value");
    defer std.testing.allocator.free(url);

    try std.testing.expect(std.mem.find(u8, url, "code=true") != null);
}

test "parseTokenResponse handles camelCase token fields" {
    const payload =
        \\{"accessToken":"camel-access","refreshToken":"camel-refresh","expiresIn":1800}
    ;
    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer std.testing.allocator.free(response.access_token);
    defer std.testing.allocator.free(response.refresh_token);

    try std.testing.expectEqualStrings("camel-access", response.access_token);
    try std.testing.expectEqualStrings("camel-refresh", response.refresh_token);
    try std.testing.expectEqual(@as(i64, 1800), response.expires_in);
}

test "parseTokenResponse maps oauth error payload to OAuthFailed" {
    const payload =
        \\{"error":"invalid_grant","error_description":"Invalid 'code' in request."}
    ;
    try std.testing.expectError(error.OAuthFailed, parseTokenResponse(payload, std.testing.allocator));
}

const FakeClaudeBrowser = struct {
    var shown_url: [1024]u8 = undefined;
    var shown_len: usize = 0;
    var thread: ?std.Thread = null;
    var exchange_body: std.ArrayList(u8) = .empty;
    var exchanges: usize = 0;

    fn param(url: []const u8, key: []const u8) []const u8 {
        const start = (std.mem.indexOf(u8, url, key) orelse return "") + key.len;
        const end = std.mem.indexOfScalarPos(u8, url, start, '&') orelse url.len;
        return url[start..end];
    }

    fn approve() void {
        const url = shown_url[0..shown_len];
        const redirect = param(url, "redirect_uri=http%3A%2F%2Flocalhost%3A");
        const port_end = std.mem.indexOf(u8, redirect, "%2F") orelse return;
        const port = std.fmt.parseInt(u16, redirect[0..port_end], 10) catch return;
        var stream = compat.net.tcpConnectHost(std.heap.page_allocator, "127.0.0.1", port) catch return;
        defer stream.close();
        var request: [512]u8 = undefined;
        const line = std.fmt.bufPrint(&request, "GET /callback?code=claude-code-1&state={s} HTTP/1.1\r\n\r\n", .{param(url, "&state=")}) catch return;
        stream.writeAll(line) catch return;
        var sink: [256]u8 = undefined;
        while (compat.net.readableWithin(compat.net.streamHandle(&stream), 3_000) catch false) {
            if ((stream.readSome(&sink) catch 0) == 0) break;
        }
    }

    fn onAuth(info: AuthInfo) void {
        @memcpy(shown_url[0..info.url.len], info.url);
        shown_len = info.url.len;
        thread = std.Thread.spawn(.{}, approve, .{}) catch null;
    }

    fn isCancelled() bool {
        return false;
    }

    fn fetch(allocator: std.mem.Allocator, url: []const u8, options: http.FetchOptions) http.FetchError!http.Fetched {
        if (!std.mem.eql(u8, url, token_url)) return error.RequestFailed;
        exchanges += 1;
        exchange_body.appendSlice(std.testing.allocator, options.body orelse "") catch return error.OutOfMemory;
        return .{ .status = 200, .body = try allocator.dupe(u8, "{\"access_token\":\"sk-ant-oat01-a\",\"refresh_token\":\"r\",\"expires_in\":3600}") };
    }
};

test "Anthropic loopback login sends the browser to a localhost callback and exchanges the code that comes back with the same redirect and state" {
    if (!loopback_supported) return error.SkipZigTest;
    defer FakeClaudeBrowser.exchange_body.clearAndFree(std.testing.allocator);
    const credentials = try loginWithLoopback(.{
        .onAuth = FakeClaudeBrowser.onAuth,
        .isCancelled = FakeClaudeBrowser.isCancelled,
        .fetch = FakeClaudeBrowser.fetch,
        .wait_ms = 5_000,
    }, std.testing.allocator);
    defer std.testing.allocator.free(credentials.refresh);
    defer std.testing.allocator.free(credentials.access);
    if (FakeClaudeBrowser.thread) |thread| thread.join();

    const url = FakeClaudeBrowser.shown_url[0..FakeClaudeBrowser.shown_len];
    try std.testing.expect(std.mem.startsWith(u8, url, "https://claude.ai/oauth/authorize?code=true&client_id=9d1c250a-e61b-44d9-88ed-5944d1962f5e&redirect_uri=http%3A%2F%2Flocalhost%3A"));
    const state = FakeClaudeBrowser.param(url, "&state=");
    try std.testing.expect(state.len >= 43);
    try std.testing.expect(std.mem.indexOf(u8, url, "code_challenge_method=S256") != null);

    var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, FakeClaudeBrowser.exchange_body.items, .{});
    defer parsed.deinit();
    const body = parsed.value.object;
    try std.testing.expectEqualStrings("claude-code-1", body.get("code").?.string);
    try std.testing.expectEqualStrings(state, body.get("state").?.string);
    const port = FakeClaudeBrowser.param(url, "redirect_uri=http%3A%2F%2Flocalhost%3A");
    const expected_redirect = try std.fmt.allocPrint(std.testing.allocator, "http://localhost:{s}/callback", .{port[0 .. std.mem.indexOf(u8, port, "%2F") orelse port.len]});
    defer std.testing.allocator.free(expected_redirect);
    try std.testing.expectEqualStrings(expected_redirect, body.get("redirect_uri").?.string);
    try std.testing.expect(!std.mem.eql(u8, state, body.get("code_verifier").?.string));
    try std.testing.expectEqualStrings("sk-ant-oat01-a", credentials.access);
}
