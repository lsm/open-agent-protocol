const std = @import("std");

const replay_id = "conformance-replay-1";
const oap_types = @import("types");
const oap_envelope = @import("envelope");
const endpoint_client = @import("endpoint_client");
const json_writer = @import("json_writer");


pub const default_exit_grace_ms: i64 = 30_000;
pub const default_probe_budget_ms: i64 = 30_000;

pub const Check = struct {
    name: []const u8,
    passed: bool = true,
    detail: []const u8 = "",
    skipped: bool = false,
};

pub const Report = struct {
    allocator: std.mem.Allocator,
    endpoint: []const u8,
    checks: std.ArrayList(Check) = .empty,

    pub fn deinit(self: *Report) void {
        for (self.checks.items) |check| {
            self.allocator.free(check.name);
            self.allocator.free(check.detail);
        }
        self.checks.deinit(self.allocator);
        self.allocator.free(self.endpoint);
    }

    pub fn passed(self: *const Report) bool {
        for (self.checks.items) |check| {
            if (!check.passed) return false;
        }
        return true;
    }

    pub fn verdict(self: *const Report, name: []const u8) ?Check {
        for (self.checks.items) |check| {
            if (std.mem.eql(u8, check.name, name)) return check;
        }
        return null;
    }
};

pub const Options = struct {
    command: []const u8,
    args: []const []const u8 = &.{},
    environment: []const []const u8 = &.{},
    session_id: []const u8 = "conformance",
    probe_budget_ms: i64 = default_probe_budget_ms,
    exit_grace_ms: i64 = default_exit_grace_ms,
};

pub fn run(allocator: std.mem.Allocator, options: Options) !Report {
    if (options.command.len == 0) return error.ConformanceNeedsCommand;
    const invocation = try std.mem.join(allocator, " ", options.args);
    defer allocator.free(invocation);
    var runner = Runner{
        .allocator = allocator,
        .session = options.session_id,
        .report = .{
            .allocator = allocator,
            .endpoint = try std.fmt.allocPrint(allocator, "{s} {s}", .{ options.command, invocation }),
        },
    };
    errdefer runner.report.deinit();
    runner.client = try endpoint_client.Client.spawn(allocator, .{
        .command = options.command,
        .args = options.args,
        .environment = options.environment,
    });
    defer runner.client.deinit();
    defer runner.releaseEnvelopes();

    try runner.drive(options.probe_budget_ms);

    runner.client.closeStdin();
    try runner.drain(runner.probeDeadline());
    const code = runner.client.waitExit(options.exit_grace_ms) catch |err| blk: {
        if (err == endpoint_client.Error.ExitGraceElapsed) {
            try runner.fail(
                "endpoint exits after stdin EOF",
                "the endpoint was still running once the exit grace elapsed, so it was killed",
            );
        } else {
            try runner.fail("endpoint exits 0 after stdin EOF", @errorName(err));
        }
        break :blk null;
    };
    if (code) |status| {
        if (status == 0) {
            try runner.pass("endpoint exits 0 after stdin EOF");
        } else {
            try runner.failOwned(
                "endpoint exits 0 after stdin EOF",
                try std.fmt.allocPrint(allocator, "exit code {d}", .{status}),
            );
        }
    }
    return runner.report;
}

fn lineBudget(line_deadline_ms: i64) std.Io.Timeout {
    return .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(line_deadline_ms), .clock = .boot } };
}

const Deadline = struct {
    io: std.Io,
    started: std.Io.Timestamp,
    budget_ms: i64,

    fn start(io: std.Io, budget_ms: i64) Deadline {
        return .{ .io = io, .started = std.Io.Timestamp.now(io, .awake), .budget_ms = budget_ms };
    }

    fn remainingMs(self: Deadline) i64 {
        const spent = self.started.durationTo(std.Io.Timestamp.now(self.io, .awake));
        return self.budget_ms - @as(i64, @intCast(@divTrunc(spent.toNanoseconds(), std.time.ns_per_ms)));
    }

    fn expired(self: Deadline) bool {
        return self.remainingMs() <= 0;
    }

    fn timeout(self: Deadline) std.Io.Timeout {
        return lineBudget(@max(self.remainingMs(), 1));
    }

    fn budget(self: Deadline) endpoint_client.Budget {
        return .{ .io = self.io, .started = self.started, .budget_ms = self.budget_ms };
    }
};

const ControlFrame = struct {
    control: []const u8,
    id: []const u8 = "",
    session_id: []const u8 = "",
    run_id: []const u8 = "",
    after: ?u64 = null,
    requested_after: u64 = 0,
    oldest_available: u64 = 0,
    latest_available: u64 = 0,
    code: []const u8 = "",
    message: []const u8 = "",

    fn deinit(self: *ControlFrame, allocator: std.mem.Allocator) void {
        allocator.free(self.control);
        if (self.id.len != 0) allocator.free(self.id);
        if (self.session_id.len != 0) allocator.free(self.session_id);
        if (self.run_id.len != 0) allocator.free(self.run_id);
        if (self.code.len != 0) allocator.free(self.code);
        if (self.message.len != 0) allocator.free(self.message);
    }
};

const ControlField = struct { key: []const u8, string: bool };

const control_fields = [_]ControlField{
    .{ .key = "control", .string = true },
    .{ .key = "id", .string = true },
    .{ .key = "session_id", .string = true },
    .{ .key = "run_id", .string = true },
    .{ .key = "after", .string = false },
    .{ .key = "requested_after", .string = false },
    .{ .key = "oldest_available", .string = false },
    .{ .key = "latest_available", .string = false },
    .{ .key = "code", .string = true },
    .{ .key = "message", .string = true },
};

fn parseControl(allocator: std.mem.Allocator, line: []const u8) !ControlFrame {
    var parsed = std.json.parseFromSlice(std.json.Value, allocator, line, .{}) catch return error.InvalidControlFrame;
    defer parsed.deinit();
    const root = switch (parsed.value) {
        .object => |entries| entries,
        else => return error.InvalidControlFrame,
    };
    var frame = ControlFrame{ .control = "" };
    errdefer frame.deinit(allocator);
    for (control_fields) |field| {
        const found = root.get(field.key) orelse continue;
        if (field.string) {
            const text = switch (found) {
                .string => |text| text,
                else => continue,
            };
            const owned = try allocator.dupe(u8, text);
            if (std.mem.eql(u8, field.key, "control")) {
                allocator.free(frame.control);
                frame.control = owned;
            } else if (std.mem.eql(u8, field.key, "id")) {
                allocator.free(frame.id);
                frame.id = owned;
            } else if (std.mem.eql(u8, field.key, "session_id")) {
                allocator.free(frame.session_id);
                frame.session_id = owned;
            } else if (std.mem.eql(u8, field.key, "run_id")) {
                allocator.free(frame.run_id);
                frame.run_id = owned;
            } else if (std.mem.eql(u8, field.key, "code")) {
                allocator.free(frame.code);
                frame.code = owned;
            } else {
                allocator.free(frame.message);
                frame.message = owned;
            }
            continue;
        }
        const number = switch (found) {
            .integer => |number| if (number < 0) return error.InvalidControlFrame else @as(u64, @intCast(number)),
            .float => return error.InvalidControlFrame,
            .number_string => return error.InvalidControlFrame,
            else => return error.InvalidControlFrame,
        };
        if (std.mem.eql(u8, field.key, "after")) {
            frame.after = number;
        } else if (std.mem.eql(u8, field.key, "requested_after")) {
            frame.requested_after = number;
        } else if (std.mem.eql(u8, field.key, "oldest_available")) {
            frame.oldest_available = number;
        } else {
            frame.latest_available = number;
        }
    }
    return frame;
}

