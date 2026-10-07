const std = @import("std");
const compat = @import("compat");
const json_writer = @import("json/writer");

fn tmpBase(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir) ![]u8 {
    const base = try std.fs.path.join(allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "makai" });
    errdefer allocator.free(base);
    try compat.fs.createDir(compat.fs.getCwd(), base);
    return base;
}

pub const ToolPermission = struct {
    tool_name: []u8,
    mode: Mode = .ask,

    pub const Mode = enum { ask, allow, deny };

    pub fn deinit(self: *ToolPermission, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_name);
        self.* = undefined;
    }
};

pub const Output = union(enum) {
    auto,
    max,
    tokens: u32,
};

pub const AutoCompact = union(enum) {
    auto,
    off,
    percent: u8,
    tokens: u32,
};

pub const VerbosityLevel = enum { quiet, normal, verbose };

pub const VerbosityPart = enum { thinking, tools, output, notices, status };

pub const Verbosity = struct {
    thinking: VerbosityLevel = .normal,
    tools: VerbosityLevel = .normal,
    output: VerbosityLevel = .normal,
    notices: VerbosityLevel = .normal,
    status: VerbosityLevel = .normal,

    pub fn all(level: VerbosityLevel) Verbosity {
        return .{ .thinking = level, .tools = level, .output = level, .notices = level, .status = level };
    }

    pub fn get(self: Verbosity, part: VerbosityPart) VerbosityLevel {
        return switch (part) {
            inline else => |tag| @field(self, @tagName(tag)),
        };
    }

    pub fn transcriptEquals(self: Verbosity, other: Verbosity) bool {
        return self.thinking == other.thinking and self.tools == other.tools and self.output == other.output and self.notices == other.notices;
    }

    pub fn cycled(self: Verbosity) Verbosity {
        const uniform = self.thinking == self.tools and self.tools == self.output and self.output == self.notices and self.notices == self.status;
        if (!uniform) return all(.normal);
        return all(switch (self.thinking) {
            .quiet => .normal,
            .normal => .verbose,
            .verbose => .quiet,
        });
    }

    pub fn set(self: *Verbosity, part: VerbosityPart, level: VerbosityLevel) void {
        switch (part) {
            inline else => |tag| @field(self, @tagName(tag)) = level,
        }
    }
};

pub const ModeSettings = struct {
    verbosity: Verbosity = .{},
    compact_output: bool = true,
    context_window: ?u32 = null,
    output: Output = .auto,
    auto_worktree: bool = false,
    autocompact: AutoCompact = .auto,
};

pub const Config = struct {
    model: []u8,
    provider: []u8,
    api: []u8,
    workspace: []u8,
    permissions: std.ArrayList(ToolPermission) = .empty,
    mode: ModeSettings = .{},

    pub fn defaults(allocator: std.mem.Allocator) !Config {
        const model = try allocator.dupe(u8, "claude-sonnet-5-5");
        errdefer allocator.free(model);
        const provider = try allocator.dupe(u8, "anthropic");
        errdefer allocator.free(provider);
        const api = try allocator.dupe(u8, "anthropic-messages");
        errdefer allocator.free(api);
        const workspace = try allocator.dupe(u8, ".");
        return .{ .model = model, .provider = provider, .api = api, .workspace = workspace };
    }

    pub fn deinit(self: *Config, allocator: std.mem.Allocator) void {
        allocator.free(self.model);
        allocator.free(self.provider);
        allocator.free(self.api);
        allocator.free(self.workspace);
        for (self.permissions.items) |*permission| permission.deinit(allocator);
        self.permissions.deinit(allocator);
        self.* = undefined;
    }
};

