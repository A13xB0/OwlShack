// Package appserver serves the MeshCore companion protocol on a TCP port per companion, so the
// apps that drive a companion radio over WiFi can drive an OwlShack companion the same way. It
// owns the listeners, the sessions and each companion's offline queue; everything a command reads
// or changes comes from a Device, which the app package implements over a running companion.
package appserver

import (
	"errors"
	"fmt"

	meshcore "github.com/meshcore-go/meshcore-go"
	"github.com/meshcore-go/meshcore-go/companion"
)

// Refusal is an ERR_CODE_* a Device answers a command with.
type Refusal byte

func (r Refusal) Error() string { return fmt.Sprintf("refused with error code %d", byte(r)) }

const (
	ErrUnsupported = Refusal(companion.ErrCodeUnsupportedCmd)
	ErrNotFound    = Refusal(companion.ErrCodeNotFound)
	ErrTableFull   = Refusal(companion.ErrCodeTableFull)
	ErrBadState    = Refusal(companion.ErrCodeBadState)
	ErrFileIO      = Refusal(companion.ErrCodeFileIoError)
	ErrIllegalArg  = Refusal(companion.ErrCodeIllegalArg)
)

// errCode is the code an error answers with; anything that is not a Refusal is a send that could not be queued.
func errCode(err error) byte {
	var r Refusal
	if errors.As(err, &r) {
		return byte(r)
	}
	return companion.ErrCodeTableFull
}

// Scope is a session's flood scope override: unset follows the companion's default scope.
type Scope struct {
	Set      bool
	Unscoped bool
	Region   *meshcore.Region
}

// Prefs are what an app sets on a companion radio that OwlShack keeps for it.
type Prefs struct {
	ManualAdd       byte
	TelemetryModes  byte // env<<4 | loc<<2 | base, as SELF_INFO packs them
	AdvLocPolicy    byte
	MultiAcks       byte
	AutoAddConfig   byte
	AutoAddMaxHops  byte
	RxDelayBaseMs   uint32
	AirtimeFactorMs uint32
	BLEPin          uint32
}

// RequestKind is which of the firmware's request commands started a request.
type RequestKind int

const (
	RequestStatus RequestKind = iota
	RequestTelemetry
	RequestBinary
	RequestAnon
	RequestPathDiscovery
)

// Message kinds, one per frame the offline queue hands an app.
const (
	MessageContact = iota
	MessageChannel
	MessageChannelData
)

// Message is one received message held for an app until it syncs it.
type Message struct {
	Kind int

	SenderPrefix [6]byte
	// AuthorPrefix is a room post's 4-byte author prefix.
	AuthorPrefix []byte
	ChannelIdx   int

	TxtType         byte
	SenderTimestamp uint32
	Text            string

	DataType uint16
	Data     []byte

	PathLen byte
	SNR     float32
}

// Event is what a Device tells its app: a message to queue, or a push to deliver if an app is connected.
type Event struct {
	Message *Message
	Push    *companion.Response
}

// Device is one companion as an app sees it.
type Device interface {
	SelfInfo() companion.SelfInfoResponse
	DeviceInfo() companion.DeviceInfoResponse
	BattAndStorage() companion.BattAndStorageResponse
	Stats(kind byte) companion.StatsResponse
	Prefs() Prefs
	SetPrefs(Prefs) error

	SetName(name string) error
	SetLatLon(lat, lon int32) error
	SetPathHashMode(mode byte) error
	DefaultScope() (name string, key []byte)
	SetDefaultScope(name string, key []byte) error
	SendSelfAdvert(flood bool) error
	SelfAdvert() ([]byte, error)
	SelfTelemetry() []byte
	PrivateKey() ([64]byte, bool)
	Sign(data []byte) ([64]byte, error)

	Contacts() []companion.ContactResponse
	Contact(key [32]byte) (companion.ContactResponse, bool)
	FindContact(prefix []byte) ([32]byte, bool)
	PutContact(companion.AddUpdateContactCommand) error
	RemoveContact(key [32]byte) error
	ResetPath(key [32]byte) error
	ShareContact(key [32]byte) error
	ExportContact(key [32]byte) ([]byte, error)
	ImportContact(advert []byte) error
	AdvertPath(key [32]byte) (companion.AdvertPathResponse, bool)

	Channel(idx byte) (companion.ChannelInfoResponse, bool)
	SetChannel(idx byte, name string, secret [16]byte) error

	SendText(key [32]byte, txtType, attempt byte, timestamp uint32, text string, scope Scope, onAck func(ack, roundTripMs uint32)) (companion.SentResponse, error)
	SendChannelText(idx byte, timestamp uint32, text string, scope Scope) error
	SendChannelData(idx byte, path []byte, flood bool, dataType uint16, data []byte, scope Scope) error
	SendRawData(path, data []byte) error
	SendRawPacket(priority byte, packet []byte) error
	SendControlData(data []byte) error
	SendTrace(tag, auth uint32, flags byte, path []byte) (companion.SentResponse, error)

	// Login and Request answer RESP_CODE_SENT now and deliver the reply later as a push.
	Login(key [32]byte, password string) (companion.SentResponse, error)
	Request(key [32]byte, kind RequestKind, data []byte, scope Scope) (companion.SentResponse, error)
	HasConnection(key [32]byte) bool
	Logout(key [32]byte)

	Subscribe(func(Event)) (cancel func())
}
