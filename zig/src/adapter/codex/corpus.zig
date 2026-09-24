const std = @import("std");
const rpc = @import("rpc");
const native = @import("native");
const session = @import("session");
const corpus = @import("adapter_corpus");
const semantic = @import("semantic");
const jsonschema = @import("jsonschema");

const corpus_relative = "fixtures/adapters/codex-appserver";
const writes_relative = "fixtures/adapters/codex-appserver-writes/conversation.json";
const schema_tree_sha256 = "d31125f254f93a9c6300e50c86ffbd3cc6ad388ef5b8833ecbd0a47371a344b6";
const participant = "user";

const Case = struct {
    id: []const u8,
    path: []const u8,
    ledger_fixture: []const u8,
};

const claimed_cases = [_]Case{
    .{ .id = "completed-text", .path = "cases/completed-text", .ledger_fixture = "completed-text" },
    .{ .id = "failed-turn", .path = "cases/failed-turn", .ledger_fixture = "failed-turn" },
    .{ .id = "interrupted-turn", .path = "cases/interrupted-turn", .ledger_fixture = "interrupted-turn" },
    .{ .id = "command-completed", .path = "cases/command-completed", .ledger_fixture = "command-completed" },
    .{ .id = "file-change-completed", .path = "cases/file-change-completed", .ledger_fixture = "file-change-completed" },
    .{ .id = "mcp-completed", .path = "cases/mcp-completed", .ledger_fixture = "mcp-completed" },
    .{ .id = "command-approval", .path = "cases/command-approval", .ledger_fixture = "command-approval" },
    .{ .id = "file-approval", .path = "cases/file-approval", .ledger_fixture = "file-approval" },
    .{ .id = "user-input", .path = "cases/user-input", .ledger_fixture = "user-input" },
    .{ .id = "permissions-approval-unsupported", .path = "cases/permissions-approval-unsupported", .ledger_fixture = "permissions-approval" },
    .{ .id = "duplicate-terminal", .path = "cases/duplicate-terminal", .ledger_fixture = "duplicate-terminal" },
    .{ .id = "process-exit", .path = "cases/process-exit", .ledger_fixture = "process-exit" },
    .{ .id = "model-per-turn", .path = "cases/model-per-turn", .ledger_fixture = "model-per-turn" },
};

const thread_result = "{\"thread\":{\"id\":\"native-thread\"}}";
const turn_result = "{\"turn\":{\"id\":\"native-turn\",\"status\":\"inProgress\"}}";
const started_frame = "{\"method\":\"turn/started\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"inProgress\"}}}";

fn readFile(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(16 * 1024 * 1024));
}

fn parseDocument(arena: std.mem.Allocator, text: []const u8) !std.json.Value {
    return std.json.parseFromSliceLeaky(std.json.Value, arena, text, .{});
}

fn member(value: std.json.Value, name: []const u8) ?std.json.Value {
    if (value != .object) return null;
    return value.object.get(name);
}

fn textMember(value: std.json.Value, name: []const u8) []const u8 {
    const found = member(value, name) orelse return "";
    return if (found == .string) found.string else "";
}

fn integerMember(value: std.json.Value, name: []const u8) i64 {
    const found = member(value, name) orelse return 0;
    return if (found == .integer) found.integer else 0;
}

fn checkManifest(manifest: std.json.Value, claimed: []const Case) !void {
    if (integerMember(manifest, "version") != 1) return error.ManifestPinMismatch;
    if (!std.mem.eql(u8, textMember(manifest, "adapter"), session.adapter_name)) return error.ManifestPinMismatch;
    if (!std.mem.eql(u8, textMember(manifest, "codex_commit"), session.codex_commit)) return error.ManifestPinMismatch;
    if (!std.mem.eql(u8, textMember(manifest, "schema_tree_sha256"), schema_tree_sha256)) return error.ManifestPinMismatch;
    const listed = member(manifest, "cases") orelse return error.CaseListDiffers;
    if (listed != .array or listed.array.items.len != claimed.len) return error.CaseListDiffers;
    for (listed.array.items, claimed) |entry, case| {
        if (!std.mem.eql(u8, textMember(entry, "id"), case.id)) return error.CaseListDiffers;
        if (!std.mem.eql(u8, textMember(entry, "path"), case.path)) return error.CaseListDiffers;
        const fixtures = member(entry, "ledger_fixtures") orelse return error.CaseListDiffers;
        if (fixtures != .array or fixtures.array.items.len != 1) return error.CaseListDiffers;
        if (fixtures.array.items[0] != .string or !std.mem.eql(u8, fixtures.array.items[0].string, case.ledger_fixture)) return error.CaseListDiffers;
    }
}

fn loadManifest(arena: std.mem.Allocator) !std.json.Value {
    const root = try corpus.corpusRoot(arena, corpus_relative);
    return parseDocument(arena, try readFile(arena, try std.fs.path.join(arena, &.{ root, "manifest.json" })));
}

