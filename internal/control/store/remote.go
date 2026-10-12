package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/selfdrivingjazz/cuelight/internal/control/remote"
)

var _ remote.PolicyRepository = (*Store)(nil)

// RemotePolicy relies on assignment history holding one row per generation:
// remote input is continuous when every row from since through the node's
// current generation allows it.
func (s *Store) RemotePolicy(ctx context.Context, nodeID string, since int64) (remote.Policy, int64, error) {
	var policy remote.Policy
	var current int64
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT n.desired_generation, bool_and(a.remote_input), bool_or(a.show_qr AND a.desired_generation = n.desired_generation)
		FROM nodes n JOIN assignments a ON a.node_id = n.id AND a.desired_generation BETWEEN $2 AND n.desired_generation
		WHERE n.id = $1
		GROUP BY n.desired_generation`, nodeID, since).Scan(&current, &policy.Enabled, &policy.ShowQR)
	if errors.Is(err, sql.ErrNoRows) {
		return remote.Policy{}, 0, remote.ErrDisabled
	}
	if err != nil {
		return remote.Policy{}, 0, wrap("read remote policy", err)
	}
	policy.ShowQR = policy.ShowQR && policy.Enabled
	return policy, current, nil
}

// CurrentRemotePolicy reads the remote policy of the node's assignment for its
// current desired generation.
func (s *Store) CurrentRemotePolicy(ctx context.Context, nodeID string) (remote.Policy, int64, error) {
	var policy remote.Policy
	var current int64
	err := s.q(ctx).QueryRowContext(ctx, `
		SELECT n.desired_generation, a.remote_input, a.remote_input AND a.show_qr
		FROM nodes n JOIN assignments a ON a.node_id = n.id AND a.desired_generation = n.desired_generation
		WHERE n.id = $1`, nodeID).Scan(&current, &policy.Enabled, &policy.ShowQR)
	if errors.Is(err, sql.ErrNoRows) || postgresCode(err) == invalidTextRepresentation {
		return remote.Policy{}, 0, remote.ErrDisabled
	}
	if err != nil {
		return remote.Policy{}, 0, wrap("read current remote policy", err)
	}
	return policy, current, nil
}
