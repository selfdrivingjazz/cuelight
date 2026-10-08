package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/jmill/cable/internal/control/assignment"
	"github.com/jmill/cable/internal/control/profile"
)

var _ profile.Repository = ProfileRepository{}

// ProfileRepository persists profiles, their screens, and activations.
type ProfileRepository struct{ Store *Store }

const profileColumns = `
	p.id::text, p.name, p.description, p.version, p.created_by::text, p.updated_by::text, p.created_at, p.updated_at,
	(SELECT max(a.activated_at) FROM profile_activations a WHERE a.profile_id = p.id),
	COALESCE(p.id = (SELECT a.profile_id FROM profile_activations a ORDER BY a.activated_at DESC, a.id DESC LIMIT 1), false)`

func (r ProfileRepository) ListProfiles(ctx context.Context) ([]profile.Profile, error) {
	q := r.Store.q(ctx)
	rows, err := q.QueryContext(ctx, `SELECT `+profileColumns+` FROM profiles p ORDER BY lower(p.name), p.id`)
	if err != nil {
		return nil, wrap("list profiles", err)
	}
	defer rows.Close()
	profiles := make([]profile.Profile, 0)
	index := make(map[string]int)
	for rows.Next() {
		value, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		index[value.ID] = len(profiles)
		profiles = append(profiles, value)
	}
	if err := rows.Err(); err != nil {
		return nil, wrap("iterate profiles", err)
	}
	screens, err := r.screens(ctx, q, "")
	if err != nil {
		return nil, err
	}
	for id, list := range screens {
		if i, ok := index[id]; ok {
			profiles[i].Screens = list
		}
	}
	return profiles, nil
}

func (r ProfileRepository) GetProfile(ctx context.Context, id string) (profile.Profile, error) {
	return r.get(ctx, r.Store.q(ctx), id)
}

func (r ProfileRepository) CreateProfile(ctx context.Context, actorID string, input profile.Input) (profile.Profile, error) {
	id, err := r.Store.newUUID()
	if err != nil {
		return profile.Profile{}, err
	}
	var result profile.Profile
	err = r.Store.inTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO profiles (id, name, description, created_by, updated_by) VALUES ($1, $2, $3, $4, $4)`,
			id, input.Name, input.Description, actorID)
		if err := profileWriteError("create profile", err); err != nil {
			return err
		}
		if err := insertScreens(ctx, tx, id, input.Screens); err != nil {
			return err
		}
		result, err = r.get(ctx, tx, id)
		return err
	})
	return result, err
}

func (r ProfileRepository) UpdateProfile(ctx context.Context, actorID, id string, expectedVersion int64, input profile.Input) (profile.Profile, error) {
	var result profile.Profile
	err := r.Store.inTransaction(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var version int64
		err := tx.QueryRowContext(ctx, `SELECT version FROM profiles WHERE id::text = $1 FOR UPDATE`, id).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			return profile.ErrNotFound
		}
		if err != nil {
			return wrap("lock profile", err)
		}
		if version != expectedVersion {
			return profile.ErrVersionConflict
		}
		_, err = tx.ExecContext(ctx, `
			UPDATE profiles SET name = $2, description = $3, updated_by = $4, version = version + 1, updated_at = now()
			WHERE id = $1`, id, input.Name, input.Description, actorID)
		if err := profileWriteError("update profile", err); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM profile_screens WHERE profile_id = $1`, id); err != nil {
			return wrap("replace profile screens", err)
		}
		if err := insertScreens(ctx, tx, id, input.Screens); err != nil {
			return err
		}
		result, err = r.get(ctx, tx, id)
		return err
	})
	return result, err
}

func (r ProfileRepository) DeleteProfile(ctx context.Context, id string) error {
	result, err := r.Store.q(ctx).ExecContext(ctx, `DELETE FROM profiles WHERE id::text = $1`, id)
	if err != nil {
		return wrap("delete profile", err)
	}
	if count, err := result.RowsAffected(); err != nil {
		return wrap("delete profile", err)
	} else if count == 0 {
		return profile.ErrNotFound
	}
	return nil
}

func (r ProfileRepository) InsertActivation(ctx context.Context, activation profile.Activation) (profile.Activation, error) {
	id, err := r.Store.newUUID()
	if err != nil {
		return profile.Activation{}, err
	}
	if activation.Results == nil {
		activation.Results = []profile.Result{}
	}
	results, err := json.Marshal(activation.Results)
	if err != nil {
		return profile.Activation{}, err
	}
	activation.ID = id
	err = r.Store.q(ctx).QueryRowContext(ctx, `
		INSERT INTO profile_activations (id, profile_id, activated_by, results) VALUES ($1, $2, $3, $4)
		RETURNING activated_at`, id, activation.ProfileID, activation.ActivatedBy, results).Scan(&activation.ActivatedAt)
	if postgresCode(err) == "23503" {
		return profile.Activation{}, profile.ErrNotFound
	}
	return activation, wrap("insert profile activation", err)
}

