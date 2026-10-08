package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmill/cable/internal/control/cableguide"
)

var _ cableguide.Repository = (*Store)(nil)
var _ cableguide.ConversionRepository = (*Store)(nil)

func (s *Store) PendingCableConversions(ctx context.Context) ([]cableguide.LegacyAssignment, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT node_id::text, media_id::text, desired_generation, remote_input, show_qr, actor_id::text FROM cable_assignment_conversions ORDER BY node_id`)
	if err != nil {
		return nil, wrap("list cable assignment conversions", err)
	}
	defer rows.Close()
	var pending []cableguide.LegacyAssignment
	for rows.Next() {
		var value cableguide.LegacyAssignment
		if err := rows.Scan(&value.NodeID, &value.MediaID, &value.DesiredGeneration, &value.RemoteInput, &value.ShowQR, &value.ActorID); err != nil {
			return nil, wrap("scan cable assignment conversion", err)
		}
		pending = append(pending, value)
	}
	return pending, wrap("iterate cable assignment conversions", rows.Err())
}

func (s *Store) CompleteCableConversion(ctx context.Context, nodeID string) error {
	_, err := s.q(ctx).ExecContext(ctx, `DELETE FROM cable_assignment_conversions WHERE node_id = $1`, nodeID)
	return wrap("complete cable assignment conversion", err)
}

// nodeCredentialActiveSQL matches nodes (alias n) that are not revoked and
// still hold an unexpired native or browser credential at $now.
const nodeCredentialActiveSQL = `n.credential_state = 'active' AND (
	EXISTS (SELECT 1 FROM node_credentials c WHERE c.node_id = n.id AND c.revoked_at IS NULL AND c.not_after > %s)
	OR EXISTS (SELECT 1 FROM browser_node_credentials b WHERE b.node_id = n.id AND b.revoked_at IS NULL AND b.expires_at > %s))`

func nodeActiveClause(nowParam string) string {
	return fmt.Sprintf(nodeCredentialActiveSQL, nowParam, nowParam)
}

func (s *Store) CreateCableGuideToken(ctx context.Context, hash []byte, grant cableguide.Grant, now time.Time) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(txCtx, `DELETE FROM cable_guide_tokens WHERE expires_at <= $1`, now); err != nil {
			return wrap("prune cable guide tokens", err)
		}
		_, err := tx.ExecContext(txCtx, `INSERT INTO cable_guide_tokens (token_hash, node_id, deployment_id, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, hash, grant.NodeID, grant.DeploymentID, now, grant.ExpiresAt)
		return wrap("create cable guide token", err)
	})
}

func (s *Store) CableGuideGrant(ctx context.Context, hash []byte, now time.Time) (cableguide.Grant, error) {
	var grant cableguide.Grant
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT t.node_id::text, t.deployment_id::text, t.expires_at
		FROM cable_guide_tokens t JOIN nodes n ON n.id = t.node_id
		WHERE t.token_hash = $1 AND t.expires_at > $2 AND `+nodeActiveClause("$2"), hash, now).Scan(&grant.NodeID, &grant.DeploymentID, &grant.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return cableguide.Grant{}, cableguide.ErrUnauthorized
	}
	return grant, wrap("get cable guide token", err)
}

// NodeCableDeployment returns the deployment whose Cable media item the
// node's current (highest generation) assignment targets, or "" when the node
// is not showing a Cable guide or no longer holds a valid credential.
func (s *Store) NodeCableDeployment(ctx context.Context, nodeID string, now time.Time) (string, error) {
	var deploymentID string
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT m.deployment_id::text
		FROM (SELECT media_id FROM assignments WHERE node_id = $1 ORDER BY desired_generation DESC LIMIT 1) a
		JOIN media m ON m.id = a.media_id
		JOIN nodes n ON n.id = $1
		WHERE m.deployment_id IS NOT NULL AND `+nodeActiveClause("$2"), nodeID, now).Scan(&deploymentID)
	if errors.Is(err, sql.ErrNoRows) || postgresCode(err) == invalidTextRepresentation {
		return "", nil
	}
	return deploymentID, wrap("get node cable deployment", err)
}

// DeploymentReferencesAsset reports whether the deployment's channels,
// blocks or block playlists reference the asset.
func (s *Store) DeploymentReferencesAsset(ctx context.Context, deploymentID, assetID string) (bool, error) {
	var referenced bool
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM cable_channels c WHERE c.deployment_id = $1 AND c.artwork_asset_id = $2
			UNION ALL
			SELECT 1 FROM cable_blocks b JOIN cable_channels c ON c.id = b.channel_id WHERE c.deployment_id = $1 AND b.asset_id = $2
			UNION ALL
			SELECT 1 FROM cable_blocks b JOIN cable_channels c ON c.id = b.channel_id
				JOIN cable_playlist_items i ON i.playlist_id = b.playlist_id
			WHERE c.deployment_id = $1 AND i.asset_id = $2)`, deploymentID, assetID).Scan(&referenced)
	if postgresCode(err) == invalidTextRepresentation {
		return false, nil
	}
	return referenced, wrap("check cable deployment asset", err)
}
