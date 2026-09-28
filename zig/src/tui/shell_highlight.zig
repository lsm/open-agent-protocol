const std = @import("std");
const zz = @import("zigzag");
const tui_theme = @import("tui_theme");

pub const Kind = enum {
    text,
    command,
    keyword,
    flag,
    string,
    variable,
    operator,
    comment,
    newline,
};

pub const Token = struct {
    kind: Kind,
    start: usize,
    end: usize,
};

const opening_keywords = [_][]const u8{ "if", "then", "else", "elif", "while", "until", "do", "time" };
const closing_keywords = [_][]const u8{ "fi", "done", "esac" };
const binding_keywords = [_][]const u8{ "for", "case", "select", "function" };

const Heredoc = struct {
    delimiter: []const u8,
    strip_tabs: bool,
};

pub const Lexer = struct {
    source: []const u8,
    index: usize = 0,
    expect_command: bool = true,
    expect_in: bool = false,
    redirect_target: bool = false,
    assignment: bool = false,
    continued: bool = false,
    line_start: bool = true,
    pending_heredoc: ?Heredoc = null,
    heredoc: ?Heredoc = null,

    pub fn init(source: []const u8) Lexer {
        return .{ .source = source };
    }

    pub fn next(self: *Lexer) ?Token {
        if (self.index >= self.source.len) return null;
        if (self.line_start) {
            self.line_start = false;
            if (self.heredoc) |doc| {
                if (self.source[self.index] != '\n') return self.heredocLine(doc);
            }
        }
        const start = self.index;
        const c = self.source[start];
        if (c == '\n') return self.newline(start);
        self.continued = false;
        switch (c) {
            ' ', '\t', '\r' => return self.blank(start),
            '\\' => if (start + 1 < self.source.len and self.source[start + 1] == '\n') {
                self.index = start + 1;
                self.continued = true;
                return .{ .kind = .text, .start = start, .end = self.index };
            },
            '#' => if (self.atBoundary(start)) return self.comment(start),
            '\'' => return self.quoted(start, start, '\'', false),
            '"' => return self.quoted(start, start, '"', true),
            '$' => return self.dollar(start),
            '`' => {
                self.index = start + 1;
                self.expect_command = true;
                self.redirect_target = false;
                self.assignment = false;
                return .{ .kind = .operator, .start = start, .end = self.index };
            },
            else => {},
        }
        if (self.redirection(start)) |end| {
            self.index = end;
            self.assignment = false;
            self.redirect_target = !isDuplication(self.source[start..end]);
            return .{ .kind = .operator, .start = start, .end = end };
        }
        if (operatorLength(self.source[start..])) |len| {
            self.index = start + len;
            self.redirect_target = false;
            self.assignment = false;
            self.expect_in = false;
            self.expect_command = c != ')';
            return .{ .kind = .operator, .start = start, .end = self.index };
        }
        return self.word(start);
    }

    fn newline(self: *Lexer, start: usize) Token {
        self.index = start + 1;
        self.line_start = true;
        if (self.pending_heredoc) |doc| {
            self.heredoc = doc;
            self.pending_heredoc = null;
        }
        if (!self.continued) {
            self.expect_command = true;
            self.expect_in = false;
        }
        self.redirect_target = false;
        self.assignment = false;
        self.continued = false;
        return .{ .kind = .newline, .start = start, .end = self.index };
    }

    fn blank(self: *Lexer, start: usize) Token {
        var i = start;
        while (i < self.source.len) : (i += 1) {
            switch (self.source[i]) {
                ' ', '\t', '\r' => {},
                else => break,
            }
        }
        self.index = i;
        self.assignment = false;
        return .{ .kind = .text, .start = start, .end = i };
    }

    fn atBoundary(self: *const Lexer, index: usize) bool {
        if (index == 0) return true;
        return switch (self.source[index - 1]) {
            ' ', '\t', '\r', '\n', ';', '|', '&', '(', ')' => true,
            else => false,
        };
    }

    fn comment(self: *Lexer, start: usize) Token {
        const end = std.mem.indexOfScalarPos(u8, self.source, start, '\n') orelse self.source.len;
        self.index = end;
        return .{ .kind = .comment, .start = start, .end = end };
    }

    fn quoted(self: *Lexer, start: usize, open: usize, quote: u8, escapes: bool) Token {
        var i = open + 1;
        while (i < self.source.len) {
            const d = self.source[i];
            if (escapes and d == '\\') {
                i = @min(self.source.len, i + 2);
                continue;
            }
            i += 1;
            if (d == quote) break;
        }
        self.index = i;
        self.settleWord();
        return .{ .kind = .string, .start = start, .end = i };
    }

    fn dollar(self: *Lexer, start: usize) Token {
        const s = self.source;
        if (start + 1 < s.len) {
            const d = s[start + 1];
            if (d == '\'') return self.quoted(start, start + 1, '\'', true);
            if (d == '(') {
                const arithmetic = start + 2 < s.len and s[start + 2] == '(';
                self.index = if (arithmetic) start + 3 else start + 2;
                self.expect_command = !arithmetic;
                self.redirect_target = false;
                self.assignment = false;
                return .{ .kind = .operator, .start = start, .end = self.index };
            }
            if (d == '{') {
                var i = start + 2;
                while (i < s.len and s[i] != '}' and s[i] != '\n') i += 1;
                if (i < s.len and s[i] == '}') i += 1;
                return self.variable(start, i);
            }
            if (isNameStart(d)) {
                var i = start + 2;
                while (i < s.len and isNameChar(s[i])) i += 1;
                return self.variable(start, i);
            }
            if (isSpecialParameter(d)) return self.variable(start, start + 2);
        }
        self.index = start + 1;
        self.settleWord();
        return .{ .kind = .text, .start = start, .end = self.index };
    }

    fn variable(self: *Lexer, start: usize, end: usize) Token {
        self.index = end;
        self.settleWord();
        return .{ .kind = .variable, .start = start, .end = end };
    }

    fn settleWord(self: *Lexer) void {
        if (self.assignment) return;
        if (self.redirect_target) {
            self.redirect_target = false;
            return;
        }
        self.expect_command = false;
    }

    fn redirection(self: *Lexer, start: usize) ?usize {
        const s = self.source;
        var i = start;
        if (std.ascii.isDigit(s[i])) {
            if (!self.atBoundary(start)) return null;
            while (i < s.len and std.ascii.isDigit(s[i])) i += 1;
            if (i >= s.len or (s[i] != '>' and s[i] != '<')) return null;
        }
        switch (s[i]) {
            '&' => {
                if (i + 1 >= s.len or s[i + 1] != '>') return null;
                i += 2;
                if (i < s.len and s[i] == '>') i += 1;
                return i;
            },
            '>' => {
                i += 1;
                if (i < s.len and (s[i] == '>' or s[i] == '|')) return i + 1;
                if (i < s.len and s[i] == '&') return duplicationEnd(s, i + 1);
                return i;
            },
            '<' => {
                i += 1;
                if (i < s.len and s[i] == '<') {
                    i += 1;
                    if (i < s.len and s[i] == '<') return i + 1;
                    const strip = i < s.len and s[i] == '-';
                    if (strip) i += 1;
                    self.pending_heredoc = heredocDelimiter(s, i, strip);
                    return i;
                }
                if (i < s.len and s[i] == '&') return duplicationEnd(s, i + 1);
                if (i < s.len and s[i] == '>') return i + 1;
                return i;
            },
            else => return null,
        }
    }

    fn word(self: *Lexer, start: usize) Token {
        const s = self.source;
        var i = start;
        while (i < s.len) {
            const d = s[i];
            if (d == '\\') {
                if (i + 1 < s.len and s[i + 1] == '\n') break;
                i = @min(s.len, i + 2);
                continue;
            }
            if (isWordBreak(d)) break;
            i += 1;
        }
        if (i == start) i = start + 1;
        self.index = i;
        return .{ .kind = self.classify(s[start..i]), .start = start, .end = i };
    }

    fn classify(self: *Lexer, text: []const u8) Kind {
        if (self.assignment) return .text;
        if (self.redirect_target) {
            self.redirect_target = false;
            return .text;
        }
        if (self.expect_in and std.mem.eql(u8, text, "in")) {
            self.expect_in = false;
            return .keyword;
        }
        if (!self.expect_command) return if (text.len > 1 and text[0] == '-') .flag else .text;
        if (isAssignment(text)) {
            self.assignment = true;
            return .variable;
        }
        if (isOneOf(text, &opening_keywords)) return .keyword;
        if (std.mem.eql(u8, text, "{") or std.mem.eql(u8, text, "!")) return .operator;
        self.expect_command = false;
        if (isOneOf(text, &closing_keywords)) return .keyword;
        if (std.mem.eql(u8, text, "}")) return .operator;
        if (isOneOf(text, &binding_keywords)) {
            self.expect_in = !std.mem.eql(u8, text, "function");
            return .keyword;
        }
        return .command;
    }

    fn heredocLine(self: *Lexer, doc: Heredoc) Token {
        const start = self.index;
        const end = std.mem.indexOfScalarPos(u8, self.source, start, '\n') orelse self.source.len;
        self.index = end;
        const line = std.mem.trimEnd(u8, self.source[start..end], "\r");
        const candidate = if (doc.strip_tabs) std.mem.trimStart(u8, line, "\t") else line;
        if (std.mem.eql(u8, candidate, doc.delimiter)) {
            self.heredoc = null;
            return .{ .kind = .operator, .start = start, .end = end };
        }
        return .{ .kind = .string, .start = start, .end = end };
    }
};

