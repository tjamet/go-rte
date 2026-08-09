package tempo

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDateKeyIsTheCacheLayout(t *testing.T) {
	assert.Equal(t, "2026/08/10.json", Date{2026, time.August, 10}.Key())
	assert.Equal(t, "2014/09/01.json", Date{2014, time.September, 1}.Key())
}

func TestDateStringIsISO(t *testing.T) {
	assert.Equal(t, "2026-08-10", Date{2026, time.August, 10}.String())
}

func TestDateAddCrossesMonthAndYearBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		from Date
		days int
		want Date
	}{
		{"forward over a month end", Date{2026, time.August, 31}, 1, Date{2026, time.September, 1}},
		{"backward over a year start", Date{2026, time.January, 1}, -1, Date{2025, time.December, 31}},
		{"across a leap day", Date{2024, time.February, 28}, 1, Date{2024, time.February, 29}},
		{"a whole window", Date{2026, time.August, 10}, -180, Date{2026, time.February, 11}},
		{"no movement", Date{2026, time.August, 10}, 0, Date{2026, time.August, 10}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.from.Add(tc.days))
		})
	}
}

// The DST switchovers are the reason Date exists rather than a time.Time: adding
// 24 hours to an instant would land on the same calendar day in spring and skip
// one in autumn.
func TestDateAddIsUnaffectedByDaylightSaving(t *testing.T) {
	assert.Equal(t, Date{2026, time.March, 30}, Date{2026, time.March, 29}.Add(1))
	assert.Equal(t, Date{2026, time.October, 26}, Date{2026, time.October, 25}.Add(1))
}

func TestDateCompareOrdersChronologically(t *testing.T) {
	early, late := Date{2026, time.August, 10}, Date{2026, time.September, 1}

	assert.Negative(t, early.Compare(late))
	assert.Positive(t, late.Compare(early))
	assert.Zero(t, early.Compare(early))
}

func TestDateSubCountsDaysBetween(t *testing.T) {
	assert.Equal(t, 360, Date{2026, time.August, 11}.Sub(Date{2025, time.August, 16}))
	assert.Equal(t, -1, Date{2025, time.December, 31}.Sub(Date{2026, time.January, 1}))
}

func TestDateRangeSpansParisLocalMidnights(t *testing.T) {
	paris, err := time.LoadLocation(parisZone)
	require.NoError(t, err)

	start, end := Date{2026, time.August, 10}.Range(paris)

	assert.Equal(t, "2026-08-10T00:00:00+02:00", start.Format(time.RFC3339))
	assert.Equal(t, "2026-08-11T00:00:00+02:00", end.Format(time.RFC3339))
}

// A Tempo day that contains a DST switchover is 23 or 25 hours long, and its
// boundaries carry different offsets.
func TestDateRangeKeepsTheOffsetOnEitherSideOfASwitchover(t *testing.T) {
	paris, err := time.LoadLocation(parisZone)
	require.NoError(t, err)

	start, end := Date{2026, time.March, 29}.Range(paris)

	assert.Equal(t, "2026-03-29T00:00:00+01:00", start.Format(time.RFC3339))
	assert.Equal(t, "2026-03-30T00:00:00+02:00", end.Format(time.RFC3339))
	assert.Equal(t, 23*time.Hour, end.Sub(start))
}

func TestDateOfTakesTheParisCalendarDay(t *testing.T) {
	paris, err := time.LoadLocation(parisZone)
	require.NoError(t, err)

	// 23:30 UTC is already the next day in Paris.
	instant := time.Date(2026, time.August, 10, 23, 30, 0, 0, time.UTC)

	assert.Equal(t, Date{2026, time.August, 11}, DateOf(instant, paris))
}

func TestParseDateRejectsWhatIsNotADay(t *testing.T) {
	for _, tc := range []struct {
		name          string
		year, month   string
		day           string
		want          Date
		wantParseFail bool
	}{
		{name: "a real day", year: "2026", month: "08", day: "10", want: Date{2026, time.August, 10}},
		{name: "a leap day", year: "2024", month: "02", day: "29", want: Date{2024, time.February, 29}},
		{name: "a day that does not exist", year: "2026", month: "02", day: "30", wantParseFail: true},
		{name: "month thirteen", year: "2026", month: "13", day: "01", wantParseFail: true},
		{name: "unpadded", year: "2026", month: "8", day: "10", wantParseFail: true},
		{name: "not a number", year: "2026", month: "aa", day: "10", wantParseFail: true},
		{name: "empty", year: "", month: "", day: "", wantParseFail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDate(tc.year, tc.month, tc.day)

			if tc.wantParseFail {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}
