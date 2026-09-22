const std = @import("std");
const compat = @import("compat");

pub const DEFAULT_E2E_TIMEOUT_MS: u64 = 60_000;

pub fn createDeadline(timeout_ms: u64) i64 {
    return compat.time.nowMillis() + @as(i64, @intCast(timeout_ms));
}

pub fn isDeadlineExceeded(deadline: i64) bool {
    return compat.time.nowMillis() > deadline;
}

pub fn testStart(test_name: []const u8) void {
    std.debug.print("\n\x1b[36m[TEST START]\x1b[0m {s}\n", .{test_name});
}

pub fn testSuccess(test_name: []const u8) void {
    std.debug.print("\x1b[32m[TEST PASS]\x1b[0m {s}\n", .{test_name});
}

pub fn skipGitHubCopilotTest(allocator: std.mem.Allocator) error{SkipZigTest}!void {
    if (!shouldSkipGitHubCopilot(allocator)) return;
    std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: E2E test for 'github_copilot' - no credentials available (set GH_COPILOT_REFRESH/GH_COPILOT_ACCESS or COPILOT_TOKEN)\n", .{});
    return error.SkipZigTest;
}

pub fn skipAzureTest(allocator: std.mem.Allocator) error{SkipZigTest}!void {
    if (!shouldSkipProvider(allocator, "azure")) {
        if (compat.getEnvVarOwned(allocator, "AZURE_OPENAI_ENDPOINT")) |_| {
            return;
        } else |_| {}
        if (compat.getEnvVarOwned(allocator, "AZURE_RESOURCE_NAME")) |_| {
            return;
        } else |_| {}
    }
    std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: E2E test for 'azure' - no credentials available (set AZURE_OPENAI_API_KEY and AZURE_OPENAI_ENDPOINT/AZURE_RESOURCE_NAME)\n", .{});
    return error.SkipZigTest;
}

pub fn skipGoogleTest(allocator: std.mem.Allocator) error{SkipZigTest}!void {
    if (!shouldSkipProvider(allocator, "google")) return;
    std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: E2E test for 'google' - no credentials available (set GOOGLE_API_KEY)\n", .{});
    return error.SkipZigTest;
}

pub fn skipOllamaTest(allocator: std.mem.Allocator) error{SkipZigTest}!void {
    if (compat.getEnvVarOwned(allocator, "OLLAMA_API_KEY")) |key| {
        allocator.free(key);
        return;
    } else |_| {}
    std.debug.print("\n\x1b[90mSKIPPED\x1b[0m: E2E test for 'ollama' - OLLAMA_API_KEY not set\n", .{});
    return error.SkipZigTest;
}

pub const AnthropicCredential = struct {
    token: []const u8,
    is_oauth: bool,

    pub fn deinit(self: *AnthropicCredential, allocator: std.mem.Allocator) void {
        allocator.free(self.token);
    }
};

pub fn getAnthropicCredential(allocator: std.mem.Allocator) !?AnthropicCredential {
    if (compat.getEnvVarOwned(allocator, "ANTHROPIC_AUTH_TOKEN")) |token| {
        if (std.mem.findScalar(u8, token, ':')) |colon_pos| {
            const access_token = try allocator.dupe(u8, token[colon_pos + 1 ..]);
            allocator.free(token);
            return AnthropicCredential{
                .token = access_token,
                .is_oauth = true,
            };
        } else {
            return AnthropicCredential{
                .token = token,
                .is_oauth = true,
            };
        }
    } else |_| {}

    if (compat.getEnvVarOwned(allocator, "ANTHROPIC_API_KEY")) |key| {
        return AnthropicCredential{
            .token = key,
            .is_oauth = false,
        };
    } else |_| {}

    return getAnthropicCredentialFromAuthFile(allocator);
}

fn getAnthropicCredentialFromAuthFile(allocator: std.mem.Allocator) !?AnthropicCredential {
    const home_dir = compat.getEnvVarOwned(allocator, "HOME") catch return null;
    defer allocator.free(home_dir);

    const auth_path = try std.fs.path.join(allocator, &[_][]const u8{ home_dir, ".oapx", "auth.json" });
    defer allocator.free(auth_path);

    const file = std.Io.Dir.openFileAbsolute(std.testing.io, auth_path, .{}) catch return null;
    defer file.close(std.testing.io);

    const stat = try file.stat(std.testing.io);
    if (stat.size > 1024 * 1024) return null;
    var read_buffer: [4096]u8 = undefined;
    var reader = file.reader(std.testing.io, &read_buffer);
    const content = try reader.interface.readAlloc(allocator, @intCast(stat.size));
    defer allocator.free(content);

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, content, .{}) catch return null;
    defer parsed.deinit();

    const root = parsed.value;
    if (root != .object) return null;

    const providers = root.object.get("providers") orelse return null;
    if (providers != .object) return null;

    const provider_obj = providers.object.get("anthropic") orelse return null;
    if (provider_obj != .object) return null;

    if (provider_obj.object.get("oauth_token")) |oauth_val| {
        if (oauth_val == .string) {
            return AnthropicCredential{
                .token = try allocator.dupe(u8, oauth_val.string),
                .is_oauth = true,
            };
        }
    }

    if (provider_obj.object.get("api_key")) |api_key_val| {
        if (api_key_val == .string) {
            return AnthropicCredential{
                .token = try allocator.dupe(u8, api_key_val.string),
                .is_oauth = false,
            };
        }
    }

    return null;
}

