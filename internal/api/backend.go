package api

import "context"

// Backend is the seam between the HTTP/WS layer and the domain; api never imports the domain.
type Backend interface {
	Companions() []CompanionInfo

	// SPIBoards lists the radio hats this build knows how to wire.
	SPIBoards() []SPIBoardInfo

	// RadioStats reports the modem's link counters; on the SPI path they are the radio's only health
	// signal. False means there is no modem to report on, which must not be answered with zeroes.
	RadioStats() (RadioStatsInfo, bool)

	// Health reports the monitoring snapshot behind GET /api/health.
	Health() HealthInfo

	// ResetModem drops the modem and reconnects it, the same path a vanished serial port takes.
	ResetModem()

	// StartDiscovery broadcasts a zero-hop discovery for the given firmware ADV_TYPEs. False means
	// there is no node to send it from, which is a different answer from finding nothing.
	StartDiscovery(types []int) (DiscoveryState, bool)
	// DiscoveryState reports the current scan without starting one.
	DiscoveryState() (DiscoveryState, bool)

	// Sensors reports every configured local sensor with its current state.
	Sensors() []SensorStatus

	// SensorProviders lists the sensor origins this build knows about, with whether each can run here.
	SensorProviders(ctx context.Context) []SensorProviderInfo

	// DiscoverSensors scans providers, or all of them when provider is empty. Read-only.
	DiscoverSensors(ctx context.Context, provider string) (SensorScan, error)

	// SensorKinds is the parts catalogue; an empty provider returns every provider's.
	SensorKinds(provider string) ([]SensorKindInfo, error)

	// CreateSensor stores a sensor and starts reading it.
	CreateSensor(ctx context.Context, in SensorInput) (int64, error)

	UpdateSensor(ctx context.Context, id int64, in SensorInput) error

	// DeleteSensor stops and forgets one sensor.
	DeleteSensor(ctx context.Context, id int64) error
	// TestSensor tries an unsaved sensor once; id is the sensor being edited, whose stored secrets in.KeepSecrets names, or 0.
	TestSensor(ctx context.Context, id int64, in SensorInput) (SensorTest, error)

	// TelemetryMap is which sensor readings each node publishes over the mesh, and what it costs.
	TelemetryMap(ctx context.Context) (TelemetryMap, error)
	// SetTelemetryMap replaces one node's map, which is validated as one thing because the rules are per node.
	SetTelemetryMap(ctx context.Context, node TelemetryNode, entries []TelemetryMapEntry) error

	// ChannelByHash resolves a channel hash byte across every companion; nil when unknown.
	ChannelByHash(hash byte) *ChannelInfo

	// AddPeer registers a peer in every companion's in-memory table, so it is reachable before its advert.
	AddPeer(pubkey []byte, name, peerType string)

	// RemovePeers drops peers from every companion's in-memory table; the DB row is deleted separately.
	RemovePeers(pubkeys [][]byte)

	// Companion returns the channel/DM senders for a named companion.
	Companion(name string) (MessageSender, DMSender, bool)

	ChannelMutator(name string) (adder ChannelAdder, remover ChannelRemover, ok bool)

	RenameChannel(companionName, oldName, newName string) error

	TraceSender(name string) (TraceSender, bool)

	AdvertSender(name string) (AdvertSender, bool)

	Repeater(name string) (*RepeaterOps, bool)

	// MqttStatus reports each broker's live state; ok=false when no MQTT observer is running.
	MqttStatus() ([]MqttBrokerStatus, bool)

	// ExportBackup builds a downloadable backup honouring opts.
	ExportBackup(ctx context.Context, opts BackupOptions) (*BackupFile, error)
	// EstimateBackup reports what opts would capture, without building the file.
	EstimateBackup(ctx context.Context, opts BackupOptions) (*BackupEstimate, error)
	// ImportBackup restores a backup database or applies a config file; filename only picks the config parser.
	ImportBackup(ctx context.Context, data []byte, filename string) (*ImportResult, error)

	// RepeaterNode returns ops for the repeater we run, not a remote one we drive; ok=false when none runs.
	RepeaterNode() (*RepeaterNodeOps, bool)

	// PersistChannels writes the companions' current channels back to the config file.
	PersistChannels(ctx context.Context) error

	// Config writes by surrogate id: id==0 creates and returns it, id>0 updates; *string secrets are nil=keep, ""=clear.
	SaveSettings(ctx context.Context, in SettingsInput) error
	SaveMqtt(ctx context.Context, in MqttInput) error
	SaveBroker(ctx context.Context, in BrokerInput) (int64, error)
	DeleteBroker(ctx context.Context, id int64) error
	SaveCompanion(ctx context.Context, in CompanionInput) (int64, error)
	// SetCompanionTelemetry sets who may read each class of a companion's telemetry.
	SetCompanionTelemetry(ctx context.Context, id int64, in CompanionTelemetryInput) error
	DeleteCompanion(ctx context.Context, id int64) error
	SaveChannel(ctx context.Context, in ChannelInput) (int64, error)
	DeleteChannel(ctx context.Context, id int64) error
	SaveTrigger(ctx context.Context, in TriggerInput) (int64, error)
	DeleteTrigger(ctx context.Context, id int64) error
	// TestTriggerItems fetches an unsaved rss or cap bot's feed and lists its newest items.
	TestTriggerItems(ctx context.Context, in TriggerTestInput) ([]TriggerTestItem, error)
	// TestTriggerRender renders an unsaved rss or cap bot's template against one item, as it would be sent.
	TestTriggerRender(ctx context.Context, in TriggerTestInput) (TriggerTestRender, error)

	// Repeater config is per-section; CreateRepeater generates a key when PrivateKey is nil and seeds the "*" region.
	CreateRepeater(ctx context.Context, in RepeaterCreateInput) error
	UpdateRepeaterNode(ctx context.Context, in RepeaterNodeInput) error
	UpdateRepeaterRelay(ctx context.Context, in RepeaterRelayInput) error
	UpdateRepeaterAdmin(ctx context.Context, in RepeaterAdminInput) error
	AddRepeaterRegion(ctx context.Context, in RepeaterRegionInput) error
	SetRepeaterRegionFlood(ctx context.Context, name string, denyFlood bool) error
	RemoveRepeaterRegion(ctx context.Context, name string) error
	DeleteRepeater(ctx context.Context) error
}

