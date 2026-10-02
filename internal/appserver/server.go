package appserver

import (
	"log/slog"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/meshcore-go/meshcore-go/companion"
)

// Port is a companion that takes app connections, and the address it listens on.
type Port struct {
	CompanionID int64
	Addr        string
}

// PortStatus is how a port is doing, for the UI and the health check.
type PortStatus struct {
	CompanionID int64
	Addr        string
	// Error is why the port could not open, empty when it is listening.
	Error string
	// Client is the connected app's address, empty when none is connected.
	Client  string
	AppName string
	Since   time.Time
	// Replaced counts connections a newer one took over, which two apps fighting over one port drive up.
	Replaced int
}

// Server holds every companion's listener, session and offline queue across reloads.
type Server struct {
	log *slog.Logger

	mu    sync.Mutex
	ports map[int64]*port
}

func New(log *slog.Logger) *Server {
	return &Server{log: log, ports: make(map[int64]*port)}
}

// Apply opens the ports wanted, closes the rest, and points each at its companion's current device; a nil device leaves the port up, refusing commands.
func (s *Server) Apply(ports []Port, devices map[int64]Device) {
	s.mu.Lock()
	defer s.mu.Unlock()

	want := make(map[int64]string, len(ports))
	for _, p := range ports {
		want[p.CompanionID] = p.Addr
	}
	for id, p := range s.ports {
		if _, ok := want[id]; !ok {
			p.close()
			delete(s.ports, id)
		}
	}
	for id, addr := range want {
		p := s.ports[id]
		if p == nil {
			p = &port{companionID: id, log: s.log.With("component", "appserver", "companion", id)}
			s.ports[id] = p
		}
		p.listen(addr)
		p.setDevice(devices[id])
	}
}

// Status reports every port, ordered by companion.
func (s *Server) Status() []PortStatus {
	s.mu.Lock()
	ps := make([]*port, 0, len(s.ports))
	for _, p := range s.ports {
		ps = append(ps, p)
	}
	s.mu.Unlock()
	out := make([]PortStatus, 0, len(ps))
	for _, p := range ps {
		out = append(out, p.status())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CompanionID < out[j].CompanionID })
	return out
}

// Close shuts every port and session.
func (s *Server) Close() {
	s.Apply(nil, nil)
}

type port struct {
	companionID int64
	log         *slog.Logger
	queue       queue

	mu          sync.Mutex
	addr        string
	ln          net.Listener
	listenErr   error
	device      Device
	unsubscribe func()
	session     *session
	replaced    int
}

func (p *port) listen(addr string) {
	p.mu.Lock()
	// A port that failed to open tries again on every Apply, so freeing the address and saving fixes it.
	if p.addr == addr && p.ln != nil {
		p.mu.Unlock()
		return
	}
	old := p.ln
	p.ln, p.addr, p.listenErr = nil, addr, nil
	p.mu.Unlock()
	if old != nil {
		old.Close()
	}

	ln, err := net.Listen("tcp", addr)
	p.mu.Lock()
	defer p.mu.Unlock()
	if err != nil {
		p.listenErr = err
		p.log.Error("app port could not open", "addr", addr, "error", err)
		return
	}
	p.ln = ln
	p.log.Info("listening for companion apps", "addr", ln.Addr().String())
	go p.accept(ln)
}

func (p *port) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.ln != ln {
			p.mu.Unlock()
			conn.Close()
			return
		}
		old := p.session
		sess := newSession(p, conn)
		p.session = sess
		if old != nil {
			p.replaced++
		}
		p.mu.Unlock()
		if old != nil {
			p.log.Info("app connection replaced by a newer one", "old", old.remote, "new", sess.remote)
			old.close()
		} else {
			p.log.Info("app connected", "client", sess.remote)
		}
		sess.start()
	}
}

func (p *port) setDevice(d Device) {
	p.mu.Lock()
	if p.device == d {
		p.mu.Unlock()
		return
	}
	unsub := p.unsubscribe
	p.device, p.unsubscribe = d, nil
	p.mu.Unlock()
	if unsub != nil {
		unsub()
	}
	if d == nil {
		return
	}
	cancel := d.Subscribe(p.event)
	p.mu.Lock()
	if p.device == d {
		p.unsubscribe = cancel
		cancel = nil
	}
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (p *port) currentDevice() Device {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.device
}

func (p *port) currentSession() *session {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.session
}

// event is the device telling its app something: a message is queued whether or not an app is connected, a push only reaches a connected one.
func (p *port) event(ev Event) {
	sess := p.currentSession()
	if ev.Message != nil {
		p.queue.push(*ev.Message)
		if sess != nil {
			sess.send(companion.PushMsgWaitingResponse{}.ToBytes())
		}
	}
	if ev.Push != nil && sess != nil {
		if b, err := ev.Push.ToBytes(); err == nil {
			sess.send(b)
		}
	}
}

func (p *port) sessionEnded(s *session) {
	p.mu.Lock()
	if p.session == s {
		p.session = nil
	}
	p.mu.Unlock()
}

func (p *port) status() PortStatus {
	p.mu.Lock()
	defer p.mu.Unlock()
	st := PortStatus{CompanionID: p.companionID, Addr: p.addr, Replaced: p.replaced}
	if p.ln != nil {
		st.Addr = p.ln.Addr().String()
	}
	if p.listenErr != nil {
		st.Error = p.listenErr.Error()
	}
	if s := p.session; s != nil {
		st.Client, st.Since = s.remote, s.since
		st.AppName = s.appName()
	}
	return st
}

func (p *port) close() {
	p.mu.Lock()
	ln, sess, unsub := p.ln, p.session, p.unsubscribe
	p.ln, p.session, p.unsubscribe, p.device = nil, nil, nil, nil
	p.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
	if sess != nil {
		sess.close()
	}
	if unsub != nil {
		unsub()
	}
}
