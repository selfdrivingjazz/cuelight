-- +cuelight Up
CREATE TABLE screenshot_jobs (
  id uuid PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  expected jsonb NOT NULL,
  requested_at timestamptz NOT NULL,
  expires_at timestamptz NOT NULL,
  claimed_at timestamptz,
  completion_at timestamptz,
  status text NOT NULL CHECK (status IN ('queued', 'claimed', 'completed', 'failed', 'unsupported', 'expired')),
  result_artifact_digest text REFERENCES artifacts(digest),
  result_media_type text,
  error_reason text,
  retention_policy text NOT NULL,
  retention_until timestamptz NOT NULL,
  artifact_retention_until timestamptz NOT NULL,
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (expires_at > requested_at),
  CHECK (retention_until >= expires_at),
  CHECK (artifact_retention_until >= retention_until),
  CHECK ((status = 'completed' AND result_artifact_digest IS NOT NULL AND result_media_type IS NOT NULL AND length(trim(result_media_type)) > 0)
      OR (status <> 'completed' AND result_artifact_digest IS NULL AND result_media_type IS NULL)),
  CHECK ((status IN ('failed', 'unsupported', 'expired') AND error_reason IS NOT NULL)
      OR (status IN ('queued', 'claimed', 'completed')))
);

CREATE INDEX screenshot_jobs_expiry_idx ON screenshot_jobs (expires_at, id)
  WHERE status IN ('queued', 'claimed');
CREATE INDEX screenshot_jobs_retention_idx ON screenshot_jobs (retention_until, id);
CREATE INDEX screenshot_jobs_artifact_retention_idx ON screenshot_jobs (artifact_retention_until, id)
  WHERE result_artifact_digest IS NOT NULL;

CREATE TABLE screenshot_idempotency_records (
  actor_id uuid NOT NULL REFERENCES actors(id),
  key text NOT NULL,
  request_digest text NOT NULL CHECK (request_digest ~ '^[a-f0-9]{64}$'),
  job_id uuid NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL,
  PRIMARY KEY (actor_id, key),
  CHECK (expires_at >= created_at)
);
CREATE INDEX screenshot_idempotency_expiry_idx
  ON screenshot_idempotency_records (expires_at, actor_id, key);

-- +cuelight Down
DROP TABLE screenshot_idempotency_records;
DROP INDEX screenshot_jobs_artifact_retention_idx;
DROP INDEX screenshot_jobs_retention_idx;
DROP INDEX screenshot_jobs_expiry_idx;
DROP TABLE screenshot_jobs;
