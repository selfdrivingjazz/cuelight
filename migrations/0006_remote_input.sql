-- +cable Up
ALTER TABLE assignments
  ADD COLUMN remote_input boolean NOT NULL DEFAULT false,
  ADD COLUMN show_qr boolean NOT NULL DEFAULT false,
  ADD CONSTRAINT assignments_qr_requires_remote CHECK (NOT show_qr OR remote_input);

-- +cable Down
ALTER TABLE assignments DROP CONSTRAINT assignments_qr_requires_remote;
ALTER TABLE assignments DROP COLUMN show_qr;
ALTER TABLE assignments DROP COLUMN remote_input;
