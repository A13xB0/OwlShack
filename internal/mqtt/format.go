package mqtt

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/meshcore-go/OwlShack/internal/buildinfo"
	"github.com/meshcore-go/OwlShack/internal/modem"
	meshcore "github.com/meshcore-go/meshcore-go"
)

// Zone-aware so downstream observers don't read our feed as naive local time and clamp rxTime to their ingest time; always format a UTC time with it.
const rxTimeLayout = "2006-01-02T15:04:05.000000Z07:00"

type packetMessage struct {
	Timestamp  string `json:"timestamp"`
	OriginID   string `json:"origin_id"`
	Origin     string `json:"origin"`
	Type       string `json:"type"`
	Direction  string `json:"direction"`
	Time       string `json:"time"`
	Date       string `json:"date"`
	Len        string `json:"len"`
	PacketType string `json:"packet_type"`
	Route      string `json:"route"`
	PayloadLen string `json:"payload_len"`
	Raw        string `json:"raw"`
	// RX-only, matching Cisien/meshcoretomqtt (the Andrew-a-g fork has no duration at all): on a TX row these would report a measured 0 dB / 0 dBm for our own transmission.
	SNR      string `json:"SNR,omitempty"`
	RSSI     string `json:"RSSI,omitempty"`
	Score    string `json:"score,omitempty"`
	Duration string `json:"duration,omitempty"`
	Hash     string `json:"hash"`
	// Path is the firmware's "[src -> dst]" hash pair, logged for the four addressed payload types.
	Path string `json:"path,omitempty"`
}

func formatPacket(pkt *meshcore.Packet, rawBytes []byte, originName, originID, direction string, sp modem.StatsProvider) ([]byte, error) {
	now := time.Now().UTC()

	route := "F"
	if pkt.IsRouteDirect() {
		route = "D"
	}

	hash := pkt.PacketHash()

	msg := packetMessage{
		Timestamp:  now.Format(rxTimeLayout),
		OriginID:   originID,
		Origin:     originName,
		Type:       "PACKET",
		Direction:  direction,
		Time:       now.Format("15:04:05"),
		Date:       fmt.Sprintf("%d/%d/%d", now.Day(), int(now.Month()), now.Year()),
		Len:        fmt.Sprintf("%d", len(rawBytes)),
		PacketType: fmt.Sprintf("%d", pkt.PayloadType()),
		Route:      route,
		PayloadLen: fmt.Sprintf("%d", len(pkt.Payload)),
		Raw:        strings.ToUpper(hex.EncodeToString(rawBytes)),
		Hash:       strings.ToUpper(hex.EncodeToString(hash[:])),
	}

	if direction == "rx" {
		// Derived from the frame itself, so they survive missing signal metadata.
		if sp != nil {
			// Firmware: time = getEstAirtimeFor(len) — derived, not measured.
			if ms := sp.EstAirtimeMs(len(rawBytes)); ms > 0 {
				msg.Duration = fmt.Sprintf("%d", ms)
			}
		}
		if pkt.IsRouteDirect() {
			msg.Path = addressedPathLabel(pkt)
		}
		// An unmeasured packet carries no measurement: 0 dB / 0 dBm would look like a real reading, and score is computed FROM the SNR.
		if pkt.HasSignalInfo {
			// Integer dB truncated toward zero: meshcoretomqtt scrapes SNR=(-?\d+) from Dispatcher.cpp's "(int)pkt->getSNR()".
			msg.SNR = fmt.Sprintf("%d", int(pkt.SNR))
			msg.RSSI = fmt.Sprintf("%d", pkt.RSSI)
			if sp != nil {
				msg.Score = fmt.Sprintf("%d", int(sp.PacketScore(float64(pkt.SNR), len(rawBytes))*1000))
			}
		}
	}

	return json.Marshal(msg)
}

// addressedPathLabel reproduces Dispatcher.cpp's "[%02X -> %02X]" trailer: payload[1] then payload[0].
func addressedPathLabel(pkt *meshcore.Packet) string {
	switch pkt.PayloadType() {
	case meshcore.PayloadTypePath, meshcore.PayloadTypeReq,
		meshcore.PayloadTypeResponse, meshcore.PayloadTypeTxtMsg:
	default:
		return ""
	}
	if len(pkt.Payload) < 2 {
		return ""
	}
	return fmt.Sprintf("%02X -> %02X", pkt.Payload[1], pkt.Payload[0])
}

