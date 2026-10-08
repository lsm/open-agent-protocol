const std = @import("std");
const compat = @import("compat");
const ai_types = @import("ai_types");
const session_events = @import("session_events");
const session_runtime = @import("session_runtime");
const agent = @import("agent");
const json_writer = @import("json_writer");
const builtin = @import("builtin");
const OwnedSlice = @import("owned_slice").OwnedSlice;

const ToolResultSource = enum { execution_end, message_end };

const ToolResultReplayEntry = struct {
    tool_call_id: []u8,
    message_index: usize,
    source: ToolResultSource,

    fn deinit(self: *ToolResultReplayEntry, allocator: std.mem.Allocator) void {
        allocator.free(self.tool_call_id);
        self.* = undefined;
    }
};

fn tmpBase(allocator: std.mem.Allocator, tmp: *std.testing.TmpDir) ![]u8 {
    const base = try std.fs.path.join(allocator, &.{ ".zig-cache", "tmp", &tmp.sub_path, "sessions" });
    errdefer allocator.free(base);
    try compat.fs.createDir(compat.fs.getCwd(), base);
    return base;
}

fn defaultIo() std.Io {
    return if (@import("builtin").is_test)
        std.testing.io
    else
        std.Io.Threaded.global_single_threaded.io();
}

fn appendFile(path: []const u8, data: []const u8) !void {
    _ = builtin;
    var file = try compat.fs.getCwd().createFile(defaultIo(), path, .{ .truncate = false, .read = true, .lock = .exclusive, .permissions = compat.fs.default_file_mode });
    defer file.close(defaultIo());
    const stat = try file.stat(defaultIo());
    try file.writePositionalAll(defaultIo(), data, stat.size);
}

const metadata_max_bytes = 1024 * 1024;
const max_jsonl_line_bytes = 8 * 1024 * 1024;
const display_tail_bytes = 256 * 1024;
const index_max_bytes = 64 * 1024;
const stream_suffix = ".stream.jsonl";
const index_suffix = ".meta.json";
const load_skips = [_][]const u8{ "provider_event", "tool_call_delta", "tool_execution_update" };

fn readJsonlRecords(allocator: std.mem.Allocator, path: []const u8, start: u64, ctx: anytype, comptime onLine: fn (@TypeOf(ctx), u64, []const u8) anyerror!void) !void {
    var file = try compat.fs.getCwd().openFile(defaultIo(), path, .{});
    defer file.close(defaultIo());
    var file_buffer: [16 * 1024]u8 = undefined;
    var reader = file.reader(defaultIo(), &file_buffer);
    if (start > 0) try reader.seekTo(start);
    var out: std.Io.Writer.Allocating = .init(allocator);
    defer out.deinit();
    var offset = start;
    while (true) {
        out.clearRetainingCapacity();
        _ = reader.interface.streamDelimiterEnding(&out.writer, '\n') catch |err| switch (err) {
            error.ReadFailed => return reader.err.?,
            error.WriteFailed => return error.OutOfMemory,
        };
        const raw_line = out.written();
        if (raw_line.len > max_jsonl_line_bytes) return error.StreamTooLong;
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len > 0) try onLine(ctx, offset, line);
        offset += raw_line.len;
        if (reader.interface.buffered().len == 0) break;
        _ = reader.interface.takeByte() catch break;
        offset += 1;
    }
}

fn readDisplayTail(allocator: std.mem.Allocator, path: []const u8, start: u64, ctx: *LoadLineContext) !void {
    const from = start -| display_tail_bytes;
    var file = try compat.fs.getCwd().openFile(defaultIo(), path, .{});
    defer file.close(defaultIo());
    const data = try allocator.alloc(u8, @intCast(start - from));
    defer allocator.free(data);
    const read = try file.readPositionalAll(defaultIo(), data, from);
    var rest = data[0..read];
    var offset = from;
    if (from > 0) {
        const newline = std.mem.indexOfScalar(u8, rest, '\n') orelse return;
        rest = rest[newline + 1 ..];
        offset += newline + 1;
    }
    var lines = std.mem.splitScalar(u8, rest, '\n');
    while (lines.next()) |raw_line| {
        const line = std.mem.trim(u8, raw_line, " \t\r");
        if (line.len > 0) try loadLine(ctx, offset, line);
        offset += raw_line.len + 1;
    }
}

fn startsCompaction(path: []const u8, offset: u64) bool {
    if (offset == 0) return false;
    var file = compat.fs.getCwd().openFile(defaultIo(), path, .{}) catch return false;
    defer file.close(defaultIo());
    var window: [1024]u8 = undefined;
    const read = file.readPositionalAll(defaultIo(), &window, offset - 1) catch return false;
    if (read < 2 or window[0] != '\n') return false;
    const line_end = std.mem.indexOfScalarPos(u8, window[0..read], 1, '\n') orelse read;
    return std.mem.indexOf(u8, window[1..line_end], "\"event\":{\"type\":\"compaction_end\",\"outcome\":\"completed\"") != null;
}

fn loadSkips(line: []const u8) bool {
    const marker = "\"event\":{\"type\":\"";
    const start = (std.mem.indexOf(u8, line, marker) orelse return false) + marker.len;
    const end = std.mem.indexOfScalarPos(u8, line, start, '"') orelse return false;
    for (load_skips) |kind| if (std.mem.eql(u8, line[start..end], kind)) return true;
    return false;
}

fn readLastJsonlLines(allocator: std.mem.Allocator, path: []const u8, max_bytes: usize) ![]u8 {
    var file = try compat.fs.getCwd().openFile(defaultIo(), path, .{});
    defer file.close(defaultIo());
    const stat = try file.stat(defaultIo());
    const read_len: usize = @intCast(@min(stat.size, max_bytes));
    const offset = stat.size - read_len;
    const data = try allocator.alloc(u8, read_len);
    errdefer allocator.free(data);
    const read = try file.readPositionalAll(defaultIo(), data, offset);
    if (read == data.len) return data;
    return allocator.realloc(data, read);
}

pub const SessionMetadata = struct {
    session_id: []u8,
    model: []u8,
    provider: []u8,
    last_active: i64,
    created_at: i64 = 0,
    compaction_offset: u64 = 0,
    compactions: u32 = 0,
    title: []u8 = &.{},
    title_generated: bool = false,
    title_renamed: bool = false,
    thinking_level: ?ai_types.ThinkingLevel = null,

    pub fn deinit(self: *SessionMetadata, allocator: std.mem.Allocator) void {
        allocator.free(self.session_id);
        allocator.free(self.model);
        allocator.free(self.provider);
        if (self.title.len > 0) allocator.free(self.title);
        self.* = undefined;
    }
};

const ReplayState = struct {
    current_role: ?session_events.SessionEvent.MessageRole = null,
    assistant_text: std.ArrayList(u8) = .empty,
    tool_call_json: std.ArrayList(u8) = .empty,
    tool_results: std.ArrayList(ToolResultReplayEntry) = .empty,

    fn deinit(self: *ReplayState, allocator: std.mem.Allocator) void {
        for (self.tool_results.items) |*entry| entry.deinit(allocator);
        self.tool_results.deinit(allocator);
        self.assistant_text.deinit(allocator);
        self.tool_call_json.deinit(allocator);
        self.* = undefined;
    }
};

pub const LoadedSession = struct {
    metadata: SessionMetadata,
    model_unavailable: bool = false,
    events: std.ArrayList(session_events.SessionEvent) = .empty,
    messages: std.ArrayList(ai_types.Message) = .empty,

    pub fn deinit(self: *LoadedSession, allocator: std.mem.Allocator) void {
        self.metadata.deinit(allocator);
        for (self.events.items) |*event| event.deinit(allocator);
        self.events.deinit(allocator);
        for (self.messages.items) |*msg| msg.deinit(allocator);
        self.messages.deinit(allocator);
        self.* = undefined;
    }
};

