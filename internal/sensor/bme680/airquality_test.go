package bme680

import (
	"math"
	"strings"
	"testing"
	"time"
)

// pollPeriod is what the hub polls at, and the period every test here runs at unless it says otherwise.
const pollPeriod = 30 * time.Second

var epoch = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// The specification prints float32 and this computes in float64, so the two agree to about seven digits and no further.
func nearSpec(got, want float64) bool { return near(got, want, math.Abs(want)*1e-6) }

// run feeds n samples of one gas resistance, period apart, and returns the last output.
func run(tr *tracker, ohms float64, n int, from time.Time) (AirQuality, time.Time) {
	var out AirQuality
	at := from
	for range n {
		out = tr.observe(ohms, 25, 40, at)
		at = at.Add(tr.period)
	}
	return out, at
}

// runInSamples is how many 30 s samples accrue the 1200 s run-in; the first sample accrues nothing.
const runInSamples = 41

func TestCoefficient_MatchesTheWorkedTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		cutoff, period, want float64
	}{
		{0.02, 300, 1.0},
		{0.02, 3, 0.311062},
		{0.02, 1, 0.117943},
		{0.0025, 300, 1.0},
		{0.0025, 3, 0.0460225},
		{0.0025, 1, 0.0155849},
		{0.0127459997, 3, 0.212671},
		{0.0127459997, 1, 0.0769231},
	} {
		if got := coefficient(tc.cutoff, tc.period); !near(got, tc.want, 1e-6) {
			t.Errorf("coefficient(%v Hz, %vs) = %.7f, want %.7f", tc.cutoff, tc.period, got, tc.want)
		}
	}
}

// The specification prints these so an implementation can check its arithmetic; step scales every width and threshold, so one wrong constant reaches every output.
func TestRateConstants_MatchTheSpecTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		period                        time.Duration
		rangeScale, step, staticSlope float64
		promoteCount                  uint8
		runInSec, maxGapSec           float64
		gasPassesThrough              bool
	}{
		{300 * time.Second, 0.499991566, 0.0484541878, 1352.29565, 2, 1200, 450, true},
		{60 * time.Second, 0.499991566, 0.0484541878, 1352.29565, 4, 1200, 90, true},
		{18 * time.Second, 1.0, 0.0969100073, 676.136414, 8, 60, 27, false},
		{3 * time.Second, 0.873658538, 0.0846662521, 773.913818, 20, 300, 4.5, false},
		{time.Second, 1.0, 0.0969100073, 676.136414, 34, 300, 1.5, false},
		// The period we actually run at: no BSEC mode owns it, so §6.3's last row applies.
		{pollPeriod, 0.499991566, 0.0484541878, 1352.29565, 6, 1200, 45, true},
	} {
		tr := newTracker(tc.period)
		if !nearSpec(tr.rangeScale, tc.rangeScale) {
			t.Errorf("%s rangeScale = %.9f, want %.9f", tc.period, tr.rangeScale, tc.rangeScale)
		}
		if !nearSpec(tr.stepSize(), tc.step) {
			t.Errorf("%s step = %.10f, want %.10f", tc.period, tr.stepSize(), tc.step)
		}
		if !nearSpec(tr.staticSlope, tc.staticSlope) {
			t.Errorf("%s staticSlope = %.6f, want %.6f", tc.period, tr.staticSlope, tc.staticSlope)
		}
		if tr.promoteCount != tc.promoteCount {
			t.Errorf("%s promoteCount = %d, want %d", tc.period, tr.promoteCount, tc.promoteCount)
		}
		if tr.runInSec != tc.runInSec {
			t.Errorf("%s run-in = %vs, want %vs", tc.period, tr.runInSec, tc.runInSec)
		}
		if tr.maxGapSec != tc.maxGapSec {
			t.Errorf("%s gap = %vs, want %vs", tc.period, tr.maxGapSec, tc.maxGapSec)
		}
		// The gas cut-off is above Nyquist from 25 s up, where smoothing degenerates to a pass-through.
		if passes := tr.alpha[0] == 1; passes != tc.gasPassesThrough {
			t.Errorf("%s gas alpha = %v, pass-through %v, want %v", tc.period, tr.alpha[0], passes, tc.gasPassesThrough)
		}
	}
}

