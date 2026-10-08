// Package fixtures loads and validates Cable's versioned, deterministic fixtures.
package fixtures

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strings"

	embedded "github.com/jmill/cable/fixtures"
)

const (
	Version      = "v1"
	ManifestName = Version + "/manifest.json"
)

var digestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

var ErrNotAlgorithmic = errors.New("fixture is checked by digest rather than an in-process generator")
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Provenance records the redistribution and licensing status of fixture data.
type Provenance struct {
	Kind                  string `json:"kind"`
	Source                string `json:"source"`
	SourceURL             string `json:"sourceUrl"`
	License               string `json:"license"`
	LicenseURL            string `json:"licenseUrl"`
	RedistributionAllowed bool   `json:"redistributionAllowed"`
	Modifications         string `json:"modifications"`
}

// Compatibility describes what a media probe is expected to recognize.
type Compatibility struct {
	Valid         bool   `json:"valid"`
	Decoder       string `json:"decoder"`
	Compatibility string `json:"compatibility"`
}

// MediaFixture is a manifest entry and its content identity.
type MediaFixture struct {
	Name      string        `json:"name"`
	Path      string        `json:"path"`
	Kind      string        `json:"kind"`
	MediaType string        `json:"mediaType"`
	Size      int64         `json:"size"`
	SHA256    string        `json:"sha256"`
	Expected  Compatibility `json:"expected"`
}

// Manifest is the complete versioned fixture index.
type Manifest struct {
	SchemaVersion  string         `json:"schemaVersion"`
	FixtureVersion string         `json:"fixtureVersion"`
	GeneratedAt    string         `json:"generatedAt"`
	Generator      string         `json:"generator"`
	Provenance     Provenance     `json:"provenance"`
	Media          []MediaFixture `json:"media"`
	API            []string       `json:"api"`
	Scenarios      []string       `json:"scenarios"`
}

// Request and Response are intentionally typed at the transport boundary while
// retaining JSON bodies so callers can decode each OpenAPI shape into its own
// strongly typed value.
type APIRequest struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Security string            `json:"security"`
	Headers  map[string]string `json:"headers,omitempty"`
	Body     json.RawMessage   `json:"body,omitempty"`
}

// The following structs mirror the OpenAPI component schemas. Keeping them
// here makes fixture consumers independent of map[string]any and catches
// accidental contract drift when fixture JSON is edited.
type Health struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Instance      string `json:"instance,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
}
type AdminToken struct {
	ID      string   `json:"id"`
	Prefix  string   `json:"prefix"`
	Scopes  []string `json:"scopes"`
	Revoked bool     `json:"revoked"`
}
type EnrollmentTokenCreated struct {
	ID        string `json:"id"`
	Secret    string `json:"secret"`
	ExpiresAt string `json:"expiresAt"`
}
type Node struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	CredentialState    string `json:"credentialState"`
	DesiredGeneration  int64  `json:"desiredGeneration,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}
