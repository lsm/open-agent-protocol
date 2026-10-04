const std = @import("std");
const ai_types = @import("ai_types");
const agent = @import("agent");
const tui_runtime = @import("tui_runtime");
const tui_state = @import("tui_state");

pub const over_oap_autocompact_refusal = "this session's endpoint does not take a compaction policy over OAP, so /autocompact does not reach it. Use oapx --tui to change it.";
pub const between_runs_refusal = "the thinking level changes between runs over OAP; set it again once this run ends.";
pub const over_oap_setting_refusal = "oapx tui fixes this setting when the session opens, and this session cannot change it mid-session over OAP. Use oapx --tui to change it.";

pub const CommandKind = enum {
    help,
    model,
    login,
    logout,
    provider,
    status,
    @"resume",
    rename,
    permissions,
    think,
    clear,
    compact,
    context,
    output,
    autocompact,
    verbose,
    zen,
    redraw,
    settings,
    abort,
    quit,
};

pub const Command = struct {
    kind: CommandKind,
    arg: ?[]const u8 = null,
};

pub const ParseError = error{
    NotACommand,
    EmptyCommand,
    UnknownCommand,
};

pub const CommandAction = enum {
    none,
    quit,
    clear_transcript,
    open_session_picker,
    rename_session,
    open_model_picker,
    refresh_models,
    logout_provider,
    add_provider,
    show_status,
    compact_during_run,
    redraw,
    remove_provider,
    list_providers,
    open_login_picker,
    open_permission_picker,
    open_settings_picker,
    start_login_provider,
    compact,
};

pub const CommandResult = struct {
    action: CommandAction = .none,
    output: []u8 = &.{},
    login_provider: []u8 = &.{},
    is_error: bool = false,

    pub fn deinit(self: *CommandResult, allocator: std.mem.Allocator) void {
        if (self.output.len > 0) allocator.free(self.output);
        if (self.login_provider.len > 0) allocator.free(self.login_provider);
        self.* = undefined;
    }
};

pub const CommandContext = struct {
    allocator: std.mem.Allocator,
    state: *tui_state.AppState,
    runtime: ?*tui_runtime.TuiRuntime = null,
    session: ?*tui_runtime.TuiSession = null,
};

const Handler = *const fn (CommandContext, Command) anyerror!CommandResult;

pub const CommandInfo = struct {
    name: []const u8,
    kind: CommandKind,
    usage: []const u8,
    description: []const u8,
    handler: Handler,
};

pub const commands = [_]CommandInfo{
    .{ .name = "help", .kind = .help, .usage = "/help", .description = "List available commands", .handler = handleHelp },
    .{ .name = "model", .kind = .model, .usage = "/model [name|refresh]", .description = "Open model picker, switch active model, or refetch every provider's models", .handler = handleModel },
    .{ .name = "login", .kind = .login, .usage = "/login [provider]", .description = "Sign in to a provider", .handler = handleLogin },
    .{ .name = "logout", .kind = .logout, .usage = "/logout <provider>", .description = "Remove a provider's saved credential", .handler = handleLogout },
    .{ .name = "status", .kind = .status, .usage = "/status", .description = "Show session status", .handler = handleStatus },
    .{ .name = "sessions", .kind = .@"resume", .usage = "/sessions", .description = "Open saved sessions", .handler = handleSessions },
    .{ .name = "resume", .kind = .@"resume", .usage = "/resume", .description = "Open saved sessions", .handler = handleSessions },
    .{ .name = "rename", .kind = .rename, .usage = "/rename <title>", .description = "Rename this session", .handler = handleRename },
    .{ .name = "permissions", .kind = .permissions, .usage = "/permissions [ask|bypass]", .description = "Pick or set tool permission mode", .handler = handlePermissions },
    .{ .name = "perm", .kind = .permissions, .usage = "/perm [ask|bypass]", .description = "Pick or set tool permission mode", .handler = handlePermissions },
    .{ .name = "provider", .kind = .provider, .usage = "/provider add <id> <base_url> [--api <api>] [--env <NAME> | --no-auth] | /provider del <id> | /provider list", .description = "Declare, delete, or list the custom providers in ~/.oapx/providers.json", .handler = handleProvider },
    .{ .name = "think", .kind = .think, .usage = "/think [off|low|medium|high|xhigh|max]", .description = "Show or set the thinking level", .handler = handleThink },
    .{ .name = "clear", .kind = .clear, .usage = "/clear", .description = "Clear transcript display", .handler = handleClear },
    .{ .name = "compact", .kind = .compact, .usage = "/compact [focus]", .description = "Summarize the conversation to free context", .handler = handleCompact },
    .{ .name = "context", .kind = .context, .usage = "/context [tokens|default]", .description = "Show or set the context window for this session", .handler = handleContext },
    .{ .name = "output", .kind = .output, .usage = "/output [auto|max|tokens]", .description = "Show or set how much output a reply may ask for", .handler = handleOutput },
    .{ .name = "autocompact", .kind = .autocompact, .usage = "/autocompact [auto|percent|tokens|off]", .description = "Show or set when the conversation compacts on its own", .handler = handleAutoCompact },
    .{ .name = "verbose", .kind = .verbose, .usage = "/verbose [quiet|normal|verbose] | /verbose <thinking|tools|output|notices|status> <level>", .description = "Show or set how much the transcript and status bar show", .handler = handleVerbose },
    .{ .name = "zen", .kind = .zen, .usage = "/zen [on|off]", .description = "Hide the transcript behind a flow and show only the final reply; tells the agent to work quietly", .handler = handleZen },
    .{ .name = "redraw", .kind = .redraw, .usage = "/redraw", .description = "Clear the terminal and reprint the session at the current verbosity", .handler = handleRedraw },
    .{ .name = "settings", .kind = .settings, .usage = "/settings", .description = "Configure TUI settings", .handler = handleSettings },
    .{ .name = "abort", .kind = .abort, .usage = "/abort", .description = "Cancel the active streaming turn", .handler = handleAbort },
    .{ .name = "quit", .kind = .quit, .usage = "/quit", .description = "Exit TUI", .handler = handleQuit },
};

pub fn parse(input: []const u8) ParseError!Command {
    const trimmed = std.mem.trim(u8, input, " \t\r\n");
    if (trimmed.len == 0 or trimmed[0] != '/') return error.NotACommand;
    const body = trimLeftAscii(trimmed[1..]);
    if (body.len == 0) return error.EmptyCommand;

    var name_end: usize = 0;
    while (name_end < body.len and !std.ascii.isWhitespace(body[name_end])) : (name_end += 1) {}
    const name = body[0..name_end];
    const arg_text = std.mem.trim(u8, body[name_end..], " \t\r\n");

    if (findCommand(name)) |info| return .{ .kind = info.kind, .arg = if (arg_text.len > 0) arg_text else null };
    return error.UnknownCommand;
}

pub fn parseOrMessage(allocator: std.mem.Allocator, input: []const u8) !CommandParseResult {
    const command = parse(input) catch |err| switch (err) {
        error.UnknownCommand => return .{ .message = try unknownCommandMessage(allocator, input) },
        error.EmptyCommand => return .{ .message = try allocator.dupe(u8, "empty command. Type /help for commands") },
        error.NotACommand => return err,
    };
    return .{ .command = command };
}

pub const CommandParseResult = union(enum) {
    command: Command,
    message: []u8,

    pub fn deinit(self: *CommandParseResult, allocator: std.mem.Allocator) void {
        switch (self.*) {
            .message => |message| allocator.free(message),
            .command => {},
        }
        self.* = undefined;
    }
};

pub fn dispatch(ctx: CommandContext, command: Command) !CommandResult {
    const info = findCommandByKind(command.kind) orelse return .{ .output = try ctx.allocator.dupe(u8, "unknown command"), .is_error = true };
    return try info.handler(ctx, command);
}

