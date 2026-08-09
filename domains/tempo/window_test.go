package tempo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHorizonReachesTomorrowOnlyOncePublished(t *testing.T) {
	paris := parisLocation(t)

	for _, tc := range []struct {
		name string
		now  time.Time
		want Date
	}{
		{
			name: "just before the publication hour",
			now:  time.Date(2026, time.August, 10, 10, 59, 59, 0, paris),
			want: Date{2026, time.August, 10},
		},
		{
			name: "on the publication hour",
			now:  time.Date(2026, time.August, 10, 11, 0, 0, 0, paris),
			want: Date{2026, time.August, 11},
		},
		{
			name: "late in the evening",
			now:  time.Date(2026, time.August, 10, 23, 59, 0, 0, paris),
			want: Date{2026, time.August, 11},
		},
		{
			// 09:00 UTC is 11:00 in Paris in summer: the rule follows Paris, not
			// the clock of whatever machine is serving.
			name: "expressed in UTC during summer time",
			now:  time.Date(2026, time.August, 10, 9, 0, 0, 0, time.UTC),
			want: Date{2026, time.August, 11},
		},
		{
			// 09:00 UTC is only 10:00 in Paris in winter, so tomorrow is not out yet.
			name: "expressed in UTC during winter time",
			now:  time.Date(2026, time.January, 15, 9, 0, 0, 0, time.UTC),
			want: Date{2026, time.January, 15},
		},
		{
			name: "over a year end",
			now:  time.Date(2026, time.December, 31, 12, 0, 0, 0, paris),
			want: Date{2027, time.January, 1},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, horizonAt(tc.now, paris))
		})
	}
}

// cachedDays makes a lookup for the planner out of a list of days already held.
func cachedDays(days ...Date) func(Date) bool {
	held := make(map[Date]bool, len(days))
	for _, d := range days {
		held[d] = true
	}
	return func(d Date) bool { return held[d] }
}

// cachedRange makes a lookup covering every day from lo to hi inclusive.
func cachedRange(lo, hi Date) func(Date) bool {
	return func(d Date) bool { return !d.Before(lo) && !d.After(hi) }
}

func nothingCached(Date) bool { return false }

func TestPlanWindowCentresOnTheRequestedDay(t *testing.T) {
	horizon := Date{2027, time.January, 1}

	got, ok := planWindow(Date{2020, time.June, 15}, Date{}, horizon, nothingCached, 360)

	require.True(t, ok)
	assert.Equal(t, Date{2019, time.December, 18}, got.Lo)
	assert.Equal(t, Date{2020, time.December, 11}, got.Hi)
	assert.Equal(t, 360, got.days())
}

func TestPlanWindowSlidesBackRatherThanOverrunTheHorizon(t *testing.T) {
	horizon := Date{2026, time.August, 11}

	got, ok := planWindow(Date{2026, time.August, 10}, Date{}, horizon, nothingCached, 360)

	require.True(t, ok)
	assert.Equal(t, horizon, got.Hi, "the window never reaches past what may be known")
	assert.Equal(t, 360, got.days(), "sliding keeps the window full, so the call is not wasted")
	assert.Equal(t, Date{2025, time.August, 17}, got.Lo)
}

func TestPlanWindowStopsAtTheFloor(t *testing.T) {
	floor := Date{2014, time.September, 1}

	got, ok := planWindow(Date{2014, time.September, 10}, floor, Date{2027, time.January, 1}, nothingCached, 360)

	require.True(t, ok)
	assert.Equal(t, floor, got.Lo)
	assert.Less(t, got.days(), 360, "a window against the floor is simply shorter")
}

// RTE rejects a range longer than 366 days as TMPLIKSUPCON_TMPLIKCAL_F03. The
// window is built by sliding and narrowing only, so it can never get there.
func TestPlanWindowNeverExceedsWhatTheAPIAccepts(t *testing.T) {
	horizon := Date{2026, time.August, 11}
	floor := Date{2014, time.September, 1}

	for offset := 0; offset < 400; offset++ {
		day := horizon.Add(-offset)

		got, ok := planWindow(day, floor, horizon, nothingCached, 360)

		require.True(t, ok)
		assert.LessOrEqual(t, got.days(), 366, "requesting %s planned %d days", day, got.days())
	}
}

func TestPlanWindowTrimsCachedRunsFromBothEnds(t *testing.T) {
	day := Date{2020, time.June, 15}
	// Everything from the natural start up to a week before the day is held,
	// and so is everything from a week after it to the natural end.
	cached := func(d Date) bool {
		return d.Before(day.Add(-7)) || d.After(day.Add(7))
	}

	got, ok := planWindow(day, Date{}, Date{2027, time.January, 1}, cached, 360)

	require.True(t, ok)
	assert.Equal(t, day.Add(-7), got.Lo)
	assert.Equal(t, day.Add(7), got.Hi)
}

// Trimming deliberately stops at the ends. A cached day in the middle rides
// along inside the range, because one call costs the same either way and
// splitting around it would cost a second.
func TestPlanWindowKeepsCachedDaysInTheInterior(t *testing.T) {
	day := Date{2020, time.June, 15}

	got, ok := planWindow(day, Date{}, Date{2027, time.January, 1}, cachedDays(day.Add(3)), 360)

	require.True(t, ok)
	assert.Equal(t, 360, got.days())
	assert.True(t, got.contains(day.Add(3)))
}

func TestPlanWindowFindsNothingToDoWhenEverythingIsHeld(t *testing.T) {
	day := Date{2020, time.June, 15}

	_, ok := planWindow(day, Date{}, Date{2027, time.January, 1}, cachedRange(day.Add(-999), day.Add(999)), 360)

	assert.False(t, ok, "a fully cached neighbourhood makes no upstream call at all")
}

// The day that caused the miss is by definition not cached, so trimming can
// never remove it: a fetch always covers the day that provoked it.
func TestPlanWindowAlwaysCoversTheRequestedDay(t *testing.T) {
	day := Date{2026, time.August, 10}
	horizon := Date{2026, time.August, 11}
	// Everything except the requested day is already held.
	cached := func(d Date) bool { return d != day }

	got, ok := planWindow(day, Date{}, horizon, cached, 360)

	require.True(t, ok)
	assert.Equal(t, day, got.Lo)
	assert.Equal(t, day, got.Hi)
	assert.True(t, got.contains(day))
}

func TestWindowQueryAsksForWholeParisDays(t *testing.T) {
	paris := parisLocation(t)
	w := window{Lo: Date{2026, time.August, 10}, Hi: Date{2026, time.August, 11}}

	query := w.query(paris)

	assert.Equal(t, "2026-08-10T00:00:00+02:00", query.Start.Format(time.RFC3339))
	assert.Equal(t, "2026-08-12T00:00:00+02:00", query.End.Format(time.RFC3339),
		"the end is exclusive, so it is the midnight after the last day")
	assert.NoError(t, query.Validate())
}
