const std = @import("std");
const adapter_corpus = @import("adapter_corpus");
const native = @import("native");
const httpapi = @import("httpapi");
const session = @import("session");
const gomarshal = @import("gomarshal");
const jsonschema = @import("jsonschema");
const semantic = @import("semantic");

const testing = std.testing;

pub const corpus_relative = "fixtures/adapters/opencode-v1.18.29";
pub const goldens_relative = "go/adapter/opencode/testdata/port-goldens.json";
const native_session = "ses_fake00000000000000";

pub const cases = [_][]const u8{
    "completed-text",   "text-streaming",   "reasoning-text",  "tool-lifecycle",
    "tool-failed",      "multi-step",       "step-failure",    "cancel-active",
    "queued-admission", "history-fence",    "foreign-session", "stream-exit",
    "observed-only",    "message-conflict", "interrupt-idle",
};

fn readFile(allocator: std.mem.Allocator, path: []const u8) ![]u8 {
    return std.Io.Dir.cwd().readFileAlloc(std.testing.io, path, allocator, .limited(16 * 1024 * 1024));
}

pub fn compact(arena: std.mem.Allocator, text: []const u8) std.mem.Allocator.Error![]const u8 {
    var out = try std.ArrayList(u8).initCapacity(arena, text.len);
    var in_string = false;
    var escaped = false;
    for (text) |byte| {
        if (in_string) {
            out.appendAssumeCapacity(byte);
            if (escaped) {
                escaped = false;
            } else if (byte == '\\') {
                escaped = true;
            } else if (byte == '"') in_string = false;
            continue;
        }
        switch (byte) {
            ' ', '\t', '\r', '\n' => {},
            '"' => {
                in_string = true;
                out.appendAssumeCapacity(byte);
            },
            else => out.appendAssumeCapacity(byte),
        }
    }
    return out.items;
}

const Definition = struct {
    cancel: bool = false,
    admission_rejected: bool = false,
    catalog: []const u8 = "",
};

fn definitionOf(scratch: std.mem.Allocator, text: []const u8) !Definition {
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, text, .{});
    if (parsed != .object) return error.InvalidCaseDefinition;
    if (parsed.object.get("replay_after") != null) return error.ReplayIsNotPorted;
    var definition = Definition{};
    if (parsed.object.get("cancel")) |value| definition.cancel = value == .bool and value.bool;
    if (parsed.object.get("admission_rejected")) |value| definition.admission_rejected = value == .bool and value.bool;
    if (adapter_corpus.stringMember(parsed.object, "catalog")) |name| definition.catalog = name;
    return definition;
}

const Action = enum { observe, cancel, cancel_idle, stream_failure };

const Frame = struct {
    history: bool,
    observed_only: bool,
    action: Action,
    event: ?native.Event = null,
};

fn actionOf(named: []const u8) !Action {
    if (named.len == 0 or std.mem.eql(u8, named, "observe")) return .observe;
    if (std.mem.eql(u8, named, "cancel")) return .cancel;
    if (std.mem.eql(u8, named, "cancel-idle")) return .cancel_idle;
    if (std.mem.eql(u8, named, "stream-failure")) return .stream_failure;
    return error.UnroutedCorpusAction;
}

fn decodeStreamFrame(scratch: std.mem.Allocator, raw: []const u8) !native.Event {
    var stream = httpapi.Stream.init(scratch, httpapi.default_frame_limit);
    defer stream.deinit();
    const wire = try std.mem.concat(scratch, u8, &.{ "event: message\ndata: ", raw, "\n\n" });
    const batch = try stream.feed(scratch, wire);
    if (batch.failure) |message| {
        std.debug.print("production SSE decode refused a corpus frame: {s}\n", .{message});
        return error.ProductionCodecRefusedCorpusFrame;
    }
    if (batch.events.len != 1) return error.ProductionCodecYieldedNoFrame;
    return batch.events[0];
}

