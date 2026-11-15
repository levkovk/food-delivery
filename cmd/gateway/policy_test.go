package main

import (
	"testing"
	"time"
)

func TestAuthorize(t *testing.T) {
	cases := []struct {
		method, path, role, upstream string
		want                         decision
	}{
		{"GET", "/api/orders", "operator", "order", allow},
		{"POST", "/api/orders", "operator", "order", allow},
		{"POST", "/api/orders/7/cancel", "operator", "order", allow},
		{"GET", "/api/restaurants/3/menu", "operator", "restaurant", allow},
		{"POST", "/api/restaurants", "operator", "restaurant", forbidden},
		{"POST", "/api/restaurants", "admin", "restaurant", allow},
		{"DELETE", "/api/menu/4", "operator", "restaurant", forbidden},
		{"GET", "/api/payments", "operator", "payment", forbidden},
		{"GET", "/api/payments", "admin", "payment", allow},
		{"PATCH", "/api/couriers/2", "operator", "courier", allow},
		{"POST", "/api/couriers", "operator", "courier", forbidden},
		{"GET", "/api/internal/quote", "admin", "", unknownRoute},
		{"GET", "/api/ordersX", "admin", "", unknownRoute},
		{"DELETE", "/api/orders/1", "admin", "", unknownRoute},
	}
	for _, c := range cases {
		up, dec := authorize(c.method, c.path, c.role)
		if dec != c.want || (dec != unknownRoute && up != c.upstream) {
			t.Errorf("%s %s as %s: got %s/%d want %s/%d", c.method, c.path, c.role, up, dec, c.upstream, c.want)
		}
	}
}

func TestUpstreamPath(t *testing.T) {
	for in, want := range map[string]string{
		"/api/orders/5":           "/orders/5",
		"/api/events/stream":      "/stream",
		"/api/events/recent":      "/recent",
		"/api/restaurants/1/menu": "/restaurants/1/menu",
	} {
		if got := upstreamPath(in); got != want {
			t.Errorf("%s -> %s, want %s", in, got, want)
		}
	}
}

func TestLimiter(t *testing.T) {
	l := newLimiter(2, 3)
	now := time.Now()
	for i := 0; i < 3; i++ {
		if !l.allow("a", now) {
			t.Fatalf("request %d within burst rejected", i)
		}
	}
	if l.allow("a", now) {
		t.Fatal("request over burst allowed")
	}
	if !l.allow("b", now) {
		t.Fatal("other client affected")
	}
	if !l.allow("a", now.Add(600*time.Millisecond)) {
		t.Fatal("tokens are not refilled")
	}
	l.cleanup(now.Add(time.Hour), time.Minute)
	if len(l.buckets) != 0 {
		t.Fatal("idle buckets not removed")
	}
}
