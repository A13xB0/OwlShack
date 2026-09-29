-- Indexed packet_hash and path columns; backfillPacketFields fills them for rows stored before this.

ALTER TABLE packets ADD COLUMN packet_hash TEXT NOT NULL DEFAULT '';
ALTER TABLE packets ADD COLUMN path TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_packets_packet_hash ON packets(packet_hash);
CREATE INDEX IF NOT EXISTS idx_packets_path ON packets(path);
