package pki

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestBootstrapAuthorityAndServerCredential(t *testing.T) {
	clock := &fixedClock{now: time.Date(2032, time.June, 7, 8, 9, 10, 0, time.UTC)}
	serials := &sequenceSource{serial: 200}
	bootstrap, err := BootstrapAuthority(context.Background(), clock, serials, AuthorityBootstrapOptions{
		CommonName: "Cable control CA",
		Lifetime:   365 * 24 * time.Hour,
		Backdate:   time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ca := parseCertificate(t, bootstrap.Certificate)
	if !ca.IsCA || ca.KeyUsage&x509.KeyUsageCertSign == 0 {
		t.Fatalf("bootstrap certificate is not a signing CA: %+v", ca)
	}
	if _, err := tls.X509KeyPair([]byte(bootstrap.Certificate), []byte(bootstrap.PrivateKey)); err != nil {
		t.Fatalf("bootstrap CA key pair does not match: %v", err)
	}

	server, err := bootstrap.Authority.BootstrapServer(context.Background(), clock, serials, []string{"control.local", "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tls.X509KeyPair([]byte(server.Certificate), []byte(server.PrivateKey)); err != nil {
		t.Fatalf("server key pair does not match: %v", err)
	}
	certificate := parseCertificate(t, server.Certificate)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	if _, err := certificate.Verify(x509.VerifyOptions{Roots: roots, DNSName: "control.local", CurrentTime: clock.now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		t.Fatalf("server certificate does not verify: %v", err)
	}
	if len(certificate.ExtKeyUsage) != 1 || certificate.ExtKeyUsage[0] != x509.ExtKeyUsageServerAuth {
		t.Fatalf("server EKUs = %v, want server auth only", certificate.ExtKeyUsage)
	}
	if block, rest := pem.Decode([]byte(server.PrivateKey)); block == nil || block.Type != "PRIVATE KEY" || len(rest) != 0 {
		t.Fatal("server private key is not PKCS#8 PEM")
	}
}