type UploadCreate struct {
	Name      string `json:"name"`
	Length    int64  `json:"length"`
	MediaType string `json:"mediaType"`
}
type Upload struct {
	ID     string `json:"id"`
	Length int64  `json:"length"`
	Offset int64  `json:"offset"`
	State  string `json:"state"`
}
type Artifact struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
}
type Asset struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Artifact Artifact `json:"artifact"`
}
type PlaybackPayload struct {
	AssetID              string   `json:"assetId"`
	Fit                  string   `json:"fit"`
	Loop                 bool     `json:"loop"`
	Muted                bool     `json:"muted"`
	Volume               int      `json:"volume"`
	ImageDurationSeconds int      `json:"imageDurationSeconds,omitempty"`
	Orientation          int      `json:"orientation"`
	AllowedDeliveryModes []string `json:"allowedDeliveryModes"`
}
type PublishReleaseInput struct {
	Playback PlaybackPayload `json:"playback"`
}
type ReleasePayload struct {
	SchemaVersion          string          `json:"schemaVersion"`
	MinimumProtocolVersion string          `json:"minimumProtocolVersion"`
	Playback               PlaybackPayload `json:"playback"`
	Artifacts              []Artifact      `json:"artifacts"`
	RequiredFeatures       []string        `json:"requiredFeatures"`
}
type ReleaseEnvelope struct {
	ID        string         `json:"id"`
	Digest    string         `json:"digest"`
	KeyID     string         `json:"keyId"`
	Signature string         `json:"signature"`
	Payload   ReleasePayload `json:"payload"`
}
type AssignmentInput struct {
	MediaID            string `json:"mediaId,omitempty"`
	PlaylistID         string `json:"playlistId,omitempty"`
	DeliveryMode       string `json:"deliveryMode,omitempty"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
	RemoteInput        bool   `json:"remoteInput"`
	ShowQR             bool   `json:"showQr"`
}
type Assignment struct {
	AssignmentInput
	ID                string `json:"id"`
	NodeID            string `json:"nodeId"`
	ReleaseID         string `json:"releaseId"`
	ReleaseDigest     string `json:"releaseDigest"`
	DesiredGeneration int64  `json:"desiredGeneration"`
}
type EnrollPlayerInput struct {
	Token     string `json:"token"`
	PublicKey string `json:"publicKey"`
}
type ClientCredential struct {
	Certificate string `json:"certificate"`
	ExpiresAt   string `json:"expiresAt"`
}
type EnrollPlayerOutput struct {
	ClientCredential
	NodeID             string `json:"nodeId"`
	EventEpoch         string `json:"eventEpoch"`
	PublisherKeyID     string `json:"publisherKeyId"`
	PublisherPublicKey string `json:"publisherPublicKey"`
}
type CapabilityReport struct {
	Platform        string `json:"platform"`
	Architecture    string `json:"architecture"`
	ProtocolVersion string `json:"protocolVersion"`
	RendererVersion string `json:"rendererVersion"`
	ProfileID       string `json:"profileId"`
	FreeBytes       int64  `json:"freeBytes,omitempty"`
	OutputReady     bool   `json:"outputReady,omitempty"`
	ClockReliable   bool   `json:"clockReliable,omitempty"`
}
type Condition struct {
	Type    string `json:"type"`
	Status  bool   `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message,omitempty"`
}
type NodeReport struct {
	EventEpoch                 string      `json:"eventEpoch"`
	Sequence                   int64       `json:"sequence"`
	DesiredGenerationSeen      int64       `json:"desiredGenerationSeen"`
	ObservedGeneration         int64       `json:"observedGeneration"`
	RenderingReleaseDigest     *string     `json:"renderingReleaseDigest"`
	CandidateReleaseDigest     *string     `json:"candidateReleaseDigest"`
	HealthyReleaseDigest       *string     `json:"healthyReleaseDigest"`
	LastKnownGoodReleaseDigest *string     `json:"lastKnownGoodReleaseDigest"`
	Conditions                 []Condition `json:"conditions"`
}
type NodeEvent struct {
	ID            string         `json:"id"`
	Epoch         string         `json:"epoch"`
	Sequence      int64          `json:"sequence"`
	Type          string         `json:"type"`
	OccurredAt    string         `json:"occurredAt"`
	CoversThrough int64          `json:"coversThrough,omitempty"`
	Data          map[string]any `json:"data,omitempty"`
}

// DecodeBody decodes an API fixture body into the caller's schema type.
func DecodeBody[T any](fixture APIFixture, response bool) (T, error) {
	var value T
	body := fixture.Request.Body
	if response {
		body = fixture.Response.Body
	}
	if len(body) == 0 || bytes.Equal(body, []byte("null")) {
		return value, errors.New("fixture body is null")
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return value, err
	}
	return value, nil
}

type APIResponse struct {
	Status    int               `json:"status"`
	MediaType string            `json:"mediaType"`
	Headers   map[string]string `json:"headers,omitempty"`
	Body      json.RawMessage   `json:"body"`
}

// APIFixture is a typed request/response fixture for a public API operation.
type APIFixture struct {
	SchemaVersion string      `json:"schemaVersion"`
	Operation     string      `json:"operation"`
	Request       APIRequest  `json:"request"`
	Response      APIResponse `json:"response"`
}