pub fn helpText(allocator: std.mem.Allocator) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    const writer = &out.writer;
    try writer.writeAll("Available commands:\n");
    for (&commands) |info| try writer.print("  {s:<18} {s}\n", .{ info.usage, info.description });
    try writer.writeAll(
        \\Keys:
        \\  Enter              send (steer while streaming)
        \\  Shift+Enter        newline
        \\  Tab                complete slash command (queue follow-up while streaming)
        \\  Esc                clear draft, then abort turn, then close modal (cancels compaction)
        \\  Ctrl+C             clear draft or abort turn, again within ~1.5s to quit (quits at once when idle)
        \\  Ctrl+D             quit when the composer is empty and idle
        \\  Ctrl+Y             copy the last reply
        \\  Shift+Tab          cycle thinking level
        \\  Up/Down            palette selection while open, otherwise history
        \\  PgUp/PgDn          scroll the transcript (mouse wheel when mouse reporting is on)
        \\  Ctrl+A/E           jump to line start/end
        \\  Ctrl+U/K           cut to line start/end
        \\  Ctrl+W             delete word backward (Alt+Backspace too)
        \\  Ctrl+Left/Right    move by word (Alt+Left/Right and Alt+B/F too)
        \\  Delete             delete forward
        \\Drafts starting with ! ask the agent to run a command; @ names a file path.
        \\
    );
    return out.toOwnedSlice();
}

pub fn unknownCommandMessage(allocator: std.mem.Allocator, input: []const u8) ![]u8 {
    const name = commandName(input) orelse "";
    if (suggestCommand(name)) |suggestion| {
        return std.fmt.allocPrint(allocator, "unknown command: /{s}. Did you mean /{s}?", .{ name, suggestion });
    }
    return std.fmt.allocPrint(allocator, "unknown command: /{s}. Type /help for commands", .{name});
}

fn trimLeftAscii(input: []const u8) []const u8 {
    var start: usize = 0;
    while (start < input.len and (input[start] == ' ' or input[start] == '\t')) : (start += 1) {}
    return input[start..];
}

fn commandName(input: []const u8) ?[]const u8 {
    const trimmed = std.mem.trim(u8, input, " \t\r\n");
    if (trimmed.len == 0 or trimmed[0] != '/') return null;
    const body = trimLeftAscii(trimmed[1..]);
    if (body.len == 0) return null;
    var end: usize = 0;
    while (end < body.len and !std.ascii.isWhitespace(body[end])) : (end += 1) {}
    return body[0..end];
}

fn findCommand(name: []const u8) ?CommandInfo {
    for (&commands) |info| if (std.mem.eql(u8, info.name, name)) return info;
    return null;
}

fn findCommandByKind(kind: CommandKind) ?CommandInfo {
    for (&commands) |info| if (info.kind == kind) return info;
    return null;
}

fn suggestCommand(name: []const u8) ?[]const u8 {
    if (name.len == 0) return null;
    for (&commands) |info| if (std.mem.startsWith(u8, info.name, name) or std.mem.startsWith(u8, name, info.name)) return info.name;
    for (&commands) |info| if (info.name[0] == name[0]) return info.name;
    return null;
}

fn handleHelp(ctx: CommandContext, command: Command) !CommandResult {
    _ = command;
    return .{ .output = try helpText(ctx.allocator) };
}

fn handleModel(ctx: CommandContext, command: Command) !CommandResult {
    if (command.arg) |model_id| {
        if (std.mem.eql(u8, model_id, "refresh")) return .{ .action = .refresh_models };
        if (ctx.session) |session| {
            try session.switchModel(model_id);
        } else if (ctx.runtime) |runtime| {
            try runtime.switchModel(model_id);
        } else {
            return error.NoRuntimeConfigured;
        }
        const model = currentModel(ctx);
        if (model) |m| try ctx.state.status.setModel(ctx.allocator, m.id, m.provider);
        return .{ .output = try std.fmt.allocPrint(ctx.allocator, "model switched to {s}", .{model_id}) };
    }
    return .{ .action = .open_model_picker };
}

fn handleLogin(ctx: CommandContext, command: Command) !CommandResult {
    if (command.arg) |provider| {
        return .{
            .action = .start_login_provider,
            .login_provider = try ctx.allocator.dupe(u8, provider),
        };
    }
    return .{ .action = .open_login_picker };
}

fn runIsActive(ctx: CommandContext) bool {
    if (ctx.state.status.streaming or ctx.state.status.compacting) return true;
    const runtime = ctx.runtime orelse return false;
    return !runtime.isIdle();
}

pub const provider_usage = "usage: /provider add <id> <base_url> [--api openai-completions|openai-responses|anthropic-messages] [--env <NAME> | --no-auth], /provider del <id>, or /provider list";

fn handleProvider(ctx: CommandContext, command: Command) !CommandResult {
    const arg = command.arg orelse return .{ .output = try ctx.allocator.dupe(u8, provider_usage), .is_error = true };
    var words = std.mem.tokenizeAny(u8, arg, " \t");
    const verb = words.next() orelse return .{ .output = try ctx.allocator.dupe(u8, provider_usage), .is_error = true };
    if (std.mem.eql(u8, verb, "del") or std.mem.eql(u8, verb, "delete")) {
        _ = words.next() orelse return .{ .output = try ctx.allocator.dupe(u8, provider_usage), .is_error = true };
        if (words.peek() != null) return .{ .output = try ctx.allocator.dupe(u8, provider_usage), .is_error = true };
        if (runIsActive(ctx)) return .{ .output = try ctx.allocator.dupe(u8, "A turn is running; delete the provider once it finishes."), .is_error = true };
        return .{ .action = .remove_provider };
    }
    if (std.mem.eql(u8, verb, "list")) {
        if (words.peek() != null) return .{ .output = try ctx.allocator.dupe(u8, provider_usage), .is_error = true };
        return .{ .action = .list_providers };
    }
    if (!std.mem.eql(u8, verb, "add") or words.peek() == null) return .{ .output = try ctx.allocator.dupe(u8, provider_usage), .is_error = true };
    return .{ .action = .add_provider };
}

fn handleLogout(ctx: CommandContext, command: Command) !CommandResult {
    if (command.arg == null) return .{ .output = try ctx.allocator.dupe(u8, "usage: /logout <provider>"), .is_error = true };
    if (runIsActive(ctx)) return .{ .output = try ctx.allocator.dupe(u8, "A turn is running; log out once it finishes."), .is_error = true };
    return .{ .action = .logout_provider };
}

fn handleStatus(ctx: CommandContext, command: Command) !CommandResult {
    _ = ctx;
    _ = command;
    return .{ .action = .show_status };
}

fn handleSessions(ctx: CommandContext, command: Command) !CommandResult {
    _ = command;
    if (ctx.state.sessions.items.len == 0) {
        return .{ .output = try ctx.allocator.dupe(u8, "no saved sessions") };
    }
    return .{ .action = .open_session_picker };
}

fn handlePermissions(ctx: CommandContext, command: Command) !CommandResult {
    if (command.arg) |arg| {
        const mode = parsePermissionMode(arg) orelse {
            return .{
                .output = try std.fmt.allocPrint(ctx.allocator, "unknown permission mode: {s}", .{arg}),
                .is_error = true,
            };
        };
        const runtime = ctx.runtime orelse return error.NoRuntimeConfigured;
        runtime.setPermissionMode(mode) catch |err| switch (err) {
            error.UnavailableOverOap => return .{ .output = try ctx.allocator.dupe(u8, over_oap_setting_refusal), .is_error = true },
            else => return err,
        };
        ctx.state.permission_mode = mode;
        return .{ .output = try std.fmt.allocPrint(ctx.allocator, "permission mode set to {s}", .{@tagName(mode)}) };
    }

    if (ctx.runtime) |runtime| {
        ctx.state.permission_mode = runtime.permissionMode();
    }
    return .{ .action = .open_permission_picker };
}