pub const Store = struct {
    allocator: std.mem.Allocator,
    base_dir: []u8,

    pub fn init(allocator: std.mem.Allocator, base_dir: []const u8) !Store {
        return .{ .allocator = allocator, .base_dir = try allocator.dupe(u8, base_dir) };
    }

    pub fn initDefault(allocator: std.mem.Allocator) !Store {
        const home = compat.getEnvVarOwned(allocator, "HOME") catch |err| switch (err) {
            error.EnvironmentVariableMissing => return error.HomeNotFound,
            else => return err,
        };
        defer allocator.free(home);
        const base = try std.fs.path.join(allocator, &.{ home, ".oapx" });
        defer allocator.free(base);
        return init(allocator, base);
    }

    pub fn deinit(self: *Store) void {
        self.allocator.free(self.base_dir);
        self.* = undefined;
    }

    pub fn load(self: Store) !Config {
        try compat.fs.createDir(compat.fs.getCwd(), self.base_dir);
        const path = try std.fs.path.join(self.allocator, &.{ self.base_dir, "config.json" });
        defer self.allocator.free(path);
        const data = compat.fs.readFileAlloc(self.allocator, compat.fs.getCwd(), path, 1024 * 1024) catch |err| switch (err) {
            error.FileNotFound => {
                var cfg = try Config.defaults(self.allocator);
                errdefer cfg.deinit(self.allocator);
                try self.save(cfg);
                return cfg;
            },
            else => return err,
        };
        defer self.allocator.free(data);
        return parseConfig(self.allocator, data);
    }

    pub fn loadIfExists(self: Store) !?Config {
        const path = try std.fs.path.join(self.allocator, &.{ self.base_dir, "config.json" });
        defer self.allocator.free(path);
        const data = compat.fs.readFileAlloc(self.allocator, compat.fs.getCwd(), path, 1024 * 1024) catch |err| switch (err) {
            error.FileNotFound => return null,
            else => return err,
        };
        defer self.allocator.free(data);
        return try parseConfig(self.allocator, data);
    }

    pub fn save(self: Store, cfg: Config) !void {
        try compat.fs.createDir(compat.fs.getCwd(), self.base_dir);
        const path = try std.fs.path.join(self.allocator, &.{ self.base_dir, "config.json" });
        defer self.allocator.free(path);
        const tmp = try std.fs.path.join(self.allocator, &.{ self.base_dir, "config.json.tmp" });
        defer self.allocator.free(tmp);
        const data = try serializeConfig(self.allocator, cfg);
        defer self.allocator.free(data);
        try compat.fs.atomicReplace(compat.fs.getCwd(), path, tmp, data);
    }
};

fn parseConfig(allocator: std.mem.Allocator, data: []const u8) !Config {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, data, .{});
    defer parsed.deinit();
    const obj = switch (parsed.value) {
        .object => |o| o,
        else => return error.InvalidConfig,
    };

    const model = try dupStringField(allocator, obj, "model", "claude-sonnet-5-5");
    errdefer allocator.free(model);
    const provider = try dupStringField(allocator, obj, "provider", "anthropic");
    errdefer allocator.free(provider);
    const api = try dupStringField(allocator, obj, "api", "");
    errdefer allocator.free(api);
    const workspace = try dupStringField(allocator, obj, "workspace", "");
    var cfg = Config{ .model = model, .provider = provider, .api = api, .workspace = workspace };
    errdefer cfg.deinit(allocator);

    if (cfg.workspace.len == 0) {
        allocator.free(cfg.workspace);
        cfg.workspace = try allocator.dupe(u8, ".");
    }

    if (obj.get("permissions")) |value| switch (value) {
        .array => |arr| for (arr.items) |item| {
            const perm_obj = switch (item) {
                .object => |o| o,
                else => continue,
            };
            const tool = stringField(perm_obj, "tool_name") orelse continue;
            const mode_text = stringField(perm_obj, "mode") orelse "ask";
            try cfg.permissions.append(allocator, .{
                .tool_name = try allocator.dupe(u8, tool),
                .mode = parsePermissionMode(mode_text),
            });
        },
        else => {},
    };

    if (obj.get("mode")) |value| switch (value) {
        .object => |mode_obj| {
            cfg.mode.compact_output = boolField(mode_obj, "compact_output", cfg.mode.compact_output);
            cfg.mode.context_window = positiveIntField(mode_obj, "context_window");
            cfg.mode.output = outputField(mode_obj);
            cfg.mode.auto_worktree = boolField(mode_obj, "auto_worktree", cfg.mode.auto_worktree);
            cfg.mode.autocompact = autoCompactField(mode_obj);
            cfg.mode.verbosity = verbosityField(mode_obj);
        },
        else => {},
    };

    return cfg;
}