// statsBlock is a wire schema shared with meshcore-bot — change field names in both. Sent and QueueLen are PROCESS-wide, not per-node.
type statsBlock struct {
	UptimeSecs uint32 `json:"uptime_secs"`
	// Both names ship: recv/sent is the firmware and CoreScope client-RF vocabulary, packets_recv/packets_sent is what CoreScope's status ingest requires.
	Recv        uint64 `json:"recv"`
	Sent        uint64 `json:"sent"`
	PacketsRecv uint64 `json:"packets_recv"`
	PacketsSent uint64 `json:"packets_sent"`
	FloodRx     uint64 `json:"flood_rx"`
	DirectRx    uint64 `json:"direct_rx"`
	// The firmware's stats-packets set; process-wide, like sent.
	FloodTx    uint64 `json:"flood_tx"`
	DirectTx   uint64 `json:"direct_tx"`
	FloodDups  uint64 `json:"flood_dups"`
	DirectDups uint64 `json:"direct_dups"`
	// recv_errors is the radio driver failing to read a packet it knew had arrived, matching the firmware field meshcoretomqtt forwards to these same brokers.
	RecvErrors uint64 `json:"recv_errors"`
	// PacketParseErrors is what recv_errors used to carry: intact bytes that did not decode.
	PacketParseErrors uint64 `json:"packet_parse_errors"`
	QueueLen          int    `json:"queue_len"`

	// Firmware stats-core / stats-radio key names; omitted when unmeasurable, but a KISS 0 still publishes.
	BatteryMV  *uint16  `json:"battery_mv,omitempty"`
	MCUTempC   *float64 `json:"mcu_temp_c,omitempty"`
	NoiseFloor int16    `json:"noise_floor"`
	LastRSSI   int16    `json:"last_rssi"`
	LastSNR    float64  `json:"last_snr"`
	// The firmware's rx_air_time / total_air_time; TX arrives via Observer.NoteTx, so it counts the whole process.
	RxAirSecs uint32 `json:"rx_air_secs"`
	TxAirSecs uint32 `json:"tx_air_secs"`

	// Busy (RF congestion, not locally fixable) and queue (our own send rate) drops stay apart: their sum is unactionable.
	TxRequeued     uint64 `json:"tx_requeued"`
	TxDroppedBusy  uint64 `json:"tx_dropped_busy"`
	TxDroppedQueue uint64 `json:"tx_dropped_queue"`
	TxFailed       uint64 `json:"tx_failed"`
	TxOutcomeLost  uint64 `json:"tx_outcome_lost"`

	// RX / driver faults.
	RxDropped uint64 `json:"rx_dropped"`
	// A signal report lost on the modem link, caught by the next frame; its packet was published with no snr/rssi.
	RxMetaMisattributed uint64 `json:"rx_meta_misattributed"`
	RxMetaTimeouts      uint64 `json:"rx_meta_timeouts"`
	HwErrors            uint64 `json:"hw_errors"`
	HwDecodeErrors      uint64 `json:"hw_decode_errors"`
	HandlerSlow         uint64 `json:"handler_slow"`

	// SPI-only faults, omitted on a KISS modem rather than published as zeroes it never measured.
	CRCErrors      *uint64 `json:"crc_errors,omitempty"`
	DriverErrors   *uint64 `json:"driver_errors,omitempty"`
	RecvRecoveries *uint64 `json:"recv_recoveries,omitempty"`
}

// u64 flattens a counter this transport cannot measure to 0: the published schema is shared with
// meshcore-bot and CoreScope, so omitting a key here is a coordinated change, not a local one.
func u64(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
}

// TxCounts mirrors node.TxStats plus the current queue depth; process-wide.
type TxCounts struct {
	Sent          uint64
	QueueLen      int
	BusyRequeued  uint64
	BusyDropped   uint64
	QueueRejected uint64
	Failed        uint64
}