const Frame = struct {
    line: []const u8,
    script: std.json.Value,
    direction: []const u8,
    kind: []const u8,
    method: []const u8,
    classification: []const u8,
    fidelity: []const u8,
    decoded: ?rpc.Message = null,

    fn notification(self: Frame) bool {
        return std.mem.eql(u8, self.direction, "server_to_client") and std.mem.eql(u8, self.kind, "notification");
    }

    fn request(self: Frame) bool {
        return std.mem.eql(u8, self.direction, "server_to_client") and std.mem.eql(u8, self.kind, "request");
    }

    fn transport(self: Frame) bool {
        return std.mem.eql(u8, self.direction, "process") and std.mem.eql(u8, self.kind, "transport");
    }

    fn awaited(self: Frame) usize {
        const declared = integerMember(self.script, "await_events");
        return if (declared > 0) @intCast(declared) else 0;
    }
};

fn decodeFrame(arena: std.mem.Allocator, frame: *Frame) !void {
    if (!frame.notification() and !frame.request()) return;
    var params: ?std.json.Value = null;
    if (member(frame.script, "params") != null) {
        const source = try corpus.memberSource(arena, frame.line, "params");
        params = try std.json.parseFromSliceLeaky(std.json.Value, arena, source, .{ .parse_numbers = false });
    }
    const wire = if (frame.request()) blk: {
        const id = integerMember(frame.script, "id");
        if (id == 0) return error.RequestWithoutID;
        break :blk try rpc.encode(arena, .{ .request = .{ .id = id, .method = frame.method, .params = params } });
    } else try rpc.encode(arena, .{ .notification = .{ .method = frame.method, .params = params } });
    var decoder = rpc.Decoder{ .source = try std.mem.concat(arena, u8, &.{ wire, "\n" }) };
    const message = (try decoder.next(arena)) orelse return error.ProductionCodecYieldedNoFrame;
    if (message.kind != (if (frame.request()) rpc.Kind.request else rpc.Kind.notification)) return error.DecodedKindDiffers;
    if (!std.mem.eql(u8, message.method, frame.method)) return error.DecodedMethodDiffers;
    if (frame.request() and !message.id.?.eql(.{ .integer = integerMember(frame.script, "id") })) return error.DecodedIDDiffers;
    frame.decoded = message;
}

fn loadFrames(arena: std.mem.Allocator, text: []const u8) ![]Frame {
    var frames = std.ArrayList(Frame).empty;
    var lines = std.mem.splitScalar(u8, text, '\n');
    while (lines.next()) |line| {
        if (line.len == 0) continue;
        const script = try parseDocument(arena, line);
        var frame = Frame{
            .line = line,
            .script = script,
            .direction = textMember(script, "direction"),
            .kind = textMember(script, "kind"),
            .method = textMember(script, "method"),
            .classification = textMember(script, "classification"),
            .fidelity = textMember(script, "fidelity"),
        };
        try decodeFrame(arena, &frame);
        try frames.append(arena, frame);
    }
    if (frames.items.len == 0) return error.NoNativeFrames;
    return frames.items;
}

const classifications = [_][]const u8{ "mapped", "required-unmapped", "unsupported-request", "observed-only" };
const fidelities = [_][]const u8{ "native", "normalized", "synthesized", "lossy", "unsupported" };

fn named(names: []const []const u8, candidate: []const u8) bool {
    for (names) |name| {
        if (std.mem.eql(u8, name, candidate)) return true;
    }
    return false;
}

fn checkClassifications(frames: []const Frame, mappings: std.json.Value, omissions: std.json.Value) !void {
    if (mappings != .array or mappings.array.items.len != frames.len) return error.MappingCountDiffers;
    if (omissions != .array) return error.InvalidOmissions;
    for (omissions.array.items, 0..) |omission, position| {
        const index = integerMember(omission, "index");
        if (index < 1 or index > frames.len) return error.InvalidOmission;
        if (textMember(omission, "reason").len == 0) return error.InvalidOmission;
        if (!std.mem.eql(u8, textMember(omission, "method"), frames[@intCast(index - 1)].method)) return error.InvalidOmission;
        for (omissions.array.items[0..position]) |earlier| {
            if (integerMember(earlier, "index") == index) return error.DuplicateOmission;
        }
    }
    for (frames, mappings.array.items, 1..) |frame, mapping, index| {
        if (integerMember(mapping, "index") != index) return error.MappingDiffers;
        if (!std.mem.eql(u8, textMember(mapping, "method"), frame.method)) return error.MappingDiffers;
        if (!std.mem.eql(u8, textMember(mapping, "classification"), frame.classification)) return error.MappingDiffers;
        if (!std.mem.eql(u8, textMember(mapping, "fidelity"), frame.fidelity)) return error.MappingDiffers;
        if (!named(&classifications, frame.classification)) return error.InvalidClassification;
        if (!named(&fidelities, frame.fidelity)) return error.InvalidFidelity;
        var omitted = false;
        for (omissions.array.items) |omission| {
            if (integerMember(omission, "index") == index) omitted = true;
        }
        const observed_only = std.mem.eql(u8, frame.classification, "observed-only");
        if (observed_only != omitted) return error.OmissionLedgerDiffers;
    }
}

const Reader = struct {
    reducer: *session.Reducer,
    cursor: usize = 0,

    fn next(self: *Reader) !std.json.Value {
        if (self.cursor >= self.reducer.envelopes.items.len) return error.EventNotEmitted;
        defer self.cursor += 1;
        return self.reducer.envelopes.items[self.cursor];
    }
};