pub const Store = struct {
    allocator: std.mem.Allocator,
    base_dir: []u8,

    pub fn init(allocator: std.mem.Allocator, base_dir: []const u8) !Store {
        return .{ .allocator = allocator, .base_dir = try allocator.dupe(u8, base_dir) };
    }

    pub fn initDefault(allocator: std.mem.Allocator) !Store {
        const home = compat.getEnvVarOwned(allocator, "HOME") catch |err| switch (err) {
            error.EnvironmentVariableMissing => return error.HomeNotFound,
            else => return err,
        };
        defer allocator.free(home);
        const base = try std.fs.path.join(allocator, &.{ home, ".oapx", "sessions" });
        defer allocator.free(base);
        return init(allocator, base);
    }

    pub fn deinit(self: *Store) void {
        self.allocator.free(self.base_dir);
        self.* = undefined;
    }

    pub fn save(self: Store, metadata: SessionMetadata, event: session_events.SessionEvent) !void {
        try self.appendRecord(metadata.session_id, ".jsonl", metadata, event);
    }

    pub fn saveEvent(self: Store, session_id: []const u8, event: session_events.SessionEvent) !void {
        try self.appendRecord(session_id, ".jsonl", null, event);
    }

    pub fn saveChunk(self: Store, session_id: []const u8, event: session_events.SessionEvent) !void {
        try self.appendRecord(session_id, stream_suffix, null, event);
    }

    fn appendRecord(self: Store, session_id: []const u8, suffix: []const u8, metadata: ?SessionMetadata, event: session_events.SessionEvent) !void {
        try compat.fs.createDir(compat.fs.getCwd(), self.base_dir);
        const path = try sessionFilePath(self.allocator, self.base_dir, session_id, suffix);
        defer self.allocator.free(path);
        const line = try serializeEventRecord(self.allocator, metadata, event);
        defer self.allocator.free(line);
        try appendFile(path, line);
    }

    pub fn conversationBytes(self: Store, session_id: []const u8) !u64 {
        const path = try sessionPath(self.allocator, self.base_dir, session_id);
        defer self.allocator.free(path);
        var file = compat.fs.getCwd().openFile(defaultIo(), path, .{}) catch |err| switch (err) {
            error.FileNotFound => return 0,
            else => return err,
        };
        defer file.close(defaultIo());
        return (try file.stat(defaultIo())).size;
    }

    pub fn saveIndex(self: Store, metadata: SessionMetadata) !void {
        try compat.fs.createDir(compat.fs.getCwd(), self.base_dir);
        const path = try sessionFilePath(self.allocator, self.base_dir, metadata.session_id, index_suffix);
        defer self.allocator.free(path);
        const tmp_path = try std.fmt.allocPrint(self.allocator, "{s}.tmp", .{path});
        defer self.allocator.free(tmp_path);
        const data = try serializeIndex(self.allocator, metadata);
        defer self.allocator.free(data);
        try compat.fs.atomicReplace(compat.fs.getCwd(), path, tmp_path, data);
    }

    pub fn saveGeneratedTitle(self: Store, session_id: []const u8, title: []const u8) !void {
        var meta = try self.loadIndex(session_id);
        defer meta.deinit(self.allocator);
        if (meta.title_renamed) return;
        try replaceString(self.allocator, &meta.title, title);
        meta.title_generated = true;
        try self.saveIndex(meta);
    }

    pub fn loadIndex(self: Store, session_id: []const u8) !SessionMetadata {
        const path = try sessionFilePath(self.allocator, self.base_dir, session_id, index_suffix);
        defer self.allocator.free(path);
        const data = try compat.fs.readFileAlloc(self.allocator, compat.fs.getCwd(), path, index_max_bytes);
        defer self.allocator.free(data);
        return parseIndex(self.allocator, session_id, data);
    }

    pub fn load(self: Store, session_id: []const u8) !LoadedSession {
        const path = try sessionPath(self.allocator, self.base_dir, session_id);
        defer self.allocator.free(path);
        var windowed = true;
        while (true) : (windowed = false) {
            const metadata = self.loadIndex(session_id) catch |err| switch (err) {
                error.OutOfMemory => return err,
                else => try defaultMetadata(self.allocator, session_id),
            };
            var loaded = LoadedSession{ .metadata = metadata };
            errdefer loaded.deinit(self.allocator);
            var replay = ReplayState{};
            defer replay.deinit(self.allocator);
            var ctx = LoadLineContext{ .allocator = self.allocator, .loaded = &loaded, .replay = &replay };
            const start: u64 = if (windowed and startsCompaction(path, loaded.metadata.compaction_offset)) loaded.metadata.compaction_offset else 0;
            if (start > 0) try readDisplayTail(self.allocator, path, start, &ctx);
            try readJsonlRecords(self.allocator, path, start, &ctx, loadLine);
            if (start > 0 and ctx.last_compaction == 0) {
                loaded.deinit(self.allocator);
                continue;
            }
            loaded.metadata.compaction_offset = ctx.last_compaction;
            return loaded;
        }
    }

    pub fn list(self: Store) !std.ArrayList(SessionMetadata) {
        var result: std.ArrayList(SessionMetadata) = .empty;
        errdefer {
            for (result.items) |*meta| meta.deinit(self.allocator);
            result.deinit(self.allocator);
        }
        var dir = compat.fs.getCwd().openDir(defaultIo(), self.base_dir, .{ .iterate = true }) catch |err| switch (err) {
            error.FileNotFound => return result,
            else => return err,
        };
        defer dir.close(defaultIo());
        var iter = dir.iterate();
        while (try iter.next(defaultIo())) |entry| {
            if (entry.kind != .file or !std.mem.endsWith(u8, entry.name, ".jsonl") or std.mem.endsWith(u8, entry.name, stream_suffix)) continue;
            const session_id = entry.name[0 .. entry.name.len - ".jsonl".len];
            var metadata = self.loadIndex(session_id) catch |err| switch (err) {
                error.OutOfMemory => return err,
                else => self.loadMetadata(session_id) catch continue,
            };
            errdefer metadata.deinit(self.allocator);
            if (metadata.title.len == 0) metadata.title = try self.firstUserText(session_id);
            try result.append(self.allocator, metadata);
        }
        return result;
    }

    pub fn deleteSession(self: Store, session_id: []const u8) !void {
        try validateSessionId(session_id);
        const path = try sessionPath(self.allocator, self.base_dir, session_id);
        defer self.allocator.free(path);
        compat.fs.getCwd().deleteFile(defaultIo(), path) catch |err| switch (err) {
            error.FileNotFound => {},
            else => return err,
        };
        inline for (.{ stream_suffix, index_suffix }) |suffix| {
            const auxiliary_path = try sessionFilePath(self.allocator, self.base_dir, session_id, suffix);
            defer self.allocator.free(auxiliary_path);
            compat.fs.getCwd().deleteFile(defaultIo(), auxiliary_path) catch |err| switch (err) {
                error.FileNotFound => {},
                else => return err,
            };
        }
        const dir_path = try std.fs.path.join(self.allocator, &.{ self.base_dir, session_id });
        defer self.allocator.free(dir_path);
        if (compat.fs.getCwd().openDir(defaultIo(), dir_path, .{})) |dir| {
            dir.close(defaultIo());
            try compat.fs.getCwd().deleteTree(defaultIo(), dir_path);
        } else |_| {}
    }

    fn loadMetadata(self: Store, session_id: []const u8) !SessionMetadata {
        const path = try sessionPath(self.allocator, self.base_dir, session_id);
        defer self.allocator.free(path);
        const data = try readLastJsonlLines(self.allocator, path, metadata_max_bytes);
        defer self.allocator.free(data);
        var meta = try defaultMetadata(self.allocator, session_id);
        errdefer meta.deinit(self.allocator);

        var end = data.len;
        while (end > 0) {
            while (end > 0 and (data[end - 1] == '\n' or data[end - 1] == '\r' or data[end - 1] == ' ' or data[end - 1] == '\t')) end -= 1;
            if (end == 0) break;
            const start = if (std.mem.lastIndexOfScalar(u8, data[0..end], '\n')) |idx| idx + 1 else 0;
            const line = std.mem.trim(u8, data[start..end], " \t\r");
            end = if (start == 0) 0 else start - 1;
            if (line.len == 0) continue;
            var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, line, .{}) catch continue;
            defer parsed.deinit();
            const obj = switch (parsed.value) {
                .object => |o| o,
                else => continue,
            };
            if (obj.get("metadata")) |value| switch (value) {
                .object => |meta_obj| {
                    try updateMetadata(self.allocator, &meta, meta_obj);
                    return meta;
                },
                else => {},
            };
        }
        try self.loadHeadMetadata(path, &meta);
        return meta;
    }

    fn firstUserText(self: Store, session_id: []const u8) ![]u8 {
        const path = try sessionPath(self.allocator, self.base_dir, session_id);
        defer self.allocator.free(path);
        var file = compat.fs.getCwd().openFile(defaultIo(), path, .{}) catch return &.{};
        defer file.close(defaultIo());
        const data = try self.allocator.alloc(u8, index_max_bytes);
        defer self.allocator.free(data);
        const read = file.readPositionalAll(defaultIo(), data, 0) catch return &.{};
        var lines = std.mem.splitScalar(u8, data[0..read], '\n');
        while (lines.next()) |line| {
            if (std.mem.indexOf(u8, line, "\"type\":\"message_end\",\"role\":\"user\"") == null) continue;
            var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, line, .{}) catch continue;
            defer parsed.deinit();
            const event = switch (parsed.value) {
                .object => |o| o.get("event") orelse continue,
                else => continue,
            };
            const text = switch (event) {
                .object => |o| stringField(o, "text") orelse continue,
                else => continue,
            };
            if (text.len > 0) return self.allocator.dupe(u8, text);
        }
        return &.{};
    }

    fn loadHeadMetadata(self: Store, path: []const u8, meta: *SessionMetadata) !void {
        var file = try compat.fs.getCwd().openFile(defaultIo(), path, .{});
        defer file.close(defaultIo());
        const data = try self.allocator.alloc(u8, index_max_bytes);
        defer self.allocator.free(data);
        const read = try file.readPositionalAll(defaultIo(), data, 0);
        const line_end = std.mem.indexOfScalar(u8, data[0..read], '\n') orelse read;
        var parsed = std.json.parseFromSlice(std.json.Value, self.allocator, data[0..line_end], .{}) catch return;
        defer parsed.deinit();
        const obj = switch (parsed.value) {
            .object => |o| o,
            else => return,
        };
        if (obj.get("metadata")) |value| switch (value) {
            .object => |meta_obj| try updateMetadata(self.allocator, meta, meta_obj),
            else => {},
        };
    }

    pub fn transcriptPath(self: Store, session_id: []const u8, index: usize) ![]u8 {
        try validateSessionId(session_id);
        const file_name = try std.fmt.allocPrint(self.allocator, "compaction-{d}.jsonl", .{index});
        defer self.allocator.free(file_name);
        return std.fs.path.join(self.allocator, &.{ self.base_dir, session_id, file_name });
    }

    pub fn saveTranscript(self: Store, session_id: []const u8, index: usize, messages: []const ai_types.Message) ![]u8 {
        const path = try self.transcriptPath(session_id, index);
        errdefer self.allocator.free(path);
        try compat.fs.createDir(compat.fs.getCwd(), std.fs.path.dirname(path) orelse ".");
        const data = try serializeTranscript(self.allocator, messages);
        defer self.allocator.free(data);
        try compat.fs.writeFile(compat.fs.getCwd(), path, data);
        return path;
    }

    pub fn resumeSession(self: Store, session_id: []const u8, runtime: *session_runtime.SessionRuntime, workspace_root: ?[]const u8) !LoadedSession {
        var loaded = try self.load(session_id);
        errdefer loaded.deinit(self.allocator);
        if (runtime.remote != null) {
            try runtime.reopenSaved(session_id, workspace_root);
            if (loaded.metadata.model.len > 0) loaded.model_unavailable = !try selectSavedModel(runtime, loaded.metadata.provider, loaded.metadata.model);
            return loaded;
        }
        try runtime.start();
        if (loaded.metadata.model.len > 0) loaded.model_unavailable = !try selectSavedModel(runtime, loaded.metadata.provider, loaded.metadata.model);
        try runtime.replaceMessages(loaded.messages.items);
        return loaded;
    }
};

fn selectSavedModel(runtime: *session_runtime.SessionRuntime, provider: []const u8, model_id: []const u8) !bool {
    for (runtime.availableModels()) |model| {
        if (std.mem.eql(u8, model.id, model_id) and std.mem.eql(u8, model.provider, provider)) {
            try runtime.switchModelExact(model);
            return true;
        }
    }
    runtime.switchModel(model_id) catch |err| switch (err) {
        error.ModelNotFound => return false,
        else => return err,
    };
    return true;
}

fn sessionPath(allocator: std.mem.Allocator, base_dir: []const u8, session_id: []const u8) ![]u8 {
    return sessionFilePath(allocator, base_dir, session_id, ".jsonl");
}

fn sessionFilePath(allocator: std.mem.Allocator, base_dir: []const u8, session_id: []const u8, suffix: []const u8) ![]u8 {
    try validateSessionId(session_id);
    const file_name = try std.fmt.allocPrint(allocator, "{s}{s}", .{ session_id, suffix });
    defer allocator.free(file_name);
    return std.fs.path.join(allocator, &.{ base_dir, file_name });
}

fn serializeIndex(allocator: std.mem.Allocator, meta: SessionMetadata) ![]u8 {
    var buf: std.ArrayList(u8) = .empty;
    errdefer buf.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buf, allocator);
    try w.beginObject();
    try w.writeStringField("session_id", meta.session_id);
    try w.writeStringField("model", meta.model);
    try w.writeStringField("provider", meta.provider);
    try w.writeIntField("created_at", meta.created_at);
    try w.writeIntField("last_active", meta.last_active);
    try w.writeIntField("compaction_offset", meta.compaction_offset);
    try w.writeIntField("compactions", meta.compactions);
    try w.writeStringField("title", meta.title);
    try w.writeBoolField("title_generated", meta.title_generated);
    try w.writeBoolField("title_renamed", meta.title_renamed);
    if (meta.thinking_level) |level| try w.writeStringField("thinking_level", @tagName(level));
    try w.endObject();
    return buf.toOwnedSlice(allocator);
}

fn parseIndex(allocator: std.mem.Allocator, session_id: []const u8, data: []const u8) !SessionMetadata {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, data, .{});
    defer parsed.deinit();
    const obj = switch (parsed.value) {
        .object => |o| o,
        else => return error.InvalidIndex,
    };
    var meta = try defaultMetadata(allocator, session_id);
    errdefer meta.deinit(allocator);
    if (stringField(obj, "model")) |v| try replaceString(allocator, &meta.model, v);
    if (stringField(obj, "provider")) |v| try replaceString(allocator, &meta.provider, v);
    meta.last_active = intField(obj, "last_active") orelse 0;
    meta.created_at = intField(obj, "created_at") orelse 0;
    meta.compaction_offset = if (uint64Field(obj, "compaction_offset")) |v| v else 0;
    meta.compactions = uint32Field(obj, "compactions") orelse 0;
    if (stringField(obj, "title")) |v| {
        if (v.len > 0) meta.title = try allocator.dupe(u8, v);
    }
    meta.title_generated = boolField(obj, "title_generated", false);
    meta.title_renamed = boolField(obj, "title_renamed", false);
    if (stringField(obj, "thinking_level")) |v| meta.thinking_level = std.meta.stringToEnum(ai_types.ThinkingLevel, v);
    return meta;
}

fn validateSessionId(session_id: []const u8) !void {
    if (session_id.len == 0) return error.InvalidSessionId;
    if (std.mem.indexOfScalar(u8, session_id, '/') != null) return error.InvalidSessionId;
    if (std.mem.indexOfScalar(u8, session_id, '\\') != null) return error.InvalidSessionId;
    if (std.mem.indexOf(u8, session_id, "..") != null) return error.InvalidSessionId;
}

fn serializeTranscript(allocator: std.mem.Allocator, messages: []const ai_types.Message) ![]u8 {
    var buf: std.ArrayList(u8) = .empty;
    errdefer buf.deinit(allocator);
    for (messages) |message| {
        var w = json_writer.JsonWriter.init(&buf, allocator);
        try writeTranscriptMessage(&w, message);
        try buf.append(allocator, '\n');
    }
    return buf.toOwnedSlice(allocator);
}

fn writeTranscriptMessage(w: *json_writer.JsonWriter, message: ai_types.Message) !void {
    try w.beginObject();
    switch (message) {
        .user => |user| {
            try w.writeStringField("role", "user");
            switch (user.content) {
                .text => |text| try w.writeStringField("text", text),
                .parts => |parts| try writeTranscriptParts(w, parts),
            }
        },
        .assistant => |assistant| {
            try w.writeStringField("role", "assistant");
            try writeTranscriptAssistant(w, assistant.content);
        },
        .tool_result => |result| {
            try w.writeStringField("role", "tool_result");
            try w.writeStringField("tool_call_id", result.tool_call_id);
            try w.writeStringField("tool_name", result.tool_name);
            if (result.is_error) try w.writeBoolField("is_error", true);
            try writeTranscriptParts(w, result.content);
        },
    }
    try w.endObject();
}

fn writeTranscriptParts(w: *json_writer.JsonWriter, parts: []const ai_types.UserContentPart) !void {
    var text: std.ArrayList(u8) = .empty;
    defer text.deinit(w.allocator);
    var images: usize = 0;
    for (parts) |part| switch (part) {
        .text => |value| try appendParagraph(w.allocator, &text, value.text),
        .image => images += 1,
    };
    try w.writeStringField("text", text.items);
    if (images > 0) try w.writeIntField("images", images);
}

fn writeTranscriptAssistant(w: *json_writer.JsonWriter, content: []const ai_types.AssistantContent) !void {
    var text: std.ArrayList(u8) = .empty;
    defer text.deinit(w.allocator);
    var thinking: std.ArrayList(u8) = .empty;
    defer thinking.deinit(w.allocator);
    var calls: usize = 0;
    for (content) |block| switch (block) {
        .text => |value| try appendParagraph(w.allocator, &text, value.text),
        .thinking => |value| try appendParagraph(w.allocator, &thinking, value.thinking),
        .tool_call => calls += 1,
        .image => {},
    };
    try w.writeStringField("text", text.items);
    if (thinking.items.len > 0) try w.writeStringField("thinking", thinking.items);
    if (calls == 0) return;
    try w.writeKey("tool_calls");
    try w.beginArray();
    for (content) |block| switch (block) {
        .tool_call => |call| {
            try w.beginObject();
            try w.writeStringField("id", call.id);
            try w.writeStringField("name", call.name);
            try w.writeStringField("arguments", call.arguments_json);
            try w.endObject();
        },
        else => {},
    };
    try w.endArray();
}

fn appendParagraph(allocator: std.mem.Allocator, out: *std.ArrayList(u8), text: []const u8) !void {
    if (text.len == 0) return;
    if (out.items.len > 0) try out.append(allocator, '\n');
    try out.appendSlice(allocator, text);
}

fn defaultMetadata(allocator: std.mem.Allocator, session_id: []const u8) !SessionMetadata {
    const id = try allocator.dupe(u8, session_id);
    errdefer allocator.free(id);
    const model = try allocator.dupe(u8, "");
    errdefer allocator.free(model);
    const provider = try allocator.dupe(u8, "");
    return .{ .session_id = id, .model = model, .provider = provider, .last_active = 0 };
}