fn serializeConfig(allocator: std.mem.Allocator, cfg: Config) ![]u8 {
    var buf: std.ArrayList(u8) = .empty;
    errdefer buf.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buf, allocator);
    try w.beginObject();
    try w.writeStringField("model", cfg.model);
    try w.writeStringField("provider", cfg.provider);
    try w.writeStringField("api", cfg.api);
    try w.writeStringField("workspace", cfg.workspace);
    try w.writeKey("permissions");
    try w.beginArray();
    for (cfg.permissions.items) |permission| {
        try w.beginObject();
        try w.writeStringField("tool_name", permission.tool_name);
        try w.writeStringField("mode", @tagName(permission.mode));
        try w.endObject();
    }
    try w.endArray();
    try w.writeKey("mode");
    try w.beginObject();
    try w.writeBoolField("compact_output", cfg.mode.compact_output);
    if (cfg.mode.context_window) |window| {
        try w.writeIntField("context_window", window);
    }
    try w.writeBoolField("auto_worktree", cfg.mode.auto_worktree);
    switch (cfg.mode.output) {
        .auto => {},
        .max => try w.writeStringField("output", "max"),
        .tokens => |count| try w.writeIntField("output", count),
    }
    switch (cfg.mode.autocompact) {
        .auto => try w.writeStringField("autocompact", "auto"),
        .off => try w.writeStringField("autocompact", "off"),
        .percent => |percent| try w.writeIntField("autocompact", percent),
        .tokens => |count| {
            var buffer: [32]u8 = undefined;
            try w.writeStringField("autocompact", try std.fmt.bufPrint(&buffer, "{d} tokens", .{count}));
        },
    }
    try w.writeKey("verbosity");
    try w.beginObject();
    inline for (@typeInfo(VerbosityPart).@"enum".fields) |field| {
        try w.writeStringField(field.name, @tagName(cfg.mode.verbosity.get(@enumFromInt(field.value))));
    }
    try w.endObject();
    try w.endObject();
    try w.endObject();
    try buf.append(allocator, '\n');
    return buf.toOwnedSlice(allocator);
}

fn verbosityField(obj: std.json.ObjectMap) Verbosity {
    var verbosity: Verbosity = .{};
    const value = obj.get("verbosity") orelse return verbosity;
    const parts = switch (value) {
        .object => |o| o,
        else => return verbosity,
    };
    inline for (@typeInfo(VerbosityPart).@"enum".fields) |field| {
        if (stringField(parts, field.name)) |text| {
            if (std.meta.stringToEnum(VerbosityLevel, text)) |level| verbosity.set(@enumFromInt(field.value), level);
        }
    }
    return verbosity;
}

fn dupStringField(allocator: std.mem.Allocator, obj: std.json.ObjectMap, key: []const u8, default: []const u8) ![]u8 {
    return try allocator.dupe(u8, stringField(obj, key) orelse default);
}

fn stringField(obj: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .string => |s| s,
        else => null,
    };
}

fn positiveIntField(obj: std.json.ObjectMap, key: []const u8) ?u32 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |n| if (n > 0) std.math.cast(u32, n) else null,
        else => null,
    };
}

fn outputField(obj: std.json.ObjectMap) Output {
    if (positiveIntField(obj, "output")) |count| return .{ .tokens = count };
    const text = stringField(obj, "output") orelse return .auto;
    if (std.mem.eql(u8, text, "max")) return .max;
    return .auto;
}

fn autoCompactField(obj: std.json.ObjectMap) AutoCompact {
    const value = obj.get("autocompact") orelse return .auto;
    return switch (value) {
        .string => |text| if (std.mem.eql(u8, text, "off")) .off else if (std.mem.endsWith(u8, text, " tokens")) (if (std.fmt.parseInt(u32, text[0 .. text.len - " tokens".len], 10)) |count| (if (count > 0) .{ .tokens = count } else .auto) else |_| .auto) else .auto,
        .integer => |n| if (n >= 1 and n <= 100) .{ .percent = @intCast(n) } else .auto,
        else => .auto,
    };
}

fn boolField(obj: std.json.ObjectMap, key: []const u8, default: bool) bool {
    const value = obj.get(key) orelse return default;
    return switch (value) {
        .bool => |b| b,
        else => default,
    };
}

