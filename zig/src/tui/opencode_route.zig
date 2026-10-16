const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const adapter_config = @import("adapter_config");
const opencode_adapter = @import("opencode_adapter");
const opencode_server = @import("opencode_server");
const opencode_install = @import("opencode_install");
const client = @import("opencode_client");
const httpapi = @import("opencode_httpapi");
const oap_execution = @import("tui/oap_execution");

const listing_wait_ns: u64 = 20 * std.time.ns_per_s;
const listing_poll_ns: u64 = 500 * std.time.ns_per_ms;

const Listed = struct {
    data: []const struct {
        id: []const u8,
        providerID: []const u8,
        name: []const u8 = "",
        enabled: bool = true,
        limit: struct { context: u64 = 0, output: u64 = 0 } = .{},
        capabilities: struct { input: []const []const u8 = &.{} } = .{},
    } = &.{},
};

pub const Route = struct {
    allocator: std.mem.Allocator,
    held: std.heap.ArenaAllocator,
    launch: opencode_server.Launch,
    server: ?*opencode_server.Server = null,
    adapter: opencode_adapter.Adapter,
    lock: std.atomic.Mutex = .unlocked,
    chosen: [2][]u8 = .{ &.{}, &.{} },
    refused: []u8 = &.{},

    pub fn discover(allocator: std.mem.Allocator, environ: *const std.process.Environ.Map) !?*Route {
        const home = environ.get("HOME") orelse return null;
        if (home.len == 0) return null;
        const self = try allocator.create(Route);
        errdefer allocator.destroy(self);
        self.* = .{ .allocator = allocator, .held = .init(allocator), .launch = undefined, .adapter = opencode_adapter.Adapter.init(allocator, .{ .endpoint = "" }) };
        errdefer self.held.deinit();
        const arena = self.held.allocator();
        const fetched = try opencode_install.installed(arena, home);
        const executable = fetched orelse try adapter_config.resolveExecutable(arena, compat.fs.defaultIo(), "opencode", environ.get("PATH") orelse "") orelse {
            self.held.deinit();
            allocator.destroy(self);
            return null;
        };
        const entry = try adapter_config.builtinOpencode(arena, environ);
        self.launch = .{
            .executable = executable,
            .environment = entry.environment,
            .log_path = try std.fs.path.join(arena, &.{ home, ".oapx", "opencode", "tui.log" }),
        };
        return self;
    }

    pub fn destroy(self: *Route) void {
        if (self.server) |server| server.deinit();
        for (self.chosen) |held| self.allocator.free(held);
        self.allocator.free(self.refused);
        self.held.deinit();
        const allocator = self.allocator;
        self.* = undefined;
        allocator.destroy(self);
    }

    pub fn away(self: *Route) oap_execution.Away {
        return .{ .ctx = self, .open = open };
    }

    fn ensureServer(self: *Route) !void {
        while (!self.lock.tryLock()) std.atomic.spinLoopHint();
        defer self.lock.unlock();
        if (self.server != null) return;
        if (self.refused.len > 0) return error.OpenCodeUnavailable;
        var scratch = std.heap.ArenaAllocator.init(self.allocator);
        defer scratch.deinit();
        var why: []const u8 = "";
        const server = opencode_server.Server.start(self.allocator, scratch.allocator(), self.launch, &why) catch |err| {
            if (err != error.ServerRefused) return err;
            self.refused = try self.allocator.dupe(u8, why);
            return error.OpenCodeUnavailable;
        };
        self.server = server;
        self.adapter.config.endpoint = server.endpoint;
        self.adapter.config.username = opencode_server.username;
        self.adapter.config.password = &server.password;
    }

    fn open(ctx: *anyopaque, model: ai_types.Model) anyerror!oap_execution.Served {
        const self: *Route = @ptrCast(@alignCast(ctx));
        try self.ensureServer();
        try self.choose(model);
        return self.adapter.adapter();
    }

    fn choose(self: *Route, model: ai_types.Model) !void {
        const split = std.mem.indexOfScalar(u8, model.id, '/') orelse return error.ModelNotFound;
        const provider_id = try self.allocator.dupe(u8, model.id[0..split]);
        errdefer self.allocator.free(provider_id);
        const model_id = try self.allocator.dupe(u8, model.id[split + 1 ..]);
        for (self.chosen) |held| self.allocator.free(held);
        self.chosen = .{ provider_id, model_id };
        self.adapter.config.model = .{ .id = model_id, .provider_id = provider_id };
    }

    pub fn models(self: *Route, allocator: std.mem.Allocator) ![]ai_types.Model {
        try self.ensureServer();
        var scratch = std.heap.ArenaAllocator.init(allocator);
        defer scratch.deinit();
        const arena = scratch.allocator();
        const server = self.server.?;
        const target = try client.parseEndpoint(arena, server.endpoint);
        const endpoint = httpapi.Endpoint{ .base_path = target.base_path, .username = opencode_server.username, .password = &server.password };
        const started = compat.time.monotonicNanos() catch 0;
        while (true) {
            const response = try client.roundTrip(allocator, arena, target, try httpapi.models(arena, endpoint), httpapi.default_frame_limit, 10 * std.time.ns_per_s);
            if (response.status != 200) return error.OpenCodeModelsRefused;
            const listed = try std.json.parseFromSliceLeaky(Listed, arena, response.body, .{ .ignore_unknown_fields = true });
            if (listed.data.len > 0) return modelsOf(allocator, listed);
            if ((compat.time.monotonicNanos() catch 0) -| started > listing_wait_ns) return &.{};
            compat.time.sleepNs(listing_poll_ns);
        }
    }
};

