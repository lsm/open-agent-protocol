const std = @import("std");
const compat = @import("compat");
const http = compat.http;
const pkce_mod = @import("oauth/pkce");
const loopback = @import("oauth/loopback");

const client_id = "app_EMoamEEZ73f0CkXaXp7hrann";
const redirect_uri = "http://localhost:1455/auth/callback";
pub const browser_callback_port: u16 = 1455;
const browser_callback_path = "/auth/callback";
const scopes = "openid profile email offline_access api.connectors.read api.connectors.invoke";
const originator = "codex_cli_rs";
const auth_url_base = "https://auth.openai.com/oauth/authorize";
pub const oauth_request_timeout_ms: u64 = 30_000;

const token_url = "https://auth.openai.com/oauth/token";
const device_user_code_url = "https://auth.openai.com/api/accounts/deviceauth/usercode";
const device_token_url = "https://auth.openai.com/api/accounts/deviceauth/token";
const device_verification_url = "https://auth.openai.com/codex/device";
const device_redirect_uri = "https://auth.openai.com/deviceauth/callback";
const device_login_window_ms: i64 = 15 * 60 * 1000;
const device_cancel_check_ms: u64 = 100;

pub const Credentials = struct {
    refresh: []const u8,
    access: []const u8,
    expires: i64,
    provider_data: ?[]const u8 = null,
};

pub const Callbacks = struct {
    onAuth: *const fn (info: AuthInfo) void,
    onPrompt: *const fn (prompt: Prompt) []const u8,
};

pub const AuthInfo = struct {
    url: []const u8,
    instructions: ?[]const u8 = null,
};

pub const Prompt = struct {
    message: []const u8,
    allow_empty: bool = false,
};

pub const Fetch = *const fn (allocator: std.mem.Allocator, url: []const u8, options: http.FetchOptions) http.FetchError!http.Fetched;

pub const DeviceCallbacks = struct {
    onAuth: *const fn (info: AuthInfo) void,
    isCancelled: *const fn () bool,
    fetch: Fetch = http.fetch,
    browser_port: ?u16 = browser_callback_port,
    wait_ms: i64 = device_login_window_ms,
};

fn isUnreservedUrlByte(byte: u8) bool {
    return (byte >= 'A' and byte <= 'Z') or
        (byte >= 'a' and byte <= 'z') or
        (byte >= '0' and byte <= '9') or
        byte == '-' or byte == '_' or byte == '.' or byte == '~';
}

fn appendUrlEncoded(allocator: std.mem.Allocator, buf: *std.ArrayList(u8), value: []const u8) !void {
    const hex = "0123456789ABCDEF";
    for (value) |byte| {
        if (isUnreservedUrlByte(byte)) {
            try buf.append(allocator, byte);
        } else {
            try buf.append(allocator, '%');
            try buf.append(allocator, hex[byte >> 4]);
            try buf.append(allocator, hex[byte & 0x0f]);
        }
    }
}

fn appendQueryParam(
    allocator: std.mem.Allocator,
    buf: *std.ArrayList(u8),
    first: *bool,
    key: []const u8,
    value: []const u8,
) !void {
    try buf.append(allocator, if (first.*) '?' else '&');
    first.* = false;
    try buf.appendSlice(allocator, key);
    try buf.append(allocator, '=');
    try appendUrlEncoded(allocator, buf, value);
}

fn appendFormParam(
    allocator: std.mem.Allocator,
    buf: *std.ArrayList(u8),
    first: *bool,
    key: []const u8,
    value: []const u8,
) !void {
    if (!first.*) try buf.append(allocator, '&');
    first.* = false;
    try buf.appendSlice(allocator, key);
    try buf.append(allocator, '=');
    try appendUrlEncoded(allocator, buf, value);
}

fn formBody(allocator: std.mem.Allocator, params: []const struct { []const u8, []const u8 }) ![]u8 {
    var body = std.ArrayList(u8).empty;
    errdefer body.deinit(allocator);

    var first = true;
    for (params) |param| {
        try appendFormParam(allocator, &body, &first, param[0], param[1]);
    }
    return try body.toOwnedSlice(allocator);
}

fn fromHexDigit(byte: u8) ?u8 {
    return switch (byte) {
        '0'...'9' => byte - '0',
        'a'...'f' => byte - 'a' + 10,
        'A'...'F' => byte - 'A' + 10,
        else => null,
    };
}

fn urlDecode(allocator: std.mem.Allocator, value: []const u8) ![]u8 {
    var decoded = std.ArrayList(u8).empty;
    errdefer decoded.deinit(allocator);

    var idx: usize = 0;
    while (idx < value.len) : (idx += 1) {
        const byte = value[idx];
        if (byte == '+') {
            try decoded.append(allocator, ' ');
        } else if (byte == '%' and idx + 2 < value.len) {
            const hi = fromHexDigit(value[idx + 1]);
            const lo = fromHexDigit(value[idx + 2]);
            if (hi != null and lo != null) {
                try decoded.append(allocator, (hi.? << 4) | lo.?);
                idx += 2;
            } else {
                try decoded.append(allocator, byte);
            }
        } else {
            try decoded.append(allocator, byte);
        }
    }

    return try decoded.toOwnedSlice(allocator);
}

fn generateState(allocator: std.mem.Allocator) ![]u8 {
    return generateStateWithRandom(allocator, compat.random.fillSecureBytes);
}

fn generateStateWithRandom(allocator: std.mem.Allocator, fill_random: fn ([]u8) void) ![]u8 {
    var random_bytes: [32]u8 = undefined;
    fill_random(&random_bytes);

    const encoder = std.base64.url_safe_no_pad.Encoder;
    const state = try allocator.alloc(u8, encoder.calcSize(random_bytes.len));
    _ = encoder.encode(state, &random_bytes);
    return state;
}

