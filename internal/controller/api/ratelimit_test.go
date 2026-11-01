package api

import (
	"testing"
	"time"
)

func TestLimiter(t *testing.T) {
	now := time.Unix(0, 0)
	l := newLimiter(Limit{Rate: 1, Burst: 3}, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if ok, _ := l.allow("a"); !ok {
			t.Fatalf("request %d within burst refused", i)
		}
	}
	ok, wait := l.allow("a")
	if ok || wait <= 0 || wait > time.Second {
		t.Fatalf("expected refusal with ≤1s wait, got ok=%v wait=%v", ok, wait)
	}
	if ok, _ := l.allow("b"); !ok {
		t.Fatal("keys must not share buckets")
	}
	now = now.Add(1500 * time.Millisecond)
	if ok, _ := l.allow("a"); !ok {
		t.Fatal("bucket did not refill")
	}
	now = now.Add(time.Hour)
	l.allow("c")
	if _, kept := l.buckets["a"]; kept {
		t.Fatal("idle full bucket not swept")
	}
}
