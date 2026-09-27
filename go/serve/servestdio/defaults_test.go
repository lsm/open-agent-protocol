package servestdio

import (
	"testing"
	"time"
)

func TestEachTeardownStageWaitsFiveSecondsByDefault(t *testing.T) {
	if DefaultShutdownTimeout != 5*time.Second {
		t.Fatalf("DefaultShutdownTimeout is %s, want 5s: a host waits this long for one teardown stage before it gives up on it", DefaultShutdownTimeout)
	}
	server, err := New(newTestHub(t, 0, 0), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if server.shutdown != DefaultShutdownTimeout {
		t.Fatalf("a server built with no shutdown option waits %s per stage, want the default %s", server.shutdown, DefaultShutdownTimeout)
	}
	override := 100 * time.Millisecond
	tight, err := New(newTestHub(t, 0, 0), Options{ShutdownTimeout: override})
	if err != nil {
		t.Fatal(err)
	}
	if tight.shutdown != override {
		t.Fatalf("an explicit per-stage window is %s, want %s", tight.shutdown, override)
	}
}