fn buildAuthUrl(allocator: std.mem.Allocator, challenge: []const u8, state: []const u8, callback_uri: []const u8) ![]u8 {
    var url = std.ArrayList(u8).empty;
    errdefer url.deinit(allocator);

    try url.appendSlice(allocator, auth_url_base);

    var first = true;
    try appendQueryParam(allocator, &url, &first, "response_type", "code");
    try appendQueryParam(allocator, &url, &first, "client_id", client_id);
    try appendQueryParam(allocator, &url, &first, "redirect_uri", callback_uri);
    try appendQueryParam(allocator, &url, &first, "scope", scopes);
    try appendQueryParam(allocator, &url, &first, "code_challenge", challenge);
    try appendQueryParam(allocator, &url, &first, "code_challenge_method", "S256");
    try appendQueryParam(allocator, &url, &first, "id_token_add_organizations", "true");
    try appendQueryParam(allocator, &url, &first, "codex_cli_simplified_flow", "true");
    try appendQueryParam(allocator, &url, &first, "state", state);
    try appendQueryParam(allocator, &url, &first, "originator", originator);

    return try url.toOwnedSlice(allocator);
}

pub fn login(callbacks: Callbacks, allocator: std.mem.Allocator) !Credentials {
    const pkce = try pkce_mod.generate(allocator);
    defer pkce.deinit(allocator);

    const state = try generateState(allocator);
    defer allocator.free(state);

    const auth_url = try buildAuthUrl(allocator, pkce.challenge, state, redirect_uri);
    defer allocator.free(auth_url);

    callbacks.onAuth(.{
        .url = auth_url,
        .instructions = "Authorize, then paste the full redirect URL (or just the code) below:",
    });

    const manual_input = callbacks.onPrompt(.{ .message = "Enter code:" });
    defer allocator.free(manual_input);

    const parsed_auth = try parseAuthFromManualInput(allocator, manual_input);
    defer allocator.free(parsed_auth.code);
    defer allocator.free(parsed_auth.state);

    if (parsed_auth.state.len > 0 and !std.mem.eql(u8, parsed_auth.state, state)) {
        return error.OAuthStateMismatch;
    }

    const token_response = try exchangeCode(http.fetch, parsed_auth.code, pkce.verifier, redirect_uri, allocator);
    defer deinitTokenResponse(allocator, token_response);

    const refresh_token = token_response.refresh_token orelse return error.ParseError;

    const expires = compat.time.nowMillis() + (token_response.expires_in * 1000) - (5 * 60 * 1000);

    return try buildCredentials(allocator, refresh_token, token_response.access_token, expires, token_response.provider_data);
}

pub fn loginWithBrowserOrDeviceCode(callbacks: DeviceCallbacks, allocator: std.mem.Allocator) !Credentials {
    var listener: ?loopback.Listener = null;
    if (callbacks.browser_port) |port| {
        if (loopback.supported) listener = loopback.Listener.openAt(port, browser_callback_path) catch null;
    }
    defer if (listener) |*held| held.close();

    const device: ?DeviceCode = requestUserCode(callbacks.fetch, allocator) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => if (listener == null) return err else null,
    };
    defer if (device) |held| held.deinit(allocator);

    const pkce = try pkce_mod.generate(allocator);
    defer pkce.deinit(allocator);
    const state = try generateState(allocator);
    defer allocator.free(state);
    const callback_uri: ?[]u8 = if (listener) |*held| try held.redirectUri(allocator) else null;
    defer if (callback_uri) |uri| allocator.free(uri);

    try showLogin(callbacks, allocator, pkce.challenge, state, callback_uri, device);

    const poll_body: ?[]u8 = if (device) |held| try std.json.Stringify.valueAlloc(allocator, .{ .device_auth_id = held.device_auth_id, .user_code = held.user_code }, .{}) else null;
    defer if (poll_body) |body| allocator.free(body);
    var polling = device != null;
    var next_poll = compat.time.nowMillis();
    const deadline = next_poll + callbacks.wait_ms;
    while (true) {
        if (callbacks.isCancelled()) return error.AuthFlowCancelled;
        if (compat.time.nowMillis() >= deadline) return error.LoginTimedOut;
        if (listener) |*held| {
            if (try held.take(allocator, state, callbacks.isCancelled)) |code| {
                defer allocator.free(code);
                const token_response = try exchangeCode(callbacks.fetch, code, pkce.verifier, callback_uri.?, allocator);
                return credentialsFrom(token_response, allocator);
            }
        } else {
            compat.time.sleepMs(device_cancel_check_ms);
        }
        if (!polling or compat.time.nowMillis() < next_poll) continue;
        var fetched = try postJson(callbacks.fetch, allocator, device_token_url, poll_body.?);
        defer fetched.deinit(allocator);
        if (fetched.status == 200) {
            const grant = try parseDeviceGrant(allocator, fetched.body);
            defer grant.deinit(allocator);
            const token_response = try exchangeCode(callbacks.fetch, grant.authorization_code, grant.code_verifier, device_redirect_uri, allocator);
            return credentialsFrom(token_response, allocator);
        }
        if (fetched.status == 403 or fetched.status == 404) {
            next_poll = compat.time.nowMillis() + @as(i64, @intCast(device.?.interval_ms));
        } else if (listener != null) {
            polling = false;
        } else {
            return error.OAuthFailed;
        }
    }
}

fn showLogin(callbacks: DeviceCallbacks, allocator: std.mem.Allocator, challenge: []const u8, state: []const u8, callback_uri: ?[]const u8, device: ?DeviceCode) !void {
    const uri = callback_uri orelse {
        const instructions = try std.fmt.allocPrint(allocator, "Enter code: {s}", .{device.?.user_code});
        defer allocator.free(instructions);
        callbacks.onAuth(.{ .url = device_verification_url, .instructions = instructions });
        return;
    };
    const auth_url = try buildAuthUrl(allocator, challenge, state, uri);
    defer allocator.free(auth_url);
    const instructions = if (device) |held|
        try std.fmt.allocPrint(allocator, "Open this URL in a browser on this computer. On another device, open " ++ device_verification_url ++ " and enter code {s} instead.", .{held.user_code})
    else
        try allocator.dupe(u8, "Open this URL in a browser on this computer; the login finishes on its own.");
    defer allocator.free(instructions);
    callbacks.onAuth(.{ .url = auth_url, .instructions = instructions });
}

