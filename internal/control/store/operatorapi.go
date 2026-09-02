package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/media"
	"github.com/jmill/cable/internal/control/operatorapi"
	"github.com/jmill/cable/internal/control/page"
	"github.com/jmill/cable/internal/control/pki"
	playerclient "github.com/jmill/cable/internal/player/client"
	canonical "github.com/jmill/cable/internal/release"
)

type OperatorAdapter struct{ Store *Store }

func NewOperatorAdapter(repository *Store) *OperatorAdapter {
	return &OperatorAdapter{Store: repository}
}

var _ operatorapi.NodeService = (*OperatorAdapter)(nil)
var _ operatorapi.NodePageService = (*OperatorAdapter)(nil)
var _ operatorapi.AssignmentService = (*Store)(nil)
var _ operatorapi.AssetService = (*Store)(nil)
var _ operatorapi.AssetPageService = (*Store)(nil)

func (a *OperatorAdapter) ListNodes(ctx context.Context) ([]operatorapi.Node, error) {
	s := a.Store
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT id::text, name, credential_state, desired_generation, observed_generation FROM nodes WHERE credential_state <> 'revoked' ORDER BY created_at, id`)
	if err != nil {
		return nil, wrap("list nodes", err)
	}
	defer rows.Close()
	result := make([]operatorapi.Node, 0)
	for rows.Next() {
		var node operatorapi.Node
		if err := rows.Scan(&node.ID, &node.Name, &node.CredentialState, &node.DesiredGeneration, &node.ObservedGeneration); err != nil {
			return nil, wrap("scan node", err)
		}
		result = append(result, node)
	}
	return result, wrap("iterate nodes", rows.Err())
}

// ListNodesPage performs keyset pagination over the immutable (created_at,id)
// order. Search and credentialState filtering are evaluated by PostgreSQL.
func (a *OperatorAdapter) ListNodesPage(ctx context.Context, request page.PageRequest) (page.Response[operatorapi.Node], error) {
	request, err := request.Normalize()
	if err != nil {
		return page.Response[operatorapi.Node]{}, err
	}
	sortKey := request.Sort
	if sortKey == "" {
		sortKey = "createdAt"
	}
	if sortKey != "createdAt" && sortKey != "name" {
		return page.Response[operatorapi.Node]{}, page.ErrSortInvalid
	}
	codec := page.DefaultCodec()
	var pos page.Position
	var previous *string
	if request.Cursor != "" {
		pos, err = codec.Decode(request.Cursor)
		if err != nil || pos.Resource != "nodes" || pos.Sort != sortKey || pos.Order != request.Order {
			return page.Response[operatorapi.Node]{}, page.ErrCursorInvalid
		}
		previousValue := pos.Previous
		previous = &previousValue
	}
	clauses := []string{"1=1"}
	args := make([]any, 0, 5)
	if request.Search != "" {
		args = append(args, "%"+strings.ToLower(request.Search)+"%")
		clauses = append(clauses, "(LOWER(n.id::text) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(n.name) LIKE $"+strconv.Itoa(len(args))+")")
	}
	if state := strings.TrimSpace(request.Filters["credentialState"]); state != "" {
		args = append(args, state)
		clauses = append(clauses, "n.credential_state = $"+strconv.Itoa(len(args)))
	} else {
		clauses = append(clauses, "n.credential_state <> 'revoked'")
	}
	if request.Cursor != "" {
		if sortKey == "name" {
			args = append(args, pos.Value, pos.ID)
			op := ">"
			if request.Order == "desc" {
				op = "<"
			}
			clauses = append(clauses, "(n.name, n.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		} else {
			at, parseErr := time.Parse(time.RFC3339Nano, pos.Value)
			if parseErr != nil {
				return page.Response[operatorapi.Node]{}, page.ErrCursorInvalid
			}
			args = append(args, at, pos.ID)
			op := ">"
			if request.Order == "desc" {
				op = "<"
			}
			clauses = append(clauses, "(n.created_at, n.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		}
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	orderBy := "n.created_at " + order + ", n.id " + order
	if sortKey == "name" {
		orderBy = "n.name " + order + ", n.id " + order
	}
	args = append(args, request.Limit+1)
	query := "SELECT n.id::text, n.name, n.credential_state, n.desired_generation, n.observed_generation, n.created_at FROM nodes n WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + orderBy + " LIMIT $" + strconv.Itoa(len(args))
	rows, err := a.Store.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return page.Response[operatorapi.Node]{}, wrap("list nodes page", err)
	}
	defer rows.Close()
	items := make([]operatorapi.Node, 0, request.Limit)
	hasMore := false
	var lastValue, lastID string
	for rows.Next() {
		var item operatorapi.Node
		var created time.Time
		if err := rows.Scan(&item.ID, &item.Name, &item.CredentialState, &item.DesiredGeneration, &item.ObservedGeneration, &created); err != nil {
			return page.Response[operatorapi.Node]{}, wrap("scan node page", err)
		}
		if len(items) < request.Limit {
			items = append(items, item)
			lastID = item.ID
			if sortKey == "name" {
				lastValue = item.Name
			} else {
				lastValue = created.UTC().Format(time.RFC3339Nano)
			}
		} else {
			hasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return page.Response[operatorapi.Node]{}, wrap("iterate nodes page", err)
	}
	hasMore = hasMore && lastID != ""
	var next *string
	if hasMore {
		value, encodeErr := codec.Encode(page.Position{Resource: "nodes", Sort: sortKey, Order: request.Order, Value: lastValue, ID: lastID, Previous: request.Cursor})
		if encodeErr != nil {
			return page.Response[operatorapi.Node]{}, encodeErr
		}
		next = &value
	}
	return page.NewResponse(items, next, previous, hasMore), nil
}
func (a *OperatorAdapter) GetNode(ctx context.Context, id string) (operatorapi.Node, error) {
	s := a.Store
	var node operatorapi.Node
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id::text, name, credential_state, desired_generation, observed_generation FROM nodes WHERE id = $1`, id).Scan(&node.ID, &node.Name, &node.CredentialState, &node.DesiredGeneration, &node.ObservedGeneration)
	if errors.Is(err, sql.ErrNoRows) {
		return operatorapi.Node{}, pkiNotFound()
	}
	return node, wrap("read node", err)
}

