package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sanjeev864/pathledger/pkg/wire"
)

const (
	segmentPrefix = "seg-"
	segmentSuffix = ".jsonl"
	// segmentMaxResults keeps every sealed segment uploadable as one batch.
	segmentMaxResults = wire.MaxResultsPerBatch
)

// Spool is a crash-safe, size-bounded on-disk queue of results. Results are
// appended to an active segment file as JSON lines; the uploader reads the
// oldest sealed segment and deletes it only after the controller accepts it.
// If the uplink is down long enough to hit the size cap, the oldest data is
// dropped first so the newest measurements always survive.
type Spool struct {
	dir      string
	maxBytes int64
	log      *slog.Logger

	mu          sync.Mutex
	active      *os.File
	activeName  string
	activeCount int
	lastSeq     int64
	dropped     int64
}

// Segment is one batch of spooled results.
type Segment struct {
	Name    string
	Results []wire.Result
}

// OpenSpool opens (creating if needed) a spool directory. Segments left by a
// previous run are treated as sealed and will be uploaded first.
func OpenSpool(dir string, maxBytes int64, log *slog.Logger) (*Spool, error) {
	if maxBytes <= 0 {
		return nil, errors.New("spool size limit must be positive")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating spool directory: %w", err)
	}
	return &Spool{dir: dir, maxBytes: maxBytes, log: log}, nil
}

// Append durably writes results to the active segment.
func (s *Spool) Append(results []wire.Result) error {
	if len(results) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range results {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("encoding result: %w", err)
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active == nil || s.activeCount+len(results) > segmentMaxResults {
		if err := s.sealLocked(); err != nil {
			return err
		}
		if err := s.openActiveLocked(); err != nil {
			return err
		}
	}
	if _, err := s.active.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("writing spool: %w", err)
	}
	if err := s.active.Sync(); err != nil {
		return fmt.Errorf("syncing spool: %w", err)
	}
	s.activeCount += len(results)
	return s.enforceCapLocked()
}

// Next returns the oldest sealed segment. When only the active segment holds
// data, it is sealed first so recent results are not held back. ok is false
// when the spool is empty.
func (s *Spool) Next() (seg Segment, ok bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	names, err := s.sealedLocked()
	if err != nil {
		return Segment{}, false, err
	}
	if len(names) == 0 {
		if s.active == nil || s.activeCount == 0 {
			return Segment{}, false, nil
		}
		if err := s.sealLocked(); err != nil {
			return Segment{}, false, err
		}
		if names, err = s.sealedLocked(); err != nil || len(names) == 0 {
			return Segment{}, false, err
		}
	}
	name := names[0]
	results, err := s.readSegment(name)
	if err != nil {
		return Segment{}, false, err
	}
	return Segment{Name: name, Results: results}, true, nil
}

// Ack deletes a segment after its results were delivered.
func (s *Spool) Ack(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == s.activeName {
		return errors.New("cannot acknowledge the active segment")
	}
	if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing segment: %w", err)
	}
	return nil
}

// Dropped reports how many results were discarded because of the size cap.
func (s *Spool) Dropped() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dropped
}

// Close seals the active segment.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sealLocked()
}

func (s *Spool) openActiveLocked() error {
	seq := time.Now().UnixNano()
	if seq <= s.lastSeq {
		seq = s.lastSeq + 1
	}
	s.lastSeq = seq
	name := fmt.Sprintf("%s%020d%s", segmentPrefix, seq, segmentSuffix)
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening spool segment: %w", err)
	}
	s.active, s.activeName, s.activeCount = f, name, 0
	return nil
}

func (s *Spool) sealLocked() error {
	if s.active == nil {
		return nil
	}
	err := s.active.Close()
	if s.activeCount == 0 {
		_ = os.Remove(filepath.Join(s.dir, s.activeName))
	}
	s.active, s.activeName, s.activeCount = nil, "", 0
	return err
}

// sealedLocked lists sealed segments, oldest first.
func (s *Spool) sealedLocked() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("listing spool: %w", err)
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, segmentPrefix) && strings.HasSuffix(n, segmentSuffix) && n != s.activeName {
			names = append(names, n)
		}
	}
	sort.Strings(names) // zero-padded sequence numbers sort chronologically
	return names, nil
}

// readSegment parses a segment, skipping any line torn by a crash mid-write.
func (s *Spool) readSegment(name string) ([]wire.Result, error) {
	f, err := os.Open(filepath.Join(s.dir, name))
	if err != nil {
		return nil, fmt.Errorf("opening segment: %w", err)
	}
	defer f.Close()
	var out []wire.Result
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r wire.Result
		if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
			s.log.Warn("skipping corrupt spool line", "segment", name, "error", err)
			continue
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading segment: %w", err)
	}
	return out, nil
}

// enforceCapLocked deletes the oldest sealed segments while the spool
// exceeds its size limit.
func (s *Spool) enforceCapLocked() error {
	names, err := s.sealedLocked()
	if err != nil {
		return err
	}
	sizes := make(map[string]int64, len(names))
	var total int64
	for _, n := range append(names, s.activeName) {
		if n == "" {
			continue
		}
		fi, err := os.Stat(filepath.Join(s.dir, n))
		if err != nil {
			continue
		}
		sizes[n] = fi.Size()
		total += fi.Size()
	}
	for _, n := range names {
		if total <= s.maxBytes {
			break
		}
		lost, _ := s.countLines(n)
		if err := os.Remove(filepath.Join(s.dir, n)); err != nil {
			return fmt.Errorf("dropping segment: %w", err)
		}
		total -= sizes[n]
		s.dropped += int64(lost)
		s.log.Warn("spool full, dropped oldest results", "segment", n, "results", lost, "limit_bytes", s.maxBytes)
	}
	return nil
}

func (s *Spool) countLines(name string) (int, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, name))
	if err != nil {
		return 0, err
	}
	return bytes.Count(b, []byte{'\n'}), nil
}
