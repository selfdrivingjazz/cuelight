// Package cable contains the authored Cable schedule and immutable presentation
// contracts. It intentionally has no knowledge of HTTP or browser storage.
package cable

import (
	"context"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

var (
	ErrNotFound            = errors.New("cable resource not found")
	ErrConflict            = errors.New("cable resource version conflict")
	ErrInvalidArgument     = errors.New("invalid cable argument")
	ErrOverlap             = errors.New("scheduled blocks overlap")
	ErrArtifactUnavailable = errors.New("cable artifact unavailable")
	ErrArtifactMissing     = ErrArtifactUnavailable
	ErrArtifactQuarantined = ErrArtifactUnavailable
	ErrBuildUnavailable    = errors.New("cable shell build unavailable")
	ErrImmutable           = errors.New("cable revision is immutable")
	ErrForbidden           = errors.New("cable action is forbidden")
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}
func (e *ValidationError) Unwrap() error { return ErrInvalidArgument }

func ETag(version int64) string { return fmt.Sprintf("\"v%d\"", version) }

type Page struct {
	NextCursor     string `json:"nextCursor,omitempty"`
	PreviousCursor string `json:"previousCursor,omitempty"`
	HasMore        bool   `json:"hasMore"`
}

type Mutation struct {
	ActorID         string
	IdempotencyKey  string
	ExpectedVersion int64
	Now             time.Time
}

type DeploymentPage struct {
	Items []CableDeployment
	Page  Page
}
type ChannelPage struct {
	Items []Channel
	Page  Page
}
type ProgramPage struct {
	Items []ProgramEntry
	Page  Page
}
type RevisionPage struct {
	Items []CableRevision
	Page  Page
}
type ShellBuildPage struct {
	Items []ShellBuild
	Page  Page
}
type PlaylistPage struct {
	Items []Playlist
	Page  Page
}
type BlockPage struct {
	Items []Block
	Page  Page
}
type ProfilePage struct {
	Items []Profile
	Page  Page
}

type CableDeployment struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Timezone            string            `json:"timezone"`
	Theme               string            `json:"theme"`
	GuideHorizonSeconds int64             `json:"guideHorizonSeconds"`
	GuideDensity        string            `json:"guideDensity"`
	PlacardDefaults     map[string]string `json:"placardDefaults,omitempty"`
	FallbackMetadata    map[string]string `json:"fallbackMetadata,omitempty"`
	Version             int64             `json:"version"`
	CreatedBy           string            `json:"createdBy"`
	UpdatedBy           string            `json:"updatedBy"`
	CreatedAt           time.Time         `json:"createdAt"`
	UpdatedAt           time.Time         `json:"updatedAt"`
}

