package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

const (
	// SchemaVersion is the current release payload schema. Version 1.0
	// payloads are still accepted so already-published media releases remain
	// verifiable after the presentation union was introduced.
	SchemaVersion       = "1.1"
	LegacySchemaVersion = "1.0"
	ProtocolV1          = "1.0"
	DigestSHA256        = "sha256"
	DigestHexBytes      = 64
)

type PresentationKind string

const (
	PresentationMedia PresentationKind = "media"
	PresentationCable PresentationKind = "cable"
)

type MediaPresentation struct {
	Playback PlaybackPayload `json:"playback"`
}

// CablePresentation identifies the immutable shell and compiled revision. The
// closure is repeated in the presentation so a player can reject a release
// whose artifact list is incomplete before attempting activation.
type CablePresentation struct {
	ShellBuildDigest       string   `json:"shellBuildDigest"`
	RevisionArtifactDigest string   `json:"revisionArtifactDigest"`
	Entrypoint             string   `json:"entrypoint"`
	BridgeProtocol         string   `json:"bridgeProtocol"`
	ArtifactClosure        []string `json:"artifactClosure"`
}

// Presentation is the signed release discriminator. Exactly one variant must
// be populated, matching Kind.
type Presentation struct {
	Kind  PresentationKind   `json:"kind"`
	Media *MediaPresentation `json:"media,omitempty"`
	Cable *CablePresentation `json:"cable,omitempty"`
}

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
	SchemaVersion          string       `json:"schemaVersion"`
	MinimumProtocolVersion string       `json:"minimumProtocolVersion"`
	Presentation           Presentation `json:"presentation"`
	Artifacts              []Artifact   `json:"artifacts"`
	RequiredFeatures       []string     `json:"requiredFeatures"`

	// Playback is retained as an in-memory construction/decode field for
	// callers that still create legacy media payloads. It is never emitted in
	// current (1.1) JSON; Canonical promotes it to Presentation.Media.
	Playback PlaybackPayload `json:"-"`
	legacy   bool
}

type EnvelopeV1 struct {
	ID        string    `json:"id"`
	Digest    string    `json:"digest"`
	KeyID     string    `json:"keyId"`
	Signature string    `json:"signature"`
	Payload   PayloadV1 `json:"payload"`
}

func (p *PayloadV1) UnmarshalJSON(data []byte) error {
	type wire struct {
		SchemaVersion          string           `json:"schemaVersion"`
		MinimumProtocolVersion string           `json:"minimumProtocolVersion"`
		Presentation           *Presentation    `json:"presentation"`
		Playback               *PlaybackPayload `json:"playback"`
		Artifacts              []Artifact       `json:"artifacts"`
		RequiredFeatures       []string         `json:"requiredFeatures"`
	}
	var value wire
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	p.SchemaVersion = value.SchemaVersion
	p.MinimumProtocolVersion = value.MinimumProtocolVersion
	p.Artifacts = value.Artifacts
	p.RequiredFeatures = value.RequiredFeatures
	p.Playback = PlaybackPayload{}
	p.Presentation = Presentation{}
	p.legacy = value.Presentation == nil && value.Playback != nil
	if value.Presentation != nil {
		p.Presentation = *value.Presentation
		if p.Presentation.Kind == PresentationMedia && p.Presentation.Media != nil {
			p.Playback = p.Presentation.Media.Playback
		}
	}
	if value.Playback != nil {
		p.Playback = *value.Playback
		if p.Presentation.Kind == "" {
			p.Presentation = Presentation{Kind: PresentationMedia, Media: &MediaPresentation{Playback: *value.Playback}}
		}
	}
	return nil
}

