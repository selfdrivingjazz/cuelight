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
	f := &fakeCableRepo{d: CableDeployment{ID: "d", Version: 2, Timezone: "UTC"}, ch: []Channel{{ID: "c", Number: 7, Name: "News", Enabled: true}}, b: ShellBuild{ID: "b", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Entrypoint: "index.html", BridgeProtocol: "1.0", ArtifactDigests: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}}
	f.p = map[string][]ProgramEntry{"c": {{ID: "p", ChannelID: "c", AssetID: "a", StartsAt: now, EndsAt: now.Add(time.Hour), Title: "Brief"}}}
	a := fakeCableArtifacts{"a": {{AssetID: "a", Digest: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc", MediaType: "video/mp4", State: "ready"}}, "shell": {{Digest: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", MediaType: "text/html", State: "ready"}}}
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
	if len(one.ArtifactDigests) != 3 {
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
	f := &fakeCableRepo{d: CableDeployment{ID: "d", Version: 1, Timezone: "UTC"}, ch: []Channel{{ID: "c", Number: 1}}, b: ShellBuild{ID: "b", Digest: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Entrypoint: "index", BridgeProtocol: "1"}}
	f.p = map[string][]ProgramEntry{"c": {{ID: "a", StartsAt: now, EndsAt: now.Add(2 * time.Hour)}, {ID: "b", StartsAt: now.Add(time.Hour), EndsAt: now.Add(3 * time.Hour)}}}
	a := fakeCableArtifacts{"x": {{Digest: "x", MediaType: "video/mp4", State: "ready"}}}
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
