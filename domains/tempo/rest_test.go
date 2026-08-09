package tempo

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	require.NoError(t, err)
	return b
}

func at(t *testing.T, rfc3339 string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc3339)
	require.NoError(t, err)
	return ts
}

func day(t *testing.T, start, end string, colour Colour, fallback bool, updated string) Day {
	t.Helper()
	return Day{
		Start:     at(t, start),
		End:       at(t, end),
		Colour:    colour,
		Fallback:  fallback,
		UpdatedAt: at(t, updated),
	}
}

func expectDecodesToDays(t *testing.T, fixture string, expected []Day) {
	t.Helper()
	got, err := decodeJSONCalendars(readFixture(t, fixture))
	require.NoError(t, err)
	assert.Equal(t, expected, got.Days())
}

// augustDays is the payload the live API returned on 2026-08-09, expressed as
// domain days. live.json and spec.json carry it in two different wire shapes and
// must decode identically.
func augustDays(t *testing.T) []Day {
	t.Helper()
	return []Day{
		day(t, "2026-08-05T00:00:00+02:00", "2026-08-06T00:00:00+02:00", Blue, false, "2026-08-04T10:20:00+02:00"),
		day(t, "2026-08-06T00:00:00+02:00", "2026-08-07T00:00:00+02:00", Blue, false, "2026-08-05T10:20:00+02:00"),
		day(t, "2026-08-07T00:00:00+02:00", "2026-08-08T00:00:00+02:00", Blue, false, "2026-08-06T10:20:00+02:00"),
		day(t, "2026-08-08T00:00:00+02:00", "2026-08-09T00:00:00+02:00", Blue, false, "2026-08-07T10:20:00+02:00"),
		day(t, "2026-08-09T00:00:00+02:00", "2026-08-10T00:00:00+02:00", Blue, false, "2026-08-08T10:20:00+02:00"),
		day(t, "2026-08-10T00:00:00+02:00", "2026-08-11T00:00:00+02:00", Blue, false, "2026-08-09T10:20:00+02:00"),
	}
}

// The three sources disagree on the wire shape (see the table in the package doc):
// the root object may be an object or an array, "values" may be an object or an
// array, and the colour may be keyed "value" or "values". All must decode alike.
func TestDecodeHandlesEveryObservedShape(t *testing.T) {
	// object root, values array, colour under "value" — the live API.
	expectDecodesToDays(t, "live.json", augustDays(t))
	// array root, values array, colour under "values" — literally what Swagger.json declares.
	expectDecodesToDays(t, "spec.json", augustDays(t))
	// array root, values object, colour under "value" — the 2016 sample archive.
	expectDecodesToDays(t, "TLC-RG02.v1.0.1.json", []Day{
		day(t, "2016-03-03T00:00:00+01:00", "2016-03-04T00:00:00+01:00", White, false, "2016-03-02T10:20:00+01:00"),
		day(t, "2016-03-04T00:00:00+01:00", "2016-03-05T00:00:00+01:00", White, false, "2016-03-03T10:20:00+01:00"),
		day(t, "2016-03-05T00:00:00+01:00", "2016-03-06T00:00:00+01:00", White, false, "2016-03-04T10:20:00+01:00"),
		day(t, "2016-03-06T00:00:00+01:00", "2016-03-07T00:00:00+01:00", Blue, false, "2016-03-05T10:20:00+01:00"),
		day(t, "2016-03-07T00:00:00+01:00", "2016-03-08T00:00:00+01:00", White, false, "2016-03-06T10:20:00+01:00"),
		day(t, "2016-03-08T00:00:00+01:00", "2016-03-09T00:00:00+01:00", White, false, "2016-03-07T10:20:00+01:00"),
		day(t, "2016-03-09T00:00:00+01:00", "2016-03-10T00:00:00+01:00", White, false, "2016-03-08T10:20:00+01:00"),
		day(t, "2016-03-10T00:00:00+01:00", "2016-03-11T00:00:00+01:00", White, false, "2016-03-09T10:20:00+01:00"),
	})
}

// fallback is a JSON bool in the sample archive but a quoted string in Swagger.json.
func TestDecodeAcceptsFallbackAsBoolAndString(t *testing.T) {
	expectDecodesToDays(t, "TLC-RG05.v1.0.1.json", []Day{
		day(t, "2016-03-12T00:00:00+01:00", "2016-03-13T00:00:00+01:00", Blue, true, "2016-03-11T10:20:00+01:00"),
	})
	// spec.json carries fallback:"false" as a string on every day.
	expectDecodesToDays(t, "spec.json", augustDays(t))
}

func TestDecodeSingleDayFixtures(t *testing.T) {
	expectDecodesToDays(t, "TLC-RG01.v1.0.1.json", []Day{
		day(t, "2016-03-12T00:00:00+01:00", "2016-03-13T00:00:00+01:00", Blue, false, "2016-03-11T10:20:00+01:00"),
	})
	expectDecodesToDays(t, "TLC-RG04.v1.0.1.json", []Day{
		day(t, "2016-03-02T00:00:00+01:00", "2016-03-03T00:00:00+01:00", White, false, "2016-03-01T10:20:00+01:00"),
	})
}

// The outer calendar window is preserved even though the days are what callers want.
func TestDecodeKeepsCalendarWindow(t *testing.T) {
	got, err := decodeJSONCalendars(readFixture(t, "live.json"))
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, at(t, "2026-01-01T00:00:00+01:00"), got[0].Start)
	assert.Equal(t, at(t, "2026-08-11T00:00:00+02:00"), got[0].End)
}

func expectColourAt(t *testing.T, cs Calendars, when string, colour Colour) {
	t.Helper()
	got, ok := cs.ColourAt(at(t, when))
	require.True(t, ok, "no colour for %s", when)
	assert.Equal(t, colour, got)
}

func TestColourAtFindsTheDayCoveringAnInstant(t *testing.T) {
	cs, err := decodeJSONCalendars(readFixture(t, "TLC-RG02.v1.0.1.json"))
	require.NoError(t, err)

	expectColourAt(t, cs, "2016-03-06T00:00:00+01:00", Blue)  // exactly the start boundary
	expectColourAt(t, cs, "2016-03-06T18:30:00+01:00", Blue)  // mid-day
	expectColourAt(t, cs, "2016-03-07T00:00:00+01:00", White) // end is exclusive

	_, ok := cs.ColourAt(at(t, "2020-01-01T00:00:00+01:00"))
	assert.False(t, ok, "instant outside every calendar must report no colour")
}
