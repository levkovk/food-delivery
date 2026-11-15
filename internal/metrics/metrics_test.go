package metrics

import (
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := NewRegistry()
	c := r.NewCounter("orders_total", "Orders", "zone")
	c.Inc("north")
	c.Add(2, "north")
	g := r.NewGauge("queue", "Queue size")
	g.Set(7)
	h := r.NewHistogram("latency_seconds", "Latency", []float64{0.1, 1}, "route")
	h.Observe(0.05, "/a")
	h.Observe(0.5, "/a")
	var b strings.Builder
	r.Write(&b)
	out := b.String()
	for _, want := range []string{
		"# TYPE orders_total counter",
		`orders_total{zone="north"} 3`,
		"queue 7",
		`latency_seconds_bucket{route="/a",le="0.1"} 1`,
		`latency_seconds_bucket{route="/a",le="1"} 2`,
		`latency_seconds_bucket{route="/a",le="+Inf"} 2`,
		`latency_seconds_count{route="/a"} 2`,
		`latency_seconds_sum{route="/a"} 0.55`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestLabelEscaping(t *testing.T) {
	r := NewRegistry()
	r.NewCounter("x_total", "x", "path").Inc(`a"b\c`)
	var b strings.Builder
	r.Write(&b)
	if !strings.Contains(b.String(), `x_total{path="a\"b\\c"} 1`) {
		t.Fatalf("bad escaping:\n%s", b.String())
	}
}
