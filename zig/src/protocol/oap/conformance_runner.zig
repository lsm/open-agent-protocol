const std = @import("std");
const oap_types = @import("types");
const oap_envelope = @import("envelope");
const endpoint_client = @import("endpoint_client");

pub const default_line_deadline_ms: u64 = 300_000;

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
    session_id: []const u8 = "conformance",
    line_deadline_ms: i64 = default_line_deadline_ms,
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
    runner.client = try endpoint_client.Client.spawn(allocator, .{ .command = options.command, .args = options.args });
    defer runner.client.deinit();
    defer runner.releaseEnvelopes();

    try runner.drive(options.line_deadline_ms);

    runner.client.closeStdin();
    const code = runner.client.waitExit() catch |err| blk: {
        try runner.fail("endpoint exits 0 after stdin EOF", @errorName(err));
        break :blk null;
    };
    if (code) |status| {
        if (status == 0) {
            try runner.pass("endpoint exits 0 after stdin EOF");
        } else {
            try runner.fail("endpoint exits 0 after stdin EOF", try std.fmt.allocPrint(allocator, "exit code {d}", .{status}));
        }
    }
    return runner.report;
}

fn lineBudget(line_deadline_ms: i64) std.Io.Timeout {
    return .{ .duration = .{ .raw = std.Io.Duration.fromMilliseconds(line_deadline_ms), .clock = .boot } };
}

