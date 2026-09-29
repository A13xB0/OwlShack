package sensor

import (
	"encoding/hex"
	"math"
	"strings"
	"testing"
	"time"

	meshcore "github.com/meshcore-go/meshcore-go"
)

func status(id int64, name string, at time.Time, err string, readings ...Reading) Status {
	return Status{Spec: Spec{ID: id, Name: name}, Readings: readings, At: at, Err: err}
}

func encode(t *testing.T, entries []ChannelEntry, statuses []Status) string {
	t.Helper()
	enc := meshcore.NewLPPEncoder()
	encodeEntries(enc, entries, statuses, everyRow)
	return strings.ToUpper(hex.EncodeToString(enc.Bytes()))
}

// The expected bytes come from the LPP wire format itself, not from what this package produces.
func TestEncodeTelemetry_WritesChannelTypeAndValue(t *testing.T) {
	now := time.Now()
	statuses := []Status{
		status(7, "air", now, "",
			Reading{Metric: Temperature, Value: 22.5, Unit: "C"},
			Reading{Metric: Humidity, Value: 55.5, Unit: "%"},
		),
	}
	entries := []ChannelEntry{
		{Channel: 2, Type: meshcore.LPPTemperature, SensorID: 7, Metric: Temperature},
		{Channel: 2, Type: meshcore.LPPRelativeHumidity, SensorID: 7, Metric: Humidity},
	}

	// 22.5C at 0.1 is 225 = 0x00E1; 55.5 %RH at 0.5 is 111 = 0x6F.
	const want = "026700E102686F"
	if got := encode(t, entries, statuses); got != want {
		t.Fatalf("encoded %s, want %s", got, want)
	}
}

func TestEncodeTelemetry_NegativeTemperatureStaysSigned(t *testing.T) {
	statuses := []Status{status(1, "cold", time.Now(), "", Reading{Metric: Temperature, Value: -5.6})}
	entries := []ChannelEntry{{Channel: 3, Type: meshcore.LPPTemperature, SensorID: 1, Metric: Temperature}}

	// -5.6C at 0.1 is -56, two's complement 0xFFC8.
	const want = "0367FFC8"
	if got := encode(t, entries, statuses); got != want {
		t.Fatalf("encoded %s, want %s", got, want)
	}
}

// Each skip is checked against a map known to encode when healthy, so an always-empty encoder fails.
func TestEncodeTelemetry_SkipsAReadingItCannotTrust(t *testing.T) {
	now := time.Now()
	entries := []ChannelEntry{{Channel: 2, Type: meshcore.LPPTemperature, SensorID: 7, Metric: Temperature}}
	healthy := Reading{Metric: Temperature, Value: 22.5}

	if got := encode(t, entries, []Status{status(7, "air", now, "", healthy)}); got == "" {
		t.Fatal("a healthy sensor encoded nothing, so the skip cases below prove nothing")
	}

	cases := []struct {
		name     string
		statuses []Status
	}{
		{"sensor is gone", []Status{status(9, "other", now, "", healthy)}},
		{"sensor is failing", []Status{status(7, "air", now, "bus error", healthy)}},
		{"sensor has never been read", []Status{status(7, "air", time.Time{}, "", healthy)}},
		{"sensor reports another metric", []Status{status(7, "air", now, "", Reading{Metric: Pressure, Value: 1013})}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := encode(t, entries, tc.statuses); got != "" {
				t.Fatalf("encoded %s, want nothing", got)
			}
		})
	}
}

// Inf would otherwise clamp to a type's ceiling and read as a real pegged measurement.
func TestEncodeTelemetry_SkipsAValueThatIsNotANumber(t *testing.T) {
	entries := []ChannelEntry{{Channel: 2, Type: meshcore.LPPGenericSensor, SensorID: 1, Metric: Resistance}}
	for _, v := range []float64{math.NaN(), math.Inf(1)} {
		statuses := []Status{status(1, "gas", time.Now(), "", Reading{Metric: Resistance, Value: v})}
		if got := encode(t, entries, statuses); got != "" {
			t.Fatalf("value %v encoded %s, want nothing", v, got)
		}
	}
}

func TestEncodeTelemetry_ClampsAnIntegerTypeRatherThanWrapping(t *testing.T) {
	statuses := []Status{status(1, "x", time.Now(), "", Reading{Metric: Percentage, Value: 180})}
	entries := []ChannelEntry{{Channel: 2, Type: meshcore.LPPPercentage, SensorID: 1, Metric: Percentage}}

	// 180 into a one-byte percentage pegs at 100 (0x64); wrapping would give 0xB4.
	const want = "027864"
	if got := encode(t, entries, statuses); got != want {
		t.Fatalf("encoded %s, want %s", got, want)
	}
}