fn operatorLength(rest: []const u8) ?usize {
    const pairs = [_][]const u8{ "&&", "||", "|&", ";;", ";&" };
    for (pairs) |pair| {
        if (std.mem.startsWith(u8, rest, pair)) return pair.len;
    }
    return switch (rest[0]) {
        '|', ';', '&', '(', ')' => 1,
        else => null,
    };
}

fn isDuplication(operator: []const u8) bool {
    if (std.mem.indexOfScalar(u8, operator, '&') == null) return false;
    const last = operator[operator.len - 1];
    return std.ascii.isDigit(last) or last == '-';
}

fn duplicationEnd(s: []const u8, from: usize) usize {
    var i = from;
    while (i < s.len and std.ascii.isDigit(s[i])) i += 1;
    if (i == from and i < s.len and s[i] == '-') i += 1;
    return i;
}

fn heredocDelimiter(s: []const u8, from: usize, strip_tabs: bool) ?Heredoc {
    var i = from;
    while (i < s.len and (s[i] == ' ' or s[i] == '\t')) i += 1;
    if (i >= s.len) return null;
    if (s[i] == '\'' or s[i] == '"') {
        const close = std.mem.indexOfScalarPos(u8, s, i + 1, s[i]) orelse return null;
        if (close == i + 1) return null;
        return .{ .delimiter = s[i + 1 .. close], .strip_tabs = strip_tabs };
    }
    if (s[i] == '\\') i += 1;
    const begin = i;
    while (i < s.len and !isWordBreak(s[i])) i += 1;
    if (i == begin) return null;
    return .{ .delimiter = s[begin..i], .strip_tabs = strip_tabs };
}