func (r ProfileRepository) LatestActivation(ctx context.Context) (profile.Activation, bool, error) {
	var value profile.Activation
	var results []byte
	err := r.Store.q(ctx).QueryRowContext(ctx, `
		SELECT id::text, profile_id::text, activated_by::text, activated_at, results
		FROM profile_activations ORDER BY activated_at DESC, id DESC LIMIT 1`).Scan(
		&value.ID, &value.ProfileID, &value.ActivatedBy, &value.ActivatedAt, &results)
	if errors.Is(err, sql.ErrNoRows) {
		return profile.Activation{}, false, nil
	}
	if err != nil {
		return profile.Activation{}, false, wrap("read latest profile activation", err)
	}
	if err := json.Unmarshal(results, &value.Results); err != nil {
		return profile.Activation{}, false, wrap("decode profile activation results", err)
	}
	return value, true, nil
}

func (r ProfileRepository) ActiveNode(ctx context.Context, nodeID string) (bool, error) {
	var active bool
	err := r.Store.q(ctx).QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM nodes WHERE id::text = $1 AND credential_state = 'active')`, nodeID).Scan(&active)
	return active, wrap("read profile node", err)
}

func (r ProfileRepository) CurrentAssignment(ctx context.Context, nodeID string) (assignment.Assignment, bool, error) {
	return r.Store.CurrentAssignment(ctx, nodeID)
}

func (r ProfileRepository) get(ctx context.Context, q queryer, id string) (profile.Profile, error) {
	value, err := scanProfile(q.QueryRowContext(ctx, `SELECT `+profileColumns+` FROM profiles p WHERE p.id::text = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return profile.Profile{}, profile.ErrNotFound
	}
	if err != nil {
		return profile.Profile{}, err
	}
	screens, err := r.screens(ctx, q, id)
	if err != nil {
		return profile.Profile{}, err
	}
	value.Screens = screens[id]
	if value.Screens == nil {
		value.Screens = []profile.Screen{}
	}
	return value, nil
}

// screens loads screens for one profile, or for every profile when id is empty.
func (r ProfileRepository) screens(ctx context.Context, q queryer, id string) (map[string][]profile.Screen, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT s.profile_id::text, s.node_id::text, COALESCE(s.media_id::text, ''), COALESCE(s.playlist_id::text, ''),
		       COALESCE(s.delivery_mode, ''), s.remote_input, s.show_qr, s.settings
		FROM profile_screens s JOIN nodes n ON n.id = s.node_id
		WHERE $1 = '' OR s.profile_id::text = $1
		ORDER BY s.profile_id, n.name, s.node_id`, id)
	if err != nil {
		return nil, wrap("list profile screens", err)
	}
	defer rows.Close()
	result := make(map[string][]profile.Screen)
	for rows.Next() {
		var profileID string
		var screen profile.Screen
		var settings []byte
		if err := rows.Scan(&profileID, &screen.NodeID, &screen.MediaID, &screen.PlaylistID, &screen.DeliveryMode, &screen.RemoteInput, &screen.ShowQR, &settings); err != nil {
			return nil, wrap("scan profile screen", err)
		}
		screen.Settings = settings
		result[profileID] = append(result[profileID], screen)
	}
	return result, wrap("iterate profile screens", rows.Err())
}

func insertScreens(ctx context.Context, tx *sql.Tx, profileID string, screens []profile.Screen) error {
	for _, screen := range screens {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO profile_screens (profile_id, node_id, media_id, playlist_id, delivery_mode, remote_input, show_qr, settings)
			VALUES ($1, $2::uuid, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, NULLIF($5, ''), $6, $7, $8)`,
			profileID, screen.NodeID, screen.MediaID, screen.PlaylistID, screen.DeliveryMode, screen.RemoteInput, screen.ShowQR, []byte(screen.Settings))
		switch postgresCode(err) {
		case "":
		case "23503", "22P02":
			return errors.Join(profile.ErrInvalid, errors.New("screen "+screen.NodeID+" references an unknown node, media, or playlist"))
		default:
			return wrap("insert profile screen", err)
		}
	}
	return nil
}

func profileWriteError(operation string, err error) error {
	if postgresCode(err) == "23505" {
		return profile.ErrNameConflict
	}
	return wrap(operation, err)
}

func scanProfile(row rowScanner) (profile.Profile, error) {
	var value profile.Profile
	var lastActivated sql.NullTime
	err := row.Scan(&value.ID, &value.Name, &value.Description, &value.Version, &value.CreatedBy, &value.UpdatedBy,
		&value.CreatedAt, &value.UpdatedAt, &lastActivated, &value.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return profile.Profile{}, err
	}
	if err != nil {
		return profile.Profile{}, wrap("scan profile", err)
	}
	if lastActivated.Valid {
		at := lastActivated.Time.UTC()
		value.LastActivatedAt = &at
	}
	value.CreatedAt = value.CreatedAt.UTC()
	value.UpdatedAt = value.UpdatedAt.UTC()
	return value, nil
}
