package api

import (
	"context"
	"encoding/json"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/meshcore-go/OwlShack/internal/store"
)

func init() {
	// Go's mime table has no ".webmanifest" entry, so the manifest would otherwise be sniffed as text/plain.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

type CompanionInfo struct {
	ID        int64         `json:"id"`
	Name      string        `json:"name"`
	PubKey    string        `json:"pubkey"`
	PeerCount int           `json:"peerCount"`
	Lat       *float64      `json:"lat,omitempty"`
	Lon       *float64      `json:"lon,omitempty"`
	Channels  []ChannelInfo `json:"channels,omitempty"`
}

type CompanionProvider func() []CompanionInfo

type MessageSender func(channelName, text string) error
type DMSender func(pubkeyHex, text string) error

type ChannelAdder func(name, privateKey string) error
type ChannelRemover func(name string) error
type ChannelRenamerFunc func(companionName, oldName, newName string) error

type CompanionLookup func(name string) (MessageSender, DMSender, bool)
type CompanionChannelMutator func(name string) (adder ChannelAdder, remover ChannelRemover, ok bool)

type RepeaterOps struct {
	Login     func(pubkeyHex, password string) (any, error)
	RoomLogin func(pubkeyHex, password string, syncSince uint32) (any, error)
	StatusReq func(pubkeyHex string) (any, error)
	CLI       func(pubkeyHex, command string) (string, error)
	Session   func(pubkeyHex string) any
	Logout    func(pubkeyHex string)
	PathGet   func(pubkeyHex string) (any, error)
	PathReset func(pubkeyHex string) error
	// PathSet takes a nil path to flood and an empty one for a direct neighbour.
	PathSet      func(pubkeyHex string, path []byte, pathHashSize uint8) error
	NeighborsReq func(pubkeyHex string, count uint8, offset uint16) (any, error)
	OwnerInfoReq func(pubkeyHex string) (any, error)
	TelemetryReq func(pubkeyHex string) (any, error)
	// RoomStatusReq is StatusReq for a room server (ServerStats trailer).
	RoomStatusReq func(pubkeyHex string) (any, error)
	// RoomKeepAlive sends REQ_TYPE_KEEP_ALIVE (direct only); since 0 means the room's stored cursor.
	RoomKeepAlive func(pubkeyHex string, since uint32) error
	// SeriesReq is the sensor min/max/avg history (GET_AVG_MIN_MAX); bounds are seconds before now, start older.
	SeriesReq func(pubkeyHex string, startSecsAgo, endSecsAgo uint32) (any, error)
	// ContactTelemetryReq uses the ECDH secret with the contact, not a login session.
	ContactTelemetryReq func(pubkeyHex string) (any, error)
	AccessList          func(pubkeyHex string) (any, error)
	SetPerm             func(pubkeyHex, targetPubkeyHex string, perms uint8) error
}

type Server struct {
	store     *store.Store
	hub       *Hub
	mux       *http.ServeMux
	log       *slog.Logger
	assets    fs.FS
	poller    NodePoller
	sigTester SignalTester

	startedAt time.Time

	mu      sync.RWMutex
	backend Backend
}

func NewServer(st *store.Store, assets fs.FS, log *slog.Logger) *Server {
	s := &Server{
		store:     st,
		hub:       NewHub(),
		mux:       http.NewServeMux(),
		log:       log,
		assets:    assets,
		startedAt: time.Now(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /api/peers", s.handleListPeers)
	s.mux.HandleFunc("POST /api/peers/delete", s.handleBulkDeletePeers)
	s.mux.HandleFunc("DELETE /api/peers/{pubkey}", s.handleDeletePeer)
	s.mux.HandleFunc("GET /api/packets", s.handleListPackets)
	s.mux.HandleFunc("GET /api/companions", s.handleListCompanions)
	s.mux.HandleFunc("GET /api/companions/{name}/contacts", s.handleListContacts)
	s.mux.HandleFunc("GET /api/companions/{name}/contacts/{pubkey}", s.handleGetContact)
	s.mux.HandleFunc("POST /api/companions/{name}/contacts", s.handleAddContact)
	s.mux.HandleFunc("DELETE /api/companions/{name}/contacts/{pubkey}", s.handleDeleteContact)
	s.mux.HandleFunc("PATCH /api/companions/{name}/contacts/{pubkey}", s.handleUpdateContactMetadata)
	s.mux.HandleFunc("PUT /api/companions/{name}/contacts/{pubkey}/location", s.handleSetContactLocation)
	s.mux.HandleFunc("GET /api/companions/{name}/contacts/{pubkey}/telemetry", s.handleContactTelemetry)
	// Per-resource config REST (reads; scoped + secret-redacted).
	s.mux.HandleFunc("GET /api/config/settings", s.handleGetSettings)
	s.mux.HandleFunc("GET /api/config/mqtt", s.handleGetMqtt)
	s.mux.HandleFunc("GET /api/config/mqtt/brokers", s.handleGetBrokers)
	s.mux.HandleFunc("GET /api/mqtt/status", s.handleMqttStatus)
	s.mux.HandleFunc("GET /api/discover", s.handleDiscoveryState)
	s.mux.HandleFunc("POST /api/discover", s.handleStartDiscovery)
	s.mux.HandleFunc("GET /api/spi/boards", s.handleSPIBoards)
	s.mux.HandleFunc("GET /api/serial/ports", s.handleSerialPorts)
	s.mux.HandleFunc("GET /api/radio/status", s.handleRadioStatus)
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("POST /api/radio/reset", s.handleRadioReset)
	s.mux.HandleFunc("GET /api/sensors", s.handleSensors)
	s.mux.HandleFunc("GET /api/sensors/providers", s.handleSensorProviders)
	s.mux.HandleFunc("POST /api/sensors/discover", s.handleSensorDiscover)
	s.mux.HandleFunc("GET /api/sensors/kinds", s.handleSensorKinds)
	s.mux.HandleFunc("POST /api/sensors", s.handleCreateSensor)
	s.mux.HandleFunc("POST /api/sensors/test", s.handleTestSensor)
	s.mux.HandleFunc("PUT /api/sensors/{id}", s.handleUpdateSensor)
	s.mux.HandleFunc("DELETE /api/sensors/{id}", s.handleDeleteSensor)
	s.mux.HandleFunc("GET /api/sensors/telemetry-map", s.handleTelemetryMap)
	s.mux.HandleFunc("PUT /api/sensors/telemetry-map", s.handleSetTelemetryMap)

	s.mux.HandleFunc("POST /api/backup", s.handleBackupExport)
	s.mux.HandleFunc("POST /api/backup/estimate", s.handleBackupEstimate)
	s.mux.HandleFunc("POST /api/backup/import", s.handleBackupImport)
	s.mux.HandleFunc("GET /api/config/companions", s.handleGetCompanions)
	s.mux.HandleFunc("GET /api/config/companions/{id}/channels", s.handleGetCompanionChannels)
	s.mux.HandleFunc("GET /api/config/channels", s.handleGetAllChannels)
	s.mux.HandleFunc("GET /api/config/triggers", s.handleGetTriggers)
	s.mux.HandleFunc("GET /api/config/repeater", s.handleGetRepeater)
	// Per-resource config REST (writes; validated + reloaded server-side).
	s.mux.HandleFunc("PUT /api/config/settings", s.handlePutSettings)
	s.mux.HandleFunc("PUT /api/config/mqtt", s.handlePutMqtt)
	s.mux.HandleFunc("POST /api/config/mqtt/brokers", s.handleSaveBroker)
	s.mux.HandleFunc("PUT /api/config/mqtt/brokers/{id}", s.handleSaveBroker)
	s.mux.HandleFunc("DELETE /api/config/mqtt/brokers/{id}", s.handleDeleteBroker)
	s.mux.HandleFunc("POST /api/config/companions", s.handleSaveCompanion)
	s.mux.HandleFunc("PUT /api/config/companions/{id}", s.handleSaveCompanion)
	s.mux.HandleFunc("PUT /api/config/companions/{id}/telemetry", s.handleSetCompanionTelemetry)
	s.mux.HandleFunc("DELETE /api/config/companions/{id}", s.handleDeleteCompanion)
	s.mux.HandleFunc("POST /api/config/companions/{id}/channels", s.handleCreateChannel)
	s.mux.HandleFunc("PUT /api/config/channels/{id}", s.handleSaveChannel)
	s.mux.HandleFunc("DELETE /api/config/channels/{id}", s.handleDeleteChannel)
	s.mux.HandleFunc("POST /api/config/triggers", s.handleSaveTrigger)
	s.mux.HandleFunc("GET /api/regions/at", s.handleRegionAt)
	s.mux.HandleFunc("GET /api/regions/{id}", s.handleGetRegion)
	s.mux.HandleFunc("POST /api/config/triggers/test/items", s.handleTestTriggerItems)
	s.mux.HandleFunc("POST /api/config/triggers/test/render", s.handleTestTriggerRender)
	s.mux.HandleFunc("PUT /api/config/triggers/{id}", s.handleSaveTrigger)
	s.mux.HandleFunc("DELETE /api/config/triggers/{id}", s.handleDeleteTrigger)
	// Repeater node config is edited per-section (no whole-config bulk PUT).
	s.mux.HandleFunc("POST /api/config/repeater", s.handleCreateRepeater)
	s.mux.HandleFunc("DELETE /api/config/repeater", s.handleDeleteRepeater)
	s.mux.HandleFunc("PUT /api/config/repeater/node", s.handleUpdateRepeaterNode)
	s.mux.HandleFunc("PUT /api/config/repeater/relay", s.handleUpdateRepeaterRelay)
	s.mux.HandleFunc("PUT /api/config/repeater/admin", s.handleUpdateRepeaterAdmin)
	s.mux.HandleFunc("POST /api/config/repeater/regions", s.handleAddRepeaterRegion)
	s.mux.HandleFunc("PATCH /api/config/repeater/regions/{name}", s.handlePatchRepeaterRegion)
	s.mux.HandleFunc("DELETE /api/config/repeater/regions/{name}", s.handleDeleteRepeaterRegion)
	s.mux.HandleFunc("GET /api/companions/{name}/channels", s.handleListChannels)
	s.mux.HandleFunc("POST /api/companions/{name}/channels", s.handleAddChannel)
	s.mux.HandleFunc("DELETE /api/companions/{name}/channels/{channel}", s.handleRemoveChannel)
	s.mux.HandleFunc("GET /api/companions/{name}/messages", s.handleListMessages)
	s.mux.HandleFunc("POST /api/companions/{name}/messages", s.handleSendMessage)
	s.mux.HandleFunc("GET /api/companions/{name}/conversations", s.handleListConversations)
	s.mux.HandleFunc("POST /api/companions/{name}/conversations/{conversationId}/read", s.handleMarkRead)
	s.mux.HandleFunc("GET /api/messages/{messageId}/echoes", s.handleListEchoes)
	s.mux.HandleFunc("GET /api/companions/{name}/messages/{messageId}/path", s.handleGetMessagePath)
	s.mux.HandleFunc("DELETE /api/messages/{messageId}", s.handleDeleteMessage)
	s.mux.HandleFunc("POST /api/companions/{name}/messages/{messageId}/retry", s.handleRetryMessage)
	s.mux.HandleFunc("POST /api/companions/{name}/conversations/{conversationId}/block", s.handleBlockSender)
	s.mux.HandleFunc("DELETE /api/companions/{name}/conversations/{conversationId}/block/{sender}", s.handleUnblockSender)
	s.mux.HandleFunc("GET /api/companions/{name}/conversations/{conversationId}/block", s.handleListBlockedSenders)
	s.mux.HandleFunc("DELETE /api/companions/{name}/conversations/{conversationId}/messages", s.handleDeleteConversationMessages)
	s.mux.HandleFunc("GET /api/companions/{name}/conversations/{conversationId}/participants", s.handleListParticipants)
	s.mux.HandleFunc("PATCH /api/companions/{name}/channels/{channel}", s.handleRenameChannel)
	s.mux.HandleFunc("GET /api/companions/{name}/channels/{channel}/key", s.handleGetChannelKey)
	s.mux.HandleFunc("GET /api/companions/{name}/contacts/{pubkey}/path", s.handleGetContactPath)
	s.mux.HandleFunc("DELETE /api/companions/{name}/contacts/{pubkey}/path", s.handleResetContactPath)
	s.mux.HandleFunc("PUT /api/companions/{name}/contacts/{pubkey}/path", s.handleRepeaterPathSet)
	s.mux.HandleFunc("POST /api/companions/{name}/trace", s.handleSendTrace)
	s.mux.HandleFunc("POST /api/companions/{name}/advert", s.handleSendAdvert)
	s.mux.HandleFunc("POST /api/companions/{name}/repeaters/{pubkey}/login", s.handleRepeaterLogin)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/status", s.handleRepeaterStatus)
	s.mux.HandleFunc("POST /api/companions/{name}/repeaters/{pubkey}/cli", s.handleRepeaterCLI)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/session", s.handleRepeaterSession)
	s.mux.HandleFunc("DELETE /api/companions/{name}/repeaters/{pubkey}/session", s.handleRepeaterLogout)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/path", s.handleRepeaterPathGet)
	s.mux.HandleFunc("DELETE /api/companions/{name}/repeaters/{pubkey}/path", s.handleRepeaterPathReset)
	s.mux.HandleFunc("PUT /api/companions/{name}/repeaters/{pubkey}/path", s.handleRepeaterPathSet)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/neighbors", s.handleRepeaterNeighbors)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/owner", s.handleRepeaterOwnerInfo)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/telemetry", s.handleRepeaterTelemetry)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/history", s.handleRepeaterSeries)
	s.mux.HandleFunc("GET /api/companions/{name}/repeaters/{pubkey}/access", s.handleRepeaterAccessList)
	s.mux.HandleFunc("PUT /api/companions/{name}/repeaters/{pubkey}/access/{target}", s.handleRepeaterAccessSet)
	s.mux.HandleFunc("DELETE /api/companions/{name}/repeaters/{pubkey}/access/{target}", s.handleRepeaterAccessRemove)
	s.mux.HandleFunc("POST /api/companions/{name}/rooms/{pubkey}/login", s.handleRoomLogin)
	s.mux.HandleFunc("GET /api/companions/{name}/rooms/{pubkey}/session", s.handleRepeaterSession)
	s.mux.HandleFunc("DELETE /api/companions/{name}/rooms/{pubkey}/session", s.handleRepeaterLogout)
	s.mux.HandleFunc("GET /api/companions/{name}/rooms/{pubkey}/status", s.handleRoomStatus)
	s.mux.HandleFunc("POST /api/companions/{name}/rooms/{pubkey}/keepalive", s.handleRoomKeepAlive)
	s.mux.HandleFunc("GET /api/repeater/status", s.handleRepeaterNodeStatus)
	s.mux.HandleFunc("GET /api/repeater/neighbors", s.handleRepeaterNodeNeighbors)
	s.mux.HandleFunc("GET /api/repeater/acl", s.handleRepeaterNodeACL)
	s.mux.HandleFunc("DELETE /api/repeater/acl/{pubkey}", s.handleRepeaterNodeRevoke)
	s.mux.HandleFunc("POST /api/repeater/advert", s.handleRepeaterNodeAdvert)
	s.mux.HandleFunc("POST /api/repeater/discover", s.handleRepeaterNodeDiscover)
	s.mux.HandleFunc("PUT /api/repeater/acl/{pubkey}", s.handleRepeaterNodeSetACL)
	s.mux.HandleFunc("DELETE /api/repeater/stats", s.handleRepeaterNodeClearStats)
	s.mux.HandleFunc("GET /api/nodes/monitored", s.handleListMonitoredNodes)
	s.mux.HandleFunc("GET /api/nodes/neighbor-links", s.handleListNeighborLinks)
	s.mux.HandleFunc("GET /api/nodes/{pubkey}/metrics", s.handleListNodeMetricNames)
	s.mux.HandleFunc("GET /api/nodes/{pubkey}/history", s.handleNodeHistory)
	s.mux.HandleFunc("POST /api/nodes/{pubkey}/poll", s.handlePollNode)
	s.mux.HandleFunc("POST /api/companions/{name}/signal-tests", s.handleStartSignalTest)
	s.mux.HandleFunc("GET /api/signal-tests", s.handleListSignalTests)
	s.mux.HandleFunc("GET /api/signal-tests/{id}", s.handleGetSignalTest)
	s.mux.HandleFunc("POST /api/signal-tests/{id}/cancel", s.handleCancelSignalTest)
	s.mux.HandleFunc("PATCH /api/signal-tests/{id}", s.handleUpdateSignalTest)
	s.mux.HandleFunc("DELETE /api/signal-tests/{id}", s.handleDeleteSignalTest)
	s.mux.HandleFunc("GET /api/links", s.handleListLinks)
	s.mux.HandleFunc("POST /api/companions/{name}/links", s.handleAddLink)
	s.mux.HandleFunc("PATCH /api/links/{id}", s.handleUpdateLink)
	s.mux.HandleFunc("DELETE /api/links/{id}", s.handleDeleteLink)
	s.mux.HandleFunc("GET /api/ws", s.handleWebSocket)

	s.mux.Handle("/", s.spaHandler())
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, s.resolveCompanionRef(r))
}