fn loadFrames(scratch: std.mem.Allocator, text: []const u8) ![]const Frame {
    var frames = std.ArrayList(Frame).empty;
    var lines = std.mem.splitScalar(u8, std.mem.trimEnd(u8, text, "\n"), '\n');
    while (lines.next()) |line| {
        const script = try std.json.parseFromSliceLeaky(std.json.Value, scratch, line, .{});
        if (script != .object) return error.InvalidScriptLine;
        const source = adapter_corpus.stringMember(script.object, "source") orelse return error.InvalidScriptLine;
        const history = std.mem.eql(u8, source, "history");
        if (!history and !std.mem.eql(u8, source, "stream")) return error.InvalidScriptLine;
        const classification = adapter_corpus.stringMember(script.object, "classification") orelse return error.InvalidScriptLine;
        const action = try actionOf(adapter_corpus.stringMember(script.object, "action") orelse "");
        var frame = Frame{ .history = history, .observed_only = std.mem.eql(u8, classification, "observed-only"), .action = action };
        if (action == .observe) {
            const raw = try adapter_corpus.memberSource(scratch, line, "raw");
            if (history) {
                var diag = native.Diagnostic{};
                frame.event = native.decodeEvent(scratch, raw, &diag) catch |err| {
                    std.debug.print("production decode refused a history frame: {s}\n", .{diag.message});
                    return err;
                };
            } else frame.event = try decodeStreamFrame(scratch, raw);
        }
        try frames.append(scratch, frame);
    }
    return frames.items;
}

