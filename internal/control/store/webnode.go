package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/jmill/cable/internal/control/webnode"
)

// lastSeenResolution bounds how often authenticated browser requests (which
// include every media range request) write last_seen_at.
const lastSeenResolution = 30 * time.Second

const invalidTextRepresentation = "22P02"

func (s *Store) CreateBrowserClaim(ctx context.Context, claim webnode.Claim, now time.Time) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(txCtx, `DELETE FROM browser_node_claims WHERE node_id IS NULL AND expires_at < $1`, now.Add(-time.Hour)); err != nil {
			return wrap("purge browser claims", err)
		}
		_, err := tx.ExecContext(txCtx, `
			INSERT INTO browser_node_claims (id, code_hash, poll_token_hash, user_agent, expires_at, created_at)
			VALUES ($1, $2, $3, $4, $5, $6)`, claim.ID, claim.CodeHash, claim.PollTokenHash, claim.UserAgent, claim.ExpiresAt, claim.CreatedAt)
		if postgresCode(err) == "23505" {
			return webnode.ErrCodeCollision
		}
		return wrap("create browser claim", err)
	})
}

func (s *Store) BrowserClaim(ctx context.Context, id string) (webnode.Claim, bool, error) {
	var claim webnode.Claim
	var nodeID sql.NullString
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT id::text, code_hash, poll_token_hash, user_agent, expires_at, created_at, node_id::text, delivered_at IS NOT NULL
		FROM browser_node_claims WHERE id = $1`, id).Scan(&claim.ID, &claim.CodeHash, &claim.PollTokenHash, &claim.UserAgent, &claim.ExpiresAt, &claim.CreatedAt, &nodeID, &claim.Delivered)
	if errors.Is(err, sql.ErrNoRows) || postgresCode(err) == invalidTextRepresentation {
		return webnode.Claim{}, false, nil
	}
	if err != nil {
		return webnode.Claim{}, false, wrap("read browser claim", err)
	}
	claim.NodeID = nodeID.String
	return claim, true, nil
}

func (s *Store) RedeemBrowserClaim(ctx context.Context, codeHash []byte, name, actorID string, now time.Time) (webnode.Node, error) {
	var node webnode.Node
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var claimID, userAgent string
		var claimed bool
		var expiresAt time.Time
		err := tx.QueryRowContext(txCtx, `
			SELECT id::text, node_id IS NOT NULL, expires_at, user_agent
			FROM browser_node_claims WHERE code_hash = $1 FOR UPDATE`, codeHash).Scan(&claimID, &claimed, &expiresAt, &userAgent)
		if errors.Is(err, sql.ErrNoRows) {
			return webnode.ErrClaimNotFound
		}
		if err != nil {
			return wrap("lock browser claim", err)
		}
		if claimed {
			return webnode.ErrClaimConflict
		}
		if !now.Before(expiresAt) {
			return webnode.ErrClaimNotFound
		}
		nodeID, err := s.newUUID()
		if err != nil {
			return err
		}
		epoch, err := s.newUUID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(txCtx, `
			INSERT INTO nodes (id, name, credential_state, event_epoch, node_class, created_at)
			VALUES ($1, $2, 'active', $3, 'browser', $4)`, nodeID, name, epoch, now); err != nil {
			return wrap("create browser node", err)
		}
		var actor any
		if actorID != "" {
			actor = actorID
		}
		if _, err := tx.ExecContext(txCtx, `
			UPDATE browser_node_claims SET node_id = $2, claimed_by = $3, claimed_at = $4 WHERE id = $1`, claimID, nodeID, actor, now); err != nil {
			return wrap("bind browser claim", err)
		}
		node = webnode.Node{ID: nodeID, Name: name, NodeClass: webnode.NodeClassBrowser, CredentialState: "active", UserAgent: userAgent, CreatedAt: now}
		return nil
	})
	return node, err
}

func (s *Store) DeliverBrowserCredential(ctx context.Context, claimID string, credential webnode.Credential, now time.Time) (string, error) {
	var epoch string
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		var nodeID sql.NullString
		var delivered bool
		err := tx.QueryRowContext(txCtx, `
			SELECT node_id::text, delivered_at IS NOT NULL FROM browser_node_claims WHERE id = $1 FOR UPDATE`, claimID).Scan(&nodeID, &delivered)
		if errors.Is(err, sql.ErrNoRows) {
			return webnode.ErrClaimGone
		}
		if err != nil {
			return wrap("lock browser claim", err)
		}
		if delivered || !nodeID.Valid || nodeID.String != credential.NodeID {
			return webnode.ErrClaimGone
		}
		if err := tx.QueryRowContext(txCtx, `SELECT event_epoch::text FROM nodes WHERE id = $1 AND credential_state = 'active'`, nodeID.String).Scan(&epoch); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return webnode.ErrClaimGone
			}
			return wrap("read browser node epoch", err)
		}
		if err := insertBrowserCredential(txCtx, tx, credential, now); err != nil {
			return err
		}
		_, err = tx.ExecContext(txCtx, `UPDATE browser_node_claims SET delivered_at = $2 WHERE id = $1`, claimID, now)
		return wrap("mark browser claim delivered", err)
	})
	return epoch, err
}

func insertBrowserCredential(ctx context.Context, tx *sql.Tx, credential webnode.Credential, now time.Time) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO browser_node_credentials (id, node_id, token_hash, expires_at, renew_after, last_seen_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, credential.ID, credential.NodeID, credential.TokenHash, credential.ExpiresAt, credential.RenewAfter, now, credential.CreatedAt)
	return wrap("create browser credential", err)
}

func (s *Store) BrowserCredential(ctx context.Context, tokenHash []byte, now time.Time) (webnode.Credential, bool, error) {
	var credential webnode.Credential
	var lastSeen sql.NullTime
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT c.id::text, c.node_id::text, c.token_hash, c.expires_at, c.renew_after, c.created_at, c.last_seen_at, n.event_epoch::text
		FROM browser_node_credentials c JOIN nodes n ON n.id = c.node_id
		WHERE c.token_hash = $1 AND c.revoked_at IS NULL AND c.expires_at > $2
		  AND n.node_class = 'browser' AND n.credential_state = 'active'`, tokenHash, now).Scan(
		&credential.ID, &credential.NodeID, &credential.TokenHash, &credential.ExpiresAt, &credential.RenewAfter, &credential.CreatedAt, &lastSeen, &credential.EventEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return webnode.Credential{}, false, nil
	}
	if err != nil {
		return webnode.Credential{}, false, wrap("read browser credential", err)
	}
	if !lastSeen.Valid || now.Sub(lastSeen.Time) >= lastSeenResolution {
		if _, err := s.q(ctx).ExecContext(ctx, `UPDATE browser_node_credentials SET last_seen_at = $2 WHERE id = $1`, credential.ID, now); err != nil {
			return webnode.Credential{}, false, wrap("touch browser credential", err)
		}
	}
	return credential, true, nil
}

