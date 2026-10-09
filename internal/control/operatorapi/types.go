package operatorapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/control/auth"
	"github.com/jmilldotdev/cuelight/internal/control/media"
	"github.com/jmilldotdev/cuelight/internal/control/page"
	"github.com/jmilldotdev/cuelight/internal/control/pki"
	"github.com/jmilldotdev/cuelight/internal/control/playlist"
	"github.com/jmilldotdev/cuelight/internal/control/release"
	"github.com/jmilldotdev/cuelight/internal/control/remote"
	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
	canonical "github.com/jmilldotdev/cuelight/internal/release"
	"sync"
	"time"
)

const (
	ScopeNodeEnroll      = "node_enroll"
	ScopeSecurityAdmin   = "security_admin"
	ScopeNodeRead        = "node_read"
	ScopeNodeWrite       = "node_write"
	ScopeAssignmentRead  = "assignment_read"
	ScopeAssignmentWrite = "assignment_write"
	ScopeMediaRead       = "media_read"
	ScopeMediaWrite      = "media_write"
	ScopeLocalMediaRead  = "local_media_read"
	ScopeReleaseRead     = "release_read"
	ScopeReleaseWrite    = "release_write"
)

var (
	ErrUnauthorized   = errors.New("operator authentication required")
	ErrForbidden      = errors.New("operator scope is insufficient")
	ErrCrossSite      = errors.New("cross-site request refused for trusted-network access; use a token")
	ErrInvalidRequest = errors.New("invalid operator request")
	ErrBodyTooLarge   = errors.New("operator request body is too large")
	ErrChunkTooLarge  = errors.New("operator upload chunk is too large")
	ErrOffsetMismatch = errors.New("upload offset mismatch")
	ErrPrecondition   = errors.New("If-Match precondition failed")
	ErrCursorInvalid  = errors.New("invalid cursor")
	ErrWaitInvalid    = errors.New("invalid wait")
)

// Problem is the RFC 9457-compatible error representation emitted by handlers.
type Problem struct {
	Type          string `json:"type"`
	Title         string `json:"title"`
	Status        int    `json:"status"`
	Detail        string `json:"detail,omitempty"`
	Instance      string `json:"instance,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
}

// Node is the stable operator representation. Desired and observed generations
// are optional because some repositories only expose node identity metadata.
type Node struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	CredentialState    string `json:"credentialState"`
	NodeClass          string `json:"nodeClass"`
	DesiredGeneration  int64  `json:"desiredGeneration,omitempty"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
}

type Assignment struct {
	ID                 string `json:"id"`
	NodeID             string `json:"nodeId"`
	MediaID            string `json:"mediaId,omitempty"`
	PlaylistID         string `json:"playlistId,omitempty"`
	ReleaseID          string `json:"releaseId"`
	ReleaseDigest      string `json:"releaseDigest"`
	DeliveryMode       string `json:"deliveryMode"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
	DesiredGeneration  int64  `json:"desiredGeneration"`
	RemoteInput        bool   `json:"remoteInput"`
	ShowQR             bool   `json:"showQr"`
}

// AssignmentInput targets exactly one media item or playlist; the server
// publishes or reuses the release that presents it.
type AssignmentInput struct {
	MediaID            string `json:"mediaId,omitempty"`
	PlaylistID         string `json:"playlistId,omitempty"`
	DeliveryMode       string `json:"deliveryMode,omitempty"`
	ExpectedGeneration int64  `json:"expectedGeneration"`
	RemoteInput        bool   `json:"remoteInput"`
	ShowQR             bool   `json:"showQr"`
}

type Upload struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Length         int64  `json:"length"`
	Offset         int64  `json:"offset"`
	MediaType      string `json:"mediaType"`
	State          string `json:"state"`
	ArtifactDigest string `json:"artifactDigest,omitempty"`
}

type Artifact struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"mediaType"`
	State     string `json:"state,omitempty"`
}

type Asset struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Artifact Artifact `json:"artifact"`
}

