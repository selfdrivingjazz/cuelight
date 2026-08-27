// Package page defines the shared, transport-neutral pagination contract used by
// controller list endpoints.
package page

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

const (
	DefaultLimit = 25
	MaxLimit     = 100
)

var (
	ErrLimitInvalid  = errors.New("invalid page limit")
	ErrCursorInvalid = errors.New("invalid page cursor")
	ErrSortInvalid   = errors.New("invalid page sort")
	ErrOrderInvalid  = errors.New("invalid page order")
)

// PageRequest is the common query contract for list operations. Filters are
// resource-specific keys, while the other fields have the same meaning for all
// resources. Cursor is opaque to callers and must not be parsed by handlers.
type PageRequest struct {
	Cursor  string
	Limit   int
	Sort    string
	Order   string
	Search  string
	Filters map[string]string
}

// NewRequest returns a request with the documented defaults applied.
func NewRequest() PageRequest { return PageRequest{Limit: DefaultLimit, Order: "asc"} }

// Parse reads the common query parameters. Unknown parameters are retained as
// filters, allowing a resource to validate its own filter vocabulary.
func Parse(values url.Values) (PageRequest, error) {
	r := NewRequest()
	r.Cursor = strings.TrimSpace(values.Get("cursor"))
	r.Sort = strings.TrimSpace(values.Get("sort"))
	r.Order = strings.ToLower(strings.TrimSpace(values.Get("order")))
	if r.Order == "" {
		r.Order = "asc"
	}
	r.Search = strings.TrimSpace(values.Get("search"))
	if raw := strings.TrimSpace(values.Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > MaxLimit {
			return PageRequest{}, ErrLimitInvalid
		}
		r.Limit = limit
	}
	if r.Cursor != "" && (len(r.Cursor) > 2048 || strings.ContainsAny(r.Cursor, "\r\n")) {
		return PageRequest{}, ErrCursorInvalid
	}
	if r.Order != "asc" && r.Order != "desc" {
		return PageRequest{}, ErrOrderInvalid
	}
	r.Filters = make(map[string]string)
	for key, values := range values {
		if key == "cursor" || key == "limit" || key == "sort" || key == "order" || key == "search" {
			continue
		}
		if len(values) == 0 {
			continue
		}
		r.Filters[key] = strings.TrimSpace(values[len(values)-1])
	}
	return r, nil
}

// Normalize applies defaults to requests assembled by non-HTTP callers.
func (r PageRequest) Normalize() (PageRequest, error) {
	if r.Limit == 0 {
		r.Limit = DefaultLimit
	}
	if r.Limit < 1 || r.Limit > MaxLimit {
		return PageRequest{}, ErrLimitInvalid
	}
	r.Order = strings.ToLower(strings.TrimSpace(r.Order))
	if r.Order == "" {
		r.Order = "asc"
	}
	if r.Order != "asc" && r.Order != "desc" {
		return PageRequest{}, ErrOrderInvalid
	}
	r.Cursor = strings.TrimSpace(r.Cursor)
	if len(r.Cursor) > 2048 || strings.ContainsAny(r.Cursor, "\r\n") {
		return PageRequest{}, ErrCursorInvalid
	}
	if r.Filters == nil {
		r.Filters = map[string]string{}
	}
	return r, nil
}

// Info is the stable envelope metadata emitted by every paginated list.
type Info struct {
	NextCursor     *string `json:"nextCursor"`
	PreviousCursor *string `json:"previousCursor"`
	HasMore        bool    `json:"hasMore"`
}

// Response is the stable paginated response envelope.
type Response[T any] struct {
	Items []T  `json:"items"`
	Page  Info `json:"page"`
}

func NewResponse[T any](items []T, next, previous *string, hasMore bool) Response[T] {
	if items == nil {
		items = []T{}
	}
	return Response[T]{Items: items, Page: Info{NextCursor: next, PreviousCursor: previous, HasMore: hasMore}}
}

// PageResponse is the descriptive alias used by API contracts.
type PageResponse[T any] = Response[T]

// EventResponse preserves sequence cursor semantics while using the same page
// envelope. Sequence cursors identify the last consumed event, so callers pass
// nextCursor back verbatim and no previous cursor is synthesized.
type EventResponse[T any] struct {
	Items []T  `json:"items"`
	Page  Info `json:"page"`
}

func NewEventResponse[T any](items []T, next *string, hasMore bool) EventResponse[T] {
	if items == nil {
		items = []T{}
	}
	return EventResponse[T]{Items: items, Page: Info{NextCursor: next, HasMore: hasMore}}
}
