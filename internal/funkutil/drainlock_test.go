package funkutil

import (
	"testing"
	"time"
)

func TestDrainLock_WaitsForRelease(t *testing.T) {
	t.Setenv("LOG_DIR", t.TempDir())

	release, err := HoldDrainLock()
	if err != nil {
		t.Fatalf("HoldDrainLock: %v", err)
	}

	released := make(chan struct{})
	go func() {
		time.Sleep(50 * time.Millisecond)
		release()
		close(released)
	}()

	start := time.Now()
	WaitForDrain(2 * time.Second)
	elapsed := time.Since(start)

	select {
	case <-released:
	default:
		t.Error("WaitForDrain returned before the drain lock was released")
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("WaitForDrain returned in %v, too fast to have actually waited for the lock", elapsed)
	}
}

func TestWaitForDrain_NoLockHeldReturnsQuickly(t *testing.T) {
	t.Setenv("LOG_DIR", t.TempDir())

	start := time.Now()
	WaitForDrain(2 * time.Second)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("WaitForDrain took %v with no lock held, want near-instant", elapsed)
	}
}
