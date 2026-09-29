package sensor

import (
	"errors"
	"fmt"
	"math"
	"slices"

	meshcore "github.com/meshcore-go/meshcore-go"
)

// ChannelSelf is the channel the firmware keeps for a node's own readings; sensors start above it.
const ChannelSelf = 1

// MaxTelemetryPayload leaves the node's own 8 bytes (voltage and temperature) in 153, the least MaxReplyBody gives a direct route or a flood path of up to 17 bytes.
const MaxTelemetryPayload = 153 - 8

// MaxChannel is the highest channel offered; the firmware stops at 17 (MAX_ACTIVE_SENSORS of 16).
const MaxChannel = 32

// Telemetry permission classes (firmware SensorManager.h); a requester masks classes out, not in.
const (
	PermBase        byte = 0x01 // the node itself: battery, MCU temperature
	PermLocation    byte = 0x02 // position, which the firmware sends only from a GPS module, and no host here has one
	PermEnvironment byte = 0x04 // everything the operator attached
	PermAll         byte = 0xFF
)

// Reply framing, from the firmware's reply_data buffer and the requesting companion's push to its app.
const (
	maxPacketPayload = 184 // MAX_PACKET_PAYLOAD
	respHeaderLen    = 4   // destination, source and the two MAC bytes
	cipherBlock      = 16
	maxFrame         = 176 // MAX_FRAME_SIZE: the requester's companion drops a longer frame to its app
	pushHeaderLen    = 8   // PUSH_CODE_TELEMETRY_RESPONSE, a reserved byte and a 6-byte key prefix
	// ReplyTagLen is the client timestamp a reply reflects back, ahead of the body.
	ReplyTagLen = 4
)

// MaxReplyBody is one reply's body in whole cipher blocks, within the packet and within the requester's frame, which carries them less the route and tag.
func MaxReplyBody(pkt *meshcore.Packet) int {
	route := 0
	if pkt != nil && pkt.IsRouteFlood() {
		route = 1 + len(pkt.Path) + 1 // path length byte, the path, the wrapped payload type
	}
	plain := min(maxPacketPayload-respHeaderLen, maxFrame-pushHeaderLen+route+ReplyTagLen) / cipherBlock * cipherBlock
	return plain - route - ReplyTagLen
}

// LPPType is one CayenneLPP type a reading can be published as; only single-value types are listed.
type LPPType struct {
	Code byte
	Name string
	// Unit is what the type means on the wire, not always what the sensor reports in.
	Unit string
	// Size is the payload in bytes, excluding the two-byte header.
	Size int
	// Step is the smallest change the type can carry.
	Step float64
}

// lppTypes is the publishable catalogue; names and units match internal/telemetry.
var lppTypes = []LPPType{
	{Code: meshcore.LPPTemperature, Name: "Temperature", Unit: "°C", Size: 2, Step: 0.1},
	{Code: meshcore.LPPRelativeHumidity, Name: "Humidity", Unit: "%RH", Size: 1, Step: 0.5},
	{Code: meshcore.LPPBarometricPressure, Name: "Pressure", Unit: "hPa", Size: 2, Step: 0.1},
	{Code: meshcore.LPPVoltage, Name: "Voltage", Unit: "V", Size: 2, Step: 0.01},
	{Code: meshcore.LPPCurrent, Name: "Current", Unit: "A", Size: 2, Step: 0.001},
	{Code: meshcore.LPPAnalogInput, Name: "Analog input", Unit: "", Size: 2, Step: 0.01},
	{Code: meshcore.LPPAnalogOutput, Name: "Analog output", Unit: "", Size: 2, Step: 0.01},
	{Code: meshcore.LPPGenericSensor, Name: "Generic sensor", Unit: "", Size: 4, Step: 1},
	{Code: meshcore.LPPPercentage, Name: "Percentage", Unit: "%", Size: 1, Step: 1},
	{Code: meshcore.LPPLuminosity, Name: "Luminosity", Unit: "lux", Size: 2, Step: 1},
	{Code: meshcore.LPPAltitude, Name: "Altitude", Unit: "m", Size: 2, Step: 1},
	{Code: meshcore.LPPConcentration, Name: "Concentration", Unit: "ppm", Size: 2, Step: 1},
	{Code: meshcore.LPPPower, Name: "Power", Unit: "W", Size: 2, Step: 1},
	{Code: meshcore.LPPFrequency, Name: "Frequency", Unit: "Hz", Size: 4, Step: 1},
	{Code: meshcore.LPPDistance, Name: "Distance", Unit: "m", Size: 4, Step: 0.001},
	{Code: meshcore.LPPEnergy, Name: "Energy", Unit: "kWh", Size: 4, Step: 0.001},
	{Code: meshcore.LPPDirection, Name: "Direction", Unit: "°", Size: 2, Step: 1},
	{Code: meshcore.LPPDigitalInput, Name: "Digital input", Unit: "", Size: 1, Step: 1},
	{Code: meshcore.LPPDigitalOutput, Name: "Digital output", Unit: "", Size: 1, Step: 1},
	{Code: meshcore.LPPPresence, Name: "Presence", Unit: "", Size: 1, Step: 1},
	{Code: meshcore.LPPSwitch, Name: "Switch", Unit: "", Size: 1, Step: 1},
	{Code: meshcore.LPPUnixTime, Name: "Unix time", Unit: "s", Size: 4, Step: 1},
}

