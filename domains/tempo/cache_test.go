package tempo

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/tjamet/go-rte/shared/rtehttp"
)

// fakeAPI stands in for the RTE calendar endpoint, generating days for whatever
// range is asked for and recording every call.
type fakeAPI struct {
	*httptest.Server

	// earliest is the bottom of the archive: days before it are not returned,
	// which is how RTE reports pre-history.
	earliest Date
	// holes are days inside the archive that come back with no value.
	holes map[Date]bool
	// fallbacks are days returned in degraded mode.
	fallbacks map[Date]bool
	// release, when non-nil, blocks each response until it is closed.
	release chan struct{}

	mu    sync.Mutex
	calls []window
	fail  int // status code to answer with instead, when non-zero
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{
		earliest:  Date{2014, time.September, 1},
		holes:     map[Date]bool{},
		fallbacks: map[Date]bool{},
	}
	api.Server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.Close)
	return api
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	paris, err := time.LoadLocation(parisZone)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	start, err := time.Parse(time.RFC3339, r.URL.Query().Get("start_date"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	end, err := time.Parse(time.RFC3339, r.URL.Query().Get("end_date"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// end_date is exclusive: it is the midnight after the last requested day.
	asked := window{Lo: DateOf(start, paris), Hi: DateOf(end, paris).Add(-1)}

	a.mu.Lock()
	a.calls = append(a.calls, asked)
	fail := a.fail
	a.mu.Unlock()

	if a.release != nil {
		<-a.release
	}
	if fail != 0 {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(fail)
		_, _ = w.Write([]byte(`{"error":"upstream is unwell"}`))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(a.payload(asked, paris))
}

func (a *fakeAPI) payload(asked window, paris *time.Location) []byte {
	type wireValue struct {
		StartDate string `json:"start_date"`
		EndDate   string `json:"end_date"`
		Value     string `json:"value"`
		Fallback  bool   `json:"fallback"`
	}
	values := []wireValue{}
	for d := asked.Lo; !d.After(asked.Hi); d = d.Add(1) {
		if d.Before(a.earliest) || a.holes[d] {
			continue
		}
		start, end := d.Range(paris)
		values = append(values, wireValue{
			StartDate: start.Format(time.RFC3339),
			EndDate:   end.Format(time.RFC3339),
			Value:     string(colourFor(d)),
			Fallback:  a.fallbacks[d],
		})
	}

	body, _ := json.Marshal(map[string]any{
		"tempo_like_calendars": map[string]any{"values": values},
	})
	return body
}

func (a *fakeAPI) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return len(a.calls)
}

func (a *fakeAPI) lastCall(t *testing.T) window {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	require.NotEmpty(t, a.calls)
	return a.calls[len(a.calls)-1]
}

// colourFor gives every day a deterministic colour, so a test can tell whether
// the day it got back is the day it asked for.
func colourFor(d Date) Colour {
	switch d.Day % 3 {
	case 0:
		return Blue
	case 1:
		return White
	default:
		return Red
	}
}

// memoryStore is a Store held in a map, so cache tests touch no disk.
type memoryStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	puts    int
}

func newMemoryStore() *memoryStore {
	return &memoryStore{objects: map[string][]byte{}}
}

func (s *memoryStore) Get(_ context.Context, key string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	if !ok {
		return nil, ErrNotCached
	}
	return data, nil
}

func (s *memoryStore) Put(_ context.Context, key string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
	s.puts++
	return nil
}

func (s *memoryStore) List(_ context.Context, prefix string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := []string{}
	for key := range s.objects {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			keys = append(keys, key)
		}
	}
	return keys, nil
}

func (s *memoryStore) has(key string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[key]
	return ok
}

func (s *memoryStore) object(t *testing.T, key string) []byte {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[key]
	require.True(t, ok, "expected %s to be cached", key)
	return data
}

// newCache wires a Cache onto the fake API and an in-memory store, with the
// clock stopped just after publication on 10 August 2026.
func newCache(t *testing.T, api *fakeAPI, store Store) *Cache {
	t.Helper()
	paris := parisLocation(t)
	return &Cache{
		Service: New(rtehttp.New(api.Client(), api.URL)),
		Store:   store,
		Now:     func() time.Time { return time.Date(2026, time.August, 10, 12, 0, 0, 0, paris) },
		Limiter: rate.NewLimiter(rate.Inf, 1),
	}
}

func TestCacheFetchesAWholeWindowToAnswerOneDay(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)

	got, err := cache.Day(context.Background(), Date{2026, time.August, 10})

	require.NoError(t, err)
	assert.Equal(t, colourFor(Date{2026, time.August, 10}), got.Colour)
	assert.Equal(t, 1, api.callCount())
	assert.Equal(t, 360, api.lastCall(t).days(), "one miss should fill the cache as far as one call allows")
	assert.True(t, store.has("2026/08/10.json"))
	assert.True(t, store.has("2025/12/25.json"), "days far from the request are cached too")
}

