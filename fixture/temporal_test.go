package fixture_test

import (
	"testing"
	"time"

	"github.com/tamnd/gql-compat/fixture"
)

// The Neo4j run of 2026-08-12 failed to load the dated fixture because the
// adapter passed {duration: "P2D"} to the driver as a map, and Bolt has no
// property type for a map. The reading of these literals belongs in one place
// so that two engines cannot end up holding two different values for the same
// fixture and then disagree about a case for a reason that is neither
// engine's.

func TestATaggedMapIsRecognisedAndAnythingElseIsNot(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want fixture.Temporal
		ok   bool
	}{
		{"date", map[string]any{"date": "2024-01-15"}, fixture.Temporal{Kind: fixture.KindDate, Literal: "2024-01-15"}, true},
		{"duration", map[string]any{"duration": "P2D"}, fixture.Temporal{Kind: fixture.KindDuration, Literal: "P2D"}, true},
		{"plain string", "2024-01-15", fixture.Temporal{}, false},
		{"unknown tag", map[string]any{"colour": "red"}, fixture.Temporal{}, false},
		{"two keys", map[string]any{"date": "2024-01-15", "time": "10:00:00"}, fixture.Temporal{}, false},
		{"tag with a non-string", map[string]any{"date": 2024}, fixture.Temporal{}, false},
		{"empty map", map[string]any{}, fixture.Temporal{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := fixture.AsTemporal(c.in)
			if ok != c.ok {
				t.Fatalf("recognised %v, want %v", ok, c.ok)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestPointsInTimeParse(t *testing.T) {
	cases := []struct {
		kind fixture.TemporalKind
		lit  string
		want time.Time
	}{
		{fixture.KindDate, "2024-01-15", time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)},
		{fixture.KindLocalTime, "10:30:00", time.Date(0, 1, 1, 10, 30, 0, 0, time.UTC)},
		{fixture.KindLocalTime, "10:30:00.5", time.Date(0, 1, 1, 10, 30, 0, 500000000, time.UTC)},
		{fixture.KindLocalDateTime, "2024-01-15T10:30:00", time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)},
		{fixture.KindDateTime, "2024-01-15T10:30:00Z", time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		t.Run(string(c.kind)+" "+c.lit, func(t *testing.T) {
			got, err := (fixture.Temporal{Kind: c.kind, Literal: c.lit}).Time()
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(c.want) {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

func TestADurationIsNotAPointInTimeAndTheReverse(t *testing.T) {
	if _, err := (fixture.Temporal{Kind: fixture.KindDuration, Literal: "P2D"}).Time(); err == nil {
		t.Error("a duration parsed as an instant, which would load the wrong data silently")
	}
	if _, err := (fixture.Temporal{Kind: fixture.KindDate, Literal: "2024-01-15"}).Duration(); err == nil {
		t.Error("a date parsed as a duration")
	}
}

func TestDurationsParseIntoPartsThatAreNotInterconvertible(t *testing.T) {
	cases := []struct {
		lit  string
		want fixture.Duration
	}{
		{"P2D", fixture.Duration{Days: 2}},
		{"P1Y", fixture.Duration{Months: 12}},
		{"P1Y6M", fixture.Duration{Months: 18}},
		{"P2W", fixture.Duration{Days: 14}},
		{"PT1H30M", fixture.Duration{Seconds: 5400}},
		{"PT0.5S", fixture.Duration{Nanos: 500000000}},
		{"P1DT2H3M4.25S", fixture.Duration{Days: 1, Seconds: 7384, Nanos: 250000000}},
		{"-P2D", fixture.Duration{Days: -2}},
	}
	for _, c := range cases {
		t.Run(c.lit, func(t *testing.T) {
			got, err := (fixture.Temporal{Kind: fixture.KindDuration, Literal: c.lit}).Duration()
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

// A literal the parser does not understand has to be an error. The failure
// mode being guarded against is the one that costs a run: "P2X" quietly
// loading as two of something.
func TestAnUnreadableDurationIsRefusedRatherThanGuessed(t *testing.T) {
	for _, lit := range []string{"", "P", "2D", "P2X", "PT", "P2.5D", "PD", "P2DT", "PT1H30"} {
		t.Run(lit, func(t *testing.T) {
			if got, err := (fixture.Temporal{Kind: fixture.KindDuration, Literal: lit}).Duration(); err == nil {
				t.Errorf("%q parsed as %+v", lit, got)
			}
		})
	}
}

func TestAnUnreadableInstantIsRefused(t *testing.T) {
	for _, lit := range []string{"", "15 January 2024", "2024-13-01", "2024-01-15T10:30:00"} {
		t.Run(lit, func(t *testing.T) {
			if got, err := (fixture.Temporal{Kind: fixture.KindDate, Literal: lit}).Time(); err == nil {
				t.Errorf("%q parsed as %s", lit, got)
			}
		})
	}
}
