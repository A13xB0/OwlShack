package repeater

import (
	"context"
	"encoding/hex"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"

	"github.com/meshcore-go/OwlShack/internal/meshpath"
	"github.com/meshcore-go/OwlShack/internal/store"
)

func (r *Repeater) registerHandlers() {
	r.radio.SetRawDataHandler(func(data []byte, snr float32, rssi int8, hasSignal bool) {
		r.recvCount.Add(1)
		if hasSignal {
			r.lastRSSI.Store(int32(rssi))
			r.lastSNRx4.Store(int32(snr * 4)) // firmware (int16_t)(getLastSNR()*4): truncated quarter-dB
			r.haveSignal.Store(true)
		}
		if r.airtime != nil {
			r.rxAirtimeMs.Add(uint64(r.airtime(len(data))))
		}
		if r.logging.Load() { // `log start` — per-packet trace
			r.log.Info("pkt rx", "len", len(data), "snr", snr, "rssi", rssi)
		}
	})

	r.node.OnPacket(meshcore.PayloadTypeAdvert, r.handleAdvert)
	r.node.OnPacket(meshcore.PayloadTypeAnonReq, r.handleAnonReq) // login
	r.node.OnPacket(meshcore.PayloadTypeReq, r.handleReq)         // status/neighbours/acl/owner
	r.node.OnPacket(meshcore.PayloadTypeTxtMsg, r.handleCLI)      // get/set CLI
	r.node.OnPacket(meshcore.PayloadTypePath, r.handlePath)       // learn admin clients' return routes
	r.node.OnPacket(meshcore.PayloadTypeControl, r.handleControl) // discover req/resp
}

// handleAdvert also persists peers so a repeater-only deployment populates discovered_peers; Upsert makes the overlap with a companion harmless.
func (r *Repeater) handleAdvert(pkt *meshcore.Packet) {
	adv, err := meshcore.AdvertFromBytes(pkt.Payload)
	if err != nil || !adv.Verify() {
		return
	}
	appData := adv.AppData()

	// Firmware onAdvertRecv: a zero-hop REPEATER advert is a direct RF neighbour, unless it is a "Share" (transport codes {0,0}).
	isShare := pkt.IsTransport() && pkt.TransportCode1 == 0 && pkt.TransportCode2 == 0
	if pkt.PathHashCount() == 0 && !isShare && appData.Type == "REPEATER" {
		pub := adv.PublicKey.PublicKey()
		if pub != r.node.Identity().Identity.PublicKey() { // don't record ourselves
			snr := 0.0
			if pkt.HasSignalInfo {
				snr = float64(pkt.SNR)
			}
			r.neighbors.Lock()
			r.neighbors.m[pub] = &neighbor{pubkey: pub, name: appData.Name, snr: snr, heard: time.Now()}
			r.neighbors.Unlock()
			if r.hub != nil {
				r.hub.Broadcast("repeaterNeighbors", NeighborInfo{
					PubKey: hex.EncodeToString(pub[:]),
					Name:   appData.Name,
					SNR:    snr,
				})
			}
		}
	}

	p := &store.Peer{
		PubKey:          adv.PublicKey.PublicKeyBytes(),
		Name:            appData.Name,
		Type:            appData.Type,
		Lat:             appData.Lat,
		Lon:             appData.Lon,
		Feat1:           appData.Feat1,
		Feat2:           appData.Feat2,
		OutPath:         pkt.Path,
		OutPathHashSize: meshpath.AdvertHashSize(pkt),
		LastAdvertTS:    adv.Timestamp,
		LastSeen:        time.Now(),
	}
	if pkt.HasSignalInfo {
		snr := float64(pkt.SNR)
		p.SNR = &snr
		p.RSSI = &pkt.RSSI
	}

	r.store.WriteAsync(func() {
		if err := r.store.Peers.Upsert(context.Background(), p); err != nil {
			r.log.Error("failed to persist peer", "error", err)
			return
		}
		if r.hub != nil {
			r.hub.Broadcast("peers", map[string]any{
				"pubkey":          hex.EncodeToString(p.PubKey[:]),
				"name":            p.Name,
				"type":            p.Type,
				"lat":             p.Lat,
				"lon":             p.Lon,
				"snr":             p.SNR,
				"rssi":            p.RSSI,
				"outPath":         hex.EncodeToString(p.OutPath),
				"outPathHashSize": p.OutPathHashSize,
				"lastAdvertTs":    p.LastAdvertTS,
				"lastSeen":        p.LastSeen.Format(time.RFC3339),
			})
		}
	})
}
