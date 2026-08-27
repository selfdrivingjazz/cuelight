// Package cable contains the authored Cable schedule and immutable presentation
// contracts. It intentionally has no knowledge of HTTP or browser storage.
package cable

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	ErrNotFound            = errors.New("cable resource not found")
	ErrConflict            = errors.New("cable resource version conflict")
	ErrInvalidArgument     = errors.New("invalid cable argument")
	ErrOverlap             = errors.New("program entries overlap")
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

type CableDeployment struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Timezone         string            `json:"timezone"`
	Theme            string            `json:"theme"`
	GuideHorizon     time.Duration     `json:"guideHorizon"`
	GuideDensity     string            `json:"guideDensity"`
	PlacardDefaults  map[string]string `json:"placardDefaults,omitempty"`
	FallbackMetadata map[string]string `json:"fallbackMetadata,omitempty"`
	Version          int64             `json:"version"`
	CreatedBy        string            `json:"createdBy"`
	UpdatedBy        string            `json:"updatedBy"`
	CreatedAt        time.Time         `json:"createdAt"`
	UpdatedAt        time.Time         `json:"updatedAt"`
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
	Programs      []RevisionProgram `json:"programs"`
}
type RevisionProgram struct {
	ID                 string    `json:"id"`
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
	ID, DeploymentID, ShellBuildID       string
	DeploymentVersion                    int64
	Digest, ShellBuildDigest             string
	Entrypoint, BridgeProtocol, Timezone string
	Window                               RevisionWindow
	Channels                             []RevisionChannel
	ArtifactDigests                      []string
	JSON                                 []byte
	Errors, Warnings                     []string
	CreatedBy                            string
	CreatedAt                            time.Time
}

type DeploymentInput struct {
	Name, Timezone, Theme             string
	GuideHorizon                      time.Duration
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

type ArtifactRepository interface {
	CableArtifactsForAsset(context.Context, string) ([]Artifact, error)
}
type ArtifactByDigestRepository interface {
	CableArtifactByDigest(context.Context, string) (Artifact, error)
}
type ShellBuildRepository interface {
	ShellBuild(context.Context, string) (ShellBuild, error)
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
	return nil
}

type Service struct {
	Repository Repository
	Authorizer Authorizer
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