const LoadLineContext = struct {
    allocator: std.mem.Allocator,
    loaded: *LoadedSession,
    replay: *ReplayState,
    last_compaction: u64 = 0,
};

fn loadLine(ctx: *LoadLineContext, offset: u64, line: []const u8) !void {
    if (loadSkips(line)) return;
    const events_before = ctx.loaded.events.items.len;
    applyLine(ctx.allocator, ctx.loaded, ctx.replay, line) catch |err| switch (err) {
        error.InvalidRecord, error.InvalidEvent, error.SyntaxError, error.UnexpectedToken, error.InvalidNumber, error.DuplicateField, error.UnknownField, error.MissingField, error.LengthMismatch => return,
        else => return err,
    };
    if (ctx.loaded.events.items.len == events_before) return;
    switch (ctx.loaded.events.items[ctx.loaded.events.items.len - 1]) {
        .compaction_end => |payload| if (payload.outcome == .completed and payload.text.slice().len > 0) {
            ctx.last_compaction = offset;
        },
        else => {},
    }
}

fn applyLine(allocator: std.mem.Allocator, loaded: *LoadedSession, replay: *ReplayState, line: []const u8) !void {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, line, .{});
    defer parsed.deinit();
    const obj = switch (parsed.value) {
        .object => |o| o,
        else => return error.InvalidRecord,
    };
    if (obj.get("metadata")) |meta_value| switch (meta_value) {
        .object => |meta_obj| try updateMetadata(allocator, &loaded.metadata, meta_obj),
        else => {},
    };
    if (obj.get("event")) |event_value| {
        const event = try parseEvent(allocator, event_value);
        errdefer {
            var ev = event;
            ev.deinit(allocator);
        }
        try replayEvent(allocator, &loaded.messages, replay, loaded.metadata, event);
        try loaded.events.append(allocator, event);
    }
    if (obj.get("message")) |message_value| {
        const message = try parseMessage(allocator, message_value);
        errdefer {
            var msg = message;
            msg.deinit(allocator);
        }
        try loaded.messages.append(allocator, message);
    }
}

fn replayEvent(allocator: std.mem.Allocator, messages: *std.ArrayList(ai_types.Message), replay: *ReplayState, meta: SessionMetadata, event: session_events.SessionEvent) !void {
    switch (event) {
        .message_start => |payload| {
            replay.current_role = payload.role;
            replay.assistant_text.clearRetainingCapacity();
            replay.tool_call_json.clearRetainingCapacity();
        },
        .text_delta => |payload| if (replay.current_role == .assistant) {
            try replay.assistant_text.appendSlice(allocator, payload.delta.slice());
        },
        .tool_call_delta => |payload| if (replay.current_role == .assistant) {
            try replay.tool_call_json.appendSlice(allocator, payload.delta.slice());
        },
        .message_end => |payload| {
            defer {
                replay.current_role = null;
                replay.assistant_text.clearRetainingCapacity();
                replay.tool_call_json.clearRetainingCapacity();
            }
            switch (payload.role) {
                .user => {
                    if (payload.content_json.slice().len > 0) {
                        try messages.append(allocator, .{ .user = .{ .content = try parseUserContent(allocator, payload.content_json.slice()), .timestamp = compat.time.nowMillis() } });
                    } else if (payload.text.slice().len > 0) {
                        try messages.append(allocator, try userMessage(allocator, payload.text.slice()));
                    }
                },
                .assistant => {
                    if (payload.content_json.slice().len > 0) {
                        try messages.append(allocator, .{ .assistant = try parseAssistantMessageFromContentJson(allocator, meta, payload.content_json.slice(), payload.stop_reason) });
                    } else if (payload.tool_calls_json.slice().len > 0) {
                        try messages.append(allocator, .{ .assistant = try parseAssistantMessageFromContentJson(allocator, meta, payload.tool_calls_json.slice(), payload.stop_reason) });
                    } else if (payload.tool_call_id.slice().len > 0) {
                        try messages.append(allocator, .{ .assistant = try assistantToolCallMessage(allocator, meta, payload.tool_call_id.slice(), payload.tool_name.slice(), payload.args_json.slice()) });
                    } else if (payload.text.slice().len > 0) {
                        try messages.append(allocator, .{ .assistant = try assistantTextMessageWithMeta(allocator, meta, payload.text.slice(), payload.stop_reason) });
                    } else if (replay.assistant_text.items.len > 0) {
                        try messages.append(allocator, .{ .assistant = try assistantTextMessageWithMeta(allocator, meta, replay.assistant_text.items, payload.stop_reason) });
                    } else if (replay.tool_call_json.items.len > 0) {
                        try messages.append(allocator, .{ .assistant = try assistantToolCallMessage(allocator, meta, "", "", replay.tool_call_json.items) });
                    }
                },
                .tool_result => if (payload.tool_call_id.slice().len > 0 and payload.content_json.slice().len > 0) {
                    const message = ai_types.Message{ .tool_result = try parseToolResultFromPayload(allocator, payload) };
                    errdefer {
                        var msg = message;
                        msg.deinit(allocator);
                    }
                    try rememberToolResultMessage(allocator, messages, replay, payload.tool_call_id.slice(), message, .message_end);
                },
            }
        },
        .tool_execution_end => |payload| {
            const message = ai_types.Message{ .tool_result = try toolResultMessage(allocator, payload) };
            errdefer {
                var msg = message;
                msg.deinit(allocator);
            }
            try rememberToolResultMessage(allocator, messages, replay, payload.tool_call_id.slice(), message, .execution_end);
        },
        .compaction_end => |payload| if (payload.outcome == .completed and payload.text.slice().len > 0) {
            var pair = try agent.compaction.historyMessages(allocator, payload.text.slice(), .{ .provider = meta.provider, .model = meta.model });
            errdefer for (&pair) |*message| message.deinit(allocator);
            try messages.ensureTotalCapacity(allocator, pair.len);
            for (messages.items) |*message| message.deinit(allocator);
            messages.clearRetainingCapacity();
            messages.appendSliceAssumeCapacity(&pair);
            for (replay.tool_results.items) |*entry| entry.deinit(allocator);
            replay.tool_results.clearRetainingCapacity();
        },
        else => {},
    }
}

fn findToolResult(replay: *ReplayState, tool_call_id: []const u8) ?usize {
    if (tool_call_id.len == 0) return null;
    for (replay.tool_results.items, 0..) |entry, i| {
        if (std.mem.eql(u8, entry.tool_call_id, tool_call_id)) return i;
    }
    return null;
}

fn rememberToolResultMessage(allocator: std.mem.Allocator, messages: *std.ArrayList(ai_types.Message), replay: *ReplayState, tool_call_id: []const u8, message: ai_types.Message, source: ToolResultSource) !void {
    if (findToolResult(replay, tool_call_id)) |entry_index| {
        const entry = &replay.tool_results.items[entry_index];
        if (entry.source == .execution_end and source == .message_end) {
            messages.items[entry.message_index].deinit(allocator);
            messages.items[entry.message_index] = message;
            entry.source = source;
        } else {
            var unused = message;
            unused.deinit(allocator);
        }
        return;
    }

    try messages.append(allocator, message);
    errdefer _ = messages.pop();
    const owned_id = try allocator.dupe(u8, tool_call_id);
    errdefer allocator.free(owned_id);
    try replay.tool_results.append(allocator, .{ .tool_call_id = owned_id, .message_index = messages.items.len - 1, .source = source });
}

fn updateMetadata(allocator: std.mem.Allocator, meta: *SessionMetadata, obj: std.json.ObjectMap) !void {
    if (stringField(obj, "session_id")) |v| try replaceString(allocator, &meta.session_id, v);
    if (stringField(obj, "model")) |v| try replaceString(allocator, &meta.model, v);
    if (stringField(obj, "provider")) |v| try replaceString(allocator, &meta.provider, v);
    if (intField(obj, "last_active")) |v| meta.last_active = v;
}

fn replaceString(allocator: std.mem.Allocator, target: *[]u8, value: []const u8) !void {
    const next = try allocator.dupe(u8, value);
    if (target.len > 0) allocator.free(target.*);
    target.* = next;
}

fn serializeEventRecord(allocator: std.mem.Allocator, metadata: ?SessionMetadata, event: session_events.SessionEvent) ![]u8 {
    var buf: std.ArrayList(u8) = .empty;
    errdefer buf.deinit(allocator);
    var w = json_writer.JsonWriter.init(&buf, allocator);
    try w.beginObject();
    if (metadata) |meta| try writeMetadata(&w, meta);
    try w.writeKey("event");
    try writeEvent(&w, event);
    try w.endObject();
    try buf.append(allocator, '\n');
    return buf.toOwnedSlice(allocator);
}

fn writeMetadata(w: *json_writer.JsonWriter, meta: SessionMetadata) !void {
    try w.writeKey("metadata");
    try w.beginObject();
    try w.writeStringField("session_id", meta.session_id);
    try w.writeStringField("model", meta.model);
    try w.writeStringField("provider", meta.provider);
    try w.writeIntField("last_active", meta.last_active);
    try w.endObject();
}

fn writeEvent(w: *json_writer.JsonWriter, event: session_events.SessionEvent) !void {
    try w.beginObject();
    switch (event) {
        .agent_start => try w.writeStringField("type", "agent_start"),
        .turn_start => try w.writeStringField("type", "turn_start"),
        .message_start => |p| {
            try w.writeStringField("type", "message_start");
            try w.writeStringField("role", @tagName(p.role));
        },
        .text_delta => |p| {
            try w.writeStringField("type", "text_delta");
            try w.writeIntField("content_index", p.content_index);
            try w.writeStringField("delta", p.delta.slice());
        },
        .thinking_delta => |p| {
            try w.writeStringField("type", "thinking_delta");
            try w.writeIntField("content_index", p.content_index);
            try w.writeStringField("delta", p.delta.slice());
        },
        .tool_call_delta => |p| {
            try w.writeStringField("type", "tool_call_delta");
            try w.writeIntField("content_index", p.content_index);
            try w.writeStringField("delta", p.delta.slice());
        },
        .provider_event => |p| {
            try w.writeStringField("type", "provider_event");
            try w.writeStringField("event_json", p.event_json.slice());
        },
        .message_end => |p| {
            try w.writeStringField("type", "message_end");
            try w.writeStringField("role", @tagName(p.role));
            try w.writeStringField("text", p.text.slice());
            try w.writeStringField("content_json", p.content_json.slice());
            try w.writeStringField("tool_call_id", p.tool_call_id.slice());
            try w.writeStringField("tool_name", p.tool_name.slice());
            try w.writeStringField("args_json", p.args_json.slice());
            try w.writeStringField("tool_calls_json", p.tool_calls_json.slice());
            try w.writeStringField("details_json", p.details_json.slice());
            try w.writeStringField("artifacts_json", p.artifacts_json.slice());
            try w.writeStringField("stop_reason", @tagName(p.stop_reason));
            try w.writeBoolField("is_error", p.is_error);
        },
        .tool_approval_requested => |p| {
            try w.writeStringField("type", "tool_approval_requested");
            try writeToolFields(w, p.tool_call_id.slice(), p.tool_name.slice(), p.args_json.slice());
        },
        .tool_execution_start => |p| {
            try w.writeStringField("type", "tool_execution_start");
            try writeToolFields(w, p.tool_call_id.slice(), p.tool_name.slice(), p.args_json.slice());
        },
        .tool_execution_update => |p| {
            try w.writeStringField("type", "tool_execution_update");
            try writeToolFields(w, p.tool_call_id.slice(), p.tool_name.slice(), p.args_json.slice());
            try w.writeStringField("partial_result_json", p.partial_result_json.slice());
        },
        .tool_execution_end => |p| {
            try w.writeStringField("type", "tool_execution_end");
            try w.writeStringField("tool_call_id", p.tool_call_id.slice());
            try w.writeStringField("tool_name", p.tool_name.slice());
            try w.writeStringField("result_json", p.result_json.slice());
            try w.writeBoolField("is_error", p.is_error);
            try w.writeIntField("raw_total_bytes", p.raw_total_bytes);
            try w.writeIntField("returned_total_bytes", p.returned_total_bytes);
            try w.writeIntField("estimated_returned_tokens", p.estimated_returned_tokens);
            try w.writeIntField("artifact_count", p.artifact_count);
            try w.writeStringField("artifact_refs", p.artifact_refs.slice());
        },
        .context_usage => |p| {
            try w.writeStringField("type", "context_usage");
            try w.writeIntField("system_prompt_bytes", p.system_prompt_bytes);
            try w.writeIntField("message_bytes", p.message_bytes);
            try w.writeIntField("tool_definition_bytes", p.tool_definition_bytes);
            try w.writeIntField("total_bytes", p.total_bytes);
            try w.writeIntField("estimated_tokens", p.estimated_tokens);
            try w.writeIntField("message_count", p.message_count);
            try w.writeIntField("tool_count", p.tool_count);
        },
        .prompt_segment_usage => |p| {
            try w.writeStringField("type", "prompt_segment_usage");
            try w.writeStringField("segment", @tagName(p.segment));
            try w.writeStringField("cache_role", @tagName(p.cache_role));
            try w.writeIntField("bytes", p.bytes);
            try w.writeIntField("estimated_tokens", p.estimated_tokens);
            try w.writeIntField("item_count", p.item_count);
        },
        .turn_end => |p| {
            try w.writeStringField("type", "turn_end");
            try w.writeStringField("stop_reason", @tagName(p.stop_reason));
        },
        .agent_end => |p| {
            try w.writeStringField("type", "agent_end");
            try w.writeStringField("reason", @tagName(p.reason));
        },
        .system_warning => |p| {
            try w.writeStringField("type", "system_warning");
            try w.writeStringField("message", p.message.slice());
        },
        .backpressure_status => |p| {
            try w.writeStringField("type", "backpressure_status");
            try w.writeBoolField("active", p.active);
            try w.writeIntField("dropped_count", p.dropped_count);
        },
        .compaction_start => |p| {
            try w.writeStringField("type", "compaction_start");
            if (p.in_run) try w.writeBoolField("in_run", true);
        },
        .compaction_end => |p| {
            try w.writeStringField("type", "compaction_end");
            try w.writeStringField("outcome", @tagName(p.outcome));
            if (p.in_run) try w.writeBoolField("in_run", true);
            try w.writeStringField("text", p.text.slice());
            try w.writeStringField("transcript", p.transcript.slice());
            try w.writeStringField("message", p.message.slice());
            try w.writeIntField("messages_before", p.messages_before);
            try w.writeIntField("tokens_before", p.tokens_before);
            try w.writeIntField("tokens_after", p.tokens_after);
        },
        .@"error" => |p| {
            try w.writeStringField("type", "error");
            try w.writeStringField("message", p.message.slice());
        },
    }
    try w.endObject();
}