// ValidationError is a backend refusing the request itself, which a handler answers 422 with its reason; any other error is the server's.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// Invalid marks err as the request's fault.
func Invalid(err error) error { return &ValidationError{Err: err} }

// RepeaterNodeOps are runtime operations on the running repeater node.
type RepeaterNodeOps struct {
	Name       string
	Stats      func() any // live relay counters + uptime + neighbour count
	Neighbors  func() any // directly-heard repeaters
	Advert     func(flood bool) error
	Discover   func() error                         // zero-hop NODE_DISCOVER_REQ; responses land in Neighbors
	ACL        func() any                           // admin clients in the ACL (name-resolved)
	RevokeACL  func(pubkey string) error            // drop a client's access
	SetACL     func(pubkey string, perms int) error // grant / change a client's role (setperm)
	ClearStats func()                               // reset relay counters (clear stats)
}

// MqttBrokerStatus mirrors mqtt.BrokerStatus; duplicated because api must not import the domain.
type MqttBrokerStatus struct {
	Name        string `json:"name"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Transport   string `json:"transport"`
	TLS         bool   `json:"tls"`
	AuthType    string `json:"authType"`
	Enabled     bool   `json:"enabled"`
	Connected   bool   `json:"connected"`
	LastError   string `json:"lastError,omitempty"`
	LastErrorTs int64  `json:"lastErrorTs,omitempty"`
	ConnectedTs int64  `json:"connectedTs,omitempty"`
	Published   uint64 `json:"published"`
	Dropped     uint64 `json:"dropped"`
	StatusTopic string `json:"statusTopic,omitempty"`
}

// --- per-resource config write inputs (JSON request bodies) ---

// SPIBoardInfo describes one radio hat this build knows about.
type SPIBoardInfo struct {
	Name       string `json:"name"`
	Label      string `json:"label"`
	Chip       string `json:"chip"`
	SPIPort    string `json:"spiPort"`
	MaxTxPower int    `json:"maxTxPower"`
	// Verified is "hardware" or "community"; shown in the picker before an antenna goes up.
	Verified string `json:"verified"`
	Notes    string `json:"notes,omitempty"`
	HasLEDs  bool   `json:"hasLeds"`
}

// DiscoveryInfo is one node that answered a zero-hop discovery.
type DiscoveryInfo struct {
	PubKey string `json:"pubkey"`
	// Name is resolved from peers we have already heard advert; empty for a node we only know by key.
	Name string `json:"name"`
	Type int    `json:"type"`
	// SNR is how well we heard them; ReportedSNR is how well they heard us, so an asymmetric link is visible.
	SNR         float64 `json:"snr"`
	ReportedSNR float64 `json:"reportedSnr"`
	// Heard is when this node answered, as a timestamp so the page can age it without polling.
	Heard string `json:"heard"`
}

// DiscoveryState is a scan and whatever has answered so far.
type DiscoveryState struct {
	Running bool `json:"running"`
	// SecsLeft counts down the collection window; responders answer after a random delay, so an
	// empty list before it reaches 0 means "not yet", not "nothing there".
	SecsLeft int `json:"secsLeft"`
	// ScanStartedAt is empty until a scan has run, so results are never shown without saying how old
	// they are: a finished scan's table looks identical to a live one.
	ScanStartedAt string          `json:"scanStartedAt"`
	Results       []DiscoveryInfo `json:"results"`
}

// HealthInfo backs GET /api/health: facts for a monitor to threshold, never verdicts, and 200 whenever the process is alive.
type HealthInfo struct {
	// Status is "ok" when Problems is empty, else "degraded"; only what the node cannot work without counts.
	Status     string   `json:"status"`
	Problems   []string `json:"problems"`
	Version    string   `json:"version"`
	UptimeSecs int64    `json:"uptimeSecs"`

	Radio    RadioHealth    `json:"radio"`
	Database DatabaseHealth `json:"database"`
	// Mqtt is the uploads' own verdict: a broker being down loses what the mesh heard from the map, not the node.
	Mqtt    MqttHealth     `json:"mqtt"`
	Brokers []BrokerHealth `json:"brokers"`
}

type MqttHealth struct {
	// Status is "ok" when every enabled broker is connected, "degraded" when one is not, and "off" when none is meant to be.
	Status   string   `json:"status"`
	Problems []string `json:"problems"`
}

// RadioHealth keeps "modem attached", "board answering" and "mesh talking" apart: a quiet mesh moves only the last.
type RadioHealth struct {
	Connected bool   `json:"connected"`
	Transport string `json:"transport"`

	// Since the board last answered, or since it connected if it has not yet; null where this transport cannot be probed.
	LastReplySecs *int64 `json:"lastReplySecs"`
	// Null when none since startup.
	LastRxSecs *int64 `json:"lastRxSecs"`
	LastTxSecs *int64 `json:"lastTxSecs"`

	TxQueueLen     int    `json:"txQueueLen"`
	TxSent         uint64 `json:"txSent"`
	TxFailed       uint64 `json:"txFailed"`
	TxDroppedBusy  uint64 `json:"txDroppedBusy"`
	TxDroppedQueue uint64 `json:"txDroppedQueue"`
	// Sends that have not gone out since the last one that did, busy retries included; the age is null while the last send worked.
	TxFailedInARow uint64 `json:"txFailedInARow"`
	TxFailingSecs  *int64 `json:"txFailingSecs"`

	InboundDroppedNew uint64 `json:"inboundDroppedNew"`
	HandlerSlow       uint64 `json:"handlerSlow"`

	// Absent where the transport cannot measure them at all, 0 where it measured none.
	CRCErrors    *uint64 `json:"crcErrors,omitempty"`
	RecvErrors   *uint64 `json:"recvErrors,omitempty"`
	DriverErrors *uint64 `json:"driverErrors,omitempty"`
	HwErrors     *uint64 `json:"hwErrors,omitempty"`

	NoiseFloor *int16   `json:"noiseFloor,omitempty"`
	BatteryMV  *uint16  `json:"batteryMv,omitempty"`
	MCUTempC   *float64 `json:"mcuTempC,omitempty"`
}

// DatabaseHealth watches the write queue, not a ping: a dropped write leaves everything downstream looking healthy.
type DatabaseHealth struct {
	// A point sample of a queue that drains in microseconds: shows sustained backpressure, not a spike.
	WriteQueueLen int    `json:"writeQueueLen"`
	WriteQueueCap int    `json:"writeQueueCap"`
	WritesDropped uint64 `json:"writesDropped"`
	// The one to threshold: WritesDropped only rises, so it cannot tell "now" from "last Tuesday".
	WritesDroppedLastSecs *int64 `json:"writesDroppedLastSecs"`
	// WALBytes is the write-ahead log on disk: a checkpoint trims it to 4 MiB, so one that keeps growing means checkpoints are not completing.
	WALBytes int64 `json:"walBytes"`
	// Free space where the database lives, for the user OwlShack runs as; null where the platform cannot say. A full disk fails every write without a drop.
	DiskFreeBytes *uint64 `json:"diskFreeBytes"`
}

// No node listing on purpose: a name or peer count cannot report a fault (a node that fails to start is a problem entry), and both resolve to coordinates on public maps.

// BrokerHealth carries ages, not the transport error (it names the broker's address) and not flags (the observer never clears lastErr, so a bool would latch).
type BrokerHealth struct {
	Name      string `json:"name"`
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	// Null when disconnected. Resetting toward zero on every scrape is how flapping shows; Connected alone reads true.
	ConnectedSecs *int64 `json:"connectedSecs"`
	// Null when it has never erred.
	LastErrorSecs *int64 `json:"lastErrorSecs"`
	Published     uint64 `json:"published"`
	Dropped       uint64 `json:"dropped"`
}

// RadioStatsInfo mirrors modem.LinkStats plus the radio's configuration; the pointer counters are absent when the backend cannot measure them, 0 when it measured none.
type RadioStatsInfo struct {
	FreqHz  uint32 `json:"freqHz"`
	BwHz    uint32 `json:"bwHz"`
	SF      uint8  `json:"sf"`
	CR      uint8  `json:"cr"`
	TxPower uint8  `json:"txPower"`

	InboundDroppedNew uint64 `json:"inboundDroppedNew"`
	HandlerSlow       uint64 `json:"handlerSlow"`

	HwDecodeErrors       *uint64 `json:"hwDecodeErrors,omitempty"`
	InboundDroppedOldest *uint64 `json:"inboundDroppedOldest,omitempty"`
	RxMetaTimeouts       *uint64 `json:"rxMetaTimeouts,omitempty"`
	RxMetaMisattributed  *uint64 `json:"rxMetaMisattributed,omitempty"`
	HwErrors             *uint64 `json:"hwErrors,omitempty"`
	TxOutcomeLost        *uint64 `json:"txOutcomeLost,omitempty"`

	PacketsRecv    *uint64 `json:"packetsRecv,omitempty"`
	PacketsSent    *uint64 `json:"packetsSent,omitempty"`
	CRCErrors      *uint64 `json:"crcErrors,omitempty"`
	RecvErrors     *uint64 `json:"recvErrors,omitempty"`
	DriverErrors   *uint64 `json:"driverErrors,omitempty"`
	RecvRecoveries *uint64 `json:"recvRecoveries,omitempty"`

	// Transport is "kiss" or "spi": which counters above can move at all depends on it.
	Transport string `json:"transport"`

	// TX outcomes, from the mux's own queue rather than the modem.
	TxSent         uint64 `json:"txSent"`
	TxFailed       uint64 `json:"txFailed"`
	TxRequeued     uint64 `json:"txRequeued"`
	TxDroppedBusy  uint64 `json:"txDroppedBusy"`
	TxDroppedQueue uint64 `json:"txDroppedQueue"`
	// Sends that have not gone out since the last one that did, busy retries included; the age is null while the last send worked.
	TxFailedInARow uint64 `json:"txFailedInARow"`
	TxFailingSecs  *int64 `json:"txFailingSecs"`
	TxQueueLen     int    `json:"txQueueLen"`

	// Board readings, absent when this hardware has no such sensor or has stopped answering.
	UptimeSecs uint32   `json:"uptimeSecs"`
	NoiseFloor *int16   `json:"noiseFloor,omitempty"`
	BatteryMV  *uint16  `json:"batteryMv,omitempty"`
	MCUTempC   *float64 `json:"mcuTempC,omitempty"`
}

// SensorProviderInfo is one sensor origin in the picker.
type SensorProviderInfo struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Available bool   `json:"available"`
	// Reason says why an unavailable provider cannot run here, so no I2C bus reads as that and not as an empty list.
	Reason string `json:"reason,omitempty"`
}

// SensorCandidate is something a provider found, its Options ready to post straight back, which keeps the add form free of per-provider fields.
type SensorCandidate struct {
	Kind     string `json:"kind"`
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Detail   string `json:"detail,omitempty"`
	// Addable is false for a part this build cannot drive; it is still listed, or an unknown part reads as an empty bus.
	Addable bool `json:"addable"`
	// UsedBy names the sensor already on this part, empty while it is free, so the page need not offer what saving refuses.
	UsedBy  string            `json:"usedBy"`
	Options map[string]string `json:"options"`
}

// SensorReading is one measurement; Label tells apart two readings of one metric, and is empty when the metric alone names it.
type SensorReading struct {
	Metric string  `json:"metric"`
	Label  string  `json:"label,omitempty"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit,omitempty"`
	// Format is "number", "flag" (a yes or no as 1 or 0) or "count", so the page never shows charging as 1.000.
	Format string `json:"format"`
	// Role is "headline", "detail" or "calibration", so the page never keeps its own list of which is which.
	Role string `json:"role"`
}

