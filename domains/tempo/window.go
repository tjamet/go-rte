package tempo

import "time"

// PublishHour is the Paris hour from which RTE has published the next day's
// colour. Before it, tomorrow simply does not exist yet.
const PublishHour = 11

// DefaultWindowDays is how many days one fetch asks for.
//
// A single day is the cheapest possible call and the worst possible use of it:
// the rate limit counts calls, not days, so asking for a year's worth costs the
// same as asking for one and leaves the cache able to answer far more. It stays
// under RTE's 366-day ceiling (TMPLIKSUPCON_TMPLIKCAL_F03) with room to spare.
const DefaultWindowDays = 360

// maxWindowDays is the longest range RTE will accept in one call.
const maxWindowDays = 366

// horizonAt returns the latest day that may be served: today, or tomorrow once
// Paris has reached the publication hour.
func horizonAt(now time.Time, loc *time.Location) Date {
	local := now.In(loc)
	today := DateOf(local, loc)
	if local.Hour() < PublishHour {
		return today
	}
	return today.Add(1)
}

// window is an inclusive range of days to fetch in one call.
type window struct {
	Lo, Hi Date
}

// days is how many days the window spans, both ends included.
func (w window) days() int { return w.Hi.Sub(w.Lo) + 1 }

// contains reports whether d falls in the window.
func (w window) contains(d Date) bool { return !d.Before(w.Lo) && !d.After(w.Hi) }

// query renders the window as an API query. RTE's end_date is exclusive, so it
// is the midnight following the last day rather than that day's own midnight.
func (w window) query(loc *time.Location) Query {
	start, _ := w.Lo.Range(loc)
	_, end := w.Hi.Range(loc)
	return Query{Start: start, End: end}
}

// planWindow chooses the range to fetch so that a miss on d fills as much of
// the cache as one call allows.
//
// floor may be the zero Date, meaning the bottom of RTE's history is not known
// yet and nothing bounds the window from below.
//
// The range is built in three steps, and the order matters:
//
//  1. centre size days on d;
//  2. slide the whole range back if it overruns the horizon, so it stays full
//     rather than being cut short — a call that asks for 200 days costs the same
//     as one that asks for 360;
//  3. trim the runs of already-cached days off each end.
//
// Trimming stops at the ends on purpose. A cached day in the interior stays
// inside the range: re-fetching it is free, whereas splitting the range around
// it would cost a second call against the rate limit.
//
// Because step 2 slides rather than extends and step 3 only narrows, the result
// can never exceed size days, and so can never provoke F03.
//
// ok is false when nothing is left to ask for, in which case no call is made.
func planWindow(d, floor, horizon Date, cached func(Date) bool, size int) (window, bool) {
	if size <= 0 || size > maxWindowDays {
		size = DefaultWindowDays
	}

	w := window{Lo: d.Add(-(size / 2))}
	w.Hi = w.Lo.Add(size - 1)

	if w.Hi.After(horizon) {
		w = window{Lo: w.Lo.Add(-w.Hi.Sub(horizon)), Hi: horizon}
	}
	if !floor.IsZero() && w.Lo.Before(floor) {
		w.Lo = floor
	}

	for !w.Lo.After(w.Hi) && cached(w.Lo) {
		w.Lo = w.Lo.Add(1)
	}
	for !w.Lo.After(w.Hi) && cached(w.Hi) {
		w.Hi = w.Hi.Add(-1)
	}
	return w, !w.Lo.After(w.Hi)
}
