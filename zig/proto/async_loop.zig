const std = @import("std");
const c = std.c;

const gpa = std.heap.smp_allocator;
const default_park = false;

const Role = enum { user, assistant, tool };

const Msg = struct {
    role: Role,
    content: []const u8 = "",
    tool_call_id: []const u8 = "",
    tool_name: []const u8 = "",
    tool_args: []const u8 = "",
};

const Phase = enum { idle, model_queued, model, tool_queued, tool, gate, backoff };

const Snapshot = struct {
    session_id: []const u8,
    workspace: []const u8,
    ask: bool,
    seq: u32,
    gate_seq: u32,
    phase: Phase,
    attempts: u8,
    shown: bool,
    text: []const u8,
    call_id: []const u8,
    call_name: []const u8,
    call_args: []const u8,
    messages: []const Msg,
};

const Event = union(enum) {
    submit: []const u8,
    model_admitted,
    delta: []const u8,
    tool_delta: struct { id: []const u8, name: []const u8, args: []const u8 },
    finished,
    failed: struct { transient: bool, text: []const u8 },
    timer,
    tool_admitted,
    tool_done: []const u8,
    gate: bool,
    cancel,
    resumed,
};

const Action = union(enum) {
    queue_model,
    send_model,
    queue_tool,
    run_tool,
    open_gate,
    abort_io,
    arm_timer: u32,
    settled,
    release_model,
    release_tool,
};

const Actions = struct {
    items: [6]Action = undefined,
    len: usize = 0,

    fn add(self: *Actions, action: Action) void {
        self.items[self.len] = action;
        self.len += 1;
    }

    fn slice(self: *const Actions) []const Action {
        return self.items[0..self.len];
    }
};

const Run = struct {
    arena: std.heap.ArenaAllocator,
    session_id: []const u8,
    workspace: []const u8,
    ask: bool,
    seq: u32 = 0,
    gate_seq: u32 = 0,
    phase: Phase = .idle,
    attempts: u8 = 0,
    shown: bool = false,
    text: std.ArrayList(u8) = .empty,
    call_id: std.ArrayList(u8) = .empty,
    call_name: std.ArrayList(u8) = .empty,
    call_args: std.ArrayList(u8) = .empty,
    messages: std.ArrayList(Msg) = .empty,

    fn create(session_id: []const u8, workspace: []const u8, ask: bool) !*Run {
        const run = try gpa.create(Run);
        run.* = .{ .arena = std.heap.ArenaAllocator.init(gpa), .session_id = "", .workspace = "", .ask = ask };
        const a = run.arena.allocator();
        run.session_id = try a.dupe(u8, session_id);
        run.workspace = try a.dupe(u8, workspace);
        return run;
    }

    fn destroy(self: *Run) void {
        self.arena.deinit();
        gpa.destroy(self);
    }

    fn snapshot(self: *const Run) Snapshot {
        return .{
            .session_id = self.session_id,
            .workspace = self.workspace,
            .ask = self.ask,
            .seq = self.seq,
            .gate_seq = self.gate_seq,
            .phase = self.phase,
            .attempts = self.attempts,
            .shown = self.shown,
            .text = self.text.items,
            .call_id = self.call_id.items,
            .call_name = self.call_name.items,
            .call_args = self.call_args.items,
            .messages = self.messages.items,
        };
    }

    fn encode(self: *const Run, a: std.mem.Allocator) ![]u8 {
        return std.json.Stringify.valueAlloc(a, self.snapshot(), .{});
    }

    fn decode(bytes: []const u8) !*Run {
        var parsed = try std.json.parseFromSlice(Snapshot, gpa, bytes, .{});
        defer parsed.deinit();
        const s = parsed.value;
        const run = try Run.create(s.session_id, s.workspace, s.ask);
        errdefer run.destroy();
        const a = run.arena.allocator();
        run.seq = s.seq;
        run.gate_seq = s.gate_seq;
        run.phase = s.phase;
        run.attempts = s.attempts;
        run.shown = s.shown;
        try run.text.appendSlice(a, s.text);
        try run.call_id.appendSlice(a, s.call_id);
        try run.call_name.appendSlice(a, s.call_name);
        try run.call_args.appendSlice(a, s.call_args);
        for (s.messages) |m| try run.messages.append(a, .{
            .role = m.role,
            .content = try a.dupe(u8, m.content),
            .tool_call_id = try a.dupe(u8, m.tool_call_id),
            .tool_name = try a.dupe(u8, m.tool_name),
            .tool_args = try a.dupe(u8, m.tool_args),
        });
        return run;
    }

    fn clearTurn(self: *Run) void {
        self.text.clearRetainingCapacity();
        self.call_id.clearRetainingCapacity();
        self.call_name.clearRetainingCapacity();
        self.call_args.clearRetainingCapacity();
        self.shown = false;
    }
};

fn needsGate(run: *const Run) bool {
    return run.ask and !std.mem.eql(u8, run.call_name.items, "Read");
}

fn beginTool(run: *Run, out: *Actions) void {
    run.phase = .tool_queued;
    out.add(.queue_tool);
}