const Runner = struct {
    allocator: std.mem.Allocator,
    client: endpoint_client.Client = undefined,
    report: Report,
    responses: std.ArrayList(oap_types.Envelope) = .empty,
    events: std.ArrayList(oap_types.Envelope) = .empty,
    controls: std.ArrayList(ControlFrame) = .empty,
    run_events: std.ArrayList([]const u8) = .empty,
    session: []const u8,
    probe_budget_ms: i64 = default_probe_budget_ms,
    revision: []const u8 = "",
    run_id: []const u8 = "",
    refusal: []const u8 = "",
    cancel_level: []const u8 = "",
    ids: usize = 0,

    fn releaseEnvelopes(self: *Runner) void {
        for (self.responses.items) |*envelope| envelope.deinit(self.allocator);
        for (self.events.items) |*envelope| envelope.deinit(self.allocator);
        for (self.controls.items) |*frame| frame.deinit(self.allocator);
        self.controls.deinit(self.allocator);
        for (self.run_events.items) |id| self.allocator.free(id);
        self.run_events.deinit(self.allocator);
        self.responses.deinit(self.allocator);
        self.events.deinit(self.allocator);
        if (self.refusal.len != 0) self.allocator.free(self.refusal);
        if (self.cancel_level.len != 0) self.allocator.free(self.cancel_level);
    }

    fn nextId(self: *Runner, kind: []const u8) ![]const u8 {
        self.ids += 1;
        return std.fmt.allocPrint(self.allocator, "conformance-{s}-{d}", .{ kind, self.ids });
    }

    fn pass(self: *Runner, name: []const u8) !void {
        const owned = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned);
        try self.add(.{ .name = owned });
    }

    fn fail(self: *Runner, name: []const u8, detail: []const u8) !void {
        const owned_name = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned_name);
        const owned_detail = try self.allocator.dupe(u8, detail);
        errdefer self.allocator.free(owned_detail);
        try self.add(.{
            .name = owned_name,
            .passed = false,
            .detail = owned_detail,
        });
    }

    fn passOwned(self: *Runner, name: []const u8, detail: []const u8) !void {
        errdefer self.allocator.free(detail);
        const owned_name = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned_name);
        try self.add(.{ .name = owned_name, .detail = detail });
    }

    fn skip(self: *Runner, name: []const u8, detail: []const u8) !void {
        const owned_name = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned_name);
        const owned_detail = try self.allocator.dupe(u8, detail);
        errdefer self.allocator.free(owned_detail);
        try self.add(.{
            .name = owned_name,
            .detail = owned_detail,
            .skipped = true,
        });
    }

    fn failOwned(self: *Runner, name: []const u8, detail: []const u8) !void {
        errdefer self.allocator.free(detail);
        const owned_name = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned_name);
        try self.add(.{ .name = owned_name, .passed = false, .detail = detail });
    }

    fn failReason(self: *Runner, name: []const u8, err: anyerror) !void {
        try self.fail(name, self.reasonOf(err));
    }

    fn add(self: *Runner, check: Check) !void {
        try self.report.checks.append(self.allocator, check);
    }


    fn pullUntil(self: *Runner, deadline: Deadline) !void {
        if (deadline.expired()) return error.ConformanceEndpointSilent;
        const frame = try self.client.next(deadline.budget()) orelse return error.ConformanceEndpointSilent;
        if (frame == .control) {
            const control = try parseControl(self.allocator, frame.control);
            self.controls.append(self.allocator, control) catch |err| {
                var released = control;
                released.deinit(self.allocator);
                return err;
            };
            return;
        }
        var envelope = try oap_envelope.deserializeEnvelope(frame.envelope, self.allocator);
        if (envelope.in_reply_to != null) {
            self.responses.append(self.allocator, envelope) catch |err| {
                envelope.deinit(self.allocator);
                return err;
            };
            return;
        }
        self.events.append(self.allocator, envelope) catch |err| {
            envelope.deinit(self.allocator);
            return err;
        };
    }

    fn takeAnswer(self: *Runner, id: []const u8) ?oap_types.Envelope {
        for (self.responses.items, 0..) |*candidate, index| {
            const reply = candidate.in_reply_to orelse continue;
            if (std.mem.eql(u8, reply, id)) return self.responses.orderedRemove(index);
        }
        return null;
    }

    fn answer(self: *Runner, id: []const u8, deadline: Deadline) !oap_types.Envelope {
        while (true) {
            if (self.takeAnswer(id)) |found| return found;
            if (deadline.expired()) return error.ConformanceEndpointSilent;
            try self.pullUntil(deadline);
        }
    }

    fn takeControl(self: *Runner, id: []const u8) ?ControlFrame {
        for (self.controls.items, 0..) |*candidate, index| {
            if (std.mem.eql(u8, candidate.id, id)) return self.controls.orderedRemove(index);
        }
        return null;
    }

    fn answerControl(self: *Runner, id: []const u8, deadline: Deadline) !ControlFrame {
        while (true) {
            if (self.takeControl(id)) |found| return found;
            if (deadline.expired()) return error.ControlUnanswered;
            const frame = self.client.next(deadline.budget()) catch |err| {
                if (err == endpoint_client.Error.EndpointClosed) break;
                return err;
            } orelse break;
            switch (frame) {
                .control => |control_line| {
                    const control = try parseControl(self.allocator, control_line);
                    if (std.mem.eql(u8, control.id, id)) return control;
                    self.controls.append(self.allocator, control) catch |err| {
                        var released = control;
                        released.deinit(self.allocator);
                        return err;
                    };
                },
                .envelope => |envelope_line| {
                    var envelope = try oap_envelope.deserializeEnvelope(envelope_line, self.allocator);
                    if (envelope.in_reply_to != null) {
                        self.responses.append(self.allocator, envelope) catch |err| {
                            envelope.deinit(self.allocator);
                            return err;
                        };
                    } else {
                        self.events.append(self.allocator, envelope) catch |err| {
                            envelope.deinit(self.allocator);
                            return err;
                        };
                    }
                },
            }
        }
        return error.ControlUnanswered;
    }

    fn nextEvent(self: *Runner, deadline: Deadline) !oap_types.Envelope {
        while (self.events.items.len == 0) {
            if (deadline.expired()) return error.ConformanceEndpointSilent;
            try self.pullUntil(deadline);
        }
        if (deadline.expired()) return error.ConformanceEndpointSilent;
        return self.events.orderedRemove(0);
    }

    fn probe(self: *Runner, payload: oap_types.Payload, run_id: ?[]const u8, revision: []const u8) ![]const u8 {
        var owned = payload;
        defer owned.deinit(self.allocator);
        const id = try self.nextId("request");
        errdefer self.allocator.free(id);
        const envelope: oap_types.Envelope = .{
            .id = id,
            .payload = owned,
            .session_id = self.session,
            .run_id = run_id,
            .capability_revision = if (revision.len == 0) null else revision,
        };
        const line = try oap_envelope.serializeEnvelope(envelope, self.allocator);
        defer self.allocator.free(line);
        try self.client.write(line);
        return id;
    }

    fn rawProbe(self: *Runner, envelope_type: []const u8, revision: []const u8) ![]const u8 {
        const id = try self.nextId("request");
        errdefer self.allocator.free(id);
        var buffer = std.ArrayList(u8).empty;
        defer buffer.deinit(self.allocator);
        var writer = json_writer.JsonWriter.init(&buffer, self.allocator);
        try writer.beginObject();
        try writer.writeStringField("protocol", oap_types.PROTOCOL);
        try writer.writeStringField("version", oap_types.VERSION);
        try writer.writeStringField("profile", oap_types.PROFILE);
        try writer.writeStringField("type", envelope_type);
        try writer.writeStringField("id", id);
        try writer.writeStringField("session_id", self.session);
        if (revision.len != 0) try writer.writeStringField("capability_revision", revision);
        try writer.writeKey("payload");
        try writer.beginObject();
        try writer.writeStringField("session_id", self.session);
        try writer.endObject();
        try writer.endObject();
        try self.client.write(buffer.items);
        return id;
    }

    fn request(self: *Runner, payload: oap_types.Payload, run_id: ?[]const u8, deadline: Deadline) !oap_types.Envelope {
        const id = try self.probe(payload, run_id, self.revision);
        defer self.allocator.free(id);

        var answered = try self.answer(id, deadline);
        if (answered.payload == .error_response) {
            const failure = answered.payload.error_response;
            var released = false;
            defer if (!released) answered.deinit(self.allocator);
            self.refusal = try std.fmt.allocPrint(self.allocator, "{s}: {s}", .{ failure.code, failure.message });
            answered.deinit(self.allocator);
            released = true;
            return error.ConformanceRefused;
        }
        return answered;
    }

    fn probeDeadline(self: *Runner) Deadline {
        return Deadline.start(self.client.io(), self.probe_budget_ms);
    }

    fn drive(self: *Runner, probe_budget_ms: i64) !void {
        self.probe_budget_ms = probe_budget_ms;

        var init_fields: [2][]const u8 = undefined;
        var init_built: usize = 0;
        var versions: []const []const u8 = undefined;
        var profiles: []const []const u8 = undefined;
        var versions_owned = false;
        var profiles_owned = false;
        var init_handed_off = false;
        errdefer if (!init_handed_off) {
            for (init_fields[0..init_built]) |copy| self.allocator.free(copy);
            if (versions_owned) oap_types.freeStringList(self.allocator, versions);
            if (profiles_owned) oap_types.freeStringList(self.allocator, profiles);
        };
        init_fields[0] = try self.allocator.dupe(u8, "conformance");
        init_built = 1;
        init_fields[1] = try self.allocator.dupe(u8, "OAP conformance runner");
        init_built = 2;
        const participant_id = init_fields[0];
        const participant_name = init_fields[1];

        versions = try oap_types.dupeStringList(self.allocator, &.{oap_types.VERSION});
        versions_owned = true;
        profiles = try oap_types.dupeStringList(self.allocator, &.{oap_types.PROFILE});
        profiles_owned = true;

        init_handed_off = true;
        const initialized = self.request(.{
            .initialize_request = .{
                .protocol_versions = versions,
                .profiles = profiles,
                .participant = .{ .id = participant_id, .name = participant_name },
            },
        }, null, self.probeDeadline()) catch |err| {
            try self.failReason("protocol.initialize.request is answered", err);
            return;
        };
        var held = initialized;
        defer held.deinit(self.allocator);
        if (held.payload != .initialize_response) {
            try self.fail("protocol.initialize.request is answered", "the endpoint answered another request");
            return;
        }
        try self.pass("protocol.initialize.request is answered");

        var capabilities = self.request(.capabilities_request, null, self.probeDeadline()) catch |err| {
            try self.failReason("capabilities.request is answered", err);
            return;
        };
        defer capabilities.deinit(self.allocator);
        if (capabilities.payload != .capabilities_response) {
            try self.fail("capabilities.response decodes as a descriptor", "the endpoint answered another request");
            return;
        }
        try self.pass("capabilities.request is answered");
        if (capabilities.payload.capabilities_response.feature("run.cancel")) |declared| {
            self.cancel_level = try self.allocator.dupe(u8, @tagName(declared.level));
        }
        if (capabilities.capability_revision) |revision| {
            self.revision = revision;
            try self.pass("capabilities.response carries a capability revision");
        } else {
            try self.fail(
                "capabilities.response carries a capability revision",
                "the response set no capability_revision, so nothing can be bound to this descriptor",
            );
        }

        var open_handed_off = false;
        const open_session = try self.allocator.dupe(u8, self.session);
        errdefer if (!open_handed_off) self.allocator.free(open_session);
        open_handed_off = true;
        var opened = self.request(.{ .session_open_request = .{ .session_id = open_session } }, null, self.probeDeadline()) catch |err| {
            try self.failReason("session.open.request is answered", err);
            return;
        };
        defer opened.deinit(self.allocator);
        if (opened.payload != .session_open_response) {
            try self.fail("session.open.response decodes as a session state", "the endpoint answered another request");
            return;
        }
        try self.pass("session.open.request is answered");
        const state = opened.payload.session_open_response;
        if (std.mem.eql(u8, state.session_id, self.session)) {
            try self.pass("the open names the session it was asked for");
        } else {
            try self.failOwned(
                "the open names the session it was asked for",
                try std.fmt.allocPrint(self.allocator, "opened {s}, asked for {s}", .{ state.session_id, self.session }),
            );
        }

        var submit_handed_off = false;
        const submit_session = try self.allocator.dupe(u8, self.session);
        errdefer if (!submit_handed_off) self.allocator.free(submit_session);
        const submit_messages = try self.scriptedMessages();
        submit_handed_off = true;
        var admitted = self.request(.{ .message_submit_request = .{
            .session_id = submit_session,
            .messages = submit_messages,
            .delivery = .auto,
        } }, null, self.probeDeadline()) catch |err| {
            try self.failReason("session.message.submit.request is answered", err);
            return;
        };
        defer admitted.deinit(self.allocator);
        if (admitted.payload != .message_submit_response) {
            try self.fail("the admission decodes", "the endpoint answered another request");
            return;
        }
        try self.pass("session.message.submit.request is answered");
        const admission = admitted.payload.message_submit_response;
        if (!admission.accepted or admission.run_id == null or admission.run_id.?.len == 0) {
            try self.failOwned(
                "the submission is admitted and names its run",
                try std.fmt.allocPrint(self.allocator, "accepted={} run={?s}", .{ admission.accepted, admission.run_id }),
            );
            return;
        }
        try self.pass("the submission is admitted and names its run");
        self.run_id = admission.run_id.?;

        if (admission.requested_delivery != .auto) {
            try self.fail(
                "the admission repeats requested_delivery and reports a concrete effective_delivery",
                "the admission did not repeat the requested auto delivery",
            );
        } else {
            const settled = try std.fmt.allocPrint(self.allocator, "auto resolved to {s}", .{@tagName(admission.effective_delivery)});
            try self.passOwned("the admission repeats requested_delivery and reports a concrete effective_delivery", settled);
        }

        try self.consumeRun(self.probeDeadline());
        try self.replayRun(self.probeDeadline());
        try self.refuseStaleRevision(self.probeDeadline());
        try self.answerCancel(self.probeDeadline());
        try self.refuseAddressableEnvelope(self.probeDeadline());
    }

    fn replayRun(self: *Runner, deadline: Deadline) !void {
        const accepted = "a cursor replay is accepted and re-delivers the run";
        if (self.run_id.len == 0) return;

        var buffer = std.ArrayList(u8).empty;
        defer buffer.deinit(self.allocator);
        var writer = json_writer.JsonWriter.init(&buffer, self.allocator);
        try writer.beginObject();
        try writer.writeStringField("control", "replay");
        try writer.writeStringField("id", replay_id);
        try writer.writeStringField("session_id", self.session);
        try writer.writeStringField("run_id", self.run_id);
        try writer.writeIntField("after", 0);
        try writer.endObject();
        try self.client.write(buffer.items);

        const control_answer = self.answerControl(replay_id, deadline) catch |err| {
            if (err == error.ControlUnanswered) {
                try self.fail(
                    accepted,
                    "the endpoint answered nothing; a control it does not implement must still be answered with unsupported_control",
                );
                return;
            }
            try self.failReason(accepted, err);
            return;
        };
        var held = control_answer;
        defer held.deinit(self.allocator);

        if (std.mem.eql(u8, held.control, "replay.accepted")) {
        } else if (std.mem.eql(u8, held.control, "replay.gap")) {
            try self.failOwned(
                accepted,
                try std.fmt.allocPrint(
                    self.allocator,
                    "the endpoint retains nothing at {d}; its window is {d}..{d}",
                    .{ held.requested_after, held.oldest_available, held.latest_available },
                ),
            );
            return;
        } else if (std.mem.eql(u8, held.code, "unsupported_control")) {
            try self.skip(accepted, "the endpoint does not implement the replay control, which the binding permits");
            return;
        } else {
            try self.failOwned(
                accepted,
                try std.fmt.allocPrint(self.allocator, "{s}: {s}", .{ held.code, held.message }),
            );
            return;
        }

        var replayed = std.ArrayList([]const u8).empty;
        defer {
            for (replayed.items) |id| self.allocator.free(id);
            replayed.deinit(self.allocator);
        }
        while (true) {
            var event = self.nextEvent(deadline) catch |err| {
                try self.failReason(accepted, err);
                return;
            };
            defer event.deinit(self.allocator);
            const belongs = event.run_id != null and std.mem.eql(u8, event.run_id.?, self.run_id);
            if (!belongs) continue;
            const id_copy = self.allocator.dupe(u8, event.id) catch |err| {
                return err;
            };
            replayed.append(self.allocator, id_copy) catch |err| {
                self.allocator.free(id_copy);
                return err;
            };
            if (event.payload.isTerminal()) break;
        }

        if (self.run_events.items.len == 0) {
            try self.fail(accepted, "the run delivered no envelope the replay could re-deliver");
            return;
        }
        if (replayed.items.len != self.run_events.items.len) {
            try self.failOwned(
                accepted,
                try std.fmt.allocPrint(
                    self.allocator,
                    "the replay delivered {d} envelopes for a run of {d}; a cursor re-delivers what the host already has, not a different set",
                    .{ replayed.items.len, self.run_events.items.len },
                ),
            );
            return;
        }
        for (replayed.items, self.run_events.items) |seen, original| {
            if (std.mem.eql(u8, seen, original)) continue;
            try self.failOwned(
                accepted,
                try std.fmt.allocPrint(
                    self.allocator,
                    "the replay delivered envelope \"{s}\" where the run had \"{s}\"",
                    .{ seen, original },
                ),
            );
            return;
        }
        try self.pass(accepted);
    }

    fn refuseStaleRevision(self: *Runner, deadline: Deadline) !void {
        const name = "a stale capability_revision is refused with stale_capabilities";
        if (self.revision.len == 0) {
            try self.skip(name, "the endpoint issued no revision, so none can be stale");
            return;
        }
        const stale = try std.fmt.allocPrint(self.allocator, "{s}-stale", .{self.revision});
        defer self.allocator.free(stale);
        var handed_off = false;
        const probe_session = try self.allocator.dupe(u8, self.session);
        defer if (!handed_off) self.allocator.free(probe_session);
        handed_off = true;
        const id = try self.probe(.{
            .session_state_request = .{ .session_id = probe_session },
        }, null, stale);
        defer self.allocator.free(id);

        var answered = self.answer(id, deadline) catch |err| {
            try self.failReason(name, err);
            return;
        };
        defer answered.deinit(self.allocator);

        if (answered.payload != .error_response) {
            try self.failOwned(
                name,
                try std.fmt.allocPrint(
                    self.allocator,
                    "a request citing a revision this endpoint never issued was answered {s}",
                    .{answered.payload.typeName()},
                ),
            );
            return;
        }
        const failure = answered.payload.error_response;
        if (!std.mem.eql(u8, failure.code, "stale_capabilities")) {
            try self.failOwned(
                name,
                try std.fmt.allocPrint(self.allocator, "refused \"{s}\", want \"stale_capabilities\"", .{failure.code}),
            );
            return;
        }
        try self.pass(name);
    }

    fn refuseAddressableEnvelope(self: *Runner, deadline: Deadline) !void {
        const name = "an addressable envelope that is wrong draws a correlated refusal";
        const id = try self.rawProbe("conformance.not.a.real.request", self.revision);
        defer self.allocator.free(id);

        var answered = self.answer(id, deadline) catch |err| {
            try self.failReason(name, err);
            return;
        };
        defer answered.deinit(self.allocator);

        if (answered.payload != .error_response) {
            try self.failOwned(
                name,
                try std.fmt.allocPrint(self.allocator, "an unserveable request was answered {s}", .{answered.payload.typeName()}),
            );
            return;
        }
        const correlated = answered.in_reply_to != null and std.mem.eql(u8, answered.in_reply_to.?, id);
        if (!correlated) {
            try self.failOwned(
                name,
                try std.fmt.allocPrint(
                    self.allocator,
                    "the refusal is correlated to \"{?s}\", not to the request \"{s}\" that drew it",
                    .{ answered.in_reply_to, id },
                ),
            );
            return;
        }
        var handed_off = false;
        const probe_session = try self.allocator.dupe(u8, self.session);
        defer if (!handed_off) self.allocator.free(probe_session);
        handed_off = true;
        var recovered = self.request(.{
            .session_state_request = .{ .session_id = probe_session },
        }, null, self.probeDeadline()) catch |err| {
            try self.failOwned(
                name,
                try std.fmt.allocPrint(
                    self.allocator,
                    "the endpoint stopped answering after a recoverable protocol error: {s}",
                    .{self.reasonOf(err)},
                ),
            );
            return;
        };
        defer recovered.deinit(self.allocator);
        try self.pass(name);
    }

    fn answerCancel(self: *Runner, deadline: Deadline) !void {
        const name = "run.cancel.request is supported, or refused as unavailable";
        if (self.run_id.len == 0) {
            try self.skip(name, "no run was admitted, so there is nothing to cancel");
            return;
        }

        var handed_off = false;
        const cancel_session = try self.allocator.dupe(u8, self.session);
        defer if (!handed_off) self.allocator.free(cancel_session);
        const cancel_run = try self.allocator.dupe(u8, self.run_id);
        defer if (!handed_off) self.allocator.free(cancel_run);
        handed_off = true;
        const id = try self.probe(.{ .run_cancel_request = .{
            .session_id = cancel_session,
            .run_id = cancel_run,
        } }, self.run_id, self.revision);
        defer self.allocator.free(id);

        var answered = self.answer(id, deadline) catch |err| {
            try self.failReason(name, err);
            return;
        };
        defer answered.deinit(self.allocator);

        const unavailable = self.cancel_level.len == 0 or
            std.mem.eql(u8, self.cancel_level, "unavailable");
        if (!unavailable) {
            if (answered.payload != .run_cancel_response and answered.payload != .error_response) {
                try self.failOwned(
                    name,
                    try std.fmt.allocPrint(
                        self.allocator,
                        "an endpoint declaring run.cancel \"{s}\" answered {s}",
                        .{ self.cancel_level, answered.payload.typeName() },
                    ),
                );
                return;
            }
            try self.pass(name);
            return;
        }

        if (answered.payload != .error_response) {
            try self.fail(name, "an endpoint declaring cancellation unavailable answered the call instead of refusing it");
            return;
        }
        const failure = answered.payload.error_response;
        if (!std.mem.eql(u8, failure.code, "unsupported_feature")) {
            try self.failOwned(
                name,
                try std.fmt.allocPrint(
                    self.allocator,
                    "refused \"{s}\", want the typed \"unsupported_feature\"",
                    .{failure.code},
                ),
            );
            return;
        }
        try self.pass(name);
    }

    fn reasonOf(self: *const Runner, err: anyerror) []const u8 {
        if (err == error.ConformanceRefused and self.refusal.len != 0) return self.refusal;
        return @errorName(err);
    }

    fn scriptedMessages(self: *Runner) ![]oap_types.Message {
        const text = try self.allocator.dupe(u8, "drive one scripted run");
        errdefer self.allocator.free(text);
        const messages = try self.allocator.alloc(oap_types.Message, 1);
        errdefer self.allocator.free(messages);
        messages[0] = .{ .role = .user, .content = .{ .text = text } };
        return messages;
    }

    fn drain(self: *Runner, deadline: Deadline) !void {
        while (true) {
            if (deadline.expired()) return;
            const frame = (self.client.next(deadline.budget()) catch |err| {
                if (err == endpoint_client.Error.EndpointClosed) return;
                try self.fail("frames the endpoint writes after the run completes decode", @errorName(err));
                return;
            }) orelse return;
            if (frame == .control) continue;
            var envelope = oap_envelope.deserializeEnvelope(frame.envelope, self.allocator) catch |err| {
                try self.fail("frames the endpoint writes after the run completes decode", @errorName(err));
                return;
            };
            self.events.append(self.allocator, envelope) catch |err| {
                envelope.deinit(self.allocator);
                return err;
            };
        }
    }

    fn consumeRun(self: *Runner, deadline: Deadline) !void {
        var last_sequence: u64 = 0;
        while (true) {
            const event = self.nextEvent(deadline) catch |err| {
                try self.fail("the run reaches a terminal event", @errorName(err));
                return;
            };
            var held = event;
            defer held.deinit(self.allocator);

            if (held.run_id != null and std.mem.eql(u8, held.run_id.?, self.run_id)) {
                const id_copy = self.allocator.dupe(u8, held.id) catch |err| {
                    return err;
                };
                self.run_events.append(self.allocator, id_copy) catch |err| {
                    self.allocator.free(id_copy);
                    return err;
                };
            }
            if (held.sequence) |sequence| {
                if (held.run_id != null and std.mem.eql(u8, held.run_id.?, self.run_id)) {
                    if (sequence <= last_sequence) {
                        try self.failOwned(
                            "run events carry an advancing per-run sequence",
                            try std.fmt.allocPrint(self.allocator, "sequence {d} did not advance past {d}", .{ sequence, last_sequence }),
                        );
                        return;
                    }
                    last_sequence = sequence;
                }
            }
            switch (held.payload) {
                .permission_requested => |*gate| {
                    self.answerPermission(gate, deadline) catch |err| {
                        try self.fail("a permission gate is resolvable from the stream", @errorName(err));
                        return;
                    };
                    try self.pass("a permission gate is resolvable from the stream");
                },
                .user_input_requested => |*gate| {
                    self.answerInput(gate, deadline) catch |err| {
                        try self.fail("a user input gate is resolvable from the stream", @errorName(err));
                        return;
                    };
                    try self.pass("a user input gate is resolvable from the stream");
                },
                else => {},
            }
            if (held.payload.isTerminal()) {
                const settled = try std.fmt.allocPrint(self.allocator, "settled {s}", .{held.payload.typeName()});
                try self.passOwned("the run reaches a terminal event", settled);
                return;
            }
        }
    }

    fn answerPermission(self: *Runner, gate: *const oap_types.PermissionEvent, deadline: Deadline) !void {
        if (gate.choices.len == 0) return error.NoChoicesOffered;

        const fields = [_][]const u8{
            gate.interaction_id,
            gate.requested_by,
            gate.responded_by,
            gate.session_id,
            gate.run_id,
            gate.choices[0].id,
        };
        var copies: [fields.len][]const u8 = undefined;
        var built: usize = 0;
        var handed_off = false;
        errdefer if (!handed_off) {
            for (copies[0..built]) |copy| self.allocator.free(copy);
        };
        for (fields, 0..) |field, index| {
            copies[index] = try self.allocator.dupe(u8, field);
            built += 1;
        }

        const payload: oap_types.Payload = .{ .permission_resolve_request = .{
            .interaction_id = copies[0],
            .requested_by = copies[1],
            .responded_by = copies[2],
            .session_id = copies[3],
            .run_id = copies[4],
            .choice_id = copies[5],
            .granted = true,
        } };
        handed_off = true;
        var resolved = try self.request(payload, copies[4], deadline);
        resolved.deinit(self.allocator);
    }

    fn answerInput(self: *Runner, gate: *const oap_types.UserInputEvent, deadline: Deadline) !void {
        const fields = [_][]const u8{
            gate.interaction_id,
            gate.requested_by,
            gate.responded_by,
            gate.session_id,
            gate.run_id,
        };
        var copies: [fields.len][]const u8 = undefined;
        var built: usize = 0;
        var answers: []oap_types.InputAnswer = &.{};
        var answered: usize = 0;
        var handed_off = false;
        errdefer if (!handed_off) {
            for (answers[0..answered]) |*written| written.deinit(self.allocator);
            self.allocator.free(answers);
            for (copies[0..built]) |copy| self.allocator.free(copy);
        };
        for (fields, 0..) |field, index| {
            copies[index] = try self.allocator.dupe(u8, field);
            built += 1;
        }

        answers = try self.allocator.alloc(oap_types.InputAnswer, gate.questions.len);
        for (gate.questions) |question| {
            answers[answered] = .{ .question_id = undefined, .text = null, .selected_option_ids = &.{} };
            answers[answered].question_id = try self.allocator.dupe(u8, question.id);
            answered += 1;
            if (question.options.len > 0) {
                const first = try self.allocator.dupe(u8, question.options[0].id);
                errdefer self.allocator.free(first);
                answers[answered - 1].selected_option_ids = try self.allocator.dupe([]const u8, &.{first});
            } else {
                answers[answered - 1].text = try self.allocator.dupe(u8, "conformance");
            }
        }

        const payload: oap_types.Payload = .{ .user_input_resolve_request = .{
            .interaction_id = copies[0],
            .requested_by = copies[1],
            .responded_by = copies[2],
            .session_id = copies[3],
            .run_id = copies[4],
            .answers = answers,
        } };
        handed_off = true;
        var resolved = try self.request(payload, copies[4], deadline);
        resolved.deinit(self.allocator);
    }
};

