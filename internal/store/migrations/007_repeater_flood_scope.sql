-- flood_max_unscoped (the firmware's NodePrefs hop cap) and default_region ('' = unscoped adverts).

ALTER TABLE repeater ADD COLUMN flood_max_unscoped INTEGER;
ALTER TABLE repeater ADD COLUMN default_region TEXT NOT NULL DEFAULT '';
