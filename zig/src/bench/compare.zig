const std = @import("std");
const compat = @import("compat");

const Report = struct {
    schema_version: u32,
    git_revision: []const u8,
    host_class: []const u8,
    target: []const u8,
    cpu_model: []const u8,
    cpu_features_hash: u64,
    zig_version: []const u8,
    optimize: []const u8,
    mode: []const u8,
    workload: []const u8,
    fixture_version: u32,
    workload_hash: u64,
    iterations: usize,
    samples: usize,
    completed_per_iteration: usize,
    bytes_per_iteration: usize,
    digest: u64,
    raw_window_ns: []const u64,
    ns_per_iteration: u64,
    window_avg_p50_ns: u64,
    window_avg_p95_ns: u64,
    window_avg_p99_ns: u64,
    allocation_count: ?usize,
    free_count: ?usize,
    allocated_bytes: ?usize,
    freed_bytes: ?usize,
    peak_live_bytes: ?usize,
    leak_bytes: ?usize,
};

const LatencyStats = struct { p50: u64, p95: u64, p99: u64 };

fn latencyStats(allocator: std.mem.Allocator, report: Report) !LatencyStats {
    var values = try allocator.alloc(u64, report.raw_window_ns.len);
    defer allocator.free(values);
    for (report.raw_window_ns, 0..) |elapsed, i| values[i] = elapsed / report.iterations + @intFromBool(elapsed % report.iterations != 0);
    std.mem.sort(u64, values, {}, std.sort.asc(u64));
    const at = struct {
        fn percentile(sorted: []const u64, numerator: usize) u64 {
            const rank = (sorted.len * numerator + 99) / 100;
            return sorted[@max(rank, 1) - 1];
        }
    }.percentile;
    return .{ .p50 = at(values, 50), .p95 = at(values, 95), .p99 = at(values, 99) };
}

fn validate(report: Report) !void {
    if (report.schema_version != 1 or report.git_revision.len == 0 or report.iterations == 0 or report.samples < 15 or report.completed_per_iteration == 0 or report.bytes_per_iteration == 0 or report.raw_window_ns.len != report.samples or report.ns_per_iteration == 0) return error.InvalidReport;
    const metrics = [_]?usize{ report.allocation_count, report.free_count, report.allocated_bytes, report.freed_bytes, report.peak_live_bytes, report.leak_bytes };
    if (std.mem.eql(u8, report.mode, "allocation")) {
        for (metrics) |metric| if (metric == null) return error.InvalidReport;
        if (report.leak_bytes.? != 0) return error.InvalidReport;
    } else if (std.mem.eql(u8, report.mode, "latency")) {
        for (metrics) |metric| if (metric != null) return error.InvalidReport;
    } else return error.InvalidReport;
}

fn expectCompatible(baseline: Report, candidate: Report) !void {
    try validate(baseline);
    try validate(candidate);
    if (baseline.schema_version != candidate.schema_version or
        baseline.fixture_version != candidate.fixture_version or
        baseline.workload_hash != candidate.workload_hash or
        baseline.iterations != candidate.iterations or
        baseline.samples != candidate.samples or
        baseline.completed_per_iteration != candidate.completed_per_iteration or
        baseline.bytes_per_iteration != candidate.bytes_per_iteration or
        !std.mem.eql(u8, baseline.host_class, candidate.host_class) or
        !std.mem.eql(u8, baseline.target, candidate.target) or
        !std.mem.eql(u8, baseline.cpu_model, candidate.cpu_model) or
        baseline.cpu_features_hash != candidate.cpu_features_hash or
        !std.mem.eql(u8, baseline.zig_version, candidate.zig_version) or
        !std.mem.eql(u8, baseline.optimize, candidate.optimize) or
        !std.mem.eql(u8, baseline.mode, candidate.mode) or
        !std.mem.eql(u8, baseline.workload, candidate.workload)) return error.IncompatibleReports;
    if ((baseline.allocated_bytes == null) != (candidate.allocated_bytes == null)) return error.IncompatibleReports;
    if (baseline.digest != candidate.digest) return error.SemanticMismatch;
}

