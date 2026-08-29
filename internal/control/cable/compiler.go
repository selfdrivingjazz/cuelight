package cable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Compiler struct {
	Repository        Repository
	Programming       ProgrammingRepository
	Artifacts         ArtifactRepository
	Builds            ShellBuildRepository
	Authorizer        Authorizer
	RevisionArtifacts RevisionArtifactStore
	Now               func() time.Time
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
	var err error
	if window, err = NormalizeWindow(window); err != nil {
		return CableRevision{}, err
	}
	d, err := c.Repository.Deployment(ctx, deploymentID)
	if err != nil {
		return CableRevision{}, err
	}
	d.Theme, err = NormalizeTheme(d.Theme)
	if err != nil {
		return CableRevision{}, err
	}
	d.Timezone, err = NormalizeTimezone(d.Timezone)
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
	b.Digest, err = normalizeDigest(b.Digest, "shellBuildDigest")
	if err != nil {
		return CableRevision{}, err
	}
	b.Entrypoint, err = NormalizeEntrypoint(b.Entrypoint)
	if err != nil {
		return CableRevision{}, err
	}
	if b.BridgeProtocol = strings.TrimSpace(b.BridgeProtocol); b.BridgeProtocol == "" {
		return CableRevision{}, fmt.Errorf("%w: shell build %s is incomplete", ErrBuildUnavailable, shellBuildID)
	}
	byDigest, ok := c.Artifacts.(ArtifactByDigestRepository)
	if !ok {
		return CableRevision{}, fmt.Errorf("%w: shell artifact lookup is unavailable", ErrBuildUnavailable)
	}
	shell, e := byDigest.CableArtifactByDigest(ctx, b.Digest)
	if e != nil || shell.State != "ready" || !isShellBundleMedia(shell.MediaType) {
		return CableRevision{}, fmt.Errorf("%w: shell bundle %s is unavailable or not a ready tar artifact", ErrBuildUnavailable, b.Digest)
	}
	shellDigest, e := normalizeDigest(shell.Digest, "shellBuildDigest")
	if e != nil || shellDigest != b.Digest {
		return CableRevision{}, fmt.Errorf("%w: shell bundle digest mismatch", ErrBuildUnavailable)
	}
	for i, digest := range b.ArtifactDigests {
		digest, e = normalizeDigest(digest, "shell artifact digest")
		if e != nil {
			return CableRevision{}, fmt.Errorf("%w: %v", ErrBuildUnavailable, e)
		}
		b.ArtifactDigests[i] = digest
		a, e := byDigest.CableArtifactByDigest(ctx, digest)
		if e != nil || a.State != "ready" {
			return CableRevision{}, fmt.Errorf("%w: shell artifact %s is unavailable", ErrBuildUnavailable, digest)
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
	now := c.now()
	result := CableRevision{DeploymentID: d.ID, DeploymentVersion: d.Version, ShellBuildID: b.ID, ShellBuildDigest: b.Digest, Entrypoint: b.Entrypoint, BridgeProtocol: b.BridgeProtocol, Timezone: d.Timezone, Theme: d.Theme, Window: window, CreatedBy: actor, CreatedAt: now, GeneratedAt: now}
	closure := make(map[string]struct{})
	closure[b.Digest] = struct{}{}
	for _, digest := range b.ArtifactDigests {
		if digest == "" {
			return CableRevision{}, fmt.Errorf("%w: empty shell artifact", ErrBuildUnavailable)
		}
		closure[digest] = struct{}{}
	}
	programming := c.Programming
	if programming == nil {
		programming, _ = c.Repository.(ProgrammingRepository)
	}
	for _, ch := range channels {
		rc := RevisionChannel{ID: ch.ID, Number: ch.Number, Name: ch.Name, Callsign: ch.Callsign, Description: ch.Description, Accent: ch.Accent, Enabled: ch.Enabled, Standby: ch.Standby, Blocks: []RevisionBlock{}, Programs: []RevisionProgram{}}
		if ch.ArtworkAssetID != "" {
			a, e := c.artifact(ctx, ch.ArtworkAssetID)
			if e != nil {
				return CableRevision{}, e
			}
			rc.ArtworkDigest = a.Digest
			closure[a.Digest] = struct{}{}
		}
		// Program entries are only a compatibility projection for repositories
		// that predate canonical blocks; Store implements ProgrammingRepository.
		if programming == nil {
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
			for _, p := range entries {
				if p.EndsAt.Before(window.StartsAt) || p.StartsAt.After(window.EndsAt) {
					continue
				}
				rp, e := c.compileProgram(ctx, p, p.ID, "", 0, false, &closure)
				if e != nil {
					return CableRevision{}, e
				}
				rc.Programs = append(rc.Programs, rp)
			}
		} else {
			blocks, e := c.allBlocks(ctx, programming, ch.ID)
			if e != nil {
				return CableRevision{}, e
			}
			sort.Slice(blocks, func(i, j int) bool {
				if blocks[i].StartsAt.Equal(blocks[j].StartsAt) {
					return blocks[i].ID < blocks[j].ID
				}
				return blocks[i].StartsAt.Before(blocks[j].StartsAt)
			})
			if err := validateBlockOrder(blocks); err != nil {
				return CableRevision{}, fmt.Errorf("channel %s: %w", ch.ID, err)
			}
			for _, block := range blocks {
				if !block.EndsAt.After(window.StartsAt) || !block.StartsAt.Before(window.EndsAt) {
					continue
				}
				rb, e := c.compileBlock(ctx, programming, block, window, &closure)
				if e != nil {
					return CableRevision{}, e
				}
				rc.Blocks = append(rc.Blocks, rb)
				rc.Programs = append(rc.Programs, rb.Items...)
			}
		}
		result.Channels = append(result.Channels, rc)
	}
	result.ArtifactDigests = make([]string, 0, len(closure))
	for digest := range closure {
		result.ArtifactDigests = append(result.ArtifactDigests, digest)
	}
	sort.Strings(result.ArtifactDigests)
	payload := revisionPayload{Schema: "cable.revision.v1", DeploymentID: result.DeploymentID, DeploymentVersion: result.DeploymentVersion, Theme: result.Theme, GeneratedAt: result.GeneratedAt, ShellBuildID: result.ShellBuildID, ShellBuildDigest: result.ShellBuildDigest, Entrypoint: result.Entrypoint, BridgeProtocol: result.BridgeProtocol, Timezone: result.Timezone, Window: result.Window, Channels: result.Channels, ArtifactDigests: result.ArtifactDigests}
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
	Theme             string            `json:"theme"`
	GeneratedAt       time.Time         `json:"generatedAt"`
	ShellBuildID      string            `json:"shellBuildId"`
	ShellBuildDigest  string            `json:"shellBuildDigest"`
	Entrypoint        string            `json:"entrypoint"`
	BridgeProtocol    string            `json:"bridgeProtocol"`
	Timezone          string            `json:"timezone"`
	Window            RevisionWindow    `json:"window"`
	Channels          []RevisionChannel `json:"channels"`
	ArtifactDigests   []string          `json:"artifactDigests"`
}
func (c Compiler) compileBlock(ctx context.Context, programming ProgrammingRepository, block Block, window RevisionWindow, closure *map[string]struct{}) (RevisionBlock, error) {
	if block.ID == "" || block.StartsAt.IsZero() || block.EndsAt.IsZero() || !block.EndsAt.After(block.StartsAt) {
		return RevisionBlock{}, &ValidationError{"block.time", "endsAt must be after startsAt"}
	}
	if (block.PlaylistID == "") == (block.AssetID == "") {
		return RevisionBlock{}, &ValidationError{"block.source", "must contain exactly one playlist or asset"}
	}
	result := RevisionBlock{ID: block.ID, StartsAt: block.StartsAt.UTC(), EndsAt: block.EndsAt.UTC(), PlaylistID: block.PlaylistID, AssetID: block.AssetID, Title: block.Title, Loop: true, Items: []RevisionProgram{}}
	if block.AssetID != "" {
		p := ProgramEntry{ID: block.ID, ChannelID: block.ChannelID, AssetID: block.AssetID, StartsAt: block.StartsAt, EndsAt: block.EndsAt, Title: block.Title, Fit: "fill", PlacardPolicy: "start", AvailabilityPolicy: "fail", Loop: true, Volume: 1}
		rp, err := c.compileProgram(ctx, p, block.ID, block.ID, 0, true, closure)
		if err != nil {
			return RevisionBlock{}, err
		}
		result.Items = append(result.Items, rp)
		return result, nil
	}
	playlist, err := programming.Playlist(ctx, block.PlaylistID)
	if err != nil {
		return RevisionBlock{}, err
	}
	if len(playlist.Items) == 0 {
		return RevisionBlock{}, &ValidationError{"block.playlistId", "playlist must contain at least one item"}
	}
	items := append([]PlaylistItem(nil), playlist.Items...)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Position != items[j].Position {
			return items[i].Position < items[j].Position
		}
		return items[i].ID < items[j].ID
	})
	for i, item := range items {
		if item.ID == "" || item.AssetID == "" || item.Position <= 0 || item.DurationSeconds <= 0 || (i > 0 && item.Position == items[i-1].Position) {
			return RevisionBlock{}, &ValidationError{"playlist.items", "items must have unique positive positions, assets, and durations"}
		}
	}
	cursor := block.StartsAt.UTC()
	occurrence := 0
	for cursor.Before(block.EndsAt.UTC()) {
		item := items[occurrence%len(items)]
		itemEnd := cursor.Add(time.Duration(item.DurationSeconds) * time.Second)
		if itemEnd.After(block.EndsAt.UTC()) {
			itemEnd = block.EndsAt.UTC()
		}
		occurrence++
		if itemEnd.After(window.StartsAt) && cursor.Before(window.EndsAt) {
			p := ProgramEntry{ID: item.ID, ChannelID: block.ChannelID, AssetID: item.AssetID, StartsAt: cursor, EndsAt: itemEnd, Title: item.Title, Subtitle: item.Subtitle, Fit: "fill", PlacardPolicy: "start", AvailabilityPolicy: "fail", Loop: true, Volume: 1}
			id := block.ID + ":" + strconv.Itoa(occurrence)
			rp, e := c.compileProgram(ctx, p, id, block.ID, item.Position, true, closure)
			if e != nil {
				return RevisionBlock{}, e
			}
			rp.ItemID = item.ID
			rp.Occurrence = occurrence
			result.Items = append(result.Items, rp)
		}
		cursor = itemEnd
	}
	return result, nil
}

