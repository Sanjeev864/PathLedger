//go:build !windows

package fsperm

import "os"

func restrictDir(dir string) error { return os.Chmod(dir, 0o700) }

func restrictFile(path string) error { return os.Chmod(path, 0o600) }

func private(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if fi.IsDir() {
		return fi.Mode().Perm()&0o077 == 0, nil
	}
	return fi.Mode().Perm()&0o077 == 0, nil
}
