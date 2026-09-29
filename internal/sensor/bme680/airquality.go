package bme680

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// § cites Bosch's BSEC 1.4.9.2 behavioural specification, kept outside the repo; S9 and S10 are left out (zero offset at any period this is polled at), and float64 stands in for its float32.

// Defaults from §10.7's 1.8 V / 300 s profile, all fixed since no vendor configuration blob is loaded.
const (
	// gasFloorOhms is where §11.2 floors the resistance before taking its logarithm.
	gasFloorOhms = 10.0

	cutoffGasHz  = 0.02
	cutoffTempHz = 0.0025
	cutoffHumHz  = 0.0025

	// stabiliseThresholdSec is 0 in every vendor configuration, so output 12 reads 1 from the first sample.
	stabiliseThresholdSec = 0.0
	// matureThresholdSec is §10.7's 4.32e13 ns.
	matureThresholdSec = 43200.0

	indexMin    = 0.0
	indexMax    = 500.0
	indexSpan   = 150.0
	indexOffset = 50.0

	co2SlopeLow    = 4.0
	co2SlopeHigh   = 10.0
	co2BaselinePpm = 400.0
	// co2Threshold is the coded crossover, not the algebraic 400/6, which is what puts a -0.04 ppm step in the curve.
	co2Threshold  = 66.66
	vocHighPpm    = 15.0
	vocLowPpm     = 0.5
	vocMinPpm     = 0.1
	vocMaxPpm     = 1000.0
	gasPercentMin = 0.0

	tempMin, tempMax = -40.0, 85.0
	humMin, humMax   = 0.0, 100.0

	// Printed decimals, not the logarithms: the nearest float to each log is a different number, and step scales every threshold below.
	stepPerRangeScale = 0.0969100073
	rangeScaleDivisor = 0.221848726
	outwardPerHorizon = 0.39794001

	// initialHorizonSec is the horizon both bands use until band 1 is calibrated and the part has matured.
	initialHorizonSec = 1800.0
	minAbsHumidity    = 0.01
)

// calibHorizonSec is the inward decay constant per band; maturedHorizonSec replaces the working horizon once band 1 is calibrated.
var (
	calibHorizonSec   = [2]float64{86400, 64800000}
	maturedHorizonSec = [2]float64{3600, 3600}
)

// mode is the parameter set a sampling period selects (§6.3), in §11.4.2's column order; any unsanctioned period, 30 s included, falls to modeSlow.
type mode int

const (
	modeSlow mode = iota
	mode18s
	mode3s
	mode1s
)

var (
	kMode         = [4]float64{0.7746, 0.60, 0.64, 0.60}
	runInSec      = [4]float64{1200, 60, 300, 300}
	humiditySlope = [4]float64{-0.501, -0.4, -0.75, -0.29}
	// tempCoef is unreachable from configuration, so 1 s mode carries a temperature term the others do not.
	tempCoef = [4]float64{0, 0, 0, -0.1}
	gainQuad = [4][3]float64{{0, 0, 1}, {0, 0, 1}, {0, 0, 1}, {-0.0192, 0.0992, 0.9253}}
	gainLin  = [4][2]float64{{0, 1}, {0, 1}, {0, 1}, {0.9918, 0.9846}}
	refTemp  = [4]float64{20, 40, 20, 40}
	refHum   = [4]float64{40, 20, 40, 20}
)

func modeFor(period time.Duration) mode {
	switch period {
	case 18 * time.Second:
		return mode18s
	case 3 * time.Second:
		return mode3s
	case time.Second:
		return mode1s
	}
	return modeSlow
}

// AirQuality is one sample's result; Accuracy covers the first four values and GasAccuracy the gas percentage, from 0 in run-in to 3 calibrated.
type AirQuality struct {
	IAQ            float64
	StaticIAQ      float64
	CO2Equivalent  float64
	BreathVOCEquiv float64
	GasPercentage  float64
	Accuracy       int
	GasAccuracy    int
	RunIn          bool
	// RunInLeft is how much more sampling run-in needs, zero once done; a gap in the samples starts it over.
	RunInLeft time.Duration
	// Stabilised is specification output 12, whose threshold ships at zero: it is true from the first sample.
	Stabilised bool
}

// tracker is one part's fusion pipeline and its learned state; the driver serialises access under its own lock.
type tracker struct {
	period time.Duration
	mode   mode

	alpha        [3]float64
	runInSec     float64
	maxGapSec    float64
	rangeScale   float64
	promoteCount uint8
	staticSlope  float64
	vocSlope     float64
	vocIntercept float64

	st trackerState
	// last is this run's previous sample, whose monotonic reading an NTP step cannot move.
	last time.Time
}

