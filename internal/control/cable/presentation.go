package cable

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GuideMediaPath is the hosted guide endpoint that streams an asset referenced
// by the deployment being presented.
const GuideMediaPath = "/api/v1/cable-guide/media/"

// GuideBridgeProtocol is the guide protocol version reported to cable-web.
const GuideBridgeProtocol = "1"

// guideLookback keeps the program currently on air (and its predecessor) in
// the presentation window.
const guideLookback = time.Hour

// Presentation is the document the hosted cable-web guide parses
// (presentationSchema in apps/cable-web/src/presentation.ts). It is computed
// live from authored deployment state for every request.
type Presentation struct {
	Revision    PresentationRevision  `json:"revision"`
	Channels    []PresentationChannel `json:"channels"`
	Blocks      []PresentationBlock   `json:"blocks"`
	Occurrences []PresentationProgram `json:"occurrences"`
	Bridge      PresentationBridge    `json:"bridge"`
	Theme       string                `json:"theme"`
	Remote      *PresentationRemote   `json:"remote,omitempty"`
}

type PresentationRevision struct {
	ID           string    `json:"id"`
	Digest       string    `json:"digest"`
	GeneratedAt  time.Time `json:"generatedAt"`
	Timezone     string    `json:"timezone"`
	Stale        bool      `json:"stale"`
	GuideDensity string    `json:"guideDensity"`
}

type PresentationChannel struct {
	ID             string `json:"id"`
	Number         int    `json:"number"`
	Name           string `json:"name"`
	Callsign       string `json:"callsign"`
	Accent         string `json:"accent"`
	Enabled        bool   `json:"enabled"`
	StandbyMessage string `json:"standbyMessage,omitempty"`
}

type PresentationBlock struct {
	ID        string    `json:"id"`
	ChannelID string    `json:"channelId"`
	StartsAt  time.Time `json:"startsAt"`
	EndsAt    time.Time `json:"endsAt"`
	Title     string    `json:"title"`
	Source    string    `json:"source"`
}

type PresentationMedia struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	Mime      string `json:"mime"`
}

type PresentationProgram struct {
	ID           string            `json:"id"`
	BlockID      string            `json:"blockId,omitempty"`
	ItemID       string            `json:"itemId,omitempty"`
	ItemPosition int               `json:"itemPosition,omitempty"`
	Occurrence   int               `json:"occurrence,omitempty"`
	ChannelID    string            `json:"channelId"`
	StartsAt     time.Time         `json:"startsAt"`
	EndsAt       time.Time         `json:"endsAt"`
	Title        string            `json:"title"`
	Subtitle     string            `json:"subtitle,omitempty"`
	Media        PresentationMedia `json:"media"`
	Placard      string            `json:"placard"`
	Fit          string            `json:"fit"`
	Loop         bool              `json:"loop"`
	Muted        bool              `json:"muted"`
	Volume       float64           `json:"volume"`
}

type PresentationBridge struct {
	ProtocolVersion string `json:"protocolVersion"`
	State           string `json:"state"`
}