fn step(run: *Run, event: Event, out: *Actions) !void {
    const a = run.arena.allocator();
    if (event == .cancel) {
        const held = run.phase;
        if (held == .idle) return;
        out.add(.abort_io);
        if (held == .model) out.add(.release_model);
        if (held == .tool) out.add(.release_tool);
        run.clearTurn();
        run.phase = .idle;
        out.add(.settled);
        return;
    }
    switch (run.phase) {
        .idle => switch (event) {
            .submit => |text| {
                try run.messages.append(a, .{ .role = .user, .content = try a.dupe(u8, text) });
                run.seq += 1;
                run.attempts = 0;
                run.clearTurn();
                run.phase = .model_queued;
                out.add(.queue_model);
            },
            else => {},
        },
        .model_queued => switch (event) {
            .model_admitted => {
                run.phase = .model;
                out.add(.send_model);
            },
            .resumed => out.add(.queue_model),
            else => {},
        },
        .model => switch (event) {
            .delta => |text| {
                try run.text.appendSlice(a, text);
                run.shown = true;
            },
            .tool_delta => |d| {
                try run.call_id.appendSlice(a, d.id);
                try run.call_name.appendSlice(a, d.name);
                try run.call_args.appendSlice(a, d.args);
            },
            .finished => {
                out.add(.release_model);
                const has_call = run.call_name.items.len > 0;
                try run.messages.append(a, .{
                    .role = .assistant,
                    .content = try a.dupe(u8, run.text.items),
                    .tool_call_id = try a.dupe(u8, run.call_id.items),
                    .tool_name = try a.dupe(u8, run.call_name.items),
                    .tool_args = try a.dupe(u8, run.call_args.items),
                });
                run.text.clearRetainingCapacity();
                if (!has_call) {
                    run.phase = .idle;
                    out.add(.settled);
                } else if (needsGate(run)) {
                    run.gate_seq += 1;
                    run.phase = .gate;
                    out.add(.open_gate);
                } else beginTool(run, out);
            },
            .failed => |f| {
                out.add(.release_model);
                if (f.transient and !run.shown and run.attempts < 2) {
                    run.clearTurn();
                    run.attempts += 1;
                    run.phase = .backoff;
                    out.add(.{ .arm_timer = @as(u32, 1000) << @intCast(run.attempts - 1) });
                } else {
                    try run.messages.append(a, .{ .role = .assistant, .content = try a.dupe(u8, f.text) });
                    run.clearTurn();
                    run.phase = .idle;
                    out.add(.settled);
                }
            },
            .resumed => {
                run.clearTurn();
                run.phase = .model_queued;
                out.add(.queue_model);
            },
            else => {},
        },
        .backoff => switch (event) {
            .timer, .resumed => {
                run.phase = .model_queued;
                out.add(.queue_model);
            },
            else => {},
        },
        .gate => switch (event) {
            .gate => |granted| {
                if (granted) beginTool(run, out) else {
                    try finishTool(run, "the user refused this call", out);
                }
            },
            else => {},
        },
        .tool_queued => switch (event) {
            .tool_admitted => {
                run.phase = .tool;
                out.add(.run_tool);
            },
            .resumed => out.add(.queue_tool),
            else => {},
        },
        .tool => switch (event) {
            .tool_done => |text| {
                out.add(.release_tool);
                try finishTool(run, text, out);
            },
            .resumed => try finishTool(run, "the tool was interrupted and its outcome is unknown", out),
            else => {},
        },
    }
}

fn finishTool(run: *Run, text: []const u8, out: *Actions) !void {
    const a = run.arena.allocator();
    try run.messages.append(a, .{
        .role = .tool,
        .content = try a.dupe(u8, text),
        .tool_call_id = try a.dupe(u8, run.call_id.items),
        .tool_name = try a.dupe(u8, run.call_name.items),
    });
    run.clearTurn();
    run.phase = .model_queued;
    out.add(.queue_model);
}

const Lock = struct {
    inner: c.pthread_mutex_t = .{},

    fn acquire(self: *Lock) void {
        _ = c.pthread_mutex_lock(&self.inner);
    }

    fn release(self: *Lock) void {
        _ = c.pthread_mutex_unlock(&self.inner);
    }
};

var stdout_lock: Lock = .{};

fn writeAll(fd: c.fd_t, bytes: []const u8) void {
    var rest = bytes;
    while (rest.len > 0) {
        const n = c.write(fd, rest.ptr, rest.len);
        if (n <= 0) return;
        rest = rest[@intCast(n)..];
    }
}

fn reply(id: i64, result: []const u8) void {
    var head: [64]u8 = undefined;
    const prefix = std.fmt.bufPrint(&head, "{{\"id\":{d},\"ok\":true,\"result\":", .{id}) catch unreachable;
    stdout_lock.acquire();
    defer stdout_lock.release();
    writeAll(1, prefix);
    writeAll(1, result);
    writeAll(1, "}\n");
}

fn refuse(id: i64, text: []const u8) void {
    var line: [256]u8 = undefined;
    const body = std.fmt.bufPrint(&line, "{{\"id\":{d},\"ok\":false,\"error\":\"{s}\"}}\n", .{ id, text }) catch unreachable;
    stdout_lock.acquire();
    defer stdout_lock.release();
    writeAll(1, body);
}

const Conn = struct {
    fd: c.fd_t,
    owner: ?*Session = null,
    send: []u8 = "",
    sent: usize = 0,
    recv: std.ArrayList(u8) = .empty,
    sse: std.ArrayList(u8) = .empty,
    head_done: bool = false,
    chunk_left: usize = 0,
    first_byte: bool = false,
    idle: bool = false,

    fn reset(self: *Conn) void {
        if (self.send.len > 0) gpa.free(self.send);
        self.send = "";
        self.sent = 0;
        self.recv.clearRetainingCapacity();
        self.sse.clearRetainingCapacity();
        self.head_done = false;
        self.chunk_left = 0;
        self.first_byte = false;
    }

    fn destroy(self: *Conn) void {
        self.reset();
        self.recv.deinit(gpa);
        self.sse.deinit(gpa);
        gpa.destroy(self);
    }
};

const Child = struct {
    output: std.ArrayList(u8) = .empty,
    pid: c.pid_t = 0,
    fd: c.fd_t = -1,
    eof: bool = false,
    exited: bool = false,
};

const Pending = union(enum) {
    none,
    gate: struct { id: i64, granted: bool },
    submit: struct { id: i64, text: []u8 },
    cancel: i64,
};

const Session = struct {
    id: []const u8,
    reactor: *Reactor,
    run: ?*Run,
    phase: Phase = .idle,
    seq: u32 = 0,
    gate_seq: u32 = 0,
    saving: bool = false,
    loading: bool = false,
    pending: Pending = .none,
    conn: ?*Conn = null,
    child: ?*Child = null,
    next_wait: ?*Session = null,
};