// newTracker sizes the filters, run-in window and promotion count for the period; a period BSEC has no mode for takes the 300 s coefficients (§6.3).
func newTracker(period time.Duration) *tracker {
	t := &tracker{st: newTrackerState()}
	t.setPeriod(period)
	return t
}

func (t *tracker) setPeriod(period time.Duration) {
	t.period = period
	t.mode = modeFor(period)
	sec := period.Seconds()

	t.alpha = [3]float64{
		coefficient(cutoffGasHz, sec),
		coefficient(cutoffTempHz, sec),
		coefficient(cutoffHumHz, sec),
	}
	t.runInSec = runInSec[t.mode]
	t.maxGapSec = 1.5 * sec

	k := math.Abs(math.Log10(kMode[t.mode]))
	t.rangeScale = k / rangeScaleDivisor
	t.staticSlope = indexSpan / k
	// Bounded: past 20 minutes the count rounds to zero and would call a mid-band sample calibrated, and under a second it overflows its byte.
	count := math.Floor(2 * math.Sqrt(math.Round(300/sec)))
	t.promoteCount = uint8(math.Min(math.Max(count, 1), 255))

	t.vocSlope = (math.Log10(vocHighPpm) - math.Log10(vocLowPpm)) / indexSpan
	t.vocIntercept = math.Log10(vocHighPpm) - (indexSpan+indexOffset)*t.vocSlope
}

// stepSize is §11.4.1's step, derived at each use as the specification requires, so it can never be left over from another rate.
func (t *tracker) stepSize() float64 { return t.rangeScale * stepPerRangeScale }

// observe runs one sample through the four stages; a gap over 1.5 periods, or a clock gone backwards, discards the run-in progress (§11.3).
func (t *tracker) observe(gasOhms, tempC, rh float64, at time.Time) AirQuality {
	stabilised, runIn, matured, softStart, dt := t.monitorRunIn(at)
	gas, temp, hum := t.condition(gasOhms, tempC, rh)
	compensated, band := t.track(gas, temp, hum, stabilised, runIn, matured, dt)
	out := t.mapIndex(compensated, band, softStart, runIn, stabilised)
	if !runIn {
		out.RunInLeft = time.Duration(math.Ceil(t.runInSec-t.st.RunInAccumSec) * float64(time.Second))
	}
	return out
}

// coefficient turns a cut-off frequency into a one-pole smoothing factor (§11.1); at or above Nyquist, as every default is at 300 s, the sample passes through.
func coefficient(cutoffHz, periodSec float64) float64 {
	nyquist := (1 / periodSec) * 0.5
	bw := math.Min(cutoffHz/nyquist, 1.0)
	if bw == 1.0 {
		return 1.0
	}
	y := math.Sin(bw * math.Pi / 2)
	y *= y
	return 2 * (math.Sqrt(y*y+y) - y)
}

// monitorRunIn is S5 (§11.3): three accumulators latching a flag each, and the soft-start ramp; dt is what this sample carries, zero across a gap.
func (t *tracker) monitorRunIn(at time.Time) (stabilised, runIn, matured bool, softStart, dt float64) {
	st := &t.st
	elapsed := at.Sub(t.last).Seconds()
	switch {
	case t.last.IsZero():
		elapsed = 0
	case elapsed > t.maxGapSec || elapsed < 0:
		st.RunInAccumSec, st.RunIn = 0, false
		elapsed = 0
	}

	if !st.Stabilised {
		st.StabiliseAccumSec += elapsed
		if st.StabiliseAccumSec >= stabiliseThresholdSec {
			st.Stabilised = true
		}
	}
	if !st.Matured {
		st.MatureAccumSec += elapsed
		if st.MatureAccumSec >= matureThresholdSec {
			st.Matured = true
		}
	}
	if !st.RunIn {
		st.RunInAccumSec += elapsed
		if st.RunInAccumSec >= t.runInSec {
			st.RunIn = true
		}
	}
	t.last = at

	softStart = 1.0
	if !st.RunIn && t.runInSec > 0 {
		softStart = st.RunInAccumSec / t.runInSec
	}
	return st.Stabilised, st.RunIn, st.Matured, softStart, elapsed
}

// restartRunIn is §11.3's reset for a timestamp that went backwards: run-in starts over and the next sample accrues nothing.
func (t *tracker) restartRunIn() {
	t.st.RunIn, t.st.RunInAccumSec, t.last = false, 0, time.Time{}
}

