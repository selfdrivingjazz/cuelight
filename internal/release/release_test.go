package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"slices"
	"testing"
)

func fixture() PayloadV1 {
	return PayloadV1{
		SchemaVersion:          SchemaVersion,
		MinimumProtocolVersion: ProtocolV1,
		Playback: PlaybackPayload{
			AssetID:             "018f0000-0000-7000-8000-000000000001",
			Fit:                 "fit",
			Loop:                true,
			Volume:              80,
			ImageDurationSecond: 10,
			Orientation:         0,
			AllowedDeliveryMode: []string{"stream", "cache_then_play"},
		},
		Artifacts: []Artifact{
			{Digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Size: 2, MediaType: "video/mp4"},
			{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Size: 1, MediaType: "image/jpeg"},
		},
		RequiredFeatures: []string{"mpv", "passive-v1"},
	}
}

func TestCanonicalNormalizesSetOrder(t *testing.T) {
	left := fixture()
	right := fixture()
	right.Artifacts[0], right.Artifacts[1] = right.Artifacts[1], right.Artifacts[0]
	right.RequiredFeatures[0], right.RequiredFeatures[1] = right.RequiredFeatures[1], right.RequiredFeatures[0]
	right.Playback.AllowedDeliveryMode[0], right.Playback.AllowedDeliveryMode[1] = right.Playback.AllowedDeliveryMode[1], right.Playback.AllowedDeliveryMode[0]

	leftDigest, leftBytes, err := Digest(left)
	if err != nil {
		t.Fatal(err)
	}
	rightDigest, rightBytes, err := Digest(right)
	if err != nil {
		t.Fatal(err)
	}
	if leftDigest != rightDigest || string(leftBytes) != string(rightBytes) {
		t.Fatalf("semantic input changed identity:\n%s\n%s", leftBytes, rightBytes)
	}
}

func TestEnvelopeProvenanceDoesNotChangePayloadDigest(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	one, err := Sign("release-one", "publisher-one", fixture(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	two, err := Sign("release-two", "publisher-one", fixture(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if one.Digest != two.Digest {
		t.Fatal("release ID changed canonical payload digest")
	}
	if err := Verify(one, "publisher-one", publicKey, ProtocolV1); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRejectsTamperingAndUnknownKey(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Sign("release-one", "publisher-one", fixture(), privateKey)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(EnvelopeV1) EnvelopeV1{
		"payload": func(value EnvelopeV1) EnvelopeV1 { value.Payload.Playback.Volume++; return value },
		"digest": func(value EnvelopeV1) EnvelopeV1 {
			value.Digest = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			return value
		},
		"signature": func(value EnvelopeV1) EnvelopeV1 { value.Signature = "AAAA"; return value },
		"schema":    func(value EnvelopeV1) EnvelopeV1 { value.Payload.SchemaVersion = "2.0"; return value },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			if err := Verify(mutate(envelope), "publisher-one", publicKey, ProtocolV1); err == nil {
				t.Fatal("tampered envelope verified")
			}
		})
	}
	if err := Verify(envelope, "publisher-two", publicKey, ProtocolV1); err == nil {
		t.Fatal("unknown publisher key verified")
	}
}

func TestVerifyComparesProtocolVersionsNumerically(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	payload := fixture()
	payload.MinimumProtocolVersion = "1.10"
	envelope, err := Sign("release-one", "publisher-one", payload, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(envelope, "publisher-one", publicKey, "1.2"); err == nil {
		t.Fatal("older numeric protocol version accepted")
	}
	if err := Verify(envelope, "publisher-one", publicKey, "1.10"); err != nil {
		t.Fatal(err)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := Sign("release-one", "publisher-one", fixture(), privateKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	var decoded EnvelopeV1
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Digest != envelope.Digest {
		t.Fatal("round trip changed digest")
	}
}

func TestCablePresentationIsTypedAndClosureIsExact(t *testing.T) {
	shell := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	revision := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	media := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	payload := PayloadV1{
		SchemaVersion:          SchemaVersion,
		MinimumProtocolVersion: ProtocolV1,
		Presentation: Presentation{
			Kind: PresentationCable,
			Cable: &CablePresentation{
				ShellBuildDigest: shell, RevisionArtifactDigest: revision,
				Entrypoint: "index.html", BridgeProtocol: ProtocolV1,
				ArtifactClosure: []string{shell, revision, media},
			},
		},
		Artifacts: []Artifact{
			{Digest: shell, Size: 10, MediaType: "text/html"},
			{Digest: revision, Size: 20, MediaType: "application/json"},
			{Digest: media, Size: 30, MediaType: "video/mp4"},
		},
	}
	digest, encoded, err := Digest(payload)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains([]string{string(PresentationCable)}, string(payload.PresentationMode())) {
		t.Fatalf("presentation kind = %q", payload.PresentationMode())
	}
	if !bytes.Contains(encoded, []byte(`"kind":"cable"`)) {
		t.Fatalf("canonical payload omitted cable discriminator: %s", encoded)
	}
	if digest == "" {
		t.Fatal("missing cable digest")
	}
	payload.Presentation.Cable.ArtifactClosure = payload.Presentation.Cable.ArtifactClosure[:2]
	if _, _, err := Digest(payload); err == nil {
		t.Fatal("incomplete cable closure accepted")
	}
}
