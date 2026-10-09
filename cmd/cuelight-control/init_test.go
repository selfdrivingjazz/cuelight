package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/app"
)

func TestInitGeneratesServableMaterialAndRefusesToOverwrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "control")
	var stdout, stderr bytes.Buffer
	args := []string{"init", "--dir", dir, "--hostname", "cuelight.lan", "--ip", "10.0.0.5", "--public-url", "https://cuelight.lan:8443", "--database-url", "postgres://u:p@db:5432/cuelight"}
	if code := run(t.Context(), args, nil, &stdout, &stderr); code != 0 {
		t.Fatalf("init exit %d: %s", code, stderr.String())
	}
	env := readEnvFile(t, filepath.Join(dir, envFileName))
	config, err := app.LoadConfig(func(name string) (string, bool) { value, ok := env[name]; return value, ok })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.LoadStartupAssets(config); err != nil {
		t.Fatalf("generated material is not servable: %v", err)
	}

	pair, err := tls.LoadX509KeyPair(config.TLSCertificatePath, config.TLSPrivateKeyPath)
	if err != nil || len(pair.Certificate) != 2 {
		t.Fatalf("chain = %d certs, err %v; want leaf+root", len(pair.Certificate), err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	root, _ := x509.ParseCertificate(pair.Certificate[1])
	if _, ok := leaf.PublicKey.(*ecdsa.PublicKey); !ok {
		t.Fatalf("leaf key is %T, want ECDSA", leaf.PublicKey)
	}
	if _, ok := root.PublicKey.(*ecdsa.PublicKey); !ok || root.CheckSignatureFrom(root) != nil {
		t.Fatalf("root is not a self-signed ECDSA certificate")
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	for _, name := range []string{"cuelight.lan", "10.0.0.5"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: roots}); err != nil {
			t.Fatalf("verify %s: %v", name, err)
		}
	}
	if leaf.NotAfter.Sub(leaf.NotBefore) > 826*24*time.Hour {
		t.Fatalf("leaf validity %s exceeds the 825-day platform limit", leaf.NotAfter.Sub(leaf.NotBefore))
	}
	for _, path := range []string{config.CAKeyPath, config.SealKeyPath, filepath.Join(dir, "tls", "server-root-key.pem"), filepath.Join(dir, envFileName)} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s: mode %v err %v; want 0600", path, info.Mode().Perm(), err)
		}
	}

	before, _ := os.ReadFile(config.CAKeyPath)
	stderr.Reset()
	if code := run(t.Context(), args, nil, &stdout, &stderr); code == 0 || !strings.Contains(stderr.String(), "already initialized") {
		t.Fatalf("second init exit %d stderr %q; want refusal", code, stderr.String())
	}
	if code := run(t.Context(), append(args, "--exists-ok"), nil, &stdout, &stderr); code != 0 {
		t.Fatalf("--exists-ok exit %d", code)
	}
	if after, _ := os.ReadFile(config.CAKeyPath); !bytes.Equal(before, after) {
		t.Fatal("re-running init changed existing key material")
	}
}

func TestInitRefusesPartialExistingMaterial(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "pki"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run(t.Context(), []string{"init", "--dir", dir, "--hostname", "cuelight.lan", "--exists-ok"}, nil, &stdout, &stderr); code == 0 {
		t.Fatal("init succeeded over an existing pki directory")
	}
	if _, err := os.Stat(filepath.Join(dir, envFileName)); !os.IsNotExist(err) {
		t.Fatalf("env file written despite refusal: %v", err)
	}
}

func TestInitReferencesExternalTLSWithoutGeneratingIt(t *testing.T) {
	source := t.TempDir()
	if code := run(t.Context(), []string{"init", "--dir", source, "--hostname", "example.ts.net"}, nil, &bytes.Buffer{}, &bytes.Buffer{}); code != 0 {
		t.Fatal("seed init failed")
	}
	cert, key := filepath.Join(source, "tls", "server.pem"), filepath.Join(source, "tls", "server-key.pem")
	dir := filepath.Join(t.TempDir(), "control")
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"init", "--dir", dir, "--tls-cert", cert, "--tls-key", key}, nil, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	env := readEnvFile(t, filepath.Join(dir, envFileName))
	if env["CUELIGHT_CONTROL_TLS_CERT_PATH"] != cert || env["CUELIGHT_CONTROL_TLS_KEY_PATH"] != key {
		t.Fatalf("env does not reference the given TLS files: %v", env)
	}
	if _, err := os.Stat(filepath.Join(dir, "tls")); !os.IsNotExist(err) {
		t.Fatalf("TLS material generated despite --tls-cert: %v", err)
	}
}

func TestInitEnvFileSurvivesShellSourcing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state with space")
	database := "postgres://cuelight:pw@db:5432/cuelight?sslmode=disable&application_name=cuelight"
	var stderr bytes.Buffer
	if code := run(t.Context(), []string{"init", "--dir", dir, "--hostname", "mars.lan", "--network-name", "Mars College", "--database-url", database}, nil, &bytes.Buffer{}, &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	script := `set -a; . "$1"; printf '%s\n%s\n%s' "$CUELIGHT_CONTROL_NETWORK_NAME" "$CUELIGHT_CONTROL_DATABASE_URL" "$CUELIGHT_CONTROL_CAS_PATH"`
	output, err := exec.Command("/bin/sh", "-c", script, "sh", filepath.Join(dir, envFileName)).CombinedOutput()
	if err != nil {
		t.Fatalf("source env file: %v: %s", err, output)
	}
	want := "Mars College\n" + database + "\n" + filepath.Join(dir, "cas")
	if string(output) != want {
		t.Fatalf("sourced values = %q, want %q", output, want)
	}
}

func readEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(line, "#") {
			values[name] = strings.Trim(value, `"`)
		}
	}
	return values
}