fn parsePermissionMode(value: []const u8) ToolPermission.Mode {
    if (std.mem.eql(u8, value, "allow")) return .allow;
    if (std.mem.eql(u8, value, "deny")) return .deny;
    return .ask;
}

test "the default config leaks nothing under allocation failure" {
    try std.testing.checkAllAllocationFailures(std.testing.allocator, struct {
        fn run(allocator: std.mem.Allocator) !void {
            var cfg = try Config.defaults(allocator);
            cfg.deinit(allocator);
        }
    }.run, .{});
}

test "verbosity survives a save and reload part by part, and an unknown level keeps the default" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var cfg = try Config.defaults(std.testing.allocator);
    defer cfg.deinit(std.testing.allocator);
    cfg.mode.verbosity = Verbosity.all(.quiet);
    cfg.mode.verbosity.status = .verbose;
    try store.save(cfg);

    var loaded = try store.load();
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(VerbosityLevel.quiet, loaded.mode.verbosity.thinking);
    try std.testing.expectEqual(VerbosityLevel.quiet, loaded.mode.verbosity.tools);
    try std.testing.expectEqual(VerbosityLevel.quiet, loaded.mode.verbosity.output);
    try std.testing.expectEqual(VerbosityLevel.quiet, loaded.mode.verbosity.notices);
    try std.testing.expectEqual(VerbosityLevel.verbose, loaded.mode.verbosity.status);

    var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, "{\"verbosity\":{\"tools\":\"loud\",\"thinking\":\"verbose\"}}", .{});
    defer parsed.deinit();
    const read = verbosityField(parsed.value.object);
    try std.testing.expectEqual(VerbosityLevel.normal, read.tools);
    try std.testing.expectEqual(VerbosityLevel.verbose, read.thinking);
}

test "the verbosity cycle steps a uniform level and resets a mixed one to normal" {
    try std.testing.expectEqual(Verbosity.all(.normal), Verbosity.all(.quiet).cycled());
    try std.testing.expectEqual(Verbosity.all(.verbose), Verbosity.all(.normal).cycled());
    try std.testing.expectEqual(Verbosity.all(.quiet), Verbosity.all(.verbose).cycled());
    var mixed = Verbosity.all(.quiet);
    mixed.status = .verbose;
    try std.testing.expectEqual(Verbosity.all(.normal), mixed.cycled());
    try std.testing.expect(mixed.transcriptEquals(Verbosity.all(.quiet)));
    try std.testing.expect(!mixed.transcriptEquals(Verbosity.all(.normal)));
}

test "save config reload preserves model provider and api" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    var cfg = try Config.defaults(std.testing.allocator);
    defer cfg.deinit(std.testing.allocator);
    std.testing.allocator.free(cfg.model);
    cfg.model = try std.testing.allocator.dupe(u8, "model-b");
    std.testing.allocator.free(cfg.provider);
    cfg.provider = try std.testing.allocator.dupe(u8, "openai");
    std.testing.allocator.free(cfg.api);
    cfg.api = try std.testing.allocator.dupe(u8, "openai-responses");
    try cfg.permissions.append(std.testing.allocator, .{ .tool_name = try std.testing.allocator.dupe(u8, "shell_execute"), .mode = .deny });
    cfg.mode.compact_output = false;
    cfg.mode.auto_worktree = true;
    try store.save(cfg);

    var loaded = try store.load();
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("model-b", loaded.model);
    try std.testing.expectEqualStrings("openai", loaded.provider);
    try std.testing.expectEqualStrings("openai-responses", loaded.api);
    try std.testing.expectEqual(@as(usize, 1), loaded.permissions.items.len);
    try std.testing.expectEqual(ToolPermission.Mode.deny, loaded.permissions.items[0].mode);
    try std.testing.expectEqual(false, loaded.mode.compact_output);
    try std.testing.expectEqual(true, loaded.mode.auto_worktree);
}