type Channel struct {
	ID             string    `json:"id"`
	DeploymentID   string    `json:"deploymentId"`
	Number         int       `json:"number"`
	Name           string    `json:"name"`
	Callsign       string    `json:"callsign"`
	Description    string    `json:"description"`
	Accent         string    `json:"accent"`
	ArtworkAssetID string    `json:"artworkAssetId,omitempty"`
	Enabled        bool      `json:"enabled"`
	Standby        bool      `json:"standby"`
	Version        int64     `json:"version"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// ProgramEntry is retained as an internal materialized projection for older
// repository implementations. Authored schedules are represented by blocks.
type ProgramEntry struct {
	ID                 string    `json:"id"`
	ChannelID          string    `json:"channelId"`
	AssetID            string    `json:"assetId"`
	StartsAt           time.Time `json:"startsAt"`
	EndsAt             time.Time `json:"endsAt"`
	Title              string    `json:"title"`
	Subtitle           string    `json:"subtitle"`
	Tag                string    `json:"tag"`
	Artist             string    `json:"artist"`
	Description        string    `json:"description"`
	ArtworkAssetID     string    `json:"artworkAssetId,omitempty"`
	PlacardPolicy      string    `json:"placardPolicy"`
	Fit                string    `json:"fit"`
	AvailabilityPolicy string    `json:"availabilityPolicy"`
	Loop               bool      `json:"loop"`
	Mute               bool      `json:"mute"`
	Volume             float64   `json:"volume"`
	Version            int64     `json:"version"`
	CreatedAt          time.Time `json:"createdAt"`
	UpdatedAt          time.Time `json:"updatedAt"`
}
type Playlist struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Items       []PlaylistItem `json:"items"`
	Version     int64          `json:"version"`
	CreatedBy   string         `json:"createdBy"`
	CreatedAt   time.Time      `json:"createdAt"`
	UpdatedAt   time.Time      `json:"updatedAt"`
}
type PlaylistItem struct {
	ID              string `json:"id"`
	PlaylistID      string `json:"playlistId"`
	AssetID         string `json:"assetId"`
	Position        int    `json:"position"`
	DurationSeconds int    `json:"durationSeconds"`
	Title           string `json:"title"`
	Subtitle        string `json:"subtitle"`
}
type Block struct {
	ID         string    `json:"id"`
	ChannelID  string    `json:"channelId"`
	PlaylistID string    `json:"playlistId,omitempty"`
	AssetID    string    `json:"assetId,omitempty"`
	Title      string    `json:"title"`
	StartsAt   time.Time `json:"startsAt"`
	EndsAt     time.Time `json:"endsAt"`
	Version    int64     `json:"version"`
	CreatedBy  string    `json:"createdBy"`
	CreatedAt  time.Time `json:"createdAt"`
}
type Profile struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	DeploymentID string    `json:"deploymentId"`
	ShellBuildID string    `json:"shellBuildId"`
	ReleaseID    string    `json:"releaseId,omitempty"`
	Version      int64     `json:"version"`
	CreatedBy    string    `json:"createdBy"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type Artifact struct {
	AssetID, Digest, MediaType string
	Size                       int64
	State                      string
}
type ShellBuild struct {
	ID, Digest, Entrypoint string
	ArtifactDigests        []string
	VersionMetadata        map[string]string
	BridgeProtocol         string
	CreatedBy              string
	CreatedAt              time.Time
}

type RevisionWindow struct {
	StartsAt time.Time `json:"startsAt"`
	EndsAt   time.Time `json:"endsAt"`
}
type RevisionChannel struct {
	ID            string            `json:"id"`
	Number        int               `json:"number"`
	Name          string            `json:"name"`
	Callsign      string            `json:"callsign,omitempty"`
	Description   string            `json:"description,omitempty"`
	Accent        string            `json:"accent,omitempty"`
	ArtworkDigest string            `json:"artworkDigest,omitempty"`
	Enabled       bool              `json:"enabled"`
	Standby       bool              `json:"standby"`
	Blocks        []RevisionBlock   `json:"blocks"`
	Programs      []RevisionProgram `json:"programs"`
}
type RevisionBlock struct {
	ID         string            `json:"id"`
	StartsAt   time.Time         `json:"startsAt"`
	EndsAt     time.Time         `json:"endsAt"`
	PlaylistID string            `json:"playlistId,omitempty"`
	AssetID    string            `json:"assetId,omitempty"`
	Title      string            `json:"title,omitempty"`
	Loop       bool              `json:"loop"`
	Items      []RevisionProgram `json:"items"`
}
type RevisionProgram struct {
	ID                 string    `json:"id"`
	BlockID            string    `json:"blockId,omitempty"`
	ItemID             string    `json:"itemId,omitempty"`
	ItemPosition       int       `json:"itemPosition,omitempty"`
	Occurrence         int       `json:"occurrence,omitempty"`
	StartsAt           time.Time `json:"startsAt"`
	EndsAt             time.Time `json:"endsAt"`
	AssetID            string    `json:"assetId"`
	ArtifactDigest     string    `json:"artifactDigest"`
	MediaType          string    `json:"mediaType"`
	Title              string    `json:"title,omitempty"`
	Subtitle           string    `json:"subtitle,omitempty"`
	Tag                string    `json:"tag,omitempty"`
	Artist             string    `json:"artist,omitempty"`
	Description        string    `json:"description,omitempty"`
	ArtworkDigest      string    `json:"artworkDigest,omitempty"`
	Fit                string    `json:"fit,omitempty"`
	PlacardPolicy      string    `json:"placardPolicy,omitempty"`
	AvailabilityPolicy string    `json:"availabilityPolicy,omitempty"`
	Loop               bool      `json:"loop"`
	Mute               bool      `json:"mute"`
	Volume             float64   `json:"volume"`
}
type CableRevision struct {
	ID                string            `json:"id"`
	DeploymentID      string            `json:"deploymentId"`
	ShellBuildID      string            `json:"shellBuildId"`
	DeploymentVersion int64             `json:"deploymentVersion"`
	Digest            string            `json:"digest"`
	ShellBuildDigest  string            `json:"shellBuildDigest"`
	Entrypoint        string            `json:"entrypoint"`
	BridgeProtocol    string            `json:"bridgeProtocol"`
	Timezone          string            `json:"timezone"`
	Theme             string            `json:"theme"`
	GuideDensity      string            `json:"guideDensity"`
	Window            RevisionWindow    `json:"window"`
	Channels          []RevisionChannel `json:"channels"`
	ArtifactDigests   []string          `json:"artifactDigests"`
	JSON              []byte            `json:"json"`
	Errors            []string          `json:"errors,omitempty"`
	Warnings          []string          `json:"warnings,omitempty"`
	CreatedBy         string            `json:"createdBy"`
	CreatedAt         time.Time         `json:"createdAt"`
	GeneratedAt       time.Time         `json:"generatedAt"`
}

// RevisionArtifactStore makes the immutable revision document available from
// the content-addressed store before its database metadata is published.
type RevisionArtifactStore interface {
	PutRevisionArtifact(context.Context, string, []byte) error
}
type DeploymentInput struct {
	Name, Timezone, Theme             string
	GuideHorizonSeconds               int64
	GuideDensity                      string
	PlacardDefaults, FallbackMetadata map[string]string
}
type ChannelInput struct {
	DeploymentID                                        string
	Number                                              int
	Name, Callsign, Description, Accent, ArtworkAssetID string
	Enabled, Standby                                    bool
}
type ProgramInput struct {
	ChannelID, AssetID                                        string
	StartsAt, EndsAt                                          time.Time
	Title, Subtitle, Tag, Artist, Description, ArtworkAssetID string
	Fit, PlacardPolicy, AvailabilityPolicy                    string
	Loop, Mute                                                bool
	Volume                                                    float64
}
type ShellBuildInput struct {
	Digest, Entrypoint string
	ArtifactDigests    []string
	VersionMetadata    map[string]string
	BridgeProtocol     string
}
type PlaylistInput struct {
	Name, Description string
	Items             []PlaylistItemInput
}
type PlaylistItemInput struct {
	AssetID, Title, Subtitle string
	Position                 int
	DurationSeconds          int
}
type BlockInput struct {
	ChannelID, PlaylistID, AssetID, Title string
	StartsAt, EndsAt                      time.Time
}
type ProfileInput struct {
	Name, Description, DeploymentID, ShellBuildID, ReleaseID string
}

type Authorizer interface {
	Authorize(context.Context, string, string, string, string) error
}
type AllowAllAuthorizer struct{}

func (AllowAllAuthorizer) Authorize(context.Context, string, string, string, string) error {
	return nil
}

type Repository interface {
	CreateDeployment(context.Context, Mutation, DeploymentInput) (CableDeployment, error)
	ListDeployments(context.Context, string, int) (DeploymentPage, error)
	Deployment(context.Context, string) (CableDeployment, error)
	UpdateDeployment(context.Context, Mutation, string, DeploymentInput) (CableDeployment, error)
	DeleteDeployment(context.Context, Mutation, string) error
	CreateChannel(context.Context, Mutation, ChannelInput) (Channel, error)
	Channel(context.Context, string) (Channel, error)
	UpdateChannel(context.Context, Mutation, string, ChannelInput) (Channel, error)
	ListChannels(context.Context, string, string, int) (ChannelPage, error)
	DeleteChannel(context.Context, Mutation, string) error
	CreateProgramEntry(context.Context, Mutation, ProgramInput) (ProgramEntry, error)
	ProgramEntry(context.Context, string) (ProgramEntry, error)
	UpdateProgramEntry(context.Context, Mutation, string, ProgramInput) (ProgramEntry, error)
	ListProgramEntries(context.Context, string, string, int) (ProgramPage, error)
	DeleteProgramEntry(context.Context, Mutation, string) error
	RegisterShellBuild(context.Context, Mutation, ShellBuildInput) (ShellBuild, error)
	ShellBuild(context.Context, string) (ShellBuild, error)
	ListShellBuilds(context.Context, string, int) (ShellBuildPage, error)
	CreateRevision(context.Context, CableRevision) error
	Revision(context.Context, string) (CableRevision, error)
	ListRevisions(context.Context, string, string, int) (RevisionPage, error)
}
type ProgrammingRepository interface {
	CreatePlaylist(context.Context, Mutation, PlaylistInput) (Playlist, error)
	Playlist(context.Context, string) (Playlist, error)
	ListPlaylists(context.Context, string, int) (PlaylistPage, error)
	CreateBlock(context.Context, Mutation, BlockInput) (Block, error)
	ListBlocks(context.Context, string, string, int) (BlockPage, error)
	CreateProfile(context.Context, Mutation, ProfileInput) (Profile, error)
	Profile(context.Context, string) (Profile, error)
	ListProfiles(context.Context, string, int) (ProfilePage, error)
}

// ProgrammingDetailRepository provides canonical block detail reads without
// forcing older programming repository implementations to grow a method.
type ProgrammingDetailRepository interface {
	Block(context.Context, string) (Block, error)
}

// ProgrammingMutationRepository contains optimistic-concurrency updates for
// authored programming resources.
type ProgrammingMutationRepository interface {
	UpdatePlaylist(context.Context, Mutation, string, PlaylistInput) (Playlist, error)
	UpdateBlock(context.Context, Mutation, string, BlockInput) (Block, error)
	UpdateProfile(context.Context, Mutation, string, ProfileInput) (Profile, error)
}

func (s Service) Deployment(ctx context.Context, id string) (CableDeployment, error) {
	if s.Repository == nil {
		return CableDeployment{}, errors.New("cable repository is required")
	}
	return s.Repository.Deployment(ctx, id)
}

func (s Service) Channel(ctx context.Context, id string) (Channel, error) {
	if s.Repository == nil {
		return Channel{}, errors.New("cable repository is required")
	}
	return s.Repository.Channel(ctx, id)
}

func (s Service) ProgramEntry(ctx context.Context, id string) (ProgramEntry, error) {
	if s.Repository == nil {
		return ProgramEntry{}, errors.New("cable repository is required")
	}
	return s.Repository.ProgramEntry(ctx, id)
}

func (s Service) Playlist(ctx context.Context, id string) (Playlist, error) {
	repository, err := s.programming()
	if err != nil {
		return Playlist{}, err
	}
	return repository.Playlist(ctx, id)
}

func (s Service) Block(ctx context.Context, id string) (Block, error) {
	repository, err := s.programming()
	if err != nil {
		return Block{}, err
	}
	detail, ok := repository.(ProgrammingDetailRepository)
	if !ok {
		return Block{}, errors.New("Cable block detail repository is required")
	}
	return detail.Block(ctx, id)
}

func (s Service) Profile(ctx context.Context, id string) (Profile, error) {
	repository, err := s.programming()
	if err != nil {
		return Profile{}, err
	}
	return repository.Profile(ctx, id)
}

type ArtifactRepository interface {
	CableArtifactsForAsset(context.Context, string) ([]Artifact, error)
}
type ArtifactByDigestRepository interface {
	CableArtifactByDigest(context.Context, string) (Artifact, error)
}
type ShellBuildRepository interface {
	ShellBuild(context.Context, string) (ShellBuild, error)
}

const (
	ThemeNebula  = "nebula"
	ThemeGallery = "gallery"
	ThemeNoir    = "noir"
)

func NormalizeTheme(theme string) (string, error) {
	theme = strings.ToLower(strings.TrimSpace(theme))
	switch theme {
	case ThemeNebula, ThemeGallery, ThemeNoir:
		return theme, nil
	default:
		return "", &ValidationError{"theme", "must be one of nebula, gallery, or noir"}
	}
}
func NormalizeGuideDensity(density string) (string, error) {
	density = strings.ToLower(strings.TrimSpace(density))
	if density == "" {
		return "half-hour", nil
	}
	switch density {
	case "quarter-hour", "half-hour", "hour":
		return density, nil
	default:
		return "", &ValidationError{"guideDensity", "must be one of quarter-hour, half-hour, or hour"}
	}
}

func NormalizeTimezone(timezone string) (string, error) {
	timezone = strings.TrimSpace(timezone)
	if timezone == "" {
		return "", &ValidationError{"timezone", "is required"}
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", &ValidationError{"timezone", "must be an IANA timezone"}
	}
	return loc.String(), nil
}

func NormalizeEntrypoint(entrypoint string) (string, error) {
	entrypoint = strings.TrimSpace(strings.ReplaceAll(entrypoint, `\`, "/"))
	if entrypoint == "" || strings.IndexByte(entrypoint, 0) >= 0 || strings.HasPrefix(entrypoint, "/") {
		return "", &ValidationError{"entrypoint", "must be a safe relative path"}
	}
	clean := path.Clean(entrypoint)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", &ValidationError{"entrypoint", "must be a safe relative path"}
	}
	return clean, nil
}

func NormalizePlacardPolicy(policy string) (string, error) {
	policy = strings.ToLower(strings.TrimSpace(policy))
	switch policy {
	case "default", "always", "start", "never":
		return policy, nil
	default:
		return "", &ValidationError{"placardPolicy", "must be one of default, always, start, or never"}
	}
}

func NormalizeAvailabilityPolicy(policy string) (string, error) {
	policy = strings.ToLower(strings.TrimSpace(policy))
	switch policy {
	case "fail", "skip", "fallback", "hold":
		return policy, nil
	default:
		return "", &ValidationError{"availabilityPolicy", "must be one of fail, skip, fallback, or hold"}
	}
}

func NormalizeMediaType(mediaType string) (string, error) {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" || strings.ContainsAny(mediaType, " \t\r\n") {
		return "", &ValidationError{"mediaType", "is invalid"}
	}
	if !supportedMedia(mediaType) {
		return "", &ValidationError{"mediaType", "must be video, image, or audio"}
	}
	return mediaType, nil
}

func validDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	for _, r := range digest {
		if !(r >= '0' && r <= '9') && !(r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func NormalizeWindow(w RevisionWindow) (RevisionWindow, error) {
	if err := ValidateWindow(w); err != nil {
		return RevisionWindow{}, err
	}
	w.StartsAt = w.StartsAt.UTC()
	w.EndsAt = w.EndsAt.UTC()
	return w, nil
}

func ValidateWindow(w RevisionWindow) error {
	if w.StartsAt.IsZero() || w.EndsAt.IsZero() || !w.EndsAt.After(w.StartsAt) {
		return &ValidationError{"window", "endsAt must be after startsAt"}
	}
	return nil
}
func ValidateProgram(p ProgramInput) error {
	if p.ChannelID == "" {
		return &ValidationError{"channelId", "is required"}
	}
	if p.AssetID == "" {
		return &ValidationError{"assetId", "is required"}
	}
	if p.StartsAt.IsZero() || p.EndsAt.IsZero() || !p.EndsAt.After(p.StartsAt) {
		return &ValidationError{"time", "endsAt must be after startsAt"}
	}
	if p.Volume < 0 || p.Volume > 1 {
		return &ValidationError{"volume", "must be between 0 and 1"}
	}
	if p.Fit != "" && p.Fit != "fit" && p.Fit != "fill" {
		return &ValidationError{"fit", "must be fit or fill"}
	}
	if _, err := NormalizePlacardPolicy(p.PlacardPolicy); err != nil {
		return err
	}
	if _, err := NormalizeAvailabilityPolicy(p.AvailabilityPolicy); err != nil {
		return err
	}
	return nil
}

func ValidateBlock(b BlockInput) error {
	if b.ChannelID == "" {
		return &ValidationError{"channelId", "is required"}
	}
	if (strings.TrimSpace(b.PlaylistID) == "") == (strings.TrimSpace(b.AssetID) == "") {
		return &ValidationError{"source", "must contain exactly one playlist or asset"}
	}
	if b.StartsAt.IsZero() || b.EndsAt.IsZero() || !b.EndsAt.After(b.StartsAt) {
		return &ValidationError{"time", "endsAt must be after startsAt"}
	}
	return nil
}

type Service struct {
	Repository  Repository
	Authorizer  Authorizer
	Programming ProgrammingRepository
}

func (s Service) allow(ctx context.Context, m Mutation, action, resource, id string) error {
	if s.Repository == nil {
		return errors.New("cable repository is required")
	}
	if s.Authorizer == nil {
		return nil
	}
	if e := s.Authorizer.Authorize(ctx, m.ActorID, action, resource, id); e != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, e)
	}
	return nil
}
func (s Service) CreateDeployment(ctx context.Context, m Mutation, in DeploymentInput) (CableDeployment, error) {
	if e := s.allow(ctx, m, "cable.deployment.create", "deployment", ""); e != nil {
		return CableDeployment{}, e
	}
	return s.Repository.CreateDeployment(ctx, m, in)
}
func (s Service) UpdateDeployment(ctx context.Context, m Mutation, id string, in DeploymentInput) (CableDeployment, error) {
	if e := s.allow(ctx, m, "cable.deployment.update", "deployment", id); e != nil {
		return CableDeployment{}, e
	}
	return s.Repository.UpdateDeployment(ctx, m, id, in)
}
func (s Service) DeleteDeployment(ctx context.Context, m Mutation, id string) error {
	if e := s.allow(ctx, m, "cable.deployment.delete", "deployment", id); e != nil {
		return e
	}
	return s.Repository.DeleteDeployment(ctx, m, id)
}
func (s Service) CreateChannel(ctx context.Context, m Mutation, in ChannelInput) (Channel, error) {
	if e := s.allow(ctx, m, "cable.channel.create", "deployment", in.DeploymentID); e != nil {
		return Channel{}, e
	}
	return s.Repository.CreateChannel(ctx, m, in)
}
func (s Service) UpdateChannel(ctx context.Context, m Mutation, id string, in ChannelInput) (Channel, error) {
	if e := s.allow(ctx, m, "cable.channel.update", "channel", id); e != nil {
		return Channel{}, e
	}
	return s.Repository.UpdateChannel(ctx, m, id, in)
}
func (s Service) DeleteChannel(ctx context.Context, m Mutation, id string) error {
	if e := s.allow(ctx, m, "cable.channel.delete", "channel", id); e != nil {
		return e
	}
	return s.Repository.DeleteChannel(ctx, m, id)
}
func (s Service) CreateProgramEntry(ctx context.Context, m Mutation, in ProgramInput) (ProgramEntry, error) {
	if e := s.allow(ctx, m, "cable.program.create", "channel", in.ChannelID); e != nil {
		return ProgramEntry{}, e
	}
	return s.Repository.CreateProgramEntry(ctx, m, in)
}
func (s Service) UpdateProgramEntry(ctx context.Context, m Mutation, id string, in ProgramInput) (ProgramEntry, error) {
	if e := s.allow(ctx, m, "cable.program.update", "program", id); e != nil {
		return ProgramEntry{}, e
	}
	return s.Repository.UpdateProgramEntry(ctx, m, id, in)
}
func (s Service) DeleteProgramEntry(ctx context.Context, m Mutation, id string) error {
	if e := s.allow(ctx, m, "cable.program.delete", "program", id); e != nil {
		return e
	}
	return s.Repository.DeleteProgramEntry(ctx, m, id)
}
func (s Service) RegisterShellBuild(ctx context.Context, m Mutation, in ShellBuildInput) (ShellBuild, error) {
	if e := s.allow(ctx, m, "cable.shell_build.create", "shell_build", ""); e != nil {
		return ShellBuild{}, e
	}
	return s.Repository.RegisterShellBuild(ctx, m, in)
}

func (s Service) programming() (ProgrammingRepository, error) {
	if s.Programming == nil {
		return nil, errors.New("Cable programming repository is required")
	}
	return s.Programming, nil
}
func (s Service) CreatePlaylist(ctx context.Context, m Mutation, in PlaylistInput) (Playlist, error) {
	if e := s.allow(ctx, m, "cable.playlist.create", "playlist", ""); e != nil {
		return Playlist{}, e
	}
	repository, err := s.programming()
	if err != nil {
		return Playlist{}, err
	}
	return repository.CreatePlaylist(ctx, m, in)
}
func (s Service) CreateBlock(ctx context.Context, m Mutation, in BlockInput) (Block, error) {
	if e := s.allow(ctx, m, "cable.block.create", "channel", in.ChannelID); e != nil {
		return Block{}, e
	}
	repository, err := s.programming()
	if err != nil {
		return Block{}, err
	}
	return repository.CreateBlock(ctx, m, in)
}
func (s Service) CreateProfile(ctx context.Context, m Mutation, in ProfileInput) (Profile, error) {
	if e := s.allow(ctx, m, "cable.profile.create", "profile", ""); e != nil {
		return Profile{}, e
	}
	repository, err := s.programming()
	if err != nil {
		return Profile{}, err
	}
	return repository.CreateProfile(ctx, m, in)
}
func (s Service) UpdatePlaylist(ctx context.Context, m Mutation, id string, in PlaylistInput) (Playlist, error) {
	if e := s.allow(ctx, m, "cable.playlist.update", "playlist", id); e != nil {
		return Playlist{}, e
	}
	repository, err := s.programming()
	if err != nil {
		return Playlist{}, err
	}
	mutator, ok := repository.(ProgrammingMutationRepository)
	if !ok {
		return Playlist{}, errors.New("Cable playlist mutation repository is required")
	}
	return mutator.UpdatePlaylist(ctx, m, id, in)
}

func (s Service) UpdateBlock(ctx context.Context, m Mutation, id string, in BlockInput) (Block, error) {
	if e := s.allow(ctx, m, "cable.block.update", "block", id); e != nil {
		return Block{}, e
	}
	repository, err := s.programming()
	if err != nil {
		return Block{}, err
	}
	mutator, ok := repository.(ProgrammingMutationRepository)
	if !ok {
		return Block{}, errors.New("Cable block mutation repository is required")
	}
	return mutator.UpdateBlock(ctx, m, id, in)
}

func (s Service) UpdateProfile(ctx context.Context, m Mutation, id string, in ProfileInput) (Profile, error) {
	if e := s.allow(ctx, m, "cable.profile.update", "profile", id); e != nil {
		return Profile{}, e
	}
	repository, err := s.programming()
	if err != nil {
		return Profile{}, err
	}
	mutator, ok := repository.(ProgrammingMutationRepository)
	if !ok {
		return Profile{}, errors.New("Cable profile mutation repository is required")
	}
	return mutator.UpdateProfile(ctx, m, id, in)
}