const Fake = struct {
    promoted: bool,
    rejected: bool,
    fed: bool = false,
    preset: []const native.Event,
    prompts: std.ArrayList(native.PromptRequest) = .empty,
    admitted: usize = 0,
    interrupts: usize = 0,

    fn from(context: *anyopaque) *Fake {
        return @ptrCast(@alignCast(context));
    }

    fn prompt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!session.PromptOutcome {
        const self = from(context);
        try self.prompts.append(arena, request);
        if (self.rejected) {
            const conflict = native.ApiError{ .status = 409, .tag = "ConflictError" };
            return .{ .failed = .{ .message = try conflict.message(arena), .api = true } };
        }
        self.admitted += 1;
        const count: i64 = @intCast(self.admitted);
        return .{ .admitted = .{
            .admitted_seq = count,
            .id = request.id,
            .session_id = session_id,
            .prompt = request.prompt,
            .delivery = request.delivery,
            .time_created = 1,
            .promoted_seq = if (self.promoted) count else null,
        } };
    }

    fn interrupt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!?session.Failure {
        _ = arena;
        _ = session_id;
        from(context).interrupts += 1;
        return null;
    }

    fn active(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!session.ActiveOutcome {
        _ = arena;
        _ = session_id;
        return .{ .listed = !from(context).fed };
    }

    fn history(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, after: i64, limit: usize) std.mem.Allocator.Error!session.HistoryOutcome {
        _ = session_id;
        _ = limit;
        var page = std.ArrayList(native.Event).empty;
        for (from(context).preset) |event| {
            if (event.durable.seq > after) try page.append(arena, event);
        }
        return .{ .page = .{ .events = page.items } };
    }

    fn client(self: *Fake) session.Native {
        return .{ .context = self, .prompt = prompt, .interrupt = interrupt, .active = active, .history = history };
    }

    fn admittedMessage(self: *Fake) []const u8 {
        if (self.admitted != 1) return "";
        return self.prompts.items[0].id;
    }
};

const Played = struct {
    reducer: session.Reducer,
    admission: session.Admission,
    prompts: []const native.PromptRequest,
    envelopes: []const std.json.Value,
};

fn play(arena: *std.heap.ArenaAllocator, id: []const u8, definition: Definition, frames: []const Frame) !Played {
    const scratch = arena.allocator();
    var preset = std.ArrayList(native.Event).empty;
    for (frames) |frame| {
        if (frame.history and frame.action == .observe) try preset.append(scratch, frame.event.?);
    }
    const fake = try scratch.create(Fake);
    fake.* = .{
        .promoted = definition.admission_rejected or std.mem.indexOf(u8, id, "queued") == null,
        .rejected = definition.admission_rejected,
        .preset = preset.items,
    };
    var reducer = session.Reducer.init(arena, .{ .native_id = native_session, .message_prefix = "msg_fake" }, fake.client());
    try reducer.open();
    const admission = try reducer.submit("session", "hello", "auto");
    if (definition.admission_rejected and (!std.mem.eql(u8, admission.admission, "queued") or !std.mem.eql(u8, admission.effective_delivery, "queue") or admission.run_id.len == 0)) {
        return error.ConflictWasNotAReservation;
    }

    for (frames, 0..) |frame, index| {
        const before = reducer.envelopes.items.len;
        switch (frame.action) {
            .observe => {
                if (frame.history) continue;
                var event = frame.event.?;
                if (event.kind == .prompted) {
                    var diag = native.Diagnostic{};
                    var data = try native.decodePrompted(scratch, event, &diag);
                    data.message_id = fake.admittedMessage();
                    event.data = try native.marshalPromptedData(scratch, data);
                }
                try reducer.observe(event);
            },
            .cancel, .cancel_idle => _ = try reducer.cancel(admission.run_id),
            .stream_failure => try reducer.transportFailed("corpus stream failure"),
        }
        if (frame.observed_only and reducer.envelopes.items.len != before) {
            std.debug.print("{s} frame {d} is observed-only yet emitted\n", .{ id, index + 1 });
            return error.ObservedOnlyFrameEmitted;
        }
    }
    fake.fed = true;
    try reducer.poll();
    return .{ .reducer = reducer, .admission = admission, .prompts = fake.prompts.items, .envelopes = reducer.envelopes.items };
}

fn marshalTrace(scratch: std.mem.Allocator, envelopes: []const std.json.Value) ![]const u8 {
    var out = std.ArrayList(u8).empty;
    try out.append(scratch, '[');
    for (envelopes, 0..) |envelope, index| {
        if (index != 0) try out.append(scratch, ',');
        try gomarshal.appendValue(&out, scratch, envelope);
    }
    try out.append(scratch, ']');
    return out.items;
}

fn controlEnvelope(scratch: std.mem.Allocator, kind: []const u8, id: []const u8, payload: std.json.Value) !std.json.ObjectMap {
    var value: std.json.ObjectMap = .empty;
    try value.put(scratch, "protocol", .{ .string = session.protocol_name });
    try value.put(scratch, "version", .{ .string = session.protocol_version });
    try value.put(scratch, "profile", .{ .string = session.profile });
    try value.put(scratch, "type", .{ .string = kind });
    try value.put(scratch, "id", .{ .string = id });
    try value.put(scratch, "payload", payload);
    return value;
}

fn cancelCut(events: []const std.json.Value) usize {
    for (events, 0..) |event, index| {
        if (!std.mem.eql(u8, event.object.get("type").?.string, "run.status.updated")) continue;
        const status = event.object.get("payload").?.object.get("status").?.string;
        if (std.mem.eql(u8, status, "cancelling")) return index;
    }
    var index = events.len;
    while (index > 0) {
        index -= 1;
        if (terminal(events[index].object.get("type").?.string)) return index;
    }
    return events.len;
}

fn terminal(kind: []const u8) bool {
    return std.mem.eql(u8, kind, "run.completed") or std.mem.eql(u8, kind, "run.failed") or std.mem.eql(u8, kind, "run.cancelled");
}

fn protocolTrace(scratch: std.mem.Allocator, admission: session.Admission, events: []const std.json.Value, cancelled: bool) ![]const std.json.Value {
    var trace = std.ArrayList(std.json.Value).empty;
    try trace.append(scratch, .{ .object = try controlEnvelope(scratch, "capabilities.request", "capabilities-request", .{ .object = .empty }) });
    var capabilities = try controlEnvelope(scratch, "capabilities.response", "capabilities-response", try session.capabilities(scratch));
    try capabilities.put(scratch, "in_reply_to", .{ .string = "capabilities-request" });
    try capabilities.put(scratch, "capability_revision", .{ .string = session.capability_revision });
    try trace.append(scratch, .{ .object = capabilities });

    var message: std.json.ObjectMap = .empty;
    try message.put(scratch, "role", .{ .string = "user" });
    try message.put(scratch, "content", .{ .string = "adaptertest" });
    var messages = std.json.Array.init(scratch);
    try messages.append(.{ .object = message });
    var request: std.json.ObjectMap = .empty;
    try request.put(scratch, "session_id", .{ .string = admission.session_id });
    try request.put(scratch, "messages", .{ .array = messages });
    try request.put(scratch, "delivery", .{ .string = admission.requested_delivery });
    var submit = try controlEnvelope(scratch, "session.message.submit.request", "submit-request", .{ .object = request });
    try submit.put(scratch, "session_id", .{ .string = admission.session_id });
    try submit.put(scratch, "capability_revision", .{ .string = session.capability_revision });
    try trace.append(scratch, .{ .object = submit });
    var response = try controlEnvelope(scratch, "session.message.submit.response", "submit-response", try session.admissionValue(scratch, admission));
    try response.put(scratch, "in_reply_to", .{ .string = "submit-request" });
    try response.put(scratch, "session_id", .{ .string = admission.session_id });
    try response.put(scratch, "capability_revision", .{ .string = session.capability_revision });
    try trace.append(scratch, .{ .object = response });

    if (!cancelled) {
        try trace.appendSlice(scratch, events);
        return trace.items;
    }
    var cancel_payload: std.json.ObjectMap = .empty;
    try cancel_payload.put(scratch, "session_id", .{ .string = admission.session_id });
    try cancel_payload.put(scratch, "run_id", .{ .string = admission.run_id });
    var cancel = try controlEnvelope(scratch, "run.cancel.request", "cancel-request", .{ .object = cancel_payload });
    try cancel.put(scratch, "session_id", .{ .string = admission.session_id });
    try cancel.put(scratch, "run_id", .{ .string = admission.run_id });
    var ack_payload: std.json.ObjectMap = .empty;
    try ack_payload.put(scratch, "session_id", .{ .string = admission.session_id });
    try ack_payload.put(scratch, "run_id", .{ .string = admission.run_id });
    try ack_payload.put(scratch, "accepted", .{ .bool = true });
    try ack_payload.put(scratch, "status", .{ .string = "cancelling" });
    var ack = try controlEnvelope(scratch, "run.cancel.response", "cancel-response", .{ .object = ack_payload });
    try ack.put(scratch, "in_reply_to", .{ .string = "cancel-request" });
    try ack.put(scratch, "session_id", .{ .string = admission.session_id });
    try ack.put(scratch, "run_id", .{ .string = admission.run_id });
    const cut = cancelCut(events);
    try trace.appendSlice(scratch, events[0..cut]);
    try trace.append(scratch, .{ .object = cancel });
    try trace.append(scratch, .{ .object = ack });
    try trace.appendSlice(scratch, events[cut..]);
    return trace.items;
}

fn expectRunInvariants(admission: session.Admission, events: []const std.json.Value) !void {
    var terminals: usize = 0;
    for (events, 0..) |event, index| {
        const object = event.object;
        if (!std.mem.eql(u8, object.get("session_id").?.string, admission.session_id)) return error.EventOutOfScope;
        if (!std.mem.eql(u8, object.get("run_id").?.string, admission.run_id)) return error.EventOutOfScope;
        if (object.get("sequence").?.integer != @as(i64, @intCast(index + 1))) return error.SequenceNotContiguous;
        if (!std.mem.eql(u8, object.get("capability_revision").?.string, session.capability_revision)) return error.StaleCapabilityRevision;
        if (!terminal(object.get("type").?.string)) continue;
        terminals += 1;
        if (index != events.len - 1) return error.TerminalIsNotLast;
    }
    if (terminals != 1) return error.NotExactlyOneTerminal;
}

fn validate(allocator: std.mem.Allocator, registry: *const jsonschema.Registry, trace: []const std.json.Value) !void {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const encoded = try marshalTrace(scratch, trace);
    const reparsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, encoded, .{});
    var validator = jsonschema.Validator.init(allocator, registry);
    defer validator.deinit();
    for (reparsed.array.items, 0..) |item, index| {
        if (try validator.validate("envelope.schema.json", item)) |_| {
            std.debug.print("trace envelope {d} is schema-invalid: {s}\n", .{ index, try gomarshal.marshal(scratch, item) });
            return error.SchemaInvalidTrace;
        }
    }
    var machine = semantic.Machine.init(allocator);
    defer machine.deinit();
    for (reparsed.array.items, 0..) |item, index| try machine.apply(index, item);
    try machine.close();
    for (machine.diagnostics.items) |diagnostic| std.debug.print("semantic {s} at {d}\n", .{ diagnostic.code, diagnostic.index });
    if (machine.diagnostics.items.len != 0) return error.SemanticallyInvalidTrace;
}