fn parsePermissionMode(value: []const u8) ?tui_runtime.PermissionMode {
    if (std.mem.eql(u8, value, "ask")) return .ask;
    if (std.mem.eql(u8, value, "bypass")) return .bypass;
    return null;
}

fn handleThink(ctx: CommandContext, command: Command) !CommandResult {
    const arg = command.arg orelse {
        return .{ .output = try std.fmt.allocPrint(ctx.allocator, "thinking level: {s}", .{@tagName(ctx.state.thinking_level)}) };
    };
    const level = parseThinkingLevel(arg) orelse {
        return .{
            .output = try std.fmt.allocPrint(ctx.allocator, "unknown thinking level: {s}. Use off, low, medium, high, xhigh or max", .{arg}),
            .is_error = true,
        };
    };
    if (ctx.runtime) |runtime| runtime.setThinkingLevel(level) catch |err| {
        return .{ .output = try ctx.allocator.dupe(u8, if (err == error.RunInProgress) between_runs_refusal else over_oap_setting_refusal), .is_error = true };
    };
    ctx.state.thinking_level = level;
    return .{ .output = try std.fmt.allocPrint(ctx.allocator, "thinking level set to {s}", .{@tagName(level)}) };
}

fn parseThinkingLevel(value: []const u8) ?ai_types.ThinkingLevel {
    const level = std.meta.stringToEnum(ai_types.ThinkingLevel, value) orelse return null;
    return if (level == .minimal) null else level;
}

fn handleSettings(ctx: CommandContext, command: Command) !CommandResult {
    _ = ctx;
    _ = command;
    return .{ .action = .open_settings_picker };
}

fn handleRename(ctx: CommandContext, command: Command) !CommandResult {
    if (command.arg == null) return .{ .output = try ctx.allocator.dupe(u8, "usage: /rename <title>"), .is_error = true };
    return .{ .action = .rename_session };
}

fn handleClear(ctx: CommandContext, command: Command) !CommandResult {
    _ = command;
    return .{ .action = .clear_transcript, .output = try ctx.allocator.dupe(u8, "transcript cleared") };
}

fn handleCompact(ctx: CommandContext, command: Command) !CommandResult {
    _ = command;
    if (ctx.state.status.compacting) return .{ .output = try ctx.allocator.dupe(u8, "Already compacting; esc cancels.") };
    if (ctx.state.status.streaming) return .{ .action = .compact_during_run };
    return .{ .action = .compact };
}

fn handleContext(ctx: CommandContext, command: Command) !CommandResult {
    const runtime = ctx.runtime orelse return error.NoRuntimeConfigured;
    const model = runtime.currentModel() orelse return error.NoModelConfigured;
    const arg = command.arg orelse return .{ .output = try contextWindowReport(ctx.allocator, runtime) };
    if (std.ascii.eqlIgnoreCase(arg, "default")) {
        runtime.setContextWindow(null) catch |err| switch (err) {
            error.AgentAlreadyStreaming => return .{
                .output = try ctx.allocator.dupe(u8, "A turn is running; restore the catalog's window once it finishes."),
                .is_error = true,
            },
            error.AboveMaximum => return error.AboveMaximum,
            error.UnavailableOverOap => return .{ .output = try ctx.allocator.dupe(u8, over_oap_setting_refusal), .is_error = true },
        };
        return .{ .output = try contextWindowReport(ctx.allocator, runtime) };
    }
    const window = tui_runtime.parseContextWindow(arg) catch {
        return .{
            .output = try std.fmt.allocPrint(ctx.allocator, "not a token count: {s}. Give a whole number, optionally with k or m, such as 1m", .{arg}),
            .is_error = true,
        };
    };
    runtime.setContextWindow(window) catch |err| switch (err) {
        error.AboveMaximum => return .{
            .output = try std.fmt.allocPrint(ctx.allocator, "{s} takes at most {d} context tokens; {d} is above it. The window in effect is {d}.", .{ model.id, runtime.contextWindowMaximum() orelse 0, window, runtime.contextWindow() }),
            .is_error = true,
        },
        error.AgentAlreadyStreaming => return .{
            .output = try ctx.allocator.dupe(u8, "A turn is running; set the context window once it finishes."),
            .is_error = true,
        },
        error.UnavailableOverOap => return .{ .output = try ctx.allocator.dupe(u8, over_oap_setting_refusal), .is_error = true },
    };
    return .{ .output = try contextWindowReport(ctx.allocator, runtime) };
}

fn contextWindowReport(allocator: std.mem.Allocator, runtime: *tui_runtime.TuiRuntime) ![]u8 {
    const model = runtime.currentModel() orelse return allocator.dupe(u8, "no model");
    var out: std.Io.Writer.Allocating = .init(allocator);
    const writer = &out.writer;
    try writer.print("context window: {d} for {s} ({s})", .{ runtime.contextWindow(), model.id, model.provider });
    if (runtime.contextWindowMaximum()) |ceiling| {
        try writer.print(", up to {d}", .{ceiling});
    } else {
        try writer.writeAll(", and the model reports no window of its own, so the provider may refuse a request this size");
    }
    try writer.writeAll(". /context default restores the catalog's window.");
    return out.toOwnedSlice();
}

fn handleOutput(ctx: CommandContext, command: Command) !CommandResult {
    const runtime = ctx.runtime orelse return error.NoRuntimeConfigured;
    const model = runtime.currentModel() orelse return error.NoModelConfigured;
    const arg = command.arg orelse return .{ .output = try outputReport(ctx.allocator, runtime) };
    const setting: agent.OutputSetting = if (std.ascii.eqlIgnoreCase(arg, "auto"))
        .auto
    else if (std.ascii.eqlIgnoreCase(arg, "max"))
        .max
    else
        .{ .tokens = tui_runtime.parseContextWindow(arg) catch {
            return .{
                .output = try std.fmt.allocPrint(ctx.allocator, "not a token count: {s}. Give auto, max, or a whole number, optionally with k, such as 64k", .{arg}),
                .is_error = true,
            };
        } };
    runtime.setOutput(setting) catch |err| switch (err) {
        error.AboveMaximum => return .{
            .output = try std.fmt.allocPrint(ctx.allocator, "{s} writes at most {d} tokens in a reply; {d} is above it. /output max asks for all of it.", .{ model.id, model.max_tokens, setting.tokens }),
            .is_error = true,
        },
        error.AgentAlreadyStreaming => return .{
            .output = try ctx.allocator.dupe(u8, "A turn is running; set the output limit once it finishes."),
            .is_error = true,
        },
        error.UnavailableOverOap => return .{ .output = try ctx.allocator.dupe(u8, over_oap_setting_refusal), .is_error = true },
    };
    return .{ .output = try outputReport(ctx.allocator, runtime) };
}

fn outputReport(allocator: std.mem.Allocator, runtime: *tui_runtime.TuiRuntime) ![]u8 {
    const model = runtime.currentModel() orelse return allocator.dupe(u8, "no model");
    const setting = runtime.outputSetting();
    var out: std.Io.Writer.Allocating = .init(allocator);
    const writer = &out.writer;
    try writer.print("output: {s}, {d} tokens a reply for {s}", .{ switch (setting) {
        .auto => "auto",
        .max => "max",
        .tokens => "set",
    }, agent.outputRequest(model, setting), model.id });
    if (model.max_tokens > 0) {
        try writer.print(", up to {d}", .{model.max_tokens});
        if (setting == .auto and agent.outputRequest(model, setting) < model.max_tokens) {
            try writer.print(". A reply cut off below that is continued once at {d}", .{model.max_tokens});
        }
    } else {
        try writer.writeAll(", and the model reports no maximum of its own");
    }
    try writer.writeAll(". /output auto restores the default.");
    return out.toOwnedSlice();
}

pub const verbose_usage = "usage: /verbose [quiet|normal|verbose], or /verbose <thinking|tools|output|notices|status> <quiet|normal|verbose>";

