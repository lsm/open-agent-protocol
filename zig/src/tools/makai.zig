const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const api_registry = @import("api_registry");
const register_builtins = @import("register_builtins");
const auth_protocol_server = @import("auth_server");
const auth_cli = @import("auth_cli");
const oauth_storage = @import("oauth/storage");
const event_stream = @import("event_stream");
const agent_loop = @import("agent_loop");
const agent_bridge = @import("agent_bridge");
const transport = @import("transport");
const model_ref = @import("model_ref");
const in_process = @import("transports/in_process");
const stdio = @import("stdio");
const tui_app = @import("tui_app");
const model_catalog = @import("model_catalog");
const provider_base_url = @import("provider_base_url");
const provider_catalog = @import("provider_catalog");
const auth_providers = @import("auth/providers");

const kimi_provider_id = "kimi";
const kimi_china_base_url = provider_catalog.baseUrlOrCompileError(kimi_provider_id, "openai-completions", "china");
const kimi_global_base_url = provider_catalog.baseUrlOrCompileError(kimi_provider_id, "openai-completions", "global");
const oap_conformance = @import("oap_conformance");
const oap_auth_adapter = @import("oap_auth_adapter");
const agent_oap_provider_bridge = @import("agent_oap_provider_bridge");
const oap_remote_provider_transport = @import("oap_remote_provider_transport");
const oap_provider_http_policy = @import("oap_provider_http_policy");
const pre_transform = @import("pre_transform");
const semantic = @import("semantic");
const provider_semantic = @import("provider_semantic");
const validator = @import("validator");
const oap_provider_types = @import("oap_provider_types");
const oap_provider_envelope = @import("oap_provider_envelope");
const oap_provider_server = @import("oap_provider_server");
const oap_provider_catalog = @import("oap_provider_catalog");
const oap_provider_runtime = @import("oap_provider_runtime");
const oap_provider_grant_channel = @import("oap_provider_grant_channel");
const auth_resolver = @import("auth_resolver");
const oap_types = @import("oap_types");
const adapter_endpoint = @import("adapter_endpoint");
const adapter_contract = @import("adapter_contract");
const adapter_config = @import("adapter_config");
const claude_adapter = @import("claude_adapter");
const codex_adapter = @import("codex_adapter");
const acp_adapter = @import("acp_adapter");
const pi_adapter = @import("pi_adapter");
const deepseek_adapter = @import("deepseek_adapter");
const opencode_adapter = @import("opencode_adapter");
const hermes_adapter = @import("hermes_adapter");
const memory_adapter = @import("memory_adapter");
const oapx_adapter = @import("oapx_adapter");
const hub = @import("hub");
const hub_stdio = @import("hub_stdio");
const hub_daemon = @import("hub_daemon");
const hub_http = @import("hub_http");
const bounded_output = @import("bounded_output");
const endpoint_signals = @import("endpoint_signals");

pub const VERSION = @import("version_options").version;

const AuthProtocolServer = auth_protocol_server.AuthProtocolServer;
const STDIO_IDLE_SLEEP_NS = std.time.ns_per_ms;
const STDIO_THREAD_JOIN_TIMEOUT_MS: u64 = 5_000;

fn kimiContextWindow() !u32 {
    return provider_catalog.rowContextWindow(kimi_provider_id) orelse error.KimiRowHasNoContextWindow;
}

fn kimiMaxTokens() !u32 {
    return provider_catalog.rowMaxTokens(kimi_provider_id) orelse error.KimiRowHasNoMaxTokens;
}

fn kimiDefaultRegion() []const u8 {
    return provider_catalog.defaultRegion(kimi_provider_id) orelse "china";
}

fn kimiRegionFromProviderData(provider_data: []const u8) []const u8 {
    if (std.mem.startsWith(u8, provider_data, "region:")) {
        return provider_catalog.regionFromValue(kimi_provider_id, provider_data["region:".len..]) orelse kimiDefaultRegion();
    }
    return kimiDefaultRegion();
}

fn loadStoredKimiRegion(allocator: std.mem.Allocator) ?[]const u8 {
    var storage = oauth_storage.AuthStorage.loadDefaultStoredOnly(allocator) catch return null;
    defer storage.deinit();
    const auth = storage.providers.get(kimi_provider_id) orelse return null;
    return switch (auth) {
        .api_key => null,
        .oauth => |creds| if (creds.provider_data) |data| kimiRegionFromProviderData(data) else null,
    };
}

fn resolvePrintKimiRegion(allocator: std.mem.Allocator, use_storage_auth: bool) []const u8 {
    if (provider_catalog.regionEnv(kimi_provider_id)) |name| {
        const region_env = compat.getEnvVarOwned(allocator, name) catch null;
        if (region_env) |env| {
            defer allocator.free(env);
            if (provider_catalog.regionFromValue(kimi_provider_id, env)) |region| return region;
        }
    }
    if (use_storage_auth) {
        if (loadStoredKimiRegion(allocator)) |region| return region;
    }
    return kimiDefaultRegion();
}

const defaultBaseUrlForRef = provider_base_url.defaultBaseUrlForRef;
const isReasoningModelRef = provider_base_url.isReasoningModelRef;

fn isResponsesOnlyModel(model_id: []const u8) bool {
    return provider_catalog.isResponsesOnlyModel(model_id);
}

const transparentProxyCompat = provider_base_url.transparentProxyCompat;

fn modelFromCanonicalRef(allocator: std.mem.Allocator, ref: []const u8) !ai_types.Model {
    var parsed = model_ref.parseModelRef(allocator, ref) catch return error.InvalidModelRef;
    errdefer parsed.deinit(allocator);

    if (oap_provider_types.parseModelRef(ref)) |oap_ref| {
        if (try servedOapModel(allocator, oap_ref.provider_id, oap_ref.wire, oap_ref.wire_id, oap_ref.model_id)) |served| {
            parsed.deinit(allocator);
            return served;
        }
        const api = oap_provider_catalog.apiForWire(oap_ref.wire, oap_ref.wire_id) orelse return error.InvalidModelRef;
        if (provider_catalog.provider(oap_ref.provider_id) != null and !provider_catalog.declaresWire(oap_ref.provider_id, api)) return error.InvalidModelRef;
        const owned_api = try allocator.dupe(u8, api);
        allocator.free(parsed.api);
        parsed.api = owned_api;
    }

    if (try modelFromProductionCatalog(allocator, parsed)) |model| {
        parsed.deinit(allocator);
        return model;
    }

    const name = try allocator.dupe(u8, parsed.model_id);
    errdefer allocator.free(name);

    const base_url = if (std.mem.eql(u8, parsed.provider_id, "openai") and
        std.mem.eql(u8, parsed.api, "openai-completions") and
        isResponsesOnlyModel(parsed.model_id))
        try allocator.dupe(u8, "")
    else
        try defaultBaseUrlForRef(allocator, parsed.provider_id, parsed.api);
    errdefer allocator.free(base_url);

    const input = try allocator.alloc([]const u8, 0);
    errdefer allocator.free(input);

    const compat_options = try transparentProxyCompat(allocator, parsed.provider_id);

    const model = ai_types.Model{
        .id = parsed.model_id,
        .name = name,
        .api = parsed.api,
        .provider = parsed.provider_id,
        .base_url = base_url,
        .reasoning = isReasoningModelRef(parsed.provider_id, parsed.model_id),
        .input = input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 4_096,
        .compat = compat_options,
        .is_owned = true,
    };
    parsed.provider_id = &.{};
    parsed.api = &.{};
    parsed.model_id = &.{};
    return model;
}

fn modelFromProductionCatalog(
    allocator: std.mem.Allocator,
    parsed: model_ref.ParsedModelRef,
) !?ai_types.Model {
    const models = model_catalog.loadProductionModels(allocator) catch |err| {
        if (err == error.OutOfMemory) return err;
        return null;
    };
    defer model_catalog.deinitModels(allocator, models);

    for (models) |model| {
        if (!std.mem.eql(u8, model.provider, parsed.provider_id)) continue;
        if (!std.mem.eql(u8, model.api, parsed.api)) continue;
        if (!std.mem.eql(u8, model.id, parsed.model_id)) continue;
        return try ai_types.cloneModel(allocator, model);
    }

    return null;
}

const StdoutSink = struct {
    file: std.Io.File,
    io: std.Io,

    fn write(context: *anyopaque, line: []const u8) anyerror!void {
        const self: *StdoutSink = @ptrCast(@alignCast(context));
        try self.file.writeStreamingAll(self.io, line);
        try self.file.writeStreamingAll(self.io, "\n");
    }
};

fn wallClockNanoseconds() u64 {
    return @intCast(compat.time.nowNanos());
}

fn unavailable(
    stderr: std.Io.File,
    surface: []const u8,
    flag: []const u8,
    reason: []const u8,
) error{Unavailable}!void {
    var buffer: [512]u8 = undefined;
    const message = std.fmt.bufPrint(&buffer, "oapx {s}: {s}: unavailable: {s}\n", .{ surface, flag, reason }) catch "oapx: unavailable\n";
    compat.stdio.writeAll(stderr, message) catch {};
    return error.Unavailable;
}

const HubRegistry = struct {
    allocator: std.mem.Allocator,
    environ: *const std.process.Environ.Map,
    surface: ConfigSurface,
    production: ?*tui_app.ProductionRuntime = null,
    memories: std.ArrayList(*memory_adapter.Adapter) = .empty,
    claudes: std.ArrayList(*claude_adapter.Adapter) = .empty,
    codexes: std.ArrayList(*codex_adapter.Adapter) = .empty,
    hermeses: std.ArrayList(*hermes_adapter.Adapter) = .empty,

    fn deinit(self: *HubRegistry) void {
        for (self.claudes.items) |claude| claude.deinit();
        self.claudes.deinit(self.allocator);
        for (self.codexes.items) |codex| codex.deinit();
        self.codexes.deinit(self.allocator);
        for (self.hermeses.items) |hermes| hermes.deinit();
        self.hermeses.deinit(self.allocator);
        if (self.production) |production| {
            production.deinit();
            self.allocator.destroy(production);
        }
        for (self.memories.items) |memory| memory.deinit();
        self.memories.deinit(self.allocator);
        self.* = undefined;
    }

    fn runtime(self: *HubRegistry) !*tui_app.ProductionRuntime {
        if (self.production) |production| return production;
        const production = try self.allocator.create(tui_app.ProductionRuntime);
        errdefer self.allocator.destroy(production);
        production.* = try tui_app.ProductionRuntime.init(self.allocator, .{});
        production.initBridge();
        self.production = production;
        return production;
    }

    fn build(context: *anyopaque, arena: std.mem.Allocator, entry: adapter_config.AdapterEntry) adapter_contract.Failure!adapter_contract.Adapter {
        const self: *HubRegistry = @ptrCast(@alignCast(context));
        if (std.mem.eql(u8, entry.kind, "claude")) {
            const config = claudeBackendConfig(self.surface, arena, entry, self.environ) catch |failure| return self.reported(failure);
            const built = try arena.create(claude_adapter.Adapter);
            built.* = claude_adapter.Adapter.init(self.allocator, config);
            try self.claudes.append(self.allocator, built);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "codex")) {
            const config = codexBackendConfig(self.surface, arena, entry, self.environ) catch |failure| return self.reported(failure);
            const built = try arena.create(codex_adapter.Adapter);
            built.* = codex_adapter.Adapter.init(self.allocator, config);
            try self.codexes.append(self.allocator, built);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "pi")) {
            const config = piBackendConfig(self.surface, arena, entry, self.environ) catch |failure| return self.reported(failure);
            const built = try arena.create(pi_adapter.Adapter);
            built.* = pi_adapter.Adapter.init(self.allocator, config);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "acp")) {
            const config = acpBackendConfig(self.surface, arena, entry, self.environ) catch |failure| return self.reported(failure);
            const built = try arena.create(acp_adapter.Adapter);
            built.* = acp_adapter.Adapter.init(self.allocator, config);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "deepseek")) {
            const config = deepseekBackendConfig(self.surface, arena, entry, self.environ) catch |failure| return self.reported(failure);
            const built = try arena.create(deepseek_adapter.Adapter);
            built.* = deepseek_adapter.Adapter.init(self.allocator, config);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "opencode")) {
            const config = opencodeBackendConfig(self.surface, arena, entry) catch |failure| return self.reported(failure);
            const built = try arena.create(opencode_adapter.Adapter);
            built.* = opencode_adapter.Adapter.init(self.allocator, config);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "hermes")) {
            const config = hermesBackendConfig(self.surface, arena, entry, self.environ) catch |failure| return self.reported(failure);
            const built = try arena.create(hermes_adapter.Adapter);
            built.* = hermes_adapter.Adapter.init(self.allocator, config);
            try self.hermeses.append(self.allocator, built);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "memory")) {
            const built = try arena.create(memory_adapter.Adapter);
            built.* = memory_adapter.Adapter.init(self.allocator);
            try self.memories.append(self.allocator, built);
            return built.adapter();
        }
        if (std.mem.eql(u8, entry.kind, "oapx")) {
            const production = self.runtime() catch |failure| return self.reported(failure);
            const built = try arena.create(oapx_adapter.Adapter);
            built.* = oapx_adapter.Adapter.init(self.allocator, production.options());
            return built.adapter();
        }
        return self.refuse(entry);
    }

    fn reported(self: *HubRegistry, failure: anyerror) adapter_contract.Failure {
        _ = self;
        if (failure == error.OutOfMemory) return error.OutOfMemory;
        return error.Unavailable;
    }

    fn refuse(self: *HubRegistry, entry: adapter_config.AdapterEntry) adapter_contract.Failure {
        self.surface.refuse("{s} \"{s}\" is of type \"{s}\", which oapx does not know; it serves claude, codex, pi, acp, hermes, deepseek, opencode, memory and oapx", .{ self.surface.noun, entry.name, entry.kind }) catch {};
        return error.Unavailable;
    }
};

fn hubConfiguredSources(
    arena: std.mem.Allocator,
    configured: []const adapter_config.ToolSource,
) ![]const adapter_contract.ConfiguredSource {
    const sources = try arena.alloc(adapter_contract.ConfiguredSource, configured.len);
    for (configured, sources) |source, *slot| {
        slot.* = .{
            .id = source.id,
            .kind = source.kind,
            .display_name = source.display_name,
            .protocol = source.protocol,
            .endpoint = source.endpoint,
            .command = source.command,
            .args = source.args,
            .environment = source.environment,
        };
    }
    return sources;
}

fn hubBindRefusal(stderr: std.Io.File, message: []const u8) error{InvalidHubOption} {
    compat.stdio.writeAll(stderr, "oapx serve: ") catch {};
    compat.stdio.writeAll(stderr, message) catch {};
    compat.stdio.writeAll(stderr, "\n") catch {};
    return error.InvalidHubOption;
}
const hub_accept_poll_ms: i32 = 10;

const keepGoing = hub_http.KeepGoing{ .context = undefined, .check = hubSignalled };

fn hubSignalled(_: *const anyopaque) bool {
    return !endpoint_signals.received();
}

fn runHubHttp(
    allocator: std.mem.Allocator,
    arena: std.mem.Allocator,
    core: *hub.Hub,
    bind: []const u8,
    served: []const u8,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !void {
    const wanted = hub_http.parseBind(bind) catch |failure| return hubBindRefusal(stderr, switch (failure) {
        error.NoPort => "--addr names no port; write host:port, as 127.0.0.1:6270",
        error.NoHost => "--addr names no host; write one, as 127.0.0.1:6270 or [::1]:6270",
        error.UnclosedBracket => "--addr opens a bracket and closes none; write an IPv6 host as [::1]:6270",
        error.UnbracketedIpv6 => "--addr cannot tell its port from an unbracketed IPv6 host; write it as [::1]:6270",
        error.NotAPort => "--addr names a port that is not a number; write host:port, as 127.0.0.1:6270",
    });
    const allow = hub_http.loopbackHosts(bind);
    const address = try compat.net.resolveAddress(arena, wanted.host, wanted.port);
    var listener = try compat.net.tcpListen(address, .{ .reuse_address = true });
    defer compat.net.closeServer(&listener);
    try compat.stdio.writeAll(stdout, "listening on http://");
    try compat.stdio.writeAll(stdout, try hub_http.addressText(arena, compat.net.listenAddress(&listener)));
    try compat.stdio.writeAll(stdout, "\n");
    try compat.stdio.writeAll(stderr, "oapx: serving adapters: ");
    try compat.stdio.writeAll(stderr, served);
    try compat.stdio.writeAll(stderr, " (restart kills all sessions)\n");
    if (allow == null) {
        try compat.stdio.writeAll(stderr, "oapx: this bind is not loopback; the single-user model is opted out of\n");
    }
    if (comptime !hub_http.pollable) {
        try compat.stdio.writeAll(stderr, "oapx: this platform cannot wait on a socket, so a stalled client is not given up on and a signal ends the process rather than the hub; the Windows path is #460\n");
    }

    var daemon = try hub_daemon.Daemon.init(allocator, core, allow orelse &.{}, keepGoing);
    defer daemon.deinit();
    const failed = hub_daemon.serveListener(&daemon, &listener, hub_accept_poll_ms);
    if (failed) |failure| {
        sweepHubSessions(core, stderr);
        try compat.stdio.writeAll(stderr, "oapx: stopped\n");
        return failure;
    }
    try compat.stdio.writeAll(stderr, "oapx: shutting down\n");
    sweepHubSessions(core, stderr);
    try compat.stdio.writeAll(stderr, "oapx: stopped\n");
}

fn hubTakesSignals() bool {
    return @import("builtin").os.tag != .windows;
}

fn sweepHubSessions(core: *hub.Hub, stderr: std.Io.File) void {
    const summary = core.closeSessions();
    if (summary.clean()) return;
    var buffer: [256]u8 = undefined;
    const message = std.fmt.bufPrint(&buffer, "oapx: shutdown: closed {d} of {d} sessions; {d} still held a run after {d} attempts, {d} were never reached; the rest were torn down\n", .{
        summary.closed,
        summary.sessions,
        summary.refused,
        summary.refused_attempts,
        summary.unattempted,
    }) catch "oapx: shutdown: a session was not closed cleanly\n";
    compat.stdio.writeAll(stderr, message) catch {};
}

fn runHub(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !void {
    var over_stdio = false;
    var config: ?[]const u8 = null;
    var addr: ?[]const u8 = null;
    var history_path: ?[]const u8 = null;
    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const argument = args[index];
        if (std.mem.eql(u8, argument, "--session-history")) {
            index += 1;
            if (index >= args.len) return error.InvalidHubOption;
            history_path = args[index];
        } else if (std.mem.startsWith(u8, argument, "--session-history=")) {
            history_path = argument["--session-history=".len..];
        } else if (std.mem.eql(u8, argument, "--stdio")) {
            over_stdio = true;
        } else if (std.mem.eql(u8, argument, "--config")) {
            index += 1;
            if (index >= args.len) return error.InvalidHubOption;
            config = args[index];
        } else if (std.mem.startsWith(u8, argument, "--config=")) {
            config = argument["--config=".len..];
        } else if (std.mem.eql(u8, argument, "--addr")) {
            index += 1;
            if (index >= args.len) return error.InvalidHubOption;
            addr = args[index];
        } else if (std.mem.startsWith(u8, argument, "--addr=")) {
            addr = argument["--addr=".len..];
        } else {
            try compat.stdio.writeAll(stderr, "oapx serve: unknown flag\n\n");
            return error.InvalidHubOption;
        }
    }
    if (over_stdio and addr != null) {
        try compat.stdio.writeAll(stderr, "oapx serve: --stdio takes no listen address; --addr and --stdio are mutually exclusive\n");
        return error.InvalidHubOption;
    }

    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var environ = try compat.createEnvMap(arena);
    const surface = withSurface(hub_config_surface, arena, stderr);
    var file = adapter_config.File{};
    if (config) |path| {
        const bytes = compat.fs.readFileAlloc(arena, compat.fs.getCwd(), path, backend_config_read_limit) catch |err| {
            try surface.refuse("cannot read --config {s}: {s}", .{ path, @errorName(err) });
            return error.BackendRefused;
        };
        var diagnostic = adapter_config.Diagnostic{};
        file = adapter_config.parse(arena, bytes, &environ, &diagnostic) catch |err| {
            if (err != error.ConfigInvalid) return err;
            try surface.refuse("{s}: {s}", .{ path, diagnostic.message });
            return error.BackendRefused;
        };
    }
    const tool_sources = try hubConfiguredSources(arena, file.tool_sources);
    var registry = HubRegistry{ .allocator = allocator, .environ = &environ, .surface = surface };
    defer registry.deinit();
    var bindings: ?hub.binding.Store = null;
    defer if (bindings) |*store| store.deinit();
    const history = history_path orelse if (compat.getEnvVarOwned(arena, "HOME")) |home| try std.fs.path.join(arena, &.{ home, ".oapx", "sessions.jsonl" }) else |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => "",
    };
    if (history.len > 0) {
        bindings = hub.binding.Store.open(allocator, history) catch |err| {
            if (err == error.OutOfMemory) return error.OutOfMemory;
            if (err == error.SessionHistoryInUse) {
                try surface.refuse("another oapx hub is using the session history {s}; pass --session-history <path> to give this one its own", .{history});
            } else {
                try surface.refuse("cannot open --session-history {s}: {s}", .{ history, @errorName(err) });
            }
            return error.BackendRefused;
        };
    }
    var core = hub.Hub.init(allocator, wallClockNanoseconds, .{ .tool_sources = tool_sources, .bindings = if (bindings) |*store| store else null });
    defer core.deinit();
    if (config == null) {
        const memory = try arena.create(memory_adapter.Adapter);
        memory.* = memory_adapter.Adapter.init(allocator);
        try registry.memories.append(allocator, memory);
        try core.register("memory", memory.adapter());
    } else {
        var load_diagnostic = adapter_config.Diagnostic{};
        core.load(arena, file, .{ .context = &registry, .make = HubRegistry.build }, &load_diagnostic) catch |failure| {
            if (failure != error.ConfigRefused) return failure;
            try surface.refuse("{s}", .{load_diagnostic.message});
            return error.BackendRefused;
        };
    }

    if (hubTakesSignals()) {
        endpoint_signals.install() catch {
            try compat.stdio.writeAll(stderr, "oapx serve: the process cannot take a signal handler; refusing to serve a hub that cannot be stopped\n");
            return error.BackendRefused;
        };
    } else {
        try compat.stdio.writeAll(stderr, "oapx serve: a console interrupt ends this process rather than the hub; the Windows path is #460\n");
    }

    const served = try std.mem.join(arena, ", ", try core.names(arena));
    if (!over_stdio) return runHubHttp(allocator, arena, &core, addr orelse hub_http.defaultBind(), served, stdout, stderr);

    var sink = StdoutSink{ .file = stdout, .io = hubIo() };
    var frontend = try hub_stdio.Frontend.init(allocator, &core, .{ .context = &sink, .write = StdoutSink.write }, .{});
    defer frontend.deinit();
    try compat.stdio.writeAll(stderr, "oapx: serving adapters over stdio: ");
    try compat.stdio.writeAll(stderr, served);
    try compat.stdio.writeAll(stderr, " (exit kills all sessions)\n");
    var input = stdin;
    hub_stdio.serve(allocator, &frontend, .{
        .read = readStdin,
        .context = &input,
        .readable = if (hubTakesSignals()) input.handle else null,
        .stop = endpoint_signals.received,
    }) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        error.StdinFailed, error.FrameLimitTooSmall => {
            if (frontend.defect()) |line| {
                var buffer: [640]u8 = undefined;
                const message = std.fmt.bufPrint(&buffer, "oapx: framing defect, stopped serving: {s}{s}\n", .{
                    line,
                    if (frontend.recorded.cut) " (cut)" else "",
                }) catch "oapx: framing defect, stopped serving\n";
                compat.stdio.writeAll(stderr, message) catch {};
            } else {
                compat.stdio.writeAll(stderr, "oapx: framing defect, stopped serving\n") catch {};
            }
            sweepHubSessions(&core, stderr);
            return error.FramingDefect;
        },
        error.InputFailed => {
            sweepHubSessions(&core, stderr);
            try compat.stdio.writeAll(stderr, "oapx: the request stream failed, stopped serving\n");
            return error.InputFailed;
        },
        error.OutputStalled => {
            sweepHubSessions(&core, stderr);
            try compat.stdio.writeAll(stderr, "oapx: the host stopped reading, stopped serving\n");
            return error.OutputStalled;
        },
        else => return err,
    };
    if (endpoint_signals.received()) try compat.stdio.writeAll(stderr, "oapx: shutting down\n");
    sweepHubSessions(&core, stderr);
    try compat.stdio.writeAll(stderr, "oapx: stopped\n");
}

fn hubIo() std.Io {
    return if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
}

fn readStdin(context: *anyopaque, buffer: []u8) anyerror!usize {
    const file: *std.Io.File = @ptrCast(@alignCast(context));
    return file.readStreaming(hubIo(), &.{buffer});
}

fn runServe(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !void {
    if (args.len == 0) {
        try compat.stdio.writeAll(stderr, "serve takes a role, agent or provider\n\n");
        return error.InvalidServeRole;
    }
    if (isCombinedServeRole(args[0])) {
        return runOapMode(allocator, args[1..], stdin, stdout, stderr, true);
    }
    const role = serveRole(args[0]) orelse {
        var buf: [256]u8 = undefined;
        const msg = try std.fmt.bufPrint(&buf, "serve takes a role, agent or provider: {s}\n\n", .{args[0]});
        try compat.stdio.writeAll(stderr, msg);
        return error.InvalidServeRole;
    };
    return switch (role) {
        .agent => runOapMode(allocator, args[1..], stdin, stdout, stderr, false),
        .provider => runServeProvider(allocator, args[1..], stdin, stdout, stderr),
    };
}

const ServeRole = enum { agent, provider };

fn isCombinedServeRole(name: []const u8) bool {
    return std.mem.eql(u8, name, "agent,provider") or std.mem.eql(u8, name, "provider,agent");
}

fn serveRole(name: []const u8) ?ServeRole {
    if (std.mem.eql(u8, name, "agent")) return .agent;
    if (std.mem.eql(u8, name, "provider")) return .provider;
    return null;
}

fn runServeProvider(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !void {
    var answers_specimens = false;
    var http_bind: ?[]const u8 = null;
    var stdio_selected = false;
    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const arg = args[index];
        if (std.mem.eql(u8, arg, "--specimens")) {
            answers_specimens = true;
            continue;
        }
        if (std.mem.eql(u8, arg, "--stdio")) {
            stdio_selected = true;
            continue;
        }
        if (std.mem.eql(u8, arg, "--http")) {
            if (http_bind != null) return error.InvalidServeOption;
            index += 1;
            if (index >= args.len) return error.MissingHttpBind;
            http_bind = args[index];
            continue;
        }
        var buf: [256]u8 = undefined;
        const msg = try std.fmt.bufPrint(&buf, "invalid serve provider option: {s}\n\n", .{arg});
        try compat.stdio.writeAll(stderr, msg);
        return error.InvalidServeOption;
    }
    if (http_bind) |bind| {
        if (answers_specimens or stdio_selected) return error.InvalidServeOption;
        return runOapProviderHttpMode(allocator, bind);
    }
    return runOapProviderMode(allocator, stdin, stdout, stderr, answers_specimens);
}

const validate_read_limit = 64 * 1024 * 1024;

const provider_profile = "open-agent-protocol.model-provider-core";

fn namesProviderProfile(trace: []std.json.Value) bool {
    for (trace) |envelope| {
        if (envelope != .object) continue;
        const declared = envelope.object.get("profile") orelse continue;
        if (declared == .string and std.mem.eql(u8, declared.string, provider_profile)) return true;
    }
    return false;
}

const ValidatePhase = enum { decode, schema, semantic };

const ValidateFinding = struct {
    phase: ValidatePhase,
    code: []const u8,
    index: usize,
    line: usize = 0,
};

const TraceItem = struct {
    raw: []const u8,
    value: std.json.Value,
    line: usize,
};

const ValidateFormat = enum { human, json };

const ConformanceFormat = enum { text, json };

fn writeConformanceString(allocator: std.mem.Allocator, out: *std.ArrayList(u8), value: []const u8) !void {
    const quoted = try std.json.Stringify.valueAlloc(allocator, value, .{});
    defer allocator.free(quoted);
    try out.appendSlice(allocator, quoted);
}

fn runConformance(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !bool {
    var format: ConformanceFormat = .text;
    var command: ?[]const u8 = null;
    var endpoint_args = std.ArrayList([]const u8).empty;
    defer endpoint_args.deinit(allocator);
    var environment = std.ArrayList([]const u8).empty;
    defer environment.deinit(allocator);
    var session: []const u8 = "conformance";
    var probe_budget_ms: i64 = oap_conformance.default_probe_budget_ms;
    var exit_grace_ms: i64 = oap_conformance.default_exit_grace_ms;

    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const arg = args[index];
        if (std.mem.eql(u8, arg, "--command")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            command = args[index];
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--command=")) {
            command = arg["--command=".len..];
            continue;
        }
        if (std.mem.eql(u8, arg, "--format")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            format = std.meta.stringToEnum(ConformanceFormat, args[index]) orelse return error.InvalidArgument;
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--format=")) {
            format = std.meta.stringToEnum(ConformanceFormat, arg["--format=".len..]) orelse return error.InvalidArgument;
            continue;
        }
        if (std.mem.eql(u8, arg, "--env")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            try environment.append(allocator, args[index]);
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--env=")) {
            try environment.append(allocator, arg["--env=".len..]);
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--exit-grace-ms=")) {
            exit_grace_ms = std.fmt.parseInt(i64, arg["--exit-grace-ms=".len..], 10) catch return error.InvalidArgument;
            if (exit_grace_ms <= 0) return error.InvalidArgument;
            continue;
        }
        if (std.mem.eql(u8, arg, "--exit-grace-ms")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            exit_grace_ms = std.fmt.parseInt(i64, args[index], 10) catch return error.InvalidArgument;
            if (exit_grace_ms <= 0) return error.InvalidArgument;
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--session=")) {
            session = arg["--session=".len..];
            continue;
        }
        if (std.mem.eql(u8, arg, "--session")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            session = args[index];
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--timeout-ms=")) {
            probe_budget_ms = std.fmt.parseInt(i64, arg["--timeout-ms=".len..], 10) catch return error.InvalidArgument;
            if (probe_budget_ms <= 0) return error.InvalidArgument;
            continue;
        }
        if (std.mem.eql(u8, arg, "--timeout-ms")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            probe_budget_ms = std.fmt.parseInt(i64, args[index], 10) catch return error.InvalidArgument;
            if (probe_budget_ms <= 0) return error.InvalidArgument;
            continue;
        }
        try endpoint_args.append(allocator, arg);
    }

    const named = command orelse {
        try compat.stdio.writeAll(stderr, "conformance: --command CMD is required; there is no built-in endpoint to drive\n");
        return true;
    };

    var report = oap_conformance.run(allocator, .{
        .command = named,
        .args = endpoint_args.items,
        .environment = environment.items,
        .session_id = session,
        .probe_budget_ms = probe_budget_ms,
        .exit_grace_ms = exit_grace_ms,
    }) catch |err| {
        try compat.stdio.writeAll(stderr, try std.fmt.allocPrint(allocator, "conformance: {s}\n", .{@errorName(err)}));
        return true;
    };
    defer report.deinit();

    var out = std.ArrayList(u8).empty;
    defer out.deinit(allocator);
    if (format == .json) try out.appendSlice(allocator, "{\"endpoint\":");
    if (format == .json) try writeConformanceString(allocator, &out, report.endpoint);
    if (format == .json) try out.appendSlice(allocator, ",\"checks\":[");
    for (report.checks.items, 0..) |check, position| {
        if (position != 0) try out.appendSlice(allocator, if (format == .json) "," else "\n");
        if (format == .json) {
            try out.appendSlice(allocator, "{\"name\":");
            try writeConformanceString(allocator, &out, check.name);
            try out.appendSlice(allocator, ",\"passed\":");
            try out.appendSlice(allocator, if (check.passed) "true" else "false");
            if (check.skipped) try out.appendSlice(allocator, ",\"skipped\":true");
            if (check.detail.len != 0) {
                try out.appendSlice(allocator, ",\"detail\":");
                try writeConformanceString(allocator, &out, check.detail);
            }
            try out.appendSlice(allocator, "}");
        } else {
            try out.appendSlice(allocator, if (check.passed) "PASS " else "FAIL ");
            try out.appendSlice(allocator, check.name);
            if (check.detail.len != 0) {
                try out.appendSlice(allocator, "\n     ");
                try out.appendSlice(allocator, check.detail);
            }
        }
    }
    if (format == .json) {
        try out.appendSlice(allocator, "],\"passed\":");
        try out.appendSlice(allocator, if (report.passed()) "true" else "false");
        try out.appendSlice(allocator, "}\n");
    } else {
        try out.appendSlice(allocator, if (report.passed()) "\nconformance: PASS\n" else "\nconformance: FAIL\n");
    }
    try compat.stdio.writeAll(stdout, out.items);
    return !report.passed();
}

const ValidateVerdict = union(enum) {
    judged: []ValidateFinding,
    unjudged: []const u8,
};

fn freeFindings(allocator: std.mem.Allocator, findings: *std.ArrayList(ValidateFinding)) void {
    for (findings.items) |finding| allocator.free(finding.code);
    findings.deinit(allocator);
}

fn appendFinding(allocator: std.mem.Allocator, out: *std.ArrayList(ValidateFinding), phase: ValidatePhase, code: []const u8, index: usize, line: usize) !void {
    const owned = try allocator.dupe(u8, code);
    errdefer allocator.free(owned);
    try out.append(allocator, .{ .phase = phase, .code = owned, .index = index, .line = line });
}

fn traceElements(allocator: std.mem.Allocator, source: []const u8) ![]const []const u8 {
    var scanner = std.json.Scanner.initCompleteInput(allocator, source);
    defer scanner.deinit();
    if (try scanner.next() != .array_begin) return error.TraceIsNotAnArray;
    var elements = std.ArrayList([]const u8).empty;
    errdefer elements.deinit(allocator);
    while (try scanner.peekNextTokenType() != .array_end) {
        const start = scanner.cursor;
        try scanner.skipValue();
        try elements.append(allocator, std.mem.trimStart(u8, source[start..scanner.cursor], " \t\r\n,"));
    }
    return elements.toOwnedSlice(allocator);
}

fn repeatsAKey(allocator: std.mem.Allocator, element: []const u8) !bool {
    var strict = std.json.parseFromSlice(std.json.Value, allocator, element, .{}) catch |err| switch (err) {
        error.OutOfMemory => return err,
        error.DuplicateField => return true,
        else => return false,
    };
    strict.deinit();
    return false;
}

fn parseValue(arena: std.mem.Allocator, text: []const u8) !?std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{ .duplicate_field_behavior = .use_last }) catch |err| switch (err) {
        error.OutOfMemory => return err,
        else => return null,
    };
}

fn traceItems(allocator: std.mem.Allocator, arena: std.mem.Allocator, source: []const u8, out: *std.ArrayList(ValidateFinding)) !?[]const TraceItem {
    const text = std.mem.trim(u8, source, " \t\r\n");
    if (text.len == 0) return &.{};
    if (text[0] == '[') {
        const document = try parseValue(arena, text) orelse {
            try appendFinding(allocator, out, .decode, "malformed_json", 0, 0);
            return null;
        };
        const elements = try traceElements(arena, text);
        const items = try arena.alloc(TraceItem, elements.len);
        for (items, elements, document.array.items) |*item, raw, value| item.* = .{ .raw = raw, .value = value, .line = 0 };
        return items;
    }
    if (try parseValue(arena, text)) |value| {
        const items = try arena.alloc(TraceItem, 1);
        items[0] = .{ .raw = text, .value = value, .line = 1 };
        return items;
    }
    var items = std.ArrayList(TraceItem).empty;
    var lines = std.mem.splitScalar(u8, text, '\n');
    var number: usize = 0;
    while (lines.next()) |untrimmed| {
        number += 1;
        const line = std.mem.trim(u8, untrimmed, " \t\r\n");
        if (line.len == 0) continue;
        const value = try parseValue(arena, line) orelse {
            try appendFinding(allocator, out, .decode, "malformed_json", items.items.len, number);
            return null;
        };
        try items.append(arena, .{ .raw = line, .value = value, .line = number });
    }
    return items.items;
}