pub fn shouldSkipAnthropic(allocator: std.mem.Allocator) bool {
    const cred = getAnthropicCredential(allocator) catch return true;
    if (cred) |c| {
        var mutable_cred = c;
        mutable_cred.deinit(allocator);
        return false;
    }
    return true;
}

pub fn getApiKey(allocator: std.mem.Allocator, provider_name: []const u8) !?[]const u8 {
    var env_var_name: std.ArrayList(u8) = .{};
    defer env_var_name.deinit(allocator);

    try env_var_name.appendSlice(allocator, provider_name);
    try env_var_name.appendSlice(allocator, "_API_KEY");

    for (env_var_name.items) |*c| {
        c.* = std.ascii.toUpper(c.*);
    }

    if (compat.getEnvVarOwned(allocator, env_var_name.items)) |key| {
        return key;
    } else |_| {
        return getApiKeyFromAuthFile(allocator, provider_name);
    }
}

fn getApiKeyFromAuthFile(allocator: std.mem.Allocator, provider_name: []const u8) !?[]const u8 {
    const home_dir = compat.getEnvVarOwned(allocator, "HOME") catch return null;
    defer allocator.free(home_dir);

    const auth_path = try std.fs.path.join(allocator, &[_][]const u8{ home_dir, ".oapx", "auth.json" });
    defer allocator.free(auth_path);

    const file = std.Io.Dir.openFileAbsolute(std.testing.io, auth_path, .{}) catch return null;
    defer file.close(std.testing.io);

    const stat = try file.stat(std.testing.io);
    if (stat.size > 1024 * 1024) return null;
    var read_buffer: [4096]u8 = undefined;
    var reader = file.reader(std.testing.io, &read_buffer);
    const content = try reader.interface.readAlloc(allocator, @intCast(stat.size));
    defer allocator.free(content);

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, content, .{}) catch return null;
    defer parsed.deinit();

    const root = parsed.value;
    if (root != .object) return null;

    const providers = root.object.get("providers") orelse return null;
    if (providers != .object) return null;

    const provider_obj = providers.object.get(provider_name) orelse return null;
    if (provider_obj != .object) return null;

    const api_key = provider_obj.object.get("api_key") orelse return null;
    if (api_key != .string) return null;

    return try allocator.dupe(u8, api_key.string);
}

pub fn shouldSkipProvider(allocator: std.mem.Allocator, provider_name: []const u8) bool {
    if (std.ascii.eqlIgnoreCase(provider_name, "anthropic")) {
        return shouldSkipAnthropic(allocator);
    }

    const api_key = getApiKey(allocator, provider_name) catch return true;
    if (api_key) |key| {
        allocator.free(key);
        return false;
    }
    return true;
}

pub const GitHubCopilotCredentials = struct {
    copilot_token: []const u8,
    github_token: []const u8,

    pub fn deinit(self: *GitHubCopilotCredentials, allocator: std.mem.Allocator) void {
        allocator.free(self.copilot_token);
        allocator.free(self.github_token);
    }
};

pub fn getGitHubCopilotCredentials(allocator: std.mem.Allocator) !?GitHubCopilotCredentials {
    const refresh_result = compat.getEnvVarOwned(allocator, "GH_COPILOT_REFRESH");
    const access_result = compat.getEnvVarOwned(allocator, "GH_COPILOT_ACCESS");

    if (refresh_result) |refresh_token| {
        if (access_result) |access_token| {
            return GitHubCopilotCredentials{
                .github_token = refresh_token,
                .copilot_token = access_token,
            };
        } else |_| {
            return GitHubCopilotCredentials{
                .github_token = refresh_token,
                .copilot_token = try allocator.dupe(u8, refresh_token),
            };
        }
    } else |_| {
        if (access_result) |access_token| {
            allocator.free(access_token);
        } else |_| {}
    }

    if (compat.getEnvVarOwned(allocator, "COPILOT_TOKEN")) |token| {
        if (std.mem.findScalar(u8, token, ':')) |colon_pos| {
            const github_token = token[0..colon_pos];
            const copilot_token = token[colon_pos + 1 ..];
            const result = GitHubCopilotCredentials{
                .copilot_token = try allocator.dupe(u8, copilot_token),
                .github_token = try allocator.dupe(u8, github_token),
            };
            allocator.free(token);
            return result;
        } else {
            return GitHubCopilotCredentials{
                .copilot_token = token,
                .github_token = try allocator.dupe(u8, token),
            };
        }
    } else |_| {
        return getGitHubCopilotCredentialsFromAuthFile(allocator);
    }
}

