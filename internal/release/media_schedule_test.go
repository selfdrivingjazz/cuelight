package release

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestSignedMediaScheduleFixtureIsDeterministicAndVerifies(t *testing.T) {
	image := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	video := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	payload := PayloadV1{
		SchemaVersion: SchemaVersion, MinimumProtocolVersion: ProtocolV1,
		Presentation: Presentation{Kind: PresentationMediaSchedule, MediaSchedule: &MediaSchedulePresentation{
			Timezone: "America/New_York", Window: MediaScheduleWindow{StartsAt: "2026-09-01T12:00:00Z", EndsAt: "2026-09-01T14:00:00Z"},
			Items: []MediaScheduleItem{
				{ID: "video", StartsAt: "2026-09-01T13:00:00Z", EndsAt: "2026-09-01T14:00:00Z", ArtifactDigest: video, MediaType: "video/mp4", Fit: "fill", Loop: true, Volume: 70},
				{ID: "image", StartsAt: "2026-09-01T12:00:00Z", EndsAt: "2026-09-01T13:00:00Z", ArtifactDigest: image, MediaType: "image/png", Fit: "fit", Volume: 40},
			}, ArtifactClosure: []string{video, image}, AllowedDeliveryModes: []string{"cache_then_play", "stream"},
		}},
		Artifacts: []Artifact{{Digest: video, Size: 2, MediaType: "video/mp4"}, {Digest: image, Size: 1, MediaType: "image/png"}},
	}
	first, firstJSON, err := Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	second, secondJSON, err := Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || string(firstJSON) != string(secondJSON) {
		t.Fatal("schedule digest is not deterministic")
	}
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	public := private.Public().(ed25519.PublicKey)
	envelope, err := Sign("schedule-release", "publisher", payload, private)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(envelope, "publisher", public, ProtocolV1); err != nil {
		t.Fatal(err)
	}
}

func TestMediaScheduleRejectsOverlapAndIncompleteClosure(t *testing.T) {
	digest := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	payload := PayloadV1{SchemaVersion: SchemaVersion, MinimumProtocolVersion: ProtocolV1, Presentation: Presentation{Kind: PresentationMediaSchedule, MediaSchedule: &MediaSchedulePresentation{
		Timezone: "UTC", Window: MediaScheduleWindow{StartsAt: "2026-09-01T00:00:00Z", EndsAt: "2026-09-01T02:00:00Z"},
		Items: []MediaScheduleItem{{ID: "a", StartsAt: "2026-09-01T00:00:00Z", EndsAt: "2026-09-01T01:30:00Z", ArtifactDigest: digest, MediaType: "image/png", Fit: "fit"}, {ID: "b", StartsAt: "2026-09-01T01:00:00Z", EndsAt: "2026-09-01T02:00:00Z", ArtifactDigest: digest, MediaType: "image/png", Fit: "fit"}}, ArtifactClosure: []string{digest}, AllowedDeliveryModes: []string{"cache_then_play"},
	}}, Artifacts: []Artifact{{Digest: digest, Size: 1, MediaType: "image/png"}}}
	if _, _, err := Digest(payload); err == nil {
		t.Fatal("overlapping schedule accepted")
	}
	payload.Presentation.MediaSchedule.Items[1].StartsAt = "2026-09-01T01:30:00Z"
	payload.Presentation.MediaSchedule.ArtifactClosure = nil
	if _, _, err := Digest(payload); err == nil {
		t.Fatal("incomplete artifact closure accepted")
	}
	payload.Presentation.MediaSchedule.ArtifactClosure = []string{digest}
	payload.Presentation.MediaSchedule.Items[0].EndsAt = "2026-09-01T01:00:00Z"
	if _, _, err := Digest(payload); err == nil {
		t.Fatal("schedule gap accepted")
	}
}
