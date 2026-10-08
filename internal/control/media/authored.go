package media

import (
	"context"
	"errors"
	"io"
	"mime"
	"net/url"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/page"
	canonical "github.com/jmill/cable/internal/release"
)

var (
	ErrArtifactUnavailable = errors.New("media artifact is unavailable")
	ErrUnauthorized        = errors.New("operator authentication required")
	ErrForbidden           = errors.New("operator scope is insufficient")
	ErrPrecondition        = errors.New("If-Match precondition failed")
	// ErrManaged rejects operator edits of Cable items, which follow their deployment.
	ErrManaged = errors.New("cable items are managed by their deployment")
	// ErrWrongKind rejects operations that only apply to the other media kind.
	ErrWrongKind = errors.New("operation does not apply to this media kind")
	// ErrCaptureDisabled reports that no server-side web capture is configured.
	ErrCaptureDisabled = errors.New("server-side web capture is not configured")
	// ErrInUse rejects deleting media that a playlist still references.
	ErrInUse = errors.New("media is used by a playlist")
)

const (
	KindFile = "file"
	KindWeb  = "web"
)

// Playback describes presentation controls for an authored media record. Asset
// bytes remain immutable; changing these fields creates a new media version.
type Playback struct {
	Fit         string `json:"fit"`
	Loop        bool   `json:"loop"`
	Muted       bool   `json:"muted"`
	Volume      int    `json:"volume"`
	Orientation int    `json:"orientation"`
}

// Media is one library item. File media wrap an uploaded asset; web media
// show a live page by URL, or for Cable items the deployment's hosted guide.
type Media struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	AssetID        string   `json:"assetId,omitempty"`
	URL            string   `json:"url,omitempty"`
	DeploymentID   string   `json:"deploymentId,omitempty"`
	FaviconURL     string   `json:"faviconUrl,omitempty"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Artist         string   `json:"artist,omitempty"`
	Tags           []string `json:"tags"`
	ArtworkAssetID string   `json:"artworkAssetId,omitempty"`
	Playback       Playback `json:"playback,omitzero"`
	Artifact       Artifact `json:"artifact,omitzero"`
	Version        int64    `json:"version"`
	CreatedBy      string   `json:"createdBy"`
	UpdatedBy      string   `json:"updatedBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Thumbnail      *Thumbnail `json:"thumbnail,omitempty"`
	// ThumbnailError and ThumbnailAttemptedAt describe the last web capture.
	ThumbnailError       string     `json:"thumbnailError,omitempty"`
	ThumbnailAttemptedAt *time.Time `json:"thumbnailAttemptedAt,omitempty"`
}

// MediaInput creates file media (Kind "" or "file", AssetID) or web media
// (Kind "web", URL; Title defaults to the URL).
type MediaInput struct {
	Kind           string   `json:"kind,omitempty"`
	AssetID        string   `json:"assetId,omitempty"`
	URL            string   `json:"url,omitempty"`
	Title          string   `json:"title"`
	Description    string   `json:"description,omitempty"`
	Artist         string   `json:"artist,omitempty"`
	Tags           []string `json:"tags"`
	ArtworkAssetID string   `json:"artworkAssetId,omitempty"`
	Playback       Playback `json:"playback"`
}

type MediaPatch struct {
	Title          *string   `json:"title,omitempty"`
	Description    *string   `json:"description,omitempty"`
	Artist         *string   `json:"artist,omitempty"`
	Tags           *[]string `json:"tags,omitempty"`
	ArtworkAssetID *string   `json:"artworkAssetId,omitempty"`
	Playback       *Playback `json:"playback,omitempty"`
}

type MediaListFilter struct {
	Playable *bool
	AssetIDs []string
	// Kind is file or web; MediaType narrows file media to image, video, or audio.
	Kind      string
	MediaType string
}
type MediaUsage struct {
	MediaID       string `json:"mediaId"`
	ReleaseCount  int64  `json:"releaseCount"`
	ScheduleCount int64  `json:"scheduleCount"`
}