const Runner = struct {
    allocator: std.mem.Allocator,
    client: endpoint_client.Client = undefined,
    report: Report,
    responses: std.ArrayList(oap_types.Envelope) = .empty,
    events: std.ArrayList(oap_types.Envelope) = .empty,
    session: []const u8,
    revision: []const u8 = "",
    run_id: []const u8 = "",
    refusal: []const u8 = "",
    ids: usize = 0,

    fn releaseEnvelopes(self: *Runner) void {
        for (self.responses.items) |*envelope| envelope.deinit(self.allocator);
        for (self.events.items) |*envelope| envelope.deinit(self.allocator);
        self.responses.deinit(self.allocator);
        self.events.deinit(self.allocator);
        if (self.refusal.len != 0) self.allocator.free(self.refusal);
    }

    fn nextId(self: *Runner, kind: []const u8) ![]const u8 {
        self.ids += 1;
        return std.fmt.allocPrint(self.allocator, "conformance-{s}-{d}", .{ kind, self.ids });
    }

    fn pass(self: *Runner, name: []const u8) !void {
        try self.add(.{ .name = try self.allocator.dupe(u8, name) });
    }

    fn fail(self: *Runner, name: []const u8, detail: []const u8) !void {
        try self.add(.{
            .name = try self.allocator.dupe(u8, name),
            .passed = false,
            .detail = try self.allocator.dupe(u8, detail),
        });
    }

    fn add(self: *Runner, check: Check) !void {
        try self.report.checks.append(self.allocator, check);
    }


    fn pullUntil(self: *Runner, line_deadline_ms: i64) !void {
        const frame = try self.client.next(lineBudget(line_deadline_ms)) orelse return error.ConformanceEndpointSilent;
        if (frame == .control) return;
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

    fn answer(self: *Runner, id: []const u8, line_deadline_ms: i64) !oap_types.Envelope {
        while (true) {
            if (self.takeAnswer(id)) |found| return found;
            try self.pullUntil(line_deadline_ms);
        }
    }

    fn nextEvent(self: *Runner, line_deadline_ms: i64) !oap_types.Envelope {
        while (self.events.items.len == 0) try self.pullUntil(line_deadline_ms);
        return self.events.orderedRemove(self.events.items.len - 1);
    }

    fn request(self: *Runner, payload: oap_types.Payload, run_id: ?[]const u8, line_deadline_ms: i64) !oap_types.Envelope {
        const id = try self.nextId("request");
        defer self.allocator.free(id);
        var owned = payload;
        defer owned.deinit(self.allocator);
        const envelope: oap_types.Envelope = .{
            .id = id,
            .payload = owned,
            .session_id = self.session,
            .run_id = run_id,
            .capability_revision = if (self.revision.len == 0) null else self.revision,
        };
        const line = try oap_envelope.serializeEnvelope(envelope, self.allocator);
        defer self.allocator.free(line);
        try self.client.write(line);

        var answered = try self.answer(id, line_deadline_ms);
        if (answered.payload == .error_response) {
            const failure = answered.payload.error_response;
            self.refusal = try std.fmt.allocPrint(self.allocator, "{s}: {s}", .{ failure.code, failure.message });
            answered.deinit(self.allocator);
            return error.ConformanceRefused;
        }
        return answered;
    }

    fn drive(self: *Runner, line_deadline_ms: i64) !void {
        const participant_id = try self.allocator.dupe(u8, "conformance");
        errdefer self.allocator.free(participant_id);
        const participant_name = try self.allocator.dupe(u8, "OAP conformance runner");
        errdefer self.allocator.free(participant_name);

        const initialized = self.request(.{
            .initialize_request = .{
                .protocol_versions = try oap_types.dupeStringList(self.allocator, &.{oap_types.VERSION}),
                .profiles = try oap_types.dupeStringList(self.allocator, &.{oap_types.PROFILE}),
                .participant = .{ .id = participant_id, .name = participant_name },
            },
        }, null, line_deadline_ms) catch |err| {
            try self.fail("protocol.initialize.request is answered", self.reasonOf(err));
            return;
        };
        var held = initialized;
        defer held.deinit(self.allocator);
        if (held.payload != .initialize_response) {
            try self.fail("protocol.initialize.request is answered", "the endpoint answered another request");
            return;
        }
        try self.pass("protocol.initialize.request is answered");

        var capabilities = self.request(.capabilities_request, null, line_deadline_ms) catch |err| {
            try self.fail("capabilities.request is answered", self.reasonOf(err));
            return;
        };
        defer capabilities.deinit(self.allocator);
        if (capabilities.payload != .capabilities_response) {
            try self.fail("capabilities.response decodes as a descriptor", "the endpoint answered another request");
            return;
        }
        try self.pass("capabilities.request is answered");
        if (capabilities.capability_revision) |revision| {
            self.revision = revision;
            try self.pass("capabilities.response carries a capability revision");
        } else {
            try self.fail(
                "capabilities.response carries a capability revision",
                "the response set no capability_revision, so nothing can be bound to this descriptor",
            );
        }

        const open_session = try self.allocator.dupe(u8, self.session);
        errdefer self.allocator.free(open_session);
        var opened = self.request(.{ .session_open_request = .{ .session_id = open_session } }, null, line_deadline_ms) catch |err| {
            try self.fail("session.open.request is answered", self.reasonOf(err));
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
            try self.fail(
                "the open names the session it was asked for",
                try std.fmt.allocPrint(self.allocator, "opened {s}, asked for {s}", .{ state.session_id, self.session }),
            );
        }

        const submit_session = try self.allocator.dupe(u8, self.session);
        errdefer self.allocator.free(submit_session);
        var admitted = self.request(.{ .message_submit_request = .{
            .session_id = submit_session,
            .messages = try self.scriptedMessages(),
            .delivery = .auto,
        } }, null, line_deadline_ms) catch |err| {
            try self.fail("session.message.submit.request is answered", self.reasonOf(err));
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
            try self.fail(
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
            try self.add(.{
                .name = try self.allocator.dupe(u8, "the admission repeats requested_delivery and reports a concrete effective_delivery"),
                .detail = try std.fmt.allocPrint(self.allocator, "auto resolved to {s}", .{@tagName(admission.effective_delivery)}),
            });
        }

        try self.consumeRun(line_deadline_ms);
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

    fn consumeRun(self: *Runner, line_deadline_ms: i64) !void {
        var last_sequence: u64 = 0;
        while (true) {
            const event = self.nextEvent(line_deadline_ms) catch |err| {
                try self.fail("the run reaches a terminal event", @errorName(err));
                return;
            };
            var held = event;
            defer held.deinit(self.allocator);

            if (held.sequence) |sequence| {
                if (held.run_id != null and std.mem.eql(u8, held.run_id.?, self.run_id)) {
                    if (sequence <= last_sequence) {
                        try self.fail(
                            "run events carry an advancing per-run sequence",
                            try std.fmt.allocPrint(self.allocator, "sequence {d} did not advance past {d}", .{ sequence, last_sequence }),
                        );
                        return;
                    }
                    last_sequence = sequence;
                }
            }
            if (held.payload.isTerminal()) {
                try self.add(.{
                    .name = try self.allocator.dupe(u8, "the run reaches a terminal event"),
                    .detail = try std.fmt.allocPrint(self.allocator, "settled {s}", .{held.payload.typeName()}),
                });
                return;
            }
        }
    }
};

test "a report judges itself on the first check that did not pass" {
    var report = Report{
        .allocator = std.testing.allocator,
        .endpoint = try std.testing.allocator.dupe(u8, "endpoint"),
        .checks = .empty,
    };
    defer report.deinit();
    try report.checks.append(std.testing.allocator, .{
        .name = try std.testing.allocator.dupe(u8, "capabilities.request is answered"),
    });
    try std.testing.expect(report.passed());
    try report.checks.append(std.testing.allocator, .{
        .name = try std.testing.allocator.dupe(u8, "the run reaches a terminal event"),
        .passed = false,
        .detail = try std.testing.allocator.dupe(u8, "the endpoint answered nothing"),
    });
    try std.testing.expect(!report.passed());
    try std.testing.expect((report.verdict("the run reaches a terminal event") orelse unreachable).passed == false);
    try std.testing.expect(report.verdict("no such check") == null);
}

test "a runner with no command to drive refuses rather than reporting an endpoint" {
    try std.testing.expectError(error.ConformanceNeedsCommand, run(std.testing.allocator, .{ .command = "" }));
}

test "a runner drives a child and reports the checks it could not pass" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    var report = try run(std.testing.allocator, .{ .command = "/bin/cat", .line_deadline_ms = 2000 });
    defer report.deinit();

    try std.testing.expect(!report.passed());
    const initialize = report.verdict("protocol.initialize.request is answered") orelse return error.CheckMissing;
    try std.testing.expect(!initialize.passed);
    try std.testing.expectEqualStrings("ConformanceEndpointSilent", initialize.detail);
    const exits = report.verdict("endpoint exits 0 after stdin EOF") orelse return error.CheckMissing;
    try std.testing.expect(exits.passed);
}

test "a script that answers the four requests and then a terminal event settles the run" {
    if (@import("builtin").os.tag == .windows) return error.SkipZigTest;
    const script =
        \\while read -r line; do
        \\  case "$line" in
        \\  *protocol.initialize.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"protocol.initialize.response","id":"a1","in_reply_to":"conformance-request-1","payload":{"protocol_version":"0.1","profile":"open-agent-protocol.agent-control-core","endpoint":{"id":"fake"}}}' ;;
        \\  *capabilities.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"capabilities.response","id":"a2","in_reply_to":"conformance-request-2","capability_revision":"rev-1","payload":{"endpoint":{"id":"fake"},"features":{}}}' ;;
        \\  *session.open.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.open.response","id":"a3","in_reply_to":"conformance-request-3","payload":{"session_id":"conformance","status":"idle"}}' ;;
        \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e2","sequence":2,"payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' ;;
        \\  esac
        \\done
    ;

    var report = try run(std.testing.allocator, .{
        .command = "/bin/sh",
        .args = &.{ "-c", script },
        .line_deadline_ms = 5000,
    });
    defer report.deinit();

    try std.testing.expect(report.passed());
    const terminal = report.verdict("the run reaches a terminal event") orelse return error.CheckMissing;
    try std.testing.expectEqualStrings("settled run.completed", terminal.detail);
    const naming = report.verdict("the open names the session it was asked for") orelse return error.CheckMissing;
    try std.testing.expect(naming.passed);
}
