package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/assignment"
	"github.com/jmilldotdev/cuelight/internal/control/pki"
	"github.com/jmilldotdev/cuelight/internal/control/playerapi"
	"github.com/jmilldotdev/cuelight/internal/media/localcas"
	"github.com/jmilldotdev/cuelight/internal/player/client"
	canonical "github.com/jmilldotdev/cuelight/internal/release"
)

var (
	_ playerapi.Repository    = (*Store)(nil)
	_ playerapi.ReleaseLoader = (*Store)(nil)
	_ playerapi.CASReader     = (*LocalCASReader)(nil)
)

// PutCapabilities stores the latest capability report for a node. Reports are
// node-scoped so one node can never overwrite another node's capabilities.
func (s *Store) PutCapabilities(ctx context.Context, nodeID string, report client.CapabilityReport) error {
	payload, err := json.Marshal(report)
	if err != nil {
		return fmt.Errorf("encode capability report: %w", err)
	}
	_, err = s.q(ctx).ExecContext(ctx, `
		INSERT INTO node_capability_reports (node_id, report, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (node_id) DO UPDATE
		SET report = EXCLUDED.report, updated_at = EXCLUDED.updated_at`, nodeID, payload)
	return wrap("upsert node capabilities", err)
}

// CurrentAssignment returns only the latest assignment for nodeID. The node
// existence check distinguishes an unknown node from a node without history.
func (s *Store) CurrentAssignment(ctx context.Context, nodeID string) (assignment.Assignment, bool, error) {
	if err := s.requirePlayerNode(ctx, nodeID); err != nil {
		return assignment.Assignment{}, false, err
	}
	return s.playerAssignment(ctx, s.q(ctx), `
		SELECT id::text, node_id::text, COALESCE(media_id::text, ''), COALESCE(playlist_id::text, ''), release_id::text, release_digest, delivery_mode,
		       desired_generation - 1, desired_generation, remote_input, show_qr, created_by::text
		FROM assignments
		WHERE node_id = $1
		ORDER BY desired_generation DESC LIMIT 1`, nodeID)
}

// WaitForAssignment uses bounded polling rather than holding a PostgreSQL
// connection open for the duration of a long poll. Every query observes the
// caller context, making cancellation safe even while a request is waiting.
func (s *Store) WaitForAssignment(ctx context.Context, nodeID string, generation, softwareGeneration int64, wait time.Duration) error {
	if err := s.requirePlayerNode(ctx, nodeID); err != nil {
		return err
	}
	if generation < 0 || wait <= 0 {
		return nil
	}
	interval := 100 * time.Millisecond
	if wait < interval {
		interval = wait
	}
	if interval <= 0 {
		return nil
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case <-ticker.C:
			var current, currentSoftware int64
			err := s.q(ctx).QueryRowContext(ctx,
				`SELECT desired_generation, software_generation FROM nodes WHERE id = $1`, nodeID).Scan(&current, &currentSoftware)
			if errors.Is(err, sql.ErrNoRows) {
				return pki.ErrNodeNotFound
			}
			if err != nil {
				return wrap("poll assignment generation", err)
			}
			if current > generation || currentSoftware > softwareGeneration {
				return nil
			}
		}
	}
}

// AssignedRelease checks the assignment history for this exact node and
// release ID. It deliberately does not authorize releases assigned to a
// different node or merely present in the global release table.
func (s *Store) AssignedRelease(ctx context.Context, nodeID, releaseID string) (assignment.Assignment, bool, error) {
	if err := s.requirePlayerNode(ctx, nodeID); err != nil {
		return assignment.Assignment{}, false, err
	}
	return s.playerAssignment(ctx, s.q(ctx), `
		SELECT id::text, node_id::text, COALESCE(media_id::text, ''), COALESCE(playlist_id::text, ''), release_id::text, release_digest, delivery_mode,
		       desired_generation - 1, desired_generation, remote_input, show_qr, created_by::text
		FROM assignments
		WHERE node_id = $1 AND release_id = $2
		ORDER BY desired_generation DESC LIMIT 1`, nodeID, releaseID)
}

