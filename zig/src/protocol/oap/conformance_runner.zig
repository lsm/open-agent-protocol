const std = @import("std");
const oap_types = @import("types");
const oap_envelope = @import("envelope");
const endpoint_client = @import("endpoint_client");

pub const default_line_deadline_ms: u64 = 300_000;

pub const default_exit_grace_ms: i64 = 30_000;

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
    line_deadline_ms: i64 = default_line_deadline_ms,
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

    try runner.drive(options.line_deadline_ms);

    runner.client.closeStdin();
    try runner.drain(options.line_deadline_ms);
    const code = runner.client.waitExit(options.exit_grace_ms) catch |err| blk: {
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
        const owned = try self.allocator.dupe(u8, name);
        try self.add(.{ .name = owned });
    }

    fn fail(self: *Runner, name: []const u8, detail: []const u8) !void {
        const owned_name = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned_name);
        const owned_detail = try self.allocator.dupe(u8, detail);
        try self.add(.{
            .name = owned_name,
            .passed = false,
            .detail = owned_detail,
        });
    }

    fn failOwned(self: *Runner, name: []const u8, detail: []const u8) !void {
        const owned_name = try self.allocator.dupe(u8, name);
        errdefer self.allocator.free(owned_name);
        errdefer self.allocator.free(detail);
        try self.add(.{ .name = owned_name, .passed = false, .detail = detail });
    }

    fn failReason(self: *Runner, name: []const u8, err: anyerror) !void {
        try self.fail(name, self.reasonOf(err));
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
        return self.events.orderedRemove(0);
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
        var init_fields: [2][]const u8 = undefined;
        var init_built: usize = 0;
        var init_handed_off = false;
        errdefer if (!init_handed_off) {
            for (init_fields[0..init_built]) |copy| self.allocator.free(copy);
        };
        init_fields[0] = try self.allocator.dupe(u8, "conformance");
        init_built = 1;
        init_fields[1] = try self.allocator.dupe(u8, "OAP conformance runner");
        init_built = 2;
        const participant_id = init_fields[0];
        const participant_name = init_fields[1];

        const versions = try oap_types.dupeStringList(self.allocator, &.{oap_types.VERSION});
        errdefer oap_types.freeStringList(self.allocator, versions);
        const profiles = try oap_types.dupeStringList(self.allocator, &.{oap_types.PROFILE});
        errdefer oap_types.freeStringList(self.allocator, profiles);

        init_handed_off = true;
        const initialized = self.request(.{
            .initialize_request = .{
                .protocol_versions = versions,
                .profiles = profiles,
                .participant = .{ .id = participant_id, .name = participant_name },
            },
        }, null, line_deadline_ms) catch |err| {
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

        var capabilities = self.request(.capabilities_request, null, line_deadline_ms) catch |err| {
            try self.failReason("capabilities.request is answered", err);
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

        var open_handed_off = false;
        var open_session: []const u8 = undefined;
        errdefer if (!open_handed_off) self.allocator.free(open_session);
        open_session = try self.allocator.dupe(u8, self.session);
        open_handed_off = true;
        var opened = self.request(.{ .session_open_request = .{ .session_id = open_session } }, null, line_deadline_ms) catch |err| {
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
        var submit_session: []const u8 = undefined;
        errdefer if (!submit_handed_off) self.allocator.free(submit_session);
        submit_session = try self.allocator.dupe(u8, self.session);
        submit_handed_off = true;
        var admitted = self.request(.{ .message_submit_request = .{
            .session_id = submit_session,
            .messages = try self.scriptedMessages(),
            .delivery = .auto,
        } }, null, line_deadline_ms) catch |err| {
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
            const check_name = try self.allocator.dupe(u8, "the admission repeats requested_delivery and reports a concrete effective_delivery");
            errdefer self.allocator.free(check_name);
            const settled = try std.fmt.allocPrint(self.allocator, "auto resolved to {s}", .{@tagName(admission.effective_delivery)});
            try self.add(.{ .name = check_name, .detail = settled });
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

    fn drain(self: *Runner, line_deadline_ms: i64) !void {
        while (true) {
            const frame = (self.client.next(lineBudget(line_deadline_ms)) catch return) orelse return;
            if (frame == .control) continue;
            var envelope = try oap_envelope.deserializeEnvelope(frame.envelope, self.allocator);
            self.events.append(self.allocator, envelope) catch |err| {
                envelope.deinit(self.allocator);
                return err;
            };
        }
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
                    self.answerPermission(gate, line_deadline_ms) catch |err| {
                        try self.fail("a permission gate is resolvable from the stream", @errorName(err));
                        return;
                    };
                    try self.pass("a permission gate is resolvable from the stream");
                },
                .user_input_requested => |*gate| {
                    self.answerInput(gate, line_deadline_ms) catch |err| {
                        try self.fail("a user input gate is resolvable from the stream", @errorName(err));
                        return;
                    };
                    try self.pass("a user input gate is resolvable from the stream");
                },
                else => {},
            }
            if (held.payload.isTerminal()) {
                const check_name = try self.allocator.dupe(u8, "the run reaches a terminal event");
                errdefer self.allocator.free(check_name);
                const settled = try std.fmt.allocPrint(self.allocator, "settled {s}", .{held.payload.typeName()});
                try self.add(.{ .name = check_name, .detail = settled });
                return;
            }
        }
    }

    fn answerPermission(self: *Runner, gate: *const oap_types.PermissionEvent, line_deadline_ms: i64) !void {
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
        var resolved = try self.request(payload, copies[4], line_deadline_ms);
        resolved.deinit(self.allocator);
    }

    fn answerInput(self: *Runner, gate: *const oap_types.UserInputEvent, line_deadline_ms: i64) !void {
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
            const question_id = try self.allocator.dupe(u8, question.id);
            const chosen: []const []const u8 = if (question.options.len > 0) blk: {
                const first = try self.allocator.dupe(u8, question.options[0].id);
                break :blk try self.allocator.dupe([]const u8, &.{first});
            } else &.{};
            const text: ?[]const u8 = if (question.options.len > 0) null else try self.allocator.dupe(u8, "conformance");
            answers[answered] = .{ .question_id = question_id, .text = text, .selected_option_ids = chosen };
            answered += 1;
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
        var resolved = try self.request(payload, copies[4], line_deadline_ms);
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
        \\  *session.message.submit.request*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.started","id":"e1","sequence":1,"payload":{"session_id":"conformance","run_id":"run-1","status":"running"}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e2","sequence":2,"payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e2","part":{"type":"text","text":"one"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.requested","id":"e2","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","title":"Allow","choices":[{"id":"approve","label":"Approve"}]}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"content.delta","id":"e4","sequence":4,"payload":{"session_id":"conformance","run_id":"run-1","message_id":"m-e4","part":{"type":"text","text":"two"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"session.message.submit.response","id":"a4","in_reply_to":"conformance-request-4","payload":{"session_id":"conformance","accepted":true,"submission_id":"s1","requested_delivery":"auto","effective_delivery":"start","admission":"started","run_id":"run-1"}}' ;;
        \\  *choice_id*) printf '%s\n' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolved","id":"e3","payload":{"interaction_id":"p-1","requested_by":"fake","responded_by":"user","session_id":"conformance","run_id":"run-1","outcome":"resolved","choice_id":"approve","granted":true}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"run.completed","id":"e6","sequence":6,"payload":{"session_id":"conformance","run_id":"run-1","stop_reason":"end_turn","final_response":{"role":"assistant","content":"done"}}}' '{"protocol":"open-agent-protocol","version":"0.1","profile":"open-agent-protocol.agent-control-core","type":"action.permission.resolve.response","id":"a5","in_reply_to":"conformance-request-5","payload":{"interaction_id":"p-1","session_id":"conformance","run_id":"run-1","accepted":true}}' ;;
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
    const gate_check = report.verdict("a permission gate is resolvable from the stream") orelse return error.CheckMissing;
    try std.testing.expect(gate_check.passed);
    const terminal = report.verdict("the run reaches a terminal event") orelse return error.CheckMissing;
    try std.testing.expectEqualStrings("settled run.completed", terminal.detail);
}
