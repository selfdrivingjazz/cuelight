package cable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type Compiler struct {
	Repository Repository
	Artifacts  ArtifactRepository
	Builds     ShellBuildRepository
	Authorizer Authorizer
	Now        func() time.Time
}

func (c Compiler) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}

func (c Compiler) authorize(ctx context.Context, actor, action, resourceType, resourceID string) error {
	if c.Authorizer == nil {
		return nil
	}
	if err := c.Authorizer.Authorize(ctx, actor, action, resourceType, resourceID); err != nil {
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	}
	return nil
}

// Compile resolves authored state into a presentation-ready immutable document.
// It performs no writes and is therefore suitable for preview.
func (c Compiler) Compile(ctx context.Context, actor, deploymentID, shellBuildID string, window RevisionWindow) (CableRevision, error) {
	if c.Repository == nil || c.Artifacts == nil {
		return CableRevision{}, errors.New("cable compiler repositories are required")
	}
	if err := c.authorize(ctx, actor, "cable.preview", "deployment", deploymentID); err != nil {
		return CableRevision{}, err
	}
	if err := ValidateWindow(window); err != nil {
		return CableRevision{}, err
	}
	d, err := c.Repository.Deployment(ctx, deploymentID)
	if err != nil {
		return CableRevision{}, err
	}
	if shellBuildID == "" {
		return CableRevision{}, &ValidationError{"shellBuildId", "is required"}
	}
	var b ShellBuild
	if c.Builds != nil {
		b, err = c.Builds.ShellBuild(ctx, shellBuildID)
	} else {
		b, err = c.Repository.ShellBuild(ctx, shellBuildID)
	}
	if err != nil {
		return CableRevision{}, err
	}
	if b.Digest == "" || b.Entrypoint == "" || b.BridgeProtocol == "" {
		return CableRevision{}, fmt.Errorf("%w: shell build %s is incomplete", ErrBuildUnavailable, shellBuildID)
	}
	if byDigest, ok := c.Artifacts.(ArtifactByDigestRepository); ok {
		for _, digest := range b.ArtifactDigests {
			a, e := byDigest.CableArtifactByDigest(ctx, digest)
			if e != nil {
				return CableRevision{}, fmt.Errorf("%w: shell artifact %s", ErrBuildUnavailable, digest)
			}
			if a.State != "ready" {
				return CableRevision{}, fmt.Errorf("%w: shell artifact %s is %s", ErrBuildUnavailable, digest, a.State)
			}
		}
	}
	channels, err := c.allChannels(ctx, deploymentID)
	if err != nil {
		return CableRevision{}, err
	}
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].Number != channels[j].Number {
			return channels[i].Number < channels[j].Number
		}
		return channels[i].ID < channels[j].ID
	})
	if err := validateChannelOrder(channels); err != nil {
		return CableRevision{}, err
	}
	result := CableRevision{DeploymentID: d.ID, DeploymentVersion: d.Version, ShellBuildID: b.ID, ShellBuildDigest: b.Digest, Entrypoint: b.Entrypoint, BridgeProtocol: b.BridgeProtocol, Timezone: d.Timezone, Window: window, CreatedBy: actor, CreatedAt: c.now()}
	closure := make(map[string]struct{})
	for _, digest := range b.ArtifactDigests {
		if digest == "" {
			return CableRevision{}, fmt.Errorf("%w: empty shell artifact", ErrBuildUnavailable)
		}
		closure[digest] = struct{}{}
	}
	for _, ch := range channels {
		entries, e := c.allPrograms(ctx, ch.ID)
		if e != nil {
			return CableRevision{}, e
		}
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].StartsAt.Equal(entries[j].StartsAt) {
				return entries[i].ID < entries[j].ID
			}
			return entries[i].StartsAt.Before(entries[j].StartsAt)
		})
		if err := validateNoOverlap(entries); err != nil {
			return CableRevision{}, fmt.Errorf("channel %s: %w", ch.ID, err)
		}
		rc := RevisionChannel{ID: ch.ID, Number: ch.Number, Name: ch.Name, Callsign: ch.Callsign, Description: ch.Description, Accent: ch.Accent, Enabled: ch.Enabled, Standby: ch.Standby, Programs: make([]RevisionProgram, 0, len(entries))}
		if ch.ArtworkAssetID != "" {
			a, e := c.artifact(ctx, ch.ArtworkAssetID)
			if e != nil {
				return CableRevision{}, e
			}
			rc.ArtworkDigest = a.Digest
			closure[a.Digest] = struct{}{}
		}
		for _, p := range entries {
			if p.EndsAt.Before(window.StartsAt) || p.StartsAt.After(window.EndsAt) {
				continue
			}
			a, e := c.artifact(ctx, p.AssetID)
			if e != nil {
				return CableRevision{}, e
			}
			closure[a.Digest] = struct{}{}
			rp := RevisionProgram{ID: p.ID, StartsAt: p.StartsAt.UTC(), EndsAt: p.EndsAt.UTC(), AssetID: p.AssetID, ArtifactDigest: a.Digest, MediaType: a.MediaType, Title: p.Title, Subtitle: p.Subtitle, Tag: p.Tag, Artist: p.Artist, Description: p.Description, Fit: p.Fit, PlacardPolicy: p.PlacardPolicy, AvailabilityPolicy: p.AvailabilityPolicy, Loop: p.Loop, Mute: p.Mute, Volume: p.Volume}
			if p.ArtworkAssetID != "" {
				art, e := c.artifact(ctx, p.ArtworkAssetID)
				if e != nil {
					return CableRevision{}, e
				}
				rp.ArtworkDigest = art.Digest
				closure[art.Digest] = struct{}{}
			}
			rc.Programs = append(rc.Programs, rp)
		}
		result.Channels = append(result.Channels, rc)
	}
	result.ArtifactDigests = make([]string, 0, len(closure))
	for digest := range closure {
		result.ArtifactDigests = append(result.ArtifactDigests, digest)
	}
	sort.Strings(result.ArtifactDigests)
	payload := revisionPayload{Schema: "cable.revision.v1", DeploymentID: result.DeploymentID, DeploymentVersion: result.DeploymentVersion, ShellBuildID: result.ShellBuildID, ShellBuildDigest: result.ShellBuildDigest, Entrypoint: result.Entrypoint, BridgeProtocol: result.BridgeProtocol, Timezone: result.Timezone, Window: result.Window, Channels: result.Channels, ArtifactDigests: result.ArtifactDigests}
	data, err := json.Marshal(payload)
	if err != nil {
		return CableRevision{}, fmt.Errorf("marshal cable revision: %w", err)
	}
	sum := sha256.Sum256(data)
	result.Digest = hex.EncodeToString(sum[:])
	result.ID = result.Digest
	result.ArtifactDigests = append(result.ArtifactDigests, result.Digest)
	result.JSON = data
	return result, nil
}

