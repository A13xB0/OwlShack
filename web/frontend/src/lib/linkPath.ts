// Resolves a link monitor's saved path to "<source> → <destination>" hop labels.

export interface NamedPeer {
  pubkey: string;
  name: string;
}

// PathPeer is a NamedPeer with the two fields hop resolution needs to choose between candidates.
export interface PathPeer extends NamedPeer {
  type?: string;
  lastSeen?: string;
}

export interface ResolvedHop {
  hash: string;
  peer?: PathPeer;
  alternatives: PathPeer[];
}

// resolveHopPeer names a hop hash, or declines to. A hash is a pubkey prefix, not an identity: on a
// 338-peer mesh 92 of 211 one-byte hashes match more than one peer and the worst matches five. Only
// a repeater forwards, so a hash whose only matches are chat or sensor nodes has not been
// identified at all — the real forwarder is a repeater we hold no advert for, and naming the phone
// that shares the prefix would be a confident lie. Among repeaters the most recently heard wins as
// the least-bad tiebreak, and every other candidate comes back in alternatives so a caller can show
// that the answer was a guess.
export function resolveHopPeer(
  hash: string,
  candidates: PathPeer[] | undefined,
): ResolvedHop {
  const repeaters = (candidates ?? []).filter((c) => c.type === "REPEATER");
  if (repeaters.length === 0) return { hash, alternatives: [] };
  const best = repeaters.reduce((a, b) =>
    (b.lastSeen ?? "") > (a.lastSeen ?? "") ? b : a,
  );
  return { hash, peer: best, alternatives: repeaters.filter((c) => c !== best) };
}

// Where we sit on a path, and whether we sit on it at all. A flood ACCUMULATES a hash at each
// relay, so a received one lists where the packet has been and it reached us from the last of
// them; our own send leads instead. A DIRECT route CONSUMES its hashes (Mesh.cpp:334-342), so a
// received one carries only the road ahead — a leg running from the relay we overheard to its next
// hop, with us on neither end. Naming us at the head would claim we are forwarding it on, which is
// as wrong as the tail claiming the next hop delivered it to us. An echo of our own flood is a
// loop, so we sit on both ends.
export function pathEnds(direction?: string, route?: string) {
  const aheadOnly = !!route?.includes("DIRECT") && direction !== "tx";
  return {
    weLead: direction === "tx" || direction === "echo",
    weTrail: direction !== "tx" && !aheadOnly,
    aheadOnly,
  };
}

// The hops a path carries, each named or left as its hash. Both the chain text and the map read it,
// so neither can resolve a hop the other would not.
export function resolveHops(
  pathHex: string,
  hashSize: number | undefined,
  peers: PathPeer[],
): ResolvedHop[] {
  const size = Math.max(1, hashSize ?? 1);
  const byHash = buildPeerCandidatesByHash(peers, size);
  return hexToHopHashes(pathHex, size).map((h) => resolveHopPeer(h, byHash.get(h)));
}

export function hexToHopHashes(pathHex: string, hashSize: number): string[] {
  const step = hashSize * 2;
  if (step <= 0) return [];
  const out: string[] = [];
  for (let i = 0; i + step <= pathHex.length; i += step) {
    out.push(pathHex.slice(i, i + step).toLowerCase());
  }
  return out;
}

// buildPeerCandidatesByHash keeps every peer sharing a hop hash. A hash is a pubkey prefix, so it
// is not unique: on a 338-peer mesh, 92 one-byte hashes match more than one node and the worst
// matches five. A caller that renders one name is asserting a node the packet never identified.
export function buildPeerCandidatesByHash(
  peers: NamedPeer[],
  hashSize: number,
): Map<string, NamedPeer[]> {
  const map = new Map<string, NamedPeer[]>();
  for (const p of peers) {
    const k = p.pubkey.slice(0, hashSize * 2).toLowerCase();
    const at = map.get(k);
    if (at) at.push(p);
    else map.set(k, [p]);
  }
  return map;
}

// Display-only toggles; the collector still records both readings.
export const FIRST_HOP_METRIC = "snr_hop1";
export const LAST_SNR_METRIC = "last_snr";

export function filterMetrics(
  metrics: Record<string, number>,
  ignoreFirstHop: boolean,
  hideLastSnr: boolean,
): Record<string, number> {
  let out = metrics;
  if (ignoreFirstHop && FIRST_HOP_METRIC in out) {
    const { [FIRST_HOP_METRIC]: _omit, ...rest } = out;
    out = rest;
  }
  if (hideLastSnr && LAST_SNR_METRIC in out) {
    const { [LAST_SNR_METRIC]: _omit, ...rest } = out;
    out = rest;
  }
  return out;
}

export function filterMetricNames(
  names: string[],
  ignoreFirstHop: boolean,
  hideLastSnr: boolean,
): string[] {
  if (!ignoreFirstHop && !hideLastSnr) return names;
  return names.filter(
    (n) =>
      !(ignoreFirstHop && n === FIRST_HOP_METRIC) &&
      !(hideLastSnr && n === LAST_SNR_METRIC),
  );
}

// Hop numbers are 1-indexed.
export function hopDirectionLabel(
  pathHex: string,
  hashSize: number,
  peers: PathPeer[],
  originName = "you",
): (hop: number) => string {
  const hashes = hexToHopHashes(pathHex, hashSize);
  const byHash = buildPeerCandidatesByHash(peers, hashSize);
  // A hash naming no repeater stays hex, and one naming several says so — a link label that reads
  // "A → B" is otherwise an assertion about which node relayed, which the hash cannot support.
  const nameFor = (hash: string) => {
    const { peer, alternatives } = resolveHopPeer(hash, byHash.get(hash));
    if (!peer) return hash;
    return alternatives.length > 0 ? `${peer.name} +${alternatives.length}` : peer.name;
  };
  return (hop: number) => {
    const toHash = hashes[hop - 1];
    if (!toHash) return `Hop ${hop}`;
    const to = nameFor(toHash);
    const from = hop === 1 ? originName : nameFor(hashes[hop - 2]);
    return `${from} → ${to}`;
  };
}

// A link to plot one path on the map. `origin` is the node the path starts at — an advert has one
// (its sender), a packet does not. `direction` and `route` carry the same meaning they do in
// HopPath, so the map places "us" on the end the text does.
export function mapPathHref(l: {
  path: string;
  hashSize?: number;
  origin?: string;
  direction?: string;
  route?: string;
}): string {
  const q = new URLSearchParams({ path: l.path });
  if (l.hashSize) q.set("hs", String(l.hashSize));
  if (l.origin) q.set("origin", l.origin);
  if (l.direction) q.set("dir", l.direction);
  if (l.route) q.set("route", l.route);
  return `/map?${q}`;
}