fn workloadIndex(workload: []const u8) !usize {
    if (std.mem.eql(u8, workload, "sse_parse")) return 0;
    if (std.mem.eql(u8, workload, "transport_round_trip")) return 1;
    return error.InvalidReport;
}

fn compareLine(allocator: std.mem.Allocator, baseline_line: []const u8, candidate_line: []const u8) !usize {
    const baseline = try std.json.parseFromSlice(Report, allocator, baseline_line, .{ .ignore_unknown_fields = false });
    defer baseline.deinit();
    const candidate = try std.json.parseFromSlice(Report, allocator, candidate_line, .{ .ignore_unknown_fields = false });
    defer candidate.deinit();
    try expectCompatible(baseline.value, candidate.value);
    const baseline_latency = try latencyStats(allocator, baseline.value);
    const candidate_latency = try latencyStats(allocator, candidate.value);
    if (baseline_latency.p50 != baseline.value.window_avg_p50_ns or baseline_latency.p95 != baseline.value.window_avg_p95_ns or baseline_latency.p99 != baseline.value.window_avg_p99_ns or
        candidate_latency.p50 != candidate.value.window_avg_p50_ns or candidate_latency.p95 != candidate.value.window_avg_p95_ns or candidate_latency.p99 != candidate.value.window_avg_p99_ns) return error.InvalidReport;

    const p50_change = (@as(f64, @floatFromInt(candidate_latency.p50)) / @as(f64, @floatFromInt(baseline_latency.p50)) - 1) * 100;
    const p95_change = (@as(f64, @floatFromInt(candidate_latency.p95)) / @as(f64, @floatFromInt(baseline_latency.p95)) - 1) * 100;
    const p99_change = (@as(f64, @floatFromInt(candidate_latency.p99)) / @as(f64, @floatFromInt(baseline_latency.p99)) - 1) * 100;
    const metricChange = struct {
        fn format(a: std.mem.Allocator, base_value: ?usize, candidate_value: ?usize, work: usize) ![]u8 {
            const base = base_value orelse return a.dupe(u8, "unavailable");
            const next = candidate_value orelse return error.IncompatibleReports;
            if (base == 0) return a.dupe(u8, if (next == 0) "0.00%" else "introduced");
            const base_per_work = @as(f64, @floatFromInt(base)) / @as(f64, @floatFromInt(work));
            const next_per_work = @as(f64, @floatFromInt(next)) / @as(f64, @floatFromInt(work));
            return std.fmt.allocPrint(a, "{d:.2}%", .{(next_per_work / base_per_work - 1) * 100});
        }
    }.format;
    const allocation_count = try metricChange(allocator, baseline.value.allocation_count, candidate.value.allocation_count, baseline.value.completed_per_iteration);
    defer allocator.free(allocation_count);
    const free_count = try metricChange(allocator, baseline.value.free_count, candidate.value.free_count, baseline.value.completed_per_iteration);
    defer allocator.free(free_count);
    const allocated_bytes = try metricChange(allocator, baseline.value.allocated_bytes, candidate.value.allocated_bytes, baseline.value.completed_per_iteration);
    defer allocator.free(allocated_bytes);
    const freed_bytes = try metricChange(allocator, baseline.value.freed_bytes, candidate.value.freed_bytes, baseline.value.completed_per_iteration);
    defer allocator.free(freed_bytes);
    const peak_live_bytes = try metricChange(allocator, baseline.value.peak_live_bytes, candidate.value.peak_live_bytes, baseline.value.completed_per_iteration);
    defer allocator.free(peak_live_bytes);
    const output = try std.fmt.allocPrint(allocator, "{s}: window-average latency p50 {d:.2}%, p95 {d:.2}%, p99 {d:.2}%; allocations/work {s}, frees/work {s}, allocated bytes/work {s}, freed bytes/work {s}, peak live bytes/work {s}\n", .{ baseline.value.workload, p50_change, p95_change, p99_change, allocation_count, free_count, allocated_bytes, freed_bytes, peak_live_bytes });
    defer allocator.free(output);
    try std.Io.File.stdout().writeStreamingAll(std.Io.Threaded.global_single_threaded.io(), output);
    return workloadIndex(baseline.value.workload);
}

