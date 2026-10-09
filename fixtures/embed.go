// Package fixtures contains the versioned, redistribution-safe Cuelight test
// fixtures. Consumers should use internal/fixtures.Load for validated access;
// this package only exposes the embedded file system to that loader.
package fixtures

import "embed"

// FS contains every checked-in fixture and its manifest.
//
//go:embed v1
var FS embed.FS