const Goldens = struct {
    capability_revision: []const u8,
    capabilities: std.json.Value,
    endpoint: []const u8,
    requests: std.json.Array,

    fn request(self: Goldens, name: []const u8) !std.json.ObjectMap {
        for (self.requests.items) |item| {
            if (std.mem.eql(u8, adapter_corpus.stringMember(item.object, "name") orelse "", name)) return item.object;
        }
        return error.NoSuchGolden;
    }
};

fn loadGoldens(scratch: std.mem.Allocator) !Goldens {
    const path = try adapter_corpus.corpusRoot(scratch, goldens_relative);
    const parsed = try std.json.parseFromSliceLeaky(std.json.Value, scratch, try readFile(scratch, path), .{});
    return .{
        .capability_revision = parsed.object.get("capability_revision").?.string,
        .capabilities = parsed.object.get("capabilities").?,
        .endpoint = parsed.object.get("endpoint").?.string,
        .requests = parsed.object.get("requests").?.array,
    };
}

fn authorized(goldens: Goldens) httpapi.Endpoint {
    return .{ .base_path = goldens.endpoint, .username = "opencode", .password = "secret" };
}

fn expectRequest(golden: std.json.ObjectMap, request: httpapi.Request) !void {
    try testing.expectEqualStrings(golden.get("method").?.string, request.method);
    try testing.expectEqualStrings(golden.get("target").?.string, request.target);
    const want = golden.get("headers").?.object;
    try testing.expectEqual(want.count(), request.headers.len);
    for (request.headers) |header| try testing.expectEqualStrings(want.get(header.name).?.string, header.value);
    if (golden.get("body")) |body| {
        try testing.expectEqualStrings(body.string, request.body.?);
    } else try testing.expectEqual(@as(?[]const u8, null), request.body);
}

