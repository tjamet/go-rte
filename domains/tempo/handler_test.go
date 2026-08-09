package tempo

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// serve runs one request against a handler over the fake API.
func serve(t *testing.T, handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func newHandler(t *testing.T, api *fakeAPI, store Store) *Handler {
	t.Helper()
	return &Handler{Cache: newCache(t, api, store)}
}

func TestHandlerServesADay(t *testing.T) {
	handler := newHandler(t, newFakeAPI(t), newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2026/08/10", nil))

	require.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "application/json", got.Header().Get("Content-Type"))
	assert.JSONEq(t, `{
		"date":"2026-08-10",
		"colour":"`+string(colourFor(Date{2026, time.August, 10}))+`",
		"start":"2026-08-10T00:00:00+02:00",
		"end":"2026-08-11T00:00:00+02:00"
	}`, got.Body.String())
}

func TestHandlerMountsUnderAnyPrefix(t *testing.T) {
	handler := http.StripPrefix("/tempo", newHandler(t, newFakeAPI(t), newMemoryStore()))

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/tempo/2026/08/10", nil))

	assert.Equal(t, http.StatusOK, got.Code)
}

func TestHandlerRejectsPathsThatAreNotADay(t *testing.T) {
	handler := newHandler(t, newFakeAPI(t), newMemoryStore())

	for _, target := range []string{
		"/",
		"/2026",
		"/2026/08",
		"/2026/08/10/11",
		"/2026/8/10",
		"/2026/02/30",
		"/2026/13/01",
		"/yyyy/mm/dd",
	} {
		t.Run(target, func(t *testing.T) {
			got := serve(t, handler, httptest.NewRequest(http.MethodGet, target, nil))

			assert.Equal(t, http.StatusBadRequest, got.Code)
		})
	}
}

func TestHandlerRejectsADayThatCannotBeKnownYet(t *testing.T) {
	api := newFakeAPI(t)
	handler := newHandler(t, api, newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2026/08/12", nil))

	assert.Equal(t, http.StatusBadRequest, got.Code)
	assert.Zero(t, api.callCount())
}

func TestHandlerReportsAHoleAsNotFound(t *testing.T) {
	api := newFakeAPI(t)
	api.holes[Date{2026, time.March, 7}] = true
	handler := newHandler(t, api, newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2026/03/07", nil))

	assert.Equal(t, http.StatusNotFound, got.Code)
}

func TestHandlerReportsPreHistoryAsNotFound(t *testing.T) {
	handler := newHandler(t, newFakeAPI(t), newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2010/01/01", nil))

	assert.Equal(t, http.StatusNotFound, got.Code)
}

func TestHandlerReportsAnUpstreamFailureAsBadGateway(t *testing.T) {
	api := newFakeAPI(t)
	api.fail = http.StatusInternalServerError
	handler := newHandler(t, api, newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2026/08/10", nil))

	assert.Equal(t, http.StatusBadGateway, got.Code)
}

func TestHandlerReportsAGivenUpWaitAsGatewayTimeout(t *testing.T) {
	api := newFakeAPI(t)
	api.release = make(chan struct{})
	t.Cleanup(func() { close(api.release) })
	handler := newHandler(t, api, newMemoryStore())

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/2026/08/10", nil).WithContext(ctx)

	got := serve(t, handler, request)

	assert.Equal(t, http.StatusGatewayTimeout, got.Code)
}

func TestHandlerRefusesToBeWrittenTo(t *testing.T) {
	handler := newHandler(t, newFakeAPI(t), newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodPost, "/2026/08/10", nil))

	assert.Equal(t, http.StatusMethodNotAllowed, got.Code)
	assert.Equal(t, "GET, HEAD", got.Header().Get("Allow"))
}

// A published past day never changes, so it can be cached by anything between
// here and the client for as long as it likes.
func TestHandlerMarksAPastDayImmutable(t *testing.T) {
	handler := newHandler(t, newFakeAPI(t), newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2020/06/15", nil))

	require.Equal(t, http.StatusOK, got.Code)
	assert.Contains(t, got.Header().Get("Cache-Control"), "immutable")
}

func TestHandlerLetsTodayExpire(t *testing.T) {
	handler := newHandler(t, newFakeAPI(t), newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2026/08/10", nil))

	require.Equal(t, http.StatusOK, got.Code)
	assert.NotContains(t, got.Header().Get("Cache-Control"), "immutable",
		"today's colour is not yet beyond revision")
}

// A degraded-mode value may still be revised, so nothing downstream should keep
// it either.
func TestHandlerForbidsStoringAFallbackDay(t *testing.T) {
	api := newFakeAPI(t)
	api.fallbacks[Date{2026, time.August, 11}] = true
	handler := newHandler(t, api, newMemoryStore())

	got := serve(t, handler, httptest.NewRequest(http.MethodGet, "/2026/08/11", nil))

	require.Equal(t, http.StatusOK, got.Code)
	assert.Equal(t, "no-store", got.Header().Get("Cache-Control"))
	assert.Contains(t, got.Body.String(), `"fallback":true`)
}
