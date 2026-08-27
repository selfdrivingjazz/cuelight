-- +cable Up
CREATE TABLE cable_deployments (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  timezone text NOT NULL,
  theme text NOT NULL CHECK (theme IN ('nebula','gallery','noir')),
  guide_horizon interval NOT NULL DEFAULT interval '24 hours',
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
CREATE TABLE cable_shell_builds (
  id uuid PRIMARY KEY,
  digest text NOT NULL UNIQUE CHECK (digest ~ '^[a-f0-9]{64}$'),
  entrypoint text NOT NULL,
  version_metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
  bridge_protocol text NOT NULL,
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE cable_shell_build_artifacts (
  shell_build_id uuid NOT NULL REFERENCES cable_shell_builds(id) ON DELETE CASCADE,
  artifact_digest text NOT NULL REFERENCES artifacts(digest),
  PRIMARY KEY (shell_build_id, artifact_digest)
);
CREATE TABLE cable_revisions (
  id uuid PRIMARY KEY,
  deployment_id uuid NOT NULL REFERENCES cable_deployments(id) ON DELETE RESTRICT,
  deployment_version bigint NOT NULL,
  shell_build_id uuid NOT NULL REFERENCES cable_shell_builds(id),
  digest text NOT NULL UNIQUE CHECK (digest ~ '^[a-f0-9]{64}$'),
  shell_build_digest text NOT NULL CHECK (shell_build_digest ~ '^[a-f0-9]{64}$'),
  entrypoint text NOT NULL,
  bridge_protocol text NOT NULL,
  timezone text NOT NULL,
  window_start timestamptz NOT NULL,
  window_end timestamptz NOT NULL,
  document jsonb NOT NULL,
  artifact_digests text[] NOT NULL,
  errors text[] NOT NULL DEFAULT '{}',
  warnings text[] NOT NULL DEFAULT '{}',
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  CHECK (window_end > window_start)
);
CREATE INDEX cable_revisions_deployment_idx ON cable_revisions(deployment_id, id);

-- +cable Down
DROP INDEX cable_revisions_deployment_idx;
DROP TABLE cable_revisions;
DROP TABLE cable_shell_build_artifacts;
DROP TABLE cable_shell_builds;
DROP INDEX cable_program_entries_channel_time_idx;
DROP TABLE cable_program_entries;
DROP TABLE cable_channels;
DROP TABLE cable_deployments;
