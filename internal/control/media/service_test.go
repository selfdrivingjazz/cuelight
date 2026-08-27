package media

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/jmill/cable/internal/media/localcas"
)

func TestUploadBoundsAndStrictOffsets(t *testing.T) {
	service, repository, temporary, _ := newTestService(t)
	mutation := Mutation{ActorID: "actor-1", IdempotencyKey: "create-1"}

	if _, err := service.CreateUpload(context.Background(), mutation, CreateUploadRequest{Name: "large", Length: 9, MediaType: "video/mp4"}); !errors.Is(err, ErrUploadTooLarge) {
		t.Fatalf("oversized upload error = %v", err)
	}
	upload, err := service.CreateUpload(context.Background(), mutation, CreateUploadRequest{Name: "clip", Length: 8, MediaType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}
	appendMutation := Mutation{ActorID: "actor-1"}
	if _, err := service.AppendChunk(context.Background(), appendMutation, upload.ID, AppendChunkRequest{Offset: 1, Length: 1, Body: bytes.NewReader([]byte("a"))}); !errors.Is(err, ErrOffsetMismatch) {
		t.Fatalf("noncontiguous offset error = %v", err)
	}
	if _, err := service.AppendChunk(context.Background(), appendMutation, upload.ID, AppendChunkRequest{Offset: 0, Length: 5, Body: bytes.NewReader([]byte("abcde"))}); !errors.Is(err, ErrChunkTooLarge) {
		t.Fatalf("oversized chunk error = %v", err)
	}
	advanced, err := service.AppendChunk(context.Background(), appendMutation, upload.ID, AppendChunkRequest{Offset: 0, Length: 4, Body: bytes.NewReader([]byte("abcd"))})
	if err != nil {
		t.Fatal(err)
	}
	if advanced.ReceivedLength != 4 || string(temporary.data[upload.ID]) != "abcd" {
		t.Fatalf("unexpected upload after append: %#v, bytes %q", advanced, temporary.data[upload.ID])
	}
	if _, err := service.AppendChunk(context.Background(), appendMutation, upload.ID, AppendChunkRequest{Offset: 1, Length: 2, Body: bytes.NewReader([]byte("bc"))}); !errors.Is(err, ErrOffsetMismatch) {
		t.Fatalf("partial prior-range retry error = %v", err)
	}
	if _, err := service.AppendChunk(context.Background(), appendMutation, upload.ID, AppendChunkRequest{Offset: 3, Length: 2, Body: bytes.NewReader([]byte("de"))}); !errors.Is(err, ErrOffsetMismatch) {
		t.Fatalf("overlapping chunk error = %v", err)
	}
	if repository.uploads[upload.ID].ReceivedLength != 4 {
		t.Fatalf("rejected chunk advanced repository: %#v", repository.uploads[upload.ID])
	}
}

func TestChunkRetryAndFinalizationAreIdempotent(t *testing.T) {
	service, _, _, store := newTestService(t)
	ctx := context.Background()
	upload, err := service.CreateUpload(ctx, Mutation{ActorID: "actor-1", IdempotencyKey: "create"}, CreateUploadRequest{Name: "clip", Length: 8, MediaType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}
	appendMutation := Mutation{ActorID: "actor-1"}
	for _, request := range []AppendChunkRequest{
		{Offset: 0, Length: 4, Body: bytes.NewReader([]byte("abcd"))},
		{Offset: 0, Length: 4, Body: bytes.NewReader([]byte("abcd"))},
		{Offset: 4, Length: 4, Body: bytes.NewReader([]byte("efgh"))},
	} {
		if _, err := service.AppendChunk(ctx, appendMutation, upload.ID, request); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.AppendChunk(ctx, appendMutation, upload.ID, AppendChunkRequest{Offset: 0, Length: 4, Body: bytes.NewReader([]byte("abce"))}); !errors.Is(err, ErrChunkConflict) {
		t.Fatalf("different retry error = %v", err)
	}

	completeMutation := Mutation{ActorID: "actor-1", IdempotencyKey: "complete"}
	first, err := service.CompleteUpload(ctx, completeMutation, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.CompleteUpload(ctx, completeMutation, upload.ID)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("repeated finalization changed asset: %#v != %#v", first, second)
	}
	if store.puts != 1 {
		t.Fatalf("repeated finalization wrote CAS %d times", store.puts)
	}
}

func TestIdenticalUploadBytesConvergeOnArtifact(t *testing.T) {
	service, repository, _, _ := newTestService(t)
	ctx := context.Background()
	var assets []Asset
	for index := range 2 {
		upload, err := service.CreateUpload(ctx, Mutation{ActorID: "actor-1", IdempotencyKey: fmt.Sprintf("create-%d", index)}, CreateUploadRequest{Name: fmt.Sprintf("clip-%d", index), Length: 4, MediaType: "video/mp4"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.AppendChunk(ctx, Mutation{ActorID: "actor-1"}, upload.ID, AppendChunkRequest{Offset: 0, Length: 4, Body: bytes.NewReader([]byte("same"))}); err != nil {
			t.Fatal(err)
		}
		asset, err := service.CompleteUpload(ctx, Mutation{ActorID: "actor-1", IdempotencyKey: fmt.Sprintf("complete-%d", index)}, upload.ID)
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, asset)
	}
	if assets[0].ID == assets[1].ID {
		t.Fatal("distinct uploads unexpectedly converged on an asset ID")
	}
	if assets[0].Artifact.Digest != assets[1].Artifact.Digest {
		t.Fatalf("identical bytes produced different digests: %q, %q", assets[0].Artifact.Digest, assets[1].Artifact.Digest)
	}
	if len(repository.artifacts) != 1 {
		t.Fatalf("artifact repository contains %d rows", len(repository.artifacts))
	}
}

func TestUncertifiedProbeFailsUploadClosed(t *testing.T) {
	repository := newFakeRepository()
	temporary := &fakeTemporaryStore{data: make(map[string][]byte)}
	store := &fakeCAS{objects: make(map[string][]byte)}
	service, err := NewService(repository, temporary, store, fakeProbe{certified: false, mediaType: "video/mp4"}, sequenceIDs(), testLimits())
	if err != nil {
		t.Fatal(err)
	}
	upload, err := service.CreateUpload(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "create"}, CreateUploadRequest{Name: "clip", Length: 4, MediaType: "video/mp4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AppendChunk(context.Background(), Mutation{ActorID: "actor-1"}, upload.ID, AppendChunkRequest{Offset: 0, Length: 4, Body: bytes.NewReader([]byte("data"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteUpload(context.Background(), Mutation{ActorID: "actor-1", IdempotencyKey: "complete"}, upload.ID); !errors.Is(err, ErrMediaUncertified) {
		t.Fatalf("uncertified probe error = %v", err)
	}
	if repository.uploads[upload.ID].State != UploadFailed {
		t.Fatalf("uncertified upload state = %s", repository.uploads[upload.ID].State)
	}
}

func newTestService(t *testing.T) (*Service, *fakeRepository, *fakeTemporaryStore, *fakeCAS) {
	t.Helper()
	repository := newFakeRepository()
	temporary := &fakeTemporaryStore{data: make(map[string][]byte)}
	store := &fakeCAS{objects: make(map[string][]byte)}
	service, err := NewService(repository, temporary, store, fakeProbe{certified: true, mediaType: "video/mp4"}, sequenceIDs(), testLimits())
	if err != nil {
		t.Fatal(err)
	}
	return service, repository, temporary, store
}

func testLimits() Limits {
	return Limits{MaxUploadBytes: 8, MaxChunkBytes: 4, MaxNameBytes: 255, MaxMediaTypeBytes: 100}
}

func sequenceIDs() IDGenerator {
	next := 0
	return func() string {
		next++
		return fmt.Sprintf("id-%d", next)
	}
}

type fakeProbe struct {
	certified bool
	mediaType string
}

func (p fakeProbe) Probe(context.Context, localcas.Object) (ProbeCertification, error) {
	return ProbeCertification{Certified: p.certified, MediaType: p.mediaType, Profile: "test-certified"}, nil
}

type fakeCAS struct {
	objects map[string][]byte
	puts    int
}

func (s *fakeCAS) Put(reader io.Reader, expected string) (localcas.Object, error) {
	content, err := io.ReadAll(reader)
	if err != nil {
		return localcas.Object{}, err
	}
	sum := sha256.Sum256(content)
	digest := hex.EncodeToString(sum[:])
	if expected != "" && digest != expected {
		return localcas.Object{}, errors.New("digest mismatch")
	}
	s.puts++
	if existing, ok := s.objects[digest]; ok && !bytes.Equal(existing, content) {
		return localcas.Object{}, errors.New("digest collision")
	}
	s.objects[digest] = append([]byte(nil), content...)
	return localcas.Object{Digest: digest, Size: int64(len(content)), Path: "/cas/" + digest}, nil
}

func (s *fakeCAS) Stat(digest string) (localcas.Object, error) {
	content, ok := s.objects[digest]
	if !ok {
		return localcas.Object{}, ErrNotFound
	}
	return localcas.Object{Digest: digest, Size: int64(len(content)), Path: "/cas/" + digest}, nil
}

type fakeTemporaryStore struct {
	data map[string][]byte
}

func (s *fakeTemporaryStore) WriteChunk(_ context.Context, uploadID string, offset, length int64, reader io.Reader) (ChunkWrite, error) {
	content, err := io.ReadAll(io.LimitReader(reader, length+1))
	if err != nil {
		return ChunkWrite{}, err
	}
	if int64(len(content)) != length {
		return ChunkWrite{}, &ValidationError{Field: "body", Reason: "length does not match chunk body"}
	}
	stored := s.data[uploadID]
	if offset > int64(len(stored)) {
		return ChunkWrite{}, &OffsetError{Expected: int64(len(stored)), Actual: offset}
	}
	end := offset + length
	if offset < int64(len(stored)) {
		if end > int64(len(stored)) || !bytes.Equal(stored[offset:end], content) {
			return ChunkWrite{}, ErrChunkConflict
		}
		return ChunkWrite{Disposition: ChunkIdentical, Size: int64(len(stored))}, nil
	}
	s.data[uploadID] = append(stored, content...)
	return ChunkWrite{Disposition: ChunkAppended, Size: int64(len(s.data[uploadID]))}, nil
}

func (s *fakeTemporaryStore) Open(_ context.Context, uploadID string) (io.ReadCloser, error) {
	content, ok := s.data[uploadID]
	if !ok {
		return nil, ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (s *fakeTemporaryStore) Remove(_ context.Context, uploadID string) error {
	delete(s.data, uploadID)
	return nil
}

type fakeRepository struct {
	uploads         map[string]Upload
	artifacts       map[string]Artifact
	completedAssets map[string]Asset
	assets          map[string]Asset
	ranges          map[string]map[int64]UploadRange
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{
		uploads:         make(map[string]Upload),
		artifacts:       make(map[string]Artifact),
		completedAssets: make(map[string]Asset),
		assets:          make(map[string]Asset),
		ranges:          make(map[string]map[int64]UploadRange),
	}
}

func (r *fakeRepository) CreateUpload(_ context.Context, _ Mutation, upload Upload) (Upload, error) {
	if existing, ok := r.uploads[upload.ID]; ok {
		return existing, nil
	}
	r.uploads[upload.ID] = upload
	return upload, nil
}

func (r *fakeRepository) Upload(_ context.Context, id string) (Upload, error) {
	upload, ok := r.uploads[id]
	if !ok {
		return Upload{}, ErrNotFound
	}
	return upload, nil
}
func (r *fakeRepository) UploadRange(_ context.Context, id string, start int64) (UploadRange, error) {
	ranges := r.ranges[id]
	stored, ok := ranges[start]
	if !ok {
		return UploadRange{}, ErrNotFound
	}
	return stored, nil
}

func (r *fakeRepository) AdvanceUpload(_ context.Context, id string, expected, next int64) (Upload, error) {
	upload, ok := r.uploads[id]
	if !ok {
		return Upload{}, ErrNotFound
	}
	if upload.State != UploadOpen || upload.ReceivedLength != expected {
		return Upload{}, ErrConflict
	}
	upload.ReceivedLength = next
	if r.ranges[id] == nil {
		r.ranges[id] = make(map[int64]UploadRange)
	}
	r.ranges[id][expected] = UploadRange{Start: expected, End: next}
	r.uploads[id] = upload
	return upload, nil
}

func (r *fakeRepository) BeginFinalization(_ context.Context, id string, expected int64) (Upload, error) {
	upload, ok := r.uploads[id]
	if !ok {
		return Upload{}, ErrNotFound
	}
	if upload.State != UploadOpen || upload.ReceivedLength != expected || expected != upload.DeclaredLength {
		return Upload{}, ErrConflict
	}
	upload.State = UploadFinalizing
	r.uploads[id] = upload
	return upload, nil
}

func (r *fakeRepository) CompleteFinalization(_ context.Context, _ Mutation, uploadID string, artifact Artifact, asset Asset) (Asset, error) {
	upload := r.uploads[uploadID]
	if upload.State == UploadComplete {
		return r.completedAssets[uploadID], nil
	}
	if upload.State != UploadFinalizing {
		return Asset{}, ErrConflict
	}
	if existing, ok := r.artifacts[artifact.Digest]; ok && existing != artifact {
		return Asset{}, ErrConflict
	}
	r.artifacts[artifact.Digest] = artifact
	r.assets[asset.ID] = asset
	r.completedAssets[uploadID] = asset
	upload.State = UploadComplete
	upload.ArtifactDigest = artifact.Digest
	r.uploads[uploadID] = upload
	return asset, nil
}

func (r *fakeRepository) CompletedAsset(_ context.Context, uploadID string) (Asset, error) {
	asset, ok := r.completedAssets[uploadID]
	if !ok {
		return Asset{}, ErrNotFound
	}
	return asset, nil
}

func (r *fakeRepository) FailUpload(_ context.Context, uploadID, _ string) error {
	upload, ok := r.uploads[uploadID]
	if !ok {
		return ErrNotFound
	}
	upload.State = UploadFailed
	r.uploads[uploadID] = upload
	return nil
}

func (r *fakeRepository) RegisterAsset(_ context.Context, _ Mutation, asset Asset) (Asset, error) {
	if existing, ok := r.artifacts[asset.Artifact.Digest]; ok && existing != asset.Artifact {
		return Asset{}, ErrConflict
	}
	r.artifacts[asset.Artifact.Digest] = asset.Artifact
	r.assets[asset.ID] = asset
	return asset, nil
}

func TestCanonicalMediaTypeAcceptsStandardAndLegacyArchiveAliases(t *testing.T) {
	if canonicalMediaType("application/gzip") != canonicalMediaType("application/x-gzip") {
		t.Fatal("gzip aliases are not equivalent")
	}
	if canonicalMediaType("application/tar") != canonicalMediaType("application/x-tar") {
		t.Fatal("tar aliases are not equivalent")
	}
}
