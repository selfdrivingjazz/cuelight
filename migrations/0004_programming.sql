-- +cable Up
CREATE TABLE cable_playlists (
  id uuid PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE cable_playlist_items (
  id uuid PRIMARY KEY,
  playlist_id uuid NOT NULL REFERENCES cable_playlists(id) ON DELETE CASCADE,
  asset_id uuid NOT NULL REFERENCES assets(id),
  position integer NOT NULL CHECK (position > 0),
  duration_seconds integer NOT NULL CHECK (duration_seconds > 0),
  title text NOT NULL DEFAULT '',
  subtitle text NOT NULL DEFAULT '',
  UNIQUE (playlist_id, position)
);
CREATE TABLE cable_blocks (
  id uuid PRIMARY KEY,
  channel_id uuid NOT NULL REFERENCES cable_channels(id) ON DELETE CASCADE,
  playlist_id uuid NOT NULL REFERENCES cable_playlists(id) ON DELETE RESTRICT,
  title text NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at timestamptz NOT NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at)
);
CREATE INDEX cable_blocks_channel_time_idx ON cable_blocks(channel_id, starts_at, id);
CREATE TABLE cable_profiles (
  id uuid PRIMARY KEY,
  name text NOT NULL UNIQUE,
  description text NOT NULL DEFAULT '',
  deployment_id uuid NOT NULL REFERENCES cable_deployments(id) ON DELETE CASCADE,
  shell_build_id uuid NOT NULL REFERENCES cable_shell_builds(id) ON DELETE RESTRICT,
  release_id uuid REFERENCES releases(id) ON DELETE SET NULL,
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +cable Down
DROP TABLE cable_profiles;
DROP INDEX cable_blocks_channel_time_idx;
DROP TABLE cable_blocks;
DROP TABLE cable_playlist_items;
DROP TABLE cable_playlists;
