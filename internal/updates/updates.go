// Package updates defines Cuelight software release manifests and their
// vendor signatures.
//
// A release manifest lists every artifact built for one version. CI signs the
// exact manifest bytes with the offline vendor release key; control servers
// mirror signed envelopes and choose which version a node should run, but a
// node installs only artifacts named by a manifest that verifies against a key
// compiled into the node. A compromised or hostile control server can therefore
// pick among vendor-signed versions but cannot supply code.
package updates

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// SchemaV1 is the only manifest schema.
	SchemaV1 = 1
	// Product names the software a manifest describes.
	Product = "cuelight"
	// ManifestFileName is the signed envelope's file name in a release.
	ManifestFileName = "cuelight-release.json"

	// signingContext separates release signatures from every other Ed25519
	// signature the project produces (for example content release envelopes).
	signingContext = "cuelight-release-manifest-v1\n"
)

// Components.
const (
	ComponentControl     = "control"
	ComponentPlayer      = "player"
	ComponentCLI         = "cli"
	ComponentMinimalNode = "minimal-node"
	ComponentDesktop     = "desktop"
	ComponentAndroid     = "android"
)

// Channels.
const (
	ChannelStable = "stable"
	ChannelBeta   = "beta"
)

// trustedKeys are the vendor release public keys compiled into every binary.
// Rotation adds a new key here one release before signing with it.
var trustedKeys = map[string]string{
	"cuelight-2026a": "mfL8FY0Qdq1zohsm6NMeGOpPsnWhPvz8BBWQL71qsEg=",
}

// TrustedKeys returns the compiled-in vendor release keys by key id.
func TrustedKeys() map[string]ed25519.PublicKey {
	keys := make(map[string]ed25519.PublicKey, len(trustedKeys))
	for id, encoded := range trustedKeys {
		raw, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			panic(fmt.Sprintf("updates: invalid compiled release key %q", id))
		}
		keys[id] = ed25519.PublicKey(raw)
	}
	return keys
}

