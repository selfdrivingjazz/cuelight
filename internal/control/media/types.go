package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"

	"github.com/selfdrivingjazz/cuelight/internal/media/localcas"
)

var (
	ErrInvalidArgument      = errors.New("invalid media argument")
	ErrNotFound             = errors.New("media record not found")
	ErrConflict             = errors.New("media record changed concurrently")
	ErrUploadTooLarge       = errors.New("upload exceeds configured limit")
	ErrChunkTooLarge        = errors.New("chunk exceeds configured limit")
	ErrOffsetMismatch       = errors.New("upload offset mismatch")
	ErrChunkConflict        = errors.New("chunk retry differs from stored bytes")
	ErrUploadIncomplete     = errors.New("upload is incomplete")
	ErrInvalidState         = errors.New("invalid upload state")
	ErrMediaUncertified     = errors.New("media probe did not certify content")
	ErrMediaTypeMismatch    = errors.New("probed media type differs from declared media type")
	ErrRegistrationDisabled = errors.New("server-local registration is disabled")
	ErrPathNotAllowed       = errors.New("local path is outside allowlisted roots")
	ErrUnsafeSource         = errors.New("local source is not a safely opened regular file")
	ErrSourceChanged        = errors.New("local source changed while being copied")
)

type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason) }
func (e *ValidationError) Unwrap() error { return ErrInvalidArgument }

type LimitError struct {
	Kind  error
	Limit int64
	Value int64
}

func (e *LimitError) Error() string {
	return fmt.Sprintf("%v: limit %d, received %d", e.Kind, e.Limit, e.Value)
}
func (e *LimitError) Unwrap() error { return e.Kind }

type OffsetError struct {
	Expected int64
	Actual   int64
}

func (e *OffsetError) Error() string {
	return fmt.Sprintf("%v: expected %d, received %d", ErrOffsetMismatch, e.Expected, e.Actual)
}
func (e *OffsetError) Unwrap() error { return ErrOffsetMismatch }

type StateError struct {
	UploadID string
	State    UploadState
}

func (e *StateError) Error() string {
	return fmt.Sprintf("%v: upload %s is %s", ErrInvalidState, e.UploadID, e.State)
}
func (e *StateError) Unwrap() error { return ErrInvalidState }

type Mutation struct {
	ActorID        string
	IdempotencyKey string
}

type UploadState string

const (
	UploadOpen       UploadState = "open"
	UploadFinalizing UploadState = "finalizing"
	UploadComplete   UploadState = "complete"
	UploadFailed     UploadState = "failed"
)

type ArtifactState string

const (
	ArtifactReady       ArtifactState = "ready"
	ArtifactQuarantined ArtifactState = "quarantined"
	ArtifactMissing     ArtifactState = "missing"
)

type Upload struct {
	ID             string
	Name           string
	DeclaredLength int64
	ReceivedLength int64
	MediaType      string
	State          UploadState
	ArtifactDigest string
	CreatedBy      string
}
type UploadRange struct {
	Start int64
	End   int64
}

type Artifact struct {
	Digest    string        `json:"digest"`
	Size      int64         `json:"size"`
	MediaType string        `json:"mediaType"`
	State     ArtifactState `json:"state"`
}

type Asset struct {
	ID       string
	Name     string
	Artifact Artifact
}

type UploadRepository interface {
	CreateUpload(context.Context, Mutation, Upload) (Upload, error)
	Upload(context.Context, string) (Upload, error)
	UploadRange(context.Context, string, int64) (UploadRange, error)
	AdvanceUpload(context.Context, string, int64, int64) (Upload, error)
	BeginFinalization(context.Context, string, int64) (Upload, error)
	// CompleteFinalization atomically upserts the digest-keyed artifact,
	// creates and links the asset, and marks the upload complete. Repeating it
	// for a completed upload returns that upload's original asset.
	CompleteFinalization(context.Context, Mutation, string, Artifact, Asset) (Asset, error)
	CompletedAsset(context.Context, string) (Asset, error)
	FailUpload(context.Context, string, string) error
}

