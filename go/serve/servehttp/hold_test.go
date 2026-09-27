package servehttp

import (
	"testing"
	"time"
)

func TestASubscriptionIsHeldForThirtySecondsByDefault(t *testing.T) {
	if DefaultSubscriptionHold != 30*time.Second {
		t.Fatalf("DefaultSubscriptionHold is %s, want 30s: a host that opens with a subscription and then attaches elsewhere waits this long before the subscription is released", DefaultSubscriptionHold)
	}
	hub, _ := newServer(t, memoryRegistry(0), Options{})
	daemon, err := New(hub, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if daemon.holdFor != DefaultSubscriptionHold {
		t.Fatalf("a server built with no hold option holds for %s, want the default %s", daemon.holdFor, DefaultSubscriptionHold)
	}
	override := time.Second
	explicit, err := New(hub, Options{SubscriptionHold: override})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.holdFor != override {
		t.Fatalf("an explicit hold window is %s, want %s", explicit.holdFor, override)
	}
}