type MediaRepository interface {
	CreateMedia(context.Context, Mutation, Media) (Media, error)
	// CreateWebMedia inserts a web item or returns the existing item for the
	// URL with created=false.
	CreateWebMedia(context.Context, Mutation, Media) (Media, bool, error)
	ListMedia(context.Context, page.PageRequest, MediaListFilter) (page.Response[Media], error)
	GetMedia(context.Context, string) (Media, error)
	UpdateMedia(context.Context, Mutation, string, int64, MediaPatch) (Media, error)
	// DeleteWebMedia removes an operator-added web item.
	DeleteWebMedia(context.Context, string) error
	GetMediaUsage(context.Context, string) (MediaUsage, error)
}

// WebCapture renders web media thumbnails. Capture runs in the background;
// Refresh captures synchronously.
type WebCapture interface {
	Capture(id, url string)
	Refresh(ctx context.Context, id string) error
}

type ContentObject struct {
	Digest string
	Size   int64
}

type ContentReader interface {
	Stat(context.Context, string) (ContentObject, error)
	Open(context.Context, string) (ContentHandle, error)
}

type ContentHandle interface {
	io.ReadSeeker
	io.Closer
}

// AuthoredService validates authored metadata and delegates all mutation
// durability, idempotency, and optimistic concurrency to the repository.
type AuthoredService struct {
	Repository MediaRepository
	NewID      IDGenerator
	// CableOrigin is the public control-plane origin used to resolve Cable
	// item URLs; empty leaves them unresolved.
	CableOrigin string
}

// CableURL is the hosted guide address of a deployment's Cable item.
func CableURL(origin, deploymentID string) string {
	return strings.TrimRight(origin, "/") + "/cable/?deployment=" + url.QueryEscape(deploymentID)
}

func (s *AuthoredService) resolve(value Media) Media {
	if value.DeploymentID != "" && s.CableOrigin != "" {
		value.URL = CableURL(s.CableOrigin, value.DeploymentID)
	}
	return value
}

func NewAuthoredService(repository MediaRepository, newID IDGenerator) (*AuthoredService, error) {
	if repository == nil || newID == nil {
		return nil, &ValidationError{Field: "dependencies", Reason: "repository and ID generator are required"}
	}
	return &AuthoredService{Repository: repository, NewID: newID}, nil
}

func (s *AuthoredService) Create(ctx context.Context, mutation Mutation, input MediaInput) (Media, error) {
	if err := validateMutation(mutation, true); err != nil {
		return Media{}, err
	}
	if input.Kind != "" && input.Kind != KindFile {
		return Media{}, &ValidationError{Field: "kind", Reason: "must be file; web media are created with CreateWeb"}
	}
	if err := validateMediaInput(input); err != nil {
		return Media{}, err
	}
	id := strings.TrimSpace(s.NewID())
	if id == "" {
		return Media{}, errors.New("media ID generator returned an empty ID")
	}
	return s.Repository.CreateMedia(ctx, mutation, Media{ID: id, Kind: KindFile, AssetID: strings.TrimSpace(input.AssetID), Title: strings.TrimSpace(input.Title), Description: strings.TrimSpace(input.Description), Artist: strings.TrimSpace(input.Artist), Tags: normalizedTags(input.Tags), ArtworkAssetID: strings.TrimSpace(input.ArtworkAssetID), Playback: input.Playback, Version: 1, CreatedBy: mutation.ActorID, UpdatedBy: mutation.ActorID})
}