func (s *Store) requirePlayerNode(ctx context.Context, nodeID string) error {
	var found string
	err := s.q(ctx).QueryRowContext(ctx, `SELECT id::text FROM nodes WHERE id = $1`, nodeID).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return pki.ErrNodeNotFound
	}
	return wrap("read player node", err)
}

func (s *Store) playerAssignment(ctx context.Context, q queryer, query string, arguments ...any) (assignment.Assignment, bool, error) {
	var value assignment.Assignment
	err := q.QueryRowContext(ctx, query, arguments...).Scan(
		&value.ID, &value.NodeID, &value.MediaID, &value.PlaylistID, &value.ReleaseID, &value.ReleaseDigest,
		&value.DeliveryMode, &value.ExpectedGeneration, &value.DesiredGeneration, &value.RemoteInput, &value.ShowQR, &value.CreatedBy)
	if errors.Is(err, sql.ErrNoRows) {
		return assignment.Assignment{}, false, nil
	}
	if err != nil {
		return assignment.Assignment{}, false, wrap("read player assignment", err)
	}
	return value, true, nil
}

// LoadRelease is intentionally a plain immutable release read. Authorization
// is performed by FetchRelease through AssignedRelease before this method is
// called, so this loader never broadens that authorization decision.
func (s *Store) LoadRelease(ctx context.Context, releaseID string) (canonical.EnvelopeV1, error) {
	envelope, err := s.releaseEnvelope(ctx, s.q(ctx), releaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return canonical.EnvelopeV1{}, playerapi.ErrNotFound
	}
	return envelope, err
}

