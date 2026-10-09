package protocol

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type revokedMap map[string]bool

func (r revokedMap) Revoked(nodeID, serial string) bool { return r[nodeID+":"+serial] }

func TestNodeIdentityComesFromVerifiedSANNotHeader(t *testing.T) {
	caCert, caKey, caPEM := makeCA(t, "trusted")
	serverCert := makeCertificate(t, caCert, caKey, certOptions{serverName: "localhost"})
	clientCert := makeCertificate(t, caCert, caKey, certOptions{nodeID: "node-one"})

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		nodeID, err := NodeIdentity(request, nil)
		if err != nil {
			http.Error(writer, err.Error(), http.StatusUnauthorized)
			return
		}
		writer.Header().Set("Content-Type", "text/plain")
		writer.Write([]byte(nodeID))
	}))
	server.TLS = &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    pool,
		MinVersion:   tls.VersionTLS13,
	}
	server.StartTLS()
	defer server.Close()

	client := server.Client()
	clientTLS := client.Transport.(*http.Transport).TLSClientConfig
	clientTLS.Certificates = []tls.Certificate{clientCert}
	clientTLS.RootCAs = pool
	clientTLS.ServerName = "localhost"
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("X-Cuelight-Node-ID", "spoofed")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if got := response.Header.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("unexpected response: %s", got)
	}
	buffer, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(buffer) != "node-one" {
		t.Fatalf("header spoofed identity: %q", buffer)
	}
}

func TestNodeIdentityRejectsRevocation(t *testing.T) {
	certificate := &x509.Certificate{DNSNames: []string{NodeSANPrefix + "node-one"}, SerialNumber: big.NewInt(42)}
	request := httptest.NewRequest(http.MethodGet, "https://control.invalid", nil)
	request.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{certificate},
		VerifiedChains:   [][]*x509.Certificate{{certificate}},
	}
	if _, err := NodeIdentity(request, revokedMap{"node-one:42": true}); err == nil {
		t.Fatal("revoked certificate accepted")
	}
}

func TestNodeIdentityRequiresVerifiedCertificate(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "https://control.invalid", nil)
	request.Header.Set("X-Cuelight-Node-ID", "spoofed")
	if _, err := NodeIdentity(request, nil); err == nil {
		t.Fatal("unverified request accepted")
	}
}

type certOptions struct {
	nodeID     string
	serverName string
}

func makeCA(t *testing.T, name string) (*x509.Certificate, ed25519.PrivateKey, []byte) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return certificate, privateKey, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func makeCertificate(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, options certOptions) tls.Certificate {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	if options.nodeID != "" {
		template.DNSNames = []string{NodeSANPrefix + options.nodeID}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	if options.serverName != "" {
		template.DNSNames = []string{options.serverName}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, publicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})
	certificate, err := tls.X509KeyPair(certificatePEM, privatePEM)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}
