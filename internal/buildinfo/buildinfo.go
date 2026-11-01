// Package buildinfo exposes version metadata set at link time:
//
//	go build -ldflags "-X github.com/sanjeev864/pathledger/internal/buildinfo.Version=v0.1.0"
package buildinfo

// Version is the release version; "dev" for local builds.
var Version = "dev"