fn handleVerbose(ctx: CommandContext, command: Command) !CommandResult {
    const arg = command.arg orelse return .{ .output = try verbosityReport(ctx) };
    var words = std.mem.tokenizeAny(u8, arg, " \t");
    const first = words.next() orelse return .{ .output = try verbosityReport(ctx) };
    if (std.meta.stringToEnum(tui_state.VerbosityLevel, first)) |level| {
        if (words.next() != null) return .{ .output = try ctx.allocator.dupe(u8, verbose_usage), .is_error = true };
        ctx.state.verbosity = tui_state.Verbosity.all(level);
        return .{ .output = try verbosityReport(ctx) };
    }
    const part = std.meta.stringToEnum(tui_state.VerbosityPart, first) orelse return .{ .output = try ctx.allocator.dupe(u8, verbose_usage), .is_error = true };
    const level_text = words.next() orelse return .{ .output = try ctx.allocator.dupe(u8, verbose_usage), .is_error = true };
    const level = std.meta.stringToEnum(tui_state.VerbosityLevel, level_text) orelse return .{ .output = try ctx.allocator.dupe(u8, verbose_usage), .is_error = true };
    if (words.next() != null) return .{ .output = try ctx.allocator.dupe(u8, verbose_usage), .is_error = true };
    ctx.state.verbosity.set(part, level);
    return .{ .output = try verbosityReport(ctx) };
}

pub const zen_usage = "usage: /zen [on|off]";

fn handleZen(ctx: CommandContext, command: Command) !CommandResult {
    const on = if (command.arg) |arg| blk: {
        if (std.ascii.eqlIgnoreCase(arg, "on")) break :blk true;
        if (std.ascii.eqlIgnoreCase(arg, "off")) break :blk false;
        return .{ .output = try ctx.allocator.dupe(u8, zen_usage), .is_error = true };
    } else !ctx.state.zen.on;
    if (on != ctx.state.zen.on) ctx.state.transcript_scroll = 0;
    if (on) ctx.state.zen.enter(tui_state.zenStart(ctx.state)) else ctx.state.zen.leave();
    return .{ .output = try ctx.allocator.dupe(u8, if (on) "zen on: the agent is asked to work quietly; /zen again to return" else "zen off") };
}

fn handleRedraw(ctx: CommandContext, command: Command) !CommandResult {
    _ = command;
    if (runIsActive(ctx)) return .{ .output = try ctx.allocator.dupe(u8, "A turn is running; redraw once it finishes."), .is_error = true };
    return .{ .action = .redraw };
}

fn verbosityReport(ctx: CommandContext) ![]u8 {
    const v = ctx.state.verbosity;
    return std.fmt.allocPrint(ctx.allocator, "verbosity: thinking {t}, tools {t}, output {t}, notices {t}, status {t}", .{ v.thinking, v.tools, v.output, v.notices, v.status });
}

fn handleAutoCompact(ctx: CommandContext, command: Command) !CommandResult {
    const arg = command.arg orelse return .{ .output = try autoCompactReport(ctx) };
    const previous = ctx.state.autocompact;
    if (std.ascii.eqlIgnoreCase(arg, "off") or std.ascii.eqlIgnoreCase(arg, "none")) {
        ctx.state.autocompact = .off;
    } else if (std.ascii.eqlIgnoreCase(arg, "auto")) {
        ctx.state.autocompact = .auto;
    } else if (parseAutoCompactTokens(arg)) |count| {
        ctx.state.autocompact = .{ .tokens = count };
    } else {
        const percent = parseAutoCompactShare(arg) orelse {
            return .{
                .output = try std.fmt.allocPrint(ctx.allocator, "not a share or a token count: {s}. Give a share of the context window from 1 to 100, optionally with a % sign; a token count with k, m or tokens, such as 120k; or auto, or off", .{arg}),
                .is_error = true,
            };
        };
        ctx.state.autocompact = .{ .percent = percent };
    }
    if (ctx.runtime) |runtime| if (runtime.remote != null) {
        var buffer: [96]u8 = undefined;
        const policy = try tui_state.autoCompactPolicyJson(&buffer, ctx.state.autocompact);
        runtime.setCompactionPolicy(policy) catch |err| switch (err) {
            error.UnavailableOverOap => {
                ctx.state.autocompact = previous;
                return .{ .output = try ctx.allocator.dupe(u8, over_oap_autocompact_refusal), .is_error = true };
            },
            error.RunInProgress => {},
            else => return err,
        };
    };
    return .{ .output = try autoCompactReport(ctx) };
}

fn autoCompactReport(ctx: CommandContext) ![]u8 {
    const allocator = ctx.allocator;
    switch (ctx.state.autocompact) {
        .off => return allocator.dupe(u8, "autocompact: off. The conversation is compacted only when you run /compact"),
        .percent => |percent| return std.fmt.allocPrint(allocator, "autocompact: {d}% of the context window.", .{percent}),
        .tokens => |count| return std.fmt.allocPrint(allocator, "autocompact: at {d} tokens.", .{count}),
        .auto => {
            const runtime = ctx.runtime orelse return allocator.dupe(u8, "autocompact: auto, at a point set by the model's context window and output limit.");
            const model = runtime.currentModel() orelse return allocator.dupe(u8, "autocompact: auto, at a point set by the model's context window and output limit.");
            const at = tui_state.autoCompactAt(.auto, model) orelse return allocator.dupe(u8, "autocompact: auto, but the model reports no context window, so nothing compacts on its own.");
            return std.fmt.allocPrint(allocator, "autocompact: auto, at {d} of the {d}-token context window, keeping room for a summary and a reply.", .{ at, model.context_window });
        },
    }
}

fn parseAutoCompactTokens(value: []const u8) ?u32 {
    const trimmed = std.mem.trim(u8, value, " \t\r\n");
    if (trimmed.len == 0) return null;
    if (std.ascii.endsWithIgnoreCase(trimmed, "tokens")) return tui_runtime.parseContextWindow(trimmed[0 .. trimmed.len - "tokens".len]) catch null;
    const last = trimmed[trimmed.len - 1];
    if (last != 'k' and last != 'K' and last != 'm' and last != 'M') return null;
    return tui_runtime.parseContextWindow(trimmed) catch null;
}

fn parseAutoCompactShare(value: []const u8) ?u8 {
    const trimmed = std.mem.trim(u8, value, " \t\r\n");
    if (trimmed.len == 0) return null;
    const digits = if (trimmed[trimmed.len - 1] == '%') trimmed[0 .. trimmed.len - 1] else trimmed;
    if (digits.len == 0) return null;
    for (digits) |c| {
        if (!std.ascii.isDigit(c)) return null;
    }
    const share = std.fmt.parseInt(u32, digits, 10) catch return null;
    if (share == 0 or share > 100) return null;
    return @intCast(share);
}