const Fifo = struct {
    head: ?*Session = null,
    tail: ?*Session = null,
    len: usize = 0,

    fn push(self: *Fifo, s: *Session) void {
        s.next_wait = null;
        if (self.tail) |t| t.next_wait = s else self.head = s;
        self.tail = s;
        self.len += 1;
    }

    fn pop(self: *Fifo) ?*Session {
        const s = self.head orelse return null;
        self.head = s.next_wait;
        if (self.head == null) self.tail = null;
        s.next_wait = null;
        self.len -= 1;
        return s;
    }

    fn remove(self: *Fifo, s: *Session) void {
        var previous: ?*Session = null;
        var at = self.head;
        while (at) |node| : (at = node.next_wait) {
            if (node == s) {
                if (previous) |p| p.next_wait = node.next_wait else self.head = node.next_wait;
                if (self.tail == node) self.tail = previous;
                node.next_wait = null;
                self.len -= 1;
                return;
            }
            previous = node;
        }
    }
};

const Cmd = union(enum) {
    open: struct { id: i64, session: []u8, workspace: []u8, ask: bool },
    submit: struct { id: i64, session: []u8, text: []u8 },
    state: struct { id: i64, session: []u8 },
    resolve: struct { id: i64, session: []u8, granted: bool },
    cancel: struct { id: i64, session: []u8 },
    tool_result: struct { session: *Session, output: []u8 },
    spawned: struct { session: *Session, pid: c.pid_t, fd: c.fd_t },
    saved: *Session,
    loaded: struct { session: *Session, bytes: []u8 },
};

const Config = struct {
    reactors: usize = 4,
    workers: usize = 4,
    model_limit: usize = 4096,
    tool_limit: usize = 64,
    first_byte_ms: u32 = 180_000,
    park: bool = false,
    park_dir: []const u8 = "",
    host_port: u16 = 0,
};

var config: Config = .{};
var system_prompt: []u8 = "";