type AssetRepository interface {
	// RegisterAsset atomically upserts the digest-keyed artifact and creates
	// the linked asset, with idempotency scoped by the supplied mutation.
	RegisterAsset(context.Context, Mutation, Asset) (Asset, error)
}

type Repository interface {
	UploadRepository
	AssetRepository
}

type ChunkDisposition uint8

const (
	ChunkAppended ChunkDisposition = iota + 1
	ChunkIdentical
)

type ChunkWrite struct {
	Disposition ChunkDisposition
	Size        int64
}

// TemporaryStore owns incomplete upload bytes. WriteChunk must consume exactly
// length bytes, reject short or long bodies, append only at its current size,
// and report ChunkIdentical only when a retry exactly matches existing bytes.
type TemporaryStore interface {
	WriteChunk(context.Context, string, int64, int64, io.Reader) (ChunkWrite, error)
	Open(context.Context, string) (io.ReadCloser, error)
	Remove(context.Context, string) error
}

type CAS interface {
	Put(io.Reader, string) (localcas.Object, error)
	Stat(string) (localcas.Object, error)
}

type ProbeCertification struct {
	Certified bool
	MediaType string
	Profile   string
}

type CertifiedProbe interface {
	Probe(context.Context, localcas.Object) (ProbeCertification, error)
}

type IDGenerator func() string

type SourceVersion struct {
	Size            int64
	Mode            fs.FileMode
	Device          uint64
	Inode           uint64
	ModTimeUnixNano int64
	ChangeUnixNano  int64
}

func (v SourceVersion) regular() bool { return v.Mode.IsRegular() }

type SafeFile interface {
	io.Reader
	io.Closer
	Version() (SourceVersion, error)
}

// SafeOpener must resolve relativePath beneath root without following any
// symbolic-link component and must reject non-regular files.
type SafeOpener interface {
	Open(root, relativePath string) (SafeFile, error)
}

type SourceCopier interface {
	// Copy must abort before CAS commit when source exceeds maxBytes.
	Copy(context.Context, CAS, io.Reader, int64) (localcas.Object, error)
}

type StreamCopier struct{}

func (StreamCopier) Copy(ctx context.Context, store CAS, source io.Reader, maxBytes int64) (localcas.Object, error) {
	if err := ctx.Err(); err != nil {
		return localcas.Object{}, err
	}
	if maxBytes <= 0 {
		return localcas.Object{}, &ValidationError{Field: "maxBytes", Reason: "must be positive"}
	}
	reader := &boundedReader{
		reader: &contextReader{ctx: ctx, reader: source},
		limit:  maxBytes,
	}
	return store.Put(reader, "")
}

type boundedReader struct {
	reader io.Reader
	limit  int64
	read   int64
}

func (r *boundedReader) Read(buffer []byte) (int, error) {
	if len(buffer) == 0 {
		return 0, nil
	}
	remaining := r.limit - r.read
	if remaining > 0 {
		if int64(len(buffer)) > remaining {
			buffer = buffer[:remaining]
		}
		count, err := r.reader.Read(buffer)
		r.read += int64(count)
		return count, err
	}
	var probe [1]byte
	count, err := r.reader.Read(probe[:])
	if count > 0 {
		return 0, &LimitError{Kind: ErrUploadTooLarge, Limit: r.limit, Value: r.limit + 1}
	}
	return 0, err
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}

type Limits struct {
	MaxUploadBytes    int64
	MaxChunkBytes     int64
	MaxNameBytes      int
	MaxMediaTypeBytes int
}

type CreateUploadRequest struct {
	Name      string
	Length    int64
	MediaType string
}

type AppendChunkRequest struct {
	Offset int64
	Length int64
	Body   io.Reader
}

type LocalRegistrationPolicy struct {
	Enabled          bool
	AllowlistedRoots []string
	MaxBytes         int64
}
