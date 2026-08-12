package fixture

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// TemporalKind names one of the six typed temporal spellings a fixture may
// use. They are the tags of GQL's temporal property types and nothing else is
// recognised, so a fixture cannot invent a type by writing a one-key map.
type TemporalKind string

// The recognised tags. A fixture writes a temporal value as a one-key map
// whose key is one of these, e.g. on: {date: "2024-01-15"}.
const (
	KindDate          TemporalKind = "date"
	KindTime          TemporalKind = "time"
	KindDateTime      TemporalKind = "datetime"
	KindLocalTime     TemporalKind = "localtime"
	KindLocalDateTime TemporalKind = "localdatetime"
	KindDuration      TemporalKind = "duration"
)

// Temporal is a typed temporal value as a fixture spells it: a tag and the
// literal text that follows it, not yet turned into any engine's type.
//
// The literal is kept as text on purpose. What an adapter must send depends on
// its driver, and there is no one Go type that covers all six: time.Duration
// cannot hold months, and a zoned time is not a time.Time. What matters is
// that the *reading* of the literal is defined here rather than in each
// adapter, so that two engines cannot end up holding different values for the
// same fixture and then disagree about a case for a reason that has nothing to
// do with either engine.
type Temporal struct {
	Kind    TemporalKind
	Literal string
}

// AsTemporal reports whether v is the tagged-map spelling of a temporal value.
//
// It is deliberately strict: exactly one key, that key a recognised tag, and
// the value a string. A two-key map or an unknown tag is not a near miss to be
// guessed at, it is a fixture that will not load, and Requires already refuses
// to claim CapTemporalValues for it.
func AsTemporal(v any) (Temporal, bool) {
	m, ok := v.(map[string]any)
	if !ok || len(m) != 1 {
		return Temporal{}, false
	}
	for k, raw := range m {
		switch TemporalKind(k) {
		case KindDate, KindTime, KindDateTime, KindLocalTime, KindLocalDateTime, KindDuration:
		default:
			return Temporal{}, false
		}
		s, ok := raw.(string)
		if !ok {
			return Temporal{}, false
		}
		return Temporal{Kind: TemporalKind(k), Literal: s}, true
	}
	return Temporal{}, false
}

