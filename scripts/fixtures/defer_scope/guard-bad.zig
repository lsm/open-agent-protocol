fn plain(flag: bool, value: *u8) void {
    if (flag) {
        defer release(value);
    }
    consume(value);
    _ = flag;
}

fn else_if(first: bool, second: bool, value: *u8) void {
    if (first) {
        noop();
    } else if (second) {
        defer release(value);
    }
    consume(value);
    _ = first;
    _ = second;
}

fn else_branch(flag: bool, value: *u8) void {
    if (flag) {
        noop();
    } else {
        defer release(value);
    }
    consume(value);
    _ = flag;
}

fn capture(found: ?u8, value: *u8) void {
    if (found) |byte| {
        defer release(value);
    }
    consume(value);
    _ = found;
    _ = byte;
}

fn nested(flag: bool, other: bool, value: *u8) void {
    if (flag) {
        if (other) {
            defer release(value);
        }
    }
    consume(value);
    _ = flag;
    _ = other;
}

fn url_in_the_head(url: []const u8, value: *u8) void {
    if (startsWith(url, "http://")) {
        defer release(value);
    }
    consume(value);
    _ = url;
}

fn error_else(found: anyerror!void, value: *u8) void {
    if (found) |_| {
        noop();
    } else |err| {
        defer release(value);
    }
    _ = err;
}

fn one_line_with_two_statements(flag: bool, value: *u8) void {
    if (flag) {
        defer release(value); use(value);
    }
    consume(value);
    _ = flag;
}