func (s *Server) Hub() *Hub {
	return s.hub
}

// SetBackend installs or swaps the domain backend while the server is serving requests.
func (s *Server) SetBackend(b Backend) {
	s.mu.Lock()
	s.backend = b
	s.mu.Unlock()
}

func (s *Server) backendRef() Backend {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.backend
}

// SetPoller installs the node poller after the HTTP server has already started.
func (s *Server) SetPoller(p NodePoller) {
	s.mu.Lock()
	s.poller = p
	s.mu.Unlock()
}

func (s *Server) pollerRef() NodePoller {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.poller
}

// SetSignalTester installs the signal-test runner after the HTTP server has already started.
func (s *Server) SetSignalTester(t SignalTester) {
	s.mu.Lock()
	s.sigTester = t
	s.mu.Unlock()
}

func (s *Server) signalTesterRef() SignalTester {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sigTester
}

// The accessors below return nil when no backend is wired yet; handlers turn that into 503/404.

func (s *Server) companionProvider() CompanionProvider {
	if b := s.backendRef(); b != nil {
		return b.Companions
	}
	return nil
}

func (s *Server) ChannelLookup() ChannelLookup {
	if b := s.backendRef(); b != nil {
		return b.ChannelByHash
	}
	return nil
}

// peerAdder is nil without a backend; the contact row still persists and hydration picks it up on restart.
func (s *Server) peerAdder() func([]byte, string, string) {
	if b := s.backendRef(); b != nil {
		return b.AddPeer
	}
	return nil
}

