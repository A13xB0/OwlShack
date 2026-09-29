package config

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Match fields for the feed trigger types. A feed item is several distinct pieces of text, so a
// pattern says which one it applies to — "severity" and "description" are not interchangeable, and
// running one regex over all of them joined makes a severity filter match the word in a sentence.
// Absent for the types whose patterns run against a single message body.
var matchFields = map[string][]string{
	"rss": {"title", "description", "content", "link", "author", "category"},
	"cap": {
		"event", "headline", "description", "instruction", "severity", "urgency",
		"certainty", "msgtype", "status", "area", "geocode", "sender", "category",
	},
}

// SplitFieldPattern splits a feed trigger's "<field>:<regex>" entry. The field is everything before
// the first colon, which a regex may otherwise contain freely.
func SplitFieldPattern(entry string) (field, pattern string, ok bool) {
	field, pattern, ok = strings.Cut(entry, ":")
	if !ok {
		return "", entry, false
	}
	return strings.ToLower(strings.TrimSpace(field)), pattern, true
}

// validateFieldPattern rejects an entry that names no field or an unknown one. A typo'd field would
// otherwise compile as a bare regex and silently never match, which looks exactly like a feed that
// has published nothing.
func validateFieldPattern(entry string, allowed []string) error {
	field, pattern, ok := SplitFieldPattern(entry)
	if !ok || field == "" {
		return fmt.Errorf("match %q must name a field, as \"<field>:<pattern>\" (one of: %s)",
			entry, strings.Join(allowed, ", "))
	}
	if !slices.Contains(allowed, field) {
		return fmt.Errorf("unknown match field %q (one of: %s)", field, strings.Join(allowed, ", "))
	}
	if pattern == "" {
		return fmt.Errorf("match on field %q has no pattern", field)
	}
	if _, err := regexp.Compile(pattern); err != nil {
		return fmt.Errorf("invalid match pattern %q: %w", pattern, err)
	}
	return nil
}