fn validateTrace(allocator: std.mem.Allocator, judge: *validator.Validator, source: []const u8, out: *std.ArrayList(ValidateFinding)) !void {
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const items = try traceItems(allocator, arena, source, out) orelse return;
    const trace = try arena.alloc(std.json.Value, items.len);
    for (trace, items) |*value, item| value.* = item.value;

    const provider = namesProviderProfile(trace);
    const schema_document = if (provider) "provider-envelope.schema.json" else "envelope.schema.json";
    var compiled = try judge.schema();
    defer compiled.deinit();
    for (items, 0..) |item, index| {
        if (try repeatsAKey(allocator, item.raw)) {
            try appendFinding(allocator, out, .decode, "duplicate_key", index, item.line);
            continue;
        }
        if (try compiled.validateWithBranches(schema_document, item.value, judge.branchesFor(schema_document)) != null) {
            try appendFinding(allocator, out, .schema, "schema_invalid", index, item.line);
        }
    }
    if (out.items.len != 0) return;

    if (provider) {
        var machine = provider_semantic.Machine.init(allocator);
        defer machine.deinit();
        for (trace, 0..) |envelope, index| try machine.apply(index, envelope);
        try machine.close();
        for (machine.diagnostics.items) |diagnostic| try appendFinding(allocator, out, .semantic, diagnostic.code, diagnostic.index, lineOf(items, diagnostic.index));
        return;
    }

    var machine = semantic.Machine.init(allocator);
    defer machine.deinit();
    machine.packs = judge.semanticPacks();
    for (trace, 0..) |envelope, index| try machine.apply(index, envelope);
    try machine.close();
    for (machine.diagnostics.items) |diagnostic| try appendFinding(allocator, out, .semantic, diagnostic.code, diagnostic.index, lineOf(items, diagnostic.index));
}

fn lineOf(items: []const TraceItem, index: usize) usize {
    return if (index < items.len) items[index].line else 0;
}

const partial_semantic_note = "semantic rules partial: this validator has not ported every rule; goap validate checks them all";
const partial_load_note = "pack load checks partial: a descriptor whose id, version or schemas is the wrong shape is refused, a schemas path is checked to stay relative, lexically contained and beneath the pack root with symlinks resolved, and a declared name outside the pack's own id or an id overlapping another's is refused; a refusal now fails the whole load and prints its codes, so no pack is silently dropped. Other descriptor fields are not shape-checked, so a wrong-shaped payload_members or envelope_types reads as absent, a payload_members entry missing payload_type, member or an object schema contributes no member, and a schema ref that is absent or not a string is skipped without a refusal. A ref that names a file the pack does not contribute, or a pointer that does not resolve or does not land on an object, is refused with no code and the load fails; a branch whose resolved schema carries no type const is refused pack_branch_unpinned, and one whose const names a type other than its own declared type is refused pack_branch_undeclared_type. A schema ref carrying no fragment is judged against the document itself, so it is accepted when that document carries the pin and refused pack_branch_unpinned when it does not; a cited name that climbs out is refused with no code. A cited name spelled with a leading separator is not a spelling the descriptor's schemas lists, but it normalises to the same name that is registered, so the branch resolves and is accepted; a declared type with no usable (absent or non-string) schema is skipped for its branch rather than refused, though its type is still registered. The rest of Decision 0004's load refusals do not run, and a pack's payload members are not widened, so a pack goap refuses may be accepted here and a trace carrying a declared member is refused schema_invalid in strict mode; in tolerant mode the core schemas are widened and the member subschema is not run on its own, so a declared member can pass unchecked";

fn writeHumanReport(out: *std.ArrayList(u8), allocator: std.mem.Allocator, path: []const u8, verdict: ValidateVerdict) !void {
    switch (verdict) {
        .unjudged => |reason| try out.print(allocator, "UNJUDGED {s}: {s}\n", .{ path, reason }),
        .judged => |findings| {
            if (findings.len == 0) {
                try out.print(allocator, "PASS {s} ({s})\n", .{ path, partial_semantic_note });
                return;
            }
            for (findings) |finding| {
                if (finding.line == 0) {
                    try out.print(allocator, "FAIL {s}: {s} {s} at {d}\n", .{ path, @tagName(finding.phase), finding.code, finding.index });
                } else {
                    try out.print(allocator, "FAIL {s}: {s} {s} at {d} (line {d})\n", .{ path, @tagName(finding.phase), finding.code, finding.index, finding.line });
                }
            }
        },
    }
}

fn writeJsonReport(out: *std.ArrayList(u8), allocator: std.mem.Allocator, path: []const u8, verdict: ValidateVerdict) !void {
    var writer: std.Io.Writer.Allocating = .fromArrayList(allocator, out);
    defer out.* = writer.toArrayList();
    var json: std.json.Stringify = .{ .writer = &writer.writer };
    try json.beginObject();
    try json.objectField("file");
    try json.write(path);
    switch (verdict) {
        .unjudged => |reason| {
            try json.objectField("valid");
            try json.write(false);
            try json.objectField("complete");
            try json.write(false);
            try json.objectField("unjudged");
            try json.write(reason);
            try json.objectField("diagnostics");
            try json.beginArray();
            try json.endArray();
        },
        .judged => |findings| {
            try json.objectField("valid");
            try json.write(findings.len == 0);
            try json.objectField("complete");
            try json.write(false);
            try json.objectField("diagnostics");
            try json.beginArray();
            for (findings) |finding| {
                try json.beginObject();
                try json.objectField("phase");
                try json.write(@tagName(finding.phase));
                try json.objectField("code");
                try json.write(finding.code);
                try json.objectField("index");
                try json.write(finding.index);
                if (finding.line != 0) {
                    try json.objectField("line");
                    try json.write(finding.line);
                }
                try json.endObject();
            }
            try json.endArray();
        },
    }
    try json.endObject();
}

fn judgeTrace(allocator: std.mem.Allocator, judge: *validator.Validator, source: []const u8, findings: *std.ArrayList(ValidateFinding)) !?[]const u8 {
    validateTrace(allocator, judge, source, findings) catch |err| switch (err) {
        error.OutOfMemory => return err,
        error.UnsupportedKeyword, error.UnsupportedPattern, error.UnresolvableRef, error.InvalidSchema => return "the schema interpreter cannot judge this trace",
        else => return @errorName(err),
    };
    return null;
}

fn validateFlagRefusal(arg: []const u8) ?[]const u8 {
    if (!std.mem.startsWith(u8, arg, "-")) return null;
    if (std.mem.eql(u8, arg, "--format") or std.mem.eql(u8, arg, "-format")) return null;
    if (std.mem.startsWith(u8, arg, "--format=")) return null;
    if (std.mem.eql(u8, arg, "--mode") or std.mem.eql(u8, arg, "-mode")) return null;
    if (std.mem.startsWith(u8, arg, "--mode=")) return null;
    if (std.mem.eql(u8, arg, "--pack") or std.mem.eql(u8, arg, "-pack")) return null;
    if (std.mem.startsWith(u8, arg, "--pack=")) return null;
    if (std.mem.eql(u8, arg, "--provider")) return "the override is not carried; a trace declaring the profile routes itself, in #367";
    return "oapx validate does not carry this flag";
}

fn runValidate(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !bool {
    var format: ValidateFormat = .human;
    var mode: validator.Mode = .strict;
    var pack_dirs = std.ArrayList([]const u8).empty;
    defer pack_dirs.deinit(allocator);
    var paths = std.ArrayList([]const u8).empty;
    defer paths.deinit(allocator);
    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const arg = args[index];
        if (validateFlagRefusal(arg)) |reason| try unavailable(stderr, "validate", arg, reason);
        if (std.mem.eql(u8, arg, "--format") or std.mem.eql(u8, arg, "-format")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            format = std.meta.stringToEnum(ValidateFormat, args[index]) orelse return error.InvalidArgument;
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--format=")) {
            format = std.meta.stringToEnum(ValidateFormat, arg["--format=".len..]) orelse return error.InvalidArgument;
            continue;
        }
        if (std.mem.eql(u8, arg, "--mode") or std.mem.eql(u8, arg, "-mode")) {
            index += 1;
            if (index >= args.len) return error.UnsupportedMode;
            mode = validator.parseMode(args[index]) orelse return error.UnsupportedMode;
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--mode=")) {
            mode = validator.parseMode(arg["--mode=".len..]) orelse return error.UnsupportedMode;
            continue;
        }
        if (std.mem.eql(u8, arg, "--pack") or std.mem.eql(u8, arg, "-pack")) {
            index += 1;
            if (index >= args.len) return error.InvalidArgument;
            try pack_dirs.append(allocator, args[index]);
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--pack=")) {
            try pack_dirs.append(allocator, arg["--pack=".len..]);
            continue;
        }
        try paths.append(allocator, arg);
    }
    if (paths.items.len == 0) return error.InvalidArgument;

    var load_codes: std.ArrayList(u8) = .empty;
    defer load_codes.deinit(allocator);
    var judge = validator.Validator.init(allocator, .{
        .mode = mode,
        .pack_dirs = pack_dirs.items,
        .io = compat.fs.defaultIo(),
        .codes = &load_codes,
    }) catch |err| {
        if (load_codes.items.len != 0) {
            try compat.stdio.writeAll(stderr, "pack load refused:");
            try compat.stdio.writeAll(stderr, load_codes.items);
            try compat.stdio.writeAll(stderr, "\n");
        }
        var buf: [512]u8 = undefined;
        const reason = std.fmt.bufPrint(&buf, "{s} did not load as a pack: {s}", .{
            if (pack_dirs.items.len == 1) pack_dirs.items[0] else "a --pack directory",
            @errorName(err),
        }) catch "a pack did not load";
        try unavailable(stderr, "validate", "--pack", reason);
        return error.Unavailable;
    };
    defer judge.deinit();
    if (pack_dirs.items.len != 0) try compat.stdio.writeAll(stderr, partial_load_note ++ "\n");

    var report = std.ArrayList(u8).empty;
    defer report.deinit(allocator);
    if (format == .json) try report.appendSlice(allocator, "[");
    var any_rejected = false;
    for (paths.items, 0..) |path, position| {
        if (format == .json and position != 0) try report.appendSlice(allocator, ",");
        const source = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, validate_read_limit) catch |err| {
            var buf: [512]u8 = undefined;
            const msg = try std.fmt.bufPrint(&buf, "{s}: unreadable: {s}\n", .{ path, @errorName(err) });
            try compat.stdio.writeAll(stderr, msg);
            any_rejected = true;
            const verdict: ValidateVerdict = .{ .unjudged = "unreadable" };
            if (format == .json) try writeJsonReport(&report, allocator, path, verdict);
            continue;
        };
        defer allocator.free(source);

        var findings = std.ArrayList(ValidateFinding).empty;
        defer freeFindings(allocator, &findings);
        const verdict: ValidateVerdict = if (try judgeTrace(allocator, &judge, source, &findings)) |reason|
            .{ .unjudged = reason }
        else
            .{ .judged = findings.items };
        switch (verdict) {
            .unjudged => any_rejected = true,
            .judged => |found| if (found.len != 0) {
                any_rejected = true;
            },
        }
        switch (format) {
            .human => try writeHumanReport(&report, allocator, path, verdict),
            .json => try writeJsonReport(&report, allocator, path, verdict),
        }
    }
    if (format == .json) try report.appendSlice(allocator, "]\n");
    try compat.stdio.writeAll(stdout, report.items);
    return any_rejected;
}

fn printUsage(file: std.Io.File) !void {
    try compat.stdio.writeAll(file,
        \\Usage:
        \\  oapx                                              Start the terminal UI
        \\  oapx tui [--context-window <tokens>]          The terminal UI, over OAP through the in-process endpoint; what oapx alone starts
        \\                                                   --context-window takes a whole number, optionally with k or m
        \\  oapx tui --attach <url> [--adapter <name>]    The terminal UI over a running oapx serve's HTTP wire; adapter defaults to oapx
        \\  oapx run [--agent] [--storage] [--model <id>] "<prompt>"
        \\  oapx serve agent [--stdio] [--model <model-ref>]
        \\  oapx serve agent [--stdio] --backend <name> [--config <path>]
        \\  oapx serve provider [--stdio] [--specimens]
        \\  oapx claude-permission-hook --endpoint <url>  A Claude Code PermissionRequest hook: asks <url>, and leaves the prompt to the session when no allow or deny comes back
        \\  oapx serve provider --http 127.0.0.1:<port>
        \\  oapx serve agent,provider --stdio [--model <model-ref>]
        \\  oapx serve --stdio | --addr <host:port> [--config <path>] [--session-history <path>]
        \\                                                   Many sessions on one wire; oapx hub is its old name
        \\  oapx validate [--format human|json] [--mode strict|tolerant] [--pack DIR]... <trace.json>...
        \\  oapx conformance --command CMD [--session <id>] [--timeout-ms <n>]
        \\                        [--exit-grace-ms <n>] [--env NAME]... [--format text|json]
        \\                   --timeout-ms bounds one probe: the whole correlation,
        \\                   not each line, so unrelated frames cannot extend it.
        \\  oapx auth providers [--json]
        \\  oapx auth login --provider <id> [--json]
        \\  oapx --version
        \\
        \\Commands:
        \\  hub              The multi-session hub: one process holding many
        \\                   sessions over one adapter registry. --config
        \\                   names the registry document; without it the
        \\                   built-in memory adapter is served alone.
        \\  run              Non-interactive print mode: stream a prompt using
        \\                   stored credentials and print every event to stdout.
        \\                   Options may appear before or after the prompt.
        \\                   Use --agent to run through the full agent loop.
        \\                   Use --storage to resolve credentials like the TUI.
        \\                   Use --model <id> to pick the model
        \\                   (default kimi-k2.7-code).
        \\  serve agent      Serve agent-control-core over stdio, one envelope per line
        \\                   Remote provider: set OAPX_PROVIDER_SERVICE_URL and
        \\                   OAPX_PROVIDER_SERVICE_SECURITY=loopback|tls|mesh_proxy
        \\                   Use --backend claude, codex or pi to serve a Claude Code,
        \\                   Codex app-server or Pi child instead of the built-in
        \\                   loop, or an ACP agent, Hermes gateway, DeepSeek harness
        \\                   or OpenCode server named by a --config entry;
        \\                   --config reads an oap-serve.json registry entry.
        \\                   --backend memory serves the in-memory reference script.
        \\                   --backend oapx serves oapx's own loop as the TUI builds it.
        \\  serve provider   Serve model-provider-core over stdio, one envelope per line
        \\                   Use --specimens to print one of every envelope it emits.
        \\                   Use --http for a loopback-only HTTP/SSE endpoint.
        \\  serve agent,provider  Serve both OAP profiles over one stdio connection
        \\  validate         Judge traces: decode, schema, then the ported semantic rules
        \\  conformance      Drive an OAP endpoint and judge it: the handshake, then
        \\                   one submitted run through to its terminal event. Needs
        \\                   --command; everything after the flags is the endpoint's
        \\                   own argv. The endpoint inherits this process's
        \\                   environment unless --env narrows it. Diagnostics go to
        \\                   stderr, so --format json stays parseable. Exits
        \\                   non-zero on any failed check.
        \\  auth providers   List oauth-capable providers
        \\  auth login       Run OAuth flow and persist credentials
        \\  --version        Print binary version
        \\
        \\Superseded flags, still accepted: -p, --oap, --oap-provider
        \\
    );
}

const TuiArgError = error{
    UnknownOption,
    MissingContextWindow,
    ContextWindowNotATokenCount,
};

const TuiArgs = struct {
    context_window: ?u32 = null,
    attach: ?[]const u8 = null,
    adapter: []const u8 = "oapx",
    adapter_named: bool = false,
};

fn parseTuiArgs(args: []const []const u8) TuiArgError!TuiArgs {
    var parsed = TuiArgs{};
    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const arg = args[index];
        if (std.mem.eql(u8, arg, "--attach") or std.mem.eql(u8, arg, "--adapter")) {
            if (index + 1 >= args.len) return error.UnknownOption;
            index += 1;
            if (std.mem.eql(u8, arg, "--attach")) {
                parsed.attach = args[index];
            } else {
                parsed.adapter = args[index];
                parsed.adapter_named = true;
            }
            continue;
        }
        if (!std.mem.eql(u8, arg, "--context-window")) return error.UnknownOption;
        if (index + 1 >= args.len) return error.MissingContextWindow;
        index += 1;
        parsed.context_window = tui_app.parseContextWindow(args[index]) catch return error.ContextWindowNotATokenCount;
    }
    return parsed;
}

fn runTuiOverOap(allocator: std.mem.Allocator, io: std.Io, args: []const []const u8, stderr: std.Io.File) !void {
    const parsed = parseTuiArgs(args) catch |err| {
        switch (err) {
            error.MissingContextWindow => try compat.stdio.writeAll(stderr, "--context-window takes a token count\n\n"),
            error.ContextWindowNotATokenCount => try compat.stdio.writeAll(stderr, "--context-window takes a whole number of tokens, optionally with k or m\n\n"),
            error.UnknownOption => try compat.stdio.writeAll(stderr, "unknown argument to tui\n\n"),
        }
        try printUsage(stderr);
        return error.InvalidArgument;
    };
    if (parsed.attach == null and parsed.adapter_named) {
        try compat.stdio.writeAll(stderr, "--adapter names the hub adapter --attach opens a session on, so it needs --attach\n\n");
        try printUsage(stderr);
        return error.InvalidArgument;
    }
    const mode: tui_app.Execution = if (parsed.attach) |url| .{ .attach = .{ .url = url, .adapter = parsed.adapter } } else .in_process;
    try tui_app.runWith(allocator, io, parsed.context_window, mode);
}

test "the tui takes a context window and refuses anything else" {
    const none = try parseTuiArgs(&.{});
    try std.testing.expect(none.context_window == null);

    const sized = try parseTuiArgs(&.{ "--context-window", "1m" });
    try std.testing.expectEqual(@as(u32, 1_000_000), sized.context_window.?);

    const exact = try parseTuiArgs(&.{ "--context-window", "272000" });
    try std.testing.expectEqual(@as(u32, 272_000), exact.context_window.?);

    try std.testing.expectError(error.MissingContextWindow, parseTuiArgs(&.{"--context-window"}));
    try std.testing.expectError(error.ContextWindowNotATokenCount, parseTuiArgs(&.{ "--context-window", "loads" }));
    try std.testing.expectError(error.ContextWindowNotATokenCount, parseTuiArgs(&.{ "--context-window", "0" }));
    try std.testing.expectError(error.UnknownOption, parseTuiArgs(&.{ "--model", "gpt-5-codex" }));
}

const DEFAULT_PRINT_MODEL_ID = "kimi-k2.7-code";

const PrintModeOptions = struct {
    prompt: []const u8,
    model_id: []const u8 = DEFAULT_PRINT_MODEL_ID,
    use_agent_loop: bool = false,
    use_storage_auth: bool = false,
};

const PrintModeInvocation = union(enum) {
    print: PrintModeOptions,
    tui_runtime: []const u8,
};

const PrintModeArgError = union(enum) {
    missing_prompt,
    missing_option_value: []const u8,
    misplaced_option: []const u8,
    unsupported_option: []const u8,
    unexpected_argument: []const u8,
};

fn takePrintModeOptionValue(args: []const []const u8, index: *usize) ?[]const u8 {
    const value_index = index.* + 1;
    if (value_index >= args.len) return null;
    const value = args[value_index];
    if (std.mem.startsWith(u8, value, "--")) return null;
    index.* = value_index;
    return value;
}

fn parsePrintModeArgs(
    args: []const []const u8,
    err_out: *PrintModeArgError,
) error{InvalidArgument}!PrintModeInvocation {
    var prompt: ?[]const u8 = null;
    var model_id: []const u8 = DEFAULT_PRINT_MODEL_ID;
    var use_agent_loop = false;
    var use_storage_auth = false;

    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const arg = args[index];
        if (!std.mem.startsWith(u8, arg, "--")) {
            if (prompt != null) {
                err_out.* = .{ .unexpected_argument = arg };
                return error.InvalidArgument;
            }
            prompt = arg;
            continue;
        }
        if (std.mem.eql(u8, arg, "--agent")) {
            use_agent_loop = true;
        } else if (std.mem.eql(u8, arg, "--storage")) {
            use_storage_auth = true;
        } else if (std.mem.eql(u8, arg, "--tui-runtime")) {
            if (prompt != null) {
                err_out.* = .{ .misplaced_option = arg };
                return error.InvalidArgument;
            }
            const tui_prompt = takePrintModeOptionValue(args, &index) orelse {
                err_out.* = .{ .missing_option_value = arg };
                return error.InvalidArgument;
            };
            return .{ .tui_runtime = tui_prompt };
        } else if (std.mem.eql(u8, arg, "--model")) {
            model_id = takePrintModeOptionValue(args, &index) orelse {
                err_out.* = .{ .missing_option_value = arg };
                return error.InvalidArgument;
            };
        } else {
            err_out.* = .{ .unsupported_option = arg };
            return error.InvalidArgument;
        }
    }

    const resolved_prompt = prompt orelse {
        err_out.* = .missing_prompt;
        return error.InvalidArgument;
    };
    return .{ .print = .{
        .prompt = resolved_prompt,
        .model_id = model_id,
        .use_agent_loop = use_agent_loop,
        .use_storage_auth = use_storage_auth,
    } };
}

fn reportPrintModeArgError(err: PrintModeArgError) void {
    switch (err) {
        .missing_prompt => perr("error: -p requires a prompt argument\n"),
        .missing_option_value => |flag| perrf("error: {s} requires a value\n", .{flag}),
        .misplaced_option => |flag| perrf("error: {s} must appear before the prompt\n", .{flag}),
        .unsupported_option => |flag| perrf("error: unsupported -p option: {s}\n", .{flag}),
        .unexpected_argument => |arg| perrf("error: unexpected -p argument: {s}\n", .{arg}),
    }
}

fn runPrintMode(allocator: std.mem.Allocator, args: []const []const u8) !void {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = parsePrintModeArgs(args, &arg_error) catch |err| {
        reportPrintModeArgError(arg_error);
        return err;
    };
    const options = switch (invocation) {
        .tui_runtime => |tui_prompt| return runPrintTuiRuntime(allocator, tui_prompt),
        .print => |parsed| parsed,
    };
    const prompt = options.prompt;
    const model_id = options.model_id;
    const use_agent_loop = options.use_agent_loop;
    const use_storage_auth = options.use_storage_auth;

    perr("[print] building kimi model from env...\n");

    const api_key_copy: ?[]u8 = if (use_storage_auth) null else blk: {
        const api_key_env = compat.getEnvVarOwned(allocator, "KIMI_API_KEY") catch |err| {
            perrf("error: set KIMI_API_KEY to use -p, or pass --storage to use saved credentials: {s}\n", .{@errorName(err)});
            return error.NoCredentials;
        };
        break :blk api_key_env;
    };
    defer if (api_key_copy) |key| allocator.free(key);

    const region = resolvePrintKimiRegion(allocator, use_storage_auth);
    const is_global_kimi = std.mem.eql(u8, region, "global");
    const base_url = if (is_global_kimi) kimi_global_base_url else kimi_china_base_url;

    const model = ai_types.Model{
        .id = model_id,
        .name = model_id,
        .api = "openai-completions",
        .provider = "kimi",
        .base_url = base_url,
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = try kimiContextWindow(),
        .max_tokens = try kimiMaxTokens(),
    };

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    try register_builtins.registerBuiltInApiProviders(&registry);

    const provider = registry.getApiProvider(model.api) orelse {
        perrf("error: no provider registered for api '{s}'\n", .{model.api});
        return error.NoProvider;
    };
    _ = provider;

    const messages = try allocator.alloc(ai_types.Message, 1);
    defer allocator.free(messages);
    messages[0] = .{ .user = .{ .content = .{ .text = prompt }, .timestamp = compat.time.nowSeconds() } };
    const context = ai_types.Context{ .messages = messages };

    perrf("[print] model={s} api={s} provider={s} base_url={s}\n", .{ model.id, model.api, model.provider, model.base_url });
    if (api_key_copy) |key| {
        perrf("[print] api_key_len={d} reasoning={any}\n", .{ key.len, model.reasoning });
    } else {
        perrf("[print] api_key=storage reasoning={any}\n", .{model.reasoning});
    }
    if (use_agent_loop) {
        return runPrintAgentLoop(allocator, model, prompt, api_key_copy);
    }

    perr("[print] starting stream via protocol bridge...\n");

    var bridge = agent_bridge.InProcessProviderProtocolBridge.init(&registry);
    const protocol = bridge.protocolClient();

    const stream = try protocol.stream(model, context, .{
        .api_key = api_key_copy,
    }, allocator);
    defer _ = stream.deinitAndDestroy();

    perr("[print] stream created, polling events...\n");

    var event_count: usize = 0;
    while (stream.wait()) |ev| {
        event_count += 1;
        var owned_event = ev;
        defer if (stream.ownership.isOwned()) ai_types.deinitAssistantMessageEvent(allocator, &owned_event);
        switch (ev) {
            .text_delta => |td| {
                perrf("[text] ({d}b) {s}\n", .{ td.delta.len, td.delta });
            },
            .thinking_delta => |td| {
                perrf("[think] ({d}b) {s}\n", .{ td.delta.len, td.delta });
            },
            .toolcall_delta => |td| {
                perrf("[tool_delta] {s}\n", .{td.delta});
            },
            .done => |d| {
                perrf("[done] stop_reason={s} events={d}\n", .{ @tagName(d.message.stop_reason), event_count });
                break;
            },
            .@"error" => |e| {
                perrf("[error] reason={s} events={d}\n", .{ @tagName(e.reason), event_count });
                break;
            },
            else => {
                perrf("[event#{d}] {s}\n", .{ event_count, @tagName(ev) });
            },
        }
    }

    if (stream.getError()) |e| {
        perrf("[print] stream error: {s}\n", .{e});
    } else if (stream.getResult()) |result| {
        perrf("[print] COMPLETE stop={s} events={d} content_blocks={d}\n", .{ @tagName(result.stop_reason), event_count, result.content.len });
    } else {
        perrf("[print] NoFinalMessage after {d} events\n", .{event_count});
    }
}

fn runPrintAgentLoop(
    allocator: std.mem.Allocator,
    model: ai_types.Model,
    prompt: []const u8,
    api_key: ?[]const u8,
) !void {
    perr("[print-agent] starting full agent loop via protocol bridge...\n");

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    try register_builtins.registerBuiltInApiProviders(&registry);

    var bridge = agent_bridge.InProcessProviderProtocolBridge.init(&registry);
    const protocol = bridge.protocolClient();

    var context = agent_loop.AgentContext.init(allocator);
    defer context.deinit();
    context.system_prompt = ai_types.OwnedSlice(u8).initBorrowed("You are Makai running a Kimi debug print request. Reply concisely.");

    const messages = try allocator.alloc(ai_types.Message, 1);
    defer allocator.free(messages);
    messages[0] = .{ .user = .{ .content = .{ .text = try allocator.dupe(u8, prompt) }, .timestamp = compat.time.nowSeconds() } };

    const stream = try agent_loop.agentLoop(allocator, messages, &context, .{
        .model = model,
        .protocol = protocol,
        .tools = &.{},
        .api_key = api_key,
        .max_tokens = model.max_tokens,
        .thinking_level = .low,
        .max_iterations = 1,
    });
    defer _ = stream.deinitAndDestroy();

    var event_count: usize = 0;
    while (stream.wait()) |event| {
        event_count += 1;
        var owned_event = event;
        defer owned_event.deinit(allocator);

        switch (owned_event) {
            .message_update => |update| {
                switch (update.event) {
                    .text_delta => |td| perrf("[agent-text] ({d}b) {s}\n", .{ td.delta.len, td.delta }),
                    .thinking_delta => |td| perrf("[agent-think] ({d}b) {s}\n", .{ td.delta.len, td.delta }),
                    else => perrf("[agent-provider-event#{d}] {s}\n", .{ event_count, @tagName(update.event) }),
                }
            },
            .message_end => |payload| {
                if (payload.message == .assistant) {
                    const msg = payload.message.assistant;
                    perrf("[agent-message-end] stop={s} content_blocks={d}\n", .{ @tagName(msg.stop_reason), msg.content.len });
                } else {
                    perrf("[agent-message-end] {s}\n", .{@tagName(payload.message)});
                }
            },
            .turn_end => |payload| {
                perrf("[agent-turn-end] stop={s} content_blocks={d}\n", .{ @tagName(payload.message.stop_reason), payload.message.content.len });
                if (payload.message.error_message.slice().len > 0) {
                    perrf("[agent-turn-end] error={s}\n", .{payload.message.error_message.slice()});
                }
            },
            .agent_end => |payload| {
                perrf("[agent-end] messages={d}\n", .{payload.messages.slice().len});
            },
            else => perrf("[agent-event#{d}] {s}\n", .{ event_count, @tagName(owned_event) }),
        }
    }

    if (stream.getError()) |e| {
        perrf("[print-agent] stream error: {s}\n", .{e});
    } else if (stream.getResult()) |result| {
        perrf("[print-agent] COMPLETE iterations={d} stop={s} events={d} content_blocks={d}\n", .{
            result.iterations,
            @tagName(result.final_message.stop_reason),
            event_count,
            result.final_message.content.len,
        });
        if (result.final_message.error_message.slice().len > 0) {
            perrf("[print-agent] final error={s}\n", .{result.final_message.error_message.slice()});
        }
    } else {
        perrf("[print-agent] NoFinalMessage after {d} events\n", .{event_count});
    }
}

fn runPrintTuiRuntime(allocator: std.mem.Allocator, prompt: []const u8) !void {
    perr("[print-tui] starting TUI runtime path with production tool registry...\n");

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    try register_builtins.registerBuiltInApiProviders(&registry);

    var bridge = agent_bridge.InProcessProviderProtocolBridge.init(&registry);

    const models = [_]ai_types.Model{.{
        .id = "kimi-k2.7-code",
        .name = "Kimi K2.7 Code",
        .api = "openai-completions",
        .provider = "kimi",
        .base_url = kimi_china_base_url,
        .reasoning = false,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = try kimiContextWindow(),
        .max_tokens = try kimiMaxTokens(),
    }};

    var loop = oapx_adapter.LocalLoop.init(allocator, .{ .protocol = (&bridge).protocolClient(), .run_async = false });
    defer loop.deinit();

    const options = tui_app.SessionRuntimeOptions{
        .protocol = (&bridge).protocolClient(),
        .models = &models,
        .initial_model_id = "kimi-k2.7-code",
        .run_async = false,
        .compact_output = true,
        .loop = loop.loop(),
    };

    var runtime = try tui_app.SessionRuntime.init(allocator, options);
    defer runtime.deinit();

    if (runtime.currentModel()) |model| {
        perrf("[print-tui] initial model={s} api={s} provider={s} base_url={s}\n", .{ model.id, model.api, model.provider, model.base_url });
    } else {
        perr("[print-tui] no initial model\n");
    }

    runtime.switchModel("kimi-k2.7-code") catch |err| {
        perrf("[print-tui] switchModel(kimi-k2.7-code) failed: {s}\n", .{@errorName(err)});
        return err;
    };
    if (runtime.currentModel()) |model| {
        perrf("[print-tui] active model={s} api={s} provider={s} base_url={s} tools={d}\n", .{
            model.id,
            model.api,
            model.provider,
            model.base_url,
            runtime.availableTools().len,
        });
    }

    try runtime.submitTurn(prompt);

    var event_count: usize = 0;
    while (true) {
        const stream = runtime.streamEvents();
        if (stream.wait()) |event| {
            event_count += 1;
            var owned_event = event;
            defer owned_event.deinit(allocator);
            switch (owned_event) {
                .text_delta => |td| perrf("[print-tui-text] ({d}b) {s}\n", .{ td.delta.slice().len, td.delta.slice() }),
                .thinking_delta => |td| perrf("[print-tui-think] ({d}b) {s}\n", .{ td.delta.slice().len, td.delta.slice() }),
                .message_end => |payload| perrf("[print-tui-message-end] role={s} stop={s} is_error={any} text={s}\n", .{
                    @tagName(payload.role),
                    @tagName(payload.stop_reason),
                    payload.is_error,
                    payload.text.slice(),
                }),
                .turn_end => |payload| perrf("[print-tui-turn-end] stop={s}\n", .{@tagName(payload.stop_reason)}),
                .agent_end => |payload| {
                    perrf("[print-tui-agent-end] reason={s} events={d}\n", .{ @tagName(payload.reason), event_count });
                    break;
                },
                .@"error" => |payload| perrf("[print-tui-error] {s}\n", .{payload.message.slice()}),
                else => perrf("[print-tui-event#{d}] {s}\n", .{ event_count, @tagName(owned_event) }),
            }
            continue;
        }

        if (stream.getError()) |err_msg| {
            perrf("[print-tui] stream error: {s}\n", .{err_msg});
            break;
        }
        if (stream.getResult()) |result| {
            perrf("[print-tui] COMPLETE reason={s} events={d}\n", .{ @tagName(result.reason), event_count });
            break;
        }
        break;
    }
}

fn perr(msg: []const u8) void {
    std.debug.print("{s}", .{msg});
}

fn perrf(comptime fmt: []const u8, args: anytype) void {
    std.debug.print(fmt, args);
}

const PRODUCTION_AUTH_SERVER_OPTIONS = auth_protocol_server.AuthProtocolServer.Options{
    .persist_credentials = true,
    .enable_real_oauth = true,
};