test "events that arrive ahead of the answer are read in the order they were written" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
        \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
        \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e2","part":{"type":"text","text":"one"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.requested","id":"e2","run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","title":"Allow","choices":[{"id":"approve","label":"Approve"}]}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e4","sequence":4,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e4","part":{"type":"text","text":"two"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","run_id":"run-1","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
        \\  *choice_id*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolved","id":"e3","run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","outcome":"resolved","choice_id":"approve","granted":true}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e6","sequence":6,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.response","id":"a5","in_reply_to":"conformance-request-5","run_id":"run-1","payload":{"interaction_id":"p-1","session_id":"conformance","run_id":"run-1","accepted":true}}' ;;
        \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"cancel-answer","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"cancellation is not implemented"}}}\n' "$rid" ;;
    \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"unknown-answer","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"no such request"}}}\n' "$rid" ;;
    \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"stale-answer","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"that revision is not current"}}}\n' "$rid" ;;
    \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"t","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
    \\  *replay*) printf '%s\n' '{"control":"replay.accepted","id":"conformance-replay-1"}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e2","part":{"type":"text","text":"one"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.requested","id":"e2","run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","title":"Allow","choices":[{"id":"approve","label":"Approve"}]}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e4","sequence":4,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e4","part":{"type":"text","text":"two"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolved","id":"e3","run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","outcome":"resolved","choice_id":"approve","granted":true}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e6","sequence":6,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' ;;
        \\  esac
        \\done
    ;

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    try std.testing.expect(report.passed());
    const gate_check = report.verdict("a permission gate is resolvable from the stream") orelse return error.CheckMissing;
    try std.testing.expect(gate_check.passed);
    const terminal = report.verdict("the run reaches a terminal event") orelse return error.CheckMissing;
    try std.testing.expectEqualStrings("settled run.completed", terminal.detail);
}

