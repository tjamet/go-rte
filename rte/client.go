// Package rte is the top-level client for the RTE France open data platform.
// It wires an authenticator, an HTTP client and the per-API domain services
// into one value, so a caller supplies credentials and gets typed results.
//
//	client := rte.New(rte.Options{
//		ClientID:     os.Getenv("RTE_CLIENT_ID"),
//		ClientSecret: os.Getenv("RTE_CLIENT_SECRET"),
//	})
//	calendars, err := client.Tempo.Calendars(ctx, tempo.Query{})
//
// Credentials are obtained from the RTE portal at https://data.rte-france.com
// by registering an application and subscribing it to the APIs you need.
package rte

import (
	"net/http"

	"github.com/tjamet/go-rte/domains/tempo"
	"github.com/tjamet/go-rte/shared/auth"
	"github.com/tjamet/go-rte/shared/rtehttp"
)

// UserAgent identifies this library to the RTE platform.
const UserAgent = "go-rte/0.1 (+https://github.com/tjamet/go-rte)"

// Options configures a Client.
type Options struct {
	// ClientID and ClientSecret are the application credentials issued by the
	// RTE portal. Required by New; ignored by NewWithAuthenticator.
	ClientID     string
	ClientSecret string

	// BaseURL overrides the platform host. Defaults to rtehttp.BaseURL; set it
	// to point at a fake in tests.
	BaseURL string
	// TokenURL overrides the OAuth2 token endpoint. Defaults to auth.TokenURL.
	TokenURL string
	// HTTPClient is the underlying client for both the token exchange and the
	// API calls. Defaults to http.DefaultClient.
	HTTPClient *http.Client
	// UserAgent overrides the identifier sent to RTE.
	UserAgent string
}

// Client is an authenticated RTE data platform client exposing one service per
// API. Further RTE APIs become additional fields here.
type Client struct {
	// Auth injects credentials into every request.
	Auth auth.Authenticator
	// HTTP is the authenticated client the services share.
	HTTP *http.Client
	// API is the shared transport the services are built on.
	API *rtehttp.Client

	// Tempo reads the Tempo Like Supply Contract calendars.
	Tempo *tempo.Service
}

// New returns a Client that authenticates with the OAuth2 client-credentials
// grant, exchanging the id and secret for a bearer token as needed.
func New(o Options) *Client {
	return NewWithAuthenticator(&auth.ClientCredentials{
		ClientID:     o.ClientID,
		ClientSecret: o.ClientSecret,
		TokenURL:     o.TokenURL,
		// The token exchange uses the caller's plain HTTP client. It must not
		// use the authenticated one built below, or fetching a token would
		// require a token.
		HTTPClient: o.HTTPClient,
	}, o)
}

// NewWithAuthenticator returns a Client using the given Authenticator, for
// callers who already hold a token or who authenticate some other way.
func NewWithAuthenticator(authenticator auth.Authenticator, o Options) *Client {
	base := http.DefaultTransport
	if o.HTTPClient != nil && o.HTTPClient.Transport != nil {
		base = o.HTTPClient.Transport
	}

	httpClient := &http.Client{
		Transport: &auth.Transport{Authenticator: authenticator, Base: base},
	}
	if o.HTTPClient != nil {
		httpClient.Timeout = o.HTTPClient.Timeout
	}

	api := rtehttp.New(httpClient, o.BaseURL)
	api.UserAgent = UserAgent
	if o.UserAgent != "" {
		api.UserAgent = o.UserAgent
	}

	return &Client{
		Auth:  authenticator,
		HTTP:  httpClient,
		API:   api,
		Tempo: tempo.New(api),
	}
}
