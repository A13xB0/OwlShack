package appserver

import (
	"net"
	"sync"
	"time"

	"github.com/meshcore-go/meshcore-go/companion"
)

// outQueue bounds frames waiting to reach an app; one that stops reading for this long is dropped rather than left to stall the radio.
const outQueue = 1024

// session is one app connection, with the state the firmware keeps per connection.
type session struct {
	port   *port
	conn   net.Conn
	remote string
	since  time.Time

	out       chan []byte
	done      chan struct{}
	closeOnce sync.Once

	mu        sync.Mutex
	name      string
	targetVer byte
	scope     Scope
	signing   bool
	signData  []byte
}

func newSession(p *port, conn net.Conn) *session {
	return &session{
		port: p, conn: conn, remote: conn.RemoteAddr().String(), since: time.Now(),
		out: make(chan []byte, outQueue), done: make(chan struct{}),
	}
}

func (s *session) start() {
	go s.writeLoop()
	go s.readLoop()
}

func (s *session) appName() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.name
}

// send queues a frame for the app; an app too far behind to take it is disconnected.
func (s *session) send(frame []byte) {
	select {
	case <-s.done:
	case s.out <- frame:
	default:
		s.port.log.Warn("app is not reading its frames, disconnecting it", "client", s.remote)
		s.close()
	}
}

func (s *session) close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.conn.Close()
		s.port.sessionEnded(s)
	})
}

func (s *session) writeLoop() {
	for {
		select {
		case <-s.done:
			return
		case frame := <-s.out:
			raw, err := companion.FrameEncode(companion.FrameTypeIncoming, frame)
			if err != nil {
				s.port.log.Error("dropping a frame too large to send", "code", frame[0], "error", err)
				continue
			}
			if _, err := s.conn.Write(raw); err != nil {
				s.close()
				return
			}
		}
	}
}

func (s *session) readLoop() {
	defer func() {
		s.close()
		s.port.log.Info("app disconnected", "client", s.remote)
	}()
	parser := companion.NewFrameParser()
	buf := make([]byte, 512)
	for {
		n, err := s.conn.Read(buf)
		if err != nil {
			return
		}
		for _, f := range parser.Feed(buf[:n]) {
			if f.Type != companion.FrameTypeOutgoing {
				continue
			}
			s.handle(f.Data)
		}
	}
}
