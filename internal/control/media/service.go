package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
)

type Service struct {
	repository Repository
	temporary  TemporaryStore
	cas        CAS
	probe      CertifiedProbe
	newID      IDGenerator
	limits     Limits
}

func NewService(repository Repository, temporary TemporaryStore, cas CAS, probe CertifiedProbe, newID IDGenerator, limits Limits) (*Service, error) {
	if repository == nil || temporary == nil || cas == nil || probe == nil || newID == nil {
		return nil, &ValidationError{Field: "dependencies", Reason: "repository, temporary store, CAS, probe, and ID generator are required"}
	}
	if limits.MaxUploadBytes <= 0 || limits.MaxChunkBytes <= 0 || limits.MaxNameBytes <= 0 || limits.MaxMediaTypeBytes <= 0 {
		return nil, &ValidationError{Field: "limits", Reason: "all limits must be positive"}
	}
	if limits.MaxChunkBytes > limits.MaxUploadBytes {
		return nil, &ValidationError{Field: "limits", Reason: "chunk limit cannot exceed upload limit"}
	}
	return &Service{
		repository: repository,
		temporary:  temporary,
		cas:        cas,
		probe:      probe,
		newID:      newID,
		limits:     limits,
	}, nil
}

func (s *Service) CreateUpload(ctx context.Context, mutation Mutation, request CreateUploadRequest) (Upload, error) {
	if err := validateMutation(mutation, true); err != nil {
		return Upload{}, err
	}
	if err := validateText("name", request.Name, s.limits.MaxNameBytes); err != nil {
		return Upload{}, err
	}
	if err := validateText("mediaType", request.MediaType, s.limits.MaxMediaTypeBytes); err != nil {
		return Upload{}, err
	}
	if request.Length <= 0 {
		return Upload{}, &ValidationError{Field: "length", Reason: "must be positive"}
	}
	if request.Length > s.limits.MaxUploadBytes {
		return Upload{}, &LimitError{Kind: ErrUploadTooLarge, Limit: s.limits.MaxUploadBytes, Value: request.Length}
	}
	uploadID := s.newID()
	if strings.TrimSpace(uploadID) == "" {
		return Upload{}, errors.New("media ID generator returned an empty upload ID")
	}
	upload := Upload{
		ID:             uploadID,
		Name:           request.Name,
		DeclaredLength: request.Length,
		MediaType:      request.MediaType,
		State:          UploadOpen,
		CreatedBy:      mutation.ActorID,
	}
	return s.repository.CreateUpload(ctx, mutation, upload)
}

func (s *Service) Upload(ctx context.Context, uploadID string) (Upload, error) {
	if strings.TrimSpace(uploadID) == "" {
		return Upload{}, &ValidationError{Field: "uploadId", Reason: "is required"}
	}
	return s.repository.Upload(ctx, uploadID)
}

func (s *Service) AppendChunk(ctx context.Context, mutation Mutation, uploadID string, request AppendChunkRequest) (Upload, error) {
	if err := validateMutation(mutation, false); err != nil {
		return Upload{}, err
	}
	if strings.TrimSpace(uploadID) == "" {
		return Upload{}, &ValidationError{Field: "uploadId", Reason: "is required"}
	}
	if request.Body == nil {
		return Upload{}, &ValidationError{Field: "body", Reason: "is required"}
	}
	if request.Offset < 0 {
		return Upload{}, &ValidationError{Field: "offset", Reason: "must be non-negative"}
	}
	if request.Length <= 0 {
		return Upload{}, &ValidationError{Field: "length", Reason: "must be positive"}
	}
	if request.Length > s.limits.MaxChunkBytes {
		return Upload{}, &LimitError{Kind: ErrChunkTooLarge, Limit: s.limits.MaxChunkBytes, Value: request.Length}
	}

	upload, err := s.repository.Upload(ctx, uploadID)
	if err != nil {
		return Upload{}, err
	}
	if upload.State != UploadOpen {
		return Upload{}, &StateError{UploadID: upload.ID, State: upload.State}
	}
	if request.Offset > upload.ReceivedLength {
		return Upload{}, &OffsetError{Expected: upload.ReceivedLength, Actual: request.Offset}
	}
	if request.Length > upload.DeclaredLength-request.Offset {
		return Upload{}, &LimitError{Kind: ErrUploadTooLarge, Limit: upload.DeclaredLength, Value: request.Offset + request.Length}
	}
	end := request.Offset + request.Length
	if request.Offset < upload.ReceivedLength && end > upload.ReceivedLength {
		return Upload{}, &OffsetError{Expected: upload.ReceivedLength, Actual: request.Offset}
	}
	if request.Offset < upload.ReceivedLength {
		storedRange, rangeErr := s.repository.UploadRange(ctx, upload.ID, request.Offset)
		if rangeErr != nil || storedRange.Start != request.Offset || storedRange.End != end {
			return Upload{}, &OffsetError{Expected: upload.ReceivedLength, Actual: request.Offset}
		}
	}

	written, err := s.temporary.WriteChunk(ctx, upload.ID, request.Offset, request.Length, request.Body)
	if err != nil {
		return Upload{}, err
	}
	if request.Offset < upload.ReceivedLength {
		if written.Disposition != ChunkIdentical || written.Size < upload.ReceivedLength {
			return Upload{}, ErrChunkConflict
		}
		return upload, nil
	}
	if written.Disposition != ChunkAppended && written.Disposition != ChunkIdentical {
		return Upload{}, fmt.Errorf("temporary store returned invalid chunk disposition %d", written.Disposition)
	}
	if written.Size != end {
		return Upload{}, fmt.Errorf("temporary store reported size %d after writing through offset %d", written.Size, end)
	}

	advanced, err := s.repository.AdvanceUpload(ctx, upload.ID, upload.ReceivedLength, end)
	if !errors.Is(err, ErrConflict) {
		return advanced, err
	}
	current, getErr := s.repository.Upload(ctx, upload.ID)
	if getErr != nil {
		return Upload{}, err
	}
	if current.State == UploadOpen && current.ReceivedLength >= end {
		storedRange, rangeErr := s.repository.UploadRange(ctx, upload.ID, request.Offset)
		if rangeErr == nil && storedRange.Start == request.Offset && storedRange.End == end {
			return current, nil
		}
	}
	return Upload{}, err
}

