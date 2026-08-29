package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jmill/cable/internal/control/remote"
)

var _ remote.PolicyRepository = (*Store)(nil)

func (s *Store) RemotePolicy(ctx context.Context, nodeID string, generation int64) (remote.Policy, error) {
	var policy remote.Policy
	err := s.q(ctx).QueryRowContext(ctx, `SELECT a.remote_input, a.show_qr FROM assignments a JOIN nodes n ON n.id=a.node_id WHERE a.node_id=$1 AND a.desired_generation=$2 AND n.desired_generation=$2`, nodeID, generation).Scan(&policy.Enabled, &policy.ShowQR)
	if errors.Is(err, sql.ErrNoRows) {
		return remote.Policy{}, remote.ErrDisabled
	}
	return policy, wrap("read remote policy", err)
}
