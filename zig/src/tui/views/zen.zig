const std = @import("std");
const zz = @import("zigzag");
const tui_theme = @import("tui_theme");
const tui_text = @import("tui_text");

pub const particle_count: usize = 700;

const disc: f32 = 50;
const squash: f32 = 0.86;
const slow: f32 = 0.55;
const life: f32 = 300;
const prewarm_steps: usize = 840;
const prewarm_dt: f32 = 0.5;
const density_gain: f32 = 2.4;
const default_half_width: f32 = 200;

const base = Rgb{ .r = 22, .g = 22, .b = 30 };
const haze = Rgb{ .r = 92, .g = 95, .b = 100 };
const yang_color = Rgb{ .r = 236, .g = 236, .b = 236 };
const yin_color = Rgb{ .r = 4, .g = 5, .b = 7 };
const ring_color = Rgb{ .r = 210, .g = 210, .b = 210 };

const Rgb = struct {
    r: f32,
    g: f32,
    b: f32,

    fn mix(self: Rgb, other: Rgb, amount: f32) Rgb {
        return .{ .r = self.r + (other.r - self.r) * amount, .g = self.g + (other.g - self.g) * amount, .b = self.b + (other.b - self.b) * amount };
    }

    fn distance(self: Rgb, other: Rgb) f32 {
        return @abs(self.r - other.r) + @abs(self.g - other.g) + @abs(self.b - other.b);
    }

    fn color(self: Rgb) zz.Color {
        return zz.Color.fromRgb(channel(self.r), channel(self.g), channel(self.b));
    }

    fn same(self: Rgb, other: Rgb) bool {
        return channel(self.r) == channel(other.r) and channel(self.g) == channel(other.g) and channel(self.b) == channel(other.b);
    }
};

fn channel(value: f32) u8 {
    return @intFromFloat(std.math.clamp(@round(value), 0, 255));
}

const Particle = struct {
    yang: bool = true,
    line: bool = false,
    x: f32 = 0,
    y: f32 = 0,
    vx: f32 = 0,
    vy: f32 = 0,
    r: f32 = 0,
    age: f32 = 0,
    delay: f32 = 0,
    phase: f32 = 0,
    size: f32 = 0,
    pull: f32 = 0,
    settle: f32 = 0,
};

pub const Flow = struct {
    particles: [particle_count]Particle = [_]Particle{.{}} ** particle_count,
    time: f32 = 0,
    seed: u64 = 0x9e3779b97f4a7c15,
    half_width: f32 = default_half_width,

    pub fn init() Flow {
        var flow: Flow = .{};
        for (&flow.particles, 0..) |*p, index| {
            flow.spawn(p, index % 2 == 0);
            p.delay = flow.unit() * life;
        }
        for (0..prewarm_steps) |_| flow.step(prewarm_dt);
        return flow;
    }

    pub fn step(self: *Flow, dt: f32) void {
        self.time += dt;
        for (&self.particles) |*p| self.advance(p, dt);
    }

    fn unit(self: *Flow) f32 {
        self.seed +%= 0x9e3779b97f4a7c15;
        var z = self.seed;
        z = (z ^ (z >> 30)) *% 0xbf58476d1ce4e5b9;
        z = (z ^ (z >> 27)) *% 0x94d049bb133111eb;
        z ^= z >> 31;
        return @as(f32, @floatFromInt(z >> 40)) / @as(f32, @floatFromInt(@as(u64, 1) << 24));
    }

    fn spread(self: *Flow) f32 {
        var sum: f32 = 0;
        for (0..4) |_| sum += self.unit();
        return (sum - 2) / 1.15;
    }

    fn spawn(self: *Flow, p: *Particle, yang: bool) void {
        const side: f32 = if (yang) -1 else 1;
        p.yang = yang;
        p.x = side * (self.half_width + 30 + self.unit() * 90);
        p.y = side * 8 + self.spread() * 11;
        p.age = 0;
        p.phase = self.unit() * std.math.tau;
        p.size = 7 + self.unit() * 10;
        p.pull = 0.75 + self.unit() * 0.5;
        p.settle = disc * @sqrt(0.04 + 0.96 * self.unit());
        p.line = self.unit() < 0.22;
        p.vx = 0;
        p.vy = 0;
        p.r = @abs(p.x);
    }

    fn advance(self: *Flow, p: *Particle, dt: f32) void {
        if (p.delay > 0) {
            p.delay -= dt;
            return;
        }
        const t = self.time;
        const dx = p.x;
        const dy = p.y / squash;
        const r = @sqrt(dx * dx + dy * dy) + 0.001;
        const settling = std.math.clamp((r - p.settle) / 26, 0, 1);
        const vr = -(0.35 + 3.2 * @min(1, r / 170)) * p.pull * settling * slow;
        const reach: f32 = if (r < disc * 1.6) 1 else @max(0.15, 1 - (r - disc * 1.6) / 120);
        const vt = (0.6 + 300 / (r + 22)) * slow * reach;
        var vx = dx / r * vr - dy / r * vt;
        var vy = (dy / r * vr + dx / r * vt) * squash;
        const far = std.math.clamp((r - disc) / 90, 0, 1);
        vx += far * (if (p.yang) @as(f32, 2.4) else -2.4) * slow;
        vx += 1.2 * slow * @sin(0.024 * p.y + t * 0.03 + p.phase);
        vy += 1.0 * slow * @cos(0.02 * p.x - t * 0.025 + p.phase);
        p.vx = vx;
        p.vy = vy;
        p.x += vx * dt;
        p.y += vy * dt;
        p.age += dt;
        p.r = r;
        if (p.age > life) self.spawn(p, p.yang);
    }
};