fn writeToolFields(w: *json_writer.JsonWriter, id: []const u8, name: []const u8, args: []const u8) !void {
    try w.writeStringField("tool_call_id", id);
    try w.writeStringField("tool_name", name);
    try w.writeStringField("args_json", args);
}

fn parseEvent(allocator: std.mem.Allocator, value: std.json.Value) !session_events.SessionEvent {
    const obj = switch (value) {
        .object => |o| o,
        else => return error.InvalidEvent,
    };
    const kind = stringField(obj, "type") orelse return error.InvalidEvent;
    if (std.mem.eql(u8, kind, "agent_start")) return .{ .agent_start = .{} };
    if (std.mem.eql(u8, kind, "turn_start")) return .{ .turn_start = .{} };
    if (std.mem.eql(u8, kind, "message_start")) return .{ .message_start = .{ .role = parseRole(stringField(obj, "role") orelse "assistant") } };
    if (std.mem.eql(u8, kind, "text_delta")) return .{ .text_delta = .{ .content_index = uintField(obj, "content_index") orelse 0, .delta = try owned(allocator, stringField(obj, "delta") orelse "") } };
    if (std.mem.eql(u8, kind, "thinking_delta")) return .{ .thinking_delta = .{ .content_index = uintField(obj, "content_index") orelse 0, .delta = try owned(allocator, stringField(obj, "delta") orelse "") } };
    if (std.mem.eql(u8, kind, "tool_call_delta")) return .{ .tool_call_delta = .{ .content_index = uintField(obj, "content_index") orelse 0, .delta = try owned(allocator, stringField(obj, "delta") orelse "") } };
    if (std.mem.eql(u8, kind, "provider_event")) return .{ .provider_event = .{ .event_json = try owned(allocator, stringField(obj, "event_json") orelse "") } };
    if (std.mem.eql(u8, kind, "message_end")) {
        const text = try allocator.dupe(u8, stringField(obj, "text") orelse "");
        errdefer allocator.free(text);
        const content_json = try allocator.dupe(u8, stringField(obj, "content_json") orelse "");
        errdefer allocator.free(content_json);
        const tool_call_id = try allocator.dupe(u8, stringField(obj, "tool_call_id") orelse "");
        errdefer allocator.free(tool_call_id);
        const tool_name = try allocator.dupe(u8, stringField(obj, "tool_name") orelse "");
        errdefer allocator.free(tool_name);
        const args_json = try allocator.dupe(u8, stringField(obj, "args_json") orelse "");
        errdefer allocator.free(args_json);
        const tool_calls_json = try allocator.dupe(u8, stringField(obj, "tool_calls_json") orelse "");
        errdefer allocator.free(tool_calls_json);
        const details_json = try allocator.dupe(u8, stringField(obj, "details_json") orelse "");
        errdefer allocator.free(details_json);
        const artifacts_json = try allocator.dupe(u8, stringField(obj, "artifacts_json") orelse "");

        return .{ .message_end = .{
            .role = parseRole(stringField(obj, "role") orelse "assistant"),
            .text = OwnedSlice(u8).initOwned(text),
            .content_json = OwnedSlice(u8).initOwned(content_json),
            .tool_call_id = OwnedSlice(u8).initOwned(tool_call_id),
            .tool_name = OwnedSlice(u8).initOwned(tool_name),
            .args_json = OwnedSlice(u8).initOwned(args_json),
            .tool_calls_json = OwnedSlice(u8).initOwned(tool_calls_json),
            .details_json = OwnedSlice(u8).initOwned(details_json),
            .artifacts_json = OwnedSlice(u8).initOwned(artifacts_json),
            .stop_reason = parseStopReason(stringField(obj, "stop_reason") orelse "stop"),
            .is_error = boolField(obj, "is_error", false),
        } };
    }
    if (std.mem.eql(u8, kind, "tool_approval_requested")) {
        const tool_call_id = try allocator.dupe(u8, stringField(obj, "tool_call_id") orelse "");
        errdefer allocator.free(tool_call_id);
        const tool_name = try allocator.dupe(u8, stringField(obj, "tool_name") orelse "");
        errdefer allocator.free(tool_name);
        const args_json = try allocator.dupe(u8, stringField(obj, "args_json") orelse "");

        return .{ .tool_approval_requested = .{
            .tool_call_id = OwnedSlice(u8).initOwned(tool_call_id),
            .tool_name = OwnedSlice(u8).initOwned(tool_name),
            .args_json = OwnedSlice(u8).initOwned(args_json),
        } };
    }
    if (std.mem.eql(u8, kind, "tool_execution_start")) {
        const tool_call_id = try allocator.dupe(u8, stringField(obj, "tool_call_id") orelse "");
        errdefer allocator.free(tool_call_id);
        const tool_name = try allocator.dupe(u8, stringField(obj, "tool_name") orelse "");
        errdefer allocator.free(tool_name);
        const args_json = try allocator.dupe(u8, stringField(obj, "args_json") orelse "");

        return .{ .tool_execution_start = .{
            .tool_call_id = OwnedSlice(u8).initOwned(tool_call_id),
            .tool_name = OwnedSlice(u8).initOwned(tool_name),
            .args_json = OwnedSlice(u8).initOwned(args_json),
        } };
    }
    if (std.mem.eql(u8, kind, "tool_execution_update")) {
        const tool_call_id = try allocator.dupe(u8, stringField(obj, "tool_call_id") orelse "");
        errdefer allocator.free(tool_call_id);
        const tool_name = try allocator.dupe(u8, stringField(obj, "tool_name") orelse "");
        errdefer allocator.free(tool_name);
        const args_json = try allocator.dupe(u8, stringField(obj, "args_json") orelse "");
        errdefer allocator.free(args_json);
        const partial_result_json = try allocator.dupe(u8, stringField(obj, "partial_result_json") orelse "");

        return .{ .tool_execution_update = .{
            .tool_call_id = OwnedSlice(u8).initOwned(tool_call_id),
            .tool_name = OwnedSlice(u8).initOwned(tool_name),
            .args_json = OwnedSlice(u8).initOwned(args_json),
            .partial_result_json = OwnedSlice(u8).initOwned(partial_result_json),
        } };
    }
    if (std.mem.eql(u8, kind, "tool_execution_end")) {
        const tool_call_id = try allocator.dupe(u8, stringField(obj, "tool_call_id") orelse "");
        errdefer allocator.free(tool_call_id);
        const tool_name = try allocator.dupe(u8, stringField(obj, "tool_name") orelse "");
        errdefer allocator.free(tool_name);
        const result_json = try allocator.dupe(u8, stringField(obj, "result_json") orelse "");
        errdefer allocator.free(result_json);
        const artifact_refs = try allocator.dupe(u8, stringField(obj, "artifact_refs") orelse "");

        return .{ .tool_execution_end = .{
            .tool_call_id = OwnedSlice(u8).initOwned(tool_call_id),
            .tool_name = OwnedSlice(u8).initOwned(tool_name),
            .result_json = OwnedSlice(u8).initOwned(result_json),
            .is_error = boolField(obj, "is_error", false),
            .raw_total_bytes = uint64Field(obj, "raw_total_bytes") orelse 0,
            .returned_total_bytes = uint64Field(obj, "returned_total_bytes") orelse 0,
            .estimated_returned_tokens = uint64Field(obj, "estimated_returned_tokens") orelse 0,
            .artifact_count = uint32Field(obj, "artifact_count") orelse 0,
            .artifact_refs = OwnedSlice(u8).initOwned(artifact_refs),
        } };
    }
    if (std.mem.eql(u8, kind, "context_usage")) return .{ .context_usage = .{
        .system_prompt_bytes = uint64Field(obj, "system_prompt_bytes") orelse 0,
        .message_bytes = uint64Field(obj, "message_bytes") orelse 0,
        .tool_definition_bytes = uint64Field(obj, "tool_definition_bytes") orelse 0,
        .total_bytes = uint64Field(obj, "total_bytes") orelse 0,
        .estimated_tokens = uint64Field(obj, "estimated_tokens") orelse 0,
        .message_count = uint32Field(obj, "message_count") orelse 0,
        .tool_count = uint32Field(obj, "tool_count") orelse 0,
    } };
    if (std.mem.eql(u8, kind, "prompt_segment_usage")) return .{ .prompt_segment_usage = .{
        .segment = parsePromptSegmentKind(stringField(obj, "segment") orelse "message_history"),
        .cache_role = parsePromptSegmentCacheRole(stringField(obj, "cache_role") orelse "dynamic"),
        .bytes = uint64Field(obj, "bytes") orelse 0,
        .estimated_tokens = uint64Field(obj, "estimated_tokens") orelse 0,
        .item_count = uint32Field(obj, "item_count") orelse 0,
    } };
    if (std.mem.eql(u8, kind, "turn_end")) return .{ .turn_end = .{ .stop_reason = parseStopReason(stringField(obj, "stop_reason") orelse "stop") } };
    if (std.mem.eql(u8, kind, "agent_end")) return .{ .agent_end = .{ .reason = parseEndReason(stringField(obj, "reason") orelse "completed") } };
    if (std.mem.eql(u8, kind, "system_warning")) return .{ .system_warning = .{ .message = try owned(allocator, stringField(obj, "message") orelse "") } };
    if (std.mem.eql(u8, kind, "backpressure_status")) return .{ .backpressure_status = .{
        .active = boolField(obj, "active", false),
        .dropped_count = uint64Field(obj, "dropped_count") orelse 0,
    } };
    if (std.mem.eql(u8, kind, "error")) return .{ .@"error" = .{ .message = try owned(allocator, stringField(obj, "message") orelse "") } };
    if (std.mem.eql(u8, kind, "compaction_start")) return .{ .compaction_start = .{ .in_run = boolField(obj, "in_run", false) } };
    if (std.mem.eql(u8, kind, "compaction_end")) {
        const text = try allocator.dupe(u8, stringField(obj, "text") orelse "");
        errdefer allocator.free(text);
        const transcript = try allocator.dupe(u8, stringField(obj, "transcript") orelse "");
        errdefer allocator.free(transcript);
        const message = try allocator.dupe(u8, stringField(obj, "message") orelse "");

        return .{ .compaction_end = .{
            .in_run = boolField(obj, "in_run", false),
            .outcome = parseCompactionOutcome(stringField(obj, "outcome") orelse "failed"),
            .text = OwnedSlice(u8).initOwned(text),
            .transcript = OwnedSlice(u8).initOwned(transcript),
            .message = OwnedSlice(u8).initOwned(message),
            .messages_before = uint64Field(obj, "messages_before") orelse 0,
            .tokens_before = uint64Field(obj, "tokens_before") orelse 0,
            .tokens_after = uint64Field(obj, "tokens_after") orelse 0,
        } };
    }
    return error.InvalidEvent;
}

fn owned(allocator: std.mem.Allocator, value: []const u8) !OwnedSlice(u8) {
    return OwnedSlice(u8).initOwned(try allocator.dupe(u8, value));
}

fn userMessage(allocator: std.mem.Allocator, text: []const u8) !ai_types.Message {
    return .{ .user = .{ .content = .{ .text = try allocator.dupe(u8, text) }, .timestamp = compat.time.nowMillis() } };
}

fn parseUserContent(allocator: std.mem.Allocator, json: []const u8) !ai_types.UserContent {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, json, .{});
    defer parsed.deinit();
    const arr = switch (parsed.value) {
        .array => |a| a,
        else => return error.InvalidMessage,
    };
    const parts = try allocator.alloc(ai_types.UserContentPart, arr.items.len);
    var initialized: usize = 0;
    errdefer {
        for (parts[0..initialized]) |*part| part.deinit(allocator);
        allocator.free(parts);
    }
    for (arr.items, 0..) |item, i| {
        parts[i] = try parseUserContentPart(allocator, item);
        initialized += 1;
    }
    return .{ .parts = parts };
}

fn parseUserParts(allocator: std.mem.Allocator, json: []const u8) ![]const ai_types.UserContentPart {
    const content = try parseUserContent(allocator, json);
    return switch (content) {
        .parts => |parts| parts,
        .text => error.InvalidMessage,
    };
}

