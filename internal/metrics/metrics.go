// Package metrics is a small, dependency-free Prometheus exposition
// implementation: counters, labelled counters and gauges computed at scrape
// time. It covers what PathLedger needs without pulling in client_golang.
package metrics

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Counter is a monotonically increasing value.
type Counter struct{ v atomic.Uint64 }

// Inc adds one.
func (c *Counter) Inc() { c.v.Add(1) }

// Add adds n.
func (c *Counter) Add(n uint64) { c.v.Add(n) }

// Value returns the current count.
func (c *Counter) Value() uint64 { return c.v.Load() }

// Vec is a family of counters (and optional sums) keyed by label values.
type Vec struct {
	labels []string
	mu     sync.Mutex
	count  map[string]uint64
	sum    map[string]float64
}

// NewVec returns a vector with the given label names.
func NewVec(labels ...string) *Vec {
	return &Vec{labels: labels, count: map[string]uint64{}, sum: map[string]float64{}}
}

// Observe counts one event with the label values and adds v to its sum.
func (v *Vec) Observe(value float64, labelValues ...string) {
	k := strings.Join(labelValues, "\x00")
	v.mu.Lock()
	v.count[k]++
	v.sum[k] += value
	v.mu.Unlock()
}

// Inc counts one event.
func (v *Vec) Inc(labelValues ...string) { v.Observe(0, labelValues...) }

type metric struct {
	name, help, kind string
	write            func(w io.Writer, name string)
}

// Registry holds metrics in registration order.
type Registry struct {
	mu      sync.Mutex
	metrics []metric
}

// NewRegistry returns a registry with Go runtime metrics pre-registered.
func NewRegistry() *Registry {
	r := &Registry{}
	r.GaugeFunc("go_goroutines", "Number of goroutines.", func() float64 { return float64(runtime.NumGoroutine()) })
	r.GaugeFunc("go_memstats_heap_alloc_bytes", "Heap bytes in use.", func() float64 {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		return float64(m.HeapAlloc)
	})
	start := time.Now()
	r.GaugeFunc("process_uptime_seconds", "Seconds since the process started.", func() float64 { return time.Since(start).Seconds() })
	return r
}

func (r *Registry) add(m metric) {
	r.mu.Lock()
	r.metrics = append(r.metrics, m)
	r.mu.Unlock()
}

// Counter registers and returns a counter.
func (r *Registry) Counter(name, help string) *Counter {
	c := &Counter{}
	r.add(metric{name, help, "counter", func(w io.Writer, n string) { fmt.Fprintf(w, "%s %d\n", n, c.Value()) }})
	return c
}

// CounterVec registers a labelled counter. When withSum is true, a
// <name>_sum series (e.g. total seconds) is exposed alongside <name>_count,
// making it a Prometheus summary without quantiles.
func (r *Registry) CounterVec(name, help string, withSum bool, labels ...string) *Vec {
	v := NewVec(labels...)
	kind := "counter"
	if withSum {
		kind = "summary"
	}
	r.add(metric{name, help, kind, func(w io.Writer, n string) {
		v.mu.Lock()
		keys := make([]string, 0, len(v.count))
		for k := range v.count {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			lbl := labelString(v.labels, strings.Split(k, "\x00"))
			if withSum {
				fmt.Fprintf(w, "%s_count%s %d\n%s_sum%s %s\n", n, lbl, v.count[k], n, lbl, formatFloat(v.sum[k]))
			} else {
				fmt.Fprintf(w, "%s%s %d\n", n, lbl, v.count[k])
			}
		}
		v.mu.Unlock()
	}})
	return v
}

// GaugeFunc registers a gauge whose value is computed at scrape time.
func (r *Registry) GaugeFunc(name, help string, f func() float64) {
	r.add(metric{name, help, "gauge", func(w io.Writer, n string) { fmt.Fprintf(w, "%s %s\n", n, formatFloat(f())) }})
}

// Collector produces gauge values at scrape time, e.g. from the database.
type Collector func(ctx context.Context) (map[string]float64, error)

// GaugeSet registers gauges filled by one collector call per scrape.
// Each key returned by c must be one of names (others are ignored).
func (r *Registry) GaugeSet(names map[string]string, c Collector) {
	var mu sync.Mutex
	var last map[string]float64
	var lastAt time.Time
	get := func() map[string]float64 {
		mu.Lock()
		defer mu.Unlock()
		if time.Since(lastAt) > 5*time.Second { // one query per scrape burst
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if v, err := c(ctx); err == nil {
				last, lastAt = v, time.Now()
			}
		}
		return last
	}
	keys := make([]string, 0, len(names))
	for k := range names {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		k := k
		r.add(metric{k, names[k], "gauge", func(w io.Writer, n string) {
			v, ok := get()[k]
			if !ok {
				v = math.NaN()
			}
			fmt.Fprintf(w, "%s %s\n", n, formatFloat(v))
		}})
	}
}

// Handler serves the text exposition format.
func (r *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		r.mu.Lock()
		ms := append([]metric(nil), r.metrics...)
		r.mu.Unlock()
		for _, m := range ms {
			fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", m.name, m.help, m.name, m.kind)
			m.write(w, m.name)
		}
	})
}

func labelString(names, values []string) string {
	if len(names) == 0 {
		return ""
	}
	parts := make([]string, len(names))
	for i, n := range names {
		v := ""
		if i < len(values) {
			v = values[i]
		}
		parts[i] = n + `="` + escape(v) + `"`
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func escape(s string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(s)
}

func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "+Inf"
	case math.IsInf(f, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(f, 'g', -1, 64)
}
