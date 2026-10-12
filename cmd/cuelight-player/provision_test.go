package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	playerclient "github.com/selfdrivingjazz/cuelight/internal/player/client"
)

// newEnrollmentServer is a TLS control stub that enrolls any node; received
// captures the last enrollment request.
func newEnrollmentServer(t *testing.T, received *playerclient.EnrollmentRequest) *httptest.Server {
	t.Helper()
	_, issuerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/player/enroll" || r.Header.Get("Idempotency-Key") == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(received); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		publicKey, err := base64.StdEncoding.DecodeString(received.PublicKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "node-one"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, ed25519.PublicKey(publicKey), issuerKey)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		publisher := make([]byte, ed25519.PublicKeySize)
		for index := range publisher {
			publisher[index] = byte(index + 1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(playerclient.Enrollment{
			ClientCredential: playerclient.ClientCredential{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})), ExpiresAt: template.NotAfter},
			NodeID:           "node-one", EventEpoch: "epoch-one", PublisherKeyID: "publisher-one", PublisherPublicKey: base64.StdEncoding.EncodeToString(publisher),
		})
	}))
	t.Cleanup(server.Close)
	return server
}

func provisionLookup(key string) (string, bool) {
	if key == provisionTokenEnvironment {
		return "one-time-token", true
	}
	return "", false
}

func TestProvisionPinsControllerCAByDigest(t *testing.T) {
	var received playerclient.EnrollmentRequest
	server := newEnrollmentServer(t, &received)
	digest := sha256.Sum256(server.Certificate().Raw)

	output := filepath.Join(t.TempDir(), "node")
	var stdout strings.Builder
	if err := runProvision(context.Background(), []string{"--control-url", server.URL, "--ca-sha256", hex.EncodeToString(digest[:]), "--output", output}, provisionLookup, &stdout); err != nil {
		t.Fatal(err)
	}
	var result provisionedNode
	if err := json.Unmarshal([]byte(stdout.String()), &result); err != nil {
		t.Fatal(err)
	}
	pinned, err := os.ReadFile(result.CAPath)
	if err != nil {
		t.Fatal(err)
	}
	if block, _ := pem.Decode(pinned); block == nil || !bytes.Equal(block.Bytes, server.Certificate().Raw) {
		t.Fatal("controller-ca.pem is not the pinned certificate")
	}

	mismatch := filepath.Join(t.TempDir(), "node")
	digest[0] ^= 0xff
	err = runProvision(context.Background(), []string{"--control-url", server.URL, "--ca-sha256", hex.EncodeToString(digest[:]), "--output", mismatch}, provisionLookup, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "pinned CA") {
		t.Fatalf("mismatched pin err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(mismatch, "node.json")); !os.IsNotExist(statErr) {
		t.Fatal("mismatched pin still provisioned a node")
	}
}

func TestProvisionEnrollsNamedNodeAndWritesPrivateRuntimeIdentity(t *testing.T) {
	var received playerclient.EnrollmentRequest
	server := newEnrollmentServer(t, &received)
	serverCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	root := t.TempDir()
	caPath := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caPath, serverCA, 0o600); err != nil {
		t.Fatal(err)
	}
	outputDirectory := filepath.Join(root, "node")
	var output strings.Builder
	if err := runProvision(context.Background(), []string{"--control-url", server.URL, "--ca-file", caPath, "--node-name", "MacBook Pro", "--output", outputDirectory}, provisionLookup, &output); err != nil {
		t.Fatal(err)
	}
	if received.Token != "one-time-token" || received.Name != "MacBook Pro" {
		t.Fatalf("enrollment request = %#v", received)
	}
	var result provisionedNode
	if err := json.Unmarshal([]byte(output.String()), &result); err != nil {
		t.Fatal(err)
	}
	if result.NodeID != "node-one" || result.ControlURL != server.URL+"/api/v1" {
		t.Fatalf("provisioned node = %#v", result)
	}
	for _, path := range []string{result.CAPath, result.CertificatePath, result.PrivateKeyPath, result.PublisherPublicKeyPath, filepath.Join(outputDirectory, "node.json")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("identity file %s mode = %o", path, info.Mode().Perm())
		}
	}
}