func LPPTypes() []LPPType { return slices.Clone(lppTypes) }

func lookupLPPType(code byte) (LPPType, bool) {
	i := slices.IndexFunc(lppTypes, func(t LPPType) bool { return t.Code == code })
	if i < 0 {
		return LPPType{}, false
	}
	return lppTypes[i], true
}

// defaultLPPType is what a metric is offered as; the channel map holds the real answer.
var defaultLPPType = map[Metric]byte{
	Temperature:    meshcore.LPPTemperature,
	Humidity:       meshcore.LPPRelativeHumidity,
	Pressure:       meshcore.LPPBarometricPressure,
	Voltage:        meshcore.LPPVoltage,
	Current:        meshcore.LPPCurrent,
	Resistance:     meshcore.LPPGenericSensor,
	GasCompensated: meshcore.LPPGenericSensor,
	Percentage:     meshcore.LPPPercentage,
	// The firmware's own BSEC channel sends its index as a generic sensor and its accuracy as an analog input, so consumers already decode them so.
	IAQ:       meshcore.LPPGenericSensor,
	StaticIAQ: meshcore.LPPGenericSensor,
	// Unsigned ppm, the unit of both; the analog input's 0.01 step wraps above 327.67, and breath VOC runs to 1000 ppm.
	CO2Equivalent:         meshcore.LPPConcentration,
	BreathVOC:             meshcore.LPPConcentration,
	GasPercentage:         meshcore.LPPPercentage,
	IAQAccuracy:           meshcore.LPPAnalogInput,
	GasPercentageAccuracy: meshcore.LPPAnalogInput,
	AirQualityRunIn:       meshcore.LPPDigitalInput,
}

// DefaultLPPType suggests a type for a metric; false means the operator has to choose.
func DefaultLPPType(m Metric) (byte, bool) {
	code, ok := defaultLPPType[m]
	return code, ok
}

// ChannelEntry is one row of the channel map: this reading, as this type, on this channel.
type ChannelEntry struct {
	ID       int64
	Channel  byte
	Type     byte
	SensorID int64
	Metric   Metric
}

// telemetrySize counts every row whether or not its sensor reads, so the budget cannot shrink.
func telemetrySize(entries []ChannelEntry) int {
	n := 0
	for _, e := range entries {
		t, ok := lookupLPPType(e.Type)
		if !ok {
			continue
		}
		n += 2 + t.Size
	}
	return n
}

// ValidateChannelMap refuses a map that could not be sent as written.
func ValidateChannelMap(entries []ChannelEntry) error {
	seen := map[[2]byte]bool{}
	for _, e := range entries {
		t, ok := lookupLPPType(e.Type)
		if !ok {
			return fmt.Errorf("%d is not an LPP type this build can publish", e.Type)
		}
		// The node's own channel takes only what a consumer reads there as the node itself, and a row there replaces the built-in reading.
		if e.Channel == ChannelSelf && !isSelfType(e.Type) {
			return fmt.Errorf("channel %d is the node's own: it carries a battery voltage or a board temperature, not a %s", ChannelSelf, t.Name)
		}
		if e.Channel == 0 {
			return errors.New("channel 0 marks the end of the reply, so a decoder reads nothing after it")
		}
		if e.Channel > MaxChannel {
			return fmt.Errorf("channel %d is above %d, the highest this build publishes on", e.Channel, MaxChannel)
		}
		if e.SensorID == 0 || e.Metric == "" {
			return fmt.Errorf("the row on channel %d says nothing to publish", e.Channel)
		}
		key := [2]byte{e.Channel, e.Type}
		if seen[key] {
			return fmt.Errorf("channel %d already carries a %s", e.Channel, t.Name)
		}
		seen[key] = true
	}
	if n := telemetrySize(entries); n > MaxTelemetryPayload {
		return fmt.Errorf("this map needs %d bytes and a reply holds %d", n, MaxTelemetryPayload)
	}
	return nil
}

