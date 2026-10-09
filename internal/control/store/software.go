package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/software"
	playerclient "github.com/jmilldotdev/cuelight/internal/player/client"
)

var _ software.Repository = (*Store)(nil)

// PutSoftwareRelease records a verified release once; re-importing identical
// envelope bytes is a no-op.
func (s *Store) PutSoftwareRelease(ctx context.Context, release software.Release) (bool, error) {
	created := false
	err := s.inTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var existing []byte
		err := tx.QueryRowContext(ctx, `SELECT envelope FROM software_releases WHERE version = $1 FOR UPDATE`, release.Version).Scan(&existing)
		if err == nil {
			if !bytes.Equal(existing, release.Envelope) {
				return software.ErrConflict
			}
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return wrap("read software release", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO software_releases (version, channel, published_at, minimum_protocol_version, key_id, envelope, source)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			release.Version, release.Channel, release.PublishedAt, release.MinimumProtocolVersion, release.KeyID, release.Envelope, release.Source); err != nil {
			if postgresCode(err) == "23505" {
				return software.ErrConflict
			}
			return wrap("insert software release", err)
		}
		for _, artifact := range release.Artifacts {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO software_release_artifacts (version, name, component, os, arch, format, sha256, size)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
				release.Version, artifact.Name, artifact.Component, artifact.OS, artifact.Arch, artifact.Format, artifact.SHA256, artifact.Size); err != nil {
				return wrap("insert software release artifact", err)
			}
		}
		created = true
		return nil
	})
	return created, err
}

func (s *Store) MarkSoftwareArtifactReady(ctx context.Context, version, name string) error {
	result, err := s.q(ctx).ExecContext(ctx, `UPDATE software_release_artifacts SET cas_state = 'ready' WHERE version = $1 AND name = $2`, version, name)
	if err != nil {
		return wrap("mark software artifact ready", err)
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return software.ErrNotFound
	}
	return nil
}

func (s *Store) ListSoftwareReleases(ctx context.Context) ([]software.Release, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT version, channel, published_at, minimum_protocol_version, key_id, envelope, source, imported_at
		FROM software_releases ORDER BY imported_at DESC, version`)
	if err != nil {
		return nil, wrap("list software releases", err)
	}
	defer rows.Close()
	var releases []software.Release
	for rows.Next() {
		var release software.Release
		if err := rows.Scan(&release.Version, &release.Channel, &release.PublishedAt, &release.MinimumProtocolVersion, &release.KeyID, &release.Envelope, &release.Source, &release.ImportedAt); err != nil {
			return nil, wrap("scan software release", err)
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("list software releases", err)
	}
	artifacts, err := s.softwareArtifacts(ctx, "")
	if err != nil {
		return nil, err
	}
	for i := range releases {
		releases[i].Artifacts = artifacts[releases[i].Version]
	}
	return releases, nil
}

func (s *Store) GetSoftwareRelease(ctx context.Context, version string) (software.Release, error) {
	var release software.Release
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT version, channel, published_at, minimum_protocol_version, key_id, envelope, source, imported_at
		FROM software_releases WHERE version = $1`, version).
		Scan(&release.Version, &release.Channel, &release.PublishedAt, &release.MinimumProtocolVersion, &release.KeyID, &release.Envelope, &release.Source, &release.ImportedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return software.Release{}, fmt.Errorf("%w: release %s is not imported", software.ErrNotFound, version)
	}
	if err != nil {
		return software.Release{}, wrap("read software release", err)
	}
	artifacts, err := s.softwareArtifacts(ctx, version)
	if err != nil {
		return software.Release{}, err
	}
	release.Artifacts = artifacts[version]
	return release, nil
}

// softwareArtifacts groups artifacts by version; an empty version reads all.
func (s *Store) softwareArtifacts(ctx context.Context, version string) (map[string][]software.Artifact, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT version, name, component, os, arch, format, sha256, size, cas_state = 'ready'
		FROM software_release_artifacts WHERE $1 = '' OR version = $1 ORDER BY version, name`, version)
	if err != nil {
		return nil, wrap("list software artifacts", err)
	}
	defer rows.Close()
	grouped := map[string][]software.Artifact{}
	for rows.Next() {
		var owner string
		var artifact software.Artifact
		if err := rows.Scan(&owner, &artifact.Name, &artifact.Component, &artifact.OS, &artifact.Arch, &artifact.Format, &artifact.SHA256, &artifact.Size, &artifact.Ready); err != nil {
			return nil, wrap("scan software artifact", err)
		}
		grouped[owner] = append(grouped[owner], artifact)
	}
	return grouped, wrap("list software artifacts", rows.Err())
}

func (s *Store) SoftwareArtifactReady(ctx context.Context, sha256 string) (bool, error) {
	var ready bool
	err := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM software_release_artifacts WHERE sha256 = $1 AND cas_state = 'ready')`, sha256).Scan(&ready)
	return ready, wrap("check software artifact", err)
}