const fixture_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e2","part":{"type":"text","text":"one"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.requested","id":"e3","sequence":3,"run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","title":"Allow","choices":[{"id":"approve","label":"Approve"}]}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","run_id":"run-1","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  *choice_id*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolved","id":"e5","sequence":5,"run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","outcome":"resolved","choice_id":"approve","granted":true}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e6","sequence":6,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.response","id":"a7","in_reply_to":"conformance-request-5","run_id":"run-1","payload":{"interaction_id":"p-1","session_id":"conformance","run_id":"run-1","accepted":true}}' ;;
    \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"cancel-answer","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"cancellation is not implemented"}}}\n' "$rid" ;;
    \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"unknown-answer","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"no such request"}}}\n' "$rid" ;;
    \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"stale-answer","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"that revision is not current"}}}\n' "$rid" ;;
    \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"t","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
    \\  *replay*) printf '%s\n' '{"control":"replay.accepted","id":"conformance-replay-1"}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e2","part":{"type":"text","text":"one"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.requested","id":"e3","sequence":3,"run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","title":"Allow","choices":[{"id":"approve","label":"Approve"}]}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolved","id":"e5","sequence":5,"run_id":"run-1","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","outcome":"resolved","choice_id":"approve","granted":true}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e6","sequence":6,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' ;;
    \\  esac
    \\done