const Outcome = struct { emitted: usize, exact: bool };

fn runCase(allocator: std.mem.Allocator, registry: *const jsonschema.Registry, goldens: Goldens, root: []const u8, id: []const u8) !Outcome {
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const dir = try std.fs.path.join(scratch, &.{ root, id });
    const definition = try definitionOf(scratch, try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "case.json" })));
    const frames = try loadFrames(scratch, try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "native.jsonl" })));
    const expected = try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "expected-oap.json" }));
    if (adapter_corpus.isBlank(.zero_byte_only, expected)) return error.BlankExpectation;

    var played = try play(&arena, id, definition, frames);
    const want = try compact(scratch, expected);
    const got = try marshalTrace(scratch, played.envelopes);
    const exact = std.mem.eql(u8, want, got);
    if (!exact) std.debug.print("\n{s} mismatch\nwant: {s}\ngot:  {s}\n", .{ id, want, got });

    try expectRunInvariants(played.admission, played.envelopes);
    try validate(allocator, registry, try protocolTrace(scratch, played.admission, played.envelopes, definition.cancel));

    if (definition.catalog.len > 0) {
        const catalog = try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, definition.catalog }));
        try testing.expectEqualStrings(try compact(scratch, catalog), try gomarshal.marshal(scratch, try played.reducer.models("session", true)));
    }

    if (played.prompts.len != 1) return error.PromptNotSentOnce;
    try expectRequest(try goldens.request("prompt"), try httpapi.prompt(scratch, authorized(goldens), native_session, played.prompts[0]));
    return .{ .emitted = played.envelopes.len, .exact = exact };
}

test "every opencode case reproduces its expected trace byte for byte, validates, and sends the recorded prompt" {
    const allocator = testing.allocator;
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const goldens = try loadGoldens(arena.allocator());
    const root = try adapter_corpus.corpusRoot(arena.allocator(), corpus_relative);
    var failed: usize = 0;
    for (cases) |id| {
        const outcome = runCase(allocator, &registry, goldens, root, id) catch |err| {
            std.debug.print("V|{s}: {s}\n", .{ id, @errorName(err) });
            failed += 1;
            continue;
        };
        if (!outcome.exact) {
            std.debug.print("V|{s}: MISMATCH\n", .{id});
            failed += 1;
            continue;
        }
        std.debug.print("V|{s}: EXACT {d}\n", .{ id, outcome.emitted });
    }
    try testing.expectEqual(@as(usize, 0), failed);
}