const ScalingRow = struct {
    n: u64,
    status: []const u8,
    peak_rss: ?u64,
    settled_rss: ?u64,
    threads: ?u64,
    fds: ?u64,
    written: ?u64,
    stored: ?u64,
    p50_ns: ?u64,
    p99_ns: ?u64,
    wall_ns: ?u64,
};

const PerAgent = struct {
    peak_rss: ?f64,
    settled_rss: ?f64,
    threads: ?f64,
    fds: ?f64,
    written: ?f64,
    stored: ?f64,
    p50_ns: ?f64,
};

const ScalingReport = struct {
    schema_version: u32,
    git_revision: []const u8,
    host_class: []const u8,
    target: []const u8,
    cpu_model: []const u8,
    mode: []const u8,
    workload: []const u8,
    fixture_version: u32,
    agent_counts: []const u64,
    history_turns: u32,
    rss_cap_bytes: u64,
    timeout_ms: u64,
    rows: []const ScalingRow,
    per_agent: PerAgent,
};

fn validateScaling(report: ScalingReport) !void {
    if (report.schema_version != 1 or report.git_revision.len == 0 or report.workload.len == 0 or !std.mem.eql(u8, report.mode, "scaling") or report.agent_counts.len == 0 or report.rows.len != report.agent_counts.len) return error.InvalidReport;
    if (!std.mem.eql(u8, report.rows[0].status, "ok")) return error.InvalidReport;
    for (report.rows, report.agent_counts) |row, count| {
        if (row.n != count) return error.InvalidReport;
        if (std.mem.startsWith(u8, row.status, "failed")) return error.InvalidReport;
        if (!std.mem.eql(u8, row.status, "ok")) continue;
        const metrics = [_]?u64{ row.peak_rss, row.settled_rss, row.threads, row.fds, row.written, row.stored, row.p50_ns, row.p99_ns, row.wall_ns };
        for (metrics) |metric| if (metric == null) return error.InvalidReport;
    }
}

fn expectScalingCompatible(baseline: ScalingReport, candidate: ScalingReport) !void {
    try validateScaling(baseline);
    try validateScaling(candidate);
    if (baseline.fixture_version != candidate.fixture_version or
        baseline.history_turns != candidate.history_turns or
        baseline.rss_cap_bytes != candidate.rss_cap_bytes or
        baseline.timeout_ms != candidate.timeout_ms or
        !std.mem.eql(u64, baseline.agent_counts, candidate.agent_counts) or
        !std.mem.eql(u8, baseline.host_class, candidate.host_class) or
        !std.mem.eql(u8, baseline.target, candidate.target) or
        !std.mem.eql(u8, baseline.cpu_model, candidate.cpu_model) or
        !std.mem.eql(u8, baseline.workload, candidate.workload)) return error.IncompatibleReports;
}

fn completedAgents(report: ScalingReport) u64 {
    var completed: u64 = 0;
    for (report.rows) |row| {
        if (std.mem.eql(u8, row.status, "ok")) completed = @max(completed, row.n);
    }
    return completed;
}

fn slopeChange(allocator: std.mem.Allocator, base_value: ?f64, candidate_value: ?f64, scale: f64, unit: []const u8) ![]u8 {
    const base = (base_value orelse return allocator.dupe(u8, "unavailable")) * scale;
    const next = (candidate_value orelse return allocator.dupe(u8, "unavailable")) * scale;
    if (@abs(base) < 0.005) return std.fmt.allocPrint(allocator, "{d:.2} -> {d:.2}{s}", .{ base, next, unit });
    return std.fmt.allocPrint(allocator, "{d:.2} -> {d:.2}{s} ({d:.2}%)", .{ base, next, unit, (next - base) / @abs(base) * 100 });
}

