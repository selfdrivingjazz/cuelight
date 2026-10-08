-- +cable Up
-- One media model: file media wrap an uploaded asset, web media show a live
-- page. Cable items are web media maintained for a deployment; their URL is
-- derived from the control plane origin when a release is published.
ALTER TABLE media
  ADD COLUMN kind text NOT NULL DEFAULT 'file' CHECK (kind IN ('file', 'web')),
  ADD COLUMN url text CHECK (url ~ '^https?://'),
  ADD COLUMN deployment_id uuid UNIQUE REFERENCES cable_deployments(id) ON DELETE CASCADE,
  ADD COLUMN favicon_url text NOT NULL DEFAULT '',
  ADD COLUMN thumbnail_digest text REFERENCES artifacts(digest),
  ADD COLUMN thumbnail_media_type text,
  ADD COLUMN thumbnail_source text CHECK (thumbnail_source IN ('server', 'node')),
  ADD COLUMN thumbnail_captured_at timestamptz,
  ADD COLUMN thumbnail_attempted_at timestamptz,
  ADD COLUMN thumbnail_error text NOT NULL DEFAULT '';
ALTER TABLE media ALTER COLUMN asset_id DROP NOT NULL;
ALTER TABLE media
  ALTER COLUMN fit SET DEFAULT 'fit',
  ALTER COLUMN loop SET DEFAULT true,
  ALTER COLUMN muted SET DEFAULT false,
  ALTER COLUMN volume SET DEFAULT 100,
  ALTER COLUMN orientation SET DEFAULT 0;
ALTER TABLE media ADD CONSTRAINT media_kind_target CHECK (
  (kind = 'file' AND asset_id IS NOT NULL AND url IS NULL AND deployment_id IS NULL)
  OR (kind = 'web' AND asset_id IS NULL AND num_nonnulls(url, deployment_id) = 1));
ALTER TABLE media ADD CONSTRAINT media_web_thumbnail_complete CHECK (
  (thumbnail_digest IS NULL AND thumbnail_media_type IS NULL AND thumbnail_source IS NULL AND thumbnail_captured_at IS NULL)
  OR (thumbnail_digest IS NOT NULL AND thumbnail_media_type IS NOT NULL AND thumbnail_source IS NOT NULL AND thumbnail_captured_at IS NOT NULL));
CREATE UNIQUE INDEX media_web_url_idx ON media (url) WHERE url IS NOT NULL;
CREATE INDEX media_kind_created_idx ON media (kind, created_at, id);
CREATE INDEX media_thumbnail_digest_idx ON media (thumbnail_digest) WHERE thumbnail_digest IS NOT NULL;

INSERT INTO media
  (id, kind, url, title, favicon_url, thumbnail_digest, thumbnail_media_type, thumbnail_source,
   thumbnail_captured_at, thumbnail_attempted_at, thumbnail_error, version, created_by, updated_by, created_at, updated_at)
SELECT id, 'web', url, COALESCE(NULLIF(btrim(title), ''), url), favicon_url, thumbnail_digest, thumbnail_media_type, thumbnail_source,
       thumbnail_captured_at, thumbnail_attempted_at, thumbnail_error, 1, created_by, created_by, created_at, updated_at
FROM web_items;

DROP INDEX web_items_thumbnail_digest_idx;
DROP INDEX web_items_created_idx;
DROP TABLE web_items;

-- Playlists may hold web media for a fixed duration alongside file assets.
ALTER TABLE cable_playlist_items ALTER COLUMN asset_id DROP NOT NULL;
ALTER TABLE cable_playlist_items ADD COLUMN media_id uuid REFERENCES media(id) ON DELETE RESTRICT;
ALTER TABLE cable_playlist_items ADD CONSTRAINT cable_playlist_items_exactly_one_source
  CHECK (num_nonnulls(asset_id, media_id) = 1);

-- Assignments record what the operator targeted; the release stays as audit.
ALTER TABLE assignments
  ADD COLUMN media_id uuid REFERENCES media(id) ON DELETE SET NULL,
  ADD COLUMN playlist_id uuid REFERENCES cable_playlists(id) ON DELETE SET NULL;
UPDATE assignments a SET media_id = m.id
FROM releases r, media m
WHERE r.id = a.release_id
  AND r.payload->'presentation'->>'kind' = 'media'
  AND m.asset_id::text = r.payload->'presentation'->'media'->'playback'->>'assetId';
UPDATE assignments a SET media_id = m.id
FROM releases r, media m
WHERE r.id = a.release_id
  AND r.payload->'presentation'->>'kind' = 'web'
  AND m.url = r.payload->'presentation'->'web'->>'url';
CREATE INDEX assignments_media_idx ON assignments (media_id) WHERE media_id IS NOT NULL;

-- +cable Down
DROP INDEX assignments_media_idx;
ALTER TABLE assignments DROP COLUMN playlist_id, DROP COLUMN media_id;

DELETE FROM cable_playlist_items WHERE asset_id IS NULL;
ALTER TABLE cable_playlist_items DROP CONSTRAINT cable_playlist_items_exactly_one_source;
ALTER TABLE cable_playlist_items DROP COLUMN media_id;
ALTER TABLE cable_playlist_items ALTER COLUMN asset_id SET NOT NULL;

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
INSERT INTO web_items
  (id, url, title, favicon_url, thumbnail_digest, thumbnail_media_type, thumbnail_source,
   thumbnail_captured_at, thumbnail_attempted_at, thumbnail_error, created_by, created_at, updated_at)
SELECT id, url, title, favicon_url, thumbnail_digest, thumbnail_media_type, thumbnail_source,
       thumbnail_captured_at, thumbnail_attempted_at, thumbnail_error, created_by, created_at, updated_at
FROM media WHERE kind = 'web' AND url IS NOT NULL;
DELETE FROM media WHERE kind = 'web';

DROP INDEX media_thumbnail_digest_idx;
DROP INDEX media_kind_created_idx;
DROP INDEX media_web_url_idx;
ALTER TABLE media DROP CONSTRAINT media_web_thumbnail_complete;
ALTER TABLE media DROP CONSTRAINT media_kind_target;
ALTER TABLE media
  ALTER COLUMN fit DROP DEFAULT,
  ALTER COLUMN loop DROP DEFAULT,
  ALTER COLUMN muted DROP DEFAULT,
  ALTER COLUMN volume DROP DEFAULT,
  ALTER COLUMN orientation DROP DEFAULT;
ALTER TABLE media ALTER COLUMN asset_id SET NOT NULL;
ALTER TABLE media
  DROP COLUMN thumbnail_error,
  DROP COLUMN thumbnail_attempted_at,
  DROP COLUMN thumbnail_captured_at,
  DROP COLUMN thumbnail_source,
  DROP COLUMN thumbnail_media_type,
  DROP COLUMN thumbnail_digest,
  DROP COLUMN favicon_url,
  DROP COLUMN deployment_id,
  DROP COLUMN url,
  DROP COLUMN kind;