// Artifact is one downloadable file of a release.
type Artifact struct {
	Component string `json:"component"`
	OS        string `json:"os"`
	Arch      string `json:"arch"`
	Format    string `json:"format"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	SHA256    string `json:"sha256"`
}

// Manifest describes every artifact of one version.
type Manifest struct {
	Schema                 int        `json:"schema"`
	Product                string     `json:"product"`
	Version                string     `json:"version"`
	Channel                string     `json:"channel"`
	PublishedAt            time.Time  `json:"publishedAt"`
	MinimumProtocolVersion string     `json:"minimumProtocolVersion"`
	Artifacts              []Artifact `json:"artifacts"`
}

// Envelope carries the exact signed manifest bytes.
type Envelope struct {
	Manifest  []byte `json:"manifest"`
	KeyID     string `json:"keyId"`
	Signature []byte `json:"signature"`
}

// Find returns the artifact for a component/os/arch/format, if present.
func (m Manifest) Find(component, os, arch, format string) (Artifact, bool) {
	for _, artifact := range m.Artifacts {
		if artifact.Component == component && artifact.OS == os && artifact.Arch == arch && artifact.Format == format {
			return artifact, true
		}
	}
	return Artifact{}, false
}

// Validate checks the manifest's structural invariants.
func (m Manifest) Validate() error {
	if m.Schema != SchemaV1 {
		return fmt.Errorf("unsupported manifest schema %d", m.Schema)
	}
	if m.Product != Product {
		return fmt.Errorf("manifest product %q is not %q", m.Product, Product)
	}
	if _, err := ParseVersion(m.Version); err != nil {
		return fmt.Errorf("manifest version: %w", err)
	}
	if m.Channel != ChannelStable && m.Channel != ChannelBeta {
		return fmt.Errorf("unsupported channel %q", m.Channel)
	}
	if m.PublishedAt.IsZero() {
		return errors.New("manifest publishedAt is required")
	}
	if strings.TrimSpace(m.MinimumProtocolVersion) == "" {
		return errors.New("manifest minimumProtocolVersion is required")
	}
	if len(m.Artifacts) == 0 {
		return errors.New("manifest has no artifacts")
	}
	names := make(map[string]struct{}, len(m.Artifacts))
	targets := make(map[string]struct{}, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		if artifact.Name == "" || strings.ContainsAny(artifact.Name, "/\\") || artifact.Name == "." || artifact.Name == ".." {
			return fmt.Errorf("artifact name %q is not a plain file name", artifact.Name)
		}
		if _, dup := names[artifact.Name]; dup {
			return fmt.Errorf("duplicate artifact name %q", artifact.Name)
		}
		names[artifact.Name] = struct{}{}
		if artifact.Component == "" || artifact.OS == "" || artifact.Arch == "" || artifact.Format == "" {
			return fmt.Errorf("artifact %q needs component, os, arch, and format", artifact.Name)
		}
		target := artifact.Component + "/" + artifact.OS + "/" + artifact.Arch + "/" + artifact.Format
		if _, dup := targets[target]; dup {
			return fmt.Errorf("duplicate artifact target %s", target)
		}
		targets[target] = struct{}{}
		if artifact.Size <= 0 {
			return fmt.Errorf("artifact %q has no size", artifact.Name)
		}
		if decoded, err := hex.DecodeString(artifact.SHA256); err != nil || len(decoded) != 32 || artifact.SHA256 != strings.ToLower(artifact.SHA256) {
			return fmt.Errorf("artifact %q sha256 must be 64 lowercase hex characters", artifact.Name)
		}
	}
	return nil
}

// Sign validates and signs a manifest.
func Sign(manifest Manifest, keyID string, key ed25519.PrivateKey) (Envelope, error) {
	if err := manifest.Validate(); err != nil {
		return Envelope{}, err
	}
	if strings.TrimSpace(keyID) == "" {
		return Envelope{}, errors.New("key id is required")
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{Manifest: payload, KeyID: keyID, Signature: ed25519.Sign(key, signed(payload))}, nil
}

// Verify checks the envelope signature against trusted keys and returns the
// validated manifest.
func (e Envelope) Verify(trusted map[string]ed25519.PublicKey) (Manifest, error) {
	key, ok := trusted[e.KeyID]
	if !ok {
		return Manifest{}, fmt.Errorf("release key %q is not trusted", e.KeyID)
	}
	if !ed25519.Verify(key, signed(e.Manifest), e.Signature) {
		return Manifest{}, errors.New("release manifest signature is invalid")
	}
	decoder := json.NewDecoder(bytes.NewReader(e.Manifest))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// ParseEnvelope decodes a signed envelope document.
func ParseEnvelope(document []byte) (Envelope, error) {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	var envelope Envelope
	if err := decoder.Decode(&envelope); err != nil {
		return Envelope{}, fmt.Errorf("decode release envelope: %w", err)
	}
	return envelope, nil
}

// ParsePrivateKey reads a PKCS#8 PEM Ed25519 private key.
func ParsePrivateKey(document []byte) (ed25519.PrivateKey, error) {
	block, _ := pem.Decode(document)
	if block == nil {
		return nil, errors.New("release signing key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse release signing key: %w", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("release signing key is not Ed25519")
	}
	return key, nil
}

func signed(payload []byte) []byte {
	return append([]byte(signingContext), payload...)
}

// Version is a parsed MAJOR.MINOR.PATCH[-PRERELEASE] version.
type Version struct {
	Major, Minor, Patch int
	Prerelease          string
}

// ParseVersion parses a semantic version without build metadata.
func ParseVersion(value string) (Version, error) {
	core, prerelease, _ := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return Version{}, fmt.Errorf("%q is not MAJOR.MINOR.PATCH", value)
	}
	numbers := make([]int, 3)
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return Version{}, fmt.Errorf("%q has an invalid numeric part", value)
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return Version{}, fmt.Errorf("%q has an invalid numeric part", value)
		}
		numbers[i] = n
	}
	if strings.Contains(value, "-") {
		if prerelease == "" {
			return Version{}, fmt.Errorf("%q has an empty prerelease", value)
		}
		for _, identifier := range strings.Split(prerelease, ".") {
			if identifier == "" || strings.Trim(identifier, "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ-") != "" {
				return Version{}, fmt.Errorf("%q has an invalid prerelease", value)
			}
		}
	}
	return Version{Major: numbers[0], Minor: numbers[1], Patch: numbers[2], Prerelease: prerelease}, nil
}

// CompareVersions orders two semantic versions; it returns an error when
// either does not parse.
func CompareVersions(a, b string) (int, error) {
	left, err := ParseVersion(a)
	if err != nil {
		return 0, err
	}
	right, err := ParseVersion(b)
	if err != nil {
		return 0, err
	}
	return left.Compare(right), nil
}

// Compare orders versions by semantic-version precedence.
func (v Version) Compare(other Version) int {
	for _, pair := range [][2]int{{v.Major, other.Major}, {v.Minor, other.Minor}, {v.Patch, other.Patch}} {
		if pair[0] != pair[1] {
			if pair[0] < pair[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case v.Prerelease == other.Prerelease:
		return 0
	case v.Prerelease == "":
		return 1
	case other.Prerelease == "":
		return -1
	}
	left, right := strings.Split(v.Prerelease, "."), strings.Split(other.Prerelease, ".")
	for i := 0; i < len(left) && i < len(right); i++ {
		if c := comparePrerelease(left[i], right[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(left) < len(right):
		return -1
	case len(left) > len(right):
		return 1
	}
	return 0
}

func comparePrerelease(a, b string) int {
	an, aErr := strconv.Atoi(a)
	bn, bErr := strconv.Atoi(b)
	switch {
	case aErr == nil && bErr == nil:
		switch {
		case an < bn:
			return -1
		case an > bn:
			return 1
		}
		return 0
	case aErr == nil:
		return -1
	case bErr == nil:
		return 1
	}
	return strings.Compare(a, b)
}