func TestEncodeTelemetry_AppendsToTheNodesOwnChannel(t *testing.T) {
	enc := meshcore.NewLPPEncoder()
	enc.AddVoltage(ChannelSelf, 4.1)
	before := len(enc.Bytes())

	statuses := []Status{status(1, "air", time.Now(), "", Reading{Metric: Temperature, Value: 22.5})}
	encodeEntries(enc, []ChannelEntry{{Channel: 2, Type: meshcore.LPPTemperature, SensorID: 1, Metric: Temperature}}, statuses, everyRow)

	got := strings.ToUpper(hex.EncodeToString(enc.Bytes()))
	if !strings.HasPrefix(got, "0174") {
		t.Fatalf("the node's own channel was lost: %s", got)
	}
	if len(enc.Bytes()) != before+4 {
		t.Fatalf("appended %d bytes, want 4", len(enc.Bytes())-before)
	}
}

func TestTelemetrySize_CountsHeaderAndPayload(t *testing.T) {
	entries := []ChannelEntry{
		{Channel: 2, Type: meshcore.LPPTemperature},      // 2 + 2
		{Channel: 2, Type: meshcore.LPPRelativeHumidity}, // 2 + 1
		{Channel: 3, Type: meshcore.LPPGenericSensor},    // 2 + 4
	}
	if got := telemetrySize(entries); got != 13 {
		t.Fatalf("size %d, want 13", got)
	}
}

func TestValidateChannelMap_AcceptsAUsableMap(t *testing.T) {
	entries := []ChannelEntry{
		{Channel: 2, Type: meshcore.LPPTemperature, SensorID: 1, Metric: Temperature},
		{Channel: 2, Type: meshcore.LPPRelativeHumidity, SensorID: 1, Metric: Humidity},
		{Channel: 3, Type: meshcore.LPPTemperature, SensorID: 2, Metric: Temperature},
	}
	if err := ValidateChannelMap(entries); err != nil {
		t.Fatalf("refused a usable map: %v", err)
	}
}