fn handleAbort(ctx: CommandContext, command: Command) !CommandResult {
    _ = command;
    if (ctx.state.status.compacting) {
        if (ctx.session) |session| {
            session.cancel();
        } else if (ctx.runtime) |runtime| {
            runtime.cancel();
        }
        return .{};
    }
    const active = ctx.state.status.streaming or
        (ctx.runtime != null and ctx.runtime.?.stream_active);
    if (!ctx.state.status.streaming and ctx.state.held_after_abort.items.len > 0) {
        const dropped = ctx.state.held_after_abort.items.len;
        ctx.state.clearHeldAfterAbort();
        return .{ .output = try std.fmt.allocPrint(ctx.allocator, "Dropped the {d} queued message{s}; they will not be sent.", .{ dropped, if (dropped == 1) "" else "s" }) };
    }
    if (active and ctx.state.stream_aborted and !ctx.state.status.streaming) {
        return .{ .output = try ctx.allocator.dupe(u8, "Still stopping: the step that was running when you aborted is ending.") };
    }
    if (active) {
        if (ctx.session) |session| {
            ctx.state.reconcileSteers(session.steersConsumedCount());
            ctx.state.setQueuedCounts(session.queuedCounts());
        } else if (ctx.runtime) |runtime| {
            ctx.state.reconcileSteers(runtime.steersConsumedCount());
            ctx.state.setQueuedCounts(runtime.queuedCounts());
        }
        if (ctx.session) |session| {
            session.cancel();
            session.clearQueuedMessages();
        } else if (ctx.runtime) |runtime| {
            runtime.cancel();
            runtime.clearQueuedMessages();
        } else {
            return .{ .output = try ctx.allocator.dupe(u8, "Nothing to abort — agent is idle.") };
        }
        ctx.state.status.streaming = false;
        ctx.state.telemetry.rate.messageAborted();
        ctx.state.stream_aborted = true;
        try ctx.state.holdQueuedAfterAbort();
        if (ctx.state.mode == .approval) {
            ctx.state.approval.deinit(ctx.allocator);
            ctx.state.mode = .normal;
        }
        const held = ctx.state.held_after_abort.items.len;
        if (held > 0) return .{ .output = try std.fmt.allocPrint(ctx.allocator, "Turn aborted. Sending the {d} queued message{s} once it stops; press esc again to drop {s}.", .{ held, if (held == 1) "" else "s", if (held == 1) "it" else "them" }) };
        return .{ .output = try ctx.allocator.dupe(u8, "Turn aborted.") };
    }
    return .{ .output = try ctx.allocator.dupe(u8, "Nothing to abort — agent is idle.") };
}

fn handleQuit(ctx: CommandContext, command: Command) !CommandResult {
    _ = ctx;
    _ = command;
    return .{ .action = .quit };
}

fn currentModel(ctx: CommandContext) ?ai_types.Model {
    if (ctx.session) |session| return session.currentModel();
    if (ctx.runtime) |runtime| return runtime.currentModel();
    return null;
}

test "parse model command with argument" {
    const command = try parse("/model gpt-4o");
    try std.testing.expectEqual(CommandKind.model, command.kind);
    try std.testing.expectEqualStrings("gpt-4o", command.arg.?);
}

test "parse abort command" {
    const command = try parse("/abort");
    try std.testing.expectEqual(CommandKind.abort, command.kind);
}

test "parse unknown command returns unknown message" {
    var parsed = try parseOrMessage(std.testing.allocator, "/unknown");
    defer parsed.deinit(std.testing.allocator);
    try std.testing.expect(parsed == .message);
    try std.testing.expect(std.mem.indexOf(u8, parsed.message, "unknown command") != null);
}

test "help output contains all command names" {
    const text = try helpText(std.testing.allocator);
    defer std.testing.allocator.free(text);
    for (&commands) |info| {
        const needle = try std.fmt.allocPrint(std.testing.allocator, "/{s}", .{info.name});
        defer std.testing.allocator.free(needle);
        try std.testing.expect(std.mem.indexOf(u8, text, needle) != null);
    }
}

test "dispatch reaches command handlers" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.status.setModel(std.testing.allocator, "model-a", "provider-a");
    try state.addSession("s1", "Saved");
    _ = try state.resolveToolOccurrenceForTest("t1", "file_write", "{}", .live_intent, .done);

    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };
    const kinds = [_]CommandKind{ .help, .status, .@"resume", .permissions, .clear, .abort, .quit };
    for (kinds) |kind| {
        var result = try dispatch(ctx, .{ .kind = kind });
        defer result.deinit(std.testing.allocator);
        if (kind == .quit) {
            try std.testing.expectEqual(CommandAction.quit, result.action);
        } else if (kind == .clear) {
            try std.testing.expectEqual(CommandAction.clear_transcript, result.action);
        } else if (kind == .@"resume") {
            try std.testing.expectEqual(CommandAction.open_session_picker, result.action);
        } else if (kind == .permissions) {
            try std.testing.expectEqual(CommandAction.open_permission_picker, result.action);
        } else if (kind == .status) {
            try std.testing.expectEqual(CommandAction.show_status, result.action);
        } else {
            try std.testing.expect(result.output.len > 0);
        }
    }
}

const context_test_model: ai_types.Model = .{
    .id = "gpt-5-codex",
    .name = "GPT-5 Codex",
    .api = "openai-responses",
    .provider = "openai",
    .base_url = "https://example.invalid",
    .reasoning = true,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 128_000,
    .max_tokens = 16_384,
};

const context_test_uncatalogued: ai_types.Model = .{
    .id = "local-model",
    .name = "Local",
    .api = "openai-completions",
    .provider = "not-a-catalogued-row",
    .base_url = "http://localhost:11434",
    .reasoning = false,
    .input = &.{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 128_000,
    .max_tokens = 8_192,
};

fn contextTestRuntime(models: []const ai_types.Model) !tui_runtime.TuiRuntime {
    return tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = models });
}

test "context sets the window for the session and names the model and the window" {
    const models = [_]ai_types.Model{context_test_model};
    var runtime = try contextTestRuntime(&models);
    defer runtime.deinit();
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime };

    var set = try dispatch(ctx, .{ .kind = .context, .arg = "1m" });
    defer set.deinit(std.testing.allocator);
    try std.testing.expect(!set.is_error);
    try std.testing.expect(std.mem.indexOf(u8, set.output, "context window: 1000000 for gpt-5-codex (openai)") != null);
    try std.testing.expect(std.mem.indexOf(u8, set.output, "up to 1000000") != null);
    try std.testing.expectEqual(@as(u64, 1_000_000), runtime.contextWindow());

    var shown = try dispatch(ctx, .{ .kind = .context });
    defer shown.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, shown.output, "context window: 1000000 for gpt-5-codex (openai)") != null);

    var reset = try dispatch(ctx, .{ .kind = .context, .arg = "default" });
    defer reset.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(u64, 128_000), runtime.contextWindow());
    try std.testing.expect(std.mem.indexOf(u8, reset.output, "context window: 128000 for gpt-5-codex (openai)") != null);
}

test "output sets how much a reply asks for, and refuses more than the model writes" {
    const models = [_]ai_types.Model{context_test_model};
    var runtime = try contextTestRuntime(&models);
    defer runtime.deinit();
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime };

    var shown = try dispatch(ctx, .{ .kind = .output });
    defer shown.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.indexOf(u8, shown.output, "output: auto, 16384 tokens a reply for gpt-5-codex, up to 16384") != null);

    var set = try dispatch(ctx, .{ .kind = .output, .arg = "8k" });
    defer set.deinit(std.testing.allocator);
    try std.testing.expect(!set.is_error);
    try std.testing.expectEqual(agent.OutputSetting{ .tokens = 8_000 }, runtime.outputSetting());
    try std.testing.expect(std.mem.indexOf(u8, set.output, "output: set, 8000 tokens a reply") != null);
    try std.testing.expect(std.mem.indexOf(u8, set.output, "continued") == null);
    try std.testing.expect(std.mem.indexOf(u8, shown.output, "continued") == null);

    var refused = try dispatch(ctx, .{ .kind = .output, .arg = "20k" });
    defer refused.deinit(std.testing.allocator);
    try std.testing.expect(refused.is_error);
    try std.testing.expect(std.mem.indexOf(u8, refused.output, "gpt-5-codex writes at most 16384 tokens in a reply; 20000 is above it") != null);
    try std.testing.expectEqual(agent.OutputSetting{ .tokens = 8_000 }, runtime.outputSetting());

    var max = try dispatch(ctx, .{ .kind = .output, .arg = "max" });
    defer max.deinit(std.testing.allocator);
    try std.testing.expectEqual(agent.OutputSetting.max, runtime.outputSetting());

    var bad = try dispatch(ctx, .{ .kind = .output, .arg = "lots" });
    defer bad.deinit(std.testing.allocator);
    try std.testing.expect(bad.is_error);
    try std.testing.expectEqual(agent.OutputSetting.max, runtime.outputSetting());
}

