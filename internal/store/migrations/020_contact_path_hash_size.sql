-- Bytes per hop for everything sent to each contact, set from its advert when added and changed only by the operator.

-- A zero-hop advert always stored 1 (Mesh::sendZeroHop sets path_len 0), so an empty path at 1 is unknown; a larger size could only have come from a flood advert.
UPDATE discovered_peers SET out_path_hash_size = 0 WHERE (out_path IS NULL OR length(out_path) = 0) AND out_path_hash_size <= 1;

ALTER TABLE companion_contacts ADD COLUMN path_hash_size INTEGER NOT NULL DEFAULT 1 CHECK (path_hash_size BETWEEN 1 AND 3);

UPDATE companion_contacts SET path_hash_size = COALESCE(
	(SELECT dp.out_path_hash_size FROM discovered_peers dp WHERE dp.pubkey = companion_contacts.peer_pubkey AND dp.out_path_hash_size BETWEEN 1 AND 3),
	(SELECT c.path_hash_size FROM companions c WHERE c.id = companion_contacts.companion_id AND c.path_hash_size BETWEEN 1 AND 3),
	(SELECT s.path_hash_size FROM settings s WHERE s.id = 1 AND s.path_hash_size BETWEEN 1 AND 3),
	CASE WHEN length(out_path) > 0 AND out_path_hash_size BETWEEN 1 AND 3 THEN out_path_hash_size END,
	1);

-- A saved multi-hop route at another size cannot be sent at this one, so it is dropped and relearned by flood; a stored 0 was sent at 1.
UPDATE companion_contacts SET out_path = NULL, out_path_hash_size = 0
WHERE length(out_path) > 0 AND max(out_path_hash_size, 1) <> path_hash_size;