const Reactor = struct {
    kq: c.fd_t,
    lock: Lock = .{},
    inbox: std.ArrayList(Cmd) = .empty,
    sessions: std.StringHashMapUnmanaged(*Session) = .empty,
    model_wait: Fifo = .{},
    tool_wait: Fifo = .{},
    model_running: usize = 0,
    tool_running: usize = 0,
    idle_conns: std.ArrayList(*Conn) = .empty,
    conns_opened: usize = 0,
    dead_conns: std.ArrayList(*Conn) = .empty,
    journal: std.ArrayList(u8) = .empty,
    journal_fd: c.fd_t = -1,
    journal_sync: bool = false,

    fn post(self: *Reactor, cmd: Cmd) void {
        self.lock.acquire();
        const was_empty = self.inbox.items.len == 0;
        self.inbox.append(gpa, cmd) catch @panic("oom");
        self.lock.release();
        if (!was_empty) return;
        self.change(1, c.EVFILT.USER, 0, c.NOTE.TRIGGER, 0, 0);
    }

    fn change(self: *Reactor, ident: usize, filter: i16, flags: u16, fflags: u32, data: isize, udata: usize) void {
        var one = [_]c.Kevent{.{ .ident = ident, .filter = filter, .flags = flags, .fflags = fflags, .data = data, .udata = udata }};
        _ = c.kevent(self.kq, &one, 1, undefined, 0, null);
    }

    fn loop(self: *Reactor) void {
        self.change(1, c.EVFILT.USER, c.EV.ADD | c.EV.CLEAR, 0, 0, 0);
        var events: [256]c.Kevent = undefined;
        var scratch: [65536]u8 = undefined;
        while (true) {
            const got = c.kevent(self.kq, undefined, 0, &events, events.len, null);
            if (got < 0) continue;
            for (events[0..@intCast(got)]) |ev| {
                if (ev.filter == c.EVFILT.USER) {
                    self.drain();
                    continue;
                }
                if (ev.udata & 1 == 1) {
                    const conn: *Conn = @ptrFromInt(ev.udata & ~@as(usize, 1));
                    self.dispatchConn(conn, ev, &scratch) catch |err| std.debug.panic("conn: {s}", .{@errorName(err)});
                    continue;
                }
                const s: *Session = @ptrFromInt(ev.udata);
                self.dispatch(s, ev, &scratch) catch |err| std.debug.panic("dispatch: {s}", .{@errorName(err)});
            }
            for (self.dead_conns.items) |conn| conn.destroy();
            self.dead_conns.clearRetainingCapacity();
            self.flushJournal();
        }
    }

    fn drain(self: *Reactor) void {
        while (true) {
            self.lock.acquire();
            var batch = self.inbox;
            self.inbox = .empty;
            self.lock.release();
            if (batch.items.len == 0) {
                batch.deinit(gpa);
                return;
            }
            for (batch.items) |cmd| self.handle(cmd) catch |err| std.debug.panic("handle: {s}", .{@errorName(err)});
            batch.deinit(gpa);
        }
    }

    fn flushJournal(self: *Reactor) void {
        if (self.journal.items.len == 0) return;
        writeAll(self.journal_fd, self.journal.items);
        self.journal.clearRetainingCapacity();
        if (self.journal_sync) _ = c.fsync(self.journal_fd);
        self.journal_sync = false;
    }

    fn record(self: *Reactor, s: *Session, event: Event) !void {
        const kind = @tagName(event);
        const text = switch (event) {
            .delta => |t| t,
            .tool_done => |t| t,
            .submit => |t| t,
            else => "",
        };
        const encoded = try std.json.Stringify.valueAlloc(gpa, .{
            .protocol = "open-agent-protocol",
            .version = "0.1",
            .profile = "open-agent-protocol.agent-control-core",
            .type = kind,
            .session_id = s.id,
            .run_id = s.seq,
            .text = text,
        }, .{});
        defer gpa.free(encoded);
        try self.journal.appendSlice(gpa, encoded);
        try self.journal.append(gpa, '\n');
    }

    fn feed(self: *Reactor, s: *Session, event: Event) anyerror!void {
        const run = s.run orelse return error.RunParked;
        var out = Actions{};
        try self.record(s, event);
        try step(run, event, &out);
        s.phase = run.phase;
        s.seq = run.seq;
        s.gate_seq = run.gate_seq;
        for (out.slice()) |action| try self.apply(s, action);
        self.maybePark(s);
    }

    fn apply(self: *Reactor, s: *Session, action: Action) anyerror!void {
        switch (action) {
            .queue_model => {
                self.model_wait.push(s);
                try self.admitModels();
            },
            .send_model => try self.sendModel(s),
            .release_model => {
                self.detachConn(s);
                self.model_running -= 1;
                try self.admitModels();
            },
            .queue_tool => {
                self.tool_wait.push(s);
                try self.admitTools();
            },
            .run_tool => try self.runTool(s),
            .release_tool => {
                self.tool_running -= 1;
                try self.admitTools();
            },
            .open_gate => {},
            .settled => self.journal_sync = true,
            .arm_timer => |ms| self.change(@intFromPtr(s), c.EVFILT.TIMER, c.EV.ADD | c.EV.ONESHOT, 0, ms, @intFromPtr(s)),
            .abort_io => {
                self.model_wait.remove(s);
                self.tool_wait.remove(s);
                if (s.conn) |conn| self.closeConn(conn);
                if (s.child) |child| if (child.pid != 0) {
                    _ = c.kill(child.pid, .KILL);
                };
            },
        }
    }

    fn admitModels(self: *Reactor) anyerror!void {
        while (self.model_running < config.model_limit / config.reactors) {
            const s = self.model_wait.pop() orelse return;
            self.model_running += 1;
            try self.feed(s, .model_admitted);
        }
    }

    fn admitTools(self: *Reactor) anyerror!void {
        while (self.tool_running < config.tool_limit / config.reactors) {
            const s = self.tool_wait.pop() orelse return;
            self.tool_running += 1;
            try self.feed(s, .tool_admitted);
        }
    }

    fn firstByteTimer(self: *Reactor, s: *Session, flags: u16, ms: u32) void {
        self.change(@intFromPtr(s) | 1, c.EVFILT.TIMER, flags, 0, ms, @intFromPtr(s));
    }

    fn detachConn(self: *Reactor, s: *Session) void {
        const conn = s.conn orelse return;
        self.firstByteTimer(s, c.EV.DELETE, 0);
        conn.owner = null;
        s.conn = null;
    }

    fn closeConn(self: *Reactor, conn: *Conn) void {
        if (conn.owner) |s| self.detachConn(s);
        if (conn.idle) {
            for (self.idle_conns.items, 0..) |held, index| {
                if (held == conn) {
                    _ = self.idle_conns.swapRemove(index);
                    break;
                }
            }
        }
        _ = c.close(conn.fd);
        conn.fd = -1;
        self.dead_conns.append(gpa, conn) catch @panic("oom");
    }

    fn failConn(self: *Reactor, conn: *Conn, text: []const u8, transient: bool) anyerror!void {
        const owner = conn.owner;
        self.closeConn(conn);
        if (owner) |s| try self.feed(s, .{ .failed = .{ .transient = transient, .text = text } });
    }

    fn sendModel(self: *Reactor, s: *Session) anyerror!void {
        const run = s.run.?;
        const conn = if (self.idle_conns.pop()) |held| held else fresh: {
            const fd = c.socket(c.AF.INET, c.SOCK.STREAM, 0);
            if (fd < 0) return self.feed(s, .{ .failed = .{ .transient = true, .text = "socket failed" } });
            _ = c.fcntl(fd, c.F.SETFL, @as(c_int, 0x0004));
            var address = std.mem.zeroes(c.sockaddr.in);
            address.family = c.AF.INET;
            address.port = std.mem.nativeToBig(u16, config.host_port);
            address.addr = std.mem.nativeToBig(u32, 0x7f000001);
            _ = c.connect(fd, @ptrCast(&address), @sizeOf(c.sockaddr.in));
            const made = try gpa.create(Conn);
            made.* = .{ .fd = fd };
            self.conns_opened += 1;
            self.change(@intCast(fd), c.EVFILT.READ, c.EV.ADD, 0, 0, @intFromPtr(made) | 1);
            break :fresh made;
        };
        conn.idle = false;
        conn.reset();
        conn.send = try requestBytes(run);
        conn.owner = s;
        s.conn = conn;
        self.change(@intCast(conn.fd), c.EVFILT.WRITE, c.EV.ADD, 0, 0, @intFromPtr(conn) | 1);
        self.firstByteTimer(s, c.EV.ADD | c.EV.ONESHOT, config.first_byte_ms);
    }

    fn dispatch(self: *Reactor, s: *Session, ev: c.Kevent, scratch: []u8) anyerror!void {
        if (ev.filter == c.EVFILT.TIMER) {
            if (ev.ident & 1 == 1) {
                if (s.conn) |conn| if (!conn.first_byte) try self.failConn(conn, "timed out before the first byte", true);
                return;
            }
            return self.feed(s, .timer);
        }
        if (ev.filter == c.EVFILT.PROC) {
            if (s.child) |child| if (child.pid == @as(c.pid_t, @intCast(ev.ident))) {
                child.exited = true;
                try self.finishChild(s);
            };
            return;
        }
        if (s.child) |child| if (child.fd == @as(c.fd_t, @intCast(ev.ident))) return self.readChild(s, child, scratch);
    }

    fn dispatchConn(self: *Reactor, conn: *Conn, ev: c.Kevent, scratch: []u8) anyerror!void {
        if (conn.fd < 0) return;
        if (ev.filter == c.EVFILT.WRITE) return self.writeConn(conn);
        return self.readConn(conn, scratch);
    }

    fn writeConn(self: *Reactor, conn: *Conn) anyerror!void {
        while (conn.sent < conn.send.len) {
            const rest = conn.send[conn.sent..];
            const n = c.write(conn.fd, rest.ptr, rest.len);
            if (n < 0) {
                if (std.c._errno().* == @intFromEnum(c.E.AGAIN)) return;
                return self.failConn(conn, "send failed", true);
            }
            conn.sent += @intCast(n);
        }
        self.change(@intCast(conn.fd), c.EVFILT.WRITE, c.EV.DELETE, 0, 0, 0);
    }

    fn readConn(self: *Reactor, conn: *Conn, scratch: []u8) anyerror!void {
        while (true) {
            const n = c.read(conn.fd, scratch.ptr, scratch.len);
            if (n < 0) {
                if (std.c._errno().* == @intFromEnum(c.E.AGAIN)) return;
                return self.failConn(conn, "read error", true);
            }
            if (n == 0) return self.failConn(conn, "read error", true);
            conn.first_byte = true;
            try conn.recv.appendSlice(gpa, scratch[0..@intCast(n)]);
            if (try self.parseHttp(conn)) return;
        }
    }

    fn parseHttp(self: *Reactor, conn: *Conn) anyerror!bool {
        if (!conn.head_done) {
            const end = std.mem.indexOf(u8, conn.recv.items, "\r\n\r\n") orelse return false;
            if (std.mem.indexOf(u8, conn.recv.items[0..end], " 200 ") == null) {
                try self.failConn(conn, "model request failed", false);
                return true;
            }
            conn.head_done = true;
            conn.recv.replaceRange(gpa, 0, end + 4, &.{}) catch unreachable;
        }
        var at: usize = 0;
        while (at < conn.recv.items.len) {
            if (conn.chunk_left == 0) {
                const line = std.mem.indexOfPos(u8, conn.recv.items, at, "\r\n") orelse break;
                if (line == at) {
                    at += 2;
                    continue;
                }
                conn.chunk_left = std.fmt.parseInt(usize, conn.recv.items[at..line], 16) catch return error.BadChunk;
                at = line + 2;
                if (conn.chunk_left == 0) {
                    if (conn.owner != null) {
                        try self.failConn(conn, "read error", true);
                        return true;
                    }
                    conn.reset();
                    conn.idle = true;
                    try self.idle_conns.append(gpa, conn);
                    return true;
                }
            }
            const take = @min(conn.chunk_left, conn.recv.items.len - at);
            try conn.sse.appendSlice(gpa, conn.recv.items[at .. at + take]);
            conn.chunk_left -= take;
            at += take;
            try self.parseSse(conn);
        }
        conn.recv.replaceRange(gpa, 0, at, &.{}) catch unreachable;
        return false;
    }

    fn parseSse(self: *Reactor, conn: *Conn) anyerror!void {
        var at: usize = 0;
        while (std.mem.indexOfPos(u8, conn.sse.items, at, "\n\n")) |end| {
            const frame = conn.sse.items[at..end];
            at = end + 2;
            if (!std.mem.startsWith(u8, frame, "data: ")) continue;
            const data = frame[6..];
            if (std.mem.eql(u8, data, "[DONE]")) continue;
            if (conn.owner) |s| try self.handleFrame(s, data);
        }
        conn.sse.replaceRange(gpa, 0, at, &.{}) catch unreachable;
    }

    fn handleFrame(self: *Reactor, s: *Session, data: []const u8) anyerror!void {
        var parsed = try std.json.parseFromSlice(std.json.Value, gpa, data, .{});
        defer parsed.deinit();
        const choice = parsed.value.object.get("choices").?.array.items[0].object;
        if (choice.get("delta")) |delta| {
            if (delta.object.get("content")) |text| if (text == .string) try self.feed(s, .{ .delta = text.string });
            if (delta.object.get("tool_calls")) |calls| {
                for (calls.array.items) |call| {
                    const function = call.object.get("function").?.object;
                    try self.feed(s, .{ .tool_delta = .{
                        .id = if (call.object.get("id")) |v| v.string else "",
                        .name = if (function.get("name")) |v| v.string else "",
                        .args = if (function.get("arguments")) |v| v.string else "",
                    } });
                }
            }
        }
        if (choice.get("finish_reason")) |reason| if (reason == .string) try self.feed(s, .finished);
    }

    fn runTool(self: *Reactor, s: *Session) anyerror!void {
        const run = s.run.?;
        var parsed = try std.json.parseFromSlice(std.json.Value, gpa, run.call_args.items, .{});
        defer parsed.deinit();
        const name = run.call_name.items;
        const args = parsed.value.object;
        var job = Job{ .reactor = self, .session = s, .kind = .read };
        if (std.mem.eql(u8, name, "Shell")) {
            job.kind = .spawn;
            job.command = try std.fmt.allocPrintSentinel(gpa, "{s}", .{args.get("command").?.string}, 0);
            s.child = try gpa.create(Child);
            s.child.?.* = .{};
        } else {
            job.path = try std.fmt.allocPrintSentinel(gpa, "{s}/{s}", .{ run.workspace, args.get("path").?.string }, 0);
            if (std.mem.eql(u8, name, "Write")) {
                job.kind = .write;
                job.bytes = try gpa.dupe(u8, args.get("content").?.string);
            }
        }
        pool.submit(job);
    }

    fn readChild(self: *Reactor, s: *Session, child: *Child, scratch: []u8) anyerror!void {
        while (true) {
            const n = c.read(child.fd, scratch.ptr, scratch.len);
            if (n < 0) {
                if (std.c._errno().* == @intFromEnum(c.E.AGAIN)) return;
                break;
            }
            if (n == 0) break;
            try child.output.appendSlice(gpa, scratch[0..@intCast(n)]);
        }
        child.eof = true;
        _ = c.close(child.fd);
        child.fd = -1;
        try self.finishChild(s);
    }

    fn finishChild(self: *Reactor, s: *Session) anyerror!void {
        const child = s.child orelse return;
        if (!child.eof or !child.exited) return;
        _ = c.waitpid(child.pid, null, 0);
        s.child = null;
        defer {
            child.output.deinit(gpa);
            gpa.destroy(child);
        }
        if (s.phase == .tool) try self.feed(s, .{ .tool_done = child.output.items });
    }

    fn maybePark(self: *Reactor, s: *Session) void {
        if (!config.park or s.saving or s.loading) return;
        if (s.phase != .gate and s.phase != .idle) return;
        if (s.pending != .none) return;
        const run = s.run orelse return;
        const bytes = run.encode(gpa) catch @panic("oom");
        run.destroy();
        s.run = null;
        s.saving = true;
        pool.submit(.{ .reactor = self, .session = s, .kind = .park_write, .bytes = bytes, .path = parkPath(s) });
    }

    fn parkPath(s: *Session) [:0]u8 {
        return std.fmt.allocPrintSentinel(gpa, "{s}/{s}.json", .{ config.park_dir, s.id }, 0) catch @panic("oom");
    }

    fn load(self: *Reactor, s: *Session) void {
        if (s.saving or s.loading) return;
        s.loading = true;
        pool.submit(.{ .reactor = self, .session = s, .kind = .park_read, .path = parkPath(s) });
    }

    fn deliver(self: *Reactor, s: *Session) anyerror!void {
        const pending = s.pending;
        s.pending = .none;
        switch (pending) {
            .none => {},
            .gate => |g| {
                try self.feed(s, .{ .gate = g.granted });
                reply(g.id, "{}");
            },
            .submit => |u| {
                try self.feed(s, .{ .submit = u.text });
                gpa.free(u.text);
                reply(u.id, "{}");
            },
            .cancel => |id| {
                try self.feed(s, .cancel);
                reply(id, "{}");
            },
        }
    }

    fn withRun(self: *Reactor, s: *Session, pending: Pending) anyerror!void {
        s.pending = pending;
        if (s.run == null) return self.load(s);
        try self.deliver(s);
    }

    fn handle(self: *Reactor, cmd: Cmd) anyerror!void {
        switch (cmd) {
            .open => |o| {
                const s = try gpa.create(Session);
                s.* = .{ .id = o.session, .reactor = self, .run = try Run.create(o.session, o.workspace, o.ask) };
                gpa.free(o.workspace);
                try self.sessions.put(gpa, s.id, s);
                self.maybePark(s);
                reply(o.id, "{}");
            },
            .submit => |u| {
                defer gpa.free(u.session);
                const s = self.sessions.get(u.session) orelse return refuse(u.id, "no session");
                try self.withRun(s, .{ .submit = .{ .id = u.id, .text = u.text } });
            },
            .state => |q| {
                defer gpa.free(q.session);
                const s = self.sessions.get(q.session) orelse return refuse(q.id, "no session");
                var text: [256]u8 = undefined;
                const busy = s.phase != .idle or s.pending != .none;
                const body = if (!busy)
                    "{\"payload\":{\"status\":\"idle\",\"active_runs\":[]}}"
                else if (s.phase == .gate and s.pending == .none)
                    try std.fmt.bufPrint(&text, "{{\"payload\":{{\"status\":\"running\",\"active_runs\":[{{\"run_id\":\"r{d}\",\"pending_interactions\":[\"permission-{d}\"]}}]}}}}", .{ s.seq, s.gate_seq })
                else
                    try std.fmt.bufPrint(&text, "{{\"payload\":{{\"status\":\"running\",\"active_runs\":[{{\"run_id\":\"r{d}\"}}]}}}}", .{s.seq});
                reply(q.id, body);
            },
            .resolve => |r| {
                defer gpa.free(r.session);
                const s = self.sessions.get(r.session) orelse return refuse(r.id, "no session");
                try self.withRun(s, .{ .gate = .{ .id = r.id, .granted = r.granted } });
            },
            .cancel => |k| {
                defer gpa.free(k.session);
                const s = self.sessions.get(k.session) orelse return refuse(k.id, "no session");
                try self.withRun(s, .{ .cancel = k.id });
            },
            .tool_result => |t| {
                defer gpa.free(t.output);
                if (t.session.phase == .tool) try self.feed(t.session, .{ .tool_done = t.output });
            },
            .spawned => |p| {
                const s = p.session;
                const child = s.child.?;
                child.pid = p.pid;
                child.fd = p.fd;
                if (s.phase != .tool) _ = c.kill(p.pid, .KILL);
                _ = c.fcntl(p.fd, c.F.SETFL, @as(c_int, 0x0004));
                self.change(@intCast(p.fd), c.EVFILT.READ, c.EV.ADD, 0, 0, @intFromPtr(s));
                self.change(@intCast(p.pid), c.EVFILT.PROC, c.EV.ADD | c.EV.ONESHOT, c.NOTE.EXIT, 0, @intFromPtr(s));
            },
            .saved => |s| {
                s.saving = false;
                if (s.pending != .none) self.load(s);
            },
            .loaded => |l| {
                defer gpa.free(l.bytes);
                const s = l.session;
                s.loading = false;
                s.run = try Run.decode(l.bytes);
                try self.deliver(s);
            },
        }
    }
};

