-- Who the MQTT feed is published as: the node companion, as every install did before this, or the repeater.
ALTER TABLE mqtt_settings ADD COLUMN identity TEXT NOT NULL DEFAULT 'companion' CHECK (identity IN ('companion', 'repeater'));
