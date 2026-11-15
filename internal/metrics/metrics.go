// Package metrics реализует минимальный реестр метрик в текстовом формате
// Prometheus: счётчики, измерители и гистограммы с метками.
package metrics

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
)

// DefaultBuckets - границы корзин гистограммы длительности запросов, секунды.
var DefaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5}

type series struct {
	labels  string
	value   float64
	buckets []uint64
	sum     float64
	count   uint64
}

type family struct {
	name    string
	help    string
	kind    string
	keys    []string
	bounds  []float64
	mu      sync.Mutex
	entries map[string]*series
}

// Registry хранит все семейства метрик сервиса.
type Registry struct {
	mu       sync.Mutex
	families []*family
}

// NewRegistry создаёт пустой реестр.
func NewRegistry() *Registry { return &Registry{} }

func (r *Registry) add(name, help, kind string, keys []string, bounds []float64) *family {
	f := &family{name: name, help: help, kind: kind, keys: keys, bounds: bounds, entries: map[string]*series{}}
	r.mu.Lock()
	r.families = append(r.families, f)
	r.mu.Unlock()
	return f
}

func (f *family) get(values []string) *series {
	if len(values) != len(f.keys) {
		panic(fmt.Sprintf("metric %s: expected %d labels, got %d", f.name, len(f.keys), len(values)))
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = fmt.Sprintf(`%s="%s"`, f.keys[i], escape(v))
	}
	key := strings.Join(parts, ",")
	s, ok := f.entries[key]
	if !ok {
		s = &series{labels: key}
		if f.kind == "histogram" {
			s.buckets = make([]uint64, len(f.bounds))
		}
		f.entries[key] = s
	}
	return s
}

func escape(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return strings.ReplaceAll(v, `"`, `\"`)
}

// Counter - монотонно растущий счётчик.
type Counter struct{ f *family }

// NewCounter регистрирует счётчик с заданными именами меток.
func (r *Registry) NewCounter(name, help string, labels ...string) *Counter {
	return &Counter{r.add(name, help, "counter", labels, nil)}
}

// Add увеличивает счётчик на delta.
func (c *Counter) Add(delta float64, labels ...string) {
	c.f.mu.Lock()
	c.f.get(labels).value += delta
	c.f.mu.Unlock()
}

// Inc увеличивает счётчик на единицу.
func (c *Counter) Inc(labels ...string) { c.Add(1, labels...) }

// Gauge - значение, которое может расти и уменьшаться.
type Gauge struct{ f *family }

// NewGauge регистрирует измеритель.
func (r *Registry) NewGauge(name, help string, labels ...string) *Gauge {
	return &Gauge{r.add(name, help, "gauge", labels, nil)}
}

// Set задаёт значение измерителя.
func (g *Gauge) Set(v float64, labels ...string) {
	g.f.mu.Lock()
	g.f.get(labels).value = v
	g.f.mu.Unlock()
}

// Histogram - распределение наблюдений по корзинам.
type Histogram struct{ f *family }

// NewHistogram регистрирует гистограмму.
func (r *Registry) NewHistogram(name, help string, bounds []float64, labels ...string) *Histogram {
	b := append([]float64(nil), bounds...)
	sort.Float64s(b)
	return &Histogram{r.add(name, help, "histogram", labels, b)}
}

// Observe добавляет наблюдение.
func (h *Histogram) Observe(v float64, labels ...string) {
	h.f.mu.Lock()
	s := h.f.get(labels)
	for i, bound := range h.f.bounds {
		if v <= bound {
			s.buckets[i]++
		}
	}
	s.sum += v
	s.count++
	h.f.mu.Unlock()
}

// Write выводит все метрики в текстовом формате экспозиции.
func (r *Registry) Write(w io.Writer) {
	r.mu.Lock()
	families := append([]*family(nil), r.families...)
	r.mu.Unlock()
	for _, f := range families {
		f.mu.Lock()
		fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", f.name, f.help, f.name, f.kind)
		keys := make([]string, 0, len(f.entries))
		for k := range f.entries {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			s := f.entries[k]
			if f.kind != "histogram" {
				fmt.Fprintf(w, "%s%s %s\n", f.name, braces(s.labels), num(s.value))
				continue
			}
			for i, bound := range f.bounds {
				fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, braces(join(s.labels, fmt.Sprintf(`le="%s"`, num(bound)))), s.buckets[i])
			}
			fmt.Fprintf(w, "%s_bucket%s %d\n", f.name, braces(join(s.labels, `le="+Inf"`)), s.count)
			fmt.Fprintf(w, "%s_sum%s %s\n", f.name, braces(s.labels), num(s.sum))
			fmt.Fprintf(w, "%s_count%s %d\n", f.name, braces(s.labels), s.count)
		}
		f.mu.Unlock()
	}
}

// Handler возвращает HTTP-обработчик для пути /metrics.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.Write(w)
	})
}

func braces(s string) string {
	if s == "" {
		return ""
	}
	return "{" + s + "}"
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "," + b
}

func num(v float64) string {
	if math.IsInf(v, 1) {
		return "+Inf"
	}
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.6f", v), "0"), ".")
}
