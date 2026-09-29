-- Optional group bot failover.

ALTER TABLE triggers ADD COLUMN failover_pattern TEXT NOT NULL DEFAULT '';

ALTER TABLE triggers ADD COLUMN failover_timeout INTEGER NOT NULL DEFAULT 0;
