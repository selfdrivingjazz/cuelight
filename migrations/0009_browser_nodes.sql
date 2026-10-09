-- +cuelight Up
ALTER TABLE nodes ADD COLUMN node_class text NOT NULL DEFAULT 'native' CHECK (node_class IN ('native', 'browser'));

CREATE TABLE browser_node_claims (
  id uuid PRIMARY KEY,
  code_hash bytea NOT NULL UNIQUE,
  poll_token_hash bytea NOT NULL,
  user_agent text NOT NULL DEFAULT '',
  expires_at timestamptz NOT NULL,
  node_id uuid REFERENCES nodes(id),
  claimed_by uuid REFERENCES actors(id),
  claimed_at timestamptz,
  delivered_at timestamptz,
  created_at timestamptz NOT NULL
);

CREATE INDEX browser_node_claims_expiry_idx ON browser_node_claims (expires_at) WHERE node_id IS NULL;

CREATE TABLE browser_node_credentials (
  id uuid PRIMARY KEY,
  node_id uuid NOT NULL REFERENCES nodes(id),
  token_hash bytea NOT NULL UNIQUE,
  expires_at timestamptz NOT NULL,
  renew_after timestamptz NOT NULL,
  revoked_at timestamptz,
  last_seen_at timestamptz,
  created_at timestamptz NOT NULL
);

CREATE INDEX browser_node_credentials_node_idx ON browser_node_credentials (node_id);

-- +cuelight Down
DROP INDEX browser_node_credentials_node_idx;
DROP TABLE browser_node_credentials;
DROP INDEX browser_node_claims_expiry_idx;
DROP TABLE browser_node_claims;
ALTER TABLE nodes DROP COLUMN node_class;
