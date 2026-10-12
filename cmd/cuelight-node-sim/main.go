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

	"github.com/selfdrivingjazz/cuelight/internal/buildinfo"
	playerclient "github.com/selfdrivingjazz/cuelight/internal/player/client"
	"github.com/selfdrivingjazz/cuelight/internal/sim"
)

func main() {
	var scenario, controlURL, caPEM, enrollmentToken, recoveryToken, statePath string
	var duration time.Duration
	flag.StringVar(&scenario, "scenario", "", "run a deterministic scenario (enroll, stream-cache, candidate-failure, offline-reconnect, event-overflow, insufficient-capacity, no-output-recovery)")
	flag.StringVar(&controlURL, "control-url", "", "HTTPS control-plane URL")
	flag.StringVar(&caPEM, "server-ca", "", "provisioned server CA PEM file")
	flag.StringVar(&enrollmentToken, "enrollment-token", "", "one-time enrollment token")
	flag.StringVar(&recoveryToken, "recovery-token", "", "one-time recovery token")
	flag.StringVar(&statePath, "state", "cuelight-node-sim.state.json", "durable simulator state path")
	flag.DurationVar(&duration, "duration", 0, "stop after this duration (default: until interrupted)")
	var capabilities playerclient.CapabilityReport
	flag.StringVar(&capabilities.Platform, "platform", "", "reported platform (default sim)")
	flag.StringVar(&capabilities.SoftwareVersion, "software-version", "", "reported softwareVersion")
	flag.StringVar(&capabilities.UpdateMethod, "update-method", "", "reported updateMethod (desktop, appliance, browser, android, none)")
	showVersion := flag.Bool("version", false, "print version")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildinfo.String("cuelight-node-sim"))
		return
	}
	if scenario == "" && controlURL == "" {
		fmt.Fprintf(os.Stderr, "cuelight-node-sim %s: -scenario or -control-url is required\n", buildinfo.Version)
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
	node, err := sim.New(sim.Config{ControlURL: controlURL, ServerCAPEM: ca, EnrollmentToken: enrollmentToken, RecoveryToken: recoveryToken, StatePath: statePath, Evidence: os.Stdout, Capabilities: capabilities})
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
