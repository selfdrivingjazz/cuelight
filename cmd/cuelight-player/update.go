package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os/exec"
	goruntime "runtime"
	"strings"
	"time"

	"github.com/jmilldotdev/cuelight/internal/buildinfo"
	"github.com/jmilldotdev/cuelight/internal/player/update"
	"github.com/jmilldotdev/cuelight/internal/updates"
)

const updateUsage = "usage: cuelight-player update apply [--opt-dir DIR] [--state-dir DIR] [--units UNIT,...] [--systemctl PATH] [--health-timeout DURATION]"

// runUpdate implements `cuelight-player update apply`, run as root by
// cuelight-updater.service when the player stages an appliance update.
func runUpdate(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] != "apply" {
		return errors.New(updateUsage)
	}
	defaults := update.DefaultLayout()
	flags := flag.NewFlagSet("cuelight-player update apply", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	optDir := flags.String("opt-dir", defaults.OptDir, "install root holding versions/ and current")
	stateDir := flags.String("state-dir", defaults.StateDir, "player state directory holding updates/ and health.json")
	units := flags.String("units", "cuelight-minimal-node.service", "comma-separated systemd units to restart")
	systemctl := flags.String("systemctl", "systemctl", "systemctl executable")
	healthTimeout := flags.Duration("health-timeout", 2*time.Minute, "how long the new version has to reach control")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 || *healthTimeout <= 0 {
		return errors.New(updateUsage)
	}
	applier := update.Applier{
		Layout:         update.Layout{OptDir: *optDir, StateDir: *stateDir},
		Trusted:        updates.TrustedKeys(),
		RunningVersion: buildinfo.Version,
		Arch:           goruntime.GOARCH,
		Units:          listValues(*units),
		Systemctl: func(ctx context.Context, args ...string) error {
			var output bytes.Buffer
			command := exec.CommandContext(ctx, *systemctl, args...)
			command.Stdout, command.Stderr = &output, &output
			if err := command.Run(); err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(output.String()))
			}
			return nil
		},
		Now: time.Now,
		Sleep: func(ctx context.Context, duration time.Duration) error {
			timer := time.NewTimer(duration)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		},
		HealthTimeout: *healthTimeout,
		HealthPoll:    time.Second,
		Logf:          log.Printf,
	}
	result, err := applier.Apply(ctx)
	if errors.Is(err, update.ErrNoRequest) {
		log.Print("no pending software update request")
		return nil
	}
	if err != nil {
		return fmt.Errorf("software update %s: %w", result.Version, err)
	}
	log.Printf("software update %s applied", result.Version)
	return nil
}

func listValues(value string) []string {
	var values []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			values = append(values, item)
		}
	}
	return values
}
