
const std = @import("std");
const Context = @import("../core/context.zig").Context;
const Environment = @import("../core/environment.zig").Environment;
const command = @import("../core/command.zig");
const model_contract = @import("../core/model.zig");
const keys = @import("../input/keys.zig");
const mouse_input = @import("../input/mouse.zig");
const message = @import("../core/message.zig");

pub const Options = struct {
    width: u16 = 80,
    height: u16 = 24,
    environment: Environment = .{},
};

pub fn Harness(comptime Model: type) type {
    model_contract.validate(Model, "Model");

    const init_fallible = model_contract.returnsError(@TypeOf(Model.init));
    const update_fallible = model_contract.returnsError(@TypeOf(Model.update));
    const view_fallible = model_contract.returnsError(@TypeOf(Model.view));

    const UserMsg = Model.Msg;
    const UserCmd = command.Cmd(UserMsg);

    return struct {
        allocator: std.mem.Allocator,
        arena: std.heap.ArenaAllocator,
        environment: Environment,
        context: Context,
        model: Model,

        quit: bool = false,
        effects: std.array_list.Managed(UserCmd),

        pending_tick: ?u64 = null,
        pending_tick_scheduled_at: u64 = 0,
        every_interval: ?u64 = null,
        last_every_tick: u64 = 0,

        started: bool = false,

        const Self = @This();

        pub const Error = std.mem.Allocator.Error ||
            error{InvalidUtf8} ||
            model_contract.ErrorSet(@TypeOf(Model.init)) ||
            model_contract.ErrorSet(@TypeOf(Model.update)) ||
            model_contract.ErrorSet(@TypeOf(Model.view));

        pub fn init(allocator: std.mem.Allocator, io: std.Io, options: Options) !Self {
            var self = Self{
                .allocator = allocator,
                .arena = std.heap.ArenaAllocator.init(allocator),
                .environment = options.environment,
                .context = undefined,
                .model = undefined,
                .effects = std.array_list.Managed(UserCmd).init(allocator),
            };

            self.context = Context.init(allocator, allocator, io, &self.environment);
            self.context.width = options.width;
            self.context.height = options.height;

            return self;
        }

        pub fn deinit(self: *Self) void {
            if (self.started and @hasDecl(Model, "deinit")) {
                self.model.deinit();
            }
            self.effects.deinit();
            self.context.deinit();
            self.arena.deinit();
        }

        pub fn start(self: *Self) Error!void {
            self.bindFrameAllocator();
            const cmd = if (comptime init_fallible)
                try self.model.init(&self.context)
            else
                self.model.init(&self.context);
            self.started = true;
            try self.process(cmd);
        }

        pub fn nextFrame(self: *Self, delta_ns: u64) void {
            _ = self.arena.reset(.retain_capacity);
            self.bindFrameAllocator();
            self.context.frame += 1;
            self.context.delta = delta_ns;
            self.context.elapsed += delta_ns;
        }

        pub fn send(self: *Self, user_msg: UserMsg) Error!void {
            self.bindFrameAllocator();
            const cmd = if (comptime update_fallible)
                try self.model.update(user_msg, &self.context)
            else
                self.model.update(user_msg, &self.context);
            try self.process(cmd);
        }

        pub fn press(self: *Self, key: keys.KeyEvent) Error!void {
            comptime requireField("key", "press");
            try self.send(@unionInit(UserMsg, "key", key));
        }

        pub fn pressChar(self: *Self, c: u21) Error!void {
            try self.press(.{ .key = .{ .char = c } });
        }

        pub fn typeText(self: *Self, text: []const u8) Error!void {
            var it = (try std.unicode.Utf8View.init(text)).iterator();
            while (it.nextCodepoint()) |c| {
                try self.pressChar(c);
            }
        }

        pub fn mouse(self: *Self, event: mouse_input.MouseEvent) Error!void {
            comptime requireField("mouse", "mouse");
            try self.send(@unionInit(UserMsg, "mouse", event));
        }

        pub fn resize(self: *Self, width: u16, height: u16) Error!void {
            self.context.width = width;
            self.context.height = height;
            if (@hasField(UserMsg, "window_size")) {
                try self.send(@unionInit(UserMsg, "window_size", .{
                    .width = width,
                    .height = height,
                }));
            }
        }

        pub fn advance(self: *Self, delta_ns: u64) Error!void {
            comptime requireField("tick", "advance");
            self.nextFrame(delta_ns);

            if (self.pending_tick) |deadline| {
                if (self.context.elapsed >= deadline) {
                    self.pending_tick = null;
                    try self.sendTick(self.context.elapsed -| self.pending_tick_scheduled_at);
                }
            }

            if (self.every_interval) |interval| {
                if (self.context.elapsed - self.last_every_tick >= interval) {
                    const tick_delta = self.context.elapsed -| self.last_every_tick;
                    self.last_every_tick = self.context.elapsed;
                    try self.sendTick(tick_delta);
                }
            }
        }

        pub fn view(self: *Self) Error![]const u8 {
            self.bindFrameAllocator();
            return if (comptime view_fallible)
                try self.model.view(&self.context)
            else
                self.model.view(&self.context);
        }

        pub fn plainView(self: *Self) Error![]const u8 {
            return stripAnsi(self.context.allocator, try self.view());
        }

        pub fn viewContains(self: *Self, needle: []const u8) Error!bool {
            return std.mem.indexOf(u8, try self.plainView(), needle) != null;
        }

        pub fn hasQuit(self: *const Self) bool {
            return self.quit;
        }

        pub fn recordedEffects(self: *const Self) []const UserCmd {
            return self.effects.items;
        }

        pub fn title(self: *const Self) ?[]const u8 {
            var i = self.effects.items.len;
            while (i > 0) {
                i -= 1;
                if (self.effects.items[i] == .set_title) return self.effects.items[i].set_title;
            }
            return null;
        }

        pub fn clearEffects(self: *Self) void {
            self.effects.clearRetainingCapacity();
        }

        fn sendTick(self: *Self, delta: u64) Error!void {
            try self.send(@unionInit(UserMsg, "tick", .{
                .timestamp = @intCast(self.context.elapsed),
                .delta = delta,
            }));
        }

        fn process(self: *Self, cmd: UserCmd) Error!void {
            switch (cmd) {
                .none => {},
                .quit => self.quit = true,
                .tick => |ns| {
                    self.pending_tick = self.context.elapsed + ns;
                    self.pending_tick_scheduled_at = self.context.elapsed;
                },
                .every => |ns| {
                    self.every_interval = ns;
                    self.last_every_tick = self.context.elapsed;
                },
                .batch, .sequence => |cmds| {
                    for (cmds) |c| try self.process(c);
                },
                .msg => |m| try self.send(m),
                .perform => |func| {
                    if (func()) |m| try self.send(m);
                },
                else => try self.effects.append(cmd),
            }
        }

        fn bindFrameAllocator(self: *Self) void {
            self.context.allocator = self.arena.allocator();
        }

        fn requireField(comptime name: []const u8, comptime method: []const u8) void {
            if (!@hasField(UserMsg, name)) {
                @compileError("Harness." ++ method ++ "() needs '" ++ @typeName(UserMsg) ++
                    "' to have a '" ++ name ++ "' field");
            }
        }
    };
}

