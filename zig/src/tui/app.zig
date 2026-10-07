const std = @import("std");
const compat = @import("compat");
const zz = @import("zigzag");
const ai_types = @import("ai_types");
const provider_catalog = @import("provider_catalog");

const anthropic_messages_base_url = provider_catalog.baseUrlOrCompileError("anthropic", "anthropic-messages", null);
const api_registry = @import("api_registry");
const register_builtins = @import("register_builtins");
const agent = @import("agent");
const event_stream = @import("event_stream");
const tui_runtime = @import("tui_runtime");
const tui_oap_execution = @import("tui/oap_execution");
const tui_auto_continue = @import("tui_auto_continue");
const tui_state = @import("tui_state");
const tui_commands = @import("tui_commands");
const tui_login = @import("tui_login");
const custom_providers = @import("custom_providers");
const auth_resolver = @import("auth_resolver");
const model_catalog = @import("model_catalog");
const tui_config = @import("tui_config");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");
const oauth_storage = @import("oauth/storage");
const session_store = @import("tui_session_store");
const tui_worktree = @import("tui_worktree");
const transcript_view = @import("tui_view_transcript");
const composer_view = @import("tui_view_composer");
const status_bar_view = @import("tui_view_status_bar");
const approval_view = @import("tui_view_approval");
const session_picker_view = @import("tui_view_session_picker");
const menu_picker_view = @import("tui_view_menu_picker");
const zen_view = @import("tui_view_zen");
const tui_render = @import("tui_render");
const permission = @import("permission");
const fixture_provider = @import("tui_fixture");
const OwnedSlice = @import("owned_slice").OwnedSlice;

extern "c" fn setenv(name: [*:0]const u8, value: [*:0]const u8, overwrite: c_int) c_int;
extern "c" fn unsetenv(name: [*:0]const u8) c_int;

pub const TuiRuntime = tui_runtime.TuiRuntime;
pub const TuiRuntimeOptions = tui_runtime.TuiRuntimeOptions;
pub const parseContextWindow = tui_runtime.parseContextWindow;

const max_session_event_jsonl_bytes = 8 * 1024 * 1024;
const max_session_event_payload_bytes = max_session_event_jsonl_bytes / 2;

fn isSecretLoginPrompt(message: []const u8) bool {
    return std.mem.indexOf(u8, message, "API key") != null or std.mem.indexOf(u8, message, "api key") != null;
}

pub const ApprovalWaiter = struct {
    allocator: std.mem.Allocator,
    mutex: std.atomic.Mutex = .unlocked,
    tool_call_id: []u8 = &.{},
    decision: ?tui_runtime.ToolApprovalDecision = null,
    shutting_down: bool = false,

    pub fn cancel(self: *ApprovalWaiter) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        self.shutting_down = true;
        self.decision = .reject;
    }

    pub fn rejectPending(self: *ApprovalWaiter) void {
        while (!self.mutex.tryLock()) std.atomic.spinLoopHint();
        defer self.mutex.unlock();
        if (self.tool_call_id.len > 0) {
            self.decision = .reject;
        }
    }

    pub fn deinit(self: *ApprovalWaiter) void {
        self.cancel();
        if (self.tool_call_id.len > 0) self.allocator.free(self.tool_call_id);
        self.* = undefined;
    }
};

fn loadRuntimeModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    return loadRuntimeModelsWithCatalog(allocator, model_catalog.loadProductionModels, true);
}

fn fixtureModels(allocator: std.mem.Allocator) ![]ai_types.Model {
    const models = try allocator.alloc(ai_types.Model, 1);
    errdefer allocator.free(models);
    models[0] = defaultModel();
    return models;
}

fn loadRuntimeModelsFresh(allocator: std.mem.Allocator) ![]ai_types.Model {
    return loadRuntimeModelsWithCatalog(allocator, model_catalog.refreshProductionModels, false);
}

fn loadRuntimeModelsFreshNoting(allocator: std.mem.Allocator, notes: *model_catalog.RefreshNotes) ![]ai_types.Model {
    return withDefaultModel(allocator, try model_catalog.refreshProductionModelsNoting(allocator, notes));
}

const ModelFetch = struct {
    thread: std.Thread,
    done: std.atomic.Value(bool) = .init(false),
    result: anyerror![]ai_types.Model = error.ModelFetchPending,
    notes: model_catalog.RefreshNotes = .init(fetch_allocator),

    const fetch_allocator = std.heap.smp_allocator;

    const Outcome = struct {
        result: anyerror![]ai_types.Model,
        notes: model_catalog.RefreshNotes,

        fn deinit(self: *Outcome) void {
            if (self.result) |models| release(models) else |_| {}
            self.notes.deinit();
        }
    };

    fn start() !*ModelFetch {
        const fetch = try fetch_allocator.create(ModelFetch);
        errdefer fetch_allocator.destroy(fetch);
        fetch.* = .{ .thread = undefined };
        fetch.thread = try std.Thread.spawn(.{}, work, .{fetch});
        return fetch;
    }

    fn work(self: *ModelFetch) void {
        self.result = loadRuntimeModelsFreshNoting(fetch_allocator, &self.notes);
        self.done.store(true, .release);
    }

    fn finish(self: *ModelFetch) Outcome {
        self.thread.join();
        const outcome: Outcome = .{ .result = self.result, .notes = self.notes };
        fetch_allocator.destroy(self);
        return outcome;
    }

    fn release(models: []ai_types.Model) void {
        model_catalog.deinitModels(fetch_allocator, models);
    }
};

fn loadRuntimeModelsWithCatalog(
    allocator: std.mem.Allocator,
    comptime loadCatalog: fn (std.mem.Allocator) anyerror![]ai_types.Model,
    comptime catch_catalog_errors: bool,
) ![]ai_types.Model {
    const catalog_models = loadCatalog(allocator) catch |err| if (catch_catalog_errors)
        try allocator.alloc(ai_types.Model, 0)
    else
        return err;
    return withDefaultModel(allocator, catalog_models);
}

fn withDefaultModel(allocator: std.mem.Allocator, catalog_models: []ai_types.Model) ![]ai_types.Model {
    var consumed: usize = 0;
    errdefer {
        for (catalog_models[consumed..]) |*model| model.deinit(allocator);
        allocator.free(catalog_models);
    }

    var models = std.ArrayList(ai_types.Model).empty;
    errdefer {
        if (models.items.len > 1) for (models.items[1..]) |*model| model.deinit(allocator);
        models.deinit(allocator);
    }
    try models.append(allocator, defaultModel());
    for (catalog_models) |model| {
        if (isDatedVariantOf(model.id, models.items[0].id)) {
            models.items[0] = foldCatalogLimits(models.items[0], model);
            var folded = model;
            folded.deinit(allocator);
            consumed += 1;
            continue;
        }
        try models.append(allocator, model);
        consumed += 1;
    }
    const result = try models.toOwnedSlice(allocator);
    allocator.free(catalog_models);
    return result;
}

fn foldCatalogLimits(base: ai_types.Model, catalog: ai_types.Model) ai_types.Model {
    var folded = base;
    folded.max_tokens = catalog.max_tokens;
    folded.context_window = catalog.context_window;
    folded.reasoning = catalog.reasoning;
    if (catalog.cost.input > 0) folded.cost = catalog.cost;
    return folded;
}

fn ownedTestModel(allocator: std.mem.Allocator, id: []const u8, max_tokens: u32, input_cost: f64) !ai_types.Model {
    var model = defaultModel();
    model.id = id;
    model.max_tokens = max_tokens;
    model.context_window = 1_000_000;
    model.cost = .{ .input = input_cost, .output = input_cost * 5, .cache_read = 0, .cache_write = 0 };
    return ai_types.cloneModel(allocator, model);
}

fn datedDefaultCatalog(allocator: std.mem.Allocator) anyerror![]ai_types.Model {
    const models = try allocator.alloc(ai_types.Model, 2);
    errdefer allocator.free(models);
    models[0] = try ownedTestModel(allocator, "claude-sonnet-5-5-20260929", 64_000, 0);
    errdefer models[0].deinit(allocator);
    models[1] = try ownedTestModel(allocator, "claude-opus-4-1", 32_000, 15.0);
    return models;
}

fn runtimeModelsFoldProbe(allocator: std.mem.Allocator) !void {
    const models = try loadRuntimeModelsWithCatalog(allocator, datedDefaultCatalog, false);
    defer model_catalog.deinitModels(allocator, models);
    try std.testing.expectEqual(@as(usize, 2), models.len);
    try std.testing.expectEqualStrings("claude-sonnet-5-5", models[0].id);
    try std.testing.expectEqualStrings("Claude Sonnet 5.5", models[0].name);
    try std.testing.expect(!models[0].is_owned);
    try std.testing.expectEqual(@as(u32, 64_000), models[0].max_tokens);
    try std.testing.expectEqual(@as(u32, 1_000_000), models[0].context_window);
    try std.testing.expectEqual(@as(f64, 2.0), models[0].cost.input);
    try std.testing.expectEqualStrings("claude-opus-4-1", models[1].id);
    try std.testing.expect(models[1].is_owned);
}

test "runtime models fold the catalog's default alias into the fallback and keep one owner per model" {
    try runtimeModelsFoldProbe(std.testing.allocator);
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, runtimeModelsFoldProbe, .{});
}

fn isDatedVariantOf(id: []const u8, base: []const u8) bool {
    if (std.mem.eql(u8, id, base)) return true;
    if (id.len != base.len + 9 or !std.mem.startsWith(u8, id, base) or id[base.len] != '-') return false;
    for (id[base.len + 1 ..]) |c| if (!std.ascii.isDigit(c)) return false;
    return true;
}

test "isDatedVariantOf recognises dated aliases of the default model" {
    try std.testing.expect(isDatedVariantOf("claude-sonnet-4-5-20250929", "claude-sonnet-4-5"));
    try std.testing.expect(isDatedVariantOf("claude-sonnet-4-5", "claude-sonnet-4-5"));
    try std.testing.expect(!isDatedVariantOf("claude-sonnet-4-5-fast", "claude-sonnet-4-5"));
    try std.testing.expect(!isDatedVariantOf("claude-opus-4-1-20250805", "claude-sonnet-4-5"));
}

test "App loginStatusFor reports stored, environment and expired credentials" {
    var storage = oauth_storage.AuthStorage{ .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(std.testing.allocator), .allocator = std.testing.allocator };
    defer storage.deinit();
    try storage.providers.put(try std.testing.allocator.dupe(u8, "kimi"), .{ .api_key = try std.testing.allocator.dupe(u8, "sk-kimi") });
    try storage.providers.put(try std.testing.allocator.dupe(u8, "anthropic"), .{ .oauth = .{
        .refresh = try std.testing.allocator.dupe(u8, "r"),
        .access = try std.testing.allocator.dupe(u8, "sk-ant-oat-x"),
        .expires = std.math.maxInt(i64),
    } });
    try storage.providers.put(try std.testing.allocator.dupe(u8, "openai-codex"), .{ .oauth = .{
        .refresh = try std.testing.allocator.dupe(u8, "r"),
        .access = try std.testing.allocator.dupe(u8, "old"),
        .expires = 1,
    } });

    try std.testing.expectEqual(App.LoginStatus.api_key, App.loginStatusFor(&storage, "kimi", false));
    try std.testing.expectEqual(App.LoginStatus.env_key, App.loginStatusFor(&storage, "kimi", true));
    try std.testing.expectEqual(App.LoginStatus.oauth, App.loginStatusFor(&storage, "anthropic", false));
    try std.testing.expectEqual(App.LoginStatus.env_key, App.loginStatusFor(null, "anthropic", true));
    try std.testing.expectEqual(App.LoginStatus.none, App.loginStatusFor(null, "kimi", false));
    try std.testing.expectEqual(App.LoginStatus.expired, App.loginStatusFor(&storage, "openai-codex", false));
    try std.testing.expectEqual(App.LoginStatus.env_key, App.loginStatusFor(&storage, "github-copilot", true));
    try std.testing.expectEqual(App.LoginStatus.none, App.loginStatusFor(&storage, "github-copilot", false));
    try std.testing.expect(std.mem.indexOf(u8, App.loginBadge(.oauth).?, "logged in") != null);
    try std.testing.expect(App.loginBadge(.none) == null);
}

test "App welcome banner neutralises control bytes in the working directory" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/tmp/evil\x1b[2J\x1b]0;pwned\x07dir");
    try app.appendWelcome();
    const entry = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.welcome, entry.kind);
    try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "\x1b") == null);
    try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "\x07") == null);
    try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "/tmp/evil?[2J?]0;pwned?dir") != null);
}

test "App tool call arguments move the path row to the agent's directory" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/work/session");
    try app.refreshCwdDisplay();
    try std.testing.expectEqualStrings("/work/session", app.state.cwdRowPath());

    try app.applyRuntimeEvent(.{ .tool_execution_start = .{
        .tool_call_id = .initBorrowed("call-1"),
        .tool_name = .initBorrowed("shell_execute"),
        .args_json = .initBorrowed("{\"workspace_root\":\"/work/other\",\"command\":\"ls\"}"),
    } });

    try std.testing.expectEqualStrings("/work/other", app.state.cwdRowPath());
    try std.testing.expect(app.state.agentCwdIsOutsideSession());
}

test "App tool call without a workspace root leaves the path row alone" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/work/session");
    try app.refreshCwdDisplay();

    try app.applyRuntimeEvent(.{ .tool_execution_start = .{
        .tool_call_id = .initBorrowed("call-1"),
        .tool_name = .initBorrowed("workspace_list"),
        .args_json = .initBorrowed("{\"query\":\"src\"}"),
    } });

    try std.testing.expectEqualStrings("/work/session", app.state.cwdRowPath());
    try std.testing.expect(!app.state.agentCwdIsOutsideSession());
}

test "App resume leaves the path row at the session root" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/work/session");
    try app.refreshCwdDisplay();
    try app.applyRuntimeEvent(.{ .tool_execution_start = .{
        .tool_call_id = .initBorrowed("call-1"),
        .tool_name = .initBorrowed("shell_execute"),
        .args_json = .initBorrowed("{\"workspace_root\":\"/work/elsewhere\"}"),
    } });
    try std.testing.expectEqualStrings("/work/elsewhere", app.state.cwdRowPath());

    app.state.resetReplayState();
    app.state.setFollowingAgentCwd(false);
    defer app.state.setFollowingAgentCwd(true);
    try app.applyRuntimeEvent(.{ .tool_execution_start = .{
        .tool_call_id = .initBorrowed("call-2"),
        .tool_name = .initBorrowed("shell_execute"),
        .args_json = .initBorrowed("{\"workspace_root\":\"/work/replayed\"}"),
    } });

    try std.testing.expectEqualStrings("/work/session", app.state.cwdRowPath());
}

test "App tool call keeps the path row muted inside the session root" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/work/session");
    try app.refreshCwdDisplay();

    try app.applyRuntimeEvent(.{ .tool_execution_start = .{
        .tool_call_id = .initBorrowed("call-1"),
        .tool_name = .initBorrowed("shell_execute"),
        .args_json = .initBorrowed("{\"workspace_root\":\"/work/session/sub\"}"),
    } });

    try std.testing.expect(!app.state.agentCwdIsOutsideSession());
}

test "App cwd display neutralises control bytes in the working directory" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/tmp/evil\x1b[2J\x1b]0;pwned\x07dir");
    try app.refreshCwdDisplay();
    try std.testing.expect(std.mem.indexOf(u8, app.state.cwd_display, "\x1b") == null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.cwd_display, "\x07") == null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.cwd_display, "/tmp/evil?[2J?]0;pwned?dir") != null);
}

fn branchRepoBase(allocator: std.mem.Allocator, tmp: *const std.testing.TmpDir) ![]u8 {
    const cwd = try currentPathOwned(allocator);
    defer allocator.free(cwd);
    return std.fs.path.join(allocator, &.{ cwd, ".zig-cache", "tmp", &tmp.sub_path });
}

const BranchRepo = struct {
    tmp: std.testing.TmpDir,
    repo: []u8,
    git: []u8,

    fn init(allocator: std.mem.Allocator) !BranchRepo {
        var self: BranchRepo = .{ .tmp = std.testing.tmpDir(.{}), .repo = &.{}, .git = &.{} };
        errdefer self.tmp.cleanup();
        const base = try branchRepoBase(allocator, &self.tmp);
        self.repo = std.fs.path.join(allocator, &.{ base, "work" }) catch |err| {
            allocator.free(base);
            return err;
        };
        allocator.free(base);
        errdefer allocator.free(self.repo);
        self.git = try std.fs.path.join(allocator, &.{ self.repo, ".git" });
        errdefer allocator.free(self.git);
        try compat.fs.createDir(compat.fs.getCwd(), self.git);
        return self;
    }

    fn deinit(self: *BranchRepo, allocator: std.mem.Allocator) void {
        allocator.free(self.git);
        allocator.free(self.repo);
        self.tmp.cleanup();
    }

    fn writeHead(self: BranchRepo, allocator: std.mem.Allocator, contents: []const u8) !void {
        const head = try std.fs.path.join(allocator, &.{ self.git, "HEAD" });
        defer allocator.free(head);
        try compat.fs.writeFile(compat.fs.getCwd(), head, contents);
    }

    fn makeDotGitAFile(self: BranchRepo, allocator: std.mem.Allocator, contents: []const u8) !void {
        const head = try std.fs.path.join(allocator, &.{ self.git, "HEAD" });
        defer allocator.free(head);
        compat.fs.removeFile(head);
        compat.fs.removeDir(self.git);
        try compat.fs.writeFile(compat.fs.getCwd(), self.git, contents);
    }
};

test "App git branch reads the branch name from the repository HEAD" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/tui-status\n");

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("tui-status", app.state.git_branch);
}

test "App git branch shows the abbreviated commit id on a detached HEAD" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0\n");

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("a1b2c3d", app.state.git_branch);
}

test "App git branch follows a worktree gitdir file to the real HEAD" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    const base = try branchRepoBase(std.testing.allocator, &repo.tmp);
    defer std.testing.allocator.free(base);
    const real_git = try std.fs.path.join(std.testing.allocator, &.{ base, "real-git" });
    defer std.testing.allocator.free(real_git);
    try compat.fs.createDir(compat.fs.getCwd(), real_git);
    const real_head = try std.fs.path.join(std.testing.allocator, &.{ real_git, "HEAD" });
    defer std.testing.allocator.free(real_head);
    try compat.fs.writeFile(compat.fs.getCwd(), real_head, "ref: refs/heads/worktree-branch\n");
    const pointer = try std.fmt.allocPrint(std.testing.allocator, "gitdir: {s}\n", .{real_git});
    defer std.testing.allocator.free(pointer);
    try repo.makeDotGitAFile(std.testing.allocator, pointer);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("worktree-branch", app.state.git_branch);
}

test "App git branch follows a relative gitdir pointer against the working directory" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    const real_git = try std.fs.path.join(std.testing.allocator, &.{ repo.repo, "..", "modules", "sub" });
    defer std.testing.allocator.free(real_git);
    try compat.fs.createDir(compat.fs.getCwd(), real_git);
    const real_head = try std.fs.path.join(std.testing.allocator, &.{ real_git, "HEAD" });
    defer std.testing.allocator.free(real_head);
    try compat.fs.writeFile(compat.fs.getCwd(), real_head, "ref: refs/heads/submodule\n");
    const pointer = try std.fmt.allocPrint(std.testing.allocator, "gitdir: ../modules/sub\n", .{});
    defer std.testing.allocator.free(pointer);
    try repo.makeDotGitAFile(std.testing.allocator, pointer);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("submodule", app.state.git_branch);
}

test "App git branch walks up to the enclosing repository" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/enclosing\n");
    const nested = try std.fs.path.join(std.testing.allocator, &.{ repo.repo, "src", "deep" });
    defer std.testing.allocator.free(nested);
    try compat.fs.createDir(compat.fs.getCwd(), nested);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, nested);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("enclosing", app.state.git_branch);
}

test "App git branch stops at a repository it cannot inspect" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/outer\n");
    const broken = try std.fs.path.join(std.testing.allocator, &.{ repo.repo, "locked" });
    defer std.testing.allocator.free(broken);
    try compat.fs.createDir(compat.fs.getCwd(), broken);
    const broken_git = try std.fs.path.join(std.testing.allocator, &.{ broken, ".git" });
    defer std.testing.allocator.free(broken_git);
    try compat.fs.symLink(compat.fs.getCwd(), "/nonexistent-oap-git-target", broken_git);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, broken);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("", app.state.git_branch);
}

test "App git branch stops at a repository whose gitdir pointer is unparseable" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/outer\n");
    try repo.makeDotGitAFile(std.testing.allocator, "not a pointer at all");

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("", app.state.git_branch);
}

test "App git branch stops at a repository whose HEAD is unreadable" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/outer\n");
    const inner = try std.fs.path.join(std.testing.allocator, &.{ repo.repo, "inner" });
    defer std.testing.allocator.free(inner);
    const inner_git = try std.fs.path.join(std.testing.allocator, &.{ inner, ".git" });
    defer std.testing.allocator.free(inner_git);
    try compat.fs.createDir(compat.fs.getCwd(), inner_git);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, inner);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("", app.state.git_branch);
}

test "App git branch takes the nearest repository rather than an enclosing one" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/outer\n");
    const inner = try std.fs.path.join(std.testing.allocator, &.{ repo.repo, "inner" });
    defer std.testing.allocator.free(inner);
    const inner_git = try std.fs.path.join(std.testing.allocator, &.{ inner, ".git" });
    defer std.testing.allocator.free(inner_git);
    try compat.fs.createDir(compat.fs.getCwd(), inner_git);
    const inner_head = try std.fs.path.join(std.testing.allocator, &.{ inner_git, "HEAD" });
    defer std.testing.allocator.free(inner_head);
    try compat.fs.writeFile(compat.fs.getCwd(), inner_head, "ref: refs/heads/inner\n");

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, inner);
    try app.refreshCwdDisplay();

    try std.testing.expectEqualStrings("inner", app.state.git_branch);
}

test "App git branch neutralises control bytes read from HEAD" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/evil\x1b[2J\x07\n");

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();

    try std.testing.expect(std.mem.indexOf(u8, app.state.git_branch, "\x1b") == null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.git_branch, "\x07") == null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.git_branch, "evil?[2J?") != null);
}

test "App slow tick re-reads the branch after the working directory changes" {
    var repo = try BranchRepo.init(std.testing.allocator);
    defer repo.deinit(std.testing.allocator);
    try repo.writeHead(std.testing.allocator, "ref: refs/heads/one\n");

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, repo.repo);
    try app.refreshCwdDisplay();
    try std.testing.expectEqualStrings("one", app.state.git_branch);

    try repo.writeHead(std.testing.allocator, "ref: refs/heads/two\n");
    try app.refreshBranchOnSlowTick();
    try std.testing.expectEqualStrings("two", app.state.git_branch);
}

const filesystem_root = "/";

test "App git branch is empty when no ancestor holds a repository" {
    const label = try gitHeadLabel(std.testing.allocator, filesystem_root);
    defer if (label) |value| std.testing.allocator.free(value);
    try std.testing.expect(label == null);
}

fn refreshGitBranchProbe(allocator: std.mem.Allocator) !void {
    const label = try gitHeadLabel(allocator, filesystem_root);
    if (label) |value| allocator.free(value);
}

test "gitHeadLabel survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, refreshGitBranchProbe, .{});
}

test "collapseHome shortens the home directory only on a path component boundary" {
    const collapsed = try collapseHome(std.testing.allocator, "/Users/lsm/work/repo", "/Users/lsm");
    defer std.testing.allocator.free(collapsed);
    try std.testing.expectEqualStrings("~/work/repo", collapsed);

    const home_itself = try collapseHome(std.testing.allocator, "/Users/lsm", "/Users/lsm");
    defer std.testing.allocator.free(home_itself);
    try std.testing.expectEqualStrings("~", home_itself);

    const sibling = try collapseHome(std.testing.allocator, "/Users/lsmith/work", "/Users/lsm");
    defer std.testing.allocator.free(sibling);
    try std.testing.expectEqualStrings("/Users/lsmith/work", sibling);
}

fn collapseHomeProbe(allocator: std.mem.Allocator) !void {
    const collapsed = try collapseHome(allocator, "/Users/lsm/work/repo", "/Users/lsm");
    allocator.free(collapsed);
}

test "collapseHome survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, collapseHomeProbe, .{});
}

test "a transcript verbosity change reprints from the first entry, and a status-only change does not" {
    var env = try TempHome.init("home-verbosity-redraw");
    defer env.deinit();
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.inline_history_flushed = 3;
    app.inline_flushed_rows = 2;

    try app.submit("/verbose status verbose");
    try std.testing.expect(!app.pending_clear_screen);
    try std.testing.expectEqual(@as(usize, 3), app.inline_history_flushed);

    try app.submit("/verbose quiet");
    if (App.terminalKeepsScrollback()) {
        try std.testing.expect(!app.pending_clear_screen);
        try std.testing.expectEqualStrings("earlier rows keep their old verbosity; run /redraw to reprint them", app.state.transcript.items[app.state.transcript.items.len - 1].text.items);
    } else {
        try std.testing.expect(app.pending_clear_screen);
        try std.testing.expectEqual(@as(usize, 0), app.inline_history_flushed);
        try std.testing.expectEqual(@as(usize, 0), app.inline_flushed_rows);
    }

    app.pending_clear_screen = false;
    try app.cycleVerbosity();
    try std.testing.expectEqual(tui_state.Verbosity.all(.normal), app.state.verbosity);
}

test "Context requestClearScreen discards history queued before the request" {
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try tctx.ctx.printAbove("stale row");
    tctx.ctx.requestClearScreen();
    try std.testing.expect(!tctx.ctx.hasPendingAbove());
    try tctx.ctx.printAbove("transcript cleared");
    const above = try tctx.ctx.takeAbove(std.testing.allocator);
    defer std.testing.allocator.free(above);
    try std.testing.expectEqualStrings("transcript cleared\n", above);
}

test "the login list names the host an override sends a row to" {
    try provider_catalog.blankEnvironment(std.testing.allocator);
    defer compat.clearTestEnv();
    auth_resolver.test_override_config = "{\"overrides\":[{\"id\":\"deepseek\",\"base_url\":\"https://proxy.example/deepseek\"}]}";
    defer auth_resolver.test_override_config = null;
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.state.picker_kind = .login;
    app.refreshLoginStatus();

    const overridden = app.pickerItem(App.loginProviderIndex("deepseek").?, null);
    try std.testing.expectEqualStrings("deepseek via proxy.example", overridden.detail.?);
    const plain = app.pickerItem(App.loginProviderIndex("openrouter").?, null);
    try std.testing.expectEqualStrings("openrouter", plain.detail.?);

    auth_resolver.test_override_config = null;
    app.refreshLoginStatus();
    try std.testing.expectEqualStrings("deepseek", app.pickerItem(App.loginProviderIndex("deepseek").?, null).detail.?);
}

test "TuiModel login picker shows which providers are logged in" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 24;
    model.app.?.login_status[App.loginProviderCatalogIndex("anthropic").?] = .oauth;
    model.app.?.login_status[App.loginProviderCatalogIndex("kimi").?] = .api_key;
    model.app.?.state.mode = .picker;
    model.app.?.state.picker_kind = .login;

    const frame = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, frame, "Login provider") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "logged in") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "api key") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "expired") == null);
}

pub const ProductionRuntime = struct {
    allocator: std.mem.Allocator,
    registry: api_registry.ApiRegistry,
    bridge: agent.InProcessProviderProtocolBridge,
    permission_engine: permission.PermissionEngine,
    models: []ai_types.Model,
    initial_model: ?SavedModelRef = null,
    context_window: ?u32 = null,
    mode_settings: tui_config.ModeSettings = .{},

    pub const InitOptions = struct {
        fixture: bool = false,
    };

    pub fn init(allocator: std.mem.Allocator, init_options: InitOptions) !ProductionRuntime {
        var registry = api_registry.ApiRegistry.init(allocator);
        errdefer registry.deinit();
        try register_builtins.registerBuiltInApiProviders(&registry);

        const workspace_root = try currentPathOwned(allocator);
        defer allocator.free(workspace_root);
        var permission_engine = permission.PermissionEngine.init(allocator, .{ .workspace_root = workspace_root }) catch
            permission.PermissionEngine.initEmpty(allocator, .{ .workspace_root = workspace_root }) catch
            @panic("OOM initializing permission engine");
        errdefer permission_engine.deinit();

        const models = if (init_options.fixture)
            try fixtureModels(allocator)
        else
            try loadRuntimeModels(allocator);
        errdefer model_catalog.deinitModels(allocator, models);

        var saved_config: ?tui_config.Config = null;
        var maybe_store: ?tui_config.Store = tui_config.Store.initDefault(allocator) catch |err| switch (err) {
            error.HomeNotFound => null,
            else => return err,
        };
        if (maybe_store) |*store| {
            defer store.deinit();
            saved_config = store.loadIfExists() catch |err| switch (err) {
                error.OutOfMemory => return error.OutOfMemory,
                else => null,
            };
        }
        errdefer if (saved_config) |*cfg| cfg.deinit(allocator);

        var mode_settings: tui_config.ModeSettings = .{};
        var initial_model: ?SavedModelRef = null;
        var saved_context_window: ?u32 = null;
        if (saved_config) |cfg| {
            saved_context_window = cfg.mode.context_window;
            mode_settings = cfg.mode;
            if (cfg.model.len > 0) {
                initial_model = SavedModelRef{
                    .id = try allocator.dupe(u8, cfg.model),
                    .provider = try allocator.dupe(u8, cfg.provider),
                    .api = try allocator.dupe(u8, cfg.api),
                };
            }
        }
        errdefer if (initial_model) |*model| model.deinit(allocator);

        const runtime = ProductionRuntime{
            .allocator = allocator,
            .registry = registry,
            .bridge = undefined,
            .permission_engine = permission_engine,
            .models = models,
            .initial_model = initial_model,
            .context_window = saved_context_window,
            .mode_settings = mode_settings,
        };
        initial_model = null;
        if (saved_config) |*cfg| cfg.deinit(allocator);
        saved_config = null;
        return runtime;
    }

    pub fn initBridge(self: *ProductionRuntime) void {
        self.bridge = agent.InProcessProviderProtocolBridge.init(&self.registry);
    }

    pub fn options(self: *ProductionRuntime) tui_runtime.TuiRuntimeOptions {
        return .{
            .protocol = (&self.bridge).protocolClient(),
            .models = self.models,
            .initial_model = if (self.initial_model) |model| .{
                .id = model.id,
                .provider = model.provider,
                .api = model.api,
            } else null,
            .permission_engine = &self.permission_engine,
            .workspace_root = self.permission_engine.workspace_root,
            .context_window = self.context_window,
            .output = agentOutput(self.mode_settings.output),
            .run_async = true,
            .compact_output = self.mode_settings.compact_output,
            .auto_worktree = self.mode_settings.auto_worktree,
            .generate_titles = true,
        };
    }

    pub fn deinit(self: *ProductionRuntime) void {
        model_catalog.deinitModels(self.allocator, self.models);
        if (self.initial_model) |*model| model.deinit(self.allocator);
        self.permission_engine.deinit();
        self.registry.deinit();
        self.* = undefined;
    }
};

const SavedModelRef = struct {
    id: []u8,
    provider: []u8,
    api: []u8,

    fn deinit(self: *SavedModelRef, allocator: std.mem.Allocator) void {
        allocator.free(self.id);
        allocator.free(self.provider);
        allocator.free(self.api);
        self.* = undefined;
    }
};

pub const fixture_env_var = "OAPX_TUI_FIXTURE";

const fixture_step_separator = '|';
const fixture_step_escape = '\\';
const fixture_tool_arg_json = "{}";

pub const FixtureStepError = error{
    EmptyFixtureStep,
    UnknownFixtureStepPrefix,
    OutOfMemory,
};

pub const FixtureRuntime = struct {
    allocator: std.mem.Allocator,
    text: []u8,
    steps: std.ArrayList(fixture_provider.ResponseStep),
    tool_specs: std.ArrayList([]fixture_provider.ToolCallSpec),
    payloads: std.ArrayList([]u8),
    provider: fixture_provider.MockProvider,

    pub fn fromEnv(allocator: std.mem.Allocator, env: *const std.process.Environ.Map) !?*FixtureRuntime {
        return fromValue(allocator, env.get(fixture_env_var));
    }

    fn fromValue(allocator: std.mem.Allocator, value: ?[]const u8) !?*FixtureRuntime {
        const supplied = value orelse return null;
        if (supplied.len == 0) return null;
        const text = try allocator.dupe(u8, supplied);
        errdefer allocator.free(text);
        const self = try allocator.create(FixtureRuntime);
        errdefer allocator.destroy(self);
        self.* = .{
            .allocator = allocator,
            .text = text,
            .steps = .empty,
            .tool_specs = .empty,
            .payloads = .empty,
            .provider = undefined,
        };
        errdefer self.deinitSteps();
        if (firstSegmentIsScenarioStep(text)) {
            try self.appendScenarioSteps(text);
        } else {
            try self.steps.append(allocator, .{ .text = text });
        }
        self.provider = fixture_provider.MockProvider.init(.{ .steps = self.steps.items, .repeat_last = true });
        return self;
    }

    fn firstSegmentIsScenarioStep(text: []const u8) bool {
        const first = text[0..unescapedSeparatorIndex(text)];
        return scenarioStepKind(first) != null;
    }

    fn scenarioStepKind(segment: []const u8) ?enum { text, tool, hold, err } {
        if (std.mem.startsWith(u8, segment, "text:")) return .text;
        if (std.mem.startsWith(u8, segment, "tool:")) return .tool;
        if (std.mem.eql(u8, segment, "hold")) return .hold;
        if (std.mem.startsWith(u8, segment, "error:")) return .err;
        return null;
    }

    fn unescapedSeparatorIndex(text: []const u8) usize {
        var i: usize = 0;
        while (i < text.len) : (i += 1) {
            if (text[i] == fixture_step_escape and i + 1 < text.len) {
                i += 1;
                continue;
            }
            if (text[i] == fixture_step_separator) return i;
        }
        return text.len;
    }

    fn unescapeStep(self: *FixtureRuntime, raw: []const u8) FixtureStepError![]const u8 {
        if (std.mem.indexOfScalar(u8, raw, fixture_step_escape) == null) return raw;
        const out = try self.allocator.alloc(u8, raw.len);
        self.payloads.append(self.allocator, out) catch {
            self.allocator.free(out);
            return error.OutOfMemory;
        };
        var len: usize = 0;
        var i: usize = 0;
        while (i < raw.len) {
            if (raw[i] == fixture_step_escape and i + 1 < raw.len and
                (raw[i + 1] == fixture_step_separator or raw[i + 1] == fixture_step_escape))
            {
                i += 1;
            }
            out[len] = raw[i];
            len += 1;
            i += 1;
        }
        return out[0..len];
    }

    fn appendScenarioSteps(self: *FixtureRuntime, text: []const u8) FixtureStepError!void {
        var rest = text;
        while (true) {
            const segment_end = unescapedSeparatorIndex(rest);
            const had_separator = segment_end < rest.len;
            const segment = try self.unescapeStep(rest[0..segment_end]);
            if (segment.len == 0) return error.EmptyFixtureStep;
            const kind = scenarioStepKind(segment) orelse return error.UnknownFixtureStepPrefix;
            switch (kind) {
                .text => {
                    const body = segment["text:".len..];
                    if (body.len == 0) return error.EmptyFixtureStep;
                    try self.steps.append(self.allocator, .{ .text = body });
                },
                .tool => {
                    const step_rest = segment["tool:".len..];
                    var name = step_rest;
                    var args_json: []const u8 = fixture_tool_arg_json;
                    if (std.mem.indexOfScalar(u8, step_rest, '#')) |hash| {
                        name = step_rest[0..hash];
                        args_json = step_rest[hash + 1 ..];
                    }
                    if (name.len == 0 or args_json.len == 0) return error.EmptyFixtureStep;
                    const spec = try self.allocator.alloc(fixture_provider.ToolCallSpec, 1);
                    spec[0] = .{ .id = "fixture-tool-call", .name = name, .arguments_json = args_json };
                    self.tool_specs.append(self.allocator, spec) catch {
                        self.allocator.free(spec);
                        return error.OutOfMemory;
                    };
                    try self.steps.append(self.allocator, .{ .tool_calls = spec });
                },
                .hold => try self.steps.append(self.allocator, .{ .wait_for_cancel = {} }),
                .err => {
                    const body = segment["error:".len..];
                    if (body.len == 0) return error.EmptyFixtureStep;
                    try self.steps.append(self.allocator, .{ .provider_error = body });
                },
            }
            if (!had_separator) return;
            rest = rest[segment_end + 1 ..];
            if (rest.len == 0) return error.EmptyFixtureStep;
        }
    }

    fn deinitSteps(self: *FixtureRuntime) void {
        for (self.tool_specs.items) |spec| self.allocator.free(spec);
        self.tool_specs.deinit(self.allocator);
        for (self.payloads.items) |payload| self.allocator.free(payload);
        self.payloads.deinit(self.allocator);
        self.steps.deinit(self.allocator);
    }

    pub fn deinit(self: *FixtureRuntime) void {
        self.deinitSteps();
        self.allocator.free(self.text);
        self.allocator.destroy(self);
    }
};

pub const App = struct {
    allocator: std.mem.Allocator,
    state: tui_state.AppState,
    runtime: ?*tui_runtime.TuiRuntime = null,
    session: ?tui_runtime.TuiSession = null,
    approval_waiter: ?*ApprovalWaiter = null,
    login: ?*tui_login.LoginSession = null,
    store: ?session_store.Store = null,
    mode_settings: tui_config.ModeSettings = .{},
    worktree_job: ?*tui_worktree.CreateJob = null,
    worktree_management_job: ?*tui_worktree.ManagementJob = null,
    pending_resume_id: []u8 = &.{},
    pending_resume_path: []u8 = &.{},
    resume_without_worktree_id: []u8 = &.{},
    pending_delete_id: []u8 = &.{},
    pending_delete_path: []u8 = &.{},
    worktree_attempted: bool = false,
    held_user_message: []u8 = &.{},
    queued_worktree_messages: std.ArrayList([]u8) = .empty,
    session_turns: usize = 0,
    pending_worktree_info: ?tui_worktree.WorktreeInfo = null,
    pending_worktree_session_id: []u8 = &.{},
    session_id: []u8 = &.{},
    working_dir: []u8 = &.{},
    launch_dir: []u8 = &.{},
    last_view_height: usize = 8,
    inline_history_flushed: usize = 0,
    inline_flushed_rows: usize = 0,
    login_status: [provider_catalog.all.len]LoginStatus = [_]LoginStatus{.none} ** provider_catalog.all.len,
    login_override_details: [provider_catalog.all.len]?[]u8 = [_]?[]u8{null} ** provider_catalog.all.len,
    pending_session_reset: bool = false,
    deferred_commands: std.ArrayList([]u8) = .empty,
    pending_compaction: ?[]u8 = null,
    pending_models: ?[]ai_types.Model = null,
    model_fetch: ?*ModelFetch = null,
    model_refetch: bool = false,
    quarantine_events: bool = false,
    quarantine_generation: u32 = 0,
    quarantine_buffer: std.ArrayList(tui_runtime.TuiEvent) = .empty,
    pending_clipboard: ?[]u8 = null,
    interrupt_armed_tick: ?u64 = null,
    pending_clear_screen: bool = false,
    slash_index: usize = 0,
    slash_index_query: u64 = 0,
    compaction_transcripts: std.ArrayList([]u8) = .empty,
    rate_model: []u8 = &.{},
    rate_provider: []u8 = &.{},
    written_model: []u8 = &.{},
    written_provider: []u8 = &.{},
    session_written: bool = false,
    session_created_at: i64 = 0,
    compaction_offset: u64 = 0,
    pending_thinking: std.ArrayList(u8) = .empty,
    pending_after_compaction: ?[]u8 = null,
    pending_after_compaction_echo: ?[]u8 = null,
    runtime_echo_suppressed: []u8 = &.{},
    compaction_just_ended: ?bool = null,
    session_title: []u8 = &.{},
    session_title_generated: bool = false,
    session_title_renamed: bool = false,
    first_user_text: []u8 = &.{},
    title_session_id: []u8 = &.{},
    run_error_text: []u8 = &.{},
    auto_continue: tui_auto_continue.Streak = .{},
    replaying_history: bool = false,
    branch_dir: []u8 = &.{},

    pub fn init(allocator: std.mem.Allocator, options: tui_runtime.TuiRuntimeOptions) !App {
        var runtime_options = options;
        const approval_waiter = try allocator.create(ApprovalWaiter);
        errdefer allocator.destroy(approval_waiter);
        approval_waiter.* = .{ .allocator = allocator };
        runtime_options.tool_approval_ctx = approval_waiter;
        runtime_options.tool_approval_callback = approvalCallback;
        const runtime_ptr = try allocator.create(tui_runtime.TuiRuntime);
        runtime_ptr.* = tui_runtime.TuiRuntime.init(allocator, runtime_options) catch |err| {
            allocator.destroy(runtime_ptr);
            return err;
        };
        var app = App{
            .allocator = allocator,
            .mode_settings = .{ .compact_output = options.compact_output, .auto_worktree = options.auto_worktree },
            .state = tui_state.AppState.init(allocator),
            .runtime = runtime_ptr,
            .approval_waiter = approval_waiter,
            .quarantine_buffer = std.ArrayList(tui_runtime.TuiEvent).empty,
        };
        errdefer app.deinit();
        app.session = app.runtime.?.createSession();
        if (options.remote != null and !app.runtime.?.recordsFromEndpoint()) try app.state.appendTranscript(.system, over_oap_notice);
        app.state.permission_mode = app.runtime.?.permissionMode();
        app.state.thinking_level = app.runtime.?.thinkingLevel();
        try app.state.setRegisteredTools(app.runtime.?.availableTools());
        if (app.runtime.?.currentModel()) |model| {
            try app.state.status.setModelWithContext(allocator, model.id, model.provider, model.context_window);
            app.state.telemetry.context_window = model.context_window;
        }
        app.store = session_store.Store.initDefault(allocator) catch null;
        try app.ensureSessionId();
        app.working_dir = currentPathOwned(allocator) catch try allocator.dupe(u8, "");
        app.launch_dir = try allocator.dupe(u8, app.working_dir);
        try app.refreshCwdDisplay();
        app.loadSessions() catch |err| try app.recordError(@errorName(err));
        return app;
    }

    pub fn initWithoutRuntime(allocator: std.mem.Allocator) App {
        return .{
            .allocator = allocator,
            .state = tui_state.AppState.init(allocator),
            .quarantine_buffer = std.ArrayList(tui_runtime.TuiEvent).empty,
        };
    }

    pub fn deinit(self: *App) void {
        self.saveEndpointRecords();
        self.forgetLoginOverrides();
        for (self.deferred_commands.items) |text| self.allocator.free(text);
        self.deferred_commands.deinit(self.allocator);
        if (self.pending_compaction) |focus| self.allocator.free(focus);
        self.pending_compaction = null;
        if (self.model_fetch) |fetch| {
            if (fetch.done.load(.acquire)) {
                var outcome = fetch.finish();
                outcome.deinit();
            } else {
                fetch.thread.detach();
            }
            self.model_fetch = null;
        }
        if (self.pending_models) |models| {
            model_catalog.deinitModels(self.allocator, models);
            self.pending_models = null;
        }
        if (self.login) |session| {
            session.deinit();
            self.login = null;
        }
        if (self.worktree_job) |job| {
            job.wait();
            if (job.poll()) |outcome_value| {
                var outcome = outcome_value;
                defer outcome.deinit(self.allocator);
                switch (outcome) {
                    .created => |created| {
                        var info = created.info;
                        self.persistOrDiscardCreatedWorktree(&info);
                    },
                    else => {},
                }
            }
            job.deinit();
            self.worktree_job = null;
        }
        if (self.worktree_management_job) |job| {
            job.wait();
            if (job.poll()) |value| {
                var outcome = value;
                outcome.deinit(self.allocator);
            }
            job.deinit();
            self.worktree_management_job = null;
        }
        if (self.pending_worktree_info) |pending| {
            var info = pending;
            self.pending_worktree_info = null;
            self.persistOrDiscardCreatedWorktree(&info);
            info.deinit(self.allocator);
        }
        if (self.pending_worktree_session_id.len > 0) self.allocator.free(self.pending_worktree_session_id);
        if (self.pending_resume_id.len > 0) self.allocator.free(self.pending_resume_id);
        if (self.pending_resume_path.len > 0) self.allocator.free(self.pending_resume_path);
        if (self.resume_without_worktree_id.len > 0) self.allocator.free(self.resume_without_worktree_id);
        if (self.pending_delete_id.len > 0) self.allocator.free(self.pending_delete_id);
        if (self.pending_delete_path.len > 0) self.allocator.free(self.pending_delete_path);
        if (self.held_user_message.len > 0) self.allocator.free(self.held_user_message);
        for (self.queued_worktree_messages.items) |message| self.allocator.free(message);
        self.queued_worktree_messages.deinit(self.allocator);
        if (self.approval_waiter) |waiter| waiter.cancel();
        if (self.runtime) |runtime| {
            runtime.deinit();
            self.allocator.destroy(runtime);
        }
        if (self.approval_waiter) |waiter| {
            waiter.deinit();
            self.allocator.destroy(waiter);
        }
        if (self.store) |*store| store.deinit();
        if (self.pending_clipboard) |c| self.allocator.free(c);
        if (self.session_id.len > 0) self.allocator.free(self.session_id);
        if (self.working_dir.len > 0) self.allocator.free(self.working_dir);
        if (self.launch_dir.len > 0) self.allocator.free(self.launch_dir);
        for (self.quarantine_buffer.items) |*event| event.deinit(self.allocator);
        self.quarantine_buffer.deinit(self.allocator);
        self.clearCompactionTranscripts();
        self.compaction_transcripts.deinit(self.allocator);
        if (self.written_model.len > 0) self.allocator.free(self.written_model);
        if (self.written_provider.len > 0) self.allocator.free(self.written_provider);
        self.pending_thinking.deinit(self.allocator);
        if (self.pending_after_compaction) |pending| self.allocator.free(pending);
        if (self.pending_after_compaction_echo) |echo| self.allocator.free(echo);
        if (self.runtime_echo_suppressed.len > 0) self.allocator.free(self.runtime_echo_suppressed);
        if (self.rate_model.len > 0) self.allocator.free(self.rate_model);
        if (self.rate_provider.len > 0) self.allocator.free(self.rate_provider);
        if (self.session_title.len > 0) self.allocator.free(self.session_title);
        if (self.first_user_text.len > 0) self.allocator.free(self.first_user_text);
        if (self.title_session_id.len > 0) self.allocator.free(self.title_session_id);
        if (self.run_error_text.len > 0) self.allocator.free(self.run_error_text);
        if (self.branch_dir.len > 0) self.allocator.free(self.branch_dir);
        self.state.deinit();
        self.* = undefined;
    }

    fn clearCompactionTranscripts(self: *App) void {
        for (self.compaction_transcripts.items) |path| self.allocator.free(path);
        self.compaction_transcripts.clearRetainingCapacity();
    }

    fn recordCompactionTranscript(self: *App, path: []const u8) !void {
        if (path.len == 0) return;
        const owned = try self.allocator.dupe(u8, path);
        errdefer self.allocator.free(owned);
        try self.compaction_transcripts.append(self.allocator, owned);
    }

    fn startCompaction(self: *App, focus: []const u8) !void {
        var session = &(self.session orelse return error.NoRuntimeConfigured);
        if (self.runtime) |runtime| if (runtime.remote != null) {
            self.state.stream_aborted = false;
            session.compact(.{ .focus = focus }) catch |err| switch (err) {
                error.UnavailableOverOap => try self.state.appendTranscript(.@"error", over_oap_compaction_refusal),
                error.NothingToCompact => try self.state.appendTranscript(.system, "Nothing to compact yet."),
                error.RunInProgress => try self.state.appendTranscript(.@"error", "A run is still open; compact once it ends."),
                else => return err,
            };
            return;
        };
        const history = session.history();
        if (history.len == 0 or agent.compaction.isCompacted(history)) {
            try self.state.appendTranscript(.system, "Nothing to compact yet.");
            return;
        }
        try self.ensureSessionId();
        var transcripts: std.ArrayList([]const u8) = .empty;
        defer transcripts.deinit(self.allocator);
        for (self.compaction_transcripts.items) |path| try transcripts.append(self.allocator, path);
        var saved: ?[]u8 = null;
        defer if (saved) |path| self.allocator.free(path);
        if (self.store) |store| {
            saved = try store.saveTranscript(self.session_id, self.compaction_transcripts.items.len + 1, history);
            try transcripts.append(self.allocator, saved.?);
        }
        self.state.stream_aborted = false;
        session.compact(.{ .focus = focus, .transcripts = transcripts.items }) catch |err| switch (err) {
            error.NothingToCompact => try self.state.appendTranscript(.system, "Nothing to compact yet."),
            else => return err,
        };
    }

    fn rememberRunError(self: *App, message: []const u8) !void {
        if (std.mem.eql(u8, self.run_error_text, message)) return;
        const owned = try self.allocator.dupe(u8, message);
        if (self.run_error_text.len > 0) self.allocator.free(self.run_error_text);
        self.run_error_text = owned;
    }

    fn forgetRunError(self: *App) void {
        if (self.run_error_text.len == 0) return;
        self.allocator.free(self.run_error_text);
        self.run_error_text = &.{};
    }

    fn noteTerminalEvent(self: *App, event: tui_runtime.TuiEvent) !bool {
        switch (event) {
            .agent_end => |payload| return payload.reason == .completed,
            .compaction_end => |payload| {
                if (payload.outcome == .completed) try self.recordCompactionTranscript(payload.transcript.slice());
                if (payload.in_run) return false;
                self.compaction_just_ended = payload.outcome == .completed;
                return true;
            },
            else => return false,
        }
    }

    fn ensureSessionId(self: *App) !void {
        if (self.session_id.len > 0) return;
        self.session_id = generateSessionId(self.allocator) catch try self.allocator.dupe(u8, "default");
        try self.state.status.setSessionId(self.allocator, self.session_id);
        try self.giveRuntimeSessionId();
    }

    fn giveRuntimeSessionId(self: *App) !void {
        const runtime = self.runtime orelse return;
        try runtime.setSessionId(self.session_id);
    }

    pub fn loadSessions(self: *App) !void {
        const store = self.store orelse return;
        var metas = try store.list();
        for (self.state.sessions.items) |*s| s.deinit(self.allocator);
        self.state.sessions.clearRetainingCapacity();
        defer {
            for (metas.items) |*meta| meta.deinit(self.allocator);
            metas.deinit(self.allocator);
        }
        std.mem.sort(session_store.SessionMetadata, metas.items, {}, newerSessionFirst);
        for (metas.items) |meta| {
            const label = try formatSessionLabel(self.allocator, meta, utcOffsetSeconds(@divFloor(meta.last_active, 1000)));
            defer self.allocator.free(label);
            try self.state.addSession(meta.session_id, label);
        }
    }

    fn deleteSelectedSession(self: *App) !void {
        const store = self.store orelse return error.NoStoreConfigured;
        if (self.state.session_index >= self.state.sessions.items.len) return;
        const id = self.state.sessions.items[self.state.session_index].id;
        if (std.mem.eql(u8, id, self.session_id)) {
            try self.state.appendTranscript(.system, "Cannot delete the active session; resume another session first.");
            return;
        }
        if (self.worktree_job != null) {
            try self.state.appendTranscript(.system, "Wait for worktree setup to finish before deleting a session.");
            return;
        }
        if (self.worktree_management_job != null) {
            try self.state.appendTranscript(.system, "Wait for worktree setup to finish before deleting a session.");
            return;
        }
        if (try tui_worktree.readSidecar(self.allocator, store.base_dir, id)) |info_value| {
            var info = info_value;
            defer info.deinit(self.allocator);
            const job = try tui_worktree.ManagementJob.start(self.allocator, tui_worktree.processRunner(), &info, .remove);
            errdefer job.deinit();
            const pending_id = try self.allocator.dupe(u8, id);
            errdefer self.allocator.free(pending_id);
            const pending_path = try self.allocator.dupe(u8, info.path);
            errdefer self.allocator.free(pending_path);
            try self.state.appendNotice("Checking and removing the session worktree…");
            self.clearPendingDelete();
            self.worktree_management_job = job;
            self.pending_delete_id = pending_id;
            self.pending_delete_path = pending_path;
            return;
        }
        try self.finishDeleteSession(id);
    }

    fn forceDeletePendingSession(self: *App) !void {
        if (self.worktree_management_job != null or self.worktree_job != null) {
            try self.state.appendTranscript(.system, "Wait for worktree setup to finish before force removing a session.");
            self.clearPendingDelete();
            return;
        }
        if (self.pending_delete_id.len == 0) return;
        const id = self.pending_delete_id;
        self.pending_delete_id = &.{};
        const path = self.pending_delete_path;
        self.pending_delete_path = &.{};
        defer if (path.len > 0) self.allocator.free(path);
        var id_owned = true;
        defer if (id_owned) self.allocator.free(id);
        const store = self.store orelse return error.NoStoreConfigured;
        const info_value = (try tui_worktree.readSidecar(self.allocator, store.base_dir, id)) orelse {
            id_owned = false;
            defer self.allocator.free(id);
            return self.finishDeleteSession(id);
        };
        var info = info_value;
        defer info.deinit(self.allocator);
        const job = try tui_worktree.ManagementJob.start(self.allocator, tui_worktree.processRunner(), &info, .remove_force);
        errdefer job.deinit();
        const pending_id = try self.allocator.dupe(u8, id);
        errdefer self.allocator.free(pending_id);
        const pending_path = try self.allocator.dupe(u8, info.path);
        errdefer self.allocator.free(pending_path);
        try self.state.appendNotice("Force removing the session worktree…");
        id_owned = false;
        self.allocator.free(id);
        self.pending_delete_id = pending_id;
        self.pending_delete_path = pending_path;
        self.worktree_management_job = job;
    }

    fn raiseForceDeletePrompt(self: *App, question: []const u8) !void {
        const label = try std.fmt.allocPrint(self.allocator, "{s} for {s}?", .{ question, self.pendingDeleteLabel() });
        defer self.allocator.free(label);
        if (self.state.mode != .session_picker) {
            try self.state.appendTranscript(.system, label);
            try self.state.appendTranscript(.system, "Delete it again from the session picker to force remove its worktree.");
            self.clearPendingDelete();
            try self.deliverHeldWorktreeMessages();
            return;
        }
        const line = try std.fmt.allocPrint(self.allocator, "{s} Press y to force, n or Esc to keep the worktree.", .{label});
        defer self.allocator.free(line);
        try self.state.appendTranscript(.system, line);
        self.pinSessionPickerToPendingDelete();
        self.state.confirm_session_force_delete = true;
        try self.deliverHeldWorktreeMessages();
    }

    fn pinSessionPickerToPendingDelete(self: *App) void {
        const id = self.pending_delete_id;
        for (self.state.sessions.items, 0..) |entry, index| {
            if (!std.mem.eql(u8, entry.id, id)) continue;
            self.state.session_index = index;
            TuiModel.ensureSessionSelectionVisible(self);
            return;
        }
    }

    fn pendingDeleteLabel(self: *App) []const u8 {
        const id = self.pending_delete_id;
        for (self.state.sessions.items) |entry| {
            if (std.mem.eql(u8, entry.id, id)) return entry.label;
        }
        return id;
    }

    fn clearPendingDelete(self: *App) void {
        if (self.pending_delete_id.len > 0) self.allocator.free(self.pending_delete_id);
        if (self.pending_delete_path.len > 0) self.allocator.free(self.pending_delete_path);
        self.pending_delete_id = &.{};
        self.pending_delete_path = &.{};
    }

    fn cancelForceDeletePrompt(self: *App) !void {
        if (self.state.confirm_session_force_delete) {
            self.state.confirm_session_force_delete = false;
            self.clearPendingDelete();
        }
        try self.deliverHeldWorktreeMessages();
    }

    fn recordWorktreeSidecar(self: *App, info: *const tui_worktree.WorktreeInfo) !void {
        const store = self.store orelse return;
        if ((store.conversationBytes(self.session_id) catch 0) > 0) {
            try tui_worktree.writeSidecar(self.allocator, store.base_dir, self.session_id, info);
            return;
        }
        const cloned = try tui_worktree.cloneInfo(self.allocator, info);
        errdefer {
            var undo = cloned;
            undo.deinit(self.allocator);
        }
        const session_id = try self.allocator.dupe(u8, self.session_id);
        self.discardPendingWorktreeSidecar();
        self.pending_worktree_info = cloned;
        self.pending_worktree_session_id = session_id;
    }

    fn flushPendingWorktreeSidecar(self: *App) void {
        if (self.pending_worktree_info == null) return;
        const store = self.store orelse return;
        if (!std.mem.eql(u8, self.pending_worktree_session_id, self.session_id)) {
            var stale = self.pending_worktree_info.?;
            self.pending_worktree_info = null;
            self.freePendingWorktreeSessionId();
            if (tui_worktree.remove(self.allocator, tui_worktree.processRunner(), &stale)) |message| {
                if (message) |text| self.allocator.free(text);
            } else |_| {}
            stale.deinit(self.allocator);
            return;
        }
        if ((store.conversationBytes(self.session_id) catch 0) == 0) return;
        var info = self.pending_worktree_info.?;
        tui_worktree.writeSidecar(self.allocator, store.base_dir, self.session_id, &info) catch {};
        self.pending_worktree_info = null;
        self.freePendingWorktreeSessionId();
        info.deinit(self.allocator);
    }

    fn discardPendingWorktreeSidecar(self: *App) void {
        const info = self.pending_worktree_info orelse return;
        self.pending_worktree_info = null;
        self.freePendingWorktreeSessionId();
        var owned = info;
        self.persistOrDiscardCreatedWorktree(&owned);
        owned.deinit(self.allocator);
    }

    fn freePendingWorktreeSessionId(self: *App) void {
        if (self.pending_worktree_session_id.len > 0) self.allocator.free(self.pending_worktree_session_id);
        self.pending_worktree_session_id = &.{};
    }

    fn persistOrDiscardCreatedWorktree(self: *App, info: *const tui_worktree.WorktreeInfo) void {
        const store = self.store orelse return;
        const recorded = (store.conversationBytes(self.session_id) catch 0) > 0;
        if (recorded) {
            tui_worktree.writeSidecar(self.allocator, store.base_dir, self.session_id, info) catch {};
            return;
        }
        if (tui_worktree.remove(self.allocator, tui_worktree.processRunner(), info)) |message| {
            if (message) |text| self.allocator.free(text);
        } else |_| {}
    }

    fn finishDeleteSession(self: *App, id: []const u8) !void {
        const store = self.store orelse return error.NoStoreConfigured;
        try store.deleteSession(id);
        try self.loadSessions();
        if (self.state.session_index >= self.state.sessions.items.len and self.state.session_index > 0) self.state.session_index -= 1;
    }

    fn adoptResumeRoot(self: *App, runtime: *tui_runtime.TuiRuntime, root: []const u8) !void {
        try runtime.setWorkspaceRoot(root);
        try replaceOwnedString(self.allocator, &self.working_dir, root);
        try self.refreshCwdDisplay();
    }

    pub fn resumeSelectedSession(self: *App) !void {
        if (self.worktree_job != null or self.worktree_management_job != null) {
            try self.state.appendTranscript(.system, "Wait for worktree setup to finish before resuming another session.");
            return;
        }
        if (self.state.status.streaming) {
            self.state.mode = .normal;
            try self.state.appendTranscript(.system, "Cannot resume a session while a turn is running; wait for it to finish or abort it.");
            return;
        }
        self.discardPendingWorktreeSidecar();
        const store = self.store orelse return error.NoStoreConfigured;
        try self.dropPendingAfterCompaction("the session was resumed before the compaction finished");
        self.dropHeldCompaction();
        self.state.clearHeldAfterAbort();
        if (self.state.session_index >= self.state.sessions.items.len) return;
        const selected = self.state.sessions.items[self.state.session_index];
        const runtime = if (self.runtime) |r| r else return error.NoRuntimeConfigured;
        const id = selected.id;
        var resume_root: ?[]u8 = null;
        defer if (resume_root) |held| self.allocator.free(held);
        if (try tui_worktree.readSidecar(self.allocator, store.base_dir, id)) |info_value| {
            var info = info_value;
            defer info.deinit(self.allocator);
            self.worktree_attempted = true;
            const fallback_without_worktree = self.resume_without_worktree_id.len > 0 and std.mem.eql(u8, self.resume_without_worktree_id, id);
            if (fallback_without_worktree) {
                self.allocator.free(self.resume_without_worktree_id);
                self.resume_without_worktree_id = &.{};
            }
            if (!tui_worktree.pathExists(info.path) and !fallback_without_worktree) {
                const job = try tui_worktree.ManagementJob.start(self.allocator, tui_worktree.processRunner(), &info, .reattach);
                errdefer job.deinit();
                const pending_id = try self.allocator.dupe(u8, id);
                errdefer self.allocator.free(pending_id);
                const pending_path = try self.allocator.dupe(u8, info.path);
                errdefer self.allocator.free(pending_path);
                try self.state.appendNotice("Reattaching this session's Git worktree…");
                self.worktree_management_job = job;
                self.pending_resume_id = pending_id;
                self.pending_resume_path = pending_path;
                return;
            }
            resume_root = if (tui_worktree.pathExists(info.path))
                try info.workingDir(self.allocator)
            else root: {
                if (info.prefix.len > 0) {
                    const prefixed = try std.fs.path.join(self.allocator, &.{ info.repo_root, std.mem.trimEnd(u8, info.prefix, &.{std.fs.path.sep}) });
                    if (tui_worktree.pathExists(prefixed)) break :root prefixed;
                    self.allocator.free(prefixed);
                }
                if (tui_worktree.pathExists(info.repo_root)) break :root try self.allocator.dupe(u8, info.repo_root);
                break :root try self.allocator.dupe(u8, self.launch_dir);
            };
        } else {
            self.worktree_attempted = false;
            if (self.launch_dir.len > 0) resume_root = try self.allocator.dupe(u8, self.launch_dir);
        }
        const over_oap = runtime.remote != null;
        if (!over_oap) if (resume_root) |root| try self.adoptResumeRoot(runtime, root);
        var loaded = try store.resumeSession(id, runtime, if (over_oap) resume_root else null);
        defer loaded.deinit(self.allocator);
        if (over_oap) if (resume_root) |root| {
            try replaceOwnedString(self.allocator, &self.working_dir, root);
            try self.refreshCwdDisplay();
        };
        const new_session_id = try self.allocator.dupe(u8, loaded.metadata.session_id);
        self.discardPendingEvents();
        self.pending_session_reset = false;
        self.quarantine_events = false;
        for (self.quarantine_buffer.items) |*buf_ev| {
            var mutable = buf_ev.*;
            mutable.deinit(self.allocator);
        }
        self.quarantine_buffer.clearRetainingCapacity();
        self.state.resetReplayState();
        self.inline_history_flushed = 0;
        self.inline_flushed_rows = 0;
        if (self.session_id.len > 0) self.allocator.free(self.session_id);
        self.session_id = new_session_id;
        try self.giveRuntimeSessionId();
        if (loaded.metadata.thinking_level) |level| {
            runtime.setThinkingLevel(level) catch {};
            self.state.thinking_level = runtime.thinkingLevel();
        }
        try self.restoreCompactionTranscripts(store, &loaded);
        try self.state.status.setSessionId(self.allocator, self.session_id);
        if (runtime.currentModel()) |model| {
            try self.state.status.setModelWithContext(self.allocator, model.id, model.provider, model.context_window);
            self.state.telemetry.context_window = model.context_window;
        } else {
            try self.state.status.setModelWithContext(self.allocator, loaded.metadata.model, loaded.metadata.provider, 0);
        }
        defer if (loaded.model_unavailable) self.noteUnavailableResumeModel(loaded.metadata.provider, loaded.metadata.model, runtime.currentModel());
        self.replaying_history = true;
        self.state.setFollowingAgentCwd(false);
        defer {
            self.replaying_history = false;
            self.state.setFollowingAgentCwd(true);
        }
        for (loaded.events.items) |*event| {
            try self.applyRuntimeEvent(event.*);
        }
        self.state.telemetry.rate = .{};
        self.discardReplayedError();
        self.state.zen.start_index = self.state.transcript.items.len;
        try self.state.finalizeInterruptedTools();
        self.state.retireToolOccurrences();
        if (self.session) |*session| session.clearQueuedMessages();
        self.refreshQueuedCounts();
        self.state.status.streaming = false;
        self.state.status.compacting = false;
        self.state.confirm_session_delete = false;
        if (self.state.confirm_session_force_delete) {
            self.state.confirm_session_force_delete = false;
            self.clearPendingDelete();
        }
        self.state.mode = .normal;
        self.session_turns = if (loaded.events.items.len > 0) 1 else 0;
        try self.adoptLoadedSession(loaded.metadata);
        self.saveSessionIndex(store);
    }

    fn noteUnavailableResumeModel(self: *App, provider: []const u8, model_id: []const u8, current: ?ai_types.Model) void {
        const msg = if (current) |model|
            std.fmt.allocPrint(self.allocator, "{s}/{s} is not available, so this session continues on {s}/{s}; pick another with /model", .{ provider, model_id, model.provider, model.id }) catch return
        else
            std.fmt.allocPrint(self.allocator, "{s}/{s} is not available; pick a model with /model", .{ provider, model_id }) catch return;
        defer self.allocator.free(msg);
        self.state.appendTranscript(.system, msg) catch {};
    }

    fn loginDiscoveryAvailable(id: []const u8) bool {
        return model_catalog.supportsCatalogModelDiscovery(id);
    }

    fn loginProviderLabel(row: provider_catalog.Provider) []const u8 {
        return row.display_name orelse row.id;
    }

    fn supportsLogin(row: provider_catalog.Provider) bool {
        for (row.auth) |kind| {
            if (kind == .api_key or kind == .oauth) return true;
        }
        return false;
    }

    fn loginProviderCount() usize {
        var count: usize = 0;
        for (provider_catalog.all) |row| {
            if (supportsLogin(row)) count += 1;
        }
        return count;
    }

    fn loginProviderAt(index: usize) ?provider_catalog.Provider {
        var visible_index: usize = 0;
        for (0..2) |availability_pass| {
            for (provider_catalog.all) |row| {
                if (!supportsLogin(row)) continue;
                if (loginDiscoveryAvailable(row.id) != (availability_pass == 0)) continue;
                if (visible_index == index) return row;
                visible_index += 1;
            }
        }
        return null;
    }

    fn loginProviderCatalogIndex(provider_id: []const u8) ?usize {
        for (provider_catalog.all, 0..) |row, index| {
            if (std.mem.eql(u8, row.id, provider_id)) return index;
        }
        return null;
    }

    pub const LoginStatus = enum { none, api_key, env_key, oauth, expired };

    pub fn loginStatusFor(storage: ?*const oauth_storage.AuthStorage, provider_id: []const u8, env_key_present: bool) LoginStatus {
        if (env_key_present) return .env_key;
        if (storage) |stored| {
            if (stored.providers.get(provider_id)) |auth| {
                return switch (auth) {
                    .api_key => .api_key,
                    .oauth => if (stored.configuredCredentialsExpired(provider_id)) .expired else .oauth,
                };
            }
        }
        return .none;
    }

    pub fn loginBadge(status: LoginStatus) ?[]const u8 {
        return switch (status) {
            .none => null,
            .api_key => tui_theme.glyph.check ++ " api key",
            .env_key => tui_theme.glyph.check ++ " env key",
            .oauth => tui_theme.glyph.check ++ " logged in",
            .expired => "expired · login again",
        };
    }

    fn refreshLoginStatus(self: *App) void {
        var loaded: ?oauth_storage.AuthStorage = oauth_storage.AuthStorage.loadDefaultStoredOnly(self.allocator) catch null;
        defer if (loaded) |*storage| storage.deinit();
        const storage: ?*const oauth_storage.AuthStorage = if (loaded) |*stored| stored else null;
        for (provider_catalog.all, 0..) |provider, i| {
            var env_present = false;
            for (provider.credential_env) |name| {
                if (compat.getEnvVarOwned(self.allocator, name)) |value| {
                    env_present = env_present or value.len > 0;
                    self.allocator.free(value);
                } else |_| {}
            }
            self.login_status[i] = loginStatusFor(storage, provider.id, env_present);
        }
        self.refreshLoginOverrides(storage) catch self.forgetLoginOverrides();
    }

    fn refreshLoginOverrides(self: *App, storage: ?*const oauth_storage.AuthStorage) !void {
        self.forgetLoginOverrides();
        var config = try auth_resolver.loadOverrides(self.allocator);
        defer config.deinit(self.allocator);
        for (provider_catalog.all, 0..) |provider, i| {
            const host = try auth_resolver.overrideHost(self.allocator, config.overrides, provider.id, storage) orelse continue;
            defer self.allocator.free(host);
            self.login_override_details[i] = try std.fmt.allocPrint(self.allocator, "{s} via {s}", .{ provider.id, host });
        }
    }

    fn forgetLoginOverrides(self: *App) void {
        for (&self.login_override_details) |*detail| {
            if (detail.*) |text| self.allocator.free(text);
            detail.* = null;
        }
    }

    const permission_modes = [_]tui_runtime.PermissionMode{ .bypass, .ask };

    fn startCatalogLogin(self: *App, provider_id: []const u8) !*tui_login.LoginSession {
        if (std.mem.eql(u8, provider_id, "anthropic")) return tui_login.LoginSession.start(self.allocator, .anthropic);
        if (std.mem.eql(u8, provider_id, "github-copilot")) return tui_login.LoginSession.start(self.allocator, .github_copilot);
        if (std.mem.eql(u8, provider_id, "openai-codex")) return tui_login.LoginSession.start(self.allocator, .openai_codex);
        if (std.mem.eql(u8, provider_id, "kimi")) return tui_login.LoginSession.start(self.allocator, .kimi);
        return tui_login.LoginSession.startApiKey(self.allocator, provider_id);
    }

    fn startCatalogLoginProvider(self: *App, provider_id: []const u8) !void {
        const row = provider_catalog.provider(provider_id) orelse return error.UnknownLoginProvider;
        for (row.auth) |kind| {
            if (kind == .oauth) return error.UnsupportedOAuthProvider;
        }
        try self.startCustomLogin(provider_id);
    }

    fn loginProviderIndex(provider_id: []const u8) ?usize {
        for (0..loginProviderCount()) |index| {
            const row = loginProviderAt(index) orelse continue;
            if (std.mem.eql(u8, provider_id, row.id)) return index;
            if (std.mem.eql(u8, row.id, "openai-codex") and (std.mem.eql(u8, provider_id, "codex") or std.mem.eql(u8, provider_id, "openai"))) return index;
            if (std.mem.eql(u8, row.id, "github-copilot") and std.mem.eql(u8, provider_id, "github")) return index;
            if (std.mem.eql(u8, row.id, "kimi") and std.mem.eql(u8, provider_id, "moonshot")) return index;
        }
        return null;
    }

    fn openPicker(self: *App, kind: tui_state.PickerKind) void {
        self.state.picker_kind = kind;
        self.state.clearPickerFilter();
        self.state.menu_index = 0;
        self.state.menu_scroll = 0;
        switch (kind) {
            .model => if (self.runtime) |runtime| {
                if (runtime.currentModel()) |active| {
                    for (runtime.availableModels(), 0..) |model, i| {
                        if (std.mem.eql(u8, model.id, active.id)) {
                            self.state.menu_index = i;
                            break;
                        }
                    }
                }
            },
            .permission => {
                if (self.runtime) |runtime| self.state.permission_mode = runtime.permissionMode();
                for (permission_modes, 0..) |mode, i| {
                    if (mode == self.state.permission_mode) {
                        self.state.menu_index = i;
                        break;
                    }
                }
            },
            .login => self.refreshLoginStatus(),
            .settings => {},
        }
        self.state.mode = .picker;
        self.ensureMenuSelectionVisible();
    }

    fn pickerSourceCount(self: *const App) usize {
        return switch (self.state.picker_kind) {
            .model => if (self.runtime) |runtime| runtime.availableModels().len else 0,
            .login => loginProviderCount(),
            .permission => permission_modes.len,
            .settings => 2,
        };
    }

    fn pickerItem(self: *const App, index: usize, current: ?ai_types.Model) menu_picker_view.Item {
        return switch (self.state.picker_kind) {
            .model => if (self.runtime) |runtime| model_item: {
                const model = runtime.availableModels()[index];
                const is_current = if (current) |active| std.mem.eql(u8, active.id, model.id) else false;
                break :model_item .{ .label = model.id, .detail = model.provider, .badge = if (is_current) tui_theme.glyph.system ++ " current" else null };
            } else .{ .label = "" },
            .login => if (loginProviderAt(index)) |row| .{
                .label = loginProviderLabel(row),
                .detail = if (!loginDiscoveryAvailable(row.id)) "models unavailable" else self.login_override_details[loginProviderCatalogIndex(row.id).?] orelse row.id,
                .badge = if (loginDiscoveryAvailable(row.id)) loginBadge(self.login_status[loginProviderCatalogIndex(row.id).?]) else "unavailable",
            } else .{ .label = "" },
            .permission => .{ .label = @tagName(permission_modes[index]), .detail = TuiModel.permissionModeDetail(permission_modes[index]) },
            .settings => .{ .label = if (index == 0) "Compact output" else "Automatic worktrees", .detail = if (index == 0) "Reduce tool output in the transcript" else "Create an isolated Git worktree for each session", .badge = if ((if (index == 0) self.mode_settings.compact_output else self.mode_settings.auto_worktree)) "on" else "off" },
        };
    }

    fn pickerMatches(self: *const App, index: usize) bool {
        const item = self.pickerItem(index, null);
        return filterMatches(self.state.pickerFilter(), item.label, item.detail orelse "");
    }

    fn pickerMatchCount(self: *const App) usize {
        var count: usize = 0;
        for (0..self.pickerSourceCount()) |i| {
            if (self.pickerMatches(i)) count += 1;
        }
        return count;
    }

    fn pickerSourceIndex(self: *const App, position: usize) ?usize {
        var seen: usize = 0;
        for (0..self.pickerSourceCount()) |i| {
            if (!self.pickerMatches(i)) continue;
            if (seen == position) return i;
            seen += 1;
        }
        return null;
    }

    fn applySelectedModel(self: *App) !void {
        const runtime = self.runtime orelse return error.NoRuntimeConfigured;
        const models = runtime.availableModels();
        if (models.len == 0) {
            self.state.mode = .normal;
            return;
        }
        const index = self.pickerSourceIndex(self.state.menu_index) orelse return;
        const model = models[index];
        if (self.state.status.streaming) {
            self.state.mode = .normal;
            const target = try runtime.requestModelSwitchAt(index);
            const msg = try std.fmt.allocPrint(self.allocator, "switching to {s}/{s} {s}", .{ target.provider, target.id, switchTiming(runtime) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.system, msg);
            return;
        }
        if (self.session) |*session| {
            try session.switchModelExact(model);
        } else {
            try runtime.switchModelExact(model);
        }
        if (runtime.currentModel()) |m| {
            try self.state.status.setModelWithContext(self.allocator, m.id, m.provider, m.context_window);
            self.state.telemetry.context_window = m.context_window;
        }
        self.persistCurrentModel();
        self.state.mode = .normal;
        const msg = try std.fmt.allocPrint(self.allocator, "model switched to {s} ({s})", .{ model.id, model.provider });
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn startLoginProviderIndex(self: *App, idx: usize) !void {
        const row = loginProviderAt(idx) orelse return;
        const provider = row.id;
        if (!loginDiscoveryAvailable(provider)) {
            try self.state.appendTranscript(.system, "model discovery is not available for this provider yet");
            return;
        }
        self.state.mode = .normal;
        if (self.login != null) {
            try self.state.appendTranscript(.system, "a login is already in progress");
            return;
        }
        self.login = self.startCatalogLogin(provider) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "could not start login for {s}: {s}", .{ provider, @errorName(err) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        const msg = try std.fmt.allocPrint(self.allocator, "starting login for {s}…", .{provider});
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn warnOnUnreadableCustomProviders(self: *App) void {
        const path = custom_providers.configPath(self.allocator) catch return;
        defer self.allocator.free(path);
        const data = compat.fs.readFileAlloc(self.allocator, compat.fs.getCwd(), path, custom_providers.max_config_bytes) catch return;
        defer self.allocator.free(data);
        const providers = custom_providers.parse(self.allocator, data) catch |err| {
            const msg = std.fmt.allocPrint(
                self.allocator,
                "{s} was not loaded: {s}. Custom providers are disabled until it parses.",
                .{ path, @errorName(err) },
            ) catch return;
            defer self.allocator.free(msg);
            self.state.appendTranscript(.@"error", msg) catch {};
            return;
        };
        custom_providers.deinitProviders(self.allocator, providers);
    }

    pub fn statusReport(self: *App, allocator: std.mem.Allocator) ![]u8 {
        var out: std.Io.Writer.Allocating = .init(allocator);
        errdefer out.deinit();
        const w = &out.writer;
        const status = &self.state.status;
        const runtime = self.runtime;
        const model: ?ai_types.Model = if (runtime) |rt| rt.currentModel() else null;
        const now_ms = compat.time.nowMillis();

        try w.writeAll("Session\n");
        try statusLine(w, "title", if (self.session_title.len > 0) self.session_title else "(untitled)");
        try statusLine(w, "id", if (self.session_id.len > 0) self.session_id else "(not saved yet)");
        if (self.session_created_at > 0) {
            var ago_buf: [24]u8 = undefined;
            try statusLinePrint(w, "started", "{s} ago", .{agoText(&ago_buf, now_ms - self.session_created_at)});
        }
        const home = compat.getEnvVarOwned(allocator, "HOME") catch null;
        defer if (home) |value| allocator.free(value);
        if (self.working_dir.len > 0) {
            const shown = try collapseHome(allocator, self.working_dir, home);
            defer allocator.free(shown);
            try statusLine(w, "directory", shown);
        }
        if (self.state.git_branch.len > 0) try statusLine(w, "branch", self.state.git_branch);
        try statusLinePrint(w, "compactions", "{d}", .{self.compaction_transcripts.items.len});
        try statusLinePrint(w, "turns", "{d}", .{status.turn_count});

        try w.writeAll("\nModel\n");
        if (model) |m| {
            try statusLinePrint(w, "model", "{s}/{s}", .{ m.provider, m.id });
            try statusLine(w, "reasoning", if (m.reasoning) "yes" else "no");
        } else {
            try statusLinePrint(w, "model", "{s}/{s}", .{ if (status.provider.len > 0) status.provider else "none", if (status.model.len > 0) status.model else "none" });
        }
        try statusLine(w, "thinking", @tagName(self.state.thinking_level));
        if (runtime) |rt| {
            if (rt.contextWindowOverride()) |window| {
                try statusLinePrint(w, "context window", "{d} (set with /context)", .{window});
            } else {
                try statusLinePrint(w, "context window", "{d}", .{rt.contextWindow()});
            }
            switch (rt.outputSetting()) {
                .auto => try statusLine(w, "output limit", "auto"),
                .max => try statusLine(w, "output limit", "max"),
                .tokens => |tokens| try statusLinePrint(w, "output limit", "{d}", .{tokens}),
            }
        }

        try w.writeAll("\nUsage\n");
        const used: u64 = if (self.state.telemetry.estimated_tokens > 0) self.state.telemetry.estimated_tokens else status.context_used;
        if (status.context_limit > 0) {
            try statusLinePrint(w, "context", "{d} / {d} ({d}%)", .{ used, status.context_limit, used * 100 / status.context_limit });
        } else {
            try statusLinePrint(w, "context", "{d}", .{used});
        }
        if (self.state.telemetry.input_cost_per_million > 0) {
            const dollars = (@as(f64, @floatFromInt(used)) / 1_000_000.0) * self.state.telemetry.input_cost_per_million;
            try statusLinePrint(w, "next request", "~${d:.4} input", .{dollars});
        }
        try usageLine(w, "last reply", self.state.telemetry.last_turn_usage);
        try usageLine(w, "this sitting", self.state.telemetry.session_usage);
        const rate = &self.state.telemetry.rate;
        if (rate.turnShown().hasFigure()) try statusLinePrint(w, "rate", "{d} tok/s", .{rate.turnShown().perSecond()});
        if (rate.average.hasFigure()) try statusLinePrint(w, "average rate", "{d} tok/s", .{rate.average.perSecond()});

        try w.writeAll("\nRun\n");
        try statusLine(w, "state", if (status.compacting) "compacting" else if (status.streaming) "streaming" else if (status.refreshing_models) "refreshing models" else "idle");
        try statusLinePrint(w, "queued", "{d} steer, {d} follow-up", .{ self.state.queue.steering, self.state.queue.follow_up });
        if (self.deferred_commands.items.len == 0) {
            try statusLine(w, "held commands", "none");
        } else for (self.deferred_commands.items, 0..) |text, i| {
            try statusLine(w, if (i == 0) "held commands" else "", text);
        }
        if (runtime) |rt| {
            if (rt.pending_model_index) |index| {
                if (index < rt.models.len) try statusLinePrint(w, "model switch", "to {s}/{s}", .{ rt.models[index].provider, rt.models[index].id });
            }
        }
        if (self.pending_compaction) |focus| try statusLinePrint(w, "held compaction", "{s}", .{if (focus.len > 0) focus else "(no focus)"});

        try w.writeAll("\nSettings\n");
        try statusLine(w, "permissions", @tagName(self.state.permission_mode));
        switch (self.state.autocompact) {
            .off => try statusLine(w, "autocompact", "off"),
            .percent => |percent| try statusLinePrint(w, "autocompact", "{d}% of the window", .{percent}),
            .tokens => |count| try statusLinePrint(w, "autocompact", "at {d} tokens", .{count}),
            .auto => if (model) |m| {
                if (tui_state.autoCompactAt(.auto, m)) |at| try statusLinePrint(w, "autocompact", "auto, at {d} tokens", .{at}) else try statusLine(w, "autocompact", "auto");
            } else try statusLine(w, "autocompact", "auto"),
        }
        const v = self.state.verbosity;
        try statusLinePrint(w, "verbosity", "thinking {t}, tools {t}, output {t}, notices {t}, status {t}", .{ v.thinking, v.tools, v.output, v.notices, v.status });
        try statusLine(w, "auto worktree", if (self.mode_settings.auto_worktree) "on" else "off");

        try w.writeAll("\nAuth\n");
        const provider_id = if (model) |m| m.provider else status.provider;
        try statusLinePrint(w, "signed in", "{s}", .{try self.signInText(provider_id)});

        return out.toOwnedSlice();
    }

    fn signInText(self: *App, provider_id: []const u8) ![]const u8 {
        if (provider_id.len == 0) return "no provider";
        self.refreshLoginStatus();
        for (provider_catalog.all, 0..) |row, i| {
            if (!std.mem.eql(u8, row.id, provider_id)) continue;
            return switch (self.login_status[i]) {
                .none => "no saved credential",
                .api_key => "saved API key",
                .env_key => "API key from the environment",
                .oauth => "signed in (OAuth)",
                .expired => "OAuth sign-in expired",
            };
        }
        if (self.isDeclaredCustomProvider(provider_id)) return "custom provider (providers.json)";
        return "unknown provider";
    }

    fn isDeclaredCustomProvider(self: *App, provider_id: []const u8) bool {
        const providers = custom_providers.load(self.allocator, custom_providers.max_config_bytes) catch return false;
        defer custom_providers.deinitProviders(self.allocator, providers);
        for (providers) |provider| {
            if (std.mem.eql(u8, provider.id, provider_id)) return true;
        }
        return false;
    }

    fn startCustomLogin(self: *App, provider_id: []const u8) !void {
        self.state.mode = .normal;
        if (self.login != null) {
            try self.state.appendTranscript(.system, "a login is already in progress");
            return;
        }
        self.login = tui_login.LoginSession.startApiKey(self.allocator, provider_id) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "could not start login for {s}: {s}", .{ provider_id, @errorName(err) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        const msg = try std.fmt.allocPrint(self.allocator, "starting login for {s}…", .{provider_id});
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn startLoginProviderName(self: *App, provider_id: []const u8) !void {
        if (provider_catalog.provider(provider_id)) |row| {
            if (supportsLogin(row)) {
                if (!loginDiscoveryAvailable(provider_id)) {
                    try self.state.appendTranscript(.system, "model discovery is not available for this provider yet");
                    return;
                }
                if (loginProviderIndex(provider_id)) |idx| return self.startLoginProviderIndex(idx);
                return self.startCatalogLoginProvider(provider_id);
            }
        }
        const idx = loginProviderIndex(provider_id) orelse {
            if (self.isDeclaredCustomProvider(provider_id)) return self.startCustomLogin(provider_id);
            const msg = try std.fmt.allocPrint(self.allocator, "unknown login provider: {s}", .{provider_id});
            defer self.allocator.free(msg);
            try self.state.status.setError(self.allocator, msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        try self.startLoginProviderIndex(idx);
    }

    fn applySelectedLogin(self: *App) !void {
        const idx = self.pickerSourceIndex(self.state.menu_index) orelse return;
        try self.startLoginProviderIndex(idx);
    }

    fn applySelectedPermission(self: *App) !void {
        const idx = self.pickerSourceIndex(self.state.menu_index) orelse return;
        const mode = permission_modes[idx];
        const runtime = self.runtime orelse return error.NoRuntimeConfigured;
        runtime.setPermissionMode(mode) catch |err| switch (err) {
            error.UnavailableOverOap => {
                self.state.mode = .normal;
                try self.state.appendTranscript(.@"error", over_oap_setting_refusal);
                return;
            },
            else => return err,
        };
        self.state.permission_mode = mode;
        self.state.mode = .normal;
        const msg = try std.fmt.allocPrint(self.allocator, "permission mode set to {s}", .{@tagName(mode)});
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn pollLogin(self: *App) !void {
        const session = self.login orelse return;
        switch (session.poll()) {
            .none => {},
            .show_auth => |auth| {
                const msg = if (auth.instructions) |ins|
                    try std.fmt.allocPrint(self.allocator, "open this URL to authorize:\n{s}\n{s}", .{ auth.url, ins })
                else
                    try std.fmt.allocPrint(self.allocator, "open this URL to authorize:\n{s}", .{auth.url});
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.system, msg);
            },
            .request_input => |req| {
                const msg = try std.fmt.allocPrint(self.allocator, "{s} (type your answer and press Enter)", .{req.message});
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.system, msg);
                self.state.login_input_secret = isSecretLoginPrompt(req.message);
                self.state.mode = .login_input;
            },
            .done => |creds| {
                const provider_id = try self.allocator.dupe(u8, session.provider_id);
                defer self.allocator.free(provider_id);
                const save_err = self.saveLoginCredentials(provider_id, creds, session.storesApiKey());
                creds.deinit(self.allocator);
                self.finishLogin();
                if (save_err) |_| {
                    self.refreshLoginStatus();
                    const msg = try std.fmt.allocPrint(self.allocator, "logged in to {s}", .{provider_id});
                    defer self.allocator.free(msg);
                    try self.state.appendTranscript(.system, msg);
                    try self.refreshModelsInBackground();
                } else |err| {
                    const msg = try std.fmt.allocPrint(self.allocator, "login succeeded but saving credentials failed: {s}", .{@errorName(err)});
                    defer self.allocator.free(msg);
                    try self.state.appendTranscript(.@"error", msg);
                }
            },
            .failed => |name| {
                const msg = try std.fmt.allocPrint(self.allocator, "login failed: {s}", .{name});
                defer self.allocator.free(msg);
                self.finishLogin();
                try self.state.appendTranscript(.@"error", msg);
            },
        }
    }

    fn finishLogin(self: *App) void {
        if (self.login) |session| {
            session.deinit();
            self.login = null;
        }
        if (self.state.mode == .login_input) self.state.mode = .normal;
    }

    fn saveLoginCredentials(self: *App, provider_id: []const u8, creds: oauth_storage.Credentials, stores_api_key: bool) !void {
        self.discardModelFetch();
        var storage = try oauth_storage.AuthStorage.loadDefault(self.allocator);
        defer storage.deinit();

        const key = try self.allocator.dupe(u8, provider_id);
        var owned = false;
        errdefer if (!owned) self.allocator.free(key);
        if (stores_api_key) {
            const api_key = try self.allocator.dupe(u8, creds.access);
            errdefer if (!owned) self.allocator.free(api_key);

            const provider_data: ?[]const u8 = if (creds.provider_data) |pd|
                try self.allocator.dupe(u8, pd)
            else
                null;
            errdefer if (!owned) {
                if (provider_data) |pd| self.allocator.free(pd);
            };

            if (storage.providers.fetchRemove(provider_id)) |removed| {
                self.allocator.free(removed.key);
                removed.value.deinit(self.allocator);
            }

            if (provider_data) |pd| {
                try storage.providers.put(key, .{ .oauth = .{
                    .refresh = "",
                    .access = api_key,
                    .expires = std.math.maxInt(i64),
                    .provider_data = pd,
                } });
            } else {
                try storage.providers.put(key, .{ .api_key = api_key });
            }
            owned = true;
            try storage.persist();
            return;
        }

        const refresh = try self.allocator.dupe(u8, creds.refresh);
        errdefer if (!owned) self.allocator.free(refresh);
        const access = try self.allocator.dupe(u8, creds.access);
        errdefer if (!owned) self.allocator.free(access);
        const pd: ?[]const u8 = if (creds.provider_data) |d| try self.allocator.dupe(u8, d) else null;
        errdefer if (!owned) {
            if (pd) |d| self.allocator.free(d);
        };

        if (storage.providers.fetchRemove(provider_id)) |removed| {
            self.allocator.free(removed.key);
            removed.value.deinit(self.allocator);
        }

        try storage.providers.put(key, .{ .oauth = .{
            .refresh = refresh,
            .access = access,
            .expires = creds.expires,
            .provider_data = pd,
        } });
        owned = true;
        try storage.persist();
    }

    fn refreshModels(self: *App) !bool {
        self.discardModelFetch();
        const models = try loadRuntimeModelsFresh(self.allocator);
        defer model_catalog.deinitModels(self.allocator, models);
        return self.applyModels(models);
    }

    fn runtimeBusy(self: *App) bool {
        const runtime = self.runtime orelse return false;
        if (runtime.remote != null) return !runtime.isIdle();
        const local = if (runtime.local_agent) |*agent_ref| agent_ref else return false;
        return !local.isIdle();
    }

    fn refreshModelsInBackground(self: *App) !void {
        if (self.model_fetch != null) {
            self.model_refetch = true;
            return;
        }
        self.model_fetch = try ModelFetch.start();
        self.state.status.refreshing_models = true;
    }

    fn discardModelFetch(self: *App) void {
        const fetch = self.model_fetch orelse return;
        self.model_fetch = null;
        self.model_refetch = false;
        self.state.status.refreshing_models = false;
        var outcome = fetch.finish();
        outcome.deinit();
    }

    fn collectModelFetch(self: *App) !void {
        const fetch = self.model_fetch orelse return;
        if (!fetch.done.load(.acquire)) return;
        self.model_fetch = null;
        var outcome = fetch.finish();
        defer outcome.deinit();
        if (self.model_refetch) {
            self.model_refetch = false;
            self.model_fetch = try ModelFetch.start();
            return;
        }
        self.state.status.refreshing_models = false;
        for (outcome.notes.items.items) |item| {
            const msg = try std.fmt.allocPrint(self.allocator, "model refresh: {s}: {s}", .{ item.source, item.reason });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
        }
        const fetched = outcome.result catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "refreshing models failed: {s}", .{@errorName(err)});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        const owned = try self.allocator.alloc(ai_types.Model, fetched.len);
        var cloned: usize = 0;
        errdefer {
            for (owned[0..cloned]) |*model| model.deinit(self.allocator);
            self.allocator.free(owned);
        }
        for (fetched, 0..) |model, index| {
            owned[index] = try ai_types.cloneModel(self.allocator, model);
            cloned += 1;
        }
        if (self.runtimeBusy()) try self.state.appendNotice("model catalog fetched; it takes effect when this turn ends");
        if (self.pending_models) |old| model_catalog.deinitModels(self.allocator, old);
        self.pending_models = owned;
    }

    fn applyPendingModelsBeforeResume(self: *App) !void {
        if (self.pending_models == null) return;
        if (self.runtime) |runtime| {
            if (runtime.local_agent) |*local| local.waitForIdle();
        }
        try self.applyPendingModels();
    }

    fn applyPendingModels(self: *App) !void {
        const models = self.pending_models orelse return;
        if (self.runtimeBusy()) return;
        self.pending_models = null;
        defer model_catalog.deinitModels(self.allocator, models);
        try self.reportModelRefresh(self.applyModels(models), "refreshing models failed");
    }

    fn applyModels(self: *App, models: []const ai_types.Model) !bool {
        const runtime = self.runtime orelse return false;
        const pending: ?[]u8 = if (runtime.pending_model_index) |idx| try std.fmt.allocPrint(self.allocator, "{s}/{s}", .{ runtime.models[idx].provider, runtime.models[idx].id }) else null;
        defer if (pending) |name| self.allocator.free(name);
        try runtime.replaceModels(models, runtime.currentModel());
        if (pending) |name| {
            if (runtime.pending_model_index == null) {
                const msg = try std.fmt.allocPrint(self.allocator, "the pending switch to {s} was dropped: the refreshed model list no longer has it", .{name});
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.@"error", msg);
            }
        }
        const model = runtime.currentModel() orelse return false;
        const switched = !std.mem.eql(u8, model.id, self.state.status.model) or
            !std.mem.eql(u8, model.provider, self.state.status.provider);
        if (switched) try self.state.status.setModel(self.allocator, model.id, model.provider);
        self.applyContextWindow();
        return switched;
    }

    fn reportModelRefresh(self: *App, switched: anyerror!bool, failure: []const u8) !void {
        const changed = switched catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "{s}: {s}", .{ failure, @errorName(err) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        try self.state.appendNotice("model catalog refreshed");
        if (!changed) return;
        const msg = try std.fmt.allocPrint(self.allocator, "model switched to {s}/{s}", .{ self.state.status.provider, self.state.status.model });
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn noteEnvironmentCredential(self: *App, provider_id: []const u8, name: []const u8) !void {
        const value = compat.getEnvVarOwned(self.allocator, name) catch return;
        defer self.allocator.free(value);
        if (value.len == 0) return;
        const msg = try std.fmt.allocPrint(self.allocator, "{s} is still set, so {s} stays signed in", .{ name, provider_id });
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn flagValue(word: ?[]const u8) ?[]const u8 {
        const value = word orelse return null;
        if (std.mem.startsWith(u8, value, "--")) return null;
        return value;
    }

    fn parseProviderAdd(arg: []const u8) ?custom_providers.NewProvider {
        var words = std.mem.tokenizeAny(u8, arg, " \t");
        if (!std.mem.eql(u8, words.next() orelse return null, "add")) return null;
        var new = custom_providers.NewProvider{
            .id = flagValue(words.next()) orelse return null,
            .base_url = flagValue(words.next()) orelse return null,
        };
        while (words.next()) |word| {
            if (std.mem.eql(u8, word, "--api")) {
                new.api = flagValue(words.next()) orelse return null;
            } else if (std.mem.eql(u8, word, "--env")) {
                if (new.auth_none) return null;
                new.env = flagValue(words.next()) orelse return null;
            } else if (std.mem.eql(u8, word, "--no-auth")) {
                if (new.env != null) return null;
                new.auth_none = true;
            } else return null;
        }
        return new;
    }

    fn addProvider(self: *App, arg: []const u8) !void {
        const new = parseProviderAdd(arg) orelse {
            try self.state.appendTranscript(.@"error", tui_commands.provider_usage);
            return;
        };
        const path = custom_providers.addProvider(self.allocator, new) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "could not declare {s}: {s}", .{ new.id, @errorName(err) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        defer self.allocator.free(path);
        const next = if (new.auth_none)
            try std.fmt.allocPrint(self.allocator, "Declared {s} in {s}, with no credential. Run /model refresh to list its models.", .{ new.id, path })
        else if (new.env) |name|
            try std.fmt.allocPrint(self.allocator, "Declared {s} in {s}; it reads its key from {s}. Run /model refresh to list its models.", .{ new.id, path, name })
        else
            try std.fmt.allocPrint(self.allocator, "Declared {s} in {s}. Run /login {s} to add its key.", .{ new.id, path, new.id });
        defer self.allocator.free(next);
        try self.state.appendTranscript(.system, next);
    }

    fn listProviders(self: *App) !void {
        var config = custom_providers.loadConfigStrict(self.allocator, custom_providers.max_config_bytes) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "could not read the provider config: {s}", .{@errorName(err)});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        defer config.deinit(self.allocator);
        const owned_path = custom_providers.configPath(self.allocator) catch null;
        defer if (owned_path) |path| self.allocator.free(path);
        const path = owned_path orelse custom_providers.config_file_name;
        if (config.providers.len == 0 and config.overrides.len == 0) {
            const msg = try std.fmt.allocPrint(self.allocator, "no providers are declared in {s}; /provider add <id> <base_url> declares one", .{path});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.system, msg);
            return;
        }
        var out: std.Io.Writer.Allocating = .init(self.allocator);
        defer out.deinit();
        const writer = &out.writer;
        if (config.providers.len > 0) {
            try writer.print("providers declared in {s}:", .{path});
        }
        for (config.providers) |provider| {
            try writer.print("\n  {s} ({s}), {s}, {s}, ", .{ provider.id, provider.name, provider.api, provider.base_url });
            if (provider.auth_none) {
                try writer.writeAll("no credential");
            } else if (provider.env_key) |key| {
                try writer.print("key from {s}", .{key});
            } else {
                try writer.writeAll("saved key");
            }
            if (provider.models.len == 0) {
                try writer.writeAll(", every discovered model");
            } else {
                try writer.print(", {d} declared models", .{provider.models.len});
            }
        }
        if (config.overrides.len > 0) {
            if (config.providers.len > 0) {
                try writer.writeAll("\noverrides on catalogued rows:");
            } else {
                try writer.print("overrides on catalogued rows in {s}:", .{path});
            }
            for (config.overrides) |override| {
                try writer.print("\n  {s}:", .{override.id});
                var first = true;
                if (override.base_url != null) {
                    try writer.writeAll(" base_url");
                    first = false;
                }
                if (override.carries_version != null) {
                    if (!first) try writer.writeAll(",");
                    try writer.writeAll(" carries_version");
                    first = false;
                }
                if (override.headers.len > 0) {
                    if (!first) try writer.writeAll(",");
                    try writer.writeAll(" headers");
                    first = false;
                }
                if (override.models.len > 0) {
                    if (!first) try writer.writeAll(",");
                    try writer.print(" {d} models", .{override.models.len});
                }
            }
        }
        try self.state.appendTranscript(.system, out.written());
    }

    fn removeProvider(self: *App, arg: []const u8) !void {
        var words = std.mem.tokenizeAny(u8, arg, " \t");
        _ = words.next();
        const id = words.next() orelse {
            try self.state.appendTranscript(.@"error", tui_commands.provider_usage);
            return;
        };
        if (self.login) |pending| {
            if (std.mem.eql(u8, pending.provider_id, id)) {
                const msg = try std.fmt.allocPrint(self.allocator, "a login to {s} is in progress; cancel it before deleting the provider", .{id});
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.@"error", msg);
                return;
            }
        }
        self.discardModelFetch();
        const path = custom_providers.deleteProvider(self.allocator, id) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "could not delete {s}: {s}", .{ id, @errorName(err) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        defer self.allocator.free(path);
        if (oauth_storage.AuthStorage.removeStored(self.allocator, id)) |key_removed| {
            const msg = try std.fmt.allocPrint(self.allocator, "deleted {s} from {s}{s}", .{ id, path, if (key_removed) ", and its saved key" else "" });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.system, msg);
        } else |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "deleted {s} from {s}, but removing its saved key failed: {s}", .{ id, path, @errorName(err) });
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
        }
        if (self.runtime != null) try self.refreshModelsInBackground();
    }

    fn logoutProviderId(name: []const u8) []const u8 {
        if (provider_catalog.provider(name) != null) return name;
        const index = loginProviderIndex(name) orelse return name;
        const row = loginProviderAt(index) orelse return name;
        return row.id;
    }

    fn logoutProvider(self: *App, requested: []const u8) !void {
        self.discardModelFetch();
        const provider_id = logoutProviderId(requested);
        if (self.login) |pending| {
            if (std.mem.eql(u8, pending.provider_id, provider_id)) {
                const msg = try std.fmt.allocPrint(self.allocator, "a login to {s} is in progress; cancel it before logging out", .{pending.provider_id});
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.@"error", msg);
                return;
            }
        }
        const removed = oauth_storage.AuthStorage.removeStored(self.allocator, provider_id) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "logout failed: {s}", .{@errorName(err)});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        const outcome = try std.fmt.allocPrint(self.allocator, "{s} {s}", .{ if (removed) "logged out of" else "no saved credential for", provider_id });
        defer self.allocator.free(outcome);
        try self.state.appendTranscript(.system, outcome);
        if (provider_catalog.provider(provider_id)) |row| {
            for (row.credential_env) |name| try self.noteEnvironmentCredential(provider_id, name);
        }
        if (custom_providers.load(self.allocator, custom_providers.max_config_bytes)) |providers| {
            defer custom_providers.deinitProviders(self.allocator, providers);
            for (providers) |provider| {
                if (!std.mem.eql(u8, provider.id, provider_id)) continue;
                if (provider.env_key) |name| try self.noteEnvironmentCredential(provider_id, name);
            }
        } else |_| {}
        if (std.mem.eql(u8, provider_id, "openai-codex")) {
            try self.state.appendTranscript(.system, "oapx imports the Codex CLI's login while it has one; sign out there too to drop openai-codex");
        }
        if (!removed) return;
        self.refreshLoginStatus();
        try self.refreshModelsInBackground();
    }

    fn submitLoginInput(self: *App, text: []const u8) void {
        const session = self.login orelse {
            self.state.mode = .normal;
            return;
        };
        session.provideInput(text) catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        self.state.login_input_secret = false;
        self.state.mode = .normal;
    }

    fn cancelLogin(self: *App) void {
        self.finishLogin();
        self.state.appendTranscript(.system, "login cancelled") catch {};
        self.state.composer.clear();
        self.state.login_input_secret = false;
        self.state.mode = .normal;
    }

    fn moveMenuSelection(self: *App, delta: isize) void {
        const n = self.pickerMatchCount();
        if (n == 0) {
            self.state.menu_index = 0;
            self.state.menu_scroll = 0;
            return;
        }
        if (delta < 0) {
            self.state.menu_index -|= @as(usize, @intCast(-delta));
        } else {
            self.state.menu_index = @min(n - 1, self.state.menu_index + @as(usize, @intCast(delta)));
        }
        self.ensureMenuSelectionVisible();
    }

    fn ensureMenuSelectionVisible(self: *App) void {
        const height = @max(self.last_view_height, 8) / 2;
        if (self.state.menu_index < self.state.menu_scroll) {
            self.state.menu_scroll = self.state.menu_index;
        } else if (height > 0 and self.state.menu_index >= self.state.menu_scroll + height) {
            self.state.menu_scroll = self.state.menu_index + 1 - height;
        }
    }

    fn saveEvent(self: *App, event: tui_runtime.TuiEvent) void {
        if (self.runtime) |runtime| if (runtime.recordsFromEndpoint()) return;
        self.persistEvent(event);
    }

    fn saveEndpointRecords(self: *App) void {
        const runtime = self.runtime orelse return;
        while (runtime.takeEndpointRecord()) |record| {
            var owned = record;
            defer owned.deinit(self.allocator);
            self.persistEvent(owned);
        }
    }

    fn persistEvent(self: *App, event: tui_runtime.TuiEvent) void {
        const store = self.store orelse return;
        if (event == .message_end and event.message_end.role == .assistant) self.flushPendingThinking(store);
        switch (event) {
            .message_start, .context_usage, .prompt_segment_usage, .agent_start, .turn_start, .turn_end, .agent_end, .compaction_start => {},
            .text_delta => |payload| {
                if (jsonStringBudget(payload.delta.slice()) > max_session_event_payload_bytes) return;
            },
            .thinking_delta => |payload| {
                if (jsonStringBudget(payload.delta.slice()) > max_session_event_payload_bytes) return;
            },
            .tool_call_delta => |payload| {
                if (jsonStringBudget(payload.delta.slice()) > max_session_event_payload_bytes) return;
            },
            .tool_execution_start => |payload| {
                if (toolRequestPayloadSize(payload) > max_session_event_payload_bytes) return;
            },
            .provider_event => |payload| {
                if (jsonStringBudget(payload.event_json.slice()) > max_session_event_payload_bytes) return;
            },
            .tool_approval_requested => |payload| {
                if (toolRequestPayloadSize(payload) > max_session_event_payload_bytes) return;
            },
            .tool_execution_update => |payload| {
                if (toolUpdatePayloadSize(payload) > max_session_event_payload_bytes) return;
            },
            .message_end => |payload| {
                if (messageEndPayloadSize(payload) > max_session_event_payload_bytes) return;
            },
            .tool_execution_end => |payload| {
                if (toolExecutionEndPayloadSize(payload) > max_session_event_payload_bytes) return;
            },
            .system_warning => |payload| {
                if (jsonStringBudget(payload.message.slice()) > max_session_event_payload_bytes) return;
            },
            .backpressure_status => {},
            .compaction_end => |payload| {
                if (jsonStringBudget(payload.text.slice()) + jsonStringBudget(payload.transcript.slice()) + jsonStringBudget(payload.message.slice()) > max_session_event_payload_bytes) return;
            },
            .@"error" => |payload| {
                if (jsonStringBudget(payload.message.slice()) > max_session_event_payload_bytes) return;
            },
        }
        switch (event) {
            .text_delta, .tool_call_delta, .provider_event, .tool_execution_update => {
                store.saveChunk(self.session_id, event) catch {};
                return;
            },
            .thinking_delta => |payload| {
                self.pending_thinking.appendSlice(self.allocator, payload.delta.slice()) catch {};
                store.saveChunk(self.session_id, event) catch {};
                return;
            },
            .message_start => self.pending_thinking.clearRetainingCapacity(),
            else => {},
        }
        const compaction = event == .compaction_end and event.compaction_end.outcome == .completed;
        const offset: ?u64 = if (compaction) store.conversationBytes(self.session_id) catch null else null;
        const wrote_metadata = self.saveConversationEvent(store, event, compaction);
        if (wrote_metadata) self.compaction_offset = offset orelse self.compaction_offset;
        const titled = switch (event) {
            .message_end => |payload| payload.role == .user and self.titleFromFirstMessage(tui_state.withoutZenNote(std.mem.trim(u8, payload.text.slice(), " \t\r\n"))),
            else => false,
        };
        if (wrote_metadata or titled or event == .agent_end) self.saveSessionIndex(store);
        if (event == .agent_end) self.requestSessionTitle();
        self.flushPendingWorktreeSidecar();
    }

    fn titleFromFirstMessage(self: *App, text: []const u8) bool {
        if (self.session_title.len > 0) return false;
        const line = titleLine(text);
        if (line.len == 0) return false;
        const title = self.allocator.dupe(u8, line) catch return false;
        const first = self.allocator.dupe(u8, text) catch {
            self.allocator.free(title);
            return false;
        };
        self.session_title = title;
        self.first_user_text = first;
        self.publishTitle();
        return true;
    }

    fn renameSession(self: *App, text: []const u8) !void {
        const line = titleLine(text);
        if (line.len == 0) {
            try self.state.appendTranscript(.@"error", "usage: /rename <title>");
            return;
        }
        const title = try self.allocator.dupe(u8, line);
        if (self.session_title.len > 0) self.allocator.free(self.session_title);
        self.session_title = title;
        self.session_title_renamed = true;
        self.publishTitle();
        if (self.session_written) {
            if (self.store) |store| self.saveSessionIndex(store);
        }
        const message = try std.fmt.allocPrint(self.allocator, "Session renamed to \"{s}\"", .{title});
        defer self.allocator.free(message);
        try self.state.appendTranscript(.system, message);
    }

    fn publishTitle(self: *App) void {
        self.state.setSessionTitle(self.allocator, self.session_title) catch {};
    }

    fn requestSessionTitle(self: *App) void {
        if (self.session_title_generated or self.session_title_renamed or self.first_user_text.len == 0) return;
        const runtime = self.runtime orelse return;
        const session_id = self.allocator.dupe(u8, self.session_id) catch return;
        if (!(runtime.requestTitle(self.first_user_text) catch false)) {
            self.allocator.free(session_id);
            return;
        }
        if (self.title_session_id.len > 0) self.allocator.free(self.title_session_id);
        self.title_session_id = session_id;
    }

    fn collectGeneratedTitle(self: *App) void {
        const runtime = self.runtime orelse return;
        const title = runtime.takeGeneratedTitle() orelse return;
        const session_id = self.title_session_id;
        self.title_session_id = &.{};
        defer if (session_id.len > 0) self.allocator.free(session_id);
        if (!std.mem.eql(u8, session_id, self.session_id)) {
            defer self.allocator.free(title);
            if (self.store) |store| store.saveGeneratedTitle(session_id, title) catch {};
            return;
        }
        if (self.session_title_renamed) {
            self.allocator.free(title);
            return;
        }
        if (self.session_title.len > 0) self.allocator.free(self.session_title);
        self.session_title = title;
        self.session_title_generated = true;
        self.publishTitle();
        if (self.store) |store| self.saveSessionIndex(store);
    }

    fn flushPendingThinking(self: *App, store: session_store.Store) void {
        defer self.pending_thinking.clearRetainingCapacity();
        var rest: []const u8 = self.pending_thinking.items;
        while (rest.len > 0) {
            const record = sessionRecordPrefix(rest);
            _ = self.saveConversationEvent(store, .{ .thinking_delta = .{ .content_index = 0, .delta = OwnedSlice(u8).initBorrowed(record) } }, false);
            rest = rest[record.len..];
        }
    }

    fn sessionRecordPrefix(text: []const u8) []const u8 {
        const limit = max_session_event_payload_bytes / jsonStringBudget("x");
        if (text.len <= limit) return text;
        var len = limit;
        while (len > limit - 3 and (text[len] & 0xc0) == 0x80) len -= 1;
        return text[0..len];
    }

    fn saveConversationEvent(self: *App, store: session_store.Store, event: tui_runtime.TuiEvent, force_metadata: bool) bool {
        const meta = self.currentSessionMetadata();
        const changed = force_metadata or !self.session_written or
            !std.mem.eql(u8, meta.model, self.written_model) or
            !std.mem.eql(u8, meta.provider, self.written_provider);
        if (!changed) {
            store.saveEvent(self.session_id, event) catch {};
            return false;
        }
        store.save(meta, event) catch return false;
        self.rememberWrittenMetadata(meta.model, meta.provider) catch {};
        if (!self.session_written) {
            self.session_written = true;
            if (self.session_created_at == 0) self.session_created_at = meta.last_active;
        }
        return true;
    }

    fn rememberWrittenMetadata(self: *App, model: []const u8, provider: []const u8) !void {
        const next_model = try self.allocator.dupe(u8, model);
        errdefer self.allocator.free(next_model);
        const next_provider = try self.allocator.dupe(u8, provider);
        if (self.written_model.len > 0) self.allocator.free(self.written_model);
        if (self.written_provider.len > 0) self.allocator.free(self.written_provider);
        self.written_model = next_model;
        self.written_provider = next_provider;
    }

    fn restoreCompactionTranscripts(self: *App, store: session_store.Store, loaded: *const session_store.LoadedSession) !void {
        self.clearCompactionTranscripts();
        for (loaded.events.items) |event| {
            if (event == .compaction_end and event.compaction_end.outcome == .completed) try self.recordCompactionTranscript(event.compaction_end.transcript.slice());
        }
        if (self.compaction_transcripts.items.len >= loaded.metadata.compactions) return;
        self.clearCompactionTranscripts();
        for (1..@as(usize, loaded.metadata.compactions) + 1) |index| {
            const path = try store.transcriptPath(self.session_id, index);
            errdefer self.allocator.free(path);
            try self.compaction_transcripts.append(self.allocator, path);
        }
    }

    fn saveSessionIndex(self: *App, store: session_store.Store) void {
        var meta = self.currentSessionMetadata();
        meta.created_at = self.session_created_at;
        meta.compaction_offset = self.compaction_offset;
        meta.compactions = @intCast(self.compaction_transcripts.items.len);
        meta.title = self.session_title;
        meta.title_generated = self.session_title_generated;
        meta.title_renamed = self.session_title_renamed;
        store.saveIndex(meta) catch {};
    }

    fn adoptLoadedSession(self: *App, metadata: session_store.SessionMetadata) !void {
        try self.rememberWrittenMetadata(metadata.model, metadata.provider);
        self.session_written = true;
        self.session_created_at = metadata.created_at;
        self.compaction_offset = metadata.compaction_offset;
        self.pending_thinking.clearRetainingCapacity();
        const title = try self.allocator.dupe(u8, metadata.title);
        if (self.session_title.len > 0) self.allocator.free(self.session_title);
        self.session_title = title;
        self.session_title_generated = metadata.title_generated;
        self.session_title_renamed = metadata.title_renamed;
        self.publishTitle();
        if (self.first_user_text.len > 0) self.allocator.free(self.first_user_text);
        self.first_user_text = &.{};
    }

    fn messageEndPayloadSize(payload: @TypeOf(@as(tui_runtime.TuiEvent, undefined).message_end)) usize {
        return jsonStringBudget(payload.text.slice()) +
            jsonStringBudget(payload.content_json.slice()) +
            jsonStringBudget(payload.tool_call_id.slice()) +
            jsonStringBudget(payload.tool_name.slice()) +
            jsonStringBudget(payload.args_json.slice()) +
            jsonStringBudget(payload.tool_calls_json.slice()) +
            jsonStringBudget(payload.details_json.slice()) +
            jsonStringBudget(payload.artifacts_json.slice());
    }

    fn toolExecutionEndPayloadSize(payload: @TypeOf(@as(tui_runtime.TuiEvent, undefined).tool_execution_end)) usize {
        return jsonStringBudget(payload.result_json.slice()) +
            jsonStringBudget(payload.tool_call_id.slice()) +
            jsonStringBudget(payload.tool_name.slice()) +
            jsonStringBudget(payload.artifact_refs.slice());
    }

    fn toolRequestPayloadSize(payload: anytype) usize {
        return jsonStringBudget(payload.tool_call_id.slice()) +
            jsonStringBudget(payload.tool_name.slice()) +
            jsonStringBudget(payload.args_json.slice());
    }

    fn toolUpdatePayloadSize(payload: @TypeOf(@as(tui_runtime.TuiEvent, undefined).tool_execution_update)) usize {
        return toolRequestPayloadSize(payload) +
            jsonStringBudget(payload.partial_result_json.slice());
    }

    fn jsonStringBudget(value: []const u8) usize {
        return value.len * 6;
    }

    test "json string budget accounts for worst-case escaping" {
        try std.testing.expectEqual(@as(usize, 24), jsonStringBudget("\\\\\\\\"));
    }

    fn currentSessionMetadata(self: *App) session_store.SessionMetadata {
        return .{
            .session_id = self.session_id,
            .model = self.state.status.model,
            .provider = self.state.status.provider,
            .last_active = compat.time.nowMillis(),
            .thinking_level = self.state.thinking_level,
        };
    }

    pub fn start(self: *App) !void {
        if (self.session) |*session| {
            session.start() catch |err| {
                try self.state.status.setError(self.allocator, @errorName(err));
                try self.state.appendTranscript(.@"error", @errorName(err));
                return;
            };
        } else {
            try self.state.status.setError(self.allocator, "no runtime configured");
        }
    }

    fn runFinishedWithoutProviderError(self: *App) void {
        self.forgetRunError();
        self.auto_continue.onUserTurn();
    }

    pub fn userTookOver(self: *App) void {
        const was_pending = self.auto_continue.pending();
        self.auto_continue.onUserTurn();
        if (!was_pending) return;
        self.state.appendTranscript(.system, "the automatic continue was dropped.") catch {};
    }

    pub fn discardReplayedError(self: *App) void {
        self.forgetRunError();
        self.auto_continue.onUserTurn();
    }

    fn scheduleAutoContinue(self: *App) void {
        if (self.state.queue.total() > 0 or self.state.held_after_abort.items.len > 0) {
            self.auto_continue.onUserTurn();
            return;
        }
        switch (self.auto_continue.onRunEndedInError(self.run_error_text, compat.time.nowMillis())) {
            .skip => {},
            .send_after => |delay| {
                const message = std.fmt.allocPrint(self.allocator, "The run ended in a provider error. Continuing in {d}s.", .{
                    @divFloor(delay + 999, 1000),
                }) catch return;
                defer self.allocator.free(message);
                self.state.appendTranscript(.system, message) catch {};
            },
        }
    }

    pub fn pumpAutoContinue(self: *App, now_ms: i64) void {
        if (!self.auto_continue.due(now_ms)) return;
        if (self.state.mode != .normal) return;
        if (self.state.status.streaming) return;
        if (self.state.queue.total() > 0) return;
        self.auto_continue.take();
        const message = std.fmt.allocPrint(self.allocator, "Provider error. Sending \"{s}\" on your behalf.", .{
            tui_auto_continue.continue_text,
        }) catch return;
        defer self.allocator.free(message);
        self.state.appendTranscript(.system, message) catch {};
        if (self.compactBeforeTurn(tui_auto_continue.continue_text) catch false) return;
        self.sendUserTurn(tui_auto_continue.continue_text) catch |err| self.recordError(@errorName(err)) catch {};
    }

    fn pollWorktree(self: *App) !void {
        const job = self.worktree_job orelse return;
        var outcome = job.poll() orelse return;
        defer outcome.deinit(self.allocator);
        job.deinit();
        self.worktree_job = null;
        self.applyWorktreeOutcome(outcome) catch |err| {
            try self.state.status.setError(self.allocator, @errorName(err));
            try self.state.appendTranscript(.@"error", @errorName(err));
        };
        if (self.held_user_message.len > 0) {
            const message = self.held_user_message;
            self.held_user_message = &.{};
            defer self.allocator.free(message);
            try self.submit(message);
        } else if (self.state.held_after_abort.items.len > 0) {
            _ = try self.sendHeld(null);
        }
        while (self.queued_worktree_messages.items.len > 0) {
            const message = self.queued_worktree_messages.orderedRemove(0);
            defer self.allocator.free(message);
            if (self.session) |*session| {
                try session.followUp(message);
                try self.state.appendQueuedFollowUp(message);
                self.refreshQueuedCounts();
            } else {
                try self.submit(message);
            }
        }
    }

    fn applyWorktreeOutcome(self: *App, outcome: tui_worktree.CreateOutcome) !void {
        switch (outcome) {
            .not_a_repo => try self.state.appendTranscript(.system, "Current directory is not a Git repository; continuing without a worktree."),
            .failed => |message| try self.state.appendTranscript(.system, message),
            .created => |created| {
                const new_dir = try created.info.workingDir(self.allocator);
                defer self.allocator.free(new_dir);
                try (self.runtime orelse return error.NoRuntimeConfigured).setWorkspaceRoot(new_dir);
                try replaceOwnedString(self.allocator, &self.working_dir, new_dir);
                try self.refreshCwdDisplay();
                try self.recordWorktreeSidecar(&created.info);
                try self.state.appendNotice("Git worktree ready for this session.");
                if (created.uncommitted > 0) try self.state.appendTranscript(.system, "Note: the original repository has uncommitted changes; the worktree starts from the current commit.");
            },
        }
    }

    fn pollWorktreeManagement(self: *App) !void {
        const job = self.worktree_management_job orelse return;
        const value = job.poll() orelse return;
        var outcome = value;
        defer outcome.deinit(self.allocator);
        job.deinit();
        self.worktree_management_job = null;
        if (self.pending_delete_id.len > 0) {
            const id = self.pending_delete_id;
            const path = self.pending_delete_path;
            switch (outcome) {
                .dirty => |report| {
                    const detail = try std.fmt.allocPrint(self.allocator, "This session's worktree has uncommitted changes:\n{s}", .{report.summary});
                    defer self.allocator.free(detail);
                    try self.state.appendTranscript(.system, detail);
                    try self.raiseForceDeletePrompt("Force remove anyway and discard them");
                    return;
                },
                .unverified => |message| {
                    const detail = try std.fmt.allocPrint(self.allocator, "Git could not verify this session's worktree ({s}).", .{message});
                    defer self.allocator.free(detail);
                    try self.state.appendTranscript(.system, detail);
                    try self.raiseForceDeletePrompt("Force remove anyway, unverified");
                    return;
                },
                else => {
                    self.pending_delete_id = &.{};
                    self.pending_delete_path = &.{};
                },
            }
            defer self.allocator.free(id);
            defer if (path.len > 0) self.allocator.free(path);
            switch (outcome) {
                .removed => |message| {
                    if (message) |text| try self.state.appendTranscript(.system, text);
                    if (message != null and path.len > 0 and tui_worktree.pathExists(path)) {
                        try self.state.appendTranscript(.system, "The session worktree could not be removed; keeping it and its worktree record.");
                    } else {
                        try self.finishDeleteSession(id);
                    }
                },
                .failed => |message| try self.state.appendTranscript(.@"error", message),
                else => return error.InvalidWorktreeOutcome,
            }
            try self.deliverHeldWorktreeMessages();
            return;
        }
        if (self.pending_resume_id.len > 0) {
            const id = self.pending_resume_id;
            self.pending_resume_id = &.{};
            defer self.allocator.free(id);
            const expected_path = self.pending_resume_path;
            self.pending_resume_path = &.{};
            defer if (expected_path.len > 0) self.allocator.free(expected_path);
            switch (outcome) {
                .reattached => |message| {
                    if (message) |text| {
                        try self.state.appendTranscript(.system, text);
                        if (!tui_worktree.pathExists(expected_path)) {
                            try self.setResumeWithoutWorktree(id);
                            try self.state.appendTranscript(.system, "The session worktree could not be reattached; resuming in the original repository.");
                        }
                    }
                },
                .missing_branch => {
                    try self.setResumeWithoutWorktree(id);
                    try self.state.appendTranscript(.system, "This session's Git worktree and branch are missing; resuming in the original repository.");
                },
                .failed => |message| {
                    try self.setResumeWithoutWorktree(id);
                    try self.state.appendTranscript(.@"error", message);
                    try self.state.appendTranscript(.system, "The session worktree could not be reattached; resuming in the original repository.");
                },
                else => return error.InvalidWorktreeOutcome,
            }
            self.state.session_index = 0;
            for (self.state.sessions.items, 0..) |entry, index| if (std.mem.eql(u8, entry.id, id)) {
                self.state.session_index = index;
                break;
            };
            try self.resumeSelectedSession();
            try self.deliverHeldWorktreeMessages();
        }
    }

    fn deliverHeldWorktreeMessages(self: *App) !void {
        if (self.held_user_message.len > 0) {
            const message = self.held_user_message;
            self.held_user_message = &.{};
            defer self.allocator.free(message);
            try self.submit(message);
        } else if (self.state.held_after_abort.items.len > 0) {
            _ = try self.sendHeld(null);
        }
        try self.drainQueuedWorktreeMessageIfIdle();
    }

    fn drainQueuedWorktreeMessageIfIdle(self: *App) !void {
        if (self.worktree_job != null or self.worktree_management_job != null or self.state.status.streaming or self.queued_worktree_messages.items.len == 0) return;
        if (self.runtime) |runtime| {
            if (runtime.local_agent) |*local| {
                if (!local.isIdle()) return;
            }
        }
        const message = self.queued_worktree_messages.orderedRemove(0);
        defer self.allocator.free(message);
        try self.submit(message);
    }

    pub fn drainEvents(self: *App) !void {
        var session = &(self.session orelse return);
        var completed_agent_end = false;
        var run_ended = false;
        var run_failed = false;
        while (session.popEvent()) |event| {
            var ev = event;
            defer ev.deinit(self.allocator);

            const gen = ev.generation();
            if (self.quarantine_generation > 0 and gen <= self.quarantine_generation) {
                continue;
            }

            if (self.quarantine_events) {
                const is_lifecycle = ev == .agent_start or ev == .turn_start or ev == .compaction_start;
                const is_terminal = ev == .agent_end or ev == .@"error" or (ev == .compaction_end and !ev.compaction_end.in_run);
                if (is_lifecycle or is_terminal) {
                    self.quarantine_events = false;
                    {
                        defer {
                            for (self.quarantine_buffer.items) |*remaining| {
                                var mutable = remaining.*;
                                mutable.deinit(self.allocator);
                            }
                            self.quarantine_buffer.clearRetainingCapacity();
                        }
                        while (self.quarantine_buffer.items.len > 0) {
                            var mutable = self.quarantine_buffer.orderedRemove(0);
                            defer mutable.deinit(self.allocator);
                            if (try self.noteTerminalEvent(mutable)) completed_agent_end = true;
                            self.saveEvent(mutable);
                            try self.applyRuntimeEvent(mutable);
                        }
                    }
                } else {
                    const cloned = try ev.clone(self.allocator);
                    self.quarantine_buffer.append(self.allocator, cloned) catch |err| {
                        var to_free = cloned;
                        to_free.deinit(self.allocator);
                        return err;
                    };
                    continue;
                }
            }
            if (try self.noteTerminalEvent(ev)) completed_agent_end = true;
            if (ev == .agent_end) run_ended = true;
            if (ev == .@"error") run_failed = true;
            self.saveEvent(ev);
            try self.applyRuntimeEvent(ev);
        }
        self.saveEndpointRecords();
        self.refreshQueuedCounts();
        self.state.reconcileSteers(session.steersConsumedCount());
        self.syncBackpressureState();
        self.syncModelTelemetry();
        try self.noteDroppedContextWindow();
        self.collectGeneratedTitle();
        if (self.pending_session_reset and !self.state.status.streaming) {
            if (self.runtime) |runtime| {
                if (runtime.local_agent) |*local| {
                    if (local.isIdle()) {
                        self.dropHeldCompaction();
                        local.clearAllQueues();
                        local.replaceMessages(&.{}) catch {};
                        self.pending_session_reset = false;
                    }
                }
            } else {
                self.pending_session_reset = false;
            }
        }
        try self.collectModelFetch();
        try self.applyPendingModels();
        var resumed_run = false;
        if (completed_agent_end and self.state.queue.total() > 0) {
            try self.applyPendingModelsBeforeResume();
            try self.applyPendingModelSwitchBeforeRun();
            session.resumeSession() catch |err| {
                try self.state.status.setError(self.allocator, @errorName(err));
                try self.state.appendTranscript(.@"error", @errorName(err));
                return;
            };
            self.refreshQueuedCounts();
            resumed_run = true;
        }
        if (self.compaction_just_ended) |completed| {
            self.compaction_just_ended = null;
            try self.sendPendingAfterCompaction(completed, resumed_run or self.state.status.streaming);
        }
        if (!completed_agent_end or self.state.queue.total() == 0) try self.drainQueuedWorktreeMessageIfIdle();
        if (run_ended and self.state.held_after_abort.items.len > 0) try self.applyPendingModelSwitchBeforeRun();
        if (run_ended) try self.sendHeldAfterAbort();
        if ((run_ended or run_failed) and !self.state.status.streaming) try self.applyPendingModelSwitchBeforeRun();
        try self.startCompactionAfterRun(run_ended or run_failed);
        try self.runDeferredAfterRun();
    }

    fn steerModelSwitch(self: *App, model_id: []const u8) !void {
        const runtime = self.runtime orelse return error.NoRuntimeConfigured;
        const model = runtime.requestModelSwitch(model_id) catch {
            const msg = try std.fmt.allocPrint(self.allocator, "no model named {s}", .{model_id});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
            return;
        };
        const msg = try std.fmt.allocPrint(self.allocator, "switching to {s}/{s} {s}", .{ model.provider, model.id, switchTiming(runtime) });
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    pub fn deferCommand(self: *App, text: []const u8) !void {
        const trimmed = std.mem.trim(u8, text, " \t\r\n");
        const owned = try self.allocator.dupe(u8, trimmed);
        errdefer self.allocator.free(owned);
        try self.deferred_commands.append(self.allocator, owned);
        const msg = try std.fmt.allocPrint(self.allocator, "{s} runs when this run ends", .{trimmed});
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn runDeferredAfterRun(self: *App) !void {
        const runtime = self.runtime orelse return;
        if (runtime.pending_model_index == null and self.deferred_commands.items.len == 0) return;
        if (self.state.status.streaming or self.state.status.compacting or !runtime.isIdle()) return;
        try self.applyPendingModelSwitchBeforeRun();
        if (self.state.queue.total() > 0) return;
        const deferred = try self.deferred_commands.toOwnedSlice(self.allocator);
        defer {
            for (deferred) |text| self.allocator.free(text);
            self.allocator.free(deferred);
        }
        for (deferred) |text| {
            self.submitCommand(text) catch |err| {
                const msg = try std.fmt.allocPrint(self.allocator, "{s} failed: {s}", .{ text, @errorName(err) });
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.@"error", msg);
            };
        }
    }

    fn applyPendingModelSwitchBeforeRun(self: *App) !void {
        const runtime = self.runtime orelse return;
        if (runtime.pending_model_index == null) return;
        if (runtime.local_agent) |*local| local.waitForIdle();
        if (!runtime.isIdle()) return;
        if (runtime.applyPendingModelSwitch()) |switched| {
            if (switched) |model| {
                try self.state.status.setModel(self.allocator, model.id, model.provider);
                self.applyContextWindow();
                self.persistCurrentModel();
                const msg = try std.fmt.allocPrint(self.allocator, "model switched to {s}/{s}", .{ model.provider, model.id });
                defer self.allocator.free(msg);
                try self.state.appendTranscript(.system, msg);
            }
        } else |err| {
            runtime.dropPendingModelSwitch();
            const msg = try std.fmt.allocPrint(self.allocator, "switching model failed: {s}", .{@errorName(err)});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
        }
    }

    fn dropHeldCompaction(self: *App) void {
        if (self.pending_compaction) |focus| self.allocator.free(focus);
        self.pending_compaction = null;
        if (self.session) |*session| {
            if (session.takeCompactionRequest(self.allocator) catch null) |steered| self.allocator.free(steered);
        }
    }

    fn steerCompaction(self: *App, focus: []const u8) !void {
        var session = &(self.session orelse return error.NoRuntimeConfigured);
        if (self.pending_compaction) |queued| self.allocator.free(queued);
        self.pending_compaction = null;
        if (try session.requestCompaction(focus)) {
            try self.state.appendTranscript(.system, "compacting before the next turn of this run, or when the run ends if no turn follows");
            return;
        }
        try self.queueCompaction(focus);
    }

    pub fn queueCompaction(self: *App, focus: []const u8) !void {
        const owned = try self.allocator.dupe(u8, focus);
        if (self.session) |*session| {
            if (session.takeCompactionRequest(self.allocator) catch null) |steered| self.allocator.free(steered);
        }
        if (self.pending_compaction) |previous| self.allocator.free(previous);
        self.pending_compaction = owned;
        try self.state.appendTranscript(.system, "compacting when this run ends");
    }

    fn startCompactionAfterRun(self: *App, run_ended: bool) !void {
        var session = &(self.session orelse return);
        if (run_ended) {
            if (try session.takeCompactionRequest(self.allocator)) |steered| {
                if (self.pending_compaction == null) self.pending_compaction = steered else self.allocator.free(steered);
            }
        }
        const focus = self.pending_compaction orelse return;
        if (self.state.status.streaming or self.state.status.compacting or self.state.queue.total() > 0) return;
        if (self.runtime) |runtime| {
            if (runtime.local_agent) |*local| {
                if (!local.isIdle()) return;
            }
        }
        self.pending_compaction = null;
        defer self.allocator.free(focus);
        self.startCompaction(focus) catch |err| {
            const msg = try std.fmt.allocPrint(self.allocator, "the held compaction could not start: {s}", .{@errorName(err)});
            defer self.allocator.free(msg);
            try self.state.appendTranscript(.@"error", msg);
        };
    }

    fn worktreeSetupRunning(self: *const App) bool {
        return self.worktree_job != null or self.worktree_management_job != null;
    }

    fn sendHeld(self: *App, typed: ?[]const u8) !bool {
        if (self.worktreeSetupRunning()) return true;
        const held = self.state.held_after_abort.items;
        var parts = std.ArrayList([]const u8).empty;
        defer parts.deinit(self.allocator);
        try parts.appendSlice(self.allocator, held);
        if (typed) |extra| try parts.append(self.allocator, extra);
        const text = try std.mem.join(self.allocator, "\n\n", parts.items);
        defer self.allocator.free(text);
        const echo = try std.mem.join(self.allocator, "\n\n", parts.items[@min(self.state.held_after_abort_echoed, held.len)..]);
        defer self.allocator.free(echo);
        const sent = if (try self.compactBeforeTurnEchoing(text, echo)) true else try self.sendUserTurnEchoing(text, echo);
        if (sent) self.state.clearHeldAfterAbort();
        return sent;
    }

    fn sendHeldAfterAbort(self: *App) !void {
        const held = self.state.held_after_abort.items;
        if (held.len == 0) return;
        const text = try std.mem.join(self.allocator, "\n\n", held);
        defer self.allocator.free(text);
        const sent = self.sendHeld(null) catch false;
        if (sent) return;
        if (self.state.composer.buffer.items.len > 0) {
            try self.state.appendTranscript(.@"error", "The queued messages could not be sent; they are kept and go out with your next message, or press esc to drop them.");
            return;
        }
        self.state.clearHeldAfterAbort();
        try self.state.replaceComposerBuffer(text);
        try self.state.appendTranscript(.@"error", "The queued messages could not be sent; they are back in the composer.");
    }

    fn applyRuntimeEvent(self: *App, event: tui_runtime.TuiEvent) !void {
        if (!self.replaying_history) {
            switch (event) {
                .@"error" => |payload| try self.rememberRunError(payload.message.slice()),
                .agent_end => |payload| switch (payload.reason) {
                    .@"error" => self.scheduleAutoContinue(),
                    .completed, .cancelled => self.runFinishedWithoutProviderError(),
                },
                else => {},
            }
        }
        switch (event) {
            .message_start => |payload| {
                if (payload.role == .user) return;
            },
            .message_end => |payload| {
                if (payload.role == .user) {
                    if (payload.steering) return;
                    try self.appendRuntimeUserMessage(payload.text.slice());
                    return;
                }
            },
            else => {},
        }
        try self.state.applyEvent(event);
    }

    fn appendRuntimeUserMessage(self: *App, text: []const u8) !void {
        const trimmed = std.mem.trim(u8, tui_state.withoutZenNote(std.mem.trim(u8, text, " \t\r\n")), " \t\r\n");
        if (trimmed.len == 0) return;
        if (self.runtime_echo_suppressed.len > 0 and std.mem.eql(u8, trimmed, self.runtime_echo_suppressed)) {
            self.allocator.free(self.runtime_echo_suppressed);
            self.runtime_echo_suppressed = &.{};
            return;
        }
        if (self.state.transcript.items.len > 0) {
            const last = &self.state.transcript.items[self.state.transcript.items.len - 1];
            if (last.kind == .user and std.mem.eql(u8, last.text.items, trimmed)) return;
        }
        try self.state.appendUserMessage(trimmed);
    }

    fn syncBackpressureState(self: *App) void {
        const runtime = self.runtime orelse return;
        const bp = runtime.backpressureState();
        self.state.backpressure_active = bp.active;
        self.state.dropped_event_count = bp.dropped_count;
    }

    fn refreshQueuedCounts(self: *App) void {
        if (self.session) |*session| {
            self.state.setQueuedCounts(session.queuedCounts());
        }
    }

    fn discardPendingEvents(self: *App) void {
        var session = &(self.session orelse return);
        while (session.popEvent()) |event| {
            var ev = event;
            defer ev.deinit(self.allocator);
            if (!self.quarantine_events and ev.generation() > self.quarantine_generation) {
                self.saveEvent(ev);
            }
        }
    }

    fn applyPendingSessionResetSync(self: *App) !void {
        if (!self.pending_session_reset) return;
        if (self.runtime) |runtime| {
            if (runtime.local_agent) |*local| {
                if (!local.isIdle()) return error.PendingSessionReset;
                self.dropHeldCompaction();
                local.clearAllQueues();
                local.replaceMessages(&.{}) catch {};
            }
        }
        self.pending_session_reset = false;
    }

    fn enqueueWorktreeMessage(self: *App, text: []const u8) !void {
        const queued = try self.allocator.dupe(u8, text);
        errdefer self.allocator.free(queued);
        try self.queued_worktree_messages.append(self.allocator, queued);
    }

    pub fn submit(self: *App, text: []const u8) !void {
        const trimmed = std.mem.trim(u8, text, " \t\r\n");
        if (trimmed.len == 0) return;
        self.state.transcript_scroll = 0;
        if (trimmed[0] == '/') return try self.submitCommand(trimmed);
        self.forgetRunError();
        self.userTookOver();
        if (self.state.held_after_abort.items.len > 0 and !self.worktreeSetupRunning()) {
            _ = try self.sendHeld(trimmed);
            return;
        }
        if (try self.compactBeforeTurn(trimmed)) return;
        try self.sendUserTurn(trimmed);
    }

    fn sendUserTurn(self: *App, trimmed: []const u8) !void {
        _ = try self.sendUserTurnEchoing(trimmed, trimmed);
    }

    fn zenNoted(self: *App, text: []const u8) !?[]u8 {
        const note = self.state.zen.noteText() orelse return null;
        return try std.fmt.allocPrint(self.allocator, "{s}\n\n{s}", .{ note, text });
    }

    fn sendUserTurnEchoing(self: *App, trimmed: []const u8, echo: []const u8) !bool {
        if (!self.state.status.streaming and !self.runtimeBusy()) try self.applyPendingModelSwitchBeforeRun();
        self.applyPendingSessionResetSync() catch |err| {
            if (err == error.PendingSessionReset) {
                try self.state.appendTranscript(.@"error", "Session reset pending; wait for the current run to finish.");
                return err;
            }
            return err;
        };
        try self.ensureSessionId();
        if (self.worktree_job != null or self.worktree_management_job != null) {
            if (self.held_user_message.len == 0) {
                self.held_user_message = try self.allocator.dupe(u8, trimmed);
                try self.state.appendNotice("Setting up this session's Git worktree; your message will be sent when ready.");
            } else {
                try self.enqueueWorktreeMessage(trimmed);
                try self.state.appendNotice("Worktree setup is still running; your message is queued and will be sent when ready.");
            }
            return true;
        }
        if (self.createsWorktree() and self.working_dir.len > 0 and self.worktree_job == null and self.worktree_management_job == null and !self.worktree_attempted and self.session_turns == 0) {
            const home = compat.getEnvVarOwned(self.allocator, "HOME") catch null;
            defer if (home) |value| self.allocator.free(value);
            if (home) |h| {
                const base = try std.fs.path.join(self.allocator, &.{ h, ".oapx", "worktrees" });
                defer self.allocator.free(base);
                if (tui_worktree.isUnderBase(self.working_dir, base)) {
                    self.worktree_attempted = true;
                    self.state.appendNotice("Already in a managed session worktree; continuing here.") catch {};
                } else {
                    const job = try tui_worktree.CreateJob.start(self.allocator, tui_worktree.processRunner(), self.working_dir, base, self.session_id);
                    errdefer job.deinit();
                    const held = try self.allocator.dupe(u8, trimmed);
                    errdefer self.allocator.free(held);
                    try self.state.appendNotice("Setting up an isolated Git worktree for this session…");
                    self.worktree_job = job;
                    self.held_user_message = held;
                    self.worktree_attempted = true;
                    return true;
                }
            }
        }
        self.state.stream_aborted = false;
        if (!self.state.status.streaming) {
            self.armAutoCompact();
            try self.giveRuntimeSessionId();
        }
        const noted = try self.zenNoted(trimmed);
        defer if (noted) |owned| self.allocator.free(owned);
        const sent = noted orelse trimmed;
        if (self.session) |*session| {
            session.submitTurn(sent) catch |err| {
                if (err == error.QueueFull) return err;
                try self.state.status.setError(self.allocator, @errorName(err));
                try self.state.appendTranscript(.@"error", @errorName(err));
                return false;
            };
            if (noted != null) self.state.zen.note = .none;
        }
        self.session_turns += 1;
        if (!std.mem.eql(u8, echo, sent)) {
            const suppressed = try self.allocator.dupe(u8, std.mem.trim(u8, tui_state.withoutZenNote(std.mem.trim(u8, sent, " \t\r\n")), " \t\r\n"));
            if (self.runtime_echo_suppressed.len > 0) self.allocator.free(self.runtime_echo_suppressed);
            self.runtime_echo_suppressed = suppressed;
        }
        if (echo.len > 0) try self.state.appendUserMessage(echo);
        self.refreshQueuedCounts();
        return true;
    }

    fn autoCompactThreshold(self: *const App) ?u64 {
        const runtime = self.runtime orelse return null;
        const model = runtime.currentModel() orelse return null;
        return tui_state.autoCompactAt(self.state.autocompact, model);
    }

    fn armAutoCompact(self: *App) void {
        const runtime = self.runtime orelse return;
        if (runtime.remote != null) {
            var buffer: [96]u8 = undefined;
            const policy = tui_state.autoCompactPolicyJson(&buffer, self.state.autocompact) catch return;
            runtime.setCompactionPolicy(policy) catch {};
            return;
        }
        runtime.armAutoCompact(self.autoCompactThreshold(), self.compaction_transcripts.items, .{ .ctx = self, .save_fn = saveRunTranscript }) catch {};
    }

    fn saveRunTranscript(ctx: ?*anyopaque, allocator: std.mem.Allocator, index: usize, history: []const ai_types.Message) ?[]u8 {
        const self: *App = @ptrCast(@alignCast(ctx.?));
        const store = self.store orelse return null;
        if (self.session_id.len == 0) return null;
        const saved = store.saveTranscript(self.session_id, index, history) catch return null;
        defer store.allocator.free(saved);
        return allocator.dupe(u8, saved) catch null;
    }

    fn contextWindowInEffect(self: *const App) u64 {
        const runtime = self.runtime orelse return 0;
        const model = runtime.currentModel() orelse return 0;
        return model.context_window;
    }

    fn estimatedTokensForTurn(self: *const App, history: []const ai_types.Message, text: []const u8) u64 {
        const message: ai_types.Message = .{ .user = .{
            .content = .{ .text = text },
            .timestamp = 0,
        } };
        const counted = @max(self.state.telemetry.estimated_tokens, agent.promptTokens(.{ .messages = history }));
        return counted + agent.estimateMessageTokens(message);
    }

    fn compactBeforeTurn(self: *App, text: []const u8) !bool {
        return self.compactBeforeTurnEchoing(text, text);
    }

    fn compactBeforeTurnEchoing(self: *App, text: []const u8, echo: []const u8) !bool {
        const runtime = self.runtime orelse return false;
        const model = runtime.currentModel() orelse return false;
        const at = tui_state.autoCompactAt(self.state.autocompact, model) orelse return false;
        if (self.pending_after_compaction != null) return false;
        if (self.state.status.streaming or self.state.status.compacting) return false;
        const history = if (self.session) |*session| session.history() else return false;
        if (history.len == 0 or agent.compaction.isCompacted(history)) return false;
        const tokens = self.estimatedTokensForTurn(history, text);
        if (tokens < at) return false;

        const pending = try self.allocator.dupe(u8, text);
        errdefer self.allocator.free(pending);
        const pending_echo = try self.allocator.dupe(u8, echo);
        errdefer self.allocator.free(pending_echo);
        const msg = try std.fmt.allocPrint(self.allocator, "context is at {d} of {d} tokens, past the {d} where it compacts; compacting before this turn.", .{ tokens, model.context_window, at });
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
        try self.startCompaction("");
        self.pending_after_compaction = pending;
        self.pending_after_compaction_echo = pending_echo;
        return true;
    }

    fn sendPendingAfterCompaction(self: *App, completed: bool, busy: bool) !void {
        const pending = self.pending_after_compaction orelse return;
        self.pending_after_compaction = null;
        defer self.allocator.free(pending);
        const echo = self.pending_after_compaction_echo;
        self.pending_after_compaction_echo = null;
        defer if (echo) |owned| self.allocator.free(owned);
        if (!completed) {
            try self.state.appendTranscript(.system, "the automatic compaction did not finish; sending the message with the history unchanged");
        }
        if (busy) {
            const shown = echo orelse pending;
            if (std.mem.eql(u8, shown, pending)) return try self.steer(pending);
            if (self.session) |*session| {
                const noted = try self.zenNoted(pending);
                defer if (noted) |owned| self.allocator.free(owned);
                try session.steer(noted orelse pending);
                if (noted != null) self.state.zen.note = .none;
                try self.state.appendSteeredMessageEchoing(pending, shown);
                self.refreshQueuedCounts();
            }
            return;
        }
        _ = try self.sendUserTurnEchoing(pending, echo orelse pending);
    }

    fn dropPendingAfterCompaction(self: *App, reason: []const u8) !void {
        const pending = self.pending_after_compaction orelse return;
        self.pending_after_compaction = null;
        defer self.allocator.free(pending);
        if (self.pending_after_compaction_echo) |echo| self.allocator.free(echo);
        self.pending_after_compaction_echo = null;
        const msg = try std.fmt.allocPrint(self.allocator, "a message waiting on an automatic compaction was not sent: {s}.", .{reason});
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    pub fn steer(self: *App, text: []const u8) !void {
        const trimmed = std.mem.trim(u8, text, " \t\r\n");
        if (trimmed.len == 0) return;
        if (trimmed[0] == '/') return try self.submitCommand(trimmed);
        self.userTookOver();
        self.applyPendingSessionResetSync() catch |err| {
            if (err == error.PendingSessionReset) {
                try self.state.appendTranscript(.@"error", "Session reset pending; wait for the current run to finish.");
                return err;
            }
            return err;
        };
        try self.ensureSessionId();
        if (self.session) |*session| {
            const noted = try self.zenNoted(trimmed);
            defer if (noted) |owned| self.allocator.free(owned);
            try session.steer(noted orelse trimmed);
            if (noted != null) self.state.zen.note = .none;
            try self.state.appendSteeredMessage(trimmed);
            self.refreshQueuedCounts();
            return;
        }
        try self.state.appendUserMessage(trimmed);
    }

    fn submitCommand(self: *App, text: []const u8) !void {
        var parsed = tui_commands.parseOrMessage(self.allocator, text) catch |err| {
            try self.state.status.setError(self.allocator, @errorName(err));
            try self.state.appendTranscript(.@"error", @errorName(err));
            return;
        };
        defer parsed.deinit(self.allocator);

        const command = switch (parsed) {
            .message => |message| {
                try self.state.status.setError(self.allocator, message);
                try self.state.appendTranscript(.@"error", message);
                return;
            },
            .command => |command| command,
        };

        if (self.state.status.streaming) {
            if (command.kind == .model and command.arg != null and !std.mem.eql(u8, command.arg.?, "refresh")) {
                try self.steerModelSwitch(command.arg.?);
                return;
            }
            if (waitsForRunEnd(command)) {
                try self.deferCommand(text);
                return;
            }
        }

        if (command.kind == .@"resume") self.loadSessions() catch |err| {
            try self.state.status.setError(self.allocator, @errorName(err));
            try self.state.appendTranscript(.@"error", @errorName(err));
            return;
        };

        const verbosity_before = self.state.verbosity;
        var result = tui_commands.dispatch(.{
            .allocator = self.allocator,
            .state = &self.state,
            .runtime = if (self.runtime) |runtime| runtime else null,
            .session = if (self.session) |*session| session else null,
        }, command) catch |err| {
            try self.state.status.setError(self.allocator, @errorName(err));
            try self.state.appendTranscript(.@"error", @errorName(err));
            return;
        };
        defer result.deinit(self.allocator);

        if (command.kind == .abort) {
            if (self.approval_waiter) |waiter| waiter.rejectPending();
            self.userTookOver();
        }

        switch (result.action) {
            .quit => return error.QuitRequested,
            .clear_transcript => {
                self.state.clearTranscript();
                self.state.clearTools();
                self.state.resetAgentCwd();
                self.inline_history_flushed = 0;
                self.inline_flushed_rows = 0;
                self.pending_clear_screen = true;
            },
            .open_session_picker => {
                try self.loadSessions();
                self.state.session_index = 0;
                self.state.session_scroll = 0;
                self.state.confirm_session_delete = false;
                if (self.state.confirm_session_force_delete) {
                    self.state.confirm_session_force_delete = false;
                    self.clearPendingDelete();
                }
                self.state.mode = .session_picker;
            },
            .open_model_picker => self.openPicker(.model),
            .open_login_picker => self.openPicker(.login),
            .open_permission_picker => self.openPicker(.permission),
            .open_settings_picker => self.openPicker(.settings),
            .start_login_provider => try self.startLoginProviderName(result.login_provider),
            .compact => try self.startCompaction(command.arg orelse ""),
            .compact_during_run => try self.steerCompaction(command.arg orelse ""),
            .rename_session => try self.renameSession(command.arg orelse ""),
            .refresh_models => try self.refreshModelsInBackground(),
            .logout_provider => try self.logoutProvider(command.arg orelse ""),
            .add_provider => try self.addProvider(command.arg orelse ""),
            .show_status => {
                const report = try self.statusReport(self.allocator);
                defer self.allocator.free(report);
                try self.state.appendTranscript(.system, report);
            },
            .redraw => self.requestRedraw(),
            .remove_provider => try self.removeProvider(command.arg orelse ""),
            .list_providers => try self.listProviders(),
            .none => {},
        }
        if (command.kind == .model and command.arg != null and result.action != .refresh_models) self.persistCurrentModel();
        switch (command.kind) {
            .context, .model => self.applyContextWindow(),
            else => {},
        }
        if (command.kind == .context and !result.is_error and command.arg != null) self.persistContextWindow();
        if (command.kind == .output and !result.is_error and command.arg != null) self.persistOutput();
        if (command.kind == .autocompact and !result.is_error and command.arg != null) self.persistAutoCompact();
        if (command.kind == .verbose and !result.is_error and command.arg != null) {
            self.persistVerbosity();
            try self.redrawAfterVerbosity(verbosity_before);
        }
        if (command.kind == .think and !result.is_error and command.arg != null) self.persistThinkingLevel();
        if (result.output.len > 0) {
            try self.state.appendTranscript(if (result.is_error) .@"error" else .system, result.output);
            if (result.is_error) try self.state.status.setError(self.allocator, result.output);
        }
    }

    pub fn recordError(self: *App, message: []const u8) !void {
        if (std.mem.eql(u8, message, "QueueFull")) {
            try self.state.status.setError(self.allocator, "stream backlog; draining");
            return;
        }
        try self.state.status.setError(self.allocator, message);
        try self.state.appendTranscript(.@"error", message);
    }

    fn noteDroppedContextWindow(self: *App) !void {
        const runtime = self.runtime orelse return;
        const refused = runtime.takeContextWindowRefused() orelse return;
        const model = runtime.currentModel() orelse return;
        self.applyContextWindow();
        const msg = try std.fmt.allocPrint(self.allocator, "{d} context tokens is above the {d} {s} takes, so the model's own {d} is in effect.", .{ refused, runtime.contextWindowMaximum() orelse 0, model.id, model.context_window });
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
    }

    fn applyContextWindow(self: *App) void {
        const runtime = self.runtime orelse return;
        const window = runtime.contextWindow();
        self.state.status.context_limit = @intCast(window);
        self.state.telemetry.context_window = window;
    }

    fn toggleSetting(self: *App, index: usize) !void {
        if (index == 0) {
            self.mode_settings.compact_output = !self.mode_settings.compact_output;
            if (self.runtime) |runtime| runtime.setCompactOutput(self.mode_settings.compact_output);
        } else {
            self.mode_settings.auto_worktree = !self.mode_settings.auto_worktree;
        }
        var store = try tui_config.Store.initDefault(self.allocator);
        defer store.deinit();
        var cfg = try store.load();
        defer cfg.deinit(self.allocator);
        var mode = self.mode_settings;
        mode.context_window = cfg.mode.context_window;
        mode.output = cfg.mode.output;
        cfg.mode = mode;
        try store.save(cfg);
    }

    fn persistCurrentModel(self: *App) void {
        const runtime = self.runtime orelse return;
        const model = runtime.currentModel() orelse return;
        self.persistSelectedModel(model) catch |err| self.recordError(@errorName(err)) catch {};
    }

    fn persistSelectedModel(self: *App, model: ai_types.Model) !void {
        var store = try tui_config.Store.initDefault(self.allocator);
        defer store.deinit();
        var cfg = try store.load();
        defer cfg.deinit(self.allocator);

        try replaceOwnedString(self.allocator, &cfg.model, model.id);
        try replaceOwnedString(self.allocator, &cfg.provider, model.provider);
        try replaceOwnedString(self.allocator, &cfg.api, model.api);
        try store.save(cfg);
    }

    fn persistContextWindow(self: *App) void {
        const runtime = self.runtime orelse return;
        var store = tui_config.Store.initDefault(self.allocator) catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer store.deinit();
        var cfg = store.load() catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer cfg.deinit(self.allocator);
        const window = runtime.contextWindowOverride();
        self.mode_settings.context_window = window;
        if (cfg.mode.context_window == window) return;
        cfg.mode.context_window = window;
        store.save(cfg) catch |err| self.recordError(@errorName(err)) catch {};
    }

    fn persistOutput(self: *App) void {
        const runtime = self.runtime orelse return;
        const setting = savedOutput(runtime.outputSetting());
        self.mode_settings.output = setting;
        var store = tui_config.Store.initDefault(self.allocator) catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer store.deinit();
        var cfg = store.load() catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer cfg.deinit(self.allocator);
        if (std.meta.eql(cfg.mode.output, setting)) return;
        cfg.mode.output = setting;
        store.save(cfg) catch |err| self.recordError(@errorName(err)) catch {};
    }

    fn persistAutoCompact(self: *App) void {
        self.mode_settings.autocompact = self.state.autocompact;
        var store = tui_config.Store.initDefault(self.allocator) catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer store.deinit();
        var cfg = store.load() catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer cfg.deinit(self.allocator);
        if (std.meta.eql(cfg.mode.autocompact, self.state.autocompact)) return;
        cfg.mode.autocompact = self.state.autocompact;
        store.save(cfg) catch |err| self.recordError(@errorName(err)) catch {};
    }

    fn requestRedraw(self: *App) void {
        self.inline_history_flushed = 0;
        self.inline_flushed_rows = 0;
        self.pending_clear_screen = true;
    }

    fn redrawAfterVerbosity(self: *App, before: tui_state.Verbosity) !void {
        if (before.transcriptEquals(self.state.verbosity)) return;
        if (!self.state.status.streaming and !self.runtimeBusy() and !terminalKeepsScrollback()) {
            self.requestRedraw();
            return;
        }
        try self.state.appendTranscript(.system, "earlier rows keep their old verbosity; run /redraw to reprint them");
    }

    fn terminalKeepsScrollback() bool {
        for ([_][]const u8{ "TMUX", "STY" }) |name| {
            const value = compat.getEnvVarOwned(std.heap.page_allocator, name) catch continue;
            defer std.heap.page_allocator.free(value);
            if (value.len > 0) return true;
        }
        return false;
    }

    pub fn cycleVerbosity(self: *App) !void {
        const before = self.state.verbosity;
        self.state.verbosity = before.cycled();
        self.persistVerbosity();
        const msg = try std.fmt.allocPrint(self.allocator, "verbosity: {t} (ctrl+o cycles)", .{self.state.verbosity.thinking});
        defer self.allocator.free(msg);
        try self.state.appendTranscript(.system, msg);
        try self.redrawAfterVerbosity(before);
    }

    fn persistVerbosity(self: *App) void {
        self.mode_settings.verbosity = self.state.verbosity;
        var store = tui_config.Store.initDefault(self.allocator) catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer store.deinit();
        var cfg = store.load() catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        defer cfg.deinit(self.allocator);
        if (std.meta.eql(cfg.mode.verbosity, self.state.verbosity)) return;
        cfg.mode.verbosity = self.state.verbosity;
        store.save(cfg) catch |err| self.recordError(@errorName(err)) catch {};
    }

    fn replaceOwnedString(allocator: std.mem.Allocator, field: *[]u8, value: []const u8) !void {
        const next = try allocator.dupe(u8, value);
        allocator.free(field.*);
        field.* = next;
    }

    fn setResumeWithoutWorktree(self: *App, id: []const u8) !void {
        try replaceOwnedString(self.allocator, &self.resume_without_worktree_id, id);
    }

    fn stageClipboard(self: *App, text: []const u8) void {
        const dup = self.allocator.dupe(u8, text) catch return;
        if (self.pending_clipboard) |old| self.allocator.free(old);
        self.pending_clipboard = dup;
    }

    fn flushClipboard(self: *App, ctx: *zz.Context) void {
        const text = self.pending_clipboard orelse return;
        self.pending_clipboard = null;
        defer self.allocator.free(text);
        const copied = ctx.setClipboard(text) catch |err| {
            self.recordError(@errorName(err)) catch {};
            return;
        };
        if (!copied) self.recordError("clipboard unavailable") catch {};
    }

    fn copyLastAssistant(self: *App) void {
        const text = self.state.lastAssistantText() orelse {
            self.state.appendTranscript(.system, "nothing to copy yet") catch {};
            return;
        };
        self.stageClipboard(text);
        self.state.appendNotice("copied last reply to clipboard") catch {};
    }

    fn createsWorktree(self: *const App) bool {
        if (!self.mode_settings.auto_worktree) return false;
        const runtime = self.runtime orelse return true;
        return runtime.movesWorkspaceLive();
    }

    fn cycleThinkingLevel(self: *App) void {
        const previous = self.state.thinking_level;
        const level = self.state.cycleThinkingLevel();
        if (self.runtime) |runtime| runtime.setThinkingLevel(level) catch |err| {
            self.state.thinking_level = previous;
            self.state.appendTranscript(.@"error", if (err == error.RunInProgress) tui_commands.between_runs_refusal else over_oap_setting_refusal) catch {};
            return;
        };
        self.persistThinkingLevel();
    }

    fn persistThinkingLevel(self: *App) void {
        if (!self.session_written) return;
        if (self.store) |store| self.saveSessionIndex(store);
    }

    pub fn refreshCwdDisplay(self: *App) !void {
        if (self.working_dir.len == 0) {
            try self.state.setCwdDisplay(self.allocator, "");
            try self.state.setGitBranch(self.allocator, "");
            return;
        }
        const sanitized = try tui_text.sanitizeTerminalText(self.allocator, self.working_dir);
        defer self.allocator.free(sanitized);
        const home = compat.getEnvVarOwned(self.allocator, "HOME") catch null;
        defer if (home) |value| self.allocator.free(value);
        const display = try collapseHome(self.allocator, sanitized, home);
        defer self.allocator.free(display);
        try self.state.setCwdDisplay(self.allocator, display);
        try self.state.setSessionRoot(self.allocator, self.working_dir);
        try self.refreshGitBranch();
    }

    pub fn refreshGitBranch(self: *App) !void {
        const raw = try gitHeadLabel(self.allocator, self.working_dir);
        defer if (raw) |value| self.allocator.free(value);
        const sanitized = if (raw) |value|
            try tui_text.sanitizeTerminalText(self.allocator, value)
        else
            try self.allocator.dupe(u8, "");
        defer self.allocator.free(sanitized);
        try self.state.setGitBranch(self.allocator, sanitized);
        if (!std.mem.eql(u8, self.branch_dir, self.working_dir)) {
            const owned = try self.allocator.dupe(u8, self.working_dir);
            if (self.branch_dir.len > 0) self.allocator.free(self.branch_dir);
            self.branch_dir = owned;
        }
    }

    pub fn refreshBranchOnSlowTick(self: *App) !void {
        if (!std.mem.eql(u8, self.branch_dir, self.working_dir)) {
            try self.refreshCwdDisplay();
            return;
        }
        try self.refreshGitBranch();
    }

    pub fn appendWelcome(self: *App) !void {
        const model = try tui_text.sanitizeTerminalText(self.allocator, if (self.state.status.model.len > 0) self.state.status.model else "no-model");
        defer self.allocator.free(model);
        const provider = try tui_text.sanitizeTerminalText(self.allocator, if (self.state.status.provider.len > 0) self.state.status.provider else "local");
        defer self.allocator.free(provider);
        const cwd = try tui_text.sanitizeTerminalText(self.allocator, if (self.working_dir.len > 0) self.working_dir else ".");
        defer self.allocator.free(cwd);
        const k = tui_theme.key;
        if (self.state.sessions.items.len == 0) {
            const welcome = try std.fmt.allocPrint(self.allocator,
                \\Makai TUI
                \\model: {s}/{s}
                \\cwd: {s}
                \\tips: {s} send {s} {s}{s} newline {s} {s}{s} thinking {s} {s}Y copy reply
                \\! asks the agent to run a command {s} /help lists every command and key
            , .{ provider, model, cwd, k.enter, tui_theme.glyph.dot, k.shift, k.enter, tui_theme.glyph.dot, k.shift, k.tab, tui_theme.glyph.dot, k.ctrl, tui_theme.glyph.dot });
            defer self.allocator.free(welcome);
            try self.state.appendTranscript(.welcome, welcome);
            return;
        }
        const welcome = try std.fmt.allocPrint(self.allocator,
            \\Makai TUI
            \\model: {s}/{s}
            \\cwd: {s}
            \\sessions: {d} saved {s} /resume to continue one
        , .{ provider, model, cwd, self.state.sessions.items.len, tui_theme.glyph.dot });
        defer self.allocator.free(welcome);
        try self.state.appendTranscript(.welcome, welcome);
    }

    fn syncModelTelemetry(self: *App) void {
        const runtime = self.runtime orelse return;
        const model = runtime.currentModel() orelse return;
        self.state.telemetry.input_cost_per_million = model.cost.input;
        if (self.rate_model.len > 0 and
            std.mem.eql(u8, self.rate_model, model.id) and
            std.mem.eql(u8, self.rate_provider, model.provider)) return;
        self.adoptRateModel(model.id, model.provider);
    }

    fn adoptRateModel(self: *App, id: []const u8, provider: []const u8) void {
        const next_model = self.allocator.dupe(u8, id) catch return;
        const next_provider = self.allocator.dupe(u8, provider) catch {
            self.allocator.free(next_model);
            return;
        };
        if (self.rate_model.len > 0) {
            self.state.telemetry.rate.resetForModel();
            self.allocator.free(self.rate_model);
        }
        if (self.rate_provider.len > 0) self.allocator.free(self.rate_provider);
        self.rate_model = next_model;
        self.rate_provider = next_provider;
    }

    pub fn slashQuery(self: *const App) ?[]const u8 {
        const text = self.state.composer.text();
        if (text.len == 0 or text[0] != '/') return null;
        if (std.mem.indexOfAny(u8, text, " \t\n") != null) return null;
        return text[1..];
    }

    pub fn slashSelection(self: *const App) usize {
        const query = self.slashQuery() orelse return 0;
        if (self.slash_index_query != std.hash.Wyhash.hash(0, query)) return 0;
        const count = slashMatchCount(query);
        if (count == 0) return 0;
        return @min(self.slash_index, count - 1);
    }

    pub fn moveSlashSelection(self: *App, delta: isize) bool {
        if (self.state.composer.history_index != null) return false;
        const query = self.slashQuery() orelse return false;
        const count = slashMatchCount(query);
        if (count == 0) return false;
        const current = self.slashSelection();
        self.slash_index = if (delta < 0) current -| @as(usize, @intCast(-delta)) else @min(count - 1, current + @as(usize, @intCast(delta)));
        self.slash_index_query = std.hash.Wyhash.hash(0, query);
        return true;
    }

    pub fn handleComposerVertical(self: *App, width: usize, delta: isize) !void {
        const composer = &self.state.composer;
        if (composer.history_index) |index| {
            if (index < composer.history.items.len and std.mem.eql(u8, composer.text(), composer.history.items[index])) {
                _ = if (delta < 0) try self.state.composerHistoryPrev() else try self.state.composerHistoryNext();
                return;
            }
        }
        if (try self.moveComposerVisualRow(width, delta)) return;
        _ = if (delta < 0) try self.state.composerHistoryPrev() else try self.state.composerHistoryNext();
    }

    fn moveComposerVisualRow(self: *App, width: usize, delta: isize) !bool {
        const composer = &self.state.composer;
        if (composer.text().len == 0) return false;
        const content_width = composer_view.contentWidth(width);
        const rows = try tui_text.layoutRows(self.allocator, composer.text(), content_width);
        defer self.allocator.free(rows);
        const pos = tui_text.cursorPos(rows, composer.text(), composer.cursor, content_width);
        const target = @as(isize, @intCast(pos.row)) + delta;
        if (target < 0 or target >= @as(isize, @intCast(rows.len))) return false;
        const goal = composer.goal_column orelse pos.col;
        composer.goal_column = goal;
        composer.cursor = tui_text.byteOffsetAtColumn(rows, composer.text(), @intCast(target), goal);
        return true;
    }

    pub fn completeSlashCommand(self: *App) !bool {
        const query = self.slashQuery() orelse return false;
        const info = slashMatch(query, self.slashSelection()) orelse return false;
        const has_args = std.mem.indexOfScalar(u8, info.usage, ' ') != null;
        const completed = try std.fmt.allocPrint(self.allocator, "/{s}{s}", .{ info.name, if (has_args) " " else "" });
        defer self.allocator.free(completed);
        try self.state.replaceComposerBuffer(completed);
        return true;
    }

    pub fn selectSlashCommand(self: *App) !void {
        const query = self.slashQuery() orelse return;
        const info = slashMatch(query, self.slashSelection()) orelse return;
        if (std.mem.eql(u8, query, info.name)) return;
        const command = try std.fmt.allocPrint(self.allocator, "/{s}", .{info.name});
        defer self.allocator.free(command);
        try self.state.replaceComposerBuffer(command);
    }

    pub fn queueFollowUp(self: *App, text: []const u8) !bool {
        const trimmed = std.mem.trim(u8, text, " \t\r\n");
        if (trimmed.len == 0 or isSlashDraft(trimmed)) return false;
        self.applyPendingSessionResetSync() catch |err| {
            if (err == error.PendingSessionReset) try self.state.appendTranscript(.@"error", "Session reset pending; wait for the current run to finish.");
            return err;
        };
        try self.ensureSessionId();
        self.userTookOver();
        if (self.session) |*session| {
            const noted = try self.zenNoted(trimmed);
            defer if (noted) |owned| self.allocator.free(owned);
            try session.followUp(noted orelse trimmed);
            if (noted != null) self.state.zen.note = .none;
            try self.state.appendQueuedFollowUp(trimmed);
            self.refreshQueuedCounts();
            return true;
        }
        return false;
    }

    pub fn decideApproval(self: *App, approved: bool, always: bool) !void {
        const id = self.state.approval.tool_call_id;
        const decision: tui_runtime.ToolApprovalDecision = if (approved) if (always) .approve_always else .approve else if (always) .reject_always else .reject;
        var matched_waiter = false;
        if (self.approval_waiter) |waiter| {
            while (!waiter.mutex.tryLock()) std.atomic.spinLoopHint();
            defer waiter.mutex.unlock();
            if (waiter.tool_call_id.len > 0 and (id.len == 0 or std.mem.eql(u8, waiter.tool_call_id, id))) {
                waiter.decision = decision;
                matched_waiter = true;
            }
        }
        if (!matched_waiter) {
            if (self.session) |*session| {
                if (id.len > 0) try session.decideToolApproval(id, decision);
            }
        }
        self.state.setApprovalDecision(approved, always);
    }
};

fn approvalCallback(ctx: ?*anyopaque, request: tui_runtime.ToolApprovalRequest) tui_runtime.ToolApprovalDecision {
    const waiter: *ApprovalWaiter = @ptrCast(@alignCast(ctx.?));
    while (!waiter.mutex.tryLock()) std.atomic.spinLoopHint();
    if (waiter.tool_call_id.len > 0) waiter.allocator.free(waiter.tool_call_id);
    waiter.tool_call_id = waiter.allocator.dupe(u8, request.tool_call_id) catch {
        waiter.mutex.unlock();
        return .approve;
    };
    waiter.decision = null;
    waiter.mutex.unlock();
    while (true) {
        while (!waiter.mutex.tryLock()) std.atomic.spinLoopHint();
        const decision = waiter.decision;
        const shutting_down = waiter.shutting_down;
        waiter.mutex.unlock();
        if (decision) |value| {
            while (!waiter.mutex.tryLock()) std.atomic.spinLoopHint();
            if (waiter.tool_call_id.len > 0) waiter.allocator.free(waiter.tool_call_id);
            waiter.tool_call_id = &.{};
            waiter.decision = null;
            waiter.mutex.unlock();
            return value;
        }
        if (shutting_down) return .reject;
        compat.time.sleepNs(1 * std.time.ns_per_ms);
    }
}

fn encodeHexLower(out: []u8, bytes: []const u8) void {
    const alphabet = "0123456789abcdef";
    for (bytes, 0..) |byte, i| {
        out[i * 2] = alphabet[byte >> 4];
        out[i * 2 + 1] = alphabet[byte & 0x0f];
    }
}

pub const RenderMode = enum {
    auto,
    inline_history,
    full_transcript,
};

pub const TuiModel = struct {
    app: ?App = null,
    options: tui_runtime.TuiRuntimeOptions = .{},
    autocompact: tui_config.AutoCompact = .auto,
    verbosity: tui_config.Verbosity = .{},
    render_mode: RenderMode = .auto,
    wheel_captured: bool = false,
    cmds: [3]zz.Cmd(Msg) = .{ .none, .none, .none },
    tab_title: [tab_title_bytes]u8 = undefined,
    tab_title_len: usize = 0,

    pub const Msg = union(enum) {
        key: zz.KeyEvent,
        mouse: zz.MouseEvent,
        tick: struct { timestamp: u64, delta: u64 },
        window_size: struct { width: u16, height: u16 },
        resumed: void,
        quit: void,
    };

    pub fn init(self: *TuiModel, ctx: *zz.Context) zz.Cmd(Msg) {
        self.deinit();
        self.app = App.init(ctx.persistent_allocator, self.options) catch |err| blk: {
            var fallback = App.initWithoutRuntime(ctx.persistent_allocator);
            fallback.state.status.setError(ctx.persistent_allocator, @errorName(err)) catch {};
            fallback.state.appendTranscript(.@"error", @errorName(err)) catch {};
            break :blk fallback;
        };
        if (self.app) |*app| {
            app.state.autocompact = self.autocompact;
            app.mode_settings.autocompact = self.autocompact;
            app.state.verbosity = self.verbosity;
            app.mode_settings.verbosity = self.verbosity;
            app.start() catch |err| {
                app.state.status.setError(app.allocator, @errorName(err)) catch {};
                app.state.appendTranscript(.@"error", @errorName(err)) catch {};
            };
            app.appendWelcome() catch |err| app.recordError(@errorName(err)) catch {};
            app.warnOnUnreadableCustomProviders();
        }
        return .{ .every = 50 * std.time.ns_per_ms };
    }

    pub fn deinit(self: *TuiModel) void {
        if (self.app) |*app| app.deinit();
        self.app = null;
    }

    const tab_title_bytes: usize = 128;

    pub fn update(self: *TuiModel, msg: Msg, ctx: *zz.Context) zz.Cmd(Msg) {
        if (msg == .resumed) {
            self.wheel_captured = false;
            self.tab_title_len = 0;
        }
        const cmd = self.step(msg, ctx);
        var count: usize = 0;
        const zen_on = if (self.app) |*app| app.state.zen.on else false;
        if (zen_on != self.wheel_captured) {
            self.wheel_captured = zen_on;
            self.cmds[count] = if (zen_on) .enable_mouse else .disable_mouse;
            count += 1;
        }
        const quitting = std.meta.activeTag(cmd) == .quit;
        const title = if (quitting) "" else if (self.app) |*app| app.state.session_title else "";
        if (self.retitle(title)) |set| {
            self.cmds[count] = .{ .set_title = set };
            count += 1;
        }
        if (count == 0) return cmd;
        self.cmds[count] = cmd;
        return .{ .batch = self.cmds[0 .. count + 1] };
    }

    fn retitle(self: *TuiModel, title: []const u8) ?[]const u8 {
        var buffer: [tab_title_bytes]u8 = undefined;
        const wanted = tabTitle(&buffer, title);
        if (std.mem.eql(u8, wanted, self.tab_title[0..self.tab_title_len])) return null;
        @memcpy(self.tab_title[0..wanted.len], wanted);
        self.tab_title_len = wanted.len;
        return self.tab_title[0..self.tab_title_len];
    }

    fn tabTitle(buffer: []u8, title: []const u8) []const u8 {
        if (title.len == 0) return "";
        const prefix = "oapx \u{b7} ";
        @memcpy(buffer[0..prefix.len], prefix);
        var end = @min(title.len, buffer.len - prefix.len);
        while (end < title.len and end > 0 and (title[end] & 0xC0) == 0x80) end -= 1;
        @memcpy(buffer[prefix.len .. prefix.len + end], title[0..end]);
        return buffer[0 .. prefix.len + end];
    }

    fn step(self: *TuiModel, msg: Msg, ctx: *zz.Context) zz.Cmd(Msg) {
        const app = &(self.app orelse return .none);
        switch (msg) {
            .key => |key| {
                if (key.key != .up and key.key != .down) app.state.composer.goal_column = null;
                if (key.modifiers.ctrl) switch (key.key) {
                    .char => |c| switch (c) {
                        'c' => return self.handleInterrupt(app, ctx),
                        'd' => {
                            if (app.state.composer.buffer.items.len == 0 and app.state.mode == .normal and !app.state.status.streaming) return self.quitCmd(app, ctx);
                            return .none;
                        },
                        'o' => {
                            app.cycleVerbosity() catch |err| app.recordError(@errorName(err)) catch {};
                            if (app.pending_clear_screen) {
                                app.pending_clear_screen = false;
                                if (self.inlineMode(ctx)) ctx.requestClearScreen();
                            }
                            return .none;
                        },
                        'y' => {
                            app.copyLastAssistant();
                            app.flushClipboard(ctx);
                            return .none;
                        },
                        'u' => {
                            _ = app.state.composer.deleteToLineStart();
                            return .none;
                        },
                        'k' => {
                            _ = app.state.composer.deleteToLineEnd();
                            return .none;
                        },
                        'w' => {
                            _ = app.state.composer.deleteWordBeforeCursor();
                            return .none;
                        },
                        'a' => {
                            app.state.composer.moveCursorHome();
                            return .none;
                        },
                        'e' => {
                            app.state.composer.moveCursorEnd();
                            return .none;
                        },
                        else => return .none,
                    },
                    .left => {
                        app.state.composer.moveCursorWordPrev();
                        return .none;
                    },
                    .right => {
                        app.state.composer.moveCursorWordNext();
                        return .none;
                    },
                    else => {},
                };
                if (key.modifiers.alt) switch (key.key) {
                    .left => {
                        app.state.composer.moveCursorWordPrev();
                        return .none;
                    },
                    .right => {
                        app.state.composer.moveCursorWordNext();
                        return .none;
                    },
                    .backspace => {
                        _ = app.state.composer.deleteWordBeforeCursor();
                        return .none;
                    },
                    .char => |c| switch (c) {
                        'b' => {
                            app.state.composer.moveCursorWordPrev();
                            return .none;
                        },
                        'f' => {
                            app.state.composer.moveCursorWordNext();
                            return .none;
                        },
                        else => {},
                    },
                    else => {},
                };
                app.interrupt_armed_tick = null;
                if (key.key == .tab and key.modifiers.eql(.{ .shift = true })) {
                    app.cycleThinkingLevel();
                    return .none;
                }
                if (app.state.mode == .approval) {
                    const composer_empty = app.state.composer.buffer.items.len == 0;
                    if (composer_empty) {
                        var decided = false;
                        switch (key.key) {
                            .char => |c| switch (c) {
                                'y' => {
                                    app.decideApproval(true, false) catch |err| app.recordError(@errorName(err)) catch {};
                                    decided = true;
                                },
                                'a' => {
                                    app.decideApproval(true, true) catch |err| app.recordError(@errorName(err)) catch {};
                                    decided = true;
                                },
                                'n' => {
                                    app.decideApproval(false, false) catch |err| app.recordError(@errorName(err)) catch {};
                                    decided = true;
                                },
                                else => {},
                            },
                            .escape => {
                                app.decideApproval(false, false) catch |err| app.recordError(@errorName(err)) catch {};
                                app.userTookOver();
                                decided = true;
                            },
                            else => {},
                        }
                        if (decided) return .none;
                    } else if (key.key == .escape) {
                        app.state.composer.clear();
                        return .none;
                    }
                }
                if (app.state.mode == .session_picker) {
                    if (app.state.confirm_session_delete) {
                        switch (key.key) {
                            .char => |c| {
                                if (c == 'y' or c == 'Y') {
                                    app.deleteSelectedSession() catch |err| app.recordError(@errorName(err)) catch {};
                                    app.state.confirm_session_delete = false;
                                } else if (c == 'n' or c == 'N') {
                                    app.state.confirm_session_delete = false;
                                }
                            },
                            .enter => {
                                app.deleteSelectedSession() catch |err| app.recordError(@errorName(err)) catch {};
                                app.state.confirm_session_delete = false;
                            },
                            .escape => app.state.confirm_session_delete = false,
                            else => {},
                        }
                        return .none;
                    }
                    if (app.state.confirm_session_force_delete) {
                        switch (key.key) {
                            .char => |c| {
                                if (c == 'y' or c == 'Y') {
                                    app.state.confirm_session_force_delete = false;
                                    app.forceDeletePendingSession() catch |err| app.recordError(@errorName(err)) catch {};
                                } else if (c == 'n' or c == 'N') {
                                    app.cancelForceDeletePrompt() catch |err| app.recordError(@errorName(err)) catch {};
                                    app.state.appendTranscript(.system, "Kept the session worktree; the session was not deleted.") catch |err| app.recordError(@errorName(err)) catch {};
                                }
                            },
                            .enter => {
                                app.state.confirm_session_force_delete = false;
                                app.forceDeletePendingSession() catch |err| app.recordError(@errorName(err)) catch {};
                            },
                            .escape => {
                                app.cancelForceDeletePrompt() catch |err| app.recordError(@errorName(err)) catch {};
                                app.state.appendTranscript(.system, "Kept the session worktree; the session was not deleted.") catch |err| app.recordError(@errorName(err)) catch {};
                            },
                            else => {},
                        }
                        return .none;
                    }
                    switch (key.key) {
                        .up => moveSessionSelection(app, -1),
                        .down => moveSessionSelection(app, 1),
                        .char => |c| switch (c) {
                            'k' => moveSessionSelection(app, -1),
                            'j' => moveSessionSelection(app, 1),
                            'd' => app.state.confirm_session_delete = true,
                            else => {},
                        },
                        .enter => {
                            app.resumeSelectedSession() catch |err| app.recordError(@errorName(err)) catch {};
                        },
                        .escape => closeModal(app),
                        else => {},
                    }
                    return .none;
                }
                if (app.state.mode == .login_input) {
                    switch (key.key) {
                        .enter => {
                            const text = app.state.composer.text();
                            app.submitLoginInput(text);
                            app.state.composer.clear();
                        },
                        .escape => {
                            app.cancelLogin();
                            app.userTookOver();
                        },
                        .backspace => _ = app.state.composer.deleteBeforeCursor(),
                        .char => |c| appendChar(app, c) catch {},
                        .paste => |text| app.state.composer.insertPaste(app.allocator, text) catch {},
                        .space => app.state.composer.insertSlice(app.allocator, " ") catch {},
                        .left => _ = app.state.composer.moveCursorPrev(),
                        .right => _ = app.state.composer.moveCursorNext(),
                        .home => app.state.composer.moveCursorHome(),
                        .end => app.state.composer.moveCursorEnd(),
                        else => {},
                    }
                    return .none;
                }
                if (app.state.mode == .picker) {
                    switch (key.key) {
                        .up => app.moveMenuSelection(-1),
                        .down => app.moveMenuSelection(1),
                        .page_up => app.moveMenuSelection(-@as(isize, @intCast(sessionPickerHeight(app)))),
                        .page_down => app.moveMenuSelection(@intCast(sessionPickerHeight(app))),
                        .char => |c| appendPickerChar(app, c) catch {},
                        .space => app.state.appendPickerFilter(" ") catch {},
                        .paste => |text| app.state.appendPickerFilter(text) catch {},
                        .backspace => _ = app.state.popPickerFilter(),
                        .enter => switch (app.state.picker_kind) {
                            .model => app.applySelectedModel() catch |err| app.recordError(@errorName(err)) catch {},
                            .login => app.applySelectedLogin() catch |err| app.recordError(@errorName(err)) catch {},
                            .permission => app.applySelectedPermission() catch |err| app.recordError(@errorName(err)) catch {},
                            .settings => if (app.pickerSourceIndex(app.state.menu_index)) |index| app.toggleSetting(index) catch |err| app.recordError(@errorName(err)) catch {},
                        },
                        .escape => closeModal(app),
                        else => {},
                    }
                    return .none;
                }
                switch (key.key) {
                    .enter => {
                        if (key.modifiers.shift) {
                            app.state.composer.insertSlice(app.allocator, "\n") catch |err| app.recordError(@errorName(err)) catch {};
                            return .none;
                        }
                        app.drainEvents() catch |err| {
                            app.state.status.setError(app.allocator, @errorName(err)) catch {};
                            app.state.appendTranscript(.@"error", @errorName(err)) catch {};
                        };
                        if (app.state.mode == .normal) app.selectSlashCommand() catch |err| app.recordError(@errorName(err)) catch {};
                        const text = app.state.composer.text();
                        if (app.state.mode == .approval) {
                            const command = tui_commands.parse(text) catch return .none;
                            if (command.kind != .abort) return .none;
                        }
                        var consumed = true;
                        if (app.state.mode == .approval) {
                            app.submit(text) catch |err| {
                                if (err == error.QuitRequested) return self.quitCmd(app, ctx);
                                if (err == error.QueueFull or err == error.PendingSessionReset) consumed = false;
                                if (err == error.PendingSessionReset) return .none;
                                app.state.status.setError(app.allocator, @errorName(err)) catch {};
                                if (err != error.QueueFull) app.state.appendTranscript(.@"error", @errorName(err)) catch {};
                            };
                        } else if (app.state.status.streaming) {
                            app.steer(text) catch |err| {
                                if (err == error.QuitRequested) return self.quitCmd(app, ctx);
                                if (err == error.QueueFull or err == error.PendingSessionReset) consumed = false;
                                if (err == error.PendingSessionReset) return .none;
                                app.recordError(@errorName(err)) catch {};
                            };
                        } else {
                            app.submit(text) catch |err| {
                                if (err == error.QuitRequested) return self.quitCmd(app, ctx);
                                if (err == error.QueueFull or err == error.PendingSessionReset) consumed = false;
                                if (err == error.PendingSessionReset) return .none;
                                app.state.status.setError(app.allocator, @errorName(err)) catch {};
                                if (err != error.QueueFull) app.state.appendTranscript(.@"error", @errorName(err)) catch {};
                            };
                        }
                        if (consumed) {
                            app.state.recordComposerHistory(text) catch |err| app.recordError(@errorName(err)) catch {};
                            app.state.composer.clear();
                            app.drainEvents() catch |err| {
                                app.state.status.setError(app.allocator, @errorName(err)) catch {};
                                app.state.appendTranscript(.@"error", @errorName(err)) catch {};
                            };
                        }
                    },
                    .backspace => _ = app.state.composer.deleteBeforeCursor(),
                    .delete => _ = app.state.composer.deleteAtCursor(),
                    .tab => {
                        if (app.state.mode == .normal and app.state.status.streaming and !app.state.status.compacting and compactDraftFocus(app.state.composer.text()) != null) {
                            const text = app.state.composer.text();
                            app.queueCompaction(compactDraftFocus(text).?) catch |err| app.recordError(@errorName(err)) catch {};
                            app.state.recordComposerHistory(text) catch |err| app.recordError(@errorName(err)) catch {};
                            app.state.composer.clear();
                        } else if (app.state.mode == .normal and app.state.status.streaming and queueableCommandDraft(app.state.composer.text())) {
                            const text = app.state.composer.text();
                            app.deferCommand(text) catch |err| app.recordError(@errorName(err)) catch {};
                            app.state.recordComposerHistory(text) catch |err| app.recordError(@errorName(err)) catch {};
                            app.state.composer.clear();
                        } else if (app.state.mode == .normal and app.state.status.streaming and !isSlashDraft(app.state.composer.text())) {
                            const text = app.state.composer.text();
                            const queued = app.queueFollowUp(text) catch |err| blk: {
                                if (err == error.CompactionInProgress) {
                                    app.state.appendTranscript(.@"error", "A compaction is running; queue it once it ends.") catch {};
                                } else if (err != error.PendingSessionReset) app.recordError(@errorName(err)) catch {};
                                break :blk false;
                            };
                            if (queued) {
                                app.state.recordComposerHistory(text) catch |err| app.recordError(@errorName(err)) catch {};
                                app.state.composer.clear();
                            }
                        } else {
                            _ = app.completeSlashCommand() catch false;
                        }
                    },
                    .char => |c| appendChar(app, c) catch {},
                    .paste => |text| app.state.composer.insertPaste(app.allocator, text) catch {},
                    .space => app.state.composer.insertSlice(app.allocator, " ") catch {},
                    .left => _ = app.state.composer.moveCursorPrev(),
                    .right => _ = app.state.composer.moveCursorNext(),
                    .home => app.state.composer.moveCursorHome(),
                    .end => app.state.composer.moveCursorEnd(),
                    .up => {
                        if (app.state.mode != .normal) {
                            _ = app.state.composerHistoryPrev() catch false;
                        } else if (!app.moveSlashSelection(-1)) {
                            app.handleComposerVertical(@max(ctx.width, 20), -1) catch {};
                        }
                    },
                    .down => {
                        if (app.state.mode != .normal) {
                            _ = app.state.composerHistoryNext() catch false;
                        } else if (!app.moveSlashSelection(1)) {
                            app.handleComposerVertical(@max(ctx.width, 20), 1) catch {};
                        }
                    },
                    .page_up => app.state.transcript_scroll += 5,
                    .page_down => app.state.transcript_scroll -|= 5,
                    .escape => self.handleEscape(app),
                    else => {},
                }
            },
            .mouse => |mouse| handleMouse(app, mouse),
            .window_size => self.refillInlineWindowAfterResize(app, ctx) catch |err| app.recordError(@errorName(err)) catch {},
            .tick => {
                app.state.anim_tick +%= 1;
                advanceZen(app);
                app.drainEvents() catch {};
                app.pumpAutoContinue(compat.time.nowMillis());
                app.pollLogin() catch {};
                app.pollWorktree() catch |err| app.recordError(@errorName(err)) catch {};
                app.pollWorktreeManagement() catch |err| app.recordError(@errorName(err)) catch {};
                const now_ms = compat.time.nowMillis();
                app.state.refreshStreamingElapsed(now_ms);
                app.state.telemetry.rate.liveAt(now_ms);
                if (app.interrupt_armed_tick) |armed| {
                    if (app.state.anim_tick -% armed > interrupt_window_ticks) app.interrupt_armed_tick = null;
                }
                if (app.state.anim_tick % branch_refresh_ticks == 0) {
                    app.refreshBranchOnSlowTick() catch |err| app.recordError(@errorName(err)) catch {};
                }
            },
            .resumed => {},
            .quit => return self.quitCmd(app, ctx),
        }
        if (app.pending_clear_screen) {
            app.pending_clear_screen = false;
            if (self.inlineMode(ctx)) ctx.requestClearScreen();
        }
        self.flushInlineHistory(app, ctx, false) catch |err| app.recordError(@errorName(err)) catch {};
        app.flushClipboard(ctx);
        return .none;
    }

    fn quitCmd(self: *TuiModel, app: *App, ctx: *zz.Context) zz.Cmd(Msg) {
        self.flushInlineHistory(app, ctx, true) catch {};
        return .quit;
    }

    const interrupt_window_ticks: u64 = 30;

    const branch_refresh_ticks: u64 = 100;

    fn streamActive(app: *const App) bool {
        if (app.state.status.streaming) return true;
        if (app.runtime) |runtime| return runtime.stream_active;
        return false;
    }

    fn abortTurn(app: *App) void {
        app.submit("/abort") catch |err| app.recordError(@errorName(err)) catch {};
    }

    fn handleInterrupt(self: *TuiModel, app: *App, ctx: *zz.Context) zz.Cmd(Msg) {
        app.userTookOver();
        if (app.interrupt_armed_tick != null) return self.quitCmd(app, ctx);
        if (app.state.mode == .login_input) {
            app.cancelLogin();
            return .none;
        }
        if (app.state.mode == .approval or streamActive(app)) {
            abortTurn(app);
            app.state.composer.clear();
            app.interrupt_armed_tick = app.state.anim_tick;
            return .none;
        }
        if (app.state.mode != .normal) {
            app.state.mode = .normal;
            return .none;
        }
        if (app.state.composer.buffer.items.len > 0) {
            app.state.composer.clear();
            app.interrupt_armed_tick = app.state.anim_tick;
            return .none;
        }
        return self.quitCmd(app, ctx);
    }

    fn closeModal(app: *App) void {
        if (app.state.confirm_session_force_delete) {
            app.state.confirm_session_force_delete = false;
            app.clearPendingDelete();
        }
        app.state.mode = .normal;
        app.userTookOver();
    }

    fn handleEscape(self: *TuiModel, app: *App) void {
        _ = self;
        app.userTookOver();
        if (app.state.composer.buffer.items.len > 0) {
            app.state.composer.clear();
            return;
        }
        if (streamActive(app) or app.state.held_after_abort.items.len > 0) {
            abortTurn(app);
            return;
        }
        app.state.mode = .normal;
    }

    pub fn inlineMode(self: *const TuiModel, ctx: *const zz.Context) bool {
        return switch (self.render_mode) {
            .auto => ctx._terminal != null,
            .inline_history => true,
            .full_transcript => false,
        };
    }

    pub fn view(self: *TuiModel, ctx: *const zz.Context) []const u8 {
        const app = &(self.app orelse return "Makai TUI failed to initialize");
        const width: usize = @max(ctx.width, 20);
        const height: usize = @max(ctx.height, 8);
        app.last_view_height = height;
        if (app.state.zen.on) return self.renderZen(app, ctx, width, height) catch "";
        const chrome = self.renderChrome(app, ctx, width, height);
        if (self.inlineMode(ctx)) {
            const fixed = countLines(chrome.status) + countLines(chrome.composer) + countLines(chrome.extra) + 1;
            const body_budget = height -| fixed;
            const body = renderInlineBody(app, ctx, width, body_budget) catch "";
            var parts: [5][]const u8 = undefined;
            var len: usize = 0;
            if (body.len > 0) {
                parts[len] = body;
                len += 1;
            }
            parts[len] = "";
            len += 1;
            if (chrome.extra.len > 0) {
                parts[len] = chrome.extra;
                len += 1;
            }
            parts[len] = chrome.composer;
            len += 1;
            parts[len] = chrome.status;
            len += 1;
            const frame = tui_render.joinVertical(ctx.allocator, parts[0..len]) catch return chrome.composer;
            return padFrameToHeight(ctx.allocator, frame, height) catch frame;
        }

        const fixed = countLines(chrome.status) + countLines(chrome.composer) + @max(countLines(chrome.extra), 1);
        const transcript_height = if (height > fixed) height - fixed else 3;
        const transcript = transcript_view.render(ctx.allocator, &app.state, .{ .width = width, .height = transcript_height, .anim_tick = app.state.anim_tick }) catch "";
        return tui_render.joinVertical(ctx.allocator, &.{ transcript, chrome.extra, chrome.composer, chrome.status }) catch "";
    }

    fn zenMood(app: *const App, running: bool) zen_view.Mood {
        if (app.state.mode == .approval) return .waiting;
        if (!running) return .idle;
        if (app.state.active_tool_summary_entry != null) return .tool;
        return .thinking;
    }

    fn advanceZen(app: *App) void {
        if (!app.state.zen.on) return;
        const running = noteZenRun(app);
        app.state.zen.phase = @mod(app.state.zen.phase + zen_view.breathStep(zenMood(app, running)), 1);
    }

    const zen_reply_top: usize = std.math.maxInt(usize) / 2;

    fn noteZenRun(app: *App) bool {
        const zen = &app.state.zen;
        const running = streamActive(app);
        if (zen.was_running and !running) {
            zen.ended_tick = app.state.anim_tick;
            app.state.transcript_scroll = zen_reply_top;
        }
        if (!zen.was_running and running) zen.beginRun(app.state.anim_tick);
        if (running) zen.ended_tick = null;
        zen.was_running = running;
        return running;
    }

    fn renderZen(self: *TuiModel, app: *App, ctx: *const zz.Context, width: usize, height: usize) ![]const u8 {
        const allocator = ctx.allocator;
        const column = zen_view.columnWidth(width);
        const extra = if (app.state.mode == .normal) renderCommandPalette(allocator, app, column) catch "" else self.renderChrome(app, ctx, column, height).extra;
        const entries = app.state.transcript.items;
        const counts = tui_state.zenCounts(entries, app.state.zen.start_index);
        const running = noteZenRun(app);
        var activity: []const u8 = "";
        if (counts.last_activity) |index| {
            const entry = &entries[index];
            if (entry.kind == .thinking) {
                activity = "thinking";
            } else if (transcript_view.toolTitle(entry.text.items)) |title| {
                activity = if (title.arg.len > 0) try std.fmt.allocPrint(allocator, "{s}  {s}", .{ title.label, title.arg }) else title.label;
            }
        }
        const waiting = running and app.state.active_assistant_entry == null and app.state.active_thinking_entry == null and app.state.active_tool_summary_entry == null and app.state.mode != .approval;
        if (waiting) activity = try std.fmt.allocPrint(allocator, "waiting for {s}", .{if (app.state.status.model.len > 0) app.state.status.model else "the model"});
        if (running) {
            app.state.zen.noteActivity(activity);
            app.state.zen.advance(app.state.anim_tick, zen_view.change_ticks);
        }
        const timed: ?usize = if (!running) null else if (waiting) std.math.maxInt(usize) - entries.len else app.state.active_tool_summary_entry;
        const tool_ms = app.state.zen.stepMs(timed, compat.time.nowMillis());
        var final_block: []const u8 = "";
        var failed = false;
        if (!running) {
            if (counts.final) |index| {
                const entry = &entries[index];
                failed = entry.kind == .@"error";
                final_block = try transcript_view.renderTranscriptEntryWith(allocator, entry, zen_view.readingWidth(width), .{});
            }
        }
        var frame: zen_view.Frame = .{
            .mood = zenMood(app, running),
            .phase = app.state.zen.phase,
            .farewell = if (app.state.zen.ended_tick) |ended| zen_view.farewellLevel(app.state.anim_tick -% ended) else null,
            .width = width,
            .height = height,
            .input = app.state.composer.buffer.items,
            .cursor = app.state.composer.cursor,
            .secret = app.state.mode == .login_input and app.state.login_input_secret,
            .placeholder = if (app.state.mode == .login_input) (if (app.state.login_input_secret) "paste the secret and press Enter" else "type your answer and press Enter") else "type a prompt",
            .extra = extra,
            .counts = .{ .thinking = counts.thinking, .tools = counts.tools, .messages = counts.messages },
            .running = running,
            .activity = app.state.zen.activity(),
            .incoming = app.state.zen.incomingActivity(),
            .rise = app.state.zen.rise(app.state.anim_tick, zen_view.change_ticks),
            .light = @as(f32, @floatFromInt(app.state.anim_tick % zen_view.light_ticks)) / @as(f32, @floatFromInt(zen_view.light_ticks)),
            .tool_ms = if (app.state.zen.settled()) tool_ms else 0,
            .final_block = final_block,
            .failed = failed,
            .title = app.state.session_title,
        };
        if (zen_view.showsReply(frame)) app.state.transcript_scroll = @min(app.state.transcript_scroll, try zen_view.maxScroll(allocator, frame));
        frame.scroll = app.state.transcript_scroll;
        return zen_view.render(allocator, frame);
    }

    const Chrome = struct {
        composer: []const u8,
        status: []const u8,
        extra: []const u8,
        queued_rows: usize,
    };

    fn renderInlineBody(app: *App, ctx: *const zz.Context, width: usize, budget: usize) ![]const u8 {
        if (app.state.transcript_scroll > 0 and budget >= 2) {
            const stream = try renderInlineStream(ctx.allocator, &app.state, 0, 0, width, true);
            const total = countLines(stream);
            const view_rows = budget - 1;
            const max_scroll = total -| view_rows;
            const scroll = @min(app.state.transcript_scroll, max_scroll);
            app.state.transcript_scroll = scroll;
            if (scroll > 0) {
                const window = try transcript_view.lineWindow(ctx.allocator, stream, view_rows, scroll);
                const pct = transcript_view.scrollPercent(total, view_rows, scroll);
                const label = try std.fmt.allocPrint(ctx.allocator, "\u{2191} SCROLL {d}% \u{b7} PgDn to return", .{pct});
                const indicator = try tui_theme.muted().render(ctx.allocator, label);
                return tui_render.joinVertical(ctx.allocator, &.{ indicator, window });
            }
        } else {
            app.state.transcript_scroll = 0;
        }
        const stream = try renderInlineStream(ctx.allocator, &app.state, app.inline_history_flushed, app.inline_flushed_rows, width, true);
        return tailLines(ctx.allocator, stream, budget);
    }

    const cwd_row_min_height: usize = 12;

    fn renderChrome(self: *TuiModel, app: *App, ctx: *const zz.Context, width: usize, height: usize) Chrome {
        _ = self;
        const hint = if (app.interrupt_armed_tick != null)
            tui_theme.key.ctrl ++ "C again to quit"
        else
            composer_view.hintText(ctx.allocator, &app.state) catch "";
        const bar = status_bar_view.render(ctx.allocator, &app.state, .{ .width = width, .hint = hint }) catch "";
        const status = if (height >= cwd_row_min_height and app.state.cwd_display.len > 0) blk: {
            const row = status_bar_view.renderCwdRow(ctx.allocator, &app.state, width) catch break :blk bar;
            break :blk tui_render.joinVertical(ctx.allocator, &.{ bar, row }) catch bar;
        } else bar;
        composer_view.adjustScroll(ctx.allocator, &app.state, width, height) catch {};
        const composer = composer_view.render(ctx.allocator, &app.state, .{ .width = width, .max_rows = composer_view.rowCap(height) }) catch "";
        const queued = if (app.state.mode == .normal) renderQueuedFollowUps(ctx.allocator, &app.state, width) catch "" else "";
        const extra = switch (app.state.mode) {
            .approval => approval_view.render(ctx.allocator, &app.state, .{ .width = width }) catch "",
            .session_picker => session_picker_view.render(ctx.allocator, &app.state, .{ .width = width, .height = sessionPickerHeight(app), .offset = app.state.session_scroll }) catch "",
            .picker => renderPicker(ctx.allocator, app, width) catch "",
            .login_input => "",
            .normal => blk: {
                const palette = renderCommandPalette(ctx.allocator, app, width) catch "";
                if (queued.len == 0) break :blk palette;
                if (palette.len == 0) break :blk queued;
                break :blk tui_render.joinVertical(ctx.allocator, &.{ queued, palette }) catch palette;
            },
        };
        return .{ .composer = composer, .status = status, .extra = extra, .queued_rows = countLines(queued) };
    }

    fn renderPicker(allocator: std.mem.Allocator, app: *const App, width: usize) ![]const u8 {
        const count = app.pickerSourceCount();
        const items = try allocator.alloc(menu_picker_view.Item, count);
        const current = if (app.state.picker_kind == .model) (if (app.runtime) |runtime| runtime.currentModel() else null) else null;
        var len: usize = 0;
        for (0..count) |i| {
            if (!app.pickerMatches(i)) continue;
            items[len] = app.pickerItem(i, current);
            len += 1;
        }
        const filter = app.state.pickerFilter();
        const title: []const u8 = switch (app.state.picker_kind) {
            .model => "Select model",
            .login => "Login provider",
            .permission => "Tool permissions",
            .settings => "TUI settings",
        };
        const empty_message = if (filter.len > 0)
            try tui_text.truncateLineToWidth(allocator, try std.fmt.allocPrint(allocator, "  nothing matches \"{s}\"", .{filter}), width -| 4)
        else if (app.state.picker_kind == .model) "  no models available" else "  (nothing to select)";
        const subtitle: ?[]const u8 = if (filter.len > 0)
            try tui_text.truncateLineToWidth(allocator, try std.fmt.allocPrint(allocator, "{s} {s}{s}", .{ tui_theme.glyph.prompt, filter, tui_theme.glyph.caret }), width -| 4)
        else if (app.state.picker_kind == .model) tui_theme.glyph.prompt ++ " type to filter" else if (app.state.picker_kind == .settings) "Enter toggles  Esc closes" else null;
        return menu_picker_view.render(allocator, .{
            .title = title,
            .subtitle = subtitle,
            .items = items[0..len],
            .selected = app.state.menu_index,
            .width = width,
            .height = sessionPickerHeight(app),
            .offset = app.state.menu_scroll,
            .empty_message = empty_message,
        });
    }

    const max_queued_rows: usize = 3;

    fn renderQueuedFollowUps(allocator: std.mem.Allocator, state: *const tui_state.AppState, width: usize) ![]const u8 {
        const held = state.held_after_abort.items[@min(state.held_after_abort_echoed, state.held_after_abort.items.len)..];
        var waiting = std.ArrayList([]const u8).empty;
        defer waiting.deinit(allocator);
        try waiting.appendSlice(allocator, held);
        try waiting.appendSlice(allocator, state.pending_follow_ups.items);
        const pending = waiting.items;
        if (pending.len == 0) return "";
        var out: std.Io.Writer.Allocating = .init(allocator);
        errdefer out.deinit();
        const writer = &out.writer;
        const shown = @min(pending.len, max_queued_rows);
        for (pending[0..shown], 0..) |text, i| {
            if (i > 0) try writer.writeByte('\n');
            const flat = try std.mem.replaceOwned(u8, allocator, text, "\n", " ");
            const safe = try tui_text.sanitizeTerminalText(allocator, flat);
            const line = try std.fmt.allocPrint(allocator, "  \u{21b3} queued  {s}", .{safe});
            try writer.writeAll(try tui_theme.muted().render(allocator, try tui_text.truncateLineToWidth(allocator, line, width -| 1)));
        }
        if (pending.len > shown) {
            const more = try std.fmt.allocPrint(allocator, "    +{d} more queued", .{pending.len - shown});
            try writer.writeByte('\n');
            try writer.writeAll(try tui_theme.dim().render(allocator, more));
        }
        return out.toOwnedSlice();
    }

    fn flushBudget(self: *TuiModel, app: *App, ctx: *const zz.Context) usize {
        const width: usize = @max(ctx.width, 20);
        const height: usize = @max(ctx.height, 8);
        const chrome = self.renderChrome(app, ctx, width, height);
        return height -| (countLines(chrome.status) + composer_view.min_panel_rows + chrome.queued_rows + 1);
    }

    fn renderCommandPalette(allocator: std.mem.Allocator, app: *const App, width: usize) ![]const u8 {
        const query = app.slashQuery() orelse return "";
        var items: [tui_commands.commands.len]menu_picker_view.Item = undefined;
        var len: usize = 0;
        for (&tui_commands.commands) |info| {
            if (!std.mem.startsWith(u8, info.name, query)) continue;
            items[len] = .{ .label = info.usage, .detail = info.description };
            len += 1;
        }
        if (len == 0) return "";
        const selected = app.slashSelection();
        return menu_picker_view.render(allocator, .{
            .title = "Commands",
            .items = items[0..len],
            .selected = selected,
            .width = width,
            .height = palette_rows,
            .offset = if (selected >= palette_rows) selected + 1 - palette_rows else 0,
            .footer = tui_theme.key.up_down ++ " select " ++ tui_theme.glyph.dot ++ " " ++ tui_theme.key.tab ++ " complete " ++ tui_theme.glyph.dot ++ " " ++ tui_theme.key.enter ++ " run",
        });
    }

    const palette_rows: usize = 8;

    fn renderInlineBlock(allocator: std.mem.Allocator, state: *const tui_state.AppState, index: usize, width: usize, live: bool) ![]u8 {
        const entries = state.transcript.items;
        const entry = &entries[index];
        const detached = index == 0 or !transcript_view.entriesAttached(&entries[index - 1], entry);
        const awaiting = live and state.mode == .approval and entry.kind == .tool and entry.tool_call_id.len > 0 and std.mem.eql(u8, entry.tool_call_id, state.approval.tool_call_id);
        const tool = if (entry.kind == .tool and entry.tool_call_id.len > 0) state.toolById(entry.tool_call_id) else null;
        const rendered = try transcript_view.renderTranscriptEntryWith(allocator, entry, width, .{ .live = live and isLiveEntry(state, index), .anim_tick = state.anim_tick, .awaiting_approval = awaiting, .tool = tool, .verbosity = state.verbosity });
        defer allocator.free(rendered);
        if (rendered.len == 0 or !detached) return allocator.dupe(u8, rendered);
        return std.mem.concat(allocator, u8, &.{ "\n", rendered });
    }

    fn renderInlineStream(allocator: std.mem.Allocator, state: *const tui_state.AppState, from: usize, skip_rows: usize, width: usize, live: bool) ![]const u8 {
        const entries = state.transcript.items;
        var out: std.Io.Writer.Allocating = .init(allocator);
        defer out.deinit();
        const writer = &out.writer;
        var first = true;
        var i = @min(from, entries.len);
        while (i < entries.len) : (i += 1) {
            const block = try renderInlineBlock(allocator, state, i, width, live);
            defer allocator.free(block);
            if (block.len == 0) continue;
            if (!first) try writer.writeAll("\n");
            first = false;
            try writer.writeAll(block);
        }
        if (live and state.status.streaming and state.active_assistant_entry == null and state.active_tool_summary_entry == null and state.mode != .approval) {
            if (!first) try writer.writeAll("\n");
            try writer.writeAll("\n");
            const waiting = try transcript_view.renderWaitingLine(allocator, state.status.model, state.anim_tick, state.status.streaming_elapsed_ms);
            defer allocator.free(waiting);
            try writer.writeAll(waiting);
        }
        return dropLines(allocator, out.written(), skip_rows);
    }

    fn dropLines(allocator: std.mem.Allocator, text: []const u8, count: usize) ![]const u8 {
        var start: usize = 0;
        var remaining = count;
        while (remaining > 0) : (remaining -= 1) {
            const newline = std.mem.indexOfScalarPos(u8, text, start, '\n') orelse return allocator.dupe(u8, "");
            start = newline + 1;
        }
        return allocator.dupe(u8, text[start..]);
    }

    fn isLiveEntry(state: *const tui_state.AppState, index: usize) bool {
        if (state.active_assistant_entry) |idx| if (idx == index) return true;
        if (state.active_thinking_entry) |idx| if (idx == index) return true;
        if (state.active_tool_summary_entry) |idx| if (idx == index) return true;
        return false;
    }

    fn tailLines(allocator: std.mem.Allocator, text: []const u8, max_lines: usize) ![]const u8 {
        if (max_lines == 0 or text.len == 0) return allocator.dupe(u8, "");
        var lines: usize = 1;
        for (text) |byte| {
            if (byte == '\n') lines += 1;
        }
        if (lines <= max_lines) return allocator.dupe(u8, text);

        var line_start = text.len;
        var remaining = max_lines;
        while (line_start > 0 and remaining > 0) {
            line_start -= 1;
            if (text[line_start] == '\n') remaining -= 1;
        }
        const start = if (remaining == 0) line_start + 1 else 0;
        return allocator.dupe(u8, text[start..]);
    }

    fn appendChar(app: *App, c: u21) !void {
        var buf: [4]u8 = undefined;
        const len = try std.unicode.utf8Encode(c, &buf);
        try app.state.composer.insertSlice(app.allocator, buf[0..len]);
    }

    fn appendPickerChar(app: *App, c: u21) !void {
        var buf: [4]u8 = undefined;
        const len = try std.unicode.utf8Encode(c, &buf);
        try app.state.appendPickerFilter(buf[0..len]);
    }

    fn flushInlineHistory(self: *TuiModel, app: *App, ctx: *zz.Context, include_active: bool) !void {
        if (!self.inlineMode(ctx)) return;
        if (app.state.zen.on and !include_active) return;
        const entries = app.state.transcript.items;
        if (app.inline_history_flushed >= entries.len) return;
        app.state.advanceSummaryScanFloor();

        const width: usize = @max(ctx.width, 20);
        const stop = if (include_active) entries.len else inlineFlushStop(app);
        var overflow: usize = std.math.maxInt(usize);
        if (!include_active) {
            const stream = try renderInlineStream(ctx.allocator, &app.state, app.inline_history_flushed, app.inline_flushed_rows, width, true);
            defer ctx.allocator.free(stream);
            overflow = countLines(stream) -| self.flushBudget(app, ctx);
        }
        while (overflow > 0 and app.inline_history_flushed < stop) {
            const block = try renderInlineBlock(ctx.allocator, &app.state, app.inline_history_flushed, width, false);
            defer ctx.allocator.free(block);
            if (block.len == 0 or countLines(block) <= app.inline_flushed_rows) {
                app.inline_history_flushed += 1;
                app.inline_flushed_rows = 0;
                continue;
            }
            const block_rows = countLines(block);
            const offset = app.inline_flushed_rows;
            const take = @min(block_rows -| offset, overflow);
            if (take == 0) break;
            try printAboveRows(ctx, block, offset, take);
            overflow -= take;
            if (offset + take >= block_rows) {
                app.inline_history_flushed += 1;
                app.inline_flushed_rows = 0;
            } else {
                app.inline_flushed_rows = offset + take;
            }
        }
    }

    fn printAboveRows(ctx: *zz.Context, text: []const u8, skip: usize, count: usize) !void {
        var lines = std.mem.splitScalar(u8, text, '\n');
        var index: usize = 0;
        var printed: usize = 0;
        while (lines.next()) |line| : (index += 1) {
            if (index < skip) continue;
            if (printed >= count) break;
            try ctx.printAbove(line);
            printed += 1;
        }
    }

    fn refillInlineWindowAfterResize(self: *TuiModel, app: *App, ctx: *zz.Context) !void {
        if (!app.state.zen.on) app.state.transcript_scroll = 0;
        if (!self.inlineMode(ctx)) return;
        const width: usize = @max(ctx.width, 20);
        const budget = self.flushBudget(app, ctx);
        const entries = app.state.transcript.items;
        var start = @min(app.inline_history_flushed, entries.len);
        const visible = try renderInlineStream(ctx.allocator, &app.state, start, app.inline_flushed_rows, width, true);
        defer ctx.allocator.free(visible);
        var rows = countLines(visible);
        if (rows >= budget) return;
        rows += app.inline_flushed_rows;
        while (start > 0 and rows < budget) {
            start -= 1;
            const block = try renderInlineBlock(ctx.allocator, &app.state, start, width, false);
            defer ctx.allocator.free(block);
            if (block.len > 0) rows += countLines(block);
        }
        app.inline_history_flushed = start;
        app.inline_flushed_rows = rows -| budget;
    }

    fn inlineFlushStop(app: *const App) usize {
        var stop = app.state.transcript.items.len;
        if (app.state.active_user_entry) |idx| stop = @min(stop, idx);
        if (app.state.active_assistant_entry) |idx| stop = @min(stop, idx);
        if (app.state.active_thinking_entry) |idx| stop = @min(stop, idx);
        if (app.state.active_tool_result_entry) |idx| stop = @min(stop, idx);
        if (app.state.active_tool_summary_entry) |idx| stop = @min(stop, idx);
        return @min(stop, app.state.summary_scan_floor);
    }

    fn handleMouse(app: *App, mouse: zz.MouseEvent) void {
        if (mouse.event_type != .press) return;
        switch (mouse.button) {
            .wheel_up => {
                app.state.transcript_scroll += 3;
            },
            .wheel_down => {
                app.state.transcript_scroll -|= 3;
            },
            else => {},
        }
    }

    fn permissionModeDetail(mode: tui_runtime.PermissionMode) []const u8 {
        return switch (mode) {
            .bypass => "run tools without prompts",
            .ask => "ask before tool execution",
        };
    }

    fn padFrameToHeight(allocator: std.mem.Allocator, frame: []const u8, height: usize) ![]const u8 {
        const rows = countLines(frame);
        if (rows >= height) return frame;
        const pad = height - rows;
        const out = try allocator.alloc(u8, pad + frame.len);
        @memset(out[0..pad], '\n');
        @memcpy(out[pad..], frame);
        return out;
    }

    fn countLines(text: []const u8) usize {
        if (text.len == 0) return 0;
        var count: usize = 1;
        for (text) |c| {
            if (c == '\n') count += 1;
        }
        return count;
    }

    fn moveSessionSelection(app: *App, delta: isize) void {
        const n = app.state.sessions.items.len;
        if (n == 0) {
            app.state.session_index = 0;
            app.state.session_scroll = 0;
            return;
        }
        if (delta < 0) {
            app.state.session_index -|= @as(usize, @intCast(-delta));
        } else {
            app.state.session_index = @min(n - 1, app.state.session_index + @as(usize, @intCast(delta)));
        }
        ensureSessionSelectionVisible(app);
    }

    fn ensureSessionSelectionVisible(app: *App) void {
        if (app.state.sessions.items.len == 0) {
            app.state.session_index = 0;
            app.state.session_scroll = 0;
            return;
        }
        if (app.state.session_index >= app.state.sessions.items.len) app.state.session_index = app.state.sessions.items.len - 1;
        const height = sessionPickerHeight(app);
        if (app.state.session_index < app.state.session_scroll) {
            app.state.session_scroll = app.state.session_index;
        } else if (height > 0 and app.state.session_index >= app.state.session_scroll + height) {
            app.state.session_scroll = app.state.session_index + 1 - height;
        }
    }

    fn visibleSessionCount(app: *const App) usize {
        return @min(app.state.sessions.items.len -| app.state.session_scroll, sessionPickerHeight(app));
    }

    fn sessionPickerHeight(app: *const App) usize {
        return @max(app.last_view_height, 8) / 2;
    }
};

fn switchTiming(runtime: *const tui_runtime.TuiRuntime) []const u8 {
    return if (runtime.local_agent != null) "before the next turn of this run, or when it ends" else "when this run ends";
}

fn waitsForRunEnd(command: tui_commands.Command) bool {
    const arg = command.arg orelse return false;
    return switch (command.kind) {
        .context, .output, .logout => true,
        .provider => std.mem.startsWith(u8, arg, "del ") or std.mem.startsWith(u8, arg, "delete ") or std.mem.eql(u8, arg, "del") or std.mem.eql(u8, arg, "delete"),
        else => false,
    };
}

fn queueableCommandDraft(text: []const u8) bool {
    const command = tui_commands.parse(std.mem.trim(u8, text, " \t\r\n")) catch return false;
    if (command.kind == .model) {
        const arg = command.arg orelse return false;
        return !std.mem.eql(u8, arg, "refresh");
    }
    return waitsForRunEnd(command);
}

fn compactDraftFocus(text: []const u8) ?[]const u8 {
    const command = tui_commands.parse(std.mem.trim(u8, text, " \t\r\n")) catch return null;
    if (command.kind != .compact) return null;
    return command.arg orelse "";
}

fn isSlashDraft(text: []const u8) bool {
    const trimmed = std.mem.trimStart(u8, text, " \t\r\n");
    return trimmed.len > 0 and trimmed[0] == '/';
}

fn filterMatches(filter: []const u8, label: []const u8, detail: []const u8) bool {
    var terms = std.mem.tokenizeScalar(u8, filter, ' ');
    while (terms.next()) |term| {
        if (std.ascii.indexOfIgnoreCase(label, term) == null and std.ascii.indexOfIgnoreCase(detail, term) == null) return false;
    }
    return true;
}

fn slashMatchCount(query: []const u8) usize {
    var count: usize = 0;
    for (&tui_commands.commands) |info| {
        if (std.mem.startsWith(u8, info.name, query)) count += 1;
    }
    return count;
}

fn slashMatch(query: []const u8, position: usize) ?tui_commands.CommandInfo {
    var seen: usize = 0;
    for (&tui_commands.commands) |info| {
        if (!std.mem.startsWith(u8, info.name, query)) continue;
        if (seen == position) return info;
        seen += 1;
    }
    return null;
}

fn defaultIo() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

fn currentPathOwned(allocator: std.mem.Allocator) ![]u8 {
    const path_z = try std.process.currentPathAlloc(defaultIo(), allocator);
    defer allocator.free(path_z);
    return allocator.dupe(u8, path_z);
}

const git_head_prefix = "ref: refs/heads/";
const git_head_max_bytes = 4096;
const detached_id_len = 7;

fn gitHeadLabel(allocator: std.mem.Allocator, dir_path: []const u8) !?[]u8 {
    if (dir_path.len == 0) return null;
    var current = try allocator.dupe(u8, dir_path);
    defer allocator.free(current);
    while (current.len > 0) {
        const lookup = try gitHeadPath(allocator, current);
        defer if (lookup.path) |value| allocator.free(value);
        if (lookup.dot_git_present) {
            const head = lookup.path orelse break;
            return try readGitBranchName(allocator, head);
        }
        const parent = std.fs.path.dirname(current) orelse break;
        if (parent.len == 0 or std.mem.eql(u8, parent, current)) break;
        const next = try allocator.dupe(u8, parent);
        allocator.free(current);
        current = next;
    }
    return null;
}

fn readGitBranchName(allocator: std.mem.Allocator, head_path: []const u8) !?[]u8 {
    const head = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), head_path, git_head_max_bytes) catch return null;
    defer allocator.free(head);
    const line = std.mem.trim(u8, head, " \t\r\n");
    if (std.mem.startsWith(u8, line, git_head_prefix)) {
        const name = std.mem.trim(u8, line[git_head_prefix.len..], " \t\r\n");
        if (name.len == 0) return null;
        return try allocator.dupe(u8, name);
    }
    if (line.len < detached_id_len) return null;
    return try allocator.dupe(u8, line[0..detached_id_len]);
}

const HeadLookup = struct {
    dot_git_present: bool,
    path: ?[]u8,
};

fn gitHeadPath(allocator: std.mem.Allocator, dir_path: []const u8) !HeadLookup {
    const dot_git = try std.fs.path.join(allocator, &.{ dir_path, ".git" });
    defer allocator.free(dot_git);
    return switch (compat.fs.fileKind(compat.fs.getCwd(), dot_git)) {
        .absent => .{ .dot_git_present = false, .path = null },
        .unreadable => .{ .dot_git_present = true, .path = null },
        .directory => .{ .dot_git_present = true, .path = try std.fs.path.join(allocator, &.{ dot_git, "HEAD" }) },
        .file => .{ .dot_git_present = true, .path = try gitPointerHeadPath(allocator, dir_path, dot_git) },
        .other => .{ .dot_git_present = true, .path = null },
    };
}

fn gitPointerHeadPath(allocator: std.mem.Allocator, dir_path: []const u8, dot_git: []const u8) !?[]u8 {
    const pointer = compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), dot_git, git_head_max_bytes) catch return null;
    defer allocator.free(pointer);
    const target = gitDirTarget(pointer) orelse return null;
    const resolved = if (std.fs.path.isAbsolute(target))
        try allocator.dupe(u8, target)
    else
        try std.fs.path.join(allocator, &.{ dir_path, target });
    defer allocator.free(resolved);
    return try std.fs.path.join(allocator, &.{ resolved, "HEAD" });
}

fn gitDirTarget(pointer: []const u8) ?[]const u8 {
    const trimmed = std.mem.trim(u8, pointer, " \t\r\n");
    const colon = std.mem.indexOfScalar(u8, trimmed, ':') orelse return null;
    if (!std.mem.eql(u8, trimmed[0..colon], "gitdir")) return null;
    const target = std.mem.trim(u8, trimmed[colon + 1 ..], " \t\r\n");
    if (target.len == 0) return null;
    return target;
}

fn statusLine(w: *std.Io.Writer, key: []const u8, value: []const u8) !void {
    try w.print("  {s:<16}{s}\n", .{ key, value });
}

fn statusLinePrint(w: *std.Io.Writer, key: []const u8, comptime fmt: []const u8, args: anytype) !void {
    try w.print("  {s:<16}", .{key});
    try w.print(fmt, args);
    try w.writeByte('\n');
}

fn usageLine(w: *std.Io.Writer, key: []const u8, usage: tui_state.UsageTotals) !void {
    if (!usage.reported()) return statusLine(w, key, "no usage reported");
    try statusLinePrint(w, key, "{d} in, {d} out, {d} cache read", .{ usage.input, usage.output, usage.cache_read });
}

fn agoText(buf: []u8, elapsed_ms: i64) []const u8 {
    const secs: u64 = if (elapsed_ms > 0) @intCast(@divFloor(elapsed_ms, 1000)) else 0;
    if (secs < 60) return std.fmt.bufPrint(buf, "{d}s", .{secs}) catch "";
    if (secs < 3600) return std.fmt.bufPrint(buf, "{d}m", .{secs / 60}) catch "";
    if (secs < 86_400) return std.fmt.bufPrint(buf, "{d}h{d}m", .{ secs / 3600, (secs % 3600) / 60 }) catch "";
    return std.fmt.bufPrint(buf, "{d}d{d}h", .{ secs / 86_400, (secs % 86_400) / 3600 }) catch "";
}

fn collapseHome(allocator: std.mem.Allocator, path: []const u8, home: ?[]const u8) ![]u8 {
    const value = home orelse return allocator.dupe(u8, path);
    if (value.len <= 1) return allocator.dupe(u8, path);
    if (std.mem.eql(u8, path, value)) return allocator.dupe(u8, "~");
    if (std.mem.startsWith(u8, path, value) and path.len > value.len and path[value.len] == '/') {
        return std.fmt.allocPrint(allocator, "~{s}", .{path[value.len..]});
    }
    return allocator.dupe(u8, path);
}

fn newerSessionFirst(_: void, a: session_store.SessionMetadata, b: session_store.SessionMetadata) bool {
    if (a.last_active == b.last_active) return std.mem.lessThan(u8, a.session_id, b.session_id);
    return a.last_active > b.last_active;
}

fn generateSessionId(allocator: std.mem.Allocator) ![]u8 {
    const millis = compat.time.nowMillis();
    const secs: i64 = @divFloor(millis, 1000);
    const ms: i64 = @mod(millis, 1000);
    const epoch = std.time.epoch.EpochSeconds{ .secs = @as(u64, @intCast(@max(secs, 0))) };
    const day = epoch.getEpochDay();
    const year_day = day.calculateYearDay();
    const month_day = year_day.calculateMonthDay();
    const day_secs = epoch.getDaySeconds();
    var random_bytes: [8]u8 = undefined;
    compat.random.fillSecureBytes(&random_bytes);
    var random_hex: [16]u8 = undefined;
    encodeHexLower(&random_hex, &random_bytes);
    return std.fmt.allocPrint(
        allocator,
        "{d:0>4}{d:0>2}{d:0>2}-{d:0>2}{d:0>2}{d:0>2}-{d:0>3}-{s}",
        .{
            year_day.year,
            month_day.month.numeric(),
            month_day.day_index + 1,
            day_secs.getHoursIntoDay(),
            day_secs.getMinutesIntoHour(),
            day_secs.getSecondsIntoMinute(),
            ms,
            random_hex,
        },
    );
}

const session_title_bytes = 60;

fn titleLine(text: []const u8) []const u8 {
    var lines = std.mem.tokenizeAny(u8, text, "\r\n");
    while (lines.next()) |raw| {
        var line = std.mem.trim(u8, raw, " \t");
        for (line, 0..) |byte, index| {
            if (byte < 0x20 or byte == 0x7f) {
                line = std.mem.trimEnd(u8, line[0..index], " ");
                break;
            }
        }
        if (line.len == 0) continue;
        if (line.len <= session_title_bytes) return line;
        var end: usize = session_title_bytes;
        while (end > 0 and (line[end] & 0xC0) == 0x80) end -= 1;
        return line[0..end];
    }
    return "";
}

const CTm = extern struct {
    tm_sec: c_int,
    tm_min: c_int,
    tm_hour: c_int,
    tm_mday: c_int,
    tm_mon: c_int,
    tm_year: c_int,
    tm_wday: c_int,
    tm_yday: c_int,
    tm_isdst: c_int,
    tm_gmtoff: c_long,
    tm_zone: ?[*:0]const u8,
};

extern "c" fn localtime_r(timer: *const std.c.time_t, result: *CTm) ?*CTm;

fn utcOffsetSeconds(epoch_seconds: i64) i64 {
    if (comptime @import("builtin").os.tag == .windows or !@import("builtin").link_libc) {
        return 0;
    } else {
        const timer: std.c.time_t = @intCast(epoch_seconds);
        var tm: CTm = undefined;
        const local = localtime_r(&timer, &tm) orelse return 0;
        return local.tm_gmtoff;
    }
}

fn formatSessionLabel(allocator: std.mem.Allocator, meta: session_store.SessionMetadata, utc_offset_seconds: i64) ![]u8 {
    const secs: i64 = @divFloor(meta.last_active, 1000) + utc_offset_seconds;
    const epoch = std.time.epoch.EpochSeconds{ .secs = @as(u64, @intCast(@max(secs, 0))) };
    const day = epoch.getEpochDay();
    const year_day = day.calculateYearDay();
    const month_day = year_day.calculateMonthDay();
    const day_secs = epoch.getDaySeconds();
    const title = titleLine(meta.title);
    return std.fmt.allocPrint(
        allocator,
        "{s} · {d:0>4}-{d:0>2}-{d:0>2} {d:0>2}:{d:0>2} · {s}",
        .{
            if (title.len > 0) title else "untitled",
            year_day.year,
            month_day.month.numeric(),
            month_day.day_index + 1,
            day_secs.getHoursIntoDay(),
            day_secs.getMinutesIntoHour(),
            if (meta.model.len > 0) meta.model else "unknown",
        },
    );
}

test "formatSessionLabel shows the title's first line, the local time and the model" {
    const session_id = try std.testing.allocator.dupe(u8, "s1");
    defer std.testing.allocator.free(session_id);
    const model = try std.testing.allocator.dupe(u8, "gpt-6-luna");
    defer std.testing.allocator.free(model);
    const provider = try std.testing.allocator.dupe(u8, "openai-codex");
    defer std.testing.allocator.free(provider);
    const title = try std.testing.allocator.dupe(u8, "  Fix the resume freeze\nwith more detail");
    defer std.testing.allocator.free(title);
    const meta = session_store.SessionMetadata{ .session_id = session_id, .model = model, .provider = provider, .last_active = 1790600760000, .title = title };

    const label = try formatSessionLabel(std.testing.allocator, meta, 8 * 3600);
    defer std.testing.allocator.free(label);
    try std.testing.expectEqualStrings("Fix the resume freeze · 2026-09-28 21:06 · gpt-6-luna", label);

    var untitled_meta = meta;
    untitled_meta.title = &.{};
    const untitled = try formatSessionLabel(std.testing.allocator, untitled_meta, 0);
    defer std.testing.allocator.free(untitled);
    try std.testing.expectEqualStrings("untitled · 2026-09-28 13:06 · gpt-6-luna", untitled);
}

test "utcOffsetSeconds reads an offset a real time zone could have" {
    const offset = utcOffsetSeconds(1790600760);
    try std.testing.expect(@abs(offset) <= 14 * 3600);
    try std.testing.expectEqual(@as(i64, 0), @mod(offset, 900));
}

test "titleLine keeps the first non-empty line, up to a control byte, within 60 bytes on a character boundary" {
    try std.testing.expectEqualStrings("first", titleLine("\n  first  \nsecond"));
    try std.testing.expectEqualStrings("", titleLine(" \n\t"));
    try std.testing.expectEqualStrings("next tweaks:", titleLine("next tweaks:\r    1. the status line"));
    try std.testing.expectEqualStrings("title", titleLine("title \x1b[2Jcleared"));
    const long = "é" ** 40;
    const cut = titleLine(long);
    try std.testing.expectEqual(@as(usize, 60), cut.len);
    try std.testing.expect(std.unicode.utf8ValidateSlice(cut));
}

fn defaultModel() ai_types.Model {
    return .{
        .id = "claude-sonnet-5-5",
        .name = "Claude Sonnet 5.5",
        .api = "anthropic-messages",
        .provider = "anthropic",
        .base_url = anthropic_messages_base_url,
        .reasoning = true,
        .input = &.{"text"},
        .cost = .{ .input = 2.0, .output = 10.0, .cache_read = 0.10, .cache_write = 2.5 },
        .context_window = 200_000,
        .max_tokens = 128_000,
    };
}

fn preferredContextWindow(stored: ?u32, flag: ?u32) ?u32 {
    return flag orelse stored;
}

pub const over_oap_notice = "oapx tui --attach: this session runs on the hub's endpoint. Resume and automatic worktrees are not carried over the hub, the context window, output limit, permission mode and workspace are fixed when the session opens, and an \"always\" answer to a tool approval applies to that call only unless the endpoint offers it; run oapx without --attach for them.";
pub const over_oap_setting_refusal = tui_commands.over_oap_setting_refusal;
pub const over_oap_compaction_refusal = "this session's endpoint does not take a compaction over OAP; run oapx without --attach to compact.";

pub fn run(allocator: std.mem.Allocator, io: std.Io, context_window: ?u32) !void {
    return runWith(allocator, io, context_window, .local);
}

pub const Execution = union(enum) {
    local,
    in_process,
    attach: struct { url: []const u8, adapter: []const u8 },
};

pub fn runWith(allocator: std.mem.Allocator, io: std.Io, context_window: ?u32, execution_mode: Execution) !void {
    var environ_map = try compat.createEnvMap(allocator);
    defer environ_map.deinit();

    var stderr_redirect = redirectStderrToLog(allocator, &environ_map);
    defer stderr_redirect.restore();
    if (stderr_redirect.active()) std.debug.print("--- oapx terminal UI session started at {d} ms (stderr redirected here while the TUI owns the terminal) ---\n", .{compat.time.nowMillis()});

    const fixture = try FixtureRuntime.fromEnv(allocator, &environ_map);
    defer if (fixture) |runtime| runtime.deinit();

    var production = try ProductionRuntime.init(allocator, .{ .fixture = fixture != null });
    defer production.deinit();
    production.initBridge();

    var options = production.options();
    options.context_window = preferredContextWindow(options.context_window, context_window);
    if (fixture) |runtime| {
        options.protocol = runtime.provider.protocolClient();
        options.generate_titles = false;
    }
    var history_store: ?session_store.Store = session_store.Store.initDefault(allocator) catch null;
    defer if (history_store) |*store| store.deinit();
    var execution: ?*tui_oap_execution.OapExecution = null;
    defer if (execution) |owned| owned.destroy();
    if (execution_mode != .local) {
        execution = switch (execution_mode) {
            .attach => |target| try tui_oap_execution.OapExecution.attach(allocator, target.url, target.adapter),
            else => try tui_oap_execution.OapExecution.create(allocator, options),
        };
        if (history_store) |*store| execution.?.setHistory(.{ .ctx = store, .load = loadSavedHistory });
        if (history_store) |*store| execution.?.setTranscripts(.{ .ctx = store, .save = saveSessionTranscript });
        options.remote = execution.?.remote();
        if (execution_mode == .attach) {
            options.generate_titles = false;
            options.auto_worktree = false;
        }
    }

    var program = zz.Program(TuiModel).initWithOptions(allocator, io, &environ_map, tuiProgramOptions());
    program.model = .{ .options = options, .autocompact = production.mode_settings.autocompact, .verbosity = production.mode_settings.verbosity };
    defer program.deinit();
    try program.run();
}

fn loadSavedHistory(ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror!?[]const ai_types.Message {
    const store: *session_store.Store = @ptrCast(@alignCast(ctx));
    var loaded = try store.load(session_id);
    defer loaded.deinit(store.allocator);
    const messages = try arena.alloc(ai_types.Message, loaded.messages.items.len);
    for (loaded.messages.items, messages) |message, *slot| slot.* = try ai_types.cloneMessage(arena, message);
    return messages;
}

fn saveSessionTranscript(ctx: *anyopaque, allocator: std.mem.Allocator, session_id: []const u8, index: usize, history: []const ai_types.Message) ?[]u8 {
    const store: *session_store.Store = @ptrCast(@alignCast(ctx));
    const saved = store.saveTranscript(session_id, index, history) catch return null;
    defer store.allocator.free(saved);
    return allocator.dupe(u8, saved) catch null;
}

const StderrRedirect = struct {
    saved_fd: ?std.posix.fd_t = null,
    log_fd: ?std.posix.fd_t = null,

    fn active(self: *const StderrRedirect) bool {
        return self.log_fd != null;
    }

    fn restore(self: *StderrRedirect) void {
        if (comptime @import("builtin").os.tag == .windows) return;
        if (self.saved_fd) |saved| {
            _ = std.c.dup2(saved, std.posix.STDERR_FILENO);
            _ = std.c.close(saved);
        }
        if (self.log_fd) |fd| _ = std.c.close(fd);
        self.* = .{};
    }
};

pub fn stderrLogPath(allocator: std.mem.Allocator, home: []const u8) ![]u8 {
    return std.fs.path.join(allocator, &.{ home, ".oapx", "tui-stderr.log" });
}

fn redirectStderrToLog(allocator: std.mem.Allocator, environ_map: *const std.process.Environ.Map) StderrRedirect {
    var redirect: StderrRedirect = .{};
    if (comptime @import("builtin").os.tag == .windows) return redirect;
    const fd = openStderrLog(allocator, environ_map) catch openDevNull() catch return redirect;
    const saved = std.c.dup(std.posix.STDERR_FILENO);
    if (saved < 0) {
        _ = std.c.close(fd);
        return redirect;
    }
    if (std.c.dup2(fd, std.posix.STDERR_FILENO) < 0) {
        _ = std.c.close(saved);
        _ = std.c.close(fd);
        return redirect;
    }
    redirect.saved_fd = saved;
    redirect.log_fd = fd;
    return redirect;
}

fn openStderrLog(allocator: std.mem.Allocator, environ_map: *const std.process.Environ.Map) !std.posix.fd_t {
    const home = environ_map.get("HOME") orelse return error.HomeNotFound;
    if (home.len == 0) return error.HomeNotFound;
    const dir = try std.fs.path.join(allocator, &.{ home, ".oapx" });
    defer allocator.free(dir);
    try compat.fs.createDir(compat.fs.getCwd(), dir);
    const path = try stderrLogPath(allocator, home);
    defer allocator.free(path);
    return std.posix.openat(std.posix.AT.FDCWD, path, .{ .ACCMODE = .WRONLY, .CREAT = true, .APPEND = true, .CLOEXEC = true }, 0o600);
}

fn openDevNull() !std.posix.fd_t {
    return std.posix.openat(std.posix.AT.FDCWD, "/dev/null", .{ .ACCMODE = .WRONLY, .CLOEXEC = true }, 0);
}

test "stderr log path lives under the makai home directory" {
    const path = try stderrLogPath(std.testing.allocator, "/tmp/home");
    defer std.testing.allocator.free(path);
    try std.testing.expectEqualStrings("/tmp/home/.oapx/tui-stderr.log", path);
}

fn tuiProgramOptions() zz.Options {
    return .{ .kitty_keyboard = true, .mouse = false, .alternate_scroll = false, .alt_screen = false, .inline_bottom_viewport = true, .cursor = false, .ctrl_c_quits = false };
}

pub fn tuiProgramOptionsForTest() zz.Options {
    if (!@import("builtin").is_test) @compileError("test-only helper");
    return tuiProgramOptions();
}

const TestContext = struct {
    arena: std.heap.ArenaAllocator,
    env: zz.Environment,
    ctx: zz.Context,

    fn setup(self: *TestContext) void {
        self.arena = std.heap.ArenaAllocator.init(std.testing.allocator);
        self.env = .{};
        self.ctx = zz.Context.init(self.arena.allocator(), std.testing.allocator, std.testing.io, &self.env);
    }

    fn deinit(self: *TestContext) void {
        self.ctx.deinit();
        self.arena.deinit();
    }
};

test "App init seeds registered tools from runtime" {
    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();
    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();

    try std.testing.expect(app.state.registered_tools.items.len >= 4);
    try std.testing.expectEqual(app.runtime.?.availableTools().len, app.state.registered_tools.items.len);
    try std.testing.expectEqualStrings("Shell", app.state.registered_tools.items[0].name);
    try std.testing.expect(app.runtime.?.permission_engine.?.workspace_root.len > 0);
}

test "App applies a staged catalog once the runtime is idle" {
    const extra_model = ai_types.Model{
        .id = "temporary-extra-model",
        .name = "Temporary Extra",
        .api = "test-api",
        .provider = "test",
        .base_url = "https://example.invalid",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{defaultModel()}, .initial_model_id = defaultModel().id });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;

    const staged = try std.testing.allocator.alloc(ai_types.Model, 2);
    staged[0] = try ai_types.cloneModel(std.testing.allocator, defaultModel());
    staged[1] = try ai_types.cloneModel(std.testing.allocator, extra_model);
    app.pending_models = staged;
    try app.applyPendingModels();
    try std.testing.expect(app.pending_models == null);
    try std.testing.expectEqual(@as(usize, 2), runtime.availableModels().len);
    try std.testing.expectEqualStrings(defaultModel().id, runtime.currentModel().?.id);
    var noted = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.eql(u8, entry.text.items, "model catalog refreshed")) noted = true;
    }
    try std.testing.expect(noted);
}

test "App steers a model switch and defers run-end commands while a run streams, then applies them" {
    var env = try TempHome.init("home-defer-commands");
    defer env.deinit();
    var other = defaultModel();
    other.id = "other-model";
    other.name = "Other";
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ defaultModel(), other }, .initial_model_id = defaultModel().id });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;

    app.state.status.streaming = true;
    try app.submit("/model other-model");
    try std.testing.expectEqual(@as(?usize, 1), runtime.pending_model_index);
    try std.testing.expectEqualStrings(defaultModel().id, runtime.currentModel().?.id);
    try app.submit("/model missing-model");
    try std.testing.expectEqualStrings("no model named missing-model", app.state.transcript.items[app.state.transcript.items.len - 1].text.items);
    try app.submit("/output 4096");
    try std.testing.expectEqual(@as(usize, 1), app.deferred_commands.items.len);
    try std.testing.expectEqualStrings("/output 4096 runs when this run ends", app.state.transcript.items[app.state.transcript.items.len - 1].text.items);

    app.state.status.streaming = false;
    try app.runDeferredAfterRun();
    try std.testing.expectEqualStrings("other-model", runtime.currentModel().?.id);
    try std.testing.expectEqualStrings("other-model", app.state.status.model);
    try std.testing.expect(runtime.pending_model_index == null);
    try std.testing.expectEqual(@as(usize, 0), app.deferred_commands.items.len);
    try std.testing.expectEqual(agent.OutputSetting{ .tokens = 4096 }, runtime.outputSetting());
}

test "a pending model switch applies once idle even with follow-ups queued, while held commands wait" {
    var env = try TempHome.init("home-switch-with-queue");
    defer env.deinit();
    var other = defaultModel();
    other.id = "other-model";
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ defaultModel(), other }, .initial_model_id = defaultModel().id });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;

    app.state.status.streaming = true;
    try app.submit("/model other-model");
    try app.submit("/output 4096");
    app.state.status.streaming = false;
    app.state.queue.follow_up = 1;

    try app.runDeferredAfterRun();
    try std.testing.expectEqualStrings("other-model", runtime.currentModel().?.id);
    try std.testing.expectEqual(@as(usize, 1), app.deferred_commands.items.len);
}

test "a refreshed model list keeps a pending switch it still lists and reports one it drops" {
    var other = defaultModel();
    other.id = "other-model";
    var third = defaultModel();
    third.id = "third-model";
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ defaultModel(), other }, .initial_model_id = defaultModel().id });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;

    _ = try runtime.requestModelSwitch("other-model");
    _ = try app.applyModels(&[_]ai_types.Model{ third, other, defaultModel() });
    try std.testing.expectEqual(@as(?usize, 1), runtime.pending_model_index);
    try std.testing.expectEqualStrings("other-model", runtime.models[runtime.pending_model_index.?].id);

    _ = try app.applyModels(&[_]ai_types.Model{ defaultModel(), third });
    try std.testing.expect(runtime.pending_model_index == null);
    const said = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.@"error", said.kind);
    try std.testing.expect(std.mem.indexOf(u8, said.text.items, "other-model was dropped") != null);
}

test "status reports the session, model, usage, run, settings and auth in one place" {
    var env = try TempHome.init("home-status-report");
    defer env.deinit();
    var other = defaultModel();
    other.id = "other-model";
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ defaultModel(), other }, .initial_model_id = defaultModel().id });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;
    app.session_title = try std.testing.allocator.dupe(u8, "oap-prs");
    try app.state.status.setModel(std.testing.allocator, defaultModel().id, defaultModel().provider);

    try app.state.applyEvent(.{ .message_end = .{ .role = .assistant, .output_tokens = 30, .input_tokens = 1_000, .cache_read_tokens = 800 } });
    try app.state.applyEvent(.{ .message_end = .{ .role = .assistant, .output_tokens = 20, .input_tokens = 500, .cache_read_tokens = 0 } });
    app.state.status.streaming = true;
    try app.submit("/model other-model");
    try app.submit("/output 4096");
    app.state.verbosity.status = .verbose;

    const report = try app.statusReport(std.testing.allocator);
    defer std.testing.allocator.free(report);
    for ([_][]const u8{
        "Session\n",                                      "title           oap-prs",
        "\nModel\n",                                     "\nUsage\n",
        "last reply      500 in, 20 out, 0 cache read",    "this sitting    1500 in, 50 out, 800 cache read",
        "\nRun\n",                                       "state           streaming",
        "held commands   /output 4096",                   "model switch    to anthropic/other-model",
        "\nSettings\n",                                  "status verbose",
        "\nAuth\n",                                      "signed in       ",
    }) |needle| {
        if (std.mem.indexOf(u8, report, needle) == null) {
            std.debug.print("missing {s} in:\n{s}\n", .{ needle, report });
            return error.TestExpectedStatusLine;
        }
    }
}

test "App refreshes runtime models after login" {
    const extra_model = ai_types.Model{
        .id = "temporary-extra-model",
        .name = "Temporary Extra",
        .api = "test-api",
        .provider = "test",
        .base_url = "https://example.invalid",
        .reasoning = false,
        .input = &.{"text"},
        .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
        .context_window = 1024,
        .max_tokens = 256,
    };
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ extra_model, defaultModel() }, .initial_model_id = "temporary-extra-model" });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;

    try std.testing.expectEqual(@as(usize, 2), runtime.availableModels().len);
    try std.testing.expect(try app.refreshModels());
    try std.testing.expectEqual(@as(usize, 1), runtime.availableModels().len);
    try std.testing.expectEqualStrings(defaultModel().id, runtime.currentModel().?.id);
    try std.testing.expectEqualStrings(defaultModel().id, app.state.status.model);
    app.state.status.context_limit = 1;
    try std.testing.expect(!try app.refreshModels());
    try std.testing.expectEqual(@as(u64, runtime.contextWindow()), @as(u64, app.state.status.context_limit));
}

const TempHome = struct {
    tmp: std.testing.TmpDir,
    home: []u8,
    previous: ?[]u8,

    fn init(sub: []const u8) !TempHome {
        var tmp = std.testing.tmpDir(.{});
        errdefer tmp.cleanup();
        const home = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, sub });
        errdefer std.testing.allocator.free(home);
        try compat.fs.createDir(compat.fs.getCwd(), home);
        const previous = std.process.Environ.getAlloc(std.testing.environ, std.testing.allocator, "HOME") catch null;
        const home_z = try std.testing.allocator.dupeZ(u8, home);
        defer std.testing.allocator.free(home_z);
        _ = setenv("HOME", home_z.ptr, 1);
        return .{ .tmp = tmp, .home = home, .previous = previous };
    }

    fn deinit(self: *TempHome) void {
        if (self.previous) |value| {
            if (std.testing.allocator.dupeZ(u8, value) catch null) |home_z| {
                defer std.testing.allocator.free(home_z);
                _ = setenv("HOME", home_z.ptr, 1);
            }
            std.testing.allocator.free(value);
        } else {
            _ = unsetenv("HOME");
        }
        std.testing.allocator.free(self.home);
        self.tmp.cleanup();
    }
};

test "App stores a custom provider key under its own id" {
    var env = try TempHome.init("home-custom-key");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    const creds = oauth_storage.Credentials{
        .refresh = try std.testing.allocator.dupe(u8, ""),
        .access = try std.testing.allocator.dupe(u8, "gateway-secret"),
        .expires = std.math.maxInt(i64),
    };
    defer creds.deinit(std.testing.allocator);

    try app.saveLoginCredentials("gateway", creds, true);

    var storage = try oauth_storage.AuthStorage.loadFromFile(std.testing.allocator);
    defer storage.deinit();
    const auth = storage.providers.get("gateway") orelse return error.MissingCustomAuth;
    switch (auth) {
        .api_key => |key| try std.testing.expectEqualStrings("gateway-secret", key),
        else => return error.UnexpectedAuthKind,
    }
}

test "App logout resolves the aliases login accepts" {
    try std.testing.expectEqualStrings("openai-codex", App.logoutProviderId("codex"));
    try std.testing.expectEqualStrings("github-copilot", App.logoutProviderId("github"));
    try std.testing.expectEqualStrings("kimi", App.logoutProviderId("moonshot"));
    try std.testing.expectEqualStrings("opencode-go", App.logoutProviderId("opencode-go"));
    try std.testing.expectEqualStrings("gateway", App.logoutProviderId("gateway"));
}

test "App logout removes only that provider's saved credential" {
    var env = try TempHome.init("home-logout");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    inline for (.{ "gateway", "other-gateway" }) |id| {
        const creds = oauth_storage.Credentials{
            .refresh = try std.testing.allocator.dupe(u8, ""),
            .access = try std.testing.allocator.dupe(u8, id ++ "-secret"),
            .expires = std.math.maxInt(i64),
        };
        defer creds.deinit(std.testing.allocator);
        try app.saveLoginCredentials(id, creds, true);
    }
    const config_path = try std.fs.path.join(std.testing.allocator, &.{ env.home, ".oapx", "providers.json" });
    defer std.testing.allocator.free(config_path);
    try compat.fs.writeFile(compat.fs.getCwd(), config_path,
        \\{"providers":[{"id":"gateway","base_url":"https://gw.test","auth":{"env":"HOME"}}]}
    );

    app.login = try tui_login.LoginSession.startApiKey(std.testing.allocator, "gateway");
    try app.submit("/logout gateway");
    try std.testing.expectEqualStrings("a login to gateway is in progress; cancel it before logging out", app.state.transcript.items[0].text.items);
    app.finishLogin();
    app.state.clearTranscript();

    try app.submit("/logout gateway");
    try std.testing.expectEqualStrings("logged out of gateway", app.state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("HOME is still set, so gateway stays signed in", app.state.transcript.items[1].text.items);
    const before = app.state.transcript.items.len;
    try app.submit("/logout gateway");
    try std.testing.expectEqualStrings("no saved credential for gateway", app.state.transcript.items[before].text.items);

    var storage = try oauth_storage.AuthStorage.loadFromFile(std.testing.allocator);
    defer storage.deinit();
    try std.testing.expect(!storage.providers.contains("gateway"));
    try std.testing.expect(storage.providers.contains("other-gateway"));
}

test "App /provider add declares a provider that /login then accepts" {
    var env = try TempHome.init("home-provider-add");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try std.testing.expect(!app.isDeclaredCustomProvider("gateway"));

    try app.submit("/provider add gateway https://gw.test/v1 --api openai-responses");
    const said = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.system, said.kind);
    try std.testing.expect(std.mem.endsWith(u8, said.text.items, "Run /login gateway to add its key."));
    try std.testing.expect(app.isDeclaredCustomProvider("gateway"));

    try app.submit("/login gateway");
    const pending = app.login orelse return error.TestExpectedLogin;
    try std.testing.expectEqualStrings("gateway", pending.provider_id);
    for (app.state.transcript.items) |entry| try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "unknown login provider") == null);
    app.finishLogin();

    try app.submit("/provider add gateway https://other.test");
    const refused = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqualStrings("could not declare gateway: DuplicateProviderId", refused.text.items);

    inline for (.{
        "/provider add other https://other.test --env A --no-auth",
        "/provider add other https://other.test --env --no-auth",
        "/provider add other https://other.test --api --env A",
        "/provider add --env A https://other.test",
    }) |input| {
        try app.submit(input);
        try std.testing.expectEqualStrings(tui_commands.provider_usage, app.state.transcript.items[app.state.transcript.items.len - 1].text.items);
    }

    const providers = try custom_providers.load(std.testing.allocator, custom_providers.max_config_bytes);
    defer custom_providers.deinitProviders(std.testing.allocator, providers);
    try std.testing.expectEqual(@as(usize, 1), providers.len);
    try std.testing.expectEqualStrings("openai-responses", providers[0].api);
}

test "App /provider del deletes a declared provider and refuses one that is not declared" {
    var env = try TempHome.init("home-provider-del");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("/provider add gateway https://gw.test/v1");
    try app.submit("/provider add other https://other.test --no-auth");
    try std.testing.expect(app.isDeclaredCustomProvider("gateway"));

    try app.submit("/provider del gateway");
    const said = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.system, said.kind);
    try std.testing.expect(std.mem.startsWith(u8, said.text.items, "deleted gateway from "));
    try std.testing.expect(!app.isDeclaredCustomProvider("gateway"));
    try std.testing.expect(app.isDeclaredCustomProvider("other"));

    try app.submit("/provider del gateway");
    try std.testing.expectEqualStrings("could not delete gateway: ProviderNotDeclared", app.state.transcript.items[app.state.transcript.items.len - 1].text.items);

    try app.submit("/provider del");
    try std.testing.expectEqualStrings(tui_commands.provider_usage, app.state.transcript.items[app.state.transcript.items.len - 1].text.items);
}

test "App /provider list names what is declared and says so when nothing is" {
    var env = try TempHome.init("home-provider-list");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("/provider list");
    const empty = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.system, empty.kind);
    try std.testing.expect(std.mem.startsWith(u8, empty.text.items, "no providers are declared in "));
    try std.testing.expect(std.mem.endsWith(u8, empty.text.items, "/provider add <id> <base_url> declares one"));

    try app.submit("/provider add gateway https://gw.test/v1 --no-auth");
    try app.submit("/provider add other https://other.test --env OTHER_KEY");
    try app.submit("/provider list");
    const listed = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.system, listed.kind);
    try std.testing.expect(std.mem.startsWith(u8, listed.text.items, "providers declared in "));
    try std.testing.expect(std.mem.indexOf(u8, listed.text.items, "gateway (gateway), openai-completions, https://gw.test, no credential, every discovered model") != null);
    try std.testing.expect(std.mem.indexOf(u8, listed.text.items, "other (other), openai-completions, https://other.test, key from OTHER_KEY, every discovered model") != null);

    try app.submit("/provider list extra");
    try std.testing.expectEqualStrings(tui_commands.provider_usage, app.state.transcript.items[app.state.transcript.items.len - 1].text.items);

    const config_path = try std.fs.path.join(std.testing.allocator, &.{ env.home, ".oapx", "providers.json" });
    defer std.testing.allocator.free(config_path);
    try compat.fs.writeFile(compat.fs.getCwd(), config_path, "{\"overrides\":[{\"id\":\"openai\",\"base_url\":\"https://proxy.test\"}]}");

    try app.submit("/provider list");
    const only_overrides = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expect(std.mem.startsWith(u8, only_overrides.text.items, "overrides on catalogued rows in "));
    try std.testing.expect(std.mem.indexOf(u8, only_overrides.text.items, "  openai: base_url") != null);
}

test "App /provider list reports a config it cannot read instead of calling it empty" {
    var env = try TempHome.init("home-provider-list-unreadable");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();

    const config_path = try std.fs.path.join(std.testing.allocator, &.{ env.home, ".oapx", "providers.json" });
    defer std.testing.allocator.free(config_path);
    try app.submit("/provider add gateway https://gw.test/v1");
    const oversized = try std.testing.allocator.alloc(u8, custom_providers.max_config_bytes + 1);
    defer std.testing.allocator.free(oversized);
    @memset(oversized, ' ');
    try compat.fs.writeFile(compat.fs.getCwd(), config_path, oversized);

    try app.submit("/provider list");
    const said = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.@"error", said.kind);
    try std.testing.expect(std.mem.startsWith(u8, said.text.items, "could not read the provider config: "));
}

test "App only offers an api-key login for a declared custom provider" {
    var env = try TempHome.init("home-custom-login");
    defer env.deinit();

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try std.testing.expect(!app.isDeclaredCustomProvider("gateway"));

    const makai_dir = try std.fs.path.join(std.testing.allocator, &.{ env.home, ".oapx" });
    defer std.testing.allocator.free(makai_dir);
    try compat.fs.createDir(compat.fs.getCwd(), makai_dir);
    const config_path = try std.fs.path.join(std.testing.allocator, &.{ makai_dir, "providers.json" });
    defer std.testing.allocator.free(config_path);
    try compat.fs.writeFile(compat.fs.getCwd(), config_path,
        \\{"providers":[{"id":"gateway","base_url":"https://gw.test"}]}
    );

    try std.testing.expect(app.isDeclaredCustomProvider("gateway"));
    try std.testing.expect(!app.isDeclaredCustomProvider("not-declared"));
}

test "TUI program enables enhanced keyboard protocol" {
    try std.testing.expect(tuiProgramOptions().kitty_keyboard);
}

test "TUI program preserves native text selection" {
    try std.testing.expect(!tuiProgramOptions().mouse);
    try std.testing.expect(!tuiProgramOptions().alternate_scroll);
    try std.testing.expect(!tuiProgramOptions().alt_screen);
    try std.testing.expect(tuiProgramOptions().inline_bottom_viewport);
}

test "TUI program routes Ctrl+C to the model and hides the terminal cursor" {
    try std.testing.expect(!tuiProgramOptions().ctrl_c_quits);
    try std.testing.expect(!tuiProgramOptions().cursor);
}

test "the terminal tab title follows the session title and is cleared on quit" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();

    const untitled = model.update(.{ .window_size = .{ .width = 80, .height = 24 } }, &tctx.ctx);
    try std.testing.expect(std.meta.activeTag(untitled) != .batch);

    try model.app.?.renameSession("Freeze hunt");
    const titled = model.update(.{ .window_size = .{ .width = 80, .height = 24 } }, &tctx.ctx);
    try std.testing.expectEqualStrings("oapx \u{b7} Freeze hunt", titled.batch[0].set_title);

    const unchanged = model.update(.{ .window_size = .{ .width = 80, .height = 24 } }, &tctx.ctx);
    try std.testing.expect(std.meta.activeTag(unchanged) != .batch);

    try std.testing.expectEqualStrings("", model.retitle("").?);
}

test "TuiModel Ctrl+C clears a draft first and quits on the second press" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("draft");

    const first = model.update(.{ .key = .{ .key = .{ .char = 'c' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, first);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
    try std.testing.expect(model.app.?.interrupt_armed_tick != null);

    const second = model.update(.{ .key = .{ .key = .{ .char = 'c' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).quit, second);
}

test "TuiModel Ctrl+C quits immediately on an empty idle composer" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();

    const cmd = model.update(.{ .key = .{ .key = .{ .char = 'c' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).quit, cmd);
}

test "TuiModel window resize refills the inline window with the history tail" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 60;
    tctx.ctx.height = 14;
    for (0..12) |i| {
        const text = try std.fmt.allocPrint(std.testing.allocator, "history entry {d}", .{i});
        defer std.testing.allocator.free(text);
        try model.app.?.state.appendTranscript(.assistant, text);
    }
    model.app.?.inline_history_flushed = model.app.?.state.transcript.items.len;

    _ = model.update(.{ .window_size = .{ .width = 60, .height = 14 } }, &tctx.ctx);
    try std.testing.expect(!tctx.ctx.hasPendingAbove());
    const frame = model.view(&tctx.ctx);
    try std.testing.expectEqual(@as(usize, 14), TuiModel.countLines(frame));
    try std.testing.expect(std.mem.indexOf(u8, frame, "history entry 11") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "history entry 0") == null);
    var lines = std.mem.splitScalar(u8, frame, '\n');
    while (lines.next()) |line| try std.testing.expect(tui_text.visibleWidth(line) <= 60);
}

test "TuiModel inline flush keeps scrollback and window contiguous" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 60;
    tctx.ctx.height = 14;
    for (0..12) |i| {
        const text = try std.fmt.allocPrint(std.testing.allocator, "history entry {d}", .{i});
        defer std.testing.allocator.free(text);
        try model.app.?.state.appendTranscript(.assistant, text);
    }

    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    const above = try tctx.ctx.takeAbove(std.testing.allocator);
    defer std.testing.allocator.free(above);
    const frame = model.view(&tctx.ctx);
    try std.testing.expectEqual(@as(usize, 14), TuiModel.countLines(frame));
    try std.testing.expect(std.mem.indexOf(u8, above, "history entry 0") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "history entry 11") != null);
    try std.testing.expect(model.app.?.inline_flushed_rows > 0 or model.app.?.inline_history_flushed > 0);

    const stream = try TuiModel.renderInlineStream(std.testing.allocator, &model.app.?.state, 0, 0, 60, true);
    defer std.testing.allocator.free(stream);
    const joined = try std.mem.concat(std.testing.allocator, u8, &.{ above, frame });
    defer std.testing.allocator.free(joined);
    var expected = std.mem.splitScalar(u8, stream, '\n');
    var actual = std.mem.splitScalar(u8, joined, '\n');
    while (expected.next()) |row| {
        const got = actual.next() orelse return error.TestUnexpectedResult;
        try std.testing.expectEqualStrings(std.mem.trimEnd(u8, row, " "), std.mem.trimEnd(u8, got, " "));
    }
    try std.testing.expectEqualStrings("", std.mem.trimEnd(u8, actual.next() orelse return error.TestUnexpectedResult, " "));
}

test "App inline flush stop follows the reconciliation floor" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var resolution = try app.state.resolveToolOccurrenceForTest("call-h", "shell_execute", "{\"command\":\"pwd\"}", .live_intent, .running);
    try app.state.appendToolSummaryTranscript("running now", resolution.tool.id);
    try app.state.appendTranscript(.assistant, "later text");
    app.state.advanceSummaryScanFloor();
    try std.testing.expectEqual(@as(usize, 0), TuiModel.inlineFlushStop(&app));

    resolution.tool.terminal_evidence = .both;
    app.state.advanceSummaryScanFloor();
    try std.testing.expectEqual(@as(usize, 2), TuiModel.inlineFlushStop(&app));
}

test "TuiModel inline frame fills the viewport when there is nothing to show yet" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 60;
    tctx.ctx.height = 20;

    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    const empty = model.view(&tctx.ctx);
    try std.testing.expectEqual(@as(usize, 20), TuiModel.countLines(empty));

    try model.app.?.state.replaceComposerBuffer("/m");
    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    const with_palette = model.view(&tctx.ctx);
    try std.testing.expectEqual(@as(usize, 20), TuiModel.countLines(with_palette));
    try std.testing.expect(std.mem.indexOf(u8, with_palette, "/model") != null);

    var rows = std.mem.splitScalar(u8, with_palette, '\n');
    var last: []const u8 = "";
    while (rows.next()) |row| last = row;
    try std.testing.expect(last.len > 0);
    try std.testing.expect(std.mem.startsWith(u8, with_palette, "\n"));
}

test "TuiModel inline frame keeps the composer on the bottom row across a picker" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 60;
    tctx.ctx.height = 16;
    for (0..12) |i| {
        const text = try std.fmt.allocPrint(std.testing.allocator, "history entry {d}", .{i});
        defer std.testing.allocator.free(text);
        try model.app.?.state.appendTranscript(.assistant, text);
    }
    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    tctx.ctx.above_buffer.clearRetainingCapacity();
    const before = try std.testing.allocator.dupe(u8, model.view(&tctx.ctx));
    defer std.testing.allocator.free(before);
    try std.testing.expectEqual(@as(usize, 16), TuiModel.countLines(before));

    model.app.?.state.mode = .picker;
    model.app.?.state.picker_kind = .model;
    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    const during = model.view(&tctx.ctx);
    try std.testing.expectEqual(@as(usize, 16), TuiModel.countLines(during));
    try std.testing.expect(std.mem.indexOf(u8, during, "Select model") != null);
    try std.testing.expect(std.mem.indexOf(u8, during, "history entry 11") != null);
    try std.testing.expect(!tctx.ctx.hasPendingAbove());

    model.app.?.state.mode = .normal;
    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    try std.testing.expect(!tctx.ctx.hasPendingAbove());
    try std.testing.expectEqualStrings(before, model.view(&tctx.ctx));
}

test "TuiModel Escape clears the draft before anything else" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("draft");

    _ = model.update(.{ .key = .{ .key = .escape } }, &tctx.ctx);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
    try std.testing.expectEqual(tui_state.AppMode.normal, model.app.?.state.mode);
}

test "TuiModel Tab completes the first matching slash command" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("/perm");

    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqualStrings("/permissions ", model.app.?.state.composer.text());
    try std.testing.expect(model.app.?.slashQuery() == null);
}

test "TuiModel arrow keys move the slash palette selection that Tab completes" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 30;
    try model.app.?.state.replaceComposerBuffer("/perm");

    _ = model.update(.{ .key = .{ .key = .down } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 1), model.app.?.slashSelection());
    const frame = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, frame, tui_theme.glyph.select ++ " /perm [ask|bypass]") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, tui_theme.glyph.select ++ " /permissions") == null);

    _ = model.update(.{ .key = .{ .key = .up } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .up } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.slashSelection());
    _ = model.update(.{ .key = .{ .key = .down } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqualStrings("/perm ", model.app.?.state.composer.text());
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.composer.history.items.len);
}

test "TuiModel Enter runs the slash command the palette selects" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("/");

    _ = model.update(.{ .key = .{ .key = .down } }, &tctx.ctx);
    try std.testing.expectEqualStrings("model", slashMatch("", model.app.?.slashSelection()).?.name);
    _ = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(tui_state.AppMode.picker, model.app.?.state.mode);
    try std.testing.expectEqual(tui_state.PickerKind.model, model.app.?.state.picker_kind);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
}

test "TuiModel typing after a palette move resets the selection" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("/");

    _ = model.update(.{ .key = .{ .key = .down } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .down } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 2), model.app.?.slashSelection());
    _ = model.update(.{ .key = .{ .key = .{ .char = 'p' } } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.slashSelection());
    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqualStrings("/permissions ", model.app.?.state.composer.text());
}

test "TuiModel arrow keys keep walking history once a recalled entry is shown" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.recordComposerHistory("first prompt");
    try model.app.?.state.recordComposerHistory("/model");

    _ = model.update(.{ .key = .{ .key = .up } }, &tctx.ctx);
    try std.testing.expectEqualStrings("/model", model.app.?.state.composer.text());
    _ = model.update(.{ .key = .{ .key = .up } }, &tctx.ctx);
    try std.testing.expectEqualStrings("first prompt", model.app.?.state.composer.text());
}

test "TuiModel Tab queues a follow-up while a turn streams and shows it until it is consumed" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 24;
    model.app.?.state.status.streaming = true;
    try model.app.?.state.replaceComposerBuffer("  then open a PR  ");

    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 1), mock.follow_up_count);
    try std.testing.expectEqual(@as(usize, 0), mock.steer_count);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
    try std.testing.expectEqual(@as(usize, 1), model.app.?.state.pending_follow_ups.items.len);
    try std.testing.expectEqualStrings("then open a PR", model.app.?.state.pending_follow_ups.items[0]);
    try std.testing.expectEqual(@as(usize, 1), model.app.?.state.queue.follow_up);
    for (model.app.?.state.transcript.items) |entry| try std.testing.expect(!std.mem.eql(u8, entry.text.items, "then open a PR"));
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "queued  then open a PR") != null);

    mock.queued_counts.follow_up = 0;
    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.pending_follow_ups.items.len);
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "queued  then open a PR") == null);
}

test "a held compaction waits while follow-ups are queued instead of blocking on the run they resume" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.pending_compaction = try std.testing.allocator.dupe(u8, "the parser");
    app.state.queue.follow_up = 1;

    try app.startCompactionAfterRun(true);
    try std.testing.expectEqualStrings("the parser", app.pending_compaction.?);

    app.state.queue.follow_up = 0;
    app.state.status.streaming = true;
    try app.startCompactionAfterRun(false);
    try std.testing.expectEqualStrings("the parser", app.pending_compaction.?);
}

test "TuiModel Tab queues no follow-up while idle or for a slash draft, and defers a run-end command" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("plain draft");

    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), mock.follow_up_count);
    try std.testing.expectEqualStrings("plain draft", model.app.?.state.composer.text());

    model.app.?.state.status.streaming = true;
    try model.app.?.state.replaceComposerBuffer("/ab");
    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), mock.follow_up_count);
    try std.testing.expectEqualStrings("/abort", model.app.?.state.composer.text());

    for ([_][]const u8{ "/model ", "  /status now" }) |draft| {
        try model.app.?.state.replaceComposerBuffer(draft);
        _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
        try std.testing.expectEqual(@as(usize, 0), mock.follow_up_count);
        try std.testing.expectEqualStrings(draft, model.app.?.state.composer.text());
    }
    try model.app.?.state.replaceComposerBuffer("/model claude");
    _ = model.update(.{ .key = .{ .key = .tab } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), mock.follow_up_count);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
    try std.testing.expectEqual(@as(usize, 1), model.app.?.deferred_commands.items.len);
    try std.testing.expectEqualStrings("/model claude", model.app.?.deferred_commands.items[0]);
    try std.testing.expect(!try model.app.?.queueFollowUp("/model claude"));
    try std.testing.expectEqual(@as(usize, 0), mock.follow_up_count);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.pending_follow_ups.items.len);
}

test "TuiModel inline flush reserves the queued rows so no row hides behind them" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 60;
    tctx.ctx.height = 14;
    try model.app.?.state.appendQueuedFollowUp("first queued follow-up");
    try model.app.?.state.appendQueuedFollowUp("second queued follow-up");
    for (0..12) |i| {
        const text = try std.fmt.allocPrint(std.testing.allocator, "history entry {d}", .{i});
        defer std.testing.allocator.free(text);
        try model.app.?.state.appendTranscript(.assistant, text);
    }

    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    const above = try tctx.ctx.takeAbove(std.testing.allocator);
    defer std.testing.allocator.free(above);
    const frame = model.view(&tctx.ctx);
    try std.testing.expectEqual(@as(usize, 14), TuiModel.countLines(frame));
    try std.testing.expect(std.mem.indexOf(u8, frame, "queued  second queued follow-up") != null);

    const stream = try TuiModel.renderInlineStream(std.testing.allocator, &model.app.?.state, 0, 0, 60, true);
    defer std.testing.allocator.free(stream);
    const joined = try std.mem.concat(std.testing.allocator, u8, &.{ above, frame });
    defer std.testing.allocator.free(joined);
    var expected = std.mem.splitScalar(u8, stream, '\n');
    var actual = std.mem.splitScalar(u8, joined, '\n');
    while (expected.next()) |row| {
        const got = actual.next() orelse return error.TestUnexpectedResult;
        try std.testing.expectEqualStrings(std.mem.trimEnd(u8, row, " "), std.mem.trimEnd(u8, got, " "));
    }
    try std.testing.expectEqualStrings("", std.mem.trimEnd(u8, actual.next() orelse return error.TestUnexpectedResult, " "));
}

test "TuiModel picker filters by typing and applies the filtered choice" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 30;
    model.app.?.openPicker(.permission);
    try std.testing.expectEqual(@as(usize, 2), model.app.?.pickerMatchCount());

    for ("ASK") |c| _ = model.update(.{ .key = .{ .key = .{ .char = c } } }, &tctx.ctx);
    try std.testing.expectEqualStrings("ASK", model.app.?.state.pickerFilter());
    try std.testing.expectEqual(@as(usize, 1), model.app.?.pickerMatchCount());
    try std.testing.expectEqual(@as(?usize, 1), model.app.?.pickerSourceIndex(0));
    const frame = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, frame, tui_theme.glyph.prompt ++ " ASK") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "ask before tool execution") != null);
    try std.testing.expect(std.mem.indexOf(u8, frame, "run tools without prompts") == null);

    _ = model.update(.{ .key = .{ .key = .{ .char = 'x' } } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.pickerMatchCount());
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "nothing matches") != null);
    _ = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(tui_state.AppMode.picker, model.app.?.state.mode);

    _ = model.update(.{ .key = .{ .key = .backspace } }, &tctx.ctx);
    try std.testing.expectEqualStrings("ASK", model.app.?.state.pickerFilter());
    model.app.?.openPicker(.permission);
    try std.testing.expectEqualStrings("", model.app.?.state.pickerFilter());
}

test "settings picker resolves filtered selection to the visible setting" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.openPicker(.settings);
    try app.state.appendPickerFilter("worktrees");
    try std.testing.expectEqual(@as(usize, 1), app.pickerMatchCount());
    try std.testing.expectEqual(@as(?usize, 1), app.pickerSourceIndex(0));
    try std.testing.expect(app.pickerSourceIndex(1) == null);
}

test "filterMatches needs every term in the label or the detail" {
    try std.testing.expect(filterMatches("", "claude-opus-4", "anthropic"));
    try std.testing.expect(filterMatches("OPUS", "claude-opus-4", "anthropic"));
    try std.testing.expect(filterMatches("anthropic opus", "claude-opus-4", "anthropic"));
    try std.testing.expect(!filterMatches("openai opus", "claude-opus-4", "anthropic"));
    try std.testing.expect(!filterMatches("gpt", "claude-opus-4", "anthropic"));
}

test "TuiModel word editing shortcuts edit the composer" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("alpha beta gamma");

    _ = model.update(.{ .key = .{ .key = .{ .char = 'w' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);
    try std.testing.expectEqualStrings("alpha beta ", model.app.?.state.composer.text());
    _ = model.update(.{ .key = .{ .key = .left, .modifiers = .{ .alt = true } } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 6), model.app.?.state.composer.cursor);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'u' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);
    try std.testing.expectEqualStrings("beta ", model.app.?.state.composer.text());
    _ = model.update(.{ .key = .{ .key = .{ .char = 'k' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
}

test "fixture runtime stays inactive without a non-empty env value" {
    try std.testing.expectEqual(@as(?*FixtureRuntime, null), try FixtureRuntime.fromValue(std.testing.allocator, null));
    try std.testing.expectEqual(@as(?*FixtureRuntime, null), try FixtureRuntime.fromValue(std.testing.allocator, ""));
}

test "fixture runtime streams the env-provided text" {
    var env = try compat.createEnvMap(std.testing.allocator);
    defer env.deinit();
    try env.put(fixture_env_var, "pty fixture reply");
    const fixture = (try FixtureRuntime.fromEnv(std.testing.allocator, &env)).?;
    defer fixture.deinit();

    const client = fixture.provider.protocolClient();
    const stream_ptr = try client.stream(fixture_provider.test_model, .{ .messages = &.{}, .is_owned = false }, .{}, std.testing.allocator);
    defer {
        stream_ptr.deinit();
        std.testing.allocator.destroy(stream_ptr);
    }

    var saw_delta = false;
    while (stream_ptr.wait()) |event| {
        const ev = event;
        defer stream_ptr.releaseEvent(ev);
        if (ev == .text_delta) saw_delta = std.mem.eql(u8, ev.text_delta.delta, "pty fixture reply");
    }
    try std.testing.expect(saw_delta);
    try std.testing.expectEqual(@as(usize, 1), fixture.provider.call_count);
}

test "fixture runtime parses scenario steps" {
    const fixture = (try FixtureRuntime.fromValue(std.testing.allocator, "text:one|tool:workspace_info|hold|error:boom|text:two")).?;
    defer fixture.deinit();

    try std.testing.expectEqual(@as(usize, 5), fixture.steps.items.len);
    try std.testing.expectEqualStrings("one", fixture.steps.items[0].text);
    try std.testing.expectEqualStrings("workspace_info", fixture.steps.items[1].tool_calls[0].name);
    try std.testing.expectEqualStrings("{}", fixture.steps.items[1].tool_calls[0].arguments_json);
    try std.testing.expect(fixture.steps.items[2] == .wait_for_cancel);
    try std.testing.expectEqualStrings("boom", fixture.steps.items[3].provider_error);
    try std.testing.expectEqualStrings("two", fixture.steps.items[4].text);
}

test "fixture runtime parses tool args after the hash" {
    const fixture = (try FixtureRuntime.fromValue(std.testing.allocator, "tool:workspace_info#{\"workspace_root\":\"/tmp\"}")).?;
    defer fixture.deinit();

    try std.testing.expectEqualStrings("workspace_info", fixture.steps.items[0].tool_calls[0].name);
    try std.testing.expectEqualStrings("{\"workspace_root\":\"/tmp\"}", fixture.steps.items[0].tool_calls[0].arguments_json);
}

test "fixture runtime keeps pipe-less text that is not a step" {
    const fixture = (try FixtureRuntime.fromValue(std.testing.allocator, "plain|reply|text")).?;
    defer fixture.deinit();

    try std.testing.expectEqual(@as(usize, 1), fixture.steps.items.len);
    try std.testing.expectEqualStrings("plain|reply|text", fixture.steps.items[0].text);
}

test "fixture runtime unescapes separators inside step payloads" {
    const fixture = (try FixtureRuntime.fromValue(std.testing.allocator, "text:pipe\\|inside|tool:shell_execute#{\"command\":\"printf a \\| cat\"}|text:done")).?;
    defer fixture.deinit();

    try std.testing.expectEqual(@as(usize, 3), fixture.steps.items.len);
    try std.testing.expectEqualStrings("pipe|inside", fixture.steps.items[0].text);
    try std.testing.expectEqualStrings("shell_execute", fixture.steps.items[1].tool_calls[0].name);
    try std.testing.expectEqualStrings("{\"command\":\"printf a | cat\"}", fixture.steps.items[1].tool_calls[0].arguments_json);
    try std.testing.expectEqualStrings("done", fixture.steps.items[2].text);
}

test "fixture runtime unescapes backslashes and keeps lone ones literal" {
    const fixture = (try FixtureRuntime.fromValue(std.testing.allocator, "text:c:\\\\path\\\\\\|end")).?;
    defer fixture.deinit();

    try std.testing.expectEqualStrings("c:\\path\\|end", fixture.steps.items[0].text);
}

test "fixture runtime rejects unknown step after a scenario prefix" {
    try std.testing.expectError(error.UnknownFixtureStepPrefix, FixtureRuntime.fromValue(std.testing.allocator, "text:one|wat"));
    try std.testing.expectError(error.EmptyFixtureStep, FixtureRuntime.fromValue(std.testing.allocator, "text:"));
    try std.testing.expectError(error.EmptyFixtureStep, FixtureRuntime.fromValue(std.testing.allocator, "text:one|"));
}

fn sessionTestApp(base: []const u8, session_id: []const u8) !App {
    var app = App.initWithoutRuntime(std.testing.allocator);
    errdefer app.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    app.session_id = try std.testing.allocator.dupe(u8, session_id);
    try app.state.status.setModel(std.testing.allocator, "model-a", "provider-a");
    return app;
}

fn sessionFileLines(base: []const u8, file_name: []const u8) !std.ArrayList([]const u8) {
    const path = try std.fs.path.join(std.testing.allocator, &.{ base, file_name });
    defer std.testing.allocator.free(path);
    const data = try compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), path, 1024 * 1024);
    errdefer std.testing.allocator.free(data);
    var lines: std.ArrayList([]const u8) = .empty;
    errdefer lines.deinit(std.testing.allocator);
    try lines.append(std.testing.allocator, data);
    var iter = std.mem.splitScalar(u8, std.mem.trimEnd(u8, data, "\n"), '\n');
    while (iter.next()) |line| try lines.append(std.testing.allocator, line);
    return lines;
}

fn freeSessionFileLines(lines: *std.ArrayList([]const u8)) void {
    std.testing.allocator.free(lines.items[0]);
    lines.deinit(std.testing.allocator);
}

test "App saveEvent keeps conversation events in the session and chunks in its stream file" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "split-events");
    defer app.deinit();

    var thinking = tui_runtime.TuiEvent{ .thinking_delta = .{ .content_index = 0, .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "plan")) } };
    defer thinking.deinit(std.testing.allocator);
    app.saveEvent(thinking);

    var approval = tui_runtime.TuiEvent{ .tool_approval_requested = .{
        .tool_call_id = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "call-1")),
        .tool_name = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "shell_execute")),
        .args_json = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "{\"command\":\"pwd\"}")),
    } };
    defer approval.deinit(std.testing.allocator);
    app.saveEvent(approval);

    var update = tui_runtime.TuiEvent{ .tool_execution_update = .{
        .tool_call_id = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "call-1")),
        .tool_name = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "shell_execute")),
        .args_json = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "{\"command\":\"pwd\"}")),
        .partial_result_json = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "{\"stdout\":\"/tmp\"}")),
    } };
    defer update.deinit(std.testing.allocator);
    app.saveEvent(update);

    var provider = tui_runtime.TuiEvent{ .provider_event = .{ .event_json = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "{\"type\":\"done\"}")) } };
    defer provider.deinit(std.testing.allocator);
    app.saveEvent(provider);

    var err = tui_runtime.TuiEvent{ .@"error" = .{ .message = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "boom")) } };
    defer err.deinit(std.testing.allocator);
    app.saveEvent(err);

    var start = tui_runtime.TuiEvent{ .tool_execution_start = .{
        .tool_call_id = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "call-1")),
        .tool_name = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "shell_execute")),
        .args_json = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "{\"command\":\"pwd\"}")),
    } };
    defer start.deinit(std.testing.allocator);
    app.saveEvent(start);

    var loaded = try app.store.?.load("split-events");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 3), loaded.events.items.len);
    try std.testing.expect(loaded.events.items[0] == .tool_approval_requested);
    try std.testing.expect(loaded.events.items[1] == .@"error");
    try std.testing.expect(loaded.events.items[2] == .tool_execution_start);
    try std.testing.expectEqualStrings("{\"command\":\"pwd\"}", loaded.events.items[2].tool_execution_start.args_json.slice());

    var stream = try sessionFileLines(base, "split-events.stream.jsonl");
    defer freeSessionFileLines(&stream);
    try std.testing.expectEqual(@as(usize, 4), stream.items.len);
    try std.testing.expect(std.mem.indexOf(u8, stream.items[1], "\"type\":\"thinking_delta\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, stream.items[2], "\"type\":\"tool_execution_update\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, stream.items[3], "\"type\":\"provider_event\"") != null);
}

test "App writes session metadata when a session starts and when its model changes" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "metadata-changes");
    defer app.deinit();

    app.saveEvent(.{ .turn_start = .{} });
    app.saveEvent(.{ .turn_end = .{ .stop_reason = .stop } });
    try app.state.status.setModel(std.testing.allocator, "model-b", "provider-b");
    app.saveEvent(.{ .turn_start = .{} });

    var lines = try sessionFileLines(base, "metadata-changes.jsonl");
    defer freeSessionFileLines(&lines);
    try std.testing.expectEqual(@as(usize, 4), lines.items.len);
    try std.testing.expect(std.mem.indexOf(u8, lines.items[1], "\"model\":\"model-a\"") != null);
    try std.testing.expect(std.mem.indexOf(u8, lines.items[2], "\"metadata\"") == null);
    try std.testing.expect(std.mem.indexOf(u8, lines.items[3], "\"model\":\"model-b\"") != null);

    var index = try app.store.?.loadIndex("metadata-changes");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("model-b", index.model);
    try std.testing.expect(index.created_at > 0);
}

test "App folds a reply's thinking into one record at the reply's end" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "folded-thinking");
    defer app.deinit();

    app.saveEvent(.{ .message_start = .{ .role = .assistant } });
    for ([_][]const u8{ "first ", "second" }) |part| {
        var delta = tui_runtime.TuiEvent{ .thinking_delta = .{ .content_index = 0, .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, part)) } };
        defer delta.deinit(std.testing.allocator);
        app.saveEvent(delta);
    }
    var end = tui_runtime.TuiEvent{ .message_end = .{ .role = .assistant, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "answer")) } };
    defer end.deinit(std.testing.allocator);
    app.saveEvent(end);

    var loaded = try app.store.?.load("folded-thinking");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 3), loaded.events.items.len);
    try std.testing.expect(loaded.events.items[0] == .message_start);
    try std.testing.expectEqualStrings("first second", loaded.events.items[1].thinking_delta.delta.slice());
    try std.testing.expect(loaded.events.items[2] == .message_end);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
}

test "App splits a reply's thinking into records that fit the session budget" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "long-thinking");
    defer app.deinit();

    const part = "\u{20ac}" ** 1000;
    app.saveEvent(.{ .message_start = .{ .role = .assistant } });
    var sent: usize = 0;
    while (sent <= max_session_event_payload_bytes / App.jsonStringBudget("x")) : (sent += part.len) {
        app.saveEvent(.{ .thinking_delta = .{ .content_index = 0, .delta = OwnedSlice(u8).initBorrowed(part) } });
    }
    var end = tui_runtime.TuiEvent{ .message_end = .{ .role = .assistant, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "answer")) } };
    defer end.deinit(std.testing.allocator);
    app.saveEvent(end);

    var loaded = try app.store.?.load("long-thinking");
    defer loaded.deinit(std.testing.allocator);
    var records: usize = 0;
    var total: usize = 0;
    for (loaded.events.items) |event| {
        if (event != .thinking_delta) continue;
        const delta = event.thinking_delta.delta.slice();
        try std.testing.expect(App.jsonStringBudget(delta) <= max_session_event_payload_bytes);
        try std.testing.expect(std.unicode.utf8ValidateSlice(delta));
        records += 1;
        total += delta.len;
    }
    try std.testing.expectEqual(@as(usize, 2), records);
    try std.testing.expectEqual(sent, total);
}

test "App writes a reply's thinking even when the reply is too large to save" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "oversized-reply");
    defer app.deinit();

    app.saveEvent(.{ .message_start = .{ .role = .assistant } });
    app.saveEvent(.{ .thinking_delta = .{ .content_index = 0, .delta = OwnedSlice(u8).initBorrowed("weighing it") } });
    const text = try std.testing.allocator.alloc(u8, max_session_event_payload_bytes / App.jsonStringBudget("x") + 1);
    defer std.testing.allocator.free(text);
    @memset(text, 'x');
    app.saveEvent(.{ .message_end = .{ .role = .assistant, .text = OwnedSlice(u8).initBorrowed(text) } });

    var loaded = try app.store.?.load("oversized-reply");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 2), loaded.events.items.len);
    try std.testing.expect(loaded.events.items[0] == .message_start);
    try std.testing.expectEqualStrings("weighing it", loaded.events.items[1].thinking_delta.delta.slice());
}

const auto_compact_history = [_]ai_types.Message{
    .{ .user = .{ .content = .{ .text = "first question" }, .timestamp = 0 } },
    .{ .assistant = .{
        .content = &.{.{ .text = .{ .text = "first answer" } }},
        .api = "test-api",
        .provider = "test-provider",
        .model = "model-a",
        .usage = .{},
        .stop_reason = .stop,
        .timestamp = 0,
    } },
};

test "App init takes mode settings from options, not the environment" {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{auto_compact_test_model} });
    defer app.deinit();
    try std.testing.expect(!app.mode_settings.auto_worktree);
    try std.testing.expect(!app.mode_settings.compact_output);

    var opted_in = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{auto_compact_test_model}, .auto_worktree = true, .compact_output = true });
    defer opted_in.deinit();
    try std.testing.expect(opted_in.mode_settings.auto_worktree);
    try std.testing.expect(opted_in.mode_settings.compact_output);
}

test "an app over the in-process endpoint opens without the attached-hub notice, which only an attached session shows" {
    const models = [_]ai_types.Model{auto_compact_test_model};
    const execution = try tui_oap_execution.OapExecution.create(std.testing.allocator, .{ .models = &models });
    defer execution.destroy();
    var app = try App.init(std.testing.allocator, .{ .models = &models, .remote = execution.remote() });
    defer app.deinit();
    for (app.state.transcript.items) |entry| try std.testing.expect(!std.mem.eql(u8, entry.text.items, over_oap_notice));

    const adapter = execution.adapter.?;
    execution.adapter = null;
    defer execution.adapter = adapter;
    var attached = try App.init(std.testing.allocator, .{ .models = &models, .remote = execution.remote() });
    defer attached.deinit();
    var shown = false;
    for (attached.state.transcript.items) |entry| shown = shown or std.mem.eql(u8, entry.text.items, over_oap_notice);
    try std.testing.expect(shown);
}

test "an app over OAP creates an automatic worktree only when its endpoint moves the workspace live" {
    const models = [_]ai_types.Model{auto_compact_test_model};
    const execution = try tui_oap_execution.OapExecution.create(std.testing.allocator, .{ .models = &models });
    defer execution.destroy();
    var app = try App.init(std.testing.allocator, .{ .models = &models, .auto_worktree = true, .remote = execution.remote() });
    defer app.deinit();
    try std.testing.expect(app.mode_settings.auto_worktree);
    try std.testing.expect(!app.createsWorktree());
    try app.runtime.?.start();
    try std.testing.expect(app.createsWorktree());
    execution.live_reasoning = false;
    try std.testing.expect(!app.createsWorktree());

    var local = try App.init(std.testing.allocator, .{ .models = &models, .auto_worktree = true });
    defer local.deinit();
    try std.testing.expect(local.createsWorktree());
}

test "resuming discards a pending worktree sidecar from another session" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.pending_worktree_info = tui_worktree.WorktreeInfo{
        .path = try std.testing.allocator.dupe(u8, "/tmp/pending-worktree-test"),
        .branch = try std.testing.allocator.dupe(u8, "tui/old"),
        .repo_root = try std.testing.allocator.dupe(u8, "/tmp"),
        .prefix = try std.testing.allocator.dupe(u8, ""),
    };
    app.pending_worktree_session_id = try std.testing.allocator.dupe(u8, "old");
    app.session_id = try std.testing.allocator.dupe(u8, "new");

    app.discardPendingWorktreeSidecar();
    try std.testing.expect(app.pending_worktree_info == null);
    try std.testing.expectEqual(@as(usize, 0), app.pending_worktree_session_id.len);
}

fn autoCompactTestApp(mock: *MockAppSession) !App {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{auto_compact_test_model} });
    errdefer app.deinit();
    app.session = mock.session();
    app.state.autocompact = .{ .percent = 50 };
    app.state.telemetry.estimated_tokens = 60_000;
    return app;
}

fn agentOutput(setting: tui_config.Output) agent.OutputSetting {
    return switch (setting) {
        .auto => .auto,
        .max => .max,
        .tokens => |count| .{ .tokens = count },
    };
}

fn savedOutput(setting: agent.OutputSetting) tui_config.Output {
    return switch (setting) {
        .auto => .auto,
        .max => .max,
        .tokens => |count| .{ .tokens = count },
    };
}

test "an output setting reaches the runtime as it was saved" {
    const saved = [_]tui_config.Output{ .auto, .max, .{ .tokens = 64_000 } };
    for (saved) |setting| try std.testing.expectEqual(setting, savedOutput(agentOutput(setting)));
}

const auto_compact_test_model = ai_types.Model{
    .id = "model-a",
    .name = "Model A",
    .api = "test-api",
    .provider = "test-provider",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &[_][]const u8{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 100_000,
    .max_tokens = 1024,
};

test "esc during a run sends the queued messages as a new run once the aborted one ends" {
    var mock = MockAppSession{ .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.session = mock.session();
    app.state.status.streaming = true;
    try app.state.appendSteeredMessage("check the logs first");
    try app.state.appendQueuedFollowUp("then open a PR");

    try app.submit("/abort");
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expectEqual(@as(usize, 2), app.state.held_after_abort.items.len);

    try mock.eventStream().push(.{ .agent_end = .{ .reason = .cancelled } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    try std.testing.expectEqualStrings("check the logs first\n\nthen open a PR", mock.submitted.items[0]);
    try std.testing.expectEqual(@as(usize, 0), app.state.held_after_abort.items.len);
    try mock.eventStream().push(.{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "check the logs first\n\nthen open a PR")) } });
    try app.drainEvents();
    const last = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.user, last.kind);
    try std.testing.expectEqualStrings("then open a PR", last.text.items);
    var echoes: usize = 0;
    for (app.state.transcript.items) |entry| {
        if (entry.kind == .user and std.mem.eql(u8, entry.text.items, "check the logs first")) echoes += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), echoes);
}

test "held follow-ups stay in the queued rows until they are sent" {
    var mock = MockAppSession{ .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.session = mock.session();
    app.state.status.streaming = true;
    try app.state.appendSteeredMessage("shown already");
    try app.state.appendQueuedFollowUp("then open a PR");
    try app.submit("/abort");

    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const rows = try TuiModel.renderQueuedFollowUps(arena.allocator(), &app.state, 80);
    try std.testing.expect(std.mem.indexOf(u8, rows, "queued  then open a PR") != null);
    try std.testing.expect(std.mem.indexOf(u8, rows, "shown already") == null);
}

test "held messages that fail to send while a draft is open are kept for the next message" {
    var mock = MockAppSession{ .submit_error = error.NoModelConfigured, .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.session = mock.session();
    app.state.status.streaming = true;
    try app.state.appendQueuedFollowUp("then open a PR");
    try app.submit("/abort");
    try app.state.replaceComposerBuffer("a draft in progress");

    try mock.eventStream().push(.{ .agent_end = .{ .reason = .cancelled } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 1), app.state.held_after_abort.items.len);
    try std.testing.expectEqualStrings("a draft in progress", app.state.composer.text());
    const last = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqualStrings("The queued messages could not be sent; they are kept and go out with your next message, or press esc to drop them.", last.text.items);
}

test "held messages that cannot be sent go back to the composer instead of being lost" {
    var mock = MockAppSession{ .submit_error = error.NoModelConfigured, .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.session = mock.session();
    app.state.status.streaming = true;
    try app.state.appendQueuedFollowUp("then open a PR");

    try app.submit("/abort");
    try mock.eventStream().push(.{ .agent_end = .{ .reason = .cancelled } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 0), app.state.held_after_abort.items.len);
    try std.testing.expectEqualStrings("then open a PR", app.state.composer.text());
    const last = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqualStrings("The queued messages could not be sent; they are back in the composer.", last.text.items);
}

test "a message typed while an aborted run winds down carries the held messages with it" {
    var mock = MockAppSession{ .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.session = mock.session();
    app.state.status.streaming = true;
    try app.state.appendQueuedFollowUp("then open a PR");

    try app.submit("/abort");
    try app.submit("and add a test");
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    try std.testing.expectEqualStrings("then open a PR\n\nand add a test", mock.submitted.items[0]);
    try std.testing.expectEqual(@as(usize, 0), app.state.held_after_abort.items.len);

    try mock.eventStream().push(.{ .agent_end = .{ .reason = .cancelled } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
}

test "held messages wait for the automatic compaction a typed turn would wait for" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history, .queued_counts = .{ .follow_up = 1 } };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();
    app.state.status.streaming = true;
    try app.state.appendSteeredMessage("check the logs first");
    try app.state.appendQueuedFollowUp("then open a PR");

    try app.submit("/abort");
    try mock.eventStream().push(.{ .agent_end = .{ .reason = .cancelled } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);

    try mock.eventStream().push(.{ .compaction_end = .{
        .outcome = .completed,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "summary")),
    } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    try std.testing.expectEqualStrings("check the logs first\n\nthen open a PR", mock.submitted.items[0]);
    var echoes: usize = 0;
    for (app.state.transcript.items) |entry| {
        if (entry.kind == .user and std.mem.indexOf(u8, entry.text.items, "check the logs first") != null) echoes += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), echoes);
}

test "autocompact holds a turn until the compaction it started has finished" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();

    try app.submit("second question");

    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expect(app.pending_after_compaction != null);

    try mock.eventStream().push(.{ .compaction_end = .{
        .outcome = .completed,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "summary")),
    } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    try std.testing.expect(app.pending_after_compaction == null);
}

test "autocompact sends the held turn when the compaction it started did not finish" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();

    try app.submit("second question");
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);

    try mock.eventStream().push(.{ .compaction_end = .{ .outcome = .cancelled } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    var said_it = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.indexOf(u8, entry.text.items, "did not finish") != null) said_it = true;
    }
    try std.testing.expect(said_it);
}

test "autocompact leaves a turn alone below the share, and when it is off" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();

    app.state.telemetry.estimated_tokens = 1_000;
    try app.submit("well under the share");

    try std.testing.expectEqual(@as(usize, 0), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    try std.testing.expect(app.pending_after_compaction == null);

    app.state.telemetry.estimated_tokens = 60_000;
    app.state.autocompact = .off;
    try app.submit("over the share, but off");

    try std.testing.expectEqual(@as(usize, 0), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 2), mock.submit_count);
}

test "autocompact by default compacts where the window leaves room for a summary and a reply" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();
    app.state.autocompact = .auto;
    const at = agent.compaction.autoCompactAt(auto_compact_test_model.context_window, auto_compact_test_model.max_tokens);

    app.state.telemetry.estimated_tokens = at - 1_000;
    try app.submit("under the point");
    try std.testing.expectEqual(@as(usize, 0), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);

    app.state.telemetry.estimated_tokens = at;
    try app.submit("at the point");
    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
}

const reported_history = [_]ai_types.Message{
    .{ .user = .{ .content = .{ .text = "first question" }, .timestamp = 0 } },
    .{ .assistant = .{
        .content = &.{.{ .text = .{ .text = "first answer" } }},
        .api = "test-api",
        .provider = "test-provider",
        .model = "model-a",
        .usage = .{ .input = 55_000, .output = 100, .cache_read = 5_000 },
        .stop_reason = .stop,
        .timestamp = 0,
    } },
};

test "autocompact counts the context from the provider's last report when the estimate reads lower" {
    var mock = MockAppSession{ .history_messages = &reported_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();
    app.state.telemetry.estimated_tokens = 1_000;

    try app.submit("second question");

    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
}

test "autocompact steers the held turn rather than blocking on a run the queue resumed" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();

    try app.submit("held turn");
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);

    mock.queued_counts.follow_up = 1;
    try mock.eventStream().push(.{ .compaction_end = .{ .outcome = .completed } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 1), mock.resume_count);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expectEqual(@as(usize, 1), mock.steer_count);
}

test "a session resume drops a held turn with a note, before the resume runs" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();
    if (app.store) |*store| store.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    try saveTestSession(app.store.?, "s1", 1);
    try app.loadSessions();
    app.state.session_index = 0;

    try app.submit("held turn");
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);

    app.resumeSelectedSession() catch {};

    try std.testing.expect(app.pending_after_compaction == null);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    var said_it = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.indexOf(u8, entry.text.items, "was not sent: the session was resumed") != null) said_it = true;
    }
    try std.testing.expect(said_it);
}

test "autocompact does not compact a history that is already a summary" {
    var mock = MockAppSession{};
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();
    mock.history_messages = &[_]ai_types.Message{
        .{ .user = .{
            .content = .{ .text = agent.compaction.header ++ "\n\n<summary>\nkept\n</summary>" },
            .timestamp = 0,
        } },
        .{ .assistant = .{
            .content = &.{.{ .text = .{ .text = agent.compaction.acknowledgement } }},
            .api = "test-api",
            .provider = "test-provider",
            .model = "model-a",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = 0,
        } },
    };

    try app.submit("after a compaction");

    try std.testing.expectEqual(@as(usize, 0), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
}

test "autocompact starts one compaction, and a turn typed during it takes the normal path" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();

    try app.submit("first held turn");
    try app.submit("second turn typed while compacting");

    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);

    try mock.eventStream().push(.{ .compaction_end = .{ .outcome = .completed } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 2), mock.submit_count);
}

test "App indexes where a completed compaction starts" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "compaction-index");
    defer app.deinit();

    var before = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "before")) } };
    defer before.deinit(std.testing.allocator);
    app.saveEvent(before);
    const offset = try app.store.?.conversationBytes("compaction-index");
    var compacted = tui_runtime.TuiEvent{ .compaction_end = .{
        .outcome = .completed,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept\n</summary>")),
    } };
    defer compacted.deinit(std.testing.allocator);
    app.saveEvent(compacted);

    var index = try app.store.?.loadIndex("compaction-index");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqual(offset, index.compaction_offset);

    var loaded = try app.store.?.load("compaction-index");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(offset, loaded.metadata.compaction_offset);
    try std.testing.expectEqual(@as(usize, 2), loaded.messages.items.len);
    try std.testing.expect(loaded.events.items[0] == .message_end);
}

test "App keeps the last compaction offset when a compaction record is not written" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "unwritten-compaction");
    defer app.deinit();

    var before = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "before")) } };
    defer before.deinit(std.testing.allocator);
    app.saveEvent(before);
    const offset = try app.store.?.conversationBytes("unwritten-compaction");
    var compacted = tui_runtime.TuiEvent{ .compaction_end = .{
        .outcome = .completed,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept\n</summary>")),
    } };
    defer compacted.deinit(std.testing.allocator);
    app.saveEvent(compacted);
    try std.testing.expectEqual(offset, app.compaction_offset);

    const path = try std.fs.path.join(std.testing.allocator, &.{ base, "unwritten-compaction.jsonl" });
    defer std.testing.allocator.free(path);
    try compat.fs.getCwd().deleteFile(defaultIo(), path);
    try compat.fs.createDir(compat.fs.getCwd(), path);
    try std.testing.expect(try app.store.?.conversationBytes("unwritten-compaction") != offset);
    app.saveEvent(compacted);
    app.saveEvent(.{ .agent_end = .{ .reason = .completed } });

    var index = try app.store.?.loadIndex("unwritten-compaction");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqual(offset, index.compaction_offset);
}

test "App titles a new session with its first message's first line" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "first-message-title");
    defer app.deinit();

    var first = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "\n fix the resume freeze\nwith detail")) } };
    defer first.deinit(std.testing.allocator);
    app.saveEvent(first);
    var second = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "another message")) } };
    defer second.deinit(std.testing.allocator);
    app.saveEvent(second);

    var index = try app.store.?.loadIndex("first-message-title");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("fix the resume freeze", index.title);
    try std.testing.expect(!index.title_generated);
}

test "App over the in-process endpoint saves each message once, from the loop's own records" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "the endpoint's reply" }} });
    const models = [_]ai_types.Model{defaultModel()};
    const execution = try tui_oap_execution.OapExecution.create(std.testing.allocator, .{ .protocol = provider.protocolClient(), .models = &models });
    defer execution.destroy();
    var app = try sessionTestApp(base, "endpoint-records");
    defer app.deinit();
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    runtime.* = tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .models = &models, .remote = execution.remote() }) catch |err| {
        std.testing.allocator.destroy(runtime);
        return err;
    };
    app.runtime = runtime;

    try runtime.submitTurn("ask the endpoint");
    var ended = false;
    var waits: usize = 0;
    while (!ended and waits < 5000) : (waits += 1) {
        while (runtime.streamEvents().poll()) |event| {
            var owned = event;
            defer owned.deinit(std.testing.allocator);
            if (owned == .agent_end) ended = true;
            app.saveEvent(owned);
        }
        if (!ended) std.testing.io.sleep(.fromNanoseconds(std.time.ns_per_ms), .boot) catch {};
    }
    try std.testing.expect(ended);
    app.saveEndpointRecords();

    var lines = try sessionFileLines(base, "endpoint-records.jsonl");
    defer freeSessionFileLines(&lines);
    var users: usize = 0;
    var replies: usize = 0;
    for (lines.items[1..]) |line| {
        if (std.mem.indexOf(u8, line, "\"role\":\"user\"") != null and std.mem.indexOf(u8, line, "ask the endpoint") != null) users += 1;
        if (std.mem.indexOf(u8, line, "\"role\":\"assistant\"") != null and std.mem.indexOf(u8, line, "the endpoint's reply") != null) replies += 1;
    }
    try std.testing.expectEqual(@as(usize, 1), users);
    try std.testing.expectEqual(@as(usize, 1), replies);
}

test "App indexes the title the model generates after the first reply" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "generated-title");
    defer app.deinit();

    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "\"Resume freeze fix.\"" }} });
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    runtime.* = tui_runtime.TuiRuntime.init(std.testing.allocator, .{
        .protocol = provider.protocolClient(),
        .models = &[_]ai_types.Model{defaultModel()},
        .generate_titles = true,
    }) catch |err| {
        std.testing.allocator.destroy(runtime);
        return err;
    };
    app.runtime = runtime;

    var first = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "the resume freezes on long sessions")) } };
    defer first.deinit(std.testing.allocator);
    app.saveEvent(first);
    app.saveEvent(.{ .agent_end = .{ .reason = .completed } });
    runtime.waitForTitleRequest();
    app.collectGeneratedTitle();

    var index = try app.store.?.loadIndex("generated-title");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Resume freeze fix", index.title);
    try std.testing.expect(index.title_generated);
    try std.testing.expectEqual(@as(usize, 1), provider.call_count);
}

test "App files a generated title under the session that asked for it" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "asking-session");
    defer app.deinit();

    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "\"Resume freeze fix.\"" }} });
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    runtime.* = tui_runtime.TuiRuntime.init(std.testing.allocator, .{
        .protocol = provider.protocolClient(),
        .models = &[_]ai_types.Model{defaultModel()},
        .generate_titles = true,
    }) catch |err| {
        std.testing.allocator.destroy(runtime);
        return err;
    };
    app.runtime = runtime;

    var first = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "the resume freezes on long sessions")) } };
    defer first.deinit(std.testing.allocator);
    app.saveEvent(first);
    app.saveEvent(.{ .agent_end = .{ .reason = .completed } });
    runtime.waitForTitleRequest();

    try saveTestSession(app.store.?, "resumed-session", 1);
    var resumed = try app.store.?.load("resumed-session");
    defer resumed.deinit(std.testing.allocator);
    std.testing.allocator.free(app.session_id);
    app.session_id = try std.testing.allocator.dupe(u8, "resumed-session");
    try app.adoptLoadedSession(resumed.metadata);
    app.collectGeneratedTitle();

    var asking = try app.store.?.loadIndex("asking-session");
    defer asking.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Resume freeze fix", asking.title);
    try std.testing.expect(asking.title_generated);
    try std.testing.expectEqual(@as(usize, 0), app.session_title.len);
    try std.testing.expect(!app.session_title_generated);
    try std.testing.expectError(error.FileNotFound, app.store.?.loadIndex("resumed-session"));
}

test "App rename names the session and keeps the name over a generated title" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "renamed-session");
    defer app.deinit();

    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "\"Resume freeze fix.\"" }} });
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    runtime.* = tui_runtime.TuiRuntime.init(std.testing.allocator, .{
        .protocol = provider.protocolClient(),
        .models = &[_]ai_types.Model{defaultModel()},
        .generate_titles = true,
    }) catch |err| {
        std.testing.allocator.destroy(runtime);
        return err;
    };
    app.runtime = runtime;

    var first = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "the resume freezes on long sessions")) } };
    defer first.deinit(std.testing.allocator);
    app.saveEvent(first);
    app.saveEvent(.{ .agent_end = .{ .reason = .completed } });
    runtime.waitForTitleRequest();

    try app.submit("/rename  Freeze hunt\nsecond line");
    app.collectGeneratedTitle();

    var index = try app.store.?.loadIndex("renamed-session");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Freeze hunt", index.title);
    try std.testing.expect(index.title_renamed);
    try std.testing.expect(!index.title_generated);
    try std.testing.expectEqualStrings("Freeze hunt", app.session_title);
    const last = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.system, last.kind);
    try std.testing.expectEqualStrings("Session renamed to \"Freeze hunt\"", last.text.items);
}

test "App rename before the first message keeps the name and asks for no title" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    var app = try sessionTestApp(base, "named-first");
    defer app.deinit();

    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "\"Resume freeze fix.\"" }} });
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    runtime.* = tui_runtime.TuiRuntime.init(std.testing.allocator, .{
        .protocol = provider.protocolClient(),
        .models = &[_]ai_types.Model{defaultModel()},
        .generate_titles = true,
    }) catch |err| {
        std.testing.allocator.destroy(runtime);
        return err;
    };
    app.runtime = runtime;

    try app.submit("/rename Freeze hunt");
    try std.testing.expectError(error.FileNotFound, app.store.?.loadIndex("named-first"));

    var first = tui_runtime.TuiEvent{ .message_end = .{ .role = .user, .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "the resume freezes on long sessions")) } };
    defer first.deinit(std.testing.allocator);
    app.saveEvent(first);
    app.saveEvent(.{ .agent_end = .{ .reason = .completed } });

    var index = try app.store.?.loadIndex("named-first");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("Freeze hunt", index.title);
    try std.testing.expect(index.title_renamed);
    try std.testing.expectEqual(@as(usize, 0), provider.call_count);
}

test "App clear_transcript clears the tool registry" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    _ = try app.state.resolveToolOccurrenceForTest("call-1", "shell_execute", "{\"command\":\"pwd\"}", .live_intent, .done);
    try app.state.appendToolSummaryTranscript("◈ shell_execute \"pwd\" ok", "call-1");

    try app.submit("/clear");

    try std.testing.expectEqual(@as(usize, 0), app.state.tools.items.len);
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.system, app.state.transcript.items[0].kind);
    try std.testing.expectEqual(@as(usize, 0), app.inline_history_flushed);
}

test "App approval decisions map to requested choices" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.state.approval.setPending(std.testing.allocator, "call-approval", "edit_file", "edit_file", "{\"path\":\"README.md\"}");
    app.state.mode = .approval;

    try app.decideApproval(true, false);
    try std.testing.expectEqual(tui_state.AppMode.normal, app.state.mode);
    try std.testing.expectEqual(tui_state.ApprovalStatus.approved, app.state.approval.status);
    try std.testing.expect(!app.state.approval.always);

    try app.state.approval.setPending(std.testing.allocator, "call-approval", "edit_file", "edit_file", "{\"path\":\"README.md\"}");
    app.state.mode = .approval;
    try app.decideApproval(true, true);
    try std.testing.expectEqual(tui_state.ApprovalStatus.approved, app.state.approval.status);
    try std.testing.expect(app.state.approval.always);

    try app.state.approval.setPending(std.testing.allocator, "call-approval", "edit_file", "edit_file", "{\"path\":\"README.md\"}");
    app.state.mode = .approval;
    try app.decideApproval(false, false);
    try std.testing.expectEqual(tui_state.ApprovalStatus.rejected, app.state.approval.status);
    try std.testing.expect(!app.state.approval.always);
}

test "App submit appends user transcript without runtime" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("hello");
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("hello", app.state.transcript.items[0].text.items);
}

test "App submit routes help command to system transcript" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("/help");
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.system, app.state.transcript.items[0].kind);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "/model") != null);
}

test "App submit starts direct OpenAI Codex login command" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();

    try app.submit("/login openai-codex");

    try std.testing.expect(app.login != null);
    try std.testing.expectEqualStrings("openai-codex", app.login.?.provider_id);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "starting login for openai-codex") != null);
}

test "App submit starts direct Kimi API key login command" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();

    try app.submit("/login kimi");

    try std.testing.expect(app.login != null);
    try std.testing.expectEqualStrings("kimi", app.login.?.provider_id);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "starting login for kimi") != null);
}

test "App cancel login clears secret composer draft" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();

    app.state.mode = .login_input;
    app.state.login_input_secret = true;
    try app.state.composer.insertSlice(std.testing.allocator, "moonshot-secret-key");

    app.cancelLogin();

    try std.testing.expectEqual(tui_state.AppMode.normal, app.state.mode);
    try std.testing.expect(!app.state.login_input_secret);
    try std.testing.expectEqual(@as(usize, 0), app.state.composer.text().len);
}

test "App saves Kimi login credentials as api key" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "home" });
    defer std.testing.allocator.free(home);
    try compat.fs.createDir(compat.fs.getCwd(), home);
    const previous_home = std.process.Environ.getAlloc(std.testing.environ, std.testing.allocator, "HOME") catch null;
    defer {
        if (previous_home) |value| {
            const value_z = std.testing.allocator.dupeZ(u8, value) catch null;
            if (value_z) |home_z| {
                defer std.testing.allocator.free(home_z);
                _ = setenv("HOME", home_z.ptr, 1);
            }
            std.testing.allocator.free(value);
        } else {
            _ = unsetenv("HOME");
        }
    }
    const home_z = try std.testing.allocator.dupeZ(u8, home);
    defer std.testing.allocator.free(home_z);
    _ = setenv("HOME", home_z.ptr, 1);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    const creds = oauth_storage.Credentials{
        .refresh = try std.testing.allocator.dupe(u8, ""),
        .access = try std.testing.allocator.dupe(u8, "moonshot-test-key"),
        .expires = std.math.maxInt(i64),
    };
    defer creds.deinit(std.testing.allocator);

    try app.saveLoginCredentials("kimi", creds, true);

    var storage = try oauth_storage.AuthStorage.loadFromFile(std.testing.allocator);
    defer storage.deinit();
    const auth = storage.providers.get("kimi") orelse return error.MissingKimiAuth;
    switch (auth) {
        .api_key => |key| try std.testing.expectEqualStrings("moonshot-test-key", key),
        .oauth => return error.ExpectedApiKeyAuth,
    }
}

test "App saves a login only for the row that was logged in" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "home" });
    defer std.testing.allocator.free(home);
    try compat.fs.createDir(compat.fs.getCwd(), home);
    const previous_home = std.process.Environ.getAlloc(std.testing.environ, std.testing.allocator, "HOME") catch null;
    defer {
        if (previous_home) |value| {
            const value_z = std.testing.allocator.dupeZ(u8, value) catch null;
            if (value_z) |home_z| {
                defer std.testing.allocator.free(home_z);
                _ = setenv("HOME", home_z.ptr, 1);
            }
            std.testing.allocator.free(value);
        } else {
            _ = unsetenv("HOME");
        }
    }
    const home_z = try std.testing.allocator.dupeZ(u8, home);
    defer std.testing.allocator.free(home_z);
    _ = setenv("HOME", home_z.ptr, 1);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    const creds = oauth_storage.Credentials{
        .refresh = try std.testing.allocator.dupe(u8, ""),
        .access = try std.testing.allocator.dupe(u8, "xiaomi-test-key"),
        .expires = std.math.maxInt(i64),
    };
    defer creds.deinit(std.testing.allocator);

    try app.saveLoginCredentials("xiaomi-token-plan-cn", creds, true);

    var storage = try oauth_storage.AuthStorage.loadFromFile(std.testing.allocator);
    defer storage.deinit();
    switch (storage.providers.get("xiaomi-token-plan-cn") orelse return error.MissingLoggedInProvider) {
        .api_key => |key| try std.testing.expectEqualStrings("xiaomi-test-key", key),
        .oauth => return error.ExpectedApiKeyAuth,
    }
    for ([_][]const u8{ "xiaomi-token-plan-sgp", "xiaomi-token-plan-ams", "xiaomi" }) |provider_id| {
        try std.testing.expect(!storage.providers.contains(provider_id));
    }
}

test "App login discovery availability follows the model catalog loader" {
    for (provider_catalog.all) |row| {
        try std.testing.expectEqual(model_catalog.supportsCatalogModelDiscovery(row.id), App.loginDiscoveryAvailable(row.id));
    }
    try std.testing.expect(!App.loginDiscoveryAvailable("google"));
    try std.testing.expect(!App.loginDiscoveryAvailable("ollama"));
    try std.testing.expect(!App.loginDiscoveryAvailable("azure"));
}

test "App login picker follows catalog order and lists each row reading a shared variable" {
    try std.testing.expectEqualStrings("openai", App.loginProviderAt(0).?.id);
    try std.testing.expectEqualStrings("anthropic", App.loginProviderAt(1).?.id);
    try std.testing.expect(App.loginProviderIndex("xiaomi") != null);
    try std.testing.expect(App.loginProviderIndex("xiaomi-token-plan-cn") != null);
    try std.testing.expect(App.loginProviderIndex("xiaomi").? != App.loginProviderIndex("xiaomi-token-plan-cn").?);
    try std.testing.expectEqualStrings("opencode-go", App.loginProviderAt(App.loginProviderIndex("opencode-go").?).?.id);
    try std.testing.expectEqualStrings("opencode-zen", App.loginProviderAt(App.loginProviderIndex("opencode-zen").?).?.id);
    try std.testing.expect(App.loginProviderAt(App.loginProviderIndex("xiaomi-token-plan-cn").?).?.display_name != null);
    try std.testing.expect(App.loginProviderIndex("google") != null);
    try std.testing.expect(!App.loginDiscoveryAvailable("google"));
    try std.testing.expect(App.loginProviderIndex("ollama") != null);
    try std.testing.expect(App.loginProviderCatalogIndex("kimi") != null);
    try std.testing.expectEqual(App.LoginStatus.env_key, App.loginStatusFor(null, "kimi", true));
    try std.testing.expectEqual(App.LoginStatus.none, App.loginStatusFor(null, "kimi", false));
}

test "multi-line /help output renders all lines into transcript view" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("/help");

    const rendered = try transcript_view.render(std.testing.allocator, &app.state, .{ .width = 100, .height = 44 });
    defer std.testing.allocator.free(rendered);

    const expect = [_][]const u8{
        "/help",   "/model", "/status",
        "/resume", "/login", "/permissions",
        "/abort",  "/clear", "/quit",
        "/think",
    };
    for (expect) |needle| {
        if (std.mem.indexOf(u8, rendered, needle) == null) {
            std.debug.print("missing {s} in rendered output:\n{s}\n", .{ needle, rendered });
            return error.TestExpectedHelpLine;
        }
    }
}

test "App submit routes unknown command to error transcript" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("/unknown");
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.@"error", app.state.transcript.items[0].kind);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "unknown command") != null);
}

test "App submit abort when idle reports idle transcript" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.submit("/abort");
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.system, app.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("Nothing to abort — agent is idle.", app.state.transcript.items[0].text.items);
}

test "App submit abort when streaming cancels and reports transcript" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.state.status.streaming = true;

    try app.submit("/abort");

    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
    try std.testing.expect(!app.state.status.streaming);
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.system, app.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("Turn aborted.", app.state.transcript.items[0].text.items);
}

test "App submit abort when streaming via runtime-only cancels and reports transcript" {
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;
    app.runtime.?.stream_active = true;

    try app.submit("/abort");

    try std.testing.expect(app.runtime.?.cancelled.load(.acquire));
    try std.testing.expect(!app.state.status.streaming);
    try std.testing.expect(app.state.stream_aborted);
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.system, app.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("Turn aborted.", app.state.transcript.items[0].text.items);
}

const gpt_model = ai_types.Model{
    .id = "gpt-5-codex",
    .name = "GPT-5 Codex",
    .api = "openai-responses",
    .provider = "openai",
    .base_url = "https://example.invalid",
    .reasoning = true,
    .input = &[_][]const u8{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 128_000,
    .max_tokens = 16_384,
};

const kimi_model = ai_types.Model{
    .id = "kimi-k2.7-code",
    .name = "Kimi K2.7 Code",
    .api = "openai-completions",
    .provider = "kimi",
    .base_url = "https://example.invalid",
    .reasoning = false,
    .input = &[_][]const u8{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 262_144,
    .max_tokens = 16_384,
};

fn oapxConfigDir(allocator: std.mem.Allocator, home: []const u8) ![]u8 {
    return std.fs.path.join(allocator, &.{ home, ".oapx" });
}

fn expectConfiguredContextWindow(allocator: std.mem.Allocator, home: []const u8, expected: ?u32) !void {
    const base = try oapxConfigDir(allocator, home);
    defer allocator.free(base);
    var store = try tui_config.Store.init(allocator, base);
    defer store.deinit();
    var cfg = try store.load();
    defer cfg.deinit(allocator);
    try std.testing.expectEqual(expected, cfg.mode.context_window);
}

fn configFileBytes(allocator: std.mem.Allocator, home: []const u8) ![]u8 {
    const base = try oapxConfigDir(allocator, home);
    defer allocator.free(base);
    const path = try std.fs.path.join(allocator, &.{ base, "config.json" });
    defer allocator.free(path);
    return compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), path, 1 << 20);
}

test "a context window the user chose is persisted, and the catalog's own is not written back" {
    defer compat.clearTestEnv();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(home);
    try compat.setTestEnv(std.testing.allocator, "HOME", home);

    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();
    if (app.store) |*owned| owned.deinit();
    app.store = null;

    try app.submit("/context 200000");
    try std.testing.expectEqual(@as(u64, 200_000), app.runtime.?.contextWindow());
    try expectConfiguredContextWindow(std.testing.allocator, home, 200_000);

    try app.submit("/context default");
    try std.testing.expectEqual(@as(u64, 128_000), app.runtime.?.contextWindow());
    try expectConfiguredContextWindow(std.testing.allocator, home, null);
}

test "a persisted window the model in effect cannot take is dropped without rewriting the file" {
    defer compat.clearTestEnv();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(home);
    try compat.setTestEnv(std.testing.allocator, "HOME", home);

    const base = try oapxConfigDir(std.testing.allocator, home);
    defer std.testing.allocator.free(base);
    var store = try tui_config.Store.init(std.testing.allocator, base);
    defer store.deinit();
    var cfg = try tui_config.Config.defaults(std.testing.allocator);
    defer cfg.deinit(std.testing.allocator);
    cfg.mode.context_window = 4_000_000_000;
    try store.save(cfg);

    const before = try configFileBytes(std.testing.allocator, home);
    defer std.testing.allocator.free(before);

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();
    try std.testing.expectEqual(@as(?u32, 4_000_000_000), production.options().context_window);

    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();
    if (app.store) |*owned| owned.deinit();
    app.store = null;
    try app.drainEvents();

    try std.testing.expect(app.runtime.?.contextWindowOverride() == null);
    try std.testing.expect(app.runtime.?.contextWindow() != 4_000_000_000);
    var said_it = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.indexOf(u8, entry.text.items, "4000000000 context tokens is above the") != null) said_it = true;
    }
    try std.testing.expect(said_it);

    const after = try configFileBytes(std.testing.allocator, home);
    defer std.testing.allocator.free(after);
    try std.testing.expectEqualStrings(before, after);
}

test "a flagless launch keeps the stored window, and the flag overrides it" {
    defer compat.clearTestEnv();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(home);
    try compat.setTestEnv(std.testing.allocator, "HOME", home);

    const base = try oapxConfigDir(std.testing.allocator, home);
    defer std.testing.allocator.free(base);
    {
        var store = try tui_config.Store.init(std.testing.allocator, base);
        defer store.deinit();
        var cfg = try tui_config.Config.defaults(std.testing.allocator);
        defer cfg.deinit(std.testing.allocator);
        cfg.mode.context_window = 200_000;
        try store.save(cfg);
    }

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();
    try std.testing.expectEqual(@as(?u32, 200_000), production.options().context_window);

    try std.testing.expectEqual(@as(?u32, 200_000), preferredContextWindow(200_000, null));
    try std.testing.expectEqual(@as(?u32, 300_000), preferredContextWindow(200_000, 300_000));
    try std.testing.expectEqual(@as(?u32, 200_000), preferredContextWindow(200_000, 200_000));
    try std.testing.expectEqual(@as(?u32, null), preferredContextWindow(null, null));
}

test "a bare /context reports the window and leaves the persisted member alone" {
    defer compat.clearTestEnv();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(home);
    try compat.setTestEnv(std.testing.allocator, "HOME", home);

    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();
    if (app.store) |*owned| owned.deinit();
    app.store = null;

    try app.submit("/context 200000");
    try expectConfiguredContextWindow(std.testing.allocator, home, 200_000);

    try app.submit("/context");
    try expectConfiguredContextWindow(std.testing.allocator, home, 200_000);
}

test "a settings toggle does not erase the persisted window" {
    defer compat.clearTestEnv();
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const home = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(home);
    try compat.setTestEnv(std.testing.allocator, "HOME", home);

    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();
    if (app.store) |*owned| owned.deinit();
    app.store = null;

    try app.submit("/context 200000");
    try expectConfiguredContextWindow(std.testing.allocator, home, 200_000);

    try app.toggleSetting(0);
    try expectConfiguredContextWindow(std.testing.allocator, home, 200_000);
    try app.toggleSetting(1);
    try expectConfiguredContextWindow(std.testing.allocator, home, 200_000);
}

test "App says so when a window the model in effect cannot take is dropped" {
    const models = [_]ai_types.Model{ gpt_model, kimi_model };
    var app = try App.init(std.testing.allocator, .{ .models = &models, .context_window = 1_100_000 });
    defer app.deinit();

    try app.drainEvents();
    var said_it = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.indexOf(u8, entry.text.items, "1100000 context tokens is above the 1000000 gpt-5-codex takes") != null) said_it = true;
    }
    try std.testing.expect(said_it);
    try std.testing.expect(app.runtime.?.contextWindowRefused() == null);

    try app.submit("/context 1m");
    try app.drainEvents();
    try app.runtime.?.switchModel("kimi-k2.7-code");
    try app.drainEvents();
    var said_again = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.indexOf(u8, entry.text.items, "1000000 context tokens is above the 262144 kimi-k2.7-code takes") != null) said_again = true;
    }
    try std.testing.expect(said_again);
    try std.testing.expectEqual(@as(u64, 262_144), app.runtime.?.contextWindow());
}

test "a failed adopt leaves the old model pair and the average in place" {
    var app = App{ .allocator = std.testing.allocator, .state = tui_state.AppState.init(std.testing.allocator) };
    defer app.state.deinit();
    app.rate_model = try std.testing.allocator.dupe(u8, "first-model");
    app.rate_provider = try std.testing.allocator.dupe(u8, "first-provider");
    defer std.testing.allocator.free(app.rate_model);
    defer std.testing.allocator.free(app.rate_provider);
    app.state.telemetry.rate.measured_since_switch = .{ .output_tokens = 400, .stream_ms = 1_000 };

    var failing = std.testing.FailingAllocator.init(std.testing.allocator, .{ .fail_index = 1 });
    const held = app.allocator;
    app.allocator = failing.allocator();
    defer app.allocator = held;
    app.adoptRateModel("second-model", "second-provider");

    try std.testing.expectEqualStrings("first-model", app.rate_model);
    try std.testing.expectEqualStrings("first-provider", app.rate_provider);
    try std.testing.expectEqual(@as(u64, 400), app.state.telemetry.rate.measured_since_switch.output_tokens);
}

test "an adopt that succeeds replaces the pair and clears the average" {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();
    app.drainEvents() catch {};

    try std.testing.expectEqualStrings(gpt_model.id, app.rate_model);
    try std.testing.expectEqualStrings(gpt_model.provider, app.rate_provider);
    app.state.telemetry.rate.measured_since_switch = .{ .output_tokens = 400, .stream_ms = 1_000 };

    try app.runtime.?.switchModel("kimi-k2.7-code");
    app.drainEvents() catch {};

    try std.testing.expectEqualStrings(kimi_model.id, app.rate_model);
    try std.testing.expectEqualStrings(kimi_model.provider, app.rate_provider);
    try std.testing.expectEqual(@as(u64, 0), app.state.telemetry.rate.measured_since_switch.output_tokens);
}

test "a resumed session's replayed events leave the rate showing nothing" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();
    if (production.models.len == 0) return error.TestExpectedTarget;
    const model_id = production.models[0].id;

    var store = try session_store.Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "replayed"),
        .model = try std.testing.allocator.dupe(u8, model_id),
        .provider = try std.testing.allocator.dupe(u8, production.models[0].provider),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    const text = "replayed assistant text";
    var delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, text));
    defer delta.deinit(std.testing.allocator);
    var ended = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, text));
    defer ended.deinit(std.testing.allocator);
    try store.save(meta, .{ .turn_start = .{} });
    try store.save(meta, .{ .message_start = .{ .role = .assistant } });
    try store.save(meta, .{ .text_delta = .{ .content_index = 0, .delta = delta } });
    try store.save(meta, .{ .message_end = .{ .role = .assistant, .text = ended } });
    try store.save(meta, .{ .turn_end = .{ .stop_reason = .stop } });

    var mock = MockAppSession{};
    defer mock.deinit();
    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();
    if (app.store) |*owned| owned.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    app.session = mock.session();
    try app.loadSessions();
    try std.testing.expectEqual(@as(usize, 1), app.state.sessions.items.len);
    app.state.session_index = 0;

    try app.resumeSelectedSession();

    try std.testing.expectEqual(@as(u64, 0), app.state.telemetry.rate.turn().output_tokens);
    try std.testing.expectEqual(@as(u64, 0), app.state.telemetry.rate.estimated_since_switch.output_tokens);
    try std.testing.expectEqual(@as(u64, 0), app.state.telemetry.rate.measured_since_switch.output_tokens);
    try std.testing.expect(!app.state.telemetry.rate.previous.hasFigure());
    try std.testing.expect(!app.state.telemetry.rate.average.hasFigure());
    try std.testing.expect(!app.state.telemetry.rate.live.hasFigure());
}

test "a session whose model is gone resumes on the current model and says so" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();
    if (production.models.len == 0) return error.TestExpectedTarget;

    var store = try session_store.Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "orphaned"),
        .model = try std.testing.allocator.dupe(u8, "retired-model"),
        .provider = try std.testing.allocator.dupe(u8, "retired-provider"),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    try store.save(meta, .{ .turn_start = .{} });
    try store.save(meta, .{ .turn_end = .{ .stop_reason = .stop } });

    var mock = MockAppSession{};
    defer mock.deinit();
    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();
    if (app.store) |*owned| owned.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    app.session = mock.session();
    try app.loadSessions();
    app.state.session_index = 0;
    const before = app.runtime.?.currentModel().?;

    try app.resumeSelectedSession();

    try std.testing.expectEqualStrings("orphaned", app.session_id);
    try std.testing.expectEqualStrings(before.id, app.runtime.?.currentModel().?.id);
    try std.testing.expectEqualStrings(before.id, app.state.status.model);
    const said = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expect(std.mem.startsWith(u8, said.text.items, "retired-provider/retired-model is not available, so this session continues on "));
}

test "a model switch resets the rate average, including between two models that cost the same" {
    const same_price = ai_types.Model{
        .id = "gpt-5-codex-twin",
        .name = "GPT-5 Codex Twin",
        .api = "openai-responses",
        .provider = "openai",
        .base_url = "https://example.invalid",
        .reasoning = true,
        .input = &[_][]const u8{"text"},
        .cost = .{ .input = 1.25, .output = 10, .cache_read = 0, .cache_write = 0 },
        .context_window = 128_000,
        .max_tokens = 16_384,
    };
    var twin = gpt_model;
    twin.id = "gpt-5-codex-twin";
    twin.cost = same_price.cost;
    const models = [_]ai_types.Model{ gpt_model, twin };
    var app = try App.init(std.testing.allocator, .{ .models = &models });
    defer app.deinit();

    app.state.telemetry.rate.measured_since_switch = .{ .output_tokens = 400, .stream_ms = 1_000 };
    app.state.telemetry.rate.turnEnded();
    app.drainEvents() catch {};
    try std.testing.expect(app.state.telemetry.rate.measured_since_switch.hasFigure());

    try app.runtime.?.switchModel("gpt-5-codex-twin");
    app.drainEvents() catch {};

    try std.testing.expectEqual(@as(u64, 0), app.state.telemetry.rate.measured_since_switch.output_tokens);
    try std.testing.expect(!app.state.telemetry.rate.average.hasFigure());
    try std.testing.expect(!app.state.telemetry.rate.previous.hasFigure());
}

test "App a model command leaves the gauge on the window in effect" {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();

    try app.submit("/context 200000");
    try std.testing.expectEqual(@as(usize, 200_000), app.state.status.context_limit);

    try app.submit("/model kimi-k2.7-code");
    try std.testing.expectEqual(@as(usize, 200_000), app.state.status.context_limit);
    try std.testing.expectEqual(@as(u64, 200_000), app.state.telemetry.context_window);

    try app.submit("/model gpt-5-codex");
    try std.testing.expectEqual(@as(u64, 200_000), app.state.telemetry.context_window);
    try std.testing.expectEqual(@as(usize, 200_000), app.state.status.context_limit);
}

test "App restores a model's one-million context window after switching back" {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();

    try app.submit("/context 1m");
    try std.testing.expectEqual(@as(usize, 1_000_000), app.state.status.context_limit);

    try app.submit("/model kimi-k2.7-code");
    try std.testing.expectEqual(@as(usize, 262_144), app.state.status.context_limit);
    try std.testing.expectEqual(@as(u64, 262_144), app.state.telemetry.context_window);

    try app.submit("/model gpt-5-codex");
    try std.testing.expectEqual(@as(usize, 1_000_000), app.state.status.context_limit);
    try std.testing.expectEqual(@as(u64, 1_000_000), app.state.telemetry.context_window);
    try std.testing.expectEqual(@as(u32, 1_000_000), app.runtime.?.currentModel().?.context_window);
}

test "App a catalog refresh that drops the window leaves the gauge on the model's own" {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{ gpt_model, kimi_model } });
    defer app.deinit();

    try app.submit("/context 1m");
    try std.testing.expectEqual(@as(usize, 1_000_000), app.state.status.context_limit);

    try app.runtime.?.replaceModels(&[_]ai_types.Model{kimi_model}, null);
    try app.drainEvents();

    try std.testing.expectEqual(@as(u64, 262_144), app.runtime.?.contextWindow());
    try std.testing.expectEqual(@as(usize, 262_144), app.state.status.context_limit);
    try std.testing.expectEqual(@as(u64, 262_144), app.state.telemetry.context_window);
}

test "App context moves the gauge, and the model's own window comes back" {
    var app = try App.init(std.testing.allocator, .{ .models = &[_]ai_types.Model{test_model} });
    defer app.deinit();

    try app.submit("/context 512");

    try std.testing.expectEqual(@as(usize, 512), app.state.status.context_limit);
    try std.testing.expectEqual(@as(u64, 512), app.state.telemetry.context_window);
    try std.testing.expectEqual(@as(u32, 512), app.runtime.?.currentModel().?.context_window);

    try app.submit("/context default");

    try std.testing.expectEqual(@as(usize, 1024), app.state.status.context_limit);
    try std.testing.expectEqual(@as(u64, 1024), app.state.telemetry.context_window);
}

test "App submit does not clear stream_aborted for slash commands" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.state.stream_aborted = true;

    try app.submit("/help");

    try std.testing.expect(app.state.stream_aborted);
    try std.testing.expect(app.state.transcript.items.len > 0);
}

test "App submit abort does not permanently shut down approval waiter" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    const waiter = try app.allocator.create(ApprovalWaiter);
    waiter.* = .{ .allocator = app.allocator };
    app.approval_waiter = waiter;
    waiter.tool_call_id = try app.allocator.dupe(u8, "call-1");

    try app.submit("/abort");

    try std.testing.expect(!waiter.shutting_down);
    try std.testing.expect(waiter.decision == .reject);

    waiter.decision = null;
    try app.state.approval.setPending(app.allocator, "call-1", "edit_file", "edit_file", "{}");
    try app.decideApproval(true, false);
    try std.testing.expect(waiter.decision == .approve);
}

test "App welcome uses session count" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    try app.state.status.setModel(std.testing.allocator, "model-a", "provider-a");
    app.working_dir = try std.testing.allocator.dupe(u8, "/tmp/work");

    try app.appendWelcome();
    try std.testing.expectEqual(tui_state.TranscriptKind.welcome, app.state.transcript.items[0].kind);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "Makai TUI") != null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "tips:") != null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "Alt+Enter") == null);

    app.state.clearTranscript();
    try app.state.addSession("s1", "saved");
    try app.appendWelcome();
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "tips:") == null);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "/resume") != null);
}

const MockAppSession = struct {
    steer_count: usize = 0,
    follow_up_count: usize = 0,
    submit_count: usize = 0,
    resume_count: usize = 0,
    cancel_count: usize = 0,
    clear_count: usize = 0,
    queued_counts: tui_runtime.QueuedCounts = .{},
    steers_consumed: u64 = 0,
    steer_enabled: bool = true,
    events: tui_runtime.TuiEventStream = undefined,
    events_initialized: bool = false,
    history_messages: []const ai_types.Message = &.{},
    compact_count: usize = 0,
    compact_focus: []u8 = &.{},
    compact_transcripts: std.ArrayList([]u8) = .empty,
    submitted: std.ArrayList([]u8) = .empty,
    steered: std.ArrayList([]u8) = .empty,
    followed: std.ArrayList([]u8) = .empty,
    submit_error: ?anyerror = null,

    fn session(self: *MockAppSession) tui_runtime.TuiSession {
        return .{
            .ctx = self,
            .ops = .{
                .start = start,
                .resume_session = resumeSession,
                .cancel = cancel,
                .submit_turn = submitTurn,
                .steer = steer,
                .follow_up = followUp,
                .clear_queued_messages = clearQueuedMessages,
                .queued_counts = queuedCounts,
                .steers_consumed = steersConsumed,
                .can_steer = canSteer,
                .switch_model = switchModel,
                .switch_model_exact = switchModelExact,
                .current_model = currentModel,
                .decide_tool_approval = decideToolApproval,
                .stream_events = streamEvents,
                .compact = compact,
                .history = history,
            },
        };
    }

    fn compact(ctx: ?*anyopaque, options: tui_runtime.CompactOptions) anyerror!void {
        const self = ptr(ctx);
        self.compact_count += 1;
        std.testing.allocator.free(self.compact_focus);
        self.compact_focus = try std.testing.allocator.dupe(u8, options.focus);
        for (self.compact_transcripts.items) |path| std.testing.allocator.free(path);
        self.compact_transcripts.clearRetainingCapacity();
        for (options.transcripts) |path| {
            const owned = try std.testing.allocator.dupe(u8, path);
            errdefer std.testing.allocator.free(owned);
            try self.compact_transcripts.append(std.testing.allocator, owned);
        }
    }

    fn history(ctx: ?*anyopaque) []const ai_types.Message {
        return ptr(ctx).history_messages;
    }

    fn ptr(ctx: ?*anyopaque) *MockAppSession {
        return @ptrCast(@alignCast(ctx.?));
    }

    fn start(ctx: ?*anyopaque) anyerror!void {
        _ = ctx;
    }

    fn resumeSession(ctx: ?*anyopaque) anyerror!void {
        const self = ptr(ctx);
        self.resume_count += 1;
        if (self.queued_counts.steering > 0) {
            self.queued_counts.steering -= 1;
        } else if (self.queued_counts.follow_up > 0) {
            self.queued_counts.follow_up -= 1;
        }
    }

    fn cancel(ctx: ?*anyopaque) void {
        const self = ptr(ctx);
        self.cancel_count += 1;
    }

    fn submitTurn(ctx: ?*anyopaque, text: []const u8) anyerror!void {
        const self = ptr(ctx);
        if (self.submit_error) |err| return err;
        self.submit_count += 1;
        const owned = try std.testing.allocator.dupe(u8, text);
        errdefer std.testing.allocator.free(owned);
        try self.submitted.append(std.testing.allocator, owned);
    }

    fn steer(ctx: ?*anyopaque, text: []const u8) anyerror!void {
        const self = ptr(ctx);
        const owned = try std.testing.allocator.dupe(u8, text);
        errdefer std.testing.allocator.free(owned);
        try self.steered.append(std.testing.allocator, owned);
        self.steer_count += 1;
        if (self.queued_counts.total() == 0) self.queued_counts.steering += 1;
    }

    fn followUp(ctx: ?*anyopaque, text: []const u8) anyerror!void {
        const self = ptr(ctx);
        const owned = try std.testing.allocator.dupe(u8, text);
        errdefer std.testing.allocator.free(owned);
        try self.followed.append(std.testing.allocator, owned);
        self.follow_up_count += 1;
        self.queued_counts.follow_up += 1;
    }

    fn clearQueuedMessages(ctx: ?*anyopaque) void {
        const self = ptr(ctx);
        self.clear_count += 1;
        self.queued_counts = .{};
    }

    fn queuedCounts(ctx: ?*anyopaque) tui_runtime.QueuedCounts {
        return ptr(ctx).queued_counts;
    }

    fn steersConsumed(ctx: ?*anyopaque) u64 {
        return ptr(ctx).steers_consumed;
    }

    fn canSteer(ctx: ?*anyopaque) bool {
        return ptr(ctx).steer_enabled;
    }

    fn switchModel(ctx: ?*anyopaque, model_id: []const u8) anyerror!void {
        _ = ctx;
        _ = model_id;
    }

    fn switchModelExact(ctx: ?*anyopaque, model: ai_types.Model) anyerror!void {
        _ = ctx;
        _ = model;
    }

    fn currentModel(ctx: ?*anyopaque) ?ai_types.Model {
        _ = ctx;
        return null;
    }

    fn decideToolApproval(ctx: ?*anyopaque, tool_call_id: []const u8, decision: tui_runtime.ToolApprovalDecision) anyerror!void {
        _ = ctx;
        _ = tool_call_id;
        _ = decision;
    }

    fn eventStream(self: *MockAppSession) *tui_runtime.TuiEventStream {
        if (!self.events_initialized) {
            self.events = tui_runtime.TuiEventStream.init(std.testing.allocator);
            self.events_initialized = true;
        }
        return &self.events;
    }

    fn streamEvents(ctx: ?*anyopaque) *tui_runtime.TuiEventStream {
        return ptr(ctx).eventStream();
    }

    fn deinit(self: *MockAppSession) void {
        if (self.events_initialized) self.events.deinit();
        for (self.submitted.items) |text| std.testing.allocator.free(text);
        self.submitted.deinit(std.testing.allocator);
        for (self.steered.items) |text| std.testing.allocator.free(text);
        self.steered.deinit(std.testing.allocator);
        for (self.followed.items) |text| std.testing.allocator.free(text);
        self.followed.deinit(std.testing.allocator);
        std.testing.allocator.free(self.compact_focus);
        for (self.compact_transcripts.items) |path| std.testing.allocator.free(path);
        self.compact_transcripts.deinit(std.testing.allocator);
    }
};

const zen_enter = tui_state.zen_enter_note ++ "\n\n";
const zen_leave = tui_state.zen_leave_note ++ "\n\n";

fn lastUserText(app: *const App) []const u8 {
    var index = app.state.transcript.items.len;
    while (index > 0) {
        index -= 1;
        const entry = &app.state.transcript.items[index];
        if (entry.kind == .user) return entry.text.items;
    }
    return "";
}

test "zen notes the next prompt once, echoes only the user's text, and lifts the note on leaving" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.submit("/zen");
    try app.submit("hello");
    try std.testing.expectEqualStrings(zen_enter ++ "hello", mock.submitted.items[0]);
    try std.testing.expectEqualStrings("hello", lastUserText(&app));
    const users = countKind(&app, .user);
    try app.appendRuntimeUserMessage(mock.submitted.items[0]);
    try std.testing.expectEqual(users, countKind(&app, .user));

    try app.submit("again");
    try std.testing.expectEqualStrings("again", mock.submitted.items[1]);

    try app.submit("/zen");
    try app.submit("bye");
    try std.testing.expectEqualStrings(zen_leave ++ "bye", mock.submitted.items[2]);
    try app.submit("after");
    try std.testing.expectEqualStrings("after", mock.submitted.items[3]);
}

fn countKind(app: *const App, kind: tui_state.TranscriptKind) usize {
    var count: usize = 0;
    for (app.state.transcript.items) |entry| {
        if (entry.kind == kind) count += 1;
    }
    return count;
}

test "zen sends no note when it is switched on and back off before anything is sent" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.submit("/zen");
    try app.submit("/zen");
    try app.submit("plain");
    try std.testing.expectEqualStrings("plain", mock.submitted.items[0]);
}

test "zen notes a steer and a follow-up, and shows each without the note" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.submit("/zen");
    app.state.status.streaming = true;
    try app.steer("fix the test");
    try std.testing.expectEqualStrings(zen_enter ++ "fix the test", mock.steered.items[0]);
    try std.testing.expectEqualStrings("fix the test", app.state.pending_steers.items[0]);
    try app.steer("and the docs");
    try std.testing.expectEqualStrings("and the docs", mock.steered.items[1]);

    var follow = App.initWithoutRuntime(std.testing.allocator);
    defer follow.deinit();
    var follow_mock = MockAppSession{};
    defer follow_mock.deinit();
    follow.session = follow_mock.session();
    try follow.submit("/zen");
    follow.state.status.streaming = true;
    try std.testing.expectEqual(true, try follow.queueFollowUp("then open a PR"));
    try std.testing.expectEqualStrings(zen_enter ++ "then open a PR", follow_mock.followed.items[0]);
    try std.testing.expectEqualStrings("then open a PR", follow.state.pending_follow_ups.items[0]);
    try std.testing.expectEqual(true, try follow.queueFollowUp("and merge"));
    try std.testing.expectEqualStrings("and merge", follow_mock.followed.items[1]);
}

test "zen notes a follow-up queued after leaving zen with the lifting note" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    try app.submit("/zen");
    try app.submit("first");
    try app.submit("/zen");
    app.state.status.streaming = true;
    try std.testing.expectEqual(true, try app.queueFollowUp("then open a PR"));
    try std.testing.expectEqualStrings(zen_leave ++ "then open a PR", mock.followed.items[0]);
}

test "zen notes messages held after an abort when they are sent" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.submit("/zen");
    try app.state.held_after_abort.append(std.testing.allocator, try std.testing.allocator.dupe(u8, "held one"));
    try app.submit("typed");
    try std.testing.expectEqualStrings(zen_enter ++ "held one\n\ntyped", mock.submitted.items[0]);
    try std.testing.expect(std.mem.indexOf(u8, lastUserText(&app), tui_state.zen_enter_note) == null);
}

test "zen notes the message an automatic compaction held, whether the run is idle or busy" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.submit("/zen");
    app.pending_after_compaction = try std.testing.allocator.dupe(u8, "after compaction");
    try app.sendPendingAfterCompaction(true, false);
    try std.testing.expectEqualStrings(zen_enter ++ "after compaction", mock.submitted.items[0]);

    var busy = App.initWithoutRuntime(std.testing.allocator);
    defer busy.deinit();
    var busy_mock = MockAppSession{};
    defer busy_mock.deinit();
    busy.session = busy_mock.session();
    try busy.submit("/zen");
    busy.pending_after_compaction = try std.testing.allocator.dupe(u8, "while busy");
    busy.pending_after_compaction_echo = try std.testing.allocator.dupe(u8, "shown");
    try busy.sendPendingAfterCompaction(true, true);
    try std.testing.expectEqualStrings(zen_enter ++ "while busy", busy_mock.steered.items[0]);
    try std.testing.expectEqualStrings("while busy", busy.state.pending_steers.items[0]);
}

test "zen fades out once on the frame the run ends, then shows the reply" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 30;
    const app = &model.app.?;
    try app.submit("/zen");
    app.state.status.streaming = true;
    try app.state.appendTranscript(.assistant, "the final reply");
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "the final reply") == null);

    app.state.status.streaming = false;
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "the final reply") == null);
    var tick: usize = 0;
    while (tick < 23) : (tick += 1) {
        _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
        try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "the final reply") == null);
    }
    _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "the final reply") != null);
}

test "zen opens a reply taller than the screen at its top and pages through it" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 20;
    const app = &model.app.?;
    try app.submit("/zen");
    app.state.status.streaming = true;
    var reply: std.Io.Writer.Allocating = .init(std.testing.allocator);
    defer reply.deinit();
    for (0..60) |i| try reply.writer.print("reply line {d}\n\n", .{i});
    try app.state.appendTranscript(.assistant, reply.written());
    _ = model.view(&tctx.ctx);
    app.state.status.streaming = false;
    for (0..30) |_| {
        _ = model.update(.{ .tick = .{ .timestamp = 0, .delta = 0 } }, &tctx.ctx);
        _ = model.view(&tctx.ctx);
    }
    _ = model.update(.{ .window_size = .{ .width = 80, .height = 20 } }, &tctx.ctx);

    const top = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, top, "done") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "reply line 0") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "reply line 59") == null);
    try std.testing.expect(std.mem.indexOf(u8, top, "PgDn") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "PgUp") == null);

    while (app.state.transcript_scroll > 0) _ = model.update(.{ .key = .{ .key = .page_down } }, &tctx.ctx);
    const bottom = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "reply line 59") != null);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "reply line 0\n") == null);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "PgUp") != null);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "PgDn") == null);
    try std.testing.expectEqual(@as(usize, 20), std.mem.count(u8, bottom, "\n") + 1);
}

test "zen captures the wheel while it is on and scrolls the reply with it" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 20;
    const app = &model.app.?;
    const tick: TuiModel.Msg = .{ .tick = .{ .timestamp = 0, .delta = 0 } };
    try std.testing.expect(model.update(tick, &tctx.ctx) != .batch);
    try app.submit("/zen");
    const entered = model.update(tick, &tctx.ctx);
    try std.testing.expect(entered.batch[0] == .enable_mouse);
    try std.testing.expect(model.update(tick, &tctx.ctx) != .batch);

    app.state.status.streaming = true;
    var reply: std.Io.Writer.Allocating = .init(std.testing.allocator);
    defer reply.deinit();
    for (0..60) |i| try reply.writer.print("reply line {d}\n\n", .{i});
    try app.state.appendTranscript(.assistant, reply.written());
    _ = model.view(&tctx.ctx);
    app.state.status.streaming = false;
    for (0..30) |_| {
        _ = model.update(tick, &tctx.ctx);
        _ = model.view(&tctx.ctx);
    }
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "reply line 0") != null);
    const wheel_down: TuiModel.Msg = .{ .mouse = .{ .x = 0, .y = 0, .button = .wheel_down, .event_type = .press } };
    while (app.state.transcript_scroll > 0) _ = model.update(wheel_down, &tctx.ctx);
    const bottom = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "reply line 59") != null);
    try std.testing.expect(std.mem.indexOf(u8, bottom, "reply line 0\n") == null);

    try std.testing.expect(model.update(.resumed, &tctx.ctx).batch[0] == .enable_mouse);
    try std.testing.expect(model.update(tick, &tctx.ctx) != .batch);

    try app.submit("/zen");
    const left = model.update(tick, &tctx.ctx);
    try std.testing.expect(left.batch[0] == .disable_mouse);
    try std.testing.expect(model.update(.resumed, &tctx.ctx) != .batch);
}

test "leaving zen mid-run after a run that ended with no reply returns the transcript to its tail" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 20;
    const app = &model.app.?;
    const tick: TuiModel.Msg = .{ .tick = .{ .timestamp = 0, .delta = 0 } };
    for (0..40) |i| {
        const line = try std.fmt.allocPrint(std.testing.allocator, "history {d}", .{i});
        defer std.testing.allocator.free(line);
        try app.state.appendTranscript(.system, line);
    }
    try app.submit("/zen");
    app.state.status.streaming = true;
    _ = model.update(tick, &tctx.ctx);
    _ = model.view(&tctx.ctx);
    app.state.status.streaming = false;
    try app.state.appendTranscript(.system, "cancelled");
    _ = model.update(tick, &tctx.ctx);
    _ = model.view(&tctx.ctx);
    try std.testing.expect(app.state.transcript_scroll > 0);
    try app.steer("/zen on");
    try std.testing.expect(app.state.transcript_scroll > 0);
    try app.steer("/zen");
    try std.testing.expectEqual(@as(usize, 0), app.state.transcript_scroll);
    try std.testing.expect(std.mem.indexOf(u8, model.view(&tctx.ctx), "SCROLL") == null);
}

test "zen names the model it is waiting on before the run's first step" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 20;
    const app = &model.app.?;
    try app.submit("/zen");
    try app.state.status.setModel(app.allocator, "space-bunny-free", "opencode-go");
    app.state.status.streaming = true;
    const settle = struct {
        var plain: [1 << 16]u8 = undefined;
        fn frames(m: *TuiModel, ctx: *zz.Context) []const u8 {
            for (0..2 * zen_view.change_ticks + 2) |_| {
                _ = m.view(ctx);
                m.app.?.state.anim_tick +%= 1;
            }
            const view = m.view(ctx);
            var len: usize = 0;
            var i: usize = 0;
            while (i < view.len) : (i += 1) {
                if (view[i] == 0x1b) {
                    while (i < view.len and view[i] != 'm') i += 1;
                    continue;
                }
                plain[len] = view[i];
                len += 1;
            }
            return plain[0..len];
        }
    };
    try std.testing.expect(std.mem.indexOf(u8, settle.frames(&model, &tctx.ctx), "waiting for space-bunny-free") != null);
    try app.state.appendTranscript(.thinking, "hmm");
    app.state.active_thinking_entry = app.state.transcript.items.len - 1;
    const thinking = settle.frames(&model, &tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, thinking, "thinking") != null);
    try std.testing.expect(std.mem.indexOf(u8, thinking, "waiting for") == null);
    try std.testing.expect(app.state.zen.settled());
}

test "App submit quit command requests quit" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try std.testing.expectError(error.QuitRequested, app.submit("/quit"));
}

test "App steer handles fallback empty and session paths" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();

    try app.steer("  steer fallback  ");
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("steer fallback", app.state.transcript.items[0].text.items);

    try app.steer("   ");
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);

    app.state.clearTranscript();
    var mock = MockAppSession{ .queued_counts = .{ .steering = 1 } };
    defer mock.deinit();
    app.session = mock.session();

    try app.steer(" steer me ");
    try std.testing.expectEqual(@as(usize, 1), mock.steer_count);
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.user, app.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("steer me", app.state.transcript.items[0].text.items);
    try std.testing.expectEqual(@as(usize, 1), app.state.pending_steers.items.len);
    try std.testing.expectEqual(@as(usize, 1), app.state.queue.steering);
}

test "TuiModel exits quit command while streaming" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    model.app.?.state.status.streaming = true;
    try model.app.?.state.replaceComposerBuffer("/quit");

    const cmd = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).quit, cmd);
}

test "TuiModel Shift Enter inserts newline without submitting" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.replaceComposerBuffer("first");

    const cmd = model.update(.{ .key = .{ .key = .enter, .modifiers = .{ .shift = true } } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqualStrings("first\n", model.app.?.state.composer.text());
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.transcript.items.len);
}

test "TuiModel Shift Tab cycles thinking level" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();

    try std.testing.expectEqual(ai_types.ThinkingLevel.low, model.app.?.state.thinking_level);
    const cmd = model.update(.{ .key = .{ .key = .tab, .modifiers = .{ .shift = true } } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqual(ai_types.ThinkingLevel.medium, model.app.?.state.thinking_level);
}

test "App drain quarantines late events until the next turn starts" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.session_id = try std.testing.allocator.dupe(u8, "deleted");
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 1,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "stale")),
    } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 0), app.state.transcript.items.len);

    try mock.eventStream().push(.{ .agent_start = .{ .generation = 2 } });
    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 2,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "fresh")),
    } });
    try app.drainEvents();
    var found_fresh = false;
    var found_stale = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.eql(u8, entry.text.items, "fresh")) found_fresh = true;
        if (std.mem.eql(u8, entry.text.items, "stale")) found_stale = true;
    }
    try std.testing.expect(found_fresh);
    try std.testing.expect(!found_stale);
    try std.testing.expect(!app.quarantine_events);
}

test "App drain exits quarantine on turn_start for remote-style streams" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.session_id = try std.testing.allocator.dupe(u8, "deleted");
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 1,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "stale")),
    } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 0), app.state.transcript.items.len);

    try mock.eventStream().push(.{ .turn_start = .{ .generation = 2 } });
    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 2,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "fresh")),
    } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("fresh", app.state.transcript.items[0].text.items);
    try std.testing.expect(!app.quarantine_events);
}

test "App drain ignores stale lifecycle events while quarantined" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.session_id = try std.testing.allocator.dupe(u8, "deleted");
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try mock.eventStream().push(.{ .agent_start = .{ .generation = 1 } });
    try mock.eventStream().push(.{ .turn_start = .{ .generation = 1 } });
    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 1,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "stale")),
    } });
    try app.drainEvents();
    try std.testing.expect(app.quarantine_events);
    try std.testing.expectEqual(@as(usize, 0), app.state.transcript.items.len);

    try mock.eventStream().push(.{ .turn_start = .{ .generation = 2 } });
    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 2,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "fresh")),
    } });
    try app.drainEvents();
    try std.testing.expect(!app.quarantine_events);
    try std.testing.expectEqual(@as(usize, 1), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("fresh", app.state.transcript.items[0].text.items);
}

test "App drain buffers fresh user message_end during quarantine until lifecycle marker" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.session_id = try std.testing.allocator.dupe(u8, "deleted");
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try mock.eventStream().push(.{ .message_end = .{
        .generation = 2,
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "next prompt")),
    } });
    try mock.eventStream().push(.{ .turn_start = .{ .generation = 2 } });
    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 2,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "reply")),
    } });
    try app.drainEvents();

    try std.testing.expect(!app.quarantine_events);
    var found_prompt = false;
    var found_reply = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.eql(u8, entry.text.items, "next prompt")) found_prompt = true;
        if (std.mem.eql(u8, entry.text.items, "reply")) found_reply = true;
    }
    try std.testing.expect(found_prompt);
    try std.testing.expect(found_reply);
}

test "App drain ends quarantine on fresh terminal error and surfaces it" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.session_id = try std.testing.allocator.dupe(u8, "deleted");
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try mock.eventStream().push(.{ .message_end = .{
        .generation = 2,
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "next prompt")),
    } });
    try mock.eventStream().push(.{ .@"error" = .{
        .generation = 2,
        .message = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "remote failed")),
    } });
    try app.drainEvents();

    try std.testing.expect(!app.quarantine_events);
    try std.testing.expectEqual(@as(usize, 0), app.quarantine_buffer.items.len);
    var found_error = false;
    for (app.state.transcript.items) |entry| {
        if (entry.kind == .@"error" and std.mem.eql(u8, entry.text.items, "remote failed")) found_error = true;
    }
    try std.testing.expect(found_error);
}

test "App drain keeps filtering stale generations after quarantine ends" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.session_id = try std.testing.allocator.dupe(u8, "deleted");
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try mock.eventStream().push(.{ .agent_start = .{ .generation = 2 } });
    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 2,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "fresh")),
    } });
    try app.drainEvents();
    try std.testing.expect(!app.quarantine_events);

    try mock.eventStream().push(.{ .text_delta = .{
        .generation = 1,
        .content_index = 0,
        .delta = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "late stale")),
    } });
    try app.drainEvents();
    var found_stale = false;
    for (app.state.transcript.items) |entry| {
        if (std.mem.eql(u8, entry.text.items, "late stale")) found_stale = true;
    }
    try std.testing.expect(!found_stale);
}

test "App submit clears pending session reset flag" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();
    app.pending_session_reset = true;

    try app.submit("hello");
    try std.testing.expect(!app.pending_session_reset);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
}

test "setting pickers apply selected values" {
    const runtime_ptr = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    runtime_ptr.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime_ptr;

    app.openPicker(.permission);
    try std.testing.expectEqual(tui_state.AppMode.picker, app.state.mode);
    try std.testing.expectEqual(tui_state.PickerKind.permission, app.state.picker_kind);
    app.state.menu_index = 1;
    try app.applySelectedPermission();
    try std.testing.expectEqual(tui_runtime.PermissionMode.ask, app.state.permission_mode);
    try std.testing.expectEqual(tui_runtime.PermissionMode.ask, runtime_ptr.permissionMode());
}

test "TuiModel drains events before routing Enter while streaming" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    model.app.?.state.status.streaming = true;
    try mock.eventStream().push(.{ .turn_end = .{ .stop_reason = .stop } });
    try mock.eventStream().push(.{ .agent_end = .{ .reason = .completed } });
    try model.app.?.state.composer.buffer.appendSlice(std.testing.allocator, "new turn");

    const cmd = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
    try std.testing.expectEqual(@as(usize, 0), mock.steer_count);
    try std.testing.expect(!model.app.?.state.status.streaming);
}

test "App drain keeps consecutive user messages distinct" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try mock.eventStream().push(.{ .message_start = .{ .role = .user } });
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "run pwd")),
    } });
    try mock.eventStream().push(.{ .message_start = .{ .role = .user } });
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "run uname -a")),
    } });

    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 2), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.user, app.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("run pwd", app.state.transcript.items[0].text.items);
    try std.testing.expectEqual(tui_state.TranscriptKind.user, app.state.transcript.items[1].kind);
    try std.testing.expectEqualStrings("run uname -a", app.state.transcript.items[1].text.items);
}

test "App drain suppresses consumption duplicate of steered message" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.steer("steer mid turn");
    mock.steers_consumed = 1;
    try mock.eventStream().push(.{ .message_end = .{
        .role = .assistant,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "turn one done")),
    } });
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .steering = true,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "steer mid turn")),
    } });

    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 2), app.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.user, app.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("steer mid turn", app.state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("turn one done", app.state.transcript.items[1].text.items);
    try std.testing.expectEqual(@as(usize, 0), app.state.pending_steers.items.len);
}

test "App drain keeps two steered echoes and renders unmatched user message" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.steer("steer one");
    try app.steer("steer two");
    mock.steers_consumed = 2;
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .steering = true,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "steer one")),
    } });
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .steering = true,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "steer two")),
    } });
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "submitted prompt")),
    } });

    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 3), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("steer one", app.state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("steer two", app.state.transcript.items[1].text.items);
    try std.testing.expectEqualStrings("submitted prompt", app.state.transcript.items[2].text.items);
    try std.testing.expectEqual(@as(usize, 0), app.state.pending_steers.items.len);
}

test "App drain reconciles pending steers when consumption events are evicted" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.steer("steer one");
    try app.steer("steer two");
    mock.steers_consumed = 2;
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .steering = true,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "steer two")),
    } });

    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 2), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("steer one", app.state.transcript.items[0].text.items);
    try std.testing.expectEqualStrings("steer two", app.state.transcript.items[1].text.items);
    try std.testing.expectEqual(@as(usize, 0), app.state.pending_steers.items.len);

    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "steer one")),
    } });

    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 3), app.state.transcript.items.len);
    try std.testing.expectEqualStrings("steer one", app.state.transcript.items[2].text.items);
    try std.testing.expectEqual(@as(usize, 0), app.state.pending_steers.items.len);
}

test "TuiModel inline render shows every steer echo while assistant streams" {
    var state = tui_state.AppState.init(std.testing.allocator);
    defer state.deinit();

    try state.applyEvent(.{ .message_start = .{ .role = .assistant } });
    try state.appendSteeredMessage("first steer");
    try state.appendTranscript(.system, "status row between steers");
    try state.appendSteeredMessage("second steer");
    try std.testing.expectEqual(@as(usize, 0), state.active_assistant_entry.?);
    try std.testing.expectEqual(@as(usize, 1), state.active_user_entry.?);

    const out = try TuiModel.renderInlineStream(std.testing.allocator, &state, 0, 0, 100, true);
    defer std.testing.allocator.free(out);
    try std.testing.expect(std.mem.indexOf(u8, out, "first steer") != null);
    try std.testing.expect(std.mem.indexOf(u8, out, "second steer") != null);
    try std.testing.expect(std.mem.indexOf(u8, out, "status row between steers") != null);
}

test "App drain auto-resumes remaining steering after completed turn" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{ .queued_counts = .{ .steering = 2 } };
    defer mock.deinit();
    app.session = mock.session();
    app.state.setQueuedCounts(mock.queued_counts);
    try mock.eventStream().push(.{ .message_end = .{
        .role = .user,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "run pwd")),
    } });

    try mock.eventStream().push(.{ .agent_end = .{ .reason = .completed } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 1), mock.resume_count);
    try std.testing.expectEqual(@as(usize, 1), app.state.queue.steering);
}

test "App drain does not auto-resume queued steering after error turn" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{ .queued_counts = .{ .steering = 1 } };
    defer mock.deinit();
    app.session = mock.session();

    try mock.eventStream().push(.{ .agent_end = .{ .reason = .@"error" } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 0), mock.resume_count);
    try std.testing.expectEqual(@as(usize, 1), app.state.queue.steering);
}

test "TuiModel local streaming Enter steers when steering available" {
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});

    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    model.app.?.runtime = runtime;
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    model.app.?.state.status.streaming = true;
    try model.app.?.state.composer.buffer.appendSlice(std.testing.allocator, "steer now");

    const cmd = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expectEqual(@as(usize, 1), mock.steer_count);
    try std.testing.expectEqualStrings("", model.app.?.state.composer.text());
    try std.testing.expectEqual(@as(usize, 1), model.app.?.state.queue.steering);
}

test "TuiModel stops Enter routing when drained event enters approval mode" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    model.app.?.state.status.streaming = true;
    try mock.eventStream().push(.{ .tool_approval_requested = .{
        .tool_call_id = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "call-approval")),
        .tool_name = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "edit_file")),
        .args_json = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "{\"path\":\"README.md\"}")),
    } });
    try model.app.?.state.composer.buffer.appendSlice(std.testing.allocator, "should wait");

    const cmd = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqual(tui_state.AppMode.approval, model.app.?.state.mode);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expectEqual(@as(usize, 0), mock.steer_count);
    try std.testing.expectEqualStrings("should wait", model.app.?.state.composer.text());
}

test "TuiModel allows /abort slash command during approval mode" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    model.app.?.state.status.streaming = true;
    try model.app.?.state.approval.setPending(std.testing.allocator, "call-approval", "edit_file", "edit_file", "{\"path\":\"README.md\"}");
    model.app.?.state.mode = .approval;

    const keys = [_]u21{ '/', 'a', 'b', 'o', 'r', 't' };
    for (keys) |c| _ = model.update(.{ .key = .{ .key = .{ .char = c } } }, &tctx.ctx);

    const cmd = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqual(tui_state.AppMode.normal, model.app.?.state.mode);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
    try std.testing.expect(!model.app.?.state.status.streaming);
    try std.testing.expectEqual(@as(usize, 1), model.app.?.state.transcript.items.len);
    try std.testing.expectEqual(tui_state.TranscriptKind.system, model.app.?.state.transcript.items[0].kind);
    try std.testing.expectEqualStrings("Turn aborted.", model.app.?.state.transcript.items[0].text.items);
}

test "TuiModel blocks non-abort slash commands during approval mode" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    model.app.?.session = mock.session();
    model.app.?.state.status.streaming = true;
    try model.app.?.state.approval.setPending(std.testing.allocator, "call-approval", "edit_file", "edit_file", "{\"path\":\"README.md\"}");
    model.app.?.state.mode = .approval;

    const keys = [_]u21{ '/', 'h', 'e', 'l', 'p' };
    for (keys) |c| _ = model.update(.{ .key = .{ .key = .{ .char = c } } }, &tctx.ctx);

    const cmd = model.update(.{ .key = .{ .key = .enter } }, &tctx.ctx);
    try std.testing.expectEqual(zz.Cmd(TuiModel.Msg).none, cmd);
    try std.testing.expectEqual(tui_state.AppMode.approval, model.app.?.state.mode);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.transcript.items.len);
    try std.testing.expectEqualStrings("/help", model.app.?.state.composer.text());
}

test "TuiModel moves composer cursor and edits in place" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();

    _ = model.update(.{ .key = .{ .key = .{ .char = 'a' } } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'b' } } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'c' } } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .left } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'X' } } }, &tctx.ctx);
    try std.testing.expectEqualStrings("abXc", model.app.?.state.composer.text());
    _ = model.update(.{ .key = .{ .key = .backspace } }, &tctx.ctx);
    try std.testing.expectEqualStrings("abc", model.app.?.state.composer.text());
}

test "session picker navigation pages through hidden rows" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.last_view_height = 8;
    try app.state.addSession("s1", "One");
    try app.state.addSession("s2", "Two");
    try app.state.addSession("s3", "Three");
    try app.state.addSession("s4", "Four");
    try app.state.addSession("s5", "Five");

    try std.testing.expectEqual(@as(usize, 4), TuiModel.visibleSessionCount(&app));
    TuiModel.moveSessionSelection(&app, 1);
    TuiModel.moveSessionSelection(&app, 1);
    TuiModel.moveSessionSelection(&app, 1);
    TuiModel.moveSessionSelection(&app, 1);
    try std.testing.expectEqual(@as(usize, 4), app.state.session_index);
    try std.testing.expectEqual(@as(usize, 1), app.state.session_scroll);
    try std.testing.expectEqual(@as(usize, 4), TuiModel.visibleSessionCount(&app));
}

test "session deletion refuses to remove the active session" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);
    try compat.fs.createDir(compat.fs.getCwd(), base);
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    app.session_id = try std.testing.allocator.dupe(u8, "active");
    try app.state.addSession("active", "Active session");
    try app.deleteSelectedSession();
    try std.testing.expectEqual(@as(usize, 1), app.state.sessions.items.len);
    try std.testing.expect(std.mem.indexOf(u8, app.state.transcript.items[0].text.items, "active session") != null);
}

test "session picker typing characters does not edit anything" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    model.app.?.state.mode = .session_picker;
    try model.app.?.state.addSession("s1", "Alpha");
    try model.app.?.state.addSession("s2", "Beta");
    model.app.?.state.session_index = 1;

    _ = model.update(.{ .key = .{ .key = .{ .char = 'g' } } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'p' } } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .backspace } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'd' }, .modifiers = .{ .ctrl = true } } }, &tctx.ctx);

    try std.testing.expectEqual(@as(usize, 2), model.app.?.state.sessions.items.len);
    try std.testing.expectEqual(@as(usize, 1), model.app.?.state.session_index);
    try std.testing.expectEqual(tui_state.AppMode.session_picker, model.app.?.state.mode);
}

fn sessionStoreBaseForAppTest(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir) ![]u8 {
    const base = try std.fs.path.join(allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    errdefer allocator.free(base);
    try compat.fs.createDir(compat.fs.getCwd(), base);
    return base;
}

fn saveTestSession(store: session_store.Store, id: []const u8, last_active: i64) !void {
    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, id),
        .model = try std.testing.allocator.dupe(u8, "model-a"),
        .provider = try std.testing.allocator.dupe(u8, "provider-a"),
        .last_active = last_active,
    };
    defer meta.deinit(std.testing.allocator);
    try store.save(meta, .{ .turn_start = .{} });
}

test "TuiModel PageUp scrolls the inline window and PageDown returns to the tail" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 60;
    tctx.ctx.height = 12;
    var i: usize = 0;
    while (i < 14) : (i += 1) {
        const text = try std.fmt.allocPrint(std.testing.allocator, "entry number {d}", .{i});
        defer std.testing.allocator.free(text);
        try model.app.?.state.appendTranscript(.user, text);
    }
    model.app.?.inline_history_flushed = 8;

    const tail = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, tail, "SCROLL") == null);
    try std.testing.expect(std.mem.indexOf(u8, tail, "entry number 13") != null);
    try std.testing.expect(std.mem.indexOf(u8, tail, "entry number 0") == null);

    _ = model.update(.{ .key = .{ .key = .page_up } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 5), model.app.?.state.transcript_scroll);
    const scrolled = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, scrolled, "SCROLL") != null);
    try std.testing.expect(std.mem.indexOf(u8, scrolled, "entry number 13") == null);
    try std.testing.expect(TuiModel.countLines(scrolled) <= 12);

    var n: usize = 0;
    while (n < 20) : (n += 1) _ = model.update(.{ .key = .{ .key = .page_up } }, &tctx.ctx);
    const top = model.view(&tctx.ctx);
    try std.testing.expect(std.mem.indexOf(u8, top, "entry number 0") != null);
    try std.testing.expect(std.mem.indexOf(u8, top, "SCROLL 100%") != null);
    const clamped = model.app.?.state.transcript_scroll;
    try std.testing.expect(clamped < 5 + 20 * 5);

    _ = model.update(.{ .key = .{ .key = .page_down } }, &tctx.ctx);
    try std.testing.expectEqual(clamped - 5, model.app.?.state.transcript_scroll);
    while (model.app.?.state.transcript_scroll > 0) _ = model.update(.{ .key = .{ .key = .page_down } }, &tctx.ctx);
    const back = model.view(&tctx.ctx);
    try std.testing.expectEqualStrings(tail, back);

    _ = model.update(.{ .key = .{ .key = .page_up } }, &tctx.ctx);
    try model.app.?.submit("/help");
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.transcript_scroll);
}

test "TuiModel PageUp and PageDown scroll the transcript" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    try model.app.?.state.appendTranscript(.user, "x");
    model.app.?.state.transcript_scroll = 0;

    _ = model.update(.{ .key = .{ .key = .page_up } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 5), model.app.?.state.transcript_scroll);
    _ = model.update(.{ .key = .{ .key = .page_down } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 0), model.app.?.state.transcript_scroll);
}

test "resume selected session clears delete reset flags" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    try saveTestSession(app.store.?, "s1", 1);
    try app.loadSessions();
    app.pending_session_reset = true;
    app.quarantine_events = true;

    try std.testing.expectError(error.NoRuntimeConfigured, app.resumeSelectedSession());
    try std.testing.expect(app.pending_session_reset);
    try std.testing.expect(app.quarantine_events);
}

test "resume selected session allows runtime without protocol" {
    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{});
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;

    try std.testing.expectError(error.NoStoreConfigured, app.resumeSelectedSession());

    app.store = try session_store.Store.init(std.testing.allocator, ".");

    try app.resumeSelectedSession();
}

fn unusedStream(
    ctx: ?*anyopaque,
    model: ai_types.Model,
    context: ai_types.Context,
    options: agent.ProtocolOptions,
    allocator: std.mem.Allocator,
) anyerror!*event_stream.AssistantMessageEventStream {
    _ = ctx;
    _ = model;
    _ = context;
    _ = options;
    _ = allocator;
    return error.Unexpected;
}

test "resume clears a compaction the saved session never finished" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = unusedStream } });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;
    app.store = try session_store.Store.init(std.testing.allocator, base);

    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "interrupted"),
        .model = try std.testing.allocator.dupe(u8, ""),
        .provider = try std.testing.allocator.dupe(u8, ""),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    try app.store.?.save(meta, .{ .agent_start = .{} });
    try app.store.?.save(meta, .{ .compaction_start = .{} });

    try app.loadSessions();
    try app.resumeSelectedSession();
    try std.testing.expectEqualStrings("interrupted", app.session_id);
    try std.testing.expect(!app.state.status.compacting);
    try std.testing.expect(!app.state.status.streaming);
}

test "resuming another session drops messages held from an abort in the one left" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = unusedStream } });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;
    app.store = try session_store.Store.init(std.testing.allocator, base);

    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "elsewhere"),
        .model = try std.testing.allocator.dupe(u8, ""),
        .provider = try std.testing.allocator.dupe(u8, ""),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    try app.store.?.save(meta, .{ .agent_start = .{} });

    try app.state.appendQueuedFollowUp("meant for the session being left");
    try app.state.holdQueuedAfterAbort();
    try std.testing.expectEqual(@as(usize, 1), app.state.held_after_abort.items.len);

    try app.loadSessions();
    try app.resumeSelectedSession();
    try std.testing.expectEqualStrings("elsewhere", app.session_id);
    try std.testing.expectEqual(@as(usize, 0), app.state.held_after_abort.items.len);
}

test "resume restores the session's thinking level, and a change after it is saved to that session" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = unusedStream } });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;
    app.store = try session_store.Store.init(std.testing.allocator, base);
    try runtime.setThinkingLevel(.low);
    app.state.thinking_level = .low;

    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "deep-thinker"),
        .model = try std.testing.allocator.dupe(u8, ""),
        .provider = try std.testing.allocator.dupe(u8, ""),
        .last_active = 1,
        .thinking_level = .high,
    };
    defer meta.deinit(std.testing.allocator);
    try app.store.?.save(meta, .{ .agent_start = .{} });
    try app.store.?.saveIndex(meta);

    try app.loadSessions();
    try app.resumeSelectedSession();
    try std.testing.expectEqual(ai_types.ThinkingLevel.high, app.state.thinking_level);
    try std.testing.expectEqual(ai_types.ThinkingLevel.high, runtime.thinkingLevel());

    app.cycleThinkingLevel();
    const cycled = app.state.thinking_level;
    try std.testing.expect(cycled != .high);
    var index = try app.store.?.loadIndex("deep-thinker");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(?ai_types.ThinkingLevel, cycled), index.thinking_level);
}

test "resume keeps every compaction transcript when it loads from the last compaction" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    const runtime = try std.testing.allocator.create(tui_runtime.TuiRuntime);
    errdefer std.testing.allocator.destroy(runtime);
    runtime.* = try tui_runtime.TuiRuntime.init(std.testing.allocator, .{ .protocol = .{ .stream_fn = unusedStream } });
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.runtime = runtime;
    app.store = try session_store.Store.init(std.testing.allocator, base);
    const store = app.store.?;

    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "two-compactions"),
        .model = try std.testing.allocator.dupe(u8, ""),
        .provider = try std.testing.allocator.dupe(u8, ""),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    const summary = agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept\n</summary>";
    const first_path = try store.transcriptPath("two-compactions", 1);
    defer std.testing.allocator.free(first_path);
    var first = tui_runtime.TuiEvent{ .compaction_end = .{ .outcome = .completed, .text = OwnedSlice(u8).initBorrowed(summary), .transcript = OwnedSlice(u8).initBorrowed(first_path) } };
    try store.save(meta, first);
    const filler_text = try std.testing.allocator.alloc(u8, 16 * 1024);
    defer std.testing.allocator.free(filler_text);
    @memset(filler_text, 'x');
    for (0..20) |_| try store.saveEvent("two-compactions", .{ .system_warning = .{ .message = OwnedSlice(u8).initBorrowed(filler_text) } });
    const offset = try store.conversationBytes("two-compactions");
    const second_path = try store.transcriptPath("two-compactions", 2);
    defer std.testing.allocator.free(second_path);
    first.compaction_end.transcript = OwnedSlice(u8).initBorrowed(second_path);
    try store.save(meta, first);
    meta.compaction_offset = offset;
    meta.compactions = 2;
    try store.saveIndex(meta);

    try app.loadSessions();
    try app.resumeSelectedSession();
    try std.testing.expectEqual(@as(usize, 2), app.compaction_transcripts.items.len);
    try std.testing.expectEqualStrings(first_path, app.compaction_transcripts.items[0]);
    try std.testing.expectEqualStrings(second_path, app.compaction_transcripts.items[1]);

    var index = try store.loadIndex("two-compactions");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(u32, 2), index.compactions);
}

test "resume selected session clears delete reset flags on success" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();

    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();
    app.runtime.?.run_async = false;

    if (app.store) |*store| store.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);

    const model = app.runtime.?.currentModel() orelse return error.NoModelConfigured;
    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "s1"),
        .model = try std.testing.allocator.dupe(u8, model.id),
        .provider = try std.testing.allocator.dupe(u8, model.provider),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    try app.store.?.save(meta, .{ .turn_start = .{} });

    try app.loadSessions();
    app.pending_session_reset = true;
    app.quarantine_events = true;
    app.quarantine_generation = 1;

    try app.resumeSelectedSession();
    try std.testing.expect(!app.pending_session_reset);
    try std.testing.expect(!app.quarantine_events);
    try std.testing.expectEqual(@as(usize, 0), app.quarantine_buffer.items.len);
    try std.testing.expectEqualStrings("s1", app.session_id);
}

test "resume of a session without a worktree resets the workspace to the launch directory" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();

    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();
    app.runtime.?.run_async = false;

    if (app.store) |*store| store.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);

    const model = app.runtime.?.currentModel() orelse return error.NoModelConfigured;
    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "s1"),
        .model = try std.testing.allocator.dupe(u8, model.id),
        .provider = try std.testing.allocator.dupe(u8, model.provider),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    try app.store.?.save(meta, .{ .turn_start = .{} });
    try app.loadSessions();

    app.worktree_attempted = true;
    if (app.working_dir.len > 0) std.testing.allocator.free(app.working_dir);
    app.working_dir = try std.testing.allocator.dupe(u8, "/tmp/managed-worktree-of-another-session");

    try app.resumeSelectedSession();
    try std.testing.expectEqualStrings(app.launch_dir, app.working_dir);
    try std.testing.expect(!app.worktree_attempted);
}

test "resume over OAP reopens the saved session after a turn has opened one, with the saved session's workspace" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "a reply" }} });
    const models = [_]ai_types.Model{defaultModel()};
    const execution = try tui_oap_execution.OapExecution.create(std.testing.allocator, .{ .protocol = provider.protocolClient(), .models = &models });
    defer execution.destroy();
    var app = try App.init(std.testing.allocator, .{ .models = &models, .remote = execution.remote() });
    defer app.deinit();
    if (app.store) |*store| store.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    execution.setHistory(.{ .ctx = &app.store.?, .load = loadSavedHistory });
    try saveTestSession(app.store.?, "saved-over-oap", 1);
    try app.loadSessions();

    try app.runtime.?.start();
    try std.testing.expect(app.runtime.?.started);
    app.worktree_attempted = true;
    try app.resumeSelectedSession();
    try std.testing.expectEqualStrings("saved-over-oap", app.session_id);
    try std.testing.expectEqualStrings("saved-over-oap", execution.session_id);
    try std.testing.expectEqualStrings(app.launch_dir, app.runtime.?.workingDirectory());
}

test "a refused resume over OAP leaves the open session's workspace and the shown directory as they were" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var provider = fixture_provider.MockProvider.init(.{ .steps = &.{.{ .text = "a reply" }} });
    const models = [_]ai_types.Model{defaultModel()};
    const execution = try tui_oap_execution.OapExecution.create(std.testing.allocator, .{ .protocol = provider.protocolClient(), .models = &models });
    defer execution.destroy();
    var app = try App.init(std.testing.allocator, .{ .models = &models, .remote = execution.remote() });
    defer app.deinit();
    if (app.store) |*store| store.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    var nothing_saved = RefusingHistory{};
    execution.setHistory(.{ .ctx = &nothing_saved, .load = RefusingHistory.load });
    try saveTestSession(app.store.?, "saved-elsewhere", 1);
    try app.loadSessions();

    try app.runtime.?.setWorkspaceRoot("/tmp/the-open-session");
    try App.replaceOwnedString(std.testing.allocator, &app.working_dir, "/tmp/the-open-session");
    try app.runtime.?.start();
    try std.testing.expectError(error.OapReopenRefused, app.resumeSelectedSession());
    try std.testing.expectEqualStrings("/tmp/the-open-session", app.runtime.?.workingDirectory());
    try std.testing.expectEqualStrings("/tmp/the-open-session", app.working_dir);
    try std.testing.expect(app.runtime.?.started);
}

const RefusingHistory = struct {
    fn load(ctx: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) anyerror!?[]const ai_types.Message {
        _ = ctx;
        _ = arena;
        _ = session_id;
        return null;
    }
};

const MockProvider = struct {
    fn stream(
        model: ai_types.Model,
        context: ai_types.Context,
        options: ?ai_types.StreamOptions,
        a: std.mem.Allocator,
    ) anyerror!*event_stream.AssistantMessageEventStream {
        _ = model;
        _ = context;

        const s = try a.create(event_stream.AssistantMessageEventStream);
        s.* = event_stream.AssistantMessageEventStream.init(a);
        _ = options;
        s.ownership = .{ .owned = ai_types.cloneAssistantMessageEvent };

        s.push(.{ .start = .{ .partial = .{
            .content = &.{},
            .api = "mock-api",
            .provider = "mock",
            .model = "mock-model",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = compat.time.nowMillis(),
            .is_owned = false,
        } } }) catch {};

        s.complete(try ai_types.cloneAssistantMessage(a, .{
            .content = &.{.{ .text = .{ .text = "ok" } }},
            .api = "mock-api",
            .provider = "mock",
            .model = "mock-model",
            .usage = .{},
            .stop_reason = .stop,
            .timestamp = compat.time.nowMillis(),
            .is_owned = false,
        }));
        s.markThreadDone();
        return s;
    }

    fn streamSimple(
        model: ai_types.Model,
        context: ai_types.Context,
        options: ?ai_types.SimpleStreamOptions,
        a: std.mem.Allocator,
    ) anyerror!*event_stream.AssistantMessageEventStream {
        _ = options;
        return stream(model, context, null, a);
    }
};

fn registerMockProvider(registry: *api_registry.ApiRegistry) !void {
    try registry.registerApiProvider(.{
        .api = "mock-api",
        .stream = MockProvider.stream,
        .stream_simple = MockProvider.streamSimple,
    }, null);
}

const test_model = ai_types.Model{
    .id = "mock-model",
    .name = "Mock",
    .api = "mock-api",
    .provider = "mock",
    .base_url = "",
    .reasoning = false,
    .input = &[_][]const u8{"text"},
    .cost = .{ .input = 0, .output = 0, .cache_read = 0, .cache_write = 0 },
    .context_window = 1024,
    .max_tokens = 256,
};

const test_messages = [_]ai_types.Message{
    .{ .user = .{
        .content = .{ .text = "hi" },
        .timestamp = 0,
    } },
};

fn testContext() ai_types.Context {
    return .{ .messages = &test_messages };
}

fn drainStreamAndVerify(allocator: std.mem.Allocator, stream: *event_stream.AssistantMessageEventStream) !void {
    var saw_start = false;
    while (stream.wait()) |ev| {
        var owned_ev = ev;
        defer ai_types.deinitAssistantMessageEvent(allocator, &owned_ev);
        if (ev == .start) saw_start = true;
    }
    try std.testing.expect(saw_start);
    try std.testing.expect(stream.getResult() != null);
}

test "ProductionRuntime initBridge gives stable registry pointer" {
    const allocator = std.testing.allocator;
    var production = try ProductionRuntime.init(allocator, .{});
    defer production.deinit();

    try registerMockProvider(&production.registry);
    production.initBridge();

    const protocol = production.options().protocol.?;
    const stream = try protocol.stream(test_model, testContext(), .{ .api_key = "test-key" }, allocator);
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    try drainStreamAndVerify(allocator, stream);
}

test "ProductionRuntime multiple sequential streams reuse stable pointer" {
    const allocator = std.testing.allocator;
    var production = try ProductionRuntime.init(allocator, .{});
    defer production.deinit();

    try registerMockProvider(&production.registry);
    production.initBridge();

    const protocol = production.options().protocol.?;

    for (0..3) |_| {
        const stream = try protocol.stream(test_model, testContext(), .{ .api_key = "test-key" }, allocator);
        defer {
            stream.deinit();
            allocator.destroy(stream);
        }
        try drainStreamAndVerify(allocator, stream);
    }
}

test "ProductionRuntime outlives stream threads from dropped TuiRuntime" {
    const allocator = std.testing.allocator;
    var production = try ProductionRuntime.init(allocator, .{});
    defer production.deinit();

    try registerMockProvider(&production.registry);
    production.initBridge();

    const stream = blk: {
        const options = production.options();
        const protocol = options.protocol.?;
        const s = try protocol.stream(test_model, testContext(), .{ .api_key = "test-key" }, allocator);
        break :blk s;
    };
    defer {
        stream.deinit();
        allocator.destroy(stream);
    }

    try drainStreamAndVerify(allocator, stream);
}

test "a granted credential does not change what the login badge reports" {
    const allocator = std.testing.allocator;

    var storage = oauth_storage.AuthStorage{
        .providers = std.StringHashMap(oauth_storage.ProviderAuth).init(allocator),
        .allocator = allocator,
    };
    defer storage.deinit();

    try storage.providers.put(try allocator.dupe(u8, "anthropic"), .{ .oauth = .{
        .refresh = try allocator.dupe(u8, "stored-refresh"),
        .access = try allocator.dupe(u8, "stored-access"),
        .expires = 0,
    } });

    try std.testing.expectEqual(App.LoginStatus.expired, App.loginStatusFor(&storage, "anthropic", false));

    try storage.putEphemeral("anthropic", .{ .api_key = try allocator.dupe(u8, "sk-granted") });

    try std.testing.expectEqual(App.LoginStatus.expired, App.loginStatusFor(&storage, "anthropic", false));
    try std.testing.expectEqual(App.LoginStatus.none, App.loginStatusFor(&storage, "kimi", false));
}

const compaction_history = [_]ai_types.Message{
    .{ .user = .{ .content = .{ .text = "question" }, .timestamp = 0 } },
    .{ .assistant = .{ .content = &.{.{ .text = .{ .text = "answer" } }}, .api = "", .provider = "", .model = "", .usage = .{}, .stop_reason = .stop, .timestamp = 0 } },
};

test "App /compact archives the history and hands the model every transcript so far" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try std.fs.path.join(std.testing.allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    defer std.testing.allocator.free(base);

    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);
    app.session_id = try std.testing.allocator.dupe(u8, "s1");
    var mock = MockAppSession{ .history_messages = &compaction_history };
    defer mock.deinit();
    app.session = mock.session();
    try app.recordCompactionTranscript("/older/compaction-1.jsonl");

    try app.submit("/compact  the parser ");

    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqualStrings("the parser", mock.compact_focus);
    try std.testing.expectEqual(@as(usize, 2), mock.compact_transcripts.items.len);
    try std.testing.expectEqualStrings("/older/compaction-1.jsonl", mock.compact_transcripts.items[0]);
    const archive = mock.compact_transcripts.items[1];
    try std.testing.expect(std.mem.endsWith(u8, archive, "s1" ++ std.fs.path.sep_str ++ "compaction-2.jsonl"));
    const data = try compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), archive, 64 * 1024);
    defer std.testing.allocator.free(data);
    try std.testing.expectEqual(@as(usize, 2), std.mem.count(u8, data, "\n"));
    try std.testing.expect(std.mem.indexOf(u8, data, "\"text\":\"answer\"") != null);

    try mock.eventStream().push(.{ .compaction_start = .{} });
    try mock.eventStream().push(.{ .compaction_end = .{
        .outcome = .completed,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, agent.compaction.header ++ " x\n\n<summary>\nkept state\n</summary>")),
        .transcript = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, archive)),
        .messages_before = 2,
        .tokens_before = 2400,
        .tokens_after = 300,
    } });
    try app.drainEvents();

    try std.testing.expectEqual(@as(usize, 2), app.compaction_transcripts.items.len);
    try std.testing.expectEqualStrings(archive, app.compaction_transcripts.items[1]);
    try std.testing.expect(!app.state.status.streaming);
    try std.testing.expect(!app.state.status.compacting);
    try std.testing.expectEqual(@as(usize, 300), app.state.status.context_used);
    const notice = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqual(tui_state.TranscriptKind.system, notice.kind);
    try std.testing.expect(std.mem.startsWith(u8, notice.text.items, "conversation compacted · 2 messages · ~2.4k → ~300 tokens"));
    try std.testing.expect(std.mem.indexOf(u8, notice.text.items, "kept state") != null);
}

test "App holds a run as streaming through a compaction made inside it, even after the turn before it ended, and records its transcript" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{ .history_messages = &compaction_history };
    defer mock.deinit();
    app.session = mock.session();
    try mock.eventStream().push(.{ .turn_end = .{ .stop_reason = .tool_use } });
    try app.drainEvents();
    try std.testing.expect(!app.state.status.streaming);

    try mock.eventStream().push(.{ .compaction_start = .{ .in_run = true } });
    try app.drainEvents();
    try std.testing.expect(app.state.status.compacting);
    try std.testing.expect(app.state.status.streaming);

    try mock.eventStream().push(.{ .compaction_end = .{
        .in_run = true,
        .outcome = .completed,
        .text = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, agent.compaction.header ++ " x\n\n<summary>\nkept state\n</summary>")),
        .transcript = OwnedSlice(u8).initOwned(try std.testing.allocator.dupe(u8, "/s/s1/compaction-1.jsonl")),
        .messages_before = 2,
        .tokens_before = 2400,
        .tokens_after = 300,
    } });
    try app.drainEvents();

    try std.testing.expect(app.state.status.streaming);
    try std.testing.expect(!app.state.status.compacting);
    try std.testing.expect(app.compaction_just_ended == null);
    try std.testing.expectEqual(@as(usize, 1), app.compaction_transcripts.items.len);
    try std.testing.expectEqualStrings("/s/s1/compaction-1.jsonl", app.compaction_transcripts.items[0]);
    try std.testing.expectEqual(@as(usize, 300), app.state.status.context_used);
}

test "App /compact reports an empty or freshly compacted history without compacting" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{};
    defer mock.deinit();
    app.session = mock.session();

    try app.submit("/compact");
    try std.testing.expectEqual(@as(usize, 0), mock.compact_count);
    const notice = app.state.transcript.items[app.state.transcript.items.len - 1];
    try std.testing.expectEqualStrings("Nothing to compact yet.", notice.text.items);

    var pair = try agent.compaction.historyMessages(std.testing.allocator, agent.compaction.header ++ " x", .{});
    defer for (&pair) |*message| message.deinit(std.testing.allocator);
    mock.history_messages = &pair;
    try app.submit("/compact");
    try std.testing.expectEqual(@as(usize, 0), mock.compact_count);
}

test "App sends the drafts queued during a compaction however the compaction ends" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    var mock = MockAppSession{ .history_messages = &compaction_history };
    defer mock.deinit();
    app.session = mock.session();

    const outcomes = [_]tui_runtime.TuiEvent.CompactionOutcome{ .cancelled, .failed, .completed };
    for (outcomes, 1..) |outcome, sent| {
        mock.queued_counts.steering = 1;
        try mock.eventStream().push(.{ .compaction_start = .{} });
        try mock.eventStream().push(.{ .compaction_end = .{ .outcome = outcome } });
        try app.drainEvents();
        try std.testing.expectEqual(sent, mock.resume_count);
        try std.testing.expect(!app.state.status.compacting);
    }
    try std.testing.expectEqual(@as(usize, 0), app.compaction_transcripts.items.len);
}

test "TuiModel up moves the cursor one visual row before touching history" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 24;
    const app = &model.app.?;
    try app.state.recordComposerHistory("older entry");
    try app.state.replaceComposerBuffer("alpha\nbeta");

    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqualStrings("alpha\nbeta", app.state.composer.text());
    try std.testing.expectEqual(@as(usize, 4), app.state.composer.cursor);

    _ = model.update(.{ .key = .{ .key = .down, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqual(@as(usize, 10), app.state.composer.cursor);
}

test "TuiModel up on the first visual row recalls history" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 24;
    const app = &model.app.?;
    try app.state.recordComposerHistory("older entry");
    try app.state.replaceComposerBuffer("alpha\nbeta");

    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqualStrings("older entry", app.state.composer.text());
}

test "TuiModel keeps walking history while a recalled entry is shown unedited" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 24;
    const app = &model.app.?;
    try app.state.recordComposerHistory("first cmd");
    try app.state.recordComposerHistory("multi\nline cmd");

    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqualStrings("multi\nline cmd", app.state.composer.text());
    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqualStrings("first cmd", app.state.composer.text());
}

test "TuiModel up at the first row of an edited recall discards the edits" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 80;
    tctx.ctx.height = 24;
    const app = &model.app.?;
    try app.state.recordComposerHistory("one");
    try app.state.recordComposerHistory("two");

    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    _ = model.update(.{ .key = .{ .key = .{ .char = 'x' }, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqualStrings("twox", app.state.composer.text());
    _ = model.update(.{ .key = .{ .key = .up, .modifiers = .{} } }, &tctx.ctx);
    try std.testing.expectEqualStrings("one", app.state.composer.text());
}

test "composer vertical moves keep the goal column across rows" {
    var app = App.initWithoutRuntime(std.testing.allocator);
    defer app.deinit();
    try app.state.replaceComposerBuffer("abcdef\nxy\nuvwxyz");

    try app.handleComposerVertical(80, -1);
    try std.testing.expectEqual(@as(usize, 9), app.state.composer.cursor);
    try app.handleComposerVertical(80, -1);
    try std.testing.expectEqual(@as(usize, 6), app.state.composer.cursor);
    try app.handleComposerVertical(80, 1);
    try std.testing.expectEqual(@as(usize, 9), app.state.composer.cursor);
    try app.handleComposerVertical(80, 1);
    try std.testing.expectEqual(@as(usize, 16), app.state.composer.cursor);
}

test "TuiModel flush budget ignores the composer's grown height" {
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator), .render_mode = .inline_history };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    tctx.ctx.width = 40;
    tctx.ctx.height = 24;
    const app = &model.app.?;

    const empty_budget = model.flushBudget(app, &tctx.ctx);
    try app.state.replaceComposerBuffer("aaaa bbbb cccc dddd eeee ffff gggg hhhh iiii jjjj kkkk llll mmmm nnnn oooo pppp");
    const grown_budget = model.flushBudget(app, &tctx.ctx);
    try std.testing.expectEqual(empty_budget, grown_budget);
}

fn pushErrorRun(app: *App, mock: *MockAppSession, message: []const u8, reason: tui_runtime.TuiEndReason) !void {
    try mock.eventStream().push(.{ .@"error" = .{ .message = OwnedSlice(u8).initOwned(try app.allocator.dupe(u8, message)) } });
    try mock.eventStream().push(.{ .agent_end = .{ .reason = reason } });
    try app.drainEvents();
}

const auto_continue_harness = struct {
    app: App,
    mock: *MockAppSession,

    fn init() !@This() {
        const mock = try std.testing.allocator.create(MockAppSession);
        mock.* = .{};
        errdefer std.testing.allocator.destroy(mock);
        var harness = @This(){
            .app = App.initWithoutRuntime(std.testing.allocator),
            .mock = mock,
        };
        harness.app.session = mock.session();
        return harness;
    }

    fn deinit(self: *@This()) void {
        self.app.deinit();
        self.mock.deinit();
        std.testing.allocator.destroy(self.mock);
    }

    fn failRun(self: *@This(), message: []const u8, reason: tui_runtime.TuiEndReason) !void {
        try pushErrorRun(&self.app, self.mock, message, reason);
    }

    fn pump(self: *@This(), advance_ms: i64) void {
        self.app.pumpAutoContinue(compat.time.nowMillis() + advance_ms);
    }

    fn pastDelay(self: *@This()) void {
        self.pump(@intCast(tui_auto_continue.default_delay_ms));
    }

    fn transcriptHas(self: *@This(), needle: []const u8) bool {
        for (self.app.state.transcript.items) |entry| {
            if (std.mem.indexOf(u8, entry.text.items, needle) != null) return true;
        }
        return false;
    }
};

test "a run ending in a provider error schedules one continue and says so" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(harness.app.auto_continue.pending());
    try std.testing.expect(harness.transcriptHas("Continuing in 3s"));

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 1), harness.mock.submit_count);
    try std.testing.expectEqual(@as(usize, 0), harness.mock.resume_count);
    try std.testing.expect(harness.transcriptHas("on your behalf"));
}

test "the automatic continue holds when the second run fails the same way" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 1), harness.mock.submit_count);

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 1), harness.mock.submit_count);
    try std.testing.expect(!harness.app.auto_continue.pending());
}

test "a user turn after a failure lets a later one nudge again" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 1), harness.mock.submit_count);

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try harness.app.submit("try the other model");
    try std.testing.expect(!harness.app.auto_continue.continued);

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 3), harness.mock.submit_count);
}

test "a cancelled run never schedules a continue" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .cancelled);
    try std.testing.expect(!harness.app.auto_continue.pending());

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
    try std.testing.expect(!harness.transcriptHas("Continuing in"));
}

test "a payment failure never schedules a continue" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("opencode-go request failed: HTTP 402 {\"error\":{\"message\":\"Insufficient balance\"}}", .@"error");
    try std.testing.expect(!harness.app.auto_continue.pending());

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
    try std.testing.expect(!harness.transcriptHas("Continuing in"));
}

test "an auth failure never schedules a continue" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 401 (check ANTHROPIC_API_KEY is valid) (authentication_error: invalid x-api-key)", .@"error");
    try std.testing.expect(!harness.app.auto_continue.pending());

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
    try std.testing.expect(!harness.transcriptHas("Continuing in"));
}

test "a context overflow never schedules a continue" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("prompt is too long: 250000 tokens > 200000 maximum", .@"error");
    try std.testing.expect(!harness.app.auto_continue.pending());

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
    try std.testing.expect(!harness.transcriptHas("Continuing in"));
}

test "a user turn inside the delay says the continue was dropped" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(harness.transcriptHas("Continuing in 3s"));

    try harness.app.submit("never mind, I will retype it");
    try std.testing.expect(harness.transcriptHas("the automatic continue was dropped"));

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 1), harness.mock.submit_count);
}

test "a clean run says nothing about a continue that was never armed" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 401 (check the key)", .@"error");
    try harness.app.submit("let me fix the key");
    try std.testing.expect(!harness.transcriptHas("dropped"));
}

test "an abort before the delay is up drops the pending continue" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(harness.app.auto_continue.pending());

    try harness.app.submit("/abort");
    try std.testing.expect(!harness.app.auto_continue.pending());

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
}

test "a follow-up queued before the delay is up drops the pending continue" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(harness.app.auto_continue.pending());

    _ = try harness.app.queueFollowUp("never mind, use the other key");
    try std.testing.expect(!harness.app.auto_continue.pending());

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
}

test "a follow-up already queued at the failure means no nudge is announced" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    _ = try harness.app.queueFollowUp("then try the other key");
    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(!harness.app.auto_continue.pending());
    try std.testing.expect(!harness.transcriptHas("Continuing in"));

    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
}

test "the automatic continue waits for an automatic compaction like a typed one" {
    var mock = MockAppSession{ .history_messages = &auto_compact_history };
    defer mock.deinit();
    var app = try autoCompactTestApp(&mock);
    defer app.deinit();

    try pushErrorRun(&app, &mock, "anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(app.auto_continue.pending());

    app.pumpAutoContinue(compat.time.nowMillis() + @as(i64, @intCast(tui_auto_continue.default_delay_ms)));
    try std.testing.expectEqual(@as(usize, 1), mock.compact_count);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expect(app.pending_after_compaction != null);

    try mock.eventStream().push(.{ .compaction_end = .{ .outcome = .completed } });
    try app.drainEvents();
    try std.testing.expectEqual(@as(usize, 1), mock.submit_count);
}

test "a picker open at the deadline defers the nudge rather than spending it" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    harness.app.state.mode = .picker;
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
    try std.testing.expect(harness.app.auto_continue.pending());

    harness.app.state.mode = .normal;
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 1), harness.mock.submit_count);
}

test "the nudge waits out its delay and holds while a run is still going" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    harness.pump(0);
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);

    harness.app.state.status.streaming = true;
    harness.pastDelay();
    try std.testing.expectEqual(@as(usize, 0), harness.mock.submit_count);
}

test "resuming a session whose last run ended in an error does not nudge" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try sessionStoreBaseForAppTest(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);

    var production = try ProductionRuntime.init(std.testing.allocator, .{});
    defer production.deinit();
    production.initBridge();

    var app = try App.init(std.testing.allocator, production.options());
    defer app.deinit();
    app.runtime.?.run_async = false;

    if (app.store) |*store| store.deinit();
    app.store = try session_store.Store.init(std.testing.allocator, base);

    const model = app.runtime.?.currentModel() orelse return error.NoModelConfigured;
    var meta = session_store.SessionMetadata{
        .session_id = try std.testing.allocator.dupe(u8, "s-error"),
        .model = try std.testing.allocator.dupe(u8, model.id),
        .provider = try std.testing.allocator.dupe(u8, model.provider),
        .last_active = 1,
    };
    defer meta.deinit(std.testing.allocator);
    const saved_error = try std.testing.allocator.dupe(u8, "anthropic request failed: HTTP 400 invalid_request_error");
    defer std.testing.allocator.free(saved_error);
    try app.store.?.save(meta, .{ .@"error" = .{ .message = OwnedSlice(u8).initOwned(saved_error) } });
    try app.store.?.saveEvent("s-error", .{ .agent_end = .{ .reason = .@"error" } });

    try app.loadSessions();
    app.state.session_index = 0;
    try app.resumeSelectedSession();

    try std.testing.expect(!app.auto_continue.pending());
    try std.testing.expectEqualStrings("", app.run_error_text);
    for (app.state.transcript.items) |entry| {
        try std.testing.expect(std.mem.indexOf(u8, entry.text.items, "Continuing in") == null);
    }
    app.pumpAutoContinue(compat.time.nowMillis() + @as(i64, @intCast(tui_auto_continue.default_delay_ms)));
    try std.testing.expectEqual(@as(usize, 0), app.runtime.?.steersConsumedCount());
}

test "Esc while a run is going aborts it and drops the pending continue" {
    var mock = MockAppSession{};
    defer mock.deinit();
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    const app = &model.app.?;
    app.session = mock.session();

    try pushErrorRun(app, &mock, "anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(app.auto_continue.pending());

    app.state.status.streaming = true;
    model.handleEscape(app);
    try std.testing.expectEqual(@as(usize, 1), mock.cancel_count);
    try std.testing.expect(!app.auto_continue.pending());

    app.pumpAutoContinue(compat.time.nowMillis() + @as(i64, @intCast(tui_auto_continue.default_delay_ms)));
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
}

test "Esc closing a picker drops the pending continue" {
    var mock = MockAppSession{};
    defer mock.deinit();
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    const app = &model.app.?;
    app.session = mock.session();
    const after_delay = compat.time.nowMillis() + @as(i64, @intCast(tui_auto_continue.default_delay_ms));

    try pushErrorRun(app, &mock, "anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    app.state.mode = .picker;
    app.pumpAutoContinue(after_delay);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
    try std.testing.expect(app.auto_continue.pending());

    _ = model.update(.{ .key = .{ .key = .escape } }, &tctx.ctx);
    try std.testing.expectEqual(tui_state.AppMode.normal, app.state.mode);
    try std.testing.expect(!app.auto_continue.pending());

    app.pumpAutoContinue(after_delay);
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
}

test "Ctrl+C drops the pending continue" {
    var mock = MockAppSession{};
    defer mock.deinit();
    var model = TuiModel{ .app = App.initWithoutRuntime(std.testing.allocator) };
    defer model.deinit();
    var tctx: TestContext = undefined;
    tctx.setup();
    defer tctx.deinit();
    const app = &model.app.?;
    app.session = mock.session();

    try pushErrorRun(app, &mock, "anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expect(app.auto_continue.pending());

    _ = model.handleInterrupt(app, &tctx.ctx);
    try std.testing.expect(!app.auto_continue.pending());

    app.pumpAutoContinue(compat.time.nowMillis() + @as(i64, @intCast(tui_auto_continue.default_delay_ms)));
    try std.testing.expectEqual(@as(usize, 0), mock.submit_count);
}

test "a run that ends clean leaves no error text for a later failure" {
    var harness = try auto_continue_harness.init();
    defer harness.deinit();

    try harness.failRun("anthropic request failed: HTTP 400 invalid_request_error", .@"error");
    try std.testing.expectEqualStrings("anthropic request failed: HTTP 400 invalid_request_error", harness.app.run_error_text);

    try harness.mock.eventStream().push(.{ .agent_end = .{ .reason = .completed } });
    try harness.app.drainEvents();
    try std.testing.expectEqualStrings("", harness.app.run_error_text);
}
