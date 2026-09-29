-- The openHop modem's access token, in its own column because the config REST reads hand out the connection string.

ALTER TABLE settings ADD COLUMN modem_token TEXT;
