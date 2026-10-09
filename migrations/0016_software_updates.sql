-- +cuelight Up
-- Vendor-signed Cuelight software releases mirrored by control, and the
-- version an operator wants each node to run. Release envelopes are verified
-- against the compiled-in vendor keys before they are stored; artifact bytes
-- live in control CAS keyed by sha256.
CREATE TABLE software_releases (
  version text PRIMARY KEY,
  channel text NOT NULL CHECK (channel IN ('stable', 'beta')),
  published_at timestamptz NOT NULL,
  minimum_protocol_version text NOT NULL,
  key_id text NOT NULL,
  -- The exact signed envelope document served to nodes.
  envelope bytea NOT NULL,
  source text NOT NULL CHECK (source IN ('feed', 'cli', 'upload')),
  imported_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE software_release_artifacts (
  version text NOT NULL REFERENCES software_releases(version) ON DELETE CASCADE,
  name text NOT NULL,
  component text NOT NULL,
  os text NOT NULL,
  arch text NOT NULL,
  format text NOT NULL,
  sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  size bigint NOT NULL CHECK (size > 0),
  -- missing: listed by the manifest but not (yet) copied into CAS.
  cas_state text NOT NULL DEFAULT 'missing' CHECK (cas_state IN ('missing', 'ready')),
  PRIMARY KEY (version, name)
);
CREATE INDEX software_release_artifacts_sha256 ON software_release_artifacts (sha256);

-- Bumped whenever a node's software target is set or cleared, so assignment
-- long-polls wake and assignment ETags change.
ALTER TABLE nodes ADD COLUMN software_generation bigint NOT NULL DEFAULT 0 CHECK (software_generation >= 0);

CREATE TABLE node_software_targets (
  node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  version text NOT NULL REFERENCES software_releases(version),
  requested_by uuid NOT NULL REFERENCES actors(id),
  requested_at timestamptz NOT NULL DEFAULT now()
);

-- Every target change, for the rollout history of a node.
CREATE TABLE node_software_target_history (
  id bigserial PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  version text,
  requested_by uuid NOT NULL REFERENCES actors(id),
  requested_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX node_software_target_history_node ON node_software_target_history (node_id, id DESC);

-- Single-row state of the release feed poll.
CREATE TABLE software_feed_state (
  id boolean PRIMARY KEY DEFAULT true CHECK (id),
  url text NOT NULL,
  last_checked_at timestamptz,
  last_success_at timestamptz,
  last_error text NOT NULL DEFAULT '',
  last_version text NOT NULL DEFAULT ''
);

-- +cuelight Down
DROP TABLE software_feed_state;
DROP TABLE node_software_target_history;
DROP TABLE node_software_targets;
ALTER TABLE nodes DROP COLUMN software_generation;
DROP TABLE software_release_artifacts;
DROP TABLE software_releases;
