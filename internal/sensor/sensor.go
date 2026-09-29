// Package sensor polls the configured sensors through a Hub that holds each one's last result.
package sensor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Provider is one origin of sensors; NewHub takes them explicitly, so a test builds exactly the set it exercises.
type Provider interface {
	// ID is the stable key stored against every sensor this provider opened.
	ID() string
	// Label names the provider in the picker.
	Label() string
	// Available reports whether this provider can run here; "found nothing" and "could not look" must stay apart.
	Available(ctx context.Context) (bool, string)
	// Kinds lists what this provider can open and the options each needs, including parts no scan can see.
	Kinds() []KindInfo
	// Discover lists what could be added right now. It must not configure or claim anything.
	Discover(ctx context.Context) ([]Candidate, error)
	// Open turns a stored spec into a live sensor.
	Open(spec Spec) (Sensor, error)
}

// Sensor is one opened source of readings.
type Sensor interface {
	Read(ctx context.Context) ([]Reading, error)
}

// Validator adds rules a field declaration cannot express; it must not touch hardware.
type Validator interface {
	Validate(spec Spec) error
}

// Claimer names the hardware a spec takes exclusive use of; two specs may not claim the same, and an empty claim is shareable.
type Claimer interface {
	Claim(spec Spec) string
}

// Staler is a provider whose sensors go out of date on their own schedule, such as a web fetch every ten minutes; the rest go stale by the poll.
type Staler interface {
	StaleAfter(spec Spec) (time.Duration, bool)
}

// Tester is a provider that can try an unsaved spec once, so a form can show what a sensor would read before it is added.
type Tester interface {
	Test(ctx context.Context, spec Spec) TestResult
}

// TestResult is one try: what answered, the reply to pick values from, and each value or why it failed.
type TestResult struct {
	// Err is why the try as a whole failed; the values that were read are still there beside it.
	Err         string
	Status      string
	ContentType string
	Body        string
	Truncated   bool
	Values      []TestValue
}

// TestValue is one configured value as the try read it; Err set means Value is meaningless.
type TestValue struct {
	Metric Metric
	Value  float64
	Unit   string
	Err    string
}

// Binder is a provider whose sensors read other sensors; the hub hands it a snapshot of itself.
type Binder interface {
	Bind(snapshot func() []Status)
}

// Field is one option a kind needs, declared so the UI can build a form for a provider it knows nothing about.
type Field struct {
	// Key is the Options entry this field writes.
	Key   string
	Label string
	// Help is one line under the input, empty when the label says enough.
	Help string
	// Default is filled in when the operator leaves the field alone.
	Default string
	// Choices constrains the value; empty where an operator may type something unlisted, such as an address behind a multiplexer.
	Choices []string
	// Required rejects a spec that leaves this empty and has no default.
	Required bool
	// Multiline means the value wants room to grow, such as an expression.
	Multiline bool
	// Identifies means the value says which part this is rather than how it is tuned.
	Identifies bool
	// Secret keeps the value off every read of the sensor, so a page shows only that one is set.
	Secret bool
	// When shows the field only while an earlier field has one of these values; hidden, it is dropped on save.
	When *When
	// Type is FieldDuration or FieldList, or empty for free text.
	Type string
	// Min and Max bound a duration; Max zero is unbounded.
	Min, Max time.Duration
	// AtLeast names an earlier duration field this one may not be shorter than.
	AtLeast string
	// Columns are a list field's inputs.
	Columns []Column
	// Tested means a test reads this list's rows, so the form puts the test just above it and shows each row's result.
	Tested bool
}

// When is the condition a field is shown under.
type When struct {
	Key    string
	Values []string
}

// Shown says whether the field applies under these options.
func (f Field) Shown(options map[string]string) bool {
	return f.When == nil || slices.Contains(f.When.Values, options[f.When.Key])
}

// Binding is one reading an expression uses under a name of the operator's choosing, so a rename cannot break it.
type Binding struct {
	// Name is what the expression calls this value.
	Name string
	// SensorID is the sensor read, and the only part the dependency graph uses.
	SensorID int64
	// Metric picks which reading, for a sensor that reports more than one.
	Metric Metric
}

// KindInfo is one entry in the parts catalogue, which has to be searchable by whatever the operator knows.
type KindInfo struct {
	Kind string
	// Label is the part as it is printed on the chip, such as "SHTC3".
	Label string
	// Provider is filled in by the hub so one flat catalogue can span every provider.
	Provider string
	// Description says what the part measures, in the operator's words.
	Description string
	// Category groups the catalogue, such as "Environment" or "Power".
	Category string
	// Metrics is what the part reports, so the catalogue can be searched by need rather than part number.
	Metrics []Metric
	// ReportsUnder, when set, is what a sensor of this kind reports under its options, as a BME680 with its heater off reports no gas.
	ReportsUnder func(options map[string]string) []Metric
	// Binds says this kind reads other sensors, so the form offers a binding editor.
	Binds  bool
	Fields []Field
	// Testable is filled in by the hub for a provider that can try a spec before it is saved.
	Testable bool
}

// ProviderProblem travels beside the results: one unavailable bus must not hide what the others found.
type ProviderProblem struct {
	Provider string
	Label    string
	Reason   string
}

// DiscoverResult keeps what a scan found apart from what it could not look at.
type DiscoverResult struct {
	Candidates []Candidate
	Problems   []ProviderProblem
}

// Spec is a configured sensor: what the operator picked, as stored.
type Spec struct {
	ID       int64
	Provider string
	Kind     string
	Name     string
	// Options are provider-specific and opaque to everything else, which is what keeps the add form generic.
	Options map[string]string
	// Bindings are the other sensors this one reads. Empty for anything that talks to hardware.
	Bindings []Binding
}