fn feed(reducer: *session.Reducer, line: []const u8) !void {
    try reducer.observe(try rpc.parseMessage(reducer.arena.allocator(), line));
}

fn answerLastCall(reducer: *session.Reducer, result: []const u8) !void {
    const arena = reducer.arena.allocator();
    const written = try rpc.parseMessage(arena, reducer.writes.items[reducer.writes.items.len - 1]);
    if (written.kind != .request) return error.NoCallToAnswer;
    try feed(reducer, try std.fmt.allocPrint(arena, "{{\"id\":{d},\"result\":{s}}}", .{ written.id.?.integer, result }));
}

fn lastAdmission(reducer: *session.Reducer) !session.Admission {
    const settled = reducer.settled.items;
    if (settled.len == 0 or settled[settled.len - 1] != .admitted) return error.SubmissionNotAdmitted;
    return settled[settled.len - 1].admitted;
}

fn interactionAmong(events: []const std.json.Value) ?[]const u8 {
    var found: ?[]const u8 = null;
    for (events) |event| {
        const kind = textMember(event, "type");
        if (!std.mem.eql(u8, kind, "action.permission.requested") and !std.mem.eql(u8, kind, "user.input.requested")) continue;
        found = textMember(member(event, "payload").?, "interaction_id");
    }
    return found;
}

fn answersOf(arena: std.mem.Allocator, resolution: std.json.Value) ![]session.Answer {
    const listed = member(resolution, "answers") orelse return &.{};
    const answers = try arena.alloc(session.Answer, listed.array.items.len);
    for (listed.array.items, answers) |entry, *answer| {
        var selected = std.ArrayList([]const u8).empty;
        if (member(entry, "selected_option_ids")) |ids| {
            for (ids.array.items) |id| try selected.append(arena, id.string);
        }
        answer.* = .{ .question_id = textMember(entry, "question_id"), .text = textMember(entry, "text"), .selected_option_ids = selected.items };
    }
    return answers;
}

fn nativeResponse(reducer: *session.Reducer, from: usize, id: i64) !std.json.Value {
    const arena = reducer.arena.allocator();
    var found: ?std.json.Value = null;
    for (reducer.writes.items[from..]) |write| {
        const written = try parseDocument(arena, write);
        const carried = member(written, "id") orelse continue;
        if (member(written, "method") != null or carried != .integer or carried.integer != id) continue;
        if (found != null) return error.RequestAnsweredTwice;
        found = written;
    }
    return found orelse error.RequestNeverAnswered;
}

fn runRequest(reducer: *session.Reducer, reader: *Reader, frame: Frame, admission: session.Admission) !void {
    const arena = reducer.arena.allocator();
    const id = integerMember(frame.script, "id");
    if (id == 0 or frame.method.len == 0) return error.InvalidReverseRequestFixture;
    const writes = reducer.writes.items.len;
    try reducer.observe(frame.decoded.?);
    const count = @max(frame.awaited(), 1);
    const first = reader.cursor;
    for (0..count) |_| _ = try reader.next();
    if (member(frame.script, "resolve")) |resolution| {
        const interaction = interactionAmong(reducer.envelopes.items[first..reader.cursor]) orelse return error.NoPortableInteraction;
        const kind = textMember(resolution, "kind");
        if (std.mem.eql(u8, kind, "permission")) {
            const granted = member(resolution, "granted") orelse std.json.Value{ .bool = false };
            try reducer.resolve(.{ .run_id = admission.run_id, .responded_by = participant, .permission = .{
                .interaction_id = interaction,
                .requested_by = session.endpoint_id,
                .responded_by = participant,
                .session_id = admission.session_id,
                .run_id = admission.run_id,
                .choice_id = textMember(resolution, "choice_id"),
                .granted = granted == .bool and granted.bool,
            } });
            _ = try reader.next();
        } else if (std.mem.eql(u8, kind, "input")) {
            try reducer.resolve(.{ .run_id = admission.run_id, .responded_by = participant, .input = .{
                .interaction_id = interaction,
                .requested_by = session.endpoint_id,
                .responded_by = participant,
                .session_id = admission.session_id,
                .run_id = admission.run_id,
                .answers = try answersOf(arena, resolution),
            } });
            _ = try reader.next();
            _ = try reader.next();
        } else {
            return error.UnsupportedResolutionKind;
        }
    }
    const response = try nativeResponse(reducer, writes, id);
    if (member(frame.script, "expected_result")) |expected| {
        const result = member(response, "result") orelse return error.NativeResponseDiffers;
        if (!corpus.equalValues(expected, result)) return error.NativeResponseDiffers;
        return;
    }
    if (member(frame.script, "expected_error")) |expected| {
        const failure = member(response, "error") orelse return error.NativeResponseDiffers;
        if (integerMember(failure, "code") != integerMember(expected, "code")) return error.NativeResponseDiffers;
        if (!std.mem.eql(u8, textMember(failure, "message"), textMember(expected, "message"))) return error.NativeResponseDiffers;
        return;
    }
    return error.RequestFixtureWithoutExpectation;
}