fn handleAuth(
    args: []const []const u8,
    allocator: std.mem.Allocator,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !void {
    return handleAuthWithOptions(args, allocator, stdin, stdout, stderr, PRODUCTION_AUTH_SERVER_OPTIONS);
}

fn handleAuthWithOptions(
    args: []const []const u8,
    allocator: std.mem.Allocator,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
    server_options: auth_protocol_server.AuthProtocolServer.Options,
) !void {
    if (args.len == 0) {
        return error.InvalidArgument;
    }

    var file_io = auth_cli.FileIo.init(allocator, stdin, stdout, stderr);
    defer file_io.deinit();
    const io = file_io.io();

    if (std.mem.eql(u8, args[0], "providers")) {
        var json_mode = false;
        if (args.len > 1) {
            if (args.len == 2 and std.mem.eql(u8, args[1], "--json")) {
                json_mode = true;
            } else {
                return error.InvalidArgument;
            }
        }
        try auth_cli.runProvidersCommand(allocator, io, server_options, .{ .json_mode = json_mode });
        return;
    }

    if (std.mem.eql(u8, args[0], "login")) {
        var provider_id: ?[]const u8 = null;
        var json_mode = false;

        var i: usize = 1;
        while (i < args.len) : (i += 1) {
            if (std.mem.eql(u8, args[i], "--provider")) {
                i += 1;
                if (i >= args.len) return error.InvalidArgument;
                provider_id = args[i];
                continue;
            }
            if (std.mem.eql(u8, args[i], "--json")) {
                json_mode = true;
                continue;
            }
            return error.InvalidArgument;
        }

        const provider = provider_id orelse return error.InvalidArgument;
        try auth_cli.runLoginCommand(allocator, io, server_options, .{
            .provider_id = provider,
            .json_mode = json_mode,
        });
        return;
    }

    return error.InvalidArgument;
}

test "the print path resolves a Kimi region exactly as the catalog does" {
    const allocator = std.testing.allocator;
    try provider_catalog.blankEnvironment(allocator);
    defer compat.clearTestEnv();

    const named = provider_catalog.defaultRegion(kimi_provider_id).?;
    const values = [_][]const u8{ "", "china", "global", "moonshot", "cn", "coding", " global ", "GLOBAL", "mars", "region:global" };

    for (values) |value| {
        const canonical = provider_catalog.regionFromValue(kimi_provider_id, value) orelse named;
        try compat.setTestEnv(allocator, "KIMI_REGION", value);
        try std.testing.expectEqualStrings(canonical, resolvePrintKimiRegion(allocator, false));
    }

    try compat.setTestEnv(allocator, "KIMI_REGION", "global");
    try std.testing.expectEqualStrings("global", kimiRegionFromProviderData("region:moonshot"));
    try std.testing.expectEqualStrings("global", kimiRegionFromProviderData("region: global "));
    try std.testing.expectEqualStrings(named, kimiRegionFromProviderData("region:"));
    try std.testing.expectEqualStrings(named, kimiRegionFromProviderData("no region here"));
    try std.testing.expectEqualStrings(named, kimiRegionFromProviderData("region:mars"));
}

test "the hub's wall clock is in nanoseconds, which is the unit the hub divides" {
    const nanos = wallClockNanoseconds();
    const millis = @divTrunc(nanos, std.time.ns_per_ms);
    try std.testing.expect(millis > 1_600_000_000_000);
    try std.testing.expect(nanos > millis);
}

const TEST_AUTH_SERVER_OPTIONS = auth_protocol_server.AuthProtocolServer.Options{
    .persist_credentials = false,
    .enable_real_oauth = false,
};

const AuthCliHarness = struct {
    allocator: std.mem.Allocator,
    args: []const []const u8,

    stdin_read: std.Io.File,
    stdin_write: std.Io.File,
    stdout_read: std.Io.File,
    stdout_write: std.Io.File,
    stderr_read: std.Io.File,
    stderr_write: std.Io.File,

    err: ?anyerror = null,

    fn init(allocator: std.mem.Allocator, args: []const []const u8) !AuthCliHarness {
        const stdin_pipe = try compat.stdio.pipe();
        const stdout_pipe = try compat.stdio.pipe();
        const stderr_pipe = try compat.stdio.pipe();

        return .{
            .allocator = allocator,
            .args = args,
            .stdin_read = stdin_pipe[0],
            .stdin_write = stdin_pipe[1],
            .stdout_read = stdout_pipe[0],
            .stdout_write = stdout_pipe[1],
            .stderr_read = stderr_pipe[0],
            .stderr_write = stderr_pipe[1],
        };
    }

    fn run(self: *AuthCliHarness) void {
        defer {
            compat.stdio.close(self.stdout_write);
            compat.stdio.close(self.stderr_write);
            compat.stdio.close(self.stdin_read);
        }

        handleAuthWithOptions(
            self.args,
            self.allocator,
            self.stdin_read,
            self.stdout_write,
            self.stderr_write,
            TEST_AUTH_SERVER_OPTIONS,
        ) catch |err| {
            self.err = err;
        };
    }

    fn readAll(file: std.Io.File, allocator: std.mem.Allocator) ![]u8 {
        var buf = std.ArrayList(u8).empty;
        defer buf.deinit(allocator);
        var chunk: [4096]u8 = undefined;
        while (true) {
            const n = compat.stdio.read(file, &chunk) catch break;
            if (n == 0) break;
            try buf.appendSlice(allocator, chunk[0..n]);
        }
        return try allocator.dupe(u8, buf.items);
    }
};

test "handleAuth providers end-to-end through CLI wrapper emits provider ids" {
    const allocator = std.testing.allocator;

    var harness = try AuthCliHarness.init(allocator, &.{"providers"});
    const thread = try std.Thread.spawn(.{}, AuthCliHarness.run, .{&harness});

    compat.stdio.close(harness.stdin_write);

    const stdout_bytes = try AuthCliHarness.readAll(harness.stdout_read, allocator);
    defer allocator.free(stdout_bytes);
    const stderr_bytes = try AuthCliHarness.readAll(harness.stderr_read, allocator);
    defer allocator.free(stderr_bytes);

    thread.join();
    compat.stdio.close(harness.stdout_read);
    compat.stdio.close(harness.stderr_read);

    try std.testing.expect(harness.err == null);
    try std.testing.expect(std.mem.find(u8, stdout_bytes, "anthropic\n") != null);
    try std.testing.expect(std.mem.find(u8, stdout_bytes, "github-copilot\n") != null);
    try std.testing.expect(std.mem.find(u8, stdout_bytes, "deepseek\n") != null);
    try std.testing.expect(std.mem.find(u8, stdout_bytes, "openrouter\n") != null);
    try std.testing.expectEqual(@as(usize, 0), stderr_bytes.len);
}

test "handleAuth providers --json end-to-end emits backward-compatible shape" {
    const allocator = std.testing.allocator;

    var harness = try AuthCliHarness.init(allocator, &.{ "providers", "--json" });
    const thread = try std.Thread.spawn(.{}, AuthCliHarness.run, .{&harness});

    compat.stdio.close(harness.stdin_write);

    const stdout_bytes = try AuthCliHarness.readAll(harness.stdout_read, allocator);
    defer allocator.free(stdout_bytes);
    const stderr_bytes = try AuthCliHarness.readAll(harness.stderr_read, allocator);
    defer allocator.free(stderr_bytes);

    thread.join();
    compat.stdio.close(harness.stdout_read);
    compat.stdio.close(harness.stderr_read);

    try std.testing.expect(harness.err == null);

    const trimmed = std.mem.trim(u8, stdout_bytes, " \t\r\n");
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, trimmed, .{});
    defer parsed.deinit();

    const root = parsed.value.object;
    try std.testing.expectEqualStrings("providers", root.get("type").?.string);
    const providers = root.get("providers").?.array;
    try std.testing.expect(providers.items.len >= 3);
    try std.testing.expectEqual(@as(usize, 0), stderr_bytes.len);
}

test "handleAuth login end-to-end drives prompt loop through CLI wrapper" {
    auth_providers.test_fixture_opt_in = true;
    defer auth_providers.test_fixture_opt_in = null;
    const allocator = std.testing.allocator;

    var harness = try AuthCliHarness.init(allocator, &.{ "login", "--provider", "test-fixture" });
    const thread = try std.Thread.spawn(.{}, AuthCliHarness.run, .{&harness});

    try compat.stdio.writeAll(harness.stdin_write, "not-the-answer\nok\n");
    compat.stdio.close(harness.stdin_write);

    const stdout_bytes = try AuthCliHarness.readAll(harness.stdout_read, allocator);
    defer allocator.free(stdout_bytes);
    const stderr_bytes = try AuthCliHarness.readAll(harness.stderr_read, allocator);
    defer allocator.free(stderr_bytes);

    thread.join();
    compat.stdio.close(harness.stdout_read);
    compat.stdio.close(harness.stderr_read);

    try std.testing.expect(harness.err == null);
    try std.testing.expect(std.mem.find(
        u8,
        stdout_bytes,
        "https://example.invalid/makai-test-fixture-login",
    ) != null);
    try std.testing.expect(std.mem.find(u8, stdout_bytes, "Login successful.") != null);

    try std.testing.expect(std.mem.find(u8, stdout_bytes, "fixture-refresh-token") == null);
    try std.testing.expect(std.mem.find(u8, stdout_bytes, "fixture-access-token") == null);
    try std.testing.expect(std.mem.find(u8, stderr_bytes, "fixture-refresh-token") == null);
    try std.testing.expect(std.mem.find(u8, stderr_bytes, "fixture-access-token") == null);
}

test "handleAuth login surfaces typed error for unknown provider via CLI wrapper" {
    const allocator = std.testing.allocator;

    var harness = try AuthCliHarness.init(allocator, &.{ "login", "--provider", "no-such-provider" });
    const thread = try std.Thread.spawn(.{}, AuthCliHarness.run, .{&harness});

    compat.stdio.close(harness.stdin_write);

    const stdout_bytes = try AuthCliHarness.readAll(harness.stdout_read, allocator);
    defer allocator.free(stdout_bytes);
    const stderr_bytes = try AuthCliHarness.readAll(harness.stderr_read, allocator);
    defer allocator.free(stderr_bytes);

    thread.join();
    compat.stdio.close(harness.stdout_read);
    compat.stdio.close(harness.stderr_read);

    try std.testing.expectEqual(auth_cli.AuthCliError.AuthLoginFailed, harness.err.?);
    try std.testing.expect(std.mem.find(u8, stderr_bytes, "auth login failed") != null);
}

pub fn main(init: std.process.Init) !void {
    const allocator = init.gpa;

    const stdout = compat.stdio.stdout();
    const stderr = compat.stdio.stderr();
    const stdin = compat.stdio.stdin();

    const args = try init.minimal.args.toSlice(allocator);
    defer allocator.free(args);

    if (args.len <= 1) {
        runTuiOverOap(allocator, init.io, &.{}, stderr) catch |err| switch (err) {
            error.InvalidArgument => return error.InvalidArgument,
            else => return err,
        };
        return;
    }

    if (std.mem.eql(u8, args[1], "--help") or std.mem.eql(u8, args[1], "-h")) {
        try printUsage(stdout);
        return;
    }

    if (std.mem.eql(u8, args[1], "--version")) {
        try compat.stdio.writeAll(stdout, VERSION ++ "\n");
        return;
    }

    if (std.mem.eql(u8, args[1], "tui")) {
        runTuiOverOap(allocator, init.io, args[2..], stderr) catch |err| switch (err) {
            error.InvalidArgument => return error.InvalidArgument,
            else => return err,
        };
        return;
    }

    if (std.mem.eql(u8, args[1], "hub") or (std.mem.eql(u8, args[1], "serve") and (args.len == 2 or std.mem.startsWith(u8, args[2], "-")))) {
        runHub(allocator, args[2..], stdin, stdout, stderr) catch |err| {
            if (err == error.InvalidHubOption) {
                try printUsage(stderr);
                return error.InvalidArgument;
            }
            if (err == error.FramingDefect or err == error.InputFailed or err == error.OutputStalled) std.process.exit(1);
            return err;
        };
        return;
    }

    if (std.mem.eql(u8, args[1], "serve")) {
        runServe(allocator, args[2..], stdin, stdout, stderr) catch |err| {
            if (err == error.InvalidServeRole or err == error.InvalidServeOption) {
                try printUsage(stderr);
                return error.InvalidArgument;
            }
            if (err == error.MalformedLine or err == error.UnaddressableEnvelope) std.process.exit(1);
            if (err == error.FrameTooLarge or err == error.BackendRefused or err == error.StdinFailed or err == error.OutputStalled or err == error.BrokenPipe) std.process.exit(1);
            return err;
        };
        return;
    }

    if (std.mem.eql(u8, args[1], "claude-permission-hook")) {
        if (args.len != 4 or !std.mem.eql(u8, args[2], "--endpoint")) {
            try compat.stdio.writeAll(stderr, "usage: oapx claude-permission-hook --endpoint <url>\n");
            return error.InvalidArgument;
        }
        var arena_state = std.heap.ArenaAllocator.init(allocator);
        defer arena_state.deinit();
        const arena = arena_state.allocator();
        const request = readAllFrom(arena, stdin) catch return;
        if (claude_adapter.permission_hook.ask(arena, args[3], request)) |output| {
            try compat.stdio.writeAll(stdout, output);
            try compat.stdio.writeAll(stdout, "\n");
        }
        return;
    }

    if (std.mem.eql(u8, args[1], "codex-bridge")) {
        if (args.len != 4 or !std.mem.eql(u8, args[2], "--sock")) {
            try compat.stdio.writeAll(stderr, "usage: oapx codex-bridge --sock <path>\n");
            return error.InvalidArgument;
        }
        if (comptime @import("builtin").os.tag == .windows) {
            try compat.stdio.writeAll(stderr, "oapx codex-bridge: Codex's control socket is a Unix socket; this platform has none\n");
            std.process.exit(1);
        }
        codex_adapter.bridge.run(allocator, args[3], std.posix.STDIN_FILENO, std.posix.STDOUT_FILENO) catch |err| {
            var line_buffer: [128]u8 = undefined;
            try compat.stdio.writeAll(stderr, std.fmt.bufPrint(&line_buffer, "oapx codex-bridge: {s}\n", .{@errorName(err)}) catch "oapx codex-bridge: failed\n");
            std.process.exit(1);
        };
        return;
    }

    if (std.mem.eql(u8, args[1], "validate")) {
        const failed = runValidate(allocator, args[2..], stdout, stderr) catch |err| {
            if (err == error.Unavailable) std.process.exit(1);
            if (err == error.UnsupportedMode) {
                try compat.stdio.writeAll(stderr, "oapx validate: --mode: strict or tolerant\n");
                std.process.exit(1);
            }
            if (err == error.InvalidArgument) try printUsage(stderr);
            return err;
        };
        if (failed) std.process.exit(1);
        return;
    }

    if (std.mem.eql(u8, args[1], "conformance")) {
        const failed = runConformance(allocator, args[2..], stdout, stderr) catch |err| {
            if (err == error.InvalidArgument) try printUsage(stderr);
            return err;
        };
        if (failed) std.process.exit(1);
        return;
    }

    if (std.mem.eql(u8, args[1], "run")) {
        try runPrintMode(allocator, args[2..]);
        return;
    }

    if (std.mem.eql(u8, args[1], "--stdio")) {
        try compat.stdio.writeAll(stderr, "oapx --stdio was the retired v1 wire; use oapx serve agent,provider --stdio\n");
        std.process.exit(2);
    }

    if (std.mem.eql(u8, args[1], "--oap")) {
        runOapMode(allocator, args[2..], stdin, stdout, stderr, false) catch |err| {
            if (err == error.MalformedLine or err == error.UnaddressableEnvelope) std.process.exit(1);
            return err;
        };
        return;
    }

    if (std.mem.eql(u8, args[1], "--oap-provider")) {
        var answers_specimens = false;
        if (args.len > 2) {
            if (args.len == 3 and std.mem.eql(u8, args[2], "--specimens")) {
                answers_specimens = true;
            } else {
                var buf: [256]u8 = undefined;
                const msg = try std.fmt.bufPrint(
                    &buf,
                    "--oap-provider takes only --specimens: {s}\n\n",
                    .{args[2]},
                );
                try compat.stdio.writeAll(stderr, msg);
                try printUsage(stderr);
                return error.UnknownOapProviderArgument;
            }
        }
        try runOapProviderMode(allocator, stdin, stdout, stderr, answers_specimens);
        return;
    }

    if (std.mem.eql(u8, args[1], "--tui")) {
        try compat.stdio.writeAll(stderr, "oapx --tui was removed; run oapx, which starts the terminal UI over OAP\n");
        return error.InvalidArgument;
    }

    if (std.mem.eql(u8, args[1], "-p")) {
        try runPrintMode(allocator, args[2..]);
        return;
    }

    if (std.mem.eql(u8, args[1], "auth")) {
        handleAuth(args[2..], allocator, stdin, stdout, stderr) catch |err| {
            if (err == error.InvalidArgument) {
                try printUsage(stderr);
            }
            return err;
        };
        return;
    }

    var msg_buf: [512]u8 = undefined;
    const msg = try std.fmt.bufPrint(&msg_buf, "unknown argument: {s}\n\n", .{args[1]});
    try compat.stdio.writeAll(stderr, msg);
    try printUsage(stderr);
    return error.InvalidArgument;
}

test "modelFromCanonicalRef applies default base URL for non-catalog refs" {
    const allocator = std.testing.allocator;
    var model = try modelFromCanonicalRef(allocator, "anthropic/anthropic-messages@claude-test-model");
    defer model.deinit(allocator);
    try std.testing.expect(model.base_url.len > 0);

    var reasoning = try modelFromCanonicalRef(allocator, "openai/openai-responses@o3-custom");
    defer reasoning.deinit(allocator);
    try std.testing.expect(reasoning.reasoning);

    var chat = try modelFromCanonicalRef(allocator, "openai/openai-responses@gpt-5-chat-latest");
    defer chat.deinit(allocator);
    try std.testing.expect(!chat.reasoning);

    var claude = try modelFromCanonicalRef(allocator, "anthropic/anthropic-messages@claude-test-model");
    defer claude.deinit(allocator);
    try std.testing.expect(claude.reasoning);

    var legacy = try modelFromCanonicalRef(allocator, "anthropic/anthropic-messages@claude-3-5-sonnet");
    defer legacy.deinit(allocator);
    try std.testing.expect(!legacy.reasoning);

    var sonnet37 = try modelFromCanonicalRef(allocator, "anthropic/anthropic-messages@claude-3-7-sonnet-latest");
    defer sonnet37.deinit(allocator);
    try std.testing.expect(sonnet37.reasoning);

    for ([_][]const u8{ "claude-2.0", "claude-1.2", "claude-v1", "claude-instant" }) |legacy_id| {
        const ref = try std.fmt.allocPrint(allocator, "anthropic/anthropic-messages@{s}", .{legacy_id});
        defer allocator.free(ref);
        var legacy_model = try modelFromCanonicalRef(allocator, ref);
        defer legacy_model.deinit(allocator);
        try std.testing.expect(!legacy_model.reasoning);
    }
}

test "OAP model wire reference resolves to local API without changing provider identity" {
    const allocator = std.testing.allocator;
    var model = try modelFromCanonicalRef(allocator, "ollama/other:ollama-chat@llama3");
    defer model.deinit(allocator);
    try std.testing.expectEqualStrings("ollama", model.provider);
    try std.testing.expectEqualStrings("ollama", model.api);
    try std.testing.expectEqualStrings("llama3", model.id);
    try std.testing.expectError(error.InvalidModelRef, modelFromCanonicalRef(allocator, "deepseek/openai-responses@deepseek-chat"));
    try std.testing.expectError(error.InvalidModelRef, modelFromCanonicalRef(allocator, "anthropic/openai-chat-completions@claude-sonnet-4-5"));
    var served = try modelFromCanonicalRef(allocator, "anthropic/anthropic-messages@claude-sonnet-4-5");
    defer served.deinit(allocator);
    try std.testing.expectEqual(@as(u32, 200_000), served.context_window);
    try std.testing.expectError(error.InvalidModelRef, modelFromCanonicalRef(allocator, "acme/other:no-such-wire@m"));
}

test "print mode parses options that follow the prompt" {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = try parsePrintModeArgs(
        &[_][]const u8{ "write a haiku", "--model", "claude-sonnet-4-5" },
        &arg_error,
    );
    try std.testing.expect(std.meta.activeTag(invocation) == .print);
    try std.testing.expectEqualStrings("write a haiku", invocation.print.prompt);
    try std.testing.expectEqualStrings("claude-sonnet-4-5", invocation.print.model_id);
    try std.testing.expect(!invocation.print.use_agent_loop);
    try std.testing.expect(!invocation.print.use_storage_auth);
}

test "print mode parses options that precede the prompt" {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = try parsePrintModeArgs(
        &[_][]const u8{ "--agent", "--storage", "--model", "claude-sonnet-4-5", "write a haiku" },
        &arg_error,
    );
    try std.testing.expectEqualStrings("write a haiku", invocation.print.prompt);
    try std.testing.expectEqualStrings("claude-sonnet-4-5", invocation.print.model_id);
    try std.testing.expect(invocation.print.use_agent_loop);
    try std.testing.expect(invocation.print.use_storage_auth);
}

test "print mode parses options split around the prompt" {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = try parsePrintModeArgs(
        &[_][]const u8{ "--agent", "write a haiku", "--storage", "--model", "claude-sonnet-4-5" },
        &arg_error,
    );
    try std.testing.expectEqualStrings("write a haiku", invocation.print.prompt);
    try std.testing.expectEqualStrings("claude-sonnet-4-5", invocation.print.model_id);
    try std.testing.expect(invocation.print.use_agent_loop);
    try std.testing.expect(invocation.print.use_storage_auth);
}

test "print mode keeps the default model when --model is absent" {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = try parsePrintModeArgs(&[_][]const u8{"write a haiku"}, &arg_error);
    try std.testing.expectEqualStrings(DEFAULT_PRINT_MODEL_ID, invocation.print.model_id);
}

test "print mode takes the last --model when repeated on both sides" {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = try parsePrintModeArgs(
        &[_][]const u8{ "--model", "first", "write a haiku", "--model", "second" },
        &arg_error,
    );
    try std.testing.expectEqualStrings("second", invocation.print.model_id);
}

test "print mode rejects a trailing --model without a value" {
    var arg_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "write a haiku", "--model" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .missing_option_value);
    try std.testing.expectEqualStrings("--model", arg_error.missing_option_value);
}

test "print mode rejects an unsupported option after the prompt" {
    var arg_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "write a haiku", "--bogus" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .unsupported_option);
    try std.testing.expectEqualStrings("--bogus", arg_error.unsupported_option);
}

test "print mode rejects a second positional argument" {
    var arg_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "write a haiku", "and a limerick" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .unexpected_argument);
    try std.testing.expectEqualStrings("and a limerick", arg_error.unexpected_argument);
}

test "print mode rejects options without a prompt" {
    var arg_error: PrintModeArgError = .{ .unsupported_option = "--sentinel" };
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "--agent", "--model", "claude-sonnet-4-5" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .missing_prompt);

    var empty_error: PrintModeArgError = .{ .unsupported_option = "--sentinel" };
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{}, &empty_error),
    );
    try std.testing.expect(std.meta.activeTag(empty_error) == .missing_prompt);
}

test "print mode rejects an option token as a --model value" {
    var arg_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "write a haiku", "--model", "--storage" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .missing_option_value);
    try std.testing.expectEqualStrings("--model", arg_error.missing_option_value);

    var leading_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "--model", "--agent", "write a haiku" }, &leading_error),
    );
    try std.testing.expect(std.meta.activeTag(leading_error) == .missing_option_value);
    try std.testing.expectEqualStrings("--model", leading_error.missing_option_value);
}

test "print mode rejects --tui-runtime once a prompt is set" {
    var arg_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "first", "--tui-runtime", "second" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .misplaced_option);
    try std.testing.expectEqualStrings("--tui-runtime", arg_error.misplaced_option);
}

test "print mode rejects an option token as a --tui-runtime prompt" {
    var arg_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{ "--tui-runtime", "--model", "some-model" }, &arg_error),
    );
    try std.testing.expect(std.meta.activeTag(arg_error) == .missing_option_value);
    try std.testing.expectEqualStrings("--tui-runtime", arg_error.missing_option_value);
}

test "print mode short-circuits on --tui-runtime with its prompt" {
    var arg_error: PrintModeArgError = .missing_prompt;
    const invocation = try parsePrintModeArgs(
        &[_][]const u8{ "--tui-runtime", "write a haiku", "--model", "ignored" },
        &arg_error,
    );
    try std.testing.expect(std.meta.activeTag(invocation) == .tui_runtime);
    try std.testing.expectEqualStrings("write a haiku", invocation.tui_runtime);

    var missing_error: PrintModeArgError = .missing_prompt;
    try std.testing.expectError(
        error.InvalidArgument,
        parsePrintModeArgs(&[_][]const u8{"--tui-runtime"}, &missing_error),
    );
    try std.testing.expect(std.meta.activeTag(missing_error) == .missing_option_value);
    try std.testing.expectEqualStrings("--tui-runtime", missing_error.missing_option_value);
}
const OapModeArgs = struct {
    default_model_id: ?[]const u8 = null,
    answers_specimens: bool = false,
    backend: ?[]const u8 = null,
    config_path: ?[]const u8 = null,
};

const OapArgError = struct {
    unknown_option: ?[]const u8 = null,
    missing_option_value: ?[]const u8 = null,
    unexpected_positional: ?[]const u8 = null,
    repeated_option: ?[]const u8 = null,
    backend_conflict: ?[]const u8 = null,
};

fn takeOptionValue(args: []const []const u8, index: *usize, option: []const u8, slot: *?[]const u8, arg_error: *OapArgError) !void {
    if (slot.* != null) {
        arg_error.repeated_option = option;
        return error.InvalidArgument;
    }
    if (index.* + 1 >= args.len or std.mem.startsWith(u8, args[index.* + 1], "--")) {
        arg_error.missing_option_value = option;
        return error.InvalidArgument;
    }
    index.* += 1;
    slot.* = args[index.*];
}

fn parseOapModeArgs(args: []const []const u8, arg_error: *OapArgError) !OapModeArgs {
    var parsed = OapModeArgs{};
    var index: usize = 0;
    while (index < args.len) : (index += 1) {
        const arg = args[index];
        if (std.mem.eql(u8, arg, "--model")) {
            if (index + 1 >= args.len or std.mem.startsWith(u8, args[index + 1], "--")) {
                arg_error.missing_option_value = "--model";
                return error.InvalidArgument;
            }
            index += 1;
            parsed.default_model_id = args[index];
            continue;
        }
        if (std.mem.eql(u8, arg, "--backend")) {
            try takeOptionValue(args, &index, "--backend", &parsed.backend, arg_error);
            continue;
        }
        if (std.mem.eql(u8, arg, "--config")) {
            try takeOptionValue(args, &index, "--config", &parsed.config_path, arg_error);
            continue;
        }
        if (std.mem.eql(u8, arg, "--stdio")) continue;
        if (std.mem.eql(u8, arg, "--specimens")) {
            parsed.answers_specimens = true;
            continue;
        }
        if (std.mem.startsWith(u8, arg, "--")) {
            arg_error.unknown_option = arg;
            return error.InvalidArgument;
        }
        arg_error.unexpected_positional = arg;
        return error.InvalidArgument;
    }
    if (parsed.backend == null) {
        if (parsed.config_path != null) {
            arg_error.backend_conflict = "--config";
            return error.InvalidArgument;
        }
        return parsed;
    }
    if (parsed.default_model_id != null) {
        arg_error.backend_conflict = "--model";
        return error.InvalidArgument;
    }
    if (parsed.answers_specimens) {
        arg_error.backend_conflict = "--specimens";
        return error.InvalidArgument;
    }
    return parsed;
}

const OAP_PROVIDER_PROFILE_REVISION = "ea5e5b9b29dc27a3e84eb6fe6a0f5055fb437988";

const OAP_PROVIDER_EXHAUSTED_MESSAGE = "oapx --oap-provider: out of memory decoding a line on stdin; the endpoint is stopping rather than continuing in an unknown state\n";

test "the only failure that escapes handleLine is the one the stderr message names" {
    const E = @typeInfo(@typeInfo(@TypeOf(oap_provider_server.Server.handleLine)).@"fn".return_type.?).error_union.error_set;
    const escaping = @typeInfo(E).error_set.?;
    try std.testing.expectEqual(@as(usize, 1), escaping.len);
    try std.testing.expectEqualStrings("OutOfMemory", escaping[0].name);
}

fn oapProviderCompatibility(
    provider_id: []const u8,
    flags: provider_base_url.ProxyCompatFlags,
) oap_provider_types.CompatibilityFacts {
    return oap_provider_runtime.mapCompatibility(
        provider_base_url.transparentProxyCompatForFlags(provider_id, flags),
    ).facts;
}

fn oapFallbackCatalogState() ?oap_provider_types.ModelCatalogState {
    return .{ .observed_at_ms = null, .complete = false };
}

const oap_served_output_modalities = [_]oap_provider_types.Modality{.text};

const oap_test_mixed_models = [_]ai_types.Model{
    .{ .id = "a1", .name = "A1", .api = "anthropic-messages", .provider = "anthropic", .base_url = "https://a.test", .reasoning = true, .input = &oap_test_text_input, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 100_000, .max_tokens = 4_096 },
    .{ .id = "k1", .name = "K1", .api = "anthropic-messages", .provider = "kimi", .base_url = "https://k.test", .reasoning = true, .input = &oap_test_text_input, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 64_000, .max_tokens = 2_048 },
    .{ .id = "a2", .name = "A2", .api = "anthropic-messages", .provider = "anthropic", .base_url = "https://a.test", .reasoning = false, .input = &oap_test_text_input, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 200_000, .max_tokens = 8_192 },
    .{ .id = "k2", .name = "K2", .api = "openai-completions", .provider = "kimi", .base_url = "https://k.test/v1", .reasoning = false, .input = &oap_test_text_input, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 64_000, .max_tokens = 2_048 },
    .{ .id = "x1", .name = "X1", .api = "no-wire-api", .provider = "acme", .base_url = "https://x.test", .reasoning = false, .input = &oap_test_text_input, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 8_000, .max_tokens = 1_024 },
};

const oap_test_mixed_models_kimi_first = [_]ai_types.Model{ oap_test_mixed_models[1], oap_test_mixed_models[0], oap_test_mixed_models[3], oap_test_mixed_models[2], oap_test_mixed_models[4] };

fn oapTestProviderServer(allocator: std.mem.Allocator) oap_provider_server.Server {
    return oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
    });
}

test "a catalog row awaiting a credential the endpoint does not serve is kept undescribed when it has a fixed endpoint, and a create for it resolves its model from the row" {
    const allocator = std.testing.allocator;
    var server = oapTestProviderServer(allocator);
    defer server.deinit();
    try populateOapProviderCatalogFrom(allocator, &server, &oap_test_served_models);
    try populateAwaitingOapProviders(allocator, &server);

    try std.testing.expect(server.findAwaitingProvider("anthropic") == null);
    try std.testing.expect(server.findAwaitingProvider("ollama") == null);
    const kimi = server.findAwaitingProvider("kimi") orelse return error.TestAwaitingRowMissing;
    try std.testing.expect(server.findProvider("kimi") == null);
    try std.testing.expect(!kimi.allows_anonymous);
    for (server.awaiting.items) |awaiting| {
        try std.testing.expect(rowAwaitsCredential(provider_catalog.provider(awaiting.id).?));
        try std.testing.expect(server.findProvider(awaiting.id) == null);
    }
    const azure = provider_catalog.provider("azure").?;
    try std.testing.expect(rowAwaitsCredential(azure));
    try std.testing.expect(provider_catalog.baseUrl("azure", provider_catalog.firstImplementedWire(azure).?.id, null) == null);
    try std.testing.expect(server.findAwaitingProvider("azure") == null);

    const ref = try std.fmt.allocPrint(allocator, "kimi/{s}@kimi-k2", .{@tagName(kimi.wire)});
    defer allocator.free(ref);
    var model = (try awaitingOapModel(allocator, &server, "kimi", ref)) orelse return error.TestAwaitingModelMissing;
    defer model.deinit(allocator);
    try std.testing.expectEqualStrings("kimi", model.provider);
    try std.testing.expectEqualStrings("kimi-k2", model.id);
    try std.testing.expect((try awaitingOapModel(allocator, &server, "anthropic", "anthropic/anthropic-messages@claude-sonnet-4-5")) == null);
}

test "the provider endpoint serves one row per provider it loaded models for, on its catalog wire whatever the load order, with every loaded model on that wire and no claimed source" {
    const allocator = std.testing.allocator;
    var reordered = oapTestProviderServer(allocator);
    defer reordered.deinit();
    try populateOapProviderCatalogFrom(allocator, &reordered, &oap_test_mixed_models_kimi_first);
    try std.testing.expectEqualStrings("kimi", reordered.providers.items[0].id);
    try std.testing.expectEqual(oap_provider_types.Wire.@"openai-chat-completions", reordered.providers.items[0].wire);
    try std.testing.expectEqualStrings("https://k.test/v1", reordered.providers.items[0].endpoint);

    var server = oapTestProviderServer(allocator);
    defer server.deinit();
    try populateOapProviderCatalogFrom(allocator, &server, &oap_test_mixed_models);

    try std.testing.expectEqual(@as(usize, 2), server.providers.items.len);
    const anthropic = server.providers.items[0];
    try std.testing.expectEqualStrings("anthropic", anthropic.id);
    try std.testing.expectEqualStrings("https://a.test", anthropic.endpoint);
    try std.testing.expectEqual(@as(?u32, 200_000), anthropic.context_window);
    try std.testing.expectEqual(@as(?u32, 8_192), anthropic.max_output_tokens);
    try std.testing.expect(anthropic.round_trips_carry);
    const kimi = server.providers.items[1];
    try std.testing.expectEqualStrings("kimi", kimi.id);
    try std.testing.expectEqual(oap_provider_types.Wire.@"openai-chat-completions", kimi.wire);
    try std.testing.expect(!kimi.round_trips_carry);

    const refs = [_][]const u8{ "anthropic/anthropic-messages@a1", "anthropic/anthropic-messages@a2", "kimi/openai-chat-completions@k2" };
    try std.testing.expectEqual(refs.len, server.models.items.len);
    for (refs, server.models.items) |ref, entry| {
        try std.testing.expectEqualStrings(ref, entry.model_ref);
        try std.testing.expectEqual(@as(?oap_provider_types.ModelSource, null), entry.source);
        try std.testing.expectEqual(oap_provider_types.AuthStatus.unknown, entry.auth_status);
        for (oap_served_base_capabilities) |capability| {
            try std.testing.expect(std.mem.indexOfScalar(oap_provider_types.ModelCapability, entry.capabilities, capability) != null);
        }
    }
    try std.testing.expect(std.mem.indexOfScalar(oap_provider_types.ModelCapability, server.models.items[0].capabilities, .reasoning) != null);
    try std.testing.expect(std.mem.indexOfScalar(oap_provider_types.ModelCapability, server.models.items[1].capabilities, .reasoning) == null);
}

test "a served model reads as signed in only when its endpoint takes no credential" {
    const allocator = std.testing.allocator;
    var server = oapTestProviderServer(allocator);
    defer server.deinit();
    try populateOapProviderCatalogFrom(allocator, &server, &oap_test_served_models);
    var checked: usize = 0;
    for (server.models.items) |entry| {
        const anonymous = std.mem.eql(u8, entry.provider_id, "ollama");
        const expected: oap_provider_types.AuthStatus = if (anonymous) .authenticated else .unknown;
        try std.testing.expectEqual(expected, entry.auth_status);
        checked += 1;
    }
    try std.testing.expectEqual(oap_test_served_models.len, checked);
}

test "a served model accepts an inference that carries tools" {
    const allocator = std.testing.allocator;
    var server = oapTestProviderServer(allocator);
    defer server.deinit();
    try populateOapProviderCatalog(allocator, &server);
    const line =
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_provider_types.PROFILE ++
        "\",\"type\":\"inference.create.request\",\"id\":\"q1\",\"payload\":{\"model_ref\":\"anthropic/anthropic-messages@claude-sonnet-4-5\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"tools\":[{\"name\":\"lookup\",\"input_schema\":{\"type\":\"object\"}}]}}";
    try server.handleLine(line);
    var refused = false;
    while (server.popOutbound()) |out| {
        defer allocator.free(out);
        if (std.mem.indexOf(u8, out, "\"accepted\":false") != null) refused = true;
    }
    try std.testing.expect(!refused);
    try std.testing.expectEqual(@as(usize, 1), server.active.items.len);
}

test "a served lookup on an unnamed wire matches its discriminator and nothing else" {
    const allocator = std.testing.allocator;
    const google = [_]ai_types.Model{
        .{ .id = "g1", .name = "G1", .api = "google-generative-ai", .provider = "google", .base_url = "https://g.test", .reasoning = false, .input = &oap_test_text_input, .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 }, .context_window = 8_000, .max_tokens = 1_024 },
    };
    oap_served_models_for_test = &google;
    defer oap_served_models_for_test = null;

    var found = (try servedOapModel(allocator, "google", .other, "google-generative-ai", "g1")) orelse return error.ServedModelMissing;
    found.deinit(allocator);
    try std.testing.expect((try servedOapModel(allocator, "google", .other, "google-gemini-cli", "g1")) == null);
    try std.testing.expect((try servedOapModel(allocator, "google", .other, null, "g1")) == null);
}

var served_snapshot_loads: usize = 0;

fn countingServedLoad(allocator: std.mem.Allocator, refusals: *model_catalog.KeyRefusals) anyerror![]ai_types.Model {
    _ = refusals;
    served_snapshot_loads += 1;
    return cloneOapModels(allocator, &oap_test_served_models);
}

fn oapUnservedCode(provider_id: []const u8) ?oap_provider_types.ErrorCode {
    const refusal = oapUnservedRefusal(provider_id) orelse return null;
    return refusal.code;
}

test "an unserved catalog provider that needs a credential is refused as credential_missing until one resolves, then as provider_unavailable after asking for a reload" {
    const allocator = std.testing.allocator;
    try provider_catalog.blankEnvironment(allocator);
    defer compat.clearTestEnv();
    try compat.setTestEnv(allocator, "HOME", "/nonexistent/oapx-awaits-credential-test-home");
    served_oap_models.last_stale_reload_ms.store(std.math.minInt(i64), .seq_cst);
    defer served_oap_models.last_stale_reload_ms.store(std.math.minInt(i64), .seq_cst);

    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .credential_missing), oapUnservedCode("anthropic"));
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .credential_missing), oapUnservedCode("deepseek"));
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, null), oapUnservedCode("ollama"));
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, null), oapUnservedCode("no-such-provider"));
    try std.testing.expect(served_oap_models.claimStaleReload(0, served_oap_stale_reload_interval_ms));
    served_oap_models.last_stale_reload_ms.store(std.math.minInt(i64), .seq_cst);

    try std.testing.expect(rowAwaitsCredential(.{ .id = "keyed", .auth = &.{.api_key}, .wires = &.{"openai-completions"} }));
    try std.testing.expect(!rowAwaitsCredential(.{ .id = "keyless", .auth = &.{ .none, .api_key }, .wires = &.{"openai-completions"} }));
    try std.testing.expect(!rowAwaitsCredential(.{ .id = "unwired", .auth = &.{.api_key}, .wires = &.{"no-such-wire"} }));

    try compat.setTestEnv(allocator, provider_catalog.credentialEnv("anthropic")[1], "sk-ant-present");
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .provider_unavailable), oapUnservedCode("anthropic"));
    try std.testing.expect(!served_oap_models.claimStaleReload(compat.time.nowMillis(), served_oap_stale_reload_interval_ms));
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .credential_missing), oapUnservedCode("deepseek"));

    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDirPath(std.testing.io, ".oapx");
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = ".oapx/auth.json", .data = "{\"deepseek\":{\"api_key\":\"sk-stored\"}}" });
    const home = try tmp.dir.realPathFileAlloc(std.testing.io, ".", allocator);
    defer allocator.free(home);
    try compat.setTestEnv(allocator, "HOME", home);
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .provider_unavailable), oapUnservedCode("deepseek"));
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .credential_missing), oapUnservedCode("openai"));

    defer {
        served_oap_models.deinit();
        served_oap_models = .{};
    }
    try served_oap_models.reload(allocator, refusingServedLoad);
    served_oap_models.last_stale_reload_ms.store(std.math.minInt(i64), .seq_cst);
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .credential_rejected), oapUnservedCode("deepseek"));
    try std.testing.expect(served_oap_models.claimStaleReload(compat.time.nowMillis(), served_oap_stale_reload_interval_ms));
    try std.testing.expectEqual(@as(?oap_provider_types.ErrorCode, .provider_unavailable), oapUnservedCode("anthropic"));
}

