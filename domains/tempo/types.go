// Package tempo reads the RTE "Tempo Like Supply Contract" signal: the colour
// assigned to each day of a Tempo tariff year. A year holds 20 RED days (peak
// consumption), 40 WHITE days (intermediate) and BLUE days for the remainder;
// RTE publishes the next day's colour every afternoon.
//
// Three sources describe the response and none of them agree, so the decoding in
// rest.go deliberately accepts all of their shapes:
//
//	source                  root      "values"  colour key  fallback
//	Swagger.json            array     array     "values"    "true" (string)
//	sample archive (2016)   array     object    "value"     true (bool)
//	live API (2026-08)      object    array     "value"     absent
//
// Callers work with the normalised types below and never see that variance.
package tempo

import (
	"slices"
	"time"
)

// Colour is the tariff colour of a Tempo day.
type Colour string

// The three Tempo colours, spelled as the JSON API returns them. The legacy XML
// representation uses the French BLEU/BLANC/ROUGE and is translated on decode.
const (
	Blue  Colour = "BLUE"
	White Colour = "WHITE"
	Red   Colour = "RED"
)

// Valid reports whether c is one of the three colours RTE publishes.
func (c Colour) Valid() bool {
	return c == Blue || c == White || c == Red
}

// Day is a single Tempo day and the colour that applies to it.
//
// Start and End keep the UTC offset RTE sent (+01:00 in winter, +02:00 in
// summer) rather than being normalised to UTC, because a Tempo day runs from
// Paris local midnight to Paris local midnight. Converting to UTC would shift
// the boundary and put an evening in the wrong day.
type Day struct {
	Start     time.Time
	End       time.Time
	Colour    Colour
	Fallback  bool // the value was produced in degraded mode
	UpdatedAt time.Time
}

// Contains reports whether instant falls in the day, start inclusive, end exclusive.
func (d Day) Contains(instant time.Time) bool {
	return !instant.Before(d.Start) && instant.Before(d.End)
}

// Calendar is a window of Tempo days as returned by one API call.
type Calendar struct {
	Start time.Time
	End   time.Time
	Days  []Day
}

// Calendars is the decoded response.
//
// It stays a slice even though the live API returns a single calendar object:
// the spec and the sample archive both return a list, and normalising the object
// form to a one-element slice keeps every observed shape representable without
// having to merge windows together.
type Calendars []Calendar

// Days returns every day across every calendar, ordered from oldest to newest.
// RTE returns days newest-first, which is rarely what a caller wants.
func (cs Calendars) Days() []Day {
	days := []Day{}
	for _, c := range cs {
		days = append(days, c.Days...)
	}
	slices.SortStableFunc(days, func(a, b Day) int { return a.Start.Compare(b.Start) })
	return days
}

// ColourAt returns the colour covering instant, and whether a day was found.
func (cs Calendars) ColourAt(instant time.Time) (Colour, bool) {
	for _, c := range cs {
		for _, d := range c.Days {
			if d.Contains(instant) {
				return d.Colour, true
			}
		}
	}
	return "", false
}