pub fn stripAnsi(allocator: std.mem.Allocator, input: []const u8) ![]const u8 {
    var out = try std.array_list.Managed(u8).initCapacity(allocator, input.len);
    errdefer out.deinit();

    var i: usize = 0;
    while (i < input.len) {
        if (input[i] != 0x1b) {
            try out.append(input[i]);
            i += 1;
            continue;
        }

        i += 1;
        if (i >= input.len) break;

        switch (input[i]) {
            '[' => {
                i += 1;
                while (i < input.len) {
                    const b = input[i];
                    i += 1;
                    if (b >= 0x40 and b <= 0x7e) break;
                }
            },
            ']' => {
                i += 1;
                while (i < input.len) {
                    if (input[i] == 0x07) {
                        i += 1;
                        break;
                    }
                    if (input[i] == 0x1b and i + 1 < input.len and input[i + 1] == '\\') {
                        i += 2;
                        break;
                    }
                    i += 1;
                }
            },
            else => i += 1,
        }
    }

    return out.toOwnedSlice();
}

test "stripAnsi drops CSI and OSC sequences" {
    const allocator = std.testing.allocator;
    const out = try stripAnsi(allocator, "\x1b[1;31mred\x1b[0m \x1b]0;title\x07tail");
    defer allocator.free(out);
    try std.testing.expectEqualStrings("red tail", out);
}
