// Package auth injects RTE credentials into outbound requests.
//
// Everything that talks to the RTE data platform goes through an Authenticator.
// Keeping credential injection behind that one interface means a domain package
// never knows how it was authenticated, and a test can swap in a fixed token
// instead of standing up an OAuth2 exchange.
package auth

import (
	"fmt"
	"net/http"
)

// Authenticator injects credentials into an outbound request.
//
// Implementations must be safe for concurrent use: one Authenticator is shared
// by every request a client makes. They receive the whole request rather than
// returning a token so that an implementation needing to fetch one can use
// r.Context() for cancellation, and so a future scheme can set something other
// than the Authorization header.
type Authenticator interface {
	Authenticate(r *http.Request) error
}

// StaticToken authenticates with a bearer token that was obtained elsewhere.
// Useful for tests, for short-lived tools, and for callers who already manage
// their own token lifecycle.
type StaticToken string

// Authenticate sets the Authorization header to the static bearer token.
func (t StaticToken) Authenticate(r *http.Request) error {
	r.Header.Set("Authorization", "Bearer "+string(t))
	return nil
}

// Transport is an http.RoundTripper that authenticates every request passing
// through it. Set it as an *http.Client's Transport and authentication becomes
// invisible to the rest of the code.
type Transport struct {
	// Authenticator injects the credentials. Required.
	Authenticator Authenticator
	// Base is the underlying RoundTripper; defaults to http.DefaultTransport.
	Base http.RoundTripper
}

// RoundTrip authenticates a copy of r and forwards it to the base transport.
func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if t.Authenticator == nil {
		return nil, fmt.Errorf("auth: transport has no authenticator")
	}
	// RoundTrip must not modify the request it is given, so authenticate a
	// clone: a retried request would otherwise carry a stale Authorization
	// header from the previous attempt.
	authenticated := r.Clone(r.Context())
	if err := t.Authenticator.Authenticate(authenticated); err != nil {
		return nil, fmt.Errorf("auth: authenticate %s %s: %w", r.Method, r.URL, err)
	}
	return t.base().RoundTrip(authenticated)
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}