// condition is S3 (§11.2): the gas goes to log10 and all three signals are low-passed.
func (t *tracker) condition(gasOhms, tempC, rh float64) (gas, temp, hum float64) {
	g := math.Log10(max(gasOhms, gasFloorOhms))
	if t.st.Smoothed[0] == 0 {
		t.st.Smoothed = [3]float64{g, tempC, rh}
	}
	out := [3]float64{
		t.st.Smoothed[0] + t.alpha[0]*(g-t.st.Smoothed[0]),
		t.st.Smoothed[1] + t.alpha[1]*(tempC-t.st.Smoothed[1]),
		t.st.Smoothed[2] + t.alpha[2]*(rh-t.st.Smoothed[2]),
	}
	t.st.Smoothed = out
	return out[0], out[1], out[2]
}

// bands is §11.4.7's reported bundle, an unseeded edge substituted so no consumer sees a zero and the band is a step wide from the first sample.
type bands struct {
	max      [2]float64
	min      [2]float64
	accuracy [2]float64
}

// track is S1 (§11.4): it compensates the gas signal for humidity, moves both bands towards it, and rates how far each can be trusted.
func (t *tracker) track(gas, temp, hum float64, stabilised, runIn, matured bool, dt float64) (float64, bands) {
	st := &t.st
	compensated := t.compensate(gas, temp, hum)
	step := t.stepSize()

	frozen, forceZero := false, false
	switch {
	case temp < tempMin || temp > tempMax || hum < humMin || hum > humMax:
		frozen = true
	case !runIn:
		frozen, forceZero = true, true
	}
	if !stabilised {
		forceZero = true
	}

	if !frozen {
		for b := range 2 {
			if st.BandMax[b] == 0 {
				st.BandMax[b] = compensated
			}
			if st.BandMin[b] == 0 && st.BandMax[b]-step > compensated {
				st.BandMin[b] = compensated
			}
			t.robustUpdate(compensated, &st.BandMax[b], false, b, 1, matured, dt)
			if st.BandMin[b] != 0 {
				t.robustUpdate(compensated, &st.BandMin[b], true, b, 0, matured, dt)
			}
		}
	}

	if forceZero {
		st.Accuracy = [2]float64{0, 0}
		st.AccuracyCounter = [2]uint8{t.promoteCount, t.promoteCount}
	} else {
		for b := range 2 {
			atExtreme := st.BandMax[b] <= compensated || st.BandMin[b] >= compensated
			st.Accuracy[b] = t.rateAccuracy(atExtreme, t.bandPosition(compensated, b), b)
		}
	}

	out := bands{max: st.BandMax, min: st.BandMin, accuracy: st.Accuracy}
	for b := range 2 {
		if out.max[b] == 0 {
			out.max[b] = compensated
		}
		if out.min[b] == 0 {
			out.min[b] = out.max[b] - step
		}
	}

	if st.HorizonNotYetSwitched {
		if st.Accuracy[0] == 3 && matured {
			st.WorkingHorizonSec = maturedHorizonSec
			st.HorizonNotYetSwitched = false
		} else {
			st.WorkingHorizonSec = [2]float64{initialHorizonSec, initialHorizonSec}
		}
	}
	return compensated, out
}

// compensate is §11.4.2 in log10 ohms, plus bme680.compensateGas's reference term: a constant per mode, which every downstream difference cancels.
func (t *tracker) compensate(gas, temp, hum float64) float64 {
	m := t.mode
	logAH := math.Log10(math.Max(absoluteHumidity(temp, hum), minAbsHumidity))
	logRef := math.Log10(absoluteHumidity(refTemp[m], refHum[m]))

	scale := 1.0
	if t.st.BandMax[0] != 0 {
		d := logAH - logRef
		gainH := gainQuad[m][0]*d*d + gainQuad[m][1]*d + gainQuad[m][2]
		gainT := gainLin[m][0]*((temp-refTemp[m])/100) + gainLin[m][1]
		scale = math.Max(math.Min(gainH*gainT, 1.5), 0.5)
	}
	comp := gas - (temp*tempCoef[m]/100 + (logAH-logRef)*humiditySlope[m])
	return (comp-t.st.BandMax[0])*scale + t.st.BandMax[0]
}

// absoluteHumidity is the water in the air in g/m3, by the Magnus expression of §11.4.2.
func absoluteHumidity(tempC, rh float64) float64 {
	return ((rh * 6.112 / 100.0) * math.Exp(17.62*tempC/(243.12+tempC)) / (tempC + 273.15)) * 216.7
}

