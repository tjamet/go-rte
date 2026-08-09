package tempo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tjamet/go-rte/shared/rtehttp"
)

// calendarServer serves a fixture from the endpoint under test and records the
// query it was called with.
type calendarServer struct {
	*httptest.Server
	path        string
	query       string
	requests    int
	statusCode  int
	contentType string
	body        string
}

func newCalendarServer(t *testing.T, fixture string) *calendarServer {
	t.Helper()
	cs := &calendarServer{statusCode: http.StatusOK, contentType: "application/json"}
	if fixture != "" {
		cs.body = string(readFixture(t, fixture))
	}
	cs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cs.path = r.URL.Path
		cs.query = r.URL.RawQuery
		cs.requests++

		w.Header().Set("Content-Type", cs.contentType)
		w.WriteHeader(cs.statusCode)
		_, _ = io.WriteString(w, cs.body)
	}))
	t.Cleanup(cs.Close)
	return cs
}

func newService(t *testing.T, cs *calendarServer) *Service {
	t.Helper()
	return New(rtehttp.New(cs.Client(), cs.URL))
}

func TestCalendarsCallsTheDocumentedEndpoint(t *testing.T) {
	server := newCalendarServer(t, "live.json")

	got, err := newService(t, server).Calendars(context.Background(), Query{})

	require.NoError(t, err)
	assert.Equal(t, "/open_api/tempo_like_supply_contract/v1/tempo_like_calendars", server.path)
	assert.Equal(t, augustDays(t), got.Days())
}

func TestCalendarsSendsTheRequestedWindow(t *testing.T) {
	server := newCalendarServer(t, "live.json")

	_, err := newService(t, server).Calendars(context.Background(), Query{
		Start: at(t, "2026-08-05T00:00:00+02:00"),
		End:   at(t, "2026-08-11T00:00:00+02:00"),
	})

	require.NoError(t, err)
	assert.Contains(t, server.query, "start_date=2026-08-05T00%3A00%3A00%2B02%3A00")
	assert.Contains(t, server.query, "end_date=2026-08-11T00%3A00%3A00%2B02%3A00")
}

func TestCalendarsOmitsUnsetParameters(t *testing.T) {
	server := newCalendarServer(t, "live.json")

	_, err := newService(t, server).Calendars(context.Background(), Query{})

	require.NoError(t, err)
	assert.Empty(t, server.query, "an empty query must not send blank dates")
}

func TestCalendarsSendsFallbackStatusOnlyWhenAsked(t *testing.T) {
	server := newCalendarServer(t, "live.json")
	wanted := true

	_, err := newService(t, server).Calendars(context.Background(), Query{FallbackStatus: &wanted})

	require.NoError(t, err)
	assert.Contains(t, server.query, "fallback_status=true")
}

// F01: RTE rejects a half-specified window. Catching it here saves a round trip
// and gives a better message than the server's code.
func TestCalendarsRejectsAHalfSpecifiedWindowWithoutCallingTheAPI(t *testing.T) {
	server := newCalendarServer(t, "live.json")
	service := newService(t, server)

	_, startOnly := service.Calendars(context.Background(), Query{Start: at(t, "2026-08-05T00:00:00+02:00")})
	_, endOnly := service.Calendars(context.Background(), Query{End: at(t, "2026-08-11T00:00:00+02:00")})

	assert.ErrorIs(t, startOnly, ErrDateRangeIncomplete)
	assert.ErrorIs(t, endOnly, ErrDateRangeIncomplete)
	assert.Zero(t, server.requests, "an invalid window must not reach the API")
}

// F02: start after end.
func TestCalendarsRejectsAnInvertedWindowWithoutCallingTheAPI(t *testing.T) {
	server := newCalendarServer(t, "live.json")

	_, err := newService(t, server).Calendars(context.Background(), Query{
		Start: at(t, "2026-08-11T00:00:00+02:00"),
		End:   at(t, "2026-08-05T00:00:00+02:00"),
	})

	assert.ErrorIs(t, err, ErrDateRangeInverted)
	assert.Zero(t, server.requests, "an inverted window must not reach the API")
}

func TestCalendarsSurfacesTheBusinessErrorCode(t *testing.T) {
	server := newCalendarServer(t, "")
	server.statusCode = http.StatusBadRequest
	server.body = `{"error":"TMPLIKSUPCON_TMPLIKCAL_F03","error_description":"period too long"}`

	_, err := newService(t, server).Calendars(context.Background(), Query{})

	require.Error(t, err)
	assert.Equal(t, CodePeriodTooLong, rtehttp.Code(err))
}