;

const refusing_script =
    \\while read -r line; do
    \\  printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"e1","in_reply_to":"conformance-request-1","payload":{"error":{"code":"unsupported_profile","message":"no"}}}'
    \\done
;

fn runAllocationProbe(allocator: std.mem.Allocator) !void {
    var report = try run(allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", fixture_script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();
    const gate = report.verdict("a permission gate is resolvable from the stream") orelse return error.ProbeReachedNoGate;
    if (!gate.passed) return;
}

test "a run that is refused part way through frees what it built exactly once" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    try std.testing.checkAllAllocationFailures(std.testing.allocator, runAllocationProbe, .{});
}

fn refusalAllocationProbe(allocator: std.mem.Allocator) !void {
    var report = try run(allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", refusing_script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();
    const refused = report.verdict("protocol.initialize.request is answered") orelse return error.ProbeReachedNoRefusal;
    if (refused.passed) return;
}

test "a refusal frees its own envelope, and the reason, exactly once" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    try std.testing.checkAllAllocationFailures(std.testing.allocator, refusalAllocationProbe, .{});
}

test "a frame the endpoint writes after the terminal event is recorded, not swallowed" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  esac
        \\done
        \\printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"nonsense.event","id":"z1","payload":{}}'
    ;

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    try std.testing.expect(!report.passed());
    const trailing = report.verdict("frames the endpoint writes after the run completes decode") orelse return error.CheckMissing;
    try std.testing.expect(!trailing.passed);
}

