package rtehttp

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// recordingServer captures the request the client actually sent and replies
// with a canned response.
type recordingServer struct {
	*httptest.Server
	path        string
	rawQuery    string
	accept      string
	userAgent   string
	statusCode  int
	contentType string
	body        string
}

func newRecordingServer(t *testing.T) *recordingServer {
	t.Helper()
	rs := &recordingServer{statusCode: http.StatusOK, contentType: "application/json", body: `{"ok":true}`}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.path = r.URL.Path
		rs.rawQuery = r.URL.RawQuery
		rs.accept = r.Header.Get("Accept")
		rs.userAgent = r.Header.Get("User-Agent")

		w.Header().Set("Content-Type", rs.contentType)
		w.WriteHeader(rs.statusCode)
		_, _ = io.WriteString(w, rs.body)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func newClient(t *testing.T, rs *recordingServer) *Client {
	t.Helper()
	return New(rs.Client(), rs.URL)
}

func TestGetJoinsPathAndReturnsBody(t *testing.T) {
	rs := newRecordingServer(t)

	resp, err := newClient(t, rs).Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/open_api/tempo_like_supply_contract/v1/tempo_like_calendars",
	})

	require.NoError(t, err)
	assert.Equal(t, "/open_api/tempo_like_supply_contract/v1/tempo_like_calendars", rs.path)
	assert.Equal(t, "application/json", rs.accept)
	assert.Equal(t, `{"ok":true}`, string(resp.Body))
	assert.Equal(t, "application/json", resp.ContentType)
}

// An RFC3339 offset contains a '+', which must reach the server percent-encoded.
// Sent raw it is decoded as a space and the date filter silently misreads.
func TestQueryEncodesTheOffsetPlusSign(t *testing.T) {
	rs := newRecordingServer(t)

	_, err := newClient(t, rs).Do(context.Background(), Request{
		Method: http.MethodGet,
		Path:   "/v1/tempo_like_calendars",
		Query:  url.Values{"start_date": {"2026-08-05T00:00:00+02:00"}},
	})

	require.NoError(t, err)
	assert.Contains(t, rs.rawQuery, "%2B02%3A00")
	assert.NotContains(t, rs.rawQuery, "+02:00")
}

func TestUserAgentIsSent(t *testing.T) {
	rs := newRecordingServer(t)
	client := newClient(t, rs)
	client.UserAgent = "go-rte-test/1.0"

	_, err := client.Do(context.Background(), Request{Method: http.MethodGet, Path: "/v1/x"})

	require.NoError(t, err)
	assert.Equal(t, "go-rte-test/1.0", rs.userAgent)
}

func expectAPIError(t *testing.T, rs *recordingServer, check func(t *testing.T, err *APIError)) {
	t.Helper()
	_, err := newClient(t, rs).Do(context.Background(), Request{Method: http.MethodGet, Path: "/v1/x"})
	require.Error(t, err)
	var apiErr *APIError
	require.ErrorAs(t, err, &apiErr)
	check(t, apiErr)
}

func TestErrorBodyMapsToTypedAPIError(t *testing.T) {
	rs := newRecordingServer(t)
	rs.statusCode = http.StatusBadRequest
	rs.body = `{"error":"TMPLIKSUPCON_TMPLIKCAL_F01",` +
		`"error_description":"both dates are mandatory",` +
		`"error_uri":"https://data.rte-france.com",` +
		`"error_details":{"transaction_id":"tx-42"}}`

	expectAPIError(t, rs, func(t *testing.T, err *APIError) {
		assert.Equal(t, http.StatusBadRequest, err.StatusCode)
		assert.Equal(t, "TMPLIKSUPCON_TMPLIKCAL_F01", err.Code)
		assert.Equal(t, "both dates are mandatory", err.Description)
		assert.Equal(t, "https://data.rte-france.com", err.URI)
		assert.Equal(t, "tx-42", err.TransactionID)
		// The transaction id is what RTE support asks for, so it must be visible.
		assert.Contains(t, err.Error(), "tx-42")
		assert.Contains(t, err.Error(), "TMPLIKSUPCON_TMPLIKCAL_F01")
	})
}

func TestCodeExtractsTheBusinessCodeFromAnyError(t *testing.T) {
	rs := newRecordingServer(t)
	rs.statusCode = http.StatusBadRequest
	rs.body = `{"error":"TMPLIKSUPCON_TMPLIKCAL_F03"}`

	_, err := newClient(t, rs).Do(context.Background(), Request{Method: http.MethodGet, Path: "/v1/x"})

	assert.Equal(t, "TMPLIKSUPCON_TMPLIKCAL_F03", Code(err))
	assert.Empty(t, Code(nil), "a nil error carries no code")
	assert.Empty(t, Code(io.EOF), "a non-API error carries no code")
}

// The gateway answers some failures in XML rather than JSON.
func TestXMLErrorBodyIsParsed(t *testing.T) {
	rs := newRecordingServer(t)
	rs.statusCode = http.StatusForbidden
	rs.contentType = "application/xml"
	rs.body = `<?xml version="1.0"?><error><error>ACCESS_DENIED</error>` +
		`<error_description>no subscription</error_description>` +
		`<error_details><transaction_id>tx-7</transaction_id></error_details></error>`

	expectAPIError(t, rs, func(t *testing.T, err *APIError) {
		assert.Equal(t, "ACCESS_DENIED", err.Code)
		assert.Equal(t, "no subscription", err.Description)
		assert.Equal(t, "tx-7", err.TransactionID)
	})
}

// An unparseable body must still surface the status and the raw payload rather
// than being swallowed.
func TestUnparseableErrorBodyIsPreservedVerbatim(t *testing.T) {
	rs := newRecordingServer(t)
	rs.statusCode = http.StatusBadGateway
	rs.contentType = "text/html"
	rs.body = "<html>gateway exploded</html>"

	expectAPIError(t, rs, func(t *testing.T, err *APIError) {
		assert.Equal(t, http.StatusBadGateway, err.StatusCode)
		assert.Empty(t, err.Code)
		assert.Contains(t, err.Body, "gateway exploded")
		assert.Contains(t, err.Error(), "502")
	})
}

func TestContextCancellationIsReported(t *testing.T) {
	rs := newRecordingServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newClient(t, rs).Do(ctx, Request{Method: http.MethodGet, Path: "/v1/x"})

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}