fn refusingServedLoad(allocator: std.mem.Allocator, refusals: *model_catalog.KeyRefusals) anyerror![]ai_types.Model {
    try refusals.add("deepseek");
    return cloneOapModels(allocator, &.{});
}

test "a stale reload is claimed at most once per interval" {
    var cache: ServedOapModels = .{};
    defer cache.deinit();
    try std.testing.expect(cache.claimStaleReload(1_000, 30_000));
    try std.testing.expect(!cache.claimStaleReload(1_000, 30_000));
    try std.testing.expect(!cache.claimStaleReload(30_999, 30_000));
    try std.testing.expect(cache.claimStaleReload(31_000, 30_000));
}

fn failingServedLoad(allocator: std.mem.Allocator, refusals: *model_catalog.KeyRefusals) anyerror![]ai_types.Model {
    _ = allocator;
    try refusals.add("deepseek");
    served_snapshot_loads += 1;
    return error.NetworkUnreachable;
}

test "a reload that loads replaces the snapshot and advances its generation, and one that fails keeps both" {
    const allocator = std.testing.allocator;
    served_snapshot_loads = 0;
    var cache: ServedOapModels = .{};
    defer cache.deinit();

    const empty = try cache.snapshot(allocator);
    defer model_catalog.deinitModels(allocator, empty);
    try std.testing.expectEqual(@as(usize, 0), empty.len);
    try std.testing.expectEqual(@as(usize, 0), served_snapshot_loads);

    try cache.reload(allocator, countingServedLoad);
    try std.testing.expectEqual(@as(u64, 1), cache.generation.load(.seq_cst));
    try std.testing.expectError(error.NetworkUnreachable, cache.reload(allocator, failingServedLoad));
    try std.testing.expectEqual(@as(u64, 1), cache.generation.load(.seq_cst));
    try std.testing.expect(!cache.refusedKey("deepseek"));
    const kept = try cache.snapshot(allocator);
    defer model_catalog.deinitModels(allocator, kept);
    try std.testing.expectEqual(oap_test_served_models.len, kept.len);
    try std.testing.expectEqual(@as(usize, 2), served_snapshot_loads);
}

var mid_load_cache: ?*ServedOapModels = null;
var mid_load_requests: usize = 0;

fn loadThatAsksForAnotherReload(allocator: std.mem.Allocator, refusals: *model_catalog.KeyRefusals) anyerror![]ai_types.Model {
    _ = refusals;
    served_snapshot_loads += 1;
    if (mid_load_requests > 0) {
        mid_load_requests -= 1;
        if (mid_load_cache.?.requestReload()) return error.SecondWorkerStarted;
    }
    return cloneOapModels(allocator, &oap_test_served_models);
}

test "a reload asked for while one is loading runs after it, on the same worker" {
    const allocator = std.testing.allocator;
    var cache: ServedOapModels = .{};
    defer cache.deinit();
    served_snapshot_loads = 0;
    mid_load_cache = &cache;
    defer mid_load_cache = null;
    mid_load_requests = 1;

    try std.testing.expect(cache.requestReload());
    cache.runReloads(allocator, loadThatAsksForAnotherReload);
    try std.testing.expectEqual(@as(usize, 2), served_snapshot_loads);
    try std.testing.expectEqual(@as(u64, 2), cache.generation.load(.seq_cst));
    try std.testing.expect(!cache.reloading.load(.seq_cst));
    try std.testing.expect(cache.requestReload());
}

var window_requests: usize = 0;

fn requestInTheReleaseWindow(cache: *ServedOapModels) void {
    if (window_requests == 0) return;
    window_requests -= 1;
    std.debug.assert(!cache.requestReload());
}

test "a reload asked for just before the worker releases is not lost" {
    const allocator = std.testing.allocator;
    var cache: ServedOapModels = .{ .before_release = requestInTheReleaseWindow };
    defer cache.deinit();
    served_snapshot_loads = 0;
    window_requests = 1;

    try std.testing.expect(cache.requestReload());
    cache.runReloads(allocator, countingServedLoad);
    try std.testing.expectEqual(@as(usize, 2), served_snapshot_loads);
    try std.testing.expect(!cache.reloading.load(.seq_cst));
}

test "a reload advertises the grant channel the endpoint was started with, and none when it takes no grants" {
    const allocator = std.testing.allocator;
    var cache: ServedOapModels = .{};
    defer cache.deinit();
    try cache.reload(allocator, countingServedLoad);

    var closed = oapTestProviderServer(allocator);
    defer closed.deinit();
    var closed_applied: u64 = 0;
    try std.testing.expect(try applyServedOapReload(allocator, &closed, &cache, &closed_applied));
    for (closed.providers.items) |descriptor| {
        try std.testing.expectEqual(oap_provider_types.CredentialGrantChannel.none, descriptor.credential_grant);
        try std.testing.expectEqual(@as(usize, 0), descriptor.grant_kinds.len);
    }

    var open = oap_provider_server.Server.init(allocator, .{ .accepts_inference = true, .grant_channel = .out_of_band });
    defer open.deinit();
    var open_applied: u64 = 0;
    try std.testing.expect(try applyServedOapReload(allocator, &open, &cache, &open_applied));
    const expected: oap_provider_types.CredentialGrantChannel = if (oap_provider_grant_channel.GrantChannel.supported) .out_of_band else .none;
    for (open.providers.items) |descriptor| try std.testing.expectEqual(expected, descriptor.credential_grant);
}

test "a finished reload replaces the providers the endpoint serves, once per generation" {
    const allocator = std.testing.allocator;
    var server = oapTestProviderServer(allocator);
    defer server.deinit();
    oap_served_models_for_test = oap_test_served_models[1..2];
    defer oap_served_models_for_test = null;
    try populateOapProviderCatalog(allocator, &server);
    try std.testing.expectEqual(@as(usize, 1), server.providers.items.len);

    var cache: ServedOapModels = .{};
    defer cache.deinit();
    var applied: u64 = 0;
    try std.testing.expect(!try applyServedOapReload(allocator, &server, &cache, &applied));
    try std.testing.expectEqual(@as(usize, 1), server.providers.items.len);

    try cache.reload(allocator, countingServedLoad);
    try std.testing.expect(try applyServedOapReload(allocator, &server, &cache, &applied));
    try std.testing.expectEqual(@as(usize, 3), server.providers.items.len);
    try std.testing.expectEqualStrings("anthropic", server.providers.items[0].id);
    try std.testing.expect(!try applyServedOapReload(allocator, &server, &cache, &applied));
}

test "the provider endpoint serves no provider when it discovered no model, as with no key present" {
    const allocator = std.testing.allocator;
    var server = oapTestProviderServer(allocator);
    defer server.deinit();
    try populateOapProviderCatalogFrom(allocator, &server, &.{});
    try std.testing.expectEqual(@as(usize, 0), server.providers.items.len);
    try std.testing.expectEqual(@as(usize, 0), server.models.items.len);
}

test "an inference for a model the endpoint does not serve fails as model_not_found, even on a served provider" {
    const allocator = std.testing.allocator;
    const refs = [_][]const u8{"anthropic/anthropic-messages@claude-unserved"};
    for (refs) |ref| {
        var registry = api_registry.ApiRegistry.init(allocator);
        defer registry.deinit();
        var server = oapTestProviderServer(allocator);
        defer server.deinit();
        try populateOapProviderCatalog(allocator, &server);
        var running = std.ArrayList(RunningOapInference).empty;
        defer running.deinit(allocator);

        const line = try std.fmt.allocPrint(
            allocator,
            "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"{s}\",\"type\":\"inference.create.request\",\"id\":\"q1\",\"payload\":{{\"model_ref\":\"{s}\",\"messages\":[{{\"role\":\"user\",\"content\":\"hi\"}}]}}}}",
            .{ oap_provider_types.PROFILE, ref },
        );
        defer allocator.free(line);
        try server.handleLine(line);
        while (server.popOutbound()) |out| allocator.free(out);
        try std.testing.expectEqual(@as(usize, 1), server.active.items.len);
        const inference_id = try allocator.dupe(u8, server.active.items[0].id);
        defer allocator.free(inference_id);

        try startOapInference(allocator, &registry, &server, &running, inference_id, &.{});
        try std.testing.expectEqual(@as(usize, 0), running.items.len);
        var refused = false;
        while (server.popOutbound()) |out| {
            defer allocator.free(out);
            if (std.mem.indexOf(u8, out, "\"model_not_found\"") != null) refused = true;
        }
        try std.testing.expect(refused);
    }
}

test "the served rows publish no lifecycle, because none states one" {
    const allocator = std.testing.allocator;
    try provider_catalog.blankEnvironment(allocator);
    defer compat.clearTestEnv();

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = if (oap_provider_grant_channel.GrantChannel.supported) .out_of_band else .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
        .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
        .catalog = oapFallbackCatalogState(),
    });
    defer server.deinit();
    try populateOapProviderCatalog(allocator, &server);

    if (server.models.items.len == 0) return error.NoBuiltInRowsPopulated;
    for (server.models.items) |row| {
        if (row.lifecycle != null) {
            std.debug.print("built-in row {s} published lifecycle {s} without stating one\n", .{ row.model_id, @tagName(row.lifecycle.?) });
            return error.BuiltInRowPublishedAnUnstatedLifecycle;
        }
    }

    const request = try std.fmt.allocPrint(allocator, "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"{s}\",\"type\":\"provider.models.list.request\",\"id\":\"q1\",\"payload\":{{}}}}", .{oap_provider_types.PROFILE});
    defer allocator.free(request);
    try server.handleLine(request);

    if (server.outbound.items.len == 0) return error.NoResponseEmitted;
    const line = server.outbound.items[server.outbound.items.len - 1];
    if (std.mem.indexOf(u8, line, "provider.models.list.response") == null) return error.NotAModelsListResponse;
    if (std.mem.indexOf(u8, line, "model_ref") == null) return error.NoModelsPublished;
    if (std.mem.indexOf(u8, line, "lifecycle") != null) {
        std.debug.print("the built-in models.list.response published lifecycle: {s}\n", .{line});
        return error.ResponsePublishedAnUnstatedLifecycle;
    }
}

fn populateOapProviderCatalog(allocator: std.mem.Allocator, server: *oap_provider_server.Server) !void {
    const models = try loadServedOapModels(allocator);
    defer model_catalog.deinitModels(allocator, models);
    try populateOapProviderCatalogFrom(allocator, server, models);
    try populateAwaitingOapProviders(allocator, server);
}

fn populateOapProviderCatalogFrom(allocator: std.mem.Allocator, server: *oap_provider_server.Server, models: []const ai_types.Model) !void {
    const proxy_flags = try provider_base_url.proxyCompatFlagsFromEnv(allocator);
    for (models, 0..) |first, index| {
        if (oap_provider_catalog.mapApiToWire(first.api) == null) continue;
        if (servedBefore(models[0..index], first.provider)) continue;
        const model = servedOapLead(models, first.provider) orelse continue;
        const mapping = oap_provider_catalog.mapApiToWire(model.api) orelse continue;

        var context_window: u32 = 0;
        var max_output_tokens: u32 = 0;
        var reasons = false;
        for (models) |sibling| {
            if (!std.mem.eql(u8, sibling.provider, model.provider)) continue;
            const sibling_mapping = oap_provider_catalog.mapApiToWire(sibling.api) orelse continue;
            if (sibling_mapping.wire != mapping.wire) continue;
            context_window = @max(context_window, sibling.context_window);
            max_output_tokens = @max(max_output_tokens, sibling.max_tokens);
            reasons = reasons or sibling.reasoning;
        }

        var provider_transferred = false;
        const id = try allocator.dupe(u8, model.provider);
        errdefer if (!provider_transferred) allocator.free(id);
        const endpoint = try allocator.dupe(u8, model.base_url);
        errdefer if (!provider_transferred) allocator.free(endpoint);
        const policies = try allocator.dupe(
            oap_provider_types.SnapshotPolicy,
            oap_provider_server.IMPLEMENTED_SNAPSHOT_POLICIES,
        );
        errdefer if (!provider_transferred) allocator.free(policies);
        const wire_id = if (mapping.wire_id) |value| try allocator.dupe(u8, value) else null;
        errdefer if (!provider_transferred) {
            if (wire_id) |value| allocator.free(value);
        };
        const grant = servedOapGrant(server.options.grant_channel);
        const grant_kinds = if (grant == .none)
            try allocator.dupe(oap_provider_types.GrantKind, &.{})
        else
            try allocator.dupe(oap_provider_types.GrantKind, &.{.static});
        errdefer if (!provider_transferred) allocator.free(grant_kinds);

        try server.addProvider(.{
            .id = id,
            .wire = mapping.wire,
            .wire_id = wire_id,
            .framing = mapping.framing,
            .endpoint = endpoint,
            .allows_anonymous = model.allows_anonymous,
            .snapshot_policies = policies,
            .answers_sync = oap_provider_server.IMPLEMENTS_SYNC,
            .compatibility = oapProviderCompatibility(model.provider, proxy_flags),
            .credential_grant = grant,
            .grant_kinds = grant_kinds,
            .context_window = if (context_window > 0) context_window else null,
            .max_output_tokens = if (max_output_tokens > 0) max_output_tokens else null,
            .round_trips_carry = reasons and mapping.wire == .@"anthropic-messages" and std.mem.eql(u8, model.provider, "anthropic"),
        });
        provider_transferred = true;

        const entries = try oap_provider_catalog.ownedModelEntriesForRow(allocator, models, model.provider, mapping.wire, .discovered);
        var added: usize = 0;
        defer {
            for (entries[added..]) |*entry| entry.deinit(allocator);
            allocator.free(entries);
        }
        for (entries) |*entry| {
            const declared = try servedOapCapabilities(allocator, entry.capabilities);
            allocator.free(entry.capabilities);
            entry.capabilities = declared;
            entry.source = null;
            entry.auth_status = if (model.allows_anonymous) .authenticated else .unknown;
            entry.output_modalities = try allocator.dupe(oap_provider_types.Modality, &oap_served_output_modalities);
            try server.addModel(entry.*);
            added += 1;
        }
    }
}

fn populateAwaitingOapProviders(allocator: std.mem.Allocator, server: *oap_provider_server.Server) !void {
    for (provider_catalog.all) |row| {
        if (!rowAwaitsCredential(row)) continue;
        if (server.findProvider(row.id) != null) continue;
        const wire = provider_catalog.firstImplementedWire(row) orelse continue;
        const mapping = oap_provider_catalog.mapApiToWire(wire.id) orelse continue;
        const base = provider_catalog.baseUrl(row.id, wire.id, provider_catalog.defaultRegion(row.id)) orelse continue;

        var transferred = false;
        const id = try allocator.dupe(u8, row.id);
        errdefer if (!transferred) allocator.free(id);
        const endpoint = try allocator.dupe(u8, base);
        errdefer if (!transferred) allocator.free(endpoint);
        const policies = try allocator.dupe(oap_provider_types.SnapshotPolicy, oap_provider_server.IMPLEMENTED_SNAPSHOT_POLICIES);
        errdefer if (!transferred) allocator.free(policies);
        const wire_id = if (mapping.wire_id) |value| try allocator.dupe(u8, value) else null;
        errdefer if (!transferred) {
            if (wire_id) |value| allocator.free(value);
        };
        try server.addAwaitingProvider(.{
            .id = id,
            .wire = mapping.wire,
            .wire_id = wire_id,
            .framing = mapping.framing,
            .endpoint = endpoint,
            .allows_anonymous = false,
            .snapshot_policies = policies,
            .answers_sync = oap_provider_server.IMPLEMENTS_SYNC,
            .credential_grant = servedOapGrant(server.options.grant_channel),
        });
        transferred = true;
    }
}

const oap_served_base_capabilities = [_]oap_provider_types.ModelCapability{ .chat, .streaming, .tools };

fn servedOapCapabilities(allocator: std.mem.Allocator, found: []const oap_provider_types.ModelCapability) ![]const oap_provider_types.ModelCapability {
    var list = std.ArrayList(oap_provider_types.ModelCapability).empty;
    errdefer list.deinit(allocator);
    try list.appendSlice(allocator, &oap_served_base_capabilities);
    for (found) |capability| {
        if (std.mem.indexOfScalar(oap_provider_types.ModelCapability, list.items, capability) == null) try list.append(allocator, capability);
    }
    return list.toOwnedSlice(allocator);
}

fn servedOapGrant(channel: oap_provider_server.GrantChannel) oap_provider_types.CredentialGrantChannel {
    return switch (channel) {
        .out_of_band => if (oap_provider_grant_channel.GrantChannel.supported) .out_of_band else .none,
        .on_envelope => .on_envelope,
        .unsupported => .none,
    };
}

fn servedOapLead(models: []const ai_types.Model, provider_id: []const u8) ?ai_types.Model {
    const primary = if (provider_catalog.provider(provider_id)) |row| provider_catalog.firstImplementedWire(row) else null;
    var lead: ?ai_types.Model = null;
    for (models) |model| {
        if (!std.mem.eql(u8, model.provider, provider_id)) continue;
        if (oap_provider_catalog.mapApiToWire(model.api) == null) continue;
        if (primary) |wire| if (std.mem.eql(u8, model.api, wire.id)) return model;
        if (lead == null) lead = model;
    }
    return lead;
}

fn servedBefore(earlier: []const ai_types.Model, provider_id: []const u8) bool {
    for (earlier) |model| {
        if (!std.mem.eql(u8, model.provider, provider_id)) continue;
        if (oap_provider_catalog.mapApiToWire(model.api) != null) return true;
    }
    return false;
}

var oap_served_models_for_test: ?[]const ai_types.Model = null;

fn loadServedOapModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    if (@import("builtin").is_test) return cloneOapModels(allocator, oap_served_models_for_test orelse &oap_test_served_models);
    return served_oap_models.snapshot(allocator);
}

fn loadProductionServedModels(allocator: std.mem.Allocator, refusals: *model_catalog.KeyRefusals) anyerror![]ai_types.Model {
    return model_catalog.loadProductionModelsNotingRefusals(allocator, refusals);
}

const ServedOapModels = struct {
    mutex: std.Io.Mutex = .init,
    models: ?[]ai_types.Model = null,
    refusals: ?model_catalog.KeyRefusals = null,
    cache_allocator: ?std.mem.Allocator = null,
    generation: std.atomic.Value(u64) = .init(0),
    reloading: std.atomic.Value(bool) = .init(false),
    reload_again: std.atomic.Value(bool) = .init(false),
    last_stale_reload_ms: std.atomic.Value(i64) = .init(std.math.minInt(i64)),
    before_release: ?*const fn (*ServedOapModels) void = null,

    fn snapshot(self: *ServedOapModels, allocator: std.mem.Allocator) ![]ai_types.Model {
        self.mutex.lockUncancelable(hubIo());
        defer self.mutex.unlock(hubIo());
        return cloneOapModels(allocator, self.models orelse &.{});
    }

    fn reload(
        self: *ServedOapModels,
        cache_allocator: std.mem.Allocator,
        load: *const fn (std.mem.Allocator, *model_catalog.KeyRefusals) anyerror![]ai_types.Model,
    ) !void {
        var refusals = model_catalog.KeyRefusals.init(cache_allocator);
        errdefer refusals.deinit();
        const fresh = try load(cache_allocator, &refusals);
        self.mutex.lockUncancelable(hubIo());
        defer self.mutex.unlock(hubIo());
        if (self.models) |previous| model_catalog.deinitModels(self.cache_allocator.?, previous);
        if (self.refusals) |*previous| previous.deinit();
        self.models = fresh;
        self.refusals = refusals;
        self.cache_allocator = cache_allocator;
        _ = self.generation.fetchAdd(1, .seq_cst);
    }

    fn refusedKey(self: *ServedOapModels, provider_id: []const u8) bool {
        self.mutex.lockUncancelable(hubIo());
        defer self.mutex.unlock(hubIo());
        const refusals = self.refusals orelse return false;
        return refusals.contains(provider_id);
    }

    fn claimStaleReload(self: *ServedOapModels, now_ms: i64, interval_ms: i64) bool {
        const last = self.last_stale_reload_ms.load(.seq_cst);
        if (now_ms -| last < interval_ms) return false;
        return self.last_stale_reload_ms.cmpxchgStrong(last, now_ms, .seq_cst, .seq_cst) == null;
    }

    fn requestReload(self: *ServedOapModels) bool {
        self.reload_again.store(true, .seq_cst);
        return !self.reloading.swap(true, .seq_cst);
    }

    fn runReloads(
        self: *ServedOapModels,
        cache_allocator: std.mem.Allocator,
        load: *const fn (std.mem.Allocator, *model_catalog.KeyRefusals) anyerror![]ai_types.Model,
    ) void {
        while (true) {
            self.reload_again.store(false, .seq_cst);
            self.reload(cache_allocator, load) catch {};
            if (self.reload_again.load(.seq_cst)) continue;
            if (self.before_release) |hook| hook(self);
            self.reloading.store(false, .seq_cst);
            if (!self.reload_again.load(.seq_cst)) return;
            if (self.reloading.swap(true, .seq_cst)) return;
        }
    }

    fn deinit(self: *ServedOapModels) void {
        if (self.models) |models| model_catalog.deinitModels(self.cache_allocator.?, models);
        if (self.refusals) |*refusals| refusals.deinit();
        self.* = undefined;
    }
};

var served_oap_models: ServedOapModels = .{};

const served_oap_startup_wait_ms: i64 = 2_000;
const served_oap_stale_reload_interval_ms: i64 = 30_000;

fn startServedOapReload() void {
    if (@import("builtin").is_test) return;
    if (!served_oap_models.requestReload()) return;
    const thread = std.Thread.spawn(.{}, servedOapReloadThread, .{}) catch {
        served_oap_models.reloading.store(false, .seq_cst);
        return;
    };
    thread.detach();
}

fn servedOapReloadThread() void {
    served_oap_models.runReloads(std.heap.page_allocator, loadProductionServedModels);
}

fn startServingOapCatalog(allocator: std.mem.Allocator, server: *oap_provider_server.Server) !u64 {
    const before = served_oap_models.generation.load(.seq_cst);
    startServedOapReload();
    if (!@import("builtin").is_test) {
        const deadline = compat.time.nowMillis() + served_oap_startup_wait_ms;
        while (served_oap_models.generation.load(.seq_cst) == before and compat.time.nowMillis() < deadline) compat.time.sleepMs(10);
    }
    try populateOapProviderCatalog(allocator, server);
    return served_oap_models.generation.load(.seq_cst);
}

fn applyServedOapReload(allocator: std.mem.Allocator, server: *oap_provider_server.Server, cache: *ServedOapModels, applied: *u64) !bool {
    const current = cache.generation.load(.seq_cst);
    if (current == applied.*) return false;
    const models = try cache.snapshot(allocator);
    defer model_catalog.deinitModels(allocator, models);
    server.clearCatalog();
    try populateOapProviderCatalogFrom(allocator, server, models);
    try populateAwaitingOapProviders(allocator, server);
    applied.* = current;
    return true;
}

fn oapUnservedRefusal(provider_id: []const u8) ?oap_provider_server.UnservedRefusal {
    const row = provider_catalog.provider(provider_id) orelse return null;
    if (!rowAwaitsCredential(row)) return null;
    if (!oapCredentialResolves(std.heap.page_allocator, provider_id)) {
        return .{ .code = .credential_missing, .message = "this provider is served once it has a credential: sign in or set its key" };
    }
    if (served_oap_models.refusedKey(provider_id)) {
        return .{ .code = .credential_rejected, .message = "the provider refused this key: sign in again or set a new key" };
    }
    if (served_oap_models.claimStaleReload(compat.time.nowMillis(), served_oap_stale_reload_interval_ms)) startServedOapReload();
    return .{ .code = .provider_unavailable, .message = "this provider has a credential but its models are not loaded: retry after they reload" };
}

fn oapCredentialResolves(allocator: std.mem.Allocator, provider_id: []const u8) bool {
    if (provider_catalog.credentialEnvIsSet(allocator, provider_id) catch false) return true;
    var storage = oauth_storage.AuthStorage.loadDefaultStoredOnly(allocator) catch return false;
    defer storage.deinit();
    return storage.providers.get(provider_id) != null;
}

fn rowAwaitsCredential(row: provider_catalog.Provider) bool {
    for (row.auth) |kind| {
        if (kind == .none) return false;
    }
    return provider_catalog.firstImplementedWire(row) != null;
}

fn cloneOapModels(allocator: std.mem.Allocator, models: []const ai_types.Model) ![]ai_types.Model {
    const cloned = try allocator.alloc(ai_types.Model, models.len);
    var filled: usize = 0;
    errdefer {
        for (cloned[0..filled]) |*model| model.deinit(allocator);
        allocator.free(cloned);
    }
    for (models, cloned) |model, *slot| {
        slot.* = try ai_types.cloneModel(allocator, model);
        filled += 1;
    }
    return cloned;
}

fn servedOapModel(allocator: std.mem.Allocator, provider_id: []const u8, wire: oap_provider_types.Wire, wire_id: ?[]const u8, model_id: []const u8) !?ai_types.Model {
    const models = try loadServedOapModels(allocator);
    defer model_catalog.deinitModels(allocator, models);
    for (models) |model| {
        if (!std.mem.eql(u8, model.provider, provider_id)) continue;
        if (!std.mem.eql(u8, model.id, model_id)) continue;
        const mapping = oap_provider_catalog.mapApiToWire(model.api) orelse continue;
        if (mapping.wire != wire) continue;
        if (wire == .other and !sameWireId(mapping.wire_id, wire_id)) continue;
        return try ai_types.cloneModel(allocator, model);
    }
    return null;
}

fn awaitingOapModel(allocator: std.mem.Allocator, server: *oap_provider_server.Server, provider_id: []const u8, model_ref_text: []const u8) !?ai_types.Model {
    if (server.findAwaitingProvider(provider_id) == null) return null;
    return modelFromCanonicalRef(allocator, model_ref_text) catch |err| switch (err) {
        error.OutOfMemory => return error.OutOfMemory,
        else => return null,
    };
}

fn sameWireId(served: ?[]const u8, named: ?[]const u8) bool {
    const left = served orelse return false;
    const right = named orelse return false;
    return std.mem.eql(u8, left, right);
}

const oap_test_text_input = [_][]const u8{"text"};

const oap_test_served_models = [_]ai_types.Model{
    .{
        .id = "claude-sonnet-4-5",
        .name = "Claude Sonnet 4.5",
        .api = "anthropic-messages",
        .provider = "anthropic",
        .base_url = provider_catalog.baseUrlOrCompileError("anthropic", "anthropic-messages", null),
        .reasoning = true,
        .input = &oap_test_text_input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 200_000,
        .max_tokens = 8_192,
    },
    .{
        .id = "gpt-4o",
        .name = "GPT-4o",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = provider_catalog.baseUrlOrCompileError("openai", "openai-responses", null),
        .reasoning = false,
        .input = &oap_test_text_input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 16_384,
    },
    .{
        .id = "llama3",
        .name = "Llama 3",
        .api = "ollama",
        .provider = "ollama",
        .base_url = "http://127.0.0.1:11434",
        .reasoning = false,
        .input = &oap_test_text_input,
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 8_192,
        .allows_anonymous = true,
    },
};

const OAP_PROVIDER_STREAM_IDLE_TTL_DEFAULT_MS: i64 = 600_000;

fn oapProviderStreamIdleTtlMs(allocator: std.mem.Allocator) i64 {
    const raw = provider_base_url.envOwnedOrNull(allocator, "OAPX_OAP_PROVIDER_STREAM_IDLE_TTL_MS") catch
        return OAP_PROVIDER_STREAM_IDLE_TTL_DEFAULT_MS;
    const value = raw orelse return OAP_PROVIDER_STREAM_IDLE_TTL_DEFAULT_MS;
    defer allocator.free(value);
    const parsed = std.fmt.parseInt(i64, std.mem.trim(u8, value, " \t\r\n"), 10) catch
        return OAP_PROVIDER_STREAM_IDLE_TTL_DEFAULT_MS;
    if (parsed < 0) return OAP_PROVIDER_STREAM_IDLE_TTL_DEFAULT_MS;
    return parsed;
}

const OapGrantChannel = struct {
    nonce: []const u8,
    channel: oap_provider_grant_channel.GrantChannel,

    fn deinit(self: *OapGrantChannel, allocator: std.mem.Allocator) void {
        self.channel.deinit();
        allocator.free(self.nonce);
        self.* = undefined;
    }
};

const OapGrantedValue = struct {
    reference: []const u8,
    value: []const u8,

    fn deinit(self: *OapGrantedValue, allocator: std.mem.Allocator) void {
        allocator.free(self.reference);
        allocator.free(self.value);
        self.* = undefined;
    }
};

fn announceOapGrants(
    allocator: std.mem.Allocator,
    server: *oap_provider_server.Server,
    channels: *std.ArrayList(OapGrantChannel),
    ordinal: *u64,
) !bool {
    var did_work = false;
    while (server.nextUnannouncedGrant()) |grant| {
        const nonce = try allocator.dupe(u8, grant.nonce);
        errdefer allocator.free(nonce);

        var channel = oap_provider_grant_channel.GrantChannel.open(allocator, ordinal.*) catch {
            try server.refuseGrant(nonce, "the endpoint could not open a credential channel");
            allocator.free(nonce);
            did_work = true;
            continue;
        };
        ordinal.* += 1;
        errdefer channel.deinit();

        try channels.ensureUnusedCapacity(allocator, 1);
        try server.announceChannel(nonce, channel.path());
        channels.appendAssumeCapacity(.{ .nonce = nonce, .channel = channel });
        did_work = true;
    }
    return did_work;
}

fn pumpOapGrants(
    allocator: std.mem.Allocator,
    server: *oap_provider_server.Server,
    channels: *std.ArrayList(OapGrantChannel),
    granted: *std.ArrayList(OapGrantedValue),
    now_ms: i64,
) !bool {
    var did_work = false;
    var index: usize = 0;
    while (index < channels.items.len) {
        var entry = &channels.items[index];
        var settled = false;

        switch (try entry.channel.poll(entry.nonce)) {
            .pending => {},
            .rejected => {
                try server.refuseGrant(entry.nonce, "the credential channel closed without a value");
                settled = true;
            },
            .value => |value| {
                var owned_value = value;
                errdefer allocator.free(owned_value);
                const reference = try server.completeGrant(entry.nonce);
                try granted.ensureUnusedCapacity(allocator, 1);
                granted.appendAssumeCapacity(.{ .reference = reference, .value = owned_value });
                owned_value = &.{};
                settled = true;
            },
        }

        if (!settled and server.expiredGrantNonce(now_ms) != null) {
            try server.refuseGrant(entry.nonce, "no credential arrived before the deadline");
            settled = true;
        }

        if (!settled) {
            index += 1;
            continue;
        }

        var removed = channels.orderedRemove(index);
        removed.deinit(allocator);
        did_work = true;
    }

    server.burnExpiredGrants(now_ms);
    index = 0;
    while (index < granted.items.len) {
        if (server.holdsGrant(granted.items[index].reference)) {
            index += 1;
            continue;
        }
        var dropped = granted.orderedRemove(index);
        dropped.deinit(allocator);
        did_work = true;
    }
    return did_work;
}

fn grantedValueFor(granted: []const OapGrantedValue, reference: []const u8) ?[]const u8 {
    for (granted) |entry| {
        if (std.mem.eql(u8, entry.reference, reference)) return entry.value;
    }
    return null;
}

const RunningOapInference = struct {
    inference_id: []const u8,
    stream: *event_stream.AssistantMessageStream,
    context: ai_types.Context,
    model: ai_types.Model,
    cancelled: *std.atomic.Value(bool),
    last_progress_ms: i64,

    fn deinit(self: *RunningOapInference, allocator: std.mem.Allocator) void {
        self.cancelled.store(true, .release);
        _ = self.stream.deinitAndDestroy();
        if (self.inference_id.len > 0) allocator.free(self.inference_id);
        self.context.deinit(allocator);
        self.model.deinit(allocator);
        allocator.destroy(self.cancelled);
    }
};

fn failOapInference(
    server: *oap_provider_server.Server,
    inference_id: []const u8,
    code: oap_provider_types.ErrorCode,
    message: []const u8,
) !void {
    try server.settleFailed(inference_id, code, message, null);
    server.releaseInference(inference_id);
}

fn resolveOapStoredCredential(
    allocator: std.mem.Allocator,
    provider_id: []const u8,
    base_url: []const u8,
    lookup: auth_resolver.OverrideLookup,
) ?auth_resolver.ResolvedKey {
    if (auth_resolver.storedCredentialWithheld(allocator, lookup, provider_id, base_url)) {
        return auth_resolver.resolveApiKeyOfKind(allocator, null, provider_id, null, .api_key_only) catch null;
    }
    var storage = oauth_storage.AuthStorage.loadDefaultStoredOnly(allocator) catch return null;
    defer storage.deinit();
    const resolved = auth_resolver.resolveApiKey(allocator, &storage, provider_id, null) catch return null;
    if (resolved.api_key.len == 0) {
        var owned = resolved;
        owned.deinit(allocator);
        return null;
    }
    return resolved;
}

fn startOapInference(
    allocator: std.mem.Allocator,
    registry: *api_registry.ApiRegistry,
    server: *oap_provider_server.Server,
    running: *std.ArrayList(RunningOapInference),
    inference_id: []const u8,
    granted: []const OapGrantedValue,
) !void {
    const inference = server.findInference(inference_id) orelse return;

    const parsed = oap_provider_server.Server.parseModelRef(inference.model_ref) orelse {
        try failOapInference(server, inference_id, .invalid_request, "model_ref is not parseable");
        return;
    };
    const provider_id = parsed.provider_id;

    var served = (try servedOapModel(allocator, provider_id, parsed.wire, parsed.wire_id, parsed.model_id)) orelse
        (try awaitingOapModel(allocator, server, provider_id, inference.model_ref)) orelse {
        try failOapInference(server, inference_id, .model_not_found, "this endpoint serves no such model");
        return;
    };
    defer served.deinit(allocator);

    const provider = registry.getApiProvider(served.api) orelse {
        try failOapInference(server, inference_id, .provider_unavailable, "the api is not registered");
        return;
    };

    var lookup = try auth_resolver.overrideLookup(allocator, provider_id);
    defer lookup.deinit(allocator);
    var model = try buildOapInferenceModel(allocator, served, lookup);
    errdefer model.deinit(allocator);
    var context = try buildOapInferenceContext(allocator, inference.messages, .{
        .provider = model.provider,
        .api = model.api,
        .model_id = model.id,
    });
    errdefer context.deinit(allocator);
    context.tools = try buildOapInferenceTools(allocator, inference.tools);

    const cancelled = try allocator.create(std.atomic.Value(bool));
    errdefer allocator.destroy(cancelled);
    cancelled.* = std.atomic.Value(bool).init(false);

    var options: ai_types.StreamOptions = .{};
    var resolved_credential: ?auth_resolver.ResolvedKey = null;
    defer if (resolved_credential) |*key| key.deinit(allocator);

    if (options.getApiKey() == null) if (inference.credential_ref) |reference| {
        if (grantedValueFor(granted, reference)) |value| options.api_key = @TypeOf(options.api_key).initBorrowed(value);
    };
    if (options.getApiKey() == null or options.getApiKey().?.len == 0) {
        resolved_credential = resolveOapStoredCredential(allocator, provider_id, model.base_url, lookup);
        if (resolved_credential) |key| options.api_key = @TypeOf(options.api_key).initBorrowed(key.api_key);
    }
    options.cancel_token = .{ .cancelled = cancelled };
    if (inference.max_output_tokens) |max| options.max_tokens = max;
    if (inference.temperature) |value| options.temperature = value;
    if (inference.tool_choice) |choice| options.tool_choice = switch (choice) {
        .auto => ai_types.ToolChoice{ .auto = {} },
        .none => ai_types.ToolChoice{ .none = {} },
        .required => ai_types.ToolChoice{ .required = {} },
        .function => |name| ai_types.ToolChoice{ .function = name },
    };
    applyOapReasoning(&options, inference.reasoning);

    const stream = provider.stream(model, context, options, allocator) catch |err| {
        const code: oap_provider_types.ErrorCode = switch (err) {
            error.MissingApiKey, error.AuthRequired => .credential_missing,
            error.AuthRefreshFailed => .credential_expired,
            else => .provider_unavailable,
        };
        const message = switch (err) {
            error.MissingApiKey, error.AuthRequired => "this provider needs a credential and none resolved",
            error.AuthRefreshFailed => "the stored credential could not be refreshed",
            else => "the provider refused the request",
        };
        model.deinit(allocator);
        context.deinit(allocator);
        allocator.destroy(cancelled);
        try failOapInference(server, inference_id, code, message);
        return;
    };
    errdefer {
        cancelled.store(true, .release);
        _ = stream.deinitAndDestroy();
    }

    const owned_id = try allocator.dupe(u8, inference_id);
    errdefer allocator.free(owned_id);
    try running.ensureUnusedCapacity(allocator, 1);

    running.appendAssumeCapacity(.{
        .inference_id = owned_id,
        .stream = stream,
        .context = context,
        .model = model,
        .cancelled = cancelled,
        .last_progress_ms = compat.time.nowMillis(),
    });
}

