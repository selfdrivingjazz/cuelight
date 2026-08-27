package pki

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"strings"
	"time"
)

type AuthorityBootstrapOptions struct {
	CommonName string
	Lifetime   time.Duration
	Backdate   time.Duration
	Random     io.Reader
}

type AuthorityBootstrap struct {
	Authority   *CertificateAuthority
	Certificate string
	PrivateKey  string
	ExpiresAt   time.Time
}

type ServerCredential struct {
	ClientCredential
	PrivateKey string
}

func BootstrapAuthority(ctx context.Context, clock Clock, serials SerialSource, options AuthorityBootstrapOptions) (AuthorityBootstrap, error) {
	if clock == nil || serials == nil || strings.TrimSpace(options.CommonName) == "" || options.Lifetime <= 0 || options.Backdate < 0 {
		return AuthorityBootstrap{}, fmt.Errorf("%w: invalid CA bootstrap options", ErrInvalidInput)
	}
	reader := options.Random
	if reader == nil {
		reader = rand.Reader
	}
	publicKey, privateKey, err := ed25519.GenerateKey(reader)
	if err != nil {
		return AuthorityBootstrap{}, fmt.Errorf("generate CA key: %w", err)
	}
	serial, err := serials.NextSerial(ctx)
	if err != nil {
		return AuthorityBootstrap{}, fmt.Errorf("generate CA serial: %w", err)
	}
	if err := validateSerial(serial); err != nil {
		return AuthorityBootstrap{}, err
	}
	now := clock.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).Set(serial),
		Subject:               pkix.Name{CommonName: strings.TrimSpace(options.CommonName)},
		NotBefore:             now.Add(-options.Backdate),
		NotAfter:              now.Add(options.Lifetime),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(reader, template, template, publicKey, privateKey)
	if err != nil {
		return AuthorityBootstrap{}, fmt.Errorf("create CA certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return AuthorityBootstrap{}, fmt.Errorf("parse generated CA certificate: %w", err)
	}
	authority, err := NewCertificateAuthority(certificate, privateKey, options.Lifetime, options.Backdate)
	if err != nil {
		return AuthorityBootstrap{}, err
	}
	authority.random = reader
	privateKeyPEM, err := marshalPrivateKey(privateKey)
	if err != nil {
		return AuthorityBootstrap{}, err
	}
	return AuthorityBootstrap{
		Authority:   authority,
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		PrivateKey:  privateKeyPEM,
		ExpiresAt:   certificate.NotAfter,
	}, nil
}

func (authority *CertificateAuthority) BootstrapServer(ctx context.Context, clock Clock, serials SerialSource, names []string) (ServerCredential, error) {
	if clock == nil || serials == nil {
		return ServerCredential{}, fmt.Errorf("%w: missing server bootstrap dependency", ErrInvalidInput)
	}
	dnsNames, ipAddresses, err := validateServerNames(names)
	if err != nil {
		return ServerCredential{}, err
	}
	var commonName string
	if len(dnsNames) != 0 {
		commonName = dnsNames[0]
	} else {
		commonName = ipAddresses[0].String()
	}
	publicKey, privateKey, err := ed25519.GenerateKey(authority.random)
	if err != nil {
		return ServerCredential{}, fmt.Errorf("generate server key: %w", err)
	}
	serial, err := serials.NextSerial(ctx)
	if err != nil {
		return ServerCredential{}, fmt.Errorf("generate server serial: %w", err)
	}
	if err := validateSerial(serial); err != nil {
		return ServerCredential{}, err
	}
	now := clock.Now().UTC()
	notBefore, notAfter, err := authority.validity(now)
	if err != nil {
		return ServerCredential{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).Set(serial),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              dnsNames,
		IPAddresses:           ipAddresses,
	}
	der, err := x509.CreateCertificate(authority.random, template, authority.certificate, publicKey, authority.signer)
	if err != nil {
		return ServerCredential{}, fmt.Errorf("issue server certificate: %w", err)
	}
	privateKeyPEM, err := marshalPrivateKey(privateKey)
	if err != nil {
		return ServerCredential{}, err
	}
	return ServerCredential{
		ClientCredential: ClientCredential{
			Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
			Serial:      serial.String(),
			ExpiresAt:   notAfter,
		},
		PrivateKey: privateKeyPEM,
	}, nil
}

func (authority *CertificateAuthority) validity(now time.Time) (time.Time, time.Time, error) {
	if now.Before(authority.certificate.NotBefore) || !now.Before(authority.certificate.NotAfter) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: CA certificate is not currently valid", ErrInvalidCA)
	}
	notBefore := now.Add(-authority.backdate)
	if notBefore.Before(authority.certificate.NotBefore) {
		notBefore = authority.certificate.NotBefore
	}
	notAfter := now.Add(authority.lifetime)
	if notAfter.After(authority.certificate.NotAfter) {
		notAfter = authority.certificate.NotAfter
	}
	if !notAfter.After(now) || !notAfter.After(notBefore) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: CA validity does not cover issued certificate", ErrInvalidCA)
	}
	return notBefore, notAfter, nil
}

func validateSerial(serial *big.Int) error {
	if serial == nil || serial.Sign() <= 0 || serial.BitLen() > 159 {
		return ErrInvalidSerial
	}
	return nil
}

func marshalPrivateKey(privateKey any) (string, error) {
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return "", fmt.Errorf("marshal private key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
}

func validateServerNames(names []string) ([]string, []net.IP, error) {
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one server name is required", ErrInvalidInput)
	}
	dnsNames := make([]string, 0, len(names))
	ipAddresses := make([]net.IP, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, nil, fmt.Errorf("%w: invalid server name", ErrInvalidInput)
		}
		if address := net.ParseIP(name); address != nil {
			ipAddresses = append(ipAddresses, address)
			continue
		}
		if !validDNSName(name) {
			return nil, nil, fmt.Errorf("%w: invalid DNS server name", ErrInvalidInput)
		}
		dnsNames = append(dnsNames, name)
	}
	return dnsNames, ipAddresses, nil
}

func validDNSName(name string) bool {
	if len(name) > 253 {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return false
		}
		for _, character := range label {
			if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' {
				continue
			}
			return false
		}
	}
	return true
}
