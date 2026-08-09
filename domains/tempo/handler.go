package tempo

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Handler serves the colour of one Tempo day at GET /YYYY/MM/DD.
//
// The path mirrors the cache layout, so a request maps onto a stored object
// without translation. Mount it anywhere with http.StripPrefix:
//
//	mux.Handle("/tempo/", http.StripPrefix("/tempo", &tempo.Handler{Cache: cache}))
type Handler struct {
	// Cache answers the requests. Required.
	Cache *Cache
}

// dayResponse is the body of a successful reply.
type dayResponse struct {
	Date   Date      `json:"date"`
	Colour Colour    `json:"colour"`
	Start  time.Time `json:"start"`
	End    time.Time `json:"end"`
	// Fallback marks a value RTE produced in degraded mode. It may still be
	// revised, so it is absent from the body of a settled day rather than false.
	Fallback bool `json:"fallback,omitempty"`
}

// Freshness directives. A published past day is beyond revision and can be kept
// by anything downstream for as long as it likes; today and tomorrow are not
// yet settled, and a degraded-mode value should not be kept at all.
const (
	immutableCacheControl = "public, max-age=31536000, immutable"
	unsettledCacheControl = "public, max-age=600"
	fallbackCacheControl  = "no-store"
)

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "only GET and HEAD are supported", http.StatusMethodNotAllowed)
		return
	}

	date, err := dateOfPath(r.URL.Path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	day, err := h.Cache.Day(r.Context(), date)
	if err != nil {
		http.Error(w, err.Error(), statusOf(err))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", h.freshness(date, day))
	if err := json.NewEncoder(w).Encode(dayResponse{
		Date:     date,
		Colour:   day.Colour,
		Start:    day.Start,
		End:      day.End,
		Fallback: day.Fallback,
	}); err != nil {
		// The status is already written, so there is nowhere left to report
		// this; the truncated body is what tells the client.
		return
	}
}

// dateOfPath reads the YYYY/MM/DD the request names.
func dateOfPath(urlPath string) (Date, error) {
	parts := strings.Split(strings.Trim(urlPath, "/"), "/")
	if len(parts) != 3 {
		return Date{}, errors.New("tempo: the path must be /YYYY/MM/DD")
	}
	return ParseDate(parts[0], parts[1], parts[2])
}

// statusOf maps a failure onto the status that describes it.
//
// A day beyond the horizon is the caller's mistake rather than a gap in the
// data: it does not exist yet and asking again will not help until tomorrow.
// A day RTE has no colour for — before the archive, or a hole inside it — is
// genuinely not found.
func statusOf(err error) int {
	switch {
	case errors.Is(err, ErrBeyondHorizon):
		return http.StatusBadRequest
	case errors.Is(err, ErrNoData):
		return http.StatusNotFound
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

// freshness says how long the answer may be kept downstream.
func (h *Handler) freshness(date Date, day Day) string {
	if day.Fallback {
		return fallbackCacheControl
	}
	today, err := h.Cache.today()
	if err != nil || !date.Before(today) {
		return unsettledCacheControl
	}
	return immutableCacheControl
}
