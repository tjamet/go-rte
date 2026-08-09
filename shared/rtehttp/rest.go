package rtehttp

import (
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// APIError is a non-2xx reply from the RTE data platform.
//
// The platform reports every business failure of an endpoint under a single
// HTTP status — the Tempo calendar API returns seven distinct conditions as
// 400 — so the discriminator callers should branch on is Code, not StatusCode.
type APIError struct {
	Method     string
	URL        string
	StatusCode int
	Status     string

	// Code is RTE's business error code, e.g. TMPLIKSUPCON_TMPLIKCAL_F01.
	Code string
	// Description is the human-readable explanation.
	Description string
	// URI points at the documentation for the failure.
	URI string
	// TransactionID identifies the call in RTE's logs. Quote it when reporting
	// an incident to RTE support.
	TransactionID string
	// Body is the raw payload, kept so nothing is lost when it does not parse.
	Body string
}

// errorPayload is the error schema Swagger.json defines. The same field names
// are used by the JSON and the XML representations.
type errorPayload struct {
	Error       string `json:"error" xml:"error"`
	Description string `json:"error_description" xml:"error_description"`
	URI         string `json:"error_uri" xml:"error_uri"`
	Details     struct {
		TransactionID string `json:"transaction_id" xml:"transaction_id"`
	} `json:"error_details" xml:"error_details"`
}

// Error implements error.
func (e *APIError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "rte: %s %s: %d %s", e.Method, e.URL, e.StatusCode, e.Status)
	if e.Code != "" {
		fmt.Fprintf(&b, ": %s", e.Code)
	}
	if e.Description != "" {
		fmt.Fprintf(&b, ": %s", e.Description)
	}
	if e.TransactionID != "" {
		fmt.Fprintf(&b, " (transaction %s)", e.TransactionID)
	}
	if e.Code == "" && e.Description == "" && e.Body != "" {
		fmt.Fprintf(&b, ": %s", truncate(e.Body, 300))
	}
	return b.String()
}

// newAPIError builds an APIError from a non-2xx response, parsing the payload
// as JSON or XML according to the content type and falling back to the raw body.
func newAPIError(method, endpoint string, resp *http.Response, contentType string, body []byte) *APIError {
	apiErr := &APIError{
		Method:     method,
		URL:        endpoint,
		StatusCode: resp.StatusCode,
		Status:     http.StatusText(resp.StatusCode),
		Body:       string(body),
	}

	var payload errorPayload
	unmarshal := json.Unmarshal
	if isXML(contentType) {
		unmarshal = xml.Unmarshal
	}
	if err := unmarshal(body, &payload); err == nil {
		apiErr.Code = payload.Error
		apiErr.Description = payload.Description
		apiErr.URI = payload.URI
		apiErr.TransactionID = payload.Details.TransactionID
	}
	return apiErr
}

// Code returns the RTE business error code carried by err, or "" if err is nil
// or is not an *APIError. It saves every caller writing the errors.As dance:
//
//	if rtehttp.Code(err) == tempo.CodePeriodTooLong { ... }
func Code(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return ""
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
