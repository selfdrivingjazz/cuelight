package pki

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"strings"
	"time"

	"github.com/jmill/cable/internal/protocol"
)

type CertificateAuthority struct {
	certificate *x509.Certificate
	signer      crypto.Signer
	lifetime    time.Duration
	backdate    time.Duration
	random      io.Reader
}

func NewCertificateAuthority(certificate *x509.Certificate, signer crypto.Signer, lifetime, backdate time.Duration) (*CertificateAuthority, error) {
	if certificate == nil || signer == nil || !certificate.IsCA || certificate.KeyUsage&x509.KeyUsageCertSign == 0 {
		return nil, ErrInvalidCA
	}
	if lifetime <= 0 || backdate < 0 {
		return nil, fmt.Errorf("%w: invalid certificate lifetime or backdate", ErrInvalidCA)
	}
	certificateKey, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("%w: certificate public key: %v", ErrInvalidCA, err)
	}
	signerKey, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil || !bytes.Equal(certificateKey, signerKey) {
		return nil, fmt.Errorf("%w: signer does not match certificate", ErrInvalidCA)
	}
	return &CertificateAuthority{
		certificate: certificate,
		signer:      signer,
		lifetime:    lifetime,
		backdate:    backdate,
		random:      rand.Reader,
	}, nil
}

func NewCertificateAuthorityPEM(certificatePEM []byte, signer crypto.Signer, lifetime, backdate time.Duration) (*CertificateAuthority, error) {
	block, rest := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, fmt.Errorf("%w: malformed CA certificate PEM", ErrInvalidCA)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%w: parse CA certificate: %v", ErrInvalidCA, err)
	}
	return NewCertificateAuthority(certificate, signer, lifetime, backdate)
}

func (authority *CertificateAuthority) Issue(nodeID string, publicKey crypto.PublicKey, serial *big.Int, now time.Time) (ClientCredential, error) {
	if err := validateNodeID(nodeID); err != nil {
		return ClientCredential{}, err
	}
	if err := validatePublicKey(publicKey); err != nil {
		return ClientCredential{}, err
	}
	if err := validateSerial(serial); err != nil {
		return ClientCredential{}, err
	}
	now = now.UTC()
	notBefore, notAfter, err := authority.validity(now)
	if err != nil {
		return ClientCredential{}, err
	}

	san := protocol.NodeSANPrefix + nodeID
	template := &x509.Certificate{
		SerialNumber:          new(big.Int).Set(serial),
		Subject:               pkix.Name{CommonName: san},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{san},
	}
	der, err := x509.CreateCertificate(authority.random, template, authority.certificate, publicKey, authority.signer)
	if err != nil {
		return ClientCredential{}, fmt.Errorf("issue client certificate: %w", err)
	}
	return ClientCredential{
		Certificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		Serial:      serial.String(),
		ExpiresAt:   notAfter,
	}, nil
}

func ParseCredentialRequest(request CredentialRequest) (crypto.PublicKey, error) {
	hasPublicKey := len(request.PublicKey) != 0
	hasCSR := len(request.CSR) != 0
	if hasPublicKey == hasCSR {
		return nil, fmt.Errorf("%w: exactly one public key or CSR is required", ErrInvalidInput)
	}
	if hasCSR {
		return parseCSR(request.CSR)
	}
	return parsePublicKey(request.PublicKey)
}

func parseCSR(value []byte) (crypto.PublicKey, error) {
	der := value
	if block, rest := pem.Decode(value); block != nil {
		if block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST" {
			return nil, fmt.Errorf("%w: unexpected PEM block %q", ErrInvalidCSR, block.Type)
		}
		if len(bytes.TrimSpace(rest)) != 0 {
			return nil, fmt.Errorf("%w: trailing PEM data", ErrInvalidCSR)
		}
		der = block.Bytes
	}
	request, err := x509.ParseCertificateRequest(der)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCSR, err)
	}
	if err := request.CheckSignature(); err != nil {
		return nil, fmt.Errorf("%w: signature verification failed: %v", ErrInvalidCSR, err)
	}
	if err := validatePublicKey(request.PublicKey); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidCSR, err)
	}
	return request.PublicKey, nil
}

func parsePublicKey(value []byte) (crypto.PublicKey, error) {
	der := value
	if block, rest := pem.Decode(value); block != nil {
		if block.Type != "PUBLIC KEY" {
			return nil, fmt.Errorf("%w: unexpected PEM block %q", ErrInvalidPublicKey, block.Type)
		}
		if len(bytes.TrimSpace(rest)) != 0 {
			return nil, fmt.Errorf("%w: trailing PEM data", ErrInvalidPublicKey)
		}
		der = block.Bytes
	}
	if len(der) == ed25519.PublicKeySize {
		key := make(ed25519.PublicKey, ed25519.PublicKeySize)
		copy(key, der)
		return key, nil
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPublicKey, err)
	}
	if err := validatePublicKey(key); err != nil {
		return nil, err
	}
	return key, nil
}

func validatePublicKey(key crypto.PublicKey) error {
	switch key := key.(type) {
	case ed25519.PublicKey:
		if len(key) != ed25519.PublicKeySize {
			return ErrInvalidPublicKey
		}
	case *ecdsa.PublicKey:
		if key == nil || key.Curve == nil || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return ErrInvalidPublicKey
		}
		switch key.Curve.Params().Name {
		case "P-256", "P-384", "P-521":
		default:
			return fmt.Errorf("%w: unsupported ECDSA curve", ErrInvalidPublicKey)
		}
	case *rsa.PublicKey:
		if key == nil || key.N == nil || key.N.BitLen() < 2048 || key.E < 3 || key.E%2 == 0 {
			return fmt.Errorf("%w: RSA key must be at least 2048 bits", ErrInvalidPublicKey)
		}
	default:
		return fmt.Errorf("%w: unsupported key type", ErrInvalidPublicKey)
	}
	return nil
}

func validateNodeID(nodeID string) error {
	if nodeID == "" || len(protocol.NodeSANPrefix)+len(nodeID) > 253 || strings.HasPrefix(nodeID, "-") || strings.HasSuffix(nodeID, "-") {
		return fmt.Errorf("%w: invalid node ID", ErrInvalidInput)
	}
	for _, character := range nodeID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' {
			continue
		}
		return fmt.Errorf("%w: invalid node ID", ErrInvalidInput)
	}
	return nil
}