// peerRemover is nil without a backend; the DB delete still runs, in-memory cleanup is best-effort.
func (s *Server) peerRemover() func([][]byte) {
	if b := s.backendRef(); b != nil {
		return b.RemovePeers
	}
	return nil
}

func (s *Server) CompanionLookup() CompanionLookup {
	if b := s.backendRef(); b != nil {
		return b.Companion
	}
	return nil
}

func (s *Server) ChannelMutator() CompanionChannelMutator {
	if b := s.backendRef(); b != nil {
		return b.ChannelMutator
	}
	return nil
}

func (s *Server) ChannelRenamer() ChannelRenamerFunc {
	if b := s.backendRef(); b != nil {
		return b.RenameChannel
	}
	return nil
}

func (s *Server) ConfigPersist() func(context.Context) error {
	if b := s.backendRef(); b != nil {
		return b.PersistChannels
	}
	return nil
}

func (s *Server) traceSenderLookup() TraceSenderLookup {
	if b := s.backendRef(); b != nil {
		return b.TraceSender
	}
	return nil
}

func (s *Server) advertSenderLookup() AdvertSenderLookup {
	if b := s.backendRef(); b != nil {
		return b.AdvertSender
	}
	return nil
}

func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServerFS(s.assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unmatched API paths must 404 rather than fall through to the SPA index.
		if strings.HasPrefix(r.URL.Path, "/api/") {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		// embed.FS files carry no Last-Modified/ETag, so anything but the hashed assets must be no-cache.
		if strings.HasPrefix(r.URL.Path, "/assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		f, err := s.assets.Open(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			r.URL.Path = "/"
			fileServer.ServeHTTP(w, r)
			return
		}
		f.Close()
		fileServer.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// serverError returns an opaque 500; the underlying error must never leak to API clients.
func (s *Server) serverError(w http.ResponseWriter, msg string, err error) {
	s.log.Error(msg, "error", err)
	writeError(w, http.StatusInternalServerError, msg)
}

// maxJSONBody is far past any real request: a bulk delete of 15,000 peers fits.
const maxJSONBody = 1 << 20

func readJSON(r *http.Request, v any) error {
	defer r.Body.Close()
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, maxJSONBody)).Decode(v)
}
