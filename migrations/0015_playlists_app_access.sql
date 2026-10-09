-- +cable Up
-- Cable programming (deployments, channels, blocks, program entries, the
-- hosted guide and its tokens) moved out of the core into the separate
-- Cuelight Cable app. Playlists stay as a core library resource, and any web
-- media item may opt in to app access: nodes mint per-node app tokens that let
-- the page read the media library and the screen's remote events.
DROP TABLE cable_assignment_conversions;
DROP TABLE cable_guide_tokens;

-- Auto-created "Cable · <name>" items are web media with no URL of their own.
-- Playlist items cannot point at them once removed; assignments and profile
-- screens follow their media foreign keys (SET NULL / CASCADE).
DELETE FROM cable_playlist_items i USING media m WHERE m.id = i.media_id AND m.deployment_id IS NOT NULL;
DELETE FROM media WHERE deployment_id IS NOT NULL;
ALTER TABLE media DROP CONSTRAINT media_kind_target;
ALTER TABLE media DROP COLUMN deployment_id;
ALTER TABLE media ADD CONSTRAINT media_kind_target CHECK (
  (kind = 'file' AND asset_id IS NOT NULL AND url IS NULL)
  OR (kind = 'web' AND asset_id IS NULL AND url IS NOT NULL));

DROP TABLE cable_program_entries;
DROP TABLE cable_blocks;
DROP TABLE cable_channels;
DROP TABLE cable_deployments;

ALTER TABLE cable_playlists RENAME TO playlists;
ALTER TABLE playlists RENAME CONSTRAINT cable_playlists_pkey TO playlists_pkey;
ALTER TABLE playlists RENAME CONSTRAINT cable_playlists_name_key TO playlists_name_key;
ALTER TABLE playlists RENAME CONSTRAINT cable_playlists_version_check TO playlists_version_check;
ALTER TABLE playlists RENAME CONSTRAINT cable_playlists_created_by_fkey TO playlists_created_by_fkey;
ALTER TABLE cable_playlist_items RENAME TO playlist_items;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_pkey TO playlist_items_pkey;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_playlist_id_position_key TO playlist_items_playlist_id_position_key;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_duration_seconds_check TO playlist_items_duration_seconds_check;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_position_check TO playlist_items_position_check;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_exactly_one_source TO playlist_items_exactly_one_source;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_asset_id_fkey TO playlist_items_asset_id_fkey;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_media_id_fkey TO playlist_items_media_id_fkey;
ALTER TABLE playlist_items RENAME CONSTRAINT cable_playlist_items_playlist_id_fkey TO playlist_items_playlist_id_fkey;

-- Playlist access is governed by the media scopes; Cable scopes are gone.
UPDATE admin_tokens SET scopes = array_remove(array_remove(scopes, 'cable_read'), 'cable_write')
WHERE scopes && ARRAY['cable_read', 'cable_write'];

ALTER TABLE media ADD COLUMN app_access boolean NOT NULL DEFAULT false;
ALTER TABLE media ADD CONSTRAINT media_app_access_web CHECK (NOT app_access OR kind = 'web');

