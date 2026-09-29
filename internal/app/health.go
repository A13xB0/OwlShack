package app

import (
	"errors"
	"sync/atomic"
	"time"

	"github.com/meshcore-go/OwlShack/internal/api"
	"github.com/meshcore-go/OwlShack/internal/modem"
)

// radioActivity is process-scoped, not a backend field: a reload swaps the backend but the radio's history carries on. 0 = none since start.
type radioActivity struct {
	lastRx, lastTx, lastReply atomic.Int64
	// silentSince is when the first link that has not heard an answer connected; a link that drops for want of traffic must not restart the silence.
	silentSince atomic.Int64
	// startErr is why the radio stack is down, nil while it runs.
	startErr atomic.Pointer[error]
}

func (a *radioActivity) started(err error) {
	if err == nil {
		a.startErr.Store(nil)
		return
	}
	a.startErr.Store(&err)
}

var (
	errCompanionStart = errors.New("companion startup")
	errRepeaterStart  = errors.New("repeater startup")
)

const (
	// txFailingAfter and txFailingMin keep a late TX_DONE, which refuses a few retries for a second or two, from flagging.
	txFailingAfter = 2 * time.Minute
	txFailingMin   = 3
	// diskLowBelow leaves room for a WAL checkpoint and the pre-upgrade copy's first pages; below it writes are about to fail.
	diskLowBelow = 32 << 20
)

func (a *radioActivity) rx() { a.lastRx.Store(time.Now().UnixNano()) }
func (a *radioActivity) tx() { a.lastTx.Store(time.Now().UnixNano()) }

// keepReply outlives the modem's teardown: reconnecting never waits for an answer, so a board still stuck after one would otherwise read as never asked.
func (a *radioActivity) keepReply(stats modem.StatsProvider) {
	lr, ok := stats.(interface{ LastReply() time.Time })
	if !ok {
		return
	}
	if t := lr.LastReply(); !t.IsZero() {
		if t.UnixNano() > a.lastReply.Load() {
			a.lastReply.Store(t.UnixNano())
		}
		a.silentSince.Store(0)
		return
	}
	if c, ok := stats.(interface{ ConnectedAt() time.Time }); ok && !c.ConnectedAt().IsZero() {
		a.silentSince.CompareAndSwap(0, c.ConnectedAt().UnixNano())
	}
}

// answered is the board's last answer, kept across reconnects, and when its silence began: that answer, else when the first unanswered link connected, since a board hung before its first answer is as stuck as one that went quiet. ok is false where the transport cannot be asked.
func (a *radioActivity) answered(stats modem.StatsProvider) (last, silentFrom time.Time, ok bool) {
	lr, ok := stats.(interface{ LastReply() time.Time })
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	last = lr.LastReply()
	if kept := a.lastReply.Load(); kept != 0 && (last.IsZero() || kept > last.UnixNano()) {
		last = time.Unix(0, kept)
	}
	silentFrom = last
	if s := a.silentSince.Load(); silentFrom.IsZero() && s != 0 {
		silentFrom = time.Unix(0, s)
	}
	if c, ok := stats.(interface{ ConnectedAt() time.Time }); ok && silentFrom.IsZero() {
		silentFrom = c.ConnectedAt()
	}
	return last, silentFrom, true
}

// radioSeen is written by the packet logger, which sees every frame in both directions.
var radioSeen radioActivity

// secsSinceTime tests IsZero, not UnixNano: a zero time.Time's nanos are a large negative number, not 0.
func secsSinceTime(t, now time.Time) *int64 {
	if t.IsZero() {
		return nil
	}
	secs := int64(now.Sub(t).Seconds())
	return &secs
}

// secsSince is nil where there is nothing to measure from, so "silent" and "cannot be asked" stay distinct.
func secsSince(nanos int64, now time.Time) *int64 {
	if nanos == 0 {
		return nil
	}
	secs := int64(now.Sub(time.Unix(0, nanos)).Seconds())
	return &secs
}

func (b *backend) Health() api.HealthInfo {
	brokers, _ := b.MqttStatus()
	return b.health(time.Now(), &radioSeen, brokers)
}

// health takes now, act and the brokers so a test can drive them without a clock, a radio or an observer.
func (b *backend) health(now time.Time, act *radioActivity, brokers []api.MqttBrokerStatus) api.HealthInfo {
	info := api.HealthInfo{
		Problems: []string{},
		Radio:    b.radioHealth(now, act),
		Brokers:  []api.BrokerHealth{},
	}

	if b.db != nil {
		queued, capacity, dropped, lastDrop := b.db.WriterStats()
		info.Database = api.DatabaseHealth{
			WriteQueueLen: queued, WriteQueueCap: capacity, WritesDropped: dropped,
			WritesDroppedLastSecs: secsSinceTime(lastDrop, now),
			WALBytes:              b.db.WALBytes(),
		}
		// Not a problem entry: the count never resets, so one transient overflow would pin "degraded" until restart.
		if free, ok := b.db.DiskFreeBytes(); ok {
			info.Database.DiskFreeBytes = &free
			if free < diskLowBelow {
				info.Problems = append(info.Problems, "database: disk almost full")
			}
		}
	}

	for _, br := range brokers {
		info.Brokers = append(info.Brokers, brokerHealth(br, now))
	}
	info.Mqtt = mqttHealth(brokers)

	var startErr error
	if p := act.startErr.Load(); p != nil {
		startErr = *p
	}
	info.Problems = append(info.Problems, radioProblems(info.Radio, startErr)...)
	if len(b.companions) == 0 && b.repeater == nil {
		info.Problems = append(info.Problems, "no companion or repeater node is running")
	}

	return info
}