fn pumpOapInferences(
    allocator: std.mem.Allocator,
    server: *oap_provider_server.Server,
    running: *std.ArrayList(RunningOapInference),
    idle_ttl_ms: i64,
) !bool {
    var did_work = false;
    var index: usize = 0;
    while (index < running.items.len) {
        const entry = &running.items[index];
        var settled = false;

        if (server.findInference(entry.inference_id)) |inference| {
            if (inference.cancel_requested) entry.cancelled.store(true, .release);
        }

        if (idle_ttl_ms > 0 and compat.time.nowMillis() - entry.last_progress_ms > idle_ttl_ms) {
            entry.cancelled.store(true, .release);
        }

        while (entry.stream.poll()) |event| {
            did_work = true;
            entry.last_progress_ms = compat.time.nowMillis();
            defer entry.stream.releaseEvent(event);
            const terminal = event == .done or event == .@"error";
            oap_provider_runtime.pumpEvent(server, entry.inference_id, event) catch {
                if (terminal) {
                    server.abandonOpenPart(entry.inference_id);
                    server.settleFailed(
                        entry.inference_id,
                        .endpoint_error,
                        "the endpoint could not deliver the terminal for this inference",
                        null,
                    ) catch {};
                }
            };
            if (terminal) settled = true;
        }

        if (!settled and entry.stream.isDone()) {
            settleOapInference(server, entry) catch {
                server.abandonOpenPart(entry.inference_id);
                server.settleFailed(
                    entry.inference_id,
                    .endpoint_error,
                    "the endpoint could not assemble a terminal for this inference",
                    null,
                ) catch {};
            };
            settled = true;
            did_work = true;
        }

        if (!settled) {
            index += 1;
            continue;
        }

        var removed = running.orderedRemove(index);
        server.releaseInference(removed.inference_id);
        removed.deinit(allocator);
    }
    return did_work;
}

fn settleOapInference(
    server: *oap_provider_server.Server,
    entry: *const RunningOapInference,
) !void {
    if (entry.stream.getError()) |message| {
        const cancelled_by_caller = if (server.findInference(entry.inference_id)) |inference|
            inference.cancel_requested
        else
            false;
        if (cancelled_by_caller) {
            try server.settleFailed(
                entry.inference_id,
                .aborted,
                "the caller cancelled this inference",
                null,
            );
        } else {
            try server.settleFailed(entry.inference_id, oap_provider_runtime.failureCode(message), message, null);
        }
        return;
    }

    const result = entry.stream.getResult() orelse {
        try server.settleFailed(
            entry.inference_id,
            .provider_unavailable,
            "the provider stream ended with no result",
            null,
        );
        return;
    };

    const usage = oap_types.Usage{
        .input_tokens = result.usage.input,
        .output_tokens = result.usage.output,
        .total_tokens = if (result.usage.total_tokens > 0)
            result.usage.total_tokens
        else
            result.usage.input + result.usage.output,
    };

    if (result.stop_reason == .@"error") {
        try server.settleFailed(
            entry.inference_id,
            .provider_unavailable,
            result.getErrorMessage() orelse "the provider reported a failure",
            usage,
        );
        return;
    }

    try server.settleCompletedFromResult(
        entry.inference_id,
        oap_provider_runtime.mapStopReason(result.stop_reason),
        usage,
        result.content,
    );
}

fn buildOapInferenceModel(
    allocator: std.mem.Allocator,
    served: ai_types.Model,
    lookup: auth_resolver.OverrideLookup,
) !ai_types.Model {
    var model = try ai_types.cloneModel(allocator, served);
    errdefer model.deinit(allocator);
    const file = switch (lookup) {
        .endpoint => |found| found,
        else => null,
    };
    if (file) |found| {
        if (found.base_url.len > 0) {
            const base_url = provider_base_url.defaultBaseUrlForRefWithFile(allocator, served.provider, served.api, null, found.base_url) catch
                try allocator.dupe(u8, found.base_url);
            allocator.free(model.base_url);
            model.base_url = base_url;
        }
    }
    const from_file = if (file) |found| auth_resolver.overrideApplies(allocator, found, served.provider, model.base_url) else false;
    if (from_file and file.?.base_url.len > 0) model.carries_version = file.?.carries_version;
    if (from_file and file.?.headers.len > 0) {
        const headers = try dupeHeaderPairs(allocator, file.?.headers);
        if (model.headers) |previous| {
            for (previous) |header| {
                allocator.free(header.name);
                allocator.free(header.value);
            }
            allocator.free(previous);
        }
        model.headers = headers;
    }
    model.credential_withheld = auth_resolver.storedCredentialWithheld(allocator, lookup, served.provider, model.base_url);
    return model;
}

fn dupeHeaderPairs(allocator: std.mem.Allocator, given: []const ai_types.HeaderPair) ![]ai_types.HeaderPair {
    const copied = try allocator.alloc(ai_types.HeaderPair, given.len);
    var filled: usize = 0;
    errdefer {
        for (copied[0..filled]) |header| {
            allocator.free(header.name);
            allocator.free(header.value);
        }
        allocator.free(copied);
    }
    for (given, copied) |header, *slot| {
        const name = try allocator.dupe(u8, header.name);
        errdefer allocator.free(name);
        slot.* = .{ .name = name, .value = try allocator.dupe(u8, header.value) };
        filled += 1;
    }
    return copied;
}

fn oapRoleIsSystem(role: oap_types.Role) bool {
    return role == .system or role == .developer;
}

fn oapMessageText(allocator: std.mem.Allocator, message: oap_types.Message) ![]const u8 {
    return switch (message.content) {
        .text => |value| try allocator.dupe(u8, value),
        .parts => |parts| blk: {
            var buffer = std.ArrayList(u8).empty;
            errdefer buffer.deinit(allocator);
            for (parts) |part| {
                switch (part) {
                    .text => |value| try buffer.appendSlice(allocator, value),
                    else => {},
                }
            }
            break :blk try buffer.toOwnedSlice(allocator);
        },
    };
}

fn buildOapInferenceTools(
    allocator: std.mem.Allocator,
    source: []const oap_provider_types.ToolDefinition,
) !?[]const ai_types.Tool {
    if (source.len == 0) return null;
    const out = try allocator.alloc(ai_types.Tool, source.len);
    var built: usize = 0;
    errdefer {
        for (out[0..built]) |*tool| tool.deinit(allocator);
        allocator.free(out);
    }
    for (source, 0..) |tool, index| {
        const name = try allocator.dupe(u8, tool.name);
        errdefer allocator.free(name);
        const description = try allocator.dupe(u8, tool.description orelse "");
        errdefer allocator.free(description);
        const schema = try allocator.dupe(u8, tool.input_schema_json orelse "{\"type\":\"object\"}");
        out[index] = .{ .name = name, .description = description, .parameters_schema_json = schema };
        built += 1;
    }
    return out;
}

fn applyOapReasoning(options: *ai_types.StreamOptions, reasoning: ?oap_provider_types.ReasoningOptions) void {
    const source = reasoning orelse return;
    if (source.enabled) |enabled| {
        options.thinking_enabled = enabled;
        options.reasoning_enabled = enabled;
    }
    if (source.budget_tokens) |budget| options.thinking_budget_tokens = budget;
    if (source.effort) |effort| {
        options.thinking_effort = @TypeOf(options.thinking_effort).initBorrowed(effort);
        options.reasoning_effort = @TypeOf(options.reasoning_effort).initBorrowed(effort);
    }
}

const OapModelIdentity = struct {
    provider: []const u8,
    api: []const u8,
    model_id: []const u8,
};

fn buildOapInferenceContext(
    allocator: std.mem.Allocator,
    source: []const oap_types.Message,
    identity: OapModelIdentity,
) !ai_types.Context {
    var system = std.ArrayList(u8).empty;
    errdefer system.deinit(allocator);

    var built_messages = std.ArrayList(ai_types.Message).empty;
    errdefer {
        for (built_messages.items) |*message| message.deinit(allocator);
        built_messages.deinit(allocator);
    }

    for (source) |message| {
        if (message.role == .assistant) {
            const content = try oapAssistantContent(allocator, message);
            const assistant = try buildOapAssistantMessage(allocator, content, identity);
            var assistant_transferred = false;
            errdefer if (!assistant_transferred) {
                var owned = assistant;
                owned.deinit(allocator);
            };
            try built_messages.ensureUnusedCapacity(allocator, 1);
            built_messages.appendAssumeCapacity(.{ .assistant = assistant });
            assistant_transferred = true;
            continue;
        }

        if (oapMessageToolResults(message)) |parts| {
            for (parts) |part| {
                if (part != .tool_result) continue;
                const result = try buildOapToolResult(allocator, part.tool_result, source);
                var result_transferred = false;
                errdefer if (!result_transferred) {
                    var owned = result;
                    owned.deinit(allocator);
                };
                try built_messages.ensureUnusedCapacity(allocator, 1);
                built_messages.appendAssumeCapacity(.{ .tool_result = result });
                result_transferred = true;
            }

            const spoken = try oapMessageText(allocator, message);
            errdefer allocator.free(spoken);
            if (spoken.len == 0) {
                allocator.free(spoken);
                continue;
            }
            try built_messages.ensureUnusedCapacity(allocator, 1);
            built_messages.appendAssumeCapacity(.{
                .user = .{ .content = .{ .text = spoken }, .timestamp = compat.time.nowMillis() },
            });
            continue;
        }

        const text = try oapMessageText(allocator, message);
        if (oapRoleIsSystem(message.role)) {
            defer allocator.free(text);
            if (system.items.len > 0) try system.appendSlice(allocator, "\n\n");
            try system.appendSlice(allocator, text);
            continue;
        }
        errdefer allocator.free(text);
        try built_messages.ensureUnusedCapacity(allocator, 1);
        built_messages.appendAssumeCapacity(.{ .user = .{ .content = .{ .text = text }, .timestamp = compat.time.nowMillis() } });
    }

    const messages = try built_messages.toOwnedSlice(allocator);
    errdefer {
        for (messages) |*message| message.deinit(allocator);
        allocator.free(messages);
    }

    const system_prompt = try system.toOwnedSlice(allocator);
    errdefer allocator.free(system_prompt);

    return ai_types.Context{
        .system_prompt = ai_types.OwnedSlice(u8).initOwned(system_prompt),
        .messages = messages,
        .is_owned = true,
    };
}

fn oapMessageToolResults(message: oap_types.Message) ?[]const oap_types.ContentPart {
    const parts = switch (message.content) {
        .parts => |value| value,
        else => return null,
    };
    for (parts) |part| {
        if (part == .tool_result) return parts;
    }
    return null;
}

fn oapToolNameForCall(source: []const oap_types.Message, tool_call_id: []const u8) []const u8 {
    for (source) |message| {
        const parts = switch (message.content) {
            .parts => |value| value,
            else => continue,
        };
        for (parts) |part| {
            if (part != .tool_call) continue;
            if (std.mem.eql(u8, part.tool_call.tool_call_id, tool_call_id)) return part.tool_call.name;
        }
    }
    return "";
}

fn buildOapToolResult(
    allocator: std.mem.Allocator,
    part: oap_types.ToolResultPart,
    source: []const oap_types.Message,
) !ai_types.ToolResultMessage {
    const id = try allocator.dupe(u8, part.tool_call_id);
    errdefer allocator.free(id);
    const name = try allocator.dupe(u8, oapToolNameForCall(source, part.tool_call_id));
    errdefer allocator.free(name);
    const body = try allocator.dupe(u8, part.result_json);
    errdefer allocator.free(body);
    const content = try allocator.alloc(ai_types.UserContentPart, 1);
    content[0] = .{ .text = .{ .text = body } };

    return ai_types.ToolResultMessage{
        .tool_call_id = id,
        .tool_name = name,
        .content = content,
        .is_error = part.is_error orelse false,
        .timestamp = compat.time.nowMillis(),
    };
}

fn oapAssistantContent(
    allocator: std.mem.Allocator,
    message: oap_types.Message,
) ![]ai_types.AssistantContent {
    var blocks = std.ArrayList(ai_types.AssistantContent).empty;
    errdefer {
        ai_types.deinitAssistantContentElements(allocator, blocks.items);
        blocks.deinit(allocator);
    }

    switch (message.content) {
        .text => |value| {
            const owned = try allocator.dupe(u8, value);
            errdefer allocator.free(owned);
            try blocks.append(allocator, .{ .text = .{ .text = owned } });
        },
        .parts => |parts| {
            for (parts) |part| switch (part) {
                .text => |value| {
                    const owned = try allocator.dupe(u8, value);
                    errdefer allocator.free(owned);
                    try blocks.append(allocator, .{ .text = .{ .text = owned } });
                },
                .reasoning => |value| {
                    const owned = try allocator.dupe(u8, value.text);
                    errdefer allocator.free(owned);
                    const signature = if (value.carry) |carry| try allocator.dupe(u8, carry) else null;
                    errdefer if (signature) |owned_carry| allocator.free(owned_carry);
                    try blocks.append(allocator, .{ .thinking = .{
                        .thinking = owned,
                        .thinking_signature = signature,
                    } });
                },
                .tool_call => |value| {
                    const id = try allocator.dupe(u8, value.tool_call_id);
                    errdefer allocator.free(id);
                    const name = try allocator.dupe(u8, value.name);
                    errdefer allocator.free(name);
                    const arguments = try allocator.dupe(u8, value.arguments_json);
                    errdefer allocator.free(arguments);
                    const signature = if (value.carry) |carry| try allocator.dupe(u8, carry) else null;
                    errdefer if (signature) |owned_carry| allocator.free(owned_carry);
                    try blocks.append(allocator, .{ .tool_call = .{
                        .id = id,
                        .name = name,
                        .arguments_json = arguments,
                        .thought_signature = signature,
                    } });
                },
                else => {},
            };
        },
    }

    return blocks.toOwnedSlice(allocator);
}

fn buildOapAssistantMessage(
    allocator: std.mem.Allocator,
    content: []ai_types.AssistantContent,
    identity: OapModelIdentity,
) !ai_types.AssistantMessage {
    errdefer ai_types.deinitAssistantContent(allocator, content);
    const api = try allocator.dupe(u8, identity.api);
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, identity.provider);
    errdefer allocator.free(provider);
    const model = try allocator.dupe(u8, identity.model_id);

    return ai_types.AssistantMessage{
        .content = content,
        .api = api,
        .provider = provider,
        .model = model,
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = compat.time.nowMillis(),
        .is_owned = true,
    };
}

fn oapSpecimenRequestId(line: []const u8, allocator: std.mem.Allocator) !?[]const u8 {
    if (std.mem.indexOf(u8, line, "\"control\"") == null) return null;
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch return null;
    defer parsed.deinit();
    if (parsed.value != .object) return null;
    const control = parsed.value.object.get("control") orelse return null;
    if (control != .string) return null;
    if (!std.mem.eql(u8, control.string, "specimen")) return null;
    const id = parsed.value.object.get("id") orelse return try allocator.dupe(u8, "");
    if (id != .string) return try allocator.dupe(u8, "");
    return try allocator.dupe(u8, id.string);
}

const HttpProviderFrame = struct {
    line: []const u8,
    terminal: bool,
};

const HTTP_PROVIDER_MAX_WORKERS: usize = 128;

const HttpProviderExchange = struct {
    request_id: []const u8,
    inference_id: ?[]u8 = null,
    frames: std.ArrayList(HttpProviderFrame) = .empty,

    fn deinit(self: *HttpProviderExchange, allocator: std.mem.Allocator) void {
        if (self.inference_id) |id| allocator.free(id);
        for (self.frames.items) |frame| allocator.free(frame.line);
        self.frames.deinit(allocator);
    }
};

const HttpProviderRuntime = struct {
    allocator: std.mem.Allocator,
    mutex: std.Io.Mutex = .init,
    registry: api_registry.ApiRegistry,
    server: oap_provider_server.Server,
    running: std.ArrayList(RunningOapInference) = .empty,
    exchanges: std.ArrayList(*HttpProviderExchange) = .empty,
    idle_ttl_ms: i64,
    workers: std.atomic.Value(usize) = std.atomic.Value(usize).init(0),
    stopping: std.atomic.Value(bool) = std.atomic.Value(bool).init(false),
    served_generation: u64 = 0,

    fn init(allocator: std.mem.Allocator) !HttpProviderRuntime {
        var runtime = HttpProviderRuntime{
            .allocator = allocator,
            .registry = api_registry.ApiRegistry.init(allocator),
            .server = oap_provider_server.Server.init(allocator, .{
                .unserved_refusal = oapUnservedRefusal,
                .capability_revision = VERSION,
                .grant_channel = .unsupported,
                .accepts_inference = true,
                .resolves_own_credentials = true,
                .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
                .catalog = oapFallbackCatalogState(),
            }),
            .idle_ttl_ms = oapProviderStreamIdleTtlMs(allocator),
        };
        errdefer runtime.deinit();
        try register_builtins.registerBuiltInApiProviders(&runtime.registry);
        runtime.served_generation = try startServingOapCatalog(allocator, &runtime.server);
        return runtime;
    }

    fn deinit(self: *HttpProviderRuntime) void {
        for (self.running.items) |*entry| entry.deinit(self.allocator);
        self.running.deinit(self.allocator);
        self.exchanges.deinit(self.allocator);
        self.server.deinit();
        self.registry.deinit();
    }

    fn dispatch(self: *HttpProviderRuntime, current: ?*HttpProviderExchange) !void {
        while (self.server.popOutbound()) |line| {
            var delivered = false;
            defer if (!delivered) self.allocator.free(line);
            var env = try oap_provider_envelope.deserializeEnvelope(line, self.allocator);
            defer env.deinit(self.allocator);
            var target: ?*HttpProviderExchange = null;
            if (env.in_reply_to) |request_id| {
                if (current) |exchange| {
                    if (std.mem.eql(u8, exchange.request_id, request_id)) target = exchange;
                }
            }
            if (target == null) if (env.in_reply_to) |request_id| {
                for (self.exchanges.items) |exchange| {
                    if (std.mem.eql(u8, exchange.request_id, request_id)) {
                        target = exchange;
                        break;
                    }
                }
            };
            if (target == null) if (env.inference_id) |inference_id| {
                for (self.exchanges.items) |exchange| {
                    if (exchange.inference_id) |owned_id| {
                        if (std.mem.eql(u8, owned_id, inference_id)) {
                            target = exchange;
                            break;
                        }
                    }
                }
            };
            const exchange = target orelse continue;
            if (env.payload == .inference_create_response and env.payload.inference_create_response.accepted) {
                if (exchange.inference_id == null) exchange.inference_id = try self.allocator.dupe(u8, env.inference_id orelse return error.MissingInferenceId);
            }
            const terminal = switch (env.payload) {
                .inference_completed, .inference_failed, .protocol_error => true,
                .inference_create_response => |answer| !answer.accepted,
                else => false,
            };
            try exchange.frames.append(self.allocator, .{ .line = line, .terminal = terminal });
            delivered = true;
        }
    }
};

fn httpProviderIo() std.Io {
    return if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
}

fn readHttpProviderBody(allocator: std.mem.Allocator, stream: *compat.net.Stream) ![]u8 {
    var header: std.ArrayList(u8) = .empty;
    defer header.deinit(allocator);
    var byte: [1]u8 = undefined;
    while (header.items.len < 16 * 1024) {
        const n = try stream.read(&byte);
        if (n == 0) return error.IncompleteHttpRequest;
        try header.append(allocator, byte[0]);
        if (std.mem.endsWith(u8, header.items, "\r\n\r\n")) break;
    }
    if (!std.mem.endsWith(u8, header.items, "\r\n\r\n")) return error.HttpHeadersTooLarge;
    var lines = std.mem.splitSequence(u8, header.items, "\r\n");
    const request_line = lines.next() orelse return error.InvalidHttpRequest;
    if (!std.mem.startsWith(u8, request_line, "POST ")) return error.HttpMethodNotAllowed;
    if (!std.mem.eql(u8, request_line, "POST /oap/v0.1/provider HTTP/1.1")) return error.HttpNotFound;
    var content_length: ?usize = null;
    var json_content_type = false;
    while (lines.next()) |line| {
        if (line.len == 0) break;
        if (std.ascii.startsWithIgnoreCase(line, "content-length:")) {
            if (content_length != null) return error.InvalidHttpRequest;
            content_length = std.fmt.parseInt(usize, std.mem.trim(u8, line["content-length:".len..], " \t"), 10) catch return error.InvalidHttpRequest;
        } else if (std.ascii.startsWithIgnoreCase(line, "content-type:")) {
            const value = std.mem.trim(u8, line["content-type:".len..], " \t");
            json_content_type = std.mem.eql(u8, value, "application/json") or std.mem.startsWith(u8, value, "application/json;");
        } else if (std.ascii.startsWithIgnoreCase(line, "transfer-encoding:")) {
            return error.UnsupportedHttpTransferEncoding;
        }
    }
    if (!json_content_type) return error.UnsupportedHttpMediaType;
    const length = content_length orelse return error.InvalidHttpRequest;
    if (length > 1024 * 1024) return error.HttpBodyTooLarge;
    const body = try allocator.alloc(u8, length);
    errdefer allocator.free(body);
    var filled: usize = 0;
    while (filled < body.len) {
        const n = try stream.read(body[filled..]);
        if (n == 0) return error.IncompleteHttpRequest;
        filled += n;
    }
    return body;
}

fn writeHttpProviderStatus(stream: *compat.net.Stream, status: []const u8) !void {
    var buffer: [160]u8 = undefined;
    const header = try std.fmt.bufPrint(&buffer, "HTTP/1.1 {s}\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", .{status});
    try stream.writeAll(header);
}

