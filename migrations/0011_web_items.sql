-- +cuelight Up
CREATE TABLE web_items (
  id uuid PRIMARY KEY,
  url text NOT NULL UNIQUE CHECK (url ~ '^https?://'),
  title text NOT NULL DEFAULT '',
  favicon_url text NOT NULL DEFAULT '',
  thumbnail_digest text REFERENCES artifacts(digest),
  thumbnail_media_type text,
  thumbnail_source text CHECK (thumbnail_source IN ('server', 'node')),
  thumbnail_captured_at timestamptz,
  thumbnail_attempted_at timestamptz,
  thumbnail_error text NOT NULL DEFAULT '',
  created_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK ((thumbnail_digest IS NULL AND thumbnail_media_type IS NULL AND thumbnail_source IS NULL AND thumbnail_captured_at IS NULL)
      OR (thumbnail_digest IS NOT NULL AND thumbnail_media_type IS NOT NULL AND thumbnail_source IS NOT NULL AND thumbnail_captured_at IS NOT NULL))
);

CREATE INDEX web_items_created_idx ON web_items (created_at, id);
CREATE INDEX web_items_thumbnail_digest_idx ON web_items (thumbnail_digest) WHERE thumbnail_digest IS NOT NULL;

-- Web pages that were only ever published as web releases become library items.
INSERT INTO web_items (id, url, created_by, created_at, updated_at)
SELECT DISTINCT ON (payload->'presentation'->'web'->>'url')
       id, payload->'presentation'->'web'->>'url', created_by, created_at, created_at
FROM releases
WHERE payload->'presentation'->>'kind' = 'web'
  AND payload->'presentation'->'web'->>'url' ~ '^https?://'
ORDER BY payload->'presentation'->'web'->>'url', created_at;

-- +cuelight Down
DROP INDEX web_items_thumbnail_digest_idx;
DROP INDEX web_items_created_idx;
DROP TABLE web_items;