fn modelsOf(allocator: std.mem.Allocator, listed: Listed) ![]ai_types.Model {
    var out: std.ArrayList(ai_types.Model) = .empty;
    errdefer {
        for (out.items) |*model| model.deinit(allocator);
        out.deinit(allocator);
    }
    var scratch = std.heap.ArenaAllocator.init(allocator);
    defer scratch.deinit();
    for (listed.data) |row| {
        if (!row.enabled) continue;
        const shaped = ai_types.Model{
            .id = try std.fmt.allocPrint(scratch.allocator(), "{s}/{s}", .{ row.providerID, row.id }),
            .name = if (row.name.len > 0) row.name else row.id,
            .api = oap_execution.away_api,
            .provider = oap_execution.away_api,
            .base_url = "",
            .reasoning = false,
            .input = if (row.capabilities.input.len > 0) row.capabilities.input else &.{"text"},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = @intCast(@min(row.limit.context, std.math.maxInt(u32))),
            .max_tokens = @intCast(@min(row.limit.output, std.math.maxInt(u32))),
        };
        try out.ensureUnusedCapacity(allocator, 1);
        out.appendAssumeCapacity(try ai_types.cloneModel(allocator, shaped));
    }
    return out.toOwnedSlice(allocator);
}

const testing = std.testing;

test "OpenCode's listed models become its route's models, named by provider and id, without what it does not list" {
    const body =
        \\{"location":{"directory":"/w"},"data":[
        \\{"id":"mimo-v2.6-flash-free","providerID":"opencode","name":"MiMo","settings":{"apiKey":"public"},"limit":{"context":200000,"output":32000},"capabilities":{"input":["text","image"]}},
        \\{"id":"off","providerID":"opencode","enabled":false}]}
    ;
    var scratch = std.heap.ArenaAllocator.init(testing.allocator);
    defer scratch.deinit();
    const listed = try std.json.parseFromSliceLeaky(Listed, scratch.allocator(), body, .{ .ignore_unknown_fields = true });
    const models = try modelsOf(testing.allocator, listed);
    defer {
        for (models) |*model| model.deinit(testing.allocator);
        testing.allocator.free(models);
    }
    try testing.expectEqual(@as(usize, 1), models.len);
    try testing.expectEqualStrings("opencode/mimo-v2.6-flash-free", models[0].id);
    try testing.expectEqualStrings(oap_execution.away_api, models[0].api);
    try testing.expectEqual(@as(u32, 200_000), models[0].context_window);
    try testing.expectEqual(@as(u32, 32_000), models[0].max_tokens);
    try testing.expectEqualStrings("image", models[0].input[1]);
}

fn bareRoute() !*Route {
    const route = try testing.allocator.create(Route);
    route.* = .{ .allocator = testing.allocator, .held = .init(testing.allocator), .launch = undefined, .adapter = opencode_adapter.Adapter.init(testing.allocator, .{ .endpoint = "" }) };
    return route;
}

test "a chosen model is the one OpenCode's sessions are created on, by its provider and its own id" {
    const route = try bareRoute();
    defer route.destroy();
    try route.choose(models_fixture);
    try testing.expectEqualStrings("opencode", route.adapter.config.model.?.provider_id);
    try testing.expectEqualStrings("mimo-v2.6-flash-free", route.adapter.config.model.?.id);
    var bare = models_fixture;
    bare.id = "no-provider";
    try testing.expectError(error.ModelNotFound, route.choose(bare));
}

test "an OpenCode that was refused once is not started again" {
    const route = try bareRoute();
    defer route.destroy();
    route.refused = try testing.allocator.dupe(u8, "OpenCode 2.0.24 has not been tested");
    try testing.expectError(error.OpenCodeUnavailable, Route.open(route, models_fixture));
    try testing.expect(route.server == null);
}

const models_fixture = ai_types.Model{
    .id = "opencode/mimo-v2.6-flash-free",
    .name = "MiMo",
    .api = oap_execution.away_api,
    .provider = oap_execution.away_api,
    .base_url = "",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 200_000,
    .max_tokens = 32_000,
};