fn compact(arena: std.mem.Allocator, text: []const u8) ![]u8 {
    var out = std.ArrayList(u8).empty;
    var in_string = false;
    var escaped = false;
    for (text) |byte| {
        if (in_string) {
            try out.append(arena, byte);
            if (escaped) {
                escaped = false;
            } else if (byte == '\\') {
                escaped = true;
            } else if (byte == '"') {
                in_string = false;
            }
            continue;
        }
        switch (byte) {
            ' ', '\t', '\n', '\r' => {},
            '"' => {
                in_string = true;
                try out.append(arena, byte);
            },
            else => try out.append(arena, byte),
        }
    }
    return out.items;
}

fn object() std.json.ObjectMap {
    return .empty;
}

fn envelope(arena: std.mem.Allocator, kind: []const u8, id: []const u8, payload: std.json.Value) !std.json.ObjectMap {
    var value = object();
    try value.put(arena, "protocol", .{ .string = session.protocol_name });
    try value.put(arena, "version", .{ .string = session.protocol_version });
    try value.put(arena, "profile", .{ .string = session.profile });
    try value.put(arena, "type", .{ .string = kind });
    try value.put(arena, "id", .{ .string = id });
    try value.put(arena, "payload", payload);
    return value;
}

fn submitPayload(arena: std.mem.Allocator, session_id: []const u8, text: []const u8, model_id: []const u8) !std.json.Value {
    var message = object();
    try message.put(arena, "role", .{ .string = "user" });
    try message.put(arena, "content", .{ .string = text });
    var messages = std.json.Array.init(arena);
    try messages.append(.{ .object = message });
    var payload = object();
    try payload.put(arena, "session_id", .{ .string = session_id });
    try payload.put(arena, "messages", .{ .array = messages });
    try payload.put(arena, "delivery", .{ .string = "auto" });
    if (model_id.len != 0) try payload.put(arena, "model_id", .{ .string = model_id });
    return .{ .object = payload };
}

fn admissionPayload(arena: std.mem.Allocator, admission: session.Admission) !std.json.Value {
    var ids = std.json.Array.init(arena);
    for (admission.message_ids) |id| try ids.append(.{ .string = id });
    var payload = object();
    try payload.put(arena, "session_id", .{ .string = admission.session_id });
    try payload.put(arena, "accepted", .{ .bool = true });
    try payload.put(arena, "submission_id", .{ .string = admission.submission_id });
    try payload.put(arena, "requested_delivery", .{ .string = "auto" });
    try payload.put(arena, "effective_delivery", .{ .string = "start" });
    try payload.put(arena, "delivery_resolution", .{ .string = "session_idle" });
    try payload.put(arena, "admission", .{ .string = "started" });
    try payload.put(arena, "run_id", .{ .string = admission.run_id });
    try payload.put(arena, "status", .{ .string = "running" });
    if (admission.model_id.len != 0) try payload.put(arena, "model_id", .{ .string = admission.model_id });
    try payload.put(arena, "message_ids", .{ .array = ids });
    return .{ .object = payload };
}

fn cancelCut(events: []const std.json.Value) usize {
    for (events, 0..) |event, index| {
        if (!std.mem.eql(u8, textMember(event, "type"), "run.status.updated")) continue;
        if (std.mem.eql(u8, textMember(member(event, "payload").?, "status"), "cancelling")) return index;
    }
    var index = events.len;
    while (index > 0) {
        index -= 1;
        if (terminalType(textMember(events[index], "type"))) return index;
    }
    return events.len;
}

fn terminalType(kind: []const u8) bool {
    return std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled");
}

const TraceShape = struct {
    submitted_text: []const u8,
    submitted_model: []const u8,
    cancelled: bool,
};

fn protocolTrace(arena: std.mem.Allocator, admission: session.Admission, events: []const std.json.Value, shape: TraceShape) ![]std.json.Value {
    var trace = std.ArrayList(std.json.Value).empty;
    const capabilities_request = try envelope(arena, "capabilities.request", "capabilities-request", .{ .object = object() });
    var capabilities_response = try envelope(arena, "capabilities.response", "capabilities-response", try session.descriptor(arena));
    try capabilities_response.put(arena, "in_reply_to", .{ .string = "capabilities-request" });
    try capabilities_response.put(arena, "capability_revision", .{ .string = session.capability_revision });
    try trace.append(arena, .{ .object = capabilities_request });
    try trace.append(arena, .{ .object = capabilities_response });

    var submit = try envelope(arena, "session.message.submit.request", "submit-request", try submitPayload(arena, admission.session_id, shape.submitted_text, shape.submitted_model));
    try submit.put(arena, "session_id", .{ .string = admission.session_id });
    try submit.put(arena, "capability_revision", .{ .string = session.capability_revision });
    var response = try envelope(arena, "session.message.submit.response", "submit-response", try admissionPayload(arena, admission));
    try response.put(arena, "in_reply_to", .{ .string = "submit-request" });
    try response.put(arena, "session_id", .{ .string = admission.session_id });
    try response.put(arena, "capability_revision", .{ .string = session.capability_revision });
    try trace.append(arena, .{ .object = submit });
    try trace.append(arena, .{ .object = response });

    if (!shape.cancelled) {
        try trace.appendSlice(arena, events);
        return trace.items;
    }
    var scope = object();
    try scope.put(arena, "session_id", .{ .string = admission.session_id });
    try scope.put(arena, "run_id", .{ .string = admission.run_id });
    var cancel = try envelope(arena, "run.cancel.request", "cancel-request", .{ .object = scope });
    try cancel.put(arena, "session_id", .{ .string = admission.session_id });
    try cancel.put(arena, "run_id", .{ .string = admission.run_id });
    var acknowledgement = object();
    try acknowledgement.put(arena, "session_id", .{ .string = admission.session_id });
    try acknowledgement.put(arena, "run_id", .{ .string = admission.run_id });
    try acknowledgement.put(arena, "accepted", .{ .bool = true });
    try acknowledgement.put(arena, "status", .{ .string = "cancelling" });
    var ack = try envelope(arena, "run.cancel.response", "cancel-response", .{ .object = acknowledgement });
    try ack.put(arena, "in_reply_to", .{ .string = "cancel-request" });
    try ack.put(arena, "session_id", .{ .string = admission.session_id });
    try ack.put(arena, "run_id", .{ .string = admission.run_id });
    const cut = cancelCut(events);
    try trace.appendSlice(arena, events[0..cut]);
    try trace.append(arena, .{ .object = cancel });
    try trace.append(arena, .{ .object = ack });
    try trace.appendSlice(arena, events[cut..]);
    return trace.items;
}