func (p PayloadV1) MarshalJSON() ([]byte, error) {
	type wire struct {
		SchemaVersion          string           `json:"schemaVersion"`
		MinimumProtocolVersion string           `json:"minimumProtocolVersion"`
		Presentation           *Presentation    `json:"presentation,omitempty"`
		Playback               *PlaybackPayload `json:"playback,omitempty"`
		Artifacts              []Artifact       `json:"artifacts"`
		RequiredFeatures       []string         `json:"requiredFeatures"`
	}
	if p.legacy {
		playback := p.Playback
		return json.Marshal(wire{
			SchemaVersion: p.SchemaVersion, MinimumProtocolVersion: p.MinimumProtocolVersion,
			Playback: &playback, Artifacts: p.Artifacts, RequiredFeatures: p.RequiredFeatures,
		})
	}
	p = normalized(p)
	return json.Marshal(wire{
		SchemaVersion: p.SchemaVersion, MinimumProtocolVersion: p.MinimumProtocolVersion,
		Presentation: &p.Presentation, Artifacts: p.Artifacts, RequiredFeatures: p.RequiredFeatures,
	})
}

func Canonical(payload PayloadV1) ([]byte, error) {
	if payload.Presentation.Kind == PresentationMedia && payload.Presentation.Media != nil &&
		payload.Playback.AssetID != "" && !reflect.DeepEqual(payload.Playback, payload.Presentation.Media.Playback) {
		return nil, errors.New("media playback fields disagree")
	}
	canonical := normalized(payload)
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
	if envelope.Payload.SchemaVersion != SchemaVersion && envelope.Payload.SchemaVersion != LegacySchemaVersion {
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
	if err := validate(envelope.Payload); err != nil {
		return err
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

	// Promote the old in-memory media construction shape to the signed union.
	if payload.Presentation.Kind == "" && payload.Playback.AssetID != "" {
		payload.Presentation = Presentation{
			Kind:  PresentationMedia,
			Media: &MediaPresentation{Playback: payload.Playback},
		}
	}
	if payload.Presentation.Kind == PresentationMedia && payload.Presentation.Media != nil {
		media := *payload.Presentation.Media
		media.Playback.AllowedDeliveryMode = sortedUnique(media.Playback.AllowedDeliveryMode)
		payload.Presentation.Media = &media
		payload.Playback = media.Playback
	}
	if payload.Presentation.Kind == PresentationCable && payload.Presentation.Cable != nil {
		cable := *payload.Presentation.Cable
		cable.ArtifactClosure = sortedUnique(cable.ArtifactClosure)
		payload.Presentation.Cable = &cable
	}
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
	if payload.SchemaVersion != SchemaVersion && payload.SchemaVersion != LegacySchemaVersion {
		return fmt.Errorf("schemaVersion must be %q or %q", SchemaVersion, LegacySchemaVersion)
	}
	if payload.MinimumProtocolVersion == "" {
		return errors.New("minimumProtocolVersion is required")
	}
	presentation := payload.Presentation
	if presentation.Kind == "" && payload.Playback.AssetID != "" {
		presentation = Presentation{Kind: PresentationMedia, Media: &MediaPresentation{Playback: payload.Playback}}
	}
	if (presentation.Media == nil) == (presentation.Cable == nil) {
		return errors.New("exactly one release presentation variant is required")
	}
	switch presentation.Kind {
	case PresentationMedia:
		if presentation.Media == nil {
			return errors.New("media presentation is required")
		}
		if payload.Playback.AssetID != "" && !reflect.DeepEqual(payload.Playback, presentation.Media.Playback) {
			return errors.New("media playback fields disagree")
		}
		if err := validatePlayback(presentation.Media.Playback); err != nil {
			return err
		}
	case PresentationCable:
		if presentation.Cable == nil {
			return errors.New("cable presentation is required")
		}
		if err := validateCable(*presentation.Cable, payload.Artifacts); err != nil {
			return err
		}
	default:
		return fmt.Errorf("presentation kind must be %q or %q", PresentationMedia, PresentationCable)
	}
	if len(payload.Artifacts) == 0 {
		return errors.New("at least one artifact is required")
	}
	seen := make(map[string]struct{}, len(payload.Artifacts))
	for _, artifact := range payload.Artifacts {
		if err := ValidateDigest(artifact.Digest); err != nil {
			return err
		}
		if artifact.Size <= 0 || artifact.MediaType == "" {
			return errors.New("artifact size and mediaType are required")
		}
		if _, exists := seen[artifact.Digest]; exists {
			return fmt.Errorf("artifact %q is listed more than once", artifact.Digest)
		}
		seen[artifact.Digest] = struct{}{}
	}
	return nil
}

func validatePlayback(playback PlaybackPayload) error {
	if playback.AssetID == "" {
		return errors.New("assetId is required")
	}
	if playback.Fit != "fit" && playback.Fit != "fill" {
		return errors.New("fit must be fit or fill")
	}
	if playback.Volume < 0 || playback.Volume > 100 {
		return errors.New("volume must be between 0 and 100")
	}
	if len(playback.AllowedDeliveryMode) == 0 {
		return errors.New("at least one delivery mode is required")
	}
	for _, mode := range playback.AllowedDeliveryMode {
		if mode != "stream" && mode != "cache_then_play" {
			return fmt.Errorf("unsupported delivery mode %q", mode)
		}
	}
	return nil
}

func validateCable(cable CablePresentation, artifacts []Artifact) error {
	if err := ValidateDigest(cable.ShellBuildDigest); err != nil {
		return fmt.Errorf("shellBuildDigest: %w", err)
	}
	if err := ValidateDigest(cable.RevisionArtifactDigest); err != nil {
		return fmt.Errorf("revisionArtifactDigest: %w", err)
	}
	if cable.Entrypoint == "" {
		return errors.New("entrypoint is required")
	}
	if cable.BridgeProtocol == "" {
		return errors.New("bridgeProtocol is required")
	}
	if len(cable.ArtifactClosure) == 0 {
		return errors.New("artifactClosure is required")
	}
	closure := sortedUnique(cable.ArtifactClosure)
	if len(closure) != len(cable.ArtifactClosure) {
		return errors.New("artifactClosure must contain unique digests")
	}
	for _, digest := range closure {
		if err := ValidateDigest(digest); err != nil {
			return fmt.Errorf("artifactClosure: %w", err)
		}
	}
	if len(artifacts) != len(closure) {
		return errors.New("artifact closure does not match artifacts")
	}
	for index, artifact := range artifacts {
		if artifact.Digest != closure[index] {
			return errors.New("artifact closure does not match artifacts")
		}
	}
	if !slices.Contains(closure, cable.ShellBuildDigest) {
		return errors.New("shellBuildDigest is missing from artifactClosure")
	}
	if !slices.Contains(closure, cable.RevisionArtifactDigest) {
		return errors.New("revisionArtifactDigest is missing from artifactClosure")
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

// MediaPlayback returns the typed media playback settings, if this is a media
// presentation.
func (p PayloadV1) MediaPlayback() (PlaybackPayload, bool) {
	p = normalized(p)
	if p.Presentation.Kind != PresentationMedia || p.Presentation.Media == nil {
		return PlaybackPayload{}, false
	}
	return p.Presentation.Media.Playback, true
}

// AllowsDeliveryMode keeps assignment validation independent of a concrete
// presentation variant. Cable releases always require the cache path because
// their shell and revision are part of the immutable artifact closure.
func (p PayloadV1) AllowsDeliveryMode(mode string) bool {
	p = normalized(p)
	switch p.Presentation.Kind {
	case PresentationMedia:
		if p.Presentation.Media == nil {
			return false
		}
		return slices.Contains(p.Presentation.Media.Playback.AllowedDeliveryMode, mode)
	case PresentationCable:
		return mode == "cache_then_play"
	default:
		return false
	}
}

func (p PayloadV1) PresentationMode() PresentationKind {
	p = normalized(p)
	return p.Presentation.Kind
}
