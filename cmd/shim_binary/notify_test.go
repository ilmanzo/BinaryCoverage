package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifySocketAddr(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"/run/systemd/notify", "/run/systemd/notify"},
		{"@/run/systemd/notify/abc", "\x00/run/systemd/notify/abc"},
	}
	for _, tc := range cases {
		got := notifySocketAddr(tc.raw)
		if got.Name != tc.want {
			t.Errorf("notifySocketAddr(%q).Name = %q, want %q", tc.raw, got.Name, tc.want)
		}
		if got.Net != "unixgram" {
			t.Errorf("notifySocketAddr(%q).Net = %q, want unixgram", tc.raw, got.Net)
		}
	}
}

func TestStartNotifyRelay_NoOpWithoutEnv(t *testing.T) {
	t.Setenv(notifySocketEnvVar, "")

	proxyPath, cleanup, err := startNotifyRelay()
	if err != nil || proxyPath != "" {
		t.Fatalf("expected no-op, got proxyPath=%q err=%v", proxyPath, err)
	}
	cleanup() // must be safe to call
}

func TestStartNotifyRelay_ForwardsDatagram(t *testing.T) {
	realPath := filepath.Join(t.TempDir(), "systemd-notify.sock")
	real, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: realPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("listen fake systemd socket: %v", err)
	}
	defer real.Close()

	t.Setenv(notifySocketEnvVar, realPath)

	proxyPath, cleanup, err := startNotifyRelay()
	if err != nil {
		t.Fatalf("startNotifyRelay: %v", err)
	}
	if proxyPath == "" {
		t.Fatal("startNotifyRelay returned empty proxy path with NOTIFY_SOCKET set")
	}
	if _, err := os.Stat(proxyPath); err != nil {
		t.Fatalf("proxy socket file missing: %v", err)
	}

	client, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: proxyPath, Net: "unixgram"})
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer client.Close()
	if _, err := client.Write([]byte("READY=1")); err != nil {
		t.Fatalf("write to proxy: %v", err)
	}

	_ = real.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 64)
	n, err := real.Read(buf)
	if err != nil {
		t.Fatalf("read from fake systemd socket: %v", err)
	}
	if got := string(buf[:n]); got != "READY=1" {
		t.Errorf("relayed datagram = %q, want READY=1", got)
	}

	cleanup()
	if _, err := os.Stat(proxyPath); !os.IsNotExist(err) {
		t.Errorf("proxy socket file not removed after cleanup: err=%v", err)
	}
}
