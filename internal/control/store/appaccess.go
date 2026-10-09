package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmilldotdev/cuelight/internal/control/appaccess"
)

var _ appaccess.Repository = (*Store)(nil)

// nodeCredentialActiveSQL matches nodes (alias n) that are not revoked and
// still hold an unexpired native or browser credential at $now.
const nodeCredentialActiveSQL = `n.credential_state = 'active' AND (
	EXISTS (SELECT 1 FROM node_credentials c WHERE c.node_id = n.id AND c.revoked_at IS NULL AND c.not_after > %s)
	OR EXISTS (SELECT 1 FROM browser_node_credentials b WHERE b.node_id = n.id AND b.revoked_at IS NULL AND b.expires_at > %s))`

func nodeActiveClause(nowParam string) string {
	return fmt.Sprintf(nodeCredentialActiveSQL, nowParam, nowParam)
}

func (s *Store) CreateAppToken(ctx context.Context, hash []byte, grant appaccess.Grant, now time.Time) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(txCtx, `DELETE FROM app_tokens WHERE expires_at <= $1`, now); err != nil {
			return wrap("prune app tokens", err)
		}
		_, err := tx.ExecContext(txCtx, `INSERT INTO app_tokens (token_hash, node_id, media_id, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`, hash, grant.NodeID, grant.MediaID, now, grant.ExpiresAt)
		return wrap("create app token", err)
	})
}

func (s *Store) AppGrant(ctx context.Context, hash []byte, now time.Time) (appaccess.Grant, error) {
	var grant appaccess.Grant
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT t.node_id::text, n.name, t.media_id::text, t.expires_at
		FROM app_tokens t JOIN nodes n ON n.id = t.node_id
		WHERE t.token_hash = $1 AND t.expires_at > $2 AND `+nodeActiveClause("$2"), hash, now).Scan(&grant.NodeID, &grant.NodeName, &grant.MediaID, &grant.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return appaccess.Grant{}, appaccess.ErrUnauthorized
	}
	return grant, wrap("get app token", err)
}

// NodeAppMedia returns the app-access web media item the node's current
// (highest generation) assignment targets, or "" when the node shows anything
// else or no longer holds a valid credential.
func (s *Store) NodeAppMedia(ctx context.Context, nodeID string, now time.Time) (string, error) {
	var mediaID string
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT m.id::text
		FROM (SELECT media_id FROM assignments WHERE node_id = $1 ORDER BY desired_generation DESC LIMIT 1) a
		JOIN media m ON m.id = a.media_id
		JOIN nodes n ON n.id = $1
		WHERE m.kind = 'web' AND m.app_access AND `+nodeActiveClause("$2"), nodeID, now).Scan(&mediaID)
	if errors.Is(err, sql.ErrNoRows) || postgresCode(err) == invalidTextRepresentation {
		return "", nil
	}
	return mediaID, wrap("get node app media", err)
}
