package main

import (
	"bufio"
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
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	ready := bufio.NewWriter(os.Stdout)
	_, _ = fmt.Fprintf(ready, "ready %s %d\n", listener.Addr().String(), os.Getpid())
	_ = ready.Flush()
	for {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_ = conn.Close()
	}
}