// SensorStatus is one sensor; Error describes the latest attempt, Readings and At the last one that worked.
type SensorStatus struct {
	ID       int64             `json:"id"`
	Provider string            `json:"provider"`
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Options  map[string]string `json:"options"`
	Bindings []SensorBinding   `json:"bindings"`
	// Reports is every metric the sensor publishes under its options, so a picker can offer one before it is ever read.
	Reports  []string        `json:"reports"`
	Readings []SensorReading `json:"readings"`
	At       *string         `json:"at"`
	// SecretsSet names the secret options that hold a value, which Options leaves out.
	SecretsSet []string `json:"secretsSet"`
	// StaleAfterSecs is how old a reading may be before it is out of date: the poll's for most, a web sensor's own.
	StaleAfterSecs float64 `json:"staleAfterSecs"`
	// AgeSecs is how old Readings were when this was sent, by the host's clock alone, so a phone with another time never reads it as stale or fresh; null until first read.
	AgeSecs *float64 `json:"ageSecs"`
	// RetryInSecs is how long until a sensor that failed to open is tried again, by the host's clock; null when it is not waiting.
	RetryInSecs *float64 `json:"retryInSecs"`
	// Category is the kind's catalogue group, such as "Environment", so the page can group without the catalogue.
	Category string `json:"category"`
	Error    string `json:"error"`
}

