package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunReportsActionableConfigurationError(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), nil, func(string) (string, bool) {
		return "", false
	}, &stdout, &stderr)
	if exitCode != 2 {
		t.Fatalf("exit code = %d, want 2", exitCode)
	}
	if !strings.Contains(stderr.String(), "CABLE_CONTROL_DATABASE_URL") || !strings.Contains(stderr.String(), "is required") {
		t.Fatalf("stderr is not actionable: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestRunVersionDoesNotRequireRuntimeConfiguration(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-version"}, nil, &stdout, &stderr)
	if exitCode != 0 || stdout.String() != "cable-control "+version+"\n" {
		t.Fatalf("exit = %d stdout = %q stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}
