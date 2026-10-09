// Package buildinfo carries the release identity stamped into every Cuelight
// binary at link time:
//
//	go build -ldflags "-X github.com/jmilldotdev/cuelight/internal/buildinfo.Version=0.2.0 -X github.com/jmilldotdev/cuelight/internal/buildinfo.Commit=abc1234"
package buildinfo

import "fmt"

// Version is the semantic version of this build; unstamped builds report 0.0.0-dev.
var Version = "0.0.0-dev"

// Commit is the source revision of this build; empty when unstamped.
var Commit = ""

// String renders "<program> <version> (<commit>)" for --version output.
func String(program string) string {
	if Commit == "" {
		return fmt.Sprintf("%s %s", program, Version)
	}
	return fmt.Sprintf("%s %s (%s)", program, Version, Commit)
}