fn requestBytes(run: *const Run) ![]u8 {
    var arena = std.heap.ArenaAllocator.init(gpa);
    defer arena.deinit();
    const a = arena.allocator();
    var body: std.ArrayList(u8) = .empty;
    try body.appendSlice(a, "{\"model\":\"bench-model\",\"stream\":true,\"messages\":[{\"role\":\"system\",\"content\":");
    try body.appendSlice(a, try std.json.Stringify.valueAlloc(a, system_prompt, .{}));
    try body.append(a, '}');
    for (run.messages.items) |m| {
        try body.appendSlice(a, ",{\"role\":\"");
        try body.appendSlice(a, @tagName(m.role));
        try body.appendSlice(a, "\",\"content\":");
        try body.appendSlice(a, try std.json.Stringify.valueAlloc(a, m.content, .{}));
        if (m.role == .tool) {
            try body.appendSlice(a, ",\"tool_call_id\":");
            try body.appendSlice(a, try std.json.Stringify.valueAlloc(a, m.tool_call_id, .{}));
        } else if (m.role == .assistant and m.tool_name.len > 0) {
            try body.appendSlice(a, ",\"tool_calls\":[{\"id\":");
            try body.appendSlice(a, try std.json.Stringify.valueAlloc(a, m.tool_call_id, .{}));
            try body.appendSlice(a, ",\"type\":\"function\",\"function\":{\"name\":");
            try body.appendSlice(a, try std.json.Stringify.valueAlloc(a, m.tool_name, .{}));
            try body.appendSlice(a, ",\"arguments\":");
            try body.appendSlice(a, try std.json.Stringify.valueAlloc(a, m.tool_args, .{}));
            try body.appendSlice(a, "}}]");
        }
        try body.append(a, '}');
    }
    try body.appendSlice(a, "]}");
    return std.fmt.allocPrint(gpa, "POST /v1/chat/completions HTTP/1.1\r\nHost: 127.0.0.1\r\nContent-Type: application/json\r\nContent-Length: {d}\r\n\r\n{s}", .{ body.items.len, body.items });
}