fn isWordBreak(c: u8) bool {
    return switch (c) {
        ' ', '\t', '\r', '\n', '|', '&', ';', '(', ')', '<', '>', '\'', '"', '`', '$' => true,
        else => false,
    };
}

fn isNameStart(c: u8) bool {
    return std.ascii.isAlphabetic(c) or c == '_';
}

fn isNameChar(c: u8) bool {
    return std.ascii.isAlphanumeric(c) or c == '_';
}

fn isSpecialParameter(c: u8) bool {
    return switch (c) {
        '?', '#', '@', '*', '$', '!', '-', '0'...'9' => true,
        else => false,
    };
}

fn isAssignment(text: []const u8) bool {
    if (text.len < 2 or !isNameStart(text[0])) return false;
    var i: usize = 1;
    while (i < text.len and isNameChar(text[i])) i += 1;
    if (i < text.len and text[i] == '+') i += 1;
    return i < text.len and text[i] == '=';
}

fn isOneOf(text: []const u8, words: []const []const u8) bool {
    for (words) |candidate| {
        if (std.mem.eql(u8, text, candidate)) return true;
    }
    return false;
}

pub const prompt = "$ ";
const continuation = "  ";
const tab_stop: usize = 4;

pub fn render(allocator: std.mem.Allocator, command: []const u8, width: usize, max_rows: usize) ![]u8 {
    const source = std.mem.trim(u8, command, " \t\r\n");
    if (source.len == 0) return allocator.dupe(u8, "");
    var rows = Rows{
        .allocator = allocator,
        .current = .init(allocator),
        .content_width = @max(width -| prompt.len, 8),
        .max_rows = @max(max_rows, 2),
    };
    defer rows.deinit();
    var lexer = Lexer.init(source);
    while (lexer.next()) |token| {
        if (rows.full) break;
        if (token.kind == .newline) {
            try rows.endLine();
            continue;
        }
        try rows.write(token.kind, source[token.start..token.end]);
    }
    return rows.finish(std.mem.count(u8, source, "\n") + 1);
}

