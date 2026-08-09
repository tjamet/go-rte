package tempo

import (
	"fmt"
	"time"
)

// Date is a Tempo day identified by its Paris calendar date.
//
// It is deliberately not a time.Time. The cache key, the fetch window
// arithmetic and the URL path are all calendar-day concepts, and an instant
// answers them wrongly twice a year: adding 24 hours across the spring
// switchover lands on the same calendar day, and across the autumn one skips
// a day. Date counts days, so neither can happen.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

// DateOf returns the calendar day instant falls on in loc.
func DateOf(instant time.Time, loc *time.Location) Date {
	year, month, day := instant.In(loc).Date()
	return Date{Year: year, Month: month, Day: day}
}

// ParseDate builds a Date from the three zero-padded path segments of a request.
//
// It rejects anything that is not a real day, including a well-formed date that
// does not exist such as the 30th of February, so the handler never asks the
// cache for a day that cannot have a colour.
func ParseDate(year, month, day string) (Date, error) {
	if len(year) != 4 || len(month) != 2 || len(day) != 2 {
		return Date{}, fmt.Errorf("tempo: %q-%q-%q is not a zero-padded YYYY MM DD date", year, month, day)
	}
	parsed, err := time.Parse(time.DateOnly, year+"-"+month+"-"+day)
	if err != nil {
		return Date{}, fmt.Errorf("tempo: parse date: %w", err)
	}
	return DateOf(parsed, time.UTC), nil
}

// Key is where the day is cached, relative to the store root.
func (d Date) Key() string { return d.path() + ".json" }

// String renders the day as YYYY-MM-DD.
func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

// MonthPrefix is the store prefix holding every day of d's month. Listing it is
// how the cache learns which days it already holds without probing each one.
func (d Date) MonthPrefix() string { return fmt.Sprintf("%04d/%02d/", d.Year, d.Month) }

func (d Date) path() string { return fmt.Sprintf("%04d/%02d/%02d", d.Year, d.Month, d.Day) }

// Add returns the day days away from d, normalising across month and year ends.
func (d Date) Add(days int) Date { return DateOf(d.midnightUTC().AddDate(0, 0, days), time.UTC) }

// Sub returns the number of days from o to d.
func (d Date) Sub(o Date) int {
	return int(d.midnightUTC().Sub(o.midnightUTC()) / (24 * time.Hour))
}

// Compare orders days chronologically, as [time.Time.Compare] does for instants.
func (d Date) Compare(o Date) int {
	return d.midnightUTC().Compare(o.midnightUTC())
}

// Before reports whether d is earlier than o.
func (d Date) Before(o Date) bool { return d.Compare(o) < 0 }

// After reports whether d is later than o.
func (d Date) After(o Date) bool { return d.Compare(o) > 0 }

// Range returns the instants a Tempo day starts and ends at in loc: local
// midnight to the next local midnight. The two carry different UTC offsets on
// the days a switchover falls in, which is why the end is computed as a day
// later rather than as start plus 24 hours.
func (d Date) Range(loc *time.Location) (start, end time.Time) {
	start = time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, loc)
	next := d.Add(1)
	end = time.Date(next.Year, next.Month, next.Day, 0, 0, 0, 0, loc)
	return start, end
}

// midnightUTC anchors the day in UTC, where every day is exactly 24 hours, so
// that arithmetic on Dates never has to reason about offsets.
func (d Date) midnightUTC() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}