fn credentialsFrom(token_response: TokenResponse, allocator: std.mem.Allocator) !Credentials {
    defer deinitTokenResponse(allocator, token_response);
    const refresh_token = token_response.refresh_token orelse return error.ParseError;
    const expires = compat.time.nowMillis() + (token_response.expires_in * 1000) - (5 * 60 * 1000);
    return try buildCredentials(allocator, refresh_token, token_response.access_token, expires, token_response.provider_data);
}

const DeviceCode = struct {
    device_auth_id: []const u8,
    user_code: []const u8,
    interval_ms: u64,

    fn deinit(self: DeviceCode, allocator: std.mem.Allocator) void {
        allocator.free(self.device_auth_id);
        allocator.free(self.user_code);
    }
};

const DeviceGrant = struct {
    authorization_code: []const u8,
    code_verifier: []const u8,

    fn deinit(self: DeviceGrant, allocator: std.mem.Allocator) void {
        allocator.free(self.authorization_code);
        allocator.free(self.code_verifier);
    }
};

fn postJson(fetch: Fetch, allocator: std.mem.Allocator, url: []const u8, body: []const u8) !http.Fetched {
    const headers = [_]std.http.Header{
        .{ .name = "accept", .value = "application/json" },
        .{ .name = "content-type", .value = "application/json" },
    };
    return fetch(allocator, url, .{
        .method = .POST,
        .extra_headers = &headers,
        .body = body,
        .accept_encoding = "identity",
        .max_response_bytes = 8192,
        .timeout_ms = oauth_request_timeout_ms,
    }) catch |err| switch (err) {
        error.OutOfMemory => error.OutOfMemory,
        else => error.OAuthFailed,
    };
}

fn requestUserCode(fetch: Fetch, allocator: std.mem.Allocator) !DeviceCode {
    const body = try std.json.Stringify.valueAlloc(allocator, .{ .client_id = client_id }, .{});
    defer allocator.free(body);
    var fetched = try postJson(fetch, allocator, device_user_code_url, body);
    defer fetched.deinit(allocator);
    if (fetched.status == 404) return error.DeviceCodeLoginUnavailable;
    if (fetched.status != 200) return error.OAuthFailed;
    return try parseUserCodeResponse(allocator, fetched.body);
}

fn parseUserCodeResponse(allocator: std.mem.Allocator, body: []const u8) !DeviceCode {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, body, .{}) catch |err| return if (err == error.OutOfMemory) error.OutOfMemory else error.ParseError;
    defer parsed.deinit();
    if (parsed.value != .object) return error.ParseError;
    const obj = &parsed.value.object;
    const id = getObjectStringField(obj, "device_auth_id") orelse return error.ParseError;
    const code = getObjectStringField(obj, "user_code") orelse getObjectStringField(obj, "usercode") orelse return error.ParseError;
    if (id.len == 0 or code.len == 0) return error.ParseError;
    const interval_s: u64 = if (obj.get("interval")) |value| switch (value) {
        .integer => |n| if (n >= 0) @intCast(n) else return error.ParseError,
        .string => |text| std.fmt.parseInt(u64, std.mem.trim(u8, text, " "), 10) catch return error.ParseError,
        else => return error.ParseError,
    } else 5;
    const device_auth_id = try allocator.dupe(u8, id);
    errdefer allocator.free(device_auth_id);
    const user_code = try allocator.dupe(u8, code);
    return .{
        .device_auth_id = device_auth_id,
        .user_code = user_code,
        .interval_ms = std.math.mul(u64, interval_s, 1000) catch return error.ParseError,
    };
}

fn parseDeviceGrant(allocator: std.mem.Allocator, body: []const u8) !DeviceGrant {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, body, .{}) catch |err| return if (err == error.OutOfMemory) error.OutOfMemory else error.ParseError;
    defer parsed.deinit();
    if (parsed.value != .object) return error.ParseError;
    const obj = &parsed.value.object;
    const code = getObjectStringField(obj, "authorization_code") orelse return error.ParseError;
    const verifier = getObjectStringField(obj, "code_verifier") orelse return error.ParseError;
    if (code.len == 0 or verifier.len == 0) return error.ParseError;
    const authorization_code = try allocator.dupe(u8, code);
    errdefer allocator.free(authorization_code);
    const code_verifier = try allocator.dupe(u8, verifier);
    return .{ .authorization_code = authorization_code, .code_verifier = code_verifier };
}

pub fn refreshToken(credentials: Credentials, allocator: std.mem.Allocator) !Credentials {
    const body = try std.json.Stringify.valueAlloc(allocator, .{
        .client_id = client_id,
        .grant_type = "refresh_token",
        .refresh_token = credentials.refresh,
    }, .{});
    defer allocator.free(body);

    const token_response = try exchangeTokens(body, "application/json", allocator);
    defer deinitTokenResponse(allocator, token_response);

    const expires = compat.time.nowMillis() + (token_response.expires_in * 1000) - (5 * 60 * 1000);
    return try credentialsFromRefreshResponse(credentials, token_response, expires, allocator);
}

pub fn getApiKey(credentials: Credentials, allocator: std.mem.Allocator) ![]const u8 {
    return try allocator.dupe(u8, credentials.access);
}

const ParsedAuth = struct {
    code: []const u8,
    state: []const u8,
};

