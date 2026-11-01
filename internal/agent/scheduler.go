package agent

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"sync"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

// RunFunc executes one test.
type RunFunc func(ctx context.Context, t wire.Test) wire.Result

// Scheduler runs each test on its own interval. Start times are jittered so
// many agents (and many tests on one agent) do not fire in lockstep, and a
// test never overlaps with its own previous run.
type Scheduler struct {
	run  RunFunc
	sink func(wire.Result)
	sem  chan struct{}
	log  *slog.Logger

	mu    sync.Mutex
	tasks map[string]*task
	wg    sync.WaitGroup
}

type task struct {
	test   wire.Test
	cancel context.CancelFunc
}

// NewScheduler creates a scheduler that runs at most maxConcurrent probes at
// once and passes every result to sink.
func NewScheduler(run RunFunc, sink func(wire.Result), maxConcurrent int, log *slog.Logger) *Scheduler {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	return &Scheduler{run: run, sink: sink, sem: make(chan struct{}, maxConcurrent), log: log, tasks: map[string]*task{}}
}

// Apply reconciles running tasks with tests: new tests start, removed tests
// stop, and changed tests restart. Invalid tests are skipped.
func (s *Scheduler) Apply(ctx context.Context, tests []wire.Test) {
	s.mu.Lock()
	defer s.mu.Unlock()
	want := make(map[string]wire.Test, len(tests))
	for _, t := range tests {
		if err := t.Validate(); err != nil || t.ID == "" {
			s.log.Warn("skipping invalid test from plan", "test_id", t.ID, "error", err)
			continue
		}
		want[t.ID] = t
	}
	for id, tk := range s.tasks {
		if nt, ok := want[id]; !ok || !reflect.DeepEqual(nt, tk.test) {
			tk.cancel()
			delete(s.tasks, id)
		}
	}
	for id, t := range want {
		if _, ok := s.tasks[id]; ok {
			continue
		}
		tctx, cancel := context.WithCancel(ctx)
		s.tasks[id] = &task{test: t, cancel: cancel}
		s.wg.Add(1)
		go s.loop(tctx, t)
	}
}

// Active returns the number of scheduled tests.
func (s *Scheduler) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}

// Stop cancels every task and waits for in-flight probes to finish.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	for id, tk := range s.tasks {
		tk.cancel()
		delete(s.tasks, id)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context, t wire.Test) {
	defer s.wg.Done()
	interval := t.Interval()
	first := time.Duration(rand.Int64N(int64(interval)))
	timer := time.NewTimer(first)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		started := time.Now()
		select {
		case s.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		res := s.run(ctx, t)
		<-s.sem
		if ctx.Err() == nil {
			s.sink(res)
		}
		// Stay on the test's cadence; if a run overran its interval, skip the
		// missed slots instead of firing back-to-back.
		timer.Reset(interval - time.Since(started)%interval)
	}
}
