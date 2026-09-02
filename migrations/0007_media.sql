-- +cable Up
CREATE TABLE media (
  id uuid PRIMARY KEY,
  asset_id uuid NOT NULL REFERENCES assets(id),
  title text NOT NULL CHECK (length(title) > 0),
  description text,
  artist text,
  tags text[] NOT NULL DEFAULT '{}',
  artwork_asset_id uuid REFERENCES assets(id),
  fit text NOT NULL CHECK (fit IN ('fit', 'fill')),
  loop boolean NOT NULL,
  muted boolean NOT NULL,
  volume integer NOT NULL CHECK (volume >= 0 AND volume <= 100),
  orientation integer NOT NULL CHECK (orientation IN (0, 90, 180, 270)),
  version bigint NOT NULL DEFAULT 1 CHECK (version > 0),
  created_by uuid NOT NULL REFERENCES actors(id),
  updated_by uuid NOT NULL REFERENCES actors(id),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO media
  (id, asset_id, title, tags, fit, loop, muted, volume, orientation,
   version, created_by, updated_by, created_at, updated_at)
SELECT DISTINCT ON (a.id)
  a.id, a.id, a.name, '{}', 'fit', true, true, 0, 0,
  1, u.created_by, u.created_by, a.created_at, a.created_at
FROM assets a
JOIN asset_artifacts aa ON aa.asset_id = a.id AND aa.role = 'original'
JOIN artifacts artifact ON artifact.digest = aa.artifact_digest
JOIN uploads u ON u.artifact_digest = artifact.digest AND u.state = 'complete'
WHERE artifact.state = 'ready'
  AND (artifact.media_type LIKE 'image/%' OR artifact.media_type LIKE 'video/%' OR artifact.media_type LIKE 'audio/%')
ORDER BY a.id, u.created_at;


CREATE INDEX media_created_idx ON media (created_at, id);
CREATE INDEX media_asset_idx ON media (asset_id);

-- +cable Down
DROP INDEX media_asset_idx;
DROP INDEX media_created_idx;
DROP TABLE media;