fn parseAuthFromManualInput(allocator: std.mem.Allocator, input: []const u8) !ParsedAuth {
    const trimmed = std.mem.trim(u8, input, " \t\r\n");

    if (trimmed.len == 0) return error.OAuthCancelled;

    if (std.mem.find(u8, trimmed, "?code=") orelse std.mem.find(u8, trimmed, "&code=")) |idx| {
        const code_start = idx + 6;
        var code_end = trimmed.len;
        if (std.mem.findAny(u8, trimmed[code_start..], "#&")) |end| {
            code_end = code_start + end;
        }
        const code = try urlDecode(allocator, trimmed[code_start..code_end]);
        errdefer allocator.free(code);

        var state: []const u8 = "";
        if (std.mem.find(u8, trimmed, "state=")) |state_idx| {
            const state_start = state_idx + 6;
            var state_end = trimmed.len;
            if (std.mem.findAny(u8, trimmed[state_start..], "#&")) |end| {
                state_end = state_start + end;
            }
            state = trimmed[state_start..state_end];
        }
        return .{ .code = code, .state = try urlDecode(allocator, state) };
    }

    if (std.mem.find(u8, trimmed, "#")) |hash_idx| {
        const code = try allocator.dupe(u8, trimmed[0..hash_idx]);
        errdefer allocator.free(code);
        return .{ .code = code, .state = try allocator.dupe(u8, trimmed[hash_idx + 1 ..]) };
    }

    return .{
        .code = try allocator.dupe(u8, trimmed),
        .state = try allocator.dupe(u8, ""),
    };
}

const TokenResponse = struct {
    access_token: []const u8,
    refresh_token: ?[]const u8,
    provider_data: ?[]const u8 = null,
    expires_in: i64,
};

fn deinitTokenResponse(allocator: std.mem.Allocator, response: TokenResponse) void {
    allocator.free(response.access_token);
    if (response.refresh_token) |refresh| allocator.free(refresh);
    if (response.provider_data) |data| allocator.free(data);
}

fn buildCredentials(
    allocator: std.mem.Allocator,
    refresh_token: []const u8,
    access_token: []const u8,
    expires: i64,
    provider_data: ?[]const u8,
) !Credentials {
    const refresh = try allocator.dupe(u8, refresh_token);
    errdefer allocator.free(refresh);
    const access = try allocator.dupe(u8, access_token);
    errdefer allocator.free(access);
    const data = if (provider_data) |value| try allocator.dupe(u8, value) else null;
    errdefer if (data) |value| allocator.free(value);

    return .{
        .refresh = refresh,
        .access = access,
        .expires = expires,
        .provider_data = data,
    };
}

fn credentialsFromRefreshResponse(credentials: Credentials, token_response: TokenResponse, expires: i64, allocator: std.mem.Allocator) !Credentials {
    const refresh_token = token_response.refresh_token orelse credentials.refresh;
    const provider_data = token_response.provider_data orelse credentials.provider_data;
    return try buildCredentials(allocator, refresh_token, token_response.access_token, expires, provider_data);
}

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
            .float => |float| {
                if (!std.math.isFinite(float)) return null;
                if (float < @as(f64, @floatFromInt(std.math.minInt(i64))) or
                    float > @as(f64, @floatFromInt(std.math.maxInt(i64))))
                {
                    return null;
                }
                return @intFromFloat(float);
            },
            else => null,
        };
    }
    return null;
}

fn getObjectAccountId(obj: *const std.json.ObjectMap) ?[]const u8 {
    const account_id = getObjectStringField(obj, "account_id") orelse return null;
    if (account_id.len == 0) return null;
    return account_id;
}

fn getJsonAccountId(value: std.json.Value) ?[]const u8 {
    return switch (value) {
        .object => |obj| {
            if (getObjectAccountId(&obj)) |account_id| return account_id;
            var iter = obj.iterator();
            while (iter.next()) |entry| {
                if (getJsonAccountId(entry.value_ptr.*)) |account_id| return account_id;
            }
            return null;
        },
        .array => |array| {
            for (array.items) |item| {
                if (getJsonAccountId(item)) |account_id| return account_id;
            }
            return null;
        },
        else => null,
    };
}

fn accountIdFromJwt(allocator: std.mem.Allocator, token: []const u8) !?[]u8 {
    const first_dot = std.mem.indexOfScalar(u8, token, '.') orelse return null;
    const payload_start = first_dot + 1;
    const second_rel = std.mem.indexOfScalar(u8, token[payload_start..], '.') orelse return null;
    const payload = token[payload_start .. payload_start + second_rel];

    const decoded_len = std.base64.url_safe_no_pad.Decoder.calcSizeForSlice(payload) catch return null;
    const decoded = try allocator.alloc(u8, decoded_len);
    defer allocator.free(decoded);
    std.base64.url_safe_no_pad.Decoder.decode(decoded, payload) catch return null;

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, decoded, .{}) catch return null;
    defer parsed.deinit();
    const account_id = getJsonAccountId(parsed.value) orelse return null;
    return try allocator.dupe(u8, account_id);
}

fn buildProviderData(allocator: std.mem.Allocator, account_id: []const u8) ![]u8 {
    return try std.json.Stringify.valueAlloc(allocator, .{ .source = "openai-codex", .account_id = account_id }, .{});
}

fn providerDataFromTokenObject(allocator: std.mem.Allocator, obj: *const std.json.ObjectMap) !?[]u8 {
    if (getObjectAccountId(obj)) |account_id| return try buildProviderData(allocator, account_id);

    if (getObjectStringField(obj, "id_token")) |id_token| {
        const account_id = (try accountIdFromJwt(allocator, id_token)) orelse return null;
        defer allocator.free(account_id);
        return try buildProviderData(allocator, account_id);
    }

    return null;
}