const Job = struct {
    reactor: *Reactor,
    session: *Session,
    kind: enum { read, write, spawn, park_write, park_read },
    path: [:0]u8 = undefined,
    bytes: []u8 = "",
    command: [:0]u8 = undefined,
};

const Pool = struct {
    lock: Lock = .{},
    ready: c.pthread_cond_t = .{},
    jobs: std.ArrayList(Job) = .empty,
    head: usize = 0,

    fn submit(self: *Pool, job: Job) void {
        self.lock.acquire();
        self.jobs.append(gpa, job) catch @panic("oom");
        self.lock.release();
        _ = c.pthread_cond_signal(&self.ready);
    }

    fn work(self: *Pool) void {
        while (true) {
            self.lock.acquire();
            while (self.head == self.jobs.items.len) {
                self.jobs.clearRetainingCapacity();
                self.head = 0;
                _ = c.pthread_cond_wait(&self.ready, &self.lock.inner);
            }
            const job = self.jobs.items[self.head];
            self.head += 1;
            self.lock.release();
            run_job(job);
        }
    }
};

var pool: Pool = .{};

fn readFile(path: [:0]const u8) []u8 {
    const fd = c.open(path, .{ .ACCMODE = .RDONLY });
    if (fd < 0) return gpa.dupe(u8, "Tool execution failed: open") catch @panic("oom");
    defer _ = c.close(fd);
    var out: std.ArrayList(u8) = .empty;
    var chunk: [16384]u8 = undefined;
    while (true) {
        const n = c.read(fd, &chunk, chunk.len);
        if (n <= 0) break;
        out.appendSlice(gpa, chunk[0..@intCast(n)]) catch @panic("oom");
    }
    return out.toOwnedSlice(gpa) catch @panic("oom");
}

