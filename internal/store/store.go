package store

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

// writerQueueDepth bounds pending write closures; the RX goroutine drops on a full queue rather than stalling.
const writerQueueDepth = 1024

type Store struct {
	db             *sql.DB
	Peers          *PeerRepo
	Contacts       *ContactRepo
	Packets        *PacketRepo
	Messages       *MessageRepo
	Conversations  *ConversationRepo
	Echoes         *EchoRepo
	BlockedSenders *BlockedSenderRepo
	Metrics        *MetricsRepo
	AppConfig      *AppConfigRepo
	Settings       *SettingsRepo
	Sensors        *SensorRepo
	TelemetryMap   *TelemetryMapRepo
	Mqtt           *MqttRepo
	Brokers        *BrokerRepo
	Companions     *CompanionRepo
	Channels       *ChannelRepo
	Triggers       *TriggerRepo
	SignalTests    *SignalTestRepo
	LinkMonitors   *LinkMonitorRepo
	Repeater       *RepeaterRepo
	RepeaterACL    *RepeaterACLRepo

	path       string
	writerCh   chan func()
	writerDone chan struct{}
	closing    chan struct{}
	closeOnce  sync.Once
	dropped    atomic.Uint64
	// lastDrop is when the most recent write was dropped, in unix nanos; 0 means never.
	lastDrop atomic.Int64
}

// walSizeLimit is what a checkpoint trims the WAL back to; without it the file stays the size of the largest burst ever written.
const walSizeLimit = 4 << 20

func Open(ctx context.Context, path string) (*Store, error) {
	// modernc.org/sqlite only honours the "_pragma=" form; the mattn-style "_journal_mode=WAL" is silently ignored.
	// NORMAL syncs at checkpoints rather than every commit, sparing the SD card; WriteSync checkpoints so a save is on disk when it returns.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)" +
		fmt.Sprintf("&_pragma=journal_size_limit(%d)&_pragma=synchronous(NORMAL)", walSizeLimit)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("pinging database: %w", err)
	}

	s := &Store{
		db:             db,
		path:           path,
		Peers:          &PeerRepo{db: db},
		Contacts:       &ContactRepo{db: db},
		Packets:        &PacketRepo{db: db, maxRows: DefaultMaxPackets},
		Messages:       &MessageRepo{db: db, maxRows: DefaultMaxMessages},
		Conversations:  &ConversationRepo{db: db},
		Echoes:         &EchoRepo{db: db},
		BlockedSenders: &BlockedSenderRepo{db: db},
		Metrics:        &MetricsRepo{db: db},
		AppConfig:      &AppConfigRepo{db: db},
		Settings:       &SettingsRepo{db: db},
		Sensors:        &SensorRepo{db: db},
		TelemetryMap:   &TelemetryMapRepo{db: db},
		Mqtt:           &MqttRepo{db: db},
		Brokers:        &BrokerRepo{db: db},
		Companions:     &CompanionRepo{db: db},
		Channels:       &ChannelRepo{db: db},
		Triggers:       &TriggerRepo{db: db},
		SignalTests:    &SignalTestRepo{db: db},
		LinkMonitors:   &LinkMonitorRepo{db: db},
		Repeater:       &RepeaterRepo{db: db},
		RepeaterACL:    &RepeaterACLRepo{db: db},
		writerCh:       make(chan func(), writerQueueDepth),
		writerDone:     make(chan struct{}),
		closing:        make(chan struct{}),
	}

	if err := s.migrate(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("running migrations: %w", err)
	}

	go s.writerLoop()

	return s, nil
}

// WriterStats reports the write queue and its drops; lastDrop is zero when none, which the ever-rising count alone cannot say.
func (s *Store) WriterStats() (queued, capacity int, dropped uint64, lastDrop time.Time) {
	if nanos := s.lastDrop.Load(); nanos != 0 {
		lastDrop = time.Unix(0, nanos)
	}
	return len(s.writerCh), cap(s.writerCh), s.dropped.Load(), lastDrop
}

// WriteAsync queues fn on the writer goroutine and never blocks; false means the queue was full and fn was dropped.
func (s *Store) WriteAsync(fn func()) bool {
	if s.closed() {
		return false
	}
	select {
	case s.writerCh <- fn:
		return true
	default:
		s.lastDrop.Store(time.Now().UnixNano())
		dropped := s.dropped.Add(1)
		if dropped == 1 || dropped%100 == 0 {
			slog.Warn("store writer queue full, dropping write", "dropped", dropped)
		}
		return false
	}
}

// WriteSync runs fn on the writer goroutine and blocks; calling it from the RX dispatch thread or inside another writer closure deadlocks.
func (s *Store) WriteSync(fn func()) {
	done := make(chan struct{})
	select {
	case s.writerCh <- func() {
		defer close(done)
		fn()
		s.checkpoint()
	}:
	case <-s.closing:
		s.writeAfterClose(fn)
		return
	}
	select {
	case <-done:
	case <-s.closing:
		<-s.writerDone
		select {
		case <-done:
		default:
			s.writeAfterClose(fn)
		}
	}
}

// writeAfterClose runs fn on the caller once the writer stops; skipping it leaves the caller's error nil, which reads as success.
func (s *Store) writeAfterClose(fn func()) {
	<-s.writerDone
	fn()
}

// checkpoint syncs the WAL and copies it into the main file, which NORMAL otherwise leaves until the WAL fills.
// ponytail: PASSIVE never waits on a reader, so one holding an older snapshot can leave a save in the WAL until the next checkpoint.
func (s *Store) checkpoint() {
	if _, err := s.db.ExecContext(context.Background(), "PRAGMA wal_checkpoint(PASSIVE)"); err != nil {
		slog.Warn("store checkpoint failed; the last save may not survive a power cut", "error", err)
	}
}

// WALBytes is the write-ahead log's size on disk, 0 when there is none; one that keeps growing means checkpoints are not completing.
func (s *Store) WALBytes() int64 {
	fi, err := os.Stat(s.path + "-wal")
	if err != nil {
		return 0
	}
	return fi.Size()
}

func (s *Store) closed() bool {
	select {
	case <-s.closing:
		return true
	default:
		return false
	}
}

func (s *Store) QueueLen() int {
	return len(s.writerCh)
}

func (s *Store) Dropped() uint64 {
	return s.dropped.Load()
}

// writerLoop runs queued closures until Close, then drains the queue; writerCh is never closed (many senders).
func (s *Store) writerLoop() {
	defer close(s.writerDone)
	for {
		select {
		case <-s.closing:
			for {
				select {
				case fn := <-s.writerCh:
					fn()
				default:
					return
				}
			}
		case fn := <-s.writerCh:
			fn()
		}
	}
}

func (s *Store) Close() error {
	s.closeOnce.Do(func() {
		close(s.closing)
		<-s.writerDone
	})
	return s.db.Close()
}
