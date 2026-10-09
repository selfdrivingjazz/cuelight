-- +cuelight Up
-- Cable is a control-hosted web page: each deployment is one web media item
-- (its URL is derived from the control origin at publish time) and the guide
-- authenticates with per-node, per-deployment guide tokens. Shell builds,
-- compiled revisions and Cable profiles are gone.
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

INSERT INTO media (id, kind, title, deployment_id, version, created_by, updated_by, created_at, updated_at)
SELECT md5('cable-media:' || d.id::text)::uuid, 'web', 'Cable · ' || d.name, d.id, 1, d.created_by, d.updated_by, d.created_at, d.updated_at
FROM cable_deployments d
WHERE NOT EXISTS (SELECT 1 FROM media m WHERE m.deployment_id = d.id);

-- Nodes whose current assignment is a compiled Cable release are re-assigned
-- to their deployment's Cable media item by the control plane at startup
-- (assignment service publishes the hosted web release), which then deletes
-- each converted row.
CREATE TABLE cable_assignment_conversions (
  node_id uuid PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE,
  media_id uuid NOT NULL REFERENCES media(id) ON DELETE CASCADE,
  desired_generation bigint NOT NULL CHECK (desired_generation > 0),
  remote_input boolean NOT NULL,
  show_qr boolean NOT NULL,
  actor_id uuid NOT NULL REFERENCES actors(id)
);
INSERT INTO cable_assignment_conversions (node_id, media_id, desired_generation, remote_input, show_qr, actor_id)
SELECT a.node_id, m.id, a.desired_generation, a.remote_input, a.show_qr, a.created_by
FROM (SELECT DISTINCT ON (node_id) * FROM assignments ORDER BY node_id, desired_generation DESC) a
JOIN releases r ON r.id = a.release_id
JOIN LATERAL (
  SELECT rev.deployment_id FROM cable_revisions rev
  WHERE rev.digest = r.payload->'presentation'->'cable'->>'revisionArtifactDigest'
  UNION ALL
  SELECT p.deployment_id FROM cable_profiles p WHERE p.release_id = r.id
  LIMIT 1
) deployment ON true
JOIN media m ON m.deployment_id = deployment.deployment_id
WHERE r.payload->'presentation'->>'kind' = 'cable';

DROP TABLE cable_profiles;
DROP INDEX cable_revisions_deployment_idx;
DROP TABLE cable_revisions;
DROP TABLE cable_shell_build_artifacts;
DROP TABLE cable_shell_builds;

-- +cuelight Down
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
  id text PRIMARY KEY CHECK (id ~ '^[a-f0-9]{64}$'),
  deployment_id uuid NOT NULL REFERENCES cable_deployments(id) ON DELETE RESTRICT,
  deployment_version bigint NOT NULL,
  shell_build_id uuid NOT NULL REFERENCES cable_shell_builds(id),
  digest text NOT NULL UNIQUE CHECK (digest ~ '^[a-f0-9]{64}$'),
  shell_build_digest text NOT NULL CHECK (shell_build_digest ~ '^[a-f0-9]{64}$'),
  entrypoint text NOT NULL,
  bridge_protocol text NOT NULL,
  timezone text NOT NULL,
  theme text NOT NULL CHECK (theme IN ('nebula','gallery','noir')),
  generated_at timestamptz NOT NULL,
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
DROP TABLE cable_assignment_conversions;
DELETE FROM media WHERE deployment_id IS NOT NULL;
DROP INDEX cable_guide_tokens_expiry_idx;
DROP INDEX cable_guide_tokens_node_idx;
DROP TABLE cable_guide_tokens;
