package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AuthenticatorFuncs is a hand-rolled fake Authenticator: it records how often
// it was called and delegates to the injected func.
type AuthenticatorFuncs struct {
	AuthenticateFunc  func(r *http.Request) error
	AuthenticateCalls int
}

func (a *AuthenticatorFuncs) Authenticate(r *http.Request) error {
	a.AuthenticateCalls++
	if a.AuthenticateFunc == nil {
		return errors.New("Authenticate not implemented")
	}
	return a.AuthenticateFunc(r)
}

// RoundTripperFunc adapts a func to http.RoundTripper so a test can stand in
// for the network without an httptest server.
type RoundTripperFunc func(r *http.Request) (*http.Response, error)

func (f RoundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestStaticTokenSetsBearerHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)

	require.NoError(t, StaticToken("abc123").Authenticate(req))

	assert.Equal(t, "Bearer abc123", req.Header.Get("Authorization"))
}

func TestTransportAuthenticatesEveryRequest(t *testing.T) {
	fake := &AuthenticatorFuncs{
		AuthenticateFunc: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer from-fake")
			return nil
		},
	}
	var seen string
	client := &http.Client{Transport: &Transport{
		Authenticator: fake,
		Base: RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			seen = r.Header.Get("Authorization")
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
		}),
	}}

	resp, err := client.Get("https://example.test/a")
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Equal(t, "Bearer from-fake", seen)
	assert.Equal(t, 1, fake.AuthenticateCalls)
}

func TestTransportAbortsWhenAuthenticationFails(t *testing.T) {
	wanted := errors.New("no credentials")
	fake := &AuthenticatorFuncs{
		AuthenticateFunc: func(r *http.Request) error { return wanted },
	}
	reached := false
	client := &http.Client{Transport: &Transport{
		Authenticator: fake,
		Base: RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			reached = true
			return nil, errors.New("must not be called")
		}),
	}}

	_, err := client.Get("https://example.test/a")

	require.Error(t, err)
	assert.ErrorIs(t, err, wanted)
	assert.False(t, reached, "the request must not reach the network unauthenticated")
	assert.Equal(t, 1, fake.AuthenticateCalls)
}

// RoundTrip must not mutate the caller's request: the http package documents
// that as a requirement, and a retried request would otherwise accumulate
// stale Authorization headers.
func TestTransportLeavesTheCallersRequestUntouched(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "https://example.test/", nil)
	transport := &Transport{
		Authenticator: StaticToken("abc123"),
		Base: RoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: http.StatusOK, Body: http.NoBody, Request: r}, nil
		}),
	}

	resp, err := transport.RoundTrip(req)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())

	assert.Empty(t, req.Header.Get("Authorization"))
}
