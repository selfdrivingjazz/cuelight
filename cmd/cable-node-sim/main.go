package main

import (
	"context"
	"encoding/base64"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/jmill/cable/internal/sim"
)

var version = "dev"

func main() {
	var scenario, controlURL, caPEM, enrollmentToken, recoveryToken, statePath string
	var duration time.Duration
	flag.StringVar(&scenario, "scenario", "", "run a deterministic scenario (enroll, stream-cache, candidate-failure, offline-reconnect, event-overflow, insufficient-capacity, no-output-recovery)")
	flag.StringVar(&controlURL, "control-url", "", "HTTPS control-plane URL")
	flag.StringVar(&caPEM, "server-ca", "", "provisioned server CA PEM file")
	flag.StringVar(&enrollmentToken, "enrollment-token", "", "one-time enrollment token")
	flag.StringVar(&recoveryToken, "recovery-token", "", "one-time recovery token")
	flag.StringVar(&statePath, "state", "cable-node-sim.state.json", "durable simulator state path")
	flag.DurationVar(&duration, "duration", 0, "stop after this duration (default: until interrupted)")
	flag.Parse()
	if scenario == "" && controlURL == "" {
		fmt.Fprintf(os.Stderr, "cable-node-sim %s: -scenario or -control-url is required\n", version)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}
	if scenario != "" {
		if err := sim.RunScenario(ctx, sim.ScenarioConfig{Name: scenario, Evidence: os.Stdout}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	ca, err := os.ReadFile(caPEM)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if strings.HasPrefix(string(ca), "base64:") {
		ca, err = base64.StdEncoding.DecodeString(strings.TrimSpace(strings.TrimPrefix(string(ca), "base64:")))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
	}
	node, err := sim.New(sim.Config{ControlURL: controlURL, ServerCAPEM: ca, EnrollmentToken: enrollmentToken, RecoveryToken: recoveryToken, StatePath: statePath, Evidence: os.Stdout})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer node.Close()
	if err := node.Run(ctx); err != nil && ctx.Err() == nil {
		node.Observe("run", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
