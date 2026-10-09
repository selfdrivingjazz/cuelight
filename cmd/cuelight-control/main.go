package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/jmilldotdev/cuelight/internal/buildinfo"
	"github.com/jmilldotdev/cuelight/internal/control/app"
	"github.com/jmilldotdev/cuelight/internal/control/composition"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runServe(ctx, nil, lookup, stdout, stderr)
	}
	if args[0] == "-version" || args[0] == "--version" {
		if len(args) != 1 {
			fmt.Fprintln(stderr, "cuelight-control: version does not accept arguments")
			return 2
		}
		fmt.Fprintln(stdout, buildinfo.String("cuelight-control"))
		return 0
	}
	switch {
	case args[0] == "serve":
		return runServe(ctx, args[1:], lookup, stdout, stderr)
	case args[0] == "init":
		return runInit(args[1:], stdout, stderr)
	case len(args) >= 3 && args[0] == "admin" && args[1] == "token" && args[2] == "create":
		return runAdminToken(ctx, args[3:], lookup, stdout, stderr)
	case args[0] == "updates":
		return runUpdates(ctx, args[1:], lookup, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "cuelight-control: usage: cuelight-control serve | init --dir DIR | admin token create | updates import DIR\n")
		return 2
	}
}

func runServe(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintf(stderr, "cuelight-control serve: unexpected arguments: %v\n", args)
		return 2
	}
	config, err := app.LoadConfig(lookup)
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-control: %v\n", err)
		return 2
	}
	if err := app.Run(ctx, config, app.Options{Handler: composition.HandlerFactory, ConfigureTLS: composition.ConfigureTLS(config)}); err != nil {
		fmt.Fprintf(stderr, "cuelight-control: %v\n", err)
		return 1
	}
	return 0
}

func runAdminToken(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("admin token create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	actorID := flags.String("actor-id", "", "existing actor UUID (default: generate one)")
	actorName := flags.String("actor-name", "cuelight-control administrator", "actor display name")
	scopeList := flags.String("scopes", "security_admin", "comma-separated token scopes")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "cuelight-control admin token create: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	config, err := app.LoadConfig(adminLookup(lookup))
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-control: %v\n", err)
		return 2
	}
	scopes := make([]string, 0)
	for _, value := range strings.Split(*scopeList, ",") {
		if value = strings.TrimSpace(value); value != "" {
			scopes = append(scopes, value)
		}
	}
	token, err := composition.BootstrapAdminToken(ctx, config, *actorID, *actorName, scopes)
	if err != nil {
		fmt.Fprintf(stderr, "cuelight-control: %v\n", err)
		return 1
	}
	// The secret is deliberately the only credential field emitted by this mode.
	if err := json.NewEncoder(stdout).Encode(struct {
		Secret string `json:"secret"`
	}{Secret: token.Secret}); err != nil {
		fmt.Fprintf(stderr, "cuelight-control: write token: %v\n", err)
		return 1
	}
	return 0
}

func adminLookup(lookup func(string) (string, bool)) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if value, ok := lookup(name); ok {
			return value, ok
		}
		switch name {
		case "CUELIGHT_CONTROL_TLS_CERT_PATH":
			return "admin-mode-unused-cert", true
		case "CUELIGHT_CONTROL_TLS_KEY_PATH":
			return "admin-mode-unused-key", true
		case "CUELIGHT_CONTROL_PUBLISHER_KEY_PATH":
			return "admin-mode-unused-publisher", true
		case "CUELIGHT_CONTROL_CAS_PATH":
			return "admin-mode-unused-cas", true
		default:
			return "", false
		}
	}
}
