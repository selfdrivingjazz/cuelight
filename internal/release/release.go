package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
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
	PresentationMedia         PresentationKind = "media"
	PresentationMediaSchedule PresentationKind = "media-schedule"
	PresentationWeb           PresentationKind = "web"

	// FeatureWeb is the required feature for releases that render live web
	// pages (web presentations and media schedules with web items).
	FeatureWeb = "web"
)

type MediaPresentation struct {
	Playback PlaybackPayload `json:"playback"`
}

// MediaScheduleWindow bounds a schedule using UTC timestamps. Timezone is
// retained separately for operators and does not change timestamp semantics.
type MediaScheduleWindow struct {
	StartsAt string `json:"startsAt"`
	EndsAt   string `json:"endsAt"`
}

// MediaScheduleItem is an immutable, timestamped occurrence. File items
// resolve their source from ArtifactDigest after verification; web items
// carry Web and render the page for the item's duration.
type MediaScheduleItem struct {
	ID                  string                `json:"id"`
	StartsAt            string                `json:"startsAt"`
	EndsAt              string                `json:"endsAt"`
	ArtifactDigest      string                `json:"artifactDigest,omitempty"`
	MediaType           string                `json:"mediaType,omitempty"`
	Fit                 string                `json:"fit,omitempty"`
	Loop                bool                  `json:"loop"`
	Muted               bool                  `json:"muted"`
	Volume              int                   `json:"volume"`
	ImageDurationSecond int                   `json:"imageDurationSeconds,omitempty"`
	Orientation         int                   `json:"orientation"`
	Web                 *MediaScheduleWebItem `json:"web,omitempty"`
}

// MediaScheduleWebItem is a live web page scheduled inside a media schedule.
type MediaScheduleWebItem struct {
	URL string `json:"url"`
}

// MediaSchedulePresentation is the signed MPV-compatible presentation form.
// ArtifactClosure must contain exactly the payload artifacts, allowing a node
type MediaSchedulePresentation struct {
	Timezone             string              `json:"timezone"`
	Window               MediaScheduleWindow `json:"window"`
	Items                []MediaScheduleItem `json:"items"`
	ArtifactClosure      []string            `json:"artifactClosure"`
	AllowedDeliveryModes []string            `json:"allowedDeliveryModes"`
}

// MediaSchedule is retained as a concise alias for callers constructing
// presentation payloads.
type MediaSchedule = MediaSchedulePresentation

// WebPresentation is a live website shown by a Chromium renderer and operated
// through the remote input stream. It carries no artifacts. AppAccess pages
// (MediaID names their web media item) are opened with an app token instead
// and consume the remote input stream themselves.
type WebPresentation struct {
	URL       string `json:"url"`
	AppAccess bool   `json:"appAccess,omitempty"`
	MediaID   string `json:"mediaId,omitempty"`
}

