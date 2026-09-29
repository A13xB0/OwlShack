package sensor

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The field types beyond free text, so a form can offer a proper input and the server can hold it to the same rules.
const (
	// FieldDuration is a length of time stored as a Go duration, such as 10m.
	FieldDuration = "duration"
	// FieldList is rows of Columns, stored as a JSON array of objects keyed by column.
	FieldList = "list"
)

const (
	maxListRows = 32
	maxListLen  = 8192
)

// Column is one input of a list field's rows.
type Column struct {
	Key         string
	Label       string
	Placeholder string
	Required    bool
}

// ListRows decodes a list field's value; empty is no rows.
func ListRows(v string) ([]map[string]string, error) {
	if strings.TrimSpace(v) == "" {
		return nil, nil
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(v), &rows); err != nil {
		return nil, fmt.Errorf("is not a list of rows: %w", err)
	}
	return rows, nil
}

// checkField holds a typed value to its declaration; opts has every earlier field's value, defaults included.
func checkField(f Field, v string, opts map[string]string, fields []Field) error {
	switch f.Type {
	case FieldDuration:
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("%s %q is not a length of time such as 10m", f.Label, v)
		}
		if d < f.Min || (f.Max > 0 && d > f.Max) {
			return fmt.Errorf("%s has to be from %s to %s", f.Label, Span(f.Min), Span(f.Max))
		}
		if f.AtLeast != "" {
			if least, err := time.ParseDuration(opts[f.AtLeast]); err == nil && d < least {
				label := f.AtLeast
				if i := slices.IndexFunc(fields, func(g Field) bool { return g.Key == f.AtLeast }); i >= 0 {
					label = fields[i].Label
				}
				return fmt.Errorf("%s has to be at least %s, as long as %s", f.Label, Span(least), strings.ToLower(label))
			}
		}
	case FieldList:
		rows, err := ListRows(v)
		if err != nil {
			return fmt.Errorf("%s %w", f.Label, err)
		}
		if f.Required && len(rows) == 0 {
			return fmt.Errorf("%s needs at least one row", f.Label)
		}
		if len(rows) > maxListRows {
			return fmt.Errorf("%s has %d rows, and %d is the most", f.Label, len(rows), maxListRows)
		}
		for n, row := range rows {
			for k, cell := range row {
				if !slices.ContainsFunc(f.Columns, func(c Column) bool { return c.Key == k }) {
					return fmt.Errorf("%s row %d has no column %q", f.Label, n+1, k)
				}
				if hasControl(cell, false) {
					return fmt.Errorf("%s row %d has a control character in it", f.Label, n+1)
				}
			}
			for _, c := range f.Columns {
				if c.Required && strings.TrimSpace(row[c.Key]) == "" {
					return fmt.Errorf("%s row %d needs a %s", f.Label, n+1, strings.ToLower(c.Label))
				}
			}
		}
	}
	return nil
}

// Span writes a duration the way a person would say it: 10m, 1h30m, 7d.
func Span(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0 && d < time.Hour:
		return fmt.Sprintf("%dm", d/time.Minute)
	}
	return strings.TrimSuffix(strings.TrimSuffix(d.String(), "0s"), "0m")
}
