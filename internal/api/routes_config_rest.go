package api

import (
	"net/http"
	"strconv"

	"github.com/meshcore-go/OwlShack/internal/store"
)

// Config REST reads never send a secret: each becomes a boolean "<field>Set".

// --- DTOs (JSON shapes) ---

type settingsDTO struct {
	LogLevel       *string  `json:"logLevel"`
	ConnectionType string   `json:"connectionType"`
	Connection     *string  `json:"connection"`
	BaudRate       *int     `json:"baudRate"`
	SPIBoard       *string  `json:"spiBoard"`
	Freq           *float64 `json:"freq"`
	BW             *float64 `json:"bw"`
	SF             *int     `json:"sf"`
	CR             *int     `json:"cr"`
	TX             *int     `json:"tx"`
	ListenAddr     *string  `json:"listenAddr"`
	MapProvider    string   `json:"mapProvider"`
	MapDarkStyle   string   `json:"mapDarkStyle"`
	MapTileKey     *string  `json:"mapTileKey"`    // sent to the browser by design: it rides on tile URLs
	ModemTokenSet  bool     `json:"modemTokenSet"` // redacted
	PathHashSize   *int     `json:"pathHashSize"`
	// DutyCycle is a percentage, the unit the firmware's `set dutycycle` takes; null is the default (50%).
	DutyCycle     *float64 `json:"dutyCycle"`
	SetupComplete bool     `json:"setupComplete"`
}

type mqttDTO struct {
	Enabled         *bool   `json:"enabled"`
	NodeCompanionID *int64  `json:"nodeCompanionId"`
	IataCode        *string `json:"iataCode"`
	StatusInterval  *int    `json:"statusInterval"`
	Owner           *string `json:"owner"`
	Email           *string `json:"email"`
}

type brokerDTO struct {
	ID                    int64    `json:"id"`
	Name                  string   `json:"name"`
	Enabled               bool     `json:"enabled"`
	Dedup                 bool     `json:"dedup"`
	Transport             string   `json:"transport"`
	Host                  string   `json:"host"`
	Port                  int      `json:"port"`
	PacketTopic           *string  `json:"packetTopic"`
	StatusTopic           *string  `json:"statusTopic"`
	DisallowedPacketTypes []string `json:"disallowedPacketTypes"`
	RetainStatus          bool     `json:"retainStatus"`
	TLSEnabled            bool     `json:"tlsEnabled"`
	TLSInsecure           bool     `json:"tlsInsecure"`
	AuthType              string   `json:"authType"`
	Username              string   `json:"username"`
	PasswordSet           bool     `json:"passwordSet"` // redacted
	Path                  string   `json:"path"`
	Audience              string   `json:"audience"`
}

type companionDTO struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	PubKey         string   `json:"pubkey"`
	PrivateKeySet  bool     `json:"privateKeySet"` // redacted
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	AdvertInterval *int     `json:"advertInterval"`
	PathHashSize   *int     `json:"pathHashSize"`
	DMPolicy       string   `json:"dmPolicy"`
	DMAllow        []string `json:"dmAllow"`
	// Telemetry* is who may read each class: "deny", "selected" or "contacts".
	TelemetryBase        string `json:"telemetryBase"`
	TelemetryLocation    string `json:"telemetryLocation"`
	TelemetryEnvironment string `json:"telemetryEnvironment"`
}

type channelDTO struct {
	ID            int64  `json:"id"`
	CompanionID   int64  `json:"companionId"`
	Name          string `json:"name"`
	PrivateKeySet bool   `json:"privateKeySet"` // redacted; fetch via the channel key endpoint
}

type triggerDTO struct {
	ID                 int64            `json:"id"`
	CompanionID        int64            `json:"companionId"`
	Type               string           `json:"type"`
	Template           string           `json:"template"`
	CharLimitBehaviour *string          `json:"charLimitBehaviour"`
	Match              []string         `json:"match"`
	Contacts           []string         `json:"contacts"`
	ChannelIDs         []int64          `json:"channelIds"`
	FailoverPattern    string           `json:"failoverPattern"`
	FailoverTimeout    int64            `json:"failoverTimeout"`
	RetryTimeout       *int64           `json:"retryTimeout"`
	MaxRetries         *int             `json:"maxRetries"`
	PathHashSize       *int             `json:"pathHashSize"`
	Schedule           *string          `json:"schedule"`
	URL                string           `json:"url"`
	Location           *TriggerLocation `json:"location"`
	Regions            *[]string        `json:"regions"`
}

