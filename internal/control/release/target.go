package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/media"
	canonical "github.com/jmill/cable/internal/release"
)

var _ assignment.Publisher = TargetPublisher{}

// ErrPlaylistEmpty rejects assigning a playlist without items.
var ErrPlaylistEmpty = errors.New("playlist has no items")

// schedule bounds: a playlist release loops its items for at most
// scheduleHorizon and never lists more than scheduleMaxItems occurrences.
const (
	scheduleHorizon  = 24 * time.Hour
	scheduleMaxItems = 2000
)

// TargetMedia is the library view of an assignable media item.
type TargetMedia struct {
	ID        string
	Kind      string
	AssetID   string
	URL       string
	AppAccess bool
	Playback  media.Playback
}

// PlaylistEntry is one playlist item: a file asset with its original
// artifact, or a web media item, shown for DurationSeconds.
type PlaylistEntry struct {
	ID              string
	DurationSeconds int
	AssetID         string
	Artifact        Artifact
	Web             *TargetMedia
}

type TargetCatalog interface {
	// TargetMedia and TargetPlaylist report assignment.ErrTargetNotFound for
	// unknown IDs.
	TargetMedia(context.Context, string) (TargetMedia, error)
	TargetPlaylist(context.Context, string) ([]PlaylistEntry, error)
}

// TargetPublisher turns an assignment target into a signed release: file
// media become media presentations, web media web presentations (carrying the
// media ID when the item has app access), and playlists media schedules.
// Identical payloads reuse the existing release by digest.
type TargetPublisher struct {
	Media                  Service
	Catalog                TargetCatalog
	Releases               Repository
	Signer                 Signer
	MinimumProtocolVersion string
	Now                    func() time.Time
}

func (p TargetPublisher) PublishTarget(ctx context.Context, actorID, idempotencyKey, mediaID, playlistID string) (canonical.EnvelopeV1, error) {
	mutation := Mutation{ActorID: actorID, IdempotencyKey: idempotencyKey}
	if mutation.ActorID == "" || mutation.IdempotencyKey == "" {
		return canonical.EnvelopeV1{}, ErrInvalidMutation
	}
	if p.Catalog == nil || p.Releases == nil || p.Signer == nil || p.MinimumProtocolVersion == "" {
		return canonical.EnvelopeV1{}, errors.New("target release publisher is not configured")
	}
	if playlistID != "" {
		result, err := p.publish(ctx, mutation, "playlist:"+playlistID, func() (PreparedPublication, error) {
			entries, err := p.Catalog.TargetPlaylist(ctx, playlistID)
			if err != nil {
				return PreparedPublication{}, err
			}
			return p.prepareSchedule(entries)
		})
		return result.Envelope, err
	}
	target, err := p.Catalog.TargetMedia(ctx, mediaID)
	if err != nil {
		return canonical.EnvelopeV1{}, err
	}
	if target.Kind == media.KindWeb {
		web := canonical.WebPresentation{URL: target.URL}
		request := "web:" + target.URL
		if target.AppAccess {
			web.AppAccess, web.MediaID = true, target.ID
			request += "#app:" + target.ID
		}
		result, err := p.publish(ctx, mutation, request, func() (PreparedPublication, error) {
			return p.prepareWeb(web)
		})
		return result.Envelope, err
	}
	result, err := p.Media.Publish(ctx, mutation, PublishInput{Playback: canonical.PlaybackPayload{
		AssetID: target.AssetID, Fit: target.Playback.Fit, Loop: target.Playback.Loop, Muted: target.Playback.Muted,
		Volume: target.Playback.Volume, Orientation: target.Playback.Orientation,
		AllowedDeliveryMode: []string{DeliveryCacheThenPlay, DeliveryStream},
	}})
	return result.Envelope, err
}

func (p TargetPublisher) publish(ctx context.Context, mutation Mutation, request string, prepare func() (PreparedPublication, error)) (PublishResult, error) {
	sum := sha256.Sum256([]byte(request))
	publication := Publication{ActorID: mutation.ActorID, IdempotencyKey: mutation.IdempotencyKey, RequestDigest: hex.EncodeToString(sum[:])}
	return signAndPublish(ctx, p.Releases, p.Signer, publication, prepare)
}

// signAndPublish runs a publication through the repository and signs the
// prepared payload when a new release is created.
func signAndPublish(ctx context.Context, releases Repository, signer Signer, publication Publication, prepare func() (PreparedPublication, error)) (PublishResult, error) {
	result, err := releases.Publish(ctx, publication, prepare, func(releaseID string, prepared PreparedPublication) (canonical.EnvelopeV1, error) {
		if releaseID == "" {
			return canonical.EnvelopeV1{}, fmt.Errorf("%w: empty release ID", ErrSignerContract)
		}
		envelope, err := signer.Sign(ctx, releaseID, prepared.Payload)
		if err != nil {
			return canonical.EnvelopeV1{}, err
		}
		if err := validateEnvelope(envelope, releaseID, prepared.Digest); err != nil {
			return canonical.EnvelopeV1{}, err
		}
		return envelope, nil
	})
	if err != nil {
		return PublishResult{}, err
	}
	if err := validateEnvelope(result.Envelope, result.Envelope.ID, result.Envelope.Digest); err != nil {
		return PublishResult{}, err
	}
	return result, nil
}

