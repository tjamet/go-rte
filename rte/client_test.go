package rte

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tjamet/go-rte/domains/tempo"
	"github.com/tjamet/go-rte/shared/auth"
)

// platform fakes both halves of the RTE platform: the token endpoint and the
// Tempo API, so a test can exercise the whole wiring in one go.
type platform struct {
	*httptest.Server
	tokenRequests     int
	calendarRequests  int
	seenAuthorization string
}

func newPlatform(t *testing.T) *platform {
	t.Helper()
	p := &platform{}
	mux := http.NewServeMux()
	mux.HandleFunc("/token/oauth", func(w http.ResponseWriter, r *http.Request) {
		p.tokenRequests++
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"access_token": "granted-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
		}))
	})
	mux.HandleFunc(tempo.CalendarsPath, func(w http.ResponseWriter, r *http.Request) {
		p.calendarRequests++
		p.seenAuthorization = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tempo_like_calendars":{`+
			`"start_date":"2026-08-05T00:00:00+02:00","end_date":"2026-08-11T00:00:00+02:00",`+
			`"values":[{"start_date":"2026-08-10T00:00:00+02:00","end_date":"2026-08-11T00:00:00+02:00",`+
			`"value":"RED","updated_date":"2026-08-09T10:20:00+02:00"}]}}`)
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

func newTestClient(t *testing.T, p *platform) *Client {
	t.Helper()
	return New(Options{
		ClientID:     "an-id",
		ClientSecret: "a-secret",
		BaseURL:      p.URL,
		TokenURL:     p.URL + "/token/oauth",
		HTTPClient:   p.Client(),
	})
}

// The point of the facade: credentials in, typed days out, with the token
// exchange happening on its own.
func TestClientAuthenticatesAndReadsCalendars(t *testing.T) {
	p := newPlatform(t)

	days, err := newTestClient(t, p).Tempo.Calendars(context.Background(), tempo.Query{})

	require.NoError(t, err)
	require.Len(t, days.Days(), 1)
	assert.Equal(t, tempo.Red, days.Days()[0].Colour)
	assert.Equal(t, "Bearer granted-token", p.seenAuthorization)
	assert.Equal(t, 1, p.tokenRequests)
}

func TestClientReusesTheTokenAcrossCalls(t *testing.T) {
	p := newPlatform(t)
	client := newTestClient(t, p)

	for range 3 {
		_, err := client.Tempo.Calendars(context.Background(), tempo.Query{})
		require.NoError(t, err)
	}

	assert.Equal(t, 3, p.calendarRequests)
	assert.Equal(t, 1, p.tokenRequests, "one token must serve every call")
}

// A caller who already holds a token should not need a client id and secret.
func TestNewWithAuthenticatorAcceptsAPreObtainedToken(t *testing.T) {
	p := newPlatform(t)

	client := NewWithAuthenticator(auth.StaticToken("already-mine"), Options{
		BaseURL:    p.URL,
		HTTPClient: p.Client(),
	})
	_, err := client.Tempo.Calendars(context.Background(), tempo.Query{})

	require.NoError(t, err)
	assert.Equal(t, "Bearer already-mine", p.seenAuthorization)
	assert.Zero(t, p.tokenRequests, "a supplied token must not trigger an exchange")
}

func TestClientSendsAUserAgent(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"tempo_like_calendars":[]}`)
	}))
	t.Cleanup(server.Close)

	client := NewWithAuthenticator(auth.StaticToken("t"), Options{BaseURL: server.URL, HTTPClient: server.Client()})
	_, err := client.Tempo.Calendars(context.Background(), tempo.Query{})

	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(seen, "go-rte/"), "got %q", seen)
}

func TestDefaultsPointAtTheRTEPlatform(t *testing.T) {
	client := New(Options{ClientID: "id", ClientSecret: "secret"})

	assert.Equal(t, "https://digital.iservices.rte-france.com", client.API.BaseURL)
	require.NotNil(t, client.Tempo)
}