type EnrollmentTokenCreated struct {
	ID        string    `json:"id"`
	Secret    string    `json:"secret"`
	ExpiresAt time.Time `json:"expiresAt"`
	// JoinURL is a cuelight://join link for desktop nodes (enrollment only,
	// present when CUELIGHT_CONTROL_PUBLIC_URL is configured).
	JoinURL string `json:"joinUrl,omitempty"`
	// CASHA256 pins the self-signed root anchoring control's TLS chain.
	CASHA256 string `json:"caSha256,omitempty"`
}

type OperatorMe struct {
	ID           string          `json:"id"`
	TokenID      string          `json:"tokenId,omitempty"`
	Scopes       []string        `json:"scopes"`
	Capabilities map[string]bool `json:"capabilities"`
	// AuthMethod is "token" for a bearer token or "network" for a token-less
	// request admitted by CUELIGHT_CONTROL_TRUSTED_NETWORKS.
	AuthMethod string `json:"authMethod"`
	// PeerAddress is the trusted TCP peer, present for network auth only.
	PeerAddress string `json:"peerAddress,omitempty"`
}

type NodeService interface {
	ListNodes(context.Context) ([]Node, error)
	GetNode(context.Context, string) (Node, error)
	UpdateNode(context.Context, string, string, string) (Node, error)
	GetNodeStatus(context.Context, string) (playerclient.NodeReport, error)
	ListNodeEvents(context.Context, string, string) ([]playerclient.NodeEvent, *string, error)
}
type NodeCapabilityService interface {
	GetNodeCapabilities(context.Context, string) (playerclient.CapabilityReport, error)
}
type NodeRevocationService interface {
	RevokeNode(context.Context, pki.Mutation, string) (pki.NodeRevocationRecord, error)
}

type AssignmentService interface {
	GetAssignment(context.Context, string) (assignment.Assignment, error)
}

// Assigner publishes or reuses the release for a target and assigns it.
type Assigner interface {
	AssignTarget(context.Context, assignment.Mutation, string, assignment.Target) (assignment.PutResult, error)
}

type AssetService interface {
	ListAssets(context.Context) ([]media.Asset, error)
	GetAsset(context.Context, string) (media.Asset, error)
}

type ReleaseService interface {
	Publish(context.Context, release.Mutation, release.PublishInput) (release.PublishResult, error)
	GetRelease(context.Context, string) (canonical.EnvelopeV1, error)
}

type UploadPageService interface {
	ListUploadsPage(context.Context, page.PageRequest) (page.Response[Upload], error)
}

type AssignmentPageService interface {
	ListAssignmentsPage(context.Context, page.PageRequest) (page.Response[Assignment], error)
}

type ReleasePageService interface {
	ListReleasesPage(context.Context, page.PageRequest) (page.Response[canonical.EnvelopeV1], error)
}

// PageRequestService is implemented by repositories that can perform keyset
// pagination. Legacy list services remain supported during migration.
type NodePageService interface {
	ListNodesPage(context.Context, page.PageRequest) (page.Response[Node], error)
}

type AssetPageService interface {
	ListAssetsPage(context.Context, page.PageRequest) (page.Response[Asset], error)
}

// Authorizer is the forward-compatible authorization seam. Implementations
// receive the authenticated actor and a stable action/resource pair.
type Authorizer interface {
	Authorize(auth.Actor, string, string, string) error
}

// ScopeAuthorizer adapts existing token scopes to the action/resource seam.
type ScopeAuthorizer struct {
	Scopes map[string]string
}

func (a ScopeAuthorizer) Authorize(actor auth.Actor, action, resourceType, resourceID string) error {
	if resourceType == "" {
		return ErrInvalidRequest
	}
	switch resourceType {
	case "node", "upload", "asset", "media", "release", "token", "enrollment-token", "operator", "assignment", "assignments", "playlist", "profile":
	default:
		return fmt.Errorf("%w: unknown resource %q", ErrForbidden, resourceType)
	}
	scope, ok := a.Scopes[action]
	if !ok {
		return fmt.Errorf("%w: unknown action %q", ErrForbidden, action)
	}
	if scope == "" {
		return nil
	}
	for _, got := range actor.Scopes {
		if got == scope {
			return nil
		}
	}
	return fmt.Errorf("%w: action %q on %s", ErrForbidden, action, resourceType)
}