func (p TargetPublisher) prepareWeb(web canonical.WebPresentation) (PreparedPublication, error) {
	if err := canonical.ValidateWebURL(web.URL); err != nil {
		return PreparedPublication{}, fmt.Errorf("%w: %v", media.ErrInvalidArgument, err)
	}
	return digestPayload(canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: p.MinimumProtocolVersion,
		Presentation:           canonical.Presentation{Kind: canonical.PresentationWeb, Web: &web},
		RequiredFeatures:       []string{"chromium"},
	})
}

// prepareSchedule loops the playlist from now until the horizon. Web items
// make the release require the "web" feature.
func (p TargetPublisher) prepareSchedule(entries []PlaylistEntry) (PreparedPublication, error) {
	if len(entries) == 0 {
		return PreparedPublication{}, ErrPlaylistEmpty
	}
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	start := now().UTC().Truncate(time.Second)
	horizon := start.Add(scheduleHorizon)
	artifacts := map[string]canonical.Artifact{}
	features := []string{}
	for _, entry := range entries {
		if entry.DurationSeconds <= 0 {
			return PreparedPublication{}, fmt.Errorf("%w: playlist item %s has no duration", media.ErrInvalidArgument, entry.ID)
		}
		if entry.Web != nil {
			features = append(features, canonical.FeatureWeb)
			continue
		}
		if err := validateArtifact(entry.AssetID, entry.Artifact, Profile{AcceptedMediaTypes: []string{"video/mp4", "image/jpeg", "image/png", "image/webp"}}); err != nil {
			return PreparedPublication{}, err
		}
		artifacts[entry.Artifact.Digest] = canonical.Artifact{Digest: entry.Artifact.Digest, Size: entry.Artifact.Size, MediaType: entry.Artifact.MediaType}
	}
	items := make([]canonical.MediaScheduleItem, 0, len(entries))
	at := start
	for occurrence := 0; at.Before(horizon) && len(items) < scheduleMaxItems; occurrence++ {
		entry := entries[occurrence%len(entries)]
		end := at.Add(time.Duration(entry.DurationSeconds) * time.Second)
		item := canonical.MediaScheduleItem{ID: entry.ID + "#" + strconv.Itoa(occurrence/len(entries)), StartsAt: at.Format(time.RFC3339), EndsAt: end.Format(time.RFC3339)}
		if entry.Web != nil {
			item.Web = &canonical.MediaScheduleWebItem{URL: entry.Web.URL}
		} else {
			item.ArtifactDigest, item.MediaType = entry.Artifact.Digest, entry.Artifact.MediaType
			item.Fit, item.Loop, item.Volume = "fit", true, 100
		}
		items = append(items, item)
		at = end
	}
	closure := make([]string, 0, len(artifacts))
	payloadArtifacts := make([]canonical.Artifact, 0, len(artifacts))
	for digest, artifact := range artifacts {
		closure = append(closure, digest)
		payloadArtifacts = append(payloadArtifacts, artifact)
	}
	slices.Sort(closure)
	slices.SortFunc(payloadArtifacts, func(a, b canonical.Artifact) int { return strings.Compare(a.Digest, b.Digest) })
	slices.Sort(features)
	return digestPayload(canonical.PayloadV1{
		SchemaVersion:          canonical.SchemaVersion,
		MinimumProtocolVersion: p.MinimumProtocolVersion,
		Presentation: canonical.Presentation{Kind: canonical.PresentationMediaSchedule, MediaSchedule: &canonical.MediaSchedulePresentation{
			Timezone:             "UTC",
			Window:               canonical.MediaScheduleWindow{StartsAt: start.Format(time.RFC3339), EndsAt: at.Format(time.RFC3339)},
			Items:                items,
			ArtifactClosure:      closure,
			AllowedDeliveryModes: []string{DeliveryCacheThenPlay, DeliveryStream},
		}},
		Artifacts:        payloadArtifacts,
		RequiredFeatures: slices.Compact(features),
	})
}

func digestPayload(payload canonical.PayloadV1) (PreparedPublication, error) {
	digest, _, err := canonical.Digest(payload)
	if err != nil {
		return PreparedPublication{}, fmt.Errorf("build release payload: %w", err)
	}
	return PreparedPublication{Payload: payload, Digest: digest}, nil
}
