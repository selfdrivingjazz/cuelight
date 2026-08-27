package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jmill/cable/internal/control/cable"
	"strings"
	"time"
)

var _ cable.Repository = (*Store)(nil)
var _ cable.ArtifactRepository = (*Store)(nil)
var _ cable.ArtifactByDigestRepository = (*Store)(nil)

func ct(m cable.Mutation) time.Time {
	if m.Now.IsZero() {
		return time.Now().UTC()
	}
	return m.Now.UTC()
}
func normalizeDeploymentInput(in cable.DeploymentInput) (cable.DeploymentInput, error) {
	var err error
	in.Theme, err = cable.NormalizeTheme(in.Theme)
	if err != nil {
		return cable.DeploymentInput{}, err
	}
	in.Timezone, err = cable.NormalizeTimezone(in.Timezone)
	if err != nil {
		return cable.DeploymentInput{}, err
	}
	if in.GuideHorizonSeconds == 0 {
		in.GuideHorizonSeconds = 24 * 60 * 60
	}
	if in.GuideHorizonSeconds < 60*60 || in.GuideHorizonSeconds > 31*24*60*60 {
		return cable.DeploymentInput{}, &cable.ValidationError{Field: "guideHorizonSeconds", Reason: "must be between one hour and 31 days"}
	}
	return in, nil
}

func normalizeProgramInput(in cable.ProgramInput) (cable.ProgramInput, error) {
	var err error
	if in.PlacardPolicy == "" {
		in.PlacardPolicy = "default"
	}
	in.PlacardPolicy, err = cable.NormalizePlacardPolicy(in.PlacardPolicy)
	if err != nil {
		return cable.ProgramInput{}, err
	}
	if in.AvailabilityPolicy == "" {
		in.AvailabilityPolicy = "fail"
	}
	in.AvailabilityPolicy, err = cable.NormalizeAvailabilityPolicy(in.AvailabilityPolicy)
	if err != nil {
		return cable.ProgramInput{}, err
	}
	return in, nil
}

