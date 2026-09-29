-- The feed URL an rss or cap trigger polls; empty for every other trigger type.

ALTER TABLE triggers ADD COLUMN url TEXT NOT NULL DEFAULT '';
