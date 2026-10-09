package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// fetchPinnedCA dials control and returns, as PEM, the certificate in its
// presented chain whose DER SHA-256 is pin, provided the server certificate
// verifies to that certificate for the control host. This lets a join link
// carry a self-signed control CA without a separate file.
func fetchPinnedCA(ctx context.Context, controlURL, pin string) ([]byte, error) {
	want, err := hex.DecodeString(strings.ToLower(strings.TrimPrefix(strings.TrimSpace(pin), "sha256:")))
	if err != nil || len(want) != sha256.Size {
		return nil, errors.New("--ca-sha256 must be 64 hexadecimal characters")
	}
	parsed, err := url.Parse(controlURL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return nil, errors.New("control URL must be an absolute HTTPS URL")
	}
	host, port := parsed.Hostname(), parsed.Port()
	if port == "" {
		port = "443"
	}
	var anchor *x509.Certificate
	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: host,
		// Verification happens in VerifyConnection against the pinned anchor.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("controller presented no certificate")
			}
			intermediates := x509.NewCertPool()
			for _, certificate := range state.PeerCertificates {
				sum := sha256.Sum256(certificate.Raw)
				if subtle.ConstantTimeCompare(sum[:], want) == 1 {
					anchor = certificate
				} else {
					intermediates.AddCert(certificate)
				}
			}
			if anchor == nil {
				return fmt.Errorf("controller certificate chain does not contain the pinned CA sha256:%x", want)
			}
			roots := x509.NewCertPool()
			roots.AddCert(anchor)
			if _, err := state.PeerCertificates[0].Verify(x509.VerifyOptions{DNSName: host, Roots: roots, Intermediates: intermediates}); err != nil {
				return fmt.Errorf("controller certificate does not verify to the pinned CA: %w", err)
			}
			return nil
		},
	}
	dialer := &tls.Dialer{NetDialer: &net.Dialer{Timeout: 15 * time.Second}, Config: config}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		return nil, fmt.Errorf("fetch pinned controller CA: %w", err)
	}
	_ = connection.Close()
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: anchor.Raw}), nil
}