// encodeEntries appends the wanted rows' readings; a missing, failing or unread sensor is left out rather than sent stale.
func encodeEntries(enc *meshcore.LPPEncoder, entries []ChannelEntry, statuses []Status, want func(ChannelEntry) bool) {
	byID := make(map[int64]Status, len(statuses))
	for _, s := range statuses {
		byID[s.Spec.ID] = s
	}
	for _, e := range entries {
		if !want(e) {
			continue
		}
		st, ok := byID[e.SensorID]
		if !ok || st.Err != "" || st.At.IsZero() {
			continue
		}
		i := slices.IndexFunc(st.Readings, func(r Reading) bool { return r.Metric == e.Metric })
		if i < 0 {
			continue
		}
		v := st.Readings[i].Value
		if math.IsNaN(v) || math.IsInf(v, 0) {
			continue
		}
		addLPP(enc, e.Channel, e.Type, v)
	}
}

// addLPP writes one value, clamped to what its wire field holds so an over-range reading pegs rather than wraps.
func addLPP(enc *meshcore.LPPEncoder, channel, typ byte, v float64) {
	switch typ {
	case meshcore.LPPTemperature:
		enc.AddTemperature(channel, within(v, math.MinInt16/10.0, math.MaxInt16/10.0))
	case meshcore.LPPRelativeHumidity:
		enc.AddRelativeHumidity(channel, within(v, 0, math.MaxUint8/2.0))
	case meshcore.LPPBarometricPressure:
		enc.AddBarometricPressure(channel, within(v, 0, math.MaxUint16/10.0))
	case meshcore.LPPVoltage:
		enc.AddVoltage(channel, within(v, math.MinInt16/100.0, math.MaxInt16/100.0))
	case meshcore.LPPCurrent:
		enc.AddCurrent(channel, within(v, math.MinInt16/1000.0, math.MaxInt16/1000.0))
	case meshcore.LPPAnalogInput:
		enc.AddAnalogInput(channel, within(v, math.MinInt16/100.0, math.MaxInt16/100.0))
	case meshcore.LPPAnalogOutput:
		enc.AddAnalogOutput(channel, within(v, math.MinInt16/100.0, math.MaxInt16/100.0))
	case meshcore.LPPAltitude:
		enc.AddAltitude(channel, within(v, math.MinInt16, math.MaxInt16))
	case meshcore.LPPDistance:
		enc.AddDistance(channel, within(v, 0, math.MaxUint32/1000.0))
	case meshcore.LPPEnergy:
		enc.AddEnergy(channel, within(v, 0, math.MaxUint32/1000.0))
	case meshcore.LPPGenericSensor:
		enc.AddGenericSensor(channel, uint32(clamp(v, 0, math.MaxUint32)))
	case meshcore.LPPFrequency:
		enc.AddFrequency(channel, uint32(clamp(v, 0, math.MaxUint32)))
	case meshcore.LPPUnixTime:
		enc.AddUnixTime(channel, uint32(clamp(v, 0, math.MaxUint32)))
	case meshcore.LPPLuminosity:
		enc.AddLuminosity(channel, uint16(clamp(v, 0, math.MaxUint16)))
	case meshcore.LPPConcentration:
		enc.AddConcentration(channel, uint16(clamp(v, 0, math.MaxUint16)))
	case meshcore.LPPPower:
		enc.AddPower(channel, uint16(clamp(v, 0, math.MaxUint16)))
	case meshcore.LPPDirection:
		enc.AddDirection(channel, uint16(clamp(v, 0, 360)))
	case meshcore.LPPPercentage:
		enc.AddPercentage(channel, byte(clamp(v, 0, 100)))
	case meshcore.LPPDigitalInput:
		enc.AddDigitalInput(channel, byte(clamp(v, 0, math.MaxUint8)))
	case meshcore.LPPDigitalOutput:
		enc.AddDigitalOutput(channel, byte(clamp(v, 0, math.MaxUint8)))
	case meshcore.LPPPresence:
		enc.AddPresence(channel, boolByte(v))
	case meshcore.LPPSwitch:
		enc.AddSwitch(channel, boolByte(v))
	}
}

func clamp(v, lo, hi float64) float64 {
	return within(math.Round(v), lo, hi)
}

// within leaves a scaled value unrounded, since the encoder truncates it as the firmware's does.
func within(v, lo, hi float64) float64 {
	return math.Min(hi, math.Max(lo, v))
}

// boolByte makes a flag from a number; anything but zero is on.
func boolByte(v float64) byte {
	if v != 0 {
		return 1
	}
	return 0
}
