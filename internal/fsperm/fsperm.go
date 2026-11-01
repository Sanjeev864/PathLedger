// Package fsperm keeps secrets (agent credentials, release keys) readable
// only by their owner, on Unix with file modes and on Windows with access
// control lists, where file modes have no effect.
package fsperm

// Private reports, for tests, whether path is readable only by its owner
// (and, on Windows, by SYSTEM and Administrators).
func Private(path string) (bool, error) { return private(path) }

// RestrictDir makes dir accessible only to its owner. On Windows it also
// grants SYSTEM and Administrators, and files created inside inherit this.
func RestrictDir(dir string) error { return restrictDir(dir) }

// RestrictFile makes a file accessible only to its owner (see RestrictDir).
func RestrictFile(path string) error { return restrictFile(path) }