// SensorField is one option a kind needs, so the UI can build a form for a provider it knows nothing about.
type SensorField struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Help  string `json:"help,omitempty"`
	// Default is filled in when the operator leaves the field alone.
	Default string `json:"default,omitempty"`
	// Choices, when set, means the value must be one of these and the UI offers a picker.
	Choices   []string `json:"choices,omitempty"`
	Required  bool     `json:"required"`
	Multiline bool     `json:"multiline,omitempty"`
	// Identifies means the value says which part this is, so a card can name it without every option.
	Identifies bool `json:"identifies,omitempty"`
	// Secret is never sent back; the form shows whether one is set and keeps it unless replaced.
	Secret bool `json:"secret,omitempty"`
	// When shows the field only while another field holds one of Values.
	When *SensorFieldWhen `json:"when,omitempty"`
	// Type is "duration" or "list", or empty for free text.
	Type string `json:"type,omitempty"`
	// MinSecs and MaxSecs bound a duration; MaxSecs 0 is unbounded.
	MinSecs float64 `json:"minSecs,omitempty"`
	MaxSecs float64 `json:"maxSecs,omitempty"`
	// AtLeast names a duration field this one may not be shorter than.
	AtLeast string `json:"atLeast,omitempty"`
	// Columns are a list field's inputs; its value is a JSON array of objects keyed by column.
	Columns []SensorColumn `json:"columns,omitempty"`
	// Tested means a test reads this list's rows, so the form puts the test just above it and shows each row's result.
	Tested bool `json:"tested,omitempty"`
}

