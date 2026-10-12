package sim

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
	"path/filepath"
	"testing"
	"time"

	playerclient "github.com/selfdrivingjazz/cuelight/internal/player/client"
)

func TestEnrollPersistsCredentialAndReloadsClient(t *testing.T) {
	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "sim-test-ca"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true}
	_, err = x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request playerclient.EnrollmentRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		publicBytes, err := base64.StdEncoding.DecodeString(request.PublicKey)
		if err != nil {
			t.Error(err)
			return
		}
		nodeTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "cuelight-node-sim-node"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		nodeDER, err := x509.CreateCertificate(rand.Reader, nodeTemplate, caTemplate, ed25519.PublicKey(publicBytes), caPrivate)
		if err != nil {
			t.Error(err)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(playerclient.Enrollment{ClientCredential: playerclient.ClientCredential{Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: nodeDER}))}, NodeID: "sim-node", EventEpoch: "epoch-1", PublisherKeyID: "publisher", PublisherPublicKey: base64.StdEncoding.EncodeToString(caPublic)})
	}))
	defer server.Close()
	root := t.TempDir()
	statePath := filepath.Join(root, "node.json")
	node, err := New(Config{ControlURL: server.URL, ServerCAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), EnrollmentToken: "enroll-token", StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	if err := node.Enroll(context.Background()); err != nil {
		t.Fatal(err)
	}
	saved := node.State()
	if saved.NodeID != "sim-node" || saved.EventEpoch != "epoch-1" || saved.PrivateKey == "" || saved.Certificate == "" {
		t.Fatalf("state=%+v", saved)
	}
	if err := node.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(Config{ControlURL: server.URL, ServerCAPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if got := reloaded.State(); got.NodeID != saved.NodeID || got.PrivateKey != saved.PrivateKey || got.Certificate != saved.Certificate {
		t.Fatalf("reloaded state=%+v", got)
	}
}
