-- Which basemap the maps draw; a stored CARTO key keeps CARTO, everyone else moves to OpenStreetMap.

ALTER TABLE settings ADD COLUMN map_provider TEXT NOT NULL DEFAULT 'osm';

UPDATE settings SET map_provider = 'carto' WHERE map_tile_key IS NOT NULL AND map_tile_key <> '';

-- How OpenStreetMap, which has no dark style, is recoloured in dark mode: 'original' inverted, or 'simplified'.
ALTER TABLE settings ADD COLUMN map_dark_style TEXT NOT NULL DEFAULT 'original';