// Layouts are the accepted spellings, in the order they are tried. Fractional
// seconds are a separate layout rather than an optional suffix because Go's
// reference-time parser has no way to say "optional".
var temporalLayouts = map[TemporalKind][]string{
	KindDate:          {"2006-01-02"},
	KindLocalTime:     {"15:04:05.999999999", "15:04:05", "15:04"},
	KindTime:          {"15:04:05.999999999Z07:00", "15:04:05Z07:00", "15:04Z07:00"},
	KindLocalDateTime: {"2006-01-02T15:04:05.999999999", "2006-01-02T15:04:05", "2006-01-02T15:04"},
	KindDateTime:      {time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04Z07:00"},
}

// Time parses a date, time, datetime, localtime or localdatetime into a
// time.Time. Calling it on a duration is an error rather than a zero value: a
// duration is not a point in time and an adapter that treats one as the other
// would silently load the wrong data.
//
// A local kind comes back in UTC, which is Go's answer for a timestamp with no
// zone. The adapter is expected to hand the fields to its driver's local type
// rather than to send this as an instant.
func (t Temporal) Time() (time.Time, error) {
	layouts, ok := temporalLayouts[t.Kind]
	if !ok {
		return time.Time{}, fmt.Errorf("fixture: %s is not a point in time", t.Kind)
	}
	for _, l := range layouts {
		if got, err := time.Parse(l, t.Literal); err == nil {
			return got, nil
		}
	}
	return time.Time{}, fmt.Errorf("fixture: %q is not a %s; accepted spellings are %v",
		t.Literal, t.Kind, layouts)
}

// Duration is an ISO 8601 duration in the four parts GQL keeps separate.
//
// Months and days are not seconds and cannot be converted into them without
// knowing when the duration is applied: a month is 28 to 31 days and a day is
// 23 to 25 hours across a daylight-saving boundary. Every engine that
// implements GQL durations keeps the parts apart for that reason, so the
// harness does too, and this type is what an adapter maps onto its driver's
// equivalent.
type Duration struct {
	Months  int
	Days    int
	Seconds int64
	Nanos   int
}

// Duration parses an ISO 8601 duration such as P2D, P1Y6M, or PT1H30M.
//
// The week designator is accepted and folded into days, because ISO defines a
// week as exactly seven days with no calendar ambiguity. Everything else the
// grammar allows and this does not is rejected by name rather than ignored: a
// fixture that says something the harness does not understand must not load as
// though it had said something else.
func (t Temporal) Duration() (Duration, error) {
	if t.Kind != KindDuration {
		return Duration{}, fmt.Errorf("fixture: %s is not a duration", t.Kind)
	}
	s := t.Literal
	bad := func() (Duration, error) {
		return Duration{}, fmt.Errorf("fixture: %q is not an ISO 8601 duration", t.Literal)
	}
	neg := false
	if rest, found := strings.CutPrefix(s, "-"); found {
		neg, s = true, rest
	}
	rest, found := strings.CutPrefix(s, "P")
	if !found {
		return bad()
	}
	date, clock, hasT := strings.Cut(rest, "T")
	// A T with nothing after it is not a duration with no time part, it is a
	// truncated literal, and ISO 8601 does not allow the designator to stand
	// alone. Same for a P with nothing at all after it.
	if (date == "" && clock == "") || (hasT && clock == "") {
		return bad()
	}

	var d Duration
	var seenAny bool
	// scan reads number-designator pairs and hands each to apply. It returns
	// false on a stray character rather than stopping early, so that "P2X"
	// fails instead of parsing as P2.
	scan := func(part string, apply func(n int64, frac string, unit byte) bool) bool {
		for part != "" {
			i := 0
			for i < len(part) && (part[i] >= '0' && part[i] <= '9') {
				i++
			}
			if i == 0 {
				return false
			}
			num, frac := part[:i], ""
			if i < len(part) && (part[i] == '.' || part[i] == ',') {
				j := i + 1
				for j < len(part) && (part[j] >= '0' && part[j] <= '9') {
					j++
				}
				if j == i+1 {
					return false
				}
				frac, i = part[i+1:j], j
			}
			if i >= len(part) {
				return false
			}
			n, err := strconv.ParseInt(num, 10, 64)
			if err != nil {
				return false
			}
			if !apply(n, frac, part[i]) {
				return false
			}
			seenAny = true
			part = part[i+1:]
		}
		return true
	}

	ok := scan(date, func(n int64, frac string, unit byte) bool {
		if frac != "" {
			return false // only seconds may be fractional
		}
		switch unit {
		case 'Y':
			d.Months += int(n) * 12
		case 'M':
			d.Months += int(n)
		case 'W':
			d.Days += int(n) * 7
		case 'D':
			d.Days += int(n)
		default:
			return false
		}
		return true
	})
	if !ok {
		return bad()
	}
	ok = scan(clock, func(n int64, frac string, unit byte) bool {
		if frac != "" && unit != 'S' {
			return false
		}
		switch unit {
		case 'H':
			d.Seconds += n * 3600
		case 'M':
			d.Seconds += n * 60
		case 'S':
			d.Seconds += n
			if frac != "" {
				for len(frac) < 9 {
					frac += "0"
				}
				nanos, err := strconv.Atoi(frac[:9])
				if err != nil {
					return false
				}
				d.Nanos = nanos
			}
		default:
			return false
		}
		return true
	})
	if !ok || !seenAny {
		return bad()
	}
	if neg {
		d = Duration{Months: -d.Months, Days: -d.Days, Seconds: -d.Seconds, Nanos: -d.Nanos}
	}
	return d, nil
}