type SensorColumn struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder,omitempty"`
	Required    bool   `json:"required"`
}

type SensorFieldWhen struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// SensorKindInfo is one entry in the parts catalogue, which has to stay searchable at sixty parts.
type SensorKindInfo struct {
	Kind        string        `json:"kind"`
	Provider    string        `json:"provider"`
	Label       string        `json:"label"`
	Description string        `json:"description,omitempty"`
	Category    string        `json:"category,omitempty"`
	Metrics     []string      `json:"metrics,omitempty"`
	Binds       bool          `json:"binds,omitempty"`
	Fields      []SensorField `json:"fields"`
	// Testable means the form can try a sensor of this kind before saving it.
	Testable bool `json:"testable"`
}

// SensorTestInput is a form as it stands, and the sensor it edits, 0 when adding.
type SensorTestInput struct {
	ID int64 `json:"id"`
	SensorInput
}

// SensorTest is one try of an unsaved sensor: what answered, the reply, and each value or why it failed.
type SensorTest struct {
	// Error is why the try as a whole failed, empty when it worked; the values read beside it still count.
	Error       string            `json:"error"`
	Status      string            `json:"status"`
	ContentType string            `json:"contentType"`
	Body        string            `json:"body"`
	Truncated   bool              `json:"truncated"`
	Values      []SensorTestValue `json:"values"`
}