const Rows = struct {
    allocator: std.mem.Allocator,
    current: std.Io.Writer.Allocating,
    content_width: usize,
    max_rows: usize,
    done: std.ArrayList([]u8) = .empty,
    lines: std.ArrayList(usize) = .empty,
    open: bool = false,
    styled: bool = false,
    col: usize = 0,
    line: usize = 0,
    full: bool = false,

    fn deinit(self: *Rows) void {
        for (self.done.items) |row| self.allocator.free(row);
        self.done.deinit(self.allocator);
        self.lines.deinit(self.allocator);
        self.current.deinit();
    }

    fn begin(self: *Rows) !bool {
        if (self.open) return true;
        if (self.done.items.len >= self.max_rows) {
            self.full = true;
            return false;
        }
        const writer = &self.current.writer;
        try tui_theme.palette.dim.writeFg(writer);
        try writer.writeAll(if (self.done.items.len == 0) prompt else continuation);
        try writer.writeAll(zz.ansi.reset);
        self.open = true;
        self.col = 0;
        return true;
    }

    fn end(self: *Rows) !void {
        try self.closeStyle();
        if (!self.open) return;
        try self.done.ensureUnusedCapacity(self.allocator, 1);
        try self.lines.ensureUnusedCapacity(self.allocator, 1);
        self.done.appendAssumeCapacity(try self.current.toOwnedSlice());
        self.lines.appendAssumeCapacity(self.line);
        self.open = false;
    }

    fn endLine(self: *Rows) !void {
        try self.closeStyle();
        if (!try self.begin()) return;
        try self.end();
        self.line += 1;
    }

    fn closeStyle(self: *Rows) !void {
        if (!self.styled) return;
        try self.current.writer.writeAll(zz.ansi.reset);
        self.styled = false;
    }

    fn write(self: *Rows, kind: Kind, text: []const u8) !void {
        var i: usize = 0;
        while (i < text.len and !self.full) {
            const c = text[i];
            switch (c) {
                '\n' => {
                    try self.endLine();
                    i += 1;
                    continue;
                },
                '\r' => {
                    i += 1;
                    continue;
                },
                '\t' => {
                    const spaces = tab_stop - (self.col % tab_stop);
                    try self.put(kind, "    "[0..spaces], spaces);
                    i += 1;
                    continue;
                },
                else => {},
            }
            const len = std.unicode.utf8ByteSequenceLength(c) catch 1;
            const end_index = @min(text.len, i + len);
            const codepoint: u21 = std.unicode.utf8Decode(text[i..end_index]) catch 0xFFFD;
            if (isControl(codepoint)) {
                try self.put(kind, "?", 1);
            } else {
                try self.put(kind, text[i..end_index], zz.measure.charWidth(codepoint));
            }
            i = end_index;
        }
        try self.closeStyle();
    }

    fn put(self: *Rows, kind: Kind, bytes: []const u8, cells: usize) !void {
        if (self.open and self.col > 0 and self.col + cells > self.content_width) try self.end();
        if (!try self.begin()) return;
        const writer = &self.current.writer;
        if (!self.styled) {
            try writeStyle(writer, kind);
            self.styled = true;
        }
        try writer.writeAll(bytes);
        self.col += cells;
    }

    fn finish(self: *Rows, total_lines: usize) ![]u8 {
        try self.end();
        var shown = self.done.items.len;
        var hidden: usize = 0;
        if (self.full and shown > 0) {
            shown -= 1;
            hidden = total_lines -| self.lines.items[shown];
        }
        var out: std.Io.Writer.Allocating = .init(self.allocator);
        errdefer out.deinit();
        const writer = &out.writer;
        for (self.done.items[0..shown], 0..) |row, i| {
            if (i > 0) try writer.writeByte('\n');
            try writer.writeAll(row);
        }
        if (hidden > 0) {
            if (shown > 0) try writer.writeByte('\n');
            try tui_theme.palette.dim.writeFg(writer);
            try writer.print("{s}… +{d} more line{s}", .{ continuation, hidden, if (hidden == 1) "" else "s" });
            try writer.writeAll(zz.ansi.reset);
        }
        return out.toOwnedSlice();
    }
};

