// notifydaemon is a minimal fixture daemon for the e2e shim signal/notify
// tests: it binds a TCP port, optionally sends READY=1 to $NOTIFY_SOCKET,
// then blocks until it receives SIGTERM/SIGINT, at which point it prints
// EXITING and returns (releasing the port) — standing in for a real daemon
// like sshd without needing host keys or a systemd unit.
package main

import (
	"fmt"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

func main() {
	if len(os.Args) >= 3 && os.Args[1] == "-notify-listener" {
		runNotifyListener(os.Args[2])
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: notifydaemon <port> | notifydaemon -notify-listener <socket-path>")
		os.Exit(1)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:"+os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	defer ln.Close()

	if sock := os.Getenv("NOTIFY_SOCKET"); sock != "" {
		notify(sock, "READY=1")
	}
	fmt.Println("READY")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	<-sigCh
	fmt.Println("EXITING")
}

// runNotifyListener stands in for systemd's own NOTIFY_SOCKET: reads one
// datagram and prints it, so the e2e test can verify the shim's relay
// without needing a real systemd unit.
func runNotifyListener(path string) {
	_ = os.Remove(path)
	ln, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen notify:", err)
		os.Exit(1)
	}
	defer ln.Close()
	buf := make([]byte, 4096)
	n, err := ln.Read(buf)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read notify:", err)
		os.Exit(1)
	}
	fmt.Println(string(buf[:n]))
}

// notify sends msg to a systemd-style NOTIFY_SOCKET, translating the
// leading '@' abstract-namespace convention to the actual leading NUL byte.
func notify(rawSock, msg string) {
	name := rawSock
	if strings.HasPrefix(name, "@") {
		name = "\x00" + name[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(msg))
}