fn parseTokenResponse(response_body: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, response_body, .{}) catch |err| {
        if (err == error.OutOfMemory) return error.OutOfMemory;
        std.debug.print("Failed to parse Codex token response JSON; response body redacted ({d} bytes)\n", .{response_body.len});
        return error.ParseError;
    };
    defer parsed.deinit();

    if (parsed.value != .object) return error.ParseError;

    const obj = &parsed.value.object;
    if (getObjectStringField(obj, "error")) |err| {
        std.debug.print("Codex OAuth error: {s}", .{err});
        if (getObjectStringField(obj, "error_description")) |desc| {
            std.debug.print(" - {s}", .{desc});
        }
        std.debug.print("\n", .{});
        return error.OAuthFailed;
    }

    const access_token = getObjectStringField(obj, "access_token") orelse {
        std.debug.print("Codex token response missing access_token; response body redacted ({d} bytes)\n", .{response_body.len});
        return error.ParseError;
    };
    const refresh_token = if (getObjectStringField(obj, "refresh_token")) |refresh|
        try allocator.dupe(u8, refresh)
    else
        null;
    errdefer if (refresh_token) |refresh| allocator.free(refresh);
    const provider_data = try providerDataFromTokenObject(allocator, obj);
    errdefer if (provider_data) |data| allocator.free(data);

    var expires_in = getObjectI64Field(obj, "expires_in") orelse 3600;
    if (expires_in <= 0) expires_in = 3600;

    return .{
        .access_token = try allocator.dupe(u8, access_token),
        .refresh_token = refresh_token,
        .provider_data = provider_data,
        .expires_in = expires_in,
    };
}

fn exchangeCode(fetch: Fetch, code: []const u8, verifier: []const u8, callback_uri: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    const body = try formBody(allocator, &.{
        .{ "grant_type", "authorization_code" },
        .{ "code", code },
        .{ "redirect_uri", callback_uri },
        .{ "client_id", client_id },
        .{ "code_verifier", verifier },
    });
    defer allocator.free(body);

    return try exchangeTokensWith(fetch, body, "application/x-www-form-urlencoded", allocator);
}

fn exchangeTokens(body: []const u8, content_type: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    return exchangeTokensWith(http.fetch, body, content_type, allocator);
}