fn parseUserContentPart(allocator: std.mem.Allocator, value: std.json.Value) !ai_types.UserContentPart {
    const obj = switch (value) {
        .object => |o| o,
        else => return error.InvalidMessage,
    };
    const kind = stringField(obj, "type") orelse return error.InvalidMessage;
    if (std.mem.eql(u8, kind, "text")) {
        const text = try allocator.dupe(u8, stringField(obj, "text") orelse "");
        errdefer allocator.free(text);
        const text_signature: ?[]u8 = if (stringField(obj, "text_signature")) |sig| try allocator.dupe(u8, sig) else null;

        return .{ .text = .{ .text = text, .text_signature = text_signature } };
    }
    if (std.mem.eql(u8, kind, "image")) {
        const data = try allocator.dupe(u8, stringField(obj, "data") orelse "");
        errdefer allocator.free(data);
        const mime_type = try allocator.dupe(u8, stringField(obj, "mime_type") orelse "");

        return .{ .image = .{ .data = data, .mime_type = mime_type } };
    }
    return error.InvalidMessage;
}

fn parseAssistantMessageFromContentJson(allocator: std.mem.Allocator, meta: SessionMetadata, json: []const u8, fallback_stop_reason: ai_types.StopReason) !ai_types.AssistantMessage {
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, json, .{});
    defer parsed.deinit();
    const arr = switch (parsed.value) {
        .array => |a| a,
        else => return error.InvalidMessage,
    };
    const content = try allocator.alloc(ai_types.AssistantContent, arr.items.len);
    var initialized: usize = 0;
    var has_tool_call = false;
    errdefer {
        deinitAssistantContentPrefix(allocator, content[0..initialized]);
        allocator.free(content);
    }
    for (arr.items, 0..) |item, i| {
        content[i] = try parseAssistantContent(allocator, item);
        if (content[i] == .tool_call) has_tool_call = true;
        initialized += 1;
    }
    const stop_reason: ai_types.StopReason = if (fallback_stop_reason == .stop and has_tool_call) .tool_use else fallback_stop_reason;
    return assistantMessage(allocator, meta, content, stop_reason);
}

fn parseAssistantContent(allocator: std.mem.Allocator, value: std.json.Value) !ai_types.AssistantContent {
    const obj = switch (value) {
        .object => |o| o,
        else => return error.InvalidMessage,
    };
    const kind = stringField(obj, "type") orelse return error.InvalidMessage;
    if (std.mem.eql(u8, kind, "text")) {
        const text = try allocator.dupe(u8, stringField(obj, "text") orelse "");
        errdefer allocator.free(text);
        const text_signature: ?[]u8 = if (stringField(obj, "text_signature")) |sig| try allocator.dupe(u8, sig) else null;

        return .{ .text = .{ .text = text, .text_signature = text_signature } };
    }
    if (std.mem.eql(u8, kind, "thinking")) {
        const thinking = try allocator.dupe(u8, stringField(obj, "thinking") orelse "");
        errdefer allocator.free(thinking);
        const thinking_signature: ?[]u8 = if (stringField(obj, "thinking_signature")) |sig| try allocator.dupe(u8, sig) else null;

        return .{ .thinking = .{ .thinking = thinking, .thinking_signature = thinking_signature } };
    }
    if (std.mem.eql(u8, kind, "tool_call")) {
        const id = try allocator.dupe(u8, stringField(obj, "id") orelse "");
        errdefer allocator.free(id);
        const name = try allocator.dupe(u8, stringField(obj, "name") orelse "");
        errdefer allocator.free(name);
        const arguments_json = try allocator.dupe(u8, stringField(obj, "arguments_json") orelse "");
        errdefer allocator.free(arguments_json);
        const thought_signature: ?[]u8 = if (stringField(obj, "thought_signature")) |sig| try allocator.dupe(u8, sig) else null;

        return .{ .tool_call = .{
            .id = id,
            .name = name,
            .arguments_json = arguments_json,
            .thought_signature = thought_signature,
        } };
    }
    if (std.mem.eql(u8, kind, "image")) {
        const data = try allocator.dupe(u8, stringField(obj, "data") orelse "");
        errdefer allocator.free(data);
        const mime_type = try allocator.dupe(u8, stringField(obj, "mime_type") orelse "");

        return .{ .image = .{ .data = data, .mime_type = mime_type } };
    }
    return error.InvalidMessage;
}

fn parseToolResultFromPayload(allocator: std.mem.Allocator, payload: anytype) !ai_types.ToolResultMessage {
    const tool_call_id = try allocator.dupe(u8, payload.tool_call_id.slice());
    errdefer allocator.free(tool_call_id);

    const tool_name = try allocator.dupe(u8, payload.tool_name.slice());
    errdefer allocator.free(tool_name);

    const content = try parseUserParts(allocator, payload.content_json.slice());
    errdefer {
        const mutable_content: []ai_types.UserContentPart = @constCast(content);
        for (mutable_content) |*part| part.deinit(allocator);
        allocator.free(content);
    }

    const details_json = try allocator.dupe(u8, payload.details_json.slice());
    errdefer allocator.free(details_json);

    const artifacts = try parseArtifacts(allocator, payload.artifacts_json.slice());

    return .{
        .tool_call_id = tool_call_id,
        .tool_name = tool_name,
        .content = content,
        .details_json = OwnedSlice(u8).initOwned(details_json),
        .artifacts = OwnedSlice(ai_types.ArtifactReference).initOwned(artifacts),
        .is_error = payload.is_error,
        .timestamp = compat.time.nowMillis(),
    };
}

fn parseArtifacts(allocator: std.mem.Allocator, json: []const u8) ![]ai_types.ArtifactReference {
    if (json.len == 0) return allocator.alloc(ai_types.ArtifactReference, 0);
    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, json, .{});
    defer parsed.deinit();
    const arr = switch (parsed.value) {
        .array => |a| a,
        else => return error.InvalidMessage,
    };
    const artifacts = try allocator.alloc(ai_types.ArtifactReference, arr.items.len);
    var initialized: usize = 0;
    errdefer {
        for (artifacts[0..initialized]) |*artifact| artifact.deinit(allocator);
        allocator.free(artifacts);
    }
    for (arr.items, 0..) |item, i| {
        const obj = switch (item) {
            .object => |o| o,
            else => return error.InvalidMessage,
        };

        const artifact_id = try allocator.dupe(u8, stringField(obj, "artifact_id") orelse "");
        errdefer allocator.free(artifact_id);

        const uri = try allocator.dupe(u8, stringField(obj, "uri") orelse "");
        errdefer allocator.free(uri);

        const mime_type = try allocator.dupe(u8, stringField(obj, "mime_type") orelse "");
        errdefer allocator.free(mime_type);

        const sha256 = try allocator.dupe(u8, stringField(obj, "sha256") orelse "");
        errdefer allocator.free(sha256);

        const description = try allocator.dupe(u8, stringField(obj, "description") orelse "");

        artifacts[i] = .{
            .artifact_id = artifact_id,
            .uri = OwnedSlice(u8).initOwned(uri),
            .mime_type = OwnedSlice(u8).initOwned(mime_type),
            .byte_size = uintField(obj, "byte_size"),
            .sha256 = OwnedSlice(u8).initOwned(sha256),
            .description = OwnedSlice(u8).initOwned(description),
        };
        initialized += 1;
    }
    return artifacts;
}

fn deinitAssistantContentPrefix(allocator: std.mem.Allocator, content: []ai_types.AssistantContent) void {
    for (content) |block| switch (block) {
        .text => |text| {
            allocator.free(text.text);
            if (text.text_signature) |sig| allocator.free(sig);
        },
        .thinking => |thinking| {
            allocator.free(thinking.thinking);
            if (thinking.thinking_signature) |sig| allocator.free(sig);
        },
        .tool_call => |tool| {
            allocator.free(tool.id);
            allocator.free(tool.name);
            allocator.free(tool.arguments_json);
            if (tool.thought_signature) |sig| allocator.free(sig);
        },
        .image => |image| {
            allocator.free(image.data);
            allocator.free(image.mime_type);
        },
    };
}

fn assistantTextMessage(allocator: std.mem.Allocator, text: []const u8, stop_reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const meta = SessionMetadata{
        .session_id = @constCast(""),
        .model = @constCast(""),
        .provider = @constCast(""),
        .last_active = 0,
    };
    return assistantTextMessageWithMeta(allocator, meta, text, stop_reason);
}

fn assistantTextMessageWithMeta(allocator: std.mem.Allocator, meta: SessionMetadata, text: []const u8, stop_reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const owned_text = try allocator.dupe(u8, text);
    errdefer allocator.free(owned_text);

    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(content);
    content[0] = .{ .text = .{ .text = owned_text } };

    return assistantMessage(allocator, meta, content, stop_reason);
}

fn assistantToolCallMessage(allocator: std.mem.Allocator, meta: SessionMetadata, id: []const u8, name: []const u8, args_json: []const u8) !ai_types.AssistantMessage {
    const owned_id = try allocator.dupe(u8, id);
    errdefer allocator.free(owned_id);

    const owned_name = try allocator.dupe(u8, name);
    errdefer allocator.free(owned_name);

    const owned_args_json = try allocator.dupe(u8, args_json);
    errdefer allocator.free(owned_args_json);

    const content = try allocator.alloc(ai_types.AssistantContent, 1);
    errdefer allocator.free(content);
    content[0] = .{ .tool_call = .{
        .id = owned_id,
        .name = owned_name,
        .arguments_json = owned_args_json,
    } };

    return assistantMessage(allocator, meta, content, .tool_use);
}

fn assistantMessage(allocator: std.mem.Allocator, meta: SessionMetadata, content: []const ai_types.AssistantContent, stop_reason: ai_types.StopReason) !ai_types.AssistantMessage {
    const api = try allocator.dupe(u8, "");
    errdefer allocator.free(api);
    const provider = try allocator.dupe(u8, meta.provider);
    errdefer allocator.free(provider);
    const model = try allocator.dupe(u8, meta.model);
    errdefer allocator.free(model);
    return .{
        .content = content,
        .api = api,
        .provider = provider,
        .model = model,
        .usage = .{},
        .stop_reason = stop_reason,
        .timestamp = compat.time.nowMillis(),
        .is_owned = true,
    };
}

fn toolResultMessage(allocator: std.mem.Allocator, p: anytype) !ai_types.ToolResultMessage {
    return toolResultFromFields(allocator, p.tool_call_id.slice(), p.tool_name.slice(), p.result_json.slice(), p.is_error);
}

fn toolResultFromFields(allocator: std.mem.Allocator, tool_call_id: []const u8, tool_name: []const u8, result: []const u8, is_error: bool) !ai_types.ToolResultMessage {
    const part_text = try allocator.dupe(u8, result);
    errdefer allocator.free(part_text);

    const parts = try allocator.alloc(ai_types.UserContentPart, 1);
    errdefer allocator.free(parts);
    parts[0] = .{ .text = .{ .text = part_text } };

    const owned_tool_call_id = try allocator.dupe(u8, tool_call_id);
    errdefer allocator.free(owned_tool_call_id);

    const owned_tool_name = try allocator.dupe(u8, tool_name);
    errdefer allocator.free(owned_tool_name);

    const details_json = try allocator.dupe(u8, result);

    return .{
        .tool_call_id = owned_tool_call_id,
        .tool_name = owned_tool_name,
        .content = parts,
        .details_json = OwnedSlice(u8).initOwned(details_json),
        .is_error = is_error,
        .timestamp = compat.time.nowMillis(),
    };
}

fn parseMessage(allocator: std.mem.Allocator, value: std.json.Value) !ai_types.Message {
    const obj = switch (value) {
        .object => |o| o,
        else => return error.InvalidMessage,
    };
    const role = stringField(obj, "role") orelse return error.InvalidMessage;
    if (std.mem.eql(u8, role, "user")) return .{ .user = .{ .content = .{ .text = try allocator.dupe(u8, stringField(obj, "text") orelse "") }, .timestamp = intField(obj, "timestamp") orelse 0 } };
    if (std.mem.eql(u8, role, "assistant")) return .{ .assistant = try assistantTextMessage(allocator, stringField(obj, "text") orelse "", parseStopReason(stringField(obj, "stop_reason") orelse "stop")) };
    if (std.mem.eql(u8, role, "tool_result")) {
        const part_text = try allocator.dupe(u8, stringField(obj, "text") orelse "");
        errdefer allocator.free(part_text);

        const parts = try allocator.alloc(ai_types.UserContentPart, 1);
        errdefer allocator.free(parts);
        parts[0] = .{ .text = .{ .text = part_text } };

        const tool_call_id = try allocator.dupe(u8, stringField(obj, "tool_call_id") orelse "");
        errdefer allocator.free(tool_call_id);

        const tool_name = try allocator.dupe(u8, stringField(obj, "tool_name") orelse "");
        errdefer allocator.free(tool_name);

        const details_json = try allocator.dupe(u8, stringField(obj, "details_json") orelse "");

        return .{ .tool_result = .{
            .tool_call_id = tool_call_id,
            .tool_name = tool_name,
            .content = parts,
            .details_json = OwnedSlice(u8).initOwned(details_json),
            .is_error = boolField(obj, "is_error", false),
            .timestamp = intField(obj, "timestamp") orelse 0,
        } };
    }
    return error.InvalidMessage;
}

fn stringField(obj: std.json.ObjectMap, key: []const u8) ?[]const u8 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .string => |s| s,
        else => null,
    };
}

fn boolField(obj: std.json.ObjectMap, key: []const u8, default: bool) bool {
    const value = obj.get(key) orelse return default;
    return switch (value) {
        .bool => |b| b,
        else => default,
    };
}

fn intField(obj: std.json.ObjectMap, key: []const u8) ?i64 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |i| @intCast(i),
        else => null,
    };
}

