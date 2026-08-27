package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	playerclient "github.com/jmill/cable/internal/player/client"
)

func TestProvisionEnrollsNamedNodeAndWritesPrivateRuntimeIdentity(t *testing.T) {
	_, issuerKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	var received playerclient.EnrollmentRequest
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/player/enroll" || r.Header.Get("Idempotency-Key") == "" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
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
	defer server.Close()
	serverCA := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	root := t.TempDir()
	caPath := filepath.Join(root, "ca.pem")
	if err := os.WriteFile(caPath, serverCA, 0o600); err != nil {
		t.Fatal(err)
	}
	outputDirectory := filepath.Join(root, "node")
	var output strings.Builder
	lookup := func(key string) (string, bool) {
		if key == provisionTokenEnvironment {
			return "one-time-token", true
		}
		return "", false
	}
	if err := runProvision(context.Background(), []string{"--control-url", server.URL, "--ca-file", caPath, "--node-name", "MacBook Pro", "--output", outputDirectory}, lookup, &output); err != nil {
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