func (c Compiler) compileProgram(ctx context.Context, p ProgramEntry, id, blockID string, itemPosition int, loop bool, closure *map[string]struct{}) (RevisionProgram, error) {
	fit := p.Fit
	if fit == "" {
		fit = "fit"
	}
	placard := p.PlacardPolicy
	if placard == "" {
		placard = "default"
	}
	var err error
	placard, err = NormalizePlacardPolicy(placard)
	if err != nil {
		return RevisionProgram{}, fmt.Errorf("program %s: %w", p.ID, err)
	}
	availability := p.AvailabilityPolicy
	if availability == "" {
		availability = "fail"
	}
	availability, err = NormalizeAvailabilityPolicy(availability)
	if err != nil {
		return RevisionProgram{}, fmt.Errorf("program %s: %w", p.ID, err)
	}
	a, err := c.artifact(ctx, p.AssetID)
	if err != nil {
		return RevisionProgram{}, err
	}
	(*closure)[a.Digest] = struct{}{}
	rp := RevisionProgram{ID: id, BlockID: blockID, ItemPosition: itemPosition, StartsAt: p.StartsAt.UTC(), EndsAt: p.EndsAt.UTC(), AssetID: p.AssetID, ArtifactDigest: a.Digest, MediaType: a.MediaType, Title: p.Title, Subtitle: p.Subtitle, Tag: p.Tag, Artist: p.Artist, Description: p.Description, Fit: fit, PlacardPolicy: placard, AvailabilityPolicy: availability, Loop: loop || p.Loop, Mute: p.Mute, Volume: p.Volume}
	if p.ArtworkAssetID != "" {
		art, e := c.artifact(ctx, p.ArtworkAssetID)
		if e != nil {
			return RevisionProgram{}, e
		}
		rp.ArtworkDigest = art.Digest
		(*closure)[art.Digest] = struct{}{}
	}
	return rp, nil
}