test "the validator the driver runs refuses a trace with a gap in its sequence" {
    const allocator = testing.allocator;
    var registry = try jsonschema.Registry.initFromBundled(allocator);
    defer registry.deinit();
    var arena = std.heap.ArenaAllocator.init(allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const root = try adapter_corpus.corpusRoot(scratch, corpus_relative);
    const native_text = try readFile(scratch, try std.fs.path.join(scratch, &.{ root, "completed-text", "native.jsonl" }));
    const played = try play(&arena, "completed-text", .{}, try loadFrames(scratch, native_text));
    try validate(allocator, &registry, try protocolTrace(scratch, played.admission, played.envelopes, false));
    const gapped = [_]std.json.Value{ played.envelopes[0], played.envelopes[2] };
    try testing.expectError(error.SemanticallyInvalidTrace, validate(allocator, &registry, try protocolTrace(scratch, played.admission, &gapped, false)));
}

test "the driver's case list is the manifest's, in order" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const root = try adapter_corpus.corpusRoot(scratch, corpus_relative);
    const manifest = try std.json.parseFromSliceLeaky(std.json.Value, scratch, try readFile(scratch, try std.fs.path.join(scratch, &.{ root, "manifest.json" })), .{});
    try testing.expectEqualStrings(native.pinned_tag, manifest.object.get("tag").?.string);
    try testing.expectEqualStrings(session.pinned_commit, manifest.object.get("commit").?.string);
    const listed = manifest.object.get("cases").?.array.items;
    try testing.expectEqual(cases.len, listed.len);
    for (cases, listed) |id, entry| {
        try testing.expectEqualStrings(id, entry.object.get("id").?.string);
        try testing.expectEqualStrings(id, entry.object.get("path").?.string);
    }
}

test "every request the port encodes is the request the Go adapter sends" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const goldens = try loadGoldens(scratch);
    const authed = authorized(goldens);
    const bare = httpapi.Endpoint{ .base_path = goldens.endpoint };
    try expectRequest(try goldens.request("create-session"), try httpapi.createSession(scratch, authed, .{ .agent = "build", .model = .{ .id = "fixture", .provider_id = "fixture", .variant = "high" } }));
    try expectRequest(try goldens.request("create-session-bare"), try httpapi.createSession(scratch, bare, .{}));
    try expectRequest(try goldens.request("prompt"), try httpapi.prompt(scratch, authed, native_session, .{ .id = "msg_fake0000000000000004", .prompt = .{ .text = "hello" }, .delivery = "steer" }));
    try expectRequest(try goldens.request("prompt-escaped-queue"), try httpapi.prompt(scratch, authed, native_session, .{
        .id = "msg_fake0000000000000009",
        .prompt = .{ .text = "a<b>&c \"q\" \\ \u{2028}\u{2029}\n\t\r\x08\x0c\x01\x1f\x7f \u{e9}" },
        .delivery = "queue",
    }));
    try expectRequest(try goldens.request("interrupt"), try httpapi.interrupt(scratch, authed, native_session));
    try expectRequest(try goldens.request("active"), try httpapi.active(scratch, authed));
    try expectRequest(try goldens.request("history"), try httpapi.history(scratch, authed, native_session, 3, 100));
    try expectRequest(try goldens.request("subscribe"), try httpapi.subscribe(scratch, authed, native_session, -1));
    try expectRequest(try goldens.request("subscribe-after"), try httpapi.subscribe(scratch, authed, native_session, 5));
    try testing.expectEqual(@as(usize, 9), goldens.requests.items.len);
}

test "the port advertises the descriptor and revision the Go adapter advertises" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const goldens = try loadGoldens(scratch);
    try testing.expectEqualStrings(goldens.capability_revision, session.capability_revision);
    try testing.expectEqualStrings(try gomarshal.marshal(scratch, goldens.capabilities), try gomarshal.marshal(scratch, try session.capabilities(scratch)));
}

