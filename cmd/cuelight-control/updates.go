package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/selfdrivingjazz/cuelight/internal/control/app"
	"github.com/selfdrivingjazz/cuelight/internal/control/composition"
)

// runUpdates implements `cuelight-control updates import <dir>`: it verifies
// the signed cuelight-release.json in dir against the compiled-in vendor keys
// and copies every listed artifact found in dir into control CAS. It needs the
// server's CUELIGHT_CONTROL_DATABASE_URL and CUELIGHT_CONTROL_CAS_PATH.
func runUpdates(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "import" {
		fmt.Fprintln(stderr, "cuelight-control: usage: cuelight-control updates import DIR")
		return 2
	}
	config, err := app.LoadConfig(updatesLookup(lookup))
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-control: %v\n", err)
		return 2
	}
	result, err := composition.ImportSoftwareRelease(ctx, config, args[1])
	if result.Version != "" {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(result)
	}
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-control: import %s: %v\n", args[1], err)
		return 1
	}
	return 0
}

// updatesLookup fills the TLS and publisher settings an import never uses.
func updatesLookup(lookup func(string) (string, bool)) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if value, ok := lookup(name); ok {
			return value, ok
		}
		switch name {
		case "CUELIGHT_CONTROL_TLS_CERT_PATH":
			return "updates-mode-unused-cert", true
		case "CUELIGHT_CONTROL_TLS_KEY_PATH":
			return "updates-mode-unused-key", true
		case "CUELIGHT_CONTROL_PUBLISHER_KEY_PATH":
			return "updates-mode-unused-publisher", true
		default:
			return "", false
		}
	}
}