-- An app token authorizes one node's view of one app-access web media item.
-- It is honored only while the node's current assignment targets that item.
CREATE TABLE app_tokens (
  token_hash bytea PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  media_id uuid NOT NULL REFERENCES media(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  CHECK (expires_at > created_at)
);
CREATE INDEX app_tokens_node_idx ON app_tokens (node_id);
CREATE INDEX app_tokens_media_idx ON app_tokens (media_id);
CREATE INDEX app_tokens_expiry_idx ON app_tokens (expires_at);

-- +cable Down
DROP TABLE app_tokens;
ALTER TABLE media DROP CONSTRAINT media_app_access_web;
ALTER TABLE media DROP COLUMN app_access;

ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_playlist_id_fkey TO cable_playlist_items_playlist_id_fkey;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_media_id_fkey TO cable_playlist_items_media_id_fkey;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_asset_id_fkey TO cable_playlist_items_asset_id_fkey;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_exactly_one_source TO cable_playlist_items_exactly_one_source;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_position_check TO cable_playlist_items_position_check;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_duration_seconds_check TO cable_playlist_items_duration_seconds_check;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_playlist_id_position_key TO cable_playlist_items_playlist_id_position_key;
ALTER TABLE playlist_items RENAME CONSTRAINT playlist_items_pkey TO cable_playlist_items_pkey;
ALTER TABLE playlist_items RENAME TO cable_playlist_items;
ALTER TABLE playlists RENAME CONSTRAINT playlists_created_by_fkey TO cable_playlists_created_by_fkey;
ALTER TABLE playlists RENAME CONSTRAINT playlists_version_check TO cable_playlists_version_check;
ALTER TABLE playlists RENAME CONSTRAINT playlists_name_key TO cable_playlists_name_key;
ALTER TABLE playlists RENAME CONSTRAINT playlists_pkey TO cable_playlists_pkey;
ALTER TABLE playlists RENAME TO cable_playlists;

CREATE TABLE cable_deployments (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  timezone text NOT NULL,
  theme text NOT NULL CHECK (theme IN ('nebula','gallery','noir')),
  guide_horizon_seconds bigint NOT NULL DEFAULT 86400 CHECK (guide_horizon_seconds > 0),
  guide_density text NOT NULL DEFAULT 'comfortable',
  placard_defaults jsonb NOT NULL DEFAULT '{}'::jsonb,
  fallback_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  updated_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE cable_channels (
  id uuid PRIMARY KEY,
  deployment_id uuid NOT NULL REFERENCES cable_deployments(id) ON DELETE CASCADE,
  number integer NOT NULL CHECK (number > 0),
  name text NOT NULL,
  callsign text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  accent text NOT NULL DEFAULT '',
  artwork_asset_id uuid REFERENCES assets(id),
  enabled boolean NOT NULL DEFAULT true,
  standby boolean NOT NULL DEFAULT false,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (deployment_id, number)
);
CREATE TABLE cable_blocks (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES cable_channels(id) ON DELETE CASCADE,
  playlist_id uuid REFERENCES cable_playlists(id) ON DELETE RESTRICT,
  title text NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  asset_id uuid REFERENCES assets(id) ON DELETE RESTRICT,
  CHECK (ends_at > starts_at),
  CONSTRAINT cable_blocks_exactly_one_source CHECK (num_nonnulls(playlist_id, asset_id) = 1),
  CONSTRAINT cable_blocks_channel_time_excl EXCLUDE USING gist (
    channel_id WITH =,
    tstzrange(starts_at, ends_at, '[)') WITH &&
  )
);
CREATE INDEX cable_blocks_channel_time_idx ON cable_blocks(channel_id, starts_at, id);
CREATE TABLE cable_program_entries (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES cable_channels(id) ON DELETE CASCADE,
  asset_id uuid NOT NULL REFERENCES assets(id),
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  title text NOT NULL DEFAULT '',
  subtitle text NOT NULL DEFAULT '',
  tag text NOT NULL DEFAULT '',
  artist text NOT NULL DEFAULT '',
  description text NOT NULL DEFAULT '',
  artwork_asset_id uuid REFERENCES assets(id),
  fit text NOT NULL DEFAULT 'contain',
  loop boolean NOT NULL DEFAULT false,
  mute boolean NOT NULL DEFAULT false,
  volume double precision NOT NULL DEFAULT 1 CHECK (volume >= 0 AND volume <= 1),
  placard_policy text NOT NULL DEFAULT 'default',
  availability_policy text NOT NULL DEFAULT 'fail',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at)
);
CREATE INDEX cable_program_entries_channel_time_idx ON cable_program_entries(channel_id, starts_at, id);

ALTER TABLE media DROP CONSTRAINT media_kind_target;
ALTER TABLE media ADD COLUMN deployment_id uuid UNIQUE REFERENCES cable_deployments(id) ON DELETE CASCADE;
ALTER TABLE media ADD CONSTRAINT media_kind_target CHECK (
  (kind = 'file' AND asset_id IS NOT NULL AND url IS NULL AND deployment_id IS NULL)
  OR (kind = 'web' AND asset_id IS NULL AND num_nonnulls(url, deployment_id) = 1));

CREATE TABLE cable_guide_tokens (
  token_hash bytea PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  deployment_id uuid NOT NULL REFERENCES cable_deployments(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  CHECK (expires_at > created_at)
);
CREATE INDEX cable_guide_tokens_node_idx ON cable_guide_tokens (node_id);
CREATE INDEX cable_guide_tokens_expiry_idx ON cable_guide_tokens (expires_at);
CREATE TABLE cable_assignment_conversions (
  node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  media_id uuid NOT NULL REFERENCES media(id) ON DELETE CASCADE,
  desired_generation bigint NOT NULL CHECK (desired_generation > 0),
  remote_input boolean NOT NULL,
  show_qr boolean NOT NULL,
  actor_id uuid NOT NULL REFERENCES actors(id)
);