func brokerToDTO(b store.Broker) brokerDTO {
	return brokerDTO{
		ID: b.ID, Name: b.Name, Enabled: b.Enabled, Dedup: b.Dedup, Transport: b.Transport,
		Host: b.Host, Port: b.Port, PacketTopic: b.PacketTopic, StatusTopic: b.StatusTopic,
		DisallowedPacketTypes: b.DisallowedPacketTypes, RetainStatus: b.RetainStatus,
		TLSEnabled: b.TLSEnabled, TLSInsecure: b.TLSInsecure, AuthType: b.AuthType,
		Username: b.Username, PasswordSet: b.Password != "", Path: b.Path, Audience: b.Audience,
	}
}

func companionToDTO(c store.Companion) companionDTO {
	return companionDTO{
		ID: c.ID, Name: c.Name, PubKey: c.PubKey, PrivateKeySet: c.PrivateKey != "",
		Latitude: c.Latitude, Longitude: c.Longitude, AdvertInterval: c.AdvertInterval,
		PathHashSize: c.PathHashSize,
		DMPolicy:     c.DMPolicy, DMAllow: c.DMAllow,
		TelemetryBase: c.TelemBase, TelemetryLocation: c.TelemLoc, TelemetryEnvironment: c.TelemEnv,
	}
}

func channelToDTO(c store.CompanionChannel) channelDTO {
	return channelDTO{ID: c.ID, CompanionID: c.CompanionID, Name: c.Name, PrivateKeySet: c.PrivateKey != ""}
}

func triggerToDTO(t store.Trigger) triggerDTO {
	return triggerDTO{
		ID: t.ID, CompanionID: t.CompanionID, Type: t.Type, Template: t.Template,
		CharLimitBehaviour: t.CharLimitBehaviour, Match: t.MatchPatterns, Contacts: t.Contacts,
		ChannelIDs: t.ChannelIDs, RetryTimeout: t.RetryTimeout, MaxRetries: t.MaxRetries,
		PathHashSize: t.PathHashSize, Schedule: t.Schedule, URL: t.URL,
		FailoverPattern: t.FailoverPattern, FailoverTimeout: t.FailoverTimeout,
		Location: locationToDTO(t.Location), Regions: t.Regions,
	}
}

func locationToDTO(l *store.TriggerLocation) *TriggerLocation {
	if l == nil {
		return nil
	}
	return &TriggerLocation{Lat: &l.Lat, Lon: &l.Lon, RadiusKm: &l.RadiusKm}
}

// --- handlers ---

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	st, err := s.store.Settings.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read settings")
		return
	}
	writeJSON(w, http.StatusOK, settingsDTO{
		LogLevel: st.LogLevel, ConnectionType: st.ConnectionType, Connection: st.Connection,
		BaudRate: st.BaudRate, SPIBoard: st.SPIBoard,
		Freq: st.Freq, BW: st.BW, SF: st.SF, CR: st.CR, TX: st.TX,
		ListenAddr: st.ListenAddr, MapProvider: st.MapProvider, MapDarkStyle: st.MapDarkStyle, MapTileKey: st.MapTileKey, PathHashSize: st.PathHashSize,
		ModemTokenSet: st.ModemToken != nil && *st.ModemToken != "",
		DutyCycle:     st.DutyCyclePct, SetupComplete: st.SetupComplete,
	})
}

func (s *Server) handleGetMqtt(w http.ResponseWriter, r *http.Request) {
	m, err := s.store.Mqtt.Get(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read mqtt settings")
		return
	}
	writeJSON(w, http.StatusOK, mqttDTO{
		Enabled: m.Enabled, NodeCompanionID: m.NodeCompanionID, IataCode: m.IataCode,
		StatusInterval: m.StatusInterval, Owner: m.Owner, Email: m.Email,
	})
}

func (s *Server) handleGetBrokers(w http.ResponseWriter, r *http.Request) {
	brokers, err := s.store.Brokers.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read brokers")
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(brokers, brokerToDTO))
}

func (s *Server) handleGetCompanions(w http.ResponseWriter, r *http.Request) {
	companions, err := s.store.Companions.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read companions")
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(companions, companionToDTO))
}

func (s *Server) handleGetCompanionChannels(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid companion id")
		return
	}
	chans, err := s.store.Channels.ListByCompanion(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read channels")
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(chans, channelToDTO))
}

// handleGetAllChannels lists every channel across all companions, each carrying its companionId.
func (s *Server) handleGetAllChannels(w http.ResponseWriter, r *http.Request) {
	chans, err := s.store.Channels.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read channels")
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(chans, channelToDTO))
}

// --- write handlers ---

func pathID(r *http.Request, name string) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil
}

