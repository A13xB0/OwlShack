-- Clamps trigger path hash sizes to the 3-byte maximum; only the bot form offered 4, and Config.Validate refuses it.

UPDATE triggers SET path_hash_size = 3 WHERE path_hash_size > 3;