// CreateWeb adds a web page to the library. Adding a URL that is already in
// the library returns the existing item with created=false.
func (s *AuthoredService) CreateWeb(ctx context.Context, mutation Mutation, input MediaInput) (Media, bool, error) {
	if err := validateMutation(mutation, false); err != nil {
		return Media{}, false, err
	}
	url := strings.TrimSpace(input.URL)
	if err := canonical.ValidateWebURL(url); err != nil {
		return Media{}, false, &ValidationError{Field: "url", Reason: err.Error()}
	}
	if strings.TrimSpace(input.AssetID) != "" {
		return Media{}, false, &ValidationError{Field: "assetId", Reason: "web media do not reference an asset"}
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		title = truncateText(url, 255)
	}
	if err := validateText("title", title, 255); err != nil {
		return Media{}, false, err
	}
	if len([]byte(input.Description)) > 4096 {
		return Media{}, false, &ValidationError{Field: "description", Reason: "exceeds 4096 bytes"}
	}
	if err := validateTags(input.Tags); err != nil {
		return Media{}, false, err
	}
	id := strings.TrimSpace(s.NewID())
	if id == "" {
		return Media{}, false, errors.New("media ID generator returned an empty ID")
	}
	return s.Repository.CreateWebMedia(ctx, mutation, Media{ID: id, Kind: KindWeb, URL: url, Title: title, Description: strings.TrimSpace(input.Description), Tags: normalizedTags(input.Tags), Version: 1, CreatedBy: mutation.ActorID, UpdatedBy: mutation.ActorID})
}

// DeleteWeb removes an operator-added web item; file media and Cable items
// cannot be deleted here.
func (s *AuthoredService) DeleteWeb(ctx context.Context, id string) error {
	current, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if current.DeploymentID != "" {
		return ErrManaged
	}
	if current.Kind != KindWeb {
		return ErrWrongKind
	}
	return s.Repository.DeleteWebMedia(ctx, current.ID)
}

func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	runes := []rune(value)
	for len(string(runes)) > limit {
		runes = runes[:len(runes)-1]
	}
	return string(runes)
}

func (s *AuthoredService) List(ctx context.Context, request page.PageRequest, filter MediaListFilter) (page.Response[Media], error) {
	result, err := s.Repository.ListMedia(ctx, request, filter)
	for index := range result.Items {
		result.Items[index] = s.resolve(result.Items[index])
	}
	return result, err
}

func (s *AuthoredService) Get(ctx context.Context, id string) (Media, error) {
	if strings.TrimSpace(id) == "" {
		return Media{}, &ValidationError{Field: "mediaId", Reason: "is required"}
	}
	value, err := s.Repository.GetMedia(ctx, strings.TrimSpace(id))
	return s.resolve(value), err
}

func (s *AuthoredService) Update(ctx context.Context, mutation Mutation, id string, expectedVersion int64, patch MediaPatch) (Media, error) {
	if err := validateMutation(mutation, true); err != nil {
		return Media{}, err
	}
	if strings.TrimSpace(id) == "" {
		return Media{}, &ValidationError{Field: "mediaId", Reason: "is required"}
	}
	if expectedVersion <= 0 {
		return Media{}, &ValidationError{Field: "version", Reason: "must be positive"}
	}
	if err := validatePatch(patch); err != nil {
		return Media{}, err
	}
	return s.Repository.UpdateMedia(ctx, mutation, strings.TrimSpace(id), expectedVersion, normalizePatch(patch))
}

func (s *AuthoredService) Usage(ctx context.Context, id string) (MediaUsage, error) {
	if strings.TrimSpace(id) == "" {
		return MediaUsage{}, &ValidationError{Field: "mediaId", Reason: "is required"}
	}
	return s.Repository.GetMediaUsage(ctx, strings.TrimSpace(id))
}