func (c Compiler) allBlocks(ctx context.Context, programming ProgrammingRepository, id string) ([]Block, error) {
	var out []Block
	cursor := ""
	for {
		p, e := programming.ListBlocks(ctx, id, cursor, 100)
		if e != nil {
			return nil, e
		}
		out = append(out, p.Items...)
		if !p.Page.HasMore {
			return out, nil
		}
		if p.Page.NextCursor == "" {
			return nil, errors.New("cable block pagination cursor missing")
		}
		cursor = p.Page.NextCursor
	}
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
func normalizeDigest(digest, field string) (string, error) {
	digest = strings.TrimSpace(digest)
	if !validDigest(digest) {
		return "", &ValidationError{field, "must be a lowercase SHA-256 digest"}
	}
	return digest, nil
}

func isShellBundleMedia(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	return mediaType == "application/x-tar" || mediaType == "application/tar" || mediaType == "application/tar+gzip" || mediaType == "application/gzip"
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
		if a.State != "ready" {
			continue
		}
		a.Digest, err = normalizeDigest(a.Digest, "artifactDigest")
		if err != nil {
			return Artifact{}, fmt.Errorf("%w: %v", ErrArtifactUnavailable, err)
		}
		a.MediaType, err = NormalizeMediaType(a.MediaType)
		if err != nil {
			return Artifact{}, fmt.Errorf("%w: %v", ErrArtifactUnavailable, err)
		}
		return a, nil
	}
	return Artifact{}, fmt.Errorf("%w: asset %s is quarantined or missing", ErrArtifactUnavailable, assetID)
}
func supportedMedia(mt string) bool {
	return strings.HasPrefix(mt, "video/") || strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "audio/")
}
func validateChannelOrder(ch []Channel) error {
	seen := map[int]bool{}
	for _, v := range ch {
		if v.ID == "" {
			return &ValidationError{"channel.id", "is required"}
		}
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
func validateBlockOrder(blocks []Block) error {
	for i, block := range blocks {
		if block.ID == "" {
			return &ValidationError{"block.id", "is required"}
		}
		if block.StartsAt.IsZero() || block.EndsAt.IsZero() || !block.EndsAt.After(block.StartsAt) {
			return &ValidationError{"block.time", "endsAt must be after startsAt"}
		}
		if (block.PlaylistID == "") == (block.AssetID == "") {
			return &ValidationError{"block.source", "must contain exactly one playlist or asset"}
		}
		if i > 0 && block.StartsAt.Before(blocks[i-1].EndsAt) {
			return fmt.Errorf("%w: %s overlaps %s", ErrOverlap, blocks[i-1].ID, block.ID)
		}
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
	if c.RevisionArtifacts == nil {
		return CableRevision{}, errors.New("revision artifact store is required for publish")
	}
	if err := c.RevisionArtifacts.PutRevisionArtifact(ctx, rev.Digest, rev.JSON); err != nil {
		return CableRevision{}, fmt.Errorf("store cable revision artifact: %w", err)
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
