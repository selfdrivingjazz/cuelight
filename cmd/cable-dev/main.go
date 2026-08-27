// cable-dev is the development-only Cable lifecycle command. It never prints
// generated credentials; the admin token is retained under the private dev root.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/jmill/cable/internal/dev"
)

var version = "dev"

type commandIO struct{ stdout, stderr io.Writer }

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], commandIO{stdout: os.Stdout, stderr: os.Stderr}))
}

func run(ctx context.Context, args []string, streams commandIO) int {
	if len(args) == 1 && (args[0] == "-version" || args[0] == "--version") {
		fmt.Fprintln(streams.stdout, version)
		return 0
	}
	flags := flag.NewFlagSet("cable-dev", flag.ContinueOnError)
	flags.SetOutput(streams.stderr)
	root := flags.String("root", ".cable-dev", "private development root")
	migrations := flags.String("migrations", "migrations", "control-plane migrations directory")
	control := flags.String("control-binary", "cable-control", "cable-control executable")
	simulator := flags.String("simulator-binary", "cable-node-sim", "cable-node-sim executable")
	image := flags.String("postgres-image", "postgres:17-alpine", "PostgreSQL Docker image")
	jsonOutput := flags.Bool("json", true, "emit structured JSON (the default)")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 1 || *root == "" {
		fmt.Fprintln(streams.stderr, "usage: cable-dev [--root PATH] [--json] {up|status|reset|down}")
		return 2
	}
	manager, err := dev.New(dev.Config{Root: *root, MigrationsPath: *migrations, ControlBinary: *control, SimulatorBinary: *simulator, PostgresImage: *image}, dev.Options{})
	if err != nil {
		fmt.Fprintln(streams.stderr, err)
		return 1
	}
	switch flags.Arg(0) {
	case "up":
		status, err := manager.Up(ctx)
		if err != nil {
			return printError(streams.stderr, err)
		}
		return printStatus(streams.stdout, status, *jsonOutput)
	case "status":
		status, err := manager.Status(ctx)
		if err != nil {
			return printError(streams.stderr, err)
		}
		return printStatus(streams.stdout, status, true)
	case "down":
		if err := manager.Down(ctx); err != nil {
			return printError(streams.stderr, err)
		}
		return 0
	case "reset":
		if err := manager.Reset(ctx); err != nil {
			return printError(streams.stderr, err)
		}
		return 0
	default:
		fmt.Fprintf(streams.stderr, "cable-dev: unknown command %q\n", flags.Arg(0))
		return 2
	}
}

func printStatus(out io.Writer, status dev.Status, jsonOutput bool) int {
	if jsonOutput {
		if err := json.NewEncoder(out).Encode(status); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	fmt.Fprintf(out, "%s\n", status.State)
	return 0
}

func printError(out io.Writer, err error) int {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		fmt.Fprintln(out, err)
		return 130
	}
	fmt.Fprintln(out, err)
	return 1
}