pub const scenarios_relative = "go/adapter/opencode/testdata/port-scenarios.json";

const ScenarioClient = struct {
    gated: bool,
    active_error: []const u8,
    history_error: []const u8,
    foreign_admission: bool,
    fed: bool = false,
    admitted: usize = 0,

    fn from(context: *anyopaque) *ScenarioClient {
        return @ptrCast(@alignCast(context));
    }

    fn prompt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, request: native.PromptRequest) std.mem.Allocator.Error!session.PromptOutcome {
        _ = arena;
        const self = from(context);
        if (self.foreign_admission) {
            return .{ .admitted = .{ .admitted_seq = 1, .id = "msg_foreign", .session_id = session_id, .prompt = request.prompt, .delivery = request.delivery, .time_created = 1 } };
        }
        self.admitted += 1;
        const count: i64 = @intCast(self.admitted);
        return .{ .admitted = .{ .admitted_seq = count, .id = request.id, .session_id = session_id, .prompt = request.prompt, .delivery = request.delivery, .time_created = 1, .promoted_seq = count } };
    }

    fn interrupt(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!?session.Failure {
        _ = context;
        _ = arena;
        _ = session_id;
        return null;
    }

    fn active(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8) std.mem.Allocator.Error!session.ActiveOutcome {
        _ = arena;
        _ = session_id;
        const self = from(context);
        if (self.active_error.len > 0) return .{ .failed = .{ .message = self.active_error } };
        return .{ .listed = self.gated and !self.fed };
    }

    fn history(context: *anyopaque, arena: std.mem.Allocator, session_id: []const u8, after: i64, limit: usize) std.mem.Allocator.Error!session.HistoryOutcome {
        _ = arena;
        _ = session_id;
        _ = after;
        _ = limit;
        const self = from(context);
        if (self.history_error.len > 0) return .{ .failed = .{ .message = self.history_error } };
        return .{ .page = .{} };
    }

    fn client(self: *ScenarioClient) session.Native {
        return .{ .context = self, .prompt = prompt, .interrupt = interrupt, .active = active, .history = history };
    }
};

fn memberOr(object: std.json.ObjectMap, name: []const u8) std.json.Value {
    return object.get(name) orelse .null;
}

fn replayScenario(arena: *std.heap.ArenaAllocator, scenario: std.json.ObjectMap) !bool {
    const scratch = arena.allocator();
    const name = scenario.get("name").?.string;
    const knobs = scenario.get("client").?.object;
    const fake = try scratch.create(ScenarioClient);
    fake.* = .{
        .gated = memberOr(knobs, "gated") == .bool,
        .active_error = adapter_corpus.stringMember(knobs, "active_error") orelse "",
        .history_error = adapter_corpus.stringMember(knobs, "history_error") orelse "",
        .foreign_admission = memberOr(knobs, "foreign_admission") == .bool,
    };
    var reducer = session.Reducer.init(arena, .{ .native_id = native_session, .message_prefix = "msg_fake" }, fake.client());
    try reducer.open();
    var admissions = std.ArrayList(session.Admission).empty;
    for (scenario.get("script").?.array.items) |item| {
        const op = item.object;
        const kind = op.get("op").?.string;
        if (std.mem.eql(u8, kind, "submit")) {
            try admissions.append(scratch, try reducer.submit("session", op.get("text").?.string, op.get("delivery").?.string));
        } else if (std.mem.eql(u8, kind, "event")) {
            var diag = native.Diagnostic{};
            try reducer.observe(try native.decodeEvent(scratch, try std.json.Stringify.valueAlloc(scratch, op.get("raw").?, .{}), &diag));
        } else if (std.mem.eql(u8, kind, "cancel")) {
            const which: usize = if (op.get("submission")) |index| try std.fmt.parseInt(usize, index.number_string, 10) else 0;
            _ = try reducer.cancel(admissions.items[which].run_id);
        } else if (std.mem.eql(u8, kind, "idle")) {
            fake.fed = true;
            try reducer.poll();
        } else if (std.mem.eql(u8, kind, "fail")) {
            try reducer.transportFailed(op.get("message").?.string);
        } else if (!std.mem.eql(u8, kind, "await")) return error.UnroutedScenarioOp;
    }
    const recorded_admissions = scenario.get("admissions").?.array.items;
    const recorded_runs = scenario.get("runs").?.array.items;
    if (recorded_admissions.len != admissions.items.len or recorded_runs.len != admissions.items.len) return error.ScenarioShapeMismatch;
    var exact = true;
    for (admissions.items, recorded_admissions, recorded_runs) |admission, want_admission, want_run| {
        const got_admission = try gomarshal.marshal(scratch, try session.admissionValue(scratch, admission));
        if (!std.mem.eql(u8, got_admission, try gomarshal.marshal(scratch, want_admission))) {
            std.debug.print("\n{s} admission mismatch\nwant: {s}\ngot:  {s}\n", .{ name, try gomarshal.marshal(scratch, want_admission), got_admission });
            exact = false;
        }
        var run = std.ArrayList(std.json.Value).empty;
        for (reducer.envelopes.items) |emitted| {
            if (std.mem.eql(u8, emitted.object.get("run_id").?.string, admission.run_id)) try run.append(scratch, emitted);
        }
        const got = try marshalTrace(scratch, run.items);
        const want = try gomarshal.marshal(scratch, want_run);
        if (!std.mem.eql(u8, got, want)) {
            std.debug.print("\n{s} run {s} mismatch\nwant: {s}\ngot:  {s}\n", .{ name, admission.run_id, want, got });
            exact = false;
        }
    }
    return exact;
}

