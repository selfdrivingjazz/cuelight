package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

const (
	SchemaVersion  = "1.0"
	ProtocolV1     = "1.0"
	DigestSHA256   = "sha256"
	DigestHexBytes = 64
)

type Artifact struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}

type PlaybackPayload struct {
	AssetID             string   `json:"assetId"`
	Fit                 string   `json:"fit"`
	Loop                bool     `json:"loop"`
	Muted               bool     `json:"muted"`
	Volume              int      `json:"volume"`
	ImageDurationSecond int      `json:"imageDurationSeconds,omitempty"`
	Orientation         int      `json:"orientation"`
	AllowedDeliveryMode []string `json:"allowedDeliveryModes"`
}

type PayloadV1 struct {
	SchemaVersion          string          `json:"schemaVersion"`
	MinimumProtocolVersion string          `json:"minimumProtocolVersion"`
	Playback               PlaybackPayload `json:"playback"`
	Artifacts              []Artifact      `json:"artifacts"`
	RequiredFeatures       []string        `json:"requiredFeatures"`
}

type EnvelopeV1 struct {
	ID        string    `json:"id"`
	Digest    string    `json:"digest"`
	KeyID     string    `json:"keyId"`
	Signature string    `json:"signature"`
	Payload   PayloadV1 `json:"payload"`
}

func Canonical(payload PayloadV1) ([]byte, error) {
	canonical := payload
	canonical.Artifacts = slices.Clone(payload.Artifacts)
	slices.SortFunc(canonical.Artifacts, func(a, b Artifact) int {
		return strings.Compare(a.Digest, b.Digest)
	})
	canonical.RequiredFeatures = sortedUnique(payload.RequiredFeatures)
	canonical.Playback.AllowedDeliveryMode = sortedUnique(payload.Playback.AllowedDeliveryMode)
	if err := validate(canonical); err != nil {
		return nil, err
	}
	return json.Marshal(canonical)
}

func Digest(payload PayloadV1) (string, []byte, error) {
	canonical, err := Canonical(payload)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), canonical, nil
}

func Sign(id, keyID string, payload PayloadV1, privateKey ed25519.PrivateKey) (EnvelopeV1, error) {
	if id == "" || keyID == "" {
		return EnvelopeV1{}, errors.New("release ID and key ID are required")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return EnvelopeV1{}, errors.New("invalid Ed25519 private key")
	}
	digest, _, err := Digest(payload)
	if err != nil {
		return EnvelopeV1{}, err
	}
	signature := ed25519.Sign(privateKey, []byte(digest))
	return EnvelopeV1{
		ID:        id,
		Digest:    digest,
		KeyID:     keyID,
		Signature: base64.StdEncoding.EncodeToString(signature),
		Payload:   normalized(payload),
	}, nil
}

func Verify(envelope EnvelopeV1, expectedKeyID string, publicKey ed25519.PublicKey, playerProtocol string) error {
	if envelope.KeyID != expectedKeyID {
		return fmt.Errorf("unknown publisher key %q", envelope.KeyID)
	}
	if len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid Ed25519 public key")
	}
	if envelope.Payload.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported release schema %q", envelope.Payload.SchemaVersion)
	}
	playerVersion, err := parseVersion(playerProtocol)
	if err != nil {
		return fmt.Errorf("invalid player protocol: %w", err)
	}
	requiredVersion, err := parseVersion(envelope.Payload.MinimumProtocolVersion)
	if err != nil {
		return fmt.Errorf("invalid required protocol: %w", err)
	}
	if playerVersion[0] < requiredVersion[0] ||
		(playerVersion[0] == requiredVersion[0] && playerVersion[1] < requiredVersion[1]) {
		return fmt.Errorf("player protocol %q is older than required %q", playerProtocol, envelope.Payload.MinimumProtocolVersion)
	}
	digest, _, err := Digest(envelope.Payload)
	if err != nil {
		return err
	}
	if digest != envelope.Digest {
		return errors.New("release digest mismatch")
	}
	signature, err := base64.StdEncoding.DecodeString(envelope.Signature)
	if err != nil {
		return errors.New("invalid release signature encoding")
	}
	if !ed25519.Verify(publicKey, []byte(digest), signature) {
		return errors.New("invalid release signature")
	}
	return nil
}

func normalized(payload PayloadV1) PayloadV1 {
	payload.Artifacts = slices.Clone(payload.Artifacts)
	slices.SortFunc(payload.Artifacts, func(a, b Artifact) int {
		return strings.Compare(a.Digest, b.Digest)
	})
	payload.RequiredFeatures = sortedUnique(payload.RequiredFeatures)
	payload.Playback.AllowedDeliveryMode = sortedUnique(payload.Playback.AllowedDeliveryMode)
	return payload
}

func sortedUnique(values []string) []string {
	result := slices.Clone(values)
	slices.Sort(result)
	return slices.Compact(result)
}

func parseVersion(value string) ([2]int, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return [2]int{}, errors.New("version must be major.minor")
	}
	var version [2]int
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return [2]int{}, errors.New("version must contain non-negative integers")
		}
		version[index] = number
	}
	return version, nil
}

func validate(payload PayloadV1) error {
	if payload.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schemaVersion must be %q", SchemaVersion)
	}
	if payload.MinimumProtocolVersion == "" {
		return errors.New("minimumProtocolVersion is required")
	}
	if payload.Playback.AssetID == "" {
		return errors.New("assetId is required")
	}
	if payload.Playback.Fit != "fit" && payload.Playback.Fit != "fill" {
		return errors.New("fit must be fit or fill")
	}
	if payload.Playback.Volume < 0 || payload.Playback.Volume > 100 {
		return errors.New("volume must be between 0 and 100")
	}
	if len(payload.Playback.AllowedDeliveryMode) == 0 {
		return errors.New("at least one delivery mode is required")
	}
	for _, mode := range payload.Playback.AllowedDeliveryMode {
		if mode != "stream" && mode != "cache_then_play" {
			return fmt.Errorf("unsupported delivery mode %q", mode)
		}
	}
	if len(payload.Artifacts) == 0 {
		return errors.New("at least one artifact is required")
	}
	for _, artifact := range payload.Artifacts {
		if err := ValidateDigest(artifact.Digest); err != nil {
			return err
		}
		if artifact.Size <= 0 || artifact.MediaType == "" {
			return errors.New("artifact size and mediaType are required")
		}
	}
	return nil
}

func ValidateDigest(digest string) error {
	if len(digest) != DigestHexBytes || strings.ToLower(digest) != digest {
		return errors.New("digest must be 64 lowercase hexadecimal characters")
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return errors.New("digest must be 64 lowercase hexadecimal characters")
	}
	return nil
}