fn exchangeTokensWith(fetch: Fetch, body: []const u8, content_type: []const u8, allocator: std.mem.Allocator) !TokenResponse {
    var headers: std.ArrayList(std.http.Header) = .empty;
    defer headers.deinit(allocator);
    try headers.append(allocator, .{ .name = "accept", .value = "application/json" });
    try headers.append(allocator, .{ .name = "content-type", .value = content_type });

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

test "buildAuthUrl includes client_id and PKCE challenge" {
    const url = try buildAuthUrl(std.testing.allocator, "challenge-value", "state-value", redirect_uri);
    defer std.testing.allocator.free(url);

    try std.testing.expect(std.mem.startsWith(u8, url, "https://auth.openai.com/oauth/authorize?response_type=code"));
    try std.testing.expect(std.mem.find(u8, url, "client_id=app_") != null);
    try std.testing.expect(std.mem.find(u8, url, "redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback") != null);
    try std.testing.expect(std.mem.find(u8, url, "scope=openid%20profile%20email%20offline_access%20api.connectors.read%20api.connectors.invoke") != null);
    try std.testing.expect(std.mem.find(u8, url, "code_challenge=challenge-value") != null);
    try std.testing.expect(std.mem.find(u8, url, "code_challenge_method=S256") != null);
    try std.testing.expect(std.mem.find(u8, url, "id_token_add_organizations=true") != null);
    try std.testing.expect(std.mem.find(u8, url, "codex_cli_simplified_flow=true") != null);
    try std.testing.expect(std.mem.find(u8, url, "state=state-value") != null);
    try std.testing.expect(std.mem.find(u8, url, "originator=codex_cli_rs") != null);
    try std.testing.expect(std.mem.find(u8, url, "audience=") == null);
}

test "formBody percent encodes token exchange fields" {
    const body = try formBody(std.testing.allocator, &.{
        .{ "grant_type", "authorization_code" },
        .{ "code", "abc/def ghi" },
        .{ "redirect_uri", redirect_uri },
    });
    defer std.testing.allocator.free(body);

    try std.testing.expectEqualStrings(
        "grant_type=authorization_code&code=abc%2Fdef%20ghi&redirect_uri=http%3A%2F%2Flocalhost%3A1455%2Fauth%2Fcallback",
        body,
    );
}

test "parseAuthFromManualInput - redirect url with code and state" {
    const input = "http://localhost:1455/auth/callback?code=abc%2F123&state=xy%7A";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("abc/123", auth.code);
    try std.testing.expectEqualStrings("xyz", auth.state);
}

test "parseAuthFromManualInput - raw code only" {
    const input = "just-a-code";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("just-a-code", auth.code);
    try std.testing.expectEqualStrings("", auth.state);
}

test "parseAuthFromManualInput - trims surrounding whitespace" {
    const input = "  code123\n";
    const auth = try parseAuthFromManualInput(std.testing.allocator, input);
    defer std.testing.allocator.free(auth.code);
    defer std.testing.allocator.free(auth.state);

    try std.testing.expectEqualStrings("code123", auth.code);
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

test "parseTokenResponse extracts tokens" {
    const payload =
        \\{"access_token":"acc","refresh_token":"ref","expires_in":1800}
    ;
    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer deinitTokenResponse(std.testing.allocator, response);

    try std.testing.expectEqualStrings("acc", response.access_token);
    try std.testing.expectEqualStrings("ref", response.refresh_token.?);
    try std.testing.expectEqual(@as(i64, 1800), response.expires_in);
}

test "parseTokenResponse defaults malformed expires_in floats" {
    const payload =
        \\{"access_token":"acc","refresh_token":"ref","expires_in":1e400}
    ;
    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer deinitTokenResponse(std.testing.allocator, response);

    try std.testing.expectEqual(@as(i64, 3600), response.expires_in);
}

test "parseTokenResponse extracts account metadata from account_id" {
    const payload =
        \\{"access_token":"acc","refresh_token":"ref","account_id":"account-123"}
    ;
    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer deinitTokenResponse(std.testing.allocator, response);

    try std.testing.expect(response.provider_data != null);
    try std.testing.expect(std.mem.indexOf(u8, response.provider_data.?, "\"source\":\"openai-codex\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, response.provider_data.?, "\"account_id\":\"account-123\"") != null);
}

test "parseTokenResponse extracts account metadata from id_token" {
    const id_token = "e30.eyJodHRwczovL2FwaS5vcGVuYWkuY29tL2F1dGgiOnsiYWNjb3VudF9pZCI6ImFjY3QtbmVzdGVkIn19.sig";
    const payload = try std.fmt.allocPrint(std.testing.allocator,
        \\{{"access_token":"acc","refresh_token":"ref","id_token":"{s}"}}
    , .{id_token});
    defer std.testing.allocator.free(payload);

    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer deinitTokenResponse(std.testing.allocator, response);

    try std.testing.expect(response.provider_data != null);
    try std.testing.expect(std.mem.indexOf(u8, response.provider_data.?, "\"account_id\":\"acct-nested\"") != null);
}

test "parseTokenResponse preserves missing refresh token as null" {
    const payload =
        \\{"access_token":"acc"}
    ;
    const response = try parseTokenResponse(payload, std.testing.allocator);
    defer deinitTokenResponse(std.testing.allocator, response);

    try std.testing.expectEqualStrings("acc", response.access_token);
    try std.testing.expect(response.refresh_token == null);
    try std.testing.expect(response.expires_in > 0);
}

test "refresh response without rotated refresh token preserves existing refresh token" {
    const credentials = Credentials{
        .refresh = "old-refresh",
        .access = "old-access",
        .expires = 1,
        .provider_data = "{\"account_id\":\"old-account\"}",
    };
    const response = TokenResponse{
        .access_token = "new-access",
        .refresh_token = null,
        .expires_in = 3600,
    };
    const refreshed = try credentialsFromRefreshResponse(credentials, response, 1234, std.testing.allocator);
    defer std.testing.allocator.free(refreshed.refresh);
    defer std.testing.allocator.free(refreshed.access);
    defer if (refreshed.provider_data) |data| std.testing.allocator.free(data);

    try std.testing.expectEqualStrings("old-refresh", refreshed.refresh);
    try std.testing.expectEqualStrings("new-access", refreshed.access);
    try std.testing.expectEqual(@as(i64, 1234), refreshed.expires);
    try std.testing.expectEqualStrings("{\"account_id\":\"old-account\"}", refreshed.provider_data.?);
}

test "parseTokenResponse maps oauth error payload to OAuthFailed" {
    const payload =
        \\{"error":"invalid_grant","error_description":"bad code"}
    ;
    try std.testing.expectError(error.OAuthFailed, parseTokenResponse(payload, std.testing.allocator));
}

fn fillTestStateBytes(buf: []u8) void {
    for (buf, 0..) |*byte, index| {
        byte.* = @intCast(index % 256);
    }
}

fn fillAltTestStateBytes(buf: []u8) void {
    for (buf, 0..) |*byte, index| {
        byte.* = @intCast((index + 1) % 256);
    }
}

test "generateStateWithRandom derives state from the supplied bytes" {
    const state = try generateStateWithRandom(std.testing.allocator, fillTestStateBytes);
    defer std.testing.allocator.free(state);

    try std.testing.expectEqualStrings("AAECAwQFBgcICQoLDA0ODxAREhMUFRYXGBkaGxwdHh8", state);
}

test "generateStateWithRandom output changes when the bytes change" {
    const first = try generateStateWithRandom(std.testing.allocator, fillTestStateBytes);
    defer std.testing.allocator.free(first);

    const second = try generateStateWithRandom(std.testing.allocator, fillAltTestStateBytes);
    defer std.testing.allocator.free(second);

    try std.testing.expect(!std.mem.eql(u8, first, second));
}

test "generateState produces distinct values across calls" {
    const first = try generateState(std.testing.allocator);
    defer std.testing.allocator.free(first);

    const second = try generateState(std.testing.allocator);
    defer std.testing.allocator.free(second);

    try std.testing.expectEqual(@as(usize, 43), first.len);
    try std.testing.expect(!std.mem.eql(u8, first, second));
}

const FakeDeviceServer = struct {
    var pending_polls: usize = 0;
    var polls: usize = 0;
    var cancel_at_poll: ?usize = null;
    var cancel_at_ms: ?i64 = null;
    var user_code_status: u16 = 200;
    var interval_seconds: []const u8 = "0";
    var exchanges: usize = 0;
    var user_code_request: std.ArrayList(u8) = .empty;
    var exchange_request: std.ArrayList(u8) = .empty;
    var shown_url: std.ArrayList(u8) = .empty;
    var shown_instructions: std.ArrayList(u8) = .empty;
    var browser_returns: bool = false;
    var poll_status: u16 = 403;
    var browser_delay_ms: u64 = 0;
    var browser: ?std.Thread = null;

    fn reset() void {
        browser_returns = false;
        browser = null;
        poll_status = 403;
        browser_delay_ms = 0;
        pending_polls = 0;
        polls = 0;
        cancel_at_poll = null;
        cancel_at_ms = null;
        user_code_status = 200;
        interval_seconds = "0";
        exchanges = 0;
    }

    fn deinit() void {
        user_code_request.clearAndFree(std.testing.allocator);
        exchange_request.clearAndFree(std.testing.allocator);
        shown_url.clearAndFree(std.testing.allocator);
        shown_instructions.clearAndFree(std.testing.allocator);
    }

    fn respond(allocator: std.mem.Allocator, status: u16, body: []const u8) http.FetchError!http.Fetched {
        return .{ .status = status, .body = try allocator.dupe(u8, body) };
    }

    fn fetch(allocator: std.mem.Allocator, url: []const u8, options: http.FetchOptions) http.FetchError!http.Fetched {
        if (std.mem.eql(u8, url, device_user_code_url)) {
            user_code_request.appendSlice(std.testing.allocator, options.body orelse "") catch return error.OutOfMemory;
            const reply = std.fmt.allocPrint(std.testing.allocator, "{{\"device_auth_id\":\"dev-1\",\"user_code\":\"ABCD-1234\",\"interval\":\"{s}\"}}", .{interval_seconds}) catch return error.OutOfMemory;
            defer std.testing.allocator.free(reply);
            return respond(allocator, user_code_status, reply);
        }
        if (std.mem.eql(u8, url, device_token_url)) {
            polls += 1;
            if (polls <= pending_polls) return respond(allocator, poll_status, "{}");
            return respond(allocator, 200, "{\"authorization_code\":\"auth-code\",\"code_challenge\":\"c\",\"code_verifier\":\"verifier-1\"}");
        }
        if (std.mem.eql(u8, url, token_url)) {
            exchanges += 1;
            exchange_request.appendSlice(std.testing.allocator, options.body orelse "") catch return error.OutOfMemory;
            return respond(allocator, 200, "{\"access_token\":\"acc\",\"refresh_token\":\"ref\",\"expires_in\":3600}");
        }
        return error.RequestFailed;
    }

    fn onAuth(info: AuthInfo) void {
        shown_url.appendSlice(std.testing.allocator, info.url) catch {};
        shown_instructions.appendSlice(std.testing.allocator, info.instructions orelse "") catch {};
        if (browser_returns) browser = std.Thread.spawn(.{}, returnFromBrowser, .{}) catch null;
    }

    fn param(url: []const u8, key: []const u8) []const u8 {
        const start = (std.mem.indexOf(u8, url, key) orelse return "") + key.len;
        const end = std.mem.indexOfScalarPos(u8, url, start, '&') orelse url.len;
        return url[start..end];
    }

    fn callbackPort() u16 {
        const redirect = param(shown_url.items, "redirect_uri=http%3A%2F%2Flocalhost%3A");
        return std.fmt.parseInt(u16, redirect[0 .. std.mem.indexOf(u8, redirect, "%2F") orelse return 0], 10) catch 0;
    }

    fn returnFromBrowser() void {
        compat.time.sleepMs(browser_delay_ms);
        var stream = compat.net.tcpConnectHost(std.heap.page_allocator, "127.0.0.1", callbackPort()) catch return;
        defer stream.close();
        var request: [512]u8 = undefined;
        const line = std.fmt.bufPrint(&request, "GET /auth/callback?code=browser-code&state={s} HTTP/1.1\r\n\r\n", .{param(shown_url.items, "&state=")}) catch return;
        stream.writeAll(line) catch return;
        var sink: [256]u8 = undefined;
        while (compat.net.readableWithin(compat.net.streamHandle(&stream), 3_000) catch false) {
            if ((stream.readSome(&sink) catch 0) == 0) break;
        }
    }

    fn isCancelled() bool {
        if (cancel_at_ms) |at| return compat.time.nowMillis() >= at;
        const at = cancel_at_poll orelse return false;
        return polls >= at;
    }

    const callbacks: DeviceCallbacks = .{ .onAuth = onAuth, .isCancelled = isCancelled, .fetch = fetch, .browser_port = null, .wait_ms = 5_000 };
    const with_browser: DeviceCallbacks = .{ .onAuth = onAuth, .isCancelled = isCancelled, .fetch = fetch, .browser_port = 0, .wait_ms = 5_000 };
};

test "Codex device login shows the verification URL and user code, polls until the code is approved, and exchanges the grant it gets" {
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.pending_polls = 2;

    const credentials = try loginWithBrowserOrDeviceCode(FakeDeviceServer.callbacks, std.testing.allocator);
    defer std.testing.allocator.free(credentials.refresh);
    defer std.testing.allocator.free(credentials.access);
    defer if (credentials.provider_data) |data| std.testing.allocator.free(data);

    try std.testing.expectEqualStrings("{\"client_id\":\"app_EMoamEEZ73f0CkXaXp7hrann\"}", FakeDeviceServer.user_code_request.items);
    try std.testing.expectEqualStrings("https://auth.openai.com/codex/device", FakeDeviceServer.shown_url.items);
    try std.testing.expectEqualStrings("Enter code: ABCD-1234", FakeDeviceServer.shown_instructions.items);
    try std.testing.expectEqual(@as(usize, 3), FakeDeviceServer.polls);
    try std.testing.expectEqualStrings(
        "grant_type=authorization_code&code=auth-code&redirect_uri=https%3A%2F%2Fauth.openai.com%2Fdeviceauth%2Fcallback&client_id=app_EMoamEEZ73f0CkXaXp7hrann&code_verifier=verifier-1",
        FakeDeviceServer.exchange_request.items,
    );
    try std.testing.expectEqualStrings("acc", credentials.access);
    try std.testing.expectEqualStrings("ref", credentials.refresh);
}

test "Codex device login stops polling once the flow is cancelled and exchanges nothing" {
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.pending_polls = 1000;
    FakeDeviceServer.cancel_at_poll = 2;

    try std.testing.expectError(error.AuthFlowCancelled, loginWithBrowserOrDeviceCode(FakeDeviceServer.callbacks, std.testing.allocator));
    try std.testing.expectEqual(@as(usize, 2), FakeDeviceServer.polls);
    try std.testing.expectEqual(@as(usize, 0), FakeDeviceServer.exchanges);
}

test "Codex device login notices a cancel while it waits out the polling interval" {
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.pending_polls = 2;
    FakeDeviceServer.interval_seconds = "2";
    const started = compat.time.nowMillis();
    FakeDeviceServer.cancel_at_ms = started + 300;

    try std.testing.expectError(error.AuthFlowCancelled, loginWithBrowserOrDeviceCode(FakeDeviceServer.callbacks, std.testing.allocator));
    try std.testing.expect(compat.time.nowMillis() - started < 1_000);
    try std.testing.expectEqual(@as(usize, 1), FakeDeviceServer.polls);
}

test "Codex device login reports a server without device codes before showing anything" {
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.user_code_status = 404;

    try std.testing.expectError(error.DeviceCodeLoginUnavailable, loginWithBrowserOrDeviceCode(FakeDeviceServer.callbacks, std.testing.allocator));
    try std.testing.expectEqual(@as(usize, 0), FakeDeviceServer.shown_url.items.len);
    try std.testing.expectEqual(@as(usize, 0), FakeDeviceServer.polls);
}

test "Codex login offers the browser and the device code together and finishes through the browser callback when it returns" {
    if (!loopback.supported) return error.SkipZigTest;
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.pending_polls = 1000;
    FakeDeviceServer.browser_returns = true;

    const credentials = try loginWithBrowserOrDeviceCode(FakeDeviceServer.with_browser, std.testing.allocator);
    defer std.testing.allocator.free(credentials.refresh);
    defer std.testing.allocator.free(credentials.access);
    defer if (credentials.provider_data) |data| std.testing.allocator.free(data);
    if (FakeDeviceServer.browser) |thread| thread.join();

    try std.testing.expect(std.mem.startsWith(u8, FakeDeviceServer.shown_url.items, "https://auth.openai.com/oauth/authorize?"));
    try std.testing.expectEqualStrings(
        "Open this URL in a browser on this computer. On another device, open https://auth.openai.com/codex/device and enter code ABCD-1234 instead.",
        FakeDeviceServer.shown_instructions.items,
    );
    const expected_redirect = try std.fmt.allocPrint(std.testing.allocator, "redirect_uri=http%3A%2F%2Flocalhost%3A{d}%2Fauth%2Fcallback", .{FakeDeviceServer.callbackPort()});
    defer std.testing.allocator.free(expected_redirect);
    try std.testing.expect(std.mem.indexOf(u8, FakeDeviceServer.exchange_request.items, "code=browser-code&") != null);
    try std.testing.expect(std.mem.indexOf(u8, FakeDeviceServer.exchange_request.items, expected_redirect) != null);
    try std.testing.expectEqualStrings("acc", credentials.access);
}

test "Codex login finishes through the device code when it is approved while the browser listener waits" {
    if (!loopback.supported) return error.SkipZigTest;
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.pending_polls = 2;

    const credentials = try loginWithBrowserOrDeviceCode(FakeDeviceServer.with_browser, std.testing.allocator);
    defer std.testing.allocator.free(credentials.refresh);
    defer std.testing.allocator.free(credentials.access);
    defer if (credentials.provider_data) |data| std.testing.allocator.free(data);

    try std.testing.expectEqual(@as(usize, 3), FakeDeviceServer.polls);
    try std.testing.expect(std.mem.indexOf(u8, FakeDeviceServer.exchange_request.items, "code=auth-code&redirect_uri=https%3A%2F%2Fauth.openai.com%2Fdeviceauth%2Fcallback") != null);
}

test "Codex login keeps waiting for the browser after the device code poll fails" {
    if (!loopback.supported) return error.SkipZigTest;
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.pending_polls = 1000;
    FakeDeviceServer.poll_status = 500;
    FakeDeviceServer.interval_seconds = "1";
    FakeDeviceServer.browser_returns = true;
    FakeDeviceServer.browser_delay_ms = 400;

    const credentials = try loginWithBrowserOrDeviceCode(FakeDeviceServer.with_browser, std.testing.allocator);
    defer std.testing.allocator.free(credentials.refresh);
    defer std.testing.allocator.free(credentials.access);
    defer if (credentials.provider_data) |data| std.testing.allocator.free(data);
    if (FakeDeviceServer.browser) |thread| thread.join();

    try std.testing.expect(std.mem.indexOf(u8, FakeDeviceServer.exchange_request.items, "code=browser-code&") != null);
    try std.testing.expect(FakeDeviceServer.polls >= 1);
}

test "Codex login falls back to the browser alone when the server issues no device codes" {
    if (!loopback.supported) return error.SkipZigTest;
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    FakeDeviceServer.user_code_status = 404;
    FakeDeviceServer.browser_returns = true;

    const credentials = try loginWithBrowserOrDeviceCode(FakeDeviceServer.with_browser, std.testing.allocator);
    defer std.testing.allocator.free(credentials.refresh);
    defer std.testing.allocator.free(credentials.access);
    defer if (credentials.provider_data) |data| std.testing.allocator.free(data);
    if (FakeDeviceServer.browser) |thread| thread.join();

    try std.testing.expectEqualStrings("Open this URL in a browser on this computer; the login finishes on its own.", FakeDeviceServer.shown_instructions.items);
    try std.testing.expectEqual(@as(usize, 0), FakeDeviceServer.polls);
    try std.testing.expect(std.mem.indexOf(u8, FakeDeviceServer.exchange_request.items, "code=browser-code&") != null);
}

test "Codex browser callback on its registered port is the redirect OpenAI accepts for this client" {
    const uri = try std.fmt.allocPrint(std.testing.allocator, "http://localhost:{d}{s}", .{ browser_callback_port, browser_callback_path });
    defer std.testing.allocator.free(uri);
    try std.testing.expectEqualStrings(redirect_uri, uri);
}

test "Codex device user code response takes a numeric or string interval in seconds" {
    const numeric = try parseUserCodeResponse(std.testing.allocator, "{\"device_auth_id\":\"d\",\"usercode\":\"U\",\"interval\":7}");
    defer numeric.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(u64, 7000), numeric.interval_ms);
    try std.testing.expectEqualStrings("U", numeric.user_code);
    const text = try parseUserCodeResponse(std.testing.allocator, "{\"device_auth_id\":\"d\",\"user_code\":\"V\",\"interval\":\" 3 \"}");
    defer text.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(u64, 3000), text.interval_ms);
    try std.testing.expectError(error.ParseError, parseUserCodeResponse(std.testing.allocator, "{\"device_auth_id\":\"d\",\"user_code\":\"V\",\"interval\":\"soon\"}"));
}

test "Codex device login survives allocation failures" {
    FakeDeviceServer.reset();
    defer FakeDeviceServer.deinit();
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            FakeDeviceServer.polls = 0;
            const credentials = try loginWithBrowserOrDeviceCode(FakeDeviceServer.callbacks, allocator);
            allocator.free(credentials.refresh);
            allocator.free(credentials.access);
            if (credentials.provider_data) |data| allocator.free(data);
        }
    }.run, .{});
}
