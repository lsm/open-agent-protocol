const std = @import("std");
const corpus = @import("corpus");
const session = @import("session");
const rpc = @import("rpc");

const Direction = enum { gateway_to_host, host_to_gateway, harness_control, native_write };

const Routing = struct { action: []const u8, direction: Direction };

const routings = [_]Routing{
    .{ .action = "auto", .direction = .gateway_to_host },
    .{ .action = "observe", .direction = .gateway_to_host },
    .{ .action = "reply", .direction = .gateway_to_host },
    .{ .action = "reply-error", .direction = .gateway_to_host },
    .{ .action = "decode-error", .direction = .gateway_to_host },
    .{ .action = "open", .direction = .host_to_gateway },
    .{ .action = "submit", .direction = .host_to_gateway },
    .{ .action = "expect-write", .direction = .native_write },
    .{ .action = "wait-submit", .direction = .harness_control },
    .{ .action = "oap-control", .direction = .harness_control },
    .{ .action = "process-exit", .direction = .harness_control },
};

fn directionOf(action: []const u8) ?Direction {
    for (routings) |entry| {
        if (std.mem.eql(u8, entry.action, action)) return entry.direction;
    }
    return null;
}

fn wireBytes(item: corpus.Step) []const u8 {
    return if (item.raw == .string) item.raw.string else item.encoded;
}

fn refusal(scratch: std.mem.Allocator, wire: []const u8) ?[]const u8 {
    var diagnostic = rpc.Diagnostic{};
    _ = rpc.parseMessage(scratch, wire, &diagnostic) catch return diagnostic.message;
    return null;
}

pub const CorpusCase = struct { id: []const u8, path: []const u8, skipped_resumes: *usize };

const resume_expectations = [_][]const u8{ "replay", "gap", "run-not-found" };

const Driver = struct {
    pub const corpus_relative = "fixtures/adapters/hermes-v2026.8.31";
    pub const blank_expectation = corpus.BlankExpectation.empty_array_only;
    pub const Reducer = session.Reducer;
    pub const Case = CorpusCase;

    pub fn open(arena: *std.heap.ArenaAllocator, case: CorpusCase) Reducer {
        _ = case;
        var reducer = Reducer.init(arena, .{});
        reducer.open();
        return reducer;
    }

    pub fn envelopes(reducer: *Reducer) []std.json.Value {
        return reducer.envelopes.items;
    }

    pub fn step(reducer: *Reducer, scratch: std.mem.Allocator, item: corpus.Step, case: CorpusCase) !corpus.Handled {
        const action = item.action;
        const direction = directionOf(action) orelse return .unhandled;
        if (item.raw == .null) {
            if (direction != .host_to_gateway) return error.OnlyAHostFrameMayBeAbsent;
            return .handled;
        }
        const wire = wireBytes(item);

        if (direction == .native_write) {
            if (refusal(scratch, wire) != null) return error.ProductionCodecRefusedCorpusFrame;
            return .handled;
        }

        if (std.mem.eql(u8, action, "decode-error")) {
            const said = refusal(scratch, wire) orelse return error.ProductionCodecAcceptedInvalidFrame;
            try reducer.transportFailed(said);
            return .handled;
        }

        if (direction == .gateway_to_host) {
            if (refusal(scratch, wire)) |_| return error.ProductionCodecRefusedCorpusFrame;
            const message = try rpc.parseMessage(scratch, wire, null);
            const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, wire, .{});
            if (admissionAnswer(parsed)) |admitted| {
                if (admitted) try reducer.admit() else reducer.refuseSubmission();
                return .handled;
            }
            try reducer.observe(message, parsed);
            return .handled;
        }

        if (std.mem.eql(u8, action, "open")) {
            if (refusal(scratch, wire) != null) return error.ProductionCodecRefusedCorpusFrame;
            return .handled;
        }
        if (std.mem.eql(u8, action, "submit")) {
            if (refusal(scratch, wire) != null) return error.ProductionCodecRefusedCorpusFrame;
            try reducer.submit();
            return .handled;
        }
        if (std.mem.eql(u8, action, "wait-submit")) return .handled;
        if (std.mem.eql(u8, action, "process-exit")) {
            const detail = if (item.raw == .object) corpus.stringMember(item.raw.object, "error") orelse "" else "";
            try reducer.transportFailed(detail);
            return .handled;
        }
        if (std.mem.eql(u8, action, "oap-control")) {
            try control(reducer, scratch, item.raw, case);
            return .handled;
        }
        return .unhandled;
    }
};

fn admissionAnswer(parsed: std.json.Value) ?bool {
    if (parsed != .object) return null;
    if (parsed.object.get("error") != null) return false;
    const result = parsed.object.get("result") orelse return null;
    if (result != .object) return null;
    const status = corpus.stringMember(result.object, "status") orelse return null;
    return std.mem.eql(u8, status, "streaming");
}

