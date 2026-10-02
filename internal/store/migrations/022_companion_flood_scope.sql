-- The region a companion's floods go out in, as a firmware companion's default scope; empty floods unscoped.
ALTER TABLE companions ADD COLUMN flood_scope TEXT NOT NULL DEFAULT '';
