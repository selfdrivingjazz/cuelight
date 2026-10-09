package operatorapi

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net/url"
	"strings"
)

// JoinLinks builds `cuelight://join` links handed to desktop nodes with a
// fresh enrollment token. The zero value (no ControlURL) disables links.
type JoinLinks struct {
	// ControlURL is the public HTTPS origin of this control plane.
	ControlURL string
	// NetworkName is the human name the desktop app shows for this network.
	NetworkName string
	// CASHA256 is the hex SHA-256 of the self-signed root DER that anchors
	// the control TLS chain; empty for publicly trusted chains.
	CASHA256 string
}

// NewJoinLinks derives link settings from the public URL, an optional network
// name (default: the public URL host), and the PEM TLS chain file contents.
func NewJoinLinks(publicURL, networkName string, chainPEM []byte) (JoinLinks, error) {
	publicURL = strings.TrimRight(strings.TrimSpace(publicURL), "/")
	if publicURL == "" {
		return JoinLinks{}, nil
	}
	parsed, err := url.Parse(publicURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return JoinLinks{}, errors.New("join links need an absolute HTTPS public URL")
	}
	name := strings.TrimSpace(networkName)
	if name == "" {
		name = parsed.Hostname()
	}
	digest, err := SelfSignedRootSHA256(chainPEM)
	if err != nil {
		return JoinLinks{}, err
	}
	return JoinLinks{ControlURL: publicURL, NetworkName: name, CASHA256: digest}, nil
}

// URL returns the join link for an enrollment secret, or "" when disabled.
func (links JoinLinks) URL(secret string) string {
	if links.ControlURL == "" {
		return ""
	}
	// Built by hand to keep the documented parameter order (url.Values sorts keys).
	link := "cuelight://join?v=1&url=" + url.QueryEscape(links.ControlURL) + "&token=" + url.QueryEscape(secret)
	if links.CASHA256 != "" {
		link += "&ca=sha256:" + links.CASHA256
	}
	return link + "&name=" + url.QueryEscape(links.NetworkName)
}

// SelfSignedRootSHA256 returns the hex SHA-256 of the DER of the last
// certificate in a PEM chain when that certificate is self-signed, and ""
// when it was issued by someone else (a publicly trusted chain).
func SelfSignedRootSHA256(chainPEM []byte) (string, error) {
	var top *x509.Certificate
	for rest := chainPEM; ; {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return "", err
		}
		top = certificate
	}
	if top == nil {
		return "", errors.New("TLS chain holds no certificate")
	}
	// Self-signed: issuer equals subject and the certificate's own key signs it
	// (CheckSignature, unlike CheckSignatureFrom, also accepts self-signed leaves).
	if !bytes.Equal(top.RawIssuer, top.RawSubject) || top.CheckSignature(top.SignatureAlgorithm, top.RawTBSCertificate, top.Signature) != nil {
		return "", nil
	}
	digest := sha256.Sum256(top.Raw)
	return hex.EncodeToString(digest[:]), nil
}