// ScenarioStep is a deterministic action in a simulator scenario.
type ScenarioStep struct {
	Action         string `json:"action"`
	Fixture        string `json:"fixture,omitempty"`
	DeliveryMode   string `json:"deliveryMode,omitempty"`
	Generation     int64  `json:"generation,omitempty"`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	Target         string `json:"target,omitempty"`
	Condition      string `json:"condition,omitempty"`
	AfterBytes     int64  `json:"afterBytes,omitempty"`
	Count          int    `json:"count,omitempty"`
	FreeBytes      int64  `json:"freeBytes,omitempty"`
	Ready          *bool  `json:"ready,omitempty"`
}

// ScenarioExpectation captures observable terminal state and invariants.
type ScenarioExpectation struct {
	Terminal               string   `json:"terminal"`
	Error                  string   `json:"error,omitempty"`
	Errors                 []string `json:"errors,omitempty"`
	ObservedGeneration     int64    `json:"observedGeneration,omitempty"`
	DeliveryModes          []string `json:"deliveryModes,omitempty"`
	SameResource           bool     `json:"sameResource,omitempty"`
	SameGeneration         bool     `json:"sameGeneration,omitempty"`
	StateUnchanged         bool     `json:"stateUnchanged,omitempty"`
	LastKnownGoodPreserved bool     `json:"lastKnownGoodPreserved,omitempty"`
	Resumed                bool     `json:"resumed,omitempty"`
	PartialNeverActivated  bool     `json:"partialNeverActivated,omitempty"`
	OverflowDetected       bool     `json:"overflowDetected,omitempty"`
	GapRecovered           bool     `json:"gapRecovered,omitempty"`
	BoundedEventCount      int      `json:"boundedEventCount,omitempty"`
	PartialCleaned         bool     `json:"partialCleaned,omitempty"`
	RenderingReleaseDigest *string  `json:"renderingReleaseDigest"`
	ControllerUnavailable  bool     `json:"controllerUnavailable,omitempty"`
	Recovered              bool     `json:"recovered,omitempty"`
	InitialError           string   `json:"initialError,omitempty"`
	Cleanup                string   `json:"cleanup"`
}

// Scenario is a typed, deterministic process/simulator scenario.
type Scenario struct {
	SchemaVersion string              `json:"schemaVersion"`
	Name          string              `json:"name"`
	Description   string              `json:"description"`
	Steps         []ScenarioStep      `json:"steps"`
	Expected      ScenarioExpectation `json:"expected"`
}

// Catalog contains validated manifest data and an immutable fixture filesystem.
type Catalog struct {
	Manifest Manifest
	FS       fs.FS
	media    map[string]MediaFixture
}

// Load returns the embedded v1 catalog after validating every checked-in byte.
func Load() (*Catalog, error) { return LoadFS(embedded.FS) }

// LoadFS validates a versioned fixture filesystem. It is exported for tests and
// downstream tools that mirror the package's fixture layout.
func LoadFS(files fs.FS) (*Catalog, error) {
	catalog := &Catalog{FS: files}
	manifestBytes, err := fs.ReadFile(files, ManifestName)
	if err != nil {
		return nil, fmt.Errorf("read fixture manifest: %w", err)
	}
	if err := decodeStrict(manifestBytes, &catalog.Manifest); err != nil {
		return nil, fmt.Errorf("decode fixture manifest: %w", err)
	}
	if err := validateManifest(catalog.Manifest); err != nil {
		return nil, err
	}
	catalog.media = make(map[string]MediaFixture, len(catalog.Manifest.Media))
	for _, item := range catalog.Manifest.Media {
		if _, exists := catalog.media[item.Name]; exists {
			return nil, fmt.Errorf("duplicate media fixture %q", item.Name)
		}
		catalog.media[item.Name] = item
		data, err := fs.ReadFile(files, item.Path)
		if err != nil {
			return nil, fmt.Errorf("read media fixture %q: %w", item.Name, err)
		}
		if err := validateMediaBytes(item, data); err != nil {
			return nil, err
		}
	}
	for _, name := range catalog.Manifest.API {
		if err := catalog.validateAPI(name); err != nil {
			return nil, err
		}
	}
	for _, name := range catalog.Manifest.Scenarios {
		if err := catalog.validateScenario(name); err != nil {
			return nil, err
		}
	}
	return catalog, nil
}