func TestCacheServesASecondDayOfTheSameWindowWithoutCallingOut(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)
	_, err := cache.Day(context.Background(), Date{2026, time.August, 10})
	require.NoError(t, err)

	got, err := cache.Day(context.Background(), Date{2026, time.June, 1})

	require.NoError(t, err)
	assert.Equal(t, colourFor(Date{2026, time.June, 1}), got.Colour)
	assert.Equal(t, 1, api.callCount(), "the day was already in the window that was fetched")
}

func TestCacheRefusesADayBeyondThePublicationHorizon(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)

	_, err := cache.Day(context.Background(), Date{2026, time.August, 12})

	assert.ErrorIs(t, err, ErrBeyondHorizon)
	assert.Zero(t, api.callCount(), "a day that cannot exist yet is never asked for")
}

func TestCacheServesTomorrowOncePublished(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)

	got, err := cache.Day(context.Background(), Date{2026, time.August, 11})

	require.NoError(t, err)
	assert.Equal(t, colourFor(Date{2026, time.August, 11}), got.Colour)
}

func TestCacheWithholdsTomorrowBeforeThePublicationHour(t *testing.T) {
	paris := parisLocation(t)
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)
	cache.Now = func() time.Time { return time.Date(2026, time.August, 10, 10, 59, 0, 0, paris) }

	_, err := cache.Day(context.Background(), Date{2026, time.August, 11})

	assert.ErrorIs(t, err, ErrBeyondHorizon)
}

// A degraded-mode value may still change, so it is served but never written.
func TestCacheServesAFallbackDayWithoutCachingIt(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	api.fallbacks[Date{2026, time.August, 11}] = true
	cache := newCache(t, api, store)

	got, err := cache.Day(context.Background(), Date{2026, time.August, 11})

	require.NoError(t, err)
	assert.True(t, got.Fallback)
	assert.False(t, store.has("2026/08/11.json"), "a provisional value must not become a final one")
}

func TestCacheRefetchesAFallbackDayEveryTime(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	api.fallbacks[Date{2026, time.August, 11}] = true
	cache := newCache(t, api, store)
	_, err := cache.Day(context.Background(), Date{2026, time.August, 11})
	require.NoError(t, err)

	_, err = cache.Day(context.Background(), Date{2026, time.August, 11})

	require.NoError(t, err)
	assert.Equal(t, 2, api.callCount(), "an uncached day is asked for again")
}

func TestCacheRecordsAHoleAsAbsentAndAsksAboutItOnce(t *testing.T) {
	hole := Date{2026, time.March, 7}
	api, store := newFakeAPI(t), newMemoryStore()
	api.holes[hole] = true
	cache := newCache(t, api, store)

	_, err := cache.Day(context.Background(), hole)

	assert.ErrorIs(t, err, ErrNoData)
	assert.JSONEq(t, `{"absent":true}`, string(store.object(t, hole.Key())))

	_, err = cache.Day(context.Background(), hole)

	assert.ErrorIs(t, err, ErrNoData)
	assert.Equal(t, 1, api.callCount(), "a known hole is not asked about again")
}

func TestCacheLearnsTheFloorFromALeadingGap(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)

	_, err := cache.Day(context.Background(), Date{2014, time.September, 10})

	require.NoError(t, err)
	assert.JSONEq(t, `{"earliest":"2014-09-01"}`, string(store.object(t, FloorKey)))
	assert.False(t, store.has("2014/08/31.json"),
		"pre-history is recorded as one floor, not as thousands of absent days")
}