func (s *Store) RotateBrowserCredential(ctx context.Context, previousID string, next webnode.Credential, now time.Time) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(txCtx, `UPDATE browser_node_credentials SET revoked_at = $2 WHERE id = $1 AND revoked_at IS NULL`, previousID, now)
		if err != nil {
			return wrap("revoke rotated browser credential", err)
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return webnode.ErrUnauthorized
		}
		return insertBrowserCredential(txCtx, tx, next, now)
	})
}

func (s *Store) ListBrowserNodes(ctx context.Context, now time.Time) ([]webnode.Node, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		SELECT n.id::text, n.name, n.credential_state, n.created_at, c.expires_at, c.last_seen_at, COALESCE(cl.user_agent, '')
		FROM nodes n
		LEFT JOIN LATERAL (
			SELECT expires_at, last_seen_at FROM browser_node_credentials
			WHERE node_id = n.id AND revoked_at IS NULL ORDER BY created_at DESC LIMIT 1
		) c ON true
		LEFT JOIN LATERAL (
			SELECT user_agent FROM browser_node_claims WHERE node_id = n.id ORDER BY created_at DESC LIMIT 1
		) cl ON true
		WHERE n.node_class = 'browser' AND n.credential_state <> 'revoked'
		ORDER BY n.created_at, n.id`)
	if err != nil {
		return nil, wrap("list browser nodes", err)
	}
	defer rows.Close()
	nodes := make([]webnode.Node, 0)
	for rows.Next() {
		node := webnode.Node{NodeClass: webnode.NodeClassBrowser}
		var expires, seen sql.NullTime
		if err := rows.Scan(&node.ID, &node.Name, &node.CredentialState, &node.CreatedAt, &expires, &seen, &node.UserAgent); err != nil {
			return nil, wrap("scan browser node", err)
		}
		if expires.Valid {
			node.CredentialExpiresAt = &expires.Time
			if node.CredentialState == "active" && !now.Before(expires.Time) {
				node.CredentialState = "expired"
			}
		}
		if seen.Valid {
			node.LastSeenAt = &seen.Time
		}
		nodes = append(nodes, node)
	}
	return nodes, wrap("iterate browser nodes", rows.Err())
}

func (s *Store) RevokeBrowserNode(ctx context.Context, nodeID string, now time.Time) error {
	return s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(txCtx, `UPDATE nodes SET credential_state = 'revoked' WHERE id = $1 AND node_class = 'browser'`, nodeID)
		if postgresCode(err) == invalidTextRepresentation {
			return webnode.ErrNodeNotFound
		}
		if err != nil {
			return wrap("revoke browser node", err)
		}
		if rows, _ := result.RowsAffected(); rows != 1 {
			return webnode.ErrNodeNotFound
		}
		if _, err = tx.ExecContext(txCtx, `DELETE FROM cable_guide_tokens WHERE node_id = $1`, nodeID); err != nil {
			return wrap("revoke cable guide tokens", err)
		}
		_, err = tx.ExecContext(txCtx, `UPDATE browser_node_credentials SET revoked_at = $2 WHERE node_id = $1 AND revoked_at IS NULL`, nodeID, now)
		return wrap("revoke browser credentials", err)
	})
}

var _ webnode.Repository = (*Store)(nil)
