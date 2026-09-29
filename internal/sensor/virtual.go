package sensor

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/vm"
)

// KindExpression derives a reading from other sensors' readings.
const KindExpression = "expression"

const (
	optExpression = "expression"
	optMetric     = "metric"
	optUnit       = "unit"
)

// bindingName keeps a value's name to an identifier, clear of the language's own syntax.
var bindingName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// VirtualProvider derives sensors from other sensors; the hub orders a poll pass by their bindings.
type VirtualProvider struct {
	// snapshot is the hub's pull seam, supplied after construction since the hub is built from its providers.
	snapshot func() []Status
}

// Bind gives the provider its source of readings. The hub calls this on itself at start-up.
func (p *VirtualProvider) Bind(snapshot func() []Status) { p.snapshot = snapshot }

func (p *VirtualProvider) ID() string    { return "virtual" }
func (p *VirtualProvider) Label() string { return "Derived" }

func (p *VirtualProvider) Available(context.Context) (bool, string) {
	if p.snapshot == nil {
		return false, "not connected to the sensor hub"
	}
	return true, ""
}

// Discover returns nothing: a derived sensor is defined, never found.
func (p *VirtualProvider) Discover(context.Context) ([]Candidate, error) { return nil, nil }

func (p *VirtualProvider) Kinds() []KindInfo {
	return []KindInfo{{
		Kind: KindExpression, Label: "Expression",
		Description: "A value worked out from other sensors", Category: "Derived",
		Binds: true,
		Fields: []Field{
			{
				Key: optExpression, Label: "Expression", Required: true, Multiline: true,
				Help: "Maths over the bound sensors, such as volts * 2 or (t + u) / 2",
			},
			{
				Key: optMetric, Label: "Measures", Required: true,
				Help: "What the result is, such as moisture or battery",
			},
			{
				Key: optUnit, Label: "Unit", Required: true,
				Help: "How the result is shown, such as % or V",
			},
		},
	}}
}

// Validate compiles the expression at save time, so a typo is refused once rather than every poll.
func (p *VirtualProvider) Validate(spec Spec) error {
	if spec.Kind != KindExpression {
		return fmt.Errorf("unknown derived sensor kind %q", spec.Kind)
	}
	if len(spec.Bindings) == 0 {
		return errors.New("a derived sensor has to read at least one other sensor")
	}
	seen := map[string]bool{}
	for _, b := range spec.Bindings {
		if !bindingName.MatchString(b.Name) {
			return fmt.Errorf("%q is not a usable name; use letters, digits and underscores, starting with a letter", b.Name)
		}
		if seen[b.Name] {
			return fmt.Errorf("two bindings are both called %q", b.Name)
		}
		seen[b.Name] = true
		if b.SensorID == 0 {
			return fmt.Errorf("binding %q names no sensor", b.Name)
		}
		if b.Metric == "" {
			return fmt.Errorf("binding %q names no reading of sensor %d", b.Name, b.SensorID)
		}
	}
	if strings.TrimSpace(spec.Options[optMetric]) == "" {
		return errors.New("say what the result measures")
	}
	if strings.TrimSpace(spec.Options[optUnit]) == "" {
		return errors.New("say what unit the result is in")
	}
	_, err := compileExpression(spec)
	return err
}

// mathFuncs adds the logarithms a dew point and a thermistor curve need; expr stops at abs/round.
func mathFuncs() []expr.Option {
	one := func(name string, fn func(float64) float64) expr.Option {
		return expr.Function(name,
			func(p ...any) (any, error) { return fn(p[0].(float64)), nil },
			new(func(float64) float64))
	}
	return []expr.Option{
		one("log", math.Log),
		one("log10", math.Log10),
		one("exp", math.Exp),
		one("sqrt", math.Sqrt),
		expr.Function("pow",
			func(p ...any) (any, error) { return math.Pow(p[0].(float64), p[1].(float64)), nil },
			new(func(float64, float64) float64)),
	}
}

// compileExpression closes the environment, so an identifier that is not a binding will not compile.
func compileExpression(spec Spec) (*vm.Program, error) {
	env := make(map[string]float64, len(spec.Bindings))
	for _, b := range spec.Bindings {
		env[b.Name] = 0
	}
	src := strings.TrimSpace(spec.Options[optExpression])
	if src == "" {
		return nil, errors.New("the expression is empty")
	}
	opts := append(mathFuncs(), expr.Env(env), expr.AsFloat64())
	prog, err := expr.Compile(src, opts...)
	if err != nil {
		return nil, fmt.Errorf("the expression does not compile: %w", err)
	}
	return prog, nil
}

func (p *VirtualProvider) Open(spec Spec) (Sensor, error) {
	if p.snapshot == nil {
		return nil, errors.New("the derived provider is not connected to the sensor hub")
	}
	prog, err := compileExpression(spec)
	if err != nil {
		return nil, err
	}
	return &exprSensor{
		snapshot: p.snapshot,
		bindings: slices.Clone(spec.Bindings),
		program:  prog,
		metric:   Metric(strings.TrimSpace(spec.Options[optMetric])),
		unit:     strings.TrimSpace(spec.Options[optUnit]),
	}, nil
}

// exprSensor evaluates one compiled expression over the readings it was bound to.
type exprSensor struct {
	snapshot func() []Status
	bindings []Binding
	program  *vm.Program
	metric   Metric
	unit     string
}

// Read refuses to evaluate on a source that is failing, stale or gone, rather than answer wrongly.
func (s *exprSensor) Read(context.Context) ([]Reading, error) {
	byID := map[int64]Status{}
	for _, st := range s.snapshot() {
		byID[st.Spec.ID] = st
	}

	env := make(map[string]float64, len(s.bindings))
	for _, b := range s.bindings {
		st, ok := byID[b.SensorID]
		if !ok {
			return nil, fmt.Errorf("%s reads sensor %d, which is no longer configured", b.Name, b.SensorID)
		}
		if st.Err != "" {
			return nil, fmt.Errorf("%s is failing: %s", st.Spec.Name, st.Err)
		}
		if st.At.IsZero() {
			return nil, fmt.Errorf("%s has not been read yet: %w", st.Spec.Name, errNotYet)
		}
		i := slices.IndexFunc(st.Readings, func(r Reading) bool { return r.Metric == b.Metric })
		if i < 0 {
			return nil, fmt.Errorf("%s reports no %s", st.Spec.Name, b.Metric)
		}
		env[b.Name] = st.Readings[i].Value
	}

	out, err := expr.Run(s.program, env)
	if err != nil {
		return nil, fmt.Errorf("evaluating: %w", err)
	}
	v, ok := out.(float64)
	if !ok {
		return nil, fmt.Errorf("the expression produced %T, not a number", out)
	}
	// encoding/json refuses NaN and Inf, so one would take the whole sensor list off the API.
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil, fmt.Errorf("the expression produced %v, which is not a reportable number", v)
	}
	return []Reading{{Metric: s.metric, Value: v, Unit: s.unit}}, nil
}

var (
	_ Provider  = &VirtualProvider{}
	_ Validator = &VirtualProvider{}
)