fn isControl(codepoint: u21) bool {
    return codepoint < 0x20 or codepoint == 0x7f or (codepoint >= 0x80 and codepoint <= 0x9f) or codepoint == 0xFFFD;
}

fn writeStyle(writer: *std.Io.Writer, kind: Kind) !void {
    const palette = tui_theme.palette;
    switch (kind) {
        .command => {
            try palette.shell_command.writeFg(writer);
            try writer.writeAll("\x1b[1m");
        },
        .keyword => try palette.shell_keyword.writeFg(writer),
        .flag => try palette.shell_flag.writeFg(writer),
        .string => try palette.shell_string.writeFg(writer),
        .variable => try palette.shell_variable.writeFg(writer),
        .operator => try palette.shell_operator.writeFg(writer),
        .comment => {
            try palette.shell_comment.writeFg(writer);
            try writer.writeAll("\x1b[3m");
        },
        .text, .newline => try palette.shell_text.writeFg(writer),
    }
}

fn kindsOf(allocator: std.mem.Allocator, source: []const u8) ![]const u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);
    var lexer = Lexer.init(source);
    while (lexer.next()) |token| {
        const letter: u8 = switch (token.kind) {
            .text => if (std.mem.trim(u8, source[token.start..token.end], " \t\r").len == 0) continue else 't',
            .command => 'c',
            .keyword => 'k',
            .flag => 'f',
            .string => 's',
            .variable => 'v',
            .operator => 'o',
            .comment => '#',
            .newline => 'n',
        };
        try out.append(allocator, letter);
    }
    return out.toOwnedSlice(allocator);
}

fn expectKinds(source: []const u8, expected: []const u8) !void {
    const kinds = try kindsOf(std.testing.allocator, source);
    defer std.testing.allocator.free(kinds);
    try std.testing.expectEqualStrings(expected, kinds);
}

fn plain(allocator: std.mem.Allocator, text: []const u8) ![]u8 {
    var out: std.ArrayList(u8) = .empty;
    errdefer out.deinit(allocator);
    var i: usize = 0;
    while (i < text.len) {
        if (text[i] == 0x1b and i + 1 < text.len and text[i + 1] == '[') {
            i += 2;
            while (i < text.len and !(text[i] >= 0x40 and text[i] <= 0x7e)) i += 1;
            i += 1;
            continue;
        }
        try out.append(allocator, text[i]);
        i += 1;
    }
    return out.toOwnedSlice(allocator);
}

test "lexer names the command word of each pipeline stage" {
    try expectKinds("grep -n 'EXA' ~/.zshrc | sed -e \"s/=.*//\"", "cfstocfs");
    try expectKinds("cd repo && make build || echo failed", "ctoctoct");
    try expectKinds("npm test; git status --short", "ctoctf");
}

