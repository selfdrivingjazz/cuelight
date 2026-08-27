package operatorapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/auth"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/pki"
	"github.com/jmill/cable/internal/control/release"
	playerclient "github.com/jmill/cable/internal/player/client"
)

// OperationScopes is exported for clients that want to mount individual
// operations while retaining the same authorization contract as NewHandler.
var OperationScopes = map[string]string{
	"createEnrollmentToken": ScopeNodeEnroll, "listAdminTokens": ScopeSecurityAdmin,
	"revokeAdminToken": ScopeSecurityAdmin, "listNodes": ScopeNodeRead,
	"getNode": ScopeNodeRead, "updateNode": ScopeNodeWrite,
	"getNodeStatus": ScopeNodeRead, "createNodeRecoveryToken": ScopeSecurityAdmin,
	"getNodeAssignment": ScopeAssignmentRead, "putNodeAssignment": ScopeAssignmentWrite,
	"listNodeEvents": ScopeNodeRead, "createUpload": ScopeMediaWrite,
	"headUpload": ScopeMediaWrite, "appendUpload": ScopeMediaWrite,
	"completeUpload": ScopeMediaWrite, "registerLocalAsset": ScopeLocalMediaRead,
	"listAssets": ScopeMediaRead, "getAsset": ScopeMediaRead,
	"publishRelease": ScopeReleaseWrite, "getRelease": ScopeReleaseRead,
}

type Handler struct{ deps Dependencies }

// NewHandler returns a composable handler mounted below /api/v1. It registers
// only operator routes; callers can compose health and player handlers beside it.
func NewHandler(deps Dependencies) http.Handler {
	h := &Handler{deps: deps}
	mux := http.NewServeMux()
	mux.Handle("POST /enrollment-tokens", h.authorized(ScopeNodeEnroll, h.createEnrollmentToken))
	mux.Handle("GET /admin-tokens", h.authorized(ScopeSecurityAdmin, h.listAdminTokens))
	mux.Handle("POST /admin-tokens/{tokenId}/revoke", h.authorized(ScopeSecurityAdmin, h.revokeAdminToken))
	mux.Handle("GET /nodes", h.authorized(ScopeNodeRead, h.listNodes))
	mux.Handle("GET /nodes/{nodeId}", h.authorized(ScopeNodeRead, h.getNode))
	mux.Handle("PATCH /nodes/{nodeId}", h.authorized(ScopeNodeWrite, h.updateNode))
	mux.Handle("GET /nodes/{nodeId}/status", h.authorized(ScopeNodeRead, h.getNodeStatus))
	mux.Handle("POST /nodes/{nodeId}/recovery-tokens", h.authorized(ScopeSecurityAdmin, h.createNodeRecoveryToken))
	mux.Handle("GET /nodes/{nodeId}/assignment", h.authorized(ScopeAssignmentRead, h.getNodeAssignment))
	mux.Handle("PUT /nodes/{nodeId}/assignment", h.authorized(ScopeAssignmentWrite, h.putNodeAssignment))
	mux.Handle("GET /nodes/{nodeId}/events", h.authorized(ScopeNodeRead, h.listNodeEvents))
	mux.Handle("POST /uploads", h.authorized(ScopeMediaWrite, h.createUpload))
	mux.Handle("HEAD /uploads/{uploadId}", h.authorized(ScopeMediaWrite, h.headUpload))
	mux.Handle("PATCH /uploads/{uploadId}", h.authorized(ScopeMediaWrite, h.appendUpload))
	mux.Handle("POST /uploads/{uploadId}/complete", h.authorized(ScopeMediaWrite, h.completeUpload))
	mux.Handle("POST /assets/register-local", h.authorized(ScopeLocalMediaRead, h.registerLocalAsset))
	mux.Handle("GET /assets", h.authorized(ScopeMediaRead, h.listAssets))
	mux.Handle("GET /assets/{assetId}", h.authorized(ScopeMediaRead, h.getAsset))
	mux.Handle("POST /releases/publish", h.authorized(ScopeReleaseWrite, h.publishRelease))
	mux.Handle("GET /releases/{releaseId}", h.authorized(ScopeReleaseRead, h.getRelease))
	return mux
}