test "every Go-recorded port scenario replays to the same admissions and runs byte for byte" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const path = try adapter_corpus.corpusRoot(scratch, scenarios_relative);
    const recorded = try std.json.parseFromSliceLeaky(std.json.Value, scratch, try readFile(scratch, path), .{ .parse_numbers = false });
    var failed: usize = 0;
    for (recorded.array.items) |scenario| {
        var replay = std.heap.ArenaAllocator.init(testing.allocator);
        defer replay.deinit();
        const name = scenario.object.get("name").?.string;
        const exact = replayScenario(&replay, scenario.object) catch |err| {
            std.debug.print("S|{s}: {s}\n", .{ name, @errorName(err) });
            failed += 1;
            continue;
        };
        if (!exact) failed += 1;
        std.debug.print("S|{s}: {s}\n", .{ name, if (exact) "EXACT" else "MISMATCH" });
    }
    try testing.expectEqual(@as(usize, 14), recorded.array.items.len);
    try testing.expectEqual(@as(usize, 0), failed);
}

const Loaded = struct { id: []const u8, definition: Definition, native_text: []const u8 };

fn replayInMemory(allocator: std.mem.Allocator, loaded: []const Loaded) !void {
    for (loaded) |entry| {
        var arena = std.heap.ArenaAllocator.init(allocator);
        defer arena.deinit();
        const frames = try loadFrames(arena.allocator(), entry.native_text);
        const played = try play(&arena, entry.id, entry.definition, frames);
        _ = try marshalTrace(arena.allocator(), played.envelopes);
    }
}

test "replaying a case propagates every allocation failure and leaks nothing" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    const scratch = arena.allocator();
    const root = try adapter_corpus.corpusRoot(scratch, corpus_relative);
    var loaded = std.ArrayList(Loaded).empty;
    for ([_][]const u8{ "tool-lifecycle", "history-fence", "cancel-active", "message-conflict" }) |id| {
        const dir = try std.fs.path.join(scratch, &.{ root, id });
        try loaded.append(scratch, .{
            .id = id,
            .definition = try definitionOf(scratch, try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "case.json" }))),
            .native_text = try readFile(scratch, try std.fs.path.join(scratch, &.{ dir, "native.jsonl" })),
        });
    }
    try testing.checkAllAllocationFailures(testing.allocator, replayInMemory, .{@as([]const Loaded, loaded.items)});
}

test "compaction strips whitespace outside strings and keeps it inside" {
    var arena = std.heap.ArenaAllocator.init(testing.allocator);
    defer arena.deinit();
    try testing.expectEqualStrings("{\"a\":\" b \\\" c \",\"d\":[1,2]}", try compact(arena.allocator(), "{ \"a\" : \" b \\\" c \",\n  \"d\": [1, 2] }\n"));
}