fn checkRunInvariants(admission: session.Admission, events: []const std.json.Value) !void {
    var terminals: usize = 0;
    for (events, 1..) |event, sequence| {
        if (!std.mem.eql(u8, textMember(event, "session_id"), admission.session_id)) return error.EventScopeDiffers;
        if (!std.mem.eql(u8, textMember(event, "run_id"), admission.run_id)) return error.EventScopeDiffers;
        if (integerMember(event, "sequence") != sequence) return error.SequenceNotContiguous;
        if (!std.mem.eql(u8, textMember(event, "capability_revision"), session.capability_revision)) return error.RevisionDiffers;
        if (!terminalType(textMember(event, "type"))) continue;
        terminals += 1;
        if (sequence != events.len) return error.TerminalNotLast;
    }
    if (terminals != 1) return error.TerminalCountDiffers;
}

fn validateTrace(allocator: std.mem.Allocator, registry: *const jsonschema.Registry, arena: std.mem.Allocator, trace: []const std.json.Value) !void {
    const wire = try rpc.encodeValues(arena, trace);
    const reparsed = try parseDocument(arena, wire);
    var validator = jsonschema.Validator.init(allocator, registry);
    defer validator.deinit();
    for (reparsed.array.items, 0..) |value, index| {
        if (try validator.validate("envelope.schema.json", value)) |failure| {
            std.debug.print("\nenvelope {d} is schema-invalid at {s} ({s})\n", .{ index, failure.pointer, failure.keyword });
            return error.SchemaInvalidEnvelope;
        }
    }
    var machine = semantic.Machine.init(allocator);
    defer machine.deinit();
    for (reparsed.array.items, 0..) |value, index| try machine.apply(index, value);
    try machine.close();
    if (machine.diagnostics.items.len == 0) return;
    for (machine.diagnostics.items) |diagnostic| std.debug.print("\nsemantic {s} at {d}\n", .{ diagnostic.code, diagnostic.index });
    return error.SemanticallyInvalidTrace;
}

pub const Outcome = struct {
    expected: usize,
    emitted: usize,
    identical: bool,
};