type SensorTestValue struct {
	Metric string `json:"metric"`
	// Value is null when Error says why it could not be read.
	Value *float64 `json:"value"`
	Unit  string   `json:"unit"`
	Error string   `json:"error"`
}

// SensorProviderProblem sits apart from the results, so an unreadable bus never reads as an empty one.
type SensorProviderProblem struct {
	Provider string `json:"provider"`
	Label    string `json:"label"`
	Reason   string `json:"reason"`
}

// SensorScan is one scan: what was found, and what could not be looked at.
type SensorScan struct {
	Candidates []SensorCandidate       `json:"candidates"`
	Problems   []SensorProviderProblem `json:"problems"`
}

// SensorBinding names one reading under the name an expression calls it, so a rename cannot break it.
type SensorBinding struct {
	Name     string `json:"name"`
	SensorID int64  `json:"sensorId"`
	Metric   string `json:"metric"`
}

// SensorInput is the add and edit form's payload.
type SensorInput struct {
	Provider string            `json:"provider"`
	Kind     string            `json:"kind"`
	Name     string            `json:"name"`
	Options  map[string]string `json:"options"`
	Bindings []SensorBinding   `json:"bindings,omitempty"`
	// KeepSecrets names secret options an edit carries over from the stored sensor, as the page is never sent them.
	KeepSecrets []string `json:"keepSecrets,omitempty"`
}

type SettingsInput struct {
	LogLevel       *string `json:"logLevel"`
	ConnectionType *string `json:"connectionType"`
	Connection     *string `json:"connection"`
	BaudRate       *int    `json:"baudRate"`
	// SPIBoard names the hat for an spi:// connection; omitted keeps the stored value.
	SPIBoard     *string  `json:"spiBoard"`
	Freq         *float64 `json:"freq"`
	BW           *float64 `json:"bw"`
	SF           *int     `json:"sf"`
	CR           *int     `json:"cr"`
	TX           *int     `json:"tx"`
	ListenAddr   *string  `json:"listenAddr"`
	MapProvider  *string  `json:"mapProvider"`  // omit = keep
	MapDarkStyle *string  `json:"mapDarkStyle"` // omit = keep
	MapTileKey   *string  `json:"mapTileKey"`   // omit = keep, "" = clear
	// ModemToken is the openHop modem's access token: omit = keep the stored one, "" = clear it.
	ModemToken   *string `json:"modemToken"`
	PathHashSize *int    `json:"pathHashSize"`
	// DutyCycle is a TX airtime cap percentage (0 < pct <= 100); null means the default, not "keep".
	DutyCycle     *float64 `json:"dutyCycle"`
	SetupComplete *bool    `json:"setupComplete"`
}

type MqttInput struct {
	Enabled         *bool   `json:"enabled"`
	NodeCompanionID *int64  `json:"nodeCompanionId"`
	IataCode        *string `json:"iataCode"`
	StatusInterval  *int    `json:"statusInterval"`
	Owner           *string `json:"owner"`
	Email           *string `json:"email"`
}

type BrokerInput struct {
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
	Password              *string  `json:"password"` // nil = keep existing
	Path                  string   `json:"path"`
	Audience              string   `json:"audience"`
}

type CompanionInput struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	PrivateKey     *string  `json:"privateKey"` // nil = keep (update) / generate (create)
	DMPolicy       string   `json:"dmPolicy"`   // "" = contacts
	DMAllow        []string `json:"dmAllow"`
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	AdvertInterval *int     `json:"advertInterval"`
	PathHashSize   *int     `json:"pathHashSize"`
}