// compensateGas normalises a gas resistance to reference air so readings in different air compare; the plate moves about 29% per doubling of water.
func compensateGas(ohms, tempC, rh float64) float64 {
	ah := math.Max(absoluteHumidity(tempC, rh), minAbsHumidity)
	return ohms * math.Pow(ah/absoluteHumidity(refTemp[modeSlow], refHum[modeSlow]), -humiditySlope[modeSlow])
}

// robustUpdate is §11.4.4: a band edge moves out at a capped rate and relaxes in by first-order decay, a minimum mirrored so one routine does both.
func (t *tracker) robustUpdate(x float64, slot *float64, isMin bool, band, horizonSel int, matured bool, dt float64) {
	st := &t.st
	step := t.stepSize()

	v, bound := *slot, step+st.BandMin[band]
	if isMin {
		v, x, bound = -v, -x, step-st.BandMax[band]
	}
	delta := x - v

	threshold := step*0.3 + step
	if st.Accuracy[band] == 3 {
		u := 1 - math.Max(st.BandMax[band]-x, 0)
		threshold = step*(u*u*0.75*0.2+0.1) + step
	}

	if delta > 0 {
		horizon := initialHorizonSec
		if band == 0 {
			horizon = st.WorkingHorizonSec[horizonSel]
		}
		if !matured && delta > threshold {
			horizon = initialHorizonSec
		}
		if maxRise := t.rangeScale * outwardPerHorizon / horizon * dt; delta > maxRise {
			delta = maxRise
		}
	} else {
		tau := calibHorizonSec[band]
		if !matured && math.Abs(delta) > threshold && band == 0 {
			tau = calibHorizonSec[0] / 3
		}
		delta *= 1 - math.Exp(-dt/tau)
	}

	if v += delta; v < bound {
		v = bound
	}
	if isMin {
		v = -v
	}
	*slot = v
}

// bandPosition is how far inside its band the signal sits, 0 at or beyond either edge and 1 mid-band (§11.4.5).
func (t *tracker) bandPosition(x float64, band int) float64 {
	hi, lo := t.st.BandMax[band], t.st.BandMin[band]
	span := t.stepSize()
	if lo != 0 {
		span = math.Max(hi-lo, span)
	}
	d := math.Min(math.Max(hi-x, 0), math.Max(x-lo, 0))
	return math.Min((d+d)/span, 1)
}

// rateAccuracy is §11.4.5 and never returns 0, which run-in writes directly with the counter preloaded, so accuracy 3 returns at once afterwards.
func (t *tracker) rateAccuracy(atExtreme bool, position float64, band int) float64 {
	st := &t.st
	f := 0.3
	if st.Accuracy[band] == 3 {
		u := 1 - position
		f = u*u*0.75*0.2 + 0.1
	}
	if st.BandMax[band]-st.BandMin[band] < t.stepSize()*(1+f) || st.BandMin[band] == 0 {
		st.AccuracyCounter[band] = t.promoteCount
		return 1
	}
	if atExtreme {
		st.AccuracyCounter[band] = 0
		return 2
	}
	if st.AccuracyCounter[band] == 255 {
		return 3
	}
	st.AccuracyCounter[band]++
	if st.AccuracyCounter[band] < t.promoteCount {
		return 2
	}
	return 3
}

// mapIndex is S2 (§11.5): the tracked band becomes the index, the static index and the three quantities derived from them.
func (t *tracker) mapIndex(compensated float64, bd bands, softStart float64, runIn, stabilised bool) AirQuality {
	st := &t.st
	high, low := bd.max[0], bd.min[0]
	gasHigh, gasLow := bd.max[1], bd.min[1]

	index := clamp(indexSpan*((high-compensated)/(high-low))+indexOffset, indexMin, indexMax)
	staticIndex := math.Max(indexOffset+(high-compensated)*t.staticSlope, indexMin)

	// While the soft start is below 1 the stored values stay put, so the outputs ease away from 50.
	if softStart < 1 {
		a := softStart * softStart
		if st.PreviousIndex < index {
			index = a*index + (1-a)*st.PreviousIndex
		}
		if st.PreviousStaticIndex < staticIndex {
			staticIndex = a*staticIndex + (1-a)*st.PreviousStaticIndex
		}
	} else {
		st.PreviousIndex, st.PreviousStaticIndex = index, staticIndex
	}

	co2 := math.Max(co2BaselinePpm, co2SlopeHigh*staticIndex)
	if staticIndex <= co2Threshold {
		co2 = math.Max(co2BaselinePpm, co2BaselinePpm+co2SlopeLow*staticIndex)
	}

	return AirQuality{
		IAQ:            index,
		StaticIAQ:      staticIndex,
		CO2Equivalent:  co2,
		BreathVOCEquiv: clamp(math.Pow(10, t.vocSlope*staticIndex+t.vocIntercept), vocMinPpm, vocMaxPpm),
		GasPercentage:  clamp(100*((gasHigh-compensated)/(gasHigh-gasLow)), gasPercentMin, 100),
		Accuracy:       int(bd.accuracy[0]),
		GasAccuracy:    int(bd.accuracy[1]),
		RunIn:          runIn,
		Stabilised:     stabilised,
	}
}

