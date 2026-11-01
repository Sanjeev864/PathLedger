package agent

import (
	"github.com/sanjeev864/pathledger/internal/fsperm"

	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func result(id string) wire.Result {
	return wire.Result{TestID: id, Time: time.Now().UTC(), Kind: wire.KindTCP, Target: "x:1", Success: true}
}

func TestSpoolAppendNextAck(t *testing.T) {
	s, err := OpenSpool(t.TempDir(), 1<<20, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Next(); ok {
		t.Fatal("empty spool returned a segment")
	}
	if err := s.Append([]wire.Result{result("a"), result("b")}); err != nil {
		t.Fatal(err)
	}
	seg, ok, err := s.Next()
	if err != nil || !ok || len(seg.Results) != 2 || seg.Results[0].TestID != "a" {
		t.Fatalf("unexpected segment %+v ok=%v err=%v", seg, ok, err)
	}
	// Results appended after sealing go to a new segment and are not lost.
	if err := s.Append([]wire.Result{result("c")}); err != nil {
		t.Fatal(err)
	}
	if err := s.Ack(seg.Name); err != nil {
		t.Fatal(err)
	}
	seg, ok, _ = s.Next()
	if !ok || len(seg.Results) != 1 || seg.Results[0].TestID != "c" {
		t.Fatalf("expected the later result, got %+v", seg)
	}
	_ = s.Ack(seg.Name)
	if _, ok, _ := s.Next(); ok {
		t.Fatal("spool should be empty")
	}
}

func TestSpoolSurvivesRestartAndTornWrite(t *testing.T) {
	dir := t.TempDir()
	s, _ := OpenSpool(dir, 1<<20, quietLog())
	_ = s.Append([]wire.Result{result("a")})
	_ = s.Close()

	// Simulate a crash in the middle of writing a line.
	entries, _ := os.ReadDir(dir)
	f, _ := os.OpenFile(filepath.Join(dir, entries[0].Name()), os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"test_id":"b","ti`)
	f.Close()

	s2, _ := OpenSpool(dir, 1<<20, quietLog())
	seg, ok, err := s2.Next()
	if err != nil || !ok || len(seg.Results) != 1 || seg.Results[0].TestID != "a" {
		t.Fatalf("expected intact result only, got %+v ok=%v err=%v", seg, ok, err)
	}
}

func TestSpoolSegmentsStayUploadable(t *testing.T) {
	s, _ := OpenSpool(t.TempDir(), 64<<20, quietLog())
	batch := make([]wire.Result, 200)
	for i := range batch {
		batch[i] = result("x")
	}
	for i := 0; i < 4; i++ {
		if err := s.Append(batch); err != nil {
			t.Fatal(err)
		}
	}
	total := 0
	for {
		seg, ok, err := s.Next()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		if len(seg.Results) > wire.MaxResultsPerBatch {
			t.Fatalf("segment of %d results exceeds batch limit", len(seg.Results))
		}
		total += len(seg.Results)
		_ = s.Ack(seg.Name)
	}
	if total != 800 {
		t.Fatalf("expected 800 results, got %d", total)
	}
}

func TestSpoolCapDropsOldest(t *testing.T) {
	s, _ := OpenSpool(t.TempDir(), 4<<10, quietLog())
	for i := 0; i < 60; i++ {
		if err := s.Append([]wire.Result{result("x")}); err != nil {
			t.Fatal(err)
		}
		if i%5 == 4 {
			// Seal periodically, as the uploader would while offline.
			s.mu.Lock()
			_ = s.sealLocked()
			s.mu.Unlock()
		}
	}
	if s.Dropped() == 0 {
		t.Fatal("expected results to be dropped once the cap was exceeded")
	}
	var size int64
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		fi, _ := e.Info()
		size += fi.Size()
	}
	if size > 4<<10+1024 {
		t.Fatalf("spool grew to %d bytes despite 4 KiB cap", size)
	}
}

func TestStateRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "agent.json")
	if _, err := LoadState(path); err != ErrNotEnrolled {
		t.Fatalf("expected ErrNotEnrolled, got %v", err)
	}
	want := State{AgentID: "a", AgentToken: "pla_secret", ControllerURL: "https://c"}
	if err := SaveState(path, want); err != nil {
		t.Fatal(err)
	}
	if ok, err := fsperm.Private(path); err != nil || !ok {
		t.Fatalf("state file is readable by others: %v", err)
	}
	got, err := LoadState(path)
	if err != nil || got.AgentToken != want.AgentToken {
		t.Fatalf("round trip failed: %+v %v", got, err)
	}
}