// radioProblems names what stops the radio working; a stack that failed to start says which part, since the modem may be fine.
func radioProblems(r api.RadioHealth, startErr error) []string {
	switch {
	case errors.Is(startErr, errCompanionStart):
		return []string{"radio: a companion failed to start"}
	case errors.Is(startErr, errRepeaterStart):
		return []string{"radio: the repeater failed to start"}
	case !r.Connected:
		return []string{"radio: modem not connected"}
	}
	var out []string
	if s := r.LastReplySecs; s != nil && time.Duration(*s)*time.Second > modem.AnswerDeadline() {
		out = append(out, "radio: board not answering")
	}
	if s := r.TxFailingSecs; s != nil && r.TxFailedInARow >= txFailingMin && time.Duration(*s)*time.Second >= txFailingAfter {
		out = append(out, "radio: transmit failing")
	}
	return out
}

// mqttHealth judges the brokers apart from the node, as a monitor paging on the radio should not page on an upload; only an enabled broker counts.
func mqttHealth(brokers []api.MqttBrokerStatus) api.MqttHealth {
	h := api.MqttHealth{Status: "off", Problems: []string{}}
	for _, br := range brokers {
		if !br.Enabled {
			continue
		}
		h.Status = "ok"
		if !br.Connected {
			h.Problems = append(h.Problems, "broker "+br.Name+" is not connected")
		}
	}
	if len(h.Problems) > 0 {
		h.Status = "degraded"
	}
	return h
}

// brokerHealth maps one broker's status onto the wire shape, turning both timestamps into ages.
func brokerHealth(st api.MqttBrokerStatus, now time.Time) api.BrokerHealth {
	h := api.BrokerHealth{
		Name: st.Name, Enabled: st.Enabled, Connected: st.Connected,
		LastErrorSecs: secsSinceUnix(st.LastErrorTs, now),
		Published:     st.Published, Dropped: st.Dropped,
	}
	// The timestamp survives a disconnect; an age from it would claim hours of uptime while down.
	if st.Connected {
		h.ConnectedSecs = secsSinceUnix(st.ConnectedTs, now)
	}
	return h
}

// secsSinceUnix is secsSince for the unix-second timestamps the MQTT status carries; 0 means unset.
func secsSinceUnix(ts int64, now time.Time) *int64 {
	if ts == 0 {
		return nil
	}
	secs := int64(now.Sub(time.Unix(ts, 0)).Seconds())
	return &secs
}

func (b *backend) radioHealth(now time.Time, act *radioActivity) api.RadioHealth {
	h := api.RadioHealth{
		LastRxSecs: secsSince(act.lastRx.Load(), now),
		LastTxSecs: secsSince(act.lastTx.Load(), now),
	}

	// The liveness probe's own signal; null on a transport that cannot be probed, rather than claiming silence.
	if _, silentFrom, ok := act.answered(b.stats); ok {
		h.LastReplySecs = secsSinceTime(silentFrom, now)
	}

	// false: a monitor scrapes on a schedule, and polling the board per scrape puts traffic on the link.
	stats, ok := b.radioStats(false)
	if !ok {
		return h // no modem: everything below would be a zero that reads as healthy
	}
	// A modem exists. On a transport that reconnects itself the object outlives the link, so ask it
	// whether the link is actually up; the others are torn down and rebuilt, where existing is up.
	h.Connected = true
	if c, ok := b.stats.(interface{ Connected() bool }); ok {
		h.Connected = c.Connected()
	}
	h.Transport = stats.Transport
	h.TxQueueLen = stats.TxQueueLen
	h.TxSent = stats.TxSent
	h.TxFailed = stats.TxFailed
	h.TxDroppedBusy = stats.TxDroppedBusy
	h.TxDroppedQueue = stats.TxDroppedQueue
	if b.mux != nil {
		tx := b.mux.TxStats()
		h.TxFailedInARow = tx.FailedInARow
		h.TxFailingSecs = secsSinceTime(tx.FailingSince, now)
	}
	h.InboundDroppedNew = stats.InboundDroppedNew
	h.HandlerSlow = stats.HandlerSlow
	h.CRCErrors = stats.CRCErrors
	h.RecvErrors = stats.RecvErrors
	h.DriverErrors = stats.DriverErrors
	h.HwErrors = stats.HwErrors
	h.NoiseFloor = stats.NoiseFloor
	h.BatteryMV = stats.BatteryMV
	h.MCUTempC = stats.MCUTempC
	return h
}
