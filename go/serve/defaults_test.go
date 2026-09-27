package serve

import (
	"testing"
	"time"
)

func TestTheHubSweepsForTenSecondsByDefault(t *testing.T) {
	if DefaultShutdownTimeout != 10*time.Second {
		t.Fatalf("DefaultShutdownTimeout is %s, want 10s: a host waits this long for the sweep to finish before it may exit", DefaultShutdownTimeout)
	}
	hub := New(NewRegistry(), Options{})
	if hub.shutdown != DefaultShutdownTimeout {
		t.Fatalf("a hub built with no shutdown option sweeps for %s, want the default %s", hub.shutdown, DefaultShutdownTimeout)
	}
	if override := 250 * time.Millisecond; New(NewRegistry(), Options{ShutdownTimeout: override}).shutdown != override {
		t.Fatal("an explicit shutdown window was not honoured")
	}
}