test "context refuses a window above the ceiling and says what the ceiling is" {
    const models = [_]ai_types.Model{context_test_model};
    var runtime = try contextTestRuntime(&models);
    defer runtime.deinit();
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime };

    var refused = try dispatch(ctx, .{ .kind = .context, .arg = "2m" });
    defer refused.deinit(std.testing.allocator);
    try std.testing.expect(refused.is_error);
    try std.testing.expect(std.mem.indexOf(u8, refused.output, "gpt-5-codex takes at most 1000000 context tokens; 2000000 is above it") != null);
    try std.testing.expectEqual(@as(u64, 128_000), runtime.contextWindow());
}

test "context lowers a window freely and says a model with no window of its own" {
    const models = [_]ai_types.Model{context_test_uncatalogued};
    var runtime = try contextTestRuntime(&models);
    defer runtime.deinit();
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime };

    var lowered = try dispatch(ctx, .{ .kind = .context, .arg = "32k" });
    defer lowered.deinit(std.testing.allocator);
    try std.testing.expect(!lowered.is_error);
    try std.testing.expect(std.mem.indexOf(u8, lowered.output, "context window: 32000 for local-model") != null);
    try std.testing.expect(std.mem.indexOf(u8, lowered.output, "the model reports no window of its own, so the provider may refuse a request this size") != null);

    var raised = try dispatch(ctx, .{ .kind = .context, .arg = "1000000" });
    defer raised.deinit(std.testing.allocator);
    try std.testing.expect(!raised.is_error);
    try std.testing.expectEqual(@as(u64, 1_000_000), runtime.contextWindow());
}

test "context refuses a value that is not a token count" {
    const models = [_]ai_types.Model{context_test_model};
    var runtime = try contextTestRuntime(&models);
    defer runtime.deinit();
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime };

    var refused = try dispatch(ctx, .{ .kind = .context, .arg = "lots" });
    defer refused.deinit(std.testing.allocator);
    try std.testing.expect(refused.is_error);
    try std.testing.expect(std.mem.indexOf(u8, refused.output, "not a token count: lots") != null);
    try std.testing.expectEqual(@as(u64, 128_000), runtime.contextWindow());
}

test "autocompact sets, reports and turns off the share for the session" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };

    var shown = try dispatch(ctx, .{ .kind = .autocompact });
    defer shown.deinit(std.testing.allocator);
    try std.testing.expect(state.autocompact == .auto);
    try std.testing.expect(std.mem.startsWith(u8, shown.output, "autocompact: auto"));

    var set = try dispatch(ctx, .{ .kind = .autocompact, .arg = "80%" });
    defer set.deinit(std.testing.allocator);
    try std.testing.expect(!set.is_error);
    try std.testing.expectEqualDeep(tui_state.AutoCompactSetting{ .percent = 80 }, state.autocompact);
    try std.testing.expectEqualStrings("autocompact: 80% of the context window.", set.output);

    var bare = try dispatch(ctx, .{ .kind = .autocompact, .arg = "55" });
    defer bare.deinit(std.testing.allocator);
    try std.testing.expectEqualDeep(tui_state.AutoCompactSetting{ .percent = 55 }, state.autocompact);

    var again = try dispatch(ctx, .{ .kind = .autocompact });
    defer again.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("autocompact: 55% of the context window.", again.output);

    var off = try dispatch(ctx, .{ .kind = .autocompact, .arg = "off" });
    defer off.deinit(std.testing.allocator);
    try std.testing.expect(state.autocompact == .off);
    try std.testing.expect(std.mem.indexOf(u8, off.output, "autocompact: off") != null);

    var auto = try dispatch(ctx, .{ .kind = .autocompact, .arg = "auto" });
    defer auto.deinit(std.testing.allocator);
    try std.testing.expect(state.autocompact == .auto);
}

test "autocompact takes a token count with k, m or tokens, and keeps a bare number a share" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };

    for ([_]struct { arg: []const u8, tokens: u32 }{
        .{ .arg = "120k", .tokens = 120_000 },
        .{ .arg = "1M", .tokens = 1_000_000 },
        .{ .arg = "90000 tokens", .tokens = 90_000 },
        .{ .arg = "90000tokens", .tokens = 90_000 },
    }) |case| {
        var result = try dispatch(ctx, .{ .kind = .autocompact, .arg = case.arg });
        defer result.deinit(std.testing.allocator);
        try std.testing.expect(!result.is_error);
        try std.testing.expectEqualDeep(tui_state.AutoCompactSetting{ .tokens = case.tokens }, state.autocompact);
    }
    var shown = try dispatch(ctx, .{ .kind = .autocompact });
    defer shown.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("autocompact: at 90000 tokens.", shown.output);

    var bare = try dispatch(ctx, .{ .kind = .autocompact, .arg = "60" });
    defer bare.deinit(std.testing.allocator);
    try std.testing.expectEqualDeep(tui_state.AutoCompactSetting{ .percent = 60 }, state.autocompact);

    for ([_][]const u8{ "0k", "k", "tokens", "0 tokens", "12q", "lots of tokens" }) |bad| {
        var result = try dispatch(ctx, .{ .kind = .autocompact, .arg = bad });
        defer result.deinit(std.testing.allocator);
        try std.testing.expect(result.is_error);
        try std.testing.expectEqualDeep(tui_state.AutoCompactSetting{ .percent = 60 }, state.autocompact);
    }
}

test "autocompact refuses a share that is not a percentage of the window" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };
    var seed = try dispatch(ctx, .{ .kind = .autocompact, .arg = "80%" });
    defer seed.deinit(std.testing.allocator);

    for ([_][]const u8{ "0", "0%", "101", "101%", "-10", "80%%", "8 0", "eighty", "", "8.5" }) |bad| {
        var result = try dispatch(ctx, .{ .kind = .autocompact, .arg = bad });
        defer result.deinit(std.testing.allocator);
        try std.testing.expect(result.is_error);
        try std.testing.expectEqualDeep(tui_state.AutoCompactSetting{ .percent = 80 }, state.autocompact);
    }
    for ([_][]const u8{ "1", "1%", "100", "100%" }) |good| {
        var result = try dispatch(ctx, .{ .kind = .autocompact, .arg = good });
        defer result.deinit(std.testing.allocator);
        try std.testing.expect(!result.is_error);
    }
}

test "zen toggles, takes on and off, and refuses anything else" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx: CommandContext = .{ .allocator = std.testing.allocator, .state = &state };
    try state.appendTranscript(.user, "earlier");

    var on = try dispatch(ctx, try parse("/zen"));
    defer on.deinit(std.testing.allocator);
    try std.testing.expect(state.zen.on);
    try std.testing.expectEqual(@as(usize, 1), state.zen.start_index);
    try std.testing.expectEqual(tui_state.ZenNote.enter, state.zen.note);

    var off = try dispatch(ctx, try parse("/zen off"));
    defer off.deinit(std.testing.allocator);
    try std.testing.expect(!state.zen.on);
    try std.testing.expectEqual(tui_state.ZenNote.none, state.zen.note);

    var explicit = try dispatch(ctx, try parse("/zen ON"));
    defer explicit.deinit(std.testing.allocator);
    try std.testing.expect(state.zen.on);

    var bad = try dispatch(ctx, try parse("/zen loud"));
    defer bad.deinit(std.testing.allocator);
    try std.testing.expect(bad.is_error);
    try std.testing.expectEqualStrings(zen_usage, bad.output);
    try std.testing.expect(state.zen.on);
}

test "status hands the report to the app" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .status });
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.show_status, result.action);
}

test "login command can target a provider directly" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .login, .arg = "openai-codex" });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(CommandAction.start_login_provider, result.action);
    try std.testing.expectEqualStrings("openai-codex", result.login_provider);
}