fn runCase(allocator: std.mem.Allocator, registry: *const jsonschema.Registry, root: []const u8, case: Case) !Outcome {
    var holder = std.heap.ArenaAllocator.init(allocator);
    defer holder.deinit();
    const arena = holder.allocator();
    const dir = try std.fs.path.join(arena, &.{ root, case.path });
    const definition = try parseDocument(arena, try readFile(arena, try std.fs.path.join(arena, &.{ dir, "case.json" })));
    if (integerMember(definition, "version") != 1 or !std.mem.eql(u8, textMember(definition, "id"), case.id)) return error.CaseMetadataDiffers;
    const names = [_][]const u8{ "native", "expected_oap", "mapping", "omissions" };
    var paths: [names.len][]const u8 = undefined;
    for (names, &paths) |name, *path| {
        const file = textMember(definition, name);
        if (file.len == 0 or !std.mem.eql(u8, std.fs.path.basename(file), file) or std.mem.eql(u8, file, "..")) return error.CaseFileEscapes;
        path.* = try std.fs.path.join(arena, &.{ dir, file });
    }
    const frames = try loadFrames(arena, try readFile(arena, paths[0]));
    try checkClassifications(frames, try parseDocument(arena, try readFile(arena, paths[2])), try parseDocument(arena, try readFile(arena, paths[3])));

    var reducer = session.Reducer.init(&holder, .{ .session_id = "session-1", .participant = participant, .model = "glm-test", .id_width = 2 });
    try reducer.open();
    try answerLastCall(&reducer, thread_result);
    const model_id = textMember(definition, "model_id");
    try reducer.submit(.{ .messages = &.{.{ .text = "hello" }}, .model_id = if (model_id.len != 0) model_id else null });
    const turn_start = try parseDocument(arena, reducer.writes.items[reducer.writes.items.len - 1]);
    try answerLastCall(&reducer, turn_result);
    const admission = try lastAdmission(&reducer);
    if (model_id.len != 0) {
        if (!std.mem.eql(u8, textMember(member(turn_start, "params").?, "model"), model_id)) return error.TurnStartModelDiffers;
        if (!std.mem.eql(u8, admission.model_id, model_id)) return error.AdmissionModelDiffers;
        if (!std.mem.eql(u8, reducer.state.current_model_id, "glm-test")) return error.PerRunModelMovedSessionDefault;
    }

    var reader = Reader{ .reducer = &reducer };
    var remaining = frames;
    const cancelled = std.mem.eql(u8, case.id, "interrupted-turn");
    if (std.mem.eql(u8, case.id, "process-exit") or cancelled) {
        try feed(&reducer, started_frame);
        _ = try reader.next();
    }
    if (cancelled) {
        if (try reducer.cancel(admission.run_id) != null) return error.CancelSettledWithoutInterrupt;
        try answerLastCall(&reducer, "{}");
        _ = try reader.next();
        remaining = frames[1..];
    }
    for (remaining) |frame| {
        const before = reducer.envelopes.items.len;
        if (frame.notification()) {
            try reducer.observe(frame.decoded.?);
            for (0..frame.awaited()) |_| _ = try reader.next();
        } else if (frame.request()) {
            try runRequest(&reducer, &reader, frame, admission);
        } else if (frame.transport()) {
            try reducer.transportFailed(textMember(frame.script, "error"));
        } else {
            return error.UnsupportedFrameDirection;
        }
        if (std.mem.eql(u8, frame.classification, "observed-only") and reducer.envelopes.items.len != before) return error.ObservedOnlyFrameEmitted;
    }
    const events = reducer.envelopes.items;
    try checkRunInvariants(admission, events);
    try validateTrace(allocator, registry, arena, try protocolTrace(arena, admission, events, .{
        .submitted_text = if (cancelled) "adaptertest" else "hello",
        .submitted_model = if (cancelled) "" else model_id,
        .cancelled = cancelled,
    }));

    const expected_text = try readFile(arena, paths[1]);
    if (corpus.isBlank(.zero_byte_or_empty_array, expected_text)) return error.BlankExpectation;
    const expected = try corpus.expectedEnvelopes(arena, expected_text);
    const want = try compact(arena, expected_text);
    const got = try rpc.encodeValues(arena, events);
    const identical = std.mem.eql(u8, want, got);
    if (!identical) std.debug.print("\n{s}: trace differs\nwant: {s}\ngot:  {s}\n", .{ case.id, want, got });
    return .{ .expected = expected.len, .emitted = events.len, .identical = identical };
}

test "the claimed case list is the manifest's, in order, and the pins match the port" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    try checkManifest(try loadManifest(scratch), &claimed_cases);
    var listed: [claimed_cases.len]corpus.CaseEntry = undefined;
    for (&listed, claimed_cases) |*entry, case| entry.* = .{ .id = case.id, .path = case.path };
    const root = try corpus.corpusRoot(scratch, corpus_relative);
    const manifest_text = try readFile(scratch, try std.fs.path.join(scratch, &.{ root, "manifest.json" }));
    const findings = try corpus.inventoryFindings(scratch, manifest_text, &listed, &.{});
    for (findings) |finding| std.debug.print("\n{s}: {s}\n", .{ corpus_relative, finding });
    try std.testing.expectEqual(@as(usize, 0), findings.len);
}

test "a case dropped from, or reordered in, the claimed list fails the manifest check" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const manifest = try loadManifest(arena.allocator());
    try std.testing.expectError(error.CaseListDiffers, checkManifest(manifest, claimed_cases[1..]));
    var swapped = claimed_cases;
    std.mem.swap(Case, &swapped[0], &swapped[1]);
    try std.testing.expectError(error.CaseListDiffers, checkManifest(manifest, &swapped));
    var renamed = claimed_cases;
    renamed[9].ledger_fixture = "permissions-approval-unsupported";
    try std.testing.expectError(error.CaseListDiffers, checkManifest(manifest, &renamed));
}

test "every Codex corpus case reproduces its expected trace byte for byte and passes the validator" {
    const allocator = std.testing.allocator;
    const root = try corpus.corpusRoot(allocator, corpus_relative);
    defer allocator.free(root);
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    var failures: usize = 0;
    for (claimed_cases) |case| {
        const outcome = runCase(allocator, &registry, root, case) catch |err| {
            std.debug.print("\n{s}: {s}\n", .{ case.id, @errorName(err) });
            failures += 1;
            continue;
        };
        if (outcome.identical and outcome.expected == outcome.emitted) continue;
        std.debug.print("\n{s}: expected {d} envelopes, emitted {d}\n", .{ case.id, outcome.expected, outcome.emitted });
        failures += 1;
    }
    try std.testing.expectEqual(@as(usize, 0), failures);
}