fn uintField(obj: std.json.ObjectMap, key: []const u8) ?usize {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |i| if (i >= 0) @intCast(i) else null,
        else => null,
    };
}

fn uint64Field(obj: std.json.ObjectMap, key: []const u8) ?u64 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |i| if (i >= 0) @intCast(i) else null,
        else => null,
    };
}

fn uint32Field(obj: std.json.ObjectMap, key: []const u8) ?u32 {
    const value = obj.get(key) orelse return null;
    return switch (value) {
        .integer => |i| if (i >= 0 and i <= std.math.maxInt(u32)) @intCast(i) else null,
        else => null,
    };
}

fn parseRole(value: []const u8) session_events.SessionEvent.MessageRole {
    if (std.mem.eql(u8, value, "user")) return .user;
    if (std.mem.eql(u8, value, "tool_result")) return .tool_result;
    return .assistant;
}

fn parsePromptSegmentKind(value: []const u8) session_events.SessionEvent.PromptSegmentKind {
    if (std.mem.eql(u8, value, "system_prompt")) return .system_prompt;
    if (std.mem.eql(u8, value, "tool_definitions")) return .tool_definitions;
    return .message_history;
}

fn parsePromptSegmentCacheRole(value: []const u8) session_events.SessionEvent.PromptSegmentCacheRole {
    if (std.mem.eql(u8, value, "stable")) return .stable;
    return .dynamic;
}

fn parseStopReason(value: []const u8) ai_types.StopReason {
    if (std.mem.eql(u8, value, "length")) return .length;
    if (std.mem.eql(u8, value, "tool_use")) return .tool_use;
    if (std.mem.eql(u8, value, "content_filter")) return .content_filter;
    if (std.mem.eql(u8, value, "error")) return .@"error";
    if (std.mem.eql(u8, value, "aborted")) return .aborted;
    return .stop;
}

fn parseCompactionOutcome(value: []const u8) session_events.SessionEvent.CompactionOutcome {
    if (std.mem.eql(u8, value, "completed")) return .completed;
    if (std.mem.eql(u8, value, "cancelled")) return .cancelled;
    return .failed;
}

fn parseEndReason(value: []const u8) session_events.SessionEndReason {
    if (std.mem.eql(u8, value, "cancelled")) return .cancelled;
    if (std.mem.eql(u8, value, "error")) return .@"error";
    return .completed;
}

test "uint32 telemetry counters above max fall back to zero" {
    var parsed = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, "{\"type\":\"context_usage\",\"message_count\":4294967296}", .{});
    defer parsed.deinit();
    var event = try parseEvent(std.testing.allocator, parsed.value);
    defer event.deinit(std.testing.allocator);
    try std.testing.expect(event == .context_usage);
    try std.testing.expectEqual(@as(u32, 0), event.context_usage.message_count);
}

test "save 10 events load replays in order" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try defaultMetadata(std.testing.allocator, "s1");
    defer meta.deinit(std.testing.allocator);
    try replaceString(std.testing.allocator, &meta.model, "model-a");
    try replaceString(std.testing.allocator, &meta.provider, "test");
    var i: usize = 0;
    while (i < 10) : (i += 1) {
        meta.last_active = @intCast(i + 1);
        const delta = try std.fmt.allocPrint(std.testing.allocator, "d{d}", .{i});
        defer std.testing.allocator.free(delta);
        var event = session_events.SessionEvent{ .text_delta = .{ .content_index = i, .delta = try owned(std.testing.allocator, delta) } };
        defer event.deinit(std.testing.allocator);
        try store.save(meta, event);
    }
    var loaded = try store.load("s1");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 10), loaded.events.items.len);
    for (loaded.events.items, 0..) |event, idx| try std.testing.expectEqual(idx, event.text_delta.content_index);
}

test "corrupted JSONL line is skipped" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try defaultMetadata(std.testing.allocator, "s2");
    defer meta.deinit(std.testing.allocator);
    try store.save(meta, .{ .turn_start = .{} });
    const path = try sessionPath(std.testing.allocator, base, "s2");
    defer std.testing.allocator.free(path);
    try appendFile(path, "{bad json}\n");
    try store.save(meta, .{ .agent_end = .{ .reason = .completed } });
    var loaded = try store.load("s2");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 2), loaded.events.items.len);
    try std.testing.expect(loaded.events.items[0] == .turn_start);
    try std.testing.expect(loaded.events.items[1] == .agent_end);
}

test "invalid session id is rejected" {
    try std.testing.expectError(error.InvalidSessionId, validateSessionId("../escape"));
    try std.testing.expectError(error.InvalidSessionId, validateSessionId("foo/bar"));
    try std.testing.expectError(error.InvalidSessionId, validateSessionId("foo\\bar"));
    try validateSessionId("session-123");
}

test "session metadata updates from last valid line" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try defaultMetadata(std.testing.allocator, "s3");
    defer meta.deinit(std.testing.allocator);
    try replaceString(std.testing.allocator, &meta.model, "m1");
    try replaceString(std.testing.allocator, &meta.provider, "p1");
    meta.last_active = 20;
    try store.save(meta, .{ .turn_start = .{} });
    meta.last_active = 30;
    try store.save(meta, .{ .turn_start = .{} });
    var list = try store.list();
    defer {
        for (list.items) |*item| item.deinit(std.testing.allocator);
        list.deinit(std.testing.allocator);
    }
    try std.testing.expectEqual(@as(usize, 1), list.items.len);
    try std.testing.expectEqual(@as(i64, 30), list.items[0].last_active);
}

test "metadata loads from tail of large session file" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try defaultMetadata(std.testing.allocator, "large-metadata");
    defer meta.deinit(std.testing.allocator);
    try replaceString(std.testing.allocator, &meta.model, "tail-model");
    try replaceString(std.testing.allocator, &meta.provider, "tail-provider");
    meta.last_active = 99;

    const path = try sessionPath(std.testing.allocator, base, "large-metadata");
    defer std.testing.allocator.free(path);
    const padding = try std.testing.allocator.alloc(u8, metadata_max_bytes + 16);
    defer std.testing.allocator.free(padding);
    @memset(padding, ' ');
    try appendFile(path, padding);
    try appendFile(path, "\n");
    try store.save(meta, .{ .turn_start = .{} });

    var list = try store.list();
    defer {
        for (list.items) |*item| item.deinit(std.testing.allocator);
        list.deinit(std.testing.allocator);
    }
    try std.testing.expectEqual(@as(usize, 1), list.items.len);
    try std.testing.expectEqualStrings("tail-model", list.items[0].model);
    try std.testing.expectEqual(@as(i64, 99), list.items[0].last_active);
}

fn contentJson(text: []const u8) !OwnedSlice(u8) {
    return owned(std.testing.allocator, text);
}

fn testMeta(session_id: []const u8) !SessionMetadata {
    var meta = try defaultMetadata(std.testing.allocator, session_id);
    try replaceString(std.testing.allocator, &meta.model, "model-a");
    try replaceString(std.testing.allocator, &meta.provider, "provider-a");
    return meta;
}

const user_parts_json = "[{\"type\":\"text\",\"text\":\"see this\"},{\"type\":\"image\",\"data\":\"base64data\",\"mime_type\":\"image/png\"}]";
const assistant_mixed_json = "[{\"type\":\"text\",\"text\":\"I will call tools\"},{\"type\":\"tool_call\",\"id\":\"call-1\",\"name\":\"demo\",\"arguments_json\":\"{\\\"x\\\":1}\"}]";
const assistant_image_json = "[{\"type\":\"image\",\"data\":\"assistant-image\",\"mime_type\":\"image/png\"}]";

test "message_end replay preserves user image parts" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("user-parts");
    defer meta.deinit(std.testing.allocator);

    var event = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "see this"), .content_json = try contentJson(user_parts_json) } };
    defer event.deinit(std.testing.allocator);
    try store.save(meta, event);

    var loaded = try store.load("user-parts");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    const user = loaded.messages.items[0].user;
    try std.testing.expect(user.content == .parts);
    try std.testing.expectEqual(@as(usize, 2), user.content.parts.len);
    try std.testing.expect(user.content.parts[1] == .image);
    try std.testing.expectEqualStrings("base64data", user.content.parts[1].image.data);
}

test "message_end replay preserves mixed assistant text and tool calls" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("assistant-mixed");
    defer meta.deinit(std.testing.allocator);

    var event = session_events.SessionEvent{ .message_end = .{ .role = .assistant, .text = try owned(std.testing.allocator, "I will call tools"), .content_json = try contentJson(assistant_mixed_json), .stop_reason = .length } };
    defer event.deinit(std.testing.allocator);
    try store.save(meta, event);

    var loaded = try store.load("assistant-mixed");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    const assistant = loaded.messages.items[0].assistant;
    try std.testing.expectEqual(@as(usize, 2), assistant.content.len);
    try std.testing.expect(assistant.content[0] == .text);
    try std.testing.expect(assistant.content[1] == .tool_call);
    try std.testing.expectEqualStrings("call-1", assistant.content[1].tool_call.id);
    try std.testing.expectEqual(ai_types.StopReason.length, assistant.stop_reason);
}

test "message_end replay preserves assistant image content" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("assistant-image");
    defer meta.deinit(std.testing.allocator);

    var event = session_events.SessionEvent{ .message_end = .{ .role = .assistant, .content_json = try contentJson(assistant_image_json) } };
    defer event.deinit(std.testing.allocator);
    try store.save(meta, event);

    var loaded = try store.load("assistant-image");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    const assistant = loaded.messages.items[0].assistant;
    try std.testing.expectEqual(@as(usize, 1), assistant.content.len);
    try std.testing.expect(assistant.content[0] == .image);
    try std.testing.expectEqualStrings("assistant-image", assistant.content[0].image.data);
}

test "load skips malformed json but propagates replay errors" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("bad-replay");
    defer meta.deinit(std.testing.allocator);
    try store.save(meta, .{ .turn_start = .{} });
    const path = try sessionPath(std.testing.allocator, base, "bad-replay");
    defer std.testing.allocator.free(path);
    try appendFile(path, "{bad json}\n");
    try appendFile(path, "{\"event\":{\"type\":\"message_end\",\"role\":\"assistant\",\"content_json\":\"{}\"}}\n");
    try std.testing.expectError(error.InvalidMessage, store.load("bad-replay"));
}

test "load counts raw JSONL line bytes against caps" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    const path = try sessionPath(std.testing.allocator, base, "raw-cap");
    defer std.testing.allocator.free(path);
    const padding = try std.testing.allocator.alloc(u8, max_jsonl_line_bytes + 1);
    defer std.testing.allocator.free(padding);
    @memset(padding, ' ');
    try appendFile(path, padding);
    try appendFile(path, "\n");
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    try std.testing.expectError(error.StreamTooLong, store.load("raw-cap"));
}

test "message_end and tool_execution_end do not duplicate tool result" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("tool-dedupe");
    defer meta.deinit(std.testing.allocator);

    var message_end = session_events.SessionEvent{ .message_end = .{
        .role = .tool_result,
        .tool_call_id = try owned(std.testing.allocator, "call-1"),
        .tool_name = try owned(std.testing.allocator, "demo"),
        .content_json = try contentJson("[{\"type\":\"text\",\"text\":\"result\"}]"),
    } };
    defer message_end.deinit(std.testing.allocator);
    try store.save(meta, message_end);

    var tool_end = session_events.SessionEvent{ .tool_execution_end = .{
        .tool_call_id = try owned(std.testing.allocator, "call-1"),
        .tool_name = try owned(std.testing.allocator, "demo"),
        .result_json = try owned(std.testing.allocator, "result"),
        .is_error = false,
    } };
    defer tool_end.deinit(std.testing.allocator);
    try store.save(meta, tool_end);

    var loaded = try store.load("tool-dedupe");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    try std.testing.expect(loaded.messages.items[0] == .tool_result);
    try std.testing.expectEqualStrings("call-1", loaded.messages.items[0].tool_result.tool_call_id);
}

test "tool_execution_end before message_end keeps one rich tool result" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("tool-dedupe-normal-order");
    defer meta.deinit(std.testing.allocator);

    var tool_end = session_events.SessionEvent{ .tool_execution_end = .{
        .tool_call_id = try owned(std.testing.allocator, "call-1"),
        .tool_name = try owned(std.testing.allocator, "demo"),
        .result_json = try owned(std.testing.allocator, "fallback"),
        .is_error = false,
    } };
    defer tool_end.deinit(std.testing.allocator);
    try store.save(meta, tool_end);

    var message_end = session_events.SessionEvent{ .message_end = .{
        .role = .tool_result,
        .tool_call_id = try owned(std.testing.allocator, "call-1"),
        .tool_name = try owned(std.testing.allocator, "demo"),
        .content_json = try contentJson("[{\"type\":\"text\",\"text\":\"rich\"}]"),
        .details_json = try owned(std.testing.allocator, "{\"rich\":true}"),
    } };
    defer message_end.deinit(std.testing.allocator);
    try store.save(meta, message_end);

    var loaded = try store.load("tool-dedupe-normal-order");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    try std.testing.expect(loaded.messages.items[0] == .tool_result);
    try std.testing.expectEqualStrings("call-1", loaded.messages.items[0].tool_result.tool_call_id);
    try std.testing.expectEqualStrings("rich", loaded.messages.items[0].tool_result.content[0].text.text);
    try std.testing.expectEqualStrings("{\"rich\":true}", loaded.messages.items[0].tool_result.details_json.slice());
}

