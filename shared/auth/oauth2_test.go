package auth

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenServer stands in for https://digital.iservices.rte-france.com/token/oauth.
// It records every exchange so a test can assert how often a token was fetched.
type tokenServer struct {
	*httptest.Server
	mu        sync.Mutex
	requests  []tokenRequest
	expiresIn int
}

type tokenRequest struct {
	clientID     string
	clientSecret string
	contentType  string
	accept       string
	body         string
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{expiresIn: 3600}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, secret, _ := r.BasicAuth()
		body, _ := io.ReadAll(r.Body)

		ts.mu.Lock()
		ts.requests = append(ts.requests, tokenRequest{
			clientID:     id,
			clientSecret: secret,
			contentType:  r.Header.Get("Content-Type"),
			accept:       r.Header.Get("Accept"),
			body:         string(body),
		})
		count := len(ts.requests)
		expiresIn := ts.expiresIn
		ts.mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		require.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"access_token": "token-" + string(rune('0'+count)),
			"token_type":   "Bearer",
			"expires_in":   expiresIn,
		}))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) exchanges() []tokenRequest {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return append([]tokenRequest(nil), ts.requests...)
}

// clock is a manually advanced time source, so token expiry is tested without
// sleeping.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newCredentials(t *testing.T, ts *tokenServer, now *clock) *ClientCredentials {
	t.Helper()
	return &ClientCredentials{
		ClientID:     "the-id",
		ClientSecret: "the-secret",
		TokenURL:     ts.URL,
		Now:          now.Now,
	}
}

func authorizationOf(t *testing.T, c *ClientCredentials) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	require.NoError(t, c.Authenticate(req))
	return req.Header.Get("Authorization")
}

func TestClientCredentialsExchangesSecretForBearerToken(t *testing.T) {
	ts := newTokenServer(t)
	credentials := newCredentials(t, ts, &clock{now: time.Unix(0, 0)})

	assert.Equal(t, "Bearer token-1", authorizationOf(t, credentials))

	exchanges := ts.exchanges()
	require.Len(t, exchanges, 1)
	assert.Equal(t, "the-id", exchanges[0].clientID, "client id is the basic-auth user")
	assert.Equal(t, "the-secret", exchanges[0].clientSecret, "client secret is the basic-auth password")
	assert.Equal(t, "application/json", exchanges[0].accept)
	assert.Equal(t, "application/json", exchanges[0].contentType)
	assert.JSONEq(t, `{"grant_type":"client_credentials"}`, exchanges[0].body)
}

func TestClientCredentialsReusesTheTokenUntilItNearlyExpires(t *testing.T) {
	ts := newTokenServer(t)
	now := &clock{now: time.Unix(0, 0)}
	credentials := newCredentials(t, ts, now)

	assert.Equal(t, "Bearer token-1", authorizationOf(t, credentials))
	assert.Equal(t, "Bearer token-1", authorizationOf(t, credentials))
	now.Advance(time.Hour - 2*time.Minute) // still inside the hour, outside the skew
	assert.Equal(t, "Bearer token-1", authorizationOf(t, credentials))

	assert.Len(t, ts.exchanges(), 1, "a valid token must not be re-fetched")
}

// The token is refreshed a little before it actually expires, so a request does
// not set off with a token that dies in flight.
func TestClientCredentialsRefreshesWithinTheExpirySkew(t *testing.T) {
	ts := newTokenServer(t)
	now := &clock{now: time.Unix(0, 0)}
	credentials := newCredentials(t, ts, now)

	assert.Equal(t, "Bearer token-1", authorizationOf(t, credentials))
	now.Advance(time.Hour - 10*time.Second) // inside the 30s skew: treated as expired
	assert.Equal(t, "Bearer token-2", authorizationOf(t, credentials))

	assert.Len(t, ts.exchanges(), 2)
}

func TestClientCredentialsFetchesOneTokenUnderConcurrency(t *testing.T) {
	ts := newTokenServer(t)
	credentials := newCredentials(t, ts, &clock{now: time.Unix(0, 0)})

	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
			assert.NoError(t, credentials.Authenticate(req))
		})
	}
	wg.Wait()

	assert.Len(t, ts.exchanges(), 1, "concurrent callers must share one exchange")
}

func TestClientCredentialsReportsTokenEndpointFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
	}))
	t.Cleanup(server.Close)
	credentials := &ClientCredentials{ClientID: "id", ClientSecret: "bad", TokenURL: server.URL}

	req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	err := credentials.Authenticate(req)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "401")
	assert.Contains(t, err.Error(), "invalid_client")
	assert.Empty(t, req.Header.Get("Authorization"), "a failed exchange must not set a header")
}

// RTE documents token_type "Bearer", but the field is honoured rather than
// assumed, and defaults when a deployment omits it.
func TestClientCredentialsHonoursTokenType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"access_token":"abc","expires_in":3600}`)
	}))
	t.Cleanup(server.Close)
	credentials := &ClientCredentials{ClientID: "id", ClientSecret: "s", TokenURL: server.URL}

	assert.Equal(t, "Bearer abc", authorizationOf(t, credentials))
}