test "an endpoint that exits non-zero is judged, and its detail is freed with it" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", "while read -r line; do :; done; exit 3" },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const exiting = report.verdict("endpoint exits 0 after stdin EOF") orelse return error.CheckMissing;
    try std.testing.expect(!exiting.passed);
    try std.testing.expectEqualStrings("exit code 3", exiting.detail);
}

test "a line with neither protocol nor control is judged, not skipped" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  esac
        \\done
        \\printf '%s\n' 'not json at all' '{"an":"object"}'
    ;

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    try std.testing.expect(!report.passed());
    const trailing = report.verdict("frames the endpoint writes after the run completes decode") orelse return error.CheckMissing;
    try std.testing.expect(!trailing.passed);
    try std.testing.expectEqualStrings("UnclassifiedFrame", trailing.detail);
}

test "a control frame is answered, never judged" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  esac
        \\done
        \\printf '%s\n' '{"control":"replay.accepted","id":"r1"}'
    ;

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    try std.testing.expect(report.verdict("frames the endpoint writes after the run completes decode") == null);
}

test "an endpoint that ignores stdin EOF is reported under goap's check name" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", "while read -r line; do :; done; sleep 30" },
        .probe_budget_ms = 1000,
        .exit_grace_ms = 200,
    });
    defer report.deinit();

    try std.testing.expect(!report.passed());
    const killed = report.verdict("endpoint exits after stdin EOF") orelse return error.CheckMissing;
    try std.testing.expect(!killed.passed);
    try std.testing.expectEqualStrings(
        "the endpoint was still running once the exit grace elapsed, so it was killed",
        killed.detail,
    );
    try std.testing.expect(report.verdict("endpoint exits 0 after stdin EOF") == null);
}

const cancel_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{"run.cancel":{"key":"run.cancel","level":"native"}}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.cancel.response","id":"cancel-answer","in_reply_to":"%s","payload":{"session_id":"conformance","run_id":"run-1","accepted":true,"status":"cancelling"}}\n' "$rid" ;;
    \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"unknown-answer","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"no such request"}}}\n' "$rid" ;;
    \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"stale-answer","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"that revision is not current"}}}\n' "$rid" ;;
    \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"state-answer","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
    \\  *replay*) printf '%s\n' '{"control":"replay.error","id":"conformance-replay-1","code":"unsupported_control","message":"no replay here"}' ;;
    \\  esac
    \\done
;

const cancelling_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{"run.cancel":{"key":"run.cancel","level":"native"}}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"cancel-answer","in_reply_to":"%s","payload":{"session_id":"conformance","accepted":true,"submission_id":"s2","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-2"}}\n' "$rid" ;;
    \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"unknown-answer","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"no such request"}}}\n' "$rid" ;;
    \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"stale-answer","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"that revision is not current"}}}\n' "$rid" ;;
    \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"state-answer","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
    \\  *replay*) printf '%s\n' '{"control":"replay.error","id":"conformance-replay-1","code":"unsupported_control","message":"no replay here"}' ;;
    \\  esac
    \\done
;

test "an endpoint declaring run.cancel supported may answer the call" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", cancel_script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const cancel = report.verdict("run.cancel.request is supported, or refused as unavailable") orelse return error.CheckMissing;
    try std.testing.expect(cancel.passed);
    try std.testing.expect(!cancel.skipped);
}

test "an endpoint declaring run.cancel supported must not answer it with another request" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", cancelling_script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const cancel = report.verdict("run.cancel.request is supported, or refused as unavailable") orelse return error.CheckMissing;
    try std.testing.expect(!cancel.passed);
    try std.testing.expectEqualStrings(
        "an endpoint declaring run.cancel \"native\" answered session.message.submit.response",
        cancel.detail,
    );
}