fn writeFile(path: [:0]const u8, bytes: []const u8) bool {
    const fd = c.open(path, .{ .ACCMODE = .WRONLY, .CREAT = true, .TRUNC = true }, @as(c.mode_t, 0o644));
    if (fd < 0) return false;
    defer _ = c.close(fd);
    writeAll(fd, bytes);
    return true;
}

fn spawnShell(job: Job) void {
    var fds: [2]c.fd_t = undefined;
    if (c.pipe(&fds) != 0) @panic("pipe");
    var actions: c.posix_spawn_file_actions_t = undefined;
    _ = c.posix_spawn_file_actions_init(&actions);
    defer _ = c.posix_spawn_file_actions_destroy(&actions);
    _ = c.posix_spawn_file_actions_adddup2(&actions, fds[1], 1);
    _ = c.posix_spawn_file_actions_adddup2(&actions, fds[1], 2);
    _ = c.posix_spawn_file_actions_addclose(&actions, fds[0]);
    _ = c.posix_spawn_file_actions_addclose(&actions, fds[1]);
    const argv = [_:null]?[*:0]const u8{ "/bin/sh", "-c", job.command.ptr };
    var pid: c.pid_t = 0;
    const failed = c.posix_spawn(&pid, "/bin/sh", &actions, null, &argv, @ptrCast(c.environ));
    _ = c.close(fds[1]);
    gpa.free(job.command);
    if (failed != 0) @panic("spawn");
    job.reactor.post(.{ .spawned = .{ .session = job.session, .pid = pid, .fd = fds[0] } });
}

fn run_job(job: Job) void {
    switch (job.kind) {
        .read => {
            defer gpa.free(job.path);
            job.reactor.post(.{ .tool_result = .{ .session = job.session, .output = readFile(job.path) } });
        },
        .write => {
            defer gpa.free(job.path);
            defer gpa.free(job.bytes);
            const text = if (writeFile(job.path, job.bytes)) "wrote the file" else "Tool execution failed: write";
            job.reactor.post(.{ .tool_result = .{ .session = job.session, .output = gpa.dupe(u8, text) catch @panic("oom") } });
        },
        .spawn => spawnShell(job),
        .park_write => {
            defer gpa.free(job.path);
            defer gpa.free(job.bytes);
            if (!writeFile(job.path, job.bytes)) @panic("park write");
            job.reactor.post(.{ .saved = job.session });
        },
        .park_read => {
            defer gpa.free(job.path);
            job.reactor.post(.{ .loaded = .{ .session = job.session, .bytes = readFile(job.path) } });
        },
    }
}

var reactors: []Reactor = &.{};

fn shard(session: []const u8) *Reactor {
    return &reactors[std.hash.Wyhash.hash(0, session) % reactors.len];
}

fn dupe(text: []const u8) []u8 {
    return gpa.dupe(u8, text) catch @panic("oom");
}

fn handleLine(line: []const u8) !void {
    var parsed = try std.json.parseFromSlice(std.json.Value, gpa, line, .{});
    defer parsed.deinit();
    const root = parsed.value.object;
    const id = root.get("id").?.integer;
    const op = root.get("op").?.string;
    if (std.mem.eql(u8, op, "adapters")) return reply(id, "{\"adapters\":[\"loop\"]}");
    if (std.mem.eql(u8, op, "open")) {
        const payload = root.get("request").?.object.get("payload").?.object;
        const session = payload.get("session_id").?.string;
        var workspace: []const u8 = "";
        var ask = false;
        if (payload.get("metadata")) |metadata| if (metadata.object.get("oapx")) |oapx| {
            if (oapx.object.get("workspace_root")) |value| workspace = value.string;
            if (oapx.object.get("permission_mode")) |value| ask = std.mem.eql(u8, value.string, "ask");
        };
        return shard(session).post(.{ .open = .{ .id = id, .session = dupe(session), .workspace = dupe(workspace), .ask = ask } });
    }
    const session = root.get("session_id").?.string;
    if (std.mem.eql(u8, op, "state")) return shard(session).post(.{ .state = .{ .id = id, .session = dupe(session) } });
    if (std.mem.eql(u8, op, "cancel")) return shard(session).post(.{ .cancel = .{ .id = id, .session = dupe(session) } });
    const payload = root.get("request").?.object.get("payload").?.object;
    if (std.mem.eql(u8, op, "submit")) {
        const text = payload.get("messages").?.array.items[0].object.get("content").?.string;
        return shard(session).post(.{ .submit = .{ .id = id, .session = dupe(session), .text = dupe(text) } });
    }
    if (std.mem.eql(u8, op, "resolve")) {
        const granted = if (payload.get("granted")) |value| value.bool else false;
        return shard(session).post(.{ .resolve = .{ .id = id, .session = dupe(session), .granted = granted } });
    }
    refuse(id, "unknown op");
}

