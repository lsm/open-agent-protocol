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