// 50 on the tracked baseline and 200 at the polluted extreme are the two published anchors.
func TestIndexMapper_HitsThePublishedAnchors(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	bd := bands{max: [2]float64{5, 5}, min: [2]float64{4, 4}, accuracy: [2]float64{3, 3}}

	if got := tr.mapIndex(5, bd, 1, true, true); !near(got.IAQ, 50, 1e-9) {
		t.Errorf("on the baseline the index is %v, want 50", got.IAQ)
	}
	if got := tr.mapIndex(4, bd, 1, true, true); !near(got.IAQ, 200, 1e-9) {
		t.Errorf("at the polluted extreme the index is %v, want 200", got.IAQ)
	}
	// The gas percentage runs the other way over band 2: all of it on the baseline, none at the extreme.
	if got := tr.mapIndex(5, bd, 1, true, true); !near(got.GasPercentage, 0, 1e-9) {
		t.Errorf("on the baseline the gas percentage is %v, want 0", got.GasPercentage)
	}
	if got := tr.mapIndex(4, bd, 1, true, true); !near(got.GasPercentage, 100, 1e-9) {
		t.Errorf("at the extreme the gas percentage is %v, want 100", got.GasPercentage)
	}
}

// staticSlope divides into the span, not by it: the static index reaches 200 at k_mode times the baseline, where the reciprocal would need five decades.
func TestStaticIndex_ReachesTwoHundredAtOneModeRatio(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		period time.Duration
		k      float64
	}{
		{pollPeriod, 0.7746},
		{3 * time.Second, 0.64},
		{time.Second, 0.60},
	} {
		tr := newTracker(tc.period)
		const baseline = 5.0
		bd := bands{max: [2]float64{baseline, baseline}, min: [2]float64{baseline - 1, baseline - 1}}

		fallen := baseline + math.Log10(tc.k)
		got := tr.mapIndex(fallen, bd, 1, true, true)
		if !near(got.StaticIAQ, 200, 1e-6) {
			t.Errorf("%s: a fall to %v of the baseline gives a static index of %v, want 200", tc.period, tc.k, got.StaticIAQ)
		}
		if !near(got.BreathVOCEquiv, 15, 1e-4) {
			t.Errorf("%s: breath VOC at index 200 is %v ppm, want 15", tc.period, got.BreathVOCEquiv)
		}
		if base := tr.mapIndex(baseline, bd, 1, true, true); !near(base.BreathVOCEquiv, 0.5, 1e-6) {
			t.Errorf("%s: breath VOC at index 50 is %v ppm, want 0.5", tc.period, base.BreathVOCEquiv)
		}
	}
}

// A restored band is live at once, but the soft start holds the index at 50 until the part has warmed up again, which is the point of the ramp.
func TestRunInRamp_HoldsTheIndexAtFiftyUntilWarmedUp(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	tr.st.BandMax, tr.st.BandMin = [2]float64{5, 5}, [2]float64{4, 4}

	first := tr.observe(30_000, 25, 40, epoch)
	if !near(first.IAQ, indexOffset, 1e-9) {
		t.Errorf("the first sample of a run-in reads %v, want the stored 50", first.IAQ)
	}
	half, at := run(tr, 30_000, runInSamples/2, epoch.Add(pollPeriod))
	warm, _ := run(tr, 30_000, runInSamples, at)
	if !warm.RunIn {
		t.Fatal("run-in did not complete, so this proves nothing")
	}
	if warm.IAQ <= indexOffset {
		t.Fatalf("the warmed-up index is %v, which is not above 50, so the ramp proves nothing", warm.IAQ)
	}
	if !(half.IAQ > indexOffset && half.IAQ < warm.IAQ) {
		t.Errorf("halfway through run-in the index is %v, want between 50 and the warmed-up %v", half.IAQ, warm.IAQ)
	}
}