func validateMediaInput(input MediaInput) error {
	if !validUUID(strings.TrimSpace(input.AssetID)) {
		return &ValidationError{Field: "assetId", Reason: "must be a UUID"}
	}
	if input.ArtworkAssetID != "" && !validUUID(strings.TrimSpace(input.ArtworkAssetID)) {
		return &ValidationError{Field: "artworkAssetId", Reason: "must be a UUID"}
	}
	if err := validateText("title", input.Title, 255); err != nil {
		return err
	}
	if len([]byte(input.Description)) > 4096 {
		return &ValidationError{Field: "description", Reason: "exceeds 4096 bytes"}
	}
	if len([]byte(input.Artist)) > 255 {
		return &ValidationError{Field: "artist", Reason: "exceeds 255 bytes"}
	}
	return validateTagsAndPlayback(input.Tags, input.Playback)
}
func validatePatch(p MediaPatch) error {
	if p.Title != nil {
		if err := validateText("title", *p.Title, 255); err != nil {
			return err
		}
	}
	if p.Description != nil && len([]byte(*p.Description)) > 4096 {
		return &ValidationError{Field: "description", Reason: "exceeds 4096 bytes"}
	}
	if p.Artist != nil && len([]byte(*p.Artist)) > 255 {
		return &ValidationError{Field: "artist", Reason: "exceeds 255 bytes"}
	}
	if p.Tags != nil {
		if err := validateTags(*p.Tags); err != nil {
			return err
		}
	}
	if p.Playback != nil {
		if err := validatePlayback(*p.Playback); err != nil {
			return err
		}
	}
	return nil
}
func validateTagsAndPlayback(tags []string, playback Playback) error {
	if err := validateTags(tags); err != nil {
		return err
	}
	return validatePlayback(playback)
}
func validateTags(tags []string) error {
	if len(tags) > 64 {
		return &ValidationError{Field: "tags", Reason: "at most 64 tags are allowed"}
	}
	seen := map[string]struct{}{}
	for _, tag := range tags {
		tag = strings.TrimSpace(tag)
		if tag == "" || len([]byte(tag)) > 128 {
			return &ValidationError{Field: "tags", Reason: "tags must be non-empty and at most 128 bytes"}
		}
		if _, ok := seen[tag]; ok {
			return &ValidationError{Field: "tags", Reason: "tags must be unique"}
		}
		seen[tag] = struct{}{}
	}
	return nil
}
func validatePlayback(p Playback) error {
	if p.Fit != "fit" && p.Fit != "fill" {
		return &ValidationError{Field: "playback.fit", Reason: "must be fit or fill"}
	}
	if p.Volume < 0 || p.Volume > 100 {
		return &ValidationError{Field: "playback.volume", Reason: "must be between 0 and 100"}
	}
	switch p.Orientation {
	case 0, 90, 180, 270:
	default:
		return &ValidationError{Field: "playback.orientation", Reason: "must be 0, 90, 180, or 270"}
	}
	return nil
}
func normalizedTags(tags []string) []string {
	out := make([]string, len(tags))
	for i, tag := range tags {
		out[i] = strings.TrimSpace(tag)
	}
	return out
}
func normalizePatch(p MediaPatch) MediaPatch {
	if p.Title != nil {
		v := strings.TrimSpace(*p.Title)
		p.Title = &v
	}
	if p.Description != nil {
		v := strings.TrimSpace(*p.Description)
		p.Description = &v
	}
	if p.Artist != nil {
		v := strings.TrimSpace(*p.Artist)
		p.Artist = &v
	}
	if p.ArtworkAssetID != nil {
		v := strings.TrimSpace(*p.ArtworkAssetID)
		p.ArtworkAssetID = &v
	}
	if p.Tags != nil {
		v := normalizedTags(*p.Tags)
		p.Tags = &v
	}
	return p
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			if character != '-' {
				return false
			}
			continue
		}
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f' || character >= 'A' && character <= 'F') {
			return false
		}
	}
	return true
}

func (m Media) Playable() bool {
	if m.Artifact.State != ArtifactReady || m.Artifact.Size <= 0 {
		return false
	}
	t, _, err := mime.ParseMediaType(m.Artifact.MediaType)
	return err == nil && (strings.HasPrefix(t, "audio/") || strings.HasPrefix(t, "video/") || strings.HasPrefix(t, "image/"))
}
