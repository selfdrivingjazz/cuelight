-- +cuelight Up
-- Poster thumbnails are keyed by the source artifact digest so identical
-- content shares one thumbnail regardless of how many media rows reference it.
CREATE TABLE media_thumbnails (
  source_digest text PRIMARY KEY REFERENCES artifacts(digest),
  thumbnail_digest text NOT NULL REFERENCES artifacts(digest),
  width integer NOT NULL CHECK (width > 0),
  height integer NOT NULL CHECK (height > 0),
  created_at timestamptz NOT NULL DEFAULT now()
);

-- +cuelight Down
DROP TABLE media_thumbnails;