fn compareScalingLine(allocator: std.mem.Allocator, baseline_line: []const u8, candidate_line: []const u8) ![]u8 {
    const baseline = try std.json.parseFromSlice(ScalingReport, allocator, baseline_line, .{ .ignore_unknown_fields = false });
    defer baseline.deinit();
    const candidate = try std.json.parseFromSlice(ScalingReport, allocator, candidate_line, .{ .ignore_unknown_fields = false });
    defer candidate.deinit();
    try expectScalingCompatible(baseline.value, candidate.value);
    const base = baseline.value.per_agent;
    const next = candidate.value.per_agent;
    const settled = try slopeChange(allocator, base.settled_rss, next.settled_rss, 1.0 / 1024.0, " KiB");
    defer allocator.free(settled);
    const peak = try slopeChange(allocator, base.peak_rss, next.peak_rss, 1.0 / 1024.0, " KiB");
    defer allocator.free(peak);
    const threads = try slopeChange(allocator, base.threads, next.threads, 1, "");
    defer allocator.free(threads);
    const fds = try slopeChange(allocator, base.fds, next.fds, 1, "");
    defer allocator.free(fds);
    const written = try slopeChange(allocator, base.written, next.written, 1, " B");
    defer allocator.free(written);
    const stored = try slopeChange(allocator, base.stored, next.stored, 1, " B");
    defer allocator.free(stored);
    const p50 = try slopeChange(allocator, base.p50_ns, next.p50_ns, 1.0 / 1000.0, " us");
    defer allocator.free(p50);
    const base_completed = completedAgents(baseline.value);
    const next_completed = completedAgents(candidate.value);
    return std.fmt.allocPrint(allocator, "{s}: per agent settled RSS {s}, peak RSS {s}, threads {s}, FDs {s}, written {s}, stored {s}, step p50 {s}; completes N={d} -> N={d}{s}\n", .{ baseline.value.workload, settled, peak, threads, fds, written, stored, p50, base_completed, next_completed, if (next_completed < base_completed) " (stops earlier)" else "" });
}

fn compareScaling(allocator: std.mem.Allocator, baseline_data: []const u8, candidate_data: []const u8) !void {
    var seen: std.ArrayList([]u8) = .empty;
    defer {
        for (seen.items) |workload| allocator.free(workload);
        seen.deinit(allocator);
    }
    var baseline_lines = std.mem.tokenizeScalar(u8, baseline_data, '\n');
    var candidate_lines = std.mem.tokenizeScalar(u8, candidate_data, '\n');
    while (baseline_lines.next()) |baseline_line| {
        const candidate_line = candidate_lines.next() orelse return error.IncompatibleReports;
        const output = try compareScalingLine(allocator, baseline_line, candidate_line);
        defer allocator.free(output);
        const workload = output[0..std.mem.indexOfScalar(u8, output, ':').?];
        for (seen.items) |earlier| if (std.mem.eql(u8, earlier, workload)) return error.InvalidReport;
        const owned = try allocator.dupe(u8, workload);
        seen.append(allocator, owned) catch |err| {
            allocator.free(owned);
            return err;
        };
        try std.Io.File.stdout().writeStreamingAll(std.Io.Threaded.global_single_threaded.io(), output);
    }
    if (candidate_lines.next() != null) return error.IncompatibleReports;
    if (seen.items.len == 0) return error.InvalidReport;
}

fn isScaling(allocator: std.mem.Allocator, data: []const u8) !bool {
    var lines = std.mem.tokenizeScalar(u8, data, '\n');
    const first = lines.next() orelse return error.InvalidReport;
    const head = try std.json.parseFromSlice(struct { mode: []const u8 }, allocator, first, .{ .ignore_unknown_fields = true });
    defer head.deinit();
    return std.mem.eql(u8, head.value.mode, "scaling");
}