fn getGitHubCopilotCredentialsFromAuthFile(allocator: std.mem.Allocator) !?GitHubCopilotCredentials {
    const home_dir = compat.getEnvVarOwned(allocator, "HOME") catch return null;
    defer allocator.free(home_dir);

    const auth_path = try std.fs.path.join(allocator, &[_][]const u8{ home_dir, ".oapx", "auth.json" });
    defer allocator.free(auth_path);

    const file = std.Io.Dir.openFileAbsolute(std.testing.io, auth_path, .{}) catch return null;
    defer file.close(std.testing.io);

    const stat = try file.stat(std.testing.io);
    if (stat.size > 1024 * 1024) return null;
    var read_buffer: [4096]u8 = undefined;
    var reader = file.reader(std.testing.io, &read_buffer);
    const content = try reader.interface.readAlloc(allocator, @intCast(stat.size));
    defer allocator.free(content);

    var parsed = std.json.parseFromSlice(std.json.Value, allocator, content, .{}) catch return null;
    defer parsed.deinit();

    const root = parsed.value;
    if (root != .object) return null;

    const providers = root.object.get("providers") orelse return null;
    if (providers != .object) return null;

    const provider_val = providers.object.get("github_copilot") orelse return null;

    if (provider_val == .string) {
        const combined = provider_val.string;
        if (std.mem.findScalar(u8, combined, ':')) |colon_pos| {
            const github_token = combined[0..colon_pos];
            const copilot_token = combined[colon_pos + 1 ..];
            return GitHubCopilotCredentials{
                .copilot_token = try allocator.dupe(u8, copilot_token),
                .github_token = try allocator.dupe(u8, github_token),
            };
        }
        return null;
    }

    if (provider_val != .object) return null;

    const copilot_token_val = provider_val.object.get("copilot_token") orelse return null;
    if (copilot_token_val != .string) return null;

    const github_token_val = provider_val.object.get("github_token") orelse return null;
    if (github_token_val != .string) return null;

    return GitHubCopilotCredentials{
        .copilot_token = try allocator.dupe(u8, copilot_token_val.string),
        .github_token = try allocator.dupe(u8, github_token_val.string),
    };
}

pub fn shouldSkipGitHubCopilot(allocator: std.mem.Allocator) bool {
    if (compat.getEnvVarOwned(allocator, "GH_COPILOT_REFRESH")) |token| {
        allocator.free(token);
        return false;
    } else |_| {}

    if (compat.getEnvVarOwned(allocator, "COPILOT_TOKEN")) |token| {
        allocator.free(token);
        return false;
    } else |_| {}

    const creds = getGitHubCopilotCredentials(allocator) catch return true;
    if (creds) |c| {
        var mutable_creds = c;
        mutable_creds.deinit(allocator);
        return false;
    }
    return true;
}

pub const FreshGitHubCopilotCredentials = struct {
    copilot_token: []const u8,
    github_token: []const u8,
    base_url: ?[]const u8 = null,

    pub fn deinit(self: *FreshGitHubCopilotCredentials, allocator: std.mem.Allocator) void {
        allocator.free(self.copilot_token);
        allocator.free(self.github_token);
        if (self.base_url) |url| allocator.free(url);
    }
};

pub fn getFreshGitHubCopilotCredentials(allocator: std.mem.Allocator) !?FreshGitHubCopilotCredentials {
    const oauth_github_copilot = @import("oauth/github_copilot");

    const creds = (try getGitHubCopilotCredentials(allocator)) orelse return null;
    var mutable_creds = creds;
    defer mutable_creds.deinit(allocator);

    const fresh_creds = try oauth_github_copilot.refreshToken(.{
        .refresh = creds.github_token,
        .access = creds.copilot_token,
        .expires = 0,
    }, allocator);

    const copilot_token = try allocator.dupe(u8, fresh_creds.access);
    const github_token = try allocator.dupe(u8, fresh_creds.refresh);
    const base_url = if (fresh_creds.base_url) |url| try allocator.dupe(u8, url) else null;

    allocator.free(fresh_creds.refresh);
    allocator.free(fresh_creds.access);
    if (fresh_creds.provider_data) |pd| allocator.free(pd);
    if (fresh_creds.enabled_models) |models| {
        for (models) |m| allocator.free(m);
        allocator.free(models);
    }
    if (fresh_creds.base_url) |url| allocator.free(url);

    return FreshGitHubCopilotCredentials{
        .copilot_token = copilot_token,
        .github_token = github_token,
        .base_url = base_url,
    };
}
