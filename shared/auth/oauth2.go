package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// TokenURL is where the RTE data platform exchanges client credentials for an
// access token.
const TokenURL = "https://digital.iservices.rte-france.com/token/oauth"

// DefaultExpirySkew is how long before its stated expiry a token is treated as
// already expired, so a request does not set off with credentials that die in
// flight.
const DefaultExpirySkew = 30 * time.Second

// grantBody is the request RTE's token endpoint is given. The platform accepts
// JSON, which is what the rest of this client speaks.
const grantBody = `{"grant_type":"client_credentials"}`

// ClientCredentials authenticates with the OAuth2 client-credentials grant:
// the client id and secret are sent as HTTP Basic credentials to TokenURL, and
// the returned bearer token is cached until it is close to expiring.
//
// The zero value is not usable; ClientID, ClientSecret and TokenURL must be set.
// A ClientCredentials is safe for concurrent use.
type ClientCredentials struct {
	// ClientID is the basic-auth user. Required.
	ClientID string
	// ClientSecret is the basic-auth password. Required.
	ClientSecret string
	// TokenURL is the token endpoint; defaults to the package TokenURL constant.
	TokenURL string
	// HTTPClient performs the token exchange; defaults to http.DefaultClient.
	// It must not be a client whose Transport authenticates through this
	// ClientCredentials, or fetching a token would recurse.
	HTTPClient *http.Client
	// ExpirySkew is how early a token is considered expired; defaults to
	// DefaultExpirySkew.
	ExpirySkew time.Duration
	// Now reads the current time; defaults to time.Now. Injected so expiry can
	// be tested without sleeping.
	Now func() time.Time

	mu     sync.Mutex
	header string    // the cached "Bearer <token>" value
	expiry time.Time // when the cached token stops being usable
}

// tokenResponse is the token endpoint's JSON reply.
type tokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
}

// Authenticate sets the Authorization header, fetching or refreshing the token
// if the cached one is missing or close to expiring.
func (c *ClientCredentials) Authenticate(r *http.Request) error {
	header, err := c.authorization(r.Context())
	if err != nil {
		return err
	}
	r.Header.Set("Authorization", header)
	return nil
}

// authorization returns the Authorization header value to use, refreshing the
// cached token when needed.
//
// The lock is held across the exchange so that concurrent callers share a
// single token fetch rather than each starting their own.
func (c *ClientCredentials) authorization(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.header != "" && c.now().Before(c.expiry.Add(-c.expirySkew())) {
		return c.header, nil
	}

	token, err := c.exchange(ctx)
	if err != nil {
		return "", err
	}

	tokenType := token.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	c.header = tokenType + " " + token.AccessToken
	c.expiry = c.now().Add(time.Duration(token.ExpiresIn) * time.Second)
	return c.header, nil
}

// exchange performs the client-credentials grant against the token endpoint.
func (c *ClientCredentials) exchange(ctx context.Context) (tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.tokenURL(), strings.NewReader(grantBody))
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: build token request: %w", err)
	}
	req.SetBasicAuth(c.ClientID, c.ClientSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: token request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return tokenResponse{}, fmt.Errorf("auth: read token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return tokenResponse{}, fmt.Errorf("auth: token endpoint returned %d %s: %s",
			resp.StatusCode, http.StatusText(resp.StatusCode), bytes.TrimSpace(body))
	}

	var token tokenResponse
	if err := json.Unmarshal(body, &token); err != nil {
		return tokenResponse{}, fmt.Errorf("auth: decode token response: %w", err)
	}
	if token.AccessToken == "" {
		return tokenResponse{}, fmt.Errorf("auth: token endpoint returned no access_token")
	}
	return token, nil
}

func (c *ClientCredentials) tokenURL() string {
	if c.TokenURL != "" {
		return c.TokenURL
	}
	return TokenURL
}

func (c *ClientCredentials) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *ClientCredentials) expirySkew() time.Duration {
	if c.ExpirySkew > 0 {
		return c.ExpirySkew
	}
	return DefaultExpirySkew
}

func (c *ClientCredentials) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
