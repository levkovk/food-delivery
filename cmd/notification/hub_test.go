package main

import (
	"testing"

	"fooddelivery/internal/events"
)

func TestHubKeepsRecentAndBroadcasts(t *testing.T) {
	h := newHub(3)
	ch, cancel := h.subscribe()
	for i := int64(1); i <= 5; i++ {
		h.publish(events.Event{OrderID: i})
	}
	if got := h.snapshot(); len(got) != 3 || got[0].OrderID != 3 {
		t.Fatalf("snapshot %+v", got)
	}
	if ev := <-ch; ev.OrderID != 1 {
		t.Fatalf("first broadcast %d", ev.OrderID)
	}
	cancel()
	if h.subscribers() != 0 {
		t.Fatal("subscriber not removed")
	}
}