test "verbose sets every part at once or one part on its own, and refuses what it cannot read" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };

    var all = try dispatch(ctx, try parse("/verbose quiet"));
    defer all.deinit(std.testing.allocator);
    try std.testing.expectEqual(tui_state.Verbosity.all(.quiet), state.verbosity);

    var one = try dispatch(ctx, try parse("/verbose status verbose"));
    defer one.deinit(std.testing.allocator);
    try std.testing.expectEqual(tui_state.VerbosityLevel.verbose, state.verbosity.status);
    try std.testing.expectEqual(tui_state.VerbosityLevel.quiet, state.verbosity.tools);
    try std.testing.expectEqualStrings("verbosity: thinking quiet, tools quiet, output quiet, notices quiet, status verbose", one.output);

    inline for (.{ "/verbose loud", "/verbose status", "/verbose status loud", "/verbose quiet now", "/verbose tools quiet extra" }) |input| {
        var bad = try dispatch(ctx, try parse(input));
        defer bad.deinit(std.testing.allocator);
        try std.testing.expect(bad.is_error);
        try std.testing.expectEqualStrings(verbose_usage, bad.output);
    }
    try std.testing.expectEqual(tui_state.VerbosityLevel.verbose, state.verbosity.status);
}

test "redraw hands its work to the app and waits for a running turn" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    var idle = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, try parse("/redraw"));
    defer idle.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.redraw, idle.action);
    state.status.streaming = true;
    var busy = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, try parse("/redraw"));
    defer busy.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.none, busy.action);
    try std.testing.expect(busy.is_error);
}

test "model refresh and logout hand their work to the app" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    var refresh = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, try parse("/model refresh"));
    defer refresh.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.refresh_models, refresh.action);

    state.status.streaming = true;
    var running = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, try parse("/model refresh"));
    defer running.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.refresh_models, running.action);
    state.status.streaming = false;

    const logout = try parse("/logout opencode-go");
    try std.testing.expectEqual(CommandKind.logout, logout.kind);
    try std.testing.expectEqualStrings("opencode-go", logout.arg.?);
    var out = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, logout);
    defer out.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.logout_provider, out.action);

    var bare = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, try parse("/logout"));
    defer bare.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.none, bare.action);
    try std.testing.expectEqualStrings("usage: /logout <provider>", bare.output);

    state.status.streaming = true;
    var busy = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, logout);
    defer busy.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.none, busy.action);
    try std.testing.expect(busy.is_error);
}

test "resume opens the session picker when sessions exist" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try state.addSession("s1", "Saved");

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .@"resume" });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(CommandAction.open_session_picker, result.action);
}

test "sessions alias parses to resume" {
    const command = try parse("/sessions");
    try std.testing.expectEqual(CommandKind.@"resume", command.kind);
}

test "resume reports empty store" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .@"resume" });
    defer result.deinit(std.testing.allocator);

    try std.testing.expect(std.mem.indexOf(u8, result.output, "no saved sessions") != null);
}

test "permissions opens picker without argument" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .permissions });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(CommandAction.open_permission_picker, result.action);
}

test "permissions command switches runtime mode" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    var runtime = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();

    try std.testing.expectEqual(tui_runtime.PermissionMode.bypass, runtime.permissionMode());
    try std.testing.expectEqual(tui_runtime.PermissionMode.bypass, state.permission_mode);

    var bypass = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, .{ .kind = .permissions, .arg = "bypass" });
    defer bypass.deinit(std.testing.allocator);
    try std.testing.expectEqual(tui_runtime.PermissionMode.bypass, runtime.permissionMode());
    try std.testing.expectEqual(tui_runtime.PermissionMode.bypass, state.permission_mode);
    try std.testing.expect(std.mem.indexOf(u8, bypass.output, "permission mode set to bypass") != null);

    var ask = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, .{ .kind = .permissions, .arg = "ask" });
    defer ask.deinit(std.testing.allocator);
    try std.testing.expectEqual(tui_runtime.PermissionMode.ask, runtime.permissionMode());
    try std.testing.expectEqual(tui_runtime.PermissionMode.ask, state.permission_mode);
}

test "perm alias parses as permissions command" {
    const command = try parse("/perm ask");
    try std.testing.expectEqual(CommandKind.permissions, command.kind);
    try std.testing.expectEqualStrings("ask", command.arg.?);
}

test "rename hands its title to the app and refuses an empty one" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    const command = try parse("/rename  Resume freeze fix ");
    try std.testing.expectEqual(CommandKind.rename, command.kind);
    try std.testing.expectEqualStrings("Resume freeze fix", command.arg.?);
    var renamed = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, command);
    defer renamed.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.rename_session, renamed.action);
    try std.testing.expect(!renamed.is_error);

    var empty = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, try parse("/rename"));
    defer empty.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.none, empty.action);
    try std.testing.expect(empty.is_error);
    try std.testing.expectEqualStrings("usage: /rename <title>", empty.output);
}

test "runtime dependent commands dispatch to no-runtime errors" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };
    try std.testing.expectError(error.NoRuntimeConfigured, dispatch(ctx, .{ .kind = .model, .arg = "model-a" }));
}

test "compact takes its focus and hands a running turn's request to the app" {
    const command = try parse("/compact  the parser rewrite ");
    try std.testing.expectEqual(CommandKind.compact, command.kind);
    try std.testing.expectEqualStrings("the parser rewrite", command.arg.?);

    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state };

    var idle = try dispatch(ctx, command);
    defer idle.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.compact, idle.action);

    state.status.streaming = true;
    var busy = try dispatch(ctx, command);
    defer busy.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.compact_during_run, busy.action);

    state.status.compacting = true;
    var again = try dispatch(ctx, command);
    defer again.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.none, again.action);
    try std.testing.expect(std.mem.indexOf(u8, again.output, "Already compacting") != null);
}

test "abort during compaction cancels it without dropping queued drafts" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;
    state.status.compacting = true;
    try state.appendSteeredMessage("steered while compacting");
    try state.appendQueuedFollowUp("queued while compacting");

    var mock = MockAbortSession{};
    defer mock.deinit();
    var session = mock.session();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(@as(usize, 0), result.output.len);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
    try std.testing.expectEqual(@as(usize, 0), mock.clear_count);
    try std.testing.expectEqual(@as(usize, 1), state.pending_steers.items.len);
    try std.testing.expectEqual(@as(usize, 1), state.pending_follow_ups.items.len);
    try std.testing.expect(state.status.compacting);
    try std.testing.expect(!state.stream_aborted);
}

test "abort when idle reports idle" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("Nothing to abort — agent is idle.", result.output);
    try std.testing.expect(!state.status.streaming);
}

test "abort when streaming cancels session" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;

    var mock = MockAbortSession{};
    defer mock.deinit();
    var session = mock.session();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("Turn aborted.", result.output);
    try std.testing.expect(!state.status.streaming);
    try std.testing.expect(state.stream_aborted);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
}

test "abort when streaming holds queued messages to send once the run stops" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;
    try state.appendSteeredMessage("steer before abort");
    try state.appendQueuedFollowUp("follow-up before abort");

    var mock = MockAbortSession{ .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var session = mock.session();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("Turn aborted. Sending the 2 queued messages once it stops; press esc again to drop them.", result.output);
    try std.testing.expectEqual(@as(usize, 1), mock.clear_count);
    try std.testing.expectEqual(@as(usize, 0), state.pending_steers.items.len);
    try std.testing.expectEqual(@as(usize, 0), state.pending_follow_ups.items.len);
    try std.testing.expectEqual(@as(usize, 2), state.held_after_abort.items.len);
    try std.testing.expectEqualStrings("steer before abort", state.held_after_abort.items[0]);
    try std.testing.expectEqualStrings("follow-up before abort", state.held_after_abort.items[1]);
    try std.testing.expectEqual(@as(usize, 1), state.held_after_abort_echoed);
    try std.testing.expectEqual(@as(usize, 1), state.transcript.items.len);
    try std.testing.expectEqualStrings("steer before abort", state.transcript.items[0].text.items);

    var again = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer again.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Dropped the 2 queued messages; they will not be sent.", again.output);
    try std.testing.expectEqual(@as(usize, 0), state.held_after_abort.items.len);
}

