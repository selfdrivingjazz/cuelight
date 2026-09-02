package media

import (
	"context"
	"errors"
	"io"
	"mime"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/page"
)

var (
	ErrArtifactUnavailable = errors.New("media artifact is unavailable")
	ErrUnauthorized        = errors.New("operator authentication required")
	ErrForbidden           = errors.New("operator scope is insufficient")
	ErrPrecondition        = errors.New("If-Match precondition failed")
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

type Media struct {
	ID             string    `json:"id"`
	AssetID        string    `json:"assetId"`
	Title          string    `json:"title"`
	Description    string    `json:"description,omitempty"`
	Artist         string    `json:"artist,omitempty"`
	Tags           []string  `json:"tags"`
	ArtworkAssetID string    `json:"artworkAssetId,omitempty"`
	Playback       Playback  `json:"playback"`
	Artifact       Artifact  `json:"artifact"`
	Version        int64     `json:"version"`
	CreatedBy      string    `json:"createdBy"`
	UpdatedBy      string    `json:"updatedBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type MediaInput struct {
	AssetID        string   `json:"assetId"`
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
}

type MediaUsage struct {
	MediaID       string `json:"mediaId"`
	ReleaseCount  int64  `json:"releaseCount"`
	ScheduleCount int64  `json:"scheduleCount"`
}

type MediaRepository interface {
	CreateMedia(context.Context, Mutation, Media) (Media, error)
	ListMedia(context.Context, page.PageRequest, MediaListFilter) (page.Response[Media], error)
	GetMedia(context.Context, string) (Media, error)
	UpdateMedia(context.Context, Mutation, string, int64, MediaPatch) (Media, error)
	GetMediaUsage(context.Context, string) (MediaUsage, error)
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
	if err := validateMediaInput(input); err != nil {
		return Media{}, err
	}
	id := strings.TrimSpace(s.NewID())
	if id == "" {
		return Media{}, errors.New("media ID generator returned an empty ID")
	}
	return s.Repository.CreateMedia(ctx, mutation, Media{ID: id, AssetID: strings.TrimSpace(input.AssetID), Title: strings.TrimSpace(input.Title), Description: strings.TrimSpace(input.Description), Artist: strings.TrimSpace(input.Artist), Tags: normalizedTags(input.Tags), ArtworkAssetID: strings.TrimSpace(input.ArtworkAssetID), Playback: input.Playback, Version: 1, CreatedBy: mutation.ActorID, UpdatedBy: mutation.ActorID})
}

func (s *AuthoredService) List(ctx context.Context, request page.PageRequest, filter MediaListFilter) (page.Response[Media], error) {
	return s.Repository.ListMedia(ctx, request, filter)
}

func (s *AuthoredService) Get(ctx context.Context, id string) (Media, error) {
	if strings.TrimSpace(id) == "" {
		return Media{}, &ValidationError{Field: "mediaId", Reason: "is required"}
	}
	return s.Repository.GetMedia(ctx, strings.TrimSpace(id))
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
	if strings.TrimSpace(input.AssetID) == "" {
		return &ValidationError{Field: "assetId", Reason: "is required"}
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

func (m Media) Playable() bool {
	if m.Artifact.State != ArtifactReady || m.Artifact.Size <= 0 {
		return false
	}
	t, _, err := mime.ParseMediaType(m.Artifact.MediaType)
	return err == nil && (strings.HasPrefix(t, "audio/") || strings.HasPrefix(t, "video/") || strings.HasPrefix(t, "image/"))
}