fn envNumber(name: [*:0]const u8, fallback: usize) usize {
    const text = c.getenv(name) orelse return fallback;
    return std.fmt.parseInt(usize, std.mem.span(text), 10) catch fallback;
}

fn readProviderPort() !u16 {
    const home = std.mem.span(c.getenv("HOME") orelse return error.NoHome);
    const path = try std.fmt.allocPrintSentinel(gpa, "{s}/.oapx/providers.json", .{home}, 0);
    defer gpa.free(path);
    const bytes = readFile(path);
    defer gpa.free(bytes);
    const marker = "127.0.0.1:";
    const at = (std.mem.indexOf(u8, bytes, marker) orelse return error.NoProvider) + marker.len;
    var end = at;
    while (end < bytes.len and std.ascii.isDigit(bytes[end])) end += 1;
    return std.fmt.parseInt(u16, bytes[at..end], 10);
}

fn selftest() !void {
    const script = [_]Event{
        .{ .submit = "first" },       .model_admitted,
        .{ .delta = "thinking " },    .{ .tool_delta = .{ .id = "c1", .name = "Shell", .args = "{\"command\":\"true\"}" } },
        .finished,                    .{ .gate = true },
        .tool_admitted,               .{ .tool_done = "output" },
        .model_admitted,              .{ .failed = .{ .transient = true, .text = "read error" } },
        .timer,                       .model_admitted,
        .{ .delta = "done" },         .finished,
        .{ .submit = "second" },      .model_admitted,
        .{ .tool_delta = .{ .id = "c2", .name = "Read", .args = "{}" } },
        .finished,
        .tool_admitted,               .{ .tool_done = "file" },
        .model_admitted,              .{ .delta = "end" },
        .finished,
    };
    const straight = try Run.create("s", "/w", true);
    defer straight.destroy();
    for (script) |event| {
        var out = Actions{};
        try step(straight, event, &out);
    }
    const expected = try straight.encode(gpa);
    defer gpa.free(expected);
    var checked: usize = 0;
    for (0..script.len + 1) |cut| {
        var run = try Run.create("s", "/w", true);
        defer run.destroy();
        for (script, 0..) |event, index| {
            if (index == cut) {
                const bytes = try run.encode(gpa);
                defer gpa.free(bytes);
                const restored = try Run.decode(bytes);
                const again = try restored.encode(gpa);
                defer gpa.free(again);
                if (!std.mem.eql(u8, bytes, again)) return error.RoundTripDiffers;
                run.destroy();
                run = restored;
                checked += 1;
            }
            var out = Actions{};
            try step(run, event, &out);
        }
        const finished = try run.encode(gpa);
        defer gpa.free(finished);
        if (!std.mem.eql(u8, finished, expected)) return error.ResumedRunDiffers;
    }
    var line: [96]u8 = undefined;
    writeAll(1, try std.fmt.bufPrint(&line, "selftest ok: suspended and resumed at {d} of {d} points\n", .{ checked, script.len + 1 }));
}

pub fn main() !void {
    if (c.getenv("PROTO_SELFTEST") != null) return selftest();
    config.reactors = envNumber("PROTO_REACTORS", 4);
    config.workers = envNumber("PROTO_WORKERS", 4);
    config.model_limit = envNumber("PROTO_MODEL_LIMIT", 4096);
    config.tool_limit = envNumber("PROTO_TOOL_LIMIT", 64);
    config.park = default_park or c.getenv("PROTO_PARK") != null;
    if (config.park) {
        const dir = std.mem.span(c.getenv("TMPDIR") orelse return error.NoTmp);
        const path = try std.fmt.allocPrintSentinel(gpa, "{s}/park", .{dir}, 0);
        _ = c.mkdir(path, 0o755);
        config.park_dir = path;
    }
    config.host_port = try readProviderPort();
    system_prompt = try gpa.alloc(u8, 8192);
    @memset(system_prompt, 'x');
    reactors = try gpa.alloc(Reactor, config.reactors);
    for (reactors) |*reactor| {
        reactor.* = .{ .kq = c.kqueue() };
        const dir = std.mem.span(c.getenv("TMPDIR") orelse return error.NoTmp);
        const path = try std.fmt.allocPrintSentinel(gpa, "{s}/journal-{d}.jsonl", .{ dir, @intFromPtr(reactor) }, 0);
        reactor.journal_fd = c.open(path, .{ .ACCMODE = .WRONLY, .CREAT = true, .APPEND = true }, @as(c.mode_t, 0o644));
        _ = try std.Thread.spawn(.{}, Reactor.loop, .{reactor});
    }
    for (0..config.workers) |_| _ = try std.Thread.spawn(.{}, Pool.work, .{&pool});
    var buffer: std.ArrayList(u8) = .empty;
    var chunk: [65536]u8 = undefined;
    while (true) {
        const n = c.read(0, &chunk, chunk.len);
        if (n <= 0) break;
        try buffer.appendSlice(gpa, chunk[0..@intCast(n)]);
        var at: usize = 0;
        while (std.mem.indexOfScalarPos(u8, buffer.items, at, '\n')) |end| {
            try handleLine(buffer.items[at..end]);
            at = end + 1;
        }
        buffer.replaceRange(gpa, 0, at, &.{}) catch unreachable;
    }
    c._exit(0);
}