test "abort does not hold a steer the run already consumed" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;
    try state.appendSteeredMessage("already folded into the run");
    try state.appendSteeredMessage("still waiting");

    var mock = MockAbortSession{ .steers_consumed = 1 };
    defer mock.deinit();
    var session = mock.session();
    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqual(@as(usize, 1), state.held_after_abort.items.len);
    try std.testing.expectEqualStrings("still waiting", state.held_after_abort.items[0]);
}

test "logout waits for a turn the status has not caught up with" {
    var runtime = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();
    runtime.stream_active = true;

    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    try std.testing.expect(!state.status.streaming);

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, try parse("/logout opencode-go"));
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.none, result.action);
    try std.testing.expect(result.is_error);

    var refresh = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, try parse("/model refresh"));
    defer refresh.deinit(std.testing.allocator);
    try std.testing.expectEqual(CommandAction.refresh_models, refresh.action);
}

test "abort cancels active turn before streaming status is set" {
    var runtime = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();
    runtime.stream_active = true;

    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("Turn aborted.", result.output);
    try std.testing.expect(!state.status.streaming);
    try std.testing.expect(state.stream_aborted);
    try std.testing.expect(runtime.cancelled.load(.acquire));
}

test "a second abort while the run winds down says it is stopping instead of aborting again" {
    var runtime = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();
    runtime.stream_active = true;

    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    const ctx = CommandContext{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime };

    var first = try dispatch(ctx, .{ .kind = .abort });
    defer first.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Turn aborted.", first.output);

    var second = try dispatch(ctx, .{ .kind = .abort });
    defer second.deinit(std.testing.allocator);
    try std.testing.expect(std.mem.startsWith(u8, second.output, "Still stopping"));
    try std.testing.expect(state.stream_aborted);
}

test "abort during approval clears approval state" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;
    state.mode = .approval;
    try state.approval.setPending(std.testing.allocator, "call-1", "edit_file", "edit_file", "{\"path\":\"README.md\"}");

    var mock = MockAbortSession{};
    defer mock.deinit();
    var session = mock.session();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer result.deinit(std.testing.allocator);

    try std.testing.expectEqualStrings("Turn aborted.", result.output);
    try std.testing.expectEqual(tui_state.AppMode.normal, state.mode);
    try std.testing.expectEqual(tui_state.ApprovalStatus.none, state.approval.status);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
}

test "double abort is harmless after first cancellation" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.status.streaming = true;

    var mock = MockAbortSession{};
    defer mock.deinit();
    var session = mock.session();

    var first = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer first.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Turn aborted.", first.output);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);

    var second = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .session = &session }, .{ .kind = .abort });
    defer second.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Nothing to abort — agent is idle.", second.output);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
}

const MockAbortSession = struct {
    queued_counts: tui_runtime.QueuedCounts = .{},
    cancel_count: usize = 0,
    clear_count: usize = 0,
    steers_consumed: u64 = 0,
    events: tui_runtime.TuiEventStream = undefined,
    events_initialized: bool = false,

    fn session(self: *MockAbortSession) tui_runtime.TuiSession {
        return .{
            .ctx = self,
            .ops = .{
                .start = mockStart,
                .resume_session = mockResumeSession,
                .cancel = mockCancel,
                .submit_turn = mockSubmitTurn,
                .steer = mockSteer,
                .clear_queued_messages = mockClearQueuedMessages,
                .queued_counts = mockQueuedCounts,
                .steers_consumed = mockSteersConsumed,
                .can_steer = mockCanSteer,
                .switch_model = mockSwitchModel,
                .current_model = mockCurrentModel,
                .decide_tool_approval = mockDecideToolApproval,
                .stream_events = mockStreamEvents,
            },
        };
    }

    fn ptr(ctx: ?*anyopaque) *MockAbortSession {
        return @ptrCast(@alignCast(ctx.?));
    }

    fn mockStart(ctx: ?*anyopaque) anyerror!void {
        _ = ctx;
    }

    fn mockResumeSession(ctx: ?*anyopaque) anyerror!void {
        _ = ctx;
    }

    fn mockCancel(ctx: ?*anyopaque) void {
        ptr(ctx).cancel_count += 1;
    }

    fn mockSubmitTurn(ctx: ?*anyopaque, text: []const u8) anyerror!void {
        _ = ctx;
        _ = text;
    }

    fn mockSteer(ctx: ?*anyopaque, text: []const u8) anyerror!void {
        _ = ctx;
        _ = text;
    }

    fn mockClearQueuedMessages(ctx: ?*anyopaque) void {
        ptr(ctx).clear_count += 1;
    }

    fn mockQueuedCounts(ctx: ?*anyopaque) tui_runtime.QueuedCounts {
        return ptr(ctx).queued_counts;
    }

    fn mockSteersConsumed(ctx: ?*anyopaque) u64 {
        return ptr(ctx).steers_consumed;
    }

    fn mockCanSteer(ctx: ?*anyopaque) bool {
        _ = ctx;
        return false;
    }

    fn mockSwitchModel(ctx: ?*anyopaque, model_id: []const u8) anyerror!void {
        _ = ctx;
        _ = model_id;
    }

    fn mockCurrentModel(ctx: ?*anyopaque) ?ai_types.Model {
        _ = ctx;
        return null;
    }

    fn mockDecideToolApproval(ctx: ?*anyopaque, tool_call_id: []const u8, decision: tui_runtime.ToolApprovalDecision) anyerror!void {
        _ = ctx;
        _ = tool_call_id;
        _ = decision;
    }

    fn eventStream(self: *MockAbortSession) *tui_runtime.TuiEventStream {
        if (!self.events_initialized) {
            self.events = tui_runtime.TuiEventStream.init(std.testing.allocator);
            self.events_initialized = true;
        }
        return &self.events;
    }

    fn mockStreamEvents(ctx: ?*anyopaque) *tui_runtime.TuiEventStream {
        return ptr(ctx).eventStream();
    }

    fn deinit(self: *MockAbortSession) void {
        if (self.events_initialized) self.events.deinit();
    }
};

test "think sets the thinking level in the state and the runtime" {
    const command = try parse("/think max");
    try std.testing.expectEqual(CommandKind.think, command.kind);
    try std.testing.expectEqualStrings("max", command.arg.?);

    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    var runtime = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    defer runtime.deinit();

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, command);
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("thinking level set to max", result.output);
    try std.testing.expectEqual(ai_types.ThinkingLevel.max, state.thinking_level);
    try std.testing.expectEqual(ai_types.ThinkingLevel.max, runtime.thinkingLevel());

    var off = try dispatch(.{ .allocator = std.testing.allocator, .state = &state, .runtime = &runtime }, .{ .kind = .think, .arg = "off" });
    defer off.deinit(std.testing.allocator);
    try std.testing.expectEqual(ai_types.ThinkingLevel.off, state.thinking_level);
    try std.testing.expectEqual(ai_types.ThinkingLevel.off, runtime.thinkingLevel());
}

test "think without a level shows the current one" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.thinking_level = .high;

    var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .think });
    defer result.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("thinking level: high", result.output);
    try std.testing.expectEqual(ai_types.ThinkingLevel.high, state.thinking_level);
}

test "think refuses a level the TUI does not offer and keeps the current one" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();
    state.thinking_level = .medium;

    for ([_][]const u8{ "minimal", "extreme" }) |level| {
        var result = try dispatch(.{ .allocator = std.testing.allocator, .state = &state }, .{ .kind = .think, .arg = level });
        defer result.deinit(std.testing.allocator);
        try std.testing.expect(result.is_error);
        try std.testing.expect(std.mem.startsWith(u8, result.output, "unknown thinking level: "));
        try std.testing.expectEqual(ai_types.ThinkingLevel.medium, state.thinking_level);
    }
}