type statusMessage struct {
	Status          string `json:"status"`
	Timestamp       string `json:"timestamp"`
	Origin          string `json:"origin"`
	OriginID        string `json:"origin_id"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version"`
	Radio           string `json:"radio"`
	ClientVersion   string `json:"client_version"`
	// Top level, not inside stats: where firmware 1.16 puts it and where CoreScope reads it.
	Repeat bool       `json:"repeat"`
	Stats  statsBlock `json:"stats"`
}

// ObserverCounts is what the observer itself tallies; RxMs / TxMs are MILLISECONDS.
type ObserverCounts struct {
	RxMs     uint64
	TxMs     uint64
	FloodTx  uint64
	DirectTx uint64
	Relaying bool
	// The most recent received packet's, mirroring the firmware's stats-radio.
	LastSNR  float64
	LastRSSI int16
}

type PacketCounts struct {
	Received   uint64
	FloodRx    uint64
	DirectRx   uint64
	FloodDups  uint64
	DirectDups uint64
}

func formatStatus(status, originName, originID string, radio modem.RadioInfo, ds modem.DeviceStats, packets PacketCounts, tx TxCounts, link modem.LinkStats, obs ObserverCounts, parseErrors uint64) ([]byte, error) {
	var radioStr string
	if radio.FreqHz > 0 {
		radioStr = fmt.Sprintf("%.3f,%.1f,%d,%d",
			float64(radio.FreqHz)/1_000_000,
			float64(radio.BwHz)/1_000,
			radio.SF,
			radio.CR,
		)
	}

	var mcuTemp *float64
	if ds.HaveMCUTemp {
		mcuTemp = &ds.MCUTempC
	}
	var batteryMV *uint16
	if ds.HaveBattery {
		batteryMV = &ds.BatteryMV
	}

	msg := statusMessage{
		Status:          status,
		Timestamp:       time.Now().UTC().Format(rxTimeLayout),
		Origin:          originName,
		OriginID:        originID,
		Model:           "OwlShack",
		FirmwareVersion: buildinfo.Version,
		Radio:           radioStr,
		ClientVersion:   "OwlShack/" + buildinfo.Version,
		Repeat:          obs.Relaying,
		Stats: statsBlock{
			UptimeSecs:        ds.UptimeSecs,
			Recv:              packets.Received,
			PacketsRecv:       packets.Received,
			FloodRx:           packets.FloodRx,
			DirectRx:          packets.DirectRx,
			FloodDups:         packets.FloodDups,
			DirectDups:        packets.DirectDups,
			RecvErrors:        u64(link.RecvErrors),
			PacketParseErrors: parseErrors,

			BatteryMV:  batteryMV,
			MCUTempC:   mcuTemp,
			NoiseFloor: ds.NoiseFloor,
			LastSNR:    obs.LastSNR,
			LastRSSI:   obs.LastRSSI,
			RxAirSecs:  uint32(obs.RxMs / 1000),
			TxAirSecs:  uint32(obs.TxMs / 1000),
			FloodTx:    obs.FloodTx,
			DirectTx:   obs.DirectTx,

			Sent:           tx.Sent,
			PacketsSent:    tx.Sent,
			QueueLen:       tx.QueueLen,
			TxRequeued:     tx.BusyRequeued,
			TxDroppedBusy:  tx.BusyDropped,
			TxDroppedQueue: tx.QueueRejected,
			TxFailed:       tx.Failed,
			TxOutcomeLost:  u64(link.TxOutcomeLost),

			RxDropped:           u64(link.InboundDroppedOldest) + link.InboundDroppedNew,
			RxMetaMisattributed: u64(link.RxMetaMisattributed),
			RxMetaTimeouts:      u64(link.RxMetaTimeouts),
			HwErrors:            u64(link.HwErrors),
			HwDecodeErrors:      u64(link.HwDecodeErrors),
			HandlerSlow:         link.HandlerSlow,

			CRCErrors:      link.CRCErrors,
			DriverErrors:   link.DriverErrors,
			RecvRecoveries: link.RecvRecoveries,
		},
	}
	return json.Marshal(msg)
}