pub const Counts = struct {
    thinking: usize = 0,
    tools: usize = 0,
    messages: usize = 0,
};

pub const Frame = struct {
    width: usize,
    height: usize,
    counts: Counts = .{},
    running: bool = false,
    elapsed_ms: u64 = 0,
    activity: []const u8 = "",
    final_block: []const u8 = "",
    failed: bool = false,
};

const min_canvas_rows: usize = 6;
const max_canvas_rows: usize = 20;

pub fn render(allocator: std.mem.Allocator, flow: *Flow, frame: Frame) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    const writer = &out.writer;
    const width = @max(frame.width, 20);
    const counts = try countsRow(allocator, frame.counts, width);
    defer allocator.free(counts);

    if (!frame.running and frame.final_block.len > 0) {
        const badge = if (frame.failed) tui_theme.glyph.err ++ " stopped" else tui_theme.glyph.check ++ " done";
        try writeCentered(writer, allocator, try styled(allocator, if (frame.failed) tui_theme.palette.danger else tui_theme.palette.success, badge), width);
        try writer.writeAll("\n\n");
        try writer.writeAll(frame.final_block);
        try writer.writeAll("\n\n");
        try writer.writeAll(counts);
        return out.toOwnedSlice();
    }

    const rows = std.math.clamp(frame.height -| 4, min_canvas_rows, max_canvas_rows);
    try canvas(allocator, writer, flow, width, rows);
    try writer.writeAll("\n\n");
    try writer.writeAll(counts);
    try writer.writeByte('\n');
    const line = if (frame.running) try activityLine(allocator, frame, width) else try allocator.dupe(u8, "zen · send a prompt; only the final reply is shown");
    defer allocator.free(line);
    try writeCentered(writer, allocator, try styled(allocator, tui_theme.palette.dim, line), width);
    return out.toOwnedSlice();
}

fn activityLine(allocator: std.mem.Allocator, frame: Frame, width: usize) ![]u8 {
    const seconds = frame.elapsed_ms / 1000;
    const clock = try std.fmt.allocPrint(allocator, "{d}:{d:0>2}", .{ seconds / 60, seconds % 60 });
    defer allocator.free(clock);
    if (frame.activity.len == 0) return std.fmt.allocPrint(allocator, "zen · {s}", .{clock});
    const budget = width -| (clock.len + 12);
    const fitted = try tui_text.truncateLineToWidth(allocator, frame.activity, budget);
    defer allocator.free(fitted);
    return std.fmt.allocPrint(allocator, "zen · {s} · {s}", .{ clock, fitted });
}

fn countsRow(allocator: std.mem.Allocator, counts: Counts, width: usize) ![]u8 {
    const thinking = try std.fmt.allocPrint(allocator, "{s} {d} thinking", .{ tui_theme.glyph.thinking, counts.thinking });
    defer allocator.free(thinking);
    const tools = try std.fmt.allocPrint(allocator, "{s} {d} tool{s}", .{ tui_theme.glyph.tool, counts.tools, if (counts.tools == 1) "" else "s" });
    defer allocator.free(tools);
    const messages = try std.fmt.allocPrint(allocator, "{s} {d} message{s}", .{ tui_theme.glyph.assistant, counts.messages, if (counts.messages == 1) "" else "s" });
    defer allocator.free(messages);
    const a = try styled(allocator, tui_theme.palette.thinking, thinking);
    defer allocator.free(a);
    const b = try styled(allocator, tui_theme.palette.tool_shell, tools);
    defer allocator.free(b);
    const c = try styled(allocator, tui_theme.palette.assistant, messages);
    defer allocator.free(c);
    const row = try std.fmt.allocPrint(allocator, "{s}   {s}   {s}", .{ a, b, c });
    defer allocator.free(row);
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try writeCentered(&out.writer, allocator, try allocator.dupe(u8, row), width);
    return out.toOwnedSlice();
}