test "an output setting survives a save, and auto is written as nothing" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    var cfg = try Config.defaults(std.testing.allocator);
    defer cfg.deinit(std.testing.allocator);
    const settings = [_]Output{ .max, .{ .tokens = 64_000 }, .auto };
    for (settings) |setting| {
        cfg.mode.output = setting;
        try store.save(cfg);
        var loaded = try store.load();
        defer loaded.deinit(std.testing.allocator);
        try std.testing.expectEqual(setting, loaded.mode.output);
    }

    const text = try serializeConfig(std.testing.allocator, cfg);
    defer std.testing.allocator.free(text);
    var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, text, .{});
    defer parsed.deinit();
    try std.testing.expect(parsed.value.object.get("mode").?.object.get("output") == null);
}

test "a context window survives a save and an absent one stays absent" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    var cfg = try Config.defaults(std.testing.allocator);
    defer cfg.deinit(std.testing.allocator);
    try store.save(cfg);

    var unset = try store.load();
    defer unset.deinit(std.testing.allocator);
    try std.testing.expect(unset.mode.context_window == null);

    cfg.mode.context_window = 1_000_000;
    try store.save(cfg);

    var set = try store.load();
    defer set.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(?u32, 1_000_000), set.mode.context_window);

    cfg.mode.context_window = null;
    try store.save(cfg);

    var cleared = try store.load();
    defer cleared.deinit(std.testing.allocator);
    try std.testing.expect(cleared.mode.context_window == null);
}

test "a context window that is not a positive whole number is not read as one" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    const malformed = [_][]const u8{
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"context_window\":0}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"context_window\":-5}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"context_window\":\"1m\"}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"context_window\":1.5}}",
    };
    for (malformed) |text| {
        const path = try std.fs.path.join(std.testing.allocator, &.{ base, "config.json" });
        defer std.testing.allocator.free(path);
        try compat.fs.writeFile(compat.fs.getCwd(), path, text);
        var loaded = try store.load();
        defer loaded.deinit(std.testing.allocator);
        try std.testing.expect(loaded.mode.context_window == null);
    }
}

test "autocompact is automatic by default and a set share, token count or off survives a save" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    var cfg = try Config.defaults(std.testing.allocator);
    defer cfg.deinit(std.testing.allocator);
    try store.save(cfg);
    var unset = try store.load();
    defer unset.deinit(std.testing.allocator);
    try std.testing.expect(unset.mode.autocompact == .auto);

    const settings = [_]AutoCompact{ .{ .percent = 65 }, .off, .{ .tokens = 120_000 }, .auto };
    for (settings) |setting| {
        cfg.mode.autocompact = setting;
        try store.save(cfg);
        var loaded = try store.load();
        defer loaded.deinit(std.testing.allocator);
        try std.testing.expectEqualDeep(setting, loaded.mode.autocompact);
    }
}

test "an autocompact value that is neither off, a share from 1 to 100, nor a positive token count reads as automatic" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    const malformed = [_][]const u8{
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"autocompact\":0}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"autocompact\":101}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"autocompact\":\"sometimes\"}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"autocompact\":true}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"autocompact\":\"0 tokens\"}}",
        "{\"model\":\"m\",\"provider\":\"p\",\"api\":\"a\",\"workspace\":\".\",\"mode\":{\"autocompact\":\"many tokens\"}}",
    };
    for (malformed) |text| {
        const path = try std.fs.path.join(std.testing.allocator, &.{ base, "config.json" });
        defer std.testing.allocator.free(path);
        try compat.fs.writeFile(compat.fs.getCwd(), path, text);
        var loaded = try store.load();
        defer loaded.deinit(std.testing.allocator);
        try std.testing.expect(loaded.mode.autocompact == .auto);
    }
}

test "missing config creates defaults" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var cfg = try store.load();
    defer cfg.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("claude-sonnet-5-5", cfg.model);

    const path = try std.fs.path.join(std.testing.allocator, &.{ base, "config.json" });
    defer std.testing.allocator.free(path);
    const data = try compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), path, 1024);
    defer std.testing.allocator.free(data);
    try std.testing.expect(std.mem.indexOf(u8, data, "claude-sonnet-5-5") != null);
}

test "loadIfExists returns null without creating defaults" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    const missing = try store.loadIfExists();
    try std.testing.expect(missing == null);

    const path = try std.fs.path.join(std.testing.allocator, &.{ base, "config.json" });
    defer std.testing.allocator.free(path);
    try std.testing.expectError(error.FileNotFound, compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), path, 1024));
}
