// Package migrations embeds the control-plane PostgreSQL migrations so that
// cuelight-control applies them without any files on disk.
package migrations

import "embed"

// FS holds every NNNN_name.sql migration at its root.
//
//go:embed *.sql
var FS embed.FS