// Validate checks the embedded package and returns the first actionable error.
func Validate() error { _, err := Load(); return err }

func validateManifest(m Manifest) error {
	if m.SchemaVersion != "1.0" || m.FixtureVersion != Version {
		return fmt.Errorf("unsupported fixture manifest version %q/%q", m.SchemaVersion, m.FixtureVersion)
	}
	if m.GeneratedAt == "" || m.Generator == "" {
		return errors.New("fixture manifest is missing deterministic generation metadata")
	}
	if m.Provenance.License != "0BSD" || !m.Provenance.RedistributionAllowed || m.Provenance.Source == "" || m.Provenance.Modifications == "" {
		return errors.New("fixture provenance must explicitly permit redistribution and identify modifications")
	}
	if len(m.Media) == 0 || len(m.API) == 0 || len(m.Scenarios) == 0 {
		return errors.New("fixture manifest must contain media, api, and scenarios")
	}
	for _, name := range append(slices.Clone(m.API), m.Scenarios...) {
		if !namePattern.MatchString(name) && !strings.HasSuffix(name, ".json") {
			return fmt.Errorf("invalid fixture name %q", name)
		}
	}
	return nil
}

func validateMediaBytes(item MediaFixture, data []byte) error {
	if !namePattern.MatchString(item.Name) || path.Base(item.Path) != item.Name || !strings.HasPrefix(item.Path, Version+"/media/") {
		return fmt.Errorf("invalid media fixture path/name %q", item.Name)
	}
	if item.Size <= 0 || int64(len(data)) != item.Size {
		return fmt.Errorf("fixture %q size is %d, manifest says %d", item.Name, len(data), item.Size)
	}
	if !digestPattern.MatchString(item.SHA256) {
		return fmt.Errorf("fixture %q has invalid SHA-256", item.Name)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != item.SHA256 {
		return fmt.Errorf("fixture %q SHA-256 mismatch", item.Name)
	}
	if err := validateSignature(item, data); err != nil {
		return err
	}
	return nil
}

func validateSignature(item MediaFixture, data []byte) error {
	valid := true
	switch item.Expected.Decoder {
	case "jpeg":
		valid = len(data) >= 4 && data[0] == 0xff && data[1] == 0xd8 && data[len(data)-2] == 0xff && data[len(data)-1] == 0xd9
	case "png":
		valid = len(data) >= 24 && bytes.Equal(data[:8], []byte{137, 80, 78, 71, 13, 10, 26, 10}) && bytes.Equal(data[len(data)-8:], []byte{'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82})
	case "h264-aac":
		valid = validISOBoxes(data)
	case "webm":
		valid = len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1a, 0x45, 0xdf, 0xa3})
	default:
		return fmt.Errorf("fixture %q has unknown decoder %q", item.Name, item.Expected.Decoder)
	}
	if item.Expected.Valid != valid {
		return fmt.Errorf("fixture %q decoder expectation is %v, observed %v", item.Name, item.Expected.Valid, valid)
	}
	return nil
}

func validISOBoxes(data []byte) bool {
	foundFTYP, foundMOOV, foundMDAT := false, false, false
	for len(data) >= 8 {
		size := int(data[0])<<24 | int(data[1])<<16 | int(data[2])<<8 | int(data[3])
		if size < 8 || size > len(data) {
			return false
		}
		switch string(data[4:8]) {
		case "ftyp":
			foundFTYP = true
		case "moov":
			foundMOOV = true
		case "mdat":
			foundMDAT = true
		}
		data = data[size:]
	}
	return len(data) == 0 && foundFTYP && foundMOOV && foundMDAT
}

func (c *Catalog) validateAPI(name string) error {
	if !strings.HasSuffix(name, ".json") {
		return fmt.Errorf("API fixture %q must be JSON", name)
	}
	data, err := fs.ReadFile(c.FS, Version+"/api/"+name)
	if err != nil {
		return fmt.Errorf("read API fixture %q: %w", name, err)
	}
	var fixture APIFixture
	if err := decodeStrict(data, &fixture); err != nil {
		return fmt.Errorf("decode API fixture %q: %w", name, err)
	}
	if fixture.SchemaVersion != "1.0" || fixture.Operation == "" || fixture.Request.Method == "" || !strings.HasPrefix(fixture.Request.Path, "/") || fixture.Request.Security == "" {
		return fmt.Errorf("API fixture %q has incomplete typed request", name)
	}
	if fixture.Response.Status < 100 || fixture.Response.Status > 599 {
		return fmt.Errorf("API fixture %q has invalid response status", name)
	}
	if fixture.Response.Status == 204 && string(fixture.Response.Body) != "null" {
		return fmt.Errorf("API fixture %q 204 response must have null body", name)
	}
	if fixture.Response.Status != 204 && fixture.Response.MediaType == "" {
		return fmt.Errorf("API fixture %q is missing response media type", name)
	}
	return validateAPIBody(fixture.Operation, fixture.Request.Body, fixture.Response.Body)
}

func validateAPIBody(operation string, request, response json.RawMessage) error {
	if len(response) == 0 {
		return errors.New("API response body is absent")
	}
	var value any
	if err := json.Unmarshal(response, &value); err != nil {
		return fmt.Errorf("API response body is not JSON: %w", err)
	}
	if operation == "getHealth" {
		var v struct {
			Status  string `json:"status"`
			Version string `json:"version"`
		}
		if json.Unmarshal(response, &v) != nil || v.Status != "ok" || v.Version == "" {
			return errors.New("health fixture has invalid typed body")
		}
	}
	if operation == "createUpload" {
		var v struct {
			Name      string `json:"name"`
			Length    int64  `json:"length"`
			MediaType string `json:"mediaType"`
		}
		if json.Unmarshal(request, &v) != nil || v.Name == "" || v.Length <= 0 || v.MediaType == "" {
			return errors.New("upload fixture has invalid typed body")
		}
	}
	return nil
}

func (c *Catalog) validateScenario(name string) error {
	data, err := fs.ReadFile(c.FS, Version+"/scenarios/"+name+".json")
	if err != nil {
		return fmt.Errorf("read scenario fixture %q: %w", name, err)
	}
	var scenario Scenario
	if err := decodeStrict(data, &scenario); err != nil {
		return fmt.Errorf("decode scenario fixture %q: %w", name, err)
	}
	if scenario.SchemaVersion != "1.0" || scenario.Name != name || scenario.Description == "" || len(scenario.Steps) == 0 || scenario.Expected.Terminal == "" || scenario.Expected.Cleanup != "deterministic" {
		return fmt.Errorf("scenario fixture %q has incomplete typed contract", name)
	}
	for _, step := range scenario.Steps {
		if step.Action == "" {
			return fmt.Errorf("scenario %q has empty action", name)
		}
		if step.Fixture != "" {
			if _, ok := c.media[step.Fixture]; !ok {
				return fmt.Errorf("scenario %q references unknown media %q", name, step.Fixture)
			}
		}
		if step.DeliveryMode != "" && step.DeliveryMode != "stream" && step.DeliveryMode != "cache_then_play" {
			return fmt.Errorf("scenario %q has invalid delivery mode", name)
		}
	}
	return nil
}

func decodeStrict(data []byte, target any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing JSON")
		}
		return err
	}
	return nil
}