// mapSlice returns a non-nil empty slice so the JSON encodes as [] rather than null.
func mapSlice[S, D any](in []S, f func(S) D) []D {
	out := make([]D, 0, len(in))
	for _, x := range in {
		out = append(out, f(x))
	}
	return out
}

// configBackend writes a 503 and returns ok=false when no backend is wired yet.
func (s *Server) configBackend(w http.ResponseWriter) (Backend, bool) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "config not available")
		return nil, false
	}
	return b, true
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in SettingsInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := b.SaveSettings(r.Context(), in); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handlePutMqtt(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in MqttInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := b.SaveMqtt(r.Context(), in); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSaveBroker(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in BrokerInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if id, ok := pathID(r, "id"); ok {
		in.ID = id
	}
	id, err := b.SaveBroker(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleDeleteBroker(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := b.DeleteBroker(r.Context(), id); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSaveCompanion(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in CompanionInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if id, ok := pathID(r, "id"); ok {
		in.ID = id
	}
	id, err := b.SaveCompanion(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleSetCompanionTelemetry(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in CompanionTelemetryInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if err := b.SetCompanionTelemetry(r.Context(), id, in); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteCompanion(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := b.DeleteCompanion(r.Context(), id); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	cid, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid companion id")
		return
	}
	var in ChannelInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	in.ID = 0
	in.CompanionID = cid
	id, err := b.SaveChannel(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleSaveChannel(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in ChannelInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	in.ID = id
	if _, err := b.SaveChannel(r.Context(), in); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := b.DeleteChannel(r.Context(), id); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSaveTrigger(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in TriggerInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if id, ok := pathID(r, "id"); ok {
		in.ID = id
	}
	id, err := b.SaveTrigger(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"id": id})
}

func (s *Server) handleTestTriggerItems(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in TriggerTestInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	items, err := b.TestTriggerItems(r.Context(), in)
	if err != nil {
		s.writeSensorError(w, "trying the bot failed", err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) handleTestTriggerRender(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	var in TriggerTestInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	res, err := b.TestTriggerRender(r.Context(), in)
	if err != nil {
		s.writeSensorError(w, "trying the bot failed", err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) handleDeleteTrigger(w http.ResponseWriter, r *http.Request) {
	b, ok := s.configBackend(w)
	if !ok {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := b.DeleteTrigger(r.Context(), id); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetTriggers lists triggers; ?companionId=N filters to one companion.
func (s *Server) handleGetTriggers(w http.ResponseWriter, r *http.Request) {
	var trigs []store.Trigger
	var err error
	if q := r.URL.Query().Get("companionId"); q != "" {
		id, perr := strconv.ParseInt(q, 10, 64)
		if perr != nil {
			writeError(w, http.StatusBadRequest, "invalid companionId")
			return
		}
		trigs, err = s.store.Triggers.ListByCompanion(r.Context(), id)
	} else {
		trigs, err = s.store.Triggers.List(r.Context())
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to read triggers")
		return
	}
	writeJSON(w, http.StatusOK, mapSlice(trigs, triggerToDTO))
}

// handleMqttStatus lists every configured broker, including one whose connect failed.
func (s *Server) handleMqttStatus(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "brokers": []MqttBrokerStatus{}})
		return
	}
	brokers, ok := b.MqttStatus()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"running": false, "brokers": []MqttBrokerStatus{}})
		return
	}
	if brokers == nil {
		brokers = []MqttBrokerStatus{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"running": true, "brokers": brokers})
}

// handleSPIBoards lists the radio hats this build supports. Empty when the
// backend is not up yet, which the UI shows as "no boards available" rather
// than an empty picker that looks like a working choice.
func (s *Server) handleSPIBoards(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeJSON(w, http.StatusOK, []SPIBoardInfo{})
		return
	}
	writeJSON(w, http.StatusOK, b.SPIBoards())
}

// handleRadioStatus reports the modem's link counters; a backend that is not up yet errors rather than answering zeroes, which would read as a healthy radio.
func (s *Server) handleRadioStatus(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "modem not connected")
		return
	}
	info, ok := b.RadioStats()
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "modem not connected")
		return
	}
	writeJSON(w, http.StatusOK, info)
}

// handleRadioReset drops the modem and reconnects it. Returns 202: the reconnect runs in the
// supervisor with its own backoff, so it has not necessarily finished when this returns.
func (s *Server) handleRadioReset(w http.ResponseWriter, r *http.Request) {
	b := s.backendRef()
	if b == nil {
		writeError(w, http.StatusServiceUnavailable, "modem not connected")
		return
	}
	b.ResetModem()
	w.WriteHeader(http.StatusAccepted)
}