func (a *OperatorAdapter) UpdateNode(ctx context.Context, id, name, match string) (operatorapi.Node, error) {
	s := a.Store
	node, err := a.GetNode(ctx, id)
	if err != nil {
		return operatorapi.Node{}, err
	}
	want := fmt.Sprintf("\"%s:%d:%d\"", node.ID, node.DesiredGeneration, node.ObservedGeneration)
	if match != want {
		return operatorapi.Node{}, operatorapi.ErrPrecondition
	}
	_, err = s.q(ctx).ExecContext(ctx, `UPDATE nodes SET name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return operatorapi.Node{}, wrap("update node", err)
	}
	node.Name = name
	return node, nil
}
func (a *OperatorAdapter) GetNodeStatus(ctx context.Context, id string) (playerclient.NodeReport, error) {
	s := a.Store
	var payload []byte
	var reportedAt time.Time
	err := s.q(ctx).QueryRowContext(ctx, `SELECT report, updated_at FROM node_report_snapshots WHERE node_id = $1`, id).Scan(&payload, &reportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return playerclient.NodeReport{}, pkiNotFound()
	}
	if err != nil {
		return playerclient.NodeReport{}, wrap("read node status", err)
	}
	var report playerclient.NodeReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return playerclient.NodeReport{}, wrap("decode node status", err)
	}
	report.ReportedAt = &reportedAt
	return report, nil
}
func (a *OperatorAdapter) GetNodeCapabilities(ctx context.Context, id string) (playerclient.CapabilityReport, error) {
	var payload []byte
	err := a.Store.q(ctx).QueryRowContext(ctx, `SELECT report FROM node_capability_reports WHERE node_id = $1`, id).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return playerclient.CapabilityReport{}, pkiNotFound()
	}
	if err != nil {
		return playerclient.CapabilityReport{}, wrap("read node capabilities", err)
	}
	var report playerclient.CapabilityReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return playerclient.CapabilityReport{}, wrap("decode node capabilities", err)
	}
	return report, nil
}
func (a *OperatorAdapter) ListNodeEvents(ctx context.Context, id, cursor string) ([]playerclient.NodeEvent, *string, error) {
	s := a.Store
	sequence := int64(0)
	if cursor != "" {
		parsed, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || parsed < 0 {
			return nil, nil, operatorapi.ErrCursorInvalid
		}
		sequence = parsed
	}
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT event FROM node_events WHERE node_id = $1 AND sequence > $2 ORDER BY sequence LIMIT 101`, id, sequence)
	if err != nil {
		return nil, nil, wrap("list node events", err)
	}
	defer rows.Close()
	items := make([]playerclient.NodeEvent, 0, 100)
	for rows.Next() {
		var payload []byte
		if err := rows.Scan(&payload); err != nil {
			return nil, nil, wrap("scan node event", err)
		}
		var event playerclient.NodeEvent
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, nil, wrap("decode node event", err)
		}
		items = append(items, event)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, wrap("iterate node events", err)
	}
	var next *string
	if len(items) > 100 {
		value := strconv.FormatInt(items[99].Sequence, 10)
		next = &value
		items = items[:100]
	}
	return items, next, nil
}

