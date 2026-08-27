package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/operatorapi"
	"github.com/jmill/cable/internal/control/page"
	canonical "github.com/jmill/cable/internal/release"
)

var _ operatorapi.UploadPageService = (*Store)(nil)
var _ operatorapi.AssignmentPageService = (*Store)(nil)
var _ operatorapi.ReleasePageService = (*Store)(nil)

func (s *Store) ListUploadsPage(ctx context.Context, request page.PageRequest) (page.Response[operatorapi.Upload], error) {
	request, err := request.Normalize()
	if err != nil {
		return page.Response[operatorapi.Upload]{}, err
	}
	sortKey := request.Sort
	if sortKey == "" {
		sortKey = "createdAt"
	}
	if sortKey != "createdAt" && sortKey != "name" {
		return page.Response[operatorapi.Upload]{}, page.ErrSortInvalid
	}
	codec := page.DefaultCodec()
	var position page.Position
	var previous *string
	if request.Cursor != "" {
		position, err = codec.Decode(request.Cursor)
		if err != nil || position.Resource != "uploads" || position.Sort != sortKey || position.Order != request.Order {
			return page.Response[operatorapi.Upload]{}, page.ErrCursorInvalid
		}
		previousValue := position.Previous
		previous = &previousValue
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 6)
	if request.Search != "" {
		args = append(args, "%"+strings.ToLower(request.Search)+"%")
		placeholder := "$" + strconv.Itoa(len(args))
		clauses = append(clauses, "(LOWER(u.id::text) LIKE "+placeholder+" OR LOWER(u.name) LIKE "+placeholder+")")
	}
	if state := strings.TrimSpace(request.Filters["state"]); state != "" {
		args = append(args, state)
		clauses = append(clauses, "u.state = $"+strconv.Itoa(len(args)))
	}
	if request.Cursor != "" {
		operator := ">"
		if request.Order == "desc" {
			operator = "<"
		}
		args = append(args, position.Value, position.ID)
		if sortKey == "name" {
			clauses = append(clauses, "(u.name, u.id::text) "+operator+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		} else {
			createdAt, parseErr := time.Parse(time.RFC3339Nano, position.Value)
			if parseErr != nil {
				return page.Response[operatorapi.Upload]{}, page.ErrCursorInvalid
			}
			args[len(args)-2] = createdAt
			clauses = append(clauses, "(u.created_at, u.id::text) "+operator+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		}
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	orderBy := "u.created_at " + order + ", u.id " + order
	if sortKey == "name" {
		orderBy = "u.name " + order + ", u.id " + order
	}
	args = append(args, request.Limit+1)
	query := "SELECT u.id::text, u.name, u.declared_length, u.received_length, u.media_type, u.state, u.artifact_digest, u.created_at FROM uploads u WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + orderBy + " LIMIT $" + strconv.Itoa(len(args))
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return page.Response[operatorapi.Upload]{}, wrap("list uploads page", err)
	}
	defer rows.Close()
	items := make([]operatorapi.Upload, 0, request.Limit)
	hasMore := false
	var lastValue, lastID string
	for rows.Next() {
		var item operatorapi.Upload
		var artifactDigest sql.NullString
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.Name, &item.Length, &item.Offset, &item.MediaType, &item.State, &artifactDigest, &createdAt); err != nil {
			return page.Response[operatorapi.Upload]{}, wrap("scan upload page", err)
		}
		item.ArtifactDigest = artifactDigest.String
		if len(items) < request.Limit {
			items = append(items, item)
			lastID = item.ID
			if sortKey == "name" {
				lastValue = item.Name
			} else {
				lastValue = createdAt.UTC().Format(time.RFC3339Nano)
			}
		} else {
			hasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return page.Response[operatorapi.Upload]{}, wrap("iterate uploads page", err)
	}
	var next *string
	if hasMore && lastID != "" {
		value, encodeErr := codec.Encode(page.Position{Resource: "uploads", Sort: sortKey, Order: request.Order, Value: lastValue, ID: lastID, Previous: request.Cursor})
		if encodeErr != nil {
			return page.Response[operatorapi.Upload]{}, encodeErr
		}
		next = &value
	}
	return page.NewResponse(items, next, previous, next != nil), nil
}

func (s *Store) ListAssignmentsPage(ctx context.Context, request page.PageRequest) (page.Response[operatorapi.Assignment], error) {
	request, err := request.Normalize()
	if err != nil {
		return page.Response[operatorapi.Assignment]{}, err
	}
	sortKey := request.Sort
	if sortKey == "" {
		sortKey = "createdAt"
	}
	if sortKey != "createdAt" {
		return page.Response[operatorapi.Assignment]{}, page.ErrSortInvalid
	}
	codec := page.DefaultCodec()
	var position page.Position
	var previous *string
	if request.Cursor != "" {
		position, err = codec.Decode(request.Cursor)
		if err != nil || position.Resource != "assignments" || position.Sort != sortKey || position.Order != request.Order {
			return page.Response[operatorapi.Assignment]{}, page.ErrCursorInvalid
		}
		previousValue := position.Previous
		previous = &previousValue
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 6)
	if request.Search != "" {
		args = append(args, "%"+strings.ToLower(request.Search)+"%")
		placeholder := "$" + strconv.Itoa(len(args))
		clauses = append(clauses, "(LOWER(a.id::text) LIKE "+placeholder+" OR LOWER(a.node_id::text) LIKE "+placeholder+" OR LOWER(a.release_id::text) LIKE "+placeholder+" OR LOWER(a.release_digest) LIKE "+placeholder+")")
	}
	if mode := strings.TrimSpace(request.Filters["deliveryMode"]); mode != "" {
		args = append(args, mode)
		clauses = append(clauses, "a.delivery_mode = $"+strconv.Itoa(len(args)))
	}
	if request.Cursor != "" {
		createdAt, parseErr := time.Parse(time.RFC3339Nano, position.Value)
		if parseErr != nil {
			return page.Response[operatorapi.Assignment]{}, page.ErrCursorInvalid
		}
		operator := ">"
		if request.Order == "desc" {
			operator = "<"
		}
		args = append(args, createdAt, position.ID)
		clauses = append(clauses, "(a.created_at, a.id::text) "+operator+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	args = append(args, request.Limit+1)
	query := "SELECT a.id::text, a.node_id::text, a.release_id::text, a.release_digest, a.delivery_mode, a.desired_generation - 1, a.desired_generation, a.created_at FROM assignments a WHERE " + strings.Join(clauses, " AND ") + " ORDER BY a.created_at " + order + ", a.id " + order + " LIMIT $" + strconv.Itoa(len(args))
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return page.Response[operatorapi.Assignment]{}, wrap("list assignments page", err)
	}
	defer rows.Close()
	items := make([]operatorapi.Assignment, 0, request.Limit)
	hasMore := false
	var lastValue, lastID string
	for rows.Next() {
		var item operatorapi.Assignment
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.NodeID, &item.ReleaseID, &item.ReleaseDigest, &item.DeliveryMode, &item.ExpectedGeneration, &item.DesiredGeneration, &createdAt); err != nil {
			return page.Response[operatorapi.Assignment]{}, wrap("scan assignment page", err)
		}
		if len(items) < request.Limit {
			items = append(items, item)
			lastID = item.ID
			lastValue = createdAt.UTC().Format(time.RFC3339Nano)
		} else {
			hasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return page.Response[operatorapi.Assignment]{}, wrap("iterate assignments page", err)
	}
	var next *string
	if hasMore && lastID != "" {
		value, encodeErr := codec.Encode(page.Position{Resource: "assignments", Sort: sortKey, Order: request.Order, Value: lastValue, ID: lastID, Previous: request.Cursor})
		if encodeErr != nil {
			return page.Response[operatorapi.Assignment]{}, encodeErr
		}
		next = &value
	}
	return page.NewResponse(items, next, previous, next != nil), nil
}

func (s *Store) ListReleasesPage(ctx context.Context, request page.PageRequest) (page.Response[canonical.EnvelopeV1], error) {
	request, err := request.Normalize()
	if err != nil {
		return page.Response[canonical.EnvelopeV1]{}, err
	}
	sortKey := request.Sort
	if sortKey == "" {
		sortKey = "createdAt"
	}
	if sortKey != "createdAt" && sortKey != "digest" {
		return page.Response[canonical.EnvelopeV1]{}, page.ErrSortInvalid
	}
	codec := page.DefaultCodec()
	var position page.Position
	var previous *string
	if request.Cursor != "" {
		position, err = codec.Decode(request.Cursor)
		if err != nil || position.Resource != "releases" || position.Sort != sortKey || position.Order != request.Order {
			return page.Response[canonical.EnvelopeV1]{}, page.ErrCursorInvalid
		}
		previousValue := position.Previous
		previous = &previousValue
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 5)
	if request.Search != "" {
		args = append(args, "%"+strings.ToLower(request.Search)+"%")
		placeholder := "$" + strconv.Itoa(len(args))
		clauses = append(clauses, "(LOWER(r.id::text) LIKE "+placeholder+" OR LOWER(r.digest) LIKE "+placeholder+" OR LOWER(r.key_id) LIKE "+placeholder+")")
	}
	if request.Cursor != "" {
		operator := ">"
		if request.Order == "desc" {
			operator = "<"
		}
		args = append(args, position.Value, position.ID)
		if sortKey == "digest" {
			clauses = append(clauses, "(r.digest, r.id::text) "+operator+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		} else {
			createdAt, parseErr := time.Parse(time.RFC3339Nano, position.Value)
			if parseErr != nil {
				return page.Response[canonical.EnvelopeV1]{}, page.ErrCursorInvalid
			}
			args[len(args)-2] = createdAt
			clauses = append(clauses, "(r.created_at, r.id::text) "+operator+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		}
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	orderBy := "r.created_at " + order + ", r.id " + order
	if sortKey == "digest" {
		orderBy = "r.digest " + order + ", r.id " + order
	}
	args = append(args, request.Limit+1)
	query := "SELECT r.id::text, r.digest, r.key_id, r.signature, r.payload, r.created_at FROM releases r WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + orderBy + " LIMIT $" + strconv.Itoa(len(args))
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return page.Response[canonical.EnvelopeV1]{}, wrap("list releases page", err)
	}
	defer rows.Close()
	items := make([]canonical.EnvelopeV1, 0, request.Limit)
	hasMore := false
	var lastValue, lastID string
	for rows.Next() {
		var item canonical.EnvelopeV1
		var signature, payload []byte
		var createdAt time.Time
		if err := rows.Scan(&item.ID, &item.Digest, &item.KeyID, &signature, &payload, &createdAt); err != nil {
			return page.Response[canonical.EnvelopeV1]{}, wrap("scan release page", err)
		}
		if err := json.Unmarshal(payload, &item.Payload); err != nil {
			return page.Response[canonical.EnvelopeV1]{}, fmt.Errorf("decode stored release %s: %w", item.ID, err)
		}
		item.Signature = base64.StdEncoding.EncodeToString(signature)
		if len(items) < request.Limit {
			items = append(items, item)
			lastID = item.ID
			if sortKey == "digest" {
				lastValue = item.Digest
			} else {
				lastValue = createdAt.UTC().Format(time.RFC3339Nano)
			}
		} else {
			hasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return page.Response[canonical.EnvelopeV1]{}, wrap("iterate releases page", err)
	}
	var next *string
	if hasMore && lastID != "" {
		value, encodeErr := codec.Encode(page.Position{Resource: "releases", Sort: sortKey, Order: request.Order, Value: lastValue, ID: lastID, Previous: request.Cursor})
		if encodeErr != nil {
			return page.Response[canonical.EnvelopeV1]{}, encodeErr
		}
		next = &value
	}
	return page.NewResponse(items, next, previous, next != nil), nil
}
