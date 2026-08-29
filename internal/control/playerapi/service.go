package playerapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/player/client"
	"github.com/jmill/cable/internal/protocol"
	canonical "github.com/jmill/cable/internal/release"
)

func NewService(repository Repository, loader ReleaseLoader, cas CASReader, limits Limits) (*Service, error) {
	if repository == nil || loader == nil || cas == nil {
		return nil, errors.New("player API requires repository, release loader, and CAS reader")
	}
	limits = limits.normalized()
	return &Service{Repository: repository, ReleaseLoader: loader, CASReader: cas, Limits: limits}, nil
}

func (s *Service) nodeID(r *http.Request) (string, error) {
	if s.Identity != nil {
		id, err := s.Identity(r)
		if err != nil || id == "" {
			return "", fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		return id, nil
	}
	id, err := protocol.NodeIdentity(r, s.RevocationChecker)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrUnauthorized, err)
	}
	return id, nil
}

func (s *Service) PutCapabilities(ctx context.Context, nodeID string, report client.CapabilityReport) error {
	if err := s.configured(); err != nil {
		return err
	}
	if strings.TrimSpace(report.Platform) == "" || strings.TrimSpace(report.Architecture) == "" || strings.TrimSpace(report.ProtocolVersion) == "" || strings.TrimSpace(report.RendererVersion) == "" || strings.TrimSpace(report.ProfileID) == "" {
		return fmt.Errorf("%w: platform, architecture, protocolVersion, rendererVersion, and profileId are required", ErrInvalidRequest)
	}
	if report.FreeBytes < 0 {
		return fmt.Errorf("%w: freeBytes must not be negative", ErrInvalidRequest)
	}
	return s.Repository.PutCapabilities(ctx, nodeID, report)
}

func (s *Service) GetAssignment(ctx context.Context, nodeID, ifNoneMatch string, wait time.Duration) (client.AssignmentResult, error) {
	if err := s.configured(); err != nil {
		return client.AssignmentResult{}, err
	}
	limits := s.Limits.normalized()
	if wait < 0 || wait > limits.MaxWait {
		return client.AssignmentResult{}, ErrWaitTooLong
	}
	value, ok, err := s.Repository.CurrentAssignment(ctx, nodeID)
	if err != nil {
		return client.AssignmentResult{}, err
	}
	if !ok {
		return client.AssignmentResult{}, ErrNotFound
	}
	etag := assignmentETag(value.DesiredGeneration)
	if etagMatches(ifNoneMatch, etag) && wait > 0 {
		err = s.Repository.WaitForAssignment(ctx, nodeID, value.DesiredGeneration, wait)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return client.AssignmentResult{}, err
			}
			return client.AssignmentResult{}, err
		}
		value, ok, err = s.Repository.CurrentAssignment(ctx, nodeID)
		if err != nil {
			return client.AssignmentResult{}, err
		}
		if !ok {
			return client.AssignmentResult{}, ErrNotFound
		}
		etag = assignmentETag(value.DesiredGeneration)
	}
	if etagMatches(ifNoneMatch, etag) {
		return client.AssignmentResult{ETag: etag, NotModified: true}, nil
	}
	kind := canonical.PresentationKind("")
	if kind == "" {
		envelope, loadErr := s.ReleaseLoader.LoadRelease(ctx, value.ReleaseID)
		if loadErr != nil || envelope.ID != value.ReleaseID || (value.ReleaseDigest != "" && envelope.Digest != value.ReleaseDigest) {
			return client.AssignmentResult{}, fmt.Errorf("%w: release binding mismatch", ErrForbidden)
		}
		kind = envelope.Payload.PresentationMode()
		if kind != canonical.PresentationMedia && kind != canonical.PresentationCable {
			return client.AssignmentResult{}, fmt.Errorf("%w: invalid release presentation", ErrForbidden)
		}
	}
	valueClient := toClientAssignment(value)
	valueClient.PresentationKind = kind
	return client.AssignmentResult{Assignment: valueClient, ETag: etag}, nil
}