func (s *Store) GetAssignment(ctx context.Context, id string) (assignment.Assignment, error) {
	value, found, err := s.CurrentAssignment(ctx, id)
	if err != nil {
		return assignment.Assignment{}, err
	}
	if !found {
		return assignment.Assignment{}, pkiNotFound()
	}
	return value, nil
}

// ListAssetsPage performs keyset pagination over asset creation time and ID.
func (s *Store) ListAssetsPage(ctx context.Context, request page.PageRequest) (page.Response[operatorapi.Asset], error) {
	request, err := request.Normalize()
	if err != nil {
		return page.Response[operatorapi.Asset]{}, err
	}
	sortKey := request.Sort
	if sortKey == "" {
		sortKey = "createdAt"
	}
	if sortKey != "createdAt" && sortKey != "name" {
		return page.Response[operatorapi.Asset]{}, page.ErrSortInvalid
	}
	codec := page.DefaultCodec()
	var pos page.Position
	var previous *string
	if request.Cursor != "" {
		pos, err = codec.Decode(request.Cursor)
		if err != nil || pos.Resource != "assets" || pos.Sort != sortKey || pos.Order != request.Order {
			return page.Response[operatorapi.Asset]{}, page.ErrCursorInvalid
		}
		previousValue := pos.Previous
		previous = &previousValue
	}
	clauses := []string{"aa.role = 'original'"}
	args := make([]any, 0, 5)
	if request.Search != "" {
		args = append(args, "%"+strings.ToLower(request.Search)+"%")
		clauses = append(clauses, "(LOWER(a.id::text) LIKE $"+strconv.Itoa(len(args))+" OR LOWER(a.name) LIKE $"+strconv.Itoa(len(args))+")")
	}
	if state := strings.TrimSpace(request.Filters["state"]); state != "" {
		args = append(args, state)
		clauses = append(clauses, "r.state = $"+strconv.Itoa(len(args)))
	}
	if request.Cursor != "" {
		args = append(args, pos.Value, pos.ID)
		op := ">"
		if request.Order == "desc" {
			op = "<"
		}
		if sortKey == "name" {
			clauses = append(clauses, "(a.name, a.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		} else {
			parsed, parseErr := time.Parse(time.RFC3339Nano, pos.Value)
			if parseErr != nil {
				return page.Response[operatorapi.Asset]{}, page.ErrCursorInvalid
			}
			args[len(args)-2] = parsed
			clauses = append(clauses, "(a.created_at, a.id::text) "+op+" ($"+strconv.Itoa(len(args)-1)+", $"+strconv.Itoa(len(args))+")")
		}
	}
	order := "ASC"
	if request.Order == "desc" {
		order = "DESC"
	}
	orderBy := "a.created_at " + order + ", a.id " + order
	if sortKey == "name" {
		orderBy = "a.name " + order + ", a.id " + order
	}
	args = append(args, request.Limit+1)
	query := "SELECT a.id::text, a.name, r.digest, r.size_bytes, r.media_type, r.state, a.created_at FROM assets a JOIN asset_artifacts aa ON aa.asset_id = a.id JOIN artifacts r ON r.digest = aa.artifact_digest WHERE " + strings.Join(clauses, " AND ") + " ORDER BY " + orderBy + " LIMIT $" + strconv.Itoa(len(args))
	rows, err := s.q(ctx).QueryContext(ctx, query, args...)
	if err != nil {
		return page.Response[operatorapi.Asset]{}, wrap("list assets page", err)
	}
	defer rows.Close()
	items := make([]operatorapi.Asset, 0, request.Limit)
	hasMore := false
	var lastValue, lastID string
	for rows.Next() {
		var item operatorapi.Asset
		var created time.Time
		if err := rows.Scan(&item.ID, &item.Name, &item.Artifact.Digest, &item.Artifact.Size, &item.Artifact.MediaType, &item.Artifact.State, &created); err != nil {
			return page.Response[operatorapi.Asset]{}, wrap("scan asset page", err)
		}
		if len(items) < request.Limit {
			items = append(items, item)
			lastID = item.ID
			if sortKey == "name" {
				lastValue = item.Name
			} else {
				lastValue = created.UTC().Format(time.RFC3339Nano)
			}
		} else {
			hasMore = true
		}
	}
	if err := rows.Err(); err != nil {
		return page.Response[operatorapi.Asset]{}, wrap("iterate assets page", err)
	}
	hasMore = hasMore && lastID != ""
	var next *string
	if hasMore {
		value, encodeErr := codec.Encode(page.Position{Resource: "assets", Sort: sortKey, Order: request.Order, Value: lastValue, ID: lastID, Previous: request.Cursor})
		if encodeErr != nil {
			return page.Response[operatorapi.Asset]{}, encodeErr
		}
		next = &value
	}
	return page.NewResponse(items, next, previous, hasMore), nil
}

func (s *Store) PutAssignment(ctx context.Context, nodeID string, mutation assignment.Mutation, input assignment.PutInput) (assignment.PutResult, error) {
	return (assignment.Service{Assignments: s}).Put(ctx, nodeID, mutation, input)
}

func (s *Store) ListAssets(ctx context.Context) ([]media.Asset, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT a.id::text, a.name, r.digest, r.size_bytes, r.media_type, r.state FROM assets a JOIN asset_artifacts aa ON aa.asset_id = a.id JOIN artifacts r ON r.digest = aa.artifact_digest WHERE aa.role = 'original' ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, wrap("list assets", err)
	}
	defer rows.Close()
	result := make([]media.Asset, 0)
	for rows.Next() {
		var value media.Asset
		if err := rows.Scan(&value.ID, &value.Name, &value.Artifact.Digest, &value.Artifact.Size, &value.Artifact.MediaType, &value.Artifact.State); err != nil {
			return nil, wrap("scan asset", err)
		}
		result = append(result, value)
	}
	return result, wrap("iterate assets", rows.Err())
}

func (s *Store) GetAsset(ctx context.Context, id string) (media.Asset, error) {
	value, err := s.asset(ctx, s.q(ctx), id)
	if errors.Is(err, sql.ErrNoRows) {
		return media.Asset{}, media.ErrNotFound
	}
	return value, err
}

func (s *Store) GetRelease(ctx context.Context, id string) (canonical.EnvelopeV1, error) {
	return s.LoadRelease(ctx, id)
}
func pkiNotFound() error { return pki.ErrNodeNotFound }