// Middleware authenticates a bearer token and stores auth.Actor in request
// context. It is useful when a caller wants one authentication boundary around
// several composed handlers; NewHandler applies the equivalent per operation.
func (d Dependencies) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := d.authenticate(r, "")
		if err != nil {
			writeError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(auth.WithActor(r.Context(), actor)))
	})
}

func (h *Handler) authorized(scope string, fn http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		actor, err := h.deps.authenticate(r, scope)
		if err != nil {
			writeError(w, err)
			return
		}
		fn(w, r.WithContext(auth.WithActor(r.Context(), actor)))
	})
}

func (d Dependencies) authenticate(r *http.Request, scope string) (auth.Actor, error) {
	if d.Auth == nil {
		return auth.Actor{}, fmt.Errorf("operator auth dependency is not configured")
	}
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return auth.Actor{}, ErrUnauthorized
	}
	var actor auth.Actor
	var err error
	if scope == "" {
		actor, err = d.Auth.Authenticate(r.Context(), parts[1])
	} else {
		actor, err = d.Auth.Authenticate(r.Context(), parts[1], scope)
	}
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredential) || errors.Is(err, auth.ErrTokenRevoked) {
			return auth.Actor{}, fmt.Errorf("%w: %v", ErrUnauthorized, err)
		}
		if errors.Is(err, auth.ErrInsufficientScope) {
			return auth.Actor{}, fmt.Errorf("%w: %v", ErrForbidden, err)
		}
		return auth.Actor{}, err
	}
	return actor, nil
}