fn styled(allocator: std.mem.Allocator, color: zz.Color, text: []const u8) ![]u8 {
    var out: std.Io.Writer.Allocating = .init(allocator);
    errdefer out.deinit();
    try color.writeFg(&out.writer);
    try out.writer.writeAll(text);
    try out.writer.writeAll("\x1b[0m");
    return out.toOwnedSlice();
}

fn writeCentered(writer: *std.Io.Writer, allocator: std.mem.Allocator, owned: []u8, width: usize) !void {
    defer allocator.free(owned);
    const visible = tui_text.visibleWidth(owned);
    for (0..(width -| visible) / 2) |_| try writer.writeByte(' ');
    try writer.writeAll(owned);
}

fn canvas(allocator: std.mem.Allocator, writer: *std.Io.Writer, flow: *Flow, cols: usize, rows: usize) !void {
    const w = cols;
    const h = rows * 2;
    const white = try allocator.alloc(f32, w * h);
    defer allocator.free(white);
    const black = try allocator.alloc(f32, w * h);
    defer allocator.free(black);
    @memset(white, 0);
    @memset(black, 0);

    const fw: f32 = @floatFromInt(w);
    const fh: f32 = @floatFromInt(h);
    const scale = 0.2 * fh / disc;
    flow.half_width = fw / 2 / scale;
    const ox = fw / 2;
    const oy = fh / 2;

    for (&flow.particles) |*p| {
        if (p.delay > 0) continue;
        const fade_in = @min(1, p.age / 10);
        const fade_out = @min(1, (life - p.age) / 25);
        const px = ox + p.x * scale;
        const py = oy + p.y * scale;
        const target = if (p.yang) white else black;
        if (p.line) {
            const speed = @sqrt(p.vx * p.vx + p.vy * p.vy) + 0.001;
            const length = (10 + p.size * 1.6) * (0.5 + 0.5 * @min(1, p.r / 80)) * scale;
            const alpha = fade_in * fade_out * (if (p.yang) @as(f32, 0.22) else 0.5) * density_gain;
            for (0..6) |i| {
                const along = @as(f32, @floatFromInt(i)) / 5 * length;
                splat(target, w, h, px - p.vx / speed * along, py - p.vy / speed * along, 0.45, alpha * 0.6);
            }
        } else {
            const in_disc: f32 = if (p.r < disc * 1.15) 1.5 else 1;
            const alpha = fade_in * fade_out * (if (p.yang) @as(f32, 0.085) else 0.2) * in_disc * density_gain;
            const size = p.size * (0.4 + 0.6 * @min(1, p.r / 110)) * (1 + 0.35 * @min(1, p.age / 30));
            splat(target, w, h, px, py, @max(0.5, size * scale * 0.45), alpha);
        }
    }

    const ring_radius = (disc + 4) * scale;
    const haze_radius = 3.6 * disc * scale;
    for (0..rows) |row| {
        var last_fg: ?Rgb = null;
        var last_bg: ?Rgb = null;
        var colored = false;
        for (0..cols) |col| {
            const top = pixel(white, black, w, col, row * 2, ox, oy, fh, ring_radius, haze_radius);
            const bottom = pixel(white, black, w, col, row * 2 + 1, ox, oy, fh, ring_radius, haze_radius);
            if (top == null and bottom == null) {
                if (colored) try writer.writeAll("\x1b[0m");
                colored = false;
                last_fg = null;
                last_bg = null;
                try writer.writeByte(' ');
                continue;
            }
            const fg = top orelse bottom.?;
            const bg: ?Rgb = if (top != null) bottom else null;
            if (bg == null and last_bg != null) {
                try writer.writeAll("\x1b[0m");
                last_fg = null;
                last_bg = null;
            }
            if (last_fg == null or !last_fg.?.same(fg)) try fg.color().writeFg(writer);
            if (bg) |value| {
                if (last_bg == null or !last_bg.?.same(value)) try value.color().writeBg(writer);
            }
            last_fg = fg;
            last_bg = bg;
            colored = true;
            try writer.writeAll(if (top != null) "\u{2580}" else "\u{2584}");
        }
        if (colored) try writer.writeAll("\x1b[0m");
        if (row + 1 < rows) try writer.writeByte('\n');
    }
}