func TestValidateChannelMap_Refuses(t *testing.T) {
	cases := []struct {
		name    string
		entries []ChannelEntry
		want    string
	}{
		{
			"the same type twice on one channel",
			[]ChannelEntry{
				{Channel: 2, Type: meshcore.LPPTemperature, SensorID: 1, Metric: Temperature},
				{Channel: 2, Type: meshcore.LPPTemperature, SensorID: 2, Metric: Temperature},
			},
			"already carries a Temperature",
		},
		{
			"a reading the node's own channel does not carry",
			[]ChannelEntry{{Channel: ChannelSelf, Type: meshcore.LPPRelativeHumidity, SensorID: 1, Metric: Humidity}},
			"the node's own",
		},
		{
			"a type this build cannot publish",
			[]ChannelEntry{{Channel: 2, Type: meshcore.LPPGPS, SensorID: 1, Metric: Temperature}},
			"not an LPP type",
		},
		{
			"a channel above what this build publishes on",
			[]ChannelEntry{{Channel: MaxChannel + 1, Type: meshcore.LPPTemperature, SensorID: 1, Metric: Temperature}},
			"highest this build publishes on",
		},
		{
			"channel 0, which a decoder reads as the end of the reply",
			[]ChannelEntry{{Channel: 0, Type: meshcore.LPPTemperature, SensorID: 1, Metric: Temperature}},
			"end of the reply",
		},
		{
			"a row naming no reading",
			[]ChannelEntry{{Channel: 2, Type: meshcore.LPPTemperature}},
			"says nothing to publish",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChannelMap(tc.entries)
			if err == nil {
				t.Fatal("accepted it")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

// The budget is the whole point of counting: a map that cannot be sent must not be saveable.
func TestValidateChannelMap_RefusesAMapTooBigForOnePacket(t *testing.T) {
	// Kept inside the channel range, so it is the budget that refuses this and not the ceiling.
	var entries []ChannelEntry
	for ch := ChannelSelf + 1; ch <= MaxChannel; ch++ {
		entries = append(entries, ChannelEntry{
			Channel: byte(ch), Type: meshcore.LPPGenericSensor, SensorID: 1, Metric: Resistance,
		})
	}
	if n := telemetrySize(entries); n <= MaxTelemetryPayload {
		t.Fatalf("the map only needs %d bytes, so this does not test the budget", n)
	}
	err := ValidateChannelMap(entries)
	if err == nil || !strings.Contains(err.Error(), "a reply holds") {
		t.Fatalf("accepted an oversized map: %v", err)
	}

	if err := ValidateChannelMap(entries[:20]); err != nil {
		t.Fatalf("refused a map that fits: %v", err)
	}
}

func TestDefaultLPPType_LeavesAnUnknownMetricToTheOperator(t *testing.T) {
	if _, ok := DefaultLPPType("moisture"); ok {
		t.Fatal("guessed a type for a metric only the operator understands")
	}
	// The firmware's power monitors publish these (EnvironmentSensorManager.cpp), so a consumer already reads them so.
	for m, want := range map[Metric]byte{Voltage: meshcore.LPPVoltage, Current: meshcore.LPPCurrent} {
		if code, ok := DefaultLPPType(m); !ok || code != want {
			t.Errorf("%s defaults to %d, want %d as the firmware sends it", m, code, want)
		}
	}
}

// The node's own channel has to be assignable: a Pi's battery is a PiSugar or an ADC divider, not the radio board the built-in reading comes from.
func TestValidateChannelMap_AcceptsTheNodesOwnTypesOnItsOwnChannel(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  byte
	}{
		{"a battery voltage", meshcore.LPPVoltage},
		{"a board temperature", meshcore.LPPTemperature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateChannelMap([]ChannelEntry{
				{Channel: ChannelSelf, Type: tc.typ, SensorID: 1, Metric: Voltage},
			})
			if err != nil {
				t.Fatalf("refused %s on the node's own channel: %v", tc.name, err)
			}
		})
	}
}

// A default type that cannot carry its metric's range wraps or pegs while still looking like a reading; these are each metric's worst case.
func TestDefaultLPPType_CarriesEachAirQualityRange(t *testing.T) {
	for _, tc := range []struct {
		metric Metric
		value  float64
		want   string
	}{
		// The index is clamped to 500, the static one is not, and both go out as unsigned 32-bit.
		{IAQ, 500, "0264000001F4"},
		{StaticIAQ, 8000, "026400001F40"},
		// Both ppm figures ride the unsigned concentration type, which reaches 65535.
		{CO2Equivalent, 13520, "027D34D0"},
		{BreathVOC, 1000, "027D03E8"},
		{GasPercentage, 100, "027864"},
		// Accuracy goes out as the firmware's own BSEC channel sends it, an analog input at 0.01.
		{IAQAccuracy, 3, "0202012C"},
		{GasPercentageAccuracy, 3, "0202012C"},
		{AirQualityRunIn, 1, "020001"},
	} {
		t.Run(string(tc.metric), func(t *testing.T) {
			code, ok := DefaultLPPType(tc.metric)
			if !ok {
				t.Fatalf("%s has no default type", tc.metric)
			}
			statuses := []Status{status(1, "air", time.Now(), "", Reading{Metric: tc.metric, Value: tc.value})}
			entries := []ChannelEntry{{Channel: 2, Type: code, SensorID: 1, Metric: tc.metric}}
			if got := encode(t, entries, statuses); got != tc.want {
				t.Errorf("%v as type %d encoded %s, want %s", tc.value, code, got, tc.want)
			}
		})
	}
}

// An expression can produce anything, and past a type's range the value has to peg at its limit rather than wrap to a plausible reading.
func TestEncodeTelemetry_PegsAValueOutsideTheTypesRange(t *testing.T) {
	for _, tc := range []struct {
		typ     byte
		v, want float64
	}{
		{meshcore.LPPRelativeHumidity, 130, 127.5},
		{meshcore.LPPRelativeHumidity, -1, 0},
		{meshcore.LPPBarometricPressure, -1, 0},
		{meshcore.LPPBarometricPressure, 7000, 6553.5},
		{meshcore.LPPTemperature, 5000, 3276.7},
		{meshcore.LPPTemperature, -5000, -3276.8},
		{meshcore.LPPAnalogInput, 400, 327.67},
		{meshcore.LPPAnalogOutput, -400, -327.68},
		{meshcore.LPPVoltage, 400, 327.67},
		{meshcore.LPPCurrent, 40, 32.767},
		{meshcore.LPPCurrent, -40, -32.768},
		{meshcore.LPPAltitude, 40000, 32767},
		{meshcore.LPPDistance, -1, 0},
		{meshcore.LPPEnergy, 1e12, 4294967.295},
	} {
		typ, _ := lookupLPPType(tc.typ)
		entries := []ChannelEntry{{Channel: 2, Type: tc.typ, SensorID: 1, Metric: Temperature}}
		enc := meshcore.NewLPPEncoder()
		encodeEntries(enc, entries, []Status{status(1, "x", time.Now(), "", Reading{Metric: Temperature, Value: tc.v})}, everyRow)
		got, err := meshcore.LPPDecode(enc.Bytes())
		if err != nil || len(got) != 1 {
			t.Fatalf("%s %v: decoded %v, %v", typ.Name, tc.v, got, err)
		}
		if v, _ := got[0].Value.(float64); math.Abs(v-tc.want) > typ.Step*1.01 {
			t.Errorf("%s %v went out as %v, want it pegged at %v", typ.Name, tc.v, v, tc.want)
		}
	}
}

// everyRow wants every row, as a reply with every class allowed does.
func everyRow(ChannelEntry) bool { return true }
