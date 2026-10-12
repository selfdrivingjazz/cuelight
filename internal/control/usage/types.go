// Package usage exposes bounded, authorization-scoped relationships between
// control-plane resources. It intentionally returns references rather than
// embedding or recursively loading related resources.
package usage

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/selfdrivingjazz/cuelight/internal/control/page"
)

var (
	ErrInvalidArgument = errors.New("invalid usage argument")
	ErrNotFound        = errors.New("usage resource not found")
	ErrForbidden       = errors.New("usage access is forbidden")
	ErrUnsupportedKind = errors.New("unsupported usage resource kind")
)

// Kind is the canonical singular resource name used in usage URLs and
// references.
type Kind string

const (
	KindMedia      Kind = "media"
	KindPlaylist   Kind = "playlist"
	KindRelease    Kind = "release"
	KindAssignment Kind = "assignment"
)

func (k Kind) Valid() bool {
	switch k {
	case KindMedia, KindPlaylist, KindRelease, KindAssignment:
		return true
	default:
		return false
	}
}

func ParseKind(value string) (Kind, error) {
	kind := Kind(strings.ToLower(strings.TrimSpace(value)))
	if !kind.Valid() {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedKind, value)
	}
	return kind, nil
}

// Reference is a deliberately shallow link to a resource related to the
// queried resource. Href is an API path, not a CAS or filesystem path.
type Reference struct {
	Kind Kind   `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
	Href string `json:"href"`
}

// Query identifies one authorized usage lookup. Limit is bounded by
// page.MaxLimit; zero selects page.DefaultLimit.
type Query struct {
	ActorID    string
	Kind       Kind
	ResourceID string
	Cursor     string
	Limit      int
}

// Page is the response envelope used by both the service and HTTP adapter.
type Page struct {
	ResourceID string      `json:"resourceId"`
	References []Reference `json:"references"`
	NextCursor string      `json:"nextCursor,omitempty"`
	HasMore    bool        `json:"hasMore"`
}

// Descriptive aliases keep transport adapters independent of the short page
// and reference names while preserving one wire contract.
type UsagePage = Page
type UsageReference = Reference
type UsageQuery = Query

// Authorizer is called before the repository is touched. Implementations may
// inspect the actor in context as well as the explicit actor ID.
type Authorizer interface {
	Authorize(context.Context, string, string, string, string) error
}

// Repository performs one SQL-backed, bounded relation query.
type Repository interface {
	Usage(context.Context, Query) (Page, error)
}

// Normalize validates non-HTTP callers using the shared pagination bounds.
func (q Query) Normalize() (Query, error) {
	q.ResourceID = strings.TrimSpace(q.ResourceID)
	if q.ResourceID == "" {
		return Query{}, fmt.Errorf("%w: resource ID is required", ErrInvalidArgument)
	}
	kind, err := ParseKind(string(q.Kind))
	if err != nil {
		return Query{}, err
	}
	q.Kind = kind
	if q.Limit == 0 {
		q.Limit = page.DefaultLimit
	}
	if q.Limit < 1 || q.Limit > page.MaxLimit {
		return Query{}, page.ErrLimitInvalid
	}
	q.Cursor = strings.TrimSpace(q.Cursor)
	if len(q.Cursor) > 2048 || strings.ContainsAny(q.Cursor, "\r\n") {
		return Query{}, page.ErrCursorInvalid
	}
	return q, nil
}
