-- Where a cap trigger's alerts must be: a point and margin in km (all three NULL for none), or regions.

ALTER TABLE triggers ADD COLUMN location_lat REAL;

ALTER TABLE triggers ADD COLUMN location_lon REAL;

ALTER TABLE triggers ADD COLUMN location_radius_km REAL;

-- The region ids a cap trigger's alerts must cover, newline-encoded; NULL takes alerts from anywhere.
ALTER TABLE triggers ADD COLUMN location_regions TEXT;
