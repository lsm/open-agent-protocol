package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
)

func main() {
	signal.Ignore(os.Interrupt)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	defer listener.Close()
	_, _ = fmt.Fprintf(os.Stdout, "ready %s %d\n", listener.Addr(), os.Getpid())
	_ = os.Stdout.Sync()
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_ = conn.Close()
	}
}
