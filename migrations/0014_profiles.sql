-- +cable Up
CREATE TABLE profiles (
  id uuid PRIMARY KEY,
  name text NOT NULL UNIQUE CHECK (length(btrim(name)) > 0),
  description text NOT NULL DEFAULT '',
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  updated_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE profile_screens (
  profile_id uuid NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  media_id uuid REFERENCES media(id) ON DELETE CASCADE,
  playlist_id uuid REFERENCES cable_playlists(id) ON DELETE CASCADE,
  delivery_mode text CHECK (delivery_mode IN ('stream', 'cache_then_play')),
  remote_input boolean NOT NULL DEFAULT false,
  show_qr boolean NOT NULL DEFAULT false,
  -- Forward room for per-screen settings that are not assignment options.
  settings jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(settings) = 'object'),
  PRIMARY KEY (profile_id, node_id),
  CHECK ((media_id IS NULL) <> (playlist_id IS NULL))
);

CREATE INDEX profile_screens_node_idx ON profile_screens (node_id);

CREATE TABLE profile_activations (
  id uuid PRIMARY KEY,
  profile_id uuid NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
  activated_by uuid NOT NULL REFERENCES actors(id),
  activated_at timestamptz NOT NULL DEFAULT now(),
  results jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(results) = 'array')
);

CREATE INDEX profile_activations_latest_idx ON profile_activations (activated_at DESC, id DESC);
CREATE INDEX profile_activations_profile_idx ON profile_activations (profile_id, activated_at DESC);

-- +cable Down
DROP TABLE profile_activations;
DROP TABLE profile_screens;
DROP TABLE profiles;