pub fn main(init: std.process.Init) !void {
    const allocator = init.gpa;
    const args = try init.minimal.args.toSlice(init.arena.allocator());
    if (args.len != 3) return error.Usage;
    const baseline_data = try compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), args[1], 1024 * 1024);
    defer allocator.free(baseline_data);
    const candidate_data = try compat.fs.readFileAlloc(allocator, compat.fs.getCwd(), args[2], 1024 * 1024);
    defer allocator.free(candidate_data);
    if (try isScaling(allocator, baseline_data)) return compareScaling(allocator, baseline_data, candidate_data);

    var baseline_lines = std.mem.tokenizeScalar(u8, baseline_data, '\n');
    var candidate_lines = std.mem.tokenizeScalar(u8, candidate_data, '\n');
    var seen = [_]bool{ false, false };
    while (baseline_lines.next()) |baseline_line| {
        const candidate_line = candidate_lines.next() orelse return error.IncompatibleReports;
        const workload_index = try compareLine(allocator, baseline_line, candidate_line);
        if (seen[workload_index]) return error.InvalidReport;
        seen[workload_index] = true;
    }
    if (candidate_lines.next() != null) return error.IncompatibleReports;
    if (!seen[0] or !seen[1]) return error.InvalidReport;
}

test "comparison rejects incompatible identity" {
    const samples = @as([15]u64, @splat(100));
    const baseline: Report = .{
        .schema_version = 1,
        .git_revision = "abc123",
        .host_class = "ci-arm64",
        .target = "aarch64-linux",
        .cpu_model = "generic",
        .cpu_features_hash = 7,
        .zig_version = "0.16.0",
        .optimize = "ReleaseFast",
        .mode = "allocation",
        .workload = "sse_parse",
        .fixture_version = 1,
        .workload_hash = 99,
        .iterations = 10,
        .samples = 15,
        .completed_per_iteration = 3,
        .bytes_per_iteration = 128,
        .digest = 42,
        .raw_window_ns = &samples,
        .ns_per_iteration = 10,
        .window_avg_p50_ns = 10,
        .window_avg_p95_ns = 10,
        .window_avg_p99_ns = 10,
        .allocation_count = 1,
        .free_count = 1,
        .allocated_bytes = 12,
        .freed_bytes = 12,
        .peak_live_bytes = 12,
        .leak_bytes = 0,
    };
    var candidate = baseline;
    try expectCompatible(baseline, candidate);
    candidate.target = "x86_64-linux";
    try std.testing.expectError(error.IncompatibleReports, expectCompatible(baseline, candidate));

    candidate = baseline;
    candidate.leak_bytes = 1;
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.leak_bytes = null;
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.mode = "latency";
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.schema_version = 2;
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.completed_per_iteration = 0;
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.bytes_per_iteration = 0;
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.bytes_per_iteration += 1;
    try std.testing.expectError(error.IncompatibleReports, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.samples = 14;
    try std.testing.expectError(error.InvalidReport, expectCompatible(baseline, candidate));
    candidate = baseline;
    candidate.workload_hash += 1;
    try std.testing.expectError(error.IncompatibleReports, expectCompatible(baseline, candidate));
}

test "scaling comparison reports slope changes and refuses another identity" {
    const line =
        \\{"schema_version":1,"git_revision":"abc","host_class":"ci","target":"x86_64-linux","cpu_model":"generic","mode":"scaling","workload":"go/history","fixture_version":1,"agent_counts":[10,100],"history_turns":3,"rss_cap_bytes":4096,"timeout_ms":120000,"rows":[{"n":10,"status":"ok","peak_rss":2048,"settled_rss":1024,"threads":3,"fds":4,"written":0,"stored":10,"p50_ns":1000,"p99_ns":2000,"wall_ns":5},{"n":100,"status":"ok","peak_rss":4096,"settled_rss":2048,"threads":3,"fds":4,"written":0,"stored":100,"p50_ns":1000,"p99_ns":2000,"wall_ns":50}],"per_agent":{"peak_rss":2048,"settled_rss":1024,"threads":0,"fds":0,"written":null,"stored":1,"p50_ns":0}}
    ;
    const slower =
        \\{"schema_version":1,"git_revision":"def","host_class":"ci","target":"x86_64-linux","cpu_model":"generic","mode":"scaling","workload":"go/history","fixture_version":1,"agent_counts":[10,100],"history_turns":3,"rss_cap_bytes":4096,"timeout_ms":120000,"rows":[{"n":10,"status":"ok","peak_rss":2048,"settled_rss":1024,"threads":3,"fds":4,"written":0,"stored":10,"p50_ns":1000,"p99_ns":2000,"wall_ns":5},{"n":100,"status":"stopped","peak_rss":null,"settled_rss":null,"threads":null,"fds":null,"written":null,"stored":null,"p50_ns":null,"p99_ns":null,"wall_ns":null}],"per_agent":{"peak_rss":null,"settled_rss":3072,"threads":1,"fds":0,"written":null,"stored":1,"p50_ns":0}}
    ;
    try std.testing.expect(try isScaling(std.testing.allocator, line));
    const output = try compareScalingLine(std.testing.allocator, line, slower);
    defer std.testing.allocator.free(output);
    try std.testing.expectEqualStrings("go/history: per agent settled RSS 1.00 -> 3.00 KiB (200.00%), peak RSS unavailable, threads 0.00 -> 1.00, FDs 0.00 -> 0.00, written unavailable, stored 1.00 -> 1.00 B (0.00%), step p50 0.00 -> 0.00 us; completes N=100 -> N=10 (stops earlier)\n", output);
    const elsewhere = try std.mem.replaceOwned(u8, std.testing.allocator, slower, "\"host_class\":\"ci\"", "\"host_class\":\"laptop\"");
    defer std.testing.allocator.free(elsewhere);
    try std.testing.expectError(error.IncompatibleReports, compareScalingLine(std.testing.allocator, line, elsewhere));
    const longer = try std.mem.replaceOwned(u8, std.testing.allocator, slower, "\"timeout_ms\":120000", "\"timeout_ms\":600000");
    defer std.testing.allocator.free(longer);
    try std.testing.expectError(error.IncompatibleReports, compareScalingLine(std.testing.allocator, line, longer));
    const missing = try std.mem.replaceOwned(u8, std.testing.allocator, line, "\"fds\":4,\"written\":0,\"stored\":100", "\"fds\":4,\"written\":null,\"stored\":100");
    defer std.testing.allocator.free(missing);
    try std.testing.expectError(error.InvalidReport, compareScalingLine(std.testing.allocator, missing, line));
    const broken = try std.mem.replaceOwned(u8, std.testing.allocator, slower, "\"status\":\"stopped\"", "\"status\":\"failed at step 3: hub exited\"");
    defer std.testing.allocator.free(broken);
    try std.testing.expectError(error.InvalidReport, compareScalingLine(std.testing.allocator, line, broken));
    const first = try std.mem.replaceOwned(u8, std.testing.allocator, line, "{\"n\":10,\"status\":\"ok\"", "{\"n\":10,\"status\":\"timeout after 120s at step 4\"");
    defer std.testing.allocator.free(first);
    try std.testing.expectError(error.InvalidReport, compareScalingLine(std.testing.allocator, first, line));
}

test "completed agents is the largest N that finished, whatever the row order" {
    const row = struct {
        fn make(n: u64, status: []const u8) ScalingRow {
            return .{ .n = n, .status = status, .peak_rss = 1, .settled_rss = 1, .threads = 1, .fds = 1, .written = 1, .stored = 1, .p50_ns = 1, .p99_ns = 1, .wall_ns = 1 };
        }
    }.make;
    const rows = [_]ScalingRow{ row(1000, "ok"), row(10, "ok"), row(100, "ok"), row(10000, "stopped") };
    const report: ScalingReport = .{ .schema_version = 1, .git_revision = "abc", .host_class = "ci", .target = "x86_64-linux", .cpu_model = "generic", .mode = "scaling", .workload = "go/empty", .fixture_version = 1, .agent_counts = &.{ 1000, 10, 100, 10000 }, .history_turns = 3, .rss_cap_bytes = 1, .timeout_ms = 1, .rows = &rows, .per_agent = .{ .peak_rss = null, .settled_rss = null, .threads = null, .fds = null, .written = null, .stored = null, .p50_ns = null } };
    try std.testing.expectEqual(@as(u64, 1000), completedAgents(report));
}
