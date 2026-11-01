package metrics

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExposition(t *testing.T) {
	r := NewRegistry()
	c := r.Counter("pl_things_total", "Things.")
	c.Add(3)
	v := r.CounterVec("pl_http_request_duration_seconds", "Latency.", true, "route", "code")
	v.Observe(0.5, "GET /x", "2xx")
	v.Observe(0.25, "GET /x", "2xx")
	v.Observe(1, `GET /"q"`, "5xx")
	r.GaugeSet(map[string]string{"pl_agents": "Agents.", "pl_missing": "Missing."}, func(context.Context) (map[string]float64, error) {
		return map[string]float64{"pl_agents": 7}, nil
	})
	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	out := rec.Body.String()
	for _, want := range []string{
		"# TYPE pl_things_total counter\npl_things_total 3\n",
		`pl_http_request_duration_seconds_count{route="GET /x",code="2xx"} 2`,
		`pl_http_request_duration_seconds_sum{route="GET /x",code="2xx"} 0.75`,
		`pl_http_request_duration_seconds_count{route="GET /\"q\"",code="5xx"} 1`,
		"pl_agents 7\n", "pl_missing NaN\n", "go_goroutines ",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