func (s *Service) CompleteUpload(ctx context.Context, mutation Mutation, uploadID string) (Asset, error) {
	if err := validateMutation(mutation, true); err != nil {
		return Asset{}, err
	}
	if strings.TrimSpace(uploadID) == "" {
		return Asset{}, &ValidationError{Field: "uploadId", Reason: "is required"}
	}
	upload, err := s.repository.Upload(ctx, uploadID)
	if err != nil {
		return Asset{}, err
	}
	switch upload.State {
	case UploadComplete:
		return s.repository.CompletedAsset(ctx, upload.ID)
	case UploadFailed:
		return Asset{}, &StateError{UploadID: upload.ID, State: upload.State}
	case UploadOpen:
		if upload.ReceivedLength != upload.DeclaredLength {
			return Asset{}, fmt.Errorf("%w: received %d of %d bytes", ErrUploadIncomplete, upload.ReceivedLength, upload.DeclaredLength)
		}
		upload, err = s.repository.BeginFinalization(ctx, upload.ID, upload.ReceivedLength)
		if err != nil {
			if errors.Is(err, ErrConflict) {
				current, getErr := s.repository.Upload(ctx, upload.ID)
				if getErr != nil {
					return Asset{}, err
				}
				if current.State == UploadComplete {
					return s.repository.CompletedAsset(ctx, upload.ID)
				}
				if current.State != UploadFinalizing {
					return Asset{}, err
				}
				upload = current
			} else {
				return Asset{}, err
			}
		}
	case UploadFinalizing:
		// Retrying finalization is safe: CAS identity is content-addressed and
		// repository completion is idempotent for this upload.
	default:
		return Asset{}, &StateError{UploadID: upload.ID, State: upload.State}
	}

	reader, err := s.temporary.Open(ctx, upload.ID)
	if err != nil {
		return Asset{}, err
	}
	object, putErr := s.cas.Put(&contextReader{ctx: ctx, reader: io.LimitReader(reader, upload.DeclaredLength+1)}, "")
	closeErr := reader.Close()
	if putErr != nil {
		return Asset{}, putErr
	}
	if closeErr != nil {
		return Asset{}, closeErr
	}
	if object.Size != upload.DeclaredLength {
		s.failUpload(ctx, upload.ID, "temporary upload length changed during finalization")
		return Asset{}, fmt.Errorf("%w: expected %d bytes, observed %d", ErrSourceChanged, upload.DeclaredLength, object.Size)
	}
	if err := localcas.ValidateDigest(object.Digest); err != nil {
		return Asset{}, fmt.Errorf("CAS returned invalid digest: %w", err)
	}
	certification, err := s.probe.Probe(ctx, object)
	if err != nil {
		return Asset{}, err
	}
	if !certification.Certified || strings.TrimSpace(certification.MediaType) == "" {
		s.failUpload(ctx, upload.ID, "media probe did not certify content")
		return Asset{}, ErrMediaUncertified
	}
	declaredType := canonicalMediaType(upload.MediaType)
	observedType := canonicalMediaType(certification.MediaType)
	if observedType != declaredType {
		s.failUpload(ctx, upload.ID, "probed media type differs from declared media type")
		return Asset{}, fmt.Errorf("%w: declared %q, observed %q", ErrMediaTypeMismatch, upload.MediaType, certification.MediaType)
	}
	artifact := Artifact{Digest: object.Digest, Size: object.Size, MediaType: observedType, State: ArtifactReady}
	assetID := s.newID()
	if strings.TrimSpace(assetID) == "" {
		return Asset{}, errors.New("media ID generator returned an empty asset ID")
	}
	asset := Asset{ID: assetID, Name: upload.Name, Artifact: artifact}
	completed, err := s.repository.CompleteFinalization(ctx, mutation, upload.ID, artifact, asset)
	if err != nil {
		return Asset{}, err
	}
	_ = s.temporary.Remove(ctx, upload.ID)
	return completed, nil
}

func (s *Service) failUpload(ctx context.Context, uploadID, reason string) {
	_ = s.repository.FailUpload(ctx, uploadID, reason)
}

func validateMutation(mutation Mutation, idempotencyRequired bool) error {
	if strings.TrimSpace(mutation.ActorID) == "" {
		return &ValidationError{Field: "actorId", Reason: "is required"}
	}
	if idempotencyRequired && strings.TrimSpace(mutation.IdempotencyKey) == "" {
		return &ValidationError{Field: "idempotencyKey", Reason: "is required"}
	}
	return nil
}

func validateText(field, value string, maximum int) error {
	if strings.TrimSpace(value) == "" {
		return &ValidationError{Field: field, Reason: "is required"}
	}
	if len(value) > maximum {
		return &ValidationError{Field: field, Reason: fmt.Sprintf("must be at most %d bytes", maximum)}
	}
	return nil
}

func canonicalMediaType(value string) string {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(value))
	if err != nil {
		parsed = strings.ToLower(strings.TrimSpace(value))
	}
	switch parsed {
	case "application/x-gzip":
		return "application/gzip"
	case "application/x-tar":
		return "application/tar"
	default:
		return parsed
	}
}