test "assistant text fallback preserves stop reason" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("assistant-stop-reason");
    defer meta.deinit(std.testing.allocator);

    var event = session_events.SessionEvent{ .message_end = .{ .role = .assistant, .text = try owned(std.testing.allocator, "partial"), .stop_reason = .aborted } };
    defer event.deinit(std.testing.allocator);
    try store.save(meta, event);

    var loaded = try store.load("assistant-stop-reason");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    try std.testing.expect(loaded.messages.items[0] == .assistant);
    try std.testing.expectEqual(ai_types.StopReason.aborted, loaded.messages.items[0].assistant.stop_reason);
}

test "load skips the chunk records replay never reads" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("skips");
    defer meta.deinit(std.testing.allocator);

    var text = session_events.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try owned(std.testing.allocator, "hi") } };
    defer text.deinit(std.testing.allocator);
    var provider = session_events.SessionEvent{ .provider_event = .{ .event_json = try owned(std.testing.allocator, "{\"type\":\"done\"}") } };
    defer provider.deinit(std.testing.allocator);
    var call = session_events.SessionEvent{ .tool_call_delta = .{ .content_index = 0, .delta = try owned(std.testing.allocator, "{\"x\":") } };
    defer call.deinit(std.testing.allocator);
    var update = session_events.SessionEvent{ .tool_execution_update = .{
        .tool_call_id = try owned(std.testing.allocator, "call-1"),
        .tool_name = try owned(std.testing.allocator, "shell_execute"),
        .args_json = try owned(std.testing.allocator, "{}"),
        .partial_result_json = try owned(std.testing.allocator, "{\"stdout\":\"part\"}"),
    } };
    defer update.deinit(std.testing.allocator);
    var done = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "done") } };
    defer done.deinit(std.testing.allocator);
    for ([_]session_events.SessionEvent{ text, provider, call, update, done }) |event| try store.save(meta, event);

    var loaded = try store.load("skips");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 2), loaded.events.items.len);
    try std.testing.expect(loaded.events.items[0] == .text_delta);
    try std.testing.expect(loaded.events.items[1] == .message_end);
}

test "load starts at the indexed compaction and keeps a display tail before it" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("indexed");
    defer meta.deinit(std.testing.allocator);

    var first = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "first question") } };
    defer first.deinit(std.testing.allocator);
    try store.save(meta, first);
    const filler_text = try std.testing.allocator.alloc(u8, 16 * 1024);
    defer std.testing.allocator.free(filler_text);
    @memset(filler_text, 'x');
    const filler = session_events.SessionEvent{ .system_warning = .{ .message = OwnedSlice(u8).initBorrowed(filler_text) } };
    for (0..20) |_| try store.saveEvent("indexed", filler);

    const offset = try store.conversationBytes("indexed");
    var compacted = session_events.SessionEvent{ .compaction_end = .{
        .outcome = .completed,
        .text = try owned(std.testing.allocator, agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept state\n</summary>"),
        .messages_before = 21,
    } };
    defer compacted.deinit(std.testing.allocator);
    try store.save(meta, compacted);
    var after = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "after compaction") } };
    defer after.deinit(std.testing.allocator);
    try store.saveEvent("indexed", after);
    meta.compaction_offset = offset;
    try store.saveIndex(meta);

    var loaded = try store.load("indexed");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(offset, loaded.metadata.compaction_offset);
    try std.testing.expectEqual(@as(usize, 3), loaded.messages.items.len);
    try std.testing.expectEqualStrings("kept state", agent.compaction.summaryOf(loaded.messages.items[0].user.content.text));
    try std.testing.expectEqualStrings("after compaction", loaded.messages.items[2].user.content.text);

    const events = loaded.events.items;
    try std.testing.expect(events[0] == .system_warning);
    var warnings: usize = 0;
    for (events) |event| {
        if (event == .system_warning) warnings += 1;
    }
    try std.testing.expect(warnings > 0 and warnings < 20);
    try std.testing.expect(events[events.len - 2] == .compaction_end);
    try std.testing.expect(events[events.len - 1] == .message_end);
}

test "load starts at an indexed compaction made inside a run and keeps it marked as one" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("in-run");
    defer meta.deinit(std.testing.allocator);

    try saveText(store, meta, .user, "old question");
    try store.save(meta, .{ .compaction_start = .{ .in_run = true } });
    const offset = try store.conversationBytes("in-run");
    var compacted = session_events.SessionEvent{ .compaction_end = .{
        .in_run = true,
        .outcome = .completed,
        .text = try owned(std.testing.allocator, agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept state\n</summary>"),
        .messages_before = 1,
    } };
    defer compacted.deinit(std.testing.allocator);
    try store.save(meta, compacted);
    try saveText(store, meta, .user, "carry on");
    meta.compaction_offset = offset;
    try store.saveIndex(meta);

    var loaded = try store.load("in-run");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 3), loaded.messages.items.len);
    try std.testing.expectEqualStrings("kept state", agent.compaction.summaryOf(loaded.messages.items[0].user.content.text));
    try std.testing.expectEqualStrings("carry on", loaded.messages.items[2].user.content.text);
    const events = loaded.events.items;
    try std.testing.expect(events[events.len - 2] == .compaction_end);
    try std.testing.expect(events[events.len - 2].compaction_end.in_run);
}

test "load reads the whole file when the indexed offset does not start a compaction" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("stale-index");
    defer meta.deinit(std.testing.allocator);

    var one = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "one") } };
    defer one.deinit(std.testing.allocator);
    try store.save(meta, one);
    var two = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "two") } };
    defer two.deinit(std.testing.allocator);
    try store.saveEvent("stale-index", two);
    meta.compaction_offset = 5;
    try store.saveIndex(meta);

    var loaded = try store.load("stale-index");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(usize, 2), loaded.events.items.len);
    try std.testing.expectEqual(@as(usize, 2), loaded.messages.items.len);
    try std.testing.expectEqual(@as(u64, 0), loaded.metadata.compaction_offset);
}

test "load reads the whole file when the indexed compaction record is torn" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("torn");
    defer meta.deinit(std.testing.allocator);

    var first = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "first question") } };
    defer first.deinit(std.testing.allocator);
    try store.save(meta, first);
    const filler_text = try std.testing.allocator.alloc(u8, 16 * 1024);
    defer std.testing.allocator.free(filler_text);
    @memset(filler_text, 'x');
    const filler = session_events.SessionEvent{ .system_warning = .{ .message = OwnedSlice(u8).initBorrowed(filler_text) } };
    for (0..20) |_| try store.saveEvent("torn", filler);

    const offset = try store.conversationBytes("torn");
    var compacted = session_events.SessionEvent{ .compaction_end = .{
        .outcome = .completed,
        .text = try owned(std.testing.allocator, agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept state\n</summary>"),
        .messages_before = 21,
    } };
    defer compacted.deinit(std.testing.allocator);
    const record = try serializeEventRecord(std.testing.allocator, meta, compacted);
    defer std.testing.allocator.free(record);
    const path = try sessionPath(std.testing.allocator, base, "torn");
    defer std.testing.allocator.free(path);
    try appendFile(path, record[0 .. record.len - 8]);
    try std.testing.expect(startsCompaction(path, offset));
    var after = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "after compaction") } };
    defer after.deinit(std.testing.allocator);
    try store.saveEvent("torn", after);
    meta.compaction_offset = offset;
    try store.saveIndex(meta);

    var loaded = try store.load("torn");
    defer loaded.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(u64, 0), loaded.metadata.compaction_offset);
    try std.testing.expectEqual(@as(usize, 21), loaded.events.items.len);
    try std.testing.expectEqual(@as(usize, 1), loaded.messages.items.len);
    try std.testing.expectEqualStrings("first question", loaded.messages.items[0].user.content.text);
}

test "list reads a session's details from its index and skips its stream file" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("indexed-list");
    defer meta.deinit(std.testing.allocator);
    meta.created_at = 11;
    meta.last_active = 22;

    try store.saveEvent("indexed-list", .{ .turn_start = .{} });
    var chunk = session_events.SessionEvent{ .text_delta = .{ .content_index = 0, .delta = try owned(std.testing.allocator, "streamed") } };
    defer chunk.deinit(std.testing.allocator);
    try store.saveChunk("indexed-list", chunk);
    try store.saveIndex(meta);

    var list = try store.list();
    defer {
        for (list.items) |*item| item.deinit(std.testing.allocator);
        list.deinit(std.testing.allocator);
    }
    try std.testing.expectEqual(@as(usize, 1), list.items.len);
    try std.testing.expectEqualStrings("indexed-list", list.items[0].session_id);
    try std.testing.expectEqualStrings("model-a", list.items[0].model);
    try std.testing.expectEqual(@as(i64, 11), list.items[0].created_at);
    try std.testing.expectEqual(@as(i64, 22), list.items[0].last_active);
}

test "list falls back to the first record's metadata when the tail has none" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("head-metadata");
    defer meta.deinit(std.testing.allocator);

    try store.save(meta, .{ .turn_start = .{} });
    const path = try sessionPath(std.testing.allocator, base, "head-metadata");
    defer std.testing.allocator.free(path);
    const padding = try std.testing.allocator.alloc(u8, metadata_max_bytes + 16);
    defer std.testing.allocator.free(padding);
    @memset(padding, ' ');
    try appendFile(path, padding);
    try appendFile(path, "\n");
    try store.saveEvent("head-metadata", .{ .turn_start = .{} });

    var list = try store.list();
    defer {
        for (list.items) |*item| item.deinit(std.testing.allocator);
        list.deinit(std.testing.allocator);
    }
    try std.testing.expectEqual(@as(usize, 1), list.items.len);
    try std.testing.expectEqualStrings("model-a", list.items[0].model);
}

fn parseIndexProbe(allocator: std.mem.Allocator) !void {
    var meta = try parseIndex(allocator, "s1", "{\"session_id\":\"s1\",\"model\":\"m\",\"provider\":\"p\",\"created_at\":1,\"last_active\":2,\"compaction_offset\":3,\"compactions\":2,\"title\":\"t\",\"title_generated\":true}");
    meta.deinit(allocator);
}

test "a generated title does not replace a session's chosen name" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    var meta = try defaultMetadata(std.testing.allocator, "renamed");
    defer meta.deinit(std.testing.allocator);
    try replaceString(std.testing.allocator, &meta.title, "My name");
    meta.title_renamed = true;
    try store.saveIndex(meta);
    try store.saveGeneratedTitle("renamed", "Generated name");

    var index = try store.loadIndex("renamed");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqualStrings("My name", index.title);
    try std.testing.expect(index.title_renamed);
    try std.testing.expect(!index.title_generated);
}

test "a session's index keeps its thinking level, and an index without one reads as unset" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();

    var meta = try defaultMetadata(std.testing.allocator, "thinks");
    defer meta.deinit(std.testing.allocator);
    meta.thinking_level = .xhigh;
    try store.saveIndex(meta);
    try store.saveGeneratedTitle("thinks", "Generated name");

    var index = try store.loadIndex("thinks");
    defer index.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(?ai_types.ThinkingLevel, .xhigh), index.thinking_level);

    var bare = try parseIndex(std.testing.allocator, "bare", "{\"model\":\"m\",\"thinking_level\":\"loud\"}");
    defer bare.deinit(std.testing.allocator);
    try std.testing.expectEqual(@as(?ai_types.ThinkingLevel, null), bare.thinking_level);
}

test "parseIndex survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseIndexProbe, .{});
}

fn parseArtifactsProbe(allocator: std.mem.Allocator) !void {
    const json =
        \\[{"artifact_id":"art-one","uri":"file:///tmp/one.txt","mime_type":"text/plain","byte_size":11,"sha256":"1111111111111111","description":"first artifact"},
        \\ {"artifact_id":"art-two","uri":"file:///tmp/two.json","mime_type":"application/json","byte_size":22,"sha256":"2222222222222222","description":"second artifact"}]
    ;

    const artifacts = try parseArtifacts(allocator, json);
    for (artifacts) |*artifact| artifact.deinit(allocator);
    allocator.free(artifacts);
}

test "parseArtifacts survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseArtifactsProbe, .{});
}

fn parseToolResultFromPayloadProbe(allocator: std.mem.Allocator) !void {
    const payload = .{
        .tool_call_id = OwnedSlice(u8).initBorrowed("call-0123456789"),
        .tool_name = OwnedSlice(u8).initBorrowed("shell_execute"),
        .content_json = OwnedSlice(u8).initBorrowed(
            \\[{"type":"text","text":"stdout from the tool"}]
        ),
        .details_json = OwnedSlice(u8).initBorrowed(
            \\{"ok":true,"exit_code":0}
        ),
        .artifacts_json = OwnedSlice(u8).initBorrowed(
            \\[{"artifact_id":"art-one","uri":"file:///tmp/one.txt","mime_type":"text/plain","byte_size":11,"sha256":"1111111111111111","description":"first artifact"}]
        ),
        .is_error = false,
    };

    var message = try parseToolResultFromPayload(allocator, payload);
    message.deinit(allocator);
}

test "parseToolResultFromPayload survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseToolResultFromPayloadProbe, .{});
}

