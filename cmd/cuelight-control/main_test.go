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
	if !strings.Contains(stderr.String(), "CUELIGHT_CONTROL_DATABASE_URL") || !strings.Contains(stderr.String(), "is required") {
		t.Fatalf("stderr is not actionable: %q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}

func TestRunServeModeHasExplicitDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"serve"}, func(string) (string, bool) { return "", false }, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want configuration exit 2; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "CUELIGHT_CONTROL_DATABASE_URL") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunAdminTokenModeValidatesDatabaseWithoutTLSAssets(t *testing.T) {
	var stdout, stderr bytes.Buffer
	lookup := func(name string) (string, bool) {
		if name == "CUELIGHT_CONTROL_DATABASE_URL" {
			return "", false
		}
		switch name {
		case "CUELIGHT_CONTROL_TLS_CERT_PATH", "CUELIGHT_CONTROL_TLS_KEY_PATH", "CUELIGHT_CONTROL_PUBLISHER_KEY_PATH", "CUELIGHT_CONTROL_CAS_PATH":
			return "unused-" + name, true
		}
		return "", false
	}
	if code := run(context.Background(), []string{"admin", "token", "create"}, lookup, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2; stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "CUELIGHT_CONTROL_DATABASE_URL") {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("unexpected secret output: %q", stdout.String())
	}
}

func TestRunRejectsAmbiguousMode(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"admin", "token"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "usage") {
		t.Fatalf("stderr=%q", stderr.String())
	}
}

func TestRunVersionDoesNotRequireRuntimeConfiguration(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run(context.Background(), []string{"-version"}, nil, &stdout, &stderr)
	if exitCode != 0 || stdout.String() != "cuelight-control "+version+"\n" {
		t.Fatalf("exit = %d stdout = %q stderr = %q", exitCode, stdout.String(), stderr.String())
	}
}
