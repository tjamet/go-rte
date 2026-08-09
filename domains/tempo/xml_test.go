package tempo

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func expectXMLDecodesToDays(t *testing.T, fixture string, expected []Day) {
	t.Helper()
	got, err := decodeCalendars(readFixture(t, fixture), "application/xml")
	require.NoError(t, err)
	assertSameDays(t, expected, got.Days())
}

func assertSameDays(t *testing.T, expected, got []Day) {
	t.Helper()
	require.Len(t, got, len(expected))
	for i := range expected {
		assertSameTime(t, expected[i].Start, got[i].Start)
		assertSameTime(t, expected[i].End, got[i].End)
		assertSameTime(t, expected[i].UpdatedAt, got[i].UpdatedAt)
		assert.Equal(t, expected[i].Colour, got[i].Colour)
		assert.Equal(t, expected[i].Fallback, got[i].Fallback)
	}
}

// assertSameTime compares the instant and the UTC offset rather than the whole
// time.Time. The XML path resolves dates against the Europe/Paris database
// while the expectations come from RFC3339 parsing, so the two carry different
// *time.Location values for the very same wall clock.
func assertSameTime(t *testing.T, expected, got time.Time) {
	t.Helper()
	assert.True(t, expected.Equal(got), "expected %s, got %s", expected, got)
	_, wantOffset := expected.Zone()
	_, gotOffset := got.Zone()
	assert.Equal(t, wantOffset, gotOffset, "UTC offset differs for %s", expected)
}

// The archive is inconsistent: RG02 and RG04 wrap the days in <Tempos> and
// carry an XML prolog, RG01 and RG05 are a bare <Tempo> with neither.
func TestXMLDecodesBothRootShapes(t *testing.T) {
	expectXMLDecodesToDays(t, "TLC-RG01.v1.0.1.xml", []Day{
		day(t, "2016-03-12T00:00:00+01:00", "2016-03-13T00:00:00+01:00", Blue, false, "2016-03-11T00:00:00+01:00"),
	})
	expectXMLDecodesToDays(t, "TLC-RG04.v1.0.1.xml", []Day{
		day(t, "2016-03-02T00:00:00+01:00", "2016-03-03T00:00:00+01:00", White, false, "2016-03-01T00:00:00+01:00"),
	})
}

func TestXMLTranslatesFrenchColours(t *testing.T) {
	expectXMLDecodesToDays(t, "TLC-RG02.v1.0.1.xml", []Day{
		day(t, "2016-03-03T00:00:00+01:00", "2016-03-04T00:00:00+01:00", White, false, "2016-03-02T00:00:00+01:00"),
		day(t, "2016-03-04T00:00:00+01:00", "2016-03-05T00:00:00+01:00", White, false, "2016-03-03T00:00:00+01:00"),
		day(t, "2016-03-05T00:00:00+01:00", "2016-03-06T00:00:00+01:00", White, false, "2016-03-04T00:00:00+01:00"),
		day(t, "2016-03-06T00:00:00+01:00", "2016-03-07T00:00:00+01:00", Blue, false, "2016-03-05T00:00:00+01:00"),
		day(t, "2016-03-07T00:00:00+01:00", "2016-03-08T00:00:00+01:00", White, false, "2016-03-06T00:00:00+01:00"),
		day(t, "2016-03-08T00:00:00+01:00", "2016-03-09T00:00:00+01:00", White, false, "2016-03-07T00:00:00+01:00"),
		day(t, "2016-03-09T00:00:00+01:00", "2016-03-10T00:00:00+01:00", White, false, "2016-03-08T00:00:00+01:00"),
		day(t, "2016-03-10T00:00:00+01:00", "2016-03-11T00:00:00+01:00", White, false, "2016-03-09T00:00:00+01:00"),
	})
}

// A Tempo day ends at the next local midnight, which is 23 or 25 hours long on
// the days France changes clocks. Adding a calendar day rather than 24h keeps
// the boundary right.
func TestXMLDayEndsAtTheNextLocalMidnightAcrossDST(t *testing.T) {
	// France moved to CEST on 2016-03-27 at 02:00.
	got, err := decodeCalendars([]byte(
		`<Tempo><DateHeureCreation>2016-03-26</DateHeureCreation>`+
			`<DateApplication>2016-03-27</DateApplication><Couleur>ROUGE</Couleur></Tempo>`),
		"application/xml")
	require.NoError(t, err)

	days := got.Days()
	require.Len(t, days, 1)
	assert.Equal(t, Red, days[0].Colour)
	assert.Equal(t, "2016-03-27T00:00:00+01:00", days[0].Start.Format("2006-01-02T15:04:05Z07:00"))
	assert.Equal(t, "2016-03-28T00:00:00+02:00", days[0].End.Format("2006-01-02T15:04:05Z07:00"))
	assert.Equal(t, 23.0, days[0].End.Sub(days[0].Start).Hours(), "the spring-forward day is 23 hours")
}

func TestXMLRejectsAnUnknownColour(t *testing.T) {
	_, err := decodeCalendars([]byte(
		`<Tempo><DateHeureCreation>2016-03-11</DateHeureCreation>`+
			`<DateApplication>2016-03-12</DateApplication><Couleur>VERT</Couleur></Tempo>`),
		"application/xml")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "VERT")
}

// The content type decides the decoder, so a caller never has to.
func TestDecodeCalendarsDispatchesOnContentType(t *testing.T) {
	json, err := decodeCalendars(readFixture(t, "TLC-RG01.v1.0.1.json"), "application/json; charset=utf-8")
	require.NoError(t, err)
	xml, err := decodeCalendars(readFixture(t, "TLC-RG01.v1.0.1.xml"), "application/xml; charset=utf-8")
	require.NoError(t, err)

	// Same day in both representations; the JSON one also carries a publication time.
	require.Len(t, json.Days(), 1)
	require.Len(t, xml.Days(), 1)
	assertSameTime(t, json.Days()[0].Start, xml.Days()[0].Start)
	assertSameTime(t, json.Days()[0].End, xml.Days()[0].End)
	assert.Equal(t, json.Days()[0].Colour, xml.Days()[0].Colour)
}

// End to end: the service must honour an XML answer even though it asked for JSON.
func TestServiceDecodesAnXMLResponse(t *testing.T) {
	server := newCalendarServer(t, "TLC-RG02.v1.0.1.xml")
	server.contentType = "application/xml"

	got, err := newService(t, server).Calendars(context.Background(), Query{})

	require.NoError(t, err)
	require.Len(t, got.Days(), 8)
	assert.Equal(t, Blue, got.Days()[3].Colour, "2016-03-06 is the blue day")
	assert.Equal(t, http.StatusOK, server.statusCode)
}