type revisionPayload struct {
	Schema            string            `json:"schema"`
	DeploymentID      string            `json:"deploymentId"`
	DeploymentVersion int64             `json:"deploymentVersion"`
	ShellBuildID      string            `json:"shellBuildId"`
	ShellBuildDigest  string            `json:"shellBuildDigest"`
	Entrypoint        string            `json:"entrypoint"`
	BridgeProtocol    string            `json:"bridgeProtocol"`
	Timezone          string            `json:"timezone"`
	Window            RevisionWindow    `json:"window"`
	Channels          []RevisionChannel `json:"channels"`
	ArtifactDigests   []string          `json:"artifactDigests"`
}

func (c Compiler) allChannels(ctx context.Context, id string) ([]Channel, error) {
	var out []Channel
	cursor := ""
	for {
		p, e := c.Repository.ListChannels(ctx, id, cursor, 100)
		if e != nil {
			return nil, e
		}
		out = append(out, p.Items...)
		if !p.Page.HasMore {
			return out, nil
		}
		if p.Page.NextCursor == "" {
			return nil, errors.New("cable channel pagination cursor missing")
		}
		cursor = p.Page.NextCursor
	}
}
func (c Compiler) allPrograms(ctx context.Context, id string) ([]ProgramEntry, error) {
	var out []ProgramEntry
	cursor := ""
	for {
		p, e := c.Repository.ListProgramEntries(ctx, id, cursor, 100)
		if e != nil {
			return nil, e
		}
		out = append(out, p.Items...)
		if !p.Page.HasMore {
			return out, nil
		}
		if p.Page.NextCursor == "" {
			return nil, errors.New("cable program pagination cursor missing")
		}
		cursor = p.Page.NextCursor
	}
}
func (c Compiler) artifact(ctx context.Context, assetID string) (Artifact, error) {
	as, err := c.Artifacts.CableArtifactsForAsset(ctx, assetID)
	if err != nil {
		return Artifact{}, err
	}
	if len(as) == 0 {
		return Artifact{}, fmt.Errorf("%w: asset %s has no artifact", ErrArtifactUnavailable, assetID)
	}
	sort.Slice(as, func(i, j int) bool { return as[i].Digest < as[j].Digest })
	for _, a := range as {
		if a.State == "ready" && a.Digest != "" {
			if !supportedMedia(a.MediaType) {
				return Artifact{}, fmt.Errorf("%w: unsupported media type %s", ErrArtifactUnavailable, a.MediaType)
			}
			return a, nil
		}
	}
	return Artifact{}, fmt.Errorf("%w: asset %s is quarantined or missing", ErrArtifactUnavailable, assetID)
}
func supportedMedia(mt string) bool {
	return strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "audio/")
}
func validateChannelOrder(ch []Channel) error {
	seen := map[int]bool{}
	for _, v := range ch {
		if v.Number <= 0 {
			return &ValidationError{"channel.number", "must be positive"}
		}
		if seen[v.Number] {
			return &ValidationError{"channel.number", "must be unique"}
		}
		seen[v.Number] = true
	}
	return nil
}
func validateNoOverlap(ps []ProgramEntry) error {
	for i := 1; i < len(ps); i++ {
		if !ps[i].StartsAt.Before(ps[i-1].EndsAt) {
			continue
		}
		return fmt.Errorf("%w: %s overlaps %s", ErrOverlap, ps[i-1].ID, ps[i].ID)
	}
	return nil
}
func (c Compiler) Preview(ctx context.Context, actor, deploymentID, shellBuildID string, window RevisionWindow) (CableRevision, error) {
	return c.Compile(ctx, actor, deploymentID, shellBuildID, window)
}
func (c Compiler) Publish(ctx context.Context, m Mutation, deploymentID, shellBuildID string, window RevisionWindow) (CableRevision, error) {
	if err := c.authorize(ctx, m.ActorID, "cable.publish", "deployment", deploymentID); err != nil {
		return CableRevision{}, err
	}
	if m.ExpectedVersion <= 0 {
		return CableRevision{}, ErrConflict
	}
	d, e := c.Repository.Deployment(ctx, deploymentID)
	if e != nil {
		return CableRevision{}, e
	}
	if d.Version != m.ExpectedVersion {
		return CableRevision{}, ErrConflict
	}
	rev, err := c.compileUnprivileged(ctx, m.ActorID, deploymentID, shellBuildID, window)
	if err != nil {
		return CableRevision{}, err
	}
	if err := c.Repository.CreateRevision(ctx, rev); err != nil {
		return CableRevision{}, err
	}
	return rev, nil
}
func (c Compiler) compileUnprivileged(ctx context.Context, actor, deploymentID, shellBuildID string, w RevisionWindow) (CableRevision, error) {
	old := c.Authorizer
	c.Authorizer = nil
	defer func() { c.Authorizer = old }()
	return c.Compile(ctx, actor, deploymentID, shellBuildID, w)
}