fn parseEventProbe(allocator: std.mem.Allocator) !void {
    const payloads = [_][]const u8{
        \\{"type":"message_end","role":"assistant","text":"final answer text","content_json":"[{\"type\":\"text\"}]","tool_call_id":"call-0123456789","tool_name":"shell_execute","args_json":"{\"command\":\"ls\"}","tool_calls_json":"[]","details_json":"{\"ok\":true}","artifacts_json":"[]","stop_reason":"stop","is_error":false}
        ,
        \\{"type":"tool_approval_requested","tool_call_id":"call-0123456789","tool_name":"shell_execute","args_json":"{\"command\":\"ls\"}"}
        ,
        \\{"type":"tool_execution_start","tool_call_id":"call-0123456789","tool_name":"shell_execute","args_json":"{\"command\":\"ls\"}"}
        ,
        \\{"type":"tool_execution_update","tool_call_id":"call-0123456789","tool_name":"shell_execute","args_json":"{\"command\":\"ls\"}","partial_result_json":"{\"stdout\":\"part\"}"}
        ,
        \\{"type":"tool_execution_end","tool_call_id":"call-0123456789","tool_name":"shell_execute","result_json":"{\"stdout\":\"done\"}","is_error":false,"artifact_count":1,"artifact_refs":"art-one,art-two"}
        ,
        \\{"type":"compaction_end","outcome":"completed","text":"This conversation was compacted.","transcript":"/s/s1/compaction-1.jsonl","message":"","messages_before":12,"tokens_before":900,"tokens_after":40}
        ,
    };

    for (payloads) |payload| {
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, payload, .{});
        defer parsed.deinit();

        var event = try parseEvent(allocator, parsed.value);
        event.deinit(allocator);
    }
}

test "parseEvent survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseEventProbe, .{});
}

fn toolResultFromFieldsProbe(allocator: std.mem.Allocator) !void {
    var message = try toolResultFromFields(
        allocator,
        "call-0123456789",
        "shell_execute",
        \\{"stdout":"output from the tool","exit_code":0}
    ,
        false,
    );
    message.deinit(allocator);
}

test "toolResultFromFields survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, toolResultFromFieldsProbe, .{});
}

fn parseMessageToolResultProbe(allocator: std.mem.Allocator) !void {
    const payload =
        \\{"role":"tool_result","text":"stdout from the tool","tool_call_id":"call-0123456789","tool_name":"shell_execute","details_json":"{\"ok\":true}","is_error":false,"timestamp":1700000000000}
    ;

    var parsed = try std.json.parseFromSlice(std.json.Value, allocator, payload, .{});
    defer parsed.deinit();

    var message = try parseMessage(allocator, parsed.value);
    message.deinit(allocator);
}

test "parseMessage tool_result survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseMessageToolResultProbe, .{});
}

fn parseAssistantContentProbe(allocator: std.mem.Allocator) !void {
    const payloads = [_][]const u8{
        \\{"type":"text","text":"assistant answer text","text_signature":"sig-abcdefgh"}
        ,
        \\{"type":"thinking","thinking":"reasoning trace here","thinking_signature":"tsig-abcdefgh"}
        ,
        \\{"type":"tool_call","id":"call-0123456789","name":"shell_execute","arguments_json":"{\"command\":\"ls\"}","thought_signature":"thought-abcdefgh"}
        ,
        \\{"type":"image","data":"aW1hZ2UtYnl0ZXM=","mime_type":"image/png"}
        ,
    };

    for (payloads) |payload| {
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, payload, .{});
        defer parsed.deinit();

        var block = [_]ai_types.AssistantContent{try parseAssistantContent(allocator, parsed.value)};
        deinitAssistantContentPrefix(allocator, &block);
    }
}

test "parseAssistantContent survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseAssistantContentProbe, .{});
}

fn parseUserContentPartProbe(allocator: std.mem.Allocator) !void {
    const payloads = [_][]const u8{
        \\{"type":"text","text":"user message text","text_signature":"sig-abcdefgh"}
        ,
        \\{"type":"image","data":"aW1hZ2UtYnl0ZXM=","mime_type":"image/png"}
        ,
    };

    for (payloads) |payload| {
        var parsed = try std.json.parseFromSlice(std.json.Value, allocator, payload, .{});
        defer parsed.deinit();

        var part = try parseUserContentPart(allocator, parsed.value);
        part.deinit(allocator);
    }
}

test "parseUserContentPart survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, parseUserContentPartProbe, .{});
}

fn assistantMessageBuildersProbe(allocator: std.mem.Allocator) !void {
    const meta = SessionMetadata{
        .session_id = @constCast("session-0123456789012"),
        .model = @constCast("claude-sonnet-4-5"),
        .provider = @constCast("anthropic"),
        .last_active = 1_700_000_000_000,
    };

    var text_message = try assistantTextMessageWithMeta(allocator, meta, "assistant answer text", .stop);
    ai_types.deinitAssistantMessageOwned(allocator, &text_message);

    var tool_message = try assistantToolCallMessage(
        allocator,
        meta,
        "call-0123456789",
        "shell_execute",
        \\{"command":"ls"}
        ,
    );
    ai_types.deinitAssistantMessageOwned(allocator, &tool_message);
}

test "assistant message builders survive an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, assistantMessageBuildersProbe, .{});
}

fn saveText(store: Store, meta: SessionMetadata, role: session_events.SessionEvent.MessageRole, text: []const u8) !void {
    var event = session_events.SessionEvent{ .message_end = .{ .role = role, .text = try owned(std.testing.allocator, text) } };
    defer event.deinit(std.testing.allocator);
    try store.save(meta, event);
}

test "load replays a completed compaction as its summary turn and acknowledgement" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("compacted");
    defer meta.deinit(std.testing.allocator);

    try saveText(store, meta, .user, "old question");
    try saveText(store, meta, .assistant, "old answer");
    var completed = session_events.SessionEvent{ .compaction_end = .{
        .outcome = .completed,
        .text = try owned(std.testing.allocator, agent.compaction.header ++ " Summary follows.\n\n<summary>\nkept state\n</summary>"),
        .transcript = try owned(std.testing.allocator, "/s/compacted/compaction-1.jsonl"),
        .messages_before = 2,
    } };
    defer completed.deinit(std.testing.allocator);
    try store.save(meta, completed);
    try saveText(store, meta, .user, "new question");
    var failed = session_events.SessionEvent{ .compaction_end = .{ .outcome = .failed, .message = try owned(std.testing.allocator, "overloaded") } };
    defer failed.deinit(std.testing.allocator);
    try store.save(meta, failed);

    var loaded = try store.load("compacted");
    defer loaded.deinit(std.testing.allocator);
    const messages = loaded.messages.items;
    try std.testing.expectEqual(@as(usize, 3), messages.len);
    try std.testing.expectEqualStrings("kept state", agent.compaction.summaryOf(messages[0].user.content.text));
    try std.testing.expectEqualStrings(agent.compaction.acknowledgement, messages[1].assistant.content[0].text.text);
    try std.testing.expectEqualStrings("model-a", messages[1].assistant.model);
    try std.testing.expectEqualStrings("new question", messages[2].user.content.text);

    try std.testing.expectEqual(@as(usize, 5), loaded.events.items.len);
    const replayed = loaded.events.items[2].compaction_end;
    try std.testing.expectEqual(session_events.SessionEvent.CompactionOutcome.completed, replayed.outcome);
    try std.testing.expectEqualStrings("/s/compacted/compaction-1.jsonl", replayed.transcript.slice());
    try std.testing.expectEqual(@as(u64, 2), replayed.messages_before);
}

test "deleteSession removes the JSONL and sidecar directory" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("delete-me");
    defer meta.deinit(std.testing.allocator);
    try saveText(store, meta, .user, "question");
    var chunk = session_events.SessionEvent{ .message_end = .{ .role = .user, .text = try owned(std.testing.allocator, "stream") } };
    defer chunk.deinit(std.testing.allocator);
    try store.saveChunk("delete-me", chunk);
    try store.saveIndex(meta);
    const stream_path = try sessionFilePath(std.testing.allocator, base, "delete-me", stream_suffix);
    defer std.testing.allocator.free(stream_path);
    const index_path = try sessionFilePath(std.testing.allocator, base, "delete-me", index_suffix);
    defer std.testing.allocator.free(index_path);
    const sidecar_dir = try std.fs.path.join(std.testing.allocator, &.{ base, "delete-me" });
    defer std.testing.allocator.free(sidecar_dir);
    try compat.fs.createDir(compat.fs.getCwd(), sidecar_dir);
    const marker = try std.fs.path.join(std.testing.allocator, &.{ sidecar_dir, "worktree.json" });
    defer std.testing.allocator.free(marker);
    try compat.fs.writeFile(compat.fs.getCwd(), marker, "{}");

    try store.deleteSession("delete-me");
    const listed = try store.list();
    defer {
        for (listed.items) |*entry| entry.deinit(std.testing.allocator);
        var mutable = listed;
        mutable.deinit(std.testing.allocator);
    }
    try std.testing.expectEqual(@as(usize, 0), listed.items.len);
    try std.testing.expectError(error.FileNotFound, compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), marker, 1024));
    try std.testing.expectError(error.FileNotFound, compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), stream_path, 1024));
    try std.testing.expectError(error.FileNotFound, compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), index_path, 1024));
}

test "saveTranscript writes one message per line beside the session files without adding a session" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    var store = try Store.init(std.testing.allocator, base);
    defer store.deinit();
    var meta = try testMeta("archived");
    defer meta.deinit(std.testing.allocator);
    try saveText(store, meta, .user, "question");

    const call = [_]ai_types.AssistantContent{
        .{ .thinking = .{ .thinking = "consider ls" } },
        .{ .text = .{ .text = "listing" } },
        .{ .tool_call = .{ .id = "c1", .name = "shell_execute", .arguments_json = "{\"command\":\"ls\"}" } },
    };
    const output = [_]ai_types.UserContentPart{.{ .text = .{ .text = "a\nb" } }};
    const messages = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "question" }, .timestamp = 0 } },
        .{ .assistant = .{ .content = &call, .api = "", .provider = "", .model = "", .usage = .{}, .stop_reason = .tool_use, .timestamp = 0 } },
        .{ .tool_result = .{ .tool_call_id = "c1", .tool_name = "shell_execute", .content = &output, .is_error = true, .timestamp = 0 } },
    };
    const path = try store.saveTranscript("archived", 1, &messages);
    defer std.testing.allocator.free(path);
    try std.testing.expect(std.mem.endsWith(u8, path, "archived" ++ std.fs.path.sep_str ++ "compaction-1.jsonl"));

    const data = try compat.fs.readFileAlloc(std.testing.allocator, compat.fs.getCwd(), path, 64 * 1024);
    defer std.testing.allocator.free(data);
    var lines = std.mem.splitScalar(u8, std.mem.trimEnd(u8, data, "\n"), '\n');
    var parsed_lines: [3]std.json.Parsed(std.json.Value) = undefined;
    var count: usize = 0;
    defer for (parsed_lines[0..count]) |*parsed| parsed.deinit();
    while (lines.next()) |line| {
        try std.testing.expect(count < parsed_lines.len);
        parsed_lines[count] = try std.json.parseFromSlice(std.json.Value, std.testing.allocator, line, .{});
        count += 1;
    }
    try std.testing.expectEqual(@as(usize, 3), count);
    const user = parsed_lines[0].value.object;
    try std.testing.expectEqualStrings("question", user.get("text").?.string);
    const assistant = parsed_lines[1].value.object;
    try std.testing.expectEqualStrings("assistant", assistant.get("role").?.string);
    try std.testing.expectEqualStrings("listing", assistant.get("text").?.string);
    try std.testing.expectEqualStrings("consider ls", assistant.get("thinking").?.string);
    const tool_call = assistant.get("tool_calls").?.array.items[0].object;
    try std.testing.expectEqualStrings("shell_execute", tool_call.get("name").?.string);
    try std.testing.expectEqualStrings("{\"command\":\"ls\"}", tool_call.get("arguments").?.string);
    const result = parsed_lines[2].value.object;
    try std.testing.expectEqualStrings("c1", result.get("tool_call_id").?.string);
    try std.testing.expectEqualStrings("a\nb", result.get("text").?.string);
    try std.testing.expect(result.get("is_error").?.bool);

    var sessions = try store.list();
    defer {
        for (sessions.items) |*session| session.deinit(std.testing.allocator);
        sessions.deinit(std.testing.allocator);
    }
    try std.testing.expectEqual(@as(usize, 1), sessions.items.len);
    try std.testing.expectEqualStrings("archived", sessions.items[0].session_id);
}

fn serializeTranscriptProbe(allocator: std.mem.Allocator) !void {
    const call = [_]ai_types.AssistantContent{
        .{ .text = .{ .text = "listing" } },
        .{ .tool_call = .{ .id = "c1", .name = "shell_execute", .arguments_json = "{}" } },
    };
    const output = [_]ai_types.UserContentPart{.{ .text = .{ .text = "out" } }};
    const messages = [_]ai_types.Message{
        .{ .user = .{ .content = .{ .text = "question" }, .timestamp = 0 } },
        .{ .assistant = .{ .content = &call, .api = "", .provider = "", .model = "", .usage = .{}, .stop_reason = .tool_use, .timestamp = 0 } },
        .{ .tool_result = .{ .tool_call_id = "c1", .tool_name = "shell_execute", .content = &output, .is_error = false, .timestamp = 0 } },
    };
    const data = try serializeTranscript(allocator, &messages);
    allocator.free(data);
}

fn saveTranscriptProbe(allocator: std.mem.Allocator, base: []const u8) !void {
    var store = try Store.init(allocator, base);
    defer store.deinit();
    const messages = [_]ai_types.Message{.{ .user = .{ .content = .{ .text = "question" }, .timestamp = 0 } }};
    const path = try store.saveTranscript("probe", 1, &messages);
    allocator.free(path);
}

test "saveTranscript survives an allocation failure at every step" {
    var tmp = std.testing.tmpDir(.{});
    defer tmp.cleanup();
    const base = try tmpBase(std.testing.allocator, &tmp);
    defer std.testing.allocator.free(base);
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, saveTranscriptProbe, .{base});
}

test "serializeTranscript survives an allocation failure at every step" {
    try std.testing.checkAllAllocationFailures(std.heap.smp_allocator, serializeTranscriptProbe, .{});
}
