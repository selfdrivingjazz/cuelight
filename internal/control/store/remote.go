package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmilldotdev/cuelight/internal/control/remote"
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
