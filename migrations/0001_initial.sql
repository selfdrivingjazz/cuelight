-- +cuelight Up
CREATE TABLE actors (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE admin_tokens (
  id uuid PRIMARY KEY,
  actor_id uuid NOT NULL REFERENCES actors(id),
  prefix text NOT NULL,
  token_hash bytea NOT NULL,
  scopes text[] NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (prefix, token_hash)
);

CREATE TABLE idempotency_records (
  actor_id uuid NOT NULL REFERENCES actors(id),
  operation text NOT NULL,
  key text NOT NULL,
  request_hash bytea NOT NULL,
  response_status integer NOT NULL,
  response_body jsonb,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (actor_id, operation, key)
);

CREATE TABLE enrollment_tokens (
  id uuid PRIMARY KEY,
  token_hash bytea NOT NULL UNIQUE,
  node_id uuid,
  purpose text NOT NULL CHECK (purpose IN ('enroll', 'recover')),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  created_by uuid NOT NULL REFERENCES actors(id)
);

CREATE TABLE nodes (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  credential_state text NOT NULL CHECK (credential_state IN ('active', 'revoked', 'expired')),
  event_epoch uuid NOT NULL,
  desired_generation bigint NOT NULL DEFAULT 0 CHECK (desired_generation >= 0),
  observed_generation bigint NOT NULL DEFAULT 0 CHECK (observed_generation >= 0),
  created_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE enrollment_tokens ADD CONSTRAINT enrollment_tokens_node_fk FOREIGN KEY (node_id) REFERENCES nodes(id);

CREATE TABLE node_credentials (
  node_id uuid NOT NULL REFERENCES nodes(id),
  serial text NOT NULL,
  not_after timestamptz NOT NULL,
  revoked_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (node_id, serial)
);

CREATE TABLE node_recovery_tokens (
  token_id uuid PRIMARY KEY REFERENCES enrollment_tokens(id),
  node_id uuid NOT NULL REFERENCES nodes(id)
);

CREATE TABLE node_capability_reports (
  node_id uuid PRIMARY KEY REFERENCES nodes(id),
  report jsonb NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE assets (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE artifacts (
  digest text PRIMARY KEY CHECK (digest ~ '^[a-f0-9]{64}$'),
  size_bytes bigint NOT NULL CHECK (size_bytes > 0),
  media_type text NOT NULL,
  state text NOT NULL CHECK (state IN ('ready', 'quarantined', 'missing')),
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE asset_artifacts (
  asset_id uuid NOT NULL REFERENCES assets(id),
  artifact_digest text NOT NULL REFERENCES artifacts(digest),
  role text NOT NULL CHECK (role IN ('original')),
  PRIMARY KEY (asset_id, role)
);

CREATE TABLE uploads (
  id uuid PRIMARY KEY,
  name text NOT NULL,
  declared_length bigint NOT NULL CHECK (declared_length > 0),
  received_length bigint NOT NULL DEFAULT 0 CHECK (received_length >= 0),
  media_type text NOT NULL,
  state text NOT NULL CHECK (state IN ('open', 'finalizing', 'complete', 'failed')),
  artifact_digest text REFERENCES artifacts(digest),
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE upload_ranges (
  upload_id uuid NOT NULL REFERENCES uploads(id) ON DELETE CASCADE,
  start_offset bigint NOT NULL CHECK (start_offset >= 0),
  end_offset bigint NOT NULL CHECK (end_offset > start_offset),
  PRIMARY KEY (upload_id, start_offset)
);

CREATE TABLE releases (
  id uuid PRIMARY KEY,
  digest text NOT NULL UNIQUE CHECK (digest ~ '^[a-f0-9]{64}$'),
  key_id text NOT NULL,
  signature bytea NOT NULL,
  payload jsonb NOT NULL,
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (id, digest)
);

CREATE TABLE release_artifacts (
  release_id uuid NOT NULL REFERENCES releases(id),
  artifact_digest text NOT NULL REFERENCES artifacts(digest),
  PRIMARY KEY (release_id, artifact_digest)
);

CREATE TABLE assignments (
  id uuid PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  desired_generation bigint NOT NULL CHECK (desired_generation > 0),
  release_id uuid NOT NULL,
  release_digest text NOT NULL,
  delivery_mode text NOT NULL CHECK (delivery_mode IN ('stream', 'cache_then_play')),
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (node_id, desired_generation),
  FOREIGN KEY (release_id, release_digest) REFERENCES releases(id, digest)
);

CREATE TABLE node_report_snapshots (
  node_id uuid PRIMARY KEY REFERENCES nodes(id),
  event_epoch uuid NOT NULL,
  sequence bigint NOT NULL CHECK (sequence > 0),
  report jsonb NOT NULL,
  updated_at timestamptz NOT NULL
);

CREATE TABLE node_events (
  node_id uuid NOT NULL REFERENCES nodes(id),
  event_epoch uuid NOT NULL,
  sequence bigint NOT NULL CHECK (sequence > 0),
  event_id uuid NOT NULL,
  event jsonb NOT NULL,
  created_at timestamptz NOT NULL,
  PRIMARY KEY (node_id, event_epoch, sequence),
  UNIQUE (node_id, event_epoch, event_id)
);

CREATE TABLE node_release_authorizations (
  node_id uuid NOT NULL REFERENCES nodes(id),
  role text NOT NULL CHECK (role IN ('desired', 'candidate', 'rendering', 'healthy', 'lkg')),
  release_id uuid NOT NULL,
  release_digest text NOT NULL,
  updated_at timestamptz NOT NULL,
  PRIMARY KEY (node_id, role),
  FOREIGN KEY (release_id, release_digest) REFERENCES releases(id, digest)
);

CREATE TABLE audit_events (
  id uuid PRIMARY KEY,
  actor_id uuid REFERENCES actors(id),
  node_id uuid REFERENCES nodes(id),
  action text NOT NULL,
  reason text,
  input_hash bytea,
  result jsonb NOT NULL,
  correlation_id text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +cuelight Down
DROP TABLE audit_events;
DROP TABLE node_release_authorizations;
DROP TABLE node_events;
DROP TABLE node_report_snapshots;
DROP TABLE assignments;
DROP TABLE release_artifacts;
DROP TABLE releases;
DROP TABLE upload_ranges;
DROP TABLE uploads;
DROP TABLE asset_artifacts;
DROP TABLE artifacts;
DROP TABLE assets;
DROP TABLE node_capability_reports;
DROP TABLE node_recovery_tokens;
DROP TABLE node_credentials;
DROP TABLE enrollment_tokens;
DROP TABLE nodes;
DROP TABLE idempotency_records;
DROP TABLE admin_tokens;
DROP TABLE actors;