// Presentation is the signed release discriminator. Exactly one variant must
// be populated, matching Kind.
type Presentation struct {
	Kind          PresentationKind           `json:"kind"`
	Media         *MediaPresentation         `json:"media,omitempty"`
	MediaSchedule *MediaSchedulePresentation `json:"mediaSchedule,omitempty"`
	Web           *WebPresentation           `json:"web,omitempty"`
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
	if payload.Presentation.Kind == PresentationMediaSchedule && payload.Presentation.MediaSchedule != nil {
		schedule := *payload.Presentation.MediaSchedule
		schedule.ArtifactClosure = sortedUnique(schedule.ArtifactClosure)
		schedule.AllowedDeliveryModes = sortedUnique(schedule.AllowedDeliveryModes)
		if value, err := time.Parse(time.RFC3339Nano, schedule.Window.StartsAt); err == nil {
			schedule.Window.StartsAt = value.UTC().Format(time.RFC3339Nano)
		}
		if value, err := time.Parse(time.RFC3339Nano, schedule.Window.EndsAt); err == nil {
			schedule.Window.EndsAt = value.UTC().Format(time.RFC3339Nano)
		}
		schedule.Items = slices.Clone(schedule.Items)
		for index := range schedule.Items {
			if value, err := time.Parse(time.RFC3339Nano, schedule.Items[index].StartsAt); err == nil {
				schedule.Items[index].StartsAt = value.UTC().Format(time.RFC3339Nano)
			}
			if value, err := time.Parse(time.RFC3339Nano, schedule.Items[index].EndsAt); err == nil {
				schedule.Items[index].EndsAt = value.UTC().Format(time.RFC3339Nano)
			}
		}
		slices.SortFunc(schedule.Items, func(a, b MediaScheduleItem) int {
			if result := strings.Compare(a.StartsAt, b.StartsAt); result != 0 {
				return result
			}
			return strings.Compare(a.ID, b.ID)
		})
		payload.Presentation.MediaSchedule = &schedule
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
	var variants int
	if presentation.Media != nil {
		variants++
	}
	if presentation.MediaSchedule != nil {
		variants++
	}
	if presentation.Web != nil {
		variants++
	}
	if variants != 1 {
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
	case PresentationMediaSchedule:
		if presentation.MediaSchedule == nil {
			return errors.New("media schedule presentation is required")
		}
		if err := validateMediaSchedule(*presentation.MediaSchedule, payload.Artifacts, payload.RequiredFeatures); err != nil {
			return err
		}
		if len(payload.Artifacts) == 0 {
			return nil
		}
	case PresentationWeb:
		if presentation.Web == nil {
			return errors.New("web presentation is required")
		}
		if err := ValidateWebURL(presentation.Web.URL); err != nil {
			return err
		}
		if presentation.Web.AppAccess != (presentation.Web.MediaID != "") {
			return errors.New("web appAccess and mediaId must be set together")
		}
		if len(payload.Artifacts) != 0 {
			return errors.New("web releases must not contain artifacts")
		}
		return nil
	default:
		return fmt.Errorf("presentation kind must be %q, %q, or %q", PresentationMedia, PresentationMediaSchedule, PresentationWeb)
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

// ValidateWebURL accepts absolute http(s) URLs without userinfo.
func ValidateWebURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("web url must be an absolute http or https URL")
	}
	if parsed.User != nil {
		return errors.New("web url must not contain userinfo")
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

func validateMediaSchedule(schedule MediaSchedulePresentation, artifacts []Artifact, requiredFeatures []string) error {
	if schedule.Timezone == "" {
		return errors.New("timezone is required")
	}
	if _, err := time.LoadLocation(schedule.Timezone); err != nil {
		return fmt.Errorf("invalid timezone: %w", err)
	}
	windowStart, err := time.Parse(time.RFC3339Nano, schedule.Window.StartsAt)
	if err != nil {
		return fmt.Errorf("window startsAt: %w", err)
	}
	windowEnd, err := time.Parse(time.RFC3339Nano, schedule.Window.EndsAt)
	if err != nil {
		return fmt.Errorf("window endsAt: %w", err)
	}
	if !windowStart.Before(windowEnd) {
		return errors.New("schedule window must be non-empty")
	}
	if len(schedule.Items) == 0 {
		return errors.New("schedule items are required")
	}
	if len(schedule.AllowedDeliveryModes) == 0 {
		return errors.New("at least one delivery mode is required")
	}
	for _, mode := range schedule.AllowedDeliveryModes {
		if mode != "stream" && mode != "cache_then_play" {
			return fmt.Errorf("unsupported delivery mode %q", mode)
		}
	}
	artifactTypes := make(map[string]string, len(artifacts))
	for _, artifact := range artifacts {
		artifactTypes[artifact.Digest] = artifact.MediaType
	}
	seenItems := make(map[string]struct{}, len(schedule.Items))
	var previousEnd time.Time
	for index, item := range schedule.Items {
		if item.ID == "" {
			return errors.New("schedule item id is required")
		}
		if _, exists := seenItems[item.ID]; exists {
			return fmt.Errorf("schedule item %q is listed more than once", item.ID)
		}
		seenItems[item.ID] = struct{}{}
		start, parseErr := time.Parse(time.RFC3339Nano, item.StartsAt)
		if parseErr != nil {
			return fmt.Errorf("item %q startsAt: %w", item.ID, parseErr)
		}
		end, parseErr := time.Parse(time.RFC3339Nano, item.EndsAt)
		if parseErr != nil {
			return fmt.Errorf("item %q endsAt: %w", item.ID, parseErr)
		}
		if !start.Before(end) || start.Before(windowStart) || end.After(windowEnd) {
			return fmt.Errorf("item %q is outside the schedule window", item.ID)
		}
		if index == 0 && !start.Equal(windowStart) {
			return errors.New("schedule must begin at the window start")
		}
		if index > 0 && start.Before(previousEnd) {
			return fmt.Errorf("schedule items overlap at %q", item.ID)
		}
		if index > 0 && !start.Equal(previousEnd) {
			return fmt.Errorf("schedule has a gap before %q", item.ID)
		}
		previousEnd = end
		if item.Web != nil {
			if err := ValidateWebURL(item.Web.URL); err != nil {
				return fmt.Errorf("item %q: %w", item.ID, err)
			}
			if item.ArtifactDigest != "" || item.MediaType != "" {
				return fmt.Errorf("item %q web items must not reference an artifact", item.ID)
			}
			if !slices.Contains(requiredFeatures, FeatureWeb) {
				return fmt.Errorf("item %q is a web item but requiredFeatures omits %q", item.ID, FeatureWeb)
			}
			continue
		}
		if err := ValidateDigest(item.ArtifactDigest); err != nil {
			return fmt.Errorf("item %q artifactDigest: %w", item.ID, err)
		}
		mediaType := item.MediaType
		if mediaType != "video/mp4" && mediaType != "image/jpeg" && mediaType != "image/png" && mediaType != "image/webp" {
			return fmt.Errorf("item %q has unsupported media type %q", item.ID, mediaType)
		}
		if artifactTypes[item.ArtifactDigest] != mediaType {
			return fmt.Errorf("item %q media type does not match artifact", item.ID)
		}
		if item.Fit != "fit" && item.Fit != "fill" {
			return fmt.Errorf("item %q fit must be fit or fill", item.ID)
		}
		if item.Volume < 0 || item.Volume > 100 {
			return fmt.Errorf("item %q volume must be between 0 and 100", item.ID)
		}
		switch item.Orientation {
		case 0, 90, 180, 270:
		default:
			return fmt.Errorf("item %q orientation is invalid", item.ID)
		}
	}
	if !previousEnd.Equal(windowEnd) {
		return errors.New("schedule must end at the window end")
	}
	closure := sortedUnique(schedule.ArtifactClosure)
	if len(closure) != len(schedule.ArtifactClosure) {
		return errors.New("artifactClosure must contain unique digests")
	}
	if len(closure) != len(artifacts) {
		return errors.New("artifact closure does not match artifacts")
	}
	for index, artifact := range artifacts {
		if artifact.Digest != closure[index] {
			return errors.New("artifact closure does not match artifacts")
		}
	}
	for _, item := range schedule.Items {
		if item.Web == nil && !slices.Contains(closure, item.ArtifactDigest) {
			return fmt.Errorf("schedule item %q artifact is missing from artifactClosure", item.ID)
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

// MediaPlayback returns the typed media playback settings, if this is a media
// presentation.
func (p PayloadV1) MediaPlayback() (PlaybackPayload, bool) {
	p = normalized(p)
	if p.Presentation.Kind != PresentationMedia || p.Presentation.Media == nil {
		return PlaybackPayload{}, false
	}
	return p.Presentation.Media.Playback, true
}

// MediaSchedulePlayback returns the immutable schedule when this is a media
// schedule presentation.
func (p PayloadV1) MediaSchedulePlayback() (MediaSchedulePresentation, bool) {
	p = normalized(p)
	if p.Presentation.Kind != PresentationMediaSchedule || p.Presentation.MediaSchedule == nil {
		return MediaSchedulePresentation{}, false
	}
	return *p.Presentation.MediaSchedule, true
}

// WebPresentation returns the web presentation, if this is a web release.
func (p PayloadV1) WebPresentation() (WebPresentation, bool) {
	if p.Presentation.Kind != PresentationWeb || p.Presentation.Web == nil {
		return WebPresentation{}, false
	}
	return *p.Presentation.Web, true
}

func (p PayloadV1) AllowsDeliveryMode(mode string) bool {
	p = normalized(p)
	switch p.Presentation.Kind {
	case PresentationMedia:
		return p.Presentation.Media != nil && slices.Contains(p.Presentation.Media.Playback.AllowedDeliveryMode, mode)
	case PresentationMediaSchedule:
		return p.Presentation.MediaSchedule != nil && slices.Contains(p.Presentation.MediaSchedule.AllowedDeliveryModes, mode)
	case PresentationWeb:
		return mode == "stream"
	default:
		return false
	}
}

func (p PayloadV1) PresentationMode() PresentationKind {
	p = normalized(p)
	return p.Presentation.Kind
}