func validCableDigest(digest string) bool {
	if len(digest) != 64 {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil && digest == strings.ToLower(digest)
}

func cl(n int) int {
	if n <= 0 {
		return 25
	}
	if n > 100 {
		return 100
	}
	return n
}
func cj(v any) ([]byte, error) { return json.Marshal(v) }
func dj(b []byte, v any) {
	if len(b) > 0 {
		_ = json.Unmarshal(b, v)
	}
}
func ce(op string, e error) error {
	if e == nil {
		return nil
	}
	if errors.Is(e, sql.ErrNoRows) {
		return cable.ErrNotFound
	}
	return fmt.Errorf("PostgreSQL %s: %w", op, e)
}
func pgConflict(e error) bool { return postgresCode(e) == "23505" }
func (s *Store) CreateDeployment(ctx context.Context, m cable.Mutation, in cable.DeploymentInput) (cable.CableDeployment, error) {
	if m.ActorID == "" || in.Name == "" {
		return cable.CableDeployment{}, cable.ErrInvalidArgument
	}
	var err error
	if in, err = normalizeDeploymentInput(in); err != nil {
		return cable.CableDeployment{}, fmt.Errorf("%w: %v", cable.ErrInvalidArgument, err)
	}
	id, e := s.newUUID()
	if e != nil {
		return cable.CableDeployment{}, e
	}
	p, _ := cj(in.PlacardDefaults)
	f, _ := cj(in.FallbackMetadata)
	_, e = s.q(ctx).ExecContext(ctx, `INSERT INTO cable_deployments(id,name,timezone,theme,guide_horizon_seconds,guide_density,placard_defaults,fallback_metadata,version,created_by,updated_by,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,$9,$9,$10,$10)`, id, in.Name, in.Timezone, in.Theme, in.GuideHorizonSeconds, in.GuideDensity, p, f, m.ActorID, ct(m))
	if e != nil {
		return cable.CableDeployment{}, ce("create cable deployment", e)
	}
	return s.Deployment(ctx, id)
}
func (s *Store) Deployment(ctx context.Context, id string) (cable.CableDeployment, error) {
	var d cable.CableDeployment
	var p, f []byte
	e := s.q(ctx).QueryRowContext(ctx, `SELECT id,name,timezone,theme,guide_horizon_seconds,guide_density,placard_defaults,fallback_metadata,version,created_by,updated_by,created_at,updated_at FROM cable_deployments WHERE id=$1`, id).Scan(&d.ID, &d.Name, &d.Timezone, &d.Theme, &d.GuideHorizonSeconds, &d.GuideDensity, &p, &f, &d.Version, &d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt)
	if e != nil {
		return d, ce("get cable deployment", e)
	}
	dj(p, &d.PlacardDefaults)
	dj(f, &d.FallbackMetadata)
	return d, nil
}
func (s *Store) UpdateDeployment(ctx context.Context, m cable.Mutation, id string, in cable.DeploymentInput) (cable.CableDeployment, error) {
	if m.ExpectedVersion <= 0 {
		return cable.CableDeployment{}, cable.ErrConflict
	}
	if in.Name == "" {
		return cable.CableDeployment{}, cable.ErrInvalidArgument
	}
	var err error
	if in, err = normalizeDeploymentInput(in); err != nil {
		return cable.CableDeployment{}, fmt.Errorf("%w: %v", cable.ErrInvalidArgument, err)
	}
	p, _ := cj(in.PlacardDefaults)
	f, _ := cj(in.FallbackMetadata)
	r, e := s.q(ctx).ExecContext(ctx, `UPDATE cable_deployments SET name=$2,timezone=$3,theme=$4,guide_horizon_seconds=$5,guide_density=$6,placard_defaults=$7,fallback_metadata=$8,version=version+1,updated_by=$9,updated_at=$10 WHERE id=$1 AND version=$11`, id, in.Name, in.Timezone, in.Theme, in.GuideHorizonSeconds, in.GuideDensity, p, f, m.ActorID, ct(m), m.ExpectedVersion)
	if e != nil {
		return cable.CableDeployment{}, ce("update cable deployment", e)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return cable.CableDeployment{}, cable.ErrConflict
	}
	return s.Deployment(ctx, id)
}
func (s *Store) ListDeployments(ctx context.Context, cursor string, limit int) (cable.DeploymentPage, error) {
	limit = cl(limit)
	q := `SELECT id,name,timezone,theme,guide_horizon_seconds,guide_density,placard_defaults,fallback_metadata,version,created_by,updated_by,created_at,updated_at FROM cable_deployments`
	args := []any{}
	if cursor != "" {
		q += ` WHERE id>$1`
		args = append(args, cursor)
	}
	q += fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, e := s.q(ctx).QueryContext(ctx, q, args...)
	if e != nil {
		return cable.DeploymentPage{}, ce("list cable deployments", e)
	}
	defer rows.Close()
	out := cable.DeploymentPage{Items: []cable.CableDeployment{}}
	for rows.Next() {
		var d cable.CableDeployment
		var p, f []byte
		if e = rows.Scan(&d.ID, &d.Name, &d.Timezone, &d.Theme, &d.GuideHorizonSeconds, &d.GuideDensity, &p, &f, &d.Version, &d.CreatedBy, &d.UpdatedBy, &d.CreatedAt, &d.UpdatedAt); e != nil {
			return out, e
		}
		dj(p, &d.PlacardDefaults)
		dj(f, &d.FallbackMetadata)
		out.Items = append(out.Items, d)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Page.HasMore = true
		out.Page.NextCursor = out.Items[limit-1].ID
	}
	return out, rows.Err()
}
func (s *Store) DeleteDeployment(ctx context.Context, m cable.Mutation, id string) error {
	r, e := s.q(ctx).ExecContext(ctx, `DELETE FROM cable_deployments WHERE id=$1 AND version=$2`, id, m.ExpectedVersion)
	if e != nil {
		return ce("delete cable deployment", e)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return cable.ErrConflict
	}
	return nil
}

func (s *Store) CreateChannel(ctx context.Context, m cable.Mutation, in cable.ChannelInput) (cable.Channel, error) {
	if m.ActorID == "" || in.DeploymentID == "" || in.Number <= 0 {
		return cable.Channel{}, cable.ErrInvalidArgument
	}
	id, e := s.newUUID()
	if e != nil {
		return cable.Channel{}, e
	}
	_, e = s.q(ctx).ExecContext(ctx, `INSERT INTO cable_channels(id,deployment_id,number,name,callsign,description,accent,artwork_asset_id,enabled,standby,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,'')::uuid,$9,$10,1,$11,$11)`, id, in.DeploymentID, in.Number, in.Name, in.Callsign, in.Description, in.Accent, in.ArtworkAssetID, in.Enabled, in.Standby, ct(m))
	if e != nil {
		if pgConflict(e) {
			return cable.Channel{}, cable.ErrConflict
		}
		return cable.Channel{}, ce("create channel", e)
	}
	return s.Channel(ctx, id)
}
func (s *Store) Channel(ctx context.Context, id string) (cable.Channel, error) {
	var v cable.Channel
	e := s.q(ctx).QueryRowContext(ctx, `SELECT id,deployment_id,number,name,callsign,description,accent,COALESCE(artwork_asset_id::text,''),enabled,standby,version,created_at,updated_at FROM cable_channels WHERE id=$1`, id).Scan(&v.ID, &v.DeploymentID, &v.Number, &v.Name, &v.Callsign, &v.Description, &v.Accent, &v.ArtworkAssetID, &v.Enabled, &v.Standby, &v.Version, &v.CreatedAt, &v.UpdatedAt)
	if e != nil {
		return v, ce("get channel", e)
	}
	return v, nil
}
func (s *Store) UpdateChannel(ctx context.Context, m cable.Mutation, id string, in cable.ChannelInput) (cable.Channel, error) {
	if m.ExpectedVersion <= 0 || in.Number <= 0 {
		return cable.Channel{}, cable.ErrInvalidArgument
	}
	r, e := s.q(ctx).ExecContext(ctx, `UPDATE cable_channels SET number=$2,name=$3,callsign=$4,description=$5,accent=$6,artwork_asset_id=NULLIF($7,'')::uuid,enabled=$8,standby=$9,version=version+1,updated_at=$10 WHERE id=$1 AND version=$11`, id, in.Number, in.Name, in.Callsign, in.Description, in.Accent, in.ArtworkAssetID, in.Enabled, in.Standby, ct(m), m.ExpectedVersion)
	if e != nil {
		if pgConflict(e) {
			return cable.Channel{}, cable.ErrConflict
		}
		return cable.Channel{}, ce("update channel", e)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return cable.Channel{}, cable.ErrConflict
	}
	return s.Channel(ctx, id)
}
func (s *Store) ListChannels(ctx context.Context, did, cursor string, limit int) (cable.ChannelPage, error) {
	limit = cl(limit)
	q := `SELECT id,deployment_id,number,name,callsign,description,accent,COALESCE(artwork_asset_id::text,''),enabled,standby,version,created_at,updated_at FROM cable_channels WHERE deployment_id=$1`
	args := []any{did}
	if cursor != "" {
		q += ` AND (number,id) > (SELECT number,id FROM cable_channels WHERE id=$2)`
		args = append(args, cursor)
	}
	q += fmt.Sprintf(` ORDER BY number,id LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, e := s.q(ctx).QueryContext(ctx, q, args...)
	if e != nil {
		return cable.ChannelPage{}, ce("list channels", e)
	}
	defer rows.Close()
	out := cable.ChannelPage{Items: []cable.Channel{}}
	for rows.Next() {
		var v cable.Channel
		if e = rows.Scan(&v.ID, &v.DeploymentID, &v.Number, &v.Name, &v.Callsign, &v.Description, &v.Accent, &v.ArtworkAssetID, &v.Enabled, &v.Standby, &v.Version, &v.CreatedAt, &v.UpdatedAt); e != nil {
			return out, e
		}
		out.Items = append(out.Items, v)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Page.HasMore = true
		out.Page.NextCursor = out.Items[limit-1].ID
	}
	return out, rows.Err()
}
func (s *Store) DeleteChannel(ctx context.Context, m cable.Mutation, id string) error {
	r, e := s.q(ctx).ExecContext(ctx, `DELETE FROM cable_channels WHERE id=$1 AND version=$2`, id, m.ExpectedVersion)
	if e != nil {
		return ce("delete channel", e)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return cable.ErrConflict
	}
	return nil
}

func (s *Store) CreateProgramEntry(ctx context.Context, m cable.Mutation, in cable.ProgramInput) (cable.ProgramEntry, error) {
	if m.ActorID == "" {
		return cable.ProgramEntry{}, cable.ErrInvalidArgument
	}
	var err error
	if in, err = normalizeProgramInput(in); err != nil || cable.ValidateProgram(in) != nil {
		if err != nil {
			return cable.ProgramEntry{}, fmt.Errorf("%w: %v", cable.ErrInvalidArgument, err)
		}
		return cable.ProgramEntry{}, cable.ErrInvalidArgument
	}
	var overlap bool
	if e := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cable_program_entries WHERE channel_id=$1 AND starts_at<$3 AND ends_at>$2)`, in.ChannelID, in.StartsAt.UTC(), in.EndsAt.UTC()).Scan(&overlap); e != nil {
		return cable.ProgramEntry{}, ce("validate program overlap", e)
	}
	if overlap {
		return cable.ProgramEntry{}, cable.ErrOverlap
	}
	id, e := s.newUUID()
	if e != nil {
		return cable.ProgramEntry{}, e
	}
	_, e = s.q(ctx).ExecContext(ctx, `INSERT INTO cable_program_entries(id,channel_id,asset_id,starts_at,ends_at,title,subtitle,tag,artist,description,artwork_asset_id,fit,loop,mute,volume,placard_policy,availability_policy,version,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,NULLIF($11,'')::uuid,$12,$13,$14,$15,$16,$17,1,$18,$18)`, id, in.ChannelID, in.AssetID, in.StartsAt.UTC(), in.EndsAt.UTC(), in.Title, in.Subtitle, in.Tag, in.Artist, in.Description, in.ArtworkAssetID, in.Fit, in.Loop, in.Mute, in.Volume, in.PlacardPolicy, in.AvailabilityPolicy, ct(m))
	if e != nil {
		return cable.ProgramEntry{}, ce("create program entry", e)
	}
	return s.ProgramEntry(ctx, id)
}
func (s *Store) ProgramEntry(ctx context.Context, id string) (cable.ProgramEntry, error) {
	var p cable.ProgramEntry
	e := s.q(ctx).QueryRowContext(ctx, `SELECT id,channel_id,asset_id,starts_at,ends_at,title,subtitle,tag,artist,description,COALESCE(artwork_asset_id::text,''),fit,loop,mute,volume,placard_policy,availability_policy,version,created_at,updated_at FROM cable_program_entries WHERE id=$1`, id).Scan(&p.ID, &p.ChannelID, &p.AssetID, &p.StartsAt, &p.EndsAt, &p.Title, &p.Subtitle, &p.Tag, &p.Artist, &p.Description, &p.ArtworkAssetID, &p.Fit, &p.Loop, &p.Mute, &p.Volume, &p.PlacardPolicy, &p.AvailabilityPolicy, &p.Version, &p.CreatedAt, &p.UpdatedAt)
	if e != nil {
		return p, ce("get program entry", e)
	}
	return p, nil
}
func (s *Store) UpdateProgramEntry(ctx context.Context, m cable.Mutation, id string, in cable.ProgramInput) (cable.ProgramEntry, error) {
	if m.ExpectedVersion <= 0 {
		return cable.ProgramEntry{}, cable.ErrInvalidArgument
	}
	var err error
	if in, err = normalizeProgramInput(in); err != nil || cable.ValidateProgram(in) != nil {
		if err != nil {
			return cable.ProgramEntry{}, fmt.Errorf("%w: %v", cable.ErrInvalidArgument, err)
		}
		return cable.ProgramEntry{}, cable.ErrInvalidArgument
	}
	var overlap bool
	if e := s.q(ctx).QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM cable_program_entries WHERE channel_id=$1 AND id<>$4 AND starts_at<$3 AND ends_at>$2)`, in.ChannelID, in.StartsAt.UTC(), in.EndsAt.UTC(), id).Scan(&overlap); e != nil {
		return cable.ProgramEntry{}, ce("validate program overlap", e)
	}
	if overlap {
		return cable.ProgramEntry{}, cable.ErrOverlap
	}
	r, e := s.q(ctx).ExecContext(ctx, `UPDATE cable_program_entries SET asset_id=$2,starts_at=$3,ends_at=$4,title=$5,subtitle=$6,tag=$7,artist=$8,description=$9,artwork_asset_id=NULLIF($10,'')::uuid,fit=$11,loop=$12,mute=$13,volume=$14,placard_policy=$15,availability_policy=$16,version=version+1,updated_at=$17 WHERE id=$1 AND version=$18`, id, in.AssetID, in.StartsAt.UTC(), in.EndsAt.UTC(), in.Title, in.Subtitle, in.Tag, in.Artist, in.Description, in.ArtworkAssetID, in.Fit, in.Loop, in.Mute, in.Volume, in.PlacardPolicy, in.AvailabilityPolicy, ct(m), m.ExpectedVersion)
	if e != nil {
		return cable.ProgramEntry{}, ce("update program entry", e)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return cable.ProgramEntry{}, cable.ErrConflict
	}
	return s.ProgramEntry(ctx, id)
}
func (s *Store) ListProgramEntries(ctx context.Context, cid, cursor string, limit int) (cable.ProgramPage, error) {
	limit = cl(limit)
	q := `SELECT id,channel_id,asset_id,starts_at,ends_at,title,subtitle,tag,artist,description,COALESCE(artwork_asset_id::text,''),fit,loop,mute,volume,placard_policy,availability_policy,version,created_at,updated_at FROM cable_program_entries WHERE channel_id=$1`
	args := []any{cid}
	if cursor != "" {
		q += ` AND (starts_at,id) > (SELECT starts_at,id FROM cable_program_entries WHERE id=$2)`
		args = append(args, cursor)
	}
	q += fmt.Sprintf(` ORDER BY starts_at,id LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, e := s.q(ctx).QueryContext(ctx, q, args...)
	if e != nil {
		return cable.ProgramPage{}, ce("list program entries", e)
	}
	defer rows.Close()
	out := cable.ProgramPage{Items: []cable.ProgramEntry{}}
	for rows.Next() {
		var p cable.ProgramEntry
		if e = rows.Scan(&p.ID, &p.ChannelID, &p.AssetID, &p.StartsAt, &p.EndsAt, &p.Title, &p.Subtitle, &p.Tag, &p.Artist, &p.Description, &p.ArtworkAssetID, &p.Fit, &p.Loop, &p.Mute, &p.Volume, &p.PlacardPolicy, &p.AvailabilityPolicy, &p.Version, &p.CreatedAt, &p.UpdatedAt); e != nil {
			return out, e
		}
		out.Items = append(out.Items, p)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Page.HasMore = true
		out.Page.NextCursor = out.Items[limit-1].ID
	}
	return out, rows.Err()
}
func (s *Store) DeleteProgramEntry(ctx context.Context, m cable.Mutation, id string) error {
	r, e := s.q(ctx).ExecContext(ctx, `DELETE FROM cable_program_entries WHERE id=$1 AND version=$2`, id, m.ExpectedVersion)
	if e != nil {
		return ce("delete program entry", e)
	}
	n, _ := r.RowsAffected()
	if n != 1 {
		return cable.ErrConflict
	}
	return nil
}