// The specification's end-to-end trace: its first sample and two later static-index anchors reproduce without the stages this does not run.
func TestIndexMapper_ReproducesThePublishedTrace(t *testing.T) {
	t.Parallel()
	tr := newTracker(3 * time.Second)
	// The trace's own first processed sample: a 3 s subscription, 25 C, 40 %RH.
	first := tr.observe(50_000*(1+0.3*math.Sin(1.0/20)), 25, 40, epoch)
	for _, tc := range []struct {
		name      string
		got, want float64
	}{
		{"index", first.IAQ, 50},
		{"static index", first.StaticIAQ, 50},
		{"CO2", first.CO2Equivalent, 600},
		{"breath VOC", first.BreathVOCEquiv, 0.499999911},
		{"gas percentage", first.GasPercentage, 0},
	} {
		if !near(tc.got, tc.want, math.Max(math.Abs(tc.want)*1e-6, 1e-12)) {
			t.Errorf("the first sample reports %s %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if first.Accuracy != 0 || first.GasAccuracy != 0 {
		t.Errorf("the first sample reports accuracy %d/%d, want 0/0", first.Accuracy, first.GasAccuracy)
	}

	// The ppm these two later static indices map to pins both coefficients, which one printed anchor alone would not.
	for _, tc := range []struct{ staticIndex, voc float64 }{
		{0, 0.160914853},
		{23.5072746, 0.274210125},
	} {
		if got := clamp(math.Pow(10, tr.vocSlope*tc.staticIndex+tr.vocIntercept), vocMinPpm, vocMaxPpm); !nearSpec(got, tc.voc) {
			t.Errorf("a static index of %v gives %.9f ppm, want %.9f", tc.staticIndex, got, tc.voc)
		}
	}
}

// The coded threshold is 66.66, not the algebraic 400/6, so the curve steps DOWN as it crosses.
func TestCO2Equivalent_StepsDownAtTheCodedCrossover(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	const baseline = 5.0
	bd := bands{max: [2]float64{baseline, baseline}, min: [2]float64{baseline - 1, baseline - 1}}

	// A signal chosen so the static index lands either side of the threshold by a hair.
	at := func(staticIndex float64) float64 {
		return tr.mapIndex(baseline-(staticIndex-indexOffset)/tr.staticSlope, bd, 1, true, true).CO2Equivalent
	}
	below, above := at(co2Threshold-1e-6), at(co2Threshold+1e-6)
	if !near(below, 666.640, 1e-3) {
		t.Errorf("just below the crossover CO2 is %.4f ppm, want 666.640", below)
	}
	if !near(above, 666.600, 1e-3) {
		t.Errorf("just above the crossover CO2 is %.4f ppm, want 666.600", above)
	}
	if above >= below {
		t.Errorf("the crossover did not step down: %.4f then %.4f", below, above)
	}
	// Below the baseline index the low branch is floored at the baseline ppm rather than going under it.
	if got := at(0); !near(got, co2BaselinePpm, 1e-9) {
		t.Errorf("at a static index of 0 CO2 is %v ppm, want the %v baseline", got, co2BaselinePpm)
	}
}

// The gas is floored before its logarithm and each channel moves by its own coefficient.
func TestConditioner_FloorsTheGasAndSmoothsEachChannel(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)

	gas, temp, hum := tr.condition(50_000, 25, 40)
	if !near(gas, math.Log10(50_000), 1e-12) || temp != 25 || hum != 40 {
		t.Fatalf("the first sample seeds the filter, so it came back as %v/%v/%v", gas, temp, hum)
	}
	_, temp, hum = tr.condition(50_000, 45, 60)
	if want := 25 + tr.alpha[1]*20; !near(temp, want, 1e-12) {
		t.Errorf("a 20 C step arrived as %v, want %v", temp, want)
	}
	if want := 40 + tr.alpha[2]*20; !near(hum, want, 1e-12) {
		t.Errorf("a 20 %%RH step arrived as %v, want %v", hum, want)
	}
	// Below the floor the logarithm would run away to minus infinity and take the band with it.
	if gas, _, _ = tr.condition(0, 45, 60); gas != math.Log10(gasFloorOhms) {
		t.Errorf("a dead short read as %v, want log10 of the %v ohm floor", gas, gasFloorOhms)
	}
}

// Out capped per horizon-second and in by decay, so one breath of clean air cannot redefine the baseline, nor one smoky evening erase it.
func TestBandEdges_MoveAtTheRatesTheHorizonsSet(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	_, at := run(tr, 50_000, runInSamples, epoch)

	before := tr.st.BandMax[0]
	unlimited := tr.compensate(math.Log10(200_000), 25, 40) - before
	_, at = run(tr, 200_000, 1, at)
	want := tr.rangeScale * outwardPerHorizon / initialHorizonSec * pollPeriod.Seconds()
	if got := tr.st.BandMax[0] - before; !near(got, want, 1e-12) {
		t.Errorf("the maximum rose %.9f, want the rate-limited %.9f", got, want)
	}
	if unlimited < 10*want {
		t.Fatalf("the unlimited rise of %.9f is too close to the %.9f limit to prove anything", unlimited, want)
	}

	before = tr.st.BandMax[0]
	delta := tr.compensate(math.Log10(40_000), 25, 40) - before
	_, _ = run(tr, 40_000, 1, at)
	// A third of the calibration horizon, which is what an unmatured band 1 relearns over.
	want = delta * (1 - math.Exp(-pollPeriod.Seconds()/(calibHorizonSec[0]/3)))
	if got := tr.st.BandMax[0] - before; !near(got, want, 1e-12) {
		t.Errorf("the maximum relaxed %.9f, want %.9f", got, want)
	}
}

// A constant signal never opens the band, so it stays one step wide at accuracy 1 for ever: the part has seen no polluted air to calibrate against.
func TestConstantSignal_KeepsTheBandOneStepWideAtAccuracyOne(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	out, _ := run(tr, 50_000, runInSamples+20, epoch)

	if !out.RunIn {
		t.Fatalf("run-in did not complete over %d samples", runInSamples+20)
	}
	if tr.st.BandMin != [2]float64{0, 0} {
		t.Errorf("the band minimum seeded at %v although the signal never fell", tr.st.BandMin)
	}
	if out.Accuracy != 1 || out.GasAccuracy != 1 {
		t.Errorf("accuracy = %d/%d, want 1/1 while the band has never opened", out.Accuracy, out.GasAccuracy)
	}
	if !near(out.IAQ, 50, 1e-9) || !near(out.StaticIAQ, 50, 1e-9) {
		t.Errorf("index = %v/%v, want 50 on the baseline", out.IAQ, out.StaticIAQ)
	}
	if !near(out.GasPercentage, 0, 1e-9) {
		t.Errorf("gas percentage = %v, want 0 against an unseeded minimum", out.GasPercentage)
	}
	// The reported minimum is substituted from the maximum, so the band is exactly one step wide.
	_, bd := tr.track(math.Log10(50_000), 25, 40, true, true, false, pollPeriod.Seconds())
	if got := bd.max[0] - bd.min[0]; !near(got, tr.stepSize(), 1e-12) {
		t.Errorf("the reported band is %.10f wide, want one step of %.10f", got, tr.stepSize())
	}
}

// Below a full soft start the stored values stay put, so the index eases away from 50 rather than tracking the previous sample.
func TestSoftStart_EasesTheIndexAwayFromFifty(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	bd := bands{max: [2]float64{5, 5}, min: [2]float64{4, 4}}

	if got := tr.mapIndex(4, bd, 0, false, true); !near(got.IAQ, 50, 1e-9) {
		t.Errorf("at the start of run-in the index is %v, want the stored 50", got.IAQ)
	}
	if got := tr.mapIndex(4, bd, 0.5, false, true); !near(got.IAQ, 0.25*200+0.75*50, 1e-9) {
		t.Errorf("a quarter of the way in the index is %v, want 87.5", got.IAQ)
	}
	if tr.st.PreviousIndex != indexOffset {
		t.Errorf("the stored index moved to %v during run-in", tr.st.PreviousIndex)
	}
	if got := tr.mapIndex(4, bd, 1, true, true); !near(got.IAQ, 200, 1e-9) {
		t.Errorf("after run-in the index is %v, want the full 200", got.IAQ)
	}
	if tr.st.PreviousIndex != 200 {
		t.Errorf("the stored index is %v after run-in, want 200", tr.st.PreviousIndex)
	}
	// A falling index is not limited at all; only a rise is blended.
	if got := tr.mapIndex(4.9, bd, 0.1, false, true); !near(got.IAQ, 65, 1e-9) {
		t.Errorf("a falling index came back as %v, want an unlimited 65", got.IAQ)
	}
}

// Accuracy climbs only once the band has opened both ways and then held mid-band, the mechanism behind the advice to show the part clean and polluted air.
func TestSquareWave_PromotesToCalibratedAfterPromoteCountMidBandSamples(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	out, at := run(tr, 50_000, runInSamples, epoch)
	if out.Accuracy != 1 {
		t.Fatalf("accuracy = %d after run-in on a flat signal, want 1", out.Accuracy)
	}

	// A drop to 30k is 0.22 in log10, over four steps, so it seeds the minimum and opens the band.
	out, at = run(tr, 30_000, 1, at)
	if out.Accuracy != 2 {
		t.Fatalf("accuracy = %d at the band edge, want 2", out.Accuracy)
	}
	if tr.st.BandMin[0] == 0 {
		t.Fatal("the minimum did not seed on a drop of four steps")
	}

	for i := 1; i <= int(tr.promoteCount); i++ {
		out, at = run(tr, 39_000, 1, at)
		want := 2
		if i >= int(tr.promoteCount) {
			want = 3
		}
		if out.Accuracy != want {
			t.Fatalf("after %d mid-band samples accuracy = %d, want %d (promoteCount %d)",
				i, out.Accuracy, want, tr.promoteCount)
		}
	}
	// A return to the edge drops it back, or accuracy would latch at 3 whatever the signal did.
	if out, _ = run(tr, 30_000, 1, at); out.Accuracy != 2 {
		t.Errorf("accuracy = %d back at the band edge, want 2", out.Accuracy)
	}
}

// Normalised by the raw step, the position saturates at 1 and pins the accuracy-3 hysteresis at its tightest for ever, without ever looking wrong.
func TestBandPosition_NormalisesByTheBandNotTheStep(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	tr.st.BandMax[0], tr.st.BandMin[0] = 5, 4

	if got := tr.bandPosition(4.9, 0); !near(got, 0.2, 1e-9) {
		t.Errorf("a signal a tenth inside a band one wide is at %v, want 0.2", got)
	}
	if got := tr.bandPosition(4.5, 0); !near(got, 1, 1e-9) {
		t.Errorf("mid-band is at %v, want 1", got)
	}
	if got := tr.bandPosition(5.5, 0); got != 0 {
		t.Errorf("outside the band is at %v, want 0", got)
	}
	// With no minimum yet the step is the only width there is.
	tr.st.BandMin[0] = 0
	if got := tr.bandPosition(5-tr.stepSize()/2, 0); !near(got, 1, 1e-9) {
		t.Errorf("half a step below an unseeded band is at %v, want 1", got)
	}
}

// What persisting state buys: the counter is preloaded rather than reset during run-in, so a calibrated part says so again as soon as it has warmed up.
func TestAfterAGap_CalibratedAccuracyReturnsOnTheFirstMidBandSample(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	_, at := run(tr, 50_000, runInSamples, epoch)
	_, at = run(tr, 30_000, 1, at)
	out, at := run(tr, 39_000, int(tr.promoteCount), at)
	if out.Accuracy != 3 {
		t.Fatalf("accuracy = %d before the gap, want 3", out.Accuracy)
	}

	if out = tr.observe(39_000, 25, 40, at.Add(10*time.Minute)); out.Accuracy != 0 {
		t.Fatalf("accuracy = %d on the first sample after a gap, want 0", out.Accuracy)
	}
	at = at.Add(10 * time.Minute)
	out, _ = run(tr, 39_000, runInSamples, at.Add(pollPeriod))
	if !out.RunIn {
		t.Fatalf("run-in did not complete again")
	}
	if out.Accuracy != 3 {
		t.Errorf("accuracy = %d on the first sample after run-in, want the 3 it had learned", out.Accuracy)
	}
}

// A gap discards run-in, and only run-in: the part has cooled, but it has not become younger.
func TestGap_DiscardsRunInAndNothingElse(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	_, at := run(tr, 50_000, runInSamples, epoch)
	if !tr.st.RunIn {
		t.Fatal("run-in did not complete, so this proves nothing")
	}
	mature := tr.st.MatureAccumSec
	bandMax := tr.st.BandMax

	out := tr.observe(50_000, 25, 40, at.Add(2*pollPeriod))
	if out.RunIn || tr.st.RunInAccumSec != 0 {
		t.Errorf("after a gap run-in = %v with %v s accrued, want false and 0", out.RunIn, tr.st.RunInAccumSec)
	}
	if out.Accuracy != 0 {
		t.Errorf("accuracy = %d during a fresh run-in, want 0", out.Accuracy)
	}
	if tr.st.MatureAccumSec != mature {
		t.Errorf("maturity accrual moved from %v to %v across the gap", mature, tr.st.MatureAccumSec)
	}
	if tr.st.BandMax != bandMax {
		t.Errorf("the learned band moved from %v to %v across the gap", bandMax, tr.st.BandMax)
	}
	// One period late is inside the 1.5-period window and must not reset anything.
	tr2 := newTracker(pollPeriod)
	_, at2 := run(tr2, 50_000, runInSamples, epoch)
	if out := tr2.observe(50_000, 25, 40, at2.Add(pollPeriod/4)); !out.RunIn {
		t.Errorf("a sample 1.25 periods late reset run-in, which is inside the window")
	}
}

// An RTC-less Pi steps its wall clock when NTP syncs, which within a run is no gap, so samples are timed on the monotonic clock.
func TestRunIn_TimesSamplesOnTheMonotonicClock(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	// time.Now carries a monotonic reading, as the driver's stamp does.
	out, at := run(tr, 50_000, runInSamples, time.Now())
	if !out.RunIn {
		t.Fatal("run-in did not complete, so this proves nothing")
	}
	// Sub between two stamps that both hold one ignores the wall clock; String shows the reading as m=.
	if !strings.Contains(tr.last.String(), " m=") {
		t.Errorf("the last sample's stamp %v lost its monotonic reading, so a wall-clock step reads as a gap", tr.last)
	}
	// Provokes the positive: a real gap still restarts the run-in.
	if out := tr.observe(50_000, 25, 40, at.Add(3*pollPeriod)); out.RunIn {
		t.Errorf("a %s gap did not restart the run-in", 3*pollPeriod)
	}
}

// Output 12's threshold ships at zero, so it is true from the first sample; anything else would be an invented delay, and it gates accuracy.
func TestStabilised_IsTrueFromTheFirstSample(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	if out := tr.observe(50_000, 25, 40, epoch); !out.Stabilised {
		t.Error("the first sample reported an unstabilised part")
	}
}

// Without the restore a reboot relearns the extremes from scratch, which takes hours of exposure.
func TestTrackerState_SurvivesARoundTrip(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	_, at := run(tr, 50_000, runInSamples, epoch)
	_, at = run(tr, 30_000, 5, at)
	_, at = run(tr, 41_000, 5, at)

	blob, err := tr.marshalState("a part")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	restored := newTracker(pollPeriod)
	if err := restored.restoreState(blob, "a part"); err != nil {
		t.Fatalf("SetState: %v", err)
	}
	// A restore always runs in again, as the driver's does, and the stamp of the last sample stays with the run it was taken in.
	tr.restartRunIn()
	restored.restartRunIn()

	want := tr.observe(42_000, 25, 40, at)
	got := restored.observe(42_000, 25, 40, at)
	if got != want {
		t.Errorf("after a restore the next sample gave\n%+v\nwant\n%+v", got, want)
	}
	// A fresh tracker must not agree by accident, or the round trip proves nothing.
	if fresh := newTracker(pollPeriod).observe(42_000, 25, 40, at); fresh == want {
		t.Error("a tracker with no state produced the same reading, so the comparison is blind")
	}
}

func TestRestoreState_RefusesWhatItCannotVouchFor(t *testing.T) {
	t.Parallel()
	// Each is this build's version and this part's unless it is the fault itself, so each is refused for its own reason.
	for _, tc := range []struct{ name, blob string }{
		{"not json", "{"},
		{"another version", `{"version":99,"workingHorizon":[1800,1800],"chip":"this","period":30000000000}`},
		{"a blob missing its fields", `{"version":3,"chip":"this","period":30000000000}`},
		{"a zero working horizon", `{"version":3,"workingHorizon":[1800,0],"chip":"this","period":30000000000}`},
		{"another part's", `{"version":3,"workingHorizon":[1800,1800],"chip":"that","period":30000000000}`},
		{"another rate's", `{"version":3,"workingHorizon":[1800,1800],"chip":"this","period":3000000000}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := newTracker(pollPeriod)
			before := tr.st
			if err := tr.restoreState([]byte(tc.blob), "this"); err == nil {
				t.Errorf("restoreState accepted %s", tc.name)
			}
			if tr.st != before {
				t.Error("a refused state still reached the tracker")
			}
		})
	}
	// Provokes the positive: the same blob with nothing wrong is taken.
	if err := newTracker(pollPeriod).restoreState([]byte(`{"version":3,"workingHorizon":[1800,1800],"chip":"this","period":30000000000}`), "this"); err != nil {
		t.Errorf("a sound blob was refused: %v", err)
	}
}

// Uncompensated, the same air reads about 29% lower at twice the absolute humidity, and a gas trend is really a humidity trend.
func TestCompensateGas_NormalisesToTheReferenceAir(t *testing.T) {
	t.Parallel()
	const raw = 100_000.0
	refT, refH := refTemp[modeSlow], refHum[modeSlow]

	if got := compensateGas(raw, refT, refH); !near(got, raw, 1) {
		t.Errorf("at the reference air the compensation moved %.0f to %.0f", raw, got)
	}
	// Absolute humidity is linear in relative humidity at a fixed temperature, so this is exactly twice.
	doubled := compensateGas(raw, refT, 2*refH)
	if want := raw * math.Pow(2, -humiditySlope[modeSlow]); !near(doubled, want, 1) {
		t.Errorf("doubling the water in the air gave %.0f, want %.0f", doubled, want)
	}
	// Drier air compensates the other way, or the sign is wrong and it doubles the error it should remove.
	if drier := compensateGas(raw, refT, refH/2); drier >= raw {
		t.Errorf("drier air gave %.0f, which is not below the raw %.0f", drier, raw)
	}
}

// The driver publishes the same compensation as an ohms reading; two spellings of it would drift.
func TestCompensate_AgreesWithTheDriversCompensatedResistance(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	for _, tc := range []struct{ ohms, temp, hum float64 }{
		{50_000, 25, 40},
		{120_000, 5, 90},
		{8_000, 35, 15},
	} {
		want := math.Log10(compensateGas(tc.ohms, tc.temp, tc.hum))
		got := tr.compensate(math.Log10(tc.ohms), tc.temp, tc.hum)
		if !near(got, want, 1e-12) {
			t.Errorf("compensate(%v, %v, %v) = %.12f, want %.12f", tc.ohms, tc.temp, tc.hum, got, want)
		}
	}
}

// Out of the part's operating envelope the bands freeze rather than learn from a reading the datasheet does not vouch for.
func TestOutOfRange_FreezesTheBands(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	_, at := run(tr, 50_000, runInSamples, epoch)

	// The gate reads the SMOOTHED temperature, so one hot sample is filtered away and several are not.
	for range 10 {
		tr.observe(10_000, 95, 40, at)
		at = at.Add(pollPeriod)
	}
	if tr.st.Smoothed[1] <= tempMax {
		t.Fatalf("the smoothed temperature only reached %v C, so the gate never fired", tr.st.Smoothed[1])
	}
	bandMax := tr.st.BandMax
	tr.observe(10_000, 95, 40, at)
	if tr.st.BandMax != bandMax {
		t.Errorf("the band moved from %v to %v above the part's operating envelope", bandMax, tr.st.BandMax)
	}
	tr.observe(10_000, 25, 40, at.Add(pollPeriod))
	if tr.st.BandMax == bandMax {
		t.Error("the band did not move on an in-range reading, so the freeze test proves nothing")
	}
}

// Without it a page can only say run-in is not done, never how long is left.
func TestRunInLeft_CountsDownToZero(t *testing.T) {
	t.Parallel()
	tr := newTracker(pollPeriod)
	out, at := run(tr, 50_000, 1, epoch)
	if out.RunInLeft != 1200*time.Second {
		t.Errorf("after the first sample %s is left, want the whole 20m0s", out.RunInLeft)
	}
	out, at = run(tr, 50_000, 10, at)
	if out.RunInLeft != 900*time.Second {
		t.Errorf("after 300s of samples %s is left, want 15m0s", out.RunInLeft)
	}
	out, _ = run(tr, 50_000, runInSamples, at)
	if !out.RunIn || out.RunInLeft != 0 {
		t.Errorf("run-in done %v with %s left, want done with none", out.RunIn, out.RunInLeft)
	}
}