// AuditRecord and AuditQuery are persistence-neutral audit contracts. The
// controller may attach a repository without coupling handlers to SQL.
type AuditRecord struct {
	ID           string    `json:"id"`
	ActorID      string    `json:"actorId"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resourceType"`
	ResourceID   string    `json:"resourceId,omitempty"`
	Outcome      string    `json:"outcome"`
	CreatedAt    time.Time `json:"createdAt"`
}

type AuditQuery struct {
	page.PageRequest
	ActorID      string
	Action       string
	ResourceType string
	ResourceID   string
}

type AuditRepository interface {
	Append(context.Context, AuditRecord) error
	Query(context.Context, AuditQuery) (page.Response[AuditRecord], error)
}

type AuditService struct{ Repository AuditRepository }

func (s AuditService) Record(ctx context.Context, record AuditRecord) error {
	if s.Repository == nil {
		return nil
	}
	return s.Repository.Append(ctx, record)
}

func (s AuditService) List(ctx context.Context, query AuditQuery) (page.Response[AuditRecord], error) {
	if s.Repository == nil {
		return page.NewResponse[AuditRecord](nil, nil, nil, false), nil
	}
	return s.Repository.Query(ctx, query)
}

// RemoteLinkService mints phone links for a node's active remote-input session.
type RemoteLinkService interface {
	Link(context.Context, string) (remote.SessionCreated, error)
}

// RemoteLink is the operator-facing phone link for an active remote session.
type RemoteLink struct {
	SessionID string    `json:"sessionId"`
	RemoteURL string    `json:"remoteUrl"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Dependencies is the explicit composition boundary for the operator API.
// Services are deliberately narrow, allowing HTTP to be tested without SQL.
type Dependencies struct {
	Authorizer Authorizer
	Audit      AuditService
	Auth       interface {
		Authenticate(context.Context, string, ...string) (auth.Actor, error)
		List(context.Context) ([]auth.TokenMetadata, error)
		Revoke(context.Context, string) (auth.TokenMetadata, error)
	}
	PKI interface {
		CreateEnrollmentToken(context.Context, pki.Mutation) (pki.TokenSecret, error)
		CreateRecoveryToken(context.Context, pki.Mutation, string) (pki.TokenSecret, error)
	}
	Nodes       NodeService
	Assignments AssignmentService
	Assigner    Assigner
	RemoteLinks RemoteLinkService
	Media       interface {
		CreateUpload(context.Context, media.Mutation, media.CreateUploadRequest) (media.Upload, error)
		Upload(context.Context, string) (media.Upload, error)
		AppendChunk(context.Context, media.Mutation, string, media.AppendChunkRequest) (media.Upload, error)
		CompleteUpload(context.Context, media.Mutation, string) (media.Asset, error)
	}
	Registration interface {
		Register(context.Context, media.Mutation, string) (media.Asset, error)
	}
	Assets          AssetService
	Releases        ReleaseService
	Uploads         UploadPageService
	AssignmentPages AssignmentPageService
	ReleasePages    ReleasePageService
	Playlists       playlist.Repository
	MaxJSONBytes    int64
	MaxChunkBytes   int64
	Idempotency     *IdempotencyCache
	Profiles        ProfileService
	JoinLinks       JoinLinks
	// NetworkTrust admits token-less operator requests from trusted peers.
	NetworkTrust NetworkTrust
}

// IdempotencyCache is only used by adapters that need HTTP-level replay. Domain
// services should continue to own durable idempotency where available.
type IdempotencyCache struct {
	mu     sync.Mutex
	values map[string][]byte
}

func NewIdempotencyCache() *IdempotencyCache {
	return &IdempotencyCache{values: make(map[string][]byte)}
}
func (c *IdempotencyCache) get(key string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.values[key]
	return append([]byte(nil), b...), ok
}
func (c *IdempotencyCache) put(key string, b []byte) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.values[key] = append([]byte(nil), b...)
}