// CompanionTelemetryInput is who may read each class: "deny", "selected" or "contacts".
type CompanionTelemetryInput struct {
	Base        string `json:"base"`
	Location    string `json:"location"`
	Environment string `json:"environment"`
}

type ChannelInput struct {
	ID          int64   `json:"id"`
	CompanionID int64   `json:"companionId"`
	Name        string  `json:"name"`
	PrivateKey  *string `json:"privateKey"` // nil = keep existing
}

// RepeaterCreateInput sets up the singleton; everything else is edited through the section endpoints.
type RepeaterCreateInput struct {
	Name       string  `json:"name"`
	PrivateKey *string `json:"privateKey"` // nil/empty = generate
	// AdminPassword is required and must be non-blank: a blank one compares equal to the blank a
	// login sends, which granted admin to any node in range.
	AdminPassword string `json:"adminPassword"`
}

// RepeaterNodeInput is the Node section; PrivateKey nil keeps the identity, a value rotates it.
type RepeaterNodeInput struct {
	Name       string   `json:"name"`
	PrivateKey *string  `json:"privateKey"`
	Latitude   *float64 `json:"latitude"`
	Longitude  *float64 `json:"longitude"`
}

// RepeaterRelayInput is the Relay-policy section: forwarding + advert cadence.
type RepeaterRelayInput struct {
	DisableFwd          *bool    `json:"disableFwd"`
	FloodMax            *int     `json:"floodMax"`
	FloodMaxUnscoped    *int     `json:"floodMaxUnscoped"`
	FloodMaxAdvert      *int     `json:"floodMaxAdvert"`
	LoopDetect          *string  `json:"loopDetect"`
	PathHashSize        *int     `json:"pathHashSize"`
	TxDelayFactor       *float64 `json:"txDelayFactor"`
	DirectTxDelayFactor *float64 `json:"directTxDelayFactor"`
	RxDelayBase         *float64 `json:"rxDelayBase"`
	MultiAcks           *int     `json:"multiAcks"`
	DefaultRegion       string   `json:"defaultRegion"` // "" = unscoped flood adverts
	AdvertInterval      *int     `json:"advertInterval"`
	FloodAdvertInterval *int     `json:"floodAdvertInterval"`
}

// RepeaterAdminInput is the Owner & access section; passwords are nil=keep, ""=clear.
type RepeaterAdminInput struct {
	OwnerInfo     string  `json:"ownerInfo"`
	AdminPassword *string `json:"adminPassword"`
	GuestPassword *string `json:"guestPassword"`
}

// RepeaterRegionInput is a region add (POST) or deny-flood toggle (PATCH) body.
type RepeaterRegionInput struct {
	Name      string `json:"name"`
	DenyFlood bool   `json:"denyFlood"`
}

// TriggerTestInput is an unsaved feed bot to try; ItemID picks the item a render uses.
type TriggerTestInput struct {
	CompanionID int64    `json:"companionId"`
	Type        string   `json:"type"`
	URL         string   `json:"url"`
	Match       []string `json:"match"`
	Template    string   `json:"template"`
	// Location is the cap trigger's point, Regions its region ids; null takes alerts from anywhere.
	Location *TriggerLocation `json:"location"`
	Regions  *[]string        `json:"regions"`
	ItemID   string           `json:"itemId"`
}

// TriggerLocation is a cap trigger's point and margin; pointers only so a missing field is refused, not read as 0.
type TriggerLocation struct {
	Lat      *float64 `json:"lat"`
	Lon      *float64 `json:"lon"`
	RadiusKm *float64 `json:"radiusKm"`
}

type TriggerTestItem struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Link  string `json:"link"`
	// Published is null when the feed gives the item no time.
	Published *string `json:"published"`
}

// TriggerTestRender is what the bot would send for one item, measured against what each way out keeps.
type TriggerTestRender struct {
	Message string `json:"message"`
	// RenderError is the template's own failure, which is normal while it is being typed.
	RenderError string `json:"renderError"`
	// Matched is false when the match patterns would skip this item, so nothing would be sent.
	Matched bool `json:"matched"`
	// Placement is anywhere (no location or regions), inside, outside or noShape; only anywhere and inside send.
	Placement string            `json:"placement"`
	Captures  map[string]string `json:"captures"`
	Bytes     int               `json:"bytes"`
	// ChannelText is what a channel receives: the firmware keeps 160 bytes including the "name: " prefix and cuts the rest.
	ChannelText  string `json:"channelText"`
	ChannelLimit int    `json:"channelLimit"`
	// DMLimit is the longest DM that sends; a longer one fails rather than being cut.
	DMLimit int `json:"dmLimit"`
}

