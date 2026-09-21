package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: goprintable <output.zig>")
		os.Exit(2)
	}
	type span struct{ lo, hi rune }
	var spans []span
	open := false
	var start rune
	for r := rune(0); r <= 0x10FFFF; r++ {
		if strconv.IsPrint(r) {
			if !open {
				start, open = r, true
			}
			continue
		}
		if open {
			spans = append(spans, span{start, r - 1})
			open = false
		}
	}
	if open {
		spans = append(spans, span{start, 0x10FFFF})
	}

	file, err := os.Create(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer file.Close()
	out := bufio.NewWriter(file)
	defer out.Flush()

	fmt.Fprintf(out, "pub const toolchain = \"%s\";\n\n", runtime.Version())
	fmt.Fprintf(out, "pub const printable = [_][2]u21{\n")
	for index, s := range spans {
		if index%4 == 0 {
			fmt.Fprint(out, "   ")
		}
		fmt.Fprintf(out, " .{ 0x%X, 0x%X },", s.lo, s.hi)
		if index%4 == 3 {
			fmt.Fprintln(out)
		}
	}
	if len(spans)%4 != 0 {
		fmt.Fprintln(out)
	}
	fmt.Fprintf(out, "};\n")
	fmt.Fprintf(os.Stderr, "%d ranges from %s\n", len(spans), runtime.Version())
}
