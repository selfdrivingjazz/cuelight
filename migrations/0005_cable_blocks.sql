-- +cable Up
CREATE EXTENSION IF NOT EXISTS btree_gist;
ALTER TABLE cable_blocks
  ADD COLUMN asset_id uuid REFERENCES assets(id) ON DELETE RESTRICT;
ALTER TABLE cable_blocks
  ALTER COLUMN playlist_id DROP NOT NULL;
ALTER TABLE cable_blocks
  ADD CONSTRAINT cable_blocks_exactly_one_source
  CHECK (num_nonnulls(playlist_id, asset_id) = 1);
ALTER TABLE cable_blocks
  ADD CONSTRAINT cable_blocks_channel_time_excl
  EXCLUDE USING gist (
    channel_id WITH =,
    tstzrange(starts_at, ends_at, '[)') WITH &&
  );

-- +cable Down
ALTER TABLE cable_blocks DROP CONSTRAINT cable_blocks_channel_time_excl;
ALTER TABLE cable_blocks DROP CONSTRAINT cable_blocks_exactly_one_source;
ALTER TABLE cable_blocks ALTER COLUMN playlist_id SET NOT NULL;
ALTER TABLE cable_blocks DROP COLUMN asset_id;
