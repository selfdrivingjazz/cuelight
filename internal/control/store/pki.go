package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jmill/cable/internal/control/pki"
)

var _ pki.Repository = (*Store)(nil)
var _ pki.Store = (*Store)(nil)

func (s *Store) IdempotentTransaction(ctx context.Context, request pki.IdempotencyRequest, fn func(pki.Store) ([]byte, error)) ([]byte, error) {
	if request.Operation == "" || request.ActorID == "" || request.Key == "" || request.RequestHash == "" || !request.ExpiresAt.After(request.CreatedAt) {
		return nil, pki.ErrInvalidMutation
	}
	var response []byte
	err := s.inTransaction(ctx, func(txCtx context.Context, tx *sql.Tx) error {
		claim, err := tx.ExecContext(txCtx, `
			INSERT INTO pki_idempotency_records
			  (operation, actor_id, key, request_hash, response_ciphertext, created_at, expires_at)
			VALUES ($1, $2, $3, $4, NULL, $5, $6)
			ON CONFLICT (operation, actor_id, key) DO NOTHING`,
			request.Operation, request.ActorID, request.Key, request.RequestHash, request.CreatedAt, request.ExpiresAt)
		if err != nil {
			return wrap("claim PKI idempotency key", err)
		}
		claimed, err := claim.RowsAffected()
		if err != nil {
			return wrap("inspect PKI idempotency claim", err)
		}
		var storedHash string
		var storedResponse []byte
		var storedExpiry time.Time
		err = tx.QueryRowContext(txCtx, `
			SELECT request_hash, response_ciphertext, expires_at
			FROM pki_idempotency_records
			WHERE operation = $1 AND actor_id = $2 AND key = $3
			FOR UPDATE`, request.Operation, request.ActorID, request.Key).Scan(&storedHash, &storedResponse, &storedExpiry)
		if err != nil {
			return wrap("lock PKI idempotency key", err)
		}
		if claimed == 0 && storedExpiry.After(request.CreatedAt) {
			if storedHash != request.RequestHash {
				return pki.ErrIdempotencyConflict
			}
			if len(storedResponse) == 0 {
				return pki.ErrInvalidIdempotencyResult
			}
			response = append([]byte(nil), storedResponse...)
			return nil
		}
		if claimed == 0 {
			_, err = tx.ExecContext(txCtx, `
				UPDATE pki_idempotency_records
				SET request_hash = $4, response_ciphertext = NULL, created_at = $5, expires_at = $6
				WHERE operation = $1 AND actor_id = $2 AND key = $3`,
				request.Operation, request.ActorID, request.Key, request.RequestHash, request.CreatedAt, request.ExpiresAt)
			if err != nil {
				return wrap("replace expired PKI idempotency record", err)
			}
		}
		sealed, err := fn(s.transactionStore(tx))
		if err != nil {
			return err
		}
		if len(sealed) == 0 {
			return pki.ErrInvalidIdempotencyResult
		}
		_, err = tx.ExecContext(txCtx, `
			UPDATE pki_idempotency_records SET response_ciphertext = $4
			WHERE operation = $1 AND actor_id = $2 AND key = $3`,
			request.Operation, request.ActorID, request.Key, sealed)
		if err != nil {
			return wrap("record sealed PKI response", err)
		}
		response = append([]byte(nil), sealed...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func (s *Store) CreateToken(ctx context.Context, token pki.Token) (pki.Token, error) {
	id, err := s.newUUID()
	if err != nil {
		return pki.Token{}, err
	}
	var nodeID any
	if token.NodeID != "" {
		nodeID = token.NodeID
	}
	token.ID = id
	_, err = s.q(ctx).ExecContext(ctx, `
		INSERT INTO enrollment_tokens
		  (id, token_hash, node_id, purpose, expires_at, consumed_at, created_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		token.ID, string(token.Hash), nodeID, token.Purpose, token.ExpiresAt, token.ConsumedAt, token.CreatedBy, token.CreatedAt)
	if err != nil {
		return pki.Token{}, wrap("create PKI token", err)
	}
	return token, nil
}

func (s *Store) ConsumeToken(ctx context.Context, hash pki.TokenHash, at time.Time) (pki.Token, error) {
	var token pki.Token
	var nodeID sql.NullString
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT id::text, token_hash, purpose, node_id::text, expires_at, consumed_at, created_by::text, created_at
		FROM enrollment_tokens WHERE token_hash = $1 FOR UPDATE`, string(hash)).Scan(
		&token.ID, &token.Hash, &token.Purpose, &nodeID, &token.ExpiresAt, &token.ConsumedAt, &token.CreatedBy, &token.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return pki.Token{}, pki.ErrTokenNotFound
	}
	if err != nil {
		return pki.Token{}, wrap("lock PKI token", err)
	}
	if token.ConsumedAt != nil {
		return pki.Token{}, pki.ErrTokenConsumed
	}
	token.NodeID = nodeID.String
	_, err = s.q(ctx).ExecContext(ctx, `UPDATE enrollment_tokens SET consumed_at = $2 WHERE id = $1`, token.ID, at)
	if err != nil {
		return pki.Token{}, wrap("consume PKI token", err)
	}
	consumed := at
	token.ConsumedAt = &consumed
	return token, nil
}

func (s *Store) CreateNode(ctx context.Context, name, epoch string, at time.Time) (pki.Node, error) {
	id, err := s.newUUID()
	if err != nil {
		return pki.Node{}, err
	}
	if name == "" {
		name = id
	}
	node := pki.Node{ID: id, Name: name, EventEpoch: epoch, CredentialState: pki.CredentialActive, CreatedAt: at}
	_, err = s.q(ctx).ExecContext(ctx, `
		INSERT INTO nodes (id, name, credential_state, event_epoch, created_at)
		VALUES ($1, $2, $3, $4, $5)`, node.ID, node.Name, node.CredentialState, node.EventEpoch, node.CreatedAt)
	if err != nil {
		return pki.Node{}, wrap("create node", err)
	}
	return node, nil
}

func (s *Store) Node(ctx context.Context, id string) (pki.Node, error) {
	var node pki.Node
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT id::text, name, event_epoch::text, credential_state, created_at
		FROM nodes WHERE id = $1`, id).Scan(&node.ID, &node.Name, &node.EventEpoch, &node.CredentialState, &node.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return pki.Node{}, pki.ErrNodeNotFound
	}
	return node, wrap("read node", err)
}

func (s *Store) UpdateNode(ctx context.Context, node pki.Node) error {
	result, err := s.q(ctx).ExecContext(ctx, `
		UPDATE nodes SET name = $2, event_epoch = $3, credential_state = $4 WHERE id = $1`,
		node.ID, node.Name, node.EventEpoch, node.CredentialState)
	if err != nil {
		return wrap("update node", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return wrap("inspect node update", err)
	}
	if changed == 0 {
		return pki.ErrNodeNotFound
	}
	return nil
}

func (s *Store) CreateCredential(ctx context.Context, credential pki.Credential) error {
	_, err := s.q(ctx).ExecContext(ctx, `
		INSERT INTO node_credentials (node_id, serial, not_after, revoked_at, created_at)
		VALUES ($1, $2, $3, $4, $5)`,
		credential.NodeID, credential.Serial, credential.NotAfter, credential.RevokedAt, credential.CreatedAt)
	return wrap("create node credential", err)
}

func (s *Store) Credential(ctx context.Context, nodeID, serial string) (pki.Credential, error) {
	var credential pki.Credential
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT node_id::text, serial, not_after, created_at, revoked_at
		FROM node_credentials WHERE node_id = $1 AND serial = $2`, nodeID, serial).Scan(
		&credential.NodeID, &credential.Serial, &credential.NotAfter, &credential.CreatedAt, &credential.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return pki.Credential{}, pki.ErrCredentialNotFound
	}
	return credential, wrap("read node credential", err)
}

func (s *Store) HasActiveCredential(ctx context.Context, nodeID string, at time.Time) (bool, error) {
	var active bool
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM node_credentials
			WHERE node_id = $1 AND revoked_at IS NULL AND not_after > $2
		)`, nodeID, at).Scan(&active)
	return active, wrap("check active node credential", err)
}

func (s *Store) RevokeCredential(ctx context.Context, nodeID, serial string, at time.Time) (pki.Credential, error) {
	var credential pki.Credential
	err := s.q(ctx).QueryRowContext(ctx, `
		UPDATE node_credentials SET revoked_at = $3
		WHERE node_id = $1 AND serial = $2 AND revoked_at IS NULL
		RETURNING node_id::text, serial, not_after, created_at, revoked_at`, nodeID, serial, at).Scan(
		&credential.NodeID, &credential.Serial, &credential.NotAfter, &credential.CreatedAt, &credential.RevokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		var exists bool
		checkErr := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM node_credentials WHERE node_id = $1 AND serial = $2)`, nodeID, serial).Scan(&exists)
		if checkErr != nil {
			return pki.Credential{}, wrap("classify credential revocation", checkErr)
		}
		if exists {
			return pki.Credential{}, pki.ErrCredentialRevoked
		}
		return pki.Credential{}, pki.ErrCredentialNotFound
	}
	return credential, wrap("revoke node credential", err)
}

func (s *Store) RevokeNodeCredentials(ctx context.Context, nodeID string, at time.Time) ([]pki.Credential, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `
		UPDATE node_credentials SET revoked_at = $2
		WHERE node_id = $1 AND revoked_at IS NULL
		RETURNING node_id::text, serial, not_after, created_at, revoked_at`, nodeID, at)
	if err != nil {
		return nil, wrap("revoke node credentials", err)
	}
	defer rows.Close()
	credentials := make([]pki.Credential, 0)
	for rows.Next() {
		var credential pki.Credential
		if err := rows.Scan(&credential.NodeID, &credential.Serial, &credential.NotAfter, &credential.CreatedAt, &credential.RevokedAt); err != nil {
			return nil, wrap("scan revoked node credential", err)
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate revoked node credentials", err)
	}
	return credentials, nil
}

func unexpectedPKIRecord(kind, id string) error {
	return fmt.Errorf("invalid stored %s %s", kind, id)
}