func (h *Handler) createEnrollmentToken(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.PKI == nil {
		writeError(w, errors.New("PKI service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.PKI.CreateEnrollmentToken(r.Context(), pki.Mutation{ActorID: actor.ID, IdempotencyKey: key})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, EnrollmentTokenCreated{ID: v.ID, Secret: v.Secret, ExpiresAt: v.ExpiresAt})
}

func (h *Handler) listAdminTokens(w http.ResponseWriter, r *http.Request) {
	if h.deps.Auth == nil {
		writeError(w, errors.New("auth service is not configured"))
		return
	}
	items, err := h.deps.Auth.List(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	// TokenMetadata contains no secret by construction. Keep the response shape
	// explicit to prevent a future persistence field from crossing this boundary.
	type item struct {
		ID      string   `json:"id"`
		Prefix  string   `json:"prefix"`
		Scopes  []string `json:"scopes"`
		Revoked bool     `json:"revoked"`
	}
	out := make([]item, len(items))
	for i, v := range items {
		out[i] = item{v.ID, v.Prefix, append([]string(nil), v.Scopes...), v.Revoked}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (h *Handler) revokeAdminToken(w http.ResponseWriter, r *http.Request) {
	if _, err := idempotencyKey(r); err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Auth == nil {
		writeError(w, errors.New("auth service is not configured"))
		return
	}
	_, err := h.deps.Auth.Revoke(r.Context(), r.PathValue("tokenId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listNodes(w http.ResponseWriter, r *http.Request) {
	if h.deps.Nodes == nil {
		writeError(w, errors.New("node service is not configured"))
		return
	}
	items, err := h.deps.Nodes.ListNodes(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []Node{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}
func (h *Handler) getNode(w http.ResponseWriter, r *http.Request) {
	if h.deps.Nodes == nil {
		writeError(w, errors.New("node service is not configured"))
		return
	}
	v, err := h.deps.Nodes.GetNode(r.Context(), r.PathValue("nodeId"))
	if err != nil {
		writeError(w, err)
		return
	}
	setNodeETag(w, v)
	writeJSON(w, http.StatusOK, v)
}
func (h *Handler) updateNode(w http.ResponseWriter, r *http.Request) {
	match := strings.TrimSpace(r.Header.Get("If-Match"))
	if match == "" {
		writeError(w, ErrPrecondition)
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" || len(in.Name) > 120 {
		writeError(w, fmt.Errorf("%w: name must be 1-120 characters", ErrInvalidRequest))
		return
	}
	if h.deps.Nodes == nil {
		writeError(w, errors.New("node service is not configured"))
		return
	}
	v, err := h.deps.Nodes.UpdateNode(r.Context(), r.PathValue("nodeId"), in.Name, match)
	if err != nil {
		writeError(w, err)
		return
	}
	setNodeETag(w, v)
	writeJSON(w, http.StatusOK, v)
}
func (h *Handler) getNodeStatus(w http.ResponseWriter, r *http.Request) {
	if h.deps.Nodes == nil {
		writeError(w, errors.New("node service is not configured"))
		return
	}
	v, err := h.deps.Nodes.GetNodeStatus(r.Context(), r.PathValue("nodeId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}
func (h *Handler) createNodeRecoveryToken(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.PKI == nil {
		writeError(w, errors.New("PKI service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.PKI.CreateRecoveryToken(r.Context(), pki.Mutation{ActorID: actor.ID, IdempotencyKey: key}, r.PathValue("nodeId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, EnrollmentTokenCreated{ID: v.ID, Secret: v.Secret, ExpiresAt: v.ExpiresAt})
}

func (h *Handler) getNodeAssignment(w http.ResponseWriter, r *http.Request) {
	if _, err := parseWait(r.URL.Query().Get("wait")); err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Assignments == nil {
		writeError(w, errors.New("assignment service is not configured"))
		return
	}
	v, err := h.deps.Assignments.GetAssignment(r.Context(), r.PathValue("nodeId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("ETag", assignmentETag(v.DesiredGeneration))
	writeJSON(w, http.StatusOK, assignmentJSON(v))
}
func (h *Handler) putNodeAssignment(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(r.Header.Get("If-Match")) == "" {
		writeError(w, ErrPrecondition)
		return
	}
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in assignment.PutInput
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	if h.deps.Assignments == nil {
		writeError(w, errors.New("assignment service is not configured"))
		return
	}
	result, err := h.deps.Assignments.PutAssignment(r.Context(), r.PathValue("nodeId"), assignment.Mutation{ActorID: actor.ID, IdempotencyKey: key}, in)
	if err != nil {
		writeError(w, err)
		return
	}
	status := http.StatusCreated
	if !result.Created {
		status = http.StatusOK
	}
	writeJSON(w, status, assignmentJSON(result.Assignment))
}
func (h *Handler) listNodeEvents(w http.ResponseWriter, r *http.Request) {
	cursor, err := parseCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Nodes == nil {
		writeError(w, errors.New("node service is not configured"))
		return
	}
	items, next, err := h.deps.Nodes.ListNodeEvents(r.Context(), r.PathValue("nodeId"), cursor)
	if err != nil {
		writeError(w, err)
		return
	}
	if items == nil {
		items = []playerclient.NodeEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": next})
}

func (h *Handler) createUpload(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in media.CreateUploadRequest
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Media == nil {
		writeError(w, errors.New("media service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.Media.CreateUpload(r.Context(), media.Mutation{ActorID: actor.ID, IdempotencyKey: key}, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, uploadJSON(v))
}
func (h *Handler) headUpload(w http.ResponseWriter, r *http.Request) {
	if h.deps.Media == nil {
		writeError(w, errors.New("media service is not configured"))
		return
	}
	v, err := h.deps.Media.Upload(r.Context(), r.PathValue("uploadId"))
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(v.ReceivedLength, 10))
	w.Header().Set("Upload-Length", strconv.FormatInt(v.DeclaredLength, 10))
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) appendUpload(w http.ResponseWriter, r *http.Request) {
	offset, err := parseNonNegativeHeader(r, "Upload-Offset")
	if err != nil {
		writeError(w, err)
		return
	}
	if r.ContentLength < 0 {
		writeError(w, fmt.Errorf("%w: Content-Length is required", ErrInvalidRequest))
		return
	}
	if r.ContentLength > h.chunkLimit() {
		writeError(w, ErrChunkTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.chunkLimit())
	if h.deps.Media == nil {
		writeError(w, errors.New("media service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.Media.AppendChunk(r.Context(), media.Mutation{ActorID: actor.ID}, r.PathValue("uploadId"), media.AppendChunkRequest{Offset: offset, Length: r.ContentLength, Body: r.Body})
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Upload-Offset", strconv.FormatInt(v.ReceivedLength, 10))
	w.WriteHeader(http.StatusNoContent)
}
func (h *Handler) completeUpload(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Media == nil {
		writeError(w, errors.New("media service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.Media.CompleteUpload(r.Context(), media.Mutation{ActorID: actor.ID, IdempotencyKey: key}, r.PathValue("uploadId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, assetJSON(v))
}
func (h *Handler) registerLocalAsset(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	if strings.TrimSpace(in.Path) == "" {
		writeError(w, ErrInvalidRequest)
		return
	}
	if h.deps.Registration == nil {
		writeError(w, errors.New("registration service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.Registration.Register(r.Context(), media.Mutation{ActorID: actor.ID, IdempotencyKey: key}, in.Path)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, assetJSON(v))
}
func (h *Handler) listAssets(w http.ResponseWriter, r *http.Request) {
	if h.deps.Assets == nil {
		writeError(w, errors.New("asset service is not configured"))
		return
	}
	v, err := h.deps.Assets.ListAssets(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	out := make([]Asset, len(v))
	for i := range v {
		out[i] = assetJSON(v[i])
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}
func (h *Handler) getAsset(w http.ResponseWriter, r *http.Request) {
	if h.deps.Assets == nil {
		writeError(w, errors.New("asset service is not configured"))
		return
	}
	v, err := h.deps.Assets.GetAsset(r.Context(), r.PathValue("assetId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, assetJSON(v))
}
func (h *Handler) publishRelease(w http.ResponseWriter, r *http.Request) {
	key, err := idempotencyKey(r)
	if err != nil {
		writeError(w, err)
		return
	}
	var in release.PublishInput
	if err := decodeJSON(w, r, h.jsonLimit(), &in); err != nil {
		writeError(w, err)
		return
	}
	if h.deps.Releases == nil {
		writeError(w, errors.New("release service is not configured"))
		return
	}
	actor, _ := auth.ActorFromContext(r.Context())
	v, err := h.deps.Releases.Publish(r.Context(), release.Mutation{ActorID: actor.ID, IdempotencyKey: key}, in)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, v.Envelope)
}
func (h *Handler) getRelease(w http.ResponseWriter, r *http.Request) {
	if h.deps.Releases == nil {
		writeError(w, errors.New("release service is not configured"))
		return
	}
	v, err := h.deps.Releases.GetRelease(r.Context(), r.PathValue("releaseId"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handler) jsonLimit() int64 {
	if h.deps.MaxJSONBytes > 0 {
		return h.deps.MaxJSONBytes
	}
	return 4 << 20
}
func (h *Handler) chunkLimit() int64 {
	if h.deps.MaxChunkBytes > 0 {
		return h.deps.MaxChunkBytes
	}
	return 8 << 20
}
func idempotencyKey(r *http.Request) (string, error) {
	v := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(v) < 16 || len(v) > 200 {
		return "", fmt.Errorf("%w: Idempotency-Key must be 16-200 characters", ErrInvalidRequest)
	}
	return v, nil
}
func parseNonNegativeHeader(r *http.Request, name string) (int64, error) {
	v := strings.TrimSpace(r.Header.Get(name))
	if v == "" {
		return 0, fmt.Errorf("%w: %s is required", ErrInvalidRequest, name)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%w: %s must be non-negative", ErrInvalidRequest, name)
	}
	return n, nil
}
func parseCursor(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if len(v) > 512 || strings.ContainsAny(v, "\r\n") {
		return "", ErrCursorInvalid
	}
	return v, nil
}
func parseWait(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || d > 30*time.Second {
		return 0, ErrWaitInvalid
	}
	return d, nil
}
func decodeJSON(w http.ResponseWriter, r *http.Request, limit int64, out any) error {
	if limit <= 0 {
		limit = 4 << 20
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return ErrBodyTooLarge
		}
		return fmt.Errorf("%w: malformed JSON", ErrInvalidRequest)
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: exactly one JSON value is required", ErrInvalidRequest)
	}
	return nil
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, err error) {
	status := statusFor(err)
	title := http.StatusText(status)
	if title == "" {
		title = "Request failed"
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Problem{Type: "about:blank", Title: title, Status: status, Detail: err.Error()})
}
func statusFor(err error) int {
	switch {
	case errors.Is(err, ErrUnauthorized), errors.Is(err, auth.ErrInvalidCredential), errors.Is(err, auth.ErrTokenRevoked):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden), errors.Is(err, auth.ErrInsufficientScope):
		return http.StatusForbidden
	case errors.Is(err, ErrBodyTooLarge), errors.Is(err, ErrChunkTooLarge), errors.Is(err, media.ErrChunkTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrPrecondition), errors.Is(err, assignment.ErrGenerationConflict):
		return http.StatusPreconditionFailed
	case errors.Is(err, media.ErrOffsetMismatch), errors.Is(err, ErrOffsetMismatch):
		return http.StatusConflict
	case errors.Is(err, auth.ErrNotFound), errors.Is(err, pki.ErrNodeNotFound), errors.Is(err, pki.ErrTokenNotFound), errors.Is(err, media.ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrInvalidRequest), errors.Is(err, ErrCursorInvalid), errors.Is(err, ErrWaitInvalid), errors.Is(err, media.ErrInvalidArgument), errors.Is(err, assignment.ErrInvalidAssignment), errors.Is(err, release.ErrInvalidMutation):
		return http.StatusBadRequest
	case errors.Is(err, assignment.ErrIdempotencyConflict), errors.Is(err, release.ErrIdempotencyConflict), errors.Is(err, pki.ErrIdempotencyConflict):
		return http.StatusConflict
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return 499
	default:
		return http.StatusInternalServerError
	}
}
func setNodeETag(w http.ResponseWriter, n Node) {
	w.Header().Set("ETag", fmt.Sprintf("\"%s:%d:%d\"", n.ID, n.DesiredGeneration, n.ObservedGeneration))
}
func assignmentETag(generation int64) string { return fmt.Sprintf("\"generation:%d\"", generation) }
func assignmentJSON(v assignment.Assignment) Assignment {
	return Assignment{ID: v.ID, NodeID: v.NodeID, ReleaseID: v.ReleaseID, ReleaseDigest: v.ReleaseDigest, DeliveryMode: v.DeliveryMode, ExpectedGeneration: v.ExpectedGeneration, DesiredGeneration: v.DesiredGeneration}
}
func uploadJSON(v media.Upload) Upload {
	return Upload{ID: v.ID, Length: v.DeclaredLength, Offset: v.ReceivedLength, State: string(v.State)}
}
func assetJSON(v media.Asset) Asset {
	return Asset{ID: v.ID, Name: v.Name, Artifact: Artifact{Digest: v.Artifact.Digest, Size: v.Artifact.Size, MediaType: v.Artifact.MediaType}}
}