// Media returns validated metadata by stable fixture name.
func (c *Catalog) Media(name string) (MediaFixture, bool) { item, ok := c.media[name]; return item, ok }

// Bytes returns a fresh copy of a validated media fixture.
func (c *Catalog) Bytes(name string) ([]byte, error) {
	item, ok := c.media[name]
	if !ok {
		return nil, fmt.Errorf("unknown media fixture %q", name)
	}
	data, err := fs.ReadFile(c.FS, item.Path)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(data), nil
}

// API decodes a named typed API fixture.
func (c *Catalog) API(name string) (APIFixture, error) {
	var out APIFixture
	data, err := fs.ReadFile(c.FS, Version+"/api/"+name)
	if err != nil {
		return out, err
	}
	err = decodeStrict(data, &out)
	return out, err
}

// Scenario decodes a named typed scenario fixture.
func (c *Catalog) Scenario(name string) (Scenario, error) {
	var out Scenario
	data, err := fs.ReadFile(c.FS, Version+"/scenarios/"+name+".json")
	if err != nil {
		return out, err
	}
	err = decodeStrict(data, &out)
	return out, err
}

// Deterministic returns the canonical generated bytes for a media fixture.
// It is used by tests to prove that checked-in binaries were not hand-edited.
func Deterministic(name string) ([]byte, error) {
	const jpegB64 = "/9j/4AAQSkZJRgABAQAAAQABAAD/2wBDAP//////////////////////////////////////////////////////////////////////////////////////2wBDAf//////////////////////////////////////////////////////////////////////////////////////wAARCAABAAEDASIAAhEBAxEB/8QAFQABAQAAAAAAAAAAAAAAAAAAAAX/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oADAMBAAIQAxAAAAH/xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oACAEBAAEFAqf/xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAEDAQE/AX//xAAUEQEAAAAAAAAAAAAAAAAAAAAA/9oACAECAQE/AX//xAAUEAEAAAAAAAAAAAAAAAAAAAAA/9oARWAAAgEBIQA/2gAMAwEAAgADAAAAEP/EABQQAQAAAAAAAAAAAAAAAAAAABD/2gAIAQMBAT8Qf//EABQRAQAAAAAAAAAAAAAAAAAAABD/2gAIAQIBAT8Qf//EABQQAQAAAAAAAAAAAAAAAAAAABD/2gAIAQEAAT8Qf//Z"
	const pngB64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="
	const webmB64 = "GkXfo0AgQoaBAULygQFfQ"
	decode := func(v string) []byte { b, _ := base64.StdEncoding.DecodeString(v); return b }
	jpeg, png, webm := decode(jpegB64), decode(pngB64), decode(webmB64)
	box := func(kind string, payload []byte) []byte {
		out := make([]byte, 8+len(payload))
		n := len(out)
		out[0] = byte(n >> 24)
		out[1] = byte(n >> 16)
		out[2] = byte(n >> 8)
		out[3] = byte(n)
		copy(out[4:8], kind)
		copy(out[8:], payload)
		return out
	}
	join := func(parts ...[]byte) []byte {
		n := 0
		for _, p := range parts {
			n += len(p)
		}
		out := make([]byte, 0, n)
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	mp4 := join(box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2avc1mp41")), box("moov", []byte("cable-fixture-h264-aac-v1")), box("mdat", []byte("CABLE-H264-AAC-DETERMINISTIC-FRAME")))
	switch name {
	case "phase1.jpg":
		return jpeg, nil
	case "phase1.png":
		return png, nil
	case "phase1.mp4":
		return nil, ErrNotAlgorithmic
	case "unsupported.webm":
		return webm, nil
	case "corrupt.jpg":
		out := bytes.Clone(jpeg)
		copy(out[:6], []byte{0, 1, 2, 3, 4, 5})
		return out, nil
	case "truncated.mp4":
		return mp4[:len(mp4)-7], nil
	default:
		return nil, fmt.Errorf("unknown media fixture %q", name)
	}
}

// ValidateDeterminism proves every valid and negative binary matches its
// documented generator, in addition to checksum and media-signature checks.
func (c *Catalog) ValidateDeterminism() error {
	for _, item := range c.Manifest.Media {
		want, err := Deterministic(item.Name)
		if errors.Is(err, ErrNotAlgorithmic) {
			continue
		}
		if err != nil {
			return err
		}
		got, err := c.Bytes(item.Name)
		if err != nil {
			return err
		}
		if !bytes.Equal(want, got) {
			return fmt.Errorf("fixture %q is not deterministic", item.Name)
		}
	}
	return nil
}