test "a trace the shared validator refuses fails the case rather than passing through" {
    const allocator = std.testing.allocator;
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    var holder = std.heap.ArenaAllocator.init(allocator);
    defer holder.deinit();
    const arena = holder.allocator();
    var reducer = session.Reducer.init(&holder, .{ .session_id = "session-1", .participant = participant, .model = "glm-test", .id_width = 2 });
    try reducer.open();
    try answerLastCall(&reducer, thread_result);
    try reducer.submit(.{ .messages = &.{.{ .text = "hello" }} });
    try answerLastCall(&reducer, turn_result);
    const admission = try lastAdmission(&reducer);
    const shape = TraceShape{ .submitted_text = "hello", .submitted_model = "", .cancelled = false };
    try feed(&reducer, started_frame);
    try std.testing.expectError(error.SemanticallyInvalidTrace, validateTrace(allocator, &registry, arena, try protocolTrace(arena, admission, reducer.envelopes.items, shape)));
    try feed(&reducer, "{\"method\":\"turn/completed\",\"params\":{\"threadId\":\"native-thread\",\"turn\":{\"id\":\"native-turn\",\"status\":\"completed\"}}}");
    try validateTrace(allocator, &registry, arena, try protocolTrace(arena, admission, reducer.envelopes.items, shape));
    try reducer.envelopes.items[0].object.put(arena, "sequence", .{ .string = "1" });
    try std.testing.expectError(error.SchemaInvalidEnvelope, validateTrace(allocator, &registry, arena, try protocolTrace(arena, admission, reducer.envelopes.items, shape)));
}

fn decodeLine(line: []const u8) !void {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const frames = try loadFrames(arena.allocator(), line);
    if (frames[0].decoded == null) return error.LineNotDecoded;
}

test "every native line is encoded and decoded by the production codec before it reaches the reducer" {
    try decodeLine("{\"direction\":\"server_to_client\",\"kind\":\"notification\",\"method\":\"turn/started\",\"params\":{\"threadId\":\"t\"}}");
    try decodeLine("{\"direction\":\"server_to_client\",\"kind\":\"request\",\"id\":3,\"method\":\"item/tool/requestUserInput\"}");
    try std.testing.expectError(error.RequestWithoutID, decodeLine("{\"direction\":\"server_to_client\",\"kind\":\"request\",\"method\":\"item/tool/requestUserInput\",\"params\":{}}"));
    try std.testing.expectError(rpc.Error.InvalidMessage, decodeLine("{\"direction\":\"server_to_client\",\"kind\":\"notification\",\"method\":\"\"}"));

    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    var deep = std.ArrayList(u8).empty;
    try deep.appendSlice(arena.allocator(), "{\"direction\":\"server_to_client\",\"kind\":\"notification\",\"method\":\"x\",\"params\":");
    try deep.appendNTimes(arena.allocator(), '[', 10000);
    try deep.appendNTimes(arena.allocator(), ']', 10000);
    try deep.append(arena.allocator(), '}');
    try std.testing.expectError(rpc.Error.InvalidMessage, decodeLine(deep.items));
}

test "a mapping that disagrees with its frame, or an observed-only frame without an omission, fails the case" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const a = arena.allocator();
    const frames = try loadFrames(a, "{\"direction\":\"server_to_client\",\"kind\":\"notification\",\"method\":\"turn/completed\",\"classification\":\"observed-only\",\"fidelity\":\"synthesized\",\"params\":{}}");
    const mapped = try parseDocument(a, "[{\"index\":1,\"method\":\"turn/completed\",\"classification\":\"observed-only\",\"fidelity\":\"synthesized\"}]");
    try checkClassifications(frames, mapped, try parseDocument(a, "[{\"index\":1,\"method\":\"turn/completed\",\"reason\":\"late\"}]"));
    try std.testing.expectError(error.OmissionLedgerDiffers, checkClassifications(frames, mapped, try parseDocument(a, "[]")));
    try std.testing.expectError(error.InvalidOmission, checkClassifications(frames, mapped, try parseDocument(a, "[{\"index\":1,\"method\":\"turn/completed\",\"reason\":\"\"}]")));
    try std.testing.expectError(error.MappingDiffers, checkClassifications(frames, try parseDocument(a, "[{\"index\":1,\"method\":\"turn/completed\",\"classification\":\"mapped\",\"fidelity\":\"synthesized\"}]"), try parseDocument(a, "[]")));
    try std.testing.expectError(error.MappingCountDiffers, checkClassifications(frames, try parseDocument(a, "[]"), try parseDocument(a, "[]")));
}

test "compaction drops only the whitespace outside strings" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    try std.testing.expectEqualStrings("[{\"a\":\"x y\\\" z\",\"b\":[1,2]}]", try compact(arena.allocator(), "[\n  {\n    \"a\": \"x y\\\" z\",\n    \"b\": [ 1, 2 ]\n  }\n]\n"));
}

const outbound_text = "hello <codex> & friends\u{2028}\u{e9}";

const Conversation = struct {
    client: []const []const u8,
    server: []const []const u8,
    capabilities: []const u8,
};