test "lexer separates variables, assignments and substitutions" {
    try expectKinds("FOO=1 BAR=$HOME/bin run ${TARGET} \"$x\"", "vvvtcvs");
    try expectKinds("echo $(date +%s) $((1+2))", "coctootoo");
    try expectKinds("echo $? $1", "cvv");
}

test "lexer marks redirections and keeps their targets as arguments" {
    try expectKinds("make 2>&1 >/dev/null", "coot");
    try expectKinds("cmd &> out.log < in.txt", "cotot");
    try expectKinds("tail -f log 2>err", "cftot");
}

test "lexer reads keywords only where a command may start" {
    try expectKinds("if test -f x; then echo yes; fi", "kcftokctok");
    try expectKinds("for f in *.zig; do zig fmt $f; done", "ktktokctvok");
    try expectKinds("echo if then", "ctt");
}

test "lexer treats a heredoc body as a string until its delimiter" {
    try expectKinds("cat <<'EOF' > out.txt\nhello $name\n  if\nEOF\nls", "cosotnsnsnonc");
    try expectKinds("python3 - <<-PY\n\tprint(1)\n\tPY\necho done", "ctotnsnonct");
}

test "lexer keeps comments and line continuations" {
    try expectKinds("ls # list files\necho a#b", "c#nct");
    try expectKinds("curl -s \\\n  https://example.com", "cftnt");
}

test "render prefixes the first row and highlights each token kind" {
    const out = try render(std.testing.allocator, "git status --short", 60, 12);
    defer std.testing.allocator.free(out);
    const text = try plain(std.testing.allocator, out);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("$ git status --short", text);
    var command_style: std.Io.Writer.Allocating = .init(std.testing.allocator);
    defer command_style.deinit();
    try writeStyle(&command_style.writer, .command);
    try std.testing.expect(std.mem.indexOf(u8, out, command_style.written()) != null);
}

test "render keeps every line of a multiline command" {
    const out = try render(std.testing.allocator, "set -e\ncd zig\nzig build\n", 60, 12);
    defer std.testing.allocator.free(out);
    const text = try plain(std.testing.allocator, out);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("$ set -e\n  cd zig\n  zig build", text);
}

test "render wraps a long line within the width" {
    const out = try render(std.testing.allocator, "echo aaaaaaaaaa bbbbbbbbbb cccccccccc", 20, 12);
    defer std.testing.allocator.free(out);
    const text = try plain(std.testing.allocator, out);
    defer std.testing.allocator.free(text);
    var rows = std.mem.splitScalar(u8, text, '\n');
    var count: usize = 0;
    while (rows.next()) |row| : (count += 1) try std.testing.expect(zz.width(row) <= 20);
    try std.testing.expect(count > 1);
    const joined = try std.mem.replaceOwned(u8, std.testing.allocator, text, "\n  ", "");
    defer std.testing.allocator.free(joined);
    try std.testing.expectEqualStrings("$ echo aaaaaaaaaa bbbbbbbbbb cccccccccc", joined);
}

test "render caps the rows and counts the hidden lines" {
    const out = try render(std.testing.allocator, "a\nb\nc\nd\ne\nf", 40, 4);
    defer std.testing.allocator.free(out);
    const text = try plain(std.testing.allocator, out);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("$ a\n  b\n  c\n  … +3 more lines", text);
}

test "render neutralises control bytes" {
    const out = try render(std.testing.allocator, "printf '\x1b]0;x\x07'", 40, 4);
    defer std.testing.allocator.free(out);
    const text = try plain(std.testing.allocator, out);
    defer std.testing.allocator.free(text);
    try std.testing.expectEqualStrings("$ printf '?]0;x?'", text);
}

test "render returns nothing for a blank command" {
    const out = try render(std.testing.allocator, " \n\t ", 40, 4);
    defer std.testing.allocator.free(out);
    try std.testing.expectEqualStrings("", out);
}
