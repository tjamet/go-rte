// Package rtehttp is the HTTP plumbing shared by every RTE domain package: it
// joins paths onto the platform base URL, encodes query parameters, and turns a
// non-2xx reply into a typed APIError.
//
// It deliberately returns the response body as bytes rather than decoding it.
// The Tempo endpoint can answer in either JSON or XML, and which decoder to use
// — and what to decode into — is a domain concern, not a transport one.
package rtehttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// BaseURL is the RTE data platform host that serves every open API.
const BaseURL = "https://digital.iservices.rte-france.com"

// Client performs requests against the RTE data platform.
//
// The *http.Client it wraps is expected to authenticate on its own, normally by
// using auth.Transport as its Transport, so nothing here deals with credentials.
type Client struct {
	// HTTP performs the requests; defaults to http.DefaultClient.
	HTTP *http.Client
	// BaseURL is the scheme and host, without a trailing slash.
	BaseURL string
	// UserAgent identifies this client to RTE; sent when non-empty.
	UserAgent string
}

// New returns a Client for the given base URL. An empty baseURL means the RTE
// platform.
func New(httpClient *http.Client, baseURL string) *Client {
	if baseURL == "" {
		baseURL = BaseURL
	}
	return &Client{HTTP: httpClient, BaseURL: strings.TrimRight(baseURL, "/")}
}

// Request describes one call to the platform.
type Request struct {
	Method  string
	Path    string     // joined onto BaseURL, leading slash included
	Query   url.Values // encoded onto the URL; may be nil
	Headers map[string]string
}

// Response is a successful reply, left undecoded for the caller.
type Response struct {
	StatusCode  int
	ContentType string
	Body        []byte
}

// Do performs r and returns the response body. A non-2xx status is returned as
// an *APIError rather than a Response.
func (c *Client) Do(ctx context.Context, r Request) (*Response, error) {
	endpoint := c.BaseURL + r.Path
	if len(r.Query) > 0 {
		// url.Values.Encode percent-encodes the '+' of an RFC3339 offset. Left
		// raw, the server would read it as a space and misparse the date.
		endpoint += "?" + r.Query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, r.Method, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("rte: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		req.Header.Set("User-Agent", c.UserAgent)
	}
	for name, value := range r.Headers {
		req.Header.Set(name, value)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("rte: %s %s: %w", r.Method, endpoint, err)
	}
	defer func() { _ = resp.Body.Close() }() // already fully read below; close error carries nothing actionable

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("rte: read %s %s: %w", r.Method, endpoint, err)
	}

	contentType := resp.Header.Get("Content-Type")
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, newAPIError(r.Method, endpoint, resp, contentType, body)
	}
	return &Response{StatusCode: resp.StatusCode, ContentType: contentType, Body: body}, nil
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// IsXML reports whether the response is one of the XML media types. The Tempo
// endpoint publishes a legacy XML representation alongside the JSON one.
func (r *Response) IsXML() bool { return isXML(r.ContentType) }

func isXML(contentType string) bool {
	mediaType, _, _ := strings.Cut(contentType, ";")
	mediaType = strings.TrimSpace(strings.ToLower(mediaType))
	return mediaType == "application/xml" || mediaType == "text/xml" ||
		strings.HasSuffix(mediaType, "+xml")
}