func (s *Store) SoftwareArtifact(ctx context.Context, version, name string) (software.Artifact, error) {
	var artifact software.Artifact
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT name, component, os, arch, format, sha256, size, cas_state = 'ready'
		FROM software_release_artifacts WHERE version = $1 AND name = $2`, version, name).
		Scan(&artifact.Name, &artifact.Component, &artifact.OS, &artifact.Arch, &artifact.Format, &artifact.SHA256, &artifact.Size, &artifact.Ready)
	if errors.Is(err, sql.ErrNoRows) {
		return software.Artifact{}, software.ErrNotFound
	}
	return artifact, wrap("read software artifact", err)
}

const softwareNodeQuery = `
	SELECT n.id::text, n.name, n.node_class, n.credential_state, r.report,
	       t.version, t.requested_by::text, t.requested_at,
	       f.event
	FROM nodes n
	LEFT JOIN node_capability_reports r ON r.node_id = n.id
	LEFT JOIN node_software_targets t ON t.node_id = n.id
	LEFT JOIN LATERAL (
		SELECT e.event FROM node_events e
		WHERE e.node_id = n.id AND e.event->>'type' = '` + software.EventUpdateFailed + `'
		ORDER BY e.created_at DESC LIMIT 1
	) f ON true`

func (s *Store) ListSoftwareNodes(ctx context.Context) ([]software.NodeRecord, error) {
	rows, err := s.q(ctx).QueryContext(ctx, softwareNodeQuery+` WHERE n.credential_state <> 'revoked' ORDER BY n.name, n.id`)
	if err != nil {
		return nil, wrap("list software nodes", err)
	}
	defer rows.Close()
	var nodes []software.NodeRecord
	for rows.Next() {
		node, err := scanSoftwareNode(rows)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, wrap("list software nodes", rows.Err())
}

func (s *Store) SoftwareNode(ctx context.Context, nodeID string) (software.NodeRecord, error) {
	if !softwareNodeIDPattern.MatchString(nodeID) {
		return software.NodeRecord{}, fmt.Errorf("%w: node %s", software.ErrNotFound, nodeID)
	}
	rows, err := s.q(ctx).QueryContext(ctx, softwareNodeQuery+` WHERE n.id = $1`, nodeID)
	if err != nil {
		return software.NodeRecord{}, wrap("read software node", err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return software.NodeRecord{}, wrap("read software node", err)
		}
		return software.NodeRecord{}, fmt.Errorf("%w: node %s", software.ErrNotFound, nodeID)
	}
	return scanSoftwareNode(rows)
}

func scanSoftwareNode(rows *sql.Rows) (software.NodeRecord, error) {
	var node software.NodeRecord
	var report, failure []byte
	var targetVersion, targetBy sql.NullString
	var targetAt sql.NullTime
	if err := rows.Scan(&node.ID, &node.Name, &node.NodeClass, &node.CredentialState, &report, &targetVersion, &targetBy, &targetAt, &failure); err != nil {
		return software.NodeRecord{}, wrap("scan software node", err)
	}
	if report != nil {
		var decoded playerclient.CapabilityReport
		if err := json.Unmarshal(report, &decoded); err != nil {
			return software.NodeRecord{}, wrap("decode node capabilities", err)
		}
		node.Report = &decoded
	}
	if targetVersion.Valid {
		node.Target = &software.Target{Version: targetVersion.String, RequestedBy: targetBy.String, RequestedAt: targetAt.Time}
	}
	if failure != nil {
		var event playerclient.NodeEvent
		if err := json.Unmarshal(failure, &event); err == nil {
			version, _ := event.Data["version"].(string)
			message, _ := event.Data["error"].(string)
			node.Failure = &software.Failure{Version: version, Error: message, At: event.OccurredAt}
		}
	}
	return node, nil
}

func (s *Store) SetSoftwareTarget(ctx context.Context, nodeID, version, actorID string) error {
	return s.changeSoftwareTarget(ctx, nodeID, version, actorID)
}

func (s *Store) ClearSoftwareTarget(ctx context.Context, nodeID, actorID string) error {
	return s.changeSoftwareTarget(ctx, nodeID, "", actorID)
}

// changeSoftwareTarget sets (or clears, for an empty version) the target and
// bumps the node's software generation so assignment polls observe it.
func (s *Store) changeSoftwareTarget(ctx context.Context, nodeID, version, actorID string) error {
	if !softwareNodeIDPattern.MatchString(nodeID) {
		return fmt.Errorf("%w: node %s", software.ErrNotFound, nodeID)
	}
	return s.inTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var current sql.NullString
		err := tx.QueryRowContext(ctx, `
			SELECT t.version FROM nodes n LEFT JOIN node_software_targets t ON t.node_id = n.id
			WHERE n.id = $1 FOR UPDATE OF n`, nodeID).Scan(&current)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: node %s", software.ErrNotFound, nodeID)
		}
		if err != nil {
			return wrap("lock node software target", err)
		}
		if current.String == version {
			return nil
		}
		if version == "" {
			_, err = tx.ExecContext(ctx, `DELETE FROM node_software_targets WHERE node_id = $1`, nodeID)
		} else {
			_, err = tx.ExecContext(ctx, `
				INSERT INTO node_software_targets (node_id, version, requested_by, requested_at) VALUES ($1, $2, $3, now())
				ON CONFLICT (node_id) DO UPDATE SET version = EXCLUDED.version, requested_by = EXCLUDED.requested_by, requested_at = EXCLUDED.requested_at`,
				nodeID, version, actorID)
		}
		if err != nil {
			if postgresCode(err) == "23503" {
				return fmt.Errorf("%w: release %s is not imported", software.ErrNotFound, version)
			}
			return wrap("write node software target", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO node_software_target_history (node_id, version, requested_by) VALUES ($1, NULLIF($2, ''), $3)`,
			nodeID, version, actorID); err != nil {
			return wrap("record node software target", err)
		}
		_, err = tx.ExecContext(ctx, `UPDATE nodes SET software_generation = software_generation + 1 WHERE id = $1`, nodeID)
		return wrap("bump node software generation", err)
	})
}