type PresentationRemote struct {
	URL       string    `json:"url"`
	ShowQR    bool      `json:"showQr"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// PresentationBuilder resolves a deployment's channels, blocks and playlists
// into the guide document for a window around now.
type PresentationBuilder struct {
	Repository  Repository
	Programming ProgrammingRepository
	Artifacts   ArtifactRepository
	Now         func() time.Time
}

func (b PresentationBuilder) now() time.Time {
	if b.Now != nil {
		return b.Now().UTC()
	}
	return time.Now().UTC()
}

// Build computes the live presentation for deploymentID. Programs whose media
// is unavailable or not playable by the guide are omitted rather than failing
// the whole guide.
func (b PresentationBuilder) Build(ctx context.Context, deploymentID string) (Presentation, error) {
	if b.Repository == nil || b.Programming == nil || b.Artifacts == nil {
		return Presentation{}, errors.New("cable presentation repositories are required")
	}
	d, err := b.Repository.Deployment(ctx, deploymentID)
	if err != nil {
		return Presentation{}, err
	}
	theme, err := NormalizeTheme(d.Theme)
	if err != nil {
		theme = ThemeNebula
	}
	timezone, err := NormalizeTimezone(d.Timezone)
	if err != nil {
		timezone = "UTC"
	}
	density, err := NormalizeGuideDensity(d.GuideDensity)
	if err != nil {
		density = "half-hour"
	}
	horizon := time.Duration(d.GuideHorizonSeconds) * time.Second
	if horizon <= 0 {
		horizon = 24 * time.Hour
	}
	now := b.now()
	windowStart, windowEnd := now.Add(-guideLookback), now.Add(horizon)

	channels, err := b.allChannels(ctx, deploymentID)
	if err != nil {
		return Presentation{}, err
	}
	sort.Slice(channels, func(i, j int) bool {
		if channels[i].Number != channels[j].Number {
			return channels[i].Number < channels[j].Number
		}
		return channels[i].ID < channels[j].ID
	})
	result := Presentation{Channels: []PresentationChannel{}, Blocks: []PresentationBlock{}, Occurrences: []PresentationProgram{}, Theme: theme, Bridge: PresentationBridge{ProtocolVersion: GuideBridgeProtocol, State: "online"}}
	media := map[string]*PresentationMedia{}
	for _, ch := range channels {
		pc := PresentationChannel{ID: ch.ID, Number: ch.Number, Name: ch.Name, Callsign: ch.Callsign, Accent: ch.Accent, Enabled: ch.Enabled}
		if ch.Standby {
			pc.StandbyMessage = "Standby"
		}
		result.Channels = append(result.Channels, pc)
		blocks, err := b.allBlocks(ctx, ch.ID)
		if err != nil {
			return Presentation{}, err
		}
		sort.Slice(blocks, func(i, j int) bool {
			if blocks[i].StartsAt.Equal(blocks[j].StartsAt) {
				return blocks[i].ID < blocks[j].ID
			}
			return blocks[i].StartsAt.Before(blocks[j].StartsAt)
		})
		for _, block := range blocks {
			if !block.EndsAt.After(windowStart) || !block.StartsAt.Before(windowEnd) || !block.EndsAt.After(block.StartsAt) {
				continue
			}
			programs, err := b.blockPrograms(ctx, ch.ID, block, windowStart, windowEnd, media)
			if err != nil {
				return Presentation{}, err
			}
			source := "playlist"
			if block.AssetID != "" {
				source = "asset"
			}
			result.Blocks = append(result.Blocks, PresentationBlock{ID: block.ID, ChannelID: ch.ID, StartsAt: block.StartsAt.UTC(), EndsAt: block.EndsAt.UTC(), Title: block.Title, Source: source})
			result.Occurrences = append(result.Occurrences, programs...)
		}
	}
	// The revision digest identifies the authored content, not the request
	// time, so cable-web can tell when the schedule actually changed.
	content, err := json.Marshal(struct {
		DeploymentID string                `json:"deploymentId"`
		Version      int64                 `json:"version"`
		Theme        string                `json:"theme"`
		Timezone     string                `json:"timezone"`
		Density      string                `json:"guideDensity"`
		Channels     []PresentationChannel `json:"channels"`
		Blocks       []PresentationBlock   `json:"blocks"`
		Occurrences  []PresentationProgram `json:"occurrences"`
	}{d.ID, d.Version, theme, timezone, density, result.Channels, result.Blocks, result.Occurrences})
	if err != nil {
		return Presentation{}, fmt.Errorf("marshal cable presentation: %w", err)
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	result.Revision = PresentationRevision{ID: digest, Digest: "sha256:" + digest, GeneratedAt: now, Timezone: timezone, GuideDensity: density}
	return result, nil
}

func (b PresentationBuilder) blockPrograms(ctx context.Context, channelID string, block Block, windowStart, windowEnd time.Time, media map[string]*PresentationMedia) ([]PresentationProgram, error) {
	program := func(id, itemID string, position, occurrence int, assetID, title, subtitle string, startsAt, endsAt time.Time) (PresentationProgram, bool, error) {
		m, err := b.media(ctx, assetID, media)
		if err != nil || m == nil {
			return PresentationProgram{}, false, err
		}
		return PresentationProgram{ID: id, BlockID: block.ID, ItemID: itemID, ItemPosition: position, Occurrence: occurrence, ChannelID: channelID, StartsAt: startsAt.UTC(), EndsAt: endsAt.UTC(), Title: title, Subtitle: subtitle, Media: *m, Placard: "start", Fit: "fill", Loop: true, Volume: 100}, true, nil
	}
	var out []PresentationProgram
	if block.AssetID != "" {
		p, ok, err := program(block.ID, "", 0, 0, block.AssetID, block.Title, "", block.StartsAt, block.EndsAt)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p)
		}
		return out, nil
	}
	if block.PlaylistID == "" {
		return nil, nil
	}
	playlist, err := b.Programming.Playlist(ctx, block.PlaylistID)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]PlaylistItem, 0, len(playlist.Items))
	for _, item := range playlist.Items {
		if item.AssetID != "" && item.DurationSeconds > 0 {
			items = append(items, item)
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Position != items[j].Position {
			return items[i].Position < items[j].Position
		}
		return items[i].ID < items[j].ID
	})
	blockEnd := block.EndsAt.UTC()
	cursor := block.StartsAt.UTC()
	for occurrence := 1; cursor.Before(blockEnd) && cursor.Before(windowEnd); occurrence++ {
		item := items[(occurrence-1)%len(items)]
		itemEnd := cursor.Add(time.Duration(item.DurationSeconds) * time.Second)
		if itemEnd.After(blockEnd) {
			itemEnd = blockEnd
		}
		if itemEnd.After(windowStart) {
			p, ok, err := program(block.ID+":"+strconv.Itoa(occurrence), item.ID, item.Position, occurrence, item.AssetID, item.Title, item.Subtitle, cursor, itemEnd)
			if err != nil {
				return nil, err
			}
			if ok {
				out = append(out, p)
			}
		}
		cursor = itemEnd
	}
	return out, nil
}

// media resolves the guide media descriptor for assetID; nil means the asset
// has no ready artifact the guide can play.
func (b PresentationBuilder) media(ctx context.Context, assetID string, cache map[string]*PresentationMedia) (*PresentationMedia, error) {
	if m, ok := cache[assetID]; ok {
		return m, nil
	}
	artifacts, err := b.Artifacts.CableArtifactsForAsset(ctx, assetID)
	if err != nil {
		return nil, err
	}
	var result *PresentationMedia
	for _, a := range artifacts {
		if a.State != "ready" {
			continue
		}
		mime := strings.ToLower(strings.TrimSpace(a.MediaType))
		kind := ""
		switch mime {
		case "video/mp4":
			kind = "video"
		case "image/jpeg", "image/png", "image/webp":
			kind = "image"
		}
		if kind != "" {
			result = &PresentationMedia{Kind: kind, Reference: GuideMediaPath + url.PathEscape(assetID), Mime: mime}
			break
		}
	}
	cache[assetID] = result
	return result, nil
}

func (b PresentationBuilder) allBlocks(ctx context.Context, channelID string) ([]Block, error) {
	var out []Block
	cursor := ""
	for {
		p, err := b.Programming.ListBlocks(ctx, channelID, cursor, 100)
		if err != nil {
			return nil, err
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

func (b PresentationBuilder) allChannels(ctx context.Context, deploymentID string) ([]Channel, error) {
	var out []Channel
	cursor := ""
	for {
		p, err := b.Repository.ListChannels(ctx, deploymentID, cursor, 100)
		if err != nil {
			return nil, err
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