func (s *Service) FetchRelease(ctx context.Context, nodeID, releaseID string) (canonical.EnvelopeV1, error) {
	if err := s.configured(); err != nil {
		return canonical.EnvelopeV1{}, err
	}
	if releaseID == "" {
		return canonical.EnvelopeV1{}, ErrNotFound
	}
	assigned, ok, err := s.Repository.AssignedRelease(ctx, nodeID, releaseID)
	if err != nil {
		return canonical.EnvelopeV1{}, err
	}
	if !ok || assigned.NodeID != nodeID || assigned.ReleaseID != releaseID {
		return canonical.EnvelopeV1{}, ErrForbidden
	}
	envelope, err := s.ReleaseLoader.LoadRelease(ctx, releaseID)
	if err != nil {
		return canonical.EnvelopeV1{}, err
	}
	if envelope.ID != releaseID || (assigned.ReleaseDigest != "" && envelope.Digest != assigned.ReleaseDigest) {
		return canonical.EnvelopeV1{}, fmt.Errorf("%w: release binding mismatch", ErrForbidden)
	}
	return envelope, nil
}

func (s *Service) Report(ctx context.Context, nodeID string, report client.NodeReport) (bool, error) {
	if err := s.configured(); err != nil {
		return false, err
	}
	if report.EventEpoch == "" || report.Sequence <= 0 || report.ObservedGeneration < 0 || report.DesiredGenerationSeen < 0 {
		return false, fmt.Errorf("%w: invalid report sequence or epoch", ErrInvalidRequest)
	}
	if report.PresentationKind != "" && report.PresentationKind != canonical.PresentationMedia && report.PresentationKind != canonical.PresentationCable {
		return false, fmt.Errorf("%w: invalid presentationKind", ErrInvalidRequest)
	}
	return s.Repository.RecordReport(ctx, nodeID, report)
}

func (s *Service) AppendEvents(ctx context.Context, nodeID string, batch client.EventBatch) (client.EventAcknowledgement, error) {
	if err := s.configured(); err != nil {
		return client.EventAcknowledgement{}, err
	}
	limits := s.Limits.normalized()
	if batch.Epoch == "" || len(batch.Events) == 0 || len(batch.Events) > limits.MaxEvents {
		return client.EventAcknowledgement{}, ErrBatchTooLarge
	}
	seenSequences := make(map[int64]client.NodeEvent, len(batch.Events))
	seenIDs := make(map[string]client.NodeEvent, len(batch.Events))
	for index, event := range batch.Events {
		if event.Epoch != batch.Epoch {
			return client.EventAcknowledgement{}, ErrEpochMismatch
		}
		if event.Sequence <= 0 || event.ID == "" || event.Type == "" {
			return client.EventAcknowledgement{}, fmt.Errorf("%w: event identity and sequence are required", ErrInvalidRequest)
		}
		batch.Events[index] = event
		if old, ok := seenSequences[event.Sequence]; ok && !sameEvent(old, event) {
			return client.EventAcknowledgement{}, ErrEventConflict
		}
		if old, ok := seenIDs[event.ID]; ok && !sameEvent(old, event) {
			return client.EventAcknowledgement{}, ErrEventConflict
		}
		seenSequences[event.Sequence] = event
		seenIDs[event.ID] = event
	}
	acknowledgement, err := s.Repository.AppendEvents(ctx, nodeID, batch)
	if err != nil {
		return client.EventAcknowledgement{}, err
	}
	if acknowledgement.Epoch == "" {
		acknowledgement.Epoch = batch.Epoch
	}
	if acknowledgement.Epoch != batch.Epoch {
		return client.EventAcknowledgement{}, ErrEpochMismatch
	}
	if acknowledgement.AcknowledgedThrough < 0 {
		return client.EventAcknowledgement{}, fmt.Errorf("%w: negative acknowledgement", ErrInvalidRequest)
	}
	return acknowledgement, nil
}

func (s *Service) HeadArtifact(ctx context.Context, nodeID, digest string) (client.ArtifactMetadata, error) {
	if err := s.configured(); err != nil {
		return client.ArtifactMetadata{}, err
	}
	if err := canonical.ValidateDigest(digest); err != nil {
		return client.ArtifactMetadata{}, ErrNotFound
	}
	if ok, err := s.authorizedArtifact(ctx, nodeID, digest); err != nil {
		return client.ArtifactMetadata{}, err
	} else if !ok {
		return client.ArtifactMetadata{}, ErrForbidden
	}
	stat, err := s.CASReader.Stat(ctx, digest)
	if err != nil {
		return client.ArtifactMetadata{}, ErrNotFound
	}
	if stat.Size < 0 {
		return client.ArtifactMetadata{}, fmt.Errorf("%w: negative size", ErrNotFound)
	}
	return client.ArtifactMetadata{Size: stat.Size, ETag: artifactETag(digest), AcceptRanges: "bytes"}, nil
}

