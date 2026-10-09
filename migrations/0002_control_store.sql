-- +cuelight Up
ALTER TABLE idempotency_records
  ALTER COLUMN response_body TYPE bytea
  USING convert_to(response_body::text, 'UTF8');

ALTER TABLE enrollment_tokens
  ADD COLUMN created_at timestamptz NOT NULL DEFAULT now();

ALTER TABLE uploads
  ADD COLUMN completed_asset_id uuid REFERENCES assets(id),
  ADD COLUMN failure_reason text;

CREATE TABLE pki_idempotency_records (
  operation text NOT NULL,
  actor_id text NOT NULL,
  key text NOT NULL,
  request_hash text NOT NULL,
  response_ciphertext bytea,
  created_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (operation, actor_id, key),
  CHECK (expires_at > created_at)
);

CREATE TABLE media_idempotency_records (
  operation text NOT NULL,
  actor_id uuid NOT NULL REFERENCES actors(id),
  key text NOT NULL,
  request_hash text NOT NULL,
  response_id uuid,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (operation, actor_id, key)
);

CREATE TABLE release_publications (
  actor_id uuid NOT NULL REFERENCES actors(id),
  key text NOT NULL,
  request_digest text NOT NULL CHECK (request_digest ~ '^[a-f0-9]{64}$'),
  release_id uuid NOT NULL,
  release_digest text NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (actor_id, key),
  FOREIGN KEY (release_id, release_digest) REFERENCES releases(id, digest)
);

CREATE TABLE assignment_idempotency_records (
  node_id uuid NOT NULL REFERENCES nodes(id),
  actor_id uuid NOT NULL REFERENCES actors(id),
  key text NOT NULL,
  request_digest text NOT NULL CHECK (request_digest ~ '^[a-f0-9]{64}$'),
  assignment_id uuid NOT NULL REFERENCES assignments(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (node_id, actor_id, key)
);

CREATE INDEX assignments_node_digest_generation_idx
  ON assignments (node_id, release_digest, desired_generation DESC);

-- +cuelight Down
DROP INDEX assignments_node_digest_generation_idx;
DROP TABLE assignment_idempotency_records;
DROP TABLE release_publications;
DROP TABLE media_idempotency_records;
DROP TABLE pki_idempotency_records;
ALTER TABLE enrollment_tokens DROP COLUMN created_at;
ALTER TABLE uploads DROP COLUMN failure_reason, DROP COLUMN completed_asset_id;
ALTER TABLE idempotency_records
  ALTER COLUMN response_body TYPE jsonb
  USING convert_from(response_body, 'UTF8')::jsonb;