const stale_refusing_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","run_id":"run-1","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"c","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"x"}}}\n' "$rid" ;;
    \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"u","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"x"}}}\n' "$rid" ;;
    \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"s","in_reply_to":"%s","payload":{"error":{"code":"invalid_request","message":"that revision is not one this endpoint issued"}}}\n' "$rid" ;;
    \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"t","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
    \\  *replay*) printf '%s\n' '{"control":"replay.error","id":"conformance-replay-1","code":"unsupported_control","message":"no replay here"}' ;;
    \\  esac
    \\done
;

test "a revision this endpoint never issued is judged, not waved through" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", stale_refusing_script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const stale = report.verdict("a stale capability_revision is refused with stale_capabilities") orelse return error.CheckMissing;
    try std.testing.expect(!stale.passed);
    try std.testing.expectEqualStrings(
        "refused \"invalid_request\", want \"stale_capabilities\"",
        stale.detail,
    );
}

test "a recovery failure is reported under the check it belongs to" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
        \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
        \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","run_id":"run-1","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
        \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"c","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"x"}}}\n' "$rid" ;;
        \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"s","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"x"}}}\n' "$rid" ;;
        \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"u","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"x"}}}\n' "$rid" ;;
        \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"r","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"this endpoint will not be asked again"}}}\n' "$rid" ;;
        \\  *replay*) printf '%s\n' '{"control":"replay.error","id":"conformance-replay-1","code":"unsupported_control","message":"no replay here"}' ;;
        \\  esac
        \\done
    ;

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 2000,
    });
    defer report.deinit();

    try std.testing.expect(!report.passed());
    const refused = report.verdict("an addressable envelope that is wrong draws a correlated refusal") orelse return error.CheckMissing;
    try std.testing.expect(!refused.passed);
    try std.testing.expectEqualStrings(
        "the endpoint stopped answering after a recoverable protocol error: unsupported_feature: this endpoint will not be asked again",
        refused.detail,
    );
    try std.testing.expect(report.verdict("the endpoint stopped answering after a recoverable protocol error") == null);
}

test "a replay is accepted and its events reach the runner" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", fixture_script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const replay = report.verdict("a cursor replay is accepted and re-delivers the run") orelse return error.CheckMissing;
    try std.testing.expect(replay.passed);
}

test "a negative number in a control frame is judged, not fatal" {
    const frame = parseControl(std.testing.allocator, "{\"control\":\"replay.gap\",\"id\":\"r1\",\"requested_after\":-1}") catch |err| {
        try std.testing.expect(err == error.InvalidControlFrame);
        return;
    };
    var released = frame;
    released.deinit(std.testing.allocator);
    return error.NegativeNotRejected;
}

test "a control frame that is not JSON is judged, not fatal" {
    try std.testing.expectError(error.InvalidControlFrame, parseControl(std.testing.allocator, "not json"));
    try std.testing.expectError(error.InvalidControlFrame, parseControl(std.testing.allocator, "[1,2,3]"));
}

test "a cursor that is not a whole non-negative number is judged, not swallowed" {
    const cases = [_][]const u8{
        "{\"control\":\"replay.gap\",\"id\":\"r1\",\"requested_after\":5.5}",
        "{\"control\":\"replay.gap\",\"id\":\"r1\",\"requested_after\":5.0}",
        "{\"control\":\"replay.gap\",\"id\":\"r1\",\"requested_after\":\"5\"}",
        "{\"control\":\"replay.gap\",\"id\":\"r1\",\"requested_after\":true}",
        "{\"control\":\"replay.gap\",\"id\":\"r1\",\"requested_after\":[5]}",
    };
    for (cases) |line| {
        try std.testing.expectError(error.InvalidControlFrame, parseControl(std.testing.allocator, line));
    }
}

const replay_verdict = "a cursor replay is accepted and re-delivers the run";

fn replayVerdictOf(report: *const Report) !Check {
    return report.verdict(replay_verdict) orelse error.CheckMissing;
}


fn withoutEnvelopeRunId(allocator: std.mem.Allocator, script: []const u8) ![]u8 {
    const marker = ",\"run_id\":\"run-1\",\"payload\"";
    const boundary = std.mem.indexOf(u8, script, "*replay*)") orelse return error.MarkerMissing;
    const stream = script[0..boundary];
    const rest = script[boundary..];
    var out = std.ArrayList(u8).empty;
    errdefer out.deinit(allocator);
    var head = stream;
    while (true) {
        const at = std.mem.indexOf(u8, head, marker) orelse {
            try out.appendSlice(allocator, head);
            break;
        };
        try out.appendSlice(allocator, head[0..at]);
        try out.appendSlice(allocator, ",\"payload\"");
        head = head[at + marker.len ..];
    }
    try out.appendSlice(allocator, rest);
    return out.toOwnedSlice(allocator);
}

fn withReplayBody(allocator: std.mem.Allocator, body: []const u8) ![]u8 {
    const marker = "@@REPLAY@@";
    const at = std.mem.indexOf(u8, replay_case_script, marker) orelse return error.MarkerMissing;
    var out = std.ArrayList(u8).empty;
    errdefer out.deinit(allocator);
    try out.appendSlice(allocator, replay_case_script[0..at]);
    try out.appendSlice(allocator, body);
    try out.appendSlice(allocator, replay_case_script[at + marker.len ..]);
    return out.toOwnedSlice(allocator);
}



const replay_case_script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
        \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
        \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
        \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"c","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"x"}}}\n' "$rid" ;;
        \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"s","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"x"}}}\n' "$rid" ;;
        \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"u","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"x"}}}\n' "$rid" ;;
        \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"t","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
        \\  *replay*) printf '%s\n' @@REPLAY@@ ;;
        \\  esac
        \\done
    ;

const replay_over_unattributed_run = "'{\"control\":\"replay.accepted\",\"id\":\"conformance-replay-1\"}' '{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.started\",\"id\":\"e1\",\"sequence\":1,\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"conformance\",\"run_id\":\"run-1\",\"status\":\"running\"}}' '{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.completed\",\"id\":\"e2\",\"sequence\":2,\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"conformance\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}'";

const renamed_replay = "'{\"control\":\"replay.accepted\",\"id\":\"conformance-replay-1\"}' '{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.started\",\"id\":\"renamed\",\"sequence\":1,\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"conformance\",\"run_id\":\"run-1\",\"status\":\"running\"}}' '{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.completed\",\"id\":\"e2\",\"sequence\":2,\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"conformance\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}'";

const short_replay = "'{\"control\":\"replay.accepted\",\"id\":\"conformance-replay-1\"}' '{\"protocol\":\"open-agent-protocol\",\"version\":\"0.1\",\"profile\":\"open-agent-protocol.agent-control-core\",\"type\":\"run.completed\",\"id\":\"e2\",\"sequence\":2,\"run_id\":\"run-1\",\"payload\":{\"session_id\":\"conformance\",\"run_id\":\"run-1\",\"stop_reason\":\"end_turn\",\"final_response\":{\"role\":\"assistant\",\"content\":\"done\"}}}'";

const unsupported_replay = "'{\"control\":\"replay.error\",\"id\":\"conformance-replay-1\",\"code\":\"unsupported_control\",\"message\":\"no replay here\"}'";

test "a replay that drops an envelope fails the check, through the public runner" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script = try withReplayBody(std.testing.allocator, short_replay);
    defer std.testing.allocator.free(script);

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const replay = try replayVerdictOf(&report);
    try std.testing.expect(!replay.passed);
    try std.testing.expectEqualStrings(
        "the replay delivered 1 envelopes for a run of 2; a cursor re-delivers what the host already has, not a different set",
        replay.detail,
    );
}