// AuthorizeArtifact permits artifacts only when they belong to one of the
// five bounded release roles for this node and the durable artifact is ready.
func (s *Store) AuthorizeArtifact(ctx context.Context, nodeID, digest string) (bool, error) {
	var authorized bool
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM node_release_authorizations nra
			JOIN release_artifacts ra
			  ON ra.release_id = nra.release_id
			JOIN artifacts a ON a.digest = ra.artifact_digest
			WHERE nra.node_id = $1
			  AND ra.artifact_digest = $2
			  AND nra.role IN ('desired', 'candidate', 'rendering', 'healthy', 'lkg')
			  AND a.state = 'ready'
		)`, nodeID, digest).Scan(&authorized)
	if err != nil {
		return false, wrap("authorize player artifact", err)
	}
	return authorized, nil
}

// RecordReport converts the wire report to the assignment observation model.
// Stale reports are successful no-ops; only a newer sequence in the current
// epoch updates the snapshot and observed generation.
func (s *Store) RecordReport(ctx context.Context, nodeID string, report client.NodeReport) (bool, error) {
	observation := assignment.Observation{
		EventEpoch: report.EventEpoch, Sequence: report.Sequence,
		DesiredGenerationSeen:      report.DesiredGenerationSeen,
		ObservedGeneration:         report.ObservedGeneration,
		CandidateReleaseDigest:     reportDigest(report.CandidateReleaseDigest),
		RenderingReleaseDigest:     reportDigest(report.RenderingReleaseDigest),
		HealthyReleaseDigest:       reportDigest(report.HealthyReleaseDigest),
		LastKnownGoodReleaseDigest: reportDigest(report.LastKnownGoodReleaseDigest),
	}
	payload, err := json.Marshal(report)
	if err != nil {
		return false, err
	}
	accepted, err := s.recordObservation(ctx, nodeID, observation, payload)
	if errors.Is(err, ErrStaleObservation) {
		return false, nil
	}
	if errors.Is(err, ErrEventEpochMismatch) {
		return false, playerapi.ErrEpochMismatch
	}
	if err != nil || !accepted {
		return accepted, err
	}
	// A fresh report changes the bounded candidate/rendering/healthy/LKG
	// authorization set. Recompute reads the just-accepted snapshot and
	// atomically replaces those role slots.
	if err := (assignment.Service{Assignments: s}).RecomputeAuthorizations(ctx, nodeID); err != nil {
		return false, err
	}
	return true, nil
}

func reportDigest(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// AppendEvents atomically validates and stores an event batch, then reports the
// highest contiguous sequence. Existing identical events are idempotent;
// sequence or ID reuse with different event content is a conflict.
func (s *Store) AppendEvents(ctx context.Context, nodeID string, batch client.EventBatch) (client.EventAcknowledgement, error) {
	var acknowledgement client.EventAcknowledgement
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var epoch string
		err := tx.QueryRowContext(txCtx, `SELECT event_epoch::text FROM nodes WHERE id = $1 FOR UPDATE`, nodeID).Scan(&epoch)
		if errors.Is(err, sql.ErrNoRows) {
			return pki.ErrNodeNotFound
		}
		if err != nil {
			return wrap("lock player event node", err)
		}
		if batch.Epoch != epoch {
			return playerapi.ErrEpochMismatch
		}
		bySequence := make(map[int64]client.NodeEvent, len(batch.Events))
		byID := make(map[string]client.NodeEvent, len(batch.Events))
		for _, event := range batch.Events {
			if event.Epoch != batch.Epoch {
				return playerapi.ErrEpochMismatch
			}
			if event.Sequence <= 0 || event.ID == "" || event.Type == "" {
				return playerapi.ErrInvalidRequest
			}
			if previous, ok := bySequence[event.Sequence]; ok && !eventsEqualEvent(previous, event) {
				return playerapi.ErrEventConflict
			}
			if previous, ok := byID[event.ID]; ok && !eventsEqualEvent(previous, event) {
				return playerapi.ErrEventConflict
			}
			bySequence[event.Sequence] = event
			byID[event.ID] = event
			if event.Sequence == math.MaxInt64 {
				return playerapi.ErrEpochMismatch
			}
		}
		for _, event := range batch.Events {
			if err := s.checkExistingEvent(txCtx, tx, nodeID, batch.Epoch, event); err != nil {
				return err
			}
		}
		for _, event := range batch.Events {
			var existing []byte
			err := tx.QueryRowContext(txCtx, `
				SELECT event FROM node_events
				WHERE node_id = $1 AND event_epoch = $2 AND sequence = $3`, nodeID, batch.Epoch, event.Sequence).Scan(&existing)
			if errors.Is(err, sql.ErrNoRows) {
				payload, marshalErr := json.Marshal(event)
				if marshalErr != nil {
					return fmt.Errorf("encode node event: %w", marshalErr)
				}
				if _, err := tx.ExecContext(txCtx, `
					INSERT INTO node_events (node_id, event_epoch, sequence, event_id, event, created_at)
					VALUES ($1, $2, $3, $4, $5, now())`, nodeID, batch.Epoch, event.Sequence, event.ID, payload); err != nil {
					return wrap("append node event", err)
				}
				continue
			}
			if err != nil {
				return wrap("check duplicate node event", err)
			}
		}
		watermark, err := contiguousWatermark(txCtx, tx, nodeID, batch.Epoch)
		if err != nil {
			return err
		}
		for _, event := range batch.Events {
			if event.Sequence > watermark {
				return playerapi.ErrEventGap
			}
		}
		acknowledgement = client.EventAcknowledgement{Epoch: batch.Epoch, AcknowledgedThrough: watermark}
		return nil
	})
	return acknowledgement, err
}

func (s *Store) checkExistingEvent(ctx context.Context, tx *sql.Tx, nodeID, epoch string, event client.NodeEvent) error {
	var payload []byte
	err := tx.QueryRowContext(ctx, `
		SELECT event FROM node_events
		WHERE node_id = $1 AND event_epoch = $2 AND sequence = $3`, nodeID, epoch, event.Sequence).Scan(&payload)
	if err == nil {
		if !eventsEqual(payload, event) {
			return playerapi.ErrEventConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return wrap("check event sequence", err)
	}
	payload = nil
	err = tx.QueryRowContext(ctx, `
		SELECT event FROM node_events
		WHERE node_id = $1 AND event_epoch = $2 AND event_id = $3`, nodeID, epoch, event.ID).Scan(&payload)
	if err == nil {
		if !eventsEqual(payload, event) {
			return playerapi.ErrEventConflict
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return wrap("check event ID", err)
	}
	return nil
}

func eventsEqual(payload []byte, event client.NodeEvent) bool {
	var stored client.NodeEvent
	if err := json.Unmarshal(payload, &stored); err != nil {
		return false
	}
	return stored.ID == event.ID && stored.Epoch == event.Epoch && stored.Sequence == event.Sequence &&
		stored.Type == event.Type && stored.OccurredAt.Equal(event.OccurredAt) &&
		stored.CoversThrough == event.CoversThrough && equalEventData(stored.Data, event.Data)
}

func eventsEqualEvent(a, b client.NodeEvent) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func equalEventData(a, b map[string]any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

type storedEvent struct {
	Sequence      int64
	CoversThrough int64
}

func contiguousWatermark(ctx context.Context, tx *sql.Tx, nodeID, epoch string) (int64, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT sequence, event->>'coversThrough'
		FROM node_events
		WHERE node_id = $1 AND event_epoch = $2
		ORDER BY sequence`, nodeID, epoch)
	if err != nil {
		return 0, wrap("read event watermark", err)
	}
	defer rows.Close()
	events := make([]storedEvent, 0)
	for rows.Next() {
		var event storedEvent
		var covers sql.NullString
		if err := rows.Scan(&event.Sequence, &covers); err != nil {
			return 0, wrap("scan event watermark", err)
		}
		if covers.Valid && covers.String != "" {
			_, _ = fmt.Sscan(covers.String, &event.CoversThrough)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return 0, wrap("iterate event watermark", err)
	}
	// The query is ordered, but sorting here also makes this helper robust to
	// alternate query plans and keeps the contiguous calculation explicit.
	sort.Slice(events, func(i, j int) bool { return events[i].Sequence < events[j].Sequence })
	var watermark int64
	for _, event := range events {
		if event.Sequence <= watermark {
			continue
		}
		if event.Sequence == watermark+1 || (event.CoversThrough >= event.Sequence-1 && event.CoversThrough >= watermark+1) {
			watermark = event.Sequence
			continue
		}
		break
	}
	return watermark, nil
}

// LocalCASReader exposes LocalCAS' read-only operations using the player API
// context-aware reader contract. It does not add writes or alter LocalCAS.
type LocalCASReader struct {
	CAS *localcas.Store
}

func NewLocalCASReader(cas *localcas.Store) *LocalCASReader { return &LocalCASReader{CAS: cas} }

func (r *LocalCASReader) Stat(ctx context.Context, digest string) (playerapi.ArtifactStat, error) {
	if err := ctx.Err(); err != nil {
		return playerapi.ArtifactStat{}, err
	}
	if r == nil || r.CAS == nil {
		return playerapi.ArtifactStat{}, errors.New("local CAS reader is not configured")
	}
	object, err := r.CAS.Stat(digest)
	if err != nil {
		return playerapi.ArtifactStat{}, err
	}
	return playerapi.ArtifactStat{Digest: object.Digest, Size: object.Size}, nil
}

func (r *LocalCASReader) Open(ctx context.Context, digest string) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil || r.CAS == nil {
		return nil, errors.New("local CAS reader is not configured")
	}
	return r.CAS.Open(digest)
}