fn splat(buffer: []f32, w: usize, h: usize, x: f32, y: f32, sigma: f32, alpha: f32) void {
    if (alpha <= 0.0005) return;
    const reach = sigma * 2.2;
    const fw: f32 = @floatFromInt(w);
    const fh: f32 = @floatFromInt(h);
    if (x + reach < 0 or y + reach < 0 or x - reach >= fw or y - reach >= fh) return;
    const x0: usize = @intFromFloat(@max(0, @floor(x - reach)));
    const y0: usize = @intFromFloat(@max(0, @floor(y - reach)));
    const x1: usize = @intFromFloat(@min(fw - 1, @ceil(x + reach)));
    const y1: usize = @intFromFloat(@min(fh - 1, @ceil(y + reach)));
    const inv = 1 / (2 * sigma * sigma);
    var yy = y0;
    while (yy <= y1) : (yy += 1) {
        const dy = @as(f32, @floatFromInt(yy)) + 0.5 - y;
        var xx = x0;
        while (xx <= x1) : (xx += 1) {
            const dx = @as(f32, @floatFromInt(xx)) + 0.5 - x;
            buffer[yy * w + xx] += alpha * @exp(-(dx * dx + dy * dy) * inv);
        }
    }
}

fn pixel(white: []const f32, black: []const f32, w: usize, col: usize, row: usize, ox: f32, oy: f32, fh: f32, ring_radius: f32, haze_radius: f32) ?Rgb {
    const x = @as(f32, @floatFromInt(col)) + 0.5 - ox;
    const y = @as(f32, @floatFromInt(row)) + 0.5 - oy;
    const d = @sqrt(x * x + y * y) / haze_radius;
    var c = base;
    if (d < 1) c = c.mix(haze, 0.5 * std.math.pow(f32, 1 - d, 1.5));
    const ellipse = @sqrt(x * x + (y / squash) * (y / squash));
    if (@abs(ellipse - ring_radius) < 0.6) c = c.mix(ring_color, 0.16);
    c = c.mix(yin_color, 1 - @exp(-black[row * w + col]));
    c = c.mix(yang_color, 1 - @exp(-white[row * w + col]));
    const edge = @as(f32, @floatFromInt(row)) + 0.5;
    const band = fh * 0.22;
    const mask = std.math.clamp(@min(edge, fh - edge) / band, 0, 1);
    c = base.mix(c, mask);
    if (c.distance(base) < 6) return null;
    return c;
}

test "a fresh flow has drawn both streams into the disc" {
    const flow = Flow.init();
    var white_in: usize = 0;
    var black_in: usize = 0;
    for (flow.particles) |p| {
        if (p.delay > 0 or p.r >= disc * 1.15) continue;
        if (p.yang) white_in += 1 else black_in += 1;
    }
    try std.testing.expect(white_in > 20);
    try std.testing.expect(black_in > 20);
}

test "the flow streams in from both sides beyond the canvas" {
    const flow = Flow.init();
    var left: usize = 0;
    var right: usize = 0;
    for (flow.particles) |p| {
        if (p.delay > 0 or p.r < disc * 3) continue;
        if (p.yang and p.x < 0) left += 1;
        if (!p.yang and p.x > 0) right += 1;
    }
    try std.testing.expect(left > 10);
    try std.testing.expect(right > 10);
}

test "a running frame fits its width and shows the counts" {
    var flow = Flow.init();
    const text = try render(std.testing.allocator, &flow, .{ .width = 80, .height = 20, .running = true, .elapsed_ms = 75_000, .counts = .{ .thinking = 3, .tools = 1, .messages = 0 }, .activity = "Shell Execute  go test ./..." });
    defer std.testing.allocator.free(text);
    var lines = std.mem.splitScalar(u8, text, '\n');
    var count: usize = 0;
    while (lines.next()) |line| : (count += 1) try std.testing.expect(tui_text.visibleWidth(line) <= 80);
    try std.testing.expectEqual(@as(usize, 16 + 3), count);
    try std.testing.expect(std.mem.indexOf(u8, text, "3 thinking") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "1 tool") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "1 tools") == null);
    try std.testing.expect(std.mem.indexOf(u8, text, "1:15") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "go test") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "\u{2580}") != null);
}

test "a stopped frame shows the final block instead of the flow" {
    var flow = Flow.init();
    const text = try render(std.testing.allocator, &flow, .{ .width = 60, .height = 20, .final_block = "the final reply", .counts = .{ .messages = 1 } });
    defer std.testing.allocator.free(text);
    try std.testing.expect(std.mem.indexOf(u8, text, "the final reply") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "done") != null);
    try std.testing.expect(std.mem.indexOf(u8, text, "\u{2580}") == null);

    const failed = try render(std.testing.allocator, &flow, .{ .width = 60, .height = 20, .final_block = "boom", .failed = true });
    defer std.testing.allocator.free(failed);
    try std.testing.expect(std.mem.indexOf(u8, failed, "stopped") != null);
}