fn writeHttpProviderJson(stream: *compat.net.Stream, body: []const u8) !void {
    var buffer: [160]u8 = undefined;
    const header = try std.fmt.bufPrint(&buffer, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {d}\r\nConnection: close\r\n\r\n", .{body.len});
    try stream.writeAll(header);
    try stream.writeAll(body);
}

fn httpProviderRequestErrorStatus(err: anyerror) []const u8 {
    return switch (err) {
        error.HttpMethodNotAllowed => "405 Method Not Allowed",
        error.HttpNotFound => "404 Not Found",
        error.HttpHeadersTooLarge, error.HttpBodyTooLarge => "413 Content Too Large",
        error.UnsupportedHttpMediaType => "415 Unsupported Media Type",
        else => "400 Bad Request",
    };
}

fn unregisterHttpProviderExchange(runtime: *HttpProviderRuntime, exchange: *HttpProviderExchange) void {
    runtime.mutex.lockUncancelable(httpProviderIo());
    defer runtime.mutex.unlock(httpProviderIo());
    for (runtime.exchanges.items, 0..) |candidate, index| {
        if (candidate == exchange) {
            _ = runtime.exchanges.orderedRemove(index);
            return;
        }
    }
}

fn popHttpProviderFrame(runtime: *HttpProviderRuntime, exchange: *HttpProviderExchange) !?HttpProviderFrame {
    runtime.mutex.lockUncancelable(httpProviderIo());
    defer runtime.mutex.unlock(httpProviderIo());
    try runtime.dispatch(null);
    if (exchange.frames.items.len == 0) return null;
    return exchange.frames.orderedRemove(0);
}

fn pumpHttpProvider(runtime: *HttpProviderRuntime) void {
    while (!runtime.stopping.load(.acquire)) {
        runtime.mutex.lockUncancelable(httpProviderIo());
        _ = applyServedOapReload(runtime.allocator, &runtime.server, &served_oap_models, &runtime.served_generation) catch false;
        _ = pumpOapInferences(runtime.allocator, &runtime.server, &runtime.running, runtime.idle_ttl_ms) catch {};
        runtime.dispatch(null) catch {};
        runtime.mutex.unlock(httpProviderIo());
        compat.time.sleepMs(1);
    }
}

fn cancelHttpProviderInference(runtime: *HttpProviderRuntime, inference_id: []const u8) void {
    const cancel: oap_provider_types.Envelope = .{
        .id = "http-abandon",
        .inference_id = inference_id,
        .payload = .{ .inference_cancel_request = .{ .reason = "HTTP stream closed" } },
    };
    const line = oap_provider_envelope.serializeEnvelope(cancel, runtime.allocator) catch return;
    defer runtime.allocator.free(line);
    runtime.mutex.lockUncancelable(httpProviderIo());
    defer runtime.mutex.unlock(httpProviderIo());
    runtime.server.handleLine(line) catch return;
    runtime.dispatch(null) catch {};
}

fn httpProviderDecodeError(runtime: *HttpProviderRuntime, body: []const u8) ![]const u8 {
    runtime.mutex.lockUncancelable(httpProviderIo());
    defer runtime.mutex.unlock(httpProviderIo());
    try runtime.dispatch(null);
    try runtime.server.handleLine(body);
    return runtime.server.popOutbound() orelse error.MissingHttpProviderResponse;
}

fn handleHttpProviderConnection(runtime: *HttpProviderRuntime, connection: compat.net.Connection) void {
    defer _ = runtime.workers.fetchSub(1, .acq_rel);
    var conn = connection;
    defer conn.stream.close();
    handleHttpProviderConnectionFallible(runtime, &conn.stream) catch {};
}

fn handleHttpProviderConnectionFallible(runtime: *HttpProviderRuntime, stream: *compat.net.Stream) !void {
    const allocator = runtime.allocator;
    const body = readHttpProviderBody(allocator, stream) catch |err| {
        try writeHttpProviderStatus(stream, httpProviderRequestErrorStatus(err));
        return;
    };
    defer allocator.free(body);
    var request = oap_provider_envelope.deserializeEnvelope(body, allocator) catch {
        var parsed_json = std.json.parseFromSlice(std.json.Value, allocator, body, .{}) catch {
            try writeHttpProviderStatus(stream, "400 Bad Request");
            return;
        };
        defer parsed_json.deinit();
        if (parsed_json.value != .object) {
            try writeHttpProviderStatus(stream, "400 Bad Request");
            return;
        }
        const answer = try httpProviderDecodeError(runtime, body);
        defer allocator.free(answer);
        try writeHttpProviderJson(stream, answer);
        return;
    };
    defer request.deinit(allocator);
    const is_stream = request.payload == .inference_create_request;
    var exchange = HttpProviderExchange{ .request_id = request.id };
    defer exchange.deinit(allocator);
    runtime.mutex.lockUncancelable(httpProviderIo());
    runtime.exchanges.append(allocator, &exchange) catch |err| {
        runtime.mutex.unlock(httpProviderIo());
        return err;
    };
    runtime.server.handleLine(body) catch |err| {
        runtime.mutex.unlock(httpProviderIo());
        unregisterHttpProviderExchange(runtime, &exchange);
        return err;
    };
    while (runtime.server.popPendingStart()) |inference_id| {
        defer allocator.free(inference_id);
        startOapInference(allocator, &runtime.registry, &runtime.server, &runtime.running, inference_id, &.{}) catch |err| {
            runtime.mutex.unlock(httpProviderIo());
            unregisterHttpProviderExchange(runtime, &exchange);
            return err;
        };
    }
    runtime.dispatch(&exchange) catch |err| {
        runtime.mutex.unlock(httpProviderIo());
        unregisterHttpProviderExchange(runtime, &exchange);
        return err;
    };
    runtime.mutex.unlock(httpProviderIo());
    defer unregisterHttpProviderExchange(runtime, &exchange);

    if (!is_stream) {
        const frame = (try popHttpProviderFrame(runtime, &exchange)) orelse {
            try writeHttpProviderStatus(stream, "500 Internal Server Error");
            return;
        };
        defer allocator.free(frame.line);
        try writeHttpProviderJson(stream, frame.line);
        return;
    }

    var settled = false;
    defer if (!settled) {
        if (exchange.inference_id) |inference_id| cancelHttpProviderInference(runtime, inference_id);
    };
    try stream.writeAll("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\nCache-Control: no-cache\r\nConnection: close\r\n\r\n");
    var last_progress_ms = compat.time.nowMillis();
    while (true) {
        const frame = try popHttpProviderFrame(runtime, &exchange);
        if (frame) |value| {
            defer allocator.free(value.line);
            try stream.writeAll("data: ");
            try stream.writeAll(value.line);
            try stream.writeAll("\n\n");
            last_progress_ms = compat.time.nowMillis();
            if (value.terminal) {
                settled = true;
                break;
            }
        } else {
            if (runtime.idle_ttl_ms > 0 and compat.time.nowMillis() - last_progress_ms > runtime.idle_ttl_ms) return error.HttpProviderStreamTimedOut;
            compat.time.sleepMs(1);
        }
    }
}

fn runOapProviderHttpMode(allocator: std.mem.Allocator, bind: []const u8) !void {
    const separator = std.mem.lastIndexOfScalar(u8, bind, ':') orelse return error.InvalidHttpBind;
    if (!std.mem.eql(u8, bind[0..separator], "127.0.0.1")) return error.HttpProviderMustBindLoopback;
    const port = std.fmt.parseInt(u16, bind[separator + 1 ..], 10) catch return error.InvalidHttpBind;
    if (port == 0) return error.InvalidHttpBind;
    const address = try compat.net.resolveAddress(allocator, "127.0.0.1", port);
    var listener = try compat.net.tcpListen(address, .{ .reuse_address = true });
    defer compat.net.closeServer(&listener);
    var runtime = try HttpProviderRuntime.init(allocator);
    const pump_thread = std.Thread.spawn(.{}, pumpHttpProvider, .{&runtime}) catch |err| {
        runtime.deinit();
        return err;
    };
    defer {
        while (runtime.workers.load(.acquire) > 0) compat.time.sleepMs(1);
        runtime.stopping.store(true, .release);
        pump_thread.join();
        runtime.deinit();
    }
    while (true) {
        const connection = try compat.net.accept(&listener);
        const existing = runtime.workers.fetchAdd(1, .acq_rel);
        if (existing >= HTTP_PROVIDER_MAX_WORKERS) {
            _ = runtime.workers.fetchSub(1, .acq_rel);
            var rejected = connection;
            writeHttpProviderStatus(&rejected.stream, "503 Service Unavailable") catch {};
            rejected.stream.close();
            continue;
        }
        const thread = std.Thread.spawn(.{}, handleHttpProviderConnection, .{ &runtime, connection }) catch |err| {
            _ = runtime.workers.fetchSub(1, .acq_rel);
            var failed = connection;
            failed.stream.close();
            return err;
        };
        thread.detach();
    }
}

test "HTTP provider listener requires a literal loopback bind" {
    try std.testing.expectError(error.HttpProviderMustBindLoopback, runOapProviderHttpMode(std.testing.allocator, "0.0.0.0:8080"));
    try std.testing.expectError(error.HttpProviderMustBindLoopback, runOapProviderHttpMode(std.testing.allocator, "provider.default.svc:8080"));
    try std.testing.expectError(error.InvalidHttpBind, runOapProviderHttpMode(std.testing.allocator, "127.0.0.1:0"));
}

test "HTTP provider runtime advertises managed credentials and routes discovery" {
    const allocator = std.testing.allocator;
    var runtime = try HttpProviderRuntime.init(allocator);
    defer runtime.deinit();
    const request = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"provider.describe.request\",\"id\":\"http-test-describe\",\"payload\":{}}";
    var exchange = HttpProviderExchange{ .request_id = "http-test-describe" };
    defer exchange.deinit(allocator);
    try runtime.exchanges.append(allocator, &exchange);
    defer runtime.exchanges.clearRetainingCapacity();
    try runtime.server.handleLine(request);
    try runtime.dispatch(&exchange);
    try std.testing.expectEqual(@as(usize, 1), exchange.frames.items.len);
    var response = try oap_provider_envelope.deserializeEnvelope(exchange.frames.items[0].line, allocator);
    defer response.deinit(allocator);
    try std.testing.expectEqualStrings("http-test-describe", response.in_reply_to.?);
    try std.testing.expect(response.payload == .provider_describe_response);
    for (response.payload.provider_describe_response.providers) |provider| {
        try std.testing.expectEqual(oap_provider_types.CredentialGrantChannel.none, provider.credential_grant);
    }
}

test "HTTP provider routes same-id requests to their current exchange" {
    const allocator = std.testing.allocator;
    var runtime = try HttpProviderRuntime.init(allocator);
    defer runtime.deinit();
    var first = HttpProviderExchange{ .request_id = "reused" };
    defer first.deinit(allocator);
    var second = HttpProviderExchange{ .request_id = "reused" };
    defer second.deinit(allocator);
    try runtime.exchanges.append(allocator, &first);
    try runtime.exchanges.append(allocator, &second);
    defer runtime.exchanges.clearRetainingCapacity();
    const request = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"inference.create.request\",\"id\":\"reused\",\"payload\":{\"model_ref\":\"missing/other:test@m\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}}";
    try runtime.server.handleLine(request);
    try runtime.dispatch(&second);
    try std.testing.expectEqual(@as(usize, 0), first.frames.items.len);
    try std.testing.expectEqual(@as(usize, 1), second.frames.items.len);
    try std.testing.expect(second.frames.items[0].terminal);
}

test "HTTP provider returns OAP errors for decode-failed envelopes" {
    const allocator = std.testing.allocator;
    var runtime = try HttpProviderRuntime.init(allocator);
    defer runtime.deinit();
    const requests = [_][]const u8{
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"provider.describe.request\",\"id\":\"wrong-profile\",\"payload\":{}}",
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"unknown.request\",\"id\":\"unknown-type\",\"payload\":{}}",
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"inference.create.request\",\"id\":\"missing-field\",\"payload\":{}}",
    };
    const ids = [_][]const u8{ "wrong-profile", "unknown-type", "missing-field" };
    const codes = [_]oap_provider_types.ErrorCode{ .protocol_violation, .invalid_request, .invalid_request };
    for (requests, ids, codes) |request, id, code| {
        const answer = try httpProviderDecodeError(&runtime, request);
        defer allocator.free(answer);
        var parsed = try oap_provider_envelope.deserializeEnvelope(answer, allocator);
        defer parsed.deinit(allocator);
        try std.testing.expectEqualStrings(id, parsed.in_reply_to.?);
        try std.testing.expect(parsed.payload == .protocol_error);
        try std.testing.expectEqual(code, parsed.payload.protocol_error.err.code);
        try std.testing.expect(parsed.payload.protocol_error.err.message.len > 0);
    }
}

fn runOapProviderMode(
    allocator: std.mem.Allocator,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
    answers_specimens: bool,
) !void {
    endpoint_signals.install() catch {};
    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();
    try register_builtins.registerBuiltInApiProviders(&registry);

    var server = oap_provider_server.Server.init(allocator, .{
        .unserved_refusal = oapUnservedRefusal,
        .capability_revision = VERSION,
        .grant_channel = if (oap_provider_grant_channel.GrantChannel.supported) .out_of_band else .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
        .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
        .catalog = oapFallbackCatalogState(),
    });
    defer server.deinit();
    var output = bounded_output.Output.init(stdout, std.math.maxInt(u64));
    try output.start();
    defer output.deinit();

    var grant_channels = std.ArrayList(OapGrantChannel).empty;
    defer {
        for (grant_channels.items) |*entry| entry.deinit(allocator);
        grant_channels.deinit(allocator);
    }
    var granted_values = std.ArrayList(OapGrantedValue).empty;
    defer {
        for (granted_values.items) |*entry| entry.deinit(allocator);
        granted_values.deinit(allocator);
    }
    var grant_ordinal: u64 = 0;

    const idle_ttl_ms = oapProviderStreamIdleTtlMs(allocator);

    var running = std.ArrayList(RunningOapInference).empty;
    defer {
        for (running.items) |*entry| entry.deinit(allocator);
        running.deinit(allocator);
    }

    var served_generation = try startServingOapCatalog(allocator, &server);

    var async_receiver = stdio.AsyncStdioReceiver.initWithFile(stdin);
    var stdin_handle = try async_receiver.receiveStreamWithHandle(allocator);
    defer _ = stdin_handle.deinit(if (endpoint_signals.received()) 0 else STDIO_THREAD_JOIN_TIMEOUT_MS);
    const stdin_stream = stdin_handle.getStream();

    while (true) {
        var did_work = false;
        if (try applyServedOapReload(allocator, &server, &served_oap_models, &served_generation)) did_work = true;

        while (if (endpoint_signals.received()) null else stdin_stream.poll()) |chunk| {
            var mutable_chunk = chunk;
            defer mutable_chunk.deinit(allocator);

            const line = std.mem.trim(u8, mutable_chunk.data, " \t\r\n");
            if (line.len == 0) continue;

            if (try oapSpecimenRequestId(line, allocator)) |request_id| {
                defer allocator.free(request_id);
                if (answers_specimens) {
                    try server.emitSpecimens(request_id);
                } else {
                    try server.emitSpecimenError(
                        request_id,
                        "this endpoint was not started with --specimens",
                    );
                }
                did_work = true;
                continue;
            }

            server.handleLine(line) catch |err| {
                _ = try drainOapProviderOutbound(&output, allocator, &server);
                try compat.stdio.writeAll(stderr, OAP_PROVIDER_EXHAUSTED_MESSAGE);
                return err;
            };
            did_work = true;
        }

        if (try announceOapGrants(allocator, &server, &grant_channels, &grant_ordinal)) did_work = true;
        if (try pumpOapGrants(allocator, &server, &grant_channels, &granted_values, compat.time.nowMillis())) did_work = true;

        while (server.popPendingStart()) |inference_id| {
            defer allocator.free(inference_id);
            try startOapInference(allocator, &registry, &server, &running, inference_id, granted_values.items);
            did_work = true;
        }

        if (try pumpOapInferences(allocator, &server, &running, idle_ttl_ms)) did_work = true;

        if (try drainOapProviderOutbound(&output, allocator, &server)) did_work = true;

        if (oapInputEnded(stdin_stream) and running.items.len == 0 and !did_work) break;
        if (!did_work) compat.time.sleepNs(STDIO_IDLE_SLEEP_NS);
    }

    _ = try drainOapProviderOutbound(&output, allocator, &server);
}

fn drainOapProviderOutbound(
    stdout: *bounded_output.Output,
    allocator: std.mem.Allocator,
    server: *oap_provider_server.Server,
) !bool {
    var wrote = false;
    while (server.popOutbound()) |line| {
        defer allocator.free(line);
        try stdout.writeAll(line);
        try stdout.writeAll("\n");
        wrote = true;
    }
    return wrote;
}

fn isProviderOapLine(allocator: std.mem.Allocator, line: []const u8) !bool {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch |err| {
        if (err == error.OutOfMemory) return err;
        return false;
    };
    defer parsed.deinit();
    if (parsed.value != .object) return false;
    const profile = parsed.value.object.get("profile") orelse return false;
    return profile == .string and std.mem.eql(u8, profile.string, provider_profile);
}

fn oapInputEnded(stream: *transport.ByteStream) bool {
    return endpoint_signals.received() or oapInputDrained(stream);
}

fn oapInputDrained(stream: *transport.ByteStream) bool {
    return stream.isDone() and !stream.hasPending();
}

fn runOapMode(
    allocator: std.mem.Allocator,
    args: []const []const u8,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
    serve_provider: bool,
) !void {
    var arg_error = OapArgError{};
    const parsed = parseOapModeArgs(args, &arg_error) catch |err| {
        if (arg_error.unknown_option) |option| {
            var buf: [256]u8 = undefined;
            const msg = try std.fmt.bufPrint(&buf, "unknown --oap option: {s}\n\n", .{option});
            try compat.stdio.writeAll(stderr, msg);
        } else if (arg_error.missing_option_value) |option| {
            var buf: [256]u8 = undefined;
            const msg = try std.fmt.bufPrint(&buf, "{s} requires a value\n\n", .{option});
            try compat.stdio.writeAll(stderr, msg);
        } else if (arg_error.unexpected_positional) |value| {
            var buf: [256]u8 = undefined;
            const msg = try std.fmt.bufPrint(&buf, "--oap takes no positional argument: {s}\n\n", .{value});
            try compat.stdio.writeAll(stderr, msg);
        } else if (arg_error.repeated_option) |option| {
            var buf: [256]u8 = undefined;
            const msg = try std.fmt.bufPrint(&buf, "{s} may be given once\n\n", .{option});
            try compat.stdio.writeAll(stderr, msg);
        } else if (arg_error.backend_conflict) |option| {
            var buf: [256]u8 = undefined;
            const msg = if (std.mem.eql(u8, option, "--config"))
                try std.fmt.bufPrint(&buf, "--config names a backend's registry entry and needs --backend\n\n", .{})
            else
                try std.fmt.bufPrint(&buf, "{s} applies to the built-in loop and cannot be combined with --backend\n\n", .{option});
            try compat.stdio.writeAll(stderr, msg);
        }
        try printUsage(stderr);
        return err;
    };
    endpoint_signals.install() catch {};
    if (parsed.backend) |name| {
        if (serve_provider) {
            try compat.stdio.writeAll(stderr, "--backend serves agent-control-core alone; serve agent,provider runs the built-in loop\n\n");
            try printUsage(stderr);
            return error.InvalidArgument;
        }
        return runBackendMode(allocator, name, parsed.config_path, stdin, stdout, stderr);
    }

    const env_model = try provider_base_url.envOwnedOrNull(allocator, "OAPX_OAP_MODEL");
    defer if (env_model) |value| allocator.free(value);
    const default_model_id: ?[]const u8 = parsed.default_model_id orelse env_model;

    const remote_url = try provider_base_url.envOwnedOrNull(allocator, "OAPX_PROVIDER_SERVICE_URL");
    defer if (remote_url) |value| allocator.free(value);
    const remote_security_text = try provider_base_url.envOwnedOrNull(allocator, "OAPX_PROVIDER_SERVICE_SECURITY");
    defer if (remote_security_text) |value| allocator.free(value);
    if (serve_provider and remote_url != null) return error.RemoteProviderRequiresAgentRole;
    var remote_config: ?oap_remote_provider_transport.Config = null;
    if (remote_url) |url| {
        const security_text = remote_security_text orelse return error.ProviderServiceSecurityRequired;
        const security = std.meta.stringToEnum(oap_provider_http_policy.Security, security_text) orelse return error.InvalidProviderServiceSecurity;
        _ = try oap_provider_http_policy.validateBaseUrl(url, security);
        remote_config = .{ .base_url = url, .security = security };
    } else if (remote_security_text != null) return error.ProviderServiceUrlRequired;
    return runOapxServe(allocator, stdin, stdout, stderr, serve_provider, parsed.answers_specimens, default_model_id, if (remote_config) |*config| config else null);
}

const served_features = [_]adapter_contract.Feature{
    .{ .key = "auth.providers", .level = .native },
    .{ .key = "auth.login", .level = .native },
    .{ .key = adapter_contract.feature_models_list, .level = .native, .reason = "the catalog the endpoint loaded at start" },
};

fn runOapxServe(
    allocator: std.mem.Allocator,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
    serve_provider: bool,
    answers_specimens: bool,
    default_model_ref: ?[]const u8,
    remote: ?*const oap_remote_provider_transport.Config,
) !void {
    var production = try tui_app.ProductionRuntime.init(allocator, .{});
    defer production.deinit();
    production.initBridge();
    var options = production.options();
    var remote_bridge: agent_oap_provider_bridge.InProcessOapProviderBridge = undefined;
    var remote_models: []ai_types.Model = &.{};
    defer {
        for (remote_models) |*model| model.deinit(allocator);
        if (remote != null) allocator.free(remote_models);
    }
    if (remote) |config| {
        remote_models = try oap_remote_provider_transport.discoverModels(allocator, config);
        remote_bridge = agent_oap_provider_bridge.InProcessOapProviderBridge.init(config.factory());
        options.protocol = remote_bridge.protocolClient();
        options.models = remote_models;
        options.initial_model = null;
    }
    var chosen: ?model_ref.ParsedModelRef = null;
    defer if (chosen) |*held| held.deinit(allocator);
    if (default_model_ref) |ref| {
        chosen = model_ref.parseModelRef(allocator, ref) catch |err| switch (err) {
            error.OutOfMemory => return error.OutOfMemory,
            else => {
                try compat.stdio.writeAll(stderr, "--model takes a model ref, provider/api@model\n");
                return error.InvalidArgument;
            },
        };
        const held = chosen.?;
        if (!catalogues(options.models, held)) {
            try compat.stdio.writeAll(stderr, "--model names a model the catalog does not list\n");
            return error.InvalidArgument;
        }
        options.initial_model = .{ .id = held.model_id, .provider = held.provider_id, .api = held.api };
    }
    var oapx = oapx_adapter.Adapter.init(allocator, options);
    defer oapx.deinit();
    try oapx.advertise(&served_features);
    var endpoint = adapter_endpoint.Endpoint.init(allocator, oapx.adapter(), .{});
    defer endpoint.deinit();

    var oap_auth_server = AuthProtocolServer.init(allocator, .{ .answers_prompts = false });
    defer oap_auth_server.deinit();
    var auth_adapter = oap_auth_adapter.Adapter.init(allocator, &oap_auth_server);
    defer auth_adapter.deinit();
    auth_adapter.setCapabilityRevision(oapx_adapter.capability_revision);

    var provider_registry = api_registry.ApiRegistry.init(allocator);
    defer provider_registry.deinit();
    var provider_server = oap_provider_server.Server.init(allocator, .{
        .unserved_refusal = oapUnservedRefusal,
        .capability_revision = VERSION,
        .grant_channel = if (oap_provider_grant_channel.GrantChannel.supported) .out_of_band else .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
        .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
        .catalog = oapFallbackCatalogState(),
    });
    defer provider_server.deinit();
    var grant_channels = std.ArrayList(OapGrantChannel).empty;
    defer {
        for (grant_channels.items) |*entry| entry.deinit(allocator);
        grant_channels.deinit(allocator);
    }
    var granted_values = std.ArrayList(OapGrantedValue).empty;
    defer {
        for (granted_values.items) |*entry| entry.deinit(allocator);
        granted_values.deinit(allocator);
    }
    var grant_ordinal: u64 = 0;
    var running_inferences = std.ArrayList(RunningOapInference).empty;
    defer {
        for (running_inferences.items) |*entry| entry.deinit(allocator);
        running_inferences.deinit(allocator);
    }
    const provider_idle_ttl_ms = oapProviderStreamIdleTtlMs(allocator);
    var served_generation: u64 = 0;
    if (serve_provider) {
        try register_builtins.registerBuiltInApiProviders(&provider_registry);
        served_generation = try startServingOapCatalog(allocator, &provider_server);
    }

    var async_receiver = stdio.AsyncStdioReceiver.initWithFileAndLimit(stdin, adapter_endpoint.default_frame_limit);
    var stdin_handle = try async_receiver.receiveStreamWithHandle(allocator);
    defer _ = stdin_handle.deinit(if (endpoint_signals.received()) 0 else STDIO_THREAD_JOIN_TIMEOUT_MS);
    const stdin_stream = stdin_handle.getStream();

    if (!serve_provider) unblockOutput(stdout);
    var output = bounded_output.Output.init(stdout, if (serve_provider) std.math.maxInt(u64) else backend_write_stall_ns);
    try output.start();
    defer output.deinit();
    if (!serve_provider) output.stall_notice = .{ .file = stderr, .message = OUTPUT_STALLED_MESSAGE };
    var auth_input_closed = false;

    while (true) {
        var did_work = false;
        while (if (endpoint_signals.received()) null else stdin_stream.poll()) |chunk| {
            var owned = chunk;
            defer owned.deinit(allocator);
            const line = std.mem.trim(u8, owned.data, " \t\r\n");
            if (line.len == 0) continue;
            did_work = true;
            if (serve_provider) {
                if (try oapSpecimenRequestId(line, allocator)) |request_id| {
                    defer allocator.free(request_id);
                    if (answers_specimens) {
                        try provider_server.emitSpecimens(request_id);
                    } else {
                        try provider_server.emitSpecimenError(request_id, "this endpoint was not started with --specimens");
                    }
                    continue;
                }
                if (try isProviderOapLine(allocator, line)) {
                    provider_server.handleLine(line) catch |err| {
                        _ = try drainOapProviderOutbound(&output, allocator, &provider_server);
                        try compat.stdio.writeAll(stderr, OAP_PROVIDER_EXHAUSTED_MESSAGE);
                        return err;
                    };
                    continue;
                }
            }
            if (try auth_adapter.handleLine(line)) continue;
            endpoint.handleLine(line) catch |err| {
                _ = try writeEndpointOutbound(&output, allocator, &endpoint);
                if (serveFatalMessage(err)) |message| try compat.stdio.writeAll(stderr, message);
                return err;
            };
            _ = try writeEndpointOutbound(&output, allocator, &endpoint);
        }
        if (stdin_stream.isDone() and !stdin_stream.hasPending()) {
            if (stdin_stream.getError()) |failure| {
                _ = try writeEndpointOutbound(&output, allocator, &endpoint);
                if (std.mem.eql(u8, failure, "stdio line too large")) {
                    try compat.stdio.writeAll(stderr, SERVE_FRAME_TOO_LARGE_MESSAGE);
                    return error.FrameTooLarge;
                }
                return error.StdinFailed;
            }
        }

        if (endpoint.sessionCount() > 0) {
            if (try endpoint.pump(if (did_work) 0 else STDIO_IDLE_SLEEP_NS)) did_work = true;
        }
        if (try auth_adapter.pump() > 0) did_work = true;
        if (auth_adapter.takeSignIn() and serve_provider) startServedOapReload();
        if (serve_provider) {
            if (try applyServedOapReload(allocator, &provider_server, &served_oap_models, &served_generation)) did_work = true;
            if (try announceOapGrants(allocator, &provider_server, &grant_channels, &grant_ordinal)) did_work = true;
            if (try pumpOapGrants(allocator, &provider_server, &grant_channels, &granted_values, compat.time.nowMillis())) did_work = true;
            while (provider_server.popPendingStart()) |inference_id| {
                defer allocator.free(inference_id);
                try startOapInference(allocator, &provider_registry, &provider_server, &running_inferences, inference_id, granted_values.items);
                did_work = true;
            }
            if (try pumpOapInferences(allocator, &provider_server, &running_inferences, provider_idle_ttl_ms)) did_work = true;
        }

        if (oapInputEnded(stdin_stream) and !auth_input_closed) {
            try auth_adapter.cancelAllOnDisconnect();
            auth_input_closed = true;
        }

        if (try writeEndpointOutbound(&output, allocator, &endpoint)) did_work = true;
        if (try writeOapAuthOutbound(&output, allocator, &auth_adapter)) did_work = true;
        if (serve_provider and try drainOapProviderOutbound(&output, allocator, &provider_server)) did_work = true;

        if (oapInputEnded(stdin_stream) and !did_work and running_inferences.items.len == 0 and oap_auth_server.activeFlowCount() == 0) break;
        if (!did_work) compat.time.sleepNs(STDIO_IDLE_SLEEP_NS);
    }
    try endpoint.finish(adapter_endpoint.default_settle_window_ns, backendClock);
    _ = try writeEndpointOutbound(&output, allocator, &endpoint);
    _ = try writeOapAuthOutbound(&output, allocator, &auth_adapter);
    if (serve_provider) _ = try drainOapProviderOutbound(&output, allocator, &provider_server);
}

fn catalogues(models: []const ai_types.Model, wanted: model_ref.ParsedModelRef) bool {
    for (models) |model| {
        if (std.mem.eql(u8, model.id, wanted.model_id) and std.mem.eql(u8, model.provider, wanted.provider_id) and std.mem.eql(u8, model.api, wanted.api)) return true;
    }
    return false;
}

fn writeOapAuthOutbound(
    stdout: *bounded_output.Output,
    allocator: std.mem.Allocator,
    adapter: *oap_auth_adapter.Adapter,
) !bool {
    var wrote = false;
    while (adapter.popOutbound()) |line| {
        defer allocator.free(line);
        try stdout.writeAll(line);
        try stdout.writeAll("\n");
        wrote = true;
    }
    return wrote;
}

const backend_config_read_limit = 1024 * 1024;

const BACKEND_MALFORMED_LINE_MESSAGE = "oapx serve agent --backend: stdin carried a line that is not an OAP envelope or control frame; the stream's framing is in doubt and the endpoint will not resynchronise\n";
const BACKEND_UNADDRESSABLE_ENVELOPE_MESSAGE = "oapx serve agent --backend: stdin carried an envelope with no id; every response this binding defines is correlated by in_reply_to, so no refusal could be addressed to it\n";
const OUTPUT_STALLED_MESSAGE = "oapx serve agent: stdout made no progress within the stall bound; no host is reading it, so the endpoint stops rather than hold events it cannot deliver\n";
const BACKEND_OUTPUT_STALLED_MESSAGE = "oapx serve agent --backend: stdout made no progress within the stall bound; no host is reading it, so the endpoint stops rather than hold events it cannot deliver\n";
const BACKEND_FRAME_TOO_LARGE_MESSAGE = "oapx serve agent --backend: a frame exceeded the 1 MiB line bound; the endpoint will not truncate it or resynchronise\n";
const SERVE_MALFORMED_LINE_MESSAGE = "oapx serve agent: stdin carried a line that is not an OAP envelope or control frame; the stream's framing is in doubt and the endpoint will not resynchronise\n";
const SERVE_UNADDRESSABLE_ENVELOPE_MESSAGE = "oapx serve agent: stdin carried an envelope with no id; every response this binding defines is correlated by in_reply_to, so no refusal could be addressed to it\n";
const SERVE_FRAME_TOO_LARGE_MESSAGE = "oapx serve agent: a frame exceeded the 1 MiB line bound; the endpoint will not truncate it or resynchronise\n";

fn backendClock() u64 {
    return compat.time.monotonicNanos() catch 0;
}

fn backendIo() std.Io {
    return if (@import("builtin").is_test) std.testing.io else std.Io.Threaded.global_single_threaded.io();
}

const ConfigSurface = struct {
    label: []const u8,
    noun: []const u8,
    stderr: std.Io.File = undefined,
    arena: std.mem.Allocator = undefined,

    fn refuse(self: ConfigSurface, comptime format: []const u8, args: anytype) !void {
        const message = try std.fmt.allocPrint(self.arena, "{s}: " ++ format ++ "\n", .{self.label} ++ args);
        try compat.stdio.writeAll(self.stderr, message);
    }
};

const endpoint_config_surface = ConfigSurface{ .label = "oapx serve agent", .noun = "backend" };
const hub_config_surface = ConfigSurface{ .label = "oapx serve", .noun = "adapter" };

fn withSurface(base: ConfigSurface, arena: std.mem.Allocator, stderr: std.Io.File) ConfigSurface {
    return .{ .label = base.label, .noun = base.noun, .stderr = stderr, .arena = arena };
}

fn backendEntry(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    name: []const u8,
    config_path: ?[]const u8,
    environ: *const std.process.Environ.Map,
    sources: *[]const adapter_config.ToolSource,
) !adapter_config.AdapterEntry {
    const path = config_path orelse {
        if (std.mem.eql(u8, name, "claude")) return adapter_config.builtinClaude(arena, environ);
        if (std.mem.eql(u8, name, "codex")) return adapter_config.builtinCodex(arena, environ);
        if (std.mem.eql(u8, name, "pi")) return adapter_config.builtinPi(arena, environ);
        return .{ .name = name, .kind = name };
    };
    const bytes = compat.fs.readFileAlloc(arena, compat.fs.getCwd(), path, backend_config_read_limit) catch |err| {
        try surface.refuse("cannot read --config {s}: {s}", .{ path, @errorName(err) });
        return error.BackendRefused;
    };
    var diagnostic = adapter_config.Diagnostic{};
    const file = adapter_config.parse(arena, bytes, environ, &diagnostic) catch |err| {
        if (err != error.ConfigInvalid) return err;
        try surface.refuse("{s}: {s}", .{ path, diagnostic.message });
        return error.BackendRefused;
    };
    sources.* = file.tool_sources;
    return file.adapter(name) orelse {
        try surface.refuse("--config {s} names no adapter \"{s}\"", .{ path, name });
        return error.BackendRefused;
    };
}

fn claudeBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
    environ: *const std.process.Environ.Map,
) !claude_adapter.Config {
    var diagnostic = adapter_config.Diagnostic{};
    const posture = adapter_config.toolPosture(arena, entry, &diagnostic) catch |err| {
        if (err != error.ConfigInvalid) return err;
        try surface.refuse("{s}", .{diagnostic.message});
        return error.BackendRefused;
    };
    const wanted = if (entry.executable.len > 0) entry.executable else "claude";
    const executable = try adapter_config.resolveExecutable(arena, backendIo(), wanted, environ.get("PATH") orelse "") orelse {
        try surface.refuse("no executable \"{s}\" on PATH for {s} \"{s}\"; name one with \"executable\" in a --config entry", .{ wanted, surface.noun, entry.name });
        return error.BackendRefused;
    };
    return .{ .backend = .{
        .executable = executable,
        .args = entry.args,
        .environment = entry.environment,
        .working_directory = entry.working_directory,
        .model = entry.model,
        .tools = switch (posture) {
            .unrestricted => .unrestricted,
            .allowed => |tools| .{ .allowed = tools },
        },
    } };
}

fn codexBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
    environ: *const std.process.Environ.Map,
) !codex_adapter.Config {
    if (entry.endpoint.len > 0) {
        const unix_scheme = "unix://";
        if (!std.mem.startsWith(u8, entry.endpoint, unix_scheme) or entry.endpoint.len == unix_scheme.len) {
            try surface.refuse("{s} \"{s}\" names endpoint \"{s}\"; a codex endpoint is unix://<path to the app-server control socket>", .{ surface.noun, entry.name, entry.endpoint });
            return error.BackendRefused;
        }
        if (entry.executable.len > 0 or entry.args.len > 0) {
            try surface.refuse("{s} \"{s}\" names an endpoint and an executable or args; a codex endpoint relays to a running app-server, so set one or the other", .{ surface.noun, entry.name });
            return error.BackendRefused;
        }
        const self_path = std.process.executablePathAlloc(backendIo(), arena) catch {
            try surface.refuse("{s} \"{s}\" needs this executable's own path to relay to its endpoint, and it could not be read", .{ surface.noun, entry.name });
            return error.BackendRefused;
        };
        return .{
            .executable = self_path,
            .control_socket = entry.endpoint[unix_scheme.len..],
            .environment = entry.environment,
            .working_directory = entry.working_directory,
            .model = entry.model,
            .approval_policy = entry.approval_policy,
            .sandbox = entry.sandbox,
        };
    }
    const wanted = if (entry.executable.len > 0) entry.executable else "codex";
    const executable = try adapter_config.resolveExecutable(arena, backendIo(), wanted, environ.get("PATH") orelse "") orelse {
        try surface.refuse("no executable \"{s}\" on PATH for {s} \"{s}\"; name one with \"executable\" in a --config entry", .{ wanted, surface.noun, entry.name });
        return error.BackendRefused;
    };
    return .{
        .executable = executable,
        .args = entry.args,
        .environment = entry.environment,
        .working_directory = entry.working_directory,
        .model = entry.model,
        .approval_policy = entry.approval_policy,
        .sandbox = entry.sandbox,
    };
}

fn piBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
    environ: *const std.process.Environ.Map,
) !pi_adapter.Config {
    const wanted = if (entry.executable.len > 0) entry.executable else "pi";
    const executable = try adapter_config.resolveExecutable(arena, backendIo(), wanted, environ.get("PATH") orelse "") orelse {
        try surface.refuse("no executable \"{s}\" on PATH for {s} \"{s}\"; name one with \"executable\" in a --config entry", .{ wanted, surface.noun, entry.name });
        return error.BackendRefused;
    };
    return .{
        .executable = executable,
        .args = entry.args,
        .environment = entry.environment,
        .working_directory = entry.working_directory,
    };
}