func (s *Service) GetArtifact(ctx context.Context, nodeID, digest string, requested *client.ByteRange, ifRange string) (client.ArtifactContent, error) {
	metadata, err := s.HeadArtifact(ctx, nodeID, digest)
	if err != nil {
		return client.ArtifactContent{}, err
	}
	start, end, partial := int64(0), metadata.Size-1, false
	if requested != nil && ifRangeMatches(ifRange, metadata.ETag) {
		var ok bool
		start, end, ok = resolveRange(*requested, metadata.Size)
		if !ok {
			return client.ArtifactContent{}, ErrRangeUnsatisfiable
		}
		partial = true
	}
	if metadata.Size == 0 {
		start, end = 0, -1
	}
	body, err := s.CASReader.Open(ctx, digest)
	if err != nil {
		return client.ArtifactContent{}, ErrNotFound
	}
	if partial {
		if start < 0 {
			// resolveRange turns suffix ranges into a concrete offset.
			_ = body.Close()
			return client.ArtifactContent{}, ErrRangeUnsatisfiable
		}
		if _, err := io.CopyN(io.Discard, body, start); err != nil {
			_ = body.Close()
			return client.ArtifactContent{}, ErrNotFound
		}
		body = &sectionReadCloser{Reader: io.LimitReader(body, end-start+1), closer: body}
	}
	content := client.ArtifactContent{Body: body, Size: end - start + 1, ETag: metadata.ETag, AcceptRanges: "bytes", Partial: partial}
	if partial {
		content.ContentRange = fmt.Sprintf("bytes %d-%d/%d", start, end, metadata.Size)
	}
	return content, nil
}

func (s *Service) authorizedArtifact(ctx context.Context, nodeID, digest string) (bool, error) {
	return s.Repository.AuthorizeArtifact(ctx, nodeID, digest)
}

func assignmentETag(generation int64) string { return fmt.Sprintf("\"assignment-%d\"", generation) }
func artifactETag(digest string) string      { return fmt.Sprintf("\"%s\"", digest) }
func toClientAssignment(value assignment.Assignment) client.Assignment {
	return client.Assignment{ID: value.ID, NodeID: value.NodeID, ReleaseID: value.ReleaseID, ReleaseDigest: value.ReleaseDigest, DeliveryMode: value.DeliveryMode, ExpectedGeneration: value.ExpectedGeneration, DesiredGeneration: value.DesiredGeneration, RemoteInput: value.RemoteInput, ShowQR: value.ShowQR}
}

func sameEvent(a, b client.NodeEvent) bool {
	return a.ID == b.ID && a.Epoch == b.Epoch && a.Sequence == b.Sequence && a.Type == b.Type && a.OccurredAt.Equal(b.OccurredAt) && a.CoversThrough == b.CoversThrough && reflect.DeepEqual(a.Data, b.Data)
}
func ifRangeMatches(value, etag string) bool { return value == "" || strings.TrimSpace(value) == etag }
func resolveRange(value client.ByteRange, size int64) (int64, int64, bool) {
	if size <= 0 {
		return 0, 0, false
	}
	if value.Start < 0 {
		length := -value.Start
		if length <= 0 {
			return 0, 0, false
		}
		if length > size {
			length = size
		}
		return size - length, size - 1, true
	}
	start := value.Start
	end := size - 1
	if value.End != nil {
		end = *value.End
	}
	if start >= size || end < start {
		return 0, 0, false
	}
	if end >= size {
		end = size - 1
	}
	return start, end, true
}

type sectionReadCloser struct {
	io.Reader
	closer io.Closer
}

func (s *sectionReadCloser) Close() error { return s.closer.Close() }

func parseByteRange(value string) (*client.ByteRange, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "bytes=") {
		return nil, ErrRangeUnsatisfiable
	}
	spec := strings.TrimSpace(strings.TrimPrefix(value, "bytes="))
	if strings.Contains(spec, ",") || spec == "" {
		return nil, ErrRangeUnsatisfiable
	}
	parts := strings.Split(spec, "-")
	if len(parts) != 2 {
		return nil, ErrRangeUnsatisfiable
	}
	if parts[0] == "" { // suffix ranges are represented by a negative start and resolved by the handler.
		n, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || n <= 0 {
			return nil, ErrRangeUnsatisfiable
		}
		return &client.ByteRange{Start: -n}, nil
	}
	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return nil, ErrRangeUnsatisfiable
	}
	result := &client.ByteRange{Start: start}
	if parts[1] != "" {
		end, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return nil, ErrRangeUnsatisfiable
		}
		result.End = &end
	}
	return result, nil
}
func etagMatches(value, etag string) bool {
	for _, candidate := range strings.Split(value, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || candidate == etag || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