func clamp(v, lo, hi float64) float64 { return math.Min(hi, math.Max(lo, v)) }

// stateVersion guards the stored shape: another version's blob is refused, not half-read, since a partly filled band pins the index without looking wrong.
const stateVersion = 3

// trackerState is what a restart carries (§10.8): extremes, accuracy and its counters, warm-up accumulators, smoothed signals; rate constants are rebuilt.
type trackerState struct {
	Version  int        `json:"version"`
	Smoothed [3]float64 `json:"smoothed"`

	StabiliseAccumSec float64 `json:"stabiliseAccum"`
	RunInAccumSec     float64 `json:"runInAccum"`
	MatureAccumSec    float64 `json:"matureAccum"`
	Stabilised        bool    `json:"stabilised"`
	RunIn             bool    `json:"runIn"`
	Matured           bool    `json:"matured"`

	BandMax               [2]float64 `json:"bandMax"`
	BandMin               [2]float64 `json:"bandMin"`
	Accuracy              [2]float64 `json:"accuracy"`
	AccuracyCounter       [2]uint8   `json:"accuracyCounter"`
	WorkingHorizonSec     [2]float64 `json:"workingHorizon"`
	HorizonNotYetSwitched bool       `json:"horizonPending"`

	PreviousIndex       float64 `json:"previousIndex"`
	PreviousStaticIndex float64 `json:"previousStaticIndex"`
}

// newTrackerState is §12.0's initial state, which is also what §11.4.8's reset produces.
func newTrackerState() trackerState {
	return trackerState{
		Version:               stateVersion,
		AccuracyCounter:       [2]uint8{2, 2},
		WorkingHorizonSec:     [2]float64{initialHorizonSec, initialHorizonSec},
		HorizonNotYetSwitched: true,
		PreviousIndex:         indexOffset,
		PreviousStaticIndex:   indexOffset,
	}
}

// savedState is a tracker's state with the part it was learned on, kept outside trackerState so a reset cannot clear it.
type savedState struct {
	trackerState
	Chip   string        `json:"chip"`
	Period time.Duration `json:"period"`
}

// marshalState is the learned calibration for the host to keep; without it a restart relearns the clean and polluted extremes, which takes hours.
func (t *tracker) marshalState(chip string) ([]byte, error) {
	b, err := json.Marshal(savedState{t.st, chip, t.period})
	if err != nil {
		return nil, fmt.Errorf("bme680: encoding air-quality state: %w", err)
	}
	return b, nil
}

// restoreState refuses a blob it cannot vouch for, leaving the pipeline relearning rather than trusting a number that would never look wrong again.
func (t *tracker) restoreState(b []byte, chip string) error {
	var s savedState
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("bme680: decoding air-quality state: %w", err)
	}
	if s.Version != stateVersion {
		return fmt.Errorf("bme680: air-quality state is version %d, this build writes %d", s.Version, stateVersion)
	}
	// A blob missing fields decodes as zeros, and a zero horizon divides the outward rate limit.
	for _, h := range s.WorkingHorizonSec {
		if h <= 0 {
			return fmt.Errorf("bme680: air-quality state holds a working horizon of %v seconds", h)
		}
	}
	// Another part's extremes are not this one's, and applied here they read as air that never changes.
	if s.Chip != chip {
		return fmt.Errorf("bme680: the stored air-quality state was learned on another part (%s, this one is %s)", s.Chip, chip)
	}
	// The bands are in the compensation of the rate they were learned at, and another mode's constants misread them.
	if s.Period != t.period {
		return fmt.Errorf("bme680: the stored air-quality state was learned sampling every %s, this part samples every %s", s.Period, t.period)
	}
	t.st = s.trackerState
	return nil
}