fn acpBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
    environ: *const std.process.Environ.Map,
) !acp_adapter.Config {
    if (entry.executable.len == 0) {
        try surface.refuse("{s} \"{s}\" is an ACP agent and needs a --config entry naming its \"executable\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    const executable = try adapter_config.resolveExecutable(arena, backendIo(), entry.executable, environ.get("PATH") orelse "") orelse {
        try surface.refuse("no executable \"{s}\" on PATH for {s} \"{s}\"", .{ entry.executable, surface.noun, entry.name });
        return error.BackendRefused;
    };
    const working_directory = entry.working_directory orelse try std.process.currentPathAlloc(backendIo(), arena);
    if (!std.fs.path.isAbsolute(working_directory)) {
        try surface.refuse("{s} \"{s}\" needs an absolute \"working_directory\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    return .{
        .executable = executable,
        .args = entry.args,
        .environment = entry.environment,
        .working_directory = working_directory,
    };
}

fn hermesBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
    environ: *const std.process.Environ.Map,
) !hermes_adapter.Config {
    if (entry.executable.len == 0) {
        try surface.refuse("{s} \"{s}\" is a Hermes gateway and needs a --config entry naming its \"executable\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    const executable = try adapter_config.resolveExecutable(arena, backendIo(), entry.executable, environ.get("PATH") orelse "") orelse {
        try surface.refuse("no executable \"{s}\" on PATH for {s} \"{s}\"", .{ entry.executable, surface.noun, entry.name });
        return error.BackendRefused;
    };
    const working_directory = entry.working_directory orelse try std.process.currentPathAlloc(backendIo(), arena);
    if (!std.fs.path.isAbsolute(working_directory)) {
        try surface.refuse("{s} \"{s}\" needs an absolute \"working_directory\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    return .{
        .executable = executable,
        .args = entry.args,
        .environment = entry.environment,
        .working_directory = working_directory,
        .model = entry.model,
    };
}

fn deepseekBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
    environ: *const std.process.Environ.Map,
) !deepseek_adapter.Config {
    if (entry.executable.len == 0 or entry.provider.len == 0 or entry.model.len == 0) {
        try surface.refuse("{s} \"{s}\" is a DeepSeek harness and needs a --config entry naming its \"executable\", \"provider\" and \"model\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    const executable = try adapter_config.resolveExecutable(arena, backendIo(), entry.executable, environ.get("PATH") orelse "") orelse {
        try surface.refuse("no executable \"{s}\" on PATH for {s} \"{s}\"", .{ entry.executable, surface.noun, entry.name });
        return error.BackendRefused;
    };
    const working_directory = entry.working_directory orelse try std.process.currentPathAlloc(backendIo(), arena);
    if (!std.fs.path.isAbsolute(working_directory)) {
        try surface.refuse("{s} \"{s}\" needs an absolute \"working_directory\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    return .{
        .executable = executable,
        .args = entry.args,
        .environment = entry.environment,
        .working_directory = working_directory,
        .provider = entry.provider,
        .model = entry.model,
        .max_tokens = entry.max_tokens,
    };
}

fn opencodeBackendConfig(
    surface: ConfigSurface,
    arena: std.mem.Allocator,
    entry: adapter_config.AdapterEntry,
) !opencode_adapter.Config {
    _ = arena;
    if (entry.endpoint.len == 0) {
        try surface.refuse("{s} \"{s}\" is an OpenCode server and needs a --config entry naming its \"endpoint\"", .{ surface.noun, entry.name });
        return error.BackendRefused;
    }
    return .{ .endpoint = entry.endpoint, .agent = entry.agent, .username = "opencode", .password = opencodePassword(entry.environment) };
}

fn opencodePassword(environment: []const []const u8) []const u8 {
    var found: []const u8 = "";
    for ([_][]const u8{ "OPENCODE_SERVER_PASSWORD=", "OPENCODE_PASSWORD=" }) |prefix| {
        for (environment) |entry| {
            if (std.mem.startsWith(u8, entry, prefix)) found = entry[prefix.len..];
        }
    }
    return found;
}

test "an OpenCode entry takes its server password from its allowlist, preferring the current name" {
    try std.testing.expectEqualStrings("", opencodePassword(&.{}));
    try std.testing.expectEqualStrings("", opencodePassword(&.{"OTHER=x"}));
    try std.testing.expectEqualStrings("legacy", opencodePassword(&.{"OPENCODE_SERVER_PASSWORD=legacy"}));
    try std.testing.expectEqualStrings("current", opencodePassword(&.{ "OPENCODE_PASSWORD=current", "OPENCODE_SERVER_PASSWORD=legacy" }));
    try std.testing.expectEqualStrings("current", opencodePassword(&.{ "OPENCODE_SERVER_PASSWORD=legacy", "OPENCODE_PASSWORD=current" }));
}

var backend_write_stall_ns: u64 = 2 * 60 * std.time.ns_per_s;

fn writeEndpointOutbound(stdout: *bounded_output.Output, allocator: std.mem.Allocator, endpoint: *adapter_endpoint.Endpoint) !bool {
    var wrote = false;
    while (endpoint.popOutbound()) |line| {
        defer allocator.free(line);
        try stdout.writeAll(line);
        try stdout.writeAll("\n");
        wrote = true;
    }
    return wrote;
}

fn unblockOutput(stdout: std.Io.File) void {
    if (@import("builtin").os.tag == .windows) return;
    const status = stdout.stat(backendIo()) catch return;
    if (status.kind != .named_pipe and status.kind != .unix_domain_socket) return;
    compat.stdio.setNonBlocking(stdout) catch {};
}

fn serveFatalMessage(err: anyerror) ?[]const u8 {
    return switch (err) {
        error.OutputStalled => OUTPUT_STALLED_MESSAGE,
        error.MalformedLine => SERVE_MALFORMED_LINE_MESSAGE,
        error.UnaddressableEnvelope => SERVE_UNADDRESSABLE_ENVELOPE_MESSAGE,
        error.FrameTooLarge => SERVE_FRAME_TOO_LARGE_MESSAGE,
        else => null,
    };
}

fn backendFatalMessage(err: anyerror) ?[]const u8 {
    return switch (err) {
        error.OutputStalled => BACKEND_OUTPUT_STALLED_MESSAGE,
        error.MalformedLine => BACKEND_MALFORMED_LINE_MESSAGE,
        error.UnaddressableEnvelope => BACKEND_UNADDRESSABLE_ENVELOPE_MESSAGE,
        error.FrameTooLarge => BACKEND_FRAME_TOO_LARGE_MESSAGE,
        else => null,
    };
}

fn runBackendMode(
    allocator: std.mem.Allocator,
    name: []const u8,
    config_path: ?[]const u8,
    stdin: std.Io.File,
    stdout: std.Io.File,
    stderr: std.Io.File,
) !void {
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var environ = try compat.createEnvMap(arena);
    var configured_sources: []const adapter_config.ToolSource = &.{};
    const surface = withSurface(endpoint_config_surface, arena, stderr);
    const entry = try backendEntry(surface, arena, name, config_path, &environ, &configured_sources);
    const tool_sources = try arena.alloc(adapter_contract.ConfiguredSource, configured_sources.len);
    for (configured_sources, tool_sources) |source, *slot| {
        slot.* = .{ .id = source.id, .kind = source.kind, .display_name = source.display_name, .protocol = source.protocol, .endpoint = source.endpoint, .command = source.command, .args = source.args, .environment = source.environment };
    }

    var claude: claude_adapter.Adapter = undefined;
    var codex: codex_adapter.Adapter = undefined;
    var pi: pi_adapter.Adapter = undefined;
    var acp: acp_adapter.Adapter = undefined;
    var deepseek: deepseek_adapter.Adapter = undefined;
    var opencode: opencode_adapter.Adapter = undefined;
    var hermes: hermes_adapter.Adapter = undefined;
    var hermes_held = false;
    defer if (hermes_held) hermes.deinit();
    var memory: memory_adapter.Adapter = undefined;
    var oapx_production: ?tui_app.ProductionRuntime = null;
    defer if (oapx_production) |*production| production.deinit();
    var oapx: oapx_adapter.Adapter = undefined;
    const served = if (std.mem.eql(u8, entry.kind, "claude")) claude_served: {
        claude = claude_adapter.Adapter.init(allocator, try claudeBackendConfig(surface, arena, entry, &environ));
        break :claude_served claude.adapter();
    } else if (std.mem.eql(u8, entry.kind, "codex")) codex_served: {
        codex = codex_adapter.Adapter.init(allocator, try codexBackendConfig(surface, arena, entry, &environ));
        break :codex_served codex.adapter();
    } else if (std.mem.eql(u8, entry.kind, "pi")) pi_served: {
        pi = pi_adapter.Adapter.init(allocator, try piBackendConfig(surface, arena, entry, &environ));
        break :pi_served pi.adapter();
    } else if (std.mem.eql(u8, entry.kind, "acp")) acp_served: {
        acp = acp_adapter.Adapter.init(allocator, try acpBackendConfig(surface, arena, entry, &environ));
        break :acp_served acp.adapter();
    } else if (std.mem.eql(u8, entry.kind, "deepseek")) deepseek_served: {
        deepseek = deepseek_adapter.Adapter.init(allocator, try deepseekBackendConfig(surface, arena, entry, &environ));
        break :deepseek_served deepseek.adapter();
    } else if (std.mem.eql(u8, entry.kind, "opencode")) opencode_served: {
        opencode = opencode_adapter.Adapter.init(allocator, try opencodeBackendConfig(surface, arena, entry));
        break :opencode_served opencode.adapter();
    } else if (std.mem.eql(u8, entry.kind, "hermes")) hermes_served: {
        hermes = hermes_adapter.Adapter.init(allocator, try hermesBackendConfig(surface, arena, entry, &environ));
        hermes_held = true;
        break :hermes_served hermes.adapter();
    } else if (std.mem.eql(u8, entry.kind, "memory")) memory_served: {
        memory = memory_adapter.Adapter.init(allocator);
        break :memory_served memory.adapter();
    } else if (std.mem.eql(u8, entry.kind, "oapx")) oapx_served: {
        oapx_production = try tui_app.ProductionRuntime.init(allocator, .{});
        oapx_production.?.initBridge();
        oapx = oapx_adapter.Adapter.init(allocator, oapx_production.?.options());
        break :oapx_served oapx.adapter();
    } else {
        try surface.refuse("{s} \"{s}\" is of type \"{s}\", which oapx does not know; it serves claude, codex, pi, acp, hermes, deepseek, opencode, memory and oapx", .{ surface.noun, name, entry.kind });
        return error.BackendRefused;
    };

    unblockOutput(stdout);
    var output = bounded_output.Output.init(stdout, backend_write_stall_ns);
    try output.start();
    defer output.deinit();
    output.stall_notice = .{ .file = stderr, .message = BACKEND_OUTPUT_STALLED_MESSAGE };
    var endpoint = adapter_endpoint.Endpoint.init(allocator, served, .{ .tool_sources = tool_sources });
    defer endpoint.deinit();

    var async_receiver = stdio.AsyncStdioReceiver.initWithFileAndLimit(stdin, adapter_endpoint.default_frame_limit);
    var stdin_handle = try async_receiver.receiveStreamWithHandle(allocator);
    defer _ = stdin_handle.deinit(if (endpoint_signals.received()) 0 else STDIO_THREAD_JOIN_TIMEOUT_MS);
    const stdin_stream = stdin_handle.getStream();

    while (!endpoint_signals.received()) {
        var did_work = false;
        while (stdin_stream.poll()) |chunk| {
            var owned = chunk;
            defer owned.deinit(allocator);
            const line = std.mem.trim(u8, owned.data, " \t\r\n");
            if (line.len == 0) continue;
            endpoint.handleLine(line) catch |err| {
                _ = try writeEndpointOutbound(&output, allocator, &endpoint);
                if (backendFatalMessage(err)) |message| try compat.stdio.writeAll(stderr, message);
                return err;
            };
            _ = try writeEndpointOutbound(&output, allocator, &endpoint);
            did_work = true;
        }
        if (stdin_stream.isDone() and !stdin_stream.hasPending()) {
            const failure = stdin_stream.getError() orelse break;
            _ = try writeEndpointOutbound(&output, allocator, &endpoint);
            if (std.mem.eql(u8, failure, "stdio line too large")) {
                try compat.stdio.writeAll(stderr, BACKEND_FRAME_TOO_LARGE_MESSAGE);
                return error.FrameTooLarge;
            }
            try surface.refuse("stdin failed: {s}", .{failure});
            return error.StdinFailed;
        }
        if (endpoint.sessionCount() > 0) {
            if (try endpoint.pump(if (did_work) 0 else STDIO_IDLE_SLEEP_NS)) did_work = true;
        }
        if (!did_work) compat.time.sleepNs(STDIO_IDLE_SLEEP_NS);
        _ = try writeEndpointOutbound(&output, allocator, &endpoint);
    }
    try endpoint.finish(adapter_endpoint.default_settle_window_ns, backendClock);
    _ = try writeEndpointOutbound(&output, allocator, &endpoint);
}

test "oap mode arguments accept a default model" {
    var arg_error = OapArgError{};
    const parsed = try parseOapModeArgs(&[_][]const u8{ "--model", "anthropic/anthropic-messages@claude" }, &arg_error);
    try std.testing.expectEqualStrings("anthropic/anthropic-messages@claude", parsed.default_model_id.?);
}

test "oap mode arguments accept explicit stdio and specimen control" {
    var arg_error = OapArgError{};
    const parsed = try parseOapModeArgs(&[_][]const u8{ "--stdio", "--specimens" }, &arg_error);
    try std.testing.expect(parsed.answers_specimens);
}

test "oap mode arguments default to no configured model" {
    var arg_error = OapArgError{};
    const parsed = try parseOapModeArgs(&[_][]const u8{}, &arg_error);
    try std.testing.expect(parsed.default_model_id == null);
}

test "oap mode rejects unknown options, missing values, and positionals" {
    var unknown = OapArgError{};
    try std.testing.expectError(
        error.InvalidArgument,
        parseOapModeArgs(&[_][]const u8{"--unknown"}, &unknown),
    );
    try std.testing.expectEqualStrings("--unknown", unknown.unknown_option.?);

    var missing = OapArgError{};
    try std.testing.expectError(
        error.InvalidArgument,
        parseOapModeArgs(&[_][]const u8{"--model"}, &missing),
    );
    try std.testing.expectEqualStrings("--model", missing.missing_option_value.?);

    var followed = OapArgError{};
    try std.testing.expectError(
        error.InvalidArgument,
        parseOapModeArgs(&[_][]const u8{ "--model", "--other" }, &followed),
    );
    try std.testing.expectEqualStrings("--model", followed.missing_option_value.?);

    var positional = OapArgError{};
    try std.testing.expectError(
        error.InvalidArgument,
        parseOapModeArgs(&[_][]const u8{"write a haiku"}, &positional),
    );
    try std.testing.expectEqualStrings("write a haiku", positional.unexpected_positional.?);
}

test "serve agent takes --backend and its --config, and without --backend serves the built-in loop" {
    var arg_error = OapArgError{};
    const chosen = try parseOapModeArgs(&[_][]const u8{ "--stdio", "--backend", "claude", "--config", "oap-serve.json" }, &arg_error);
    try std.testing.expectEqualStrings("claude", chosen.backend.?);
    try std.testing.expectEqualStrings("oap-serve.json", chosen.config_path.?);

    const bare = try parseOapModeArgs(&[_][]const u8{ "--backend", "hermes" }, &arg_error);
    try std.testing.expectEqualStrings("hermes", bare.backend.?);
    try std.testing.expect(bare.config_path == null);

    const native = try parseOapModeArgs(&[_][]const u8{"--stdio"}, &arg_error);
    try std.testing.expect(native.backend == null);
}

test "a backend flag given twice, without a value, or beside a built-in loop flag is refused naming it" {
    const cases = [_]struct { args: []const []const u8, repeated: ?[]const u8 = null, missing: ?[]const u8 = null, conflict: ?[]const u8 = null }{
        .{ .args = &.{ "--backend", "claude", "--backend", "hermes" }, .repeated = "--backend" },
        .{ .args = &.{ "--backend", "claude", "--config", "a.json", "--config", "b.json" }, .repeated = "--config" },
        .{ .args = &.{"--backend"}, .missing = "--backend" },
        .{ .args = &.{ "--backend", "--stdio" }, .missing = "--backend" },
        .{ .args = &.{ "--backend", "claude", "--config" }, .missing = "--config" },
        .{ .args = &.{ "--config", "oap-serve.json" }, .conflict = "--config" },
        .{ .args = &.{ "--backend", "claude", "--model", "sonnet" }, .conflict = "--model" },
        .{ .args = &.{ "--backend", "claude", "--specimens" }, .conflict = "--specimens" },
    };
    for (cases) |case| {
        var arg_error = OapArgError{};
        try std.testing.expectError(error.InvalidArgument, parseOapModeArgs(case.args, &arg_error));
        if (case.repeated) |option| try std.testing.expectEqualStrings(option, arg_error.repeated_option.?);
        if (case.missing) |option| try std.testing.expectEqualStrings(option, arg_error.missing_option_value.?);
        if (case.conflict) |option| try std.testing.expectEqualStrings(option, arg_error.backend_conflict.?);
    }
}

const BackendRun = struct {
    allocator: std.mem.Allocator,
    name: []const u8,
    config_path: ?[]const u8,
    stdin_file: std.Io.File,
    stdout_file: std.Io.File,
    stderr_file: std.Io.File,
    err: ?anyerror = null,

    fn run(self: *BackendRun) void {
        runBackendMode(self.allocator, self.name, self.config_path, self.stdin_file, self.stdout_file, self.stderr_file) catch |err| {
            self.err = err;
        };
        compat.stdio.close(self.stdin_file);
        compat.stdio.close(self.stdout_file);
        compat.stdio.close(self.stderr_file);
    }
};

const BuiltinRun = struct {
    stdin_file: std.Io.File,
    stdout_file: std.Io.File,
    stderr_file: std.Io.File,
    err: ?anyerror = null,

    fn run(self: *BuiltinRun) void {
        runOapMode(std.heap.page_allocator, &.{}, self.stdin_file, self.stdout_file, self.stderr_file, false) catch |err| {
            self.err = err;
        };
        compat.stdio.close(self.stdin_file);
        compat.stdio.close(self.stdout_file);
        compat.stdio.close(self.stderr_file);
    }
};

fn readAllFrom(allocator: std.mem.Allocator, file: std.Io.File) ![]u8 {
    var collected = std.ArrayList(u8).empty;
    errdefer collected.deinit(allocator);
    var buffer: [4096]u8 = undefined;
    while (true) {
        const read = compat.stdio.read(file, &buffer) catch |err| switch (err) {
            error.EndOfStream => break,
            error.WouldBlock => continue,
            else => return err,
        };
        if (read == 0) break;
        try collected.appendSlice(allocator, buffer[0..read]);
    }
    return collected.toOwnedSlice(allocator);
}

test "the memory backend answers capabilities as the reference endpoint and exits clean at end of input" {
    const allocator = std.testing.allocator;
    const stdin_pipe = try compat.stdio.pipe();
    const stdout_pipe = try compat.stdio.pipe();
    const stderr_pipe = try compat.stdio.pipe();

    var runner = BackendRun{
        .allocator = allocator,
        .name = "memory",
        .config_path = null,
        .stdin_file = stdin_pipe[0],
        .stdout_file = stdout_pipe[1],
        .stderr_file = stderr_pipe[1],
    };
    const thread = try std.Thread.spawn(.{}, BackendRun.run, .{&runner});

    try compat.stdio.writeLine(stdin_pipe[1], "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}");
    compat.stdio.close(stdin_pipe[1]);
    const written = try readAllFrom(allocator, stdout_pipe[0]);
    defer allocator.free(written);
    compat.stdio.close(stdout_pipe[0]);
    const complained = try readAllFrom(allocator, stderr_pipe[0]);
    defer allocator.free(complained);
    compat.stdio.close(stderr_pipe[0]);
    thread.join();

    try std.testing.expect(runner.err == null);
    try std.testing.expectEqual(@as(usize, 0), complained.len);
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, std.mem.trimEnd(u8, written, "\n"), .{});
    defer parsed.deinit();
    try std.testing.expectEqualStrings("q1", parsed.value.object.get("in_reply_to").?.string);
    try std.testing.expectEqualStrings("capabilities.response", parsed.value.object.get("type").?.string);
    try std.testing.expectEqualStrings(memory_adapter.capability_revision, parsed.value.object.get("capability_revision").?.string);
    try std.testing.expectEqualStrings(memory_adapter.endpoint_id, parsed.value.object.get("payload").?.object.get("endpoint").?.object.get("id").?.string);
}

fn refusedBackend(allocator: std.mem.Allocator, name: []const u8, config_path: ?[]const u8) ![]u8 {
    const stdin_pipe = try compat.stdio.pipe();
    const stdout_pipe = try compat.stdio.pipe();
    const stderr_pipe = try compat.stdio.pipe();
    var runner = BackendRun{
        .allocator = allocator,
        .name = name,
        .config_path = config_path,
        .stdin_file = stdin_pipe[0],
        .stdout_file = stdout_pipe[1],
        .stderr_file = stderr_pipe[1],
    };
    const thread = try std.Thread.spawn(.{}, BackendRun.run, .{&runner});
    compat.stdio.close(stdin_pipe[1]);
    const written = try readAllFrom(allocator, stdout_pipe[0]);
    defer allocator.free(written);
    compat.stdio.close(stdout_pipe[0]);
    const complained = try readAllFrom(allocator, stderr_pipe[0]);
    compat.stdio.close(stderr_pipe[0]);
    thread.join();
    errdefer allocator.free(complained);
    try std.testing.expectEqual(@as(?anyerror, error.BackendRefused), runner.err);
    try std.testing.expectEqual(@as(usize, 0), written.len);
    return complained;
}

test "a codex endpoint relays through this executable to the named socket, only a unix endpoint is accepted, and never beside an executable or args" {
    var arena_state = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    const stderr_pipe = try compat.stdio.pipe();
    defer compat.stdio.close(stderr_pipe[0]);
    var surface = hub_config_surface;
    surface.stderr = stderr_pipe[1];
    surface.arena = arena;
    var environ = std.process.Environ.Map.init(arena);
    const shared = try codexBackendConfig(surface, arena, .{ .name = "codexd", .kind = "codex", .endpoint = "unix:///tmp/codex.sock" }, &environ);
    try std.testing.expectEqualStrings("/tmp/codex.sock", shared.control_socket);
    try std.testing.expect(shared.executable.len > 0);
    try std.testing.expectEqual(@as(usize, 0), shared.args.len);
    try std.testing.expectError(error.BackendRefused, codexBackendConfig(surface, arena, .{ .name = "codexd", .kind = "codex", .endpoint = "ws://127.0.0.1:1" }, &environ));
    try std.testing.expectError(error.BackendRefused, codexBackendConfig(surface, arena, .{ .name = "codexd", .kind = "codex", .endpoint = "unix:///tmp/codex.sock", .executable = "/bin/codex" }, &environ));
    try std.testing.expectError(error.BackendRefused, codexBackendConfig(surface, arena, .{ .name = "codexd", .kind = "codex", .endpoint = "unix:///tmp/codex.sock", .args = &.{"-c"} }, &environ));
    compat.stdio.close(stderr_pipe[1]);
    const complained = try readAllFrom(std.testing.allocator, stderr_pipe[0]);
    defer std.testing.allocator.free(complained);
    try std.testing.expect(std.mem.indexOf(u8, complained, "unix://") != null);
}

test "the hub's registry builds every entry a document names, and a child inherits only the variables its entry lists" {
    const allocator = std.testing.allocator;
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    defer complained_on.close(std.testing.io);

    var environ = std.process.Environ.Map.init(arena);
    try environ.put("HOME", "/home/me");
    try environ.put("AWS_SECRET_ACCESS_KEY", "secret");
    const document =
        "{\"adapters\":{" ++
        "\"claude\":{\"type\":\"claude\",\"executable\":\"/bin/echo\",\"allowed_tools\":[\"Read\"],\"environment\":[\"LITERAL=kept\",\"HOME\",\"UNSET_NAME\"]}," ++
        "\"memory\":{\"type\":\"memory\"}" ++
        "}}";
    var diagnostic = adapter_config.Diagnostic{};
    const file = try adapter_config.parse(arena, document, &environ, &diagnostic);
    try std.testing.expectEqual(@as(usize, 2), file.adapters.len);
    try std.testing.expectEqualStrings("claude", file.adapters[0].name);
    try std.testing.expectEqualStrings("memory", file.adapters[1].name);

    var registry = HubRegistry{ .allocator = allocator, .environ = &environ, .surface = withSurface(hub_config_surface, arena, complained_on) };
    defer registry.deinit();
    const claude = try HubRegistry.build(&registry, arena, file.adapter("claude").?);
    const claude_adapter_instance: *claude_adapter.Adapter = @ptrCast(@alignCast(claude.ptr));
    const inherited = claude_adapter_instance.config.backend.environment;
    try std.testing.expectEqual(@as(usize, 2), inherited.len);
    try std.testing.expectEqualStrings("LITERAL=kept", inherited[0]);
    try std.testing.expectEqualStrings("HOME=/home/me", inherited[1]);
    var refusal = adapter_contract.Refusal{};
    const claude_descriptor = try claude.probe(&refusal);
    try std.testing.expect(claude_descriptor.capability_revision.len > 0);

    const memory = try HubRegistry.build(&registry, arena, file.adapter("memory").?);
    const memory_descriptor = try memory.probe(&refusal);
    try std.testing.expectEqualStrings("reference.memory", memory_descriptor.endpoint.id);
}

test "the hub's registry refuses an entry of a type it does not know, naming the entry and the type" {
    const allocator = std.testing.allocator;
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});

    var environ = std.process.Environ.Map.init(arena);
    var diagnostic = adapter_config.Diagnostic{};
    const file = try adapter_config.parse(arena, "{\"adapters\":{\"ghost\":{\"type\":\"ghost\"}}}", &environ, &diagnostic);
    var registry = HubRegistry{ .allocator = allocator, .environ = &environ, .surface = withSurface(hub_config_surface, arena, complained_on) };
    defer registry.deinit();
    try std.testing.expectError(error.Unavailable, HubRegistry.build(&registry, arena, file.adapter("ghost").?));
    complained_on.close(std.testing.io);
    const complained = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(complained);
    try std.testing.expectEqualStrings("oapx serve: adapter \"ghost\" is of type \"ghost\", which oapx does not know; it serves claude, codex, pi, acp, hermes, deepseek, opencode, memory and oapx\n", complained);
}

test "the hub's registry reports a known adapter's own requirement once, not as an unknown type" {
    const allocator = std.testing.allocator;
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});

    var environ = std.process.Environ.Map.init(arena);
    var diagnostic = adapter_config.Diagnostic{};
    const file = try adapter_config.parse(arena, "{\"adapters\":{\"a\":{\"type\":\"opencode\"}}}", &environ, &diagnostic);
    var registry = HubRegistry{ .allocator = allocator, .environ = &environ, .surface = withSurface(hub_config_surface, arena, complained_on) };
    defer registry.deinit();
    try std.testing.expectError(error.Unavailable, HubRegistry.build(&registry, arena, file.adapter("a").?));
    complained_on.close(std.testing.io);
    const complained = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(complained);
    try std.testing.expectEqualStrings("oapx serve: adapter \"a\" is an OpenCode server and needs a --config entry naming its \"endpoint\"\n", complained);
}

test "two entries of one type are two adapters, each with its own executable" {
    const allocator = std.testing.allocator;
    var arena_state = std.heap.ArenaAllocator.init(allocator);
    defer arena_state.deinit();
    const arena = arena_state.allocator();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    defer complained_on.close(std.testing.io);

    var environ = std.process.Environ.Map.init(arena);
    const document =
        "{\"adapters\":{" ++
        "\"first\":{\"type\":\"claude\",\"executable\":\"/bin/one\",\"unrestricted_tools\":true,\"environment\":[\"LITERAL=first\"]}," ++
        "\"second\":{\"type\":\"claude\",\"executable\":\"/bin/two\",\"unrestricted_tools\":true,\"environment\":[\"LITERAL=second\"]}" ++
        "}}";
    var diagnostic = adapter_config.Diagnostic{};
    const file = try adapter_config.parse(arena, document, &environ, &diagnostic);
    var registry = HubRegistry{ .allocator = allocator, .environ = &environ, .surface = withSurface(hub_config_surface, arena, complained_on) };
    defer registry.deinit();

    const first = try HubRegistry.build(&registry, arena, file.adapter("first").?);
    const second = try HubRegistry.build(&registry, arena, file.adapter("second").?);
    try std.testing.expect(first.ptr != second.ptr);
    const first_claude: *claude_adapter.Adapter = @ptrCast(@alignCast(first.ptr));
    const second_claude: *claude_adapter.Adapter = @ptrCast(@alignCast(second.ptr));
    try std.testing.expectEqualStrings("/bin/one", first_claude.config.backend.executable);
    try std.testing.expectEqualStrings("/bin/two", second_claude.config.backend.executable);
    try std.testing.expectEqualStrings("LITERAL=first", first_claude.config.backend.environment[0]);
    try std.testing.expectEqualStrings("LITERAL=second", second_claude.config.backend.environment[0]);
}

test "naming one pack twice, however it is spelled, loads one pack" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "pack", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "pack/pack.json",
        .data = "{\"id\":\"com.example.note\",\"version\":\"1.0.0\",\"schemas\":[\"note.schema.json\"],\"envelope_types\":[{\"type\":\"com.example.note.thing\",\"role\":\"event\",\"schema\":\"note.schema.json#/$defs/thing\"}]}",
    });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "pack/note.schema.json",
        .data = "{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"$defs\":{\"thing\":{\"type\":\"object\",\"required\":[\"type\",\"id\",\"payload\"],\"properties\":{\"type\":{\"const\":\"com.example.note.thing\"},\"id\":{\"type\":\"string\"},\"payload\":{\"type\":\"object\"}}}}}",
    });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "trace.json",
        .data = "[{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"com.example.note.thing\",\"id\":\"n1\",\"payload\":{}}]",
    });
    const cwd = try std.process.currentPathAlloc(std.testing.io, allocator);
    defer allocator.free(cwd);
    const base = try std.fmt.allocPrint(allocator, "{s}/.zig-cache/tmp/{s}", .{ cwd[0..], tmp.sub_path[0..] });
    defer allocator.free(base);
    const pack = try std.fmt.allocPrint(allocator, "{s}/pack", .{base});
    defer allocator.free(pack);
    const trace = try std.fmt.allocPrint(allocator, "{s}/trace.json", .{base});
    defer allocator.free(trace);
    const trailing = try std.fmt.allocPrint(allocator, "{s}/", .{pack});
    defer allocator.free(trailing);
    const dotted = try std.fmt.allocPrint(allocator, "{s}/./pack", .{base});
    defer allocator.free(dotted);

    const rounds = [_][]const []const u8{
        &.{pack},
        &.{ pack, pack },
        &.{ pack, trailing },
        &.{ dotted, pack },
        &.{ pack, trailing, dotted, pack },
    };
    for (rounds, 0..) |named, round| {
        var args = std.ArrayList([]const u8).empty;
        defer args.deinit(allocator);
        for (named) |dir| {
            try args.append(allocator, "--pack");
            try args.append(allocator, dir);
        }
        try args.append(allocator, "--format=json");
        try args.append(allocator, trace);
        const name = try std.fmt.allocPrint(allocator, "out{d}", .{round});
        defer allocator.free(name);
        var out = try tmp.dir.createFile(std.testing.io, name, .{});
        var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
        _ = try runValidate(allocator, args.items, out, complained_on);
        out.close(std.testing.io);
        complained_on.close(std.testing.io);
        const judged = try tmp.dir.readFileAlloc(std.testing.io, name, allocator, .limited(1 << 20));
        defer allocator.free(judged);
        try std.testing.expect(std.mem.indexOf(u8, judged, "schema_invalid") == null);
    }
}

test "validate says the pack load checks are not the ones goap runs" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "pack", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "pack/pack.json",
        .data = "{\"id\":\"com.example.note\",\"version\":\"1.0.0\"}",
    });
    var out = try tmp.dir.createFile(std.testing.io, "stdout", .{});
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    const dir = try tmp.dir.realPathFileAlloc(std.testing.io, "pack", allocator);
    defer allocator.free(dir);
    _ = try runValidate(allocator, &.{ "--pack", dir, "--format=json", "t.json" }, out, complained_on);
    out.close(std.testing.io);
    complained_on.close(std.testing.io);
    const said = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(said);
    try std.testing.expect(std.mem.startsWith(u8, said, partial_load_note));
}

test "validate names the pack directory that would not load, and judges no trace" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var out = try tmp.dir.createFile(std.testing.io, "stdout", .{});
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    try std.testing.expectError(error.Unavailable, runValidate(allocator, &.{ "--pack", "storage", "t.json" }, out, complained_on));
    out.close(std.testing.io);
    complained_on.close(std.testing.io);
    const complained = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(complained);
    const said = try tmp.dir.readFileAlloc(std.testing.io, "stdout", allocator, .limited(4096));
    defer allocator.free(said);
    try std.testing.expect(std.mem.indexOf(u8, complained, "oapx validate: --pack: unavailable: storage did not load as a pack:") != null);
    try std.testing.expect(said.len == 0);
}

test "validate refuses a flag it has never heard of, by the name it was given" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var out = try tmp.dir.createFile(std.testing.io, "stdout", .{});
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    try std.testing.expectError(error.Unavailable, runValidate(allocator, &.{"--bogus"}, out, complained_on));
    out.close(std.testing.io);
    complained_on.close(std.testing.io);
    const complained = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(complained);
    try std.testing.expectEqualStrings("oapx validate: --bogus: unavailable: oapx validate does not carry this flag\n", complained);
}

test "validate names the goap flag it is refusing rather than a generic one" {
    try std.testing.expectEqualStrings("the override is not carried; a trace declaring the profile routes itself, in #367", validateFlagRefusal("--provider").?);
}

test "validate still takes a path, and every flag it does carry" {
    try std.testing.expect(validateFlagRefusal("fixtures/manifest.json") == null);
    try std.testing.expect(validateFlagRefusal("manifest.json") == null);
    try std.testing.expect(validateFlagRefusal("--format") == null);
    try std.testing.expect(validateFlagRefusal("-format") == null);
    try std.testing.expect(validateFlagRefusal("--format=json") == null);
    try std.testing.expect(validateFlagRefusal("--mode") == null);
    try std.testing.expect(validateFlagRefusal("-mode") == null);
    try std.testing.expect(validateFlagRefusal("--mode=tolerant") == null);
    try std.testing.expect(validateFlagRefusal("--pack") == null);
    try std.testing.expect(validateFlagRefusal("-pack") == null);
    try std.testing.expect(validateFlagRefusal("--pack=./p") == null);
}

test "a mode oapx does not carry is refused by name, and never read as a path" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var out = try tmp.dir.createFile(std.testing.io, "stdout", .{});
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    try std.testing.expectError(error.UnsupportedMode, runValidate(allocator, &.{ "--mode", "lenient", "t.json" }, out, complained_on));
    try std.testing.expectError(error.UnsupportedMode, runValidate(allocator, &.{ "--mode=lenient", "t.json" }, out, complained_on));
    try std.testing.expectError(error.UnsupportedMode, runValidate(allocator, &.{"--mode"}, out, complained_on));
    out.close(std.testing.io);
    complained_on.close(std.testing.io);
    const complained = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(complained);
    try std.testing.expect(std.mem.indexOf(u8, complained, "unreadable") == null);
    try std.testing.expect(std.mem.indexOf(u8, complained, "lenient") == null);
    try std.testing.expect(std.mem.indexOf(u8, complained, "t.json") == null);
}

test "unavailable names the surface it was called for" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    var complained_on = try tmp.dir.createFile(std.testing.io, "stderr", .{});
    try std.testing.expectError(error.Unavailable, unavailable(complained_on, "serve", "--addr", "the HTTP and SSE transport lands with #388"));
    complained_on.close(std.testing.io);
    const complained = try tmp.dir.readFileAlloc(std.testing.io, "stderr", allocator, .limited(4096));
    defer allocator.free(complained);
    try std.testing.expectEqualStrings("oapx serve: --addr: unavailable: the HTTP and SSE transport lands with #388\n", complained);
}

test "a backend oapx does not know, or a --config entry it cannot serve, is refused on stderr before any request is read" {
    const allocator = std.testing.allocator;
    const unknown = try refusedBackend(allocator, "nonesuch", null);
    defer allocator.free(unknown);
    try std.testing.expectEqualStrings("oapx serve agent: backend \"nonesuch\" is of type \"nonesuch\", which oapx does not know; it serves claude, codex, pi, acp, hermes, deepseek, opencode, memory and oapx\n", unknown);

    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "oap-serve.json", .data = "{\"adapters\":{\"work\":{\"type\":\"claude\",\"executable\":\"/bin/sh\"}}}" });
    const cwd = try std.process.currentPathAlloc(std.testing.io, allocator);
    defer allocator.free(cwd);
    const path = try std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", tmp.sub_path[0..], "oap-serve.json" });
    defer allocator.free(path);

    const unstated = try refusedBackend(allocator, "work", path);
    defer allocator.free(unstated);
    try std.testing.expectEqualStrings("oapx serve agent: config: adapter \"work\": state its tool posture: set \"allowed_tools\" to the tools the child may use, or \"unrestricted_tools\": true to give it the harness default\n", unstated);

    const absent = try refusedBackend(allocator, "other", path);
    defer allocator.free(absent);
    try std.testing.expect(std.mem.endsWith(u8, absent, "names no adapter \"other\"\n"));

    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "oap-serve.json", .data = "{\"adapters\":{\"typo\":{\"type\":\"claude\",\"executble\":\"/bin/sh\"}}}" });
    const misspelt = try refusedBackend(allocator, "typo", path);
    defer allocator.free(misspelt);
    try std.testing.expect(std.mem.endsWith(u8, misspelt, "config: adapter \"typo\": unknown field \"executble\"\n"));
}

test "every provider the oap endpoint advertises accepts an inference" {
    const allocator = std.testing.allocator;

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
    });
    defer server.deinit();

    try populateOapProviderCatalog(allocator, &server);
    try std.testing.expect(server.providers.items.len > 0);
    try std.testing.expectEqual(server.providers.items.len, server.models.items.len);

    for (server.models.items, 0..) |entry, index| {
        const payload = try std.fmt.allocPrint(
            allocator,
            "{{\"model_ref\":\"{s}\",\"messages\":[{{\"role\":\"user\",\"content\":\"hi\"}}]}}",
            .{entry.model_ref},
        );
        defer allocator.free(payload);

        const line = try std.fmt.allocPrint(
            allocator,
            "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"{s}\",\"type\":\"inference.create.request\",\"id\":\"q{d}\",\"payload\":{s}}}",
            .{ oap_provider_types.PROFILE, index, payload },
        );
        defer allocator.free(line);
        try server.handleLine(line);

        const outbound = server.popOutbound() orelse return error.TestExpectedOutbound;
        defer allocator.free(outbound);

        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, outbound, .{});
        defer parsed.deinit();

        const response_payload = parsed.value.object.get("payload").?.object;
        const accepted = response_payload.get("accepted").?.bool;
        if (!accepted) {
            const message = response_payload.get("error").?.object.get("message").?.string;
            std.debug.print("\n{s} refused at create: {s}\n", .{ entry.model_ref, message });
        }
        try std.testing.expect(accepted);
    }
}

test "a failed start releases the inference it could not run" {
    const allocator = std.testing.allocator;

    var registry = api_registry.ApiRegistry.init(allocator);
    defer registry.deinit();

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
    });
    defer server.deinit();

    try populateOapProviderCatalog(allocator, &server);

    var running = std.ArrayList(RunningOapInference).empty;
    defer running.deinit(allocator);

    const entry = server.models.items[0];
    const line = try std.fmt.allocPrint(
        allocator,
        "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"{s}\",\"type\":\"inference.create.request\",\"id\":\"q1\",\"payload\":{{\"model_ref\":\"{s}\",\"messages\":[{{\"role\":\"user\",\"content\":\"hi\"}}]}}}}",
        .{ oap_provider_types.PROFILE, entry.model_ref },
    );
    defer allocator.free(line);
    try server.handleLine(line);
    while (server.popOutbound()) |out| allocator.free(out);

    try std.testing.expectEqual(@as(usize, 1), server.active.items.len);
    const inference_id = try allocator.dupe(u8, server.active.items[0].id);
    defer allocator.free(inference_id);

    try startOapInference(allocator, &registry, &server, &running, inference_id, &.{});

    try std.testing.expectEqual(@as(usize, 0), running.items.len);
    if (server.active.items.len != 0) {
        std.debug.print(
            "\na failed start left {d} inference(s) in server.active\n",
            .{server.active.items.len},
        );
        return error.FailedStartLeakedInference;
    }
    while (server.popOutbound()) |out| allocator.free(out);
}

fn settleTestInference(
    allocator: std.mem.Allocator,
    server: *oap_provider_server.Server,
    cancel_first: bool,
    failure: []const u8,
) !oap_provider_types.ErrorCode {
    const line =
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_provider_types.PROFILE ++
        "\",\"type\":\"inference.create.request\",\"id\":\"q1\",\"payload\":{\"model_ref\":\"ollama/other:ollama-chat@llama3\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}]}}";
    try server.handleLine(line);
    while (server.popOutbound()) |out| allocator.free(out);

    const inference_id = try allocator.dupe(u8, server.active.items[0].id);
    defer allocator.free(inference_id);

    if (cancel_first) server.active.items[0].cancel_requested = true;

    const stream = try allocator.create(event_stream.AssistantMessageStream);
    stream.* = event_stream.AssistantMessageStream.init(allocator);
    stream.completeWithError(failure);

    const cancelled = try allocator.create(std.atomic.Value(bool));
    cancelled.* = std.atomic.Value(bool).init(true);

    var entry = RunningOapInference{
        .inference_id = inference_id,
        .stream = stream,
        .context = .{ .messages = &.{} },
        .model = .{
            .id = "m",
            .name = "m",
            .api = "ollama",
            .provider = "ollama",
            .base_url = "",
            .reasoning = false,
            .input = &.{},
            .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
            .context_window = 1,
            .max_tokens = 1,
        },
        .cancelled = cancelled,
        .last_progress_ms = 0,
    };
    defer {
        _ = stream.deinitAndDestroy();
        allocator.destroy(cancelled);
    }

    try settleOapInference(server, &entry);
    const out = server.popOutbound() orelse return error.TestExpectedOutbound;
    defer allocator.free(out);
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, out, .{});
    defer parsed.deinit();
    const code = parsed.value.object.get("payload").?.object.get("error").?.object.get("code").?.string;
    return oap_provider_types.ErrorCode.parse(code).?;
}

test "a cancelled inference settles as aborted and a failed one does not" {
    const allocator = std.testing.allocator;

    var cancelled_server = oap_provider_server.Server.init(allocator, .{ .accepts_inference = true, .resolves_own_credentials = true });
    defer cancelled_server.deinit();
    try populateOapProviderCatalog(allocator, &cancelled_server);
    const cancelled_code = try settleTestInference(allocator, &cancelled_server, true, "request cancelled");
    try std.testing.expectEqual(oap_provider_types.ErrorCode.aborted, cancelled_code);
    try std.testing.expectEqual(oap_provider_types.ErrorAction.accept, cancelled_code.action());

    var failed_server = oap_provider_server.Server.init(allocator, .{ .accepts_inference = true, .resolves_own_credentials = true });
    defer failed_server.deinit();
    try populateOapProviderCatalog(allocator, &failed_server);
    const failed_code = try settleTestInference(allocator, &failed_server, false, "request cancelled");
    try std.testing.expectEqual(oap_provider_types.ErrorCode.provider_unavailable, failed_code);
    try std.testing.expectEqual(oap_provider_types.ErrorAction.retry, failed_code.action());
}

test "an inference the provider refuses with HTTP 401 settles as a refused credential, which is not retried" {
    const allocator = std.testing.allocator;
    var server = oap_provider_server.Server.init(allocator, .{ .accepts_inference = true, .resolves_own_credentials = true });
    defer server.deinit();
    try populateOapProviderCatalog(allocator, &server);
    const code = try settleTestInference(allocator, &server, false, "ollama request failed: HTTP 401");
    try std.testing.expectEqual(oap_provider_types.ErrorCode.credential_rejected, code);
    try std.testing.expectEqual(oap_provider_types.ErrorAction.authenticate, code.action());
}

test "the host drops a granted secret when the grant passes its expiry" {
    const allocator = std.testing.allocator;

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .out_of_band,
        .accepts_inference = true,
        .resolves_own_credentials = false,
        .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
        .default_grant_ttl_ms = 1_000,
    });
    defer server.deinit();
    try populateOapProviderCatalog(allocator, &server);

    var channels = std.ArrayList(OapGrantChannel).empty;
    defer {
        for (channels.items) |*entry| entry.deinit(allocator);
        channels.deinit(allocator);
    }
    var granted = std.ArrayList(OapGrantedValue).empty;
    defer {
        for (granted.items) |*entry| entry.deinit(allocator);
        granted.deinit(allocator);
    }
    var ordinal: u64 = 78000;

    const request =
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_provider_types.PROFILE ++
        "\",\"type\":\"provider.credential.grant.request\",\"id\":\"g1\",\"payload\":{\"provider_id\":\"anthropic\",\"nonce\":\"n-exp\"}}";
    try server.handleLine(request);

    try std.testing.expect(try announceOapGrants(allocator, &server, &channels, &ordinal));
    const announced = server.popOutbound() orelse return error.TestExpectedOutbound;
    defer allocator.free(announced);
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, announced, .{});
    defer parsed.deinit();
    const channel_path = parsed.value.object.get("payload").?.object.get("channel").?.string;

    try oap_provider_grant_channel.connectAndWrite(channel_path, "n-exp\nsk-expiring-secret");

    var rounds: usize = 0;
    while (rounds < 200 and granted.items.len == 0) : (rounds += 1) {
        _ = try pumpOapGrants(allocator, &server, &channels, &granted, compat.time.nowMillis());
    }
    try std.testing.expectEqual(@as(usize, 1), granted.items.len);
    try std.testing.expectEqualStrings("sk-expiring-secret", granted.items[0].value);
    try std.testing.expect(server.holdsGrant(granted.items[0].reference));

    _ = try pumpOapGrants(allocator, &server, &channels, &granted, compat.time.nowMillis() + 60_000);

    try std.testing.expectEqual(@as(usize, 0), granted.items.len);
    try std.testing.expectEqual(@as(usize, 0), server.grants.items.len);
}

test "a granted credential crosses the side channel and reaches the inference" {
    const allocator = std.testing.allocator;

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .out_of_band,
        .accepts_inference = true,
        .resolves_own_credentials = false,
        .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
    });
    defer server.deinit();
    try populateOapProviderCatalog(allocator, &server);

    var channels = std.ArrayList(OapGrantChannel).empty;
    defer {
        for (channels.items) |*entry| entry.deinit(allocator);
        channels.deinit(allocator);
    }
    var granted = std.ArrayList(OapGrantedValue).empty;
    defer {
        for (granted.items) |*entry| entry.deinit(allocator);
        granted.deinit(allocator);
    }
    var ordinal: u64 = 77000;

    const request =
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_provider_types.PROFILE ++
        "\",\"type\":\"provider.credential.grant.request\",\"id\":\"g1\",\"payload\":{\"provider_id\":\"anthropic\",\"nonce\":\"n-123\"}}";
    try server.handleLine(request);

    try std.testing.expect(try announceOapGrants(allocator, &server, &channels, &ordinal));
    try std.testing.expectEqual(@as(usize, 1), channels.items.len);

    const announced = server.popOutbound() orelse return error.TestExpectedOutbound;
    defer allocator.free(announced);
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, announced, .{});
    defer parsed.deinit();
    const channel_path = parsed.value.object.get("payload").?.object.get("channel").?.string;

    try oap_provider_grant_channel.connectAndWrite(channel_path, "n-123\nsk-granted-secret");

    var rounds: usize = 0;
    while (rounds < 200 and granted.items.len == 0) : (rounds += 1) {
        _ = try pumpOapGrants(allocator, &server, &channels, &granted, compat.time.nowMillis());
    }

    try std.testing.expectEqual(@as(usize, 1), granted.items.len);
    try std.testing.expectEqualStrings("sk-granted-secret", granted.items[0].value);
    try std.testing.expectEqual(@as(usize, 0), channels.items.len);

    const reference = granted.items[0].reference;
    try std.testing.expectEqualStrings("sk-granted-secret", grantedValueFor(granted.items, reference) orelse "");
    try std.testing.expect(grantedValueFor(granted.items, "grant:nobody:0") == null);

    const create = try std.fmt.allocPrint(
        allocator,
        "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"{s}\",\"type\":\"inference.create.request\",\"id\":\"q1\",\"payload\":{{\"model_ref\":\"anthropic/anthropic-messages@claude-sonnet-4-5\",\"messages\":[{{\"role\":\"user\",\"content\":\"hi\"}}],\"credential_ref\":\"{s}\"}}}}",
        .{ oap_provider_types.PROFILE, reference },
    );
    defer allocator.free(create);
    try server.handleLine(create);

    const response = server.popOutbound() orelse return error.TestExpectedOutbound;
    defer allocator.free(response);
    var decoded = try std.json.parseFromSlice(std.json.Value, allocator, response, .{});
    defer decoded.deinit();
    try std.testing.expect(decoded.value.object.get("payload").?.object.get("accepted").?.bool);

    const inference_id = server.active.items[0].id;
    try std.testing.expectEqualStrings(reference, server.active.items[0].credential_ref.?);
    _ = inference_id;
}