func (s *Store) PlayerSoftwareTarget(ctx context.Context, nodeID string) (string, int64, string, error) {
	var version, method sql.NullString
	var generation int64
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT t.version, n.software_generation, r.report->>'updateMethod'
		FROM nodes n
		LEFT JOIN node_software_targets t ON t.node_id = n.id
		LEFT JOIN node_capability_reports r ON r.node_id = n.id
		WHERE n.id = $1`, nodeID).Scan(&version, &generation, &method)
	if errors.Is(err, sql.ErrNoRows) {
		return "", 0, "", fmt.Errorf("%w: node %s", software.ErrNotFound, nodeID)
	}
	if err != nil {
		return "", 0, "", wrap("read player software target", err)
	}
	return version.String, generation, method.String, nil
}

func (s *Store) SoftwareFeedState(ctx context.Context) (software.FeedState, error) {
	var state software.FeedState
	var checked, succeeded sql.NullTime
	err := s.q(ctx).QueryRowContext(ctx, `SELECT url, last_checked_at, last_success_at, last_error, last_version FROM software_feed_state`).
		Scan(&state.URL, &checked, &succeeded, &state.LastError, &state.LastVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return software.FeedState{}, nil
	}
	if err != nil {
		return software.FeedState{}, wrap("read software feed state", err)
	}
	state.LastCheckedAt, state.LastSuccessAt = optionalTime(checked), optionalTime(succeeded)
	return state, nil
}

func (s *Store) PutSoftwareFeedState(ctx context.Context, state software.FeedState) error {
	_, err := s.q(ctx).ExecContext(ctx, `
		INSERT INTO software_feed_state (id, url, last_checked_at, last_success_at, last_error, last_version)
		VALUES (true, $1, $2, $3, $4, $5)
		ON CONFLICT (id) DO UPDATE SET url = EXCLUDED.url, last_checked_at = EXCLUDED.last_checked_at,
			last_success_at = EXCLUDED.last_success_at, last_error = EXCLUDED.last_error, last_version = EXCLUDED.last_version`,
		state.URL, state.LastCheckedAt, state.LastSuccessAt, state.LastError, state.LastVersion)
	return wrap("write software feed state", err)
}

func optionalTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time.UTC()
	return &at
}

var softwareNodeIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