test "an endpoint that refuses the replay control skips the check, through the public runner" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script = try withReplayBody(std.testing.allocator, unsupported_replay);
    defer std.testing.allocator.free(script);

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const replay = try replayVerdictOf(&report);
    try std.testing.expect(replay.skipped);
    try std.testing.expect(replay.passed);
    try std.testing.expectEqualStrings(
        "the endpoint does not implement the replay control, which the binding permits",
        replay.detail,
    );
}


test "a run whose events name no run is reported by the replay check, through the public runner" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script = try withReplayBody(std.testing.allocator, replay_over_unattributed_run);
    defer std.testing.allocator.free(script);
    const run_events = try withoutEnvelopeRunId(std.testing.allocator, script);
    defer std.testing.allocator.free(run_events);

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", run_events },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const replay = try replayVerdictOf(&report);
    try std.testing.expect(!replay.passed);
    try std.testing.expectEqualStrings("the run delivered no envelope the replay could re-deliver", replay.detail);
}


const chatty_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  esac
    \\done
    \\while true; do printf '%s\n' '{"control":"heartbeat"}'; done
;

test "a child that writes unrelated frames forever cannot outlive the probe budget" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const io = std.testing.io;
    const before = std.Io.Timestamp.now(io, .awake);
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", chatty_script },
        .probe_budget_ms = 1500,
        .exit_grace_ms = 500,
    });
    defer report.deinit();
    const elapsed = before.durationTo(std.Io.Timestamp.now(io, .awake));
    const spent = @as(i64, @intCast(@divTrunc(elapsed.toNanoseconds(), std.time.ns_per_ms)));

    try std.testing.expect(spent >= 0);
    try std.testing.expect(spent < 20_000);
    try std.testing.expect(!report.passed());
    const replay = report.verdict("a cursor replay is accepted and re-delivers the run") orelse return error.CheckMissing;
    try std.testing.expect(!replay.passed);
    try std.testing.expectEqualStrings(
        "the endpoint answered nothing; a control it does not implement must still be answered with unsupported_control",
        replay.detail,
    );
}

const no_terminal_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  *run.cancel.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"c","in_reply_to":"%s","payload":{"error":{"code":"unsupported_feature","message":"x"}}}\n' "$rid" ;;
    \\  *-stale*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"s","in_reply_to":"%s","payload":{"error":{"code":"stale_capabilities","message":"x"}}}\n' "$rid" ;;
    \\  *conformance.not.a.real.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"error.response","id":"u","in_reply_to":"%s","payload":{"error":{"code":"unknown_request","message":"x"}}}\n' "$rid" ;;
    \\  *session.state.request*) rid=$(printf '%s' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p'); printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.state.response","id":"t","in_reply_to":"%s","payload":{"session_id":"conformance","status":"idle"}}\n' "$rid" ;;
    \\  *replay*) printf '%s\n' '{"control":"replay.accepted","id":"conformance-replay-1"}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"r1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' ; sleep 30 ;;
    \\  esac
    \\done
;
test "an accepted replay that never terminates is judged inside the budget" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const io = std.testing.io;
    const before = std.Io.Timestamp.now(io, .awake);
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", no_terminal_script },
        .probe_budget_ms = 1500,
        .exit_grace_ms = 200,
    });
    defer report.deinit();
    const elapsed = before.durationTo(std.Io.Timestamp.now(io, .awake));
    const spent = @as(i64, @intCast(@divTrunc(elapsed.toNanoseconds(), std.time.ns_per_ms)));

    try std.testing.expect(spent >= 0);
    try std.testing.expect(spent < 20_000);
    try std.testing.expect(!report.passed());
    const replay = report.verdict("a cursor replay is accepted and re-delivers the run") orelse return error.CheckMissing;
    try std.testing.expect(!replay.passed);
    try std.testing.expectEqualStrings("ConformanceEndpointSilent", replay.detail);
}



test "a replay that renames an envelope fails the check, through the public runner" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script = try withReplayBody(std.testing.allocator, renamed_replay);
    defer std.testing.allocator.free(script);

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .probe_budget_ms = 5000,
    });
    defer report.deinit();

    const replay = try replayVerdictOf(&report);
    try std.testing.expect(!replay.passed);
    try std.testing.expectEqualStrings(
        "the replay delivered envelope \"renamed\" where the run had \"e1\"",
        replay.detail,
    );

}

const spam_after_eof_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  esac
    \\done
    \\while true; do printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"spam","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","message_id":"m","part":{"type":"text","text":"x"}}}'; done
;

test "a child that spams well-formed frames after stdin EOF is reaped inside the budget" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const io = std.testing.io;
    const before = std.Io.Timestamp.now(io, .awake);
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", spam_after_eof_script },
        .probe_budget_ms = 1500,
        .exit_grace_ms = 500,
    });
    defer report.deinit();
    const elapsed = before.durationTo(std.Io.Timestamp.now(io, .awake));
    const spent = @as(i64, @intCast(@divTrunc(elapsed.toNanoseconds(), std.time.ns_per_ms)));

    try std.testing.expect(spent >= 0);
    try std.testing.expect(spent < 20_000);
    const killed = report.verdict("endpoint exits after stdin EOF") orelse return error.CheckMissing;
    try std.testing.expect(!killed.passed);
    try std.testing.expectEqualStrings(
        "the endpoint was still running once the exit grace elapsed, so it was killed",
        killed.detail,
    );
    try std.testing.expect(report.verdict("endpoint exits 0 after stdin EOF") == null);
}

test "a probe deadline only ever runs down" {
    const io = std.testing.io;
    const d = Deadline.start(io, 40);
    try std.testing.expect(d.remainingMs() <= 40);
    try std.testing.expect(d.remainingMs() >= 0);
    try std.testing.expect(!d.expired());
    std.Io.sleep(io, .fromMilliseconds(60), .awake) catch {};
    try std.testing.expect(d.expired());
    try std.testing.expect(d.remainingMs() < 0);
}

const blank_line_flood_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  esac
    \\done
    \\while true; do printf '\n\n\n\n\n\n\n\n'; done
;

const partial_line_trickle_script =
    \\while read -r line; do
    \\  case "$line" in
    \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
    \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
    \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
    \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"run_id":"run-1","payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
    \\  esac
    \\done
    \\while true; do printf '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agen'; sleep 0.05; done
;

fn elapsedMs(io: std.Io, before: std.Io.Timestamp) i64 {
    const d = before.durationTo(std.Io.Timestamp.now(io, .awake));
    return @as(i64, @intCast(@divTrunc(d.toNanoseconds(), std.time.ns_per_ms)));
}

test "a blank-line flood cannot keep a correlation inside next" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const io = std.testing.io;
    const before = std.Io.Timestamp.now(io, .awake);
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", blank_line_flood_script },
        .probe_budget_ms = 1200,
        .exit_grace_ms = 500,
    });
    defer report.deinit();
    const spent = elapsedMs(io, before);

    try std.testing.expect(spent >= 0);
    try std.testing.expect(spent < 15_000);
}

test "a partial line trickled in forever cannot keep a correlation inside next" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const io = std.testing.io;
    const before = std.Io.Timestamp.now(io, .awake);
    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", partial_line_trickle_script },
        .probe_budget_ms = 1200,
        .exit_grace_ms = 500,
    });
    defer report.deinit();
    const spent = elapsedMs(io, before);

    try std.testing.expect(spent >= 0);
    try std.testing.expect(spent < 15_000);
}