fn control(reducer: *session.Reducer, scratch: std.mem.Allocator, raw: std.json.Value, case: CorpusCase) !void {
    if (raw != .object) return error.ControlIsNotAnObject;
    const op = corpus.stringMember(raw.object, "op") orelse return error.ControlWithoutOperation;

    if (std.mem.eql(u8, op, "close")) return;

    if (std.mem.eql(u8, op, "assert-state")) {
        const want = corpus.stringMember(raw.object, "status") orelse return error.ControlWithoutStatus;
        const running = if (reducer.run) |run| run.started and !run.terminal else false;
        if (std.mem.eql(u8, want, "running")) {
            if (!running) return error.ReducerIsNotRunning;
            return;
        }
        if (std.mem.eql(u8, want, "idle")) {
            if (running) return error.ReducerIsNotIdle;
            return;
        }
        return error.UnroutedControlStatus;
    }

    if (std.mem.eql(u8, op, "resolve")) {
        const kind = corpus.stringMember(raw.object, "kind") orelse return error.ControlWithoutKind;
        const answer = corpus.stringMember(raw.object, "answer") orelse return error.ControlWithoutAnswer;
        const binding = reducer.pendingInteraction(kind) orelse return error.NoInteractionOfThatKind;
        const question = binding.questions[0];
        const built: session.Answer = if (std.mem.eql(u8, question.kind, "text"))
            .{ .question_id = question.id, .text = answer }
        else
            .{ .question_id = question.id, .selected_option_ids = try scratch.dupe([]const u8, &.{answer}) };
        try reducer.resolve(binding.id, try scratch.dupe(session.Answer, &.{built}));
        return;
    }

    const expect = corpus.stringMember(raw.object, "expect") orelse return error.ControlWithoutExpectation;

    if (std.mem.eql(u8, op, "overlap-submit")) {
        if (!std.mem.eql(u8, expect, "run-active")) return error.UnroutedControlExpectation;
        if (reducer.submit()) |_| return error.OverlapWasAccepted else |err| {
            if (err != session.Error.RunActive) return err;
        }
        return;
    }

    if (std.mem.eql(u8, op, "submit-closed")) {
        if (!std.mem.eql(u8, expect, "session-closed")) return error.UnroutedControlExpectation;
        if (reducer.submit()) |_| return error.SubmitAfterCloseWasAccepted else |err| {
            if (err != session.Error.SessionUnusable) return err;
        }
        return;
    }

    if (!std.mem.eql(u8, op, "resume")) return error.UnroutedControlOperation;
    for (resume_expectations) |known| {
        if (!std.mem.eql(u8, expect, known)) continue;
        case.skipped_resumes.* += 1;
        return;
    }
    return error.UnroutedControlExpectation;
}

pub const Harness = corpus.Harness(Driver);

const Declared = struct { id: []const u8, skipped_resumes: usize = 0 };

const cases = [_]Declared{
    .{ .id = "admission-busy" },
    .{ .id = "admission-orders" },
    .{ .id = "hygiene-globals" },
    .{ .id = "interaction-expire" },
    .{ .id = "interaction-gates" },
    .{ .id = "malformed-frame" },
    .{ .id = "pre-ready-observation" },
    .{ .id = "process-exit" },
    .{ .id = "ready-handshake" },
    .{ .id = "reconciliation" },
    .{ .id = "recovery-journal", .skipped_resumes = 3 },
    .{ .id = "replay-epoch", .skipped_resumes = 3 },
    .{ .id = "settlement-statuses" },
    .{ .id = "side-channels" },
    .{ .id = "steering-unavailable", .skipped_resumes = 1 },
    .{ .id = "streaming-provenance" },
    .{ .id = "subagent-frames" },
    .{ .id = "tool-lifecycle" },
};

test "the Hermes case list is exactly the corpus manifest's" {
    var listed: [cases.len]corpus.CaseEntry = undefined;
    for (&listed, cases) |*entry, declared| entry.* = .{ .id = declared.id, .path = declared.id };
    try Harness.expectInventory(std.testing.allocator, &listed);
}

test "the Zig reducer reproduces every Hermes expectation, all eighteen of them" {
    const allocator = std.testing.allocator;
    const root = try Harness.root(allocator);
    defer allocator.free(root);
    var failed: usize = 0;
    for (cases) |declared| {
        var skipped: usize = 0;
        const case = CorpusCase{ .id = declared.id, .path = declared.id, .skipped_resumes = &skipped };
        const outcome = Harness.runCase(allocator, root, case) catch |err| {
            std.debug.print("V|{s}: {s}\n", .{ case.id, @errorName(err) });
            failed += 1;
            continue;
        };
        if (outcome.expected != outcome.emitted or outcome.first_mismatch != null) {
            std.debug.print("V|{s}: expected={d} emitted={d} mismatch={?d}\n", .{ case.id, outcome.expected, outcome.emitted, outcome.first_mismatch });
            failed += 1;
            continue;
        }
        if (skipped != declared.skipped_resumes) {
            std.debug.print("V|{s}: skipped {d} resume ops, declared {d}\n", .{ case.id, skipped, declared.skipped_resumes });
            failed += 1;
            continue;
        }
        std.debug.print("V|{s}: EXACT {d}, skipped {d} resume ops\n", .{ case.id, outcome.expected, skipped });
    }
    if (failed != 0) return error.CorpusMismatch;
}
