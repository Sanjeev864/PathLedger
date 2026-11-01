package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// maxLogBytes is the size at which the log file is rotated at start-up.
const maxLogBytes = 10 << 20

// openLog opens path for appending. A file over maxLogBytes is first moved
// to path.1 (replacing any older one), so logs never grow without bound on
// machines without a log service (Windows scheduled tasks, launchd).
func openLog(path string) (io.WriteCloser, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > maxLogBytes {
		_ = os.Remove(path + ".1")
		if err := os.Rename(path, path+".1"); err != nil {
			return nil, fmt.Errorf("rotating log file: %w", err)
		}
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
}
