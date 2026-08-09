package tempo

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/tjamet/go-rte/shared/rtehttp"
)

// Errors returned before a request is made, when the query cannot be valid.
// RTE would reject these too (as F01 and F02), but failing here saves the round
// trip and says what is wrong in the caller's own terms.
var (
	// ErrDateRangeIncomplete means Start was given without End, or the reverse.
	ErrDateRangeIncomplete = errors.New("tempo: start and end must be given together or not at all")
	// ErrDateRangeInverted means Start is after End.
	ErrDateRangeInverted = errors.New("tempo: start is after end")
)

// Service reads Tempo calendars from the RTE data platform.
type Service struct {
	Client *rtehttp.Client
}

// New returns a Service backed by the given HTTP client.
func New(client *rtehttp.Client) *Service {
	return &Service{Client: client}
}

// Query selects which Tempo days to return.
//
// The zero Query asks for RTE's default window. Start and End must be supplied
// together or not at all; the API rejects a half-specified range.
type Query struct {
	Start time.Time
	End   time.Time
	// FallbackStatus filters on degraded mode when non-nil.
	FallbackStatus *bool
}

// Validate reports whether the query can be sent.
func (q Query) Validate() error {
	if q.Start.IsZero() != q.End.IsZero() {
		return ErrDateRangeIncomplete
	}
	if !q.Start.IsZero() && q.Start.After(q.End) {
		return ErrDateRangeInverted
	}
	return nil
}

// values renders the query as URL parameters, omitting anything unset.
func (q Query) values() url.Values {
	values := url.Values{}
	if !q.Start.IsZero() {
		values.Set("start_date", q.Start.Format(time.RFC3339))
		values.Set("end_date", q.End.Format(time.RFC3339))
	}
	if q.FallbackStatus != nil {
		values.Set("fallback_status", strconv.FormatBool(*q.FallbackStatus))
	}
	return values
}

// Calendars returns the Tempo days matching q.
//
// A non-2xx reply comes back as an *rtehttp.APIError; because RTE reports every
// business failure as HTTP 400, use rtehttp.Code(err) and the Code* constants
// to tell them apart.
func (s *Service) Calendars(ctx context.Context, q Query) (Calendars, error) {
	if err := q.Validate(); err != nil {
		return nil, err
	}

	resp, err := s.Client.Do(ctx, rtehttp.Request{
		Method: http.MethodGet,
		Path:   CalendarsPath,
		Query:  q.values(),
	})
	if err != nil {
		return nil, err
	}

	calendars, err := decodeJSONCalendars(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("tempo: %w", err)
	}
	return calendars, nil
}