test "a descriptor claims the carry round trip only where the provider declares reasoning" {
    const allocator = std.testing.allocator;

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
    });
    defer server.deinit();

    try populateOapProviderCatalog(allocator, &server);
    try std.testing.expect(server.providers.items.len > 0);

    var claimed: usize = 0;
    for (server.providers.items) |descriptor| {
        if (!descriptor.round_trips_carry) continue;
        claimed += 1;
        var declares_reasoning = false;
        for (oap_test_served_models) |model| {
            if (!std.mem.eql(u8, model.provider, descriptor.id)) continue;
            declares_reasoning = declares_reasoning or model.reasoning;
        }
        if (!declares_reasoning) {
            std.debug.print(
                "\n{s} claims a carry round trip without declaring reasoning\n",
                .{descriptor.id},
            );
            return error.CarryRoundTripOverClaimed;
        }
    }
    try std.testing.expect(claimed > 0);
}

test "the signature lookup finds a carry only on the block that carries one" {
    const content = [_]ai_types.AssistantContent{
        .{ .text = .{ .text = "answer" } },
        .{ .thinking = .{ .thinking = "weighing", .thinking_signature = "sig-abc" } },
    };
    const partial = ai_types.AssistantMessage{
        .content = &content,
        .api = "anthropic-messages",
        .provider = "anthropic",
        .model = "m",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    };

    try std.testing.expect(oap_provider_runtime.thinkingSignature(partial, 0) == null);
    const signature = oap_provider_runtime.thinkingSignature(partial, 1) orelse return error.TestExpectedSignature;
    try std.testing.expectEqualStrings("sig-abc", signature);
    try std.testing.expect(oap_provider_runtime.thinkingSignature(partial, 7) == null);
}

fn buildContextUnderFailure(allocator: std.mem.Allocator, source: []const oap_types.Message) !void {
    var context = try buildOapInferenceContext(allocator, source, .{
        .provider = "anthropic",
        .api = "anthropic-messages",
        .model_id = "claude-sonnet-4-5",
    });
    context.deinit(allocator);
}

test "building an inference context leaks nothing and frees nothing twice under allocation failure" {
    var parts = [_]oap_types.ContentPart{
        .{ .reasoning = .{ .text = "prior thinking", .carry = "SIG-MARKER" } },
        .{ .text = "spoken" },
        .{ .tool_call = .{
            .tool_call_id = "c1",
            .name = "search",
            .arguments_json = "{}",
            .carry = "TOOL-SIG",
        } },
    };
    const source = [_]oap_types.Message{
        .{ .role = .system, .content = .{ .text = "be brief" } },
        .{ .role = .user, .content = .{ .text = "first question" } },
        .{ .role = .assistant, .content = .{ .parts = parts[0..] } },
        .{ .role = .user, .content = .{ .text = "second question" } },
    };

    try std.testing.checkAllAllocationFailures(
        std.testing.allocator,
        buildContextUnderFailure,
        .{source[0..]},
    );
}

test "a replayed carry survives the transform that feeds the provider" {
    const allocator = std.testing.allocator;

    var parts = [_]oap_types.ContentPart{
        .{ .reasoning = .{ .text = "prior thinking", .carry = "SIG-MARKER" } },
        .{ .text = "prior answer" },
    };
    const source = [_]oap_types.Message{
        .{ .role = .assistant, .content = .{ .parts = parts[0..] } },
    };

    var context = try buildOapInferenceContext(allocator, source[0..], .{
        .provider = "anthropic",
        .api = "anthropic-messages",
        .model_id = "claude-sonnet-4-5",
    });
    defer context.deinit(allocator);

    var transformed = try pre_transform.preTransform(allocator, context.messages, .{
        .target_api = "anthropic-messages",
        .target_provider = "anthropic",
        .target_model_id = "claude-sonnet-4-5",
        .max_tool_id_len = 64,
        .insert_synthetic_results = true,
        .tools = null,
        .is_oauth = false,
    });
    defer transformed.deinit();

    const content = transformed.messages[0].assistant.content;
    try std.testing.expect(content[0] == .thinking);
    try std.testing.expectEqualStrings("SIG-MARKER", content[0].thinking.thinking_signature orelse "");
}

test "the inbound half puts a replayed carry back on the block it belongs to" {
    const allocator = std.testing.allocator;

    var parts = [_]oap_types.ContentPart{
        .{ .reasoning = .{ .text = "first", .carry = "sig-one" } },
        .{ .text = "spoken" },
        .{ .reasoning = .{ .text = "second", .carry = "sig-two" } },
    };
    const messages = [_]oap_types.Message{
        .{ .role = .assistant, .content = .{ .parts = parts[0..] } },
    };

    var context = try buildOapInferenceContext(allocator, messages[0..], .{
        .provider = "anthropic",
        .api = "anthropic-messages",
        .model_id = "m",
    });
    defer context.deinit(allocator);

    try std.testing.expectEqual(@as(usize, 1), context.messages.len);
    const content = context.messages[0].assistant.content;
    try std.testing.expectEqual(@as(usize, 3), content.len);

    try std.testing.expectEqualStrings("sig-one", content[0].thinking.thinking_signature orelse "");
    try std.testing.expectEqualStrings("first", content[0].thinking.thinking);
    try std.testing.expect(content[1] == .text);
    try std.testing.expectEqualStrings("sig-two", content[2].thinking.thinking_signature orelse "");
    try std.testing.expectEqualStrings("second", content[2].thinking.thinking);
}

test "reasoning options reach the stream options and the model declares reasoning" {
    const allocator = std.testing.allocator;

    var options: ai_types.StreamOptions = .{};
    applyOapReasoning(&options, .{ .enabled = true, .budget_tokens = 2048, .effort = null });
    try std.testing.expect(options.thinking_enabled);
    try std.testing.expectEqual(@as(?u32, 2048), options.thinking_budget_tokens);

    const served = oap_test_served_models[0];
    try std.testing.expect(served.reasoning);

    var model = try buildOapInferenceModel(allocator, served, .none);
    defer model.deinit(allocator);
    try std.testing.expect(model.reasoning);
}

test "a served model on an overridden row carries the override's base, headers and version, and withholds the stored key" {
    const allocator = std.testing.allocator;
    try provider_catalog.blankEnvironment(allocator);
    defer compat.clearTestEnv();
    const served = oap_test_served_models[1];
    var headers = [_]ai_types.HeaderPair{.{ .name = @constCast("X-Tenant"), .value = @constCast("acme") }};
    const lookup = auth_resolver.OverrideLookup{ .endpoint = .{ .base_url = @constCast("https://proxy.example/v1"), .forwards_credential = false, .carries_version = true, .headers = &headers } };

    var model = try buildOapInferenceModel(allocator, served, lookup);
    defer model.deinit(allocator);
    try std.testing.expectEqualStrings("https://proxy.example/v1", model.base_url);
    try std.testing.expectEqual(@as(?bool, true), model.carries_version);
    try std.testing.expectEqualStrings("X-Tenant", model.headers.?[0].name);
    try std.testing.expect(auth_resolver.storedCredentialWithheld(allocator, lookup, served.provider, model.base_url));
    try std.testing.expect(model.credential_withheld);

    var forwarding = lookup;
    forwarding.endpoint.forwards_credential = true;
    try std.testing.expect(!auth_resolver.storedCredentialWithheld(allocator, forwarding, served.provider, model.base_url));
}

test "describe names a draft revision that identifies a state rather than a stream" {
    const allocator = std.testing.allocator;

    const streams = [_][]const u8{ "main", "master", "HEAD", "head", "latest", "trunk", "drafts/main" };
    for (streams) |stream| {
        if (std.ascii.eqlIgnoreCase(OAP_PROVIDER_PROFILE_REVISION, stream)) {
            std.debug.print(
                "\nprofile_revision is \"{s}\", which names a stream and not a state\n",
                .{OAP_PROVIDER_PROFILE_REVISION},
            );
            return error.ProfileRevisionNamesAStream;
        }
    }
    try std.testing.expect(OAP_PROVIDER_PROFILE_REVISION.len > 0);

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
        .profile_revision = OAP_PROVIDER_PROFILE_REVISION,
    });
    defer server.deinit();

    const line =
        "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"" ++ oap_provider_types.PROFILE ++
        "\",\"type\":\"provider.describe.request\",\"id\":\"q1\",\"payload\":{}}";
    try server.handleLine(line);

    const outbound = server.popOutbound() orelse return error.TestExpectedOutbound;
    defer allocator.free(outbound);

    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, outbound, .{});
    defer parsed.deinit();

    const published = parsed.value.object.get("payload").?.object.get("profile_revision").?.string;
    try std.testing.expectEqualStrings(OAP_PROVIDER_PROFILE_REVISION, published);
}

fn populateCatalogUnderFailure(allocator: std.mem.Allocator) !void {
    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
    });
    defer server.deinit();
    try populateOapProviderCatalog(allocator, &server);
}

test "populating the oap catalogue leaks nothing when an allocation fails" {
    try std.testing.checkAllAllocationFailures(
        std.testing.allocator,
        populateCatalogUnderFailure,
        .{},
    );
}

test "every capability the oap endpoint implements is advertised and honoured" {
    const allocator = std.testing.allocator;

    inline for (@typeInfo(oap_provider_types.SnapshotPolicy).@"enum".fields) |field| {
        const policy = @field(oap_provider_types.SnapshotPolicy, field.name);
        var implemented = false;
        for (oap_provider_server.IMPLEMENTED_SNAPSHOT_POLICIES) |candidate| {
            if (candidate == policy) implemented = true;
        }
        try std.testing.expect(implemented);
    }

    var server = oap_provider_server.Server.init(allocator, .{
        .capability_revision = VERSION,
        .grant_channel = .unsupported,
        .accepts_inference = true,
        .resolves_own_credentials = true,
    });
    defer server.deinit();

    try populateOapProviderCatalog(allocator, &server);
    try std.testing.expect(server.providers.items.len > 0);

    for (server.providers.items) |descriptor| {
        try std.testing.expectEqual(oap_provider_server.IMPLEMENTS_SYNC, descriptor.answers_sync);
        try std.testing.expectEqual(
            oap_provider_server.IMPLEMENTED_SNAPSHOT_POLICIES.len,
            descriptor.snapshot_policies.len,
        );
    }

    var counter: usize = 0;
    for (server.models.items) |entry| {
        for (oap_provider_server.IMPLEMENTED_SNAPSHOT_POLICIES) |policy| {
            counter += 1;
            const payload = try std.fmt.allocPrint(
                allocator,
                "{{\"model_ref\":\"{s}\",\"messages\":[{{\"role\":\"user\",\"content\":\"hi\"}}],\"include_snapshot\":\"{s}\"}}",
                .{ entry.model_ref, @tagName(policy) },
            );
            defer allocator.free(payload);

            const line = try std.fmt.allocPrint(
                allocator,
                "{{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"{s}\",\"type\":\"inference.create.request\",\"id\":\"s{d}\",\"payload\":{s}}}",
                .{ oap_provider_types.PROFILE, counter, payload },
            );
            defer allocator.free(line);
            try server.handleLine(line);

            const outbound = server.popOutbound() orelse return error.TestExpectedOutbound;
            defer allocator.free(outbound);

            var parsed = try std.json.parseFromSlice(std.json.Value, allocator, outbound, .{});
            defer parsed.deinit();

            const response_payload = parsed.value.object.get("payload").?.object;
            if (!response_payload.get("accepted").?.bool) {
                const message = response_payload.get("error").?.object.get("message").?.string;
                std.debug.print(
                    "\n{s} refused include_snapshot={s}: {s}\n",
                    .{ entry.model_ref, @tagName(policy), message },
                );
                return error.AdvertisedPolicyRefused;
            }

            const honoured = response_payload.get("honoured").?.object.get("include_snapshot").?.string;
            if (!std.mem.eql(u8, honoured, @tagName(policy))) {
                std.debug.print(
                    "\n{s} downgraded include_snapshot={s} to {s}\n",
                    .{ entry.model_ref, @tagName(policy), honoured },
                );
                return error.AdvertisedPolicyDowngraded;
            }
        }
    }
}

test "the oap descriptors state compatibility facts only where makai asserts them" {
    const silent = oapProviderCompatibility("openai", .{});
    try std.testing.expect(silent.isEmpty());

    const asserted = oapProviderCompatibility("openai", .{ .openai_proxy = true });
    try std.testing.expect(!asserted.isEmpty());
    try std.testing.expectEqual(@as(?bool, true), asserted.supports_store);
    try std.testing.expectEqual(@as(?bool, true), asserted.supports_developer_role);
    try std.testing.expectEqual(@as(?bool, true), asserted.supports_reasoning_effort);
    try std.testing.expect(asserted.max_tokens_field.? == .max_completion_tokens);

    const anthropic = oapProviderCompatibility("anthropic", .{ .anthropic_proxy = true });
    try std.testing.expectEqual(@as(?bool, true), anthropic.cache_ttl_control);

    const unasserted_anthropic = oapProviderCompatibility("anthropic", .{ .openai_proxy = true });
    try std.testing.expect(unasserted_anthropic.isEmpty());
}

test "serve names a role, and the role is a noun rather than a flag" {
    try std.testing.expectEqual(ServeRole.agent, serveRole("agent").?);
    try std.testing.expectEqual(ServeRole.provider, serveRole("provider").?);
    try std.testing.expect(serveRole("--agent") == null);
    try std.testing.expect(serveRole("Agent") == null);
    try std.testing.expect(serveRole("endpoint") == null);
    try std.testing.expect(serveRole("") == null);
    try std.testing.expect(isCombinedServeRole("agent,provider"));
    try std.testing.expect(isCombinedServeRole("provider,agent"));
    try std.testing.expect(!isCombinedServeRole("agent,agent"));
}

test "combined stdio drains queued input after reader reports EOF" {
    var stream = transport.ByteStream.init(std.testing.allocator);
    defer stream.deinit();
    try stream.push(.{ .data = "last request", .owned = false });
    stream.complete({});
    try std.testing.expect(!oapInputDrained(&stream));
    var chunk = stream.poll().?;
    chunk.deinit(std.testing.allocator);
    try std.testing.expect(oapInputDrained(&stream));
}

test "combined stdio dispatches only the provider profile to the provider handler" {
    const allocator = std.testing.allocator;
    try std.testing.expect(try isProviderOapLine(
        allocator,
        "{\"protocol\":\"open-agent-protocol\",\"profile\":\"open-agent-protocol.model-provider-core\",\"type\":\"provider.describe.request\",\"id\":\"q1\",\"payload\":{}}",
    ));
    try std.testing.expect(!try isProviderOapLine(
        allocator,
        "{\"protocol\":\"open-agent-protocol\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q2\",\"payload\":{}}",
    ));
    try std.testing.expect(!try isProviderOapLine(allocator, "{broken"));
}

fn diagnosedCodes(allocator: std.mem.Allocator, trace: []const u8, out: *std.ArrayList([]const u8)) !void {
    var judge = try validator.Validator.init(allocator, .{ .io = std.testing.io });
    defer judge.deinit();
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try validateTrace(allocator, &judge, trace, &found);
    for (found.items) |finding| try out.append(allocator, try allocator.dupe(u8, finding.code));
}

fn judgedFindings(allocator: std.mem.Allocator, trace: []const u8, out: *std.ArrayList(ValidateFinding)) !void {
    var judge = try validator.Validator.init(allocator, .{ .io = std.testing.io });
    defer judge.deinit();
    try validateTrace(allocator, &judge, trace, out);
}

test "validate accepts a trace the validator judges clean" {
    const allocator = std.testing.allocator;
    const trace =
        \\[
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"capq","payload":{}}
        \\]
    ;
    var codes = std.ArrayList([]const u8).empty;
    defer {
        for (codes.items) |code| allocator.free(code);
        codes.deinit(allocator);
    }
    try diagnosedCodes(allocator, trace, &codes);
    try std.testing.expectEqual(@as(usize, 0), codes.items.len);
}

test "validate reports the code a run event before its start earns" {
    const allocator = std.testing.allocator;
    const trace =
        \\[
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.request","id":"submit","session_id":"s1","payload":{"session_id":"s1","messages":[{"role":"user","content":"go"}],"delivery":"auto"}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"admit","in_reply_to":"submit","session_id":"s1","payload":{"session_id":"s1","accepted":true,"submission_id":"sub1","requested_delivery":"auto","effective_delivery":"queue","admission":"queued","run_id":"r1","status":"queued"}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"delta","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","message_id":"m1","part":{"type":"text","text":"pre"}}}
        \\]
    ;
    var codes = std.ArrayList([]const u8).empty;
    defer {
        for (codes.items) |code| allocator.free(code);
        codes.deinit(allocator);
    }
    try diagnosedCodes(allocator, trace, &codes);
    var named_missing_start = false;
    var named_missing_terminal = false;
    for (codes.items) |code| {
        if (std.mem.eql(u8, code, semantic.code_missing_run_started)) named_missing_start = true;
        if (std.mem.eql(u8, code, semantic.code_missing_run_terminal)) named_missing_terminal = true;
    }
    try std.testing.expect(named_missing_start);
    try std.testing.expect(named_missing_terminal);
}

const ndjson_first = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}";
const ndjson_repeating = "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q2\",\"id\":\"q3\",\"payload\":{}}";

test "validate judges a lone object as a one-envelope trace on line 1" {
    const allocator = std.testing.allocator;
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try judgedFindings(allocator, "{}", &found);
    try std.testing.expectEqual(@as(usize, 1), found.items.len);
    try std.testing.expectEqual(ValidatePhase.schema, found.items[0].phase);
    try std.testing.expectEqualStrings("schema_invalid", found.items[0].code);
    try std.testing.expectEqual(@as(usize, 0), found.items[0].index);
    try std.testing.expectEqual(@as(usize, 1), found.items[0].line);
}

test "an empty, blank or bracketed-empty trace has no envelopes and nothing to refuse" {
    const allocator = std.testing.allocator;
    for ([_][]const u8{ "", " \n\t\n", "[]" }) |source| {
        var found = std.ArrayList(ValidateFinding).empty;
        defer freeFindings(allocator, &found);
        try judgedFindings(allocator, source, &found);
        try std.testing.expectEqual(@as(usize, 0), found.items.len);
    }
}

test "a newline-delimited trace counts blank lines and names the envelope a repeated key spoils" {
    const allocator = std.testing.allocator;
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try judgedFindings(allocator, ndjson_first ++ "\n\n" ++ ndjson_repeating ++ "\n", &found);
    try std.testing.expectEqual(@as(usize, 1), found.items.len);
    try std.testing.expectEqual(ValidatePhase.decode, found.items[0].phase);
    try std.testing.expectEqualStrings("duplicate_key", found.items[0].code);
    try std.testing.expectEqual(@as(usize, 1), found.items[0].index);
    try std.testing.expectEqual(@as(usize, 3), found.items[0].line);
}

test "a newline-delimited line that does not parse refuses the trace at that line" {
    const allocator = std.testing.allocator;
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try judgedFindings(allocator, ndjson_first ++ "\n{\"broken\n" ++ ndjson_first ++ "\n", &found);
    try std.testing.expectEqual(@as(usize, 1), found.items.len);
    try std.testing.expectEqual(ValidatePhase.decode, found.items[0].phase);
    try std.testing.expectEqualStrings("malformed_json", found.items[0].code);
    try std.testing.expectEqual(@as(usize, 1), found.items[0].index);
    try std.testing.expectEqual(@as(usize, 2), found.items[0].line);
}

test "validate judges a provider trace with the provider machine, not the agent one" {
    const allocator = std.testing.allocator;
    const trace =
        \\[
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.started","id":"e1","inference_id":"i1","sequence":1,"payload":{"model_ref":"anthropic/anthropic-messages@claude","started_at_ms":1}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.ended","id":"n0","inference_id":"i1","sequence":2,"payload":{"part_index":0,"part_kind":"text","text":"hello"}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.completed","id":"t1","inference_id":"i1","sequence":3,"payload":{"stop_reason":"stop","message":{"role":"assistant","content":[{"type":"text","text":"hello"}]},"usage":{"input_tokens":7,"output_tokens":3}}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.model-provider-core","type":"inference.part.delta","id":"late","inference_id":"i1","sequence":4,"payload":{"part_index":0,"delta":"more"}}
        \\]
    ;
    var codes = std.ArrayList([]const u8).empty;
    defer {
        for (codes.items) |code| allocator.free(code);
        codes.deinit(allocator);
    }
    try diagnosedCodes(allocator, trace, &codes);
    try std.testing.expect(codes.items.len != 0);
}

test "validate refuses an envelope the schema refuses before any semantic rule runs" {
    const allocator = std.testing.allocator;
    const trace =
        \\[
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","payload":{}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"delta","session_id":"s1","run_id":"r1","sequence":1,"payload":{"session_id":"s1","run_id":"r1","message_id":"m1","part":{"type":"text","text":"pre"}}}
        \\]
    ;
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try judgedFindings(allocator, trace, &found);
    try std.testing.expectEqual(@as(usize, 1), found.items.len);
    try std.testing.expectEqual(ValidatePhase.schema, found.items[0].phase);
    try std.testing.expectEqualStrings("schema_invalid", found.items[0].code);
    try std.testing.expectEqual(@as(usize, 0), found.items[0].index);
}

test "validate refuses a repeated key at decode, naming the envelope that repeats it" {
    const allocator = std.testing.allocator;
    const trace =
        \\[
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"q1","payload":{}},
        \\  {"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.request","id":"q2","id":"q3","payload":{}}
        \\]
    ;
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try judgedFindings(allocator, trace, &found);
    try std.testing.expectEqual(@as(usize, 1), found.items.len);
    try std.testing.expectEqual(ValidatePhase.decode, found.items[0].phase);
    try std.testing.expectEqualStrings("duplicate_key", found.items[0].code);
    try std.testing.expectEqual(@as(usize, 1), found.items[0].index);
}

test "validate reports malformed JSON as a decode finding" {
    const allocator = std.testing.allocator;
    var found = std.ArrayList(ValidateFinding).empty;
    defer freeFindings(allocator, &found);
    try judgedFindings(allocator, "[{\"protocol\":", &found);
    try std.testing.expectEqual(@as(usize, 1), found.items.len);
    try std.testing.expectEqual(ValidatePhase.decode, found.items[0].phase);
    try std.testing.expectEqualStrings("malformed_json", found.items[0].code);
}

test "a pass names its semantic rules as partial" {
    const allocator = std.testing.allocator;
    var out = std.ArrayList(u8).empty;
    defer out.deinit(allocator);
    try writeHumanReport(&out, allocator, "trace.json", .{ .judged = &.{} });
    try std.testing.expectEqualStrings("PASS trace.json (" ++ partial_semantic_note ++ ")\n", out.items);
}

test "a JSON report names the phase of each finding and never claims completeness" {
    const allocator = std.testing.allocator;
    var findings = [_]ValidateFinding{.{ .phase = .schema, .code = "schema_invalid", .index = 2 }};
    var out = std.ArrayList(u8).empty;
    defer out.deinit(allocator);
    try writeJsonReport(&out, allocator, "trace.json", .{ .judged = &findings });
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, out.items, .{});
    defer parsed.deinit();
    const report = parsed.value.object;
    try std.testing.expectEqualStrings("trace.json", report.get("file").?.string);
    try std.testing.expect(!report.get("valid").?.bool);
    try std.testing.expect(!report.get("complete").?.bool);
    const diagnostic = report.get("diagnostics").?.array.items[0].object;
    try std.testing.expectEqualStrings("schema", diagnostic.get("phase").?.string);
    try std.testing.expectEqualStrings("schema_invalid", diagnostic.get("code").?.string);
    try std.testing.expectEqual(@as(i64, 2), diagnostic.get("index").?.integer);
}

test "a SIGTERM ends the served backend as end of input does, exiting clean" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const stdin_pipe = try compat.stdio.pipe();
    const stdout_pipe = try compat.stdio.pipe();
    const stderr_pipe = try compat.stdio.pipe();
    endpoint_signals.install() catch {};
    defer endpoint_signals.reset();

    var runner = BackendRun{
        .allocator = std.heap.page_allocator,
        .name = "memory",
        .config_path = null,
        .stdin_file = stdin_pipe[0],
        .stdout_file = stdout_pipe[1],
        .stderr_file = stderr_pipe[1],
    };
    const thread = try std.Thread.spawn(.{}, BackendRun.run, .{&runner});
    try compat.stdio.writeLine(stdin_pipe[1], "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}");
    try std.posix.raise(std.posix.SIG.TERM);
    const written = try readAllFrom(allocator, stdout_pipe[0]);
    defer allocator.free(written);
    compat.stdio.close(stdout_pipe[0]);
    const complained = try readAllFrom(allocator, stderr_pipe[0]);
    defer allocator.free(complained);
    compat.stdio.close(stderr_pipe[0]);
    thread.join();
    compat.stdio.close(stdin_pipe[1]);

    try std.testing.expect(runner.err == null);
    try std.testing.expectEqual(@as(usize, 0), complained.len);
}

test "a SIGTERM ends the built-in agent loop as end of input does, after its answer is out" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const stdin_pipe = try compat.stdio.pipe();
    const stdout_pipe = try compat.stdio.pipe();
    const stderr_pipe = try compat.stdio.pipe();
    defer endpoint_signals.reset();

    var runner = BuiltinRun{ .stdin_file = stdin_pipe[0], .stdout_file = stdout_pipe[1], .stderr_file = stderr_pipe[1] };
    const thread = try std.Thread.spawn(.{}, BuiltinRun.run, .{&runner});
    try compat.stdio.writeLine(stdin_pipe[1], "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}");
    var first: [1]u8 = undefined;
    try std.testing.expectEqual(@as(usize, 1), try compat.stdio.read(stdout_pipe[0], &first));
    try std.posix.raise(std.posix.SIG.TERM);
    const rest = try readAllFrom(allocator, stdout_pipe[0]);
    defer allocator.free(rest);
    compat.stdio.close(stdout_pipe[0]);
    const complained = try readAllFrom(allocator, stderr_pipe[0]);
    defer allocator.free(complained);
    compat.stdio.close(stderr_pipe[0]);
    thread.join();
    compat.stdio.close(stdin_pipe[1]);

    try std.testing.expect(runner.err == null);
    try std.testing.expect(std.mem.indexOf(u8, rest, "\"capabilities.response\"") != null);
    try std.testing.expectEqual(@as(usize, 0), complained.len);
}

test "the built-in agent loop stops once its unread stdout passes the stall bound, saying why" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const stdin_pipe = try compat.stdio.pipe();
    const stdout_pipe = try compat.stdio.pipe();
    const stderr_pipe = try compat.stdio.pipe();
    const bound = backend_write_stall_ns;
    backend_write_stall_ns = 300 * std.time.ns_per_ms;
    defer backend_write_stall_ns = bound;

    var runner = BuiltinRun{ .stdin_file = stdin_pipe[0], .stdout_file = stdout_pipe[1], .stderr_file = stderr_pipe[1] };
    const thread = try std.Thread.spawn(.{}, BuiltinRun.run, .{&runner});
    var sent: usize = 0;
    while (sent < 400) : (sent += 1) {
        compat.stdio.writeLine(stdin_pipe[1], "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}") catch break;
    }
    const complained = try readAllFrom(allocator, stderr_pipe[0]);
    defer allocator.free(complained);
    compat.stdio.close(stderr_pipe[0]);
    thread.join();
    compat.stdio.close(stdout_pipe[0]);
    compat.stdio.close(stdin_pipe[1]);

    try std.testing.expectEqual(@as(?anyerror, error.OutputStalled), runner.err);
    try std.testing.expectEqualStrings(OUTPUT_STALLED_MESSAGE, complained);
}

test "a served backend whose stdout nobody reads stops once the stall bound passes, saying why" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const allocator = std.testing.allocator;
    const stdin_pipe = try compat.stdio.pipe();
    const stdout_pipe = try compat.stdio.pipe();
    const stderr_pipe = try compat.stdio.pipe();
    const bound = backend_write_stall_ns;
    backend_write_stall_ns = 300 * std.time.ns_per_ms;
    defer backend_write_stall_ns = bound;

    var runner = BackendRun{
        .allocator = std.heap.page_allocator,
        .name = "memory",
        .config_path = null,
        .stdin_file = stdin_pipe[0],
        .stdout_file = stdout_pipe[1],
        .stderr_file = stderr_pipe[1],
    };
    const thread = try std.Thread.spawn(.{}, BackendRun.run, .{&runner});
    var sent: usize = 0;
    while (sent < 400) : (sent += 1) {
        compat.stdio.writeLine(stdin_pipe[1], "{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"capabilities.request\",\"id\":\"q1\",\"payload\":{}}") catch break;
    }
    const complained = try readAllFrom(allocator, stderr_pipe[0]);
    defer allocator.free(complained);
    compat.stdio.close(stderr_pipe[0]);
    thread.join();
    compat.stdio.close(stdout_pipe[0]);
    compat.stdio.close(stdin_pipe[1]);

    try std.testing.expectEqual(@as(?anyerror, error.OutputStalled), runner.err);
    try std.testing.expectEqualStrings(BACKEND_OUTPUT_STALLED_MESSAGE, complained);
}

const CliCase = struct {
    good: [:0]u8,
    malformed: [:0]u8,
    core: [:0]u8,
    invalid: [:0]u8,
    base: [:0]u8,
};

fn cliCases(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir) !CliCase {
    try tmp.dir.createDir(std.testing.io, "good", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "good/pack.json",
        .data = "{\"id\":\"com.example.note\",\"version\":\"1.0.0\",\"schemas\":[\"note.schema.json\"],\"envelope_types\":[{\"type\":\"com.example.note.ping\",\"role\":\"event\",\"schema\":\"note.schema.json#/$defs/ping\"}]}",
    });
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "good/note.schema.json",
        .data = "{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"$defs\":{\"ping\":{\"type\":\"object\",\"required\":[\"type\",\"session_id\"],\"properties\":{\"type\":{\"const\":\"com.example.note.ping\"},\"session_id\":{\"type\":\"string\"}}}}}",
    });
    try tmp.dir.createDir(std.testing.io, "malformed", .default_dir);
    try tmp.dir.writeFile(std.testing.io, .{
        .sub_path = "malformed/pack.json",
        .data = "{\"id\":\"com.example.malformed\",\"version\":\"1.0.0\",\"schemas\":\"note.schema.json\",\"envelope_types\":[]}",
    });
    const cwd = try std.process.currentPathAlloc(std.testing.io, allocator);
    defer allocator.free(cwd);
    const base = try std.fmt.allocPrintSentinel(allocator, "{s}/.zig-cache/tmp/{s}", .{ cwd[0..], tmp.sub_path[0..] }, 0);
    const good = try std.fmt.allocPrintSentinel(allocator, "{s}/good", .{base}, 0);
    const malformed = try std.fmt.allocPrintSentinel(allocator, "{s}/malformed", .{base}, 0);
    const core = try std.fmt.allocPrintSentinel(allocator, "{s}/fixtures/valid/core-completed.json", .{cwd[0..]}, 0);
    const invalid = try std.fmt.allocPrintSentinel(allocator, "{s}/fixtures/packs/bad-unprefixed-name", .{cwd[0..]}, 0);
    return .{ .good = good, .malformed = malformed, .core = core, .invalid = invalid, .base = base };
}

fn freeCliCases(allocator: std.mem.Allocator, cases: CliCase) void {
    allocator.free(cases.good);
    allocator.free(cases.malformed);
    allocator.free(cases.core);
    allocator.free(cases.invalid);
    allocator.free(cases.base);
}

fn runCliCase(
    allocator: std.mem.Allocator,
    tmp: *std.testing.TmpDir,
    stem: []const u8,
    packs: []const []const u8,
    trace: []const u8,
) !struct { refused: bool, said: []u8, judged: []u8 } {
    var args = std.ArrayList([]const u8).empty;
    defer args.deinit(allocator);
    for (packs) |dir| {
        try args.append(allocator, "--pack");
        try args.append(allocator, dir);
    }
    try args.append(allocator, "--format=json");
    try args.append(allocator, trace);
    const out_name = try std.fmt.allocPrint(allocator, "{s}out", .{stem});
    defer allocator.free(out_name);
    const err_name = try std.fmt.allocPrint(allocator, "{s}err", .{stem});
    defer allocator.free(err_name);
    var out = try tmp.dir.createFile(std.testing.io, out_name, .{});
    var complained = try tmp.dir.createFile(std.testing.io, err_name, .{});
    var refused = false;
    _ = runValidate(allocator, args.items, out, complained) catch |err| {
        try std.testing.expectEqual(error.Unavailable, err);
        refused = true;
    };
    out.close(std.testing.io);
    complained.close(std.testing.io);
    return .{
        .refused = refused,
        .said = try tmp.dir.readFileAlloc(std.testing.io, err_name, allocator, .limited(1 << 20)),
        .judged = try tmp.dir.readFileAlloc(std.testing.io, out_name, allocator, .limited(1 << 20)),
    };
}

test "validate judges a pure core trace and a contributed type with no refusal" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cases = try cliCases(allocator, &tmp);
    defer freeCliCases(allocator, cases);

    const core = try runCliCase(allocator, &tmp, "core", &.{}, cases.core);
    defer allocator.free(core.said);
    defer allocator.free(core.judged);
    try std.testing.expect(!core.refused);
    try std.testing.expect(std.mem.indexOf(u8, core.judged, "schema_invalid") == null);

    const contributed = try runCliCase(allocator, &tmp, "contributed", &.{cases.good}, cases.core);
    defer allocator.free(contributed.said);
    defer allocator.free(contributed.judged);
    try std.testing.expect(!contributed.refused);
    try std.testing.expect(std.mem.indexOf(u8, contributed.judged, "schema_invalid") == null);
}

test "validate refuses the load when a pack is invalid, and says which code" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const cases = try cliCases(allocator, &tmp);
    defer freeCliCases(allocator, cases);

    const mixed = try runCliCase(allocator, &tmp, "mixed", &.{ cases.good, cases.invalid }, cases.core);
    defer allocator.free(mixed.said);
    defer allocator.free(mixed.judged);
    try std.testing.expect(mixed.refused);
    try std.testing.expect(std.mem.indexOf(u8, mixed.said, "pack_unprefixed_name") != null);
    try std.testing.expect(std.mem.indexOf(u8, mixed.judged, "\"valid\": true") == null);

    const malformed = try runCliCase(allocator, &tmp, "malformed", &.{cases.malformed}, cases.core);
    defer allocator.free(malformed.said);
    defer allocator.free(malformed.judged);
    try std.testing.expect(malformed.refused);
    try std.testing.expect(std.mem.indexOf(u8, malformed.judged, "\"valid\": true") == null);
}

test "validate names the code of a refusal whose pack id is longer than any fixed line buffer" {
    const allocator = std.testing.allocator;
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    try tmp.dir.createDir(std.testing.io, "longid", .default_dir);
    const long_id = try std.fmt.allocPrint(allocator, "com.example.{s}", .{"x" ** 400});
    defer allocator.free(long_id);
    const descriptor = try std.fmt.allocPrint(allocator,
        \\{{"id": "{s}", "version": "1.0.0", "capability_keys": ["capabilities.request.thing"]}}
    , .{long_id});
    defer allocator.free(descriptor);
    try tmp.dir.writeFile(std.testing.io, .{ .sub_path = "longid/pack.json", .data = descriptor });
    const long_dir = try tmp.dir.realPathFileAlloc(std.testing.io, "longid", allocator);
    defer allocator.free(long_dir);

    const cases = try cliCases(allocator, &tmp);
    defer freeCliCases(allocator, cases);

    const long_run = try runCliCase(allocator, &tmp, "longid", &.{long_dir}, cases.core);
    defer allocator.free(long_run.said);
    defer allocator.free(long_run.judged);
    try std.testing.expect(long_run.refused);
    try std.testing.expect(std.mem.indexOf(u8, long_run.said, "pack load refused:") != null);
    try std.testing.expect(std.mem.indexOf(u8, long_run.said, "pack_unprefixed_name") != null);
    try std.testing.expect(std.mem.indexOf(u8, long_run.said, long_id) != null);
    try std.testing.expect(std.mem.indexOf(u8, long_run.judged, "\"valid\": true") == null);

    const short_run = try runCliCase(allocator, &tmp, "shortid", &.{cases.invalid}, cases.core);
    defer allocator.free(short_run.said);
    defer allocator.free(short_run.judged);
    try std.testing.expect(short_run.refused);
    try std.testing.expect(std.mem.indexOf(u8, short_run.said, "pack load refused:") != null);
    try std.testing.expect(std.mem.indexOf(u8, short_run.said, "pack_unprefixed_name") != null);
    try std.testing.expect(std.mem.indexOf(u8, short_run.judged, "\"valid\": true") == null);
}