func (s *Store) RegisterShellBuild(ctx context.Context, m cable.Mutation, in cable.ShellBuildInput) (cable.ShellBuild, error) {
	if m.ActorID == "" || !validCableDigest(strings.TrimSpace(in.Digest)) {
		return cable.ShellBuild{}, cable.ErrInvalidArgument
	}
	var err error
	in.Digest = strings.TrimSpace(in.Digest)
	if in.Entrypoint, err = cable.NormalizeEntrypoint(in.Entrypoint); err != nil {
		return cable.ShellBuild{}, fmt.Errorf("%w: %v", cable.ErrInvalidArgument, err)
	}
	in.BridgeProtocol = strings.TrimSpace(in.BridgeProtocol)
	if in.BridgeProtocol == "" {
		return cable.ShellBuild{}, cable.ErrInvalidArgument
	}
	seen := map[string]bool{}
	for i, digest := range in.ArtifactDigests {
		digest = strings.TrimSpace(digest)
		if !validCableDigest(digest) {
			return cable.ShellBuild{}, cable.ErrInvalidArgument
		}
		if seen[digest] {
			return cable.ShellBuild{}, cable.ErrInvalidArgument
		}
		seen[digest] = true
		in.ArtifactDigests[i] = digest
	}
	if !seen[in.Digest] {
		return cable.ShellBuild{}, cable.ErrInvalidArgument
	}
	meta, _ := cj(in.VersionMetadata)
	id, e := s.newUUID()
	if e != nil {
		return cable.ShellBuild{}, e
	}
	_, e = s.q(ctx).ExecContext(ctx, `INSERT INTO cable_shell_builds(id,digest,entrypoint,version_metadata,bridge_protocol,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, in.Digest, in.Entrypoint, meta, in.BridgeProtocol, m.ActorID, ct(m))
	if e != nil {
		if pgConflict(e) {
			return cable.ShellBuild{}, cable.ErrConflict
		}
		return cable.ShellBuild{}, ce("register shell build", e)
	}
	for _, d := range in.ArtifactDigests {
		if _, e = s.q(ctx).ExecContext(ctx, `INSERT INTO cable_shell_build_artifacts(shell_build_id,artifact_digest) VALUES($1,$2)`, id, d); e != nil {
			return cable.ShellBuild{}, ce("bind shell build artifact", e)
		}
	}
	return s.ShellBuild(ctx, id)
}
func (s *Store) ShellBuild(ctx context.Context, id string) (cable.ShellBuild, error) {
	var b cable.ShellBuild
	var meta []byte
	e := s.q(ctx).QueryRowContext(ctx, `SELECT id,digest,entrypoint,version_metadata,bridge_protocol,created_by,created_at FROM cable_shell_builds WHERE id=$1`, id).Scan(&b.ID, &b.Digest, &b.Entrypoint, &meta, &b.BridgeProtocol, &b.CreatedBy, &b.CreatedAt)
	if e != nil {
		return b, ce("get shell build", e)
	}
	dj(meta, &b.VersionMetadata)
	rows, e := s.q(ctx).QueryContext(ctx, `SELECT artifact_digest FROM cable_shell_build_artifacts WHERE shell_build_id=$1 ORDER BY artifact_digest`, id)
	if e != nil {
		return b, ce("list shell build artifacts", e)
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if e = rows.Scan(&d); e != nil {
			return b, e
		}
		b.ArtifactDigests = append(b.ArtifactDigests, d)
	}
	return b, rows.Err()
}
func (s *Store) ListShellBuilds(ctx context.Context, cursor string, limit int) (cable.ShellBuildPage, error) {
	limit = cl(limit)
	q := `SELECT id FROM cable_shell_builds`
	args := []any{}
	if cursor != "" {
		q += ` WHERE id>$1`
		args = append(args, cursor)
	}
	q += fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, e := s.q(ctx).QueryContext(ctx, q, args...)
	if e != nil {
		return cable.ShellBuildPage{}, ce("list shell builds", e)
	}
	defer rows.Close()
	out := cable.ShellBuildPage{Items: []cable.ShellBuild{}}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return out, e
		}
		b, e := s.ShellBuild(ctx, id)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, b)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Page.HasMore = true
		out.Page.NextCursor = out.Items[limit-1].ID
	}
	return out, rows.Err()
}
func (s *Store) CreateRevision(ctx context.Context, r cable.CableRevision) error {
	if !validCableDigest(r.Digest) || len(r.JSON) == 0 || !validCableDigest(r.ShellBuildDigest) || r.Theme == "" || r.Timezone == "" || r.GeneratedAt.IsZero() {
		return cable.ErrInvalidArgument
	}
	if _, err := cable.NormalizeTheme(r.Theme); err != nil {
		return cable.ErrInvalidArgument
	}
	if _, err := cable.NormalizeTimezone(r.Timezone); err != nil {
		return cable.ErrInvalidArgument
	}
	if _, err := cable.NormalizeEntrypoint(r.Entrypoint); err != nil {
		return cable.ErrInvalidArgument
	}
	if _, err := cable.NormalizeWindow(r.Window); err != nil {
		return cable.ErrInvalidArgument
	}
	foundShell := false
	for _, digest := range r.ArtifactDigests {
		if digest == r.ShellBuildDigest {
			foundShell = true
			break
		}
	}
	if !foundShell {
		return cable.ErrInvalidArgument
	}
	if r.ID == "" {
		r.ID = r.Digest
	}
	errorItems := r.Errors
	if errorItems == nil {
		errorItems = []string{}
	}
	warningItems := r.Warnings
	if warningItems == nil {
		warningItems = []string{}
	}
	_, e := s.q(ctx).ExecContext(ctx, `INSERT INTO cable_revisions(id,deployment_id,deployment_version,shell_build_id,digest,shell_build_digest,entrypoint,bridge_protocol,timezone,theme,generated_at,window_start,window_end,document,artifact_digests,errors,warnings,created_by,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`, r.ID, r.DeploymentID, r.DeploymentVersion, r.ShellBuildID, r.Digest, r.ShellBuildDigest, r.Entrypoint, r.BridgeProtocol, r.Timezone, r.Theme, r.GeneratedAt.UTC(), r.Window.StartsAt.UTC(), r.Window.EndsAt.UTC(), r.JSON, r.ArtifactDigests, errorItems, warningItems, r.CreatedBy, r.CreatedAt.UTC())
	if e != nil {
		if pgConflict(e) {
			return cable.ErrConflict
		}
		return ce("create cable revision", e)
	}
	return nil
}
func (s *Store) Revision(ctx context.Context, id string) (cable.CableRevision, error) {
	var r cable.CableRevision
	var document, artifactDigests, errorItems, warningItems []byte
	e := s.q(ctx).QueryRowContext(ctx, `SELECT id,deployment_id,deployment_version,shell_build_id,digest,shell_build_digest,entrypoint,bridge_protocol,timezone,theme,generated_at,window_start,window_end,document,array_to_json(artifact_digests),array_to_json(errors),array_to_json(warnings),created_by,created_at FROM cable_revisions WHERE id=$1`, id).Scan(&r.ID, &r.DeploymentID, &r.DeploymentVersion, &r.ShellBuildID, &r.Digest, &r.ShellBuildDigest, &r.Entrypoint, &r.BridgeProtocol, &r.Timezone, &r.Theme, &r.GeneratedAt, &r.Window.StartsAt, &r.Window.EndsAt, &document, &artifactDigests, &errorItems, &warningItems, &r.CreatedBy, &r.CreatedAt)
	if e != nil {
		return r, ce("get cable revision", e)
	}
	for name, value := range map[string]struct {
		data   []byte
		target *[]string
	}{
		"artifact digests": {artifactDigests, &r.ArtifactDigests},
		"errors":           {errorItems, &r.Errors},
		"warnings":         {warningItems, &r.Warnings},
	} {
		if err := json.Unmarshal(value.data, value.target); err != nil {
			return r, fmt.Errorf("decode cable revision %s: %w", name, err)
		}
	}
	r.JSON = document
	return r, nil
}
func (s *Store) ListRevisions(ctx context.Context, did, cursor string, limit int) (cable.RevisionPage, error) {
	limit = cl(limit)
	q := `SELECT id FROM cable_revisions WHERE deployment_id=$1`
	args := []any{did}
	if cursor != "" {
		q += ` AND id>$2`
		args = append(args, cursor)
	}
	q += fmt.Sprintf(` ORDER BY id LIMIT $%d`, len(args)+1)
	args = append(args, limit+1)
	rows, e := s.q(ctx).QueryContext(ctx, q, args...)
	if e != nil {
		return cable.RevisionPage{}, ce("list cable revisions", e)
	}
	defer rows.Close()
	out := cable.RevisionPage{Items: []cable.CableRevision{}}
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			return out, e
		}
		r, e := s.Revision(ctx, id)
		if e != nil {
			return out, e
		}
		out.Items = append(out.Items, r)
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		out.Page.HasMore = true
		out.Page.NextCursor = out.Items[limit-1].ID
	}
	return out, rows.Err()
}
func (s *Store) CableArtifactsForAsset(ctx context.Context, id string) ([]cable.Artifact, error) {
	rows, e := s.q(ctx).QueryContext(ctx, `SELECT aa.asset_id,a.digest,a.size_bytes,a.media_type,a.state FROM asset_artifacts aa JOIN artifacts a ON a.digest=aa.artifact_digest WHERE aa.asset_id=$1 ORDER BY a.digest`, id)
	if e != nil {
		return nil, ce("list cable asset artifacts", e)
	}
	defer rows.Close()
	out := []cable.Artifact{}
	for rows.Next() {
		var a cable.Artifact
		if e = rows.Scan(&a.AssetID, &a.Digest, &a.Size, &a.MediaType, &a.State); e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
func (s *Store) CableArtifactByDigest(ctx context.Context, digest string) (cable.Artifact, error) {
	var a cable.Artifact
	e := s.q(ctx).QueryRowContext(ctx, `SELECT digest,size_bytes,media_type,state FROM artifacts WHERE digest=$1`, digest).Scan(&a.Digest, &a.Size, &a.MediaType, &a.State)
	if e != nil {
		return a, ce("get cable artifact", e)
	}
	return a, nil
}

// RegisterCableRevisionArtifact records the already-written immutable revision
// document in the artifact catalog. The CAS write itself is deliberately owned
// by the composition adapter.
func (s *Store) RegisterCableRevisionArtifact(ctx context.Context, digest string, size int64) error {
	if !validCableDigest(digest) || size <= 0 {
		return cable.ErrInvalidArgument
	}
	_, err := s.q(ctx).ExecContext(ctx, `INSERT INTO artifacts(digest,size_bytes,media_type,state) VALUES($1,$2,'application/vnd.cable.revision+json','ready') ON CONFLICT(digest) DO UPDATE SET size_bytes=EXCLUDED.size_bytes,media_type=EXCLUDED.media_type,state='ready'`, digest, size)
	if err != nil {
		return ce("register cable revision artifact", err)
	}
	return nil
}
