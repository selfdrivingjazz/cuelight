package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jmill/cable/internal/control/app"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cable-control", flag.ContinueOnError)
	flags.SetOutput(stderr)
	showVersion := flags.Bool("version", false, "print version and exit")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "cable-control: unexpected arguments: %v\n", flags.Args())
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "cable-control %s\n", version)
		return 0
	}
	config, err := app.LoadConfig(lookup)
	if err != nil {
		fmt.Fprintf(stderr, "cable-control: %v\n", err)
		return 2
	}
	if err := app.Run(ctx, config, app.Options{Version: version}); err != nil {
		fmt.Fprintf(stderr, "cable-control: %v\n", err)
		return 1
	}
	return 0
}