func TestCacheAnswersBelowTheFloorWithoutCallingOut(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)
	_, err := cache.Day(context.Background(), Date{2014, time.September, 10})
	require.NoError(t, err)
	before := api.callCount()

	_, err = cache.Day(context.Background(), Date{2010, time.January, 1})

	assert.ErrorIs(t, err, ErrNoData)
	assert.Equal(t, before, api.callCount())
}

// The floor is in the store, so a fresh process picks it up rather than
// rediscovering it with a wasted call.
func TestCacheReadsTheFloorLeftByAPreviousProcess(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	require.NoError(t, store.Put(context.Background(), FloorKey, []byte(`{"earliest":"2014-09-01"}`)))
	cache := newCache(t, api, store)

	_, err := cache.Day(context.Background(), Date{2010, time.January, 1})

	assert.ErrorIs(t, err, ErrNoData)
	assert.Zero(t, api.callCount())
}

// A hit answers from the cache and then widens it, so a caller walking
// backwards through history keeps finding the days it is about to ask for.
func TestCacheWidensItselfBehindAHit(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)
	// The first miss fetches 2019-12-18 to 2020-12-11.
	_, err := cache.Day(context.Background(), Date{2020, time.June, 15})
	require.NoError(t, err)
	edge := Date{2019, time.December, 18}
	require.True(t, store.has(edge.Key()))

	got, err := cache.Day(context.Background(), edge)

	require.NoError(t, err)
	assert.Equal(t, colourFor(edge), got.Colour)
	assert.Eventually(t, func() bool { return store.has("2019/06/21.json") }, time.Second, time.Millisecond,
		"a hit at the edge should pull the neighbouring window in behind it")
}

// A window wholly below the archive teaches nothing about where the archive
// begins, so every day of it is marked absent instead. The point is that the
// second request is answered without calling out: otherwise a day nobody can
// answer would be asked about for ever.
func TestCacheRecordsAWindowBelowTheArchiveAsAbsent(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)

	_, err := cache.Day(context.Background(), Date{2012, time.January, 1})

	assert.ErrorIs(t, err, ErrNoData)
	assert.False(t, store.has(FloorKey), "an empty answer says nothing about where data starts")

	_, err = cache.Day(context.Background(), Date{2012, time.January, 1})

	assert.ErrorIs(t, err, ErrNoData)
	assert.Equal(t, 1, api.callCount())
}

func TestCacheCollapsesConcurrentMissesIntoOneCall(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	api.release = make(chan struct{})
	cache := newCache(t, api, store)

	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Twenty adjacent days, deep enough in the past that none of their
			// windows is clamped by the horizon: each plans a range one day
			// apart from its neighbour's, so matching on equality would start
			// twenty fetches and only matching on coverage collapses them.
			_, errs[i] = cache.Day(context.Background(), Date{2020, time.June, 15}.Add(-i))
		}()
	}
	// Let every goroutine reach the coordinator before the response is released.
	assert.Eventually(t, func() bool { return api.callCount() >= 1 }, time.Second, time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	close(api.release)
	wg.Wait()

	for i, err := range errs {
		assert.NoError(t, err, "request %d", i)
	}
	assert.Equal(t, 1, api.callCount(), "concurrent misses in one neighbourhood cost one call")
}

func TestCacheReturnsWhenTheCallerGivesUpButFinishesTheFill(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	api.release = make(chan struct{})
	cache := newCache(t, api, store)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := cache.Day(ctx, Date{2026, time.August, 10})

	assert.ErrorIs(t, err, context.DeadlineExceeded)

	close(api.release)
	assert.Eventually(t, func() bool { return store.has("2026/08/10.json") }, time.Second, time.Millisecond,
		"the fill continues for whoever asks next")
}

func TestCacheReportsAnUpstreamFailure(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	api.fail = http.StatusInternalServerError
	cache := newCache(t, api, store)

	_, err := cache.Day(context.Background(), Date{2026, time.August, 10})

	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrNoData, "an outage is not an answer about the day")
}

// A limiter with no tokens and no refill makes the wait unbounded, so the
// caller's own deadline is what ends it.
func TestCacheWaitsForTheRateLimiter(t *testing.T) {
	api, store := newFakeAPI(t), newMemoryStore()
	cache := newCache(t, api, store)
	cache.Limiter = rate.NewLimiter(0, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := cache.Day(ctx, Date{2026, time.August, 10})

	require.Error(t, err)
	assert.Zero(t, api.callCount(), "the limiter holds the call back")
}
