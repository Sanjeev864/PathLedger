package api

import (
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limit is a token-bucket rate: Rate tokens per second, up to Burst.
type Limit struct {
	Rate  float64
	Burst float64
}

// Default limits. Agents upload every ~15 s and fetch plans every minute, so
// their limit leaves ample headroom while stopping a runaway agent.
var (
	limitAgent      = Limit{Rate: 1, Burst: 20}
	limitManagement = Limit{Rate: 10, Burst: 50}
	limitEnroll     = Limit{Rate: 0.2, Burst: 10} // per client IP
)

type bucket struct {
	tokens float64
	last   time.Time
}

// limiter keeps one bucket per key in memory. Limits are per controller
// replica; a shared store (Redis) is only worth adding once there are many.
type limiter struct {
	mu        sync.Mutex
	limit     Limit
	buckets   map[string]*bucket
	now       func() time.Time
	lastSweep time.Time
}

func newLimiter(l Limit, now func() time.Time) *limiter {
	return &limiter{limit: l, buckets: map[string]*bucket{}, now: now}
}

// allow takes one token for key, or reports how long until one is available.
func (l *limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) > 10*time.Minute {
		// Full buckets carry no state worth keeping.
		for k, b := range l.buckets {
			if now.Sub(b.last).Seconds()*l.limit.Rate >= l.limit.Burst {
				delete(l.buckets, k)
			}
		}
		l.lastSweep = now
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.limit.Burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.limit.Burst, b.tokens+now.Sub(b.last).Seconds()*l.limit.Rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	wait := time.Duration((1 - b.tokens) / l.limit.Rate * float64(time.Second))
	return false, wait
}

func (s *Server) rateLimited(w http.ResponseWriter, l *limiter, key string) bool {
	ok, wait := l.allow(key)
	if ok {
		return false
	}
	w.Header().Set("Retry-After", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	writeError(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry later")
	return true
}

// clientIP returns the caller's address. X-Forwarded-For is honoured only
// when the controller is configured to sit behind a trusted proxy, since
// otherwise any client could forge it.
func (s *Server) clientIP(r *http.Request) string {
	if s.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[len(parts)-1])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