fn loadConversation(arena: std.mem.Allocator) !Conversation {
    const path = try corpus.corpusRoot(arena, writes_relative);
    const document = try parseDocument(arena, try readFile(arena, path));
    if (integerMember(document, "version") != 1) return error.ConversationPinMismatch;
    if (!std.mem.eql(u8, textMember(document, "codex_commit"), session.codex_commit)) return error.ConversationPinMismatch;
    if (!std.mem.eql(u8, textMember(document, "capability_revision"), session.capability_revision)) return error.ConversationPinMismatch;
    var client = std.ArrayList([]const u8).empty;
    var server = std.ArrayList([]const u8).empty;
    for (member(document, "frames").?.array.items) |entry| {
        const direction = textMember(entry, "direction");
        if (std.mem.eql(u8, direction, "client_to_server")) {
            try client.append(arena, textMember(entry, "frame"));
        } else if (std.mem.eql(u8, direction, "server_to_client")) {
            try server.append(arena, textMember(entry, "frame"));
        } else {
            return error.UnroutedConversationFrame;
        }
    }
    return .{ .client = client.items, .server = server.items, .capabilities = textMember(document, "capabilities") };
}

const Server = struct {
    frames: []const []const u8,
    at: usize = 0,

    fn next(self: *Server, arena: std.mem.Allocator) !rpc.Message {
        if (self.at >= self.frames.len) return error.ConversationExhausted;
        defer self.at += 1;
        var decoder = rpc.Decoder{ .source = try std.mem.concat(arena, u8, &.{ self.frames[self.at], "\n" }) };
        return (try decoder.next(arena)) orelse error.ProductionCodecYieldedNoFrame;
    }
};

fn replayConversation(arena: *std.heap.ArenaAllocator, conversation: Conversation) ![]const []const u8 {
    const a = arena.allocator();
    var server = Server{ .frames = conversation.server };
    var sent = std.ArrayList([]const u8).empty;
    try sent.append(a, try rpc.encode(a, .{ .request = .{ .id = 0, .method = native.method_initialize, .params = try native.initializeParams(a, session.client_name, session.protocol_version) } }));
    const initialized = try server.next(a);
    if (initialized.kind != .response or !initialized.id.?.eql(.{ .integer = 0 }) or !native.decodes(initialized.result.?, native.initialize_response)) return error.HandshakeRefused;
    try sent.append(a, try rpc.encode(a, .{ .notification = .{ .method = native.method_initialized } }));

    var reducer = session.Reducer.init(arena, .{ .session_id = "session-1", .participant = participant, .model = "glm-test", .approval_policy = "on-request", .sandbox = "workspace-write", .id_width = 2 });
    try reducer.open();
    try reducer.observe(try server.next(a));
    try reducer.submit(.{ .messages = &.{.{ .text = outbound_text }}, .model_id = "glm-per-turn" });
    try reducer.observe(try server.next(a));
    const admission = try lastAdmission(&reducer);
    for (0..3) |_| try reducer.observe(try server.next(a));
    try reducer.resolve(.{ .run_id = admission.run_id, .responded_by = participant, .permission = .{
        .interaction_id = reducer.pendingInteraction() orelse return error.NoPendingInteraction,
        .requested_by = session.endpoint_id,
        .responded_by = participant,
        .session_id = admission.session_id,
        .run_id = admission.run_id,
        .choice_id = "decline",
        .granted = false,
    } });
    try reducer.observe(try server.next(a));
    try reducer.resolve(.{ .run_id = admission.run_id, .responded_by = participant, .input = .{
        .interaction_id = reducer.pendingInteraction() orelse return error.NoPendingInteraction,
        .requested_by = session.endpoint_id,
        .responded_by = participant,
        .session_id = admission.session_id,
        .run_id = admission.run_id,
        .answers = &.{ .{ .question_id = "mode", .selected_option_ids = &.{"option-2"} }, .{ .question_id = "note", .text = "ship it" } },
    } });
    for (0..3) |_| try reducer.observe(try server.next(a));
    if (try reducer.cancel(admission.run_id) != null) return error.CancelSettledWithoutInterrupt;
    for (0..2) |_| try reducer.observe(try server.next(a));
    if (server.at != server.frames.len) return error.ConversationNotExhausted;
    if (!std.mem.eql(u8, textMember(reducer.envelopes.items[reducer.envelopes.items.len - 1], "type"), "run.cancelled")) return error.ConversationDidNotSettle;
    try reducer.close();
    try sent.appendSlice(a, reducer.writes.items);
    return sent.items;
}

test "every frame the Zig adapter writes to Codex is byte-identical to the frame the Go adapter wrote" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const conversation = try loadConversation(arena.allocator());
    const sent = try replayConversation(&arena, conversation);
    for (sent, 0..) |frame, index| {
        if (index >= conversation.client.len) break;
        if (!std.mem.eql(u8, frame, conversation.client[index])) {
            std.debug.print("\nwrite {d} differs\nwant: {s}\ngot:  {s}\n", .{ index, conversation.client[index], frame });
            return error.WriteDiffers;
        }
    }
    try std.testing.expectEqual(conversation.client.len, sent.len);
}

test "the Zig capability descriptor is byte-identical to the one the Go adapter advertises" {
    var arena = std.heap.ArenaAllocator.init(std.testing.allocator);
    defer arena.deinit();
    const conversation = try loadConversation(arena.allocator());
    try std.testing.expectEqualStrings(conversation.capabilities, try rpc.encodeValue(arena.allocator(), try session.descriptor(arena.allocator())));
}
