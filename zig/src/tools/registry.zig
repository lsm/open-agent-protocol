const std = @import("std");
const agent = @import("agent");
const permission = @import("permission");
const shell = @import("tools/shell");
const file = @import("tools/file");
const edit = @import("tools/edit");
const search = @import("tools/search");
const workspace = @import("tools/workspace");
const artifact = @import("tools/artifact");
const hashline = @import("tools/hashline");
pub const mcp_bridge = @import("tools/mcp_bridge");

pub const ToolRegistry = struct {
    tools: std.ArrayList(agent.AgentTool) = .empty,

    pub fn init() ToolRegistry {
        return .{};
    }

    pub fn deinit(self: *ToolRegistry, allocator: std.mem.Allocator) void {
        self.tools.deinit(allocator);
        self.* = undefined;
    }

    pub fn register(self: *ToolRegistry, allocator: std.mem.Allocator, tool: agent.AgentTool) !void {
        if (self.resolve(tool.name) != null) return error.DuplicateTool;
        try self.tools.append(allocator, tool);
    }

    pub fn replaceOrRegister(self: *ToolRegistry, allocator: std.mem.Allocator, tool: agent.AgentTool) !void {
        for (self.tools.items) |*existing| {
            if (std.mem.eql(u8, existing.name, tool.name)) {
                existing.* = tool;
                return;
            }
        }
        try self.tools.append(allocator, tool);
    }

    pub fn registerDefaults(self: *ToolRegistry, allocator: std.mem.Allocator) !void {
        for (defaultTools()) |tool| try self.register(allocator, tool);
    }

    pub fn registerMcpBridge(self: *ToolRegistry, allocator: std.mem.Allocator, bridge: *mcp_bridge.McpBridge) !void {
        var tools = std.ArrayList(agent.AgentTool).empty;
        defer tools.deinit(allocator);
        try bridge.appendAgentTools(&tools);
        for (tools.items) |tool| try self.replaceOrRegister(allocator, tool);
    }

    pub fn resolve(self: *const ToolRegistry, name: []const u8) ?agent.AgentTool {
        for (self.tools.items) |tool| if (std.mem.eql(u8, tool.name, name)) return tool;
        return null;
    }

    pub fn list(self: *const ToolRegistry) []const agent.AgentTool {
        return self.tools.items;
    }

};

pub fn defaultTools() []const agent.AgentTool {
    return &.{
        shell.execute_tool,
        file.read_tool,
        file.write_tool,
        file.stat_tool,
        edit.apply_tool,
        hashline.read_tool,
        hashline.edit_tool,
        search.text_tool,
        workspace.info_tool,
        workspace.list_tool,
        workspace.git_status_tool,
        artifact.retrieve_tool,
    };
}

test "registry registers resolves and lists defaults" {
    var registry = ToolRegistry.init();
    defer registry.deinit(std.testing.allocator);
    try registry.registerDefaults(std.testing.allocator);
    try std.testing.expect(registry.resolve("shell_execute") != null);
    try std.testing.expect(registry.resolve("file_read") != null);
    try std.testing.expect(registry.resolve("artifact_retrieve") != null);
    try std.testing.expect(registry.resolve("hashline_read") != null);
    try std.testing.expect(registry.resolve("hashline_edit") != null);
    try std.testing.expectEqual(@as(usize, 12), registry.list().len);
    for (registry.list()) |tool| try std.testing.expect(tool.short_description != null);
    try std.testing.expectError(error.DuplicateTool, registry.register(std.testing.allocator, shell.execute_tool));
    const replacement = agent.AgentTool{ .label = "Replacement Shell", .name = "shell_execute", .description = "Replacement shell tool.", .short_description = "Replacement shell", .parameters_schema_json = shell.schema_execute, .execute = shell.execute };
    try registry.replaceOrRegister(std.testing.allocator, replacement);
    try std.testing.expectEqualStrings("Replacement Shell", registry.resolve("shell_execute").?.label);
    try std.testing.expectEqual(@as(usize, 12), registry.list().len);
}

test "each declared kind yields the decision its own schema args earn" {
    var engine = try permission.PermissionEngine.initEmpty(std.testing.allocator, .{ .workspace_root = "/workspace" });
    defer engine.deinit();

    const Case = struct {
        name: []const u8,
        operation: permission.Operation,
        args: []const u8,
        decision: permission.PermissionDecision,
    };
    const cases = [_]Case{
        .{ .name = "file_stat", .operation = .read, .args = "{\"description\":\"d\",\"workspace_root\":\"/workspace\",\"path\":\"src/main.zig\"}", .decision = .allow },
        .{ .name = "file_stat", .operation = .read, .args = "{\"description\":\"d\",\"workspace_root\":\"/workspace\",\"path\":\"/etc/passwd\"}", .decision = .deny },
        .{ .name = "workspace_git_status", .operation = .shell, .args = "{\"description\":\"d\",\"workspace_root\":\"/workspace\"}", .decision = .prompt },
        .{ .name = "workspace_info", .operation = .read, .args = "{\"description\":\"d\",\"workspace_root\":\"/workspace\"}", .decision = .prompt },
        .{ .name = "search_text", .operation = .read, .args = "{\"description\":\"d\",\"workspace_root\":\"/workspace\",\"query\":\"needle\"}", .decision = .prompt },
        .{ .name = "artifact_retrieve", .operation = .read, .args = "{\"description\":\"d\",\"reference\":\"shell_execute:call\"}", .decision = .prompt },
    };
    for (cases) |case| {
        const declared = declaredOperation(case.name) orelse return error.UnknownTool;
        try std.testing.expectEqual(case.operation, declared);
        try std.testing.expectEqual(case.decision, engine.evaluateTool(declared, case.name, case.args));
    }
}

fn declaredOperation(name: []const u8) ?permission.Operation {
    for (defaultTools()) |tool| {
        if (std.mem.eql(u8, tool.name, name)) return tool.operation;
    }
    return null;
}

test "every built-in declares an operation kind, so no built-in resolves to unknown" {
    for (defaultTools()) |tool| {
        try std.testing.expect(
            tool.operation != .unknown,
        );
        try std.testing.expect(
            permission.resolveOperation(tool.operation, tool.name) == tool.operation,
        );
    }
}
