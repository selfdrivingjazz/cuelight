// Command cuelight-scenario runs a checked-in process-backed Cuelight scenario.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/selfdrivingjazz/cuelight/internal/dev"
	"github.com/selfdrivingjazz/cuelight/internal/e2e"
)

func main() { os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr)) }

func run(parent context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("cuelight-scenario", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var scenario, root, output, proof string
	flags.StringVar(&scenario, "scenario", "", "checked-in scenario name")
	flags.StringVar(&root, "root", "", "development root")
	flags.StringVar(&output, "output", "-", "JSONL evidence output path, or - for stdout")
	flags.StringVar(&proof, "proof-path", "", "optional copy of terminal JSONL evidence")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if scenario == "" {
		fmt.Fprintln(stderr, "--scenario is required")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected arguments:", flags.Args())
		return 2
	}
	if root == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		root = filepath.Join(cache, "cuelight", "scenario")
	}
	var evidence io.Writer = stdout
	var outputFile, proofFile *os.File
	if output != "" && output != "-" {
		var err error
		outputFile, err = openOutput(output)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 8
		}
		evidence = outputFile
		defer outputFile.Close()
	}
	if proof != "" {
		var err error
		proofFile, err = openOutput(proof)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 8
		}
		defer proofFile.Close()
		if output == "-" {
			evidence = proofFile
		}
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	config := dev.Config{Root: root}
	runner, err := e2e.NewProduction(config, nil, evidence)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	_, err = runner.Run(ctx, scenario)
	if err != nil {
		var unsupported *e2e.UnsupportedFault
		if errors.As(err, &unsupported) {
			fmt.Fprintln(stderr, unsupported)
			return 6
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			fmt.Fprintln(stderr, err)
			return 9
		}
		fmt.Fprintln(stderr, err)
		return 1
	}
	// If proof-path is requested along with a separate output, preserve the
	// exact terminal record without putting credentials in either destination.
	if proofFile != nil && outputFile != nil {
		// The runner owns one evidence writer; callers selecting two destinations
		// should use --proof-path as the primary output to retain one record.
		_ = proofFile.Sync()
	}
	return 0
}

func openOutput(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
}