// Candidate is something a provider found. Nothing is configured from it until the operator says so.
type Candidate struct {
	Kind string
	// Provider is filled in by the hub, so a candidate carries everything needed to configure it.
	Provider string
	Label    string
	Detail   string
	// Addable is always set: a part found but undrivable must not leave the bus looking empty.
	Addable bool
	// UsedBy names the configured sensor already on this part, and is empty while the part is free.
	UsedBy  string
	Options map[string]string
}

// Metric names what a reading measures; the set is open and the UI renders the name as given.
type Metric string

const (
	Temperature Metric = "temperature"
	Humidity    Metric = "humidity"
	Pressure    Metric = "pressure"
	Voltage     Metric = "voltage"
	Current     Metric = "current"
	Resistance  Metric = "resistance"
	// GasCompensated is a gas resistance normalised to reference air, which the raw one is not comparable without.
	GasCompensated Metric = "gas_compensated"
	Percentage     Metric = "percentage"

	// Derived by the air-quality fusion, not measured: IAQ is the 0-500 index, and StaticIAQ the same without scaling to the tracked band, so two parts compare.
	IAQ       Metric = "iaq"
	StaticIAQ Metric = "static_iaq"
	// CO2Equivalent and BreathVOC are inferred from the same signal, not measured: a gas sensor cannot tell one molecule from another.
	CO2Equivalent Metric = "co2_equivalent"
	BreathVOC     Metric = "breath_voc"
	GasPercentage Metric = "gas_percentage"
	// IAQAccuracy and GasPercentageAccuracy say how far the part has calibrated, 0 to 3; below 2 it has not seen both clean and polluted air.
	IAQAccuracy           Metric = "iaq_accuracy"
	GasPercentageAccuracy Metric = "gas_percentage_accuracy"
	// AirQualityRunIn is 1 once the plate has been heated long enough for the index to mean anything.
	AirQualityRunIn Metric = "air_quality_run_in"
	// AirQualityRunInLeft is the seconds of run-in still to go, reported only while it runs, and in no part's metrics so no channel map offers a countdown to the mesh.
	AirQualityRunInLeft Metric = "air_quality_run_in_left"
)

// Metrics is what the framework itself knows about, not everything a reading may carry.
func Metrics() []Metric {
	return []Metric{
		Temperature, Humidity, Pressure, Voltage, Current, Resistance, GasCompensated, Percentage,
		IAQ, StaticIAQ, CO2Equivalent, BreathVOC, GasPercentage,
		IAQAccuracy, GasPercentageAccuracy, AirQualityRunIn,
	}
}

// formats marks the readings that are not measurements; every other metric, an operator's own included, is a number.
var formats = map[Metric]string{
	Charging: "flag", Plugged: "flag", AirQualityRunIn: "flag",
	IAQAccuracy: "count", GasPercentageAccuracy: "count",
}

// FormatOf says how a reading reads: "flag" is a yes or no carried as 1 or 0, "count" a whole number, "number" anything else.
func FormatOf(m Metric) string {
	if f, ok := formats[m]; ok {
		return f
	}
	return "number"
}

// roles marks the readings that are not a card's headline; every other metric, an operator's own included, is one.
var roles = map[Metric]string{
	Resistance: "detail", GasCompensated: "detail", StaticIAQ: "detail",
	CO2Equivalent: "detail", BreathVOC: "detail", GasPercentage: "detail",
	IAQAccuracy: "calibration", GasPercentageAccuracy: "calibration", AirQualityRunIn: "calibration",
	AirQualityRunInLeft: "calibration",
}

// RoleOf says where a reading belongs on a card: "headline" shown large, "detail" in a list, "calibration" read as how far a fusion has learned.
func RoleOf(m Metric) string {
	if r, ok := roles[m]; ok {
		return r
	}
	return "headline"
}

// Reading is one measurement; Label separates readings a sensor reports more than one of, and is empty otherwise.
type Reading struct {
	Metric Metric
	Label  string
	Value  float64
	Unit   string
}

// Status keeps three states apart: At zero is never polled, At set is a good read, Err set is a failed attempt over the last good one.
type Status struct {
	Spec     Spec
	Readings []Reading
	At       time.Time
	Err      string
	// StaleAfter is how old a reading may be before it is out of date, and zero where the poll decides.
	StaleAfter time.Duration
	// RetryAt is when a sensor that failed to open is next tried, and zero when it is open or has not been tried.
	RetryAt time.Time
}

// ProviderInfo is one provider's entry in the picker.
type ProviderInfo struct {
	ID        string
	Label     string
	Available bool
	// Reason is why the provider is unavailable, empty when it is available.
	Reason string
}

// Validate rejects a spec that names nothing openable; a sensor with no provider would be stored and never read.
func (s Spec) Validate() error {
	if s.Provider == "" {
		return errors.New("provider is required")
	}
	if s.Kind == "" {
		return errors.New("kind is required")
	}
	if s.Name == "" {
		return errors.New("name is required")
	}
	if n := utf8.RuneCountInString(s.Name); n > maxNameLen {
		return fmt.Errorf("name is %d characters, and %d is the most", n, maxNameLen)
	}
	if hasControl(s.Name, false) {
		return errors.New("name has a control character in it")
	}
	return nil
}

// Caps on what an operator types, far past any real name or expression.
const (
	maxNameLen   = 64
	maxOptionLen = 1024
)

// hasControl finds a character that only garbles a label, or reorders the text after it; a multiline field keeps its line breaks and tabs.
func hasControl(s string, multiline bool) bool {
	return strings.ContainsFunc(s, func(r rune) bool {
		if multiline && (r == '\n' || r == '\r' || r == '\t') {
			return false
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r)
	})
}