type TriggerInput struct {
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

// BackupFile is a generated backup ready to stream to the browser.
type BackupFile struct {
	Name        string
	ContentType string
	Data        []byte
}

// BackupOptions day fields are -1 for everything, 0 for none, or a positive number of days back.
type BackupOptions struct {
	// CompanionIDs is required: [] is none, and a pointer only so an omitted field is rejected.
	CompanionIDs *[]int64 `json:"companionIds"`
	Contacts     bool     `json:"contacts"`
	Triggers     bool     `json:"triggers"`
	Mqtt         bool     `json:"mqtt"`
	Repeater     bool     `json:"repeater"`
	Peers        bool     `json:"peers"`
	MessageDays  int      `json:"messageDays"`
	PacketDays   int      `json:"packetDays"`
	MetricDays   int      `json:"metricDays"`
	// IdentityKeys keeps the node private keys, so a restore is the same node on the mesh.
	IdentityKeys bool `json:"identityKeys"`
}

// BackupEstimate is the row count a selection captures, plus the DB size as an upper bound.
type BackupEstimate struct {
	Companions int64 `json:"companions"`
	Contacts   int64 `json:"contacts"`
	Messages   int64 `json:"messages"`
	Packets    int64 `json:"packets"`
	Peers      int64 `json:"peers"`
	Metrics    int64 `json:"metrics"`
	Bytes      int64 `json:"bytes"`
}

// ImportResult describes what a restore did.
type ImportResult struct {
	// Kind is "database" (a backup, staged for restart) or "config".
	Kind            string `json:"kind"`
	Companions      int    `json:"companions"`
	RestartRequired bool   `json:"restartRequired"`
	SchemaVersion   int    `json:"schemaVersion,omitempty"`
	Detail          string `json:"detail"`
}

// TelemetryNode names the map's owner; each node answers for itself, so a channel means nothing without it.
type TelemetryNode struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

// TelemetryNodeInfo is one node an operator can give a map to.
type TelemetryNodeInfo struct {
	Node TelemetryNode `json:"node"`
	Name string        `json:"name"`
	// Serves is false for a node that stores a map but cannot send it, so the page can say so.
	Serves bool `json:"serves"`
	// Reason says why, when Serves is false.
	Reason string `json:"reason,omitempty"`
}

// TelemetryMapEntry is one reading published by one node on one LPP channel as one type.
type TelemetryMapEntry struct {
	Node     TelemetryNode `json:"node"`
	Channel  int           `json:"channel"`
	Type     int           `json:"type"`
	SensorID int64         `json:"sensorId"`
	Metric   string        `json:"metric"`
}

// LPPTypeInfo is one CayenneLPP type a reading can be published as.
type LPPTypeInfo struct {
	Code int    `json:"code"`
	Name string `json:"name"`
	// Unit is what the type means on the wire, which is not always what the sensor reports in.
	Unit string `json:"unit,omitempty"`
	// Bytes is the payload plus the two-byte header, which is what the operator is spending.
	Bytes int `json:"bytes"`
	// Step is the smallest change the type can carry, so the loss is visible before saving.
	Step float64 `json:"step"`
}

// TelemetryMap is the whole editor payload, so the page cannot render a map against another catalogue.
type TelemetryMap struct {
	Nodes   []TelemetryNodeInfo `json:"nodes"`
	Entries []TelemetryMapEntry `json:"entries"`
	Types   []LPPTypeInfo       `json:"types"`
	// Defaults maps a metric to the type it is offered as; an absent metric has no obvious answer.
	Defaults map[string]int `json:"defaults"`
	// SelfChannel is the channel a node keeps for its own readings; a row there replaces the built-in one and may carry only SelfTypes.
	SelfChannel int   `json:"selfChannel"`
	SelfTypes   []int `json:"selfTypes"`
	// MaxChannel is the highest channel worth offering; see sensor.MaxChannel for why.
	MaxChannel int `json:"maxChannel"`
	// MaxBytes is per node, because the budget is per reply.
	MaxBytes int `json:"maxBytes"`
}
