package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

const notifySocketEnvVar = "NOTIFY_SOCKET"

// notifySocketAddr translates systemd's NOTIFY_SOCKET convention into a
// net.UnixAddr: a leading '@' denotes the abstract namespace, represented on
// Linux by a leading NUL byte — net.Dial/Listen don't do this translation
// automatically.
func notifySocketAddr(raw string) *net.UnixAddr {
	name := raw
	if strings.HasPrefix(name, "@") {
		name = "\x00" + name[1:]
	}
	return &net.UnixAddr{Name: name, Net: "unixgram"}
}

// startNotifyRelay proxies sd_notify datagrams from the child to the real
// systemd NOTIFY_SOCKET. This is needed because systemd's default
// NotifyAccess=main only trusts datagrams from the MainPID it tracks — the
// shim parent — while the real daemon runs as the shim's child under a
// different PID (see childMain's syscall.Exec), so its own sd_notify()
// calls would otherwise be silently dropped. Returns ("", no-op cleanup,
// nil) if NOTIFY_SOCKET isn't set (not a Type=notify service).
func startNotifyRelay() (proxyPath string, cleanup func(), err error) {
	raw := os.Getenv(notifySocketEnvVar)
	if raw == "" {
		return "", func() {}, nil
	}

	proxyPath = filepath.Join(os.TempDir(), fmt.Sprintf("funkoverage-notify-%d.sock", os.Getpid()))
	_ = os.Remove(proxyPath)
	proxy, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: proxyPath, Net: "unixgram"})
	if err != nil {
		return "", nil, fmt.Errorf("listen notify proxy: %w", err)
	}

	real, err := net.DialUnix("unixgram", nil, notifySocketAddr(raw))
	if err != nil {
		proxy.Close()
		os.Remove(proxyPath)
		return "", nil, fmt.Errorf("dial real notify socket %q: %w", raw, err)
	}

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := proxy.Read(buf)
			if err != nil {
				return
			}
			_, _ = real.Write(buf[:n])
		}
	}()

	cleanup = func() {
		proxy.Close()
		real.Close()
		os.Remove(proxyPath)
	}
	return proxyPath, cleanup, nil
}
