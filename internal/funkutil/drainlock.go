package funkutil

import (
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

func drainLockPath() string { return filepath.Join(LogDir(), ".drain.lock") }

// HoldDrainLock acquires a shared advisory lock signaling "a tracer helper
// is mid-shutdown, flushing its log to disk". Held only for that brief
// window — not the tracer's whole lifetime, which for a long-running
// daemon must never block report/uninstall — so multiple helpers finishing
// around the same time can hold it concurrently. Call the returned release
// func once flushing is done (closing the fd also releases the flock, so a
// crash before that still cleans up).
func HoldDrainLock() (release func(), err error) {
	f, err := os.OpenFile(drainLockPath(), os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		f.Close()
		return nil, err
	}
	return func() { f.Close() }, nil
}

// WaitForDrain blocks, up to timeout, until no tracer helper currently
// holds HoldDrainLock — so a report/uninstall run immediately after a
// short-lived traced process exits sees fully-flushed log data instead of
// racing the helper's still-in-progress shutdown. Best-effort: any error
// (including timeout) is silently ignored, since this is a race-avoidance
// nicety, not a correctness requirement.
func WaitForDrain(timeout time.Duration) {
	f, err := os.OpenFile(drainLockPath(), os.O_CREATE|os.O_RDWR, 0o666)
	if err != nil {
		return
	}
	defer f.Close()

	deadline := time.Now().Add(timeout)
	for {
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err == nil {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
			return
		}
		if time.Now().After(deadline) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}
