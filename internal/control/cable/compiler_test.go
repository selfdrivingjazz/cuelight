package cable

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeCableRepo struct {
	d         CableDeployment
	ch        []Channel
	p         map[string][]ProgramEntry
	b         ShellBuild
	published []CableRevision
}

func TestCompilerDeterministicDigestAndClosure(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	f := &fakeCableRepo{d: CableDeployment{ID: "d", Version: 2, Timezone: "UTC", Theme: "nebula"}, ch: []Channel{{ID: "c", Number: 7, Name: "News", Enabled: true}}, b: ShellBuild{ID: "b", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Entrypoint: "index.html", BridgeProtocol: "1.0", ArtifactDigests: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}
	f.p = map[string][]ProgramEntry{"c": {{ID: "p", ChannelID: "c", AssetID: "a", StartsAt: now, EndsAt: now.Add(time.Hour), Title: "Brief"}}}
	a := fakeCableArtifacts{"a": {{AssetID: "a", Digest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", MediaType: "video/mp4", State: "ready"}}, "shell": {{Digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MediaType: "text/html", State: "ready"}}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/x-tar", State: "ready"}}, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb": {{Digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MediaType: "text/html", State: "ready"}}}
	c := Compiler{Repository: f, Artifacts: a, Now: func() time.Time { return now }}
	w := RevisionWindow{StartsAt: now, EndsAt: now.Add(2 * time.Hour)}
	one, e := c.Compile(context.Background(), "actor", "d", "b", w)
	if e != nil {
		t.Fatal(e)
	}
	two, e := c.Compile(context.Background(), "actor", "d", "b", w)
	if e != nil {
		t.Fatal(e)
	}
	if one.Digest != two.Digest || one.ID != one.Digest {
		t.Fatalf("non-deterministic digest: %s %s", one.Digest, two.Digest)
	}
	if len(one.ArtifactDigests) != 4 {
		t.Fatalf("closure=%v", one.ArtifactDigests)
	}
}
func (f *fakeCableRepo) CreateDeployment(context.Context, Mutation, DeploymentInput) (CableDeployment, error) {
	return f.d, nil
}
func (f *fakeCableRepo) Deployment(context.Context, string) (CableDeployment, error) { return f.d, nil }
func (f *fakeCableRepo) UpdateDeployment(context.Context, Mutation, string, DeploymentInput) (CableDeployment, error) {
	return f.d, nil
}
func (f *fakeCableRepo) ListDeployments(context.Context, string, int) (DeploymentPage, error) {
	return DeploymentPage{}, nil
}
func (f *fakeCableRepo) DeleteDeployment(context.Context, Mutation, string) error { return nil }
func (f *fakeCableRepo) CreateChannel(context.Context, Mutation, ChannelInput) (Channel, error) {
	return Channel{}, nil
}
func (f *fakeCableRepo) Channel(context.Context, string) (Channel, error) { return Channel{}, nil }
func (f *fakeCableRepo) UpdateChannel(context.Context, Mutation, string, ChannelInput) (Channel, error) {
	return Channel{}, nil
}
func (f *fakeCableRepo) ListChannels(context.Context, string, string, int) (ChannelPage, error) {
	return ChannelPage{Items: f.ch}, nil
}
func (f *fakeCableRepo) DeleteChannel(context.Context, Mutation, string) error { return nil }
func (f *fakeCableRepo) CreateProgramEntry(context.Context, Mutation, ProgramInput) (ProgramEntry, error) {
	return ProgramEntry{}, nil
}
func (f *fakeCableRepo) ProgramEntry(context.Context, string) (ProgramEntry, error) {
	return ProgramEntry{}, nil
}
func (f *fakeCableRepo) UpdateProgramEntry(context.Context, Mutation, string, ProgramInput) (ProgramEntry, error) {
	return ProgramEntry{}, nil
}
func (f *fakeCableRepo) ListProgramEntries(_ context.Context, id, cursor string, _ int) (ProgramPage, error) {
	return ProgramPage{Items: f.p[id]}, nil
}
func (f *fakeCableRepo) DeleteProgramEntry(context.Context, Mutation, string) error { return nil }
func (f *fakeCableRepo) RegisterShellBuild(context.Context, Mutation, ShellBuildInput) (ShellBuild, error) {
	return f.b, nil
}
func (f *fakeCableRepo) ShellBuild(context.Context, string) (ShellBuild, error) { return f.b, nil }
func (f *fakeCableRepo) ListShellBuilds(context.Context, string, int) (ShellBuildPage, error) {
	return ShellBuildPage{}, nil
}
func (f *fakeCableRepo) CreateRevision(_ context.Context, r CableRevision) error {
	f.published = append(f.published, r)
	return nil
}
func (f *fakeCableRepo) Revision(context.Context, string) (CableRevision, error) {
	return CableRevision{}, nil
}
func (f *fakeCableRepo) ListRevisions(context.Context, string, string, int) (RevisionPage, error) {
	return RevisionPage{}, nil
}

type fakeCableArtifacts map[string][]Artifact

func (f fakeCableArtifacts) CableArtifactsForAsset(_ context.Context, id string) ([]Artifact, error) {
	a := f[id]
	if len(a) == 0 {
		return nil, errors.New("none")
	}
	return a, nil
}
func (f fakeCableArtifacts) CableArtifactByDigest(_ context.Context, id string) (Artifact, error) {
	for _, as := range f {
		for _, a := range as {
			if a.Digest == id {
				return a, nil
			}
		}
	}
	return Artifact{}, errors.New("none")
}
func TestCompilerRejectsOverlapAndPublishRequiresVersion(t *testing.T) {
	now := time.Unix(0, 0).UTC()
	f := &fakeCableRepo{d: CableDeployment{ID: "d", Version: 1, Timezone: "UTC", Theme: "nebula"}, ch: []Channel{{ID: "c", Number: 1}}, b: ShellBuild{ID: "b", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Entrypoint: "index", BridgeProtocol: "1", ArtifactDigests: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	f.p = map[string][]ProgramEntry{"c": {{ID: "a", StartsAt: now, EndsAt: now.Add(2 * time.Hour)}, {ID: "b", StartsAt: now.Add(time.Hour), EndsAt: now.Add(3 * time.Hour)}}}
	a := fakeCableArtifacts{"x": {{Digest: "x", MediaType: "video/mp4", State: "ready"}}, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/x-tar", State: "ready"}}}
	c := Compiler{Repository: f, Artifacts: a}
	_, e := c.Preview(context.Background(), "actor", "d", "b", RevisionWindow{StartsAt: now, EndsAt: now.Add(4 * time.Hour)})
	if !errors.Is(e, ErrOverlap) {
		t.Fatalf("error=%v", e)
	}
	_, e = c.Publish(context.Background(), Mutation{ActorID: "actor"}, "d", "b", RevisionWindow{StartsAt: now, EndsAt: now.Add(4 * time.Hour)})
	if !errors.Is(e, ErrConflict) {
		t.Fatalf("publish error=%v", e)
	}
}

type blockCableRepo struct {
	*fakeCableRepo
	blocks    map[string][]Block
	playlists map[string]Playlist
}

func (f *blockCableRepo) CreatePlaylist(context.Context, Mutation, PlaylistInput) (Playlist, error) {
	return Playlist{}, nil
}
func (f *blockCableRepo) Playlist(_ context.Context, id string) (Playlist, error) {
	return f.playlists[id], nil
}
func (f *blockCableRepo) ListPlaylists(context.Context, string, int) (PlaylistPage, error) {
	return PlaylistPage{}, nil
}
func (f *blockCableRepo) CreateBlock(context.Context, Mutation, BlockInput) (Block, error) {
	return Block{}, nil
}
func (f *blockCableRepo) ListBlocks(_ context.Context, channelID, _ string, _ int) (BlockPage, error) {
	return BlockPage{Items: f.blocks[channelID]}, nil
}
func (f *blockCableRepo) CreateProfile(context.Context, Mutation, ProfileInput) (Profile, error) {
	return Profile{}, nil
}
func (f *blockCableRepo) Profile(context.Context, string) (Profile, error) {
	return Profile{}, nil
}
func (f *blockCableRepo) ListProfiles(context.Context, string, int) (ProfilePage, error) {
	return ProfilePage{}, nil
}

func TestCompilerMaterializesPlaylistWrapAndNextBlock(t *testing.T) {
	now := time.Date(2026, 8, 27, 0, 0, 0, 0, time.UTC)
	base := &fakeCableRepo{d: CableDeployment{ID: "d", Version: 1, Timezone: "UTC", Theme: "nebula"}, ch: []Channel{{ID: "c", Number: 1}}, b: ShellBuild{ID: "b", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Entrypoint: "index", BridgeProtocol: "1", ArtifactDigests: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}
	repo := &blockCableRepo{fakeCableRepo: base, blocks: map[string][]Block{"c": {
		{ID: "b1", ChannelID: "c", PlaylistID: "pl", StartsAt: now, EndsAt: now.Add(10 * time.Minute)},
		{ID: "b2", ChannelID: "c", AssetID: "direct", StartsAt: now.Add(10 * time.Minute), EndsAt: now.Add(12 * time.Minute)},
	}}, playlists: map[string]Playlist{"pl": {ID: "pl", Items: []PlaylistItem{
		{ID: "i1", PlaylistID: "pl", AssetID: "a", Position: 1, DurationSeconds: 240, Title: "A"},
		{ID: "i2", PlaylistID: "pl", AssetID: "b", Position: 2, DurationSeconds: 180, Title: "B"},
	}}}}
	artifacts := fakeCableArtifacts{
		"a":      {{AssetID: "a", Digest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", MediaType: "video/mp4", State: "ready"}},
		"b":      {{AssetID: "b", Digest: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", MediaType: "video/mp4", State: "ready"}},
		"direct": {{AssetID: "direct", Digest: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", MediaType: "video/mp4", State: "ready"}},
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa": {{Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", MediaType: "application/x-tar", State: "ready"}},
	}
	rev, err := (Compiler{Repository: repo, Artifacts: artifacts, Now: func() time.Time { return now }}).Compile(context.Background(), "actor", "d", "b", RevisionWindow{StartsAt: now, EndsAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	channel := rev.Channels[0]
	if len(channel.Blocks) != 2 || len(channel.Programs) != 4 {
		t.Fatalf("blocks=%d programs=%d", len(channel.Blocks), len(channel.Programs))
	}
	if got, want := channel.Blocks[0].Items[2].StartsAt, now.Add(7*time.Minute); !got.Equal(want) {
		t.Fatalf("playlist wrap starts at %s, want %s", got, want)
	}
	if got, want := channel.Blocks[0].Items[2].EndsAt, now.Add(10*time.Minute); !got.Equal(want) {
		t.Fatalf("playlist boundary ends at %s, want %s", got, want)
	}
	if got := channel.Programs[3].StartsAt; !got.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("next block starts at %s", got)
	}
	if !channel.Programs[3].Loop || !channel.Blocks[0].Loop {
		t.Fatal("block and materialized media must loop")
	}
}

func TestValidateBlockRequiresExactlyOneSource(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	for _, input := range []BlockInput{
		{ChannelID: "c", StartsAt: at, EndsAt: at.Add(time.Hour)},
		{ChannelID: "c", PlaylistID: "p", AssetID: "a", StartsAt: at, EndsAt: at.Add(time.Hour)},
		{ChannelID: "c", AssetID: "a", StartsAt: at, EndsAt: at},
	} {
		if err := ValidateBlock(input); err == nil {
			t.Fatalf("ValidateBlock(%+v) accepted invalid source/timing", input)
		}
	}
}

func TestValidateBlockOrderUsesHalfOpenIntervals(t *testing.T) {
	at := time.Unix(0, 0).UTC()
	adjacent := []Block{
		{ID: "a", PlaylistID: "p", StartsAt: at, EndsAt: at.Add(time.Hour)},
		{ID: "b", AssetID: "x", StartsAt: at.Add(time.Hour), EndsAt: at.Add(2 * time.Hour)},
	}
	if err := validateBlockOrder(adjacent); err != nil {
		t.Fatalf("adjacent blocks overlap: %v", err)
	}
	adjacent[1].StartsAt = at.Add(30 * time.Minute)
	if err := validateBlockOrder(adjacent); !errors.Is(err, ErrOverlap) {
		t.Fatalf("overlap error=%v", err)
	}
}

func TestNormalizeGuideDensityDefaultsToHalfHour(t *testing.T) {
	for input, want := range map[string]string{"": "half-hour", "quarter-hour": "quarter-hour", "half-hour": "half-hour", "hour": "hour"} {
		got, err := NormalizeGuideDensity(input)
		if err != nil || got != want {
			t.Fatalf("NormalizeGuideDensity(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := NormalizeGuideDensity("comfortable"); err == nil {
		t.Fatal("legacy non-time density accepted")
	}
}
